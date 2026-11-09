package transport

// 官方 forgePanel 的 **worktree 生命周期** 6 个方法（L1 契约层）。
//
// 这 6 个方法在官方是 `client-ui-forge` 的 Host Remote：
//
//	forgePanel/requestWorktree      typert.host.js:1262 / client-ui-forge/src/index.ts:430
//	forgePanel/cancelWorktree       typert.host.js:591  / …/index.ts:449
//	forgePanel/retryWorktree        typert.host.js:1347 / …/index.ts:478
//	forgePanel/stopCreation         typert.host.js:1381 / …/index.ts:465
//	forgePanel/restoreWorktree      typert.host.js:1313 / …/index.ts:499
//	forgePanel/continueInProject    typert.host.js:849  / …/index.ts:530
//
// 官方结果都是 `z.void()`（成功无载荷），参数是位置参数、wire 名就是下面的
// 对象键（`sessionId`；requestWorktree 额外 `from: string|null`）。
//
// 本文件**只做协议翻译**：参数校验（缺字段/未知字段/类型）、四字段错误、
// 成功空对象；真实状态机在 `internal/worktree`，宿主在
// `cmd/dshd/serve_worktree_api.go`。这样：
//   - transport 不 import 任何 session/git 实现（R7）；
//   - 同一台状态机同时服务 HTTP 与（将来）mux 面，不会分叉。
//
// # 为什么不能沿用 forge 族的"结构化 501"
//
// 官方这 6 个方法本地可做（git 本机有），返回 501 等于把"没做"说成"做不到"。
// 本实现真的建 `.agents/worktrees/<branch>`、真的切会话目录、真的取消/重试/
// 恢复；没有 worktree 服务时按官方码 `forgePanel/worktrees-unavailable` 拒绝，
// 不返回假成功。
//
// # 接线（WIRING）
//
// server.go 的 handleAPI 在 `IsOfficialForgeLiveshareMethod` 之前加：
//
//	if IsOfficialForgeWorktreeMethod(name) {
//		s.ServeOfficialForgeWorktreeAPI(w, r, name)
//		return
//	}
//
// 顺序是硬要求：forge 族的旧谓词会先吞掉 `forgePanel.*` 并回 501。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"dshgo/internal/worktree"
)

// 方法常量复用 handlers_forge_liveshare_official.go 的 forgePanel 常量组
// （同一个 wire 名只能有一个定义）。

// OfficialForgeWorktreeMethods 返回这 6 个方法（顺序固定，供快照/台账）。
func OfficialForgeWorktreeMethods() []Method {
	return []Method{
		MethodForgePanelRequestWorktree,
		MethodForgePanelCancelWorktree,
		MethodForgePanelRetryWorktree,
		MethodForgePanelStopCreation,
		MethodForgePanelRestoreWorktree,
		MethodForgePanelContinueInProject,
	}
}

// IsOfficialForgeWorktreeMethod 判断一个 /api 路径名是否属于这 6 个方法。
func IsOfficialForgeWorktreeMethod(name string) bool {
	switch Method(name) {
	case MethodForgePanelRequestWorktree,
		MethodForgePanelCancelWorktree,
		MethodForgePanelRetryWorktree,
		MethodForgePanelStopCreation,
		MethodForgePanelRestoreWorktree,
		MethodForgePanelContinueInProject:
		return true
	}
	return false
}

// OfficialWorktreeAPI 是宿主注入的 worktree 生命周期实现。
//
// 每个字段都必须是真实操作；nil 表示该方法没接线，调用方回 501 点名缺什么。
type OfficialWorktreeAPI struct {
	RequestWorktree   func(sessionID string, from *string) error
	CancelWorktree    func(sessionID string) error
	RetryWorktree     func(sessionID string) error
	StopCreation      func(sessionID string) error
	RestoreWorktree   func(sessionID string) error
	ContinueInProject func(sessionID string) error
}

var officialWorktreeMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialWorktreeAPI
}{m: map[*Server]OfficialWorktreeAPI{}}

// AttachOfficialWorktreeAPI 把 worktree 实现挂到 Server 上。
func (s *Server) AttachOfficialWorktreeAPI(api OfficialWorktreeAPI) {
	if s == nil {
		return
	}
	officialWorktreeMount.mu.Lock()
	defer officialWorktreeMount.mu.Unlock()
	officialWorktreeMount.m[s] = api
}

// DetachOfficialWorktreeAPI 摘掉实现（测试收尾用）。
func (s *Server) DetachOfficialWorktreeAPI() {
	if s == nil {
		return
	}
	officialWorktreeMount.mu.Lock()
	defer officialWorktreeMount.mu.Unlock()
	delete(officialWorktreeMount.m, s)
}

