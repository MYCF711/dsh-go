package transport

// GO.2 T04：session 残余（projections 宿主键 / selectModel provider /
// 无路由方法 / control）与 attachWait 流面的行为测试。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeProjectionSource 在 fakeOfficialSource 之上实现 T04 的三个宿主能力接口。
type fakeProjectionSource struct {
	*fakeOfficialSource

	lastUsed, next *OfficialModelSelection
	preset         *string
	subagents      []OfficialSubagentCatalogEntry
	sessionIDs     []string

	catalog    OfficialModelCatalog
	catalogErr error

	resolveFn func(sessionID, provider, model, effort string) (OfficialModelSelection, error)
	resolved  []string
	cleared   []string

	initChanged bool
	initErr     error
}

func (f *fakeProjectionSource) ModelSelection(string) (*OfficialModelSelection, *OfficialModelSelection, error) {
	return f.lastUsed, f.next, nil
}

func (f *fakeProjectionSource) AgentPreset(string) (*string, error) { return f.preset, nil }

func (f *fakeProjectionSource) SubagentCatalog(string) ([]OfficialSubagentCatalogEntry, error) {
	return f.subagents, nil
}

func (f *fakeProjectionSource) SessionIDs() ([]string, error) { return f.sessionIDs, nil }

func (f *fakeProjectionSource) ModelCatalog() (OfficialModelCatalog, error) {
	return f.catalog, f.catalogErr
}

func (f *fakeProjectionSource) InitializeDefaultModel() (bool, error) {
	return f.initChanged, f.initErr
}

func (f *fakeProjectionSource) ResolveModelSelection(sessionID, provider, model, effort string) (OfficialModelSelection, error) {
	f.resolved = append(f.resolved, provider+"|"+model+"|"+effort)
	if f.resolveFn != nil {
		return f.resolveFn(sessionID, provider, model, effort)
	}
	return OfficialModelSelection{Provider: provider, Model: model, ReasoningEffort: effort}, nil
}

func (f *fakeProjectionSource) ClearModelSelection(sessionID string) error {
	f.cleared = append(f.cleared, sessionID)
	return nil
}

func newProjectionSource() *fakeProjectionSource {
	return &fakeProjectionSource{
		fakeOfficialSource: &fakeOfficialSource{events: officialFixtureEvents(), logEnd: 6},
	}
}

