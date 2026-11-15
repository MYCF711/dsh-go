package session

import (
	"bytes"
	"encoding/json"

	"dshgo/codecs/v4"
	"dshgo/internal/storage"
)

// `conversationContinuation` 投影：官方 rc.2 新增的"空 prompt 可续跑"判定状态。
//
// 官方证据（`dsh-api-session-controller/lib/index.js`）：
//   - 定义 `:479-513`（key `conversationContinuation`、`stateVersion: 4`、init/apply/wire view）；
//   - 注册 `:706`；
//   - 可续错误码集 `:459-466`；
//   - 使用处 `:895`：空 content 且 `mode === "queue"` 时，只有
//     `ending !== null && inbox.nextTurn.length === 0 && 没有 pending 的 user 来源消息`
//     才允许续跑，否则 `session/agent-busy` + `details.reason = "NO_CONTINUABLE_FAILURE_OR_PENDING_INPUT"`。
//
// 投影值 = 可续跑的最新失败 `turn/end` 的 seq，或 null。它是官方 UI "重试"按钮的
// 宿主侧依赖：没有这个状态，空 prompt 只能 400。
const ProjectionKeyConversationContinuation = "conversationContinuation"

// ContinuableFailureCodes 是官方允许"空 prompt 续跑"的 6 个错误码
// （`dsh-api-session-controller/lib/index.js:459-466`，逐字）。
var ContinuableFailureCodes = []string{
	"RATE_LIMIT",
	"SERVER",
	"TIMEOUT",
	"TRANSPORT",
	"EMPTY_RESPONSE",
	"STREAM_CLOSED",
}

// IsContinuableFailure 判断错误码是否在官方可续集里。
func IsContinuableFailure(code string) bool {
	for _, candidate := range ContinuableFailureCodes {
		if candidate == code {
			return true
		}
	}
	return false
}

// conversationContinuationSchema 是官方 stateSchema 的 Go 形状：
// `{admitted: boolean, failedAttempt: boolean, ending: number|null}`。
var conversationContinuationSchema = storage.Object(map[string]storage.Schema{
	"admitted":      storage.Boolean(),
	"failedAttempt": storage.Boolean(),
	"ending":        storage.Nullable(storage.Integer()),
})

// ConversationContinuationUnit 返回官方 `conversationContinuation` 投影单元（stateVersion 4）。
//
// 四个字段语义照官方 apply 逐条抄：
//   - `turn/start` 清空（新一轮开始，旧的失败轮不再是"最近失败"）；
//   - `step/start` / `assistant/message` 清 `failedAttempt`（新一轮尝试已经成功开始）；
//   - `user/message` 且 append surface 上：(user + 非空 content) 或 (retry + 空 content) → `admitted`；
//   - `assistant/attempt` → `failedAttempt`；
//   - `turn/end` 且 `reason.kind === 'error'` 且错误码可续 且 admitted 且 failedAttempt
//     → `ending = seq`，否则清空。
func ConversationContinuationUnit() ProjectionUnit {
	return ProjectionUnit{
		Key:          ProjectionKeyConversationContinuation,
		StateVersion: 4,
		Schema:       conversationContinuationSchema,
		Init: func(v4.Header, int64) any {
			return conversationContinuationState(false, false, nil)
		},
		Apply: applyConversationContinuation,
		View:  conversationContinuationView,
	}
}

// conversationContinuationState 造一份新状态（永远新 map：折叠器用 DeepEqual
// 判断"这个事件改了没有"，原地改会永远判成"没改"）。
func conversationContinuationState(admitted, failedAttempt bool, ending any) map[string]any {
	return map[string]any{
		"admitted":      admitted,
		"failedAttempt": failedAttempt,
		"ending":        ending,
	}
}

// conversationContinuationFields 读回状态三字段（坏状态按"清空"处理：
// 一条读不懂的事件不该让重试按钮亮起来）。
func conversationContinuationFields(state any) (admitted, failedAttempt bool, ending any) {
	current, ok := state.(map[string]any)
	if !ok {
		return false, false, nil
	}
	admitted, _ = current["admitted"].(bool)
	failedAttempt, _ = current["failedAttempt"].(bool)
	if value, ok := current["ending"]; ok {
		if _, isNumber := asContinuationSeq(value); isNumber {
			ending = current["ending"]
		}
	}
	return admitted, failedAttempt, ending
}

