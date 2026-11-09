package transport

// `config.patch.*` 在**真实 handleAPI 分派**里的接线证据。
//
// 与 handlers_config_patch_test.go 的分工：
//   - 那个文件直调 ConfigPatchHandler（证明 handler 自身的语义）；
//   - 这里经 `Server.ServeHTTP` 走完整的分派顺序：cookie 认证 → 描述符校验 →
//     `config.patch.` 前缀分支 → handler。它钉住三件只有接线才会错的事：
//     1. 前缀分支必须排在 `switch Method(name)` 之前，且不能吞掉 config.get/set；
//     2. Options.ConfigPatchHandler 为 nil 时两个方法保持原有 501；
//     3. 描述符级校验（Options.Descriptors）只对已注册方法生效。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/config"
)

// newConfigPatchWiredServer 按 cmd/dshd/serve.go 的装配方式接线：
// store 注入、handler 自建描述符、Options.Descriptors 另登记 config.patch.*。
func newConfigPatchWiredServer(t *testing.T, withHandler bool) (*Server, *config.Store) {
	t.Helper()
	store, err := config.OpenStore(filepath.Join(t.TempDir(), "state", "config-tree.json"))
	if err != nil {
		t.Fatalf("打开配置树失败: %v", err)
	}
	registry := NewRegistry()
	if err := registry.RegisterAll(ConfigPatchDescriptors()); err != nil {
		t.Fatalf("注册 config.patch 描述符失败: %v", err)
	}
	options := Options{Descriptors: registry}
	if withHandler {
		options.ConfigPatchHandler = NewConfigPatchHandler(store, nil) // nil = 自建描述符
	}
	server, err := NewServer(options)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	server.mu.Lock()
	server.cookies["config-patch-wiring"] = struct{}{}
	server.mu.Unlock()
	return server, store
}

// callConfigPatch 走真实 ServeHTTP（带/不带 cookie 由调用方决定）。
func callConfigPatch(t *testing.T, server *Server, method, body string, withCookie bool) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	if withCookie {
		request.AddCookie(&http.Cookie{Name: cookieName, Value: "config-patch-wiring"})
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	var envelope map[string]any
	if raw := recorder.Body.Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("%s 的响应不是 JSON: %v body=%s", method, err, recorder.Body.String())
		}
	}
	return recorder, envelope
}

func configPatchResult(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("缺 result: %+v", envelope)
	}
	return result
}

func configPatchError(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	apiError, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("缺 error: %+v", envelope)
	}
	return apiError
}

func configPatchNumber(t *testing.T, value any, name string) int64 {
	t.Helper()
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("%s 不是数字: %T %v", name, value, value)
	}
	return int64(number)
}

