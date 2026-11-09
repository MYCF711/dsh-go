package webfetch

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dshgo/internal/spi"
)

// HTTPProvider 是默认抓取 provider（官方 `dsh-web-fetch-http` 的宿主形状）。
//
// 边界（每一条都是"不把模型当浏览器"的具体化）：
//   - 只接受 http/https，URL 里不许带凭据；
//   - 只跟随**同源**重定向（跨源让调用方自己再发一次请求，避免把源站当跳板）；
//   - 字节 / 字符双上限，超了截断并置 Truncated（Content-Length 声明超限直接拒绝）；
//   - 按 Content-Type 分类：html 交给工具层转 markdown，text/json/xml 原样；
//     二进制/未知类型结构化报错，绝不把乱码当正文；
//   - 超时是 provider 的兜底，调用方的 ctx 优先。
type HTTPProvider struct {
	// Client 可替换；nil 用默认 Transport（重定向由本 provider 自己处理）。
	Client *http.Client
	// MaxBytes 是响应体字节上限；<=0 用内置 512KiB（与历史实现一致）。
	MaxBytes int
	// MaxBodyChars 是解码后字符上限；<=0 用官方默认 200000。
	MaxBodyChars int
	// Timeout 是单次抓取的兜底超时；<=0 用 30s。
	Timeout time.Duration
	// MaxRedirects 是同源重定向跳数上限；<0 表示不允许重定向，0 也用默认 5。
	MaxRedirects int
	// UserAgent 是请求 UA。
	UserAgent string
}

// HTTPProviderID 是注册键（官方 `http`）。
const HTTPProviderID = "http"

const (
	defaultProviderMaxBytes = 512 * 1024
	defaultMaxBodyChars     = 200000
	defaultProviderTimeout  = 30 * time.Second
	defaultMaxRedirects     = 5
	defaultProviderUA       = "dsh-go/0.2.2 (+https://github.com/deepseek-ai)"
	// maxFetchURLLength 是官方 web-fetch-http 的 URL 长度上限。
	maxFetchURLLength = 2048
)

func (p *HTTPProvider) ID() string { return HTTPProviderID }

// Available：匿名抓取永远可用（没有凭据要检查）。
func (p *HTTPProvider) Available() bool { return true }

func (p *HTTPProvider) maxBytes() int {
	if p.MaxBytes > 0 {
		return p.MaxBytes
	}
	return defaultProviderMaxBytes
}

func (p *HTTPProvider) maxBodyChars() int {
	if p.MaxBodyChars > 0 {
		return p.MaxBodyChars
	}
	return defaultMaxBodyChars
}

func (p *HTTPProvider) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return defaultProviderTimeout
}

func (p *HTTPProvider) maxRedirects() int {
	if p.MaxRedirects < 0 {
		return 0
	}
	if p.MaxRedirects == 0 {
		return defaultMaxRedirects
	}
	return p.MaxRedirects
}

func (p *HTTPProvider) userAgent() string {
	if strings.TrimSpace(p.UserAgent) != "" {
		return p.UserAgent
	}
	return defaultProviderUA
}

