//go:build windows

package creation

// containedFile 是生成包的**包含性判据**（决定放不放行）：放行后
// readManifest/readCapped/requireBuiltEntry 会真的打开那条路径读内容。
//
// Go ≥1.23 的 `winsymlink=1` 语义下，只看最后一段的 `Mode()&os.ModeSymlink`
// 对两类链接是瞎的：
//   - 目录 junction 的 FileMode 是 ModeIrregular，不是 ModeSymlink；
//   - 中间组件是链接时（`bundle\junction\evil.js`），Lstat 看到的是一个正常常规文件。
//
// 判据复用 internal/sandbox 的同一份实现（#120）：最终一段用
// IsReparsePoint（GetFileAttributes 的 REPARSE_POINT 属性位），中间组件用
// ResolveExistingPath 解析后再核一次包含性。普通包目录行为不变。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
)

// makeCreationJunction 建一个目录 junction；环境不支持时 SKIP。
func makeCreationJunction(t *testing.T, link, target string) {
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

func TestContainedFileRejectsJunction(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "bundle")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(outside, "evil.js")
	if err := os.WriteFile(evil, []byte("module.exports = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeCreationJunction(t, filepath.Join(dir, "junction"), outside)

	// 变异点二：最后一段本身是 junction。改回裸 ModeSymlink 后这条红——修复前
	// Lstat 报 ModeIrregular（既不是 symlink 也不 IsRegular），containedFile
	// 照样返回路径，调用方再 os.Open 就是打开一个目录。
	if got, err := containedFile(dir, "junction"); err == nil {
		t.Fatalf("最后一段是 junction 时 containedFile 必须拒绝，实际放行 %q", got)
	}

	// 变异点一（真洞）：中间组件是 junction。改回裸 ModeSymlink 后这条红——修复前
	// Lstat(bundle\junction\evil.js) 是正常常规文件，判据全过，读的却是包外的文件。
	if got, err := containedFile(dir, "junction/evil.js"); err == nil {
		t.Fatalf("中间组件是 junction 时 containedFile 必须拒绝，实际放行 %q（读的会是包外的 %q）", got, evil)
	}

	// 变异点三：**指向包内**的 junction（解析后没逃出包，所以只有"最后一段是链接"
	// 这条判据能拒它）。改回裸 ModeSymlink 后这条红：ModeIrregular 既不是 symlink、
	// 也不是目录，原判据会放行。
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "inside.js"), []byte("module.exports = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeCreationJunction(t, filepath.Join(dir, "inside-junction"), filepath.Join(dir, "sub"))
	if got, err := containedFile(dir, "inside-junction"); err == nil {
		t.Fatalf("最后一段是指向包内的 junction 也必须拒（生成包里不允许任何链接），实际放行 %q", got)
	}

	// 正控：包内普通文件照旧放行，且返回的仍是词法路径（不改变调用方拿到的路径）。
	plain := filepath.Join(dir, "ok.js")
	if err := os.WriteFile(plain, []byte("module.exports = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := containedFile(dir, "ok.js")
	if err != nil {
		t.Fatalf("包内普通文件不该被拒：%v", err)
	}
	if got != plain {
		t.Fatalf("返回路径变了：%q（期望 %q）", got, plain)
	}
}
