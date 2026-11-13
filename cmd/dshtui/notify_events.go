package main

// 四类系统通知：任务完成 / 等待授权 / 等待回答 / 计划评审。
//
// 一个开关（taskNotifications）覆盖四类，每类独立标题与正文；正文来源、
// 清洗规则与抑制规则逐条照上游 dsh-TUI 0.20.0：
//
//	任务完成  标题 "dsh-TUI · Task complete"
//	          正文 = **最新已结算主回复**的单行纯文本预览（去 Markdown，
//	          最多 200 字素）；无回复只显示标题（正文为空串）；
//	          BEL 只响铃。
//	等待授权  标题 "dsh-TUI · Approval needed"
//	          正文 = 会话名：工具名：asker 理由（宿主原文，规范成一行并限长，
//	          **不承诺脱敏**；**不读工具参数**）。
//	等待回答  标题 "dsh-TUI · Waiting for your answer"
//	          正文 = 第一个问题的 header 与 question（选项与详情留在面板里）。
//	计划评审  标题 "dsh-TUI · Plan review"
//	          正文 = 问题 header 与 question，**从不带计划本身**。
//
// 抑制规则（四条都不通知）：重放、取消、失败；后台会话与子代理保持安静；
// 外部 DSH 通知插件已启用且运行时完成/授权/提问让位，计划评审不让位。
//
// 上游对照：lib/types/dsh-adapter/channel/task-completion.js（完成的结算门）、
// lib/types/dsh-adapter/plugin.js（四类正文与抑制）、
// lib/types/ink/terminal-notification.js（notificationText / clipToWidthGraphemes）。

import (
	"encoding/json"
	"strings"
)

// ───────────────────────────────────────────────────────────── 正文构造

// taskCompletionPreview 把一条已结算回复变成通知正文：去 Markdown、压成一行、
// 最多 200 字素。空串表示"没有可预览的回复"——那就不带正文（只显示标题）。
//
// 为什么要去 Markdown：通知会离开 TUI 进系统通知中心，有的通知守护进程会把
// `**`、`#` 原样显示出来（上游注释：notification daemons may interpret raw
// Markdown headings）。所以正文必须是**可见文字**，不是源文本。
func taskCompletionPreview(text string) string {
	plain := markdownPlainText(text)
	if strings.TrimSpace(plain) == "" {
		return ""
	}
	return notificationText(plain, notificationMaxGraphemes)
}

