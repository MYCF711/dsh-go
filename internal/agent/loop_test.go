package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

// ---------- 假模型 ----------

type scriptedChat struct {
	steps []*llm.Response
	errs  []error
	i     int
	reqs  []llm.Request
}

func (c *scriptedChat) Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error) {
	c.reqs = append(c.reqs, req)
	if c.i >= len(c.steps) {
		return nil, fmt.Errorf("脚本用完了")
	}
	i := c.i
	c.i++
	if i < len(c.errs) && c.errs[i] != nil {
		return nil, c.errs[i]
	}
	r := c.steps[i]
	if onEvent != nil {
		if r.Reasoning != "" {
			_ = onEvent(llm.Event{Kind: llm.EventReasoning, Text: r.Reasoning})
		}
		if r.Content != "" {
			_ = onEvent(llm.Event{Kind: llm.EventContent, Text: r.Content})
		}
	}
	return r, nil
}

// ---------- 假工具 ----------

type fakeTool struct {
	name string
	res  spi.Result
	err  error
}

func (t *fakeTool) Name() string            { return t.name }
func (t *fakeTool) Caps() spi.Caps          { return spi.Caps{} }
func (t *fakeTool) Description() string     { return "假工具" }
func (t *fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *fakeTool) Exec(context.Context, json.RawMessage) (spi.Result, error) {
	return t.res, t.err
}

type fakeRegistry struct{ tools map[string]spi.Tool }

func (r *fakeRegistry) Register(t spi.Tool) error {
	if r.tools == nil {
		r.tools = map[string]spi.Tool{}
	}
	r.tools[t.Name()] = t
	return nil
}
func (r *fakeRegistry) Tools() []spi.Tool {
	out := make([]spi.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
func (r *fakeRegistry) Get(name string) (spi.Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

type fakeRunner struct{ reg *fakeRegistry }

func (r *fakeRunner) Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
	t, ok := r.reg.Get(name)
	if !ok {
		return spi.Result{Hint: "没有这个工具"}, fmt.Errorf("工具 %q 不存在", name)
	}
	return t.Exec(ctx, args)
}

func newLoop(t *testing.T, chat Chat, tools ...spi.Tool) (*Loop, *[]Event) {
	t.Helper()
	reg := &fakeRegistry{tools: map[string]spi.Tool{}}
	for _, tl := range tools {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	var events []Event
	l := &Loop{
		Chat:   chat,
		Tools:  reg,
		Runner: &fakeRunner{reg: reg},
		Model:  "test-model",
		Emit: func(e Event) error {
			events = append(events, e)
			return nil
		},
	}
	return l, &events
}

func kinds(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = string(e.Kind)
	}
	return out
}

// ---------- 测试 ----------

func TestSingleStepWithoutTools(t *testing.T) {
	chat := &scriptedChat{steps: []*llm.Response{{Content: "你好", FinishReason: "stop"}}}
	l, events := newLoop(t, chat)

	hist, err := l.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[1].Role != "assistant" || hist[1].Content != "你好" {
		t.Fatalf("历史 = %+v", hist)
	}
	got := strings.Join(kinds(*events), ",")
	want := "step/start,assistant/text,assistant/message,turn/end"
	if got != want {
		t.Fatalf("事件序列 = %s，期望 %s", got, want)
	}
	if (*events)[len(*events)-1].Finish != "stop" {
		t.Fatalf("turn/end 没带 finish: %+v", (*events)[len(*events)-1])
	}
}

func TestToolCallRunsAndFeedsBack(t *testing.T) {
	tool := &fakeTool{name: "read", res: spi.Result{Summary: "读到 3 行"}}
	chat := &scriptedChat{steps: []*llm.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{{
				ID: "call_1", Type: "function",
				Function: llm.Func{Name: "read", Arguments: `{"file_path":"a.txt"}`},
			}},
		},
		{Content: "读完了", FinishReason: "stop"},
	}}
	l, events := newLoop(t, chat, tool)

	hist, err := l.Run(context.Background(), 2, []llm.Message{{Role: "user", Content: "读 a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 4 {
		t.Fatalf("历史长度 = %d：%+v", len(hist), hist)
	}
	if hist[1].Role != "assistant" || len(hist[1].ToolCalls) != 1 {
		t.Fatalf("assistant 消息没带工具调用: %+v", hist[1])
	}
	if hist[2].Role != "tool" || hist[2].ToolCallID != "call_1" {
		t.Fatalf("tool 消息不对: %+v", hist[2])
	}
	if !strings.Contains(hist[2].Content, "读到 3 行") {
		t.Fatalf("tool 消息内容 = %q", hist[2].Content)
	}
	if hist[3].Content != "读完了" {
		t.Fatalf("最终回答 = %q", hist[3].Content)
	}

	got := strings.Join(kinds(*events), ",")
	want := "step/start,assistant/message,tool/call,tool/result,step/end,step/start,assistant/text,assistant/message,turn/end"
	if got != want {
		t.Fatalf("事件序列 =\n %s\n期望\n %s", got, want)
	}
}

func TestToolErrorBecomesToolMessage(t *testing.T) {
	tool := &fakeTool{name: "read", res: spi.Result{Hint: "先 glob 找文件"}, err: errors.New("文件不存在")}
	chat := &scriptedChat{steps: []*llm.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Function: llm.Func{Name: "read", Arguments: `{}`},
			}},
		},
		{Content: "抱歉", FinishReason: "stop"},
	}}
	l, events := newLoop(t, chat, tool)

	hist, err := l.Run(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 没有初始历史时：0=assistant(tool_calls)，1=tool 结果，2=最终回答。
	if hist[1].Role != "tool" {
		t.Fatalf("第 1 条应是 tool 消息，得到 %+v", hist[1])
	}
	if !strings.Contains(hist[1].Content, "文件不存在") || !strings.Contains(hist[1].Content, "先 glob 找文件") {
		t.Fatalf("失败信息与 Hint 都要进 tool 消息，得到 %q", hist[1].Content)
	}
	for _, e := range *events {
		if e.Kind == EventToolResult && !e.IsError {
			t.Fatal("tool/result 应标记 IsError")
		}
	}
}

