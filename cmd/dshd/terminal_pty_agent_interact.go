package main

// agent 终端**交互**（`terminal_send` / `terminal_read` / `terminal_signal` /
// `terminal_close` / `terminal_list`）在真 ConPTY 上的实现。
//
// # 官方那一支（逐条 file:line）
//
// 注册表（dsh-terminal）——五个方法的**语义边界**都在这里：
//
//	startSend(owner,id,request)  `dsh-terminal/src/index.ts:224-235`
//	    closing → 'PTY session <id> is closing'；已有 active send →
//	    TerminalError('PTY session <id> already has an active send','SEND_ACTIVE')
//	read(owner,id,{offset,count})   :244-246
//	signal(owner,id,signal)         :255-257
//	kill(owner,id,reason)           :266-282（并发关闭 → 返回 false = already-closing）
//	list(owner)                     :289-293（**发布顺序**）
//	expectOwned                     :368-373
//	    'unknown PTY session <id>' / 'PTY session <id> belongs to another agent'
//	快照                            :375-386（sessionId,name?,type,pid?,status）
//
// 后端（terminal-panel）——这些句子会原样进模型请求，逐字照抄：
//
//	TAKEN_OVER          'The user has taken over this terminal; read it with terminal_read or open a new terminal.'
//	                    `terminal-panel/src/session.ts:15`
//	POLICY_CHANGED      'The session sandbox settings changed after this terminal opened, so it no longer accepts input;
//	                     open a new terminal with terminal_open. You can still read, signal or close this one.'
//	                    `:18`；判据 samePolicy `:48-51`、`:333`
//	send 前置条件顺序    `:328-339`（closing → active send → not running → control → policy → aborted）
//	'PTY session has exited'  `:331`
//	send 等待循环        `:248-292`（本轮询替代 changedWithin，见下）
//	settle              `:294-299`
//	read 参数校验        `:341-347`
//	                    'PTY read offset must be a non-negative safe integer'
//	                    'PTY read count must be a positive safe integer'
//	signal              `:349-356`（closing → 抛；成功 → targetPgid）
//	close               `:362-373`（control 是 agent 才真关；user-control 吞掉）
//	状态投影            `:82-84`（exited 的 signal 恒 null）
//
// 控制器拒绝（api-terminal-controller）：
//
//	requireAgentControl  'api-terminal-controller/lib/index.js:802-805'
//	    not-running → 'The terminal is not running'
//	    user-control → 'The user has taken over this terminal'（再被 modelError 换成 TAKEN_OVER）
//	pageLines            `:194-220`（本文件用 terminal.AgentScreenPage 的 Go 移植）
//
// Windows 信号语义（`dsh-subprocess-local`，官方唯一的 Windows 实现）：
//
//	foregroundPgid(shellPid) = shellPid   `windows-inspector.ts:114-116`
//	signalForeground                      `subprocess-local/src/terminal.ts:196-220`
//	    SIGKILL（pgid 就是 shell 自己）→ 'refusing to SIGKILL the terminal shell; terminate the terminal session instead'
//	    SIGINT → 往终端写 '\x03'
//	    SIGTSTP/SIGHUP → 'signal <s> is unsupported on Windows; only SIGINT, SIGTERM, and SIGKILL are available'
//	    其余 → taskkill /T（force 只在 SIGKILL 时加；SIGKILL 上一次已经拒绝）
//
// # 本宿主的三处如实差异
//
//  1. **没有 revision/change 通知**。官方的 send 等待靠 `terminal.changed(revision)`
//     精确唤醒（`session.ts:118-134`），本宿主只有"总字节游标"可轮询，所以按
//     `agentSendPollStep`(25ms) 轮询——与 `open.go` 启动等待的理由和量级相同。
//     `drain()`（官方等队列排空，`:281`）同理用"游标静默一小段时间"近似。
//  2. **没有屏幕模型**：read/send 的正文来自有界滚动缓冲（`internal/terminal/scrollback.go`），
//     不是 xterm 缓冲区重放后的逻辑行。与 `user_terminal_read` 已有的差异同源。
//  3. **close 的 already-closing 口径更宽**：官方区分"正在关（返回 false）"与
//     "早关完了（unknown PTY session）"；本宿主不保留那条时间线，已关过的 id 也返回
//     false（工具侧看到 `already-closing`），比官方宽松一档——不谎报"这次关掉了"。
//
// # 为什么不校验 sandbox 档位
//
// agent 终端只有 `danger-full-access` 能起来（confined 档没有 argv provider 会 503，
// 见 `terminal_pty_agent.go` 的 confineAgentArgv）。所以这里不重复那道闸门：
// 终端能存在就是全权档起的，send 的沙箱约束由那条 shell 自己的档位决定。

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"dshgo/internal/terminal"
	"dshgo/internal/transport"
)

