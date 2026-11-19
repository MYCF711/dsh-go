package nodeprovider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/ptc"
	"dshgo/internal/sandbox"
)

// requireNode 在没有 node 的机器上跳过**执行面**测试。
//
// 这是如实跳过，不是"通过了"：本 provider 的全部价值都在那一步外部进程上，
// 跳过只说明这台机器测不了它。
func requireNode(t *testing.T) {
	t.Helper()
	if _, err := lookNode(); err != nil {
		t.Skip("机器上没有 node：PTC 执行面跳过（跳过 ≠ 通过）")
	}
}

func newTestProvider(t *testing.T, mutate func(*Config)) *Provider {
	t.Helper()
	cfg := DefaultConfig()
	cfg.WorkspaceRoot = t.TempDir()
	if mutate != nil {
		mutate(&cfg)
	}
	provider, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

func runResolved(t *testing.T, provider *Provider, req ptc.RunRequest) ptc.RunResult {
	t.Helper()
	spec, err := provider.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := provider.Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run 返回 error（程序失败应该写在 result.Error 里）：%v", err)
	}
	return result
}

func numberOf(t *testing.T, value any) json.Number {
	t.Helper()
	num, ok := value.(json.Number)
	if !ok {
		t.Fatalf("期望 json.Number，实际 %T(%v)", value, value)
	}
	return num
}

func TestNewRejectsUnimplementedModes(t *testing.T) {
	for _, mode := range []ExecutionMode{ModeVMScript, ModePersistentInMemory} {
		if _, err := New(Config{ExecutionMode: mode}); err == nil {
			t.Errorf("executionMode %q 必须装配期就拒（静默降级成 per-call 会让保留状态变成查不出的错觉）", string(mode))
		}
	}
	if _, err := New(Config{ExecutionMode: "whatever"}); err == nil {
		t.Error("未知 executionMode 必须被拒")
	}
	provider, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("默认配置应该可用：%v", err)
	}
	_ = provider.Close()
}

func TestDescriptors(t *testing.T) {
	provider := newTestProvider(t, nil)
	if provider.Language() != "typescript" {
		t.Errorf("language 应为官方 typescript，得到 %q", provider.Language())
	}
	if provider.Isolation() != "process" {
		t.Errorf("isolation 应为 process，得到 %q", provider.Isolation())
	}
	if provider.ProgramFormat() != ptc.FormatFunctionBody {
		t.Errorf("per-call 的 programFormat 应为 function-body，得到 %q", string(provider.ProgramFormat()))
	}
	if provider.RetainsState() {
		t.Error("per-call 不保留状态")
	}
	if _, ok := provider.SandboxMode(); ok {
		t.Error("本 provider 没有文件 confinement，SandboxMode 必须报 false")
	}
	spec, ok := provider.Timeout()
	if !ok {
		t.Fatal("per-call provider 支持数字覆盖，Timeout 应 ok")
	}
	if spec.DefaultMs != 120_000 || spec.MaxMs != 600_000 {
		t.Errorf("预算默认/上限应为 120000/600000，得到 %d/%d", spec.DefaultMs, spec.MaxMs)
	}
	if provider.ExecutionInstructions() == "" {
		t.Error("provider 必须给出自己的执行说明（语法/输出/状态规则归 provider）")
	}
	if err := provider.Release(context.Background(), struct{}{}); err != nil {
		t.Errorf("不保留状态的 provider Release 应无事可做：%v", err)
	}
}

