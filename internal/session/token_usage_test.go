package session

import (
	"encoding/json"
	"reflect"
	"testing"

	"dshgo/codecs/v4"
)

// 官方 `tokenUsage` 投影的边界用例（`@deepseek-ai/dsh-token-meter`）。
//
// 每个用例对应官方注释里点名的一条折叠纪律，而不是"随便跑一条 happy path"：
//
//   - 空日志              → 四桶全 0 且 `Measured == false`（"没发生过" ≠ "用量是 0"）
//   - 跨 step 累计        → 每个被采纳的样本都加进 totals
//   - 同一 step 再来样本  → `addReplacing` 替换（不是再累加一次）
//   - `llm/retry-started` → 关掉替换槽，重试后的用量**再加一次**
//   - 没有 usage 的消息   → 状态不动（不写 0 的假账）
//   - `assistant/attempt` → 只认 stream 里的 `usage` chunk（顶层 `usage` 官方不看）
//   - 只有 totalTokens    → 未命中输入由 total − output − 缓存得到（旧日志）
//   - wire view           → 恰好四个官方键
//
// 官方 file:line 见 token_usage.go 顶部与每个断言的注释。

func tokenUsageOf(t *testing.T, events ...v4.Event) TokenUsageProjection {
	t.Helper()
	return ProjectTokenUsage(events)
}

// TestTokenUsageUnitRegistered 钉住官方单元声明
// （`dsh-token-meter/src/usage-projection.ts:111-144`）：key / stateVersion / init。
func TestTokenUsageUnitRegistered(t *testing.T) {
	unit, ok := DefaultProjectionRegistry().Unit(ProjectionKeyTokenUsage)
	if !ok {
		t.Fatalf("%q 必须注册在默认投影表里", ProjectionKeyTokenUsage)
	}
	if ProjectionKeyTokenUsage != "tokenUsage" {
		t.Fatalf("投影键 = %q，官方是 tokenUsage", ProjectionKeyTokenUsage)
	}
	if unit.StateVersion != TokenUsageStateVersion || TokenUsageStateVersion != 2 {
		t.Fatalf("stateVersion = %d，官方是 2", unit.StateVersion)
	}
	state, ok := unit.Init(v4.Header{}, 0).(map[string]any)
	if !ok {
		t.Fatalf("init 必须给出 state map")
	}
	// 官方 `init: () => ({ totals: zeroBuckets(), last: null })`。
	if len(state) != 2 {
		t.Fatalf("init 字段 = %v，官方只有 totals 与 last", state)
	}
	if last, exists := state["last"]; !exists || last != nil {
		t.Fatalf("init.last = %#v，官方是 null", state["last"])
	}
	totals, ok := state["totals"].(map[string]any)
	if !ok {
		t.Fatalf("init.totals 必须是四桶对象：%#v", state["totals"])
	}
	for _, key := range []string{
		"uncachedInputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens",
	} {
		value, exists := totals[key]
		if !exists {
			t.Fatalf("init.totals 缺字段 %q：%v", key, totals)
		}
		if number, ok := sessionStatsNumber(value); !ok || number != 0 {
			t.Fatalf("init.totals[%q] = %v，官方是全 0", key, value)
		}
	}
}

// TestTokenUsageEmptyLogIsUnmeasuredZero 钉住"空会话不编 0"：
// 四桶是全 0（官方 `zeroBuckets`），但 `Measured == false`——那是"没发生过"，
// 与"provider 真的回报了 0"是两件事。
func TestTokenUsageEmptyLogIsUnmeasuredZero(t *testing.T) {
	got := tokenUsageOf(t)
	if got.Buckets != (UsageBuckets{}) {
		t.Fatalf("空日志四桶 = %+v，期望全 0", got.Buckets)
	}
	if got.Measured {
		t.Fatal("空日志不该声称有实测（Measured 必须 false）")
	}
}

