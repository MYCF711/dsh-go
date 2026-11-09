package transport

// 先红（HS-14）：`config.get`/`config.set` 只搬扁平 kv，没有 revision，也没有
// 嵌套树。本文件钉住 transport 侧的补丁面：`config.patch.get` / `config.patch.apply`
// 走**同一个** config.Store（internal/config），因此：
//
//   - 读：同时给出嵌套树（新）与扁平投影（旧客户端不回归）；
//   - 写：必须带 expectedRevision，过期就是 409 `revision_conflict`，且带上
//     服务端当前 revision 让客户端能立刻重读；
//   - 派发前先按**描述符**校验参数（typert 层），错参数是 400 `bad_request`，
//     不与"revision 冲突"混成一个码——两者该做的事完全不同；
//   - 没有接线（Store 为 nil）时是 501，不是 500，也不是静默成功。
//
// 这两个方法是 **additive**：不加入 canonical 25，不改 `config.get/set` 的形状。

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/config"
)

func newConfigPatchFixture(t *testing.T) (*ConfigPatchHandler, *config.Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "values.json")
	store, err := config.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.RegisterAll(ConfigPatchDescriptors()); err != nil {
		t.Fatalf("config patch 描述符自身不合法: %v", err)
	}
	return NewConfigPatchHandler(store, registry), store, dir
}

func dispatch(t *testing.T, handler http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("响应不是合法 JSON：%v\n%s", err, rec.Body.String())
	}
	result, ok := doc["result"].(map[string]any)
	if !ok {
		t.Fatalf("成功响应缺 result：%s", rec.Body.String())
	}
	return result
}

