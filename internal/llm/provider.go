package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// 这一层是官方 0.2.2 两个包的宿主形状：
//
//   - `dsh-llm` 的 provider 适配器（一个 `api` = 一条 wire 协议 + 该协议的扩展字段面）；
//   - `dsh-deepseek-llm-api-extensions` 的"provider 专属顶层请求字段"注册表：
//     谁拥有这个字段、请求期怎么 prepare、2xx 后怎么 accept，都在这里，
//     而**不改** base adapter 的序列化。
//
// 为什么需要它：`Request` 是封闭结构体，官方/第三方 provider 的扩展字段
// （如 DeepSeek 的 `dsh_session_log`、pi-ai 的 `compat`）以前根本没有落点，
// 只能被静默丢掉；而 `api` 只认 `openai-completions`，换一条协议就得改 catalog、
// runner、test 三处硬编码。注册表把这两件事各收到一个显式入口。
//
// 默认形状的硬约束：没有注册任何扩展、没有指定 provider 时，发出的 JSON 必须与
// 历史实现**逐字节一致**（TestDefaultWireBodyIsByteIdentical / TestStreamDefaultWireBodyMatchesLegacyMarshal）。

const (
	// ProviderAPIOpenAICompletions 是默认协议：OpenAI chat/completions 兼容端点。
	ProviderAPIOpenAICompletions = "openai-completions"
	// ProviderAPIDeepSeekCompletions 是 DeepSeek 的 OpenAI 兼容路由。
	//
	// 官方 `deepseek-official` 路由本身走 Anthropic Messages（见 dsh-llm-deepseek），
	// 本仓 #96 采用 OpenAI 兼容 wire，所以这里给它一个**独立的 adapter id**，
	// 让它能声明自己的输出上限拼写与 usage 扩展计数，而不是把 DeepSeek 的
	// 差异塞进 OpenAI 的默认形状里。
	ProviderAPIDeepSeekCompletions = "deepseek-completions"
)

// ProviderAdapter 是一条 wire 协议的适配器。
//
// 它只声明**协议差异**，不实现 HTTP：真正发请求的仍是 Client（重试/超时/流解析
// 已验收，本任务不重做）。
type ProviderAdapter interface {
	// API 是 catalog `model.api` 与 `Client.API` 的取值，也是注册表键。
	API() string
	// MaxTokensField 返回这条协议**只接受/首选**的输出上限字段名。
	// 返回 "" 表示沿用既有协商（默认 max_tokens，400/422 指向该字段时换拼写）。
	MaxTokensField() string
	// RequiredFields 是任何一条请求都必须带的顶层字段。
	RequiredFields() []string
	// OptionalFields 是这条协议允许出现的可选顶层字段（含扩展字段名）。
	OptionalFields() []string
	// UsageExtensions 声明这条协议在 `usage` 里可能出现的扩展计数
	// （点号表示嵌套，如 prompt_tokens_details.cached_tokens）。
	//
	// 解析本身是**无损**的（Usage 自己保留所有数值叶），这里声明的是
	// "这些扩展属于谁"——换一个 provider 不应该把别人的扩展算成自己的。
	UsageExtensions() []string
}

// openAICompletionsAdapter 是历史默认协议。
type openAICompletionsAdapter struct{}

func (openAICompletionsAdapter) API() string { return ProviderAPIOpenAICompletions }

// MaxTokensField 返回 ""：保留既有"先发 max_tokens、被明确拒绝才换拼写"的协商。
func (openAICompletionsAdapter) MaxTokensField() string { return "" }

func (openAICompletionsAdapter) RequiredFields() []string {
	return []string{"model", "messages", "stream"}
}

func (openAICompletionsAdapter) OptionalFields() []string {
	return []string{"tools", "stream_options", "max_tokens", "max_completion_tokens", "reasoning_effort"}
}

func (openAICompletionsAdapter) UsageExtensions() []string {
	return []string{"prompt_tokens_details.cached_tokens", "completion_tokens_details.reasoning_tokens"}
}

// deepSeekCompletionsAdapter 是 DeepSeek OpenAI 兼容路由。
type deepSeekCompletionsAdapter struct{}

func (deepSeekCompletionsAdapter) API() string { return ProviderAPIDeepSeekCompletions }

