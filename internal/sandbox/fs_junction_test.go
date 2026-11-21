//go:build windows

package sandbox

// 经目录 junction 的越界写必须被策略拒绝。
//
// 为什么这条用例在 Go ≥1.23 下会红：`filepath.EvalSymlinks` 不再跟进
// IO_REPARSE_TAG_MOUNT_POINT（目录 junction），`os.Lstat` 也只报 ModeIrregular。
// 于是 `workspace\link\evil.txt`（link → 工作区外的目录）被 canonicalize 成它自己，
// 分类成 pathWorkspace，`EvaluateFS(write)` 返回 allow/workspace-write；
// 真写盘时 `FS.Write` 用的是原始路径，操作系统跟着 junction 落到工作区外。
//
// 修复的判据就是这条：**策略层必须 deny**，而工作区内的普通路径仍然是 allow
// （正向控制，别把正常路径一起判死）。

import (
	"os"
	"path/filepath"
	"testing"
)

func newJunctionPolicy(t *testing.T, workspace, home string) *Policy {
	t.Helper()
	pol, err := New(Config{
		SessionID: "s1", Mode: ModeWorkspaceWrite,
		WorkspaceRoot: workspace, HomeDir: home,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return pol
}

func TestEvaluateFSRejectsWriteThroughJunction(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	workspace := filepath.Join(base, "ws")
	home := filepath.Join(base, "home")
	for _, dir := range []string{outside, workspace, home} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(workspace, "link")
	if err := makeJunction(t, link, outside); err != nil {
		t.Skipf("本机建不了 junction，跳过这条边界断言：%v", err)
	}
	pol := newJunctionPolicy(t, workspace, home)

	target := filepath.Join(link, "evil.txt")
	call := Call{SessionID: "s1", CallID: "c1"}

	d, err := pol.EvaluateFS(FSRequest{Call: call, Operation: OperationWrite, Path: target})
	if err != nil {
		t.Fatalf("EvaluateFS(write) 不该报错：%v", err)
	}
	if d.Action != ActionDeny {
		t.Fatalf("经 junction 的越界写被放行：action=%s reason=%s canonical=%q（junction 没有被解析）",
			d.Action, d.Reason, d.CanonicalPath)
	}
	// canonical 必须落到 junction 的**真实目标**里，而不是 junction 名字上。
	// 期望值也用 canonical 形式：t.TempDir() 给的是 8.3 短名（ADMINI~1），
	// 而解析出来的目标用的是长名，两种写法指同一个目录。
	wantOutside, werr := canonicalPath(outside)
	if werr != nil {
		t.Fatalf("canonicalPath(outside): %v", werr)
	}
	want := filepath.Join(wantOutside, "evil.txt")
	if rel, relErr := filepath.Rel(want, d.CanonicalPath); relErr != nil || rel != "." {
		t.Fatalf("canonical=%q 不指向 junction 目标 %q", d.CanonicalPath, want)
	}

	// 读也一样：经 junction 读到工作区外的东西同样越界。
	rd, rerr := pol.EvaluateFS(FSRequest{Call: call, Operation: OperationRead, Path: target})
	if rerr != nil {
		t.Fatalf("EvaluateFS(read) 不该报错：%v", rerr)
	}
	if rd.Action != ActionDeny {
		t.Fatalf("经 junction 的越界读被放行：action=%s reason=%s", rd.Action, rd.Reason)
	}
}

// 正向控制：工作区内的普通路径一个字节都不变（修复不能靠"更保守"换安全）。
func TestEvaluateFSAllowsPlainWorkspaceWrite(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "ws")
	home := filepath.Join(base, "home")
	for _, dir := range []string{workspace, home} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(workspace, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	pol := newJunctionPolicy(t, workspace, home)
	d, err := pol.EvaluateFS(FSRequest{
		Call:      Call{SessionID: "s1", CallID: "c1"},
		Operation: OperationWrite,
		Path:      filepath.Join(sub, "ok.txt"),
	})
	if err != nil {
		t.Fatalf("EvaluateFS 不该报错：%v", err)
	}
	if d.Action != ActionAllow || d.Reason != ReasonWorkspaceWrite {
		t.Fatalf("工作区内的普通写必须照旧放行：action=%s reason=%s", d.Action, d.Reason)
	}
	// 新建文件（目标不存在）走同一条 canonical 规则。
	d2, err2 := pol.EvaluateFS(FSRequest{
		Call:      Call{SessionID: "s1", CallID: "c2"},
		Operation: OperationWrite,
		Path:      filepath.Join(workspace, "deep", "new", "n.txt"),
	})
	if err2 != nil || d2.Action != ActionAllow {
		t.Fatalf("工作区内新建也必须放行：action=%s err=%v", d2.Action, err2)
	}
}
