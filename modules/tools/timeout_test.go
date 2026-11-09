package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// per-call deadline：单次调用可以声明自己的预算，覆盖流水线的统一预算。
//
// 红：`Options.ToolTimeout` 是流水线**构造时**定死的（默认 30 分钟），
// 调用方没有办法给"这一次"设一个更短的窗口——`spi.WithToolDeadline` 根本不存在。
func TestPerCallDeadlineOverridesPipelineDefault(t *testing.T) {
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	tl := &hangTool{name: "hang", release: release, started: make(chan struct{})}
	p, k := newPipe(t, Options{ToolTimeout: 30 * time.Minute}, tl)
	defer k.Root().Dispose()

	budget := 80 * time.Millisecond
	ctx := spi.WithToolDeadline(context.Background(), budget)

	type outcome struct {
		res spi.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := p.Run(ctx, "hang", json.RawMessage(`{}`))
		done <- outcome{res: res, err: err}
	}()

	select {
	case <-tl.started:
	case <-time.After(3 * time.Second):
		t.Fatal("工具没有开始执行")
	}

	var out outcome
	select {
	case out = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("per-call deadline 没生效：还在等流水线默认的 30 分钟")
	}

	var te *ToolTimeoutError
	if !errors.As(out.err, &te) {
		t.Fatalf("per-call deadline 到点必须报成 *ToolTimeoutError，得到 %T: %v", out.err, out.err)
	}
	if te.Budget != budget {
		t.Fatalf("预算应是这一次调用声明的 %s，得到 %s", budget, te.Budget)
	}
	if te.Source != timeoutSourcePerCall {
		t.Fatalf("错误里要写清预算是单次调用定的（%q），得到 %q", timeoutSourcePerCall, te.Source)
	}
	// 结构化错误三样都要在：Summary 给人看、Data 给模型事实、Hint 给下一步。
	if out.res.Summary == "" || out.res.Data == nil || out.res.Hint == "" {
		t.Fatalf("超时结果必须结构化：%+v", out.res)
	}
	data, ok := out.res.Data.(map[string]any)
	if !ok || data["code"] != "tool_timeout" || data["tool"] != "hang" {
		t.Fatalf("结构化字段不对：%#v", out.res.Data)
	}
	if data["timeout_ms"] != budget.Milliseconds() {
		t.Fatalf("timeout_ms 应为 %d（这一次调用的预算），得到 %#v", budget.Milliseconds(), data["timeout_ms"])
	}

	close(release)
	released = true
}

// 单次调用也可以**关掉**统一预算：负值 = 只看各工具自己。
func TestPerCallDeadlineCanDisablePipelineTimeout(t *testing.T) {
	tl := &slowTool{name: "slow", d: 120 * time.Millisecond, res: spi.Result{Summary: "慢慢跑完"}}
	p, k := newPipe(t, Options{ToolTimeout: 20 * time.Millisecond}, tl)
	defer k.Root().Dispose()

	ctx := spi.WithToolDeadline(context.Background(), -1)
	res, err := p.Run(ctx, "slow", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("这一次调用明确关掉了 deadline，不该被掐：%v", err)
	}
	if res.Summary != "慢慢跑完" {
		t.Fatalf("结果不对：%+v", res)
	}
}

// 没有 per-call 绑定时，默认 30 分钟必须**一个字节都不变**。
func TestPerCallDeadlineUnsetKeepsDefaultBudget(t *testing.T) {
	t.Setenv(toolTimeoutEnv, "")
	tl := &slowTool{name: "ok", res: spi.Result{Summary: "ok"}}
	p, k := newPipe(t, Options{}, tl)
	defer k.Root().Dispose()

	budget, source := resolveToolBudget(context.Background(), p.opts.ToolTimeout)
	if budget != defaultToolTimeout {
		t.Fatalf("默认预算被改掉了：%s（期望 %s）", budget, defaultToolTimeout)
	}
	if source != timeoutSourcePipeline {
		t.Fatalf("没绑 per-call 时来源应是流水线，得到 %q", source)
	}
	if p.opts.ToolTimeout < 15*time.Minute {
		t.Fatalf("统一预算 %s 比 bash 自带的 15 分钟上限还短", p.opts.ToolTimeout)
	}
}

// 三值约定：0 = 未定义（用流水线）、正 = 这一次的预算、负 = 这一次关闭。
func TestToolBudgetResolution(t *testing.T) {
	pipeline := 42 * time.Minute
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		want    time.Duration
		wantSrc string
	}{
		{name: "unset", ctx: context.Background(), want: pipeline, wantSrc: timeoutSourcePipeline},
		{name: "zero", ctx: spi.WithToolDeadline(context.Background(), 0), want: pipeline, wantSrc: timeoutSourcePipeline},
		{name: "positive", ctx: spi.WithToolDeadline(context.Background(), 90*time.Millisecond), want: 90 * time.Millisecond, wantSrc: timeoutSourcePerCall},
		{name: "negative", ctx: spi.WithToolDeadline(context.Background(), -1), want: -1, wantSrc: timeoutSourcePerCall},
		{name: "nil-ctx", ctx: nil, want: pipeline, wantSrc: timeoutSourcePipeline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, src := resolveToolBudget(tc.ctx, pipeline)
			if got != tc.want || src != tc.wantSrc {
				t.Fatalf("budget=%s source=%s，期望 %s / %s", got, src, tc.want, tc.wantSrc)
			}
		})
	}
}
