package session

import (
	"encoding/json"
	"fmt"
	"math"

	"dshgo/codecs/v4"
	"dshgo/internal/storage"
)

// `sessionStats` 投影：整份日志的轮/步计数与墙钟时间。
//
// 官方证据（`@deepseek-ai/dsh-session-stats`）：
//   - 折叠语义（模块注释）：`lib/types/projection.js:1-24`，源 `src/projection.ts:1-24`；
//   - wire schema（**strict**，八个字段全部非负）：`lib/types/projection.js:27-36`；
//   - state schema（wire 的超集：lastTurn / openStep / pendingCalls）：同文件 `:43-52`；
//   - `apply` 逐条：同文件 `:83-153`；
//   - `wire.view`：同文件 `:154-167`；
//   - 字段语义（每个字段一行）：`src/types.ts:24-41`；
//   - 首个 token 的重建：`@deepseek-ai/dsh-llm/src/assistant-stream.ts:245-345`
//     （`isTokenDelta` / `runFirstTokenTime` / `assistantStreamFirstTokenTime`）。
//
// 三条折叠纪律（官方注释逐字要点，抄在代码旁而不是只写在文档里）：
//
//  1. **`step/end` 才是 step 计数权威**，不是 `assistant/message`。它是 step 生命
//     周期的权威：loop 每个进入的 step 在 `finally` 里恰好 append 一条，所以完成、
//     失败、取消、max-tokens 的 step 都各算一步。数组装好的 assistant 消息会
//     **多算** max-tokens 的 usage-host 消息（空内容、不在 surface 上），并**少算**
//     被取消的 step（消息还没组装就 aborted 了）。
//
//  2. 墙钟折叠逐字段对齐客户端的窗口折叠（`deriveStats`，`dsh-client-ui-conversation`；
//     本投影是那份折叠的"整窗兜底"角色）：model 时间 = `step/start` → `assistant/message`；
//     first token = 第一个非空 delta chunk，且**跨 step 内的 `llm/retry` 存活**
//     （`llm/retry` 不在 switch 里，状态原样保留）；decode = first token → 组装消息，
//     仅在那条 step 同时报告 output tokens 时累加；tool 时间按 `callId` 配对
//     `tool/call` → `tool/result`。
//
//  3. 取消的 step 不组装消息，所以它的 partial stream 时间在**任何**时间数字里都
//     不计——与客户端窗口一致：那边把它画成一个不计时的 interrupted 节点。
//
// 本文件只做**纯派生**：从 v4 原始帧折叠，读路径不写盘（R5 红线）。所有数字都能
// 在日志里指回一条真实事件；没有事件来源时它是 0（"没有发生过"），不是"猜的值"。

// ProjectionKeySessionStats 是官方投影键（`src/types.ts:50`）。
const ProjectionKeySessionStats = "sessionStats"

// SessionStatsStateVersion 是官方 stateVersion（`src/projection.ts:109`）。
const SessionStatsStateVersion = 1

// 折叠涉及的事件名。`eventStepStart` / `eventTurnEnd` 已在 projection.go 里声明为
// 包内常量（投影缓存与列表元数据也用它们）；这里补本投影独有的几条（全部是官方
// v4 词汇表成员，见 known_events.go 的 officialEventTypes）。
const (
	eventStepEnd          = "step/end"
	eventAssistantAttempt = "assistant/attempt"
	eventAssistantMessage = "assistant/message"
	eventToolCall         = "tool/call"
	eventToolResult       = "tool/result"
	// eventAssistantReasoning 是宿主自有 durable 增量（官方把增量放在
	// `assistant/attempt` / `assistant/message` 的 `stream` 里，宿主写成独立的
	// `assistant/text` | `assistant/reasoning`，见 cmd/dshd/main.go 的 Emit switch）。
	eventAssistantReasoning = "assistant/reasoning"
)

