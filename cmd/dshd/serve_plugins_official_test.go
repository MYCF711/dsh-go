package main

// HS-16C 的真实状态验收：官方 pluginManager.* 不再是“501 占位/假 installed”，
// 而是从**真实** pluginmgr 状态与**真实** wasm Loader 读出来：
//
//   - listBundles 读 Manager.Bundles() 的 profile 投影；
//   - listPlugins 读 Loader.Loaded() 的 live 条目（fiberPhase=active/null）；
//   - install/enable/disable/update/remove 走既有真实事务；
//   - JS/Cordis 包回官方 ChangeResult 的结构化 unsupported-platform；
//   - warnings 反映当前进程是否真的装载，而不是 HS-07 前的过时告警。
//
// 这里用真 wasm 夹具（internal/wasmhost/testdata/toolplugin.wasm）而不是 mock。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

func newOfficialPluginManagerTestServer(t *testing.T, rt *pluginRuntime) *transport.Server {
	t.Helper()
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("transport.NewServer: %v", err)
	}
	srv.AttachOfficialPluginManagerAPI(newOfficialPluginManagerAPI(rt))
	t.Cleanup(srv.DetachOfficialPluginManagerAPI)
	return srv
}

func callOfficialPluginManager(t *testing.T, srv *transport.Server, method string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("序列化 %s 请求体: %v", method, err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.ServeOfficialPluginManagerAPI(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 响应不是 JSON: %q", method, rec.Body.String())
		}
	}
	return rec.Code, out
}

func officialResultArray(t *testing.T, code int, out map[string]any, method string) []any {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("%s 状态码 %d，期望 200（body=%v）", method, code, out)
	}
	result, ok := out["result"].([]any)
	if !ok {
		t.Fatalf("%s 的 result 不是数组：%v", method, out)
	}
	return result
}

