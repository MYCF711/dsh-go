package webfrontend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bootFixture 造一份带 boot 载荷的 dist。
//
// manifest 为空 = 不写 dsh-boot.json（裸官方 dist 那一档）。
func bootFixture(t *testing.T, manifest string, files map[string]string) string {
	t.Helper()
	all := map[string]string{IndexFile: officialIndex}
	for name, content := range files {
		all[name] = content
	}
	if manifest != "" {
		all[BootManifestFile] = manifest
	}
	return writeDist(t, all)
}

// officialBootManifest 是官方 wire 形状的最小实例：
// 一条 bootstrap（含模块系统实现）+ 一条 application 批次。
const officialBootManifest = `{
  "rev": "208adc5149be",
  "release": {"version": "0.2.2-rc.1", "commit": "c59d10a"},
  "entries": [
    {"id": "@deepseek-ai/dsh-client-modules", "url": "plugins/b0.js?rev=582d45f3fd37", "rev": "582d45f3fd37", "inject": [], "immediately": true},
    {"id": "@deepseek-ai/dsh-client-ui-renderer", "url": "plugins/b1.js?rev=f530bf6121e8", "rev": "f530bf6121e8", "inject": ["@deepseek-ai/dsh-client-modules"], "external": ["@deepseek-ai/dsh-client-modules"]}
  ],
  "batches": [
    {"phase": "bootstrap", "url": "plugins/b0.js?rev=582d45f3fd37", "rev": "582d45f3fd37", "entries": ["@deepseek-ai/dsh-client-modules"]},
    {"phase": "application", "url": "plugins/b1.js?rev=f530bf6121e8", "rev": "f530bf6121e8", "entries": ["@deepseek-ai/dsh-client-ui-renderer"]}
  ]
}`

// bootPluginFiles 是上面那份清单点名的两个脚本。
func bootPluginFiles() map[string]string {
	return map[string]string{
		"plugins/b0.js": `window.__ModuleLoader__.load({id:"@deepseek-ai/dsh-client-modules",factory:()=>({})})`,
		"plugins/b1.js": `window.__ModuleLoader__.load({id:"@deepseek-ai/dsh-client-ui-renderer",factory:()=>({})})`,
	}
}

// fetchIndex 取一次 GET / 的响应体。
func fetchIndex(t *testing.T, front *Frontend) string {
	t.Helper()
	rec := httptest.NewRecorder()
	front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / 应 200，实际 %d（body=%q）", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestRenderIndexRowsTwoSections 钉住官方 renderIndexInjections 的两段语义。
//
// 官方（`@deepseek-ai/dsh-host-webserver` lib/index.js 的
// `renderIndexInjections`）：head 行插在开 `<head>` 之后、body 行插在开
// `<body>` 之后，就绪尾脚本追加在最后一条 body 行之后。这里同时断言
// **原文其余字节不动**（一次插入，不是重写文档）。
func TestRenderIndexRowsTwoSections(t *testing.T) {
	rows := []Row{
		{Kind: RowScript, Placement: PlacementHead, Text: "var headOnly=1"},
		{Kind: RowGlobal, Placement: PlacementHead, Name: "__X__", Value: map[string]int{"a": 1}},
		{Kind: RowScriptPreload, Placement: PlacementHead, Src: "plugins/b1.js?rev=1"},
		{Kind: RowScriptSrc, Placement: PlacementHead, Src: "plugins/b0.js?rev=2"},
	}
	got, err := RenderIndexRows(officialIndex, rows)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}

	const wantHead = `<script>var headOnly=1</script>` +
		`<script>globalThis["__X__"] = {"a":1}</script>` +
		`<link rel="preload" as="script" href="plugins/b1.js?rev=1">` +
		`<script src="plugins/b0.js?rev=2"></script>`
	headAt := strings.Index(officialIndex, "<head>") + len("<head>")
	bodyAt := strings.Index(officialIndex, "<body>") + len("<body>")

	want := officialIndex[:headAt] + wantHead + officialIndex[headAt:bodyAt] + readyMarkup + officialIndex[bodyAt:]
	if got != want {
		t.Fatalf("两段注入的位置/字节不对\n got: %s\nwant: %s", got, want)
	}
	// 就绪尾脚本必须在 <body> 之后、页面内容之前，且只有一条。
	if strings.Count(got, "__DSH_BOOT_READY__") != 1 {
		t.Fatalf("就绪尾脚本必须恰好一条，实际 %d", strings.Count(got, "__DSH_BOOT_READY__"))
	}
	if strings.Index(got, "__DSH_BOOT_READY__") < strings.Index(got, "<body>") {
		t.Fatalf("就绪尾脚本必须落在 <body> 之后")
	}
}

