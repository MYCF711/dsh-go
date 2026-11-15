package terminal

// 会话里的终端对**工具面**的只读投影：`user_terminal_list` / `user_terminal_read`
// 需要的全部宿主能力，以及它们唯一的共享状态来源。
//
// # 为什么这两个工具要一份契约放在终端层
//
// 官方 rc.2 里这两个工具属于 `@deepseek-ai/dsh-terminal-panel`，但数据不在那个包里：
// 它们只是 `ctx.terminalController` 的渲染层——
//
//	list         → terminalController.list(agentId).filter(info => info.control === 'user')
//	             （`terminal-panel/src/tools.ts:74`；`WebTerminalInfo` 见
//	              `dsh-api-terminal-controller/lib/typert.host.js:188` 的 control 字段）
//	read         → terminalController.readScreen(agent, id, {offset:0, count, maxBytes})
//	             （`tools.ts:144`；`TerminalScreenPage` 是控制器 `readScreen` 的产出，见 `dsh-api-terminal-controller/lib/index.js:764`/`:1243`）
//	子代理拒绝    → agent.session.header.origin === 'subagent'（`tools.ts:35`）
//
// 也就是说 `control` **不是**工具自己算的，它就是 `terminal.watch` / `terminal.list`
// 推给侧栏的那一份 `WebTerminalInfo.control`。本宿主若在工具里另建一份"谁是用户终端"
// 的判断，就会出现"侧栏显示 A、工具说 B"的两份真值——所以这里只声明**取真值的能力**，
// 不持有任何状态：
//
//	terminal.watch / terminal.list / terminal.attach  ← 同一份 host roster
//	                                    ↑
//	                     UserTerminals（本文件的接口）
//	                     ↑                          ↑
//	         user_terminal_* 工具              （尚无 agent 终端创建路径）
//
// # 本宿主当前的事实（A2 报告 §0 与 `internal/transport/aligned()` 同源）
//
// 没有 `openForAgent` / `agentLaunch` 对应的 agent 终端分配路径，所以 roster 里
// `origin`/`control` 恒为 `user`；这里的 `Control` 字段照样逐条透传，等将来真的有了
// agent 终端车道，工具侧的筛选一行都不用改（筛选判据是值，不是"当前事实"）。

import (
	"context"
	"strings"
	"unicode/utf8"

	"dshgo/internal/kernel"
)

// 官方 `UserTerminal.running` 的三个取值（`terminal-panel/src/tools.ts:25`）。
const (
	// PanelRunningYes 表示前台有命令在跑（官方 `inspectActivity === 'busy'`）。
	PanelRunningYes = "yes"
	// PanelRunningNo 表示前台空闲，或终端已经不在 running 状态。
	PanelRunningNo = "no"
	// PanelRunningUnknown 表示无法观测（本宿主没有前台进程探针，见下）。
	PanelRunningUnknown = "unknown"
)

// 官方 `WebTerminalInfo.control` 的两个取值（`typert.host.js:188`）。
const (
	// PanelControlUser 表示输入归用户（用户终端，或被用户接管后的 agent 终端）。
	PanelControlUser = "user"
	// PanelControlAgent 表示输入归 agent（agent 自己开的、用户还没接管）。
	PanelControlAgent = "agent"
)

// 官方会话头 `origin` 的唯一已知取值（`persistence.md:174` 的 `origin?: 'subagent'`）。
const (
	// PanelOriginSubagent 表示这条会话是子代理会话，用户看不到它的侧栏。
	PanelOriginSubagent = "subagent"
	// PanelOriginUser 表示主会话（用户正在看的那个）。
	PanelOriginUser = "user"
)

// PanelTerminal 是一条终端在工具面的投影（官方 `UserTerminal`，
// `terminal-panel/src/tools.ts:21-26`）。
//
// 字段与官方逐条对应：
//
//	id/title/cwd      官方 WebTerminalInfo 的同名字段（`typert.host.js:188` 定字段；`dsh-api-terminal-controller/lib/index.js:1173-1176` 的 `list()` 是整个 roster 的唯一投影点）
//	Control           **同一份**状态：`WebTerminalInfo.control`（决定工具看得见看不见）
//	State             `running|failed|exited`（官方用它判断"还活着吗"）
//	Running           官方 `running: 'yes'|'no'|'unknown'`（本宿主由适配层算好）
//	ExitCode          已退出时的真实退出码（给人看的原因，不是编的 0）
type PanelTerminal struct {
	ID       string
	Title    string
	Cwd      string
	Control  string
	State    string
	Running  string
	ExitCode *int
}

// ControlUser 报告这条终端是不是用户控制的（工具唯一可见的那一类）。
func (t PanelTerminal) ControlUser() bool { return t.Control == PanelControlUser }

// ScreenPage 是官方 `TerminalScreenPage` 的形状（`dsh-api-terminal-controller`；
// 消费点 `terminal-panel/src/tools.ts:144` 与渲染 `:55-68`）。
//
// 语义与官方一致：Text 是**这一段**的正文，LineBegin/LineEnd 是它在整屏里的
// 行号（1 起），TotalLines 是整屏行数，Truncated 表示"更旧的内容没给"。
type ScreenPage struct {
	Text       string
	TotalLines int
	LineBegin  int
	LineEnd    int
	Truncated  bool
}

