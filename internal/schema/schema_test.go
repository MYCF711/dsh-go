package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"dshgo/internal/schema"
)

// 「注解还是参数名」只有一个判断函数，且答案取决于位置。
//
// 2026-11-04 的同形 bug 就出在把位置丢了：同一个键名，在 schema 对象上是
// 注解（删），在 properties 映射里是参数名（留）。
func TestIsAnnotationKeyIsPositionAware(t *testing.T) {
	cases := []struct {
		key           string
		inPropertyMap bool
		want          bool
	}{
		{"description", false, true},   // schema 对象上的说明 → 注解
		{"description", true, false},   // properties 里的参数名 → 保留
		{"type", false, false},         // 结构关键字
		{"required", false, false},     // 结构关键字
		{"description ", false, false}, // 不做 trim：键名精确匹配
	}
	for _, c := range cases {
		if got := schema.IsAnnotationKey(c.key, c.inPropertyMap); got != c.want {
			t.Errorf("IsAnnotationKey(%q, %v) = %v，期望 %v", c.key, c.inPropertyMap, got, c.want)
		}
	}
}

// 「名字 → schema」的词汇表只有一份。少写一个（$defs/definitions）就会把
// 里面的参数名当注解删掉；多写一个就会把 items 的名字当关键字。
func TestIsPropertyMapKeyVocabulary(t *testing.T) {
	for _, k := range []string{"properties", "patternProperties", "$defs", "definitions"} {
		if !schema.IsPropertyMapKey(k) {
			t.Errorf("%q 应当是名字映射", k)
		}
	}
	for _, k := range []string{"items", "anyOf", "allOf", "oneOf", "additionalProperties", "required", "type", "description"} {
		if schema.IsPropertyMapKey(k) {
			t.Errorf("%q 不是名字映射：它的值是 schema 本身或字面量", k)
		}
	}
}

// 名叫 description 的参数不是注解，一个都不许删；注解必须删。
//
// 这条测试同时钉住两件事，因为这两件事是同一条规则的两面：只留参数名
// 不删注解，压表就没意义；只删注解不留参数名，就是 2026-11-04 那条 bug。
func TestCompactKeepsParamNamedDescription(t *testing.T) {
	in := json.RawMessage(`{
	  "type":"object",
	  "properties":{
	    "description":{"type":"string","description":"给用户看的短描述"},
	    "command":{"type":"string","description":"要执行的命令"},
	    "options":{"type":"array","items":{
	      "type":"object",
	      "properties":{"description":{"type":"string","description":"选项说明"}},
	      "required":["description"]
	    }}
	  },
	  "required":["command","description"]
	}`)
	got := schema.Compact(in)

	var s struct {
		Properties map[string]struct {
			Type  string `json:"type"`
			Items *struct {
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
				Required []string `json:"required"`
			} `json:"items"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatalf("压完不是合法 JSON: %v（%s）", err, got)
	}
	if s.Properties["description"].Type != "string" {
		t.Fatalf("名叫 description 的参数被删了：模型只看到 required 里一个没类型的名字\n%s", got)
	}
	if it := s.Properties["options"].Items; it == nil || it.Properties["description"].Type != "string" {
		t.Fatalf("数组元素里名叫 description 的参数被删了\n%s", got)
	}
	for _, r := range s.Required {
		if _, ok := s.Properties[r]; !ok {
			t.Fatalf("required 点名了 %q，properties 里却没有——压表造出了悬空的必填项\n%s", r, got)
		}
	}
	for _, text := range []string{"给用户看的短描述", "要执行的命令", "选项说明"} {
		if strings.Contains(string(got), text) {
			t.Fatalf("注解 %q 还在：%s", text, got)
		}
	}
	// 固定点：再压一遍必须一字不差，否则说明还有该删的没删干净。
	if again := schema.Compact(got); string(again) != string(got) {
		t.Fatalf("压表不是不动点：\n一遍: %s\n二遍: %s", got, again)
	}
}

// 不是合法 JSON 就原样返回：投影宁可少压一点，也不能把看不懂的东西弄坏。
func TestCompactBadInputPassthrough(t *testing.T) {
	bad := json.RawMessage(`{not json`)
	if got := string(schema.Compact(bad)); got != string(bad) {
		t.Fatalf("坏输入应当原样返回，得到 %s", got)
	}
}

// 压完必须更小：这是这个函数存在的全部理由。
func TestCompactShrinks(t *testing.T) {
	in := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string","description":"一句话"}}}`)
	if len(schema.Compact(in)) >= len(in) {
		t.Fatal("压完没有变小，这个函数就没有意义")
	}
}
