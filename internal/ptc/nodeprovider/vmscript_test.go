package nodeprovider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"dshgo/internal/ptc"
)

// 本文件测的是官方 `executionMode: vm-script` 的形态：
// `node:vm` 上下文里的 Script（`format=script`）+ 可选的 scriptPersistence。
//
// 隔离层级这件事在这里也有断言，而不是只写在文档里：
// `TestVMScriptContextIsScopeIsolationNotSecurityBoundary` 断言的正是
// "上下文里能拿到真实 Node 对象、能 await import('node:fs')" —— 也就是
// 官方 README 的 "the context is not a security boundary" 可验证的那一面。
// 如果有人把这个测试改成"挡住了 fs"，那就是在撒谎，不是修 bug。

func newVMScriptProvider(t *testing.T, persist bool, mutate func(*Config)) *Provider {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ExecutionMode = ModeVMScript
	cfg.ScriptPersistence = persist
	cfg.WorkspaceRoot = t.TempDir()
	if mutate != nil {
		mutate(&cfg)
	}
	provider, err := New(cfg)
	if err != nil {
		t.Fatalf("New(vm-script): %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

func objectValue(t *testing.T, value any) map[string]any {
	t.Helper()
	record, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("期望对象，实际 %T(%v)", value, value)
	}
	return record
}

func TestVMScriptDescriptors(t *testing.T) {
	provider := newVMScriptProvider(t, false, nil)
	if provider.ProgramFormat() != ptc.FormatScript {
		t.Errorf("vm-script 的 programFormat 应为 script（官方 execution.ts:162），得到 %q", string(provider.ProgramFormat()))
	}
	if provider.RetainsState() {
		t.Error("scriptPersistence 默认 false（官方 config.ts:37）：不保留状态")
	}
	if provider.Isolation() != "process" {
		t.Errorf("isolation 仍是 process：vm 上下文不改变执行底座，得到 %q", provider.Isolation())
	}
	if _, ok := provider.SandboxMode(); ok {
		t.Error("本 provider 没有文件 confinement，SandboxMode 必须报 false")
	}
	if _, ok := provider.Timeout(); !ok {
		t.Error("vm-script 同样支持逐次数字预算")
	}

	instructions := provider.ExecutionInstructions()
	for _, want := range []string{
		"script statements",
		"Top-level declarations are supported",
		"no OS file sandbox",
		"not a security boundary",
		"await import",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("执行说明必须写清 %q（provider 自有语法/输出/状态规则）：%q", want, instructions)
		}
	}
	// 冒名守卫：不许把 node:vm 说成沙箱/安全隔离。
	lower := strings.ToLower(instructions)
	for _, banned := range []string{"sandboxed", "sandbox isolation", "safe sandbox", "安全隔离", "沙箱隔离"} {
		if strings.Contains(lower, banned) {
			t.Errorf("执行说明里出现了冒名说法 %q —— node:vm 不是安全边界：%q", banned, instructions)
		}
	}

	persisting := newVMScriptProvider(t, true, nil)
	if !persisting.RetainsState() {
		t.Error("scriptPersistence:true 时必须报 RetainsState")
	}

	perCall := newTestProvider(t, nil)
	if perCall.ProgramFormat() != ptc.FormatFunctionBody {
		t.Errorf("per-call 仍应是 function-body，得到 %q", string(perCall.ProgramFormat()))
	}
	if instructions == perCall.ExecutionInstructions() {
		t.Error("两种形态的语法/状态规则不同，执行说明不能一样")
	}
}

func TestVMScriptCompletionValueAndDeclarations(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, nil)

	result := runResolved(t, provider, ptc.RunRequest{Program: "var total = 1 + 2\ntotal * 2"})
	if result.Failed() {
		t.Fatalf("script 形态不该失败：%+v", result.Error)
	}
	if got := numberOf(t, result.Value).String(); got != "6" {
		t.Fatalf("末表达式的完成值应为 6，得到 %s", got)
	}

	// 顶层 return 不支持（官方 README:72 "Top-level return ... are not supported"）。
	unsupported := runResolved(t, provider, ptc.RunRequest{Program: "return 5"})
	if !unsupported.Failed() || unsupported.Error.Kind != ptc.FailureException {
		t.Fatalf("顶层 return 必须按 exception 失败：%+v", unsupported.Error)
	}
	if !strings.Contains(unsupported.Error.Message, "did not compile") {
		t.Errorf("编译失败要说清是编译失败：%q", unsupported.Error.Message)
	}
}

