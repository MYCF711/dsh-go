package search

// 官方 `dsh-web-search-deepseek` 的三个"设置页可编辑"字段是**设置值 → 真实消费方**
// 的接线，不是 schema 里多出来的三个空壳：
//
//	官方权威源：@deepseek-ai/dsh-web-search-deepseek/src/index.ts:71-79
//	  apiVersion: z.string().default('2023-06-01').volatile(),
//	  maxTokens:  z.number().default(4096).volatile(),
//	  maxUses:    z.number().step(1).min(1).default(5).volatile(),
//
// 宿主侧的名字（设置命名空间 `web-search-deepseek` 经 settings_effective.go 的叠加层
// 变成这三个环境变量，本文件就是它们的消费方判据）：
//
//	maxUses    → DSHD_SEARCH_MAX_USES
//	maxTokens  → DSHD_SEARCH_MAX_TOKENS
//	apiVersion → DSHD_SEARCH_API_VERSION
//
// 判据四条：
//  1. 三个变量真的进 provider（并且真的到了请求的 header/body 上，不是只写进 struct）；
//  2. 空 / 非法 / <=0 一律回落官方默认（写坏配置不该把能力变成"永远拒绝"）；
//  3. `ModuleWithEnv(getenv)` 用的是**传进来的读取器**，不是进程环境；
//  4. `Module()` 与 `ModuleWithEnv(nil)` 都不 panic，且 nil 走 os.Getenv。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// staticEnv 复用 webapi_red_test.go 的同名 helper：把 map 变成 getenv 接缝，
// 不污染进程环境。

func TestDeepSeekFromEnvReadsOfficialFieldSeams(t *testing.T) {
	env := map[string]string{
		"DEEPSEEK_API_KEY":         "k",
		"DEEPSEEK_SEARCH_BASE_URL": "https://example.invalid/anthropic/v1",
		"DSHD_SEARCH_MAX_USES":     "9",
		"DSHD_SEARCH_MAX_TOKENS":   "2048",
		"DSHD_SEARCH_API_VERSION":  "2025-01-01",
	}
	provider := NewDeepSeekFromEnv(staticEnv(env))
	if provider.MaxUses != 9 {
		t.Fatalf("DSHD_SEARCH_MAX_USES 没生效: MaxUses = %d，期望 9", provider.MaxUses)
	}
	if provider.MaxTokens != 2048 {
		t.Fatalf("DSHD_SEARCH_MAX_TOKENS 没生效: MaxTokens = %d，期望 2048", provider.MaxTokens)
	}
	if provider.APIVersion != "2025-01-01" {
		t.Fatalf("DSHD_SEARCH_API_VERSION 没生效: APIVersion = %q，期望 2025-01-01", provider.APIVersion)
	}
	// 首尾空白不该变成"非法值"。
	env["DSHD_SEARCH_API_VERSION"] = "  2024-11-05  "
	if got := NewDeepSeekFromEnv(staticEnv(env)).APIVersion; got != "2024-11-05" {
		t.Fatalf("apiVersion 没去首尾空白: %q", got)
	}
}

