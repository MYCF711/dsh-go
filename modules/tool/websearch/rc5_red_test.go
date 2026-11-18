package websearch

// 本文件是官方基线 rc.2 → rc.5 同步（S1）的**先红**差异测试：只用既有公开形状
// （`search.NewWebProvidersFromEnv` / `DescribeCatalog` / `seam.SelectSearch`）写断言，
// 因此在 rc.2 口径的旧实现上能编过、会失败（红→绿记录见交付报告）。
//
// 官方依据（rc.5 解包 `tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/`）：
//
//   - `dsh-web/src/index.ts:58`  `DEEPSEEK_MODEL_ROUTES = new Set(['deepseek-official','deepseek-account'])`
//   - `dsh-web/src/index.ts:64`  `DEEPSEEK_ROUTE_SEARCH_PROVIDER = 'deepseek-official'`
//   - `dsh-web/src/index.ts:67`  `OTHER_ROUTE_SEARCH_PROVIDER = 'deepseek-search-api'`
//   - `dsh-web/src/index.ts:229-241` `selectSearchProvider(llmProvider)`：未配置时按
//     `request.llmProvider` 算 preferred，再交给 `resolveProvider`
//   - `dsh-web/src/index.ts:305-328` `resolveProvider`：configured → preferred(usable) →
//     唯一可用 → ambiguous / unavailable
//   - `dsh-tool-web/src/search.ts:409` `llmProvider = exec.agent?.session.requestContext()?.provider`
//   - `dsh-base/cordis.patch.yml:488-491` rc.5 的 `web` 行**只**配 `fetchProvider: http`：
//     搜索不再固定（rc.2 的 `searchProvider: deepseek-search-api` 已被删除）
//
// rc.2 行为（旧实现）：装配期把 searchPin 钉成 `deepseek-search-api`，模型路由不参与选择。
// rc.5 行为（本文件断言）：不钉死；未配置时 DeepSeek 两条路由 → `deepseek-official`，
// 其余路由与"没有模型请求" → `deepseek-search-api`。

import (
	"testing"

	"dshgo/modules/search"
)

// deepSeekRouteEnv 是"DeepSeek 凭据齐全 + 会话走某条模型路由"的最小装配环境。
//
// 两个 DeepSeek provider 都只吃 DEEPSEEK_API_KEY（Search/Fetch API 与原生搜索共用），
// 所以这个环境下"可用 provider"同时有 deepseek-official 与 deepseek-search-api——
// 选择结果完全由 rc.5 的路由优先规则决定，不会被可用性差异掩盖。
func deepSeekRouteEnv(route string) func(string) string {
	values := map[string]string{"DEEPSEEK_API_KEY": "sk-test"}
	if route != "" {
		values["DSHD_WEB_LLM_PROVIDER"] = route
	}
	return envMap(values)
}

// rc.5：会话在 DeepSeek 模型路由（API-key 路由）上时，搜索优先走 legacy Messages 搜索
// `deepseek-official`（DeepSeek 不按次收费）。rc.2 旧实现钉死 `deepseek-search-api`。
func TestRC5DeepSeekRoutePrefersLegacyMessagesSearch(t *testing.T) {
	for _, route := range []string{"deepseek-official", "deepseek-account"} {
		t.Run(route, func(t *testing.T) {
			seam, err := search.NewWebProvidersFromEnv(deepSeekRouteEnv(route))
			if err != nil {
				t.Fatal(err)
			}
			selected, err := seam.SelectSearch()
			if err != nil {
				t.Fatalf("DeepSeek 路由 %q 上该选中 deepseek-official: %v", route, err)
			}
			if selected.ID() != "deepseek-official" {
				t.Fatalf("DeepSeek 路由 %q 必须优先 deepseek-official（rc.5）；实际 %q",
					route, selected.ID())
			}
		})
	}
}