func TestVMScriptAwaitsFinalExpressionPromise(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, nil)

	// 这正是官方 workflow-ptc 交给 script 形态的形状（host.ts:293：
	// `script: "(async () => {\n${PROGRAM}\n})()"`）。
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "(async () => {\n  await new Promise(function (r) { setTimeout(r, 20) });\n  return { ok: true, n: 7 };\n})()",
	})
	if result.Failed() {
		t.Fatalf("末表达式是 Promise 时必须 await 完成值：%+v", result.Error)
	}
	record := objectValue(t, result.Value)
	if record["ok"] != true {
		t.Errorf("对象完成值应完整穿过 JSON 边界：%v", record)
	}
	if got := numberOf(t, record["n"]).String(); got != "7" {
		t.Errorf("n 应为 7，得到 %s", got)
	}
}

func TestVMScriptBareTopLevelAwaitNamesTheLimitation(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, nil)

	result := runResolved(t, provider, ptc.RunRequest{Program: "const v = await Promise.resolve(1)\nv"})
	if !result.Failed() || result.Error.Kind != ptc.FailureException {
		t.Fatalf("裸顶层 await 目前不支持，必须是一次 exception：%+v", result.Error)
	}
	message := result.Error.Message
	for _, want := range []string{"await is only valid in async functions", "vm-script-await.ts", "(async () =>"} {
		if !strings.Contains(message, want) {
			t.Errorf("失败消息要如实点名缺口与写法（缺 %q）：%q", want, message)
		}
	}
}

func TestVMScriptNonStrictUnlessOptedIn(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, nil)

	// 官方 README:72："Script syntax is non-strict unless the program opts into strict mode"。
	loose := runResolved(t, provider, ptc.RunRequest{Program: "implicitlyAssigned = 7\nimplicitlyAssigned"})
	if loose.Failed() {
		t.Fatalf("非严格模式允许隐式全局：%+v", loose.Error)
	}
	if got := numberOf(t, loose.Value).String(); got != "7" {
		t.Fatalf("隐式全局赋值应得 7，得到 %s", got)
	}

	strict := runResolved(t, provider, ptc.RunRequest{Program: "'use strict'\nstrictMiss = 1"})
	if !strict.Failed() || strict.Error.Kind != ptc.FailureException {
		t.Fatalf("显式严格模式必须挡隐式全局：%+v", strict.Error)
	}
}

func TestVMScriptBindingsAndErrorClass(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, nil)

	result := runResolved(t, provider, ptc.RunRequest{
		Program: "(async () => {\n" +
			"  var sum = await tools.add({ a: 1, b: 2 });\n" +
			"  var record = { sum: sum };\n" +
			"  try { await tools.boom({}); } catch (e) {\n" +
			"    record.name = e.name; record.tool = e.toolName; record.typed = e instanceof ToolCallError;\n" +
			"  }\n" +
			"  return record;\n" +
			"})()",
		Bindings: []ptc.BindingNamespace{{
			Global: "tools",
			Functions: map[string]ptc.BindingFunction{
				"add": func(ctx context.Context, args any) (any, error) {
					record, ok := args.(map[string]any)
					if !ok {
						t.Errorf("绑定收到 %T，不是对象", args)
						return nil, errors.New("args")
					}
					total := 0
					for _, v := range record {
						number, ok := v.(json.Number)
						if !ok {
							t.Errorf("参数值应是 json.Number，得到 %T", v)
							return nil, errors.New("args")
						}
						parsed, err := number.Int64()
						if err != nil {
							return nil, err
						}
						total += int(parsed)
					}
					return json.Number(numberString(total)), nil
				},
				"boom": func(ctx context.Context, args any) (any, error) {
					return nil, errors.New("工具炸了")
				},
			},
			ErrorClass: &ptc.BindingErrorClass{Name: "ToolCallError", MemberNameProperty: "toolName"},
		}},
	})
	if result.Failed() {
		t.Fatalf("vm-script 里的绑定往返不该失败：%+v", result.Error)
	}
	record := objectValue(t, result.Value)
	if got := numberOf(t, record["sum"]).String(); got != "3" {
		t.Errorf("绑定返回值应为 3，得到 %s", got)
	}
	if record["name"] != "ToolCallError" {
		t.Errorf("错误类名应为 ToolCallError，得到 %v", record["name"])
	}
	if record["tool"] != "boom" {
		t.Errorf("memberNameProperty 应带出成员名，得到 %v", record["tool"])
	}
	// 官方 src/process.ts:129-134：错误构造器同时也是程序可 instanceof 的全局。
	if record["typed"] != true {
		t.Errorf("错误类必须同时是 context 里的全局构造器（class的空壳不算）：%v", record["typed"])
	}
}

