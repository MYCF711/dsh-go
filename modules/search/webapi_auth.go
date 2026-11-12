package search

// 本文件是官方 0.2.2-rc.2 `dsh-web-deepseek-api` 的**凭据决策树**：
// 一次 search/fetch 该把哪一路凭据发到哪个端点，逐条对照官方源码。
//
// 权威（只读）：
//   - `official-rc2/.../dsh-web-deepseek-api/src/index.ts:214-238` `resolveTarget`
//   - `official-rc2/.../dsh-web-deepseek-api/src/index.ts:247-259` `ruleSources`（规则→有序凭据）
//   - `official-rc2/.../dsh-web-deepseek-api/src/index.ts:266-298` official / account / api-key 三种源
//   - `official-rc2/.../dsh-web-deepseek-api/src/index.ts:300-315` 路由设置解析失败 = misconfigured
//   - `official-rc2/.../dsh-web-deepseek-api/src/index.ts:324-353` 缺凭据的诊断文案
//   - `official-rc2-docs/.../upgrade-guide/v0.2.2-rc.1/web-search-billing/guide.md:12`
//
// 规则表（官方 `ruleSources`，一字不改地照搬）：
//
//	account-conversation（auto + 会话走 deepseek-account）→ 账号, 然后 API key
//	official-conversation（auto + 会话走 deepseek-official）→ 只 API key
//	other-conversation（auto + 其他/无路由）              → API key, 然后账号
//	account（显式）                                       → 只账号
//	api-key（显式）                                       → 只 API key
//
// 能力自有 `*BaseURL` / `*ApiKeyEnv` 非空时**整体接管**，不看 billing（`index.ts:221-231`）。

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// web 能力的官方扩展错误码（官方 `transport.ts:317,343-346`）。
//
// `internal/spi` 只列共享码，并明确"code 是开放字符串（消费方必须容忍 provider 私有码）"，
// 所以 provider 私有码定义在这里，不动接缝。
const (
	// WebProviderMisconfigured 是某个凭据源的设置解析不出来（官方 `WEB_PROVIDER_MISCONFIGURED`）。
	WebProviderMisconfigured = "WEB_PROVIDER_MISCONFIGURED"
)

// webAPICapability 是一处能力在官方文案与错误指引里的三元组（`index.ts:145-148`）。
type webAPICapability struct {
	// name 是能力名（诊断用）。
	name string
	// subject 是文案里的主语（`Search` / `Fetch`）。
	subject string
	// prefix 是配置字段前缀（`search` / `fetch`）。
	prefix string
	// path 是接在 base 后面的操作路径。
	path string
}

var (
	webAPISearchCapability = webAPICapability{name: "search", subject: "Search", prefix: "search", path: "/search"}
	webAPIFetchCapability  = webAPICapability{name: "fetch", subject: "Fetch", prefix: "fetch", path: "/fetch"}
)

// webAPISourceKind 是凭据来源的种类（官方 `DeepSeekWebAuthSource` 的三态）。
type webAPISourceKind int

const (
	// webAPISourceAPIKey 是 API key 源：`Authorization: Bearer`。
	webAPISourceAPIKey webAPISourceKind = iota
	// webAPISourceAccount 是登录账号源：`x-dsh-auth-token`。
	webAPISourceAccount
	// webAPISourceMisconfigured 是设置解析不出来的源：走到它就失败。
	webAPISourceMisconfigured
)

// webAPISource 是一路凭据（官方 `DeepSeekWebAuthSource` 的宿主形状）。
type webAPISource struct {
	kind webAPISourceKind
	// baseURL 是这路凭据要发去的 API base（含路径前缀，如 `/v1/tools`）。
	baseURL string
	// keyRef 是 API key 的凭据引用名（官方 `CredentialRef`）。
	keyRef string
	// keyValue 读当前 key 值（空 = 没有 key）。每次都重新读，不缓存。
	keyValue func() string
	// accountConfigured 回答"有没有可申请的账号令牌"，不读值。
	accountConfigured func() bool
	// resolveToken 为目标端点申请令牌；空串 = 没有令牌。
	resolveToken func(ctx context.Context, endpoint string) (string, error)
	// rejectToken 撤销被 401 拒绝的那一个令牌。
	rejectToken func(ctx context.Context, token string) error
	// message 是 misconfigured 源给模型的诊断。
	message string
}

// webAPIAuth 是一次操作的有序凭据列表（官方 `DeepSeekWebAuth`）。
type webAPIAuth struct {
	sources []webAPISource
	// missingMessage 是"一路都取不到凭据"时的模型可见诊断。
	missingMessage string
}

