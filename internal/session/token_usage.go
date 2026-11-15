package session

import (
	"encoding/json"

	"dshgo/codecs/v4"
	"dshgo/internal/storage"
)

// `tokenUsage` 投影：整份日志累计的 provider 上报用量，四桶不相交。
//
// 官方证据（`@deepseek-ai/dsh-token-meter`）：
//   - 投影键声明：`src/projection.ts:70-79`（`tokenUsage: TokenUsageProjection`，
//     **不是** `SessionProjectionSummaryExcluded`——会话列表摘要里也带它）；
//   - 四桶语义：`src/projection.ts:15-20`（不相交；reasoning tokens 已含在
//     `outputTokens` 里，不再二次累加）；
//   - init 全 0：`src/usage-projection.ts:14-19`（`zeroBuckets`）；
//   - 单条样本 → 四桶：`src/usage-projection.ts:21-26`（`bucketsFrom`：
//     `uncachedInputTokens = usage.inputTokens`，缓存两桶缺省按 0）；
//   - 样本相等判定：同文件 `:28-32`（`bucketsEqual`）；
//   - 累计替换：同文件 `:34-43`（`addReplacing`）；
//   - wire schema（**strict**，四桶全部必填、int、非负）：同文件 `:45-50`；
//   - state schema（`{totals, last:{turn,step,buckets}|null}`）：同文件 `:56-63`；
//   - 单元声明（key / stateVersion 2 / init / apply / wire.view）：同文件 `:111-144`；
//   - 样本来源 `usageOf`：同文件 `:82-86`；
//   - 流内 `usage` chunk 兜底：`@deepseek-ai/dsh-llm/src/assistant-stream.ts:373-382`
//     （`lastAssistantStreamChunk(stream, 'usage')`）；
//   - `TokenUsage` 记录形状：`@deepseek-ai/dsh-llm/src/types.ts:182-196`。
//
// # 为什么这条投影必须存在
//
// 官方用量胶囊 `UsagePill`（`dsh-client-ui-chat/src/client/chat/StatsPills.tsx:296-347`）
// 与统计面板的合计行（`dsh-experimental-client-ui-stats/src/client/stats-totals.ts:13-39`）
// 都**只**从 `useProjection('tokenUsage')` 取数：宿主没有这个键，用量弹窗就只能整段
// 静态（界面表现就是「未知」）。而 `tokenUsage` 与 `context.usage.usageBuckets` 是
// 同一组桶的不同两个面，不能互相顶替——前者是**整份日志的累计**，后者是**最近一次
// 请求的实测**。
//
// # 折叠语义（官方逐条，抄在代码旁）
//
//  1. **累计，不是最近一次**：每个被采纳的样本都加进 `totals`。同一 `{turn, step}`
//     上出现更新样本时用 `addReplacing` **替换**（先减旧再加新），所以重试/重放不会
//     把同一段用量算两遍。
//  2. **`llm/retry-started` 关掉替换槽**：重试意味着上一次 attempt 的用量已经成为
//     历史，下一次 attempt 的样本必须**加**而不是替换（否则重试后的真实用量会被抹掉）。
//  3. **没有样本就什么都不做**：provider 没上报 usage 的 `assistant/message` 不改状态，
//     不会写一条"用量 0"的假账。
//
// 本文件只做**纯派生**：从 v4 原始帧折叠，读路径不写盘（R5 红线）。

const (
	// ProjectionKeyTokenUsage 是官方投影键（`src/projection.ts:73`）。
	ProjectionKeyTokenUsage = "tokenUsage"
	// TokenUsageStateVersion 是官方 stateVersion（`src/usage-projection.ts:113`）。
	TokenUsageStateVersion = 2
)

// eventLLMRetryStarted 是"一次重试开始"的耐久事件（官方
// `@deepseek-ai/dsh-llm-retry/src/index.ts:186` 的 `append('llm/retry-started', …)`）。
//
// 宿主自己目前不写这条事件（重试在 `internal/llm` 内部完成），但它是官方 v4 词汇表
// 的成员（`known_events.go:71`），读官方会话日志时必须认得——否则重试过的 step
// 会把第二次 attempt 的用量当成"替换"，少算一段。
const eventLLMRetryStarted = "llm/retry-started"