// TestTokenUsageAccumulatesAcrossSteps 钉住"累计"语义（官方注释：durable
// cumulative provider usage for a complete session log）。
func TestTokenUsageAccumulatesAcrossSteps(t *testing.T) {
	got := tokenUsageOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantMessageEvent(t, 1, 1500, 1, 1,
			[]map[string]any{{"type": "text", "text": "第一段"}},
			map[string]any{"inputTokens": 9, "outputTokens": 6, "totalTokens": 15}),
		stepEndEvent(t, 2, 1600, 1, 1),
		stepStartEvent(t, 3, 2000, 1, 2),
		assistantMessageEvent(t, 4, 2500, 1, 2,
			[]map[string]any{{"type": "text", "text": "第二段"}},
			map[string]any{
				"inputTokens": 20, "outputTokens": 10, "totalTokens": 30,
				"cacheReadTokens": 100, "cacheWriteTokens": 5,
			}),
		stepEndEvent(t, 5, 2600, 1, 2),
	)
	want := UsageBuckets{
		UncachedInputTokens: 29,  // 9 + 20
		OutputTokens:        16,  // 6 + 10
		CacheReadTokens:     100, // 0 + 100
		CacheWriteTokens:    5,   // 0 + 5
	}
	if got.Buckets != want {
		t.Fatalf("四桶 = %+v，期望 %+v", got.Buckets, want)
	}
	if !got.Measured {
		t.Fatal("有过 usage 样本时 Measured 必须 true")
	}
}

// TestTokenUsageReplacesSameStepSample 钉住 `addReplacing`
// （`src/usage-projection.ts:34-43`）：同一 `{turn, step}` 上出现更新样本时，
// 先减旧再加新——不是把两次 attempt 都累加。
func TestTokenUsageReplacesSameStepSample(t *testing.T) {
	got := tokenUsageOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantMessageEvent(t, 1, 1500, 1, 1, nil,
			map[string]any{"inputTokens": 9, "outputTokens": 6, "totalTokens": 15}),
		assistantMessageEvent(t, 2, 1600, 1, 1, nil,
			map[string]any{"inputTokens": 40, "outputTokens": 7, "totalTokens": 47}),
	)
	want := UsageBuckets{UncachedInputTokens: 40, OutputTokens: 7}
	if got.Buckets != want {
		t.Fatalf("四桶 = %+v，期望 %+v（替换而不是累加）", got.Buckets, want)
	}
}

// TestTokenUsageRetryStartedClosesReplacementSlot 钉住 `llm/retry-started`
// （`src/usage-projection.ts:117-121`）：重试开始后替换槽关掉，下一次 attempt 的
// 用量**加**进去，否则重试之后真实发生的用量会被替换语义抹掉。
func TestTokenUsageRetryStartedClosesReplacementSlot(t *testing.T) {
	got := tokenUsageOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantMessageEvent(t, 1, 1500, 1, 1, nil,
			map[string]any{"inputTokens": 9, "outputTokens": 6, "totalTokens": 15}),
		statsEvent(t, eventLLMRetryStarted, 2, 1700, map[string]any{"turn": 1, "step": 1}),
		assistantMessageEvent(t, 3, 1900, 1, 1, nil,
			map[string]any{"inputTokens": 40, "outputTokens": 7, "totalTokens": 47}),
	)
	want := UsageBuckets{UncachedInputTokens: 49, OutputTokens: 13}
	if got.Buckets != want {
		t.Fatalf("四桶 = %+v，期望 %+v（重试后累加）", got.Buckets, want)
	}

	// 重试事件属于**别的** step 时不许关掉当前替换槽：否则那一步的用量会被
	// 重复累加一次。
	other := tokenUsageOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantMessageEvent(t, 1, 1500, 1, 1, nil,
			map[string]any{"inputTokens": 9, "outputTokens": 6, "totalTokens": 15}),
		statsEvent(t, eventLLMRetryStarted, 2, 1700, map[string]any{"turn": 1, "step": 2}),
		assistantMessageEvent(t, 3, 1900, 1, 1, nil,
			map[string]any{"inputTokens": 40, "outputTokens": 7, "totalTokens": 47}),
	)
	if other.Buckets != (UsageBuckets{UncachedInputTokens: 40, OutputTokens: 7}) {
		t.Fatalf("别的 step 的重试不该关替换槽，四桶 = %+v", other.Buckets)
	}
}

