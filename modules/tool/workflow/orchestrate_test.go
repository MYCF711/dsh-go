package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	dshworkflow "dshgo/internal/workflow"
)

// ── 编排面的测试替身 ──────────────────────────────────────────────────

// fakeActivator 是两段式委派面的测试实现：按发布顺序回放固定正文。
type fakeActivator struct {
	mu       sync.Mutex
	texts    []string
	failNext bool
	failed   bool
	// echo 为真时把 prompt 回显成正文（模板替换断言用）。
	echo     bool
	requests []spi.SubagentRequest
}

func (f *fakeActivator) StartActivation(ctx context.Context, req spi.SubagentRequest, options spi.ActivationOptions) (spi.SubagentActivation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	index := len(f.requests) - 1
	if f.failNext {
		f.failNext = false
		return nil, errors.New("没有可用的子代理 provider")
	}
	text := ""
	if index < len(f.texts) {
		text = f.texts[index]
	}
	if f.echo {
		text = "echo:" + req.Prompt
	}
	return &fakeActivation{childID: fmt.Sprintf("child-%d", index+1),
		result: spi.SubagentResult{Text: text, Failed: f.failed}}, nil
}

func (f *fakeActivator) snapshot() []spi.SubagentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]spi.SubagentRequest(nil), f.requests...)
}

type fakeActivation struct {
	childID string
	result  spi.SubagentResult
}

func (a *fakeActivation) ChildSessionID() string { return a.childID }

func (a *fakeActivation) Await(ctx context.Context) (spi.SubagentResult, error) {
	return a.result, nil
}

// logSink 把耐久帧写进真实的 v4 会话日志（与生产同一条写路径）。
type logSink struct {
	mu  sync.Mutex
	log *session.Log
}

func (s *logSink) AppendEvent(eventType string, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.log.Append(eventType, data)
}

// newSessionLog 造一个临时会话日志。
func newSessionLog(t *testing.T) *session.Log {
	t.Helper()
	log, err := session.CreateSession(t.TempDir(), t.TempDir(), "session-test", "default")
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// durableFrames 读回 v4 日志里的 `tool-workflow/*` 帧（type + data 原文）。
func durableFrames(t *testing.T, log *session.Log) []durableFrame {
	t.Helper()
	back, err := v4.ReadFile(log.Path)
	if err != nil {
		t.Fatalf("读会话日志失败：%v", err)
	}
	var frames []durableFrame
	for _, event := range back.Events {
		if !strings.HasPrefix(event.Type, "tool-workflow/") {
			continue
		}
		frames = append(frames, durableFrame{Type: event.Type, Data: string(event.Data)})
	}
	return frames
}

// durableFrame 是一条耐久帧。
type durableFrame struct {
	Type string
	Data string
}

// orchestrationCtx 造一个"像宿主那样"的 ctx：会话身份 + 子调用通道 + 事件写口。
func orchestrationCtx(t *testing.T, runner spi.Runner, log *session.Log, depth int) context.Context {
	t.Helper()
	ctx := spi.WithSession(context.Background(), "session-test")
	ctx = spi.WithSubDispatch(ctx, spi.SubDispatch{Runner: runner, Depth: depth})
	if log != nil {
		ctx = spi.WithEventSink(ctx, &logSink{log: log})
	}
	return ctx
}

// runOrchestrated 跑一段编排脚本。
func runOrchestrated(t *testing.T, ctx context.Context, activator spi.SubagentActivator, body map[string]any) (spi.Result, error) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return (&Tool{svc: activator}).Exec(ctx, raw)
}

// scriptArgs 造工具入参。
func scriptArgs(t *testing.T, script string, meta any) map[string]any {
	t.Helper()
	args := map[string]any{"script": script}
	if meta != nil {
		args["meta"] = meta
	}
	return args
}

// meta 是合法 meta 的 JSON 形态（走一遍编码，证明数据形状与官方一致）。
func officialMeta() map[string]any {
	return map[string]any{
		"name":        "review-and-fix",
		"description": "审查改动并修复",
		"whenToUse":   "用户要求工作流时",
		"phases": []any{
			map[string]any{"title": "审查", "detail": "先审"},
			map[string]any{"title": "实现"},
		},
	}
}

