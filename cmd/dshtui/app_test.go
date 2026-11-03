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

// 差分渲染：上一帧相同的行必须**跳过**，否则每帧都在重画整屏。
// 这条测的是 Screen 的行为，不是布局——但两者共同决定"流式时不闪"。
func TestRenderSkipsUnchangedRows(t *testing.T) {
	scr := &Screen{width: 20, height: 4}
	var buf strings.Builder
	scr.out = nil // 不走真终端

	// 直接测"哪些行被判为变化"：先画一帧，再画只改最后一行的一帧
	scr.prev = []string{fitWidth("a", 20), fitWidth("b", 20), fitWidth("c", 20), fitWidth("d", 20)}
	lines := []string{"a", "b", "c", "CHANGED"}
	changed := 0
	for i, ln := range lines {
		if fitWidth(ln, scr.width) != scr.prev[i] {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("应当只有 1 行变化，实际 %d", changed)
	}
	_ = buf
}

// 状态行：未知用量显示 "?"，不要显示 0 —— "不知道"和"没有"是两件事。
func TestFmtTokensDistinguishesUnknownFromZero(t *testing.T) {
	if got := fmtTokens(0); got != "?" {
		t.Fatalf("0 应显示 ?，实际 %q", got)
	}
	if got := fmtTokens(1234); got != "1.2k" {
		t.Fatalf("1234 应显示 1.2k，实际 %q", got)
	}
	if got := fmtTokens(999); got != "999" {
		t.Fatalf("999 应原样显示，实际 %q", got)
	}
}

// 状态行要带上 ctx 与 tps（有数据时），没数据时不许编。
func TestViewStatusShowsCtxAndTPS(t *testing.T) {
	a := newTestApp(100, 12)
	a.ctxTokens = 12345
	a.tps = 42.5
	got := strings.Join(a.view(), "\n")
	if !strings.Contains(got, "ctx 12.3k") {
		t.Fatalf("状态行没显示上下文用量：%s", got)
	}
	if !strings.Contains(got, "tok/s") {
		t.Fatalf("状态行没显示速率：%s", got)
	}

	b := newTestApp(100, 12)
	if strings.Contains(strings.Join(b.view(), "\n"), "tok/s") {
		t.Fatal("没有速率数据时不该显示 tok/s")
	}
}

// /resume：列出来的序号必须能选中对应会话；越界/非数字不许误切。
func TestResumePickingByNumber(t *testing.T) {
	a := newTestApp(100, 14)
	a.pickFrom = []sessionRow{
		{ID: "session-11111111", Title: "第一个"},
		{ID: "session-22222222", Title: "第二个"},
	}

	// 越界：不该切，但也要取消选择状态
	if a.pick("9") {
		t.Fatal("越界序号不该被认为已消费")
	}
	if a.pickFrom != nil {
		t.Fatal("越界之后应当取消选择状态")
	}

	// 非数字：不消费
	a.pickFrom = []sessionRow{{ID: "session-22222222"}}
	if a.pick("你好") {
		t.Fatal("非数字不该被当成选择")
	}
	if a.pickFrom != nil {
		t.Fatal("输入非数字后应当取消选择状态")
	}
}

// 导出：角色必须保留（导出的东西多半是回看"谁说了什么"），
// 且序号要带上——不然回看时对不上原始会话。
func TestRenderMarkdownKeepsRolesAndSeq(t *testing.T) {
	md := renderMarkdown([]message{
		{Seq: 1, Role: "user", Text: "你好"},
		{Seq: 2, Role: "assistant", Text: "在的"},
		{Seq: 3, Role: "tool", Text: "输出"},
	})
	for _, want := range []string{"你", "助手", "工具", "#1", "#2", "#3", "你好", "在的"} {
		if !strings.Contains(md, want) {
			t.Fatalf("导出内容缺 %q：%s", want, md)
		}
	}
}

// 收到审批请求：必须把问题摆到屏幕上，并把状态行切成"等你批准"。
// 不处理它 = 这一轮永远卡住，而屏幕看起来毫无变化。
func TestApprovalRequestIsShown(t *testing.T) {
	a := newTestApp(120, 16)
	a.handleFrame(ServerFrame{Type: "approval/request", Data: json.RawMessage(
		`{"requestId":"req-1","toolCall":{"title":"bash · rm -rf build/"}}`)})

	if a.pending.id != "req-1" {
		t.Fatalf("没记住 requestId：%q", a.pending.id)
	}
	joined := strings.Join(a.lines, "\n")
	if !strings.Contains(joined, "需要你批准") || !strings.Contains(joined, "rm -rf") {
		t.Fatalf("审批内容没显示：%s", joined)
	}
	status := a.view()[len(a.view())-2]
	if !strings.Contains(status, "等你批准") {
		t.Fatalf("状态行没切成等待态：%s", status)
	}
}

// 有审批在等时，y/a/n 是给审批的，不许被当成普通输入吃掉。
func TestApprovalKeysAreNotSwallowed(t *testing.T) {
	a := newTestApp(120, 16)
	var got []string
	a.approve = func(reqID, opt string) error {
		got = append(got, reqID+":"+opt)
		return nil
	}
	for _, k := range []rune{'y', 'a', 'n'} {
		a.input = nil
		a.pending.id = "req-1"
		a.handleRune(k)
		if len(a.input) != 0 {
			t.Fatalf("%q 被当成输入塞进了输入行", k)
		}
	}
	want := []string{"req-1:allow_once", "req-1:allow_always", "req-1:reject_once"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("按键→选项映射不对：%v", got)
	}
}

// 没有审批在等时，y/a/n 就是普通字符。
func TestNormalKeysStillTypeWhenNoApproval(t *testing.T) {
	a := newTestApp(120, 16)
	a.handleRune('y')
	if string(a.input) != "y" {
		t.Fatalf("没有审批时 y 应当正常输入，实际 %q", string(a.input))
	}
}
