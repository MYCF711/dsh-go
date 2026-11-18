package terminal

// agent 终端（`terminal_open` 开出来的那批）的**交互能力**：send / read / signal /
// close / list。
//
// # 官方那一支（逐条 file:line）
//
// 工具面（六个工具全在同一个包里注册）：
//
//	terminal_send    tool-terminal/src/index.ts:202-304（desc :204-205；参数 :206-213；
//	                 结果 oneOf 后台/前台 :215-235；执行 startSend :285-288）
//	terminal_read    tool-terminal/src/index.ts:306-337（desc :308；参数 :309-313；
//	                 结果 {text,totalLines,lineBegin,lineEnd,truncated} :316-326）
//	terminal_signal  tool-terminal/src/index.ts:339-362（desc :341；signal 枚举 :344；
//	                 结果 {delivered,targetPgid} :347-355）
//	terminal_close   tool-terminal/src/index.ts:364-393（desc :366；结果 {sessionId,outcome} :371-379）
//	terminal_list    tool-terminal/src/index.ts:395-408（desc :397；结果 = 快照数组 :400-402）
//	正文渲染         tool-terminal/src/render.ts:120-131（send）、:150-158（read）、
//	                 :166-177（list）、:93-98（boundTerminalText）
//
// 服务面（`ctx.terminals.*`，注册表在 dsh-terminal）：
//
//	startSend(owner,id,request)  dsh-terminal/src/index.ts:224-235
//	                             （closing → SESSION_CLOSING；已有在跑的 send → SEND_ACTIVE）
//	read(owner,id,{offset,count})  :244-246
//	signal(owner,id,signal)        :255-257
//	kill(owner,id,reason)          :266-282（并发关闭 → 返回 false = already-closing）
//	list(owner)                    :289-293
//	期待的所有权错误                :368-373（unknown PTY session / belongs to another agent）
//	会话快照形状                    :375-386（sessionId,name?,type,pid?,status）
//
// 后端（`PanelTerminalSession`，真正决定语义的那一层）：
//
//	send 前置条件（顺序）   terminal-panel/src/session.ts:328-339
//	    closing → 'PTY session is closing'
//    已有 active send → TerminalError('PTY session already has an active send','SEND_ACTIVE')
//    not running → 'PTY session has exited'
//    control !== 'agent' → TAKEN_OVER（:15）
//    policy 变了 → POLICY_CHANGED（:18、samePolicy :48-51）
//    signal 已取消 → 'PTY send aborted before write'
//	send 等待循环        :248-292
//	    drain → mark → write(text + (submit ? '\r' : '')) → 循环：
//	    stop/不 running → session_exit；>= timeoutMs → timeout；>= idleSilenceMs →
//	    inferred_idle；静默 >= promptSilenceMs 且距上次探测 >= probeIntervalMs 时探测
//	    前台；waitsForInput（pgid === shell pid 或 inputWaiting）→ stdin_read
//	settle               :294-299（drain → remainder → {viewport,waitReason,sessionStatus,truncated}）
//	read 校验            :341-347（offset 非负安全整数、count 正安全整数）
//	signal               :349-356（closing → 抛；成功 → {delivered:true,targetPgid}）
//	close                :362-373（control 是 agent 才真关；user-control 吞掉 = 终端留给人）
//	状态投影             :82-84（running / exited{exitCode,signal}）
//
// 等待阈值（官方 config，全部在 terminal-panel 包里）：
//
//	promptSilenceMs 300 / idleSilenceMs 3000 / probeIntervalMs 1000 /
//	timeoutMs 30000 / maxReadBytes 256KiB   terminal-panel/src/config.ts:47-51
//	顺序校验 promptSilenceMs <= idleSilenceMs <= timeoutMs  :59-68
//
// Windows 上的真实信号语义（`dsh-subprocess-local`，官方唯一的 Windows 实现）：
//
//	foregroundPgid(shellPid) = shellPid      windows-inspector.ts:114-116
//	signalForeground                        subprocess-local/src/terminal.ts:196-220
//	    SIGKILL 且 pgid === shell pid → 抛 'refusing to SIGKILL the terminal shell; terminate
//	                                    the terminal session instead'（:202-204）
//	    SIGINT → 往终端写 '\x03'（:206-213，Windows 没有进程组投递，Ctrl-C 就是这条）
//	    SIGTSTP / SIGHUP → 抛 'signal <s> is unsupported on Windows; only SIGINT, SIGTERM,
//	                        and SIGKILL are available'（:214-216）
//	    其余（SIGTERM，以及理论上非 shell 的 SIGKILL）→ taskkill（signalGroup :218；
//	    WindowsProcessInspector.signalGroup → taskkill /T，force 只在 SIGKILL 时加：
//	    windows-inspector.ts:127-129、:158-166）
//
// # 本宿主的三处如实差异（不是新语义，是缺了官方的前提）
//
//  1. **屏幕模型是 `x/vt` 而不是 `@xterm/headless`**（见 `vtscreen.go`）。官方 read
//     走 xterm 缓冲区重放后的逻辑行（`renderLines` + `pageLines`，
//     `dsh-api-terminal-controller/src/screen.ts:35-49`、`:57-77`），本宿主现在
//     走同一条取法（`Screen.Lines`），**不再是**按换行切原始输出。
//     剩下的差异只有"折行标记靠推断"与"resize 不重排"，逐条写在 `vtscreen.go`
//     文件头；这与 `user_terminal_read`（`internal/terminal/panel.go` 的
//     `NewestLinesPage`）无关，那是另一套口径。
//  2. **没有前台进程探针**。官方 `inspectForeground` 在 Windows 上就是用 shell pid
//     当伪进程组（见上），本宿主直接按同一取值语义处理：pgid = shell pid、不等待
//     输入未知。于是 `waitsForInput` 在 Windows 上恒真——这与官方 Windows 实现一致。
//  3. **页内行号按官方 `pageLines` 口径**（newest-relative，lineBegin=请求的 offset、
//     lineEnd=offset+实际返回行数），不是 `user_terminal_read` 那份 1 起绝对行号：
//     后者是 `dsh-terminal-panel` 自己的渲染（tools.ts），两者本来就是两套口径。
//
// # 为什么这份契约在 internal/terminal
//
// 与 `UserTerminals` 同一个理由：工具跑在 `dshd run` 子进程里，真终端在 serve 父进程，
// 中间的桥（`internal/terminal/bridge.go`）两侧都要这套类型；而 `internal/transport`
// 已经 import 本包，反向 import 就是环。

