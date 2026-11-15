package approval

// 审批三态的**错误身份**（官方 0.2.2-rc.2）。
//
// 官方原件（`dsh-tools`）：
//   - `lib/pipeline.js:612-660` 的 `serviceAsk`：四种审批结果各自映射成一个
//     `approvalDenial(reason, code)`；
//   - `lib/pipeline.js:841-856`：`approvalDenial` 造 `info: {name:"ApprovalError", code}`；
//   - `lib/types/error-codes.js:6-16`：三个码的原词。
//
// 为什么码必须分三态：模型对三种情况的下一步动作**完全不同**——
//   - `APPROVAL_REJECTED`：人看了、说了不。要重新征求同意，或换成只读方式；
//   - `APPROVAL_CANCELLED`：这次批准被取消（不是人拒绝你）。可以重新发起；
//   - `APPROVAL_UNAVAILABLE`：根本没有审批通道（无人值守/策略 deny/无人回答）。
//     换问法没用，必须让用户先改变环境。
// 只给一句 "Error: 用户拒绝了" 时，模型只能从中文措辞里猜，猜错的代价是
// 反复重试一个永远不可能通过的调用。

import (
	"errors"
	"testing"

	"dshgo/internal/spi"
)

// 三个码与错误类名必须逐字等于官方原词。
func TestDenialIdentitiesMatchOfficial(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    *Denial
		code string
	}{
		{"用户拒绝", Rejected("人说不"), "APPROVAL_REJECTED"},
		{"审批被取消", Cancelled("没人回答之前撤了"), "APPROVAL_CANCELLED"},
		{"没有审批通道", Unavailable("无人值守"), "APPROVAL_UNAVAILABLE"},
	} {
		info := tc.d.Info()
		if info.Name != "ApprovalError" {
			t.Fatalf("%s 的错误类名 = %q，官方是 ApprovalError", tc.name, info.Name)
		}
		if info.Code != tc.code {
			t.Fatalf("%s 的码 = %q，官方是 %q", tc.name, info.Code, tc.code)
		}
		// 与 spi 里的码表必须是同一个值：两份字面量各自漂移时，落盘的 code
		// 与宿主内部分支会开始打架，而且两边都"看着对"。
		if info.Code != string(tc.d.Code) {
			t.Fatalf("%s 的 Info().Code = %q 与 Denial.Code = %q 不一致", tc.name, info.Code, tc.d.Code)
		}
	}
	if string(CodeRejected) != spi.CodeApprovalRejected ||
		string(CodeCancelled) != spi.CodeApprovalCancelled ||
		string(CodeUnavailable) != spi.CodeApprovalUnavailable {
		t.Fatal("approval 的三个码与 spi 的码表漂移了")
	}
}

// Denial 必须同时是 error（Runner 的返回值）与结果身份（Result.Info）的来源。
func TestDenialIsBothErrorAndResultIdentity(t *testing.T) {
	d := Rejected("用户拒绝了 bash")
	if d.Error() != "用户拒绝了 bash" {
		t.Fatalf("Error() = %q，应当是理由原文", d.Error())
	}

	res, err := d.Failed(spi.Result{
		Summary: "没跑",
		Text:    "拒绝正文",
		Hint:    "换成只读方式确认，或先说明你要做什么再请求",
	})
	if err == nil {
		t.Fatal("拒绝必须同时返回 error：调用方靠 errors.Is/As 认它")
	}
	var back *Denial
	if !errors.As(err, &back) || back.Code != Rejected("").Code {
		t.Fatalf("errors.As 认不回 Denial：%v", err)
	}
	// 四字段错误契约与 Result{Hint} 语义都不受这条改动影响：
	// 这里只多一个 Info，不新增/改动任何既有字段。
	if res.Hint == "" {
		t.Fatal("失败结果必须保留 Hint（R3：失败要给下一步）")
	}
	if res.Summary != "没跑" || res.Text != "拒绝正文" {
		t.Fatalf("补身份改动了工具的既有正文：%+v", res)
	}
	if res.Info == nil || res.Info.Name != spi.ErrorNameApproval || res.Info.Code != spi.CodeApprovalRejected {
		t.Fatalf("Result.Info = %+v，期望 ApprovalError/APPROVAL_REJECTED", res.Info)
	}
}
