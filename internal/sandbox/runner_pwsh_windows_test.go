//go:build windows

package sandbox

// 受限身份下「程序到底能不能起」必须在**接线前**测得准。
//
// 本机实测：`cmd.exe` 在受限令牌里正常；PowerShell 7 加载期就 0xc0000142
// (STATUS_DLL_INIT_FAILED)。把两个因子拆开验证过——只造受限令牌、**不降完整性级别**
// 仍然是 0xc0000142，所以拦住它的是受限令牌本身，不是 Low 完整性标签。
//
// 因此这条测试不写死「pwsh 一定能跑」也不写死「一定不能跑」（那是本机事实，不是
// 产品契约），而是断言**探针结论与真实启动结果一致**：探针若与实际不符，pwsh-sandbox
// 就会在第一条真实命令上炸，而不是在启用时如实退回策略层。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func pwshExecutable(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("pwsh"); err == nil {
		return p
	}
	candidate := `C:\Program Files\PowerShell\7\pwsh.exe`
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	candidate = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	t.Skip("本机没有 PowerShell，无法验证受控身份里的启动能力")
	return ""
}

func TestConfinedShellProbeMatchesReality(t *testing.T) {
	shell := pwshExecutable(t)
	base := t.TempDir()
	parent := filepath.Join(base, "sandbox-roots")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("建受控根父目录: %v", err)
	}
	runner, err := NewRunner(RunnerSpec{SessionID: "probe-consistency", ParentDir: parent, Mode: ModeWorkspaceWrite})
	if err != nil {
		t.Skipf("本环境用不了 ACL runner，**明确跳过**：%v", err)
	}
	t.Cleanup(func() {
		if closeErr := runner.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})

	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "exit 0"}
	probeErr := runner.ProbeExecutable(shell, args...)

	// 不走探针、直接跑一次同样的命令：这才是「真实结果」。
	direct := exec.Command(shell, args...)
	release, err := runner.Prepare(direct)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	out, runErr := direct.CombinedOutput()
	if relErr := release(); relErr != nil {
		t.Fatalf("release: %v", relErr)
	}

	if (probeErr == nil) != (runErr == nil) {
		t.Fatalf("探针结论与真实启动结果不一致：探针 err=%v，直跑 err=%v 输出=%q —— "+
			"接线会在错误的位置失败", probeErr, runErr, strings.TrimSpace(string(out)))
	}
	if probeErr != nil {
		// 本机事实：能力探针必须让工具退回策略层，而不是硬上。
		t.Logf("本机实测：%s 在受限身份下起不来（真实 err=%v），"+
			"pwsh-sandbox 接线时必须**如实退回策略层**；探针 err=%v", filepath.Base(shell), runErr, probeErr)
		return
	}
	t.Logf("本机实测：%s 能在受限身份下运行（输出 %q）；受控根 %s",
		filepath.Base(shell), strings.TrimSpace(string(out)), runner.Root())
}
