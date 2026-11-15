package main

// 官方 `create_personalized_page` 卡片（rc.2 `#13 creation-recommendations` 条目）
// 的端到端验收：真发 HTTP 到宿主 `/api/actionCards.act`，断言**真实响应形状**
// 与**真实副作用**（v4 落盘的决定序列、profile 保留副本、pluginManager 状态）。
//
// 不 mock actionCards、不 mock pluginManager：`actionCardAct(home)` 走的是
// 生产用的同一个 `actioncards.Open(home)`，`actionCards.act` 的分发、phase
// 闸门、decision 落盘全部真实执行。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/actioncards"
	"dshgo/internal/creation"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

const creationFixtureID = "11111111-2222-4333-8444-555555555555"

type creationEnv struct {
	base    string
	cookie  string
	home    string
	manager *pluginmgr.Manager
	cards   *actioncards.Service
}

// newCreationEnv 起一个真实 transport Server：只挂官方账号组的
// `actionCards.act`（创建 kind 的动词都从这里进来），并注册 creation kind。
func newCreationEnv(t *testing.T, opts ...creation.Option) creationEnv {
	t.Helper()
	home := t.TempDir()
	manager, err := pluginmgr.Open(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatalf("pluginmgr.Open: %v", err)
	}
	if _, err := newCreationRecommendations(home, manager, opts...); err != nil {
		t.Fatalf("newCreationRecommendations: %v", err)
	}
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialAccountAPI(transport.OfficialAccountAPI{ActionCardAct: actionCardAct(home)})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	// actioncards.Open 按 **home 的绝对路径**复用：每个用例一个全新 TempDir，
	// 所以既拿到"生产用的同一个 Service"，也不会污染别的用例。
	return creationEnv{
		base: ts.URL, cookie: srv.MintInternalCookie(), home: home, manager: manager,
		cards: actioncards.Open(home),
	}
}