// TokenUsageProjection 是 `tokenUsage` 的折叠结果：四桶累计 + 是否真的实测过。
//
// 为什么要 `Measured`：官方 wire 的四桶**全部必填**（`projectionSchema.strict()`），
// 所以"没有用量"在 wire 上只能表达成 0——官方 `init` 就是全 0。但"全 0"有两种
// 截然不同的事实：**provider 真的回报了 0**（例如全缓存命中的提示词）与
// **一次都没回报过**。这一位把后者单独标出来，供需要区分的调用方（与测试）使用；
// 界面按官方口径只看四桶（`billedInputTokens === 0 && outputTokens === 0` 时整枚
// 不画），所以 wire 形状不变。
type TokenUsageProjection struct {
	// Buckets 是官方 wire view 的四个桶（**累计值**，`src/usage-projection.ts:143`）。
	Buckets UsageBuckets
	// Measured 表示整份日志里至少有一条 provider 上报的 usage 样本。
	// false 时四桶全是 0——那是"没有发生过"，不是"用量是 0"。
	Measured bool
}

// tokenUsageSample 是 state 里的 `last` 槽：最近一次被采纳的样本
// （官方 `last: {turn, step, buckets} | null`）。
type tokenUsageSample struct {
	Turn    int64
	Step    int64
	Buckets UsageBuckets
}

// tokenUsageState 是官方 state 的 Go 视图
// （`{totals: 四桶, last: 样本 | null}`）。
type tokenUsageState struct {
	Totals UsageBuckets
	Last   *tokenUsageSample
}

// tokenUsageBucketsSchema 是官方 `projectionSchema`（`src/usage-projection.ts:45-50`）：
// 四桶全部必填、int、非负。
//
// 为什么 int 与非负都要管：`12.5` 个 token 是不存在的形状，静默接受它会让"用量"
// 变成一个小数；负数同理（provider 自相矛盾时上游已经夹过 0）。
var tokenUsageBucketsSchema = storage.Object(map[string]storage.Schema{
	"uncachedInputTokens": nonNegativeIntegerSchema("uncachedInputTokens"),
	"outputTokens":        nonNegativeIntegerSchema("outputTokens"),
	"cacheReadTokens":     nonNegativeIntegerSchema("cacheReadTokens"),
	"cacheWriteTokens":    nonNegativeIntegerSchema("cacheWriteTokens"),
})

// tokenUsageSampleSchema 是 `last` 槽的形状（`{turn, step, buckets}`）。
var tokenUsageSampleSchema = storage.Object(map[string]storage.Schema{
	"turn":    nonNegativeIntegerSchema("last.turn"),
	"step":    nonNegativeIntegerSchema("last.step"),
	"buckets": tokenUsageBucketsSchema,
})

// tokenUsageStateSchema 是官方 state schema（`src/usage-projection.ts:56-63`）。
//
// 为什么 state 也要过 schema：它是**持久化**的行（`session_projcache`），读回来必须
// 能证明"这份状态是这条投影能产出的形状"，否则一行坏记录会让整份快照说错话。
var tokenUsageStateSchema = storage.Object(map[string]storage.Schema{
	"totals": tokenUsageBucketsSchema,
	"last":   storage.Nullable(tokenUsageSampleSchema),
})

// TokenUsageUnit 返回官方 `tokenUsage` 投影单元（stateVersion 2）。
func TokenUsageUnit() ProjectionUnit {
	return ProjectionUnit{
		Key:          ProjectionKeyTokenUsage,
		StateVersion: TokenUsageStateVersion,
		Schema:       tokenUsageStateSchema,
		Init:         func(v4.Header, int64) any { return tokenUsageState{}.toMap() },
		Apply: func(state any, event v4.Event) any {
			next, _ := applyTokenUsage(readTokenUsageState(state), event)
			return next.toMap()
		},
		// 官方 wire.view：`state => state.totals`（`src/usage-projection.ts:143`）。
		View: func(state any) any { return readTokenUsageState(state).Totals },
	}
}

// ProjectTokenUsage 从整份 v4 日志的原始帧折叠出 `tokenUsage`（纯读，零写盘）。
//
// `Measured` 在折叠途中顺手记下（官方 wire 没有这一位，见 TokenUsageProjection 的
// 说明）；四桶与官方 `view(state) = state.totals` 逐字一致。
func ProjectTokenUsage(events []v4.Event) TokenUsageProjection {
	state := tokenUsageState{}
	measured := false
	for _, event := range events {
		next, sampled := applyTokenUsage(state, event)
		state = next
		if sampled {
			measured = true
		}
	}
	return TokenUsageProjection{Buckets: state.Totals, Measured: measured}
}

