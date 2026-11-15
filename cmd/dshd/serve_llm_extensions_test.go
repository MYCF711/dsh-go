package main

// 官方 #6 `deepseek-llm-api-extensions` + #17 `plugin-package-inventory-deepseek`
// 的**生产接线**验收：不是"函数存在"，而是"真的进了请求体"。
//
// 判据（与审计 plugin-face.md 主表 #6 的"差什么"逐条对应）：
//   - 装配期注册之后，`PrepareRequestExtensions(deepseek-completions)` 真的返回
//     `dsh_plugin_packages`，值是 version-1 库存；
//   - 用 httptest 假 SSE 端点接住一模一样的 DeepSeek 请求时，**原始请求体**里
//     真的出现这个顶层字段（且包身份来自真实 manager+loader 投影）；
//   - 非 DeepSeek 协议（openai-completions）的请求体里**不出现**它；
//   - 库存取失败（包目录损坏 → Bundles 报错）时请求照发，只少这一个字段，
//     并且省略原因要报给调用方（不许静默成功）。
//
// 夹具全部是真实的：真实 pluginmgr.Manager/state.json + 真实 wasm Loader
// （internal/wasmhost/testdata/toolplugin.wasm），不 mock 库存。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"dshgo/internal/llm"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

// writeInventoryPluginPackage 写一个可真实安装的 wasm 包（id/version 可控）。
//
// 与 serve_plugins_test.go 的 writePluginTransportPackage 同一形状，只是把 id
// 参数化了：库存的排序/去重要看**多个不同名**的包，硬编码一个 id 做不到。
func writeInventoryPluginPackage(t *testing.T, dir, id, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建包目录: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join("..", "..", "internal", "wasmhost", "testdata", "toolplugin.wasm"))
	if err != nil {
		t.Fatalf("读不到 wasm 夹具: %v", err)
	}
	sum := sha256.Sum256(payload)
	manifest := map[string]any{
		"schema": "dsh.local-plugin@1", "id": id, "version": version,
		"kind": "wasm", "entry": "plugin.wasm",
		"files": map[string]string{"plugin.wasm": hex.EncodeToString(sum[:])},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("序列化 manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatalf("写 plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), payload, 0o644); err != nil {
		t.Fatalf("写 plugin.wasm: %v", err)
	}
	return dir
}

// pluginPackagesTestFixture 是"真实 manager + 真实 loader + 两个已启用包"的夹具。
type pluginPackagesTestFixture struct {
	manager    *pluginmgr.Manager
	rt         *pluginRuntime
	pluginsDir string
}

func newPluginPackagesTestFixture(t *testing.T) *pluginPackagesTestFixture {
	t.Helper()
	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	manager, err := pluginmgr.Open(pluginsDir)
	if err != nil {
		t.Fatalf("pluginmgr.Open: %v", err)
	}
	rt, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatalf("newPluginRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })

	// 两个不同名的包：安装后**真实 enable**（enable 会真的把 wasm 装进 Loader，
	// 这是官方"root fiber ACTIVE"在本仓的唯一真值来源）。
	for _, pkg := range []struct{ id, version string }{
		{"acme.audit", "1.0.0"},
		{"zeta.tool", "2.3.1"},
	} {
		dir := writeInventoryPluginPackage(t, filepath.Join(t.TempDir(), pkg.id), pkg.id, pkg.version)
		if _, err := manager.Install(dir); err != nil {
			t.Fatalf("真实安装 %s: %v", pkg.id, err)
		}
		if _, err := manager.Enable(pkg.id); err != nil {
			t.Fatalf("真实 enable %s: %v", pkg.id, err)
		}
	}
	loaded := rt.Loaded()
	if len(loaded) != 2 {
		t.Fatalf("夹具要求两个包都真的装载，实际 %d：%+v", len(loaded), loaded)
	}
	return &pluginPackagesTestFixture{manager: manager, rt: rt, pluginsDir: pluginsDir}
}

// sseCaptureServer 是假 DeepSeek SSE 端点：原样记下**收到的请求字节**再回一条最小流。
//
// 为什么要记原始字节而不是反序列化成 llm.Request：llm.Request 是封闭结构体，
// 扩展字段反序列化时会被丢掉——那样断言"字段存在"永远为假（也永远测不出来）。
type sseCaptureServer struct {
	mu     sync.Mutex
	bodies []string
}

func (s *sseCaptureServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, string(raw))
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *sseCaptureServer) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

func (s *sseCaptureServer) last(t *testing.T) string {
	t.Helper()
	bodies := s.snapshot()
	if len(bodies) == 0 {
		t.Fatal("假 SSE 端点没收到任何请求")
	}
	return bodies[len(bodies)-1]
}

// requestField 把一条原始请求体解成顶层字段表。
func requestField(t *testing.T, body, field string) (json.RawMessage, bool) {
	t.Helper()
	var sent map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("请求体不是 JSON 对象: %v（body=%s）", err, body)
	}
	value, ok := sent[field]
	return value, ok
}

