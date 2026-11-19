package llm

// 模型发现的**协议**分支（官方 `dsh-llm-pi-ai/src/discovery.ts`）。
//
// # 为什么需要它
//
// `llm.discoverModels` 的官方入参里有一个 `api`（wire 协议名）：
//
//	dsh-llm/lib/typert.host.js:12  api: z.string().optional()
//	  'Wire protocol the endpoint speaks, when the draft names one.'
//
// 携带它的意义不只是记录：**不同协议的模型列表端点、鉴权头与响应形状都不同**。
// 官方 `discovery.ts:38-49` 的 `LISTABLE_PROTOCOLS` 是闭合集合，逐条对应一种
// 可读的列表端点；集合外的协议官方回 `DISCOVERY_UNSUPPORTED`（:302-307）——
// 那是"这个协议没有可读的列表"，不是"没有模型"。
//
// 本仓此前只实现 `openai-completions` 一种（`GET <baseURL>/models` + Bearer），
// 把 `api` 字段读进来就丢掉：声明 `anthropic-messages` 的草稿会被**按 OpenAI
// 的形状**去问，拿到 401/404 后报成"端点不通"。这里按官方逐协议补齐。
//
// # 官方的硬判据（逐条搬，不自造）
//
//   - 端点（`discovery.ts:117-121` `listingUrl`）：
//     openai-completions / openai-responses → `{base}/models`
//     google-generative-ai                  → `{base}/models?pageSize=1000`
//     anthropic-messages                    → `{root}/v1/models?limit=1000`
//     （`root` = base 去掉尾部斜杠、再掉一层 `/v1`——网关文档两种拼法都发）
//   - 鉴权（`:325-335`）：anthropic 用 `x-api-key` + `anthropic-version: 2023-06-01`；
//     gemini 用 `x-goog-api-key`；OpenAI 系用 `Authorization: Bearer`。
//   - 响应（`:187-232` / `:245-273`）：OpenAI 系先看 `data` 数组，没有再看
//     `models` 对象（网关扩展形态，键是端点认的 id）；Gemini 只有 `models` 数组，
//     且 `supportedGenerationMethods` 里没有 `generateContent` 的直接跳过。
//   - 缺 `api` 时按 `openai-completions` 问（`:293`，官方词面
//     "the shape a gateway is overwhelmingly likely to speak"）。
//   - 端点未列出模型**不是**空列表：坏形状响亮报错。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// 官方 `LISTABLE_PROTOCOLS` 的另外三条（`openai-completions` 已由 provider.go 定义）。
const (
	// ProviderAPIAnthropicMessages 是 Anthropic Messages 协议（原生 `/v1/models`）。
	ProviderAPIAnthropicMessages = "anthropic-messages"
	// ProviderAPIOpenAIResponses 是 OpenAI Responses 协议（与 chat completions 同一
	// 个 `/models` 列表端点，官方 `listingUrl` 对它没有分支）。
	ProviderAPIOpenAIResponses = "openai-responses"
	// ProviderAPIGoogleGenerativeAI 是 Gemini 协议（`{base}/models?pageSize=N`）。
	ProviderAPIGoogleGenerativeAI = "google-generative-ai"
)

// anthropicVersion 是 Anthropic 列表端点要求的最小 API 版本（`discovery.ts:52`）。
const anthropicVersion = "2023-06-01"

// geminiModelLimit 是 Gemini 列表一页的上限（`discovery.ts:58`）；只读一页，
// 不跟 `nextPageToken`——与官方一致。
const geminiModelLimit = 1000

// anthropicModelLimit 同理（`discovery.ts:55`），不跟 `has_more`。
const anthropicModelLimit = 1000

// ErrDiscoverProtocolUnsupported 对应官方 `DISCOVERY_UNSUPPORTED`：这个协议
// 没有可读的模型列表（Azure 的 `api-key`、Codex 的 OAuth…官方同样不做）。
var ErrDiscoverProtocolUnsupported = errors.New("llm: 该协议没有可读的模型列表")

// DiscoveryProtocols 是官方 `LISTABLE_PROTOCOLS` 的逐字投影（顺序按官方）。
func DiscoveryProtocols() []string {
	return []string{
		ProviderAPIAnthropicMessages,
		ProviderAPIGoogleGenerativeAI,
		ProviderAPIOpenAICompletions,
		ProviderAPIOpenAIResponses,
	}
}

