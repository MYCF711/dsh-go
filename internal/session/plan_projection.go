package session

import (
	"encoding/json"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/storage"
)

// `plan` 投影：计划模式的当前状态，wire 值就是官方那份 `{active, pending}`。
//
// 官方证据（`@deepseek-ai/dsh-plan-mode`）：
//   - wire 值声明：`src/types.ts:21-24`（`PlanProjection = {active: boolean,
//     pending: boolean}`，两个字段都必填）；
//   - 状态声明：`src/types.ts:26-38`（`PlanUnitState` = `active` / `wanted` /
//     `running` / `activeAtLastHeader` / `entered`）；
//   - state schema（strict）：`src/index.ts:118-126`；
//   - wire schema：`src/index.ts:128-131`；
//   - 单元声明（key `plan` / stateVersion 4 / init / apply / wire.view）：
//     `src/index.ts:133-171`；
//   - 事件词汇：`src/index.ts:36`（`plan/mode`，log-only、非 surface、整值替换）、
//     `command/run`（`name === 'plan'` 且 `args !== undefined`）、`command/done`
//     （按 `commandId` 配对）、`request/header`（记录最后一次 header 时的 active）；
//   - view：`src/index.ts:164-168`（`wanted = running?.wanted ?? wanted;
//     pending = wanted !== null && wanted !== active`）；
//   - 客户端怎么用这两个位：`dsh-client-ui-conversation/src/client/input/hub.ts:108-119`
//     （`runMode: plan?.active ? 'plan' : …`）与
//     `dsh-client-ui-plan/src/client/PlanModeControl.tsx:33`
//     （`const target = plan.pending ? !plan.active : plan.active`）。
//
// # 为什么 pending 是"排队中的切换"而不是"方案等人批"
//
// 客户端把 `pending` 当"生效目标的取反"用（`target = pending ? !active : active`）。
// 它表示**一个已记录、尚未结算的切换选择**：`running`（还有一次操作没落地）或
// `wanted`（操作成功但目标与当前状态不同，等下一次被接受的 in-turn pre-step 追加）。
// 它**不是**"方案提交后等人批准"——那个状态在官方由 plan-review 那个待答问题表达，
// 不在这个投影里。
//
// # 宿主的事件来源
//
// 官方从 `/plan` 命令与 `plan/mode` 事件折叠。宿主两条路都要认：
//
//  1. `plan/mode` 是 v4 词汇表成员（`internal/session/known_events.go:74`），读官方
//     写出来的日志必须认它——否则打开官方会话时计划模式状态整段丢失；
//  2. 宿主自己的计划模式开关是**两个工具**（`modules/tool/plan/plan.go` 的
//     `enter_plan_mode` / `exit_plan_mode`），状态落在 `<home>/state/plan/<会话>.json`，
//     日志里唯一的真实证据就是这两个工具的 `tool/call` + `tool/result` 配对。
//
// 第 2 条是本仓的**唯一真值来源**：`tool/call` 把目标状态记进 `running`（于是
// 调用在飞的那段时间里 `pending` 为真，客户端据此把"生效目标"画成取反后的值——
// 这正是官方对 `running` 的定义），`tool/result` 无失败落地时才改 `active`
// （失败则只清掉 `running`，状态不动——与磁盘上那份状态"没写成就不算进"一致）。
//
// 这仍然是**纯派生**：从 v4 原始帧折叠，读路径不写盘（R5 红线）。

const (
	// ProjectionKeyPlan 是官方投影键（`src/types.ts:56` 的
	// `SessionProjectionMap.plan`）。
	ProjectionKeyPlan = "plan"
	// PlanStateVersion 是官方 stateVersion（`src/index.ts:136`）。
	PlanStateVersion = 4
)

// 宿主计划模式的两个工具名（`modules/tool/plan/plan.go`）。
const (
	// ToolEnterPlanMode 进入计划模式。
	ToolEnterPlanMode = "enter_plan_mode"
	// ToolExitPlanMode 提交方案并退出计划模式。
	ToolExitPlanMode = "exit_plan_mode"
)

// 折叠用到的事件名（官方 v4 词汇）。`eventToolCall` / `eventToolResult` 已在
// session_stats.go 里声明为包内常量，这里只补 plan 独有的几条。
const (
	eventPlanMode      = "plan/mode"
	eventCommandRun    = "command/run"
	eventCommandDone   = "command/done"
	eventRequestHeader = "request/header"
)

