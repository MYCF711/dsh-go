package session

import (
	"encoding/json"
	"testing"

	"dshgo/internal/spi"
)

// 官方 rc.2 `attachment-error-codes/guide.md:12-16`：
// `AttachmentError` 现在继承 `HarnessError`，所以失败轮次的 `turn/end` 记的是
// 真实 code（`ATTACHMENT_NOT_FOUND`/`ATTACHMENT_CORRUPT`/`ATTACHMENT_READ_FAILED`/`INVALID_IMAGE`/`IMAGE_TOO_LARGE` …），
// 不再折成 `UNKNOWN`；工具里逃出的 `AttachmentError` 也要在 `tool/result` 上落
// `error: { name: 'AttachmentError', code }`。
//
// 同类新增码：`CLEANUP_FAILED`（`dsh-terminal/lib/index.js:36`）、
// `SUBPROCESS_*`（`dsh-subprocess-local/lib/index.js:703,816,1634`）、
// `JOB_*` 七个（`dsh-jobs/lib/types/error.js:71-78`）。
func TestErrorCodeCatalogCoverage(t *testing.T) {
	must := map[string][]string{
		"attachment": AttachmentErrorCodes,
		"image":      ImageAdmissionErrorCodes,
		"subprocess": SubprocessErrorCodes,
		"job":        JobErrorCodes,
	}
	for family, codes := range must {
		if len(codes) == 0 {
			t.Fatalf("%s 码表不能为空", family)
		}
		for _, code := range codes {
			if !IsKnownErrorCode(code) {
				t.Fatalf("%s 的 %s 必须在已知码表里", family, code)
			}
		}
	}
	for _, code := range []string{
		"ATTACHMENT_NOT_FOUND", "ATTACHMENT_CORRUPT", "ATTACHMENT_READ_FAILED",
		"INVALID_IMAGE", "IMAGE_TOO_LARGE", "TOO_MANY_IMAGES",
		"CLEANUP_FAILED",
		"SUBPROCESS_RUNNER_FAILED", "SUBPROCESS_RUNNER_IPC_FAILED", "SUBPROCESS_INVALID_ARGV", "SUBPROCESS_PTY_UNAVAILABLE",
		"JOB_NOT_FOUND", "JOB_LIMIT_REACHED", "JOB_BACKGROUND_DISABLED", "JOB_CONTROLLER_UNAVAILABLE",
		"JOB_FOREIGN_SESSION", "JOB_OWNER_NOT_LIVE", "JOB_OWNER_REGISTRY_UNAVAILABLE",
	} {
		if !IsKnownErrorCode(code) {
			t.Fatalf("%s 必须落在已知码表（rc.2 新增码面）", code)
		}
	}
	if len(JobErrorCodes) != 7 {
		t.Fatalf("官方 JobError 恰好 7 个码，得到 %d：%v", len(JobErrorCodes), JobErrorCodes)
	}
	if IsKnownErrorCode("TOTALLY_MADE_UP") || IsKnownErrorCode("") {
		t.Fatalf("码表不该接受未知码")
	}
}

// `turn/end.reason.error` 必须带真实 `{name, code}`，否则官方客户端只能看到 UNKNOWN。
// 宿主旧写法（`TurnEndData(kind="error")`）只写 `{name:"Error", message}`，把 code 丢了。
func TestTurnEndErrorKeepsCode(t *testing.T) {
	for _, code := range []string{
		"ATTACHMENT_NOT_FOUND", "ATTACHMENT_READ_FAILED", "INVALID_IMAGE", "IMAGE_TOO_LARGE",
		"CLEANUP_FAILED", "SUBPROCESS_RUNNER_FAILED", "JOB_NOT_FOUND",
	} {
		data := TurnEndErrorData(2, TurnEndError{
			Name:    "AttachmentError",
			Code:    code,
			Message: "读不到附件",
		})
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		var decoded struct {
			Turn   int `json:"turn"`
			Reason struct {
				Kind  string `json:"kind"`
				Error struct {
					Name    string `json:"name"`
					Code    string `json:"code"`
					Message string `json:"message"`
					Reason  string `json:"reason"`
				} `json:"error"`
			} `json:"reason"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("反序列化失败: %v", err)
		}
		if decoded.Turn != 2 || decoded.Reason.Kind != "error" {
			t.Fatalf("turn/end 形状不对：%s", raw)
		}
		if decoded.Reason.Error.Code != code {
			t.Fatalf("error.code = %q，want %q（不得折叠）", decoded.Reason.Error.Code, code)
		}
		if decoded.Reason.Error.Code == "UNKNOWN" {
			t.Fatalf("不得折成 UNKNOWN")
		}
		if decoded.Reason.Error.Name != "AttachmentError" || decoded.Reason.Error.Message != "读不到附件" {
			t.Fatalf("name/message 必须原样保留：%s", raw)
		}
	}
	// 旧构造器的错误分支仍然可用（不破坏存量调用点），但它不含 code——
	// 生产写入点必须改用 TurnEndErrorData（见报告"需要的接线行"）。
	legacy := TurnEndData(2, "error", "老原因")
	legacyReason := legacy["reason"].(map[string]any)
	if _, has := legacyReason["error"].(map[string]any)["code"]; has {
		t.Fatalf("旧构造器不该凭空多出 code 字段（它没有 code 入参）")
	}
}

// `tool/result` 的失败必须同时满足官方两条：
//   - `message.isError === true`；
//   - `error: { name, code }` 与 message 并列（`dsh-api-session-controller` 客户端
//     `validateSessionEventData` 的 "error requires message.isError === true"）。
func TestToolResultErrorKeepsCode(t *testing.T) {
	data := ToolResultEventWithError(1, 2, "call-1",
		[]spi.Block{{Type: "text", Text: "读图失败"}}, "/work", []int64{4},
		TurnEndError{Name: "AttachmentError", Code: "ATTACHMENT_CORRUPT", Message: "附件损坏"})
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var decoded struct {
		Turn    int `json:"turn"`
		Message struct {
			IsError bool   `json:"isError"`
			Role    string `json:"role"`
		} `json:"message"`
		Error *struct {
			Name   string `json:"name"`
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if !decoded.Message.IsError || decoded.Message.Role != "tool" {
		t.Fatalf("失败工具结果必须 isError=true/role=tool：%s", raw)
	}
	if decoded.Error == nil || decoded.Error.Code != "ATTACHMENT_CORRUPT" || decoded.Error.Name != "AttachmentError" {
		t.Fatalf("tool/result.error 必须带真实码：%s", raw)
	}
	// 成功结果不带 error 字段（官方 `error?` 可选，成功时缺席）。
	ok := ToolResultEvent(1, 2, "call-2", []spi.Block{{Type: "text", Text: "ok"}}, false, "/work", nil)
	if _, has := ok["error"]; has {
		t.Fatalf("成功结果不该带 error：%v", ok)
	}
}
