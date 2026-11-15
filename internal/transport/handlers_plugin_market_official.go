package transport

// PARITY-W3-MARKET：pluginMarket 4 个方法与 pluginRegistryProbe.fastest 的 L1 契约。
// 真实索引存储/HTTP 在 internal/market；本文件只做官方参数/结果与结构化错误。
//
// # 结果形状 = 官方 typert 的**逐字段**形状
//
// 权威：`@deepseek-ai/dsh-plugin-market/lib/typert.host.js`（`MarketSearchResult` /
// `MarketIndexSummary` / `PluginDetail`）与 `dsh-client-ui-plugin-manager/lib/
// typert.host.js`（`pluginRegistryProbe.fastest` → `string | null`）。
//
// 关键点（此前宿主形状错在这些地方）：
//   - `pluginMarket.search` 回 `{url,title,description?,metadata{categories,labels,
//     sortKeys},fetchedAt,entries[MarketListingEntry],total,nextOffset}` —— 没有
//     `index`/`plugins`/`offset` 这三个自造字段；
//   - `entries[].versions[].compatibility` 是 union 对象（不是 bool）、`install`
//     是 `{spec,registry?}`、`publishTimestamp` 是 Unix **秒**整数；
//   - `listIndexes` 的 `title`/`description` 是 `string | localized` union；
//   - `fastest` **无参数**，结果是 registry URL 字符串或 null。
//
// 参数侧同样按官方 strictObject 校验：必填缺失、未知字段、类型/范围不符一律 400，
// 不做"猜一个默认值"。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
)

const (
	MethodPluginMarketListIndexes     Method = "pluginMarket.listIndexes"
	MethodPluginMarketSearch          Method = "pluginMarket.search"
	MethodPluginMarketReadEntryDetail Method = "pluginMarket.readEntryDetail"
	MethodPluginMarketSetIndexes      Method = "pluginMarket.setIndexes"
	MethodPluginRegistryProbeFastest  Method = "pluginRegistryProbe.fastest"
)

// OfficialLocalizedText 是官方 `string | localized` union。
//
// 下发时单语言回字符串、多语言回对象（客户端两种都收；官方 schema 对对象要求
// 含 `en`，单语言回字符串可以避开"索引只给了别的语言"这种越界形状）。
type OfficialLocalizedText map[string]string

// UnmarshalJSON 接受 union 的两种写法（测试与内部回读用）。
func (t *OfficialLocalizedText) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*t = nil
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*t = OfficialLocalizedText{"en": text}
		return nil
	}
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	*t = values
	return nil
}

// MarshalJSON 按官方 union 下发。
func (t OfficialLocalizedText) MarshalJSON() ([]byte, error) {
	if len(t) == 1 {
		for _, value := range t {
			return json.Marshal(value)
		}
	}
	return json.Marshal(map[string]string(t))
}

// OfficialMarketIndex 是 `pluginMarket.listIndexes` 的一项（官方 MarketIndexSummary）。
type OfficialMarketIndex struct {
	URL         string                `json:"url"`
	Title       OfficialLocalizedText `json:"title,omitempty"`
	Description OfficialLocalizedText `json:"description,omitempty"`
	Error       string                `json:"error,omitempty"`
}

// OfficialMarketDeclaration 是市场声明的一类 id（官方 MarketDeclaration）。
type OfficialMarketDeclaration struct {
	Title OfficialLocalizedText `json:"title"`
}

// OfficialMarketSortKey 是市场声明的排序键（官方 MarketSortKeyDeclaration）。
type OfficialMarketSortKey struct {
	Title OfficialLocalizedText `json:"title"`
	Order string                `json:"order"`
}

// OfficialMarketIndexMetadata 是索引的三张声明表（官方 MarketIndexMetadata）。
// 三组**总是**下发（缺省为空对象），客户端靠它画分类菜单/标签/排序键。
type OfficialMarketIndexMetadata struct {
	Categories map[string]OfficialMarketDeclaration `json:"categories"`
	Labels     map[string]OfficialMarketDeclaration `json:"labels"`
	SortKeys   map[string]OfficialMarketSortKey     `json:"sortKeys"`
}

