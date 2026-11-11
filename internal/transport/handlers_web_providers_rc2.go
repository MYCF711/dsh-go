package transport

// 官方 0.2.2-rc.2 的 `web.listProviders`（包 `dsh-web`）wire 入口。
//
// 契约逐条对应官方源码（只读）：
//   - 方法名 `web/listProviders`，**无参数**（`parameters: []`）——
//     `dsh-web/lib/typert.host.js:27-41`。
//   - 结果 `{search: [{id,title?,description?}], fetch: [...]}`，每条按能力分组、
//     "detached `{ id, title, description }` entries per capability, in registration
//     order"；且**可用性不过滤清单**（"Availability does not filter the lists"）——
//     `dsh-web/src/index.ts:146-157`、`src/types.ts:196-217`。
//   - provider 没声明 title/description 时字段**缺席**（不是空串）——
//     `dsh-web/src/index.ts:246-252` 的 `providerInfo()`。
//
// 宿主附加字段（官方没有；"能力下限：只能更多不能更少"要求把运行态说清楚）：
// 每条 provider 的 `available` / `default` / `selected`，顶层 `resolution`
// （当前选择结果与失败码）、`baseline`（rc.2 基线默认 provider 及其是否已注册）、
// `limits`（web_search 的生效上限）。
//
// 真值由宿主注入（`OfficialWebProviderAPI`）：本文件只做官方形状与严格无参校验，
// 不静态列举 provider —— 清单必须来自真实注册表与真实凭据
// （实现见 `modules/tool/websearch/providers.go` 的 `ProviderAPI`）。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

// MethodWebListProviders 是官方方法名（`dsh-web` Remote namespace `web`）。
const MethodWebListProviders Method = "web.listProviders"

// OfficialWebProviderInfo 是一条 provider 的官方形状。
//
// 前三个字段是官方 `WebProviderInfo`（`dsh-web/src/types.ts:196-205`）；
// 其余是宿主附加的只读运行态。
type OfficialWebProviderInfo struct {
	// ID 是注册键，也是 `searchProvider` / `fetchProvider` 选的值。
	ID string `json:"id"`
	// Title 是显示名；provider 没声明时省略。
	Title string `json:"title,omitempty"`
	// Description 是 picker 说明：`string` 或 `{en: string, [locale]: string}`；
	// provider 没声明时省略（官方 LocalizedText）。
	Description any `json:"description,omitempty"`

	// Available 是 provider 自己 `Available()` 的回答（本地检查，不联网）。
	// 用指针以区分"未知"与 false：nil = 宿主没给出（官方的清单本身不含该字段）。
	Available *bool `json:"available,omitempty"`
	// Default 表示这一条就是当前配置钉死（或基线默认生效）的 provider。
	Default bool `json:"default,omitempty"`
	// Selected 表示执行期解析当前会选中的那个 provider（真实可用且唯一/已钉死）。
	Selected bool `json:"selected,omitempty"`
}

// OfficialWebProviderSelection 是一个能力的"当前选择"结果。
type OfficialWebProviderSelection struct {
	// Configured 是配置钉死的 id；空 = 未钉死（按官方规则自动选择）。
	Configured string `json:"configured,omitempty"`
	// Source 说明 Configured 从哪来：config / env / default / auto。
	Source string `json:"source,omitempty"`
	// Selected 是执行期真正会选中的 provider id；选择失败时为空。
	Selected string `json:"selected,omitempty"`
	// ErrorCode 是选择失败时的官方 web 码（WEB_PROVIDER_*），成功时为空。
	ErrorCode string `json:"errorCode,omitempty"`
	// Error 是选择失败的可读说明。
	Error string `json:"error,omitempty"`
}

// OfficialWebProviderResolution 是两个能力各自的当前选择结果。
type OfficialWebProviderResolution struct {
	Search OfficialWebProviderSelection `json:"search"`
	Fetch  OfficialWebProviderSelection `json:"fetch"`
}

// OfficialWebProviderBaseline 报告 rc.2 基础组合的默认 provider 与本宿主是否已注册它。
//
// 为什么把"基线"和"当前默认"分开：官方基础组合钉的是 `deepseek-search-api`
// （rc.2 `dsh-base/cordis.patch.yml:487`），但本宿主不一定实现了那个 provider。
// 把"基线要求什么"和"本宿主真的有什么"混成一个字段，界面就只能撒谎。
type OfficialWebProviderBaseline struct {
	Search           string `json:"search"`
	SearchRegistered bool   `json:"searchRegistered"`
	Fetch            string `json:"fetch"`
	FetchRegistered  bool   `json:"fetchRegistered"`
}

// OfficialWebSearchLimits 是 `web_search` 的生效上限（官方 `tool-web` 配置面）。
type OfficialWebSearchLimits struct {
	MaxQueries      int `json:"maxQueries"`
	MaxResults      int `json:"maxResults"`
	SummaryMaxChars int `json:"summaryMaxChars"`
	SearchTimeoutMS int `json:"searchTimeoutMs"`
}

