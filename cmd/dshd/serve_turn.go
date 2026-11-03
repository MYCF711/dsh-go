package main

import (
	"context"
	"encoding/json"
	"os"

	"dshgo/internal/llm"
	"dshgo/internal/transport"
)

// modelRunner 是 serve 的真 runner：一轮 = 读历史 + 调一次模型 + 推事件。
//
// 这一轮**不跑工具**：没有工具调用就没有"多步"，但它足以让界面真的对上话。
// 工具循环是下一块，单独接。
//
// ⚠️ 落盘这轮还没做：关掉 serve 再打开，这一轮不会出现在历史里。
// 这是**已知缺口**，不是设计——下一轮补（要抢写锁 + 按 v4 规则追加用户/助手事件）。
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
	publishTurn(r.pub, topic, turnID, nil)
	return nil
}