// TestTokenUsageIgnoresMessagesWithoutUsage 钉住"没有样本就什么都不做"：
// provider 没上报 usage 的结算事件不会写一条"用量 0"的假账。
func TestTokenUsageIgnoresMessagesWithoutUsage(t *testing.T) {
	got := tokenUsageOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantMessageEvent(t, 1, 1500, 1, 1,
			[]map[string]any{{"type": "text", "text": "没有 usage"}}, nil),
		stepEndEvent(t, 2, 1600, 1, 1),
	)
	if got.Buckets != (UsageBuckets{}) {
		t.Fatalf("四桶 = %+v，没有 usage 样本时必须全 0", got.Buckets)
	}
	if got.Measured {
		t.Fatal("没有 usage 样本时 Measured 必须 false")
	}
}

// TestTokenUsageReadsStreamUsageChunk 钉住官方 `usageOf` 的回落分支
// （`src/usage-projection.ts:82-86` + `lastAssistantStreamChunk`
// `dsh-llm/src/assistant-stream.ts:373-382`）：顶层没有 `usage` 时，
// 从 compact stream 的最后一条 `usage` chunk 取。
func TestTokenUsageReadsStreamUsageChunk(t *testing.T) {
	message := func(seq, time int64, streams ...map[string]any) v4.Event {
		t.Helper()
		raw := make([]map[string]any, 0, len(streams))
		raw = append(raw, streams...)
		return statsEvent(t, "assistant/message", seq, time, map[string]any{
			"turn": 1, "step": 1,
			"message": map[string]any{"role": "assistant", "content": []map[string]any{}},
			"stream":  raw,
		})
	}
	usageChunk := func(input, output int) map[string]any {
		return map[string]any{
			"type": "chunk",
			"chunk": map[string]any{
				"type":  "usage",
				"usage": map[string]any{"inputTokens": input, "outputTokens": output},
			},
		}
	}

	// 多条 usage chunk 时官方取**最后一条**（`lastAssistantStreamChunk`）。
	got := tokenUsageOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		message(1, 1500, usageChunk(9, 6), usageChunk(11, 8)),
	)
	if want := (UsageBuckets{UncachedInputTokens: 11, OutputTokens: 8}); got.Buckets != want {
		t.Fatalf("四桶 = %+v，期望 %+v（最后一条 usage chunk）", got.Buckets, want)
	}

	// `assistant/attempt` 官方**也**认流里的 usage chunk。
	attempt := tokenUsageOf(t,
		statsEvent(t, eventAssistantAttempt, 0, 1400, map[string]any{
			"turn": 1, "step": 1, "stream": []map[string]any{usageChunk(3, 4)},
		}),
	)
	if want := (UsageBuckets{UncachedInputTokens: 3, OutputTokens: 4}); attempt.Buckets != want {
		t.Fatalf("assistant/attempt 四桶 = %+v，期望 %+v", attempt.Buckets, want)
	}

	// 反过来：`assistant/attempt` 的**顶层** usage 官方不看
	// （`usageOf` 只对 `assistant/message` 读 `event.data.usage`）。
	topLevel := tokenUsageOf(t,
		statsEvent(t, eventAssistantAttempt, 0, 1400, map[string]any{
			"turn": 1, "step": 1,
			"usage": map[string]any{"inputTokens": 3, "outputTokens": 4},
		}),
	)
	if topLevel.Buckets != (UsageBuckets{}) {
		t.Fatalf("assistant/attempt 顶层 usage 不该被采纳，四桶 = %+v", topLevel.Buckets)
	}
}

// TestTokenUsageDerivesUncachedInputFromLegacyTotal 钉住宿主旧日志的兼容口径
// （`v4Usage.buckets` 的第二种形状）：只有 `totalTokens` 时，未命中输入 =
// total − output − 缓存读 − 缓存写。
func TestTokenUsageDerivesUncachedInputFromLegacyTotal(t *testing.T) {
	got := tokenUsageOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantMessageEvent(t, 1, 1500, 1, 1, nil, map[string]any{
			"totalTokens": 100, "outputTokens": 20, "cacheReadTokens": 30,
		}),
	)
	want := UsageBuckets{UncachedInputTokens: 50, OutputTokens: 20, CacheReadTokens: 30}
	if got.Buckets != want {
		t.Fatalf("四桶 = %+v，期望 %+v", got.Buckets, want)
	}
}

