package main

// 全屏界面主循环：一块屏幕、一个会话、若干帧。
//
// 结构刻意简单：状态都在 app 里，渲染是**纯函数**（app.view() → []string），
// 这样"屏幕长什么样"可以脱开终端单独测——TUI 最难查的 bug 就是布局，
// 而布局一旦藏在绘制调用里就没法断言了。

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type app struct {
	scr  *Screen
	cl   *client
	sid  string
	addr string

	// transcript 是已经"排版好"的行；不做富文本，先把内容跑通。
	lines  []string
	input  []rune
	status string
	tools  int
	busy   bool
	start  time.Time

	// 用量与速率：都来自 session.load，**不自己估**。
	ctxTokens  int
	outTokens  int
	outAtStart int
	tps        float64
	quit       bool
	err        string

	frames <-chan ServerFrame
	// pickFrom 非空时，下一次输入被当作"选第 N 个会话"。
	pickFrom []sessionRow
}

func newApp(addr, token string) (*app, error) {
	cl, err := newClient(addr, token)
	if err != nil {
		return nil, err
	}
	return &app{scr: NewScreen(), cl: cl, addr: addr, start: time.Now()}, nil
}

// run 是主循环。返回 nil 表示正常退出。
func (a *app) run() error {
	a.scr.Enter()
	restore := rawInput()
	defer func() {
		restore()
		a.scr.Leave()
	}()
	setTitle("dsh-TUI (dsh-go)")

	if err := a.newSession(); err != nil {
		return err
	}

	keys := make(chan rune, 32)
	specials := make(chan string, 8)
	go readKeys(keys, specials)

	frames := a.frames
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for !a.quit {
		a.render()
		select {
		case r := <-keys:
			a.handleRune(r)
		case k := <-specials:
			a.handleSpecial(k)
		case f, ok := <-frames:
			if !ok {
				// 流断了：重连由 client 负责，这里只把状态说清楚，不让界面假装还在跑。
				a.status = "连接已断开，正在重连…"
				frames = nil
				continue
			}
			a.handleFrame(f)
		case <-tick.C:
			a.tickStatus()
		}
	}
	return nil
}

// pick 处理"/resume 之后那一次输入"。返回 true 表示这次输入已被消费。
//
// 单独成函数是为了可测：不联网络也能验"越界不切、非数字取消"。
func (a *app) pick(text string) bool {
	if a.pickFrom == nil {
		return false
	}
	n, err := atoiSmall(text)
	if err == nil && n >= 1 && n <= len(a.pickFrom) {
		row := a.pickFrom[n-1]
		a.pickFrom = nil
		if err := a.switchTo(row); err != nil {
			a.err = err.Error()
		}
		return true
	}
	// 不是有效序号：取消选择，把这次输入当普通消息继续
	a.pickFrom = nil
	return false
}

// switchTo 切到另一个已有会话：换订阅，不新建。
func (a *app) switchTo(row sessionRow) error {
	ch, _, err := a.cl.Subscribe(row.ID, 0)
	if err != nil {
		return err
	}
	a.sid = row.ID
	a.frames = ch
	a.lines = nil
	a.tools = 0
	a.ctxTokens, a.outTokens, a.tps = 0, 0, 0
	title := row.Title
	if title == "" {
		title = "(无标题)"
	}
	a.push("已切到 " + short(row.ID) + " — " + trunc(title, 40))
	return nil
}

