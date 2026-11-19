package runcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// ---- 测试替身 ----------------------------------------------------------------

type fakeTool struct {
	name   string
	safe   bool
	exec   func(ctx context.Context, args json.RawMessage) (spi.Result, error)
	calls  int
	callsM sync.Mutex
}

func (f *fakeTool) Name() string            { return f.name }
func (f *fakeTool) Description() string     { return "fake " + f.name }
func (f *fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeTool) Caps() spi.Caps          { return spi.Caps{} }

func (f *fakeTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	f.callsM.Lock()
	f.calls++
	f.callsM.Unlock()
	return f.exec(ctx, args)
}

func (f *fakeTool) ConcurrencySafe(args json.RawMessage) bool { return f.safe }

func (f *fakeTool) callCount() int {
	f.callsM.Lock()
	defer f.callsM.Unlock()
	return f.calls
}

type fakeRegistry struct {
	mu    sync.Mutex
	tools []spi.Tool
}

func (r *fakeRegistry) Register(tool spi.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append(r.tools, tool)
	return nil
}

func (r *fakeRegistry) Tools() []spi.Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]spi.Tool, len(r.tools))
	copy(out, r.tools)
	return out
}

func (r *fakeRegistry) Get(name string) (spi.Tool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tool := range r.tools {
		if tool.Name() == name {
			return tool, true
		}
	}
	return nil, false
}

type fakeRunner struct{ registry *fakeRegistry }

func (r *fakeRunner) Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
	tool, ok := r.registry.Get(name)
	if !ok {
		return spi.Result{Hint: "没有这个工具"}, fmt.Errorf("unknown tool %q", name)
	}
	return tool.Exec(ctx, args)
}

func newTestTool(t *testing.T, cfg Config, tools ...spi.Tool) (*Tool, *fakeRegistry, *fakeRunner) {
	t.Helper()
	registry := &fakeRegistry{}
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	tool, err := New(cfg, registry)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tool, registry, &fakeRunner{registry: registry}
}

func withDispatch(ctx context.Context, runner spi.Runner) context.Context {
	return spi.WithSubDispatch(ctx, spi.SubDispatch{Runner: runner, Depth: 1})
}

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("机器上没有 node：run_code 执行面跳过（跳过 ≠ 通过）")
	}
}

