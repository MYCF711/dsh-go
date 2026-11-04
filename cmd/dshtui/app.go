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

	// scroll 是"离底部多少行"：0 = 贴底跟随新输出，>0 = 正在往回看。
	//
	// 为什么用**尾部锚定**而不是记住首行的绝对下标：正文超过 maxLines 时
	// 从头部裁掉的是**开头**，而"离底部多远"这个量不受裁头影响——同一个
	// scroll 在裁剪前后指着同一段字。记绝对下标就得在每次裁剪时修一次，
	// 少修一次的症状是"回看时内容莫名往前跳一行"。
	scroll int

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

	// pending 是正在等回答的审批（requestId + 给人看的一行标题 + 截止时刻）。
	//
	// 为什么界面必须处理它：宿主在 ask 策略下会一直等，客户端不回答这一轮就卡死，
	// 而屏幕上什么都没变——人只会以为"模型卡住了"。
	pending struct {
		id    string
		title string
		// deadline 是**宿主**给的截止时刻（approval/request 的 deadlineMs）。
		// 零值 = 宿主没报超时（老宿主）：这时界面只显示"等待中"，
		// **不拿本地默认值编一个倒计时**——编出来的数字到点不对，比没有更糟。
		deadline time.Time
	}
	// approve 是发审批应答的那个动作；测试里替换它，生产里走 client。
	approve func(requestID, option string) error

	// now 是时钟。生产里是 time.Now；测试里换成可控的假时钟，
	// 否则"每秒变小""到 0 超时"只能用真等待来验，测试会又慢又不稳。
	now func() time.Time
}

// 审批倒计时的两档显示与阈值。
//
// approvalWarnSeconds：剩余 ≤ 这个数就换成更强的提示（`!! 还有 3s`）。
// 5 秒是个能被"扫一眼"的长度：它大约是"端起杯子放下"的时间，
// 再短就来不及反应，再长又会一直闪、失去"要出事了"的意味。
const approvalWarnSeconds = 5

// approvalTitleWidth 是状态行里标题的截断宽度。
//
// 从 40 收到 30 是因为加了倒计时前缀：80 列终端上 40 字标题会把
// 后面的 `[y]/[a]/[n]` 按键提示挤出屏幕外——**看不见怎么答**比看不见标题更糟。
const approvalTitleWidth = 30

// approvalTimeoutNote 是超时后留在状态行上的那句。
//
// 抽成常量是因为它有两个用处：写到 err，以及"新的一问上来时认出它、把它让位"——
// 两处各写一遍字面量的话，迟早改一处漏一处，症状是状态行上挂着一句过期的"已超时"。
const approvalTimeoutNote = "审批已超时，按拒绝处理"

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
	return &app{scr: NewScreen(), cl: cl, addr: addr, start: time.Now(), now: time.Now}, nil
}

