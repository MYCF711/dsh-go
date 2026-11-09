package main

import (
	"testing"

	"dshgo/internal/session"
)

// GO.6 / T2 的**先红点**（任务书原文）：
//
//	构造 input=100 / cacheRead=900 / output=50 → pressure 必须是 1000（不是 1050）。
//
// 宿主以前 `context.usage.used` 直接取最近 assistant 的 totalTokens（含输出），
// 于是这一条会报 1050；官方 `context.measurement.pressureTokens` 只看 prompt 侧，
// 必须报 1000。`used` 保留（向后兼容），但它的口径在 RESULT.md 里写清是 totalTokens。
func TestContextUsageReportsOfficialPressureProjectedAndBuckets(t *testing.T) {
	home := t.TempDir()
	const sid = "session-context-pressure"
	lg, err := session.CreateSession(home, t.TempDir(), sid, "default")
	if err != nil {
		t.Fatal(err)
	}
	appendW149ContextEvent(t, lg, "system/message", session.SystemMessageData(1, 1, "system text"))
	appendW149ContextEvent(t, lg, "request/context", map[string]any{"model": "model-a", "contextWindow": 10000})
	appendW149ContextEvent(t, lg, "assistant/message", session.AssistantMessageEvent(1, 1,
		[]map[string]any{session.TextBlock("answer")}, "stop",
		map[string]any{"inputTokens": 100, "outputTokens": 50, "totalTokens": 1050, "cacheReadTokens": 900}))
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := newSessionSource(home, "").ContextUsage(sid, "full")
	if err != nil {
		t.Fatal(err)
	}
	if got.PressureTokens != 1000 {
		t.Fatalf("pressureTokens = %d，期望 1000（input 100 + cacheRead 900，输出 50 不算）", got.PressureTokens)
	}
	if got.Used != 1050 {
		t.Fatalf("used = %d，期望 1050（向后兼容 = 最近一次 totalTokens，含输出）", got.Used)
	}
	if got.UsageBuckets == nil {
		t.Fatal("有实测锚点却没有四桶投影")
	}
	want := struct {
		uncached, output, read, write int
	}{100, 50, 900, 0}
	b := got.UsageBuckets
	if b.UncachedInputTokens != want.uncached || b.OutputTokens != want.output ||
		b.CacheReadTokens != want.read || b.CacheWriteTokens != want.write {
		t.Fatalf("四桶 = %+v，期望 %+v", *b, want)
	}
	if got.Max != 10000 {
		t.Fatalf("max = %d，期望 10000（request/context 声明的窗口）", got.Max)
	}
	// 采样在 assistant 事件加入 surface **之前**打点：这条消息自己的正文
	// 应该出现在 projected 的增量里（下一次请求会带上它）。
	if got.ProjectedTokens <= got.PressureTokens {
		t.Fatalf("projected = %d 必须大于 pressure = %d（这一次的回答已经进了 surface）",
			got.ProjectedTokens, got.PressureTokens)
	}
}

// 实测是 0（provider 报了 totalTokens=0）：pressure 就是 0，四桶全 0——
// **不许**把"实测 0"渲染成"不知道"，也不许反过来说成"压力 5"。
func TestContextUsageMeasuredZeroStaysZero(t *testing.T) {
	home := t.TempDir()
	const sid = "session-context-no-usage"
	lg, err := session.CreateSession(home, t.TempDir(), sid, "default")
	if err != nil {
		t.Fatal(err)
	}
	appendW149ContextEvent(t, lg, "assistant/message", session.AssistantMessageEvent(1, 1,
		[]map[string]any{session.TextBlock("answer")}, "stop", map[string]any{"totalTokens": 0}))
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := newSessionSource(home, "").ContextUsage(sid, "full")
	if err != nil {
		t.Fatal(err)
	}
	if got.PressureTokens != 0 {
		t.Fatalf("pressure = %d，期望 0（实测就是 0）", got.PressureTokens)
	}
	if b := got.UsageBuckets; b != nil &&
		(b.UncachedInputTokens != 0 || b.OutputTokens != 0 || b.CacheReadTokens != 0 || b.CacheWriteTokens != 0) {
		t.Fatalf("四桶 = %+v，期望全 0", *b)
	}
}
