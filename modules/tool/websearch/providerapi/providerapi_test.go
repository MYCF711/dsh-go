package providerapi

// `web.listProviders` 的 wire 映射测试：官方形状 + 宿主附加字段 + 序列化语义。
//
// 官方契约（只读）：
//   - `dsh-web/lib/typert.host.js:27-41`：方法 `web/listProviders`、`parameters: []`
//   - `dsh-web/src/types.ts:196-205`：`WebProviderInfo = {id, title?, description?}`
//   - `dsh-web/src/index.ts:246-252`：没声明的 title/description **缺席**
//   - `dsh-web/src/types.ts:210-217`：`WebProviderList = {search: [...], fetch: [...]}`

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dshgo/modules/tool/websearch"
)

func envMap(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// 官方两个数组 + 每条 id/available；没声明 title/description 时字段缺席。
func TestOfficialShape(t *testing.T) {
	list, err := ProviderAPI(envMap(map[string]string{"DEEPSEEK_API_KEY": "sk-test"})).
		ListOfficialWebProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Search) != 3 || len(list.Fetch) != 2 {
		t.Fatalf("清单条数不对: search=%d fetch=%d", len(list.Search), len(list.Fetch))
	}
	for _, entry := range list.Search {
		if entry.ID == "" || entry.Available == nil {
			t.Fatalf("每条必须带 id 与真实 available: %+v", entry)
		}
		if entry.Title != "" || entry.Description != nil {
			t.Fatalf("我们的 provider 不声明 title/description，必须缺席（官方语义）: %+v", entry)
		}
	}
	if list.Limits == nil || list.Limits.MaxQueries != 10 || list.Limits.MaxResults != 10 {
		t.Fatalf("limits 必须是 rc.2 生效值 10/10: %+v", list.Limits)
	}
	if list.Limits.SearchTimeoutMS != 60000 {
		t.Fatalf("基础组合预算该是 60000ms；实际 %d", list.Limits.SearchTimeoutMS)
	}
	if list.Baseline == nil || list.Baseline.Search != "deepseek-search-api" || !list.Baseline.SearchRegistered {
		t.Fatalf("baseline 必须报 rc.2 默认且已注册: %+v", list.Baseline)
	}
	if list.Resolution == nil || list.Resolution.Search.Selected != "deepseek-search-api" {
		t.Fatalf("resolution 必须报真实选择（rc.2 默认真生效）: %+v", list.Resolution)
	}
	if list.Resolution.Search.Source != "baseline" {
		t.Fatalf("默认来源该是 rc.2 基础组合（baseline）: %+v", list.Resolution.Search)
	}
	if len(list.Credentials) == 0 {
		t.Fatal("credentials 必须列出真实配置状态")
	}
	for _, row := range list.Credentials {
		if row.Name == "DEEPSEEK_API_KEY" && !row.Configured {
			t.Fatalf("配了 key 必须报已配置: %+v", row)
		}
	}

	// 序列化后：缺席字段不能变成 null / 空串（官方是"字段不存在"）。
	raw, err := json.Marshal(list.Search[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"description":null`) || strings.Contains(string(raw), `"title":""`) {
		t.Fatalf("没声明的字段不许出现在 JSON 里: %s", raw)
	}
	if !strings.Contains(string(raw), `"available":`) {
		t.Fatalf("宿主附加的 available 必须出现: %s", raw)
	}
}

// 官方字段名必须是 `search` / `fetch`（界面按这两个键取数组）。
func TestOfficialFieldNames(t *testing.T) {
	list, err := ProviderAPI(envMap(nil)).ListOfficialWebProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"search":[`, `"fetch":[`, `"resolution":`, `"baseline":`, `"limits":`, `"credentials":`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("结果缺少 %s: %s", key, raw)
		}
	}
}

// 没有 provider 声明 description 时，整条 JSON 里都不该出现 description 键。
func TestOfficialOmitsAbsentDescription(t *testing.T) {
	list, err := ProviderAPI(envMap(nil)).ListOfficialWebProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(list.Search)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "description") {
		t.Fatalf("没人声明 description，JSON 里就不该有它: %s", raw)
	}
}

