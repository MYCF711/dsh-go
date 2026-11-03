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

// ---------------------------------------------------------------- 工具卡折叠

// 默认折叠：tool/call 那一行必须还在，tool/result 的内容不许出现在 view() 里。
// 内容本身仍然要记下来——折叠是显示层的事，不是把历史丢掉。
func TestToolResultCollapsedByDefault(t *testing.T) {
	a := newTestApp(100, 20)
	a.handleFrame(ServerFrame{Type: "tool/call", Data: json.RawMessage(`{"name":"bash","title":"列出目录"}`)})
	a.handleFrame(ServerFrame{Type: "tool/result", Data: json.RawMessage(`{"summary":"退出码 0，输出 12 字节"}`)})

	if a.showToolResults {
		t.Fatal("默认应当是折叠的")
	}
	if !strings.Contains(strings.Join(a.lines, "\n"), "退出码 0") {
		t.Fatal("结果行应当被记下来，展开时要能立刻显示")
	}
	screen := strings.Join(a.view(), "\n")
	if !strings.Contains(screen, "bash") || !strings.Contains(screen, "列出目录") {
		t.Fatalf("折叠后工具卡那一行必须还在：\n%s", screen)
	}
	if strings.Contains(screen, "退出码 0") {
		t.Fatalf("默认折叠时结果内容不该出现在屏幕上：\n%s", screen)
	}
}

// Ctrl+T 展开：已有的结果行立刻出现；再按一次消失。
func TestToggleToolResults(t *testing.T) {
	a := newTestApp(100, 20)
	a.handleFrame(ServerFrame{Type: "tool/call", Data: json.RawMessage(`{"name":"bash","title":"列出目录"}`)})
	a.handleFrame(ServerFrame{Type: "tool/result", Data: json.RawMessage(`{"summary":"退出码 0，输出 12 字节"}`)})

	if strings.Contains(strings.Join(a.view(), "\n"), "退出码 0") {
		t.Fatal("折叠状态下不该看到结果")
	}

	a.handleRune(20) // Ctrl+T
	if !a.showToolResults {
		t.Fatal("Ctrl+T 应当切到展开")
	}
	if !strings.Contains(strings.Join(a.view(), "\n"), "退出码 0") {
		t.Fatalf("展开后结果行没出现：\n%s", strings.Join(a.view(), "\n"))
	}

	a.handleRune(20) // Ctrl+T 再来一次
	if a.showToolResults {
		t.Fatal("再按一次应当切回折叠")
	}
	if strings.Contains(strings.Join(a.view(), "\n"), "退出码 0") {
		t.Fatal("折叠后结果行应当再次消失")
	}

	// /tools 是同一个动作的可打字入口
	a.command("/tools")
	if !a.showToolResults {
		t.Fatal("/tools 应当也能展开")
	}
}

// 错误结果在折叠状态下**必须照样显示**：折叠是省地方，不是把失败藏起来。
// 两种落法（顶层 isError / message.isError）都要认。
func TestErrorResultVisibleWhenCollapsed(t *testing.T) {
	cases := []string{
		`{"isError":true,"summary":"bash: 没有这个文件"}`,
		`{"message":{"isError":true,"content":[{"type":"text","text":"拒绝执行 rm"}]}}`,
	}
	for _, payload := range cases {
		a := newTestApp(100, 20)
		a.handleFrame(ServerFrame{Type: "tool/call", Data: json.RawMessage(`{"name":"bash","title":"rm"}`)})
		a.handleFrame(ServerFrame{Type: "tool/result", Data: json.RawMessage(payload)})

		if a.showToolResults {
			t.Fatal("这条用例要在折叠状态下验")
		}
		joined := strings.Join(a.view(), "\n")
		if !strings.Contains(joined, "ERROR") {
			t.Fatalf("%s：错误结果被折叠藏起来了：\n%s", payload, joined)
		}
		if !strings.Contains(joined, "没有这个文件") && !strings.Contains(joined, "拒绝执行") {
			t.Fatalf("%s：错误正文没显示：\n%s", payload, joined)
		}
	}
}