// TestVMScriptContextIsScopeIsolationNotSecurityBoundary 是这份文件的**纪律测试**。
//
// 它断言的是"没挡住什么"：vm 上下文拿到的是外层真实 Node 对象、能 await import
// 到 node:fs、process.exit 一样能杀进程。这不是缺陷，是官方对 vm-script 的定义
// （README.md:72 "the context is not a security boundary"）——如果有人把这里改成
// 「fs 被挡住了」，那就是把上下文隔离冒名成安全隔离。
func TestVMScriptContextIsScopeIsolationNotSecurityBoundary(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, nil)

	result := runResolved(t, provider, ptc.RunRequest{
		Program: "(async () => {\n" +
			"  var fsmod = await import('node:fs');\n" +
			"  return {\n" +
			"    processType: typeof process,\n" +
			"    versionIsString: typeof process.version === 'string',\n" +
			"    requireType: typeof require,\n" +
			"    bufferType: typeof Buffer,\n" +
			"    fsReadFile: typeof fsmod.readFileSync,\n" +
			"  };\n" +
			"})()",
	})
	if result.Failed() {
		t.Fatalf("不该失败：%+v", result.Error)
	}
	record := objectValue(t, result.Value)
	if record["processType"] != "object" || record["versionIsString"] != true {
		t.Errorf("vm 上下文里拿到的 process 就是外层真实对象：%v", record)
	}
	if record["bufferType"] != "function" {
		t.Errorf("Buffer 同样来自外层 realm：%v", record)
	}
	if record["requireType"] != "undefined" {
		t.Errorf("require 不在 globalThis 上，所以 context 里不可见（这是作用域事实，不是安全边界）：%v", record)
	}
	if record["fsReadFile"] != "function" {
		t.Errorf("await import('node:fs') 必须能拿到真实模块 —— 文件访问是按进程权限生效的：%v", record)
	}
}

func TestVMScriptInvalidOutputIsRealmAware(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, nil)

	// vm realm 创建的对象原型与外层 Object.prototype 不是同一个对象；判据必须
	// realm 无关，否则合法的 script 完成值会被误判成"异形原型"。
	valid := runResolved(t, provider, ptc.RunRequest{Program: "({ a: 1, list: [1, 2, 3] })"})
	if valid.Failed() {
		t.Fatalf("realm 里创建的普通对象/数组必须能无损穿过：%+v", valid.Error)
	}
	record := objectValue(t, valid.Value)
	if got := numberOf(t, record["a"]).String(); got != "1" {
		t.Errorf("a 应为 1，得到 %s", got)
	}

	cases := []struct{ name, program string }{
		{"类实例", "class C { constructor() { this.a = 1 } } new C()"},
		{"稀疏数组", "[1, , 3]"},
		{"数组上的非索引属性", "var a = [1, 2]; a.extra = 3; a"},
		{"符号键", "var o = { a: 1 }; o[Symbol('s')] = 2; o"},
		{"不可枚举键", "var o = { a: 1 }; Object.defineProperty(o, 'hidden', { value: 2 }); o"},
		{"函数", "({ f: function () {} })"},
	}
	for _, tc := range cases {
		result := runResolved(t, provider, ptc.RunRequest{Program: tc.program})
		if !result.Failed() {
			t.Errorf("%s：不能无损穿过的完成值必须失败", tc.name)
			continue
		}
		if result.Error.Kind != ptc.FailureInvalidOutput {
			t.Errorf("%s：kind 应为 invalid-output，得到 %q（%s）", tc.name, string(result.Error.Kind), result.Error.Message)
		}
	}
}

