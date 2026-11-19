package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/llm"
)

type modelCatalogGolden struct {
	Schema   string `json:"schema"`
	Accepted []struct {
		Name           string         `json:"name"`
		Method         string         `json:"method"`
		Params         map[string]any `json:"params"`
		Status         int            `json:"status"`
		Revision       int64          `json:"revision"`
		Order          []string       `json:"order"`
		TestOK         bool           `json:"testOK"`
		TestModel      string         `json:"testModel"`
		ActiveModel    string         `json:"activeModel"`
		ActiveRevision int64          `json:"activeRevision"`
	} `json:"accepted"`
	Rejected []struct {
		Name   string         `json:"name"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
		Status int            `json:"status"`
		Error  string         `json:"error"`
	} `json:"rejected"`
}

func readModelCatalogGolden(t *testing.T) modelCatalogGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "transport", "testdata", "model-catalog-golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden modelCatalogGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if golden.Schema != "dshgo.model-catalog.golden@1" || len(golden.Accepted) != 7 || len(golden.Rejected) < 17 {
		t.Fatalf("bad model catalog golden: schema=%q accepted=%d rejected=%d", golden.Schema, len(golden.Accepted), len(golden.Rejected))
	}
	return golden
}

func expandModelCatalogValue(value any, baseURL string) any {
	switch typed := value.(type) {
	case string:
		if typed == "{{BASE_URL}}" {
			return baseURL
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = expandModelCatalogValue(typed[i], baseURL)
		}
	case map[string]any:
		for key := range typed {
			typed[key] = expandModelCatalogValue(typed[key], baseURL)
		}
	}
	return value
}

func modelCatalogResult(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %+v", envelope)
	}
	return result
}

func assertModelCatalogOrder(t *testing.T, result map[string]any, want []string) {
	t.Helper()
	rawModels, ok := result["models"].([]any)
	if !ok || len(rawModels) != len(want) {
		t.Fatalf("models=%v want order=%v", result["models"], want)
	}
	for i, id := range want {
		model := rawModels[i].(map[string]any)
		if model["id"] != id {
			t.Fatalf("model[%d].id=%v want=%s", i, model["id"], id)
		}
	}
}

// modelCatalogEvidence 把每条证据留到测试收尾时统一输出。
//
// 为什么不在拿到响应时立刻 t.Logf：Go 在非 -v 模式下会把测试体里的日志
// 按调用顺序全部倾倒到 `--- FAIL` 之后，而 scripts/check.sh 只显示其中
// 12 行（历史上 grep -A 12）。本用例正常有 20+ 条证据，真正的失败消息会被顶出
// 窗口，CI 上看起来就像"没有任何断言行"。改在 t.Cleanup 里输出后，
// 失败消息固定成为 `--- FAIL` 之后的第 1 行，证据排在它后面。
type modelCatalogEvidence struct {
	entries []string
}

func (e *modelCatalogEvidence) attach(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, entry := range e.entries {
			t.Logf("%s", entry)
		}
	})
}

func (e *modelCatalogEvidence) log(t *testing.T, label string, envelope map[string]any) {
	t.Helper()
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("%s evidence marshal: %v", label, err)
	}
	if strings.Contains(string(raw), "catalog-test-secret") || strings.Contains(string(raw), "wrong-secret") {
		t.Fatalf("credential echoed in %s: %s", label, raw)
	}
	e.entries = append(e.entries, label+" "+string(raw))
}

// logModelCatalogEvidence 是其它用例仍在使用的即时证据输出；本文件新增的
// modelCatalogEvidence 只服务于上面那条对 CI 日志窗口敏感的真进程用例。
func logModelCatalogEvidence(t *testing.T, label string, envelope map[string]any) {
	t.Helper()
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "catalog-test-secret") || strings.Contains(string(raw), "wrong-secret") {
		t.Fatalf("credential echoed in %s: %s", label, raw)
	}
	t.Logf("%s %s", label, raw)
}

// modelCatalogOutcomes 按 journal 顺序取结局帧（turn/done 与 turn/error）。
//
// 会话日志是 append-only，所以"第 N 个结局帧之后的结局帧"就是"第 N+1 次 prompt 的结局"。
func modelCatalogOutcomes(frames []any) []map[string]any {
	out := make([]map[string]any, 0, 4)
	for _, raw := range frames {
		frame, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch frame["type"] {
		case "turn/done", "turn/error":
			out = append(out, frame)
		}
	}
	return out
}

func modelCatalogHistoryFrames(t *testing.T, client *http.Client, base, sessionID string) []any {
	t.Helper()
	history := postPluginGlue(t, client, base, "session.history",
		map[string]any{"id": sessionID, "sinceSeq": 0, "limit": 1000}, http.StatusOK)
	frames, ok := modelCatalogResult(t, history)["frames"].([]any)
	if !ok {
		t.Fatalf("session.history 没有 frames: %+v", history)
	}
	return frames
}

// modelCatalogOutcomeBaseline 返回"现在"有几个结局帧。发 prompt **之前**取一次，
// 之后拿它当水位——这是本用例唯一能区分"这一轮收工了"和"前面某一轮早就收工了"的东西。
func modelCatalogOutcomeBaseline(t *testing.T, client *http.Client, base, sessionID string) int {
	t.Helper()
	return len(modelCatalogOutcomes(modelCatalogHistoryFrames(t, client, base, sessionID)))
}

// waitModelCatalogOutcome 等**这一次 prompt** 的结局帧落进 journal。
//
// 判据从"history 里出现任意 turn/done"改成"结局帧数越过 baseline"，原因是一个真实
// 的判据 bug（task-86，2026-11-19；与 `TestTerminalAgentOpsOverBridgeOnRealPTY` 的
// ConPTY 就绪判据同一类）：
//
//	原来 `waitModelCatalogTurn` 扫全量 history，见到**任意一条** turn/done 就 return。
//	本用例重启宿主后还会对 oldSession 再发一次 prompt——那时 history 里已经躺着两轮
//	更早的 turn/done，于是这个"等待"立刻返回：**这一轮到底有没有跑完、有没有报错，
//	用例根本没看**。慢机红不了、真错也绿不了，属于"判据比行为宽"。
//
// 现在：只有结局帧数越过 baseline 才算收工；且只检查**新出现**的结局帧里有没有
// turn/error（旧轮次的错误不该冤枉这一轮）。超时返回 error 而不是直接 Fatal，
// 这样"判据本身有没有效力"可以被下面的回归用例单独钉住。
func waitModelCatalogOutcome(t *testing.T, client *http.Client, base, sessionID string, baseline int, wait time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(wait)
	attempts := 0
	for {
		attempts++
		frames := modelCatalogHistoryFrames(t, client, base, sessionID)
		outcomes := modelCatalogOutcomes(frames)
		for _, frame := range outcomes[min(baseline, len(outcomes)):] {
			if frame["type"] == "turn/error" {
				return fmt.Errorf("turn failed: %+v", frame)
			}
		}
		if len(outcomes) > baseline {
			return nil
		}
		if time.Now().After(deadline) {
			types := make([]string, 0, len(frames))
			for _, raw := range frames {
				if frame, ok := raw.(map[string]any); ok {
					types = append(types, fmt.Sprint(frame["type"]))
				}
			}
			return fmt.Errorf("turn/done timeout after %s (%d polls) session=%s outcomes=%d want>%d frameTypes=%v frames=%+v",
				wait, attempts, sessionID, len(outcomes), baseline, types, frames)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func waitModelCatalogTurn(t *testing.T, client *http.Client, base, sessionID string, baseline int) {
	t.Helper()
	if err := waitModelCatalogOutcome(t, client, base, sessionID, baseline, realChainWait); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestModelCatalogRealDSHDAuthenticatedGoldenABRestartAndRuntime(t *testing.T) {
	evidence := &modelCatalogEvidence{}
	evidence.attach(t)
	const secret = "catalog-test-secret"
	seenModels := make(chan string, 16)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":"received %s"}`, r.Header.Get("Authorization"))
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"gateway-only","owned_by":"gateway"}]}`)
		case "/v1/chat/completions":
			var request struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if request.Stream {
				seenModels <- request.Model
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"local-response","model":%q,"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`, request.Model)
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(gateway.Close)
	t.Setenv("DSHD_SERVE_MODEL_RUNNER", "1")
	t.Setenv("DSHD_LLM_BASE", gateway.URL+"/v1")
	t.Setenv("DSHD_LLM_KEY", secret)

	exe := buildPluginGlueDSHD(t)
	decoy := filepath.Join(t.TempDir(), "decoy-home")
	homeA := filepath.Join(t.TempDir(), "home-a")
	homeB := filepath.Join(t.TempDir(), "home-b")
	serverA := startPluginGlueServer(t, exe, homeA, decoy)
	serverB := startPluginGlueServer(t, exe, homeB, decoy)
	methods := []string{"models.catalog.list", "models.catalog.upsert", "models.catalog.reorder", "models.catalog.test", "models.catalog.set-active"}
	anonymous := &http.Client{Timeout: postTimeoutForSettings}
	for _, method := range methods {
		envelope := postPluginGlue(t, anonymous, serverA.base, method, map[string]any{}, http.StatusUnauthorized)
		assertPluginGlueError(t, envelope, "unauthorized")
	}
	clientA := connectPluginGlueServer(t, serverA)
	clientB := connectPluginGlueServer(t, serverB)
	initialA := modelCatalogResult(t, postPluginGlue(t, clientA, serverA.base, "models.catalog.list", map[string]any{}, http.StatusOK))
	initialB := modelCatalogResult(t, postPluginGlue(t, clientB, serverB.base, "models.catalog.list", map[string]any{}, http.StatusOK))
	for name, result := range map[string]map[string]any{"A": initialA, "B": initialB} {
		if result["revision"] != float64(0) || len(result["models"].([]any)) != 0 {
			t.Fatalf("initial %s=%+v", name, result)
		}
		active := result["active"].(map[string]any)
		if active["id"] != "group/deepseek-v4.1-flash-1017" || active["revision"] != float64(0) {
			t.Fatalf("initial active %s=%+v", name, active)
		}
	}

	oldSession := modelCatalogResult(t, postPluginGlue(t, clientA, serverA.base, "session.create", map[string]any{}, http.StatusOK))["sessionId"].(string)
	golden := readModelCatalogGolden(t)
	for _, accepted := range golden.Accepted {
		params := expandModelCatalogValue(accepted.Params, gateway.URL+"/v1").(map[string]any)
		envelope := postPluginGlue(t, clientA, serverA.base, accepted.Method, params, accepted.Status)
		evidence.log(t, "MODEL_CATALOG_ACCEPT_"+accepted.Name, envelope)
		result := modelCatalogResult(t, envelope)
		if accepted.TestOK {
			if result["ok"] != true || result["model"] != accepted.TestModel || result["id"] != accepted.TestModel {
				t.Fatalf("test result=%+v", result)
			}
			continue
		}
		if result["revision"] != float64(accepted.Revision) {
			t.Fatalf("%s revision=%v want=%d", accepted.Name, result["revision"], accepted.Revision)
		}
		if accepted.Method == "models.catalog.set-active" {
			if result["activeModel"] != accepted.ActiveModel || result["activeRevision"] != float64(accepted.ActiveRevision) {
				t.Fatalf("set-active result=%+v", result)
			}
		} else {
			assertModelCatalogOrder(t, result, accepted.Order)
		}
	}

	for _, rejected := range golden.Rejected {
		params := expandModelCatalogValue(rejected.Params, gateway.URL+"/v1").(map[string]any)
		envelope := postPluginGlue(t, clientA, serverA.base, rejected.Method, params, rejected.Status)
		assertPluginGlueError(t, envelope, rejected.Error)
		evidence.log(t, "MODEL_CATALOG_REJECT_"+rejected.Name, envelope)
	}
	wrongCredential := postPluginGlue(t, clientA, serverA.base, "models.catalog.test", map[string]any{"id": "model-a", "credential": "wrong-secret"}, http.StatusBadGateway)
	assertPluginGlueError(t, wrongCredential, "upstream_error")
	evidence.log(t, "MODEL_CATALOG_REJECT_wrong credential", wrongCredential)
	emptyCatalog := postPluginGlue(t, clientB, serverB.base, "models.catalog.reorder", map[string]any{"ids": []string{}, "expectedRevision": 0}, http.StatusConflict)
	assertPluginGlueError(t, emptyCatalog, "empty_catalog")
	evidence.log(t, "MODEL_CATALOG_REJECT_empty catalog", emptyCatalog)

	settings := modelCatalogResult(t, postPluginGlue(t, clientA, serverA.base, "settings.form.get", map[string]any{"ns": "model"}, http.StatusOK))
	field := settings["fields"].([]any)[0].(map[string]any)
	if field["current"] != "model-a" || settings["revision"] != float64(1) {
		t.Fatalf("W61 default after set-active=%+v", settings)
	}
	afterRejected := modelCatalogResult(t, postPluginGlue(t, clientA, serverA.base, "models.catalog.list", map[string]any{}, http.StatusOK))
	if afterRejected["revision"] != float64(5) {
		t.Fatalf("rejected writes changed revision: %+v", afterRejected)
	}
	assertModelCatalogOrder(t, afterRejected, []string{"model-a", "model-c"})
	modelA := afterRejected["models"].([]any)[0].(map[string]any)
	if modelA["name"] != "Alpha Two" || modelA["futureScalar"] != float64(42) || modelA["futureField"] == nil || modelA["reasoningEfforts"] == nil || modelA["baseURL"] != gateway.URL+"/v1" {
		t.Fatalf("full fields did not round trip: %+v", modelA)
	}
	credential := afterRejected["credentials"].(map[string]any)["LOCAL_MODEL_CATALOG_KEY"].(map[string]any)
	if credential["configured"] != true || credential["writable"] != true {
		t.Fatalf("credential status=%+v", credential)
	}

	// The original canonical models.list remains the gateway projection, not a
	// silently repurposed persisted-catalog method.
	canonical := modelCatalogResult(t, postPluginGlue(t, clientA, serverA.base, "models.list", map[string]any{}, http.StatusOK))
	canonicalModels := canonical["models"].([]any)
	if len(canonicalModels) != 1 || canonicalModels[0].(map[string]any)["id"] != "gateway-only" {
		t.Fatalf("canonical models.list changed: %+v", canonical)
	}

	newSession := modelCatalogResult(t, postPluginGlue(t, clientA, serverA.base, "session.create", map[string]any{}, http.StatusOK))["sessionId"].(string)
	overridesRaw, err := os.ReadFile(filepath.Join(homeA, "state", "session-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	var overrides map[string]string
	if err := json.Unmarshal(overridesRaw, &overrides); err != nil {
		t.Fatal(err)
	}
	if overrides[oldSession] != "group/deepseek-v4.1-flash-1017" || overrides[newSession] != "model-a" {
		t.Fatalf("old/new session defaults retroactive or missing: old=%q new=%q", overrides[oldSession], overrides[newSession])
	}
	for _, expected := range []struct {
		session string
		model   string
	}{{oldSession, "group/deepseek-v4.1-flash-1017"}, {newSession, "model-a"}} {
		baseline := modelCatalogOutcomeBaseline(t, clientA, serverA.base, expected.session)
		postPluginGlue(t, clientA, serverA.base, "session.prompt", map[string]any{"id": expected.session, "text": "prove active model"}, http.StatusOK)
		select {
		case got := <-seenModels:
			if got != expected.model {
				t.Fatalf("runtime session=%s model=%q want=%q", expected.session, got, expected.model)
			}
			t.Logf("MODEL_CATALOG_RUNTIME session=%s model=%s", expected.session, got)
		case <-time.After(realChainWait):
			t.Fatalf("model request timeout session=%s stderr=%s", expected.session, serverA.stderr.String())
		}
		waitModelCatalogTurn(t, clientA, serverA.base, expected.session, baseline)
	}

	serverA.stop()
	serverA = startPluginGlueServer(t, exe, homeA, decoy)
	clientA = connectPluginGlueServer(t, serverA)
	reopened := modelCatalogResult(t, postPluginGlue(t, clientA, serverA.base, "models.catalog.list", map[string]any{}, http.StatusOK))
	if reopened["revision"] != float64(5) || reopened["active"].(map[string]any)["id"] != "model-a" || reopened["active"].(map[string]any)["revision"] != float64(1) {
		t.Fatalf("reopened catalog=%+v", reopened)
	}
	assertModelCatalogOrder(t, reopened, []string{"model-a", "model-c"})
	evidence.log(t, "MODEL_CATALOG_REOPENED", map[string]any{"result": reopened})
	reopenedSession := modelCatalogResult(t, postPluginGlue(t, clientA, serverA.base, "session.create", map[string]any{}, http.StatusOK))["sessionId"].(string)
	for _, expected := range []struct {
		session string
		model   string
	}{{oldSession, "group/deepseek-v4.1-flash-1017"}, {reopenedSession, "model-a"}} {
		baseline := modelCatalogOutcomeBaseline(t, clientA, serverA.base, expected.session)
		postPluginGlue(t, clientA, serverA.base, "session.prompt", map[string]any{"id": expected.session, "text": "prove reopened active model"}, http.StatusOK)
		select {
		case got := <-seenModels:
			if got != expected.model {
				t.Fatalf("reopened runtime session=%s model=%q want=%q", expected.session, got, expected.model)
			}
			t.Logf("MODEL_CATALOG_REOPENED_RUNTIME session=%s model=%s", expected.session, got)
		case <-time.After(realChainWait):
			t.Fatalf("reopened model request timeout session=%s stderr=%s", expected.session, serverA.stderr.String())
		}
		waitModelCatalogTurn(t, clientA, serverA.base, expected.session, baseline)
	}
	finalB := modelCatalogResult(t, postPluginGlue(t, clientB, serverB.base, "models.catalog.list", map[string]any{}, http.StatusOK))
	if finalB["revision"] != float64(0) || len(finalB["models"].([]any)) != 0 || finalB["active"].(map[string]any)["id"] != "group/deepseek-v4.1-flash-1017" {
		t.Fatalf("home B changed: %+v", finalB)
	}
	t.Log("MODEL_CATALOG_GOLDEN_SUMMARY OK=7 FAIL=0")
	t.Log("MODEL_CATALOG_REJECT_SUMMARY OK=19 FAIL=0")
	t.Log("MODEL_CATALOG_RUNTIME_SUMMARY OK=4 FAIL=0")
}

func TestModelCatalogSetActiveReachesLoopRunnerWithoutRetroactiveRewrite(t *testing.T) {
	home := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv.json")
	t.Setenv("DSHD_MODEL_WIRE_ARGV", capture)
	t.Setenv("DSHD_SERVE_NO_FOLLOW", "1")
	settings, _, permissions := openSettingsStores(t, home, "base-model", "allow")
	catalog := llm.OpenModelCatalog(home)
	httpServer := httptest.NewServer(newModelCatalogHTTPHandler(catalog, settings))
	t.Cleanup(httpServer.Close)
	post := func(method string, params any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(params)
		resp, err := http.Post(httpServer.URL+"/api/"+method, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s status=%d body=%s", method, resp.StatusCode, body)
		}
		var envelope map[string]any
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	post("models.catalog.upsert", map[string]any{"model": map[string]any{"id": "loop-active"}, "expectedRevision": 0}, http.StatusOK)
	ids := []string{"session-old", "session-new", "session-reopened"}
	create := func(string) (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	oldSession, err := createWithSettingsDefaults(create, settings)("")
	if err != nil {
		t.Fatal(err)
	}
	post("models.catalog.set-active", map[string]any{"id": "loop-active", "expectedRevision": 0}, http.StatusOK)
	newSession, err := createWithSettingsDefaults(create, settings)("")
	if err != nil {
		t.Fatal(err)
	}
	base := &loopRunner{home: home, policy: "allow", maxSteps: 6, model: "base-model"}
	runner := &modelOverrideLoopRunner{base: base, permissions: permissions}
	for _, expected := range []struct{ session, model string }{{oldSession, "base-model"}, {newSession, "loop-active"}} {
		if err := runner.Run(context.Background(), expected.session, "turn-"+expected.session, "next-prompt"); err != nil {
			t.Fatal(err)
		}
		model, _ := readLoopModelArg(t, capture)
		if model != expected.model {
			t.Fatalf("loop runner session=%s model=%q want=%q", expected.session, model, expected.model)
		}
		t.Logf("MODEL_CATALOG_LOOP_RUNTIME session=%s model=%s", expected.session, model)
	}

	reopenedSettings, _, reopenedPermissions := openSettingsStores(t, home, "base-model", "allow")
	reopenedCatalog := llm.OpenModelCatalog(home)
	view, err := reopenedCatalog.List()
	if err != nil || view.Revision != 1 {
		t.Fatalf("reopened catalog=%+v err=%v", view, err)
	}
	reopenedSession, err := createWithSettingsDefaults(create, reopenedSettings)("")
	if err != nil {
		t.Fatal(err)
	}
	reopenedRunner := &modelOverrideLoopRunner{base: base, permissions: reopenedPermissions}
	if err := reopenedRunner.Run(context.Background(), reopenedSession, "turn-reopened", "next-prompt"); err != nil {
		t.Fatal(err)
	}
	model, _ := readLoopModelArg(t, capture)
	if model != "loop-active" {
		t.Fatalf("reopened loop model=%q", model)
	}
	t.Log("MODEL_CATALOG_LOOP_SUMMARY OK=3 FAIL=0")
}

// TestModelCatalogOutcomeWaiterIgnoresStaleTurnDone 钉住 waitModelCatalogOutcome 的判据：
// **"history 里有 turn/done" 不等于"这一次 prompt 收工了"**。
//
// 这是 task-86 里那条真实判据 bug 的回归用例：旧实现在重启宿主后再 prompt 同一会话时，
// 因为 history 里已有上一轮的 turn/done 而**立刻返回**，于是这一轮跑没跑完、有没有报错
// 全都没被看见。三条断言分别对应：不许提前返回 / 收工了必须返回 / 报错了必须报出来。
func TestModelCatalogOutcomeWaiterIgnoresStaleTurnDone(t *testing.T) {
	var mu sync.Mutex
	var frames []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		snapshot := append([]any(nil), frames...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"frames": snapshot}})
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	push := func(frame map[string]any) {
		mu.Lock()
		frames = append(frames, frame)
		mu.Unlock()
	}

	// ① 只有**上一轮**的 turn/done：本轮还没收工，必须判超时（而不是当成收工）。
	push(map[string]any{"type": "turn/start", "data": map[string]any{"turn": float64(1)}})
	push(map[string]any{"type": "turn/done", "data": map[string]any{"turn": float64(1)}})
	if err := waitModelCatalogOutcome(t, client, server.URL, "sess-1", 1, 300*time.Millisecond); err == nil {
		t.Fatal("把上一轮的 turn/done 当成了这一轮收工：判据比行为宽，慢机和真错都会绿")
	}

	// ② 这一轮真的收工了 ⇒ 必须返回。
	push(map[string]any{"type": "turn/done", "data": map[string]any{"turn": float64(2)}})
	if err := waitModelCatalogOutcome(t, client, server.URL, "sess-1", 1, 2*time.Second); err != nil {
		t.Fatalf("这一轮已收工却报超时: %v", err)
	}

	// ③ 这一轮**失败**了 ⇒ 必须报 turn failed（不能被后面的帧盖掉）。
	push(map[string]any{"type": "turn/error", "data": map[string]any{"turn": float64(3), "message": "boom"}})
	err := waitModelCatalogOutcome(t, client, server.URL, "sess-1", 2, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "turn failed") {
		t.Fatalf("这一轮的 turn/error 必须报出来，实得 %v", err)
	}
}

// TestModelCatalogReplaceTransportGuards 钉住 `models.catalog.replace` 的
// Transport 面：裸参数、严格白名单、`params` 包装拒绝、可选键省略而非 null，
// 以及 revision 乐观锁与重复 id 的既有错误码。
func TestModelCatalogReplaceTransportGuards(t *testing.T) {
	home := t.TempDir()
	settings, _, _ := openSettingsStores(t, home, "base-model", "allow")
	catalog := llm.OpenModelCatalog(home)
	server := httptest.NewServer(newModelCatalogHTTPHandler(catalog, settings))
	t.Cleanup(server.Close)
	post := func(body string, want int) map[string]any {
		t.Helper()
		resp, err := http.Post(server.URL+"/api/models.catalog.replace", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s status=%d want=%d body=%s", body, resp.StatusCode, want, raw)
		}
		var envelope map[string]any
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("%s envelope: %v (%s)", body, err, raw)
		}
		return envelope
	}
	assertError := func(body string, want int, code string) {
		t.Helper()
		envelope := post(body, want)
		errField, ok := envelope["error"].(map[string]any)
		if !ok || errField["code"] != code || errField["hint"] == "" || errField["retryable"] == nil {
			t.Fatalf("%s error 四字段=%+v", body, envelope["error"])
		}
	}

	// 参数守卫：缺数组 / null / params 包装 / 未知键 / 类型错 / expectedRevision=null。
	assertError(`{}`, http.StatusBadRequest, "bad_request")
	assertError(`{"models":null}`, http.StatusBadRequest, "bad_request")
	assertError(`{"params":{"models":[]}}`, http.StatusBadRequest, "bad_request")
	assertError(`{"models":[],"extra":true}`, http.StatusBadRequest, "bad_request")
	assertError(`{"models":"not-an-array"}`, http.StatusBadRequest, "bad_request")
	assertError(`{"models":[{"id":"model-a"}],"expectedRevision":null}`, http.StatusBadRequest, "bad_request")
	// 被拒的写入不许留下任何目录或 revision。
	if view, err := catalog.List(); err != nil || view.Revision != 0 || len(view.Models) != 0 {
		t.Fatalf("被拒参数写了目录: %+v err=%v", view, err)
	}

	// 成功：整数组写入（含未知字段与 apiKeyEnv 引用）。
	envelope := post(`{"models":[{"id":"model-a","name":"Alpha","futureField":{"z":1}},{"id":"model-b"}],"expectedRevision":0}`, http.StatusOK)
	result := modelCatalogResult(t, envelope)
	if result["revision"] != float64(1) {
		t.Fatalf("revision=%v，期望 1", result["revision"])
	}
	assertModelCatalogOrder(t, result, []string{"model-a", "model-b"})
	if _, ok := result["active"].(map[string]any); !ok {
		t.Fatalf("replace 的结果不是完整 CatalogView: %+v", result)
	}

	// 陈旧 revision：409 且不覆盖。
	assertError(`{"models":[{"id":"model-z"}],"expectedRevision":0}`, http.StatusConflict, "revision_conflict")
	afterConflict := modelCatalogResult(t, post(`{"models":[{"id":"model-a"},{"id":"model-b"}],"expectedRevision":1}`, http.StatusOK))
	if afterConflict["revision"] != float64(2) {
		t.Fatalf("重新提交后 revision=%v，期望 2", afterConflict["revision"])
	}
	assertModelCatalogOrder(t, afterConflict, []string{"model-a", "model-b"})

	// 重复 id 与坏行：既有错误码，且都在写入前失败。
	assertError(`{"models":[{"id":"model-a"},{"id":"model-a"}],"expectedRevision":2}`, http.StatusConflict, "duplicate_id")
	assertError(`{"models":[{"id":""}],"expectedRevision":2}`, http.StatusBadRequest, "invalid_id")
	assertError(`{"models":[{"id":"model-a","apiKey":"not-returned"}],"expectedRevision":2}`, http.StatusBadRequest, "invalid_field")

	// models=[] 是合法目标：删到空 + revision 推进；空目录仍可 list。
	cleared := modelCatalogResult(t, post(`{"models":[],"expectedRevision":2}`, http.StatusOK))
	if cleared["revision"] != float64(3) || len(cleared["models"].([]any)) != 0 {
		t.Fatalf("清空后 view=%+v", cleared)
	}
}
