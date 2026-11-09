package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/transport"
	"dshgo/internal/webfrontend"
)

// officialIndexHTML 是官方 dist 的 index.html（逐字取自 app.asar 的
// `@deepseek-ai/dsh-web-frontend/dist/index.html`）。
const officialIndexHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>DeepSeek Harness</title>
    <script type="module" crossorigin src="./assets/index-BgUyXZcc.js"></script>
    <link rel="stylesheet" crossorigin href="./assets/index-lh_YM6sT.css">
  </head>
  <body>
    <div id="root"></div>
  </body>
</html>
`

// writeFrontendDist 造一份最小 dist 并返回根目录。
func writeFrontendDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":           officialIndexHTML,
		"assets/index-app.js":  "console.log('spa')",
		"assets/index-app.css": "body{margin:0}",
	}
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("写 %s 失败: %v", name, err)
		}
	}
	return dir
}

// startFrontendServer 按生产接线方式（Options.HandlerWrap）起一个服务。
//
// 这里刻意复刻 `transport.Server.Start` 里的那三行：只有 wrap 非 nil 才
// 换成它。测试拿到的 handler 与真机上是同一个对象链。
func startFrontendServer(t *testing.T, dir string) (*transport.Server, http.Handler) {
	t.Helper()
	t.Setenv(frontendDirEnv, dir)
	front, err := newFrontend()
	if err != nil {
		t.Fatalf("建前端失败: %v", err)
	}
	srv, err := transport.NewServer(transport.Options{HandlerWrap: frontendHandlerWrap(front)})
	if err != nil {
		t.Fatalf("建 transport 失败: %v", err)
	}
	var handler http.Handler = srv
	if wrap := frontendHandlerWrap(front); wrap != nil {
		if wrapped := wrap(srv); wrapped != nil {
			handler = wrapped
		}
	}
	return srv, handler
}

// TestFrontendDisabledByDefault 钉住"未设置就一个字节都不变"。
//
// 这是默认关闭路径的唯一判据：没设 DSHD_FRONTEND_DIR 时，`GET /` 必须
// 还是原来那条 404 JSON，且与"根本没接线"逐字节相同。
func TestFrontendDisabledByDefault(t *testing.T) {
	t.Setenv(frontendDirEnv, "")
	front, err := newFrontend()
	if err != nil {
		t.Fatalf("未设置时不该报错: %v", err)
	}
	if front != nil {
		t.Fatalf("未设置 %s 时前端必须关闭", frontendDirEnv)
	}
	if wrap := frontendHandlerWrap(front); wrap != nil {
		t.Fatalf("未启用时 HandlerWrap 必须是 nil（Start 才会走原 handler）")
	}

	plain, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wired, err := transport.NewServer(transport.Options{HandlerWrap: frontendHandlerWrap(front)})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/index.html", "/assets/index-app.js", "/?token=x"} {
		before := httptest.NewRecorder()
		plain.ServeHTTP(before, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1"+path, nil))
		after := httptest.NewRecorder()
		wired.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1"+path, nil))
		if before.Code != after.Code || before.Body.String() != after.Body.String() {
			t.Fatalf("%s 的响应在被"+
				"接线后变了: %d/%q → %d/%q", path,
				before.Code, before.Body.String(), after.Code, after.Body.String())
		}
	}
}

// TestFrontendMissingDirStaysDisabled 钉住"目录不存在 = 关闭"。
func TestFrontendMissingDirStaysDisabled(t *testing.T) {
	t.Setenv(frontendDirEnv, filepath.Join(t.TempDir(), "does-not-exist"))
	front, err := newFrontend()
	if err != nil {
		t.Fatalf("目录不存在不该让宿主起不来: %v", err)
	}
	if front != nil {
		t.Fatalf("目录不存在时必须关闭前端")
	}
}

// TestFrontendServesSPA 钉住"官方 SPA 能加载"。
func TestFrontendServesSPA(t *testing.T) {
	_, handler := startFrontendServer(t, writeFrontendDist(t))

	// 1) GET / 必须是注入后的 HTML。
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / 应 200，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type 不对: %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<script>globalThis["__DSH_CONNECTION_RECOVERY__"] = {"backoffBaseMs":500,"backoffFactor":2,`,
		`<script>globalThis["__DSH_TRANSPORT__"] = {"ownsHost":true,"streamBaseUrl":"http://127.0.0.1:4321"}</script>`,
		`<div id="root"></div>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("入口文档缺 %q\n实际: %s", want, body)
		}
	}
	// 注入必须落在 <head> 之后、原文之前（否则入口模组跑在读全局之前）。
	if strings.Index(body, "__DSH_TRANSPORT__") > strings.Index(body, "index-BgUyXZcc.js") {
		t.Fatalf("注入必须在入口 <script> 之前")
	}

	// 2) 资产必须按扩展名直发。
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/assets/index-app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 资产应 200，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("资产 Content-Type 不对: %q", got)
	}
	if got := rec.Body.String(); got != "console.log('spa')" {
		t.Fatalf("资产字节不对: %q", got)
	}
}

// TestFrontendTokenExchangesCookie 钉住 `GET /?token=` → 303 + cookie。
//
// 语义与既有 /connect 完全一致（一次性 token、HttpOnly cookie、303 到 /），
// 所以这里同时断言：换过之后再拿同一个 token 是 401（一次性）。
func TestFrontendTokenExchangesCookie(t *testing.T) {
	srv, handler := startFrontendServer(t, writeFrontendDist(t))
	token := srv.Token()
	if token == "" {
		t.Fatalf("transport 必须给出 launch token")
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/?token="+token, nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("带 token 的 GET / 必须 303，实际 %d（body=%q）", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("Location 必须是 /（与 /connect 一致），实际 %q", got)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("必须发一份 cookie，实际 %d", len(cookies))
	}
	if !cookies[0].HttpOnly {
		t.Fatalf("cookie 必须是 HttpOnly")
	}

	// 带 cookie 再打开 / → 200 且带注入。
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("带 cookie 的 GET / 应 200，实际 %d", rec.Code)
	}

	// 同一个 token 只能换一次。
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/?token="+token, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token 是一次性的，第二次必须 401，实际 %d", rec.Code)
	}
}

// TestFrontendNamedRoutesStillTransport 钉住接口路径没被静态站吞掉。
//
// 静态站的 404 与 /api 的 404 长得一样，吞掉的后果是"界面所有接口
// 都 404"——只有断言响应体是 transport 的四字段错误体才分得清。
func TestFrontendNamedRoutesStillTransport(t *testing.T) {
	_, handler := startFrontendServer(t, writeFrontendDist(t))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4321/api/session.list", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("没 cookie 的 /api 必须 401（transport 的认证），实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("/api 必须回 JSON，实际 %q", got)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/ws", nil))
	if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/html") {
		t.Fatalf("/ws 不许被静态站接走")
	}
}

// TestFrontendHandlerWrapNilIsNoop 钉住 wrap(nil) 就是 nil。
func TestFrontendHandlerWrapNilIsNoop(t *testing.T) {
	if wrap := frontendHandlerWrap(nil); wrap != nil {
		t.Fatalf("nil 前端必须得到 nil wrap")
	}
	// 非 nil 前端必须产出非 nil handler。
	front, err := webfrontend.New(webfrontend.Config{Dir: writeFrontendDist(t)})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := frontendHandlerWrap(front)(srv); got == nil {
		t.Fatalf("启用前端必须产出 handler")
	}
}

// TestFrontendOverRealListener 用**真监听**跑一遍（不是 httptest 假连接）。
//
// 为什么单独要这一条：httptest 直接调 handler，跳过了 `net.Listen` 与
// 真正的 HTTP 客户端 —— 而 `http.ServeContent` 的 Content-Length /
// Range / If-None-Match 全在连接层才有意义。真监听能抓到"handler 绿、
// 线上红"的那一类问题。
//
// 设了 M0_OFFICIAL_DIST 时改用**官方 dist 真目录**跑（真机证据）；
// 没设就跳过，不让默认门禁依赖 app.asar。
func TestFrontendOverRealListener(t *testing.T) {
	dir := os.Getenv("M0_OFFICIAL_DIST")
	official := dir != ""
	if !official {
		dir = writeFrontendDist(t)
	}
	srv, handler := startFrontendServer(t, dir)
	if official {
		// 真监听；生产接线只有 HandlerWrap 一条路，这里用 Server 自己。
		url, err := srv.Start()
		if err != nil {
			t.Fatalf("监听失败: %v", err)
		}
		defer func() { _ = srv.Close() }()

		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Get(url + "/")
		if err != nil {
			t.Fatalf("GET / 失败: %v", err)
		}
		body := make([]byte, 0, 4096)
		buf := make([]byte, 4096)
		for {
			n, err := response.Body.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil {
				break
			}
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("真监听 GET / 应 200，实际 %d", response.StatusCode)
		}
		if !strings.Contains(string(body), `globalThis["__DSH_TRANSPORT__"]`) {
			t.Fatalf("真监听下入口文档没有注入")
		}

		// 真实 303 + cookie。
		response, err = client.Get(url + "/?token=" + srv.Token())
		if err != nil {
			t.Fatalf("GET /?token= 失败: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("真监听 GET /?token= 应 303，实际 %d", response.StatusCode)
		}
		if len(response.Cookies()) != 1 {
			t.Fatalf("真监听没发 cookie")
		}

		// 真实入口 bundle（970 KB 级）必须 200 且是 JS。
		response, err = client.Get(url + "/assets/index-BgUyXZcc.js")
		if err != nil {
			t.Fatalf("GET 入口 bundle 失败: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("官方入口 bundle 应 200，实际 %d", response.StatusCode)
		}
		if got := response.Header.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
			t.Fatalf("入口 bundle 的 Content-Type 不对: %q", got)
		}
		return
	}

	// 默认门禁：假连接下把同一条链路走完。
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "__DSH_TRANSPORT__") {
		t.Fatalf("默认 dist 的 GET / 不对: %d", rec.Code)
	}
}

// TestFrontendProbeRealServer 是**手动**真机探针：设 M0_PROBE_LISTEN=1
// 时真起监听、把 URL 与 launch token 写进 M0_PROBE_READY 指定的文件，
// 然后挂 60 秒等外部 curl 复核。默认跳过，不进任何门禁。
//
// 为什么要它：真机证据要的是"从内核 socket 读回来的字节"，而不是
// handler 的返回值。这个探针就是那条连接的另一端。
func TestFrontendProbeRealServer(t *testing.T) {
	if os.Getenv("M0_PROBE_LISTEN") != "1" {
		t.Skip("手动探针：设 M0_PROBE_LISTEN=1 才起真监听")
	}
	dir := os.Getenv("M0_OFFICIAL_DIST")
	if dir == "" {
		t.Fatalf("探针需要 M0_OFFICIAL_DIST 指向官方 dist")
	}
	ready := os.Getenv("M0_PROBE_READY")
	if ready == "" {
		t.Fatalf("探针需要 M0_PROBE_READY 指向 ready 文件")
	}
	srv, _ := startFrontendServer(t, dir)
	url, err := srv.Start()
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer func() { _ = srv.Close() }()
	line := "url=" + url + "\ntoken=" + srv.Token() + "\n"
	if err := os.WriteFile(ready, []byte(line), 0o644); err != nil {
		t.Fatalf("写 ready 文件失败: %v", err)
	}
	time.Sleep(60 * time.Second)
}

// TestFrontendMountComposesWithInner 钉住"两个开关正交"。
//
// 生产接线是 `frontendMount(front, officialWireMount(deps))`：
// 前端在外层、官方 wire 在内层。四种组合（前端开/关 × wire 开/关）
// 都必须成立，少一种就是"开了前端之后官方 wire 的路径全 404"。
func TestFrontendMountComposesWithInner(t *testing.T) {
	dir := writeFrontendDist(t)
	inner := func(*transport.Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Inner", "1")
			w.WriteHeader(http.StatusTeapot)
		})
	}

	// 前端开：/ 由静态站接，/api/* 落到内层。
	front, err := webfrontend.New(webfrontend.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	handler := frontendMount(front, inner)(srv)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "__DSH_TRANSPORT__") {
		t.Fatalf("前端开启时 / 必须由静态站接，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/api/remote.mux", nil))
	if rec.Code != http.StatusTeapot || rec.Header().Get("X-Inner") != "1" {
		t.Fatalf("具名路由必须落到内层，实际 %d", rec.Code)
	}

	// 前端关：内层原样生效。
	handler = frontendMount(nil, inner)(srv)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("前端关闭时内层必须原样生效，实际 %d", rec.Code)
	}

	// 内层关、前端关：就是 Server 自己。
	handler = frontendMount(nil, nil)(srv)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("两个开关都关时必须回到既有 404，实际 %d", rec.Code)
	}
}
