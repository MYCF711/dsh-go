package transport

// 5 个原生方法的 L1 契约测试（参数/结果/四字段/未接线）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dshgo/internal/native"
)

func TestOfficialNativeMethodSet(t *testing.T) {
	methods := []string{
		"directoryPicker.pick", "settings.openSettingsDocument",
		"session.canOpenWorkspacePath", "session.openWorkspacePath",
		"session.workspacePathApplications",
	}
	for _, method := range methods {
		if !IsOfficialNativeMethod(method) {
			t.Fatalf("%s 没被认领", method)
		}
	}
	if IsOfficialNativeMethod("session.list") {
		t.Fatal("session.list 不属于原生方法")
	}
}

func TestOfficialNativeWithoutAPI(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/directoryPicker.pick", strings.NewReader(`{}`))
	srv.ServeOfficialNativeAPI(rec, req, string(MethodDirectoryPickerPick))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("状态 = %d，期望 501", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	errObj := body["error"].(map[string]any)
	if !strings.Contains(errObj["message"].(string), "directoryPicker.pick") {
		t.Fatalf("501 要点名方法: %#v", errObj)
	}
}

func TestOfficialNativeParamsAndResults(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	var calls []string
	icon := "data:image/png;base64,AA=="
	srv.AttachOfficialNativeAPI(OfficialNativeAPI{
		PickDirectory: func(context.Context) (string, bool, error) {
			calls = append(calls, "pick")
			return `C:\picked`, true, nil
		},
		OpenSettingsDocument: func(context.Context) error {
			calls = append(calls, "settings")
			return nil
		},
		CanOpenWorkspacePath: func() (bool, error) {
			calls = append(calls, "can")
			return true, nil
		},
		OpenWorkspacePath: func(_ context.Context, path, action, application string) error {
			calls = append(calls, "open:"+path+":"+action+":"+application)
			return nil
		},
		WorkspacePathApplications: func(context.Context, string) ([]OfficialWorkspacePathApplication, error) {
			calls = append(calls, "apps")
			return []OfficialWorkspacePathApplication{{ID: "C:\\app.exe", Name: "App", Default: true, Icon: &icon}}, nil
		},
	})
	defer srv.DetachOfficialNativeAPI()

	post := func(method, body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
		srv.ServeOfficialNativeAPI(rec, req, method)
		return rec.Code, rec.Body.String()
	}

	// pick：字符串结果
	if status, body := post("directoryPicker.pick", `{}`); status != 200 {
		t.Fatalf("pick = %d %q", status, body)
	} else {
		var decoded map[string]any
		_ = json.Unmarshal([]byte(body), &decoded)
		if decoded["result"] != `C:\picked` {
			t.Fatalf("pick result = %#v（%q）", decoded["result"], body)
		}
	}
	// pick：用户取消 → 官方 null
	srv.AttachOfficialNativeAPI(OfficialNativeAPI{
		PickDirectory: func(context.Context) (string, bool, error) { return "", false, nil },
	})
	if status, body := post("directoryPicker.pick", `{}`); status != 200 || !strings.Contains(body, "null") {
		t.Fatalf("pick 取消 = %d %q", status, body)
	}

	// 重新挂完整实现
	srv.AttachOfficialNativeAPI(OfficialNativeAPI{
		OpenSettingsDocument: func(context.Context) error { return nil },
		CanOpenWorkspacePath: func() (bool, error) { return true, nil },
		OpenWorkspacePath: func(_ context.Context, path, action, application string) error {
			calls = append(calls, "open:"+path+":"+action+":"+application)
			return nil
		},
		WorkspacePathApplications: func(context.Context, string) ([]OfficialWorkspacePathApplication, error) {
			return []OfficialWorkspacePathApplication{{ID: "C:\\app.exe", Name: "App", Default: true, Icon: &icon}}, nil
		},
	})
	if status, body := post("settings.openSettingsDocument", `{}`); status != 200 || !strings.Contains(body, `"opened":true`) {
		t.Fatalf("settings = %d %q", status, body)
	}
	if status, body := post("session.canOpenWorkspacePath", `{}`); status != 200 || !strings.Contains(body, `"result":true`) {
		t.Fatalf("canOpen = %d %q", status, body)
	}
	if status, body := post("session.openWorkspacePath", `{"path":"C:\\a.txt","action":"reveal"}`); status != 200 ||
		!strings.Contains(body, `"opened":true`) {
		t.Fatalf("open reveal = %d %q", status, body)
	}
	if status, body := post("session.workspacePathApplications", `{"path":"C:\\a.txt"}`); status != 200 ||
		!strings.Contains(body, `"default":true`) || !strings.Contains(body, `"icon":"data:image/png;base64,AA=="`) {
		t.Fatalf("applications = %d %q", status, body)
	}

	// 参数错误
	if status, body := post("session.openWorkspacePath", `{}`); status != 400 || !strings.Contains(body, "path") {
		t.Fatalf("缺 path = %d %q", status, body)
	}
	if status, body := post("session.openWorkspacePath", `{"path":"C:\\a","action":"run"}`); status != 400 ||
		!strings.Contains(body, "reveal") {
		t.Fatalf("坏 action = %d %q", status, body)
	}
	if status, body := post("session.workspacePathApplications", `{"path":"C:\\a","extra":1}`); status != 400 {
		t.Fatalf("未知字段 = %d %q", status, body)
	}

	// 结构化错误
	srv.AttachOfficialNativeAPI(OfficialNativeAPI{
		OpenWorkspacePath: func(context.Context, string, string, string) error {
			return &native.Error{Code: "gateway/cancelled", Status: 400, Message: "已取消"}
		},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/session.openWorkspacePath", strings.NewReader(`{"path":"C:\\a"}`))
	srv.ServeOfficialNativeAPI(rec, req, string(MethodSessionOpenWorkspacePath))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 400 || body["error"].(map[string]any)["code"] != "gateway/cancelled" {
		t.Fatalf("结构化错误 = %d %#v", rec.Code, body)
	}
}
