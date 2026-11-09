// Package webfetch 提供 `web_fetch` 工具：取一个 HTTP(S) URL 的正文，解码成文本。
//
// 结构与官方 `dsh-tool-web` 一致：工具只管 schema、校验、呈现与上限，
// 真正联网的是注册进 `spi.WebProviders` 的抓取 provider（默认 `http`）。
// 换一个抓取后端（企业代理、渲染服务）只需注册新的 FetchProvider。
//
// 边界感很重要：
//   - 只接受 http/https（file:// 之类直接拒，避免把本地文件当"网页"读）
//   - 有超时、有大小上限——网页可以无限大，模型上下文不行
//   - 非 2xx 不报"成功但空"，而是把状态码和一小段正文一起回去
//   - HTML 转成可读文本再交给模型（旧实现把标签原样倒出去）
package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

var schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "要取的 HTTP(S) URL"}
  },
  "required": ["url"],
  "additionalProperties": false
}`)

const (
	// 正文上限。超出的部分不取——网页可以无限长，模型上下文不行。
	maxBytes = defaultProviderMaxBytes
	// 非 2xx 时回给模型的正文片段（够看清错误页就行）。
	maxErrSnippet = 500
)

// Tool 是模型可见的 `web_fetch`。
type Tool struct {
	// Client 可替换 HTTP 客户端；只在没有注入 seam 时用来造默认 provider
	// （单测与旧调用点仍可直接 &Tool{}）。
	Client *http.Client
	// Web 是注入的 web 能力接缝；nil 时按默认 http provider 现场造一个。
	Web *spi.WebProviders
}

func (t *Tool) Name() string            { return "web_fetch" }
func (t *Tool) Caps() spi.Caps          { return spi.Caps{Network: true} }
func (t *Tool) Schema() json.RawMessage { return schema }
func (t *Tool) Description() string {
	return "取一个 HTTP(S) 网页的正文并解码成文本。要看具体某个链接的内容时用。"
}

type args struct {
	URL string `json:"url"`
}

type data struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Bytes       int    `json:"bytes"`
	Truncated   bool   `json:"truncated"`
}

// seam 返回本次调用使用的接缝：注入的优先，否则用默认 http provider。
func (t *Tool) seam() (*spi.WebProviders, error) {
	if t.Web != nil {
		return t.Web, nil
	}
	local := spi.NewWebProviders("", "")
	if err := local.RegisterFetch(&HTTPProvider{Client: t.Client}); err != nil {
		return nil, err
	}
	return local, nil
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"url": "https://…"}`}, err
	}
	target := strings.TrimSpace(a.URL)
	if target == "" {
		return spi.Result{Hint: "url 不能为空：给完整地址，含 https://"}, fmt.Errorf("url 为空")
	}
	u, err := url.Parse(target)
	if err != nil {
		return spi.Result{Hint: "URL 解析不了：检查是否有空格或中文未转义"}, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return spi.Result{Hint: fmt.Sprintf("只支持 http/https（给的是 %q）；本地文件用 read", u.Scheme)},
			fmt.Errorf("不支持的协议: %s", u.Scheme)
	}
	if u.Host == "" {
		return spi.Result{Hint: "URL 里没有主机名：写成 https://example.com/path 这种形式"},
			fmt.Errorf("缺主机名")
	}

	seam, err := t.seam()
	if err != nil {
		return spi.Result{Hint: "宿主没有装配抓取后端：需要 web 能力（search 模块）"}, err
	}
	result, err := seam.Fetch(ctx, target)
	if err != nil {
		return spi.Result{Hint: fetchHint(err)}, err
	}

	text := result.Body.Content
	if result.Body.Kind == spi.FetchBodyHTML {
		text = htmlToMarkdown(text)
	}

	if result.StatusCode < 200 || result.StatusCode >= 300 {
		snippet := text
		if runes := []rune(snippet); len(runes) > maxErrSnippet {
			snippet = string(runes[:maxErrSnippet]) + "…"
		}
		hint := "非 2xx：先看状态码——404 是地址不对，403 多半要登录/被挡，5xx 是对方的问题"
		if result.StatusCode == 401 || result.StatusCode == 403 {
			hint = "被拒绝：这个页面可能要登录或有反爬；换官方的原始地址（如 raw.githubusercontent.com）"
		}
		return spi.Result{
			Summary: fmt.Sprintf("HTTP %d（%d 字节）", result.StatusCode, result.Bytes),
			Text:    snippet,
			Data: data{
				URL: result.URL, Status: result.StatusCode,
				ContentType: result.ContentType, Bytes: result.Bytes, Truncated: result.Truncated,
			},
			Hint: hint,
		}, fmt.Errorf("HTTP %d", result.StatusCode)
	}

	summary := fmt.Sprintf("HTTP %d（%d 字节", result.StatusCode, result.Bytes)
	if result.Truncated {
		summary += "，已截断"
	}
	summary += "）"

	return spi.Result{
		Summary: summary,
		Text:    text,
		Data: data{
			URL: result.URL, Status: result.StatusCode,
			ContentType: result.ContentType, Bytes: result.Bytes, Truncated: result.Truncated,
		},
		Hint: truncHint(result.Truncated),
	}, nil
}

// fetchHint 把结构化 web 码翻成"下一步做什么"。
func fetchHint(err error) string {
	switch spi.WebErrorCode(err) {
	case spi.WebInvalidURL:
		return "URL 不合法：给完整的 http(s) 地址；本地文件用 read"
	case spi.WebBlockedURL:
		return "URL 里不允许带用户名/口令：改成公开地址"
	case spi.WebRedirectBlocked:
		return "重定向被拒（跨源或超过跳数）：直接对你最终想读的那个 URL 再发一次请求"
	case spi.WebFetchTimeout:
		return "抓取超时：这个页面太慢或太大；换更具体的地址（锚点/API），或稍后重试"
	case spi.WebFetchTooLarge:
		return "响应超过上限：换更具体的地址（锚点/API），别指望一次取全"
	case spi.WebUnsupportedContentType:
		return "这个 URL 不是可读文本（图片/PDF/二进制/未知字符集）：换它的 HTML 或 JSON 原始地址"
	case spi.WebAborted:
		return "抓取被取消：需要就重新发一次"
	case spi.WebProviderUnavailable, spi.WebProviderConfiguredMissing, spi.WebProviderConfiguredUnavailable:
		return "宿主没有可用的抓取后端：检查 web provider 配置（DSHD_WEB_FETCH_PROVIDER）"
	default:
		return "连不上：确认域名拼写、网络可达；内网地址可能被策略挡住"
	}
}

func truncHint(truncated bool) string {
	if truncated {
		return fmt.Sprintf("正文超过 %d 字节已截断：需要更多就换更具体的地址（锚点/API），别指望一次取全", maxBytes)
	}
	return ""
}

// Module 返回装配用的模块描述。
//
// 它把一个 `http` 抓取 provider 注册进 search 模块提供的 web 接缝，再注册工具；
// 两个动作都在同一作用域里，卸载即撤销。
func Module() kernel.Module {
	return kernel.Module{
		ID:       "tool-webfetch",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools", "web"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			seam, err := kernel.Inject(s, spi.WebKey)
			if err != nil {
				return err
			}
			if err := seam.RegisterFetch(&HTTPProvider{}); err != nil {
				return err
			}
			return reg.Register(&Tool{Web: seam})
		},
	}
}
