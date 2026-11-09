//go:build windows

package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTree 用 Windows Job Object 收整棵进程树（与 jobs / cordis 外部进程
// 同一机制）：git 会起钩子/子进程，只杀父进程会留下孤儿继续持有 index.lock。
type processTree interface {
	Stop() error
	Close() error
}

type jobTree struct{ job windows.Handle }

func buildProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func attachProcessTree(cmd *exec.Cmd) (processTree, error) {
	if cmd == nil || cmd.Process == nil {
		return nil, fmt.Errorf("没有可挂接的进程")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	tree := &jobTree{job: job}
	ok := false
	defer func() {
		if !ok {
			_ = windows.TerminateJobObject(job, 1)
			_ = windows.CloseHandle(job)
		}
	}()
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	const jobObjectLimitKillOnJobClose = 0x00002000
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	access := uint32(windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE)
	handle, err := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err != nil {
		return nil, fmt.Errorf("OpenProcess: %w", err)
	}
	err = windows.AssignProcessToJobObject(job, handle)
	_ = windows.CloseHandle(handle)
	if err != nil {
		return nil, fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	ok = true
	return tree, nil
}

func (t *jobTree) Stop() error {
	if t == nil || t.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(t.job, 1)
}

func (t *jobTree) Close() error {
	if t == nil || t.job == 0 {
		return nil
	}
	handle := t.job
	t.job = 0
	return windows.CloseHandle(handle)
}

// runCommand 执行一条命令（合并 stdout/stderr），超时/取消时回收整棵进程树。
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
		if output != "" {
			return output, fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), waitErr, output)
		}
		return output, fmt.Errorf("%s %s: %w", binary, strings.Join(args, " "), waitErr)
	}
	return output, nil
}
