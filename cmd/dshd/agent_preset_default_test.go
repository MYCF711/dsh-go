package main

// 官方 agent-preset registry 的 `selectedDefault` 设置通道：真的写一次、真的生效。
//
// 官方契约（随 rc.2 原件）：
//   - `dsh-client-ui-agent-preset/src/client/settings-store.ts:13,29-30`：
//     管理界面调 `ctx.remote.settings.update('agent-preset-registry', {selectedDefault: id}, rev)`；
//   - `dsh-agent-preset-registry/src/index.ts:58,82`：
//     `static Config = z.object({default: z.string().required(), selectedDefault: z.string().volatile()})`，
//     `get defaultId() { return this.config.selectedDefault.get() ?? this.config.default }`。
//
// 所以"补上这个通道"不是加一个只读字段：写进去的 id 必须真的成为后续会话的默认
// preset、也必须真的改变 `agentPresets.list.isDefault`。本文件的断言全部落在
// 这两件可观察事实上，写一个不存在的 id 则必须在写路径被拒（不许静默回落）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/settingsform"
)

// writeUserPreset 造一个真实存在的用户 preset（`<home>/modes/*.toml`）。
func writeUserPreset(t *testing.T, home, id, name string) {
	t.Helper()
	dir := filepath.Join(home, "modes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "id = \"" + id + "\"\nname = \"" + name + "\"\ndescription = \"用户自建\"\ntools = [\"read\"]\n"
	if err := os.WriteFile(filepath.Join(dir, id+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

type settingsHTTPHarness struct {
	t       *testing.T
	handler http.Handler
}

func newSettingsHTTPHarness(t *testing.T, home string) *settingsHTTPHarness {
	t.Helper()
	store, _, _ := openSettingsStores(t, home, "base-model", "ask")
	return &settingsHTTPHarness{
		t: t,
		handler: newSettingsFormHTTPHandlerWithService(
			settingsform.NewService(store, settingsform.HostRegistry())),
	}
}

// post 打一次真实的 `/api/settings.*`，返回解码后的信封。
func (h *settingsHTTPHarness) post(method string, params any) (int, map[string]any) {
	h.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		h.t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(string(raw))))
	var envelope map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			h.t.Fatalf("%s 响应不是 JSON: %s", method, rec.Body.String())
		}
	}
	return rec.Code, envelope
}

// namespaceOf 从 describe/mutate 的结果里取一个命名空间视图。
func namespaceOf(t *testing.T, result map[string]any, ns string) map[string]any {
	t.Helper()
	namespaces, _ := result["namespaces"].([]any)
	for _, candidate := range namespaces {
		view, _ := candidate.(map[string]any)
		if view["ns"] == ns {
			return view
		}
	}
	t.Fatalf("settings.describe 没有命名空间 %q：%v", ns, namespaces)
	return nil
}

