package main

// 这一组钉住 base 插件 #70 `subagent-fork-in-process` / #73 `tool-subagent-fork` 的
// **结构化种子**：fork 出来的子会话日志里，逐轮追加的是父会话**已完成轮次**的真实
// user/assistant/tool 事件；in-flight 那一轮不在里面；fork 之后父会话再长也不进来。
//
// 官方契约逐条对应：
//   - `dsh-subagent-fork-in-process/src/index.ts:46-53` `completedTurnPrefix`：
//     种子 = 最后一条 `turn/end` 为止的前缀（in-flight 天然被排除）；
//   - 同文件 `:72-78`：种子创建时取一次，之后成为子会话自己的 durable transcript；
//   - `README.md:36`（"Seed boundary"）："the child sees every finished turn and none of
//     the in-flight one" / "The seed is a one-time snapshot taken at fork time: later parent
//     turns never reach the child." / "before the first completed turn the seed is empty
//     and the child behaves like a fresh spawn."；
//   - `README.md:32`：种子只带对话历史（工具作用域与授权不跟着走）。
//
// 全程真 v4 文件、真 `subRunner`、真 `ChildSpawn`：不 mock 会话层，因为"结构化种子"
// 这件事的**唯一证据**就是子会话那份日志里的事件类型与块形状。

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// forkSeedChat 是"父会话两轮 + 一轮 in-flight"的假模型。
//
// 它按调用次数产出可判别的正文，用来证明"哪一轮进了子会话、哪一轮没进"：
//  1. 第 1 轮第 1 步：只有工具调用（没有正文）——旧实现最容易丢掉的那一种；
//  2. 第 1 轮第 2 步：正文 + 思考块 → 这一轮结算；
//  3. 第 2 轮：正文 → 结算；
//  4. 第 3 轮：正文 → **这一轮模拟 in-flight**（调用方不写 turn/end）。
type forkSeedChat struct {
	calls int
}

func (c *forkSeedChat) Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error) {
	c.calls++
	switch c.calls {
	case 1:
		return &llm.Response{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{{
				ID: "call-seed-1", Type: "function",
				Function: llm.Func{Name: "read", Arguments: `{"path":"a.txt"}`},
			}},
		}, nil
	case 2:
		return &llm.Response{Content: "第 1 轮结论：只有 68 个文件",
			Reasoning: "先想一下：用户要的是文件数", FinishReason: "stop"}, nil
	case 3:
		return &llm.Response{Content: "第 2 轮结论：那就先算大小", FinishReason: "stop"}, nil
	default:
		return &llm.Response{Content: "第 3 轮结论：这一轮还在跑", FinishReason: "stop"}, nil
	}
}

// forkSeedRunner 让第 1 轮第 1 步真的执行成功（真工具结果落盘）。
type forkSeedRunner struct{}

func (forkSeedRunner) Run(context.Context, string, json.RawMessage) (spi.Result, error) {
	return spi.Result{Summary: "68 个文件"}, nil
}

// runSeededParentTurns 用真 agent.Loop + 真 v4 落盘，把父会话跑到"第 3 轮 in-flight"。
//
// 写盘形状与 `cmd/dshd/serve_turn.go:405-480` 的 `persistModelEvents` 一致：
// turn/start → user/message（**由调用方写**，Loop 只为 steer 发 user 事件）→
// assistant/message（块序 思考→正文→调用）→ tool/result → turn/end。
// 第 3 轮**不写** turn/end —— 那就是 in-flight。
func runSeededParentTurns(t *testing.T, home, cwd, parent string) {
	t.Helper()
	lg, err := session.CreateSession(home, cwd, parent, "default")
	if err != nil {
		t.Fatalf("建父会话: %v", err)
	}
	state := &parentLogWriter{lg: lg, cwd: cwd}
	loop := &agent.Loop{
		Chat:     &forkSeedChat{},
		Runner:   forkSeedRunner{},
		Model:    "subagent-seed-test-model",
		MaxSteps: 2,
		Emit:     state.emit,
	}
	for i, prompt := range []string{"数一下目录里有几个文件", "那你打算怎么算", "顺便看一眼大小"} {
		state.turn = i + 1
		if err := lg.Append("turn/start", session.TurnStartData(state.turn)); err != nil {
			lg.Close()
			t.Fatalf("写第 %d 轮 turn/start: %v", state.turn, err)
		}
		state.closer = newTurnCloser(lg, state.turn)
		// 真实入口把这一轮的用户输入**先**落盘，再跑循环（见 persistModelEvents）。
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: prompt}})); err != nil {
			lg.Close()
			t.Fatalf("写第 %d 轮 user: %v", state.turn, err)
		}
		if _, err := loop.Run(context.Background(), state.turn, []llm.Message{{Role: "user", Content: prompt}}); err != nil {
			lg.Close()
			t.Fatalf("跑父会话第 %d 轮: %v", state.turn, err)
		}
	}
	// 第 3 轮：in-flight，故意不 End。
	if err := lg.Close(); err != nil {
		t.Fatalf("关父会话: %v", err)
	}

	// 先自证父日志真的是"2 轮已结算 + 1 轮 in-flight"：后面的断言才有意义。
	path := mustFind(t, home, parent)
	back := readLogAt(t, path)
	starts, ends := 0, 0
	for _, ev := range back.Events {
		switch ev.Type {
		case "turn/start":
			starts++
		case "turn/end":
			ends++
		}
	}
	if starts != 3 || ends != 2 {
		t.Fatalf("父会话轮次基底不对：turn/start=%d turn/end=%d，要 3 与 2（第 3 轮 in-flight）", starts, ends)
	}
}

