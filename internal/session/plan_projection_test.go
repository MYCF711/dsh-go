package session

import (
	"encoding/json"
	"testing"

	"dshgo/codecs/v4"
)

// `plan` 投影的边界用例（`@deepseek-ai/dsh-plan-mode`）。
//
// 官方 wire 只有两个位（`src/types.ts:21-24`），但它们的**来源**有四条：
// `plan/mode` 事件、`/plan` 命令的 run/done 配对、`request/header`（只影响
// 状态里的 activeAtLastHeader，不上线），以及宿主自己的两个计划模式工具
// （`enter_plan_mode` / `exit_plan_mode`）的 tool/call + tool/result 配对。
// 这里逐条钉住：active 什么时候动、pending 什么时候为真、失败为什么不改状态。

func planEvent(t *testing.T, seq int64, kind, data string) v4.Event {
	t.Helper()
	if !json.Valid([]byte(data)) {
		t.Fatalf("测试夹具的 data 不是 JSON：%s", data)
	}
	return v4.Event{Type: kind, Seq: seq, Time: 1000 + seq, Data: json.RawMessage(data)}
}

// planToolsCall 是一条工具调用的 tool/call。
func planToolsCall(t *testing.T, seq int64, callID, name string) v4.Event {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"callId": callID, "name": name, "arguments": "{}"})
	if err != nil {
		t.Fatalf("marshal tool/call: %v", err)
	}
	return planEvent(t, seq, "tool/call", string(raw))
}

// planToolOK 是一条成功的 tool/result（宿主的写侧形状：message.source.callId +
// message.toolCallId 都写，见 internal/session/log.go:190-207）。
func planToolOK(t *testing.T, seq int64, callID string) v4.Event {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"turn": 1, "step": 1,
		"message": map[string]any{
			"role": "tool", "source": map[string]any{"kind": "tool", "callId": callID},
			"toolCallId": callID, "content": []any{}, "isError": false, "id": "m1",
		},
	})
	if err != nil {
		t.Fatalf("marshal tool/result: %v", err)
	}
	return planEvent(t, seq, "tool/result", string(raw))
}

// planToolFailed 是一条失败的 tool/result（宿主写侧：message.isError 为 true，
// 同时 data 顶层有 error 对象，见 internal/session/error_codes.go:150-153）。
func planToolFailed(t *testing.T, seq int64, callID, code string) v4.Event {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"turn": 1, "step": 1,
		"message": map[string]any{
			"role": "tool", "source": map[string]any{"kind": "tool", "callId": callID},
			"toolCallId": callID, "content": []any{}, "isError": true, "id": "m1",
		},
		"error": map[string]any{"code": code, "name": "Error", "message": "失败"},
	})
	if err != nil {
		t.Fatalf("marshal tool/result: %v", err)
	}
	return planEvent(t, seq, "tool/result", string(raw))
}

// TestPlanUnitDeclaresOfficialShape 钉住键名与 stateVersion 与官方一致。
func TestPlanUnitDeclaresOfficialShape(t *testing.T) {
	unit := PlanUnit()
	if unit.Key != "plan" {
		t.Fatalf("投影键 = %q，官方是 plan", unit.Key)
	}
	if ProjectionKeyPlan != "plan" {
		t.Fatalf("ProjectionKeyPlan = %q", ProjectionKeyPlan)
	}
	// 官方 `stateVersion: 4`（dsh-plan-mode/src/index.ts:136）。
	if unit.StateVersion != 4 {
		t.Fatalf("stateVersion = %d，官方是 4", unit.StateVersion)
	}
	// 官方两个工具名（modules/tool/plan/plan.go）。
	if ToolEnterPlanMode != "enter_plan_mode" || ToolExitPlanMode != "exit_plan_mode" {
		t.Fatalf("工具名 = %q/%q", ToolEnterPlanMode, ToolExitPlanMode)
	}
}