// Fetch 取一个 URL；非 2xx 是结果而不是错误（状态码与正文一起给出去）。
func (p *HTTPProvider) Fetch(ctx context.Context, target string) (spi.FetchResult, error) {
	current, err := validateFetchURL(target)
	if err != nil {
		return spi.FetchResult{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	client := p.client()
	redirects := 0
	for {
		req, reqErr := http.NewRequestWithContext(callCtx, http.MethodGet, current.String(), nil)
		if reqErr != nil {
			return spi.FetchResult{}, spi.WrapWebError(spi.WebInvalidURL, reqErr, "请求构造失败: %s", current.String())
		}
		req.Header.Set("User-Agent", p.userAgent())
		req.Header.Set("Accept", "text/html,application/xhtml+xml,text/*;q=0.9,application/json;q=0.8")

		resp, doErr := client.Do(req)
		if doErr != nil {
			return spi.FetchResult{}, translateFetchError(ctx, callCtx, doErr)
		}
		if isRedirectStatus(resp.StatusCode) {
			_ = resp.Body.Close()
			if redirects >= p.maxRedirects() {
				return spi.FetchResult{}, spi.NewWebError(spi.WebRedirectBlocked,
					"重定向超过上限（%d 跳）：%s", p.maxRedirects(), current.String())
			}
			location := strings.TrimSpace(resp.Header.Get("Location"))
			if location == "" {
				return spi.FetchResult{}, spi.NewWebError(spi.WebProviderError,
					"重定向响应 HTTP %d 没有 Location: %s", resp.StatusCode, current.String())
			}
			next, parseErr := current.Parse(location)
			if parseErr != nil {
				return spi.FetchResult{}, spi.WrapWebError(spi.WebProviderError, parseErr, "重定向 Location 非法: %q", location)
			}
			if _, err := validateFetchURL(next.String()); err != nil {
				return spi.FetchResult{}, err
			}
			if !sameOrigin(current, next) {
				return spi.FetchResult{}, spi.NewWebError(spi.WebRedirectBlocked,
					"跨源重定向到 %s 不会自动跟随；请直接对那个 URL 再发一次请求", next.String())
			}
			current = next
			redirects++
			continue
		}
		return p.readBody(resp, current)
	}
}

// client 返回一个"重定向由我自己处理"的客户端副本（不改调用方传入的 Client）。
func (p *HTTPProvider) client() *http.Client {
	base := p.Client
	if base == nil {
		base = &http.Client{Timeout: 0, Transport: http.DefaultTransport}
	}
	copied := *base
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copied
}

// readBody 读取、截断、分类、解码最终响应体。
func (p *HTTPProvider) readBody(resp *http.Response, finalURL *url.URL) (spi.FetchResult, error) {
	defer resp.Body.Close()
	contentType := resp.Header.Get("Content-Type")
	kind, ok := classifyContentType(contentType)
	if !ok {
		return spi.FetchResult{}, spi.NewWebError(spi.WebUnsupportedContentType,
			"不支持的 Content-Type %q（只处理 html/text/json/xml）: %s", contentType, finalURL.String())
	}
	limit := p.maxBytes()
	if declared := resp.ContentLength; declared > int64(limit) {
		return spi.FetchResult{}, spi.NewWebError(spi.WebFetchTooLarge,
			"响应声明 %d 字节，超过上限 %d 字节: %s", declared, limit, finalURL.String())
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if readErr != nil {
		return spi.FetchResult{}, spi.WrapWebError(spi.WebProviderError, readErr, "读正文失败: %s", finalURL.String())
	}
	truncated := len(body) > limit
	if truncated {
		body = body[:limit]
	}
	decoded, decodeErr := decodeBody(body, parseCharset(contentType))
	if decodeErr != nil {
		return spi.FetchResult{}, decodeErr
	}
	if maxChars := p.maxBodyChars(); len([]rune(decoded)) > maxChars {
		decoded = string([]rune(decoded)[:maxChars])
		truncated = true
	}
	result := spi.FetchResult{URL: finalURL.String(), StatusCode: resp.StatusCode, ContentType: contentType, Bytes: len(body), Truncated: truncated}
	result.Body.Kind = kind
	result.Body.Content = decoded
	return result, nil
}

// validateFetchURL 是抓取前的无网络校验。
func validateFetchURL(input string) (*url.URL, error) {
	target := strings.TrimSpace(input)
	if target == "" {
		return nil, spi.NewWebError(spi.WebInvalidURL, "url 不能为空")
	}
	if len(target) > maxFetchURLLength {
		return nil, spi.NewWebError(spi.WebInvalidURL, "URL 超过 %d 字节上限", maxFetchURLLength)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, spi.WrapWebError(spi.WebInvalidURL, err, "URL 解析失败: %q", target)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, spi.NewWebError(spi.WebInvalidURL, "只支持 http/https（给的是 %q）", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, spi.NewWebError(spi.WebInvalidURL, "URL 里没有主机名")
	}
	if parsed.User != nil {
		return nil, spi.NewWebError(spi.WebBlockedURL, "URL 里不允许带凭据")
	}
	return parsed, nil
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && a.Port() == b.Port()
}

// classifyContentType 把 Content-Type 归到 html / text，未知类型返回 false。
func classifyContentType(contentType string) (spi.FetchBodyKind, bool) {
	mimeType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch {
	case mimeType == "text/html" || mimeType == "application/xhtml+xml":
		return spi.FetchBodyHTML, true
	case strings.HasPrefix(mimeType, "text/"):
		return spi.FetchBodyText, true
	case mimeType == "application/json" || mimeType == "application/xml",
		strings.HasSuffix(mimeType, "+json"), strings.HasSuffix(mimeType, "+xml"):
		return spi.FetchBodyText, true
	case mimeType == "":
		// 没有 Content-Type 的响应：按纯文本读（官方拒绝，但历史实现接受；
		// 这里保留既有语义，避免把能读的页面判死）。
		return spi.FetchBodyText, true
	}
	return "", false
}

// parseCharset 取 Content-Type 里的 charset 参数（小写）。
func parseCharset(contentType string) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(params["charset"]))
}

// decodeBody 按声明字符集解码；不认识的字符集报 WEB_UNSUPPORTED_CONTENT_TYPE。
func decodeBody(body []byte, charset string) (string, error) {
	switch charset {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return strings.ToValidUTF8(string(body), "\uFFFD"), nil
	case "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252":
		return decodeWindows1252(body), nil
	default:
		return "", spi.NewWebError(spi.WebUnsupportedContentType, "不支持的字符集 %q", charset)
	}
}

// windows1252High 是 0x80–0x9F 在 cp1252 里的码点（其余字节与 Latin-1 相同）。
var windows1252High = [32]rune{
	0x20AC, 0xFFFD, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0xFFFD, 0x017D, 0xFFFD,
	0xFFFD, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0xFFFD, 0x017E, 0x0178,
}

func decodeWindows1252(body []byte) string {
	var builder strings.Builder
	builder.Grow(len(body))
	for _, char := range body {
		if char >= 0x80 && char <= 0x9F {
			builder.WriteRune(windows1252High[char-0x80])
			continue
		}
		builder.WriteRune(rune(char))
	}
	return builder.String()
}

// translateFetchError 把 HTTP 层的失败翻成稳定的 web 码。
func translateFetchError(parent, callCtx context.Context, err error) error {
	if parent.Err() != nil {
		return spi.WrapWebError(spi.WebAborted, parent.Err(), "抓取被取消")
	}
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return spi.WrapWebError(spi.WebFetchTimeout, err, "抓取超时")
	}
	return spi.WrapWebError(spi.WebProviderError, err, "抓取请求失败")
}