// 官方原文常量（逐字；这些句子会进模型请求，翻译或改写都算篡改官方形状）。
const (
	// agentTakenOver 是"人已接管"的**模型可见**原文（`terminal-panel/src/session.ts:15`）。
	agentTakenOver = "The user has taken over this terminal; read it with terminal_read or open a new terminal."
	// agentPolicyChanged 是"会话沙箱档位变了、终端不再收输入"的原文（`session.ts:18`）。
	agentPolicyChanged = "The session sandbox settings changed after this terminal opened, so it no longer accepts input; " +
		"open a new terminal with terminal_open. You can still read, signal or close this one."
	// agentSendExited 是"send 时终端已不在运行"的原文（`session.ts:331`）。
	agentSendExited = "PTY session has exited"
	// agentControlExited 是控制器那句 "The terminal is not running"（`lib/index.js:803`）。
	agentControlExited = "The terminal is not running"
	// agentShellKillRefused 是"不许对 shell 本身 SIGKILL"的原文（`subprocess-local/src/terminal.ts:202-204`）。
	agentShellKillRefused = "refusing to SIGKILL the terminal shell; terminate the terminal session instead"
	// agentReadOffsetInvalid / agentReadCountInvalid 是 read 的两句参数校验原文
	// （`terminal-panel/src/session.ts:344-345`）。
	agentReadOffsetInvalid = "PTY read offset must be a non-negative safe integer"
	agentReadCountInvalid  = "PTY read count must be a positive safe integer"
	// agentSendAborted 是"写之前就被取消"的原文（`terminal-panel/src/session.ts:334`）。
	agentSendAborted = "PTY send aborted before write"
)

// 本宿主替代官方通知机制的两个轮询参数（见文件头差异 1）。
const (
	// agentSendPollStep 是 send 等待循环的轮询步长。官方靠 change 通知，没有这个数；
	// 取 25ms 与 `open.go` 的 startupPoll 同一量级：远小于官方的 300ms 静默阈值，
	// 又不会把桥/滚动缓冲打满。
	agentSendPollStep = 25 * time.Millisecond
	// agentSendDrainGrace 是 settle 前"等输出排空"的上限（官方 `drain()` 是等队列真的空，
	// 这里退而求其次：游标静默一个轮询步长就算排空，最多等这么久）。
	agentSendDrainGrace = 250 * time.Millisecond
)

// agentEntry 取一条属于该会话的 agent 终端，错误用**注册表原话**。
//
// 与 `lookup` 的分工：`lookup` 服务 wire 方法（错误是 transport 的四类错误码），
// 这里服务的是注册表语义的工具（`dsh-terminal/src/index.ts:368-373` 的两句话）。
func (a *officialPTYTerminalAPI) agentEntry(sessionID, id string) (*officialPTYEntry, error) {
	if a == nil {
		return nil, errors.New("没有 PTY 终端层（api 为 nil）：接线缺失")
	}
	if !validJobActionPart(sessionID) {
		return nil, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: agent 终端交互缺 id", transport.ErrJTOfficialBadParam)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry := a.entries[sessionID+"\x00"+id]; entry != nil && !entry.closed {
		return entry, nil
	}
	for _, other := range a.entries {
		if other.id == id && other.sessionID != sessionID && !other.closed {
			return nil, fmt.Errorf("PTY session %s belongs to another agent", id)
		}
	}
	return nil, fmt.Errorf("unknown PTY session %s", id)
}

// agentStatusOf 是官方 `statusOf`（`terminal-panel/src/session.ts:82-84`）的逐条实现：
// running 只有 kind；exited 带 exitCode，**signal 恒 null**（本宿主没有信号名）。
func agentStatusOf(state string, exitCode *int) terminal.AgentTerminalStatus {
	if state == "running" {
		return terminal.AgentTerminalRunning()
	}
	return terminal.AgentTerminalExited(exitCode, nil)
}

