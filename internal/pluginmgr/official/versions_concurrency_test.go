package official

// GO.7 / T9 内部测试：`pluginManager.versionLookupConcurrency` 真的在 adapter 的
// 每个版本补齐查询上生效（不是"定义了一个没人读的常量"）。
//
// 为什么用内部测试：观测点 `versionProbe` 是**测试缝**（生产恒为 nil），
// 只用来数"同时在飞的补齐查询数"。外部测试（`versions_fallback_test.go`）
// 负责钉住可观察的结果（问源顺序 / 回退源列表 / incompatible 字段）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

// testPackage 写一个最小可安装包（只用来让 listVersions 认为它"已安装且来自 registry"）。
func testPackage(t *testing.T, dir, id, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("payload " + id + " " + version)
	sum := sha256.Sum256(payload)
	manifest := map[string]any{
		"schema": "dsh.local-plugin@1", "id": id, "version": version,
		"kind": "wasm", "entry": "plugin.wasm",
		"files": map[string]string{"plugin.wasm": hex.EncodeToString(sum[:])},
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// packument 造一个含 N 个版本的 registry 响应。
func packumentServer(t *testing.T, name string, versions map[string]map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": name, "dist-tags": map[string]string{"latest": "1.0.0"},
			"versions": versions,
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestListVersionsEnrichmentUsesBoundedConcurrencyPool(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	manager, err := pluginmgr.Open(t.TempDir())
	if err != nil {
		t.Fatalf("pluginmgr.Open: %v", err)
	}
	local := testPackage(t, filepath.Join(t.TempDir(), "pkg"), "acme.pool", "1.0.0")
	if _, err := manager.Install(local); err != nil {
		t.Fatalf("install: %v", err)
	}

	// 6 个版本，每个都带 engines.dsh（→ 每个版本都要做一次真实的兼容性补齐）。
	versions := map[string]map[string]any{}
	for i, version := range []string{"1.0.0", "1.0.1", "1.0.2", "1.0.3", "1.0.4", "1.0.5"} {
		engines := map[string]any{"dsh": "^0.2.0"}
		if i >= 3 {
			engines["dsh"] = "^9.0.0"
		}
		versions[version] = map[string]any{"version": version, "engines": engines}
	}
	server := packumentServer(t, "acme.pool", versions)
	registry := server.URL

	var mu sync.Mutex
	inFlight, peak := 0, 0
	a := &adapter{manager: manager}
	a.versionProbe = func(string) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		time.Sleep(15 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
	}

	registryCopy := registry
	list, err := a.listVersions(transport.OfficialListVersionsParams{Name: "acme.pool", Registry: &registryCopy})
	if err != nil {
		t.Fatalf("listVersions: %v", err)
	}
	if list.Status != "ok" {
		t.Fatalf("listVersions 状态 = %q，want ok（%+v）", list.Status, list)
	}
	if len(list.Versions) != len(versions) {
		t.Fatalf("补齐后版本数 = %d，want %d（每个版本都必须真的被补齐，不能只补第一个）",
			len(list.Versions), len(versions))
	}
	if peak > pluginmgr.OfficialVersionLookupConcurrency {
		t.Fatalf("同时在飞的补齐查询 %d 路，超过官方默认上限 %d", peak, pluginmgr.OfficialVersionLookupConcurrency)
	}
	if peak < 2 {
		t.Fatalf("峰值只有 %d 路：6 个版本没有走并发池（默认上限 %d）", peak, pluginmgr.OfficialVersionLookupConcurrency)
	}

	// 显式把并发压到 1：峰值必须回到 1（证明上限是**读配置**的，不是写死的）。
	t.Setenv(pluginmgr.EnvVersionLookupConcurrency, "1")
	mu.Lock()
	peak = 0
	mu.Unlock()
	if _, err := a.listVersions(transport.OfficialListVersionsParams{Name: "acme.pool", Registry: &registryCopy}); err != nil {
		t.Fatalf("listVersions(concurrency=1): %v", err)
	}
	mu.Lock()
	serialPeak := peak
	mu.Unlock()
	if serialPeak != 1 {
		t.Fatalf("并发上限设成 1 之后峰值仍是 %d 路", serialPeak)
	}
}
