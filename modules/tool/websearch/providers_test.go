package websearch

// web.listProviders 的**宿主真值推导**测试（官方 0.2.2-rc.2 `dsh-web`）。
//
// 这里断言的是"清单跟着真实装配与真实环境走"，不是"清单里有某几个我写死的 id"：
//
//   - 注册表来源：`modules/search` + `modules/tool/webfetch`（生产同一份装配代码）
//   - 可用性来源：各 provider 自己的 `Available()`
//   - 选择结果：接缝的 `SelectSearch` / `SelectFetch`（官方执行期规则）
//   - 基线默认：rc.2 `cordis.patch.yml:487-488`（`deepseek-search-api` / `http`）
//   - 上限：与 `web_search` 同一个解析函数
//
// 与先红文件（rc2_red_test.go）的分工：那里只碰**既有**形状，这里覆盖新增形状。

import (
	"strings"
	"testing"

	"dshgo/internal/spi"
	"dshgo/modules/search"
	"dshgo/modules/tool/webfetch"
)

// envMap 把 map 变成 getenv 接缝（生产用 os.Getenv，这里不污染进程环境）。
func envMap(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// entryOf 按 id 取清单里的一条（清单顺序是排序后的 id，不按位置断言）。
func entryOf(entries []CatalogEntry, id string) CatalogEntry {
	for _, entry := range entries {
		if entry.ID == id {
			return entry
		}
	}
	return CatalogEntry{}
}

func entryIDs(entries []CatalogEntry) string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return strings.Join(ids, ",")
}

// 清单必须由真实装配推导：搜索两个 provider、抓取匿名 http，可用性各按自己的判定。
func TestRC2CatalogDerivesFromRealComposition(t *testing.T) {
	catalog, err := DescribeCatalog(envMap(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}))
	if err != nil {
		t.Fatalf("投影失败: %v", err)
	}

	if got := entryIDs(catalog.Search); got != "deepseek-official,deepseek-search-api,searxng" {
		t.Fatalf("搜索 provider 清单必须来自真实注册表；实际 %q", got)
	}
	for _, entry := range catalog.Search {
		// 只有 DeepSeek 那两个配得上 key（DSHD_SEARCH_URL 未配）。
		want := entry.ID == "deepseek-official" || entry.ID == "deepseek-search-api"
		if entry.Available != want {
			t.Fatalf("%s 的可用性该是 %v；实际 %v", entry.ID, want, entry.Available)
		}
	}
	if got := entryIDs(catalog.Fetch); got != "deepseek-fetch-api,"+webfetch.HTTPProviderID {
		t.Fatalf("抓取 provider 清单不对: %q", got)
	}
	if !entryOf(catalog.Fetch, webfetch.HTTPProviderID).Available {
		t.Fatal("匿名 http 抓取永远可用（官方 available() 恒 true）")
	}
	if !entryOf(catalog.Fetch, "deepseek-fetch-api").Available {
		t.Fatal("配了 DEEPSEEK_API_KEY 时 deepseek-fetch-api 必须报可用")
	}

	// rc.2 基线默认 provider 有凭据时真生效：未钉死 → 选中的就是 deepseek-search-api。
	if catalog.SearchSelection.Source != "baseline" ||
		catalog.SearchSelection.Selected != search.BaseBundleSearchProviderID {
		t.Fatalf("选择结果不对: %+v", catalog.SearchSelection)
	}
	if !entryOf(catalog.Search, "deepseek-search-api").Selected {
		t.Fatalf("Selected 标记不对: %+v", catalog.Search)
	}
	if entryOf(catalog.Search, "deepseek-official").Selected || entryOf(catalog.Search, "searxng").Selected {
		t.Fatalf("只有选中的那个能带 Selected: %+v", catalog.Search)
	}
	if !entryOf(catalog.Search, "deepseek-search-api").Default {
		t.Fatalf("基线默认 provider 要标 Default: %+v", catalog.Search)
	}

	// rc.2 基线默认 provider 与"本宿主是否已注册"必须分开报——现在两个都注册了。
	if catalog.Baseline.Search != "deepseek-search-api" || !catalog.Baseline.SearchRegistered {
		t.Fatalf("baseline.search 该是已注册的 deepseek-search-api；实际 %+v", catalog.Baseline)
	}
	if catalog.Baseline.Fetch != "http" || !catalog.Baseline.FetchRegistered {
		t.Fatalf("baseline.fetch 该是已注册的 http；实际 %+v", catalog.Baseline)
	}
}

