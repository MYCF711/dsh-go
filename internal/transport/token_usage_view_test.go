package transport

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 官方 `tokenUsage` 投影必须出现在 `session.projections` 的 values 里
// （`@deepseek-ai/dsh-token-meter/src/usage-projection.ts:45-50` 的 wire schema，
// `:143` 的 `view: state => state.totals`）。
//
// 为什么这条测试在 transport 层：折叠本身由 `internal/session` 的单测覆盖，
// 这里钉的是**接线**——一个折得再对的投影，只要没被 `officialProjectionValues`
// 写进 values，界面拿到的仍然是"未知"。官方用量弹窗
// （`dsh-client-ui-chat/src/client/chat/StatsPills.tsx:296-347` 的 `UsagePill`）
// 与统计面板合计行（`dsh-experimental-client-ui-stats/src/client/stats-totals.ts:13-39`）
// 都只从 `useProjection('tokenUsage')` 取数。
func TestOfficialTokenUsageProjectionWired(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	src := &fakeOfficialSource{logEnd: 6, events: []DurableEvent{
		{Type: "turn/start", Seq: 0, Time: 1000, Data: raw(`{"turn":1}`)},
		{Type: "step/start", Seq: 1, Time: 2000, Data: raw(`{"turn":1,"step":1}`)},
		// 第一段结算：未命中输入 9、输出 6。
		{Type: "assistant/message", Seq: 2, Time: 2600, Data: raw(`{"turn":1,"step":1,"message":{"role":"assistant","content":[]},"stream":[{"type":"finish","reason":{"kind":"completed"}}],"usage":{"inputTokens":9,"outputTokens":6,"totalTokens":15}}`)},
		{Type: "step/end", Seq: 3, Time: 2700, Data: raw(`{"turn":1,"step":1}`)},
		{Type: "step/start", Seq: 4, Time: 3000, Data: raw(`{"turn":1,"step":2}`)},
		// 第二段结算（带缓存两桶）：因为 turn/step 不同，**累加**而不是替换。
		{Type: "assistant/message", Seq: 5, Time: 3600, Data: raw(`{"turn":1,"step":2,"message":{"role":"assistant","content":[]},"stream":[{"type":"finish","reason":{"kind":"completed"}}],"usage":{"inputTokens":20,"outputTokens":10,"totalTokens":30,"cacheReadTokens":100,"cacheWriteTokens":5}}`)},
		{Type: "step/end", Seq: 6, Time: 3700, Data: raw(`{"turn":1,"step":2}`)},
	}}
	s := officialServerWith(t, src, Options{})

	rec := postOfficial(t, s.handleSessionProjections, `{"sessionId":"s1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("projections status=%d body=%s", rec.Code, rec.Body.String())
	}
	values := unwrapResult(t, rec.Body.Bytes())["values"].(map[string]any)
	rawUsage, ok := values["tokenUsage"]
	if !ok {
		t.Fatalf("values 里必须有 tokenUsage 键（否则用量弹窗只能显示「未知」）：%v", values)
	}
	usage, ok := rawUsage.(map[string]any)
	if !ok {
		t.Fatalf("tokenUsage 必须是对象：%#v", rawUsage)
	}
	// 官方 schema 四个桶全部必填（`.strict()` + 每个 key 都是 required）：
	// 少一个就是界面读不到的桶。
	want := map[string]float64{
		"uncachedInputTokens": 29,  // 9 + 20
		"outputTokens":        16,  // 6 + 10
		"cacheReadTokens":     100, // 0 + 100
		"cacheWriteTokens":    5,   // 0 + 5
	}
	if len(usage) != len(want) {
		t.Fatalf("tokenUsage 字段 = %v，期望恰好四个官方桶", usage)
	}
	for key, expected := range want {
		number, ok := usage[key].(float64)
		if !ok {
			t.Fatalf("tokenUsage[%q] = %#v，期望 number", key, usage[key])
		}
		if number != expected {
			t.Fatalf("tokenUsage[%q] = %v，期望 %v", key, number, expected)
		}
	}
}

// 空会话也必须给出四桶全 0 的 tokenUsage（官方 `init: () => ({totals: zeroBuckets()})`
// + `snapshot()` 对每个已注册 wire 单元都产出一个值）：键缺席会被界面读成
// 「这个宿主没有这条投影」，而四桶全 0 是"还没有任何用量"——后者才是真话。
func TestOfficialTokenUsageProjectionEmptySession(t *testing.T) {
	src := &fakeOfficialSource{logEnd: -1, events: nil}
	s := officialServerWith(t, src, Options{})

	rec := postOfficial(t, s.handleSessionProjections, `{"sessionId":"s1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	values := unwrapResult(t, rec.Body.Bytes())["values"].(map[string]any)
	usage, ok := values["tokenUsage"].(map[string]any)
	if !ok {
		t.Fatalf("空会话也必须给出 tokenUsage（官方 init 是四桶全 0）：%v", values)
	}
	if len(usage) != 4 {
		t.Fatalf("空会话 tokenUsage 字段 = %v，期望恰好四个官方桶", usage)
	}
	for key, value := range usage {
		number, ok := value.(float64)
		if !ok || number != 0 {
			t.Fatalf("空会话 tokenUsage[%q] = %#v，期望 0", key, value)
		}
	}
}
