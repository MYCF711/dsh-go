package websearch

// 本文件是官方 0.2.2-rc.2 `web.listProviders` 的**宿主真值推导**：
// 清单从真实装配（`modules/search` 的搜索 provider + `modules/tool/webfetch` 的匿名
// http）与真实环境/凭据投影出来，不是一张写死的 id 表。
//
// 为什么要在 serve 进程里重新装一遍接缝：宿主的工具执行在子进程里（`dshd run`），
// 而 `web.listProviders` 是 HTTP 面（`dshd serve`）。两个进程看不到彼此的内存注册表，
// 但**装配代码和启动环境是同一份**——`HostProviders` 调的就是生产那两个模块用的
// 构造函数与注册表达式，因此投影与真值同源；任何一侧改了装配，这里跟着变，
// 不存在"两套清单互相漂"的问题（`TestRC2CatalogDerivesFromRealComposition` 钉住）。
//
// 官方契约（只读）：
//   - `dsh-web/src/index.ts:146-157` `listProviders(): WebProviderList`
//   - `dsh-web/src/types.ts:196-217` `WebProviderInfo` / `WebProviderList`
//   - `dsh-web/src/index.ts:246-252` 没声明的 title/description **缺席**
//   - `dsh-web/src/index.ts:274-297` 选择规则与 `WEB_PROVIDER_*` 错误码
//   - `dsh-web/src/index.ts:100-101` `DSH_WEB_SEARCH_PROVIDER` / `DSH_WEB_FETCH_PROVIDER`
//   - rc.2 基础组合默认 `searchProvider: deepseek-search-api` / `fetchProvider: http`
//     （`dsh-base/cordis.patch.yml:487-488`；rc.1 在 `:470-471`，搜索是 deepseek-official）

import (
	"strings"
	"time"

	"dshgo/internal/spi"
	"dshgo/modules/search"
	"dshgo/modules/tool/webfetch"
)

// ProviderDefaults 是一个组合给两个能力钉的默认 provider id。
type ProviderDefaults struct {
	Search string
	Fetch  string
}

// RC2ProviderDefaults 是官方 0.2.2-rc.2 基础组合的默认 provider
// （`dsh-base/cordis.patch.yml:487-488`）。rc.1 是 Search=deepseek-official（`:470`）。
var RC2ProviderDefaults = ProviderDefaults{
	Search: baseBundleSearchProvider,
	Fetch:  webfetch.HTTPProviderID,
}

// CatalogEntry 是清单里的一条 provider。
//
// 前三个字段是官方 `WebProviderInfo`；我们的 provider 目前不声明 title/description，
// 因此保持空（官方语义：没声明就缺席），不编一个看起来像官方的显示名出来。
type CatalogEntry struct {
	ID          string
	Title       string
	Description string
	// Available 是 provider 自己 `Available()` 的回答（真实运行态）。
	Available bool
	// Default 表示它就是当前配置钉死（或基线默认生效）的 provider。
	Default bool
	// Selected 表示执行期解析当前会选中的那个 provider。
	Selected bool
}

// CapabilityState 是一个能力的"当前选择"。
type CapabilityState struct {
	// Configured 是钉死的 provider id；空 = 未钉死。
	Configured string
	// Source 说明 Configured 从哪来：env / config / auto。
	Source string
	// Selected 是执行期真正会选中的 id；选择失败时为空。
	Selected string
	// ErrorCode 是选择失败时的官方 web 码（WEB_PROVIDER_*）。
	ErrorCode string
	// Error 是选择失败的可读说明。
	Error string
}

// Baseline 报告 rc.2 基线默认 provider 以及本宿主是否注册了它。
type Baseline struct {
	Search           string
	SearchRegistered bool
	Fetch            string
	FetchRegistered  bool
}

// Limits 是 `web_search` 的生效上限（官方 `tool-web` 配置面）。
type Limits struct {
	MaxQueries      int
	MaxResults      int
	SummaryMaxChars int
	SearchTimeout   time.Duration
}

// Catalog 是 `web.listProviders` 的宿主投影。
type Catalog struct {
	Search []CatalogEntry
	Fetch  []CatalogEntry

	SearchSelection CapabilityState
	FetchSelection  CapabilityState

	Baseline Baseline
	Limits   Limits
	// Credentials 是这套组合会读的凭据/端点/选择变量及它们**当前是否已配置**。
	Credentials []CredentialState
}

// CredentialState 是一个环境变量的真实配置状态。
type CredentialState struct {
	// Name 是环境变量名（官方与宿主前缀都列，二者喂同一个字段）。
	Name string
	// Configured 是"这个变量当前非空"（真读环境，不是猜）。
	Configured bool
	// Role 说明它管什么：selection / endpoint / credential / model / billing / limit / route / legacy。
	Role string
	// Capability 是 search / fetch / both。
	Capability string
}

