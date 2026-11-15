package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// ── 测试替身 ──────────────────────────────────────────────────────────

// fakeActivation 是一次已发布、可等待的委派。
type fakeActivation struct {
	childID string
	result  spi.SubagentResult
	err     error
	// block 非 nil 时 Await 阻塞到它关闭或 ctx 取消（取消路径用）。
	block chan struct{}
	// started 记录 Await 被调用的时刻（用来证明 agent-start 在结果之前）。
	awaited *int32
}

func (a *fakeActivation) ChildSessionID() string { return a.childID }

func (a *fakeActivation) Await(ctx context.Context) (spi.SubagentResult, error) {
	if a.awaited != nil {
		atomic.AddInt32(a.awaited, 1)
	}
	if a.block != nil {
		select {
		case <-a.block:
		case <-ctx.Done():
			return spi.SubagentResult{}, ctx.Err()
		}
	}
	return a.result, a.err
}

// fakeActivator 是两段式委派面的测试实现。
type fakeActivator struct {
	mu sync.Mutex
	// startErr 非 nil 时 StartActivation 失败（AGENT_START）。
	startErr error
	// awaitErr 非 nil 时 Await 返回错误（AGENT_RESULT：结果通道坏掉）。
	awaitErr error
	// failed 为真时子代理"跑完但没成"（Failed=true，解成 null）。
	failed bool
	// texts 按发布顺序给子代理正文。
	texts []string
	// structured 按发布顺序给结构化结果。
	structured []json.RawMessage
	// block 非 nil 时每个 child 都阻塞到它关闭（取消测试用）。
	block chan struct{}
	// echo 为真时把 prompt 回显成子代理正文（pipeline 断言模板替换用）。
	echo bool
	// requests 记录每次委派的请求（断言 prompt/父会话/Continuable）。
	requests []spi.SubagentRequest
	started  int32
	awaited  int32
}

func (f *fakeActivator) StartActivation(ctx context.Context, req spi.SubagentRequest, options spi.ActivationOptions) (spi.SubagentActivation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	index := len(f.requests)
	f.requests = append(f.requests, req)
	atomic.AddInt32(&f.started, 1)
	activation := &fakeActivation{
		childID: fmt.Sprintf("child-%d", index+1),
		block:   f.block,
		awaited: &f.awaited,
	}
	if index < len(f.texts) {
		activation.result.Text = f.texts[index]
	}
	if f.echo {
		activation.result.Text = "echo:" + req.Prompt
	}
	if index < len(f.structured) {
		activation.result.Structured = f.structured[index]
	}
	activation.err = f.awaitErr
	activation.result.Failed = f.failed
	return activation, nil
}

// requestsSnapshot 返回已发布请求的副本（并发安全）。
func (f *fakeActivator) requestsSnapshot() []spi.SubagentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]spi.SubagentRequest(nil), f.requests...)
}

// eventRecorder 收集进程内事件（会被并发调用 → 自己上锁）。
type eventRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *eventRecorder) onEvent(event Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *eventRecorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func (r *eventRecorder) names() []EventName {
	events := r.snapshot()
	names := make([]EventName, len(events))
	for index, event := range events {
		names[index] = event.Name
	}
	return names
}

// durableFrame 是一条落盘帧（type + 原始 JSON）。
type durableFrame struct {
	Type string
	Data string
}

// frameSink 是 spi.EventSink 的测试实现。
type frameSink struct {
	mu       sync.Mutex
	frames   []durableFrame
	failNext bool
	failures int
}

func (s *frameSink) AppendEvent(eventType string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failures++
		s.failNext = false
		return errors.New("模拟写盘失败")
	}
	s.frames = append(s.frames, durableFrame{Type: eventType, Data: string(raw)})
	return nil
}

func (s *frameSink) snapshot() []durableFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]durableFrame(nil), s.frames...)
}

// testMeta 是一份合法 meta。
func testMeta() Meta {
	detail := "先审后改"
	return Meta{
		Name:        "review-and-fix",
		Description: "审查改动并修复",
		WhenToUse:   "用户要求工作流时",
		Phases: []Phase{
			{Title: "审查", Detail: detail, Provider: "spawn", Model: "m"},
			{Title: "实现"},
		},
	}
}

// harness 是一套装配好的引擎 + 观测器 + 耐久写口。
type harness struct {
	engine    *Engine
	events    *eventRecorder
	sink      *frameSink
	activator *fakeActivator
	run       *Run
}

// newHarness 造一套测试装置；options 可改 Params（limits/Sink 等）。
func newHarness(t *testing.T, options ...func(*Params)) *harness {
	t.Helper()
	activator := &fakeActivator{texts: []string{"子代理 1 的结果", "子代理 2 的结果", "子代理 3 的结果"}}
	events := &eventRecorder{}
	sink := &frameSink{}
	idSeq := 0
	params := Params{
		Activator: activator,
		Sink:      sink,
		OnEvent:   events.onEvent,
		NewID: func() string {
			idSeq++
			return fmt.Sprintf("run-%d", idSeq)
		},
	}
	for _, option := range options {
		option(&params)
	}
	engine := New(params)
	run, err := engine.Start(context.Background(), testMeta(), "session-1")
	if err != nil {
		t.Fatalf("Start 不该失败：%v", err)
	}
	t.Cleanup(run.Dispose)
	return &harness{engine: engine, events: events, sink: sink, activator: activator, run: run}
}

// ── 词汇表：6 事件 / 11 码 ─────────────────────────────────────────────

