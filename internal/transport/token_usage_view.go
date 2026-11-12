package transport

import (
	"dshgo/codecs/v4"
	"dshgo/internal/session"
)

// 官方 `tokenUsage` 投影在 `session.projections` 里的 wire 视图。
//
// 官方形状（`@deepseek-ai/dsh-token-meter/src/usage-projection.ts:45-50` 的
// `projectionSchema` + `:143` 的 `wire.view = state => state.totals`，四桶**全部
// 必填**、整数、非负）：
//
//	{
//	  uncachedInputTokens: number,  // prompt 侧未命中缓存的部分
//	  outputTokens:        number,  // 输出 token（reasoning 已含在内，不另加）
//	  cacheReadTokens:     number,  // 缓存读命中
//	  cacheWriteTokens:    number,  // 缓存写
//	}
//
// 四桶**不相交**（`src/projection.ts:15-20`），所以 prompt 侧压力是前三桶中
// `uncachedInputTokens + cacheReadTokens + cacheWriteTokens` 之和——输出桶不在其中。
//
// # 为什么必须出现在这里
//
// 官方用量胶囊 `UsagePill` 与统计面板的合计行都只从
// `useProjection('tokenUsage')` 取数（`dsh-client-ui-chat/src/client/chat/StatsPills.tsx:296-347`、
// `dsh-experimental-client-ui-stats/src/client/stats-totals.ts:13-39`）：缺这个键，
// 用量弹窗整段无法成立（界面表现就是「未知」）。
//
// # 0 与「未知」的区分（官方口径，别自己加戏）
//
// 官方 wire 的四桶**没有**可选位：`init` 就是全 0（`zeroBuckets`），
// `snapshot()` 对每个已注册的 wire 单元都会产出一个值
// （`dsh-session-projection/src/index.ts:508-528` 的惰性折叠），所以"还没有任何用量"
// 在官方就是四桶全 0，**不是**键缺席。真正的「未知」只有一种：这个宿主根本没有
// 这条投影（键缺席 = 老宿主），此时界面按官方规则退回窗口聚合。
//
// 需要区分"provider 回报了 0"与"一次都没回报"的调用方，读 Go 侧的
// `session.TokenUsageProjection.Measured`——wire 上不表达（官方也没有这一位）。
//
// 派生口径与 `sessionStats` / `contextPressure` 完全同源：都是对 durable v4 事件做
// **纯派生**折叠（读路径不写盘，R5 红线），所以同一个会话在 HTTP
// `session.projections`、`session.follow` 基线、`session.control` 三处看到的是同一份数字。
func officialTokenUsageView(events []DurableEvent) session.UsageBuckets {
	v4Events := make([]v4.Event, 0, len(events))
	for _, event := range events {
		// 折叠只读 type/seq/time/data 四字段：信封上的 surfaceOp / sourceEventSeqs /
		// ignorable 是"这条事件怎么进 surface"的说明，与用量记账无关。
		v4Events = append(v4Events, v4.Event{Type: event.Type, Seq: event.Seq, Time: event.Time, Data: event.Data})
	}
	return session.ProjectTokenUsage(v4Events).Buckets
}
