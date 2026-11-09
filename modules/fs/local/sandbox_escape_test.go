package local

// canonical / reparse point 一致性：`~` 通道只对 home 内部生效。
//
// 旧实现只做字符串前缀比较，home 里一个指向外部的目录联接（reparse point）
// 就能把 home 外的文件读出来——"可读"于是变成了"可越界读"。
// 写路径一直走 CheckWrite 的 canonical 判定，所以先钉住"读也必须是同一条规则"。

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/sandbox"
)

func TestTildeReadCannotEscapeHomeThroughReparsePoint(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	home := filepath.Join(base, "home")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{workspace, home, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("leak"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeDirLink(t, filepath.Join(home, "leak"), outside)

	fs, err := New(workspace, WithHome(home))
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.Read("~/leak/secret.txt")
	if err == nil {
		t.Fatalf("~ 经 reparse point 读到了 home 外文件：%q（canonical 规则必须与写路径一致）", string(data))
	}

	// 同一个 reparse point 的写必须是写边界错误，而不是别的什么。
	err = fs.Write("~/leak/new.txt", []byte("x"))
	if err == nil {
		t.Fatal("~ 经 reparse point 写出了 home 外文件")
	}
	if !errors.Is(err, sandbox.ErrPathOutsideHome) && !errors.Is(err, sandbox.ErrWriteOutsideWorkspace) &&
		!errors.Is(err, sandbox.ErrOutsideReadBoundary) {
		t.Fatalf("写错误类型 = %v，期望 home 越界或写/读边界之一", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "new.txt")); statErr == nil {
		t.Fatal("home 外的文件被写出来了")
	}
}

func TestTildeReadInsideHomeStillWorks(t *testing.T) {
	fs, _, home := newTildeFS(t)
	if err := os.WriteFile(filepath.Join(home, "inside.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := fs.Read("~/inside.txt")
	if err != nil {
		t.Fatalf("home 内的普通 ~ 读不应被 canonical 化误伤: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("读到 %q", string(data))
	}
}
