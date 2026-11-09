package transport

// 官方 `contextPressure` wire 视图（dsh-token-meter/lib/types/usage-projection.js:140-190）。
//
// 判据：
//  1. 三字段**各自**"未知就省略"——用 0 冒充不知道是可观测的谎报；
//  2. 数值与 `context.usage` 同源（同一份 session.ProjectContext）；
//  3. 键本身总在 `session.projections.values` 里（官方 SessionProjectionMap 没有 optional）。

import (
	"encoding/json"
	"testing"

	"dshgo/codecs/v4"
)

// rawEvent 造一条 v4 事件（transport 测试里只需要 type + data）。
func rawEvent(t *testing.T, typ string, data any) v4.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("构造 %s 事件: %v", typ, err)
	}
	return v4.Event{Type: typ, Data: raw}
}

func durableEventsOf(t *testing.T, events []v4.Event) []DurableEvent {
	t.Helper()
	out := make([]DurableEvent, 0, len(events))
	for i, ev := range events {
		out = append(out, DurableEvent{Type: ev.Type, Seq: int64(i + 1), Time: ev.Time, Data: ev.Data})
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	return string(raw)
}

func TestContextPressureViewOmitsUnknownFields(t *testing.T) {
	// 没有任何事件：三字段全省略，序列化就是 `{}`（不是 `{"contextWindow":0,...}`）。
	view := officialContextPressureView(nil)
	if got := mustJSON(t, view); got != "{}" {
		t.Fatalf("没有任何实测时必须整段省略，得到 %s", got)
	}
	if view.ContextWindow != nil || view.PressureTokens != nil || view.ProjectedTokens != nil {
		t.Fatalf("未知字段必须是 nil（不是 0）：%+v", view)
	}
}

func TestContextPressureViewProjectsThreeNumbers(t *testing.T) {
	events := []v4.Event{
		rawEvent(t, "request/context", map[string]any{"contextWindow": 10000}),
		rawEvent(t, "assistant/message", map[string]any{
			"turn": 1, "step": 1,
			"blocks":       []any{map[string]any{"type": "text", "text": "answer"}},
			"finishReason": "stop",
			"usage": map[string]any{
				"inputTokens": 100, "outputTokens": 50, "totalTokens": 1050, "cacheReadTokens": 900,
			},
		}),
	}
	view := officialContextPressureView(durableEventsOf(t, events))
	if view.ContextWindow == nil || *view.ContextWindow != 10000 {
		t.Fatalf("contextWindow 必须来自 request/context，得到 %v", view.ContextWindow)
	}
	if view.PressureTokens == nil || *view.PressureTokens != 1000 {
		t.Fatalf("pressureTokens = input+cacheRead+cacheWrite = 1000（不含 output），得到 %v", view.PressureTokens)
	}
	if view.ProjectedTokens == nil {
		t.Fatal("有实测锚点时 projectedTokens 必须有值（官方 wire.view 判据）")
	}
	if *view.ProjectedTokens < 0 {
		t.Fatalf("projectedTokens 不能是负数：%d", *view.ProjectedTokens)
	}
	// 与 context.usage 同源：两边必须是同一份数。
	if raw := mustJSON(t, view); raw == "{}" {
		t.Fatalf("三个数都该出现：%s", raw)
	}
}

func TestContextPressureViewWindowUnknownStaysOmitted(t *testing.T) {
	// 只有 usage、没有 request/context：pressure/projected 有值，window 省略。
	events := []v4.Event{
		rawEvent(t, "assistant/message", map[string]any{
			"turn": 1, "step": 1,
			"blocks":       []any{map[string]any{"type": "text", "text": "answer"}},
			"finishReason": "stop",
			"usage":        map[string]any{"inputTokens": 10, "outputTokens": 1},
		}),
	}
	view := officialContextPressureView(durableEventsOf(t, events))
	if view.ContextWindow != nil {
		t.Fatalf("没有 request/context 时 contextWindow 必须省略，得到 %v", *view.ContextWindow)
	}
	if view.PressureTokens == nil || *view.PressureTokens != 10 {
		t.Fatalf("pressureTokens 必须仍然给出：%v", view.PressureTokens)
	}
	raw := mustJSON(t, view)
	if raw == "{}" || raw[0] != '{' {
		t.Fatalf("视图必须是 JSON 对象：%s", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["contextWindow"]; ok {
		t.Fatalf("JSON 里不能出现 contextWindow 键：%s", raw)
	}
}
