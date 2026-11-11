// Package jobs 是官方 0.2.2-rc.2 后台任务失败分类的 Go 等价形状。
//
// 官方原件（随 rc.2 asar 发布的源码）：
//
//	@deepseek-ai/dsh-jobs/src/error.ts:16-24        JOB_ERROR_CODES（7 码）
//	@deepseek-ai/dsh-jobs/src/error.ts:30           JobError extends HarnessError{ name:'JobError', code }
//	@deepseek-ai/dsh-jobs-local/src/index.ts:121    waitAborted() → Error{ name:'AbortError' }
//	@deepseek-ai/dsh-tools/src/schema.ts:462-470    ToolArgsError{ name:'ToolArgsError', code:'INVALID_ARGS' }
//	@deepseek-ai/dsh-tool-jobs/src/index.ts:460-461 timeout_ms 正值校验（抛 ToolArgsError）
//
// # 为什么是一个独立包
//
// 码是**跨层契约**：job 工具（modules/exec/bash）产出它，transport 的四字段/流故障
// 投影它，`cmd/dshd` 的落盘取码器（`turn_error_codes.go`，官方 HarnessError.code 的
// 等价形状）按 `Code() string` 读它，v4 `tool/result.error.code` 持久化它。放在
// internal/jobs 里，三层拿的是同一份字面值，而不是各自抄一张表。
//
// # 不做的事
//
// 这里**不**判定谁该失败、也不改任何既有错误文本：只承载官方码，并把宿主既有的
// 失败（`spi.ErrJobNotFound` / `spi.ErrJobWrongInstance`）映射到官方分类。认不出来
// 的错误保持原样——宁可少一个码，也不许编一个 UNKNOWN 冒充真码。
package jobs

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"dshgo/internal/spi"
)

// 官方错误身份原词（逐字，不要"顺手改顺眼"）。这些值是持久化契约：
// 它们会落进 v4 `tool/result.error.{name,code}` 与 `turn/end.reason.error`，
// 客户端按 code 分支，拼错一个字符只在很远的将来表现为"模型老做错事"。
const (
	// ErrorName 是 JobError 的错误类名（官方 `this.name = 'JobError'`）。
	ErrorName = "JobError"
	// ErrorNameAbort 是被取消时的错误类名（官方 `waitAborted()` 的 `name='AbortError'`）。
	ErrorNameAbort = "AbortError"
	// AbortMessage 是取消错误的正文（官方 `new Error('wait aborted')`，逐字）。
	AbortMessage = "wait aborted"
	// ErrorNameToolArgs 是参数错误的类名（官方 ToolArgsError）。
	ErrorNameToolArgs = "ToolArgsError"
	// CodeInvalidArgs 是参数错误的码（官方 `super(..., 'INVALID_ARGS')`）。
	CodeInvalidArgs = "INVALID_ARGS"
)

// Code 是官方 {@link JobError} 的一个码。
type Code string

// 官方 JOB_ERROR_CODES，**顺序与官方数组一致**（`dsh-jobs/src/error.ts:16-24`）。
//
//   - CodeBackgroundDisabled —— 工具的配置把后台执行关掉了（官方
//     `enableRunInBackground:false`；宿主等价开关见 modules/exec/bash 的
//     EnvRunInBackground）。
//   - CodeControllerUnavailable —— jobs 服务在，但没有 serve 这个 owner 的 job controller。
//   - CodeNotFound —— id 没有对应任何已登记的任务。
//   - CodeForeignSession —— 任务属于另一个会话/实例。
//   - CodeOwnerNotLive —— 任务的 owner Agent 已经不是活的。
//   - CodeOwnerRegistryUnavailable —— 任务点名了 owner，但组合里没有 agent registry。
//   - CodeLimitReached —— owner 的并发任务上限已满。
const (
	CodeBackgroundDisabled       Code = "JOB_BACKGROUND_DISABLED"
	CodeControllerUnavailable    Code = "JOB_CONTROLLER_UNAVAILABLE"
	CodeNotFound                 Code = "JOB_NOT_FOUND"
	CodeForeignSession           Code = "JOB_FOREIGN_SESSION"
	CodeOwnerNotLive             Code = "JOB_OWNER_NOT_LIVE"
	CodeOwnerRegistryUnavailable Code = "JOB_OWNER_REGISTRY_UNAVAILABLE"
	CodeLimitReached             Code = "JOB_LIMIT_REACHED"
)

