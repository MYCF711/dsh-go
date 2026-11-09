package main

import (
	"encoding/json"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

func appendW149ContextEvent(t *testing.T, lg *session.Log, kind string, data any) {
	t.Helper()
	if err := lg.Append(kind, data); err != nil {
		t.Fatal(err)
	}
}

func TestContextUsageFullPreservesOnlyMeasuredSnapshot(t *testing.T) {
	home := t.TempDir()
	const sid = "session-context-full"
	lg, err := session.CreateSession(home, t.TempDir(), sid, "default")
	if err != nil {
		t.Fatal(err)
	}
	appendW149ContextEvent(t, lg, "system/message", session.SystemMessageData(1, 1, "system text"))
	appendW149ContextEvent(t, lg, "request/context", map[string]any{
		"model": "model-a", "contextWindow": 1000,
		"contextUsage": map[string]any{
			"categories": []any{map[string]any{"name": "messages", "tokens": 100, "kind": "used"}},
			"sections":   []any{map[string]any{"name": "system", "tokens": 40}},
			"files":      []any{map[string]any{"path": "D:/work/AGENTS.md", "tokens": 10}},
			"skills":     []any{},
			"tools":      []any{map[string]any{"name": "search", "server": "docs", "tokens": 20}},
		},
	})
	appendW149ContextEvent(t, lg, "assistant/message", session.AssistantMessageEvent(1, 1,
		[]map[string]any{session.TextBlock("answer")}, "stop",
		map[string]any{"inputTokens": 70, "outputTokens": 30, "totalTokens": 100}))
	// 该记录位于选中 assistant 之后，不能倒灌进它的快照。
	appendW149ContextEvent(t, lg, "request/context", map[string]any{"contextWindow": 9999})
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := newSessionSource(home, "").ContextUsage(sid, "full")
	if err != nil {
		t.Fatal(err)
	}
	if got.Used != 100 || got.Max != 1000 || len(got.Categories) != 1 || got.Categories[0].Name != "messages" || got.Categories[0].Tokens != 100 {
		t.Fatalf("usage=%+v", got)
	}
	if got.Sections == nil || len(*got.Sections) != 1 || (*got.Sections)[0].Name != "system" || (*got.Sections)[0].Tokens != 40 {
		t.Fatalf("sections=%+v", got.Sections)
	}
	if got.Files == nil || (*got.Files)[0].Path != "D:/work/AGENTS.md" || got.Skills == nil || len(*got.Skills) != 0 {
		t.Fatalf("files/skills=%+v/%+v", got.Files, got.Skills)
	}
	if got.Tools == nil || len(*got.Tools) != 1 || (*got.Tools)[0].Server != "docs" || (*got.Tools)[0].Tokens != 20 {
		t.Fatalf("tools=%+v", got.Tools)
	}
}

func TestContextUsageFullDoesNotInventBreakdownOrReuseStaleUsage(t *testing.T) {
	home := t.TempDir()
	const sid = "session-context-aggregate"
	lg, err := session.CreateSession(home, t.TempDir(), sid, "default")
	if err != nil {
		t.Fatal(err)
	}
	appendW149ContextEvent(t, lg, "system/message", session.SystemMessageData(1, 1, "real system text"))
	appendW149ContextEvent(t, lg, "request/header", map[string]any{"header": map[string]any{"tools": []any{map[string]any{"name": "read"}}}})
	appendW149ContextEvent(t, lg, "assistant/message", session.AssistantMessageEvent(1, 1,
		[]map[string]any{session.TextBlock("answer")}, "stop",
		map[string]any{"inputTokens": 70, "outputTokens": 30, "totalTokens": 100}))
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := newSessionSource(home, "").ContextUsage(sid, "full")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Categories) != 1 || got.Categories[0].Name != "context" || got.Categories[0].Tokens != 100 || got.Max != 0 {
		t.Fatalf("无真实 breakdown 却伪造分类/max: %+v", got)
	}
	if got.Sections != nil || got.Files != nil || got.Skills != nil || got.Tools != nil {
		t.Fatalf("没有持久计量却伪造 details: %+v", got)
	}
}