// SessionStats 是官方 wire view 的八个字段（`src/types.ts:24-41`）。
//
// 字段名与官方 zod 键**逐字一致**（JSON 标签就是 wire 契约）；turns / steps /
// ttftSteps 是整数，其余四个毫秒数与 decodeTokens 官方类型是 number。
type SessionStats struct {
	// Turns 是"至少闭合过一个 step"的不同轮次数（`step/end` 去重）。
	Turns int64 `json:"turns"`
	// Steps 是已闭合的 step 数（`step/end` 条数，完成/失败/取消一视同仁）。
	Steps int64 `json:"steps"`
	// LLMMs 是 model 墙钟之和（`step/start` → `assistant/message`），毫秒。
	LLMMs int64 `json:"llmMs"`
	// ToolMs 是配对成功的 `tool/call` → `tool/result` 墙钟之和，毫秒。
	ToolMs int64 `json:"toolMs"`
	// TTFTMs 是首 token 延迟之和（`step/start` → 第一个非空 delta），毫秒。
	TTFTMs int64 `json:"ttftMs"`
	// TTFTSteps 是记录了首 token 的 step 数。
	TTFTSteps int64 `json:"ttftSteps"`
	// DecodeMs 是 decode 墙钟之和（first token → 组装消息），毫秒。
	DecodeMs int64 `json:"decodeMs"`
	// DecodeTokens 是同批 step 上 provider 上报的 output tokens 之和。
	DecodeTokens int64 `json:"decodeTokens"`
}

// 官方 wire schema 的两档数字校验（zod：`z.number().int().nonnegative()` 与
// `z.number().nonnegative()`）。
//
// 为什么要区分：checkpoint 行从 JSON 读回来时数字是 `json.Number` / `float64`，
// 而 int 字段在官方 schema 里**只接受整数**——`12.5` 个 step 是不存在的形状，
// 静默接受它会让"一步"变成"半天"。
func nonNegativeIntegerSchema(field string) storage.Schema {
	return storage.SchemaFunc(func(value any) error {
		number, ok := sessionStatsNumber(value)
		if !ok {
			return fmt.Errorf("%s 期望非负整数，收到 %T", field, value)
		}
		if math.Trunc(number) != number {
			return fmt.Errorf("%s 期望整数，收到 %v", field, number)
		}
		if number < 0 {
			return fmt.Errorf("%s 期望非负整数，收到 %v", field, number)
		}
		return nil
	})
}

func nonNegativeNumberSchema(field string) storage.Schema {
	return storage.SchemaFunc(func(value any) error {
		number, ok := sessionStatsNumber(value)
		if !ok {
			return fmt.Errorf("%s 期望非负 number，收到 %T", field, value)
		}
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return fmt.Errorf("%s 期望有限 number，收到 %v", field, number)
		}
		if number < 0 {
			return fmt.Errorf("%s 期望非负 number，收到 %v", field, number)
		}
		return nil
	})
}

// sessionStatsNumber 把 JSON 世界里的三种数字形状统一成 float64。
func sessionStatsNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
}

// sessionStatsTotalsSchema 是官方 wire schema（`lib/types/projection.js:27-36`）的
// 八个字段——**输出边界**（wire view 只允许这八个键，且全部非负）。
//
// 它是 state schema 的基座：官方 `sessionStatsStateSchema` 用 `extend` 在它上面加
// 三个折叠边界字段（lastTurn / openStep / pendingCalls）。state 是 wire 的严格超集，
// 所以两档共用同一份字段声明——两处各抄一遍就是下一次"两处各错一次"的来源。
func sessionStatsTotalsSchema() map[string]storage.Schema {
	return map[string]storage.Schema{
		"turns":        nonNegativeIntegerSchema("turns"),
		"steps":        nonNegativeIntegerSchema("steps"),
		"llmMs":        nonNegativeNumberSchema("llmMs"),
		"toolMs":       nonNegativeNumberSchema("toolMs"),
		"ttftMs":       nonNegativeNumberSchema("ttftMs"),
		"ttftSteps":    nonNegativeIntegerSchema("ttftSteps"),
		"decodeMs":     nonNegativeNumberSchema("decodeMs"),
		"decodeTokens": nonNegativeNumberSchema("decodeTokens"),
	}
}

