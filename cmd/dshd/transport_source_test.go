package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dshgo/codecs/v4"
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
