//go:build !windows

package analytics

import "os/exec"

// hideWindow 在非 Windows 上是空操作（本仓稳定版只支持 Windows；保留它只是
// 为了让 internal/analytics 在其它平台上仍可编译与测试）。
func hideWindow(cmd *exec.Cmd) {}
