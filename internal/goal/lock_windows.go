//go:build windows

package goal

// Windows 的排它文件锁：`LockFileEx` + `UnlockFileEx`。
//
// 为什么走 `syscall.NewLazyDLL` 而不是 `golang.org/x/sys/windows`：后者只是
// go.mod 里的间接依赖，直接 import 会要求提升为直接依赖（改 go.mod），
// 而这层只需要两个 kernel32 导出函数。标准库的 `syscall` 没有导出
// `LockFileEx`，所以用 LazyDLL 动态取。

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	// LOCKFILE_EXCLUSIVE_LOCK / LOCKFILE_FAIL_IMMEDIATELY（winnt.h）。
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// tryLockFile 尝试取排它锁；被占用时返回 errLockBusy（不是错误，是重试信号）。
func tryLockFile(file *os.File) error {
	var overlapped syscall.Overlapped
	ok, _, err := procLockFileEx.Call(
		file.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0,
		1, // 锁 1 字节：只要句柄上有一把锁，语义就是"这个文档被独占"
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ok != 0 {
		return nil
	}
	// ERROR_LOCK_VIOLATION = 33：别的持有者拿着锁。
	if errors.Is(err, syscall.Errno(33)) {
		return errLockBusy
	}
	return err
}

func unlockFile(file *os.File) error {
	var overlapped syscall.Overlapped
	ok, _, err := procUnlockFileEx.Call(
		file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ok != 0 {
		return nil
	}
	return err
}
