package main

import (
	"context"
	"strings"
	"testing"

	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// 这一组钉住子代理继承的**文本出口**：`subagent_fork` 拿给子代理第一次模型调用的
// 是父会话**已完成轮次**的转写（结构化的那份落盘子会话日志，见
// `subagent_seed_fork_test.go`）。两个出口同源（`readSubagentSeed` 一次取数）。
//
// 判据是官方的 `completedTurnPrefix`（`dsh-subagent-fork-in-process/src/index.ts:46-53`）：
// 种子止于父会话**最后一条 `turn/end`**。in-flight 那一轮没有 `turn/end`，
// 所以天然不在里面；首轮完成之前种子为空、等同 spawn。
//
// 2026-11-04 的 `serve_turn.go` 修过同一个坑（`m.Text == ""` 丢掉只有 tool_calls
// 的 assistant），但继承这条路原来还留着原样的判断。它的症状比 serve 那边更难发现：
// 子代理不会报错，只是"不知道父会话干了什么"，然后重新做一遍已经做过的事。

// 判据（任务书写死的三条）：只有 tool_calls 的 assistant、只有 reasoning 的
// assistant，两条都必须出现在转写里；三样都没有的那条才算空。
//
// 2026-11-14 改写：事件按**真实轮次**铺（`turn/start` … `turn/end`），因为种子现在
// 按官方"已完成轮次前缀"切——一份没有 `turn/end` 的日志根本没有已完成轮次；
// 没有轮次边界的历史不属于"种子"，而是"正在跑的那一轮"，不该进来。
func TestSubagentInheritKeepsToolCallsAndReasoning(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-inherit"

	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		// —— 第 1 轮：user + 只有工具调用的 assistant + tool/result ——
		// 正文里带换行：转写必须一条消息一行（见下面对行的断言）。
		if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
			return err
		}
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: "看看目录里有几个文件\n顺便说一下大小"},
		})); err != nil {
			return err
		}
		// 只有工具调用、没有正文：模型直接开干时最常见的一种回答。
		if err := lg.Append("assistant/message", session.AssistantMessageEvent(1, 1,
			[]map[string]any{session.ToolCallBlock("call_1", "bash", `{"command":"ls"}`)},
			"tool_calls", nil)); err != nil {
			return err
		}
		if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, "call_1",
			[]spi.Block{{Type: "text", Text: "68 个文件"}}, false, cwd, nil)); err != nil {
			return err
		}
		if err := lg.Append("turn/end", session.TurnEndData(1, "completed", "")); err != nil {
			return err
		}

		// —— 第 2 轮：只有思考块的 assistant + 真空的那条 ——
		if err := lg.Append("turn/start", session.TurnStartData(2)); err != nil {
			return err
		}
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: "那你打算怎么算"},
		})); err != nil {
			return err
		}
		// 只有思考块、没有正文：模型想了、只是没输出正文。
		if err := lg.Append("assistant/message", session.AssistantMessageEvent(2, 1,
			[]map[string]any{session.ReasoningBlock("先想一下：用户要的是文件数")},
			"stop", nil)); err != nil {
			return err
		}
		// 真空：无正文、无思考块、无 tool_calls。三条里只有它该被跳过。
		if err := lg.Append("assistant/message", session.AssistantMessageEvent(2, 2,
			[]map[string]any{{"type": "usage-note", "text": ""}}, "stop", nil)); err != nil {
			return err
		}
		return lg.Append("turn/end", session.TurnEndData(2, "completed", ""))
	})

	got := inheritFromCtx(home)(spi.WithSession(context.Background(), sid))
	t.Logf("转写：\n%s", got)

	if !strings.Contains(got, "看看目录里有几个文件 顺便说一下大小") {
		t.Fatalf("用户消息没进转写（或多行正文没压成一行）：\n%s", got)
	}
	// 判据一：只有 tool_calls 的 assistant 必须出现，且调用要变成一句人话。
	if !strings.Contains(got, `[调用 bash：{"command":"ls"}]`) {
		t.Fatalf("只有 tool_calls 的 assistant 丢了（子代理会以为父会话没查过目录）：\n%s", got)
	}
	// 判据二：只有 reasoning 的 assistant 必须出现。
	if !strings.Contains(got, "先想一下：用户要的是文件数") {
		t.Fatalf("只有 reasoning 的 assistant 丢了：\n%s", got)
	}
	// 判据三：真空那条才算空——assistant 恰好两条，不是三条、也不是一条。
	if n := strings.Count(got, "assistant: "); n != 2 {
		t.Fatalf("assistant 行数 = %d，要 2（tool_calls 那条 + reasoning 那条；真空那条该跳过）：\n%s", n, got)
	}
	if !strings.Contains(got, "tool result: 68 个文件") {
		t.Fatalf("工具结果丢了：\n%s", got)
	}
	// 轮次边界是结构：两轮各有一个 `[轮次 N]` 头（官方种子按"已完成的轮次"切）。
	if n := strings.Count(got, "[轮次 "); n != 2 {
		t.Fatalf("轮次头 = %d，要 2（第 1 轮与第 2 轮各一个）：\n%s", n, got)
	}

	// 一条消息一行：子代理结果里的「继承 N 条历史」按非空行数算（inheritNote）。
	lines := nonEmptyLines(got)
	// 精确排布：轮次头 1、user、assistant+调用、tool、轮次头 2、user、assistant+思考 = 7 行。
	if len(lines) != 7 {
		t.Fatalf("转写有 %d 行，要 7 行（2 轮次头 + user/assistant+调用/tool + user/assistant+思考）：\n%s\n%v",
			len(lines), got, lines)
	}
	wantPrefixes := []string{
		"[轮次 1]", "user: ", "assistant: ", "tool result: ",
		"[轮次 2]", "user: ", "assistant: ",
	}
	for i, want := range wantPrefixes {
		if !strings.HasPrefix(lines[i], want) {
			t.Fatalf("第 %d 行应以 %q 开头，实得 %q（inheritNote 与模型都按这个读）", i+1, want, lines[i])
		}
	}
}

