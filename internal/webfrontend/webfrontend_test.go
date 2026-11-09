package webfrontend

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeDist 造一份最小 dist 并返回它的根目录。
//
// 文件名用斜杠写法，转成本机分隔符——测试里出现反斜杠字面量的话，
// 在非 Windows 上根本跑不起来。
func writeDist(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("建目录 %s 失败: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("写 %s 失败: %v", full, err)
		}
	}
	return dir
}

// newTestFrontend 建一份只含 index.html 的前端。
func newTestFrontend(t *testing.T) *Frontend {
	t.Helper()
	front, err := New(Config{Dir: writeDist(t, map[string]string{IndexFile: officialIndex})})
	if err != nil {
		t.Fatalf("建前端失败: %v", err)
	}
	return front
}

// TestNewRejectsBadDist 钉住"起不来"的三类配置错误。
//
// 这三条都必须**在进程起来之前**失败：目录不存在、没有 index.html、
// index.html 缺注入锚点。少一条就会变成"起来了但页面白屏"。
func TestNewRejectsBadDist(t *testing.T) {
	t.Run("空目录", func(t *testing.T) {
		if _, err := New(Config{}); err == nil {
			t.Fatalf("缺 Dir 必须报错")
		}
	})
	t.Run("目录不存在", func(t *testing.T) {
		if _, err := New(Config{Dir: filepath.Join(t.TempDir(), "nope")}); err == nil {
			t.Fatalf("不存在的目录必须报错")
		}
	})
	t.Run("不是目录", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := New(Config{Dir: file}); err == nil {
			t.Fatalf("文件当目录必须报错")
		}
	})
	t.Run("没有 index.html", func(t *testing.T) {
		dir := writeDist(t, map[string]string{"assets/app.js": "x"})
		_, err := New(Config{Dir: dir})
		if err == nil {
			t.Fatalf("缺 index.html 必须报错")
		}
		if !strings.Contains(err.Error(), IndexFile) {
			t.Fatalf("错误里必须出现 %s: %v", IndexFile, err)
		}
	})
	t.Run("缺注入锚点", func(t *testing.T) {
		dir := writeDist(t, map[string]string{IndexFile: "<div id=\"root\"></div>"})
		_, err := New(Config{Dir: dir})
		if err == nil {
			t.Fatalf("缺 <head> 锚点必须报错")
		}
		if !strings.Contains(err.Error(), "<head>") {
			t.Fatalf("错误里必须说清缺哪个锚点: %v", err)
		}
	})
}

// TestServeIndexInjectsGlobals 钉住"Serve 出来的页面带两个注入"。
func TestServeIndexInjectsGlobals(t *testing.T) {
	front := newTestFrontend(t)
	rec := httptest.NewRecorder()
	front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:52341/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / 应 200，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type 不对: %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`globalThis["__DSH_CONNECTION_RECOVERY__"] = {"backoffBaseMs":500,`,
		`globalThis["__DSH_TRANSPORT__"] = {"ownsHost":true,"streamBaseUrl":"http://127.0.0.1:52341"}`,
		`<div id="root"></div>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("响应里缺 %q\n实际: %s", want, body)
		}
	}
	// 官方：渲染后的 index 不带缓存头。
	if cc := rec.Header().Get("Cache-Control"); cc != "" {
		t.Fatalf("index 不该带 Cache-Control，实际 %q", cc)
	}
}

// TestServeIndexHead 钉住 HEAD 只回头不回体。
func TestServeIndexHead(t *testing.T) {
	front := newTestFrontend(t)
	rec := httptest.NewRecorder()
	front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "http://127.0.0.1:1/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD / 应 200，实际 %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD 不该有响应体，实际 %d 字节", rec.Body.Len())
	}
}

// TestServeAsset 钉住资产直发（类型/缓存头/字节）。
func TestServeAsset(t *testing.T) {
	front, err := New(Config{Dir: writeDist(t, map[string]string{
		IndexFile:              officialIndex,
		"assets/a.js":          "console.log(1)",
		"assets/a.css":         "body{}",
		"manifest.webmanifest": "{}",
		"assets/noext":         "x",
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want string
		body string
	}{
		{"/assets/a.js", "text/javascript; charset=utf-8", "console.log(1)"},
		{"/assets/a.css", "text/css; charset=utf-8", "body{}"},
		{"/manifest.webmanifest", "application/manifest+json", "{}"},
		// 官方 MIME 表里没有的扩展名 → application/octet-stream（不是猜）。
		{"/assets/noext", "application/octet-stream", "x"},
	} {
		rec := httptest.NewRecorder()
		front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1"+tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 应 200，实际 %d", tc.path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.want {
			t.Fatalf("%s 的 Content-Type 不对: got=%q want=%q", tc.path, got, tc.want)
		}
		if got := rec.Body.String(); got != tc.body {
			t.Fatalf("%s 的响应体不对: got=%q want=%q", tc.path, got, tc.body)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s 必须带 cache-control: no-cache，实际 %q", tc.path, got)
		}
		if got := rec.Header().Get("ETag"); !strings.HasPrefix(got, `W/"`) {
			t.Fatalf("%s 的 ETag 必须是弱 ETag，实际 %q", tc.path, got)
		}
	}
}

