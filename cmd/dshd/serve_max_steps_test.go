package main

// PLAY-HOST-DEFAULTS：每轮步数上限的默认值。
//
// # 为什么钉这个数（官方真实会话对照证据）
//
// 官方 40 个真实会话 3060 轮里 **1519 轮**的 max step ≥ 9；全体 assistant step
// 分位 p50=13 / p90=90 / p95=174 / p99=938 / max=1318，>200 的样本 11507 个。
// 也就是说 8 步的硬上限会砍掉一半以上真实轮次——用户看到的是
// `turn 用满了 8 步仍未收敛（模型一直在请求工具）`，而那一轮本来能跑完。
//
// 所以这里钉两件事：
//   - 默认值必须明显高于真实分布的 p99（2000），不能回到 8/12；
//   - 12 步的模型-工具循环在默认值下**必须正常收敛**，且不返回"用满仍未收敛"。
//
// 不吞未收敛错误、不无限循环：真的死循环仍然在 2000 步被截断，只是不再比
// 官方宿主更弱（AGENTS.md 能力下限规则）。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

const requiredConvergenceSteps = 7

// twelveStepToolLoopSteps 是回归用的真实形状：连续 11 步请求工具、第 12 步给最终文本。
// 1519/3060 轮真实会话超过 8 步，12 步正好落在 p50=13 附近。
const twelveStepToolLoopSteps = 12

// toolLoopChat 是确定性的 fake 模型：前 toolSteps-1 步请求工具，第 toolSteps 步给最终文本。
type toolLoopChat struct {
	calls     int
	toolSteps int
}

func (c *toolLoopChat) Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error) {
	c.calls++
	if c.calls < c.toolSteps {
		return &llm.Response{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{{
				ID:       "call-" + strconv.Itoa(c.calls),
				Function: llm.Func{Name: "read", Arguments: `{}`},
			}},
		}, nil
	}
	return &llm.Response{Content: "converged", FinishReason: "stop"}, nil
}

type okStepRunner struct{}

func (okStepRunner) Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
	return spi.Result{Summary: "ok"}, nil
}

func runToolLoop(t *testing.T, toolSteps, maxSteps int) (int, error) {
	t.Helper()
	chat := &toolLoopChat{toolSteps: toolSteps}
	loop := &agent.Loop{Chat: chat, Runner: okStepRunner{}, Model: "m", MaxSteps: maxSteps}
	_, err := loop.Run(context.Background(), 1, nil)
	return chat.calls, err
}

func TestServeDefaultMaxStepsConvergesSevenStepSequence(t *testing.T) {
	// 先证明 7 步序列在 6 步上限下确实"用满仍未收敛"（旧默认红点）。
	if _, err := runToolLoop(t, requiredConvergenceSteps, 6); err == nil {
		t.Fatal("6 步上限对 7 步序列必须报未收敛（红点基线）")
	} else if !strings.Contains(err.Error(), "用满") {
		t.Fatalf("6 步上限的错误必须说明用满，实际 %v", err)
	}
	// 8 是真实试玩里报"用满仍未收敛"的那个值，必须已经被甩开。
	if defaultTurnMaxSteps < 9 {
		t.Fatalf("每轮步数默认值 %d 回到 8 步硬上限：官方 3060 轮里 1519 轮 max step ≥ 9，"+
			"这会把一半以上真实轮次砍成“用满仍未收敛”", defaultTurnMaxSteps)
	}
	// 2000 的依据：真实分布 p99=938、max=1318，安全阀要明显高于分布而不是卡在中间。
	if defaultTurnMaxSteps < 2000 {
		t.Fatalf("每轮步数默认值 %d 低于官方真实分布 p99=938 的安全余量（2000）", defaultTurnMaxSteps)
	}

	calls, err := runToolLoop(t, requiredConvergenceSteps, defaultTurnMaxSteps)
	if err != nil {
		t.Fatalf("默认 %d 步必须让 7 步序列收敛，实际 %v", defaultTurnMaxSteps, err)
	}
	if calls != requiredConvergenceSteps {
		t.Fatalf("收敛应在第 %d 步，实际 %d 次模型请求", requiredConvergenceSteps, calls)
	}
}

// 12 步模型-工具循环（真实试玩里被 8 步砍掉的那一类轮次）必须正常完成。
func TestDefaultMaxStepsConvergesTwelveStepToolLoop(t *testing.T) {
	calls, err := runToolLoop(t, twelveStepToolLoopSteps, defaultTurnMaxSteps)
	if err != nil {
		t.Fatalf("默认 %d 步下 12 步模型-工具循环必须正常完成，实际 %v", defaultTurnMaxSteps, err)
	}
	if calls != twelveStepToolLoopSteps {
		t.Fatalf("收敛应在第 %d 步，实际 %d 次模型请求", twelveStepToolLoopSteps, calls)
	}
}

// 默认值不是"无限"：死循环仍然在 2000 步被截断，错误文案不变。
func TestDefaultMaxStepsStillStopsRunawayLoop(t *testing.T) {
	chat := &toolLoopChat{toolSteps: defaultTurnMaxSteps + 50} // 永远在请求工具
	loop := &agent.Loop{Chat: chat, Runner: okStepRunner{}, Model: "m", MaxSteps: defaultTurnMaxSteps}
	_, err := loop.Run(context.Background(), 1, nil)
	if err == nil {
		t.Fatal("死循环必须在默认步数处被截断，不能无限跑")
	}
	if !strings.Contains(err.Error(), "用满") || !strings.Contains(err.Error(), strconv.Itoa(defaultTurnMaxSteps)) {
		t.Fatalf("截断错误必须说明用满 %d 步，实际 %v", defaultTurnMaxSteps, err)
	}
	if chat.calls != defaultTurnMaxSteps {
		t.Fatalf("死循环应在 %d 步截断，实际 %d 次模型请求", defaultTurnMaxSteps, chat.calls)
	}
}

func TestServeDefaultMaxStepsForwardedToChild(t *testing.T) {
	home := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv.json")
	t.Setenv("DSHD_MODEL_WIRE_ARGV", capture)
	t.Setenv("DSHD_SERVE_NO_FOLLOW", "1")
	sid := "session-play-defaults"
	lg, err := session.CreateSession(home, t.TempDir(), sid, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}

	base := &loopRunner{home: home, policy: "deny", maxSteps: defaultTurnMaxSteps, model: "m"}
	runner := &modelOverrideLoopRunner{base: base}
	if err := runner.Run(context.Background(), sid, "turn-"+sid, "prompt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, args := readLoopModelArg(t, capture)
	if !containsArgPair(args, "--max-steps", strconv.Itoa(defaultTurnMaxSteps)) {
		t.Fatalf("serve 的默认步数没有原样转发给子进程：%v", args)
	}
}