// 折叠/展开/搜索都不能破坏"每屏恰好高度行"的契约。
func TestViewHeightStableAcrossFoldAndSearch(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 30}, {40, 10}} {
		w, h := size[0], size[1]
		a := newTestApp(w, h)
		for i := 0; i < 40; i++ {
			a.push("正文 " + string(rune('a'+i%26)))
		}
		a.handleFrame(ServerFrame{Type: "tool/call", Data: json.RawMessage(`{"name":"bash","title":"ls"}`)})
		a.handleFrame(ServerFrame{Type: "tool/result", Data: json.RawMessage(`{"summary":"ok"}`)})
		a.handleFrame(ServerFrame{Type: "tool/result", Data: json.RawMessage(`{"isError":true,"summary":"失败"}`)})

		check := func(stage string) {
			got := a.view()
			if len(got) != h {
				t.Fatalf("%dx%d/%s：view 给了 %d 行，应为 %d", w, h, stage, len(got), h)
			}
			for i, ln := range got {
				if n := len([]rune(ln)); n > w {
					t.Fatalf("%dx%d/%s：第 %d 行宽 %d 超过 %d", w, h, stage, i, n, w)
				}
			}
		}
		check("折叠")
		a.handleRune(20)
		check("展开")
		a.handleRune(20)
		check("再折叠")
		a.handleRune(6)
		for _, r := range "正文" {
			a.handleRune(r)
		}
		check("搜索中")
		a.handleSpecial("esc")
		check("退出搜索")
	}
}

// ---------------------------------------------------------------- 会话内搜索

// 搜索：进搜索模式 → 输入关键词 → 状态行报命中数；Esc 退出后不再显示。
// 命中数只算**显示得出来的行**：被折叠的工具结果不参与，否则数字和屏幕对不上。
func TestSearchCountsMatchesAndEscClears(t *testing.T) {
	a := newTestApp(100, 20)
	a.push("第一行 有 needle 在这里")
	a.push("第二行 没有关键词")
	a.push("第三行 也有 Needle") // 大小写不同也要命中

	a.handleFrame(ServerFrame{Type: "tool/call", Data: json.RawMessage(`{"name":"bash","title":"grep"}`)})
	a.handleFrame(ServerFrame{Type: "tool/result", Data: json.RawMessage(`{"summary":"needle 在第 7 行"}`)})

	a.handleRune(6) // Ctrl+F
	if !a.searching {
		t.Fatal("Ctrl+F 应当进入搜索模式")
	}
	if got := a.view(); !strings.HasPrefix(got[len(got)-1], "/ ") {
		t.Fatalf("搜索时输入行前缀应当是 /：%q", got[len(got)-1])
	}
	for _, r := range "needle" {
		a.handleRune(r)
	}
	if a.searchQuery != "needle" {
		t.Fatalf("查询串没跟上输入：%q", a.searchQuery)
	}

	got := a.view()
	status := got[len(got)-2]
	if !strings.Contains(status, "匹配 2 处") {
		t.Fatalf("状态行没报命中数（应为 2 处，折叠的结果行不算）：%q", status)
	}

	a.handleSpecial("esc")
	if a.searching || a.searchQuery != "" || len(a.input) != 0 {
		t.Fatalf("Esc 之后搜索状态没清干净：searching=%v query=%q input=%q",
			a.searching, a.searchQuery, string(a.input))
	}
	after := a.view()
	if strings.Contains(after[len(after)-2], "匹配") {
		t.Fatalf("退出搜索后状态行不该还留着命中数：%q", after[len(after)-2])
	}
	if !strings.HasPrefix(after[len(after)-1], "› ") {
		t.Fatalf("退出搜索后输入行前缀应回到 ›：%q", after[len(after)-1])
	}
}

