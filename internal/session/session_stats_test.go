package session

import (
	"encoding/json"
	"testing"

	"dshgo/codecs/v4"
)

// 官方 `sessionStats` 投影的边界用例（`@deepseek-ai/dsh-session-stats`）。
//
// 每个用例对应官方注释里点名的一条折叠纪律，而不是"随便跑一条 happy path"：
//
//   - 完成 step           → 八个字段全部从真实事件累加
//   - 失败 step           → step 照数（`step/end` 是权威），消息侧时间全 0
//   - 取消 step           → 不组装消息 ⇒ 它的 partial stream 时间在**任何**数字里都不计
//   - max-tokens 空消息   → 数 `assistant/message` 会**多算**，数 `step/end` 才对
//   - `llm/retry`         → 第一步的 first token **存活**（不被重试推后）
//   - tool 乱序 result    → 按 callId 配对，不错配、不配对不存在的调用
//
// 官方 file:line 见 session_stats.go 顶部与每个断言的注释。

// statsEvent 造一条带**显式时间**的 v4 事件（墙钟断言的时钟在这里）。
func statsEvent(t *testing.T, typ string, seq, time int64, data any) v4.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("事件 %s 序列化失败: %v", typ, err)
	}
	return v4.Event{Type: typ, Seq: seq, Time: time, Data: raw}
}

func statsOf(t *testing.T, events ...v4.Event) SessionStats {
	t.Helper()
	return ProjectSessionStats(events)
}

func stepStartEvent(t *testing.T, seq, time int64, turn, step int) v4.Event {
	t.Helper()
	return statsEvent(t, "step/start", seq, time, map[string]any{"turn": turn, "step": step})
}

func stepEndEvent(t *testing.T, seq, time int64, turn, step int) v4.Event {
	t.Helper()
	return statsEvent(t, "step/end", seq, time, map[string]any{"turn": turn, "step": step})
}

func assistantDeltaEvent(t *testing.T, seq, time int64, turn, step int, typ, text string) v4.Event {
	t.Helper()
	return statsEvent(t, typ, seq, time, map[string]any{
		"turn": turn, "step": step, "index": 0, "text": text, "model": "test-model",
	})
}

// assistantMessageEvent 造一条宿主写法的 `assistant/message`。
//
// content 为空时它就是官方注释里那种 "max-tokens 的 usage-host 消息"：有 usage、
// 空内容、不在 surface 上。
func assistantMessageEvent(t *testing.T, seq, time int64, turn, step int, content []map[string]any, usage map[string]any) v4.Event {
	t.Helper()
	data := map[string]any{
		"turn":    turn,
		"step":    step,
		"message": map[string]any{"role": "assistant", "content": content},
		"stream":  []map[string]any{{"type": "finish", "reason": map[string]any{"kind": "completed"}}},
	}
	if usage != nil {
		data["usage"] = usage
	}
	return statsEvent(t, "assistant/message", seq, time, data)
}

func toolCallEvent(t *testing.T, seq, time int64, turn, step int, callID string) v4.Event {
	t.Helper()
	return statsEvent(t, "tool/call", seq, time, map[string]any{
		"turn": turn, "step": step, "callId": callID, "name": "read", "arguments": "{}",
	})
}

func toolResultEvent(t *testing.T, seq, time int64, turn, step int, callID string) v4.Event {
	t.Helper()
	return statsEvent(t, "tool/result", seq, time, map[string]any{
		"turn": turn,
		"step": step,
		"message": map[string]any{
			"role":       "tool",
			"source":     map[string]any{"kind": "tool", "callId": callID},
			"toolCallId": callID,
			"content":    []map[string]any{{"type": "text", "text": "ok"}},
			"isError":    false,
			"id":         "m-" + callID,
		},
	})
}

func statsTurnEndEvent(t *testing.T, seq, time int64, turn int, kind string) v4.Event {
	t.Helper()
	return statsEvent(t, "turn/end", seq, time, map[string]any{
		"turn":   turn,
		"reason": map[string]any{"kind": kind},
	})
}

