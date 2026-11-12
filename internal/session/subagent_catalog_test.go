package session

import (
	"encoding/json"
	"strings"
	"testing"

	"dshgo/codecs/v4"
)

func catalogEvent(t *testing.T, seq int64, payload map[string]any) v4.Event {
	t.Helper()
	return testEvent(t, EventSubagentCatalog, seq, payload)
}

// 官方 v3 载荷（rc.2 起 native V4 只写这一版）：
//
//	{version:3, childId, childCreatedAt, name} &
//	({mode:'continuable', label} | {mode:'external', label?})
//
// 证据：`dsh-session-reference/lib/typert.host.js:649`（SubagentCatalogEvent）；
// 校验：`dsh-session-format-v3-to-v4/lib/index.js:1055-1056`（nativeCatalogFact）、
// `:926-930`（历史版本 catalogFact）。
func TestSubagentCatalogV3Accepted(t *testing.T) {
	data := json.RawMessage(`{"version":3,"childId":"c1","childCreatedAt":1700,"mode":"continuable","label":"调研员","name":"worker-1"}`)
	fact, err := ValidateSubagentCatalogPayload(data)
	if err != nil {
		t.Fatalf("合法 v3 必须通过：%v", err)
	}
	if fact.Version != 3 || fact.Name != "worker-1" || fact.Mode != "continuable" || fact.Label != "调研员" {
		t.Fatalf("解析结果不对：%+v", fact)
	}
	// external 允许省略 label。
	if _, err := ValidateSubagentCatalogPayload(json.RawMessage(`{"version":3,"childId":"c2","childCreatedAt":1,"mode":"external","name":"kid"}`)); err != nil {
		t.Fatalf("v3 external 无 label 必须通过：%v", err)
	}
}

func TestSubagentCatalogV3NameRules(t *testing.T) {
	base := func(name string) string {
		return `{"version":3,"childId":"c1","childCreatedAt":1,"mode":"continuable","label":"L","name":"` + name + `"}`
	}
	// 正则 `^[a-z][a-z0-9-]{0,31}$`、保留名 `lead`
	valid := []string{"a", "worker-1", "w0", "abcdefghijklmnopqrstuvwxyzabcdef", "team-lead-2"}
	for _, name := range valid {
		if _, err := ValidateSubagentCatalogPayload(json.RawMessage(base(name))); err != nil {
			t.Fatalf("name %q 应该合法：%v", name, err)
		}
	}
	invalid := []string{"lead", "Lead", "WORKER", "1worker", "-worker", "worker_1", "工人", strings.Repeat("a", 33)}
	for _, name := range invalid {
		if _, err := ValidateSubagentCatalogPayload(json.RawMessage(base(name))); err == nil {
			t.Fatalf("name %q 必须被拒（正则/保留名）", name)
		}
	}
}