// agentSnapshotOf 刷新一条终端的真值并造出快照。
//
// 刷新走 `screenOf`（与 attach / terminal.output 同一个真值点：它会更新 state /
// exitCode / screen），所以 list 不会把已经退出的终端报成 running。
func (a *officialPTYTerminalAPI) agentSnapshotOf(entry *officialPTYEntry) terminal.AgentTerminalSnapshot {
	a.screenOf(entry) // 值刷新；返回的屏幕内容这里不用
	a.mu.Lock()
	snap := terminal.AgentTerminalSnapshot{
		SessionID: entry.id,
		Name:      entry.agentName,
		Type:      entry.agentType,
		Status:    agentStatusOf(entry.state, entry.exitCode),
	}
	term := entry.term
	a.mu.Unlock()
	if term != nil {
		if pty := term.PTY(); pty != nil {
			snap.PID = pty.PID()
		}
	}
	return snap
}

// AgentTerminalList 是官方 `ctx.terminals.list(owner)`（`dsh-terminal/src/index.ts:289-293`）。
//
// 只列**agent 开的**终端（`origin=agent`）：本宿主的 roster 是"会话维度"的，
// 而官方注册表是"owner（agent）维度"的。会话里 agent 开的终端就是 origin=agent 的那些
// ——被用户接管的仍是同一条（`control` 变 user、`origin` 不变），所以 list 之后
// 照样看得见它，与官方"接管后 session 记录还在注册表里"一致。
//
// 顺序按创建时间（官方是 Map 的插入顺序 = 发布顺序），时间相同再按 id 定序，
// 避免 map 遍历的随机顺序泄漏成"每次 list 顺序不一样"。
func (a *officialPTYTerminalAPI) AgentTerminalList(sessionID string) ([]terminal.AgentTerminalSnapshot, error) {
	if a == nil {
		return nil, errors.New("没有 PTY 终端层（api 为 nil）：接线缺失")
	}
	if !validJobActionPart(sessionID) {
		return nil, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	a.mu.Lock()
	entries := make([]*officialPTYEntry, 0, len(a.entries))
	for _, entry := range a.entries {
		if entry.sessionID != sessionID || entry.closed {
			continue
		}
		if entry.origin != transport.OfficialTerminalOriginAgent {
			continue
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].createdAt.Equal(entries[j].createdAt) {
			return entries[i].createdAt.Before(entries[j].createdAt)
		}
		return entries[i].id < entries[j].id
	})
	a.mu.Unlock()

	out := make([]terminal.AgentTerminalSnapshot, 0, len(entries))
	for _, entry := range entries {
		out = append(out, a.agentSnapshotOf(entry))
	}
	return out, nil
}

// beginAgentSend 是官方 send 的前置条件（`terminal-panel/src/session.ts:328-339` 的顺序）。
func (a *officialPTYTerminalAPI) beginAgentSend(ctx context.Context, entry *officialPTYEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry.agentSending {
		// 注册表原文（`dsh-terminal/src/index.ts:227`）：同一会话只许一个 send 在跑。
		return fmt.Errorf("PTY session %s already has an active send", entry.id)
	}
	if entry.term == nil || entry.state != "running" {
		return errors.New(agentSendExited)
	}
	if entry.control != transport.OfficialTerminalControlAgent {
		return errors.New(agentTakenOver)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", agentSendAborted, err)
	}
	entry.agentSending = true
	return nil
}

// endAgentSend 释放"已有在跑的 send"标记。
func (a *officialPTYTerminalAPI) endAgentSend(entry *officialPTYEntry) {
	a.mu.Lock()
	entry.agentSending = false
	a.mu.Unlock()
}

