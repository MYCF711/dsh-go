package search

// 本文件是 E2（官方 0.2.2-rc.2 `dsh-web-deepseek-api` 两个真 provider）的**先红**测试：
// 它只用下面这套**已冻结的公开形状**写断言，因此在实现落地前编译不过（红），
// 落地后必须全绿。红→绿日志见报告 `tmp/GO8-RC2/reports/E2-deepseek-web-api.md`。
//
// 官方权威（只读）：
//   - `dsh-web-deepseek-api/src/search.ts:13,16,19,22,99-114`（id / base / count 默认 / 5 rps / 请求形状）
//   - `dsh-web-deepseek-api/src/fetch.ts:13,51-62`（id / 请求形状 / 结果映射）
//   - `dsh-web-deepseek-api/src/transport.ts:181-262`（dispatch 与错误映射）
//   - `dsh-web-deepseek-api/src/index.ts:99-109,214-259,324-353`（配置 schema 与凭据规则）
//   - `dsh-base/cordis.patch.yml:487-488`（基础组合默认 searchProvider / fetchProvider）
//
// 纪律：**禁止真实出网**。所有网络断言都打在 `net/http/httptest` 桩上。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// staticEnv 把 map 变成 getenv 接缝（不污染进程环境）。
func staticEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// fakeClock 是可注入时钟：限速的"等多久"可以精确断言，不用真的睡 1 秒。
type fakeClock struct {
	now   time.Time
	waits []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	return nil
}

// 官方 id 常量：两个 provider 的注册键（`search.ts:13`、`fetch.ts:13`）。
func TestRC2DeepSeekAPIProviderIDs(t *testing.T) {
	if DeepSeekSearchAPIProviderID != "deepseek-search-api" {
		t.Fatalf("Search API provider id 必须是 deepseek-search-api（search.ts:13）；实际 %q", DeepSeekSearchAPIProviderID)
	}
	if DeepSeekFetchAPIProviderID != "deepseek-fetch-api" {
		t.Fatalf("Fetch API provider id 必须是 deepseek-fetch-api（fetch.ts:13）；实际 %q", DeepSeekFetchAPIProviderID)
	}
	if DeepSeekWebAPIBaseURL != "https://api.deepseek.com/v1/tools" {
		t.Fatalf("官方 base 必须是 https://api.deepseek.com/v1/tools（search.ts:16）；实际 %q", DeepSeekWebAPIBaseURL)
	}
	if DeepSeekDefaultCount != 10 || DeepSeekSearchRequestsPerSecond != 5 {
		t.Fatalf("默认值必须照官方：count=10（search.ts:19）、rps=5（search.ts:22）；实际 %d/%d",
			DeepSeekDefaultCount, DeepSeekSearchRequestsPerSecond)
	}
	if BaseBundleSearchProviderID != DeepSeekSearchAPIProviderID {
		t.Fatalf("rc.2 基础组合 searchProvider 就是 deepseek-search-api（cordis.patch.yml:487）")
	}
	if BaseBundleFetchProviderID != "http" {
		t.Fatalf("rc.2 基础组合 fetchProvider 是 http（cordis.patch.yml:488）；实际 %q", BaseBundleFetchProviderID)
	}
}

