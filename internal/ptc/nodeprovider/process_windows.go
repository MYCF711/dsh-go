//go:build windows

package nodeprovider

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTree 用 Windows Job Object 收整棵进程树。
//
// 只杀父进程会留下孤儿：node 里 `child_process` 起的子进程会继续持有端口与文件锁，
// 下一次运行就撞 "address in use"，而且没人知道是谁占的。
type processTree interface {
	Stop() error
	Close() error
}

type jobTree struct{ job windows.Handle }

// buildProcAttrs 抑制子进程控制台窗口。
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
