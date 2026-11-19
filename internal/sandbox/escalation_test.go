package sandbox

import (
	"errors"
	"strings"
	"testing"
)

// 这些断言钉的是**官方逐字文案**（`@deepseek-ai/dsh-sandbox/src/escalation.ts`，
// 0.2.2-rc.2）。模型看到的就是这几行；改字就是改契约，必须先改官方向量表。
func TestEscalationMarkersAreOfficialVerbatim(t *testing.T) {
	if got, want := SandboxDenialMarker(ModeReadOnly), "[sandbox: file access denied under read-only mode]"; got != want {
		t.Fatalf("denial marker = %q, want %q", got, want)
	}
	if got, want := SandboxDenialMarker(ModeWorkspaceWrite), "[sandbox: file access denied under workspace-write mode]"; got != want {
		t.Fatalf("denial marker = %q, want %q", got, want)
	}
	wantHint := "[sandbox: escalation available — retry this exact command once with sandbox_permissions " +
		"(the narrowest wider mode that suffices) + justification; the approval prompt asks the user]"
	if got := EscalationHintMarker("command"); got != wantHint {
		t.Fatalf("escalation hint = %q, want %q", got, wantHint)
	}
	if got := EscalationHintMarker("operation"); strings.Contains(got, "command") {
		t.Fatalf("hint 必须按族替换名词: %q", got)
	}
	wantDesc := "The narrowest wider sandbox mode for a one-shot retry of the exact command the sandbox just denied; " +
		"the retry asks the user for approval."
	if got := SandboxPermissionsDescription("command"); got != wantDesc {
		t.Fatalf("permissions description = %q, want %q", got, wantDesc)
	}
	wantJust := "Required with sandbox_permissions: one sentence for the user explaining why this exact command needs the wider access. " +
		"Use the language of the user’s current request."
	if got := JustificationDescription(); got != wantJust {
		t.Fatalf("justification description = %q, want %q", got, wantJust)
	}
}

// 官方错误码表是闭集且有序（`escalation.ts:33-38`）。
func TestEscalationErrorCodesAreClosedVocabulary(t *testing.T) {
	want := []EscalationErrorCode{
		"SANDBOX_ESCALATION_INVALID_ARGUMENT",
		"SANDBOX_ESCALATION_UNAVAILABLE",
		"SANDBOX_ESCALATION_REJECTED",
		"SANDBOX_ESCALATION_CANCELLED",
	}
	if len(EscalationErrorCodes) != len(want) {
		t.Fatalf("codes = %v", EscalationErrorCodes)
	}
	for i, code := range want {
		if EscalationErrorCodes[i] != code {
			t.Fatalf("codes[%d] = %q, want %q", i, EscalationErrorCodes[i], code)
		}
	}
	err := &EscalationError{Code: EscalationRejected, Message: "x"}
	if !errors.Is(err, ErrEscalation) {
		t.Fatal("EscalationError 必须能被 errors.Is(ErrEscalation) 命中")
	}
	var typed *EscalationError
	if !errors.As(err, &typed) || typed.Code != EscalationRejected || typed.Error() != "x" {
		t.Fatalf("errors.As 取不回码: %+v", typed)
	}
}