import (
	"context"
	"strings"
	"time"
)

// AgentWaitReason 的四个官方取值（`dsh-terminal/src/types.ts:14`）。
const (
	// AgentWaitStdinRead 表示前台程序正在等输入（Windows 上 pgid 恒等于 shell pid，
	// 所以"shell 静默到就绪"就落这一档，见文件头）。
	AgentWaitStdinRead = "stdin_read"
	// AgentWaitInferredIdle 表示输出静默到 idleSilenceMs 仍没能证实前台可写。
	AgentWaitInferredIdle = "inferred_idle"
	// AgentWaitTimeout 表示撞到单次 send 的上限。
	AgentWaitTimeout = "timeout"
	// AgentWaitSessionExit 表示等待期间会话退出（或被取消）。
	AgentWaitSessionExit = "session_exit"
)

// AgentSignalAllowed 报告一个信号名是不是工具面允许的五个之一
// （`dsh-terminal/src/types.ts:21`、`tool-terminal/src/index.ts:344`）。
func AgentSignalAllowed(signal string) bool {
	switch signal {
	case "SIGINT", "SIGTERM", "SIGKILL", "SIGTSTP", "SIGHUP":
		return true
	default:
		return false
	}
}

// AgentTerminalStatus 是官方 `TerminalSessionStatus`（`dsh-terminal/src/types.ts:24-26`）：
// running 只有 kind；exited 必带可空的 exitCode 与 signal。
//
// 拆成"kind + 两个可空字段"而不是两个 struct：本包不持有 JSON 形状（那是工具层的
// Data 约定），但语义必须能表达"exited 一定带着这两个字段（可以为 null）"。
type AgentTerminalStatus struct {
	// Kind 是 `running` 或 `exited`。
	Kind string
	// ExitCode 只在 exited 时有意义；nil = 不知道（官方允许 null）。
	ExitCode *int
	// Signal 只在 exited 时有意义；nil = 不知道（官方允许 null）。
	Signal *string
}

