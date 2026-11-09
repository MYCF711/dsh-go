package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type forgeChangeRequestFake struct {
	checks OfficialForgeChangeRequestChecks
	err    error
}

func (f forgeChangeRequestFake) ChangeRequestChecks(context.Context, OfficialForgeChangeRequestParams) (OfficialForgeChangeRequestChecks, error) {
	return f.checks, f.err
}
func (f forgeChangeRequestFake) ChangeRequestDiff(context.Context, OfficialForgeChangeRequestParams) (OfficialForgeChangeRequestDiff, error) {
	return OfficialForgeChangeRequestDiff{HeadSHA: "abc", Files: []OfficialForgeChangeRequestFile{}}, f.err
}
func (f forgeChangeRequestFake) ChangeRequestText(context.Context, OfficialForgeChangeRequestParams) (string, error) {
	return "package main\n", f.err
}
func (f forgeChangeRequestFake) ChangeRequestStates(context.Context, OfficialForgeChangeRequestParams) (OfficialForgeChangeRequestStatesFrame, error) {
	return OfficialForgeChangeRequestStatesFrame{Type: "baseline", States: []OfficialForgeChangeRequestState{}}, f.err
}

func TestOfficialForgeChangeRequestHTTP(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv.AttachOfficialForgeChangeRequestAPI(forgeChangeRequestFake{checks: OfficialForgeChangeRequestChecks{
		HeadSHA: "abc", Title: "Fix", State: "open", ChecksRollup: "success",
		Checks: []OfficialForgeChangeRequestCheck{{Name: "ci", State: "success"}},
	}})
	t.Cleanup(srv.DetachOfficialForgeChangeRequestAPI)
	cookie := srv.MintInternalCookie()
	for method, body := range map[string]string{
		"forgePanel.changeRequestChecks": `{"sessionId":"s","url":"https://github.com/o/r/pull/1","headSha":"abc"}`,
		"forgePanel.changeRequestDiff":   `{"sessionId":"s","url":"https://github.com/o/r/pull/1"}`,
		"forgePanel.changeRequestText":   `{"sessionId":"s","url":"https://github.com/o/r/pull/1","commit":"abc","path":"a.go"}`,
		"forgePanel.changeRequestStates": `{}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", method, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["result"] == nil {
			t.Fatalf("%s 缺 result：%s err=%v", method, rec.Body.String(), err)
		}
	}
}

func TestOfficialForgeChangeRequestErrorsStructured(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatal(err)
	}
	// 未接线：503 provider_unavailable，不是 501；缺参数仍是 400。
	cookie := srv.MintInternalCookie()
	req := httptest.NewRequest(http.MethodPost, "/api/forgePanel.changeRequestChecks", strings.NewReader(`{"sessionId":"s","url":"https://github.com/o/r/pull/1","headSha":"abc"}`))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "provider_unavailable") {
		t.Fatalf("未接线 = %d %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/forgePanel.changeRequestChecks", strings.NewReader(`{"sessionId":"s"}`))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "缺 url") {
		t.Fatalf("缺参数 = %d %s", rec.Code, rec.Body.String())
	}

	srv.AttachOfficialForgeChangeRequestAPI(forgeChangeRequestFake{err: &ForgeChangeRequestError{Status: 502, Code: "provider_unavailable", Message: "GitHub 网络失败"}})
	t.Cleanup(srv.DetachOfficialForgeChangeRequestAPI)
	req = httptest.NewRequest(http.MethodPost, "/api/forgePanel.changeRequestChecks", strings.NewReader(`{"sessionId":"s","url":"https://github.com/o/r/pull/1","headSha":"abc"}`))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "GitHub 网络失败") {
		t.Fatalf("provider 错误 = %d %s", rec.Code, rec.Body.String())
	}
}
