//go:build windows

package analytics

import (
	"os/exec"
	"syscall"
)

// hideWindow 让环境查询的子进程不弹控制台窗口（官方 execFile 的 `windowsHide: true`）。
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
}
