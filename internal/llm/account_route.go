package llm

// 官方 base 插件 #99 `llm-deepseek-account`（包 `@deepseek-ai/dsh-llm-deepseek-account`）
// 在宿主侧的形状：**账号令牌认证的推理路由**。
//
// 官方原件（`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-llm-deepseek-account/src/index.ts`）：
//
//	const PROVIDER = 'deepseek-account'                                   // :18
//	const TOKEN_HEADER = 'x-dsh-auth-token'                              // :20
//	const resolveAuth = async (connection) => {
//	  const token = await resolveDeepSeekAccountToken(ctx, connection.baseURL)
//	  if (token === undefined) throw new LlmError(
//	    'Sign in to DeepSeek to use the account provider. The request destination must allow account authentication.',
//	    'ACCOUNT_SIGN_IN_REQUIRED')                                      // :36
//	  return {
//	    headers: { [TOKEN_HEADER]: token },
//	    onRequestError: async (error) => {
//	      if (error.code === QUOTA_EXCEEDED_CODE) return ...ACCOUNT_QUOTA_EXCEEDED_CODE   // :41-43
//	      if (error.failure.status !== 401) return error
//	      const rejected = new LlmError(error.message, 'ACCOUNT_TOKEN_INVALID', ...)      // :45
//	      await rejectDeepSeekAccountToken(ctx, token)                                    // :46
//	      return rejected
//	    },
//	  }
//	}
//
// 三条不能省的语义：
//
//  1. **目的地栅栏**：令牌只发给 `inferenceOrigin` 那个 origin
//     （`dsh-deepseek-account/src/index.ts:457`：`destination.origin !== this.inferenceOrigin`
//     就返回 undefined）。它不是可选的谨慎，而是"私有部署的令牌绝不外流"的唯一保证。
//  2. **未登录是官方四字段错误**，不是"空请求"：码 `ACCOUNT_SIGN_IN_REQUIRED`、文案逐字。
//  3. **401 作废令牌**：码 `ACCOUNT_TOKEN_INVALID`，并回调宿主去删那份**仍然匹配**的令牌。

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

const (
	// ProviderAPIDeepSeekAccount 是官方账号路由的 provider id（`index.ts:18`）。
	//
	// 它是一条**独立协议 id**（而不是复用 `deepseek-completions`）：协议差异不在
	// JSON 形状上，而在**认证面**上——这条路由没有 api key，只有 `x-dsh-auth-token`。
	// 复用同一个 id 会让"这条模型到底用哪种认证"变成一个看不见的隐式状态。
	ProviderAPIDeepSeekAccount = "deepseek-account"
	// AccountTokenHeader 是账号令牌的请求头名（`index.ts:20`，逐字）。
	AccountTokenHeader = "x-dsh-auth-token"
	// AccountSignInRequiredCode / AccountTokenInvalidCode 是官方两个错误码（`:36`/`:45`）。
	AccountSignInRequiredCode = "ACCOUNT_SIGN_IN_REQUIRED"
	AccountTokenInvalidCode   = "ACCOUNT_TOKEN_INVALID"
	// AccountSignInRequiredMessage 是官方原文（`:36`），**逐字**，不改写。
	AccountSignInRequiredMessage = "Sign in to DeepSeek to use the account provider. " +
		"The request destination must allow account authentication."
)

// AccountAuthError 是账号路由的认证失败。
//
// 与 `GatewayError` 分开：网关错误讲的是"网络/上游怎么坏的"，这里讲的是
// "这次请求根本没有可用的账号凭据"，调用方据此提示登录而不是重试。
type AccountAuthError struct {
	Code    string
	Message string
}

func (e *AccountAuthError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// AccountAuth 是宿主注入的账号认证来源。
//
// 三个字段各自对应官方一段真实行为，缺一个都会退化成"看起来能用"：
//   - InferenceOrigin：令牌允许送达的 origin（`resolveToken` 的栅栏）；
//   - Token：读取当前令牌；第二返回值 false = 未登录/已过期；
//   - Reject：401 之后作废"仍然匹配"的那份令牌（`rejectToken`）。
type AccountAuth struct {
	InferenceOrigin string
	Token           func(ctx context.Context) (string, bool)
	Reject          func(ctx context.Context, token string) error
}

// AllowsDestination 判断一次请求的 baseURL 是否在这个账号的推理 origin 上。
//
// 判据与官方 `resolveToken` 一致（`dsh-deepseek-account/src/index.ts:457`）：
// 比 origin；带凭据的 URL 一律不允许（那种 URL 本身就不该出现在配置里）。
func (a *AccountAuth) AllowsDestination(baseURL string) bool {
	if a == nil {
		return false
	}
	if strings.TrimSpace(a.InferenceOrigin) == "" {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.User != nil {
		return false
	}
	return originOf(parsed) == a.InferenceOrigin
}

func originOf(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}

// AccountAuthErrorFor 把一次认证失败收敛成官方码。空令牌与"目的地不允许账号认证"
// 是**同一个**码与同一段文案——官方就是这么写的，因为它对调用方的下一步是同一件事：去登录。
func AccountAuthErrorFor(reason string) *AccountAuthError {
	message := AccountSignInRequiredMessage
	if strings.TrimSpace(reason) != "" {
		message = fmt.Sprintf("%s（%s）", AccountSignInRequiredMessage, reason)
	}
	return &AccountAuthError{Code: AccountSignInRequiredCode, Message: message}
}

// deepSeekAccountAdapter 是账号路由的协议适配器。
//
// wire 形状与 `deepseek-completions` **完全相同**（同一个 DeepSeek
// chat/completions 兼容面）：差异只在认证头，而认证头由 Client 的 AccountAuth
// 承担，不进协议适配器。这里逐字段转发，不复制一份常量，避免两处漂移。
type deepSeekAccountAdapter struct{}

func (deepSeekAccountAdapter) API() string { return ProviderAPIDeepSeekAccount }

func (deepSeekAccountAdapter) MaxTokensField() string {
	return deepSeekCompletionsAdapter{}.MaxTokensField()
}

func (deepSeekAccountAdapter) RequiredFields() []string {
	return deepSeekCompletionsAdapter{}.RequiredFields()
}

func (deepSeekAccountAdapter) OptionalFields() []string {
	return deepSeekCompletionsAdapter{}.OptionalFields()
}

func (deepSeekAccountAdapter) UsageExtensions() []string {
	return deepSeekCompletionsAdapter{}.UsageExtensions()
}

// AccountRouteProvider 是官方 `llm.listConfigurableProviders` 里那条账号路由的
// 注册表条目（`index.ts:73-74`：provider / displayName / settingsNs / settingsPath）。
//
// settingsNs 取官方插件 id：设置面按插件 id 寻址，取包名会让 UI 找不到它。
func AccountRouteProvider() ConfigurableProvider {
	return ConfigurableProvider{
		Provider:     ProviderAPIDeepSeekAccount,
		DisplayName:  "DeepSeek Account",
		SettingsNS:   "llm-deepseek-account",
		SettingsPath: []string{},
	}
}
