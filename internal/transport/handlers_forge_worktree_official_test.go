package transport

// forgePanel worktree 6 方法的 L1 契约测试（参数校验、四字段错误、void 成功）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dshgo/internal/worktree"
)

func TestOfficialForgeWorktreeMethodSet(t *testing.T) {
	methods := OfficialForgeWorktreeMethods()
	if len(methods) != 6 {
		t.Fatalf("方法数 = %d，期望 6", len(methods))
	}
	for _, method := range methods {
		if !IsOfficialForgeWorktreeMethod(string(method)) {
			t.Fatalf("%s 没被认领", method)
		}
	}
	if IsOfficialForgeWorktreeMethod("forgePanel.branches") {
		t.Fatal("forgePanel.branches 不属于 worktree 族")
	}
}

func TestOfficialForgeWorktreeWithoutAPI(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/forgePanel.requestWorktree",
		strings.NewReader(`{"sessionId":"s-1"}`))
	srv.ServeOfficialForgeWorktreeAPI(rec, req, string(MethodForgePanelRequestWorktree))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("状态 = %d，期望 501", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %q", rec.Body.String())
	}
	errObj := body["error"].(map[string]any)
	if !strings.Contains(errObj["message"].(string), "forgePanel.requestWorktree") ||
		!strings.Contains(errObj["message"].(string), "OfficialWorktreeAPI") {
		t.Fatalf("501 要点名方法与缺什么: %#v", errObj)
	}
}

func TestOfficialForgeWorktreeParamsAndResults(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	var gotSession string
	var gotFrom *string
	attach := func() {
		srv.AttachOfficialWorktreeAPI(OfficialWorktreeAPI{
			RequestWorktree: func(sessionID string, from *string) error {
				gotSession, gotFrom = sessionID, from
				return nil
			},
			CancelWorktree: func(string) error { return nil },
			RetryWorktree: func(string) error {
				return &worktree.Error{Code: worktree.CodeNotWaiting, Status: 409, Message: "没有等待中的请求"}
			},
			StopCreation:      func(string) error { return nil },
			RestoreWorktree:   func(string) error { return nil },
			ContinueInProject: func(string) error { return nil },
		})
	}
	attach()
	defer srv.DetachOfficialWorktreeAPI()

	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"缺 sessionId", `{}`, 400, "bad_request"},
		{"未知字段", `{"sessionId":"s","extra":1}`, 400, "bad_request"},
		{"sessionId 类型错", `{"sessionId":3}`, 400, "bad_request"},
		{"from 类型错", `{"sessionId":"s","from":3}`, 400, "bad_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(tc.body))
			srv.ServeOfficialForgeWorktreeAPI(rec, req, string(MethodForgePanelRequestWorktree))
			if rec.Code != tc.status {
				t.Fatalf("状态 = %d，期望 %d（%s）", rec.Code, tc.status, rec.Body.String())
			}
		})
	}

	// from=null 与省略等价；from="main" 原样传入。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"sessionId":"s-1","from":null}`))
	srv.ServeOfficialForgeWorktreeAPI(rec, req, string(MethodForgePanelRequestWorktree))
	if rec.Code != 200 {
		t.Fatalf("成功状态 = %d", rec.Code)
	}
	var okBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &okBody); err != nil {
		t.Fatalf("成功响应不是 JSON: %q", rec.Body.String())
	}
	if result, ok := okBody["result"].(map[string]any); !ok || len(result) != 0 {
		t.Fatalf("官方 void → result 空对象，实际 %q", rec.Body.String())
	}
	if gotSession != "s-1" || gotFrom != nil {
		t.Fatalf("from=null 应传 nil：session=%q from=%v", gotSession, gotFrom)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"sessionId":"s-1","from":"main"}`))
	srv.ServeOfficialForgeWorktreeAPI(rec, req, string(MethodForgePanelRequestWorktree))
	if gotFrom == nil || *gotFrom != "main" {
		t.Fatalf("from 未传：%v", gotFrom)
	}

	// 结构化错误翻四字段。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"sessionId":"s-1"}`))
	srv.ServeOfficialForgeWorktreeAPI(rec, req, string(MethodForgePanelRetryWorktree))
	if rec.Code != 409 {
		t.Fatalf("not-waiting 状态 = %d", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"].(map[string]any)["code"] != worktree.CodeNotWaiting {
		t.Fatalf("错误码 = %#v", body["error"])
	}
}