// MaxTokensField 固定发 max_tokens：DeepSeek 的 chat/completions 只认这个拼写，
// 协商里"被 400 点名就换成 max_completion_tokens"对它只会多打一轮必失败的请求。
func (deepSeekCompletionsAdapter) MaxTokensField() string { return maxTokensFieldLegacy }

func (deepSeekCompletionsAdapter) RequiredFields() []string {
	return []string{"model", "messages", "stream"}
}

func (deepSeekCompletionsAdapter) OptionalFields() []string {
	// 没有 max_completion_tokens：这是 OpenAI 现行拼写，DeepSeek 不认。
	return []string{"tools", "stream_options", "max_tokens", "reasoning_effort"}
}

func (deepSeekCompletionsAdapter) UsageExtensions() []string {
	return []string{
		// DeepSeek 原生 usage 扩展（OpenAI 兼容端点直接回在 usage 顶层）。
		"prompt_cache_hit_tokens",
		"prompt_cache_miss_tokens",
		// 兼容网关把同样的量放在 details 对象里（本机 magpie 网关的真实形状）。
		"prompt_tokens_details.cached_tokens",
		"prompt_tokens_details.cache_read_tokens",
		"prompt_tokens_details.cache_write_tokens",
		"completion_tokens_details.reasoning_tokens",
	}
}

// adapterRegistry 是 (api → adapter) 的显式注册表。
//
// 禁止 init() 注册（R2）：内置两个 adapter 在包级字典里，第三方在装配期
// 显式调用 RegisterProviderAdapter。
type adapterRegistry struct {
	mu       sync.RWMutex
	adapters map[string]ProviderAdapter
}

func newAdapterRegistry(builtins ...ProviderAdapter) *adapterRegistry {
	r := &adapterRegistry{adapters: make(map[string]ProviderAdapter, len(builtins))}
	for _, a := range builtins {
		if a == nil {
			continue
		}
		r.adapters[a.API()] = a
	}
	return r
}

var providerAdapters = newAdapterRegistry(openAICompletionsAdapter{}, deepSeekCompletionsAdapter{}, deepSeekAccountAdapter{})

// RegisterProviderAdapter 注册一条 wire 协议。
//
// 重复注册同一个 api 是错误（一个能力只允许一个提供者，与 kernel.Provide 同一口径），
// 空 api 也是错误；返回错误而不是 panic，装配期可以直接 fail-loud。
func RegisterProviderAdapter(adapter ProviderAdapter) error {
	if adapter == nil {
		return fmt.Errorf("llm: 注册了 nil provider 适配器")
	}
	api := strings.TrimSpace(adapter.API())
	if api == "" || api != adapter.API() {
		return fmt.Errorf("llm: provider 适配器的 api 不能为空或含首尾空白: %q", adapter.API())
	}
	providerAdapters.mu.Lock()
	defer providerAdapters.mu.Unlock()
	if prev, ok := providerAdapters.adapters[api]; ok {
		return fmt.Errorf("llm: provider 适配器 %q 已由 %T 提供，不允许重复注册", api, prev)
	}
	providerAdapters.adapters[api] = adapter
	return nil
}

// LookupProviderAdapter 按 api 取适配器。未知 api 返回错误（不是"回落到默认"：
// 静默回落会让 catalog 里写错的 api 变成另一条协议，问题只会更晚暴露）。
func LookupProviderAdapter(api string) (ProviderAdapter, error) {
	name := strings.TrimSpace(api)
	if name == "" {
		name = ProviderAPIOpenAICompletions
	}
	providerAdapters.mu.RLock()
	defer providerAdapters.mu.RUnlock()
	adapter, ok := providerAdapters.adapters[name]
	if !ok {
		return nil, fmt.Errorf("llm: 未知 provider api %q（已注册: %s）", api, strings.Join(providerAdapterAPIsLocked(), ", "))
	}
	return adapter, nil
}

// ProviderAdapterAPIs 返回已注册的 api（排序，供 catalog 校验与 `dshd` 诊断）。
func ProviderAdapterAPIs() []string {
	providerAdapters.mu.RLock()
	defer providerAdapters.mu.RUnlock()
	return providerAdapterAPIsLocked()
}

func providerAdapterAPIsLocked() []string {
	out := make([]string, 0, len(providerAdapters.adapters))
	for api := range providerAdapters.adapters {
		out = append(out, api)
	}
	sort.Strings(out)
	return out
}

