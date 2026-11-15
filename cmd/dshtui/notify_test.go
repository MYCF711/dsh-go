package main

// 通知层的行为断言：文本清洗、OSC 字节（含 2048 预算与字素边界）、
// 终端判定表、投递门禁与 windows 降级、设置读写。
//
// 这一层全部用**行为断言**：断言的是"写出去的字节"和"判定的结果"，
// 不是内部实现细节。字节断言逐字节比较（`%q` 报差异），因为通知写偏一个
// 分号或一个终止符，屏幕上都不会报错。

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// notifyRig 是一次通知投递的测试装置：投递器的外部事实全部可控，
// 输出落在一个 bytes.Buffer 上，断言直接比字节。
type notifyRig struct {
	app      *app
	buf      *bytes.Buffer
	notifier *terminalNotifier
	env      map[string]string
	platform string
	tty      bool
	// seq 是喂帧用的单调序号（模拟 journal 里的真实 seq）。
	seq int
}

func newNotifyRig(t *testing.T, method notificationMethod, condition notificationCondition, env map[string]string) *notifyRig {
	t.Helper()
	rig := &notifyRig{
		buf:      &bytes.Buffer{},
		env:      map[string]string{},
		platform: "linux", // auto 走终端判定表；Windows 分支另有专门用例
		tty:      true,
	}
	for k, v := range env {
		rig.env[k] = v
	}
	getenv := func(k string) string { return rig.env[k] }
	n := newTerminalNotifier(notifierFacts{
		getenv:   getenv,
		platform: rig.platform,
		tty:      rig.tty,
		// 焦点走**真实那条路**（app 的 notifyState），测试直接改 a.notify.focusKnown/focused。
		focused: func() bool { return rig.app == nil || rig.app.notify.terminalFocusedNow() },
	}, rig.buf)
	if err := n.configure(condition, method); err != nil {
		t.Fatalf("配置投递器失败：%v", err)
	}
	a := &app{scr: newScreenTo(io.Discard, 120, 36), sid: "s1"}
	a.notify.notifier = n
	a.notify.condition = condition
	a.notify.method = method
	a.resetNotifications("", 0)
	rig.app = a
	rig.notifier = n
	return rig
}

// send 直接走投递器（不经四类构造），返回本次写出的字节。
func (r *notifyRig) send(t *testing.T, title, message string) []byte {
	t.Helper()
	r.buf.Reset()
	note := r.notifier.notify(title, message)
	if note != "" {
		t.Logf("降级说明：%s", note)
	}
	return append([]byte(nil), r.buf.Bytes()...)
}

// ───────────────────────────────────────────────────────────── 文本清洗

func TestNotificationTextFlattensOneLine(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"去 ANSI", "\x1b[31m红\x1b[0m字", "红字"},
		{"多行压一行", "第一行\n\n第二行\t带制表", "第一行 第二行 带制表"},
		{"控制字符换空格", "a\x00b\x07c\x1bd", "a b c d"},
		{"首尾空白去掉", "  前后有空白  ", "前后有空白"},
		{"空白折叠", "多   个    空格", "多 个 空格"},
		{"空串", "", ""},
		{"只有控制字符", "\x00\x07\x1b", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := notificationText(c.in, notificationMaxGraphemes); got != c.want {
				t.Fatalf("notificationText(%q) = %q，want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNotificationTextCapsAt200Graphemes(t *testing.T) {
	// 250 个 CJK 字：200 字素上限，末尾补省略号（省略号自己占一位）。
	in := strings.Repeat("字", 250)
	got := notificationText(in, notificationMaxGraphemes)
	if n := len(graphemeStrings(got)); n != notificationMaxGraphemes {
		t.Fatalf("截断后字素数 = %d，want %d", n, notificationMaxGraphemes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断后要以省略号收尾：%q", got)
	}
	if want := strings.Repeat("字", notificationMaxGraphemes-1) + "…"; got != want {
		t.Fatalf("截断结果不对：%q", got)
	}
}

func TestNotificationTextKeepsEmojiWhole(t *testing.T) {
	// 家庭 emoji 是一个字素（4 个码点 + 3 个 ZWJ）：按字素切不能切出半个。
	family := "👨‍👩‍👧‍👦"
	in := strings.Repeat(family, 201)
	got := notificationText(in, notificationMaxGraphemes)
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果不是合法 UTF-8：%q", got)
	}
	if n := len(graphemeStrings(got)); n != notificationMaxGraphemes {
		t.Fatalf("字素数 = %d，want %d", n, notificationMaxGraphemes)
	}
	if want := strings.Repeat(family, notificationMaxGraphemes-1) + "…"; got != want {
		t.Fatalf("emoji 被切坏了：%q", got)
	}
}

func TestNotificationTextExactlyAtCapHasNoEllipsis(t *testing.T) {
	// 恰好 200 个字素时不加省略号（上游：=== max 时不进截断分支）。
	in := strings.Repeat("字", notificationMaxGraphemes)
	if got := notificationText(in, notificationMaxGraphemes); got != in {
		t.Fatalf("恰好到上限不该动原文：len=%d", len(got))
	}
}

func TestClipToWidthGraphemes(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"放得下就不动", "abc", 10, "abc"},
		{"ASCII 截断", "abcdef", 4, "abc…"},
		{"CJK 占两列", "中文标题", 5, "中文…"},
		{"恰好占满不多截", "中文", 4, "中文"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clipToWidthGraphemes(c.in, c.width); got != c.want {
				t.Fatalf("clipToWidthGraphemes(%q,%d) = %q，want %q", c.in, c.width, got, c.want)
			}
		})
	}
}

