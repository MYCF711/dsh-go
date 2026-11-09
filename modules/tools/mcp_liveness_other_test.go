//go:build !windows

package tools

import (
	"os"
	"syscall"
	"testing"
)

func processAliveMCP(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func killMCPProcess(t *testing.T, pid int) {
	t.Helper()
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("找不到 PID %d: %v", pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("终止 PID %d 失败: %v", pid, err)
	}
}
