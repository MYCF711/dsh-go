package llm

// `llm.discoverModels` 的**协议分支**测试（官方 dsh-llm-pi-ai/src/discovery.ts）。
//
// 钉住的东西（每条都能指向官方行）：
//
//   - 空 `api` = 没声明 → `openai-completions`（discovery.ts:293）；
//   - 集合外协议 → ErrDiscoverProtocolUnsupported（官方 DISCOVERY_UNSUPPORTED，
//     :302-307），且**不发请求**；
//   - 列表端点三种拼法（:117-121 `listingUrl`）：OpenAI 系 `{base}/models`、
//     Gemini `{base}/models?pageSize=1000`、Anthropic `{root}/v1/models?limit=1000`
//     （`root` 掉一层尾部 `/v1`）；
//   - 鉴权头三种（:325-335）：Bearer / `x-goog-api-key` / `x-api-key`+`anthropic-version`；
//   - 响应两种（:187-232 / :245-273）：OpenAI 系 `data` 数组或 `models` 对象；
//     Gemini `models` 数组，且没有 `generateContent` 的条目跳过、`models/` 前缀去掉；
//   - 形状不对响亮报错，不是空列表。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// discoveryProbe 是一次被端点收到的请求的快照。
type discoveryProbe struct {
	path    string
	query   url.Values
	headers http.Header
	hits    int
}

func newDiscoveryProbeServer(t *testing.T, body string) (*httptest.Server, *discoveryProbe) {
	t.Helper()
	probe := &discoveryProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe.hits++
		probe.path = r.URL.Path
		probe.query = r.URL.Query()
		probe.headers = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, probe
}

// noDiscoveryCache 关掉列表缓存（默认 10 分钟），否则同一端点第二次探测不发网络。
func noDiscoveryCache(t *testing.T) {
	t.Helper()
	t.Setenv(modelsMaxAgeEnv, "0")
}

// TestNormalizeDiscoveryProtocol：官方 LISTABLE_PROTOCOLS 逐条认，集合外拒。
func TestNormalizeDiscoveryProtocol(t *testing.T) {
	for _, api := range []string{"", "  ", ProviderAPIOpenAICompletions, ProviderAPIOpenAIResponses,
		ProviderAPIAnthropicMessages, ProviderAPIGoogleGenerativeAI} {
		t.Run("accept/"+api, func(t *testing.T) {
			got, err := NormalizeDiscoveryProtocol(api)
			if err != nil {
				t.Fatalf("NormalizeDiscoveryProtocol(%q): %v", api, err)
			}
			if api == "" || strings.TrimSpace(api) == "" {
				if got != ProviderAPIOpenAICompletions {
					t.Fatalf("空 api 应默认 %s，得到 %s", ProviderAPIOpenAICompletions, got)
				}
				return
			}
			if got != api {
				t.Fatalf("协议 %q 被改成 %q", api, got)
			}
		})
	}
	// Azure 是官方**明确排除**的：它用 `api-key` 头且要 `api-version` 查询，
	// 猜错会把鉴权失败报成"这个 provider 没有模型"（discovery.ts:44-49）。
	for _, api := range []string{"azure-openai", "codex-oauth", "openai-codex"} {
		t.Run("reject/"+api, func(t *testing.T) {
			if _, err := NormalizeDiscoveryProtocol(api); !errors.Is(err, ErrDiscoverProtocolUnsupported) {
				t.Fatalf("协议 %q 应被判不支持，得到 %v", api, err)
			}
		})
	}
}

// TestDiscoveryListingEndpoint：官方 listingUrl 的三种拼法。
func TestDiscoveryListingEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		base     string
		protocol string
		want     string
	}{
		{"openai-completions/v1", "https://h/v1", ProviderAPIOpenAICompletions, "https://h/v1/models"},
		{"openai-completions/root", "https://h", ProviderAPIOpenAICompletions, "https://h/models"},
		{"openai-responses/同 OpenAI", "https://h/v1/", ProviderAPIOpenAIResponses, "https://h/v1/models"},
		{"anthropic/root", "https://api.anthropic.com", ProviderAPIAnthropicMessages,
			"https://api.anthropic.com/v1/models?limit=1000"},
		{"anthropic/带 v1 的两种拼法都要认", "https://api.anthropic.com/v1", ProviderAPIAnthropicMessages,
			"https://api.anthropic.com/v1/models?limit=1000"},
		{"anthropic/尾部斜杠", "https://api.anthropic.com/v1/", ProviderAPIAnthropicMessages,
			"https://api.anthropic.com/v1/models?limit=1000"},
		{"gemini", "https://generativelanguage.googleapis.com/v1beta", ProviderAPIGoogleGenerativeAI,
			"https://generativelanguage.googleapis.com/v1beta/models?pageSize=1000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := discoveryListingEndpoint(tc.base, tc.protocol)
			if err != nil {
				t.Fatalf("discoveryListingEndpoint(%q,%q): %v", tc.base, tc.protocol, err)
			}
			if got != tc.want {
				t.Fatalf("端点 = %q，期望 %q", got, tc.want)
			}
		})
	}
	for _, base := range []string{"", "file:///etc/passwd", "ftp://h", "not a url"} {
		if _, err := discoveryListingEndpoint(base, ProviderAPIAnthropicMessages); err == nil {
			t.Fatalf("baseURL %q 被接受了", base)
		}
	}
}