func TestTruncateGraphemesUTF8Budgets(t *testing.T) {
	// 每个 CJK 字 3 字节：10 字节预算 = 省略号 3 字节 + 2 个字（6 字节）＝ 9 字节。
	if got := truncateGraphemesUTF8(strings.Repeat("字", 10), 10); got != "字字…" {
		t.Fatalf("按字节截断不对：%q", got)
	}
	// 预算不足 3 字节时不留省略号：不能为了个省略号超预算。
	if got := truncateGraphemesUTF8("abcdef", 2); got != "ab" {
		t.Fatalf("小预算不该补省略号：%q", got)
	}
	// 放得下原样返回（不做任何改写）。
	if got := truncateGraphemesUTF8("短", 99); got != "短" {
		t.Fatalf("放得下要原样返回：%q", got)
	}
	// 绝不切出半个 UTF-8 字符。
	for budget := 1; budget < 20; budget++ {
		got := truncateGraphemesUTF8(strings.Repeat("字", 5), budget)
		if !utf8.ValidString(got) || len(got) > budget {
			t.Fatalf("预算 %d 切出了非法/超长结果：%q (%d 字节)", budget, got, len(got))
		}
	}
}

// ───────────────────────────────────────────────────────────── OSC 字节

func TestOSC777Bytes(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "Ghostty"})
	got := rig.send(t, notificationTitleTaskComplete, "2 个文件已改")
	want := "\x1b]777;notify;dsh-TUI · Task complete;2 个文件已改\x07"
	if string(got) != want {
		t.Fatalf("OSC 777 字节不对：\n got=%q\nwant=%q", got, want)
	}
}

func TestOSC777BodyBudgetExact2048Bytes(t *testing.T) {
	// 整条序列（含标题与协议字节）必须**正好**压到 2048 预算里。
	// 标题 "T"：`ESC]777;notify;T;` + BEL = 16 字节，正文预算 2032；
	// 省略号 3 字节，所以正文 = 2029 个 x + "…"。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	got := rig.send(t, "T", strings.Repeat("x", 5000))
	want := "\x1b]777;notify;T;" + strings.Repeat("x", 2029) + "…\x07"
	if string(got) != want {
		t.Fatalf("2048 预算下的字节不对：got %d 字节 want %d 字节", len(got), len(want))
	}
	if len(got) != notificationMaxOSCBytes {
		t.Fatalf("整条序列 %d 字节，want %d", len(got), notificationMaxOSCBytes)
	}
}