// ── 端到端：6 事件真的发、4 类耐久帧真的落盘 ──────────────────────────

// 一次完整的编排 run 必须：
//  1. 发 6 类事件（这里通过耐久帧与结果反证）；
//  2. 把 4 类 `tool-workflow/*` 帧按官方顺序写进 v4 日志；
//  3. 结果正文/Data 照官方渲染。
func TestOrchestrationRunPersistsOfficialFrames(t *testing.T) {
	log := newSessionLog(t)
	activator := &fakeActivator{texts: []string{"风险点：无", "测试全绿"}}
	ctx := orchestrationCtx(t, &fakeRunner{}, log, 0)

	script := `[
	  {"op":"phase","title":"审查"},
	  {"op":"log","message":"开始扇出"},
	  {"op":"agent","prompt":"读改动并列出风险点","label":"读改动","save":"risks"},
	  {"op":"phase","title":"实现"},
	  {"op":"agent","prompt":"跑一遍全量测试","label":"全量测试","phase":"验证","save":"tests"},
	  {"op":"return","value":{"risks":"${risks}","tests":"${tests}"}}
	]`
	result, err := runOrchestrated(t, ctx, activator, scriptArgs(t, script, officialMeta()))
	if err != nil {
		t.Fatalf("编排不该失败：%v（hint=%s）", err, result.Hint)
	}
	if !strings.Contains(result.Text, `workflow "review-and-fix" completed (2 agents).`) {
		t.Fatalf("正文没有官方渲染头：%s", result.Text)
	}
	if !strings.Contains(result.Text, `"risks": "风险点：无"`) {
		t.Fatalf("正文没有 return 值：%s", result.Text)
	}

	// 1) 耐久帧：顺序与数量（官方 tool-workflow 的 4 类）。
	frames := durableFrames(t, log)
	wantTypes := []string{
		"tool-workflow/run-start",
		"tool-workflow/agent-start",
		"tool-workflow/agent-end",
		"tool-workflow/agent-start",
		"tool-workflow/agent-end",
		"tool-workflow/run-end",
	}
	if len(frames) != len(wantTypes) {
		t.Fatalf("耐久帧数 = %d，期望 %d：%+v", len(frames), len(wantTypes), frames)
	}
	for index, want := range wantTypes {
		if frames[index].Type != want {
			t.Fatalf("第 %d 帧 = %q，期望 %q", index+1, frames[index].Type, want)
		}
	}
	// 2) payload 逐字段（键名就是 GUI 的契约）。runId 由宿主 mint（uuid），
	//    这里先取它，再断言其余字段——同一个 run 的四类帧必须**同一个 id**。
	runID := frameField(t, frames[0].Data, "runId")
	if runID == "" || len(runID) != 36 {
		t.Fatalf("runId 不是 uuid 形状：%q", runID)
	}
	assertFrameJSON(t, frameWithout(t, frames[0].Data, "runId"), `{"name":"review-and-fix"}`)
	assertFrameJSON(t, frameWithout(t, frames[1].Data, "runId"),
		`{"seq":1,"label":"读改动","phase":"审查","childId":"child-1"}`)
	assertFrameJSON(t, frameWithout(t, frames[2].Data, "runId"), `{"seq":1,"outcome":"completed"}`)
	assertFrameJSON(t, frameWithout(t, frames[3].Data, "runId"),
		`{"seq":2,"label":"全量测试","phase":"验证","childId":"child-2"}`)
	assertFrameJSON(t, frameWithout(t, frames[4].Data, "runId"), `{"seq":2,"outcome":"completed"}`)
	assertFrameJSON(t, frameWithout(t, frames[5].Data, "runId"), `{"stopReason":"completed"}`)
	for _, frame := range frames {
		if got := frameField(t, frame.Data, "runId"); got != runID {
			t.Fatalf("%s 的 runId = %q，与 run-start 的 %q 不一致", frame.Type, got, runID)
		}
	}

	// 3) 结果 Data。
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 形状不对：%#v", result.Data)
	}
	if data["kind"] != "workflow" || data["name"] != "review-and-fix" {
		t.Fatalf("Data 身份字段 = %#v", data)
	}
	if data["stopReason"] != dshworkflow.StopCompleted {
		t.Fatalf("Data.stopReason = %#v", data["stopReason"])
	}
	if data["agentsStarted"] != 2 {
		t.Fatalf("Data.agentsStarted = %#v", data["agentsStarted"])
	}
	if got := result.Summary; !strings.Contains(got, "2 个子代理") {
		t.Fatalf("Summary = %q", got)
	}
}

