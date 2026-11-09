package forge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type forgeRequest struct {
	Method string
	Path   string
	Body   string
	Auth   string
}

type forgeFixture struct {
	mu       sync.Mutex
	requests []forgeRequest
	head     string
	state    string
	// writeStatus 让 PUT/PATCH/POST 返回指定错误（0 = 成功）。
	writeStatus int
	// reviewURL 是 review 回执的 html_url。
	reviewURL string
}

func (f *forgeFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	f.mu.Lock()
	f.requests = append(f.requests, forgeRequest{Method: r.Method, Path: r.URL.Path, Body: string(body), Auth: r.Header.Get("Authorization")})
	head, state, writeStatus := f.head, f.state, f.writeStatus
	reviewURL := f.reviewURL
	f.mu.Unlock()

	if r.URL.Path == "/repos/o/r/pulls/7" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 7, "state": state, "head": map[string]any{"sha": head, "ref": "feature"},
			"html_url": "https://github.com/o/r/pull/7",
		})
		return
	}
	if writeStatus != 0 {
		http.Error(w, "fixture refusal", writeStatus)
		return
	}
	switch {
	case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/pulls/7":
		_, _ = w.Write([]byte(`{"number":7,"draft":false}`))
	case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/pulls/7/merge":
		_, _ = w.Write([]byte(`{"merged":true,"sha":"` + head + `"}`))
	case r.Method == http.MethodPost && (r.URL.Path == "/repos/o/r/pulls/7/comments" || r.URL.Path == "/repos/o/r/pulls/7/reviews"):
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"html_url": reviewURL})
	default:
		http.NotFound(w, r)
	}
}

func newFixture(t *testing.T) (*forgeFixture, *httptest.Server) {
	t.Helper()
	fixture := &forgeFixture{head: headA, state: "open", reviewURL: "https://github.com/o/r/pull/7#discussion_r1"}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	return fixture, server
}

func newWrite(t *testing.T, server *httptest.Server) *WriteProvider {
	t.Helper()
	return NewWriteProvider(&ChangeRequestProvider{BaseURL: server.URL, Token: "test-token", HTTP: server.Client()})
}

func (f *forgeFixture) snapshot() []forgeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forgeRequest(nil), f.requests...)
}

func TestMarkReadyPreflightAndIdempotent(t *testing.T) {
	fixture, server := newFixture(t)
	provider := newWrite(t, server)
	if err := provider.MarkReady(context.Background(), "https://github.com/o/r/pull/7", headA); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	requests := fixture.snapshot()
	if len(requests) != 2 || requests[0].Method != http.MethodGet || requests[1].Method != http.MethodPatch {
		t.Fatalf("markReady 请求序列 = %+v", requests)
	}
	if requests[1].Auth != "Bearer test-token" {
		t.Fatalf("写请求没有带 token: %+v", requests[1])
	}
	if !strings.Contains(requests[1].Body, `"draft":false`) {
		t.Fatalf("markReady body = %q", requests[1].Body)
	}
	// 幂等：同一 head 再调用不得再发写请求。
	if err := provider.MarkReady(context.Background(), "https://github.com/o/r/pull/7", headA); err != nil {
		t.Fatalf("second MarkReady: %v", err)
	}
	if got := len(fixture.snapshot()); got != 3 {
		t.Fatalf("重复 markReady 期望一次 preflight（共 3 个请求），实际 %d", got)
	}
	// head 移动：stale，且不得发 PATCH。
	fixture.mu.Lock()
	fixture.head = headB
	fixture.mu.Unlock()
	err := provider.MarkReady(context.Background(), "https://github.com/o/r/pull/7", headA)
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != "stale_head" {
		t.Fatalf("head 移动应 stale_head，实际 %v", err)
	}
	if got := len(fixture.snapshot()); got != 4 {
		t.Fatalf("stale 不应写，请求数 = %d (%+v)", got, fixture.snapshot())
	}
}

func TestMarkReadyRequiresLogin(t *testing.T) {
	_, server := newFixture(t)
	provider := NewWriteProvider(&ChangeRequestProvider{BaseURL: server.URL, HTTP: server.Client()})
	err := provider.MarkReady(context.Background(), "https://github.com/o/r/pull/7", "")
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != "auth_required" {
		t.Fatalf("未登录应 auth_required，实际 %v", err)
	}
}