func atoiSmall(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func (a *app) newSession() error {
	sid, err := a.cl.createSession()
	if err != nil {
		return err
	}
	a.sid = sid
	ch, _, err := a.cl.Subscribe(sid, 0)
	if err != nil {
		return err
	}
	a.frames = ch
	a.push("会话 " + short(sid))
	return nil
}

// handleRune 处理普通字符（含中文：rune 级）。
func (a *app) handleRune(r rune) {
	switch r {
	case 3: // Ctrl+C
		a.quit = true
	case 4: // Ctrl+D
		a.quit = true
	case 127, 8: // Backspace
		if n := len(a.input); n > 0 {
			a.input = a.input[:n-1]
		}
	case 13, 10: // Enter
		a.submit()
	default:
		if r >= 32 {
			a.input = append(a.input, r)
		}
	}
}

func (a *app) handleSpecial(k string) {
	switch k {
	case "esc":
		a.input = nil
	}
}

func (a *app) submit() {
	text := strings.TrimSpace(string(a.input))
	a.input = nil
	if text == "" {
		return
	}
	// /resume 之后输入的数字 = 选第 N 个会话
	if a.pick(text) {
		return
	}
	if strings.HasPrefix(text, "/") {
		a.command(text)
		return
	}
	a.push("› " + text)
	if err := a.cl.prompt(a.sid, text); err != nil {
		a.err = err.Error()
		return
	}
	a.busy = true
	a.outAtStart = a.outTokens
	a.start = time.Now()
}

// command 是最小的一批斜杠命令。**没有实现的不装作有**：明确说"还没有"。
func (a *app) command(text string) {
	switch strings.Fields(text)[0] {
	case "/help":
		a.push("命令：/help 帮助 · /new 新会话 · /clear 清屏 · /quit 退出")
	case "/new":
		if err := a.newSession(); err != nil {
			a.err = err.Error()
		}
	case "/clear":
		a.lines = nil
	case "/quit", "/exit":
		a.quit = true
	case "/resume":
		rows, err := a.cl.listSessions()
		if err != nil {
			a.err = "列会话失败：" + err.Error()
			return
		}
		if len(rows) == 0 {
			a.push("没有可恢复的会话")
			return
		}
		// 只列前 10 条：屏幕上放不下更多，而且这些都是最近的
		for i, r := range rows {
			if i >= 10 {
				a.push(fmt.Sprintf("…还有 %d 条", len(rows)-10))
				break
			}
			title := r.Title
			if title == "" {
				title = "(无标题)"
			}
			a.push(fmt.Sprintf("  %d. %s  %s", i+1, short(r.ID), trunc(title, 50)))
		}
		a.pickFrom = rows
		a.push("输入序号切换会话（例如 2），或直接继续打字取消")
	case "/compact":
		// 宿主有 session.compact 就调；没有就如实说，不假装压过了
		if _, err := a.cl.post("session.compact", map[string]any{"id": a.sid}); err != nil {
			a.err = "这个宿主还没提供 session.compact：" + err.Error()
			return
		}
		a.push("已请求压缩")
	default:
		a.err = "还不支持 " + strings.Fields(text)[0] + "（/help 看现有命令）"
	}
}

// handleFrame 把一帧变成几行字。只做**看得懂**的映射，不猜结构。
func (a *app) handleFrame(f ServerFrame) {
	switch f.Type {
	case "user/message":
		return // 自己刚打的那句已经显示了，避免重复
	case "assistant/message":
		if t := extractText(f.Data); t != "" {
			for _, ln := range strings.Split(strings.TrimRight(t, "\n"), "\n") {
				a.push(ln)
			}
			a.push("")
		}
	case "tool/call":
		a.tools++
		name, title := extractTool(f.Data)
		if title != "" {
			a.push("· " + name + " — " + title)
		} else if name != "" {
			a.push("· " + name)
		}
	case "tool/result":
		if brief := extractBrief(f.Data); brief != "" {
			a.push("  ← " + brief)
		}
	case "turn/error":
		a.push("！错误：" + string(f.Data))
		a.busy = false
	case "turn/done", "turn/end":
		if a.busy {
			if st, err := a.cl.loadStats(a.sid); err == nil {
				a.ctxTokens = st.ContextTokens
				delta := st.OutputTotal - a.outAtStart
				secs := time.Since(a.start).Seconds()
				if secs > 0.2 && delta > 0 {
					a.tps = float64(delta) / secs
				}
				a.outTokens = st.OutputTotal
			}
		}
		a.busy = false
	}
}

func (a *app) push(s string) {
	a.lines = append(a.lines, s)
	// 只留最近 500 行：内存不随会话膨胀；真正的虚拟化是下一步
	if len(a.lines) > 500 {
		a.lines = a.lines[len(a.lines)-500:]
	}
}

func (a *app) tickStatus() {
	if a.busy {
		a.status = fmt.Sprintf("· 工作中 %s", time.Since(a.start).Round(time.Second))
	} else if a.status == "" {
		a.status = "就绪"
	}
}

func (a *app) render() {
	a.scr.Resize()
	a.scr.Render(a.view())
}

// view 是**纯函数**：给定状态，返回这一屏的每一行。
// 这样布局可以被测试断言，而不是靠肉眼看。
func (a *app) view() []string {
	body := a.scr.height - 3
	if body < 1 {
		body = 1
	}
	lines := make([]string, 0, a.scr.height)

	// 正文：只取尾部能放下的部分
	start := 0
	if len(a.lines) > body {
		start = len(a.lines) - body
	}
	lines = append(lines, a.lines[start:]...)
	for len(lines) < body {
		lines = append(lines, "")
	}

	// 状态行
	left := fmt.Sprintf(" %s · 会话 %s · 工具 %d · ctx %s",
		"dsh-go", short(a.sid), a.tools, fmtTokens(a.ctxTokens))
	if a.tps > 0 {
		left += fmt.Sprintf(" · %.1f tok/s", a.tps)
	}
	if a.outTokens > 0 {
		left += fmt.Sprintf(" · 累计 out %s", fmtTokens(a.outTokens))
	}
	if a.busy {
		left += " · 运行中"
	} else if a.status != "" {
		left += " · " + a.status
	}
	if a.err != "" {
		left = " ! " + a.err
	}
	lines = append(lines, strings.Repeat("─", a.scr.width))
	lines = append(lines, fitWidth(left, a.scr.width))

	// 输入行
	prompt := "› " + string(a.input)
	lines = append(lines, fitWidth(prompt, a.scr.width))
	return lines
}

// ---------------------------------------------------------------- 输入

// readKeys 把字节流转成 rune，并把几个控制序列归一成特殊键。
//
// 不做完整行编辑（历史/光标移动）：那是下一步。这里只保证
// "能打字、能删、能回车、能退"——足够验证整条链路。
func readKeys(runes chan<- rune, specials chan<- string) {
	buf := make([]byte, 1)
	var pend []byte
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return
		}
		c := buf[0]
		if c == 0x1b {
			// 转义序列：ESC 单独 = 清输入；ESC [ … 暂时忽略方向键
			specials <- "esc"
			continue
		}
		pend = append(pend, c)
		if c&0xC0 != 0x80 { // UTF-8 起始字节
			continue
		}
		if !utf8Valid(pend) {
			continue
		}
		for _, r := range string(pend) {
			runes <- r
		}
		pend = pend[:0]
	}
}

