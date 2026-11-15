package transport

import (
	"dshgo/codecs/v4"
	"dshgo/internal/session"
)

// 官方 `plan` 投影在 `session.projections` 里的 wire 视图。
//
// 官方形状（`@deepseek-ai/dsh-plan-mode/src/types.ts:21-24`，两个字段都必填）：
//
//	{
//	  active:  boolean,  // 被记录在案的计划模式状态（第一次切换之前是 false）
//	  pending: boolean,  // 有一个已记录、尚未结算的切换选择，目标与 active 相反
//	}
//
// 为什么必须出现在这里：客户端的**输入框模式**与**计划模式控件**都只认这一个键——
//
//   - `dsh-client-ui-conversation/src/client/input/hub.ts:108-119`
//     `runMode: plan?.active ? 'plan' : goal?.goal.phase === 'active' ? 'goal' : 'default'`；
//   - `dsh-client-ui-plan/src/client/PlanModeControl.tsx:33`
//     `const target = plan.pending ? !plan.active : plan.active`。
//
// 键**总是**出现：官方 `plan` 键的缺席含义是"部署里没装 plan-mode 这个能力"，
// 而宿主总是装了（`modules/tool/plan`），所以缺席在这里是错的表达。
//
// 派生口径与 `sessionStats` / `tokenUsage` / `contextPressure` 完全同源：都是对
// durable v4 事件做**纯派生**折叠（读路径不写盘，R5 红线），所以同一个会话在
// HTTP `session.projections`、`session.follow` 基线、`session.control` 三处看到的
// 是同一份值。
func officialPlanView(events []DurableEvent) session.PlanProjection {
	v4Events := make([]v4.Event, 0, len(events))
	for _, event := range events {
		// 折叠只读 type/seq/time/data 四字段：信封上的 surfaceOp /
		// sourceEventSeqs / ignorable 是"这条事件怎么进 surface"的说明。
		v4Events = append(v4Events, v4.Event{Type: event.Type, Seq: event.Seq, Time: event.Time, Data: event.Data})
	}
	return session.ProjectPlan(v4Events)
}
