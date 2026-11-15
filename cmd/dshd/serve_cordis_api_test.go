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

// ── 全链条：inventory / runHostHalf / getClientCode / invoke / settle ────
//
// 这条用例来自 #49 的端到端冒烟，形状按合并后的官方契约对齐（runHostHalf 回
// 官方 ok/startedHere、getClientCode 回五个字段、invoke 的失败是 200 里的
// 判别联合、settle 以 pluginId+resolution 为参）：断言强度不变，只是钉的是
// 官方字段名。

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
	runHost := mustValue[cordisRunHostOK](t, "runHostHalf", host)
	// 外部 node 真被拉起：进程号与真实导出的方法表都要在（官方 5 个方法的回执里
	// 没有这两个字段，所以直接读内部快照——断言强度与 #49 相同）。
	snap, ok := s.entrySnapshot("js-a")
	if !ok || snap.PID == 0 || len(snap.Methods) != 2 {
		t.Fatalf("host=%+v snapshot=%+v", runHost, snap)
	}
	code, err := callCordis(t, s, "dynamicCordisRunner.getClientCode", map[string]any{"pluginId": "js-a"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustValue[cordisClientCode](t, "getClientCode", code).Code, "js-a") {
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
	failed, err := callCordis(t, s, "dynamicCordisRunner.invoke", map[string]any{
		"pluginId": "js-a", "requestId": "req-fail", "method": "fail", "args": nil,
	})
	if err != nil {
		t.Fatalf("官方 invoke 的失败是 200 里的值，不该是 HTTP 错误：%v", err)
	}
	// 插件抛错必须失败（且是官方判别联合的 handler-error 分支）。
	if f := mustValue[cordisInvokeFailure](t, "invoke(fail)", failed); f.OK || f.Code != "handler-error" {
		t.Fatalf("插件抛错必须失败：%+v", f)
	}
	settled, err := callCordis(t, s, "dynamicCordisRunner.settleUserRun", map[string]any{
		"pluginId": "js-a",
		"resolution": map[string]any{"ok": true, "pluginRunId": runHost.PluginRunID,
			"waitingFor": []string{"ui"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok := mustValue[cordisSettleOK](t, "settleUserRun", settled); !ok.OK ||
		ok.PluginRunID != runHost.PluginRunID || ok.Status != "running" {
		t.Fatalf("settle=%+v", settled)
	}
	// 本宿主没有 model 侧待审批请求表：任何 requestId 都是官方的 unknown 分支，
	// 不许 accepted（也绝不假装结算掉了一个请求）。
	unknown, err := callCordis(t, s, "dynamicCordisRunner.resolveRequestRun", map[string]any{"requestId": "unknown"})
	if err != nil {
		t.Fatalf("官方 resolveRequestRun 的 unknown 分支是值不是 HTTP 错误：%v", err)
	}
	if got := mustValue[cordisResolveReceipt](t, "resolveRequestRun", unknown); got.Accepted {
		t.Fatal("未知 requestId 不许 accepted")
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

	// 激活（带归属会话）之后：latestRun / activeRun 立刻点得起来；包里有 Client 半，
	// 所以这次运行停在 client-pending，currentPackageId 要等页面结算才推进。
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
	if row.CurrentPackageID != "" {
		t.Fatalf("Client 半还没结算就推进了 currentPackageId：%q", row.CurrentPackageID)
	}
	if row.ActiveRun == nil || row.ActiveRun.PackageID != pkg.PackageID || row.ActiveRun.PluginRunID == "" {
		t.Fatalf("activeRun=%+v", row.ActiveRun)
	}
	run := row.LatestRun
	if run == nil {
		t.Fatal("latestRun 缺失")
	}
	if run.PluginRunID != row.ActiveRun.PluginRunID || run.PackageID != pkg.PackageID ||
		run.Mode != "run" || run.Status != "waiting" {
		t.Fatalf("latestRun=%+v", run)
	}
	if run.Host.Status != "running" || run.Client.Status != "pending" {
		t.Fatalf("两半状态不是真值：host=%+v client=%+v", run.Host, run.Client)
	}
	if run.Host.WaitingFor == nil || run.Client.WaitingFor == nil {
		t.Fatalf("waitingFor 必须是数组（不能是 null）：%+v", run)
	}

	// 页面结算成功之后：这次运行才是 running，currentPackageId 才推进到该包。
	if _, err := callCordis(t, s, "dynamicCordisRunner.settleUserRun", map[string]any{
		"pluginId":   "js-shape",
		"resolution": map[string]any{"ok": true, "pluginRunId": run.PluginRunID},
	}); err != nil {
		t.Fatal(err)
	}
	inv, err = callCordis(t, s, "dynamicCordisRunner.inventory", nil)
	if err != nil {
		t.Fatal(err)
	}
	row = findRow(t, inv.([]cordisInventoryRow), "js-shape")
	if row.CurrentPackageID != pkg.PackageID {
		t.Fatalf("currentPackageId=%q，包身份=%q", row.CurrentPackageID, pkg.PackageID)
	}
	if row.ActiveRun == nil || row.ActiveRun.PackageID != pkg.PackageID || row.ActiveRun.PluginRunID == "" {
		t.Fatalf("结算后 activeRun=%+v", row.ActiveRun)
	}
	run = row.LatestRun
	if run == nil || run.PluginRunID != row.ActiveRun.PluginRunID || run.PackageID != pkg.PackageID ||
		run.Mode != "run" || run.Status != "running" {
		t.Fatalf("结算后 latestRun=%+v", run)
	}
	if run.Host.Status != "running" || run.Client.Status != "pending" {
		t.Fatalf("结算后两半状态不是真值：host=%+v client=%+v", run.Host, run.Client)
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
	// 没有 Client 半 = 官方原地 commit：currentPackageId 立刻就是这个包。
	if only.CurrentPackageID != only.Packages[0].PackageID {
		t.Fatalf("无 Client 半却没 commit：current=%q pkg=%q", only.CurrentPackageID, only.Packages[0].PackageID)
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

// ── runHostHalf 的官方形状（5 方法形状对齐） ─────────────────────────────

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
	// 没有归属会话的面板调用也照样能激活（wire 上带的是插件身份）。
	if rec := post("dynamicCordisRunner.runHostHalf", `{"pluginId":"js-http"}`); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), "js-http") {
		t.Fatalf("HTTP runHostHalf(panel)=%d %s", rec.Code, rec.Body.String())
	}
	invokeBody := `{"pluginId":"js-http","pluginRunId":"` + env.Result.PluginRunID + `","method":"echo","args":{"text":"wire"}}`
	if rec := post("dynamicCordisRunner.invoke", invokeBody); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), "wire") {
		t.Fatalf("HTTP invoke=%d %s", rec.Code, rec.Body.String())
	}
	// 插件抛错是官方 invoke 的失败分支：200 里的判别联合（code=handler-error），
	// 不是 HTTP 四字段错误——官方 UI 按 code 分支。
	if rec := post("dynamicCordisRunner.invoke",
		`{"pluginId":"js-http","pluginRunId":"`+env.Result.PluginRunID+`","method":"fail","args":{}}`); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"code":"handler-error"`) {
		t.Fatalf("HTTP invoke(fail)=%d %s", rec.Code, rec.Body.String())
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