// NormalizeDiscoveryProtocol 把调用方给的 `api` 收敛成可探测协议名。
//
// 空串 = 没声明 → `openai-completions`（官方 `discovery.ts:293` 的默认）。
// 集合外 → `ErrDiscoverProtocolUnsupported`，**不**偷偷按 OpenAI 去问：
// 那样会把"协议不支持"报成"端点/凭据有问题"。
func NormalizeDiscoveryProtocol(api string) (string, error) {
	trimmed := strings.TrimSpace(api)
	if trimmed == "" {
		return ProviderAPIOpenAICompletions, nil
	}
	for _, known := range DiscoveryProtocols() {
		if trimmed == known {
			return known, nil
		}
	}
	return "", fmt.Errorf("%w: %q 不在官方可读列表里（%s）；请手工填模型，或换一个协议",
		ErrDiscoverProtocolUnsupported, trimmed, strings.Join(DiscoveryProtocols(), " / "))
}

// discoveryListingEndpoint 按协议构造列表端点（官方 `listingUrl` 的逐条搬运）。
//
// OpenAI 系沿用 `discoverEndpoint` 的四形态规范化（既有 58 个方法的口径，
// 不改它）；Anthropic 与 Gemini 各按官方拼法另算，因为它们**不**接受
// "<baseURL>/models" 这种拼法。
func discoveryListingEndpoint(raw string, protocol string) (string, error) {
	base, err := normalizedBaseURL(raw)
	if err != nil {
		return "", err
	}
	switch protocol {
	case ProviderAPIGoogleGenerativeAI:
		return base + "/models?pageSize=" + fmt.Sprint(geminiModelLimit), nil
	case ProviderAPIAnthropicMessages:
		// 官方：`root = base 去掉尾部 /v1`，再走 `<root>/v1/models`。
		root := strings.TrimSuffix(base, "/v1")
		return root + "/v1/models?limit=" + fmt.Sprint(anthropicModelLimit), nil
	default:
		return discoverEndpoint(base)
	}
}

// normalizedBaseURL 校验 scheme/host 并去掉尾部斜杠（查询串与片段对列表端点
// 没有意义，一并丢掉，免得不同拼法拿到不同缓存）。
func normalizedBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("baseURL 是空串")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("baseURL 不是合法 URL: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("baseURL 必须是 http/https，收到 %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("baseURL 缺 host")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	trimmedPath := strings.TrimSuffix(parsed.Path, "/")
	if trimmedPath == "" {
		return parsed.Scheme + "://" + parsed.Host, nil
	}
	return parsed.Scheme + "://" + parsed.Host + trimmedPath, nil
}

