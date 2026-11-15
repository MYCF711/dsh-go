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

// findRow 在官方形状的 inventory 里按 pluginId 找一行。
func findRow(t *testing.T, rows []cordisInventoryRow, id string) cordisInventoryRow {
	t.Helper()
	for _, row := range rows {
		if row.PluginID == id {
			return row
		}
	}
	t.Fatalf("inventory 里没有 %q：%+v", id, rows)
	return cordisInventoryRow{}
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
	rows := inv.([]cordisInventoryRow)
	// go-a（注册在册）+ js-a（盘上真实目录）都该列出来，未激活也要看得见。
	if len(rows) != 2 {
		t.Fatalf("inventory=%+v", rows)
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
	if !stopped.(cordisStopOK).OK {
		t.Fatalf("stop=%+v", stopped)
	}
	undef, err := callCordis(t, s, "dynamicCordisRunner.undefineFromPanel", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt := undef.(cordisUndefineOK); !receipt.OK || receipt.WasRunning {
		t.Fatalf("undefine 回执不是官方的 {ok:true,wasRunning:false}：%+v", receipt)
	}
}

// ── 官方形状：逐字段 ────────────────────────────────────────────────────
//
// 官方 inventory schema 在 `dsh-cordis-host-runner/lib/typert.host.js:33-63`。
// 这组用例钉的是"每个字段都是真值或如实缺席"，不是"字段存在"。

func TestCordisInventoryOfficialShape(t *testing.T) {
	home := t.TempDir()
	s := newCordisService(home)
	makeJSPlugin(t, home, "js-shape")

	inv, err := callCordis(t, s, "dynamicCordisRunner.inventory", nil)
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, inv.([]cordisInventoryRow), "js-shape")
	// 从未激活：有包元数据（盘上读的），但没有 run，也没有归属会话。
	if row.AgentID != "" {
		t.Fatalf("没人激活过却给出了归属会话：%q", row.AgentID)
	}
	if len(row.Packages) != 1 {
		t.Fatalf("packages=%+v", row.Packages)
	}
	pkg := row.Packages[0]
	if pkg.Name != "js-shape" || pkg.PackageID == "" || !pkg.HasHostHalf || !pkg.HasClientHalf {
		t.Fatalf("包元数据不是盘上的真值：%+v", pkg)
	}
	if pkg.Purpose != "" {
		t.Fatalf("purpose 在盘上没有来源，必须留空：%q", pkg.Purpose)
	}
	if row.CurrentPackageID != "" || row.ActiveRun != nil || row.LatestRun != nil || row.NextPackageID != "" {
		t.Fatalf("未激活不该有 run 字段：%+v", row)
	}

	// 激活（带归属会话）之后：currentPackageId / activeRun / latestRun 都要点得起来。
	if _, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf",
		map[string]any{"pluginId": "js-shape", "sessionId": "sess-1"}); err != nil {
		t.Fatal(err)
	}
	inv, err = callCordis(t, s, "dynamicCordisRunner.inventory", nil)
	if err != nil {
		t.Fatal(err)
	}
	row = findRow(t, inv.([]cordisInventoryRow), "js-shape")
	if row.AgentID != "sess-1" {
		t.Fatalf("agentId 应来自真实激活参数：%q", row.AgentID)
	}
	if row.CurrentPackageID != pkg.PackageID {
		t.Fatalf("currentPackageId=%q，包身份=%q", row.CurrentPackageID, pkg.PackageID)
	}
	if row.ActiveRun == nil || row.ActiveRun.PackageID != pkg.PackageID || row.ActiveRun.PluginRunID == "" {
		t.Fatalf("activeRun=%+v", row.ActiveRun)
	}
	run := row.LatestRun
	if run == nil {
		t.Fatal("latestRun 缺失")
	}
	if run.PluginRunID != row.ActiveRun.PluginRunID || run.PackageID != pkg.PackageID ||
		run.Mode != "run" || run.Status != "running" {
		t.Fatalf("latestRun=%+v", run)
	}
	if run.Host.Status != "running" || run.Client.Status != "pending" {
		t.Fatalf("两半状态不是真值：host=%+v client=%+v", run.Host, run.Client)
	}
	if run.Host.WaitingFor == nil || run.Client.WaitingFor == nil {
		t.Fatalf("waitingFor 必须是数组（不能是 null）：%+v", run)
	}

	// 没有 Client 半的插件：client.status 必须是 absent，不能装成 pending。
	onlyHost := filepath.Join(home, "cordis-plugins", "js-hostonly")
	if err := os.MkdirAll(onlyHost, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onlyHost, "package.json"),
		[]byte(`{"name":"js-hostonly","version":"1.0.0","main":"host.cjs"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onlyHost, "host.cjs"), []byte("module.exports = {};"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf",
		map[string]any{"pluginId": "js-hostonly", "sessionId": "sess-1"}); err != nil {
		t.Fatal(err)
	}
	inv, err = callCordis(t, s, "dynamicCordisRunner.inventory", nil)
	if err != nil {
		t.Fatal(err)
	}
	only := findRow(t, inv.([]cordisInventoryRow), "js-hostonly")
	if only.Packages[0].HasClientHalf {
		t.Fatal("没有 client/client.js 却报了 HasClientHalf")
	}
	if only.LatestRun == nil || only.LatestRun.Client.Status != "absent" {
		t.Fatalf("client 半应为 absent：%+v", only.LatestRun)
	}
}

// ── stop / undefine 的官方回执 ──────────────────────────────────────────
//
// 官方 `DynamicCordisStopResponse` / `DynamicCordisUndefineReceipt`
// （`dsh-cordis-host-runner/src/types.ts:299-322`）：失败也是 **200 里的值**，
// 带 reason，而不是 HTTP 四字段错误——官方面板按 reason 分支。

func TestCordisPanelReceiptsAreOfficial(t *testing.T) {
	home := t.TempDir()
	s := newCordisService(home)
	makeJSPlugin(t, home, "js-panel")

	// 插件不存在：plugin-missing，且是值不是错误。
	got, err := callCordis(t, s, "dynamicCordisRunner.stopFromPanel", map[string]any{"pluginId": "nope"})
	if err != nil {
		t.Fatalf("plugin-missing 不该是 HTTP 错误：%v", err)
	}
	if r := got.(cordisRefusal); r.OK || r.Reason != "plugin-missing" || r.Message == "" {
		t.Fatalf("stop 缺失插件回执=%+v", r)
	}
	got, err = callCordis(t, s, "dynamicCordisRunner.undefineFromPanel", map[string]any{"pluginId": "nope"})
	if err != nil {
		t.Fatalf("undefine plugin-missing 不该是 HTTP 错误：%v", err)
	}
	if r := got.(cordisRefusal); r.OK || r.Reason != "plugin-missing" {
		t.Fatalf("undefine 缺失插件回执=%+v", r)
	}

	// 插件在但没跑：not-running。
	got, err = callCordis(t, s, "dynamicCordisRunner.stopFromPanel", map[string]any{"pluginId": "js-panel"})
	if err != nil {
		t.Fatal(err)
	}
	if r := got.(cordisRefusal); r.OK || r.Reason != "not-running" {
		t.Fatalf("未运行时的 stop 回执=%+v", r)
	}

	// 真跑起来 → stop 成功只回 {ok:true}。
	if _, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf",
		map[string]any{"pluginId": "js-panel", "sessionId": "sess-2"}); err != nil {
		t.Fatal(err)
	}
	got, err = callCordis(t, s, "dynamicCordisRunner.stopFromPanel",
		map[string]any{"pluginId": "js-panel", "sessionId": "sess-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.(cordisStopOK).OK {
		t.Fatalf("stop 回执=%+v", got)
	}
	raw, _ := json.Marshal(got)
	if string(raw) != `{"ok":true}` {
		t.Fatalf("stop 成功回执必须恰好是 {ok:true}：%s", raw)
	}

	// 停下之后 inventory 要如实变成 stopped（不是 running、也不是消失）。
	inv, err := callCordis(t, s, "dynamicCordisRunner.inventory", nil)
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, inv.([]cordisInventoryRow), "js-panel")
	if row.ActiveRun != nil || row.CurrentPackageID != "" {
		t.Fatalf("停下后仍有 activeRun/currentPackageId：%+v", row)
	}
	if row.LatestRun == nil || row.LatestRun.Status != "stopped" || row.LatestRun.Host.Status != "stopped" {
		t.Fatalf("停下后 latestRun 不是 stopped：%+v", row.LatestRun)
	}

	// 再跑一次再移除：wasRunning 必须为真（官方用它决定要不要注入用户消息）。
	if _, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf",
		map[string]any{"pluginId": "js-panel", "sessionId": "sess-2"}); err != nil {
		t.Fatal(err)
	}
	got, err = callCordis(t, s, "dynamicCordisRunner.undefineFromPanel",
		map[string]any{"pluginId": "js-panel", "sessionId": "sess-2"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt := got.(cordisUndefineOK); !receipt.OK || !receipt.WasRunning {
		t.Fatalf("undefine 回执=%+v", receipt)
	}
	inv, err = callCordis(t, s, "dynamicCordisRunner.inventory", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range inv.([]cordisInventoryRow) {
		if row.PluginID == "js-panel" {
			t.Fatalf("移除后还在 inventory 里：%+v", row)
		}
	}
	// "移除"必须是真的：目录没了，inventory 才不会再把它读回来。
	if _, err := os.Stat(filepath.Join(home, "cordis-plugins", "js-panel")); !os.IsNotExist(err) {
		t.Fatalf("undefine 之后插件目录仍在（err=%v）", err)
	}
}

// ── 两条事件的真实发射路径 ──────────────────────────────────────────────
//
// 官方这 4 条是 **forwarded Host cordis 事件**（`dsh-api-remotes/src/remote-events.ts`
// 的 `mode: 'emit'`，走 WS，不是 HTTP 流）。本宿主能真发的只有 2 条：
// `cordis/dynamic-package`（激活成功）与 `cordis/dynamic-retract`（stop/undefine）。
// 落点与 agent 生命周期帧同一处：该**归属会话**的 Transport journal + `/ws`。
// 没有归属会话就**不发帧**——那会是一条没有接收方、也没有对应激活的假帧。

func TestCordisEventsFireOnlyOnRealActivations(t *testing.T) {
	home := t.TempDir()
	s := newCordisService(home)
	journals := transport.NewJournals(nil)
	var frames []transport.ServerFrame
	var topics []string
	s.withEvents(journals, func(topic string, frame transport.ServerFrame) {
		topics = append(topics, topic)
		frames = append(frames, frame)
	})
	makeJSPlugin(t, home, "js-ev")
	makeJSPlugin(t, home, "js-ev2")

	// 没带归属会话：不发帧。
	if _, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf",
		map[string]any{"pluginId": "js-ev"}); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("没有归属会话却发了帧：%+v", frames)
	}

	// 带归属会话激活：一条 dynamic-package，落在 session:sess-3 上。
	if _, err := callCordis(t, s, "dynamicCordisRunner.runHostHalf",
		map[string]any{"pluginId": "js-ev2", "sessionId": "sess-3"}); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].Type != "cordis/dynamic-package" {
		t.Fatalf("激活事件不对：%+v", frames)
	}
	if topics[0] != "session:sess-3" {
		t.Fatalf("事件 topic=%q，应落在归属会话", topics[0])
	}
	var payload struct {
		PluginID    string `json:"pluginId"`
		PackageID   string `json:"packageId"`
		PluginRunID string `json:"pluginRunId"`
		Name        string `json:"name"`
	}
	if err := json.Unmarshal(frames[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	inv, err := callCordis(t, s, "dynamicCordisRunner.inventory", nil)
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, inv.([]cordisInventoryRow), "js-ev2")
	if payload.PluginID != "js-ev2" || payload.PackageID != row.Packages[0].PackageID ||
		payload.PluginRunID != row.ActiveRun.PluginRunID || payload.Name != "js-ev2" {
		t.Fatalf("dynamic-package 负载与 inventory 不一致：%+v vs %+v", payload, row)
	}
	if frames[0].Seq <= 0 || frames[0].Time <= 0 {
		t.Fatalf("帧没有真实 seq/time：%+v", frames[0])
	}

	// stop：一条 dynamic-retract（归属会话在激活时已记下，stop 不必再带）。
	if _, err := callCordis(t, s, "dynamicCordisRunner.stopFromPanel",
		map[string]any{"pluginId": "js-ev2"}); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[1].Type != "cordis/dynamic-retract" {
		t.Fatalf("撤下事件不对：%+v", frames)
	}
	var retracted struct {
		PluginID    string `json:"pluginId"`
		PackageID   string `json:"packageId"`
		PluginRunID string `json:"pluginRunId"`
	}
	if err := json.Unmarshal(frames[1].Data, &retracted); err != nil {
		t.Fatal(err)
	}
	if retracted.PluginID != payload.PluginID || retracted.PackageID != payload.PackageID ||
		retracted.PluginRunID != payload.PluginRunID {
		t.Fatalf("dynamic-retract 负载=%+v，激活时是=%+v", retracted, payload)
	}

	// 帧真的进了 journal（= session.history 与 /ws 读的那一份）。
	recorded, _, _ := journals.Get("sess-3").Since(transport.SinceNothing, 0)
	if len(recorded) != 2 || recorded[0].Type != "cordis/dynamic-package" ||
		recorded[1].Type != "cordis/dynamic-retract" {
		t.Fatalf("journal 里的 cordis 帧=%+v", recorded)
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
