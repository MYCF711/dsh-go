package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dshgo/internal/spi"
)

type fakeRunner struct {
	res spi.SubagentResult
	err error
	got spi.SubagentRequest
}

func (f *fakeRunner) RunSub(ctx context.Context, req spi.SubagentRequest) (spi.SubagentResult, error) {
	f.got = req
	return f.res, f.err
}

// 正常：只要结果，不要中间步骤；Data 带步数与字数。
func TestReturnsOnlyResult(t *testing.T) {
	svc := &fakeRunner{res: spi.SubagentResult{Text: "结论：42", Steps: 4}}
	tool := &Tool{svc: svc}
	res, err := tool.Exec(context.Background(),
		json.RawMessage(`{"description":"查一件事","prompt":"自包含的任务描述"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "结论：42" {
		t.Fatalf("正文要是子代理的结论: %q", res.Text)
	}
	if d := res.Data.(data); d.Steps != 4 || d.Description != "查一件事" {
		t.Fatalf("Data 不对: %+v", d)
	}
	if svc.got.Prompt != "自包含的任务描述" {
		t.Fatalf("prompt 没传下去: %+v", svc.got)
	}
}

// 没装运行器：明确报错，不假装"派了"。
func TestNoRunnerIsLoud(t *testing.T) {
	tool := &Tool{}
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"description":"x","prompt":"y"}`))
	if err == nil {
		t.Fatal("没有运行器必须报错")
	}
	if !strings.Contains(res.Hint, "bash") {
		t.Fatalf("Hint 要给退路: %q", res.Hint)
	}
}

// 空 prompt / 空 description：都要说清。
func TestValidation(t *testing.T) {
	tool := &Tool{svc: &fakeRunner{}}
	if _, err := tool.Exec(context.Background(), json.RawMessage(`{"description":"x","prompt":"  "}`)); err == nil {
		t.Fatal("空 prompt 要报错")
	}
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"description":"","prompt":"y"}`))
	if err == nil || !strings.Contains(res.Hint, "3-5 个词") {
		t.Fatalf("空 description 要报错并说格式: %v %q", err, res.Hint)
	}
}

// 子代理失败：错误要传上来，不能吞掉说"完成"。
func TestChildFailurePropagates(t *testing.T) {
	tool := &Tool{svc: &fakeRunner{err: errors.New("子代理没有产出正文")}}
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"description":"x","prompt":"y"}`))
	if err == nil {
		t.Fatal("子代理失败必须报错")
	}
	if !strings.Contains(res.Hint, "自己直接做") {
		t.Fatalf("Hint 要给退路: %q", res.Hint)
	}
}