// TestDiscoverAnthropicMessages：Anthropic 的端点、两个头、以及 `data` 数组解析。
func TestDiscoverAnthropicMessages(t *testing.T) {
	noDiscoveryCache(t)
	srv, probe := newDiscoveryProbeServer(t, `{"data":[
      {"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5","context_window":200000,"max_tokens":8192},
      {"id":"claude-haiku-4-5"}
    ]}`)
	models, err := New(srv.URL, "sk-ant-one-shot").DiscoverModelsProtocol(context.Background(), ProviderAPIAnthropicMessages)
	if err != nil {
		t.Fatalf("DiscoverModelsProtocol(anthropic): %v", err)
	}
	if probe.path != "/v1/models" {
		t.Fatalf("请求路径 %q，期望 /v1/models（官方 listingUrl）", probe.path)
	}
	if got := probe.query.Get("limit"); got != "1000" {
		t.Fatalf("limit = %q，期望 1000（官方 ANTHROPIC_MODEL_LIMIT）", got)
	}
	if got := probe.headers.Get("x-api-key"); got != "sk-ant-one-shot" {
		t.Fatalf("x-api-key = %q（Anthropic 不用 Bearer）", got)
	}
	if got := probe.headers.Get("anthropic-version"); got != anthropicVersion {
		t.Fatalf("anthropic-version = %q，期望 %q", got, anthropicVersion)
	}
	if got := probe.headers.Get("Authorization"); got != "" {
		t.Fatalf("Anthropic 请求不该带 Authorization，得到 %q", got)
	}
	if len(models) != 2 {
		t.Fatalf("条数 %d，期望 2", len(models))
	}
	if models[0].ID != "claude-sonnet-4-5" || models[0].Name != "Claude Sonnet 4.5" ||
		models[0].ContextWindow != 200000 || models[0].MaxTokens != 8192 {
		t.Fatalf("第一条映射错：%+v", models[0])
	}
	// 没有元数据的条目：名字留空（"absent means unknown"），不拿 id 冒充 name。
	if models[1].Name != "" || models[1].ContextWindow != 0 || models[1].MaxTokens != 0 {
		t.Fatalf("缺元数据的条目被编了值：%+v", models[1])
	}
}

// TestDiscoverGoogleGenerativeAI：Gemini 的端点 + `x-goog-api-key` + models 数组。
func TestDiscoverGoogleGenerativeAI(t *testing.T) {
	noDiscoveryCache(t)
	srv, probe := newDiscoveryProbeServer(t, `{"models":[
      {"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro",
       "inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent"]},
      {"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]},
      {"name":"models/gemini-2.5-flash"}
    ]}`)
	models, err := New(srv.URL+"/v1beta", "sk-goog").DiscoverModelsProtocol(context.Background(), ProviderAPIGoogleGenerativeAI)
	if err != nil {
		t.Fatalf("DiscoverModelsProtocol(gemini): %v", err)
	}
	if probe.path != "/v1beta/models" {
		t.Fatalf("请求路径 %q，期望 /v1beta/models", probe.path)
	}
	if got := probe.query.Get("pageSize"); got != "1000" {
		t.Fatalf("pageSize = %q，期望 1000", got)
	}
	if got := probe.headers.Get("x-goog-api-key"); got != "sk-goog" {
		t.Fatalf("x-goog-api-key = %q", got)
	}
	if got := probe.headers.Get("Authorization"); got != "" {
		t.Fatalf("Gemini 请求不该带 Authorization，得到 %q", got)
	}
	// embedding 那条没有 generateContent，必须跳过；`models/` 前缀必须去掉。
	if len(models) != 2 {
		t.Fatalf("条数 %d，期望 2（embedding 应被跳过）：%+v", len(models), models)
	}
	if models[0].ID != "gemini-2.5-pro" || models[0].Name != "Gemini 2.5 Pro" ||
		models[0].ContextWindow != 1048576 || models[0].MaxTokens != 65536 {
		t.Fatalf("第一条映射错：%+v", models[0])
	}
	// 没有 displayName 时 name 留空（= 不知道），不拿 id 冒充（见 parseGeminiListing
	// 的注释：官方 schema 里 name 是 optional，客户端必须能处理它缺席）。
	if models[1].ID != "gemini-2.5-flash" || models[1].Name != "" {
		t.Fatalf("第二条的 name 应留空：%+v", models[1])
	}
}

