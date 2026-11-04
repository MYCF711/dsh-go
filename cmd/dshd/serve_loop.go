package main

import "context"

// modelOverrideLoopRunner 给默认 loopRunner 补上每轮模型解析。
//
// 每轮构造一个轻量 loopRunner 副本，而不是改写共享 base.model：不同会话可以
// 并发跑，后开始的 B 绝不能覆盖 A 即将使用的 --model。jobsMu/jobs 不复制；
// 后台任务仍由原 base 与 broker 的回调维护，journal、审批与推送出口保持原样。
//
// **每加一个"每轮透传给子进程"的字段，都必须在这里补一行**：
// 这个副本是逐字段拷的，漏一个就会让**某一轮**静默退回默认值——
// 比如漏了 toolsMode，就只有"走 modelOverrideLoopRunner 的那条路"不带
// --tools-mode，而 run 那条路带着；两条路的表不一样，且从外面完全看不出来。
// TestServeForwardsToolsModeToTurnChild 钉住的就是这一格。
type modelOverrideLoopRunner struct {
	base *loopRunner
}

func (r *modelOverrideLoopRunner) Run(ctx context.Context, sessionID, turnID, text string) error {
	model, err := resolveServeModel(r.base.home, sessionID, r.base.model)
	if err != nil {
		r.base.emitTurn(sessionID, turnID, err)
		return err
	}
	next := &loopRunner{
		journals:  r.base.journals,
		broker:    r.base.broker,
		model:     model,
		reasoning: r.base.reasoning,
		pub:       r.base.pub,
		home:      r.base.home,
		policy:    r.base.policy,
		maxSteps:  r.base.maxSteps,
		toolsMode: r.base.toolsMode,
	}
	return next.Run(ctx, sessionID, turnID, text)
}
