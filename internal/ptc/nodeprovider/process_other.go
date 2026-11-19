//go:build !windows

package nodeprovider

import "os/exec"

// processTree 在非 Windows 上退化为"只杀根进程"。
//
// 本仓支持范围是 Windows（AGENTS.md：只做 Windows 稳定版）；这段保留只为让
// `go build` 在别的平台过，不冒充进程组回收——真正的整棵树回收在
// process_windows.go 里用 Job Object 做。
type processTree interface {
	Stop() error
	Close() error
}

func buildProcAttrs(cmd *exec.Cmd) {}

func attachProcessTree(cmd *exec.Cmd) (processTree, error) {
	return nil, errProcessTreeUnsupported
}

type noTree struct{}

func (noTree) Stop() error  { return nil }
func (noTree) Close() error { return nil }

var errProcessTreeUnsupported = noTreeError{}

type noTreeError struct{}

func (noTreeError) Error() string { return "本平台没有进程树回收（只杀根进程）" }