// webAPIGuidance 是端点失败时要点名的用户设置（官方 `DeepSeekWebGuidance`）。
type webAPIGuidance struct {
	subject string
	// custom 表示端点是能力自有 baseURL（而不是从模型路由推导出来的）。
	custom bool
	// baseURLField / apiKeyEnvField 是官方配置字段名（`searchBaseURL` 等）。
	baseURLField   string
	apiKeyEnvField string
}

// webAPIRule 是给一次操作的凭据排序的规则（官方 `Rule`）。
type webAPIRule string

const (
	ruleAccountConversation  webAPIRule = "account-conversation"
	ruleOfficialConversation webAPIRule = "official-conversation"
	ruleOtherConversation    webAPIRule = "other-conversation"
	ruleAccount              webAPIRule = "account"
	ruleAPIKey               webAPIRule = "api-key"
)

// 官方两条 DeepSeek 模型路由的 provider id（`index.ts:132-136`）。
const (
	webAPIRouteAccount  = "deepseek-account"
	webAPIRouteOfficial = "deepseek-official"
)

// resolveWebAPITarget 解析一处能力的端点与有序凭据（官方 `resolveTarget`）。
func resolveWebAPITarget(capability webAPICapability, options DeepSeekWebAPIOptions) (webAPIAuth, webAPIGuidance) {
	guidance := webAPIGuidance{
		subject:        capability.subject,
		baseURLField:   capability.prefix + "BaseURL",
		apiKeyEnvField: capability.prefix + "ApiKeyEnv",
	}
	ownBaseURL := nonEmpty(options.Config.BaseURL)
	ownRef := nonEmpty(options.Config.ApiKeyEnv)
	if ownBaseURL != "" || ownRef != "" {
		// 自有端点/凭据：整体接管，不看 billing（官方 `index.ts:221-231`）。
		ref := ownRef
		if ref == "" {
			ref = DeepSeekAPIKeyRef
		}
		base := ownBaseURL
		if base == "" {
			base = options.officialBaseURL()
		}
		source := apiKeySource(capability, base, ref, options)
		return webAPIAuth{
			sources:        []webAPISource{source},
			missingMessage: ownMissingMessage(capability, ref),
		}, withCustom(guidance)
	}

	rule := ruleFor(options.Config)
	sources := ruleSources(capability, rule, options)
	ref := DeepSeekAPIKeyRef
	for _, source := range sources {
		if source.kind == webAPISourceAPIKey && source.keyRef != "" {
			ref = source.keyRef
			break
		}
	}
	return webAPIAuth{sources: sources, missingMessage: missingMessage(capability, rule, ref)}, guidance
}

// withCustom 标记"端点来自能力自有设置"。
func withCustom(guidance webAPIGuidance) webAPIGuidance {
	guidance.custom = true
	return guidance
}

// ruleFor 是 auto 的会话级判定（官方 `index.ts:232-234`）。
func ruleFor(config WebAPIConfig) webAPIRule {
	if config.Billing != "" && config.Billing != BillingAuto {
		if config.Billing == BillingAccount {
			return ruleAccount
		}
		return ruleAPIKey
	}
	switch config.LLMProvider {
	case webAPIRouteAccount:
		return ruleAccountConversation
	case webAPIRouteOfficial:
		return ruleOfficialConversation
	default:
		return ruleOtherConversation
	}
}

// ruleSources 是规则到有序凭据的映射（官方 `ruleSources`，`index.ts:247-259`）。
func ruleSources(capability webAPICapability, rule webAPIRule, options DeepSeekWebAPIOptions) []webAPISource {
	switch rule {
	case ruleAccountConversation:
		return []webAPISource{accountSource(capability, options), officialSource(capability, options)}
	case ruleOfficialConversation, ruleAPIKey:
		return []webAPISource{officialSource(capability, options)}
	case ruleOtherConversation:
		return []webAPISource{officialSource(capability, options), accountSource(capability, options)}
	case ruleAccount:
		return []webAPISource{accountSource(capability, options)}
	default:
		return []webAPISource{misconfiguredSource(capability, "",
			fmt.Errorf("unknown billing rule %q", string(rule)))}
	}
}

// officialSource 是 DeepSeek API key 那一路（官方 `officialSource`，`index.ts:266-273`）。
//
// 官方：模型路由已挂载 → 用该路由解析出的 base 的 origin + `/v1/tools`；没挂载 →
// 官方 base + `DEEPSEEK_API_KEY`。本宿主还没有 `deepseek-official` 模型路由插件，
// 因此**恒走"未挂载"分支**（官方 base + DEEPSEEK_API_KEY），除非调用方给了测试端点。
func officialSource(capability webAPICapability, options DeepSeekWebAPIOptions) webAPISource {
	return apiKeySource(capability, options.officialBaseURL(), DeepSeekAPIKeyRef, options)
}

