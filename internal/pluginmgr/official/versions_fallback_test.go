package official_test

// GO.7 / T9：`pluginManager.fallbackRegistries` 与版本列表的真实问源顺序。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-plugin-manager/lib/index.js）：
//   - :1951 `fallbackRegistries: …default([NPMMIRROR_REGISTRY])`（默认 npmmirror）
//   - :1076-1096 `registryPlan()`：指定的源不在配置集合里时**只问它自己**
//   - :1097-1103 `NEXT_REGISTRY_KINDS`：只有 network/timeout/not-found/
//     no-matching-version 才轮到下一个源
//
// 红侧事实（GO.7 之前）：`adapter.registries()` 硬写 `FallbackRegistries: []string{}`，
// `listVersions` 只问 `RegistryFromEnv()` 给的那一个源，失败即 503。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/pluginmgr"
)

// TestRegistriesReportsRealFallbackList 钉住 registries() 的真实内容。
func TestRegistriesReportsRealFallbackList(t *testing.T) {
	f := newFixture(t, "")
	t.Setenv("DSHD_PLUGIN_REGISTRY", "https://registry.npmjs.org")
	// 未设回退源 = 官方默认（npmmirror），不是空列表。
	t.Setenv("DSHD_PLUGIN_REGISTRY_FALLBACKS", "")

	result := f.result(t, "pluginManager.registries", map[string]any{})
	if got := result["registry"]; got != "https://registry.npmjs.org/" {
		t.Fatalf("registry = %v，want https://registry.npmjs.org/（规范化后）", got)
	}
	fallbacks, ok := result["fallbackRegistries"].([]any)
	if !ok || len(fallbacks) != 1 || fallbacks[0] != "https://registry.npmmirror.com/" {
		t.Fatalf("fallbackRegistries = %v，want [https://registry.npmmirror.com/]（官方默认）", result["fallbackRegistries"])
	}
	// 宿主没有 pnpm 集成 → pnpm 自有源未知 → resolved 必须是 null，不能把
	// "我们配置的源"冒充成 pnpm 解析出来的源。
	if resolved, has := result["resolved"]; !has || resolved != nil {
		t.Fatalf("resolved = %v，want null（本仓没有 pnpm 自有配置）", resolved)
	}

	// 显式配置的回退源按顺序如实回传。
	t.Setenv("DSHD_PLUGIN_REGISTRY_FALLBACKS", "https://mirror-a.example, https://mirror-b.example/")
	result = f.result(t, "pluginManager.registries", map[string]any{})
	fallbacks, ok = result["fallbackRegistries"].([]any)
	if !ok || len(fallbacks) != 2 || fallbacks[0] != "https://mirror-a.example/" || fallbacks[1] != "https://mirror-b.example/" {
		t.Fatalf("配置后的 fallbackRegistries = %v", result["fallbackRegistries"])
	}
}

