package terminal

// 终端**屏幕模型**：官方那一支是 `@xterm/headless` 的 `Terminal`，本宿主用
// `github.com/charmbracelet/x/vt` 的 `Emulator`（同一个东西的 Go 实现）。
//
// # 为什么必须有它
//
// 官方 `read` 读的是 xterm 缓冲区（`dsh-api-terminal-controller/src/terminal.ts:276-280`）：
//
//	readScreen(request) {
//	  const buffer = this.screen.buffer.active
//	  return pageLines(renderLines(buffer, 0, lastContentRow(buffer) + 1), request)
//	}
//
// 屏幕是"字节流**应用之后**的状态"，不是字节流本身。只按换行切原始字节的话，
// 光标定位（`ESC[r;cH`）、回车重画（`\r` 进度条）、清屏、交替屏全都读不出来：
// `vim` / `htop` 一屏重画的内容会变成几百行碎片，进度条会变成 `10%50%100%`。
// 所以这一层的唯一职责就是把字节流喂成屏幕，再按官方的取法渲染成逻辑行。
//
// # 官方口径（逐条 file:line）
//
//	renderLines(buffer, from, to)   src/screen.ts:35-49
//	    把**被折行的行**（xterm 的 `isWrapped`）接回一条逻辑行，再 trimEnd。
//	lastContentRow(buffer)          src/screen.ts:10-15
//	    从缓冲区最后一行往上退，退到光标所在行或第一个非空行为止。
//	pageLines(lines, request)       src/screen.ts:57-77（分页在 agenttools.go 里）
//	snapshot = serializer.serialize()   src/terminal.ts:55-56、:100
//	    可见屏的**重放字节流**（`SerializeAddon` 默认 `scrollback:0`），不是文本、
//	    也不是原始输出尾部。
//
// 本文件对应官方 `Screen`（`@xterm/headless` 的 buffer + serializer）这一层。
//
// # 四处如实差异（都是"做不到"或"做不到一样"，不是新语义）
//
//  1. **折行标记是推断的**。xterm 在缓冲区行上带 `isWrapped`；`x/vt` 不留这个位
//     （它只有待折状态 `phantom`，`cc.go:25-45`）。这里按"上一行写满整宽"推断，
//     与真实 `isWrapped` 在这些情况下一致：显式换行后的一行（上一行不满 → 不接）、
//     自动折行（上一行满 → 接）。**唯一不同**：一行**刚好写满整宽后接显式换行**时，
//     官方判为两条逻辑行，本实现会接成一条（本推断的已知代价，由测试钉住）。
//  2. **没有 resize 重排**。xterm 会在 `resize` 时重排折行；`x/vt` 的 `Resize`
//     只改网格尺寸。本宿主 agent 终端不在跑起来之后改尺寸（`session.go` 的
//     `Resize` 是给"用户拖窗口"用的），所以影响面被限制在那条路径上。
//  3. **没有滚动区/原点模式造成的行折叠**（DECSTBM 只是装饰性状态，不影响文本格），
//     逻辑行渲染只看格子内容，与官方同一取法。
//  4. **滚回行数有内存顶**（官方 `scrollback:1000` 只在 xterm 里按行算；这里再多一道
//     格子预算，见 `screenScrollbackRows`）——与 `scrollback.go` 的 `ScrollbackBytes`
//     同源，只是单位从字节换成了格子。