// parentLogWriter 是父会话的写盘器（真 Loop 的 Emit → 真 v4 事件）。
type parentLogWriter struct {
	lg     *session.Log
	cwd    string
	turn   int
	closer *turnCloser
}

func (w *parentLogWriter) emit(ev agent.Event) error {
	switch ev.Kind {
	case agent.EventUserMsg:
		// Loop 只为 steer 消息发这个事件（初始 prompt 由调用方落盘，见 runSeededParentTurns）。
		// steer 会往**同一轮**里再插一条 user，那与 serve 的真实样子一致，照写。
		if ev.User == nil {
			return nil
		}
		return w.lg.Append("user/message",
			session.UserMessage([]spi.Block{{Type: "text", Text: ev.User.Message.Content}}))
	case agent.EventAssistantMsg:
		if ev.Assistant == nil {
			return nil
		}
		blocks := assistantBlocksFrom(ev.Assistant.Content, ev.Assistant.Reasoning, ev.Assistant.ToolCalls)
		return w.lg.Append("assistant/message",
			session.AssistantMessageEvent(w.turn, ev.Step, blocks, ev.Assistant.Finish, nil))
	case agent.EventToolResult:
		if ev.Result == nil {
			return nil
		}
		return w.lg.Append("tool/result", session.ToolResultEvent(w.turn, ev.Step,
			ev.CallID, []spi.Block{{Type: "text", Text: ev.Result.Summary}}, ev.IsError, w.cwd, nil))
	case agent.EventTurnEnd:
		// 第 3 轮 in-flight：不结算。
		if w.turn >= 3 || w.closer == nil {
			return nil
		}
		return w.closer.End(ev.Finish)
	}
	return nil
}

// newForkSubRunner 造一个真的 subRunner（真 home、真落盘）。
func newForkSubRunner(t *testing.T, home string, reply string) *subRunner {
	t.Helper()
	return &subRunner{
		chat:     subagentFakeChat{reply: reply},
		tools:    subagentEmptyRegistry{},
		model:    "test-model",
		maxSteps: 2,
		timeout:  10 * time.Second,
		home:     home,
	}
}