// TestTokenUsageMeasuredDistinguishesRealZero 钉住"0 与未知可区分"：
// provider 回报全 0 的用量是**实测的 0**（Measured=true），
// 与一次都没回报（Measured=false）在 Go 侧必须可分辨。
func TestTokenUsageMeasuredDistinguishesRealZero(t *testing.T) {
	realZero := tokenUsageOf(t,
		stepStartEvent(t, 0, 1000, 1, 1),
		assistantMessageEvent(t, 1, 1500, 1, 1, nil,
			map[string]any{"inputTokens": 0, "outputTokens": 0, "totalTokens": 0}),
	)
	if realZero.Buckets != (UsageBuckets{}) {
		t.Fatalf("实测 0 的四桶 = %+v", realZero.Buckets)
	}
	if !realZero.Measured {
		t.Fatal("provider 回报了 0 也是实测（Measured 必须 true）")
	}
	if unknown := tokenUsageOf(t, stepStartEvent(t, 0, 1000, 1, 1)); unknown.Measured {
		t.Fatal("从未回报 usage 时 Measured 必须 false")
	}
}

// TestTokenUsageStateSchemaRejectsBadRows 钉住 state schema（官方
// `tokenUsageStateSchema`，`src/usage-projection.ts:56-63`）：持久化行读回来
// 必须是这条投影能产出的形状。
func TestTokenUsageStateSchemaRejectsBadRows(t *testing.T) {
	unit := TokenUsageUnit()
	valid := map[string]any{
		"totals": map[string]any{
			"uncachedInputTokens": 1, "outputTokens": 2,
			"cacheReadTokens": 3, "cacheWriteTokens": 4,
		},
		"last": map[string]any{
			"turn": 1, "step": 1,
			"buckets": map[string]any{
				"uncachedInputTokens": 1, "outputTokens": 2,
				"cacheReadTokens": 3, "cacheWriteTokens": 4,
			},
		},
	}
	if err := unit.Schema.Validate(valid); err != nil {
		t.Fatalf("合法 state 被拒：%v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"多一个未声明字段": func(state map[string]any) { state["measured"] = true },
		"负数桶": func(state map[string]any) {
			state["totals"] = map[string]any{
				"uncachedInputTokens": -1, "outputTokens": 0,
				"cacheReadTokens": 0, "cacheWriteTokens": 0,
			}
		},
		"小数桶": func(state map[string]any) {
			state["totals"] = map[string]any{
				"uncachedInputTokens": 1.5, "outputTokens": 0,
				"cacheReadTokens": 0, "cacheWriteTokens": 0,
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := map[string]any{
				"totals": map[string]any{
					"uncachedInputTokens": 1, "outputTokens": 2,
					"cacheReadTokens": 3, "cacheWriteTokens": 4,
				},
				"last": map[string]any{
					"turn": 1, "step": 1,
					"buckets": map[string]any{
						"uncachedInputTokens": 1, "outputTokens": 2,
						"cacheReadTokens": 3, "cacheWriteTokens": 4,
					},
				},
			}
			mutate(candidate)
			if err := unit.Schema.Validate(candidate); err == nil {
				t.Fatalf("非法 state 必须被拒：%#v", candidate)
			}
		})
	}
}

// TestTokenUsageStateCheckpointRoundTrip 钉住 state 的 map 往返：
// 折叠后的状态读回来必须还是同一份事实（checkpoint 是持久化热路径）。
func TestTokenUsageStateCheckpointRoundTrip(t *testing.T) {
	unit := TokenUsageUnit()
	state := unit.Init(v4.Header{}, 0)
	state = unit.Apply(state, stepStartEvent(t, 0, 1000, 1, 1))
	state = unit.Apply(state, assistantMessageEvent(t, 1, 1500, 1, 1, nil,
		map[string]any{"inputTokens": 9, "outputTokens": 6, "totalTokens": 15}))

	read := readTokenUsageState(state)
	if want := (UsageBuckets{UncachedInputTokens: 9, OutputTokens: 6}); read.Totals != want {
		t.Fatalf("读回的四桶 = %+v，期望 %+v", read.Totals, want)
	}
	if read.Last == nil || read.Last.Turn != 1 || read.Last.Step != 1 {
		t.Fatalf("读回的 last = %+v", read.Last)
	}
	if !reflect.DeepEqual(read.toMap(), state) {
		t.Fatalf("state 往返不一致：\n%#v\n%#v", read.toMap(), state)
	}
	// 坏状态按空状态处理（一条读不懂的记录不许让整份投影说错话）。
	if broken := readTokenUsageState("不是对象"); broken.Totals != (UsageBuckets{}) || broken.Last != nil {
		t.Fatalf("坏状态读回 = %+v，期望空状态", broken)
	}
}

// TestTokenUsageTrackerReportsChanges 钉住增量折叠：只有真的带了 usage 的事件
// 才把 tokenUsage 报成变化（observeProjections 帧靠这个）。
func TestTokenUsageTrackerReportsChanges(t *testing.T) {
	registry := DefaultProjectionRegistry()
	tracker := NewProjectionTracker(registry, nil, -1, v4.Header{Version: 4}, 0)

	changed := func(changes []ProjectionChange) bool {
		for _, change := range changes {
			if change.Key == ProjectionKeyTokenUsage {
				return true
			}
		}
		return false
	}
	// step/start 会改 sessionStats（开了一个 step），但**不**改 tokenUsage：
	// 用量只由带 usage 的结算事件推动。
	if changes := tracker.Apply(stepStartEvent(t, 0, 1000, 1, 1)); changed(changes) {
		t.Fatalf("step/start 不该改 tokenUsage：%#v", changes)
	}
	changes := tracker.Apply(assistantMessageEvent(t, 1, 1500, 1, 1, nil,
		map[string]any{"inputTokens": 9, "outputTokens": 6, "totalTokens": 15}))
	var found bool
	for _, change := range changes {
		if change.Key != ProjectionKeyTokenUsage {
			continue
		}
		found = true
		buckets, ok := change.Val.(UsageBuckets)
		if !ok {
			t.Fatalf("变化值类型 = %T", change.Val)
		}
		if want := (UsageBuckets{UncachedInputTokens: 9, OutputTokens: 6}); buckets != want {
			t.Fatalf("变化值 = %+v，期望 %+v", buckets, want)
		}
	}
	if !found {
		t.Fatalf("带 usage 的 assistant/message 必须报 tokenUsage 变化：%#v", changes)
	}
	snapshot, ok := tracker.Snapshot().Values[ProjectionKeyTokenUsage].(UsageBuckets)
	if !ok {
		t.Fatalf("快照里的 tokenUsage 类型 = %T", tracker.Snapshot().Values[ProjectionKeyTokenUsage])
	}
	if want := (UsageBuckets{UncachedInputTokens: 9, OutputTokens: 6}); snapshot != want {
		t.Fatalf("快照四桶 = %+v，期望 %+v", snapshot, want)
	}
}

// TestTokenUsageWireViewIsFourBuckets 钉住官方 wire view
// （`src/usage-projection.ts:143` 的 `view: state => state.totals`）：
// 输出恰好四个官方键，一个不多一个不少（`.strict()`）。
func TestTokenUsageWireViewIsFourBuckets(t *testing.T) {
	unit := TokenUsageUnit()
	state := unit.Init(v4.Header{}, 0)
	state = unit.Apply(state, assistantMessageEvent(t, 0, 1500, 1, 1, nil,
		map[string]any{
			"inputTokens": 9, "outputTokens": 6, "totalTokens": 15,
			"cacheReadTokens": 100, "cacheWriteTokens": 5,
		}))

	raw, err := json.Marshal(unit.View(state))
	if err != nil {
		t.Fatalf("view 序列化失败：%v", err)
	}
	var fields map[string]float64
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("view 不是对象：%s", raw)
	}
	want := map[string]float64{
		"uncachedInputTokens": 9,
		"outputTokens":        6,
		"cacheReadTokens":     100,
		"cacheWriteTokens":    5,
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("wire view = %v，期望恰好四个官方键 %v", fields, want)
	}
}
