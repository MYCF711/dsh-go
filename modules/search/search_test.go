package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/spi"
)

// SearxNG 夹具：真实风格 JSON（answers + results，含重复 URL）。
func TestSearxNGSearchesAndDedupes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "官方 同步" {
			t.Errorf("查询参数没有正确转义: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"answers":["摘要答案"],"results":[
			{"title":"一","url":"https://a.example","content":"片段一"},
			{"title":"重复","url":"https://a.example","content":"重复"},
			{"title":"二","url":"https://b.example","content":"片段二"}
		]}`)
	}))
	defer srv.Close()

	provider := &SearxNG{Endpoint: srv.URL + "/search?q={query}&format=json", HTTP: srv.Client()}
	if !provider.Available() {
		t.Fatal("配了端点就该可用")
	}
	result, err := provider.Search(context.Background(), "官方 同步", 8)
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "摘要答案" || len(result.Sources) != 2 {
		t.Fatalf("映射不对: %+v", result)
	}
	if result.Sources[0].URL != "https://a.example" || result.Sources[0].Snippet != "片段一" {
		t.Fatalf("来源字段不对: %+v", result.Sources[0])
	}
}

func TestSearxNGUnavailableWithoutEndpoint(t *testing.T) {
	provider := &SearxNG{}
	if provider.Available() {
		t.Fatal("没配端点不能算可用")
	}
	if _, err := provider.Search(context.Background(), "x", 8); spi.WebErrorCode(err) != spi.WebProviderUnavailable {
		t.Fatalf("没有端点要结构化报错: %v", err)
	}
}

func TestSearxNGUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "bad gateway")
	}))
	defer srv.Close()
	provider := &SearxNG{Endpoint: srv.URL + "/search?q={query}", HTTP: srv.Client()}
	_, err := provider.Search(context.Background(), "x", 8)
	if spi.WebErrorCode(err) != spi.WebProviderError || !strings.Contains(err.Error(), "502") {
		t.Fatalf("上游错误要带状态码与稳定码: %v", err)
	}
}

// DeepSeek 原生搜索：钉住请求形状（官方 dsh-web-search-deepseek 的 Messages 请求）。
func TestDeepSeekSearchRequestAndResponseShape(t *testing.T) {
	const secret = "deepseek-secret-key"
	var mu sync.Mutex
	var seenPath, seenAPIKey, seenVersion string
	var seenBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenPath = r.URL.Path
		seenAPIKey = r.Header.Get("x-api-key")
		seenVersion = r.Header.Get("anthropic-version")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[
			{"type":"text","text":"I searched.","citations":[{"url":"https://a.example","cited_text":"引用片段"}]},
			{"type":"web_search_tool_result","content":[
				{"type":"web_search_result","url":"https://a.example","title":"来源一","page_age":"2026-11-01"},
				{"type":"web_search_result","url":"https://a.example","title":"重复"},
				{"type":"web_search_result","url":"https://b.example","title":"来源二"}
			]}
		]}`)
	}))
	defer srv.Close()

	provider := &DeepSeek{BaseURL: srv.URL + "/anthropic/v1", APIKey: secret, Model: "deepseek-v4-flash"}
	if !provider.Available() {
		t.Fatal("有 key + 合法端点就该可用")
	}
	result, err := provider.Search(context.Background(), "官方同步", 8)
	if err != nil {
		t.Fatal(err)
	}
	if seenPath != "/anthropic/v1/messages" {
		t.Fatalf("端点拼接不对: %q", seenPath)
	}
	if seenAPIKey != secret || seenVersion != DeepSeekDefaultAPIVersion {
		t.Fatalf("认证头不对: key=%q version=%q", seenAPIKey, seenVersion)
	}
	// 请求体形状：model / max_tokens / messages（text 块）/ tools（web_search_20250305）/ thinking disabled。
	if seenBody["model"] != "deepseek-v4-flash" {
		t.Fatalf("model 不对: %v", seenBody["model"])
	}
	if seenBody["max_tokens"] != float64(DeepSeekDefaultMaxTokens) {
		t.Fatalf("max_tokens 不对: %v", seenBody["max_tokens"])
	}
	tools, _ := seenBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools 不对: %v", seenBody["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "web_search_20250305" || tool["name"] != "web_search" || tool["max_uses"] != float64(DeepSeekDefaultMaxUses) {
		t.Fatalf("原生搜索工具声明不对: %v", tool)
	}
	thinking, _ := seenBody["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking 应为 disabled: %v", seenBody["thinking"])
	}
	messages, _ := seenBody["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)
	if !strings.Contains(text["text"].(string), "官方同步") {
		t.Fatalf("查询没进请求: %v", text)
	}

	if len(result.Sources) != 2 {
		t.Fatalf("结果应按 URL 去重: %+v", result.Sources)
	}
	if result.Sources[0].URL != "https://a.example" || result.Sources[0].Snippet != "引用片段" || result.Sources[0].PublishedAt != "2026-11-01" {
		t.Fatalf("来源映射不对: %+v", result.Sources[0])
	}
	if result.Sources[0].Title != "来源一" {
		t.Fatalf("标题不对: %+v", result.Sources[0])
	}
}