// 装配：两个 provider 必须真的注册进接缝（`web.listProviders` 才看得到）。
func TestRC2RegistersDeepSeekAPIProviders(t *testing.T) {
	seam, err := NewWebProvidersFromEnv(staticEnv(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seam.SearchProviderIDs(), ","); got != "deepseek-official,deepseek-search-api,searxng" {
		t.Fatalf("搜索侧必须注册 deepseek-search-api；实际 %q", got)
	}
	if got := strings.Join(seam.FetchProviderIDs(), ","); got != DeepSeekFetchAPIProviderID {
		t.Fatalf("抓取侧必须注册 deepseek-fetch-api；实际 %q", got)
	}
}

// 默认 provider 真生效：有 DeepSeek 凭据且未钉死时，选中的就是 rc.2 基线默认。
func TestRC2DefaultSearchProviderTakesEffect(t *testing.T) {
	seam, err := NewWebProvidersFromEnv(staticEnv(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := seam.SelectSearch()
	if err != nil {
		t.Fatalf("默认 provider 必须可直接选中: %v", err)
	}
	if selected.ID() != DeepSeekSearchAPIProviderID {
		t.Fatalf("rc.2 默认搜索 provider 该是 %s；实际 %s", DeepSeekSearchAPIProviderID, selected.ID())
	}
	// 没有凭据时**不硬钉**：退回既有自动选择语义（否则搜索会从"不可用"变成"配置缺失"）。
	empty, err := NewWebProvidersFromEnv(staticEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if empty.SearchPin() != "" {
		t.Fatalf("没有凭据时不该钉死不可用的默认 provider；实际 pin=%q", empty.SearchPin())
	}
	if _, err := empty.SelectSearch(); spi.WebErrorCode(err) != spi.WebProviderUnavailable {
		t.Fatalf("没有凭据该报 %s；实际 %q", spi.WebProviderUnavailable, spi.WebErrorCode(err))
	}
}

// Search API 请求/响应形状（官方 `search.ts:102-113` 的 body、`transport.ts:194-206` 的头部）。
func TestRC2SearchAPIOfficialRequestShape(t *testing.T) {
	type seen struct {
		path    string
		auth    string
		token   string
		ctype   string
		accept  string
		agent   string
		body    map[string]any
		rawBody string
	}
	var mu sync.Mutex
	var got seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		got = seen{
			path: r.URL.Path, auth: r.Header.Get("Authorization"), token: r.Header.Get("x-dsh-auth-token"),
			ctype: r.Header.Get("Content-Type"), accept: r.Header.Get("Accept"),
			agent: r.Header.Get("User-Agent"), body: body, rawBody: string(raw),
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[
			{"url":"https://a.example","title":"来源一","snippet":"片段一","summary":"摘要一","date":"2026-11-01"},
			{"url":"https://b.example","title":"","snippet":"","date":null}
		]}`)
	}))
	defer srv.Close()

	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{
					Count: 10, RequestsPerSecond: 5, SendOriginalQuestion: true,
					Billing: BillingAuto, BaseURL: srv.URL,
				},
				Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}),
			}
		},
		OriginalQuestion: func() string { return "  为什么\n\n要有  \t这个限制？  " },
		HTTP:             srv.Client(),
	}
	if !provider.Available() {
		t.Fatal("有 key + 合法 baseURL 就该可用")
	}
	result, err := provider.Search(context.Background(), "官方同步", 7)
	if err != nil {
		t.Fatal(err)
	}

	if got.path != "/search" {
		t.Fatalf("端点 = baseURL + /search（search.ts:103）；实际 %q", got.path)
	}
	if got.auth != "Bearer sk-test" {
		t.Fatalf("API key 走 Authorization: Bearer（transport.ts:333-336）；实际 %q", got.auth)
	}
	if got.token != "" {
		t.Fatalf("API key 不该带 x-dsh-auth-token；实际 %q", got.token)
	}
	if got.ctype != "application/json" || got.accept != "application/json" {
		t.Fatalf("content-type/accept 不对: %q %q", got.ctype, got.accept)
	}
	if got.agent != deepSeekWebAPIUserAgent {
		t.Fatalf("user-agent 必须是官方 %q；实际 %q", deepSeekWebAPIUserAgent, got.agent)
	}
	if got.body["query"] != "官方同步" {
		t.Fatalf("query 没进 body: %v", got.body)
	}
	if got.body["count"] != float64(7) {
		t.Fatalf("count = min(配置 count, 调用方上限)（search.ts:106）；实际 %v", got.body["count"])
	}
	if got.body["need_content"] != false {
		t.Fatalf("need_content 必须 false（search.ts:107）；实际 %v", got.body["need_content"])
	}
	// ori_question：空白折叠 + 只留前 128 字符（search.ts:160-164）。
	if got.body["ori_question"] != "为什么 要有 这个限制？" {
		t.Fatalf("ori_question 空白要折叠成单空格；实际 %v", got.body["ori_question"])
	}

	if len(result.Sources) != 2 {
		t.Fatalf("结果条数不对: %+v", result.Sources)
	}
	first := result.Sources[0]
	if first.URL != "https://a.example" || first.Title != "来源一" || first.Snippet != "片段一" ||
		first.Summary != "摘要一" || first.PublishedAt != "2026-11-01" {
		t.Fatalf("字段映射不对（search.ts:48-64）: %+v", first)
	}
	second := result.Sources[1]
	if second.Title != "" || second.Snippet != "" || second.Summary != "" || second.PublishedAt != "" {
		t.Fatalf("空字段必须缺席而不是填空串（search.ts:57-60）: %+v", second)
	}
	if result.Truncated {
		t.Fatal("Search API 自己不做截断（search.ts:63 truncated:false）")
	}
}

// count 的上限：配置 1..50，且调用方的 maxResults 更小时取更小者。
func TestRC2SearchAPICountBounds(t *testing.T) {
	cases := []struct {
		name    string
		count   int
		limit   int
		want    float64
		comment string
	}{
		{"caller smaller", 10, 3, 3, "search.ts:106 min(count, maxResults)"},
		{"caller zero uses count", 10, 0, 10, "maxResults 缺省时用 count"},
		{"config larger", 50, 0, 50, "schema 上限 50（index.ts:100）"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seenCount any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				seenCount = body["count"]
				io.WriteString(w, `{"results":[]}`)
			}))
			defer srv.Close()
			provider := &DeepSeekSearchAPI{
				Resolve: func() DeepSeekWebAPIOptions {
					return DeepSeekWebAPIOptions{
						Config: WebAPIConfig{Count: tc.count, RequestsPerSecond: 5, Billing: BillingAuto, BaseURL: srv.URL},
						Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}),
					}
				},
				HTTP: srv.Client(),
			}
			if _, err := provider.Search(context.Background(), "q", tc.limit); err != nil {
				t.Fatal(err)
			}
			if seenCount != tc.want {
				t.Fatalf("%s: count 该是 %v；实际 %v", tc.comment, tc.want, seenCount)
			}
		})
	}
}

// 限速：一秒窗口内最多 searchRequestsPerSecond 次，超出的那次要等（search.ts:121-136）。
func TestRC2SearchAPIRateLimitsRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	clock := &fakeClock{now: time.Unix(1000, 0)}
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{Count: 10, RequestsPerSecond: 2, Billing: BillingAuto, BaseURL: srv.URL},
				Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}),
			}
		},
		HTTP: srv.Client(),
		Now:  clock.Now,
		Wait: clock.Wait,
	}
	for i := 0; i < 3; i++ {
		if _, err := provider.Search(context.Background(), "q", 0); err != nil {
			t.Fatalf("第 %d 次搜索失败: %v", i+1, err)
		}
	}
	if len(clock.waits) != 1 {
		t.Fatalf("第三次要等一个窗口（limit=2）；实际等待 %d 次: %v", len(clock.waits), clock.waits)
	}
	// 官方：等到"窗口里最早那次 + 1001ms"（search.ts:134 oldest + 1001 - now）。
	if want := 1001 * time.Millisecond; clock.waits[0] != want {
		t.Fatalf("等待时长该是 %v；实际 %v", want, clock.waits[0])
	}
}

// 限速等待必须可被取消：等的时候 ctx 断了要 WEB_ABORTED（search.ts:121-136 + transport.ts:411-425）。
func TestRC2SearchAPIRateLimitWaitAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	clock := &fakeClock{now: time.Unix(1000, 0)}
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{Count: 10, RequestsPerSecond: 1, Billing: BillingAuto, BaseURL: srv.URL},
				Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}),
			}
		},
		HTTP: srv.Client(),
		Now:  clock.Now,
		// 模拟"等到一半被取消"。
		Wait: func(ctx context.Context, d time.Duration) error { return context.Canceled },
	}
	if _, err := provider.Search(context.Background(), "q", 0); err != nil {
		t.Fatal(err)
	}
	_, err := provider.Search(context.Background(), "q", 0)
	if spi.WebErrorCode(err) != spi.WebAborted {
		t.Fatalf("限速等待被取消要报 %s；实际 %q（%v）", spi.WebAborted, spi.WebErrorCode(err), err)
	}
}

// Fetch API：请求 {url}，响应 {content}（fetch.ts:51-62）。
func TestRC2FetchAPIOfficialRequestShape(t *testing.T) {
	var seenPath, seenAuth, seenAgent string
	var seenBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("Authorization")
		seenAgent = r.Header.Get("User-Agent")
		_ = json.NewDecoder(r.Body).Decode(&seenBody)
		io.WriteString(w, `{"content":"# 标题\n\n正文"}`)
	}))
	defer srv.Close()

	provider := &DeepSeekFetchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{Billing: BillingAuto, BaseURL: srv.URL},
				Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}),
			}
		},
		HTTP: srv.Client(),
	}
	if !provider.Available() {
		t.Fatal("有 key + 合法 baseURL 就该可用")
	}
	result, err := provider.Fetch(context.Background(), "https://target.example/a")
	if err != nil {
		t.Fatal(err)
	}
	if seenPath != "/fetch" {
		t.Fatalf("端点 = baseURL + /fetch（fetch.ts:54）；实际 %q", seenPath)
	}
	if seenAuth != "Bearer sk-test" {
		t.Fatalf("认证头不对: %q", seenAuth)
	}
	if seenAgent != deepSeekWebAPIUserAgent {
		t.Fatalf("user-agent 不对: %q", seenAgent)
	}
	if seenBody["url"] != "https://target.example/a" {
		t.Fatalf("body 必须是 {url}（fetch.ts:55）；实际 %v", seenBody)
	}
	if result.URL != "https://target.example/a" || result.StatusCode != 200 {
		t.Fatalf("结果 url/statusCode 不对（fetch.ts:58）: %+v", result)
	}
	if result.Body.Kind != spi.FetchBodyText || result.Body.Content != "# 标题\n\n正文" {
		t.Fatalf("正文映射不对: %+v", result.Body)
	}
	if result.Bytes != len(result.Body.Content) || result.Truncated {
		t.Fatalf("宿主附加字段不对: bytes=%d truncated=%v", result.Bytes, result.Truncated)
	}
}

// 错误映射（transport.ts:207-262）：HTTP 错误带状态码 + 上游 detail + 端点与恢复指引。
func TestRC2SearchAPIErrorMapping(t *testing.T) {
	t.Run("http error with detail", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"upstream boom"}}`)
		}))
		defer srv.Close()
		provider := &DeepSeekSearchAPI{
			Resolve: func() DeepSeekWebAPIOptions {
				return DeepSeekWebAPIOptions{
					Config: WebAPIConfig{Count: 10, RequestsPerSecond: 5, Billing: BillingAuto, BaseURL: srv.URL},
					Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}),
				}
			},
			HTTP: srv.Client(),
		}
		_, err := provider.Search(context.Background(), "q", 0)
		if spi.WebErrorCode(err) != spi.WebProviderError {
			t.Fatalf("HTTP 错误要报 %s；实际 %q", spi.WebProviderError, spi.WebErrorCode(err))
		}
		for _, want := range []string{"HTTP 500", "upstream boom", "/search", "searchBaseURL", "searchApiKeyEnv"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("错误文案缺少 %q: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "sk-test") {
			t.Fatalf("错误里泄露了凭据: %v", err)
		}
	})

	t.Run("invalid body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"results":"not-an-array"}`)
		}))
		defer srv.Close()
		provider := &DeepSeekSearchAPI{
			Resolve: func() DeepSeekWebAPIOptions {
				return DeepSeekWebAPIOptions{
					Config: WebAPIConfig{Count: 10, RequestsPerSecond: 5, Billing: BillingAuto, BaseURL: srv.URL},
					Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}),
				}
			},
			HTTP: srv.Client(),
		}
		_, err := provider.Search(context.Background(), "q", 0)
		if spi.WebErrorCode(err) != spi.WebProviderError || !strings.Contains(err.Error(), "invalid response body") {
			t.Fatalf("非法响应体要报 %s 且点名 invalid response body（transport.ts:159-161）；实际 %q %v",
				spi.WebProviderError, spi.WebErrorCode(err), err)
		}
	})

	t.Run("aborted", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer srv.Close()
		provider := &DeepSeekSearchAPI{
			Resolve: func() DeepSeekWebAPIOptions {
				return DeepSeekWebAPIOptions{
					Config: WebAPIConfig{Count: 10, RequestsPerSecond: 5, Billing: BillingAuto, BaseURL: srv.URL},
					Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}),
				}
			},
			HTTP: srv.Client(),
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := provider.Search(ctx, "q", 0)
		if spi.WebErrorCode(err) != spi.WebAborted {
			t.Fatalf("取消要报 %s；实际 %q（%v）", spi.WebAborted, spi.WebErrorCode(err), err)
		}
	})
}

