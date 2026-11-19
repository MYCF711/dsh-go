package nodeprovider

import (
	"context"
	"fmt"
	"time"

	"dshgo/internal/ptc"
)

// Run 执行一次程序。
//
// 返回约定与官方一致：**程序层面的失败写在 RunResult.Error 里**，不返回 error；
// 只有调用方误用（RunSpec 不完整、bindings 非法、同一 stateOwner 并发运行）才
// 返回 error。
//
// 机器上没有 node 时也不返回 error：那是一次"底座建不起来"的运行失败
// （kind=`worker-exit`，message 点名 node），这样上层不需要两套错误处理——
// 但它同时把 ErrNodeUnavailable 串在 message 里，便于诊断区分。
//
// 进程归属按官方两条路走（`src/execution.ts:84-112`）：
//
//   - `stateOwner` 为 nil 或不保留状态 → 一次运行一个全新进程；
//   - 保留状态（vm-script + scriptPersistence）且给了 owner → 复用该 owner 的
//     常驻进程；同一 owner 上并发运行是**误用**（官方 `worker?.assertIdle()`）。
func (p *Provider) Run(ctx context.Context, spec ptc.RunSpec) (ptc.RunResult, error) {
	if err := ptc.ValidateBindings(spec.Bindings); err != nil {
		return ptc.RunResult{}, err
	}
	if spec.Cwd == "" {
		return ptc.RunResult{}, ptc.Misuse("RunSpec.Cwd 为空：run 不补缺省值（缺省只在 resolve 里补）")
	}
	if spec.TimeoutMs != nil && *spec.TimeoutMs <= 0 {
		return ptc.RunResult{}, ptc.Misuse("RunSpec.TimeoutMs 必须是正数毫秒或 nil（收到 %d）", *spec.TimeoutMs)
	}

	started := time.Now()
	if !p.retains || spec.StateOwner == nil {
		return p.runTemporary(ctx, spec, started)
	}
	key, err := stateOwnerKey(spec.StateOwner)
	if err != nil {
		return ptc.RunResult{}, err
	}
	return p.runRetained(ctx, key, spec, started)
}

// runTemporary 起一个全新进程跑这一轮，结束/超时/取消都收整棵进程树。
func (p *Provider) runTemporary(ctx context.Context, spec ptc.RunSpec, started time.Time) (ptc.RunResult, error) {
	nodePath, err := p.resolveNode(ctx)
	if err != nil {
		return ptc.Fail(ptc.FailureWorkerExit, nodeUnavailableMessage(err), nil), nil
	}
	sess, err := p.start(ctx, nodePath, spec, false)
	if err != nil {
		return ptc.Fail(classifyStartFailure(ctx, spec),
			fmt.Sprintf("起受管 node 进程失败：%v", err), nil), nil
	}
	defer sess.close()
	state := &runState{}
	return sess.execute(ctx, spec, started, state), nil
}

// runRetained 复用（或新建）某个 owner 的常驻进程。
//
// 失败归类与一次性运行同源：起不来仍然是 `worker-exit`/`timeout`/`abort`，
// 但"同一 owner 并发运行""owner 身份不合法"是误用，走 error 返回。
func (p *Provider) runRetained(ctx context.Context, key string, spec ptc.RunSpec, started time.Time) (ptc.RunResult, error) {
	sess, err := p.acquireRetained(ctx, key, spec)
	if err != nil {
		if ptc.IsMisuse(err) {
			return ptc.RunResult{}, err
		}
		return ptc.Fail(classifyStartFailure(ctx, spec),
			fmt.Sprintf("起受管 node 进程失败：%v", err), nil), nil
	}
	defer sess.busy.Store(false)
	state := &runState{}
	result := sess.execute(ctx, spec, started, state)
	if keepRetained(result) {
		return result, nil
	}
	// 官方 worker.ts:161-173：进程被杀/输出超限时保留状态随之清掉，并在中断诊断后
	// 补一句"保留变量是保留还是清掉"。
	p.dropRetained(key, sess)
	return markRetainedCleared(result), nil
}

// nodeUnavailableMessage 把"机器上没 node"说成一句能指导修复的话。
func nodeUnavailableMessage(err error) string {
	message := "外部 Node 不可用：" + err.Error()
	if err == ErrNodeUnavailable || err.Error() == ErrNodeUnavailable.Error() {
		message += "（装 Node.js 并确保 `node --version` 可用；宿主不会用内置引擎假装能跑）"
	}
	return message
}

// classifyStartFailure 把"进程没起来"归到官方八种 kind 里最贴近的一种。
func classifyStartFailure(ctx context.Context, spec ptc.RunSpec) ptc.FailureKind {
	if ctx.Err() != nil {
		return ptc.FailureAbort
	}
	if spec.TimeoutMs != nil {
		return ptc.FailureTimeout
	}
	return ptc.FailureWorkerExit
}
