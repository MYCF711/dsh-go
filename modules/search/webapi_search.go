package search

// 本文件是官方 0.2.2-rc.2 `deepseek-search-api` 的真实现：一次搜索 = 一次直连 API 请求，
// 没有辅助模型请求、不做摘要生成。
//
// 权威（只读）：
//   - `official-rc2/.../dsh-web-deepseek-api/src/search.ts:13-25`（id / base / 默认值 / ori_question 上限）
//   - `official-rc2/.../dsh-web-deepseek-api/src/search.ts:48-64` `mapSearchResponse`（响应形状与字段缺席语义）
//   - `official-rc2/.../dsh-web-deepseek-api/src/search.ts:70-137`（available / search / 限速窗口）
//   - `official-rc2/.../dsh-web-deepseek-api/src/search.ts:160-167`（问题折叠与 128 字符上限）
//
// 与官方的差异（宿主真实约束）：
//   - `ori_question` 的来源：官方从会话里读触发这次搜索的用户问题
//     （`index.ts:156-160` `latestUserQuestion`）。宿主 `spi.SearchProvider` 的
//     `Search(ctx, query, limit)` 没有这个入参，所以问题由**显式接缝**注入
//     （`OriginalQuestion`）；没有注入就不发该字段（官方在没有 prompt 时同样不发）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"dshgo/internal/spi"
)

// rateWindow 是限速的窗口长度（官方 `search.ts:121-136` 的 1000ms）。
const rateWindow = time.Second

// webAPISearchBody 是官方 Search API 的请求体（字段顺序同 `search.ts:104-109`）。
type webAPISearchBody struct {
	Query            string `json:"query"`
	Count            int    `json:"count"`
	NeedContent      bool   `json:"need_content"`
	OriginalQuestion string `json:"ori_question,omitempty"`
}

// DeepSeekSearchAPI 是 `deepseek-search-api`（官方 `DeepSeekSearchApiProvider`）。
type DeepSeekSearchAPI struct {
	// Resolve 读一次配置快照（官方 `resolveOptions()`，每次搜索入口读一次，不混两份设置）。
	Resolve func() DeepSeekWebAPIOptions
	// OriginalQuestion 是触发这次搜索的用户问题（宿主接缝；nil = 拿不到，不发 ori_question）。
	OriginalQuestion func() string
	// HTTP 可替换；nil 用 30s 超时且拒绝重定向的默认客户端。
	HTTP *http.Client
	// Now 是可替换时钟（默认 time.Now）；与 Wait 一起构成限速的可测接缝。
	Now func() time.Time
	// Wait 是可替换等待（默认按 ctx 睡）；返回错误 = 这次搜索不发出。
	Wait func(ctx context.Context, d time.Duration) error

	mu     sync.Mutex
	starts []int64 // 当前窗口内每次请求的开始时刻（毫秒，单调递增）
}

// ID 是注册键（官方 `search.ts:13`）。
func (p *DeepSeekSearchAPI) ID() string { return DeepSeekSearchAPIProviderID }

// resolveOptions 读一次快照并归一化（非法值回落官方默认，不让工具变成永远拒绝）。
func (p *DeepSeekSearchAPI) resolveOptions() DeepSeekWebAPIOptions {
	options := DeepSeekWebAPIOptions{}
	if p.Resolve != nil {
		options = p.Resolve()
	}
	options.Config = options.Config.normalized()
	return options
}

// normalized 是配置快照的兜底：count / rps 非法时用官方默认。
func (config WebAPIConfig) normalized() WebAPIConfig {
	if config.Count < 1 || config.Count > searchCountMax {
		config.Count = DeepSeekDefaultCount
	}
	if config.RequestsPerSecond < 1 {
		config.RequestsPerSecond = DeepSeekSearchRequestsPerSecond
	}
	if config.Billing == "" {
		config.Billing = BillingAuto
	}
	return config
}