// 重定向必须失败（官方 `redirect: 'error'`，search.ts:67-68 → WEB_PROVIDER_ERROR）。
func TestRC2SearchAPIRejectsRedirect(t *testing.T) {
	var target *httptest.Server
	target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	defer target.Close()
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{Count: 10, RequestsPerSecond: 5, Billing: BillingAuto, BaseURL: target.URL},
				Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "sk-test"}),
			}
		},
	}
	_, err := provider.Search(context.Background(), "q", 0)
	if spi.WebErrorCode(err) != spi.WebProviderError {
		t.Fatalf("重定向要失败并报 %s；实际 %q（%v）", spi.WebProviderError, spi.WebErrorCode(err), err)
	}
}

// billing 决策树（index.ts:232-259）：auto×路由、account、api-key、自有 baseURL 优先。
func TestRC2BillingDecisionTree(t *testing.T) {
	type want struct {
		auth  string // authorization 头
		token string // x-dsh-auth-token 头
		path  string // 端点路径：api-key 源 = 给定 base + /search；账号源 = origin + /v1/tools/search
		code  string // 期望错误码（空 = 成功）
	}
	cases := []struct {
		name    string
		config  WebAPIConfig
		account bool
		env     map[string]string
		want    want
	}{
		{
			name:   "api-key billing uses key only",
			config: WebAPIConfig{Billing: BillingAPIKey, Count: 10, RequestsPerSecond: 5},
			env:    map[string]string{"DEEPSEEK_API_KEY": "sk-key"},
			want:   want{auth: "Bearer sk-key", path: "/search"},
		},
		{
			name:    "account billing uses token only",
			config:  WebAPIConfig{Billing: BillingAccount, Count: 10, RequestsPerSecond: 5},
			account: true,
			env:     map[string]string{"DEEPSEEK_API_KEY": "sk-key"},
			want:    want{token: "acct-token", path: "/v1/tools/search"},
		},
		{
			name:    "auto in account conversation prefers account token",
			config:  WebAPIConfig{Billing: BillingAuto, LLMProvider: "deepseek-account", Count: 10, RequestsPerSecond: 5},
			account: true,
			env:     map[string]string{"DEEPSEEK_API_KEY": "sk-key"},
			want:    want{token: "acct-token", path: "/v1/tools/search"},
		},
		{
			name:   "auto in official conversation uses key even with account",
			config: WebAPIConfig{Billing: BillingAuto, LLMProvider: "deepseek-official", Count: 10, RequestsPerSecond: 5},
			env:    map[string]string{"DEEPSEEK_API_KEY": "sk-key"},
			// account 源不在这个规则的列表里（index.ts:252-253 只给 officialSource）。
			want: want{auth: "Bearer sk-key", path: "/search"},
		},
		{
			name:   "auto outside a conversation prefers key",
			config: WebAPIConfig{Billing: BillingAuto, Count: 10, RequestsPerSecond: 5},
			env:    map[string]string{"DEEPSEEK_API_KEY": "sk-key"},
			// other-conversation = [official, account]（index.ts:254）。
			want: want{auth: "Bearer sk-key", path: "/search"},
		},
		{
			name:    "auto outside a conversation falls back to account without key",
			config:  WebAPIConfig{Billing: BillingAuto, Count: 10, RequestsPerSecond: 5},
			account: true,
			want:    want{token: "acct-token", path: "/v1/tools/search"},
		},
		{
			name:   "api-key billing without key is credential missing",
			config: WebAPIConfig{Billing: BillingAPIKey, Count: 10, RequestsPerSecond: 5},
			want:   want{code: spi.WebProviderCredentialMissing},
		},
		{
			name:   "account billing without account is credential missing",
			config: WebAPIConfig{Billing: BillingAccount, Count: 10, RequestsPerSecond: 5},
			env:    map[string]string{"DEEPSEEK_API_KEY": "sk-key"},
			want:   want{code: spi.WebProviderCredentialMissing},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth, gotToken, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotToken = r.Header.Get("x-dsh-auth-token")
				gotPath = r.URL.Path
				io.WriteString(w, `{"results":[]}`)
			}))
			defer srv.Close()
			// 让官方/账号路由都打到本地桩：*BaseURL 不设时靠 env 指路（官方 base 不可出网）。
			cfg := tc.config
			cfg.BaseURL = ""
			provider := &DeepSeekSearchAPI{
				Resolve: func() DeepSeekWebAPIOptions {
					return DeepSeekWebAPIOptions{
						Config: cfg,
						Getenv: staticEnv(tc.env),
						Account: WebAPIAccount{
							BaseURL:    srv.URL,
							Configured: func() bool { return tc.account },
							ResolveToken: func(ctx context.Context, endpoint string) (string, error) {
								if !tc.account {
									return "", nil
								}
								if !strings.HasPrefix(endpoint, srv.URL) {
									t.Errorf("账号令牌要发到账号路由的端点；实际 %q", endpoint)
								}
								return "acct-token", nil
							},
						},
						OfficialBaseURL: srv.URL,
					}
				},
				HTTP: srv.Client(),
			}
			_, err := provider.Search(context.Background(), "q", 0)
			if tc.want.code != "" {
				if spi.WebErrorCode(err) != tc.want.code {
					t.Fatalf("期望错误码 %s；实际 %q（%v）", tc.want.code, spi.WebErrorCode(err), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotAuth != tc.want.auth || gotToken != tc.want.token {
				t.Fatalf("凭据选择不对: authorization=%q token=%q（期望 %q/%q）", gotAuth, gotToken, tc.want.auth, tc.want.token)
			}
			if gotPath != tc.want.path {
				t.Fatalf("端点拼接不对: %q（期望 %q）", gotPath, tc.want.path)
			}
		})
	}
}