func TestOSC777CJKBodyBudgetAndGraphemeBoundary(t *testing.T) {
	// 短标题下算正文预算，并用 CJK 正文验证"按整字素切"：
	// 标题 "题" → `ESC]777;notify;题;` + BEL = 18 字节 → 正文预算 2030；
	// 省略号 3 字节 → 2027 字节可放字，每个 CJK 字 3 字节 → 675 个字 + 省略号。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	got := string(rig.send(t, "题", strings.Repeat("字", 1000)))
	wantBody := strings.Repeat("字", 675) + "…"
	want := "]777;notify;题;" + wantBody + ""
	if got != want {
		t.Fatalf("CJK 正文的字素切结果不对：got %d 字节 want %d 字节", len(got), len(want))
	}
	if len(got) > notificationMaxOSCBytes {
		t.Fatalf("总字节 %d 超过 2048 预算", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("OSC 777 序列里有半个 UTF-8 字符")
	}
}

func TestOSC777TitleTakesBudgetBeforeBody(t *testing.T) {
	// 标题预算 = 2048 - len(`ESC]777;notify;;BEL`) = 2033 字节，**标题优先**：
	// 700 个 CJK 标题（2100 字节）先被截到 676 字 + 省略号（2031 字节），
	// 这时正文预算只剩 2 字节——放不下一个字，正文为空。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	longTitle := strings.Repeat("题", 700)
	got := string(rig.send(t, longTitle, "正文"))
	wantTitle := strings.Repeat("题", 676) + "…"
	want := "]777;notify;" + wantTitle + ";"
	if titleBudget := notificationMaxOSCBytes - len("]777;notify;;"); titleBudget != 2033 {
		t.Fatalf("标题预算 = %d，want 2033", titleBudget)
	}
	if got != want {
		t.Fatalf("超长标题的截断不对：got %d 字节 want %d 字节", len(got), len(want))
	}
	if len(got) > notificationMaxOSCBytes {
		t.Fatalf("总字节 %d 超过预算", len(got))
	}
}

func TestOSC777LongTitleStillKeepsBodyWhenItFits(t *testing.T) {
	// 300 个 CJK 标题（900 字节，没到标题预算）时正文照样放得下：
	// 正文预算 = 2048 - (13 + 900 + 1 + 1) = 1133 → 1133-3=1130 字节省 → 376 字 + 省略号。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	title := strings.Repeat("题", 300)
	got := string(rig.send(t, title, strings.Repeat("字", 500)))
	wantBody := strings.Repeat("字", 376) + "…"
	want := "]777;notify;" + title + ";" + wantBody + ""
	if got != want {
		t.Fatalf("长标题 + 正文的字节预算不对：got %d 字节 want %d 字节", len(got), len(want))
	}
}

func TestOSC777EmojiBudgetNeverSplitsGrapheme(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	family := "👨‍👩‍👧‍👦" // 25 字节
	got := string(rig.send(t, "T", strings.Repeat(family, 200)))
	if len(got) > notificationMaxOSCBytes {
		t.Fatalf("总字节 %d 超过预算", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("emoji 正文切出了半个字符")
	}
	body := strings.TrimSuffix(strings.TrimPrefix(got, "\x1b]777;notify;T;"), "\x07")
	// 每个完整的家庭 emoji 有 3 个 ZWJ：切出来的正文里 ZWJ 数必须是 3 的倍数。
	if zwj := strings.Count(body, "\u200d"); zwj%3 != 0 {
		t.Fatalf("正文里有半个家庭 emoji（ZWJ 数 %d）", zwj)
	}
	if !strings.HasSuffix(body, "…") {
		t.Fatalf("截断后要有省略号：%q", body)
	}
}

func TestOSC99Bytes(t *testing.T) {
	// Kitty：标题一条、正文一条（单块 → d=1），终止符是 ST（不是 BEL，避免响铃）。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "kitty"})
	got := rig.send(t, notificationTitleApproval, "会话：bash")
	want := "\x1b]99;i=1:d=0:p=title;dsh-TUI · Approval needed\x1b\\" +
		"\x1b]99;i=1:d=1:p=body;会话：bash\x1b\\"
	if string(got) != want {
		t.Fatalf("OSC 99 字节不对：\n got=%q\nwant=%q", got, want)
	}
}

func TestOSC99BodyChunking(t *testing.T) {
	// Kitty 每条 OSC 的负载上限 2048 字节：3000 个字符要切成两块，
	// 只有最后一块标 d=1（否则终端会当成两条通知）。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "kitty"})
	got := string(rig.send(t, "T", strings.Repeat("a", 3000)))
	want := "\x1b]99;i=1:d=0:p=title;T\x1b\\" +
		"\x1b]99;i=1:d=0:p=body;" + strings.Repeat("a", notificationMaxOSCBytes) + "\x1b\\" +
		"\x1b]99;i=1:d=1:p=body;" + strings.Repeat("a", 3000-notificationMaxOSCBytes) + "\x1b\\"
	if got != want {
		t.Fatalf("OSC 99 分块不对：got %d 字节", len(got))
	}
}

func TestOSC99IDIncrementsPerNotification(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "kitty"})
	first := string(rig.send(t, "A", ""))
	second := string(rig.send(t, "B", ""))
	if !strings.Contains(first, "i=1:") {
		t.Fatalf("第一条通知 id 应该是 1：%q", first)
	}
	if !strings.Contains(second, "i=2:") {
		t.Fatalf("第二条通知 id 应该是 2（否则终端会把两条合成一条）：%q", second)
	}
}

func TestOSC9TitlePrefixAndBudget(t *testing.T) {
	// iTerm2：OSC 9 没有独立标题字段，标题只能当正文前缀。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "iTerm.app"})
	got := rig.send(t, notificationTitleQuestion, "确认：要继续吗")
	want := "\x1b]9;dsh-TUI · Waiting for your answer: 确认：要继续吗\x07"
	if string(got) != want {
		t.Fatalf("OSC 9 字节不对：\n got=%q\nwant=%q", got, want)
	}
}

