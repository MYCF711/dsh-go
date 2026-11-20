package pwsh

// ACL 受控根在 pwsh 工具里的接线。
//
// 语义（必须与 `internal/sandbox` 和 `docs/sandbox-policy.md` 一致）：
//   - **默认不启用**：没配 `DSHD_SANDBOX_ACL_ROOT` 就完全不起 runner，
//     命令仍以宿主用户权限运行，`enforcement` 如实报 `policy-only-no-os-isolation`；
//   - 启用了就**先探一次**：本机实测 PowerShell 7 在受限令牌下加载期就 0xc0000142
//     退出（`cmd.exe` 正常，且拆开验证过拦住它的是受限令牌本身，不是 Low 完整性）。
//     探不通就如实退回策略层，并把原因写进 `runner_note`——静默退回不允许；
//   - 探通了就每条命令一个受限子进程，cwd 固定在受控根
//     `<home>/sandbox-roots/<session-id>/`，写/删只在那里成立；
//   - 受控根模式下 `run_in_background` 明确拒绝：后台面走 jobs 运行时，
//     那里打不了受限令牌，静默降级成"没沙箱"等于冒名。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
)

// runnerFor 返回这个会话可用的 ACL runner；nil 表示这次调用走策略层。
// 第二个返回值是「启用了但用不上」的具体原因（未启用时为空）。
//
// 结论按会话缓存：探针要真起一次 PowerShell（几百毫秒），不能每条命令都探。
func (t *Tool) runnerFor(ctx context.Context, shell string) (*sandbox.Runner, string) {
	if t == nil || strings.TrimSpace(t.aclRootParent) == "" {
		return nil, ""
	}
	sessionID := strings.TrimSpace(spi.SessionFrom(ctx))
	if sessionID == "" {
		return nil, "缺少会话上下文，无法为这次调用指派受控根"
	}

	t.runnerMu.Lock()
	defer t.runnerMu.Unlock()
	if t.runners == nil {
		t.runners = make(map[string]sessionRunnerState)
	}
	if cached, ok := t.runners[sessionID]; ok {
		return cached.runner, cached.note
	}

	mode := sandbox.ModeWorkspaceWrite
	if t.gate != nil {
		if sessionMode := t.gate.SessionMode(sessionID); sessionMode != "" {
			mode = sessionMode
		}
	}
	runner, err := sandbox.NewRunner(sandbox.RunnerSpec{
		SessionID: sessionID,
		ParentDir: t.aclRootParent,
		HomeDir:   t.homeDir,
		Mode:      mode,
	})
	if err != nil {
		state := sessionRunnerState{note: "已启用受控根但 runner 建不出来，退回策略层：" + err.Error()}
		t.runners[sessionID] = state
		return nil, state.note
	}
	// 能力探针：确认这个 shell 在受限身份里真的能起来。
	if probeErr := runner.ProbeExecutable(shell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "exit 0"); probeErr != nil {
		_ = runner.Close()
		state := sessionRunnerState{note: "已启用受控根但 " + filepath.Base(shell) +
			" 在受限身份下起不来，退回策略层：" + probeErr.Error()}
		t.runners[sessionID] = state
		return nil, state.note
	}
	state := sessionRunnerState{runner: runner}
	t.runners[sessionID] = state
	return runner, ""
}

// workdirInsideRoot 判断调用方给的 workdir 是否落在受控根里。
// 空 workdir 由 `Runner.Prepare` 设成受控根本身，天然成立。
func workdirInsideRoot(workdir, root string) (bool, error) {
	if strings.TrimSpace(workdir) == "" {
		return true, nil
	}
	dir, err := sandbox.CanonicalPath(workdir)
	if err != nil {
		return false, err
	}
	canonicalRoot, err := sandbox.CanonicalPath(root)
	if err != nil {
		return false, err
	}
	return sandbox.WithinRoot(canonicalRoot, dir), nil
}

// closeRunners 收掉本工具持有的全部 runner（受控根一起删）。
// 供工具卸载/测试收尾用；进程退出时由操作系统回收，但显式收更干净。
func (t *Tool) closeRunners() error {
	if t == nil {
		return nil
	}
	t.runnerMu.Lock()
	runners := t.runners
	t.runners = nil
	t.runnerMu.Unlock()
	var firstErr error
	for _, state := range runners {
		if state.runner == nil {
			continue
		}
		if err := state.runner.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("关闭受控根 %s: %w", state.runner.Root(), err)
		}
	}
	return firstErr
}