// 自有 *BaseURL / *ApiKeyEnv 无论 billing 都优先（index.ts:221-231）。
func TestRC2OwnEndpointAndKeyTakePrecedence(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{
					Billing: BillingAccount, // billing 是 account，但自有端点+key 仍然优先
					Count:   10, RequestsPerSecond: 5,
					BaseURL: srv.URL, ApiKeyEnv: "MY_SEARCH_KEY",
				},
				Getenv: staticEnv(map[string]string{"MY_SEARCH_KEY": "own-key", "DEEPSEEK_API_KEY": "other"}),
			}
		},
		HTTP: srv.Client(),
	}
	if _, err := provider.Search(context.Background(), "q", 0); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer own-key" {
		t.Fatalf("自有 searchApiKeyEnv 必须优先（index.ts:221-231）；实际 %q", gotAuth)
	}
	if gotPath != "/search" {
		t.Fatalf("自有 searchBaseURL 必须优先；实际 %q", gotPath)
	}
}

// 缺凭据的文案要能直接指导用户（index.ts:324-353）。
func TestRC2MissingCredentialMessages(t *testing.T) {
	cases := []struct {
		name   string
		config WebAPIConfig
		wants  []string
	}{
		// other-conversation：官方文案只给"配 key 或登录账号"两条路，不点 billing（index.ts:339-342）。
		{"auto outside", WebAPIConfig{Billing: BillingAuto}, []string{"DEEPSEEK_API_KEY", "sign in"}},
		// api-key：点明 billing 可以改成 account / auto（index.ts:347-349）。
		{"api-key", WebAPIConfig{Billing: BillingAPIKey}, []string{"API key", "searchBilling", "account", "auto"}},
		// account：点明 billing 可以改成 api-key / auto（index.ts:343-346）。
		{"account", WebAPIConfig{Billing: BillingAccount}, []string{"sign in", "searchBilling", "api-key", "auto"}},
		// official-conversation：点明 billing=account 的路（index.ts:335-338）。
		{"official conversation", WebAPIConfig{Billing: BillingAuto, LLMProvider: "deepseek-official"},
			[]string{"DEEPSEEK_API_KEY", "searchBilling", "account"}},
		// account-conversation：账号 + key 都没有（index.ts:331-334）。
		{"account conversation", WebAPIConfig{Billing: BillingAuto, LLMProvider: "deepseek-account"},
			[]string{"sign in", "DEEPSEEK_API_KEY"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &DeepSeekSearchAPI{
				Resolve: func() DeepSeekWebAPIOptions {
					return DeepSeekWebAPIOptions{
						Config: tc.config,
						Getenv: staticEnv(nil),
					}
				},
			}
			_, err := provider.Search(context.Background(), "q", 0)
			if spi.WebErrorCode(err) != spi.WebProviderCredentialMissing {
				t.Fatalf("要报 %s；实际 %q", spi.WebProviderCredentialMissing, spi.WebErrorCode(err))
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("文案里要出现 %q: %v", want, err)
				}
			}
		})
	}
}

