package search

// 本文件是 E2 的**第二批判定测试**（先红文件 `webapi_red_test.go` 覆盖公开形状与
// 官方默认值；这里补端到端、边界与失败路径）。仍然**禁止真出网**：所有网络断言
// 都打在 `net/http/httptest` 桩上，报告里另有一条 `HTTP_PROXY` 指向死端口的跑法作为
// "没有任何测试真的出网"的证据。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/spi"
)

// 端到端：环境装配 → 接缝选择 → 真 HTTP 请求 → 结果上限截断。
func TestRC2SeamSearchEndToEnd(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/search" {
			t.Errorf("路径不对: %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-live" {
			t.Errorf("认证头不对: %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"results":[
			{"url":"https://a.example","title":"A","snippet":"a","date":null},
			{"url":"https://b.example","title":"B","snippet":"b","date":"2026-11-02"},
			{"url":"https://c.example","title":"C","snippet":"c","date":null}
		]}`)
	}))
	defer srv.Close()

	seam, err := NewWebProvidersFromEnv(staticEnv(map[string]string{
		"DEEPSEEK_API_KEY":             "sk-live",
		"DSHD_WEB_SEARCH_API_BASE_URL": srv.URL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := seam.Search(context.Background(), "端到端", 2)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("应只发一次请求；实际 %d", hits)
	}
	if len(result.Sources) != 2 || !result.Truncated {
		t.Fatalf("接缝要按 maxResults 截断并置 Truncated: %+v", result)
	}
	if result.Sources[0].URL != "https://a.example" || result.Sources[1].Title != "B" {
		t.Fatalf("顺序/字段不对: %+v", result.Sources)
	}
}

// 端到端：抓取侧钉到 deepseek-fetch-api 时走 Fetch API（默认是 http，必须显式钉）。
func TestRC2SeamFetchEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fetch" {
			t.Errorf("路径不对: %q", r.URL.Path)
		}
		io.WriteString(w, `{"content":"页面正文"}`)
	}))
	defer srv.Close()
	seam, err := NewWebProvidersFromEnv(staticEnv(map[string]string{
		"DEEPSEEK_API_KEY":            "sk-live",
		"DSHD_WEB_FETCH_API_BASE_URL": srv.URL,
		"DSHD_WEB_FETCH_PROVIDER":     DeepSeekFetchAPIProviderID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := seam.Fetch(context.Background(), "https://target.example/page")
	if err != nil {
		t.Fatal(err)
	}
	if result.Body.Content != "页面正文" || result.StatusCode != 200 {
		t.Fatalf("结果不对: %+v", result)
	}
}

// 响应校验必须照官方的严格程度（`search.ts:48-64`）。
func TestRC2SearchAPIResponseValidation(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		valid   bool
		sources int
	}{
		{"empty results is success", `{"results":[]}`, true, 0},
		{"missing results", `{}`, false, 0},
		{"results not array", `{"results":{}}`, false, 0},
		{"item missing url", `{"results":[{"title":"t","snippet":"s","date":null}]}`, false, 0},
		{"item empty url", `{"results":[{"url":"","title":"t","snippet":"s","date":null}]}`, false, 0},
		{"item missing title", `{"results":[{"url":"https://a","snippet":"s","date":null}]}`, false, 0},
		{"item non-string title", `{"results":[{"url":"https://a","title":7,"snippet":"s","date":null}]}`, false, 0},
		{"item missing snippet", `{"results":[{"url":"https://a","title":"t","date":null}]}`, false, 0},
		{"summary null is invalid", `{"results":[{"url":"https://a","title":"t","snippet":"s","summary":null,"date":null}]}`, false, 0},
		{"item missing date", `{"results":[{"url":"https://a","title":"t","snippet":"s"}]}`, false, 0},
		{"date number is invalid", `{"results":[{"url":"https://a","title":"t","snippet":"s","date":5}]}`, false, 0},
		{"blank fields collapse to absent", `{"results":[{"url":"https://a","title":"","snippet":"","summary":"","date":""}]}`, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.payload)
			}))
			defer srv.Close()
			provider := &DeepSeekSearchAPI{
				Resolve: func() DeepSeekWebAPIOptions {
					return DeepSeekWebAPIOptions{
						Config: WebAPIConfig{Count: 10, RequestsPerSecond: 5, Billing: BillingAuto, BaseURL: srv.URL},
						Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}),
					}
				},
				HTTP: srv.Client(),
			}
			result, err := provider.Search(context.Background(), "q", 0)
			if !tc.valid {
				if spi.WebErrorCode(err) != spi.WebProviderError || !strings.Contains(err.Error(), "invalid response body") {
					t.Fatalf("非法响应要报 %s + invalid response body；实际 %q（%v）",
						spi.WebProviderError, spi.WebErrorCode(err), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Sources) != tc.sources {
				t.Fatalf("结果条数该是 %d；实际 %d", tc.sources, len(result.Sources))
			}
		})
	}
}

// Fetch API 响应校验（`fetch.ts:56-59`：content 必须是 string）。
func TestRC2FetchAPIResponseValidation(t *testing.T) {
	cases := []struct {
		payload string
		valid   bool
		content string
	}{
		{`{"content":""}`, true, ""},
		{`{"content":"x"}`, true, "x"},
		{`{}`, false, ""},
		{`{"content":null}`, false, ""},
		{`{"content":1}`, false, ""},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, tc.payload)
		}))
		provider := &DeepSeekFetchAPI{
			Resolve: func() DeepSeekWebAPIOptions {
				return DeepSeekWebAPIOptions{
					Config: WebAPIConfig{Billing: BillingAuto, BaseURL: srv.URL},
					Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}),
				}
			},
			HTTP: srv.Client(),
		}
		result, err := provider.Fetch(context.Background(), "https://target.example")
		srv.Close()
		if !tc.valid {
			if spi.WebErrorCode(err) != spi.WebProviderError || !strings.Contains(err.Error(), "invalid response body") {
				t.Fatalf("%s: 期望 invalid response body；实际 %q（%v）", tc.payload, spi.WebErrorCode(err), err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.payload, err)
		}
		if result.Body.Content != tc.content {
			t.Fatalf("%s: content 映射不对: %q", tc.payload, result.Body.Content)
		}
	}
}

// 账号令牌被 401 拒绝：先撤销那一个令牌，再告诉用户"重新登录"，端点不需要改
// （官方 `transport.ts:237-245`）。
func TestRC2AccountTokenRejectedIsRevoked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-dsh-auth-token") != "acct-token" {
			t.Errorf("账号令牌没带上: %q", r.Header.Get("x-dsh-auth-token"))
		}
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"token expired"}}`)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var rejected []string
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{Billing: BillingAccount, Count: 10, RequestsPerSecond: 5},
				Getenv: staticEnv(nil),
				Account: WebAPIAccount{
					BaseURL:    srv.URL,
					Configured: func() bool { return true },
					ResolveToken: func(context.Context, string) (string, error) {
						return "acct-token", nil
					},
					RejectToken: func(_ context.Context, token string) error {
						mu.Lock()
						defer mu.Unlock()
						rejected = append(rejected, token)
						return nil
					},
				},
			}
		},
		HTTP: srv.Client(),
	}
	_, err := provider.Search(context.Background(), "q", 0)
	if spi.WebErrorCode(err) != spi.WebProviderError {
		t.Fatalf("401 要报 %s；实际 %q", spi.WebProviderError, spi.WebErrorCode(err))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rejected) != 1 || rejected[0] != "acct-token" {
		t.Fatalf("被拒的令牌要被撤销一次；实际 %v", rejected)
	}
	if !strings.Contains(err.Error(), "sign in") || !strings.Contains(err.Error(), "token expired") {
		t.Fatalf("文案要带上游 detail 与重新登录指引: %v", err)
	}
}

