package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubAdapter 是测试用的第三条协议，用来证明"provider 适配器可注册"不是一个
// 只有内置两条才成立的封闭集合。
type stubAdapter struct {
	api    string
	pinned string
	usage  []string
}

func (s stubAdapter) API() string              { return s.api }
func (s stubAdapter) MaxTokensField() string   { return s.pinned }
func (s stubAdapter) RequiredFields() []string { return []string{"model", "messages", "stream"} }
func (s stubAdapter) OptionalFields() []string { return []string{"max_tokens"} }
func (s stubAdapter) UsageExtensions() []string {
	return s.usage
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestProviderAdapterRegistryLifecycle(t *testing.T) {
	t.Cleanup(resetProviderAdapters)

	apis := ProviderAdapterAPIs()
	if !containsString(apis, ProviderAPIOpenAICompletions) || !containsString(apis, ProviderAPIDeepSeekCompletions) {
		t.Fatalf("内置 adapter 不在注册表里: %v", apis)
	}
	if _, err := LookupProviderAdapter(""); err != nil {
		t.Fatalf("空 api 必须回落到默认协议: %v", err)
	}

	custom := stubAdapter{api: "fixture-wire", pinned: "max_tokens"}
	if err := RegisterProviderAdapter(custom); err != nil {
		t.Fatalf("注册自定义 adapter 失败: %v", err)
	}
	got, err := LookupProviderAdapter("fixture-wire")
	if err != nil || got.API() != "fixture-wire" {
		t.Fatalf("查不到刚注册的 adapter: %v %+v", err, got)
	}
	if err := RegisterProviderAdapter(custom); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复注册必须失败: %v", err)
	}
	if err := RegisterProviderAdapter(nil); err == nil {
		t.Fatal("nil adapter 必须失败")
	}
	if err := RegisterProviderAdapter(stubAdapter{api: "  "}); err == nil {
		t.Fatal("空 api 必须失败")
	}
	if _, err := LookupProviderAdapter("no-such-wire"); err == nil || !strings.Contains(err.Error(), "已注册") {
		t.Fatalf("未知 api 必须报错并列出已注册项: %v", err)
	}

	resetProviderAdapters()
	if _, err := LookupProviderAdapter("fixture-wire"); err == nil {
		t.Fatal("reset 之后自定义 adapter 不该还在")
	}
}

func TestCatalogAcceptsRegisteredProviderAPI(t *testing.T) {
	t.Cleanup(resetProviderAdapters)
	if err := RegisterProviderAdapter(stubAdapter{api: "fixture-wire"}); err != nil {
		t.Fatal(err)
	}
	store := OpenModelCatalog(t.TempDir())
	t.Setenv("FIXTURE_PROVIDER_KEY", "")
	credential := "fixture-secret"
	if _, err := store.Upsert(json.RawMessage(`{"id":"fixture-model","api":"fixture-wire","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"FIXTURE_PROVIDER_KEY"}`), &credential, catalogRevision(0)); err != nil {
		t.Fatalf("注册过协议后 catalog 仍拒绝: %v", err)
	}
	provider, err := store.ResolveProvider("fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	if provider.API != "fixture-wire" || provider.Client().API != "fixture-wire" {
		t.Fatalf("provider 没有把协议带下去: %+v client=%q", provider, provider.Client().API)
	}

	if _, err := store.Upsert(json.RawMessage(`{"id":"unknown-model","api":"no-such-wire","baseURL":"http://127.0.0.1:9/v1"}`), nil, catalogRevision(1)); ModelCatalogErrorCode(err) != "invalid_field" {
		t.Fatalf("未注册的 api 必须被 catalog 拒绝: %v code=%s", err, ModelCatalogErrorCode(err))
	}
}

// captureBodies 起一个回放 SSE 的端点，并把每次请求体原样留下。
type captureBodies struct {
	mu     sync.Mutex
	bodies []string
	status int
	body   string
}

func (c *captureBodies) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.bodies = append(c.bodies, string(raw))
	status, body := c.status, c.body
	c.mu.Unlock()
	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
		io.WriteString(w, body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, `data: {"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}`+"\n\n")
	io.WriteString(w, "data: [DONE]\n\n")
}

func (c *captureBodies) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

func newCaptureServer(t *testing.T) (*httptest.Server, *captureBodies) {
	t.Helper()
	recorder := &captureBodies{}
	srv := httptest.NewServer(recorder)
	t.Cleanup(srv.Close)
	return srv, recorder
}