func asContinuationSeq(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func applyConversationContinuation(state any, event v4.Event) any {
	admitted, failedAttempt, ending := conversationContinuationFields(state)
	switch event.Type {
	case eventTurnStart:
		return conversationContinuationState(false, false, nil)
	case eventStepStart, "assistant/message":
		if !failedAttempt {
			return state
		}
		return conversationContinuationState(admitted, false, ending)
	case eventUserMsg:
		if admitted {
			return state
		}
		if surfaceOpKind(event) != "append" {
			return state
		}
		if !admitsContinuableWork(event.Data) {
			return state
		}
		return conversationContinuationState(true, failedAttempt, ending)
	case "assistant/attempt":
		if failedAttempt {
			return state
		}
		return conversationContinuationState(admitted, true, ending)
	case eventTurnEnd:
		if !admitted || !failedAttempt || !continuableTurnEnd(event.Data) {
			return conversationContinuationState(false, false, nil)
		}
		return conversationContinuationState(false, false, int64(event.Seq))
	default:
		return state
	}
}

// surfaceOpKind 取事件的落位种类。
//
// 优先看**信封**（官方 canonical：`SessionEvent.surfaceOp`）；信封没有时看 data 里的
// `surfaceOp`——宿主 `UserMessage()` 等构造器把 `"surfaceOp":"append"` 写在 data 里
// （internal/session/log.go:151），两条路径必须都能读出 append，否则宿主自己写的
// 用户消息会被判成"不是在 append surface 上"，重试按钮永远不亮。
func surfaceOpKind(event v4.Event) string {
	if len(bytes.TrimSpace(event.SurfaceOp)) > 0 {
		kind, _, _ := event.SurfaceOpKind()
		return kind
	}
	var data struct {
		SurfaceOp json.RawMessage `json:"surfaceOp"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return "append"
	}
	raw := bytes.TrimSpace(data.SurfaceOp)
	if len(raw) == 0 {
		return "append"
	}
	if raw[0] == '"' {
		var kind string
		if err := json.Unmarshal(raw, &kind); err == nil && kind != "" {
			return kind
		}
		return "append"
	}
	var op struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(raw, &op); err == nil && op.Op != "" {
		return op.Op
	}
	return "append"
}

// admitsContinuableWork 复刻官方 admitted 判定：
// `source.kind === 'user' && content.length > 0` 或 `source.kind === 'retry' && content.length === 0`。
func admitsContinuableWork(data json.RawMessage) bool {
	var payload struct {
		Content []json.RawMessage `json:"content"`
		Source  struct {
			Kind string `json:"kind"`
		} `json:"source"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	switch payload.Source.Kind {
	case "user":
		return len(payload.Content) > 0
	case "retry":
		return len(payload.Content) == 0
	default:
		return false
	}
}

// continuableTurnEnd 判断 `turn/end` 是否是"可续跑的失败结算"。
func continuableTurnEnd(data json.RawMessage) bool {
	var payload struct {
		Reason struct {
			Kind  string `json:"kind"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"reason"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	return payload.Reason.Kind == "error" && IsContinuableFailure(payload.Reason.Error.Code)
}

// conversationContinuationView 是 wire 值：可续跑的 `turn/end` seq，或 null（缺席）。
func conversationContinuationView(state any) any {
	_, _, ending := conversationContinuationFields(state)
	return ending
}

// EvaluateContinuation 用同一台折叠算一段事件的 `conversationContinuation`，
// 供 prompt 准入（transport）复用——**不许**在 transport 里再折一份，
// 两份折叠迟早对不上。
func EvaluateContinuation(events []v4.Event) (int64, bool) {
	state := conversationContinuationState(false, false, nil)
	for _, event := range events {
		state = applyConversationContinuation(state, event).(map[string]any)
	}
	_, _, ending := conversationContinuationFields(state)
	seq, ok := asContinuationSeq(ending)
	if !ok {
		return 0, false
	}
	return seq, true
}