func TestVMScriptRetainsStateAcrossCalls(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, true, nil)
	owner := &struct{ name string }{"owner-a"}

	first := runResolved(t, provider, ptc.RunRequest{
		Program:    "var kept = 41;\nvar firstPid = process.pid;\nfirstPid",
		StateOwner: owner,
	})
	if first.Failed() {
		t.Fatalf("第一轮不该失败：%+v", first.Error)
	}
	firstPid := numberOf(t, first.Value).String()

	second := runResolved(t, provider, ptc.RunRequest{
		Program:    "({ kept: kept + 1, pid: process.pid, sameProcess: process.pid === firstPid })",
		StateOwner: owner,
	})
	if second.Failed() {
		t.Fatalf("第二轮不该失败：%+v", second.Error)
	}
	record := objectValue(t, second.Value)
	if got := numberOf(t, record["kept"]).String(); got != "42" {
		t.Errorf("顶层声明必须跨次保留（官方 README:74）：得到 %s", got)
	}
	if record["sameProcess"] != true {
		t.Errorf("scriptPersistence 必须复用同一个进程：%v", record)
	}
	if got := numberOf(t, record["pid"]).String(); got != firstPid {
		t.Errorf("进程号应变（同一个进程），首轮 %s 与次轮 %s", firstPid, got)
	}
}

func TestVMScriptWithoutPersistenceIgnoresStateOwner(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, nil)
	owner := &struct{ name string }{"owner-b"}

	first := runResolved(t, provider, ptc.RunRequest{
		Program:    "var kept = 1;\nprocess.pid",
		StateOwner: owner,
	})
	if first.Failed() {
		t.Fatalf("第一轮不该失败：%+v", first.Error)
	}
	firstPid := numberOf(t, first.Value).String()

	// 官方 README:74："scriptPersistence: false is the default: every call uses a
	// fresh process and context, ignoring stateOwner."
	second := runResolved(t, provider, ptc.RunRequest{
		Program:    "({ leaked: typeof kept, pid: process.pid })",
		StateOwner: owner,
	})
	if second.Failed() {
		t.Fatalf("第二轮不该失败：%+v", second.Error)
	}
	record := objectValue(t, second.Value)
	if record["leaked"] != "undefined" {
		t.Errorf("scriptPersistence=false 时不许继承上一轮声明：%v", record)
	}
	if got := numberOf(t, record["pid"]).String(); got == firstPid {
		t.Errorf("scriptPersistence=false 时每次必须是新进程（两轮 pid 都是 %s）", got)
	}
}

func TestVMScriptRetainedConcurrentRunIsMisuse(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, true, nil)
	owner := &struct{}{}

	busy, err := provider.Resolve(ptc.RunRequest{
		Program:    "var s = Date.now();\nwhile (Date.now() - s < 1200) {}\n'slow'",
		StateOwner: owner,
		Timeout:    ptc.TimeoutMs(20_000),
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := provider.Run(context.Background(), busy)
		done <- runErr
	}()
	time.Sleep(400 * time.Millisecond)

	second, err := provider.Resolve(ptc.RunRequest{Program: "'fast'", StateOwner: owner})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, runErr := provider.Run(context.Background(), second)
	if !ptc.IsMisuse(runErr) {
		t.Fatalf("同一 stateOwner 上的并发运行必须是误用（官方 worker.ts:77 assertIdle），得到 %v", runErr)
	}
	if firstErr := <-done; firstErr != nil {
		t.Fatalf("在途那一轮不该因为被拒的第二轮出错：%v", firstErr)
	}
}

