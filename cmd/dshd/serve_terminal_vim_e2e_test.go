package main

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"dshgo/internal/terminal"
	"dshgo/internal/transport"
)

// TestTerminalVimRendersAsRealScreen 是终端这条线的**端到端验收**：
// 让一个真实的全屏 TUI（vim）在真 PTY 上跑，断言两件事各自成立：
//
//  1. **attach 快照的 Screen 是重放字节流**（带 ESC）。这是官方 `serialize()` 的等价物
//     （`dsh-api-terminal-controller/src/terminal.ts:55-56`），用途是喂 GUI 的 xterm 重放。
//  2. **`terminal_read` 读到的是解释后的屏幕**（不带 ESC，`~` 按行成列）。
//     这才是 agent 拿来做判断的东西。
//
// 为什么这条必须有：前面每一块（宿主 vtscreen、GUI 网格、resize）各自都有测试，
// 但「两层一起对」没人验过。而把这层搞混的后果很直接 —— 我第一版就断言
// 「Screen 里不该有 ESC」，红了才发现**带 ESC 才是对的**：那是重放流，不是屏幕文本。
func TestTerminalVimRendersAsRealScreen(t *testing.T) {
	cwd := t.TempDir()
	h := newTerminalResizeHarness(t, cwd)
	ctx := context.Background()

	if _, err := h.jt.TerminalCreate(ctx, "sess-vim", transport.OfficialTerminalCreateRequest{
		ID: "tv", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}

	// -u NONE 关用户配置、-N 打开 nocompatible，使屏幕形状确定。
	// 先 echo 再进 vim：ConPTY 处理首帧期间可能吞掉紧随其后的输入字节
	// （与 modules/exec/bash/pty_test.go 的既有教训一致）。
	if err := h.jt.Input("sess-vim", "tv", "echo ready\nvim -u NONE -N e2e-marker.txt\n"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	// 判据必须是 vim 自己画出来的东西。
	// 一开始我用文件名当 marker —— 命令行回显里就有它，快照会在 vim 还没渲染时被取走，
	// 那是假阳性判据。`~` 只能由按行定位绘制产生，才是真判据。
	replay := waitForScreen(t, h, "sess-vim", "tv", "~")

	// ── 第一层：重放流，应该**带** ESC。
	if !strings.ContainsRune(replay, 0x1b) {
		t.Fatalf("attach 快照的重放流里没有 ESC，说明它不是 serialize 等价物：%q", replay)
	}
	// 不要写死行号：vim 从第几行开始画 `~` 取决于屏高和此前的输出。
	// 第一版我写死了 ESC[2;1H~，本地连跑 3 次都过、CI 却红 —— 那是**断言太紧**，
	// 不是产品问题。判据要的是「有光标定位引导的绘制」，行号本身不是判据的一部分。
	if !cursorAddressed(replay) {
		t.Fatalf("重放流里看不到「ESC[行;列H 后紧跟 ~」这种按行定位绘制：%q", replay)
	}

	// ── 第二层：terminal_read，应该**不带** ESC 且是网格形状。
	page, err := h.jt.officialPTYTerminalAPI.AgentTerminalRead(
		context.Background(), "sess-vim", "tv",
		terminal.AgentReadRequest{Offset: 0, Count: 500, MaxReadBytes: 1 << 20})
	if err != nil {
		t.Fatalf("terminal_read 失败：%v", err)
	}
	text := page.Text
	if strings.TrimSpace(text) == "" {
		t.Fatal("terminal_read 返回空 —— 说明没有屏幕模型")
	}
	if strings.ContainsRune(text, 0x1b) {
		t.Fatalf("terminal_read 正文里出现了原始 ESC，说明没经过 VT 解释：%q", text)
	}

	tildeRows := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "~") {
			tildeRows++
		}
	}
	if tildeRows < 3 {
		t.Fatalf("只有 %d 行以 ~ 开头，vim 编辑区形状不对（至少该有多行）：\n%s", tildeRows, text)
	}
	if !strings.Contains(text, "e2e-marker.txt") {
		t.Fatalf("屏幕上看不到 vim 状态行里的文件名：\n%s", text)
	}

	// 收尾：退出 vim，别把进程留着。
	_ = h.jt.Input("sess-vim", "tv", "\x1b:q!\n")
	time.Sleep(100 * time.Millisecond)
}

// waitForScreen 轮询 attach 快照直到屏幕包含 marker。
//
// 与 waitPTYSnapshot 的区别：这里的 marker 必须是**只有渲染后才会出现**的内容。
// 用命令行回显就能命中的字符串当 marker，会在程序还没画之前就取走快照，
// 得到的是「刚敲完命令」的屏，而不是「程序画完」的屏。
func waitForScreen(t *testing.T, h *terminalResizeHarness, sessionID, id, marker string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		snap, err := h.jt.officialPTYTerminalAPI.TerminalAttach(context.Background(), sessionID, id)
		if err != nil {
			t.Fatalf("attach 失败：%v", err)
		}
		last = snap.Screen
		if strings.Contains(last, marker) {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等不到屏幕上出现 %q（最后 screen=%q）", marker, last)
	return last
}

// cursorAddressed 判「重放流里有没有『跳到某行某列、然后画一个 ~』」这种按行定位绘制。
//
// 只要求「行号任意」，因为 vim 具体从哪一行开始画 `~` 取决于屏高与先前的输出；
// 把它写死成某个行号，测的就不是「有没有光标定位」，而是「vim 这一版怎么排版」。
func cursorAddressed(replay string) bool {
	return regexp.MustCompile(`\x1b\[[0-9]+;[0-9]+H~`).MatchString(replay)
}
