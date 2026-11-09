// Package forge 的受控写操作：GitHub Change Request merge / markReady /
// submitReview。
//
// 与只读 provider 共用 URL 解析、令牌、API base 与 preflight；所有写请求
// 都先读当前 PR 的 head，并与调用方确认的 headSha 比较。写请求本身**不自动
// 重试**：一旦请求可能已到达 GitHub（网络错误/5xx），返回 unknown 而不是再发
// 一次；同一进程内对同一 (URL, headSha, 请求体) 的成功结果做幂等缓存。
//
// 测试只允许 local httptest；生产 BaseURL 由 DSHD_GITHUB_API_URL /
// GITHUB_TOKEN 控制，未登录时返回结构化 auth_required，不发请求。
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// ReviewComment 是官方 ForgeReviewComment 的宿主形状。
type ReviewComment struct {
	Path      string `json:"path"`
	Side      string `json:"side"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Body      string `json:"body"`
}

// ReviewRequest 是官方 ForgeReviewRequest 的 union（kind=comment|review）。
type ReviewRequest struct {
	HeadSha  string          `json:"headSha"`
	Kind     string          `json:"kind"`
	Comment  *ReviewComment  `json:"comment,omitempty"`
	Event    string          `json:"event,omitempty"`
	Body     string          `json:"body,omitempty"`
	Comments []ReviewComment `json:"comments,omitempty"`
}

// ReviewResult 是官方 ForgeReviewResult 的判别 union。
type ReviewResult struct {
	OK      bool   `json:"ok"`
	URL     string `json:"url,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// WriteProvider 是受控写客户端；mu 让同一进程内的重试串行化，done 缓存
// **成功**结果，保证重复提交不会在远端产生第二条 review/merge。
type WriteProvider struct {
	provider *ChangeRequestProvider

	mu   sync.Mutex
	done map[string]ReviewResult
}

// NewWriteProvider 用已配置的只读 provider 构造写客户端（测试注入 BaseURL）。
func NewWriteProvider(provider *ChangeRequestProvider) *WriteProvider {
	if provider == nil {
		provider = NewChangeRequestProviderFromEnv()
	}
	return &WriteProvider{provider: provider, done: map[string]ReviewResult{}}
}

// NewWriteProviderFromEnv 生产构造：GITHUB_TOKEN/GH_TOKEN，或 `gh auth token`
// 的登录态。未登录不阻止构造，第一次写会得到结构化 auth_required。
func NewWriteProviderFromEnv() *WriteProvider {
	provider := NewChangeRequestProviderFromEnv()
	if provider.Token == "" {
		if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			provider.Token = strings.TrimSpace(string(out))
		}
	}
	return NewWriteProvider(provider)
}

var headShaPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

func validHeadSha(sha string) bool { return headShaPattern.MatchString(strings.TrimSpace(sha)) }

// ForgeStatus / ForgeCode 让 transport 不 import forge 也能读结构化失败。
func (e *ForgeHTTPError) ForgeStatus() int  { return e.Status }
func (e *ForgeHTTPError) ForgeCode() string { return e.Code }

func (w *WriteProvider) cache(key string) (ReviewResult, bool) {
	result, ok := w.done[key]
	return result, ok
}

func (w *WriteProvider) remember(key string, result ReviewResult) {
	if w.done == nil {
		w.done = map[string]ReviewResult{}
	}
	w.done[key] = result
}

func reviewFailure(code, message string) ReviewResult {
	if code == "" {
		code = "failed"
	}
	return ReviewResult{OK: false, Code: code, Message: message}
}

func reviewCode(err error) string {
	var httpErr *ForgeHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Code {
		case "stale_head":
			return "stale"
		case "provider_unavailable":
			return "unknown"
		case "unsupported":
			return "unsupported"
		}
	}
	return "failed"
}

