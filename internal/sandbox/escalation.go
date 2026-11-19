package sandbox

import (
	"fmt"
	"strings"
)

// 本文件逐字移植官方 `@deepseek-ai/dsh-sandbox/src/escalation.ts`（0.2.2-rc.2）的
// 「升级词表与编排」：严格更宽阶梯、参数配对校验、面向模型的拒绝/提示标记、升级错误码。
// 官方把它放在 `dsh-sandbox` 接缝包里由每个沙箱化工具族共用一份
// （`escalation.ts:1-17` 的模块注释：一个家，两个族的审批顺序与逐字错误文本才不会漂移）。
//
// **这里仍然不是 OS 沙盒**：本文件只回答「这次调用允不允许更宽的档位」。
// 实际隔离层级由 Enforcement 自报（`policy.go:64-70`），未到达的层级不得写 DONE
// （AGENTS.md「沙盒不得冒名」）。官方对应实现是 `dsh-sandbox-local` + `dsh-sandbox-windows-acl`，
// 本仓没有；本文件不含任何 OS 强制代码。

// ErrEscalation 是升级词表的哨兵错误：`errors.Is(err, ErrEscalation)` 命中所有
// 升级错误，调用方按 EscalationError.Code 分派。
var ErrEscalation = fmt.Errorf("sandbox 一次性升级未通过")

// EscalationErrorCode 是官方 `SANDBOX_ESCALATION_ERROR_CODES` 的取值面
// （`escalation.ts:33-38`）。
type EscalationErrorCode string

const (
	// EscalationInvalidArgument：升级字段没有成对出现、justification 全空，
	// 或目标档位不比本次调用的有效档位严格更宽。模型改请求即可。
	EscalationInvalidArgument EscalationErrorCode = "SANDBOX_ESCALATION_INVALID_ARGUMENT"
	// EscalationUnavailable：这个组合无法发起审批（没有沙箱化执行器、
	// 没有审批服务、没有调用方 Agent 或没有审批通道）。
	EscalationUnavailable EscalationErrorCode = "SANDBOX_ESCALATION_UNAVAILABLE"
	// EscalationRejected：用户拒绝了这次升级，操作保持拒绝。
	EscalationRejected EscalationErrorCode = "SANDBOX_ESCALATION_REJECTED"
	// EscalationCancelled：审批在没有结论的情况下结束，而调用本身没有被取消。
	EscalationCancelled EscalationErrorCode = "SANDBOX_ESCALATION_CANCELLED"
)

// EscalationErrorCodes 是官方错误码表的顺序副本（`escalation.ts:33-38`）：
// 消费方与文档按这个顺序枚举，避免各自维护一份。
var EscalationErrorCodes = []EscalationErrorCode{
	EscalationInvalidArgument,
	EscalationUnavailable,
	EscalationRejected,
	EscalationCancelled,
}

// EscalationError 是一次没有产生授权的 `sandbox_permissions` 请求；命令没有执行。
// 文案面向模型，含安全的下一步（官方 `SandboxEscalationError`，`escalation.ts:43-56`）。
type EscalationError struct {
	Code    EscalationErrorCode `json:"code"`
	Message string              `json:"message"`
}

