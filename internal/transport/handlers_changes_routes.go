package transport

// 官方 0.2.2-rc.2 `dsh-client-ui-deliverables` 的三条变更路由。
//
// 权威源（随包发布的 TypeScript 原件）：
//
//	dsh-client-ui-deliverables/src/changes.ts:6-8
//	  export const CHANGES_SUMMARY_PATH = '/api/changes.summary'
//	  export const CHANGES_DIFF_PATH    = '/api/changes.diff'
//	  export const CHANGES_OPEN_PATH    = '/api/changes.open'
//	:20-38  summary：GET ?sessionId=&seq= → {turn, files[], total, added, deleted}
//	:40-57  diff：   GET ?sessionId=&seq=&index= → WorkspaceFileDiff union
//	:70-80  open：   POST ?sessionId=&seq=&index=（+ action=reveal / application=）
//
// 它们是**浏览器直接 fetch 的文档相对路由**，不是 `POST /api/<method>` 的
// typert 方法：所以这里必须在 `handleAPI` 的分派之前按路径接住，否则会被
// "未知方法" 分支吞掉。响应体是裸对象（不是 `{result: ...}` 信封），
// 因为消费端 `response.json()` 之后直接按字段校验。
//
// 错误映射（消费端只看状态码）：
//
//	400  参数缺失/形状非法（sessionId 非空、seq/index 非负整数）
//	404  seq/index 这个会话没有记录（官方 `'missing'` 状态）
//	422  open 在这台机器上没有原生打开通道（官方 `'nativeUnavailable'`）
//	501  宿主没接线这三条路由（不假装 200）
//	500  经真实通道读记录/算 diff 失败

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

const (
	// ChangesSummaryRoute 是变更摘要路由（逐字，官方 changes.ts:6）。
	ChangesSummaryRoute = "/api/changes.summary"
	// ChangesDiffRoute 是单个文件 before/after 比较路由（官方 changes.ts:7）。
	ChangesDiffRoute = "/api/changes.diff"
	// ChangesOpenRoute 在宿主桌面上打开一个变更文件（官方 changes.ts:8）。
	ChangesOpenRoute = "/api/changes.open"
)

// ErrChangesOpenUnsupported 表示本机没有可用的原生打开通道。
//
// Transport 不 import internal/native（R7：Transport 不认识宿主实现），所以
// 宿主在回调里把 native.ErrUnsupported 翻成它；路由层据此回 422（官方
// present-open 客户端把 422 映射成 `nativeUnavailable`）。
var ErrChangesOpenUnsupported = errors.New("changes.open: 本机没有原生打开通道")

// ChangesSummaryFile 是摘要里的一行（官方 WorkspaceChangedFile 的线上子集）。
type ChangesSummaryFile struct {
	Path      string `json:"path"`
	Display   string `json:"display"`
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Binary    bool   `json:"binary,omitempty"`
	Oversized bool   `json:"oversized,omitempty"`
}

// ChangesSnapshot 是这一轮前后两张快照树的 id（官方 WorkspaceChangesSummary.snapshot）。
type ChangesSnapshot struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// ChangesSummary 是一条 `workspace/changes` 事件的摘要。
type ChangesSummary struct {
	Turn    int                  `json:"turn"`
	Files   []ChangesSummaryFile `json:"files"`
	Total   int                  `json:"total"`
	Added   int                  `json:"added"`
	Deleted int                  `json:"deleted"`
	// Cwd 是 files[].path 相对的会话工作目录（官方同名字段）。
	Cwd string `json:"cwd,omitempty"`
	// Snapshot 只在真的拍了快照时出现（没有 git 的会话没有它）。
	Snapshot *ChangesSnapshot `json:"snapshot,omitempty"`
}

// ChangesHunk 是统一 diff 的一段（官方 WorkspaceDiffHunk 逐字）。
type ChangesHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// ChangesTextDiff 是文本文件的比较结果（官方 WorkspaceFileDiff 的 text 支）。
type ChangesTextDiff struct {
	Kind    string        `json:"kind"`
	Path    string        `json:"path"`
	Display string        `json:"display"`
	Before  bool          `json:"before"`
	After   bool          `json:"after"`
	Hunks   []ChangesHunk `json:"hunks"`
	Coarse  bool          `json:"coarse"`
}

// ChangesRefusalDiff 是 binary / oversized 两支（官方 union 的另两支）。
type ChangesRefusalDiff struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Display string `json:"display"`
}

// ChangesAPI 是三条变更路由的宿主实现。
//
// 三个回调都必须区分"这个 seq/index 没有记录"（ok=false → 404）与
// "读记录/算 diff 真失败了"（err != nil → 500）；缺省（nil）时路由回 501。
type ChangesAPI struct {
	// Summary 取一条 workspace/changes 事件的摘要。
	Summary func(sessionID string, seq int64) (ChangesSummary, bool, error)
	// Diff 比较摘要里第 index 个文件的 before/after。
	//
	// 返回值是官方 union：ChangesTextDiff / ChangesRefusalDiff。
	Diff func(sessionID string, seq int64, index int) (any, bool, error)
	// Open 在宿主桌面打开（或定位）摘要里第 index 个文件的当前路径。
	//
	// action 为空或 `reveal`；application 非空时用它指定的程序打开。
	// 没有原生通道时返回 ErrChangesOpenUnsupported（回 422）。
	Open func(sessionID string, seq int64, index int, action, application string) error
}

