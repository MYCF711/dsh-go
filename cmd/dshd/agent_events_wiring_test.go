package main

// 这一组钉的是 `agent/*` 生命周期边的**真实发射路径**（不是"函数被调了"）。
//
// 补的缺口：`agent.Registry` 的五条边只有一个出口 `RegistryOptions.Emit`，
// 而生产里唯一的注册表以前传的是零值 `RegistryOptions{}` —— `Emit == nil`，
// 于是 `agent/created` / `agent/status` / `agent/disposed` / `subagent/start` /
// `subagent/end` **一条都不发**，界面上 agent 面永远是空的。
//
// 每条用例都从真实装配点出发：
//   - 轮次路径：`goalRoundRunner.Run`（serve 里每条真的跑了一轮的会话都过它）；
//   - 委派路径：`agentOfficialAPI.beginChildRun`（`subagents.prompt` 投递被接受之后）
//     + `goalRoundDriver.settleSubagent`（child 那一轮真的收工之后）；
//   - 帧的落点就是 `session.history` 与 `/ws` 读的那份 Transport journal，
//     并且真的推给了订阅者（topic `session:<id>`）。

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/transport"
)

// ── 夹具 ────────────────────────────────────────────────────────────────

type agentPublished struct {
	topic string
	frame transport.ServerFrame
}

type agentEventRig struct {
	home     string
	journals *transport.Journals
	agents   *agent.Registry
	pub      *agentEventPub

	mu        sync.Mutex
	published []agentPublished
}

// newAgentEventRig 用**生产同一条线**造夹具：registry → agentEventSink →
// Transportation journal + WS 推送出口。
func newAgentEventRig(t *testing.T) *agentEventRig {
	t.Helper()
	rig := &agentEventRig{home: t.TempDir(), journals: transport.NewJournals(nil), pub: &agentEventPub{}}
	rig.pub.set(func(topic string, frame transport.ServerFrame) {
		rig.mu.Lock()
		rig.published = append(rig.published, agentPublished{topic: topic, frame: frame})
		rig.mu.Unlock()
	})
	rig.agents = agent.NewRegistry(agent.RegistryOptions{
		Emit: agentEventSink{journals: rig.journals, pub: rig.pub}.Emit,
	})
	return rig
}

func (r *agentEventRig) publishedFor(sessionID string) []transport.ServerFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]transport.ServerFrame, 0, len(r.published))
	for _, item := range r.published {
		if item.topic != "session:"+sessionID {
			continue
		}
		out = append(out, item.frame)
	}
	return out
}

// agentFrames 取一条会话 journal 里 agent 面的帧（按 seq 升序，journal 本身有序）。
func (r *agentEventRig) agentFrames(sessionID string) []transport.ServerFrame {
	frames, _, _ := r.journals.Get(sessionID).Since(transport.SinceNothing, 0)
	out := make([]transport.ServerFrame, 0, len(frames))
	for _, frame := range frames {
		if len(frame.Type) > 6 && frame.Type[:6] == "agent/" {
			out = append(out, frame)
			continue
		}
		if len(frame.Type) > 9 && frame.Type[:9] == "subagent/" {
			out = append(out, frame)
		}
	}
	return out
}

// kinds 把帧类型列出来（断言顺序/条数用）。
func agentFrameKinds(frames []transport.ServerFrame) []string {
	out := make([]string, 0, len(frames))
	for _, frame := range frames {
		out = append(out, frame.Type)
	}
	return out
}

// payload 把一帧的 data 解成 map（逐字段断言用）。
func agentFramePayloadOf(t *testing.T, frame transport.ServerFrame) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(frame.Data, &got); err != nil {
		t.Fatalf("帧 %s 的 data 不是 JSON 对象：%v（%s）", frame.Type, err, frame.Data)
	}
	return got
}

