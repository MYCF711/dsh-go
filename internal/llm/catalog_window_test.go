package llm

import (
	"encoding/json"
	"testing"
)

// CatalogContextWindow 必须同时认官方 schema（contextWindow）与网关形状
// （context_length），并且把"目录没声明"如实报成 0。
func TestCatalogContextWindow(t *testing.T) {
	view := ModelCatalogView{Models: []json.RawMessage{
		json.RawMessage(`{"id":"group/deepseek-v4.1-flash-1017","name":"flash","contextWindow":1000000,"maxTokens":128000}`),
		json.RawMessage(`{"id":"gw/model-snake","owned_by":"gateway","context_length":65536}`),
		json.RawMessage(`{"id":"gw/no-window","owned_by":"gateway"}`),
		json.RawMessage(`{"id":"both/window","contextWindow":32000,"context_length":65536}`),
		json.RawMessage(`{"id":"zero/window","contextWindow":0}`),
		// 两个不同前缀的候选同时存在：无前缀输入不能猜，必须按"没声明"处理。
		json.RawMessage(`{"id":"a/ambig","contextWindow":1000}`),
		json.RawMessage(`{"id":"b/ambig","contextWindow":2000}`),
		// 后缀必须在 `/` 边界上，"m" 不能命中 "suffix/notm"。
		json.RawMessage(`{"id":"suffix/notm","contextWindow":500}`),
	}}
	tests := []struct {
		name    string
		modelID string
		want    int
	}{
		{name: "官方camelCase字段", modelID: "group/deepseek-v4.1-flash-1017", want: 1000000},
		{name: "网关snake_case字段", modelID: "gw/model-snake", want: 65536},
		{name: "目录没声明就是0", modelID: "gw/no-window", want: 0},
		{name: "两个都给时官方字段优先", modelID: "both/window", want: 32000},
		{name: "0不是声明", modelID: "zero/window", want: 0},
		{name: "模型不在目录里", modelID: "unknown/model", want: 0},
		{name: "空模型id", modelID: "", want: 0},
		// 会话里常见无前缀 id（TUI2GO launcher / session-model.json）；catalog 的键
		// 带 `group/` 前缀。唯一后缀候选要命中，否则上下文占用会退回默认值。
		{name: "无前缀别名命中带前缀条目", modelID: "deepseek-v4.1-flash-1017", want: 1000000},
		// 反例：两个不同前缀的候选同时存在时不猜。
		{name: "两个前缀候选同时存在不猜", modelID: "ambig", want: 0},
		// 反例：后缀必须落在 `/` 边界，不能按字符尾巴乱匹配。
		{name: "后缀不在斜杠边界不命中", modelID: "m", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CatalogContextWindow(view, tc.modelID); got != tc.want {
				t.Fatalf("CatalogContextWindow(%q) = %d，期望 %d", tc.modelID, got, tc.want)
			}
		})
	}
}