// 没有父会话（ctx 里没有会话 id）时返回空串，而不是一段空转写。
// 空转写的后果是结果摘要说"继承 0 条"，与"读不到"分不开——前者像正常，后者要能被看出来。
func TestSubagentInheritWithoutSession(t *testing.T) {
	if got := inheritFromCtx(t.TempDir())(context.Background()); got != "" {
		t.Fatalf("没有会话时应当返回空串，得到 %q", got)
	}
}

// 首轮完成之前 fork：种子为空，子代理等同全新 spawn。
//
// 官方原话（`dsh-subagent-fork-in-process/src/index.ts:41`）：
// "before any completed turn the child starts fresh"；
// `README.md:36`："before the first completed turn the seed is empty and the child
// behaves like a fresh spawn"。
func TestSubagentInheritEmptyBeforeFirstCompletedTurn(t *testing.T) {
	home := t.TempDir()
	const sid = "session-inflight-first"

	seedTurnSession(t, home, t.TempDir(), sid, func(lg *session.Log) error {
		// 第 1 轮正在跑：user 已落、assistant 已落，但还没有 turn/end。
		if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
			return err
		}
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: "正在跑的那一轮"},
		})); err != nil {
			return err
		}
		return lg.Append("assistant/message", session.AssistantMessageEvent(1, 1,
			[]map[string]any{session.TextBlock("这一轮还没结算")}, "stop", nil))
	})

	if got := inheritFromCtx(home)(spi.WithSession(context.Background(), sid)); got != "" {
		t.Fatalf("首轮完成之前种子必须为空（等同 spawn），得到：\n%s", got)
	}
}

// in-flight 那一轮不出现在转写里：父会话跑完第 1 轮、第 2 轮正在跑 → 只有第 1 轮。
//
// 官方原话（`README.md:36`）："the child sees every finished turn and none of the
// in-flight one"。
func TestSubagentInheritExcludesInFlightTurn(t *testing.T) {
	home := t.TempDir()
	const sid = "session-inflight-second"
	const done = "这一轮已经结算"
	const inflight = "这一轮还在跑"

	seedTurnSession(t, home, t.TempDir(), sid, func(lg *session.Log) error {
		if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
			return err
		}
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: done}})); err != nil {
			return err
		}
		if err := lg.Append("assistant/message", session.AssistantMessageEvent(1, 1,
			[]map[string]any{session.TextBlock("收到")}, "stop", nil)); err != nil {
			return err
		}
		if err := lg.Append("turn/end", session.TurnEndData(1, "completed", "")); err != nil {
			return err
		}
		// 第 2 轮：只有 turn/start 与 user，没有 turn/end（= 正在跑）。
		if err := lg.Append("turn/start", session.TurnStartData(2)); err != nil {
			return err
		}
		return lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: inflight}}))
	})

	got := inheritFromCtx(home)(spi.WithSession(context.Background(), sid))
	if !strings.Contains(got, done) {
		t.Fatalf("已完成轮次没进转写：\n%s", got)
	}
	if strings.Contains(got, inflight) {
		t.Fatalf("in-flight 轮次进了转写（子代理会看到正在跑的事）：\n%s", got)
	}
	if strings.Contains(got, "[轮次 2]") {
		t.Fatalf("in-flight 轮次头进了转写：\n%s", got)
	}
}

func nonEmptyLines(s string) []string {
	out := make([]string, 0, 8)
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
