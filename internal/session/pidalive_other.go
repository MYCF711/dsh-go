//go:build !windows

package session

import (
	"errors"
	"syscall"
)

// pidAlive 报告 pid 是否仍是一个活进程（非 Windows 平台仅保留可编译的
// best-effort 实现，本仓正式支持范围只有 Windows，见 AGENTS.md 的支持范围裁定）。
//
// 用 `signal 0`（不真发信号，只做存在性与权限检查）：ESRCH = 进程不存在；
// EPERM = 进程存在但不是我们的。两种情况之外都按"不活"处理（保守方向是
// 不回收，而不是删错锁）。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
