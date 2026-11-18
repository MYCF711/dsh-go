package transport

import (
	"encoding/json"
	"net/http"
)

// TerminalOutput 是 `terminal.output` 的返回。
type TerminalOutput struct {
	Output string `json:"output"`
	// Next 是下次调用该传的 `since`（字节偏移）。
	// **增量而不是全量**：一个跑了半小时的终端，每帧整段传会把界面拖死。
	Next int  `json:"next"`
	Dead bool `json:"dead"`
}

// handleTerminalOpen 是 /api/terminal.open：起一条长连接 shell。
//
// 参数 `cwd` 可选（空 = 宿主当前工作目录）。
func (s *Server) handleTerminalOpen(w http.ResponseWriter, r *http.Request) {
	if s.opt.TerminalOpen == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("terminal.open 未接线"))
		return
	}
	var params struct {
		Cwd string `json:"cwd"`
	}
	_ = json.NewDecoder(r.Body).Decode(&params) // 空 body 合法（用默认目录）
	id, err := s.opt.TerminalOpen(params.Cwd)
	if err != nil {
		// **502 而不是 500**：起不来说明底层 shell 有问题（不是我们的代码错）。
		// 码按 internal 报：这是宿主这一侧起不来，客户端重试是合理的。
		writeJSON(w, http.StatusBadGateway, errorBodyFor("internal", "起终端失败："+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"id": id}})
}

// handleTerminalInput 是 /api/terminal.input：往终端写一段（原样，含换行）。
func (s *Server) handleTerminalInput(w http.ResponseWriter, r *http.Request) {
	var params struct {
		SessionID string `json:"sessionId"`
		AgentID   string `json:"agentId"`
		ID        string `json:"id"`
		Data      string `json:"data"`
		Typed     bool   `json:"typed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "请求体不是 JSON 对象"))
		return
	}
	if params.ID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 id：往哪个终端写"))
		return
	}
	sessionID := params.SessionID
	if sessionID == "" {
		sessionID = params.AgentID
	}
	if sessionID != "" {
		if s.opt.TerminalInputScoped == nil {
			writeJSON(w, http.StatusNotImplemented, errorBody("terminal.input 的 sessionId/typed 上行未接线"))
			return
		}
		if err := s.opt.TerminalInputScoped(sessionID, params.ID, params.Data, params.Typed); err != nil {
			writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"ok": true}})
		return
	}
	if s.opt.TerminalInput == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("terminal.input 未接线"))
		return
	}
	if err := s.opt.TerminalInput(params.ID, params.Data); err != nil {
		// 404 只表示"这个终端不在了"；别的失败（写不动）不该混进这个码里。
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"ok": true}})
}

// handleTerminalResize 是 /api/terminal.resize：改终端尺寸。
//
// **这是对 `terminal.input` 那条回落投影的同型补全**，不是官方 HTTP 面：
// 官方 uplink 的 write/resize 都只在 L2 mux 的 attach 帧机上（原文见
// `handlers_job_terminal_official.go:1300`）。本宿主没有 officialwire mux 客户端，
// 所以 resize 也要一条 HTTP 回落投影，否则界面在无 mux 时改不了 PTY 尺寸。
//
// 参数与 `terminal.input` 同型：`sessionId`（兼容旧名的 `agentId`）+ `id`，
// 再加官方帧里的 `cols`/`rows`。**官方档位由接线方校验**（cols∈[2,maxCols]、
// rows∈[1,maxRows]，越界是报错不是截断）：这里只挡结构错误（缺 id/session、
// 尺寸不是正整数），档位不在本层另立一份——那就会出现两个真相。
func (s *Server) handleTerminalResize(w http.ResponseWriter, r *http.Request) {
	var params struct {
		SessionID string `json:"sessionId"`
		AgentID   string `json:"agentId"`
		ID        string `json:"id"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "请求体不是 JSON 对象"))
		return
	}
	if params.ID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 id：改哪个终端的尺寸"))
		return
	}
	sessionID := params.SessionID
	if sessionID == "" {
		sessionID = params.AgentID
	}
	if sessionID == "" {
		// 和 terminal.input 的区别在这里：那边有"只给 id"的既有管道终端回落，
		// resize 没有——官方 resize 属于会话内的终端，缺会话就只能拒绝（不猜）。
		writeJSON(w, http.StatusBadRequest,
			errorBodyFor("bad_request", "缺 sessionId：resize 只在会话内的终端上有意义"))
		return
	}
	if params.Cols <= 0 || params.Rows <= 0 {
		writeJSON(w, http.StatusBadRequest,
			errorBodyFor("bad_request", "cols/rows 必须是正整数（官方帧是 {type:'resize',cols,rows}）"))
		return
	}
	if s.opt.TerminalResize == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "terminal.resize 未接线"))
		return
	}
	if err := s.opt.TerminalResize(sessionID, params.ID, params.Cols, params.Rows); err != nil {
		// 复用官方终端域**唯一**的错误映射：参数越界 400、跨会话 403、终端没了 404、
		// 未支持 501。自己另写一套就会出现"HTTP 回 404、mux 回 400"这种只在一侧复现的错。
		writeJTOfficialError(w, string(MethodTerminalResize), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"ok": true}})
}

// handleTerminalOutput 是 /api/terminal.output：取增量输出。
//
// 参数 `since` 是上次拿到的 `next`（0 = 从头）。
func (s *Server) handleTerminalOutput(w http.ResponseWriter, r *http.Request) {
	if s.opt.TerminalOutput == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("terminal.output 未接线"))
		return
	}
	var params struct {
		ID    string `json:"id"`
		Since int    `json:"since"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "请求体不是 JSON 对象"))
		return
	}
	if params.ID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 id：取哪个终端的输出"))
		return
	}
	out, err := s.opt.TerminalOutput(params.ID, params.Since)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": out})
}
