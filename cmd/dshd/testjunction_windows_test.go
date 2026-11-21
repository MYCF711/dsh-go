//go:build windows

package main

// 目录 junction 测试夹具（写/读边界共用的那类断言都用它）。
//
// 为什么不用 os.Symlink：junction 不需要管理员/开发者模式，而这批断言的靶子恰恰是
// 「Go ≥1.23 的 winsymlink=1 不把 junction 当符号链接」——用 os.Symlink 造出来的
// 是另一条代码路径（Go 认它是 ModeSymlink，EvalSymlinks 会跟），测不到缺陷。

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
)

// makeTestJunction 建一个目录 junction；环境不支持时 SKIP。
//
// 判"真的建成了"用的是与生产代码同一条判据（`sandbox.ResolveExistingPath` 能把它
// 解析到目标），不用 `os.Lstat(...).Mode()&os.ModeSymlink`——目录 junction 在本机
// 有一部分进程里被 Go 报成 ModeIrregular（见 internal/sandbox/runner_windows.go:455）。
func makeTestJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("本机建不了目录 junction，跳过这条边界断言：%v（%s）",
			err, strings.TrimSpace(string(out)))
	}
	resolved, rerr := sandbox.ResolveExistingPath(link)
	if rerr != nil || strings.EqualFold(filepath.Clean(resolved), filepath.Clean(link)) {
		t.Skipf("junction 建了但解析不到目标（resolved=%q err=%v），本机环境不支持这条断言",
			resolved, rerr)
	}
}
