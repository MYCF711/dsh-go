package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// 官方 `workspaceController.documentsLookupTimeoutMs`（默认 10000ms）。
//
// 官方给 Documents 目录查找 10000ms 预算，理由是 Windows 的已知文件夹查询在
// 漫游配置、断网的 OneDrive 重定向、被安全软件拦住的 shell 扩展上确实会卡住。
// 宿主原来在 workspace/initializeDefault 里同步调 `SHGetKnownFolderPath`，没有
// 任何预算：一个卡死的 Win32 调用会把这次请求（以及装配它的启动路径）一起拖住。
//
// 为什么超时后返回错误而不是猜 `%USERPROFILE%\Documents`：猜出来的路径可能是错的
// （OneDrive 重定向后真目录在别处），而"默认工作区指错目录"比"这一步失败"更难查。
const (
	// DefaultDocumentsLookupTimeoutMS 是官方 `workspaceController.documentsLookupTimeoutMs` 的默认值。
	DefaultDocumentsLookupTimeoutMS = 10000
	// EnvDocumentsLookupTimeoutMS 覆盖 Documents 查找预算（毫秒）；0 = 不设预算。
	EnvDocumentsLookupTimeoutMS = "DSHD_DOCUMENTS_LOOKUP_TIMEOUT_MS"
)

// documentsLookupTimeout 解析查找预算：默认 10000ms；0 = 不设预算；非法值退回默认。
func documentsLookupTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(EnvDocumentsLookupTimeoutMS))
	if raw == "" {
		return time.Duration(DefaultDocumentsLookupTimeoutMS) * time.Millisecond
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return time.Duration(DefaultDocumentsLookupTimeoutMS) * time.Millisecond
	}
	return time.Duration(n) * time.Millisecond
}

// documentsDirWithTimeout 给一次 Documents 查找套上预算。
//
// 超时只表示"这次查找没希望了"：goroutine 用一个容量 1 的 channel 写结果，
// 所以即使调用方先返回，卡住的 goroutine 也不会永久阻塞在发送上（真正的
// Win32 调用无法被 Go 取消，这一点是 cgo/syscall 的既有限制，写在注释里而不是
// 装作能取消）。
func documentsDirWithTimeout(lookup func() (string, error), timeout time.Duration) (string, error) {
	if timeout <= 0 {
		return lookup()
	}
	type lookupResult struct {
		dir string
		err error
	}
	done := make(chan lookupResult, 1)
	go func() {
		dir, err := lookup()
		done <- lookupResult{dir: dir, err: err}
	}()
	select {
	case res := <-done:
		return res.dir, res.err
	case <-time.After(timeout):
		return "", fmt.Errorf("解析 Documents 已知文件夹超过 %s 预算（由 %s 控制）",
			timeout, EnvDocumentsLookupTimeoutMS)
	}
}

// documentsDirBounded 是装配期用的入口：把真实查找函数包成带预算的形式。
func documentsDirBounded() (string, error) {
	return documentsDirWithTimeout(officialDocumentsDirectory, documentsLookupTimeout())
}
