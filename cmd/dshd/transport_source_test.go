package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

// TestSessionSourceGolden 拿一个**真实会话**跑 sessionSource.Messages，钉住三件事：
//   - 至少产出 1 条消息，且 Seq 严格递增（transport 的分页依赖这个）
//   - Role 只可能是 user / assistant / tool
//   - 至少有一条 user 且正文非空（否则界面打开会话会是空白）
//
// 语料不在仓库里（只读引用 DSH_TEST_SESSION），CI 上没有就 Skip——
// 这不是"测不到"，是"语料不该进仓库"。
func TestSessionSourceGolden(t *testing.T) {
	path := os.Getenv("DSH_TEST_SESSION")
	if path == "" {
		t.Skip("没设 DSH_TEST_SESSION，跳过真实语料 golden（CI 上本来就没有这份会话）")
	}

	// 会话 id 与 cwd 都从文件自己的 header 里取：目录名是转义过的 segment，
	// 反解不是我们要验的东西，而 header 里两个值都是现成的。
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读语料失败 %s: %v", path, err)
	}
	if back.Header.ID == "" {
		t.Fatalf("语料 header 没有 id：%s", path)
	}

	// <home>/sessions/<projectKey>/<segment>/session.v4.jsonl.zstd，往上四层就是 home。
	home := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	if base := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path)))); base != "sessions" {
		t.Fatalf("语料路径不像 <home>/sessions/<project>/<segment>/session.v4.jsonl.zstd（倒数第三层是 %q）：%s",
			base, path)
	}

	src := newSessionSource(home, back.Header.Cwd)
	msgs, err := src.Messages(back.Header.ID)
	if err != nil {
		t.Fatalf("Messages(%q) 出错: %v", back.Header.ID, err)
	}
	if len(msgs) == 0 {
		// 有的会话只有 header 和控制类事件（没有 user/assistant/tool），映射出 0 条是对的。
		// 那种会话做 golden 什么也证明不了——换一个，不是让它红。
		t.Skipf("这个会话没有消息事件（%d 条事件），换一个有消息的会话", len(back.Events))
	}
	t.Logf("会话 %s：%d 条事件 → %d 条消息", back.Header.ID, len(back.Events), len(msgs))

	prev := -1
	sawUser := false
	for i, m := range msgs {
		if m.Seq <= prev {
			t.Fatalf("第 %d 条 Seq=%d 没有严格递增（上一条 %d）", i, m.Seq, prev)
		}
		prev = m.Seq

		switch m.Role {
		case "user":
			if m.Text != "" {
				sawUser = true
			}
		case "assistant", "tool":
			// 只要求 role 合法；正文可能为空（纯 tool-call 的 assistant 消息就是没有文字）。
		default:
			t.Fatalf("第 %d 条 Seq=%d 的 Role=%q 不是 user/assistant/tool", i, m.Seq, m.Role)
		}
	}
	if !sawUser {
		t.Fatal("没有任何一条 user 消息带正文——界面打开会话会是空白")
	}

	// 找不到的会话必须报哨兵值，界面靠它区分「会话没了」与「读失败」。
	if _, err := src.Messages("definitely-not-a-session-id"); !errors.Is(err, transport.ErrNoSuchSession) {
		t.Fatalf("不存在的会话应返回 ErrNoSuchSession，实际 %v", err)
	}
}