func TestOSC9WithoutBodyIsTitleOnly(t *testing.T) {
	// 无回复的完成通知：正文为空时 OSC 9 只带标题，不补一个空冒号。
	rig := newNotifyRig(t, notifMethodOSC9, notifAlways, nil)
	got := rig.send(t, notificationTitleTaskComplete, "")
	want := "\x1b]9;dsh-TUI · Task complete\x07"
	if string(got) != want {
		t.Fatalf("空正文的 OSC 9 不对：\n got=%q\nwant=%q", got, want)
	}
}

func TestOSC9BudgetExact(t *testing.T) {
	// OSC 9 的预算 = 2048 - len(`ESC]9;BEL`) = 2043，正文含 `标题: ` 前缀。
	rig := newNotifyRig(t, notifMethodOSC9, notifAlways, nil)
	got := rig.send(t, "T", strings.Repeat("y", 4000))
	if len(got) != notificationMaxOSCBytes {
		t.Fatalf("OSC 9 整条序列 %d 字节，want %d", len(got), notificationMaxOSCBytes)
	}
	// 负载预算 = 2048 - len(`ESC]9;BEL`) = 2043；负载 = "T: " + N 个 y + "…"。
	payloadPrefix := "T: "
	want := "\x1b]9;" + payloadPrefix + strings.Repeat("y", 2043-len(payloadPrefix)-3) + "…\x07"
	if string(got) != want {
		t.Fatalf("OSC 9 预算字节不对：%d vs %d", len(got), len(want))
	}
}

func TestBELDeliveryIsRawBell(t *testing.T) {
	// 其它终端：只响铃，不携带任何标题正文（上游同义）。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "vscode"})
	if got := rig.send(t, notificationTitleTaskComplete, "正文"); string(got) != "\x07" {
		t.Fatalf("BEL 投递必须只有一个字节，实际 %q", got)
	}
	// 显式 bel 档在任何终端都一样。
	rig = newNotifyRig(t, notifMethodBEL, notifAlways, map[string]string{"TERM_PROGRAM": "Ghostty"})
	if got := rig.send(t, "T", "正文"); string(got) != "\x07" {
		t.Fatalf("显式 bel 档必须是裸 BEL，实际 %q", got)
	}
}

func TestOSCCControlCharactersStripped(t *testing.T) {
	// 标题里混进 BEL/ESC 会把序列提前终止（甚至注入另一条序列），必须删掉。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	got := string(rig.send(t, "a\x07b\x1bc", "正文"))
	want := "\x1b]777;notify;abc;正文\x07"
	if got != want {
		t.Fatalf("控制字符没被删干净：%q", got)
	}
	if n := strings.Count(got, "\x07"); n != 1 {
		t.Fatalf("序列里出现了 %d 个 BEL，只能有终止符那一个", n)
	}
}

func TestOSC777TitleSemicolonReplaced(t *testing.T) {
	// OSC 777 用 `;` 分字段：标题里的分号换成空格，否则正文会被读成第二个字段。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	got := string(rig.send(t, "A;B", "C"))
	want := "\x1b]777;notify;A B;C\x07"
	if got != want {
		t.Fatalf("标题分号没替换：%q", got)
	}
}

func TestTmuxPassthroughWrap(t *testing.T) {
	// tmux：整条序列包进 DCS 透传，内部 ESC 要成对转义。
	rig := newNotifyRig(t, notifMethodOSC9, notifAlways, map[string]string{"TMUX": "/tmp/tmux-1000/default,1,0"})
	got := string(rig.send(t, "T", "M"))
	want := "\x1bPtmux;\x1b\x1b]9;T: M\x07\x1b\\"
	if got != want {
		t.Fatalf("tmux 包装不对：\n got=%q\nwant=%q", got, want)
	}
}

func TestScreenPassthroughWrap(t *testing.T) {
	rig := newNotifyRig(t, notifMethodOSC9, notifAlways, map[string]string{"STY": "1234.pts-0.host"})
	got := string(rig.send(t, "T", "M"))
	want := "\x1bP\x1b]9;T: M\x07\x1b\\"
	if got != want {
		t.Fatalf("screen 包装不对：%q", got)
	}
}

func TestBELIsNotWrappedInTmux(t *testing.T) {
	// 裸 BEL 才能触发 tmux 的 bell-action；包进 DCS 之后 tmux 看不见响铃。
	rig := newNotifyRig(t, notifMethodBEL, notifAlways, map[string]string{"TMUX": "/tmp/tmux-1000/default,1,0"})
	if got := rig.send(t, "T", "M"); string(got) != "\x07" {
		t.Fatalf("tmux 里的 BEL 必须是裸 0x07，实际 %q", got)
	}
}

// ───────────────────────────────────────────────────────────── 终端判定表