// markdownPlainText 抽出 Markdown 的可见文字（与上游 markdownText(tokens) 同范围）：
// 标题只留文字、列表只留项、围栏代码保留代码行、行内样式去掉分隔符、
// 链接只留 label、图片只留 alt、HTML 标签去掉、转义还原。
//
// 不走 markdown.go 的 render 路径：那条路刻意**保留**链接的 `](url)` 原文
// （屏幕上不吞字），而通知正文要的是"读起来是一句话"，两者目标相反。
func markdownPlainText(src string) string {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out []string
	inFence := false
	for _, raw := range lines {
		line := raw
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if _, ok := fenceOpen(line); ok {
				inFence = true
				continue
			}
			if trimmed == "" {
				continue
			}
			if isRule(trimmed) {
				continue
			}
			if level, text, ok := headingLine(line); ok && level > 0 {
				out = append(out, markdownInlineText(text))
				continue
			}
			if level, marker, ok := listItem(line); ok && level >= 0 {
				// listItem 给的是**符号**（"-"、"1."），正文要的是符号之后的部分。
				trimmedLine := strings.TrimLeft(line, " ")
				if len(trimmedLine) > len(marker) {
					out = append(out, markdownInlineText(strings.TrimSpace(trimmedLine[len(marker):])))
				}
				continue
			}
			// 引用块：去掉前导 `>`，再按行内规则抽文字。
			if strings.HasPrefix(trimmed, ">") {
				out = append(out, markdownInlineText(strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))))
				continue
			}
			// 表格：分隔行（|---|:--:|）不是内容；其余按单元格拼成一句。
			if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
				cells := strings.Split(strings.Trim(trimmed, "|"), "|")
				if tableSeparatorRow(cells) {
					continue
				}
				parts := make([]string, 0, len(cells))
				for _, cell := range cells {
					if text := strings.TrimSpace(markdownInlineText(cell)); text != "" {
						parts = append(parts, text)
					}
				}
				if len(parts) > 0 {
					out = append(out, strings.Join(parts, " "))
				}
				continue
			}
			out = append(out, markdownInlineText(trimmed))
			continue
		}
		// 围栏代码块内部：代码正文原样保留（上游 code token 也是保留 text）。
		if _, ok := fenceOpen(line); ok && strings.HasPrefix(strings.TrimSpace(line), strings.TrimSpace(trimmed)) {
			if closedFenceLine(line) {
				inFence = false
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// closedFenceLine 判断这一行是不是围栏的收尾行（与开头同种围栏）。
func closedFenceLine(line string) bool {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return false
	}
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// tableSeparatorRow 判断表格分隔行（每格只由 -、: 和空白组成）。
func tableSeparatorRow(cells []string) bool {
	sawDash := false
	for _, cell := range cells {
		// 变量名刻意不叫 text：它是一格表格单元格，不是"消息正文"，
		// 别让 text-only 守卫把这条判据误当成"按正文判有无内容"。
		cellText := strings.TrimSpace(cell)
		if cellText == "" {
			return false
		}
		for _, r := range cellText {
			switch r {
			case '-':
				sawDash = true
			case ':', ' ':
			default:
				return false
			}
		}
	}
	return sawDash
}

// markdownInlineText 去掉行内 Markdown 标记，只留可见文字。
//
// 与上游 markdownText 的 token 分派等价：strong/em/del/link 取子内容，
// codespan/escape/image 取 text，html 去标签。未闭合的分隔符原样保留
// （宁可多留两个星号，也不要把后面的正文吃掉）。
func markdownInlineText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			// 转义：`\*` → `*`
			b.WriteByte(s[i+1])
			i += 2
		case s[i] == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j >= 0 {
				b.WriteString(s[i+1 : i+1+j])
				i += j + 2
				continue
			}
			b.WriteByte(s[i])
			i++
		case s[i] == '!' && i+1 < len(s) && s[i+1] == '[':
			if label, next, ok := inlineLinkLabel(s, i+1); ok {
				b.WriteString(markdownInlineText(label))
				i = next
				continue
			}
			b.WriteByte(s[i])
			i++
		case s[i] == '[':
			if label, next, ok := inlineLinkLabel(s, i); ok {
				b.WriteString(markdownInlineText(label))
				i = next
				continue
			}
			b.WriteByte(s[i])
			i++
		case s[i] == '<':
			if end := strings.IndexByte(s[i:], '>'); end > 0 {
				tag := s[i+1 : i+end]
				if looksHTMLTag(tag) { // 只吃真标签：`a < b` 不是标签
					i += end + 1
					continue
				}
			}
			b.WriteByte(s[i])
			i++
		case s[i] == '*' || s[i] == '_' || s[i] == '~':
			c := s[i]
			run := 1
			for i+run < len(s) && s[i+run] == c && run < 3 {
				run++
			}
			mark := s[i : i+run]
			if run >= 2 {
				mark = s[i : i+2]
			}
			if end := strings.Index(s[i+len(mark):], mark); end > 0 {
				inner := s[i+len(mark) : i+len(mark)+end]
				b.WriteString(markdownInlineText(inner))
				i += len(mark) + end + len(mark)
				continue
			}
			b.WriteString(s[i : i+run])
			i += run
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// inlineLinkLabel 解析从 `[` 开始的链接/图片：返回 label 与 `](dest)` 之后的位置。
func inlineLinkLabel(s string, open int) (label string, next int, ok bool) {
	close := strings.IndexByte(s[open+1:], ']')
	if close < 0 {
		return "", open, false
	}
	labelEnd := open + 1 + close
	if labelEnd+1 >= len(s) || s[labelEnd+1] != '(' {
		return "", open, false
	}
	end := strings.IndexByte(s[labelEnd+2:], ')')
	if end < 0 {
		return "", open, false
	}
	return s[open+1 : labelEnd], labelEnd + 2 + end + 1, true
}

// looksHTMLTag 粗略判定 `<…>` 里是不是 HTML 标签（字母开头的标签名、注释或闭合）。
func looksHTMLTag(tag string) bool {
	if tag == "" {
		return false
	}
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "!--") || strings.HasPrefix(trimmed, "/") {
		return true
	}
	r := rune(trimmed[0])
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// approvalNotificationMessage 是授权通知正文：会话名、工具名、asker 理由，
// 按这个顺序用 `：` 连接，空的部分丢掉（上游同形）。
//
// 理由**不承诺脱敏**：宿主给的原文（DSH 的沙箱升级会把模型自己写的
// justification 原样带上来）会出现在正文里；这里只把它规范成一行并限长。
// **工具参数一个字节都不读**——正文只认宿主单独给出的 tool 名与 reason，
// 面板里那份带命令与参数的完整描述不参与构造。
func approvalNotificationMessage(sessionTitle, toolName, reason string) string {
	parts := make([]string, 0, 3)
	if name := notificationSessionName(sessionTitle); name != "" {
		parts = append(parts, name)
	}
	if tool := notificationText(toolName, notificationMaxGraphemes); tool != "" {
		parts = append(parts, tool)
	}
	if reasonText := notificationText(reason, notificationMaxGraphemes); reasonText != "" {
		parts = append(parts, reasonText)
	}
	return strings.Join(parts, "：")
}

// notificationSessionName 是正文里的会话名：没命名给占位名，命名了按列让位
// （先截会话名，因为它最不重要；上游 NOTIFICATION_SESSION_NAME_MAX_WIDTH）。
func notificationSessionName(sessionTitle string) string {
	name := notificationText(sessionTitle, notificationMaxGraphemes)
	if name == "" {
		return notificationUntitledSessionDoc
	}
	return clipToWidthGraphemes(name, notificationSessionNameMaxWidth)
}

// questionNotificationMessage 是"等待回答 / 计划评审"的正文：header 与 question
// 用 `：` 连接，空的部分丢掉。选项、详情、链接、计划正文都不进正文——
// 锁屏上不是做选择的地方，正文只要说清"在等你什么"。
func questionNotificationMessage(header, question string) string {
	parts := make([]string, 0, 2)
	if h := notificationText(header, notificationMaxGraphemes); h != "" {
		parts = append(parts, h)
	}
	if q := notificationText(question, notificationMaxGraphemes); q != "" {
		parts = append(parts, q)
	}
	return strings.Join(parts, "：")
}

// ───────────────────────────────────────────────────────────── 完成通知的结算门

// completionTracker 是"这一轮成功结算了"的状态机，与上游 createTaskCompletion
// 同一套判据：只看**主会话已结算回复**，只认 reason.kind=completed。
//
//	replay      重放的帧整批不看（history 重放不是"刚刚发生"）
//	turn/start  新的一轮开始：候选清零
//	assistant/message  只认主会话（没有 parentCallId）、未被中断、本轮最新 seq 的正文
//	turn/end    reason.kind == completed 才算候选；aborted/error/max-tokens/interrupted
//	            /forked/blocked 都不是"完成"，一条都不通知
type completionTracker struct {
	generation uint64
	// epoch 是**轮次身份**：每个 turn/start 加一。
	//
	// 为什么不用帧里的 `turn` 字段：本仓宿主的 serve 是"一轮一个子进程"，
	// 每条 prompt 都从 turn=1 开始（真机取证见
	// tmp/GO8-RC2/_hw/evidence/probe-report.json：三轮都是 {"turn":1}）。
	// 拿 `turn` 当单调序号会得出"第二轮及以后的完成通知永远不发"——
	// 这正是 2026-11-13 真机抓包里唯一没发出来的那条。
	epoch          int
	completedEpoch int
	pending        bool
	responseEpoch  int
	responseSeq    int
	preview        string
}

func newCompletionTracker() completionTracker {
	return completionTracker{generation: ^uint64(0), completedEpoch: -1, responseEpoch: -1, responseSeq: -1}
}

func (t *completionTracker) reset() {
	t.epoch = 0
	t.completedEpoch = -1
	t.pending = false
	t.responseEpoch = -1
	t.responseSeq = -1
	t.preview = ""
}

// observe 吃一帧，返回"现在可以结算了"。只有 turn/end(completed) 会返回 true。
func (t *completionTracker) observe(f ServerFrame, generation uint64) bool {
	if t.generation != generation {
		t.reset()
		t.generation = generation
	}
	switch f.Type {
	case "session/reset":
		t.reset()
	case "turn/start":
		t.epoch++
		t.pending = false
		t.responseEpoch = t.epoch
		t.responseSeq = -1
		t.preview = ""
	case "assistant/message":
		var m struct {
			ParentCallID string          `json:"parentCallId"`
			Interrupted  bool            `json:"interrupted"`
			Message      json.RawMessage `json:"message"`
		}
		if json.Unmarshal(f.Data, &m) != nil {
			return false
		}
		// 子代理的回复与被打断的回复都不是"落后的主回复"，绝不进正文。
		if m.ParentCallID != "" || m.Interrupted {
			return false
		}
		if m.Message != nil {
			var inner struct {
				ParentCallID string `json:"parentCallId"`
				Interrupted  bool   `json:"interrupted"`
			}
			if json.Unmarshal(m.Message, &inner) == nil {
				if inner.ParentCallID != "" || inner.Interrupted {
					return false
				}
			}
		}
		if t.responseEpoch < 0 {
			t.responseEpoch = t.epoch
		}
		if t.responseEpoch != t.epoch || f.Seq <= t.responseSeq {
			return false // 只要本轮最新的那一条
		}
		t.responseSeq = f.Seq
		var text strings.Builder
		for _, blk := range parseAssistantBlocks(f.Data) {
			if blk.kind == "text" {
				text.WriteString(blk.text)
			}
		}
		if preview := taskCompletionPreview(text.String()); preview != "" {
			t.preview = preview
		}
	case "turn/end":
		var m struct {
			Reason struct {
				Kind string `json:"kind"`
			} `json:"reason"`
		}
		if json.Unmarshal(f.Data, &m) != nil {
			return false
		}
		// 同一轮的 turn/end 只认一次（重放/重复帧不许发第二条）。
		if t.completedEpoch >= t.epoch {
			return false
		}
		t.completedEpoch = t.epoch
		// 取消 / 失败 / 截断 / 分叉都不是"完成"。
		t.pending = m.Reason.Kind == "completed"
		return t.pending
	}
	return false
}

// ───────────────────────────────────────────────────────────── 外部通知插件让位

// notificationKind 是四类通知的身份：它只用于"要不要让位给外部通知插件"。
type notificationKind int

const (
	kindTaskComplete notificationKind = iota
	kindApproval
	kindQuestion
	kindPlanReview
)

// yieldToExternalNotifier 是"让位"规则本身（上游 questionNotice / 投递层同义）：
// 外部 DSH 通知插件已启用且正在运行时，完成 / 授权 / 提问三类让位（不发），
// **计划评审不让位**。
//
// 为什么计划评审必须例外：外部插件按 approval/asked 与 tool/call 映射自己的通知，
// 计划评审两类都不是；让位之后只剩它那条"空闲了"的通用提醒，
// 那**永远说不出**"有个计划评审在等你"，人就永远不知道该回去。
func yieldToExternalNotifier(kind notificationKind, active bool) bool {
	if !active {
		return false
	}
	return kind != kindPlanReview
}

// pendingAsk 是一次"已经声明、等结算"的提问/计划评审。
type pendingAsk struct {
	callID     string
	planReview bool
	header     string
	question   string
}

// notifyState 是 app 里与通知有关的全部状态。
//
// 为什么集中在一个结构里：通知是**跨帧**的（提问在 tool/call 声明、
// 在 tool/result 结算；完成在 turn/end 观察、在下一轮开始时作废），
// 散在各个字段里最容易出现的错是"切换会话时漏清一个"，
// 症状是上一个会话的提问在切过去之后才弹通知。
type notifyState struct {
	notifier  *terminalNotifier
	condition notificationCondition
	method    notificationMethod

	// replayUntil 是重放抑制水位：seq ≤ 它的帧是历史重放，不产生任何通知。
	// 宿主按 sinceSeq 重放 journal 时帧仍带 seq，所以水位是唯一的判据。
	replayUntil int

	// generation 是一次会话绑定的代际：切会话/新建会话时递增，
	// 让完成状态机把旧会话的候选整批作废。
	generation   uint64
	sessionTitle string
	completion   completionTracker

	// pendingAsks 按 callId 记还没结算的提问/计划评审（tool/call 声明）。
	pendingAsks map[string]pendingAsk
	// notifiedApprovals 记这一进程已经通知过的 requestId：宿主重连时会
	// 重发挂起审批（不带 seq），不能让同一问弹第二次。
	notifiedApprovals map[string]struct{}

	// focusKnown/focused：终端焦点状态。未知算聚焦（上游 getTerminalFocused）。
	focusKnown bool
	focused    bool

	// externalNotifier 是"外部 DSH 通知插件已启用且正在运行"这一事实。
	//
	// 本仓没有外部通知插件生态（Go 宿主不嵌 JS 插件运行时，R6），所以生产里
	// 它恒为 false——但**规则本身按上游实现**（见 yieldToExternalNotifier）：
	// 完成/授权/提问让位，计划评审不让位。哪天真有外部通知源接进来，
	// 只把这一个事实置真即可，不用改四类构造。
	externalNotifier bool
}

// terminalFocusedNow 返回"此刻终端是否聚焦"；没有焦点上报时按聚焦处理。
func (n *notifyState) terminalFocusedNow() bool {
	if !n.focusKnown {
		return true
	}
	return n.focused
}

// markFocus 记录一次焦点变化；收到按键也算聚焦（上游 failsafe）。
func (n *notifyState) markFocus(focused bool) {
	n.focusKnown = true
	n.focused = focused
}

// ───────────────────────────────────────────────────────────── app 侧入口

// notificationGeneration 返回当前会话绑定代际。
func (a *app) notificationGeneration() uint64 { return a.notify.generation }

// resetNotifications 在会话绑定/切换时作废全部跨帧通知状态。
func (a *app) resetNotifications(sessionTitle string, replayUntil int) {
	a.notify.generation++
	a.notify.replayUntil = replayUntil
	a.notify.sessionTitle = sessionTitle
	a.notify.completion = newCompletionTracker()
	a.notify.pendingAsks = nil
	a.notify.notifiedApprovals = nil
}

// frameIsReplay 判断这一帧是不是历史重放（seq 落在订阅时读到的水位以内）。
func (a *app) frameIsReplay(f ServerFrame) bool {
	return f.Seq > 0 && a.notify.replayUntil > 0 && f.Seq <= a.notify.replayUntil
}

// notifyAlertsEnabled 是四类通知共用的门禁。
//
// 判定只有一份实现（`terminalNotifier.alertsEnabled`）；这里只负责把 app 侧
// 当前的设置同步给投递器再问它——两份判定各写一遍的话，"设置改了但门禁没跟上"
// 会变成一类只在特定顺序下复现的 bug。
func (a *app) notifyAlertsEnabled() bool {
	if a.notify.notifier == nil {
		return false
	}
	if err := a.notify.notifier.configure(a.notify.condition, a.notify.method); err != nil {
		return false
	}
	return a.notify.notifier.alertsEnabled()
}

// deliverNotification 投递一条通知，并把"降级说明"写到状态行与正文里。
//
// 降级必须说出来：本版没有 windows toast，`auto` 在本地 Windows 又会解析成
// windows，不说明的话用户只会看到"什么都没发生"。
func (a *app) deliverNotification(kind notificationKind, title, message string) {
	if !a.notifyAlertsEnabled() {
		return
	}
	if yieldToExternalNotifier(kind, a.notify.externalNotifier) {
		return
	}
	note := a.notify.notifier.notify(title, message)
	if note == "" {
		return
	}
	a.err = note
	a.push("！" + note)
}

// notifyTaskComplete 投递"任务完成"。body 为空时只显示标题（上游同义），
// 而 BEL 档本来就只响铃、不携带正文。
func (a *app) notifyTaskComplete(body string) {
	a.deliverNotification(kindTaskComplete, notificationTitleTaskComplete, body)
}

// notifyApproval 投递"等待授权"。
func (a *app) notifyApproval(sessionTitle, toolName, reason string) {
	a.deliverNotification(kindApproval, notificationTitleApproval, approvalNotificationMessage(sessionTitle, toolName, reason))
}

// notifyQuestion 投递"等待回答"。
func (a *app) notifyQuestion(header, question string) {
	message := questionNotificationMessage(header, question)
	if message == "" {
		return // 正文为空的通知是纯噪声（上游同一条规则）
	}
	a.deliverNotification(kindQuestion, notificationTitleQuestion, message)
}

// notifyPlanReview 投递"计划评审"。计划正文永远不进正文（这里根本不接收它）。
func (a *app) notifyPlanReview(header, question string) {
	message := questionNotificationMessage(header, question)
	if message == "" {
		return
	}
	a.deliverNotification(kindPlanReview, notificationTitlePlanReview, message)
}

// observeNotificationFrame 让四类通知观察每一帧。replay 为真时整批不看。
func (a *app) observeNotificationFrame(f ServerFrame, replay bool) {
	if replay {
		return
	}
	switch f.Type {
	case "session/title":
		var m struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(f.Data, &m) == nil && strings.TrimSpace(m.Title) != "" {
			a.notify.sessionTitle = m.Title
		}
	case "tool/call":
		a.observeToolCallForNotices(f)
	case "tool/result":
		a.observeToolResultForNotices(f)
	case "approval/request":
		a.observeApprovalRequest(f)
	case "approval/answered", "approval/timeout", "approval/cancel":
		var m struct {
			RequestID string `json:"requestId"`
		}
		_ = json.Unmarshal(f.Data, &m)
		delete(a.notify.notifiedApprovals, m.RequestID)
	}
	if a.notify.completion.observe(f, a.notify.generation) {
		// 候选已经挂上，真正的结算在主循环"这一批帧处理完"之后（settleCompletion）。
		a.completionPending = true
	}
}

// observeApprovalRequest 只对**当前会话**的挂起审批发通知（后台会话与子代理安静）。
//
// 边界说明：宿主把 approval/request 推进被订阅会话的 journal，本 TUI 一次
// 只订阅一个会话，所以别的会话的审批根本到不了这里；这里仍按 sessionId
// 比对一次，作为"订阅之外没有第二条通路"的显式防线。宿主负载里没有
// agentId，子代理的审批与主会话的审批在协议上不可区分（见报告"未实现项"）。
func (a *app) observeApprovalRequest(f ServerFrame) {
	var m struct {
		RequestID string `json:"requestId"`
		SessionID string `json:"sessionId"`
		Tool      string `json:"tool"`
		Reason    string `json:"reason"`
		AgentID   string `json:"agentId"`
		ToolCall  struct {
			Title string `json:"title"`
		} `json:"toolCall"`
	}
	if json.Unmarshal(f.Data, &m) != nil || m.RequestID == "" {
		return
	}
	if m.SessionID != "" && a.sid != "" && m.SessionID != a.sid {
		return
	}
	if m.AgentID != "" && m.AgentID != a.sid {
		return // 子代理/其它 agent 的审批：只留在面板里
	}
	if a.notify.notifiedApprovals == nil {
		a.notify.notifiedApprovals = map[string]struct{}{}
	}
	if _, seen := a.notify.notifiedApprovals[m.RequestID]; seen {
		return
	}
	a.notify.notifiedApprovals[m.RequestID] = struct{}{}
	// 工具名只认宿主单独给出的 `tool` 字段；没有它才退到 toolCall.title。
	// **不读 toolCall.rawInput**：正文里绝不出现工具参数。
	toolName := m.Tool
	if toolName == "" {
		toolName = m.ToolCall.Title
	}
	a.notifyApproval(a.notify.sessionTitle, toolName, m.Reason)
}

// observeToolCallForNotices 记下"这次调用可能是一次提问/计划评审"，
// 等它的 tool/result 结算再决定发不发（失败/取消的调用一条都不发）。
func (a *app) observeToolCallForNotices(f ServerFrame) {
	name, _ := extractTool(f.Data)
	if name != "ask_user_question" && name != "exit_plan_mode" {
		return
	}
	callID := extractCallID(f.Data)
	if callID == "" {
		return
	}
	args := toolCallArguments(f.Data)
	ask := pendingAsk{callID: callID, planReview: name == "exit_plan_mode"}
	if name == "ask_user_question" {
		var parsed struct {
			Questions []struct {
				Header   string `json:"header"`
				Question string `json:"question"`
				Intent   struct {
					Kind string `json:"kind"`
				} `json:"intent"`
			} `json:"questions"`
		}
		if json.Unmarshal(args, &parsed) == nil && len(parsed.Questions) > 0 {
			ask.header = parsed.Questions[0].Header
			ask.question = parsed.Questions[0].Question
			// 计划评审的**上游判据**是"问题带 intent.kind=plan-review"，
			// 不是"用哪个工具问"。本宿主的 ask_user_question 会把模型给的
			// 问题对象原样带进 tool/call 的 arguments（schema 对问题项
			// additionalProperties: true），所以这里能读到同一个判别字段。
			if parsed.Questions[0].Intent.Kind == "plan-review" {
				ask.planReview = true
			}
		}
	} else {
		// exit_plan_mode：宿主不给 question header/question（schema 只有 plan），
		// 所以正文只能退到 tool/result 的 summary 字段；拿不到就不发。
		var parsed struct {
			Question string `json:"question"`
			Header   string `json:"header"`
		}
		_ = json.Unmarshal(args, &parsed)
		ask.header = parsed.Header
		ask.question = parsed.Question
	}
	if a.notify.pendingAsks == nil {
		a.notify.pendingAsks = map[string]pendingAsk{}
	}
	a.notify.pendingAsks[callID] = ask
}

// toolCallArguments 取 tool/call 帧里模型给的参数字符串。
//
// 帧里的形状是 `{"…","arguments":"<JSON 字符串>"}`（v4 ToolCallEvent 的字段），
// 拿不到就退回整帧（某些宿主把参数平铺在帧上）。
func toolCallArguments(data json.RawMessage) json.RawMessage {
	var call struct {
		Arguments string `json:"arguments"`
	}
	if json.Unmarshal(data, &call) == nil && strings.TrimSpace(call.Arguments) != "" {
		return json.RawMessage(call.Arguments)
	}
	return data
}

// observeToolResultForNotices 在工具**成功**结算时发出提问/计划评审通知。
//
// 失败不通知：参数不合法的 ask_user_question 没有真的把问题交到人手上，
// 弹"在等你回答"是假消息。取消/中断同理——这一轮的 tool/result 不会到，
// 候选自然过期（切会话时整批清空）。
func (a *app) observeToolResultForNotices(f ServerFrame) {
	callID := extractCallID(f.Data)
	if callID == "" {
		return
	}
	ask, ok := a.notify.pendingAsks[callID]
	if !ok {
		return
	}
	delete(a.notify.pendingAsks, callID)
	if toolResultIsError(f.Data) {
		return
	}
	if ask.planReview {
		header, question := ask.header, ask.question
		if header == "" && question == "" {
			// 宿主没给问题文本：正文只允许来自宿主**单独给出**的摘要字段
			// （`summary`），绝不允许拿 tool/result 的正文块凑——那里就是整份计划，
			// 而"计划评审从不带计划本身"是硬规则，宁可这一条不发。
			// 上游同样"正文为空就不发"：一条只有标题的空通知是纯噪声。
			question = toolResultSummary(f.Data)
		}
		a.notifyPlanReview(header, question)
		return
	}
	a.notifyQuestion(ask.header, ask.question)
}

// toolResultIsError 读工具结果帧里的 isError。
func toolResultIsError(data json.RawMessage) bool {
	var m struct {
		IsError bool `json:"isError"`
		Message struct {
			IsError bool `json:"isError"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &m) != nil {
		return false
	}
	return m.IsError || m.Message.IsError
}

// toolResultSummary 只读工具结果帧里宿主**单独给出**的 `summary` 字段。
//
// 刻意不碰 `message.content` 的正文块：exit_plan_mode 的正文就是整份计划，
// 一旦从那里取"第一行"，计划正文就漏进了通知正文——那正是规则禁止的事。
// 没有 summary 就返回空，让调用方走"正文为空 = 不发"的路径。
func toolResultSummary(data json.RawMessage) string {
	var m struct {
		Summary string `json:"summary"`
	}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	return strings.TrimSpace(m.Summary)
}

// settleCompletion 在"这一批帧处理完"之后决定这一轮要不要发完成通知。
//
// 门禁与上游一致：还没结算完（busy）或还有一次提交在飞（= 有排队跟进）就不发；
// 后一轮的 turn/start 已经在 drain 里把候选作废了。
// 上游另外两条（宿主队列里的排队项、活跃 goal）本 TUI 没有对应状态：
// 没有客户端队列、没有 goal 面板，故不构成门禁条件（见报告"未实现项"）。
func (a *app) settleCompletion() {
	a.completionPending = false
	t := &a.notify.completion
	if !t.pending {
		return
	}
	if a.busy || a.pendingSend != nil {
		return // 新一轮已经开始/提交在飞：不算"任务完成"
	}
	t.pending = false
	body := ""
	// 正文只取**这一轮**最新的已结算主回复；别的轮次的回复不算数。
	if t.responseEpoch >= 0 && t.responseEpoch == t.completedEpoch {
		body = t.preview
	}
	a.notifyTaskComplete(body)
}

// drainFrames 把**已经到达**的帧按到达顺序处理完，再结算完成通知。
//
// 为什么要 drain 而不是处理一帧就结算：turn/end 与"下一轮 turn/start"、
// 排队的 user/message 可能在同一批里连着到。先看一帧就发通知会造成
// "任务完成"紧接着又开跑一条新轮——那是假消息。
// 上游用 setImmediate 等同一批 dispatch 落定，这里是同一个意图的 Go 形态。
func (a *app) drainFrames(ch <-chan ServerFrame) {
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				a.settleCompletion()
				return
			}
			a.handleFrame(f)
		default:
			a.settleCompletion()
			return
		}
	}
}
