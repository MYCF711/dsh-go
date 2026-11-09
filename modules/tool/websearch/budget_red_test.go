package websearch

// 本文件是 GO.6（官方 `tool.web.searchTimeoutMs` / `tool.web.searchSummaryMaxChars`）
// 的**先红**证据：它只用既有公开形状（`Tool{Web: ...}` + `Exec`）写断言，
// 因此在旧实现上能编过、会失败。
//
// 旧实现的两处降级：
//  1. 搜索没有自己的预算 —— 慢 provider 挂多久就等多久（本用例是 5s）；
//  2. 来源摘要按宿主自造的 240 rune 截断 —— 官方是 `searchSummaryMaxChars`
//     默认 800，且**省略号算在上限里**。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"dshgo/internal/spi"
)

// slowProvider 故意**不理会 ctx**：真实 provider 也可能这样。预算必须由
// 工具层自己兜住——否则「超时」只是把 ctx 传下去，provider 不守就白搭。
type slowProvider struct {
	id    string
	delay time.Duration
}

func (s *slowProvider) ID() string      { return s.id }
func (s *slowProvider) Available() bool { return true }
func (s *slowProvider) Search(ctx context.Context, query string, limit int) (spi.SearchResult, error) {
	time.Sleep(s.delay)
	return spi.SearchResult{Answer: "慢 provider 终于回来了"}, nil
}

// searchCode 是本仓 web 错误码的稳定取值（与 spi 常量同字面量，先红阶段不引常量）。
const searchCode = "WEB_SEARCH_TIMEOUT"

// 先红：预算 1s + 慢 provider 5s。旧实现会等满 5s 并成功返回。
func TestSearchBudgetTimesOutWithoutWaitingForSlowProvider(t *testing.T) {
	// 先红阶段用字面量，这里把它与 spi 常量钉在一起，防止两处漂移。
	if searchCode != spi.WebSearchTimeout {
		t.Fatalf("错误码漂移：%q != %q", searchCode, spi.WebSearchTimeout)
	}
	t.Setenv("DSHD_WEB_SEARCH_TIMEOUT_MS", "1000")
	tool := &Tool{Web: newSeam(t, &slowProvider{id: "slow", delay: 5 * time.Second})}

	start := time.Now()
	res, err := execSearch(t, tool, `{"queries":["慢查询"]}`)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("1s 预算必须超时返回错误；实际等了 %v 后成功返回 %+v", elapsed, res)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("超时返回用了 %v：搜索预算没有生效（旧实现会等满 provider 的 5s）", elapsed)
	}
	if code := spi.WebErrorCode(err); code != searchCode {
		t.Fatalf("错误码 = %q，期望 %q（err=%v）", code, searchCode, err)
	}
	if !strings.Contains(res.Hint, "DSHD_WEB_SEARCH_TIMEOUT_MS") {
		t.Fatalf("Hint 要说清这是搜索预算、怎么调：%q", res.Hint)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("返回得太早（%v）：预算 1s 不该提前放弃", elapsed)
	}
}

// 先红：2000 字符摘要，默认上限 800（含省略号）。旧实现截在 240。
func TestSearchSummaryCappedAtOfficialDefault(t *testing.T) {
	summary := strings.Repeat("摘", 2000)
	tool := &Tool{Web: newSeam(t, &fakeProvider{id: "fake", available: true, result: spi.SearchResult{
		Sources: []spi.SearchSource{{Title: "长摘要", URL: "https://a.example", Summary: summary}},
	}})}
	res, err := execSearch(t, tool, `{"queries":["长摘要"]}`)
	if err != nil {
		t.Fatal(err)
	}
	excerpt := sourceExcerpt(t, res.Text, "长摘要", "https://a.example")
	if !strings.HasSuffix(excerpt, "…") {
		t.Fatalf("被截断的摘要必须以省略号结尾: %q", excerpt)
	}
	if n := utf8.RuneCountInString(excerpt); n != 800 {
		t.Fatalf("摘要上限应为 800 字符（含省略号），实际 %d 字符: %q", n, excerpt)
	}
	if !strings.HasPrefix(summary, strings.TrimSuffix(excerpt, "…")) {
		t.Fatal("截断必须保留摘要前缀")
	}
}

// 先红：上限可由 DSHD_WEB_SEARCH_SUMMARY_MAX_CHARS 覆盖。
func TestSearchSummaryCapEnvOverride(t *testing.T) {
	t.Setenv("DSHD_WEB_SEARCH_SUMMARY_MAX_CHARS", "100")
	tool := &Tool{Web: newSeam(t, &fakeProvider{id: "fake", available: true, result: spi.SearchResult{
		Sources: []spi.SearchSource{{Title: "长摘要", URL: "https://a.example", Summary: strings.Repeat("摘", 2000)}},
	}})}
	res, err := execSearch(t, tool, `{"queries":["长摘要"]}`)
	if err != nil {
		t.Fatal(err)
	}
	excerpt := sourceExcerpt(t, res.Text, "长摘要", "https://a.example")
	if n := utf8.RuneCountInString(excerpt); n != 100 {
		t.Fatalf("DSHD_WEB_SEARCH_SUMMARY_MAX_CHARS=100 应钳到 100 字符（含省略号），实际 %d: %q", n, excerpt)
	}
}

// sourceExcerpt 从 `- [标题](URL) — 摘要` 这一行里取出摘要正文。
func sourceExcerpt(t *testing.T, text, title, url string) string {
	t.Helper()
	prefix := fmt.Sprintf("- [%s](%s) — ", title, url)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("正文里找不到来源行 %q:\n%s", prefix, text)
	return ""
}
