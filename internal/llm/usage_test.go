package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// DeepSeek / OpenAI 兼容网关的 usage 对象里有官方扩展计数：
//
//	{"prompt_tokens":38,"completion_tokens":8,"total_tokens":46,
//	 "prompt_cache_hit_tokens":128,"prompt_cache_miss_tokens":10,
//	 "prompt_tokens_details":{"cached_tokens":128,"cache_write_tokens":0},
//	 "completion_tokens_details":{"reasoning_tokens":8}}
//
// 旧实现把它们整块丢掉（Usage 只有三个整数），于是"缓存命中多少、思考花了多少"
// 这类官方扩展字段在宿主的任何投影里都不存在。这条用例在旧实现上因此是红的。
func TestDeepSeekUsageExtensionsAreKept(t *testing.T) {
	c, _ := sseServer(t,
		chunkOf(`"content":"x"`, ""),
		`{"choices":[],"usage":{"prompt_tokens":38,"completion_tokens":8,"total_tokens":46,`+
			`"prompt_cache_hit_tokens":128,"prompt_cache_miss_tokens":10,`+
			`"prompt_tokens_details":{"cached_tokens":128,"cache_write_tokens":0},`+
			`"completion_tokens_details":{"reasoning_tokens":8}}}`,
	)
	resp, err := c.Stream(context.Background(), Request{Model: "m"}, nil)
	if err != nil {
		t.Fatalf("流式调用失败: %v", err)
	}
	if resp.Usage.PromptTokens != 38 || resp.Usage.CompletionTokens != 8 || resp.Usage.TotalTokens != 46 {
		t.Fatalf("标准三字段被扩展字段带偏了: %+v", resp.Usage)
	}
	raw, err := json.Marshal(resp.Usage)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"prompt_cache_hit_tokens":128`,
		`"prompt_cache_miss_tokens":10`,
		`"cached_tokens":128`,
		`"reasoning_tokens":8`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("usage 扩展字段被丢了：缺 %s\n实际: %s", want, raw)
		}
	}
}

// 没有扩展字段时，usage 的 JSON 形状必须与旧实现逐字节一致（不改 OpenAI 形状）。
func TestUsageWithoutExtensionsKeepsLegacyShape(t *testing.T) {
	var usage Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}`), &usage); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}` {
		t.Fatalf("默认形状变了: %s", raw)
	}
}
