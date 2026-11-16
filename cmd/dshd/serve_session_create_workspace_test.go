package main

import (
	"errors"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// TestWorkspaceCreateSessionUsesSelectedDirectory 证明 workspaceId 不是装饰参数：
// 新会话的真实 v4 header.cwd 与 agentPreset 来自所选工作区/请求，且同一份
// registry 立即列出该会话。默认工作区 A 与进程 cwd 都不能冒充选择结果。
func TestWorkspaceCreateSessionUsesSelectedDirectory(t *testing.T) {
	home := t.TempDir()
	source := newSessionSource(home, "")
	host := newOfficialWorkspaceHost(home, source, nil)
	dirA, dirB := t.TempDir(), t.TempDir()
	wsA, err := host.createWorkspace(dirA)
	if err != nil {
		t.Fatalf("建工作区 A: %v", err)
	}
	wsB, err := host.createWorkspace(dirB)
	if err != nil {
		t.Fatalf("建工作区 B: %v", err)
	}
	if wsA.Workspace.WorkspaceID == wsB.Workspace.WorkspaceID {
		t.Fatal("两个目录必须有不同 workspaceId")
	}

	id, err := host.createSession(wsB.Workspace.WorkspaceID, func(cwd string) (string, error) {
		create, err := serveCreate(home, cwd)
		if err != nil {
			return "", err
		}
		return create("read-only")
	})
	if err != nil {
		t.Fatalf("按 workspaceId 建会话: %v", err)
	}
	path, err := session.FindByID(home, id)
	if err != nil {
		t.Fatalf("回读新会话: %v", err)
	}
	header, err := v4.ReadHeader(path)
	if err != nil {
		t.Fatalf("读新会话 header: %v", err)
	}
	if want := v4.NormalizeCwd(dirB); header.Cwd != want {
		t.Fatalf("header.cwd=%q，期望所选工作区 %q（不是 A=%q）", header.Cwd, want, dirA)
	}
	if header.AgentPreset != "read-only" {
		t.Fatalf("header.agentPreset=%q", header.AgentPreset)
	}

	if err := host.withDocument(func() error {
		rec := host.doc.Workspaces[wsB.Workspace.WorkspaceID]
		if rec == nil || len(rec.ManualSessionIDs) == 0 || rec.ManualSessionIDs[0] != id {
			t.Fatalf("新会话未 prepend 到 registry 手动顺序：%+v", rec)
		}
		if got := host.sessionIDsLocked(rec); len(got) == 0 || got[0] != id {
			t.Fatalf("工作区视图没有新会话：%v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCreateSessionUnknownIDDoesNotCreate(t *testing.T) {
	home := t.TempDir()
	host := newOfficialWorkspaceHost(home, newSessionSource(home, ""), nil)
	before, err := session.ListSessions(home, "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	id, err := host.createSession("ws-missing", func(string) (string, error) {
		calls++
		return "must-not-exist", nil
	})
	if err == nil || id != "" || calls != 0 {
		t.Fatalf("未知 workspaceId 必须在建会话前失败：id=%q calls=%d err=%v", id, calls, err)
	}
	var fault *transport.OfficialWorkspaceFault
	if !errors.As(err, &fault) || fault.Code != "workspace/not-found" || fault.Details["workspaceId"] != "ws-missing" {
		t.Fatalf("错误必须保留官方码与 details：%v", err)
	}
	after, err := session.ListSessions(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("失败后不得新增会话：before=%d after=%d", len(before), len(after))
	}
}

func TestWorkspaceCreateSessionRejectsFalseMembership(t *testing.T) {
	home := t.TempDir()
	host := newOfficialWorkspaceHost(home, newSessionSource(home, ""), nil)
	created, err := host.createWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := host.createSession(created.Workspace.WorkspaceID, func(string) (string, error) {
		return "session-without-real-log", nil
	})
	if err == nil || id != "" {
		t.Fatalf("没有真实 v4 日志的 id 不得挂进工作区：id=%q err=%v", id, err)
	}
	var fault *transport.OfficialWorkspaceFault
	if !errors.As(err, &fault) || fault.Code != "session/workspace-attach-failed" {
		t.Fatalf("应返回 attach-failed，实际 %v", err)
	}
	if err := host.withDocument(func() error {
		rec := host.doc.Workspaces[created.Workspace.WorkspaceID]
		if len(rec.ManualSessionIDs) != 0 {
			t.Fatalf("假归属已写入 registry：%v", rec.ManualSessionIDs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
