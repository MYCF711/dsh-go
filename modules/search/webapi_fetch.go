package search

// 本文件是官方 0.2.2-rc.2 `deepseek-fetch-api` 的真实现：抓取目标页面的 Markdown
// 正文，一次直连 API 请求，没有模型请求。
//
// 权威（只读）：
//   - `official-rc2/.../dsh-web-deepseek-api/src/fetch.ts:13-21`（id 与选项）
//   - `official-rc2/.../dsh-web-deepseek-api/src/fetch.ts:23-62`（available / fetch / 结果映射）
//   - `official-rc2/.../dsh-web-deepseek-api/src/index.ts:187-201`（注册与 ready）
//
// 官方语义（照抄，不"顺手优化"）：Fetch API **不报目标状态码、也不报最终 URL**，
// 目标取不到可用内容就是失败；因此成功 = 请求的页面已以 HTTP 200 取回。
// 所以结果里的 `statusCode` 恒为 200、`url` 就是请求的那个 URL。

import (
	"context"
	"encoding/json"
	"net/http"

	"dshgo/internal/spi"
)

// webAPIFetchBody 是官方 Fetch API 的请求体（`fetch.ts:55`）。
type webAPIFetchBody struct {
	URL string `json:"url"`
}

// DeepSeekFetchAPI 是 `deepseek-fetch-api`（官方 `DeepSeekFetchApiProvider`）。
type DeepSeekFetchAPI struct {
	// Resolve 读一次配置快照（官方 `resolveOptions()`，每次抓取入口读一次）。
	Resolve func() DeepSeekWebAPIOptions
	// HTTP 可替换；nil 用 30s 超时且拒绝重定向的默认客户端。
	HTTP *http.Client
}

// ID 是注册键（官方 `fetch.ts:13`）。
func (p *DeepSeekFetchAPI) ID() string { return DeepSeekFetchAPIProviderID }

// resolveOptions 读一次快照并归一化。
func (p *DeepSeekFetchAPI) resolveOptions() DeepSeekWebAPIOptions {
	options := DeepSeekWebAPIOptions{}
	if p.Resolve != nil {
		options = p.Resolve()
	}
	options.Config = options.Config.normalized()
	return options
}

// Available 是本地可用性：有凭据 + 每个端点都合法（官方 `fetch.ts:45-49`）。
func (p *DeepSeekFetchAPI) Available() bool {
	options := p.resolveOptions()
	auth, _ := resolveWebAPITarget(webAPIFetchCapability, options)
	return auth.ready() && auth.sourcesParse()
}

// Fetch 发一次 Fetch API 请求（官方 `fetch.ts:51-62`）。
func (p *DeepSeekFetchAPI) Fetch(ctx context.Context, target string) (spi.FetchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return spi.FetchResult{}, webAPIAborted(ctx, err)
	}
	if target == "" {
		return spi.FetchResult{}, spi.NewWebError(spi.WebInvalidURL, "web: 抓取目标 URL 是空的")
	}
	options := p.resolveOptions()
	auth, guidance := resolveWebAPITarget(webAPIFetchCapability, options)
	result, err := dispatchWebAPI(ctx, p.HTTP, auth, webAPIRequest[fetchPayload]{
		path:     webAPIFetchCapability.path,
		body:     webAPIFetchBody{URL: target},
		decode:   decodeWebAPIFetchResponse,
		guidance: guidance,
	})
	if err != nil {
		return spi.FetchResult{}, err
	}
	return spi.FetchResult{
		URL:        target,
		StatusCode: http.StatusOK,
		Body: struct {
			Kind    spi.FetchBodyKind
			Content string
		}{Kind: spi.FetchBodyText, Content: result.Content},
		Bytes:     len(result.Content),
		Truncated: false,
	}, nil
}

// fetchPayload 是解码后的正文（官方 `WebFetchResult.body.content`）。
type fetchPayload struct {
	Content string
}

// decodeWebAPIFetchResponse 校验并投影响应：`content` 必须是 string（官方 `fetch.ts:57`）。
//
// 用 map 逐字段读而不是 struct：官方的判据是**精确字段名 + typeof**，
// struct 的 JSON 解析对字段名不区分大小写，会把 `CONTENT` 也认下来。
func decodeWebAPIFetchResponse(payload []byte) (fetchPayload, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return fetchPayload{}, webAPIInvalidResponse()
	}
	content, ok := webAPIStringField(fields, "content")
	if !ok {
		return fetchPayload{}, webAPIInvalidResponse()
	}
	return fetchPayload{Content: content}, nil
}
