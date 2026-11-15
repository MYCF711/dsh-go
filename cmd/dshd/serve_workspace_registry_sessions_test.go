package main

// 缺口 ④ 的端到端核验：`workspace.registry`（GUI 侧 workspace 面板读的宿主扩展）
// 必须带**真实会话归属**（`sessionIds`）与**手动拖拽顺序**，而不是只按 cwd 临时分组。
//
// 官方形状（`dsh-api-workspace-controller/src/feed.ts:22-33` 的 workspaceView）：
//
//	{ workspaceId, path, title, sessionIds, createdAt, updatedAt }
//
// 拖拽顺序的官方动词是 `workspace.insertSessionBefore`
// （`src/commands.ts` / `src/index.ts:149`，结果 `{workspace:{…sessionIds…}}`）。
// 已有的 workspace.create/insertSessionBefore 用例验的是这两个动词自己的返回；
// 本用例补的是 GUI 真正读的那条路：**从 registry 里读回来**仍然对。

import (
	"testing"
	"time"
)

func TestWorkspaceRegistryCarriesSessionIDsAndManualOrder(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	dir := t.TempDir()
	ids := []string{
		"cccccccc-0000-0000-0000-000000000001",
		"cccccccc-0000-0000-0000-000000000002",
		"cccccccc-0000-0000-0000-000000000003",
	}
	for _, id := range ids {
		newRealSession(t, h.home, dir, id)
		time.Sleep(10 * time.Millisecond) // UpdatedAt 决定默认顺序
	}
	created := h.ok(t, "workspace.create", map[string]any{"path": dir})
	workspaceID, _ := officialWorkspaceView(t, created)["workspaceId"].(string)
	if workspaceID == "" {
		t.Fatalf("workspace.create 没回 workspaceId：%v", created)
	}

	registrySessionIDs := func() []string {
		t.Helper()
		registry := h.ok(t, "workspace.registry", map[string]any{})
		workspaces, _ := registry["workspaces"].([]any)
		for _, item := range workspaces {
			view, _ := item.(map[string]any)
			if view["workspaceId"] == workspaceID {
				return officialWorkspaceStrings(t, view, "sessionIds")
			}
		}
		t.Fatalf("registry 里没有工作区 %s：%v", workspaceID, workspaces)
		return nil
	}

	// 1) registry 带回真实归属：这个工作区名下的三条会话，最近更新的在前。
	if got := registrySessionIDs(); !equalStrings(got, []string{ids[2], ids[1], ids[0]}) {
		t.Fatalf("registry.sessionIds=%v，期望按最近更新倒序 %v", got, []string{ids[2], ids[1], ids[0]})
	}

	// 2) 拖拽：把最早那条移到最前，再从 registry 读回来。
	h.ok(t, "workspace.insertSessionBefore", map[string]any{
		"workspaceId": workspaceID, "sessionId": ids[0], "beforeSessionId": ids[2],
	})
	if got := registrySessionIDs(); !equalStrings(got, []string{ids[0], ids[2], ids[1]}) {
		t.Fatalf("拖拽后 registry.sessionIds=%v，期望 %v", got, []string{ids[0], ids[2], ids[1]})
	}

	// 3) 重启后（同一 home 重新装配）顺序仍是手动顺序，不是回落到 cwd 分组。
	restarted := newOfficialWorkspaceHarnessAt(t, h.home)
	registry := restarted.ok(t, "workspace.registry", map[string]any{})
	workspaces, _ := registry["workspaces"].([]any)
	if len(workspaces) != 1 {
		t.Fatalf("重启后 registry 应有 1 个工作区，实际 %v", workspaces)
	}
	view, _ := workspaces[0].(map[string]any)
	if got := officialWorkspaceStrings(t, view, "sessionIds"); !equalStrings(got, []string{ids[0], ids[2], ids[1]}) {
		t.Fatalf("重启后 registry.sessionIds=%v，手动顺序丢了", got)
	}
}
