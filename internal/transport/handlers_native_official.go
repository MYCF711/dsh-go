package transport

// 官方 5 个 Windows 原生能力方法的 L1 契约层。
//
//	directoryPicker.pick                  workspace-controller typert: directoryPicker/pick
//	settings.openSettingsDocument         settings-controller typert: settings/openSettingsDocument
//	session.canOpenWorkspacePath          session-controller typert: session/canOpenWorkspacePath
//	session.openWorkspacePath             session-controller typert: session/openWorkspacePath
//	session.workspacePathApplications     session-controller typert: session/workspacePathApplications
//
// 官方结果形状（逐字段）：
//
//	pick                    string | null（null = 操作员取消）
//	openSettingsDocument    { opened: true }
//	canOpenWorkspacePath    boolean
//	openWorkspacePath       { opened: true }，参数 { path, action?: 'reveal', application?: string }
//	workspacePathApplications  [{ id, name, default, icon: string|null }]，参数 { path }
//
// 本文件只做协议翻译；真实 Windows API（ShellExecuteEx、文件关联查询、原生
// 目录选择器、进程启动）在 `internal/native`，宿主在
// `cmd/dshd/serve_native_api.go`。
//
// # 为什么 pick 的 null 允许出现
//
// 本仓 writeJSON 把 `{"result":null}` 视为静默成功并拒绝；但官方 `pick` 的
// null 是**值**（用户取消），不是"没有载荷"。这里显式写 enveloped 的
// `{"result":null}`，并在代码里注明这条唯一例外。
//
// # 接线（WIRING）
//
// server.go 的 handleAPI 在 settings/session/workspace 族分派**之前**加：
//
//	if IsOfficialNativeMethod(name) {
//		s.ServeOfficialNativeAPI(w, r, name)
//		return
//	}
//
// 5 个名字里有 3 个在既有 session 族里是 501 占位（openWorkspacePath 等），
// 必须早于族分派，否则永远到不了真实实现。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"dshgo/internal/native"
)

const (
	MethodDirectoryPickerPick          Method = "directoryPicker.pick"
	MethodSettingsOpenSettingsDocument Method = "settings.openSettingsDocument"
	MethodSessionCanOpenWorkspacePath  Method = "session.canOpenWorkspacePath"
	// 这两个常量在 handlers_session_official.go 已定义（同一个 wire 名只能
	// 有一个定义），这里只做引用与文档。
)

// OfficialWorkspacePathApplication 是 `session.workspacePathApplications` 的一行。
type OfficialWorkspacePathApplication struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Default bool    `json:"default"`
	Icon    *string `json:"icon"`
}

// OfficialNativeAPI 是宿主注入的 5 项原生能力实现。
type OfficialNativeAPI struct {
	// PickDirectory 弹原生目录选择器。chosen=false 表示操作员取消（官方 null）。
	PickDirectory func(ctx context.Context) (path string, chosen bool, err error)
	// OpenSettingsDocument 用默认程序打开设置文档。
	OpenSettingsDocument func(ctx context.Context) error
	// CanOpenWorkspacePath 报告本机是否有原生打开通道。
	CanOpenWorkspacePath func() (bool, error)
	// OpenWorkspacePath 打开一个路径（action="reveal" 定位 / application 指定程序）。
	OpenWorkspacePath func(ctx context.Context, path, action, application string) error
	// WorkspacePathApplications 列出该路径的注册处理程序。
	WorkspacePathApplications func(ctx context.Context, path string) ([]OfficialWorkspacePathApplication, error)
}

var officialNativeMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialNativeAPI
}{m: map[*Server]OfficialNativeAPI{}}

// AttachOfficialNativeAPI 把原生能力挂到 Server 上。
func (s *Server) AttachOfficialNativeAPI(api OfficialNativeAPI) {
	if s == nil {
		return
	}
	officialNativeMount.mu.Lock()
	defer officialNativeMount.mu.Unlock()
	officialNativeMount.m[s] = api
}

// DetachOfficialNativeAPI 摘掉实现（测试收尾用）。
func (s *Server) DetachOfficialNativeAPI() {
	if s == nil {
		return
	}
	officialNativeMount.mu.Lock()
	defer officialNativeMount.mu.Unlock()
	delete(officialNativeMount.m, s)
}

func (s *Server) officialNativeAPI() (OfficialNativeAPI, bool) {
	if s == nil {
		return OfficialNativeAPI{}, false
	}
	officialNativeMount.mu.RLock()
	defer officialNativeMount.mu.RUnlock()
	api, ok := officialNativeMount.m[s]
	return api, ok
}

// IsOfficialNativeMethod 判断一个 /api 路径名是否属于这 5 个原生方法。
func IsOfficialNativeMethod(name string) bool {
	switch Method(name) {
	case MethodDirectoryPickerPick,
		MethodSettingsOpenSettingsDocument,
		MethodSessionCanOpenWorkspacePath,
		MethodSessionOpenWorkspacePath,
		MethodSessionWorkspacePathApps:
		return true
	}
	return false
}

