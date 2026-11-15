package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dshgo/internal/spi"
)

// 官方 0.2.2 `dsh-web-search-deepseek`：用 Anthropic Messages 兼容端点上的
// 原生 `web_search_20250305` server tool 做搜索。
//
// 为什么值得单独做一个 provider：SearxNG 需要自建端点；DeepSeek 的搜索是
// 模型侧原生工具，凭据与对话共用一把 key，返回的是结构化 `web_search_tool_result`
// 块。**没有这些块就是错误**，不能退化成"抓正文片段"去猜结果。
//
// 凭据解析照官方优先级：**宿主独立凭据存储（credentials.set 写的那个文件）
// 优先于继承的进程环境**，环境变量只是回落。凭据值在每次可用性判断/每次请求
// 时现取，不在构造时快照 —— 否则"界面上刚存了 key"要等下一个进程才生效。
// 账号登录态（x-dsh-auth-token）仍未接。

const (
	// DeepSeekProviderID 是注册键，与官方 provider id 一致。
	DeepSeekProviderID = "deepseek-official"
	// DeepSeekDefaultBaseURL 是官方默认搜索端点（含 /v1，请求时再接 /messages）。
	DeepSeekDefaultBaseURL = "https://api.deepseek.com/anthropic/v1"
	// DeepSeekDefaultModel 是官方默认的 Anthropic 格式模型名。
	DeepSeekDefaultModel = "deepseek-v4-flash"
	// DeepSeekDefaultAPIVersion 是 `anthropic-version` 默认值。
	DeepSeekDefaultAPIVersion = "2023-06-01"
	// DeepSeekDefaultMaxTokens 是辅助搜索请求的输出上限。
	DeepSeekDefaultMaxTokens = 4096
	// DeepSeekDefaultMaxUses 是单次请求里原生 web_search 的最大使用次数。
	DeepSeekDefaultMaxUses = 5
	// deepSeekUserAgent 是归属头（不伪装浏览器）。
	deepSeekUserAgent = "dsh-go/0.2.2"
)

// 官方 `dsh-web-search-deepseek` Config 里三个"设置页可编辑"字段（`src/index.ts:71-79`）
// 的宿主接缝名。设置命名空间 `web-search-deepseek` 经 cmd/dshd 的设置叠加层变成
// 这三个环境变量，**这里才是它们真正的消费方**：
//
//	web-search-deepseek.maxUses    → DSHD_SEARCH_MAX_USES
//	web-search-deepseek.maxTokens  → DSHD_SEARCH_MAX_TOKENS
//	web-search-deepseek.apiVersion → DSHD_SEARCH_API_VERSION
//
// 官方 schema 里 maxUses 是 `.step(1).min(1).default(5)`、maxTokens 是裸
// `.number().default(4096)`、apiVersion 是 `.default('2023-06-01')`；三者空/非法/<=0
// 一律回落官方默认（本仓既有约定：写坏配置不该把能力变成"永远拒绝"——0 次
// web_search 或 0 输出 token 的请求只会稳定失败，那不是"关闭"）。
const (
	// envDeepSeekMaxUses 对应官方 `maxUses`（默认 DeepSeekDefaultMaxUses）。
	envDeepSeekMaxUses = "DSHD_SEARCH_MAX_USES"
	// envDeepSeekMaxTokens 对应官方 `maxTokens`（默认 DeepSeekDefaultMaxTokens）。
	envDeepSeekMaxTokens = "DSHD_SEARCH_MAX_TOKENS"
	// envDeepSeekAPIVersion 对应官方 `apiVersion`（默认 DeepSeekDefaultAPIVersion）。
	envDeepSeekAPIVersion = "DSHD_SEARCH_API_VERSION"
)

// DeepSeek 是 DeepSeek 原生搜索 provider。
type DeepSeek struct {
	// BaseURL 是 Messages 兼容端点根（默认 DeepSeekDefaultBaseURL）。
	BaseURL string
	// APIKey 是**显式注入**的 key（宿主直接构造 / 测试用）；非空时它说了算。
	// 生产装配留空，交给 Credentials → Getenv 解析（见 resolvedAPIKey）。
	APIKey string
	// Credentials 是宿主独立凭据存储的取回入口（nil = 不问库，只读环境）。
	Credentials spi.CredentialResolver
	// Getenv 是环境变量入口；nil = 没有环境（直接构造的 provider 不会被
	// 机器上的同名变量改变可用性）。回落顺序见 resolvedAPIKey。
	Getenv func(string) string
	// Model 是请求里带的模型 id。
	Model string
	// APIVersion 是 anthropic-version 头。
	APIVersion string
	// MaxTokens / MaxUses 是请求参数。
	MaxTokens int
	MaxUses   int
	// HTTP 可替换；nil 用 30s 超时的默认客户端。
	HTTP *http.Client
}

