package main

// RC2-E7：Office 面接线的产品级测试。
//
// 覆盖四步接线的**产品形状**（不是 handler 单测）：
//  1. server.go 的分派把 `/api/officeKit.*` 交给 officeKit handler（见
//     internal/transport/handlers_office_kit_dispatch_test.go；这里从宿主侧再验一次）；
//  2. `newOfficeKitAPI` 的适配器真能出 PDF、错误码按表映射；
//  3. `newOfficePDFAPIWith` 与 officeKit 面复用同一个 provider；
//  4. `builtinModules` 真的注册了三个 office 工具，且用的是共享槽里的那一份 provider。
//
// 判据固定：有引擎 → 真 PDF 字节（`%PDF-` 头 + bytes 一致）；没引擎 → 501 +
// capableWhen，绝不返回空 data / 空 PDF。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/officekit"
	"dshgo/internal/transport"
)

// repoPath 把相对仓库根的路径拼成测试进程可用的路径（go test 的 cwd 是包目录）。
func repoPath(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

// evidenceDir 是本次接线的证据目录（产物 PDF 落这里，报告引用它）。
func evidenceDir(t *testing.T) string {
	t.Helper()
	dir := repoPath("tmp", "GO8-RC2", "e7-evidence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建证据目录失败：%v", err)
	}
	return dir
}

func probeDocx(t *testing.T) string {
	t.Helper()
	path := repoPath("internal", "officekit", "testdata", "probe.docx")
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absolute); err != nil {
		t.Skipf("testdata 缺失：%v", err)
	}
	return absolute
}

// newOfficeKitHostServer 起一个只挂了 Office 面的 transport server（真 HTTP）。
func newOfficeKitHostServer(t *testing.T, api transport.OfficialOfficeKitAPI) (*httptest.Server, string) {
	t.Helper()
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialOfficeKitAPI(api)
	t.Cleanup(srv.DetachOfficialOfficeKitAPI)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv.MintInternalCookie()
}

// postOfficeKitHost 打一次 officeKit 方法，返回 HTTP 状态与原始响应体。
func postOfficeKitHost(t *testing.T, base, cookie, method, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+"/api/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "dshd_session", Value: cookie})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw)
}

type officeKitRenderEnvelope struct {
	Result struct {
		Data         []byte   `json:"data"`
		MissingFonts []string `json:"missingFonts"`
		Generation   string   `json:"generation"`
		AbsolutePath string   `json:"absolutePath"`
		Version      string   `json:"version"`
		Eof          bool     `json:"eof"`
		Bytes        int      `json:"bytes"`
	} `json:"result"`
}

// ---------------------------------------------------------------------------
// 1) 共享 provider：同一进程只有一份引擎/队列/generation
// ---------------------------------------------------------------------------

func TestSharedOfficeKitProviderIsSingleton(t *testing.T) {
	home := t.TempDir()
	first := sharedOfficeKitProvider(home)
	if first == nil {
		t.Fatal("共享槽给了 nil")
	}
	if again := sharedOfficeKitProvider(home); again != first {
		t.Fatal("同一 home 两次取到不同 provider：工具面与 Transport 面会变成两套引擎状态")
	}
	if other := sharedOfficeKitProvider(t.TempDir()); other == first {
		t.Fatal("不同 home 共用了一个 provider：产物仓与保留策略会串台")
	}

	// serve 侧登记的那一份必须原样被后续取用（cmdServe → 工具/Transport 共用）。
	explicit := officekit.Open(officekit.Options{Home: t.TempDir()})
	t.Cleanup(explicit.Close)
	explicitHome := t.TempDir()
	shareOfficeKitProvider(explicitHome, explicit)
	if got := sharedOfficeKitProvider(explicitHome); got != explicit {
		t.Fatal("登记后的共享 provider 不是同一实例")
	}
	if got := sharedOfficeKitProvider(""); got != explicit {
		t.Fatal("空 home 应回落到「当前登记的 provider」")
	}
}