// TestServeAssetRevalidation 钉住 304：同一个 ETag 回来就是空 304。
func TestServeAssetRevalidation(t *testing.T) {
	front, err := New(Config{Dir: writeDist(t, map[string]string{
		IndexFile:     officialIndex,
		"assets/a.js": "console.log(1)",
	})})
	if err != nil {
		t.Fatal(err)
	}
	handler := front.Wrap(http.NotFoundHandler())

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/assets/a.js", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("第一次响应必须带 ETag")
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/assets/a.js", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, req)
	if second.Code != http.StatusNotModified {
		t.Fatalf("ETag 命中应 304，实际 %d", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 不该有响应体")
	}
}

// TestServeAssetPrecompressed 钉住预压变体：**不动态压缩**，直接吐 .gz/.br。
func TestServeAssetPrecompressed(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte("console.log(1)")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	gzipped := buf.String()

	front, err := New(Config{Dir: writeDist(t, map[string]string{
		IndexFile:     officialIndex,
		"assets/a.js": "console.log(1)",
		// 预压文件的内容故意与原文不同：谁发错了字节，一眼就能看出来。
		"assets/a.js.gz": gzipped,
		"assets/b.js":    "plain-b",
		"assets/b.js.gz": gzipped,
	})})
	if err != nil {
		t.Fatal(err)
	}
	handler := front.Wrap(http.NotFoundHandler())

	// 1) 客户端支持 gzip → 发 .gz，并标 Content-Encoding。
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/assets/a.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("必须直接吐预压文件并标 gzip，实际 %q", got)
	}
	if got := rec.Body.String(); got != gzipped {
		t.Fatalf("响应体必须是预压字节（不是现压出来的），实际 %d 字节", len(got))
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("预压响应的 Content-Type 必须按原扩展名，实际 %q", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("预压响应必须 Vary: Accept-Encoding，实际 %q", got)
	}

	// 2) 客户端明确拒绝（q=0）→ 发原文。
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/assets/a.js", nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("q=0 是明确拒绝，不许发压缩体")
	}
	if got := rec.Body.String(); got != "console.log(1)" {
		t.Fatalf("q=0 必须发原文，实际 %q", got)
	}

	// 3) 没带 Accept-Encoding → 发原文。
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/assets/b.js", nil))
	if got := rec.Body.String(); got != "plain-b" {
		t.Fatalf("没声明编码时必须发原文，实际 %q", got)
	}
}

// TestServeTraversalForbidden 钉住遍历围栏：越界是 403，不是 404。
func TestServeTraversalForbidden(t *testing.T) {
	front := newTestFrontend(t)
	handler := front.Wrap(http.NotFoundHandler())
	for _, path := range []string{
		"/../secret.txt",
		"/assets/../../secret.txt",
		// 百分号编码：net/url 解码后仍然是 `..`。
		"/%2e%2e/secret.txt",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1"+path, nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 必须 403，实际 %d", path, rec.Code)
		}
	}
}

