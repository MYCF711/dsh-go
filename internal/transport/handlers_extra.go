package transport

import (
	"encoding/json"
	"errors"
	"net/http"
)

// handleSessionDelete 是 /api/session.delete。
//
// 删会话不可逆，所以这里只做三件事：校验参数、调宿主、把宿主的错误原文回给界面。
// **不在协议层自作主张**（不缓存、不"删不动就当成功"）。
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if s.opt.Delete == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("session.delete 未接线"))
		return
	}
	var req struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("请求体不是 JSON 对象"))
		return
	}
	if req.SessionID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("缺 sessionId"))
		return
	}
	if err := s.opt.Delete(req.SessionID); err != nil {
		// 找不到会话和删失败必须分开：前者是 404（界面换个 id 重试），
		// 后者是 500（界面该提示"这次没删掉"）。
		if errors.Is(err, ErrNoSuchSession) {
			writeJSON(w, http.StatusNotFound, errorBody("会话不存在"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBody("删除失败："+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleSessionFork 是 /api/session.fork：从某条消息开一个分支。
func (s *Server) handleSessionFork(w http.ResponseWriter, r *http.Request) {
	if s.opt.Fork == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("session.fork 未接线"))
		return
	}
	var req struct {
		SessionID string `json:"sessionId"`
		Title     string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("请求体不是 JSON 对象"))
		return
	}
	if req.SessionID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("缺 sessionId"))
		return
	}
	id, err := s.opt.Fork(req.SessionID, req.Title)
	if err != nil {
		if errors.Is(err, ErrNoSuchSession) {
			writeJSON(w, http.StatusNotFound, errorBody("会话不存在"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBody("分支失败："+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": id})
}