// OfficialMarketRepository 是插件源码仓库的展示信息（官方 MarketRepository）。
type OfficialMarketRepository struct {
	URL       string `json:"url"`
	Directory string `json:"directory,omitempty"`
}

// OfficialMarketPluginMetadata 是插件的市场元数据（官方 MarketPluginMetadata）。
type OfficialMarketPluginMetadata struct {
	Category string             `json:"category,omitempty"`
	Keywords []string           `json:"keywords"`
	Labels   []string           `json:"labels"`
	SortKeys map[string]float64 `json:"sortKeys"`
}

// OfficialMarketInstall 是"装这一确切版本"的参数（官方 MarketInstallTarget）。
type OfficialMarketInstall struct {
	Spec     string `json:"spec"`
	Registry string `json:"registry,omitempty"`
}

// OfficialMarketEngines 是这一版的运行时要求（官方 MarketEngines）。
type OfficialMarketEngines struct {
	DSH string `json:"dsh,omitempty"`
}

// OfficialMarketSource 是版本来源的官方 union（npm | git）。
type OfficialMarketSource struct {
	Type     string
	Registry string
	URL      string
	Commit   string
	Path     string
}

// MarshalJSON 只下发该 union 分支的字段：npm 分支没有 url/commit/path，git 分支
// 没有 registry。
func (s OfficialMarketSource) MarshalJSON() ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "git":
		object := map[string]string{"type": "git", "url": s.URL, "commit": s.Commit}
		if s.Path != "" {
			object["path"] = s.Path
		}
		return json.Marshal(object)
	default:
		object := map[string]string{"type": "npm"}
		if s.Registry != "" {
			object["registry"] = s.Registry
		}
		return json.Marshal(object)
	}
}

// OfficialMarketCompatibility 是兼容性 union（官方 MarketCompatibility）。
//
// 兼容时只有 `compatible:true`；不兼容时带 runtime 版本、未满足的要求与豁免标记。
type OfficialMarketCompatibility struct {
	Compatible     bool              `json:"compatible"`
	RuntimeVersion string            `json:"runtimeVersion,omitempty"`
	Requirements   map[string]string `json:"requirements,omitempty"`
	Exempted       *bool             `json:"exempted,omitempty"`
}

// OfficialMarketListingVersion 是一行版本（官方 MarketListingVersion）。
type OfficialMarketListingVersion struct {
	Version          string                      `json:"version"`
	PublishTimestamp int64                       `json:"publishTimestamp"`
	Source           OfficialMarketSource        `json:"source"`
	Install          OfficialMarketInstall       `json:"install"`
	Engines          OfficialMarketEngines       `json:"engines"`
	Compatibility    OfficialMarketCompatibility `json:"compatibility"`
	// Update 是官方 0.2.2-rc.2 市场版本行的 `update: boolean`
	// （`dsh-plugin-market/src/index.ts` 的 listingOf）：已装副本来自**完整 commit
	// 钉住的 git spec**、这一版更新、且钉住同仓库同 `path:` 目录时才是 true。
	// **总是显式下发**：缺席与 false 在客户端语义不同（缺席会被读成"老宿主"）。
	Update bool `json:"update"`
}

// OfficialMarketInstallation 是"这一包在 profile 里的状态"（官方 MarketInstallation）。
type OfficialMarketInstallation struct {
	Status           string `json:"status"`
	InstalledVersion string `json:"installedVersion,omitempty"`
}

// OfficialMarketEntry 是搜索结果的一个条目（官方 MarketListingEntry）。
type OfficialMarketEntry struct {
	Name           string                         `json:"name"`
	Title          OfficialLocalizedText          `json:"title,omitempty"`
	Description    OfficialLocalizedText          `json:"description,omitempty"`
	Icon           string                         `json:"icon,omitempty"`
	Author         string                         `json:"author,omitempty"`
	Homepage       string                         `json:"homepage,omitempty"`
	Repository     *OfficialMarketRepository      `json:"repository,omitempty"`
	Metadata       OfficialMarketPluginMetadata   `json:"metadata"`
	Versions       []OfficialMarketListingVersion `json:"versions"`
	DefaultVersion string                         `json:"defaultVersion,omitempty"`
	Installation   OfficialMarketInstallation     `json:"installation"`
}