// planCommandName 是计划模式命令名（`src/index.ts:222` 的 `name: 'plan'`）。
const planCommandName = "plan"

// PlanProjection 是 `plan` 的 wire 值（官方 `PlanProjection`，两个字段都必填）。
type PlanProjection struct {
	// Active 是**在force 的**计划模式状态（最后一次被接受的切换；第一次之前为 false）。
	Active bool `json:"active"`
	// Pending 表示有一个已记录、尚未结算的切换选择，目标与 Active 相反。
	// 客户端据此把"生效目标"画成 `!Active`。
	Pending bool `json:"pending"`
}

// planRunning 是"还有一次切换没落地"的那次操作（官方
// `running: {commandId, wanted} | null`）。
//
// `CommandID` 是**结算方用来配对的标识**：官方 `command/done.commandId` 与
// 宿主的 `tool/result.message.source.callId` 都落在同一个槽里。两个源码不会撞
// （UUID），而且配对逻辑是"相等才算它"，撞了也只会多清一次 `running`，
// 不会改 `active`。
type planRunning struct {
	CommandID string
	Wanted    bool
}

// planUnitState 是官方 `PlanUnitState` 的 Go 视图。
type planUnitState struct {
	// Active 是被记录在案的计划模式状态。
	Active bool
	// Wanted 是那次已结算的选择的目标；`nil` 表示没有悬着的选择。
	Wanted *bool
	// Running 是最近一次还在等结算的切换；`nil` 表示没有。
	Running *planRunning
	// ActiveAtLastHeader 是最后一次 `request/header` 时的 Active；`nil` 表示还没有 header。
	ActiveAtLastHeader *bool
	// Entered 表示这个会话里出现过 active=true 的计划模式。
	Entered bool
}

// planRunningSchema 是 `running` 槽的形状（`src/index.ts:120-123`）。
var planRunningSchema = storage.Object(map[string]storage.Schema{
	"commandId": storage.String(),
	"wanted":    storage.Boolean(),
})

// planUnitStateSchema 是官方 state schema（`src/index.ts:118-126`，strict）。
var planUnitStateSchema = storage.Object(map[string]storage.Schema{
	"active":             storage.Boolean(),
	"wanted":             storage.Nullable(storage.Boolean()),
	"running":            storage.Nullable(planRunningSchema),
	"activeAtLastHeader": storage.Nullable(storage.Boolean()),
	"entered":            storage.Boolean(),
})

// toMap 把状态摊成 storage 行要的那个 map（null 槽写 nil，不写零值——
// "没有悬着的选择"与"选择的目标是 false"是两件事）。
func (s planUnitState) toMap() map[string]any {
	var wanted any
	if s.Wanted != nil {
		wanted = *s.Wanted
	}
	var running any
	if s.Running != nil {
		running = map[string]any{"commandId": s.Running.CommandID, "wanted": s.Running.Wanted}
	}
	var header any
	if s.ActiveAtLastHeader != nil {
		header = *s.ActiveAtLastHeader
	}
	return map[string]any{
		"active":             s.Active,
		"wanted":             wanted,
		"running":            running,
		"activeAtLastHeader": header,
		"entered":            s.Entered,
	}
}

// readPlanUnitState 把 storage 行读回 Go 视图。
func readPlanUnitState(value any) planUnitState {
	records, ok := value.(map[string]any)
	if !ok {
		return planUnitState{}
	}
	state := planUnitState{}
	if active, ok := records["active"].(bool); ok {
		state.Active = active
	}
	if wanted, ok := records["wanted"].(bool); ok {
		state.Wanted = &wanted
	}
	if running, ok := records["running"].(map[string]any); ok {
		commandID, _ := running["commandId"].(string)
		wanted, _ := running["wanted"].(bool)
		state.Running = &planRunning{CommandID: commandID, Wanted: wanted}
	}
	if header, ok := records["activeAtLastHeader"].(bool); ok {
		state.ActiveAtLastHeader = &header
	}
	if entered, ok := records["entered"].(bool); ok {
		state.Entered = entered
	}
	return state
}

// view 是官方 `wire.view`（`src/index.ts:164-168`）的逐行移植。
func (s planUnitState) view() PlanProjection {
	wanted := s.Wanted
	if s.Running != nil {
		target := s.Running.Wanted
		wanted = &target
	}
	return PlanProjection{
		Active:  s.Active,
		Pending: wanted != nil && *wanted != s.Active,
	}
}

