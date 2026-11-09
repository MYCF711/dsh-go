package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeForgeWriteFailure struct {
	status int
	code   string
	msg    string
}

func (e *fakeForgeWriteFailure) Error() string     { return e.msg }
func (e *fakeForgeWriteFailure) ForgeStatus() int  { return e.status }
func (e *fakeForgeWriteFailure) ForgeCode() string { return e.code }

type forgeWriteCall struct {
	op        string
	sessionID string
	url       string
	method    string
	headSha   string
	request   OfficialForgeReviewRequest
}

func callForgeWrite(t *testing.T, name, body string, api OfficialForgeWriteAPI) (int, map[string]any) {
	t.Helper()
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialForgeWriteAPI(api)
	defer srv.DetachOfficialForgeWriteAPI()
	request := httptest.NewRequest(http.MethodPost, "/api/"+name, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.serveOfficialForgeWriteAPI(recorder, request, name)
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("响应不是 JSON：%v %s", err, recorder.Body.String())
	}
	return recorder.Code, envelope
}

func TestForgeWriteMergeSuccessAndValidation(t *testing.T) {
	var calls []forgeWriteCall
	api := OfficialForgeWriteAPI{
		Merge: func(_ context.Context, sessionID, rawURL, method, headSha string) error {
			calls = append(calls, forgeWriteCall{op: "merge", sessionID: sessionID, url: rawURL, method: method, headSha: headSha})
			return nil
		},
	}
	status, envelope := callForgeWrite(t, "forgePanel.merge",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"squash","headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, api)
	if status != http.StatusOK || envelope["result"] == nil {
		t.Fatalf("merge 成功响应 = %d %+v", status, envelope)
	}
	if len(calls) != 1 || calls[0].method != "squash" || calls[0].headSha[0] != 'a' {
		t.Fatalf("宿主调用 = %+v", calls)
	}
	// 未知参数 / 非法 method / 缺 headSha 必须是 400，且不调用宿主。
	bad := []string{
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"squash","headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","extra":1}`,
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"bogus","headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"squash"}`,
	}
	for _, body := range bad {
		status, _ := callForgeWrite(t, "forgePanel.merge", body, api)
		if status != http.StatusBadRequest {
			t.Fatalf("非法 merge body 应 400，得到 %d：%s", status, body)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("非法参数不应调用宿主：%+v", calls)
	}
}

func TestForgeWriteMarkReadyAcceptsOptionalHead(t *testing.T) {
	var gotHead string
	api := OfficialForgeWriteAPI{
		MarkReady: func(_ context.Context, _, _, headSha string) error {
			gotHead = headSha
			return nil
		},
	}
	status, _ := callForgeWrite(t, "forgePanel.markReady",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7"}`, api)
	if status != http.StatusOK {
		t.Fatalf("markReady 无 headSha 应 200，得到 %d", status)
	}
	if gotHead != "" {
		t.Fatalf("无 headSha 时宿主应收到空串，得到 %q", gotHead)
	}
	status, _ = callForgeWrite(t, "forgePanel.markReady",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","headSha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`, api)
	if status != http.StatusOK || gotHead[0] != 'b' {
		t.Fatalf("带 headSha = %d %q", status, gotHead)
	}
}

func TestForgeWriteSubmitReviewCommentAndReview(t *testing.T) {
	var calls []forgeWriteCall
	api := OfficialForgeWriteAPI{
		SubmitReview: func(_ context.Context, sessionID, rawURL string, request OfficialForgeReviewRequest) (OfficialForgeReviewResult, error) {
			calls = append(calls, forgeWriteCall{op: "review", sessionID: sessionID, url: rawURL, request: request})
			return OfficialForgeReviewResult{OK: true, URL: "https://github.com/o/r/pull/7#review"}, nil
		},
	}
	status, envelope := callForgeWrite(t, "forgePanel.submitReview",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"comment","comment":{"path":"a.go","side":"R","startLine":1,"endLine":4,"body":"nit"}}}`, api)
	if status != http.StatusOK {
		t.Fatalf("comment review = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["ok"] != true || result["url"] == "" {
		t.Fatalf("review result = %+v", result)
	}
	if len(calls) != 1 || calls[0].request.Kind != "comment" || calls[0].request.Comment.EndLine != 4 {
		t.Fatalf("宿主请求 = %+v", calls)
	}
	status, _ = callForgeWrite(t, "forgePanel.submitReview",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"review","event":"request-changes","body":"fix","comments":[{"path":"a.go","side":"L","startLine":2,"endLine":2,"body":"here"}]}}`, api)
	if status != http.StatusOK || len(calls) != 2 || calls[1].request.Kind != "review" || calls[1].request.Event != "request-changes" {
		t.Fatalf("review 请求 = %d %+v", status, calls)
	}
}

func TestForgeWriteReviewValidation(t *testing.T) {
	api := OfficialForgeWriteAPI{
		SubmitReview: func(context.Context, string, string, OfficialForgeReviewRequest) (OfficialForgeReviewResult, error) {
			t.Fatal("非法 request 不应调用宿主")
			return OfficialForgeReviewResult{}, nil
		},
	}
	bad := []string{
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"deadbeef","kind":"comment","comment":{"path":"a","side":"R","startLine":1,"endLine":1,"body":"x"}}}`,
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"comment","comment":{"path":"a","side":"X","startLine":1,"endLine":1,"body":"x"}}}`,
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"comment","comment":{"path":"a","side":"R","startLine":3,"endLine":1,"body":"x"}}}`,
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"review","event":"bogus","body":"x"}}`,
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"review","event":"comment","body":"","comments":[]}}`,
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"comment","comment":{"path":"a","side":"R","startLine":1,"endLine":1,"body":"x"},"extra":1}}`,
	}
	for _, body := range bad {
		status, _ := callForgeWrite(t, "forgePanel.submitReview", body, api)
		if status != http.StatusBadRequest {
			t.Fatalf("非法 review 应 400，得到 %d：%s", status, body)
		}
	}
}

func TestForgeWriteFailureMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{&fakeForgeWriteFailure{status: 409, code: "stale_head", msg: "moved"}, http.StatusConflict, "stale_head"},
		{&fakeForgeWriteFailure{status: 401, code: "auth_required", msg: "login"}, http.StatusUnauthorized, "unauthenticated"},
		{&fakeForgeWriteFailure{status: 403, code: "forbidden", msg: "denied"}, http.StatusForbidden, "permission_denied"},
		{&fakeForgeWriteFailure{status: 502, code: "provider_unavailable", msg: "down"}, http.StatusBadGateway, "unknown"},
		{context.Canceled, 499, "canceled"},
	}
	for _, tc := range cases {
		api := OfficialForgeWriteAPI{
			Merge: func(context.Context, string, string, string, string) error { return tc.err },
		}
		status, envelope := callForgeWrite(t, "forgePanel.merge",
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"merge","headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, api)
		if status != tc.status {
			t.Fatalf("错误 %v 状态 = %d，期望 %d", tc.err, status, tc.status)
		}
		errObj, _ := envelope["error"].(map[string]any)
		if errObj["code"] != tc.code {
			t.Fatalf("错误 %v code = %v，期望 %s", tc.err, errObj["code"], tc.code)
		}
	}
}

func TestForgeWriteUnwiredStays501(t *testing.T) {
	status, _ := callForgeWrite(t, "forgePanel.merge",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"merge","headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		OfficialForgeWriteAPI{})
	if status != http.StatusNotImplemented {
		t.Fatalf("未接线应 501，得到 %d", status)
	}
}
