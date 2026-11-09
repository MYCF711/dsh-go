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
	// 其余两项保持原值（这次只接 keepRecent，不顺手改别人的预算）。
	opts := r.compactSelectOptions()
	if opts.KeepTokens != 2000 || opts.MinToCompact != 2 {
		t.Fatalf("KeepTokens/MinToCompact = %d/%d，期望保持 2000/2", opts.KeepTokens, opts.MinToCompact)
	}
}
