package ptc

import (
	"context"

	"dshgo/internal/sandbox"
)

// ProgramFormat 是源码的求值形态，与"provider 是否保留状态"无关。
//
// 官方 `src/types.ts:12`：`'function-body' | 'repl' | 'script'`。
type ProgramFormat string

const (
	// FormatFunctionBody 是 async 函数体：顶层 `await` 与 `return` 都可用。
	FormatFunctionBody ProgramFormat = "function-body"
	// FormatREPL 是 REPL 语句：顶层声明、`await`、末表达式可用，无顶层 `return`。
	FormatREPL ProgramFormat = "repl"
	// FormatScript 是脚本：与 REPL 同语法，末表达式是完成值。
	FormatScript ProgramFormat = "script"
)

// JsonValue 是穿过这条缝的**无损 JSON** 值域。
//
// 与官方 `PtcJsonValue` 同形：null / bool / number / string / 数组 / 对象。
// 函数、symbol、undefined、NaN/Inf、BigInt、循环引用都在值域之外——
// provider 必须让运行以 `invalid-output` 失败，**不得**替换成一个渲染后的字符串。
type JsonValue = any

// BindingFunction 是暴露给程序的一个宿主侧异步可调用对象。
//
// 官方 `src/types.ts:23`：`(args) => Promise<PtcJsonValue>`。args 与返回值都必须
// 是无损 JSON；provider 有责任把不可克隆的值报成错误，而不是悄悄损坏这次运行。
type BindingFunction func(ctx context.Context, args any) (any, error)

// BindingErrorClass 是一个命名空间在程序里可见的**有类型拒绝**契约。
//
// 官方 `src/types.ts:35-45`：runtime 在 `Name` 这个全局下注入一个真的错误构造器；
// 被拒绝的成员调用成为它的实例，并通过 `MemberNameProperty` 暴露成员名。
type BindingErrorClass struct {
	// Name 是构造器全局名，也是结果的 `Error.name`。
	Name string
	// MemberNameProperty 是承载成员名的自有属性名。
	MemberNameProperty string
}

// BindingNamespace 是程序可见的一个全局对象（例如 `tools`）。
//
// 官方 `src/types.ts:54-70`。函数名是任意字符串：provider 必须把 `__proto__`、
// `constructor` 这类名字当成**普通自有属性**（空原型对象），不能让原型链串味。
type BindingNamespace struct {
	// Global 是程序看到的全局标识符。
	Global string
	// Functions 是按"程序里怎么调"命名的成员。
	Functions map[string]BindingFunction
	// ErrorClass 可选：这个命名空间的有类型拒绝契约。
	ErrorClass *BindingErrorClass
}

// TimeoutChoice 复刻官方 `timeoutMs?: number | null` 的**三态**。
//
// 三态必须能区分，否则"没给"与"显式不要超时"会被压成同一个值：
//
//	{Set:false}                  → 用 provider 默认
//	{Set:true, NoDeadline:true}  → 显式 null：不要 elapsed deadline
//	{Set:true, Ms:n}             → 正数毫秒，provider 校验并封顶
type TimeoutChoice struct {
	// Set 表示调用方**显式给了**这个字段。
	Set bool
	// NoDeadline 表示显式 null。
	NoDeadline bool
	// Ms 是请求的毫秒预算（NoDeadline 为 false 时有效）。
	Ms int64
}

// TimeoutDefault 返回"没给"这一态。
func TimeoutDefault() TimeoutChoice { return TimeoutChoice{} }

// TimeoutNone 返回"显式不要 deadline"这一态（官方 workflow 适配器就是这么请求的）。
func TimeoutNone() TimeoutChoice { return TimeoutChoice{Set: true, NoDeadline: true} }

// TimeoutMs 返回"请求 n 毫秒"这一态。
func TimeoutMs(ms int64) TimeoutChoice { return TimeoutChoice{Set: true, Ms: ms} }

// RunRequest 是一次程序运行的调用方输入。
//
// 官方 `src/types.ts:76-110`。官方用 `signal?: AbortSignal` 表达"取消这次运行"；
// Go 里同语义的东西是 `Run(ctx, spec)` 的 ctx——这里不再放一个 signal 字段，
// 免得同一件事有两个来源。（语义仍是官方的：**硬停**，结果 kind=`abort`；
// 在途的绑定调用由**调用方**负责收尾，runtime 只停止继续问。）
type RunRequest struct {
	// Program 是源码，语言见 Runtime.Language，语法见 Runtime.ProgramFormat。
	Program string
	// StateOwner 是可选的内存状态身份（逐对象）。省略 = 要一个全新环境。
	// 不保留状态的 provider 忽略它。
	StateOwner any
	// Bindings 是暴露给程序的宿主函数，一个命名空间一个全局对象。
	Bindings []BindingNamespace
	// Cwd 是执行世界里的工作目录；空 = 由 provider 取默认。
	Cwd string
	// Timeout 是 elapsed 预算三态。
	Timeout TimeoutChoice
	// SandboxMode 是这次运行要的文件档位（官方字段名 `sandboxPolicy`）。
	// 没有 confinement 的 provider **必须拒绝**非 nil 值，不许默默忽略。
	SandboxMode *sandbox.Mode
}