import (
	"fmt"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// maxScreenCells 是屏幕模型滚回区的**格子预算**（120000 格）。
//
// 为什么需要它：`x/vt` 的滚回缓冲是 `[]uv.Cell`，每格 112 字节
// （`unsafe.Sizeof(uv.Cell{})`），1000 行 × 120 列 × 112B ≈ 13 MiB —— 这是
// "真屏幕模型"的固有成本（官方 xterm 也是这个量级），但不能让它随终端宽度无限涨。
// 120000 格 = 在官方默认 120 列下正好 1000 行（官方值），在 500 列（官方
// `maxCols`）下压到 240 行。
const maxScreenCells = 120_000

// Screen 是一条终端的屏幕模型（并发安全；零值不可用）。
//
// 写入口只有 `Write`（PTY 输出字节），读出口只有 `Lines` / `Snapshot`。
// 读是**不加额外同步的整屏取像**：持锁期间不会写进来，所以一次 `Lines` 看到的
// 是同一时刻的屏，不会"上半屏是新的、下半屏是旧的"。
type Screen struct {
	mu   sync.Mutex
	emu  *vt.Emulator
	cols int
	rows int
	// scrollback 是实际生效的滚回行数（已按格子预算压过）。
	scrollback int
	closed     bool
}

// NewScreen 建一个 cols×rows 的屏幕，滚回保留 scrollbackRows 行。
//
// scrollbackRows<=0 表示**不保留滚回**（只留可见屏）；超出格子预算时按
// `maxScreenCells` 压到更少行。
func NewScreen(cols, rows, scrollbackRows int) (*Screen, error) {
	if cols <= 0 || rows <= 0 {
		return nil, fmt.Errorf("%w: 屏幕行列必须为正（拿到 %dx%d）", ErrBadSize, cols, rows)
	}
	s := &Screen{emu: vt.NewEmulator(cols, rows), cols: cols, rows: rows}
	s.scrollback = screenScrollbackRows(cols, scrollbackRows)
	if s.scrollback > 0 {
		s.emu.SetScrollbackSize(s.scrollback)
	}
	return s, nil
}

// screenScrollbackRows 把请求的行数压进格子预算。
func screenScrollbackRows(cols, rows int) int {
	if rows <= 0 || cols <= 0 {
		return 0
	}
	if limit := maxScreenCells / cols; limit < rows {
		if limit < 1 {
			return 1
		}
		return limit
	}
	return rows
}

// Size 返回当前网格尺寸。
func (s *Screen) Size() (cols, rows int) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// Write 把 PTY 输出字节喂进屏幕（与 `Scrollback.Write` 同一股字节）。
//
// 返回错误只有一种：屏幕已经 Close。字节流本身不会被拒绝（终端不能因为
// "看不懂某个序列"就丢数据）。
func (s *Screen) Write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.emu == nil {
		return ErrClosed
	}
	_, _ = s.emu.Write(p)
	return nil
}

// Resize 改网格尺寸（保留已有内容，不重排折行——见文件头差异 2）。
func (s *Screen) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("%w: 屏幕行列必须为正（拿到 %dx%d）", ErrBadSize, cols, rows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.emu == nil {
		return ErrClosed
	}
	s.emu.Resize(cols, rows)
	s.cols, s.rows = cols, rows
	return nil
}

// Lines 返回官方 `renderLines(buffer, 0, lastContentRow+1)` 的逻辑行。
//
// 顺序是**屏幕顺序**：先是滚回行（最旧在前），再是可见屏。空屏返回 nil。
func (s *Screen) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.emu == nil {
		return nil
	}
	texts, full := s.rowsLocked()
	if len(texts) == 0 {
		return nil
	}
	lines := make([]string, 0, len(texts))
	var current strings.Builder
	open := false
	for i, text := range texts {
		if open && i > 0 && full[i-1] {
			current.WriteString(text)
			continue
		}
		if open {
			lines = append(lines, strings.TrimRight(current.String(), " "))
			current.Reset()
		}
		current.WriteString(text)
		open = true
	}
	if open {
		lines = append(lines, strings.TrimRight(current.String(), " "))
	}
	return lines
}

