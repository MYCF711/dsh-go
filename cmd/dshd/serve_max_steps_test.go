package main

// PLAY-HOST-DEFAULTS：每轮步数上限的默认值。
//
// # 官方是"没有上限"，所以默认必须是 0
//
// 以前这里钉的是 2000（"安全阀要高于真实 step 分布的 p99=938"）。那个数没有
// 官方依据——官方 agent loop 根本没有内置轮次预算：
//
//   - `dsh-agent-loop/README.zh.md:236`（英文 README.md:236 同段）：
//     "**没有内置轮次预算**……限制失控轮次的策略必须从既有生命周期扩展点
//     （如 `agent/turn-stopping`）执行取消。"
//   - `dsh-agent-loop/src/agent.ts:350` 是 `while (true)`：步号只增不减、无天花板；
//     出口只有 AbortSignal、`agent/pre-step` 拒绝、无 steer 时收轮、或抛错。
//
// 官方防死循环靠**外部中止**（per-turn AbortSignal + 两个 hook），不靠计数器。
// 宿主等价物已经在生产路径上：`internal/agent/loop.go:329/:356` 每步查 `ctx.Err()`，
// `session.cancel` → `cancelTurn`（`serve_approval.go:500-519`）取消这一轮。
//
// 所以这组用例钉三件事：
//   - 默认值就是 0（= 不设上限，与官方一致），谁把它改回 2000/8/12 立刻红；
//   - 默认值下长工具循环**不会被截断**（没有隐藏的第二套上限）；
//   - 默认值下真死循环靠**取消**退出来（官方机制），而不是靠步数计数器；
//     同时 `--max-steps` 显式给正数时仍然截断（这是宿主比官方多的一档）。

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dshgo/internal/agent"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

const requiredConvergenceSteps = 7

// longToolLoopSteps 是"远超过以前那个 2000 步安全阀之外不可能、但真实会话确实会到"
// 的形状：连续 N-1 步请求工具、第 N 步给最终文本。默认 0 时必须跑完。
const longToolLoopSteps = 500

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

// cancelAfterChat 是永远请求工具的 fake 模型（真死循环），并在第 after 次调用时
// 触发取消——它就是官方的 AbortSignal（用户点"停止"）。
type cancelAfterChat struct {
	calls     int
	after     int
	cancel    context.CancelFunc
	callStops chan int
}

func (c *cancelAfterChat) Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error) {
	c.calls++
	if c.calls == c.after {
		c.cancel()
	}
	return &llm.Response{
		FinishReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID:       "call-" + strconv.Itoa(c.calls),
			Function: llm.Func{Name: "read", Arguments: `{}`},
		}},
	}, nil
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

// 1. 默认值必须是"不设上限"（官方语义），不是任何自造的计数。
//
// 变异判据：把 `defaultTurnMaxSteps` 改回 2000/8/12，这条立刻红。
func TestDefaultTurnMaxStepsIsUnlimitedLikeOfficial(t *testing.T) {
	if defaultTurnMaxSteps != 0 {
		t.Fatalf("每轮步数默认值 = %d，但官方 agent loop **没有内置轮次预算**"+
			"（dsh-agent-loop/README.zh.md:236；src/agent.ts:350 是 while(true)，无步号天花板）。"+
			"官方口径是「0 = 不设上限」，宿主只能与之同档；要限流由部署方显式给 --max-steps 正数。",
			defaultTurnMaxSteps)
	}
}

// 2. 默认值下长工具循环必须跑完：不许存在隐藏的第二套上限。
func TestDefaultMaxStepsDoesNotTruncateLongToolLoop(t *testing.T) {
	calls, err := runToolLoop(t, longToolLoopSteps, defaultTurnMaxSteps)
	if err != nil {
		t.Fatalf("默认 %d（不设上限）下 %d 步工具循环必须正常完成，实际 %v",
			defaultTurnMaxSteps, longToolLoopSteps, err)
	}
	if calls != longToolLoopSteps {
		t.Fatalf("收敛应在第 %d 步，实际 %d 次模型请求", longToolLoopSteps, calls)
	}
	// 7 步这种最常见形状当然也要过（以前用 6 步上限当红点基线，见第 3 条）。
	if calls, err := runToolLoop(t, requiredConvergenceSteps, defaultTurnMaxSteps); err != nil || calls != requiredConvergenceSteps {
		t.Fatalf("默认值下 %d 步序列必须收敛：calls=%d err=%v", requiredConvergenceSteps, calls, err)
	}
}