func TestVMScriptReleaseClearsRetainedState(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, true, nil)
	owner := &struct{}{}

	first := runResolved(t, provider, ptc.RunRequest{Program: "var kept = 1;\nprocess.pid", StateOwner: owner})
	if first.Failed() {
		t.Fatalf("第一轮不该失败：%+v", first.Error)
	}
	firstPid := numberOf(t, first.Value).String()

	if err := provider.Release(context.Background(), owner); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second := runResolved(t, provider, ptc.RunRequest{
		Program:    "({ leaked: typeof kept, pid: process.pid })",
		StateOwner: owner,
	})
	if second.Failed() {
		t.Fatalf("释放后重开不该失败：%+v", second.Error)
	}
	record := objectValue(t, second.Value)
	if record["leaked"] != "undefined" {
		t.Errorf("Release 之后保留状态必须清掉：%v", record)
	}
	if got := numberOf(t, record["pid"]).String(); got == firstPid {
		t.Errorf("Release 之后必须是新进程（两轮 pid 都是 %s）", got)
	}
}

func TestVMScriptTimeoutClearsRetainedState(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, true, nil)
	owner := &struct{}{}

	first := runResolved(t, provider, ptc.RunRequest{Program: "var kept = 5;\nprocess.pid", StateOwner: owner})
	if first.Failed() {
		t.Fatalf("第一轮不该失败：%+v", first.Error)
	}
	firstPid := numberOf(t, first.Value).String()

	timedOut := runResolved(t, provider, ptc.RunRequest{
		Program:    "while (true) {}",
		StateOwner: owner,
		Timeout:    ptc.TimeoutMs(600),
	})
	if !timedOut.Failed() || timedOut.Error.Kind != ptc.FailureTimeout {
		t.Fatalf("死循环必须按 timeout 失败：%+v", timedOut.Error)
	}
	if !strings.Contains(timedOut.Error.Message, "retained variables were cleared") {
		t.Errorf("超时消息要说清保留状态被清掉（官方 worker.ts:167-169）：%q", timedOut.Error.Message)
	}

	third := runResolved(t, provider, ptc.RunRequest{
		Program:    "({ leaked: typeof kept, pid: process.pid })",
		StateOwner: owner,
	})
	if third.Failed() {
		t.Fatalf("超时之后重开不该失败：%+v", third.Error)
	}
	record := objectValue(t, third.Value)
	if record["leaked"] != "undefined" {
		t.Errorf("超时必须清掉保留状态：%v", record)
	}
	if got := numberOf(t, record["pid"]).String(); got == firstPid {
		t.Errorf("超时后必须换新进程（两轮 pid 都是 %s）", got)
	}
}

func TestVMScriptExceptionKeepsRetainedProcess(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, true, nil)
	owner := &struct{}{}

	first := runResolved(t, provider, ptc.RunRequest{Program: "var kept = 5;\nprocess.pid", StateOwner: owner})
	if first.Failed() {
		t.Fatalf("第一轮不该失败：%+v", first.Error)
	}
	firstPid := numberOf(t, first.Value).String()

	thrown := runResolved(t, provider, ptc.RunRequest{Program: "throw new Error('boom')", StateOwner: owner})
	if !thrown.Failed() || thrown.Error.Kind != ptc.FailureException {
		t.Fatalf("程序自己抛必须按 exception 失败：%+v", thrown.Error)
	}

	third := runResolved(t, provider, ptc.RunRequest{
		Program:    "({ kept: kept, pid: process.pid })",
		StateOwner: owner,
	})
	if third.Failed() {
		t.Fatalf("程序抛异常之后进程仍该健康：%+v", third.Error)
	}
	record := objectValue(t, third.Value)
	if got := numberOf(t, record["kept"]).String(); got != "5" {
		t.Errorf("程序自己抛了不该清状态（官方只清超时/取消/超限）：%v", record)
	}
	if got := numberOf(t, record["pid"]).String(); got != firstPid {
		t.Errorf("程序自己抛了不该换进程：首轮 %s，第三轮 %s", firstPid, got)
	}
}

