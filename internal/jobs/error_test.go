package jobs

// 官方 0.2.2-rc.2 `JobError` 的 Go 等价形状（R3-G15）。
//
// 官方原件：
//
//	@deepseek-ai/dsh-jobs/src/error.ts:16-24   JOB_ERROR_CODES（7 码，顺序即契约）
//	@deepseek-ai/dsh-jobs/src/error.ts:30      JobError extends HarnessError{ name:'JobError', code }
//	@deepseek-ai/dsh-jobs-local/src/index.ts:121   waitAborted() → name='AbortError'
//	@deepseek-ai/dsh-tool-jobs/src/index.ts:460-461  timeout_ms 正值校验 → ToolArgsError
//
// 这里的断言全部是**持久化契约**：码会落进 v4 `tool/result.error.code` 与
// `turn/end.reason.error.code`，客户端按 code 分支；拼错一个字符只在很远的
// 将来表现为"模型老做错事"。所以每个码、每个名字都逐字钉住。

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"dshgo/internal/spi"
)

// TestJobErrorCodesMatchOfficial 钉住 7 码的**取值与顺序**（官方数组顺序）。
func TestJobErrorCodesMatchOfficial(t *testing.T) {
	want := []Code{
		CodeBackgroundDisabled,
		CodeControllerUnavailable,
		CodeNotFound,
		CodeForeignSession,
		CodeOwnerNotLive,
		CodeOwnerRegistryUnavailable,
		CodeLimitReached,
	}
	got := Codes()
	if len(got) != len(want) {
		t.Fatalf("码表长度 %d，期望 %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个码 = %q，期望 %q", i, got[i], want[i])
		}
		if !want[i].Valid() {
			t.Fatalf("%q 没被判为合法码", want[i])
		}
	}
	// 逐字值必须与官方 TS 常量一致（不是"看着像"）。
	literals := map[Code]string{
		CodeBackgroundDisabled:       "JOB_BACKGROUND_DISABLED",
		CodeControllerUnavailable:    "JOB_CONTROLLER_UNAVAILABLE",
		CodeNotFound:                 "JOB_NOT_FOUND",
		CodeForeignSession:           "JOB_FOREIGN_SESSION",
		CodeOwnerNotLive:             "JOB_OWNER_NOT_LIVE",
		CodeOwnerRegistryUnavailable: "JOB_OWNER_REGISTRY_UNAVAILABLE",
		CodeLimitReached:             "JOB_LIMIT_REACHED",
	}
	for code, literal := range literals {
		if string(code) != literal {
			t.Fatalf("%q 的字面值必须逐字等于官方 %q", code, literal)
		}
	}
	if Code("JOB_UNKNOWN").Valid() {
		t.Fatalf("未登记的码不许被判为合法")
	}
}

// TestJobErrorCarriesCodeAndName 钉住错误对象的三个身份面：
//   - `name == 'JobError'`（官方 `this.name`；本仓的落盘取码器按具体类型名取 name）
//   - `code` 逐字是官方码（官方 `HarnessError.code`；本仓按 `Code() string` 取码）
//   - 包一层 fmt.Errorf 之后身份不丢（`errors.As` 能取回来）
func TestJobErrorCarriesCodeAndName(t *testing.T) {
	cause := errors.New("底层原因")
	err := New(CodeNotFound, "unknown job job-1", cause)

	if err.Code() != string(CodeNotFound) {
		t.Fatalf("Code() = %q，期望 %q", err.Code(), CodeNotFound)
	}
	if err.JobCode() != CodeNotFound {
		t.Fatalf("JobCode() = %q", err.JobCode())
	}
	if err.Name() != ErrorName {
		t.Fatalf("Name() = %q，期望 %q", err.Name(), ErrorName)
	}
	typ := reflect.TypeOf(err)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Name() != "JobError" {
		t.Fatalf("Go 类型名 = %q，期望 JobError（落盘 name 由类型名取）", typ.Name())
	}
	if !strings.Contains(err.Error(), "unknown job job-1") {
		t.Fatalf("Error() 没带官方 message：%q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Unwrap 丢了 cause")
	}

	wrapped := fmt.Errorf("启动后台任务: %w", err)
	var back *JobError
	if !errors.As(wrapped, &back) {
		t.Fatalf("包一层之后 errors.As 取不回 *JobError")
	}
	if back.JobCode() != CodeNotFound {
		t.Fatalf("包一层之后码变了：%q", back.JobCode())
	}
}

