//go:build !windows

package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
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

func runCommand(ctx context.Context, dir, binary string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	buildProcAttrs(cmd)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s: %w", binary, err)
	}
	tree, treeErr := attachProcessTree(cmd)
	if treeErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if tree != nil {
		_ = tree.Stop()
		_ = tree.Close()
	}
	output := strings.TrimSpace(buf.String())
	if waitErr != nil {
		return output, fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), waitErr, output)
	}
	return output, nil
}