// 事件名与顺序必须与官方逐字一致（dsh-workflow/src/index.ts:94-100）。
func TestEventNamesMatchOfficial(t *testing.T) {
	want := []EventName{
		"workflow/start", "workflow/phase", "workflow/log",
		"workflow/agent-start", "workflow/agent-end", "workflow/end",
	}
	if got := EventNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("事件名/顺序不符官方：\n got %v\nwant %v", got, want)
	}
	if len(want) != 6 {
		t.Fatalf("官方是 6 个事件，实得 %d", len(want))
	}
}

// 致命码必须与官方逐字一致（dsh-workflow/src/index.ts:108-119，共 11 条）。
func TestErrorCodesMatchOfficial(t *testing.T) {
	want := []ErrorCode{
		"SCRIPT_PARSE", "META_INVALID", "INVALID_ARGUMENT", "UNSUPPORTED_OPTION",
		"UNSUPPORTED_SCHEMA", "AGENT_CAP", "ITEM_CAP", "AGENT_START",
		"AGENT_RESULT", "RESULT_UNSERIALIZABLE", "CANCELLED",
	}
	if got := ErrorCodes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("致命码不符官方：\n got %v\nwant %v", got, want)
	}
	if len(want) < 7 {
		t.Fatalf("官方至少 7 个致命码，实得 %d", len(want))
	}
}

// ── meta 校验 ────────────────────────────────────────────────────────

func TestValidateMetaOfficialRules(t *testing.T) {
	cases := []struct {
		name       string
		raw        any
		wantCode   ErrorCode
		wantSubstr string
	}{
		{"合法", map[string]any{"name": "w", "description": "d"}, "", ""},
		{"非对象", "w", CodeMetaInvalid, "meta must be an object"},
		{"nil", nil, CodeMetaInvalid, "meta must be an object"},
		{"数组", []any{}, CodeMetaInvalid, "meta must be an object"},
		{"缺 name", map[string]any{"description": "d"}, CodeMetaInvalid, "meta.name must be a non-empty string"},
		{"空 name", map[string]any{"name": "", "description": "d"}, CodeMetaInvalid, "meta.name must be a non-empty string"},
		{"缺 description", map[string]any{"name": "w"}, CodeMetaInvalid, "meta.description must be a non-empty string"},
		{"whenToUse 类型", map[string]any{"name": "w", "description": "d", "whenToUse": 1},
			CodeMetaInvalid, "meta.whenToUse must be a string"},
		{"phases 类型", map[string]any{"name": "w", "description": "d", "phases": "x"},
			CodeMetaInvalid, "meta.phases must be an array"},
		{"phases 元素类型", map[string]any{"name": "w", "description": "d", "phases": []any{"x"}},
			CodeMetaInvalid, "meta.phases[0] must be an object"},
		{"phase 缺 title", map[string]any{"name": "w", "description": "d", "phases": []any{map[string]any{}}},
			CodeMetaInvalid, "meta.phases[0].title must be a non-empty string"},
		{"phase detail 类型", map[string]any{"name": "w", "description": "d",
			"phases": []any{map[string]any{"title": "t", "detail": 3}}},
			CodeMetaInvalid, `meta.phases[0].detail must be a string`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			meta, err := ValidateMeta(testCase.raw)
			if testCase.wantCode == "" {
				if err != nil {
					t.Fatalf("不该失败：%v", err)
				}
				if meta.Name != "w" || meta.Description != "d" {
					t.Fatalf("归一化结果不对：%+v", meta)
				}
				return
			}
			if err == nil {
				t.Fatal("必须失败")
			}
			if code := CodeOf(err); code != testCase.wantCode {
				t.Fatalf("码 = %q，期望 %q（%v）", code, testCase.wantCode, err)
			}
			if !strings.Contains(err.Error(), testCase.wantSubstr) {
				t.Fatalf("文案缺 %q：%s", testCase.wantSubstr, err)
			}
		})
	}
}

// 未知字段必须**报名字**（官方按白名单拒绝，不是忽略）。
func TestValidateMetaRejectsUnknownFields(t *testing.T) {
	_, err := ValidateMeta(map[string]any{"name": "w", "description": "d", "extra": true})
	if err == nil || !strings.Contains(err.Error(), "meta.extra is not a recognized field") {
		t.Fatalf("未知字段必须被点名：%v", err)
	}
	_, err = ValidateMeta(map[string]any{"name": "w", "description": "d",
		"phases": []any{map[string]any{"title": "t", "bogus": 1}}})
	if err == nil || !strings.Contains(err.Error(), "meta.phases[0].bogus is not a recognized field") {
		t.Fatalf("phase 未知字段必须被点名：%v", err)
	}
}

// 归一化 meta 是**副本**：改返回值不能影响调用方的 map（官方 meta.ts:68-71 的理由）。
func TestValidateMetaCopiesPhases(t *testing.T) {
	raw := map[string]any{"name": "w", "description": "d",
		"phases": []any{map[string]any{"title": "t"}}}
	meta, err := ValidateMeta(raw)
	if err != nil {
		t.Fatal(err)
	}
	meta.Phases[0].Title = "改掉"
	again, err := ValidateMeta(raw)
	if err != nil {
		t.Fatal(err)
	}
	if again.Phases[0].Title != "t" {
		t.Fatalf("归一化结果不该是调用方数据的别名：%v", again.Phases[0].Title)
	}
}

// ── schema 子集 ──────────────────────────────────────────────────────