// PlanUnit 返回官方 `plan` 投影单元（stateVersion 4）。
func PlanUnit() ProjectionUnit {
	return ProjectionUnit{
		Key:          ProjectionKeyPlan,
		StateVersion: PlanStateVersion,
		Schema:       planUnitStateSchema,
		Init: func(v4.Header, int64) any {
			return planUnitState{}.toMap()
		},
		Apply: func(state any, event v4.Event) any {
			return applyPlanEvent(readPlanUnitState(state), event).toMap()
		},
		View: func(state any) any {
			return readPlanUnitState(state).view()
		},
	}
}

// ProjectPlan 从整份 v4 日志的原始帧折叠出 `plan`（纯读，零写盘）。
//
// 这是 `session.projections` 那条读路径的唯一入口：HTTP unary、follow 基线、
// `session.control` 三处必须**同一份折叠**，否则同一个会话会说出三种事实。
func ProjectPlan(events []v4.Event) PlanProjection {
	unit := PlanUnit()
	state := unit.Init(v4.Header{}, 0)
	for _, event := range events {
		state = unit.Apply(state, event)
	}
	view, _ := unit.View(state).(PlanProjection)
	return view
}

// PlanFolder 是 `plan` 的**增量**折叠器，给 `session.observeProjections` 的
// 改动集用。
//
// 为什么不能复用 ProjectPlan：增量帧要回答的是"这一条事件改了哪些键"，整份
// 重算只能得到"现在是什么"（而且长会话上是 O(n²)）。这里持有单元状态，逐条
// 事件 apply，只在 **wire 值真的变了**时才报 changed——状态里只对视图无影响
// 的位（例如 `entered`）变化不发帧。
type PlanFolder struct {
	unit   ProjectionUnit
	state  any
	view   PlanProjection
	primed bool
}

// NewPlanFolder 返回一个从日志前缀为空开始折叠的增量折叠器。
func NewPlanFolder() *PlanFolder {
	unit := PlanUnit()
	return &PlanFolder{unit: unit, state: unit.Init(v4.Header{}, 0)}
}

// Apply 折叠一条事件，返回当前 wire 值与"值是否变了"。
//
// 第一次 Apply 一定会报 changed=true（从"还没有这个键"到"给出初始值"
// {false,false}），这样流的基线帧与增量帧不会自相矛盾。
func (f *PlanFolder) Apply(event v4.Event) (PlanProjection, bool) {
	f.state = f.unit.Apply(f.state, event)
	view, _ := f.unit.View(f.state).(PlanProjection)
	changed := !f.primed || view != f.view
	f.view = view
	f.primed = true
	return view, changed
}

// View 是已经折叠到的那个 wire 值（还没有事件时是 {false,false}）。
func (f *PlanFolder) View() PlanProjection { return f.view }

