//go:build windows

package main

// workspace/initializeDefault 的 Documents 目录解析（Windows）。
//
// 与官方 `defaultWorkspaceDirectory` 的 Windows 分支同源：读当前账户的
// Documents **已知文件夹**（[Environment]::GetFolderPath(MyDocuments) 的
// Win32 等价物 SHGetKnownFolderPath(FOLDERID_Documents)），因此 OneDrive 重定向
// 后的 Documents 也能拿到真实路径，不是猜 `%USERPROFILE%\Documents`。

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// officialDocumentsDirectory 返回当前账户 Documents 已知文件夹的绝对路径。
func officialDocumentsDirectory() (string, error) {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Documents, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("解析系统 Documents 已知文件夹: %w", err)
	}
	return dir, nil
}