func TestAgentPresetDefaultSettingsNamespaceEndToEnd(t *testing.T) {
	f := newAgentAPIFixture(t)
	home := f.home
	writeUserPreset(t, home, "review", "Review")
	settings := newSettingsHTTPHarness(t, home)

	// 1) describe 发布官方命名空间，且默认值就是宿主内置默认 preset。
	code, envelope := settings.post("settings.describe", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("settings.describe HTTP=%d %v", code, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	view := namespaceOf(t, result, settingsform.NamespaceAgentPreset)
	if view["applies"] != "live" {
		t.Fatalf("agent-preset 命名空间 applies=%v，期望 live", view["applies"])
	}
	value, _ := view["value"].(map[string]any)
	if value[settingsform.FieldSelectedDefault] != settingsform.AgentPresetFallback {
		t.Fatalf("未写入时的 value=%v，期望 selectedDefault=%q", value, settingsform.AgentPresetFallback)
	}
	schemaJSON, err := json.Marshal(view["schema"])
	if err != nil || !strings.Contains(string(schemaJSON), settingsform.FieldSelectedDefault) {
		t.Fatalf("schema 里没有 %s：%s（err=%v）", settingsform.FieldSelectedDefault, schemaJSON, err)
	}
	revision := int64(view["revision"].(float64))

	// 2) 真的写一次；写进去的 id 必须真实存在。
	code, envelope = settings.post("settings.mutate", map[string]any{
		"ns": settingsform.NamespaceAgentPreset,
		"ops": []map[string]any{{
			"op": "set", "path": []string{settingsform.FieldSelectedDefault}, "value": "review",
		}},
		"expectedRevision": revision,
	})
	if code != http.StatusOK {
		t.Fatalf("写 selectedDefault HTTP=%d %v", code, envelope)
	}

	// 3) 生效一：解析结果（会话创建、投影回落都用它）。
	if got := selectedAgentPresetDefault(home); got != "review" {
		t.Fatalf("selectedAgentPresetDefault=%q，期望 review", got)
	}

	// 4) 生效二：agentPresets.list 的 isDefault 跟着走。
	roster, err := f.api.AgentPresetsList()
	if err != nil {
		t.Fatalf("agentPresets.list: %v", err)
	}
	var reviewDefault, fallbackDefault bool
	for _, preset := range roster.Presets {
		if preset.ID == "review" {
			reviewDefault = preset.IsDefault
		}
		if preset.ID == settingsform.AgentPresetFallback {
			fallbackDefault = preset.IsDefault
		}
	}
	if !reviewDefault || fallbackDefault {
		t.Fatalf("isDefault 没跟着 selectedDefault 走：%+v", roster.Presets)
	}

	// 5) 生效三：真的新建一个会话，v4 header 里写的是它。
	cwd := t.TempDir()
	create, err := serveCreate(home, cwd)
	if err != nil {
		t.Fatalf("serveCreate: %v", err)
	}
	id, err := create("")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path, err := session.FindByID(home, id)
	if err != nil {
		t.Fatalf("找不到刚建的会话 %s: %v", id, err)
	}
	header, err := v4.ReadHeader(path)
	if err != nil {
		t.Fatalf("读 header: %v", err)
	}
	if header.AgentPreset != "review" {
		t.Fatalf("新会话 header.agentPreset=%q，期望 review（设置没生效）", header.AgentPreset)
	}

	// 6) 复位（官方 volatile 字段的"清空"）：解析回落宿主默认，再次新建回到 default。
	code, envelope = settings.post("settings.form.reset", map[string]any{
		"ns":               settingsform.NamespaceAgentPreset,
		"path":             []string{settingsform.FieldSelectedDefault},
		"expectedRevision": revision + 1,
	})
	if code != http.StatusOK {
		t.Fatalf("reset HTTP=%d %v", code, envelope)
	}
	if got := selectedAgentPresetDefault(home); got != modes.DefaultID {
		t.Fatalf("复位后 selectedAgentPresetDefault=%q，期望 %q", got, modes.DefaultID)
	}
	id, err = create("")
	if err != nil {
		t.Fatalf("create again: %v", err)
	}
	path, err = session.FindByID(home, id)
	if err != nil {
		t.Fatalf("找不到第二个会话: %v", err)
	}
	header, err = v4.ReadHeader(path)
	if err != nil {
		t.Fatalf("读 header: %v", err)
	}
	if header.AgentPreset != modes.DefaultID {
		t.Fatalf("复位后新会话 header.agentPreset=%q，期望 %q", header.AgentPreset, modes.DefaultID)
	}
}

func TestAgentPresetDefaultRejectsUnknownPreset(t *testing.T) {
	f := newAgentAPIFixture(t)
	home := f.home
	writeUserPreset(t, home, "review", "Review")
	settings := newSettingsHTTPHarness(t, home)

	code, envelope := settings.post("settings.mutate", map[string]any{
		"ns": settingsform.NamespaceAgentPreset,
		"ops": []map[string]any{{
			"op": "set", "path": []string{settingsform.FieldSelectedDefault}, "value": "no-such-preset",
		}},
		"expectedRevision": 0,
	})
	if code == http.StatusOK {
		t.Fatalf("写一个不存在的 preset 竟然成功：%v", envelope)
	}
	failure, _ := envelope["error"].(map[string]any)
	if failure == nil || failure["code"] != "invalid_value" {
		t.Fatalf("拒绝原因不是 invalid_value：%v", envelope)
	}
	// 拒绝必须是"真的没写进去"：解析结果仍是宿主默认，describe 的 revision 也没动。
	if got := selectedAgentPresetDefault(home); got != modes.DefaultID {
		t.Fatalf("被拒的写入还是生效了：selectedAgentPresetDefault=%q", got)
	}
	if _, again := settings.post("settings.describe", map[string]any{}); again != nil {
		result, _ := again["result"].(map[string]any)
		view := namespaceOf(t, result, settingsform.NamespaceAgentPreset)
		if int64(view["revision"].(float64)) != 0 {
			t.Fatalf("被拒的写入推进了 revision：%v", view["revision"])
		}
	}
}

// TestAgentPresetFallbackMatchesModesDefault 把 settingsform 的回落字面量与
// 运行时默认 mode 钉在一起：两边各写一份字面量就会漂，漂了就是"设置里显示的
// 默认"和"真正建的会话"不是同一个 preset。
func TestAgentPresetFallbackMatchesModesDefault(t *testing.T) {
	if settingsform.AgentPresetFallback != modes.DefaultID {
		t.Fatalf("settingsform.AgentPresetFallback=%q，modes.DefaultID=%q",
			settingsform.AgentPresetFallback, modes.DefaultID)
	}
}