// 主用例：父会话 2 轮已完成 + 第 3 轮 in-flight → fork。
//
// 三条必须同时成立：
//  1. 子会话日志里逐轮出现父会话的 user/assistant/tool 真实事件（按事件类型与块形状断言，
//     不是"含某段文字"）；
//  2. 子会话里**没有**第 3 轮的任何内容；
//  3. fork 之后父会话再跑第 4 轮 → 子会话日志**不变**（一次性快照）。
func TestSubagentForkSeedIsStructuredCompletedTurns(t *testing.T) {
	home := t.TempDir()
	const parent = "parent-fork-seed"
	runSeededParentTurns(t, home, home, parent)

	r := newForkSubRunner(t, home, "子代理结论")
	ctx := spi.WithSession(context.Background(), parent)
	seed := readSubagentSeed(home, ctx)
	if seed.Text == "" {
		t.Fatal("已完成两轮却取不到文本种子：先看 turn/end 判据")
	}

	res, err := r.RunSub(ctx, spi.SubagentRequest{
		Prompt:         "接着算大小",
		Description:    "接着算",
		Continuable:    true,
		Inherit:        seed.Text,
		Seed:           mustWireSeed(t, seed),
		ChildSessionID: "child-fork-seed",
	})
	if err != nil {
		t.Fatalf("fork 委派失败: %v", err)
	}
	if res.ChildSessionID != "child-fork-seed" {
		t.Fatalf("子会话 id 不对: %q", res.ChildSessionID)
	}

	childPath := mustFind(t, home, "child-fork-seed")
	child := readLogAt(t, childPath)

	// 1. 结构化：按事件类型数，并逐条看块形状。
	var users, assistants, tools int
	var sawReasoningBlock, sawToolCallBlock bool
	for _, ev := range child.Events {
		switch ev.Type {
		case "user/message":
			users++
		case "assistant/message":
			assistants++
			for _, b := range contentBlocksOf(t, ev.Data) {
				switch b.Type {
				case "reasoning":
					sawReasoningBlock = true
				case "tool-call":
					sawToolCallBlock = true
				}
			}
		case "tool/result":
			tools++
		}
	}
	// 父已完成两轮的真实往返：user×2、assistant×3（第 1 轮两步 + 第 2 轮一步）、tool×1；
	// 再加这一趟委派自己的 user（prompt）+ assistant（answer）：
	//   user = 2+1，assistant = 3+1，tool = 1。
	if users != 3 {
		t.Fatalf("子会话 user/message = %d，要 3（父 2 轮 + 这一趟的 prompt）", users)
	}
	if assistants != 4 {
		t.Fatalf("子会话 assistant/message = %d，要 4（父 3 条 + 这一趟的 answer）", assistants)
	}
	if tools != 1 {
		t.Fatalf("子会话 tool/result = %d，要 1（父会话唯一的工具结果）", tools)
	}
	if !sawReasoningBlock {
		t.Fatal("子会话里没有任何 reasoning 块：父会话的思考在种子里丢了（结构化种子不许退回纯文本）")
	}
	if !sawToolCallBlock {
		t.Fatal("子会话里没有任何 tool-call 块：父会话只有工具调用的那一轮在种子里丢了")
	}

	// 帧顺序：父子帧 → 种子 → 这一趟委派。
	if child.Events[0].Type != agent.EventChildSpawn {
		t.Fatalf("第一帧必须是父子帧，得到 %s", child.Events[0].Type)
	}
	last := child.Events[len(child.Events)-1]
	if last.Type != "assistant/message" || !strings.Contains(string(last.Data), "子代理结论") {
		t.Fatalf("最后一帧必须是这一趟委派的回答，得到 %s %s", last.Type, last.Data)
	}

	// 2. in-flight 那一轮不在子会话里。
	for _, ev := range child.Events {
		raw := string(ev.Data)
		if strings.Contains(raw, "第 3 轮结论") || strings.Contains(raw, "顺便看一眼大小") {
			t.Fatalf("in-flight 轮次进了子会话：type=%s data=%s", ev.Type, raw)
		}
	}

	// 3. 一次性快照：父会话再跑第 4 轮，子会话日志一个字节都不变。
	before := readFileBytes(t, childPath)
	appendParentTurns(t, home, parent, 1)
	after := readFileBytes(t, childPath)
	if string(before) != string(after) {
		t.Fatal("fork 之后父会话又跑了一轮，子会话日志跟着变了（种子必须是一次性快照）")
	}
}

// 首轮完成之前 fork：子会话种子为空，与全新 spawn 的形状一致。
func TestSubagentForkSeedEmptyBeforeFirstCompletedTurn(t *testing.T) {
	home := t.TempDir()
	const parent = "parent-fork-inflight-first"
	// 只有 in-flight 的第 1 轮（没有任何 turn/end）。
	seedTurnSession(t, home, home, parent, func(lg *session.Log) error {
		if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
			return err
		}
		return lg.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: "正在跑的第一轮"},
		}))
	})

	r := newForkSubRunner(t, home, "子代理结论")
	ctx := spi.WithSession(context.Background(), parent)
	seed := readSubagentSeed(home, ctx)
	if len(seed.Turns) != 0 || len(seed.Events) != 0 || seed.Text != "" {
		t.Fatalf("首轮完成之前种子必须为空，实得 %+v", seed)
	}

	if _, err := r.RunSub(ctx, spi.SubagentRequest{
		Prompt:         "干点别的",
		Description:    "干点别的",
		Continuable:    true,
		Inherit:        seed.Text,
		Seed:           mustWireSeed(t, seed),
		ChildSessionID: "child-empty-seed",
	}); err != nil {
		t.Fatalf("fork 委派失败: %v", err)
	}

	child := readLogAt(t, mustFind(t, home, "child-empty-seed"))
	// 子会话 = 父子帧 + 这一趟的 user + assistant，**没有**父会话的任何东西。
	var types []string
	for _, ev := range child.Events {
		types = append(types, ev.Type)
		if strings.Contains(string(ev.Data), "正在跑的第一轮") {
			t.Fatalf("in-flight 的第一轮进了子会话：%s", ev.Data)
		}
	}
	want := []string{agent.EventChildSpawn, "user/message", "assistant/message"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("首轮前 fork 的子会话事件序列 = %v，要 %v（等同 spawn）", types, want)
	}
}

