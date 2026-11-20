//go:build !windows

package sandbox

// 非 Windows：**明确不支持**，不是「装个空实现让它静默变绿」。
//
// 官方在 Linux/macOS 上是 bwrap / Landlock / Seatbelt 链，本仓没有实现任何一条；
// 与其返回一个什么都不做的 runner 让 enforcement 看起来变好了，不如在这里
// fail-loud，让调用方如实退回策略层（`policy-only-no-os-isolation`）。

import (
	"fmt"
	"runtime"
)

// platformSupportsRunner 如实返回 false。
func platformSupportsRunner() bool { return false }

func newRunnerImpl() (runnerImpl, error) {
	return nil, fmt.Errorf("%w: %s 上没有 ACL runner（本仓只实现了 Windows 受限令牌 + "+
		"目录 ACL；官方的 bwrap/Landlock/Seatbelt 链未做）", ErrRunnerUnsupported, runtime.GOOS)
}
