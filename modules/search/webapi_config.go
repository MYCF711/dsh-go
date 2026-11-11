package search

// 本文件是官方 0.2.2-rc.2 `@deepseek-ai/dsh-web-deepseek-api` 的**配置面**：
// 两个 provider（`deepseek-search-api` / `deepseek-fetch-api`）共用的一份设置，
// 字段、默认值、边界逐条对照官方源码与配置目录。
//
// 权威（只读）：
//   - `official-rc2/.../dsh-web-deepseek-api/src/index.ts:64-109`（Config 接口 + zod schema）
//   - `official-rc2-docs/.../config-catalog.md:4850-4896`（同字段的文档形状）
//   - `official-rc2/.../dsh-web-deepseek-api/src/search.ts:13-22`（id / base / count / rps 默认）
//   - `official-rc2/.../dsh-web-deepseek-api/src/fetch.ts:13`（fetch id）
//   - `official-rc2/.../dsh-base/cordis.patch.yml:487-488`（基础组合默认 provider）
//
// 与官方的形状差异（宿主约束，不是简化）：
//   - 官方配置由 cordis 的 schema 校验、由设置界面写入；本宿主的配置来自**启动环境**
//     （`DSHD_*`），非法值按本仓惯例回落官方默认值（不把工具变成永远拒绝）。
//   - 官方旧字段 `baseURL` / `apiKeyEnv` / `apiKey` 已不是 schema 字段；宿主对应三个
//     环境变量名，**读得到但完全不生效**（官方语义：load without error, no effect）。

import (
	"context"
	"strconv"
	"strings"
)

const (
	// DeepSeekSearchAPIProviderID 是 Search API provider 的注册键（官方 `search.ts:13`）。
	DeepSeekSearchAPIProviderID = "deepseek-search-api"
	// DeepSeekFetchAPIProviderID 是 Fetch API provider 的注册键（官方 `fetch.ts:13`）。
	DeepSeekFetchAPIProviderID = "deepseek-fetch-api"

	// BaseBundleSearchProviderID 是官方基础组合钉死的 searchProvider
	// （rc.2 `dsh-base/cordis.patch.yml:487`；rc.1 在 `:470` 是 deepseek-official）。
	BaseBundleSearchProviderID = DeepSeekSearchAPIProviderID
	// BaseBundleFetchProviderID 是官方基础组合钉死的 fetchProvider
	// （rc.2 `dsh-base/cordis.patch.yml:488` = `http`，与 rc.1 `:471` 相同）。
	BaseBundleFetchProviderID = "http"

	// DeepSeekWebAPIBaseURL 是官方 Search/Fetch API 默认 base，**含** `/v1/tools` 前缀，
	// 请求时再接 `/search`、`/fetch`（官方 `search.ts:16`）。
	DeepSeekWebAPIBaseURL = "https://api.deepseek.com/v1/tools"

	// DeepSeekDefaultCount 是一次搜索的结果数默认值，也是服务端自己的默认（`search.ts:19`）。
	DeepSeekDefaultCount = 10
	// DeepSeekSearchRequestsPerSecond 是账号级 5 QPS 限制对应的默认限速（`search.ts:22`）。
	DeepSeekSearchRequestsPerSecond = 5

	// DeepSeekAPIKeyRef 是官方 DeepSeek API key 的凭据引用名（官方 `index.ts:130`）。
	DeepSeekAPIKeyRef = "DEEPSEEK_API_KEY"
	// DeepSeekAPIKeyEnvAlias 是宿主既有别名：与 DEEPSEEK_API_KEY 喂同一个字段。
	DeepSeekAPIKeyEnvAlias = "DSHD_SEARCH_API_KEY"

	// WebAPIPluginName 是官方插件名（诊断文案里点名 `web-deepseek-api.*` 字段，`search.ts:53`）。
	WebAPIPluginName = "web-deepseek-api"

	// webAPIToolsPath 是 Search/Fetch API 在 DeepSeek origin 下的路径前缀（`index.ts:127`）。
	webAPIToolsPath = "/v1/tools"

	// deepSeekWebAPIUserAgent 是官方归属头（`transport.ts:10`）。
	deepSeekWebAPIUserAgent = "deepseek-harness/0.0.1"

	// originalQuestionMaxChars 是 `ori_question` 的长度上限（`search.ts:25`）。
	originalQuestionMaxChars = 128

	// searchCountMax 是 `count` 的 schema 上限（`index.ts:100` `.min(1).max(50)`）。
	searchCountMax = 50
)

