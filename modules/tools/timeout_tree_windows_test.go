//go:build windows

package tools

import (
	"bufio"
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

const jobObjectLimitKillOnJobClose = 0x00002000

// windowsHeartbeatTree 用 Windows Job Object 拥有整棵树（与产品里的 bash 会话
// 同一套机制：KILL_ON_JOB_CLOSE + 归属句柄）。
type windowsHeartbeatTree struct {
	cmd *exec.Cmd
	out *bufio.Reader
	job windows.Handle
}

func (t *windowsHeartbeatTree) Out() *bufio.Reader { return t.out }
func (t *windowsHeartbeatTree) Wait() error        { return t.cmd.Wait() }

func (t *windowsHeartbeatTree) Kill() error {
	if t.job == 0 {
		return nil
	}
	job := t.job
	t.job = 0
	err := windows.TerminateJobObject(job, 1)
	if cerr := windows.CloseHandle(job); err == nil {
		err = cerr
	}
	return err
}

// startHeartbeatTree 起这棵树并把 root 放进 Job Object。
//
// 顺序很重要：**先**创建 Job 并 assign root，root 才会在派生自己的子进程时把
// 归属传下去。heartbeatScript 里的那 300ms 就是给这个窗口留的——不睡的话，
// 子 shell 可能早于 assign 被创建，而它不会属于任何 Job。
func startHeartbeatTree() (heartbeatTree, error) {
	bash, err := findBashForTest()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bash, "--noprofile", "--norc", "-c", heartbeatScript)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t := &windowsHeartbeatTree{cmd: cmd, out: bufio.NewReader(out)}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	t.job = job
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = t.Kill()
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	access := uint32(windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE)
	h, err := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = t.Kill()
		return nil, fmt.Errorf("OpenProcess(root): %w", err)
	}
	err = windows.AssignProcessToJobObject(job, h)
	_ = windows.CloseHandle(h)
	if err != nil {
		_ = t.Kill()
		return nil, fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return t, nil
}

// findBashForTest 找 Git Bash：本仓的执行层本来就依赖它（modules/exec/bash 的
// findBash），没有它这条证据只能跳过——门禁判据是"树被收掉"，不是"某台机器装了什么"。
func findBashForTest() (string, error) {
	if p, err := exec.LookPath("bash"); err == nil {
		return p, nil
	}
	for _, p := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\Git\usr\bin\bash.exe`,
	} {
		if _, err := exec.LookPath(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("找不到 bash：装 Git for Windows")
}