func TestConfigPatchWiringDispatchesThroughHandleAPI(t *testing.T) {
	server, store := newConfigPatchWiredServer(t, true)

	// 0) 没有 cookie：必须 401，且 handler 一步都没走（不能绕过认证门）。
	recorder, _ := callConfigPatch(t, server, "config.patch.get", "", false)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("无 cookie 的 config.patch.get 状态码 = %d，want 401", recorder.Code)
	}
	if store.Revision() != 0 {
		t.Fatalf("未认证请求居然改了配置：revision=%d", store.Revision())
	}

	// 1) config.patch.get：200 + revision 0 + 空树/空扁平表。
	recorder, envelope := callConfigPatch(t, server, "config.patch.get", "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("config.patch.get 状态码 = %d，body=%s", recorder.Code, recorder.Body.String())
	}
	result := configPatchResult(t, envelope)
	if got := configPatchNumber(t, result["revision"], "revision"); got != 0 {
		t.Fatalf("初始 revision = %d，want 0", got)
	}
	if values, ok := result["values"].(map[string]any); !ok || len(values) != 0 {
		t.Fatalf("初始 values = %#v，want 空对象", result["values"])
	}
	if flat, ok := result["flat"].(map[string]any); !ok || len(flat) != 0 {
		t.Fatalf("初始 flat = %#v，want 空对象", result["flat"])
	}

	// 2) config.patch.apply：200 + revision 1 + 嵌套树与扁平投影同时更新。
	applyBody := `{"expectedRevision":0,"ops":[{"op":"set","path":"ui.theme","value":"dark"}]}`
	recorder, envelope = callConfigPatch(t, server, "config.patch.apply", applyBody, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("config.patch.apply 状态码 = %d，body=%s", recorder.Code, recorder.Body.String())
	}
	result = configPatchResult(t, envelope)
	if got := configPatchNumber(t, result["revision"], "revision"); got != 1 {
		t.Fatalf("apply 后 revision = %d，want 1", got)
	}
	if got := configPatchNumber(t, result["applied"], "applied"); got != 1 {
		t.Fatalf("applied = %d，want 1", got)
	}
	ui, ok := result["values"].(map[string]any)["ui"].(map[string]any)
	if !ok || ui["theme"] != "dark" {
		t.Fatalf("values 没有嵌套的 ui.theme: %#v", result["values"])
	}
	if flat, ok := result["flat"].(map[string]any); !ok || flat["ui.theme"] != "dark" {
		t.Fatalf("flat 投影 = %#v，want ui.theme=dark", result["flat"])
	}
	if store.Revision() != 1 {
		t.Fatalf("落盘后 store revision = %d，want 1", store.Revision())
	}

	// 3) 同一份旧 revision 再来一次：409 revision_conflict，带服务端当前 revision。
	recorder, envelope = callConfigPatch(t, server, "config.patch.apply", applyBody, true)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("旧 revision 的 apply 状态码 = %d，want 409，body=%s", recorder.Code, recorder.Body.String())
	}
	apiError := configPatchError(t, envelope)
	if apiError["code"] != "revision_conflict" {
		t.Fatalf("冲突 code = %v，want revision_conflict", apiError["code"])
	}
	if got := configPatchNumber(t, envelope["revision"], "revision"); got != 1 {
		t.Fatalf("冲突响应里的服务端 revision = %d，want 1", got)
	}
	if got := configPatchNumber(t, envelope["expectedRevision"], "expectedRevision"); got != 0 {
		t.Fatalf("冲突响应里的 expectedRevision = %d，want 0", got)
	}
	if store.Revision() != 1 {
		t.Fatalf("冲突不该写盘：revision=%d", store.Revision())
	}

	// 4) 描述符级校验先于 handler：未知参数 400（否则会被静默忽略成"改了没生效"）。
	recorder, envelope = callConfigPatch(t, server, "config.patch.apply",
		`{"expectedRevision":1,"ops":[{"op":"set","path":"a","value":1}],"tpyo":true}`, true)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("未知参数的状态码 = %d，want 400，body=%s", recorder.Code, recorder.Body.String())
	}
	if configPatchError(t, envelope)["code"] != "bad_request" {
		t.Fatalf("未知参数 code = %v，want bad_request", configPatchError(t, envelope)["code"])
	}
	if store.Revision() != 1 {
		t.Fatalf("被拒的补丁不该写盘：revision=%d", store.Revision())
	}

	// 5) `config.patch.` 前缀不能吞掉 `config.get`：没有 ConfigGet 回调时仍是既有 501。
	recorder, envelope = callConfigPatch(t, server, "config.get", "", true)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("config.get 状态码 = %d，want 501（前缀分支吞掉了它？）", recorder.Code)
	}
	if configPatchError(t, envelope)["code"] != "not_implemented" {
		t.Fatalf("config.get code = %v，want not_implemented", configPatchError(t, envelope)["code"])
	}
}

func TestConfigPatchWiringNilHandlerKeepsNotImplemented(t *testing.T) {
	// 没接线 = 501。这是既有未接线语义，接线不能把"没接"变成 200 / 500。
	server, store := newConfigPatchWiredServer(t, false)

	recorder, envelope := callConfigPatch(t, server, "config.patch.get", "", true)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("nil handler 的 config.patch.get 状态码 = %d，want 501，body=%s", recorder.Code, recorder.Body.String())
	}
	if configPatchError(t, envelope)["code"] != "not_implemented" {
		t.Fatalf("nil handler code = %v，want not_implemented", configPatchError(t, envelope)["code"])
	}

	recorder, _ = callConfigPatch(t, server, "config.patch.apply",
		`{"expectedRevision":0,"ops":[{"op":"set","path":"a","value":1}]}`, true)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("nil handler 的 config.patch.apply 状态码 = %d，want 501", recorder.Code)
	}
	if store.Revision() != 0 {
		t.Fatalf("nil handler 居然写了配置：revision=%d", store.Revision())
	}
}

func TestConfigPatchWiringPrefixDoesNotSwallowSiblings(t *testing.T) {
	// 前缀判定是 `config.patch.`：`config.patchy.get` 之类不该被接走。
	server, _ := newConfigPatchWiredServer(t, true)
	for _, method := range []string{"config.patches.get", "config.patch", "config.patchx.get"} {
		recorder, _ := callConfigPatch(t, server, method, "", true)
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("%s 状态码 = %d，want 501（前缀过宽）", method, recorder.Code)
		}
	}
}
