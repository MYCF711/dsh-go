//go:build !windows

package main

// workspace/initializeDefault 的 Documents 目录解析（非 Windows）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// officialDocumentsDirectory 返回宿主账户的 Documents 目录绝对路径。
//
// 非 Windows 平台没有统一的 Known Folder API：这里按 POSIX 惯例取
// `$HOME/Documents`，不存在就报告"不可用"——绝不返回一个猜出来的路径让
// initializeDefault 去建目录。
func officialDocumentsDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("取账户 home 目录: %w", err)
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return "", fmt.Errorf("账户 home 目录为空")
	}
	dir := filepath.Join(home, "Documents")
	if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
		return "", fmt.Errorf("系统 Documents 目录不可用: %s", dir)
	}
	return dir, nil
}
