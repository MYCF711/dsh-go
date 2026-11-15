//go:build windows

package session

import "golang.org/x/sys/windows"

// stillActiveExitCode 是 Windows 的 STILL_ACTIVE（GetExitCodeProcess 用它表示"还在跑"）。
const stillActiveExitCode = 259

// pidAlive 报告 pid 是否仍是一个活进程。
//
// 为什么不能只用 `os.FindProcess`：Windows 上它对任何 pid 都返回成功
// （Go 文档明说 FindProcess 在 Windows 上总是成功），判不了死活。
// 所以真的去 OpenProcess：拿得到句柄再看退出码是不是 STILL_ACTIVE。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActiveExitCode
}