// AgentTerminalSend 是官方 `ctx.terminals.startSend(owner, id, request)`
// （`dsh-terminal/src/index.ts:224-235`）。
func (a *officialPTYTerminalAPI) AgentTerminalSend(ctx context.Context, sessionID, id string, req terminal.AgentSendRequest) (terminal.AgentSendResult, error) {
	entry, err := a.agentEntry(sessionID, id)
	if err != nil {
		return terminal.AgentSendResult{}, err
	}
	// 先刷一次真值：state 是"最近一次观察"的结果，send 的前置判定要看当下。
	a.screenOf(entry)

	// 策略比较（官方 samePolicy(launched, current)，`session.ts:333`）。
	a.mu.Lock()
	launchedMode, launchedRoot := entry.sandboxMode, entry.policyRoot
	a.mu.Unlock()
	if req.PolicyMode != launchedMode || req.PolicyWorkspaceRoot != launchedRoot {
		return terminal.AgentSendResult{}, errors.New(agentPolicyChanged)
	}

	if err := a.beginAgentSend(ctx, entry); err != nil {
		return terminal.AgentSendResult{}, err
	}
	defer a.endAgentSend(entry)

	a.mu.Lock()
	term := entry.term
	a.mu.Unlock()
	if term == nil {
		return terminal.AgentSendResult{}, errors.New(agentSendExited)
	}
	pty := term.PTY()
	if pty == nil {
		return terminal.AgentSendResult{}, fmt.Errorf("%w: %s 不是 PTY 终端", transport.ErrJTOfficialUnsupported, id)
	}

	// 官方在写之前记一个屏幕标记，结算时把标记之后的内容当作 viewport
	// （`session.ts:251`）。这里对应"写之前的绝对字节游标"。
	cursor := pty.Total()
	input := req.Text
	if req.Submit {
		// 官方只写 '\r'（`session.ts:253`：`${request.text}${request.submit ? '\r' : ''}`）。
		input += "\r"
	}
	if input != "" {
		if err := term.Input(input); err != nil {
			return terminal.AgentSendResult{}, mapPTYError(err)
		}
	}

	waitReason := waitAgentSend(ctx, pty, req)
	drainAgentOutput(pty)

	a.screenOf(entry)
	a.mu.Lock()
	status := agentStatusOf(entry.state, entry.exitCode)
	a.mu.Unlock()

	viewport, truncated := settleAgentViewport(pty, cursor, req.MaxReadBytes)
	return terminal.AgentSendResult{
		Viewport:   viewport,
		WaitReason: waitReason,
		Status:     status,
		Truncated:  truncated,
	}, nil
}

// waitAgentSend 是官方 send 等待循环（`terminal-panel/src/session.ts:248-292`）的移植。
//
// 判定顺序逐条照抄：stop/不 running → session_exit；超时 → timeout；静默到 idle →
// inferred_idle；静默到 prompt 且到了探测间隔 → 探测前台，前台可写 → stdin_read。
func waitAgentSend(ctx context.Context, pty *terminal.Session, req terminal.AgentSendRequest) string {
	timeout, idleSilence := req.Timeout, req.IdleSilence
	promptSilence, probeInterval := req.PromptSilence, req.ProbeInterval
	started := time.Now()
	revision := pty.Total()
	lastChange := started
	// lastProbe 的零值语义 = 官方循环里的 -Infinity：第一次够静默就立刻探测。
	var lastProbe time.Time

	for {
		if ctx.Err() != nil || pty.Exited() {
			return terminal.AgentWaitSessionExit
		}
		now := time.Now()
		if timeout > 0 && now.Sub(started) >= timeout {
			return terminal.AgentWaitTimeout
		}
		if idleSilence > 0 && now.Sub(lastChange) >= idleSilence {
			return terminal.AgentWaitInferredIdle
		}
		probeDue := promptSilence <= 0 || now.Sub(lastChange) >= promptSilence
		if probeDue && (lastProbe.IsZero() || probeInterval <= 0 || now.Sub(lastProbe) >= probeInterval) {
			lastProbe = now
			if cur := pty.Total(); cur != revision {
				// 官方：探测前发现 revision 变了 → 只更新 lastChange，不判定就绪。
				revision, lastChange = cur, time.Now()
				continue
			}
			if agentWaitsForInput(pty) {
				return terminal.AgentWaitStdinRead
			}
			continue
		}
		delay := agentSendPollStep
		if timeout > 0 {
			if remain := timeout - now.Sub(started); remain < delay {
				delay = remain
			}
		}
		if delay < time.Millisecond {
			delay = time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return terminal.AgentWaitSessionExit
		case <-timer.C:
		}
		if cur := pty.Total(); cur != revision {
			revision, lastChange = cur, time.Now()
		}
	}
}