func TestNotificationProtocolTable(t *testing.T) {
	// auto 档的判定表：每个终端名一条（上游 getNotificationProtocol）。
	cases := []struct {
		name        string
		termProgram string
		term        string
		xtversion   string
		want        notificationTransport
	}{
		{"Ghostty", "Ghostty", "xterm-256color", "", transportOSC777},
		{"ghostty 小写", "ghostty", "", "", transportOSC777},
		{"WezTerm", "WezTerm", "xterm-256color", "", transportOSC777},
		{"TERM=xterm-ghostty", "", "xterm-ghostty", "", transportOSC777},
		{"XTVERSION=ghostty", "", "xterm-256color", "ghostty 1.2.3", transportOSC777},
		{"XTVERSION=WezTerm", "", "xterm-256color", "WezTerm 20240203", transportOSC777},
		{"kitty", "kitty", "xterm-kitty", "", transportOSC99},
		{"TERM=xterm-kitty", "", "xterm-kitty", "", transportOSC99},
		{"XTVERSION=kitty", "", "xterm-256color", "kitty(0.32.0)", transportOSC99},
		{"iTerm.app", "iTerm.app", "xterm-256color", "", transportOSC9},
		{"WarpTerminal", "WarpTerminal", "xterm-256color", "", transportOSC9},
		{"XTVERSION=iTerm2", "", "xterm-256color", "iTerm2 3.5.0", transportOSC9},
		{"XTVERSION=Warp", "", "xterm-256color", "WarpTerminal", transportOSC9},
		{"VS Code 一体终端", "vscode", "xterm-256color", "", transportBEL},
		{"未知终端", "", "", "", transportBEL},
		{"词边界不误命中", "", "xterm-256color", "iterminal", transportBEL},
		{"koi 不是 kitty", "", "xterm-256color", "koi-term", transportBEL},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := notificationProtocol(c.termProgram, c.term, c.xtversion); got != c.want {
				t.Fatalf("notificationProtocol(%q,%q,%q) = %s，want %s",
					c.termProgram, c.term, c.xtversion, got, c.want)
			}
		})
	}
}

func TestNotificationTransportPerTerminalBytes(t *testing.T) {
	// 判定表落到**字节**：每个终端名一条，看它到底写出了哪条协议。
	cases := []struct {
		name string
		env  map[string]string
		want string // 序列里必须出现的协议标记
	}{
		{"Ghostty → OSC 777", map[string]string{"TERM_PROGRAM": "Ghostty"}, "\x1b]777;notify;"},
		{"WezTerm → OSC 777", map[string]string{"TERM_PROGRAM": "WezTerm"}, "\x1b]777;notify;"},
		{"kitty → OSC 99", map[string]string{"TERM_PROGRAM": "kitty"}, "\x1b]99;i=1:d=0:p=title;"},
		{"iTerm2 → OSC 9", map[string]string{"TERM_PROGRAM": "iTerm.app"}, "\x1b]9;"},
		{"Warp → OSC 9", map[string]string{"TERM_PROGRAM": "WarpTerminal"}, "\x1b]9;"},
		{"未知终端 → BEL", map[string]string{"TERM": "xterm-256color"}, "\x07"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newNotifyRig(t, notifMethodAuto, notifAlways, c.env)
			got := string(rig.send(t, "T", "M"))
			if !strings.HasPrefix(got, c.want) {
				t.Fatalf("投递路径不对：%q 不以 %q 开头", got, c.want)
			}
		})
	}
}

func TestResolveNotificationTransport(t *testing.T) {
	cases := []struct {
		name     string
		method   notificationMethod
		platform string
		ssh      bool
		protocol notificationTransport
		want     notificationTransport
	}{
		{"auto 本地 Windows → toast", notifMethodAuto, "win32", false, transportOSC9, transportWindows},
		{"auto SSH → 连接终端协议", notifMethodAuto, "win32", true, transportOSC777, transportOSC777},
		{"auto 非 Windows → 探测协议", notifMethodAuto, "linux", false, transportOSC99, transportOSC99},
		{"显式 osc9 覆盖平台", notifMethodOSC9, "win32", false, transportBEL, transportOSC9},
		{"显式 bel 覆盖平台", notifMethodBEL, "win32", false, transportOSC777, transportBEL},
		{"windows 在非 Windows 回落探测协议", notifMethodWindows, "linux", false, transportOSC9, transportOSC9},
		{"windows 在 Windows 是 toast", notifMethodWindows, "win32", false, transportOSC9, transportWindows},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveNotificationTransport(c.method, c.platform, c.ssh, c.protocol)
			if got != c.want {
				t.Fatalf("resolveNotificationTransport = %s，want %s", got, c.want)
			}
		})
	}
}