// TestServeSymlinkEscapeForbidden 钉住软链逃逸：路径在根内，读出去的却是根外。
//
// 没有这一道，dist 里放一个指向 /etc 的软链就能读宿主上的任意文件。
// Windows 上建软链需要开发者模式/管理员权限，拿不到就跳过（并说明原因），
// 不能假装通过。
func TestServeSymlinkEscapeForbidden(t *testing.T) {
	dir := writeDist(t, map[string]string{IndexFile: officialIndex})
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("本机不能建符号链接（%v），这一条未验证", err)
	}
	front, err := New(Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/escape.txt", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("软链逃逸必须 403，实际 %d（body=%q）", rec.Code, rec.Body.String())
	}
}

// TestServeAbsolutePathStaysInside 钉住绝对路径不会跳出 dist。
//
// 官方把 URL 路径直接 `join` 到 dist 根下面，所以 `/C:/Windows/win.ini`
// 这类路径必须变成 dist 里的一条**不存在**路径（404），而不是宿主的真
// 文件。UNC 形状（`//server/share`）同理。
func TestServeAbsolutePathStaysInside(t *testing.T) {
	front := newTestFrontend(t)
	handler := front.Wrap(http.NotFoundHandler())
	for _, path := range []string{"/C:/Windows/win.ini", "/etc/passwd", "//server/share/x"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1"+path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 必须是 dist 内的 404，实际 %d（body=%q）", path, rec.Code, rec.Body.String())
		}
	}
}

// TestServeMissingAndDirectoryAreEmpty404 钉住"没有 SPA 回退"。
//
// 这一条是官方的显式设计：错拼的路径必须是显式失败，不能悄悄给一份
// index——否则"资源 404"永远查不出来（页面看起来能开，功能全坏）。
func TestServeMissingAndDirectoryAreEmpty404(t *testing.T) {
	front, err := New(Config{Dir: writeDist(t, map[string]string{
		IndexFile:     officialIndex,
		"assets/a.js": "x",
	})})
	if err != nil {
		t.Fatal(err)
	}
	handler := front.Wrap(http.NotFoundHandler())
	for _, path := range []string{"/nope", "/assets", "/assets/"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1"+path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s 必须 404，实际 %d", path, rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("%s 的 404 必须是空体，实际 %q", path, rec.Body.String())
		}
	}
}

// TestServeMethodNotAllowed 钉住非 GET/HEAD → 405 + Allow。
func TestServeMethodNotAllowed(t *testing.T) {
	front := newTestFrontend(t)
	rec := httptest.NewRecorder()
	front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST / 必须 405，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("405 必须带 Allow: GET, HEAD，实际 %q", got)
	}
}

// TestServeIndexRenderFailureIsLoud 钉住运行期渲染失败 → 500 + 缺哪个锚点。
//
// 正常路径上 New 已经把缺锚点挡在启动期；这里直接造一个锚点被抹掉的
// 实例，证明**就算走到运行期**也不会静默发一份没注入的 200。
func TestServeIndexRenderFailureIsLoud(t *testing.T) {
	dir := writeDist(t, map[string]string{IndexFile: officialIndex})
	// 手工造实例也要走 New 的那一步符号链接求值：TempDir 在 Windows 上是
	// 8.3 短名（ADMINI~1），不求值的话围栏会把它判成越界——那测到的
	// 就不是"缺锚点"而是"路径不对"。
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	front := &Frontend{
		root:          resolved,
		indexPath:     filepath.Join(resolved, IndexFile),
		indexHTML:     "<div id=\"root\"></div>", // 故意没有 <head>
		recovery:      DefaultRecoveryConfig(),
		streamBaseURL: defaultStreamBaseURL,
	}
	rec := httptest.NewRecorder()
	front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("缺锚点必须 500，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<head>") {
		t.Fatalf("500 响应必须说清缺哪个锚点，实际 %q", rec.Body.String())
	}
}

