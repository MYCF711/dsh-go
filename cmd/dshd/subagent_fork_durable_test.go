package main

// base 插件 #70 `subagent-fork-in-process` / #73 `tool-subagent-fork` 的**最后一段缺口**：
// 一次性同步 fork（`Continuable=false`）也必须留一条 durable 子会话。
//
// 官方契约（`@deepseek-ai/dsh-subagent-fork-in-process/README.md`，rc.2 解包树）：
//
//	"One tool call creates a background child ... **The child works in its own
//	 session**; the parent receives its final answer in a completion notice."
//	"this backend supplies ... the parent log as the child session seed"
//	`src/index.ts:72-78`：种子在创建时取一次，之后 "becomes part of the child's own
//	 durable transcript"。
//	"Seed boundary"："before the first completed turn the seed is empty and the child
//	 behaves like a fresh spawn."
//
// 落到宿主上：`req.Seed` 非空 = 这次 fork 真的继承了父会话的已完成轮次，那这一趟
// **必须**有一条子会话把那批事件写进自己的 v4 日志；`req.Seed` 为空 = 首轮完成前的
// fork，官方说它等同 spawn，那就不该凭空多出一条会话（既有行为一个字节都不变）。
//
// 全程真 v4 文件、真 `subRunner`、真 `ChildSpawn`：不留会话这件事的唯一证据就是盘上。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/spi"
)

// TestOneShotForkKeepsDurableChildSession 是主用例：不设 Continuable 的 fork
// （就是 `subagent_fork` 工具的真实请求形状）跑完之后，盘上多出一条子会话，
// 它的日志 = 父子帧 + 父会话种子 + 这一趟的提问与回答。
func TestOneShotForkKeepsDurableChildSession(t *testing.T) {
	home := t.TempDir()
	const parent = "parent-fork-durable"
	runSeededParentTurns(t, home, home, parent)

	before := mustSessionIDs(t, home)
	r := newForkSubRunner(t, home, "子代理结论：一共 68 个文件")
	ctx := spi.WithSession(context.Background(), parent)
	seed := readSubagentSeed(home, ctx)
	if seed.Text == "" {
		t.Fatal("父会话有两轮已完成却取不到种子：先看 completedTurnPrefix 判据")
	}
	events := mustWireSeed(t, seed)
	if len(events) == 0 {
		t.Fatal("种子事件为空：fork 的 durable 出口没东西可写")
	}

	res, err := r.RunSub(ctx, spi.SubagentRequest{
		Prompt:      "接着算大小",
		Description: "接着算",
		Inherit:     seed.Text,
		Seed:        events,
		// **故意不设 Continuable**：这就是"一次性同步 fork"的形状。
	})
	if err != nil {
		t.Fatalf("一次性 fork 委派失败: %v", err)
	}

	// 1. 盘上真的多了一条子会话（而不是只回了一个 id）。
	after := mustSessionIDs(t, home)
	if len(after) != len(before)+1 {
		t.Fatalf("一次性 fork 应新增恰好一条子会话：before=%v after=%v", before, after)
	}
	if res.ChildSessionID == "" {
		t.Fatal("一次性 fork 没回 child id：子会话没建成却当成成功")
	}
	childPath := mustFind(t, home, res.ChildSessionID)
	child := readLogAt(t, childPath)

	// 2. 第一条是父子帧：归属（授权边界）必须落在盘上。
	if len(child.Events) == 0 || child.Events[0].Type != "subagent/spawn" {
		t.Fatalf("子会话第一条事件不是父子帧（%v）", eventTypes(child.Events))
	}
	var frame struct {
		ChildSessionID  string `json:"childSessionId"`
		ParentSessionID string `json:"parentSessionId"`
	}
	if err := json.Unmarshal(child.Events[0].Data, &frame); err != nil {
		t.Fatalf("父子帧解析失败: %v", err)
	}
	if frame.ChildSessionID != res.ChildSessionID || frame.ParentSessionID != parent {
		t.Fatalf("父子帧归属不对：%+v（child 应为 %q，parent 应为 %q）",
			frame, res.ChildSessionID, parent)
	}

	// 3. 种子逐条、按原顺序落进子会话日志（类型序列逐条相等）。
	if len(child.Events) < len(events)+1 {
		t.Fatalf("子会话事件数 %d < 父子帧 + 种子 %d", len(child.Events), len(events)+1)
	}
	seeded := child.Events[1 : 1+len(events)]
	for i, raw := range events {
		var ev v4.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("种子第 %d 条解析失败: %v", i, err)
		}
		if seeded[i].Type != ev.Type {
			t.Fatalf("种子第 %d 条类型漂了：子会话 %q / 父会话 %q", i, seeded[i].Type, ev.Type)
		}
	}
	// 种子只搬对话事件：子会话里不该出现任何 turn/* 边界（那会伪造轮次）。
	for _, ev := range seeded {
		if strings.HasPrefix(ev.Type, "turn/") {
			t.Fatalf("种子把轮次边界也搬进来了（%q）——子会话不伪造轮次", ev.Type)
		}
	}

	// 4. 这一趟的提问与回答在种子之后（历史顺序：来历 → 继承来的过去 → 我这一趟）。
	tail := child.Events[1+len(events):]
	if len(tail) != 2 || tail[0].Type != "user/message" || tail[1].Type != "assistant/message" {
		t.Fatalf("种子之后应恰好是一条提问 + 一条回答，实际 %v", eventTypes(tail))
	}
	if !strings.Contains(string(tail[0].Data), "接着算大小") {
		t.Fatalf("子会话里没有这一趟的提问正文: %s", tail[0].Data)
	}
	if !strings.Contains(string(tail[1].Data), "子代理结论：一共 68 个文件") {
		t.Fatalf("子会话里没有这一趟的回答正文: %s", tail[1].Data)
	}
}