// deepSeekAPIKeyRefs 是这把 key 在宿主里的**别名 ref**，顺序即优先级。
//
// 一个凭据两个名字（`DeepSeekAPIKeyRef` / `DeepSeekAPIKeyEnvAlias`，与
// Search/Fetch API 那两条 provider 共用同一对常量）。两者指向同一把 key，
// 所以优先级链是
// 「库里任意一个 > 环境里任意一个」，再在各自那一层按这个顺序取第一个非空
// （见 resolvedAPIKey 的两轮循环：先整轮问库，再整轮问环境）。
var deepSeekAPIKeyRefs = []string{DeepSeekAPIKeyRef, DeepSeekAPIKeyEnvAlias}

// NewDeepSeekFromEnv 按环境造一个 DeepSeek provider（生产与测试同一份）。
//
// 官方 `dsh-web-search-deepseek` 的 Config 字段 → 宿主环境变量：
//
//	baseURL    → DEEPSEEK_SEARCH_BASE_URL / DSHD_SEARCH_BASE_URL
//	model      → DEEPSEEK_SEARCH_MODEL / DSHD_SEARCH_MODEL
//	apiKey     → DEEPSEEK_API_KEY / DSHD_SEARCH_API_KEY（官方是 credential-ref：先问宿主独立凭据存储）
//	apiVersion → DSHD_SEARCH_API_VERSION
//	maxTokens  → DSHD_SEARCH_MAX_TOKENS
//	maxUses    → DSHD_SEARCH_MAX_USES
//
// 后三条是 `web-search-deepseek` 设置页字段的**真实消费方**：cmd/dshd 装配时把设置
// 文档叠在环境变量前面（settingsEnvOverlay），所以这里读到的就是用户在设置页里改的值。
//
// apiKey 与其余五个不同，**故意不在这里快照**：它的取值要走"凭据库 > 环境"
// （见 resolvedAPIKey），而库里的值可以在进程存活期间被 credentials.set 改掉。
// 这里只交出两个入口（Getenv / Credentials），取值时机是 Available / Search。
// 端点/模型名不是凭据（凭据 ref 就是凭据名，见 catalog 的 apiKeyEnv），
// 只从"设置叠加层之后的环境"读。
func NewDeepSeekFromEnv(getenv func(string) string) *DeepSeek {
	return newDeepSeek(getenv, nil)
}