// ───────────────────────────────────────────────────────────── 门禁与降级

func TestNotificationGate(t *testing.T) {
	cases := []struct {
		name       string
		condition  notificationCondition
		focused    bool
		focusKnown bool
		tty        bool
		wantBytes  bool
	}{
		{"always + 聚焦也发", notifAlways, true, true, true, true},
		{"always + 失焦也发", notifAlways, false, true, true, true},
		{"unfocused + 失焦发", notifWhenUnfocused, false, true, true, true},
		{"unfocused + 聚焦不发", notifWhenUnfocused, true, true, true, false},
		{"off 什么都不发", notifOff, false, true, true, false},
		{"输出被重定向不发", notifAlways, false, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newNotifyRig(t, notifMethodOSC9, c.condition, nil)
			rig.tty = c.tty
			rig.notifier.facts.tty = c.tty
			rig.app.notify.focusKnown = c.focusKnown
			rig.app.notify.focused = c.focused
			rig.buf.Reset()
			rig.app.deliverNotification(kindTaskComplete, "T", "M")
			got := rig.buf.Len() > 0
			if got != c.wantBytes {
				t.Fatalf("门禁判断错误：写出字节=%v，want %v", got, c.wantBytes)
			}
		})
	}
}

func TestUnknownFocusCountsAsFocused(t *testing.T) {
	// 没有焦点上报的终端：焦点"未知"必须按聚焦处理，否则 unfocused 档
	// 会在前台一直弹通知；这样的终端要通知就得显式选 always（上游同义）。
	rig := newNotifyRig(t, notifMethodOSC9, notifWhenUnfocused, nil)
	rig.app.notify.focusKnown = false
	rig.app.deliverNotification(kindTaskComplete, "T", "M")
	if rig.buf.Len() != 0 {
		t.Fatalf("未知焦点按聚焦处理，unfocused 档不该发通知，实际写出 %q", rig.buf.String())
	}
	// 收到一次 focus-out 之后就该发了。
	rig.app.handleSpecial("focus-out")
	rig.app.deliverNotification(kindTaskComplete, "T", "M")
	if rig.buf.Len() == 0 {
		t.Fatalf("失焦之后 unfocused 档应该发通知")
	}
	// 再收到按键（failsafe）= 重新聚焦：不再发。
	rig.buf.Reset()
	rig.app.handleRune('a')
	rig.app.deliverNotification(kindTaskComplete, "T", "M")
	if rig.buf.Len() != 0 {
		t.Fatalf("按键之后视为聚焦，不该再发通知")
	}
}

func TestWindowsMethodDegradesLoudlyToBell(t *testing.T) {
	// `auto` 在本地 Windows 解析成 windows；本版没有 toast 路径，
	// 必须**响铃 + 明确说明**，不许静默。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, nil)
	rig.platform = "win32"
	rig.notifier.facts.platform = "win32"
	rig.buf.Reset()
	note := rig.notifier.notify("T", "M")
	if got := rig.buf.String(); got != "\x07" {
		t.Fatalf("windows 档必须降级为响铃，实际 %q", got)
	}
	if !strings.Contains(note, "未实现") {
		t.Fatalf("降级说明必须明确说未实现，实际 %q", note)
	}
	// 同一条说明只报一次（第二次返回空，避免每来一条通知刷一行）。
	if again := rig.notifier.notify("T", "M"); again != "" {
		t.Fatalf("降级说明应只报一次，第二次仍返回 %q", again)
	}
	if rig.buf.String() != "\x07\x07" {
		t.Fatalf("第二次仍应响铃，实际 %q", rig.buf.String())
	}
}

func TestExplicitWindowsMethodOnNonWindowsUsesProtocol(t *testing.T) {
	// 非 Windows 上 `windows` 是平台能力缺失：回落到探测协议（上游同义）。
	rig := newNotifyRig(t, notifMethodWindows, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	got := string(rig.send(t, "T", "M"))
	if !strings.HasPrefix(got, "\x1b]777;notify;") {
		t.Fatalf("非 Windows 的 windows 档应回落探测协议，实际 %q", got)
	}
}

func TestSSHAutoKeepsTerminalProtocol(t *testing.T) {
	// SSH 会话不该弹本机 toast：auto 走**连接终端**的协议。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{
		"SSH_CONNECTION": "10.0.0.1 50000 10.0.0.2 22",
		"TERM_PROGRAM":   "ghostty",
	})
	rig.platform = "win32"
	rig.notifier.facts.platform = "win32"
	got := string(rig.send(t, "T", "M"))
	if !strings.HasPrefix(got, "\x1b]777;notify;") {
		t.Fatalf("SSH 下的 auto 应走终端协议，实际 %q", got)
	}
}

