//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	// processImageQueryAttempts 是 QueryFullProcessImageName 遇到**瞬时错误**
	// 的有界重试次数（含首次）。T06 F4 的现场是"进程对象已建、映像段尚未就绪"
	// 的 ERROR_GEN_FAILURE(31)；3 × 20ms 足以跨过这个窗口，又不会把一次 turn
	// 的启动延迟变成可感知的卡顿。
	processImageQueryAttempts = 3
	// processImageQueryBackoff 是两次查询之间的退避。只对 ERROR_GEN_FAILURE 生效。
	processImageQueryBackoff = 20 * time.Millisecond
)

// 两个系统调用点单独抽出来，只有一个原因：**让瞬时错误可注入**。
//
// 生产值就是唯一实现（下面两个闭包）；测试把它们换成受控实现，才能造出
// T06 F4 的 `ERROR_GEN_FAILURE` 现场而不去赌真实进程对象何时就绪。
// 除了可注入，这一层不改变任何判定：打开句柄、查询完整映像、逐字比较。
var (
	openProcessForImage = func(pid int) (windows.Handle, error) {
		return windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	}
	queryProcessImageName = func(h windows.Handle) (string, error) {
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
			return "", err
		}
		return windows.UTF16ToString(buf[:n]), nil
	}
)

// queryProcessImageWithRetry 只在 ERROR_GEN_FAILURE 上重试；其余错误一次就失败。
//
// 为什么只认 31：ERROR_ACCESS_DENIED（权限边界）与"映像不匹配"都是**判定**，
// 不是瞬时现象。对它们重试等于延长判定窗口；放行则是把归属校验变成摆设。
// 重试耗尽仍失败时保留原始错误链（errors.Is 能继续看到 31），并标出重试次数。
func queryProcessImageWithRetry(h windows.Handle, pid int) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= processImageQueryAttempts; attempt++ {
		name, err := queryProcessImageName(h)
		if err == nil {
			return name, nil
		}
		lastErr = err
		if !isTransientProcessImageQueryError(err) {
			return "", fmt.Errorf("查询 PID %d 完整映像路径失败: %w", pid, err)
		}
		if attempt < processImageQueryAttempts {
			time.Sleep(processImageQueryBackoff)
		}
	}
	return "", fmt.Errorf(
		"查询 PID %d 完整映像路径失败（瞬时错误，已重试 %d 次）: %w",
		pid, processImageQueryAttempts, lastErr)
}

// isTransientProcessImageQueryError 判定"重试就可能好"的唯一错误：ERROR_GEN_FAILURE。
//
// 用 errors.As 取 errno 而不是字符串匹配：x/sys 可能返回裸 Errno，也可能被
// *os.SyscallError 包一层；两种都要认。
func isTransientProcessImageQueryError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == windows.ERROR_GEN_FAILURE
	}
	return false
}

// verifyProcessImage 只查询调用方给出的精确 PID；不枚举镜像名、不调用 taskkill。
func verifyProcessImage(pid int, expectedImage string) error {
	h, err := openProcessForImage(pid)
	if err != nil {
		return fmt.Errorf("打开 PID %d 失败: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	actual, err := queryProcessImageWithRetry(h, pid)
	if err != nil {
		return err
	}
	actual, err = canonicalProcessImage(actual)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expectedImage) {
		return fmt.Errorf("PID %d 映像=%q，期望=%q", pid, actual, expectedImage)
	}
	return nil
}
