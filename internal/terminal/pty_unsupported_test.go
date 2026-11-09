//go:build !windows

package terminal_test

// 非 Windows 的官方形状：**没有实现就如实说没有**。
// 这不是环境错误——调用方（cmd/dshd）据此回 501 not_implemented 四字段错误体。

import (
	"errors"
	"runtime"
	"testing"

	"dshgo/internal/terminal"
)

func TestPTYUnsupportedOnNonWindows(t *testing.T) {
	_, err := terminal.Open(terminal.Config{
		Path: "/bin/sh", Args: []string{"-i"}, Cols: 80, Rows: 24,
	})
	if err == nil {
		t.Fatalf("%s 上不应该声称有 PTY 实现", runtime.GOOS)
	}
	if !errors.Is(err, terminal.ErrUnsupported) {
		t.Fatalf("非 Windows 必须返回 ErrUnsupported（官方形状 501），拿到 %v", err)
	}
}
