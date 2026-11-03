package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// view 是纯函数，所以布局可以被断言——TUI 最难查的 bug 就是布局，
// 藏在绘制调用里就没法测了。
func newTestApp(w, h int) *app {
	s := &Screen{width: w, height: h}
	return &app{scr: s, sid: "session-abcdef12"}
}

// 每屏必须**恰好**是终端高度：多一行会顶掉滚屏，少一行底部会残留上一帧。
func TestViewFillsExactlyTerminalHeight(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 30}, {40, 10}} {
		a := newTestApp(size[0], size[1])
		got := a.view()
		if len(got) != size[1] {
			t.Fatalf("%dx%d：view 给了 %d 行，应为 %d", size[0], size[1], len(got), size[1])
		}
		for i, ln := range got {
			if w := len([]rune(ln)); w > size[0] {
				t.Fatalf("第 %d 行宽 %d 超过终端宽度 %d", i, w, size[0])
			}
		}
	}
}

// 正文很多时只显示尾部：人关心的是最后发生了什么。
func TestViewShowsTailOfTranscript(t *testing.T) {
	a := newTestApp(80, 12)
	for i := 0; i < 100; i++ {
		a.push("行 " + string(rune('A'+i%26)))
	}
	got := a.view()
	if !strings.Contains(strings.Join(got, "\n"), "行 ") {
		t.Fatal("正文没显示")
	}
	// 最上面那几行不该是第 1 条（尾部截取）
	if strings.Contains(got[0], "开始") {
		t.Fatal("似乎显示了头部而不是尾部")
	}
}

// 输入行必须显示当前输入（含中文）。
func TestViewShowsInput(t *testing.T) {
	a := newTestApp(80, 12)
	a.input = []rune("你好 dsh")
	got := a.view()
	if !strings.Contains(got[len(got)-1], "你好 dsh") {
		t.Fatalf("输入行没显示：%q", got[len(got)-1])
	}
}

// 帧映射：assistant/message 的两种落法都要能取到正文。
func TestHandleFrameTextBothShapes(t *testing.T) {
	for _, payload := range []string{
		`{"text":"直接给 text"}`,
		`{"message":{"content":[{"type":"text","text":"包在 message 里"}]}}`,
		`{"content":[{"type":"text","text":"顶层 content"}]}`,
	} {
		a := newTestApp(80, 12)
		a.handleFrame(ServerFrame{Type: "assistant/message", Data: json.RawMessage(payload)})
		if !strings.Contains(strings.Join(a.lines, "\n"), "text") &&
			!strings.Contains(strings.Join(a.lines, "\n"), "包在") &&
			!strings.Contains(strings.Join(a.lines, "\n"), "顶层") {
			t.Fatalf("%s 没被映射成正文：%v", payload, a.lines)
		}
	}
}

// 工具帧要显示成一行卡片摘要，而不是整段 JSON。
func TestHandleFrameToolCard(t *testing.T) {
	a := newTestApp(80, 20)
	a.handleFrame(ServerFrame{Type: "tool/call", Data: json.RawMessage(`{"name":"bash","title":"列出目录"}`)})
	a.handleFrame(ServerFrame{Type: "tool/result", Data: json.RawMessage(`{"summary":"退出码 0，输出 12 字节"}`)})
	joined := strings.Join(a.lines, "\n")
	if !strings.Contains(joined, "bash") || !strings.Contains(joined, "列出目录") {
		t.Fatalf("工具卡没画出来：%s", joined)
	}
	if !strings.Contains(joined, "退出码 0") {
		t.Fatalf("结果摘要没画出来：%s", joined)
	}
	if a.tools != 1 {
		t.Fatalf("工具计数应为 1，实际 %d", a.tools)
	}
}

// 未知命令要明确说不支持，不能静默吞掉——否则人以为命令生效了。
func TestUnknownCommandSaysSo(t *testing.T) {
	a := newTestApp(80, 12)
	a.command("/nope")
	if a.err == "" || !strings.Contains(a.err, "还不支持") {
		t.Fatalf("未知命令没报：%q", a.err)
	}
}

// /clear 清屏；/help 有输出。
func TestSlashCommands(t *testing.T) {
	a := newTestApp(80, 12)
	a.push("一些内容")
	a.command("/help")
	if len(a.lines) == 0 {
		t.Fatal("/help 没有输出")
	}
	a.command("/clear")
	if len(a.lines) != 0 {
		t.Fatalf("/clear 之后还剩 %d 行", len(a.lines))
	}
}