// RunSpec 是**已解析**的执行输入：run 不会再补任何缺省值。
//
// 官方 `src/types.ts:113-118`。
type RunSpec struct {
	Program    string
	StateOwner any
	Bindings   []BindingNamespace
	// Cwd 是绝对路径。
	Cwd string
	// TimeoutMs 是封顶后的正数毫秒；nil 表示这次运行没有 elapsed deadline。
	TimeoutMs   *int64
	SandboxMode *sandbox.Mode
}

// FailureKind 是这次运行为什么失败。
//
// 官方 `src/types.ts:146`，八种**正交**结果：预算到期不是异常，取消不是超时，
// 底座死亡两者都不是。
type FailureKind string

const (
	// FailureException：程序抛了，或解析/转换失败。
	FailureException FailureKind = "exception"
	// FailureTimeout：实现自己的预算到期；message 要说清是哪一段。
	FailureTimeout FailureKind = "timeout"
	// FailureAbort：请求的 signal（Go 里是 ctx）触发了。
	FailureAbort FailureKind = "abort"
	// FailureWorkerExit：执行底座没结算就死了（例如 OOM）。
	FailureWorkerExit FailureKind = "worker-exit"
	// FailureInvalidOutput：完成值不是无损 JSON。
	FailureInvalidOutput FailureKind = "invalid-output"
	// FailureOutputLimit：序列化后的日志/值/诊断超过上限。
	FailureOutputLimit FailureKind = "output-limit"
	// FailureProtocol：程序发了非法或超预算的控制帧。
	FailureProtocol FailureKind = "protocol"
	// FailureSandboxUnavailable：要求的 confinement 建不起来。
	FailureSandboxUnavailable FailureKind = "sandbox-unavailable"
)

// Valid 报告 kind 是否在官方词表里。
func (k FailureKind) Valid() bool {
	switch k {
	case FailureException, FailureTimeout, FailureAbort, FailureWorkerExit,
		FailureInvalidOutput, FailureOutputLimit, FailureProtocol, FailureSandboxUnavailable:
		return true
	}
	return false
}

// RunFailure 是一次失败运行的原因。
type RunFailure struct {
	Kind    FailureKind
	Message string
}

// Enforcement 是选定后端报告的 confinement 完整度。
//
// 官方 `@deepseek-ai/dsh-sandbox/src/index.ts:76`：`'full' | 'partial'`。
type Enforcement string

const (
	// EnforcementFull 是完整执行。
	EnforcementFull Enforcement = "full"
	// EnforcementPartial 是部分执行（例如只挡了一部分路径）。
	EnforcementPartial Enforcement = "partial"
)

// RunSandbox 是这次运行实际施加的文件限制，**与结果成败无关地**独立上报。
//
// 官方 `src/types.ts:121-128`。
type RunSandbox struct {
	// Mode 是这次执行用的文件档位。
	Mode sandbox.Mode
	// Denied 表示程序失败文本匹配到了后端的拒绝诊断；它既不是执行证明，
	// 也不是穷尽的拒绝记录。
	Denied bool
	// Enforcement 是选定后端报告的完整度；全权档缺省。
	Enforcement Enforcement
}

// RunResult 是一次运行的结果。
//
// 官方 `src/types.ts:156-174`：**错误是结果上的一个字段，永远不是 run 的 reject**。
// 报告"程序失败了"是调用方的事，不是异常路径。
type RunResult struct {
	Sandbox  *RunSandbox
	Value    JsonValue
	HasValue bool
	Logs     []string
	Error    *RunFailure
}

// Failed 报告这次运行是否失败。
func (r RunResult) Failed() bool { return r.Error != nil }

// Fail 造一个失败结果（日志照样保留——超时/取消都必须带上已捕获输出）。
func Fail(kind FailureKind, message string, logs []string) RunResult {
	return RunResult{Logs: logs, Error: &RunFailure{Kind: kind, Message: message}}
}
