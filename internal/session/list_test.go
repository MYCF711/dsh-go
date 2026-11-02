package session

import (
	"strings"
	"testing"

	"dshgo/internal/spi"
)

func mkSession(t *testing.T, home, cwd, id, userText, assistantText string) string {
	t.Helper()
	l, err := CreateSession(home, cwd, id, "read-only")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("user/message", UserMessage([]spi.Block{{Type: "text", Text: userText}})); err != nil {
		t.Fatal(err)
	}
	if err := l.Append("assistant/message", AssistantMessageEvent(1, 1,
		[]map[string]any{TextBlock(assistantText)}, "stop", nil)); err != nil {
		t.Fatal(err)
	}
	l.Close()
	return l.Path
}

func TestListSessionsAllProjects(t *testing.T) {
	home := t.TempDir()
	mkSession(t, home, "/tmp/proj-a", "a-1", "项目 A 的第一句", "A 的回复")
	mkSession(t, home, "/tmp/proj-b", "b-1", "项目 B 的第一句", "B 的回复")

	got, err := ListSessions(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应该列出 2 个会话，得到 %d", len(got))
	}
	for _, l := range got {
		if !l.Readable {
			t.Fatalf("这几份都得读得动: %+v", l)
		}
		if l.Events != 2 {
			t.Fatalf("%s 事件数 = %d", l.SessionID, l.Events)
		}
		if l.Preset != "read-only" {
			t.Fatalf("%s 的 preset = %q", l.SessionID, l.Preset)
		}
		if !strings.Contains(l.Summary, "回复") {
			t.Fatalf("摘要应该优先取助手消息，得到 %q", l.Summary)
		}
	}
}

func TestListSessionsFilterByProject(t *testing.T) {
	home := t.TempDir()
	mkSession(t, home, "/tmp/proj-a", "a-1", "A", "A 回复")
	mkSession(t, home, "/tmp/proj-b", "b-1", "B", "B 回复")

	got, err := ListSessions(home, "/tmp/proj-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "a-1" {
		t.Fatalf("按项目过滤后 = %+v", got)
	}
}

func TestListSessionsEmptyHome(t *testing.T) {
	got, err := ListSessions(t.TempDir(), "")
	if err != nil {
		t.Fatalf("没有 sessions 目录不该报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("应该是空列表，得到 %d", len(got))
	}
}

func TestListSessionsSummaryFallsBackToUser(t *testing.T) {
	home := t.TempDir()
	l, err := CreateSession(home, "/tmp/p", "u-1", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("user/message", UserMessage([]spi.Block{{Type: "text", Text: "只有用户消息"}})); err != nil {
		t.Fatal(err)
	}
	l.Close()

	got, err := ListSessions(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Summary, "只有用户消息") {
		t.Fatalf("没有助手消息时应回落到用户消息，得到 %+v", got)
	}
}