// 子代理失败：解成 null，脚本继续；agent-end 的 outcome=failed；run 仍是 completed。
func TestOrchestrationChildFailureResolvesNull(t *testing.T) {
	log := newSessionLog(t)
	activator := &fakeActivator{failed: true}
	ctx := orchestrationCtx(t, &fakeRunner{}, log, 0)
	script := `[
	  {"op":"agent","prompt":"会失败的子代理","save":"outcome"},
	  {"op":"return","value":{"outcome":"${outcome}"}}
	]`
	result, err := runOrchestrated(t, ctx, activator, scriptArgs(t, script, officialMeta()))
	if err != nil {
		t.Fatalf("子代理失败不该让脚本失败：%v", err)
	}
	if !strings.Contains(result.Text, `"outcome": null`) {
		t.Fatalf("子代理失败必须解成 null：%s", result.Text)
	}
	for _, frame := range durableFrames(t, log) {
		if frame.Type != "tool-workflow/agent-end" {
			continue
		}
		var payload struct {
			RunID   string `json:"runId"`
			Seq     int    `json:"seq"`
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.RunID == "" || payload.Seq != 1 || payload.Outcome != "failed" {
			t.Fatalf("agent-end 载荷 = %+v（runId 必须与 run 一致且非空）", payload)
		}
	}
}

// 扇出（N 个子代理）与分组（M 个 phase）的嵌套关系在**耐久帧**里也要成立。
func TestOrchestrationFanOutGrouping(t *testing.T) {
	log := newSessionLog(t)
	activator := &fakeActivator{texts: []string{"a", "b", "c"}}
	ctx := orchestrationCtx(t, &fakeRunner{}, log, 0)
	script := `[
	  {"op":"phase","title":"审查"},
	  {"op":"parallel","agents":[{"prompt":"p1"},{"prompt":"p2"}],"save":"reviews"},
	  {"op":"phase","title":"实现"},
	  {"op":"agent","prompt":"p3","save":"fix"},
	  {"op":"return","value":{"reviews":"${reviews}","fix":"${fix}"}}
	]`
	result, err := runOrchestrated(t, ctx, activator, scriptArgs(t, script, officialMeta()))
	if err != nil {
		t.Fatalf("扇出失败：%v", err)
	}
	data := result.Data.(map[string]any)
	if data["agentsStarted"] != 3 {
		t.Fatalf("agentsStarted = %#v", data["agentsStarted"])
	}
	// 分组：seq 1-2 → 审查，seq 3 → 实现。
	grouped := map[string][]float64{}
	for _, frame := range durableFrames(t, log) {
		if frame.Type != "tool-workflow/agent-start" {
			continue
		}
		var payload struct {
			Seq   int     `json:"seq"`
			Phase *string `json:"phase"`
		}
		if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Phase == nil {
			t.Fatalf("seq %d 没有 phase 字段：%s", payload.Seq, frame.Data)
		}
		grouped[*payload.Phase] = append(grouped[*payload.Phase], float64(payload.Seq))
	}
	if len(grouped["审查"]) != 2 || len(grouped["实现"]) != 1 {
		t.Fatalf("阶段分组 = %#v", grouped)
	}
	if grouped["实现"][0] != 3 {
		t.Fatalf("实现阶段的 seq = %v，期望 3（扇出 2 个之后）", grouped["实现"])
	}
}

// `args` 入参必须真的能在脚本里引用（schema 里写着 `${args}` 能用）。
//
// 这条防的是"文档说得出、实现收不到"：模板替换只认变量表里的名字，
// 忘了把 args 塞进去，脚本会静默收到字面量 `${args}`——不报错，只是白跑。
func TestOrchestrationArgsReachable(t *testing.T) {
	log := newSessionLog(t)
	activator := &fakeActivator{echo: true}
	ctx := orchestrationCtx(t, &fakeRunner{}, log, 0)
	body := map[string]any{
		"script": `[{"op":"agent","prompt":"处理 ${args}","save":"v"},
		           {"op":"return","value":{"echo":"${v}"}}]`,
		"meta": officialMeta(),
		"args": map[string]any{"files": []any{"a.go", "b.go"}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Tool{svc: activator}).Exec(ctx, raw)
	if err != nil {
		t.Fatalf("编排失败：%v（hint=%s）", err, result.Hint)
	}
	requests := activator.snapshot()
	if len(requests) != 1 {
		t.Fatalf("发布数 = %d", len(requests))
	}
	if !strings.Contains(requests[0].Prompt, `"files":["a.go","b.go"]`) {
		t.Fatalf("args 没进模板：%q", requests[0].Prompt)
	}
}

// 旧步骤表与编排操作**混写**：步骤仍然按既有语义记 trace、失败不中断。
func TestOrchestrationMixedWithLegacyStep(t *testing.T) {
	log := newSessionLog(t)
	runner := &fakeRunner{reply: func(tool string, _ json.RawMessage) (spi.Result, error) {
		if tool == "boom" {
			return spi.Result{Hint: "炸了"}, errors.New("炸了")
		}
		return spi.Result{Summary: "ok", Data: "工具结果"}, nil
	}}
	activator := &fakeActivator{texts: []string{"子代理正文"}}
	ctx := orchestrationCtx(t, runner, log, 0)
	script := `[
	  {"tool":"glob","args":{"pattern":"*.go"},"save":"files"},
	  {"op":"log","message":"工具跑完了"},
	  {"tool":"boom"},
	  {"op":"agent","prompt":"用 ${files} 干活","save":"worker"},
	  {"op":"return","value":"${worker}"}
	]`
	result, err := runOrchestrated(t, ctx, activator, scriptArgs(t, script, officialMeta()))
	if err != nil {
		t.Fatalf("混写不该失败：%v", err)
	}
	data := result.Data.(map[string]any)
	steps, ok := data["steps"].([]trace)
	if !ok || len(steps) != 2 {
		t.Fatalf("步骤 trace = %#v", data["steps"])
	}
	if steps[0].Tool != "glob" || !steps[0].OK {
		t.Fatalf("第一步 = %+v", steps[0])
	}
	if steps[1].Tool != "boom" || steps[1].OK {
		t.Fatalf("失败的步骤必须记成 ERR 且不中断：%+v", steps[1])
	}
	if data["failed"] != 1 {
		t.Fatalf("失败计数 = %#v", data["failed"])
	}
	if !strings.Contains(result.Text, "子代理正文") {
		t.Fatalf("return 值不对：%s", result.Text)
	}
	// 子代理的 prompt 里必须已经替换了 ${files}。
	requests := activator.snapshot()
	if len(requests) != 1 || !strings.Contains(requests[0].Prompt, "工具结果") {
		t.Fatalf("prompt 模板没替换：%+v", requests)
	}
}

// 只用步骤表的旧脚本：一个耐久帧都不写（没有 run，就没有节点可画）。
func TestLegacyScriptWritesNoDurableFrames(t *testing.T) {
	log := newSessionLog(t)
	ctx := orchestrationCtx(t, &fakeRunner{}, log, 0)
	body := map[string]any{
		"script": `[{"tool":"glob","args":{"pattern":"*.go"},"save":"files"}]`,
		"meta":   map[string]any{"batch": "B-7", "by": "lead"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Tool{}).Exec(ctx, raw)
	if err != nil {
		t.Fatalf("旧脚本不该失败：%v", err)
	}
	// meta 原样回显（既有契约）。
	data := result.Data.(map[string]any)
	if echo, ok := data["meta"].(map[string]any); !ok || echo["batch"] != "B-7" {
		t.Fatalf("meta 没回显：%#v", data["meta"])
	}
	if frames := durableFrames(t, log); len(frames) != 0 {
		t.Fatalf("旧步骤表不该写耐久帧：%+v", frames)
	}
}

// 没有 EventSink（例如子代理内部的会话）：编排**照样跑完**，只是不落耐久帧。
func TestOrchestrationWithoutSinkStillRuns(t *testing.T) {
	activator := &fakeActivator{texts: []string{"ok"}}
	ctx := orchestrationCtx(t, &fakeRunner{}, nil, 0)
	script := `[{"op":"agent","prompt":"p","save":"v"},{"op":"return","value":"${v}"}]`
	result, err := runOrchestrated(t, ctx, activator, scriptArgs(t, script, officialMeta()))
	if err != nil {
		t.Fatalf("没有写口也该跑完：%v", err)
	}
	if !strings.Contains(result.Text, `"ok"`) {
		t.Fatalf("结果不对：%s", result.Text)
	}
}

// 嵌套调用（Depth>0）不落耐久帧：它的 run 属于子调用，挂在父 run 下会混层。
func TestOrchestrationNestedCallSkipsDurableFrames(t *testing.T) {
	log := newSessionLog(t)
	activator := &fakeActivator{texts: []string{"ok"}}
	ctx := orchestrationCtx(t, &fakeRunner{}, log, 1)
	script := `[{"op":"agent","prompt":"p","save":"v"},{"op":"return","value":"${v}"}]`
	if _, err := runOrchestrated(t, ctx, activator, scriptArgs(t, script, officialMeta())); err != nil {
		t.Fatalf("嵌套调用不该失败：%v", err)
	}
	if frames := durableFrames(t, log); len(frames) != 0 {
		t.Fatalf("嵌套调用不该写耐久帧：%+v", frames)
	}
}

// ── 致命码：脚本层 ──────────────────────────────────────────────────

// 每个致命码都能被**真实失败**触发，并带正确的码。
func TestOrchestrationFatalCodes(t *testing.T) {
	cases := []struct {
		name     string
		script   string
		meta     any
		body     map[string]any
		wantCode dshworkflow.ErrorCode
	}{
		{
			name:     "SCRIPT_PARSE 非法 JSON",
			script:   `not json`,
			wantCode: dshworkflow.CodeScriptParse,
		},
		{
			name:     "SCRIPT_PARSE 未知 op",
			script:   `[{"op":"teleport"}]`,
			wantCode: dshworkflow.CodeScriptParse,
		},
		{
			name:     "SCRIPT_PARSE 项不是对象",
			script:   `["agent"]`,
			wantCode: dshworkflow.CodeScriptParse,
		},
		{
			name:     "META_INVALID 缺 description",
			script:   `[{"op":"log","message":"x"}]`,
			meta:     map[string]any{"name": "w"},
			wantCode: dshworkflow.CodeMetaInvalid,
		},
		{
			name:     "META_INVALID 未知字段",
			script:   `[{"op":"log","message":"x"}]`,
			meta:     map[string]any{"name": "w", "description": "d", "extra": 1},
			wantCode: dshworkflow.CodeMetaInvalid,
		},
		{
			name:     "INVALID_ARGUMENT 空 title",
			script:   `[{"op":"phase","title":""}]`,
			wantCode: dshworkflow.CodeInvalidArgument,
		},
		{
			name:     "INVALID_ARGUMENT 未知字段",
			script:   `[{"op":"log","mesage":"拼错了"}]`,
			wantCode: dshworkflow.CodeInvalidArgument,
		},
		{
			name:     "INVALID_ARGUMENT 空 prompt",
			script:   `[{"op":"agent","prompt":""}]`,
			wantCode: dshworkflow.CodeInvalidArgument,
		},
		{
			name:     "UNSUPPORTED_OPTION 未知选项",
			script:   `[{"op":"agent","prompt":"p","bogus":true}]`,
			wantCode: dshworkflow.CodeUnsupportedOption,
		},
		{
			name:     "UNSUPPORTED_OPTION 后置选项",
			script:   `[{"op":"agent","prompt":"p","effort":"high"}]`,
			wantCode: dshworkflow.CodeUnsupportedOption,
		},
		{
			name:     "UNSUPPORTED_SCHEMA 非 object 根",
			script:   `[{"op":"agent","prompt":"p","schema":{"type":"array"}}]`,
			wantCode: dshworkflow.CodeUnsupportedSchema,
		},
		{
			name:     "AGENT_START 起不来",
			script:   `[{"op":"agent","prompt":"p"}]`,
			body:     map[string]any{"failStart": true},
			wantCode: dshworkflow.CodeAgentStart,
		},
		{
			name:     "RESULT_UNSERIALIZABLE 返回值不是 JSON",
			script:   `[{"tool":"badjson","save":"bad"},{"op":"return","value":"${bad}"}]`,
			wantCode: dshworkflow.CodeResultUnserializable,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{reply: func(tool string, _ json.RawMessage) (spi.Result, error) {
				return spi.Result{Data: map[string]any{"bad": func() {}}}, nil
			}}
			activator := &fakeActivator{texts: []string{"ok"}}
			if testCase.body != nil {
				if fail, _ := testCase.body["failStart"].(bool); fail {
					activator.failNext = true
				}
			}
			meta := testCase.meta
			if meta == nil {
				meta = officialMeta()
			}
			ctx := orchestrationCtx(t, runner, newSessionLog(t), 0)
			result, err := runOrchestrated(t, ctx, activator, scriptArgs(t, testCase.script, meta))
			if err == nil {
				t.Fatalf("必须失败（期望码 %s），实得结果 %+v", testCase.wantCode, result)
			}
			if !strings.Contains(err.Error(), string(testCase.wantCode)) {
				t.Fatalf("错误文本里没有致命码 %s：%v（hint=%s）",
					testCase.wantCode, err, result.Hint)
			}
			if !strings.Contains(result.Hint, string(testCase.wantCode)) {
				t.Fatalf("Hint 里没有致命码 %s：%q", testCase.wantCode, result.Hint)
			}
		})
	}
}

// 编排脚本没有 meta ⇒ META_INVALID（官方 meta 必填）。
func TestOrchestrationRequiresMeta(t *testing.T) {
	ctx := orchestrationCtx(t, &fakeRunner{}, newSessionLog(t), 0)
	_, err := runOrchestrated(t, ctx, &fakeActivator{},
		scriptArgs(t, `[{"op":"log","message":"x"}]`, nil))
	if err == nil || !strings.Contains(err.Error(), "META_INVALID") {
		t.Fatalf("缺 meta 必须报 META_INVALID：%v", err)
	}
}

// 没有子代理能力 ⇒ AGENT_START（官方 start() 在 provider 未注册时抛同一个码）。
func TestOrchestrationWithoutActivator(t *testing.T) {
	ctx := orchestrationCtx(t, &fakeRunner{}, newSessionLog(t), 0)
	_, err := runOrchestrated(t, ctx, nil, scriptArgs(t, `[{"op":"log","message":"x"}]`, officialMeta()))
	if err == nil || !strings.Contains(err.Error(), "AGENT_START") {
		t.Fatalf("没有子代理能力必须报 AGENT_START：%v", err)
	}
}

// run_in_background：明确拒绝（不许假装在后台跑）。
func TestRunInBackgroundRejected(t *testing.T) {
	ctx := orchestrationCtx(t, &fakeRunner{}, newSessionLog(t), 0)
	raw, err := json.Marshal(map[string]any{
		"script":            `[{"op":"log","message":"x"}]`,
		"meta":              officialMeta(),
		"run_in_background": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Tool{svc: &fakeActivator{}}).Exec(ctx, raw)
	if err == nil {
		t.Fatal("run_in_background 必须明确失败")
	}
	if !strings.Contains(result.Hint, "run_in_background") {
		t.Fatalf("Hint 必须点名参数：%q", result.Hint)
	}
}

// ── 取消 ────────────────────────────────────────────────────────────

// 外部取消：脚本在等子代理时被取消 → 结果是 CANCELLED，且 run-end 帧的
// stopReason 是 cancelled（GUI 据此把 run 画成"已取消"）。
func TestOrchestrationCancellation(t *testing.T) {
	log := newSessionLog(t)
	activator := newBlockingActivator()
	ctx, cancel := context.WithCancel(orchestrationCtx(t, &fakeRunner{}, log, 0))
	script := `[{"op":"agent","prompt":"长活","save":"v"}]`
	done := make(chan error, 1)
	go func() {
		_, err := runOrchestrated(t, ctx, activator, scriptArgs(t, script, officialMeta()))
		done <- err
	}()
	activator.waitStarted(t)
	cancel()
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "CANCELLED") {
		t.Fatalf("取消必须带 CANCELLED：%v", err)
	}
	frames := durableFrames(t, log)
	if len(frames) == 0 {
		t.Fatal("取消路径也必须留下耐久帧（至少要能画出已取消）")
	}
	last := frames[len(frames)-1]
	if last.Type != "tool-workflow/run-end" {
		t.Fatalf("末帧 = %q，期望 run-end", last.Type)
	}
	assertFrameJSON(t, frameWithout(t, last.Data, "runId"), `{"stopReason":"cancelled"}`)
	// 在飞的 member 必须被合成 cancelled（否则界面上永远在转圈）。
	var sawCancelled bool
	for _, frame := range frames {
		if frame.Type == "tool-workflow/agent-end" && strings.Contains(frame.Data, `"outcome":"cancelled"`) {
			sawCancelled = true
		}
	}
	if !sawCancelled {
		t.Fatalf("没有合成 cancelled 的 agent-end：%+v", frames)
	}
}

// blockingActivator 发布一个永不完成的 child（等取消）。
//
// started 在构造时就建好：如果延迟到 StartActivation 里再建，waitStarted
// 可能先读到一个 nil 然后永远等一个**没人关**的 channel（这是真的挂住，不是慢）。
type blockingActivator struct {
	started chan struct{}
	once    sync.Once
}

func newBlockingActivator() *blockingActivator {
	return &blockingActivator{started: make(chan struct{})}
}

func (b *blockingActivator) StartActivation(ctx context.Context, req spi.SubagentRequest, options spi.ActivationOptions) (spi.SubagentActivation, error) {
	b.once.Do(func() { close(b.started) })
	return &blockingActivation{}, nil
}

func (b *blockingActivator) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("StartActivation 没在超时内发布 child")
	}
}

