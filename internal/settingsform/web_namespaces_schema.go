package settingsform

// 官方 web 能力的三条设置命名空间：`web`、`web-deepseek-api`、`web-search-deepseek`。
//
// # 为什么这三条必须存在
//
// 官方 UI 包 `dsh-client-ui-settings-web-search` 的页面在
// `src/client/index.ts:78` 用 `ctx.configForms.whileServed([WEB_NS], ...)` 决定
// **整页出不出来**（`WEB_NS = 'web'`），两个 provider 的专属控件再各自看
// `web-deepseek-api` / `web-search-deepseek` 是否被服务。所以只补 `web` 能让页面
// 出现，但"结果数 / 计费方式 / 旧的 Messages 搜索"三块会缺——三条必须一起发
// （GUI 侧 4 条 MISSING 设置页之一）。
//
// # 权威源（随 rc.2 包发布的 TypeScript 原件）
//
//	<official-rc2>/dsh/node_modules/@deepseek-ai/
//	  dsh-web/src/index.ts:88-91
//	    static Config = z.object({
//	      searchProvider: z.string().volatile(),
//	      fetchProvider: z.string().volatile(),
//	    })
//	  dsh-web-deepseek-api/src/index.ts:99-109
//	    export const Config = z.object({
//	      count: z.number().step(1).min(1).max(50).default(DEEPSEEK_DEFAULT_COUNT).volatile(),
//	      searchRequestsPerSecond: z.number().step(1).min(1).default(DEEPSEEK_SEARCH_REQUESTS_PER_SECOND).volatile(),
//	      sendOriginalQuestion: z.boolean().default(true).volatile(),
//	      searchBilling: z.union(['auto','account','api-key']).default('auto').volatile(),
//	      fetchBilling: z.union(['auto','account','api-key']).default('auto').volatile(),
//	      searchBaseURL: z.string().volatile(),
//	      searchApiKeyEnv: z.string().role('credential-ref').volatile(),
//	      fetchBaseURL: z.string().volatile(),
//	      fetchApiKeyEnv: z.string().role('credential-ref').volatile(),
//	    })
//	  dsh-web-deepseek-api/src/search.ts:19,22
//	    DEEPSEEK_DEFAULT_COUNT = 10 / DEEPSEEK_SEARCH_REQUESTS_PER_SECOND = 5
//	  dsh-web-search-deepseek/src/index.ts:71-79
//	    export const Config = z.object({
//	      apiKey: z.string().role('secret').volatile(),
//	      apiKeyEnv: z.string().role('credential-ref').default('DEEPSEEK_API_KEY').volatile(),
//	      baseURL: z.string().volatile(),
//	      model: z.string().default('deepseek-v4-flash').volatile(),
//	      apiVersion: z.string().default('2023-06-01').volatile(),
//	      maxTokens: z.number().default(4096).volatile(),
//	      maxUses: z.number().step(1).min(1).default(5).volatile(),
//	    })
//	  dsh-web-search-deepseek/src/provider.ts:37,40,46
//	    DEEPSEEK_DEFAULT_MODEL='deepseek-v4-flash' / DEEPSEEK_DEFAULT_API_VERSION='2023-06-01'
//	    DEEPSEEK_DEFAULT_MAX_USES = 5
//	  dsh-client-ui-settings-web-search/src/client/web-search-card-controller.ts:52-56
//	    WEB_SEARCH_NS='web-search-deepseek' / WEB_API_NS='web-deepseek-api' / WEB_NS='web'
//
// # 线上 envelope（实跑取证）
//
// tmp/GO8-RC2/_hs4/dump-hs4.mjs：`web` 直接 require 官方
// `dsh-web/lib/index.js` 的 `Config.toJSON()`；另两条按上面 zod 声明逐行重建
// （官方那两个包 require 时缺第三方依赖 eventsource-parser，见脚本注释）。
// 结果见 _hs4/official-envelopes-hs4.json，要点：
//
//	searchBaseURL / fetchBaseURL / baseURL   {"type":"string","meta":{}}
//	*ApiKeyEnv（credential-ref）              {"type":"string","meta":{"role":"credential-ref"}}
//	apiKey（secret）                          {"type":"string","meta":{"role":"secret"}}
//	maxTokens / timeoutMs 之类裸 number       只有 meta.default
//
// `apiKey` 是 **secret**：官方 plainSchema 会删掉它的 default/required，值本身
// 从不上线（本仓 secret 槽位只报"设没设"，见 settingsform.makeView 的 secretPaths）。
//
// # 本仓的消费方（真生效）
//
// modules/search 的每个 provider 都是从**同一个 getenv 接缝**读配置的
// （modules/search/search.go:83-105、modules/search/webapi_config.go:132-160、
// modules/search/deepseek.go:64-84）。cmd/dshd 在装配时把这份设置叠在环境变量
// **前面**（cmd/dshd/settings_effective.go 的 settingsEnvOverlay），于是：
//
//	web.searchProvider                 → DSHD_WEB_SEARCH_PROVIDER
//	web.fetchProvider                  → DSHD_WEB_FETCH_PROVIDER
//	web-deepseek-api.count             → DSHD_WEB_SEARCH_COUNT
//	web-deepseek-api.searchRequestsPerSecond → DSHD_WEB_SEARCH_REQUESTS_PER_SECOND
//	web-deepseek-api.sendOriginalQuestion    → DSHD_WEB_SEND_ORIGINAL_QUESTION
//	web-deepseek-api.searchBilling/fetchBilling → DSHD_WEB_SEARCH_BILLING / DSHD_WEB_FETCH_BILLING
//	web-deepseek-api.searchBaseURL/fetchBaseURL → DSHD_WEB_SEARCH_API_BASE_URL / DSHD_WEB_FETCH_API_BASE_URL
//	web-deepseek-api.searchApiKeyEnv/fetchApiKeyEnv → 同上（凭据引用名）
//	web-search-deepseek.baseURL        → DEEPSEEK_SEARCH_BASE_URL
//	web-search-deepseek.model          → DEEPSEEK_SEARCH_MODEL
//	web-search-deepseek.maxUses        → DSHD_SEARCH_MAX_USES
//
// 缺口（如实登记）：`web-search-deepseek.apiKey` / `apiKeyEnv` 在本仓**没有**
// 真正的凭据解析器——官方按 credential-ref 名字去凭据服务取值，本仓
// modules/search/deepseek.go:77 固定读 DEEPSEEK_API_KEY / DSHD_SEARCH_API_KEY
// 两个环境变量，所以这两个字段可读写、能往返，但不改变行为；`apiVersion` 同理
// （宿主写死 DeepSeekDefaultAPIVersion）。

