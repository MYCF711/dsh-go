package session

import (
	"dshgo/internal/spi"
)

// 失败码面（官方 rc.2 把一批包错误统一到带 `code` 的 `HarnessError`
// ——`dsh-error/lib/index.js:13-21`——于是这些码会原样落进 `turn/end.reason.error.code`
// 与 `tool/result.error.code`，**不再折成 `UNKNOWN`**）。
//
// 为什么宿主必须照抄这张表：官方客户端按 `code` 分支（重试/UI 提示/附件重选），
// 码一旦被折叠或丢掉，客户端只能看到"未知失败"。本文件只做两件事：
//  1. 把官方码表登记成可断言的 Go 值（防"某天有人加了个 default 折叠"）；
//  2. 提供**保留 code** 的 turn/end 与 tool/result 构造器。
//
// 码表来源逐条：
//   - 附件/图片准入：`dsh-attachment/lib/types/error.js:3-24`（IMAGE_ADMISSION_ERROR_CODES + ATTACHMENT_ERROR_CODES）
//   - 终端清理：`dsh-terminal/lib/index.js:36`（CLEANUP_FAILED）
//   - 子进程：`dsh-subprocess-local/lib/index.js:703,816,1634,1449`
//   - 作业：`dsh-jobs/lib/types/error.js:71-78`（7 个 JOB_* 码）
const (
	// CodeCleanupFailed 表示"启动与清理都失败，后端资源可能还活着"。
	CodeCleanupFailed = "CLEANUP_FAILED"
)

// ImageAdmissionErrorCodes 是图片准入失败码（官方 IMAGE_ADMISSION_ERROR_CODES，逐字）。
var ImageAdmissionErrorCodes = []string{
	"TOO_MANY_IMAGES",
	"IMAGES_TOO_LARGE",
	"UNSUPPORTED_IMAGE_TYPE",
	"INVALID_IMAGE_BASE64",
	"INVALID_IMAGE",
	"IMAGE_TYPE_MISMATCH",
	"IMAGE_TOO_LARGE",
	"IMAGE_TOO_MANY_PIXELS",
	"IMAGE_DIMENSION_TOO_LARGE",
}

// AttachmentErrorCodes 是 `AttachmentError` 的全部码（官方 ATTACHMENT_ERROR_CODES =
// 图片准入码 + 文件/存储码，逐字）。注意它们**不共享前缀**——所以不能靠前缀判类。
var AttachmentErrorCodes = buildAttachmentErrorCodes()

// SubprocessErrorCodes 是 `SubprocessError` 在 rc.2 用到的码（逐字）。
var SubprocessErrorCodes = []string{
	"SUBPROCESS_RUNNER_FAILED",
	"SUBPROCESS_RUNNER_IPC_FAILED",
	"SUBPROCESS_INVALID_ARGV",
	"SUBPROCESS_PTY_UNAVAILABLE",
}

// JobErrorCodes 是 `JobError` 的 7 个码（`dsh-jobs/lib/types/error.js:71-78`，逐字）。
var JobErrorCodes = []string{
	"JOB_BACKGROUND_DISABLED",
	"JOB_CONTROLLER_UNAVAILABLE",
	"JOB_NOT_FOUND",
	"JOB_FOREIGN_SESSION",
	"JOB_OWNER_NOT_LIVE",
	"JOB_OWNER_REGISTRY_UNAVAILABLE",
	"JOB_LIMIT_REACHED",
}

// knownErrorCodes 是所有登记过的失败码（含上面四族 + CLEANUP_FAILED）。
var knownErrorCodes = buildKnownErrorCodes()

func buildAttachmentErrorCodes() []string {
	out := make([]string, 0, len(ImageAdmissionErrorCodes)+8)
	out = append(out, ImageAdmissionErrorCodes...)
	return append(out,
		"INVALID_FILE_BASE64",
		"INVALID_ATTACHMENT_REF",
		"ATTACHMENT_CORRUPT",
		"ATTACHMENT_WRITE_FAILED",
		"ATTACHMENT_NOT_FOUND",
		"ATTACHMENT_READ_FAILED",
		"ATTACHMENT_PROJECTION_UNSUPPORTED",
		"ATTACHMENT_FILES_UNSUPPORTED",
	)
}

func buildKnownErrorCodes() map[string]struct{} {
	families := [][]string{AttachmentErrorCodes, SubprocessErrorCodes, JobErrorCodes, {CodeCleanupFailed}}
	out := make(map[string]struct{}, 32)
	for _, family := range families {
		for _, code := range family {
			out[code] = struct{}{}
		}
	}
	return out
}

// IsKnownErrorCode 判断 code 是否在官方 rc.2 的失败码面里。
//
// 它**不是**一道"只许这些码通过"的闸门：宿主自己的结构化码（例如
// `workspace-file/not-found`）照样可以透传。它的用途是让"新码面是否被折叠"可断言。
func IsKnownErrorCode(code string) bool {
	_, ok := knownErrorCodes[code]
	return ok
}

// TurnEndError 是一次失败轮的官方错误形状
// （`SessionEventMap['turn/end'].reason.error` = `{name, code, reason?, message?}`；
// 声明见 `dsh-session-reference/lib/typert.host.js:573`，rc.2 起 code 是真实码而不是 UNKNOWN）。
type TurnEndError struct {
	// Name 是错误类名：`AttachmentError` / `SubprocessError` / `Error` …
	Name string
	// Code 是稳定机器码（官方 "route on this, never by parsing message"）。
	Code string
	// Message 是给人看的原因，不含原始字节或宿主路径。
	Message string
	// Reason 是官方 `reason?` 的附加分类（如 `MODEL_DOES_NOT_SUPPORT_IMAGES`）。
	Reason string
}

// errorPayload 是官方 `{name, code, reason?, message?}` 的落盘形状。
func (e TurnEndError) errorPayload() map[string]any {
	name := e.Name
	if name == "" {
		name = "Error"
	}
	payload := map[string]any{"name": name, "code": e.Code}
	if e.Message != "" {
		payload["message"] = e.Message
	}
	if e.Reason != "" {
		payload["reason"] = e.Reason
	}
	return payload
}

// TurnEndErrorData 构造**保留 code** 的失败 `turn/end` 载荷。
//
// 与 `TurnEndData(turn, "error", detail)` 的区别：那条老构造函数没有 code 入参，
// 落盘只有 `{name:"Error", message}`，官方客户端读不出失败类。
// 生产写入点应改用本函数（接线行见报告 `B3-session-events.md`）。
func TurnEndErrorData(turn int, failure TurnEndError) map[string]any {
	return map[string]any{
		"turn": turn,
		"reason": map[string]any{
			"kind":  "error",
			"error": failure.errorPayload(),
		},
	}
}

// ToolResultEventWithError 构造失败的工具结果，并带上官方要求的
// `error: {name, code, reason?}`（与 `message.isError = true` 并存）。
//
// 官方规则（`dsh-api-session-controller` 客户端的 `validateSessionEventData`）：
// "error requires message.isError === true"——两个字段必须同时出现，只写一个会被判非法。
func ToolResultEventWithError(turn, step int, callID string, blocks []spi.Block, cwd string, sourceSeqs []int64, failure TurnEndError) map[string]any {
	event := ToolResultEvent(turn, step, callID, blocks, true, cwd, sourceSeqs)
	event["error"] = failure.errorPayload()
	return event
}