// TestSessionStatsUnitRegistered 钉住官方单元声明
// （`dsh-session-stats/lib/types/projection.js:66-82`）：key / stateVersion / init 全 0。
func TestSessionStatsUnitRegistered(t *testing.T) {
	unit, ok := DefaultProjectionRegistry().Unit(ProjectionKeySessionStats)
	if !ok {
		t.Fatalf("%q 必须注册在默认投影表里", ProjectionKeySessionStats)
	}
	if ProjectionKeySessionStats != "sessionStats" {
		t.Fatalf("投影键 = %q，官方是 sessionStats", ProjectionKeySessionStats)
	}
	if unit.StateVersion != SessionStatsStateVersion || SessionStatsStateVersion != 1 {
		t.Fatalf("stateVersion = %d，官方是 1", unit.StateVersion)
	}
	state, ok := unit.Init(v4.Header{}, 0).(map[string]any)
	if !ok {
		t.Fatalf("init 必须给出 state map")
	}
	for _, key := range []string{
		"turns", "steps", "llmMs", "toolMs", "ttftMs", "ttftSteps", "decodeMs", "decodeTokens",
	} {
		value, exists := state[key]
		if !exists {
			t.Fatalf("init 缺字段 %q：%v", key, state)
		}
		if number, ok := sessionStatsNumber(value); !ok || number != 0 {
			t.Fatalf("init[%q] = %v，官方是全 0", key, value)
		}
	}
	// 空日志 = 全 0（不是"未知"）：官方每个字段"0 until its first contributing event lands"。
	if got := statsOf(t); got != (SessionStats{}) {
		t.Fatalf("空日志的投影必须是全 0：%+v", got)
	}
}

// TestSessionStatsCompletedStep 是**完成 step** 的完整账：
//
//	step/start(1000) → assistant/text(1200) → assistant/message(1500, outputTokens=7)
//	→ step/end(1600)
//
// 于是 llmMs = 500（step/start → 消息）、ttftMs = 200（step/start → 首 token）、
// decodeMs = 300（首 token → 消息）、decodeTokens = 7。
func TestSessionStatsCompletedStep(t *testing.T) {
	got := statsOf(t,
		statsTurnEndEvent(t, 0, 900, 1, "completed"), // 上一轮的 turn/end 不影响计数
		stepStartEvent(t, 1, 1000, 1, 1),
		assistantDeltaEvent(t, 2, 1200, 1, 1, "assistant/text", "你"),
		assistantMessageEvent(t, 3, 1500, 1, 1,
			[]map[string]any{{"type": "text", "text": "你好"}},
			map[string]any{"inputTokens": 10, "outputTokens": 7, "totalTokens": 17}),
		stepEndEvent(t, 4, 1600, 1, 1),
		statsTurnEndEvent(t, 5, 1700, 1, "completed"),
	)
	want := SessionStats{
		Turns:        1,
		Steps:        1,
		LLMMs:        500,
		ToolMs:       0,
		TTFTMs:       200,
		TTFTSteps:    1,
		DecodeMs:     300,
		DecodeTokens: 7,
	}
	if got != want {
		t.Fatalf("完成 step 的账 = %+v，期望 %+v", got, want)
	}
}

// TestSessionStatsFailedStep 是**失败 step**：模型报错时宿主照样写 step/start →
// step/end（`finally` 语义），但没有 assistant/message。
//
// 结论：steps/turns 各 1（`step/end` 是权威），消息侧时间全 0——失败的 step 不
// 假装有模型墙钟。
func TestSessionStatsFailedStep(t *testing.T) {
	got := statsOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		stepEndEvent(t, 1, 1300, 1, 1),
		statsTurnEndEvent(t, 2, 1400, 1, "error"),
	)
	want := SessionStats{Turns: 1, Steps: 1}
	if got != want {
		t.Fatalf("失败 step 的账 = %+v，期望 %+v（只有 step/end 计数）", got, want)
	}
}

