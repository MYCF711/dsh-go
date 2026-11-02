package session

import (
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/spi"
)

func buildTenEventLog(t *testing.T) (*Log, *v4.Log) {
	t.Helper()
	home := t.TempDir()
	l, err := CreateSession(home, "/tmp/compact", "s-compact", "standard")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := l.Append("user/message", UserMessage([]spi.Block{{Type: "text", Text: "问"}})); err != nil {
			t.Fatal(err)
		}
		if err := l.Append("assistant/message", AssistantMessageEvent(1, i+1,
			[]map[string]any{TextBlock("答")}, "stop", nil)); err != nil {
			t.Fatal(err)
		}
	}
	back, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	return l, back
}

func TestCompactHidesRangeAndAppends(t *testing.T) {
	l, back := buildTenEventLog(t)
	before := len(back.Events)

	id, shadowed, err := Compact(l, back.Events, Span{Start: 0, End: 3},
		[]spi.Block{{Type: "text", Text: "（前两轮摘要）"}},
		CompactionMeta{Provider: "magpie", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || !strings.HasPrefix(id, "cmp-") {
		t.Fatalf("compactionId = %q", id)
	}
	if len(shadowed) != 4 {
		t.Fatalf("被遮蔽 %d 条，期望 4：%v", len(shadowed), shadowed)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	after, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != before+3 {
		t.Fatalf("日志长度 %d → %d，期望 +3（start/summary/end）", before, len(after.Events))
	}
	// 日志一条不少：被遮蔽的事件还在文件里。
	seqs := map[int64]bool{}
	for _, ev := range after.Events {
		seqs[ev.Seq] = true
	}
	for s := int64(0); s < int64(before); s++ {
		if !seqs[s] {
			t.Fatalf("seq %d 从日志里消失了——日志必须 append-only", s)
		}
	}

	surface := Surface(after)
	hiddenNodes := 0
	checkpoints := 0
	for _, n := range surface {
		if n.Summary {
			checkpoints++
		}
		if n.Seq >= 0 && n.Seq <= 3 {
			hiddenNodes++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("替身节点数 = %d，期望 1", checkpoints)
	}
	if hiddenNodes != 0 {
		t.Fatalf("被遮蔽区间仍在 surface 上：%d 个节点", hiddenNodes)
	}
	// 原本 8 个节点，遮蔽 4 个，加 1 个替身 = 5。
	if len(surface) != 5 {
		t.Fatalf("surface 节点数 = %d，期望 5", len(surface))
	}
}

func TestCompactRejectsEmptyRange(t *testing.T) {
	l, back := buildTenEventLog(t)
	defer l.Close()
	if _, _, err := Compact(l, back.Events, Span{Start: 100, End: 200}, nil, CompactionMeta{}); err == nil {
		t.Fatal("区间里没有事件必须报错")
	}
	if _, _, err := Compact(l, back.Events, Span{Start: 5, End: 1}, nil, CompactionMeta{}); err == nil {
		t.Fatal("start > end 必须报错")
	}
}

func TestCompactTwiceCascades(t *testing.T) {
	l, back := buildTenEventLog(t)
	if _, _, err := Compact(l, back.Events, Span{Start: 0, End: 3},
		[]spi.Block{{Type: "text", Text: "第一次摘要"}}, CompactionMeta{}); err != nil {
		t.Fatal(err)
	}
	mid, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	// 第二次把"第一次的摘要"也压进去（真实语料里就是这样级联的）。
	var firstSummarySeq int64 = -1
	for _, ev := range mid.Events {
		if ev.Type == "compaction/summary" {
			firstSummarySeq = ev.Seq
		}
	}
	if firstSummarySeq < 0 {
		t.Fatal("找不到第一次的 summary")
	}
	if _, _, err := Compact(l, mid.Events, Span{Start: 0, End: firstSummarySeq},
		[]spi.Block{{Type: "text", Text: "第二次摘要"}}, CompactionMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	after, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	surface := Surface(after)
	checkpoints := 0
	for _, n := range surface {
		if n.Summary {
			checkpoints++
			if n.Message.Content != "第二次摘要" {
				t.Fatalf("可见摘要 = %q，期望最后一次", n.Message.Content)
			}
		}
	}
	if checkpoints != 1 {
		t.Fatalf("级联后可见摘要 = %d，期望 1（前一个被后一个遮蔽）", checkpoints)
	}
}