type blockingActivation struct{}

func (a *blockingActivation) ChildSessionID() string { return "child-blocked" }

func (a *blockingActivation) Await(ctx context.Context) (spi.SubagentResult, error) {
	<-ctx.Done()
	return spi.SubagentResult{}, ctx.Err()
}

// ── GUI 投影映射 ────────────────────────────────────────────────────

// 本仓 GUI（`dsh-ui-wfr/plugins/dsconversation/official_workflow_run.go` 的
// `transcriptFrame` 与 `projectWorkflowFrame`）只从这四类帧里读固定字段。
// 这个用例把"帧形状 ↔ 投影字段"钉成断言：任一侧改名，这里先红。
func TestGUIGoProjectionFieldMapping(t *testing.T) {
	log := newSessionLog(t)
	activator := &fakeActivator{texts: []string{"ok"}}
	ctx := orchestrationCtx(t, &fakeRunner{}, log, 0)
	script := `[{"op":"phase","title":"审查"},{"op":"agent","prompt":"p","save":"v"},
	           {"op":"return","value":"${v}"}]`
	if _, err := runOrchestrated(t, ctx, activator, scriptArgs(t, script, officialMeta())); err != nil {
		t.Fatalf("编排失败：%v", err)
	}
	frames := durableFrames(t, log)
	if len(frames) != 4 {
		t.Fatalf("帧数 = %d：%+v", len(frames), frames)
	}
	// 每类帧的**字段集合**（多一个少一个都算契约漂移）。
	want := map[string][]string{
		"tool-workflow/run-start":   {"runId", "name"},
		"tool-workflow/agent-start": {"runId", "seq", "label", "phase", "childId"},
		"tool-workflow/agent-end":   {"runId", "seq", "outcome"},
		"tool-workflow/run-end":     {"runId", "stopReason"},
	}
	for _, frame := range frames {
		fields, ok := want[frame.Type]
		if !ok {
			t.Fatalf("未知帧类型 %q", frame.Type)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
			t.Fatalf("%s 不是 JSON 对象：%s", frame.Type, frame.Data)
		}
		if len(payload) != len(fields) {
			t.Fatalf("%s 字段数 = %d，期望 %d：%s", frame.Type, len(payload), len(fields), frame.Data)
		}
		for _, field := range fields {
			if _, present := payload[field]; !present {
				t.Fatalf("%s 缺字段 %q（GUI 读不到）：%s", frame.Type, field, frame.Data)
			}
		}
	}
	// GUI 的 run 状态由 stopReason 派生：completed → WorkflowStatusCompleted。
	var stopReason string
	for _, frame := range frames {
		if frame.Type == "tool-workflow/run-end" {
			var payload struct {
				StopReason string `json:"stopReason"`
			}
			if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
				t.Fatal(err)
			}
			stopReason = payload.StopReason
		}
	}
	if stopReason != "completed" {
		t.Fatalf("GUI 的 stopReason 取值 = %q，期望 completed", stopReason)
	}
}

