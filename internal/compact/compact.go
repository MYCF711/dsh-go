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
	// KeepRecent 是**至少**保留的最近消息数。
	//
	// 官方没有条数下限——它只按 token 预算切（`selectCompactableRange`，
	// `dsh-compaction-basic/lib/index.js:410-432`），保留 0 条时也合法。宿主保留
	// 一个下限纯粹是安全垫：预算算不出来时（窗口未知）不能把模型眼前清空。
	// 默认值见 withDefaults。
	KeepRecent int
	// KeepTokens 是保留尾巴的 token 预算。
	//
	// **这是官方 `retainTokens` 的宿主等价物**，官方口径是
	// `floor((W − O) × 0.16)`（`lib/index.js:17`、`:133`），由调用方用
	// `PressureBudget.RetainTokens()` 算出来传进来（见 `cmd/dshd` 的自动压缩）。
	//
	// `<= 0` 表示**没有 token 预算，只按条数留**——这正是官方手动压缩的形状：
	// `compactNow` 给 `selectCompactableRange(..., 0)`（`lib/index.js:1009`）。
	// 宿主以前在这里有一条自造的兜底 `= 2000`（官方没有任何绝对 token 默认值），
	// 先把它删掉了：算不出预算就退回"只按条数"，不编一个数。
	KeepTokens int
	// MinToCompact 是触发压缩的最小条数。太少不值得压。
	//
	// 这是宿主的性价比判断（官方没有这个判据：它只看 token 量），保持原值。
	MinToCompact int
	// MaxMessages 是表面超过多少条才触发（0 = 不看条数，只按调用方要求）。
	MaxMessages int
}

func (o Options) withDefaults() Options {
	if o.KeepRecent <= 0 {
		o.KeepRecent = 2
	}
	// KeepTokens 不再补一个自造的绝对值：`<= 0` 就是"只按条数留"。
	if o.MinToCompact <= 0 {
		o.MinToCompact = 2
	}
	return o
}

// keepFrom 返回尾巴的起始下标：从末尾往前累计，直到超出 token 预算。
//
// 两条硬约束：
//  1. 至少留 KeepRecent 条（不然模型眼前一片空白）；
//  2. 再多留就是浪费预算——一条 2,300 token 的工具输出不该因为"条数还够"就整条留下。
func keepFrom(surface []session.SurfaceMsg, o Options) int {
	acc := 0
	for i := len(surface) - 1; i >= 0; i-- {
		acc += session.EstimateRunes(surface[i].Message.Content)
		kept := len(surface) - i
		if acc > o.KeepTokens && kept > o.KeepRecent {
			// i 这一条装不进预算了：从它之后切，保留 surface[i+1:].
			return i + 1
		}
	}
	return 0
}

// Plan 选出一个可压缩的前缀区间（保留给只关心 seq 的调用方）。
func Plan(surface []session.SurfaceMsg, opts Options) (session.Span, int, bool) {
	items, span, ok := Select(surface, opts)
	return span, len(items), ok
}

// Select 选出要压缩的节点本身，并给出它们的 seq 区间。
//
// 为什么还要返回 items：**surface 的顺序不是 seq 顺序**——压缩替身会被插到
// 它遮蔽的位置，但它的 seq 是最后追加的。按 seq 区间回头去筛节点会筛空
// （踩过：日志里出现「没有可摘要的消息」）。
//
// 两条硬约束：
//  1. 只动前缀——最近的 `KeepRecent` 条一定留；
//  2. 开头的 system/message 不压：DSH 只允许被「恰好覆盖该节点的 system/message」
//     替换，压掉它等于让模型忘记身份。压缩替身同样不主动压（没有新信息可提炼）。
func Select(surface []session.SurfaceMsg, opts Options) ([]session.SurfaceMsg, session.Span, bool) {
	o := opts.withDefaults()
	if o.MaxMessages > 0 && len(surface) <= o.MaxMessages {
		return nil, session.Span{}, false
	}

	start := 0
	for start < len(surface) && (surface[start].Type == "system/message" || surface[start].Summary) {
		start++
	}
	end := keepFrom(surface, o) // 开区间右端：按 token 预算推出
	if end <= start {
		return nil, session.Span{}, false
	}
	cut := surface[start:end]
	if len(cut) < o.MinToCompact {
		return nil, session.Span{}, false
	}

	minSeq, maxSeq := cut[0].Seq, cut[0].Seq
	for _, m := range cut {
		if m.Seq < 0 {
			continue
		}
		if minSeq < 0 || m.Seq < minSeq {
			minSeq = m.Seq
		}
		if m.Seq > maxSeq {
			maxSeq = m.Seq
		}
	}
	return cut, session.Span{Start: minSeq, End: maxSeq}, true
}