func utf8Valid(b []byte) bool {
	r := []rune(string(b))
	return len(r) > 0 && string(r) == string(b)
}

// ---------------------------------------------------------------- 小工具

// fmtTokens 把 token 数压成 k 形式。**未知就说未知**（"?"），不要显示 0——
// 0 看起来像"没用量"，而"我不知道"和"没有"是两件事。
func fmtTokens(n int) string {
	if n <= 0 {
		return "?"
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// extractText 从 assisatnt/message 的两种落法里取正文。
func extractText(data json.RawMessage) string {
	var m struct {
		Text    string `json:"text"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	if m.Text != "" {
		return m.Text
	}
	for _, raw := range []json.RawMessage{m.Message.Content, m.Content} {
		if s := joinContent(raw); s != "" {
			return s
		}
	}
	return ""
}

func joinContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" && blk.Text != "" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

func extractTool(data json.RawMessage) (string, string) {
	var m struct {
		Name  string `json:"name"`
		Tool  string `json:"tool"`
		Title string `json:"title"`
	}
	_ = json.Unmarshal(data, &m)
	name := m.Name
	if name == "" {
		name = m.Tool
	}
	return name, m.Title
}

func extractBrief(data json.RawMessage) string {
	var m struct {
		Summary string `json:"summary"`
		Text    string `json:"text"`
		IsError bool   `json:"isError"`
	}
	_ = json.Unmarshal(data, &m)
	s := m.Summary
	if s == "" {
		s = m.Text
	}
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if m.IsError {
		s = "ERROR " + s
	}
	return trunc(s, 80)
}