// TestPlanProjectionFold 是本投影的主用例表。
//
// `want` 的语义逐条对应官方 view（`src/index.ts:164-168`）：
//
//	pending = wanted !== null && wanted !== active
//
// 其中 `wanted` 取 `running?.wanted ?? wanted`。
func TestPlanProjectionFold(t *testing.T) {
	cases := []struct {
		name   string
		events []v4.Event
		want   PlanProjection
	}{
		{
			// 官方 init：什么都没发生过 → 两个位都是 false。
			name:   "空日志",
			events: nil,
			want:   PlanProjection{Active: false, Pending: false},
		},
		{
			// 关键语义：tool/call 已落地、tool/result 还没来 —— 一次切换在飞，
			// 目标是 true，而 active 还是 false，所以 pending 成立。
			// 客户端据此把生效目标画成 `!active = true`（PlanModeControl.tsx:33）。
			name: "enter_plan_mode 在飞 → pending",
			events: []v4.Event{
				planToolsCall(t, 0, "c1", ToolEnterPlanMode),
			},
			want: PlanProjection{Active: false, Pending: true},
		},
		{
			name: "enter_plan_mode 落地 → active",
			events: []v4.Event{
				planToolsCall(t, 0, "c1", ToolEnterPlanMode),
				planToolOK(t, 1, "c1"),
			},
			want: PlanProjection{Active: true, Pending: false},
		},
		{
			// 失败不改状态：宿主的 Exec 在 setState 失败时返回 err，磁盘上那份
			// 没动，投影也不能说它动过。
			name: "enter_plan_mode 失败 → 状态不动",
			events: []v4.Event{
				planToolsCall(t, 0, "c1", ToolEnterPlanMode),
				planToolFailed(t, 1, "c1", "EACCES"),
			},
			want: PlanProjection{Active: false, Pending: false},
		},
		{
			// 已在计划模式里提交方案：active 还是 true，但目标已经翻成 false，
			// 于是 pending 为真、生效目标 = !active = false。这正是"正在离开"。
			name: "exit_plan_mode 在飞 → pending 且仍 active",
			events: []v4.Event{
				planToolsCall(t, 0, "c1", ToolEnterPlanMode),
				planToolOK(t, 1, "c1"),
				planToolsCall(t, 2, "c2", ToolExitPlanMode),
			},
			want: PlanProjection{Active: true, Pending: true},
		},
		{
			name: "exit_plan_mode 落地 → 退出",
			events: []v4.Event{
				planToolsCall(t, 0, "c1", ToolEnterPlanMode),
				planToolOK(t, 1, "c1"),
				planToolsCall(t, 2, "c2", ToolExitPlanMode),
				planToolOK(t, 3, "c2"),
			},
			want: PlanProjection{Active: false, Pending: false},
		},
		{
			// 官方写出来的日志（宿主不写这条事件，但必须认得它）。
			name:   "plan/mode 事件",
			events: []v4.Event{planEvent(t, 0, "plan/mode", `{"active":true}`)},
			want:   PlanProjection{Active: true, Pending: false},
		},
		{
			name: "plan/mode 两次（整值替换）",
			events: []v4.Event{
				planEvent(t, 0, "plan/mode", `{"active":true}`),
				planEvent(t, 1, "plan/mode", `{"active":false}`),
			},
			want: PlanProjection{Active: false, Pending: false},
		},
		{
			// 官方 /plan 路径：命令成功但目标与当前状态不同 → wanted 留在状态里，
			// 于是 pending 为真（官方 état 机 src/index.ts:143-149）。
			name: "/plan on 命令成功 → pending",
			events: []v4.Event{
				planEvent(t, 0, "command/run", `{"name":"plan","args":"看看怎么做","commandId":"k1"}`),
				planEvent(t, 1, "command/done", `{"commandId":"k1","kind":"success"}`),
			},
			want: PlanProjection{Active: false, Pending: true},
		},
		{
			// 目标与当前状态相同 → 官方不算 pending。
			name: "/plan off 命令成功（已在非计划模式）→ 不 pending",
			events: []v4.Event{
				planEvent(t, 0, "command/run", `{"name":"plan","args":"off","commandId":"k1"}`),
				planEvent(t, 1, "command/done", `{"commandId":"k1","kind":"success"}`),
			},
			want: PlanProjection{Active: false, Pending: false},
		},
		{
			// 命令失败 → 官方丢掉 wanted。
			name: "/plan 命令失败 → 不 pending",
			events: []v4.Event{
				planEvent(t, 0, "command/run", `{"name":"plan","args":"on","commandId":"k1"}`),
				planEvent(t, 1, "command/done", `{"commandId":"k1","kind":"error"}`),
			},
			want: PlanProjection{Active: false, Pending: false},
		},
		{
			// 没有 args 的 `/plan` 只是查询，不进入排队（官方 `args === undefined` 直接 return）。
			name: "/plan 无参数 → 不算选择",
			events: []v4.Event{
				planEvent(t, 0, "command/run", `{"name":"plan","commandId":"k1"}`),
				planEvent(t, 1, "command/done", `{"commandId":"k1","kind":"success"}`),
			},
			want: PlanProjection{Active: false, Pending: false},
		},
		{
			// 别的工具不影响计划模式状态。
			name: "无关工具调用 → 状态不动",
			events: []v4.Event{
				planToolsCall(t, 0, "c1", "todo_write"),
				planToolOK(t, 1, "c1"),
			},
			want: PlanProjection{Active: false, Pending: false},
		},
		{
			// 配对是按 callId 的：别人（或迟到的重放）的 result 不能结算这次切换。
			name: "callId 不匹配的 result → 不结算",
			events: []v4.Event{
				planToolsCall(t, 0, "c1", ToolEnterPlanMode),
				planToolOK(t, 1, "c9"),
			},
			want: PlanProjection{Active: false, Pending: true},
		},
		{
			// 官方 apply：`plan/mode` 会清掉悬着的选择（`wanted: null`），
			// 但不清 `running`——running 只由它自己那条 result 结算。
			name: "enter 在飞时来一条 plan/mode → 仍是 pending",
			events: []v4.Event{
				planToolsCall(t, 0, "c1", ToolEnterPlanMode),
				planEvent(t, 1, "plan/mode", `{"active":false}`),
			},
			want: PlanProjection{Active: false, Pending: true},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ProjectPlan(testCase.events)
			if got != testCase.want {
				t.Fatalf("ProjectPlan = %+v，期望 %+v", got, testCase.want)
			}
		})
	}
}

