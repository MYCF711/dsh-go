package main

// `acpRunner.keepRecent` 曾经是死字段：构造时赋 8，但从没有人读它；
// 真正生效的是 autoCompactNow 里写死的 `KeepRecent: 2`。
//
// 处理方式：**接线**（不是删字段）——把使用点改成读字段，并把默认值定成
// 改动前实际生效的 2（默认行为一字不变）。这样这个旋钮要么是 2（默认），
// 要么是被显式改过的值，测试也能直接看见它。
//
// 为什么不选"删字段"：压缩保留条数是**真实策略**（决定摘要粗不粗、模型眼前
// 还剩多少上下文），只是暂时没有 CLI 入口；删掉字段等于把策略彻底钉死，
// 而没有使用点的字段又只是一个假旋钮。接线的成本是一行，收益是它从此
// 只有一个来源。

import (
	"testing"

	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

func TestACPCompactSelectOptionsFollowKeepRecent(t *testing.T) {
	r := newACPRunner(&fakeReg{tools: map[string]spi.Tool{}}, &recorder{}, "http://127.0.0.1:1/v1", "m", t.TempDir(), 2)
	t.Cleanup(func() { _ = r.Shutdown() })

	// 默认值必须与改动前**实际生效**的一致（2），不能把这个隐含行为改掉。
	if defaultACPKeepRecent != 2 {
		t.Fatalf("defaultACPKeepRecent = %d，期望 2（与改动前写死的值一致，避免行为漂移）", defaultACPKeepRecent)
	}
	if got := r.compactSelectOptions().KeepRecent; got != 2 {
		t.Fatalf("默认 KeepRecent = %d，期望 2", got)
	}
	// 改字段 → 使用点跟着变，证明它不是死字段。
	r.keepRecent = 5
	if got := r.compactSelectOptions().KeepRecent; got != 5 {
		t.Fatalf("改 keepRecent 后使用点仍读到 %d，期望 5：字段没有接到使用点", got)
	}
	// KeepTokens 不再是我方自造的 2000，而是官方
	// `retainTokens = floor((W − O) × retainRatio)`
	//（`dsh-compaction-basic/lib/index.js:17` 的 `DEFAULT_RETAIN_RATIO = .16`、
	// `:133` 的公式）。窗口写死成 1M / maxOutput 128k，算出来是 139520——
	// 与旧值 2000 差 69.76 倍，把 2000 改回去这条会立刻红。
	r.ctxWindow = 1_000_000
	r.capacity = llm.ModelCapacity{ContextWindow: 1_000_000, MaxOutput: 128_000}
	opts := r.compactSelectOptions()
	const wantKeepTokens = 139520 // floor((1000000−128000) × 0.16)
	if opts.KeepTokens != wantKeepTokens {
		t.Fatalf("KeepTokens = %d，期望 %d（官方 retainRatio 0.16 × 消息预算 872000）",
			opts.KeepTokens, wantKeepTokens)
	}
	if opts.MinToCompact != 2 {
		t.Fatalf("MinToCompact = %d，期望 2", opts.MinToCompact)
	}
	// 窗口未知 → 算不出官方预算，退回「只按条数留」（官方手动压缩的形状：
	// `selectCompactableRange(..., 0)`，`lib/index.js:1009`），不编一个绝对数。
	r.ctxWindow = 0
	r.capacity = llm.ModelCapacity{}
	if got := r.compactSelectOptions().KeepTokens; got != 0 {
		t.Fatalf("窗口未知时 KeepTokens = %d，期望 0（只按条数留，不编绝对值）", got)
	}
}
