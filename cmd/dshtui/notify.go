package main

// 系统通知的三路终端投递（上游 dsh-TUI 0.20.0 的 Notifications）。
//
// 为什么单独成文件、且把**构造**与**投递**分开：通知的每条规则都是字节级的
// ——OSC 777/99/9 的 2048 字节预算、终止符（BEL 还是 ST）、tmux/screen 的
// DCS 透传包装、按字素截断。这些一旦写偏，屏幕上不会报错，只会"通知没出现"
// 或者"通知里出现半截乱码"，是最难归因的一类现象。所以：
//
//   - notificationText / clipToWidthGraphemes / truncateGraphemesUTF8：纯文本清洗与截断
//   - oscSequence / wrapForMultiplexer / notificationProtocol / resolveNotificationTransport：
//     纯函数，输入就是环境事实，输出就是字节
//   - terminalNotifier：唯一的副作用点（写 stdout），门禁（开关 + 焦点 + TTY）也在这
//
// 上游对照（/tmp/tui020/package，0.20.0 解包源码）：
//   - lib/types/ink/terminal-notification.js：notificationText / clipToWidthGraphemes /
//     truncateUtf8 / createTerminalNotifier（OSC 777/99/9 + BEL + 2048 预算 + tmux 包装）
//   - lib/types/ink/terminal.js：getNotificationProtocol / resolveNotificationMethod
//   - lib/types/ink/termio/osc.js：osc() 与 wrapForMultiplexer()
//   - lib/types/ink/terminal-focus-state.js：未知焦点算"聚焦"（getTerminalFocused）

import (
	"io"
	"strconv"
	"strings"
	"unicode"
)

const (
	// notificationMaxGraphemes 是通知正文的字素上限（上游默认 200）。
	notificationMaxGraphemes = 200
	// notificationMaxOSCBytes 是 OSC 777/9 的整条序列预算，**含标题与协议字节**。
	// 2048 的来由是 Ghostty 1.2.3 的 OSC 缓冲区大小（上游注释），按整条未包装的
	// 序列计数，把引导符与终止符一起算进去是最保守的做法。
	notificationMaxOSCBytes = 2048
	// notificationSessionNameMaxWidth 是授权通知正文里会话名的显示列上限。
	// 上游 plugin.js 的 NOTIFICATION_SESSION_NAME_MAX_WIDTH = 20：工具名与理由
	// 才是"哪一问在等你"的关键，会话名先让位。
	notificationSessionNameMaxWidth = 20
)

// 四类通知的标题。上游标题经 i18n 本地化（zh 见 lib/types/i18n.js:264-267），
// 这里钉的是上游给出的英文原文——通知是**离开 TUI 的文字**，与体内中文界面
// 不必同语言，且这四条是规格里逐字写明的。
const (
	notificationTitleTaskComplete  = "dsh-TUI · Task complete"
	notificationTitleApproval      = "dsh-TUI · Approval needed"
	notificationTitleQuestion      = "dsh-TUI · Waiting for your answer"
	notificationTitlePlanReview    = "dsh-TUI · Plan review"
	notificationUntitledSessionDoc = "未命名会话" // 上游 'untitled-session' 的 zh
)

// OSC 协议里的三个常量字节。
const (
	oscIntroducer       = "\x1b]"
	oscStringTerminator = "\x1b\\"
	belChar             = "\x07"
)

// ───────────────────────────────────────────────────────────── 设置档位

// notificationCondition 是 taskNotifications 开关：unfocused（默认）/ always / off。
type notificationCondition string

const (
	notifWhenUnfocused notificationCondition = "unfocused"
	notifAlways        notificationCondition = "always"
	notifOff           notificationCondition = "off"
)

// notificationMethod 是 notificationMethod 设置：auto（默认）/ osc9 / bel / windows。
type notificationMethod string

const (
	notifMethodAuto    notificationMethod = "auto"
	notifMethodOSC9    notificationMethod = "osc9"
	notifMethodBEL     notificationMethod = "bel"
	notifMethodWindows notificationMethod = "windows"
)

