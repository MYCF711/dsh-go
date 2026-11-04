// Package agent 是 turn/step 双循环：一次用户输入 = 一个 turn，turn 里每一步 = 一次模型调用。
//
// 它只做编排，不实现任何东西：模型来自 Chat，工具来自 spi.Runner，
// 结果如何变成消息来自 Project。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

// Chat 是模型入口（消费方定义的窄接口，internal/llm 天然满足）。
type Chat interface {
	Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error)
}

// EventKind 是 agent 层的事件种类。写会话日志时按这些种类落盘。
type EventKind string

const (
	EventStepStart    EventKind = "step/start"
	EventReasoning    EventKind = "assistant/reasoning"
	EventText         EventKind = "assistant/text"
	EventAssistantMsg EventKind = "assistant/message"
	EventToolCall     EventKind = "tool/call"
	EventToolResult   EventKind = "tool/result"
	EventStepEnd      EventKind = "step/end"
	EventTurnEnd      EventKind = "turn/end"
	EventUsage        EventKind = "usage/update"
)

// Event 是循环里发生的一件事。
type Event struct {
	Kind       EventKind
	Turn, Step int
	Text       string // reasoning / text 增量
	CallID     string
	ToolName   string
	Arguments  string
	Result     *spi.Result
	IsError    bool
	Finish     string
	Err        error

	// 以下只在 EventAssistantMsg 上出现：一步的完整产物（写会话用）。
	Assistant *AssistantTurn

	// 以下只在 EventUsage 上出现：累计用量与窗口大小。
	UsageUsed int
	UsageSize int
}

// AssistantTurn 是一步模型的完整输出。
type AssistantTurn struct {
	Content   string
	Reasoning string
	ToolCalls []llm.ToolCall
	Finish    string
	Usage     *llm.Usage
}

// Loop 是一次会话的驱动器。
type Loop struct {
	Chat   Chat
	Tools  spi.ToolRegistry
	Runner spi.Runner
	// Project 把工具结果压成模型可见的文本块（默认用 session.ProjectResult 的同类语义）。
	Project func(spi.Result) []spi.Block
	// Model 与系统提示由调用方给；Loop 不猜。
	Model string
	// Reasoning 是推理强度（网关的 reasoning_effort）。空 = 不带这个参数。
	// 不是所有模型都认它，所以只能由调用方显式开。
	Reasoning string
	System    string
	// CompactTools 为真时，工具表压掉参数里的 description。
	// 实测能省四成字节——但参数说明也帮模型填对参数，所以默认关，
	// 用 --tool-schema=compact 显式打开，并靠 A/B 数据决定要不要改默认。
	CompactTools bool
	// SessionID 绑到每一步的 ctx 上，让工具能回答"我在为哪个会话干活"。
	// 空字符串不算错——单会话场景（`dshd run`）本来就是匿名的。
	SessionID string
	MaxSteps  int
	// Emit 观测量；返回错误即中断整个 turn。
	Emit func(Event) error
	// lastImages 暂存"上一步工具产出的图片"，由 runTool2 写、调用方读走即清。
	// 放在结构体上是因为 Run 会并发处理多个 tool call，但它们是顺序执行的。
	lastImages []llm.MessageImage
	// Compact 可选：每步开始前调用，返回可能被瘦身过的历史。
	//
	// 为什么放在 Loop 里：压缩是**策略**（选区间 + 生成摘要），
	// 但"什么时候压"必须由跑 turn 的人决定——它才知道预算和会话文件在哪。
	Compact func(ctx context.Context, step int, history []llm.Message) ([]llm.Message, error)
}

func (l *Loop) emit(ev Event) error {
	if l.Emit == nil {
		return nil
	}
	return l.Emit(ev)
}

func (l *Loop) maxSteps() int {
	if l.MaxSteps > 0 {
		return l.MaxSteps
	}
	return 32
}