// TestOfficialSessionProjectionsHostExtras：宿主实现了能力接口时，那三个键必须
// 出现且是宿主给的真值（不是编的）。
func TestOfficialSessionProjectionsHostExtras(t *testing.T) {
	preset := "review"
	src := newProjectionSource()
	src.lastUsed = &OfficialModelSelection{Provider: "local", Model: "model-a", ReasoningEffort: "low"}
	src.next = &OfficialModelSelection{Provider: "gateway", Model: "deepseek-chat"}
	src.preset = &preset
	src.subagents = []OfficialSubagentCatalogEntry{{ID: "kid-1", CreatedAt: 42, Mode: "continuable"}}
	s := officialServerWith(t, src.fakeOfficialSource, Options{})
	// 把 Source 换成带宿主能力的包装（替换 fakeOfficialSource 会丢 OfficialSessionHost）。
	s.opt.Source = src

	rec := postOfficial(t, s.handleSessionProjections, `{"sessionId":"s1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	values := unwrapResult(t, rec.Body.Bytes())["values"].(map[string]any)
	selection := values["modelSelection"].(map[string]any)
	used := selection["lastUsed"].(map[string]any)
	if used["provider"] != "local" || used["model"] != "model-a" || used["reasoningEffort"] != "low" {
		t.Fatalf("lastUsed 必须是宿主给的真值：%v", selection)
	}
	next := selection["next"].(map[string]any)
	if next["provider"] != "gateway" || next["model"] != "deepseek-chat" {
		t.Fatalf("next 必须是宿主给的真值：%v", selection)
	}
	if values["agentPreset"] != "review" {
		t.Fatalf("agentPreset 必须是宿主真值：%v", values["agentPreset"])
	}
	catalog := values["subagentCatalog"].([]any)
	if len(catalog) != 1 || catalog[0].(map[string]any)["mode"] != "continuable" {
		t.Fatalf("subagentCatalog 必须是宿主真值：%v", values["subagentCatalog"])
	}
	if _, has := catalog[0].(map[string]any)["label"]; !has {
		t.Fatalf("官方 continuable catalog 行的 label 必填：%v", catalog[0])
	}
	// nil 侧必须是 JSON null（官方 union 的另一支），不是"键缺失"。
	src.lastUsed, src.next = nil, nil
	values = unwrapResult(t, postOfficial(t, s.handleSessionProjections, `{"sessionId":"s1"}`).Body.Bytes())["values"].(map[string]any)
	selection = values["modelSelection"].(map[string]any)
	if selection["lastUsed"] != nil || selection["next"] != nil {
		t.Fatalf("两侧都没有真值时必须是 null：%v", selection)
	}
}

// TestOfficialSessionResidualMethodsAreRouted：三条 session 残余不再落 generic
// 501。没有对应宿主能力时必须是**结构化** 501（点名缺哪个接口），有能力时是真实现。
func TestOfficialSessionResidualMethodsAreRouted(t *testing.T) {
	src := newProjectionSource()
	s := officialServerWith(t, src.fakeOfficialSource, Options{})
	call := func(method, body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.serveOfficialAgentAPI(rec, req, method)
		var out map[string]any
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("%s 的响应不是 JSON：%q", method, rec.Body.String())
			}
		}
		return rec.Code, out
	}

	// 假源还没有实现 OfficialModelCatalogHost / OfficialSessionControlHost：
	// 必须点名能力，而不是移交 server.go 的 generic "not implemented"。
	for _, method := range []string{"session.modelCatalog", "session.initializeDefaultModel", "session.control"} {
		code, out := call(method, `{}`)
		if code != http.StatusNotImplemented {
			t.Fatalf("%s 缺宿主能力时应该 501，实际 %d %v", method, code, out)
		}
		msg, _ := out["error"].(map[string]any)["message"].(string)
		if !strings.Contains(msg, "OfficialModelCatalogHost") && !strings.Contains(msg, "OfficialSessionControlHost") {
			t.Fatalf("%s 的 501 必须点名缺哪个宿主接口：%q", method, msg)
		}
		if strings.TrimSpace(msg) == "not implemented" {
			t.Fatalf("%s 不能被 generic 501 吞掉", method)
		}
	}

	// 换上真能力后：零参数形状是硬校验。
	s.opt.Source = src
	if code, out := call("session.modelCatalog", `{"sessionId":"s1"}`); code != http.StatusBadRequest {
		t.Fatalf("modelCatalog 是零参数方法，多给字段必须 400：%d %v", code, out)
	}
	src.catalog = OfficialModelCatalog{
		Default:           OfficialModelSelection{Provider: "local", Model: "model-a"},
		RoutableProviders: []string{"local"},
		Groups: []OfficialModelProvider{{
			ID: "local", Name: "local",
			Models: []OfficialModelEntry{{ID: "model-a", Name: "Alpha"}},
		}},
	}
	code, out := call("session.modelCatalog", `{}`)
	if code != http.StatusOK {
		t.Fatalf("modelCatalog 200：%d %v", code, out)
	}
	result := out["result"].(map[string]any)
	if result["default"].(map[string]any)["provider"] != "local" {
		t.Fatalf("default 必须原样透出：%v", result)
	}
	if groups := result["groups"].([]any); len(groups) != 1 {
		t.Fatalf("groups 必须是宿主真值：%v", result)
	}
	// initializeDefaultModel 的 void 用空 result 对象表达（本仓出口不接受 null）。
	if code, out := call("session.initializeDefaultModel", `{}`); code != http.StatusOK {
		t.Fatalf("initializeDefaultModel 200：%d %v", code, out)
	} else if result, ok := out["result"].(map[string]any); !ok || len(result) != 0 {
		t.Fatalf("initializeDefaultModel 的 void 必须是空 result 对象：%v", out)
	}
}

// TestOfficialSessionControlStreamBaseline：control 是官方流；首帧 baseline 必须
// 带上每个真实会话的 {asOfSeq, values}，且值域来自同一份投影计算。
func TestOfficialSessionControlStreamBaseline(t *testing.T) {
	src := newProjectionSource()
	src.sessionIDs = []string{"s1"}
	s := officialServerWith(t, src.fakeOfficialSource, Options{})
	s.opt.Source = src

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/session.control", strings.NewReader(`{}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.handleSessionControl(rec, req)
		close(done)
	}()
	time.Sleep(80 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("session.control 没在取消后退出")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/x-ndjson") {
		t.Fatalf("control 必须是 NDJSON 流：%q", ct)
	}
	scanner := bufio.NewScanner(bytes.NewReader(rec.Body.Bytes()))
	if !scanner.Scan() {
		t.Fatal("control 没有 baseline 帧")
	}
	var frame struct {
		Type  string `json:"type"`
		Value struct {
			Projections map[string]struct {
				AsOfSeq int64          `json:"asOfSeq"`
				Values  map[string]any `json:"values"`
			} `json:"projections"`
		} `json:"value"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
		t.Fatalf("baseline 不是合法 JSON：%v（%q）", err, scanner.Text())
	}
	if frame.Type != "baseline" {
		t.Fatalf("首帧必须是 baseline：%q", frame.Type)
	}
	projection, ok := frame.Value.Projections["s1"]
	if !ok {
		t.Fatalf("baseline 必须含真实会话 s1：%v", frame.Value.Projections)
	}
	if projection.AsOfSeq != 6 {
		t.Fatalf("asOfSeq 必须是真实日志末端 6：%d", projection.AsOfSeq)
	}
	if _, ok := projection.Values["sessionListMetadata"]; !ok {
		t.Fatalf("values 必须与 session.projections 同一份：%v", projection.Values)
	}
}

// TestOfficialSessionSelectModelProvider：provider 不再只回显——不可路由 400，
// 可路由时宿主先落 provider 元数据；model 落盘失败必须回滚 provider 元数据。
func TestOfficialSessionSelectModelProvider(t *testing.T) {
	src := newProjectionSource()
	src.resolveFn = func(_, provider, model, effort string) (OfficialModelSelection, error) {
		if provider == "wrong" {
			return OfficialModelSelection{}, fmt.Errorf("%w: provider 不匹配", ErrOfficialBadInput)
		}
		return OfficialModelSelection{Provider: "local", Model: model, ReasoningEffort: effort}, nil
	}
	s := officialServerWith(t, src.fakeOfficialSource, Options{
		SetSessionModel: func(sessionID, model string) error {
			if model == "boom" {
				return fmt.Errorf("写盘失败")
			}
			src.resolved = append(src.resolved, "MODEL_WRITTEN")
			return nil
		},
		SetSessionEffort: func(sessionID, effort string) error { return nil },
	})
	s.opt.Source = src

	code, out := postOfficialJSON(t, s.handleSessionSelectModel, `{"sessionId":"s1","provider":"wrong","model":"m","reasoningEffort":"low"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("不可路由的 provider 必须 400：%d %v", code, out)
	}
	for _, entry := range src.resolved {
		if entry == "MODEL_WRITTEN" {
			t.Fatalf("校验失败时不能写 model：%v", src.resolved)
		}
	}
	if len(src.cleared) != 0 {
		t.Fatalf("校验失败时不该有需要回滚的写：%v", src.cleared)
	}
	code, out = postOfficialJSON(t, s.handleSessionSelectModel, `{"sessionId":"s1","provider":"local","model":"m","reasoningEffort":"low"}`)
	if code != http.StatusOK {
		t.Fatalf("可路由的选择必须 200：%d %v", code, out)
	}
	selected := out["result"].(map[string]any)["selected"].(map[string]any)
	if selected["provider"] != "local" || selected["model"] != "m" || selected["reasoningEffort"] != "low" {
		t.Fatalf("selected 必须回真实落盘的选择：%v", selected)
	}
	code, out = postOfficialJSON(t, s.handleSessionSelectModel, `{"sessionId":"s1","provider":"local","model":"boom"}`)
	if code == http.StatusOK {
		t.Fatalf("model 落盘失败不能报成功：%v", out)
	}
	if len(src.cleared) != 1 || src.cleared[0] != "s1" {
		t.Fatalf("model 落盘失败必须回滚 provider 元数据：%v", src.cleared)
	}
}

// postOfficialJSON 是"直接调 handler + 解 JSON"的小工具（与 postOfficial 的区别：这里
// 要拿到状态码 + 已解包信封）。
func postOfficialJSON(t *testing.T, handler officialHandler, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/official", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON：%q", rec.Body.String())
		}
	}
	return rec.Code, out
}

