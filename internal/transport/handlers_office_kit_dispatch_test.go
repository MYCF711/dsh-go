package transport

// RC2-E7：officeKit 面**经真实 server.ServeHTTP 分派**的接线测试。
//
// 为什么单独一个文件：`handlers_office_kit_rc2_test.go` 是直接调
// `s.ServeOfficialOfficeKitAPI` 的，它证明的是「handler 会给出正确的 501/200」，
// **证明不了 server.go 里有这条分派**。rc.1 的逐方法未实现表只认得旧名字，
// 新方法如果只在 handler 里被认领而没进 `IsOfficialOfficeKitMethod` 分派，
// 真实 `/api` 上就会落到 404——只有走 ServeHTTP 才测得到（同
// `TestForgeRC2ReachableThroughHTTP` 的判据）。
//
// 先红后绿：接线前这里第一条是 404（分派不认领），接线后是 501 + capableWhen，
// 挂上真引擎后第三条能拿到真 PDF。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/officekit"
)

// officeKitHTTPCall 打一次 /api/<method>，走完整 ServeHTTP（cookie → 方法分派）。
func officeKitHTTPCall(t *testing.T, srv *Server, method string, body string) (int, string) {
	t.Helper()
	cookie := srv.MintInternalCookie()
	if cookie == "" {
		t.Fatalf("MintInternalCookie 返回空值")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestOfficeKitReachableThroughHTTP：两个名称在真实 /api 上必须被认领。
// 没挂实现 → 501 + capableWhen（不是 404、不是静默成功）；挂了实现 → 200。
func TestOfficeKitReachableThroughHTTP(t *testing.T) {
	srv := newOfficeKitTestServer(t)
	renderBody := `{"workspaceFileScopeId":"s","path":"a.docx","priority":"foreground"}`

	// 1) 没有实现：两个方法都必须 501，不能落到 404。
	for method, body := range map[string]string{
		string(MethodOfficeKitGeneration): `{}`,
		string(MethodOfficeKitRender):     renderBody,
	} {
		status, out := officeKitHTTPCall(t, srv, method, body)
		if status == http.StatusNotFound {
			t.Fatalf("%s 回了 404：server.go 的分派没认领 officeKit 面（body=%s）", method, out)
		}
		if status != http.StatusNotImplemented {
			t.Fatalf("%s 状态码 %d，期望 501（body=%s）", method, status, out)
		}
		// 分派必须把请求交给 officeKit handler：错误码是 officeKit 面的 unavailable，
		// 而不是通用未实现表的 not_implemented。
		for _, want := range []string{`"unavailable"`, `"capableWhen"`} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s 的 501 体缺 %q：%s", method, want, out)
			}
		}
		if strings.Contains(out, "%PDF") {
			t.Fatalf("%s 没引擎却给了 PDF 数据：%s", method, out)
		}
	}

	// 2) 挂上**没有引擎的**真适配器（产品在「引擎没装」时的真实状态）：
	// 必须 501 + code=unavailable + capableWhen + capability.tried，且没有 PDF 数据。
	// 输入用一个**真实存在**的 Office 文件：参数校验（文件不存在 → 400）先于引擎探测，
	// 拿不存在的路径测不出「引擎缺失」这一层。
	fixture, err := filepath.Abs(filepath.Join("..", "officekit", "testdata", "probe.docx"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(fixture); statErr != nil {
		t.Skipf("testdata 缺失：%v", statErr)
	}
	empty := t.TempDir()
	noEngine := officekit.Open(officekit.Options{Home: t.TempDir(),
		EnginePath: filepath.Join(empty, "absent"), SearchRoots: []string{empty}})
	t.Cleanup(noEngine.Close)
	if noEngine.Engine() != nil {
		t.Skip("这台机器自动发现了引擎；无法验证「引擎缺失」路径")
	}
	srv.AttachOfficialOfficeKitAPI(&realOfficeKitAdapter{provider: noEngine})
	status, out := officeKitHTTPCall(t, srv, string(MethodOfficeKitRender),
		`{"workspaceFileScopeId":"s","path":`+quoteJSON(fixture)+`,"priority":"foreground"}`)
	if status != http.StatusNotImplemented {
		t.Fatalf("引擎缺失时 render 应 501，得到 %d %s", status, out)
	}
	for _, want := range []string{"unavailable", "capableWhen", "capability", "tried", "DSHD_OFFICE_KIT_ENGINE"} {
		if !strings.Contains(out, want) {
			t.Fatalf("引擎缺失的 501 体缺 %q：%s", want, out)
		}
	}
	if strings.Contains(out, "%PDF") {
		t.Fatalf("引擎缺失却返回了 PDF 数据：%s", out)
	}
	srv.DetachOfficialOfficeKitAPI()

	// 3) 挂上实现：generation 正常返回、render 能走通（假的 PDF 字节也算分派通了）。
	srv.AttachOfficialOfficeKitAPI(&fakeOfficeKit{generation: "officekit-abc",
		capability: OfficialOfficeKitCapability{Available: true, Generation: "officekit-abc"}})
	t.Cleanup(srv.DetachOfficialOfficeKitAPI)
	if status, out := officeKitHTTPCall(t, srv, string(MethodOfficeKitGeneration), `{}`); status != 200 {
		t.Fatalf("generation = %d %s", status, out)
	}
	status, out = officeKitHTTPCall(t, srv, string(MethodOfficeKitRender), renderBody)
	if status != 200 {
		t.Fatalf("render = %d %s", status, out)
	}
	if !strings.Contains(out, "JVBERi0") {
		t.Fatalf("分派通了但结果里没有 base64 PDF：%s", out)
	}
}

// TestOfficeKitDispatchEndToEndRealEngine 走「真实 ServeHTTP → 适配器 → 真引擎」，
// 证明接线后的 officeKit.render 拿到的是**真 PDF**，不是占位字节。
func TestOfficeKitDispatchEndToEndRealEngine(t *testing.T) {
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
	srv := newOfficeKitTestServer(t)
	srv.AttachOfficialOfficeKitAPI(&realOfficeKitAdapter{provider: provider})
	t.Cleanup(srv.DetachOfficialOfficeKitAPI)

	body := `{"workspaceFileScopeId":"s1","path":` + quoteJSON(fixture) + `,"priority":"foreground"}`
	status, out := officeKitHTTPCall(t, srv, string(MethodOfficeKitRender), body)
	if status != 200 {
		t.Fatalf("render = %d %s", status, out)
	}
	var response struct {
		Result struct {
			Data    []byte `json:"data"`
			Bytes   int    `json:"bytes"`
			Eof     bool   `json:"eof"`
			Version string `json:"version"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("结果不是合法 JSON：%v", err)
	}
	if !strings.HasPrefix(string(response.Result.Data), "%PDF-") {
		t.Fatalf("分派拿到的 data 不是 PDF")
	}
	if response.Result.Bytes != len(response.Result.Data) || !response.Result.Eof {
		t.Fatalf("RenderedDocumentBytes 不完整：bytes=%d len=%d eof=%v",
			response.Result.Bytes, len(response.Result.Data), response.Result.Eof)
	}
	t.Logf("分派级真实 PDF：%d 字节（version=%s）", len(response.Result.Data), response.Result.Version)
}

// quoteJSON 把路径转成 JSON 字符串字面量（避免在 payload 里手写转义）。
func quoteJSON(s string) string {
	buf, _ := json.Marshal(s)
	return string(buf)
}