// Run 跑一个 turn：直到模型不再请求工具，或步数用尽。
//
// messages 会被追加（assistant 消息与 tool 结果都进历史），返回值是新历史。
func (l *Loop) Run(ctx context.Context, turn int, history []llm.Message) ([]llm.Message, error) {
	// 工具靠这个知道"我是哪个会话"；不设的话读信箱一类的工具只能拒绝。
	if l.SessionID != "" {
		ctx = spi.WithSession(ctx, l.SessionID)
	}
	if l.Chat == nil {
		return history, fmt.Errorf("agent: 没有配置 Chat")
	}
	msgs := append([]llm.Message(nil), history...)
	if l.System != "" && (len(msgs) == 0 || msgs[0].Role != "system") {
		msgs = append([]llm.Message{{Role: "system", Content: l.System}}, msgs...)
	}

	for step := 1; step <= l.maxSteps(); step++ {
		if l.Compact != nil {
			next, err := l.Compact(ctx, step, msgs)
			if err != nil {
				return msgs, err
			}
			if next != nil {
				msgs = next
			}
		}
		if err := ctx.Err(); err != nil {
			return msgs, err
		}
		if err := l.emit(Event{Kind: EventStepStart, Turn: turn, Step: step}); err != nil {
			return msgs, err
		}

		req := llm.Request{
			Model:    l.Model,
			Messages: msgs,
			Tools:    l.toolDefs(),
			// 只有显式要了才带：默认不带，避免把不支持该参数的网关打挂。
			Reasoning: l.Reasoning,
		}
		resp, err := l.Chat.Stream(ctx, req, func(e llm.Event) error {
			switch e.Kind {
			case llm.EventReasoning:
				return l.emit(Event{Kind: EventReasoning, Turn: turn, Step: step, Text: e.Text})
			case llm.EventContent:
				return l.emit(Event{Kind: EventText, Turn: turn, Step: step, Text: e.Text})
			}
			return nil
		})
		if err != nil {
			if resp != nil {
				msgs = append(msgs, assistantMessage(resp))
			}
			return msgs, fmt.Errorf("第 %d 步模型调用失败: %w", step, err)
		}

		// 空响应不是"模型没话说"，是**这一次调用根本没成**。
		//
		// 真机踩过：网关列着一个模型 id，但那个上游是坏的（502 / 直接空流），
		// 而某些网关在这种情况下回的是 **200 + 空 SSE**。于是这一轮会被记成
		// "assistant 说了空话、turn completed"，退出码 0——用户看到的是
		// "模型什么都没说"，而真相是"这个模型不能用"。两种事的下一步完全不同。
		if err := emptyResponseErr(resp); err != nil {
			return msgs, fmt.Errorf("第 %d 步模型调用失败: %w", step, err)
		}
		msgs = append(msgs, assistantMessage(resp))

		var usage *llm.Usage
		if resp.Usage.TotalTokens > 0 {
			u := resp.Usage
			usage = &u
		}
		if err := l.emit(Event{Kind: EventAssistantMsg, Turn: turn, Step: step, Finish: resp.FinishReason,
			Assistant: &AssistantTurn{
				Content: resp.Content, Reasoning: resp.Reasoning,
				ToolCalls: resp.ToolCalls, Finish: resp.FinishReason, Usage: usage,
			}}); err != nil {
			return msgs, err
		}

		if len(resp.ToolCalls) == 0 {
			if err := l.emit(Event{Kind: EventTurnEnd, Turn: turn, Step: step, Finish: resp.FinishReason}); err != nil {
				return msgs, err
			}
			return msgs, nil
		}

		stopAfter := false
		for _, call := range resp.ToolCalls {
			msg, stop := l.runTool2(ctx, turn, step, call)
			msgs = append(msgs, msg)
			// 带图的结果：图片不能塞进 tool 消息（OpenAI 兼容接口不支持），
			// 所以紧跟一条 user 消息把图送过去。少了这一步，模型只会看到
			// "读到了图片"这几个字，却看不见图——工具等于白跑。
			if img := l.lastImages; len(img) > 0 {
				l.lastImages = nil
				msgs = append(msgs, llm.Message{
					Role:    "user",
					Content: "（这是工具刚返回的图片）",
					Images:  img,
				})
			}
			if stop {
				stopAfter = true
			}
		}
		if err := l.emit(Event{Kind: EventStepEnd, Turn: turn, Step: step}); err != nil {
			return msgs, err
		}
		// 工具有话说"必须等人"：本轮到此为止，别再让模型自己往下编。
		if stopAfter {
			if err := l.emit(Event{Kind: EventTurnEnd, Turn: turn, Step: step, Finish: "awaiting_user"}); err != nil {
				return msgs, err
			}
			return msgs, nil
		}
	}

	return msgs, fmt.Errorf("turn 用满了 %d 步仍未收敛（模型一直在请求工具）", l.maxSteps())
}

