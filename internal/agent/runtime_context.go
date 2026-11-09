package agent

// 官方 `context.runtimeContext.perStep`（默认 true）：
//
//	"the loop re-projects runtime context facts every attempt without
//	 reassembling the prompt" —— @deepseek-ai/dsh-agent-loop/README.md:120
//
// 为什么必须有：系统提示里的"今天几号 / 在哪个目录 / 什么平台"是**装配那一刻**
// 的事实。长会话跑上几小时后，模型看到的日期还是开工那天——它会照着过期事实
// 推理（"今天是周五"），而用户看不出这是提示词陈旧。
//
// # 为什么是"另起一段 system 消息"而不是改写系统提示
//
// 官方明确"without reassembling the bound prompt or tools"：重新装配整条提示
// 会让每一轮的提示前缀都变（prompt cache 全失效），而且系统提示是**已落盘**的
// 事实（v4 `system/message`），每步改写等于篡改历史。所以这里把运行时事实作为
// 一条**请求期** system 消息追加在末尾：模型每步都能看到最新值，历史一个字节
// 不动。
//
// # 三条硬约束
//
//  1. **不落盘**：注入只作用于这一次请求的 `Messages` 副本，`Run` 返回的历史里
//     没有它（否则下一轮会带上一份过期事实，等于又回到老问题）；
//  2. **默认形状不变**：没配 provider 时不追加任何消息，报文逐字节不变；
//  3. **空事实不占位**：provider 返回空串（或全空白）= 这一段不出现，不留空壳。

import (
	"strings"

	"dshgo/internal/llm"
)

// RuntimeContextPrefix 是运行时事实段的稳定标记。
//
// 它同时是"这一段是派生的、不是用户输入"的机读凭据：日志/回放里看到这个
// 前缀就知道该段可以随时被下一次投影替换。
const RuntimeContextPrefix = "[runtime-context]"

// runtimeContextMessage 把事实包成一条 system 消息；空事实返回 ok=false。
func runtimeContextMessage(facts string) (llm.Message, bool) {
	facts = strings.TrimSpace(facts)
	if facts == "" {
		return llm.Message{}, false
	}
	return llm.Message{Role: "system", Content: RuntimeContextPrefix + "\n" + facts}, true
}

// withRuntimeContext 返回"这一次请求"的消息副本：历史原样 + 一条最新的运行时
// 事实，位置在**领头 system 消息之后**。历史本身（msgs）不会被修改，调用方后续
// 写回的历史也不会带上它。
//
// 为什么不是追加到末尾（踩过，2026-11-09 两条真机/真实链用例同时红）：
//   - 末尾那条消息的语义是「这一轮真正的输入」（user 消息或 tool 结果）。宿主
//     自己的 run 测试与真实链（context_usage_real_test.go）都按
//     `messages[len-1].content` 取 prompt；把 system 段放末尾会让它们把运行时
//     事实当成用户最后一句话，模型也拿不到"现在的输入是什么"。
//   - provider 侧同样按最后一条 user/tool 消息决定"要不要继续"。
//
// 放领头 system 之后而不是插进 system 里：系统提示是**已落盘**的事实
// （v4 `system/message`），不能每步改写；而这条注入只活这一次请求。
func withRuntimeContext(msgs []llm.Message, facts string) []llm.Message {
	message, ok := runtimeContextMessage(facts)
	if !ok {
		return msgs
	}
	at := 0
	for at < len(msgs) && msgs[at].Role == "system" {
		at++
	}
	out := make([]llm.Message, 0, len(msgs)+1)
	out = append(out, msgs[:at]...)
	out = append(out, message)
	out = append(out, msgs[at:]...)
	return out
}
