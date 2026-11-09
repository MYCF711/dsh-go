package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

// runIDRecorder 是只记录"这次工具调用拿到的 ctx 是谁"的假 Runner。
//
// 它不实现任何工具语义：本文件的被测对象是**循环往 ctx 上放了什么**。
type runIDRecorder struct {
	mu       sync.Mutex
	runIDs   []string
	sessions []string
	depths   []int
	runners  []spi.Runner
}

func (r *runIDRecorder) Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
	sub := spi.SubDispatchFrom(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runIDs = append(r.runIDs, spi.RunIDFrom(ctx))
	r.sessions = append(r.sessions, spi.SessionFrom(ctx))
	r.depths = append(r.depths, sub.Depth)
	r.runners = append(r.runners, sub.Runner)
	return spi.Result{Summary: "ok"}, nil
}

func (r *runIDRecorder) snapshot() (runIDs, sessions []string, depths []int, runners []spi.Runner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.runIDs...), append([]string(nil), r.sessions...),
		append([]int(nil), r.depths...), append([]spi.Runner(nil), r.runners...)
}

// toolScript 造一个"同一参数连调两次 + 一句收尾"的一轮脚本。
//
// 两次是故意的：run id 只要在**同一轮**里变了，repeat-tool-reminder 的链就会被
// 拆成两条，官方阈值 3/5/8 永远数不到。
func toolScript() *scriptedChat {
	return &scriptedChat{steps: []*llm.Response{
		{ToolCalls: []llm.ToolCall{
			{ID: "c1", Type: "function", Function: llm.Func{Name: "grep", Arguments: `{"pattern":"x"}`}},
			{ID: "c2", Type: "function", Function: llm.Func{Name: "grep", Arguments: `{"pattern":"x"}`}},
		}, FinishReason: "tool_calls"},
		{Content: "好了", FinishReason: "stop"},
	}}
}

// 一轮 = 一个 turn，而 turn 是**工具链的分段边界**：同 turn 稳定、跨 turn 唯一。
//
// 红：Loop.Run 从来没往 ctx 上放过 run id（`spi.WithRunID` 全仓零调用），
// 于是 repeat-tool-reminder 的链跨轮累计——新的一轮里同一个调用数到第 3 次
// 不再提醒（计数已经从上一轮接着走）。
func TestRunIDIsPerTurnUniqueAndStableWithinTurn(t *testing.T) {
	rec := &runIDRecorder{}
	loop := &Loop{Chat: toolScript(), Runner: rec, Model: "m", SessionID: "sess-A", MaxSteps: 4}

	if _, err := loop.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	loop.Chat = toolScript()
	if _, err := loop.Run(context.Background(), 2, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("第二轮: %v", err)
	}

	runIDs, sessions, _, _ := rec.snapshot()
	if len(runIDs) != 4 {
		t.Fatalf("两轮各两次工具调用，应记录 4 条，得到 %d 条: %v", len(runIDs), runIDs)
	}
	for i, id := range runIDs {
		if id == "" {
			t.Fatalf("第 %d 次工具调用的 ctx 上没有 run id：repeat-tool-reminder 只能跨轮累计", i+1)
		}
	}
	// 同 turn 稳定：一轮里所有调用的 run id 必须完全相同。
	if runIDs[0] != runIDs[1] {
		t.Fatalf("同一轮里 run id 变了（%q vs %q）：链会被拆开，阈值永远数不到", runIDs[0], runIDs[1])
	}
	// 每 turn 唯一：第二轮必须换一个 id，否则计数会接着上一轮走。
	if runIDs[2] != runIDs[3] {
		t.Fatalf("第二轮里 run id 变了（%q vs %q）", runIDs[2], runIDs[3])
	}
	if runIDs[0] == runIDs[2] {
		t.Fatalf("两个 turn 的 run id 相同（%q）：新一轮的计数不会从 1 重新数", runIDs[0])
	}
	for i, s := range sessions {
		if s != "sess-A" {
			t.Fatalf("第 %d 次调用丢了会话 id：%q", i+1, s)
		}
	}
}

// A/B 两个会话即使 turn 序号相同，run id 也必须不同：谁都不能串到对方的链上。
func TestRunIDIsolatedPerSession(t *testing.T) {
	recA := &runIDRecorder{}
	recB := &runIDRecorder{}
	loopA := &Loop{Chat: toolScript(), Runner: recA, Model: "m", SessionID: "sess-A", MaxSteps: 4}
	loopB := &Loop{Chat: toolScript(), Runner: recB, Model: "m", SessionID: "sess-B", MaxSteps: 4}

	if _, err := loopA.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("A: %v", err)
	}
	if _, err := loopB.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("B: %v", err)
	}

	idsA, sessA, _, _ := recA.snapshot()
	idsB, sessB, _, _ := recB.snapshot()
	if len(idsA) == 0 || len(idsB) == 0 {
		t.Fatalf("没记录到调用：A=%v B=%v", idsA, idsB)
	}
	if sessA[0] != "sess-A" || sessB[0] != "sess-B" {
		t.Fatalf("会话隔离错了：A=%q B=%q", sessA[0], sessB[0])
	}
	if idsA[0] == idsB[0] {
		t.Fatalf("A/B 两个会话在同一个 turn 上拿到了同一个 run id（%q）", idsA[0])
	}
}

// 宿主每轮放的**子调用通道**（workflow/team 的 runner + depth）不能被 run id 挤掉。
//
// 红：两者共用 ctxKey(1)，Loop 一放 run id，guardedRunner 放的 SubDispatch 就没了——
// 子调用的 Depth 归零（被当成顶层调用），workflow 也拿不到 runner。
func TestRunIDBindingKeepsSubDispatch(t *testing.T) {
	rec := &runIDRecorder{}
	// 生产形状：宿主在进 Loop 之前已经放了受管子调用通道（Depth>=1 = 子代理/子调用）。
	base := spi.WithSubDispatch(context.Background(), spi.SubDispatch{Runner: rec, Depth: 2})

	loop := &Loop{Chat: toolScript(), Runner: rec, Model: "m", SessionID: "sess-sub", MaxSteps: 4}
	if _, err := loop.Run(base, 1, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("%v", err)
	}

	runIDs, _, depths, runners := rec.snapshot()
	if len(runIDs) == 0 {
		t.Fatal("没记录到调用")
	}
	for i := range runIDs {
		if runIDs[i] == "" {
			t.Fatalf("第 %d 次调用没有 run id", i+1)
		}
		if depths[i] != 2 {
			t.Fatalf("第 %d 次调用的嵌套深度丢了：%d（期望 2）——子调用会被当成顶层调用计数", i+1, depths[i])
		}
		if runners[i] != spi.Runner(rec) {
			t.Fatalf("第 %d 次调用的子调用 Runner 丢了：%#v", i+1, runners[i])
		}
	}
}
