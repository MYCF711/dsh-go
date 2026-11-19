package main

// `llm.discoverModels` 的**协议分支**在宿主接线层的测试。
//
// 为什么单独写一份：`internal/llm` 的测试证明协议分支本身对，但
// `cmd/dshd` 的 `discoverModels` 是**唯一**把这些入参（baseURL / api / apiKey）
// 接到发现器上的地方，而 `request.api` 曾被读进来就丢掉——草稿声明
// `anthropic-messages` 时会被按 OpenAI 的形状去问。这里从接线层钉住：
//
//   - api 真的被透传（端点/鉴权头随之变）；
//   - 集合外协议回官方 `DISCOVERY_UNSUPPORTED` 语义（400 + 前缀），且**不发请求**；
//   - 结果字段名与官方数组形状一致（id/name/contextWindow/maxTokens/inputModalities）。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

// discoveryWire 记录一次被端点收到的请求。
type discoveryWire struct {
	path     string
	query    string
	header   http.Header
	requests int
}

func newDiscoveryWire(t *testing.T, body string) (*httptest.Server, *discoveryWire) {
	t.Helper()
	wire := &discoveryWire{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire.requests++
		wire.path = r.URL.Path
		wire.query = r.URL.RawQuery
		wire.header = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, wire
}

func TestDiscoverModelsPassesProtocolToEndpoint(t *testing.T) {
	t.Setenv("DSHD_LLM_MODELS_MAX_AGE_MS", "0")
	srv, wire := newDiscoveryWire(t, `{"data":[{"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5","context_window":200000}]}`)
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	models, err := api.LLMDiscoverModels(context.Background(), "llm", transport.OfficialModelDiscoveryRequest{
		BaseURL: srv.URL,
		API:     "anthropic-messages",
		APIKey:  "sk-ant-once",
	})
	if err != nil {
		t.Fatalf("llm.discoverModels(anthropic): %v", err)
	}
	// 官方 listingUrl：Anthropic 走 `<root>/v1/models?limit=1000`，不是 `<base>/models`。
	if wire.path != "/v1/models" || wire.query != "limit=1000" {
		t.Fatalf("端点 = %s?%s，期望 /v1/models?limit=1000（request.api 没透传？）", wire.path, wire.query)
	}
	if got := wire.header.Get("x-api-key"); got != "sk-ant-once" {
		t.Fatalf("x-api-key = %q（Anthropic 用 x-api-key，不是 Bearer）", got)
	}
	if got := wire.header.Get("anthropic-version"); got == "" {
		t.Fatalf("缺 anthropic-version 头")
	}
	if len(models) != 1 {
		t.Fatalf("条数 %d，期望 1", len(models))
	}
	if models[0].ID != "claude-sonnet-4-5" || models[0].Name != "Claude Sonnet 4.5" ||
		models[0].ContextWindow != 200000 {
		t.Fatalf("结果映射错（字段名必须与官方 ModelDiscovery 逐字一致）：%+v", models[0])
	}
}

func TestDiscoverModelsUnsupportedProtocolIsStructuredBadInput(t *testing.T) {
	t.Setenv("DSHD_LLM_MODELS_MAX_AGE_MS", "0")
	srv, wire := newDiscoveryWire(t, `{"data":[]}`)
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	_, err := api.LLMDiscoverModels(context.Background(), "llm", transport.OfficialModelDiscoveryRequest{
		BaseURL: srv.URL,
		API:     "azure-openai",
	})
	if err == nil {
		t.Fatalf("azure-openai 被当成可探测协议")
	}
	if !errors.Is(err, transport.ErrOfficialBadInput) {
		t.Fatalf("应回 400 形状（客户端改输入就能过），得到 %v", err)
	}
	if !strings.Contains(err.Error(), "DISCOVERY_UNSUPPORTED") {
		t.Fatalf("message 必须带官方码 DISCOVERY_UNSUPPORTED：%v", err)
	}
	if wire.requests != 0 {
		t.Fatalf("不支持的协议发了 %d 次请求（必须零请求）", wire.requests)
	}
}

func TestDiscoverModelsGoogleProtocolThroughHost(t *testing.T) {
	t.Setenv("DSHD_LLM_MODELS_MAX_AGE_MS", "0")
	srv, wire := newDiscoveryWire(t, `{"models":[
      {"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","inputTokenLimit":1048576,"outputTokenLimit":65536},
      {"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]}
    ]}`)
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	models, err := api.LLMDiscoverModels(context.Background(), "llm", transport.OfficialModelDiscoveryRequest{
		BaseURL: srv.URL,
		API:     "google-generative-ai",
		APIKey:  "gk-once",
	})
	if err != nil {
		t.Fatalf("llm.discoverModels(gemini): %v", err)
	}
	if wire.path != "/models" || wire.query != "pageSize=1000" {
		t.Fatalf("端点 = %s?%s，期望 /models?pageSize=1000", wire.path, wire.query)
	}
	if got := wire.header.Get("x-goog-api-key"); got != "gk-once" {
		t.Fatalf("x-goog-api-key = %q", got)
	}
	if len(models) != 1 || models[0].ID != "gemini-2.5-pro" {
		t.Fatalf("embedding 应被跳过、models/ 前缀应去掉：%+v", models)
	}
}

func TestDiscoverModelsMissingBaseURLIsStructured(t *testing.T) {
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	// 没有 baseURL、也没有 provider：fail-loud，不拿进程默认端点顶上。
	if _, err := api.LLMDiscoverModels(context.Background(), "llm", transport.OfficialModelDiscoveryRequest{}); !errors.Is(err, transport.ErrOfficialBadInput) {
		t.Fatalf("缺 baseURL 应回 400 形状，得到 %v", err)
	}
}