// ServeOfficialNativeAPI 是 5 个原生方法的 HTTP 入口。
func (s *Server) ServeOfficialNativeAPI(w http.ResponseWriter, r *http.Request, name string) {
	api, ok := s.officialNativeAPI()
	if !ok {
		writeOfficialNativeError(w, &native.Error{
			Code: "gateway/method-unavailable", Status: http.StatusNotImplemented,
			Message: name + " 未接线：宿主没有挂 OfficialNativeAPI",
		})
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}
	ctx := r.Context()

	switch Method(name) {
	case MethodDirectoryPickerPick:
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.PickDirectory == nil {
			writeOfficialNativeError(w, nativeNotWired(name, "PickDirectory"))
			return
		}
		path, chosen, err := api.PickDirectory(ctx)
		if err != nil {
			writeOfficialNativeError(w, err)
			return
		}
		if !chosen {
			// 官方结果就是 string|null：null = 取消。这是本仓唯一允许的
			// `{"result":null}`（值是 null，不是"没有载荷"），所以显式走
			// writeOfficialNullResult 绕过 writeJSON 的"空载荷"兜底。
			writeOfficialNullResult(w)
			return
		}
		if strings.TrimSpace(path) == "" {
			writeOfficialNativeError(w, &native.Error{
				Code: "gateway/internal", Status: http.StatusInternalServerError,
				Message: "目录选择器报告成功但返回了空路径",
			})
			return
		}
		writeJSON(w, http.StatusOK, path)

	case MethodSettingsOpenSettingsDocument:
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.OpenSettingsDocument == nil {
			writeOfficialNativeError(w, nativeNotWired(name, "OpenSettingsDocument"))
			return
		}
		if err := api.OpenSettingsDocument(ctx); err != nil {
			writeOfficialNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"opened": true})

	case MethodSessionCanOpenWorkspacePath:
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.CanOpenWorkspacePath == nil {
			writeOfficialNativeError(w, nativeNotWired(name, "CanOpenWorkspacePath"))
			return
		}
		canOpen, err := api.CanOpenWorkspacePath()
		if err != nil {
			writeOfficialNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, canOpen)

	case MethodSessionOpenWorkspacePath:
		path, err := params.str("path")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		action, _ := optionalStringParam(params, "action")
		application, _ := optionalStringParam(params, "application")
		if action != "" && action != "reveal" {
			writeOfficialNativeError(w, &native.Error{
				Code: "gateway/bad-request", Status: http.StatusBadRequest,
				Message: "openWorkspacePath 的 action 只能是 reveal 或缺省",
			})
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.OpenWorkspacePath == nil {
			writeOfficialNativeError(w, nativeNotWired(name, "OpenWorkspacePath"))
			return
		}
		if err := api.OpenWorkspacePath(ctx, path, action, application); err != nil {
			writeOfficialNativeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"opened": true})

	case MethodSessionWorkspacePathApps:
		path, err := params.str("path")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.WorkspacePathApplications == nil {
			writeOfficialNativeError(w, nativeNotWired(name, "WorkspacePathApplications"))
			return
		}
		apps, err := api.WorkspacePathApplications(ctx, path)
		if err != nil {
			writeOfficialNativeError(w, err)
			return
		}
		if apps == nil {
			apps = []OfficialWorkspacePathApplication{}
		}
		writeJSON(w, http.StatusOK, apps)

	default:
		writeOfficialNativeError(w, nativeNotWired(name, "未知的原生方法"))
	}
}

// writeOfficialNullResult 写官方 `string|null` 里的 null。
//
// 这是 writeJSON "成功不许是空载荷" 规则的**唯一**例外：`directoryPicker.pick`
// 的返回类型就是 `string|null`，null 是"用户取消"这个值本身。没有别的成功
// 结果可以走这里。
func writeOfficialNullResult(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintln(w, `{"result":null}`)
}

// optionalStringParam 取一个可选字符串参数；缺省/空串返回空。
func optionalStringParam(p *officialParams, name string) (string, bool) {
	raw, ok := p.take(name)
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func nativeNotWired(name, field string) *native.Error {
	return &native.Error{Code: "gateway/method-unavailable", Status: http.StatusNotImplemented,
		Message: name + " 未接线：宿主没有 " + field}
}

// writeOfficialNativeError 把 native.Error 翻成官方四字段。
func writeOfficialNativeError(w http.ResponseWriter, err error) {
	var typed *native.Error
	if errors.As(err, &typed) {
		writeJSON(w, typed.Status, errorBodyFor(typed.Code, typed.Message))
		return
	}
	// 没有类型的错误一律 500：绝不渲染成成功。
	writeJSON(w, http.StatusInternalServerError, errorBodyFor("gateway/internal", err.Error()))
}