// applyDiscoveryAuth 按协议写鉴权头（官方 `discovery.ts:325-335`）。
//
// 凭据是**一次性**的：只进这一次请求头，不进错误消息、不进缓存键原文。
func applyDiscoveryAuth(req *http.Request, protocol, apiKey string) {
	if protocol == ProviderAPIAnthropicMessages {
		// 版本头与凭据无关，无论有没有 key 都要带（官方同样无条件 set）。
		req.Header.Set("anthropic-version", anthropicVersion)
	}
	if apiKey == "" {
		return
	}
	switch protocol {
	case ProviderAPIAnthropicMessages:
		req.Header.Set("x-api-key", apiKey)
	case ProviderAPIGoogleGenerativeAI:
		req.Header.Set("x-goog-api-key", apiKey)
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

// parseOpenAIListing 读 OpenAI 系列表（官方 `readListing`，`discovery.ts:187-232`）。
//
// 两种形态：标准 `data` 数组优先；网关扩展的 `models` 对象（键即 id，嵌套
// `id` 只在键为空时兜底）。对象里非对象值一律忽略（可能是目录元数据）。
func parseOpenAIListing(reader io.Reader, endpoint string) ([]DiscoveredModel, error) {
	var doc struct {
		Data   json.RawMessage `json:"data"`
		Models json.RawMessage `json:"models"`
	}
	if err := json.NewDecoder(reader).Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s 的响应不是合法 JSON: %w", endpoint, err)
	}
	if len(doc.Data) > 0 && string(doc.Data) != "null" {
		var entries []discoverEntry
		if err := json.Unmarshal(doc.Data, &entries); err != nil {
			return nil, fmt.Errorf("%s 的 data 不是数组（官方只认数组或 models 对象）: %w", endpoint, err)
		}
		out := make([]DiscoveredModel, 0, len(entries))
		for i, entry := range entries {
			if strings.TrimSpace(entry.ID) == "" {
				// 官方结果里 id 是**必填**：拿不到 id 就没法代表这一条。
				// 静默丢掉会让用户以为"这个模型不存在"，所以响亮报错。
				return nil, fmt.Errorf("%s 的第 %d 条模型没有 id（官方契约 id 必填）", endpoint, i)
			}
			out = append(out, discoveredFromOpenAIEntry(entry.ID, entry))
		}
		return out, nil
	}
	if len(doc.Models) > 0 && string(doc.Models) != "null" {
		// 先收到 RawMessage：官方对 `models` 对象的判据是
		// "`.filter(([, raw]) => raw !== null && typeof raw === 'object' && !Array.isArray(raw))`
		// —— **非对象值直接忽略**（网关会把 `object: "list"` 之类的目录元数据混进来），
		// 直接 unmarshal 成 map[string]discoverEntry 会因一个字符串属性整页报错。
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(doc.Models, &raw); err != nil {
			return nil, fmt.Errorf("%s 的 models 不是对象（官方只认 data 数组或 models 对象）: %w", endpoint, err)
		}
		out := make([]DiscoveredModel, 0, len(raw))
		for key, value := range raw {
			var entry discoverEntry
			if err := json.Unmarshal(value, &entry); err != nil {
				// 非对象（字符串/数组/null）→ 跳过，不是失败。
				continue
			}
			id := strings.TrimSpace(key)
			if id == "" {
				id = strings.TrimSpace(entry.ID)
			}
			// 官方：拿不到 id 的条目跳过（单条坏行不该否掉整个端点）。
			if id == "" {
				continue
			}
			out = append(out, discoveredFromOpenAIEntry(id, entry))
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s 的响应里既没有 data 数组也没有 models 对象（这不是「没有模型」，"+
		"是响应形状不对）：确认它是 OpenAI 兼容端点（GET /models）", endpoint)
}

// discoveredFromOpenAIEntry 把一条 OpenAI 系记录映射成官方 `ModelDiscovery`。
//
// name 取官方三个候选里第一个非空的（discovery.ts:214 `label(entry?.name,
// entry?.display_name, entry?.displayName)`）。
//
// **有意不跟官方的一处**：官方最后还有 `?? id` 回落，本仓保持"没给就是不知道"
// （空串），与 `ListModels` 及既有 `discover_test.go` 钉住的判据一致。官方
// 结果 schema 里 `name` 本来就是 optional（`llm/discoverModels` 的 zod：
// `'name': z.string().optional()`），客户端必须能处理它缺席；拿 id 冒充 name
// 会让"端点真的没起名字"这件事在界面上看不出来。
func discoveredFromOpenAIEntry(id string, entry discoverEntry) DiscoveredModel {
	return DiscoveredModel{
		ID:              id,
		Name:            firstLabel(entry.Name, entry.DisplayNameSnake, entry.DisplayNameCamel),
		ContextWindow:   firstPositive(entry.ContextWindow, entry.ContextLength, entry.MaxContextTokens, architectureContextLength(entry)),
		MaxTokens:       firstPositive(entry.MaxTokens, entry.MaxOutputTokens, topProviderMaxCompletionTokens(entry)),
		InputModalities: normalizeInputModalities(entry.InputModalities, entry.Modalities, architectureInputModalities(entry)),
	}
}

// firstLabel 取第一个 trim 后非空的字符串（全都空 → 空串 = 不知道）。
func firstLabel(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// geminiListingEntry 是 Gemini `GET /models` 的一条原始记录。
type geminiListingEntry struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	InputTokenLimit            float64  `json:"inputTokenLimit"`
	OutputTokenLimit           float64  `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

// parseGeminiListing 读 Gemini 列表（官方 `readGeminiListing`，`discovery.ts:245-273`）。
//
// `name` 是资源名（`models/gemini-2.5-pro`），请求侧要的是去掉前缀的裸 id；
// `supportedGenerationMethods` 声明了却没有 `generateContent` 的（embedding 之类）
// 不能跑对话，跳过。缺 id 的条目同样跳过，不整体失败。
// 没有 `displayName` 时 name 留空（= 不知道），理由与 `discoveredFromOpenAIEntry`
// 同一条：官方结果里 name 是可选的，拿 id 冒充会让"端点没起名字"看不出来。
// `models` 数组不存在（打到了别的服务）是形状错误，不是"没有模型"。
func parseGeminiListing(reader io.Reader, endpoint string) ([]DiscoveredModel, error) {
	var doc struct {
		Models *[]geminiListingEntry `json:"models"`
	}
	if err := json.NewDecoder(reader).Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s 的响应不是合法 JSON: %w", endpoint, err)
	}
	if doc.Models == nil {
		return nil, fmt.Errorf("%s 的响应里没有 models 数组（这不是「没有模型」，"+
			"是响应形状不对）：确认它是 Gemini 端点（GET /models）", endpoint)
	}
	out := make([]DiscoveredModel, 0, len(*doc.Models))
	for _, entry := range *doc.Models {
		if len(entry.SupportedGenerationMethods) > 0 && !slices.Contains(entry.SupportedGenerationMethods, "generateContent") {
			continue
		}
		id := strings.TrimPrefix(strings.TrimSpace(entry.Name), "models/")
		if id == "" {
			continue
		}
		out = append(out, DiscoveredModel{
			ID:            id,
			Name:          strings.TrimSpace(entry.DisplayName),
			ContextWindow: firstPositive(entry.InputTokenLimit),
			MaxTokens:     firstPositive(entry.OutputTokenLimit),
		})
	}
	return out, nil
}