// keysOf 返回对象的键（升序），用来钉"字段名逐条对齐官方"。
func keysOf(t *testing.T, value any) []string {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("要按键比对的值不是对象：%#v", value)
	}
	out := make([]string, 0, len(object))
	for key := range object {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sameKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ── 1. 真实轮次路径：created / status 真的落到 journal，并且真的推给订阅者 ──

type turnWritingRunner struct {
	home string
	kind string
	// runs 记录真实调用次数（证明"函数真的被调了"，而不是只有断言通过）。
	runs int
}

func (r *turnWritingRunner) Run(_ context.Context, sessionID, _ /*turnID*/, text string) error {
	r.runs++
	return writeGoalRoundTurn(r.home, sessionID, text, r.kind)
}

func TestAgentLifecycleFramesReachJournalAndSubscribersOnRealTurnPath(t *testing.T) {
	rig := newAgentEventRig(t)
	createGoalRoundSession(t, rig.home, "session-agent-a")

	store := openGoalStoreForTest(t, rig.home, rig.journals, nil)
	driver := newGoalRoundDriver(store, rig.home, rig.agents)
	if err := driver.Load(); err != nil {
		t.Fatalf("读 goal 轮次游标: %v", err)
	}
	next := &turnWritingRunner{home: rig.home, kind: "completed"}
	runner := driver.wrap(next)

	if err := runner.Run(context.Background(), "session-agent-a", "turn-1", "跑一轮"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if next.runs != 1 {
		t.Fatalf("真实 runner 被调了 %d 次，want 1", next.runs)
	}

	frames := rig.agentFrames("session-agent-a")
	if got, want := agentFrameKinds(frames), []string{
		agent.EventAgentCreated, agent.EventAgentStatus, agent.EventAgentStatus,
	}; !sameKeys(got, want) {
		t.Fatalf("轮次路径上的 agent 边=%v，want %v", got, want)
	}

	created := agentFramePayloadOf(t, frames[0])
	if keys := keysOf(t, created); !sameKeys(keys, []string{"agent"}) {
		t.Fatalf("agent/created 的载荷键=%v，want [agent]（官方是 {agent, source, signal?}，"+
			"source/signal 在注册表这一层拿不到，省略不编）", keys)
	}
	handle := created["agent"].(map[string]any)
	if keys := keysOf(t, handle); !sameKeys(keys, []string{"depth", "id", "status"}) {
		t.Fatalf("agent 句柄的键=%v（父为空时 parentSessionId 按 omitempty 省略）", keys)
	}
	if handle["id"] != "session-agent-a" || handle["depth"].(float64) != 0 || handle["status"] != "idle" {
		t.Fatalf("agent/created 的 agent 句柄=%v", handle)
	}

	running := agentFramePayloadOf(t, frames[1])
	if keys := keysOf(t, running); !sameKeys(keys, []string{"agent", "status"}) {
		t.Fatalf("agent/status 的载荷键=%v，want [agent status]", keys)
	}
	if running["status"] != string(agent.AgentRunning) {
		t.Fatalf("第一条 status=%v，want running", running["status"])
	}
	idle := agentFramePayloadOf(t, frames[2])
	if idle["status"] != string(agent.AgentIdle) {
		t.Fatalf("收工后的 status=%v，want idle", idle["status"])
	}

	// 订阅者路径：同一批帧必须**真的推出去**（topic `session:<id>`），
	// 否则在线界面只能等下一次 history 拉取，而断线重连看不到 agent 面。
	pushed := rig.publishedFor("session-agent-a")
	if got, want := agentFrameKinds(pushed), agentFrameKinds(frames); !sameKeys(got, want) {
		t.Fatalf("推给订阅者的 agent 帧=%v，journal 里是 %v", got, want)
	}
	for index, frame := range pushed {
		if frame.Seq != frames[index].Seq {
			t.Fatalf("第 %d 帧推出去的 seq=%d，journal 里是 %d（推送与 history 必须是同一个 seq 域）",
				index, frame.Seq, frames[index].Seq)
		}
		if frame.Time <= 0 {
			t.Fatalf("第 %d 帧没有真实时间戳: %+v", index, frame)
		}
	}
}

// ── 2. 逐字段对齐官方（纯投影函数，五条边的键与值） ─────────────────────

func TestAgentEventPayloadFieldsMatchOfficial(t *testing.T) {
	run := agent.SubagentRun{
		RunID: "run-1", Provider: agent.ProviderSpawn,
		ParentSessionID: "parent-1", ChildID: "child-1", Local: true,
	}
	start := agent.AgentEvent{Kind: agent.EventSubagentStart, SessionID: "child-1", ParentID: "parent-1", Depth: 1,
		Status: agent.AgentRunning, Subagent: &agent.SubagentSettled{SubagentRun: run}}
	end := agent.AgentEvent{Kind: agent.EventSubagentEnd, SessionID: "child-1", ParentID: "parent-1", Depth: 1,
		Subagent: &agent.SubagentSettled{
			SubagentRun: run, StopReason: agent.SubagentStopMaxTokens,
			LastAssistantMessage: "半截回答",
		}}
	disposed := agent.AgentEvent{Kind: agent.EventAgentDisposed, SessionID: "child-1", ParentID: "parent-1", Depth: 1}

	cases := []struct {
		name     string
		event    agent.AgentEvent
		wantKeys []string
	}{
		{"agent/created", agent.AgentEvent{Kind: agent.EventAgentCreated, SessionID: "s", Status: agent.AgentIdle},
			[]string{"agent"}},
		{"agent/status", agent.AgentEvent{Kind: agent.EventAgentStatus, SessionID: "s", Status: agent.AgentRunning},
			[]string{"agent", "status"}},
		{"agent/disposed", disposed, []string{"agent"}},
		// 官方 SubagentRunInfo 的字段集，逐条：runId / provider / parentSessionId / id / local。
		{"subagent/start", start, []string{"id", "local", "parentSessionId", "provider", "runId"}},
		// SubagentRunEndInfo = SubagentRunInfo + stopReason + lastAssistantMessage?。
		{"subagent/end", end,
			[]string{"id", "lastAssistantMessage", "local", "parentSessionId", "provider", "runId", "stopReason"}},
	}
	for _, tc := range cases {
		payload, err := agentFramePayload(tc.event)
		if err != nil {
			t.Fatalf("%s: agentFramePayload: %v", tc.name, err)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		var generic map[string]any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		if keys := keysOf(t, generic); !sameKeys(keys, tc.wantKeys) {
			t.Fatalf("%s 的载荷键=%v，want %v（raw=%s）", tc.name, keys, tc.wantKeys, raw)
		}
	}

	// 值也要对：stopReason 用官方词表，lastAssistantMessage 是**官方类型**
	// （ContentBlock[]，不是裸字符串）。
	payload, err := agentFramePayload(end)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payload)
	var endObject map[string]any
	if err := json.Unmarshal(raw, &endObject); err != nil {
		t.Fatal(err)
	}
	if endObject["stopReason"] != agent.SubagentStopMaxTokens {
		t.Fatalf("stopReason=%v，want %s", endObject["stopReason"], agent.SubagentStopMaxTokens)
	}
	if endObject["provider"] != agent.ProviderSpawn {
		t.Fatalf("provider=%v，want %s（官方 dsh-base 给进程内 spawn 后端配的名字）",
			endObject["provider"], agent.ProviderSpawn)
	}
	if endObject["id"] != "child-1" || endObject["parentSessionId"] != "parent-1" || endObject["local"] != true {
		t.Fatalf("委托地址字段=%v", endObject)
	}
	blocks, ok := endObject["lastAssistantMessage"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("lastAssistantMessage 不是数组（官方是 ContentBlock[]）：%#v", endObject["lastAssistantMessage"])
	}
	block := blocks[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "半截回答" {
		t.Fatalf("lastAssistantMessage 的内容块=%v", block)
	}

	// 官方字段缺省 = 整个键不出现（官方同样省略空值）。
	withoutText, err := agentFramePayload(agent.AgentEvent{Kind: agent.EventSubagentEnd, SessionID: "child-1",
		Subagent: &agent.SubagentSettled{SubagentRun: run, StopReason: agent.SubagentStopCompleted}})
	if err != nil {
		t.Fatal(err)
	}
	rawWithout, _ := json.Marshal(withoutText)
	var withoutObject map[string]any
	if err := json.Unmarshal(rawWithout, &withoutObject); err != nil {
		t.Fatal(err)
	}
	if _, present := withoutObject["lastAssistantMessage"]; present {
		t.Fatalf("没有末条输出时 lastAssistantMessage 不该出现：%s", rawWithout)
	}

	// 认不出的边必须响亮失败（静默丢一条边 = 读方以为这个 agent 一直活着）。
	if _, err := agentFramePayload(agent.AgentEvent{Kind: "agent/unknown"}); err == nil {
		t.Fatal("不认识的生命周期边竟然投影成功了")
	}
}

// ── 3. `Emit == nil` 的既有语义：只维护状态、不发边、不 panic ─────────────

func TestRegistryWithoutEmitKeepsStateAndDoesNotPanic(t *testing.T) {
	reg := agent.NewRegistry(agent.RegistryOptions{}) // Emit == nil：与修复前的生产装配同形
	if _, err := reg.Create("parent", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := reg.Start("parent"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	run, err := reg.BeginSubagent("parent", "child", agent.ProviderSpawn, "run-1")
	if err != nil {
		t.Fatalf("BeginSubagent: %v", err)
	}
	if run.ParentSessionID != "parent" {
		t.Fatalf("委派地址没有记下真父：%+v", run)
	}
	if _, err := reg.Stop("parent"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := reg.EndSubagent("run-1", agent.SubagentStopCompleted, ""); err != nil {
		t.Fatalf("EndSubagent: %v", err)
	}
	if err := reg.Dispose("parent"); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	// 状态照旧可读（"nil = 只维护状态不发边，照样能读"）。
	if list := reg.List(); len(list) != 0 {
		t.Fatalf("收尾后注册表里还有 %d 个 agent：%+v", len(list), list)
	}
}

// ── 3b. `agent/disposed`：会话真的被删时的收尾边 ─────────────────────────

// serve.go 的 `Delete` 闭包在删目录**之前**调 `agents.Dispose`（顺序理由写在那一行）。
// 这里钉住那条边的落点：dispose 真的发出 `agent/disposed`，并且真的推给订阅者。
func TestAgentDisposedFrameOnSessionDeletePath(t *testing.T) {
	rig := newAgentEventRig(t)
	createGoalRoundSession(t, rig.home, "session-gone")
	if err := ensureAgentRegistered(rig.agents, rig.home, "session-gone"); err != nil {
		t.Fatalf("登记: %v", err)
	}
	if err := rig.agents.Dispose("session-gone"); err != nil {
		t.Fatalf("Dispose: %v", err)
	}

	frames := rig.agentFrames("session-gone")
	if got, want := agentFrameKinds(frames), []string{
		agent.EventAgentCreated, agent.EventAgentDisposed,
	}; !sameKeys(got, want) {
		t.Fatalf("删除路径上的 agent 边=%v，want %v", got, want)
	}
	disposed := agentFramePayloadOf(t, frames[1])
	handle := disposed["agent"].(map[string]any)
	if handle["id"] != "session-gone" {
		t.Fatalf("agent/disposed 的句柄=%v", handle)
	}
	if _, present := disposed["status"]; present {
		t.Fatalf("agent/disposed 的载荷不该带 status：%v", disposed)
	}
	if got, want := agentFrameKinds(rig.publishedFor("session-gone")), agentFrameKinds(frames); !sameKeys(got, want) {
		t.Fatalf("推给订阅者的 disposed 边=%v，journal 里是 %v", got, want)
	}
	if _, ok := rig.agents.Get("session-gone"); ok {
		t.Fatal("dispose 之后 agent 还在注册表里")
	}
}

// ── 4. 子代理生命周期：真实投递 → 真实收工 → start/end 成对 ──────────────

func TestSubagentStartEndOnRealDeliveryAndSettle(t *testing.T) {
	rig := newAgentEventRig(t)
	createGoalRoundSession(t, rig.home, "session-parent")
	createGoalRoundSession(t, rig.home, "session-child")

	api := newAgentOfficialAPICore(agentOfficialDeps{home: rig.home, journals: rig.journals, agents: rig.agents})
	// 真实装配点：`subagents.prompt` 投递被接受之后调它（见 serve_agent_api.go）。
	api.beginChildRun("session-parent", "session-child")

	childFrames := rig.agentFrames("session-child")
	if got, want := agentFrameKinds(childFrames), []string{
		agent.EventAgentCreated, agent.EventSubagentStart,
	}; !sameKeys(got, want) {
		t.Fatalf("投递之后的子会话边=%v，want %v", got, want)
	}
	startPayload := agentFramePayloadOf(t, childFrames[1])
	if startPayload["provider"] != agent.ProviderSpawn ||
		startPayload["id"] != "session-child" || startPayload["parentSessionId"] != "session-parent" ||
		startPayload["local"] != true {
		t.Fatalf("subagent/start 载荷=%v", startPayload)
	}
	runID, _ := startPayload["runId"].(string)
	if runID == "" {
		t.Fatalf("subagent/start 没有 runId：%v", startPayload)
	}

	// child 那一轮真的跑完（真实 v4 `turn/end{max-tokens}`），驱动结算这次委派。
	store := openGoalStoreForTest(t, rig.home, rig.journals, nil)
	driver := newGoalRoundDriver(store, rig.home, rig.agents)
	if err := driver.Load(); err != nil {
		t.Fatalf("读 goal 轮次游标: %v", err)
	}
	runner := driver.wrap(&turnWritingRunner{home: rig.home, kind: "max-tokens"})
	if err := runner.Run(context.Background(), "session-child", "turn-1", "跑一轮"); err != nil {
		t.Fatalf("Run(child): %v", err)
	}

	childFrames = rig.agentFrames("session-child")
	if got, want := agentFrameKinds(childFrames), []string{
		agent.EventAgentCreated, agent.EventSubagentStart,
		agent.EventAgentStatus, agent.EventSubagentEnd,
	}; !sameKeys(got, want) {
		t.Fatalf("完整子代理生命周期=%v，want %v", got, want)
	}
	endPayload := agentFramePayloadOf(t, childFrames[3])
	// 官方 `epochStopReason`：子会话日志的 turn/end.reason.kind 是唯一真值来源
	// （max-tokens 截断就是 max-tokens，绝不报 completed）。
	if endPayload["stopReason"] != agent.SubagentStopMaxTokens {
		t.Fatalf("stopReason=%v，want %s（子会话真实的 turn/end.kind 是 max-tokens）",
			endPayload["stopReason"], agent.SubagentStopMaxTokens)
	}
	if endPayload["runId"] != runID {
		t.Fatalf("end.runId=%v，start.runId=%v（start/end 必须按 runId 配对）", endPayload["runId"], runID)
	}
	if endPayload["parentSessionId"] != "session-parent" {
		t.Fatalf("end.parentSessionId=%v", endPayload["parentSessionId"])
	}
	// 结算后 child 不再在册：停止 = 注册表里没有。
	if _, ok := rig.agents.Get("session-child"); ok {
		t.Fatal("结算后的子代理还在注册表里")
	}

	// 订阅者也要真的收到这对边。
	pushedKinds := agentFrameKinds(rig.publishedFor("session-child"))
	if !sameKeys(pushedKinds, agentFrameKinds(childFrames)) {
		t.Fatalf("推给订阅者的子代理边=%v，journal 里是 %v", pushedKinds, agentFrameKinds(childFrames))
	}
}

// subagentStopReasonFromTurnEnd 是官方 switch 的宿主映射，逐条钉住。
func TestSubagentStopReasonMapping(t *testing.T) {
	cases := []struct {
		kind  string
		found bool
		want  string
	}{
		{"max-tokens", true, agent.SubagentStopMaxTokens},
		{"aborted", true, agent.SubagentStopAborted},
		{"interrupted", true, agent.SubagentStopAborted},
		{"error", true, agent.SubagentStopError},
		{"blocked", true, agent.SubagentStopRefusal},
		{"completed", true, agent.SubagentStopCompleted},
		{"", true, agent.SubagentStopCompleted},
		// 官方 default 分支：说不出名字的原因当成 error，绝不报成功。
		{"forked", true, agent.SubagentStopError},
		// 根本没有闭合的 turn：不许报 completed。
		{"", false, agent.SubagentStopAborted},
	}
	for _, tc := range cases {
		if got := subagentStopReasonFromTurnEnd(tc.kind, tc.found); got != tc.want {
			t.Fatalf("turn/end kind=%q found=%v → %s，want %s", tc.kind, tc.found, got, tc.want)
		}
	}
}
