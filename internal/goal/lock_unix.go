//go:build !windows

package goal

// POSIX 的排它文件锁：`flock(LOCK_EX|LOCK_NB)`。
//
// 与 Windows 侧同语义：拿不到就返回 errLockBusy 让上层重试；进程退出时
// 内核自动释放。

import (
	"errors"
	"os"
	"syscall"
)

func tryLockFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errLockBusy
	}
	return err
}

func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