// resetProviderAdapters 只给测试用：把注册表恢复成内置两条，避免用例之间互相污染。
func resetProviderAdapters() {
	providerAdapters.mu.Lock()
	defer providerAdapters.mu.Unlock()
	providerAdapters.adapters = map[string]ProviderAdapter{
		ProviderAPIOpenAICompletions:   openAICompletionsAdapter{},
		ProviderAPIDeepSeekCompletions: deepSeekCompletionsAdapter{},
		// 账号路由（官方 #99 llm-deepseek-account）：wire 形状同 DeepSeek 兼容面，
		// 差别只在认证头，由 Client 的 AccountAuth 承担（见 account_route.go）。
		ProviderAPIDeepSeekAccount: deepSeekAccountAdapter{},
	}
}

// ---------------------------------------------------------------------------
// provider 专属顶层请求字段（官方 deepseekLlmApiExtensions 的宿主形状）
// ---------------------------------------------------------------------------

// RequestExtension 是一个 provider 专属的顶层请求字段。
//
// 与官方一致：字段名全局唯一（按 api 维度）、prepare 失败只丢这一个字段、
// 2xx 后按注册顺序跑 accept。字段值必须是合法 JSON（官方做 structuredClone + freeze，
// 这里在 prepare 后立刻 unmarshal 校验一次，避免把半截 JSON 拼进请求体）。
type RequestExtension struct {
	// Field 是顶层字段名，不能为空/含首尾空白，也不能与 base 请求的字段撞名。
	Field string
	// API 是这个字段属于哪条协议；"" = 所有协议。
	API string
	// Prepare 从本次请求算出字段值；返回的值必须是一个 JSON 值。
	// 返回值非 nil 的 accept 会在 HTTP 2xx 之后调用一次（非 2xx / 连接失败不调用）。
	Prepare func(ctx context.Context, req *Request) (value json.RawMessage, accept func() error, err error)
}

type extensionRegistry struct {
	mu      sync.RWMutex
	byAPI   map[string]map[string]RequestExtension
	ordered []RequestExtension
}

var requestExtensions = &extensionRegistry{byAPI: map[string]map[string]RequestExtension{}}

func extensionKey(api string) string {
	if api == "" {
		return "*"
	}
	return api
}

// RegisterRequestExtension 注册一个 provider 扩展字段。
func RegisterRequestExtension(ext RequestExtension) error {
	field := strings.TrimSpace(ext.Field)
	if field == "" || field != ext.Field {
		return fmt.Errorf("llm: 扩展字段名不能为空或含首尾空白: %q", ext.Field)
	}
	if ext.Prepare == nil {
		return fmt.Errorf("llm: 扩展字段 %q 没有 prepare", field)
	}
	if api := strings.TrimSpace(ext.API); api != ext.API {
		return fmt.Errorf("llm: 扩展字段 %q 的 api 不能含首尾空白: %q", field, ext.API)
	}
	requestExtensions.mu.Lock()
	defer requestExtensions.mu.Unlock()
	key := extensionKey(ext.API)
	bucket := requestExtensions.byAPI[key]
	if bucket == nil {
		bucket = map[string]RequestExtension{}
		requestExtensions.byAPI[key] = bucket
	}
	if _, dup := bucket[field]; dup {
		return fmt.Errorf("llm: 扩展字段 %q 在 api %q 上已注册，不允许重复", field, ext.API)
	}
	bucket[field] = ext
	requestExtensions.ordered = append(requestExtensions.ordered, ext)
	return nil
}

// PreparedRequestExtension 是一条已算出值的扩展字段。
type PreparedRequestExtension struct {
	Field  string
	Value  json.RawMessage
	Accept func() error
}

