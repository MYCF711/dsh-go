// Package search 是官方 0.2.2 `dsh-web` 能力接缝的宿主默认装配：
// 把可替换的搜索/抓取 provider 注册进 `spi.WebProviders`。
//
// 为什么不是"一个写死的 SearxNG 调用"：搜索没有公认的公共端点，每家请求/响应
// 形状都不一样；而且**没有配后端时必须明确报错**——不能返回"搜过了，没结果"，
// 那会让模型以为互联网上真的没有，而不是"这个宿主没接搜索"。
//
// 注册的 provider（id 与官方完全一致）：
//
//	deepseek-search-api   DeepSeek Search API（官方 rc.2 基础组合默认 searchProvider）
//	deepseek-fetch-api    DeepSeek Fetch API（页面 Markdown）
//	searxng               SearxNG 风格端点（自建）
//	deepseek-official     DeepSeek 原生 web_search（Anthropic Messages 上的 server tool）
//
// 配置方式（环境变量；官方同名变量 `DSH_WEB_SEARCH_PROVIDER` / `DSH_WEB_FETCH_PROVIDER` 也认）：
//
//	DSHD_SEARCH_URL                        SearxNG 端点，含 {query} 占位
//	DEEPSEEK_API_KEY                       DeepSeek 凭据（Search/Fetch API 与原生搜索共用；别名 DSHD_SEARCH_API_KEY）
//	DEEPSEEK_SEARCH_BASE_URL               原生搜索端点（默认 https://api.deepseek.com/anthropic/v1）
//	DSHD_SEARCH_MODEL                      原生搜索用的模型 id（默认 deepseek-v4-flash）
//	DSHD_WEB_SEARCH_PROVIDER               钉死用哪个搜索 provider
//	DSHD_WEB_FETCH_PROVIDER                钉死用哪个抓取 provider
//
// rc.2 `dsh-web-deepseek-api` 的九个设置（默认值与官方 config-catalog 一致）：
//
//	DSHD_WEB_SEARCH_COUNT                  每次搜索的结果数，1..50，默认 10
//	DSHD_WEB_SEARCH_REQUESTS_PER_SECOND    一秒窗口内最多发出的搜索数，>=1，默认 5
//	DSHD_WEB_SEND_ORIGINAL_QUESTION        是否发 ori_question，默认 true
//	DSHD_WEB_SEARCH_BILLING                search 的计费方式 auto|account|api-key，默认 auto
//	DSHD_WEB_FETCH_BILLING                 fetch 的计费方式，默认 auto
//	DSHD_WEB_SEARCH_API_BASE_URL           search 自有端点（含路径前缀；空 = 官方 base）
//	DSHD_WEB_SEARCH_API_KEY_ENV            search 自有凭据引用（空 = DEEPSEEK_API_KEY）
//	DSHD_WEB_FETCH_API_BASE_URL            fetch 自有端点
//	DSHD_WEB_FETCH_API_KEY_ENV             fetch 自有凭据引用
//	DSHD_WEB_LLM_PROVIDER                  这一轮对话的模型路由 id（auto 的会话级判定输入）
//
// 旧字段 `baseURL` / `apiKeyEnv` / `apiKey`（宿主对应 DSHD_WEB_BASE_URL / DSHD_WEB_API_KEY_ENV /
// DSHD_WEB_API_KEY）**读得到但不生效**：官方明确它们已不是 schema 字段，
// 加载不报错、也没有效果。
package search

