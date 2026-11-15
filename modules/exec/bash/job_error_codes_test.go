package bash

// 宿主 job 失败路径必须带官方 rc.2 的 `JOB_*` 码（R3-G15），不折叠成 UNKNOWN。
//
// 官方原件：
//
//	@deepseek-ai/dsh-jobs/src/error.ts:16-24       7 码
//	@deepseek-ai/dsh-tool-jobs/src/index.ts:460-461  timeout_ms 正值校验（ToolArgsError）
//	@deepseek-ai/dsh-jobs-local/src/index.ts:121    waitAborted() → name='AbortError'
//
// 这里的每个用例都走**真实的工具执行**（不是构造错误对象），因为要证明的是
// "模型看到的失败带码"，而不是"码表里有个字符串"。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"dshgo/internal/jobs"
	"dshgo/internal/spi"
)

func mustJobCode(t *testing.T, err error, want jobs.Code) *jobs.JobError {
	t.Helper()
	if err == nil {
		t.Fatalf("期望失败，实际成功")
	}
	var je *jobs.JobError
	if !errors.As(err, &je) {
		t.Fatalf("错误类型 = %T，期望 *jobs.JobError（err=%v）", err, err)
	}
	if je.JobCode() != want {
		t.Fatalf("码 = %q，期望 %q（err=%v）", je.JobCode(), want, err)
	}
	return je
}

// TestJobOutputUnknownIDCarriesJobNotFound 钉住"id 不存在" → JOB_NOT_FOUND，
// 且失败结果的 error.info 带 {name:'JobError', code:'JOB_NOT_FOUND'}。
func TestJobOutputUnknownIDCarriesJobNotFound(t *testing.T) {
	svc := NewService(t.TempDir())
	t.Cleanup(svc.Close)
	tool := &jobOutputTool{svc: svc}

	// 注意 id 形状：`job-<别的ownerID>-<n>` 是"属于另一个实例"（JOB_FOREIGN_SESSION，
	// 见 service.go 的 job() 判据）；不是这个形状的未知 id 才是 JOB_NOT_FOUND。
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"job_id":"job-999"}`))
	je := mustJobCode(t, err, jobs.CodeNotFound)
	if !errors.Is(je, spi.ErrJobNotFound) {
		t.Fatalf("映射之后仍要 errors.Is(spi.ErrJobNotFound)")
	}
	if res.Info == nil {
		t.Fatalf("失败结果必须带 error.info")
	}
	if res.Info.Name != jobs.ErrorName {
		t.Fatalf("info.name = %q，期望 %q", res.Info.Name, jobs.ErrorName)
	}
	if res.Info.Code != string(jobs.CodeNotFound) {
		t.Fatalf("info.code = %q，期望 %q", res.Info.Code, jobs.CodeNotFound)
	}
	if strings.TrimSpace(res.Hint) == "" {
		t.Fatalf("失败必须带 Hint（下一步）")
	}
}

// TestJobOutputForeignInstanceCarriesJobForeignSession 钉住"任务属于另一个 exec
// 服务实例" → JOB_FOREIGN_SESSION（官方 JOB_FOREIGN_SESSION 的宿主等价条件）。
func TestJobOutputForeignInstanceCarriesJobForeignSession(t *testing.T) {
	owner := NewService(t.TempDir())
	t.Cleanup(owner.Close)
	other := NewService(t.TempDir())
	t.Cleanup(other.Close)

	started, err := owner.RunBackground("sleep 2")
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	if started.JobID == "" {
		t.Fatalf("没拿到 job id：%+v", started)
	}
	foreignID := started.JobID
	t.Cleanup(func() { _ = owner.Kill(foreignID) })

	tool := &jobOutputTool{svc: other}
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"job_id":"`+foreignID+`"}`))
	je := mustJobCode(t, err, jobs.CodeForeignSession)
	if !errors.Is(je, spi.ErrJobWrongInstance) {
		t.Fatalf("映射之后仍要 errors.Is(spi.ErrJobWrongInstance)")
	}
	if res.Info == nil || res.Info.Code != string(jobs.CodeForeignSession) {
		t.Fatalf("info = %+v，期望 code=%q", res.Info, jobs.CodeForeignSession)
	}
}

// TestBackgroundLimitCarriesJobLimitReached 钉住 `jobs.maxConcurrentJobsPerOwner`
// 超限 → JOB_LIMIT_REACHED（官方这条码的下一步是 job_kill）。
func TestBackgroundLimitCarriesJobLimitReached(t *testing.T) {
	t.Setenv(EnvMaxConcurrentJobsPerOwner, "1")
	t.Setenv(EnvSettledRetainBytes, "0")
	svc := NewService(t.TempDir())
	t.Cleanup(svc.Close)
	tool := &shellTool{svc: svc}

	first, err := tool.Exec(context.Background(), json.RawMessage(
		`{"command":"sleep 2","description":"占额度","run_in_background":true}`))
	if err != nil {
		t.Fatalf("第 1 个后台任务应当成功：%v", err)
	}
	var data shellData
	if err := json.Unmarshal(mustJSON(first.Data), &data); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Kill(data.JobID) })

	res, err := tool.Exec(context.Background(), json.RawMessage(
		`{"command":"sleep 2","description":"超限","run_in_background":true}`))
	mustJobCode(t, err, jobs.CodeLimitReached)
	if res.Info == nil || res.Info.Code != string(jobs.CodeLimitReached) {
		t.Fatalf("info = %+v，期望 code=%q", res.Info, jobs.CodeLimitReached)
	}
	if !strings.Contains(err.Error(), "1") {
		t.Fatalf("错误必须写明上限：%v", err)
	}
}