// OfficialMarketSearchResult 是 `pluginMarket.search` 的结果（官方 MarketSearchResult）。
type OfficialMarketSearchResult struct {
	URL         string                      `json:"url"`
	Title       OfficialLocalizedText       `json:"title"`
	Description OfficialLocalizedText       `json:"description,omitempty"`
	Metadata    OfficialMarketIndexMetadata `json:"metadata"`
	// FetchedAt 是回答这一页的文档抓取时刻（Unix 毫秒）：值不同 = 不同文档。
	FetchedAt int64                 `json:"fetchedAt"`
	Entries   []OfficialMarketEntry `json:"entries"`
	Total     int                   `json:"total"`
	// NextOffset 是下一页的 offset，最后一页为 **null**（官方 union 里有 null，所以
	// 不能用 omitempty：缺席与 null 在客户端语义不同）。
	NextOffset *int `json:"nextOffset"`
}

// OfficialMarketEntryDetail 是 `pluginMarket.readEntryDetail` 的结果（官方 PluginDetail）。
type OfficialMarketEntryDetail struct {
	Status   string `json:"status"`
	Origin   string `json:"origin,omitempty"`
	Markdown string `json:"markdown,omitempty"`
	Document string `json:"document,omitempty"`
}

// OfficialMarketSort 是 `query.sort` 校验后的形状（官方 MarketSort union）。
type OfficialMarketSort struct {
	Kind      string
	ID        string
	Direction string
}

// OfficialMarketQuery 是 `pluginMarket.search` 的 query（官方 MarketQuery）。
type OfficialMarketQuery struct {
	Text      string
	Category  string
	Labels    []string
	Sort      OfficialMarketSort
	Offset    int
	Limit     int
	Refresh   bool
	FetchedAt int64
}

// SortSpec 把校验过的 sort 转成存储层的通用 map（internal/market 不 import transport）。
func (q OfficialMarketQuery) SortSpec() map[string]any {
	spec := map[string]any{"kind": q.Sort.Kind}
	if q.Sort.ID != "" {
		spec["id"] = q.Sort.ID
	}
	if q.Sort.Direction != "" {
		spec["direction"] = q.Sort.Direction
	}
	return spec
}

type OfficialPluginMarketAPI interface {
	ListIndexes(context.Context) ([]OfficialMarketIndex, error)
	SetIndexes(context.Context, []string) error
	Search(context.Context, string, OfficialMarketQuery) (OfficialMarketSearchResult, error)
	ReadEntryDetail(context.Context, string, string, string, string) (OfficialMarketEntryDetail, error)
}

// OfficialPluginRegistryProbeAPI 是官方 `pluginRegistryProbe.fastest`：**无参数**，
// 结果是"最先响应的 registry URL"或空串（官方 null）。
type OfficialPluginRegistryProbeAPI interface {
	Fastest(context.Context) (string, error)
}

type PluginMarketError struct {
	Status  int
	Code    string
	Message string
}

func (e *PluginMarketError) Error() string { return e.Message }

var pluginMarketMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialPluginMarketAPI
}{m: map[*Server]OfficialPluginMarketAPI{}}
var pluginRegistryProbeMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialPluginRegistryProbeAPI
}{m: map[*Server]OfficialPluginRegistryProbeAPI{}}