// credentialEnvNames 是这套组合真实读取的变量（取值来自各模块的构造函数）。
//
// 顺序固定，便于界面稳定渲染与测试逐条核对。
var credentialEnvNames = []CredentialState{
	{Name: "DSHD_WEB_SEARCH_PROVIDER", Role: "selection", Capability: "search"},
	{Name: "DSH_WEB_SEARCH_PROVIDER", Role: "selection", Capability: "search"},
	{Name: "DSHD_WEB_FETCH_PROVIDER", Role: "selection", Capability: "fetch"},
	{Name: "DSH_WEB_FETCH_PROVIDER", Role: "selection", Capability: "fetch"},
	{Name: "DSHD_SEARCH_URL", Role: "endpoint", Capability: "search"},
	{Name: "DEEPSEEK_API_KEY", Role: "credential", Capability: "search"},
	{Name: "DSHD_SEARCH_API_KEY", Role: "credential", Capability: "search"},
	{Name: "DEEPSEEK_SEARCH_BASE_URL", Role: "endpoint", Capability: "search"},
	{Name: "DSHD_SEARCH_BASE_URL", Role: "endpoint", Capability: "search"},
	{Name: "DEEPSEEK_SEARCH_MODEL", Role: "model", Capability: "search"},
	{Name: "DSHD_SEARCH_MODEL", Role: "model", Capability: "search"},

	// rc.2 `dsh-web-deepseek-api` 的九个设置（真源：`modules/search.WebAPISettingsFromEnv`）。
	// 官方字段名 → 宿主环境变量在那一侧逐条注明；这里跟着真实装配走。
	{Name: "DSHD_WEB_SEARCH_COUNT", Role: "limit", Capability: "search"},
	{Name: "DSHD_WEB_SEARCH_REQUESTS_PER_SECOND", Role: "limit", Capability: "search"},
	{Name: "DSHD_WEB_SEND_ORIGINAL_QUESTION", Role: "limit", Capability: "search"},
	{Name: "DSHD_WEB_SEARCH_BILLING", Role: "billing", Capability: "search"},
	{Name: "DSHD_WEB_FETCH_BILLING", Role: "billing", Capability: "fetch"},
	{Name: "DSHD_WEB_SEARCH_API_BASE_URL", Role: "endpoint", Capability: "search"},
	{Name: "DSHD_WEB_SEARCH_API_KEY_ENV", Role: "credential", Capability: "search"},
	{Name: "DSHD_WEB_FETCH_API_BASE_URL", Role: "endpoint", Capability: "fetch"},
	{Name: "DSHD_WEB_FETCH_API_KEY_ENV", Role: "credential", Capability: "fetch"},
	{Name: "DSHD_WEB_LLM_PROVIDER", Role: "route", Capability: "both"},

	// 旧字段（官方 rc.1 的 baseURL / apiKeyEnv / apiKey）：**读得到但不生效**。
	// 官方 rc.2 明确它们已不是 schema 字段（`src/index.ts:61-62`：load without error、
	// no effect）。列出来是为了让界面能告诉用户"你设的这一条不会改变任何行为"。
	{Name: "DSHD_WEB_BASE_URL", Role: "legacy", Capability: "both"},
	{Name: "DSHD_WEB_API_KEY_ENV", Role: "legacy", Capability: "both"},
	{Name: "DSHD_WEB_API_KEY", Role: "legacy", Capability: "both"},
}

// HostProviders 按**生产同一份装配代码**造出 serve 进程侧的接缝镜像。
//
// 搜索侧来自 `modules/search.NewWebProvidersFromEnv`（与 run 进程同一个函数），
// 抓取侧就是 `modules/tool/webfetch` 注册的匿名 http（同一个表达式）。
// provider 的可用性由它们自己的 `Available()` 判定，这里不做二次推导。
func HostProviders(getenv func(string) string) (*spi.WebProviders, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	seam, err := search.NewWebProvidersFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if err := seam.RegisterFetch(&webfetch.HTTPProvider{}); err != nil {
		return nil, err
	}
	return seam, nil
}

// DescribeCatalog 从真实装配投影出清单（`web.listProviders` 的真值）。
func DescribeCatalog(getenv func(string) string) (Catalog, error) {
	seam, err := HostProviders(getenv)
	if err != nil {
		return Catalog{}, err
	}
	return DescribeCatalogOf(seam, getenv), nil
}

