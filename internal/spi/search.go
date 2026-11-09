package spi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"dshgo/internal/kernel"
)

// 这一文件是官方 0.2.2 `dsh-web` 的宿主形状：**一个 web 能力接缝，两个独立的
// provider 注册表（搜索 / 抓取）+ 一条与注册顺序无关的选择规则**。
//
// 为什么搜索与抓取共享一个 seam：provider 选择、取消、错误码与结果上限应该
// 只有一个 owner；两者的请求/结果形状仍然分开（SearchRequest/SearchResult 与
// FetchRequest/FetchResult 互不混用）。
//
// 与官方的差异（都是宿主实际约束，不是简化）：
//   - 没有 provider 变更事件，也没有 capability-status 查询（官方也承认没有）；
//   - `ready()` 只做本地凭据检查，不联网（与官方一致）。
//
// 关键产品语义：**没有可用 provider 时，任何调用都必须结构化报错**，
// 绝不返回"搜过了，没结果"或一个空 FetchResult——那会让模型以为互联网上真的
// 没有，而真实原因是宿主没接能力。

// WebError 是 web 能力的结构化错误。
//
// code 是开放字符串（消费方必须容忍 provider 私有码）；下面的常量是共享码。
type WebError struct {
	Code    string
	Message string
	Err     error
}

func (e *WebError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *WebError) Unwrap() error { return e.Err }

// web 错误码（官方 dsh-web / dsh-web-fetch-http / dsh-web-search-deepseek 的共享集合）。
const (
	WebDuplicateProvider             = "WEB_DUPLICATE_PROVIDER"
	WebProviderConfiguredMissing     = "WEB_PROVIDER_CONFIGURED_MISSING"
	WebProviderConfiguredUnavailable = "WEB_PROVIDER_CONFIGURED_UNAVAILABLE"
	WebProviderAmbiguous             = "WEB_PROVIDER_AMBIGUOUS"
	WebProviderUnavailable           = "WEB_PROVIDER_UNAVAILABLE"
	WebProviderCredentialMissing     = "WEB_PROVIDER_CREDENTIAL_MISSING"
	WebProviderError                 = "WEB_PROVIDER_ERROR"
	WebAborted                       = "WEB_ABORTED"
	WebInvalidURL                    = "WEB_INVALID_URL"
	WebBlockedURL                    = "WEB_BLOCKED_URL"
	WebRedirectBlocked               = "WEB_REDIRECT_BLOCKED"
	// WebSearchTimeout 是搜索自己的协作预算用完（官方 tool.web.searchTimeoutMs）。
	// 它必须与 WebAborted 分开：预算是**工具层**的一次决定，取消是**调用方**的决定，
	// 模型下一步做什么完全不同（调预算 / 换查询 vs 重新发一次）。
	WebSearchTimeout          = "WEB_SEARCH_TIMEOUT"
	WebFetchTimeout           = "WEB_FETCH_TIMEOUT"
	WebFetchTooLarge          = "WEB_FETCH_TOO_LARGE"
	WebUnsupportedContentType = "WEB_UNSUPPORTED_CONTENT_TYPE"
)