import (
	"os"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// Module 装配默认搜索能力。
//
// 它只 provide 一个 `spi.WebProviders`（搜索 + 抓取共用的接缝）；`http` 抓取 provider
// 由 `modules/tool/webfetch` 注册进来，两边不互相 import。
func Module() kernel.Module {
	return kernel.Module{
		ID:       "search",
		Backend:  kernel.BackendStatic,
		Provides: []string{"web"},
		Register: func(s *kernel.Scope) error {
			seam, err := NewWebProvidersFromEnv(os.Getenv)
			if err != nil {
				return err
			}
			return kernel.Provide(s, spi.WebKey, seam)
		},
	}
}

// NewWebProvidersFromEnv 按环境装配默认 provider 集合。
//
// 生产（Module）与测试走**同一份**代码：provider 集合、钉死规则、可用性判定
// 都不是测试专用的仿造品。
//
// 默认 provider 的规则（官方 rc.2 基础组合 `cordis.patch.yml:487-488`）：
//   - 环境钉死优先（`DSHD_WEB_SEARCH_PROVIDER` / 官方同名变量）；
//   - 未钉死且 rc.2 基线默认 provider **当下可用** → 钉它
//     （`deepseek-search-api` / `http`），这就是"默认值真正生效"；
//   - 未钉死且基线默认不可用 → 保持空 pin，退回既有"自动选择唯一可用"语义。
//
// 为什么不无条件钉：官方的基线默认是配在 cordis 配置里的，而 DeepSeek 那两条
// 模型路由（账号/API-key）在官方宿主里会解析出凭据。本宿主还没有这两条路由插件，
// Search/Fetch API 的凭据只能来自 `DEEPSEEK_API_KEY`；无条件钉死会让"只配了
// SearxNG"的部署从"能用"退化成 `WEB_PROVIDER_CONFIGURED_UNAVAILABLE`。
// 有凭据时（正常部署）默认值与官方完全一致。
func NewWebProvidersFromEnv(getenv func(string) string) (*spi.WebProviders, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	searchAPI := NewDeepSeekSearchAPIFromEnv(getenv)
	fetchAPI := NewDeepSeekFetchAPIFromEnv(getenv)

	searchPin := spi.WebSearchPinFromEnv(getenv)
	if searchPin == "" && searchAPI.Available() {
		searchPin = BaseBundleSearchProviderID
	}
	fetchPin := spi.WebFetchPinFromEnv(getenv)
	if fetchPin == "" && fetchAPI.Available() {
		// 基线默认 fetchProvider 是匿名 http（官方 `cordis.patch.yml:488`）。
		// 不钉的话 `http` 与 `deepseek-fetch-api` 会同时可用 → WEB_PROVIDER_AMBIGUOUS。
		fetchPin = BaseBundleFetchProviderID
	}

	seam := spi.NewWebProviders(searchPin, fetchPin)
	if err := seam.RegisterSearch(&SearxNG{Endpoint: getenv("DSHD_SEARCH_URL")}); err != nil {
		return nil, err
	}
	if err := seam.RegisterSearch(NewDeepSeekFromEnv(getenv)); err != nil {
		return nil, err
	}
	if err := seam.RegisterSearch(searchAPI); err != nil {
		return nil, err
	}
	if err := seam.RegisterFetch(fetchAPI); err != nil {
		return nil, err
	}
	return seam, nil
}

// NewDeepSeekSearchAPIFromEnv 按环境造 `deepseek-search-api`（生产与测试同一份）。
func NewDeepSeekSearchAPIFromEnv(getenv func(string) string) *DeepSeekSearchAPI {
	getenv = normalizeGetenv(getenv)
	return &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions { return DeepSeekSearchAPIOptionsFromEnv(getenv) },
	}
}

// NewDeepSeekFetchAPIFromEnv 按环境造 `deepseek-fetch-api`（生产与测试同一份）。
func NewDeepSeekFetchAPIFromEnv(getenv func(string) string) *DeepSeekFetchAPI {
	getenv = normalizeGetenv(getenv)
	return &DeepSeekFetchAPI{
		Resolve: func() DeepSeekWebAPIOptions { return DeepSeekFetchAPIOptionsFromEnv(getenv) },
	}
}

// normalizeGetenv 让 nil 读取器变成"什么都没有"，且永不返回 nil。
func normalizeGetenv(getenv func(string) string) func(string) string {
	if getenv == nil {
		return func(string) string { return "" }
	}
	return getenv
}
