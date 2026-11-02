package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"dshgo/internal/agent"
)

// 压表只许去掉参数的说明文字。类型、enum、required、items 一个都不许动——
// 少一个 required 模型就会漏参数，少一个 enum 就会传非法值。
//
// 这条测试盯着"两种表结构一致"，因为 compact 已经默认开启：
// 一旦有人在某处漏改，模型看到的就是残缺的契约，而单测很难发现。
func TestCompactTableKeepsContract(t *testing.T) {
	table := buildToolTableMust(t)
	var fullBytes, compactBytes int
	for _, e := range table {
		compact := agent.CompactSchema(e.Parameters)
		fullBytes += len(e.Parameters)
		compactBytes += len(compact)

		var a, b any
		if err := json.Unmarshal(e.Parameters, &a); err != nil {
			t.Fatalf("%s: 原 schema 不是合法 JSON: %v", e.Name, err)
		}
		if err := json.Unmarshal(compact, &b); err != nil {
			t.Fatalf("%s: 压完不是合法 JSON: %v", e.Name, err)
		}
		if !reflect.DeepEqual(stripDesc(a), stripDesc(b)) {
			t.Fatalf("%s: 压表改动了结构\n原: %v\n压后: %v", e.Name, a, b)
		}
	}
	if compactBytes >= fullBytes {
		t.Fatalf("压完没有变小（%d → %d），默认开它就没有意义", fullBytes, compactBytes)
	}
	t.Logf("工具表 %d 个：%d → %d 字节（省 %.0f%%）",
		len(table), fullBytes, compactBytes, 100*(1-float64(compactBytes)/float64(fullBytes)))
}

func stripDesc(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if k == "description" {
				continue
			}
			out[k] = stripDesc(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = stripDesc(val)
		}
		return out
	}
	return v
}

// 默认值本身也要钉住：翻默认是经过 20 次 A/B 的决定，不该被顺手改回去。
// flag 的默认值由 scripts/check.sh 里的 `--help` 断言兜住（那才是模型真正看到的）。
func TestCompactIsTheDefault(t *testing.T) {
	if defaultToolSchema != "compact" {
		t.Fatalf("默认工具表 = %q，期望 compact（改它要先跑 scripts/tool-table-ab.py 拿数据）", defaultToolSchema)
	}
}