func TestAssertObjectJSONSchemaRejectsOutsideSubset(t *testing.T) {
	cases := []struct {
		name       string
		schema     any
		wantSubstr string
	}{
		{"根不是 object", map[string]any{"type": "array"}, `schema.type must be "object"`},
		{"未知关键字", map[string]any{"type": "object", "patternProperties": map[string]any{}},
			"schema.patternProperties is not a supported keyword"},
		{"type 数组", map[string]any{"type": []any{"object", "null"}}, "type arrays are not supported"},
		{"oneOf 兄弟", map[string]any{"oneOf": []any{
			map[string]any{"type": "object"}, map[string]any{"type": "object"}},
			"required": []any{}}, "is not supported beside oneOf"},
		{"required 不在 properties", map[string]any{"type": "object",
			"properties": map[string]any{}, "required": []any{"x"}},
			`required names "x" which is not in properties`},
		{"关键字与类型不搭", map[string]any{"type": "string", "items": map[string]any{"type": "string"}},
			`items is not supported on type "string"`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := AssertObjectJSONSchema(testCase.schema)
			if err == nil {
				t.Fatal("必须失败")
			}
			if code := CodeOf(err); code != CodeUnsupportedSchema {
				t.Fatalf("码 = %q，期望 UNSUPPORTED_SCHEMA", code)
			}
			if !strings.Contains(err.Error(), testCase.wantSubstr) {
				t.Fatalf("文案缺 %q：%s", testCase.wantSubstr, err)
			}
		})
	}
}

func TestAssertObjectJSONSchemaAcceptsSubset(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"risks":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"level":   map[string]any{"type": "string", "enum": []any{"low", "high"}},
			"flag":    map[string]any{"type": "boolean", "const": true},
			"either":  map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
		},
		"required":             []any{"summary"},
		"additionalProperties": false,
		"description":          "审查结论",
	}
	if err := AssertObjectJSONSchema(schema); err != nil {
		t.Fatalf("合法子集不该失败：%v", err)
	}
}

func TestValidateAgainstSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"count":   map[string]any{"type": "integer"},
		},
		"required":             []any{"summary"},
		"additionalProperties": false,
	}
	good := map[string]any{"summary": "ok", "count": float64(2)}
	if violations := ValidateAgainstSchema(schema, good); len(violations) != 0 {
		t.Fatalf("合法值不该违规：%v", violations)
	}
	missing := map[string]any{"count": float64(1)}
	if violations := ValidateAgainstSchema(schema, missing); len(violations) == 0 ||
		!strings.Contains(strings.Join(violations, ";"), "value.summary is required") {
		t.Fatalf("缺必填字段必须报：%v", violations)
	}
	extra := map[string]any{"summary": "ok", "bogus": true}
	if violations := ValidateAgainstSchema(schema, extra); len(violations) == 0 {
		t.Fatal("additionalProperties=false 时多余字段必须报")
	}
	wrongType := map[string]any{"summary": "ok", "count": "two"}
	if violations := ValidateAgainstSchema(schema, wrongType); len(violations) == 0 {
		t.Fatal("类型不符必须报")
	}
}

// ── 6 事件载荷逐字段 ─────────────────────────────────────────────────