// AgentTerminalRunning 造一个 running 状态。
func AgentTerminalRunning() AgentTerminalStatus {
	return AgentTerminalStatus{Kind: "running"}
}

// AgentTerminalExited 造一个 exited 状态（exitCode/signal 可以都为 nil = 不知道）。
func AgentTerminalExited(exitCode *int, signal *string) AgentTerminalStatus {
	return AgentTerminalStatus{Kind: "exited", ExitCode: exitCode, Signal: signal}
}

// AgentTerminalSnapshot 是 `terminal_list` 的一行（官方 `TerminalSessionSnapshot`，
// `dsh-terminal/src/types.ts:118-130`）。
//
// name 是官方 spawn 请求里的 owner-local 显示名，本宿主在 `terminal_open` 时记进
// 条目（见 bridge 的 open 请求），空 = 这次开终端没给 name。
type AgentTerminalSnapshot struct {
	SessionID string
	Name      string
	Type      string
	PID       int
	Status    AgentTerminalStatus
}

// AgentSendRequest 是一次交互输入（官方 `TerminalSendRequest` 加后端阈值，
// `dsh-terminal/src/types.ts:48-56`、`terminal-panel/src/session.ts:28-34`）。
type AgentSendRequest struct {
	// Text 是原样写进终端的 UTF-8 文本。
	Text string
	// Submit 决定写完是否补一个回车（官方写的是 '\r'，`session.ts:253`）。
	Submit bool
	// PromptSilence 是"静默多久算回到可输入状态"（官方 promptSilenceMs）。
	PromptSilence time.Duration
	// IdleSilence 是"静默多久算推断空闲"（官方 idleSilenceMs）。
	IdleSilence time.Duration
	// ProbeInterval 是两次前台探测的最小间隔（官方 probeIntervalMs）。
	ProbeInterval time.Duration
	// Timeout 是单次 send 的上限（官方 timeoutMs）。
	Timeout time.Duration
	// MaxReadBytes 是返回正文的 UTF-8 字节上限（官方 maxReadBytes）。
	MaxReadBytes int
	// PolicyMode / PolicyWorkspaceRoot 是**会话当前**解析出的沙箱策略，用来和终端
	// 启动时的策略比较（官方 samePolicy，`session.ts:48-51`、判据 :333）。
	PolicyMode          string
	PolicyWorkspaceRoot string
}

// AgentSendResult 是一次 send 的结算（官方 `TerminalSendResult`，
// `dsh-terminal/src/types.ts:66-76`）。
type AgentSendResult struct {
	// Viewport 是结算时还没被调用方取走的有界正文（官方 `viewport`）。
	Viewport string
	// WaitReason 是四个 AgentWait* 之一。
	WaitReason string
	// Status 是结算时观察到的会话状态。
	Status AgentTerminalStatus
	// Truncated 表示正文被操作或滚动缓冲的上界丢过内容。
	Truncated bool
}

// AgentReadRequest 是一次有界回读（官方 `TerminalReadRequest`，
// `dsh-terminal/src/types.ts:88-94`）。
type AgentReadRequest struct {
	// Offset 是从最新一行往回的偏移（官方默认 0）。
	Offset int
	// Count 是请求的行数（官方默认 500，后端上限仍然生效）。
	Count int
	// MaxReadBytes 是正文的 UTF-8 字节上限（官方 maxReadBytes）。
	MaxReadBytes int
}