// officialBaseURL 是官方 DeepSeek API（Search/Fetch）的 base。
func (o DeepSeekWebAPIOptions) officialBaseURL() string {
	if base := nonEmpty(o.OfficialBaseURL); base != "" {
		return base
	}
	return DeepSeekWebAPIBaseURL
}

// accountSource 是登录账号那一路（官方 `accountSource`，`index.ts:275-291`）。
//
// 端点 = 账号路由 base 的 origin + `/v1/tools`；路由 base 缺省时认官方提到的
// `DEEPSEEK_BASE_URL`（`index.ts:277-280`），再缺省用官方 origin。
// 本宿主没有账号服务键，`WebAPIAccount` 零值 = 没有令牌（provider 如实报缺凭据）。
func accountSource(capability webAPICapability, options DeepSeekWebAPIOptions) webAPISource {
	base := nonEmpty(options.Account.BaseURL)
	if base == "" {
		base = nonEmpty(options.getenv("DEEPSEEK_BASE_URL"))
	}
	if base == "" {
		base = "https://api.deepseek.com"
	}
	tools, err := toolsBase(base)
	if err != nil {
		return misconfiguredSource(capability, webAPIRouteAccount, err)
	}
	return webAPISource{
		kind:              webAPISourceAccount,
		baseURL:           tools,
		accountConfigured: options.Account.Configured,
		resolveToken:      options.Account.ResolveToken,
		rejectToken:       options.Account.RejectToken,
	}
}

// apiKeySource 是一路 API key 凭据（官方 `apiKeySource`，`index.ts:293-298`）。
func apiKeySource(capability webAPICapability, baseURL, ref string, options DeepSeekWebAPIOptions) webAPISource {
	if _, err := parseAPIBase(baseURL); err != nil {
		// 端点本身不合法：这路凭据到此为止（官方 `available()` 也要求 baseURL 可解析）。
		return webAPISource{
			kind:    webAPISourceMisconfigured,
			baseURL: baseURL,
			message: fmt.Sprintf("%s API base %q is not a usable http(s) URL: %v. Correct %s.%sBaseURL (or the"+
				" DeepSeek model route's base URL it is derived from) before %s can send a request.",
				capability.subject, baseURL, err, WebAPIPluginName, capability.prefix, capability.subject),
		}
	}
	return webAPISource{
		kind:     webAPISourceAPIKey,
		baseURL:  baseURL,
		keyRef:   ref,
		keyValue: func() string { return options.credentialValue(ref) },
	}
}

// credentialValue 读一处凭据的当前值（官方：读一次即用，不保留）。
func (o DeepSeekWebAPIOptions) credentialValue(ref string) string {
	if ref == "" {
		return ""
	}
	value := strings.TrimSpace(o.getenv(ref))
	if value == "" && ref == DeepSeekAPIKeyRef {
		// 宿主既有别名：与 DEEPSEEK_API_KEY 喂同一个字段。
		value = strings.TrimSpace(o.getenv(DeepSeekAPIKeyEnvAlias))
	}
	return value
}

// misconfiguredSource 是设置解析不出来的凭据源（官方 `misconfigured`，`index.ts:300-315`）。
func misconfiguredSource(capability webAPICapability, route string, err error) webAPISource {
	provider, credential := "DeepSeek API-key model provider", "that provider's API key"
	if route == webAPIRouteAccount {
		provider, credential = "DeepSeek account model provider", "the DeepSeek account sign-in"
	}
	return webAPISource{
		kind: webAPISourceMisconfigured,
		message: fmt.Sprintf("DeepSeek %s API sends %s to the origin of the %s, and that provider's settings do not"+
			" resolve: %v. %s did not send the request. Guide the user to correct that provider's settings, or to set"+
			" %s.%sBaseURL and %s.%sApiKeyEnv so %s uses its own endpoint.",
			capability.subject, credential, provider, err, capability.subject,
			WebAPIPluginName, capability.prefix, WebAPIPluginName, capability.prefix, capability.subject),
	}
}

// ready 回答"这次操作有没有一路现在真能取到凭据"。
//
// 与官方的差异（宿主真实约束）：官方 `available()` 只看"源存在"，真正的令牌读取留给
// `ready()`；本宿主的接缝用 `Available()` 做 provider 选择，所以这里直接回答"能不能取到"。
// 净效果一致（官方 `ready()` 也读令牌），但宿主不会把"肯定取不到凭据"的 provider 选上。
func (auth webAPIAuth) ready() bool {
	for _, source := range auth.sources {
		switch source.kind {
		case webAPISourceMisconfigured:
			return false
		case webAPISourceAccount:
			if source.resolveToken != nil && source.accountConfigured != nil && source.accountConfigured() {
				return true
			}
		case webAPISourceAPIKey:
			if source.keyValue != nil && source.keyValue() != "" {
				return true
			}
		}
	}
	return false
}

