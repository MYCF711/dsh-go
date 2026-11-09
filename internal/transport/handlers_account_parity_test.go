package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// PARITY-ACCOUNT：getState/hasRunningAccountTasks/watch/watchExpiry 的 L1 契约。
func TestOfficialAccountStateAndStreams(t *testing.T) {
	watch := make(chan OfficialAccountView, 2)
	expiry := make(chan string, 2)
	api := OfficialAccountAPI{
		AccountGetState: func(context.Context) (OfficialAccountView, error) {
			return OfficialAccountView{Status: "credential-stored", Source: "local",
				Links: OfficialAccountLinks{UsageURL: "", TopUpURL: ""}}, nil
		},
		AccountHasRunningTasks: func(context.Context) (bool, error) { return true, nil },
		AccountWatch: func(context.Context) (<-chan OfficialAccountView, error) {
			return watch, nil
		},
		AccountWatchExpiry: func(context.Context) (<-chan string, error) {
			return expiry, nil
		},
	}
	srv := accountTestServer(t, api)
	code, out := callOfficialAccount(t, srv, string(MethodAccountGetState), map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("getState HTTP=%d：%v", code, out)
	}
	result := out["result"].(map[string]any)
	if result["status"] != "credential-stored" || result["source"] != "local" {
		t.Fatalf("getState 结果：%v", result)
	}
	code, out = callOfficialAccount(t, srv, string(MethodAccountHasRunningTasks), map[string]any{})
	if code != http.StatusOK || out["result"] != true {
		t.Fatalf("hasRunningAccountTasks：HTTP=%d %v", code, out)
	}

	// watch：首帧 NDJSON，不包 result 信封。
	watch <- OfficialAccountView{Status: "credential-stored", Source: "local"}
	close(watch)
	req := httptest.NewRequest(http.MethodPost, "/api/account.watch", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.serveOfficialAccountAPI(rec, req, string(MethodAccountWatch))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("watch HTTP=%d ct=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	var view OfficialAccountView
	if err := json.Unmarshal([]byte(strings.TrimSpace(rec.Body.String())), &view); err != nil {
		t.Fatalf("watch 帧不是 JSON：%v body=%s", err, rec.Body.String())
	}
	if view.Source != "local" || view.Status != "credential-stored" {
		t.Fatalf("watch 帧：%+v", view)
	}

	expiry <- "session-expired"
	close(expiry)
	req = httptest.NewRequest(http.MethodPost, "/api/account.watchExpiry", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	srv.serveOfficialAccountAPI(rec, req, string(MethodAccountWatchExpiry))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `"session-expired"` {
		t.Fatalf("watchExpiry HTTP=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestOfficialAccountStreamUnwiredIs501(t *testing.T) {
	srv := accountTestServer(t, OfficialAccountAPI{})
	for _, method := range []Method{MethodAccountWatch, MethodAccountWatchExpiry} {
		code, out := callOfficialAccount(t, srv, string(method), map[string]any{})
		if code != http.StatusNotImplemented {
			t.Fatalf("%s 未接线 HTTP=%d，期望 501：%v", method, code, out)
		}
	}
}