func TestEventPayloadsFieldByField(t *testing.T) {
	h := newHarness(t)
	if err := h.run.Phase("审查"); err != nil {
		t.Fatal(err)
	}
	h.run.Log("开始扇出")
	if _, err := h.run.Agent(context.Background(), "读改动并列出风险点", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.run.Agent(context.Background(), "跑一遍全量测试", map[string]any{
		"phase": "验证", "label": "全量测试",
	}); err != nil {
		t.Fatal(err)
	}
	result := h.run.Complete(map[string]any{"ok": true})
	if result.StopReason != StopCompleted {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	h.run.Dispose()

	events := h.events.snapshot()
	if len(events) != 8 {
		t.Fatalf("事件数 = %d，期望 8：%v", len(events), h.events.names())
	}
	// 1) workflow/start：身份快照（id + 归一化 meta）。
	start := events[0]
	if start.Name != EventStart {
		t.Fatalf("第 1 个事件是 %q", start.Name)
	}
	if start.Run.ID != "run-1" {
		t.Fatalf("run.id = %q", start.Run.ID)
	}
	if start.Run.Meta.Name != "review-and-fix" || start.Run.Meta.Description != "审查改动并修复" {
		t.Fatalf("start 载荷的 meta = %+v", start.Run.Meta)
	}
	if start.Run.Meta.WhenToUse != "用户要求工作流时" || len(start.Run.Meta.Phases) != 2 {
		t.Fatalf("start 载荷的 meta 可选字段丢了：%+v", start.Run.Meta)
	}
	// 2) workflow/phase：title 原样。
	if events[1].Name != EventPhase || events[1].Title != "审查" {
		t.Fatalf("phase 事件 = %+v", events[1])
	}
	// 3) workflow/log：message 原样。
	if events[2].Name != EventLog || events[2].Message != "开始扇出" {
		t.Fatalf("log 事件 = %+v", events[2])
	}
	// 4) agent-start：seq/label/phase/childId 逐字段。
	agentStart1 := events[3]
	if agentStart1.Name != EventAgentStart {
		t.Fatalf("第 4 个事件是 %q", agentStart1.Name)
	}
	if agentStart1.Agent.Seq != 1 || agentStart1.Agent.Label != "读改动并列出风险点" {
		t.Fatalf("agent-start 载荷 = %+v", agentStart1.Agent)
	}
	if agentStart1.Agent.ChildID != "child-1" {
		t.Fatalf("childId = %q", agentStart1.Agent.ChildID)
	}
	if agentStart1.Agent.Phase == nil || *agentStart1.Agent.Phase != "审查" {
		t.Fatalf("没有 phase() 时 agent 应继承当前阶段：%+v", agentStart1.Agent.Phase)
	}
	// 5) agent-end：seq 配对 + outcome。
	agentEnd1 := events[4]
	if agentEnd1.Name != EventAgentEnd || agentEnd1.End.Seq != 1 || agentEnd1.End.Outcome != OutcomeCompleted {
		t.Fatalf("agent-end 载荷 = %+v", agentEnd1.End)
	}
	// 6) 第二次 agent：显式 phase/label 优先。
	agentStart2 := events[5]
	if agentStart2.Agent.Seq != 2 || agentStart2.Agent.Label != "全量测试" ||
		agentStart2.Agent.Phase == nil || *agentStart2.Agent.Phase != "验证" {
		t.Fatalf("第二个 agent-start = %+v", agentStart2.Agent)
	}
	if events[6].Name != EventAgentEnd || events[6].End.Seq != 2 {
		t.Fatalf("第二个 agent-end = %+v", events[6].End)
	}
	// 7) workflow/end：**不带 value**，stopReason/agentsStarted/error 三字段。
	end := events[7]
	if end.Name != EventEnd {
		t.Fatalf("末事件是 %q", end.Name)
	}
	if end.Result.StopReason != StopCompleted {
		t.Fatalf("end.stopReason = %q", end.Result.StopReason)
	}
	if end.Result.AgentsStarted != 2 {
		t.Fatalf("end.agentsStarted = %d，期望 2", end.Result.AgentsStarted)
	}
	if end.Result.Error != "" {
		t.Fatalf("成功时 end.error 必须缺省：%q", end.Result.Error)
	}
	// 结果本身仍带着 value（value 只交给持有 run 的人）。
	if !reflect.DeepEqual(result.Value, map[string]any{"ok": true}) {
		t.Fatalf("result.Value = %#v", result.Value)
	}
}

// 一次完整 run 的事件序列：start → phase → log → agent-start → agent-end →
// phase → agent-start → agent-end → end，**顺序与数量**都钉死。
func TestFullRunEventSequence(t *testing.T) {
	h := newHarness(t)
	if err := h.run.Phase("审查"); err != nil {
		t.Fatal(err)
	}
	h.run.Log("开始")
	if _, err := h.run.Agent(context.Background(), "a", nil); err != nil {
		t.Fatal(err)
	}
	if err := h.run.Phase("实现"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.run.Agent(context.Background(), "b", nil); err != nil {
		t.Fatal(err)
	}
	h.run.Complete(nil)
	h.run.Dispose()

	want := []EventName{
		EventStart, EventPhase, EventLog,
		EventAgentStart, EventAgentEnd,
		EventPhase, EventAgentStart, EventAgentEnd,
		EventEnd,
	}
	if got := h.events.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("事件序列不符：\n got %v\nwant %v", got, want)
	}
}

// ── 扇出与分组 ────────────────────────────────────────────────────────

// 扇出（N 个子代理）与分组（M 个 phase）的嵌套关系：
// seq 连续、每个 phase 下的成员按到达顺序、agent-end 与 agent-start 按 seq 配对。
func TestFanOutAndPhaseGrouping(t *testing.T) {
	activator := &fakeActivator{texts: []string{"r1", "r2", "r3"}}
	events := &eventRecorder{}
	engine := New(Params{Activator: activator, OnEvent: events.onEvent})
	run, err := engine.Start(context.Background(), testMeta(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Dispose()

	if err := run.Phase("审查"); err != nil {
		t.Fatal(err)
	}
	values, err := run.Parallel(context.Background(), []any{
		map[string]any{"prompt": "p1"},
		map[string]any{"prompt": "p2"},
		map[string]any{"prompt": "p3"},
	})
	if err != nil {
		t.Fatalf("parallel 不该失败：%v", err)
	}
	if len(values) != 3 {
		t.Fatalf("扇出结果数 = %d", len(values))
	}
	if err := run.Phase("实现"); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Agent(context.Background(), "p4", nil); err != nil {
		t.Fatal(err)
	}
	result := run.Complete(nil)
	if result.AgentsStarted != 4 {
		t.Fatalf("agentsStarted = %d，期望 4", result.AgentsStarted)
	}

	// 分组：seq 1-3 属于"审查"，seq 4 属于"实现"。
	grouped := map[string][]int{}
	paired := map[int]bool{}
	for _, event := range events.snapshot() {
		switch event.Name {
		case EventAgentStart:
			if event.Agent.Phase == nil {
				t.Fatalf("seq %d 没有阶段", event.Agent.Seq)
			}
			grouped[*event.Agent.Phase] = append(grouped[*event.Agent.Phase], event.Agent.Seq)
			paired[event.Agent.Seq] = false
		case EventAgentEnd:
			if _, ok := paired[event.End.Seq]; !ok {
				t.Fatalf("agent-end seq %d 没有配对的 agent-start", event.End.Seq)
			}
			paired[event.End.Seq] = true
		}
	}
	// 并发扇出的发布顺序不押（谁先拿到额度谁先发），成员集合必须正好是 1-3。
	sort.Ints(grouped["审查"])
	if !reflect.DeepEqual(grouped["审查"], []int{1, 2, 3}) {
		t.Fatalf("审查阶段成员 = %v，期望 [1 2 3]", grouped["审查"])
	}
	if !reflect.DeepEqual(grouped["实现"], []int{4}) {
		t.Fatalf("实现阶段成员 = %v，期望 [4]", grouped["实现"])
	}
	for seq, ended := range paired {
		if !ended {
			t.Fatalf("seq %d 的 agent-end 没发", seq)
		}
	}
}

// 子代理失败（普通失败）解成 null，不是致命错误（官方 runtime.ts:221-222）。
func TestAgentFailureResolvesNull(t *testing.T) {
	activator := &fakeActivator{failed: true}
	events := &eventRecorder{}
	engine := New(Params{Activator: activator, OnEvent: events.onEvent})
	run, err := engine.Start(context.Background(), testMeta(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Dispose()
	value, err := run.Agent(context.Background(), "p", nil)
	if err != nil {
		t.Fatalf("子代理失败不是致命错误：%v", err)
	}
	if value != nil {
		t.Fatalf("失败应解成 null，实得 %#v", value)
	}
	// 但 agent-end 的 outcome 必须是 failed。
	var outcome Outcome
	for _, event := range events.snapshot() {
		if event.Name == EventAgentEnd {
			outcome = event.End.Outcome
		}
	}
	if outcome != OutcomeFailed {
		t.Fatalf("outcome = %q，期望 failed", outcome)
	}
}

// 带 schema 的子代理：拿到 structured 且通过校验才成功；拿不到算这次调用失败。
func TestAgentSchemaStructuredOutput(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"summary": map[string]any{"type": "string"}}, "required": []any{"summary"}}
	activator := &fakeActivator{structured: []json.RawMessage{
		json.RawMessage(`{"summary":"ok"}`),
		json.RawMessage(`{"bogus":1}`),
		json.RawMessage(`not json`),
	}}
	engine := New(Params{Activator: activator})
	run, err := engine.Start(context.Background(), testMeta(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Dispose()
	value, err := run.Agent(context.Background(), "p", map[string]any{"schema": schema})
	if err != nil {
		t.Fatalf("合法结构化输出不该失败：%v", err)
	}
	if !reflect.DeepEqual(value, map[string]any{"summary": "ok"}) {
		t.Fatalf("结构化结果 = %#v", value)
	}
	// 不符合 schema / 不是 JSON：都解成 null（子代理失败），不是致命错误。
	for _, name := range []string{"不符合 schema", "不是 JSON"} {
		value, err = run.Agent(context.Background(), "p", map[string]any{"schema": schema})
		if err != nil {
			t.Fatalf("%s 不该是致命错误：%v", name, err)
		}
		if value != nil {
			t.Fatalf("%s 应解成 null，实得 %#v", name, value)
		}
	}
}

// ── 致命码：逐条真实触发 ───────────────────────────────────────────────

// 每个致命码都要能被**真实失败**触发，并带正确的 code。
func TestFatalCodesAreTriggerable(t *testing.T) {
	t.Run("INVALID_ARGUMENT 空 prompt", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.run.Agent(context.Background(), "", nil)
		assertCode(t, err, CodeInvalidArgument)
	})
	t.Run("INVALID_ARGUMENT 空 phase", func(t *testing.T) {
		h := newHarness(t)
		assertCode(t, h.run.Phase(""), CodeInvalidArgument)
	})
	t.Run("INVALID_ARGUMENT 选项类型", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.run.Agent(context.Background(), "p", map[string]any{"label": 3})
		assertCode(t, err, CodeInvalidArgument)
	})
	t.Run("INVALID_ARGUMENT options 不是对象", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.run.Agent(context.Background(), "p", "opts")
		assertCode(t, err, CodeInvalidArgument)
	})
	t.Run("UNSUPPORTED_OPTION 未知", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.run.Agent(context.Background(), "p", map[string]any{"bogus": true})
		assertCode(t, err, CodeUnsupportedOption)
	})
	t.Run("UNSUPPORTED_OPTION 后置", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.run.Agent(context.Background(), "p", map[string]any{"effort": "high"})
		assertCode(t, err, CodeUnsupportedOption)
		if !strings.Contains(err.Error(), "deferred") {
			t.Fatalf("后置选项要说清是 deferred：%v", err)
		}
	})
	t.Run("UNSUPPORTED_OPTION provider", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.run.Agent(context.Background(), "p", map[string]any{"provider": "spawn"})
		assertCode(t, err, CodeUnsupportedOption)
	})
	t.Run("UNSUPPORTED_SCHEMA", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.run.Agent(context.Background(), "p", map[string]any{
			"schema": map[string]any{"type": "array"}})
		assertCode(t, err, CodeUnsupportedSchema)
	})
	t.Run("AGENT_CAP", func(t *testing.T) {
		h := newHarness(t, func(params *Params) {
			params.Limits = Limits{MaxTotalAgents: 2, MaxItemsPerCall: 8}
		})
		for index := 0; index < 2; index++ {
			if _, err := h.run.Agent(context.Background(), "p", nil); err != nil {
				t.Fatalf("第 %d 个不该撞上限：%v", index+1, err)
			}
		}
		_, err := h.run.Agent(context.Background(), "p", nil)
		assertCode(t, err, CodeAgentCap)
		if !strings.Contains(err.Error(), "total agent cap (2)") {
			t.Fatalf("上限文案要带数字：%v", err)
		}
	})
	t.Run("ITEM_CAP parallel", func(t *testing.T) {
		h := newHarness(t, func(params *Params) {
			params.Limits = Limits{MaxTotalAgents: 100, MaxItemsPerCall: 2}
		})
		_, err := h.run.Parallel(context.Background(), []any{
			map[string]any{"prompt": "1"}, map[string]any{"prompt": "2"}, map[string]any{"prompt": "3"},
		})
		assertCode(t, err, CodeItemCap)
	})
	t.Run("ITEM_CAP pipeline", func(t *testing.T) {
		h := newHarness(t, func(params *Params) {
			params.Limits = Limits{MaxTotalAgents: 100, MaxItemsPerCall: 2}
		})
		_, err := h.run.Pipeline(context.Background(), []any{"a", "b", "c"},
			[]any{map[string]any{"prompt": "读 ${item}"}})
		assertCode(t, err, CodeItemCap)
	})
	t.Run("AGENT_START", func(t *testing.T) {
		activator := &fakeActivator{startErr: errors.New("没有 provider")}
		events := &eventRecorder{}
		engine := New(Params{Activator: activator, OnEvent: events.onEvent})
		run, err := engine.Start(context.Background(), testMeta(), "s")
		if err != nil {
			t.Fatal(err)
		}
		defer run.Dispose()
		_, agentErr := run.Agent(context.Background(), "p", nil)
		assertCode(t, agentErr, CodeAgentStart)
		// 没发布 child ⇒ 这一对事件**一个都不发**（官方 index.ts:60-67）。
		if count := len(events.snapshot()); count != 1 {
			t.Fatalf("只该有 workflow/start，实得 %d 条：%v", count, events.names())
		}
	})
	t.Run("AGENT_RESULT", func(t *testing.T) {
		activator := &fakeActivator{awaitErr: errors.New("结果通道断了")}
		engine := New(Params{Activator: activator})
		run, err := engine.Start(context.Background(), testMeta(), "s")
		if err != nil {
			t.Fatal(err)
		}
		defer run.Dispose()
		_, agentErr := run.Agent(context.Background(), "p", nil)
		assertCode(t, agentErr, CodeAgentResult)
	})
	t.Run("RESULT_UNSERIALIZABLE", func(t *testing.T) {
		h := newHarness(t)
		result := h.run.Complete(map[string]any{"bad": func() {}})
		if result.StopReason != StopError {
			t.Fatalf("stopReason = %q，期望 error", result.StopReason)
		}
		if result.FatalCode != CodeResultUnserializable {
			t.Fatalf("码 = %q，期望 RESULT_UNSERIALIZABLE", result.FatalCode)
		}
		if !strings.Contains(result.Error, "not plain JSON data") {
			t.Fatalf("文案不对：%s", result.Error)
		}
	})
	t.Run("CANCELLED", func(t *testing.T) {
		h := newHarness(t)
		h.run.Cancel("父步骤中止")
		_, err := h.run.Agent(context.Background(), "p", nil)
		assertCode(t, err, CodeCancelled)
	})
}

