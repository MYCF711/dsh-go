//go:build !windows

package bash

import (
	"fmt"

	"dshgo/internal/terminal"
)

// attachPTYProcessTree 在非 Windows 上不会被走到：
// terminal.Open 会先返回 terminal.ErrUnsupported。这里只是让包在非 Windows 上可编译。
func attachPTYProcessTree(*terminal.Session) (*processControl, error) {
	return nil, fmt.Errorf("%w: 非 Windows 没有 PTY 实现", terminal.ErrUnsupported)
}
