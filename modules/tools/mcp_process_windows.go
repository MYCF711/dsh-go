//go:build windows

package tools

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mcpProcessTree 用 Windows Job Object 收整棵树。
//
// 为什么不是只 Kill 父进程：MCP server 常常自己是 node/python 包装器，
// 真正的服务是它的孙进程。只杀父进程会留下孤儿继续持有端口与文件锁，
// 下一次 reconnect 就会撞 "address in use"，而且没人知道是谁占的。
type mcpProcessTree struct{ job windows.Handle }

func attachMCPProcessTree(cmd *exec.Cmd) (*mcpProcessTree, error) {
	if cmd == nil || cmd.Process == nil {
		return nil, fmt.Errorf("MCP 进程树：没有可挂接的进程")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("MCP CreateJobObject: %w", err)
	}
	tree := &mcpProcessTree{job: job}
	ok := false
	defer func() {
		if !ok {
			_ = windows.TerminateJobObject(job, 1)
			_ = windows.CloseHandle(job)
		}
	}()
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = 0x00002000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return nil, fmt.Errorf("MCP SetInformationJobObject: %w", err)
	}
	access := uint32(windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE)
	handle, err := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err != nil {
		return nil, fmt.Errorf("MCP OpenProcess: %w", err)
	}
	err = windows.AssignProcessToJobObject(job, handle)
	_ = windows.CloseHandle(handle)
	if err != nil {
		return nil, fmt.Errorf("MCP AssignProcessToJobObject: %w", err)
	}
	ok = true
	return tree, nil
}

func (t *mcpProcessTree) Stop() error {
	if t == nil || t.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(t.job, 1)
}

func (t *mcpProcessTree) Close() error {
	if t == nil || t.job == 0 {
		return nil
	}
	handle := t.job
	t.job = 0
	return windows.CloseHandle(handle)
}