// runTool 执行一次工具调用并返回给模型的 tool 消息。
func (l *Loop) runTool(ctx context.Context, turn, step int, call llm.ToolCall) llm.Message {
	msg, _ := l.runTool2(ctx, turn, step, call)
	return msg
}

func (l *Loop) runTool2(ctx context.Context, turn, step int, call llm.ToolCall) (llm.Message, bool) {
	args := call.Function.Arguments
	_ = l.emit(Event{
		Kind: EventToolCall, Turn: turn, Step: step,
		CallID: call.ID, ToolName: call.Function.Name, Arguments: args,
	})

	msg := llm.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name}
	if l.Runner == nil {
		msg.Content = "（宿主没有工具执行器）"
		return msg, false
	}
	res, err := l.Runner.Run(ctx, call.Function.Name, json.RawMessage(args))
	if err != nil {
		// 失败时**模型看到的**和**客户端看到的**必须是同一份文本。
		// 早先发给客户端的是工具原始的 res（Summary 为空），
		// 界面上就是一行红色但什么都不说的记录。
		shown := spi.Result{
			Summary: "Error: " + err.Error(),
			Hint:    res.Hint,
		}
		// 取消/中断 ≠ 失败：命令可能已经跑完了，只是我们没收到结果。
		// DSH 的措辞就是这个意思——"结果未知，你自己决定要不要重试"。
		// 说成 "failed" 会让模型以为命令没执行，从而做出错误判断。
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			shown.Summary = "The tool call was interrupted after it was recorded, but no result was durably recorded. " +
				"Its outcome is unknown. Decide whether to retry from the tool's output or a safer read."
			shown.Hint = "结果未知：先读一下现场（文件/进程/日志）确认它到底做没做，再决定重试与否"
		}
		msg.Content = renderBlocks(l.project(shown))
		_ = l.emit(Event{
			Kind: EventToolResult, Turn: turn, Step: step,
			CallID: call.ID, ToolName: call.Function.Name, IsError: true, Err: err, Result: &shown,
		})
		return msg, false
	}
	msg.Content = renderBlocks(l.project(res))
	_ = l.emit(Event{
		Kind: EventToolResult, Turn: turn, Step: step,
		CallID: call.ID, ToolName: call.Function.Name, Result: &res,
	})
	if len(res.Images) > 0 {
		imgs := make([]llm.MessageImage, 0, len(res.Images))
		for _, im := range res.Images {
			imgs = append(imgs, llm.MessageImage{MIME: im.MIME, DataBase64: im.DataBase64})
		}
		l.lastImages = imgs
	}
	return msg, res.Stop
}

func (l *Loop) project(r spi.Result) []spi.Block {
	if l.Project != nil {
		return l.Project(r)
	}
	// 默认投影：摘要 + 提示；Data 交给上层（会话层有更完整的实现）。
	blocks := make([]spi.Block, 0, 2)
	if r.Summary != "" {
		blocks = append(blocks, spi.Block{Type: "text", Text: r.Summary})
	}
	if r.Hint != "" {
		blocks = append(blocks, spi.Block{Type: "text", Text: "提示：" + r.Hint})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, spi.Block{Type: "text", Text: "（无输出）"})
	}
	return blocks
}

func renderBlocks(blocks []spi.Block) string {
	var b strings.Builder
	for i, blk := range blocks {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(blk.Text)
	}
	return b.String()
}

// assistantMessage 把一次响应变成历史里的 assistant 消息。
func assistantMessage(resp *llm.Response) llm.Message {
	return llm.Message{
		Role:      "assistant",
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	}
}

