package transport

// PLAY-WORKSPACE-HOST 先红：TUI2Go 的 workspace provider 需要
// `listWorkspaces / listWorkspaceRegistry / resolveWorkspace / switchWorkspace /
// removeWorkspace / workspaceCommands` 六个动词，而当前 workspace 族只有
// create/rename/delete/insert*/pin/archive 与目录型 `workspace.list`，
// **没有任何一个方法能列出 registry 本身**。
//
// 本文件定义 5 个宿主扩展动词（比官方多，不改官方形状）：
//
//	workspace.registry  列出 registry（Order 为唯一顺序真相）
//	workspace.resolve   把 uri / path / id / title 解析成 registry 条目
//	workspace.switch    切换当前工作区（持久化 defaultWorkspaceId）
//	workspace.remove    只摘注册、**不删文件**（区别于 workspace.delete）
//	workspace.commands  列出本宿主真实支持的 workspace 命令及可用性
//
// 红点：这些名字现在既不在 IsOfficialWorkspaceMethod，也没有 handler，
// 打上去只会落 `not_implemented`。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func workspaceFakeRegistryAPI() OfficialWorkspaceAPI {
	wsA := OfficialWorkspace{WorkspaceID: "ws-a", Path: "C:/tmp/a", Title: "A",
		SessionIDs: []string{}, CreatedAt: "2026-11-08T00:00:00.000Z", UpdatedAt: "2026-11-08T00:00:00.000Z"}
	wsB := OfficialWorkspace{WorkspaceID: "ws-b", Path: "C:/tmp/b", Title: "B",
		SessionIDs: []string{"s-1"}, CreatedAt: "2026-11-08T00:01:00.000Z", UpdatedAt: "2026-11-08T00:01:00.000Z"}
	var api OfficialWorkspaceAPI
	api.Registry = func() (OfficialWorkspaceRegistry, error) {
		return OfficialWorkspaceRegistry{
			Version:            1,
			DefaultWorkspaceID: "ws-a",
			Order:              []string{"ws-b", "ws-a"},
			Workspaces:         []OfficialWorkspace{wsB, wsA},
			ArchivedSessionIDs: []string{"s-9"},
			PinnedSessionIDs:   []string{"s-1"},
			RegistryPath:       "C:/home/state/workspaces.json",
		}, nil
	}
	api.Resolve = func(ref string) (OfficialWorkspace, error) {
		switch strings.TrimSuffix(strings.TrimPrefix(ref, "workspace://"), "/") {
		case "ws-a", "C:/tmp/a", "A", "file:///C:/tmp/a":
			return wsA, nil
		}
		return OfficialWorkspace{}, OfficialWorkspaceError("workspace/not-found", "没有这个工作区：%q", ref)
	}
	api.Switch = func(ref string) (OfficialWorkspaceSwitchValue, error) {
		if ref != "ws-b" {
			return OfficialWorkspaceSwitchValue{}, OfficialWorkspaceError("workspace/not-found", "没有这个工作区：%q", ref)
		}
		return OfficialWorkspaceSwitchValue{Workspace: wsB, PreviousWorkspaceID: "ws-a"}, nil
	}
	api.Remove = func(ref string) (OfficialWorkspaceRemoveValue, error) {
		if ref != "ws-b" {
			return OfficialWorkspaceRemoveValue{}, OfficialWorkspaceError("workspace/not-found", "没有这个工作区：%q", ref)
		}
		return OfficialWorkspaceRemoveValue{Removed: true, WorkspaceID: "ws-b", DefaultWorkspaceID: "ws-a"}, nil
	}
	api.Commands = func() (OfficialWorkspaceCommandList, error) {
		return OfficialWorkspaceCommandList{Commands: []OfficialWorkspaceCommand{
			{ID: "registry", Title: "列出工作区", Available: true},
			{ID: "switch", Title: "切换工作区", Available: true},
			{ID: "remove", Title: "移除注册", Available: true},
			{ID: "open-in-editor", Title: "在编辑器打开", Available: false, Reason: "宿主没有编辑器集成"},
		}}, nil
	}
	return api
}

func callWorkspaceMethod(t *testing.T, name string, body string, api OfficialWorkspaceAPI) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ServeOfficialWorkspace(rec, req, name, api, OfficialDirectoryPickerAPI{})
	var payload map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	}
	return rec.Code, payload
}

