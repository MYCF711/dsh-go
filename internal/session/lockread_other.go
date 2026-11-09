//go:build !windows

package session

import "os"

// readLockFileShared 在非 Windows 上等价于普通读文件（删除不受打开句柄阻塞）。
//
// Windows 是本稳定版的唯一支持平台；本文件只是 `!windows` 的兼容编译
// best-effort，不承诺非 Windows 运行时语义，也不纳入 Windows stable 验收。
// 若要声称 Linux/macOS 支持，必须另立任务在真实非 Windows 主机执行
// internal/session 锁测试（见 T07R/LEAD-SIGNOFF.md §5）。
func readLockFileShared(path string) ([]byte, error) {
	return os.ReadFile(path)
}
