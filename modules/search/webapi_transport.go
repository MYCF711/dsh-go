package search

// 本文件是官方 0.2.2-rc.2 `dsh-web-deepseek-api` 的**调度层**：两个 provider 共用
// 一次 HTTP POST 的凭据选择、取消、错误映射与结果解码。
//
// 权威（只读）：
//   - `official-rc2/.../dsh-web-deepseek-api/src/transport.ts:181-262` `dispatchDeepSeekWeb`
//   - `official-rc2/.../dsh-web-deepseek-api/src/transport.ts:310-347` `authHeaders`
//   - `official-rc2/.../dsh-web-deepseek-api/src/transport.ts:264-299` 端点失败文案
//   - `official-rc2/.../dsh-web-deepseek-api/src/transport.ts:361-431` 取消与 `WEB_ABORTED`
//
// 语义要点（照官方，不"顺手优化"）：
//   - 先解析凭据、再等限速、再发请求；请求失败**不换下一路凭据**（`transport.ts:56-59`）；
//   - 重定向失败（官方 `redirect:'error'`）；
//   - 401 + 账号令牌 → 先撤令牌，再报"重新登录"，且**端点不需要改**；
//   - 取消一律 `WEB_ABORTED`（含取消发生在等限速、解析凭据、读 body 的时候）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"dshgo/internal/spi"
)

const (
	// webAPIMaxResponseBytes 是读响应体的硬上限（防上游把宿主拖进 OOM）。
	webAPIMaxResponseBytes = 8 << 20
	// webAPIDefaultTimeout 是可替换 HTTP 客户端缺失时的默认超时。
	webAPIDefaultTimeout = 30 * time.Second
	// webAPIErrorDetailMaxRunes 是错误体 detail 的截断长度。
	webAPIErrorDetailMaxRunes = 500
)

// errWebAPIRedirect 让 HTTP 客户端拒绝跟随重定向（官方 `redirect: 'error'`）。
var errWebAPIRedirect = errors.New("web-deepseek-api: HTTP redirects are not followed")

// webAPIRequest 是一次已解析的请求（官方 `DeepSeekWebRequest`）。
type webAPIRequest[T any] struct {
	// path 是接在凭据源 base 后面的操作路径（`/search` / `/fetch`）。
	path string
	// body 是 JSON 请求体。
	body any
	// decode 校验并投影响应体。
	decode func(payload []byte) (T, error)
	// guidance 是端点失败时要点名的用户设置。
	guidance webAPIGuidance
	// waitToSend 是"拿到凭据后、发请求前"的等待（限速）；拒绝则不发请求。
	waitToSend func(ctx context.Context) error
}

// webAPICredential 是这次请求实际带上的凭据（官方 `SearchAuth`）。
type webAPICredential struct {
	kind     webAPISourceKind
	endpoint string
	headers  map[string]string
	reject   func(ctx context.Context) error
	// secret 是已发出的凭据值，用于从错误文案里清洗掉它。
	secret string
}