// sessionStatsOpenStepSchema 是一条 step 的折叠边界事实
// （官方 `openStep: {turn, step, startTime, firstTokenTime}`）。
var sessionStatsOpenStepSchema = storage.Object(map[string]storage.Schema{
	"turn":           nonNegativeIntegerSchema("openStep.turn"),
	"step":           nonNegativeIntegerSchema("openStep.step"),
	"startTime":      nonNegativeNumberSchema("openStep.startTime"),
	"firstTokenTime": storage.Nullable(nonNegativeNumberSchema("openStep.firstTokenTime")),
})

// sessionStatsStateSchema 是官方 state schema（`lib/types/projection.js:43-52`）：
// wire 八字段 + lastTurn + openStep + pendingCalls。
//
// 为什么 state 也要过 schema：它是**持久化**的行（`session_projcache`），读回来必须
// 能证明"这份状态是这条投影能产出的形状"，否则一行坏记录会让整份快照说错话。
var sessionStatsStateSchema = func() storage.Schema {
	fields := sessionStatsTotalsSchema()
	fields["lastTurn"] = storage.Nullable(nonNegativeIntegerSchema("lastTurn"))
	fields["openStep"] = storage.Nullable(sessionStatsOpenStepSchema)
	// pendingCalls 是 `Record<string, number>`：callId → 派发时间。
	fields["pendingCalls"] = storage.Record(nonNegativeNumberSchema("pendingCalls"))
	return storage.Object(fields)
}()

// SessionStatsUnit 返回官方 `sessionStats` 投影单元（stateVersion 1）。
func SessionStatsUnit() ProjectionUnit {
	return ProjectionUnit{
		Key:          ProjectionKeySessionStats,
		StateVersion: SessionStatsStateVersion,
		Schema:       sessionStatsStateSchema,
		Init: func(v4.Header, int64) any {
			return newSessionStatsState().toMap()
		},
		Apply: applySessionStats,
		View: func(state any) any {
			return readSessionStatsState(state).view()
		},
	}
}

// ProjectSessionStats 从整份 v4 日志的原始帧折叠出 `sessionStats`（纯读，零写盘）。
//
// 这是 `session.projections` 那条读路径的唯一入口：HTTP unary、follow 基线、
// `session.control` 三处必须**同一份折叠**，否则同一个会话会说出三种事实。
func ProjectSessionStats(events []v4.Event) SessionStats {
	unit := SessionStatsUnit()
	state := unit.Init(v4.Header{}, 0)
	for _, event := range events {
		state = unit.Apply(state, event)
	}
	view, _ := unit.View(state).(SessionStats)
	return view
}

// sessionStatsOpenStep 是折叠边界：当前打开 step 的事实。
type sessionStatsOpenStep struct {
	Turn           int64
	Step           int64
	StartTime      int64
	FirstTokenTime *int64
}

// sessionStatsState 是 state map 的 Go 视图（官方 `SessionStatsState`）。
type sessionStatsState struct {
	SessionStats
	// LastTurn 是最后一条被计数的 `step/end` 的轮次；null = 还没数过。
	LastTurn *int64
	// OpenStep 是打开中的 step 边界；null = 不在 step 里，或它的消息已组装。
	OpenStep *sessionStatsOpenStep
	// PendingCalls 是"结果还没落地"的工具调用派发时间，按 callId 索引。
	PendingCalls map[string]int64
}

func newSessionStatsState() sessionStatsState {
	return sessionStatsState{PendingCalls: map[string]int64{}}
}

