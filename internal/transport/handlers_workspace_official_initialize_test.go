package transport

// 官方 `workspace/initializeDefault` 的 transport 契约测试。
//
// 契约原件（只读镜像，见 tmp/GO2-TEAM/T01/official-ref/typert.host.js:117-126）：
//
//	workspace/initializeDefault.result = z.union([z.undefined(), z.object({
//	  workspace: z.object({workspaceId, path, title, sessionIds, createdAt, updatedAt})
//	})])
//	parameters: []（无参数）
//
// 本仓成功信封不接受 `{"result":null}`（出口会变 500），因此官方 undefined
// 用**空 result 对象** `{}` 表达——与 handler 内注释一致。

import (
	"net/http"
	"testing"
)

// TestOfficialWorkspaceInitializeDefaultShape 钉住官方 result 形状：`{workspace}`
// 六个字段齐全、`sessionIds` 是数组，且官方 zod 之外的多余字段（created）不出现。
func TestOfficialWorkspaceInitializeDefaultShape(t *testing.T) {
	calls := 0
	api := OfficialWorkspaceAPI{
		InitializeDefault: func() (OfficialWorkspaceValue, bool, error) {
			calls++
			created := calls == 1
			return OfficialWorkspaceValue{Workspace: OfficialWorkspace{
				WorkspaceID: "ws-default-1",
				Path:        "/home/u/Documents/deepseek-harness/default-workspace",
				Title:       "default-workspace",
				SessionIDs:  []string{},
				CreatedAt:   "2026-11-08T00:00:00Z",
				UpdatedAt:   "2026-11-08T00:00:00Z",
			}}, created, nil
		},
	}
	srv := officialWorkspaceTestServer(t, api, OfficialDirectoryPickerAPI{})

	code, out := callOfficialWorkspace(t, srv, "workspace.initializeDefault", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("initializeDefault 应 200，实际 %d（%v）", code, out)
	}
	res := officialWorkspaceResult(t, "workspace.initializeDefault", out)
	ws, ok := res["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("result 缺 workspace 对象：%v", res)
	}
	for _, key := range []string{"workspaceId", "path", "title", "sessionIds", "createdAt", "updatedAt"} {
		if _, has := ws[key]; !has {
			t.Fatalf("workspace 缺官方字段 %s：%v", key, ws)
		}
	}
	if ids, ok := ws["sessionIds"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("空工作区 sessionIds 应是空数组（不是 null）：%v", ws["sessionIds"])
	}
	// created 是宿主侧验收事实，不是官方 result 字段；混进 wire 会违反官方 strict 契约。
	if _, has := res["created"]; has {
		t.Fatalf("result 不许出现官方契约外的 created 字段：%v", res)
	}

	// 第二次（复用）仍然是同一形状；id 由宿主保证，transport 只透传。
	code, out = callOfficialWorkspace(t, srv, "workspace.initializeDefault", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("第二次 initializeDefault 应 200，实际 %d（%v）", code, out)
	}
	if got, _ := officialWorkspaceResult(t, "workspace.initializeDefault", out)["workspace"].(map[string]any)["workspaceId"].(string); got != "ws-default-1" {
		t.Fatalf("第二次 workspaceId 变了：%q", got)
	}
	if calls != 2 {
		t.Fatalf("宿主 InitializeDefault 应被调用 2 次，实际 %d", calls)
	}
}

// TestOfficialWorkspaceInitializeDefaultUndefined 官方 undefined（首次初始化不适用）
// 在成功信封里必须表达为**空 result 对象**，不能是 {"result":null}（出口 500），
// 也不能是假造出来的 {"workspace":{}}。
func TestOfficialWorkspaceInitializeDefaultUndefined(t *testing.T) {
	api := OfficialWorkspaceAPI{
		InitializeDefault: func() (OfficialWorkspaceValue, bool, error) {
			return OfficialWorkspaceValue{}, false, nil
		},
	}
	srv := officialWorkspaceTestServer(t, api, OfficialDirectoryPickerAPI{})

	code, out := callOfficialWorkspace(t, srv, "workspace.initializeDefault", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("不适用的 initializeDefault 仍是成功（官方 undefined），实际 %d（%v）", code, out)
	}
	res := officialWorkspaceResult(t, "workspace.initializeDefault", out)
	if len(res) != 0 {
		t.Fatalf("官方 undefined 应表达为空 result 对象，实际 %v", res)
	}
}

// TestOfficialWorkspaceInitializeDefaultNotAttached 未接线必须 501 并点名方法，
// 不得静默成功。
func TestOfficialWorkspaceInitializeDefaultNotAttached(t *testing.T) {
	srv := officialWorkspaceTestServer(t, OfficialWorkspaceAPI{}, OfficialDirectoryPickerAPI{})
	code, out := callOfficialWorkspace(t, srv, "workspace.initializeDefault", map[string]any{})
	if code != http.StatusNotImplemented {
		t.Fatalf("未接线时应 501，实际 %d（%v）", code, out)
	}
	assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
	if msg := officialWorkspaceErrorMessage(t, out); msg == "" {
		t.Fatalf("未接线错误必须点名方法：%v", out)
	}
}

// TestOfficialWorkspaceInitializeDefaultRejectsParams 官方 parameters 为空：
// 任何字段都是 additionalProperties 违约（strictCodec）。
func TestOfficialWorkspaceInitializeDefaultRejectsParams(t *testing.T) {
	api := OfficialWorkspaceAPI{
		InitializeDefault: func() (OfficialWorkspaceValue, bool, error) {
			t.Fatal("未知字段必须在调用宿主之前就 400")
			return OfficialWorkspaceValue{}, false, nil
		},
	}
	srv := officialWorkspaceTestServer(t, api, OfficialDirectoryPickerAPI{})
	code, out := callOfficialWorkspace(t, srv, "workspace.initializeDefault", map[string]any{"path": "/tmp/x"})
	if code != http.StatusBadRequest {
		t.Fatalf("多余参数应 400，实际 %d（%v）", code, out)
	}
	assertFourFieldError(t, code, out, http.StatusBadRequest, "bad_request")
}
