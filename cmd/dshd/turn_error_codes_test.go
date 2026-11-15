package main

// rc.2 失败码落盘：`turn/end.reason.error.code` 与 `tool/result.error.code`
// **一字不变**地从真实错误对象/工具结果身份落到 v4 日志上，不出现 `UNKNOWN`。
//
// 这些用例故意让四类官方错误（AttachmentError / SubprocessError / JobError /
// TerminalBackendCleanupError）各走一遍**真实写入点**（turnCloser.Exit、
// settleTurnEndPayload、toolResultEventOf），而不是只测取码函数：
// 折叠这类 bug 恰恰发生在"取到码之后又没写进去"的缝里。

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// 四类官方错误的 Go 形状（类名逐字，取码接口各自不同——这正是要覆盖的点）。
type AttachmentError struct{ code string }

func (e *AttachmentError) Error() string { return "附件不可用：" + e.code }
func (e *AttachmentError) AttachmentError() (string, int) {
	return e.code, http.StatusBadRequest
}

type SubprocessError struct{ code string }

func (e *SubprocessError) Error() string { return "子进程失败：" + e.code }
func (e *SubprocessError) Code() string  { return e.code }

type JobError struct{ code string }

func (e *JobError) Error() string { return "作业失败：" + e.code }
func (e *JobError) Code() string  { return e.code }

type TerminalBackendCleanupError struct{}

func (e *TerminalBackendCleanupError) Error() string { return "启动与清理都失败" }
func (e *TerminalBackendCleanupError) Code() string  { return session.CodeCleanupFailed }