func streamDeepSeek(t *testing.T, srvURL, api string, onOmit func(error)) *llm.Response {
	t.Helper()
	client := llm.NewWithAPI(srvURL, "k", api)
	client.OnExtensionError = onOmit
	resp, err := client.Stream(context.Background(),
		llm.Request{Model: "deepseek-v4", Messages: []llm.Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("Stream(%s): %v", api, err)
	}
	return resp
}

// TestPluginPackagesExtensionIsRegisteredAndSent 是 #6 的"接上"判据：
// 装配期注册 → prepare 出值 → 真实请求体里出现字段 → 非 DeepSeek 协议不出现 →
// 库存坏掉也不阻塞请求。
func TestPluginPackagesExtensionIsRegisteredAndSent(t *testing.T) {
	fixture := newPluginPackagesTestFixture(t)
	capture := &sseCaptureServer{}
	srv := httptest.NewServer(capture)
	t.Cleanup(srv.Close)

	// 装配期那一行（serve.go 里同款调用）：整个测试二进制里只允许发生一次。
	if err := registerPluginPackagesExtension(fixture.rt); err != nil {
		t.Fatalf("registerPluginPackagesExtension: %v", err)
	}

	t.Run("prepare-returns-version-1-inventory", func(t *testing.T) {
		prepared, omitted, err := llm.PrepareRequestExtensions(context.Background(), llm.ProviderAPIDeepSeekCompletions,
			&llm.Request{Model: "deepseek-v4"})
		if err != nil {
			t.Fatalf("PrepareRequestExtensions: %v", err)
		}
		if len(omitted) != 0 {
			t.Fatalf("活跃库存可读时不该省略字段：%v", omitted)
		}
		if len(prepared) != 1 || prepared[0].Field != requestExtensionPluginPackages {
			t.Fatalf("prepared = %+v，期望恰好 %s", prepared, requestExtensionPluginPackages)
		}
		var inventory struct {
			Version  int `json:"version"`
			Packages []struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"packages"`
		}
		if err := json.Unmarshal(prepared[0].Value, &inventory); err != nil {
			t.Fatalf("字段值不是合法 JSON: %v（%s）", err, prepared[0].Value)
		}
		if inventory.Version != 1 {
			t.Fatalf("version = %d，官方形状恒为 1", inventory.Version)
		}
		// 两个真实包都真的装载了，库存必须按 name 升序（字节序）出现。
		if len(inventory.Packages) != 2 {
			t.Fatalf("packages = %+v，期望两个真实包", inventory.Packages)
		}
		if inventory.Packages[0].Name != "acme.audit" || inventory.Packages[0].Version != "1.0.0" {
			t.Fatalf("第一包身份不对：%+v", inventory.Packages[0])
		}
		if inventory.Packages[1].Name != "zeta.tool" || inventory.Packages[1].Version != "2.3.1" {
			t.Fatalf("第二包身份不对：%+v", inventory.Packages[1])
		}
	})

	t.Run("deepseek-request-body-carries-field", func(t *testing.T) {
		streamDeepSeek(t, srv.URL, llm.ProviderAPIDeepSeekCompletions, nil)
		body := capture.last(t)
		value, ok := requestField(t, body, requestExtensionPluginPackages)
		if !ok {
			t.Fatalf("DeepSeek 请求体里没有 %s：%s", requestExtensionPluginPackages, body)
		}
		if !strings.Contains(string(value), `"name":"acme.audit"`) ||
			!strings.Contains(string(value), `"version":1`) {
			t.Fatalf("字段值不是真实库存：%s", value)
		}
		// 留一条可复跑的真实载荷证据（`-v` 才打印）。
		t.Logf("DeepSeek 请求体里的 %s = %s", requestExtensionPluginPackages, value)
		// 扩展字段是**顶层**字段，不能混进 messages / 工具定义里。
		if strings.Contains(body, `"messages":[{"role":"user","content":"hi","dsh_plugin_packages"`) {
			t.Fatalf("扩展字段被塞进了 messages：%s", body)
		}
	})

	t.Run("deepseek-account-route-carries-field", func(t *testing.T) {
		streamDeepSeek(t, srv.URL, llm.ProviderAPIDeepSeekAccount, nil)
		body := capture.last(t)
		if _, ok := requestField(t, body, requestExtensionPluginPackages); !ok {
			t.Fatalf("账号路由请求体里没有 %s（同一个 DeepSeek 官方面）：%s", requestExtensionPluginPackages, body)
		}
	})

	t.Run("openai-completions-body-has-no-field", func(t *testing.T) {
		streamDeepSeek(t, srv.URL, llm.ProviderAPIOpenAICompletions, nil)
		body := capture.last(t)
		if strings.Contains(body, requestExtensionPluginPackages) {
			t.Fatalf("非 DeepSeek 协议不该带该字段：%s", body)
		}
	})

	// 注册是幂等的反面：同一字段在同一个协议上重复注册必须 fail-loud，
	// 否则"谁来注册"会变成一个没人负责的隐式状态。
	t.Run("duplicate-registration-fails-loud", func(t *testing.T) {
		err := registerPluginPackagesExtension(fixture.rt)
		if err == nil || !strings.Contains(err.Error(), "不允许重复") {
			t.Fatalf("重复注册必须报错，实际 %v", err)
		}
	})

	// 库存取失败（包目录被破坏 → Manager.Bundles 报 package_corrupt）：
	// 请求照发，只少这一个字段；省略原因要报给调用方，不能静默成功。
	t.Run("broken-inventory-omits-field-without-blocking", func(t *testing.T) {
		manifestPath := filepath.Join(fixture.pluginsDir, "packages", "acme.audit", "1.0.0", "plugin.json")
		original, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatalf("读已安装 manifest: %v", err)
		}
		if err := os.Remove(manifestPath); err != nil {
			t.Fatalf("破坏包目录: %v", err)
		}
		t.Cleanup(func() { _ = os.WriteFile(manifestPath, original, 0o644) })

		var omissionErrors []error
		var mu sync.Mutex
		// 请求必须真的发出去并成功：库存不是模型请求的前置条件。
		streamDeepSeek(t, srv.URL, llm.ProviderAPIDeepSeekCompletions, func(err error) {
			mu.Lock()
			defer mu.Unlock()
			omissionErrors = append(omissionErrors, err)
		})
		body := capture.last(t)
		if _, ok := requestField(t, body, requestExtensionPluginPackages); ok {
			t.Fatalf("库存不可读时不该发这个字段：%s", body)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(omissionErrors) == 0 {
			t.Fatal("省略字段必须报告给调用方（不许静默成功）")
		}
		if !strings.Contains(omissionErrors[0].Error(), requestExtensionPluginPackages) {
			t.Fatalf("省略原因要点名这个字段：%v", omissionErrors[0])
		}
	})
}

// TestCollectActivePluginPackagesRules 钉住官方那三条纯规则（不依赖 I/O）：
// 只收"启用且真的装载"的条目、去重键 name\u0000version、字节序排序且
// name-only 排在同名带版本之前。
func TestCollectActivePluginPackagesRules(t *testing.T) {
	active := "active"
	pending := "pending"
	entries := []transport.OfficialPluginEntry{
		{EntryID: "zeta.tool", Enabled: true, FiberPhase: &active},
		{EntryID: "acme.audit", Enabled: true, FiberPhase: &active},
		// 同名同版重复出现：去重后只留一条（官方 host 树 + preset 树会重叠）。
		{EntryID: "acme.audit", Enabled: true, FiberPhase: &active},
		// 禁用 / 未装载（fiberPhase=null）/ 非 active 一律不进库存。
		{EntryID: "disabled.pkg", Enabled: false, FiberPhase: &active},
		{EntryID: "unloaded.pkg", Enabled: true, FiberPhase: nil},
		{EntryID: "pending.pkg", Enabled: true, FiberPhase: &pending},
	}
	bundles := []transport.OfficialBundle{
		{Name: "acme.audit", Version: "1.0.0"},
		{Name: "zeta.tool", Version: "2.3.1"},
		// 版本是空白串 = name-only 条目（官方 identityFromManifest 同款）。
		{Name: "nameonly.pkg", Version: "   "},
		{Name: "disabled.pkg", Version: "9.9.9"},
		{Name: "unloaded.pkg", Version: "9.9.9"},
		{Name: "pending.pkg", Version: "9.9.9"},
	}
	entries = append(entries, transport.OfficialPluginEntry{EntryID: "nameonly.pkg", Enabled: true, FiberPhase: &active})
	// 说明：本仓的版本来自 `officialPluginBundles` 的 name→version 投影，所以同名
	// 条目只可能收敛成同一个身份；"name-only 排在同名带版本之前"由排序比较器
	// （空串最小）保证，而不是靠造一堆同名不同版本的假投影。

	got := collectActivePluginPackages(entries, bundles)
	want := []dshPluginPackageIdentity{
		{Name: "acme.audit", Version: "1.0.0"},
		{Name: "nameonly.pkg"},
		{Name: "zeta.tool", Version: "2.3.1"},
	}
	if len(got) != len(want) {
		t.Fatalf("packages = %+v，期望 %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("packages[%d] = %+v，期望 %+v", index, got[index], want[index])
		}
	}

	// 空条目/未接线是"没有库存"而不是"假库存"。
	if empty := collectActivePluginPackages(nil, nil); len(empty) != 0 {
		t.Fatalf("空投影必须收敛成空 packages：%+v", empty)
	}
	if _, err := activePluginPackages(nil); err == nil {
		t.Fatal("运行时未接线必须返回错误，不能静默给空库存")
	}
}