// Available 是**本地**可用性：有凭据 + 每个端点都合法（官方 `search.ts:92-97`）。
//
// 不联网、不读令牌值以外的任何东西（官方同样"读一次快照即判定"）。
func (p *DeepSeekSearchAPI) Available() bool {
	options := p.resolveOptions()
	auth, _ := resolveWebAPITarget(webAPISearchCapability, options)
	return auth.ready() && auth.sourcesParse()
}

// Search 发一次 Search API 请求（官方 `search.ts:99-114`）。
func (p *DeepSeekSearchAPI) Search(ctx context.Context, query string, limit int) (spi.SearchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return spi.SearchResult{}, webAPIAborted(ctx, err)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return spi.SearchResult{}, fmt.Errorf("没有查询词")
	}
	options := p.resolveOptions()
	auth, guidance := resolveWebAPITarget(webAPISearchCapability, options)

	count := options.Config.Count
	if limit > 0 && limit < count {
		count = limit
	}
	body := webAPISearchBody{Query: query, Count: count, NeedContent: false}
	if options.Config.SendOriginalQuestion && p.OriginalQuestion != nil {
		body.OriginalQuestion = boundedOriginalQuestion(p.OriginalQuestion())
	}

	result, err := dispatchWebAPI(ctx, p.HTTP, auth, webAPIRequest[spi.SearchResult]{
		path:     webAPISearchCapability.path,
		body:     body,
		decode:   mapDeepSeekSearchResponse,
		guidance: guidance,
		waitToSend: func(ctx context.Context) error {
			return p.waitForRequestSlot(ctx, options.Config.RequestsPerSecond)
		},
	})
	if err != nil {
		return spi.SearchResult{}, err
	}
	// Truncated 由接缝在调用方上限处置位（官方 provider 自己恒 false）。
	return result, nil
}

// waitForRequestSlot 等到"最近一秒内开始的请求数 < limit"，然后登记这次开始
// （官方 `waitForRequestSlot`，`search.ts:121-136`；窗口含第一毫秒）。
//
// 等待也要能被取消：等的时候 ctx 断了就是 WEB_ABORTED，不是 provider 错误。
func (p *DeepSeekSearchAPI) waitForRequestSlot(ctx context.Context, limit int) error {
	if limit < 1 {
		limit = DeepSeekSearchRequestsPerSecond
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := p.now().UnixMilli()
		p.mu.Lock()
		for len(p.starts) > 0 && p.starts[0] < now-rateWindow.Milliseconds() {
			p.starts = p.starts[1:]
		}
		if len(p.starts) < limit {
			p.starts = append(p.starts, now)
			p.mu.Unlock()
			return nil
		}
		oldest := p.starts[0]
		p.mu.Unlock()

		wait := time.Duration(oldest+rateWindow.Milliseconds()+1-now) * time.Millisecond
		if wait < 0 {
			wait = 0
		}
		if err := p.wait(ctx, wait); err != nil {
			return err
		}
	}
}