func TestSubagentCatalogV3FieldRules(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"continuable 缺 label", `{"version":3,"childId":"c1","childCreatedAt":1,"mode":"continuable","name":"w1"}`},
		{"缺 name", `{"version":3,"childId":"c1","childCreatedAt":1,"mode":"continuable","label":"L"}`},
		{"缺 childId", `{"version":3,"childCreatedAt":1,"mode":"continuable","label":"L","name":"w1"}`},
		{"缺 childCreatedAt", `{"version":3,"childId":"c1","mode":"continuable","label":"L","name":"w1"}`},
		{"mode one-shot（v3 不允许）", `{"version":3,"childId":"c1","childCreatedAt":1,"mode":"one-shot","name":"w1"}`},
		{"label 非字符串", `{"version":3,"childId":"c1","childCreatedAt":1,"mode":"continuable","label":3,"name":"w1"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ValidateSubagentCatalogPayload(json.RawMessage(c.data)); err == nil {
				t.Fatalf("必须被拒：%s", c.data)
			}
		})
	}
}

// v0/1/2 必须继续可读（官方 catalogFact 的逐条约束照抄）：
//   - v2 ⇔ mode==='external'；v0 不允许 'unknown'；v1 允许 'unknown'。
func TestSubagentCatalogHistoricalVersionsStillReadable(t *testing.T) {
	valid := []string{
		`{"version":0,"childId":"c0","childCreatedAt":1,"mode":"continuable","label":"L"}`,
		`{"version":0,"childId":"c0","childCreatedAt":1,"mode":"one-shot"}`,
		`{"version":1,"childId":"c1","childCreatedAt":1,"mode":"unknown"}`,
		`{"version":2,"childId":"c2","childCreatedAt":1,"mode":"external","label":"L"}`,
		`{"version":2,"childId":"c2","childCreatedAt":1,"mode":"external"}`,
	}
	for _, data := range valid {
		if _, err := ValidateSubagentCatalogPayload(json.RawMessage(data)); err != nil {
			t.Fatalf("历史版本必须可读：%s → %v", data, err)
		}
	}
	invalid := []string{
		`{"version":0,"childId":"c0","childCreatedAt":1,"mode":"unknown"}`,
		`{"version":2,"childId":"c2","childCreatedAt":1,"mode":"continuable","label":"L"}`,
		`{"version":1,"childId":"c1","childCreatedAt":1,"mode":"external"}`,
		`{"version":4,"childId":"c4","childCreatedAt":1,"mode":"external"}`,
		`{"version":1,"childId":"c1","childCreatedAt":1,"mode":"continuable"}`,
	}
	for _, data := range invalid {
		if _, err := ValidateSubagentCatalogPayload(json.RawMessage(data)); err == nil {
			t.Fatalf("必须被拒：%s", data)
		}
	}
}

// 同代唯一性：childId 恒唯一；`name` 只对写了 name 的目录事实（v3）去重
// （`dsh-session-format-v3-to-v4/lib/index.js:1037-1046`）。
func TestSubagentCatalogGenerationUniqueness(t *testing.T) {
	inherited := int64(2)
	events := []v4.Event{
		// 继承代（seq < inheritedEventCount）不参与去重：祖先可以与本代同名。
		catalogEvent(t, 0, map[string]any{"version": 3, "childId": "old", "childCreatedAt": 1, "mode": "external", "name": "worker-1"}),
		testEvent(t, "turn/start", 1, map[string]any{"turn": 1}),
		catalogEvent(t, 2, map[string]any{"version": 3, "childId": "a", "childCreatedAt": 2, "mode": "continuable", "label": "L", "name": "worker-2"}),
		testEvent(t, "turn/start", 3, map[string]any{"turn": 2}),
		catalogEvent(t, 4, map[string]any{"version": 3, "childId": "b", "childCreatedAt": 3, "mode": "external", "name": "worker-3"}),
	}
	if err := ValidateSubagentCatalogGeneration(events, inherited); err != nil {
		t.Fatalf("本代名字各不同应通过：%v", err)
	}

	dupName := append([]v4.Event(nil), events...)
	dupName = append(dupName, catalogEvent(t, 5, map[string]any{"version": 3, "childId": "c", "childCreatedAt": 4, "mode": "external", "name": "worker-3"}))
	if err := ValidateSubagentCatalogGeneration(dupName, inherited); err == nil {
		t.Fatalf("同代重名必须被拒")
	}

	dupID := append([]v4.Event(nil), events...)
	dupID = append(dupID, catalogEvent(t, 5, map[string]any{"version": 3, "childId": "a", "childCreatedAt": 4, "mode": "external", "name": "worker-9"}))
	if err := ValidateSubagentCatalogGeneration(dupID, inherited); err == nil {
		t.Fatalf("同代重复 childId 必须被拒")
	}

	// 历史版本没有 name：不参与重名判定（否则三个 v1 目录会被误判成重名）。
	historic := []v4.Event{
		catalogEvent(t, 0, map[string]any{"version": 1, "childId": "x", "childCreatedAt": 1, "mode": "unknown"}),
		catalogEvent(t, 1, map[string]any{"version": 1, "childId": "y", "childCreatedAt": 2, "mode": "unknown"}),
	}
	if err := ValidateSubagentCatalogGeneration(historic, 0); err != nil {
		t.Fatalf("历史版本无 name，不该触发重名：%v", err)
	}
}

// 写入端构造器：rc.2 起按 v3 写（Lead 裁决：接受前向不兼容，旧 v0/1/2 保持可读）。
func TestSubagentCatalogV3Builder(t *testing.T) {
	payload := SubagentCatalogV3("kid-1", 1700, "continuable", "调研员", "worker-1")
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if decoded["version"] != float64(3) || decoded["name"] != "worker-1" || decoded["mode"] != "continuable" || decoded["label"] != "调研员" {
		t.Fatalf("v3 构造器形状不对：%s", raw)
	}
	if _, err := ValidateSubagentCatalogPayload(raw); err != nil {
		t.Fatalf("自己写出的 v3 载荷必须过校验：%v", err)
	}
	// external 不写 label（官方 v3 external 的 label 可选，缺省即不出现）。
	ext := SubagentCatalogV3("kid-2", 1800, "external", "", "worker-2")
	if _, has := ext["label"]; has {
		t.Fatalf("external 空 label 不该落成空串键：%v", ext)
	}
}
