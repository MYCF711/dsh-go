package main

// boot 三件套的生产接线测试（`-run 'Frontend|BootFacade'` 会连这里一起跑）。
//
// 与 `serve_frontend_test.go` 的分工：那边钉静态站本身（404/403/405、token、
// 资产直发、具名路由），这边钉**注入的那三条**——门面、图、就绪尾脚本——
// 以及"关掉之后逐字节回到旧形状"。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/transport"
	"dshgo/internal/webfrontend"
)

// bootDistFiles 是这一组测试共用的 boot 载荷：一条 bootstrap（官方模块系统
// 实现）+ 一条 application 批次，URL 都是 dist 内路径。
const bootManifestJSON = `{
  "rev": "208adc5149be",
  "release": {"version": "0.2.2-rc.1"},
  "entries": [
    {"id": "@deepseek-ai/dsh-client-modules", "url": "plugins/b0.js?rev=582d45f3fd37", "rev": "582d45f3fd37", "immediately": true},
    {"id": "@deepseek-ai/dsh-client-ui-renderer", "url": "plugins/b1.js?rev=f530bf6121e8", "rev": "f530bf6121e8", "inject": ["@deepseek-ai/dsh-client-modules"]}
  ],
  "batches": [
    {"phase": "bootstrap", "url": "plugins/b0.js?rev=582d45f3fd37", "rev": "582d45f3fd37", "entries": ["@deepseek-ai/dsh-client-modules"]},
    {"phase": "application", "url": "plugins/b1.js?rev=f530bf6121e8", "rev": "f530bf6121e8", "entries": ["@deepseek-ai/dsh-client-ui-renderer"]}
  ]
}`

// writeBootDist 造一份带 boot 载荷的 dist；manifest 为空则不写 dsh-boot.json。
func writeBootDist(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":          officialIndexHTML,
		"assets/index-app.js": "console.log('spa')",
		"plugins/b0.js":       `window.__ModuleLoader__.load({id:"@deepseek-ai/dsh-client-modules",factory:()=>({})})`,
		"plugins/b1.js":       `window.__ModuleLoader__.load({id:"@deepseek-ai/dsh-client-ui-renderer",factory:()=>({})})`,
	}
	if manifest != "" {
		files["dsh-boot.json"] = manifest
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

// bootHandler 按生产接线（frontendMount）返回 handler。
//
// 刻意走 transport.NewServer 而不是裸 front：真机上静态站就是套在 Server
// 外面的那一层，跳过去会漏掉"具名路由没被吞"这类只有组合起来才看得见的事。
func bootHandler(t *testing.T, dir string) (http.Handler, *webfrontend.Frontend) {
	t.Helper()
	t.Setenv(frontendDirEnv, dir)
	front, err := newBootFacadeFrontend()
	if err != nil {
		t.Fatalf("建前端失败: %v", err)
	}
	if front == nil {
		t.Fatalf("目录在，前端必须启用")
	}
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("建 transport 失败: %v", err)
	}
	wrap := bootFacadeMount(front, nil)
	if wrap == nil {
		t.Fatalf("启用前端必须有 wrap")
	}
	return wrap(srv), front
}