// 官方默认值钉在代码里：工具名 / 结果上限（dsh-tool-workflow/src/index.ts:59-63）。
func TestToolDefaultsMatchOfficial(t *testing.T) {
	if (&Tool{}).Name() != "workflow" {
		t.Fatalf("工具名 = %q，官方默认 workflow", (&Tool{}).Name())
	}
	if maxResultChars != 50000 {
		t.Fatalf("maxResultChars = %d，官方默认 50000", maxResultChars)
	}
	caps := (&Tool{}).Caps()
	if caps.Writes || caps.Executes || caps.Destructive {
		t.Fatalf("Caps 应为空（真正干活的是子代理/子调用）：%+v", caps)
	}
}

// 每个工作流耐久帧类型都必须在 v4 已知事件表里（否则官方读路径会整份拒载）。
func TestWorkflowDurableFramesAreKnownV4Types(t *testing.T) {
	for _, eventType := range []string{
		"tool-workflow/run-start", "tool-workflow/agent-start",
		"tool-workflow/agent-end", "tool-workflow/run-end",
	} {
		if !session.IsKnownEventType(eventType) {
			t.Fatalf("%s 不在 v4 已知事件表里", eventType)
		}
	}
}

// ── 小工具 ──────────────────────────────────────────────────────────

// frameField 取一条帧载荷里的字符串字段（空 = 没有/不是字符串）。
func frameField(t *testing.T, data, field string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("不是 JSON 对象：%s（%v）", data, err)
	}
	text, _ := payload[field].(string)
	return text
}

// frameWithout 去掉一个字段后重新编码（用于"除 runId 外逐字段"断言）。
func frameWithout(t *testing.T, data, field string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("不是 JSON 对象：%s（%v）", data, err)
	}
	delete(payload, field)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func assertFrameJSON(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("不是 JSON：%s（%v）", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("期望值不是 JSON：%s", want)
	}
	gotRaw, _ := json.Marshal(gotValue)
	wantRaw, _ := json.Marshal(wantValue)
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("载荷不符：\n got %s\nwant %s", got, want)
	}
}