// seedCreationSession 在真实 v4 里写一张 `create_personalized_page` 卡
// （propose_action 的 call + 成功 result），可选预置一条 decision。
func seedCreationSession(t *testing.T, home, sessionID, callID string, details map[string]any, decision map[string]any) {
	t.Helper()
	lg, err := session.CreateSession(home, filepath.Join(home, "cwd"), sessionID, "default")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"action": creation.Action, "title": "本次验收清单",
		"brief": "在当前会话右侧展示验收清单，可勾选和取消。", "details": details,
	})
	if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, callID, "propose_action", string(args))); err != nil {
		t.Fatalf("tool/call: %v", err)
	}
	if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, callID,
		[]spi.Block{{Type: "text", Text: "Card shown"}}, false, "", nil)); err != nil {
		t.Fatalf("tool/result: %v", err)
	}
	if decision != nil {
		if err := lg.Append("plugin:action-cards/decision", decision); err != nil {
			t.Fatalf("decision: %v", err)
		}
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// creationDecisionPhases 读真实 v4，按写入顺序返回这张卡的 decision phase。
func creationDecisionPhases(t *testing.T, home, sessionID, callID string) []string {
	t.Helper()
	path, err := session.FindByID(home, sessionID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var phases []string
	for _, event := range log.Events {
		if event.Type != "plugin:action-cards/decision" {
			continue
		}
		var decision actioncards.Decision
		if json.Unmarshal(event.Data, &decision) != nil || decision.CallID != callID {
			continue
		}
		phases = append(phases, decision.Phase)
	}
	return phases
}

// writeCreationPackage 写一份本仓**真能装**的 wasm 插件包（dsh.local-plugin@1）。
func writeCreationPackage(t *testing.T, dir, id, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	payload := []byte("creation e2e wasm payload")
	sum := sha256.Sum256(payload)
	manifest := map[string]any{
		"schema": "dsh.local-plugin@1", "id": id, "version": version,
		"kind": "wasm", "entry": "plugin.wasm",
		"files": map[string]string{"plugin.wasm": hex.EncodeToString(sum[:])},
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatalf("写 plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), payload, 0o644); err != nil {
		t.Fatalf("写 plugin.wasm: %v", err)
	}
	return dir
}

// writeCreationGeneratedBundle 按官方判据写一份"生成页面包"（package.json +
// JSON 补丁 + 已构建入口）。用来构造"保留副本还在"的真实 restore 场景。
func writeCreationGeneratedBundle(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manifest := map[string]any{
		"name": name, "version": "1.0.0", "main": "./index.js",
		"dsh": map[string]any{"bundle": map[string]any{"patch": "./cordis.patch.yml"}},
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "package.json"), raw, 0o600); err != nil {
		t.Fatalf("写 package.json: %v", err)
	}
	patch := `[{"insert":[{"id":"` + name + `:page","name":"` + name + `"}]}]`
	if err := os.WriteFile(filepath.Join(dir, "cordis.patch.yml"), []byte(patch), 0o600); err != nil {
		t.Fatalf("写 cordis.patch.yml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("export default {}"), 0o600); err != nil {
		t.Fatalf("写 index.js: %v", err)
	}
}

// e2eCreator 是注入给 create 的确定性生成器：在官方指定的暂存目录里写出一份
// 结构合法的生成包。这不是"假数据"——它写的是真实文件，后续的真校验/真复制/
// 真安装都跑在它写出的东西上。
type e2eCreator struct {
	mu       sync.Mutex
	requests []creation.CreateRequest
}

func (c *e2eCreator) Create(_ context.Context, request creation.CreateRequest) error {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.mu.Unlock()
	// 校验判据要求补丁行 name 必须是本包或本包导出；这里用包根入口。
	manifest := map[string]any{
		"name": request.BundleName, "version": "1.0.0", "main": "./index.js",
		"dsh": map[string]any{"bundle": map[string]any{"patch": "./cordis.patch.yml"}},
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.MkdirAll(filepath.Join(request.SourceDirectory, "lib"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(request.SourceDirectory, "package.json"), raw, 0o600); err != nil {
		return err
	}
	patch := `[{"insert":[{"id":"` + request.BundleName + `:page","name":"` + request.BundleName + `"}]}]`
	if err := os.WriteFile(filepath.Join(request.SourceDirectory, "cordis.patch.yml"), []byte(patch), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(request.SourceDirectory, "index.js"), []byte("export default {}"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(request.SourceDirectory, "lib", "panel.js"), []byte("export const panel = 1"), 0o600)
}

// ---------------------------------------------------------------------------
// create：生产路径（没有 Creator）如实拒绝，且一个字节都不写
// ---------------------------------------------------------------------------

func TestCreationCreateRefusedOverHTTP(t *testing.T) {
	env := newCreationEnv(t)
	seedCreationSession(t, env.home, "s-creation", "call-creation",
		map[string]any{"slot": "sidebar-right", "initialContent": "1. 导出成功\n2. 断网提示清楚"}, nil)

	status, envelope := postRecommendationHTTP(t, env.base, env.cookie, "actionCards.act",
		`{"agentId":"s-creation","callId":"call-creation","verb":"create","payload":{"slot":"sidebar-right"}}`)
	if status != http.StatusOK {
		t.Fatalf("create = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["status"] != "refused" || result["reason"] != creation.ReasonCreatorUnavailable {
		t.Fatalf("没有 Creator 时必须 refused{%s}，实际 %+v", creation.ReasonCreatorUnavailable, result)
	}
	// 再点一次仍是 refused（而不是 settled）：证明上面那次**没有**写入任何 decision。
	_, envelope = postRecommendationHTTP(t, env.base, env.cookie, "actionCards.act",
		`{"agentId":"s-creation","callId":"call-creation","verb":"create","payload":{"slot":"sidebar-right"}}`)
	result, _ = envelope["result"].(map[string]any)
	if result["status"] != "refused" {
		t.Fatalf("重复 create 仍应 refused（没有任何 decision 被写），实际 %+v", result)
	}
	if phases := creationDecisionPhases(t, env.home, "s-creation", "call-creation"); len(phases) != 0 {
		t.Fatalf("拒绝时不该写 decision，实际 %v", phases)
	}
	// 真实"没写盘"证据。
	if _, err := os.Stat(filepath.Join(env.home, creation.CreatedPluginsDir)); !os.IsNotExist(err) {
		t.Fatalf("拒绝时不该创建 profile 的 %s: %v", creation.CreatedPluginsDir, err)
	}
	if _, err := os.Stat(filepath.Join(env.home, "cwd", creation.StagingDirName)); !os.IsNotExist(err) {
		t.Fatalf("拒绝时不该创建暂存目录: %v", err)
	}
}

func TestCreationCreateInvalidLocationOverHTTP(t *testing.T) {
	env := newCreationEnv(t)
	seedCreationSession(t, env.home, "s-creation", "call-creation", map[string]any{"slot": "page"}, nil)

	for _, payload := range []string{`{}`, `{"slot":"middle"}`, `{"slot":"page","customLocation":"  "}`} {
		status, envelope := postRecommendationHTTP(t, env.base, env.cookie, "actionCards.act",
			`{"agentId":"s-creation","callId":"call-creation","verb":"create","payload":`+payload+`}`)
		if status != http.StatusOK {
			t.Fatalf("create(%s) = %d %+v", payload, status, envelope)
		}
		result, _ := envelope["result"].(map[string]any)
		if result["status"] != "refused" || result["reason"] != creation.ReasonInvalidLocation {
			t.Fatalf("非法 location 必须 refused{%s}，payload=%s 实际 %+v", creation.ReasonInvalidLocation, payload, result)
		}
	}
}

// ---------------------------------------------------------------------------
// create：注入 Creator → 真写盘、真校验、真调安装器（安装器真实拒绝 → failed）
// ---------------------------------------------------------------------------

func TestCreationCreateWithCreatorRealPipelineOverHTTP(t *testing.T) {
	creator := &e2eCreator{}
	env := newCreationEnv(t, creation.WithCreator(creator))
	seedCreationSession(t, env.home, "s-creation", "call-creation",
		map[string]any{"slot": "sidebar-left", "initialContent": "第 1 周 12，第 2 周 9"}, nil)

	status, envelope := postRecommendationHTTP(t, env.base, env.cookie, "actionCards.act",
		`{"agentId":"s-creation","callId":"call-creation","verb":"create",`+
			`"payload":{"slot":"sidebar-left","customLocation":"左边第二个入口"}}`)
	if status != http.StatusOK {
		t.Fatalf("create = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["status"] != "settled" {
		t.Fatalf("create 应同步给 settled，实际 %+v", result)
	}
	decision, _ := result["decision"].(map[string]any)
	if decision["phase"] != creation.PhaseFailed {
		t.Fatalf("本仓安装器不接受 Cordis/npm 包，phase 必须是 failed，实际 %+v", decision)
	}
	data, _ := decision["data"].(map[string]any)
	bundleName, _ := data["bundleName"].(string)
	if !strings.HasPrefix(bundleName, creation.HostBundlePrefix) {
		t.Fatalf("decision.data.bundleName 必须是宿主保留名，实际 %v", data)
	}
	if data["slot"] != creation.SlotSidebarLeft || data["customLocation"] != "左边第二个入口" {
		t.Fatalf("decision.data 位置字段 = %+v", data)
	}

	if len(creator.requests) != 1 {
		t.Fatalf("Creator 应被调用一次，实际 %d", len(creator.requests))
	}
	request := creator.requests[0]
	if request.BundleName != bundleName {
		t.Fatalf("Creator 拿到的保留名与 decision 不一致: %q vs %q", request.BundleName, bundleName)
	}
	if !request.HasInitialContent || !strings.Contains(request.InitialContent, "第 1 周 12") {
		t.Fatalf("Creator 必须拿到 initialContent: %+v", request)
	}

	// 真落盘：暂存目录 + profile 保留副本（真实文件）。
	if _, err := os.Stat(filepath.Join(request.SourceDirectory, "package.json")); err != nil {
		t.Fatalf("暂存目录缺 package.json: %v", err)
	}
	id, err := creation.IDOfBundleName(bundleName)
	if err != nil {
		t.Fatalf("IDOfBundleName: %v", err)
	}
	copyDir := creation.ProfileCopyDir(env.home, id)
	if _, err := os.Stat(filepath.Join(copyDir, "lib", "panel.js")); err != nil {
		t.Fatalf("profile 保留副本缺子目录文件: %v", err)
	}
	// 保留副本仍是可安装候选（restore 靠它）：用同一个公开校验器确认。
	if err := creation.ValidateBundle(copyDir, bundleName); err != nil {
		t.Fatalf("保留副本应通过官方判据: %v", err)
	}
	// 真安装器：没装上（不是假 installed）。
	if _, installed, err := env.manager.Bundle(bundleName); err != nil || installed {
		t.Fatalf("Cordis/npm 包不该被 pluginmgr 装上: installed=%v err=%v", installed, err)
	}
	// 中间态真的落过盘：creating 在起 Creator 之前，installing 在真装之前。
	if phases := creationDecisionPhases(t, env.home, "s-creation", "call-creation"); strings.Join(phases, ",") != "creating,installing,failed" {
		t.Fatalf("decision 序列应为 creating,installing,failed，实际 %v", phases)
	}
	// 投影里的最终 phase 也必须是 failed（GUI 读的就是它）。
	view, err := env.cards.View("s-creation")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if view.Decisions["call-creation"].Phase != creation.PhaseFailed {
		t.Fatalf("投影 phase = %+v", view.Decisions["call-creation"])
	}
}

// ---------------------------------------------------------------------------
// dismiss / undo / restore：真实落盘与真实 pluginManager 副作用
// ---------------------------------------------------------------------------

func TestCreationDismissRecordsLocationOverHTTP(t *testing.T) {
	env := newCreationEnv(t)
	seedCreationSession(t, env.home, "s-creation", "call-creation", map[string]any{"slot": "page"}, nil)

	status, envelope := postRecommendationHTTP(t, env.base, env.cookie, "actionCards.act",
		`{"agentId":"s-creation","callId":"call-creation","verb":"dismiss",`+
			`"payload":{"slot":"sidebar-left","customLocation":"  左边第二个入口  "}}`)
	if status != http.StatusOK {
		t.Fatalf("dismiss = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["status"] != "settled" {
		t.Fatalf("dismiss 应 settled，实际 %+v", result)
	}
	decision, _ := result["decision"].(map[string]any)
	if decision["phase"] != "dismissed" {
		t.Fatalf("dismiss 的 phase = %+v", decision)
	}
	data, _ := decision["data"].(map[string]any)
	if data["slot"] != creation.SlotSidebarLeft || data["customLocation"] != "左边第二个入口" {
		t.Fatalf("Not now 也要记下位置并 trim，实际 %+v", data)
	}
	if phases := creationDecisionPhases(t, env.home, "s-creation", "call-creation"); strings.Join(phases, ",") != "dismissed" {
		t.Fatalf("dismiss 的落盘序列 = %v", phases)
	}
}

func TestCreationUndoReallyUninstallsOverHTTP(t *testing.T) {
	env := newCreationEnv(t)
	bundleName := creation.BundleName(creationFixtureID)
	pkg := writeCreationPackage(t, filepath.Join(env.home, "pkg"), bundleName, "1.0.0")
	if _, err := env.manager.Install(pkg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	seedCreationSession(t, env.home, "s-creation", "call-creation", map[string]any{"slot": "page"},
		map[string]any{"callId": "call-creation", "action": creation.Action, "phase": creation.PhaseInstalled,
			"data": map[string]any{"slot": "page", "bundleName": bundleName}})

	status, envelope := postRecommendationHTTP(t, env.base, env.cookie, "actionCards.act",
		`{"agentId":"s-creation","callId":"call-creation","verb":"undo","payload":{}}`)
	if status != http.StatusOK {
		t.Fatalf("undo = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	decision, _ := result["decision"].(map[string]any)
	if decision["phase"] != creation.PhaseRemoved {
		t.Fatalf("undo 应记 removed，实际 %+v", decision)
	}
	data, _ := decision["data"].(map[string]any)
	if data["application"] != creation.ApplicationApplied {
		t.Fatalf("undo 的 application 应为 applied，实际 %+v", data)
	}
	// 真实副作用：pluginManager 里真的没有了。
	if _, installed, err := env.manager.Bundle(bundleName); err != nil || installed {
		t.Fatalf("undo 必须真的卸载: installed=%v err=%v", installed, err)
	}
}

func TestCreationRestoreRealAttemptOverHTTP(t *testing.T) {
	env := newCreationEnv(t)
	bundleName := creation.BundleName(creationFixtureID)
	// 真实保留副本（"曾经生成过、后来被移除"的页面包）。
	writeCreationGeneratedBundle(t, creation.ProfileCopyDir(env.home, creationFixtureID), bundleName)
	seedCreationSession(t, env.home, "s-creation", "call-creation", map[string]any{"slot": "page"},
		map[string]any{"callId": "call-creation", "action": creation.Action, "phase": creation.PhaseRemoved,
			"data": map[string]any{"slot": "page", "bundleName": bundleName, "application": creation.ApplicationApplied}})

	status, envelope := postRecommendationHTTP(t, env.base, env.cookie, "actionCards.act",
		`{"agentId":"s-creation","callId":"call-creation","verb":"restore","payload":{}}`)
	if status != http.StatusOK {
		t.Fatalf("restore = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	decision, _ := result["decision"].(map[string]any)
	if decision["phase"] != creation.PhaseFailed {
		t.Fatalf("restore 真装被本仓安装器拒绝后应记 failed，实际 %+v", decision)
	}
	data, _ := decision["data"].(map[string]any)
	if _, present := data["application"]; present {
		t.Fatalf("restore 的收据不该保留上一次的 application，实际 %+v", data)
	}
	if _, installed, err := env.manager.Bundle(bundleName); err != nil || installed {
		t.Fatalf("不该假装装上: installed=%v err=%v", installed, err)
	}
	if phases := creationDecisionPhases(t, env.home, "s-creation", "call-creation"); strings.Join(phases, ",") != "removed,installing,failed" {
		t.Fatalf("restore 的落盘序列 = %v", phases)
	}
}

func TestCreationRestoreSourceMissingOverHTTP(t *testing.T) {
	env := newCreationEnv(t)
	bundleName := creation.BundleName(creationFixtureID)
	seedCreationSession(t, env.home, "s-creation", "call-creation", map[string]any{"slot": "page"},
		map[string]any{"callId": "call-creation", "action": creation.Action, "phase": creation.PhaseRemoved,
			"data": map[string]any{"slot": "page", "bundleName": bundleName}})

	status, envelope := postRecommendationHTTP(t, env.base, env.cookie, "actionCards.act",
		`{"agentId":"s-creation","callId":"call-creation","verb":"restore","payload":{}}`)
	if status != http.StatusOK {
		t.Fatalf("restore = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["status"] != "refused" || result["reason"] != creation.ReasonSourceMissing {
		t.Fatalf("保留副本不存在必须 refused{%s}，实际 %+v", creation.ReasonSourceMissing, result)
	}
}
