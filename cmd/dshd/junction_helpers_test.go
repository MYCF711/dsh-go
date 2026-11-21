//go:build windows

package main

// 测试用的目录 junction 构造器（`mklink /J`：不需要管理员/开发者模式，正是
// 实际可被利用的那一形态）。
//
// 用 internal/sandbox.IsReparsePoint 确认"真的建成了"——**不**拿
// `os.Lstat(...).Mode()&os.ModeSymlink` 验：Go ≥1.23 的 `winsymlink=1` 语义下
// 目录 junction 报 ModeIrregular，那个判据会把"建成了"误判成"没建成"
// （与 internal/sandbox/runner_windows_junction_test.go 的同一做法）。

import (
	"os/exec"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
)

// makeTestJunction 建一个目录 junction；环境不支持时 SKIP。
func makeTestJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("本机建不了 junction，跳过这条隔离断言：%v（%s）", err, strings.TrimSpace(string(out)))
	}
	reparse, rerr := sandbox.IsReparsePoint(link)
	if rerr != nil || !reparse {
		t.Skipf("mklink 报成功但不是 reparse point（环境异常）：%v %v", rerr, reparse)
	}
}
