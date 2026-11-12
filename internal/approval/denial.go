package approval

// 审批未通过时的**错误身份**（官方 0.2.2-rc.2 新增的有形状的错误信封）。
//
// 官方原件（@deepseek-ai/dsh-tools）：
//
//	// lib/pipeline.js:612-660 —— serviceAsk 把四种审批结果映射成四种结局
//	case "allowed-once": return { decision: { kind: "allow" }, ... }
//	case "rejected":     return { decision: approvalDenial(`the user rejected tool "…"`, APPROVAL_REJECTED) }
//	case "cancelled":    return { decision: approvalDenial(…, APPROVAL_CANCELLED), approvalCancelled: true }
//	case "unavailable":  return { decision: approvalDenial(…, APPROVAL_UNAVAILABLE) }
//
//	// lib/pipeline.js:841-856 —— 拒绝不再是裸字符串，而是带可路由身份的结构
//	function approvalDenial(reason, code) {
//	  return { kind: "deny", reason, info: { name: APPROVAL_ERROR_NAME, code } }
//	}
//
//	// lib/types/error-codes.js:6-16 —— 三个码与类名的原词
//
// 本仓的审批判定发生在 `cmd/dshd`（guardedRunner 的 ask）与界面应答之间，
// 这里只提供**唯一的身份构造点**：谁判定、谁落结果，但码只有一处定义。
// 接线行见 `tmp/GO8-RC2/reports/D3-tool-pipeline.md` §接线。

import (
	"dshgo/internal/spi"
)

// Code 是审批未通过的身份码。
//
// 它与会落盘的 `tool/result.error.code` 是同一个值，因此只在 spi 里定义一次，
// 这里转成有名字的类型——不要让两个包各写一份字面量，那迟早会漂。
type Code string

const (
	// CodeRejected：人看过并拒绝了这次调用。模型应重新征求同意，或换只读方式。
	CodeRejected Code = Code(spi.CodeApprovalRejected)
	// CodeCancelled：审批请求被取消（**不是**有人拒绝）。可以重新发起。
	CodeCancelled Code = Code(spi.CodeApprovalCancelled)
	// CodeUnavailable：需要审批但没有任何通道能回答（没有审批服务/没有会话/
	// 无人值守策略）。换问法没用，必须让用户先改变环境。
	CodeUnavailable Code = Code(spi.CodeApprovalUnavailable)
)

// Denial 是一次"审批未通过"：给人看的理由 + 给代码/模型看的身份。
//
// 它同时扮演两个角色（这是刻意的，不是图省事）：
//   - error：Runner 的第二个返回值，调用方用 errors.As 认它；
//   - 结果身份来源：落进 `spi.Result.Info`，投影成 `tool/result.error{name,code}`。
//
// 两个角色必须同源：如果 error 说的是"用户拒绝"、Result.Info 说的是
// "没有通道"，模型会按其中任意一个行动，而两者不可能同时被满足。
type Denial struct {
	Code   Code
	Reason string
}

// Error 让 Denial 可以当 error 用（理由原文，不要再拼一遍前缀）。
func (d *Denial) Error() string {
	if d == nil {
		return ""
	}
	if d.Reason != "" {
		return d.Reason
	}
	return string(d.Code)
}

// Info 是官方 `error.info{name,code}` 的形状：类名固定 ApprovalError。
func (d *Denial) Info() spi.ErrorInfo {
	name := spi.ErrorNameApproval
	code := ""
	if d != nil {
		code = string(d.Code)
	}
	return spi.ErrorInfo{Name: name, Code: code}
}

// Failed 返回 Runner 的"失败结果 + 错误"这一对：Hint/Text/Data 等原样保留，
// 只补 `Info`。
//
// 为什么不是"新建一个只有 Hint 的结果"：工具失败时的正文就是模型判断下一步的
// 现场（例如 bash 已经打印出来的那半截输出），丢掉它等于让模型只看得到"被拒了"。
func (d *Denial) Failed(res spi.Result) (spi.Result, error) {
	if d == nil {
		return res, nil
	}
	return res.WithErrorInfo(spi.ErrorNameApproval, string(d.Code)), d
}

// Rejected 造"人拒绝了"（APPROVAL_REJECTED）。
func Rejected(reason string) *Denial { return &Denial{Code: CodeRejected, Reason: reason} }

// Cancelled 造"审批被取消"（APPROVAL_CANCELLED）。
func Cancelled(reason string) *Denial { return &Denial{Code: CodeCancelled, Reason: reason} }

// Unavailable 造"没有审批通道"（APPROVAL_UNAVAILABLE）。
func Unavailable(reason string) *Denial { return &Denial{Code: CodeUnavailable, Reason: reason} }