// TestSessionStatsCancelledStepDropsStreamTime 是**取消 step**：它已经流过 token，
// 但没组装消息就 aborted。
//
// 官方注释（`projection.js:19-21`）："A cancelled step assembles no message, so its
// partial stream time stays uncounted in every time figure"。所以首 token 虽然被
// 记进折叠边界，但 ttftMs/ttftSteps/decodeMs 一个都不累加，step/end 也不留残留。
func TestSessionStatsCancelledStepDropsStreamTime(t *testing.T) {
	events := []v4.Event{
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantDeltaEvent(t, 1, 1800, 1, 1, "assistant/text", "半句话"),
		stepEndEvent(t, 2, 2000, 1, 1),
		statsTurnEndEvent(t, 3, 2000, 1, "aborted"),
	}
	got := statsOf(t, events...)
	want := SessionStats{Turns: 1, Steps: 1}
	if got != want {
		t.Fatalf("取消 step 的账 = %+v，期望 %+v（partial stream 时间任何数字里都不计）", got, want)
	}
	// 反向证明：同一条流如果有消息组装，200/1000 这两个数就会出现——
	// 说明上面那个 0 不是"没折叠"，而是"官方语义要求不计"。
	assembled := statsOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantDeltaEvent(t, 1, 1800, 1, 1, "assistant/text", "半句话"),
		assistantMessageEvent(t, 2, 2000, 1, 1,
			[]map[string]any{{"type": "text", "text": "半句话"}},
			map[string]any{"outputTokens": 3}),
		stepEndEvent(t, 3, 2000, 1, 1),
	)
	if assembled.TTFTMs != 800 || assembled.DecodeMs != 200 || assembled.DecodeTokens != 3 {
		t.Fatalf("组装后的同一条流应当计时：%+v", assembled)
	}
}

// TestSessionStatsMaxTokensEmptyMessage 是**max-tokens 的 usage-host 消息**：
// 一个 step 里既有一条组装好的消息，又有一条空内容、不在 surface 上的 usage 消息。
//
// 官方注释（`projection.js:6-11`）：数 `assistant/message` 会**多算**它。所以
// steps 必须是 1（`step/end` 条数），多出来的那条消息不许再算一步，也不许再累加
// 一次 model 墙钟（边界已经关掉了）。
func TestSessionStatsMaxTokensEmptyMessage(t *testing.T) {
	got := statsOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantDeltaEvent(t, 1, 1100, 1, 1, "assistant/text", "被截断的回答"),
		assistantMessageEvent(t, 2, 1500, 1, 1,
			[]map[string]any{{"type": "text", "text": "被截断的回答"}},
			map[string]any{"outputTokens": 4}),
		// usage-host 消息：空内容、不在 surface 上、只有 usage。
		assistantMessageEvent(t, 3, 1600, 1, 1, []map[string]any{},
			map[string]any{"outputTokens": 4}),
		stepEndEvent(t, 4, 1700, 1, 1),
	)
	// 数 assistant/message 会得到 steps=2；官方要 1。
	if got.Steps != 1 {
		t.Fatalf("steps = %d，多算了 usage-host 消息（官方：step/end 才是权威）", got.Steps)
	}
	want := SessionStats{
		Turns:        1,
		Steps:        1,
		LLMMs:        500, // 只算到第一条消息：边界关掉后第二条不再累加
		TTFTMs:       100,
		TTFTSteps:    1,
		DecodeMs:     400,
		DecodeTokens: 4,
	}
	if got != want {
		t.Fatalf("max-tokens step 的账 = %+v，期望 %+v", got, want)
	}
}

// TestSessionStatsTurnCountsOncePerTurn 钉住 turns 的语义："至少闭合过一个 step 的
// 不同轮次数"（`types.ts:25-26`）；同一轮里的多个 step 只算一轮。
func TestSessionStatsTurnCountsOncePerTurn(t *testing.T) {
	got := statsOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		stepEndEvent(t, 1, 1100, 1, 1),
		stepStartEvent(t, 2, 1200, 1, 2),
		stepEndEvent(t, 3, 1300, 1, 2),
		stepStartEvent(t, 4, 1400, 2, 1),
		stepEndEvent(t, 5, 1500, 2, 1),
	)
	if got.Turns != 2 || got.Steps != 3 {
		t.Fatalf("turns/steps = %d/%d，期望 2/3", got.Turns, got.Steps)
	}
}