// 3. 默认值下**真死循环**靠取消退出（官方机制），不是靠步数计数器。
//
// 官方 README.zh.md:236 写明官方没有内置预算、要靠 `agent/turn-stopping` 之类
// 扩展点取消；宿主的生产出口是 `session.cancel` → `cancelTurn` → 循环每步的
// `ctx.Err()`（internal/agent/loop.go:329/:356）。这条用例就是那条路径的最小复现：
// 一个永远请求工具的模型 + 第 5 次调用时的取消，必须在取消点附近退出，
// 而且必须把 context.Canceled 原样抛出来（不吞错、不改写成"用满 N 步"）。
func TestRunawayToolLoopExitsOnCancelNotStepCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chat := &cancelAfterChat{after: 5, cancel: cancel}
	loop := &agent.Loop{Chat: chat, Runner: okStepRunner{}, Model: "m", MaxSteps: defaultTurnMaxSteps}

	done := make(chan error, 1)
	go func() {
		_, err := loop.Run(ctx, 1, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("死循环被取消后必须返回错误，不能静默当成正常收轮")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后应原样返回 context.Canceled，实际 %v", err)
		}
		if strings.Contains(err.Error(), "用满") {
			t.Fatalf("不设上限时不该出现「用满」文案（那说明又冒出一个步数上限）：%v", err)
		}
		if chat.calls > chat.after+1 {
			t.Fatalf("取消后仍在请求模型：取消点 %d，实际调用 %d 次", chat.after, chat.calls)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("取消后 15s 仍未退出（已调用 %d 次）：循环没响应 context，官方也不会这样挂", chat.calls)
	}
}

// 4. `--max-steps` 显式给正数时仍然截断：这是宿主**比官方多**的那一档，
// 不许因为有官方"没有上限"就把它删掉。
func TestExplicitMaxStepsStillTruncatesRunawayLoop(t *testing.T) {
	const cap = 2000                           // 显式给一个正数，语义与默认值无关
	chat := &toolLoopChat{toolSteps: cap + 50} // 永远在请求工具
	loop := &agent.Loop{Chat: chat, Runner: okStepRunner{}, Model: "m", MaxSteps: cap}
	_, err := loop.Run(context.Background(), 1, nil)
	if err == nil {
		t.Fatal("显式上限下死循环必须被截断")
	}
	if !strings.Contains(err.Error(), "用满") || !strings.Contains(err.Error(), strconv.Itoa(cap)) {
		t.Fatalf("截断错误必须说明用满 %d 步，实际 %v", cap, err)
	}
	if chat.calls != cap {
		t.Fatalf("死循环应在 %d 步截断，实际 %d 次模型请求", cap, chat.calls)
	}
}

// 5. 显式上限的"红点基线"：6 步上限对 7 步序列必须报未收敛。
//
// 这条不依赖默认值——它钉的是"截断机制本身还有效"，与第 1 条（默认值=不设上限）
// 互补：默认值是 0 时这条仍然要绿，否则说明上限机制被改坏了。
func TestExplicitSixStepCapReportsUnconverged(t *testing.T) {
	if _, err := runToolLoop(t, requiredConvergenceSteps, 6); err == nil {
		t.Fatal("6 步上限对 7 步序列必须报未收敛（红点基线）")
	} else if !strings.Contains(err.Error(), "用满") {
		t.Fatalf("6 步上限的错误必须说明用满，实际 %v", err)
	}
}

// 6. serve 的默认值必须原样转发给子进程 run。
//
// 现在转发的是 0（= 不设上限）。这条同时钉住"不许在透传层偷偷换成别的数"：
// serve 与 run 一旦各有一套默认值，屏幕上就会看到一个查不到原因的截断。
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
		t.Fatalf("serve 的默认步数（%d）没有原样转发给子进程：%v", defaultTurnMaxSteps, args)
	}
}