// assertCode 断言错误上的致命码。
func assertCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("必须失败（期望码 %s）", want)
	}
	if got := CodeOf(err); got != want {
		t.Fatalf("码 = %q，期望 %q（%v）", got, want, err)
	}
}

// ── 取消 ─────────────────────────────────────────────────────────────

// 取消：result 是 cancelled、error 带原因、在飞的 member 合成 outcome=cancelled
// 且**恰好一次**（官方 host.ts:276-278、:318）。
func TestCancelSynthesizesCancelledMemberEnds(t *testing.T) {
	activator := &fakeActivator{block: make(chan struct{})}
	events := &eventRecorder{}
	engine := New(Params{Activator: activator, OnEvent: events.onEvent})
	run, err := engine.Start(context.Background(), testMeta(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Dispose()

	agentDone := make(chan error, 1)
	go func() {
		_, agentErr := run.Agent(context.Background(), "p", nil)
		agentDone <- agentErr
	}()
	// 等 agent-start 真的发出来再取消（否则可能取消在发布之前，就没得合成）。
	if !waitFor(func() bool { return len(events.snapshot()) >= 2 }) {
		t.Fatal("agent-start 没在超时内发出")
	}
	run.Cancel("用户按了停止")
	if agentErr := <-agentDone; CodeOf(agentErr) != CodeCancelled {
		t.Fatalf("在途 agent 的码 = %q，期望 CANCELLED（%v）", CodeOf(agentErr), agentErr)
	}

	result := run.Result()
	if result.StopReason != StopCancelled {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if !strings.Contains(result.Error, "workflow run cancelled: 用户按了停止") {
		t.Fatalf("取消原因没带出来：%q", result.Error)
	}

	// agent-end 恰好一次，且 outcome=cancelled。
	ends := 0
	for _, event := range events.snapshot() {
		if event.Name == EventAgentEnd && event.End.Outcome == OutcomeCancelled {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("合成 cancelled 的 agent-end 数 = %d，期望 1", ends)
	}
}

// 取消优先于完成（官方 drive() 结尾：cancelReason 非空就覆盖成 cancelled）。
func TestCancelBeatsComplete(t *testing.T) {
	h := newHarness(t)
	h.run.Cancel("先取消")
	result := h.run.Complete("done")
	if result.StopReason != StopCancelled {
		t.Fatalf("取消必须优先，实得 %q", result.StopReason)
	}
}

// 外部 ctx 取消 → 走同一条取消路径（官方 reason 是 'workflow signal aborted'）。
func TestExternalContextCancellation(t *testing.T) {
	activator := &fakeActivator{}
	ctx, cancel := context.WithCancel(context.Background())
	engine := New(Params{Activator: activator})
	run, err := engine.Start(ctx, testMeta(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Dispose()
	cancel()
	if !waitFor(func() bool { return run.Result().StopReason == StopCancelled }) {
		t.Fatalf("外部取消没生效：%q", run.Result().StopReason)
	}
	if !strings.Contains(run.Result().Error, "workflow signal aborted") {
		t.Fatalf("取消原因 = %q，期望官方那条", run.Result().Error)
	}
}

// ── 上限默认值（照官方）─────────────────────────────────────────────

// 三个默认值必须与官方一致（dsh-workflow-ptc/src/index.ts:106-112、:143-145）。
func TestDefaultLimitsMatchOfficial(t *testing.T) {
	limits := DefaultLimits().Resolve()
	if limits.MaxTotalAgents != 1000 {
		t.Fatalf("maxTotalAgents 默认 = %d，官方 1000", limits.MaxTotalAgents)
	}
	if limits.MaxItemsPerCall != 4096 {
		t.Fatalf("maxItemsPerCall 默认 = %d，官方 4096", limits.MaxItemsPerCall)
	}
	// 自动并发 = min(16, max(1, cores-2))。
	cores := runtime.NumCPU()
	want := cores - 2
	if want < 1 {
		want = 1
	}
	if want > 16 {
		want = 16
	}
	if limits.MaxConcurrentAgents != want {
		t.Fatalf("自动并发 = %d，期望 min(16,max(1,%d-2)) = %d",
			limits.MaxConcurrentAgents, cores, want)
	}
}

// 并发闸门：上限 2 时同时在飞的子代理不超过 2，且**全部**跑完。
func TestConcurrencyLimitIsReal(t *testing.T) {
	activator := &concurrencyProbe{limit: 2}
	engine := New(Params{
		Limits:    Limits{MaxConcurrentAgents: 2, MaxTotalAgents: 100, MaxItemsPerCall: 100},
		Activator: activator,
	})
	run, err := engine.Start(context.Background(), testMeta(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Dispose()
	specs := make([]any, 6)
	for index := range specs {
		specs[index] = map[string]any{"prompt": fmt.Sprintf("p%d", index+1)}
	}
	if _, err := run.Parallel(context.Background(), specs); err != nil {
		t.Fatalf("parallel 失败：%v", err)
	}
	if peak := activator.peak(); peak > 2 {
		t.Fatalf("峰值并发 = %d，超过上限 2", peak)
	}
	if peak := activator.peak(); peak < 2 {
		t.Fatalf("峰值并发 = %d，闸门没有真的并行（期望 2）", peak)
	}
	if got := len(activator.requestsSnapshot()); got != 6 {
		t.Fatalf("发布数 = %d，期望 6", got)
	}
}

// concurrencyProbe 记录在飞峰值。
type concurrencyProbe struct {
	limit    int
	mu       sync.Mutex
	live     int
	peakSeen int
	requests []spi.SubagentRequest
}

func (p *concurrencyProbe) StartActivation(ctx context.Context, req spi.SubagentRequest, options spi.ActivationOptions) (spi.SubagentActivation, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.live++
	if p.live > p.peakSeen {
		p.peakSeen = p.live
	}
	index := len(p.requests)
	p.mu.Unlock()
	return &probeActivation{probe: p, childID: fmt.Sprintf("child-%d", index)}, nil
}

func (p *concurrencyProbe) peak() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peakSeen
}

func (p *concurrencyProbe) requestsSnapshot() []spi.SubagentRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]spi.SubagentRequest(nil), p.requests...)
}

type probeActivation struct {
	probe   *concurrencyProbe
	childID string
}

func (a *probeActivation) ChildSessionID() string { return a.childID }

func (a *probeActivation) Await(ctx context.Context) (spi.SubagentResult, error) {
	time.Sleep(5 * time.Millisecond)
	a.probe.mu.Lock()
	a.probe.live--
	a.probe.mu.Unlock()
	return spi.SubagentResult{Text: a.childID}, nil
}

// ── 耐久帧（4 类，逐字段照官方）──────────────────────────────────────

func TestRecorderWritesOfficialDurableFrames(t *testing.T) {
	h := newHarness(t)
	if err := h.run.Phase("审查"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.run.Agent(context.Background(), "读改动", map[string]any{"label": "读改动"}); err != nil {
		t.Fatal(err)
	}
	h.run.Complete(nil)
	h.run.Dispose()

	frames := h.sink.snapshot()
	wantTypes := []string{
		"tool-workflow/run-start",
		"tool-workflow/agent-start",
		"tool-workflow/agent-end",
		"tool-workflow/run-end",
	}
	if len(frames) != len(wantTypes) {
		t.Fatalf("帧数 = %d，期望 %d：%+v", len(frames), len(wantTypes), frames)
	}
	for index, want := range wantTypes {
		if frames[index].Type != want {
			t.Fatalf("第 %d 帧类型 = %q，期望 %q", index+1, frames[index].Type, want)
		}
	}
	// 载荷逐字段（键名是 GUI 的契约，驼峰一个都不能变）。
	assertJSON(t, frames[0].Data, `{"name":"review-and-fix","runId":"run-1"}`)
	assertJSON(t, frames[1].Data, `{"childId":"child-1","label":"读改动","phase":"审查","runId":"run-1","seq":1}`)
	assertJSON(t, frames[2].Data, `{"outcome":"completed","runId":"run-1","seq":1}`)
	assertJSON(t, frames[3].Data, `{"runId":"run-1","stopReason":"completed"}`)
}

// 没有 phase 的 agent：载荷里**不能出现** phase 键（GUI 区分"缺省"与"空串"）。
func TestRecorderOmitsAbsentPhase(t *testing.T) {
	h := newHarness(t)
	if _, err := h.run.Agent(context.Background(), "没有阶段的活", nil); err != nil {
		t.Fatal(err)
	}
	h.run.Complete(nil)
	h.run.Dispose()
	for _, frame := range h.sink.snapshot() {
		if frame.Type == "tool-workflow/agent-start" {
			if strings.Contains(frame.Data, `"phase"`) {
				t.Fatalf("缺省 phase 不该落成字段：%s", frame.Data)
			}
		}
	}
}

// 显式空阶段名是**合法身份**：载荷里要出现 `"phase":""`。
func TestRecorderKeepsEmptyPhase(t *testing.T) {
	h := newHarness(t)
	if _, err := h.run.Agent(context.Background(), "p", map[string]any{"phase": ""}); err != nil {
		t.Fatal(err)
	}
	h.run.Complete(nil)
	h.run.Dispose()
	for _, frame := range h.sink.snapshot() {
		if frame.Type == "tool-workflow/agent-start" {
			if !strings.Contains(frame.Data, `"phase":""`) {
				t.Fatalf("空阶段名要保留成字段：%s", frame.Data)
			}
		}
	}
}

// 写盘失败：报告一次并**停用**记录（官方 record.ts 的纪律），不影响 run 结算。
func TestRecorderDisablesAfterWriteFailure(t *testing.T) {
	activator := &fakeActivator{texts: []string{"a", "b", "c"}}
	sink := &frameSink{}
	var reported int32
	engine := New(Params{
		Activator:     activator,
		Sink:          sink,
		OnRecordError: func(error) { atomic.AddInt32(&reported, 1) },
	})
	run, err := engine.Start(context.Background(), testMeta(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Dispose()
	sink.mu.Lock()
	sink.failNext = true
	sink.mu.Unlock()
	// 下一次写（agent-start）失败。
	if _, err := run.Agent(context.Background(), "p", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Agent(context.Background(), "p", nil); err != nil {
		t.Fatal(err)
	}
	result := run.Complete(nil)
	if result.StopReason != StopCompleted {
		t.Fatalf("记录失败不能改变 run 的结算：%q", result.StopReason)
	}
	if got := atomic.LoadInt32(&reported); got != 1 {
		t.Fatalf("失败报告次数 = %d，期望 1（停用后不再报）", got)
	}
}

// Dispose 幂等；结算之后再 Dispose 不 panic、不再发事件。
func TestDisposeIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.run.Complete(nil)
	before := len(h.events.snapshot())
	h.run.Dispose()
	h.run.Dispose()
	if after := len(h.events.snapshot()); after != before {
		t.Fatalf("Dispose 之后又多发了事件：%d → %d", before, after)
	}
}

// ── pipeline 语义 ────────────────────────────────────────────────────

// pipeline：每个 item 独立走完整条 stage 链，**无 barrier**；普通失败丢 item。
func TestPipelinePerItemChains(t *testing.T) {
	activator := &fakeActivator{echo: true}
	events := &eventRecorder{}
	engine := New(Params{Activator: activator, OnEvent: events.onEvent})
	run, err := engine.Start(context.Background(), testMeta(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer run.Dispose()
	values, err := run.Pipeline(context.Background(), []any{"a.go", "b.go"}, []any{
		map[string]any{"prompt": "读 ${item}"},
		map[string]any{"prompt": "复核 ${prev}"},
	})
	if err != nil {
		t.Fatalf("pipeline 失败：%v", err)
	}
	want := []any{"echo:复核 echo:读 a.go", "echo:复核 echo:读 b.go"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("pipeline 结果 = %#v；期望 %#v", values, want)
	}
	// 4 次发布 = 2 item × 2 stage。
	if got := len(activator.requestsSnapshot()); got != 4 {
		t.Fatalf("发布数 = %d，期望 4", got)
	}
	// 模板替换：两条 item 各自独立（并发执行，不押顺序，按集合断言）。
	prompts := make([]string, 0, 4)
	for _, request := range activator.requestsSnapshot() {
		prompts = append(prompts, request.Prompt)
	}
	for _, wantPrompt := range []string{
		"读 a.go", "读 b.go", // 第一 stage：${item}
		"复核 echo:读 a.go", "复核 echo:读 b.go", // 第二 stage：${prev}
	} {
		found := false
		for _, prompt := range prompts {
			if prompt == wantPrompt {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少 stage prompt %q（实得 %v）", wantPrompt, prompts)
		}
	}
}

// 子代理请求的三条硬约束：Continuable、父会话、显示标签。
func TestAgentRequestContract(t *testing.T) {
	h := newHarness(t)
	if _, err := h.run.Agent(context.Background(), "做一件事", map[string]any{"label": "短标签"}); err != nil {
		t.Fatal(err)
	}
	requests := h.activator.requestsSnapshot()
	if len(requests) != 1 {
		t.Fatalf("请求数 = %d", len(requests))
	}
	if !requests[0].Continuable {
		t.Fatal("每个 agent() 都必须留下可续问子会话（childId 才是真地址）")
	}
	if requests[0].ParentSessionID != "session-1" {
		t.Fatalf("父会话 = %q，期望 session-1", requests[0].ParentSessionID)
	}
	if requests[0].Description != "短标签" {
		t.Fatalf("显示标签 = %q", requests[0].Description)
	}
}

// ── 小工具 ───────────────────────────────────────────────────────────

func assertJSON(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("不是 JSON：%s（%v）", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("期望值不是 JSON：%s", want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON 不符：\n got %s\nwant %s", got, want)
	}
}

// waitFor 在 2 秒内轮询条件（避免用固定 sleep 赌调度）。
func waitFor(condition func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