// parseNotificationCondition 严格解析三档开关；未知值报错而不是悄悄取默认。
//
// 静默回退的后果是"设置写了却不生效"：用户改成 off 之后通知还在响，
// 屏幕上没有任何东西会告诉他这次没生效。
func parseNotificationCondition(v string) (notificationCondition, error) {
	switch notificationCondition(strings.TrimSpace(v)) {
	case "", notifWhenUnfocused:
		return notifWhenUnfocused, nil
	case notifAlways:
		return notifAlways, nil
	case notifOff:
		return notifOff, nil
	}
	return notifWhenUnfocused, errUnknownNotificationValue("taskNotifications", v, "unfocused | always | off")
}

// parseNotificationMethod 严格解析四档投递方式；`windows` 是合法取值
// （上游有这个档），但本版没有 PowerShell toast 路径——取值成功、投递时
// 明确降级并报错（见 terminalNotifier.notify），不在这里假装它不存在。
func parseNotificationMethod(v string) (notificationMethod, error) {
	switch notificationMethod(strings.TrimSpace(v)) {
	case "", notifMethodAuto:
		return notifMethodAuto, nil
	case notifMethodOSC9:
		return notifMethodOSC9, nil
	case notifMethodBEL:
		return notifMethodBEL, nil
	case notifMethodWindows:
		return notifMethodWindows, nil
	}
	return notifMethodAuto, errUnknownNotificationValue("notificationMethod", v, "auto | osc9 | bel | windows")
}

func errUnknownNotificationValue(key, value, allowed string) error {
	return &notificationValueError{key: key, value: value, allowed: allowed}
}

type notificationValueError struct{ key, value, allowed string }

func (e *notificationValueError) Error() string {
	return e.key + " 不接受 " + strconv.Quote(e.value) + "（可选：" + e.allowed + "）"
}

// ───────────────────────────────────────────────────────────── 文本清洗

// notificationText 把任意来源的文本规范成"一条通知正文"：
// 去 ANSI、C0/C1 控制字符换成空格、空白折叠、去首尾，再按字素截到上限。
//
// 为什么四类通知共用这一条规则：正文会离开 TUI，进 OSC 负载；裸控制字符要么
// 提前终止序列（ESC/BEL），要么在终端里打印成垃圾。多行文本也必须压成一行，
// 因为系统通知只渲染一行。这与上游 notificationText 逐条对应。
func notificationText(text string, maxGraphemes int) string {
	flat := notificationSingleLine(text)
	clusters := graphemeStrings(flat)
	if len(clusters) <= maxGraphemes {
		return flat
	}
	keep := maxGraphemes - 1 // 省略号自己占一个字符位
	if keep < 0 {
		keep = 0
	}
	var b strings.Builder
	b.Grow(len(flat))
	for _, g := range clusters[:keep] {
		b.WriteString(g)
	}
	b.WriteString("…")
	return b.String()
}