// rc.5：其它任何路由（以及没有模型请求）→ `deepseek-search-api`。
// 这一条在 rc.2 旧实现上也成立，用来钉住"改动没有把默认值搞反"。
func TestRC5OtherRoutePrefersSearchAPI(t *testing.T) {
	for _, route := range []string{"", "gateway", "openai", "anthropic"} {
		t.Run("route="+route, func(t *testing.T) {
			seam, err := search.NewWebProvidersFromEnv(deepSeekRouteEnv(route))
			if err != nil {
				t.Fatal(err)
			}
			selected, err := seam.SelectSearch()
			if err != nil {
				t.Fatalf("路由 %q 上该选中 deepseek-search-api: %v", route, err)
			}
			if selected.ID() != "deepseek-search-api" {
				t.Fatalf("路由 %q 必须选 deepseek-search-api；实际 %q", route, selected.ID())
			}
		})
	}
}

// rc.5：基础组合**不再固定** searchProvider（rc.5 `cordis.patch.yml:488-491` 的 web 行
// 只剩 fetchProvider）。旧实现有凭据就钉 `deepseek-search-api`，所以这条在旧实现上红。
func TestRC5BaseCompositionPinsNoSearchProvider(t *testing.T) {
	seam, err := search.NewWebProvidersFromEnv(deepSeekRouteEnv("deepseek-account"))
	if err != nil {
		t.Fatal(err)
	}
	if pin := seam.SearchPin(); pin != "" {
		t.Fatalf("rc.5 基础组合不固定搜索 provider（按路由选）；当前仍钉死 %q", pin)
	}
}

// rc.5：`web.listProviders` 的宿主附加投影也要跟着路由走：
//   - Source 不再是 "baseline"（rc.5 没有基线默认搜索 provider）；
//   - Selected / Default 落在路由优先的那个 provider 上；
//   - Baseline.Search 为空（组合不固定），抓取侧仍固定 http。
func TestRC5CatalogSelectionFollowsRoute(t *testing.T) {
	catalog, err := DescribeCatalog(deepSeekRouteEnv("deepseek-account"))
	if err != nil {
		t.Fatal(err)
	}
	if catalog.SearchSelection.Source != "auto" {
		t.Fatalf("rc.5 未配置时来源是 auto（按路由选）；实际 %q", catalog.SearchSelection.Source)
	}
	if catalog.SearchSelection.Selected != "deepseek-official" {
		t.Fatalf("DeepSeek 路由上 Selected 该是 deepseek-official；实际 %q",
			catalog.SearchSelection.Selected)
	}
	if !entryOf(catalog.Search, "deepseek-official").Selected {
		t.Fatalf("deepseek-official 必须带 Selected: %+v", catalog.Search)
	}
	if !entryOf(catalog.Search, "deepseek-official").Default {
		t.Fatalf("DeepSeek 路由上默认 provider 是 deepseek-official: %+v", catalog.Search)
	}
	if entryOf(catalog.Search, "deepseek-search-api").Default {
		t.Fatalf("DeepSeek 路由上 deepseek-search-api 不该是默认: %+v", catalog.Search)
	}
	if catalog.Baseline.Search != "" {
		t.Fatalf("rc.5 基础组合不固定搜索 provider，Baseline.Search 该为空；实际 %q",
			catalog.Baseline.Search)
	}
	if catalog.Baseline.Fetch != "http" || !catalog.Baseline.FetchRegistered {
		t.Fatalf("抓取侧仍固定 http（rc.5 cordis.patch.yml:491）；实际 %+v", catalog.Baseline)
	}
}

// rc.5：非 DeepSeek 路由上，清单投影的 Selected/Default 是 deepseek-search-api。
func TestRC5CatalogOtherRouteSelectsSearchAPI(t *testing.T) {
	catalog, err := DescribeCatalog(deepSeekRouteEnv("gateway"))
	if err != nil {
		t.Fatal(err)
	}
	if catalog.SearchSelection.Selected != "deepseek-search-api" {
		t.Fatalf("其它路由 Selected 该是 deepseek-search-api；实际 %q", catalog.SearchSelection.Selected)
	}
	if !entryOf(catalog.Search, "deepseek-search-api").Default {
		t.Fatalf("其它路由上默认 provider 是 deepseek-search-api: %+v", catalog.Search)
	}
}