// newDeepSeek 是装配的真实形态：环境入口 + 凭据库入口（nil = 不问库）。
func newDeepSeek(getenv func(string) string, credentials spi.CredentialResolver) *DeepSeek {
	if getenv == nil {
		// nil = **没有环境**（不是"读进程环境"）：保持"没配就不可用"的老语义，
		// 只在宿主装配缺参数时出现。
		getenv = func(string) string { return "" }
	}
	base := firstNonEmpty(getenv("DEEPSEEK_SEARCH_BASE_URL"), getenv("DSHD_SEARCH_BASE_URL"))
	if base == "" {
		base = DeepSeekDefaultBaseURL
	}
	model := firstNonEmpty(getenv("DEEPSEEK_SEARCH_MODEL"), getenv("DSHD_SEARCH_MODEL"))
	if model == "" {
		model = DeepSeekDefaultModel
	}
	// 官方 maxUses 有 `.min(1)`、maxTokens 是裸 number；两者都要正整数才生效。
	maxUses := DeepSeekDefaultMaxUses
	if n, ok := parseBoundedInt(getenv(envDeepSeekMaxUses), 1, 0); ok {
		maxUses = n
	}
	maxTokens := DeepSeekDefaultMaxTokens
	if n, ok := parseBoundedInt(getenv(envDeepSeekMaxTokens), 1, 0); ok {
		maxTokens = n
	}
	// apiVersion 是非空才生效（官方 `.default('2023-06-01')`，空串等于没设）。
	apiVersion := strings.TrimSpace(getenv(envDeepSeekAPIVersion))
	if apiVersion == "" {
		apiVersion = DeepSeekDefaultAPIVersion
	}
	return &DeepSeek{
		BaseURL:     base,
		Credentials: credentials,
		Getenv:      getenv,
		Model:       model,
		APIVersion:  apiVersion,
		MaxTokens:   maxTokens,
		MaxUses:     maxUses,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// resolvedAPIKey 是"这把 key 现在是什么"的**唯一**判定点。
//
// 优先级照官方 credentials provider：**可写凭据存储是主来源，继承的进程环境
// 只是 fallback**。两个别名 ref 下，这条优先级要分两轮走：
//
//	第一轮：问库要 DEEPSEEK_API_KEY，没有就问库要 DSHD_SEARCH_API_KEY；
//	第二轮（库里两个都没有）：问环境，同一个顺序。
//
// 为什么不能"逐个别名地 库→env"：那样库里的 DSHD_SEARCH_API_KEY 会被环境里
// 的 DEEPSEEK_API_KEY 抢过去，等于把官方"文件优先于环境"反过来
// （env 有值 + 库有值 → 必须是库赢）。
//
// 显式注入的 APIKey（宿主直构 / 测试）永远最优先：那是调用方已经决定的值。
// 每次调用都现取：界面上刚存下的 key 下一步就该生效，不等重启。
func (d *DeepSeek) resolvedAPIKey() string {
	if explicit := strings.TrimSpace(d.APIKey); explicit != "" {
		return explicit
	}
	if d.Credentials != nil {
		for _, ref := range deepSeekAPIKeyRefs {
			if value, ok := d.Credentials.ResolveCredential(ref); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	getenv := d.Getenv
	if getenv == nil {
		// Getenv 为 nil = **没有环境**，不是"读进程环境"。
		// 直接构造的 provider（测试、宿主显式注入）不该被机器上恰好存在的
		// DEEPSEEK_API_KEY 悄悄改变可用性：环境入口必须显式交进来才生效。
		return ""
	}
	for _, ref := range deepSeekAPIKeyRefs {
		if value := strings.TrimSpace(getenv(ref)); value != "" {
			return value
		}
	}
	return ""
}

func (d *DeepSeek) ID() string { return DeepSeekProviderID }

// Available 是本地检查：能解析到 key（凭据库 > 环境）且端点可解析。绝不联网。
func (d *DeepSeek) Available() bool {
	if d.resolvedAPIKey() == "" {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(d.BaseURL))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func (d *DeepSeek) endpoint() string {
	return strings.TrimSuffix(strings.TrimSpace(d.BaseURL), "/") + "/messages"
}

func (d *DeepSeek) model() string {
	if strings.TrimSpace(d.Model) == "" {
		return DeepSeekDefaultModel
	}
	return d.Model
}

func (d *DeepSeek) apiVersion() string {
	if strings.TrimSpace(d.APIVersion) == "" {
		return DeepSeekDefaultAPIVersion
	}
	return d.APIVersion
}

func (d *DeepSeek) maxTokens() int {
	if d.MaxTokens <= 0 {
		return DeepSeekDefaultMaxTokens
	}
	return d.MaxTokens
}

func (d *DeepSeek) maxUses() int {
	if d.MaxUses <= 0 {
		return DeepSeekDefaultMaxUses
	}
	return d.MaxUses
}

// deepSeekSearchBody 是**官方件的请求形状**（字段顺序也照着来，便于对照抓包）。
type deepSeekSearchBody struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Messages  []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		MaxUses int    `json:"max_uses"`
	} `json:"tools"`
	Thinking struct {
		Type string `json:"type"`
	} `json:"thinking"`
}

// deepSeekSearchResponse 只声明用到的块。
type deepSeekSearchResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		// text 块的引用（snippet 的真源）。
		Citations []struct {
			URL       string `json:"url"`
			CitedText string `json:"cited_text"`
		} `json:"citations"`
		// web_search_tool_result 块的 content。
		Content []struct {
			Type    string `json:"type"`
			URL     string `json:"url"`
			Title   string `json:"title"`
			PageAge string `json:"page_age"`
		} `json:"content"`
	} `json:"content"`
}

// Search 发一次原生 web_search，并把结果映射成 seam 的 SearchSource。
func (d *DeepSeek) Search(ctx context.Context, query string, limit int) (spi.SearchResult, error) {
	// key 一次取定，这一轮请求的认证头与错误清洗用**同一个值**：分成两次取的话，
	// 中途被 credentials.set 改掉就会拿旧 key 去发、用新 key 去清洗（漏回显）。
	apiKey := d.resolvedAPIKey()
	if apiKey == "" || !d.Available() {
		return spi.SearchResult{}, spi.NewWebError(spi.WebProviderCredentialMissing,
			"DeepSeek 搜索没有 API key：在设置里存 DEEPSEEK_API_KEY（credentials.set），或设环境变量 DEEPSEEK_API_KEY / DSHD_SEARCH_API_KEY")
	}
	if strings.TrimSpace(query) == "" {
		return spi.SearchResult{}, fmt.Errorf("没有查询词")
	}
	if limit <= 0 {
		limit = 8
	}

	body := deepSeekSearchBody{Model: d.model(), MaxTokens: d.maxTokens()}
	body.Messages = append(body.Messages, struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}{
		Role: "user",
		Content: []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{{Type: "text", Text: "Perform a web search for the query: " + query}},
	})
	body.Tools = append(body.Tools, struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		MaxUses int    `json:"max_uses"`
	}{Type: "web_search_20250305", Name: "web_search", MaxUses: d.maxUses()})
	body.Thinking.Type = "disabled"
	payload, err := json.Marshal(body)
	if err != nil {
		return spi.SearchResult{}, spi.WrapWebError(spi.WebProviderError, err, "序列化搜索请求失败")
	}

	endpoint := d.endpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return spi.SearchResult{}, spi.WrapWebError(spi.WebProviderError, err, "构造搜索请求失败")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("anthropic-version", d.apiVersion())
	req.Header.Set("User-Agent", deepSeekUserAgent)

	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return spi.SearchResult{}, spi.WrapWebError(spi.WebAborted, ctx.Err(), "DeepSeek 搜索被取消")
		}
		return spi.SearchResult{}, spi.WrapWebError(spi.WebProviderError, err,
			"DeepSeek 搜索请求失败（端点 %s）", endpoint)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return spi.SearchResult{}, spi.WrapWebError(spi.WebProviderError, err, "读 DeepSeek 搜索响应失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return spi.SearchResult{}, spi.NewWebError(spi.WebProviderError,
			"DeepSeek 搜索返回 HTTP %d：%s（端点 %s）", resp.StatusCode, deepSeekErrorDetail(raw, apiKey), endpoint)
	}
	var parsed deepSeekSearchResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return spi.SearchResult{}, spi.WrapWebError(spi.WebProviderError, err, "DeepSeek 搜索响应不是合法 JSON")
	}
	return mapDeepSeekSearch(parsed)
}