// ───────────────────────────────────────────────────────────── 设置

func TestLoadTUISettingsDefaults(t *testing.T) {
	dir := t.TempDir()
	settings, warnings := loadTUISettings(filepath.Join(dir, "nope.json"))
	if len(warnings) != 0 {
		t.Fatalf("没有设置文件时不该告警：%v", warnings)
	}
	if settings.condition != notifWhenUnfocused || settings.method != notifMethodAuto {
		t.Fatalf("默认值应为 unfocused/auto，实际 %s/%s", settings.condition, settings.method)
	}
}

func TestLoadTUISettingsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"values":{"taskNotifications":"always","notificationMethod":"osc9"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, warnings := loadTUISettings(path)
	if len(warnings) != 0 {
		t.Fatalf("合法设置不该告警：%v", warnings)
	}
	if settings.condition != notifAlways || settings.method != notifMethodOSC9 {
		t.Fatalf("读到的设置不对：%s/%s", settings.condition, settings.method)
	}
}

func TestLoadTUISettingsWarnsOnInvalidValues(t *testing.T) {
	// 坏值不许静默吞掉：必须回落到默认**并且**给出告警。
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"values":{"taskNotifications":"sometimes","notificationMethod":"toast"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, warnings := loadTUISettings(path)
	if settings.condition != notifWhenUnfocused || settings.method != notifMethodAuto {
		t.Fatalf("坏值应回落默认，实际 %s/%s", settings.condition, settings.method)
	}
	if len(warnings) != 2 {
		t.Fatalf("两个坏值应产生两条告警，实际 %v", warnings)
	}
}

func TestLoadTUISettingsWarnsOnBrokenDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, warnings := loadTUISettings(path)
	if settings.condition != notifWhenUnfocused || len(warnings) == 0 {
		t.Fatalf("坏文档应告警并回落默认，实际 %v / %v", settings, warnings)
	}
}

func TestSaveTUISettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "settings.json")
	in := tuiSettings{condition: notifAlways, method: notifMethodBEL}
	if err := saveTUISettings(path, in); err != nil {
		t.Fatalf("写设置失败：%v", err)
	}
	out, warnings := loadTUISettings(path)
	if len(warnings) != 0 {
		t.Fatalf("回读不该告警：%v", warnings)
	}
	if out != in {
		t.Fatalf("回读不一致：%+v vs %+v", out, in)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"taskNotifications": "always"`, `"notificationMethod": "bel"`, `"version": 1`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("设置文件缺少 %s：%s", want, raw)
		}
	}
}

func TestSettingsPathEnvOverride(t *testing.T) {
	env := map[string]string{settingsFileEnv: "D:/tmp/custom-settings.json"}
	if got := settingsPath(func(k string) string { return env[k] }); got != "D:/tmp/custom-settings.json" {
		t.Fatalf("环境变量应覆盖设置路径，实际 %q", got)
	}
}

func TestCommandSettingsAppliesAndPersists(t *testing.T) {
	dir := t.TempDir()
	rig := newNotifyRig(t, notifMethodAuto, notifWhenUnfocused, nil)
	rig.app.settingsPath = filepath.Join(dir, "settings.json")
	rig.app.settings = defaultTUISettings()

	rig.app.command("/settings taskNotifications=always")
	if rig.app.notify.condition != notifAlways {
		t.Fatalf("设置没有立即生效：%s", rig.app.notify.condition)
	}
	if rig.app.settings.condition != notifAlways {
		t.Fatalf("设置没有写进内存层：%s", rig.app.settings.condition)
	}
	onDisk, warnings := loadTUISettings(rig.app.settingsPath)
	if len(warnings) != 0 || onDisk.condition != notifAlways {
		t.Fatalf("设置没有写盘：%+v %v", onDisk, warnings)
	}

	rig.app.command("/settings notificationMethod=bel")
	if rig.app.notify.method != notifMethodBEL {
		t.Fatalf("投递方式没有立即生效：%s", rig.app.notify.method)
	}
	onDisk, _ = loadTUISettings(rig.app.settingsPath)
	if onDisk.method != notifMethodBEL {
		t.Fatalf("投递方式没有写盘：%s", onDisk.method)
	}
}

func TestCommandSettingsRejectsUnknownValuesAndKeepsOld(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifWhenUnfocused, nil)
	rig.app.command("/settings taskNotifications=sometimes")
	if rig.app.notify.condition != notifWhenUnfocused {
		t.Fatalf("非法值不该改设置，实际 %s", rig.app.notify.condition)
	}
	if !strings.Contains(rig.app.err, "不接受") {
		t.Fatalf("非法值要明确报错，实际 %q", rig.app.err)
	}
}

func TestCommandSettingsRefusesJumpBackExplicitly(t *testing.T) {
	// jumpBack 单独立项（写 HKCU 注册表 + 原生 toast）：给了要明确拒绝，不许装作设上。
	rig := newNotifyRig(t, notifMethodAuto, notifWhenUnfocused, nil)
	rig.app.command("/settings jumpBack=true")
	if !strings.Contains(rig.app.err, "未实现") {
		t.Fatalf("jumpBack 要明确说未实现，实际 %q", rig.app.err)
	}
	if rig.app.settingsPath == "" {
		return // 没有路径时不会写盘，也不必断言
	}
	if _, err := os.Stat(rig.app.settingsPath); err == nil {
		t.Fatalf("拒绝的设置不该被写盘")
	}
}

func TestCommandSettingsShowsResolution(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifWhenUnfocused, map[string]string{"TERM_PROGRAM": "Ghostty"})
	rig.app.command("/settings")
	if len(rig.app.lines) == 0 {
		t.Fatalf("/settings 无参数要显示当前设置")
	}
	joined := strings.Join(rig.app.lines, "\n")
	for _, want := range []string{"taskNotifications=unfocused", "notificationMethod=auto", "osc777"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("/settings 输出缺少 %q：\n%s", want, joined)
		}
	}
}

func TestParseNotificationValues(t *testing.T) {
	for _, c := range []struct {
		in   string
		want notificationCondition
		err  bool
	}{
		{"", notifWhenUnfocused, false},
		{"unfocused", notifWhenUnfocused, false},
		{"always", notifAlways, false},
		{"off", notifOff, false},
		{"sometimes", notifWhenUnfocused, true},
	} {
		got, err := parseNotificationCondition(c.in)
		if (err != nil) != c.err {
			t.Fatalf("parseNotificationCondition(%q) 错误判断不对：%v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("parseNotificationCondition(%q) = %s，want %s", c.in, got, c.want)
		}
	}
	for _, c := range []struct {
		in   string
		want notificationMethod
		err  bool
	}{
		{"", notifMethodAuto, false},
		{"auto", notifMethodAuto, false},
		{"osc9", notifMethodOSC9, false},
		{"bel", notifMethodBEL, false},
		{"windows", notifMethodWindows, false},
		{"toast", notifMethodAuto, true},
	} {
		got, err := parseNotificationMethod(c.in)
		if (err != nil) != c.err {
			t.Fatalf("parseNotificationMethod(%q) 错误判断不对：%v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("parseNotificationMethod(%q) = %s，want %s", c.in, got, c.want)
		}
	}
}

// TestSettingsFileIsNotUpstreamPath 是一条防线断言：本产品不写上游 TUI 的
// `~/.dsh-tui/settings.json`（两个程序共写一个文件迟早互相覆盖）。
func TestSettingsFileIsNotUpstreamPath(t *testing.T) {
	path := settingsPath(nil)
	if path == "" {
		t.Skip("取不到用户目录")
	}
	if strings.Contains(strings.ReplaceAll(path, "\\", "/"), "/.dsh-tui/") {
		t.Fatalf("设置文件不能落在上游 TUI 的 ~/.dsh-tui 下：%s", path)
	}
	if !strings.HasSuffix(strings.ReplaceAll(path, "\\", "/"), ".dsh-go-tui/settings.json") {
		t.Fatalf("设置文件路径不对：%s", path)
	}
}

// TestNotificationBytesAreUTF8 是一条总防线：任何投递方式写出的字节都必须是
// 合法 UTF-8（切坏了就是终端里的替换方块）。
func TestNotificationBytesAreUTF8(t *testing.T) {
	for _, method := range []notificationMethod{notifMethodAuto, notifMethodOSC9, notifMethodBEL} {
		for _, env := range []map[string]string{
			{"TERM_PROGRAM": "ghostty"},
			{"TERM_PROGRAM": "kitty"},
			{"TERM_PROGRAM": "iTerm.app"},
			{"TERM": "xterm-256color"},
		} {
			rig := newNotifyRig(t, method, notifAlways, env)
			got := rig.send(t, "标题😀", strings.Repeat("正文😀", 800))
			if !utf8.Valid(got) {
				t.Fatalf("method=%s env=%v 写出了非法 UTF-8", method, env)
			}
		}
	}
}

func TestNotificationTextIsStableForBytesBufferOutput(t *testing.T) {
	// 输出流是 io.Writer：写失败不能崩，也不能重复写。
	rig := newNotifyRig(t, notifMethodOSC9, notifAlways, nil)
	rig.notifier.out = errorWriter{}
	if got := rig.notifier.notify("T", "M"); got != "" {
		t.Fatalf("写失败不该产生降级说明：%q", got)
	}
}

type errorWriter struct{}

func (errorWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

var _ = bytes.MinRead
