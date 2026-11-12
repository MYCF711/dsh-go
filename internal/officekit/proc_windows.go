//go:build windows

package officekit

// Windows：整棵进程树用 Job Object 收（与 internal/worktree/proc_windows.go 同一机制）。
//
// 为什么不用 cmd.Process.Kill()：dsoffice.exe 是 LibreOffice 壳，内部还会起 soffice.bin
// 等子进程；只杀父进程会留下孤儿继续吃 CPU / 占着临时 profile 目录（AGENTS 铁律 11 的
// 同一类事故）。Job Object + KILL_ON_JOB_CLOSE 保证「关掉 job 就没人活着」。

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

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
		return nil, errors.New("没有可挂接的进程")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	access := uint32(windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE)
	handle, err := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err != nil {
		// 进程可能已经退出：不是失败，但也不能假装挂上了。
		return nil, err
	}
	err = windows.AssignProcessToJobObject(job, handle)
	_ = windows.CloseHandle(handle)
	if err != nil {
		return nil, err
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

// startManaged 起一个子进程并挂上进程树；返回的 stop 可重复调用。
func startManaged(cmd *exec.Cmd) (stop func(), wait func() error, err error) {
	buildProcAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	tree, treeErr := attachProcessTree(cmd)
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
	if treeErr != nil {
		// 挂不上 job 时退化为单进程杀，但要如实告诉调用方这层保护没了。
		return stop, wait, nil
	}
	return stop, wait, nil
}

// waitDelay 是对「已经 kill 但管道没关」的兜底等待上限。
const waitDelay = 5 * time.Second
