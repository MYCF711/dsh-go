//go:build !windows

package sandbox

import "path/filepath"

// ResolveExistingPath 在非 Windows 上就是 EvalSymlinks（符号链接语义一致，
// 没有 junction/重解析点这一层）。Windows 版本见 reparse_windows.go。
func ResolveExistingPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