// TestRenderIndexRowsMissingBodyIsAnchorError 钉住"缺 body 锚点也响亮失败"。
func TestRenderIndexRowsMissingBodyIsAnchorError(t *testing.T) {
	_, err := RenderIndexRows("<html><head></head><div id=\"root\"></div></html>", []Row{
		{Kind: RowGlobal, Name: "__X__", Value: 1},
	})
	if err == nil {
		t.Fatalf("缺 <body> 必须报错")
	}
	var anchor *AnchorError
	if !asAnchorError(err, &anchor) {
		t.Fatalf("必须是 *AnchorError，实际 %T: %v", err, err)
	}
	if anchor.Anchor != "<body>" {
		t.Fatalf("必须点名 <body>，实际 %q", anchor.Anchor)
	}
	if !strings.Contains(err.Error(), "<body>") {
		t.Fatalf("错误文本必须带锚点字面量，实际 %q", err.Error())
	}
}

// asAnchorError 是 errors.As 的小包装（本包不引 errors 只为这一处可读性）。
func asAnchorError(err error, target **AnchorError) bool {
	for err != nil {
		if anchor, ok := err.(*AnchorError); ok {
			*target = anchor
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// TestBootInjectsFacadeGraphAndReady 是这次改动的核心断言：
// `GET /` 同时带上门面、模块图、就绪尾脚本，且顺序/位置与官方一致。
func TestBootInjectsFacadeGraphAndReady(t *testing.T) {
	front, err := New(Config{Dir: bootFixture(t, officialBootManifest, bootPluginFiles())})
	if err != nil {
		t.Fatalf("建前端失败: %v", err)
	}
	body := fetchIndex(t, front)

	// 1. 门面在最前（官方行序：queue → preload → bootstrap → graph）。
	facadeAt := strings.Index(body, "window.__ModuleLoader__=")
	if facadeAt < 0 {
		t.Fatalf("页面里没有门面\n实际: %s", body)
	}
	if facadeAt > strings.Index(body, `globalThis["__DSH_BOOT__"]`) {
		t.Fatalf("门面必须排在 __DSH_BOOT__ 之前（官方 bootInjections 的行序）")
	}
	// 门面必须是**官方那一份**：排队模式 + 那条官方报错文案。
	for _, want := range []string{
		`mode:"queue"`,
		`pendingQueue,`,
		`createClientModuleSystem(this,{id:registration.id,exports},options)`,
		"client-modules: HTML did not preload",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("门面缺官方契约片段 %q", want)
		}
	}
	// 退化分支的**代码**总在门面里（它是函数定义），但这条链路不该走它：
	// 图里必须有 bootstrap 批次，且该批次点名了提供模块系统的官方包。
	if front.boot.degraded {
		t.Fatalf("有 bootstrap batch 时不该被判成退化载荷")
	}
	found := false
	for _, batch := range front.boot.graph.Batches {
		if batch.Phase != BootPhaseBootstrap {
			continue
		}
		for _, id := range batch.Entries {
			if id == bootstrapClientModuleID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("bootstrap 批次必须点名 %s（模块系统由官方 bundle 提供）", bootstrapClientModuleID)
	}

	// 2. bootstrap 批次是阻塞 script-src，application 批次是 preload。
	if !strings.Contains(body, `<script src="plugins/b0.js?rev=582d45f3fd37"></script>`) {
		t.Fatalf("缺 bootstrap script-src 行\n实际: %s", body)
	}
	if !strings.Contains(body, `<link rel="preload" as="script" href="plugins/b1.js?rev=f530bf6121e8">`) {
		t.Fatalf("缺 application preload 行\n实际: %s", body)
	}

	// 3. 图必须是**逐字节**等于我们校验过的结构（字段顺序进 golden）。
	raw := body[strings.Index(body, `globalThis["__DSH_BOOT__"] = `)+len(`globalThis["__DSH_BOOT__"] = `):]
	raw = raw[:strings.Index(raw, "</script>")]
	want, err := json.Marshal(front.boot.graph)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.ReplaceAll(string(want), "<", `\u003c`); raw != got {
		t.Fatalf("图字节不一致\n got: %s\nwant: %s", raw, got)
	}
	for _, want := range []string{
		`"rev":"208adc5149be"`,
		`"release":{"version":"0.2.2-rc.1","commit":"c59d10a"}`,
		`"id":"@deepseek-ai/dsh-client-ui-renderer"`,
		`"immediately":true`,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("图缺 %q\n实际: %s", want, raw)
		}
	}

	// 4. 就绪尾脚本在 body 之后（入口模组靠它等"注入已落地"）。
	if strings.Index(body, "__DSH_BOOT_READY__") < strings.Index(body, "<body>") {
		t.Fatalf("就绪尾脚本必须在 <body> 之后")
	}

	// 5. 图里点名的每条 URL 都必须是我们真的会发的文件。
	for _, batch := range front.boot.graph.Batches {
		rec := httptest.NewRecorder()
		front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/"+batch.URL, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("批次脚本 %s 必须能取到，实际 %d", batch.URL, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
			t.Fatalf("批次脚本 Content-Type 不对: %q", got)
		}
	}
}

// TestBootWithoutManifestIsLoudNotFacadeMissing 钉住"裸 dist"这一档。
//
// 裸官方 dist 没有 dsh-boot.json：三条仍然注入（所以页面不再停在
// facade missing），但门面必须**响亮说明**为什么挂不上，并把结构化原因
// 写进 __DSH_BOOT_DIAGNOSIS__。
func TestBootWithoutManifestIsLoudNotFacadeMissing(t *testing.T) {
	front, err := New(Config{Dir: bootFixture(t, "", map[string]string{"assets/index-BgUyXZcc.js": "x"})})
	if err != nil {
		t.Fatalf("没有清单不该让前端起不来: %v", err)
	}
	body := fetchIndex(t, front)

	if !strings.Contains(body, "window.__ModuleLoader__=") {
		t.Fatalf("裸 dist 也必须装上门面（否则页面停在 facade missing）")
	}
	if !strings.Contains(body, `globalThis["__DSH_BOOT__"] = {"rev":"`) {
		t.Fatalf("裸 dist 也必须注入一份合法空图\n实际: %s", body)
	}
	if !strings.Contains(body, `"entries":[]`) || !strings.Contains(body, `"batches":[]`) {
		t.Fatalf("空图的 entries/batches 必须是空数组（官方解析器不认 null）\n实际: %s", body)
	}
	if !strings.Contains(body, "__DSH_BOOT_READY__") {
		t.Fatalf("裸 dist 也必须注入就绪尾脚本（它和门面同一个 patch）")
	}
	for _, want := range []string{
		"carries no bootstrap batch",
		"__DSH_BOOT_DIAGNOSIS__",
		"dsh-boot.json",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("退化分支缺诊断信息 %q", want)
		}
	}
	if _, ok := front.BootDescription(); !ok {
		t.Fatalf("BootDescription 必须报告 boot 已启用")
	}
}