// notificationSingleLine 是 notificationText 的"压成一行"那一半。
func notificationSingleLine(text string) string {
	s := stripANSIVisible(text)
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		// C0/C1（含 DEL）一律换空格：BEL/ESC 在 OSC 负载里会截断序列。
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			r = ' '
		}
		if unicode.IsSpace(r) {
			if b.Len() == 0 {
				continue // 首部空白直接丢，省一次 TrimSpace
			}
			pendingSpace = true
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// stripANSIVisible 去掉转义序列，但把"孤立 ESC"留成空格而不是删掉。
//
// 为什么与 stripANSI 不同：正文的清洗规则是"控制字符换成空格"——上游只吃
// **完整**的转义序列，孤立 ESC 本来就留在原文里，随后被控制字符那条规则换成
// 空格。直接用 stripANSI 会把 `c<ESC>d` 拼成 `cd`，两个词粘在一起，
// 通知正文里这会让句子读不懂。
func stripANSIVisible(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if n := ansiSeqLen(s[i:]); n > 1 {
				i += n // 真序列：整段丢掉
				continue
			}
			b.WriteByte(' ') // 孤立 ESC：一个空格
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// clipToWidthGraphemes 按**显示列**截断，省略号算一列，且按整字素走。
//
// 与 notificationText 的差别：那是"多少个字符"，这是"占多少格"——
// 会话名在通知正文里按列让位（CJK 两列），所以两条规则都要有。
func clipToWidthGraphemes(text string, maxWidth int) string {
	if inputStringWidth(text) <= maxWidth {
		return text
	}
	rs := []rune(text)
	var b strings.Builder
	width := 0
	for _, c := range inputClusters(rs) {
		if width+c.width > maxWidth-1 {
			break
		}
		b.WriteString(string(rs[c.start:c.end]))
		width += c.width
	}
	b.WriteString("…")
	return b.String()
}

// truncateGraphemesUTF8 按 **UTF-8 字节**截断，按整字素边界切。
//
// OSC 的 2048 是字节预算，不是字符数：一个 CJK 字 3 字节，一个 ZWJ 家庭
// emoji 可以到 25 字节。所以这里数字节，但绝不把一个字素切两半——
// 切坏的结果是终端里出现一个替换方块，比少几个字更难解释。
// 与上游 truncateUtf8 对应（省略号占 3 字节，预算不够 3 字节时不留省略号）。
func truncateGraphemesUTF8(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	suffix := ""
	if maxBytes >= 3 {
		suffix = "…"
	}
	budget := maxBytes - len(suffix)
	if budget < 0 {
		budget = 0
	}
	var b strings.Builder
	b.Grow(maxBytes)
	bytes := 0
	for _, g := range graphemeStrings(text) {
		if bytes+len(g) > budget {
			break
		}
		b.WriteString(g)
		bytes += len(g)
	}
	b.WriteString(suffix)
	return b.String()
}

// graphemeStrings 按整字素切分字符串。
//
// 复用草稿编辑器的分段器（input.go 的 inputClusters）：同一个界面上"字符边界"
// 只能有一个定义，否则光标能停的位置和通知能切的位置会各说各话。
func graphemeStrings(s string) []string {
	if s == "" {
		return nil
	}
	rs := []rune(s)
	clusters := inputClusters(rs)
	out := make([]string, len(clusters))
	for i, c := range clusters {
		out[i] = string(rs[c.start:c.end])
	}
	return out
}

// ───────────────────────────────────────────────────────────── OSC 构造

// terminalName 是上游 env.terminal 的等价物：TERM_PROGRAM 缺省时退回 TERM。
func terminalName(termProgram, term string) string {
	name := strings.TrimSpace(termProgram)
	if name == "" {
		name = strings.TrimSpace(term)
	}
	return strings.ToLower(name)
}

// notificationOSCTerminator 选 OSC 的收尾字节：Kitty 用 ST，其它用 BEL。
//
// 为什么对 Kitty 特殊：它的 OSC 解析在遇到 BEL 时会响一声（上游 osc() 的注释），
// 而 BEL 恰恰是我们"投递方式 = 响铃"时要用的信号——两者不能混。
func notificationOSCTerminator(termProgram, term string) string {
	if terminalName(termProgram, term) == "kitty" {
		return oscStringTerminator
	}
	return belChar
}

// oscSequence 构造 `ESC ] p1;p2;…;pN <终止符>`。
//
// 负载里的 C0/C1 一律删掉：BEL 与 ST 都是终止符，ESC 会另起一条序列，
// C1 在 8 位终端上是同一件事。这是"不可信文本进 OSC"的唯一收口点
// （上游 osc() 的 sanitizeOscPart 与 OSC_PAYLOAD_UNSAFE 同义）。
func oscSequence(terminator string, parts ...string) string {
	var b strings.Builder
	b.WriteString(oscIntroducer)
	for i, part := range parts {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(stripOSCUnsafe(part))
	}
	b.WriteString(terminator)
	return b.String()
}

func stripOSCUnsafe(s string) string {
	safe := true
	for _, r := range s {
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wrapForMultiplexer 在 tmux / GNU screen 里把序列塞进 DCS 透传。
//
// 为什么必须包：tmux 与 screen 会自己解析 OSC，包一层 DCS 才能原样送到外层终端。
// tmux 3.3+ 需要 `set -g allow-passthrough on`，没开时 tmux 直接丢掉整段 DCS
// ——不显示垃圾，也不比不包更糟；我们不替用户改 tmux.conf。
//
// **BEL 不包**：裸 \x07 才能触发 tmux 的 bell-action（窗口标记）；
// 包进 DCS 之后 tmux 看不见响铃，等于这条投递路径失效。
func wrapForMultiplexer(sequence string, getenv func(string) string) string {
	if getenv == nil {
		return sequence
	}
	if getenv("TMUX") != "" {
		escaped := strings.ReplaceAll(sequence, "\x1b", "\x1b\x1b")
		return "\x1bPtmux;" + escaped + oscStringTerminator
	}
	if getenv("STY") != "" {
		return "\x1bP" + sequence + oscStringTerminator
	}
	return sequence
}

// ───────────────────────────────────────────────────────────── 终端判定

// notificationTransport 是"实际走哪条路"。
type notificationTransport string

const (
	transportOSC777  notificationTransport = "osc777"
	transportOSC99   notificationTransport = "osc99"
	transportOSC9    notificationTransport = "osc9"
	transportBEL     notificationTransport = "bel"
	transportWindows notificationTransport = "windows"
)

// notificationProtocol 是上游 getNotificationProtocol 的判定表（auto 档）。
//
// 整个判定只看三样：TERM_PROGRAM（小写后比较）、TERM（逐字比较）、
// XTVERSION 探测回来的终端名（可选，SSH 场景下 TERM_PROGRAM 不转发，只有它能认出来）。
//
//	ghostty / wezterm（TERM=xterm-ghostty、探测名 ghostty|wezterm 开头）→ OSC 777
//	kitty（TERM=xterm-kitty、探测名 kitty 开头）                       → OSC 99
//	iterm.app / warpterminal（探测名 iterm/iterm2/warp 开头）          → OSC 9
//	其它                                                              → 响铃
func notificationProtocol(termProgram, term, xtversion string) notificationTransport {
	program := strings.ToLower(strings.TrimSpace(termProgram))
	probed := strings.ToLower(strings.TrimSpace(xtversion))
	switch {
	case program == "ghostty" || program == "wezterm" ||
		term == "xterm-ghostty" ||
		probeStartsWithWord(probed, "ghostty") || probeStartsWithWord(probed, "wezterm"):
		return transportOSC777
	case program == "kitty" || term == "xterm-kitty" || probeStartsWithWord(probed, "kitty"):
		return transportOSC99
	case program == "iterm.app" || program == "warpterminal" ||
		probeStartsWithWord(probed, "iterm") || probeStartsWithWord(probed, "iterm2") ||
		probeStartsWithWord(probed, "warp") || probeStartsWithWord(probed, "warpterminal"):
		return transportOSC9
	}
	return transportBEL
}

// probeStartsWithWord 对应上游 `/^<name>\b/i`：前缀命中且后面是词边界。
// 不接受 "koi-term"（词边界不在 name 之后）这类误命中。
func probeStartsWithWord(probed, name string) bool {
	if !strings.HasPrefix(probed, name) {
		return false
	}
	if len(probed) == len(name) {
		return true
	}
	next := rune(probed[len(name)])
	return !unicode.IsLetter(next) && !unicode.IsDigit(next) && next != '_'
}

// resolveNotificationTransport 是上游 resolveNotificationMethod 的等价判定：
//
//	osc9 / bel      → 显式覆盖，任何平台都照办
//	auto + SSH      → 走**连接终端**的协议（远端不该弹本机的 toast）
//	auto / windows  → 本地 Windows 解析成 windows（系统 toast）；其它平台走探测协议
//
// `windows` 走同一个函数是因为它是一个**平台能力**而不是终端序列：
// 在非 Windows 上它只能回落到探测协议（上游 terminal-notification.js 同义）。
func resolveNotificationTransport(method notificationMethod, platform string, ssh bool, protocol notificationTransport) notificationTransport {
	switch method {
	case notifMethodOSC9:
		return transportOSC9
	case notifMethodBEL:
		return transportBEL
	case notifMethodAuto:
		if ssh {
			return protocol
		}
	}
	if platform == "win32" {
		return transportWindows
	}
	return protocol
}

// ───────────────────────────────────────────────────────────── 四路字节

// osc777Sequence 是 Ghostty / WezTerm 的 `OSC 777 ; notify ; title ; body`。
//
// 预算分两次算，与上游逐字相同：
//
//	titleBudget = 2048 - len(OSC 777 的空标题整条序列)
//	bodyBudget  = 2048 - len(OSC 777 带上已定标题的整条序列)
//
// 这样"标题 + 正文 + 所有协议字节"永远不超过 2048。标题里的 `;` 是字段分隔符，
// 必须先换成空格，否则正文会被解析成第二个字段。
func osc777Sequence(terminator, title, message string) string {
	titleBudget := notificationMaxOSCBytes - len(oscSequence(terminator, "777", "notify", "", ""))
	safeTitle := truncateGraphemesUTF8(strings.ReplaceAll(title, ";", " "), titleBudget)
	bodyBudget := notificationMaxOSCBytes - len(oscSequence(terminator, "777", "notify", safeTitle, ""))
	return oscSequence(terminator, "777", "notify", safeTitle, truncateGraphemesUTF8(message, bodyBudget))
}

// osc99Sequences 是 Kitty 的 OSC 99：标题一条，正文按 2048 一段分块，
// 只有最后一段标 d=1，这样终端把它们合成一条通知。
//
// 与上游同样的切法：正文按 code point 切（不是按整字素），因为 Kitty 自己把
// 同 id 的段拼起来；标题不分块（上游也不分块）。
func osc99Sequences(terminator, title, message string, id int) string {
	var b strings.Builder
	b.WriteString(oscSequence(terminator, "99", "i="+strconv.Itoa(id)+":d=0:p=title", title))
	chunks := chunkByCodePoint(message, notificationMaxOSCBytes)
	for i, part := range chunks {
		done := 0
		if i == len(chunks)-1 {
			done = 1
		}
		b.WriteString(oscSequence(terminator, "99", "i="+strconv.Itoa(id)+":d="+strconv.Itoa(done)+":p=body", part))
	}
	return b.String()
}

// chunkByCodePoint 把 payload 按 code point 装进每块 maxBytes 字节的若干块。
// 空串也要给一块（上游同样 push 空块），否则"只有标题"的通知会少一段结构。
func chunkByCodePoint(payload string, maxBytes int) []string {
	var chunks []string
	var chunk strings.Builder
	bytes := 0
	for _, r := range payload {
		part := string(r)
		if bytes+len(part) > maxBytes && chunk.Len() > 0 {
			chunks = append(chunks, chunk.String())
			chunk.Reset()
			bytes = 0
		}
		chunk.WriteString(part)
		bytes += len(part)
	}
	return append(chunks, chunk.String())
}

// osc9Sequence 是 iTerm2 / Warp 的 OSC 9。
//
// OSC 9 没有独立标题字段，所以标题只能当**正文前缀**（`标题: 正文`）；
// 整条序列（含协议字节）同样受 2048 预算约束。
func osc9Sequence(terminator, title, message string) string {
	payload := title
	if message != "" {
		payload = title + ": " + message
	}
	budget := notificationMaxOSCBytes - len(oscSequence(terminator, "9", ""))
	return oscSequence(terminator, "9", truncateGraphemesUTF8(payload, budget))
}

// ───────────────────────────────────────────────────────────── 投递

// notifierFacts 是一次投递要读的全部外部事实。全部注入（而不是直接读
// os.Getenv / process.platform），因为判定表与门禁必须能在测试里逐条驱动。
type notifierFacts struct {
	getenv    func(string) string
	platform  string
	tty       bool        // stdout 是不是真交互终端（重定向输出绝不写序列）
	focused   func() bool // 终端此刻是否聚焦；未知要当"聚焦"（上游同义）
	xtversion string      // XTVERSION 探测回来的终端名（本版未探测，留空）
}

// terminalNotifier 是唯一写通知字节的地方。
type terminalNotifier struct {
	condition notificationCondition
	method    notificationMethod
	facts     notifierFacts
	nextID    int
	out       io.Writer
	noted     map[string]struct{}
}

// windowsToastNote 是本版"windows 档降级"的说明。
//
// 为什么不能静默：`auto` 在本地 Windows 上会解析成 windows（上游如此），
// 而本版**没有** PowerShell toast 路径（单独立项）。若不说话，用户会看到
// "设置是 auto、终端是 Windows、结果什么都没有"——分不清是没实现还是坏了。
const windowsToastNote = "通知方式 windows（系统原生 toast）本版未实现：已降级为响铃；" +
	"要终端路径请设 notificationMethod=osc9 或 bel"

func newTerminalNotifier(facts notifierFacts, out io.Writer) *terminalNotifier {
	n := &terminalNotifier{facts: facts, out: out, noted: map[string]struct{}{}}
	if err := n.configure("", ""); err != nil {
		// 默认值不可能解析失败；真失败说明字面量写错了，直接暴露。
		panic(err)
	}
	return n
}

// configure 应用一次设置变更（默认值也在解析里，见 parse*）。
func (n *terminalNotifier) configure(condition notificationCondition, method notificationMethod) error {
	cond, err := parseNotificationCondition(string(condition))
	if err != nil {
		return err
	}
	m, err := parseNotificationMethod(string(method))
	if err != nil {
		return err
	}
	n.condition, n.method = cond, m
	return nil
}

// alertsEnabled 是四类通知共用的唯一门禁（上游 notificationAlertsEnabled）：
// 输出必须是真终端、开关不是 off、且（unfocused 档时）终端确实失焦。
//
// "未知焦点算聚焦"是上游 getTerminalFocused 的语义：没有焦点上报的终端
// 永远不该被当成失焦，否则 `unfocused` 档会在前台一直弹通知；
// 这样的终端要通知就得显式选 `always`。
func (n *terminalNotifier) alertsEnabled() bool {
	if n == nil {
		return false
	}
	if !n.facts.tty || n.condition == notifOff {
		return false
	}
	if n.condition == notifWhenUnfocused && n.facts.focused != nil && n.facts.focused() {
		return false
	}
	return true
}

// notificationBytes 是**纯构造**：给定标题与正文，返回要写的字节与降级说明。
// 它不改门禁外的状态，只在 Kitty 分支推进通知 id（同一 notifier 内单调）。
func (n *terminalNotifier) notificationBytes(title, message string) ([]byte, string) {
	termProgram := n.facts.getenv("TERM_PROGRAM")
	term := n.facts.getenv("TERM")
	terminator := notificationOSCTerminator(termProgram, term)
	protocol := notificationProtocol(termProgram, term, n.facts.xtversion)
	transport := resolveNotificationTransport(n.method, n.facts.platform, isSSHSession(n.facts.getenv), protocol)
	var sequence string
	var note string
	switch transport {
	case transportWindows:
		// 没有 toast 路径：按上游"弹窗投不出去就响铃"的回退规则降级，
		// 同时把原因带回给调用方去说清楚（不许静默）。
		return []byte(belChar), windowsToastNote
	case transportOSC777:
		sequence = osc777Sequence(terminator, title, message)
	case transportOSC99:
		n.nextID++
		sequence = osc99Sequences(terminator, title, message, n.nextID)
	case transportOSC9:
		sequence = osc9Sequence(terminator, title, message)
	default: // transportBEL
		// BEL **不套包装**：裸 0x07 才能触发 tmux 的 bell-action；
		// 包进 DCS 之后 tmux 看不见响铃，等于这条路径失效。
		return []byte(belChar), note
	}
	return []byte(wrapForMultiplexer(sequence, n.facts.getenv)), note
}

// notify 投递一条通知：门禁 → 构造 → 写 stdout。返回非空说明表示这次
// 投递是**降级**的（目前只有 windows 档），调用方必须把它说出来。
func (n *terminalNotifier) notify(title, message string) string {
	if !n.alertsEnabled() {
		return ""
	}
	payload, note := n.notificationBytes(title, message)
	if len(payload) > 0 && n.out != nil {
		_, _ = n.out.Write(payload)
	}
	if note != "" && n.noted != nil {
		// 同一条降级说明一个进程只报一次：每次通知都刷一行会把它淹没。
		if _, seen := n.noted[note]; seen {
			return ""
		}
		n.noted[note] = struct{}{}
	}
	return note
}

// isSSHSession 判定"这是 SSH 会话"：上游看 SSH_CONNECTION / SSH_CLIENT / SSH_TTY。
func isSSHSession(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	return getenv("SSH_CONNECTION") != "" || getenv("SSH_CLIENT") != "" || getenv("SSH_TTY") != ""
}
