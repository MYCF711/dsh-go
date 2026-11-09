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
// 与官方的差异（宿主约束）：本仓没有 credentials seam 的跨模块读取，
// 凭据只从启动环境解析（DEEPSEEK_API_KEY / DSHD_SEARCH_API_KEY）；
// 账号登录态（x-dsh-auth-token）未接。

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

// DeepSeek 是 DeepSeek 原生搜索 provider。
type DeepSeek struct {
	// BaseURL 是 Messages 兼容端点根（默认 DeepSeekDefaultBaseURL）。
	BaseURL string
	// APIKey 为空的 provider 视为不可用（选择器会报结构化错误，不会静默换后端）。
	APIKey string
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

// NewDeepSeekFromEnv 按环境造一个 DeepSeek provider（生产与测试同一份）。
func NewDeepSeekFromEnv(getenv func(string) string) *DeepSeek {
	if getenv == nil {
		return &DeepSeek{}
	}
	base := firstNonEmpty(getenv("DEEPSEEK_SEARCH_BASE_URL"), getenv("DSHD_SEARCH_BASE_URL"))
	if base == "" {
		base = DeepSeekDefaultBaseURL
	}
	model := firstNonEmpty(getenv("DEEPSEEK_SEARCH_MODEL"), getenv("DSHD_SEARCH_MODEL"))
	if model == "" {
		model = DeepSeekDefaultModel
	}
	return &DeepSeek{
		BaseURL:    base,
		APIKey:     firstNonEmpty(getenv("DEEPSEEK_API_KEY"), getenv("DSHD_SEARCH_API_KEY")),
		Model:      model,
		APIVersion: DeepSeekDefaultAPIVersion,
		MaxTokens:  DeepSeekDefaultMaxTokens,
		MaxUses:    DeepSeekDefaultMaxUses,
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

func (d *DeepSeek) ID() string { return DeepSeekProviderID }

// Available 是本地检查：有 key 且端点可解析。绝不联网。
func (d *DeepSeek) Available() bool {
	if strings.TrimSpace(d.APIKey) == "" {
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
	if !d.Available() {
		return spi.SearchResult{}, spi.NewWebError(spi.WebProviderCredentialMissing,
			"DeepSeek 搜索没有 API key：设 DEEPSEEK_API_KEY（或 DSHD_SEARCH_API_KEY）")
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
	req.Header.Set("x-api-key", d.APIKey)
	req.Header.Set("Authorization", "Bearer "+d.APIKey)
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
			"DeepSeek 搜索返回 HTTP %d：%s（端点 %s）", resp.StatusCode, deepSeekErrorDetail(raw, d.APIKey), endpoint)
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