func callJSON(t *testing.T, code, description string, extra map[string]any) json.RawMessage {
	t.Helper()
	payload := map[string]any{"code": code, "description": description}
	for key, value := range extra {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return encoded
}

// ---- 不需要 node 的契约面 -----------------------------------------------------

func TestToolMetadata(t *testing.T) {
	tool, _, _ := newTestTool(t, DefaultConfig())
	if tool.Name() != "run_code" {
		t.Errorf("工具名应为官方 RUN_CODE_NAME=run_code，得到 %q", tool.Name())
	}
	if !strings.Contains(tool.Description(), "TypeScript") {
		t.Errorf("Description 应对齐官方 TypeScript flavor，得到 %q", tool.Description())
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("schema 不是合法 JSON：%v", err)
	}
	if schema.Type != "object" {
		t.Errorf("schema type 应为 object，得到 %q", schema.Type)
	}
	for _, want := range []string{"code", "description", "timeoutMs"} {
		if _, ok := schema.Properties[want]; !ok {
			t.Errorf("schema 缺参数 %q（官方字段名）", want)
		}
	}
	required := strings.Join(schema.Required, ",")
	if !strings.Contains(required, "code") || !strings.Contains(required, "description") {
		t.Errorf("code 与 description 都必填，实际 required=%v", schema.Required)
	}
	caps := tool.Caps()
	if !caps.Executes {
		t.Error("run_code 会起外部进程，Caps.Executes 必须为真")
	}
	if caps.Writes || caps.Network || caps.Destructive {
		t.Error("写入/联网/破坏性由子调用各自的 Caps 声明；重复声明只会让每次 run_code 多问一遍人")
	}
	if tool.ConcurrencySafe(nil) {
		t.Error("官方：root run_code 调用是独占的，必须串行")
	}
}

func TestExecWithoutDispatchChannel(t *testing.T) {
	tool, _, _ := newTestTool(t, DefaultConfig())
	result, err := tool.Exec(context.Background(), callJSON(t, "return 1", "noop", nil))
	if err == nil {
		t.Fatal("没有受管子调用通道时必须拒绝，不许自己找后门")
	}
	if !strings.Contains(result.Hint, "SubDispatch") {
		t.Errorf("Hint 应点名缺的是受管子调用通道，得到 %q", result.Hint)
	}
}

func TestExecArgumentValidation(t *testing.T) {
	tool, _, _ := newTestTool(t, DefaultConfig())
	cases := []struct {
		name string
		raw  string
		why  string
	}{
		{"空参数", ``, "什么都没有"},
		{"缺 code", `{"description":"x"}`, "code 必填"},
		{"空 code", `{"code":"   ","description":"x"}`, "空白 code 不算程序"},
		{"缺 description", `{"code":"return 1"}`, "description 必填"},
		{"未知字段", `{"code":"return 1","description":"x","nope":1}`, "additionalProperties:false"},
		{"负超时", `{"code":"return 1","description":"x","timeoutMs":-5}`, "超时必须正数"},
	}
	for _, tc := range cases {
		_, err := tool.Exec(context.Background(), json.RawMessage(tc.raw))
		if err == nil {
			t.Errorf("%s：必须被拒（%s）", tc.name, tc.why)
		}
	}
}

func TestAllowanceFor(t *testing.T) {
	ms := func(v int64) *int64 { return &v }
	cases := []struct {
		timeout   *int64
		perMinute int
		want      int64
		why       string
	}{
		{ms(120_000), 1024, 2048, "官方 README 的例子：默认两分钟 → 2048 次"},
		{ms(60_000), 1, 1, "每分钟 1 次"},
		{ms(30_000), 1024, 512, "半分钟折半"},
		{nil, 1024, math.MaxInt64, "没有期限就没有有限额度"},
	}
	for _, tc := range cases {
		if got := allowanceFor(tc.timeout, tc.perMinute); got != tc.want {
			t.Errorf("allowanceFor(%v, %d) = %d，应为 %d（%s）", deref(tc.timeout), tc.perMinute, got, tc.want, tc.why)
		}
	}
}

func deref(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func TestConfigDefaultsMatchOfficial(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxSubCallsPerMinute != 1024 {
		t.Errorf("maxSubCallsPerMinute 官方默认 1024，得到 %d", cfg.MaxSubCallsPerMinute)
	}
	if cfg.MaxParallelSubCalls != 10 {
		t.Errorf("maxParallelSubCalls 官方默认 10，得到 %d", cfg.MaxParallelSubCalls)
	}
	normalized, err := Config{}.normalized()
	if err != nil {
		t.Fatalf("normalized: %v", err)
	}
	if normalized.MaxSubCallsPerMinute != 1024 || normalized.MaxParallelSubCalls != 10 {
		t.Errorf("零值配置应落到官方默认，得到 %+v", normalized)
	}
}

func TestModuleDeclaration(t *testing.T) {
	module := Module()
	if module.ID != "tool-runcode" {
		t.Errorf("模块 id 应为 tool-runcode，得到 %q", module.ID)
	}
	if len(module.Requires) != 1 || module.Requires[0] != "tools" {
		t.Errorf("依赖应为 [tools]，得到 %v", module.Requires)
	}
	if module.Register == nil {
		t.Fatal("Register 不能为空")
	}
}

func TestOptInEnvName(t *testing.T) {
	if OptInEnv != "DSHD_PTC" {
		t.Fatalf("开关名变了就要同步 cmd/dshd/main.go 与文档，得到 %q", OptInEnv)
	}
}

// ---- 需要 node 的执行面 ------------------------------------------------------

func echoTool(name string, fn func(args json.RawMessage) (any, error)) *fakeTool {
	return &fakeTool{name: name, safe: true, exec: func(ctx context.Context, args json.RawMessage) (spi.Result, error) {
		value, err := fn(args)
		if err != nil {
			return spi.Result{Hint: "fake 失败"}, err
		}
		return spi.Result{Data: value}, nil
	}}
}

func TestExecRunsProgram(t *testing.T) {
	requireNode(t)
	echo := echoTool("echo", func(args json.RawMessage) (any, error) {
		var payload struct {
			X int `json:"x"`
		}
		if err := json.Unmarshal(args, &payload); err != nil {
			return nil, err
		}
		return map[string]any{"doubled": payload.X * 2}, nil
	})
	tool, _, runner := newTestTool(t, DefaultConfig(), echo)
	ctx, cancel := context.WithTimeout(withDispatch(context.Background(), runner), 60*time.Second)
	defer cancel()

	result, err := tool.Exec(ctx, callJSON(t, "console.log('start'); return await tools.echo({ x: 21 })", "翻倍", nil))
	if err != nil {
		t.Fatalf("不该失败：%v（text=%q）", err, result.Text)
	}
	if !strings.Contains(result.Text, "start") {
		t.Errorf("日志应出现在正文里：%q", result.Text)
	}
	if !strings.Contains(result.Text, "doubled") {
		t.Errorf("完成值应出现在正文里：%q", result.Text)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 形状不对：%T", result.Data)
	}
	if _, hasLogs := data["logs"]; !hasLogs {
		t.Error("官方输出 schema 里 logs 必填")
	}
	if echo.callCount() != 1 {
		t.Errorf("echo 应被调用 1 次，实际 %d", echo.callCount())
	}
}

func TestExecToolFailureIsCatchable(t *testing.T) {
	boom := echoTool("boom", func(args json.RawMessage) (any, error) {
		return nil, errors.New("工具自己炸了")
	})
	tool, _, runner := newTestTool(t, DefaultConfig(), boom)
	ctx, cancel := context.WithTimeout(withDispatch(context.Background(), runner), 60*time.Second)
	defer cancel()

	program := "try { await tools.boom({}) } catch (e) { return { name: e.name, tool: e.toolName, msg: e.message } }"
	result, err := tool.Exec(ctx, callJSON(t, program, "抓住工具失败", nil))
	if err != nil {
		t.Fatalf("程序 catch 住了工具失败，run_code 不该失败：%v", err)
	}
	if !strings.Contains(result.Text, "ToolCallError") {
		t.Errorf("错误类名应是官方的 ToolCallError：%q", result.Text)
	}
	if !strings.Contains(result.Text, "boom") {
		t.Errorf("toolName 应带出成员名：%q", result.Text)
	}
}

func TestExecAllowanceExhaustion(t *testing.T) {
	echo := echoTool("echo", func(args json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil })
	tool, _, runner := newTestTool(t, Config{MaxSubCallsPerMinute: 1, MaxParallelSubCalls: 1}, echo)
	ctx, cancel := context.WithTimeout(withDispatch(context.Background(), runner), 60*time.Second)
	defer cancel()

	// 60000ms × 1 次/分钟 = 1 次额度；程序要调 3 次。
	program := "for (let i = 0; i < 3; i += 1) { try { await tools.echo({}) } catch (e) {} } return 'never'"
	result, err := tool.Exec(ctx, callJSON(t, program, "撞额度", map[string]any{"timeoutMs": 60000}))
	if err == nil {
		t.Fatalf("额度耗尽必须失败，得到成功：%q", result.Text)
	}
	if result.Info == nil || result.Info.Code != codeRunFailed {
		t.Errorf("应有 CODE_RUN_FAILED 身份，得到 %+v", result.Info)
	}
	if !strings.Contains(result.Text, "call 2 was not dispatched") {
		t.Errorf("正文必须点名被拒的调用序号（官方原文），得到 %q", result.Text)
	}
	if !strings.Contains(result.Text, "infinite loop") {
		t.Errorf("正文必须带死循环提示，得到 %q", result.Text)
	}
	if echo.callCount() != 1 {
		t.Errorf("被拒的那次不该派发：echo 应只被调用 1 次，实际 %d", echo.callCount())
	}
}

func TestExecAbortIsSharedAbortedOutcome(t *testing.T) {
	echo := echoTool("echo", func(args json.RawMessage) (any, error) { return nil, nil })
	tool, _, runner := newTestTool(t, DefaultConfig(), echo)
	base := withDispatch(context.Background(), runner)
	ctx, cancel := context.WithCancel(base)
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	result, err := tool.Exec(ctx, callJSON(t, "await new Promise(function (r) { setTimeout(r, 30000) }); return 1", "被取消", nil))
	if err == nil {
		t.Fatalf("取消必须失败，得到成功：%q", result.Text)
	}
	if result.Info == nil || result.Info.Code != spi.CodeAborted {
		t.Errorf("调用方取消要走共享的 ABORTED 身份，得到 %+v", result.Info)
	}
	if !strings.Contains(result.Text, "tool call aborted") {
		t.Errorf("正文应是官方的 'Error: tool call aborted'，得到 %q", result.Text)
	}
}

func TestExecProgramExceptionIsCodeRunFailed(t *testing.T) {
	echo := echoTool("echo", func(args json.RawMessage) (any, error) { return nil, nil })
	tool, _, runner := newTestTool(t, DefaultConfig(), echo)
	ctx, cancel := context.WithTimeout(withDispatch(context.Background(), runner), 60*time.Second)
	defer cancel()
	result, err := tool.Exec(ctx, callJSON(t, "throw new Error('炸')", "抛异常", nil))
	if err == nil {
		t.Fatal("程序抛异常必须失败")
	}
	if result.Info == nil || result.Info.Code != codeRunFailed {
		t.Errorf("程序失败应是 CODE_RUN_FAILED，得到 %+v", result.Info)
	}
	if !strings.Contains(result.Text, "exception") {
		t.Errorf("正文应带失败种类，得到 %q", result.Text)
	}
	if result.Hint == "" {
		t.Error("失败必须带 Hint（R3）")
	}
}

func TestExecSkipsItselfAsBinding(t *testing.T) {
	tool, registry, _ := newTestTool(t, DefaultConfig())
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tools := tool.bindableTools()
	for _, candidate := range tools {
		if candidate.Name() == "run_code" {
			t.Fatal("程序里不该能直接调 run_code 自己（会变成无限嵌套）")
		}
	}
}