// applyTokenUsage 是官方 `apply`（`src/usage-projection.ts:116-142`）的逐条移植。
//
// 第二个返回值表示"这条事件是一条被采纳的 usage 样本"（即使四桶与上一次完全相同、
// 状态没变）——`Measured` 要的就是这个事实，而不是"状态变了没有"。
func applyTokenUsage(state tokenUsageState, event v4.Event) (tokenUsageState, bool) {
	if event.Type == eventLLMRetryStarted {
		turn, step, ok := tokenUsageCoordinates(event.Data)
		if !ok || state.Last == nil || state.Last.Turn != turn || state.Last.Step != step {
			return state, false
		}
		// 官方：重试开始 ⇒ 关掉替换槽。下一次 attempt 的用量必须**再加一次**，
		// 否则"重试之后真实发生的用量"会被替换语义抹掉。
		state.Last = nil
		return state, false
	}
	if event.Type != eventAssistantMessage && event.Type != eventAssistantAttempt {
		// 官方：只有 assistant 结算事件带 usage 样本。
		return state, false
	}
	usage, ok := tokenUsageSampleOf(event)
	if !ok {
		// provider 没上报用量：状态原样返回（**不写一条 0 的假账**）。
		return state, false
	}
	turn, step, ok := tokenUsageCoordinates(event.Data)
	if !ok {
		// 说不清属于哪一步的结算事件：凭空多算一次用量比少算更坏，跳过。
		return state, false
	}
	buckets := usage.buckets()
	var previous *UsageBuckets
	if state.Last != nil && state.Last.Turn == turn && state.Last.Step == step {
		prior := state.Last.Buckets
		previous = &prior
		if prior == buckets {
			// 同一 {turn,step} 上完全相同的重复样本：状态原样返回
			// （官方 `bucketsEqual` 之后 `return state`，靠 `Object.is` 门住变更流）。
			return state, true
		}
	}
	state.Totals = addReplacingUsage(state.Totals, previous, buckets)
	state.Last = &tokenUsageSample{Turn: turn, Step: step, Buckets: buckets}
	return state, true
}

// addReplacingUsage 是官方 `addReplacing`（`src/usage-projection.ts:34-43`）：
// 先把上一次同 {turn,step} 的样本减掉，再加上新样本。
//
// 为什么不加 `max(0, …)` 夹：官方的替换是**精确**的（减掉的就是当初加进去的那份），
// 夹负会把"替换前后不一致"这种真 bug 藏起来。四桶都是非负整数，减多减少都能在
// 测试里一眼看出来。
func addReplacingUsage(totals UsageBuckets, previous *UsageBuckets, next UsageBuckets) UsageBuckets {
	var prior UsageBuckets
	if previous != nil {
		prior = *previous
	}
	return UsageBuckets{
		UncachedInputTokens: totals.UncachedInputTokens - prior.UncachedInputTokens + next.UncachedInputTokens,
		OutputTokens:        totals.OutputTokens - prior.OutputTokens + next.OutputTokens,
		CacheReadTokens:     totals.CacheReadTokens - prior.CacheReadTokens + next.CacheReadTokens,
		CacheWriteTokens:    totals.CacheWriteTokens - prior.CacheWriteTokens + next.CacheWriteTokens,
	}
}

// tokenUsageSampleOf 是官方 `usageOf`（`src/usage-projection.ts:82-86`）：
// **只有 `assistant/message`** 的顶层 `data.usage` 是直接样本；`assistant/message`
// 与 `assistant/attempt` 两者都可以从 compact `stream` 里最后一条 `usage` chunk 取。
//
// 为什么 `assistant/attempt` 不看顶层 `usage`：官方逐字就是这么写的——attempt 是
// "这一轮尝试"的中间结算，它的用量只在流里出现过。多认一个字段的后果是同一段用量
// 可能被 attempt 与随后的 message 各折一次（官方不会）。
func tokenUsageSampleOf(event v4.Event) (v4Usage, bool) {
	if event.Type != eventAssistantMessage && event.Type != eventAssistantAttempt {
		// 官方：只有 assistant 结算事件带 usage 样本。
		return v4Usage{}, false
	}
	if event.Type == eventAssistantMessage {
		if usage, ok := assistantEventUsage(event); ok {
			return usage, true
		}
	}
	return assistantStreamUsage(event.Data)
}