func (e *EscalationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

// Unwrap 让 errors.Is(err, ErrEscalation) 命中，且不吞掉原始错误链。
func (e *EscalationError) Unwrap() error { return ErrEscalation }

// EscalationTargets 是官方 `ESCALATION_TARGETS`（`escalation.ts:77`）：
// 一次调用可能升级到的**全部**档位。read-only 是地板，没有东西能升到它。
// 只要组合会设限就整表广告——把枚举裁到「比默认档更宽」，会让有效档位更窄的会话
// （例如默认 danger-full-access、会话被切到 read-only）拿到空枚举，无处使力。
var EscalationTargets = []Mode{ModeWorkspaceWrite, ModeDangerFullAccess}

// widerModes 是官方 `WIDER_MODES`（`escalation.ts:64-67`）逐条：键是本次调用的
// 有效档位，值是可以升到的档位。danger-full-access 没有更宽的档位，所以不在表里。
var widerModes = map[Mode][]Mode{
	ModeReadOnly:       {ModeWorkspaceWrite, ModeDangerFullAccess},
	ModeWorkspaceWrite: {ModeDangerFullAccess},
}

// WiderModes 返回 from 档位在官方阶梯上能升到的档位（返回副本，调用方可安全修改）。
func WiderModes(from Mode) []Mode {
	src, ok := widerModes[from]
	if !ok {
		return nil
	}
	out := make([]Mode, len(src))
	copy(out, src)
	return out
}

// StrictlyWider 报告 to 是否在 from 的严格更宽阶梯上。
// 同档不算更宽（官方 `approveEscalation` 对同档直接返回，不需要审批）。
func StrictlyWider(from, to Mode) bool {
	for _, m := range widerModes[from] {
		if m == to {
			return true
		}
	}
	return false
}

// ValidateEscalationArgs 校验工具 schema 表达不了的那个配对关系：
// `sandbox_permissions` 与 `justification` 必须同行——没理由的审批提示、
// 或者一个理由没有驱动任何请求，都是畸形请求；且理由必须是非空句子。
// 逐条对应官方 `validateEscalationArgs`（`escalation.ts:88-98`）。
//
// justification 用指针表达「参数缺席」与「给了空串」的区别：官方是三句不同的文案，
// 压成一句会让模型收到错误的下一步。permission 为空串表示没请求升级。
func ValidateEscalationArgs(permission Permission, justification *string) error {
	if permission != "" && justification == nil {
		return &EscalationError{
			Code:    EscalationInvalidArgument,
			Message: "invalid escalation: sandbox_permissions requires a justification",
		}
	}
	if justification != nil && permission == "" {
		return &EscalationError{
			Code:    EscalationInvalidArgument,
			Message: "invalid escalation: justification is only valid together with sandbox_permissions",
		}
	}
	if justification != nil && strings.TrimSpace(*justification) == "" {
		return &EscalationError{
			Code:    EscalationInvalidArgument,
			Message: "invalid justification: expected a non-empty sentence",
		}
	}
	return nil
}

// SandboxDenialMarker 是官方 `sandboxDenialMarker`（`escalation.ts:108-110`）：
// 两个设限工具族共同讲授并上报的**面向模型的拒绝标记**——模型无论被内核拒绝文件效果
// 还是被策略拒绝改动，看到的是同一行。
func SandboxDenialMarker(mode Mode) string {
	return fmt.Sprintf("[sandbox: file access denied under %s mode]", string(mode))
}

// EscalationHintMarker 是官方 `escalationHintMarker`（`escalation.ts:121-123`）：
// 当组合广告了升级字段时，这条提示随拒绝一起出现——提示落在决策点，
// 被允许的重试不依赖模型回忆起工具描述。
func EscalationHintMarker(subject string) string {
	return fmt.Sprintf("[sandbox: escalation available — retry this exact %s once with sandbox_permissions (the narrowest wider mode that suffices) + justification; the approval prompt asks the user]", subject)
}

// SandboxPermissionsDescription 是官方 `sandboxPermissionsDescription`
// （`escalation.ts:132-134`）：`sandbox_permissions` 参数的描述，承载升级规则。
// 工具 schema 直接引用它，避免两个族各写一份。
func SandboxPermissionsDescription(subject string) string {
	return fmt.Sprintf("The narrowest wider sandbox mode for a one-shot retry of the exact %s the sandbox just denied; the retry asks the user for approval.", subject)
}

// JustificationDescription 是官方 `justification` 参数的描述
// （`dsh-tool-pwsh/src/index.ts:437-440`，两个族逐字相同）。
// 末尾的撇号是官方的 U+2019，保持逐字。
func JustificationDescription() string {
	return "Required with sandbox_permissions: one sentence for the user explaining why this exact command needs the wider access. " +
		"Use the language of the user’s current request."
}

// EscalationNotWiderMessage 是官方「目标档位不够宽」的执行期错误文案
// （`escalation.ts:216-219`）。
func EscalationNotWiderMessage(requested Permission, effective Mode) string {
	return fmt.Sprintf("sandbox escalation to %q is not strictly wider than this call's current %q mode", string(requested), string(effective))
}

// EscalationRejectedMessage 是官方「用户拒绝升级」的文案（`escalation.ts:248-251`）。
func EscalationRejectedMessage(requested Permission, subject string) string {
	return fmt.Sprintf("the user rejected escalating this %s to %q; it stays denied, so stop and explain instead of working around it", subject, string(requested))
}