func TestWorkspaceRegistryIsRoutableAndOrdered(t *testing.T) {
	if !IsOfficialWorkspaceMethod("workspace.registry") {
		t.Fatal("workspace.registry 必须进 IsOfficialWorkspaceMethod，否则 /api 永远落 not_implemented")
	}
	code, payload := callWorkspaceMethod(t, "workspace.registry", `{}`, workspaceFakeRegistryAPI())
	if code != http.StatusOK {
		t.Fatalf("HTTP=%d payload=%v", code, payload)
	}
	res, _ := payload["result"].(map[string]any)
	if res == nil {
		t.Fatalf("缺 result：%v", payload)
	}
	if res["defaultWorkspaceId"] != "ws-a" {
		t.Fatalf("defaultWorkspaceId=%v", res["defaultWorkspaceId"])
	}
	order, _ := res["order"].([]any)
	if len(order) != 2 || order[0] != "ws-b" {
		t.Fatalf("order 必须是 registry 的真实顺序，得到 %v", res["order"])
	}
	ws, _ := res["workspaces"].([]any)
	if len(ws) != 2 {
		t.Fatalf("workspaces 应 2 条，得到 %v", res["workspaces"])
	}
	first, _ := ws[0].(map[string]any)
	if first["workspaceId"] != "ws-b" {
		t.Fatalf("workspaces 必须按 order 排列，首条=%v", first["workspaceId"])
	}
}

func TestWorkspaceResolveAcceptsUriPathIdTitle(t *testing.T) {
	api := workspaceFakeRegistryAPI()
	for _, body := range []string{
		`{"uri":"workspace://ws-a"}`,
		`{"path":"C:/tmp/a"}`,
		`{"workspaceId":"ws-a"}`,
		`{"title":"A"}`,
		`{"ref":"workspace://ws-a"}`,
	} {
		code, payload := callWorkspaceMethod(t, "workspace.resolve", body, api)
		if code != http.StatusOK {
			t.Fatalf("%s → HTTP=%d %v", body, code, payload)
		}
		res, _ := payload["result"].(map[string]any)
		ws, _ := res["workspace"].(map[string]any)
		if ws == nil || ws["workspaceId"] != "ws-a" {
			t.Fatalf("%s → %v", body, res)
		}
	}
}

func TestWorkspaceResolveNotFoundIs404(t *testing.T) {
	code, payload := callWorkspaceMethod(t, "workspace.resolve", `{"uri":"workspace://nope"}`, workspaceFakeRegistryAPI())
	if code != http.StatusNotFound {
		t.Fatalf("应为 404，得到 %d %v", code, payload)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("四字段 code 应为 not_found，得到 %v", payload)
	}
}

func TestWorkspaceSwitchReturnsPrevious(t *testing.T) {
	code, payload := callWorkspaceMethod(t, "workspace.switch", `{"workspaceId":"ws-b"}`, workspaceFakeRegistryAPI())
	if code != http.StatusOK {
		t.Fatalf("HTTP=%d %v", code, payload)
	}
	res, _ := payload["result"].(map[string]any)
	if res["previousWorkspaceId"] != "ws-a" {
		t.Fatalf("previousWorkspaceId=%v", res["previousWorkspaceId"])
	}
	ws, _ := res["workspace"].(map[string]any)
	if ws["workspaceId"] != "ws-b" {
		t.Fatalf("workspace=%v", ws)
	}
}

func TestWorkspaceRemoveDoesNotDeleteFiles(t *testing.T) {
	code, payload := callWorkspaceMethod(t, "workspace.remove", `{"workspaceId":"ws-b"}`, workspaceFakeRegistryAPI())
	if code != http.StatusOK {
		t.Fatalf("HTTP=%d %v", code, payload)
	}
	res, _ := payload["result"].(map[string]any)
	if res["removed"] != true || res["workspaceId"] != "ws-b" {
		t.Fatalf("%v", res)
	}
	if res["defaultWorkspaceId"] != "ws-a" {
		t.Fatalf("remove 必须回报当前 default，得到 %v", res["defaultWorkspaceId"])
	}
}

func TestWorkspaceCommandsExposeAvailability(t *testing.T) {
	code, payload := callWorkspaceMethod(t, "workspace.commands", `{}`, workspaceFakeRegistryAPI())
	if code != http.StatusOK {
		t.Fatalf("HTTP=%d %v", code, payload)
	}
	res, _ := payload["result"].(map[string]any)
	cmds, _ := res["commands"].([]any)
	if len(cmds) == 0 {
		t.Fatalf("commands 空：%v", res)
	}
	var sawUnavailable bool
	for _, c := range cmds {
		m := c.(map[string]any)
		if m["available"] == false {
			sawUnavailable = true
			if m["reason"] == "" {
				t.Fatalf("不可用命令必须给 reason：%v", m)
			}
		}
	}
	if !sawUnavailable {
		t.Fatal("应至少有一条 available=false 的命令（真实可用性必须能表达）")
	}
}

// 未接线时必须是 501（与同族一致），不能假装空列表。
func TestWorkspaceRegistryUnwiredIs501(t *testing.T) {
	code, payload := callWorkspaceMethod(t, "workspace.registry", `{}`, OfficialWorkspaceAPI{})
	if code != http.StatusNotImplemented {
		t.Fatalf("未接线应为 501，得到 %d %v", code, payload)
	}
}
