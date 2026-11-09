package sandbox

// Gate 的会话维度：ACP 一个进程多会话，档位与只读根都必须按会话解析。
// 这里钉三件事：A/B 不互相污染、只读根只放读、进程工作区可切换但必须 canonical。

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestGatePerSessionModesDoNotLeak(t *testing.T) {
	f := newPolicyFixture(t)
	g := newTestGate(t, ModeWorkspaceWrite, f)

	if err := g.SetSessionMode("session-A", ModeReadOnly); err != nil {
		t.Fatal(err)
	}
	if got := g.SessionMode("session-A"); got != ModeReadOnly {
		t.Fatalf("A 档位 = %s", got)
	}
	if got := g.SessionMode("session-B"); got != ModeWorkspaceWrite {
		t.Fatalf("未设置的会话应沿用进程档位，得到 %s", got)
	}

	d, err := g.EvaluateCommand(sessionCtx("session-A"), "touch x", f.workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionDeny || d.Reason != ReasonReadOnlyExecute {
		t.Fatalf("A 应被 read-only 拒绝: %+v", d)
	}
	if d, err = g.EvaluateCommand(sessionCtx("session-B"), "touch x", f.workspace, ""); err != nil || d.Action != ActionAllow {
		t.Fatalf("B 不应被 A 的档位污染: %+v %v", d, err)
	}

	// 收窄可以改成更宽：显式设置才算数，未知值 fail-loud。
	if err := g.SetSessionMode("session-A", ModeDangerFullAccess); err != nil {
		t.Fatal(err)
	}
	if d, err = g.EvaluateCommand(sessionCtx("session-A"), "touch x", f.outside, ""); err != nil || d.Action != ActionAllow {
		t.Fatalf("A 改成 danger 后应放行: %+v %v", d, err)
	}
	if err := g.SetSessionMode("session-A", "yolo"); !errors.Is(err, ErrUnknownMode) {
		t.Fatalf("未知档位必须 fail-loud: %v", err)
	}

	// ForgetSession 之后回到进程档位（不留"上一个会话的档位"）。
	g.ForgetSession("session-A")
	if got := g.SessionMode("session-A"); got != ModeWorkspaceWrite {
		t.Fatalf("Forget 后档位 = %s", got)
	}
	if _, err := g.EvaluateCommand(sessionCtx("session-A"), "touch x", f.outside, ""); err != nil {
		t.Fatal(err)
	} else if d, _ := g.EvaluateCommand(sessionCtx("session-A"), "touch x", f.outside, ""); d.Action != ActionDeny {
		t.Fatalf("Forget 后应回到 workspace-write 的拒绝: %+v", d)
	}
}

func TestGateSessionExtraReadRootsAreReadOnly(t *testing.T) {
	f := newPolicyFixture(t)
	g := newTestGate(t, ModeWorkspaceWrite, f)
	extraPath := filepath.Join(f.extra, "artifact.txt")

	before, err := g.EvaluateFS(sessionCtx("session-A"), OperationRead, extraPath, "")
	if err != nil || before.Reason != ReasonExtraRead {
		t.Fatalf("进程级 extra 已存在，读应允许: %+v %v", before, err)
	}

	// 新会话的额外根：另一个目录。
	other := t.TempDir()
	if err := g.SetSessionExtraReadRoots("session-A", []string{other}); err != nil {
		t.Fatal(err)
	}
	readOther, err := g.EvaluateFS(sessionCtx("session-A"), OperationRead, filepath.Join(other, "x.txt"), "")
	if err != nil {
		t.Fatal(err)
	}
	if readOther.Action != ActionAllow || readOther.Reason != ReasonExtraRead {
		t.Fatalf("additionalDirectories 读应允许: %+v", readOther)
	}
	writeOther, err := g.EvaluateFS(sessionCtx("session-A"), OperationWrite, filepath.Join(other, "x.txt"), "")
	if err != nil {
		t.Fatal(err)
	}
	if writeOther.Action != ActionDeny || writeOther.Reason != ReasonExtraReadOnly {
		t.Fatalf("additionalDirectories 必须只读: %+v", writeOther)
	}

	// 只影响这一个会话。
	readB, err := g.EvaluateFS(sessionCtx("session-B"), OperationRead, filepath.Join(other, "x.txt"), "")
	if err != nil {
		t.Fatal(err)
	}
	if readB.Action != ActionDeny {
		t.Fatalf("A 的 extra root 泄漏到 B: %+v", readB)
	}

	// 不存在的目录 fail-loud。
	if err := g.SetSessionExtraReadRoots("session-A", []string{filepath.Join(other, "missing")}); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("不存在的额外根必须 fail-loud: %v", err)
	}
}

func TestGateSetWorkspaceRootRejectsInvalid(t *testing.T) {
	f := newPolicyFixture(t)
	g := newTestGate(t, ModeWorkspaceWrite, f)
	if err := g.SetWorkspaceRoot(f.outside); err != nil {
		t.Fatalf("换到另一个存在的目录应允许: %v", err)
	}
	if d, err := g.EvaluateCommand(sessionCtx("session-A"), "touch x", f.outside, ""); err != nil || d.Action != ActionAllow {
		t.Fatalf("换根后新工作区内命令应允许: %+v %v", d, err)
	}
	if d, _ := g.EvaluateCommand(sessionCtx("session-A"), "touch x", f.workspace, ""); d.Action != ActionDeny {
		t.Fatalf("换根后旧工作区必须变成工作区外: %+v", d)
	}
	if err := g.SetWorkspaceRoot(filepath.Join(f.workspace, "missing")); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("不存在的根必须 fail-loud: %v", err)
	}
}

// 一次性批准必须绑定调用身份：同一命令/路径/会话，换一个 call id 就是另一次调用。
func TestOneShotApprovalRequiresTheSameCallID(t *testing.T) {
	f := newPolicyFixture(t)
	g := newTestGate(t, ModeWorkspaceWrite, f)
	ctx := sessionCtx("session-A")

	pending, err := g.EvaluateCommand(ctx, "touch approved.txt", f.outside, PermissionDangerFullAccess)
	if err != nil {
		t.Fatal(err)
	}
	approval := pending.Approval.Decide(true)

	same, err := g.EvaluateCommand(WithCallID(WithEscalation(ctx, approval), pending.CallID),
		"touch approved.txt", f.outside, PermissionDangerFullAccess)
	if err != nil || same.Action != ActionAllow {
		t.Fatalf("同一次调用应放行: %+v %v", same, err)
	}
	if _, err := g.EvaluateCommand(WithEscalation(ctx, approval),
		"touch approved.txt", f.outside, PermissionDangerFullAccess); !errors.Is(err, ErrApprovalBinding) {
		t.Fatalf("没有带回 call id 的复用必须被拒: %v", err)
	}
	if _, err := g.EvaluateCommand(WithCallID(WithEscalation(ctx, approval), "another-call"),
		"touch approved.txt", f.outside, PermissionDangerFullAccess); !errors.Is(err, ErrApprovalBinding) {
		t.Fatalf("另一个 call id 复用批准必须被拒: %v", err)
	}
}
