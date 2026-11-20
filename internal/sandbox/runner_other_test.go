//go:build !windows

package sandbox

import (
	"errors"
	"runtime"
	"testing"
)

// 非 Windows：runner 明确不支持。
//
// 这一条**显式跳过并说明**，不是静默绿：`go test -run TestRunner -v ./internal/sandbox`
// 会打印这段原因，CI 的 SKIP 行也看得到。跳过之前先把桩的 fail-loud 行为断言掉，
// 免得「不支持」被误实现成「装个空实现」。
func TestRunnerUnsupportedOnNonWindows(t *testing.T) {
	_, err := NewRunner(RunnerSpec{SessionID: "s1", HomeDir: t.TempDir(), Mode: ModeWorkspaceWrite})
	if !errors.Is(err, ErrRunnerUnsupported) {
		t.Fatalf("非 Windows 上建 runner 必须报 ErrRunnerUnsupported，实际 %v", err)
	}
	t.Skipf("sandbox ACL runner 只有 Windows 实现（受限令牌 + Low 完整性 + 目录 ACL）；"+
		"os=%s **明确不支持**，命令继续只走策略层。原因：%v", runtime.GOOS, err)
}
