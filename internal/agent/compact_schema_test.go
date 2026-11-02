package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// 压表只许掉 description，结构（type/enum/required/items）一个不许动——
// 少一个 required 模型就会漏参数，少一个 enum 就会传非法值。
func TestCompactSchemaKeepsStructure(t *testing.T) {
	in := json.RawMessage(`{
	  "type":"object",
	  "properties":{
	    "action":{"type":"string","enum":["a","b"],"description":"很长很长的说明"},
	    "list":{"type":"array","items":{"type":"string","description":"元素说明"},"description":"列表说明"}
	  },
	  "required":["action"]
	}`)
	got := string(CompactSchema(in))
	if strings.Contains(got, "description") {
		t.Fatalf("description 没被去掉: %s", got)
	}
	for _, want := range []string{`"enum":["a","b"]`, `"required":["action"]`, `"type":"array"`, `"items"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("结构被改坏了，缺 %s: %s", want, got)
		}
	}
}

// 不是合法 JSON 就原样返回，不许 panic 也不许把表弄成空。
func TestCompactSchemaBadInputPassthrough(t *testing.T) {
	bad := json.RawMessage(`{not json`)
	if got := string(CompactSchema(bad)); got != string(bad) {
		t.Fatalf("坏输入应当原样返回，得到 %s", got)
	}
}

// 压完必须更小：这是它的全部理由。
func TestCompactSchemaShrinks(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string","description":"一句话"}}}`)
	if len(CompactSchema(in)) >= len(in) {
		t.Fatal("压完没有变小，这个函数就没有意义")
	}
}
