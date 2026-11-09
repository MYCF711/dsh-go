//go:build !windows

package tools

import (
	"bufio"
	"os/exec"
	"syscall"
)

// unixHeartbeatTree 用独立进程组拥有整棵树（与产品里 bash 会话的 Unix 归属方式一致）。
type unixHeartbeatTree struct {
	cmd *exec.Cmd
	out *bufio.Reader
}

func (t *unixHeartbeatTree) Out() *bufio.Reader { return t.out }
func (t *unixHeartbeatTree) Wait() error        { return t.cmd.Wait() }

// Kill 终止整个进程组（负 PID = 组内全部进程）。
func (t *unixHeartbeatTree) Kill() error {
	if t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
}

// startHeartbeatTree 起这棵树，root 落在自己的进程组里。
func startHeartbeatTree() (heartbeatTree, error) {
	cmd := exec.Command("/bin/sh", "-c", heartbeatScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &unixHeartbeatTree{cmd: cmd, out: bufio.NewReader(out)}, nil
}