// TestNamedRoutesDelegated 钉住具名路由不被静态站吞掉。
//
// 少一条，界面看到的就是"接口全 404"——而 /api 的 404 与静态站的 404
// 长得一模一样，查起来要命。
func TestNamedRoutesDelegated(t *testing.T) {
	front := newTestFrontend(t)
	var seen []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	handler := front.Wrap(next)
	for _, path := range []string{"/api/session.list", "/api/remote.mux", "/connect", "/ws", "/debug/stats"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1"+path, nil))
		if rec.Code != http.StatusTeapot {
			t.Fatalf("%s 必须交给下一棒，实际 %d", path, rec.Code)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("具名路由必须全部转交，实际只转了 %v", seen)
	}
}

// TestTokenRewritesToConnect 钉住 `GET /?token=` 走 /connect 那条路。
//
// 官方 BrowserAuth.authorizeIndex 的语义是"根路径带一次性 token →
// 303 + Set-Cookie + Location ./"。本宿主复用 /connect 的实现（同一份
// 一次性 token 与 cookie），所以这里断言的是**路径被改写、query 原样**。
func TestTokenRewritesToConnect(t *testing.T) {
	front := newTestFrontend(t)
	var gotPath, gotToken, gotRawQuery string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.URL.Query().Get("token")
		gotRawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusSeeOther)
	})
	rec := httptest.NewRecorder()
	front.Wrap(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/?token=abc", nil))
	if gotPath != "/connect" {
		t.Fatalf("必须改写成 /connect，实际 %q", gotPath)
	}
	if gotToken != "abc" || !strings.Contains(gotRawQuery, "token=abc") {
		t.Fatalf("token 必须原样带过去，实际 path=%q raw=%q", gotPath, gotRawQuery)
	}
	// 原始请求不能被就地改坏（包装层不许留副作用）。
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/?token=abc", nil)
	rec = httptest.NewRecorder()
	front.Wrap(next).ServeHTTP(rec, req)
	if req.URL.Path != "/" {
		t.Fatalf("请求对象被就地改写了: %q", req.URL.Path)
	}
}

// TestMultipleTokensServeIndex 钉住"多个 token 不换 cookie"。
//
// 官方只认恰好一个 token（`tokens.length === 1`）；多给几个是可疑输入，
// 不能拿它去换 cookie。
func TestMultipleTokensServeIndex(t *testing.T) {
	front := newTestFrontend(t)
	var delegated bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { delegated = true })
	rec := httptest.NewRecorder()
	front.Wrap(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/?token=a&token=b", nil))
	if delegated {
		t.Fatalf("多个 token 不该走 /connect")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "__DSH_TRANSPORT__") {
		t.Fatalf("多个 token 应按普通 index 请求处理，实际 %d", rec.Code)
	}
}

// TestContentTypeTable 钉住官方 MIME 表（含"未知即 octet-stream"）。
func TestContentTypeTable(t *testing.T) {
	for name, want := range map[string]string{
		"index.html":           "text/html; charset=utf-8",
		"a.js":                 "text/javascript; charset=utf-8",
		"a.css":                "text/css; charset=utf-8",
		"a.png":                "image/png",
		"a.svg":                "image/svg+xml",
		"a.json":               "application/json",
		"a.map":                "application/json",
		"manifest.webmanifest": "application/manifest+json",
		"a.gz":                 "application/gzip",
		// 官方表里没有的：字体与图标都回落，不猜。
		"a.woff2": "application/octet-stream",
		"a.ico":   "application/octet-stream",
		"a.bin":   "application/octet-stream",
		"a":       "application/octet-stream",
	} {
		if got := contentType(name); got != want {
			t.Fatalf("%s 的 Content-Type: got=%q want=%q", name, got, want)
		}
	}
}