func (s *Server) officialWorktreeAPI() (OfficialWorktreeAPI, bool) {
	if s == nil {
		return OfficialWorktreeAPI{}, false
	}
	officialWorktreeMount.mu.RLock()
	defer officialWorktreeMount.mu.RUnlock()
	api, ok := officialWorktreeMount.m[s]
	return api, ok
}

// ServeOfficialForgeWorktreeAPI 是 6 个方法的 HTTP 入口。
func (s *Server) ServeOfficialForgeWorktreeAPI(w http.ResponseWriter, r *http.Request, name string) {
	api, ok := s.officialWorktreeAPI()
	if !ok {
		writeOfficialWorktreeError(w, officialWorktreeNotImplemented(name, "宿主没有挂 worktree 服务（transport.OfficialWorktreeAPI 未接线）"))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	sessionID, err := params.str("sessionId")
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	var from *string
	if Method(name) == MethodForgePanelRequestWorktree {
		raw, present := params.take("from")
		if present {
			if strings.TrimSpace(string(raw)) == "null" {
				from = nil
			} else {
				var value string
				if err := json.Unmarshal(raw, &value); err != nil {
					writeOfficialError(w, fmt.Errorf("%w：requestWorktree 的 from 必须是字符串或 null", ErrOfficialBadInput))
					return
				}
				from = &value
			}
		}
	}
	if err := params.finish(); err != nil {
		writeOfficialError(w, err)
		return
	}

	var callErr error
	switch Method(name) {
	case MethodForgePanelRequestWorktree:
		if api.RequestWorktree == nil {
			writeOfficialWorktreeError(w, officialWorktreeNotImplemented(name, "宿主没有 requestWorktree 实现"))
			return
		}
		callErr = api.RequestWorktree(sessionID, from)
	case MethodForgePanelCancelWorktree:
		if api.CancelWorktree == nil {
			writeOfficialWorktreeError(w, officialWorktreeNotImplemented(name, "宿主没有 cancelWorktree 实现"))
			return
		}
		callErr = api.CancelWorktree(sessionID)
	case MethodForgePanelRetryWorktree:
		if api.RetryWorktree == nil {
			writeOfficialWorktreeError(w, officialWorktreeNotImplemented(name, "宿主没有 retryWorktree 实现"))
			return
		}
		callErr = api.RetryWorktree(sessionID)
	case MethodForgePanelStopCreation:
		if api.StopCreation == nil {
			writeOfficialWorktreeError(w, officialWorktreeNotImplemented(name, "宿主没有 stopCreation 实现"))
			return
		}
		callErr = api.StopCreation(sessionID)
	case MethodForgePanelRestoreWorktree:
		if api.RestoreWorktree == nil {
			writeOfficialWorktreeError(w, officialWorktreeNotImplemented(name, "宿主没有 restoreWorktree 实现"))
			return
		}
		callErr = api.RestoreWorktree(sessionID)
	case MethodForgePanelContinueInProject:
		if api.ContinueInProject == nil {
			writeOfficialWorktreeError(w, officialWorktreeNotImplemented(name, "宿主没有 continueInProject 实现"))
			return
		}
		callErr = api.ContinueInProject(sessionID)
	default:
		writeOfficialWorktreeError(w, officialWorktreeNotImplemented(name, "未知的 worktree 方法"))
		return
	}
	if callErr != nil {
		writeOfficialWorktreeError(w, callErr)
		return
	}
	// 官方 `z.void()`：成功无载荷。本仓 L1 出口用空对象表达（与
	// commands.execute 的"这次调用没有载荷"同一约定）。
	writeJSON(w, http.StatusOK, map[string]any{})
}

// officialWorktreeNotImplemented 是"没接线"的 501（点名方法 + 缺什么）。
func officialWorktreeNotImplemented(name, detail string) error {
	return &worktree.Error{Code: "not_implemented", Status: http.StatusNotImplemented,
		Message: name + " 未接线：" + detail}
}

// writeOfficialWorktreeError 把 worktree 的结构化错误翻成四字段信封。
func writeOfficialWorktreeError(w http.ResponseWriter, err error) {
	var typed *worktree.Error
	if errors.As(err, &typed) {
		writeJSON(w, typed.Status, errorBodyFor(typed.Code, typed.Message))
		return
	}
	// 没有类型的错误一律按宿主内部问题报：绝不渲染成成功。
	writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", err.Error()))
}
