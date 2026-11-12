package transport

import (
	"dshgo/codecs/v4"
	"dshgo/internal/session"
)

// 官方 `sessionStats` 投影在 `session.projections` 里的 wire 视图。
//
// 官方形状（`@deepseek-ai/dsh-session-stats/lib/types/projection.js:154-167`，八个
// 字段全部必填且非负）：
//
//	{
//	  turns:        number,  // 至少闭合过一个 step 的轮次数（step/end 去重）
//	  steps:        number,  // 已闭合的 step 数（完成/失败/取消/ max-tokens 各算一步）
//	  llmMs:        number,  // step/start → assistant/message 的墙钟之和
//	  toolMs:       number,  // tool/call → tool/result 配对墙钟之和（按 callId）
//	  ttftMs:       number,  // step/start → 第一个非空 delta 的延迟之和
//	  ttftSteps:    number,  // 记录了首 token 的 step 数
//	  decodeMs:     number,  // first token → 组装消息的墙钟之和
//	  decodeTokens: number,  // 同批 step 上 provider 上报的 output tokens 之和
//	}
//
// 为什么必须出现在这里：客户端 `dsh-experimental-client-ui-stats` 用
// `useProjection('sessionStats')` 取整份日志的合计（而不是自己按窗口聚合），
// 缺这个键时它的合计行整段不画——界面表现就是状态栏「活动」恒为「未知」。
//
// 派生口径与 `contextPressure` 完全同源：都是对 durable v4 事件做**纯派生**折叠
// （读路径不写盘，R5 红线），所以同一个会话在 HTTP `session.projections`、
// `session.follow` 基线、`session.control` 三处看到的是同一份数字。
func officialSessionStatsView(events []DurableEvent) session.SessionStats {
	v4Events := make([]v4.Event, 0, len(events))
	for _, event := range events {
		// 折叠只读 type/seq/time/data 四字段：信封上的 surfaceOp / sourceEventSeqs /
		// ignorable 是"这条事件怎么进 surface"的说明，与计数、墙钟无关。
		v4Events = append(v4Events, v4.Event{Type: event.Type, Seq: event.Seq, Time: event.Time, Data: event.Data})
	}
	return session.ProjectSessionStats(v4Events)
}
