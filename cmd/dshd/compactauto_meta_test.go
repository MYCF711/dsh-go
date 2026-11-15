package main

// `compaction/summary.shadowedTokenCount` 的语义是**被遮蔽那段历史有多少 token**。
//
// 旧实现（`compactauto.go` 的 `TokenCount: len([]rune(text))`）把这个字段写成了
// **摘要正文的字数**：一次压掉几万 token 的历史、摘要只有几十字，落盘就是
// `shadowedTokenCount: 几十`。官方客户端与 `ManualCompactionResult.shadowedTokenCount`
// 的读者拿到的是一个已知不是 token 的数字。
//
// 这个用例钉住：落盘值等于"被遮蔽区间按宿主估算器算出来的体积"，且**不等于**
// 摘要字数（旧值）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// seedAutoCompactSession 造一条"够压"的会话并返回它的路径（写完即关闭，
// 因为 autoCompact 自己要抢写锁）。
func seedAutoCompactSession(t *testing.T) string {
	t.Helper()
	lg, err := session.CreateSession(t.TempDir(), t.TempDir(), "session-compact-tokens", "default")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	// 每条都要够长：`keep` 是**条数下限**，真正的保留量由 KeepTokens（默认 2000）
	// 定；12 条几千字的往返才压得出前缀。
	long := strings.Repeat("这一轮要把历史撑到明显超过保留预算，这样压缩区间才有实体内容可遮蔽。", 12)
	for i := 0; i < 6; i++ {
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: long}})); err != nil {
			t.Fatalf("写 user/message: %v", err)
		}
		if err := lg.Append("assistant/message", session.AssistantMessageEvent(1, i+1,
			[]map[string]any{session.TextBlock("收到，这一条也占预算")}, "stop", nil)); err != nil {
			t.Fatalf("写 assistant/message: %v", err)
		}
	}
	path := lg.Path
	if err := lg.Close(); err != nil {
		t.Fatalf("关会话: %v", err)
	}
	return path
}

// summaryTokenCount 读回最后一条 compaction/summary 的 shadowedTokenCount。
func summaryTokenCount(t *testing.T, path string) int {
	t.Helper()
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读回日志: %v", err)
	}
	got := -1
	for _, ev := range back.Events {
		if ev.Type != "compaction/summary" {
			continue
		}
		var data struct {
			TokenCount int `json:"shadowedTokenCount"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("解析 compaction/summary: %v", err)
		}
		got = data.TokenCount
	}
	if got < 0 {
		t.Fatal("日志里没有 compaction/summary：压缩没有真的发生")
	}
	return got
}

func TestAutoCompactWritesShadowedTokensNotSummaryLength(t *testing.T) {
	const summary = "好" // 1 个 rune：旧值会落盘成 1，和 token 数一眼就能分开
	base := d1SSE(t, d1Text(summary, "stop"))

	path := seedAutoCompactSession(t)
	before, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("压缩前读日志: %v", err)
	}

	res, err := autoCompact(context.Background(), path, 1, 0, compactMetaModel, base)
	if err != nil {
		t.Fatalf("自动压缩: %v", err)
	}
	if !res.Compacted {
		t.Fatalf("6 组问答按 keep=1 必须能压，实际没压")
	}

	want := compactShadowedTokens(before, res.From, res.To)
	if want <= len([]rune(summary)) {
		t.Fatalf("前置条件不成立：被遮蔽区间的估算只有 %d token，和摘要字数分不开", want)
	}
	if got := summaryTokenCount(t, path); got != want {
		t.Fatalf("shadowedTokenCount = %d，应为被遮蔽区间的 token 估算 %d（旧实现写的是摘要字数 %d）",
			got, want, len([]rune(summary)))
	}
}