// sendOriginalQuestion=false → 绝不发 ori_question（index.ts:183-186）。
func TestRC2SendOriginalQuestionFalseOmitsField(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{Count: 10, RequestsPerSecond: 5, SendOriginalQuestion: false, Billing: BillingAuto, BaseURL: srv.URL},
				Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}),
			}
		},
		OriginalQuestion: func() string { return "触发这次搜索的问题" },
		HTTP:             srv.Client(),
	}
	if _, err := provider.Search(context.Background(), "q", 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["ori_question"]; ok {
		t.Fatalf("sendOriginalQuestion=false 时不该有 ori_question: %v", body)
	}
}

// 旧字段（baseURL / apiKeyEnv / apiKey）不再生效：写了也不能改变行为，更不能让加载报错
// （index.ts:61-62「they load without error and have no effect」）。
func TestRC2LegacyFieldsHaveNoEffect(t *testing.T) {
	seam, err := NewWebProvidersFromEnv(staticEnv(map[string]string{
		"DSHD_WEB_BASE_URL":    "https://legacy.invalid/v1/tools",
		"DSHD_WEB_API_KEY_ENV": "LEGACY_KEY",
		"DSHD_WEB_API_KEY":     "legacy-literal-key",
		"LEGACY_KEY":           "legacy-key",
	}))
	if err != nil {
		t.Fatalf("旧字段不能让装配报错: %v", err)
	}
	// 旧字段喂不出可用性：没有 DEEPSEEK_API_KEY，两个新 provider 都必须不可用。
	for _, status := range seam.SearchProviderStatuses() {
		if status.ID == DeepSeekSearchAPIProviderID && status.Available {
			t.Fatal("旧 apiKey/baseURL 字段不能算凭据（无效果），provider 不该可用")
		}
	}
	for _, status := range seam.FetchProviderStatuses() {
		if status.ID == DeepSeekFetchAPIProviderID && status.Available {
			t.Fatal("旧 apiKey/baseURL 字段不能算凭据，fetch provider 不该可用")
		}
	}
}