// TestBootFacadeInjectsThreeGlobals 是这次交付的主断言：
// GET / 同时带门面、图、就绪尾脚本，且都落在入口模组之前。
func TestBootFacadeInjectsThreeGlobals(t *testing.T) {
	handler, front := bootHandler(t, writeBootDist(t, bootManifestJSON))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / 应 200，实际 %d（body=%q）", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type 不对: %q", got)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"window.__ModuleLoader__=",
		`globalThis["__DSH_BOOT__"] = {"rev":"208adc5149be"`,
		"__DSH_BOOT_READY__",
		`<script src="plugins/b0.js?rev=582d45f3fd37"></script>`,
		`<link rel="preload" as="script" href="plugins/b1.js?rev=f530bf6121e8">`,
		// 两条既有全局必须还在（历史接线不许被这次改动挤掉）。
		`globalThis["__DSH_CONNECTION_RECOVERY__"] = {"backoffBaseMs":500,`,
		`globalThis["__DSH_TRANSPORT__"] = {"ownsHost":true,"streamBaseUrl":"http://127.0.0.1:4321"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("入口文档缺 %q\n实际: %s", want, body)
		}
	}

	// 门面与图必须在入口模组之前（否则入口跑在读全局之前）。
	// 就绪尾脚本是**例外**：官方把它放在 body 尾，而入口是 type="module"
	// （延迟执行），所以文档顺序上尾脚本在后、实际执行在前。这条语义由
	// 下面那条"必须在 <body> 之后"单独钉。
	entryAt := strings.Index(body, "index-BgUyXZcc.js")
	for _, marker := range []string{"window.__ModuleLoader__=", `globalThis["__DSH_BOOT__"]`} {
		if at := strings.Index(body, marker); at < 0 || at > entryAt {
			t.Fatalf("%q 必须出现在入口模组之前（at=%d, entry=%d）", marker, at, entryAt)
		}
	}
	// 就绪尾脚本在 body 之后：入口模组 await 它，位置错了就等不到。
	if strings.Index(body, "__DSH_BOOT_READY__") < strings.Index(body, "<body>") {
		t.Fatalf("就绪尾脚本必须落在 <body> 之后")
	}

	// 图里点名的每条 URL 都必须真的能取到（图写了 404 = 页面报 chunk 加载失败）。
	var graph webfrontend.BootGraph
	raw := body[strings.Index(body, `globalThis["__DSH_BOOT__"] = `)+len(`globalThis["__DSH_BOOT__"] = `):]
	raw = raw[:strings.Index(raw, "</script>")]
	if err := json.Unmarshal([]byte(raw), &graph); err != nil {
		t.Fatalf("注入的图不是合法 JSON: %v\n原文: %s", err, raw)
	}
	if len(graph.Entries) != 2 || len(graph.Batches) != 2 {
		t.Fatalf("图条数不对: entries=%d batches=%d", len(graph.Entries), len(graph.Batches))
	}
	for _, batch := range graph.Batches {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/"+batch.URL, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("批次 %s 必须 200，实际 %d", batch.URL, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
			t.Fatalf("批次 %s 的 Content-Type 不对: %q", batch.URL, got)
		}
		if !strings.Contains(rec.Body.String(), "__ModuleLoader__.load") {
			t.Fatalf("批次 %s 的字节不对: %q", batch.URL, rec.Body.String())
		}
	}
	if description, ok := front.BootDescription(); !ok || !strings.Contains(description, "entries=2") {
		t.Fatalf("启动描述不对: %q ok=%v", description, ok)
	}
}

// TestBootFacadeEnvSwitch 钉住开关：默认开、`0` 关、拼错报错。
func TestBootFacadeEnvSwitch(t *testing.T) {
	cases := []struct {
		env      string
		wantErr  string
		wantBoot bool
	}{
		{env: "", wantBoot: true},
		{env: "1", wantBoot: true},
		{env: "on", wantBoot: true},
		{env: "0", wantBoot: false},
		{env: "off", wantBoot: false},
		{env: "FALSE", wantBoot: false},
		{env: "of", wantErr: "不是开关值"},
	}
	for _, tc := range cases {
		t.Run("DSHD_BOOT_FACADE="+tc.env, func(t *testing.T) {
			t.Setenv(bootFacadeEnv, tc.env)
			options, err := bootFacadeOptions()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("必须报 %q，实际 %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if options.Disabled == tc.wantBoot {
				t.Fatalf("Disabled=%v，期望 boot=%v", options.Disabled, tc.wantBoot)
			}
		})
	}
}

// TestBootFacadeDisabledMatchesLegacy 钉住关掉之后 GET / 与旧接线逐字节相同。
func TestBootFacadeDisabledMatchesLegacy(t *testing.T) {
	dir := writeBootDist(t, bootManifestJSON)

	t.Setenv(frontendDirEnv, dir)
	t.Setenv(bootFacadeEnv, "0")
	disabled, err := newBootFacadeFrontend()
	if err != nil {
		t.Fatalf("建前端失败: %v", err)
	}
	legacy, err := newFrontend()
	if err != nil {
		t.Fatalf("建旧前端失败: %v", err)
	}
	fetch := func(front *webfrontend.Frontend) string {
		rec := httptest.NewRecorder()
		front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / 应 200，实际 %d", rec.Code)
		}
		return rec.Body.String()
	}
	// 两条路径都必须只注入历史的两条全局（新接线默认是开的，所以这里
	// 的对照对象是"关掉开关"与"RenderIndex 的旧形状"）。
	got := fetch(disabled)
	if strings.Contains(got, "__ModuleLoader__") || strings.Contains(got, "__DSH_BOOT_READY__") || strings.Contains(got, `__DSH_BOOT__`) {
		t.Fatalf("关掉之后不该出现 boot 三件套\n实际: %s", got)
	}
	// legacy 这一份（newFrontend）默认是**开**的：它必须带三件套，
	// 证明"默认开启"这条判据在 serve_frontend.go 那条老接线里也成立。
	if want := fetch(legacy); !strings.Contains(want, "window.__ModuleLoader__=") {
		t.Fatalf("newFrontend 必须默认带 boot 三件套（页面才起得来）\n实际: %s", want)
	}
}

// TestBootFacadeMissingBodyAnchorFailsStartup 钉住"缺 body 锚点在启动时炸"。
func TestBootFacadeMissingBodyAnchorFailsStartup(t *testing.T) {
	dir := t.TempDir()
	// 只有 head，没有 body：boot 的就绪尾脚本没有落点。
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><head></head><div id=\"root\"></div></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(frontendDirEnv, dir)
	t.Setenv(bootFacadeEnv, "")
	if _, err := newBootFacadeFrontend(); err == nil {
		t.Fatalf("缺 <body> 必须让宿主起不来（不许发一份没注入的页面）")
	} else if !strings.Contains(err.Error(), "<body>") {
		t.Fatalf("错误必须点名 <body>，实际 %q", err.Error())
	}
}

// TestBootFacadeNoManifestStillServesThree 钉住"裸 dist 也能过 facade missing"。
func TestBootFacadeNoManifestStillServesThree(t *testing.T) {
	handler, front := bootHandler(t, writeBootDist(t, ""))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/", nil))
	body := rec.Body.String()
	for _, want := range []string{"window.__ModuleLoader__=", `globalThis["__DSH_BOOT__"] = {`, "__DSH_BOOT_READY__", "__DSH_BOOT_DIAGNOSIS__"} {
		if !strings.Contains(body, want) {
			t.Fatalf("裸 dist 缺 %q\n实际: %s", want, body)
		}
	}
	if description, ok := front.BootDescription(); !ok || !strings.Contains(description, webfrontend.BootManifestFile) {
		t.Fatalf("启动描述必须点名缺哪份清单，实际 %q ok=%v", description, ok)
	}
}

// TestBootFacadeNamedRoutesUntouched 钉住 boot 注入没有把接口路径吃掉。
func TestBootFacadeNamedRoutesUntouched(t *testing.T) {
	handler, _ := bootHandler(t, writeBootDist(t, bootManifestJSON))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4321/api/session.list", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("没 cookie 的 /api 必须 401（transport 的认证），实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("/api 必须回 JSON，实际 %q", got)
	}
}