// 真消费方判据：三个设置值必须出现在实际请求的 header/body 上。
func TestDeepSeekFieldSeamsReachTheWire(t *testing.T) {
	var mu sync.Mutex
	var seenVersion string
	var seenBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenVersion = r.Header.Get("anthropic-version")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[
			{"type":"web_search_tool_result","content":[
				{"type":"web_search_result","url":"https://a.example","title":"来源一"}
			]}
		]}`)
	}))
	defer srv.Close()

	provider := NewDeepSeekFromEnv(staticEnv(map[string]string{
		"DEEPSEEK_API_KEY":         "k",
		"DEEPSEEK_SEARCH_BASE_URL": srv.URL + "/anthropic/v1",
		"DSHD_SEARCH_MAX_USES":     "9",
		"DSHD_SEARCH_MAX_TOKENS":   "2048",
		"DSHD_SEARCH_API_VERSION":  "2025-01-01",
	}))
	if _, err := provider.Search(context.Background(), "官方同步", 8); err != nil {
		t.Fatalf("搜索失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seenVersion != "2025-01-01" {
		t.Fatalf("请求头 anthropic-version = %q，期望设置里的 2025-01-01", seenVersion)
	}
	if seenBody["max_tokens"] != float64(2048) {
		t.Fatalf("请求体 max_tokens = %v，期望设置里的 2048", seenBody["max_tokens"])
	}
	tools, _ := seenBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools 不对: %v", seenBody["tools"])
	}
	if got := tools[0].(map[string]any)["max_uses"]; got != float64(9) {
		t.Fatalf("请求体 max_uses = %v，期望设置里的 9", got)
	}
}

// 空 / 非法 / <=0 一律回落官方默认；apiVersion 空串等于没设。
func TestDeepSeekFieldSeamsFallBackOnInvalid(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"空值", map[string]string{}},
		{"非数字", map[string]string{
			"DSHD_SEARCH_MAX_USES":   "nine",
			"DSHD_SEARCH_MAX_TOKENS": "2k",
		}},
		{"零", map[string]string{
			"DSHD_SEARCH_MAX_USES":   "0",
			"DSHD_SEARCH_MAX_TOKENS": "0",
		}},
		{"负数", map[string]string{
			"DSHD_SEARCH_MAX_USES":   "-3",
			"DSHD_SEARCH_MAX_TOKENS": "-4096",
		}},
		{"小数", map[string]string{
			"DSHD_SEARCH_MAX_USES":   "5.5",
			"DSHD_SEARCH_MAX_TOKENS": "4096.5",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewDeepSeekFromEnv(staticEnv(tc.env))
			if provider.MaxUses != DeepSeekDefaultMaxUses {
				t.Fatalf("MaxUses = %d，期望回落官方默认 %d", provider.MaxUses, DeepSeekDefaultMaxUses)
			}
			if provider.MaxTokens != DeepSeekDefaultMaxTokens {
				t.Fatalf("MaxTokens = %d，期望回落官方默认 %d", provider.MaxTokens, DeepSeekDefaultMaxTokens)
			}
			if provider.APIVersion != DeepSeekDefaultAPIVersion {
				t.Fatalf("APIVersion = %q，期望回落官方默认 %q", provider.APIVersion, DeepSeekDefaultAPIVersion)
			}
		})
	}

	// apiVersion 只给空白 → 等同没设。
	blank := NewDeepSeekFromEnv(staticEnv(map[string]string{"DSHD_SEARCH_API_VERSION": "   "}))
	if blank.APIVersion != DeepSeekDefaultAPIVersion {
		t.Fatalf("空白 apiVersion = %q，期望回落 %q", blank.APIVersion, DeepSeekDefaultAPIVersion)
	}
}

// 装配辅助：只用传入的模块装配一次，并取出 web 接缝。
func assembleWebSeam(t *testing.T, mod kernel.Module) *spi.WebProviders {
	t.Helper()
	k := kernel.New("test-search-env")
	root := k.Root()
	if _, err := kernel.Assemble(root, []kernel.Module{mod}); err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	seam, err := kernel.Inject(root, spi.WebKey)
	if err != nil {
		t.Fatalf("Inject(WebKey) 失败: %v", err)
	}
	if seam == nil {
		t.Fatal("装配出来的 web 接缝是 nil")
	}
	return seam
}

// 把接缝里的 DeepSeek provider 取出来（钉死 deepseek-official 后选中它）。
func deepSeekFromSeam(t *testing.T, seam *spi.WebProviders) *DeepSeek {
	t.Helper()
	provider, err := seam.SelectSearch()
	if err != nil {
		t.Fatalf("选搜索 provider 失败: %v", err)
	}
	ds, ok := provider.(*DeepSeek)
	if !ok {
		t.Fatalf("选中的不是 DeepSeek provider: %T（%s）", provider, provider.ID())
	}
	return ds
}

// ModuleWithEnv 用的是**传进来的**读取器：进程环境里故意放一个不同的值，
// 结果必须是读取器给的那个（否则设置文档的优先级根本没接上）。
func TestModuleWithEnvUsesGivenReader(t *testing.T) {
	t.Setenv(envDeepSeekMaxUses, "3") // 进程环境：不该被读
	t.Setenv(envDeepSeekMaxTokens, "512")

	custom := staticEnv(map[string]string{
		"DEEPSEEK_API_KEY":         "k",
		"DSHD_WEB_SEARCH_PROVIDER": DeepSeekProviderID,
		envDeepSeekMaxUses:         "11",
		envDeepSeekMaxTokens:       "777",
		envDeepSeekAPIVersion:      "2026-02-02",
	})
	seam := assembleWebSeam(t, ModuleWithEnv(custom))
	ds := deepSeekFromSeam(t, seam)
	if ds.MaxUses != 11 {
		t.Fatalf("ModuleWithEnv 没用自己的读取器: MaxUses = %d，期望 11（进程环境是 3）", ds.MaxUses)
	}
	if ds.MaxTokens != 777 {
		t.Fatalf("ModuleWithEnv 没用自己的读取器: MaxTokens = %d，期望 777（进程环境是 512）", ds.MaxTokens)
	}
	if ds.APIVersion != "2026-02-02" {
		t.Fatalf("ModuleWithEnv 没用自己的读取器: APIVersion = %q，期望 2026-02-02", ds.APIVersion)
	}
}

// Module() 与 ModuleWithEnv(nil) 都不 panic，且 nil 走 os.Getenv——两者读同一份进程环境。
func TestModuleWithEnvNilFallsBackToProcessEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "k")
	t.Setenv("DSHD_WEB_SEARCH_PROVIDER", DeepSeekProviderID)
	t.Setenv(envDeepSeekMaxUses, "7")
	t.Setenv(envDeepSeekMaxTokens, "1234")
	t.Setenv(envDeepSeekAPIVersion, "2026-03-03")

	for _, tc := range []struct {
		name string
		mod  kernel.Module
	}{
		{"Module()", Module()},
		{"ModuleWithEnv(nil)", ModuleWithEnv(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mod.ID != "search" {
				t.Fatalf("模块 id = %q，期望 search", tc.mod.ID)
			}
			seam := assembleWebSeam(t, tc.mod)
			ds := deepSeekFromSeam(t, seam)
			if ds.MaxUses != 7 || ds.MaxTokens != 1234 || ds.APIVersion != "2026-03-03" {
				t.Fatalf("没有从进程环境读设置: MaxUses=%d MaxTokens=%d APIVersion=%q",
					ds.MaxUses, ds.MaxTokens, ds.APIVersion)
			}
			// 三个 provider 仍然齐全（Module→ModuleWithEnv 重构不能少注册）。
			if got := strings.Join(seam.SearchProviderIDs(), ","); got != "deepseek-official,deepseek-search-api,searxng" {
				t.Fatalf("注册的 provider 不对: %q", got)
			}
		})
	}
}
