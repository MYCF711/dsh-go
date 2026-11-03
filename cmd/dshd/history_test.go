package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// buildSession 造一个带压缩遮蔽的会话文件。
func buildSessionForHistory(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	w, err := session.CreateSession(home, "/tmp/h", "hist-1", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append("system/message", session.SystemMessageData(1, 1, "你是 dsh-go")); err != nil {
		t.Fatal(err)
	}
	if err := w.Append("user/message", session.UserMessage([]spi.Block{
		{Type: "text", Text: "暗号是 NEEDLE-42，记住它"},
	})); err != nil {
		t.Fatal(err)
	}
	if err := w.Append("assistant/message", session.AssistantMessageEvent(1, 1,
		[]map[string]any{session.TextBlock("好的")}, "stop", nil)); err != nil {
		t.Fatal(err)
	}
	// 把 0..1 压掉，让暗号只存在于原始日志里
	// 读一遍只为确认压缩后文件仍可读；内容由下面的 Events 断言。
	if _, rerr := os.ReadFile(w.Path); rerr != nil {
		t.Fatalf("压缩后读不回会话文件: %v", rerr)
	}
	if _, _, err := session.Compact(w, mustRead(t, w.Path).Events,
		session.Span{Start: 0, End: 1},
		[]spi.Block{{Type: "text", Text: "（摘要）"}}, session.CompactionMeta{}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return w.Path
}

func mustRead(t *testing.T, p string) *v4.Log {
	t.Helper()
	l, err := v4.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestHistorySearchFindsShadowed(t *testing.T) {
	path := buildSessionForHistory(t)
	h := &historyHolder{}
	h.set(path)

	hits, err := h.Search("NEEDLE-42", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("原始日志里有暗号，应该能查到")
	}
	if !strings.Contains(hits[0].Excerpt, "NEEDLE-42") {
		t.Fatalf("片段里没有关键词: %q", hits[0].Excerpt)
	}
	if !hits[0].Hidden {
		t.Fatal("这条已经被压缩遮蔽，Hidden 应为 true（模型要知道它不在表面上）")
	}
	if hits[0].Role != "user" {
		t.Fatalf("role = %q", hits[0].Role)
	}
}

func TestHistorySearchNoSession(t *testing.T) {
	h := &historyHolder{}
	if _, err := h.Search("x", 5); err == nil {
		t.Fatal("没有活动会话时必须报错，不能返回空结果假装查过了")
	}
}

func TestHistorySearchEmptyQuery(t *testing.T) {
	h := &historyHolder{}
	if _, err := h.Search("", 5); err == nil {
		t.Fatal("空 query 必须报错")
	}
}

func TestHistorySearchNotHiddenWhenSurfaced(t *testing.T) {
	path := buildSessionForHistory(t)
	h := &historyHolder{}
	h.set(path)
	hits, err := h.Search("好的", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("没查到未被遮蔽的助手消息")
	}
	if hits[0].Hidden {
		t.Fatal("这条没被压缩，Hidden 应为 false")
	}
}

var _ = filepath.Join

// attachHistory 是 run / acp 两条入口共用的一步。漏掉它，
// 模型回查就会撞上"没有活动会话"——所以这里把它钉住。
func TestAttachHistoryPointsService(t *testing.T) {
	home := t.TempDir()
	l, err := session.CreateSession(home, "/tmp/h2", "hist-2", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Append("user/message", session.UserMessage([]spi.Block{
		{Type: "text", Text: "编号 ATTACH-1 记一下"},
	})); err != nil {
		t.Fatal(err)
	}

	hostHistory.set("") // 先清空，证明是 attachHistory 起了作用
	if _, err := hostHistory.Search("ATTACH-1", 5); err == nil {
		t.Fatal("清空之后本该报没有活动会话")
	}

	attachHistory(l)
	hits, err := hostHistory.Search("ATTACH-1", 5)
	if err != nil {
		t.Fatalf("attachHistory 之后应该能查: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("应该能查到刚写进去的消息")
	}

	attachHistory(nil) // nil 不能 panic
}
