package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

func makeJSPlugin(t *testing.T, home, id string) string {
	t.Helper()
	dir := filepath.Join(home, "cordis-plugins", id)
	if err := os.MkdirAll(filepath.Join(dir, "client"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"`+id+`","version":"2.0.0","main":"host.cjs"}`)
	write("host.cjs", `module.exports = {
  echo(args) { return { echo: args.text || null, n: args.n || 0 }; },
  fail() { throw new Error('guard boom'); },
};`)
	write("client/client.js", "export const id = '"+id+"';\n")
	return dir
}

func callCordis(t *testing.T, s *cordisService, method string, params map[string]any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	return s.HandleOfficialCordis(context.Background(), method, raw)
}

func TestCordisInventoryClientCodeInvoke(t *testing.T) {
	home := t.TempDir()
	s := newCordisService(home)
	if err := s.RegisterGo("go-a", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	makeJSPlugin(t, home, "js-a")
	if _, err := callCordis(t, s, "dynamicCordisRunner.inventory", nil); err != nil {
		t.Fatal(err)
	}
	inv, _ := callCordis(t, s, "dynamicCordisRunner.inventory", nil)
	if len(inv.([]*cordisEntry)) != 1 {
		t.Fatalf("inventory=%v", inv)
	}
	host, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	if host.(*cordisEntry).PID == 0 || len(host.(*cordisEntry).Methods) != 2 {
		t.Fatalf("host=%+v", host)
	}
	code, err := callCordis(t, s, "dynamicCordisRunner.getClientCode", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(code.(map[string]any)["code"].(string), "js-a") {
		t.Fatalf("client code=%v", code)
	}
	res, err := callCordis(t, s, "dynamicCordisRunner.invoke", map[string]any{
		"pluginId": "js-a", "requestId": "req-1", "method": "echo", "args": map[string]any{"text": "hi", "n": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("invoke 结果为空")
	}
	if _, err := callCordis(t, s, "dynamicCordisRunner.invoke", map[string]any{
		"pluginId": "js-a", "requestId": "req-fail", "method": "fail", "args": nil,
	}); err == nil {
		t.Fatal("插件抛错必须失败")
	}
	settled, err := callCordis(t, s, "dynamicCordisRunner.settleUserRun", map[string]any{"requestId": "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	if settled.(map[string]any)["requestId"] != "req-1" {
		t.Fatalf("settle=%v", settled)
	}
	if _, err := callCordis(t, s, "dynamicCordisRunner.resolveRequestRun", map[string]any{"requestId": "unknown"}); err == nil {
		t.Fatal("未知 requestId 必须失败")
	}
	inspect, err := callCordis(t, s, "dynamicCordisRunner.resolveInspectQuery", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	if inspect == nil {
		t.Fatal("inspect 为空")
	}
	mf, err := callCordis(t, s, "dynamicCordisRunner.syncInspectManifest", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.(map[string]any)["hash"].(string)) != 64 {
		t.Fatalf("manifest=%v", mf)
	}
	if _, err := callCordis(t, s, "dynamicCordisRunner.reportClientGuardFailure", map[string]any{"pluginId": "js-a", "failure": "guard"}); err != nil {
		t.Fatal(err)
	}
	if _, err := callCordis(t, s, "dynamicCordisRunner.reportRenderFailure", map[string]any{"pluginId": "js-a", "failure": "render"}); err != nil {
		t.Fatal(err)
	}
	stopped, err := callCordis(t, s, "dynamicCordisRunner.stopFromPanel", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.(*cordisEntry).State != "stopped" {
		t.Fatalf("stop=%+v", stopped)
	}
	undef, err := callCordis(t, s, "dynamicCordisRunner.undefineFromPanel", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	if undef.(map[string]any)["undefined"] != true {
		t.Fatalf("undefine=%v", undef)
	}
}

func TestCordisNoNodeIsLoud(t *testing.T) {
	s := newCordisService(t.TempDir())
	s.nodePath = filepath.Join(t.TempDir(), "missing-node.exe")
	_, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", map[string]any{"pluginId": "js-a"})
	if err == nil || !strings.Contains(err.Error(), "node") {
		t.Fatalf("无 node 必须响亮失败，得到 %v", err)
	}
}

func TestCordisHTTPInventory(t *testing.T) {
	home := t.TempDir()
	s := newCordisService(home)
	if err := s.RegisterGo("go-a", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	makeJSPlugin(t, home, "js-http")
	srv := &transport.Server{}
	srv.AttachOfficialCordisAPI(s)
	req := httptest.NewRequest("POST", "/api/dynamicCordisRunner.inventory", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	srv.ServeOfficialCordisAPI(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "go-a") {
		t.Fatalf("HTTP inventory=%d %s", w.Code, w.Body.String())
	}
	post := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/"+method, strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeOfficialCordisAPI(rec, req)
		return rec
	}
	if rec := post("dynamicCordisRunner.runHostHalf", `{"pluginId":"js-http"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), "js-http") {
		t.Fatalf("HTTP runHostHalf=%d %s", rec.Code, rec.Body.String())
	}
	if rec := post("dynamicCordisRunner.invoke", `{"pluginId":"js-http","requestId":"http-1","method":"echo","args":{"text":"wire"}}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), "wire") {
		t.Fatalf("HTTP invoke=%d %s", rec.Code, rec.Body.String())
	}
	if rec := post("dynamicCordisRunner.invoke", `{"pluginId":"js-http","requestId":"http-2","method":"fail","args":{}}`); rec.Code == 200 {
		t.Fatalf("HTTP 插件抛错不应 200：%s", rec.Body.String())
	}
}