func TestMergeHeadBodyAndConflict(t *testing.T) {
	fixture, server := newFixture(t)
	provider := newWrite(t, server)
	if err := provider.Merge(context.Background(), "https://github.com/o/r/pull/7", "squash", headA); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	requests := fixture.snapshot()
	if len(requests) != 2 || requests[1].Method != http.MethodPut || requests[1].Path != "/repos/o/r/pulls/7/merge" {
		t.Fatalf("merge 请求 = %+v", requests)
	}
	if !strings.Contains(requests[1].Body, `"merge_method":"squash"`) || !strings.Contains(requests[1].Body, `"sha":"`+headA+`"`) {
		t.Fatalf("merge body = %q", requests[1].Body)
	}
	if err := provider.Merge(context.Background(), "https://github.com/o/r/pull/7", "squash", headA); err != nil {
		t.Fatalf("idempotent merge: %v", err)
	}
	if got := len(fixture.snapshot()); got != 3 {
		t.Fatalf("重复 merge 应只多一次 preflight（共 3 个请求），实际 %d", got)
	}
	fixture.mu.Lock()
	fixture.head = headB
	fixture.mu.Unlock()
	err := provider.Merge(context.Background(), "https://github.com/o/r/pull/7", "merge", headA)
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != "stale_head" {
		t.Fatalf("head 移动应 stale_head，实际 %v", err)
	}
	if got := len(fixture.snapshot()); got != 4 {
		t.Fatalf("stale 不应写，请求数 = %d", got)
	}
}

func TestMergeHTTPConflictIsStale(t *testing.T) {
	fixture, server := newFixture(t)
	fixture.writeStatus = http.StatusConflict
	provider := newWrite(t, server)
	err := provider.Merge(context.Background(), "https://github.com/o/r/pull/7", "merge", headA)
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != "stale_head" || httpErr.Status != http.StatusConflict {
		t.Fatalf("409 应 stale_head，实际 %v", err)
	}
}

func TestMergeCancellationBeforeWriteDoesNotWrite(t *testing.T) {
	fixture, server := newFixture(t)
	provider := newWrite(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := provider.Merge(ctx, "https://github.com/o/r/pull/7", "merge", headA)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled，实际 %v", err)
	}
	if requests := fixture.snapshot(); len(requests) != 0 {
		t.Fatalf("取消后仍发了请求：%+v", requests)
	}
}

func TestSubmitReviewCommentAndReview(t *testing.T) {
	fixture, server := newFixture(t)
	provider := newWrite(t, server)
	comment := ReviewComment{Path: "src/a.go", Side: "R", StartLine: 3, EndLine: 7, Body: "nit"}
	result, err := provider.SubmitReview(context.Background(), "https://github.com/o/r/pull/7", ReviewRequest{
		HeadSha: headA, Kind: "comment", Comment: &comment,
	})
	if err != nil || !result.OK || result.URL == "" {
		t.Fatalf("comment review = %+v, %v", result, err)
	}
	requests := fixture.snapshot()
	if len(requests) != 2 || requests[1].Path != "/repos/o/r/pulls/7/comments" {
		t.Fatalf("comment 请求 = %+v", requests)
	}
	if !strings.Contains(requests[1].Body, `"commit_id":"`+headA+`"`) ||
		!strings.Contains(requests[1].Body, `"start_line":3`) ||
		!strings.Contains(requests[1].Body, `"side":"RIGHT"`) {
		t.Fatalf("comment body = %q", requests[1].Body)
	}
	// 同请求幂等：不再写第二次。
	if _, err := provider.SubmitReview(context.Background(), "https://github.com/o/r/pull/7", ReviewRequest{
		HeadSha: headA, Kind: "comment", Comment: &comment,
	}); err != nil {
		t.Fatalf("idempotent review: %v", err)
	}
	if got := len(fixture.snapshot()); got != 3 {
		t.Fatalf("重复 review 应只多一次 preflight（共 3 个请求），实际 %d", got)
	}

	result, err = provider.SubmitReview(context.Background(), "https://github.com/o/r/pull/7", ReviewRequest{
		HeadSha: headA, Kind: "review", Event: "request-changes", Body: "please fix",
		Comments: []ReviewComment{{Path: "src/a.go", Side: "L", StartLine: 9, EndLine: 9, Body: "here"}},
	})
	if err != nil || !result.OK {
		t.Fatalf("review = %+v, %v", result, err)
	}
	requests = fixture.snapshot()
	if len(requests) != 5 || requests[4].Path != "/repos/o/r/pulls/7/reviews" {
		t.Fatalf("review 请求 = %+v", requests)
	}
	if !strings.Contains(requests[4].Body, `"event":"REQUEST_CHANGES"`) ||
		!strings.Contains(requests[4].Body, `"side":"LEFT"`) {
		t.Fatalf("review body = %q", requests[4].Body)
	}
}

