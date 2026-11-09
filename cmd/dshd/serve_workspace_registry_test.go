package main

// PLAY-WORKSPACE-HOST 宿主侧探针：registry / resolve / switch / remove /
// commands 必须读写**同一份** `<home>/state/workspaces.json`，并且状态在
// "重新打开宿主"（等价于进程重启）后仍然成立。

import (
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/transport"
)

func newRegistryTestHost(t *testing.T, home string) *officialWorkspaceHost {
	t.Helper()
	h := newOfficialWorkspaceHost(home, nil, nil)
	if h == nil {
		t.Fatal("newOfficialWorkspaceHost 返回 nil（home 为空？）")
	}
	return h
}

func TestWorkspaceRegistryHostRoundTrip(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	dirA := filepath.Join(root, "alpha")
	dirB := filepath.Join(root, "beta")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	h := newRegistryTestHost(t, home)
	wsA, err := h.createWorkspace(dirA)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	wsB, err := h.createWorkspace(dirB)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	reg, err := h.registry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if len(reg.Workspaces) != 2 || len(reg.Order) != 2 {
		t.Fatalf("registry 应 2 条，得到 %d/%d", len(reg.Workspaces), len(reg.Order))
	}
	if reg.Workspaces[0].WorkspaceID != reg.Order[0] {
		t.Fatalf("workspaces 必须按 order 排列：%v vs %v", reg.Workspaces[0].WorkspaceID, reg.Order[0])
	}
	if reg.RegistryPath == "" {
		t.Fatal("registryPath 必须回报真实文件路径")
	}
	if _, statErr := os.Stat(reg.RegistryPath); statErr != nil {
		t.Fatalf("registryPath 指向的文件不存在：%v", statErr)
	}

	// resolve：四种引用形状都要落到同一条记录。
	for _, ref := range []string{
		"workspace://" + wsA.Workspace.WorkspaceID,
		wsA.Workspace.Title, // 标题（create 从目录名派生：alpha）
		dirA,
		filepath.ToSlash(dirA),
		wsA.Workspace.WorkspaceID,
	} {
		got, err := h.resolveWorkspaceRef(ref)
		if err != nil {
			t.Fatalf("resolve %q: %v", ref, err)
		}
		if got.WorkspaceID != wsA.Workspace.WorkspaceID {
			t.Fatalf("resolve %q 落到 %q", ref, got.WorkspaceID)
		}
	}
	if _, err := h.resolveWorkspaceRef("workspace://nope"); err == nil {
		t.Fatal("不存在的引用必须报错")
	}

	// switch：持久化 defaultWorkspaceId，并回报前一个。
	sw, err := h.switchWorkspace(wsB.Workspace.WorkspaceID)
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	if sw.Workspace.WorkspaceID != wsB.Workspace.WorkspaceID {
		t.Fatalf("switch 落到 %q", sw.Workspace.WorkspaceID)
	}

	// 模拟重启：同一 home 重新构造宿主，然后重新加载。
	h2 := newRegistryTestHost(t, home)
	reg2, err := h2.registry()
	if err != nil {
		t.Fatalf("重启后 registry: %v", err)
	}
	if reg2.DefaultWorkspaceID != wsB.Workspace.WorkspaceID {
		t.Fatalf("重启后 defaultWorkspaceId 应为 %q，得到 %q",
			wsB.Workspace.WorkspaceID, reg2.DefaultWorkspaceID)
	}

	// remove：只摘注册，文件必须还在。
	rm, err := h2.removeWorkspace(wsB.Workspace.WorkspaceID)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !rm.Removed || rm.WorkspaceID != wsB.Workspace.WorkspaceID {
		t.Fatalf("remove 结果：%+v", rm)
	}
	if _, statErr := os.Stat(dirB); statErr != nil {
		t.Fatalf("remove 不能删文件，但目录不见了：%v", statErr)
	}

	h3 := newRegistryTestHost(t, home)
	reg3, err := h3.registry()
	if err != nil {
		t.Fatalf("remove 后重启 registry: %v", err)
	}
	if len(reg3.Workspaces) != 1 {
		t.Fatalf("remove 后应剩 1 条，得到 %d", len(reg3.Workspaces))
	}
	if _, err := h3.resolveWorkspaceRef(wsB.Workspace.WorkspaceID); err == nil {
		t.Fatal("remove 后 resolve 必须 not-found")
	}
}

func TestWorkspaceCommandsAvailabilityFollowsRegistry(t *testing.T) {
	home := t.TempDir()
	h := newRegistryTestHost(t, home)

	empty, err := h.workspaceCommands()
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	byID := map[string]transport.OfficialWorkspaceCommand{}
	for _, c := range empty.Commands {
		byID[c.ID] = c
	}
	if sw := byID["switch"]; sw.Available {
		t.Fatal("空 registry 时 switch 必须标不可用")
	} else if sw.Reason == "" {
		t.Fatal("不可用命令必须给 reason")
	}

	dir := t.TempDir()
	if _, err := h.createWorkspace(dir); err != nil {
		t.Fatalf("create: %v", err)
	}
	full, err := h.workspaceCommands()
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	byID = map[string]transport.OfficialWorkspaceCommand{}
	for _, c := range full.Commands {
		byID[c.ID] = c
	}
	if !byID["switch"].Available || !byID["remove"].Available {
		t.Fatalf("有工作区后 switch/remove 应可用：%+v", full.Commands)
	}
	if byID["open-in-editor"].Available {
		t.Fatal("宿主没有编辑器集成，open-in-editor 必须标不可用")
	}
}
