//go:build windows

package sandbox

// 受控根的**父目录**不许是 reparse point。
//
// 理由（本单硬红线）：ACE 与 Low 标签打在**解析后的目标**上。`<home>/sandbox-roots`
// 这个父目录在本机可被同用户任何进程预先创建；它若是一个指向用户目录树的
// junction，`MkdirAll(root)` 就会把受控根建进那个目标，随后 `modifyDACL` /
// `setLowLabel` 就把驻留的写 ACE 与 Low 标签落到了用户的目录上。
//
// `ensureControlledRoot` 里原有的"父目录与受控根 canonical 结果必须仍是父子关系"
// 挡不住这一格：它是拿**解析后**的父目录去比解析后的受控根，只要根确实建在
// 解析后的父目录里就恒真。所以这条测试断言的是"建根**之前**就拒绝"，并且
// 顺带断言 junction 目标里**一个目录都没多出来**。
//
// 用 junction（`mklink /J`）而不是 symlink：junction 不需要管理员/开发者模式，
// 正是实际可被利用的那一形态。

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeJunction 建一个目录 junction；环境不支持时返回错误让调用方 SKIP。
func makeJunction(t *testing.T, link, target string) error {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	// 用与生产代码同一个判据（GetFileAttributes 的属性位）确认链接真的建成了。
	// 不用 os.Lstat(...).Mode()&os.ModeSymlink：目录 junction 在本机有一部分进程里
	// 被 Go 报成 ModeIrregular，那个判据会把"建成了"误判成"没建成"。
	reparse, err := isReparsePoint(link)
	if err != nil {
		return err
	}
	if !reparse {
		return errors.New("mklink 报了成功但目标不是 reparse point（环境异常）")
	}
	return nil
}

func TestEnsureControlledRootRejectsJunctionParent(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "user-tree")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	// 父目录被预先造成 junction（`<home>/sandbox-roots` → 用户目录树）。
	parent := filepath.Join(base, "sandbox-roots")
	if err := makeJunction(t, parent, target); err != nil {
		t.Skipf("本机建不了 junction，跳过这条隔离断言：%v", err)
	}

	root := filepath.Join(parent, "sess-1")
	err := ensureControlledRoot(root)
	if !errors.Is(err, ErrRunnerRootInvalid) {
		t.Fatalf("父目录是 junction 时必须在打 ACL 之前以 ErrRunnerRootInvalid 拒绝，实际 %v", err)
	}
	// 关键：拒绝发生在建根之前——junction 目标里不许凭空多出一个目录
	// （多出来就意味着"先建了根、再发现不对"，那时 ACL 已经落下去了）。
	if _, statErr := os.Lstat(filepath.Join(target, "sess-1")); !os.IsNotExist(statErr) {
		t.Fatalf("拒绝之前已经在 junction 目标里建了受控根（%v）：ACL/Low 标签会落在用户目录树上", statErr)
	}
}

// 正控：普通父目录照旧能建根，别把正常路径一起判死。
func TestEnsureControlledRootAcceptsPlainParent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "sandbox-roots", "sess-1")
	if err := ensureControlledRoot(root); err != nil {
		t.Fatalf("普通父目录下建根不该失败：%v", err)
	}
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() {
		t.Fatalf("受控根没建出来：%v %v", st, err)
	}
}