const (
	// NamespaceWeb 是官方 web 服务的命名空间（逐字）。
	NamespaceWeb = "web"
	// NamespaceWebDeepSeekAPI 是官方 Search/Fetch API provider 的命名空间（逐字）。
	NamespaceWebDeepSeekAPI = "web-deepseek-api"
	// NamespaceWebSearchDeepSeek 是官方旧 Messages 搜索 provider 的命名空间（逐字）。
	NamespaceWebSearchDeepSeek = "web-search-deepseek"
)

const (
	// FieldSearchProvider / FieldFetchProvider 是两个能力各自的 provider id。
	FieldSearchProvider = "searchProvider"
	FieldFetchProvider  = "fetchProvider"
	// FieldWebCount 是每次搜索的结果数。
	FieldWebCount = "count"
	// FieldWebRequestsPerSecond 是一秒窗口内的搜索请求上限。
	FieldWebRequestsPerSecond = "searchRequestsPerSecond"
	// FieldWebSendOriginalQuestion 决定是否把用户问题作为 ori_question 发出。
	FieldWebSendOriginalQuestion = "sendOriginalQuestion"
	// FieldWebSearchBilling / FieldWebFetchBilling 是两处能力的计费方式。
	FieldWebSearchBilling = "searchBilling"
	FieldWebFetchBilling  = "fetchBilling"
	// FieldWebSearchBaseURL / FieldWebSearchAPIKeyEnv 是 search 自有端点与凭据引用。
	FieldWebSearchBaseURL   = "searchBaseURL"
	FieldWebSearchAPIKeyEnv = "searchApiKeyEnv"
	// FieldWebFetchBaseURL / FieldWebFetchAPIKeyEnv 是 fetch 自有端点与凭据引用。
	FieldWebFetchBaseURL   = "fetchBaseURL"
	FieldWebFetchAPIKeyEnv = "fetchApiKeyEnv"
	// FieldWebLegacyAPIKey 是旧 Messages 搜索的凭据本体（secret，值不上线）。
	FieldWebLegacyAPIKey = "apiKey"
	// FieldWebLegacyAPIKeyEnv 是旧 Messages 搜索的凭据引用名。
	FieldWebLegacyAPIKeyEnv = "apiKeyEnv"
	// FieldWebLegacyBaseURL 是旧 Messages 搜索的端点根。
	FieldWebLegacyBaseURL = "baseURL"
	// FieldWebLegacyModel 是旧 Messages 搜索用的模型 id。
	FieldWebLegacyModel = "model"
	// FieldWebLegacyAPIVersion 是 anthropic-version 头。
	FieldWebLegacyAPIVersion = "apiVersion"
	// FieldWebLegacyMaxTokens 是辅助搜索请求的输出上限。
	FieldWebLegacyMaxTokens = "maxTokens"
	// FieldWebLegacyMaxUses 是单次请求里原生 web_search 的最大使用次数。
	FieldWebLegacyMaxUses = "maxUses"
)