func TestOfficialPluginManagerListBundlesAndPluginsUseRealRuntime(t *testing.T) {
	manager, err := pluginmgr.Open(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })

	pkg := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "v1"), "1.0.0")
	if _, err := manager.Install(pkg); err != nil {
		t.Fatalf("真实安装失败: %v", err)
	}
	srv := newOfficialPluginManagerTestServer(t, rt)

	// 安装但未启用：listBundles 看到真实 profile 状态，listPlugins 为空。
	code, out := callOfficialPluginManager(t, srv, "pluginManager.listBundles", map[string]any{})
	bundles := officialResultArray(t, code, out, "pluginManager.listBundles")
	if len(bundles) != 1 {
		t.Fatalf("listBundles 应返回 1 个真实包：%v", bundles)
	}
	bundle := bundles[0].(map[string]any)
	if bundle["name"] != "acme.audit" || bundle["version"] != "1.0.0" {
		t.Fatalf("bundle 身份不对：%v", bundle)
	}
	if bundle["installed"] != true || bundle["enabled"] != false || bundle["availability"] != "profile" {
		t.Fatalf("bundle 状态不对：%v", bundle)
	}
	if bundle["updatable"] != false || bundle["removable"] != true || bundle["official"] != false {
		t.Fatalf("bundle 能力不对：%v", bundle)
	}
	if bundle["source"] != pkg {
		t.Fatalf("bundle.source 必须是真实绝对来源 %q：%v", pkg, bundle)
	}
	if rows, ok := bundle["rows"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("本仓包没有 patch rows，必须是 []：%v", bundle)
	}
	if overrides, ok := bundle["overrides"].([]any); !ok || len(overrides) != 0 {
		t.Fatalf("本仓包没有 overrides，必须是 []：%v", bundle)
	}

	code, out = callOfficialPluginManager(t, srv, "pluginManager.listPlugins", map[string]any{})
	plugins := officialResultArray(t, code, out, "pluginManager.listPlugins")
	if len(plugins) != 1 {
		t.Fatalf("已安装包应作为 Loader 条目出现（enabled=false）：%v", plugins)
	}
	if plugins[0].(map[string]any)["enabled"] != false || plugins[0].(map[string]any)["fiberPhase"] != nil {
		t.Fatalf("未启用必须是 enabled=false, fiberPhase=null（不能假报 active）：%v", plugins[0])
	}

	// 真实 enable → Loader 里真的有 wasm 实例，listPlugins 才应出现 active。
	if _, err := manager.Enable("acme.audit"); err != nil {
		t.Fatalf("真实 enable 失败: %v", err)
	}
	code, out = callOfficialPluginManager(t, srv, "pluginManager.listPlugins", map[string]any{})
	plugins = officialResultArray(t, code, out, "pluginManager.listPlugins")
	if len(plugins) != 1 {
		t.Fatalf("启用后 listPlugins 应返回 1 条真实 Loader 条目：%v", plugins)
	}
	entry := plugins[0].(map[string]any)
	if entry["entryId"] != "acme.audit" || entry["moduleName"] != "plugin.wasm" {
		t.Fatalf("Loader 条目身份不对：%v", entry)
	}
	if entry["enabled"] != true || entry["fiberPhase"] != "active" {
		t.Fatalf("真实装载必须是 enabled=true, fiberPhase=active：%v", entry)
	}
	if entry["patchId"] != "acme.audit" {
		t.Fatalf("patchId 必须指向 profile 管理键：%v", entry)
	}

	// 真实 disable → 运行时卸载，listPlugins 立即消失；listBundles 仍报 enabled=false。
	if _, err := manager.Disable("acme.audit"); err != nil {
		t.Fatalf("真实 disable 失败: %v", err)
	}
	code, out = callOfficialPluginManager(t, srv, "pluginManager.listPlugins", map[string]any{})
	plugins = officialResultArray(t, code, out, "pluginManager.listPlugins")
	if len(plugins) != 1 || plugins[0].(map[string]any)["fiberPhase"] != nil || plugins[0].(map[string]any)["enabled"] != false {
		t.Fatalf("禁用后必须回到 enabled=false, fiberPhase=null（不能假报 active）：%v", plugins)
	}
	code, out = callOfficialPluginManager(t, srv, "pluginManager.listBundles", map[string]any{})
	bundles = officialResultArray(t, code, out, "pluginManager.listBundles")
	if len(bundles) != 1 || bundles[0].(map[string]any)["enabled"] != false {
		t.Fatalf("禁用后 listBundles 必须仍报真实 profile 状态：%v", bundles)
	}
}

func TestOfficialPluginManagerEnableDisableWarningsReflectLoader(t *testing.T) {
	manager, err := pluginmgr.Open(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	pkg := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "v1"), "1.0.0")
	if _, err := manager.Install(pkg); err != nil {
		t.Fatal(err)
	}
	srv := newOfficialPluginManagerTestServer(t, rt)

	code, out := callOfficialPluginManager(t, srv, "pluginManager.setPluginEnabled",
		map[string]any{"id": "acme.audit", "enabled": true})
	if code != http.StatusOK {
		t.Fatalf("setPluginEnabled 失败：%d %v", code, out)
	}
	result := out["result"].(map[string]any)
	if result["application"] != "applied" || result["changed"] != true {
		t.Fatalf("enable 结果不对：%v", result)
	}
	warnings := warningText(result)
	if strings.Contains(warnings, "没有运行时插件加载器") {
		t.Fatalf("HS-07W 之后不能再报“没有运行时插件加载器”：%v", warnings)
	}
	if !strings.Contains(warnings, "已装载到当前进程的运行时") {
		t.Fatalf("enable 成功必须回报真实装载：%v", warnings)
	}

	code, out = callOfficialPluginManager(t, srv, "pluginManager.setBundleEnabled",
		map[string]any{"name": "acme.audit", "enabled": false})
	if code != http.StatusOK {
		t.Fatalf("setBundleEnabled(false) 失败：%d %v", code, out)
	}
	result = out["result"].(map[string]any)
	if result["application"] != "applied" || result["enabled"] != false {
		t.Fatalf("disable 结果不对：%v", result)
	}
	warnings = warningText(result)
	if !strings.Contains(warnings, "已从 profile 禁用") {
		t.Fatalf("disable 必须如实说明当前运行时没有它：%v", warnings)
	}
}

