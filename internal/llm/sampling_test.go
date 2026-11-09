package llm

// 官方每请求采样面恰 4 个字段：reasoningEffort / temperature / maxTokens / stop
// （`tmp/GO3-PARAMS/_official/dsh-llm*/README.md`；**明确没有 top_p**）。
//
// 宿主 `Request` 原来只有 Model/Messages/Tools/Stream/StreamOptions/MaxTokens/
// Reasoning：temperature 与 stop 全树 0 命中，模型只能用服务端默认温度，也没法
// 用停用词截断输出（`docs/parameter-register.md` #13/#20/#21）。
//
// 这一组测试钉住三件事：
//  1. 两个字段真的存在（否则无从透传）；
//  2. **不设就不发**——默认 wire 报文逐字节不变（严格网关不吃无谓漂移）；
//  3. 设了就逐字出现（`temperature:0` 这种"显式零值"也必须发出去，它是
//     确定性复现的唯一手段，不能被 omitempty 吃掉）。

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestRequestExposesOfficialSamplingSurface 是最小契约：字段不存在就无从透传。
func TestRequestExposesOfficialSamplingSurface(t *testing.T) {
	typ := reflect.TypeOf(Request{})
	for _, name := range []string{"Temperature", "Stop"} {
		if _, ok := typ.FieldByName(name); !ok {
			t.Fatalf("llm.Request 缺 %s 字段：官方每请求采样面（reasoningEffort/temperature/maxTokens/stop）"+
				"缺这一项时宿主无法透传，模型只能吃服务端默认值", name)
		}
	}
}

func floatPtr(value float64) *float64 { return &value }

// TestWireOmitsSamplingWhenUnset 是"默认行为一字不改"的守卫：
// 不设 Temperature/Stop 时，请求体里连键名都不出现（不是 null、不是空数组）。
func TestWireOmitsSamplingWhenUnset(t *testing.T) {
	body, err := marshalWireRequest(Request{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	}, defaultWireProfile())
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, key := range []string{`"temperature"`, `"stop"`} {
		if strings.Contains(text, key) {
			t.Fatalf("没设 %s 时请求体里出现了这个键（严格网关会 400）：%s", key, text)
		}
	}
}

// TestWireCarriesSamplingWhenSet：显式设置（含 temperature=0）必须逐字出现在 wire 里。
func TestWireCarriesSamplingWhenSet(t *testing.T) {
	body, err := marshalWireRequest(Request{
		Model:       "m",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		Stream:      true,
		Temperature: floatPtr(0),
		Stop:        []string{"\n\n", "STOP"},
	}, defaultWireProfile())
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v（%s）", err, body)
	}
	temperature, ok := wire["temperature"]
	if !ok {
		t.Fatalf("显式 temperature=0 没有发出去（被 omitempty 吃掉）：%s", body)
	}
	if value, ok := temperature.(float64); !ok || value != 0 {
		t.Fatalf("wire temperature = %#v，期望 0", temperature)
	}
	stop, ok := wire["stop"].([]any)
	if !ok || len(stop) != 2 || stop[0] != "\n\n" || stop[1] != "STOP" {
		t.Fatalf("wire stop = %#v，期望 [\"\n\n\",\"STOP\"]（顺序原样）", wire["stop"])
	}
}

// TestWireOmitsEmptyStop：空切片（不是 nil）同样不发——"没有停用词"不是"发一个空数组"。
func TestWireOmitsEmptyStop(t *testing.T) {
	body, err := marshalWireRequest(Request{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Stream:   true,
		Stop:     []string{},
	}, defaultWireProfile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"stop"`) {
		t.Fatalf("空 stop 切片被发出去了：%s", body)
	}
}