func TestResolveRejections(t *testing.T) {
	provider := newTestProvider(t, nil)
	mode := mustMode(t, "read-only")

	cases := []struct {
		name string
		req  ptc.RunRequest
		why  string
	}{
		{"空程序", ptc.RunRequest{Program: "   "}, "空 program 不能发起运行"},
		{"cwd 不存在", ptc.RunRequest{Program: "return 1", Cwd: filepath.Join(provider.cfg.WorkspaceRoot, "nope")}, "cwd 必须真实存在"},
		{"显式沙盒档位", ptc.RunRequest{Program: "return 1", SandboxMode: &mode}, "没有 confinement 必须拒绝显式档位"},
		{"零超时", ptc.RunRequest{Program: "return 1", Timeout: ptc.TimeoutMs(0)}, "零不是正数"},
		{"非法 binding 名", ptc.RunRequest{Program: "return 1", Bindings: []ptc.BindingNamespace{{Global: "lambda"}}}, "保留字不能当全局名"},
	}
	for _, tc := range cases {
		_, err := provider.Resolve(tc.req)
		if err == nil {
			t.Errorf("%s：必须被拒（%s）", tc.name, tc.why)
			continue
		}
		if !ptc.IsMisuse(err) {
			t.Errorf("%s：必须是误用身份：%v", tc.name, err)
		}
	}

	spec, err := provider.Resolve(ptc.RunRequest{Program: "return 1"})
	if err != nil {
		t.Fatalf("最小合法请求应能解析：%v", err)
	}
	if spec.TimeoutMs == nil || *spec.TimeoutMs != 120_000 {
		t.Errorf("没给 timeoutMs 时应落到默认 120000，得到 %v", derefInt(spec.TimeoutMs))
	}
	if !filepath.IsAbs(spec.Cwd) {
		t.Errorf("cwd 应被解析成绝对路径：%q", spec.Cwd)
	}
	none, err := provider.Resolve(ptc.RunRequest{Program: "return 1", Timeout: ptc.TimeoutNone()})
	if err != nil {
		t.Fatalf("显式 null 应可解析：%v", err)
	}
	if none.TimeoutMs != nil {
		t.Errorf("显式 null 必须解析成 nil（无期限），得到 %d", *none.TimeoutMs)
	}
}

func derefInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func mustMode(t *testing.T, raw string) sandbox.Mode {
	t.Helper()
	switch sandbox.Mode(raw) {
	case sandbox.ModeReadOnly, sandbox.ModeWorkspaceWrite, sandbox.ModeDangerFullAccess:
		return sandbox.Mode(raw)
	}
	t.Fatalf("未知档位 %q", raw)
	return ""
}

func TestRunReturnsValue(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{Program: "return 1 + 2"})
	if result.Failed() {
		t.Fatalf("程序不该失败：%+v", result.Error)
	}
	if !result.HasValue {
		t.Fatal("有 return 值时必须 HasValue")
	}
	if got := numberOf(t, result.Value); got.String() != "3" {
		t.Fatalf("值应为 3，得到 %s", got)
	}
	if len(result.Logs) != 0 {
		t.Errorf("没有打印时不该有日志：%v", result.Logs)
	}
}

func TestRunCapturesConsoleLogs(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "console.log('first'); console.error('second'); return 'ok'",
	})
	if result.Failed() {
		t.Fatalf("不该失败：%+v", result.Error)
	}
	if len(result.Logs) != 2 || !strings.Contains(result.Logs[0], "first") || !strings.Contains(result.Logs[1], "second") {
		t.Fatalf("日志顺序/内容不对：%v", result.Logs)
	}
}

func TestRunCapturesStdoutWrites(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "process.stdout.write('raw-bytes'); return 1",
	})
	if result.Failed() {
		t.Fatalf("不该失败：%+v", result.Error)
	}
	if len(result.Logs) != 1 || !strings.Contains(result.Logs[0], "raw-bytes") {
		t.Fatalf("程序直接写 stdout 的字节也必须变成日志帧（否则会冲掉控制通道）：%v", result.Logs)
	}
}

func TestRunStripsTypeScriptAnnotations(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "const n: number = 41;\nconst box: { v: number } = { v: n };\nreturn box.v + 1",
	})
	if result.Failed() {
		t.Fatalf("可擦除 TypeScript 语法应该能跑：%+v", result.Error)
	}
	if got := numberOf(t, result.Value).String(); got != "42" {
		t.Fatalf("值应为 42，得到 %s", got)
	}
}

func TestRunBindingRoundTrip(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "return await tools.add({ a: 1, b: 2 })",
		Bindings: []ptc.BindingNamespace{{
			Global: "tools",
			Functions: map[string]ptc.BindingFunction{
				"add": func(ctx context.Context, args any) (any, error) {
					if _, ok := args.(map[string]any); !ok {
						t.Fatalf("绑定收到 %T，不是对象", args)
					}
					return json.Number("3"), nil
				},
			},
		}},
	})
	if result.Failed() {
		t.Fatalf("绑定往返不该失败：%+v", result.Error)
	}
	if got := numberOf(t, result.Value).String(); got != "3" {
		t.Fatalf("值应为 3，得到 %s", got)
	}
}