// 只设 `*ApiKeyEnv`（不设 `*BaseURL`）：端点用官方 base，凭据用那一处引用
// （官方 `index.ts:221-231`）。
func TestRC2OwnApiKeyEnvOnlyUsesOfficialBase(t *testing.T) {
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
					Billing: BillingAuto, Count: 10, RequestsPerSecond: 5, ApiKeyEnv: "OWN_KEY",
				},
				Getenv:          staticEnv(map[string]string{"OWN_KEY": "own-key"}),
				OfficialBaseURL: srv.URL,
			}
		},
		HTTP: srv.Client(),
	}
	if _, err := provider.Search(context.Background(), "q", 0); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer own-key" || gotPath != "/search" {
		t.Fatalf("端点/凭据不对: auth=%q path=%q", gotAuth, gotPath)
	}
}

// 端点写坏了 = misconfigured（不是悄悄换成别的端点）。
func TestRC2MisconfiguredBaseURL(t *testing.T) {
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{Billing: BillingAuto, Count: 10, RequestsPerSecond: 5, BaseURL: "not-a-url"},
				Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}),
			}
		},
	}
	if provider.Available() {
		t.Fatal("端点写坏了不该算可用")
	}
	_, err := provider.Search(context.Background(), "q", 0)
	if spi.WebErrorCode(err) != WebProviderMisconfigured {
		t.Fatalf("要报 %s；实际 %q（%v）", WebProviderMisconfigured, spi.WebErrorCode(err), err)
	}
	if !strings.Contains(err.Error(), "searchBaseURL") {
		t.Fatalf("文案要点名该改哪个字段: %v", err)
	}
}

// 每次操作读一次设置（官方 `resolveOptions()`）：配置改了，下一次操作立刻生效。
func TestRC2ConfigIsLivePerOperation(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	current := DeepSeekWebAPIOptions{
		Config: WebAPIConfig{Count: 10, RequestsPerSecond: 5, Billing: BillingAuto, BaseURL: srv.URL + "/first"},
		Getenv: staticEnv(map[string]string{"DEEPSEEK_API_KEY": "k"}),
	}
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions { return current },
		HTTP:    srv.Client(),
	}
	if _, err := provider.Search(context.Background(), "q", 0); err != nil {
		t.Fatal(err)
	}
	current.Config.BaseURL = srv.URL + "/second"
	if _, err := provider.Search(context.Background(), "q", 0); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/first/search,/second/search" {
		t.Fatalf("设置必须每次操作重读（官方 resolveOptions）：%v", paths)
	}
}

// 缺凭据时**不发请求**（证明不会偷偷打到官方端点）。
func TestRC2MissingCredentialSendsNothing(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	provider := &DeepSeekSearchAPI{
		Resolve: func() DeepSeekWebAPIOptions {
			return DeepSeekWebAPIOptions{
				Config: WebAPIConfig{Billing: BillingAPIKey, Count: 10, RequestsPerSecond: 5, BaseURL: srv.URL},
				Getenv: staticEnv(nil),
			}
		},
		HTTP: srv.Client(),
	}
	if _, err := provider.Search(context.Background(), "q", 0); err == nil {
		t.Fatal("缺凭据必须报错")
	}
	if hits != 0 {
		t.Fatalf("缺凭据时不该发请求；实际 %d 次", hits)
	}
}