// 清单是**每次调用重新投影**的真值：环境变了，下一次调用必须跟着变。
func TestOfficialIsLiveState(t *testing.T) {
	env := map[string]string{}
	api := ProviderAPI(func(name string) string { return env[name] })

	before, err := api.ListOfficialWebProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Resolution.Search.ErrorCode == "" || before.Resolution.Search.Selected != "" {
		t.Fatalf("没凭据时必须报结构化选择失败: %+v", before.Resolution.Search)
	}

	env["DEEPSEEK_API_KEY"] = "sk-test"
	after, err := api.ListOfficialWebProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Resolution.Search.Selected != "deepseek-search-api" || after.Resolution.Search.ErrorCode != "" {
		t.Fatalf("配上凭据后必须能选中 rc.2 默认 provider：%+v", after.Resolution.Search)
	}

	var availableAfter bool
	for _, entry := range after.Search {
		if entry.ID == "deepseek-official" {
			availableAfter = entry.Available != nil && *entry.Available
		}
	}
	if !availableAfter {
		t.Fatalf("配上凭据后 deepseek-official 必须报可用: %+v", after.Search)
	}
}

// 直接用 Catalog 映射（不经过 getenv）也要保持同一形状：宿主附加字段齐全。
func TestOfficialFromCatalog(t *testing.T) {
	catalog := websearch.Catalog{
		Search: []websearch.CatalogEntry{
			{ID: "a", Title: "Provider A", Description: "slower, costs tokens", Available: true, Default: true, Selected: true},
			{ID: "b", Available: false},
		},
		Fetch: []websearch.CatalogEntry{{ID: "http", Available: true, Default: true, Selected: true}},
		SearchSelection: websearch.CapabilityState{
			Configured: "a", Source: "env", Selected: "a",
		},
		FetchSelection: websearch.CapabilityState{Source: "auto", Selected: "http"},
		Baseline:       websearch.Baseline{Search: "deepseek-search-api", Fetch: "http", FetchRegistered: true},
		Limits:         websearch.Limits{MaxQueries: 3, MaxResults: 4, SummaryMaxChars: 800},
	}
	list := Official(catalog)
	if len(list.Search) != 2 || list.Search[0].Title != "Provider A" || list.Search[0].Description == nil {
		t.Fatalf("声明了的 title/description 必须透出: %+v", list.Search)
	}
	if list.Search[1].Description != nil {
		t.Fatalf("没声明的必须缺席: %+v", list.Search[1])
	}
	if list.Limits.MaxQueries != 3 || list.Limits.MaxResults != 4 {
		t.Fatalf("limits 映射不对: %+v", list.Limits)
	}
	if list.Resolution.Search.Configured != "a" || list.Resolution.Search.Source != "env" {
		t.Fatalf("selection 映射不对: %+v", list.Resolution.Search)
	}
}

// web.listProviders 的真实投影（E2 报告取证）：两个 rc.2 DeepSeek API provider 必须
// 出现在清单里，且默认/选中落在官方基础组合的默认值上（cordis.patch.yml:487-488）。
func TestOfficialListProvidersEvidence(t *testing.T) {
	env := map[string]string{"DEEPSEEK_API_KEY": "sk-test"}
	list, err := ProviderAPI(func(name string) string { return env[name] }).
		ListOfficialWebProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("web.listProviders (DEEPSEEK_API_KEY set): %s", raw)

	var searchIDs, fetchIDs []string
	for _, entry := range list.Search {
		searchIDs = append(searchIDs, entry.ID)
	}
	for _, entry := range list.Fetch {
		fetchIDs = append(fetchIDs, entry.ID)
	}
	if strings.Join(searchIDs, ",") != "deepseek-official,deepseek-search-api,searxng" {
		t.Fatalf("搜索清单不对: %v", searchIDs)
	}
	if strings.Join(fetchIDs, ",") != "deepseek-fetch-api,http" {
		t.Fatalf("抓取清单不对: %v", fetchIDs)
	}
	if !list.Baseline.SearchRegistered || !list.Baseline.FetchRegistered {
		t.Fatalf("两个基线默认 provider 都必须已注册: %+v", list.Baseline)
	}
	if list.Resolution.Search.Selected != "deepseek-search-api" || list.Resolution.Fetch.Selected != "http" {
		t.Fatalf("默认 provider 必须真生效: %+v", list.Resolution)
	}
}
