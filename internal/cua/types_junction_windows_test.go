//go:build windows

package cua

// 应用身份（Grant.ID）对目录 junction 的盲区。
//
// `ApplicationIdentity` 的 id 是 `sha256(lower(CanonicalExecutablePath(path)))`，
// 也就是"这条许可/拒绝属于哪个应用"的身份键。只靠 `filepath.EvalSymlinks` 解析时，
// Go ≥1.23 的 `winsymlink=1` 让同一个 exe 的两种拼法算出两个身份：
//
//	<junction>/app.exe   → EvalSymlinks 对 junction 下面的路径直接 errno=3，
//	                       CanonicalExecutablePath 退回原始拼法 → 身份 A
//	<真实目录>/app.exe   → 解析到长名真实路径                       → 身份 B
//
// 后果是 deny/revoke 只认其中一条，换拼法就落在"没决策过"上。反向控制同样要钉：
// 普通路径的摘要**不许变**（改了就等于把已有的持久决策全部作废）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
)

func makeIdentityJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("本机建不了目录 junction，跳过这条身份断言：%v（%s）",
			err, strings.TrimSpace(string(out)))
	}
	resolved, rerr := sandbox.ResolveExistingPath(link)
	if rerr != nil || strings.EqualFold(filepath.Clean(resolved), filepath.Clean(link)) {
		t.Skipf("junction 建了但解析不到目标（resolved=%q err=%v）", resolved, rerr)
	}
}

// TestApplicationIdentityIsOneExeAcrossJunction 同一个 exe 的两种拼法必须是同一个身份。
func TestApplicationIdentityIsOneExeAcrossJunction(t *testing.T) {
	realDir := filepath.Join(t.TempDir(), "apps")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(realDir, "demo.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "apps-link")
	makeIdentityJunction(t, link, realDir)

	viaReal := ApplicationIdentity(exe)
	viaJunction := ApplicationIdentity(filepath.Join(link, "demo.exe"))
	if viaReal.ID == "" || viaJunction.ID == "" {
		t.Fatalf("身份摘要不能为空：real=%+v junction=%+v", viaReal, viaJunction)
	}
	if viaReal.ID != viaJunction.ID {
		t.Fatalf("同一个 exe 经 junction 拼法与真实路径算出了两个身份（junction=%s real=%s）"+
			"——deny/revoke 只会认得其中一条", viaJunction.ID, viaReal.ID)
	}
	// ExecutablePath 也要落到真实路径（它用于取图标/显示，junction 拼法会让界面
	// 显示一个"看起来在别处"的路径）。
	if !strings.EqualFold(viaJunction.ExecutablePath, viaReal.ExecutablePath) {
		t.Fatalf("规范路径不一致：junction=%q real=%q",
			viaJunction.ExecutablePath, viaReal.ExecutablePath)
	}
}

// TestApplicationIdentityPlainPathUnchanged 反向控制：普通路径的身份一个字节都不变。
//
// 摘要写死成期望值是故意的：这条钉的正是"已有持久决策不许被这次修复作废"。
func TestApplicationIdentityPlainPathUnchanged(t *testing.T) {
	realDir := filepath.Join(t.TempDir(), "apps")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(realDir, "stable.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 期望值 = 用 ResolveExistingPath 的结果手算，与修前的 EvalSymlinks 结果相同
	// （普通路径没有重解析点，两者一字不差）。
	canonical, err := sandbox.ResolveExistingPath(exe)
	if err != nil {
		t.Fatalf("ResolveExistingPath: %v", err)
	}
	want := ApplicationIdentity(canonical)
	got := ApplicationIdentity(exe)
	if got.ID != want.ID {
		t.Fatalf("普通路径的身份必须与修前一致：got=%s want=%s", got.ID, want.ID)
	}
	if !strings.EqualFold(got.ExecutablePath, want.ExecutablePath) {
		t.Fatalf("普通路径的规范形态变了：got=%q want=%q", got.ExecutablePath, want.ExecutablePath)
	}
}