func TestRunBindingErrorClass(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "try { await tools.boom({}); return { caught: false }; } catch (e) { return { name: e.name, tool: e.toolName, msg: e.message }; }",
		Bindings: []ptc.BindingNamespace{{
			Global: "tools",
			Functions: map[string]ptc.BindingFunction{
				"boom": func(ctx context.Context, args any) (any, error) {
					return nil, errors.New("工具炸了")
				},
			},
			ErrorClass: &ptc.BindingErrorClass{Name: "ToolCallError", MemberNameProperty: "toolName"},
		}},
	})
	if result.Failed() {
		t.Fatalf("程序自己 catch 了绑定失败，运行应该成功：%+v", result.Error)
	}
	record, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("值形状不对：%T", result.Value)
	}
	if record["name"] != "ToolCallError" {
		t.Errorf("错误类名应是 ToolCallError，得到 %v", record["name"])
	}
	if record["tool"] != "boom" {
		t.Errorf("memberNameProperty 应带出成员名 boom，得到 %v", record["tool"])
	}
	if !strings.Contains(strings.TrimSpace(toString(record["msg"])), "工具炸了") {
		t.Errorf("message 应保留绑定错误正文，得到 %v", record["msg"])
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func TestRunBindingWithoutErrorClass(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "try { await tools.boom({}); } catch (e) { return e.message; }",
		Bindings: []ptc.BindingNamespace{{
			Global: "tools",
			Functions: map[string]ptc.BindingFunction{
				"boom": func(ctx context.Context, args any) (any, error) { return nil, errors.New("没有类型契约") },
			},
		}},
	})
	if result.Failed() {
		t.Fatalf("不该失败：%+v", result.Error)
	}
	if !strings.Contains(toString(result.Value), "没有类型契约") {
		t.Fatalf("没有 errorClass 时应抛普通 Error，message 保留正文，得到 %v", result.Value)
	}
}

func TestRunProtoMemberNameIsOrdinaryProperty(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "return await tools['__proto__']({})",
		Bindings: []ptc.BindingNamespace{{
			Global: "tools",
			Functions: map[string]ptc.BindingFunction{
				"__proto__": func(ctx context.Context, args any) (any, error) { return "ordinary", nil },
			},
		}},
	})
	if result.Failed() {
		t.Fatalf("`__proto__` 必须当普通自有属性：%+v", result.Error)
	}
	if toString(result.Value) != "ordinary" {
		t.Fatalf("得到 %v", result.Value)
	}
}

func TestRunUndefinedCompletionHasNoValue(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{Program: "console.log('done')"})
	if result.Failed() {
		t.Fatalf("不该失败：%+v", result.Error)
	}
	if result.HasValue {
		t.Fatalf("没有 return 值时不该有 value，得到 %v", result.Value)
	}
	if len(result.Logs) != 1 {
		t.Errorf("日志应保留：%v", result.Logs)
	}
}

func TestRunException(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{Program: "throw new Error('炸')"})
	if !result.Failed() {
		t.Fatal("抛异常必须失败")
	}
	if result.Error.Kind != ptc.FailureException {
		t.Errorf("kind 应为 exception，得到 %q", string(result.Error.Kind))
	}
	if !strings.Contains(result.Error.Message, "炸") {
		t.Errorf("message 应保留异常正文：%q", result.Error.Message)
	}
}

func TestRunSyntaxErrorIsException(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{Program: "return (((((("})
	if !result.Failed() || result.Error.Kind != ptc.FailureException {
		t.Fatalf("语法错误应按 exception 归类：%+v", result.Error)
	}
}