// MarkReady 把 draft PR 标成 ready for review。
//
// 官方 markReady 参数没有 headSha；本宿主允许可选 headSha：给了就做并发校验，
// 没给就取 preflight 的当前 head（不会把已移动的 head 当成确认值）。
func (w *WriteProvider) MarkReady(ctx context.Context, rawURL, headSha string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	ref, err := parseChangeRequestURL(rawURL)
	if err != nil {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: err.Error()}
	}
	if headSha != "" && !validHeadSha(headSha) {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: "headSha 不是完整 commit object name"}
	}
	pr, err := w.provider.fetchPR(ctx, ref)
	if err != nil {
		return err
	}
	current := strings.TrimSpace(pr.Head.SHA)
	if headSha != "" && !strings.EqualFold(headSha, current) {
		return &ForgeHTTPError{Status: http.StatusConflict, Code: "stale_head", Message: "The pull request head has changed"}
	}
	if !strings.EqualFold(pr.State, "open") {
		return &ForgeHTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_spec", Message: "The pull request is not open"}
	}
	key := "ready|" + ref.WebURL + "|" + current
	if _, ok := w.cache(key); ok {
		return nil
	}
	_, err = w.provider.writeJSON(ctx, ref, http.MethodPatch,
		fmt.Sprintf("/repos/%s/%s/pulls/%d", ref.Owner, ref.Repo, ref.Number),
		map[string]any{"draft": false})
	if err != nil {
		return err
	}
	w.remember(key, ReviewResult{OK: true})
	return nil
}

// Merge 以 merge/squash/rebase 合并 PR，并显式带 headSha；GitHub 在 head
// 已移动时返回 409，本方法映射成 stale_head。
func (w *WriteProvider) Merge(ctx context.Context, rawURL, method, headSha string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if method != "merge" && method != "squash" && method != "rebase" {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: "merge method 必须是 merge/squash/rebase"}
	}
	if !validHeadSha(headSha) {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: "headSha 不是完整 commit object name"}
	}
	ref, err := parseChangeRequestURL(rawURL)
	if err != nil {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: err.Error()}
	}
	pr, err := w.provider.fetchPR(ctx, ref)
	if err != nil {
		return err
	}
	if !strings.EqualFold(pr.Head.SHA, headSha) {
		return &ForgeHTTPError{Status: http.StatusConflict, Code: "stale_head", Message: "The pull request head has changed"}
	}
	if !strings.EqualFold(pr.State, "open") {
		return &ForgeHTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_spec", Message: "The pull request is not open"}
	}
	// 幂等缓存必须放在 preflight 之后：head 变了要报 stale，不能被旧成功
	// 结果遮住。
	key := "merge|" + ref.WebURL + "|" + method + "|" + pr.Head.SHA
	if _, ok := w.cache(key); ok {
		return nil
	}
	_, err = w.provider.writeJSON(ctx, ref, http.MethodPut,
		fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", ref.Owner, ref.Repo, ref.Number),
		map[string]any{"merge_method": method, "sha": headSha})
	if err != nil {
		return err
	}
	w.remember(key, ReviewResult{OK: true})
	return nil
}

// SubmitReview 发布一条 inline comment 或一条 review。先读 PR 并比较
// headSha；head 变了返回官方 result 的 {ok:false,code:"stale"}，不写。
func (w *WriteProvider) SubmitReview(ctx context.Context, rawURL string, request ReviewRequest) (ReviewResult, error) {
	if err := ctx.Err(); err != nil {
		return ReviewResult{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := validateReviewRequest(request); err != nil {
		return reviewFailure("failed", err.Error()), nil
	}
	ref, err := parseChangeRequestURL(rawURL)
	if err != nil {
		return ReviewResult{}, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: err.Error()}
	}
	encoded, _ := json.Marshal(request)
	pr, err := w.provider.fetchPR(ctx, ref)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ReviewResult{}, err
		}
		return reviewFailure(reviewCode(err), err.Error()), nil
	}
	if !strings.EqualFold(pr.Head.SHA, request.HeadSha) {
		return reviewFailure("stale", "The pull request head has changed"), nil
	}
	if !strings.EqualFold(pr.State, "open") {
		return reviewFailure("failed", "The pull request is not open"), nil
	}
	key := "review|" + ref.WebURL + "|" + string(encoded) + "|" + pr.Head.SHA
	if result, ok := w.cache(key); ok {
		return result, nil
	}
	path, body := reviewWritePath(ref, request)
	raw, err := w.provider.writeJSON(ctx, ref, http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/pulls/%d/%s", ref.Owner, ref.Repo, ref.Number, path), body)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ReviewResult{}, err
		}
		return reviewFailure(reviewCode(err), err.Error()), nil
	}
	var receipt struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || strings.TrimSpace(receipt.HTMLURL) == "" {
		return reviewFailure("unknown", "GitHub 未返回 review URL"), nil
	}
	result := ReviewResult{OK: true, URL: receipt.HTMLURL}
	w.remember(key, result)
	return result, nil
}