// rowsLocked 取 [滚回行..., 可见屏 0..lastContentRow] 的每行文本与"是否写满整宽"。
//
// 调用方必须持锁。空屏（一行内容都没有且光标在第一行）返回空切片。
func (s *Screen) rowsLocked() ([]string, []bool) {
	alt := s.emu.IsAltScreen()
	var texts []string
	var full []bool
	// 交替屏没有滚回（官方 buffer.active 换成备用缓冲区，`baseY` 为 0）。
	if !alt {
		n := s.emu.ScrollbackLen()
		texts = make([]string, 0, n+s.rows)
		full = make([]bool, 0, n+s.rows)
		for y := 0; y < n; y++ {
			text, isFull := s.scrollbackRowLocked(y)
			texts = append(texts, text)
			full = append(full, isFull)
		}
	} else {
		texts = make([]string, 0, s.rows)
		full = make([]bool, 0, s.rows)
	}
	last := s.lastContentRowLocked()
	if last < 0 {
		return nil, nil
	}
	for y := 0; y <= last; y++ {
		texts = append(texts, s.visibleRowLocked(y))
		full = append(full, s.fullRowLocked(y))
	}
	return texts, full
}

// lastContentRowLocked 是官方 `lastContentRow`（src/screen.ts:10-15）的等价：
// 从可见屏最后一行往上退，退到**光标行**就停（不再往上），中间的空行跳过；
// 光标下面的非空行（例如 vim 的状态行）会让它停下。
//
// 返回 -1 表示整屏没有内容。
func (s *Screen) lastContentRowLocked() int {
	cursorY := s.emu.CursorPosition().Y
	if cursorY < 0 {
		cursorY = 0
	}
	if cursorY >= s.rows {
		cursorY = s.rows - 1
	}
	last := s.rows - 1
	for last > cursorY && s.rowBlankLocked(last) {
		last--
	}
	return last
}

// rowBlankLocked 报告可见屏某行是不是一个格子都没写过。
func (s *Screen) rowBlankLocked(y int) bool {
	for x := 0; x < s.cols; {
		c := s.emu.CellAt(x, y)
		if c == nil {
			break
		}
		if !cellEmpty(c) {
			return false
		}
		x += cellAdvance(c)
	}
	return true
}

// fullRowLocked 报告可见屏某行是不是整宽都**写过内容**（折行推断的唯一依据）。
//
// "写过"用的是 `EmptyCell` 判据：`x/vt` 里没写过的格子就是 `Cell{Content:" ",Width:1}`
// （`ultraviolet/cell.go:12`），写过的空格和它长得一样——所以"整行全是空格"不会被
// 判成写满（宁可少接一行，也不把两条不相干的逻辑行粘起来）。
func (s *Screen) fullRowLocked(y int) bool {
	covered := 0
	for x := 0; x < s.cols; {
		c := s.emu.CellAt(x, y)
		if cellEmpty(c) {
			return false
		}
		w := cellAdvance(c)
		covered += w
		x += w
	}
	return covered >= s.cols
}

// visibleRowLocked 取可见屏一行的文本（空格子按空格，与官方 translateToString 一致；
// 末尾空格由逻辑行上的 TrimRight 去掉）。
func (s *Screen) visibleRowLocked(y int) string {
	var b strings.Builder
	b.Grow(s.cols)
	for x := 0; x < s.cols; {
		c := s.emu.CellAt(x, y)
		if c == nil {
			b.WriteByte(' ')
			x++
			continue
		}
		if c.Content == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Content)
		}
		x += cellAdvance(c)
	}
	return b.String()
}

// scrollbackRowLocked 取一条滚回行的文本与"是否写满整宽"。
//
// `x/vt` 的滚回行已经剪掉了尾部空格（`scrollback.go:47-50` 的 Push），所以
// 走到 nil 就是这行到头（不是"这行很空"）。
func (s *Screen) scrollbackRowLocked(y int) (string, bool) {
	var b strings.Builder
	b.Grow(s.cols)
	covered := 0
	for x := 0; x < s.cols; {
		c := s.emu.ScrollbackCellAt(x, y)
		if c == nil {
			break
		}
		if c.Content == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Content)
		}
		w := cellAdvance(c)
		covered += w
		x += w
	}
	return b.String(), covered >= s.cols
}

// cellAdvance 是一个格子的**推进宽度**：宽字符（CJK）占 2 列，其余至少 1 列。
//
// 必须按 Cell.Width 推进，不能按"一个格子一列"：`x/vt` 用一格存整个宽字符，
// 后面那格是空的占位；按 1 列推进会把它后面的内容整体左移一格。
func cellAdvance(c *uv.Cell) int {
	if c == nil || c.Width < 1 {
		return 1
	}
	return c.Width
}