// mapDeepSeekSearch 与官方 mapAnthropicResponse 同语义：
// 只认 web_search_tool_result 里的 web_search_result；snippet 来自 text 块 citations
// （首次出现优先）；按 URL 去重；一个结果块都没有 = 错误（不是空成功）。
func mapDeepSeekSearch(response deepSeekSearchResponse) (spi.SearchResult, error) {
	snippets := map[string]string{}
	for _, block := range response.Content {
		if block.Type != "text" {
			continue
		}
		for _, citation := range block.Citations {
			if citation.URL == "" || citation.CitedText == "" {
				continue
			}
			if _, seen := snippets[citation.URL]; !seen {
				snippets[citation.URL] = citation.CitedText
			}
		}
	}
	sawResultBlock := false
	seen := map[string]bool{}
	var sources []spi.SearchSource
	for _, block := range response.Content {
		if block.Type != "web_search_tool_result" {
			continue
		}
		sawResultBlock = true
		for _, item := range block.Content {
			if item.Type != "web_search_result" || item.URL == "" || seen[item.URL] {
				continue
			}
			seen[item.URL] = true
			sources = append(sources, spi.SearchSource{
				URL:         item.URL,
				Title:       item.Title,
				Snippet:     snippets[item.URL],
				PublishedAt: item.PageAge,
			})
		}
	}
	if !sawResultBlock {
		return spi.SearchResult{}, spi.NewWebError(spi.WebProviderError,
			"DeepSeek 没有返回 web_search_tool_result 块：这次请求可能没触发原生搜索（不是「零命中」）")
	}
	return spi.SearchResult{Sources: sources}, nil
}

// deepSeekErrorDetail 取错误消息并清洗凭据（错误体可能回显 key）。
func deepSeekErrorDetail(raw []byte, credential string) string {
	var parsed struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	text := strings.TrimSpace(string(raw))
	if err := json.Unmarshal(raw, &parsed); err == nil {
		switch value := parsed.Error.(type) {
		case string:
			if value != "" {
				text = value
			}
		case map[string]any:
			if message, ok := value["message"].(string); ok && message != "" {
				text = message
			}
		}
		if parsed.Message != "" {
			text = parsed.Message
		}
	}
	if credential != "" {
		text = strings.ReplaceAll(text, credential, "[redacted]")
	}
	if runes := []rune(text); len(runes) > 300 {
		text = string(runes[:300]) + "…"
	}
	return text
}