func (s *Server) AttachOfficialPluginMarketAPI(api OfficialPluginMarketAPI) {
	pluginMarketMount.mu.Lock()
	defer pluginMarketMount.mu.Unlock()
	pluginMarketMount.m[s] = api
}
func (s *Server) DetachOfficialPluginMarketAPI() {
	pluginMarketMount.mu.Lock()
	defer pluginMarketMount.mu.Unlock()
	delete(pluginMarketMount.m, s)
}
func (s *Server) AttachOfficialPluginRegistryProbeAPI(api OfficialPluginRegistryProbeAPI) {
	pluginRegistryProbeMount.mu.Lock()
	defer pluginRegistryProbeMount.mu.Unlock()
	pluginRegistryProbeMount.m[s] = api
}
func (s *Server) DetachOfficialPluginRegistryProbeAPI() {
	pluginRegistryProbeMount.mu.Lock()
	defer pluginRegistryProbeMount.mu.Unlock()
	delete(pluginRegistryProbeMount.m, s)
}

func IsOfficialPluginMarketAPIMethod(name string) bool {
	switch Method(name) {
	case MethodPluginMarketListIndexes, MethodPluginMarketSearch,
		MethodPluginMarketReadEntryDetail, MethodPluginMarketSetIndexes:
		return true
	}
	return false
}

