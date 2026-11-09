package sandbox

import (
	"path/filepath"
	"testing"
)

// 官方 `sandbox.additionalWritableRoots`（默认 []）：workspace-write 档位下，
// 允许在工作区之外**再开几个可写目录**，不必把整档升到 danger-full-access。
//
// 先红：Config 没有 AdditionalWritableRoots 字段。
func TestAdditionalWritableRootsAllowWritesOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	extra := t.TempDir()
	home := t.TempDir()
	p, err := New(Config{
		SessionID: "s1", Mode: ModeWorkspaceWrite,
		WorkspaceRoot: ws, HomeDir: home,
		AdditionalWritableRoots: []string{extra},
	})
	if err != nil {
		t.Fatalf("带追加可写根的策略要能构造：%v", err)
	}
	d, err := p.EvaluateFS(FSRequest{
		Call:      Call{SessionID: "s1", CallID: "c1"},
		Operation: OperationWrite,
		Path:      filepath.Join(extra, "out.txt"),
	})
	if err != nil {
		t.Fatalf("判定失败：%v", err)
	}
	if d.Action != ActionAllow {
		t.Fatalf("追加可写根下的写入应放行，拿到 %s（%s）", d.Action, d.Reason)
	}
	if d.Reason != ReasonExtraWritable {
		t.Fatalf("原因要是 extra-writable，拿到 %q", d.Reason)
	}
}

// 不设时零影响：工作区外写入仍然 deny（默认行为与现在完全一致）。
func TestAdditionalWritableRootsDefaultEmptyKeepsDeny(t *testing.T) {
	ws := t.TempDir()
	other := t.TempDir()
	p, err := New(Config{SessionID: "s1", Mode: ModeWorkspaceWrite, WorkspaceRoot: ws, HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.EvaluateFS(FSRequest{
		Call:      Call{SessionID: "s1", CallID: "c1"},
		Operation: OperationWrite,
		Path:      filepath.Join(other, "out.txt"),
	})
	if err != nil {
		t.Fatalf("判定失败：%v", err)
	}
	if d.Action != ActionDeny || d.Reason != ReasonOutsideWorkspace {
		t.Fatalf("不设追加根时工作区外写入必须 deny/outside-workspace，拿到 %s/%s", d.Action, d.Reason)
	}
}

// 追加可写根不是"读根"：读仍然走原来的 extra-read 通道，两者不混。
func TestAdditionalWritableRootDoesNotWidenReads(t *testing.T) {
	ws := t.TempDir()
	extra := t.TempDir()
	p, err := New(Config{
		SessionID: "s1", Mode: ModeWorkspaceWrite, WorkspaceRoot: ws, HomeDir: t.TempDir(),
		AdditionalWritableRoots: []string{extra},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.EvaluateFS(FSRequest{
		Call:      Call{SessionID: "s1", CallID: "c1"},
		Operation: OperationRead,
		Path:      filepath.Join(extra, "in.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 读的通道没有被追加可写根放大：它仍按 outside-read 拒绝（要读就显式配 ExtraReadRoots）。
	if d.Action != ActionDeny || d.Reason != ReasonOutsideRead {
		t.Fatalf("追加可写根不该顺带放开读，拿到 %s/%s", d.Action, d.Reason)
	}
}