const (
	// DefaultWebCount 是官方默认结果数（search.ts:19）。
	DefaultWebCount = 10
	// WebCountMax 是官方 schema 上限（index.ts:100 的 `.max(50)`）。
	WebCountMax = 50
	// DefaultWebRequestsPerSecond 是官方默认限速（search.ts:22）。
	DefaultWebRequestsPerSecond = 5
	// DefaultWebSendOriginalQuestion 是官方默认（index.ts:101）。
	DefaultWebSendOriginalQuestion = true
	// DefaultWebBilling 是官方默认计费方式（index.ts:102-103 的 `.default('auto')`）。
	DefaultWebBilling = "auto"
	// DefaultWebLegacyAPIKeyEnv 是官方默认凭据引用（web-search-deepseek/src/index.ts:72）。
	DefaultWebLegacyAPIKeyEnv = "DEEPSEEK_API_KEY"
	// DefaultWebLegacyModel 是官方默认模型 id（provider.ts:37）。
	DefaultWebLegacyModel = "deepseek-v4-flash"
	// DefaultWebLegacyAPIVersion 是官方默认头值（provider.ts:40）。
	DefaultWebLegacyAPIVersion = "2023-06-01"
	// DefaultWebLegacyMaxTokens 是官方默认输出上限（index.ts:76 的 `.default(4096)`）。
	DefaultWebLegacyMaxTokens = 4096
	// DefaultWebLegacyMaxUses 是官方默认 server-tool 使用次数（provider.ts:46）。
	DefaultWebLegacyMaxUses = 5
	// MinWebUses 是官方 `maxUses` 下限（index.ts:78 的 `.min(1)`）。
	MinWebUses = 1
	// MinWebRequestsPerSecond 是官方下限（index.ts:101 的 `.min(1)`）。
	MinWebRequestsPerSecond = 1
)

// WebBillings 是官方 `WebBilling` 的三个取值（index.ts:63,72 的 union 顺序）。
var WebBillings = []string{"auto", "account", "api-key"}

// webNamespace 是 `web` 的声明：只有两条 provider 选择，**官方没有默认值**
// （`z.string().volatile()` 不带 default，值是"未钉死"，由部署组合决定）。
func webNamespace() uiNamespace {
	return uiNamespace{
		ns:    NamespaceWeb,
		title: "Web providers",
		fields: []uiField{
			uiStringField(FieldSearchProvider, nil),
			uiStringField(FieldFetchProvider, nil),
		},
		// 官方的默认就是"不钉死"：base 里一个键都不写（与 locale.preference 同形）。
		base: map[string]any{},
	}
}