// TestSessionStatsFirstTokenSurvivesRetry 是**跨重试的首 token**：
//
//	step/start(1000) → 第一次 attempt 的 delta(1200) → llm/retry → 重试 delta(1900)
//	→ assistant/message(2200, outputTokens=5) → step/end
//
// 官方注释（`projection.js:16-18`）：first token 是"第一个非空 delta chunk"，且
// **跨 step 内 `llm/retry` 存活**——`llm/retry` 不在 apply 的 switch 里，状态原样
// 保留。所以 ttftMs 必须是 200，不是 900。
func TestSessionStatsFirstTokenSurvivesRetry(t *testing.T) {
	got := statsOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantDeltaEvent(t, 1, 1200, 1, 1, "assistant/text", "先"),
		statsEvent(t, "llm/retry", 2, 1500, map[string]any{"turn": 1, "step": 1}),
		assistantDeltaEvent(t, 3, 1900, 1, 1, "assistant/text", "重试后的回答"),
		assistantMessageEvent(t, 4, 2200, 1, 1,
			[]map[string]any{{"type": "text", "text": "重试后的回答"}},
			map[string]any{"outputTokens": 5}),
		stepEndEvent(t, 5, 2300, 1, 1),
	)
	want := SessionStats{
		Turns:        1,
		Steps:        1,
		LLMMs:        1200,
		TTFTMs:       200, // 1200 - 1000：第一次 attempt 的 token，不是 1900
		TTFTSteps:    1,
		DecodeMs:     1000, // 2200 - 1200
		DecodeTokens: 5,
	}
	if got != want {
		t.Fatalf("跨重试的首 token = %+v，期望 %+v", got, want)
	}
}

// TestSessionStatsToolPairingByCallID 是**乱序 result 的配对**。
//
// 三个调用（c1@1000、c2@1100、c3@1200），两个结果按 c3 → c1 的**乱序**落地：
//
//	正确（按 callId）：(1300-1200) + (1500-1000) = 600
//	FIFO 错配：        (1300-1000) + (1500-1100) = 700
//	LIFO 错配：        (1300-1200) + (1500-1100) = 500
//
// 三种口径各不相同，所以 600 这条断言真的在测"按 callId 配对"。
func TestSessionStatsToolPairingByCallID(t *testing.T) {
	got := statsOf(t,
		toolCallEvent(t, 0, 1000, 1, 1, "c1"),
		toolCallEvent(t, 1, 1100, 1, 1, "c2"),
		toolCallEvent(t, 2, 1200, 1, 1, "c3"),
		toolResultEvent(t, 3, 1300, 1, 1, "c3"),
		toolResultEvent(t, 4, 1500, 1, 1, "c1"),
	)
	if got.ToolMs != 600 {
		t.Fatalf("toolMs = %d，期望 600（按 callId 配对；FIFO 会得到 700、LIFO 500）", got.ToolMs)
	}
}

// TestSessionStatsToolPairingDropsUnmatched 钉住三条"不配对"纪律：
//   - 没有派发的 result（callId 未知）不计时，也不许污染 toolMs；
//   - `turn/end` 丢掉残留（结果总在自己的轮内落地）；
//   - 轮结束之后迟到的 result 找不到派发记录，照样不计。
func TestSessionStatsToolPairingDropsUnmatched(t *testing.T) {
	got := statsOf(t,
		toolCallEvent(t, 0, 1000, 1, 1, "c1"),
		toolResultEvent(t, 1, 1200, 1, 1, "ghost"), // 没派发过
		toolResultEvent(t, 2, 1300, 1, 1, "c1"),    // 正常配对：300
		statsTurnEndEvent(t, 3, 1400, 1, "completed"),
		toolResultEvent(t, 4, 9000, 1, 1, "c1"), // 轮结束后迟到：不计
	)
	if got.ToolMs != 300 {
		t.Fatalf("toolMs = %d，期望 300（未派发的 result 与迟到 result 都不计）", got.ToolMs)
	}
	// 残留被 turn/end 清空：再开一轮 tool/call 时不会把上一轮的派发时间算进来。
	next := statsOf(t,
		toolCallEvent(t, 0, 1000, 1, 1, "c1"),
		statsTurnEndEvent(t, 1, 1400, 1, "aborted"),
		toolCallEvent(t, 2, 5000, 1, 2, "c1"),
		toolResultEvent(t, 3, 5100, 1, 2, "c1"),
	)
	if next.ToolMs != 100 {
		t.Fatalf("跨轮残留没清干净：toolMs = %d，期望 100", next.ToolMs)
	}
}

