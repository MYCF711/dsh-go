package search

import (
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

// SearxNG 是 SearxNG 风格 JSON 端点上的搜索 provider。
//
// 端点形如 http://127.0.0.1:8080/search?q={query}&format=json，
// 响应按 `{results:[{title,url,content}], answers?:[...]}` 解析。
type SearxNG struct {
	// Endpoint 是带 {query} 占位的 URL；空表示没配置（= 不可用）。
	Endpoint string
	// HTTP 可替换；nil 时用 20s 超时的默认客户端。
	HTTP *http.Client
}

// SearxNGProviderID 是注册键。
const SearxNGProviderID = "searxng"

func (p *SearxNG) ID() string { return SearxNGProviderID }

// Available 只做本地检查：端点配了就算可用（不联网探活）。
func (p *SearxNG) Available() bool { return strings.TrimSpace(p.Endpoint) != "" }

// Search 跑一条查询。
func (p *SearxNG) Search(ctx context.Context, query string, limit int) (spi.SearchResult, error) {
	if !p.Available() {
		return spi.SearchResult{}, spi.NewWebError(spi.WebProviderUnavailable,
			"搜索后端没配置：设 DSHD_SEARCH_URL 指向一个 SearxNG 风格端点")
	}
	if strings.TrimSpace(query) == "" {
		return spi.SearchResult{}, fmt.Errorf("没有查询词")
	}
	if limit <= 0 {
		limit = 8
	}
	target := strings.ReplaceAll(p.Endpoint, "{query}", url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return spi.SearchResult{}, spi.WrapWebError(spi.WebProviderError, err, "搜索请求构造失败")
	}
	req.Header.Set("Accept", "application/json")
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return spi.SearchResult{}, spi.WrapWebError(spi.WebAborted, ctx.Err(), "搜索被取消")
		}
		return spi.SearchResult{}, spi.WrapWebError(spi.WebProviderError, err, "搜索服务连不上")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return spi.SearchResult{}, spi.WrapWebError(spi.WebProviderError, err, "读搜索响应失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(body))
		if len([]rune(snippet)) > 300 {
			snippet = string([]rune(snippet)[:300]) + "…"
		}
		return spi.SearchResult{}, spi.NewWebError(spi.WebProviderError,
			"搜索服务返回 %d：%s", resp.StatusCode, snippet)
	}

	var raw struct {
		Answer  string   `json:"answer"`
		Answers []string `json:"answers"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return spi.SearchResult{}, spi.WrapWebError(spi.WebProviderError, err, "搜索响应不是预期的 JSON")
	}

	out := spi.SearchResult{Answer: raw.Answer}
	if out.Answer == "" && len(raw.Answers) > 0 {
		out.Answer = raw.Answers[0]
	}
	seen := map[string]bool{}
	for _, r := range raw.Results {
		if r.URL == "" || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		out.Sources = append(out.Sources, spi.SearchSource{Title: r.Title, URL: r.URL, Snippet: r.Content})
		if len(out.Sources) >= limit {
			out.Truncated = len(raw.Results) > len(out.Sources)
			break
		}
	}
	return out, nil
}