// TestOfficialSessionResidualMethodsRouteThroughHandleAPI：不只是 handler 可直接
// 调用——这三条方法真的能从 `/api/...` 进入官方分派（server.go 的
// `IsOfficialAgentMethod` 分支），这是"无路由方法"缺口关闭的直接证据。
func TestOfficialSessionResidualMethodsRouteThroughHandleAPI(t *testing.T) {
	src := newProjectionSource()
	s := newTestServer(t, Options{Source: src.fakeOfficialSource})

	// 没有宿主能力：结构化 501（点名缺接口），不是 server.go 的 generic 501。
	rec := postJSON(t, s, "/api/session.modelCatalog", `{}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("无能力的 modelCatalog 应该 501，实际 %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("501 不是 JSON：%q", rec.Body.String())
	}
	msg, _ := out["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "OfficialModelCatalogHost") {
		t.Fatalf("必须点名缺的宿主接口（证明走到了官方分派而不是 generic 501）：%q", msg)
	}
	rec = postJSON(t, s, "/api/session.initializeDefaultModel", `{}`)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "OfficialModelCatalogHost") {
		t.Fatalf("无目录能力的 initializeDefaultModel 必须结构化 501：%d %s", rec.Code, rec.Body.String())
	}

	// 换上实现：同一个 /api 路径真的返回真实目录。
	s.opt.Source = src
	src.catalog = OfficialModelCatalog{
		Default:           OfficialModelSelection{Provider: "gateway", Model: "m"},
		RoutableProviders: []string{"gateway"},
		Groups:            []OfficialModelProvider{{ID: "gateway", Name: "gateway", Models: []OfficialModelEntry{{ID: "m", Name: "M"}}}},
	}
	rec = postJSON(t, s, "/api/session.modelCatalog", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("真实能力下 modelCatalog 必须 200：%d %s", rec.Code, rec.Body.String())
	}
	// initializeDefaultModel：宿主有目录能力时回 void（空 result 对象）。
	rec = postJSON(t, s, "/api/session.initializeDefaultModel", `{}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"result":{}`) {
		t.Fatalf("initializeDefaultModel 的 void 必须是空 result 对象：%d %s", rec.Code, rec.Body.String())
	}
	// control：真实 NDJSON 流（经 handleAPI 的 statusRecorder 也必须有 Flush）。
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/session.control", strings.NewReader(`{}`)).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "t04-cookie"})
	s.mu.Lock()
	s.cookies["t04-cookie"] = struct{}{}
	s.mu.Unlock()
	rec = httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.handleAPI(rec, req)
		close(done)
	}()
	time.Sleep(80 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleAPI 的 control 流没在取消后退出")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/x-ndjson") {
		t.Fatalf("handleAPI 的 control 必须是 NDJSON：%q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"type":"baseline"`) {
		t.Fatalf("handleAPI 的 control 必须发 baseline：%q", rec.Body.String())
	}
}