// toMap 把 Go 视图展开成 state map（官方状态是纯 JSON，persisted-cache 前置条件）。
func (s sessionStatsState) toMap() map[string]any {
	var openStep any
	if s.OpenStep != nil {
		var firstTokenTime any
		if s.OpenStep.FirstTokenTime != nil {
			firstTokenTime = *s.OpenStep.FirstTokenTime
		}
		openStep = map[string]any{
			"turn":           s.OpenStep.Turn,
			"step":           s.OpenStep.Step,
			"startTime":      s.OpenStep.StartTime,
			"firstTokenTime": firstTokenTime,
		}
	}
	var lastTurn any
	if s.LastTurn != nil {
		lastTurn = *s.LastTurn
	}
	pendingCalls := make(map[string]any, len(s.PendingCalls))
	for callID, dispatched := range s.PendingCalls {
		pendingCalls[callID] = dispatched
	}
	return map[string]any{
		"turns":        s.Turns,
		"steps":        s.Steps,
		"llmMs":        s.LLMMs,
		"toolMs":       s.ToolMs,
		"ttftMs":       s.TTFTMs,
		"ttftSteps":    s.TTFTSteps,
		"decodeMs":     s.DecodeMs,
		"decodeTokens": s.DecodeTokens,
		"lastTurn":     lastTurn,
		"openStep":     openStep,
		"pendingCalls": pendingCalls,
	}
}

// view 是官方 wire view（`lib/types/projection.js:154-167`）：state 的严格子集。
func (s sessionStatsState) view() SessionStats { return s.SessionStats }

// readSessionStatsState 把 state map 读回 Go 视图。
//
// 读不懂的状态按"全新的空状态"处理（与 conversationContinuationFields 同一条纪律：
// 一条读不懂的事件不该让整份投影说错话）。**返回的 PendingCalls 与 state 共享底层
// map**——需要改它的调用方必须先克隆（见 clonePendingCalls），否则折叠器用
// `reflect.DeepEqual` 比较 before/after 时会看不见这次改动。
func readSessionStatsState(state any) sessionStatsState {
	read := newSessionStatsState()
	fields, ok := state.(map[string]any)
	if !ok {
		return read
	}
	read.Turns = sessionStatsInt64(fields["turns"])
	read.Steps = sessionStatsInt64(fields["steps"])
	read.LLMMs = sessionStatsInt64(fields["llmMs"])
	read.ToolMs = sessionStatsInt64(fields["toolMs"])
	read.TTFTMs = sessionStatsInt64(fields["ttftMs"])
	read.TTFTSteps = sessionStatsInt64(fields["ttftSteps"])
	read.DecodeMs = sessionStatsInt64(fields["decodeMs"])
	read.DecodeTokens = sessionStatsInt64(fields["decodeTokens"])
	if lastTurn, ok := sessionStatsOptionalInt64(fields["lastTurn"]); ok {
		read.LastTurn = lastTurn
	}
	if raw, ok := fields["openStep"].(map[string]any); ok {
		open := sessionStatsOpenStep{
			Turn:      sessionStatsInt64(raw["turn"]),
			Step:      sessionStatsInt64(raw["step"]),
			StartTime: sessionStatsInt64(raw["startTime"]),
		}
		if firstTokenTime, ok := sessionStatsOptionalInt64(raw["firstTokenTime"]); ok {
			open.FirstTokenTime = firstTokenTime
		}
		read.OpenStep = &open
	}
	if raw, ok := fields["pendingCalls"].(map[string]any); ok {
		read.PendingCalls = make(map[string]int64, len(raw))
		for callID, dispatched := range raw {
			read.PendingCalls[callID] = sessionStatsInt64(dispatched)
		}
	}
	return read
}

func sessionStatsInt64(value any) int64 {
	number, ok := sessionStatsNumber(value)
	if !ok {
		return 0
	}
	return int64(number)
}

func sessionStatsOptionalInt64(value any) (*int64, bool) {
	if value == nil {
		return nil, false
	}
	number, ok := sessionStatsNumber(value)
	if !ok {
		return nil, false
	}
	parsed := int64(number)
	return &parsed, true
}

// clonePendingCalls 克隆派发表（折叠是**纯**的：绝不原地改调用方交进来的状态）。
func clonePendingCalls(pending map[string]int64) map[string]int64 {
	clone := make(map[string]int64, len(pending)+1)
	for callID, dispatched := range pending {
		clone[callID] = dispatched
	}
	return clone
}