// 结构化种子的内容与父会话逐条对得上：不是"重新编一段摘要"。
func TestSubagentForkSeedCarriesParentEventPayloads(t *testing.T) {
	home := t.TempDir()
	const parent = "parent-fork-payload"
	runSeededParentTurns(t, home, home, parent)

	seed := readSubagentSeed(home, spi.WithSession(context.Background(), parent))
	if len(seed.Turns) != 2 {
		t.Fatalf("取到 %d 轮，要 2（父会话已完成两轮）", len(seed.Turns))
	}
	// 第 1 轮：user + 只有工具调用的 assistant + tool/result + 带思考的收尾 assistant
	// （模型第 1 轮走了两步：先调工具，再给结论——真实多步轮次就该整轮都在）。
	if got := len(seed.Turns[0].Items); got != 4 {
		t.Fatalf("第 1 轮搬了 %d 条事件，要 4（user/assistant+调用/tool/assistant+思考）", got)
	}
	if seed.Turns[0].Items[1].Event.Type != "assistant/message" {
		t.Fatalf("第 1 轮第 2 条不是 assistant/message：%s", seed.Turns[0].Items[1].Event.Type)
	}
	foundCall := false
	for _, b := range contentBlocksOf(t, seed.Turns[0].Items[1].Event.Data) {
		if b.Type == "tool-call" && b.ID == "call-seed-1" && b.Name == "read" {
			foundCall = true
		}
	}
	if !foundCall {
		t.Fatal("第 1 轮只有工具调用的那条 assistant 没有原样带过来（块 id/name 对不上）")
	}
	// reasoning 块在**第 1 轮第 2 条 assistant**（收尾那一步想了一下）。
	foundReasoning := false
	for _, b := range contentBlocksOf(t, seed.Turns[0].Items[3].Event.Data) {
		if b.Type == "reasoning" && strings.Contains(b.Text, "先想一下") {
			foundReasoning = true
		}
	}
	if !foundReasoning {
		t.Fatal("收尾那一步的 reasoning 块没有原样带过来")
	}
	// 第 2 轮：user + assistant。
	if got := len(seed.Turns[1].Items); got != 2 {
		t.Fatalf("第 2 轮搬了 %d 条事件，要 2（user/assistant）", got)
	}
	// 轮次号来自父日志的 turn/end（不是编的）。
	if seed.Turns[0].Number != 1 || seed.Turns[1].Number != 2 {
		t.Fatalf("轮次号 = %d/%d，要 1/2", seed.Turns[0].Number, seed.Turns[1].Number)
	}
}

// ── 夹具 ─────────────────────────────────────────────────────────────────

// readLogAt 读一份 v4 会话（等价"换一个进程/重启后再读"）。
func readLogAt(t *testing.T, path string) *v4.Log {
	t.Helper()
	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读会话 %s: %v", path, err)
	}
	return log
}

// readFileBytes 按字节读文件（一次性快照的判据是"一个字节都没变"）。
func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件 %s: %v", path, err)
	}
	return raw
}

// contentBlocksOf 取一条事件的 content 块（`message.content` 优先，顶层兜底）。
func contentBlocksOf(t *testing.T, data json.RawMessage) []session.ContentBlock {
	t.Helper()
	blocks, ok := session.ContentBlocks(data, session.PreferNested)
	if !ok {
		t.Fatalf("事件 content 解不开: %s", data)
	}
	return blocks
}

// mustWireSeed 把种子转成 spi 要的 durable 事件。
func mustWireSeed(t *testing.T, seed subagentSeed) []json.RawMessage {
	t.Helper()
	_, events := seed.forkSeedCells()
	return events
}