// dispatchWebAPI 用一份凭据快照发一次请求，并投影结果。
func dispatchWebAPI[T any](ctx context.Context, client *http.Client, auth webAPIAuth, request webAPIRequest[T]) (T, error) {
	var zero T
	credential, err := webAPIAuthHeaders(ctx, auth, request.path)
	if err != nil {
		return zero, err
	}
	if request.waitToSend != nil {
		if waitErr := request.waitToSend(ctx); waitErr != nil {
			return zero, webAPIAbortOrError(ctx, waitErr, "DeepSeek web request could not start")
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, webAPIAborted(ctx, err)
	}

	payload, err := json.Marshal(request.body)
	if err != nil {
		return zero, webAPIError(spi.WebProviderError, fmt.Sprintf("DeepSeek web request body could not be encoded: %v", err))
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, credential.endpoint, bytes.NewReader(payload))
	if err != nil {
		return zero, webAPIEndpointError(request.guidance, credential.endpoint,
			fmt.Sprintf("DeepSeek web request could not be built: %v", err), err)
	}
	for name, value := range credential.headers {
		httpRequest.Header.Set(name, value)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", deepSeekWebAPIUserAgent)

	response, err := webAPIClient(client).Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil || isCancellation(err) {
			return zero, webAPIAborted(ctx, err)
		}
		return zero, webAPIEndpointError(request.guidance, credential.endpoint,
			fmt.Sprintf("DeepSeek web request failed: %v", err), err)
	}
	defer response.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(response.Body, webAPIMaxResponseBytes+1))

	if !webAPIResponseOK(response.StatusCode) {
		message := fmt.Sprintf("DeepSeek API error (HTTP %d)", response.StatusCode)
		if detail := webAPIErrorDetail(raw); detail != "" {
			message += ": " + detail
		}
		message = redactSecret(message, credential.secret)
		// 取消发生在读错误体的时候也要报 WEB_ABORTED（官方 `transport.ts:228-236`）。
		if readErr != nil && (ctx.Err() != nil || isCancellation(readErr)) {
			return zero, webAPIAborted(ctx, readErr)
		}
		// 账号令牌被拒：先撤销这个令牌，再告诉用户"重新登录"，端点不需要改
		// （官方 `transport.ts:237-245`）。
		if response.StatusCode == http.StatusUnauthorized && credential.kind == webAPISourceAccount {
			if credential.reject != nil {
				_ = credential.reject(ctx)
			}
			return zero, webAPIError(spi.WebProviderError, message+"\n\nDeepSeek rejected the account sign-in used for"+
				" this web request. Guide the user to sign in to DeepSeek again; the endpoint does not need changing.")
		}
		return zero, webAPIEndpointError(request.guidance, credential.endpoint, message, nil)
	}

	if readErr != nil {
		if ctx.Err() != nil || isCancellation(readErr) {
			return zero, webAPIAborted(ctx, readErr)
		}
		return zero, webAPIEndpointError(request.guidance, credential.endpoint,
			fmt.Sprintf("DeepSeek returned an unprocessable response body: %v", readErr), readErr)
	}
	if err := ctx.Err(); err != nil {
		return zero, webAPIAborted(ctx, err)
	}
	if len(raw) > webAPIMaxResponseBytes {
		return zero, webAPIEndpointError(request.guidance, credential.endpoint,
			fmt.Sprintf("DeepSeek returned a response body larger than %d bytes", webAPIMaxResponseBytes), nil)
	}

	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return zero, webAPIEndpointError(request.guidance, credential.endpoint,
			fmt.Sprintf("DeepSeek returned an unprocessable response body: %v", err), err)
	}
	decoded, err := request.decode(raw)
	if err != nil {
		message := err.Error()
		var webErr *spi.WebError
		if errors.As(err, &webErr) && webErr.Message != "" {
			message = webErr.Message
		}
		return zero, webAPIEndpointError(request.guidance, credential.endpoint, message, err)
	}
	return decoded, nil
}

// webAPIAuthHeaders 按顺序取第一路存在的凭据（官方 `authHeaders`，`transport.ts:310-347`）。
func webAPIAuthHeaders(ctx context.Context, auth webAPIAuth, path string) (webAPICredential, error) {
	if err := ctx.Err(); err != nil {
		return webAPICredential{}, webAPIAborted(ctx, err)
	}
	for _, source := range auth.sources {
		if err := ctx.Err(); err != nil {
			return webAPICredential{}, webAPIAborted(ctx, err)
		}
		switch source.kind {
		case webAPISourceMisconfigured:
			return webAPICredential{}, webAPIError(WebProviderMisconfigured, source.message)
		case webAPISourceAccount:
			endpoint := joinWebAPIPath(source.baseURL, path)
			token, err := resolveWebAPICredential(ctx, func(ctx context.Context) (string, error) {
				if source.resolveToken == nil {
					return "", nil
				}
				return source.resolveToken(ctx, endpoint)
			})
			if err != nil {
				return webAPICredential{}, err
			}
			if token == "" {
				// 没有令牌就换下一路（官方：空令牌等于"这一路没有凭据"）。
				continue
			}
			credential := webAPICredential{
				kind:     webAPISourceAccount,
				endpoint: endpoint,
				headers:  map[string]string{"x-dsh-auth-token": token},
				secret:   token,
			}
			if source.rejectToken != nil {
				reject := source.rejectToken
				credential.reject = func(ctx context.Context) error { return reject(ctx, token) }
			}
			return credential, nil
		case webAPISourceAPIKey:
			key := ""
			if source.keyValue != nil {
				key = source.keyValue()
			}
			if err := ctx.Err(); err != nil {
				return webAPICredential{}, webAPIAborted(ctx, err)
			}
			if key == "" {
				continue
			}
			return webAPICredential{
				kind:     webAPISourceAPIKey,
				endpoint: joinWebAPIPath(source.baseURL, path),
				headers:  map[string]string{"Authorization": "Bearer " + key},
				secret:   key,
			}, nil
		}
	}
	message := auth.missingMessage
	if message == "" {
		message = "DeepSeek web access has no credential to send"
	}
	return webAPICredential{}, webAPIError(spi.WebProviderCredentialMissing, message)
}