// nonNegativeDelta 是官方的 `Math.max(0, later - earlier)`：墙钟只加不减。
//
// provider 的时间戳偶有回退（时钟漂移、日志被重排），负的墙钟不是"负耗时"，
// 是"这段测量不可信"——按 0 记，与客户端窗口折叠同一条纪律。
func nonNegativeDelta(later, earlier int64) int64 {
	if later <= earlier {
		return 0
	}
	return later - earlier
}

// applySessionStats 是官方 `apply`（`lib/types/projection.js:83-153`）的逐条移植。
//
// 无关键一律**原样返回同一个 state**（官方注释：`Object.is` 是变更流的门；这里
// 折叠器用 `reflect.DeepEqual` 达到同一效果）。
func applySessionStats(state any, event v4.Event) any {
	switch event.Type {
	case eventStepStart:
		turn, step, ok := sessionStatsCoordinates(event.Data)
		if !ok {
			return state
		}
		// 官方：每个 step/start 无条件替换边界（前一步没闭合也不能把时间算到这一步）。
		current := readSessionStatsState(state)
		current.OpenStep = &sessionStatsOpenStep{Turn: turn, Step: step, StartTime: event.Time}
		return current.toMap()

	case eventAssistantAttempt:
		current := readSessionStatsState(state)
		open := current.OpenStep
		if open == nil {
			return state
		}
		turn, step, ok := sessionStatsCoordinates(event.Data)
		if !ok || turn != open.Turn || step != open.Step {
			return state
		}
		// 首个 token 只记**第一次**：step 内重试（llm/retry 之后的第二次 attempt）
		// 不许把已经记下的 first token 推后。
		if open.FirstTokenTime != nil {
			return state
		}
		first, ok := assistantStreamFirstTokenTime(event.Data)
		if !ok {
			return state
		}
		updated := *open
		updated.FirstTokenTime = &first
		current.OpenStep = &updated
		return current.toMap()

	case eventAssistantMessage:
		current := readSessionStatsState(state)
		open := current.OpenStep
		if open == nil {
			return state
		}
		turn, step, ok := sessionStatsCoordinates(event.Data)
		if !ok || turn != open.Turn || step != open.Step {
			return state
		}
		firstToken := open.FirstTokenTime
		if firstToken == nil {
			// 官方兜底：attempt 没记过（或宿主根本不写 assistant/attempt）时，
			// 从这条消息自己的 stream 里取第一个 token。
			if first, ok := assistantStreamFirstTokenTime(event.Data); ok {
				firstToken = &first
			}
		}
		// 一个 step 只组装一条消息：关掉边界之后，防御性的重复消息不可能二次累加。
		current.LLMMs += nonNegativeDelta(event.Time, open.StartTime)
		current.OpenStep = nil
		if firstToken != nil {
			current.TTFTMs += nonNegativeDelta(*firstToken, open.StartTime)
			current.TTFTSteps++
			if outputTokens, ok := sessionStatsOutputTokens(event.Data); ok {
				current.DecodeMs += nonNegativeDelta(event.Time, *firstToken)
				current.DecodeTokens += outputTokens
			}
		}
		return current.toMap()

	case eventToolCall:
		callID, ok := sessionStatsCallID(event.Data)
		if !ok {
			return state
		}
		current := readSessionStatsState(state)
		current.PendingCalls = clonePendingCalls(current.PendingCalls)
		current.PendingCalls[callID] = event.Time
		return current.toMap()

	case eventToolResult:
		callID, ok := sessionStatsResultCallID(event.Data)
		if !ok {
			return state
		}
		current := readSessionStatsState(state)
		// 自有键判定：callId 是 provider 铸造的（模型/工具 JSON 边界），所以一条
		// 没有记录过调用的结果必须读成"未配对"。Go map 没有原型链，`constructor`
		// / `toString` 天然读不到——官方要防的那条 NaN 污染在 Go 这边不存在。
		dispatched, ok := current.PendingCalls[callID]
		if !ok {
			return state
		}
		current.ToolMs += nonNegativeDelta(event.Time, dispatched)
		pending := clonePendingCalls(current.PendingCalls)
		delete(pending, callID)
		current.PendingCalls = pending
		return current.toMap()

	case eventStepEnd:
		turn, _, ok := sessionStatsCoordinates(event.Data)
		if !ok {
			return state
		}
		current := readSessionStatsState(state)
		// 轮次号由宿主分配且每会话单调，"这条 step/end 属于新的一轮吗"只需要
		// 一个 lastTurn 槽位。
		if current.LastTurn == nil || *current.LastTurn != turn {
			current.Turns++
			current.LastTurn = &turn
		}
		current.Steps++
		current.OpenStep = nil
		return current.toMap()

	case eventTurnEnd:
		// 结果一直没落地的调用属于被取消/失败的轮：结果总在自己的轮内落地，
		// 所以这里丢掉残留，而不是让持久化状态无限长大。
		current := readSessionStatsState(state)
		if len(current.PendingCalls) == 0 {
			return state
		}
		current.PendingCalls = map[string]int64{}
		return current.toMap()

	case EventAssistantText, eventAssistantReasoning:
		// 宿主自有 durable 增量：官方把增量放在 `assistant/attempt` / `assistant/message`
		// 的 `stream` 里，宿主写成独立的 `assistant/text` | `assistant/reasoning` 事件
		// （`cmd/dshd/main.go` 的 Emit switch、`cmd/dshd/serve_turn.go` 同一份映射）。
		// 它们就是"第一个非空 delta chunk"在宿主日志里的等价物——不折叠它们，
		// 宿主的 ttftMs/ttftSteps 会恒为 0（界面就只能一直显示"未知"）。
		var data struct {
			Text *string `json:"text"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil || data.Text == nil || *data.Text == "" {
			// 官方 isTokenDelta 只认 `text !== ''`：空 delta 不是 token。
			return state
		}
		current := readSessionStatsState(state)
		open := current.OpenStep
		if open == nil || open.FirstTokenTime != nil {
			return state
		}
		turn, step, ok := sessionStatsCoordinates(event.Data)
		if !ok || turn != open.Turn || step != open.Step {
			return state
		}
		first := event.Time
		updated := *open
		updated.FirstTokenTime = &first
		current.OpenStep = &updated
		return current.toMap()

	default:
		return state
	}
}

// sessionStatsCoordinates 取事件的 `{turn, step}`。
//
// 缺字段按"读不懂"处理（ok=false）：一条说不清属于哪一步的边界事件不能计入任何
// 一步，否则 steps 会凭空多出一条。
func sessionStatsCoordinates(data json.RawMessage) (turn, step int64, ok bool) {
	var coordinates struct {
		Turn *int64 `json:"turn"`
		Step *int64 `json:"step"`
	}
	if err := json.Unmarshal(data, &coordinates); err != nil {
		return 0, 0, false
	}
	if coordinates.Turn == nil || coordinates.Step == nil {
		return 0, 0, false
	}
	return *coordinates.Turn, *coordinates.Step, true
}

// sessionStatsCallID 取 `tool/call.callId`。
func sessionStatsCallID(data json.RawMessage) (string, bool) {
	var payload struct {
		CallID *string `json:"callId"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.CallID == nil || *payload.CallID == "" {
		return "", false
	}
	return *payload.CallID, true
}

// sessionStatsResultCallID 取 `tool/result.message.source.callId`（官方同一字段路径）。
//
// 为什么只认这一条：官方 `tool/result` 的 apply 就是 `event.data.message.source.callId`。
// 换成别的字段（例如 `message.toolCallId`）会让"官方日志能配、宿主日志配不上"或者
// 反过来——两条日志必须配出同一个 toolMs。
func sessionStatsResultCallID(data json.RawMessage) (string, bool) {
	var payload struct {
		Message struct {
			Source struct {
				CallID *string `json:"callId"`
			} `json:"source"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", false
	}
	if payload.Message.Source.CallID == nil || *payload.Message.Source.CallID == "" {
		return "", false
	}
	return *payload.Message.Source.CallID, true
}

// sessionStatsOutputTokens 取 provider 上报的 completion tokens
// （官方 `usageOutputTokens`：缺省/非法都返回"未报告"）。
func sessionStatsOutputTokens(data json.RawMessage) (int64, bool) {
	var payload struct {
		Usage *struct {
			OutputTokens *json.Number `json:"outputTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Usage == nil || payload.Usage.OutputTokens == nil {
		return 0, false
	}
	number, err := payload.Usage.OutputTokens.Float64()
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) || number < 0 {
		return 0, false
	}
	return int64(number), true
}

// assistantStreamFirstTokenTime 复刻官方 `assistantStreamFirstTokenTime`
// （`@deepseek-ai/dsh-llm/src/assistant-stream.ts:333-343`）：逐条记录找第一个
// 算 token 的成员，找到就停。
//
// 宿主自己写的 `assistant/message.stream` 只有 `{type:"finish"}`（没有 chunk），
// 所以宿主日志走 `assistant/text` 那条桥；官方日志（含 `assistant/attempt` 的
// compact stream）走这里。
func assistantStreamFirstTokenTime(data json.RawMessage) (int64, bool) {
	var envelope struct {
		Stream []json.RawMessage `json:"stream"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return 0, false
	}
	for _, raw := range envelope.Stream {
		record, ok := parseAssistantStreamRecord(raw)
		if !ok {
			continue
		}
		if time, ok := record.firstTokenTime(); ok {
			return time, true
		}
	}
	return 0, false
}

// assistantStreamRecord 是 compact stream 的一条记录（官方
// `AssistantStreamRecord = {type:'chunk', time, chunk} | 打包 run`）。
type assistantStreamRecord struct {
	Type  string          `json:"type"`
	Time  int64           `json:"time"`
	Chunk json.RawMessage `json:"chunk"`
	Time0 int64           `json:"time0"`
	DT    []int64         `json:"dt"`
	Texts []string        `json:"texts"`
	Args  []string        `json:"args"`
	Name  *string         `json:"name"`
}

func parseAssistantStreamRecord(raw json.RawMessage) (assistantStreamRecord, bool) {
	var record assistantStreamRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Type == "" {
		return assistantStreamRecord{}, false
	}
	return record, true
}

// firstTokenTime 复刻官方 `assistantStreamFirstTokenTime` 的单条语义：
// 裸 chunk 用 `isTokenDelta` 判，打包 run 用 `runFirstTokenTime` 重建成员时间。
func (r assistantStreamRecord) firstTokenTime() (int64, bool) {
	if r.Type == "chunk" {
		if sessionStatsTokenDeltaChunk(r.Chunk) {
			return r.Time, true
		}
		return 0, false
	}
	// 打包 run（官方 `runFirstTokenTime`，:312-315）：带名字的 tool-call run
	// 从它的**第一个成员**开始算，否则取第一个非空 fragment。
	if r.Type == "tool-call-chunks" && r.Name != nil {
		return r.Time0, true
	}
	fragments := r.Texts
	if r.Type == "tool-call-chunks" {
		fragments = r.Args
	}
	time := r.Time0
	for index, fragment := range fragments {
		if index > 0 {
			if index-1 >= len(r.DT) {
				// dt 长度不够就重建不出这条成员的时间（官方会读到 undefined 并
				// 得到 NaN）。宁可停在这里（漏一个 token 时间），也不返回一个假时间。
				return 0, false
			}
			time += r.DT[index-1]
		}
		if fragment != "" {
			return time, true
		}
	}
	return 0, false
}

// sessionStatsTokenDeltaChunk 复刻官方 `isTokenDelta`
// （`@deepseek-ai/dsh-llm/src/assistant-stream.ts:245-260`）。
func sessionStatsTokenDeltaChunk(raw json.RawMessage) bool {
	var chunk struct {
		Type           string  `json:"type"`
		Text           *string `json:"text"`
		ArgumentsDelta *string `json:"argumentsDelta"`
		Name           *string `json:"name"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return false
	}
	switch chunk.Type {
	case "text-delta", "reasoning-delta":
		return chunk.Text != nil && *chunk.Text != ""
	case "tool-call-delta":
		return (chunk.ArgumentsDelta != nil && *chunk.ArgumentsDelta != "") || chunk.Name != nil
	default:
		return false
	}
}
