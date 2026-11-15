package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/spi"
)

// fakeProvider 是一个真实实现 spi.SearchProvider 的内存 provider：
// 记下被问到的查询与上限，按预设结果/错误回答。
type fakeProvider struct {
	id        string
	available bool
	result    spi.SearchResult
	err       error

	mu      sync.Mutex
	queries []string
	limits  []int
}

func (f *fakeProvider) ID() string { return f.id }
func (f *fakeProvider) Available() bool {
	return f.available
}
func (f *fakeProvider) Search(ctx context.Context, query string, limit int) (spi.SearchResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.limits = append(f.limits, limit)
	f.mu.Unlock()
	return f.result, f.err
}

func (f *fakeProvider) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

func newSeam(t *testing.T, providers ...spi.SearchProvider) *spi.WebProviders {
	t.Helper()
	seam := spi.NewWebProviders("", "")
	for _, provider := range providers {
		if err := seam.RegisterSearch(provider); err != nil {
			t.Fatalf("注册 provider 失败: %v", err)
		}
	}
	return seam
}

func execSearch(t *testing.T, tool *Tool, raw string) (spi.Result, error) {
	t.Helper()
	return tool.Exec(context.Background(), json.RawMessage(raw))
}

// 正常：摘要 + 来源都进正文，Data 结构化，官方文案齐全。
func TestSearchOK(t *testing.T) {
	provider := &fakeProvider{id: "fake", available: true, result: spi.SearchResult{
		Answer: "答案是 42",
		Sources: []spi.SearchSource{
			{Title: "来源一", URL: "https://a.example", Snippet: "片段一"},
			{Title: "来源二", URL: "https://b.example"},
		},
	}}
	tool := &Tool{Web: newSeam(t, provider)}
	res, err := execSearch(t, tool, `{"queries":["普遍性问题"]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		externalWebContentNotice,
		"答案是 42",
		"- [来源一](https://a.example) — 片段一",
		"- [来源二](https://b.example)",
		"Sources:",
		citeInstruction,
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("正文缺 %q:\n%s", want, res.Text)
		}
	}
	d, ok := res.Data.(data)
	if !ok || d.Count != 2 || d.Answer == "" || d.Truncated {
		t.Fatalf("Data 不对: %+v", res.Data)
	}
	if seen := provider.seen(); len(seen) != 1 || seen[0] != "普遍性问题" {
		t.Fatalf("查询没有传下去: %v", seen)
	}
	if provider.limits[0] != defaultMaxResults {
		t.Fatalf("上限没有传下去: %d", provider.limits[0])
	}
}

// 没装后端 ≠ 搜出来是空的。必须报错并指路。
func TestNoProviderIsLoud(t *testing.T) {
	tool := &Tool{}
	res, err := execSearch(t, tool, `{"queries":["x"]}`)
	if err == nil {
		t.Fatal("没有后端必须报错，不能返回空结果")
	}
	if !strings.Contains(res.Hint, "web_fetch") {
		t.Fatalf("Hint 要给出退路: %q", res.Hint)
	}
}

// "后端没配置" / "后端不可用" / "后端连不上" 三种说法要不一样——
// 它们对应三个完全不同的下一步。
func TestUnconfiguredVsUnavailableVsUnreachable(t *testing.T) {
	empty := &Tool{Web: newSeam(t)}
	res, err := execSearch(t, empty, `{"queries":["x"]}`)
	if err == nil || spi.WebErrorCode(err) != spi.WebProviderUnavailable {
		t.Fatalf("没有可用 provider 必须结构化报错: %v code=%s", err, spi.WebErrorCode(err))
	}
	if !strings.Contains(res.Hint, "DSHD_SEARCH_URL") || !strings.Contains(res.Hint, "DEEPSEEK_API_KEY") {
		t.Fatalf("没配置要提具体配置项: %q", res.Hint)
	}

	down := &Tool{Web: newSeam(t, &fakeProvider{id: "fake", available: true, err: spi.NewWebError(spi.WebProviderError, "dial tcp: refused")})}
	res, _ = execSearch(t, down, `{"queries":["x"]}`)
	if strings.Contains(res.Hint, "DSHD_SEARCH_URL") {
		t.Fatalf("连不上不该让人去改配置: %q", res.Hint)
	}
	if !strings.Contains(res.Hint, "web_fetch") {
		t.Fatalf("连不上要给出退路: %q", res.Hint)
	}

	unavailable := &Tool{Web: spi.NewWebProviders("fake", "")}
	if err := unavailable.Web.RegisterSearch(&fakeProvider{id: "fake", available: false}); err != nil {
		t.Fatal(err)
	}
	res, _ = execSearch(t, unavailable, `{"queries":["x"]}`)
	if !strings.Contains(res.Hint, "DEEPSEEK_API_KEY") || !strings.Contains(res.Hint, "DSHD_WEB_SEARCH_PROVIDER") {
		t.Fatalf("配置了但不可用，要同时说凭据与 provider 选择: %q", res.Hint)
	}
}

// 零命中：不是错误，但要说"换个说法"。
func TestZeroHits(t *testing.T) {
	tool := &Tool{Web: newSeam(t, &fakeProvider{id: "fake", available: true})}
	res, err := execSearch(t, tool, `{"queries":["不存在的东西"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Hint, "换个更常见的说法") {
		t.Fatalf("零命中要给下一步: %q", res.Hint)
	}
	if !strings.Contains(res.Text, "No results found.") {
		t.Fatalf("零命中要有官方文案: %q", res.Text)
	}
}

// 输入边界：空数组 / 空词 / 超过上限。
//
// 上限本身随官方基线走：rc.2 是 10（`dsh-tool-web/src/search.ts:23`），
// rc.1 是 4。这里用包内常量断言，避免把某一个版本的数写死在测试里。
func TestInputLimits(t *testing.T) {
	tool := &Tool{Web: newSeam(t, &fakeProvider{id: "fake", available: true})}
	if _, err := execSearch(t, tool, `{"queries":[]}`); err == nil {
		t.Fatal("空数组要报错")
	}
	if _, err := execSearch(t, tool, `{"queries":["  "]}`); err == nil {
		t.Fatal("空词要报错")
	}
	over := make([]string, 0, defaultMaxQueries+1)
	for i := 0; i < defaultMaxQueries+1; i++ {
		over = append(over, fmt.Sprintf("q%d", i))
	}
	raw, err := json.Marshal(map[string]any{"queries": over})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Exec(context.Background(), raw); err == nil {
		t.Fatalf("超过 %d 条要报错", defaultMaxQueries)
	}
}

// 完全重复的查询只搜一次（官方语义；不是错误）。
func TestDuplicateQueriesCollapse(t *testing.T) {
	provider := &fakeProvider{id: "fake", available: true}
	tool := &Tool{Web: newSeam(t, provider)}
	if _, err := execSearch(t, tool, `{"queries":["同一个","同一个"]}`); err != nil {
		t.Fatal(err)
	}
	if seen := provider.seen(); len(seen) != 1 {
		t.Fatalf("重复查询应只搜一次: %v", seen)
	}
}

// 多查询：并发、轮转合并、按 URL 去重、超过上限置 truncated。
func TestMultiQueryMergesRoundRobinAndCaps(t *testing.T) {
	// 同一个 provider 连跑两条查询（多查询合并的语义在工具层，不在 provider 层）。
	// 结果按**查询词**取，不按调用顺序：多查询是并发的，调用顺序本就不该被依赖。
	provider := &mapProvider{byQuery: map[string]spi.SearchResult{
		"q1": {Answer: "答案一", Sources: []spi.SearchSource{{URL: "https://shared.example"}, {URL: "https://a1.example"}, {URL: "https://a2.example"}}},
		"q2": {Answer: "答案二", Sources: []spi.SearchSource{{URL: "https://shared.example"}, {URL: "https://b1.example"}}},
	}}
	tool := &Tool{Web: newSeam(t, provider)}
	res, err := execSearch(t, tool, `{"queries":["q1","q2"]}`)
	if err != nil {
		t.Fatal(err)
	}
	d := res.Data.(data)
	var urls []string
	for _, source := range d.Sources {
		urls = append(urls, source.URL)
	}
	want := []string{"https://shared.example", "https://a1.example", "https://b1.example", "https://a2.example"}
	if strings.Join(urls, ",") != strings.Join(want, ",") {
		t.Fatalf("轮转合并顺序不对:\n got=%v\nwant=%v", urls, want)
	}
	if d.Truncated {
		t.Fatal("这次没有超上限，不该标 truncated")
	}
	if !strings.Contains(res.Text, "### q1") || !strings.Contains(res.Text, "### q2") {
		t.Fatalf("多查询的答案要按查询分段: %q", res.Text)
	}
}

// 一条查询失败要取消兄弟查询并把错误抛出来（不是吞掉当"部分成功"）。
func TestProviderErrorAbortsSiblings(t *testing.T) {
	provider := &failingProvider{}
	tool := &Tool{Web: newSeam(t, provider)}
	res, err := execSearch(t, tool, `{"queries":["ok","boom"]}`)
	if err == nil {
		t.Fatal("一条查询失败必须报错")
	}
	if !strings.Contains(res.Hint, "web_fetch") {
		t.Fatalf("Hint 要给出退路: %q", res.Hint)
	}
	if spi.WebErrorCode(err) != spi.WebProviderError {
		t.Fatalf("错误码要透传: %s", spi.WebErrorCode(err))
	}
}

// 钉死一个未注册的 provider：结构化错误，不静默换后端。
func TestConfiguredProviderMissing(t *testing.T) {
	seam := spi.NewWebProviders("nope", "")
	if err := seam.RegisterSearch(&fakeProvider{id: "fake", available: true}); err != nil {
		t.Fatal(err)
	}
	tool := &Tool{Web: seam}
	_, err := execSearch(t, tool, `{"queries":["x"]}`)
	if spi.WebErrorCode(err) != spi.WebProviderConfiguredMissing {
		t.Fatalf("错误码不对: %v", err)
	}
}

// 多个可用 provider 且没钉死：必须 ambiguous，而不是按注册顺序随便挑一个。
func TestAmbiguousProviderIsLoud(t *testing.T) {
	tool := &Tool{Web: newSeam(t,
		&fakeProvider{id: "a", available: true},
		&fakeProvider{id: "b", available: true},
	)}
	res, err := execSearch(t, tool, `{"queries":["x"]}`)
	if spi.WebErrorCode(err) != spi.WebProviderAmbiguous {
		t.Fatalf("错误码不对: %v", err)
	}
	if !strings.Contains(res.Hint, "DSHD_WEB_SEARCH_PROVIDER") {
		t.Fatalf("Hint 要说清怎么钉死: %q", res.Hint)
	}
}

// mapProvider 按查询词返回固定结果（并发下也不依赖调用顺序）。
type mapProvider struct {
	mu      sync.Mutex
	byQuery map[string]spi.SearchResult
}

func (m *mapProvider) ID() string      { return "mapped" }
func (m *mapProvider) Available() bool { return true }
func (m *mapProvider) Search(_ context.Context, query string, _ int) (spi.SearchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byQuery[query], nil
}

// recordingProvider 是可复用的固定结果 provider。
type recordingProvider struct {
	id        string
	available bool
	result    spi.SearchResult
}

func (r *recordingProvider) ID() string      { return r.id }
func (r *recordingProvider) Available() bool { return r.available }
func (r *recordingProvider) Search(context.Context, string, int) (spi.SearchResult, error) {
	return r.result, nil
}

// failingProvider 对带 "boom" 的查询返回 provider 错误。
type failingProvider struct{}

func (f *failingProvider) ID() string      { return "failing" }
func (f *failingProvider) Available() bool { return true }
func (f *failingProvider) Search(_ context.Context, query string, _ int) (spi.SearchResult, error) {
	if strings.Contains(query, "boom") {
		return spi.SearchResult{}, spi.NewWebError(spi.WebProviderError, "upstream exploded")
	}
	return spi.SearchResult{Sources: []spi.SearchSource{{URL: "https://ok.example"}}}, nil
}