func TestOfficialPluginManagerUpdateRetainedAndRemoveAreReal(t *testing.T) {
	manager, err := pluginmgr.Open(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	v1 := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "v1"), "1.0.0")
	v2 := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "v2"), "2.0.0")
	if _, err := manager.Install(v1); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update("acme.audit", v2); err != nil {
		t.Fatalf("准备保留版本失败: %v", err)
	}
	// 用**真实** manager.Restore 把 current 指回 v1，同时 v2 仍留在保留版本里：
	// 这正是官方 updateBundle 的“目标版本已在本机 store”分支（restoreLocked）。
	if _, err := manager.Restore("acme.audit", "1.0.0"); err != nil {
		t.Fatalf("准备 current=v1 失败: %v", err)
	}
	srv := newOfficialPluginManagerTestServer(t, rt)

	code, out := callOfficialPluginManager(t, srv, "pluginManager.updateBundle",
		map[string]any{"name": "acme.audit", "version": "2.0.0"})
	if code != http.StatusOK {
		t.Fatalf("updateBundle 失败：%d %v", code, out)
	}
	update := out["result"].(map[string]any)
	if update["from"] != "1.0.0" || update["to"] != "2.0.0" {
		t.Fatalf("update 的 from/to 不对：%v", update)
	}
	inner := update["result"].(map[string]any)
	if inner["application"] != "applied" {
		t.Fatalf("update 必须 applied：%v", inner)
	}
	state, ok, err := manager.Bundle("acme.audit")
	if err != nil || !ok || state.Version != "2.0.0" || state.PreviousVersion != "1.0.0" {
		t.Fatalf("update 没有改到真实状态：state=%+v ok=%v err=%v", state, ok, err)
	}

	// removeBundle 真的卸载（state 与包目录都没了）。
	code, out = callOfficialPluginManager(t, srv, "pluginManager.removeBundle", map[string]any{"name": "acme.audit"})
	if code != http.StatusOK {
		t.Fatalf("removeBundle 失败：%d %v", code, out)
	}
	remove := out["result"].(map[string]any)
	if remove["application"] != "applied" || remove["stage"] != "remove" {
		t.Fatalf("remove 结果不对：%v", remove)
	}
	if _, ok, err := manager.Bundle("acme.audit"); err != nil || ok {
		t.Fatalf("remove 之后真实状态里仍有包：ok=%v err=%v", ok, err)
	}
}

