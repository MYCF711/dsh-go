package agent

// 官方 `DSH_MAX_TOKENS_AS_SUCCESS` 的解析与结算闸门（GO.5 / T1）。
//
// 官方原件：`@dsa/dsh-sdk-app/cordis.patch.yml:21`
//
//	DSH_MAX_TOKENS_AS_SUCCESS: true   # 把 max-tokens 结束当成功，除非显式 false
//
// 语义要点（照抄官方，不改写）：
//   - **默认 true**：finish=length 是"这一轮跑完了（只是被截断）"，不是失败；
//   - 只有**显式** false 才把它当失败；
//   - 官方该值按 JSON 解析（`"false"` / `"0"` / `"no"` 这类写法的语义由本函数
//     显式钉住：这三种是 false，其余一律 true——"除非显式 false"这句话本来
//     就是这个意思，猜不出来的一律走默认）。
//
// 为什么由 agent 包读：finish 是老模型循环的产物，结算点也在这里；放到 cmd/dshd
// 会让子代理、side-query、schedule 四个入口各写一遍，早晚漂移。

import (
	"fmt"
	"os"
	"strings"

	"dshgo/internal/llm"
)

// EnvMaxTokensAsSuccess 是官方变量名（逐字）。
const EnvMaxTokensAsSuccess = "DSH_MAX_TOKENS_AS_SUCCESS"

// MaxTokensAsSuccess 返回"max-tokens 结束是否算成功"。
//
// 只认三种 false 写法（大小写不敏感、去首尾空白）：`false` / `0` / `no`；
// 未设置、空串或其它值一律 true（官方默认）。
func MaxTokensAsSuccess(getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(getenv(EnvMaxTokensAsSuccess))) {
	case "false", "0", "no":
		return false
	default:
		return true
	}
}

// maxTokensAsSuccessErr 是 finish=length 的结算闸门。
//
// 默认（env 未设 / true）返回 nil：这一轮照常收口，turn/end 记 `max-tokens`
// （v4 词汇表里"按预期结束"的一种，客户端不会当完整回答）。
// env=false 时返回错误：调用方的出口会写 `turn/end{kind:"error"}` 并让进程
// 非零退出——"被截断的半句"不再伪装成一次成功的回合。
//
// 错误原文必须点名变量：用户看到的是"为什么这一轮失败了"，不是"运行时心情"。
func maxTokensAsSuccessErr(resp *llm.Response, getenv func(string) string) error {
	if resp == nil || resp.FinishReason != "length" {
		return nil
	}
	if MaxTokensAsSuccess(getenv) {
		return nil
	}
	return fmt.Errorf("模型输出被 max_tokens 截断（finish=length），"+
		"且 %s=false 明确要求把它当失败：把 max_tokens 上限调大后重试", EnvMaxTokensAsSuccess)
}
