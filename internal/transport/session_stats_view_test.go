package transport

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 官方 `sessionStats` 投影必须出现在 `session.projections` 的 values 里
// （`@deepseek-ai/dsh-session-stats/lib/types/projection.js:154-167`）。
//
// 为什么这条测试在 transport 层：折叠本身由 `internal/session` 的单测覆盖，
// 这里钉的是**接线**——一个折得再对的投影，只要没被 `officialProjectionValues`
// 写进 values，界面拿到的仍然是"未知"。客户端
// `dsh-experimental-client-ui-stats` 就是 `useProjection('sessionStats')`。
func TestOfficialSessionStatsProjectionWired(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	src := &fakeOfficialSource{logEnd: 9, events: []DurableEvent{
		{Type: "turn/start", Seq: 0, Time: 1000, Data: raw(`{"turn":1}`)},
		{Type: "user/message", Seq: 1, Time: 1010, Data: raw(`{"content":[{"type":"text","text":"跑一下"}],"source":{"kind":"user"}}`)},
		{Type: "step/start", Seq: 2, Time: 2000, Data: raw(`{"turn":1,"step":1}`)},
		{Type: "assistant/text", Seq: 3, Time: 2200, Data: raw(`{"turn":1,"step":1,"index":0,"text":"我来","model":"m"}`)},
		{Type: "assistant/message", Seq: 4, Time: 2600, Data: raw(`{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"我来"}]},"stream":[{"type":"finish","reason":{"kind":"completed"}}],"usage":{"inputTokens":9,"outputTokens":6,"totalTokens":15}}`)},
		{Type: "step/end", Seq: 5, Time: 2700, Data: raw(`{"turn":1,"step":1}`)},
		{Type: "tool/call", Seq: 6, Time: 3000, Data: raw(`{"turn":1,"step":1,"callId":"c1","name":"read","arguments":"{}"}`)},
		{Type: "tool/result", Seq: 7, Time: 3400, Data: raw(`{"turn":1,"step":1,"message":{"role":"tool","source":{"kind":"tool","callId":"c1"},"toolCallId":"c1","content":[],"isError":false,"id":"m1"}}`)},
		{Type: "turn/end", Seq: 8, Time: 3500, Data: raw(`{"turn":1,"reason":{"kind":"completed"}}`)},
	}}
	s := officialServerWith(t, src, Options{})

	rec := postOfficial(t, s.handleSessionProjections, `{"sessionId":"s1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("projections status=%d body=%s", rec.Code, rec.Body.String())
	}
	values := unwrapResult(t, rec.Body.Bytes())["values"].(map[string]any)
	rawStats, ok := values["sessionStats"]
	if !ok {
		t.Fatalf("values 里必须有 sessionStats 键（否则界面只能显示「未知」）：%v", values)
	}
	stats, ok := rawStats.(map[string]any)
	if !ok {
		t.Fatalf("sessionStats 必须是对象：%#v", rawStats)
	}
	// 官方 schema 八个字段全部必填（`.strict()` + 每个 key 都是 required）：
	// 少一个就是界面读不到的字段。
	want := map[string]float64{
		"turns":        1,
		"steps":        1,
		"llmMs":        600, // 2600 - 2000
		"toolMs":       400, // 3400 - 3000
		"ttftMs":       200, // 2200 - 2000
		"ttftSteps":    1,
		"decodeMs":     400, // 2600 - 2200
		"decodeTokens": 6,
	}
	if len(stats) != len(want) {
		t.Fatalf("sessionStats 字段 = %v，期望恰好八个官方键", stats)
	}
	for key, expected := range want {
		number, ok := stats[key].(float64)
		if !ok {
			t.Fatalf("sessionStats[%q] = %#v，期望 number", key, stats[key])
		}
		if number != expected {
			t.Fatalf("sessionStats[%q] = %v，期望 %v", key, number, expected)
		}
	}
}

// 空会话也必须给出全 0 的 sessionStats（官方：每个字段"0 until its first
// contributing event lands"）——键缺席会让界面把"还没发生"画成"不知道"。
func TestOfficialSessionStatsProjectionEmptySession(t *testing.T) {
	src := &fakeOfficialSource{logEnd: -1, events: nil}
	s := officialServerWith(t, src, Options{})

	rec := postOfficial(t, s.handleSessionProjections, `{"sessionId":"s1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	values := unwrapResult(t, rec.Body.Bytes())["values"].(map[string]any)
	stats, ok := values["sessionStats"].(map[string]any)
	if !ok {
		t.Fatalf("空会话也必须给出 sessionStats：%v", values)
	}
	for key, value := range stats {
		if number, ok := value.(float64); !ok || number != 0 {
			t.Fatalf("空会话 sessionStats[%q] = %#v，期望 0", key, value)
		}
	}
}