func TestVMScriptProgramEnvironmentIsScrubbed(t *testing.T) {
	requireNode(t)
	t.Setenv("DSH_PTC_SECRET_MARKER", "must-not-leak")
	provider := newVMScriptProvider(t, false, nil)

	result := runResolved(t, provider, ptc.RunRequest{
		Program: "var names = Object.keys(process.env).map(function (k) { return k.toUpperCase() });\n" +
			"({ marker: names.indexOf('DSH_PTC_SECRET_MARKER') !== -1, path: names.indexOf('PATH') !== -1 })",
	})
	if result.Failed() {
		t.Fatalf("不该失败：%+v", result.Error)
	}
	record := objectValue(t, result.Value)
	if record["marker"] == true {
		t.Error("程序可见的 process.env 里不许有宿主凭据类变量（官方 environment.ts:13-19）")
	}
	if record["path"] != true {
		t.Errorf("原生子进程启动路径必须保留（PATH/PATHEXT/SYSTEMROOT/WINDIR/TEMP/TMP）：%v", record)
	}
}

func TestVMScriptOutputLimit(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, func(cfg *Config) { cfg.MaxOutputBytes = 200 })
	result := runResolved(t, provider, ptc.RunRequest{
		Program: "for (var i = 0; i < 100; i += 1) { console.log('xxxxxxxxxxxxxxxxxxxx') }\n1",
	})
	if !result.Failed() || result.Error.Kind != ptc.FailureOutputLimit {
		t.Fatalf("超输出预算必须报 output-limit：%+v", result.Error)
	}
}

func TestVMScriptWorkerExit(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, false, nil)
	result := runResolved(t, provider, ptc.RunRequest{Program: "process.exit(3)"})
	if !result.Failed() || result.Error.Kind != ptc.FailureWorkerExit {
		t.Fatalf("程序自杀必须按 worker-exit 失败：%+v", result.Error)
	}
}

func TestStateOwnerKey(t *testing.T) {
	type owner struct{ id int }
	pointer := &owner{1}
	first, err := stateOwnerKey(pointer)
	if err != nil {
		t.Fatalf("指针 owner 应有 key：%v", err)
	}
	second, err := stateOwnerKey(pointer)
	if err != nil {
		t.Fatalf("同一个指针必须得到同一个 key：%v", err)
	}
	if first != second {
		t.Errorf("同一个 owner 的 key 必须稳定：%q vs %q", first, second)
	}
	other, err := stateOwnerKey(&owner{1})
	if err != nil {
		t.Fatalf("另一个指针：%v", err)
	}
	if other == first {
		t.Errorf("不同对象即使内容相同也必须是不同 owner（官方 WeakMap 身份语义）：%q", other)
	}
	if _, err := stateOwnerKey([]int{1, 2}); err == nil {
		t.Error("不可比较且无稳定身份的值不能当 owner")
	}
	if _, err := stateOwnerKey("agent-1"); err != nil {
		t.Errorf("可比较值允许当 owner（Go 没有 WeakMap 的等价退化）：%v", err)
	}
}

// TestVMScriptBackgroundOutputDoesNotLeakIntoNextCall 钉住输出归属。
//
// 官方 src/execution.ts:130："After completion, timeout, or cancellation, new tool calls
// are rejected and captured output is discarded; background work may still change state."
// 常驻会话里上一轮留下的定时器会继续跑，它的 console.log **不能**变成下一轮的输出
// （worker 用 AsyncLocalStorage 把输出归到发起它的那次求值）。
func TestVMScriptBackgroundOutputDoesNotLeakIntoNextCall(t *testing.T) {
	requireNode(t)
	provider := newVMScriptProvider(t, true, nil)
	owner := &struct{}{}

	first := runResolved(t, provider, ptc.RunRequest{
		Program:    "setInterval(function () { console.log('bg-tick') }, 5);\n'first'",
		StateOwner: owner,
	})
	if first.Failed() {
		t.Fatalf("第一轮不该失败：%+v", first.Error)
	}

	second := runResolved(t, provider, ptc.RunRequest{
		Program:    "(async () => {\n  await new Promise(function (r) { setTimeout(r, 150) });\n  return 'second';\n})()",
		StateOwner: owner,
	})
	if second.Failed() {
		t.Fatalf("第二轮不该失败：%+v", second.Error)
	}
	for _, line := range second.Logs {
		if strings.Contains(line, "bg-tick") {
			t.Fatalf("上一轮后台定时器的输出泄漏进了这一轮：%v", second.Logs)
		}
	}
}