// 没有凭据时两个 provider 都不可用，调用要结构化报错（不是空结果）。
func TestRC2UnavailableWithoutCredential(t *testing.T) {
	env := staticEnv(nil)
	searchAPI := NewDeepSeekSearchAPIFromEnv(env)
	if searchAPI.Available() {
		t.Fatal("没有 DEEPSEEK_API_KEY 时 Search API provider 不该可用")
	}
	err := func() error { _, err := searchAPI.Search(context.Background(), "q", 0); return err }()
	if spi.WebErrorCode(err) != spi.WebProviderCredentialMissing {
		t.Fatalf("Search 缺凭据要报 %s；实际 %q", spi.WebProviderCredentialMissing, spi.WebErrorCode(err))
	}
	fetchAPI := NewDeepSeekFetchAPIFromEnv(env)
	if fetchAPI.Available() {
		t.Fatal("没有 DEEPSEEK_API_KEY 时 Fetch API provider 不该可用")
	}
	_, err = fetchAPI.Fetch(context.Background(), "https://example.com")
	if spi.WebErrorCode(err) != spi.WebProviderCredentialMissing {
		t.Fatalf("Fetch 缺凭据要报 %s；实际 %q", spi.WebProviderCredentialMissing, spi.WebErrorCode(err))
	}
}