// 配置字段的环境变量名。
//
// 命名规则与既有 DeepSeek 原生搜索（`DEEPSEEK_SEARCH_BASE_URL` 那组）**刻意分开**：
// 那组喂的是 `deepseek-official`（Anthropic Messages 上的原生 web_search），
// 这一组喂 rc.2 的 Search/Fetch API。两组端点语义完全不同，混用会把模型指到错的 URL。
const (
	envWebSearchCount             = "DSHD_WEB_SEARCH_COUNT"
	envWebSearchRequestsPerSecond = "DSHD_WEB_SEARCH_REQUESTS_PER_SECOND"
	envWebSendOriginalQuestion    = "DSHD_WEB_SEND_ORIGINAL_QUESTION"
	envWebSearchBilling           = "DSHD_WEB_SEARCH_BILLING"
	envWebFetchBilling            = "DSHD_WEB_FETCH_BILLING"
	envWebSearchAPIBaseURL        = "DSHD_WEB_SEARCH_API_BASE_URL"
	envWebSearchAPIKeyEnv         = "DSHD_WEB_SEARCH_API_KEY_ENV"
	envWebFetchAPIBaseURL         = "DSHD_WEB_FETCH_API_BASE_URL"
	envWebFetchAPIKeyEnv          = "DSHD_WEB_FETCH_API_KEY_ENV"
	envWebLLMProvider             = "DSHD_WEB_LLM_PROVIDER"
	envWebLegacyBaseURL           = "DSHD_WEB_BASE_URL"
	envWebLegacyAPIKeyEnv         = "DSHD_WEB_API_KEY_ENV"
	envWebLegacyAPIKey            = "DSHD_WEB_API_KEY"
)

// Billing 是一处能力（search / fetch）的计费方式（官方 `index.ts:80-82`）。
type Billing string

const (
	// BillingAuto 跟随对话：DeepSeek 模型路由维持会话级行为，其他会话/会话外
	// 先用 API key、没有 key 才用登录账号（官方 `index.ts:247-259`）。
	BillingAuto Billing = "auto"
	// BillingAccount 只用登录账号。
	BillingAccount Billing = "account"
	// BillingAPIKey 只用 API key。
	BillingAPIKey Billing = "api-key"
)

// WebAPISettings 是 `dsh-web-deepseek-api` 那一份设置（官方 Config 的宿主等价物）。
//
// 它是**跨能力**的：search 与 fetch 各自从里面取自己那三个字段
// （`searchBilling` / `searchBaseURL` / `searchApiKeyEnv` 或 fetch 的三个）。
type WebAPISettings struct {
	// Count 是每次搜索的结果数（1..50，默认 10）。
	Count int
	// RequestsPerSecond 是一秒窗口内最多发出的搜索请求数（>=1，默认 5）。
	RequestsPerSecond int
	// SendOriginalQuestion 决定是否把触发这次搜索的用户问题作为 ori_question 发出。
	SendOriginalQuestion bool
	// SearchBilling / FetchBilling 是两处能力各自的计费方式（默认 auto）。
	SearchBilling Billing
	FetchBilling  Billing
	// SearchBaseURL / SearchAPIKeyEnv / FetchBaseURL / FetchAPIKeyEnv 是能力自有端点与凭据引用。
	SearchBaseURL   string
	SearchAPIKeyEnv string
	FetchBaseURL    string
	FetchAPIKeyEnv  string
	// LLMProvider 是这一轮对话的模型路由 id（`deepseek-account` / `deepseek-official` / 其他）。
	//
	// 官方从 `session.requestContext().provider` 取（`index.ts:176`）；宿主把它作为接缝
	// 传进来（环境变量 `DSHD_WEB_LLM_PROVIDER`），为空 = 会话外/非 DeepSeek 路由。
	LLMProvider string
}

// WebAPISettingsFromEnv 按官方默认值与边界解析设置。
//
// 非法值一律回落官方默认（本仓既有约定：写坏配置不该把能力变成"永远拒绝"）。
func WebAPISettingsFromEnv(getenv func(string) string) WebAPISettings {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	settings := WebAPISettings{
		Count:                DeepSeekDefaultCount,
		RequestsPerSecond:    DeepSeekSearchRequestsPerSecond,
		SendOriginalQuestion: true,
		SearchBilling:        BillingAuto,
		FetchBilling:         BillingAuto,
		SearchBaseURL:        strings.TrimSpace(getenv(envWebSearchAPIBaseURL)),
		SearchAPIKeyEnv:      strings.TrimSpace(getenv(envWebSearchAPIKeyEnv)),
		FetchBaseURL:         strings.TrimSpace(getenv(envWebFetchAPIBaseURL)),
		FetchAPIKeyEnv:       strings.TrimSpace(getenv(envWebFetchAPIKeyEnv)),
		LLMProvider:          strings.TrimSpace(getenv(envWebLLMProvider)),
	}
	if count, ok := parseBoundedInt(getenv(envWebSearchCount), 1, searchCountMax); ok {
		settings.Count = count
	}
	if rps, ok := parseBoundedInt(getenv(envWebSearchRequestsPerSecond), 1, 0); ok {
		settings.RequestsPerSecond = rps
	}
	if raw := strings.TrimSpace(getenv(envWebSendOriginalQuestion)); raw != "" {
		if value, ok := parseBool(raw); ok {
			settings.SendOriginalQuestion = value
		}
	}
	settings.SearchBilling = parseBilling(getenv(envWebSearchBilling), BillingAuto)
	settings.FetchBilling = parseBilling(getenv(envWebFetchBilling), BillingAuto)
	return settings
}