// 摘要输入**不设**字符上限。
//
// 宿主以前在这里有两个自造的数：`maxPromptChars = 24000` 和
// `maxMessageChars = 1200`——官方没有任何对应值。官方摘要调用的输入是
// "把被压缩的那段对话原样重放一遍"（`summarizeWithLlm`：
// `dsh-compaction-basic/lib/index.js:292-340`，replay the conversation prefix），
// 逐条不截断、总量也不截断：前缀与上一次真实请求共享，靠的正是 provider 的
// KV cache。我们按 1200 字砍单条，等于**一条工具输出只有开头进得了摘要**，
// 后面全丢——压完的摘要自然是残的。这两条上限已删除。

// Chat 是摘要生成依赖的模型入口。
type Chat interface {
	Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error)
}

// SummarizeMaxTokensDefault 是官方摘要调用的**输出上限**出厂值。
//
// 官方 `@deepseek-ai/dsh-compaction-basic` 的 `maxTokens` 是"摘要调用能输出多少
// token"，默认取 `config.maxTokens ?? headroomTokens`：
//
//	`tmp/GO3-PARAMS/_official/dsh-compaction-basic/lib/index.js:66-67`
//	  const headroomTokens = config.headroomTokens ?? 65536;
//	  const maxTokens = config.maxTokens ?? headroomTokens;
//	同文件 :961-965 把它放进 `spec.maxTokens`，摘要调用用这一档。
//
// 宿主以前写死 2000（比官方小 32 倍）：长会话的摘要会被截断在 2000 token，
// 而官方允许 65536。这里只对齐**上限**，模型实际输出多长不受影响。
const SummarizeMaxTokensDefault = 65536

// Summarize 把一段 surface 消息压成文字摘要（官方默认输出上限）。
func Summarize(ctx context.Context, chat Chat, model string, items []session.SurfaceMsg) (string, error) {
	return SummarizeWithMaxTokens(ctx, chat, model, items, 0)
}

// SummarizeWithMaxTokens 同 Summarize，但显式给摘要调用的输出上限。
//
// maxTokens <= 0 表示用官方默认（SummarizeMaxTokensDefault）。生产调用方必须传
// 解析后的压缩策略（`Policy.SummarizeMaxTokens()`），否则 `modelPolicies[].maxTokens`
// 这条官方配置在摘要调用上等于没接线。
func SummarizeWithMaxTokens(ctx context.Context, chat Chat, model string, items []session.SurfaceMsg, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = SummarizeMaxTokensDefault
	}
	if len(items) == 0 {
		return "", fmt.Errorf("没有可摘要的消息")
	}
	var b strings.Builder
	b.WriteString("把下面这段对话压成一份**结构化摘要**，供后续继续工作时参考。\n\n")
	b.WriteString("要求：\n")
	b.WriteString("1. 保留：用户目标、已确认的决定与原因、尚未完成的事、关键文件路径与命令。\n")
	b.WriteString("2. 丢掉：寒暄、重复解释、已被推翻的方案。\n")
	b.WriteString("3. 不要编造原文里没有的内容；不确定就写「未提及」。\n")
	b.WriteString("4. 用中文，分小节。不设字数上限：把该留的留全，长度只受摘要调用的输出 token 上限约束。\n\n")
	b.WriteString("--- 对话开始 ---\n")
	for _, m := range items {
		role := m.Message.Role
		if m.Summary {
			role = "earlier-summary"
		}
		text := strings.TrimSpace(m.Message.Content)
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
		MaxTokens: maxTokens,
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
