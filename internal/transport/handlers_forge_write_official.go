package transport

// forgePanel merge / markReady / submitReview 的受控写操作契约层。
//
// 这三个方法没有本地假值：宿主必须提供 provider 写客户端（GitHub HTTP），
// 每次写前校验 PR 当前 head 与调用方 headSha；未接线回 501，未登录/无权限/
// head 变化回四字段错误，review 失败回官方 ForgeReviewResult union。
//
// 分派接线（WIRING）：`serveOfficialForgeLiveshareAPI` 在进入旧 501 前先
// 判断 `IsOfficialForgeWriteMethod(name) && s.hasOfficialForgeWriteAPI()`，
// 有实现时走本文件；没有实现时保持原 501。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// OfficialForgeReviewComment 是官方 ForgeReviewComment。
type OfficialForgeReviewComment struct {
	Path      string `json:"path"`
	Side      string `json:"side"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Body      string `json:"body"`
}

// OfficialForgeReviewRequest 是官方 ForgeReviewRequest 的 union。
type OfficialForgeReviewRequest struct {
	HeadSha  string                       `json:"headSha"`
	Kind     string                       `json:"kind"`
	Comment  *OfficialForgeReviewComment  `json:"comment,omitempty"`
	Event    string                       `json:"event,omitempty"`
	Body     string                       `json:"body,omitempty"`
	Comments []OfficialForgeReviewComment `json:"comments,omitempty"`
}

// OfficialForgeReviewResult 是官方 ForgeReviewResult 的判别 union。
type OfficialForgeReviewResult struct {
	OK      bool   `json:"ok"`
	URL     string `json:"url,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// OfficialForgeWriteAPI 是宿主实现函数表。任一为 nil 表示未接线 → 501。
type OfficialForgeWriteAPI struct {
	MarkReady    func(ctx context.Context, sessionID, url, headSha string) error
	Merge        func(ctx context.Context, sessionID, url, method, headSha string) error
	SubmitReview func(ctx context.Context, sessionID, url string, request OfficialForgeReviewRequest) (OfficialForgeReviewResult, error)
}

var forgeWriteMount = struct {
	mu     sync.RWMutex
	byServ map[*Server]OfficialForgeWriteAPI
}{byServ: map[*Server]OfficialForgeWriteAPI{}}

// AttachOfficialForgeWriteAPI 给这个 Server 挂上真实写实现。
func (s *Server) AttachOfficialForgeWriteAPI(api OfficialForgeWriteAPI) {
	if s == nil {
		return
	}
	forgeWriteMount.mu.Lock()
	defer forgeWriteMount.mu.Unlock()
	forgeWriteMount.byServ[s] = api
}

// DetachOfficialForgeWriteAPI 摘掉（测试清理用）。
func (s *Server) DetachOfficialForgeWriteAPI() {
	if s == nil {
		return
	}
	forgeWriteMount.mu.Lock()
	defer forgeWriteMount.mu.Unlock()
	delete(forgeWriteMount.byServ, s)
}

func (s *Server) officialForgeWriteAPI() OfficialForgeWriteAPI {
	if s == nil {
		return OfficialForgeWriteAPI{}
	}
	forgeWriteMount.mu.RLock()
	defer forgeWriteMount.mu.RUnlock()
	return forgeWriteMount.byServ[s]
}

func (s *Server) hasOfficialForgeWriteAPI() bool {
	api := s.officialForgeWriteAPI()
	return api.MarkReady != nil || api.Merge != nil || api.SubmitReview != nil
}

// IsOfficialForgeWriteMethod 报告是不是本文件实现的 3 个写方法。
func IsOfficialForgeWriteMethod(name string) bool {
	switch Method(name) {
	case MethodForgePanelMerge, MethodForgePanelMarkReady, MethodForgePanelSubmitReview:
		return true
	}
	return false
}

type forgeWriteFailure interface {
	error
	ForgeStatus() int
	ForgeCode() string
}

func writeForgeWriteFailure(w http.ResponseWriter, err error) {
	var failure forgeWriteFailure
	if errors.As(err, &failure) {
		status, code := http.StatusBadGateway, "action_failed"
		switch failure.ForgeCode() {
		case "stale_head":
			status, code = http.StatusConflict, "stale_head"
		case "auth_required":
			status, code = http.StatusUnauthorized, "unauthenticated"
		case "forbidden":
			status, code = http.StatusForbidden, "permission_denied"
		case "provider_unavailable":
			status, code = http.StatusBadGateway, "unknown"
		case "invalid_spec", "not_found":
			status, code = http.StatusUnprocessableEntity, "action_failed"
		}
		writeJSON(w, status, errorBodyFor(code, failure.Error()))
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeJSON(w, 499, errorBodyFor("canceled", "写操作在完成前被取消；远端是否已写入未知，重试前先刷新 PR 状态"))
		return
	}
	writeJSON(w, http.StatusBadGateway, errorBodyFor("action_failed", err.Error()))
}

// serveOfficialForgeWriteAPI 处理 3 个写方法；调用方已确认有实现。
func (s *Server) serveOfficialForgeWriteAPI(w http.ResponseWriter, r *http.Request, name string) {
	api := s.officialForgeWriteAPI()
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
	rawURL, err := params.str("url")
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	if strings.TrimSpace(sessionID) == "" {
		writeOfficialError(w, officialBadRequest("sessionId 是空串"))
		return
	}
	if strings.TrimSpace(rawURL) == "" {
		writeOfficialError(w, officialBadRequest("url 是空串"))
		return
	}
	switch Method(name) {
	case MethodForgePanelMarkReady:
		headSha := ""
		if raw, ok := params.take("headSha"); ok {
			if err := json.Unmarshal(raw, &headSha); err != nil || strings.TrimSpace(headSha) == "" {
				writeOfficialError(w, officialBadRequest("headSha 必须是非空字符串"))
				return
			}
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.MarkReady == nil {
			writeOfficialError(w, fmt.Errorf("%w：forgePanel.markReady 未接线", ErrOfficialNotImplemented))
			return
		}
		if err := api.MarkReady(r.Context(), sessionID, rawURL, headSha); err != nil {
			writeForgeWriteFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case MethodForgePanelMerge:
		method, err := params.str("method")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		switch method {
		case "merge", "squash", "rebase":
		default:
			writeOfficialError(w, officialBadRequest("method 只能是 merge/squash/rebase，收到 %q", method))
			return
		}
		headSha, err := params.str("headSha")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if strings.TrimSpace(headSha) == "" {
			writeOfficialError(w, officialBadRequest("headSha 是空串"))
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.Merge == nil {
			writeOfficialError(w, fmt.Errorf("%w：forgePanel.merge 未接线", ErrOfficialNotImplemented))
			return
		}
		if err := api.Merge(r.Context(), sessionID, rawURL, method, headSha); err != nil {
			writeForgeWriteFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case MethodForgePanelSubmitReview:
		rawRequest, ok := params.take("request")
		if !ok {
			writeOfficialError(w, officialBadRequest("缺 request"))
			return
		}
		request, err := parseOfficialForgeReviewRequest(rawRequest)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.SubmitReview == nil {
			writeOfficialError(w, fmt.Errorf("%w：forgePanel.submitReview 未接线", ErrOfficialNotImplemented))
			return
		}
		result, err := api.SubmitReview(r.Context(), sessionID, rawURL, request)
		if err != nil {
			writeForgeWriteFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	default:
		writeOfficialError(w, fmt.Errorf("%w：不是 forgePanel 写方法 %q", ErrOfficialNotImplemented, name))
	}
}

func parseOfficialForgeReviewRequest(raw json.RawMessage) (OfficialForgeReviewRequest, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return OfficialForgeReviewRequest{}, officialBadRequest("request 必须是 JSON 对象")
	}
	var request OfficialForgeReviewRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return OfficialForgeReviewRequest{}, officialBadRequest("request 形状非法：%v", err)
	}
	if !validForgeHeadSha(request.HeadSha) {
		return OfficialForgeReviewRequest{}, officialBadRequest("request.headSha 必须是 40/64 位 commit object name")
	}
	allowed := map[string]bool{"headSha": true, "kind": true}
	switch request.Kind {
	case "comment":
		allowed["comment"] = true
	case "review":
		allowed["event"] = true
		allowed["body"] = true
		allowed["comments"] = true
	default:
		return OfficialForgeReviewRequest{}, officialBadRequest("request.kind 只能是 comment 或 review")
	}
	for key := range object {
		if !allowed[key] {
			return OfficialForgeReviewRequest{}, officialBadRequest("request 不认识的字段 %q（kind=%s）", key, request.Kind)
		}
	}
	if request.Kind == "comment" {
		if request.Comment == nil {
			return OfficialForgeReviewRequest{}, officialBadRequest("comment review 缺 comment")
		}
		if err := validateOfficialForgeComment(*request.Comment); err != nil {
			return OfficialForgeReviewRequest{}, err
		}
		return request, nil
	}
	switch request.Event {
	case "comment", "approve", "request-changes":
	default:
		return OfficialForgeReviewRequest{}, officialBadRequest("request.event 只能是 comment/approve/request-changes")
	}
	for _, comment := range request.Comments {
		if err := validateOfficialForgeComment(comment); err != nil {
			return OfficialForgeReviewRequest{}, err
		}
	}
	if request.Event != "approve" && strings.TrimSpace(request.Body) == "" && len(request.Comments) == 0 {
		return OfficialForgeReviewRequest{}, officialBadRequest("review 既没有 body 也没有 comments")
	}
	return request, nil
}

func validateOfficialForgeComment(comment OfficialForgeReviewComment) error {
	if strings.TrimSpace(comment.Path) == "" {
		return officialBadRequest("comment.path 是空串")
	}
	if comment.Side != "L" && comment.Side != "R" {
		return officialBadRequest("comment.side 只能是 L 或 R")
	}
	if comment.StartLine < 1 || comment.EndLine < comment.StartLine {
		return officialBadRequest("comment 行号非法：startLine=%d endLine=%d", comment.StartLine, comment.EndLine)
	}
	if strings.TrimSpace(comment.Body) == "" {
		return officialBadRequest("comment.body 是空串")
	}
	return nil
}

func validForgeHeadSha(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for _, r := range sha {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