func (l *Loop) toolDefs() []llm.ToolDef {
	if l.Tools == nil {
		return nil
	}
	tools := l.Tools.Tools()
	defs := make([]llm.ToolDef, 0, len(tools))
	for _, t := range tools {
		schema := t.Schema()
		if l.CompactTools {
			schema = CompactSchema(schema)
		}
		defs = append(defs, llm.ToolDef{
			Type: "function",
			Function: llm.ToolFuncDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  schema,
			},
		})
	}
	return defs
}

// CompactSchema 去掉 JSON schema 里所有 **annotation** description，只留结构。
//
// 为什么值得做：工具表 17 KB 里有 6.6 KB 是参数的说明文字与它们的键名开销。
// 保留结构（类型、enum、required）就够模型填对参数的情形很常见——
// 但"常见"不等于"总是"，所以这只是个开关，默认仍是完整表。
//
// 关键区别（真机踩过，2026-11-04）：`properties` 这一层的键是**参数名**，不是注解。
// 本仓有 8 个工具真有一个叫 `description` 的参数。早先这里只认键名，见
// `description` 就删，于是参数被删掉、required 里却还点着它——模型看到
// "必填但没类型"，凭空少一条契约，而且整条链路没有任何一处报错。
// 现在按位置判断：只有**schema 对象上的** description 是注解；
// properties/patternProperties/$defs 映射里的键，一个都不许动。
func CompactSchema(raw json.RawMessage) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(compactNode(v, false))
	if err != nil {
		return raw
	}
	return b
}

// compactNode 递归压一个 schema 节点。
//
// inPropertyMap 为真表示 x 是 `properties` 这类映射的**值**：
// 它的键是参数名（可能就叫 description），它的值是参数自己的 schema。
func compactNode(x any, inPropertyMap bool) any {
	switch t := x.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		if inPropertyMap {
			// 这一层是「参数名 → schema」：键全部保留，值按 schema 走。
			for k, val := range t {
				out[k] = compactNode(val, false)
			}
			return out
		}
		for k, val := range t {
			if k == "description" {
				continue // 这个才是注解
			}
			// properties/patternProperties/$defs 的值是「名字 → schema」的映射。
			out[k] = compactNode(val, isPropertyMapKey(k))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = compactNode(val, false)
		}
		return out
	}
	return x
}

// isPropertyMapKey 判断某个 schema 关键字下面挂的是不是「名字 → schema」。
//
// 只有这四个：properties / patternProperties / $defs / definitions。
// 其余（items / anyOf / additionalProperties…）挂的是 schema 本身，
// 哪怕参数正好叫 items，也不能把它的名字当关键字处理。
func isPropertyMapKey(k string) bool {
	switch k {
	case "properties", "patternProperties", "$defs", "definitions":
		return true
	}
	return false
}

// emptyResponseErr 把"看起来成功、其实什么都没拿到"的响应判成错误。
//
// 判据只有一条：**正文、思考、工具调用三者皆空**。这时候无论是哪种 finish，
// 这一轮都没有任何可用的产出，继续走下去只会让模型对着空历史瞎猜。
// 分两种情况说清，因为下一步完全不同：
//   - finish=length：是被 max_tokens 截断了（把上限调大，不是换模型）
//   - 其它：这个模型 id 大概率不可用（`dshd models` 看一眼，别在这上面重试）
func emptyResponseErr(resp *llm.Response) error {
	if resp == nil {
		return fmt.Errorf("模型没有返回响应体（网关可能中途断了）：先 dshd doctor 看端点通不通")
	}
	if len(resp.Content) > 0 || len(resp.Reasoning) > 0 || len(resp.ToolCalls) > 0 {
		return nil
	}
	if resp.FinishReason == "length" {
		return fmt.Errorf("模型输出被 max_tokens 截断了，这一轮没有任何可用内容：把上限调大再试")
	}
	return fmt.Errorf("模型返回了空响应（finish=%q）：这个模型 id 可能不可用——用 dshd models 看有哪些、"+
		"或在 --model 上换一个；不要在这上面反复重试", resp.FinishReason)
}
