package sandbox

// 一次性升级的真实语义：批准只属于**那一次调用**。
//
// 本文件先钉"批准不能漂"的基线路径：同会话、同 cwd 下的另一条命令也必须被拒。
// 调用身份（call id）与重放路径见 runtime_test.go 的 Gate 用例。

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestOneShotApprovalCannotBeReusedForAnotherCommand(t *testing.T) {
	f := newPolicyFixture(t)
	g := newTestGate(t, ModeWorkspaceWrite, f)
	ctx := sessionCtx("session-A")

	pending, err := g.EvaluateCommand(ctx, "touch approved.txt", f.outside, PermissionDangerFullAccess)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Action != ActionNeedsApproval || pending.Approval == nil {
		t.Fatalf("越界命令应先 needs-approval: %+v", pending)
	}
	approval := pending.Approval.Decide(true)

	// 同一份批准 + 另一条命令（同会话、同 cwd）。
	other, err := g.EvaluateCommand(WithEscalation(ctx, approval), "rm -rf everything", f.outside, PermissionDangerFullAccess)
	if !errors.Is(err, ErrApprovalBinding) {
		t.Fatalf("批准漂到另一条命令：action=%s reason=%s err=%v", other.Action, other.Reason, err)
	}
}

func TestFSApprovalCannotBeReusedForAnotherPath(t *testing.T) {
	f := newPolicyFixture(t)
	g := newTestGate(t, ModeWorkspaceWrite, f)
	ctx := sessionCtx("session-A")

	pending, err := g.EvaluateFS(ctx, OperationWrite, filepath.Join(f.outside, "one.txt"), PermissionDangerFullAccess)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Action != ActionNeedsApproval || pending.Approval == nil {
		t.Fatalf("越界写应先 needs-approval: %+v", pending)
	}
	approval := pending.Approval.Decide(true)
	_, err = g.EvaluateFS(WithEscalation(ctx, approval),
		OperationWrite, filepath.Join(f.outside, "two.txt"), PermissionDangerFullAccess)
	if !errors.Is(err, ErrApprovalBinding) {
		t.Fatalf("批准漂到另一个路径: err=%v", err)
	}
}