// NewWebError 造一个带码的结构化错误。
func NewWebError(code, format string, args ...any) *WebError {
	return &WebError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WrapWebError 在保留底层原因的同时给出稳定错误码。
func WrapWebError(code string, err error, format string, args ...any) *WebError {
	return &WebError{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// WebErrorCode 取错误链里的 web 码；不是 WebError 时返回 ""。
func WebErrorCode(err error) string {
	for err != nil {
		if webErr, ok := err.(*WebError); ok {
			return webErr.Code
		}
		unwrap, ok := err.(interface{ Unwrap() error })
		if !ok {
			return ""
		}
		err = unwrap.Unwrap()
	}
	return ""
}

// IsWebErrorCode 判断错误链里是否带这个码。
func IsWebErrorCode(err error, code string) bool { return WebErrorCode(err) == code }

// SearchProvider 是一个可替换的搜索后端。
//
// 一次调用只问**一个**查询（与官方一致）：多查询的并发、合并、去重与上限
// 是工具层的事，不是 provider 的事——否则每个 provider 都要各写一份合并逻辑。
type SearchProvider interface {
	// ID 是注册键；同名重复注册是错误。
	ID() string
	// Available 是**本地**可用性检查（如凭据是否就绪），绝不联网。
	Available() bool
	// Search 返回这一条查询的合并结果；limit 是调用方期望的上限（provider 可以
	// 多返回，seam 负责截断并置 Truncated）。
	Search(ctx context.Context, query string, limit int) (SearchResult, error)
}

// SearchResult 是一次搜索的结果（官方 WebSearchResult）。
type SearchResult struct {
	// Answer 是 provider 给的摘要答案；没有就是空。
	Answer string
	// Sources 是来源列表，顺序即相关性顺序。
	Sources []SearchSource
	// Truncated 表示 provider 自己截断过（seam 截断也会置位）。
	Truncated bool
}

// SearchSource 是一条引用来源（官方 WebSource）。
type SearchSource struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	Summary     string `json:"summary,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
}

// FetchProvider 是一个可替换的抓取后端。
type FetchProvider interface {
	ID() string
	Available() bool
	// Fetch 取一个 URL。非 2xx 是**结果**而不是错误（官方语义：状态码是内容的一部分），
	// 只有无法发起/无法解码/超限才报 WebError。
	Fetch(ctx context.Context, target string) (FetchResult, error)
}

// FetchBodyKind 是抓取正文的闭集。
type FetchBodyKind string

const (
	// FetchBodyHTML 是 HTML 正文，由工具层转成模型可读文本。
	FetchBodyHTML FetchBodyKind = "html"
	// FetchBodyText 是纯文本/JSON/XML 等，原样交给模型。
	FetchBodyText FetchBodyKind = "text"
)

// FetchResult 是一次抓取的结果（官方 WebFetchResult）。
type FetchResult struct {
	// URL 是最终 URL（跟随同源重定向之后）。
	URL string
	// StatusCode 是最终响应的状态码；非 2xx 同样是结果。
	StatusCode int
	// Body 是解码后的正文。
	Body struct {
		Kind    FetchBodyKind
		Content string
	}
	// ContentType 是最终响应的原始 Content-Type（宿主诊断用；官方 seam 不含它）。
	ContentType string
	// Bytes 是实际读入的响应体字节数（截断前）。
	Bytes int
	// Truncated 表示正文被字节/字符上限截断。
	Truncated bool
}

// WebProviders 是搜索与抓取两个注册表 + 选择规则。
type WebProviders struct {
	mu       sync.RWMutex
	search   map[string]SearchProvider
	fetch    map[string]FetchProvider
	searchID string
	fetchID  string
}

// NewWebProviders 造一个空接缝；searchPin/fetchPin 是配置里钉死的 provider id
// （空 = 未钉死，自动选择唯一的可用 provider）。
func NewWebProviders(searchPin, fetchPin string) *WebProviders {
	return &WebProviders{
		search:   map[string]SearchProvider{},
		fetch:    map[string]FetchProvider{},
		searchID: strings.TrimSpace(searchPin),
		fetchID:  strings.TrimSpace(fetchPin),
	}
}

// RegisterSearch 注册搜索 provider；重复 id 是错误。
func (w *WebProviders) RegisterSearch(provider SearchProvider) error {
	if provider == nil {
		return fmt.Errorf("web: 注册了 nil 搜索 provider")
	}
	id := strings.TrimSpace(provider.ID())
	if id == "" || id != provider.ID() {
		return NewWebError(WebDuplicateProvider, "web: 搜索 provider 的 id 不能为空或含首尾空白: %q", provider.ID())
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, dup := w.search[id]; dup {
		return NewWebError(WebDuplicateProvider, "web: 搜索 provider %q 已注册，不允许重复", id)
	}
	w.search[id] = provider
	return nil
}

// RegisterFetch 注册抓取 provider；重复 id 是错误。
func (w *WebProviders) RegisterFetch(provider FetchProvider) error {
	if provider == nil {
		return fmt.Errorf("web: 注册了 nil 抓取 provider")
	}
	id := strings.TrimSpace(provider.ID())
	if id == "" || id != provider.ID() {
		return NewWebError(WebDuplicateProvider, "web: 抓取 provider 的 id 不能为空或含首尾空白: %q", provider.ID())
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, dup := w.fetch[id]; dup {
		return NewWebError(WebDuplicateProvider, "web: 抓取 provider %q 已注册，不允许重复", id)
	}
	w.fetch[id] = provider
	return nil
}

// Search 跑一条查询并强制 maxResults 上限（官方 capSources 语义）。
func (w *WebProviders) Search(ctx context.Context, query string, maxResults int) (SearchResult, error) {
	provider, err := w.SelectSearch()
	if err != nil {
		return SearchResult{}, err
	}
	result, err := provider.Search(ctx, query, maxResults)
	if err != nil {
		return SearchResult{}, err
	}
	if maxResults > 0 && len(result.Sources) > maxResults {
		result.Sources = result.Sources[:maxResults]
		result.Truncated = true
	}
	return result, nil
}

// Fetch 取一个 URL；provider 选择失败时结构化报错。
func (w *WebProviders) Fetch(ctx context.Context, target string) (FetchResult, error) {
	provider, err := w.SelectFetch()
	if err != nil {
		return FetchResult{}, err
	}
	return provider.Fetch(ctx, target)
}

// SelectSearch 按官方规则选搜索 provider（执行期解析，永不依赖注册顺序）。
func (w *WebProviders) SelectSearch() (SearchProvider, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.searchID != "" {
		provider, ok := w.search[w.searchID]
		if !ok {
			return nil, NewWebError(WebProviderConfiguredMissing,
				"web: 配置的搜索 provider %q 没有注册（已注册: %s）", w.searchID, strings.Join(webSearchIDs(w.search), ", "))
		}
		if !provider.Available() {
			return nil, NewWebError(WebProviderConfiguredUnavailable,
				"web: 配置的搜索 provider %q 已注册但当前不可用（缺凭据或配置）", w.searchID)
		}
		return provider, nil
	}
	var usable []SearchProvider
	for _, provider := range w.search {
		if provider.Available() {
			usable = append(usable, provider)
		}
	}
	if len(usable) == 0 {
		return nil, NewWebError(WebProviderUnavailable, "web: 没有可用的搜索 provider")
	}
	if len(usable) > 1 {
		ids := make([]string, 0, len(usable))
		for _, provider := range usable {
			ids = append(ids, provider.ID())
		}
		sort.Strings(ids)
		return nil, NewWebError(WebProviderAmbiguous,
			"web: 有多个可用搜索 provider（%s）；请显式配置一个", strings.Join(ids, ", "))
	}
	return usable[0], nil
}

// SelectFetch 按同一规则选抓取 provider。
func (w *WebProviders) SelectFetch() (FetchProvider, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.fetchID != "" {
		provider, ok := w.fetch[w.fetchID]
		if !ok {
			return nil, NewWebError(WebProviderConfiguredMissing,
				"web: 配置的抓取 provider %q 没有注册（已注册: %s）", w.fetchID, strings.Join(webFetchIDs(w.fetch), ", "))
		}
		if !provider.Available() {
			return nil, NewWebError(WebProviderConfiguredUnavailable,
				"web: 配置的抓取 provider %q 已注册但当前不可用", w.fetchID)
		}
		return provider, nil
	}
	var usable []FetchProvider
	for _, provider := range w.fetch {
		if provider.Available() {
			usable = append(usable, provider)
		}
	}
	if len(usable) == 0 {
		return nil, NewWebError(WebProviderUnavailable, "web: 没有可用的抓取 provider")
	}
	if len(usable) > 1 {
		ids := make([]string, 0, len(usable))
		for _, provider := range usable {
			ids = append(ids, provider.ID())
		}
		sort.Strings(ids)
		return nil, NewWebError(WebProviderAmbiguous,
			"web: 有多个可用抓取 provider（%s）；请显式配置一个", strings.Join(ids, ", "))
	}
	return usable[0], nil
}

// SearchReady 报告"现在这一步能不能用 web_search"。
// 选择失败回答 false（错误会在真正调用时结构化报出，这里只用于工具可见性）。
func (w *WebProviders) SearchReady() bool {
	provider, err := w.SelectSearch()
	return err == nil && provider.Available()
}

// FetchReady 报告"现在这一步能不能用 web_fetch"。
func (w *WebProviders) FetchReady() bool {
	provider, err := w.SelectFetch()
	return err == nil && provider.Available()
}

// SearchProviderIDs 返回已注册的搜索 provider id（排序，诊断用）。
func (w *WebProviders) SearchProviderIDs() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return webSearchIDs(w.search)
}

// FetchProviderIDs 返回已注册的抓取 provider id（排序，诊断用）。
func (w *WebProviders) FetchProviderIDs() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return webFetchIDs(w.fetch)
}

func webSearchIDs(providers map[string]SearchProvider) []string {
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func webFetchIDs(providers map[string]FetchProvider) []string {
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// WebSearchPinFromEnv 读取钉死的搜索 provider id。
//
// 两个前缀都认：宿主自己的 DSHD_* 与官方 DSH_WEB_SEARCH_PROVIDER 同名变量，
// 它们喂的是同一个字段（不是隐藏的优先级链）。
func WebSearchPinFromEnv(getenv func(string) string) string {
	if getenv == nil {
		return ""
	}
	for _, name := range []string{"DSHD_WEB_SEARCH_PROVIDER", "DSH_WEB_SEARCH_PROVIDER"} {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// WebFetchPinFromEnv 读取钉死的抓取 provider id（同样两个前缀）。
func WebFetchPinFromEnv(getenv func(string) string) string {
	if getenv == nil {
		return ""
	}
	for _, name := range []string{"DSHD_WEB_FETCH_PROVIDER", "DSH_WEB_FETCH_PROVIDER"} {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// WebKey 提供服务：网页搜索 + 抓取接缝（官方 ctx.web）。
var WebKey = kernel.NewKey[*WebProviders]("web")