// officialCodes 是官方码表的固定顺序。新增/删除码必须显式改这里，
// 让"码表悄悄少了一个"过不了 `TestJobErrorCodesMatchOfficial`。
var officialCodes = []Code{
	CodeBackgroundDisabled,
	CodeControllerUnavailable,
	CodeNotFound,
	CodeForeignSession,
	CodeOwnerNotLive,
	CodeOwnerRegistryUnavailable,
	CodeLimitReached,
}

// Codes 返回官方 7 码的副本（调用方改不动码表本身）。
func Codes() []Code {
	out := make([]Code, len(officialCodes))
	copy(out, officialCodes)
	return out
}

// Valid 报告 c 是否是官方登记过的码。
func (c Code) Valid() bool {
	for _, known := range officialCodes {
		if c == known {
			return true
		}
	}
	return false
}

// JobError 是官方 `JobError` 的 Go 等价形状：
//
//	code  —— 稳定分类（官方 `HarnessError.code`），由 Code() 暴露给落盘取码器；
//	name  —— "JobError"（官方 `this.name`；本仓落盘按具体类型名取，正好对上）；
//	cause —— 底层原因（官方 `options.cause`），Unwrap 之后 errors.Is 仍可达。
//
// **消息文本不是契约**：官方每条码有自己的英文文案，宿主保留自己的中文文案与
// 可执行的下一步；客户端/模型只许按 code 分支。
type JobError struct {
	code    Code
	message string
	cause   error
}

// New 造一个 JobError。
//
// message 必须说清"哪个操作失败 + 安全的下一步"（官方构造器注释的同一要求：
// "names the job operation and the safe next step"）。
func New(code Code, message string, cause error) *JobError {
	return &JobError{code: code, message: message, cause: cause}
}

// Error 实现 error。
func (e *JobError) Error() string {
	if e == nil {
		return ""
	}
	if e.message != "" {
		return e.message
	}
	if e.cause != nil {
		return e.cause.Error()
	}
	return string(e.code)
}