// agentWaitsForInput 是官方 `waitsForInput`（`terminal-panel/src/session.ts:106-108`）：
// `foreground.processGroupId === terminal.pid || foreground.inputWaiting`。
//
// 本宿主没有前台探针，取值语义照官方 Windows 实现（`windows-inspector.ts:114-116`）：
// 前台进程组的 id **就是 shell 的 pid**，于是这个判据恒真——Windows 上"shell 静默到
// 就绪"正是 `stdin_read`。所以这里只需确认 shell 还活着。
func agentWaitsForInput(pty *terminal.Session) bool {
	return pty != nil && !pty.Exited()
}

// drainAgentOutput 近似官方 `drain()`（等已收到的输出落到屏幕/缓冲）。
func drainAgentOutput(pty *terminal.Session) {
	if pty == nil {
		return
	}
	last := pty.Total()
	deadline := time.Now().Add(agentSendDrainGrace)
	for time.Now().Before(deadline) {
		time.Sleep(agentSendPollStep)
		if cur := pty.Total(); cur == last {
			return
		} else {
			last = cur
		}
	}
}

// settleAgentViewport 取这次 send 期间新产生的、最新的 maxBytes 字节。
//
// 官方 `remainder()` 是"从屏幕标记到末尾"的最新 maxBytes 字节 + 丢失标记
// （`terminal-panel/src/session.ts:175-179`）。本宿主对应"从写之前的字节游标到现在"，
// 滚动缓冲已经淘汰掉的部分由 `Since` 的 lost 如实报告。
func settleAgentViewport(pty *terminal.Session, cursor int64, maxBytes int) (string, bool) {
	if pty == nil {
		return "", false
	}
	data, _, lost := pty.Scrollback().Since(cursor)
	text, bounded := terminal.NewestBytes(string(data), maxBytes)
	return text, lost || bounded
}

// AgentTerminalRead 是官方 `ctx.terminals.read(owner, id, request)`
// （`dsh-terminal/src/index.ts:244-246`）。
func (a *officialPTYTerminalAPI) AgentTerminalRead(ctx context.Context, sessionID, id string, req terminal.AgentReadRequest) (terminal.ScreenPage, error) {
	entry, err := a.agentEntry(sessionID, id)
	if err != nil {
		return terminal.ScreenPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return terminal.ScreenPage{}, err
	}
	// 官方校验（`terminal-panel/src/session.ts:344-345`）：读也校验，且不要求 control=agent
	// （人接管后仍然可以读，`session.ts:349-356` 的 signal 才要求控制权）。
	if req.Offset < 0 {
		return terminal.ScreenPage{}, errors.New(agentReadOffsetInvalid)
	}
	if req.Count <= 0 {
		return terminal.ScreenPage{}, errors.New(agentReadCountInvalid)
	}
	a.mu.Lock()
	term := entry.term
	stale := entry.screen
	a.mu.Unlock()
	screen := stale
	if term != nil {
		if pty := term.PTY(); pty != nil {
			// maxBytes<=0 = 要整段（滚动缓冲本身有界：默认 1000 行 / 1 MiB）。
			screen, _ = pty.Tail(0)
		}
	}
	return terminal.AgentScreenPage(screen, req.Offset, req.Count, req.MaxReadBytes), nil
}