// webDeepSeekAPINamespace 是 `web-deepseek-api` 的声明（9 个字段，一个不多）。
func webDeepSeekAPINamespace() uiNamespace {
	return uiNamespace{
		ns:    NamespaceWebDeepSeekAPI,
		title: "DeepSeek search and fetch API",
		fields: []uiField{
			uiIntegerField(FieldWebCount, 1, WebCountMax, DefaultWebCount),
			// searchRequestsPerSecond 官方只有下限，没有上限（index.ts:101）。
			uiMinOnlyIntegerField(FieldWebRequestsPerSecond, MinWebRequestsPerSecond, DefaultWebRequestsPerSecond),
			uiBooleanField(FieldWebSendOriginalQuestion, DefaultWebSendOriginalQuestion),
			uiEnumField(FieldWebSearchBilling, WebBillings, DefaultWebBilling),
			uiEnumField(FieldWebFetchBilling, WebBillings, DefaultWebBilling),
			// 两个自有端点与两个凭据引用：官方无默认值（未设置 = 用官方 base / 官方 key）。
			uiStringField(FieldWebSearchBaseURL, nil),
			uiStringField(FieldWebSearchAPIKeyEnv, nil).withRole(uiRoleCredentialRef),
			uiStringField(FieldWebFetchBaseURL, nil),
			uiStringField(FieldWebFetchAPIKeyEnv, nil).withRole(uiRoleCredentialRef),
		},
		base: map[string]any{
			FieldWebCount:                DefaultWebCount,
			FieldWebRequestsPerSecond:    DefaultWebRequestsPerSecond,
			FieldWebSendOriginalQuestion: DefaultWebSendOriginalQuestion,
			FieldWebSearchBilling:        DefaultWebBilling,
			FieldWebFetchBilling:         DefaultWebBilling,
		},
	}
}

// webSearchDeepSeekNamespace 是 `web-search-deepseek` 的声明（7 个字段）。
func webSearchDeepSeekNamespace() uiNamespace {
	return uiNamespace{
		ns:    NamespaceWebSearchDeepSeek,
		title: "DeepSeek web search (Messages)",
		fields: []uiField{
			// apiKey 是 secret：envelope 只有 meta.role='secret'（没有 default/required），
			// 值本身从不上线；settings.describe 只在 secrets 槽位报"设没设"。
			uiStringField(FieldWebLegacyAPIKey, nil).withRole(uiRoleSecret),
			uiStringField(FieldWebLegacyAPIKeyEnv, DefaultWebLegacyAPIKeyEnv).withRole(uiRoleCredentialRef),
			uiStringField(FieldWebLegacyBaseURL, nil),
			uiStringField(FieldWebLegacyModel, DefaultWebLegacyModel),
			uiStringField(FieldWebLegacyAPIVersion, DefaultWebLegacyAPIVersion),
			// maxTokens 官方是裸 number（index.ts:76），没有 step/min/max。
			uiBareNumberField(FieldWebLegacyMaxTokens, DefaultWebLegacyMaxTokens),
			uiMinOnlyIntegerField(FieldWebLegacyMaxUses, MinWebUses, DefaultWebLegacyMaxUses),
		},
		base: map[string]any{
			FieldWebLegacyAPIKeyEnv:  DefaultWebLegacyAPIKeyEnv,
			FieldWebLegacyModel:      DefaultWebLegacyModel,
			FieldWebLegacyAPIVersion: DefaultWebLegacyAPIVersion,
			FieldWebLegacyMaxTokens:  DefaultWebLegacyMaxTokens,
			FieldWebLegacyMaxUses:    DefaultWebLegacyMaxUses,
		},
		check: func(value map[string]any) error {
			return checkBareNumberPositive(NamespaceWebSearchDeepSeek, FieldWebLegacyMaxTokens, value)
		},
	}
}

// checkBareNumberPositive 给"官方裸 number、运行期才判正"的字段补一条写路径判据。
//
// `maxTokens` 在官方是 `z.number().default(4096)`：schema 不设界，但 0 或负数
// 在真实请求里等于"不许模型输出"，那是坏值而不是"关闭"。所以写路径直接
// `invalid_value`，不写进去等下一次请求才炸。
func checkBareNumberPositive(ns, field string, value map[string]any) error {
	raw, present := value[field]
	if !present || raw == nil {
		return nil
	}
	number, ok := integerValue(raw)
	if !ok {
		return newInvalidTypeError([]string{ns, field}, "integer", raw)
	}
	if number <= 0 {
		return &Error{
			Code:    "invalid_value",
			Message: joinPath([]string{ns, field}) + " 必须是正整数，收到 " + formatInt(number),
			Hint:    "取一个正整数（官方默认 4096）",
			Status:  400,
		}
	}
	return nil
}

// buildWebNamespaceSpecs 返回三条 web 命名空间（顺序与官方页面的服务顺序一致：
// web 先，两个 provider 后）。
func buildWebNamespaceSpecs() []NamespaceSpec {
	return []NamespaceSpec{
		uiNamespaceSpec(webNamespace()),
		uiNamespaceSpec(webDeepSeekAPINamespace()),
		uiNamespaceSpec(webSearchDeepSeekNamespace()),
	}
}
