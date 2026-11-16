package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTerminalInputScopedForwardsSessionAndTyped(t *testing.T) {
	var gotSession, gotID, gotData string
	var gotTyped bool
	s := &Server{opt: Options{TerminalInputScoped: func(sessionID, id, data string, typed bool) error {
		gotSession, gotID, gotData, gotTyped = sessionID, id, data, typed
		return nil
	}}}
	req := httptest.NewRequest(http.MethodPost, "/api/terminal.input", strings.NewReader(
		`{"sessionId":"s1","id":"t1","data":"echo hi\n","typed":true}`))
	rec := httptest.NewRecorder()
	s.handleTerminalInput(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped input 失败：%d %s", rec.Code, rec.Body.String())
	}
	if gotSession != "s1" || gotID != "t1" || gotData != "echo hi\n" || !gotTyped {
		t.Fatalf("官方 write 字段未原样转发：session=%q id=%q data=%q typed=%v", gotSession, gotID, gotData, gotTyped)
	}
}

func TestTerminalInputScopedDoesNotGuessWithoutCallback(t *testing.T) {
	legacyCalled := false
	s := &Server{opt: Options{TerminalInput: func(string, string) error {
		legacyCalled = true
		return nil
	}}}
	req := httptest.NewRequest(http.MethodPost, "/api/terminal.input", strings.NewReader(
		`{"agentId":"s1","id":"t1","data":"x","typed":true}`))
	rec := httptest.NewRecorder()
	s.handleTerminalInput(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("带会话的官方上行未接线必须 501：%d %s", rec.Code, rec.Body.String())
	}
	if legacyCalled {
		t.Fatal("带 sessionId 时不许退化成按 id 猜终端")
	}
}