// appendParentTurns 给父会话再跑 n 轮（证明"fork 之后父新增的轮次进不来"）。
func appendParentTurns(t *testing.T, home, parent string, n int) {
	t.Helper()
	path := mustFind(t, home, parent)
	lg, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatalf("续写父会话: %v", err)
	}
	back := readLogAt(t, path)
	turn := 0
	for _, ev := range back.Events {
		if ev.Type == "turn/start" {
			turn++
		}
	}
	for i := 0; i < n; i++ {
		turn++
		if err := lg.Append("turn/start", session.TurnStartData(turn)); err != nil {
			lg.Close()
			t.Fatalf("写第 %d 轮 turn/start: %v", turn, err)
		}
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: "fork 之后父会话又说的第 " + strconv.Itoa(turn) + " 轮"},
		})); err != nil {
			lg.Close()
			t.Fatalf("写第 %d 轮 user: %v", turn, err)
		}
		if err := lg.Append("assistant/message", session.AssistantMessageEvent(turn, 1,
			[]map[string]any{session.TextBlock("fork 之后的新回答 " + strconv.Itoa(turn))}, "stop", nil)); err != nil {
			lg.Close()
			t.Fatalf("写第 %d 轮 assistant: %v", turn, err)
		}
		if err := lg.Append("turn/end", session.TurnEndData(turn, "completed", "")); err != nil {
			lg.Close()
			t.Fatalf("写第 %d 轮 turn/end: %v", turn, err)
		}
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关父会话: %v", err)
	}
}

// 种子只认"已完成轮次"，按最后一个 `turn/end` 切（官方 `completedTurnPrefix`）。
func TestCompletedTurnPrefixCutsAtLastTurnEnd(t *testing.T) {
	mk := func(typ string) v4.Event { return v4.Event{Type: typ} }
	events := []v4.Event{
		mk("turn/start"), mk("user/message"), mk("assistant/message"), mk("turn/end"),
		mk("turn/start"), mk("user/message"),
	}
	if got := completedTurnPrefix(events); got != 4 {
		t.Fatalf("前缀长度 = %d，要 4（到第 1 轮 turn/end 为止，第 2 轮 in-flight 不算）", got)
	}
	// 一条 turn/end 都没有：种子为空（首轮之前等同 spawn）。
	if got := completedTurnPrefix([]v4.Event{mk("turn/start"), mk("user/message")}); got != 0 {
		t.Fatalf("没有 turn/end 时前缀长度 = %d，要 0", got)
	}
}

// 块投影：user 的附件/引用不是"没有内容"，assistant 的思考/调用不是"没有内容"。
//
// 这一条盯的是 text-only 那类事故的形状：拿"正文是不是空"判"这条消息有没有东西"。
func TestSeedBlockProjectionNeverDropsNonTextBlocks(t *testing.T) {
	// 只带一张附件、没有正文的 user 消息：必须算"有内容"。
	user := json.RawMessage(`{"content":[{"type":"attachment","attachmentId":"att-1","mime":"image/png"}]}`)
	if text, ok := userSeedParts(user); !ok || !strings.Contains(text, "att-1") {
		t.Fatalf("只带附件的用户消息被判成了空：text=%q ok=%v", text, ok)
	}
	// 只带引用的 user 消息：同样有内容。
	ref := json.RawMessage(`{"content":[{"type":"reference","referenceId":"ref-9"}]}`)
	if text, ok := userSeedParts(ref); !ok || !strings.Contains(text, "ref-9") {
		t.Fatalf("只带引用的用户消息被判成了空：text=%q ok=%v", text, ok)
	}
	// assistant 只有思考块：三样判据里思考占一样，正文为空但有内容。
	think := json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"reasoning","text":"想过了"}]}}`)
	text, reasoning, calls, ok := assistantSeedParts(think)
	if !ok || !assistantHasPayload(text, reasoning, calls) {
		t.Fatalf("只有思考的 assistant 被判成了空：text=%q reasoning=%q calls=%d", text, reasoning, calls)
	}
	// assistant 只有工具调用：同样有内容。
	call := json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"tool-call","id":"c1","name":"bash","arguments":"{}"}]}}`)
	text, reasoning, calls, ok = assistantSeedParts(call)
	if !ok || !assistantHasPayload(text, reasoning, calls) {
		t.Fatalf("只有工具调用的 assistant 被判成了空：text=%q reasoning=%q calls=%d", text, reasoning, calls)
	}
	// 三样都没有：这才算空。
	empty := json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"usage-note","text":""}]}}`)
	text, reasoning, calls, ok = assistantSeedParts(empty)
	if !ok || assistantHasPayload(text, reasoning, calls) {
		t.Fatalf("三样都没有却判成了有内容：text=%q reasoning=%q calls=%d", text, reasoning, calls)
	}
}
