package terminal

// 屏幕模型的判据用例（`internal/terminal/vtscreen.go`）。
//
// 这里的每一条都在钉同一件事：**读出来的是"屏幕上是什么"，不是"按写入顺序把控制
// 序列剥掉之后拼起来是什么"**。两类最能暴露差别的字节流各有一组用例：
//
//	光标定位   `vim` / `htop` 那种 `ESC[r;cH` 逐处重画的整屏刷新
//	回车重画   `\r` 进度条 / 下载百分比
//
// 两组都同时断言"剥序列拼接"得到的是**另一个东西**（`strippedConcat`），这样
// 用例不是"抄了一遍实现的输出"，而是钉住了修复本身。

import (
	"regexp"
	"strings"
	"testing"
)

// ansiPattern 是"剥掉控制序列"的最朴素实现：CSI / OSC / 其它两字节 ESC 序列。
//
// 它代表**旧口径**（按写入顺序拼行），不是产品代码——只用来证明新口径真的不一样。
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// strippedConcat 是旧口径的等价物：剥序列 → 按换行切 → 丢末尾空行。
func strippedConcat(stream string) []string {
	return AgentScreenLines(ansiPattern.ReplaceAllString(stream, ""))
}

func newTestScreen(t *testing.T, cols, rows, scrollback int) *Screen {
	t.Helper()
	s, err := NewScreen(cols, rows, scrollback)
	if err != nil {
		t.Fatalf("建屏幕失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestScreenCursorAddressingReadsScreen 钉住"光标定位读出来是屏幕"。
//
// 字节流模拟 `vim` 的刷新方式：先铺一屏，再逐行定位重写，最后把光标放回第 2 行。
// 旧口径会把这几次重画按顺序拼成一串碎片（7 行），屏幕口径只有 4 行。
func TestScreenCursorAddressingReadsScreen(t *testing.T) {
	stream := strings.Join([]string{
		"\x1b[2J\x1b[H",             // 清屏 + 回家
		"\x1b[1;1HOLD-FIRST-LINE",   // 先写一遍，等一下会被覆盖
		"\x1b[1;1H\x1b[2Knew title", // 定位回第 1 行、清掉该行、重写
		"\x1b[2;1Hbody line 1",
		"\x1b[3;1Hbody line 2",
		"\x1b[4;1Hstatus bar",
		"\x1b[4;1H\x1b[2K~", // 状态行再改一次
		"\x1b[2;3H",         // 光标停在正文第 2 行第 3 列（编辑器里的样子）
	}, "")

	want := []string{"new title", "body line 1", "body line 2", "~"}
	old := strippedConcat(stream)
	if strings.Join(old, "\n") == strings.Join(want, "\n") {
		t.Fatalf("旧口径（剥序列拼接）不该和屏幕口径一致，否则这条用例证明不了任何东西: %#v", old)
	}

	s := newTestScreen(t, 40, 6, 0)
	if err := s.Write([]byte(stream)); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	if got := s.Lines(); !equalLines(got, want) {
		t.Fatalf("屏幕行 = %#v，期望 %#v（旧口径本该给出的碎片: %#v）", got, want, old)
	}
}

// TestScreenCarriageReturnRedrawReadsOneLine 钉住"回车重画只留最后状态"。
//
// 进度条就是 `\r` 反复重画同一行：旧口径会把每个百分比都拼进去（`10%50%100%`），
// 屏幕口径只有最后写的那一个。
func TestScreenCarriageReturnRedrawReadsOneLine(t *testing.T) {
	// 真实进度条重画时都会带「清掉本行」：终端不会替你擦掉上一次的长尾巴。
	stream := "downloading\r\x1b[2K  1%\r\x1b[2K 50%\r\x1b[2K100%\r\x1b[2Kdone\n"
	s := newTestScreen(t, 40, 4, 0)
	if err := s.Write([]byte(stream)); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	// 官方 `lastContentRow` 把**光标所在行**也留下（换行之后光标在新的一行），
	// 所以最后会有一个空行——这是官方口径，不是多余的行。
	want := []string{"done", ""}
	if got := s.Lines(); !equalLines(got, want) {
		t.Fatalf("屏幕行 = %#v，期望 %#v", got, want)
	}
	if old := strippedConcat(stream); !(len(old) == 1 && strings.Contains(old[0], " 1%") && strings.Contains(old[0], "100%")) {
		t.Fatalf("旧口径应当是「每个百分比拼在同一行、连 \\r 都留着」，实际 %#v", old)
	}
}

// TestScreenAlternateScreen 钉住交替屏：进备用屏只读备用屏，退出后回到主屏内容。
func TestScreenAlternateScreen(t *testing.T) {
	s := newTestScreen(t, 30, 5, 0)
	if err := s.Write([]byte("shell history line\r\n$ prompt\r\n")); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	main := s.Lines()
	if err := s.Write([]byte("\x1b[?1049h\x1b[2J\x1b[HALT VIEW\x1b[2;1Hsecond")); err != nil {
		t.Fatalf("进交替屏失败: %v", err)
	}
	if got := s.Lines(); !equalLines(got, []string{"ALT VIEW", "second"}) {
		t.Fatalf("交替屏内容 = %#v，期望 [ALT VIEW second]", got)
	}
	if err := s.Write([]byte("\x1b[?1049l")); err != nil {
		t.Fatalf("出交替屏失败: %v", err)
	}
	if got := s.Lines(); !equalLines(got, main) {
		t.Fatalf("退出交替屏后 = %#v，期望回到 %#v", got, main)
	}
}

// TestScreenLastContentRowStopsAtCursorAndBelowContent 钉住官方 `lastContentRow`
// （`src/screen.ts:10-15`）的取法：从最后一行往上退，退到光标行就停；光标**下面**
// 有内容（编辑器状态行）时不能把它上面的空行也丢掉。
func TestScreenLastContentRowStopsAtCursorAndBelowContent(t *testing.T) {
	s := newTestScreen(t, 20, 6, 0)
	// 内容在第 1 行、状态行在第 5 行，光标停在正文（第 2 行）。
	if err := s.Write([]byte("body\x1b[5;1Hstatus\x1b[2;1H")); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	got := s.Lines()
	want := []string{"body", "", "", "", "status"}
	if !equalLines(got, want) {
		t.Fatalf("屏幕行 = %#v，期望 %#v（第 5 行有内容 → 不能截在第 2 行）", got, want)
	}
}

// TestScreenTrailingBlankBelowCursorTrimmed 钉住 lastContentRow 的另一半：
// 光标下面的空行要被切掉（否则空屏也会返回一整屏空行）。
func TestScreenTrailingBlankBelowCursorTrimmed(t *testing.T) {
	s := newTestScreen(t, 20, 6, 0)
	// 内容在第 1 行，光标挪到第 4 行：0..3 行要给（含中间两行真空行），
	// 第 5、6 行（光标下面）要切掉——所以是 4 行而不是满屏 6 行。
	if err := s.Write([]byte("only line\x1b[4;1H")); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	want := []string{"only line", "", "", ""}
	if got := s.Lines(); !equalLines(got, want) {
		t.Fatalf("屏幕行 = %#v，期望 %#v（光标下面的空行要切掉）", got, want)
	}
}

// TestScreenScrollbackJoinsScreen 钉住"滚回行在屏幕之前、按屏幕顺序"。
func TestScreenScrollbackJoinsScreen(t *testing.T) {
	s := newTestScreen(t, 12, 3, 100)
	for i := 0; i < 6; i++ {
		if err := s.Write([]byte("line" + string(rune('0'+i)) + "\r\n")); err != nil {
			t.Fatalf("喂字节失败: %v", err)
		}
	}
	got := s.Lines()
	// 最后一行之后又一个 CRLF，光标停在空行上，官方口径会带上它。
	want := []string{"line0", "line1", "line2", "line3", "line4", "line5", ""}
	if !equalLines(got, want) {
		t.Fatalf("屏幕行 = %#v，期望滚回+可见屏全部 %#v", got, want)
	}
}

// TestScreenWrapJoinInference 钉住折行推断（`vtscreen.go` 文件头差异 1）：
// 写满整宽的行与下一行接成一条逻辑行（官方 `renderLines` 的 `isWrapped` 行为）。
func TestScreenWrapJoinInference(t *testing.T) {
	wide := strings.Repeat("x", 10)
	s := newTestScreen(t, 10, 5, 100)
	// 第一行正好写满 10 列 → 折行；第二行 3 个字符 → 与第一行同一条逻辑行。
	if err := s.Write([]byte(wide + "abc\r\nshort\r\n")); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	want := []string{wide + "abc", "short", ""}
	if got := s.Lines(); !equalLines(got, want) {
		t.Fatalf("逻辑行 = %#v，期望 %#v（第一行写满 → 折行接上）", got, want)
	}
}

// TestScreenWrapInferenceKnownDivergence 把这个推断的**已知代价**钉在测试里：
// 一行刚好写满整宽之后接一个显式换行时，官方 xterm 判两条逻辑行（`isWrapped` 没置位），
// 本实现按"上一行写满"推断会合成一条。这里断言当前行为，随时改推断就会红。
func TestScreenWrapInferenceKnownDivergence(t *testing.T) {
	wide := strings.Repeat("y", 10)
	s := newTestScreen(t, 10, 5, 100)
	if err := s.Write([]byte(wide + "\r\nnext\r\n")); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	got := s.Lines()
	if !equalLines(got, []string{wide + "next", ""}) {
		t.Fatalf("已知差异的行为变了 = %#v；若推断改成能区分显式换行，请连同 vtscreen.go 文件头差异 1 一起改", got)
	}
}

// TestScreenWideRunesKeepColumns 钉住宽字符（CJK）按 2 列推进：写一个汉字再定位，
// 位置不能差一列（按"一格一列"推进会把它后面的内容整体左移）。
func TestScreenWideRunesKeepColumns(t *testing.T) {
	s := newTestScreen(t, 10, 3, 0)
	// 第一列写"汉"（占 2 列），然后定位到第 4 列写 Z。
	if err := s.Write([]byte("汉\x1b[1;4HZ")); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	got := s.Lines()
	if len(got) != 1 || got[0] != "汉 Z" && got[0] != "汉  Z" {
		t.Fatalf("宽字符行 = %#v，期望「汉」之后第 4 列是 Z", got)
	}
}

// TestScreenSnapshotReplaysIntoSameScreen 钉住 attach 的 `screen` 语义：
// 官方是 `serialize()` 的重放流，消费方写进自己的终端就得到同一屏。
// 这里把快照喂给一个全新屏幕，断言两边逻辑行完全相同。
func TestScreenSnapshotReplaysIntoSameScreen(t *testing.T) {
	stream := strings.Join([]string{
		"\x1b[1;1HTITLE\x1b[2;1H---",
		"\x1b[3;1Hbody\x1b[5;1Hstatus",
		"\x1b[2;1H", // 光标停在正文
	}, "")
	src := newTestScreen(t, 20, 6, 100)
	if err := src.Write([]byte(stream)); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	snap := src.Snapshot(0)
	if snap == "" {
		t.Fatal("快照为空")
	}
	if replayed := ScreenFromSnapshot(snap, 20, 6); !equalLines(replayed, src.Lines()) {
		t.Fatalf("重放后逻辑行 = %#v，期望与源屏一致 %#v（快照 %q）", replayed, src.Lines(), snap)
	}
}

// TestScreenSnapshotAltScreenRoundTrip 钉住交替屏快照也带着切换：重放后仍是交替屏内容。
func TestScreenSnapshotAltScreenRoundTrip(t *testing.T) {
	src := newTestScreen(t, 20, 4, 100)
	if err := src.Write([]byte("main\r\n\x1b[?1049h\x1b[2J\x1b[HALT")); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	snap := src.Snapshot(0)
	if replayed := ScreenFromSnapshot(snap, 20, 4); !equalLines(replayed, src.Lines()) {
		t.Fatalf("交替屏重放 = %#v，期望 %#v（快照 %q）", replayed, src.Lines(), snap)
	}
}

// TestScreenSnapshotByteBoundKeepsNewestRows 钉住快照的字节预算：
// 超限时丢**最上面的行**，剩下的行仍然落在正确位置（不像截断原始字节那样留半个序列）。
func TestScreenSnapshotByteBoundKeepsNewestRows(t *testing.T) {
	src := newTestScreen(t, 20, 8, 0)
	if err := src.Write([]byte("row0\r\nrow1\r\nrow2\r\nrow3\r\nrow4\r\nrow5")); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	snap := src.Snapshot(40)
	if len(snap) > 40 {
		t.Fatalf("快照 %d 字节，超过预算 40", len(snap))
	}
	if !strings.Contains(snap, "row5") {
		t.Fatalf("预算之内的最新行必须留下，快照 = %q", snap)
	}
	if strings.Contains(snap, "row0") {
		t.Fatalf("最旧的行该被丢掉，快照 = %q", snap)
	}
	replayed := ScreenFromSnapshot(snap, 20, 8)
	if len(replayed) == 0 || replayed[len(replayed)-1] != "row5" {
		t.Fatalf("重放后最新行不在最后: %#v", replayed)
	}
}

// TestScreenResizeKeepsContent 钉住 resize 之后内容还在（不重排，见文件头差异 2）。
func TestScreenResizeKeepsContent(t *testing.T) {
	s := newTestScreen(t, 20, 5, 0)
	if err := s.Write([]byte("keep me")); err != nil {
		t.Fatalf("喂字节失败: %v", err)
	}
	if err := s.Resize(30, 8); err != nil {
		t.Fatalf("resize 失败: %v", err)
	}
	if cols, rows := s.Size(); cols != 30 || rows != 8 {
		t.Fatalf("尺寸 = %dx%d，期望 30x8", cols, rows)
	}
	if got := s.Lines(); !equalLines(got, []string{"keep me"}) {
		t.Fatalf("resize 后内容 = %#v，期望 [keep me]", got)
	}
}

// TestScreenClosedRefusesWrites 钉住 Close 之后写入如实失败（不静默吞）。
func TestScreenClosedRefusesWrites(t *testing.T) {
	s, err := NewScreen(10, 3, 0)
	if err != nil {
		t.Fatalf("建屏幕失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := s.Write([]byte("x")); err == nil {
		t.Fatal("Close 之后写入应当报错")
	}
	if got := s.Lines(); got != nil {
		t.Fatalf("Close 之后不该再给行: %#v", got)
	}
}

// TestScreenScrollbackRowsBudget 钉住格子预算：行数被 maxScreenCells 压过。
func TestScreenScrollbackRowsBudget(t *testing.T) {
	if got := screenScrollbackRows(120, 1000); got != 1000 {
		t.Fatalf("120 列应当给满 1000 行，拿到 %d", got)
	}
	if got := screenScrollbackRows(500, 1000); got != maxScreenCells/500 {
		t.Fatalf("500 列应当压到 %d 行，拿到 %d", maxScreenCells/500, got)
	}
	if got := screenScrollbackRows(120, 0); got != 0 {
		t.Fatalf("请求 0 行滚回应当给 0，拿到 %d", got)
	}
}

func equalLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// BenchmarkScreenLines 量"读一页屏幕行"的成本：滚回 1000 行 × 120 列的满屏，
// 每次都要走 120k 个格子。这个数决定 `terminal.read` 能不能被高频调用。
func BenchmarkScreenLines(b *testing.B) {
	s, err := NewScreen(120, 40, 1000)
	if err != nil {
		b.Fatalf("建屏幕失败: %v", err)
	}
	defer func() { _ = s.Close() }()
	line := strings.Repeat("x", 110)
	for i := 0; i < 1100; i++ {
		if err := s.Write([]byte(line + "\r\n")); err != nil {
			b.Fatalf("喂字节失败: %v", err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := s.Lines(); len(got) == 0 {
			b.Fatal("屏幕行为空")
		}
	}
}

// BenchmarkScreenSnapshot 量"取 attach 快照"的成本（只渲染可见屏）。
func BenchmarkScreenSnapshot(b *testing.B) {
	s, err := NewScreen(120, 40, 1000)
	if err != nil {
		b.Fatalf("建屏幕失败: %v", err)
	}
	defer func() { _ = s.Close() }()
	line := strings.Repeat("y", 110)
	for i := 0; i < 200; i++ {
		if err := s.Write([]byte(line + "\r\n")); err != nil {
			b.Fatalf("喂字节失败: %v", err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := s.Snapshot(0); got == "" {
			b.Fatal("快照为空")
		}
	}
}