// GO.6 口径改判（**故意改了旧断言，理由写在下面**）：
//
//   - 最近 assistant 缺 usage：仍然 fail-loud。没有实测锚点就没有压力，不能补零。
//   - 「usage 之后 surface 又被改变」：不再报错。旧断言守的是"不许把陈旧的
//     totalTokens 当成当前上下文占用"；而官方（dsh-token-meter
//     lib/types/usage-projection.js）正是为这个状态提供了 projectedTokens
//     ——pressure 只描述最近一次请求，projected 预测下一次。继续报错会让
//     projectedTokens 在**唯一有用的那个状态**（用户刚敲新消息 / 工具结果刚灌入）
//     永远观测不到，属于宿主比官方更弱。
func TestContextUsageRejectsMissingUsageButProjectsChangedSurface(t *testing.T) {
	t.Run("missing-usage", func(t *testing.T) {
		home := t.TempDir()
		const sid = "session-context-stale"
		lg, err := session.CreateSession(home, t.TempDir(), sid, "default")
		if err != nil {
			t.Fatal(err)
		}
		appendW149ContextEvent(t, lg, "assistant/message", session.AssistantMessageEvent(1, 1,
			[]map[string]any{session.TextBlock("old")}, "stop", map[string]any{"totalTokens": 40}))
		appendW149ContextEvent(t, lg, "assistant/message", session.AssistantMessageEvent(2, 1,
			[]map[string]any{session.TextBlock("latest-without-usage")}, "stop", nil))
		if err := lg.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := newSessionSource(home, "").ContextUsage(sid, "full"); err == nil || !strings.Contains(err.Error(), "context.usage") {
			t.Fatalf("最近 assistant 没有 usage 却被当成当前真值: err=%v", err)
		}
	})

	t.Run("changed-surface", func(t *testing.T) {
		home := t.TempDir()
		const sid = "session-context-stale"
		lg, err := session.CreateSession(home, t.TempDir(), sid, "default")
		if err != nil {
			t.Fatal(err)
		}
		appendW149ContextEvent(t, lg, "assistant/message", session.AssistantMessageEvent(1, 1,
			[]map[string]any{session.TextBlock("old")}, "stop", map[string]any{"totalTokens": 40}))
		appendW149ContextEvent(t, lg, "user/message", session.UserMessage([]spi.Block{{Type: "text", Text: "new unmeasured input"}}))
		if err := lg.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := newSessionSource(home, "").ContextUsage(sid, "full")
		if err != nil {
			t.Fatalf("surface 变了也要给出投影（projectedTokens 就是为此存在）: %v", err)
		}
		if got.Used != 40 {
			t.Fatalf("used = %d，期望最近一次实测 totalTokens 40（向后兼容口径）", got.Used)
		}
		if got.PressureTokens != 40 {
			t.Fatalf("pressure = %d，期望 40（只有 totalTokens 的旧会话按 total 兜底）", got.PressureTokens)
		}
		if got.ProjectedTokens <= got.PressureTokens {
			t.Fatalf("projected = %d 必须大于 pressure = %d（surface 长了）", got.ProjectedTokens, got.PressureTokens)
		}
		if got.UsageBuckets == nil || got.UsageBuckets.UncachedInputTokens != 40 {
			t.Fatalf("四桶 = %+v，期望未命中输入 40", got.UsageBuckets)
		}
	})
}

func TestContextUsageSnapshotStrictNullAndSafeIntegers(t *testing.T) {
	valid := `{"contextWindow":1000,"contextUsage":{"categories":[{"name":"messages","tokens":1,"kind":"used"}],"sections":[],"files":[],"skills":[],"tools":[]}}`
	if _, err := parseContextUsageSnapshot([]byte(valid)); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}
	invalid := []string{
		`null`,
		`{"contextWindow":null}`,
		`{"contextWindow":0}`,
		`{"contextWindow":9007199254740992}`,
		`{"contextUsage":null}`,
		`{"contextUsage":{"sections":null}}`,
		`{"contextUsage":{"sections":[{"name":"system"}]}}`,
		`{"contextUsage":{"sections":[{"name":"system","tokens":null}]}}`,
		`{"contextUsage":{"sections":[{"name":"system","tokens":-1}]}}`,
		`{"contextUsage":{"sections":[{"name":"system","tokens":9007199254740992}]}}`,
		`{"contextUsage":{"categories":[{"name":"messages","tokens":1}]}}`,
		`{"contextUsage":{"tools":[{"name":"read","tokens":1,"server":""}]}}`,
		`{"contextUsage":{"tools":[{"name":"read","tokens":1,"server":null}]}}`,
	}
	for _, raw := range invalid {
		if _, err := parseContextUsageSnapshot([]byte(raw)); err == nil {
			t.Errorf("非法快照被接受: %s", raw)
		}
	}
	if _, err := parseContextUsageSnapshot([]byte(`{"contextWindow":9007199254740991,"contextUsage":{"sections":[{"name":"system","tokens":9007199254740991}]}}`)); err != nil {
		t.Fatalf("最大 JS 安全整数应通过: %v", err)
	}
}

func TestContextUsageRejectsUnsafeTotalAndAcceptsMeasuredZero(t *testing.T) {
	for _, test := range []struct {
		usage map[string]any
		want  int
		bad   bool
	}{{map[string]any{"totalTokens": 0}, 0, false}, {map[string]any{"totalTokens": int64(9007199254740992)}, 0, true}} {
		lg := &v4.Log{Events: []v4.Event{{Type: "assistant/message", Data: []byte(`{"usage":` + mustW149JSON(t, test.usage) + `}`)}}}
		got, err := newSessionSource(t.TempDir(), "").contextUsageFromV4(lg)
		if test.bad && err == nil {
			t.Fatalf("unsafe total 被接受: %+v", got)
		}
		if !test.bad && (err != nil || got.Used != test.want || len(got.Categories) != 1 || got.Categories[0].Tokens != test.want) {
			t.Fatalf("measured zero: got=%+v err=%v", got, err)
		}
	}
}

func mustW149JSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
