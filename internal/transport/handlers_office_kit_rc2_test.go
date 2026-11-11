package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/officekit"
)

// fakeOfficeKit 是可编排的 officeKit 实现。
type fakeOfficeKit struct {
	mu          sync.Mutex
	generation  string
	capability  OfficialOfficeKitCapability
	renderErr   error
	renderCalls int
	lastScope   string
	lastPath    string
	lastPrio    string
}

func (f *fakeOfficeKit) Generation(context.Context) (string, OfficialOfficeKitCapability, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generation, f.capability, nil
}

func (f *fakeOfficeKit) Render(_ context.Context, scope, path, priority string) (OfficialOfficePDFResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renderCalls++
	f.lastScope, f.lastPath, f.lastPrio = scope, path, priority
	if f.renderErr != nil {
		return OfficialOfficePDFResult{}, f.renderErr
	}
	n := len("%PDF-1.7")
	return OfficialOfficePDFResult{MissingFonts: []string{}, Generation: f.generation, Data: []byte("%PDF-1.7"),
		Eof: true, AbsolutePath: "/w/" + path, Version: "v1", Bytes: &n}, nil
}

func newOfficeKitTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer(Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func postOfficeKit(s *Server, method, body string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeOfficialOfficeKitAPI(rec, req, method)
	return rec.Code, rec.Body.String()
}

func TestOfficialOfficeKitMethodsListed(t *testing.T) {
	got := OfficialOfficeKitMethods()
	want := []Method{MethodOfficeKitGeneration, MethodOfficeKitRender}
	if len(got) != len(want) {
		t.Fatalf("方法数不对：%v", got)
	}
	for i, method := range want {
		if got[i] != method {
			t.Fatalf("方法表第 %d 项 = %v，期望 %v", i, got[i], method)
		}
		if !IsOfficialOfficeKitMethod(string(method)) {
			t.Fatalf("%v 没被认出", method)
		}
	}
	if IsOfficialOfficeKitMethod("officeKit.unknown") || IsOfficialOfficeKitMethod("officeToPdf.render") {
		t.Fatal("非本文件方法被认成 officeKit 面")
	}
}

// 没接线 = 结构化 501 + capableWhen（不许 404/空结果）。
func TestOfficeKitWithoutAPIReports501(t *testing.T) {
	s := newOfficeKitTestServer(t)
	cases := map[string]string{
		string(MethodOfficeKitGeneration): `{}`,
		string(MethodOfficeKitRender):     `{"workspaceFileScopeId":"s","path":"a.docx","priority":"foreground"}`,
	}
	for method, payload := range cases {
		code, body := postOfficeKit(s, method, payload)
		if code != 501 {
			t.Fatalf("%s 未接线应 501，得到 %d %s", method, code, body)
		}
		if !strings.Contains(body, "unavailable") || !strings.Contains(body, "capableWhen") {
			t.Fatalf("%s 的 501 体缺 code/capableWhen：%s", method, body)
		}
	}
}

func TestOfficeKitGenerationAndRender(t *testing.T) {
	s := newOfficeKitTestServer(t)
	api := &fakeOfficeKit{generation: "officekit-abc",
		capability: OfficialOfficeKitCapability{Available: true, Generation: "officekit-abc"}}
	s.AttachOfficialOfficeKitAPI(api)
	t.Cleanup(s.DetachOfficialOfficeKitAPI)

	code, body := postOfficeKit(s, string(MethodOfficeKitGeneration), `{}`)
	if code != 200 || !strings.Contains(body, `"officekit-abc"`) {
		t.Fatalf("generation = %d %s", code, body)
	}
	// generation 不接受参数（官方契约里它没有入参）。
	if code, _ = postOfficeKit(s, string(MethodOfficeKitGeneration), `{"x":1}`); code != 400 {
		t.Fatalf("generation 带参数应 400，得到 %d", code)
	}

	code, body = postOfficeKit(s, string(MethodOfficeKitRender),
		`{"workspaceFileScopeId":"s1","path":"a.docx","priority":"background"}`)
	if code != 200 {
		t.Fatalf("render = %d %s", code, body)
	}
	if !strings.Contains(body, "JVBERi0") {
		t.Fatalf("render 结果里没有 base64 PDF：%s", body)
	}
	for _, field := range []string{"missingFonts", "generation", "offset", "eof", "absolutePath", "version", "bytes"} {
		if !strings.Contains(body, field) {
			t.Fatalf("render 结果缺字段 %s：%s", field, body)
		}
	}
	if api.lastScope != "s1" || api.lastPath != "a.docx" || api.lastPrio != "background" {
		t.Fatalf("参数没透传：%+v", api)
	}
	// 会话 id 的两种旧写法也要认。
	if code, body = postOfficeKit(s, string(MethodOfficeKitRender), `{"sessionId":"s2","path":"a.docx","priority":"foreground"}`); code != 200 {
		t.Fatalf("sessionId 别名 = %d %s", code, body)
	}
}

func TestOfficeKitRenderRejectsBadParameters(t *testing.T) {
	s := newOfficeKitTestServer(t)
	s.AttachOfficialOfficeKitAPI(&fakeOfficeKit{generation: "g"})
	t.Cleanup(s.DetachOfficialOfficeKitAPI)
	cases := map[string]string{
		"缺 scope":     `{"path":"a.docx","priority":"foreground"}`,
		"缺 path":      `{"workspaceFileScopeId":"s","priority":"foreground"}`,
		"缺 priority":  `{"workspaceFileScopeId":"s","path":"a.docx"}`,
		"priority 非法": `{"workspaceFileScopeId":"s","path":"a.docx","priority":"now"}`,
		"未知字段":        `{"workspaceFileScopeId":"s","path":"a.docx","priority":"foreground","extra":1}`,
		"非 JSON":      `nope`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if code, response := postOfficeKit(s, string(MethodOfficeKitRender), body); code != 400 {
				t.Fatalf("应 400，得到 %d %s", code, response)
			}
		})
	}
}