// Unwrap 暴露 cause，保证 errors.Is/As 能穿过映射看到宿主 sentinel。
func (e *JobError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Code 返回官方码串（`cmd/dshd` 的 HarnessError 等价接口按它取码）。
func (e *JobError) Code() string {
	if e == nil {
		return ""
	}
	return string(e.code)
}

// JobCode 返回类型化码（宿主内部分支用）。
func (e *JobError) JobCode() Code {
	if e == nil {
		return ""
	}
	return e.code
}

// Name 返回官方类名。
func (e *JobError) Name() string { return ErrorName }

// CodeOf 报告 err 里是否承载着官方 job 码。
//
// 只认**真的带了码**的错误：认不出来就返回 false，调用方保持旧错误形状
// （宁可不带码，也不许把认不出来的失败折成 UNKNOWN）。
func CodeOf(err error) (Code, bool) {
	if err == nil {
		return "", false
	}
	var typed *JobError
	if errors.As(err, &typed) && typed != nil && typed.code != "" {
		return typed.code, true
	}
	return "", false
}

// From 把宿主既有的 job 失败映射成官方码。
//
// 已带码的原样返回（包括被 fmt.Errorf 包过的）。否则按宿主 sentinel 分类：
//
//	spi.ErrJobNotFound      → JOB_NOT_FOUND       （id 没有对应任务）
//	spi.ErrJobWrongInstance → JOB_FOREIGN_SESSION （任务属于另一个 exec 服务实例）
//
// 映射保留**原错误文本**并把原错误放进 cause，因此 `errors.Is(err, sentinel)`
// 在映射之后仍然成立——既有按 sentinel 分支的调用点不需要改。
func From(err error) (*JobError, bool) {
	if err == nil {
		return nil, false
	}
	var typed *JobError
	if errors.As(err, &typed) && typed != nil {
		return typed, true
	}
	switch {
	case errors.Is(err, spi.ErrJobNotFound):
		return New(CodeNotFound, err.Error(), err), true
	case errors.Is(err, spi.ErrJobWrongInstance):
		return New(CodeForeignSession, err.Error(), err), true
	}
	return nil, false
}

// AbortError 是官方 `waitAborted()` 的 Go 等价形状。
//
// 官方把普通 `Error('wait aborted')` 的 name 改成 'AbortError'，好让工具注册表
// 把它与普通失败区分开（"The name lets the tool registry classify it"）。
// 本仓靠**类型名**取 name，所以用同名类型承载：落盘就是 `name:'AbortError'`。
//
// 它不是 JobError：取消不是"任务失败分类"，不许带 JOB_* 码（官方同样没有）。
type AbortError struct{}

// Error 实现 error（官方正文逐字）。
func (e *AbortError) Error() string { return AbortMessage }

// Name 返回官方类名（与 spi.ErrorNameAbort 同一个词）。
func (e *AbortError) Name() string { return ErrorNameAbort }

// WaitAborted 造一个取消错误：调用方自己的信号中止了一次等待。
func WaitAborted() error { return &AbortError{} }

// IsAbort 报告 err 是不是一次"调用方取消的等待"。
func IsAbort(err error) bool {
	if err == nil {
		return false
	}
	var abort *AbortError
	return errors.As(err, &abort)
}

// ToolArgsError 是官方 `ToolArgsError` 的 Go 等价形状：
// name='ToolArgsError'、code='INVALID_ARGS'、消息 `invalid arguments: <violations>`。
type ToolArgsError struct {
	// Violations 是逐条违规（官方按 schema-walk 顺序；这里保持调用方给的顺序）。
	Violations []string
}

// Error 实现 error（官方拼接格式逐字）。
func (e *ToolArgsError) Error() string {
	if e == nil {
		return ""
	}
	return "invalid arguments: " + strings.Join(e.Violations, "; ")
}

// Code 返回官方码（落盘取码器按它读）。
func (e *ToolArgsError) Code() string { return CodeInvalidArgs }

// Name 返回官方类名。
func (e *ToolArgsError) Name() string { return ErrorNameToolArgs }

// ValidateWaitTimeout 是官方 `job_output` 对 `timeout_ms` 的校验（逐条）。
//
// 官方（`dsh-tool-jobs/src/index.ts:460-461`）：
//
//	if (args.timeout_ms !== undefined && (!Number.isFinite(args.timeout_ms) || args.timeout_ms <= 0))
//	  throw new ToolArgsError([`invalid timeout_ms: expected a positive number of milliseconds, got ${JSON.stringify(...)}`])
//
// 也就是说：**给了就必须是正的有限数**；0 与负数都拒绝（不是"改成默认值"）。
// 缺省（字段没给）不调用本函数——那种情况走默认等待时长。
func ValidateWaitTimeout(ms float64) error {
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms <= 0 {
		return &ToolArgsError{Violations: []string{fmt.Sprintf(
			"invalid timeout_ms: expected a positive number of milliseconds, got %s", formatJSONNumber(ms))}}
	}
	return nil
}

// formatJSONNumber 按官方 `JSON.stringify(number)` 的形状渲染参数值。
//
// JS 里 NaN/Infinity 序列化成 "null"，有限数用最短往返表示。
func formatJSONNumber(ms float64) string {
	if math.IsNaN(ms) || math.IsInf(ms, 0) {
		return "null"
	}
	return strconv.FormatFloat(ms, 'g', -1, 64)
}