// packumentRegistry 起一个只回一个包版本列表的 registry 桩。
func packumentRegistry(t *testing.T, name, latest string, versions map[string]map[string]any) *httptest.Server {
	t.Helper()
	if versions == nil {
		versions = map[string]map[string]any{}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": name, "dist-tags": map[string]string{"latest": latest}, "versions": versions,
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestListVersionsAsksFallbackRegistryAndMarksIncompatible(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	f := newFixture(t, "")
	pkg := packageFixture(t, filepath.Join(f.home, "private", "v1"), "acme.fb", "1.0.0", nil)
	f.result(t, "pluginManager.installBundle", map[string]any{"spec": pkg})

	// 主源：包不存在（404 → not-found：官方唯一会轮到下一个源的"包级"失败）。
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Not found"}`, http.StatusNotFound)
	}))
	defer primary.Close()

	// 回退源：1.0.0 兼容当前 runtime，2.0.0 明确不兼容（engines.dsh 要求 ^9）。
	fallback := packumentRegistry(t, "acme.fb", "2.0.0", map[string]map[string]any{
		"1.0.0": {"version": "1.0.0", "engines": map[string]any{"dsh": "^0.2.0"}},
		"2.0.0": {"version": "2.0.0", "engines": map[string]any{"dsh": "^9.0.0"}},
	})

	t.Setenv("DSHD_PLUGIN_REGISTRY", primary.URL)
	t.Setenv("DSHD_PLUGIN_REGISTRY_FALLBACKS", fallback.URL)

	result := f.result(t, "pluginManager.listVersions", map[string]any{
		"name": "acme.fb", "options": map[string]any{"registry": primary.URL},
	})
	if result["status"] != "ok" {
		t.Fatalf("listVersions 状态 = %v，want ok（%v）", result["status"], result)
	}
	if result["installed"] != "1.0.0" {
		t.Fatalf("installed = %v，want 1.0.0", result["installed"])
	}

	attempts, ok := result["attempts"].([]any)
	if !ok || len(attempts) != 2 {
		t.Fatalf("attempts = %v，want 两跳（主源 not-found → 回退源成功）", result["attempts"])
	}
	first, _ := attempts[0].(map[string]any)
	if first["problem"] != "not-found" {
		t.Fatalf("第一跳 problem = %v，want not-found", first["problem"])
	}
	if !strings.HasPrefix(first["registry"].(string), primary.URL) {
		t.Fatalf("第一跳问的不是主源：%v", first["registry"])
	}
	second, _ := attempts[1].(map[string]any)
	if problem, has := second["problem"]; has && problem != "" {
		t.Fatalf("第二跳（回退源）不该带 problem：%v", second)
	}
	if !strings.HasPrefix(second["registry"].(string), fallback.URL) {
		t.Fatalf("第二跳问的不是回退源：%v", second["registry"])
	}
	if registry, _ := result["registry"].(string); !strings.HasPrefix(registry, fallback.URL) {
		t.Fatalf("answering registry = %v，want 回退源", result["registry"])
	}

	versions, ok := result["versions"].([]any)
	if !ok || len(versions) != 2 {
		t.Fatalf("versions = %v，want 2 行", result["versions"])
	}
	var sawCompatible, sawIncompatible bool
	for _, row := range versions {
		entry, _ := row.(map[string]any)
		switch entry["version"] {
		case "1.0.0":
			if _, has := entry["incompatible"]; has {
				t.Fatalf("1.0.0 与当前 runtime 兼容，不该有 incompatible：%v", entry)
			}
			sawCompatible = true
		case "2.0.0":
			incompatible, has := entry["incompatible"].(map[string]any)
			if !has {
				t.Fatalf("2.0.0 要求 engines.dsh=^9.0.0，必须带 incompatible：%v", entry)
			}
			if incompatible["runtimeVersion"] != pluginmgr.HostRuntimeVersion {
				t.Fatalf("incompatible.runtimeVersion = %v，want %q", incompatible["runtimeVersion"], pluginmgr.HostRuntimeVersion)
			}
			if incompatible["version"] != "2.0.0" {
				t.Fatalf("incompatible.version = %v，want 2.0.0", incompatible["version"])
			}
			sawIncompatible = true
		}
	}
	if !sawCompatible || !sawIncompatible {
		t.Fatalf("两个版本没有都被补齐（compatible=%v incompatible=%v）：%v", sawCompatible, sawIncompatible, versions)
	}
}

// TestListVersionsNeverFallsBackFromUnknownRegistry：调用方指定的源不在配置集合里
// → 只问它自己。私有源绝不因为一次 404 就把包名泄露给公共回退源。
func TestListVersionsNeverFallsBackFromUnknownRegistry(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	f := newFixture(t, "")
	pkg := packageFixture(t, filepath.Join(f.home, "private", "v1"), "acme.private", "1.0.0", nil)
	f.result(t, "pluginManager.installBundle", map[string]any{"spec": pkg})

	var publicHits int
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicHits++
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "acme.private"})
	}))
	defer public.Close()

	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Not found"}`, http.StatusNotFound)
	}))
	defer private.Close()

	t.Setenv("DSHD_PLUGIN_REGISTRY", private.URL)
	t.Setenv("DSHD_PLUGIN_REGISTRY_FALLBACKS", public.URL)

	// 指定一个**不在**配置集合里的源（另一个私有源）：计划里只有它自己。
	stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Not found"}`, http.StatusNotFound)
	}))
	defer stranger.Close()

	f.fourField(t, "pluginManager.listVersions", map[string]any{
		"name": "acme.private", "options": map[string]any{"registry": stranger.URL},
	}, http.StatusServiceUnavailable, "registry_unavailable")
	if publicHits != 0 {
		t.Fatalf("未知源失败后打了 %d 次公共回退源——私有包名被外溢了", publicHits)
	}
}
