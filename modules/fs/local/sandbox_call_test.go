package local

// FS 的 per-call 沙盒入口：带 ctx 的判定 + 一次性升级。
//
// 旧路径只有进程级 Boundary（spi.FS 没有 ctx），所以"这一次调用"的会话/权限/
// 批准无法表达。这里钉住新入口的语义：三档、越界拒绝、一次性批准只对同一次
// 调用生效、换路径/换调用都失败、没有策略闸门时 fail-loud（不静默放行）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
)

func newCallFS(t *testing.T, mode sandbox.Mode) (*FS, *sandbox.Gate, string, string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	home := filepath.Join(base, "home")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, home, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gate, err := sandbox.NewGate(sandbox.ProcessConfig{Mode: mode, WorkspaceRoot: root, HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := New(root, WithHome(home), WithSandboxGate(gate))
	if err != nil {
		t.Fatal(err)
	}
	return fs, gate, root, home, outside
}

func callCtx(session string) context.Context {
	return spi.WithSession(context.Background(), session)
}

func TestWriteCallWorkspaceThreeTiers(t *testing.T) {
	fs, _, root, _, outside := newCallFS(t, sandbox.ModeWorkspaceWrite)

	if err := fs.WriteCall(callCtx("s-A"), "inside.txt", []byte("ok")); err != nil {
		t.Fatalf("工作区内写应允许: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "inside.txt")); err != nil {
		t.Fatalf("工作区内文件应存在: %v", err)
	}

	target := filepath.Join(outside, "x.txt")
	err := fs.WriteCall(callCtx("s-A"), target, []byte("x"))
	if !errors.Is(err, sandbox.ErrDenied) {
		t.Fatalf("工作区外写必须被策略拒绝，得到 %v", err)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("被拒绝的写不能留下文件")
	}
}

func TestWriteCallOneShotDangerIsBoundToOneCall(t *testing.T) {
	fs, gate, _, _, outside := newCallFS(t, sandbox.ModeWorkspaceWrite)
	ctx := callCtx("s-A")
	target := filepath.Join(outside, "approved.txt")

	pending, err := gate.EvaluateFS(ctx, sandbox.OperationWrite, target, sandbox.PermissionDangerFullAccess)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Action != sandbox.ActionNeedsApproval || pending.Approval == nil {
		t.Fatalf("越界写应先 needs-approval: %+v", pending)
	}
	approved := sandbox.WithCallID(
		sandbox.WithEscalation(sandbox.WithPermission(ctx, sandbox.PermissionDangerFullAccess), pending.Approval.Decide(true)),
		pending.CallID)
	if err := fs.WriteCall(approved, target, []byte("approved")); err != nil {
		t.Fatalf("批准过的越界写应允许: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("批准后文件应存在: %v", err)
	}

	// 同一份批准换路径：必须 ErrApprovalBinding。
	if err := fs.WriteCall(approved, filepath.Join(outside, "another.txt"), []byte("x")); !errors.Is(err, sandbox.ErrApprovalBinding) {
		t.Fatalf("批准漂到另一个路径: %v", err)
	}
	// 同一份批准不带权限请求：拒绝（升级必须由参数显式声明）。
	noPermission := sandbox.WithCallID(sandbox.WithEscalation(ctx, pending.Approval.Decide(true)), pending.CallID)
	if err := fs.WriteCall(noPermission, target, []byte("x")); err == nil {
		t.Fatal("没有 sandbox_permissions 却带批准，必须失败")
	}
}

func TestWriteCallReadOnlyDeniesEverything(t *testing.T) {
	fs, _, root, _, _ := newCallFS(t, sandbox.ModeReadOnly)
	if err := fs.WriteCall(callCtx("s-A"), "inside.txt", []byte("x")); !errors.Is(err, sandbox.ErrDenied) {
		t.Fatalf("read-only 必须拒绝写: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "inside.txt")); err == nil {
		t.Fatal("read-only 会话写出了文件")
	}
}

func TestWriteCallRequiresSessionContext(t *testing.T) {
	fs, _, _, _, _ := newCallFS(t, sandbox.ModeWorkspaceWrite)
	if err := fs.WriteCall(context.Background(), "inside.txt", []byte("x")); !errors.Is(err, sandbox.ErrMissingContext) {
		t.Fatalf("缺会话上下文必须 fail-loud: %v", err)
	}
}

func TestWriteCallWithoutGateFailsLoud(t *testing.T) {
	t.Setenv(sandbox.EnvMode, "")
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteCall(callCtx("s-A"), "inside.txt", []byte("x")); !errors.Is(err, sandbox.ErrPolicyUnwired) {
		t.Fatalf("没有策略闸门时必须 fail-loud（不能静默放行）: %v", err)
	}
}

func TestCheckWritableCallKeepsReadBeforeWrite(t *testing.T) {
	fs, _, root, _, _ := newCallFS(t, sandbox.ModeWorkspaceWrite)
	path := filepath.Join(root, "doc.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fs.CheckWritableCall(callCtx("s-A"), "doc.txt", "write"); err == nil {
		t.Fatal("没读过就改，必须拒绝")
	}
	if _, err := fs.Read("doc.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.CheckWritableCall(callCtx("s-A"), "doc.txt", "write"); err != nil {
		t.Fatalf("读过之后应允许: %v", err)
	}
	// 外部改动之后必须拒绝（读后写观测仍然生效）。
	if err := os.WriteFile(path, []byte("v2-changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fs.CheckWritableCall(callCtx("s-A"), "doc.txt", "write"); err == nil {
		t.Fatal("文件在读过之后变了，必须要求重读")
	}
}
