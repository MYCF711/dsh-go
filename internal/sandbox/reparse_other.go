//go:build !windows

package sandbox

import "path/filepath"

// ResolveExistingPath 在非 Windows 上就是 EvalSymlinks（符号链接语义一致，
// 没有 junction/重解析点这一层）。Windows 版本见 reparse_windows.go。
func ResolveExistingPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// IsReparsePoint 在非 Windows 上恒为 false：没有 junction/重解析点这一层；
// 符号链接的**最后一段**仍由调用方自己的 `Mode()&os.ModeSymlink` 判据负责
// （那是各平台一致的既有行为）。Windows 版本（按 GetFileAttributes 的属性位判）
// 见 reparse_windows.go。
func IsReparsePoint(path string) (bool, error) { return false, nil }

// PathHasReparsePoint 在非 Windows 上恒为 false：没有 junction 这一层。
// 注意这意味着"**中间组件**是符号链接"的情形在这里不判——与 #120 之前的行为
// 一致，本仓的支持范围只有 Windows 稳定版（见 AGENTS.md §0 支持范围裁定）。
func PathHasReparsePoint(path string) (bool, error) { return false, nil }

// ResolveExistingPath 在非 Windows 上就是 EvalSymlinks（与 #120 的
// resolveExistingPath 同义，只是导出给别的包用）。
func ResolveExistingPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
