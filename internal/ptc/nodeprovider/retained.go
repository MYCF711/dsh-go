package nodeprovider

import (
	"context"

	"dshgo/internal/ptc"
)

// 这个文件只管一件事：**按 stateOwner 常驻的会话**（官方 `scriptPersistence: true`）。
//
// 官方形状（`src/execution.ts:88-112`、`src/worker.ts:75-99`）：
//
//   - owner 的进程在 `owners` 里查找，命中就复用（顶层声明、函数、对象身份随之保留）；
//   - 同一 owner 上并发运行被拒（`assertIdle`），不是排队也不是静默串行；
//   - 沙盒策略变了必须**先释放**再换环境；
//   - `release(owner)` 释放某个 owner 的进程并等清理。
//
// 这里的差异只有一处，且是保守方向：官方在超时/取消时先尝试**合作停止**
// （`interruptEvaluation`：revoke 在途绑定调用并等信息落定），成功才保留变量；
// 本 provider 的单线程 worker 没有合作停止通道，所以超时/取消**一律终止进程树**
// 并清掉保留状态——结果消息里写明"retained variables were cleared"，不假装保留。

// keepRetained 报告一次求值之后常驻进程是否还能继续用。
//
// 官方 worker.ts:163：`preserved = retainsState && !discard && kind != 'output-limit'`；
// discard 覆盖超时、取消与输出超限。这里等价地写成白名单：
// 只有"程序正常结束"和"程序自己抛了 / 完成值不是无损 JSON"保留进程——
// 这两种情况下进程本身是健康的，只是这一次求值不成功。
func keepRetained(result ptc.RunResult) bool {
	if !result.Failed() {
		return true
	}
	switch result.Error.Kind {
	case ptc.FailureException, ptc.FailureInvalidOutput:
		return true
	}
	return false
}

// markRetainedCleared 在中断诊断后面补一句保留状态的下场（官方 worker.ts:167-169）。
func markRetainedCleared(result ptc.RunResult) ptc.RunResult {
	if result.Error == nil {
		return result
	}
	switch result.Error.Kind {
	case ptc.FailureTimeout, ptc.FailureAbort:
		result.Error = &ptc.RunFailure{
			Kind:    result.Error.Kind,
			Message: result.Error.Message + "; retained variables were cleared (this state owner's process was terminated)",
		}
	}
	return result
}

// acquireRetained 拿到某个 owner 的常驻会话（必要时新建），并把它标成"在用"。
//
// 锁覆盖起进程那一小段：常驻模式本来就是"一个 owner 一个进程"，这点串行换的是
// 简单明确的单飞语义，不是热路径。
func (p *Provider) acquireRetained(ctx context.Context, key string, spec ptc.RunSpec) (*session, error) {
	p.retainedMu.Lock()
	defer p.retainedMu.Unlock()
	if p.closed {
		return nil, ptc.Misuse("provider 已关闭：不再接受运行")
	}
	if existing, ok := p.retained[key]; ok {
		if existing.isClosed() {
			delete(p.retained, key)
		} else {
			if !existing.busy.CompareAndSwap(false, true) {
				return nil, ptc.Misuse("同一个 stateOwner 上已有一次运行在途：官方形状是单飞（worker.ts:76-79 assertIdle），不是排队")
			}
			return existing, nil
		}
	}
	nodePath, err := p.resolveNode(ctx)
	if err != nil {
		return nil, err
	}
	sess, err := p.start(ctx, nodePath, spec, true)
	if err != nil {
		return nil, err
	}
	sess.busy.Store(true)
	p.retained[key] = sess
	return sess, nil
}

// dropRetained 把一个 owner 的会话从账上摘掉并终止（失败/超限之后清状态）。
func (p *Provider) dropRetained(key string, sess *session) {
	p.retainedMu.Lock()
	if current, ok := p.retained[key]; ok && current == sess {
		delete(p.retained, key)
	}
	p.retainedMu.Unlock()
	sess.close()
}

// releaseKey 是 Release 的实现：摘掉并终止这个 owner 的会话。
func (p *Provider) releaseKey(key string) {
	p.retainedMu.Lock()
	sess := p.retained[key]
	delete(p.retained, key)
	p.retainedMu.Unlock()
	if sess != nil {
		sess.close()
	}
}
