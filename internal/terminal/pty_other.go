//go:build !windows

package terminal

import (
	"fmt"
	"runtime"
)

// openPlatform 在非 Windows 上**如实**返回 ErrUnsupported。
//
// 这不是环境错误，是能力缺失：调用方（cmd/dshd）把它翻成官方 501 not_implemented
// 四字段错误体。**禁止**在这里退化成管道实现来"让接口看起来能用"——
// 那正是本任务要消除的"用管道 shell 冒充 PTY"。
func openPlatform(Config) (platformProcess, error) {
	return nil, fmt.Errorf("%w: %s 上还没有接线 PTY 实现；官方形状是 501 not_implemented",
		ErrUnsupported, runtime.GOOS)
}