// 没有凭据时清单照样列全（官方「availability 不过滤清单」），只是选择失败。
func TestRC2CatalogListsUnavailableProviders(t *testing.T) {
	catalog, err := DescribeCatalog(envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := entryIDs(catalog.Search); got != "deepseek-official,deepseek-search-api,searxng" {
		t.Fatalf("不可用也必须列出来（官方语义）；实际 %q", got)
	}
	for _, entry := range catalog.Search {
		if entry.Available {
			t.Fatalf("%s 没凭据不该可用", entry.ID)
		}
	}
	if catalog.SearchSelection.ErrorCode != spi.WebProviderUnavailable {
		t.Fatalf("没有可用 provider 要报 %s；实际 %q",
			spi.WebProviderUnavailable, catalog.SearchSelection.ErrorCode)
	}
	if catalog.SearchSelection.Selected != "" {
		t.Fatalf("选择失败时 Selected 必须为空；实际 %q", catalog.SearchSelection.Selected)
	}
}

// env 钉死优先于自动选择；钉死的 id 不存在/不可用要报官方错误码，绝不悄悄换后端。
func TestRC2CatalogHonorsPinAndReportsMissing(t *testing.T) {
	t.Run("pinned unavailable", func(t *testing.T) {
		catalog, _ := DescribeCatalog(envMap(map[string]string{"DSHD_WEB_SEARCH_PROVIDER": "searxng"}))
		if catalog.SearchSelection.Configured != "searxng" || catalog.SearchSelection.Source != "env" {
			t.Fatalf("env 钉死必须被认到: %+v", catalog.SearchSelection)
		}
		if catalog.SearchSelection.ErrorCode != spi.WebProviderConfiguredUnavailable {
			t.Fatalf("钉死但不可用要报 %s；实际 %q",
				spi.WebProviderConfiguredUnavailable, catalog.SearchSelection.ErrorCode)
		}
		if !entryOf(catalog.Search, "searxng").Default {
			t.Fatalf("钉死的 searxng 要标 Default: %+v", catalog.Search)
		}
	})
	t.Run("pinned missing", func(t *testing.T) {
		catalog, _ := DescribeCatalog(envMap(map[string]string{"DSH_WEB_SEARCH_PROVIDER": "no-such-provider"}))
		if catalog.SearchSelection.ErrorCode != spi.WebProviderConfiguredMissing {
			t.Fatalf("钉死未注册的 id 要报 %s；实际 %q",
				spi.WebProviderConfiguredMissing, catalog.SearchSelection.ErrorCode)
		}
	})
	t.Run("pinned ok", func(t *testing.T) {
		catalog, _ := DescribeCatalog(envMap(map[string]string{
			"DSHD_WEB_SEARCH_PROVIDER": "deepseek-official",
			"DEEPSEEK_API_KEY":         "sk-test",
		}))
		if catalog.SearchSelection.Selected != "deepseek-official" || catalog.SearchSelection.ErrorCode != "" {
			t.Fatalf("钉死且可用时该选中它: %+v", catalog.SearchSelection)
		}
	})
}

// 官方 DSH_WEB_FETCH_PROVIDER / DSH_WEB_SEARCH_PROVIDER 与宿主 DSHD_* 喂同一个字段。
func TestRC2CatalogReadsOfficialEnvNames(t *testing.T) {
	catalog, _ := DescribeCatalog(envMap(map[string]string{"DSH_WEB_FETCH_PROVIDER": webfetch.HTTPProviderID}))
	if catalog.FetchSelection.Configured != webfetch.HTTPProviderID ||
		catalog.FetchSelection.Selected != webfetch.HTTPProviderID {
		t.Fatalf("官方 DSH_WEB_FETCH_PROVIDER 必须被认到: %+v", catalog.FetchSelection)
	}
	httpEntry := entryOf(catalog.Fetch, webfetch.HTTPProviderID)
	if !httpEntry.Default || !httpEntry.Selected {
		t.Fatalf("钉死的 http 要同时标 Default 与 Selected: %+v", catalog.Fetch)
	}
}

// 上限必须由同一份解析函数给出（否则界面上的数与真正生效的数会漂）。
func TestRC2CatalogReportsEffectiveLimits(t *testing.T) {
	catalog, _ := DescribeCatalog(envMap(map[string]string{"DSHD_WEB_SEARCH_MAX_QUERIES": "7"}))
	if catalog.Limits.MaxQueries != 7 {
		t.Fatalf("上限必须是生效值（env=7）；实际 %d", catalog.Limits.MaxQueries)
	}
	if catalog.Limits.MaxResults != defaultMaxResults {
		t.Fatalf("默认来源上限该是 %d（官方 rc.2）；实际 %d", defaultMaxResults, catalog.Limits.MaxResults)
	}
	if catalog.Limits.SummaryMaxChars != defaultSummaryMaxChars {
		t.Fatalf("摘要上限该是 %d；实际 %d", defaultSummaryMaxChars, catalog.Limits.SummaryMaxChars)
	}
	if catalog.Limits.SearchTimeout != baseBundleSearchBudget {
		t.Fatalf("预算该是基础组合 60s；实际 %v", catalog.Limits.SearchTimeout)
	}
}

// 上限是部署可调的（官方 tool-web 的 searchMaxQueries/searchMaxResults）；
// 宿主对应 DSHD_WEB_SEARCH_MAX_*，非法值回落默认，不静默改语义。
func TestRC2LimitResolvers(t *testing.T) {
	t.Setenv("DSHD_WEB_SEARCH_MAX_QUERIES", "2")
	t.Setenv("DSHD_WEB_SEARCH_MAX_RESULTS", "3")
	tool := &Tool{}
	if got := tool.queryLimit(); got != 2 {
		t.Fatalf("DSHD_WEB_SEARCH_MAX_QUERIES=2 应生效；实际 %d", got)
	}
	if got := tool.sourceLimit(); got != 3 {
		t.Fatalf("DSHD_WEB_SEARCH_MAX_RESULTS=3 应生效；实际 %d", got)
	}
	// 显式字段优先于环境。
	explicit := &Tool{MaxQueries: 1, MaxResults: 1}
	if explicit.queryLimit() != 1 || explicit.sourceLimit() != 1 {
		t.Fatal("Tool.MaxQueries/MaxResults 必须优先于环境变量")
	}
	// 非法值回落默认（写坏了不该变成永远拒绝）。
	t.Setenv("DSHD_WEB_SEARCH_MAX_QUERIES", "0")
	t.Setenv("DSHD_WEB_SEARCH_MAX_RESULTS", "not-a-number")
	if got := (&Tool{}).queryLimit(); got != defaultMaxQueries {
		t.Fatalf("非法值必须回落 %d；实际 %d", defaultMaxQueries, got)
	}
	if got := (&Tool{}).sourceLimit(); got != defaultMaxResults {
		t.Fatalf("非法值必须回落 %d；实际 %d", defaultMaxResults, got)
	}
}

// 凭据状态是真读环境：key 一配上，可用性必须跟着变（不是写死的 true/false 表）。
func TestRC2CatalogCredentialsAreLiveState(t *testing.T) {
	without, _ := DescribeCatalog(envMap(nil))
	if !hasConfigured(without.Credentials, "DEEPSEEK_API_KEY", false) {
		t.Fatalf("没配 key 时必须报未配置: %+v", without.Credentials)
	}
	with, _ := DescribeCatalog(envMap(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}))
	if !hasConfigured(with.Credentials, "DEEPSEEK_API_KEY", true) {
		t.Fatalf("配了 key 必须报已配置: %+v", with.Credentials)
	}
	// 端点变量同理（SearxNG 的可用性就是它）。
	searx, _ := DescribeCatalog(envMap(map[string]string{"DSHD_SEARCH_URL": "https://s.example/search?q={query}"}))
	if !hasConfigured(searx.Credentials, "DSHD_SEARCH_URL", true) {
		t.Fatalf("端点配置要从环境读出来: %+v", searx.Credentials)
	}
	if !entryOf(searx.Search, "searxng").Available {
		t.Fatalf("配了端点后 searxng 必须可用: %+v", searx.Search)
	}
}