// 没有 web_search_tool_result 块 = 错误（不是"零命中"，也不能抓正文猜结果）。
func TestDeepSeekSearchWithoutResultBlocksIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"text","text":"I did not search."}]}`)
	}))
	defer srv.Close()
	provider := &DeepSeek{BaseURL: srv.URL, APIKey: "k"}
	_, err := provider.Search(context.Background(), "x", 8)
	if spi.WebErrorCode(err) != spi.WebProviderError || !strings.Contains(err.Error(), "web_search_tool_result") {
		t.Fatalf("没有结果块必须报错: %v", err)
	}
}

// 非 2xx：错误里带状态码，且绝不回显凭据。
func TestDeepSeekSearchUpstreamErrorRedactsCredential(t *testing.T) {
	const secret = "leaky-secret-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid key `+secret+`"}}`)
	}))
	defer srv.Close()
	provider := &DeepSeek{BaseURL: srv.URL, APIKey: secret}
	_, err := provider.Search(context.Background(), "x", 8)
	if err == nil {
		t.Fatal("401 必须失败")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("错误里泄露了凭据: %v", err)
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("要说清状态码并清洗: %v", err)
	}
}

func TestDeepSeekUnavailableWithoutKey(t *testing.T) {
	provider := &DeepSeek{BaseURL: "https://api.deepseek.com/anthropic/v1"}
	if provider.Available() {
		t.Fatal("没有 key 不能算可用")
	}
	_, err := provider.Search(context.Background(), "x", 8)
	if spi.WebErrorCode(err) != spi.WebProviderCredentialMissing {
		t.Fatalf("缺凭据要有稳定码: %v", err)
	}
}

// 环境装配：rc.2 的两个 DeepSeek API provider 注册进来，基线默认 provider 真正生效，
// 而"只有 SearxNG"的部署仍然能用（不硬钉不可用的默认值）。
func TestNewWebProvidersFromEnv(t *testing.T) {
	env := map[string]string{
		"DEEPSEEK_API_KEY":      "k",
		"DEEPSEEK_SEARCH_MODEL": "deepseek-v4-flash",
	}
	getenv := func(name string) string { return env[name] }
	seam, err := NewWebProvidersFromEnv(getenv)
	if err != nil {
		t.Fatal(err)
	}
	ids := seam.SearchProviderIDs()
	if strings.Join(ids, ",") != "deepseek-official,deepseek-search-api,searxng" {
		t.Fatalf("注册的 provider 不对: %v", ids)
	}
	if got := strings.Join(seam.FetchProviderIDs(), ","); got != DeepSeekFetchAPIProviderID {
		t.Fatalf("抓取侧注册的 provider 不对: %q", got)
	}
	if !seam.SearchReady() {
		t.Fatal("有 key 时搜索应该就绪")
	}
	// rc.2 基线默认 provider（deepseek-search-api）可用时直接生效（cordis.patch.yml:487）。
	if seam.SearchPin() != BaseBundleSearchProviderID {
		t.Fatalf("有凭据时该钉 rc.2 基线默认 %q；实际 %q", BaseBundleSearchProviderID, seam.SearchPin())
	}
	selected, err := seam.SelectSearch()
	if err != nil || selected.ID() != DeepSeekSearchAPIProviderID {
		t.Fatalf("应选中 DeepSeek Search API: %v %v", err, selected)
	}

	// rc.2 基线默认可用 → 即使 SearxNG 也配了也不会 ambiguous（官方基础组合就是这么钉的）。
	env["DSHD_SEARCH_URL"] = "http://127.0.0.1:1/search?q={query}"
	seam, err = NewWebProvidersFromEnv(getenv)
	if err != nil {
		t.Fatal(err)
	}
	selected, err = seam.SelectSearch()
	if err != nil || selected.ID() != DeepSeekSearchAPIProviderID {
		t.Fatalf("基线默认优先于自动选择: %v %v", err, selected)
	}

	// 钉死 searxng → 选它。
	env["DSHD_WEB_SEARCH_PROVIDER"] = SearxNGProviderID
	seam, err = NewWebProvidersFromEnv(getenv)
	if err != nil {
		t.Fatal(err)
	}
	selected, err = seam.SelectSearch()
	if err != nil || selected.ID() != SearxNGProviderID {
		t.Fatalf("钉死的 provider 没生效: %v %v", err, selected)
	}

	// 没有 DeepSeek 凭据时不硬钉（否则"只用 SearxNG"的部署会退化成配置错误）。
	noKey := map[string]string{"DSHD_SEARCH_URL": "http://127.0.0.1:1/search?q={query}"}
	seam, err = NewWebProvidersFromEnv(func(name string) string { return noKey[name] })
	if err != nil {
		t.Fatal(err)
	}
	if seam.SearchPin() != "" {
		t.Fatalf("没有凭据时不该钉死默认 provider；实际 %q", seam.SearchPin())
	}
	selected, err = seam.SelectSearch()
	if err != nil || selected.ID() != SearxNGProviderID {
		t.Fatalf("没有 DeepSeek 凭据时该自动选中唯一的 SearxNG: %v %v", err, selected)
	}

	// 抓取侧：有 DeepSeek 凭据时钉 rc.2 基线默认 http（官方 cordis.patch.yml:488），
	// 否则会与 deepseek-fetch-api 撞成 WEB_PROVIDER_AMBIGUOUS。
	withKey := map[string]string{"DEEPSEEK_API_KEY": "k"}
	seam, err = NewWebProvidersFromEnv(func(name string) string { return withKey[name] })
	if err != nil {
		t.Fatal(err)
	}
	if seam.FetchPin() != BaseBundleFetchProviderID {
		t.Fatalf("有凭据时抓取侧该钉 %q；实际 %q", BaseBundleFetchProviderID, seam.FetchPin())
	}
	if _, err := seam.SelectFetch(); spi.WebErrorCode(err) != spi.WebProviderConfiguredMissing {
		// http provider 由 modules/tool/webfetch 注册；本函数只造搜索侧 + Search/Fetch API。
		// 组合里它一定在（见 websearch.HostProviders 与 cmd/dshd 的模块表）。
		t.Fatalf("只造搜索侧时 http 还没注册，该报 %s；实际 %q",
			spi.WebProviderConfiguredMissing, spi.WebErrorCode(err))
	}
}