// 没有引擎时：501 + code=unavailable + capableWhen + capability（含 engineRoot 等）。
func TestOfficeKitUnavailableCarriesCapability(t *testing.T) {
	s := newOfficeKitTestServer(t)
	capability := OfficialOfficeKitCapability{Available: false, Generation: "officekit-unavailable",
		CapableWhen: "设置 DSHD_OFFICE_KIT_ENGINE 指向引擎包根", Tried: []string{"auto:/x"}}
	api := &fakeOfficeKit{generation: "officekit-unavailable", capability: capability,
		renderErr: &OfficeKitAPIError{Status: 501, Code: "unavailable",
			Message: "本机没有可用的 LibreOfficeKit 原生引擎", CapableWhen: capability.CapableWhen}}
	s.AttachOfficialOfficeKitAPI(api)
	t.Cleanup(s.DetachOfficialOfficeKitAPI)

	code, body := postOfficeKit(s, string(MethodOfficeKitRender),
		`{"workspaceFileScopeId":"s","path":"a.docx","priority":"foreground"}`)
	if code != 501 {
		t.Fatalf("应 501，得到 %d %s", code, body)
	}
	for _, want := range []string{"unavailable", "capableWhen", "capability", "DSHD_OFFICE_KIT_ENGINE", "tried", "auto:/x"} {
		if !strings.Contains(body, want) {
			t.Fatalf("501 体缺 %q：%s", want, body)
		}
	}
}