// TestSessionStatsOfficialStreamRecords 钉住**官方日志**的 compact stream 折叠
// （宿主自己不写 assistant/attempt，但读官方日志时 first token 在 stream 里）。
//
// 三种记录形状（`dsh-llm/src/assistant-stream.ts:44-74`）：
//   - 裸 chunk：`{type:'chunk', time, chunk}`，靠 `isTokenDelta` 判；
//   - 打包 run：`{type:'text-chunks'|'reasoning-chunks', time0, dt, texts}`；
//   - tool-call run：`{type:'tool-call-chunks', time0, dt, args, name?}`。
func TestSessionStatsOfficialStreamRecords(t *testing.T) {
	// assistant/attempt 里的首 token：第一条 text-delta 是空串（不是 token），
	// 第二条才是（时间 = 1250）。
	attempt := statsEvent(t, "assistant/attempt", 1, 1250, map[string]any{
		"turn": 1, "step": 1,
		"stream": []map[string]any{
			{"type": "chunk", "time": 1200, "chunk": map[string]any{"type": "text-delta", "text": ""}},
			{"type": "chunk", "time": 1250, "chunk": map[string]any{"type": "text-delta", "text": "你"}},
			{"type": "chunk", "time": 1300, "chunk": map[string]any{"type": "text-delta", "text": "好"}},
		},
	})
	message := statsEvent(t, "assistant/message", 2, 2000, map[string]any{
		"turn": 1, "step": 1,
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "你好"}}},
		"usage":   map[string]any{"outputTokens": 2},
		"stream":  []map[string]any{{"type": "finish", "reason": map[string]any{"kind": "completed"}}},
	})
	got := statsOf(t, stepStartEvent(t, 0, 1000, 1, 1), attempt, message, stepEndEvent(t, 3, 2100, 1, 1))
	if got.TTFTMs != 250 || got.DecodeMs != 750 || got.DecodeTokens != 2 {
		t.Fatalf("assistant/attempt 的首 token = %+v，期望 ttftMs=250 decodeMs=750", got)
	}

	// 只有消息、没有 attempt 时，从消息自己的 stream 里重建（打包 run 的成员时间）。
	packed := statsEvent(t, "assistant/message", 1, 2000, map[string]any{
		"turn": 1, "step": 1,
		"message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "你好"}}},
		"usage":   map[string]any{"outputTokens": 3},
		"stream": []map[string]any{
			{"type": "text-chunks", "time0": 1100, "index": 0, "dt": []int64{50, 50}, "texts": []string{"", "你", "好"}},
		},
	})
	fromMessage := statsOf(t, stepStartEvent(t, 0, 1000, 1, 1), packed, stepEndEvent(t, 2, 2000, 1, 1))
	// 第一个非空 fragment 是 "你"，它的时间是 time0 + dt[0] = 1150。
	if fromMessage.TTFTMs != 150 || fromMessage.DecodeMs != 850 || fromMessage.DecodeTokens != 3 {
		t.Fatalf("打包 run 的首 token = %+v，期望 ttftMs=150 decodeMs=850", fromMessage)
	}

	// 带名字的 tool-call run：从 run 的第一个成员开始算（time0）。
	toolRun := statsEvent(t, "assistant/message", 1, 2000, map[string]any{
		"turn": 1, "step": 1,
		"message": map[string]any{"role": "assistant", "content": []map[string]any{}},
		"stream": []map[string]any{
			{"type": "tool-call-chunks", "time0": 1050, "index": 0, "dt": []int64{10}, "id": "c1", "name": "read", "args": []string{""}},
		},
	})
	fromToolRun := statsOf(t, stepStartEvent(t, 0, 1000, 1, 1), toolRun, stepEndEvent(t, 2, 2000, 1, 1))
	if fromToolRun.TTFTMs != 50 || fromToolRun.TTFTSteps != 1 {
		t.Fatalf("tool-call run 的首 token = %+v，期望 ttftMs=50", fromToolRun)
	}
}

// TestSessionStatsReasoningDeltaCounts 钉住"reasoning 也算 token"
// （官方 `isTokenDelta` 同时接受 text-delta 与 reasoning-delta）。
func TestSessionStatsReasoningDeltaCounts(t *testing.T) {
	got := statsOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantDeltaEvent(t, 1, 1400, 1, 1, "assistant/reasoning", "先想一想"),
		assistantMessageEvent(t, 2, 2000, 1, 1,
			[]map[string]any{{"type": "text", "text": "答"}},
			map[string]any{"outputTokens": 1}),
		stepEndEvent(t, 3, 2100, 1, 1),
	)
	if got.TTFTMs != 400 || got.TTFTSteps != 1 || got.DecodeMs != 600 {
		t.Fatalf("reasoning delta 必须算首 token：%+v", got)
	}
	// 空 delta 不算 token（官方 `text !== ''`）。
	empty := statsOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantDeltaEvent(t, 1, 1400, 1, 1, "assistant/text", ""),
		assistantMessageEvent(t, 2, 2000, 1, 1, []map[string]any{}, nil),
		stepEndEvent(t, 3, 2100, 1, 1),
	)
	if empty.TTFTSteps != 0 || empty.TTFTMs != 0 {
		t.Fatalf("空 delta 不是 token：%+v", empty)
	}
}

