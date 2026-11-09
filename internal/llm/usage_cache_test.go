package llm

import (
	"encoding/json"
	"testing"
)

// usageFromJSON 用真实 wire 正文造 Usage：走的是 UnmarshalJSON 那条无损收扩展的路。
func usageFromJSON(t *testing.T, raw string) Usage {
	t.Helper()
	var usage Usage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		t.Fatalf("解析 usage %s: %v", raw, err)
	}
	return usage
}

// 三种真实形状 + 完全没有缓存计数的情形。
//
// 关键数字来自官方 48838 条 assistant usage 的真实样本：
// {inputTokens:7930,outputTokens:72,totalTokens:29762,cacheReadTokens:21760}
// ——即 prompt_tokens=29690 已含 21760 命中，写 v4 时要拆成 7930 + 21760。
func TestUsageCacheTokens(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantRead      int64
		wantWrite     int64
		wantOKRead    bool
		wantOKWrite   bool
		wantInclusive bool
	}{
		{
			name:          "DeepSeek原生prompt_cache_hit_tokens",
			raw:           `{"prompt_tokens":29690,"completion_tokens":72,"total_tokens":29762,"prompt_cache_hit_tokens":21760,"prompt_cache_miss_tokens":7930}`,
			wantRead:      21760,
			wantOKRead:    true,
			wantInclusive: true,
		},
		{
			name:          "OpenAI形状prompt_tokens_details.cached_tokens",
			raw:           `{"prompt_tokens":29690,"completion_tokens":72,"total_tokens":29762,"prompt_tokens_details":{"cached_tokens":21760}}`,
			wantRead:      21760,
			wantOKRead:    true,
			wantInclusive: true,
		},
		{
			// 网关的另一种拼法（provider.go DeepSeek adapter 的 UsageExtensions 里就有）。
			name:          "网关cache_read_tokens与cache_write_tokens",
			raw:           `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cache_read_tokens":40,"cache_write_tokens":10}}`,
			wantRead:      40,
			wantWrite:     10,
			wantOKRead:    true,
			wantOKWrite:   true,
			wantInclusive: true,
		},
		{
			// Anthropic 形状：prompt_tokens 本来就只有未命中部分，不能再减。
			name:          "Anthropic形状cache_read_input_tokens",
			raw:           `{"prompt_tokens":7930,"completion_tokens":72,"total_tokens":8002,"cache_read_input_tokens":21760,"cache_creation_input_tokens":1024}`,
			wantRead:      21760,
			wantWrite:     1024,
			wantOKRead:    true,
			wantOKWrite:   true,
			wantInclusive: false,
		},
		{
			// 实测到的 0 仍是实测：ok 必须为 true，写会话时该写 0 而不是不写键。
			name:          "provider真实给了0也算有值",
			raw:           `{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"prompt_cache_hit_tokens":0}`,
			wantRead:      0,
			wantOKRead:    true,
			wantInclusive: true,
		},
		{
			// 一条都没有：ok 全 false，调用方**不写键**，不拿 0 冒充实测。
			name:          "没有缓存计数时不写键",
			raw:           `{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}`,
			wantOKRead:    false,
			wantOKWrite:   false,
			wantInclusive: true,
		},
		{
			// 只有无关扩展（推理 token）时同样算"没有缓存计数"。
			name:          "只有reasoning_tokens不算缓存",
			raw:           `{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":7}}`,
			wantOKRead:    false,
			wantOKWrite:   false,
			wantInclusive: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			usage := usageFromJSON(t, tc.raw)
			read, write, okRead, okWrite := UsageCacheTokens(usage)
			if read != tc.wantRead || okRead != tc.wantOKRead {
				t.Fatalf("read=%d okRead=%v，期望 %d/%v（raw=%s）", read, okRead, tc.wantRead, tc.wantOKRead, tc.raw)
			}
			if write != tc.wantWrite || okWrite != tc.wantOKWrite {
				t.Fatalf("write=%d okWrite=%v，期望 %d/%v（raw=%s）", write, okWrite, tc.wantWrite, tc.wantOKWrite, tc.raw)
			}
			if got := UsagePromptTokensIncludeCache(usage); got != tc.wantInclusive {
				t.Fatalf("UsagePromptTokensIncludeCache=%v，期望 %v（raw=%s）", got, tc.wantInclusive, tc.raw)
			}
		})
	}
}

// 缓存读的优先级：同一个 usage 同时带多套拼法时取第一个真值，不合并。
func TestUsageCacheTokensPrefersNativeDeepSeekField(t *testing.T) {
	usage := usageFromJSON(t, `{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,
		"prompt_cache_hit_tokens":31,"prompt_tokens_details":{"cached_tokens":77}}`)
	read, _, okRead, _ := UsageCacheTokens(usage)
	if !okRead || read != 31 {
		t.Fatalf("read=%d okRead=%v，期望 31/true；多套拼法不该相加或取错", read, okRead)
	}
}