// AgentTerminalControls 是 agent 终端交互的全部宿主能力。
//
// 数据源（`UserTerminals` 的实现）实现了它，桥的五个 agent 终端 op 才可用；
// 没实现 → 工具层**响亮失败**（缺的正是官方 `ctx.terminals` 的那五个方法），
// 绝不返回空列表/假成功。
type AgentTerminalControls interface {
	// AgentTerminalList 列这条会话里**由 agent 开出来**的终端（官方 `list(owner)`）。
	AgentTerminalList(ctx context.Context, sessionID string) ([]AgentTerminalSnapshot, error)
	// AgentTerminalSend 写一次输入并等到就绪/静默/超时/退出（官方 `startSend`）。
	AgentTerminalSend(ctx context.Context, sessionID, id string, req AgentSendRequest) (AgentSendResult, error)
	// AgentTerminalRead 回一页**屏幕行**（官方 `read` → `readScreen` → `pageLines`）。
	// 读的是屏幕模型（`Screen.Lines`），不是原始输出字节。
	AgentTerminalRead(ctx context.Context, sessionID, id string, req AgentReadRequest) (ScreenPage, error)
	// AgentTerminalSignal 把信号投给前台进程组，返回真实收到的进程组 id
	// （官方 `signal` → `TerminalSignalResult.targetPgid`）。
	AgentTerminalSignal(ctx context.Context, sessionID, id, signal string) (int, error)
	// AgentTerminalClose 关闭一条 agent 终端；true = 这次真的关掉了，
	// false = 同一次关闭已经在进行（官方 `kill` 的返回值语义）。
	AgentTerminalClose(ctx context.Context, sessionID, id string) (bool, error)
}

// AgentScreenLines 把**原始输出文本**按换行切成行（不做屏幕重放）。
//
// 这条路径只留给"手上只有字节、没有屏幕模型"的调用方（例如已落盘的历史文本）。
// `terminal_read` **不走它**：那条路走屏幕模型（`Screen.Lines`），否则 `\r` 重画
// 与光标定位读不出来（见 `vtscreen.go` 文件头）。空文本 → 0 行（不是 1 个空行）。
func AgentScreenLines(screen string) []string {
	text := strings.ReplaceAll(screen, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// AgentScreenPage 是官方 `pageLines`（`dsh-api-terminal-controller/src/screen.ts:57-77`）
// 的逐条移植：newest-relative 分页 + 字节上限。
//
// 入参是**逻辑行**（官方 `renderLines` 的输出，本宿主 = `Screen.Lines()`），
// 不是原始文本：官方分页也是对屏幕行分页，不关心这些行是怎么来的。
//
// 三条容易写错的细节，全部照官方：
//
//   - `end = total - offset`、`begin = end - count`，两者都夹到 >= 0；
//   - 字节预算从**新到旧**累加，行间换行符算 1 字节（最后一行不算），撞上限就停；
//   - 一行都没留下但选中区间非空时，保留最新那一行的**尾部字节**并置 truncated；
//   - 返回的 lineBegin 就是请求的 offset（未夹紧），lineEnd = offset + 实际行数。
//
// `tailWithin` 在官方按 grapheme 簇截（`screen.ts:86-97`，用 Intl.Segmenter）；
// 本仓没有 grapheme 分段器，按 UTF-8 rune 边界截（不自造依赖），差别只在
// 组合字符/ZWJ 序列上可能出现半簇——这条差异写在这里，不假装逐字节一致。
func AgentScreenPage(lines []string, offset, count, maxBytes int) ScreenPage {
	total := len(lines)

	end := total - offset
	if end < 0 {
		end = 0
	}
	begin := end - count
	if begin < 0 {
		begin = 0
	}
	selected := lines[begin:end]

	// 字节预算从新到旧：selected 的最后一个元素后面没有换行符。
	bytes := 0
	first := len(selected)
	for first > 0 {
		next := len(selected[first-1])
		if first != len(selected) {
			next++ // 行间换行
		}
		if bytes+next > maxBytes {
			break
		}
		bytes += next
		first--
	}
	kept := selected[first:]
	truncated := first > 0
	if len(kept) == 0 && len(selected) > 0 {
		// 最新那一行本身就超上限：留它的尾部字节（官方 tailWithin），并如实置 truncated。
		tail, _ := NewestBytes(selected[len(selected)-1], maxBytes)
		kept = []string{tail}
		truncated = true
	}
	return ScreenPage{
		Text:       strings.Join(kept, "\n"),
		TotalLines: total,
		LineBegin:  offset,
		LineEnd:    offset + len(kept),
		Truncated:  truncated,
	}
}