func validateReviewRequest(request ReviewRequest) error {
	if !validHeadSha(request.HeadSha) {
		return errors.New("Invalid review commit")
	}
	switch request.Kind {
	case "comment":
		if request.Comment == nil {
			return errors.New("comment review 缺 comment")
		}
		return validateReviewComment(*request.Comment)
	case "review":
		switch request.Event {
		case "comment", "approve", "request-changes":
		default:
			return fmt.Errorf("review event 非法: %q", request.Event)
		}
		for _, comment := range request.Comments {
			if err := validateReviewComment(comment); err != nil {
				return err
			}
		}
		if request.Event != "approve" && strings.TrimSpace(request.Body) == "" && len(request.Comments) == 0 {
			return errors.New("Empty review")
		}
		return nil
	default:
		return fmt.Errorf("review kind 非法: %q", request.Kind)
	}
}

func validateReviewComment(comment ReviewComment) error {
	if strings.TrimSpace(comment.Path) == "" || strings.TrimSpace(comment.Body) == "" {
		return errors.New("Invalid review comment")
	}
	if comment.Side != "L" && comment.Side != "R" {
		return errors.New("Invalid review comment side")
	}
	if comment.StartLine < 1 || comment.EndLine < comment.StartLine {
		return errors.New("Invalid review comment range")
	}
	return nil
}

func inlineComment(comment ReviewComment) map[string]any {
	side := "RIGHT"
	if comment.Side == "L" {
		side = "LEFT"
	}
	body := map[string]any{
		"path": comment.Path,
		"side": side,
		"line": comment.EndLine,
		"body": comment.Body,
	}
	if comment.StartLine != comment.EndLine {
		body["start_line"] = comment.StartLine
		body["start_side"] = side
	}
	return body
}

func reviewWritePath(ref changeRequestRef, request ReviewRequest) (string, map[string]any) {
	_ = ref
	if request.Kind == "comment" {
		side := "RIGHT"
		if request.Comment.Side == "L" {
			side = "LEFT"
		}
		body := map[string]any{
			"commit_id": request.HeadSha,
			"path":      request.Comment.Path,
			"side":      side,
			"line":      request.Comment.EndLine,
			"body":      request.Comment.Body,
		}
		if request.Comment.StartLine != request.Comment.EndLine {
			body["start_line"] = request.Comment.StartLine
			body["start_side"] = side
		}
		return "comments", body
	}
	events := map[string]string{"comment": "COMMENT", "approve": "APPROVE", "request-changes": "REQUEST_CHANGES"}
	comments := make([]map[string]any, 0, len(request.Comments))
	for _, comment := range request.Comments {
		comments = append(comments, inlineComment(comment))
	}
	return "reviews", map[string]any{
		"commit_id": request.HeadSha,
		"event":     events[request.Event],
		"body":      request.Body,
		"comments":  comments,
	}
}

// writeJSON 是唯一的写请求出口。它不重试；任何可能已经到达上游的失败都
// 映射成 ForgeHTTPError（transport 再决定 unknown/failed），绝不伪造成功。
func (p *ChangeRequestProvider) writeJSON(ctx context.Context, ref changeRequestRef, method, path string, body any) ([]byte, error) {
	if strings.TrimSpace(p.Token) == "" {
		return nil, &ForgeHTTPError{Status: http.StatusUnauthorized, Code: "auth_required", Message: "GitHub 未登录：设置 GITHUB_TOKEN/GH_TOKEN 或 gh auth login"}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.apiBase(ref), "/")+path, strings.NewReader(string(raw)))
	if err != nil {
		return nil, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: err.Error()}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "dsh-go/0.2.2")
	client := p.HTTP
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_unavailable", Message: "GitHub 写请求失败：" + err.Error()}
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_unavailable", Message: "读取 GitHub 写响应失败：" + readErr.Error()}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return responseBody, nil
	}
	code := "provider_error"
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		code = "auth_required"
	case http.StatusForbidden:
		code = "forbidden"
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusConflict:
		code = "stale_head"
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		code = "invalid_spec"
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		code = "provider_unavailable"
	}
	return nil, &ForgeHTTPError{Status: resp.StatusCode, Code: code,
		Message: fmt.Sprintf("GitHub %s %s 返回 HTTP %d: %s", method, path, resp.StatusCode, truncateForge(string(responseBody), 500))}
}