// turnEndEvents 读回一份日志里所有 turn/end 的 reason 对象。
func turnEndEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读回日志 %s: %v", path, err)
	}
	out := make([]map[string]any, 0, 1)
	for _, ev := range back.Events {
		if ev.Type != "turn/end" {
			continue
		}
		var payload struct {
			Reason map[string]any `json:"reason"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("解析 turn/end: %v（%s）", err, ev.Data)
		}
		out = append(out, payload.Reason)
	}
	return out
}

// errorObjectOf 从 turn/end 的 reason 里取 error 对象。
func errorObjectOf(t *testing.T, reason map[string]any) map[string]any {
	t.Helper()
	if kind, _ := reason["kind"].(string); kind != "error" {
		t.Fatalf("reason.kind = %v，want error（%v）", reason["kind"], reason)
	}
	obj, ok := reason["error"].(map[string]any)
	if !ok {
		t.Fatalf("reason.error 不是对象：%v", reason)
	}
	return obj
}

// TestTurnEndExitKeepsRealErrorCode 是本任务的主断言：
// 四类错误的码在 turn/end 上原样出现，且**都不是** UNKNOWN。
func TestTurnEndExitKeepsRealErrorCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
		want string
	}{
		{"AttachmentError", &AttachmentError{code: "ATTACHMENT_CORRUPT"}, "ATTACHMENT_CORRUPT", "AttachmentError"},
		{"SubprocessError", &SubprocessError{code: "SUBPROCESS_RUNNER_FAILED"}, "SUBPROCESS_RUNNER_FAILED", "SubprocessError"},
		{"JobError", &JobError{code: "JOB_NOT_FOUND"}, "JOB_NOT_FOUND", "JobError"},
		{"TerminalBackendCleanupError", &TerminalBackendCleanupError{}, session.CodeCleanupFailed, "TerminalBackendCleanupError"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			lg, err := session.CreateSession(home, t.TempDir(), "s-"+c.name, modes.DefaultID)
			if err != nil {
				t.Fatalf("建会话: %v", err)
			}
			t.Cleanup(func() { _ = lg.Close() })
			tc := newTurnCloser(lg, 1)
			if err := tc.Exit(context.Background(), c.err); err != nil {
				t.Fatalf("Exit: %v", err)
			}

			reasons := turnEndEvents(t, lg.Path)
			if len(reasons) != 1 {
				t.Fatalf("turn/end 条数 = %d，want 1", len(reasons))
			}
			obj := errorObjectOf(t, reasons[0])
			if got, _ := obj["code"].(string); got != c.code {
				t.Fatalf("turn/end.error.code = %q，want %q（整条 reason：%v）", got, c.code, reasons[0])
			}
			if got, _ := obj["name"].(string); got != c.want {
				t.Fatalf("turn/end.error.name = %q，want %q", got, c.want)
			}
			if code, _ := obj["code"].(string); code == "UNKNOWN" {
				t.Fatalf("code 被折叠成 UNKNOWN：%v", reasons[0])
			}
		})
	}
}

// TestTurnEndExitWithoutCodeKeepsLegacyShape：认不出码的错误**不编 UNKNOWN**，
// 保持改动前的 {name:"Error", message} 形状（少一个 code，不多一个假码）。
func TestTurnEndExitWithoutCodeKeepsLegacyShape(t *testing.T) {
	home := t.TempDir()
	lg, err := session.CreateSession(home, t.TempDir(), "s-plain", modes.DefaultID)
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	if err := newTurnCloser(lg, 1).Exit(context.Background(), errPlainForTest{}); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	obj := errorObjectOf(t, turnEndEvents(t, lg.Path)[0])
	if _, has := obj["code"]; has {
		t.Fatalf("无码错误不该编一个 code：%v", obj)
	}
	if got, _ := obj["name"].(string); got != "Error" {
		t.Fatalf("name = %q，want Error", got)
	}
}

// TestTurnEndExitCancelledStaysAborted：取消不是失败类，仍写 aborted + reason。
func TestTurnEndExitCancelledStaysAborted(t *testing.T) {
	home := t.TempDir()
	lg, err := session.CreateSession(home, t.TempDir(), "s-cancel", modes.DefaultID)
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newTurnCloser(lg, 1).Exit(ctx, context.Canceled); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	reason := turnEndEvents(t, lg.Path)[0]
	if kind, _ := reason["kind"].(string); kind != "aborted" {
		t.Fatalf("reason.kind = %v，want aborted（%v）", reason["kind"], reason)
	}
	if _, has := reason["error"]; has {
		t.Fatalf("aborted 不该带 error 对象：%v", reason)
	}
}

// TestSettleTurnEndKeepsErrorCode：子进程没写结局时，宿主补的 turn/end 也必须保留码，
// 并把原来那句人读的解读留在 message 里。
func TestSettleTurnEndKeepsErrorCode(t *testing.T) {
	cause := &TerminalBackendCleanupError{}
	data := settleTurnEndPayload(context.Background(), cause, 3)
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	var payload struct {
		Turn   int            `json:"turn"`
		Reason map[string]any `json:"reason"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if payload.Turn != 3 {
		t.Fatalf("turn = %d，want 3", payload.Turn)
	}
	obj := errorObjectOf(t, payload.Reason)
	if code, _ := obj["code"].(string); code != session.CodeCleanupFailed {
		t.Fatalf("code = %q，want %q", code, session.CodeCleanupFailed)
	}
	msg, _ := obj["message"].(string)
	if msg == "" || msg == cause.Error() {
		t.Fatalf("message 应保留原来的解读前缀，得到 %q", msg)
	}
}

// TestToolResultKeepsErrorCode 覆盖 tool/result 的两条真实来源：
// 工具自己给的 Result.Info（官方 error.info）与错误对象自己的码。
func TestToolResultKeepsErrorCode(t *testing.T) {
	view := spi.ViewSpec{Kind: "text"}
	cases := []struct {
		name  string
		event agent.Event
		code  string
		cls   string
	}{
		{
			name: "Result.Info 的作业码",
			event: agent.Event{Turn: 2, Step: 1, CallID: "c-info", IsError: true,
				Result: &spi.Result{Hint: "换个作业 id", View: &view,
					Info: &spi.ErrorInfo{Name: "JobError", Code: "JOB_LIMIT_REACHED"}}},
			code: "JOB_LIMIT_REACHED", cls: "JobError",
		},
		{
			name: "错误对象的附件码",
			event: agent.Event{Turn: 2, Step: 2, CallID: "c-err", IsError: true,
				Err:    &AttachmentError{code: "ATTACHMENT_READ_FAILED"},
				Result: &spi.Result{Summary: "读不出来", View: &view}},
			code: "ATTACHMENT_READ_FAILED", cls: "AttachmentError",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blocks := []spi.Block{{Type: "text", Text: "失败"}}
			event := toolResultEventOf(c.event, blocks, "/tmp/wd", []int64{7})
			raw, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("序列化: %v", err)
			}
			var payload struct {
				Message map[string]any  `json:"message"`
				Error   map[string]any  `json:"error"`
				View    json.RawMessage `json:"view"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("解析: %v", err)
			}
			if isErr, _ := payload.Message["isError"].(bool); !isErr {
				t.Fatalf("message.isError 必须是 true（官方错误对象的伴随要求）：%s", raw)
			}
			if got, _ := payload.Error["code"].(string); got != c.code {
				t.Fatalf("tool/result.error.code = %q，want %q（%s）", got, c.code, raw)
			}
			if got, _ := payload.Error["name"].(string); got != c.cls {
				t.Fatalf("tool/result.error.name = %q，want %q", got, c.cls)
			}
			if code, _ := payload.Error["code"].(string); code == "UNKNOWN" {
				t.Fatalf("code 被折叠成 UNKNOWN：%s", raw)
			}
			if len(payload.View) == 0 {
				t.Fatalf("view 被丢掉了（工具呈现层会少画东西）：%s", raw)
			}
		})
	}
}

// TestToolResultWithoutIdentityKeepsLegacyShape：取不到身份时保持既有形状
// （没有 error 键），不编 name/code。
func TestToolResultWithoutIdentityKeepsLegacyShape(t *testing.T) {
	event := toolResultEventOf(agent.Event{Turn: 1, Step: 1, CallID: "c", IsError: true,
		Result: &spi.Result{Summary: "工具自己的一句话失败"}}, []spi.Block{{Type: "text", Text: "x"}}, "", nil)
	if _, has := event["error"]; has {
		t.Fatalf("没有错误身份时不该写 error 对象：%v", event)
	}
	message, _ := event["message"].(map[string]any)
	if isErr, _ := message["isError"].(bool); !isErr {
		t.Fatalf("isError 仍要如实写 true：%v", event)
	}
}

// errPlainForTest 是"认不出码"的普通错误（模拟 exec.ExitError 一类）。
type errPlainForTest struct{}

func (errPlainForTest) Error() string { return "子进程退出码 1" }