// 其它错误码的 HTTP 映射（400/504/502/429）。
func TestOfficeKitErrorStatusMapping(t *testing.T) {
	cases := map[int]string{400: "invalid-request", 504: "timeout", 502: "failed", 429: "busy"}
	for status, code := range cases {
		t.Run(code, func(t *testing.T) {
			s := newOfficeKitTestServer(t)
			api := &fakeOfficeKit{generation: "g",
				renderErr: &OfficeKitAPIError{Status: status, Code: code, Message: "boom"}}
			s.AttachOfficialOfficeKitAPI(api)
			t.Cleanup(s.DetachOfficialOfficeKitAPI)
			gotStatus, body := postOfficeKit(s, string(MethodOfficeKitRender),
				`{"workspaceFileScopeId":"s","path":"a.docx","priority":"foreground"}`)
			if gotStatus != status {
				t.Fatalf("%s 应 %d，得到 %d %s", code, status, gotStatus, body)
			}
			if !strings.Contains(body, code) {
				t.Fatalf("错误体没带 %s：%s", code, body)
			}
		})
	}
}

// 与旧 officeToPdf 面互不干扰：两个方法名不能互相认领。
func TestOfficeKitDoesNotClaimLegacyMethods(t *testing.T) {
	if IsOfficialOfficePDFMethod(string(MethodOfficeKitRender)) {
		t.Fatal("officeToPdf 面认领了 officeKit.render")
	}
	if IsOfficialOfficeKitMethod(string(MethodOfficeToPdfRender)) {
		t.Fatal("officeKit 面认领了 officeToPdf.render")
	}
}

// ---------------------------------------------------------------------------
// 端到端：按报告 §7.4 的接线形状把真实 provider 接到 Transport 上。
// 这段适配器与建议的 cmd/dshd/serve_office_kit.go 同形，用来证明接线能编译、
// 且线上行为就是「有引擎 → 真 PDF；没引擎 → 501 + capableWhen」。
// ---------------------------------------------------------------------------

type realOfficeKitAdapter struct{ provider *officekit.Provider }

func (a *realOfficeKitAdapter) Generation(context.Context) (string, OfficialOfficeKitCapability, error) {
	capability := a.provider.Capability()
	return capability.Generation, OfficialOfficeKitCapability{
		Available: capability.Available, Generation: capability.Generation,
		EngineVersion: capability.EngineVersion, EngineRoot: capability.EngineRoot,
		Executable: capability.Executable, ProgramDirectory: capability.ProgramDirectory,
		Source: capability.Source, Tried: capability.Tried, CapableWhen: capability.CapableWhen,
	}, nil
}

func (a *realOfficeKitAdapter) Render(ctx context.Context, _ string, path, priority string) (OfficialOfficePDFResult, error) {
	doc, err := a.provider.Render(ctx, path, priority)
	if err != nil {
		return OfficialOfficePDFResult{}, toOfficeKitAPIError(err)
	}
	fonts := doc.MissingFonts
	if fonts == nil {
		fonts = []string{}
	}
	return OfficialOfficePDFResult{MissingFonts: fonts, Generation: doc.Generation, Offset: doc.Offset,
		Data: doc.Data, Eof: doc.Eof, AbsolutePath: doc.AbsolutePath, Version: doc.Version, Bytes: doc.Bytes}, nil
}

// toOfficeKitAPIError 是 §4 错误码表的实现（建议原样搬进 cmd/dshd/serve_office_kit.go）。
func toOfficeKitAPIError(err error) error {
	var kitErr *officekit.Error
	if !errors.As(err, &kitErr) {
		return &OfficeKitAPIError{Status: 502, Code: "failed", Message: err.Error()}
	}
	status := 502
	code := string(kitErr.Code)
	switch kitErr.Code {
	case officekit.CodeUnavailable:
		status = 501
	case officekit.CodeInvalidRequest, officekit.CodeInvalidDocument, officekit.CodeUnsupportedFormat,
		officekit.CodeInputTooLarge, officekit.CodeOutputTooLarge, officekit.CodeInvalidOutput,
		officekit.CodeSourceChanged:
		status = 400
	case officekit.CodeTimeout:
		status = 504
	case officekit.CodeBusy:
		status = 429
	}
	return &OfficeKitAPIError{Status: status, Code: code, Message: kitErr.Error(), CapableWhen: kitErr.CapableWhen}
}

