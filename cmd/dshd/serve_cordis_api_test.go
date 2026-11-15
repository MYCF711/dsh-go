package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

// cordisKeys 返回一个结果对象的**全部** JSON 键（排序）。
//
// 用它断言"形状与官方逐字一致"：多一个字段、少一个字段都会红——这正是这一轮
// 要修的东西（旧形状多 `file`/`bytes`/`result`/`run` 这类自制字段，少 ok 之类的
// 官方字段）。
func cordisKeys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("结果不能序列化: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("结果不是 JSON 对象: %s", raw)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func assertKeys(t *testing.T, where string, v any, want ...string) {
	t.Helper()
	got := cordisKeys(t, v)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s 的字段必须**恰好**是 %v，实际 %v（多/少字段都是形状漂移）", where, want, got)
	}
}

func mustValue[T any](t *testing.T, where string, v any) T {
	t.Helper()
	got, ok := v.(T)
	if !ok {
		t.Fatalf("%s 的结果类型是 %T", where, v)
	}
	return got
}

func TestCordisRunHostHalfOfficialShape(t *testing.T) {
	home := t.TempDir()
	s := newCordisService(home)
	makeJSPlugin(t, home, "js-a")
	wantPkg := cordisPackageID("js-a", "2.0.0")

	first, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", map[string]any{
		"pluginId": "js-a", "packageId": wantPkg, "mode": "run",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "runHostHalf(ok)", first, "ok", "pluginId", "packageId", "pluginRunId", "waitingFor", "startedHere")
	ok := mustValue[cordisRunHostOK](t, "runHostHalf", first)
	if !ok.OK || ok.PluginID != "js-a" || ok.PackageID != wantPkg || ok.PluginRunID == "" || !ok.StartedHere {
		t.Fatalf("runHostHalf=%+v", ok)
	}
	if ok.WaitingFor == nil || len(ok.WaitingFor) != 0 {
		t.Fatalf("waitingFor 必须是空数组（本宿主没有缺失的 Host 服务）：%v", ok.WaitingFor)
	}
	raw, _ := json.Marshal(first)
	if !strings.Contains(string(raw), `"waitingFor":[]`) {
		t.Fatalf("waitingFor 不能是 null：%s", raw)
	}

	// 同一次运行的第二次激活 = 挂上去，startedHere 必须是 false、run 号不变。
	again, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", map[string]any{"pluginId": "js-a", "mode": "run"})
	if err != nil {
		t.Fatal(err)
	}
	attached := mustValue[cordisRunHostOK](t, "runHostHalf(attach)", again)
	if attached.StartedHere || attached.PluginRunID != ok.PluginRunID {
		t.Fatalf("挂到已活运行必须 startedHere=false 且 run 号不变：%+v", attached)
	}

	// 结算一次，让这个包成为"上一次成功的版本"（官方 mode 判定看 currentPackageId）。
	if _, err := callCordis(t, s, "dynamicCordisRunner.settleUserRun", map[string]any{
		"pluginId": "js-a", "resolution": map[string]any{"ok": true, "pluginRunId": ok.PluginRunID},
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		params map[string]any
		expect string
	}{
		{"包不存在", map[string]any{"pluginId": "js-a", "packageId": "pkg-nope"}, "没有包"},
		{"update 同一版本", map[string]any{"pluginId": "js-a", "mode": "update"}, "已是当前"},
		{"mode 非法", map[string]any{"pluginId": "js-a", "mode": "approve"}, "run / update"},
		{"model 侧请求不存在", map[string]any{"pluginId": "js-a", "requestId": "q-1"}, "没有 model 侧待审批"},
		{"插件不存在", map[string]any{"pluginId": "nope"}, "不存在"},
	} {
		got, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", tc.params)
		if err != nil {
			t.Fatalf("%s：官方 runHostHalf 失败也是 200 里的值，不该报错：%v", tc.name, err)
		}
		assertKeys(t, "runHostHalf(fail/"+tc.name+")", got, "ok", "message")
		fail := mustValue[cordisRunHostFailure](t, tc.name, got)
		if fail.OK || !strings.Contains(fail.Message, tc.expect) {
			t.Fatalf("%s：%+v（消息应含 %q）", tc.name, fail, tc.expect)
		}
	}

	// Go 原生插件没有 Host 半：如实拒绝，不假装跑起来。
	if err := s.RegisterGo("go-a", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	got, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", map[string]any{"pluginId": "go-a"})
	if err != nil {
		t.Fatal(err)
	}
	if fail := mustValue[cordisRunHostFailure](t, "go", got); fail.OK || !strings.Contains(fail.Message, "Go 原生") {
		t.Fatalf("Go 插件：%+v", fail)
	}
}

func TestCordisGetClientCodeOfficialShape(t *testing.T) {
	home := t.TempDir()
	s := newCordisService(home)
	makeJSPlugin(t, home, "js-a")

	// 官方 getClientCode 只对**当前激活**发源码，自己不会激活插件。
	if _, err := callCordis(t, s, "dynamicCordisRunner.getClientCode", map[string]any{"pluginId": "js-a"}); err == nil {
		t.Fatal("没在跑的插件不许发 Client 源码")
	}

	host, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	run := mustValue[cordisRunHostOK](t, "runHostHalf", host)

	code, err := callCordis(t, s, "dynamicCordisRunner.getClientCode", map[string]any{
		"agentId": "s-1", "pluginId": "js-a", "pluginRunId": run.PluginRunID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "getClientCode", code, "code", "name", "pluginId", "packageId", "pluginRunId")
	got := mustValue[cordisClientCode](t, "getClientCode", code)
	if !strings.Contains(got.Code, "js-a") || got.Name != "js-a" ||
		got.PackageID != cordisPackageID("js-a", "2.0.0") || got.PluginRunID != run.PluginRunID {
		t.Fatalf("getClientCode=%+v", got)
	}

	// run 号对不上 = 不是当前激活。
	if _, err := callCordis(t, s, "dynamicCordisRunner.getClientCode", map[string]any{
		"pluginId": "js-a", "pluginRunId": "run-nope",
	}); err == nil {
		t.Fatal("run 号对不上必须失败")
	}
}

func TestCordisInvokeOfficialShape(t *testing.T) {
	home := t.TempDir()
	s := newCordisService(home)
	makeJSPlugin(t, home, "js-a")

	// 没在跑：官方回 plugin-not-running 回执（不是顺手激活，也不是 HTTP 错）。
	notRunning, err := callCordis(t, s, "dynamicCordisRunner.invoke", map[string]any{
		"pluginId": "js-a", "method": "echo", "args": map[string]any{"text": "x"},
	})
	if err != nil {
		t.Fatalf("官方 invoke 的失败是 200 里的值：%v", err)
	}
	assertKeys(t, "invoke(plugin-not-running)", notRunning, "ok", "code", "message")
	if f := mustValue[cordisInvokeFailure](t, "invoke", notRunning); f.OK || f.Code != "plugin-not-running" {
		t.Fatalf("invoke=%+v", f)
	}

	host, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	run := mustValue[cordisRunHostOK](t, "runHostHalf", host)

	res, err := callCordis(t, s, "dynamicCordisRunner.invoke", map[string]any{
		"pluginId": "js-a", "pluginRunId": run.PluginRunID, "method": "echo",
		"args": map[string]any{"text": "hi", "n": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 官方成功分支**只有** ok/value 两个键（判别联合，别加多余字段）。
	assertKeys(t, "invoke(ok)", res, "ok", "value")
	value := mustValue[cordisInvokeValue](t, "invoke", res)
	if !value.OK {
		t.Fatalf("invoke=%+v", value)
	}
	obj, ok := value.Value.(map[string]any)
	if !ok || obj["echo"] != "hi" || obj["n"] != float64(3) {
		t.Fatalf("invoke.value=%#v", value.Value)
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), `"value":{`) {
		t.Fatalf("value 必须是插件真实返回值：%s", raw)
	}

	// 插件抛错 → handler-error（判别联合的失败分支，带 message 不带 value）。
	failed, err := callCordis(t, s, "dynamicCordisRunner.invoke", map[string]any{
		"pluginId": "js-a", "pluginRunId": run.PluginRunID, "method": "fail", "args": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "invoke(handler-error)", failed, "ok", "code", "message")
	if f := mustValue[cordisInvokeFailure](t, "invoke", failed); f.OK || f.Code != "handler-error" ||
		!strings.Contains(f.Message, "guard boom") {
		t.Fatalf("invoke=%+v", f)
	}

	// 方法不存在 → method-not-found（外部 node 的 -32601）。
	missing, err := callCordis(t, s, "dynamicCordisRunner.invoke", map[string]any{
		"pluginId": "js-a", "pluginRunId": run.PluginRunID, "method": "nope", "args": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f := mustValue[cordisInvokeFailure](t, "invoke", missing); f.Code != "method-not-found" {
		t.Fatalf("invoke=%+v", f)
	}

	// 旧 run 号 → stale-run。
	stale, err := callCordis(t, s, "dynamicCordisRunner.invoke", map[string]any{
		"pluginId": "js-a", "pluginRunId": "run-old", "method": "echo", "args": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f := mustValue[cordisInvokeFailure](t, "invoke", stale); f.Code != "stale-run" {
		t.Fatalf("invoke=%+v", f)
	}
}

func TestCordisSettleUserRunOfficialShape(t *testing.T) {
	home := t.TempDir()
	s := newCordisService(home)
	makeJSPlugin(t, home, "js-a")

	host, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	run := mustValue[cordisRunHostOK](t, "runHostHalf", host)
	wantPkg := cordisPackageID("js-a", "2.0.0")

	// 包里有 client/client.js，激活后是 client-pending；页面报成功才 commit。
	settled, err := callCordis(t, s, "dynamicCordisRunner.settleUserRun", map[string]any{
		"agentId": "s-1", "pluginId": "js-a",
		"resolution": map[string]any{"ok": true, "pluginRunId": run.PluginRunID, "waitingFor": []string{"ui"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "settleUserRun(ok)", settled,
		"ok", "status", "pluginId", "packageId", "pluginRunId", "waitingFor",
		"clientWaitingFor", "currentPackageId", "mode")
	ok := mustValue[cordisSettleOK](t, "settleUserRun", settled)
	if !ok.OK || ok.Status != "running" || ok.PluginID != "js-a" || ok.PackageID != wantPkg ||
		ok.PluginRunID != run.PluginRunID || ok.CurrentPackageID != wantPkg || ok.Mode != "run" {
		t.Fatalf("settleUserRun=%+v", ok)
	}
	if len(ok.WaitingFor) != 0 || len(ok.ClientWaitingFor) != 1 || ok.ClientWaitingFor[0] != "ui" {
		t.Fatalf("waitingFor/clientWaitingFor=%v/%v", ok.WaitingFor, ok.ClientWaitingFor)
	}

	// 迟到的 run 号：官方只接受当前激活，回 client-half-failed。
	late, err := callCordis(t, s, "dynamicCordisRunner.settleUserRun", map[string]any{
		"pluginId": "js-a", "resolution": map[string]any{"ok": true, "pluginRunId": "run-old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "settleUserRun(late)", late, "ok", "reason", "message")
	if f := mustValue[cordisSettleFailure](t, "settleUserRun", late); f.OK || f.Reason != "client-half-failed" {
		t.Fatalf("settleUserRun=%+v", f)
	}

	// 页面报 Client 半失败：撤下那次运行并记失败尝试。
	failed, err := callCordis(t, s, "dynamicCordisRunner.settleUserRun", map[string]any{
		"pluginId": "js-a",
		"resolution": map[string]any{"ok": false, "reason": "client-half-failed",
			"pluginRunId": run.PluginRunID, "message": "render 崩了"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f := mustValue[cordisSettleFailure](t, "settleUserRun", failed); f.OK || f.Reason != "client-half-failed" ||
		f.Message != "render 崩了" {
		t.Fatalf("settleUserRun=%+v", f)
	}
	if _, live := s.liveClient("js-a"); live {
		t.Fatal("Client 半失败后那次运行必须被撤下")
	}

	// 插件不存在 → plugin-missing。
	missing, err := callCordis(t, s, "dynamicCordisRunner.settleUserRun", map[string]any{
		"pluginId": "nope", "resolution": map[string]any{"ok": true, "pluginRunId": "run-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f := mustValue[cordisSettleFailure](t, "settleUserRun", missing); f.Reason != "plugin-missing" {
		t.Fatalf("settleUserRun=%+v", f)
	}

	// 旧 wire 的 requestId 不再冒充 pluginId：缺 pluginId 是参数错。
	if _, err := callCordis(t, s, "dynamicCordisRunner.settleUserRun", map[string]any{
		"requestId": "req-1",
	}); err == nil {
		t.Fatal("官方 settleUserRun 的第二参是 pluginId，缺了必须报错")
	}
}

func TestCordisResolveRequestRunOfficialShape(t *testing.T) {
	s := newCordisService(t.TempDir())
	// 本宿主没有 model 侧待审批请求表：任何 requestId 都是官方的 unknown 分支。
	got, err := callCordis(t, s, "dynamicCordisRunner.resolveRequestRun", map[string]any{
		"requestId": "unknown", "resolution": map[string]any{"ok": true, "pluginRunId": "run-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "resolveRequestRun", got, "accepted")
	if r := mustValue[cordisResolveReceipt](t, "resolveRequestRun", got); r.Accepted {
		t.Fatal("没有待审批请求时不许 accepted")
	}
	if _, err := callCordis(t, s, "dynamicCordisRunner.resolveRequestRun", map[string]any{}); err == nil {
		t.Fatal("缺 requestId 必须报错")
	}
}

func TestCordisNoNodeIsLoud(t *testing.T) {
	s := newCordisService(t.TempDir())
	s.nodePath = filepath.Join(t.TempDir(), "missing-node.exe")
	makeJSPlugin(t, s.home, "js-a")
	// 没有 node 是**这台机器**跑不起来这条能力，不是插件加载失败：必须是带 Hint 的
	// 结构化不可用（501 not_implemented），不能折成 ok:false 或空结果。
	_, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf", map[string]any{"pluginId": "js-a"})
	if err == nil || !strings.Contains(err.Error(), "node") {
		t.Fatalf("无 node 必须响亮失败，得到 %v", err)
	}
	var api *transport.CordisAPIError
	if !errors.As(err, &api) || api.Status != 501 || api.Code != "not_implemented" || api.Hint == "" {
		t.Fatalf("无 node 要给 501 not_implemented + Hint，得到 %#v", err)
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
	hostRec := post("dynamicCordisRunner.runHostHalf", `{"agentId":"s-1","pluginId":"js-http","mode":"run"}`)
	if hostRec.Code != 200 || !strings.Contains(hostRec.Body.String(), `"startedHere":true`) {
		t.Fatalf("HTTP runHostHalf=%d %s", hostRec.Code, hostRec.Body.String())
	}
	var env struct {
		Result cordisRunHostOK `json:"result"`
	}
	if err := json.Unmarshal(hostRec.Body.Bytes(), &env); err != nil || env.Result.PluginRunID == "" {
		t.Fatalf("HTTP runHostHalf 结果解不开：%v %s", err, hostRec.Body.String())
	}
	invokeBody := `{"pluginId":"js-http","pluginRunId":"` + env.Result.PluginRunID + `","method":"echo","args":{"text":"wire"}}`
	if rec := post("dynamicCordisRunner.invoke", invokeBody); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), "wire") {
		t.Fatalf("HTTP invoke=%d %s", rec.Code, rec.Body.String())
	}
	// 官方这两个 report 的结果是字面量 null：wire 上是 {"result":null}，
	// 不能变成"空载荷 500"，也不能塞一个自制回执。
	for _, m := range []string{"dynamicCordisRunner.reportClientGuardFailure", "dynamicCordisRunner.reportRenderFailure"} {
		rec := post(m, `{"agentId":"s-1","pluginId":"js-http","pluginRunId":"`+env.Result.PluginRunID+
			`","failure":{"message":"guard","slot":"chat"}}`)
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"result":null}` {
			t.Fatalf("HTTP %s=%d %s", m, rec.Code, rec.Body.String())
		}
	}
	// 诊断确实落盘了（null 回执不等于什么都不做）。
	raw, err := os.ReadFile(filepath.Join(home, "state", "cordis", "events.jsonl"))
	if err != nil || !strings.Contains(string(raw), "guard") {
		t.Fatalf("report 的副作用必须真实落盘：%v %s", err, raw)
	}
}