// TestSessionStatsWireShape 钉住 wire 形状：八个字段、官方键名、全部非负。
//
// 官方 schema 是 **strict**（`projection.js:27-36` 的 `.strict()`）：多一个键就是
// 多一份界面读不懂的载荷；负数是"负的耗时"，不存在。
func TestSessionStatsWireShape(t *testing.T) {
	stats := SessionStats{
		Turns: 1, Steps: 2, LLMMs: 3, ToolMs: 4, TTFTMs: 5, TTFTSteps: 6, DecodeMs: 7, DecodeTokens: 8,
	}
	raw, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"turns": 1, "steps": 2, "llmMs": 3, "toolMs": 4,
		"ttftMs": 5, "ttftSteps": 6, "decodeMs": 7, "decodeTokens": 8,
	}
	if len(decoded) != len(want) {
		t.Fatalf("wire 字段 = %v，期望恰好 %d 个", decoded, len(want))
	}
	for key, value := range want {
		number, ok := decoded[key].(float64)
		if !ok || number != value {
			t.Fatalf("wire[%q] = %v，期望 %v", key, decoded[key], value)
		}
	}

	schema := SessionStatsUnit().Schema
	valid := map[string]any{
		"turns": 1, "steps": 2, "llmMs": 3, "toolMs": 4, "ttftMs": 5,
		"ttftSteps": 6, "decodeMs": 7, "decodeTokens": 8,
		"lastTurn":     nil,
		"openStep":     nil,
		"pendingCalls": map[string]any{},
	}
	if err := schema.Validate(valid); err != nil {
		t.Fatalf("合法 state 必须过 schema：%v", err)
	}
	withUnknown := map[string]any{"surprise": 1}
	for key, value := range valid {
		withUnknown[key] = value
	}
	if err := schema.Validate(withUnknown); err == nil {
		t.Fatal("多出来的键必须被拒绝（官方 strict）")
	}
	for _, broken := range []map[string]any{
		func() map[string]any { copy := cloneStatsState(valid); copy["turns"] = -1; return copy }(),
		func() map[string]any { copy := cloneStatsState(valid); copy["steps"] = 1.5; return copy }(),
		func() map[string]any { copy := cloneStatsState(valid); copy["llmMs"] = -0.5; return copy }(),
		func() map[string]any { copy := cloneStatsState(valid); copy["ttftSteps"] = "6"; return copy }(),
	} {
		if err := schema.Validate(broken); err == nil {
			t.Fatalf("非法 state 必须被拒绝：%v", broken)
		}
	}
}

func cloneStatsState(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

// TestSessionStatsTrackerReportsKey 钉住"接到既有 session.projections 机制"：
// 增量折叠器必须把 sessionStats 作为**变化键**报出来（observeProjections 帧），
// 而不是只在冷读快照里出现。
func TestSessionStatsTrackerReportsKey(t *testing.T) {
	tracker := NewProjectionTracker(DefaultProjectionRegistry(), nil, -1, v4.Header{ID: "s1", Version: 4}, 0)
	if changes := tracker.Apply(stepStartEvent(t, 0, 1000, 1, 1)); len(changes) != 1 ||
		changes[0].Key != ProjectionKeySessionStats {
		t.Fatalf("step/start 的变更 = %#v，期望恰好 sessionStats 一键", changes)
	}
	// 无关事件不发帧。
	if changes := tracker.Apply(statsEvent(t, "request/header", 1, 1100, map[string]any{"turn": 1})); len(changes) != 0 {
		t.Fatalf("无关事件的变更 = %#v，期望空", changes)
	}
	if changes := tracker.Apply(stepEndEvent(t, 2, 1200, 1, 1)); len(changes) != 1 ||
		changes[0].Key != ProjectionKeySessionStats {
		t.Fatalf("step/end 的变更 = %#v", changes)
	}
	view, ok := tracker.Snapshot().Values[ProjectionKeySessionStats].(SessionStats)
	if !ok || view.Steps != 1 || view.Turns != 1 {
		t.Fatalf("快照里的 sessionStats = %#v", tracker.Snapshot().Values[ProjectionKeySessionStats])
	}
}