func TestSubmitReviewStaleAndFailure(t *testing.T) {
	fixture, server := newFixture(t)
	provider := newWrite(t, server)
	fixture.mu.Lock()
	fixture.head = headB
	fixture.mu.Unlock()
	result, err := provider.SubmitReview(context.Background(), "https://github.com/o/r/pull/7", ReviewRequest{
		HeadSha: headA, Kind: "comment", Comment: &ReviewComment{Path: "a", Side: "R", StartLine: 1, EndLine: 1, Body: "x"},
	})
	if err != nil || result.OK || result.Code != "stale" {
		t.Fatalf("head 移动应 stale result，实际 %+v, %v", result, err)
	}
	if requests := fixture.snapshot(); len(requests) != 1 {
		t.Fatalf("stale 不应写：%+v", requests)
	}

	fixture.mu.Lock()
	fixture.head = headA
	fixture.writeStatus = http.StatusUnauthorized
	fixture.mu.Unlock()
	result, err = provider.SubmitReview(context.Background(), "https://github.com/o/r/pull/7", ReviewRequest{
		HeadSha: headA, Kind: "comment", Comment: &ReviewComment{Path: "a", Side: "R", StartLine: 1, EndLine: 1, Body: "x"},
	})
	if err != nil || result.OK || result.Code != "failed" {
		t.Fatalf("401 应 failed result，实际 %+v, %v", result, err)
	}
}

func TestSubmitReviewUnknownAfterWrite(t *testing.T) {
	fixture, server := newFixture(t)
	provider := newWrite(t, server)
	fixture.writeStatus = http.StatusBadGateway
	result, err := provider.SubmitReview(context.Background(), "https://github.com/o/r/pull/7", ReviewRequest{
		HeadSha: headA, Kind: "comment", Comment: &ReviewComment{Path: "a", Side: "R", StartLine: 1, EndLine: 1, Body: "x"},
	})
	if err != nil || result.OK || result.Code != "unknown" {
		t.Fatalf("5xx 应 unknown result，实际 %+v, %v", result, err)
	}
}

func TestWriteRejectsInvalidURLAndHead(t *testing.T) {
	_, server := newFixture(t)
	provider := newWrite(t, server)
	if err := provider.Merge(context.Background(), "not-a-url", "merge", headA); err == nil {
		t.Fatal("非法 URL 竟然成功")
	}
	if err := provider.Merge(context.Background(), "https://github.com/o/r/pull/7", "merge", "deadbeef"); err == nil {
		t.Fatal("短 headSha 竟然成功")
	}
	if err := provider.Merge(context.Background(), "https://github.com/o/r/pull/7", "bogus", headA); err == nil {
		t.Fatal("非法 method 竟然成功")
	}
	if _, err := provider.SubmitReview(context.Background(), "https://github.com/o/r/pull/7", ReviewRequest{
		HeadSha: headA, Kind: "comment",
	}); err != nil {
		t.Fatalf("缺 comment 应是 result failed 而不是 transport error：%v", err)
	}
}

func TestProviderHTTPTimeoutDoesNotRetry(t *testing.T) {
	var calls int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		http.Error(w, "slow", http.StatusGatewayTimeout)
	}))
	defer server.Close()
	provider := NewWriteProvider(&ChangeRequestProvider{BaseURL: server.URL, Token: "t", HTTP: &http.Client{Timeout: 10 * time.Millisecond}})
	err := provider.MarkReady(context.Background(), "https://github.com/o/r/pull/7", "")
	if err == nil {
		t.Fatal("超时竟然成功")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		// 预检在超时前已经发出；关键是写请求没有重试：这里只允许 0 或 1 次。
		if calls > 1 {
			t.Fatalf("写操作被重试了 %d 次", calls)
		}
	}
}

func TestMergeRetryAfterFailureWritesAgain(t *testing.T) {
	fixture, server := newFixture(t)
	fixture.writeStatus = http.StatusBadGateway
	provider := newWrite(t, server)
	err := provider.Merge(context.Background(), "https://github.com/o/r/pull/7", "merge", headA)
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != "provider_unavailable" {
		t.Fatalf("5xx 应 provider_unavailable，实际 %v", err)
	}
	fixture.mu.Lock()
	fixture.writeStatus = 0
	fixture.mu.Unlock()
	if err := provider.Merge(context.Background(), "https://github.com/o/r/pull/7", "merge", headA); err != nil {
		t.Fatalf("失败后重试应成功：%v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	writes := 0
	for _, request := range fixture.requests {
		if request.Method == http.MethodPut {
			writes++
		}
	}
	if writes != 2 {
		t.Fatalf("失败后重试应写两次（第一次 5xx、第二次成功），实际 %d", writes)
	}
}