// Empty 报告这一页是否没有任何正文。
func (p ScreenPage) Empty() bool { return strings.TrimSpace(p.Text) == "" }

// UserTerminals 是本宿主对"用户终端"的只读能力。
//
// 实现方必须是**与 `terminal.watch` / `terminal.list` 同一份**真值来源；
// 不许另建状态机（两份状态必然漂成"侧栏有、工具没有"）。
type UserTerminals interface {
	// ListUserTerminals 列这条会话的全部终端（含 agent 控制的），
	// 过滤由调用方做——工具只筛 `control === 'user'`，但这不代表实现可以提前丢条目：
	// 未来 `user_terminal_read` 要能对"agent 终端被用户接管"给出准确解释。
	ListUserTerminals(ctx context.Context, sessionID string) ([]PanelTerminal, error)
	// ReadUserScreen 读一条终端渲染屏幕的最新 lines 行（lines<=0 由调用方保证为正）。
	ReadUserScreen(ctx context.Context, sessionID, id string, lines int) (ScreenPage, error)
	// SessionOrigin 返回会话来源：`user`（主会话）或 `subagent`（子代理会话）。
	// 空串 = 宿主判断不了；工具据此**不**拒绝（宁可可用，也不误伤主会话）。
	SessionOrigin(ctx context.Context, sessionID string) (string, error)
}

// UserTerminalsKey 是宿主提供 UserTerminals 的服务键。
//
// 为什么键在终端层而不是工具包：实现要坐在 `internal/transport` 的 terminal 服务上
// （同一份 roster），工具包只消费接口。键放工具包里会让 transport 反向 import 模块。
var UserTerminalsKey = kernel.NewKey[UserTerminals]("userterminals")

// DefaultPanelReadBytes 是官方 `maxReadBytes` 的默认值（`terminal-panel/src/config.ts:51`，
// 256 KiB）。这里复述一份是为了让"默认值只有一个出处"能被测试钉住。
const DefaultPanelReadBytes = 256 * 1024

// NewestBytes 取字符串最新的 maxBytes 个字节，且不切断 UTF-8 字符。
//
// 对应官方 `newestBytes`（`terminal-panel/src/session.ts:142-147`，用 TextRetainer
// 的 tail 模式）。第二个返回值表示"前面的内容被丢了"。
func NewestBytes(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", len(s) > 0
	}
	if len(s) <= maxBytes {
		return s, false
	}
	cut := len(s) - maxBytes
	// 不能从多字节字符中间起头：否则模型收到的正文里第一个字是半个字节。
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:], true
}

// NewestLinesPage 把"输出尾部"换算成官方的 ScreenPage。
//
// # 为什么输入是原始尾部而不是"屏幕"
//
// 官方 `readScreen` 走的是终端控制器的**屏幕模型**（重放光标/清屏序列后的结果）。
// 本宿主没有屏幕模型：PTY 侧只有有界滚动缓冲（`internal/terminal/scrollback.go`），
// `terminal.attach` 的 `screen` 就是它的尾部字节。所以这里按换行切行、剥掉行尾 `\r`
// （PTY 的换行是 CRLF），**不**重放 `\r`/清屏——那需要一份真正的屏幕模型，属于
// "没实现就说没有"，不在这里假装。
//
// 字节上限的淘汰顺序：先按**整行**淘汰最旧的（这样 LineBegin 仍是真实行号），
// 若最新那一行本身就超过上限，才截字节（此时行号不动，Truncated 置位）。
func NewestLinesPage(screen string, lines, maxBytes int) ScreenPage {
	if lines <= 0 {
		lines = 1
	}
	text := strings.ReplaceAll(screen, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return ScreenPage{}
	}
	parts := strings.Split(text, "\n")
	total := len(parts)

	// 先满足"最新 lines 行"。
	begin := total - lines
	if begin < 0 {
		begin = 0
	}
	truncated := false

	// 再满足字节上限：**最新那一行永远保留**（否则会得到一页空白+一个偏移的行号），
	// 从第二新往前累加，加不下就停。最新行本身超上限时交给下面的字节截断处理。
	if maxBytes > 0 {
		next := total - 1
		size := len(parts[next])
		for i := total - 2; i >= begin; i-- {
			add := len(parts[i]) + 1 // 行间换行
			if size+add > maxBytes {
				truncated = true
				break
			}
			size += add
			next = i
		}
		begin = next
	}

	body := strings.Join(parts[begin:], "\n")
	// 单行超上限：只能截字节，行号保持（官方同样允许"一行被截一半"）。
	if maxBytes > 0 && len(body) > maxBytes {
		trimmed, lost := NewestBytes(body, maxBytes)
		body = trimmed
		truncated = truncated || lost
	}
	return ScreenPage{
		Text:       body,
		TotalLines: total,
		LineBegin:  begin + 1,
		LineEnd:    total,
		Truncated:  truncated,
	}
}