func (s *Server) ServeOfficialPluginMarketAPI(w http.ResponseWriter, r *http.Request, name string) {
	api := pluginMarketMount.m[s]
	if api == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBodyFor("registry_unavailable",
			fmt.Sprintf("%s 未接线：宿主没有 AttachOfficialPluginMarketAPI", name)))
		return
	}
	params, err := decodeMarketParams(r)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	ctx := r.Context()
	var result any
	switch Method(name) {
	case MethodPluginMarketListIndexes:
		if err := params.finish(); err != nil {
			writeMarketError(w, err)
			return
		}
		items, err := api.ListIndexes(ctx)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		if items == nil {
			items = []OfficialMarketIndex{}
		}
		result = items
	case MethodPluginMarketSetIndexes:
		urls, err := params.strings("urls", true)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeMarketError(w, err)
			return
		}
		if err := api.SetIndexes(ctx, urls); err != nil {
			writeMarketError(w, err)
			return
		}
		// 官方结果类型是 z.void()：没有载荷。本仓 writeJSON 不接受 `{"result":null}`
		// （会被判成静默成功 → 500），所以用空对象表达"这次调用没有载荷"
		// （与 commands.execute / agentTeams.setEnabled 同一条约定）。
		result = map[string]any{}
	case MethodPluginMarketSearch:
		rawURL, err := params.String("url", true)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		query, err := params.marketQuery()
		if err != nil {
			writeMarketError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeMarketError(w, err)
			return
		}
		search, err := api.Search(ctx, rawURL, query)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		result = search
	case MethodPluginMarketReadEntryDetail:
		rawURL, err := params.String("url", true)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		entryName, err := params.String("name", true)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		version, err := params.String("version", true)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		language, err := params.String("language", true)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		if err := params.detailOptions(); err != nil {
			writeMarketError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeMarketError(w, err)
			return
		}
		detail, err := api.ReadEntryDetail(ctx, rawURL, entryName, version, language)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		result = detail
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (s *Server) ServeOfficialPluginRegistryProbeAPI(w http.ResponseWriter, r *http.Request) {
	api := pluginRegistryProbeMount.m[s]
	if api == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBodyFor("registry_unavailable",
			"pluginRegistryProbe.fastest 未接线：宿主没有 AttachOfficialPluginRegistryProbeAPI"))
		return
	}
	params, err := decodeMarketParams(r)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	// 官方这个方法**没有参数**：多给任何字段都是客户端接错，400 而不是忽略。
	if err := params.finish(); err != nil {
		writeMarketError(w, err)
		return
	}
	fastest, err := api.Fastest(r.Context())
	if err != nil {
		writeMarketError(w, err)
		return
	}
	if strings.TrimSpace(fastest) == "" {
		// 官方结果是 `string | null`，这里的 null 是**值**（"没有更快的 registry"），
		// 不是"没有载荷"；与 directoryPicker.pick 的取消同一类，所以显式走
		// writeOfficialNullResult（writeJSON 的"空载荷"兜底会把 null 判成 500）。
		writeOfficialNullResult(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": fastest})
}

// marketParams 是"裸 JSON 对象"参数读取口。
//
// 官方 wire 把位置参数按**名字**放进同一个 JSON 对象（`url` / `query` / `urls` /
// `options`），本仓 `/api` 的 body 就是它。读过的键被记账，`finish()` 之后还有
// 未读键就是未知字段 → 400（对应官方 strictObject 的 additionalProperties:false）。
type marketParams struct {
	obj    map[string]json.RawMessage
	unread map[string]bool
}

func decodeMarketParams(r *http.Request) (*marketParams, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, marketBadRequest("读请求体失败: %v", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, marketBadRequest("请求体必须是 JSON 对象")
	}
	unread := make(map[string]bool, len(obj))
	for key := range obj {
		unread[key] = true
	}
	return &marketParams{obj: obj, unread: unread}, nil
}

func (p *marketParams) take(key string) (json.RawMessage, bool) {
	raw, ok := p.obj[key]
	if ok {
		delete(p.unread, key)
	}
	return raw, ok
}

func (p *marketParams) finish() error {
	if len(p.unread) == 0 {
		return nil
	}
	keys := make([]string, 0, len(p.unread))
	for key := range p.unread {
		keys = append(keys, key)
	}
	return marketBadRequest("未知参数 %q（官方契约里没有这个字段）", strings.Join(keys, ", "))
}

// String 读一个字符串参数；required 且缺席/空串即 400。
func (p *marketParams) String(key string, required bool) (string, error) {
	raw, ok := p.take(key)
	if !ok {
		if required {
			return "", marketBadRequest("缺少必填参数 %q", key)
		}
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", marketBadRequest("参数 %q 必须是字符串", key)
	}
	if required && strings.TrimSpace(value) == "" {
		return "", marketBadRequest("参数 %q 不能为空", key)
	}
	return value, nil
}

func (p *marketParams) strings(key string, required bool) ([]string, error) {
	raw, ok := p.take(key)
	if !ok {
		if required {
			return nil, marketBadRequest("缺少必填参数 %q", key)
		}
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, marketBadRequest("参数 %q 必须是字符串数组", key)
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

func (p *marketParams) object(key string, required bool) (*marketParams, error) {
	raw, ok := p.take(key)
	if !ok {
		if required {
			return nil, marketBadRequest("缺少必填参数 %q", key)
		}
		return nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, marketBadRequest("参数 %q 必须是 JSON 对象", key)
	}
	unread := make(map[string]bool, len(obj))
	for name := range obj {
		unread[name] = true
	}
	return &marketParams{obj: obj, unread: unread}, nil
}

// integer 读一个整数参数并校验范围（官方 `offset` / `limit` 都是 integer）。
func (p *marketParams) integer(key string, required bool, min, max int) (int, error) {
	value, present, err := p.integerValue(key, required)
	if err != nil || !present {
		return 0, err
	}
	if value < int64(min) || value > int64(max) {
		return 0, marketBadRequest("参数 %q 必须在 %d..%d，实际 %d", key, min, max, value)
	}
	return int(value), nil
}

// integerValue 按官方 `z.number()` + "必须是整数"的口径读一个整数：
// JSON 数字（含 `1e3` 这种写法）都认，非整数/超出安全整数范围一律 400。
func (p *marketParams) integerValue(key string, required bool) (int64, bool, error) {
	raw, ok := p.take(key)
	if !ok {
		if required {
			return 0, false, marketBadRequest("缺少必填参数 %q", key)
		}
		return 0, false, nil
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, false, marketBadRequest("参数 %q 必须是整数", key)
	}
	if math.Trunc(number) != number || math.Abs(number) > 1<<53 {
		return 0, false, marketBadRequest("参数 %q 必须是整数", key)
	}
	return int64(number), true, nil
}

func (p *marketParams) boolean(key string, required bool) (bool, error) {
	raw, ok := p.take(key)
	if !ok {
		if required {
			return false, marketBadRequest("缺少必填参数 %q", key)
		}
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, marketBadRequest("参数 %q 必须是布尔", key)
	}
	return value, nil
}

// marketQuery 读并校验官方 MarketQuery：`sort`/`offset`/`limit` 必填，
// `text`/`category`/`labels`/`refresh`/`fetchedAt` 可选，未知字段 400。
func (p *marketParams) marketQuery() (OfficialMarketQuery, error) {
	var query OfficialMarketQuery
	params, err := p.object("query", true)
	if err != nil {
		return query, err
	}
	if query.Text, err = params.String("text", false); err != nil {
		return query, err
	}
	if query.Category, err = params.String("category", false); err != nil {
		return query, err
	}
	if query.Labels, err = params.strings("labels", false); err != nil {
		return query, err
	}
	if query.Sort, err = params.marketSort(); err != nil {
		return query, err
	}
	if query.Offset, err = params.integer("offset", true, 0, maxMarketInt); err != nil {
		return query, err
	}
	if query.Limit, err = params.integer("limit", true, 1, 100); err != nil {
		return query, err
	}
	if query.Refresh, err = params.boolean("refresh", false); err != nil {
		return query, err
	}
	if query.FetchedAt, err = params.integer64("fetchedAt", false); err != nil {
		return query, err
	}
	return query, params.finish()
}

// maxMarketInt 是 offset 的上界：官方是"非负整数"（JSON number），这里只挡溢出。
const maxMarketInt = 1 << 30

func (p *marketParams) integer64(key string, required bool) (int64, error) {
	value, _, err := p.integerValue(key, required)
	return value, err
}

// marketSort 读官方 MarketSort union：kind 四选一，published/key 还必须有 direction。
func (p *marketParams) marketSort() (OfficialMarketSort, error) {
	var sort OfficialMarketSort
	params, err := p.object("sort", true)
	if err != nil {
		return sort, err
	}
	if sort.Kind, err = params.String("kind", true); err != nil {
		return sort, err
	}
	switch sort.Kind {
	case "index", "relevance":
		return sort, params.finish()
	case "published", "key":
	default:
		return sort, marketBadRequest("query.sort.kind 只能是 index / relevance / published / key，实际 %q", sort.Kind)
	}
	if sort.Direction, err = params.direction(); err != nil {
		return sort, err
	}
	if sort.Kind == "key" {
		if sort.ID, err = params.String("id", true); err != nil {
			return sort, err
		}
	}
	return sort, params.finish()
}

func (p *marketParams) direction() (string, error) {
	value, err := p.String("direction", true)
	if err != nil {
		return "", err
	}
	if value != "asc" && value != "desc" {
		return "", marketBadRequest("direction 只能是 asc 或 desc，实际 %q", value)
	}
	return value, nil
}

// detailOptions 校验 readEntryDetail 的可选 options（官方 strictObject）。
//
// 宿主当前**没有** npm 文件 CDN 读取器、也不做详情缓存，所以这两个选项在当前
// 实现里没有效果；但它们属于官方契约，非法值仍然 400（不静默接受坏参数）。
func (p *marketParams) detailOptions() error {
	params, err := p.object("options", false)
	if err != nil || params == nil {
		return err
	}
	if _, err := params.boolean("refresh", false); err != nil {
		return err
	}
	raw, ok := params.take("npmFileSources")
	if ok {
		var sources []string
		if err := json.Unmarshal(raw, &sources); err != nil {
			return marketBadRequest("options.npmFileSources 必须是字符串数组")
		}
		for _, source := range sources {
			switch source {
			case "jsdelivr", "unpkg", "npmmirror":
			default:
				return marketBadRequest("options.npmFileSources 只认 jsdelivr / unpkg / npmmirror，实际 %q", source)
			}
		}
	}
	return params.finish()
}

func marketBadRequest(format string, args ...any) *PluginMarketError {
	return &PluginMarketError{Status: http.StatusBadRequest, Code: "bad_request", Message: fmt.Sprintf(format, args...)}
}

func writeMarketError(w http.ResponseWriter, err error) {
	var e *PluginMarketError
	if ok := errors.As(err, &e); ok {
		writeJSON(w, e.Status, errorBodyFor(e.Code, e.Message))
		return
	}
	writeJSON(w, http.StatusBadGateway, errorBodyFor("registry_unavailable", err.Error()))
}