// 官方 WIDER_MODES（`escalation.ts:64-67`）：严格更宽才能升级；同档不是升级；
// danger-full-access 之上没有更宽档位。
func TestWiderModesLadder(t *testing.T) {
	cases := []struct {
		from, to Mode
		wider    bool
	}{
		{ModeReadOnly, ModeWorkspaceWrite, true},
		{ModeReadOnly, ModeDangerFullAccess, true},
		{ModeWorkspaceWrite, ModeDangerFullAccess, true},
		// 同档不是"更宽"
		{ModeReadOnly, ModeReadOnly, false},
		{ModeWorkspaceWrite, ModeWorkspaceWrite, false},
		{ModeDangerFullAccess, ModeDangerFullAccess, false},
		// 反向（更窄）与 danger 之上的任何目标都不是更宽
		{ModeWorkspaceWrite, ModeReadOnly, false},
		{ModeDangerFullAccess, ModeWorkspaceWrite, false},
		{ModeDangerFullAccess, ModeReadOnly, false},
	}
	for _, c := range cases {
		if got := StrictlyWider(c.from, c.to); got != c.wider {
			t.Fatalf("StrictlyWider(%q,%q) = %v, want %v", c.from, c.to, got, c.wider)
		}
	}
	if got := WiderModes(ModeReadOnly); len(got) != 2 || got[0] != ModeWorkspaceWrite || got[1] != ModeDangerFullAccess {
		t.Fatalf("WiderModes(read-only) = %v", got)
	}
	if got := WiderModes(ModeWorkspaceWrite); len(got) != 1 || got[0] != ModeDangerFullAccess {
		t.Fatalf("WiderModes(workspace-write) = %v", got)
	}
	if got := WiderModes(ModeDangerFullAccess); got != nil {
		t.Fatalf("WiderModes(danger-full-access) = %v, want nil", got)
	}
	// 广告词表是官方 ESCALATION_TARGETS：闸住的是"能升到哪"，不是"默认档比它宽的裁掉"。
	if len(EscalationTargets) != 2 || EscalationTargets[0] != ModeWorkspaceWrite || EscalationTargets[1] != ModeDangerFullAccess {
		t.Fatalf("EscalationTargets = %v", EscalationTargets)
	}
	// WiderModes 返回副本：调用方修改不得污染词表。
	got := WiderModes(ModeReadOnly)
	got[0] = ModeReadOnly
	if WiderModes(ModeReadOnly)[0] != ModeWorkspaceWrite {
		t.Fatal("WiderModes 必须返回副本")
	}
}

// 官方 validateEscalationArgs（`escalation.ts:88-98`）：三句不同的文案，
// 对应"缺理由 / 孤理由 / 空理由"三种畸形请求。
func TestValidateEscalationArgs(t *testing.T) {
	blank := ""
	sentence := " 需要读取工作区外的构建产物  "
	other := "因为要装依赖"

	// 不请求升级、也不给理由：合法。
	if err := ValidateEscalationArgs("", nil); err != nil {
		t.Fatalf("空请求应合法: %v", err)
	}
	// 成对出现且理由非空：合法。
	if err := ValidateEscalationArgs(PermissionDangerFullAccess, &sentence); err != nil {
		t.Fatalf("成对请求应合法: %v", err)
	}
	if err := ValidateEscalationArgs(PermissionWorkspaceWrite, &sentence); err != nil {
		t.Fatalf("成对请求应合法: %v", err)
	}

	cases := []struct {
		name       string
		permission Permission
		justify    *string
		wantMsg    string
	}{
		{"缺理由", PermissionDangerFullAccess, nil, "invalid escalation: sandbox_permissions requires a justification"},
		{"孤理由", "", &other, "invalid escalation: justification is only valid together with sandbox_permissions"},
		{"空理由", PermissionDangerFullAccess, &blank, "invalid justification: expected a non-empty sentence"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateEscalationArgs(c.permission, c.justify)
			var typed *EscalationError
			if !errors.As(err, &typed) {
				t.Fatalf("want EscalationError, got %v", err)
			}
			if typed.Code != EscalationInvalidArgument {
				t.Fatalf("code = %q", typed.Code)
			}
			if typed.Message != c.wantMsg {
				t.Fatalf("message = %q, want %q", typed.Message, c.wantMsg)
			}
		})
	}
}

// 引擎侧的升级错误同样用官方文案（`escalation.ts:216-219`、`:248-251`）。
func TestEscalationEngineMessagesAreOfficial(t *testing.T) {
	if got, want := EscalationNotWiderMessage(PermissionWorkspaceWrite, ModeDangerFullAccess),
		`sandbox escalation to "workspace-write" is not strictly wider than this call's current "danger-full-access" mode`; got != want {
		t.Fatalf("not-wider = %q, want %q", got, want)
	}
	if got, want := EscalationRejectedMessage(PermissionDangerFullAccess, "command"),
		`the user rejected escalating this command to "danger-full-access"; it stays denied, so stop and explain instead of working around it`; got != want {
		t.Fatalf("rejected = %q, want %q", got, want)
	}
}
