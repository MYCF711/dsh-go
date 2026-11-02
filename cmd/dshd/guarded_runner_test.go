package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"dshgo/internal/approval"
	"dshgo/internal/spi"
)

// fakeTool 只用来声明 Caps：Run 的分支全看 Caps，不看名字。
type fakeTool struct {
	name string
	caps spi.Caps
}

func (f *fakeTool) Name() string            { return f.name }
func (f *fakeTool) Description() string     { return "测试用" }
func (f *fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeTool) Caps() spi.Caps          { return f.caps }
func (f *fakeTool) Exec(context.Context, json.RawMessage) (spi.Result, error) {
	return spi.Result{Summary: "执行了"}, nil
}

type fakeReg struct{ tools map[string]spi.Tool }

func (r *fakeReg) Register(t spi.Tool) error { r.tools[t.Name()] = t; return nil }
func (r *fakeReg) Get(name string) (spi.Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}
func (r *fakeReg) Tools() []spi.Tool {
	out := make([]spi.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// inner 记录"到底执行了没有"。
type recorder struct {
	called int
}

func (r *recorder) Run(context.Context, string, json.RawMessage) (spi.Result, error) {
	r.called++
	return spi.Result{Summary: "执行了"}, nil
}

func newRunner(t *testing.T, caps spi.Caps, modeApproval string, answer bool, asked *int) (*guardedRunner, *recorder) {
	t.Helper()
	inner := &recorder{}
	reg := &fakeReg{tools: map[string]spi.Tool{"w": &fakeTool{name: "w", caps: caps}}}
	g := &guardedRunner{
		inner: inner, reg: reg,
		sessionID:    "s1",
		approvals:    approval.New(),
		modeApproval: modeApproval,
		ask: func(context.Context, string, string, string) (bool, error) {
			if asked != nil {
				*asked++
			}
			return answer, nil
		},
	}
	return g, inner
}

// 只读工具不经过审批：不问、直接跑。
func TestGuardedRunnerSkipsApprovalForReadOnly(t *testing.T) {
	asked := 0
	g, inner := newRunner(t, spi.Caps{}, "", false, &asked)
	if _, err := g.Run(context.Background(), "w", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if inner.called != 1 {
		t.Fatal("只读工具应当直接执行")
	}
	if asked != 0 {
		t.Fatalf("只读工具不该问人，实际问了 %d 次", asked)
	}
}

// 写类工具被拒时**绝不能执行**。这条是审批的全部意义。
func TestGuardedRunnerRefusalDoesNotExecute(t *testing.T) {
	asked := 0
	g, inner := newRunner(t, spi.Caps{Writes: true}, "", false, &asked)
	_, err := g.Run(context.Background(), "w", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("拒绝时必须有 error")
	}
	if inner.called != 0 {
		t.Fatal("被拒绝的工具一次都不能执行")
	}
	if asked != 1 {
		t.Fatalf("应当问一次，实际 %d 次", asked)
	}
}

// 模式 deny 连问都不问，且说清是"策略"不是"人"。
func TestGuardedRunnerModeDenyDoesNotAsk(t *testing.T) {
	asked := 0
	g, inner := newRunner(t, spi.Caps{Executes: true}, "deny", true, &asked)
	res, err := g.Run(context.Background(), "w", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("deny 模式必须拒绝")
	}
	if asked != 0 {
		t.Fatalf("deny 模式不该问人，实际问了 %d 次", asked)
	}
	if inner.called != 0 {
		t.Fatal("deny 模式下工具不能执行")
	}
	if res.Hint == "" {
		t.Fatal("拒绝要带 Hint")
	}
}

// 审批通过才执行。
func TestGuardedRunnerApprovedExecutes(t *testing.T) {
	g, inner := newRunner(t, spi.Caps{Network: true}, "", true, nil)
	if _, err := g.Run(context.Background(), "w", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if inner.called != 1 {
		t.Fatal("同意之后应当执行")
	}
}

// 审批本身出错（例如客户端断开）不能当成"同意"。
func TestGuardedRunnerAskErrorFailsClosed(t *testing.T) {
	inner := &recorder{}
	reg := &fakeReg{tools: map[string]spi.Tool{"w": &fakeTool{name: "w", caps: spi.Caps{Destructive: true}}}}
	g := &guardedRunner{
		inner: inner, reg: reg, sessionID: "s1", approvals: approval.New(),
		ask: func(context.Context, string, string, string) (bool, error) {
			return false, errors.New("客户端断开")
		},
	}
	if _, err := g.Run(context.Background(), "w", json.RawMessage(`{}`)); err == nil {
		t.Fatal("审批出错必须失败")
	}
	if inner.called != 0 {
		t.Fatal("审批出错时工具不能执行——失败要关，不能开")
	}
}
