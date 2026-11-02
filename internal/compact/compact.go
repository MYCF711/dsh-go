// Package compact 决定"压哪一段"并生成摘要。
//
// 它与 session 分工：session 负责**写事件**（append-only + 精确遮蔽），
// 这里只负责策略——所以可以单独测，不需要碰文件。
package compact

import (
	"context"
	"fmt"
	"strings"

	"dshgo/internal/llm"
	"dshgo/internal/session"
)

// Options 是压缩策略参数。
type Options struct {
	// KeepRecent 是**必须保留**的最近消息数（默认 6）。压缩永远只动前缀。
	KeepRecent int
	// MinToCompact 是触发压缩的最小条数（默认 2）。太少不值得压。
	MinToCompact int
	// MaxMessages 是表面超过多少条才触发（0 = 不看条数，只按调用方要求）。
	MaxMessages int
}

func (o Options) withDefaults() Options {
	if o.KeepRecent <= 0 {
		o.KeepRecent = 6
	}
	if o.MinToCompact <= 0 {
		o.MinToCompact = 2
	}
	return o
}

// Plan 选出一个可压缩的前缀区间。
//
// 两条硬约束：
//  1. 只动前缀——最近的 `KeepRecent` 条一定留；
//  2. 开头若是 system/message（surface 节点 0），**不压它**：DSH 只允许被
//     「恰好覆盖该节点的 system/message」替换，压掉它等于让模型忘记身份。
func Plan(surface []session.SurfaceMsg, opts Options) (session.Span, int, bool) {
	o := opts.withDefaults()
	if o.MaxMessages > 0 && len(surface) <= o.MaxMessages {
		return session.Span{}, 0, false
	}

	start := 0
	for start < len(surface) && surface[start].Type == "system/message" {
		start++
	}
	end := len(surface) - o.KeepRecent // 开区间右端（不含）
	if end <= start {
		return session.Span{}, 0, false
	}
	cut := surface[start:end]
	if len(cut) < o.MinToCompact {
		return session.Span{}, 0, false
	}
	return session.Span{Start: cut[0].Seq, End: cut[len(cut)-1].Seq}, len(cut), true
}

// 输入预算：太大既贵又容易被截断，太小压不住长会话。
const (
	maxPromptChars  = 24000
	maxMessageChars = 1200
)

// Chat 是摘要生成依赖的模型入口。
type Chat interface {
	Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error)
}

// Summarize 把一段 surface 消息压成文字摘要。
func Summarize(ctx context.Context, chat Chat, model string, items []session.SurfaceMsg) (string, error) {
	if len(items) == 0 {
		return "", fmt.Errorf("没有可摘要的消息")
	}
	var b strings.Builder
	b.WriteString("把下面这段对话压成一份**结构化摘要**，供后续继续工作时参考。\n\n")
	b.WriteString("要求：\n")
	b.WriteString("1. 保留：用户目标、已确认的决定与原因、尚未完成的事、关键文件路径与命令。\n")
	b.WriteString("2. 丢掉：寒暄、重复解释、已被推翻的方案。\n")
	b.WriteString("3. 不要编造原文里没有的内容；不确定就写「未提及」。\n")
	b.WriteString("4. 用中文，分小节，控制在 400 字以内。\n\n")
	b.WriteString("--- 对话开始 ---\n")
	for i, m := range items {
		role := m.Message.Role
		if m.Summary {
			role = "earlier-summary"
		}
		if b.Len() > maxPromptChars {
			fmt.Fprintf(&b, "（后面还有 %d 条因篇幅省略）\n", len(items)-i)
			break
		}
		text := strings.TrimSpace(m.Message.Content)
		if len([]rune(text)) > maxMessageChars {
			text = string([]rune(text)[:maxMessageChars]) + "…（本条已截断）"
		}
		if text == "" && len(m.Message.ToolCalls) > 0 {
			names := make([]string, 0, len(m.Message.ToolCalls))
			for _, tc := range m.Message.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			text = "（调用工具：" + strings.Join(names, ", ") + "）"
		}
		fmt.Fprintf(&b, "[%s] %s\n", role, text)
	}
	b.WriteString("--- 对话结束 ---\n")

	resp, err := chat.Stream(ctx, llm.Request{
		Model: model,
		Messages: []llm.Message{
			{Role: "system", Content: "你是会话压缩器。只输出摘要正文，不要客套。"},
			{Role: "user", Content: b.String()},
		},
		MaxTokens: 2000,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("生成摘要失败: %w", err)
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return "", fmt.Errorf("模型返回了空摘要（finish=%q，输出 %d token；推理型模型可能把预算花在了思维链上）",
			resp.FinishReason, resp.Usage.CompletionTokens)
	}
	return out, nil
}