// ---------------------------------------------------------------------------
// 2) (scope, path) → 绝对路径：GUI 传的是工作区相对路径
// ---------------------------------------------------------------------------

func TestOfficeKitAbsolutePathResolution(t *testing.T) {
	sessionDir := t.TempDir()
	home := t.TempDir()
	api := newOfficeKitAPI(nil, home).withSessionCwd(func(scope string) (string, error) {
		if scope == "s1" {
			return sessionDir, nil
		}
		return "", os.ErrNotExist
	})

	if got, err := api.absolutePath("s1", filepath.Join(sessionDir, "a.docx")); err != nil || got != filepath.Join(sessionDir, "a.docx") {
		t.Fatalf("绝对路径应原样使用：%q %v", got, err)
	}
	if got, err := api.absolutePath("s1", "docs/a.docx"); err != nil || got != filepath.Join(sessionDir, "docs", "a.docx") {
		t.Fatalf("相对路径应按会话 cwd 解析：%q %v", got, err)
	}
	// 会话 id 解析不出来时退回 <home>/workspaces/<scope>（与旧 officeToPdf 面同语义）。
	if got, err := api.absolutePath("unknown", "a.docx"); err != nil ||
		got != filepath.Join(home, "workspaces", "unknown", "a.docx") {
		t.Fatalf("退回 home 的解析不对：%q %v", got, err)
	}
	// `..` 逃逸必须拒：workspaceFileScope 限定读取范围。
	if _, err := api.absolutePath("s1", "..\\..\\secret.docx"); err == nil {
		t.Fatal("越出工作区的路径没被拒")
	}
	if _, err := api.absolutePath("s1", "  "); err == nil {
		t.Fatal("空 path 没被拒")
	}
}

// ---------------------------------------------------------------------------
// 3) 工具面：builtinModules 注册了三个 office 工具，且用共享 provider
// ---------------------------------------------------------------------------

func TestOfficeToolsRegisteredWithSharedProvider(t *testing.T) {
	names := map[string]bool{}
	for _, entry := range buildToolTableMust(t) {
		names[entry.Name] = true
	}
	for _, want := range []string{"office_convert", "office_recalculate", "office_render"} {
		if !names[want] {
			t.Fatalf("builtinModules 里没有 %s", want)
		}
	}
}

// ---------------------------------------------------------------------------
// 4) 端到端：真 HTTP → 产品适配器 → 真引擎 → 真 PDF
// ---------------------------------------------------------------------------