// TestBootGraphRejectsBadManifests 钉住清单的每一条拒绝判据。
//
// 这些判据是官方 `parseBootManifest` 的同一套（页面侧解析器会抛的那些），
// 外加两条本部署专属约束：url 必须是 dist 内路径、文件必须真的在。
func TestBootGraphRejectsBadManifests(t *testing.T) {
	cases := map[string]struct {
		manifest string
		want     string
	}{
		"绝对 URL（官方宿主地址）": {
			manifest: `{"rev":"r","entries":[{"id":"a","url":"http://127.0.0.1:18082/plugins/a.js","rev":"1"}],"batches":[{"phase":"application","url":"http://127.0.0.1:18082/plugins/a.js","rev":"1","entries":["a"]}]}`,
			want:     "绝对 URL",
		},
		"文件不存在": {
			manifest: `{"rev":"r","entries":[{"id":"a","url":"plugins/nope.js","rev":"1"}],"batches":[{"phase":"application","url":"plugins/nope.js","rev":"1","entries":["a"]}]}`,
			want:     "不存在",
		},
		"entry 不属于任何批次": {
			manifest: `{"rev":"r","entries":[{"id":"a","url":"plugins/b0.js","rev":"1"},{"id":"b","url":"plugins/b0.js","rev":"1"}],"batches":[{"phase":"application","url":"plugins/b0.js","rev":"1","entries":["a"]}]}`,
			want:     "不属于任何批次",
		},
		"phase 不在词表": {
			manifest: `{"rev":"r","entries":[{"id":"a","url":"plugins/b0.js","rev":"1"}],"batches":[{"phase":"later","url":"plugins/b0.js","rev":"1","entries":["a"]}]}`,
			want:     "phase 只能是",
		},
		"批次指向不存在的 entry": {
			manifest: `{"rev":"r","entries":[{"id":"a","url":"plugins/b0.js","rev":"1"}],"batches":[{"phase":"application","url":"plugins/b0.js","rev":"1","entries":["a","ghost"]}]}`,
			want:     "不存在的 entry",
		},
		"id 重复": {
			manifest: `{"rev":"r","entries":[{"id":"a","url":"plugins/b0.js","rev":"1"},{"id":"a","url":"plugins/b0.js","rev":"2"}],"batches":[{"phase":"application","url":"plugins/b0.js","rev":"1","entries":["a"]}]}`,
			want:     "重复",
		},
		"缺 rev": {
			manifest: `{"rev":"","entries":[],"batches":[]}`,
			want:     "rev 必须是非空字符串",
		},
		"entries 是 null": {
			manifest: `{"rev":"r","entries":null,"batches":[]}`,
			want:     "entries 必须是数组",
		},
		"越界 ..": {
			manifest: `{"rev":"r","entries":[{"id":"a","url":"../a.js","rev":"1"}],"batches":[{"phase":"application","url":"plugins/b0.js","rev":"1","entries":["a"]}]}`,
			want:     "..",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(Config{Dir: bootFixture(t, tc.manifest, bootPluginFiles())})
			if err == nil {
				t.Fatalf("必须报错")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误必须点出 %q，实际 %q", tc.want, err.Error())
			}
		})
	}
}

