package main

// 官方 `llm.resolveModelInfo.contextWindowValidation` 在**命令行声明面**上的落点：
//
//	if (context !== undefined && (!Number.isInteger(context.contextWindow) || context.contextWindow <= 0))
//	    throw new LlmError(..., "INVALID_MODEL_CONTEXT")
//
// 宿主 `--context-window` 的帮助文本是 `0=按模型容量解析`（docs/model-capacity.md §1），
// 所以 0 是"未声明"、负值才是"写错了"。旧实现把负值也当成未声明，于是
// `--context-window -1` 会静默跑在 1M 默认容量上，用户看不到自己写错了
// （docs/parameter-register.md #19）。

import (
	"testing"

	"dshgo/internal/llm"
)

func TestRunRejectsNegativeDeclaredContextWindow(t *testing.T) {
	fake := newSamplingCaptureServer(t)
	const sid = "capacity-negative"
	home, _ := d1Workspace(t, sid)

	err := d1Run(t, home, sid, fake.URL, "--context-window", "-1")
	if err == nil {
		t.Fatal("--context-window -1 被接受：负值声明必须 fail-loud INVALID_MODEL_CONTEXT")
	}
	code, ok := llm.ModelContextErrorCode(err)
	if !ok || code != llm.ErrorCodeInvalidModelContext {
		t.Fatalf("错误 = %v，错误码 = %q/%v，期望 %s", err, code, ok, llm.ErrorCodeInvalidModelContext)
	}
	// fail-closed：参数非法时一个模型请求都不发。
	fake.mu.Lock()
	requests := len(fake.bodies)
	fake.mu.Unlock()
	if requests != 0 {
		t.Fatalf("参数非法时仍然发了 %d 次模型请求（必须 fail-closed）", requests)
	}
}

// TestRunAcceptsUndeclaredContextWindow：`--context-window 0` 是既有的
// "按模型容量解析"，不能被新校验误伤（host_run_options 每次给子进程透传的就是它）。
func TestRunAcceptsUndeclaredContextWindow(t *testing.T) {
	fake := newSamplingCaptureServer(t)
	const sid = "capacity-undeclared"
	home, _ := d1Workspace(t, sid)

	if err := d1Run(t, home, sid, fake.URL, "--context-window", "0"); err != nil {
		t.Fatalf("--context-window 0 被拒（0 = 按模型容量解析）: %v", err)
	}
	fake.mu.Lock()
	requests := len(fake.bodies)
	fake.mu.Unlock()
	if requests == 0 {
		t.Fatal("这一轮没有真的发模型请求，测试没走到链路上")
	}
}
