// Package agent 是 turn/step 双循环：一次用户输入 = 一个 turn，turn 里每一步 = 一次模型调用。
//
// 它只做编排，不实现任何东西：模型来自 Chat，工具来自 spi.Runner，
// 结果如何变成消息来自 Project。
package agent

import (
	"context"
	"encoding/json"
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
	Model    string
	System   string
	MaxSteps int
	// Emit 观测量；返回错误即中断整个 turn。
	Emit func(Event) error
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
	if l.Chat == nil {
		return history, fmt.Errorf("agent: 没有配置 Chat")
	}
	msgs := append([]llm.Message(nil), history...)
	if l.System != "" && (len(msgs) == 0 || msgs[0].Role != "system") {
		msgs = append([]llm.Message{{Role: "system", Content: l.System}}, msgs...)
	}

	for step := 1; step <= l.maxSteps(); step++ {
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

		for _, call := range resp.ToolCalls {
			msgs = append(msgs, l.runTool(ctx, turn, step, call))
		}
		if err := l.emit(Event{Kind: EventStepEnd, Turn: turn, Step: step}); err != nil {
			return msgs, err
		}
	}

	return msgs, fmt.Errorf("turn 用满了 %d 步仍未收敛（模型一直在请求工具）", l.maxSteps())
}

// runTool 执行一次工具调用并返回给模型的 tool 消息。
func (l *Loop) runTool(ctx context.Context, turn, step int, call llm.ToolCall) llm.Message {
	args := call.Function.Arguments
	_ = l.emit(Event{
		Kind: EventToolCall, Turn: turn, Step: step,
		CallID: call.ID, ToolName: call.Function.Name, Arguments: args,
	})

	msg := llm.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name}
	if l.Runner == nil {
		msg.Content = "（宿主没有工具执行器）"
		return msg
	}
	res, err := l.Runner.Run(ctx, call.Function.Name, json.RawMessage(args))
	if err != nil {
		msg.Content = renderBlocks(l.project(spi.Result{
			Summary: "工具失败：" + err.Error(),
			Hint:    res.Hint,
		}))
		_ = l.emit(Event{
			Kind: EventToolResult, Turn: turn, Step: step,
			CallID: call.ID, ToolName: call.Function.Name, IsError: true, Err: err, Result: &res,
		})
		return msg
	}
	msg.Content = renderBlocks(l.project(res))
	_ = l.emit(Event{
		Kind: EventToolResult, Turn: turn, Step: step,
		CallID: call.ID, ToolName: call.Function.Name, Result: &res,
	})
	return msg
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
		defs = append(defs, llm.ToolDef{
			Type: "function",
			Function: llm.ToolFuncDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Schema(),
			},
		})
	}
	return defs
}