// TestBootFacadeDisabledKeepsLegacyBytes 钉住关闭开关是**逐字节**回到旧形状。
func TestBootFacadeDisabledKeepsLegacyBytes(t *testing.T) {
	dir := bootFixture(t, officialBootManifest, bootPluginFiles())
	front, err := New(Config{Dir: dir, Boot: &BootOptions{Disabled: true}})
	if err != nil {
		t.Fatalf("建前端失败: %v", err)
	}
	if _, ok := front.BootDescription(); ok {
		t.Fatalf("关闭时 BootDescription 必须报 false")
	}
	body := fetchIndex(t, front)
	want, err := RenderIndex(officialIndex, []Injection{
		{Name: GlobalConnectionRecovery, Value: DefaultRecoveryConfig()},
		{Name: GlobalTransport, Value: TransportConfig{OwnsHost: true, StreamBaseURL: "http://127.0.0.1:4321"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if body != want {
		t.Fatalf("关闭 boot 后必须与历史形状逐字节相同\n got: %s\nwant: %s", body, want)
	}
	if strings.Contains(body, "__DSH_BOOT_READY__") || strings.Contains(body, "__ModuleLoader__") {
		t.Fatalf("关闭后不该出现 boot 三件套")
	}
}

// TestBootMissingBodyAnchorIs500 钉住"运行期注入失败必须 500 并点名锚点"。
//
// 用一个手工实例（indexHTML 没有 body）模拟"起来之后才发现"的那一类：
// 不许静默发一份没注入的 200。
func TestBootMissingBodyAnchorIs500(t *testing.T) {
	dir := bootFixture(t, officialBootManifest, bootPluginFiles())
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	front := &Frontend{
		root:          resolved,
		indexPath:     filepath.Join(resolved, IndexFile),
		indexHTML:     "<html><head></head><div id=\"root\"></div></html>",
		recovery:      DefaultRecoveryConfig(),
		streamBaseURL: defaultStreamBaseURL,
		boot:          &bootPayload{graph: emptyBootGraph(resolved), facade: bootFacadeScript},
	}
	rec := httptest.NewRecorder()
	front.Wrap(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("缺 <body> 必须 500，实际 %d（body=%q）", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "<head>") && !strings.Contains(rec.Body.String(), "<body>") {
		t.Fatalf("500 必须点名缺哪个锚点，实际 %q", rec.Body.String())
	}
}

// TestBootManifestIsReadFromDist 钉住清单路径的默认值。
func TestBootManifestIsReadFromDist(t *testing.T) {
	front, err := New(Config{Dir: bootFixture(t, officialBootManifest, bootPluginFiles())})
	if err != nil {
		t.Fatalf("建前端失败: %v", err)
	}
	description, ok := front.BootDescription()
	if !ok {
		t.Fatalf("BootDescription 必须报告已启用")
	}
	if !strings.Contains(description, BootManifestFile) {
		t.Fatalf("描述里必须带清单文件名，实际 %q", description)
	}
	if !strings.Contains(description, "entries=2") || !strings.Contains(description, "batches=2") {
		t.Fatalf("描述里必须带计数，实际 %q", description)
	}
}

// TestBootManifestPathOverride 钉住"清单可以搬到别处"。
func TestBootManifestPathOverride(t *testing.T) {
	dir := bootFixture(t, "", bootPluginFiles())
	manifest := filepath.Join(dir, "boot", "custom.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(officialBootManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	front, err := New(Config{Dir: dir, Boot: &BootOptions{ManifestPath: manifest}})
	if err != nil {
		t.Fatalf("建前端失败: %v", err)
	}
	body := fetchIndex(t, front)
	if !strings.Contains(body, `"@deepseek-ai/dsh-client-ui-renderer"`) {
		t.Fatalf("清单没有被读进图\n实际: %s", body)
	}
}