// DescribeCatalogOf 从给定接缝投影清单（替换实现/测试用）。
//
// getenv 同时决定"生效上限"（DSHD_WEB_SEARCH_* 与 web_search 工具同一份解析函数）。
func DescribeCatalogOf(web *spi.WebProviders, getenv func(string) string) Catalog {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if web == nil {
		// 空接缝：所有能力都"没有可用 provider"，但清单本身仍然是合法的（两个空数组），
		// 让调用方拿到的是结构化状态而不是 panic 或 null。
		web = spi.NewWebProviders("", "")
	}
	tool := &Tool{Getenv: getenv}
	catalog := Catalog{
		Search: entriesOf(web.SearchProviderStatuses()),
		Fetch:  entriesOf(web.FetchProviderStatuses()),
		SearchSelection: selectionOf(web.SearchPin(), searchPinSource(web.SearchPin(), getenv), func() (string, error) {
			provider, err := web.SelectSearch()
			if err != nil {
				return "", err
			}
			return provider.ID(), nil
		}),
		FetchSelection: selectionOf(web.FetchPin(), fetchPinSource(web.FetchPin(), getenv), func() (string, error) {
			provider, err := web.SelectFetch()
			if err != nil {
				return "", err
			}
			return provider.ID(), nil
		}),
		Limits: Limits{
			MaxQueries:      tool.queryLimit(),
			MaxResults:      tool.sourceLimit(),
			SummaryMaxChars: tool.summaryMaxChars(),
			SearchTimeout:   tool.searchBudget(""),
		},
		Credentials: credentialsOf(getenv),
	}
	catalog.Baseline = Baseline{
		Search:           RC2ProviderDefaults.Search,
		SearchRegistered: hasID(catalog.Search, RC2ProviderDefaults.Search),
		Fetch:            RC2ProviderDefaults.Fetch,
		FetchRegistered:  hasID(catalog.Fetch, RC2ProviderDefaults.Fetch),
	}
	// "当前默认"= 钉死的那个；没钉死时 = rc.2 基线默认（且本宿主确实注册了它）。
	for i := range catalog.Search {
		entry := &catalog.Search[i]
		switch {
		case catalog.SearchSelection.Configured != "":
			entry.Default = entry.ID == catalog.SearchSelection.Configured
		default:
			entry.Default = entry.ID == catalog.Baseline.Search
		}
		entry.Selected = entry.ID == catalog.SearchSelection.Selected
	}
	for i := range catalog.Fetch {
		entry := &catalog.Fetch[i]
		switch {
		case catalog.FetchSelection.Configured != "":
			entry.Default = entry.ID == catalog.FetchSelection.Configured
		default:
			entry.Default = entry.ID == catalog.Baseline.Fetch
		}
		entry.Selected = entry.ID == catalog.FetchSelection.Selected
	}
	return catalog
}

func entriesOf(statuses []spi.ProviderStatus) []CatalogEntry {
	out := make([]CatalogEntry, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, CatalogEntry{ID: status.ID, Available: status.Available})
	}
	return out
}

// selectionOf 跑一次官方的执行期选择规则，把结果（或结构化失败码）投影出来。
func selectionOf(configured, source string, resolve func() (string, error)) CapabilityState {
	state := CapabilityState{Configured: configured, Source: source}
	selected, err := resolve()
	if err != nil {
		state.ErrorCode = spi.WebErrorCode(err)
		state.Error = err.Error()
		return state
	}
	state.Selected = selected
	return state
}

// searchPinSource 说明钉死的 id 从哪来：环境变量、rc.2 基础组合默认，还是直接改的配置。
//
// `baseline` 是 rc.2 新增的第四种来源：`modules/search` 在**没被环境钉死**、且
// 基础组合默认 provider（`deepseek-search-api`）当下可用时会把它作为默认 pin，
// 所以 rc.2 的默认值真生效、且不是用户配置（官方把它配在 `cordis.patch.yml:487`）。
func searchPinSource(pin string, getenv func(string) string) string {
	if pin == "" {
		return "auto"
	}
	if spi.WebSearchPinFromEnv(getenv) == pin {
		return "env"
	}
	if pin == search.BaseBundleSearchProviderID {
		return "baseline"
	}
	return "config"
}

func fetchPinSource(pin string, getenv func(string) string) string {
	if pin == "" {
		return "auto"
	}
	if spi.WebFetchPinFromEnv(getenv) == pin {
		return "env"
	}
	if pin == search.BaseBundleFetchProviderID {
		return "baseline"
	}
	return "config"
}

func credentialsOf(getenv func(string) string) []CredentialState {
	out := make([]CredentialState, 0, len(credentialEnvNames))
	for _, row := range credentialEnvNames {
		row.Configured = strings.TrimSpace(getenv(row.Name)) != ""
		out = append(out, row)
	}
	return out
}

func hasID(entries []CatalogEntry, id string) bool {
	for _, entry := range entries {
		if entry.ID == id {
			return true
		}
	}
	return false
}
