package session

import (
	"strconv"
	"strings"
	"testing"
)

func writeQuerySession(t *testing.T, home, cwd, id string, texts ...string) string {
	t.Helper()
	log, err := CreateSession(home, cwd, id, "")
	if err != nil {
		t.Fatalf("建会话 %s: %v", id, err)
	}
	if err := log.Append("turn/start", map[string]any{"turn": 1}); err != nil {
		t.Fatal(err)
	}
	for index, text := range texts {
		event := UserMessage([]Block{{Type: "text", Text: text}})
		if err := log.Append("user/message", event); err != nil {
			t.Fatalf("写第 %d 条: %v", index, err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return log.Path
}

func TestSessionQueryCrossSessionPagination(t *testing.T) {
	home := t.TempDir()
	writeQuerySession(t, home, "/project", "s-1", "alpha needle one", "beta needle two")
	writeQuerySession(t, home, "/project", "s-2", "gamma needle three", "unrelated")

	var all []QueryHit
	cursor := ""
	pages := 0
	for {
		page, err := SearchSessions(QueryRequest{Home: home, Text: "needle", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("第 %d 页失败: %v", pages, err)
		}
		if page.Engine != QueryEngineScan {
			t.Fatalf("engine = %q，必须诚实标注 scan（不是 FTS5）", page.Engine)
		}
		all = append(all, page.Hits...)
		pages++
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatalf("最后一页不该有 nextCursor: %q", page.NextCursor)
			}
			break
		}
		if page.NextCursor == "" {
			t.Fatal("HasMore=true 必须有 nextCursor")
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("分页没有收敛")
		}
	}
	if pages != 2 {
		t.Fatalf("页数 = %d，期望 2（3 条命中、每页 2 条）", pages)
	}
	if len(all) != 3 {
		t.Fatalf("命中 = %d 条，期望 3", len(all))
	}
	seen := map[string]bool{}
	for _, hit := range all {
		key := hit.SessionID + "#" + strconv.FormatInt(hit.Seq, 10)
		if seen[key] {
			t.Fatalf("分页重复了 %s", key)
		}
		seen[key] = true
	}
	if all[0].SessionID != "s-1" || all[len(all)-1].SessionID != "s-2" {
		t.Fatalf("会话顺序不稳定: %+v", all)
	}
	if !strings.Contains(all[0].Excerpt, "needle") {
		t.Fatalf("excerpt = %q", all[0].Excerpt)
	}
	if all[0].Role != "user" {
		t.Fatalf("role = %q", all[0].Role)
	}
}

func TestSessionQueryWhitespaceFlexibleAndCaseInsensitive(t *testing.T) {
	home := t.TempDir()
	writeQuerySession(t, home, "/project", "s-1", "Hello   World\n\nsecond line")
	page, err := SearchSessions(QueryRequest{Home: home, Text: "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 1 {
		t.Fatalf("命中 = %d 条（大小写不敏感 + 空白折叠）", len(page.Hits))
	}
	// 换行在摘录里必须摊平（否则一行摘录里带 \n 会把界面撑坏）。
	if strings.Contains(page.Hits[0].Excerpt, "\n") {
		t.Fatalf("excerpt 必须把换行摊平: %q", page.Hits[0].Excerpt)
	}
}

func TestSessionQueryInvalidRequests(t *testing.T) {
	home := t.TempDir()
	writeQuerySession(t, home, "/project", "s-1", "hello")
	cases := []struct {
		name    string
		request QueryRequest
	}{
		{"empty", QueryRequest{Home: home, Text: "  "}},
		{"nul", QueryRequest{Home: home, Text: "he\x00llo"}},
		{"too-long", QueryRequest{Home: home, Text: strings.Repeat("x", QueryMaxChars+1)}},
		{"bad-cursor", QueryRequest{Home: home, Text: "hello", Cursor: "not-a-cursor"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SearchSessions(tc.request); err == nil {
				t.Fatal("非法请求必须失败")
			}
		})
	}
	// limit 夹紧：0 用默认值，超上限夹到 100。
	page, err := SearchSessions(QueryRequest{Home: home, Text: "hello", Limit: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if page.Limit != QueryMaxLimit {
		t.Fatalf("limit = %d，期望夹紧到 %d", page.Limit, QueryMaxLimit)
	}
}

func TestSessionQueryFilters(t *testing.T) {
	home := t.TempDir()
	writeQuerySession(t, home, "/project", "s-1", "needle in session one")
	writeQuerySession(t, home, "/project", "s-2", "needle in session two")

	bySession, err := SearchSessions(QueryRequest{Home: home, Text: "needle", SessionID: "s-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySession.Hits) != 1 || bySession.Hits[0].SessionID != "s-2" {
		t.Fatalf("SessionID 过滤 = %+v", bySession.Hits)
	}

	byType, err := SearchSessions(QueryRequest{Home: home, Text: "needle", Types: []string{"assistant/message"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byType.Hits) != 0 {
		t.Fatalf("类型过滤 = %+v", byType.Hits)
	}

	missing, err := SearchSessions(QueryRequest{Home: home, Text: "needle", SessionID: "nope"})
	if err != nil {
		t.Fatalf("不存在的会话应当给空页而不是报错: %v", err)
	}
	if len(missing.Hits) != 0 || missing.HasMore {
		t.Fatalf("missing = %+v", missing)
	}
}

func TestSessionQueryMarksCompactionShadowed(t *testing.T) {
	home := t.TempDir()
	log, err := CreateSession(home, "/project", "s-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append("user/message", UserMessage([]Block{{Type: "text", Text: "needle before compaction"}})); err != nil {
		t.Fatal(err)
	}
	if err := log.Append("compaction/prune", map[string]any{
		"shadowedSeqs":       []int64{0},
		"shadowedRange":      map[string]any{"start": 0, "end": 0},
		"shadowedTokenCount": 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := SearchSessions(QueryRequest{Home: home, Text: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 1 {
		t.Fatalf("命中 = %d", len(page.Hits))
	}
	if !page.Hits[0].Hidden {
		t.Fatal("被压缩遮蔽的 seq 必须标 Hidden（命中要能说明它已经不在表面上）")
	}
}

func TestEventTextCoversToolCallsAndReasoning(t *testing.T) {
	// 只有 tool_calls 的 assistant 也必须可检索（历史检索存在的理由就是"上次那个 ls 在哪一轮"）。
	assistant := testEvent(t, "assistant/message", 0, map[string]any{
		"turn": 1, "step": 1,
		"message": map[string]any{
			"role": "assistant",
			"content": []map[string]any{
				{"type": "reasoning", "text": "先看目录"},
				{"type": "tool-call", "id": "c1", "name": "bash", "arguments": `{"command":"ls"}`},
			},
		},
	})
	text := EventText(assistant)
	if !strings.Contains(text, "[调用 bash：") || !strings.Contains(text, "ls") {
		t.Fatalf("tool-call 文本 = %q", text)
	}
	if !strings.Contains(text, "[思考：") || !strings.Contains(text, "先看目录") {
		t.Fatalf("reasoning 文本 = %q", text)
	}
	// 空 text 块不算内容。
	empty := testEvent(t, "user/message", 0, map[string]any{
		"content": []map[string]any{{"type": "text", "text": "   "}},
	})
	if got := EventText(empty); got != "" {
		t.Fatalf("空白 text 块 = %q，期望空串", got)
	}
}

func TestQueryCursorRoundTrip(t *testing.T) {
	cursor := QueryCursor{SessionID: "s-1", Seq: 42}
	encoded, err := EncodeQueryCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Fatalf("游标必须是 URL 安全的 base64: %q", encoded)
	}
	decoded, err := DecodeQueryCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != cursor {
		t.Fatalf("round trip = %+v，期望 %+v", decoded, cursor)
	}
	if _, err := DecodeQueryCursor("!!!"); err == nil {
		t.Fatal("坏游标必须失败")
	}
}
