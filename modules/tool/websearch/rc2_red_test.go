package websearch

// 本文件是 GO.8 / 官方 0.2.2-rc.2 的**先红**差异测试：只用**既有公开形状**
// （`Tool{Web: ...}` + `Exec` + `Schema()` + 包内既有常量/函数）写断言，
// 所以在 rc.1 口径的旧实现上能编过、会失败（见报告里的红→绿记录）。
//
// 三处差异，逐条给官方证据：
//
//  1. 单次 `web_search` 的查询上限 4→10、来源上限 8→10。
//     rc.2 `dsh-tool-web/src/search.ts:20` `WEB_SEARCH_MAX_RESULTS = 10`、
//     `:23` `WEB_SEARCH_MAX_QUERIES = 10`，并成为配置默认值（`src/index.ts:61-62`）；
//     rc.1 编译产物同位置是 8 / 4（`official-rc1/.../dsh-tool-web/lib/index.js:26,28`）。
//  2. 基础组合的搜索 provider 从 `deepseek-official` 改成 `deepseek-search-api`。
//     rc.2 `dsh-base/cordis.patch.yml:487`；rc.1 `:470`。
//  3. 60s 搜索预算是**组合级**的：rc.2 同一段注释写「This composition gives
//     web_search 60s whichever provider serves it」（`cordis.patch.yml:479-480`），
//     rc.1 的注释是「this shipped DeepSeek route gets 60s」（rc.1 `:454-466`）——
//     即 rc.1 按 provider 判、rc.2 不再按 provider 判。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// manySources 造 n 条互不相同的来源（URL 去重后仍然剩 n 条）。
func manySources(n int) []spi.SearchSource {
	out := make([]spi.SearchSource, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, spi.SearchSource{
			Title: fmt.Sprintf("来源%d", i+1),
			URL:   fmt.Sprintf("https://example.com/%d", i+1),
		})
	}
	return out
}

// 官方 rc.2：一次调用收 1–10 条查询（旧实现上限 4，5 条起就拒）。
func TestRC2AcceptsTenQueries(t *testing.T) {
	provider := &fakeProvider{id: "fake", available: true, result: spi.SearchResult{Answer: "合并结果"}}
	tool := &Tool{Web: newSeam(t, provider)}

	queries := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		queries = append(queries, fmt.Sprintf("查询%d", i+1))
	}
	raw, err := json.Marshal(map[string]any{"queries": queries})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Exec(context.Background(), raw); err != nil {
		t.Fatalf("rc.2 允许 10 条查询；旧实现（上限 4）会拒掉: %v", err)
	}
	if got := len(provider.seen()); got != 10 {
		t.Fatalf("10 条查询都该真的跑：实际 %d 条", got)
	}
}

// 官方 rc.2：11 条超出 bound，必须**响亮拒绝**（不是静默截断），且 Hint 点名真实上限。
func TestRC2RejectsElevenQueriesLoudly(t *testing.T) {
	tool := &Tool{Web: newSeam(t, &fakeProvider{id: "fake", available: true})}
	queries := make([]string, 0, 11)
	for i := 0; i < 11; i++ {
		queries = append(queries, fmt.Sprintf("查询%d", i+1))
	}
	raw, _ := json.Marshal(map[string]any{"queries": queries})
	res, err := tool.Exec(context.Background(), raw)
	if err == nil {
		t.Fatalf("11 条查询必须报错: %+v", res)
	}
	if !strings.Contains(res.Hint, "10") {
		t.Fatalf("Hint 要点名 rc.2 的真实上限 10: %q", res.Hint)
	}
}

// 官方 rc.2：schema 自己也要广告新上限（`search.ts:370` `1–${maxQueries} search queries`）。
func TestRC2SchemaAdvertisesTenQueries(t *testing.T) {
	schema := string((&Tool{}).Schema())
	if !strings.Contains(schema, "1–10") && !strings.Contains(schema, "1-10") {
		t.Fatalf("schema 的查询说明必须写 1–10（旧实现写 1–4）: %s", schema)
	}
}

// 官方 rc.2：一次调用的来源上限 8→10（`search.ts:23` 的默认值 + merge/capSources 截断）。
func TestRC2ReturnsTenSources(t *testing.T) {
	provider := &fakeProvider{id: "fake", available: true, result: spi.SearchResult{Sources: manySources(20)}}
	tool := &Tool{Web: newSeam(t, provider)}
	res, err := execSearch(t, tool, `{"queries":["来源上限"]}`)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 形状不对: %+v", res.Data)
	}
	if d.Count != 10 {
		t.Fatalf("rc.2 单次最多返回 10 个来源（旧实现 8）；实际 %d", d.Count)
	}
	if !d.Truncated {
		t.Fatal("多返回的来源被截断时必须置 Truncated（官方 capSources 语义）")
	}
}

// 官方 rc.2 基础组合钉的搜索 provider 是 deepseek-search-api（rc.1 是 deepseek-official）。
func TestRC2BaseBundleSearchProvider(t *testing.T) {
	if baseBundleSearchProvider != "deepseek-search-api" {
		t.Fatalf("rc.2 基础组合的 searchProvider 是 deepseek-search-api（cordis.patch.yml:487）；当前 %q",
			baseBundleSearchProvider)
	}
}

// 官方 rc.2：60s 是**基础组合**给 web_search 的预算，与哪个 provider 服务无关
// （cordis.patch.yml:479-480「whichever provider serves it」）。rc.1 按 provider 判，
// 所以旧实现里 searxng 只有 30s。
func TestRC2BaseBudgetIsCompositionLevel(t *testing.T) {
	tool := &Tool{}
	for _, id := range []string{"deepseek-search-api", "deepseek-official", "searxng", "任意别的"} {
		if got := tool.searchBudget(id); got != baseBundleSearchBudget {
			t.Fatalf("provider %q 的预算该是组合级 60s（rc.2）；实际 %v", id, got)
		}
	}
	// 显式配置仍然第一优先（官方 tool-web 的 searchTimeoutMs 就是这个字段）。
	explicit := &Tool{Budget: 5 * time.Second}
	if got := explicit.searchBudget("searxng"); got != 5*time.Second {
		t.Fatalf("显式 Budget 必须优先；实际 %v", got)
	}
}
