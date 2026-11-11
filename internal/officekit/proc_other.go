//go:build !windows

package officekit

// 非 Windows：进程组 + SIGKILL（本仓只对 Windows 出稳定版，这里是兼容编译与
// 最小可用的 best-effort，明确 OUT_OF_SUPPORT，不作为发布判据）。

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

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
		return nil, errors.New("没有可挂接的进程")
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

func startManaged(cmd *exec.Cmd) (stop func(), wait func() error, err error) {
	buildProcAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	tree, _ := attachProcessTree(cmd)
	stop = func() {
		if tree != nil {
			_ = tree.Stop()
			_ = tree.Close()
			return
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	wait = func() error {
		defer func() {
			if tree != nil {
				_ = tree.Stop()
				_ = tree.Close()
			}
		}()
		return cmd.Wait()
	}
	return stop, wait, nil
}

const waitDelay = 5 * time.Second
