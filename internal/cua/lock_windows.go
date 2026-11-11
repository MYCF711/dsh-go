//go:build windows

package cua

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile 取跨进程排它锁（`LockFileEx`）。
//
// 为什么必须有它：应用决策的读改写是"读最新 → 改 → 原子替换"，两个宿主进程
// 同时做就会丢更新（A 的 revoke 被 B 的旧快照覆盖回去 = 用户的拒绝被撤销）。
// 官方在同一位置用 `withFileLock`（`dsh-cua/src/permission-store.ts:115`），
// 本仓用同一套语义：锁一个独立锁文件的 1 个字节，数据文件保持原子替换写。
//
// 锁不到时**阻塞等待**而不是立刻失败：持有者是一次毫秒级读改写，
// 把"正在写"报成错误只会让一次正常的 revoke 变成 500。
func lockFile(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		_ = file.Close()
	}, nil
}