// B：session.load 的消息投影不能把"只有思考"的回答变成一条空消息。
//
// serve 推的 assistant/message 帧本来就带 reasoning（serve_turn.go 只在正文为空时
// 带它），可读回来时 transport.Message 没有 reasoning 字段——界面上就是一条空气泡，
// 而这次回答的全部内容都在思考块里。断了线重新 load 就等于把这条回答丢了。
//
// 断言按**线上 JSON** 判，不按结构体字段：旧代码没有 Reasoning 字段，
// 这条测试必须能在旧代码上红（而不是编译不过）。
func TestLoadKeepsReasoningOnlyAssistant(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-load-reasoning"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: "先想想"},
		})); err != nil {
			return err
		}
		return lg.Append("assistant/message", session.AssistantMessageEvent(1, 1,
			[]map[string]any{session.ReasoningBlock("先想一下：只有思考，没有正文")},
			"stop", nil))
	})

	msgs, err := newSessionSource(home, cwd).Messages(sid)
	if err != nil {
		t.Fatal(err)
	}
	var got *transport.Message
	for i := range msgs {
		if msgs[i].Role == "assistant" {
			got = &msgs[i]
		}
	}
	if got == nil {
		t.Fatalf("只有 reasoning 的 assistant 被整条丢掉了（%d 条消息）：%+v", len(msgs), msgs)
	}

	raw, merr := json.Marshal(*got)
	if merr != nil {
		t.Fatal(merr)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("解线上消息: %v（%s）", err, raw)
	}
	text, _ := wire["text"].(string)
	reason, _ := wire["reasoning"].(string)
	if strings.TrimSpace(text) == "" && strings.TrimSpace(reason) == "" {
		t.Fatalf("session.load 里这条是空消息（text 与 reasoning 都空）："+
			"serve 推的帧带 reasoning，读回来不能没有：%s", raw)
	}
	if !strings.Contains(reason, "只有思考") {
		t.Fatalf("reasoning 正文没带回来：%s", raw)
	}
}

// 不许误伤：只有 tool_calls 的 assistant 照样算内容。
//
// 它在 load 里的 text 是空的（调用本身不是正文），但**不能因此整条过滤**——
// 工具名/参数挂在下一条 tool/result 的 Tool/Args 上，少了这一条，
// 界面上就只剩一条"没有来源的工具结果"。判据是 assistantHasPayload 的三样占一样。
func TestLoadKeepsToolCallOnlyAssistant(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-load-toolcall"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: "看看目录"},
		})); err != nil {
			return err
		}
		if err := lg.Append("assistant/message", session.AssistantMessageEvent(1, 1,
			[]map[string]any{session.ToolCallBlock("call_1", "bash", `{"command":"ls"}`)},
			"tool_calls", nil)); err != nil {
			return err
		}
		// 真实日志里调用另有一条 tool/call 事件（agent 循环写的），tool/result 的
		// Tool/Args 就是从它补的。
		if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, "call_1", "bash", `{"command":"ls"}`)); err != nil {
			return err
		}
		return lg.Append("tool/result", session.ToolResultEvent(1, 1, "call_1",
			[]spi.Block{{Type: "text", Text: "68 个文件"}}, false, cwd, nil))
	})

	msgs, err := newSessionSource(home, cwd).Messages(sid)
	if err != nil {
		t.Fatal(err)
	}
	sawAssistant, sawTool := false, false
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			sawAssistant = true
		case "tool":
			sawTool = true
			if m.Tool != "bash" || !strings.Contains(m.Args, "ls") {
				t.Fatalf("工具结果没补上调用信息（Tool=%q Args=%q）：%+v", m.Tool, m.Args, msgs)
			}
		}
	}
	if !sawAssistant {
		t.Fatalf("只有 tool_calls 的 assistant 被过滤了——它的工具结果会变成孤立的：%+v", msgs)
	}
	if !sawTool {
		t.Fatalf("工具结果丢了：%+v", msgs)
	}
}

// 不许误伤：真正空的消息（无 text、无 reasoning、无 tool_calls）仍然不算内容。
//
// 旧投影只按"JSON 解得开"就收下，这类事件会变成一条空消息——界面画出空气泡，
// 读盘的人却以为"那次模型说了什么，只是没显示"。
func TestLoadDropsTrulyEmptyAssistant(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	const sid = "session-load-empty"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: "空一轮"},
		})); err != nil {
			return err
		}
		return lg.Append("assistant/message", session.AssistantMessageEvent(1, 1,
			[]map[string]any{{"type": "usage-note", "text": ""}}, "stop", nil))
	})

	msgs, err := newSessionSource(home, cwd).Messages(sid)
	if err != nil {
		t.Fatal(err)
	}
	sawUser := false
	for _, m := range msgs {
		if m.Role == "user" && m.Text == "空一轮" {
			sawUser = true
		}
		if m.Role == "assistant" {
			t.Fatalf("三样都没有的 assistant 仍然算内容（空消息 %+v）；整组消息：%+v", m, msgs)
		}
	}
	if !sawUser {
		t.Fatalf("用户消息不该被这次过滤误伤：%+v", msgs)
	}
}
