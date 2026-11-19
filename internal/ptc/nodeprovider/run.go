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
// 只有调用方误用（RunSpec 不完整、bindings 非法）才返回 error。
//
// 机器上没有 node 时也不返回 error：那是一次"底座建不起来"的运行失败
// （kind=`worker-exit`，message 点名 node），这样上层不需要两套错误处理——
// 但它同时把 ErrNodeUnavailable 串在 message 里，便于诊断区分。
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
	nodePath, err := p.resolveNode(ctx)
	if err != nil {
		message := "外部 Node 不可用：" + err.Error()
		if err == ErrNodeUnavailable || err.Error() == ErrNodeUnavailable.Error() {
			message += "（装 Node.js 并确保 `node --version` 可用；宿主不会用内置引擎假装能跑）"
		}
		return ptc.Fail(ptc.FailureWorkerExit, message, nil), nil
	}

	session, err := p.start(ctx, nodePath, spec)
	if err != nil {
		kind := ptc.FailureWorkerExit
		if ctx.Err() != nil {
			kind = ptc.FailureAbort
		} else if spec.TimeoutMs != nil {
			kind = ptc.FailureTimeout
		}
		return ptc.Fail(kind, fmt.Sprintf("起受管 node 进程失败：%v", err), nil), nil
	}
	defer session.close()

	return session.execute(ctx, spec, started), nil
}