// parseBoundedInt 读一个区间内的整数；空/非法/越界都回答 false（调用方回落默认）。
// max <= 0 表示没有上界。
func parseBoundedInt(raw string, min, max int) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || (max > 0 && value > max) {
		return 0, false
	}
	return value, true
}

// parseBool 认常见的布尔写法；其他一律非法。
func parseBool(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// parseBilling 只认官方闭集 `auto | account | api-key`；其他回落 fallback。
func parseBilling(raw string, fallback Billing) Billing {
	switch Billing(strings.ToLower(strings.TrimSpace(raw))) {
	case BillingAuto:
		return BillingAuto
	case BillingAccount:
		return BillingAccount
	case BillingAPIKey:
		return BillingAPIKey
	default:
		return fallback
	}
}

// WebAPIConfig 是一处能力在**一次操作开始时**读到的配置快照
// （官方 `resolveOptions()`：the options for the NEXT search/fetch，读一次不混两份设置）。
type WebAPIConfig struct {
	// Count / RequestsPerSecond 只有 search 用；fetch 忽略。
	Count             int
	RequestsPerSecond int
	// SendOriginalQuestion 只有 search 用；fetch 忽略。
	SendOriginalQuestion bool
	// Billing 是这处能力的计费方式。
	Billing Billing
	// BaseURL / ApiKeyEnv 是这处能力自有的端点与凭据引用（空 = 未设置）。
	BaseURL   string
	ApiKeyEnv string
	// LLMProvider 是这一轮的模型路由 id（决定 auto 的会话级行为）。
	LLMProvider string
}

// WebAPIAccount 是**登录账号**这一路凭据的宿主接缝。
//
// 官方用账号服务按目标端点申请令牌，并在 401 时撤销被拒的令牌
// （`transport.ts:29-39`）。本宿主账号服务在 `internal/account`，模块侧没有跨域读取
// 通道（`internal/spi` 没有账号服务键），因此这里是显式注入点：零值 = 账号一路不可用
// （provider 会如实报"没有登录账号"，绝不假装有令牌）。
type WebAPIAccount struct {
	// BaseURL 是账号路由的 origin 基址（含路径前缀）；空时用 `DEEPSEEK_BASE_URL`，
	// 再空则用官方 origin（`index.ts:275-291`）。
	BaseURL string
	// Configured 只回答"有没有可申请的令牌"，不读值、不联网。
	Configured func() bool
	// ResolveToken 为目标端点申请令牌；空串 = 没有令牌。
	ResolveToken func(ctx context.Context, endpoint string) (string, error)
	// RejectToken 撤销被 401 拒绝的那一个令牌；失败不能盖掉原始错误。
	RejectToken func(ctx context.Context, token string) error
}

// DeepSeekWebAPIOptions 是一次 search/fetch 的完整输入快照。
type DeepSeekWebAPIOptions struct {
	// Config 是这处能力的配置。
	Config WebAPIConfig
	// Getenv 是凭据/端点的读取接缝（生产 = os.Getenv）。
	Getenv func(string) string
	// Account 是登录账号凭据源；零值 = 未接登录账号。
	Account WebAPIAccount
	// OfficialBaseURL 覆盖"官方 DeepSeek API 路由"的 base（测试与自建端点用）；
	// 空 = DeepSeekWebAPIBaseURL。
	OfficialBaseURL string
}

// getenv 是 nil 安全的读取器。
func (o DeepSeekWebAPIOptions) getenv(name string) string {
	if o.Getenv == nil {
		return ""
	}
	return o.Getenv(name)
}

// DeepSeekSearchAPIOptionsFromEnv 按环境造 search 侧的快照读取器输入。
func DeepSeekSearchAPIOptionsFromEnv(getenv func(string) string) DeepSeekWebAPIOptions {
	settings := WebAPISettingsFromEnv(getenv)
	return DeepSeekWebAPIOptions{
		Config: WebAPIConfig{
			Count:                settings.Count,
			RequestsPerSecond:    settings.RequestsPerSecond,
			SendOriginalQuestion: settings.SendOriginalQuestion,
			Billing:              settings.SearchBilling,
			BaseURL:              settings.SearchBaseURL,
			ApiKeyEnv:            settings.SearchAPIKeyEnv,
			LLMProvider:          settings.LLMProvider,
		},
		Getenv: getenv,
	}
}

// DeepSeekFetchAPIOptionsFromEnv 按环境造 fetch 侧的快照读取器输入。
func DeepSeekFetchAPIOptionsFromEnv(getenv func(string) string) DeepSeekWebAPIOptions {
	settings := WebAPISettingsFromEnv(getenv)
	return DeepSeekWebAPIOptions{
		Config: WebAPIConfig{
			Billing:     settings.FetchBilling,
			BaseURL:     settings.FetchBaseURL,
			ApiKeyEnv:   settings.FetchAPIKeyEnv,
			LLMProvider: settings.LLMProvider,
		},
		Getenv: getenv,
	}
}