// TestOneShotForkWithoutSeedStaysEphemeral：空种子 = 首轮完成之前的 fork，官方说
// 它 "behaves like a fresh spawn"——不留会话（既有行为逐字节不变）。
func TestOneShotForkWithoutSeedStaysEphemeral(t *testing.T) {
	home := t.TempDir()
	r := newForkSubRunner(t, home, "做完了")
	ctx := spi.WithSession(context.Background(), "parent-no-seed")

	res, err := r.RunSub(ctx, spi.SubagentRequest{
		Prompt: "回答：做完了", Description: "一次性",
	})
	if err != nil {
		t.Fatalf("无种子 fork 委派失败: %v", err)
	}
	if res.ChildSessionID != "" {
		t.Fatalf("空种子（= 首轮前的 fork）却建了子会话: %q", res.ChildSessionID)
	}
	if ids := mustSessionIDs(t, home); len(ids) != 0 {
		t.Fatalf("空种子 fork 不该在盘上留会话，实际 %v", ids)
	}
}

// TestOneShotForkWithoutHomeFailsLoudlyInsteadOfDroppingSeed：种子落不了盘时必须
// **响亮失败**——静默成功等于把"父会话的历史"从子代理的 durable transcript 里删掉。
func TestOneShotForkWithoutHomeFailsLoudlyInsteadOfDroppingSeed(t *testing.T) {
	home := t.TempDir()
	const parent = "parent-fork-no-home"
	runSeededParentTurns(t, home, home, parent)
	ctx := spi.WithSession(context.Background(), parent)
	events := mustWireSeed(t, readSubagentSeed(home, ctx))
	if len(events) == 0 {
		t.Fatal("种子事件为空：先看 completedTurnPrefix 判据")
	}

	r := newForkSubRunner(t, home, "结论")
	r.home = "" // 装配里没有 home = 种子落不了盘
	_, err := r.RunSub(ctx, spi.SubagentRequest{
		Prompt: "接着算大小", Description: "接着算", Seed: events,
	})
	if err == nil {
		t.Fatal("home 为空却当成功返回：fork 种子被静默丢掉了")
	}
	if !strings.Contains(err.Error(), "fork 种子") {
		t.Fatalf("错误没说明丢的是 fork 种子: %v", err)
	}
}

// eventTypes 是断言输出用的类型序列。
func eventTypes(events []v4.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}
