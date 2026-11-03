package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

// modelRunner 是 serve 的真 runner：一轮 = 读历史 + 调一次模型 + 推事件。
//
// 这一轮**不跑工具**：没有工具调用就没有"多步"，但它足以让界面真的对上话。
// 工具循环是下一块，单独接。
//
// 落盘：这一轮的用户消息与助手回答都会追加进 v4 日志（append-only），
// 所以关掉 serve 再打开，这轮对话还在。
type modelRunner struct {
	pub   func(topic string, f transport.ServerFrame)
	home  string
	base  string
	model string
}

func (r *modelRunner) Run(ctx context.Context, sessionID, turnID, text string) error {
	topic := "session:" + sessionID

	src := newSessionSource(r.home, "")
	msgs, err := src.Messages(sessionID)
	if err != nil {
		publishTurn(r.pub, topic, turnID, err)
		return err
	}

	history := make([]llm.Message, 0, len(msgs)+1)
	for _, m := range msgs {
		if m.Role == "" || m.Text == "" {
			continue
		}
		history = append(history, llm.Message{Role: m.Role, Content: m.Text})
	}
	history = append(history, llm.Message{Role: "user", Content: text})

	resp, err := llm.New(r.base, os.Getenv("DSHD_LLM_KEY")).Stream(ctx,
		llm.Request{Model: r.model, Messages: history, Stream: true}, nil)
	if err != nil {
		publishTurn(r.pub, topic, turnID, err)
		return err
	}

	// 先推正文，让界面立刻看到字——turn/done 只表示这一轮结束了。
	if resp != nil && resp.Content != "" {
		data, _ := json.Marshal(map[string]any{"text": resp.Content})
		if r.pub != nil {
			r.pub(topic, transport.ServerFrame{Seq: 1, Type: "assistant/message", Data: data})
		}
	}
	// 落盘。顺序：先用户、再助手——与真实会话一致，读回时才对得上。
	// 存不下来必须说出来：**"回答看见了但没存"** 比"没回答"更难查。
	if perr := r.persist(sessionID, text, resp); perr != nil {
		publishTurn(r.pub, topic, turnID, perr)
		return perr
	}

	publishTurn(r.pub, topic, turnID, nil)
	return nil
}

// persist 把这一轮写进 v4 日志。
//
// OpenSessionAt 会抢写锁；抢不到就报错，不硬写——同一份会话只允许一个写入者，
// 硬写的后果是重复 seq，而 seq 是 surface 与遮蔽的排序依据。
func (r *modelRunner) persist(sessionID, text string, resp *llm.Response) error {
	src := newSessionSource(r.home, "")
	path, err := src.resolve(sessionID)
	if err != nil {
		return fmt.Errorf("存不下来（找不到会话文件）: %w", err)
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		return fmt.Errorf("存不下来（打不开写入）: %w", err)
	}
	defer logw.Close()

	if err := logw.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: text}})); err != nil {
		return fmt.Errorf("存不下来（写用户消息）: %w", err)
	}
	if resp != nil && resp.Content != "" {
		blocks := []map[string]any{{"type": "text", "text": resp.Content}}
		if err := logw.Append("assistant/message",
			session.AssistantMessageEvent(1, 1, blocks, resp.FinishReason, nil)); err != nil {
			return fmt.Errorf("存不下来（写回答）: %w", err)
		}
	}
	return nil
}
