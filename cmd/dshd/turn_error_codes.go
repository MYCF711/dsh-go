package main

// rc.2 失败码落盘：四个生产写入点的**唯一**取码处。
//
// 官方 rc.2 把一批包错误统一到带 `code` 的 `HarnessError`（`dsh-error/lib/index.js:13-21`），
// 于是这些码会原样落进 `turn/end.reason.error.code` 与 `tool/result.error.code`，
// 客户端按码分支（重试、附件重选、清理告警）。以前这条信息在写入点被折叠成
// `{name:"Error", message:"..."}`——码没了，客户端只剩"未知失败"。
//
// 本文件只回答一个问题：**这个错误对象自己带的稳定码是什么**。它不查表、
// 不按消息文本猜、不把认不出来的错误改写成 `UNKNOWN`：
//
//  1. `Code() string`：官方 `HarnessError{code}` 在 Go 侧的等价形状。
//     `internal/llm` 的 `InvalidModelContextError` / `StreamIdleError` 已经是这个形状；
//     `SubprocessError` / `JobError` / `TerminalBackendCleanupError` 落地时同形即可自动生效。
//  2. `AttachmentError() (code, status)`：`dsh-attachment` 的 `AttachmentError` 在本仓
//     已经有实现（`cmd/dshd/attachments.go` 的 `attachmentStoreError`），码由错误对象自己给。
//  3. 都没有 → `ok=false`。调用方**保持旧形状**（`TurnEndData` / `ToolResultEventWithView`），
//     宁可少一个 code 字段，也不编一个 `UNKNOWN` 冒充真码。
//
// `Name` 取 Go 具体类型名（`AttachmentError` / `subprocessError` …），与官方
// `this.name = new.target.name` 的语义一致；取不到类型名时退回 `"Error"`。

import (
	"context"
	"errors"
	"reflect"

	"dshgo/internal/agent"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// codedError 是官方 `HarnessError.code` 的 Go 等价接口。
type codedError interface {
	Code() string
}

// attachmentCodedError 是 `dsh-attachment` 的 AttachmentError 在本仓的既有形状
// （`cmd/dshd/attachments.go`、`internal/transport/server.go` 都按这个接口取码）。
type attachmentCodedError interface {
	AttachmentError() (code string, status int)
}

// errorCodeOf 从真实错误对象取稳定码；取不到返回空串。
func errorCodeOf(err error) string {
	if err == nil {
		return ""
	}
	if typed, ok := err.(codedError); ok {
		if code := typed.Code(); code != "" {
			return code
		}
	}
	if typed, ok := err.(attachmentCodedError); ok {
		if code, _ := typed.AttachmentError(); code != "" {
			return code
		}
	}
	return ""
}

// errorTypeName 取错误的具体类型名（指针自动解引用），取不到就 "Error"。
func errorTypeName(err error) string {
	typ := reflect.TypeOf(err)
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == nil {
		return "Error"
	}
	if name := typ.Name(); name != "" {
		return name
	}
	return "Error"
}

// turnEndErrorOf 把真实错误对象翻成落盘的 `turn/end.reason.error` 形状。
//
// 第二个返回值是"这个错误对象真的带码吗"：false 时调用方必须走旧构造函数，
// 不能拿一个空 code 甚至 `UNKNOWN` 顶上（那就是这次要修掉的折叠）。
func turnEndErrorOf(err error) (session.TurnEndError, bool) {
	code := errorCodeOf(err)
	if code == "" {
		return session.TurnEndError{}, false
	}
	return session.TurnEndError{
		Name:    errorTypeName(err),
		Code:    code,
		Message: err.Error(),
	}, true
}

// turnEndFailureOfExit 判定"函数出口"这条路径要不要写保留 code 的失败轮。
//
// 只有**真失败**才走 `TurnEndErrorData`：取消/超时是 aborted（v4 用 `reason` 一句原因，
// 不是失败类），无码错误保持既有 `TurnEndData` 形状。判据是 ctx 与 err 本身，
// 不看消息文本。
func turnEndFailureOfExit(ctx context.Context, err error) (session.TurnEndError, bool) {
	if err == nil {
		return session.TurnEndError{}, false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return session.TurnEndError{}, false
	}
	if ctx != nil && ctx.Err() != nil {
		// ctx 自己已经结束：这一轮是"被中止"的那一类，留给 turnEndForExit 判 aborted。
		return session.TurnEndError{}, false
	}
	return turnEndErrorOf(err)
}

// settleTurnEndPayload 给"子进程没能自己收口"的那一轮挑载荷。
//
// 与 `turnEndForSettle` 的 kind/detail 判定同源，只是**能取到真实码时**用
// `TurnEndErrorData`：`detail` 里那句人读的解读（"子进程退出但这一轮没有 turn/end：
// …"）保留在 `message`，code 来自错误对象本身。
func settleTurnEndPayload(ctx context.Context, cause error, turn int) map[string]any {
	kind, detail := turnEndForSettle(ctx, cause)
	if cause != nil {
		if failure, ok := turnEndErrorOf(cause); ok {
			failure.Message = detail
			return session.TurnEndErrorData(turn, failure)
		}
	}
	return session.TurnEndData(turn, kind, detail)
}

// toolFailureOf 取一条失败工具结果的官方错误身份。
//
// 两个来源，都来自**真实对象**，都不查表：
//  1. `spi.Result.Info`（工具自己给的 `{name, code}`，即官方 `error.info` 的 Go 等价物；
//     `internal/approval/denial.go` 的 `ApprovalError`、`internal/agent/parallel.go` 的
//     `AbortError`、`modules/tool/schedule` 等已经这样产出。此前这些身份在落盘时
//     **整条丢掉**，客户端只能看到"工具失败了"）；
//  2. 结果错误对象自己的码（`ATTACHMENT_*` / `SUBPROCESS_*` / `JOB_*` / `CLEANUP_FAILED`）。
func toolFailureOf(e agent.Event) (session.TurnEndError, bool) {
	if e.Result != nil && e.Result.Info != nil {
		info := e.Result.Info
		if info.Code != "" {
			failure := session.TurnEndError{
				Name: info.Name,
				Code: info.Code,
			}
			switch {
			case e.Err != nil:
				failure.Message = e.Err.Error()
			case e.Result.Hint != "":
				failure.Message = e.Result.Hint
			}
			return failure, true
		}
	}
	return turnEndErrorOf(e.Err)
}

// toolResultEventOf 构造一条工具结果事件。
//
// 失败的且能取到官方错误身份时用 `session.ToolResultEventWithError`
// （`message.isError=true` 与 `error:{name,code}` 必须同时出现，官方客户端按这条判合法性）；
// 其余情况保持既有 `ToolResultEventWithView` 形状。
//
// `view` 在两条路上都必须保留：它是工具呈现层的数据，丢 view 等于让界面少画东西
// （`ToolResultEventWithError` 的签名不含 view，所以这里补回同一把键）。
func toolResultEventOf(e agent.Event, blocks []spi.Block, cwd string, sourceSeqs []int64) map[string]any {
	view := viewOf(e.Result)
	if e.IsError {
		if failure, ok := toolFailureOf(e); ok {
			event := session.ToolResultEventWithError(
				e.Turn, e.Step, e.CallID, blocks, cwd, sourceSeqs, failure)
			if len(view) > 0 {
				event["view"] = view
			}
			return event
		}
	}
	return session.ToolResultEventWithView(
		e.Turn, e.Step, e.CallID, blocks, e.IsError, cwd, sourceSeqs, view)
}