// PrepareRequestExtensions 取出这条协议（含通用字段）的扩展并按注册顺序 prepare。
//
// 语义与官方一致：
//   - prepare 失败/返回非法 JSON → **只丢这一个字段**，其余照发；
//     返回 omitted 里带字段名与原因，由调用方决定记日志；
//   - 上下文已取消 → 立刻返回取消错误，不再继续 prepare。
func PrepareRequestExtensions(ctx context.Context, api string, req *Request) (prepared []PreparedRequestExtension, omitted map[string]error, err error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	requestExtensions.mu.RLock()
	snapshot := make([]RequestExtension, 0, len(requestExtensions.ordered))
	for _, ext := range requestExtensions.ordered {
		if ext.API == "" || ext.API == api {
			snapshot = append(snapshot, ext)
		}
	}
	requestExtensions.mu.RUnlock()
	if len(snapshot) == 0 {
		return nil, nil, nil
	}
	for _, ext := range snapshot {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		value, accept, prepareErr := ext.Prepare(ctx, req)
		if prepareErr != nil {
			if omitted == nil {
				omitted = map[string]error{}
			}
			omitted[ext.Field] = prepareErr
			continue
		}
		if value == nil {
			// nil 表示"这次请求这个字段不适用"（官方返回 undefined 同义），不是失败。
			continue
		}
		if !json.Valid(value) {
			if omitted == nil {
				omitted = map[string]error{}
			}
			omitted[ext.Field] = fmt.Errorf("扩展字段 %q 的值不是合法 JSON", ext.Field)
			continue
		}
		prepared = append(prepared, PreparedRequestExtension{Field: ext.Field, Value: value, Accept: accept})
	}
	return prepared, omitted, nil
}

// RunExtensionAccept 在 2xx 之后跑所有 accept 回调，失败合并成一个错误
// （多个失败一起报，不吞任何一个）。
func RunExtensionAccept(prepared []PreparedRequestExtension) error {
	var failures []error
	for _, ext := range prepared {
		if ext.Accept == nil {
			continue
		}
		if err := ext.Accept(); err != nil {
			failures = append(failures, fmt.Errorf("扩展字段 %q 的 accept 失败: %w", ext.Field, err))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return errors.Join(failures...)
}

// marshalWireRequestWithExtensions 把已 prepare 的扩展字段拼到**已序列化**的// base 请求体上。
//
// 为什么拼字节而不是重新 marshal 成一个 map：map 会重排 key，默认形状就不再
// 逐字节不变了。这里只在最后一个 '}' 前追加 `,"field":value`，base 的每个字节
// 都原样保留。
func marshalWireRequestWithExtensions(base []byte, prepared []PreparedRequestExtension) ([]byte, error) {
	if len(prepared) == 0 {
		return base, nil
	}
	if len(base) < 2 || base[len(base)-1] != '}' {
		return nil, fmt.Errorf("llm: base 请求体不是 JSON 对象")
	}
	baseKeys := map[string]json.RawMessage{}
	if err := json.Unmarshal(base, &baseKeys); err != nil {
		return nil, fmt.Errorf("llm: base 请求体不是合法 JSON 对象: %w", err)
	}
	out := make([]byte, 0, len(base)+64*len(prepared))
	out = append(out, base[:len(base)-1]...)
	empty := len(baseKeys) == 0
	for _, ext := range prepared {
		if _, clash := baseKeys[ext.Field]; clash {
			// 官方：字段撞名在 HTTP 之前失败（REQUEST_EXTENSION）。
			return nil, fmt.Errorf("llm: 扩展字段 %q 与 base 请求字段撞名，拒绝发送", ext.Field)
		}
		if !empty {
			out = append(out, ',')
		}
		empty = false
		name, err := json.Marshal(ext.Field)
		if err != nil {
			return nil, err
		}
		out = append(out, name...)
		out = append(out, ':')
		out = append(out, ext.Value...)
	}
	out = append(out, '}')
	return out, nil
}

// wireChangeAllowed 报告可选字段协商提出的新形状是否被当前协议允许。
//
// 已钉死输出上限拼写的协议（DeepSeek）只允许去掉 stream_options /
// reasoning_effort，绝不允许把 max_tokens 换成 max_completion_tokens。
func wireChangeAllowed(adapter ProviderAdapter, next wireProfile) bool {
	if pinned := adapter.MaxTokensField(); pinned != "" && next.maxTokensField != pinned {
		return false
	}
	return true
}

// resetRequestExtensions 只给测试用：清空注册表，避免用例之间互相污染。
func resetRequestExtensions() {
	requestExtensions.mu.Lock()
	defer requestExtensions.mu.Unlock()
	requestExtensions.byAPI = map[string]map[string]RequestExtension{}
	requestExtensions.ordered = nil
}