// OfficialWebCredential 是一个 provider 相关环境变量的真实配置状态。
//
// 宿主附加（官方清单不含）：官方把凭据解析藏在 provider 内部，失败时只给
// `WEB_PROVIDER_*` 错误码；宿主把"这个变量现在配没配"直接列出来，界面才能
// 在搜索失败时说清缺哪一项，而不是让用户去翻文档。
type OfficialWebCredential struct {
	// Name 是环境变量名。
	Name string `json:"name"`
	// Configured 是"当前非空"（真读环境）。
	Configured bool `json:"configured"`
	// Role 是它管什么：selection / endpoint / credential / model。
	Role string `json:"role,omitempty"`
	// Capability 是 search / fetch / both。
	Capability string `json:"capability,omitempty"`
}

// OfficialWebProviderList 是 `web.listProviders` 的结果。
//
// `search` / `fetch` 是官方字段；其余为宿主附加（见文件头注释）。
type OfficialWebProviderList struct {
	Search []OfficialWebProviderInfo `json:"search"`
	Fetch  []OfficialWebProviderInfo `json:"fetch"`

	Resolution  *OfficialWebProviderResolution `json:"resolution,omitempty"`
	Baseline    *OfficialWebProviderBaseline   `json:"baseline,omitempty"`
	Limits      *OfficialWebSearchLimits       `json:"limits,omitempty"`
	Credentials []OfficialWebCredential        `json:"credentials,omitempty"`
}

// OfficialWebProviderAPI 由宿主注入：返回**真实**的 provider 清单。
type OfficialWebProviderAPI interface {
	ListOfficialWebProviders(ctx context.Context) (OfficialWebProviderList, error)
}

var officialWebProviderMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialWebProviderAPI
}{m: map[*Server]OfficialWebProviderAPI{}}

// AttachOfficialWebProviderAPI 挂上 `web.listProviders` 的真实现。
//
// 接线一行（见报告）：`srv.AttachOfficialWebProviderAPI(websearch.ProviderAPI(os.Getenv))`。
func (s *Server) AttachOfficialWebProviderAPI(api OfficialWebProviderAPI) {
	if s == nil {
		return
	}
	officialWebProviderMount.mu.Lock()
	defer officialWebProviderMount.mu.Unlock()
	officialWebProviderMount.m[s] = api
}

// DetachOfficialWebProviderAPI 摘掉真实现（server 关闭/测试用）。
func (s *Server) DetachOfficialWebProviderAPI() {
	if s == nil {
		return
	}
	officialWebProviderMount.mu.Lock()
	defer officialWebProviderMount.mu.Unlock()
	delete(officialWebProviderMount.m, s)
}

func (s *Server) officialWebProviderAPI() (OfficialWebProviderAPI, bool) {
	officialWebProviderMount.mu.RLock()
	defer officialWebProviderMount.mu.RUnlock()
	api, ok := officialWebProviderMount.m[s]
	return api, ok
}

// IsOfficialWebProviderMethod 判断是不是 `web.*` 官方方法。
func IsOfficialWebProviderMethod(name string) bool {
	return Method(name) == MethodWebListProviders
}

// ServeOfficialWebProviderAPI 是 `POST /api/web.listProviders` 的入口。
//
// 与 `llm.listProviders` 同一个口径：**严格无参**（官方 parameters: []），
// 未接线时返回结构化未实现（四字段），不返回空清单假装成功。
func (s *Server) ServeOfficialWebProviderAPI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if !IsOfficialWebProviderMethod(name) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", "不是 web provider 方法"))
		return
	}
	api, ok := s.officialWebProviderAPI()
	if !ok {
		writeOfficialError(w, officialNotImplemented(
			"%s 未接线：宿主没有 AttachOfficialWebProviderAPI（接线：srv.AttachOfficialWebProviderAPI(websearch.ProviderAPI(os.Getenv))）", name))
		return
	}
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeOfficialError(w, officialBadRequest("读参数失败: %v", err))
			return
		}
		if len(raw) > 0 {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil || len(obj) != 0 {
				writeOfficialError(w, officialBadRequest("%s 不接受参数（官方 parameters: []）", name))
				return
			}
		}
	}
	result, err := api.ListOfficialWebProviders(r.Context())
	if err != nil {
		var typed *officialError
		if errors.As(err, &typed) {
			writeOfficialError(w, err)
			return
		}
		writeOfficialError(w, officialInternal("%v", err))
		return
	}
	// 官方结果必须**两个数组都在**（空也是 `[]` 而不是 null）：界面按数组渲染，
	// 缺席会让 picker 变成"加载失败"，而真实状态可能是"一个都没注册"。
	if result.Search == nil {
		result.Search = []OfficialWebProviderInfo{}
	}
	if result.Fetch == nil {
		result.Fetch = []OfficialWebProviderInfo{}
	}
	writeJSON(w, http.StatusOK, result)
}