// clock 返回这个 app 的当前时间。now 为空（测试里直接构造的零值 app）时退回真时钟，
// 免得每个用例都必须记得注入。
func (a *app) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// run 是主循环。返回 nil 表示正常退出。
func (a *app) run() error {
	a.scr.Enter()
	restore := rawInput()
	defer func() {
		restore()
		// Leave 连着把窗口标题还回去（进 alt-screen 前的那个），
		// 标题本身由 Screen.Enter 设成产品名——这里不再单独设一次，
		// 否则两处各写一个名字，迟早又改成不一样的。
		a.scr.Leave()
	}()

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
	a.scroll = 0 // 换了会话就没有可回看的旧内容了
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

// answerApproval 回一次审批。y=这一次 · a=以后都允许 · n=拒绝。
//
// 答完立刻清掉整个 pending（**连截止时刻一起**）：倒计时还挂着的话，
// 用户会以为那一下没生效、还在等第二个决定。
func (a *app) answerApproval(option string) {
	id := a.pending.id
	if id == "" {
		return
	}
	a.clearPending()
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

// ---------------------------------------------------------------- 审批倒计时

// clearPending 清掉挂起审批的全部状态（id / 标题 / 截止时刻）。
//
// 只有这一个入口：回答、界面侧超时、宿主 approval/answered、宿主 approval/timeout
// 四处都走它。少清一个字段的后果是**倒计时继续跑**——屏幕上还在闪
// `!! 还有 2s`，而这一问早就结束了。
func (a *app) clearPending() {
	a.pending.id = ""
	a.pending.title = ""
	a.pending.deadline = time.Time{}
}

// deadlineFrom 把宿主报的超时换成本地截止时刻。
//
// 优先级：绝对 deadlineMs > 时长 timeoutMs > 什么都没有（返回零值）。
func (a *app) deadlineFrom(deadlineMs, timeoutMs int64) time.Time {
	now := a.clock()
	var total time.Duration
	if timeoutMs > 0 {
		total = time.Duration(timeoutMs) * time.Millisecond
	}
	if deadlineMs > 0 {
		d := time.UnixMilli(deadlineMs)
		if total > 0 {
			left := d.Sub(now)
			if left > total {
				// 客户端时钟比宿主慢：剩余时间不可能比总时长还多。
				d = now.Add(total)
			} else if left <= 0 {
				// 刚收到的请求就已经"过期"了：只可能是两边时钟不一致。
				// 这时**不许替宿主提前判超时**——它还挂着等这一问；
				// 替它拒掉，用户就再也答不了了，而现象正是"莫名其妙被拒"。
				d = now.Add(total)
			}
		}
		return d
	}
	if total > 0 {
		// 老宿主只报时长。注意重放时这样会从满额重新算——所以宿主优先发 deadlineMs。
		return now.Add(total)
	}
	return time.Time{}
}

// approvalLeft 返回距截止还有多久（已过则为 0）以及**有没有时限**。
//
// ok=false 只出现在两种时候：没有挂起审批，或宿主没报超时。
// 这两种都不许编数字——"我不知道还剩多久"和"还剩 0 秒"是两件事。
func (a *app) approvalLeft() (time.Duration, bool) {
	if a.pending.id == "" || a.pending.deadline.IsZero() {
		return 0, false
	}
	left := a.pending.deadline.Sub(a.clock())
	if left < 0 {
		left = 0
	}
	return left, true
}

// approvalStatus 是状态行上"等你批准"那一行。三档：
//
//	有时限、>5s   ? 等你批准（12s）：bash · rm -rf build/  [y]允许 [a]以后都允许 [n]拒绝
//	有时限、≤5s   !! 还有 3s：bash · rm -rf build/  [y]允许 [a]以后都允许 [n]拒绝
//	无时限        ? 等你批准（倒计时不可用：宿主未报超时）：…
//
// **纯函数**：只读状态、不改。超时那一刻的清理归 expireApproval；
// 这里看到 secs<=0 只如实说"已超时"。渲染顺手改状态的话 view() 就不再是纯函数，
// 而布局测试全靠它是纯函数。
func (a *app) approvalStatus() string {
	// 按键提示永远在：看不见怎么答才是真的卡死。
	const keys = "  [y]允许 [a]以后都允许 [n]拒绝"
	title := trunc(a.pending.title, approvalTitleWidth)
	left, ok := a.approvalLeft()
	if !ok {
		return " ? 等你批准（倒计时不可用：宿主未报超时）：" + title + keys
	}
	// 向上取整：还剩 0.4s 也要说 1s，说 0s 会让人以为已经过去了。
	secs := int((left + time.Second - 1) / time.Second)
	if secs <= 0 {
		return " ! " + approvalTimeoutNote
	}
	if secs <= approvalWarnSeconds {
		return fmt.Sprintf(" !! 还有 %ds：%s%s", secs, title, keys)
	}
	return fmt.Sprintf(" ? 等你批准（%ds）：%s%s", secs, title, keys)
}

// expireApproval 在倒计时走到 0 时清掉 pending。返回这次是否真的到期了。
//
// 为什么界面这边也要自己判超时（宿主明明会推 approval/timeout）：
// 那一帧**可能晚到、也可能在断线时丢掉**。界面还挂着"等你批准"的话，
// 用户按 y 只会拿到 404，而屏幕上看起来他明明还能答。
func (a *app) expireApproval() bool {
	left, ok := a.approvalLeft()
	if !ok || left > 0 {
		return false
	}
	a.noteApprovalTimeout()
	return true
}

// noteApprovalTimeout 记一次"超时按拒绝处理"：清 pending + 状态行说明 + 正文留一行。
//
// 正文那一行是给"人离开过"准备的：状态行会被下一个事件顶掉，
// 而正文留在那里，回来看得见刚才被拒的是哪一条。
func (a *app) noteApprovalTimeout() {
	title := trunc(a.pending.title, 60)
	a.clearPending()
	a.err = approvalTimeoutNote
	if title == "" {
		a.push("！审批已超时，这一问按拒绝处理")
		return
	}
	a.push("！审批超时：" + title + " —— 已按拒绝处理")
}

// noteApprovalCancel 记一次"这一问被取消"：清 pending + 正文留一行。
//
// 为什么必须**点名取消者**：三种结算在屏幕上太容易长得一样了——
// 应答留的是「→ allow_once」，超时写「审批超时」，而取消如果只写
// 「审批结束」，用户就分不清这一问是自己拒的、到点超时的、还是被放行的。
// 取消这一种的事实是：**宿主**按 `session.cancel` 结算并取消了这一轮，
// 工具没有执行。所以正文那行原话照写，状态行则回到普通状态（取消不是错误）。
func (a *app) noteApprovalCancel() {
	title := trunc(a.pending.title, 60)
	a.clearPending()
	const what = " —— 宿主按 session.cancel 取消了这一轮，工具没有执行"
	if title == "" {
		a.push("！审批已取消" + what)
		return
	}
	a.push("！审批已取消：" + title + what)
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
	if i < 0 || i >= len(a.meta) {
		return false
	}
	return a.hiddenMeta(a.meta[i])
}

// hiddenMeta 是折叠规则的**唯一**实现：isHidden 和"新行到达要不要挪窗口"
// 都走它。两处各写一遍的话，迟早会出现"折叠的行被算进了可见行"这种错，
// 而它的症状只是回看时少/多一行，很难归因。
func (a *app) hiddenMeta(m lineMeta) bool {
	if a.showToolResults {
		return false
	}
	return m.toolResult && !m.isError
}

// bodyHeight 是正文区能放的行数（整屏去掉分隔线、状态行、输入行）。
func (a *app) bodyHeight() int {
	body := a.scr.height - 3
	if body < 1 {
		body = 1
	}
	return body
}

// maxScroll 是能往回看的上限。内容装得下一屏时是 0——**滚不动**。
//
// 用 visibleCount 而不是 visibleLines：这个函数在**每压一行**的时候都会被
// 调到（回看中要夹紧），流式输出时一帧几十行，不能每行都复制一份切片。
func (a *app) maxScroll() int {
	n := a.visibleCount()
	body := a.bodyHeight()
	if n <= body {
		return 0
	}
	return n - body
}

// visibleCount 是 visibleLines 的**条数**，不复制、不分配。
// 判断仍然只走 isHidden，两个口径不会分叉。
func (a *app) visibleCount() int {
	if a.showToolResults {
		return len(a.lines)
	}
	n := 0
	for i := range a.lines {
		if !a.isHidden(i) {
			n++
		}
	}
	return n
}

// setScroll 是改滚动位置的**唯一**入口：一律夹在 [0, maxScroll]。
//
// 夹在写入侧而不是只在 view 里夹，是为了让按键有确定的反馈：
// 若是只在显示时夹，在顶部再按几次 PgUp 会把 scroll 越推越高，
// 之后按 PgDn 前几下看起来"没反应"。
func (a *app) setScroll(n int) {
	if n < 0 {
		n = 0
	}
	if max := a.maxScroll(); n > max {
		n = max
	}
	a.scroll = n
}

// scrollBack 往回看 n 行（n>0 = 更新的内容），scrollForward 往新内容方向 n 行。
// 两者都落在 setScroll 上，所以越界不会留后遗症。
func (a *app) scrollBack(n int)    { a.setScroll(a.scroll + n) }
func (a *app) scrollForward(n int) { a.setScroll(a.scroll - n) }

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

// handleSpecial 处理方向上的一类按键（都由 readKeys 解码成名字）。
//
//	PgUp / PgDn       翻一屏（正好是一屏的行数，接得上、也不留缝）
//	↑ / ↓             单行
//	Home / Ctrl+Home  到最上面
//	End / Ctrl+End    回到最底部（scroll=0 就恢复"跟随新输出"）
//
// Ctrl+End 与 End 在这里是同一件事：这个界面没有行内光标，"到行尾"没有意义，
// 而"回到底部继续跟随"是回看之后最常用的那一下。两种终端的 Ctrl+Home/End
// 编码还不一样（`\x1b[1;5H` 与 `\x1b[1;5~`），索性合并。
func (a *app) handleSpecial(k string) {
	switch k {
	case "esc":
		// 搜索里的 Esc = 退出搜索并清掉查询串；别的时候 = 清输入行。
		if a.searching {
			a.exitSearch()
			return
		}
		a.input = nil
	case "pgup":
		a.scrollBack(a.bodyHeight())
	case "pgdn":
		a.scrollForward(a.bodyHeight())
	case "up":
		a.scrollBack(1)
	case "down":
		a.scrollForward(1)
	case "home":
		a.setScroll(a.maxScroll())
	case "end":
		a.setScroll(0)
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
		a.push("滚动：PgUp/PgDn 翻一屏 · ↑/↓ 一行 · Ctrl+Home 到最上面 · Ctrl+End 回到底部（继续跟随）")
	case "/new":
		if err := a.newSession(); err != nil {
			a.err = err.Error()
		}
	case "/clear":
		a.lines = nil
		a.meta = nil
		// 内容没了就没有"回看"可言：scroll 留在高位会让新内容一出现就
		// 处于回看状态（屏幕上看着像"没显示最新几行"）。
		a.scroll = 0
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
			// 宿主报的超时：total 与绝对截止（毫秒时间戳）。
			// **老宿主这两个字段都没有**——那时只显示"等待中"，绝不编一个数字。
			TimeoutMs  int64 `json:"timeoutMs"`
			DeadlineMs int64 `json:"deadlineMs"`
		}
		if json.Unmarshal(f.Data, &req) == nil && req.RequestID != "" {
			// 新的一问上来时，把状态行上**上一问留下**的那句"已超时"让位：
			// 不让位的话，用户答完这一问，状态行会突然冒出"审批已超时"——
			// 看起来像刚答的那条超时了。别的错误提示（比如"回答审批失败"）不动。
			if a.err == approvalTimeoutNote {
				a.err = ""
			}
			a.pending.id = req.RequestID
			a.pending.title = req.ToolCall.Title
			a.pending.deadline = a.deadlineFrom(req.DeadlineMs, req.TimeoutMs)
			a.push("！需要你批准：" + req.ToolCall.Title)
			a.push("  按 y 允许这一次 · a 以后都允许 · n 拒绝")
			if left, ok := a.approvalLeft(); ok {
				// 正文里也写一句：状态行只有一行，人不在时会被后来的事件顶掉，
				// 而"多久会自己拒掉"是回头查这件事时最要紧的一个数。
				a.push(fmt.Sprintf("  宿主那边 %ds 后按拒绝处理，到点这一问就作废",
					int((left+time.Second-1)/time.Second)))
			}
		}
	case "approval/answered":
		a.clearPending()
	case "approval/timeout":
		// 宿主已经按拒绝结算。界面必须**立刻**收掉倒计时和按键，
		// 否则用户按 y 只会拿到 404（这一问不在了），而屏幕上看起来他还能答。
		// 已经被 expireApproval 处理过的不再重复记一行。
		if a.pending.id != "" {
			a.noteApprovalTimeout()
		}
	case "approval/cancel":
		// 宿主按拒绝结算并**取消了这一轮**（`session.cancel` 的落点，见
		// cmd/dshd/serve_approval.go 的 CancelSession）。负载与 approval/request
		// 同形，所以 requestId 一比就知道是不是屏幕上这一问。
		//
		// 不处理它的后果不是崩，是**屏幕在骗人**：气泡还挂着「等你批准」、
		// 倒计时还在跑，而宿主那边这一问早就没了——按 y 只会拿到 404。
		//
		// 为什么按 requestId 认：重连重放会把旧的 cancel 帧再送一遍，
		// 不比对的话，一条上一轮的 cancel 会把这一轮**新**的气泡收掉。
		var req struct {
			RequestID string `json:"requestId"`
		}
		_ = json.Unmarshal(f.Data, &req)
		if a.pending.id != "" && (req.RequestID == "" || req.RequestID == a.pending.id) {
			a.noteApprovalCancel()
		}
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
	// 正在回看时又来了新行：把"离底部多远"跟着 +1，屏幕上那几行才**不动**。
	// 不做这一步，新输出会把整个窗口往下挤——人正在读的那段就滑走了，
	// 而且越流式越明显（每 100ms 一帧）。
	//
	// 折叠掉的行不占显示格子，所以不加。裁头（超过 maxLines）也不用补偿：
	// scroll 是相对**底部**的距离，从头部裁掉多少都不改变它指的那段内容。
	if a.scroll > 0 && !a.hiddenMeta(m) {
		a.scroll++
	}
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
	if a.scroll > 0 {
		// 回看位置可能已经贴到新的顶部（内容被裁掉一截），趁机夹回去，
		// 免得 scroll 越积越大、之后按 PgDn 前几下没反应。
		a.setScroll(a.scroll)
	}
}

func (a *app) tickStatus() {
	// 先结算审批倒计时：到点就清 pending，别让状态行多显示一帧"还能答"。
	//
	// 倒计时**不另起计时器**：主循环里这个 250ms 的 tick 已经在了，
	// 再用一个 goroutine 去改 a 的字段，就多一种"渲染时状态被改一半"的可能，
	// 而那种错只会表现为偶尔少画一行。250ms 粒度足够——显示本来就只到秒。
	a.expireApproval()
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
	body := a.bodyHeight()
	lines := make([]string, 0, a.scr.height)

	// 正文：先按折叠状态过滤，再按滚动位置取一屏。
	// 过滤在取窗口**之前**做：否则折叠的行会占掉本该给其它内容的格子，
	// 也会让"离底部多远"按看不见的行来算。
	visible := a.visibleLines()

	// max 是能往回看的上限：内容比屏幕短就是 0（滚不动）。
	max := 0
	if len(visible) > body {
		max = len(visible) - body
	}
	// 这里按 max 夹一次而不是直接信任 a.scroll：view 不假设状态干净
	// （测试、以及 maxLines 裁剪都可能让 scroll 暂时越界）。
	scroll := a.scroll
	if scroll < 0 {
		scroll = 0
	}
	if scroll > max {
		scroll = max
	}
	end := len(visible) - scroll
	start := end - body
	if start < 0 {
		start = 0
	}
	lines = append(lines, visible[start:end]...)
	for len(lines) < body {
		lines = append(lines, "")
	}

	// 状态行
	left := fmt.Sprintf(" %s · 会话 %s · 工具 %d · ctx %s",
		productName, short(a.sid), a.tools, ctxPercent(a.ctxTokens, a.ctxWindow))
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
		left = a.approvalStatus()
	} else if a.err != "" {
		left = " ! " + a.err
	}
	if scroll > 0 {
		// 回看时最要紧的三件事是"你在回看 / 还差多少回到最新 / 怎么回去"，
		// 所以放在**最前面**：窄终端按列截断，先牺牲的应该是后面那些常驻指标。
		//
		// N 是**回到底部还要过的行数**（就是 scroll），和同一行里推荐的
		// PgDn/Ctrl+End 是同一件事——要是按"上面还有多少行"报数，
		// 人按提示按 PgDn 会发现次数对不上。
		left = " " + fmt.Sprintf("↑ 回看中（还有 %d 行）· PgDn/Ctrl+End 回到底部", scroll) +
			" · " + strings.TrimLeft(left, " ")
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

// readKeys 把字节流转成 rune / 按键名。
//
// 不做完整行编辑（历史/光标移动）：那是下一步。这里只保证
// "能打字、能删、能回车、能退、能翻页"——足够验证整条链路。
func readKeys(runes chan<- rune, specials chan<- string) {
	decodeKeys(newStdinSource(os.Stdin), runes, specials)
}

// escTimeout 是"ESC 之后还给不给后续字节留时间"。
//
// 孤立 ESC（人按 Esc）和转义序列前缀（PgUp = ESC [ 5 ~）在字节上完全一样，
// 唯一能区分的是"后面还来不来字节"。50ms 够：真按键的后续字节是同一批
// 进内核缓冲的（微秒级就到），而人对 50ms 的延迟没有感觉。
//
// 只对 ESC 后面的**第一个**字节计时；一旦看到 '[' / 'O'（确认是序列），
// 后面就阻塞等到底——否则机器一卡，序列会在中途被拆开，往输入行里塞 "[5~"。
//
// 为什么非要有这个等待：旧代码一见 ESC 就立刻当 esc 发出去。加了翻页之后
// 这个问题会变严重——PgUp 会顺带触发 esc，把输入行里打了一半的字清掉。
// "翻页把话删了"比"翻页没反应"更难接受。
const escTimeout = 50 * time.Millisecond

// byteSource 是解码器的输入。抽出来有两个目的：
// 测试能直接喂字节（strings.Reader，没有真时钟），
// 生产路径能在 ESC 之后**超时**而不是死等。
//
// afterEsc 为 true 表示上一个字节是 ESC：这时源可以只等一小会儿就报 timeout。
// timeout 只在这个位置出现，孤立 ESC 就靠它认出来。
type byteSource interface {
	next(afterEsc bool) (c byte, timeout bool, ok bool)
}

// readerSource 是测试用的源：读到哪算哪，没有超时（EOF 即结束）。
type readerSource struct {
	in  io.Reader
	buf [1]byte
}

func (r *readerSource) next(bool) (byte, bool, bool) {
	n, err := r.in.Read(r.buf[:])
	if err != nil || n == 0 {
		return 0, false, false // ok=false：输入结束
	}
	return r.buf[0], false, true
}

// chanSource 是生产用的源：一个 goroutine 把 stdin 的字节泵进来，
// 解码器在 ESC 之后最多等 escTimeout。
//
// 为什么要泵：os.Stdin 在 Windows 控制台上不支持 SetReadDeadline
// （不是可 poll 的文件），等不到超时；只能把"读"挪进 goroutine。
type chanSource struct {
	ch  <-chan byte
	gap time.Duration
}

func (s *chanSource) next(afterEsc bool) (byte, bool, bool) {
	if !afterEsc {
		c, ok := <-s.ch
		return c, false, ok
	}
	t := time.NewTimer(s.gap)
	defer t.Stop()
	select {
	case c, ok := <-s.ch:
		return c, false, ok
	case <-t.C:
		return 0, true, false
	}
}

// newStdinSource 起一个泵 goroutine 并返回对应的源。
func newStdinSource(in *os.File) byteSource {
	return &chanSource{ch: pumpBytes(in), gap: escTimeout}
}

// pumpBytes 把 in 的字节一个一个送进 channel；读失败或 EOF 就关掉它
// （解码器看到关闭 = 输入结束）。
func pumpBytes(in *os.File) <-chan byte {
	ch := make(chan byte, 64)
	go func() {
		defer close(ch)
		buf := make([]byte, 1)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				ch <- buf[0]
			}
			if err != nil || n == 0 {
				return
			}
		}
	}()
	return ch
}

// readKeysFrom 是解码部分，单独拆出来是为了能喂字节流做测试：
// 键盘解码错了，屏幕上是"按键没反应"，那是最难查的一类现象。
func readKeysFrom(in io.Reader, runes chan<- rune, specials chan<- string) {
	decodeKeys(&readerSource{in: in}, runes, specials)
}

// decodeKeys 是完整的状态机：字节流 → rune / 按键名。
//
// 这里修掉三个会让按键**失效或误伤**的问题：
//
//  1. 旧写法"等到续字节才解码"（`c&0xC0 != 0x80 → continue`）对多字节字符成立，
//     但对 ASCII 永远等不到续字节——字母、Ctrl+T、Ctrl+F、y/a/n、Ctrl+C
//     一个都发不出来。改成"凑满一个完整字符就发"（utf8.FullRune）。
//  2. ESC 之后必须把整个转义序列吞掉（ESC [ A 之类），否则按一次方向键
//     会往输入行里塞 "[A"。
//  3. ESC 不能一见就发：PgUp/PgDn/Ctrl+Home/Ctrl+End 也都是 ESC 开头的序列。
//     所以要先把 ESC 扣住，确认不是序列（超时 / 后面跟的不是 [ 或 O）再发。
func decodeKeys(src byteSource, runes chan<- rune, specials chan<- string) {
	var pend []byte
	pendingEsc := false
	csi := false
	var seq []byte // CSI/SS3 的参数与终结字节（"5~"、"1;5H"、"A"）
	for {
		c, timeout, ok := src.next(pendingEsc)
		if timeout {
			// ESC 之后等不到后续字节 = 人按的就是 Esc
			if pendingEsc {
				specials <- "esc"
				pendingEsc = false
			}
			continue
		}
		if !ok {
			// 输入结束：挂着的 ESC 也要交代掉，再收工
			if pendingEsc {
				specials <- "esc"
			}
			return
		}
		switch {
		case pendingEsc:
			pendingEsc = false
			if c == '[' || c == 'O' {
				// CSI（ESC [ …）与 SS3（ESC O …）走同一条路：
				// 方向键在真终端上两种都有
				csi = true
				seq = seq[:0]
				continue
			}
			// 孤立 ESC 后面跟的是普通按键：ESC 照发，这个字节照常处理
			specials <- "esc"
		case csi:
			// 终结字节也要进 seq（"A"、"5~"、"1;5H" 的末字节都是）
			seq = append(seq, c)
			// 终结字节在 0x40–0x7E
			if c >= 0x40 && c <= 0x7e {
				csi = false
				if name := keyName(seq); name != "" {
					specials <- name
				}
				continue
			}
			// 参数收太长说明根本不是序列：放弃这段，别越攒越多
			if len(seq) > 16 {
				csi = false
			}
			continue
		case c == 0x1b:
			pendingEsc = true
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

// keyName 把 CSI/SS3 的参数+终结字节认成按键名。
//
// 只认**确定知道**的几个，认不出来返回空串（静默吞掉）：
// 往输入行里塞 "[1;2C" 比没反应糟得多。修饰键（"1;5H" 里的 5）直接不看——
// 这个界面里 Ctrl+Home 和 Home 是同一件事（见 handleSpecial）。
func keyName(seq []byte) string {
	if len(seq) == 0 {
		return ""
	}
	numPart := seq[:len(seq)-1]
	if i := indexByte(numPart, ';'); i >= 0 {
		numPart = numPart[:i]
	}
	num, _ := atoiSmall(string(numPart)) // SS3 的 "OA" 没有数字，得到 0
	switch seq[len(seq)-1] {
	case 'A':
		return "up"
	case 'B':
		return "down"
	case 'H':
		return "home"
	case 'F':
		return "end"
	case '~':
		switch num {
		case 1, 7:
			return "home" // ESC[1~ / ESC[7~
		case 4, 8:
			return "end" // ESC[4~ / ESC[8~
		case 5:
			return "pgup"
		case 6:
			return "pgdn"
		}
	}
	return ""
}

// indexByte 是 bytes.IndexByte 的最小替代：这里只处理几个字节，不值得再引一个包。
func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
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
	b.WriteString("导出自 " + versionLine() + "\n\n")
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