// TestOfficeKitEndToEndRealEngine 走完整链路：HTTP 入口 → 适配器 → 真引擎 → 真 PDF。
func TestOfficeKitEndToEndRealEngine(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "officekit", "testdata", "probe.docx"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("testdata 缺失：%v", err)
	}
	provider := officekit.Open(officekit.Options{Home: t.TempDir(), Timeout: 120 * time.Second})
	t.Cleanup(provider.Close)
	if provider.Engine() == nil {
		t.Skip("本机没有 LibreOfficeKit 引擎")
	}
	s := newOfficeKitTestServer(t)
	s.AttachOfficialOfficeKitAPI(&realOfficeKitAdapter{provider: provider})
	t.Cleanup(s.DetachOfficialOfficeKitAPI)

	code, body := postOfficeKit(s, string(MethodOfficeKitGeneration), `{}`)
	if code != 200 || !strings.Contains(body, "officekit-") {
		t.Fatalf("generation = %d %s", code, body)
	}
	payload := fmt.Sprintf(`{"workspaceFileScopeId":"s1","path":%q,"priority":"foreground"}`, fixture)
	code, body = postOfficeKit(s, string(MethodOfficeKitRender), payload)
	if code != 200 {
		t.Fatalf("render = %d %s", code, body)
	}
	var response struct {
		Result struct {
			Data         []byte   `json:"data"`
			Generation   string   `json:"generation"`
			AbsolutePath string   `json:"absolutePath"`
			Version      string   `json:"version"`
			Eof          bool     `json:"eof"`
			Bytes        int      `json:"bytes"`
			MissingFonts []string `json:"missingFonts"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("结果不是合法 JSON：%v %s", err, body)
	}
	if !strings.HasPrefix(string(response.Result.Data), "%PDF-") {
		t.Fatalf("data 不是 PDF：%q", string(response.Result.Data[:min(len(response.Result.Data), 8)]))
	}
	if len(response.Result.Data) < 1000 || response.Result.Bytes != len(response.Result.Data) ||
		!response.Result.Eof || response.Result.Generation == "" || response.Result.Version == "" ||
		response.Result.AbsolutePath != fixture || response.Result.MissingFonts == nil {
		t.Fatalf("RenderedDocumentBytes 字段不完整：%+v", response.Result)
	}
	t.Logf("端到端真实 PDF：%d 字节（generation=%s）", len(response.Result.Data), response.Result.Generation)
}

// TestOfficeKitEndToEndNoEngine 同一条链路在“没有引擎”的部署上必须是 501。
func TestOfficeKitEndToEndNoEngine(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "officekit", "testdata", "probe.docx"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("testdata 缺失：%v", err)
	}
	empty := t.TempDir()
	provider := officekit.Open(officekit.Options{Home: t.TempDir(),
		EnginePath: filepath.Join(empty, "absent"), SearchRoots: []string{empty}})
	t.Cleanup(provider.Close)
	s := newOfficeKitTestServer(t)
	s.AttachOfficialOfficeKitAPI(&realOfficeKitAdapter{provider: provider})
	t.Cleanup(s.DetachOfficialOfficeKitAPI)
	payload := fmt.Sprintf(`{"workspaceFileScopeId":"s1","path":%q,"priority":"foreground"}`, fixture)
	code, body := postOfficeKit(s, string(MethodOfficeKitRender), payload)
	if code != 501 {
		t.Fatalf("没有引擎应 501，得到 %d %s", code, body)
	}
	for _, want := range []string{"unavailable", "capableWhen", "DSHD_OFFICE_KIT_ENGINE", "capability", "tried"} {
		if !strings.Contains(body, want) {
			t.Fatalf("501 体缺 %q：%s", want, body)
		}
	}
	if strings.Contains(body, "%PDF") {
		t.Fatalf("没有引擎却返回了 PDF 数据：%s", body)
	}
}