// applyPlanEvent 折叠一个事件（官方 `src/index.ts:139-163` 的 apply，
// 加上宿主两个计划模式工具的 `tool/call` / `tool/result` 配对）。
func applyPlanEvent(state planUnitState, event v4.Event) planUnitState {
	switch event.Type {
	case eventCommandRun:
		// 官方：`name === 'plan' && args !== undefined` 才算一次选择；
		// 没有 args 的 `/plan` 只是查询，不进入排队。
		name, args, commandID, ok := planCommandRun(event.Data)
		if !ok || name != planCommandName {
			return state
		}
		state.Running = &planRunning{CommandID: commandID, Wanted: strings.TrimSpace(args) != "off"}
		return state

	case eventCommandDone:
		commandID, kind, ok := planCommandDone(event.Data)
		if !ok || state.Running == nil || state.Running.CommandID != commandID {
			return state
		}
		// 官方：只有成功的、且目标与当前状态不同的命令才留下 wanted。
		var wanted *bool
		if kind == "success" && state.Running.Wanted != state.Active {
			target := state.Running.Wanted
			wanted = &target
		}
		state.Wanted = wanted
		state.Running = nil
		return state

	case eventPlanMode:
		// 官方：log-only 整值替换，并清掉悬着的选择。
		active, ok := planModeActive(event.Data)
		if !ok {
			return state
		}
		state.Active = active
		state.Wanted = nil
		if active {
			state.Entered = true
		}
		return state

	case eventRequestHeader:
		// 官方在这里做 `activeAtLastHeader = state.active`（`src/index.ts:158-160`），
		// 但那一位**只有一个消费者**：模式切换时的旁白消息
		// （`src/index.ts:462-478` 的 `narration`——"用户把这个会话切到了计划模式"）。
		// 宿主没有那条旁白（计划模式开关走工具，工具结果自己叙述了切换），
		// 所以这一位在宿主里**没有读者**。
		//
		// 为什么不照抄：写一个没有读者的位会带来真实副作用——增量折叠器按
		// **状态**比较（`projection.go:587-596`），于是每条 `request/header` 都会
		// 让这一位从 nil 变成 false，从而给客户端发一帧值完全没变的 `plan`。
		// 官方契约里 `request/header` 与 `plan` 的可观察值无关，发这一帧是纯噪音。
		// 将来宿主真的加模式切换旁白时，这里必须开始记录，并同时处理帧去重。
		return state

	case eventToolCall:
		// 宿主的真实证据：两个计划模式工具一开始调用就记下目标状态。
		name, callID, ok := planToolCall(event.Data)
		if !ok {
			return state
		}
		switch name {
		case ToolEnterPlanMode:
			state.Running = &planRunning{CommandID: callID, Wanted: true}
		case ToolExitPlanMode:
			state.Running = &planRunning{CommandID: callID, Wanted: false}
		}
		return state

	case eventToolResult:
		callID, failed, ok := planToolResult(event.Data)
		if !ok || state.Running == nil || state.Running.CommandID != callID {
			return state
		}
		// 失败的工具调用没有改任何状态（宿主 `setState` 失败时返回 err，
		// 磁盘上的那份没动，这里也不能动）。
		if failed {
			state.Running = nil
			return state
		}
		state.Active = state.Running.Wanted
		state.Wanted = nil
		state.Running = nil
		return state
	}
	return state
}

// planCommandRun 读 `command/run` 的 `{name, args, commandId}`
// （`args` 缺省 = 没有选择，ok=false）。
func planCommandRun(data json.RawMessage) (name, args, commandID string, ok bool) {
	var payload struct {
		Name      string  `json:"name"`
		Args      *string `json:"args"`
		CommandID string  `json:"commandId"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", "", "", false
	}
	if payload.Args == nil {
		return "", "", "", false
	}
	return payload.Name, *payload.Args, payload.CommandID, true
}

// planCommandDone 读 `command/done` 的 `{commandId, kind}`。
func planCommandDone(data json.RawMessage) (commandID, kind string, ok bool) {
	var payload struct {
		CommandID string `json:"commandId"`
		Kind      string `json:"kind"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", "", false
	}
	return payload.CommandID, payload.Kind, true
}

// planModeActive 读 `plan/mode` 的 `{active}`。
func planModeActive(data json.RawMessage) (bool, bool) {
	var payload struct {
		Active *bool `json:"active"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Active == nil {
		return false, false
	}
	return *payload.Active, true
}

// planToolCall 读 `tool/call` 的 `{callId, name}`。
func planToolCall(data json.RawMessage) (name, callID string, ok bool) {
	var payload struct {
		CallID string `json:"callId"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.CallID == "" {
		return "", "", false
	}
	return payload.Name, payload.CallID, true
}

// planToolResult 读 `tool/result` 的 callId 与失败标记。
//
// callId 两条路径都认：`message.source.callId`（官方与宿主写侧都写，
// 见 `internal/session/log.go:193-196`）与 `message.toolCallId`（同一帧上的
// 冗余字段，历史日志与官方日志都可能有）。
//
// 失败判定两条都认：`message.isError === true` 与 data 顶层的 `error` 对象
// （`ToolResultEventWithError` 就是这么写的，见 `internal/session/error_codes.go:150-153`）。
func planToolResult(data json.RawMessage) (callID string, failed, ok bool) {
	var payload struct {
		Message struct {
			ToolCallID string `json:"toolCallId"`
			IsError    bool   `json:"isError"`
			Source     struct {
				CallID string `json:"callId"`
			} `json:"source"`
		} `json:"message"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", false, false
	}
	callID = payload.Message.Source.CallID
	if callID == "" {
		callID = payload.Message.ToolCallID
	}
	if callID == "" {
		return "", false, false
	}
	failed = payload.Message.IsError || len(payload.Error) > 0
	return callID, failed, true
}
