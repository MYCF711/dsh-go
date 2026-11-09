//go:build windows

package tools

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// processAliveMCP 判一个 PID 是否还活着。
//
// Windows 上 os.Process.Signal 对非 Kill 信号一律返回 EWINDOWS，
// 所以 `Signal(0)` 那种便携写法在这里会恒假——那会让"没泄漏"的断言
// 永远通过，也就是永远测不到泄漏。只能真的去 OpenProcess。
func processAliveMCP(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}

func killMCPProcess(t *testing.T, pid int) {
	t.Helper()
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("打开 PID %d 失败: %v", pid, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	if err := windows.TerminateProcess(handle, 9); err != nil {
		t.Fatalf("终止 PID %d 失败: %v", pid, err)
	}
}

var _ = os.Getpid