func TestOfficialPluginManagerJSPackageIsStructuredUnsupported(t *testing.T) {
	manager, err := pluginmgr.Open(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	srv := newOfficialPluginManagerTestServer(t, rt)

	// Cordis/npm 形态：package.json + index.js，没有 dsh 的 plugin.json。
	pkg := filepath.Join(t.TempDir(), "cordis")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"acme.cordis","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "index.js"), []byte("export default {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := callOfficialPluginManager(t, srv, "pluginManager.inspect", map[string]any{"spec": pkg})
	if code != http.StatusOK {
		t.Fatalf("inspect JS 包失败：%d %v", code, out)
	}
	inspect := out["result"].(map[string]any)
	if inspect["status"] != "refused" || inspect["problem"] != "not-a-package" {
		t.Fatalf("JS inspect 必须 refused/not-a-package：%v", inspect)
	}
	if reason, _ := inspect["reason"].(string); !strings.Contains(reason, "JS") && !strings.Contains(reason, "Cordis") {
		t.Fatalf("inspect 拒绝理由必须点名 JS/Cordis：%v", inspect)
	}

	code, out = callOfficialPluginManager(t, srv, "pluginManager.installBundle", map[string]any{"spec": pkg})
	if code != http.StatusOK {
		t.Fatalf("JS install 必须是 200 ChangeResult.failed，不能是 HTTP 400：%d %v", code, out)
	}
	result := out["result"].(map[string]any)
	if result["changed"] != false || result["application"] != "failed" {
		t.Fatalf("JS install 结果必须是 failed、changed=false：%v", result)
	}
	errObj, _ := result["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "unsupported-platform" {
		t.Fatalf("JS install 错误码必须是 unsupported-platform：%v", result)
	}
	if diagnostic, _ := errObj["diagnostic"].(string); !strings.Contains(diagnostic, "JS") && !strings.Contains(diagnostic, "Cordis") {
		t.Fatalf("JS install 诊断必须点名 JS/Cordis：%v", errObj)
	}
	if plugins, err := manager.List(); err != nil || len(plugins) != 0 {
		t.Fatalf("被拒绝的 JS 包不能留在已安装状态：%v %v", plugins, err)
	}
}

func TestOfficialPluginManagerRegistriesIsStructuredUnsupported(t *testing.T) {
	manager, err := pluginmgr.Open(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	srv := newOfficialPluginManagerTestServer(t, rt)

	code, out := callOfficialPluginManager(t, srv, "pluginManager.registries", map[string]any{})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("registries 未配置 registry 应回结构化 503 registry_unavailable，实际 %d %v", code, out)
	}
	errObj, _ := out["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "registry_unavailable" {
		t.Fatalf("registries 必须是 registry_unavailable 四字段错误：%v", out)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "registry") {
		t.Fatalf("registries 的错误必须点名 registry：%v", errObj)
	}
	for _, key := range []string{"message", "hint", "retryable"} {
		if _, ok := errObj[key]; !ok {
			t.Fatalf("四字段错误缺 %s：%v", key, errObj)
		}
	}
}

func TestOfficialPluginManagerInstallBundleIsRealAndLoaded(t *testing.T) {
	manager, err := pluginmgr.Open(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	pkg := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "v1"), "1.0.0")
	srv := newOfficialPluginManagerTestServer(t, rt)

	code, out := callOfficialPluginManager(t, srv, "pluginManager.installBundle", map[string]any{
		"spec":    pkg,
		"options": map[string]any{"requestId": "req-real-1"},
	})
	if code != http.StatusOK {
		t.Fatalf("installBundle 失败：%d %v", code, out)
	}
	result := out["result"].(map[string]any)
	if result["application"] != "applied" || result["changed"] != true {
		t.Fatalf("installBundle 必须真实 applied：%v", result)
	}
	if result["bundle"] != "acme.audit" || result["version"] != "1.0.0" {
		t.Fatalf("installBundle 没回报真实 bundle/version：%v", result)
	}
	if !strings.Contains(warningText(result), "已装载到当前进程的运行时") {
		t.Fatalf("默认 enabled=true 必须真的装载 wasm：%v", result)
	}
	state, ok, err := manager.Bundle("acme.audit")
	if err != nil || !ok || !state.Enabled || state.Version != "1.0.0" {
		t.Fatalf("installBundle 没写真实状态：state=%+v ok=%v err=%v", state, ok, err)
	}
	if loaded := rt.Loaded(); len(loaded) != 1 || loaded[0].ID != "acme.audit" {
		t.Fatalf("installBundle 后 Loader 里必须真的有 wasm：%v", loaded)
	}
	// 作业收工后 waitForInstall 如实回 active:false（官方的 null 等价物）。
	code, out = callOfficialPluginManager(t, srv, "pluginManager.waitForInstall", map[string]any{"requestId": "req-real-1"})
	if code != http.StatusOK || out["result"].(map[string]any)["active"] != false {
		t.Fatalf("已收工作业必须回 active:false：%d %v", code, out)
	}
	code, out = callOfficialPluginManager(t, srv, "pluginManager.cancelInstall", map[string]any{"requestId": "req-real-1"})
	if code != http.StatusOK || out["result"].(map[string]any)["status"] != "not-running" {
		t.Fatalf("已收工作业 cancel 必须 not-running：%d %v", code, out)
	}
}

func TestOfficialPluginManagerExemptionIsRealAndWarningUpdated(t *testing.T) {
	manager, err := pluginmgr.Open(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	srv := newOfficialPluginManagerTestServer(t, rt)

	code, out := callOfficialPluginManager(t, srv, "pluginManager.setVersionExemption", map[string]any{
		"packageVersion": "acme.audit@1.0.0",
		"runtimeVersion": pluginmgr.HostRuntimeVersion,
		"enabled":        true,
		"acceptRisk":     true,
	})
	if code != http.StatusOK {
		t.Fatalf("setVersionExemption 失败：%d %v", code, out)
	}
	result := out["result"].(map[string]any)
	if result["application"] != "applied" {
		t.Fatalf("豁免必须真实 applied：%v", result)
	}
	warnings := warningText(result)
	if strings.Contains(warnings, "没有运行时插件加载器") {
		t.Fatalf("豁免 warning 不能沿用 HS-07 前的过时说法：%v", warnings)
	}
	if !strings.Contains(warnings, "不会热重载已装插件") {
		t.Fatalf("豁免 warning 必须说清不会热重载：%v", warnings)
	}
	exemptions, readWarnings := manager.VersionExemptions()
	if len(readWarnings) != 0 {
		t.Fatalf("正常写入后读侧不应有警告：%v", readWarnings)
	}
	if len(exemptions["acme.audit@1.0.0"]) != 1 || exemptions["acme.audit@1.0.0"][0] != pluginmgr.HostRuntimeVersion {
		t.Fatalf("豁免没有持久化到 compatibility.json：%v", exemptions)
	}
	code, out = callOfficialPluginManager(t, srv, "pluginManager.listVersionExemptions", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("listVersionExemptions 失败：%d %v", code, out)
	}
	list := out["result"].(map[string]any)
	got, _ := list["exemptions"].(map[string]any)
	if versions, _ := got["acme.audit@1.0.0"].([]any); len(versions) != 1 || versions[0] != pluginmgr.HostRuntimeVersion {
		t.Fatalf("读侧必须与写侧同一份文件：%v", list)
	}
}

// TestOfficialPluginManagerHTTPProbe 走真实 TCP socket + 真实 cookie 认证 +
// handleAPI 路由（不是直接调 handler），证明 /api/pluginManager.listBundles
// 在经过 Transport 的正式路由后仍能读到真实状态。
func TestOfficialPluginManagerHTTPProbe(t *testing.T) {
	manager, err := pluginmgr.Open(filepath.Join(t.TempDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	pkg := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "v1"), "1.0.0")
	if _, err := manager.Install(pkg); err != nil {
		t.Fatal(err)
	}
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv.AttachOfficialPluginManagerAPI(newOfficialPluginManagerAPI(rt))
	base, err := srv.Start()
	if err != nil {
		t.Fatalf("真实监听失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
		// /connect 用 303 把壳带回 /；探针自己收下 Set-Cookie 即可，不跟跳转。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		t.Fatalf("GET /connect: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("/connect 状态码 %d，期望 303", resp.StatusCode)
	}

	resp, err = client.Post(base+"/api/pluginManager.listBundles", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST listBundles: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("真实 HTTP listBundles 状态码 %d（期望 200）", resp.StatusCode)
	}
	var envelope map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	bundles, ok := envelope["result"].([]any)
	if !ok || len(bundles) != 1 {
		t.Fatalf("真实 HTTP inventory 不对：%v", envelope)
	}
	if bundles[0].(map[string]any)["name"] != "acme.audit" {
		t.Fatalf("真实 HTTP inventory 读错包：%v", bundles[0])
	}
}

func warningText(result map[string]any) string {
	raw, _ := json.Marshal(result["warnings"])
	return string(raw)
}
