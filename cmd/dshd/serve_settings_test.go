package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dshgo/internal/llm"
	"dshgo/internal/settingsform"
)

func openSettingsStores(t *testing.T, home, baseModel, basePolicy string) (*serveSettingsStore, *modelOverrideStore, *permissionOverrideStore) {
	t.Helper()
	models := newModelOverrideStore(home)
	if err := models.Load(); err != nil {
		t.Fatal(err)
	}
	permissions := newPermissionOverrideStore(home)
	if err := permissions.Load(); err != nil {
		t.Fatal(err)
	}
	return newServeSettingsStore(baseModel, basePolicy, models, permissions), models, permissions
}

func setDefaultForTest(t *testing.T, store *serveSettingsStore, namespace, field, value string, revision int64) {
	t.Helper()
	svc := settingsform.New(store)
	_, err := svc.Mutate(namespace, []settingsform.PathOp{{Op: "set", Path: []string{field}, Value: value}}, &revision)
	if err != nil {
		t.Fatal(err)
	}
}

func resetDefaultForTest(t *testing.T, store *serveSettingsStore, namespace string, revision int64) {
	t.Helper()
	if _, err := settingsform.New(store).Reset(namespace, nil, &revision); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsDefaultsReachExistingTurnRunnerAcrossABResetAndReopen(t *testing.T) {
	home := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv.json")
	t.Setenv("DSHD_MODEL_WIRE_ARGV", capture)
	t.Setenv("DSHD_SERVE_NO_FOLLOW", "1")
	store, models, permissions := openSettingsStores(t, home, "base-model", "allow")
	// 第一份 models 在这段里用不上（断言的是 store/permissions；模型覆盖在下面 99 行
	// 重新打开一次之后才断言）——显式丢弃，别让"这一份确实没读"和"忘了读"长得一样。
	_ = models

	ids := []string{"session-a", "session-b", "session-c"}
	create := func(string) (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	createWithDefaults := createWithSettingsDefaults(create, store)

	setDefaultForTest(t, store, settingsform.NamespaceModel, settingsform.FieldDefaultModel, "model-A", 0)
	setDefaultForTest(t, store, settingsform.NamespacePermission, settingsform.FieldDefaultPreset, "deny", 0)
	sessionA, err := createWithDefaults("")
	if err != nil || sessionA != "session-a" {
		t.Fatalf("create A=%q err=%v", sessionA, err)
	}
	setDefaultForTest(t, store, settingsform.NamespaceModel, settingsform.FieldDefaultModel, "model-B", 1)
	setDefaultForTest(t, store, settingsform.NamespacePermission, settingsform.FieldDefaultPreset, "ask", 1)
	sessionB, err := createWithDefaults("")
	if err != nil || sessionB != "session-b" {
		t.Fatalf("create B=%q err=%v", sessionB, err)
	}
	resetDefaultForTest(t, store, settingsform.NamespaceModel, 2)
	resetDefaultForTest(t, store, settingsform.NamespacePermission, 2)
	sessionC, err := createWithDefaults("")
	if err != nil || sessionC != "session-c" {
		t.Fatalf("create C=%q err=%v", sessionC, err)
	}

	base := &loopRunner{home: home, policy: "allow", maxSteps: 6, model: "base-model"}
	runner := &modelOverrideLoopRunner{base: base, permissions: permissions}
	assertRun := func(sid, wantModel, wantPolicy string) {
		t.Helper()
		if err := runner.Run(context.Background(), sid, "turn-"+sid, "next-prompt"); err != nil {
			t.Fatalf("Run(%s): %v", sid, err)
		}
		model, args := readLoopModelArg(t, capture)
		if model != wantModel || !containsArgPair(args, "--policy", wantPolicy) {
			t.Fatalf("%s argv model=%q policy-want=%q args=%v", sid, model, wantPolicy, args)
		}
	}
	assertRun(sessionA, "model-A", "deny")
	assertRun(sessionB, "model-B", "ask")
	assertRun(sessionC, "base-model", "allow")

	// Reopen both existing files; the original runner still resolves session A/B/C.
	store, models, permissions = openSettingsStores(t, home, "base-model", "allow")
	_ = store
	_ = models
	runner = &modelOverrideLoopRunner{base: base, permissions: permissions}
	assertRun(sessionA, "model-A", "deny")
	assertRun(sessionB, "model-B", "ask")
	assertRun(sessionC, "base-model", "allow")
	if got := settingsform.New(store); got == nil {
		t.Fatal("reopened service is nil")
	}
	t.Log("SETTINGS_RUNNER_SUMMARY OK=1 FAIL=0")
}

func TestSettingsStoreFailedFlushRollsBackMemory(t *testing.T) {
	home := t.TempDir()
	store, models, _ := openSettingsStores(t, home, "base-model", "allow")
	setDefaultForTest(t, store, settingsform.NamespaceModel, settingsform.FieldDefaultModel, "persisted-model", 0)

	// Rename a file over an existing directory fails on Windows and POSIX. Only
	// this temporary test store is redirected; no shared path is touched.
	blocked := filepath.Join(home, "blocked-target")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	models.path = blocked
	_, err := store.Write(settingsform.NamespaceModel, map[string]any{settingsform.FieldDefaultModel: "must-not-appear"}, nil)
	if err == nil {
		t.Fatal("flush over directory unexpectedly succeeded")
	}
	doc, readErr := store.Read(settingsform.NamespaceModel)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := effectiveSettingsValue(doc, settingsform.FieldDefaultModel); got != "persisted-model" || doc.Revision != 1 {
		t.Fatalf("failed flush changed readable state: value=%q revision=%d", got, doc.Revision)
	}
}

func TestSettingsStoreStartupReadFailureIsNotDefaultSuccess(t *testing.T) {
	home := t.TempDir()
	store, _, _ := openSettingsStores(t, home, "base-model", "allow")
	store.setLoadErrors(errors.New("session-model.json: invalid JSON"), nil)
	_, err := settingsform.New(store).Get(settingsform.NamespaceModel)
	if err == nil || settingsform.AsError(err).Code != "internal" {
		t.Fatalf("startup corruption returned fake default: %v", err)
	}
	_, err = store.Write(settingsform.NamespaceModel, map[string]any{settingsform.FieldDefaultModel: "new"}, nil)
	if err == nil || settingsform.AsError(err).Code != "internal" {
		t.Fatalf("startup corruption allowed write: %v", err)
	}
}

func TestSettingsStoreRejectsCorruptReservedMetadata(t *testing.T) {
	home := t.TempDir()
	_, models, _ := openSettingsStores(t, home, "base-model", "allow")
	if err := os.MkdirAll(filepath.Dir(models.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(models.path, []byte(`{"\u0000settingsFormRevision":"broken"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _, _ := openSettingsStores(t, home, "base-model", "allow")
	_, err := settingsform.New(store).Get(settingsform.NamespaceModel)
	if err == nil || settingsform.AsError(err).Code != "internal" {
		t.Fatalf("corrupt revision error=%v", err)
	}
}

type settingsGolden struct {
	Accepted []struct {
		Method   string         `json:"method"`
		Params   map[string]any `json:"params"`
		Status   int            `json:"status"`
		Current  string         `json:"current"`
		Revision int64          `json:"revision"`
	} `json:"accepted"`
	Rejected []struct {
		Name   string         `json:"name"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
		Status int            `json:"status"`
		Error  string         `json:"error"`
	} `json:"rejected"`
}

func readSettingsGolden(t *testing.T) settingsGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "transport", "testdata", "settings-form-golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden settingsGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Accepted) == 0 || len(golden.Rejected) == 0 {
		t.Fatalf("empty golden: %+v", golden)
	}
	return golden
}

func settingsEnvelopeCurrent(t *testing.T, envelope map[string]any) (string, int64) {
	t.Helper()
	result := envelope["result"].(map[string]any)
	revision := int64(result["revision"].(float64))
	if fields, ok := result["fields"].([]any); ok {
		field := fields[0].(map[string]any)
		return field["current"].(string), revision
	}
	values := result["value"].(map[string]any)
	for _, value := range values {
		return value.(string), revision
	}
	t.Fatalf("no current value in %+v", envelope)
	return "", 0
}

func logSettingsEvidence(t *testing.T, label string, envelope map[string]any) {
	t.Helper()
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s", label, raw)
}

func TestSettingsFormRealDSHDAuthenticatedGoldenABRestartReset(t *testing.T) {
	exe := buildPluginGlueDSHD(t)
	decoy := filepath.Join(t.TempDir(), "decoy-home")
	homeA := filepath.Join(t.TempDir(), "home-a")
	homeB := filepath.Join(t.TempDir(), "home-b")
	serverA := startPluginGlueServer(t, exe, homeA, decoy)
	serverB := startPluginGlueServer(t, exe, homeB, decoy)

	anonymous := &http.Client{Timeout: postTimeoutForSettings}
	for _, method := range []string{"settings.describe", "settings.mutate", "settings.form.describe", "settings.form.get", "settings.form.set", "settings.form.reset"} {
		envelope := postPluginGlue(t, anonymous, serverA.base, method, map[string]any{}, http.StatusUnauthorized)
		assertPluginGlueError(t, envelope, "unauthorized")
	}
	clientA := connectPluginGlueServer(t, serverA)
	clientB := connectPluginGlueServer(t, serverB)

	described := postPluginGlue(t, clientA, serverA.base, "settings.describe", map[string]any{}, http.StatusOK)
	logSettingsEvidence(t, "REAL_DESCRIBE", described)
	result := described["result"].(map[string]any)
	// 11 个命名空间：model / permission / llm + 官方客户端与插件的 7 个 UI 命名空间
	// （locale / ui-theme / ui-chat / ui-settings / ui-conversation /
	// session-log-deepseek / session-telemetry-otel，后两者是本仓"能力上不做但
	// 结构化拒绝"的开关：声明 true / FEEDBACK_ONLY 会得到 501）+ 官方
	// agent-preset registry 的 selectedDefault（2026-11-15 补）。
	// `llm` 是 0.2.2-rc.2 加的（发布 `providers.<route>.retryPolicy` 节点，
	// 官方 UI 的"重试条件/次数/等待时间"就是从这里读候选与正下界的）；这些 UI
	// 命名空间与它共用同一份 <home>/state/settings-namespaces.json，
	// 带 revision 乐观锁。
	if len(result["namespaces"].([]any)) != 11 || len(result["sections"].([]any)) != 11 || len(result["errors"].([]any)) != 0 {
		t.Fatalf("bad real describe: %+v", result)
	}
	llmSection := f2LLMSection(t, result)
	if llmSection["writable"] != true {
		t.Fatalf("rc.2 的 llm 命名空间必须可写（缺口 #2），实际 %v：%v", llmSection["writable"], llmSection)
	}
	if providers, ok := f2LLMNamespace(t, result)["value"].(map[string]any)["providers"].(map[string]any); !ok || len(providers) != 0 {
		t.Fatalf("全新 home 的 llm.value 应是空 providers 字典：%v", f2LLMNamespace(t, result)["value"])
	}
	initialGet := postPluginGlue(t, clientA, serverA.base, "settings.form.get", map[string]any{"ns": "permission"}, http.StatusOK)
	if current, revision := settingsEnvelopeCurrent(t, initialGet); current != "allow" || revision != 0 {
		t.Fatalf("initial get current=%q revision=%d", current, revision)
	}
	logSettingsEvidence(t, "REAL_GET_INITIAL", initialGet)
	golden := readSettingsGolden(t)
	for _, accepted := range golden.Accepted {
		envelope := postPluginGlue(t, clientA, serverA.base, accepted.Method, accepted.Params, accepted.Status)
		current, revision := settingsEnvelopeCurrent(t, envelope)
		if current != accepted.Current || revision != accepted.Revision {
			t.Fatalf("accepted %s current=%q revision=%d, want %q/%d", accepted.Method, current, revision, accepted.Current, accepted.Revision)
		}
		logSettingsEvidence(t, "GOLDEN_ACCEPT", envelope)
	}
	for _, rejected := range golden.Rejected {
		params := rejected.Params
		if rejected.Name == "out of range" {
			params["value"] = strings.Repeat("x", 257)
		}
		envelope := postPluginGlue(t, clientA, serverA.base, rejected.Method, params, rejected.Status)
		assertPluginGlueError(t, envelope, rejected.Error)
		logSettingsEvidence(t, "GOLDEN_REJECT_"+rejected.Name, envelope)
	}

	// Set both again after the golden reset; these are the values that must survive restart.
	permissionSet := postPluginGlue(t, clientA, serverA.base, "settings.form.set", map[string]any{
		"ns": "permission", "path": []string{"defaultPreset"}, "value": "deny", "expectedRevision": 2,
	}, http.StatusOK)
	modelSet := postPluginGlue(t, clientA, serverA.base, "settings.mutate", map[string]any{
		"ns": "model", "ops": []any{map[string]any{"op": "set", "path": []string{"defaultModel"}, "value": "settings-model-A"}}, "expectedRevision": 2,
	}, http.StatusOK)
	logSettingsEvidence(t, "REAL_SET_PERMISSION", permissionSet)
	logSettingsEvidence(t, "REAL_SET_MODEL", modelSet)

	conflict := postPluginGlue(t, clientA, serverA.base, "settings.form.set", map[string]any{
		"ns": "permission", "path": []string{"defaultPreset"}, "value": "allow", "expectedRevision": 2,
	}, http.StatusConflict)
	assertPluginGlueError(t, conflict, "revision_conflict")
	logSettingsEvidence(t, "REAL_REVISION_CONFLICT", conflict)

	permissionB := postPluginGlue(t, clientB, serverB.base, "settings.form.get", map[string]any{"ns": "permission"}, http.StatusOK)
	if current, revision := settingsEnvelopeCurrent(t, permissionB); current != "allow" || revision != 0 {
		t.Fatalf("home B changed: current=%q revision=%d", current, revision)
	}

	created := postPluginGlue(t, clientA, serverA.base, "session.create", map[string]any{}, http.StatusOK)
	sessionID := created["result"].(map[string]any)["sessionId"].(string)
	loaded := postPluginGlue(t, clientA, serverA.base, "session.load", map[string]any{"id": sessionID, "tailMessages": 1}, http.StatusOK)
	if got := loaded["result"].(map[string]any)["permission"]; got != "deny" {
		t.Fatalf("new session runtime permission=%v, want deny", got)
	}
	modelRaw, err := os.ReadFile(filepath.Join(homeA, "state", "session-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	var modelValues map[string]string
	if err := json.Unmarshal(modelRaw, &modelValues); err != nil {
		t.Fatal(err)
	}
	if modelValues[sessionID] != "settings-model-A" {
		t.Fatalf("new session model storage=%q", modelValues[sessionID])
	}
	t.Logf("REAL_NEW_SESSION id=%s permission=deny model=settings-model-A", sessionID)

	serverA.stop()
	serverA = startPluginGlueServer(t, exe, homeA, decoy)
	clientA = connectPluginGlueServer(t, serverA)
	afterRestart := postPluginGlue(t, clientA, serverA.base, "settings.form.get", map[string]any{"ns": "model"}, http.StatusOK)
	if current, revision := settingsEnvelopeCurrent(t, afterRestart); current != "settings-model-A" || revision != 3 {
		t.Fatalf("restart model current=%q revision=%d", current, revision)
	}
	logSettingsEvidence(t, "REAL_AFTER_RESTART", afterRestart)

	resetPermission := postPluginGlue(t, clientA, serverA.base, "settings.form.reset", map[string]any{"ns": "permission", "expectedRevision": 3}, http.StatusOK)
	resetModel := postPluginGlue(t, clientA, serverA.base, "settings.mutate", map[string]any{
		"ns": "model", "ops": []any{map[string]any{"op": "unset", "path": []string{"defaultModel"}}}, "expectedRevision": 3,
	}, http.StatusOK)
	if current, _ := settingsEnvelopeCurrent(t, resetPermission); current != "allow" {
		t.Fatalf("reset permission=%q", current)
	}
	if current, _ := settingsEnvelopeCurrent(t, resetModel); current != "group/deepseek-v4.1-flash-1017" {
		t.Fatalf("reset model=%q", current)
	}
	logSettingsEvidence(t, "REAL_RESET_PERMISSION", resetPermission)
	logSettingsEvidence(t, "REAL_RESET_MODEL", resetModel)

	serverA.stop()
	serverA = startPluginGlueServer(t, exe, homeA, decoy)
	clientA = connectPluginGlueServer(t, serverA)
	afterResetRestart := postPluginGlue(t, clientA, serverA.base, "settings.form.get", map[string]any{"ns": "permission"}, http.StatusOK)
	if current, revision := settingsEnvelopeCurrent(t, afterResetRestart); current != "allow" || revision != 4 {
		t.Fatalf("reset restart current=%q revision=%d", current, revision)
	}
	logSettingsEvidence(t, "REAL_AFTER_RESET_RESTART", afterResetRestart)

	// ── 官方客户端 / 插件 UI 设置命名空间：真 dshd 上可读、可写、可重置 ──
	//
	// 真机截图暴露的缺口就是这 4 行：宿主没发布命名空间时设置面板显示「未知」。
	// 这里走真实 HTTP + 真实 <home>/state/settings-namespaces.json，并跨一次重启。
	uiNamespaces := []string{
		"locale", "ui-theme", "ui-chat", "ui-settings",
		// 2026-11-13 补：官方另两个 general slot 的命名空间。
		"ui-conversation", "session-log-deepseek",
		// 2026-11-14 补：官方 dsh-session-telemetry-otel 的开关。
		"session-telemetry-otel",
		// 2026-11-15 补：官方 agent-preset registry 的 selectedDefault。
		"agent-preset-registry",
	}
	for _, ns := range uiNamespaces {
		section := f2Section(t, result, ns)
		if section["writable"] != true {
			t.Fatalf("%s 的 section 必须可写：%v", ns, section)
		}
		fields, ok := section["fields"].([]any)
		if !ok || len(fields) == 0 {
			t.Fatalf("%s 的 section 必须带字段投影（界面按 current 画值）：%v", ns, section)
		}
	}
	uiThemeSet := postPluginGlue(t, clientA, serverA.base, "settings.form.set", map[string]any{
		"ns": "ui-theme", "path": []string{"fontSize"}, "value": 18, "expectedRevision": 0,
	}, http.StatusOK)
	if got := settingsFieldCurrent(t, uiThemeSet, "fontSize"); got != float64(18) {
		t.Fatalf("ui-theme.fontSize 写入后 current=%v，want 18", got)
	}
	logSettingsEvidence(t, "REAL_UI_THEME_SET", uiThemeSet)
	uiChatLegacy := postPluginGlue(t, clientA, serverA.base, "settings.form.set", map[string]any{
		"ns": "ui-chat", "path": []string{"transcriptView"}, "value": "normal", "expectedRevision": 0,
	}, http.StatusOK)
	if got := settingsFieldCurrent(t, uiChatLegacy, "transcriptView"); got != "detailed" {
		t.Fatalf("ui-chat 的旧值 normal 必须读作 detailed，实际 %v", got)
	}
	logSettingsEvidence(t, "REAL_UI_CHAT_LEGACY", uiChatLegacy)

	serverA.stop()
	serverA = startPluginGlueServer(t, exe, homeA, decoy)
	clientA = connectPluginGlueServer(t, serverA)
	uiThemeReload := postPluginGlue(t, clientA, serverA.base, "settings.form.get", map[string]any{"ns": "ui-theme"}, http.StatusOK)
	if got := settingsFieldCurrent(t, uiThemeReload, "fontSize"); got != float64(18) {
		t.Fatalf("重启后 ui-theme.fontSize 必须仍是 18，实际 %v", got)
	}
	logSettingsEvidence(t, "REAL_UI_THEME_AFTER_RESTART", uiThemeReload)

	outOfRange := postPluginGlue(t, clientA, serverA.base, "settings.form.set", map[string]any{
		"ns": "ui-theme", "path": []string{"fontSize"}, "value": 23, "expectedRevision": 1,
	}, http.StatusBadRequest)
	assertPluginGlueError(t, outOfRange, "out_of_range")
	logSettingsEvidence(t, "REAL_UI_THEME_OUT_OF_RANGE", outOfRange)

	uiThemeReset := postPluginGlue(t, clientA, serverA.base, "settings.form.reset", map[string]any{
		"ns": "ui-theme", "expectedRevision": 1,
	}, http.StatusOK)
	if got := settingsFieldCurrent(t, uiThemeReset, "fontSize"); got != float64(14) {
		t.Fatalf("reset 后 ui-theme.fontSize 必须回到官方默认 14，实际 %v", got)
	}
	logSettingsEvidence(t, "REAL_UI_THEME_RESET", uiThemeReset)

	// A real dshd modelRunner must put the persisted new-session default on the
	// actual OpenAI-compatible request. The endpoint is local and answers one
	// ordinary SSE assistant message; no external model or GUI is involved.
	seenModel := make(chan string, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		seenModel <- request.Model
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(gateway.Close)
	t.Setenv("DSHD_SERVE_MODEL_RUNNER", "1")
	t.Setenv("DSHD_LLM_BASE", gateway.URL+"/v1")
	t.Setenv("DSHD_LLM_KEY", "")
	homeModel := filepath.Join(t.TempDir(), "home-model-runtime")
	modelServer := startPluginGlueServer(t, exe, homeModel, decoy)
	modelClient := connectPluginGlueServer(t, modelServer)
	postPluginGlue(t, modelClient, modelServer.base, "settings.form.set", map[string]any{
		"ns": "model", "path": []string{"defaultModel"}, "value": "runtime-settings-model", "expectedRevision": 0,
	}, http.StatusOK)
	created = postPluginGlue(t, modelClient, modelServer.base, "session.create", map[string]any{}, http.StatusOK)
	runtimeSession := created["result"].(map[string]any)["sessionId"].(string)
	postPluginGlue(t, modelClient, modelServer.base, "session.prompt", map[string]any{"id": runtimeSession, "text": "prove model"}, http.StatusOK)
	select {
	case got := <-seenModel:
		if got != "runtime-settings-model" {
			t.Fatalf("runtime request model=%q", got)
		}
		t.Logf("REAL_RUNTIME_MODEL session=%s model=%s", runtimeSession, got)
	case <-time.After(realChainWait):
		t.Fatalf("model request not received; serve stderr=%s", modelServer.stderr.String())
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		history := postPluginGlue(t, modelClient, modelServer.base, "session.history", map[string]any{"id": runtimeSession, "sinceSeq": 0, "limit": 100}, http.StatusOK)
		frames := history["result"].(map[string]any)["frames"].([]any)
		done := false
		for _, raw := range frames {
			frame := raw.(map[string]any)
			if frame["type"] == "turn/error" {
				t.Fatalf("runtime model turn failed: %+v", frame)
			}
			done = done || frame["type"] == "turn/done"
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn/done not received; frames=%+v stderr=%s", frames, modelServer.stderr.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Log("REAL_RUNTIME_TURN_DONE OK=1 FAIL=0")
	t.Log("SETTINGS_FORM_REAL_SUMMARY OK=1 FAIL=0")
}

// postTimeoutForSettings 是这些真进程用例的 HTTP 客户端超时。
//
// 60s 而不是 15s：这些请求打在**真 dshd 子进程**上（起进程 + 建 home + 绑端口 +
// 走一遍 settings 写入 + 落盘 + revision 乐观锁）。CI runner 只有 2 核，还套着
// `-race`（二进制更慢），15s 在负载高时不够——2026-11-13 已两次红在
// `TestSettingsFormRealDSHD…`（PR #17 round 4、PR #19）。
//
// **超时是环境容差，不是行为契约**：放宽它不降低任何断言强度，
// 等不到照样 fail-closed。
const postTimeoutForSettings = 60 * time.Second

// ---------------------------------------------------------------------------
// F2 · `llm` 命名空间写入面（rc.2 已知缺口 #2）
// ---------------------------------------------------------------------------
//
// D4 只发布了 `providers.<route>.retryPolicy` 的 schema，宿主设置存储没有 llm
// 的读写分支，所以 describe 里 `sections.llm.writable=false`、写回结构化
// `read_only`。官方 UI 这一面是**可写**的（重试条件/次数/等待时间都能改），
// 缺口 #2 就是补这条写入面：能改、能读回、下一轮真按新策略重试。

// f2Post 发一次 L1 settings Transport 请求，返回 HTTP 状态与原始响应体。
func f2Post(t *testing.T, client *http.Client, base, method string, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("%s 序列化参数: %v", method, err)
	}
	response, err := client.Post(base+"/api/"+method, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s POST: %v", method, err)
	}
	defer response.Body.Close()
	var payload bytes.Buffer
	if _, err := payload.ReadFrom(response.Body); err != nil {
		t.Fatalf("%s 读响应: %v", method, err)
	}
	return response.StatusCode, payload.Bytes()
}

func f2Result(t *testing.T, label string, status int, payload []byte, want int) map[string]any {
	t.Helper()
	if status != want {
		t.Fatalf("%s 应 %d，实际 %d：%s", label, want, status, payload)
	}
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("%s 响应不是 JSON: %v 原文=%s", label, err, payload)
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s 响应缺 result 对象：%s", label, payload)
	}
	return result
}

// f2Call 发一次设置请求并按期望状态码取 result 对象。
func f2Call(t *testing.T, client *http.Client, base, method string, body any, want int) map[string]any {
	t.Helper()
	status, payload := f2Post(t, client, base, method, body)
	return f2Result(t, method, status, payload, want)
}

// f2AssertSettingsError 钉住 Transport 四字段错误（code/message/hint/retryable）
// 与"Hint 必须能告诉客户端怎么改"。
func f2AssertSettingsError(t *testing.T, label string, status int, payload []byte, wantStatus int, wantCode, wantInMessage string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("%s 应 %d，实际 %d：%s", label, wantStatus, status, payload)
	}
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("%s 响应不是 JSON: %v 原文=%s", label, err, payload)
	}
	rawError, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("%s 响应缺 error 对象：%s", label, payload)
	}
	for _, key := range []string{"code", "message", "hint", "retryable"} {
		if _, present := rawError[key]; !present {
			t.Fatalf("%s 错误缺四字段之一 %q：%s", label, key, payload)
		}
	}
	if rawError["code"] != wantCode {
		t.Fatalf("%s code = %v，期望 %s：%s", label, rawError["code"], wantCode, payload)
	}
	message, _ := rawError["message"].(string)
	if wantInMessage != "" && !strings.Contains(message, wantInMessage) {
		t.Fatalf("%s 的 message 应提到 %q，实际 %q", label, wantInMessage, message)
	}
	if hint, _ := rawError["hint"].(string); strings.TrimSpace(hint) == "" {
		t.Fatalf("%s 的 Hint 不能为空（要告诉客户端怎么改）：%s", label, payload)
	}
}

// f2LLMSection 从 describe 结果里取 llm 的 section。
// f2Section 按 ns 取 describe 的 sections 一项。
func f2Section(t *testing.T, described map[string]any, ns string) map[string]any {
	t.Helper()
	sections, ok := described["sections"].([]any)
	if !ok {
		t.Fatalf("describe 缺 sections：%v", described)
	}
	for _, raw := range sections {
		section, ok := raw.(map[string]any)
		if ok && section["ns"] == ns {
			return section
		}
	}
	t.Fatalf("describe 没有 %s section：%v", ns, sections)
	return nil
}

// settingsFieldCurrent 取一次 settings.form.get/set/reset 结果里某个字段的 current。
func settingsFieldCurrent(t *testing.T, envelope map[string]any, name string) any {
	t.Helper()
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("envelope 缺 result：%v", envelope)
	}
	fields, ok := result["fields"].([]any)
	if !ok {
		t.Fatalf("result 缺 fields：%v", result)
	}
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if ok && field["name"] == name {
			return field["current"]
		}
	}
	t.Fatalf("fields 里没有 %q：%v", name, fields)
	return nil
}

func f2LLMSection(t *testing.T, described map[string]any) map[string]any {
	t.Helper()
	sections, ok := described["sections"].([]any)
	if !ok {
		t.Fatalf("describe 缺 sections：%v", described)
	}
	for _, raw := range sections {
		section, ok := raw.(map[string]any)
		if ok && section["ns"] == "llm" {
			return section
		}
	}
	t.Fatalf("describe 没有 llm section：%v", sections)
	return nil
}

// f2LLMNamespace 从 describe 结果里取 llm 的 namespace 视图。
func f2LLMNamespace(t *testing.T, described map[string]any) map[string]any {
	t.Helper()
	namespaces, ok := described["namespaces"].([]any)
	if !ok {
		t.Fatalf("describe 缺 namespaces：%v", described)
	}
	for _, raw := range namespaces {
		namespace, ok := raw.(map[string]any)
		if ok && namespace["ns"] == "llm" {
			return namespace
		}
	}
	t.Fatalf("describe 没有 llm namespace：%v", namespaces)
	return nil
}

// f2StoredRetryPolicy 从 describe 的 llm.value 里取 `providers.<route>.retryPolicy`。
func f2StoredRetryPolicy(t *testing.T, described map[string]any, route string) map[string]any {
	t.Helper()
	namespace := f2LLMNamespace(t, described)
	value, ok := namespace["value"].(map[string]any)
	if !ok {
		t.Fatalf("llm.value 不是对象：%v", namespace["value"])
	}
	providers, ok := value["providers"].(map[string]any)
	if !ok {
		t.Fatalf("llm.value.providers 不是对象：%v", value)
	}
	profile, ok := providers[route].(map[string]any)
	if !ok {
		t.Fatalf("llm.value.providers.%s 不是对象：%v", route, providers)
	}
	policy, ok := profile["retryPolicy"].(map[string]any)
	if !ok {
		t.Fatalf("providers.%s 没有 retryPolicy：%v", route, profile)
	}
	return policy
}

// f2ProviderRetryPolicyValue 拼一份合法的 retryPolicy 值。
func f2ProviderRetryPolicyValue(maxRetries, initialDelayMs, maxDelayMs int, codes []any) map[string]any {
	return map[string]any{
		"mode":           "normal",
		"maxRetries":     maxRetries,
		"retryableCodes": codes,
		"backoff": map[string]any{
			"initialDelayMs": initialDelayMs,
			"maxDelayMs":     maxDelayMs,
			"jitterRatio":    0,
		},
	}
}

// TestSettingsLLMNamespaceWritableRoundTripAndValidation 是缺口 #2 的读写闭环：
//   - describe 的 `sections.llm.writable` 必须是 true（D4 阶段是 false）；
//   - `settings.form.set` 能改 `providers.<route>.retryPolicy`，重开存储后读回同一份值；
//   - 非法值（负/零延迟、空 code、未知键、越界 maxRetries、非法 mode、重复 code）
//     → 四字段错误 + Hint，且 revision 与已存值都不变（不静默截断、不写半份）；
//   - `expectedRevision` 冲突语义与 model/permission 一致（409 revision_conflict）；
//   - 多段 path 的 unset 与"reset 不带 path = 整段清空"都真的落盘。
func TestSettingsLLMNamespaceWritableRoundTripAndValidation(t *testing.T) {
	home := t.TempDir()
	store, _, _ := openSettingsStores(t, home, "base-model", "allow")
	server := httptest.NewServer(newSettingsFormHTTPHandler(store))
	defer server.Close()
	client := server.Client()

	status, payload := f2Post(t, client, server.URL, "settings.describe", map[string]any{})
	described := f2Result(t, "settings.describe", status, payload, http.StatusOK)
	section := f2LLMSection(t, described)
	if section["writable"] != true {
		t.Fatalf("llm 命名空间必须可写（rc.2 缺口 #2），实际 writable=%v：%v", section["writable"], section)
	}
	if section["revision"] != float64(0) {
		t.Fatalf("全新 home 的 llm revision 应为 0，实际 %v", section["revision"])
	}
	value := f2LLMNamespace(t, described)["value"].(map[string]any)
	if providers, ok := value["providers"].(map[string]any); !ok || len(providers) != 0 {
		t.Fatalf("全新 home 的 llm.value 应是空的 providers 字典：%v", value)
	}

	route := "openai-completions"
	policy := f2ProviderRetryPolicyValue(1, 1, 5, []any{"SERVER"})
	set := f2Call(t, client, server.URL, "settings.form.set", map[string]any{
		"ns": "llm", "path": []string{"providers", route, "retryPolicy"},
		"value": policy, "expectedRevision": float64(0),
	}, http.StatusOK)
	if set["revision"] != float64(1) {
		t.Fatalf("第一次写入后 revision 应为 1，实际 %v", set["revision"])
	}

	// 重开存储（等价于 dshd 重启）：同一条值必须还在，revision 不能归零。
	reopened, _, _ := openSettingsStores(t, home, "base-model", "allow")
	reopenedServer := httptest.NewServer(newSettingsFormHTTPHandler(reopened))
	defer reopenedServer.Close()
	status, payload = f2Post(t, reopenedServer.Client(), reopenedServer.URL, "settings.describe", map[string]any{})
	reloaded := f2Result(t, "settings.describe(reopen)", status, payload, http.StatusOK)
	if stored := f2StoredRetryPolicy(t, reloaded, route); stored["maxRetries"] != float64(1) || stored["mode"] != "normal" {
		t.Fatalf("重开后读回的值不对：%v", stored)
	}
	if got := f2LLMSection(t, reloaded)["revision"]; got != float64(1) {
		t.Fatalf("重开后 revision 应为 1，实际 %v", got)
	}
	if got := f2LLMSection(t, reloaded)["writable"]; got != true {
		t.Fatalf("重开后 llm 仍必须可写，实际 %v", got)
	}

	// 非法值矩阵：每一条都必须整次拒绝（四字段错误 + Hint），revision 停在 1。
	invalid := []struct {
		name          string
		policy        map[string]any
		wantCode      string
		wantInMessage string
	}{
		{"负延迟", f2ProviderRetryPolicyValue(1, -1, 5, []any{"SERVER"}), "out_of_range", "initialDelayMs"},
		{"零延迟", f2ProviderRetryPolicyValue(1, 0, 5, []any{"SERVER"}), "out_of_range", "initialDelayMs"},
		{"空 code 数组", f2ProviderRetryPolicyValue(1, 1, 5, []any{}), "out_of_range", "retryableCodes"},
		{"未知键", map[string]any{"mode": "normal", "retryAttempts": 1}, "unknown_field", "retryAttempts"},
		{"maxRetries 越界", f2ProviderRetryPolicyValue(9007199254740992, 1, 5, []any{"SERVER"}), "out_of_range", "maxRetries"},
		{"initialDelayMs > maxDelayMs", f2ProviderRetryPolicyValue(1, 100, 10, []any{"SERVER"}), "out_of_range", "maxDelayMs"},
		{"mode 非法", map[string]any{"mode": "sometimes"}, "invalid_enum", "sometimes"},
		{"重复 code", f2ProviderRetryPolicyValue(1, 1, 5, []any{"SERVER", "SERVER"}), "invalid_value", "重复"},
	}
	for _, testCase := range invalid {
		status, payload = f2Post(t, client, server.URL, "settings.form.set", map[string]any{
			"ns": "llm", "path": []string{"providers", route, "retryPolicy"},
			"value": testCase.policy, "expectedRevision": float64(1),
		})
		f2AssertSettingsError(t, "非法策略 "+testCase.name, status, payload, http.StatusBadRequest, testCase.wantCode, testCase.wantInMessage)
	}
	status, payload = f2Post(t, client, server.URL, "settings.describe", map[string]any{})
	afterRejected := f2Result(t, "settings.describe(after rejects)", status, payload, http.StatusOK)
	if got := f2LLMSection(t, afterRejected)["revision"]; got != float64(1) {
		t.Fatalf("被拒的写入不许改动 revision，实际 %v", got)
	}
	if got := f2StoredRetryPolicy(t, afterRejected, route)["maxRetries"]; got != float64(1) {
		t.Fatalf("被拒的写入不许改动已存的值，实际 maxRetries=%v", got)
	}

	// revision 冲突语义与其它命名空间一致。
	status, payload = f2Post(t, client, server.URL, "settings.form.set", map[string]any{
		"ns": "llm", "path": []string{"providers", route, "retryPolicy"},
		"value": f2ProviderRetryPolicyValue(2, 1, 5, []any{"SERVER"}), "expectedRevision": float64(0),
	})
	f2AssertSettingsError(t, "过期 revision", status, payload, http.StatusConflict, "revision_conflict", "expected=0")

	// 多段 path 的 unset（settings.mutate）与整命名空间 reset。
	mutated := f2Call(t, client, server.URL, "settings.mutate", map[string]any{
		"ns": "llm", "expectedRevision": float64(1),
		"ops": []any{map[string]any{"op": "unset", "path": []string{"providers", route, "retryPolicy"}}},
	}, http.StatusOK)
	if mutated["revision"] != float64(2) {
		t.Fatalf("unset 后 revision 应为 2，实际 %v", mutated["revision"])
	}
	status, payload = f2Post(t, client, server.URL, "settings.describe", map[string]any{})
	unset := f2Result(t, "settings.describe(unset)", status, payload, http.StatusOK)
	unsetProviders := f2LLMNamespace(t, unset)["value"].(map[string]any)["providers"].(map[string]any)
	if profile, ok := unsetProviders[route].(map[string]any); !ok || len(profile) != 0 {
		t.Fatalf("unset 之后该路由下不应再有 retryPolicy：%v", unsetProviders)
	}

	reset := f2Call(t, client, server.URL, "settings.form.reset", map[string]any{
		"ns": "llm", "expectedRevision": float64(2),
	}, http.StatusOK)
	if reset["revision"] != float64(3) {
		t.Fatalf("整命名空间 reset 后 revision 应为 3，实际 %v", reset["revision"])
	}
	status, payload = f2Post(t, client, server.URL, "settings.describe", map[string]any{})
	afterReset := f2Result(t, "settings.describe(reset)", status, payload, http.StatusOK)
	resetValue := f2LLMNamespace(t, afterReset)["value"].(map[string]any)
	if providers, ok := resetValue["providers"].(map[string]any); !ok || len(providers) != 0 {
		t.Fatalf(`reset 不带 path 必须清空 user 段（回落到声明默认 {"providers":{}}），实际 %v`, resetValue)
	}
	if got := f2LLMSection(t, afterReset)["revision"]; got != float64(3) {
		t.Fatalf("reset 后 describe 的 revision 应为 3，实际 %v", got)
	}
}

// TestSettingsLLMRetryPolicyChangesRealAttempts 是缺口 #2 的端到端：
// 经真实设置 Transport 改 `providers.<route>.retryPolicy.maxRetries` → 读回 →
// 用**计数假网关**（本机 httptest，一个字节都不出网）验证客户端真发的请求
// 次数随设置变化（2 次 → 4 次），即"下一轮真生效"。
func TestSettingsLLMRetryPolicyChangesRealAttempts(t *testing.T) {
	home := t.TempDir()
	store, _, _ := openSettingsStores(t, home, "", "allow")
	server := httptest.NewServer(newSettingsFormHTTPHandler(store))
	defer server.Close()
	client := server.Client()

	var attempts int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
	}))
	defer gateway.Close()

	route := llm.ProviderAPIOpenAICompletions
	model := fmt.Sprintf(`{"id":"retry-e2e","api":%q,"baseURL":%q}`, route, gateway.URL)
	initialRevision := int64(0)
	if _, err := llm.OpenModelCatalog(home).Upsert(json.RawMessage(model), nil, &initialRevision); err != nil {
		t.Fatalf("upsert 模型: %v", err)
	}

	writePolicy := func(revision int64, maxRetries int) {
		t.Helper()
		payload := map[string]any{
			"ns": "llm", "path": []string{"providers", route, "retryPolicy"},
			"value": f2ProviderRetryPolicyValue(maxRetries, 1, 5, []any{"SERVER"}),
		}
		if revision > 0 {
			payload["expectedRevision"] = revision
		}
		f2Call(t, client, server.URL, "settings.form.set", payload, http.StatusOK)
	}
	runAttempts := func(maxRetries int) int {
		t.Helper()
		provider, err := llm.OpenModelCatalog(home).ResolveProvider("retry-e2e")
		if err != nil {
			t.Fatalf("ResolveProvider: %v", err)
		}
		if provider.Retry == nil || provider.Retry.MaxRetries != maxRetries {
			t.Fatalf("设置里的 maxRetries=%d 没进 provider 解析（缺口 #2 未生效）：%+v", maxRetries, provider.Retry)
		}
		raw, err := provider.RetryPolicyJSON()
		if err != nil || !strings.Contains(raw, fmt.Sprintf(`"maxRetries":%d`, maxRetries)) {
			t.Fatalf("传给 dshd run 子进程的策略 JSON = %q err=%v", raw, err)
		}
		atomic.StoreInt32(&attempts, 0)
		if _, err := provider.Client().Stream(context.Background(), llm.Request{Model: "retry-e2e"}, nil); err == nil {
			t.Fatal("假网关恒回 502，这一轮必须失败")
		}
		return int(atomic.LoadInt32(&attempts))
	}

	writePolicy(0, 1)
	if got := runAttempts(1); got != 2 {
		t.Fatalf("maxRetries=1 → 首发 + 1 次重试 = 2 次请求，实际 %d 次", got)
	}
	writePolicy(1, 3)
	if got := runAttempts(3); got != 4 {
		t.Fatalf("maxRetries=3 → 首发 + 3 次重试 = 4 次请求，实际 %d 次", got)
	}
}
