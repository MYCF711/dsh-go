package transport

// PARITY-W3-MARKET：pluginMarket 4 个方法与 pluginRegistryProbe.fastest 的 L1 契约。
// 真实索引存储/HTTP 在 internal/market；本文件只做官方参数/结果与结构化错误。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const (
	MethodPluginMarketListIndexes    Method = "pluginMarket.listIndexes"
	MethodPluginRegistryProbeFastest Method = "pluginRegistryProbe.fastest"
)

type OfficialMarketIndex struct {
	URL         string            `json:"url"`
	Title       map[string]string `json:"title,omitempty"`
	Description map[string]string `json:"description,omitempty"`
	Error       string            `json:"error,omitempty"`
}

type OfficialMarketPlugin struct {
	Name           string                        `json:"name"`
	Title          map[string]string             `json:"title"`
	Description    map[string]string             `json:"description"`
	Author         string                        `json:"author,omitempty"`
	Keywords       []string                      `json:"keywords"`
	Category       string                        `json:"category,omitempty"`
	Labels         []string                      `json:"labels"`
	Versions       []OfficialMarketPluginVersion `json:"versions"`
	DefaultVersion string                        `json:"defaultVersion,omitempty"`
	Installation   map[string]any                `json:"installation"`
}

type OfficialMarketPluginVersion struct {
	Version     string            `json:"version"`
	PublishedAt string            `json:"publishedAt,omitempty"`
	Deprecated  string            `json:"deprecated,omitempty"`
	Engines     map[string]string `json:"engines,omitempty"`
	Compatible  bool              `json:"compatible"`
	// Update 是官方 0.2.2-rc.2 市场版本行新增的 `update: boolean`
	// （`dsh-plugin-market/src/index.ts:481-487`、`types.ts` 的
	// MarketListingVersion.update）：已装副本来自**完整 commit 钉住的 git spec**、
	// 这一版更新、且钉住同仓库同 `path:` 目录时才是 true。**总是显式下发**：
	// 缺席与 false 在客户端语义不同（缺席会被读成"老宿主，别显示升级按钮"）。
	Update bool `json:"update"`
}

type OfficialMarketSearchResult struct {
	Index      OfficialMarketIndex    `json:"index"`
	Plugins    []OfficialMarketPlugin `json:"plugins"`
	Total      int                    `json:"total"`
	Offset     int                    `json:"offset"`
	NextOffset *int                   `json:"nextOffset,omitempty"`
	FetchedAt  int64                  `json:"fetchedAt"`
}

type OfficialMarketEntryDetail struct {
	Status   string `json:"status"`
	Origin   string `json:"origin,omitempty"`
	Markdown string `json:"markdown,omitempty"`
	Document string `json:"document,omitempty"`
}

type OfficialMarketQuery struct {
	Text      string
	Category  string
	Labels    []string
	Sort      map[string]any
	Offset    int
	Limit     int
	Refresh   bool
	FetchedAt int64
}

type OfficialPluginMarketAPI interface {
	ListIndexes(context.Context) ([]OfficialMarketIndex, error)
	SetIndexes(context.Context, []string) error
	Search(context.Context, string, OfficialMarketQuery) (OfficialMarketSearchResult, error)
	ReadEntryDetail(context.Context, string, string, string, string) (OfficialMarketEntryDetail, error)
}

type OfficialPluginRegistryProbeAPI interface {
	Fastest(context.Context, []string) (OfficialRegistryProbeResult, error)
}

type OfficialRegistryProbeResult struct {
	Registry  string `json:"registry"`
	LatencyMs int64  `json:"latencyMs"`
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
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "请求体必须是 JSON 对象"))
		return
	}
	ctx := r.Context()
	var result any
	switch Method(name) {
	case MethodPluginMarketListIndexes:
		items, err := api.ListIndexes(ctx)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		result = items
	case MethodPluginMarketSetIndexes:
		var urls []string
		if err := json.Unmarshal(obj["urls"], &urls); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "urls 必须是字符串数组"))
			return
		}
		if err := api.SetIndexes(ctx, urls); err != nil {
			writeMarketError(w, err)
			return
		}
		result = map[string]any{}
	case MethodPluginMarketSearch:
		url, _ := rawString(obj, "url")
		var query OfficialMarketQuery
		if q, ok := obj["query"]; ok {
			_ = json.Unmarshal(q, &query)
		}
		if query.Limit == 0 {
			query.Limit = 50
		}
		search, err := api.Search(ctx, url, query)
		if err != nil {
			writeMarketError(w, err)
			return
		}
		result = search
	case MethodPluginMarketReadEntryDetail:
		url, _ := rawString(obj, "url")
		name, _ := rawString(obj, "name")
		version, _ := rawString(obj, "version")
		language, _ := rawString(obj, "language")
		detail, err := api.ReadEntryDetail(ctx, url, name, version, language)
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
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var obj struct {
		URLs []string `json:"urls"`
	}
	_ = json.Unmarshal(raw, &obj)
	result, err := api.Fastest(r.Context(), obj.URLs)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func writeMarketError(w http.ResponseWriter, err error) {
	var e *PluginMarketError
	if ok := errorsAs(err, &e); ok {
		writeJSON(w, e.Status, errorBodyFor(e.Code, e.Message))
		return
	}
	writeJSON(w, http.StatusBadGateway, errorBodyFor("registry_unavailable", err.Error()))
}

func rawString(obj map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := obj[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }
