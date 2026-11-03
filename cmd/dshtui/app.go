package main

// 全屏界面主循环：一块屏幕、一个会话、若干帧。
//
// 结构刻意简单：状态都在 app 里，渲染是**纯函数**（app.view() → []string），
// 这样"屏幕长什么样"可以脱开终端单独测——TUI 最难查的 bug 就是布局，
// 而布局一旦藏在绘制调用里就没法断言了。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

type app struct {
	scr  *Screen
	cl   *client
	sid  string
	addr string

	// transcript 是已经"排版好"的行；不做富文本，先把内容跑通。
	lines []string
	// meta 与 lines 一一对应，记"这一行是什么"。
	//
	// 为什么不直接把结果行删掉：折叠必须可逆。删掉就得在展开时重放历史帧，
	// 而重放会重新触发计数/审批等副作用。所以内容是照收的，只在**显示**时过滤——
	// 过滤发生在 view() 里，展开只是翻一个 bool。
	meta   []lineMeta
	input  []rune
	status string
	tools  int
	busy   bool
	start  time.Time

	// showToolResults 是工具结果行的展开开关：默认 false（折叠）。
	showToolResults bool
	// searching/searchQuery 是会话内搜索：在**显示得出来的行**里找子串。
	// 不做高亮（差分渲染下着色一错就很难查），只报命中数。
	searching   bool
	searchQuery string

	// 用量与速率：都来自 session.load，**不自己估**。
	ctxTokens  int
	ctxWindow  int
	outTokens  int
	outAtStart int
	tps        float64
	quit       bool
	err        string

	frames <-chan ServerFrame
	// pickFrom 非空时，下一次输入被当作"选第 N 个会话"。
	pickFrom []sessionRow

	// pending 是正在等回答的审批（requestId + 给人看的一行标题）。
	//
	// 为什么界面必须处理它：宿主在 ask 策略下会一直等，客户端不回答这一轮就卡死，
	// 而屏幕上什么都没变——人只会以为"模型卡住了"。
	pending struct {
		id    string
		title string
	}
	// approve 是发审批应答的那个动作；测试里替换它，生产里走 client。
	approve func(requestID, option string) error
}

// lineMeta 是"这一行从哪来"的标记，跟着 lines 一起走。
//
// 折叠只看它，不看文本内容——靠前缀字符串猜"这行是不是工具结果"
// 会在模型正文恰好以 "  ← " 开头时误伤，那是查不出来的错。
type lineMeta struct {
	// toolResult 表示这是 tool/result 的摘要行（折叠时默认不显示）。
	toolResult bool
	// isError 表示这次工具调用失败了。**错误永远显示**：
	// 把失败折叠起来，等于让人以为工具成功了。
	isError bool
}

// maxLines 是内存里保留的正文行数：不随会话无限涨，翻页/滚动是下一步的事。
const maxLines = 500

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
	a.meta = nil
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
// answerApproval 回一次审批。y=这一次 · a=以后都允许 · n=拒绝。
func (a *app) answerApproval(option string) {
	id := a.pending.id
	if id == "" {
		return
	}
	a.pending.id = ""
	approve := a.approve
	if approve == nil {
		approve = func(reqID, opt string) error {
			_, err := a.cl.post("session.approve", map[string]any{
				"requestId": reqID, "optionId": opt,
			})
			return err
		}
	}
	if err := approve(id, option); err != nil {
		a.err = "回答审批失败：" + err.Error()
		return
	}
	a.push("  → " + option)
}

// 按键：
//
//	Ctrl+C / Ctrl+D   退出
//	Ctrl+T            展开 / 折叠工具结果（默认折叠）
//	Ctrl+F            进入会话内搜索（Esc 退出）
//	Enter             发送；搜索模式下退出搜索
//	Backspace         删一个字；搜索模式下删空后再按 = 退出搜索
//
// 为什么是 Ctrl+T / Ctrl+F 而不是裸 t / 裸 /：输入行是**同一块键盘**，
// 裸 t 会把"测试一下"的 t 吃掉，裸 / 会让 /help 这类命令永远打不出来。
// 单字母快捷键要配"普通模式/输入模式"两态才安全，那是下一步的事。
func (a *app) handleRune(r rune) {
	// 有审批在等时，y/a/n 先给审批用——这时候打字没有意义。
	if a.pending.id != "" {
		switch r {
		case 'y', 'Y':
			a.answerApproval("allow_once")
			return
		case 'a', 'A':
			a.answerApproval("allow_always")
			return
		case 'n', 'N':
			a.answerApproval("reject_once")
			return
		}
	}
	// 搜索模式下按键先归搜索：除了退出，别的都进查询串。
	if a.searching {
		a.handleSearchRune(r)
		return
	}
	switch r {
	case 3: // Ctrl+C
		a.quit = true
	case 4: // Ctrl+D
		a.quit = true
	case 6: // Ctrl+F
		a.enterSearch()
	case 20: // Ctrl+T
		a.toggleToolResults()
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

// handleSearchRune 处理搜索模式下的按键。
//
// 查询实时生效（每敲一个字更新命中数）：搜索要的是"边打边看还有几处"，
// 按回车才出结果会让人多按一次键才知道词对不对。
func (a *app) handleSearchRune(r rune) {
	switch r {
	case 3, 4: // Ctrl+C / Ctrl+D：退出程序永远有效
		a.quit = true
	case 20: // Ctrl+T：搜索时也能展开工具结果（命中数会跟着变）
		a.toggleToolResults()
	case 127, 8: // Backspace
		if n := len(a.input); n > 0 {
			a.input = a.input[:n-1]
			a.syncSearch()
			return
		}
		// 查询删空了再按一次退格 = 退出搜索（不想伸手找 Esc 的人也能退）
		a.exitSearch()
	case 13, 10: // Enter：只退出搜索，**不发送**
		// 搜索串不是消息：把它当 prompt 发出去是最坏的一种手滑。
		a.exitSearch()
	default:
		if r >= 32 {
			a.input = append(a.input, r)
			a.syncSearch()
		}
	}
}

// enterSearch 进搜索模式：输入行清空，前缀从 › 变成 /。
func (a *app) enterSearch() {
	a.searching = true
	a.searchQuery = ""
	a.input = nil
	// 清掉上一次的错误：状态行位置有限，搜索时得让位给命中数
	// （错误本身已经推进正文，不会因为这里清掉就丢）。
	a.err = ""
}

// exitSearch 退出搜索并清掉查询串：状态行不该留下上一次的命中数。
func (a *app) exitSearch() {
	a.searching = false
	a.searchQuery = ""
	a.input = nil
}

func (a *app) syncSearch() {
	a.searchQuery = strings.TrimSpace(string(a.input))
}

// toggleToolResults 翻转折叠开关。展开是**纯显示**动作：
// 行一直在 lines 里，这里只改一个 bool，所以不会重放、不会重复计数。
func (a *app) toggleToolResults() {
	a.showToolResults = !a.showToolResults
	if a.showToolResults {
		a.status = "工具结果已展开（Ctrl+T 收起）"
	} else {
		// 折叠时状态行已经有"结果已折叠 Ctrl+T"了，这里只补最要紧的一句：
		// 错误不会被藏起来——否则人会以为"折叠=看不到失败"。
		a.status = "错误仍会显示"
	}
}

// isHidden 判断第 i 行在当前折叠状态下要不要显示。
// **错误行永远显示**：折叠是省地方，不是把失败藏起来。
func (a *app) isHidden(i int) bool {
	if a.showToolResults {
		return false
	}
	if i < 0 || i >= len(a.meta) {
		return false
	}
	m := a.meta[i]
	return m.toolResult && !m.isError
}

// visibleLines 返回"现在屏幕上该显示的行"。
//
// 搜索和渲染都走它：两边算的是同一份集合，不会出现"报了 3 处但屏上找不到"。
// 折叠时它只是过滤，不复制全量历史（lines 仍是唯一那份）。
func (a *app) visibleLines() []string {
	if a.showToolResults {
		return a.lines
	}
	out := make([]string, 0, len(a.lines))
	for i, ln := range a.lines {
		if a.isHidden(i) {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// matchCount 数命中行数。**大小写不敏感**：终端搜索里人不会记得原文是什么大小写。
func matchCount(lines []string, query string) int {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0
	}
	n := 0
	for _, ln := range lines {
		if strings.Contains(strings.ToLower(ln), q) {
			n++
		}
	}
	return n
}

func (a *app) handleSpecial(k string) {
	switch k {
	case "esc":
		// 搜索里的 Esc = 退出搜索并清掉查询串；别的时候 = 清输入行。
		if a.searching {
			a.exitSearch()
			return
		}
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
		a.push("命令：/help 帮助 · /new 新会话 · /clear 清屏 · /quit 退出 · /tools 折叠/展开工具结果")
		a.push("按键：Ctrl+T 折叠/展开工具结果 · Ctrl+F 搜索会话（Esc 退出） · Esc 清输入")
	case "/new":
		if err := a.newSession(); err != nil {
			a.err = err.Error()
		}
	case "/clear":
		a.lines = nil
		a.meta = nil
	case "/quit", "/exit":
		a.quit = true
	case "/tools":
		// 和 Ctrl+T 同一件事：给不记得快捷键的人一条打字就能走的路。
		a.toggleToolResults()
		if a.showToolResults {
			a.push("工具结果：已展开")
		} else {
			a.push("工具结果：已折叠（错误仍会显示）")
		}
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
	case "/export":
		msgs, err := a.cl.loadMessages(a.sid, 2000)
		if err != nil {
			a.err = "导出失败：" + err.Error()
			return
		}
		if len(msgs) == 0 {
			a.push("这个会话还没有内容可导出")
			return
		}
		path := fmt.Sprintf("dsh-export-%s.md", short(a.sid))
		if err := os.WriteFile(path, []byte(renderMarkdown(msgs)), 0o644); err != nil {
			a.err = "写导出文件失败：" + err.Error()
			return
		}
		a.push(fmt.Sprintf("已导出 %d 条消息到 %s", len(msgs), path))
	case "/compact":
		// 宿主有 session.compact 就调；没有就如实说，不假装压过了。
		res, err := a.cl.post("session.compact", map[string]any{"sessionId": a.sid})
		if err != nil {
			a.err = "压缩没做成：" + err.Error()
			return
		}
		var r struct {
			Compacted bool `json:"compacted"`
			Shadowed  int  `json:"shadowed"`
		}
		_ = json.Unmarshal(res, &r)
		if !r.Compacted {
			// "没到该压的时候"是正常结果，不是失败——别让人以为点坏了
			a.push("现在不需要压缩（会话还没长到该压的时候）")
			return
		}
		a.push(fmt.Sprintf("已压缩：遮蔽 %d 条事件", r.Shadowed))
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
		// 结果行照收（展开时要能立刻显示），但打上"可折叠"的标记。
		// 失败的结果 always-visible：见 isHidden。
		if brief := extractBrief(f.Data); brief != "" {
			a.pushMeta("  ← "+brief, lineMeta{
				toolResult: true,
				isError:    extractIsError(f.Data),
			})
		}
	case "approval/request":
		var req struct {
			RequestID string `json:"requestId"`
			ToolCall  struct {
				Title string `json:"title"`
			} `json:"toolCall"`
		}
		if json.Unmarshal(f.Data, &req) == nil && req.RequestID != "" {
			a.pending.id = req.RequestID
			a.pending.title = req.ToolCall.Title
			a.push("！需要你批准：" + req.ToolCall.Title)
			a.push("  按 y 允许这一次 · a 以后都允许 · n 拒绝")
		}
	case "approval/answered":
		a.pending.id = ""
	case "turn/error":
		a.push("！错误：" + string(f.Data))
		a.busy = false
	case "turn/done", "turn/end":
		if a.busy {
			if st, err := a.cl.loadStats(a.sid); err == nil {
				a.ctxTokens = st.ContextTokens
				a.ctxWindow = st.ContextWindow
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
	a.pushMeta(s, lineMeta{})
}

// pushMeta 追加一行并带上它的来历。裁剪时 lines 与 meta **必须一起裁**，
// 否则下标错位，折叠会藏错行——这种错在屏幕上看起来只是"少了一行"。
func (a *app) pushMeta(s string, m lineMeta) {
	a.lines = append(a.lines, s)
	a.meta = append(a.meta, m)
	// 只留最近 maxLines 行：内存不随会话膨胀；真正的虚拟化是下一步
	if n := len(a.lines); n > maxLines {
		excess := n - maxLines // 丢掉最前面这么多行
		a.lines = a.lines[excess:]
		// meta 必须丢掉**同样的前缀**，否则下标错位、折叠会藏错行。
		// （写成 a.meta[len(a.meta)-excess:] 是错的：那只留下最后 excess 条，
		//  测试 TestTrimKeepsMetaAligned 就是钉这个的。）
		if excess <= len(a.meta) {
			a.meta = a.meta[excess:]
		} else {
			a.meta = nil // 防御：万一有人绕过 push 直接改 lines
		}
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
//
// 折叠与搜索都只影响"取哪几行"和状态行文字，不改变条数：
// 每屏仍**恰好** height 行（上面 body 行 + 分隔线 + 状态行 + 输入行）。
func (a *app) view() []string {
	body := a.scr.height - 3
	if body < 1 {
		body = 1
	}
	lines := make([]string, 0, a.scr.height)

	// 正文：先按折叠状态过滤，再只取尾部能放下的部分。
	// 过滤在尾部截取**之前**做：否则 folded 的行会占掉本该给其它内容的格子。
	visible := a.visibleLines()
	start := 0
	if len(visible) > body {
		start = len(visible) - body
	}
	lines = append(lines, visible[start:]...)
	for len(lines) < body {
		lines = append(lines, "")
	}

	// 状态行
	left := fmt.Sprintf(" %s · 会话 %s · 工具 %d · ctx %s",
		"dsh-go", short(a.sid), a.tools, ctxPercent(a.ctxTokens, a.ctxWindow))
	if a.tps > 0 {
		left += fmt.Sprintf(" · %.1f tok/s", a.tps)
	}
	if a.outTokens > 0 {
		left += fmt.Sprintf(" · 累计 out %s", fmtTokens(a.outTokens))
	}
	if !a.showToolResults && len(visible) < len(a.lines) {
		// 有被折叠的行才提示，没有就不占地方
		left += " · 结果已折叠 Ctrl+T"
	}
	if a.busy {
		left += " · 运行中"
	} else if a.status != "" {
		left += " · " + a.status
	}
	if a.searching {
		// 搜索时状态行只说搜索的事：命中数放在最前面，窄终端也不会被截掉。
		if a.searchQuery == "" {
			left = " 搜索：输入关键词 · Esc 退出"
		} else {
			left = fmt.Sprintf(" 搜索「%s」· 匹配 %d 处 · Esc 退出",
				trunc(a.searchQuery, 20), matchCount(visible, a.searchQuery))
		}
	}
	if a.pending.id != "" {
		left = " ? 等你批准：" + trunc(a.pending.title, 40) + "  [y]允许 [a]以后都允许 [n]拒绝"
	} else if a.err != "" {
		left = " ! " + a.err
	}
	lines = append(lines, strings.Repeat("─", a.scr.width))
	lines = append(lines, fitWidth(left, a.scr.width))

	// 输入行：搜索模式前缀是 /，普通输入仍是 ›
	prompt := "› "
	if a.searching {
		prompt = "/ "
	}
	lines = append(lines, fitWidth(prompt+string(a.input), a.scr.width))
	return lines
}

// ---------------------------------------------------------------- 输入

// readKeys 把字节流转成 rune，并把几个控制序列归一成特殊键。
//
// 不做完整行编辑（历史/光标移动）：那是下一步。这里只保证
// "能打字、能删、能回车、能退"——足够验证整条链路。
func readKeys(runes chan<- rune, specials chan<- string) {
	readKeysFrom(os.Stdin, runes, specials)
}

// readKeysFrom 是 readKeys 的解码部分，单独拆出来是为了能喂字节流做测试：
// 键盘解码错了，屏幕上是"按键没反应"，那是最难查的一类现象。
//
// 这里修掉两个会让按键**完全失效**的问题：
//
//  1. 旧写法"等到续字节才解码"（`c&0xC0 != 0x80 → continue`）对多字节字符成立，
//     但对 ASCII 永远等不到续字节——字母、Ctrl+T、Ctrl+F、y/a/n、Ctrl+C
//     一个都发不出来。改成"凑满一个完整字符就发"（utf8.FullRune）。
//  2. ESC 之后必须把整个转义序列吞掉（ESC [ A 之类），否则按一次方向键
//     会往输入行里塞 "[A"。
func readKeysFrom(in io.Reader, runes chan<- rune, specials chan<- string) {
	buf := make([]byte, 1)
	var pend []byte
	// 上一个字节是 ESC/CSI 前缀时，后续字节属于同一条转义序列，整段吞掉。
	escTail, csiTail := false, false
	for {
		n, err := in.Read(buf)
		if err != nil || n == 0 {
			return
		}
		c := buf[0]
		switch {
		case csiTail:
			// CSI/SS3 的终止字节在 0x40–0x7E（"[A"、"[1;5C"、"OP" 都是）
			if c >= 0x40 && c <= 0x7e {
				csiTail = false
			}
			continue
		case escTail:
			escTail = false
			if c == '[' || c == 'O' {
				csiTail = true
				continue
			}
			// 孤立的 ESC 后面跟的是普通按键：这个按键照常处理，不吞
		case c == 0x1b:
			// 单独一个 ESC 与转义序列前缀没法"等一等"再区分（按键没有超时），
			// 所以立刻当成 esc 发出去：清输入 / 退出搜索。
			specials <- "esc"
			escTail = true
			continue
		}
		pend = append(pend, c)
		if !utf8.FullRune(pend) {
			if len(pend) > utf8.UTFMax {
				pend = pend[:0] // 非法字节串：丢掉，别把后面的正常按键也吃掉
			}
			continue
		}
		for _, r := range string(pend) {
			runes <- r
		}
		pend = pend[:0]
	}
}

// ---------------------------------------------------------------- 小工具

// ctxPercent 画上下文占用：**知道上限才画百分比**，不知道就只报用了多少。
//
// 只给一半信息的进度条比没有更糟：数字看着精确，其实分母是猜的。
func ctxPercent(used, window int) string {
	if used <= 0 {
		return "?"
	}
	if window <= 0 {
		return fmtTokens(used) // 不知道上限：如实报数字
	}
	pct := float64(used) / float64(window) * 100
	if pct > 100 {
		pct = 100
	}
	const width = 10
	filled := int(pct / 100 * width)
	bar := strings.Repeat("▓", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("%s %.0f%% (%s/%s)", bar, pct, fmtTokens(used), fmtTokens(window))
}

// renderMarkdown 把消息列表排成 markdown。**保留角色**：
// 导出的东西多半是拿去回看"谁说了什么"，把角色抹掉就没用了。
func renderMarkdown(msgs []message) string {
	var b strings.Builder
	b.WriteString("导出自 dsh-TUI(dsh-go)\n\n")
	for _, m := range msgs {
		who := m.Role
		switch m.Role {
		case "user":
			who = "你"
		case "assistant":
			who = "助手"
		case "tool":
			who = "工具"
		}
		fmt.Fprintf(&b, "### %s · #%d\n\n%s\n\n", who, m.Seq, strings.TrimRight(m.Text, "\n"))
	}
	return b.String()
}

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
		Summary string          `json:"summary"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
		Message struct {
			Text    string          `json:"text"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal(data, &m)
	s := m.Summary
	if s == "" {
		s = m.Text
	}
	if s == "" {
		// WS 推送（夹具第 6 帧）是顶层 content 块数组，没有顶层 text；
		// v4 事件把同样的东西放在 message.content 里。两种都认，
		// 否则真机上每条结果都会显示成空行——折叠也就等于没做。
		s = joinContent(m.Content)
	}
	if s == "" {
		s = m.Message.Text
	}
	if s == "" {
		s = joinContent(m.Message.Content)
	}
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if extractIsError(data) {
		if s == "" {
			s = "ERROR"
		} else {
			s = "ERROR " + s
		}
	}
	return trunc(s, 80)
}

// extractIsError 取"这次工具调用失败了没有"。两种落法都认：
// 顶层 isError（WS 推送的形状）与 message.isError（v4 事件的形状）。
//
// 为什么要认两种：漏认一种的后果不是"少一个标记"，而是**错误被折叠藏起来**，
// 屏幕上看起来和成功一模一样。
func extractIsError(data json.RawMessage) bool {
	var m struct {
		IsError bool `json:"isError"`
		Message struct {
			IsError bool `json:"isError"`
		} `json:"message"`
	}
	_ = json.Unmarshal(data, &m)
	return m.IsError || m.Message.IsError
}