// resolveWebAPICredential 在取消信号下读一处凭据（官方 `resolveCredential`，`transport.ts:361-384`）。
func resolveWebAPICredential(ctx context.Context, resolve func(context.Context) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", webAPIAborted(ctx, err)
	}
	value, err := resolve(ctx)
	if err != nil {
		if ctx.Err() != nil || isCancellation(err) {
			return "", webAPIAborted(ctx, err)
		}
		return "", webAPIError(spi.WebProviderError, fmt.Sprintf("DeepSeek web credential resolution failed: %v", err))
	}
	return strings.TrimSpace(value), nil
}

// webAPIClient 返回一个**拒绝重定向**的客户端。
//
// 官方 `fetch(..., {redirect:'error'})`；Go 的默认客户端会跟随重定向，所以这里克隆
// 调用方给的客户端并覆盖 `CheckRedirect`。克隆是为了不改调用方（测试共享的 `httptest` 客户端）。
func webAPIClient(custom *http.Client) *http.Client {
	clone := http.Client{Timeout: webAPIDefaultTimeout}
	if custom != nil {
		clone = *custom
		if clone.Timeout == 0 {
			clone.Timeout = webAPIDefaultTimeout
		}
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return errWebAPIRedirect }
	return &clone
}

// joinWebAPIPath 把操作路径接在 base 后面，避免重复斜杠（官方 `joinPath`，`transport.ts:349-352`）。
func joinWebAPIPath(baseURL, path string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + path
}

// webAPIResponseOK 判断 2xx。
func webAPIResponseOK(status int) bool { return status >= 200 && status < 300 }

// webAPIAborted 是取消的稳定错误（官方 `searchAborted`，`transport.ts:415-425`）。
func webAPIAborted(ctx context.Context, cause error) error {
	if cause == nil {
		cause = ctx.Err()
	}
	return &spi.WebError{Code: spi.WebAborted, Message: "DeepSeek web request aborted", Err: cause}
}

// webAPIAbortOrError 把"等待/预检"阶段的失败归类：取消 → WEB_ABORTED，其他 → WEB_PROVIDER_ERROR。
func webAPIAbortOrError(ctx context.Context, err error, description string) error {
	if ctx.Err() != nil || isCancellation(err) {
		return webAPIAborted(ctx, err)
	}
	return webAPIError(spi.WebProviderError, fmt.Sprintf("%s: %v", description, err))
}

// webAPIError 造一个无端点上下文的稳定错误。
func webAPIError(code, message string) error {
	return &spi.WebError{Code: code, Message: message}
}

// webAPIEndpointError 给"已经开始发请求"的失败补上端点与恢复指引
// （官方 `searchEndpointError`，`transport.ts:273-299`）。
func webAPIEndpointError(guidance webAPIGuidance, endpoint, message string, cause error) error {
	baseURLField := WebAPIPluginName + "." + guidance.baseURLField
	apiKeyEnvField := WebAPIPluginName + "." + guidance.apiKeyEnvField
	recovery := fmt.Sprintf("%s derives this endpoint from the base URL of the DeepSeek model provider whose credential"+
		" it uses. If that endpoint is not intended, guide the user to check that provider's base URL, or to set %s to a"+
		" trusted DeepSeek endpoint and %s to the credential reference of its API key.",
		guidance.subject, baseURLField, apiKeyEnvField)
	if guidance.custom {
		recovery = fmt.Sprintf("%s sends the API key under %s to %s. If that endpoint is not intended, guide the user to"+
			" correct %s and %s, or to remove both so %s uses the DeepSeek credential of the conversation.",
			guidance.subject, apiKeyEnvField, baseURLField, baseURLField, apiKeyEnvField, guidance.subject)
	}
	text := fmt.Sprintf("%s\n\nThe web request used endpoint %q. %s Only the user should choose or change the endpoint.",
		message, endpoint, recovery)
	return &spi.WebError{Code: spi.WebProviderError, Message: text, Err: cause}
}

// webAPIErrorDetail 从错误体里抽一条人话（官方 `transport.ts:219-227`）。
func webAPIErrorDetail(raw []byte) string {
	text := ""
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if detail, ok := parsed["error"].(string); ok && detail != "" {
			text = detail
		} else if nested, ok := parsed["error"].(map[string]any); ok {
			if message, ok := nested["message"].(string); ok && message != "" {
				text = message
			}
		}
		if text == "" {
			if message, ok := parsed["message"].(string); ok && message != "" {
				text = message
			}
		}
	}
	if text == "" {
		text = strings.TrimSpace(string(raw))
	}
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > webAPIErrorDetailMaxRunes {
		text = strings.TrimSpace(string(runes[:webAPIErrorDetailMaxRunes])) + "…"
	}
	return text
}

// redactSecret 把已发出的凭据从文案里清掉（错误体可能回显 key）。
func redactSecret(text, secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}

// isCancellation 判断错误链里是不是取消/超时。
func isCancellation(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
