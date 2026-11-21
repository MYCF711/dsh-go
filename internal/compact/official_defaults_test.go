package compact

// 本文件钉住「压缩保留预算」与「摘要输入不截断」这两件事的**官方值**。
//
// 两处历史上都是宿主编的数，官方没有：
//   - `Options.KeepTokens` 兜底 `= 2000`（官方是 `retainTokens = floor((W−O)×0.16)`）
//   - `maxPromptChars = 24000` / `maxMessageChars = 1200`（官方摘要输入不截断）
//
// 每个断言都写成「改回旧值必红」的形状。

import (
	"context"
	"math"
	"strings"
	"testing"

	"dshgo/internal/session"
)

// 官方 retainRatio = 0.16（`dsh-compaction-basic/lib/index.js:17`），
// 公式 `floor(messageBudgetTokens × retainRatio)`（同文件 :133）。
func TestRetainTokensFollowsOfficialRatio(t *testing.T) {
	b := PressureBudget{ContextWindow: 1_000_000, ReservedCompletionTokens: 128_000}
	got, err := b.RetainTokens()
	if err != nil {
		t.Fatalf("RetainTokens: %v", err)
	}
	// (1000000−128000) × 0.16 = 139520；旧的自造值 2000 与它差 69.76 倍。
	const want = 139_520
	if got != want {
		t.Fatalf("retainTokens = %d，期望 %d（官方 floor((W−O)×0.16)）", got, want)
	}
	if DefaultRetainRatio != 0.16 {
		t.Fatalf("DefaultRetainRatio = %v，官方 0.16", DefaultRetainRatio)
	}
	// 显式比例覆盖（官方 modelPolicies[].retainRatio 的形状）。
	b.RetainRatio = 0.05
	got, err = b.RetainTokens()
	if err != nil {
		t.Fatalf("RetainTokens(0.05): %v", err)
	}
	if got != int(math.Floor(872_000*0.05)) {
		t.Fatalf("retainRatio 覆盖后 = %d，期望 %d", got, int(math.Floor(872_000*0.05)))
	}
}

// 窗口未知时 fail-loud，不退回任何绝对值——"查不到就查不到"。
func TestRetainTokensFailsLoudWithoutWindow(t *testing.T) {
	if got, err := (PressureBudget{}).RetainTokens(); err == nil {
		t.Fatalf("窗口未知却算出了 retainTokens = %d，应当报错而不是编一个数", got)
	}
}

// 官方 `:134`：retainTokens 必须小于 thresholdTokens，否则永远压不下去。
func TestRetainTokensRejectsRatioNotBelowThreshold(t *testing.T) {
	b := PressureBudget{ContextWindow: 1_000_000, ReservedCompletionTokens: 0, RetainRatio: 0.9}
	if got, err := b.RetainTokens(); err == nil {
		t.Fatalf("retainRatio 0.9 ≥ thresholdRatio 0.8 却算出 %d，应当报错", got)
	}
}

// `KeepTokens <= 0` 必须是「只按条数留」，**不能**被兜底成 2000。
func TestKeepTokensZeroMeansCountOnly(t *testing.T) {
	if got := (Options{}).withDefaults().KeepTokens; got != 0 {
		t.Fatalf("Options{}.withDefaults().KeepTokens = %d，期望 0（已删掉自造的 2000 兜底）", got)
	}
	s := surfaceOf(10)
	// 不传 KeepTokens：保留 4 条，压掉前 6 条。
	span, n, ok := Plan(s, Options{KeepRecent: 4})
	if !ok {
		t.Fatal("只按条数留也应该能压")
	}
	if n != 6 || span.Start != 0 || span.End != 5 {
		t.Fatalf("只按条数留：压缩 %d 条，区间 [%d,%d]，期望 6 条 [0,5]", n, span.Start, span.End)
	}
}

// 官方摘要输入不截断：一条 5000 字的消息必须**整条**进摘要请求。
// 旧值 maxMessageChars=1200 会把后面 3800 字砍掉并加上「本条已截断」。
func TestSummarizeDoesNotTruncateLongMessage(t *testing.T) {
	long := strings.Repeat("长", 5000)
	s := []session.SurfaceMsg{msg(0, "user/message", "user", long)}
	chat := &fakeChat{reply: "ok"}
	if _, err := Summarize(context.Background(), chat, "m", s); err != nil {
		t.Fatal(err)
	}
	prompt := chat.got.Messages[1].Content
	if !strings.Contains(prompt, long) {
		t.Fatalf("5000 字的消息没有整条进摘要请求（prompt 长度 %d）：摘要输入被截断了", len([]rune(prompt)))
	}
	if strings.Contains(prompt, "本条已截断") {
		t.Fatal("摘要输入仍在按旧值 1200 字截断")
	}
}

// 官方摘要输入不设总量上限：60 条 × 800 字必须全部进请求。
// 旧值 maxPromptChars=24000 会在中途 break 并写「后面还有 N 条因篇幅省略」。
func TestSummarizeDoesNotCapTotalPrompt(t *testing.T) {
	items := make([]session.SurfaceMsg, 0, 60)
	for i := 0; i < 60; i++ {
		items = append(items, msg(int64(i), "user/message", "user", strings.Repeat("字", 800)))
	}
	chat := &fakeChat{reply: "ok"}
	if _, err := Summarize(context.Background(), chat, "m", items); err != nil {
		t.Fatal(err)
	}
	prompt := chat.got.Messages[1].Content
	if strings.Contains(prompt, "因篇幅省略") {
		t.Fatalf("摘要输入仍在按旧值 24000 字截断（prompt 长度 %d）", len([]rune(prompt)))
	}
	// 60 条 × 800 字 = 48000 字，全都得在。
	if n := strings.Count(prompt, strings.Repeat("字", 800)); n != 60 {
		t.Fatalf("摘要请求里完整消息 %d 条，期望 60（总量上限 24000 已删）", n)
	}
}
