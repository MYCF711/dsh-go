package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/transport"
)

const forgeTestHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type forgeWriteFixture struct {
	mu       sync.Mutex
	requests []string
	head     string
	write    int
}

func (f *forgeWriteFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path+" "+string(body))
	head, write := f.head, f.write
	f.mu.Unlock()
	if r.URL.Path == "/repos/o/r/pulls/7" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 7, "state": "open", "head": map[string]any{"sha": head},
			"html_url": "https://github.com/o/r/pull/7",
		})
		return
	}
	if write != 0 {
		http.Error(w, "refused", write)
		return
	}
	if r.Method == http.MethodPost {
		_ = json.NewEncoder(w).Encode(map[string]any{"html_url": "https://github.com/o/r/pull/7#review"})
		return
	}
	_, _ = w.Write([]byte(`{"merged":true}`))
}

func forgeWriteHTTPServer(t *testing.T) (*forgeWriteFixture, *httptest.Server, *httptest.Server, string) {
	t.Helper()
	fixture := &forgeWriteFixture{head: forgeTestHead}
	upstream := httptest.NewServer(fixture)
	t.Cleanup(upstream.Close)
	t.Setenv("DSHD_GITHUB_API_URL", upstream.URL)
	t.Setenv("GITHUB_TOKEN", "test-token")
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialForgeWriteAPI(newForgeWriteAPI(t.TempDir()))
	api := httptest.NewServer(srv)
	t.Cleanup(api.Close)
	return fixture, upstream, api, srv.MintInternalCookie()
}

func postForgeWrite(t *testing.T, base, cookie, method, body string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+"/api/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "dshd_session", Value: cookie})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("响应不是 JSON：%v %s", err, raw)
	}
	return response.StatusCode, envelope
}

func TestForgeWriteHTTPMergeAndMarkReady(t *testing.T) {
	fixture, _, api, cookie := forgeWriteHTTPServer(t)
	status, envelope := postForgeWrite(t, api.URL, cookie, "forgePanel.merge",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"squash","headSha":"`+forgeTestHead+`"}`)
	if status != http.StatusOK || envelope["result"] == nil {
		t.Fatalf("merge = %d %+v", status, envelope)
	}
	fixture.mu.Lock()
	requests := append([]string(nil), fixture.requests...)
	fixture.mu.Unlock()
	if len(requests) != 2 || !strings.HasPrefix(requests[1], "PUT ") || !strings.Contains(requests[1], `"sha":"`+forgeTestHead+`"`) {
		t.Fatalf("上游请求 = %+v", requests)
	}

	status, _ = postForgeWrite(t, api.URL, cookie, "forgePanel.markReady",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","headSha":"`+forgeTestHead+`"}`)
	if status != http.StatusOK {
		t.Fatalf("markReady = %d", status)
	}
}

func TestForgeWriteHTTPStaleAndAuth(t *testing.T) {
	fixture, _, api, cookie := forgeWriteHTTPServer(t)
	fixture.mu.Lock()
	fixture.head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fixture.mu.Unlock()
	status, envelope := postForgeWrite(t, api.URL, cookie, "forgePanel.merge",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"merge","headSha":"`+forgeTestHead+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("head 移动应 409，得到 %d %+v", status, envelope)
	}
	errObj, _ := envelope["error"].(map[string]any)
	if errObj["code"] != "stale_head" {
		t.Fatalf("stale code = %+v", errObj)
	}
}

func TestForgeWriteHTTPReview(t *testing.T) {
	_, _, api, cookie := forgeWriteHTTPServer(t)
	status, envelope := postForgeWrite(t, api.URL, cookie, "forgePanel.submitReview",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"`+forgeTestHead+`","kind":"comment","comment":{"path":"a.go","side":"R","startLine":1,"endLine":2,"body":"nit"}}}`)
	if status != http.StatusOK {
		t.Fatalf("review = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["ok"] != true || result["url"] != "https://github.com/o/r/pull/7#review" {
		t.Fatalf("review result = %+v", result)
	}
}

func TestForgeWriteHTTPUnwired501(t *testing.T) {
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	api := httptest.NewServer(srv)
	defer api.Close()
	cookie := srv.MintInternalCookie()
	status, _ := postForgeWrite(t, api.URL, cookie, "forgePanel.merge",
		`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"merge","headSha":"`+forgeTestHead+`"}`)
	if status != http.StatusNotImplemented {
		t.Fatalf("未接线应 501，得到 %d", status)
	}
}