// cellEmpty 报告一个格子是不是"没写过"（`x/vt` 用同一个值表示空与空格）。
func cellEmpty(c *uv.Cell) bool {
	return c == nil || c.IsZero() || c.Equal(&uv.EmptyCell)
}

// Snapshot 返回官方 `serialize()` 的等价物：**可见屏的重放字节流**。
//
// 官方是 `SerializeAddon.serialize()`（`src/terminal.ts:104`，默认 `scrollback:0`）：
// 使用者（GUI 的 xterm）把它写进去，就得到和宿主一模一样的可见屏。所以这里
// 输出的是"先复位、再逐行定位涂色"的字节流，不是文本、也不是输出尾部。
//
// maxBytes<=0 表示不限；超限时**丢掉最上面的行**（每行自带定位前缀，所以丢掉
// 上面的行之后剩下的行仍然落在正确位置，不会像截断原始字节那样留下半个序列）。
func (s *Screen) Snapshot(maxBytes int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.emu == nil {
		return ""
	}
	rows := strings.Split(s.emu.Render(), "\n")
	pos := s.emu.CursorPosition()
	// 前后固定部分先算出来：复位 + 可能的交替屏切换 + 清屏 + 光标落点 + 光标可见性。
	var head strings.Builder
	head.WriteString("\x1b[0m")
	if s.emu.IsAltScreen() {
		// 交替屏要先切过去再涂：屏幕模型自己是交替屏状态，重放也必须落在同一块缓冲区。
		head.WriteString("\x1b[?1049h")
	}
	head.WriteString("\x1b[H\x1b[2J")
	var tail strings.Builder
	fmt.Fprintf(&tail, "\x1b[%d;%dH", pos.Y+1, pos.X+1)
	if s.emu.CursorHidden() {
		tail.WriteString("\x1b[?25l")
	} else {
		tail.WriteString("\x1b[?25h")
	}
	budget := -1
	if maxBytes > 0 {
		budget = maxBytes - head.Len() - tail.Len()
		if budget < 0 {
			budget = 0
		}
	}
	// 只涂**非空行**：空白行在清屏之后本来就是空白，涂它除了费字节没有别的作用
	// （官方 serializer 也不为空白行输出东西）。
	kept := make([]int, 0, len(rows))
	used := 0
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i] == "" {
			continue
		}
		// 行定位前缀 `ESC[<row>;1H` 最多 9 字节（row <= 500）。
		cost := len(rows[i]) + 9
		if budget >= 0 && used+cost > budget {
			break
		}
		used += cost
		kept = append(kept, i)
	}
	var b strings.Builder
	b.Grow(head.Len() + used + tail.Len())
	b.WriteString(head.String())
	for n := len(kept) - 1; n >= 0; n-- {
		i := kept[n]
		if i > 0 {
			fmt.Fprintf(&b, "\x1b[%d;1H", i+1)
		}
		b.WriteString(rows[i])
	}
	b.WriteString(tail.String())
	return b.String()
}

// Close 释放屏幕模型；之后 Write/Lines/Snapshot 都不会再改状态。
func (s *Screen) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.emu != nil {
		_ = s.emu.Close()
	}
	return nil
}

// ScreenFromSnapshot 把一段 `Snapshot` 字节流重放进一个临时屏幕，并取回它的逻辑行。
//
// 用途只有一个：宿主重启后 / 终端已关闭时，`entry.screen` 里只剩重放字节流，
// `read` 仍然要能按屏幕口径给出行（官方在控制器里保留的是活缓冲区，这里保留
// 的是它的等价重放流）。
func ScreenFromSnapshot(snapshot string, cols, rows int) []string {
	if snapshot == "" || cols <= 0 || rows <= 0 {
		return nil
	}
	scr, err := NewScreen(cols, rows, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = scr.Close() }()
	if err := scr.Write([]byte(snapshot)); err != nil {
		return nil
	}
	return scr.Lines()
}
