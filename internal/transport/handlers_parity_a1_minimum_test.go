package transport

// PARITY-A1-MINIMUM：三个最小真能力的方法级契约测试。
//
// 基线（T19W3 dshd）对三者都是通用 501；这里钉住修复后的真实结果：
//   - cotTranslation.limits：真实上限 + 真实可用 provider，不列不可用的 google/bing；
//   - permissionPresets.catalog：三档真实 sandbox preset + 真实默认，不伪造 auto/custom；
//   - pluginInventory.list：宿主注入的真实 entries，未接线时点名 501，不空成功。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func parityServe(t *testing.T, h http.HandlerFunc, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	raw := rec.Body.Bytes()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s 响应不是 JSON：%v；body=%s", method, err, raw)
	}
	// writeJSON 统一包一层 result；本测试只关心结果载荷。
	if result, ok := out["result"].(map[string]any); ok {
		out = result
	}
	return rec.Code, out
}

func TestOfficialCotTranslationLimits(t *testing.T) {
	s := &Server{}
	code, out := parityServe(t, s.ServeOfficialCotTranslationLimitsAPI, "cotTranslation.limits", `{}`)
	if code != http.StatusOK {
		t.Fatalf("limits HTTP=%d：%v", code, out)
	}
	if out["maxTextChars"] != float64(cotTranslationMaxChars) {
		t.Fatalf("maxTextChars=%v，期望 %d", out["maxTextChars"], cotTranslationMaxChars)
	}
	prefs, _ := out["preferences"].(map[string]any)
	if prefs["provider"] != cotProviderDSOfficial || prefs["targetLanguage"] != "auto" {
		t.Fatalf("preferences 不对：%v", prefs)
	}
	providers, _ := out["availableProviders"].([]any)
	if len(providers) != 2 || providers[0] != cotProviderDSOfficial || providers[1] != cotProviderDSAccount {
		t.Fatalf("availableProviders 必须只列真正可走的网关路径：%v", providers)
	}
	for _, bad := range []string{cotProviderGoogle, cotProviderBing} {
		for _, p := range providers {
			if p == bad {
				t.Fatalf("不可用的 provider %q 被列进 availableProviders", bad)
			}
		}
	}

	// 官方参数为空；多余字段必须 400，不能静默忽略。
	code, _ = parityServe(t, s.ServeOfficialCotTranslationLimitsAPI, "cotTranslation.limits", `{"x":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("limits 多余参数 HTTP=%d，期望 400", code)
	}
}

func TestOfficialPermissionPresetsCatalog(t *testing.T) {
	s := &Server{}
	code, out := parityServe(t, s.ServeOfficialPermissionPresetsAPI, "permissionPresets.catalog", `{}`)
	if code != http.StatusOK {
		t.Fatalf("catalog HTTP=%d：%v", code, out)
	}
	opts, _ := out["options"].([]any)
	if len(opts) != 3 {
		t.Fatalf("options 必须是三档真实 sandbox preset，实际 %v", opts)
	}
	want := []string{"read-only", "workspace-write", "danger-full-access"}
	for i, raw := range opts {
		opt := raw.(map[string]any)
		if opt["value"] != want[i] {
			t.Fatalf("options[%d]=%v，期望 %s", i, opt, want[i])
		}
		if opt["name"] == "" || opt["description"] == "" {
			t.Fatalf("option 缺真实 name/description：%v", opt)
		}
	}
	if out["defaultPreset"] != "workspace-write" {
		t.Fatalf("defaultPreset=%v，本仓默认 sandbox 是 workspace-write", out["defaultPreset"])
	}
	defaults, _ := out["defaultOptions"].([]any)
	if len(defaults) != 1 || defaults[0].(map[string]any)["value"] != "workspace-write" {
		t.Fatalf("defaultOptions 只应列真实可写的默认：%v", defaults)
	}
	// 不伪造 auto/custom。
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), `"auto"`) || strings.Contains(string(raw), `"custom"`) {
		t.Fatalf("catalog 不得伪造 auto/custom：%s", raw)
	}

	code, _ = parityServe(t, s.ServeOfficialPermissionPresetsAPI, "permissionPresets.catalog", `{"x":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("catalog 多余参数 HTTP=%d，期望 400", code)
	}
}

func TestOfficialPluginInventoryList(t *testing.T) {
	active := "active"
	management := true
	api := OfficialPluginManagerAPI{
		PluginInventoryList: func() (OfficialPluginInventory, error) {
			return OfficialPluginInventory{
				ManagementAvailable: &management,
				Entries: []OfficialPluginInventoryEntry{
					{EntryID: "acme.echo", ModuleName: "plugin.wasm", Enabled: true, FiberPhase: &active},
					{EntryID: "acme.off", ModuleName: "plugin.wasm", Enabled: false, FiberPhase: nil},
				},
			}, nil
		},
	}
	s, err := NewServer(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.AttachOfficialPluginManagerAPI(api)
	code, out := parityServe(t, s.ServeOfficialPluginInventoryAPI, "pluginInventory.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("inventory HTTP=%d：%v", code, out)
	}
	if out["managementAvailable"] != true {
		t.Fatalf("managementAvailable 必须是真实 true：%v", out)
	}
	entries, _ := out["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries 数=%d，期望 2", len(entries))
	}
	first := entries[0].(map[string]any)
	if first["entryId"] != "acme.echo" || first["moduleName"] != "plugin.wasm" ||
		first["enabled"] != true || first["fiberPhase"] != "active" {
		t.Fatalf("entry 映射不对：%v", first)
	}
	second := entries[1].(map[string]any)
	if second["enabled"] != false || second["fiberPhase"] != nil {
		t.Fatalf("未装载条目必须是 enabled=false/fiberPhase=null：%v", second)
	}
	if _, ok := out["agentPresets"]; ok {
		t.Fatalf("本仓不组合官方 agent-preset roster，不得伪造 agentPresets：%v", out)
	}

	code, _ = parityServe(t, s.ServeOfficialPluginInventoryAPI, "pluginInventory.list", `{"x":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("inventory 多余参数 HTTP=%d，期望 400", code)
	}
}

func TestOfficialPluginInventoryUnwiredIsLoud(t *testing.T) {
	s, err := NewServer(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	code, out := parityServe(t, s.ServeOfficialPluginInventoryAPI, "pluginInventory.list", `{}`)
	if code != http.StatusNotImplemented {
		t.Fatalf("未接线 HTTP=%d，期望 501；%v", code, out)
	}
	errObj, _ := out["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	if errObj["code"] != "not_implemented" || !strings.Contains(msg, "pluginInventory.list") {
		t.Fatalf("未接线必须四字段 501 并点名方法：%v", out)
	}
}

// TestCotTextUTF16Len 钉住 limits 的 maxTextChars 口径：官方按 UTF-16 码元，
// 不是 rune 数。translate 与 limits 必须共用同一判据。
func TestCotTextUTF16Len(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"abc", 3},
		{"你好", 2},
		{"😀", 2}, // U+1F600 在 UTF-16 是代理对
		{"a😀b", 4},
		{"", 0},
	}
	for _, c := range cases {
		if got := cotTextUTF16Len(c.text); got != c.want {
			t.Fatalf("cotTextUTF16Len(%q)=%d，期望 %d", c.text, got, c.want)
		}
	}
}
