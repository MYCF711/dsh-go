//go:build !windows

package tools

import "os/exec"

// mcpProcessTree 在非 Windows 上是空实现：Close 里的 Process.Kill 只收父进程，
// 孙进程由子进程自己负责（本项目只发版 Windows，POSIX 侧没有进程组再挂接的席位）。
type mcpProcessTree struct{}

func attachMCPProcessTree(*exec.Cmd) (*mcpProcessTree, error) { return &mcpProcessTree{}, nil }
func (*mcpProcessTree) Stop() error                           { return nil }
func (*mcpProcessTree) Close() error                          { return nil }