func hasConfigured(rows []CredentialState, name string, want bool) bool {
	for _, row := range rows {
		if row.Name == name {
			return row.Configured == want
		}
	}
	return false
}

// rc.2 两个默认 provider 的字面量只允许有一个真源：官方 `cordis.patch.yml:487-488`。
// 这里钉住"websearch 的常量 == modules/search 的常量 == webfetch 的 http id"，
// 任何一侧改字面量都会在这里红。
func TestRC2BaseBundleDefaultsHaveSingleSource(t *testing.T) {
	if baseBundleSearchProvider != search.BaseBundleSearchProviderID {
		t.Fatalf("websearch.baseBundleSearchProvider=%q 与 search.BaseBundleSearchProviderID=%q 不一致",
			baseBundleSearchProvider, search.BaseBundleSearchProviderID)
	}
	if baseBundleSearchProvider != "deepseek-search-api" {
		t.Fatalf("rc.2 基础组合 searchProvider 必须是 deepseek-search-api；实际 %q", baseBundleSearchProvider)
	}
	if search.BaseBundleFetchProviderID != webfetch.HTTPProviderID {
		t.Fatalf("search.BaseBundleFetchProviderID=%q 与 webfetch.HTTPProviderID=%q 不一致",
			search.BaseBundleFetchProviderID, webfetch.HTTPProviderID)
	}
}

// 两个新 provider 必须在 `web.listProviders` 的清单里，且默认标记落在 rc.2 基线上。
func TestRC2NewProvidersAppearInCatalog(t *testing.T) {
	catalog, err := DescribeCatalog(envMap(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}))
	if err != nil {
		t.Fatal(err)
	}
	searchEntry := entryOf(catalog.Search, search.DeepSeekSearchAPIProviderID)
	if searchEntry.ID == "" {
		t.Fatalf("deepseek-search-api 必须在清单里: %+v", catalog.Search)
	}
	if !searchEntry.Available || !searchEntry.Default || !searchEntry.Selected {
		t.Fatalf("有凭据时 deepseek-search-api 该是 可用+默认+选中: %+v", searchEntry)
	}
	if entryOf(catalog.Fetch, search.DeepSeekFetchAPIProviderID).ID == "" {
		t.Fatalf("deepseek-fetch-api 必须在清单里: %+v", catalog.Fetch)
	}
	// 官方基础组合默认抓取是 http（`cordis.patch.yml:488`），因此它是 Default+Selected。
	httpEntry := entryOf(catalog.Fetch, webfetch.HTTPProviderID)
	if !httpEntry.Default || !httpEntry.Selected {
		t.Fatalf("基线默认抓取必须是 http: %+v", catalog.Fetch)
	}
}
