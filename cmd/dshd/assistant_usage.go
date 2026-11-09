package main

import "dshgo/internal/llm"

// assistantUsageFrom 把一次调用的 `llm.Usage` 投影成 v4 `assistant/message` 的 usage 对象。
//
// # 键名照官方 v4，不另起一套
//
// 官方真实会话里 assistant usage 就长这样（本任务对照证据）：
//
//	{inputTokens:7930, outputTokens:72, totalTokens:29762, cacheReadTokens:21760}
//
// 48838 条样本里 48636 条带 `cacheReadTokens`。TUI 状态栏的 `缓存 N%` 读的就是
// 这个键（`cacheRead/(input+cacheRead+cacheWrite)`），上下文占用读的是
// `input+cacheRead+cacheWrite`；宿主以前只写前三个键，于是缓存永远是 0.0%。
//
// # 两条硬规矩
//
//  1. **inputTokens 是未命中缓存的那部分**。OpenAI 兼容端点（DeepSeek 原生、magpie
//     网关）的 `prompt_tokens` 已经含缓存命中，官方那套口径是 `input = prompt - 命中 - 写入`：
//     上例 7930 + 21760 = 29690 = prompt_tokens，totalTokens 29762 = 29690 + 72。
//     原样把 prompt_tokens 写进 inputTokens 再补上 cache 计数，等于把命中算两遍——
//     缓存命中率被低估、上下文占用被抬高。Anthropic 形状（`cache_read_input_tokens`）
//     的 `prompt_tokens` 本来只有未命中部分，不能再减，见 llm.UsagePromptTokensIncludeCache。
//  2. **provider 没给的缓存计数不写键**，也不写 0 冒充实测：界面的
//     `cacheKnown` 靠"键在不在"区分「不知道」与「实测 0」。
//
// nil（这一步没有服务端用量）返回 nil：`session.AssistantMessageEvent` 据此不写 usage 键，
// 与"写了三个 0"是两种不同的事实。
func assistantUsageFrom(usage *llm.Usage) map[string]any {
	if usage == nil {
		return nil
	}
	cacheRead, cacheWrite, okRead, okWrite := llm.UsageCacheTokens(*usage)
	input := usage.PromptTokens
	if llm.UsagePromptTokensIncludeCache(*usage) {
		// 命中/写入都算在 prompt_tokens 里：减掉才是官方口径的 input。
		if okRead {
			input -= int(cacheRead)
		}
		if okWrite {
			input -= int(cacheWrite)
		}
	}
	if input < 0 {
		// provider 自相矛盾（命中比 prompt 还大）时不编负数：0 是能说出口的最小真值。
		input = 0
	}
	out := map[string]any{
		"inputTokens":  input,
		"outputTokens": usage.CompletionTokens,
		"totalTokens":  usage.TotalTokens,
	}
	if okRead {
		out["cacheReadTokens"] = int(cacheRead)
	}
	if okWrite {
		out["cacheWriteTokens"] = int(cacheWrite)
	}
	return out
}
