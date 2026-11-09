package agent

// 官方 `context.runtimeContext.perStep`（默认 true，@deepseek-ai/dsh-agent-loop/README.md:120）。
//
// 判据（缺一即红）：
//  1. 每一步的**请求**里都带最新的运行时事实（时间/目录这类会过期的值）；
//  2. 事实段**不落进返回历史**（否则下一轮带的是过期值，等于没修）；
//  3. 没配 provider 时请求消息逐字节不变（默认形状不变）；
//  4. provider 返回空白 = 这一段不出现（不留空壳）。

import (
	"context"
	"strings"
	"testing"

	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

// runtimeCaptureChat 记下每次请求的消息，并按脚本回答。
type runtimeCaptureChat struct {
	requests [][]llm.Message
	replies  []*llm.Response
}

func (c *runtimeCaptureChat) Stream(_ context.Context, req llm.Request, _ func(llm.Event) error) (*llm.Response, error) {
	c.requests = append(c.requests, append([]llm.Message(nil), req.Messages...))
	if len(c.replies) == 0 {
		return &llm.Response{Content: "done", FinishReason: "stop"}, nil
	}
	reply := c.replies[0]
	c.replies = c.replies[1:]
	return reply, nil
}

func runtimeMessages(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == "system" && strings.HasPrefix(m.Content, RuntimeContextPrefix) {
			out = append(out, m.Content)
		}
	}
	return out
}

func newRuntimeLoop(chat Chat, facts func() string) *Loop {
	reg := &fakeRegistry{tools: map[string]spi.Tool{"noop": &fakeTool{name: "noop"}}}
	return &Loop{
		Chat:           chat,
		Runner:         &fakeRunner{reg: reg},
		Model:          "m",
		System:         "sys",
		MaxSteps:       3,
		RuntimeContext: facts,
	}
}

func TestRuntimeContextIsReprojectedEveryStep(t *testing.T) {
	chat := &runtimeCaptureChat{replies: []*llm.Response{
		// 第一步：请求一次工具调用，逼出第二步。
		{FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.Func{Name: "noop", Arguments: "{}"}}}},
		{Content: "好了", FinishReason: "stop"},
	}}
	step := 0
	l := newRuntimeLoop(chat, func() string {
		step++
		return "当前时间: 2026-11-09T12:0" + string(rune('0'+step)) + ":00Z\n工作目录: D:/ws"
	})
	history, err := l.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(chat.requests) < 2 {
		t.Fatalf("至少两步才谈得上「每步重投影」，实际 %d 步", len(chat.requests))
	}
	for i, req := range chat.requests {
		got := runtimeMessages(req)
		if len(got) != 1 {
			t.Fatalf("第 %d 步必须恰好一条运行时事实段，得到 %d：%v", i+1, len(got), got)
		}
		if !strings.Contains(got[0], "当前时间") {
			t.Fatalf("第 %d 步缺事实正文：%q", i+1, got[0])
		}
	}
	// 每一步都是**最新**值（不是第一步的陈旧快照）。
	got := runtimeMessages(chat.requests[1])
	if len(got) != 1 || !strings.Contains(got[0], "12:02:00Z") {
		t.Fatalf("第二步必须重新投影（含 12:02），实际 %v", got)
	}
	// 位置：必须插在领头 system 之后，**不能**占末尾——末尾那条是这一轮真正的
	// 输入（user 消息或 tool 结果），宿主自己的 run 用例与真实链都按它取 prompt。
	req := chat.requests[1]
	if strings.HasPrefix(req[len(req)-1].Content, RuntimeContextPrefix) {
		t.Fatalf("运行时事实不能占最后一条消息：%+v", req[len(req)-1])
	}
	if req[1].Role != "system" || !strings.HasPrefix(req[1].Content, RuntimeContextPrefix) {
		t.Fatalf("运行时事实应紧跟领头 system（下标 1），实际 %+v", req[:2])
	}
	// 事实段不能进返回历史。
	if got := runtimeMessages(history); len(got) != 0 {
		t.Fatalf("运行时事实不能落进历史（否则下一轮带过期值）：%v", got)
	}
}

func TestRuntimeContextAbsentKeepsRequestBytes(t *testing.T) {
	chat := &runtimeCaptureChat{}
	l := newRuntimeLoop(chat, nil)
	if _, err := l.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	req := chat.requests[0]
	if len(req) != 2 || req[0].Role != "system" || req[1].Role != "user" {
		t.Fatalf("没配 RuntimeContext 时请求消息必须保持原形状：%+v", req)
	}
	if runtimeMessages(req) != nil {
		t.Fatal("没配 provider 不能凭空出现运行时事实段")
	}
}

func TestRuntimeContextBlankFactsAreOmitted(t *testing.T) {
	chat := &runtimeCaptureChat{}
	l := newRuntimeLoop(chat, func() string { return "  \n\t " })
	if _, err := l.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if got := runtimeMessages(chat.requests[0]); len(got) != 0 {
		t.Fatalf("全空白事实不能造出空段：%v", got)
	}
	if len(chat.requests[0]) != 2 {
		t.Fatalf("空白事实时消息数必须不变（system+user）：%+v", chat.requests[0])
	}
}
