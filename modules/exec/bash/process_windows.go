//go:build windows

package bash

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"dshgo/internal/spi"
	"golang.org/x/sys/windows"
)

const (
	jobObjectLimitKillOnJobClose = 0x00002000
	stillActive                  = 259
	// createNewProcessGroup 让新 bash 成为独立控制台进程组的根：宽限终止请求
	// （CTRL_BREAK）只打这棵树，不会连带宿主进程自己。
	createNewProcessGroup = 0x00000200
)

type windowsJobTree struct {
	job windows.Handle
	// pgid 是 bash root 的进程组 id（= root PID，创建时带 CREATE_NEW_PROCESS_GROUP）。
	// 宽限终止请求按进程组投递，宿主进程自己不受影响。
	pgid int
}

// graceful 投递 CTRL_BREAK：Windows 上没有可捕获的 SIGTERM，这是唯一能让
// console 应用「先收尾」的通道（官方同样把 TERM 层做成宽限等待）。
//
// 注意 MSYS bash 自己的 console 处置**不跑** bash trap（实测：进程直接以
// STATUS_CONTROL_C_EXIT 结束），所以对 bash 会话这一层常常等价于"快速但非
// 强杀"的退出；对装了 console handler 的程序（pwsh/node/Go 子进程）才是真收尾。
// 无论哪种，宽限到期仍存活就一定走 TerminateJobObject。
func (t *windowsJobTree) graceful() (string, error) {
	if t.pgid <= 0 {
		return "", fmt.Errorf("没有进程组 id，无法投递宽限终止请求")
	}
	if err := windows.GenerateConsoleCtrlEvent(syscall.CTRL_BREAK_EVENT, uint32(t.pgid)); err != nil {
		return "", fmt.Errorf("投递 CTRL_BREAK 到进程组 %d 失败: %w", t.pgid, err)
	}
	return DeliveryCtrlBreak, nil
}

func (t *windowsJobTree) terminate() error {
	if t.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(t.job, 1)
}

func (t *windowsJobTree) release() error {
	if t.job == 0 {
		return nil
	}
	h := t.job
	t.job = 0
	return windows.CloseHandle(h)
}

func (t *windowsJobTree) terminateOnClose(bool) bool { return true }

// prepareProcessTree 让 bash 成为独立控制台进程组的根：宽限终止请求（CTRL_BREAK）
// 只打这棵树，既不会连带宿主，也不会被宿主的 Ctrl-C 顺手带走。
func prepareProcessTree(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
	return nil
}

// attachProcessTree 在 startSession 返回、任何用户命令写入 stdin 之前调用。
// bash 以 --noprofile --norc -s 启动，因此此窗口内不会执行用户命令或派生用户子进程。
func attachProcessTree(cmd *exec.Cmd) (*processControl, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	native := &windowsJobTree{job: job, pgid: cmd.Process.Pid}
	ok := false
	defer func() {
		if !ok {
			_ = native.terminate() // 若已 assign，KILL_ON_JOB_CLOSE 也只收这棵新树
			_ = native.release()
		}
	}()
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	access := uint32(windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE)
	h, err := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err != nil {
		return nil, fmt.Errorf("OpenProcess(new bash): %w", err)
	}
	err = windows.AssignProcessToJobObject(job, h)
	_ = windows.CloseHandle(h)
	if err != nil {
		return nil, fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	identity, err := queryProcessIdentity(cmd.Process.Pid)
	if err != nil {
		return nil, err
	}
	if !identity.Active || identity.Image == "" || identity.CreationID == "" {
		return nil, fmt.Errorf("%w: 新 bash 元数据不完整: %+v", spi.ErrProcessTreeUnsafe, identity)
	}
	identity.ProcessTree = "windows-job-object"
	ok = true
	return &processControl{identity: identity, native: native}, nil
}

func queryProcessImage(h windows.Handle) (string, error) {
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}

func queryProcessIdentity(pid int) (processIdentity, error) {
	identity := processIdentity{PID: pid}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_NOT_FOUND) || errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return identity, spi.ErrJobProcessExited
		}
		return identity, err
	}
	defer windows.CloseHandle(h)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return identity, err
	}
	identity.Active = exitCode == stillActive
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return identity, err
	}
	identity.CreationID = strconv.FormatUint(uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime), 16)
	if identity.Active {
		identity.Image, err = queryProcessImage(h)
		if err != nil {
			return identity, err
		}
	}
	return identity, nil
}