// TestPlanUnitStatePassesOwnSchema 钉住"折叠出来的状态能被自己的 schema 接住"。
//
// 这不是多余的：状态要经 `session_projcache` 落盘再读回来，schema 不过的行会被
// 整行丢弃（projection.go:239 的 `unit.Schema.Validate`），那时"计划模式状态
// 偶尔丢失"会非常难查。
func TestPlanUnitStatePassesOwnSchema(t *testing.T) {
	unit := PlanUnit()
	state := unit.Init(v4.Header{ID: "s1", Version: 4}, 0)
	if err := unit.Schema.Validate(state); err != nil {
		t.Fatalf("init 状态不合 schema：%v", err)
	}
	events := []v4.Event{
		planEvent(t, 0, "command/run", `{"name":"plan","args":"on","commandId":"k1"}`),
		planEvent(t, 1, "command/done", `{"commandId":"k1","kind":"success"}`),
		planToolsCall(t, 2, "c1", ToolEnterPlanMode),
		planToolsCall(t, 3, "c2", ToolExitPlanMode),
		planToolFailed(t, 4, "c1", "X"),
		planEvent(t, 5, "request/header", `{"header":{}}`),
		planEvent(t, 6, "plan/mode", `{"active":true}`),
	}
	for _, event := range events {
		state = unit.Apply(state, event)
		if err := unit.Schema.Validate(state); err != nil {
			t.Fatalf("折叠到 %s 之后状态不合 schema：%v（状态 %#v）", event.Type, err, state)
		}
	}
	// 状态必须能过一遍 JSON（落盘就是这么走的），并且回读后 view 不变。
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("状态不能 JSON 序列化：%v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("状态不能 JSON 反序列化：%v", err)
	}
	if err := unit.Schema.Validate(decoded); err != nil {
		t.Fatalf("回读的状态不合 schema：%v", err)
	}
	before, _ := unit.View(state).(PlanProjection)
	after, _ := unit.View(decoded).(PlanProjection)
	if before != after {
		t.Fatalf("回读后 view = %+v，落盘前是 %+v", after, before)
	}
}

// TestPlanUnitReportsChangeKeys 钉住增量帧的键名：`observeProjections` 只在
// 状态**真的变了**时才发这一键，客户端才会刷新输入框模式。
func TestPlanUnitReportsChangeKeys(t *testing.T) {
	tracker := NewProjectionTracker(DefaultProjectionRegistry(), nil, -1, v4.Header{ID: "s1", Version: 4}, 0)

	changes := tracker.Apply(planToolsCall(t, 0, "c1", ToolEnterPlanMode))
	if !hasPlanChange(changes) {
		t.Fatalf("enter_plan_mode 的 tool/call 必须报出 plan 变化：%#v", changes)
	}
	changes = tracker.Apply(planToolOK(t, 1, "c1"))
	if !hasPlanChange(changes) {
		t.Fatalf("enter_plan_mode 的落地必须报出 plan 变化：%#v", changes)
	}
	// 已经落到 active=true 之后，一条无关事件不该再报 plan。
	changes = tracker.Apply(planEvent(t, 2, "step/start", `{"turn":1,"step":2}`))
	if hasPlanChange(changes) {
		t.Fatalf("无关事件不该报 plan 变化：%#v", changes)
	}

	snapshot := tracker.Snapshot()
	value, ok := snapshot.Values[ProjectionKeyPlan].(PlanProjection)
	if !ok {
		t.Fatalf("快照里必须有 plan（拿到 %#v）", snapshot.Values[ProjectionKeyPlan])
	}
	if !value.Active || value.Pending {
		t.Fatalf("快照里的 plan = %+v，期望 {true false}", value)
	}
}

func hasPlanChange(changes []ProjectionChange) bool {
	for _, change := range changes {
		if change.Key == ProjectionKeyPlan {
			return true
		}
	}
	return false
}