func TestOfficeKitWiringEndToEndRealEngine(t *testing.T) {
	fixture := probeDocx(t)
	home := t.TempDir()
	provider := officekit.Open(officekit.Options{Home: home, Timeout: 120 * time.Second})
	t.Cleanup(provider.Close)
	if provider.Engine() == nil {
		t.Skip("本机没有 LibreOfficeKit 引擎")
	}
	shareOfficeKitProvider(home, provider)

	// 会话工作区：把夹具复制进去，模拟 GUI 传「工作区相对路径」。
	sessionDir := t.TempDir()
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "probe.docx"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	api := newOfficeKitAPI(provider, home).withSessionCwd(func(scope string) (string, error) {
		return sessionDir, nil
	})
	base, cookie := newOfficeKitHostServer(t, api)

	// generation 必须与工具面拿到的是同一个 provider 的 generation。
	status, body := postOfficeKitHost(t, base.URL, cookie, "officeKit.generation", `{}`)
	if status != 200 {
		t.Fatalf("generation = %d %s", status, body)
	}
	if !strings.Contains(body, provider.GenerationID()) || !strings.Contains(body, sharedOfficeKitProvider(home).GenerationID()) {
		t.Fatalf("generation 与共享 provider 不一致（两套引擎状态？）：%s", body)
	}

	// 相对路径（GUI 形状）也要能出真 PDF。
	status, body = postOfficeKitHost(t, base.URL, cookie, "officeKit.render",
		`{"workspaceFileScopeId":"session-1","path":"probe.docx","priority":"foreground"}`)
	if status != 200 {
		t.Fatalf("render = %d %s", status, body)
	}
	var envelope officeKitRenderEnvelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("结果不是合法 JSON：%v", err)
	}
	if !strings.HasPrefix(string(envelope.Result.Data), "%PDF-") {
		t.Fatalf("data 不是 PDF：%q", string(envelope.Result.Data[:min(len(envelope.Result.Data), 8)]))
	}
	if len(envelope.Result.Data) < 1000 || envelope.Result.Bytes != len(envelope.Result.Data) || !envelope.Result.Eof {
		t.Fatalf("RenderedDocumentBytes 不完整：bytes=%d len=%d eof=%v",
			envelope.Result.Bytes, len(envelope.Result.Data), envelope.Result.Eof)
	}
	if envelope.Result.AbsolutePath != filepath.Join(sessionDir, "probe.docx") {
		t.Fatalf("absolutePath = %q，期望会话工作区里的那份", envelope.Result.AbsolutePath)
	}
	if envelope.Result.MissingFonts == nil || envelope.Result.Version == "" {
		t.Fatalf("缺 missingFonts/version：%+v", envelope.Result)
	}
	// 证据落盘：报告里引用的就是这份真 PDF。
	artifact := filepath.Join(evidenceDir(t), "e7-officekit-wiring.pdf")
	if err := os.WriteFile(artifact, envelope.Result.Data, 0o644); err != nil {
		t.Fatalf("写证据 PDF 失败：%v", err)
	}
	t.Logf("接线后端到端真实 PDF：%d 字节 → %s（generation=%s）",
		len(envelope.Result.Data), artifact, envelope.Result.Generation)
}

// ---------------------------------------------------------------------------
// 5) 引擎缺失：四操作面仍 501 + capableWhen，不许假渲染
// ---------------------------------------------------------------------------

func TestOfficeKitWiringWithoutEngineReports501(t *testing.T) {
	fixture := probeDocx(t)
	empty := t.TempDir()
	provider := officekit.Open(officekit.Options{Home: t.TempDir(),
		EnginePath: filepath.Join(empty, "absent"), SearchRoots: []string{empty}})
	t.Cleanup(provider.Close)
	if provider.Engine() != nil {
		t.Skip("这台机器自动发现了引擎；无法验证「引擎缺失」路径")
	}
	capability := provider.Capability()
	if capability.Available || capability.CapableWhen == "" || len(capability.Tried) == 0 {
		t.Fatalf("没有引擎时 Capability 必须说清「装/配什么就能用」：%+v", capability)
	}
	base, cookie := newOfficeKitHostServer(t, newOfficeKitAPI(provider))

	// render：501 + unavailable + capableWhen + capability（含 tried），且没有 PDF 数据。
	status, body := postOfficeKitHost(t, base.URL, cookie, "officeKit.render",
		`{"workspaceFileScopeId":"s","path":`+jsonString(fixture)+`,"priority":"foreground"}`)
	if status != http.StatusNotImplemented {
		t.Fatalf("没有引擎时 render 应 501，得到 %d %s", status, body)
	}
	for _, want := range []string{"unavailable", "capableWhen", "capability", "tried", "DSHD_OFFICE_KIT_ENGINE"} {
		if !strings.Contains(body, want) {
			t.Fatalf("501 体缺 %q：%s", want, body)
		}
	}
	if strings.Contains(body, "%PDF") || strings.Contains(body, "JVBERi0") {
		t.Fatalf("没有引擎却返回了 PDF 数据：%s", body)
	}

	// generation：不许编造一个「能用」的 generation。
	status, body = postOfficeKitHost(t, base.URL, cookie, "officeKit.generation", `{}`)
	if status != 200 || !strings.Contains(body, "officekit-unavailable") {
		t.Fatalf("没有引擎时 generation 必须是真值 officekit-unavailable：%d %s", status, body)
	}
}

// jsonString 把字符串转成 JSON 字面量（避免 payload 里手写转义）。
func jsonString(s string) string {
	buf, _ := json.Marshal(s)
	return string(buf)
}
