package actioncards

// Kind.Act 的会话上下文接缝。
//
// # 为什么需要它
//
// 官方 `actionCards` 把 kind 的回调挂在**插件上下文**上：
//
//	act: (agent, proposal, verb, payload) => …
//
// kind 要判断"这张卡现在是什么状态"时，自己就能 `ctx.actionCards.view(agent.session)`
// 读回当前 decision。本宿主的 `Kind.Act` 只有
// `(ctx, proposal, verb, payload)`——没有 agent、没有 sessionId、也没有当前
// decision。对于只做"从 proposal.details 读参数"的 kind（`enable_official_plugin`）
// 这够用；但对官方 `create_personalized_page` 的 `undo` / `restore` 就不够：
// 这两个动作必须读回**之前记下的 bundleName 与 phase**（官方
// `creation-recommendations/src/index.ts` 的 `decide()` 里 `this.cardOf(agent, …)`）。
//
// 所以 Service 在调用 `kind.Act` 前，把"这次动作属于哪个会话、当前 decision 是什么"
// 放进它传下去的 context。这是**加法**：既有的 kind 忽略这些值即可，签名不变，
// 也不会各自去 `actioncards.Open(home)` 里再猜一遍状态（那会绕开 Service 已读过的
// 同一份投影，出现"两次动作看到两个 phase"）。
//
// 事实源仍然是会话 v4：`Decision` 来自 `Service.Act` 刚折叠出来的 `Project(events)`，
// 不是内存里的第二份状态。

import "context"

// ActContext 是一次 kind 动作的会话上下文。
type ActContext struct {
	// SessionID 是发起动作的会话（官方 `agent.session`）。
	SessionID string
	// Proposal 是这张卡的提案（与 Kind.Act 的 proposal 参数同值，方便一路传）。
	Proposal Proposal
	// Decision 是这张卡**当前**已经记录的 decision；HasDecision 为 false 时无意义。
	Decision Decision
	// HasDecision 表示这张卡之前是否已记录过 decision。
	HasDecision bool
}

type actContextKey struct{}

// WithActContext 把动作上下文挂到 ctx 上（Service 内部使用）。
func WithActContext(ctx context.Context, value ActContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actContextKey{}, value)
}

// ActContextFrom 读取 Service 传下来的动作上下文。
func ActContextFrom(ctx context.Context) (ActContext, bool) {
	if ctx == nil {
		return ActContext{}, false
	}
	value, ok := ctx.Value(actContextKey{}).(ActContext)
	return value, ok
}

// SessionFrom 读取当前动作的会话 id（没有就返回空串）。
func SessionFrom(ctx context.Context) string {
	value, _ := ActContextFrom(ctx)
	return value.SessionID
}

// DecisionFrom 读取这张卡当前已记录的 decision。
//
// 返回 ok=false 表示这张卡还没有任何 decision（官方 `cardOf()` 返回 undefined）。
func DecisionFrom(ctx context.Context) (Decision, bool) {
	value, ok := ActContextFrom(ctx)
	if !ok || !value.HasDecision {
		return Decision{}, false
	}
	return value.Decision, true
}
