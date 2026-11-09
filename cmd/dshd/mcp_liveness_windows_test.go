//go:build windows

package main

import "golang.org/x/sys/windows"

// processAlive 判一个 PID 是否还活着。
//
// Windows 不支持 `Signal(0)`（os 包对非 Kill 信号一律返回 EWINDOWS），
// 所以只能真的去 OpenProcess：拿到了就是活的，ERROR_INVALID_PARAMETER
// 就是已经退出（PID 没了）。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	// 还需要排除"句柄能开但进程已经退出"的僵尸：GetExitCodeStill active = 259。
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		_ = windows.CloseHandle(handle)
		return false
	}
	_ = windows.CloseHandle(handle)
	const stillActive = 259
	return code == stillActive
}