// 环境解析：9 个官方配置项按官方默认/边界解析（index.ts:99-109）。
func TestRC2ConfigFromEnv(t *testing.T) {
	config := WebAPISettingsFromEnv(staticEnv(nil))
	if config.Count != DeepSeekDefaultCount || config.RequestsPerSecond != DeepSeekSearchRequestsPerSecond ||
		!config.SendOriginalQuestion || config.SearchBilling != BillingAuto || config.FetchBilling != BillingAuto {
		t.Fatalf("默认值不对: %+v", config)
	}
	config = WebAPISettingsFromEnv(staticEnv(map[string]string{
		"DSHD_WEB_SEARCH_COUNT":               "50",
		"DSHD_WEB_SEARCH_REQUESTS_PER_SECOND": "7",
		"DSHD_WEB_SEND_ORIGINAL_QUESTION":     "false",
		"DSHD_WEB_SEARCH_BILLING":             "account",
		"DSHD_WEB_FETCH_BILLING":              "api-key",
		"DSHD_WEB_SEARCH_API_BASE_URL":        "https://s.example/v1/tools",
		"DSHD_WEB_SEARCH_API_KEY_ENV":         "S_KEY",
		"DSHD_WEB_FETCH_API_BASE_URL":         "https://f.example/v1/tools",
		"DSHD_WEB_FETCH_API_KEY_ENV":          "F_KEY",
		"DSHD_WEB_LLM_PROVIDER":               "deepseek-account",
	}))
	if config.Count != 50 || config.RequestsPerSecond != 7 || config.SendOriginalQuestion ||
		config.SearchBilling != BillingAccount || config.FetchBilling != BillingAPIKey ||
		config.SearchBaseURL != "https://s.example/v1/tools" || config.SearchAPIKeyEnv != "S_KEY" ||
		config.FetchBaseURL != "https://f.example/v1/tools" || config.FetchAPIKeyEnv != "F_KEY" ||
		config.LLMProvider != "deepseek-account" {
		t.Fatalf("显式配置没被读出来: %+v", config)
	}
	// 越界/非法回落默认，不静默改语义（count 1..50、rps >= 1）。
	clamped := WebAPISettingsFromEnv(staticEnv(map[string]string{
		"DSHD_WEB_SEARCH_COUNT":               "999",
		"DSHD_WEB_SEARCH_REQUESTS_PER_SECOND": "0",
		"DSHD_WEB_SEARCH_BILLING":             "not-a-mode",
		"DSHD_WEB_SEND_ORIGINAL_QUESTION":     "maybe",
	}))
	if clamped.Count != DeepSeekDefaultCount || clamped.RequestsPerSecond != DeepSeekSearchRequestsPerSecond ||
		clamped.SearchBilling != BillingAuto || !clamped.SendOriginalQuestion {
		t.Fatalf("非法值必须回落官方默认: %+v", clamped)
	}
}

// 真正的限速证据：真实时钟下第 3 次请求被推迟到第一个窗口之后。
// 只打 httptest 桩，不出网。
func TestRC2SearchAPIRateLimitRealClock(t *testing.T) {
	if testing.Short() {
		t.Skip("短暂模式跳过真实时钟的 1s 等待")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{Count: 10, RequestsPerSecond: 2, Billing: BillingAuto, BaseURL: srv.URL},
				Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}),
			}
		},
		HTTP: srv.Client(),
	}
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := provider.Search(context.Background(), "q", 0); err != nil {
			t.Fatalf("第 %d 次搜索失败: %v", i+1, err)
		}
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("限速 2 rps 时第三次必须等到 1s 窗口之后；实际只用了 %v", elapsed)
	}
}
