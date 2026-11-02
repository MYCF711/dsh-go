package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/spi"
)

// buildLog 写一个会话文件并读回来。
func buildLog(t *testing.T, appendFn func(l *Log)) *v4.Log {
	t.Helper()
	home := t.TempDir()
	l, err := CreateSession(home, "/tmp/surface", "s-surface", "standard")
	if err != nil {
		t.Fatal(err)
	}
	appendFn(l)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	back, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	return back
}

func TestSurfaceHidesCompactedRange(t *testing.T) {
	log := buildLog(t, func(l *Log) {
		must := func(ev string, d any) {
			t.Helper()
			if err := l.Append(ev, d); err != nil {
				t.Fatal(err)
			}
		}
		must("user/message", UserMessage([]spi.Block{{Type: "text", Text: "第一问"}})) // seq 0
		must("assistant/message", AssistantMessageEvent(1, 1,                       // seq 1
			[]map[string]any{TextBlock("第一答")}, "stop", nil))
		must("user/message", UserMessage([]spi.Block{{Type: "text", Text: "第二问"}})) // seq 2
		// seq 3：把 0..1 压成摘要
		must("compaction/summary", map[string]any{
			"compactionId":       "c1",
			"summary":            []map[string]any{TextBlock("（摘要）第一问第一答")},
			"shadowedRange":      map[string]any{"start": 0, "end": 1},
			"shadowedSeqs":       []int64{0, 1},
			"shadowedTokenCount": 42,
			"provider":           "magpie",
			"model":              "m",
		})
		must("user/message", UserMessage([]spi.Block{{Type: "text", Text: "第三问"}})) // seq 4
	})

	surface := Surface(log)
	if len(surface) != 3 {
		t.Fatalf("surface 节点数 = %d，期望 3（第二问 + 摘要 + 第三问）：%+v", len(surface), surface)
	}
	if surface[0].Message.Content != "第二问" {
		t.Fatalf("节点 0 = %+v", surface[0])
	}
	if !surface[1].Summary || surface[1].Source != "compact-checkpoint" {
		t.Fatalf("节点 1 应是压缩摘要: %+v", surface[1])
	}
	if !strings.Contains(surface[1].Message.Content, "摘要") {
		t.Fatalf("摘要内容不对: %q", surface[1].Message.Content)
	}
	if surface[2].Message.Content != "第三问" {
		t.Fatalf("节点 2 = %+v", surface[2])
	}

	// 日志本身不能少一条（append-only）。
	if len(log.Events) != 5 {
		t.Fatalf("日志事件数 = %d，期望 5（遮蔽只发生在派生期）", len(log.Events))
	}
}

func TestSurfaceKeepsSystemMessage(t *testing.T) {
	log := buildLog(t, func(l *Log) {
		if err := l.Append("system/message", map[string]any{
			"turn": 1, "step": 1,
			"message": map[string]any{"role": "system", "content": []map[string]any{TextBlock("你是 dsh-go")}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := l.Append("user/message", UserMessage([]spi.Block{{Type: "text", Text: "hi"}})); err != nil {
			t.Fatal(err)
		}
	})
	hist := History(log)
	if len(hist) != 2 || hist[0].Role != "system" || hist[1].Role != "user" {
		t.Fatalf("历史 = %+v", hist)
	}
}

func TestSurfaceHonorsReplaceOp(t *testing.T) {
	home := t.TempDir()
	l, err := CreateSession(home, "/tmp/surface", "s-replace", "standard")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("user/message", UserMessage([]spi.Block{{Type: "text", Text: "原文"}})); err != nil {
		t.Fatal(err)
	}
	// 手工追加一条带 replace 的 user/message
	w, err := v4.OpenAppend(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(v4.Event{
		Type: "user/message", Seq: 1, Time: 1,
		Data:      []byte(`{"content":[{"type":"text","text":"替换后"}],"role":"user","id":"x","surfaceOp":"append"}`),
		SurfaceOp: []byte(`{"op":"replace","startSeq":0,"endSeq":0}`),
	}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	l.Close()

	back, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	surface := Surface(back)
	if len(surface) != 1 || surface[0].Message.Content != "替换后" {
		t.Fatalf("replace 没生效: %+v", surface)
	}
}

// TestSurfaceRealCorpus 在真实压缩过的会话上验证：节点变少、摘要出现、日志不短。
func TestSurfaceRealCorpus(t *testing.T) {
	root := os.Getenv("DSH_HOME")
	if root == "" {
		root = `D:\DeepSeek Harness\home`
	}
	sessions := filepath.Join(root, "sessions")
	if _, err := os.Stat(sessions); err != nil {
		t.Skip("本机没有真实会话目录")
	}
	checked := 0
	err := filepath.Walk(sessions, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".zstd") || checked >= 40 {
			return nil
		}
		log, err := v4.ReadFile(p)
		if err != nil {
			return nil
		}
		eligible, summaries, prunes := 0, 0, 0
		for _, ev := range log.Events {
			switch ev.Type {
			case "system/message", "developer/message", "user/message", "assistant/message", "tool/result":
				eligible++
			case "compaction/summary":
				summaries++
			case "compaction/prune":
				prunes++
			}
		}
		compacted := summaries + prunes
		if compacted == 0 {
			return nil
		}
		checked++
		surface := Surface(log)
		summaryNodes := 0
		for _, n := range surface {
			if n.Summary {
				summaryNodes++
			}
		}
		if len(surface) > eligible+summaryNodes {
			t.Fatalf("%s: surface=%d 超过上限 %d", filepath.Base(p), len(surface), eligible+summaryNodes)
		}
		// 压缩会**级联**：后一次压缩把上一次的摘要节点也遮蔽掉，
		// 所以可见摘要节点数通常远小于压缩次数（真实语料里 9 次压缩 → 1 个可见摘要）。
		// 只有 compaction/summary 会产生替身节点；compaction/prune 是纯裁剪，不留摘要。
		if summaryNodes > summaries {
			t.Errorf("%s: 可见摘要 %d 多于 summary 事件 %d", filepath.Base(p), summaryNodes, summaries)
		}
		if summaries > 0 && summaryNodes == 0 {
			t.Errorf("%s: 有 %d 次 summary 却没有任何可见摘要", filepath.Base(p), summaries)
		}
		t.Logf("%s：可派生 %d 条 → surface %d 条（summary %d / prune %d，可见摘要 %d）",
			filepath.Base(filepath.Dir(p)), eligible, len(surface), summaries, prunes, summaryNodes)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Skip("没有找到压缩过的会话")
	}
}
