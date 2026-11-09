package main

import (
	"encoding/json"
	"testing"

	"dshgo/internal/llm"
	"dshgo/internal/session"
)

// usageExtensions 造 provider 扩展计数（真实 wire 形状由 llm 的 UnmarshalJSON 收进来，
// 这里直接给同一张扁平表，省掉一层 HTTP 夹具）。
func usageExtensions(pairs map[string]int64) llm.Usage {
	return llm.Usage{PromptTokens: 29690, CompletionTokens: 72, TotalTokens: 29762, Extensions: pairs}
}

// v4 `assistant/message` 的 usage 必须带缓存计数，且 inputTokens 是**未命中**部分。
//
// 官方真实样本：{inputTokens:7930,outputTokens:72,totalTokens:29762,cacheReadTokens:21760}
// —— 7930 + 21760 = 29690 = prompt_tokens，totalTokens = 29690 + 72。
// 宿主以前只写前三个键，TUI 状态栏于是永远显示"缓存 0.0%"。
func TestAssistantUsageWritesDeepSeekCacheRead(t *testing.T) {
	usage := assistantUsageFrom(ptrUsage(usageExtensions(map[string]int64{
		"prompt_cache_hit_tokens":  21760,
		"prompt_cache_miss_tokens": 7930,
	})))
	if usage == nil {
		t.Fatal("有 usage 时必须给 usage 对象")
	}
	// 线上形状（v4 事件里的 usage 对象）才是唯一判据。
	wire := usageWire(t, usage)
	assertToken(t, wire, "inputTokens", 7930)
	assertToken(t, wire, "outputTokens", 72)
	assertToken(t, wire, "totalTokens", 29762)
	assertToken(t, wire, "cacheReadTokens", 21760)
	if _, exists := wire["cacheWriteTokens"]; exists {
		t.Fatalf("provider 没给写侧计数却写了 cacheWriteTokens=%v", wire["cacheWriteTokens"])
	}
}

// OpenAI / magpie 网关形状：命中在 prompt_tokens_details.cached_tokens 里。
func TestAssistantUsageWritesOpenAICachedTokens(t *testing.T) {
	usage := assistantUsageFrom(ptrUsage(llm.Usage{
		PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105,
		Extensions: map[string]int64{"prompt_tokens_details.cached_tokens": 40},
	}))
	wire := usageWire(t, usage)
	assertToken(t, wire, "inputTokens", 60)
	assertToken(t, wire, "cacheReadTokens", 40)
	if _, exists := wire["cacheWriteTokens"]; exists {
		t.Fatalf("provider 没给写侧计数却写了 cacheWriteTokens=%v", wire["cacheWriteTokens"])
	}
}

// provider 给了写侧计数时才写 cacheWriteTokens（网关形状）。
func TestAssistantUsageWritesCacheWriteWhenProviderGivesIt(t *testing.T) {
	usage := assistantUsageFrom(ptrUsage(llm.Usage{
		PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105,
		Extensions: map[string]int64{
			"prompt_tokens_details.cache_read_tokens":  40,
			"prompt_tokens_details.cache_write_tokens": 10,
		},
	}))
	wire := usageWire(t, usage)
	assertToken(t, wire, "inputTokens", 50)
	assertToken(t, wire, "cacheReadTokens", 40)
	assertToken(t, wire, "cacheWriteTokens", 10)
}

// Anthropic 形状：prompt_tokens 本来就只有未命中部分，不能再减。
func TestAssistantUsageKeepsAnthropicInputUnchanged(t *testing.T) {
	usage := assistantUsageFrom(ptrUsage(llm.Usage{
		PromptTokens: 7930, CompletionTokens: 72, TotalTokens: 8002,
		Extensions: map[string]int64{
			"cache_read_input_tokens":     21760,
			"cache_creation_input_tokens": 1024,
		},
	}))
	wire := usageWire(t, usage)
	assertToken(t, wire, "inputTokens", 7930)
	assertToken(t, wire, "cacheReadTokens", 21760)
	assertToken(t, wire, "cacheWriteTokens", 1024)
}

// 一条缓存计数都没有时不写缓存键，也不写 0 冒充实测（界面靠"键在不在"区分未知）。
func TestAssistantUsageOmitsCacheKeysWhenProviderIsSilent(t *testing.T) {
	usage := assistantUsageFrom(ptrUsage(llm.Usage{
		PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15,
		Extensions: map[string]int64{"completion_tokens_details.reasoning_tokens": 7},
	}))
	wire := usageWire(t, usage)
	assertToken(t, wire, "inputTokens", 12)
	assertToken(t, wire, "totalTokens", 15)
	if _, exists := wire["cacheReadTokens"]; exists {
		t.Fatalf("没有缓存读数时不该写 cacheReadTokens=%v", wire["cacheReadTokens"])
	}
	if _, exists := wire["cacheWriteTokens"]; exists {
		t.Fatalf("没有缓存写数时不该写 cacheWriteTokens=%v", wire["cacheWriteTokens"])
	}
}

// provider 说 0 就是实测 0：键必须写出来（与"不知道"不同）。
func TestAssistantUsageWritesMeasuredZero(t *testing.T) {
	usage := assistantUsageFrom(ptrUsage(llm.Usage{
		PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15,
		Extensions: map[string]int64{"prompt_cache_hit_tokens": 0},
	}))
	wire := usageWire(t, usage)
	assertToken(t, wire, "inputTokens", 12)
	assertToken(t, wire, "cacheReadTokens", 0)
}

// 没有服务端用量（这一步没有 usage）→ 不写 usage 键。
func TestAssistantUsageNilStaysAbsent(t *testing.T) {
	if usage := assistantUsageFrom(nil); usage != nil {
		t.Fatalf("nil usage 必须保持 nil（不写 usage 键），实际 %#v", usage)
	}
	data := session.AssistantMessageEvent(1, 1, nil, "stop", assistantUsageFrom(nil))
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire["usage"]; exists {
		t.Fatalf("没有实测用量却写了 usage 键：%s", raw)
	}
}

// provider 自相矛盾（命中比 prompt 大）时不编负数。
func TestAssistantUsageNeverWritesNegativeInput(t *testing.T) {
	usage := assistantUsageFrom(ptrUsage(llm.Usage{
		PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11,
		Extensions: map[string]int64{"prompt_cache_hit_tokens": 99},
	}))
	wire := usageWire(t, usage)
	assertToken(t, wire, "inputTokens", 0)
	assertToken(t, wire, "cacheReadTokens", 99)
}

func ptrUsage(u llm.Usage) *llm.Usage { return &u }

// usageWire 走真实落盘路径：session.AssistantMessageEvent → JSON → usage 对象。
func usageWire(t *testing.T, usage map[string]any) map[string]int64 {
	t.Helper()
	data := session.AssistantMessageEvent(1, 1, nil, "stop", usage)
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Usage map[string]int64 `json:"usage"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Usage == nil {
		t.Fatalf("assistant/message 没有 usage 对象：%s", raw)
	}
	return wire.Usage
}

func assertToken(t *testing.T, wire map[string]int64, key string, want int64) {
	t.Helper()
	got, exists := wire[key]
	if !exists {
		t.Fatalf("usage 缺 %s（实际 %v）", key, wire)
	}
	if got != want {
		t.Fatalf("usage.%s = %d，期望 %d（实际 %v）", key, got, want, wire)
	}
}