func TestConfigPatchGetReturnsTreeAndFlat(t *testing.T) {
	handler, store, _ := newConfigPatchFixture(t)
	if _, err := store.Apply(config.Patch{
		ExpectedRevision: 0,
		Ops: []config.PatchOp{
			{Op: config.OpSet, Path: "ui.theme", Value: "dark"},
			{Op: config.OpSet, Path: "ui.zoom", Value: 1.25},
		},
	}); err != nil {
		t.Fatal(err)
	}

	rec := dispatch(t, handler, string(MethodConfigPatchGet), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d，body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	if got, _ := result["revision"].(float64); got != 1 {
		t.Fatalf("revision = %v，want 1", result["revision"])
	}

	values, ok := result["values"].(map[string]any)
	if !ok {
		t.Fatalf("values 不是对象：%v", result["values"])
	}
	ui, ok := values["ui"].(map[string]any)
	if !ok {
		t.Fatalf("values.ui 不是嵌套对象（这正是基线的缺口）：%v", values)
	}
	if ui["theme"] != "dark" {
		t.Fatalf("ui.theme = %v", ui["theme"])
	}

	flat, ok := result["flat"].(map[string]any)
	if !ok {
		t.Fatalf("flat 不是对象：%v", result["flat"])
	}
	if flat["ui.theme"] != "dark" || flat["ui.zoom"] != "1.25" {
		t.Fatalf("扁平投影不对：%v", flat)
	}
}

func TestConfigPatchApplyBumpsRevision(t *testing.T) {
	handler, store, _ := newConfigPatchFixture(t)
	body := `{"expectedRevision":0,"ops":[{"op":"set","path":"ui.theme","value":"dark"},{"op":"set","path":"ui.zoom","value":1.25}]}`
	rec := dispatch(t, handler, string(MethodConfigPatchApply), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d，body=%s", rec.Code, rec.Body.String())
	}
	result := decodeResult(t, rec)
	if got, _ := result["revision"].(float64); got != 1 {
		t.Fatalf("revision = %v，want 1", result["revision"])
	}
	if got, _ := result["applied"].(float64); got != 2 {
		t.Fatalf("applied = %v，want 2", result["applied"])
	}
	if store.Revision() != 1 {
		t.Fatalf("store revision = %d", store.Revision())
	}
	if got, _ := store.Tree().Get("ui.theme"); got != "dark" {
		t.Fatalf("落不到 store：ui.theme = %#v", got)
	}
}

func TestConfigPatchApplyStaleRevisionIs409(t *testing.T) {
	handler, store, _ := newConfigPatchFixture(t)
	if _, err := store.Apply(config.Patch{ExpectedRevision: 0, Ops: []config.PatchOp{{Op: config.OpSet, Path: "lang", Value: "zh"}}}); err != nil {
		t.Fatal(err)
	}
	body := `{"expectedRevision":0,"ops":[{"op":"set","path":"lang","value":"en"}]}`
	rec := dispatch(t, handler, string(MethodConfigPatchApply), body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d，want 409；body=%s", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("错误响应不是 JSON: %v", err)
	}
	errObj, ok := doc["error"].(map[string]any)
	if !ok {
		t.Fatalf("缺四字段错误信封：%s", rec.Body.String())
	}
	if errObj["code"] != "revision_conflict" {
		t.Fatalf("code = %v，want revision_conflict", errObj["code"])
	}
	for _, key := range []string{"message", "hint", "retryable"} {
		if _, ok := errObj[key]; !ok {
			t.Fatalf("错误信封缺 %q：%v", key, errObj)
		}
	}
	if errObj["retryable"] != false {
		t.Fatalf("revision_conflict 不该标可盲重试：%v", errObj["retryable"])
	}
	// 服务端当前 revision 必须一起给出，客户端否则只能盲猜。
	if got, _ := doc["revision"].(float64); got != 1 {
		t.Fatalf("冲突响应里的 revision = %v，want 1", doc["revision"])
	}
	if got, _ := doc["expectedRevision"].(float64); got != 0 {
		t.Fatalf("冲突响应里的 expectedRevision = %v，want 0", doc["expectedRevision"])
	}
	// 冲突不许落盘。
	if got, _ := store.Tree().Get("lang"); got != "zh" {
		t.Fatalf("冲突时树被改了：lang = %#v", got)
	}
}

func TestConfigPatchApplyValidatesParams(t *testing.T) {
	handler, store, _ := newConfigPatchFixture(t)
	cases := []struct {
		name string
		body string
	}{
		{"缺 expectedRevision", `{"ops":[{"op":"set","path":"lang","value":"en"}]}`},
		{"缺 ops", `{"expectedRevision":0}`},
		{"expectedRevision 不是整数", `{"expectedRevision":"0","ops":[]}`},
		{"未知字段", `{"expectedRevision":0,"ops":[],"澄清":"x"}`},
		{"坏 JSON", `{"expectedRevision":0,`},
		{"op 未知", `{"expectedRevision":0,"ops":[{"op":"replace","path":"lang","value":"en"}]}`},
		{"unset 不存在的路径", `{"expectedRevision":0,"ops":[{"op":"unset","path":"nope"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := dispatch(t, handler, string(MethodConfigPatchApply), tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d，want 400；body=%s", rec.Code, rec.Body.String())
			}
			var doc map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatalf("错误响应不是 JSON: %v", err)
			}
			errObj, _ := doc["error"].(map[string]any)
			if errObj["code"] != "bad_request" {
				t.Fatalf("code = %v，want bad_request（参数错与 revision 冲突必须能分辨）", errObj["code"])
			}
			if hint, _ := errObj["hint"].(string); strings.TrimSpace(hint) == "" {
				t.Fatal("参数错误必须带 hint")
			}
		})
	}
	if store.Revision() != 0 {
		t.Fatalf("非法请求改了 revision：%d", store.Revision())
	}
}

func TestConfigPatchUnwiredIs501(t *testing.T) {
	handler := NewConfigPatchHandler(nil, nil)
	for _, method := range []Method{MethodConfigPatchGet, MethodConfigPatchApply} {
		rec := dispatch(t, handler, string(method), `{}`)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s 未接线 status = %d，want 501", method, rec.Code)
		}
		var doc map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("错误响应不是 JSON: %v", err)
		}
		errObj, _ := doc["error"].(map[string]any)
		if errObj["code"] != "not_implemented" {
			t.Fatalf("%s 未接线 code = %v，want not_implemented", method, errObj["code"])
		}
	}
}

// additive：config.patch.* 不进 canonical 25，也不和 config.get/set 撞名。
func TestConfigPatchMethodsAreAdditive(t *testing.T) {
	if len(ConfigPatchMethods()) != 2 {
		t.Fatalf("ConfigPatchMethods() = %v", ConfigPatchMethods())
	}
	canonical := make(map[Method]bool, len(SessionMethods()))
	for _, m := range SessionMethods() {
		canonical[m] = true
	}
	for _, m := range ConfigPatchMethods() {
		if canonical[m] {
			t.Fatalf("%q 污染了 canonical 25", m)
		}
		if !strings.HasPrefix(string(m), "config.patch.") {
			t.Fatalf("%q 不在 config.patch.* 命名空间下", m)
		}
	}
	if len(SessionMethods()) != 25 {
		t.Fatalf("canonical 25 被改了：%d", len(SessionMethods()))
	}

	descriptors := ConfigPatchDescriptors()
	if len(descriptors) != len(ConfigPatchMethods()) {
		t.Fatalf("描述符 %d 条 vs 方法 %d 条", len(descriptors), len(ConfigPatchMethods()))
	}
	for _, d := range descriptors {
		if d.Source != "additive:config.patch" {
			t.Fatalf("%q 的 source = %q", d.Method, d.Source)
		}
	}
}

// 落盘范围：只允许写 store 自己那个文件；任何请求都不许碰到 profiles/cordis.patch.yml。
func TestConfigPatchNeverTouchesProfilePatch(t *testing.T) {
	home := t.TempDir()
	patch := filepath.Join(home, "profiles", "default", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(patch), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("plugins: []\n")
	if err := os.WriteFile(patch, original, 0o600); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(home, "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenStore(filepath.Join(dir, "values.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.RegisterAll(ConfigPatchDescriptors()); err != nil {
		t.Fatal(err)
	}
	handler := NewConfigPatchHandler(store, registry)

	rec := dispatch(t, handler, string(MethodConfigPatchApply), `{"expectedRevision":0,"ops":[{"op":"set","path":"ui.theme","value":"dark"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d，body=%s", rec.Code, rec.Body.String())
	}

	if got, err := os.ReadFile(patch); err != nil {
		t.Fatalf("热重载开关被删了: %v", err)
	} else if string(got) != string(original) {
		t.Fatalf("写配置改到了 %s：%q", patch, got)
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 {
		t.Fatalf("store 目录里有残留文件：%v", entries)
	}
}

// Store 的写失败要按 500 报出去，不许静默成功。
func TestConfigPatchStoreFailureIs500(t *testing.T) {
	dir := t.TempDir()
	store, err := config.OpenStore(filepath.Join(dir, "values.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(config.Patch{Ops: []config.PatchOp{{Op: config.OpSet, Path: "lang", Value: "zh"}}}); err != nil {
		t.Fatal(err)
	}
	store.FailNextWrite(errors.New("磁盘满了"))

	registry := NewRegistry()
	if err := registry.RegisterAll(ConfigPatchDescriptors()); err != nil {
		t.Fatal(err)
	}
	handler := NewConfigPatchHandler(store, registry)
	rec := dispatch(t, handler, string(MethodConfigPatchApply), `{"expectedRevision":1,"ops":[{"op":"set","path":"lang","value":"en"}]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d，want 500；body=%s", rec.Code, rec.Body.String())
	}
	if store.Revision() != 1 {
		t.Fatalf("写失败后 revision = %d，want 1", store.Revision())
	}
}