func TestRunInvalidOutput(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	cases := []struct {
		name    string
		program string
	}{
		{"函数", "return { f: function () {} }"},
		{"NaN", "return 1 / 0 * 0"},
		{"稀疏数组", "return [1, , 3]"},
		{"异形原型", "class C { constructor() { this.a = 1 } } return new C()"},
	}
	for _, tc := range cases {
		result := runResolved(t, provider, ptc.RunRequest{Program: tc.program})
		if !result.Failed() {
			t.Errorf("%s：不能无损穿过的完成值必须失败", tc.name)
			continue
		}
		if result.Error.Kind != ptc.FailureInvalidOutput {
			t.Errorf("%s：kind 应为 invalid-output，得到 %q", tc.name, string(result.Error.Kind))
		}
	}
}

func TestRunTimeoutKillsProcess(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	started := time.Now()
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "console.log('before'); while (true) {}",
		Timeout: ptc.TimeoutMs(700),
	})
	elapsed := time.Since(started)
	if !result.Failed() || result.Error.Kind != ptc.FailureTimeout {
		t.Fatalf("死循环必须在预算到期时报 timeout：%+v", result.Error)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("超时后没有及时停进程（耗时 %s）——那说明 kill 没生效", elapsed)
	}
}

func TestRunAbort(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	spec, err := provider.Resolve(ptc.RunRequest{
		Program: "await new Promise(function (r) { setTimeout(r, 30000) }); return 1",
		Timeout: ptc.TimeoutNone(),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(400 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	result, err := provider.Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Failed() || result.Error.Kind != ptc.FailureAbort {
		t.Fatalf("取消必须报 abort：%+v", result.Error)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("取消后没有及时停（耗时 %s）", elapsed)
	}
}

func TestRunWorkerExit(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	result := runResolved(t, provider, ptc.RunRequest{Program: "process.exit(3)"})
	if !result.Failed() {
		t.Fatal("进程提前退出必须失败")
	}
	if result.Error.Kind != ptc.FailureWorkerExit {
		t.Fatalf("kind 应为 worker-exit，得到 %q", string(result.Error.Kind))
	}
}

func TestRunOutputLimit(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, func(cfg *Config) { cfg.MaxOutputBytes = 200 })
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "for (let i = 0; i < 100; i += 1) { console.log('xxxxxxxxxxxxxxxxxxxx') } return 1",
	})
	if !result.Failed() || result.Error.Kind != ptc.FailureOutputLimit {
		t.Fatalf("超输出预算必须报 output-limit：%+v", result.Error)
	}
}

func TestRunConcurrentFreshProcesses(t *testing.T) {
	requireNode(t)
	provider := newTestProvider(t, nil)
	var wg sync.WaitGroup
	results := make([]ptc.RunResult, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			spec, err := provider.Resolve(ptc.RunRequest{
				Program: "globalThis.__mark = " + strconv.Itoa(index) + "; return globalThis.__mark",
			})
			if err != nil {
				t.Errorf("Resolve: %v", err)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			results[index], _ = provider.Run(ctx, spec)
		}(i)
	}
	wg.Wait()
	for i, result := range results {
		if result.Failed() {
			t.Fatalf("并发运行 %d 失败：%+v", i, result.Error)
		}
		if got := numberOf(t, result.Value).String(); got != strconv.Itoa(i) {
			t.Fatalf("并发运行 %d 的值串了：%s", i, got)
		}
	}
}

func itoa(v int) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.Repeat(" ", 0)+numberString(v), "\n", ""))
}

func numberString(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRunWithoutNodeReportsWorkerExit(t *testing.T) {
	// 这条**不需要**真 node：给一个存在的非 node 文件，验证失败归类。
	dir := t.TempDir()
	fake := filepath.Join(dir, "not-node.exe")
	if err := os.WriteFile(fake, []byte("not a program"), 0o644); err != nil {
		t.Fatalf("写假 node：%v", err)
	}
	provider := newTestProvider(t, func(cfg *Config) { cfg.NodePath = fake })
	result := runResolved(t, provider, ptc.RunRequest{Program: "return 1"})
	if !result.Failed() {
		t.Fatal("node 不可用时必须是一次失败的运行")
	}
	if result.Error.Kind != ptc.FailureWorkerExit {
		t.Errorf("kind 应为 worker-exit，得到 %q", string(result.Error.Kind))
	}
	if !strings.Contains(result.Error.Message, "Node") {
		t.Errorf("message 要点名 Node 不可用：%q", result.Error.Message)
	}
}
