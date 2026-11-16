package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func callSessionCreateHandler(t *testing.T, srv *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/session.create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleSessionCreate(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON 对象：%q：%v", rec.Body.String(), err)
	}
	return rec.Code, out
}

func TestSessionCreateWorkspaceRoutesByOfficialParameter(t *testing.T) {
	var gotMode, gotWorkspace string
	legacyCalls := 0
	srv := &Server{opt: Options{
		Create: func(mode string) (string, error) {
			legacyCalls++
			return "session-legacy", nil
		},
		CreateInWorkspace: func(mode, workspaceID string) (string, error) {
			gotMode, gotWorkspace = mode, workspaceID
			return "session-workspace", nil
		},
	}}
	code, out := callSessionCreateHandler(t, srv, `{"mode":"read-only","workspaceId":"ws-selected"}`)
	if code != http.StatusOK {
		t.Fatalf("workspaceId 建会话应 200，实际 %d：%v", code, out)
	}
	result := out["result"].(map[string]any)
	if result["sessionId"] != "session-workspace" {
		t.Fatalf("sessionId = %v", result["sessionId"])
	}
	if gotMode != "read-only" || gotWorkspace != "ws-selected" || legacyCalls != 0 {
		t.Fatalf("路由参数错误：mode=%q workspace=%q legacyCalls=%d", gotMode, gotWorkspace, legacyCalls)
	}

	code, out = callSessionCreateHandler(t, srv, `{"mode":"default"}`)
	if code != http.StatusOK || legacyCalls != 1 {
		t.Fatalf("旧 mode 契约必须保持：HTTP=%d calls=%d out=%v", code, legacyCalls, out)
	}
}

func TestSessionCreateWorkspaceNotFoundKeepsOfficialError(t *testing.T) {
	srv := &Server{opt: Options{CreateInWorkspace: func(mode, workspaceID string) (string, error) {
		return "", OfficialWorkspaceErrorDetails("workspace/not-found",
			map[string]any{"workspaceId": workspaceID}, "workspace %q not found", workspaceID)
	}}}
	code, out := callSessionCreateHandler(t, srv, `{"workspaceId":"ws-missing"}`)
	if code != http.StatusNotFound {
		t.Fatalf("不存在的工作区应 404，实际 %d：%v", code, out)
	}
	errObject := out["error"].(map[string]any)
	if errObject["code"] != "not_found" || !strings.HasPrefix(errObject["message"].(string), "workspace/not-found: ") {
		t.Fatalf("官方错误码没保留：%v", errObject)
	}
	details := out["details"].(map[string]any)
	if details["workspaceId"] != "ws-missing" {
		t.Fatalf("details.workspaceId = %v", details["workspaceId"])
	}
}

func TestSessionCreateWorkspaceRejectsCwdAndMalformedInput(t *testing.T) {
	calls := 0
	srv := &Server{opt: Options{
		Create:            func(string) (string, error) { calls++; return "unexpected", nil },
		CreateInWorkspace: func(string, string) (string, error) { calls++; return "unexpected", nil },
	}}
	for _, body := range []string{
		`{"workspaceId":"ws-a","cwd":"/tmp/other"}`,
		`{"workspaceId":42}`,
		`{`,
	} {
		code, out := callSessionCreateHandler(t, srv, body)
		if code != http.StatusBadRequest {
			t.Fatalf("请求 %s 应 400，实际 %d：%v", body, code, out)
		}
	}
	if calls != 0 {
		t.Fatalf("坏请求不得落到建会话回调，calls=%d", calls)
	}
}

func TestSessionCreateWorkspaceRequiresRealWiring(t *testing.T) {
	srv := &Server{opt: Options{Create: func(string) (string, error) {
		t.Fatal("workspaceId 未接线时不得退回进程 cwd")
		return "", nil
	}}}
	code, out := callSessionCreateHandler(t, srv, `{"workspaceId":"ws-a"}`)
	if code != http.StatusNotImplemented {
		t.Fatalf("未接线应 501，实际 %d：%v", code, out)
	}
}