// TestJobErrorFromHostSentinels 钉住"宿主既有失败 → 官方码"的映射：
// 映射**保留原错误文本与 cause**（调用方仍能 errors.Is 到宿主 sentinel）。
func TestJobErrorFromHostSentinels(t *testing.T) {
	notFound := fmt.Errorf("%w %q；用 job_list 看有哪些", spi.ErrJobNotFound, "job-9")
	je, ok := From(notFound)
	if !ok {
		t.Fatalf("ErrJobNotFound 必须映射成 JobError")
	}
	if je.JobCode() != CodeNotFound {
		t.Fatalf("ErrJobNotFound → %q，期望 %q", je.JobCode(), CodeNotFound)
	}
	if !errors.Is(je, spi.ErrJobNotFound) {
		t.Fatalf("映射之后 errors.Is(spi.ErrJobNotFound) 必须仍为真")
	}
	if !strings.Contains(je.Error(), "job-9") {
		t.Fatalf("映射必须保留原错误文本：%q", je.Error())
	}

	foreign := fmt.Errorf("%w: job-a-1", spi.ErrJobWrongInstance)
	je, ok = From(foreign)
	if !ok || je.JobCode() != CodeForeignSession {
		t.Fatalf("ErrJobWrongInstance → %v/%q，期望 %q", ok, je.JobCode(), CodeForeignSession)
	}

	// 已经是 JobError 的原样返回（包括被包过的）。
	original := New(CodeLimitReached, "limit", nil)
	je, ok = From(fmt.Errorf("起任务: %w", original))
	if !ok || je != original {
		t.Fatalf("已有 JobError 必须原样返回")
	}

	// 认不出来的错误不许被编一个码（宁可不带码，也不许折成 UNKNOWN）。
	if _, ok := From(errors.New("随机失败")); ok {
		t.Fatalf("无法分类的错误不许凭空给码")
	}
	if _, ok := CodeOf(errors.New("随机失败")); ok {
		t.Fatalf("无法分类的错误不许凭空给码（CodeOf）")
	}
}

// TestEveryJobCodeHasTrigger 每个官方码至少一条触发用例。
//
// **宿主真实产生点**（在 modules/exec/bash 与 transport 各有用例）：
//   - JOB_NOT_FOUND            ← spi.ErrJobNotFound（job_output/job_kill/job.list 找不到 id）
//   - JOB_FOREIGN_SESSION      ← spi.ErrJobWrongInstance（跨实例/跨会话的 id）
//   - JOB_LIMIT_REACHED        ← jobs.maxConcurrentJobsPerOwner 上限
//   - JOB_BACKGROUND_DISABLED  ← DSHD_RUN_IN_BACKGROUND=0（官方 enableRunInBackground:false 的等价开关）
//
// **官方 composition 专有、宿主没有对应产生点**（如实登记，不假装）：
//   - JOB_CONTROLLER_UNAVAILABLE       —— 官方"jobs 服务在、但没有 controller 服务这个 owner"；
//     宿主是单进程 kernel，`spi.Job` 自己就是 controller（缺 controller 时走 legacy Kill 回落）
//   - JOB_OWNER_NOT_LIVE               —— 官方"owner Agent 不在了"；宿主按 exec 服务实例隔离 owner
//   - JOB_OWNER_REGISTRY_UNAVAILABLE   —— 官方"组合里没有 agent registry"；宿主不靠 registry 解析 owner
//
// 后三者仍是**必须存在于码表里**的 wire 契约（客户端按码分支），所以这里用构造器
// 证明它们可被承载、可被认出；triggers 里逐条注明来源。
func TestEveryJobCodeHasTrigger(t *testing.T) {
	triggers := map[Code]func() error{
		CodeNotFound:           func() error { return From2(t, fmt.Errorf("%w job-1", spi.ErrJobNotFound)) },
		CodeForeignSession:     func() error { return From2(t, fmt.Errorf("%w job-2", spi.ErrJobWrongInstance)) },
		CodeLimitReached:       func() error { return New(CodeLimitReached, "limit reached", nil) },
		CodeBackgroundDisabled: func() error { return New(CodeBackgroundDisabled, "run_in_background disabled", nil) },
		CodeControllerUnavailable: func() error {
			return New(CodeControllerUnavailable, "no job controller serves this agent", nil)
		},
		CodeOwnerNotLive: func() error {
			return New(CodeOwnerNotLive, `session "s-1" has no live agent`, nil)
		},
		CodeOwnerRegistryUnavailable: func() error {
			return New(CodeOwnerRegistryUnavailable, "no agent registry", nil)
		},
	}
	for _, code := range Codes() {
		trigger, ok := triggers[code]
		if !ok {
			t.Fatalf("码 %q 没有触发用例（每个码至少一条）", code)
		}
		err := trigger()
		got, ok := CodeOf(err)
		if !ok || got != code {
			t.Fatalf("触发用例没产出 %q（得到 %q/%v，err=%v）", code, got, ok, err)
		}
	}
}