// AgentTerminalSignal 是官方 `ctx.terminals.signal(owner, id, signal)`
// （`dsh-terminal/src/index.ts:255-257`）。
func (a *officialPTYTerminalAPI) AgentTerminalSignal(ctx context.Context, sessionID, id, signal string) (int, error) {
	entry, err := a.agentEntry(sessionID, id)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !terminal.AgentSignalAllowed(signal) {
		return 0, fmt.Errorf("%w: 未知信号 %q", transport.ErrJTOfficialBadParam, signal)
	}
	// 控制权与运行状态：官方 `requireAgentControl`（`api-terminal-controller/lib/index.js:802-805`）。
	a.screenOf(entry)
	a.mu.Lock()
	state, control, term := entry.state, entry.control, entry.term
	a.mu.Unlock()
	if term == nil || state != "running" {
		return 0, errors.New(agentControlExited)
	}
	if control != transport.OfficialTerminalControlAgent {
		// 官方：user-control 由 modelError 换成 TAKEN_OVER（`session.ts:73-75`）。
		return 0, errors.New(agentTakenOver)
	}
	if runtime.GOOS != "windows" {
		return 0, fmt.Errorf("%w: 本宿主的 PTY 只在 Windows 上有实现（internal/terminal/pty_other.go）",
			transport.ErrJTOfficialUnsupported)
	}
	pty := term.PTY()
	if pty == nil {
		return 0, fmt.Errorf("%w: %s 不是 PTY 终端", transport.ErrJTOfficialUnsupported, id)
	}
	// 官方 Windows：前台进程组的 id 就是 shell 的 pid（`windows-inspector.ts:114-116`）。
	pgid := pty.PID()
	if pgid <= 0 {
		// 官方 `signalForeground` 在解析不出前台组时抛这句（`subprocess-local/src/terminal.ts:199-201`）。
		return 0, fmt.Errorf("cannot resolve foreground process group for terminal %s", id)
	}
	if signal == "SIGKILL" {
		// 官方：pgid 就是 shell 自己 → 拒绝（`subprocess-local/src/terminal.ts:202-204`）。
		return 0, errors.New(agentShellKillRefused)
	}
	switch signal {
	case "SIGINT":
		// 官方 Windows 的 Ctrl-C 投递路径：往终端写 0x03（`terminal.ts:206-213`）。
		if err := term.Input("\x03"); err != nil {
			return 0, mapPTYError(err)
		}
		return pgid, nil
	case "SIGTSTP", "SIGHUP":
		return 0, fmt.Errorf(
			"signal %s is unsupported on Windows; only SIGINT, SIGTERM, and SIGKILL are available", signal)
	}
	// 只剩 SIGTERM：官方走 `taskkill /PID <pgid> /T`（不加 /F，force 只给 SIGKILL）。
	//
	// 官方对 taskkill 的结果**故意不检查**（"an already-absent tree … as tolerable as
	// ESRCH is for POSIX"，`windows-inspector.ts:158-166`）；这里同样不把它变成失败，
	// 否则"进程已经没了"会被报成投递失败。
	agentTaskkillTree(pgid)
	return pgid, nil
}

// agentTaskkillTree 对应官方 Windows 的 `signalGroup` → `taskkill /PID <pid> /T`
// （`windows-inspector.ts:127-129`、`:158-166`；force=false，因为 SIGKILL 已被拒绝）。
func agentTaskkillTree(pid int) {
	cmd := exec.Command("taskkill", "/PID", fmt.Sprint(pid), "/T")
	if err := cmd.Run(); err != nil {
		// 结果不参与判定（官方同行为）；不留痕迹会让人以为投递成功了所以这里仍不返回错误，
		// 但把实际情况记在 stdout/stderr 丢弃之外——见上面的注释。
		_ = err
	}
}

// AgentTerminalClose 是官方 `ctx.terminals.kill(owner, id, reason)`
// （`dsh-terminal/src/index.ts:266-282`）+ 后端 close（`terminal-panel/src/session.ts:362-373`）。
func (a *officialPTYTerminalAPI) AgentTerminalClose(ctx context.Context, sessionID, id string) (bool, error) {
	entry, err := a.agentEntry(sessionID, id)
	if err != nil {
		// 已关过（或正在关）：官方这条路径的返回值是 false = already-closing
		// （见文件头差异 3：本宿主把"已关完"也归到这一档，不谎报"这次关掉了"）。
		a.mu.Lock()
		closedBefore := a.closedIDs[sessionID+"\x00"+id]
		a.mu.Unlock()
		if closedBefore {
			return false, nil
		}
		return false, err
	}
	a.mu.Lock()
	control := entry.control
	a.mu.Unlock()
	if control != transport.OfficialTerminalControlAgent {
		// 官方 `PanelTerminalSession.close`：control 不是 agent 就**不真关**，
		// user-control 被吞掉（终端留给用户），注册表照样摘掉自己的记录 →
		// 工具侧看到 outcome=closed。
		return true, nil
	}
	if err := a.TerminalClose(ctx, sessionID, id); err != nil {
		return false, err
	}
	return true, nil
}
