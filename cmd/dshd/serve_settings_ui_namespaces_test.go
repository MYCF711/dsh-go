package main

// 官方 4 个客户端 UI 设置命名空间（locale / ui-theme / ui-chat / ui-settings）
// 的 L1 Transport 行为：真 handler + 真 <home>/state/settings-namespaces.json。
//
// 真机截图暴露的缺口正是这 4 行：宿主没发布命名空间时设置面板只能显示「未知」。
// 这里钉的是"接上之后真的能读能写"：describe 报全、form.set 写进去、
// form.get 读回来、非法值四字段拒绝、form.reset 回官方默认、
// transcriptView 旧值读作 detailed 且不回写、重启（新 Store）后值还在。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const uiClientNamespacesJSON = `{
  "version": 1,
  "namespaces": {
    "ui-chat": { "revision": 3, "value": { "transcriptView": "normal" } }
  }
}`

func postSettingsL1(t *testing.T, handler http.Handler, method string, params map[string]any) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("序列化 %s 参数: %v", method, err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/"+method, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	payload := map[string]any{}
	if raw := recorder.Body.Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("%s 响应不是 JSON: %v 原文=%s", method, err, raw)
		}
	}
	return recorder.Code, payload
}

// settingsL1Describe 调一次 settings.describe 并返回 result。
func settingsL1Describe(t *testing.T, handler http.Handler) map[string]any {
	t.Helper()
	status, payload := postSettingsL1(t, handler, "settings.describe", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("settings.describe status=%d payload=%v", status, payload)
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("settings.describe 缺 result: %v", payload)
	}
	return result
}

func settingsL1Section(t *testing.T, result map[string]any, ns string) map[string]any {
	t.Helper()
	sections, ok := result["sections"].([]any)
	if !ok {
		t.Fatalf("describe 缺 sections: %v", result)
	}
	for _, raw := range sections {
		section, ok := raw.(map[string]any)
		if ok && section["ns"] == ns {
			return section
		}
	}
	t.Fatalf("sections 里没有 %s: %v", ns, result["sections"])
	return nil
}

func settingsL1Namespace(t *testing.T, result map[string]any, ns string) map[string]any {
	t.Helper()
	namespaces, ok := result["namespaces"].([]any)
	if !ok {
		t.Fatalf("describe 缺 namespaces: %v", result)
	}
	for _, raw := range namespaces {
		namespace, ok := raw.(map[string]any)
		if ok && namespace["ns"] == ns {
			return namespace
		}
	}
	t.Fatalf("namespaces 里没有 %s: %v", ns, result["namespaces"])
	return nil
}

// settingsJSONEqual 按 JSON 形状比较（Go 的 int 18 与 JSON 的 18 是同一个值）。
func settingsJSONEqual(t *testing.T, got, want any) bool {
	t.Helper()
	normalize := func(value any) any {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("序列化 %v: %v", value, err)
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("反序列化 %s: %v", raw, err)
		}
		return out
	}
	return reflect.DeepEqual(normalize(got), normalize(want))
}

func settingsL1Field(t *testing.T, section map[string]any, name string) map[string]any {
	t.Helper()
	fields, ok := section["fields"].([]any)
	if !ok {
		t.Fatalf("section 缺 fields: %v", section)
	}
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if ok && field["name"] == name {
			return field
		}
	}
	t.Fatalf("section %v 没有字段 %q: %v", section["ns"], name, fields)
	return nil
}

// TestSettingsFormUIClientNamespacesOverHTTP：6 个客户端 UI 命名空间的 describe /
// set / get / 非法值 / reset 全链路。
func TestSettingsFormUIClientNamespacesOverHTTP(t *testing.T) {
	home := t.TempDir()
	store, _, _ := openSettingsStores(t, home, "base-model", "allow")
	handler := newSettingsFormHTTPHandler(store)

	result := settingsL1Describe(t, handler)
	namespaces, ok := result["namespaces"].([]any)
	if !ok || len(namespaces) != 9 {
		t.Fatalf("settings.describe 必须报 9 个命名空间（model/permission/llm + 6 个客户端 UI）：%v", result["namespaces"])
	}
	sections, ok := result["sections"].([]any)
	if !ok || len(sections) != 9 {
		t.Fatalf("settings.describe 必须报 9 个 section：%v", result["sections"])
	}

	defaults := []struct {
		ns   string
		want map[string]any
	}{
		{"locale", map[string]any{}},
		{"ui-theme", map[string]any{
			"preference":         "system",
			"fontSize":           float64(14),
			"codeFontSize":       float64(11),
			"terminalFontSize":   float64(13),
			"textFontFamily":     "",
			"codeFontFamily":     "",
			"terminalFontFamily": "",
		}},
		{"ui-chat", map[string]any{
			"transcriptView":   "detailed",
			"collapseTiming":   "completion",
			"foldedReplies":    "all",
			"performanceUsage": "detailed",
			"linkOpening":      "sidebar",
		}},
		{"ui-settings", map[string]any{"enabled": true}},
		// 2026-11-13 补：官方 submission-settings.ts:19 DEFAULT_BUSY_ENTER_BEHAVIOR='queue'。
		{"ui-conversation", map[string]any{"busyEnter": "queue"}},
		// 官方 dsh-session-log-deepseek/src/index.ts:60 `z.boolean().default(true)`。
		{"session-log-deepseek", map[string]any{"enabled": true}},
	}
	for _, entry := range defaults {
		namespace := settingsL1Namespace(t, result, entry.ns)
		value, ok := namespace["value"].(map[string]any)
		if !ok {
			t.Fatalf("%s 的 namespaces[].value 必须是对象：%v", entry.ns, namespace["value"])
		}
		if !reflect.DeepEqual(value, entry.want) {
			t.Fatalf("%s 的默认 value=%v，want %v", entry.ns, value, entry.want)
		}
		if namespace["applies"] != "live" {
			t.Fatalf("%s 的 applies 必须是 live，实际 %v", entry.ns, namespace["applies"])
		}
		schema, ok := namespace["schema"].(map[string]any)
		if !ok || schema["refs"] == nil {
			t.Fatalf("%s 必须下发 schemastery envelope：%v", entry.ns, namespace["schema"])
		}
		section := settingsL1Section(t, result, entry.ns)
		if section["writable"] != true {
			t.Fatalf("%s 的 section 必须可写：%v", entry.ns, section)
		}
		fields, ok := section["fields"].([]any)
		if !ok || len(fields) != len(entry.want) {
			// locale 的 preference 声明了但官方没有默认值：字段数对不上是配置错误。
			if entry.ns != "locale" || len(fields) != 1 {
				t.Fatalf("%s 的 sections 字段数=%v，want %d", entry.ns, fields, len(entry.want))
			}
		}
		for _, raw := range fields {
			field := raw.(map[string]any)
			want, present := entry.want[field["name"].(string)]
			if !present {
				if field["current"] != nil || field["default"] != nil {
					t.Fatalf("%s.%v 官方没有默认值，current/default 必须是 null：%v", entry.ns, field["name"], field)
				}
				continue
			}
			if !reflect.DeepEqual(field["current"], want) {
				t.Fatalf("%s.%v 的 current=%v，want %v", entry.ns, field["name"], field["current"], want)
			}
			if !reflect.DeepEqual(field["default"], want) {
				t.Fatalf("%s.%v 的 default=%v，want %v", entry.ns, field["name"], field["default"], want)
			}
		}
	}

	// 写合法值：14 个字段全部走一遍 settings.form.set，再 form.get 读回来。
	writes := []struct {
		ns    string
		field string
		value any
	}{
		{"locale", "preference", "zh-Hans"},
		{"ui-theme", "preference", "dark"},
		{"ui-theme", "fontSize", 18},
		{"ui-theme", "codeFontSize", 16},
		{"ui-theme", "terminalFontSize", 10},
		{"ui-theme", "textFontFamily", "Noto Sans SC, sans-serif"},
		{"ui-theme", "codeFontFamily", "JetBrains Mono"},
		{"ui-theme", "terminalFontFamily", "Cascadia Mono"},
		{"ui-chat", "transcriptView", "verbose"},
		{"ui-chat", "collapseTiming", "next-input"},
		{"ui-chat", "foldedReplies", "final"},
		{"ui-chat", "performanceUsage", "compact"},
		{"ui-chat", "linkOpening", "new-tab"},
		{"ui-settings", "enabled", false},
	}
	for _, entry := range writes {
		// expectedRevision 省略 = 不做栅栏；这里要按顺序写满 14 个字段。
		status, payload := postSettingsL1(t, handler, "settings.form.set", map[string]any{
			"ns": entry.ns, "path": []string{entry.field}, "value": entry.value,
		})
		if status != http.StatusOK {
			t.Fatalf("%s.%s 写入失败 status=%d payload=%v", entry.ns, entry.field, status, payload)
		}
		section, ok := payload["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s.%s 写入结果缺 result：%v", entry.ns, entry.field, payload)
		}
		if got := settingsL1Field(t, section, entry.field)["current"]; !settingsJSONEqual(t, got, entry.value) {
			t.Fatalf("%s.%s 写入后 current=%v，want %v", entry.ns, entry.field, got, entry.value)
		}
	}
	for _, entry := range writes {
		status, payload := postSettingsL1(t, handler, "settings.form.get", map[string]any{"ns": entry.ns})
		if status != http.StatusOK {
			t.Fatalf("%s form.get 失败 status=%d payload=%v", entry.ns, status, payload)
		}
		section := payload["result"].(map[string]any)
		if got := settingsL1Field(t, section, entry.field)["current"]; !settingsJSONEqual(t, got, entry.value) {
			t.Fatalf("%s.%s form.get 读回 current=%v，want %v", entry.ns, entry.field, got, entry.value)
		}
		if overridden, ok := settingsL1Field(t, section, entry.field)["overridden"].(bool); !ok || !overridden {
			t.Fatalf("%s.%s 写入后必须标 overridden：%v", entry.ns, entry.field, settingsL1Field(t, section, entry.field))
		}
	}

	// 非法值：枚举外 / 越界 / 坏 BCP47 各自四字段错误，且不改持久值。
	illegal := []struct {
		ns    string
		field string
		value any
		code  string
	}{
		{"ui-theme", "preference", "purple", "invalid_enum"},
		{"ui-theme", "fontSize", 23, "out_of_range"},
		{"ui-chat", "transcriptView", "bogus", "invalid_enum"},
		{"locale", "preference", "zh_CN", "invalid_value"},
		{"ui-settings", "enabled", "yes", "invalid_type"},
	}
	for _, entry := range illegal {
		status, payload := postSettingsL1(t, handler, "settings.form.set", map[string]any{
			"ns": entry.ns, "path": []string{entry.field}, "value": entry.value,
		})
		if status != http.StatusBadRequest {
			t.Fatalf("%s.%s 写 %v 必须 400，实际 %d：%v", entry.ns, entry.field, entry.value, status, payload)
		}
		apiErr, ok := payload["error"].(map[string]any)
		if !ok {
			t.Fatalf("%s.%s 拒绝必须返回四字段 error：%v", entry.ns, entry.field, payload)
		}
		if apiErr["code"] != entry.code {
			t.Fatalf("%s.%s 的错误码=%v，want %q（%v）", entry.ns, entry.field, apiErr["code"], entry.code, apiErr)
		}
		for _, key := range []string{"code", "message", "hint", "retryable"} {
			if _, present := apiErr[key]; !present {
				t.Fatalf("%s.%s 的四字段错误缺 %q：%v", entry.ns, entry.field, key, apiErr)
			}
		}
		if apiErr["retryable"] != false {
			t.Fatalf("%s.%s 的参数类错误不许 retryable：%v", entry.ns, entry.field, apiErr)
		}
	}

	// reset：整段回官方默认值。
	for _, entry := range []struct {
		ns   string
		want map[string]any
	}{
		{"locale", map[string]any{}},
		{"ui-theme", map[string]any{
			"preference": "system", "fontSize": float64(14), "codeFontSize": float64(11),
			"terminalFontSize": float64(13), "textFontFamily": "", "codeFontFamily": "", "terminalFontFamily": "",
		}},
		{"ui-chat", map[string]any{
			"transcriptView": "detailed", "collapseTiming": "completion", "foldedReplies": "all",
			"performanceUsage": "detailed", "linkOpening": "sidebar",
		}},
		{"ui-settings", map[string]any{"enabled": true}},
	} {
		status, payload := postSettingsL1(t, handler, "settings.form.reset", map[string]any{"ns": entry.ns})
		if status != http.StatusOK {
			t.Fatalf("%s reset 失败 status=%d payload=%v", entry.ns, status, payload)
		}
		namespace := settingsL1Namespace(t, settingsL1Describe(t, handler), entry.ns)
		if !reflect.DeepEqual(namespace["value"], entry.want) {
			t.Fatalf("%s reset 后 value=%v，want %v", entry.ns, namespace["value"], entry.want)
		}
	}

	// 持久化：同一个 home 重新开 store（= 重启），值必须还在。
	themeSetStatus, themeSetPayload := postSettingsL1(t, handler, "settings.form.set", map[string]any{
		"ns": "ui-theme", "path": []string{"fontSize"}, "value": 20,
	})
	if themeSetStatus != http.StatusOK {
		t.Fatalf("重启前写 ui-theme.fontSize 失败：%d %v", themeSetStatus, themeSetPayload)
	}
	reopened, _, _ := openSettingsStores(t, home, "base-model", "allow")
	reopenedHandler := newSettingsFormHTTPHandler(reopened)
	section := settingsL1Section(t, settingsL1Describe(t, reopenedHandler), "ui-theme")
	if got := settingsL1Field(t, section, "fontSize")["current"]; got != float64(20) {
		t.Fatalf("重启后 ui-theme.fontSize 必须仍是 20，实际 %v", got)
	}
	raw, err := os.ReadFile(filepath.Join(home, "state", "settings-namespaces.json"))
	if err != nil {
		t.Fatalf("读设置文档：%v", err)
	}
	if !bytes.Contains(raw, []byte(`"ui-theme"`)) {
		t.Fatalf("ui-theme 必须落进 <home>/state/settings-namespaces.json：%s", raw)
	}
}

// TestSettingsFormUIClientNamespaceLegacyTranscriptViewOverHTTP：真实设置文档里
// 存着旧值 normal 时，宿主读作 detailed（describe 与 form.get 两条出口），
// 且**不回写**——文档里仍是 normal。
func TestSettingsFormUIClientNamespaceLegacyTranscriptViewOverHTTP(t *testing.T) {
	home := t.TempDir()
	stateDir := filepath.Join(home, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("建 state 目录：%v", err)
	}
	path := filepath.Join(stateDir, "settings-namespaces.json")
	if err := os.WriteFile(path, []byte(uiClientNamespacesJSON), 0o644); err != nil {
		t.Fatalf("写设置文档：%v", err)
	}
	store, _, _ := openSettingsStores(t, home, "base-model", "allow")
	handler := newSettingsFormHTTPHandler(store)

	described := settingsL1Describe(t, handler)
	namespace := settingsL1Namespace(t, described, "ui-chat")
	value := namespace["value"].(map[string]any)
	if value["transcriptView"] != "detailed" {
		t.Fatalf("旧值 normal 的 value.transcriptView 必须读作 detailed，实际 %v", value["transcriptView"])
	}
	user, ok := namespace["user"].(map[string]any)
	if !ok || user["transcriptView"] != "normal" {
		t.Fatalf("user 段必须原样保留旧值 normal（不写回），实际 %v", namespace["user"])
	}
	section := settingsL1Section(t, described, "ui-chat")
	if got := settingsL1Field(t, section, "transcriptView")["current"]; got != "detailed" {
		t.Fatalf("旧值 normal 的 section.current 必须读作 detailed，实际 %v", got)
	}
	enum, ok := settingsL1Field(t, section, "transcriptView")["enum"].([]any)
	if !ok || len(enum) != 4 {
		t.Fatalf("transcriptView 的选项必须只有当前 4 个模式，实际 %v", settingsL1Field(t, section, "transcriptView")["enum"])
	}
	for _, option := range enum {
		if option == "normal" || option == "expanded" {
			t.Fatalf("旧值 %v 不许出现在选项里：%v", option, enum)
		}
	}
	status, payload := postSettingsL1(t, handler, "settings.form.get", map[string]any{"ns": "ui-chat"})
	if status != http.StatusOK {
		t.Fatalf("form.get ui-chat 失败：%d %v", status, payload)
	}
	if got := settingsL1Field(t, payload["result"].(map[string]any), "transcriptView")["current"]; got != "detailed" {
		t.Fatalf("form.get 的 current 必须是 detailed，实际 %v", got)
	}
	// 读了一轮之后磁盘上仍是旧值：不回写。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读设置文档：%v", err)
	}
	if !bytes.Contains(raw, []byte(`"normal"`)) {
		t.Fatalf("旧值被回写了（磁盘上 should 还是 normal）：%s", raw)
	}
	if bytes.Contains(raw, []byte(`"detailed"`)) {
		t.Fatalf("旧值被回写成 detailed（官方裁定不许回写）：%s", raw)
	}
}