// DeepSeek 只认 max_tokens：协商里"被 400 点名换拼写"这条对它必须彻底关掉，
// 否则每轮都要多打一次必失败的请求。
func TestDeepSeekAdapterPinsMaxTokensSpelling(t *testing.T) {
	srv, recorder := newCaptureServer(t)
	client := NewWithAPI(srv.URL, "k", ProviderAPIDeepSeekCompletions)
	if _, err := client.Stream(context.Background(), Request{
		Model: "deepseek-v4", MaxTokens: 42,
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	bodies := recorder.snapshot()
	if len(bodies) != 1 {
		t.Fatalf("应该只发一次请求，实际 %d 次", len(bodies))
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal([]byte(bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent["max_tokens"]; !ok {
		t.Fatalf("DeepSeek 请求必须带 max_tokens: %s", bodies[0])
	}
	if _, ok := sent["max_completion_tokens"]; ok {
		t.Fatalf("DeepSeek 请求不该出现 max_completion_tokens: %s", bodies[0])
	}
}

// 端点明确拒绝 max_tokens 时，钉死拼写的协议仍然只发一次：
// 换拼写对 DeepSeek 是错的，重发只是浪费一次请求。
func TestDeepSeekAdapterNeverRenamesMaxTokens(t *testing.T) {
	recorder := &captureBodies{status: http.StatusBadRequest, body: `{"error":{"message":"unknown parameter: max_tokens"}}`}
	srv := httptest.NewServer(recorder)
	t.Cleanup(srv.Close)
	client := NewWithAPI(srv.URL, "k", ProviderAPIDeepSeekCompletions)
	_, err := client.Stream(context.Background(), Request{Model: "deepseek-v4", MaxTokens: 42, Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err == nil {
		t.Fatal("400 必须失败")
	}
	if got := len(recorder.snapshot()); got != 1 {
		t.Fatalf("钉死的协议不该换拼写重发，实际发了 %d 次", got)
	}
}

// provider 扩展字段按 api 归属：DeepSeek 的字段不进 OpenAI 请求，且默认形状
// 仍与历史 marshal 逐字节一致。
func TestRequestExtensionIsProviderScopedAndShapePreserving(t *testing.T) {
	t.Cleanup(resetRequestExtensions)
	srv, recorder := newCaptureServer(t)
	if err := RegisterRequestExtension(RequestExtension{
		Field: "dsh_session_log",
		API:   ProviderAPIDeepSeekCompletions,
		Prepare: func(context.Context, *Request) (json.RawMessage, func() error, error) {
			return json.RawMessage(`{"seq":7}`), nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	req := Request{Model: "m", MaxTokens: 3, Messages: []Message{{Role: "user", Content: "hi"}}}
	deep := NewWithAPI(srv.URL, "k", ProviderAPIDeepSeekCompletions)
	if _, err := deep.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	open := NewWithAPI(srv.URL, "k", ProviderAPIOpenAICompletions)
	if _, err := open.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	bodies := recorder.snapshot()
	if len(bodies) != 2 {
		t.Fatalf("请求数 = %d", len(bodies))
	}
	if !strings.Contains(bodies[0], `"dsh_session_log":{"seq":7}`) {
		t.Fatalf("DeepSeek 请求缺扩展字段: %s", bodies[0])
	}
	if strings.Contains(bodies[1], "dsh_session_log") {
		t.Fatalf("OpenAI 请求不该带 DeepSeek 的扩展字段: %s", bodies[1])
	}
	// OpenAI 形状逐字节不变：与历史 marshal（再加 include_usage 默认）一致。
	expectedRequest := req
	expectedRequest.Stream = true
	expectedRequest.StreamOptions = &StreamOptions{IncludeUsage: true}
	want, err := marshalWireRequest(expectedRequest, defaultWireProfile())
	if err != nil {
		t.Fatal(err)
	}
	if bodies[1] != string(want) {
		t.Fatalf("默认形状漂移:\n got=%s\nwant=%s", bodies[1], want)
	}
}

// prepare 失败只丢那一个字段（请求照发），4xx 不跑 accept，2xx 才跑一次。
func TestRequestExtensionOmitAndAcceptLifecycle(t *testing.T) {
	t.Cleanup(resetRequestExtensions)
	accepts := 0
	var extensionErrors []error
	if err := RegisterRequestExtension(RequestExtension{
		Field: "broken_field",
		API:   ProviderAPIDeepSeekCompletions,
		Prepare: func(context.Context, *Request) (json.RawMessage, func() error, error) {
			return nil, nil, fmt.Errorf("boom")
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterRequestExtension(RequestExtension{
		Field: "dsh_plugin_packages",
		API:   ProviderAPIDeepSeekCompletions,
		Prepare: func(context.Context, *Request) (json.RawMessage, func() error, error) {
			return json.RawMessage(`["pkg"]`), func() error { accepts++; return nil }, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	reject := &captureBodies{status: http.StatusBadRequest, body: `{"error":{"message":"nope"}}`}
	rejectSrv := httptest.NewServer(reject)
	t.Cleanup(rejectSrv.Close)
	deepReject := NewWithAPI(rejectSrv.URL, "k", ProviderAPIDeepSeekCompletions)
	deepReject.OnExtensionError = func(err error) { extensionErrors = append(extensionErrors, err) }
	if _, err := deepReject.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, nil); err == nil {
		t.Fatal("400 必须失败")
	}
	if accepts != 0 {
		t.Fatalf("非 2xx 不该跑 accept（跑了 %d 次）", accepts)
	}
	if len(extensionErrors) == 0 || !strings.Contains(extensionErrors[0].Error(), "broken_field") {
		t.Fatalf("prepare 失败要被报告出来: %v", extensionErrors)
	}
	rejectBodies := reject.snapshot()
	if len(rejectBodies) != 1 {
		t.Fatalf("请求数 = %d", len(rejectBodies))
	}
	if strings.Contains(rejectBodies[0], "broken_field") {
		t.Fatalf("prepare 失败的字段不该被发出去: %s", rejectBodies[0])
	}
	if !strings.Contains(rejectBodies[0], `"dsh_plugin_packages":["pkg"]`) {
		t.Fatalf("其余扩展字段要照发: %s", rejectBodies[0])
	}

	srv, recorder := newCaptureServer(t)
	deepOK := NewWithAPI(srv.URL, "k", ProviderAPIDeepSeekCompletions)
	if _, err := deepOK.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if accepts != 1 {
		t.Fatalf("2xx 之后 accept 必须恰好跑一次，实际 %d", accepts)
	}
	if len(recorder.snapshot()) != 1 {
		t.Fatal("2xx 请求数不对")
	}
}

// 与 base 请求字段撞名必须在 HTTP 之前失败（官方 REQUEST_EXTENSION 语义）。
func TestRequestExtensionCollisionFailsBeforeHTTP(t *testing.T) {
	t.Cleanup(resetRequestExtensions)
	srv, recorder := newCaptureServer(t)
	if err := RegisterRequestExtension(RequestExtension{
		Field: "model",
		API:   ProviderAPIDeepSeekCompletions,
		Prepare: func(context.Context, *Request) (json.RawMessage, func() error, error) {
			return json.RawMessage(`"evil"`), nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	client := NewWithAPI(srv.URL, "k", ProviderAPIDeepSeekCompletions)
	_, err := client.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "撞名") {
		t.Fatalf("撞名必须 fail-loud: %v", err)
	}
	if got := len(recorder.snapshot()); got != 0 {
		t.Fatalf("撞名的请求不该发出去（发了 %d 次）", got)
	}
}

// usage 扩展字段按协议归属：DeepSeek 原生缓存计数不属于 OpenAI 协议。
func TestUsageExtensionOwnershipByAdapter(t *testing.T) {
	openai, err := LookupProviderAdapter(ProviderAPIOpenAICompletions)
	if err != nil {
		t.Fatal(err)
	}
	if containsString(openai.UsageExtensions(), "prompt_cache_hit_tokens") {
		t.Fatal("OpenAI 协议不该把 DeepSeek 的 prompt_cache_hit_tokens 算成自己的")
	}
	if !containsString(openai.UsageExtensions(), "prompt_tokens_details.cached_tokens") {
		t.Fatal("OpenAI 协议的 cached_tokens 归属丢了")
	}
	deepseek, err := LookupProviderAdapter(ProviderAPIDeepSeekCompletions)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"prompt_cache_hit_tokens", "prompt_cache_miss_tokens", "prompt_tokens_details.cache_write_tokens"} {
		if !containsString(deepseek.UsageExtensions(), field) {
			t.Fatalf("DeepSeek 协议缺 usage 扩展归属 %q", field)
		}
	}
}

// 真机：本机模型网关会回 usage 扩展（prompt_tokens_details / completion_tokens_details）。
// 默认跳过；真机跑：
//
//	DSHD_LIVE_LLM=1 go test ./internal/llm -run TestLiveUsageExtensions -v
func TestLiveUsageExtensions(t *testing.T) {
	if os.Getenv("DSHD_LIVE_LLM") != "1" {
		t.Skip("设置 DSHD_LIVE_LLM=1 才跑真机")
	}
	base := os.Getenv("DSHD_LLM_BASE")
	if base == "" {
		base = "http://127.0.0.1:3425/v1"
	}
	model := os.Getenv("DSHD_LLM_MODEL")
	if model == "" {
		model = "group/deepseek-v4.1-flash-1017"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := New(base, os.Getenv("DSHD_LLM_KEY"))
	resp, err := client.Stream(ctx, Request{
		Model: model, MaxTokens: 8,
		Messages: []Message{{Role: "user", Content: "只回一个字：好"}},
	}, nil)
	if err != nil {
		t.Fatalf("真机调用失败: %v", err)
	}
	raw, _ := json.Marshal(resp.Usage)
	t.Logf("真实 usage 形状: %s", raw)
	if len(resp.Usage.Extensions) == 0 {
		t.Fatal("真机 usage 没有任何扩展计数：扩展字段又丢了")
	}
}

// DeepSeek 的 required / 默认 / 可选字段面必须与**实际请求**一致：
// 三条 required 永远在；可选字段只在调用方给了才出现；max_tokens 是必填拼写。
func TestDeepSeekAdapterRequiredAndOptionalFields(t *testing.T) {
	adapter, err := LookupProviderAdapter(ProviderAPIDeepSeekCompletions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(adapter.RequiredFields(), ",") != "model,messages,stream" {
		t.Fatalf("required 字段声明不对: %v", adapter.RequiredFields())
	}
	optional := adapter.OptionalFields()
	if !containsString(optional, "max_tokens") || containsString(optional, "max_completion_tokens") {
		t.Fatalf("DeepSeek 的 max_tokens 拼写声明不对: %v", optional)
	}
	openai, err := LookupProviderAdapter(ProviderAPIOpenAICompletions)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(openai.OptionalFields(), "max_completion_tokens") {
		t.Fatal("OpenAI 协议应允许 max_completion_tokens")
	}

	srv, recorder := newCaptureServer(t)
	client := NewWithAPI(srv.URL, "k", ProviderAPIDeepSeekCompletions)
	if _, err := client.Stream(context.Background(), Request{
		Model: "deepseek-v4", Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(recorder.snapshot()[0]), &body); err != nil {
		t.Fatal(err)
	}
	for _, required := range adapter.RequiredFields() {
		if _, ok := body[required]; !ok {
			t.Fatalf("required 字段 %q 没发出去: %s", required, recorder.snapshot()[0])
		}
	}
	for _, optional := range []string{"tools", "max_tokens", "max_completion_tokens", "reasoning_effort"} {
		if _, ok := body[optional]; ok {
			t.Fatalf("调用方没给的可选字段 %q 不该出现: %s", optional, recorder.snapshot()[0])
		}
	}
	// 默认 stream_options（工具层默认打开 include_usage）仍在。
	if _, ok := body["stream_options"]; !ok {
		t.Fatalf("stream_options 默认没发出去: %s", recorder.snapshot()[0])
	}

	srv2, recorder2 := newCaptureServer(t)
	client2 := NewWithAPI(srv2.URL, "k", ProviderAPIDeepSeekCompletions)
	if _, err := client2.Stream(context.Background(), Request{
		Model: "deepseek-v4", MaxTokens: 64, Reasoning: "high",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	var body2 map[string]json.RawMessage
	if err := json.Unmarshal([]byte(recorder2.snapshot()[0]), &body2); err != nil {
		t.Fatal(err)
	}
	if _, ok := body2["max_tokens"]; !ok {
		t.Fatalf("给了 max_tokens 就要发: %s", recorder2.snapshot()[0])
	}
	if _, ok := body2["reasoning_effort"]; !ok {
		t.Fatalf("给了 reasoning_effort 就要发（它在这条协议的可选字段里）: %s", recorder2.snapshot()[0])
	}
}

func TestUsageExtensionAccessor(t *testing.T) {
	var usage Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":1,"prompt_cache_hit_tokens":9,"nested":{"a":2}}`), &usage); err != nil {
		t.Fatal(err)
	}
	if value, ok := usage.Extension("prompt_cache_hit_tokens"); !ok || value != 9 {
		t.Fatalf("扩展取值不对: %d %v", value, ok)
	}
	if value, ok := usage.Extension("nested.a"); !ok || value != 2 {
		t.Fatalf("嵌套扩展取值不对: %d %v", value, ok)
	}
	if _, ok := usage.Extension("missing"); ok {
		t.Fatal("不存在的扩展不该命中")
	}
}