// TestDiscoverOpenAIResponsesUsesOpenAIListing：官方 `listingUrl` 对 responses
// 没有分支——它就是 `{base}/models`，鉴权仍是 Bearer。
func TestDiscoverOpenAIResponsesUsesOpenAIListing(t *testing.T) {
	noDiscoveryCache(t)
	srv, probe := newDiscoveryProbeServer(t, `{"data":[{"id":"gpt-5","name":"GPT-5"}]}`)
	models, err := New(srv.URL+"/v1", "sk-openai").DiscoverModelsProtocol(context.Background(), ProviderAPIOpenAIResponses)
	if err != nil {
		t.Fatalf("DiscoverModelsProtocol(openai-responses): %v", err)
	}
	if probe.path != "/v1/models" || probe.query.Get("limit") != "" || probe.query.Get("pageSize") != "" {
		t.Fatalf("responses 的端点应是 {base}/models 且不带分页参数：path=%q query=%v", probe.path, probe.query)
	}
	if got := probe.headers.Get("Authorization"); got != "Bearer sk-openai" {
		t.Fatalf("Authorization = %q，期望 Bearer", got)
	}
	if len(models) != 1 || models[0].ID != "gpt-5" {
		t.Fatalf("解析错：%+v", models)
	}
}

// TestDiscoverProtocolUnsupportedSendsNothing：集合外协议必须在**发请求之前**拒。
func TestDiscoverProtocolUnsupportedSendsNothing(t *testing.T) {
	noDiscoveryCache(t)
	srv, probe := newDiscoveryProbeServer(t, `{"data":[]}`)
	_, err := New(srv.URL+"/v1", "sk").DiscoverModelsProtocol(context.Background(), "azure-openai")
	if !errors.Is(err, ErrDiscoverProtocolUnsupported) {
		t.Fatalf("azure 应判不支持，得到 %v", err)
	}
	if probe.hits != 0 {
		t.Fatalf("不支持的协议发了 %d 次请求（必须零请求）", probe.hits)
	}
}

// TestDiscoverOpenAIModelsMapShape：网关扩展的 `models` 对象形态（官方 :198-211）。
func TestDiscoverOpenAIModelsMapShape(t *testing.T) {
	noDiscoveryCache(t)
	// 混进来的目录元数据（`object: "list"` 是字符串）必须忽略，不能让整页失败。
	srv, _ := newDiscoveryProbeServer(t, `{"models":{
      "object":"list",
      "alias-1":{"id":"canonical-1","context_length":32768},
      "":{"id":"from-nested-id"}
    }}`)
	models, err := New(srv.URL+"/v1", "").DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels(models map): %v", err)
	}
	// 键是端点认的 id；键为空时才用嵌套 id 兜底。
	byID := map[string]DiscoveredModel{}
	for _, model := range models {
		byID[model.ID] = model
	}
	if _, ok := byID["alias-1"]; !ok {
		t.Fatalf("键 alias-1 没有成为 id：%+v", models)
	}
	if byID["alias-1"].ContextWindow != 32768 {
		t.Fatalf("alias-1 的 contextWindow 没解析：%+v", byID["alias-1"])
	}
	if _, ok := byID["from-nested-id"]; !ok {
		t.Fatalf("空键应回落到嵌套 id：%+v", models)
	}
}

// TestDiscoverGeminiMissingModelsArrayIsLoud：形状不对是错误，不是"没有模型"。
func TestDiscoverGeminiMissingModelsArrayIsLoud(t *testing.T) {
	noDiscoveryCache(t)
	srv, _ := newDiscoveryProbeServer(t, `{"object":"list","data":[]}`)
	_, err := New(srv.URL, "").DiscoverModelsProtocol(context.Background(), ProviderAPIGoogleGenerativeAI)
	if err == nil {
		t.Fatalf("缺 models 数组被当成空列表")
	}
	if !strings.Contains(err.Error(), "models") {
		t.Fatalf("错误没说清缺 models：%v", err)
	}
}

// TestDiscoverGeminiEmptyListIsEmptySlice：真·空列表要能区分出来。
func TestDiscoverGeminiEmptyListIsEmptySlice(t *testing.T) {
	noDiscoveryCache(t)
	srv, _ := newDiscoveryProbeServer(t, `{"models":[]}`)
	models, err := New(srv.URL, "").DiscoverModelsProtocol(context.Background(), ProviderAPIGoogleGenerativeAI)
	if err != nil {
		t.Fatalf("DiscoverModelsProtocol(gemini): %v", err)
	}
	if models == nil || len(models) != 0 {
		t.Fatalf("空列表应是长度 0 的非 nil 切片，实际 %#v", models)
	}
}
