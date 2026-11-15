//go:build !windows

package cua

// 非 Windows 构建的锁是 **best-effort**：只保证同进程内串行（Store.mu），
// 不保证跨进程。本仓支持范围只到 Windows（AGENTS.md「只做 Windows 稳定版」），
// 非 Windows 仅保留为可编译的兜底，明确 OUT_OF_SUPPORT——这里不做 flock，
// 是不给"看起来支持 Linux"的假象：跨进程丢更新这种问题不该被静默掩盖。
func lockFile(string) (func(), error) { return func() {}, nil }
