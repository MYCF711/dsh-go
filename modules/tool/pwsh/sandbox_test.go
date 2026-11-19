package pwsh

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
)

func gateFor(t *testing.T, mode sandbox.Mode, workspace string) *sandbox.Gate {
	t.Helper()
	g, err := sandbox.NewGate(sandbox.ProcessConfig{Mode: mode, WorkspaceRoot: workspace, HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func ctxWithSession(id string) context.Context {
	return spi.WithSession(context.Background(), id)
}

func pwshArgs(t *testing.T, command, workdir string, extra map[string]any) json.RawMessage {
	t.Helper()
	args := map[string]any{"command": command}
	if workdir != "" {
		args["workdir"] = workdir
	}
	for k, v := range extra {
		args[k] = v
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// countSpawns 把 pwsh 的进程构造点换成计数桩，返回"启动了几次"的探针。
func countSpawns(t *testing.T) *int {
	t.Helper()
	old := execCommandContext
	calls := 0
	execCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		calls++
		return old(ctx, name, arg...)
	}
	t.Cleanup(func() { execCommandContext = old })
	return &calls
}

// read-only：策略拒绝发生在进程构造之前——零进程、marker 不存在。
func TestSandboxReadOnlyDeniesBeforeProcess(t *testing.T) {
	spawns := countSpawns(t)
	ws := t.TempDir()
	marker := filepath.Join(ws, "pwsh-must-not-exist.txt")
	tool := &Tool{gate: gateFor(t, sandbox.ModeReadOnly, ws)}
	_, err := tool.Exec(ctxWithSession("session-ro"), pwshArgs(t,
		"Set-Content -Path '"+strings.ReplaceAll(marker, `\`, "/")+"' -Value ran", "", nil))
	if !errors.Is(err, sandbox.ErrDenied) {
		t.Fatalf("read-only 命令必须被策略拒绝，得到 %v", err)
	}
	if *spawns != 0 {
		t.Fatalf("deny 之后仍然构造了 %d 次进程", *spawns)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("deny 之后 PowerShell 还是启动了（marker 存在）")
	}
}

func TestSandboxNeedsApprovalDoesNotStartProcess(t *testing.T) {
	ws := t.TempDir()
	marker := filepath.Join(ws, "pwsh-unapproved.txt")
	tool := &Tool{gate: gateFor(t, sandbox.ModeWorkspaceWrite, ws)}
	spawns := countSpawns(t)
	_, err := tool.Exec(ctxWithSession("session-ww"), pwshArgs(t,
		"Set-Content -Path '"+strings.ReplaceAll(marker, `\`, "/")+"' -Value ran", "",
		map[string]any{"sandbox_permissions": "danger-full-access", "justification": "需要写工作区外的构建产物"}))
	if !errors.Is(err, sandbox.ErrNeedsApproval) {
		t.Fatalf("未审批升级应 needs-approval，得到 %v", err)
	}
	if *spawns != 0 {
		t.Fatalf("needs-approval 之后仍然构造了 %d 次进程", *spawns)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("needs-approval 之后 PowerShell 还是启动了")
	}
	if tool.lastDecision == nil || tool.lastDecision.Approval == nil || !tool.lastDecision.Approval.OneShot {
		t.Fatalf("结果缺少一次性审批需求: %+v", tool.lastDecision)
	}
}

// 已批准的一次性升级只在当前调用生效；后续调用重新要求审批。
func TestSandboxApprovedOneShotOnlyAppliesToThatCall(t *testing.T) {
	if _, _, err := resolveShell(); err != nil {
		t.Skip("本机没有 PowerShell：", err)
	}
	ws := t.TempDir()
	outside := t.TempDir()
	gate := gateFor(t, sandbox.ModeWorkspaceWrite, ws)
	tool := &Tool{gate: gate}
	ctx := ctxWithSession("session-ww")
	spawns := countSpawns(t)

	first := filepath.Join(outside, "pwsh-approved.txt")
	second := filepath.Join(outside, "pwsh-second.txt")
	command := "Set-Content -Path '"
	// 审批绑定的是命令原文：先定下完整命令，再用**同一条**命令求值/执行。
	fullFirst := command + strings.ReplaceAll(first, `\`, "/") + "' -Value ok"
	pending, err := gate.EvaluateCommand(ctx, fullFirst, outside, sandbox.PermissionDangerFullAccess)
	if err != nil || pending.Action != sandbox.ActionNeedsApproval || pending.Approval == nil {
		t.Fatalf("未审批判定 = %+v, %v", pending, err)
	}
	// 批准绑定的是这一次调用：重试时把同一个 call id 带回去。
	approvedCtx := sandbox.WithCallID(sandbox.WithEscalation(ctx, pending.Approval.Decide(true)), pending.CallID)
	if _, err := tool.Exec(approvedCtx, pwshArgs(t, fullFirst,
		outside, map[string]any{"sandbox_permissions": "danger-full-access", "justification": "需要写工作区外的构建产物"})); err != nil {
		t.Fatalf("已批准的一次性升级应执行: %v", err)
	}
	if _, statErr := os.Stat(first); statErr != nil {
		t.Fatalf("已批准调用没有真正执行: %v", statErr)
	}
	_, err = tool.Exec(ctx, pwshArgs(t, command+strings.ReplaceAll(second, `\`, "/")+"' -Value no",
		outside, map[string]any{"sandbox_permissions": "danger-full-access", "justification": "需要写工作区外的构建产物"}))
	if !errors.Is(err, sandbox.ErrNeedsApproval) {
		t.Fatalf("后续调用必须重新审批，得到 %v", err)
	}
	if _, statErr := os.Stat(second); statErr == nil {
		t.Fatal("一次性批准漂移到了后续调用")
	}
	// 同一份批准套到另一条命令：绑定不一致，拒绝且零进程。
	drifted := filepath.Join(outside, "pwsh-drifted.txt")
	spawnsBefore := *spawns
	_, err = tool.Exec(approvedCtx, pwshArgs(t, command+strings.ReplaceAll(drifted, `\`, "/")+"' -Value no",
		outside, map[string]any{"sandbox_permissions": "danger-full-access", "justification": "需要写工作区外的构建产物"}))
	if !errors.Is(err, sandbox.ErrApprovalBinding) {
		t.Fatalf("批准漂到另一条命令必须 ErrApprovalBinding，得到 %v", err)
	}
	if *spawns != spawnsBefore {
		t.Fatal("绑定失败时仍然构造了进程")
	}
	if _, statErr := os.Stat(drifted); statErr == nil {
		t.Fatal("批准被另一条命令继承了")
	}
}

func TestSandboxUnwiredToolRefusesEscalation(t *testing.T) {
	ws := t.TempDir()
	marker := filepath.Join(ws, "pwsh-unwired.txt")
	tool := &Tool{}
	_, err := tool.Exec(ctxWithSession("session-x"), pwshArgs(t,
		"Set-Content -Path '"+strings.ReplaceAll(marker, `\`, "/")+"' -Value ran", "",
		map[string]any{"sandbox_permissions": "danger-full-access", "justification": "需要写工作区外的构建产物"}))
	if !errors.Is(err, sandbox.ErrPolicyUnwired) {
		t.Fatalf("未接线时显式升级必须 fail-loud，得到 %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("未接线时仍执行了带 sandbox_permissions 的调用")
	}
}

func TestSandboxUnknownPermissionFailsLoud(t *testing.T) {
	tool := &Tool{gate: gateFor(t, sandbox.ModeWorkspaceWrite, t.TempDir())}
	_, err := tool.Exec(ctxWithSession("session-x"), pwshArgs(t, "Get-Date", "", map[string]any{"sandbox_permissions": "yolo"}))
	if !errors.Is(err, sandbox.ErrUnknownPermission) {
		t.Fatalf("未知 sandbox_permissions 必须 fail-loud，得到 %v", err)
	}
}

func TestSandboxNewToolFailsLoudOnUnknownEnv(t *testing.T) {
	t.Setenv(sandbox.EnvMode, "yolo")
	if _, err := newTool(); !errors.Is(err, sandbox.ErrUnknownMode) {
		t.Fatalf("未知环境档位必须 fail-loud，得到 %v", err)
	}
}

// 官方 validateEscalationArgs（`dsh-sandbox/src/escalation.ts:88-98`）在工具层生效：
// 请求与理由必须成对，理由必须是非空句子——仍然在构造进程之前。
func TestSandboxEscalationRequiresPairedJustification(t *testing.T) {
	ws := t.TempDir()
	spawns := countSpawns(t)
	tool := &Tool{gate: gateFor(t, sandbox.ModeWorkspaceWrite, ws)}
	cases := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"缺理由", map[string]any{"sandbox_permissions": "danger-full-access"},
			"invalid escalation: sandbox_permissions requires a justification"},
		{"孤理由", map[string]any{"justification": "因为要装依赖"},
			"invalid escalation: justification is only valid together with sandbox_permissions"},
		{"空理由", map[string]any{"sandbox_permissions": "danger-full-access", "justification": "   "},
			"invalid justification: expected a non-empty sentence"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := tool.Exec(ctxWithSession("session-pair"), pwshArgs(t, "Get-Date", "", c.extra))
			var escalation *sandbox.EscalationError
			if !errors.As(err, &escalation) || escalation.Code != sandbox.EscalationInvalidArgument {
				t.Fatalf("want SANDBOX_ESCALATION_INVALID_ARGUMENT，得到 %v", err)
			}
			if err.Error() != c.want {
				t.Fatalf("message = %q, want %q", err.Error(), c.want)
			}
			if res.Hint != c.want {
				t.Fatalf("Hint = %q, want 官方原文 %q", res.Hint, c.want)
			}
		})
	}
	if *spawns != 0 {
		t.Fatalf("畸形升级请求仍然构造了 %d 次进程", *spawns)
	}
}

// 拒绝与待审批都带官方标记行（`dsh-sandbox/src/escalation.ts:108-123`）：
// 两个设限工具族共用同一行，模型不用背工具描述。
func TestSandboxMarkersAreOfficial(t *testing.T) {
	ws := t.TempDir()
	ro := &Tool{gate: gateFor(t, sandbox.ModeReadOnly, ws)}
	res, err := ro.Exec(ctxWithSession("session-mk"), pwshArgs(t, "Get-Date", "", nil))
	if !errors.Is(err, sandbox.ErrDenied) {
		t.Fatalf("read-only 必须拒绝，得到 %v", err)
	}
	if !strings.Contains(res.Hint, "[sandbox: file access denied under read-only mode]") {
		t.Fatalf("拒绝 Hint 缺官方 marker：%q", res.Hint)
	}

	ww := &Tool{gate: gateFor(t, sandbox.ModeWorkspaceWrite, ws)}
	res, err = ww.Exec(ctxWithSession("session-mk2"), pwshArgs(t, "Get-Date", "",
		map[string]any{"sandbox_permissions": "danger-full-access", "justification": "需要工作区外的产物"}))
	if !errors.Is(err, sandbox.ErrNeedsApproval) {
		t.Fatalf("want needs-approval，得到 %v", err)
	}
	if !strings.Contains(res.Hint, "[sandbox: escalation available — retry this exact command once with sandbox_permissions") {
		t.Fatalf("待审批 Hint 缺官方提示行：%q", res.Hint)
	}
	if !strings.Contains(res.Hint, "justification; the approval prompt asks the user]") {
		t.Fatalf("提示行必须逐字官方：%q", res.Hint)
	}
}

// read-only 不是硬上限：官方阶梯（`escalation.ts:64-67`）允许单次升到 workspace-write，
// 但必须先拿到审批——工具层看到的是 needs-approval，不是 deny。
func TestSandboxReadOnlyCanEscalateToWorkspaceWrite(t *testing.T) {
	ws := t.TempDir()
	tool := &Tool{gate: gateFor(t, sandbox.ModeReadOnly, ws)}
	spawns := countSpawns(t)
	res, err := tool.Exec(ctxWithSession("session-ro-up"), pwshArgs(t, "Get-Date", "",
		map[string]any{"sandbox_permissions": "workspace-write", "justification": "要写工作区内的产物"}))
	if !errors.Is(err, sandbox.ErrNeedsApproval) {
		t.Fatalf("read-only 的 workspace-write 请求应 needs-approval，得到 %v", err)
	}
	if *spawns != 0 {
		t.Fatalf("未审批前构造了 %d 次进程", *spawns)
	}
	if tool.lastDecision == nil || tool.lastDecision.Approval == nil ||
		tool.lastDecision.Approval.Permission != sandbox.PermissionWorkspaceWrite {
		t.Fatalf("审批需求必须绑定 workspace-write 目标：%+v", tool.lastDecision)
	}
	if !strings.Contains(res.Hint, "escalation available") {
		t.Fatalf("待审批必须广告升级提示：%q", res.Hint)
	}
}