// From2 是测试辅助：From 必须成功，否则直接 fail。
func From2(t *testing.T, err error) error {
	t.Helper()
	je, ok := From(err)
	if !ok {
		t.Fatalf("From(%v) 没认出 JobError", err)
	}
	return je
}

// TestWaitAbortedIsNamedAbortError 钉住 `waitAborted()`：官方是普通 Error 只改 name，
// 本仓用同名 Go 类型承载，落盘 name 取类型名即 "AbortError"，消息逐字 "wait aborted"。
func TestWaitAbortedIsNamedAbortError(t *testing.T) {
	err := WaitAborted()
	if err == nil {
		t.Fatal("WaitAborted() 返回 nil")
	}
	if !IsAbort(err) {
		t.Fatalf("IsAbort 没认出 %T", err)
	}
	if got := err.Error(); got != AbortMessage {
		t.Fatalf("消息 = %q，期望 %q", got, AbortMessage)
	}
	typ := reflect.TypeOf(err)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Name() != ErrorNameAbort {
		t.Fatalf("Go 类型名 = %q，期望 %q（落盘 name 取类型名）", typ.Name(), ErrorNameAbort)
	}
	if _, isJob := CodeOf(err); isJob {
		t.Fatalf("取消不是 JobError：不许带 JOB_* 码")
	}
}

// TestValidateWaitTimeout 钉住官方 `job_output` 的 timeout_ms 规则：
// 给了就必须是**正的有限数**；缺省（不调用本函数）走默认值。
func TestValidateWaitTimeout(t *testing.T) {
	for _, bad := range []float64{0, -1, -0.5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		err := ValidateWaitTimeout(bad)
		if err == nil {
			t.Fatalf("%v 必须被拒", bad)
		}
		var args *ToolArgsError
		if !errors.As(err, &args) {
			t.Fatalf("%v 的错误类型 = %T，期望 *ToolArgsError（官方 ToolArgsError）", bad, err)
		}
		if args.Code() != CodeInvalidArgs {
			t.Fatalf("码 = %q，期望 %q", args.Code(), CodeInvalidArgs)
		}
		if args.Name() != ErrorNameToolArgs {
			t.Fatalf("name = %q，期望 %q", args.Name(), ErrorNameToolArgs)
		}
		if !strings.Contains(args.Error(), "invalid timeout_ms: expected a positive number of milliseconds") {
			t.Fatalf("消息没照官方口径：%q", args.Error())
		}
	}
	for _, good := range []float64{1, 0.5, 5000, 120000} {
		if err := ValidateWaitTimeout(good); err != nil {
			t.Fatalf("%v 应当合法：%v", good, err)
		}
	}
}
