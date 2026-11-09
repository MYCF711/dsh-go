package transport

import (
	"strings"
	"testing"
)

// 这一组钉住 GO.6 的 wire 形状（官方 `context.measurement.*`）：
//
//   - 三个新键只加不改：used/max/categories 的键名与语义一个都没动；
//   - 没有实测锚点时三个键**整体省略**（omitempty），旧客户端与旧快照照旧解析；
//   - 数值越界（负数/超安全整数）必须被拒，不能把脏值送到界面。
//
// 口径来源：@deepseek-ai/dsh-token-meter/lib/types/usage-projection.js:58/185 与
// `usage.fields.projectionShape`。

func TestContextUsageWireProjectionShape(t *testing.T) {
	server, err := NewServer(Options{
		ContextUsage: func(string, string) (ContextUsageView, error) {
			return ContextUsageView{
				Used:            1050,
				Max:             10000,
				Categories:      []ContextCategory{{Name: "context", Tokens: 1050, Kind: "used"}},
				PressureTokens:  1000,
				ProjectedTokens: 1200,
				UsageBuckets: &ContextUsageBuckets{
					UncachedInputTokens: 100, OutputTokens: 50, CacheReadTokens: 900, CacheWriteTokens: 0,
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := callW149(t, server, "context.usage", `{"id":"session-a","detail":"full"}`).Body.String()
	// 这条日志就是 RESULT.md 里贴的 wire 样本（`go test -run ... -v` 可复现）。
	t.Logf("context.usage wire = %s", raw)
	for _, want := range []string{`"pressureTokens":1000`, `"projectedTokens":1200`, `"uncachedInputTokens":100`, `"outputTokens":50`, `"cacheReadTokens":900`, `"cacheWriteTokens":0`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("wire 缺 %s：%s", want, raw)
		}
	}
	if !strings.Contains(raw, `"used":1050`) {
		t.Fatalf("既有键 used 被改动了：%s", raw)
	}
}

func TestContextUsageWireOmitsProjectionWithoutMeasurement(t *testing.T) {
	server, err := NewServer(Options{
		ContextUsage: func(string, string) (ContextUsageView, error) {
			return ContextUsageView{
				Used:       0,
				Categories: []ContextCategory{{Name: "context", Tokens: 0, Kind: "used"}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := callW149(t, server, "context.usage", `{"id":"session-a","detail":"full"}`).Body.String()
	for _, key := range []string{"pressureTokens", "projectedTokens", "usageBuckets"} {
		if strings.Contains(raw, `"`+key+`"`) {
			t.Fatalf("没有实测锚点却发了 %s（旧客户端会把它当实测 0）：%s", key, raw)
		}
	}
}

func TestContextUsageWireRejectsNegativeBuckets(t *testing.T) {
	server, err := NewServer(Options{
		ContextUsage: func(string, string) (ContextUsageView, error) {
			return ContextUsageView{
				Used:           10,
				Categories:     []ContextCategory{{Name: "context", Tokens: 10, Kind: "used"}},
				PressureTokens: -1,
				UsageBuckets:   &ContextUsageBuckets{UncachedInputTokens: 1},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := callW149(t, server, "context.usage", `{"id":"session-a","detail":"full"}`)
	if recorder.Code == 200 {
		t.Fatalf("负压力被接受：%s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "pressureTokens") {
		t.Fatalf("错误文案没点出字段：%s", recorder.Body.String())
	}
}