// TestBackgroundDisabledCarriesJobBackgroundDisabled 钉住部署级开关
// `run_in_background:false`（官方 enableRunInBackground:false）→ JOB_BACKGROUND_DISABLED。
func TestBackgroundDisabledCarriesJobBackgroundDisabled(t *testing.T) {
	t.Setenv(EnvRunInBackground, "0")
	svc := NewService(t.TempDir())
	t.Cleanup(svc.Close)
	tool := &shellTool{svc: svc}

	res, err := tool.Exec(context.Background(), json.RawMessage(
		`{"command":"echo hi","description":"被关掉的后台","run_in_background":true}`))
	mustJobCode(t, err, jobs.CodeBackgroundDisabled)
	if res.Info == nil || res.Info.Code != string(jobs.CodeBackgroundDisabled) {
		t.Fatalf("info = %+v，期望 code=%q", res.Info, jobs.CodeBackgroundDisabled)
	}
	// 被关掉时**不许**留下后台任务（否则模型会去 job_list 找不到它）。
	if got := len(svc.List()); got != 0 {
		t.Fatalf("被拒的后台请求不许留下任务：%d 个", got)
	}
}

// TestJobOutputWaitAbortIsNamedAbortError 钉住"调用方自己取消等待" → 名为
// AbortError 的取消（官方 waitAborted()），不是普通失败、也不是 JOB_* 码。
func TestJobOutputWaitAbortIsNamedAbortError(t *testing.T) {
	svc := NewService(t.TempDir())
	t.Cleanup(svc.Close)
	started, err := svc.RunBackground("sleep 5")
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	longID := started.JobID
	if longID == "" {
		t.Fatalf("没拿到 job id：%+v", started)
	}
	t.Cleanup(func() { _ = svc.Kill(longID) })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	tool := &jobOutputTool{svc: svc}
	res, err := tool.Exec(ctx, json.RawMessage(
		`{"job_id":"`+longID+`","wait":true,"timeout_ms":4000}`))
	if err == nil {
		t.Fatalf("ctx 取消后不该成功返回")
	}
	if !jobs.IsAbort(err) {
		t.Fatalf("错误类型 = %T / %v，期望 jobs 的 AbortError", err, err)
	}
	if res.Info == nil || res.Info.Name != jobs.ErrorNameAbort || res.Info.Code != spi.CodeAborted {
		t.Fatalf("info = %+v，期望 {name:%q, code:%q}", res.Info, jobs.ErrorNameAbort, spi.CodeAborted)
	}
}

// TestJobOutputRejectsNonPositiveTimeout 钉住官方 `timeout_ms` 校验：
// 显式给了 0/负数 → 参数错误（ToolArgsError / INVALID_ARGS），**不是**静默改成默认值。
func TestJobOutputRejectsNonPositiveTimeout(t *testing.T) {
	svc := NewService(t.TempDir())
	t.Cleanup(svc.Close)
	started, err := svc.RunBackground("sleep 1")
	if err != nil {
		t.Fatalf("RunBackground: %v", err)
	}
	timeoutID := started.JobID
	if timeoutID == "" {
		t.Fatalf("没拿到 job id：%+v", started)
	}
	t.Cleanup(func() { _ = svc.Kill(timeoutID) })
	tool := &jobOutputTool{svc: svc}

	for _, body := range []string{
		`{"job_id":"` + timeoutID + `","wait":true,"timeout_ms":0}`,
		`{"job_id":"` + timeoutID + `","timeout_ms":-5}`,
	} {
		res, err := tool.Exec(context.Background(), json.RawMessage(body))
		if err == nil {
			t.Fatalf("%s 必须被拒", body)
		}
		var args *jobs.ToolArgsError
		if !errors.As(err, &args) {
			t.Fatalf("%s 的错误类型 = %T，期望 *jobs.ToolArgsError", body, err)
		}
		if res.Info == nil || res.Info.Code != jobs.CodeInvalidArgs {
			t.Fatalf("%s 的 info = %+v，期望 code=%q", body, res.Info, jobs.CodeInvalidArgs)
		}
		if !strings.Contains(err.Error(), "invalid timeout_ms") {
			t.Fatalf("%s 的消息没照官方口径：%v", body, err)
		}
	}
}
