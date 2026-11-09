package transport

// 官方 `contextPressure` wire 视图（`@deepseek-ai/dsh-token-meter/lib/types/usage-projection.js:140-190`）。
//
// 官方形状（三字段**各自**可选，未知就整段省略，绝不补 0）：
//
//	{
//	  contextWindow?:   number,   // 最近一次 request/context 声明的窗口
//	  pressureTokens?:  number,   // 最近一次实测的 prompt 侧三桶之和
//	  projectedTokens?: number,   // max(0, pressure + surface − sampledSurface)
//	}
//
// 为什么单独一个视图而不是复用 `context.usage`：`context.usage` 是宿主自己的
// 聚合面（used/max/categories/sections…），官方 `contextPressure` 是
// **投影键**（`SessionProjectionMap.contextPressure`），只出现在
// `session.projections` / follow baseline / `session.control` 的 values 里。
// 两条面必须各说各的形状：界面按官方键名取数，少一个键就少一块 UI。
//
// 三个数的来源与 `context.usage` 完全同源（都走 session.ProjectContext），
// 所以同一个会话在两个面上看到的是同一份事实。

import (
	"dshgo/codecs/v4"
	"dshgo/internal/session"
)

// ContextPressureView 是官方 contextPressure 的三字段视图。
//
// 全部用指针 + omitempty：官方 wire 的形状就是"缺键 = 不知道"
// （`...contextWindow === undefined ? {} : {contextWindow}`）。用 0 冒充
// "不知道"会让界面画出 0% 的占用条——那是可观测的谎报。
type ContextPressureView struct {
	ContextWindow   *int `json:"contextWindow,omitempty"`
	PressureTokens  *int `json:"pressureTokens,omitempty"`
	ProjectedTokens *int `json:"projectedTokens,omitempty"`
}

// officialContextPressureView 把 durable 事件折叠成官方 contextPressure 视图。
//
// 派生失败（坏事件）时整段省略：投影是只读展示，不能因为一条坏事件让
// `session.projections` 整体 500——那会让界面连标题都拿不到。
func officialContextPressureView(events []DurableEvent) ContextPressureView {
	v4Events := make([]v4.Event, 0, len(events))
	for _, ev := range events {
		v4Events = append(v4Events, v4.Event{Type: ev.Type, Seq: ev.Seq, Time: ev.Time, Data: ev.Data})
	}
	projection, err := session.ProjectContext(v4Events)
	if err != nil {
		return ContextPressureView{}
	}
	var view ContextPressureView
	if projection.ContextWindow > 0 {
		window := projection.ContextWindow
		view.ContextWindow = &window
	}
	if projection.Measured {
		pressure := projection.PressureTokens
		projected := projection.ProjectedTokens
		view.PressureTokens = &pressure
		view.ProjectedTokens = &projected
	}
	return view
}