// 搜索串不是消息：Enter 只退出搜索，不许把它当 prompt 发出去。
func TestSearchEnterDoesNotSend(t *testing.T) {
	a := newTestApp(100, 20)
	a.push("一些内容")
	a.handleRune(6)
	for _, r := range "内容" {
		a.handleRune(r)
	}
	a.handleRune(13) // Enter
	if a.searching || a.searchQuery != "" {
		t.Fatal("Enter 应当退出搜索并清空查询")
	}
	if strings.Contains(strings.Join(a.lines, "\n"), "› 内容") {
		t.Fatal("搜索串被当成消息发出去了")
	}
}

// 超过 maxLines 裁剪之后，meta 必须仍然和 lines 对齐——
// 错位的后果是"折叠藏错行"，屏幕上看起来只是少了一行，极难查。
func TestTrimKeepsMetaAligned(t *testing.T) {
	a := newTestApp(100, 12)
	call := ServerFrame{Type: "tool/call", Data: json.RawMessage(`{"name":"bash","title":"ls"}`)}
	result := ServerFrame{Type: "tool/result", Data: json.RawMessage(`{"summary":"ok"}`)}
	for i := 0; i < 600; i++ {
		a.handleFrame(call)
		a.handleFrame(result)
	}
	if len(a.lines) != maxLines || len(a.meta) != maxLines {
		t.Fatalf("裁剪后 lines=%d meta=%d，应都为 %d", len(a.lines), len(a.meta), maxLines)
	}
	if strings.Contains(strings.Join(a.view(), "\n"), "← ok") {
		t.Fatal("折叠时结果行不该出现")
	}
	a.handleRune(20)
	if !strings.Contains(strings.Join(a.view(), "\n"), "← ok") {
		t.Fatal("展开后结果行没出现——meta 很可能和 lines 错位了")
	}
}

// 键盘解码：ASCII 与 Ctrl 键必须真能发到 handleRune。
//
// 这条是**前置缺陷**的回归测试：旧解码器"等到续字节才解码"，
// 对多字节字符成立，但 ASCII 永远等不到续字节——于是字母、Ctrl+T、
// Ctrl+F、y/a/n（审批）、Ctrl+C（退出）在真机上按了全都没反应，
// 而单测直接调 handleRune，看不出这个问题。
// 同时验证方向键（ESC [ A）被整段吞掉，不会往输入行塞 "[A"。
func TestReadKeysDecodesASCIIAndControlKeys(t *testing.T) {
	rch := make(chan rune, 64)
	sch := make(chan string, 8)
	readKeysFrom(strings.NewReader("ab中\x14\x06\x1b[A\n"), rch, sch)
	close(rch)
	close(sch)

	var got []rune
	for r := range rch {
		got = append(got, r)
	}
	want := []rune{'a', 'b', '中', 20, 6, '\n'}
	if string(got) != string(want) {
		t.Fatalf("解码结果 %q（%v），期望 %q（%v）", string(got), got, string(want), want)
	}

	esc := 0
	for range sch {
		esc++
	}
	if esc != 1 {
		t.Fatalf("ESC 应当只发一次 esc 事件，实际 %d 次", esc)
	}
}

// 上下文进度条：知道上限才画百分比，不知道就只报数字。
// 只给一半信息（有分子没分母）的进度条比没有更糟——数字看着精确，其实分母是猜的。
func TestCtxPercent(t *testing.T) {
	cases := []struct {
		used, window int
		want         string
	}{
		{0, 0, "?"},         // 什么都还不知道
		{12000, 0, "12.0k"}, // 知道用了多少，不知道上限 → 只报数字
		{50000, 100000, "▓▓▓▓▓░░░░░ 50% (50.0k/100.0k)"},    // 两样都知道 → 画条
		{200000, 100000, "▓▓▓▓▓▓▓▓▓▓ 100% (200.0k/100.0k)"}, // 超了也不画成 120%
	}
	for _, c := range cases {
		if got := ctxPercent(c.used, c.window); got != c.want {
			t.Fatalf("ctxPercent(%d,%d) = %q，期望 %q", c.used, c.window, got, c.want)
		}
	}
}