// sourcesParse 回答"每个非 misconfigured 源的 baseURL 都是可用的 http(s) 端点"。
//
// misconfigured 源不算"可解析"（它会失败），与官方 `available()` 的 `every(...)` 等价。
func (auth webAPIAuth) sourcesParse() bool {
	for _, source := range auth.sources {
		if source.kind == webAPISourceMisconfigured {
			return false
		}
		if _, err := parseAPIBase(source.baseURL); err != nil {
			return false
		}
	}
	return true
}

// toolsBase 把 Messages base URL 的 origin 接上 `/v1/tools`（官方 `toolsBase`，`index.ts:374-377`）。
func toolsBase(messagesBaseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(messagesBaseURL))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("base URL scheme must be http or https: %q", messagesBaseURL)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("base URL has no host: %q", messagesBaseURL)
	}
	return parsed.Scheme + "://" + parsed.Host + webAPIToolsPath, nil
}

// parseAPIBase 校验一个已成形的 API base（含路径前缀）。
func parseAPIBase(baseURL string) (*url.URL, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, errors.New("empty base URL")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, errors.New("missing host")
	}
	return parsed, nil
}

// nonEmpty 把"缺席"和"空串"归一成空串（官方 `nonEmpty`，`index.ts:379-382`）。
func nonEmpty(value string) string { return strings.TrimSpace(value) }

// ownMissingMessage 是能力自有端点缺 key 的诊断（官方 `index.ts:227-229`）。
func ownMissingMessage(capability webAPICapability, ref string) string {
	quoted := fmt.Sprintf("%q", ref)
	return fmt.Sprintf("DeepSeek %s API sends the API key under %s to its own endpoint, and no key is configured for %s."+
		" Guide the user to export %s in the launching environment, or to correct %s.%sApiKeyEnv.",
		capability.subject, quoted, quoted, quoted, WebAPIPluginName, capability.prefix)
}

// missingMessage 是"一路凭据都没有"的诊断（官方 `missingMessage`，`index.ts:324-353`）。
func missingMessage(capability webAPICapability, rule webAPIRule, keyRef string) string {
	quoted := fmt.Sprintf("%q", keyRef)
	billingField := WebAPIPluginName + "." + capability.prefix + "Billing"
	billingEnv := "DSHD_WEB_" + strings.ToUpper(capability.prefix) + "_BILLING"
	settings := "on the Web search and fetch settings page (" + billingEnv + ")"
	addKey := "add the DeepSeek API key on the Models page or export " + quoted + " in the launching environment"
	switch rule {
	case ruleAccountConversation:
		return fmt.Sprintf("DeepSeek %s API uses the DeepSeek account sign-in of this conversation, then the DeepSeek API"+
			" key, and neither is available: no signed-in DeepSeek account is available, and no key is configured for %s."+
			" Guide the user to sign in to DeepSeek, or to %s.", capability.subject, quoted, addKey)
	case ruleOfficialConversation:
		return fmt.Sprintf("DeepSeek %s API uses the DeepSeek API key, as this conversation's model provider does, and no key"+
			" is configured for %s. Guide the user to %s, or to sign in to DeepSeek and set %s to \"account\" %s.",
			capability.subject, quoted, addKey, billingField, settings)
	case ruleOtherConversation:
		return fmt.Sprintf("DeepSeek %s API uses the DeepSeek API key, then the DeepSeek account sign-in, and neither is"+
			" available: no key is configured for %s, and no signed-in DeepSeek account is available. Guide the user to %s,"+
			" or to sign in to DeepSeek.", capability.subject, quoted, addKey)
	case ruleAccount:
		return fmt.Sprintf("DeepSeek %s API bills only the signed-in DeepSeek account, and no signed-in DeepSeek account is"+
			" available. %s did not send the request. Guide the user to sign in to DeepSeek, or to set %s to \"api-key\""+
			" or \"auto\" %s.", capability.subject, capability.subject, billingField, settings)
	case ruleAPIKey:
		return fmt.Sprintf("DeepSeek %s API bills only the DeepSeek API key, and no key is configured for %s. Guide the user"+
			" to %s, or to set %s to \"account\" or \"auto\" %s.",
			capability.subject, quoted, addKey, billingField, settings)
	default:
		return fmt.Sprintf("DeepSeek %s API has no credential to send for rule %q.", capability.subject, string(rule))
	}
}