func TestMaxStepsGuard(t *testing.T) {
	call := llm.ToolCall{ID: "c", Function: llm.Func{Name: "read", Arguments: `{}`}}
	chat := &scriptedChat{steps: []*llm.Response{
		{FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{call}},
		{FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{call}},
		{FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{call}},
	}}
	l, _ := newLoop(t, chat, &fakeTool{name: "read", res: spi.Result{Summary: "x"}})
	l.MaxSteps = 3

	_, err := l.Run(context.Background(), 1, nil)
	if err == nil || !strings.Contains(err.Error(), "36") && !strings.Contains(err.Error(), "3") {
		t.Fatalf("步数用尽必须报错，得到 %v", err)
	}
}

func TestEmitErrorStops(t *testing.T) {
	chat := &scriptedChat{steps: []*llm.Response{{Content: "x", FinishReason: "stop"}}}
	l, _ := newLoop(t, chat)
	l.Emit = func(Event) error { return errors.New("下游不要了") }
	if _, err := l.Run(context.Background(), 1, nil); err == nil {
		t.Fatal("Emit 报错必须中断")
	}
}

func TestSystemPromptInsertedOnce(t *testing.T) {
	chat := &scriptedChat{steps: []*llm.Response{{Content: "ok", FinishReason: "stop"}}}
	l, _ := newLoop(t, chat)
	l.System = "你是 dsh-go"
	hist, err := l.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if hist[0].Role != "system" || hist[0].Content != "你是 dsh-go" {
		t.Fatalf("系统提示没插到最前面: %+v", hist)
	}
	if len(chat.reqs) != 1 || chat.reqs[0].Messages[0].Role != "system" {
		t.Fatal("发给模型的请求里没有系统提示")
	}
}

func TestToolDefsReachTheModel(t *testing.T) {
	chat := &scriptedChat{steps: []*llm.Response{{Content: "ok", FinishReason: "stop"}}}
	l, _ := newLoop(t, chat, &fakeTool{name: "read"}, &fakeTool{name: "write"})
	if _, err := l.Run(context.Background(), 1, nil); err != nil {
		t.Fatal(err)
	}
	defs := chat.reqs[0].Tools
	if len(defs) != 2 {
		t.Fatalf("工具定义数 = %d", len(defs))
	}
	for _, d := range defs {
		if d.Type != "function" || d.Function.Name == "" || len(d.Function.Parameters) == 0 {
			t.Fatalf("工具定义不完整: %+v", d)
		}
	}
}

func TestModelErrorSurfaces(t *testing.T) {
	chat := &scriptedChat{steps: []*llm.Response{{}}, errs: []error{errors.New("网关 502")}}
	l, _ := newLoop(t, chat)
	_, err := l.Run(context.Background(), 1, nil)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("模型错误要冒出来，得到 %v", err)
	}
}
