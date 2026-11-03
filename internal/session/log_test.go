package session

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/spi"
)

func TestSessionWritesReadableLog(t *testing.T) {
	home := t.TempDir()
	cwd := `D:\work\demo`
	l, err := CreateSession(home, cwd, "session-test-1", "standard")
	if err != nil {
		t.Fatal(err)
	}

	must := func(ev string, d any) {
		t.Helper()
		if err := l.Append(ev, d); err != nil {
			t.Fatalf("append %s: %v", ev, err)
		}
	}
	must("turn/start", TurnStartData(1))
	must("user/message", UserMessage([]spi.Block{{Type: "text", Text: "你好"}}))
	must("assistant/message", AssistantMessageEvent(1, 1,
		[]map[string]any{TextBlock("我去读文件"), ToolCallBlock("call_1", "read", `{"file_path":"a.txt"}`)},
		"tool_calls", map[string]any{"inputTokens": 10, "outputTokens": 3, "totalTokens": 13}))
	must("tool/call", ToolCallEvent(1, 1, "call_1", "read", `{"file_path":"a.txt"}`))
	must("tool/result", ToolResultEvent(1, 1, "call_1", []spi.Block{{Type: "text", Text: "读到了"}}, false, cwd, []int64{3}))
	must("turn/end", TurnEndData(1, "completed", ""))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// 路径规则：<home>/sessions/<projectKey>/<segment>/session.v4.jsonl.zstd
	want := filepath.Join(home, "sessions", "--D-work-demo--", "session-test-1", "session.v4.jsonl.zstd")
	if l.Path != want {
		t.Fatalf("路径 = %s，期望 %s", l.Path, want)
	}

	back, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatalf("自己写的文件读不回来: %v", err)
	}
	if back.Header.Cwd != cwd || back.Header.AgentPreset != "standard" || back.Header.Version != 4 {
		t.Fatalf("header = %+v", back.Header)
	}
	if len(back.Events) != 6 {
		t.Fatalf("事件数 = %d，期望 6", len(back.Events))
	}
	wantTypes := []string{"turn/start", "user/message", "assistant/message", "tool/call", "tool/result", "turn/end"}
	for i, ev := range back.Events {
		if ev.Type != wantTypes[i] {
			t.Fatalf("第 %d 个事件类型 = %q，期望 %q", i, ev.Type, wantTypes[i])
		}
		if ev.Seq != int64(i) {
			t.Fatalf("第 %d 个事件 seq = %d", i, ev.Seq)
		}
	}
}

func TestMessageEventsCarrySurfaceOp(t *testing.T) {
	home := t.TempDir()
	l, err := CreateSession(home, "/tmp/x", "s2", "standard")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("user/message", UserMessage([]spi.Block{{Type: "text", Text: "hi"}})); err != nil {
		t.Fatal(err)
	}
	if err := l.Append("tool/result", ToolResultEvent(1, 1, "c1", []spi.Block{{Type: "text", Text: "r"}}, false, "/tmp/x", []int64{0})); err != nil {
		t.Fatal(err)
	}
	l.Close()

	back, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range back.Events {
		var m map[string]any
		if err := json.Unmarshal(ev.Data, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["surfaceOp"]; !ok {
			t.Fatalf("%s 缺 surfaceOp", ev.Type)
		}
	}
}

func TestToolCallArgumentsAreStrings(t *testing.T) {
	home := t.TempDir()
	l, err := CreateSession(home, "/tmp/x", "s3", "standard")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append("tool/call", ToolCallEvent(1, 2, "c9", "read", `{"file_path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	l.Close()

	back, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Data struct {
			Arguments json.RawMessage `json:"arguments"`
		} `json:"data"`
	}
	if err := json.Unmarshal(back.Events[0].Raw, &m); err != nil {
		t.Fatal(err)
	}
	var s string
	if err := json.Unmarshal(m.Data.Arguments, &s); err != nil {
		t.Fatalf("arguments 必须是 JSON 字符串，得到 %s", m.Data.Arguments)
	}
	if s != `{"file_path":"a.txt"}` {
		t.Fatalf("arguments = %q", s)
	}
}

// H3：工具的 ViewSpec 要能落到事件里；没有视图时**不出现 view 键**（不是 null）。
func TestToolResultEventCarriesView(t *testing.T) {
	view := json.RawMessage(`{"kind":"plain","body":"内容"}`)
	ev := ToolResultEventWithView(1, 2, "c1", nil, false, "/w", nil, view)
	got, ok := ev["view"].(json.RawMessage)
	if !ok || len(got) == 0 {
		t.Fatalf("带视图时应当有 view 键，实际 %v", ev["view"])
	}

	bare := ToolResultEventWithView(1, 2, "c1", nil, false, "/w", nil, nil)
	if _, has := bare["view"]; has {
		t.Fatalf("没有视图时不该出现 view 键，实际 %v", bare["view"])
	}
}
