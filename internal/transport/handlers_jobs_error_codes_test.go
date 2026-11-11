package transport

// Job 失败码在两条 wire 面上的投影（R3-G15 的"不折叠成 UNKNOWN"）。
//
// 官方 `JobError` 的 7 个码是**分类**，不是状态码；本仓的 four-field 错误契约与
// 官方 typert 流故障各有自己的码表。这个文件钉住"每个 JOB_* 码落到哪一档"：
// 任何码都不许掉进 `internal` / `gateway/internal`（那就是折叠成 UNKNOWN）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"dshgo/internal/jobs"
)

// TestWriteTaskActionErrorMapsJobCodes 钉住 unary/任务动作面（jobs.list|output|stop
// 共用的四字段出口）对 7 个码的状态码与四字段 code。
func TestWriteTaskActionErrorMapsJobCodes(t *testing.T) {
	want := map[jobs.Code]struct {
		status int
		code   string
	}{
		jobs.CodeNotFound:                 {http.StatusNotFound, "not_found"},
		jobs.CodeForeignSession:           {http.StatusForbidden, "cross_session"},
		jobs.CodeLimitReached:             {http.StatusTooManyRequests, "job_limit_reached"},
		jobs.CodeBackgroundDisabled:       {http.StatusNotImplemented, "job_background_disabled"},
		jobs.CodeControllerUnavailable:    {http.StatusNotImplemented, "job_controller_unavailable"},
		jobs.CodeOwnerNotLive:             {http.StatusConflict, "job_owner_not_live"},
		jobs.CodeOwnerRegistryUnavailable: {http.StatusNotImplemented, "job_owner_registry_unavailable"},
	}
	for _, code := range jobs.Codes() {
		expect, ok := want[code]
		if !ok {
			t.Fatalf("码 %q 没有投影规则（7 个码必须逐个有档）", code)
		}
		rec := httptest.NewRecorder()
		writeTaskActionError(rec, jobs.New(code, "任务操作失败："+string(code), nil))
		if rec.Code != expect.status {
			t.Fatalf("%s 的状态码 = %d，期望 %d（body=%s）", code, rec.Code, expect.status, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 的响应不是 JSON：%q", code, rec.Body.String())
		}
		assertFourFieldError(t, rec.Code, out, expect.status, expect.code)
		if expect.code == "internal" {
			t.Fatalf("%s 被折叠成 internal（等于 UNKNOWN）", code)
		}
	}
}

// TestOfficialJobTerminalFaultMapsJobCodes 钉住官方 job.* 流面（gateway/* 词表）。
func TestOfficialJobTerminalFaultMapsJobCodes(t *testing.T) {
	want := map[jobs.Code]struct {
		status int
		code   string
	}{
		jobs.CodeNotFound:                 {http.StatusNotFound, "gateway/session-not-found"},
		jobs.CodeForeignSession:           {http.StatusForbidden, "gateway/forbidden"},
		jobs.CodeLimitReached:             {http.StatusTooManyRequests, "gateway/service-unavailable"},
		jobs.CodeBackgroundDisabled:       {http.StatusNotImplemented, "gateway/method-unavailable"},
		jobs.CodeControllerUnavailable:    {http.StatusNotImplemented, "gateway/invocation-unavailable"},
		jobs.CodeOwnerNotLive:             {http.StatusConflict, "gateway/context-unavailable"},
		jobs.CodeOwnerRegistryUnavailable: {http.StatusNotImplemented, "gateway/context-unavailable"},
	}
	for _, code := range jobs.Codes() {
		expect, ok := want[code]
		if !ok {
			t.Fatalf("码 %q 没有流面投影规则", code)
		}
		fault := officialJobTerminalFault(MethodJobList, jobs.New(code, "流式任务失败", nil))
		if fault == nil {
			t.Fatalf("%s 的流故障是 nil", code)
		}
		if fault.Status != expect.status {
			t.Fatalf("%s 的状态码 = %d，期望 %d（fault=%+v）", code, fault.Status, expect.status, fault)
		}
		if fault.Code != expect.code {
			t.Fatalf("%s 的 gateway 码 = %q，期望 %q", code, fault.Code, expect.code)
		}
		if fault.Code == "gateway/internal" {
			t.Fatalf("%s 被折叠成 gateway/internal（等于 UNKNOWN）", code)
		}
	}
}

// TestOfficialJobTerminalFaultCancelIsAbort 钉住"调用方自己取消等待"：
// 官方命名 AbortError，gateway 词表里对应 gateway/cancelled（不是 internal）。
func TestOfficialJobTerminalFaultCancelIsAbort(t *testing.T) {
	fault := officialJobTerminalFault(MethodJobFollow, jobs.WaitAborted())
	if fault == nil {
		t.Fatal("取消的流故障是 nil")
	}
	if fault.Code != "gateway/cancelled" {
		t.Fatalf("取消的 gateway 码 = %q，期望 gateway/cancelled", fault.Code)
	}
	if fault.Status != http.StatusRequestTimeout {
		t.Fatalf("取消的状态码 = %d，期望 408", fault.Status)
	}
}
