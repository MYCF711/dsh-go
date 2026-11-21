//go:build windows

package main

// attachments 的"不接受链接"是**安全判据**（决定放不放行）：cleanAuthorizedPath
// 放行后宿主会 os.Open 那条路径按附件落盘，或者把 path 原样发给模型
// （reference 块）。所以"这条路径是不是真的就是用户给的那个文件"必须判准。
//
// Go ≥1.23 的 `winsymlink=1` 语义下，只看最后一段的 `Mode()&os.ModeSymlink`
// 对两类链接是瞎的：
//   - 目录 junction 的 FileMode 是 ModeIrregular，不是 ModeSymlink；
//   - 中间组件是链接时（`root\junction\file.txt`），Lstat 就是一个正常常规文件。
//
// 修复后判据走 internal/sandbox 的 REPARSE_POINT 属性位（#120 的同一份实现，
// 不在这里另造一套）。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanAuthorizedPathRejectsJunctionComponent(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	makeTestJunction(t, filepath.Join(root, "junction"), outside)

	// 变异点：把 sandbox.PathHasReparsePoint 判据改回裸 `Mode()&os.ModeSymlink`，
	// 这条必须红。修复前 cleanAuthorizedPath(root\junction\secret.txt) 返回
	// nil error——Lstat 看到的是 outside 里的常规文件，于是放行的路径在物理上
	// 落在用户没给过的那棵树上。
	if abs, _, err := cleanAuthorizedPath(filepath.Join(root, "junction", "secret.txt")); err == nil {
		t.Fatalf("经 junction 的路径必须在授权判据就被拒（放行后 os.Open 会落到 %q，实际放行 %q）", secret, abs)
	}

	// 正控：用户显式给出的真实常规文件照旧放行（别把正常路径一起判死）。
	plain := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(plain, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	abs, info, err := cleanAuthorizedPath(plain)
	if err != nil {
		t.Fatalf("真实常规文件不该被拒：%v", err)
	}
	if abs != plain || info == nil || info.Size() != 2 {
		t.Fatalf("放行结果不对：abs=%q info=%v（期望 %q / size=2）", abs, info, plain)
	}
}