// writeChangesJSON 写**裸对象**（官方这三条路由的消费端直接 response.json() 之后
// 按字段判断，没有 `{result: …}` 信封）。错误体仍用既有四字段 errorBody，
// 但同样不套信封——客户端只看状态码，形状与成功响应保持同一种"裸 JSON"。
func writeChangesJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		// 走同一个四字段错误出口（手写 JSON 正文会被 error_contract 守卫抓，而且
		// 少字段的正文客户端也解不动）。
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", "changes 响应无法序列化"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// serveChangesRoute 在 /api 分派之前接住三条变更路由；未命中返回 false。
func (s *Server) serveChangesRoute(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case ChangesSummaryRoute:
		s.handleChangesSummary(w, r)
		return true
	case ChangesDiffRoute:
		s.handleChangesDiff(w, r)
		return true
	case ChangesOpenRoute:
		s.handleChangesOpen(w, r)
		return true
	default:
		return false
	}
}

// changesQuery 解析三条路由共用的查询参数。
func changesQuery(w http.ResponseWriter, r *http.Request, wantIndex bool) (string, int64, int, bool) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "changes 路由需要非空 sessionId"))
		return "", 0, 0, false
	}
	rawSeq := strings.TrimSpace(r.URL.Query().Get("seq"))
	seq, err := strconv.ParseInt(rawSeq, 10, 64)
	if rawSeq == "" || err != nil || seq < 0 {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request",
			"changes 路由需要非负整数 seq（workspace/changes 事件的 seq）"))
		return "", 0, 0, false
	}
	index := 0
	if wantIndex {
		rawIndex := strings.TrimSpace(r.URL.Query().Get("index"))
		parsed, err := strconv.Atoi(rawIndex)
		if rawIndex == "" || err != nil || parsed < 0 {
			writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request",
				"changes.diff/open 需要非负整数 index（摘要 files 里的下标）"))
			return "", 0, 0, false
		}
		index = parsed
	}
	return sessionID, seq, index, true
}

func (s *Server) handleChangesSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeChangesJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if !s.authorized(r) {
		writeChangesJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
		return
	}
	sessionID, seq, _, ok := changesQuery(w, r, false)
	if !ok {
		return
	}
	if s.opt.Changes.Summary == nil {
		writeChangesJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			"changes.summary 未接线：宿主没有 workspace/changes 记录源"))
		return
	}
	summary, found, err := s.opt.Changes.Summary(sessionID, seq)
	if err != nil {
		writeChangesJSON(w, http.StatusInternalServerError, errorBodyFor("internal", err.Error()))
		return
	}
	if !found {
		writeChangesJSON(w, http.StatusNotFound, errorBodyFor("not_found",
			"这个会话在 seq="+strconv.FormatInt(seq, 10)+" 没有 workspace/changes 记录"))
		return
	}
	if summary.Files == nil {
		summary.Files = []ChangesSummaryFile{}
	}
	writeChangesJSON(w, http.StatusOK, summary)
}

func (s *Server) handleChangesDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeChangesJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if !s.authorized(r) {
		writeChangesJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
		return
	}
	sessionID, seq, index, ok := changesQuery(w, r, true)
	if !ok {
		return
	}
	if s.opt.Changes.Diff == nil {
		writeChangesJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			"changes.diff 未接线：宿主没有 workspace/changes 记录源"))
		return
	}
	diff, found, err := s.opt.Changes.Diff(sessionID, seq, index)
	if err != nil {
		writeChangesJSON(w, http.StatusInternalServerError, errorBodyFor("internal", err.Error()))
		return
	}
	if !found {
		writeChangesJSON(w, http.StatusNotFound, errorBodyFor("not_found",
			"这个会话在 seq="+strconv.FormatInt(seq, 10)+" 的 index="+strconv.Itoa(index)+" 没有可比较的文件"))
		return
	}
	writeChangesJSON(w, http.StatusOK, diff)
}

func (s *Server) handleChangesOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeChangesJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if !s.authorized(r) {
		writeChangesJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
		return
	}
	sessionID, seq, index, ok := changesQuery(w, r, true)
	if !ok {
		return
	}
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	if action != "" && action != "reveal" {
		writeChangesJSON(w, http.StatusBadRequest, errorBodyFor("bad_request",
			"changes.open 的 action 只能是 reveal 或缺省"))
		return
	}
	if s.opt.Changes.Open == nil {
		writeChangesJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			"changes.open 未接线：宿主没有桌面打开通道"))
		return
	}
	if err := s.opt.Changes.Open(sessionID, seq, index, action, strings.TrimSpace(r.URL.Query().Get("application"))); err != nil {
		if errors.Is(err, ErrChangesOpenUnsupported) {
			writeChangesJSON(w, http.StatusUnprocessableEntity, errorBodyFor("not_implemented", err.Error()))
			return
		}
		writeChangesJSON(w, http.StatusInternalServerError, errorBodyFor("internal", err.Error()))
		return
	}
	writeChangesJSON(w, http.StatusOK, map[string]any{"opened": true})
}
