//go:build !windows

package extern

import (
	"fmt"
	"os/exec"
	"syscall"
)

// 非 Windows 分支只做**兼容编译**的 best-effort：用户裁定只支持 Windows 稳定版，
// 这里不承诺等价语义（Unix 用进程组 + 负 pid kill），也不作为验收对象。

type processTree interface {
	Stop() error
	Close() error
}

type pgidTree struct{ pid int }

func buildProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachProcessTree(cmd *exec.Cmd) (processTree, error) {
	if cmd == nil || cmd.Process == nil {
		return nil, fmt.Errorf("没有可挂接的进程")
	}
	return &pgidTree{pid: cmd.Process.Pid}, nil
}

func (t *pgidTree) Stop() error {
	if t == nil || t.pid <= 0 {
		return nil
	}
	return syscall.Kill(-t.pid, syscall.SIGKILL)
}

func (t *pgidTree) Close() error { return nil }

// ProcessAlive 用 signal 0 探测存活。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