// TestAcceptsEncoding 钉住 Accept-Encoding 的四种形状。
func TestAcceptsEncoding(t *testing.T) {
	for _, tc := range []struct {
		header string
		enc    string
		want   bool
	}{
		{"gzip", "gzip", true},
		{"gzip, deflate, br", "br", true},
		{"gzip;q=0", "gzip", false},
		{"gzip;q=0.5", "gzip", true},
		{"", "gzip", false},
		{"identity", "gzip", false},
		{"*", "br", true},
		{"*;q=0", "br", false},
		{"GZIP", "gzip", true},
	} {
		if got := acceptsEncoding(tc.header, tc.enc); got != tc.want {
			t.Fatalf("acceptsEncoding(%q, %q): got=%v want=%v", tc.header, tc.enc, got, tc.want)
		}
	}
}

// countingWriter 只数不存。
//
// 为什么不用 httptest.ResponseRecorder：它把整个响应体存在自己里，1 MiB
// 的资产就会被"测试夹具"完整分配一次——那样量到的分配跟 handler 无关，
// 断言必然失败或必然通过，两种都没有意义。
type countingWriter struct {
	header     http.Header
	status     int
	written    int64
	maxWrite   int
	writeCalls int
}

func (w *countingWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *countingWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if len(p) > w.maxWrite {
		w.maxWrite = len(p)
	}
	w.written += int64(len(p))
	w.writeCalls++
	return len(p), nil
}

// TestAssetStreamsWithoutFullAllocation 是"零运行时内存增量"的硬断言。
//
// 两件事一起证：
//
//  1. 1 MiB 文件的响应过程中，进程堆的累计分配必须 **< 512 KiB**——
//     `os.ReadFile` 会一次分配 1 MiB，这里直接判死；
//  2. 单次 Write 不超过 64 KiB——证明是分块流式，而不是"读全了再写"。
//
// 阈值留了 2 倍余量：`io.Copy` 的 32 KiB 缓冲、`ServeContent` 的
// Content-Length 计算都会分配一点，而 GC/测试框架的并发分配也会计入
// TotalAlloc。真正的判据是"分配量必须**与文件大小无关**"，所以 1 MiB
// 的文件绝不该出现 1 MiB 级的分配。
func TestAssetStreamsWithoutFullAllocation(t *testing.T) {
	const size = 1 << 20 // 1 MiB
	dir := writeDist(t, map[string]string{IndexFile: officialIndex})
	big := filepath.Join(dir, "big.bin")
	writePattern(t, big, size)

	front, err := New(Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	handler := front.Wrap(http.NotFoundHandler())

	w := &countingWriter{}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/big.bin", nil)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	handler.ServeHTTP(w, req)
	runtime.ReadMemStats(&after)

	if w.written != size {
		t.Fatalf("响应体长度不对: got=%d want=%d", w.written, size)
	}
	delta := after.TotalAlloc - before.TotalAlloc
	// 把三个数打出来：断言失败时要能一眼看出"是整份分配还是分块"，
	// 而不是只知道超了阈值。
	t.Logf("1 MiB 资产：分配 %d 字节（阈值 %d），单次 Write 最大 %d 字节，共 %d 次",
		delta, size/2, w.maxWrite, w.writeCalls)
	if delta >= size/2 {
		t.Fatalf("1 MiB 资产整份进了堆：响应过程中分配 %d 字节（阈值 %d）", delta, size/2)
	}
	if w.maxWrite > 64<<10 {
		t.Fatalf("单次 Write %d 字节（阈值 64 KiB），不是流式发送", w.maxWrite)
	}
	if w.writeCalls < 2 {
		t.Fatalf("只写了 %d 次，1 MiB 不可能是流式的", w.writeCalls)
	}
}

// writePattern 用 32 KiB 一块写完一个确定内容的大文件。
func writePattern(t *testing.T, path string, size int) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("建 %s 失败: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	chunk := bytes.Repeat([]byte("0123456789abcdef"), 2048) // 32 KiB
	for left := size; left > 0; {
		n := len(chunk)
		if n > left {
			n = left
		}
		if _, err := file.Write(chunk[:n]); err != nil {
			t.Fatalf("写 %s 失败: %v", path, err)
		}
		left -= n
	}
}