// assistantEventUsage 取 `assistant/message` 的顶层 `data.usage`。
//
// 与 `usageOfEvent`（usage_projection.go）语义相同但**只认一种事件**：那条折叠
// 关心"最近一次真正结算的请求"，这里关心"这条消息是否带了一份用量"。
func assistantEventUsage(event v4.Event) (v4Usage, bool) {
	var data struct {
		Usage *v4Usage `json:"usage"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil || data.Usage == nil {
		return v4Usage{}, false
	}
	return *data.Usage, true
}

// assistantStreamUsage 复刻官方 `lastAssistantStreamChunk(stream, 'usage')`
// （`@deepseek-ai/dsh-llm/src/assistant-stream.ts:373-382`）：从后往前找第一条
// `type == "chunk"` 且 `chunk.type == "usage"` 的记录，取它的 `usage`。
//
// 打包 run（`text-chunks` / `reasoning-chunks` / `tool-call-chunks`）里不含 usage，
// 所以只认裸 chunk，与官方逐字一致。
func assistantStreamUsage(data json.RawMessage) (v4Usage, bool) {
	var envelope struct {
		Stream []json.RawMessage `json:"stream"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return v4Usage{}, false
	}
	for index := len(envelope.Stream) - 1; index >= 0; index-- {
		record, ok := parseAssistantStreamRecord(envelope.Stream[index])
		if !ok || record.Type != "chunk" || len(record.Chunk) == 0 {
			continue
		}
		var chunk struct {
			Type  string   `json:"type"`
			Usage *v4Usage `json:"usage"`
		}
		if err := json.Unmarshal(record.Chunk, &chunk); err != nil || chunk.Type != "usage" || chunk.Usage == nil {
			continue
		}
		return *chunk.Usage, true
	}
	return v4Usage{}, false
}

// tokenUsageCoordinates 取事件的 `{turn, step}`。
//
// 缺字段按"读不懂"处理（ok=false）：一条说不清属于哪一步的样本不能计入任何一步，
// 否则同一段用量会在下一次 `step/start` 之后被当成"新样本"再加一遍。
func tokenUsageCoordinates(data json.RawMessage) (turn, step int64, ok bool) {
	return sessionStatsCoordinates(data)
}

// toMap 把 Go 视图展开成 state map（官方状态是纯 JSON，persisted-cache 前置条件）。
func (s tokenUsageState) toMap() map[string]any {
	var last any
	if s.Last != nil {
		last = map[string]any{
			"turn":    s.Last.Turn,
			"step":    s.Last.Step,
			"buckets": s.Last.Buckets.toMap(),
		}
	}
	return map[string]any{
		"totals": s.Totals.toMap(),
		"last":   last,
	}
}

// toMap 把四桶展开成官方 JSON 键名（与 wire view 逐字一致）。
func (b UsageBuckets) toMap() map[string]any {
	return map[string]any{
		"uncachedInputTokens": b.UncachedInputTokens,
		"outputTokens":        b.OutputTokens,
		"cacheReadTokens":     b.CacheReadTokens,
		"cacheWriteTokens":    b.CacheWriteTokens,
	}
}

// readTokenUsageState 把 state map 读回 Go 视图。
//
// 读不懂的状态按"全新的空状态"处理（与 `readSessionStatsState` 同一条纪律）：
// 一条读不懂的记录不该让整份投影说错话。
func readTokenUsageState(state any) tokenUsageState {
	fields, ok := state.(map[string]any)
	if !ok {
		return tokenUsageState{}
	}
	read := tokenUsageState{Totals: readUsageBuckets(fields["totals"])}
	if raw, ok := fields["last"].(map[string]any); ok {
		read.Last = &tokenUsageSample{
			Turn:    sessionStatsInt64(raw["turn"]),
			Step:    sessionStatsInt64(raw["step"]),
			Buckets: readUsageBuckets(raw["buckets"]),
		}
	}
	return read
}

// readUsageBuckets 把一个 JSON 对象读回四桶（缺字段按 0，非负由 schema 保证）。
func readUsageBuckets(value any) UsageBuckets {
	fields, ok := value.(map[string]any)
	if !ok {
		return UsageBuckets{}
	}
	return UsageBuckets{
		UncachedInputTokens: int(sessionStatsInt64(fields["uncachedInputTokens"])),
		OutputTokens:        int(sessionStatsInt64(fields["outputTokens"])),
		CacheReadTokens:     int(sessionStatsInt64(fields["cacheReadTokens"])),
		CacheWriteTokens:    int(sessionStatsInt64(fields["cacheWriteTokens"])),
	}
}