// now 是可替换时钟（默认 time.Now）。
func (p *DeepSeekSearchAPI) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// wait 是可替换等待（默认按 ctx 睡，可被取消）。
func (p *DeepSeekSearchAPI) wait(ctx context.Context, d time.Duration) error {
	if p.Wait != nil {
		return p.Wait(ctx, d)
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// mapDeepSeekSearchResponse 校验响应并投影成 seam 的 SearchResult
// （官方 `mapSearchResponse`，`search.ts:48-64`）。
//
// 严格照官方：
//   - 顶层必须是对象且 `results` 是数组；
//   - 每条必须有非空 string `url`、string `title`、string `snippet`；
//   - `summary` 缺席或 string（null 也算非法）；
//   - `date` 必须存在且为 string 或 null；
//   - 空串/空日期的字段在结果里**缺席**（不是空串）。
func mapDeepSeekSearchResponse(payload []byte) (spi.SearchResult, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		return spi.SearchResult{}, webAPIInvalidResponse()
	}
	rawResults, ok := document["results"]
	if !ok {
		return spi.SearchResult{}, webAPIInvalidResponse()
	}
	var results []json.RawMessage
	if err := json.Unmarshal(rawResults, &results); err != nil {
		return spi.SearchResult{}, webAPIInvalidResponse()
	}
	sources := make([]spi.SearchSource, 0, len(results))
	for _, rawItem := range results {
		source, err := decodeWebAPISearchItem(rawItem)
		if err != nil {
			return spi.SearchResult{}, err
		}
		sources = append(sources, source)
	}
	return spi.SearchResult{Sources: sources, Truncated: false}, nil
}

// decodeWebAPISearchItem 校验并映射一条来源。
func decodeWebAPISearchItem(rawItem json.RawMessage) (spi.SearchSource, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawItem, &fields); err != nil {
		return spi.SearchSource{}, webAPIInvalidResponse()
	}
	url, ok := webAPIStringField(fields, "url")
	if !ok || url == "" {
		return spi.SearchSource{}, webAPIInvalidResponse()
	}
	title, ok := webAPIStringField(fields, "title")
	if !ok {
		return spi.SearchSource{}, webAPIInvalidResponse()
	}
	snippet, ok := webAPIStringField(fields, "snippet")
	if !ok {
		return spi.SearchSource{}, webAPIInvalidResponse()
	}
	summary := ""
	if rawSummary, present := fields["summary"]; present {
		value, ok := webAPIStringValue(rawSummary)
		if !ok {
			return spi.SearchSource{}, webAPIInvalidResponse()
		}
		summary = value
	}
	// date 必须存在：官方判据是 `item.date !== null && typeof item.date !== 'string'`
	// （缺席时 undefined !== null 成立且 typeof 不是 string → 非法）。
	rawDate, present := fields["date"]
	if !present {
		return spi.SearchSource{}, webAPIInvalidResponse()
	}
	date := ""
	if value, ok := webAPIStringValue(rawDate); ok {
		date = value
	} else if !webAPINullValue(rawDate) {
		return spi.SearchSource{}, webAPIInvalidResponse()
	}

	source := spi.SearchSource{URL: url}
	if title != "" {
		source.Title = title
	}
	if snippet != "" {
		source.Snippet = snippet
	}
	if summary != "" {
		source.Summary = summary
	}
	if date != "" {
		source.PublishedAt = date
	}
	return source, nil
}

// webAPIStringField 读一个必须是 string 的字段；缺失或类型不对都回答 false。
func webAPIStringField(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok {
		return "", false
	}
	return webAPIStringValue(raw)
}

// webAPIStringValue 把一段 JSON 当 string 读；不是 string 就回答 false。
//
// `null` 必须单独判掉：`json.Unmarshal` 把 null 读进 string 是"无效果、无错误"，
// 而官方判据是 `typeof value !== 'string'`（null 的 typeof 是 object → 非法）。
func webAPIStringValue(raw json.RawMessage) (string, bool) {
	if webAPINullValue(raw) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// webAPINullValue 判断一段 JSON 是不是 null。
func webAPINullValue(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// webAPIInvalidResponse 是官方 `invalidResponse()` 的忠实翻译（`transport.ts:156-161`）。
func webAPIInvalidResponse() error {
	return &spi.WebError{Code: spi.WebProviderError, Message: "DeepSeek returned an invalid response body"}
}

// boundedOriginalQuestion 把触发问题折成 ori_question 能收的形状：
// 空白折成单空格、首尾去空白、只留前 128 个字符（官方 `search.ts:154-164`）。
func boundedOriginalQuestion(question string) string {
	collapsed := joinWebAPIWhitespace(question)
	if collapsed == "" {
		return ""
	}
	runes := []rune(collapsed)
	if len(runes) > originalQuestionMaxChars {
		runes = runes[:originalQuestionMaxChars]
	}
	return string(runes)
}

// joinWebAPIWhitespace 把任意空白折成单个空格并去首尾空白
// （官方 `question.replace(/\s+/gu, ' ').trim()`）。
//
// JS 的 `\s` 比 Go 的 `strings.Fields` 多认 U+FEFF，所以先把它换成空格。
func joinWebAPIWhitespace(value string) string {
	value = strings.ReplaceAll(value, string(rune(0xFEFF)), " ")
	return strings.Join(strings.Fields(value), " ")
}
