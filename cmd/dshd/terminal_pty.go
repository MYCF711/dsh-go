package main

// 官方 terminal.* 的 **PTY 后端**：把 internal/terminal 的真 ConPTY 会话翻译成官方字段。
//
// # 为什么需要这一层
//
// `serve_terminal_api.go` 里的 `officialTerminalAPI` 包在**管道式**持久 shell 之上，
// 它自己把三条缺口写在文件头：cols/rows 只是元数据（没有 SIGWINCH）、
// attach 的 screen 是原始输出尾部（没有屏幕模型）、`environment.scrollback` 恒为 0。
//
// 本文件是同一批官方方法的**真 PTY 实现**：
//
//	官方字段                真来源
//	info.cols/rows          CreatePseudoConsole 的初始尺寸
//	attach.screen           有界滚动缓冲的尾部（含 ANSI 原始字节）
//	attach.sequence         绝对已写字节数（单调）
//	attach.writable         进程还活着
//	environment.scrollback  真实保留行数（默认 1000，官方默认值）
//	info.exitCode           真实进程退出码（不是编的 0）
//	TerminalInput.resize    ResizePseudoConsole（真的作用在子进程上）
//
// # 职责边界
//
//   - 本文件只管官方字段/围栏/落盘；真 PTY 在 `internal/terminal`，
//     进程树归属在 `modules/exec/bash` 的 processControl。
//   - `Input`/`Output`/`Resize` 是给既有 `terminal.input`/`terminal.output`
//     语义用的**同一批真实操作**（官方 wire 里它们是 attach 的 uplink，
//     本宿主 HTTP 一问一答，所以拆成独立方法，见 WIRING.md）。
//
// # 接线
//
// 构造 `newOfficialPTYJobTerminalService(...)` 的返回值与既有
// `newOfficialJobTerminalService(...)` **方法集相同**，`serve.go` 换一行构造即可；
// 具体接线见 `$S/HS-08/WIRING.md`（本文件不改 serve.go）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dshgo/internal/terminal"
	"dshgo/internal/transport"
	"dshgo/modules/exec/bash"
)

// officialPTYDefaultScrollbackRows 与官方 TerminalEnvironment.scrollback 默认值一致（1000 行）。
const officialPTYDefaultScrollbackRows = 1000

// officialPTYScrollbackRowsFromEnv 读 `DSHD_TERMINAL_SCROLLBACK_ROWS`：
// 官方只约束"保留多少行"，宿主按部署环境调；非法值/0 走官方默认 1000。
func officialPTYScrollbackRowsFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("DSHD_TERMINAL_SCROLLBACK_ROWS"))
	if raw == "" {
		return officialPTYDefaultScrollbackRows
	}
	rows, err := strconv.Atoi(raw)
	if err != nil || rows <= 0 {
		return officialPTYDefaultScrollbackRows
	}
	return rows
}

// mapPTYError 把 PTY 核心的哨兵翻成官方形状：
//
//	terminal.ErrUnsupported → transport.ErrJTOfficialUnsupported（HTTP 501 not_implemented）
//	terminal.ErrClosed/ErrExited → transport.ErrJTOfficialTerminalGone（HTTP 404）
//
// 其余错误原样上抛：**不许**把实现 bug 吞成 501。
func mapPTYError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, terminal.ErrUnsupported) {
		return fmt.Errorf("%w: %v", transport.ErrJTOfficialUnsupported, err)
	}
	if errors.Is(err, terminal.ErrClosed) || errors.Is(err, terminal.ErrExited) {
		return fmt.Errorf("%w: %v", transport.ErrJTOfficialTerminalGone, err)
	}
	return err
}

// officialPTYEntry 是一条 PTY 终端身份。
type officialPTYEntry struct {
	id        string
	sessionID string
	title     string
	shell     transport.OfficialTerminalShell
	cwd       string
	cols      int
	rows      int

	term *bash.Terminal

	state    string
	exitCode *int
	errMsg   string

	closed   bool
	retained bool
	screen   string
	sequence int64

	createdAt time.Time
	lastUsed  time.Time

	// ---- 官方 api-terminal-controller 的四条生命周期预算（见下方 sweep/closeEntry）----
	idle          bool          // 最近一次 sweep 判定的闲置状态（activityPollIntervalMs）
	idleSince     time.Time     // 第一次被判闲置的时刻（unattendedTimeoutMs 从这里算）
	reaped        bool          // 因闲置超寿命而被回收
	disposeHow    string        // 销毁路径：grace-exit / grace-timeout / forced
	disposeWait   time.Duration // 宽限实际等了多久（disposeGraceMs 的实测值）
	disposePolite bool          // 是否走了「先礼」（给 shell 发 exit 机会）
	// cleanupAttempts / cleanupPending / cleanupNextAt 是清理失败的重试账
	// （官方 cleanupRetryMs）：失败不吞、不丢，按节奏重试到成功或进程退出。
	cleanupAttempts int
	cleanupPending  bool
	cleanupNextAt   time.Time
}

func (e *officialPTYEntry) key() string { return e.sessionID + "\x00" + e.id }

// officialPTYTerminalAPI 是 terminal.* 的真 PTY 实现。
type officialPTYTerminalAPI struct {
	mu       sync.Mutex
	home     string
	bash     *bash.Service
	cwdOf    func(sessionID string) (string, error)
	stateDir string
	// scrollbackRows 是官方 environment.scrollback 的真实来源（行）。
	scrollbackRows int

	entries   map[string]*officialPTYEntry
	closedIDs map[string]bool
	now       func() time.Time

	// maxTerminals 是每会话同时存活的终端上限（官方 terminal.maxTerminals）。
	// 构造函数里落 officialTerminalMaxTerminals；用例可以调小以免真起 8 条 PTY。
	maxTerminals int

	// ---- 官方 api-terminal-controller 的四条生命周期预算 ----
	// disposeGrace      官方 terminal.disposeGraceMs（默认 1000）：销毁前给 shell 的宽限
	// cleanupRetry      官方 terminal.cleanupRetryMs（默认 60000）：清理失败的重试节奏
	// activityPoll      官方 terminal.activityPollIntervalMs（默认 30000）：活跃度采样窗口
	// unattendedTTL     官方 terminal.unattendedTimeoutMs（默认 2h）：无人值守终端寿命上限
	disposeGrace  time.Duration
	cleanupRetry  time.Duration
	activityPoll  time.Duration
	unattendedTTL time.Duration

	// closeFn 是"真正关掉一条 PTY"的注入点（默认 a.bash.CloseTerminal）。
	// 用例靠它制造一次失败来验重试节奏。
	closeFn func(id string) error

	// closedEntries 留一份已关条目的账：销毁路径与清理重试都要在 delete(entries) 之后还能查。
	closedEntries map[string]*officialPTYEntry
	sweepStop     chan struct{}
	sweepOnce     sync.Once
}

// newOfficialPTYTerminalAPI 起 PTY 终端层。
//
// svc 必须是 terminal.open 用的**同一个** bash.Service（否则官方列表与既有终端两套实例）。
// scrollbackRows<=0 时按官方默认 1000 行。
func newOfficialPTYTerminalAPI(home string, svc *bash.Service, cwdOf func(string) (string, error), scrollbackRows int) (*officialPTYTerminalAPI, error) {
	return newOfficialPTYTerminalAPIWith(home, svc, cwdOf, scrollbackRows, true)
}

// newOfficialPTYTerminalAPIWith 是构造函数的实现，startSweep 决定要不要起后台 sweepLoop。
//
// 为什么留这个开关：sweepLoop 的 ticker 会周期性读 a.activityPoll / a.cleanupRetry /
// a.unattendedTTL / a.now。生命周期用例必须**先改这些配置、再手动 sweep(now)**
// 才能确定性地验四条预算；后台 loop 同时读同一批字段就是夹具与产品之间的竞争，
// `-race` 会先抓它、把真竞争淹掉（docs/race.md 记的同名教训）。
// 生产路径永远传 true（run/acp/serve 三处都走这个构造函数）。
func newOfficialPTYTerminalAPIWith(home string, svc *bash.Service, cwdOf func(string) (string, error), scrollbackRows int, startSweep bool) (*officialPTYTerminalAPI, error) {
	if svc == nil {
		return nil, fmt.Errorf("official PTY terminal：必须复用 terminal.open 的 bash.Service")
	}
	if cwdOf == nil {
		cwdOf = func(string) (string, error) { return "", nil }
	}
	if scrollbackRows <= 0 {
		scrollbackRows = 1000
	}
	a := &officialPTYTerminalAPI{
		home:           home,
		bash:           svc,
		cwdOf:          cwdOf,
		stateDir:       filepath.Join(home, "state", "terminals-pty"),
		scrollbackRows: scrollbackRows,
		entries:        map[string]*officialPTYEntry{},
		closedIDs:      map[string]bool{},
		now:            time.Now,
		maxTerminals:   officialTerminalMaxTerminals,
		disposeGrace:   officialTerminalDisposeGrace,
		cleanupRetry:   officialTerminalCleanupRetry,
		activityPoll:   officialTerminalActivityPoll,
		unattendedTTL:  officialTerminalUnattendedTimeout,
		closedEntries:  map[string]*officialPTYEntry{},
		sweepStop:      make(chan struct{}),
	}
	if err := os.MkdirAll(a.stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("建 PTY 终端状态目录失败: %w", err)
	}
	a.loadLayouts()
	if startSweep {
		go a.sweepLoop()
	}
	return a, nil
}

// Close 关掉本层启动的**全部** PTY 会话（只关自己记录的，不扫别人的）。
//
// 这里走**强制**销毁（不走 disposeGrace）：进程要退了，给每条终端各留 1s 宽限
// 会把退出时间按终端数放大。宽限只属于用户主动 `terminal.close`。
func (a *officialPTYTerminalAPI) Close() {
	if a == nil {
		return
	}
	a.stopSweep()
	a.mu.Lock()
	targets := make([]*officialPTYEntry, 0, len(a.entries))
	for _, e := range a.entries {
		targets = append(targets, e)
	}
	a.entries = map[string]*officialPTYEntry{}
	a.mu.Unlock()
	for _, e := range targets {
		a.closeEntryForced(e)
	}
}

// closeEntry 是用户主动销毁的路径：先按 disposeGraceMs「先礼」再「后兵」，
// 清理失败按 cleanupRetryMs 进重试队列（不吞错、不丢账）。
func (a *officialPTYTerminalAPI) closeEntry(entry *officialPTYEntry) {
	closeFn := a.closeFn
	if closeFn == nil {
		closeFn = a.bash.CloseTerminal
	}
	// 只有"还没决定过销毁路径"且 term 还在时才走宽限：closeEntry 可能因清理失败被重试，
	// 重试不能再发一次礼（那样每次重试都多等 1s）。
	if entry.term != nil && entry.disposeHow == "" && a.disposeGrace > 0 {
		entry.disposePolite = true
		start := time.Now()
		// 「礼」：让 shell 自己走 exit（历史/临时文件这类收尾才有机会跑）。
		_ = entry.term.Input("exit\r")
		deadline := start.Add(a.disposeGrace)
		for !termExited(entry.term) {
			remain := time.Until(deadline)
			if remain <= 0 {
				break
			}
			// 睡眠切片夹在剩余预算内：否则最后一次 sleep 会把实测等待顶过 grace。
			if remain > 5*time.Millisecond {
				remain = 5 * time.Millisecond
			}
			time.Sleep(remain)
		}
		entry.disposeWait = time.Since(start)
		if termExited(entry.term) {
			entry.disposeHow = "grace-exit"
		} else {
			entry.disposeHow = "grace-timeout"
		}
	} else if entry.disposeHow == "" {
		entry.disposeHow = "forced"
	}

	entry.closed = true
	if entry.term != nil {
		entry.cleanupAttempts++
		if err := closeFn(entry.term.ID()); err != nil {
			// 清理失败不吞：记账后按 cleanupRetryMs 重试（sweep 驱动）。
			entry.errMsg = err.Error()
			entry.cleanupPending = true
			entry.cleanupNextAt = a.now().Add(a.cleanupRetry)
			return
		}
		entry.term = nil
	}
	entry.cleanupPending = false
	entry.state = "exited"
}

// closeEntryForced 是进程退出路径：一刀切，不宽限。
func (a *officialPTYTerminalAPI) closeEntryForced(entry *officialPTYEntry) {
	closeFn := a.closeFn
	if closeFn == nil {
		closeFn = a.bash.CloseTerminal
	}
	entry.closed = true
	if entry.disposeHow == "" {
		entry.disposeHow = "forced"
	}
	if entry.term != nil {
		if err := closeFn(entry.term.ID()); err != nil {
			entry.errMsg = err.Error()
		}
		entry.term = nil
	}
	entry.cleanupPending = false
	entry.state = "exited"
}

// termExited 报这条 PTY 会话的进程是否已经退出（宽限轮询的判据）。
func termExited(term *bash.Terminal) bool {
	if term == nil {
		return true
	}
	if pty := term.PTY(); pty != nil {
		return pty.Exited()
	}
	return true
}

// sweepLoop 是四条生命周期预算的驱动：定时采样活跃度、回收无人值守终端、重试失败的清理。
func (a *officialPTYTerminalAPI) sweepLoop() {
	interval := a.activityPoll
	if interval <= 0 {
		interval = officialTerminalActivityPoll
	}
	if a.cleanupRetry > 0 && a.cleanupRetry < interval {
		interval = a.cleanupRetry
	}
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}
	if interval < 50*time.Millisecond {
		interval = 50 * time.Millisecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-a.sweepStop:
			return
		case now := <-tick.C:
			a.sweep(now)
		}
	}
}

func (a *officialPTYTerminalAPI) stopSweep() {
	a.sweepOnce.Do(func() {
		if a.sweepStop != nil {
			close(a.sweepStop)
		}
	})
}

// sweep 是一次采样：判定闲置、回收超寿命的无人值守终端、处理到期的清理重试。
//
// now 显式传入（不内部读时钟）：用例可以确定性地推进时间，不靠 sleep 赌时序。
func (a *officialPTYTerminalAPI) sweep(now time.Time) {
	var retries []*officialPTYEntry
	var reaps []*officialPTYEntry
	a.mu.Lock()
	for key, e := range a.entries {
		if e.closed {
			if e.cleanupPending && !now.Before(e.cleanupNextAt) {
				retries = append(retries, e)
			}
			continue
		}
		idle := a.activityPoll > 0 && now.Sub(e.lastUsed) > a.activityPoll
		e.idle = idle
		if !idle {
			e.idleSince = time.Time{}
			continue
		}
		if e.idleSince.IsZero() {
			e.idleSince = now
			continue
		}
		if a.unattendedTTL > 0 && now.Sub(e.idleSince) >= a.unattendedTTL {
			e.reaped = true
			delete(a.entries, key)
			a.closedIDs[key] = true
			a.closedEntries[key] = e
			reaps = append(reaps, e)
		}
	}
	// 已关但清理未完成的条目也可能要重试（不在 entries 里了）。
	for key, e := range a.closedEntries {
		if e.cleanupPending && !now.Before(e.cleanupNextAt) && !containsEntry(retries, e) {
			e.cleanupNextAt = a.now().Add(a.cleanupRetry)
			retries = append(retries, e)
			_ = key
		}
	}
	a.mu.Unlock()

	for _, e := range retries {
		a.retryCleanup(e)
	}
	for _, e := range reaps {
		a.closeEntry(e)
	}
}

func containsEntry(list []*officialPTYEntry, want *officialPTYEntry) bool {
	for _, e := range list {
		if e == want {
			return true
		}
	}
	return false
}

// retryCleanup 是清理重试：成功就落账，失败就再排下一次。
func (a *officialPTYTerminalAPI) retryCleanup(entry *officialPTYEntry) {
	closeFn := a.closeFn
	if closeFn == nil {
		closeFn = a.bash.CloseTerminal
	}
	id := ""
	if entry.term != nil {
		id = entry.term.ID()
	}
	entry.cleanupAttempts++
	if err := closeFn(id); err != nil {
		entry.errMsg = err.Error()
		entry.cleanupNextAt = a.now().Add(a.cleanupRetry)
		return
	}
	entry.cleanupPending = false
	entry.term = nil
	entry.state = "exited"
}

// ---- 用例/可观测读点（生产代码也用来做一致性检查）----

func (a *officialPTYTerminalAPI) entryOf(sessionID, id string) *officialPTYEntry {
	key := sessionID + "\x00" + id
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.entries[key]; e != nil {
		return e
	}
	return a.closedEntries[key]
}

func (a *officialPTYTerminalAPI) disposeOutcome(sessionID, id string) (how string, waited time.Duration, polite bool) {
	e := a.entryOf(sessionID, id)
	if e == nil {
		return "", 0, false
	}
	return e.disposeHow, e.disposeWait, e.disposePolite
}

func (a *officialPTYTerminalAPI) cleanupStats(sessionID, id string) (attempts int, done bool) {
	e := a.entryOf(sessionID, id)
	if e == nil {
		return 0, false
	}
	return e.cleanupAttempts, !e.cleanupPending
}

func (a *officialPTYTerminalAPI) isIdle(sessionID, id string) bool {
	e := a.entryOf(sessionID, id)
	return e != nil && e.idle
}

func (a *officialPTYTerminalAPI) isReaped(sessionID, id string) bool {
	e := a.entryOf(sessionID, id)
	return e != nil && e.reaped
}

// TerminalCreate 起一条**真 PTY** shell 会话并登记官方身份。
//
// 官方幂等：同一会话里同一 id 再 create，参数一致就回同一条（不新起进程）；
// 参数不一致即 400——静默忽略差异会让界面以为尺寸/shell 已生效。
func (a *officialPTYTerminalAPI) TerminalCreate(ctx context.Context, sessionID string, req transport.OfficialTerminalCreateRequest) (transport.OfficialWebTerminalInfo, error) {
	if !validJobActionPart(sessionID) {
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	id := strings.TrimSpace(req.ID)
	switch {
	case id == "":
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf("%w: 缺 id", transport.ErrJTOfficialBadParam)
	case utf8.RuneCountInString(id) > officialTerminalMaxIDRunes:
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf("%w: id 最多 %d 个字符",
			transport.ErrJTOfficialBadParam, officialTerminalMaxIDRunes)
	case !validJobActionPart(id):
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf("%w: id 含非法字符（路径分隔符/控制字符）",
			transport.ErrJTOfficialBadParam)
	}
	cols, rows := clampTerminalSize(req.Cols, req.Rows)
	if cols <= 0 || rows <= 0 {
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf("%w: cols/rows 必须是正整数", transport.ErrJTOfficialBadParam)
	}
	shell, err := resolveOfficialShell(req.ShellPath)
	if err != nil {
		return transport.OfficialWebTerminalInfo{}, err
	}
	cwd := a.sessionCwd(sessionID)
	key := sessionID + "\x00" + id

	a.mu.Lock()
	if a.closedIDs[key] {
		a.mu.Unlock()
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf("%w: %s 已在本会话关闭",
			transport.ErrJTOfficialTerminalGone, id)
	}
	if existing := a.entries[key]; existing != nil && !existing.closed {
		info := existing.info()
		sameShape := existing.cols == cols && existing.rows == rows &&
			strings.EqualFold(existing.shell.Path, shell.Path)
		a.mu.Unlock()
		if !sameShape {
			return transport.OfficialWebTerminalInfo{}, fmt.Errorf(
				"%w: 终端 %s 已存在且参数不同（现在 %d×%d %s），要换参数请先 terminal.close",
				transport.ErrJTOfficialBadParam, id, existing.cols, existing.rows, existing.shell.Path)
		}
		return info, nil
	}
	a.mu.Unlock()

	// 官方 `terminal.maxTerminals`（默认 8）：本会话同时存活的终端条数上限。
	// 放在 OpenPTY **之前**：先拒绝，别先起一个 conhost 再关掉。
	if limit := a.maxTerminals; limit > 0 {
		a.mu.Lock()
		open := 0
		for _, e := range a.entries {
			if e.sessionID == sessionID && !e.closed {
				open++
			}
		}
		a.mu.Unlock()
		if open >= limit {
			return transport.OfficialWebTerminalInfo{}, fmt.Errorf(
				"%w: 本会话已有 %d 条终端（上限 %d），先 terminal.close 再开",
				transport.ErrJTOfficialBadParam, open, limit)
		}
	}

	// 真 PTY：起不来说明本机没有 ConPTY 或 shell 起不来，原样上抛（不降级成管道）。
	term, err := a.bash.OpenPTY(bash.PTYOptions{
		Cwd:            cwd,
		Path:           shell.Path,
		Args:           shell.Args,
		Cols:           cols,
		Rows:           rows,
		ScrollbackRows: a.scrollbackRows,
	})
	if err != nil {
		return transport.OfficialWebTerminalInfo{}, mapPTYError(err)
	}

	now := a.now()
	entry := &officialPTYEntry{
		id: id, sessionID: sessionID,
		title: defaultTerminalTitle(shell, id),
		shell: transport.OfficialTerminalShell{Path: shell.Path, Args: shell.Args, Name: shell.Name},
		cwd:   cwd, cols: cols, rows: rows,
		term: term, state: "running",
		createdAt: now, lastUsed: now,
	}
	a.mu.Lock()
	a.entries[entry.key()] = entry
	a.mu.Unlock()
	a.persistSession(sessionID)
	return entry.info(), nil
}

// TerminalAttach 回一帧真实快照（官方首帧）。
func (a *officialPTYTerminalAPI) TerminalAttach(ctx context.Context, sessionID, id string) (transport.OfficialTerminalSnapshot, error) {
	entry, err := a.lookup(sessionID, id, "terminal.attach")
	if err != nil {
		return transport.OfficialTerminalSnapshot{}, err
	}
	screen, sequence, dead := a.screenOf(entry)
	writable := entry.state == "running" && !dead
	return transport.OfficialTerminalSnapshot{
		Type:     "snapshot",
		Sequence: sequence,
		Screen:   screen,
		Info:     entry.info(),
		Writable: writable,
		OneShot:  true,
	}, nil
}

// TerminalList 列这条会话的**真实存活**终端 + 被 retain 的（含重启后恢复的布局）。
func (a *officialPTYTerminalAPI) TerminalList(sessionID string) ([]transport.OfficialWebTerminalInfo, error) {
	if !validJobActionPart(sessionID) {
		return nil, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	a.mu.Lock()
	entries := make([]*officialPTYEntry, 0, len(a.entries))
	for _, e := range a.entries {
		if e.sessionID == sessionID && !e.closed {
			entries = append(entries, e)
		}
	}
	a.mu.Unlock()
	// 顺手把已经退出的标出来（真实状态，不是猜的）。
	for _, e := range entries {
		if e.state == "running" && e.term != nil {
			a.screenOf(e)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].createdAt.Equal(entries[j].createdAt) {
			return entries[i].id < entries[j].id
		}
		return entries[i].createdAt.Before(entries[j].createdAt)
	})
	out := make([]transport.OfficialWebTerminalInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.info())
	}
	return out, nil
}

// TerminalClose 真关：关会话 → 回收整棵进程树；重复 close 成功（官方语义）。
func (a *officialPTYTerminalAPI) TerminalClose(ctx context.Context, sessionID, id string) error {
	if !validJobActionPart(sessionID) {
		return fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	key := sessionID + "\x00" + id
	a.mu.Lock()
	if a.closedIDs[key] {
		a.mu.Unlock()
		return nil // 官方：repeated closes succeed
	}
	entry := a.entries[key]
	if entry == nil {
		a.mu.Unlock()
		// 不是本会话的终端要报 cross_session 而不是 404：既不能操作，也不该被当成"没有"。
		if _, err := a.lookup(sessionID, id, "terminal.close"); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", transport.ErrJTOfficialTerminalGone, id)
	}
	entry.closed = true
	delete(a.entries, key)
	a.closedIDs[key] = true
	// 关闭后仍要能查销毁路径与清理重试账，所以留一份已关条目的引用。
	a.closedEntries[key] = entry
	a.mu.Unlock()

	a.closeEntry(entry)
	a.persistSession(sessionID)
	return nil
}

// TerminalEnvironment 报**本层真实生效**的限制（每个数都能在代码里找到强制点）。
func (a *officialPTYTerminalAPI) TerminalEnvironment(ctx context.Context, sessionID string) (transport.OfficialTerminalEnvironment, error) {
	if !validJobActionPart(sessionID) {
		return transport.OfficialTerminalEnvironment{}, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	return transport.OfficialTerminalEnvironment{
		Cwd:              a.sessionCwd(sessionID),
		MaxInputBytes:    officialTerminalMaxInputBytes,
		MaxBufferedBytes: officialTerminalMaxScreenBytes,
		MaxCols:          officialTerminalMaxCols,
		MaxRows:          officialTerminalMaxRows,
		// 真 PTY：保留行数是真的（Scrollback 按行淘汰）。
		Scrollback: a.scrollbackRows,
	}, nil
}

// TerminalShells 是**真实探测**（与管道式实现共用同一套探测）。
func (a *officialPTYTerminalAPI) TerminalShells(ctx context.Context, sessionID string) ([]transport.OfficialTerminalShell, error) {
	if !validJobActionPart(sessionID) {
		return nil, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	return discoverOfficialShells()
}

// TerminalRename 改标题并落盘（真实持久化：宿主重启后 list 仍是新标题）。
func (a *officialPTYTerminalAPI) TerminalRename(ctx context.Context, sessionID, id, title string) error {
	entry, err := a.lookup(sessionID, id, "terminal.rename")
	if err != nil {
		return err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("%w: title 不能为空", transport.ErrJTOfficialBadParam)
	}
	a.mu.Lock()
	entry.title = title
	entry.lastUsed = a.now()
	a.mu.Unlock()
	a.persistSession(sessionID)
	return nil
}

// TerminalRetain 让身份"被保留"：屏幕尾部与布局落盘（宿主重启后仍可见）。
func (a *officialPTYTerminalAPI) TerminalRetain(ctx context.Context, sessionID, id string) error {
	entry, err := a.lookup(sessionID, id, "terminal.retain")
	if err != nil {
		return err
	}
	screen, sequence, _ := a.screenOf(entry)
	a.mu.Lock()
	entry.retained = true
	if sequence > entry.sequence {
		entry.sequence = sequence
	}
	if screen != "" {
		entry.screen = screen
	}
	entry.lastUsed = a.now()
	a.mu.Unlock()
	a.persistSession(sessionID)
	return nil
}

// Input 把一段输入写进真 PTY（官方 attach 的 uplink write）。
func (a *officialPTYTerminalAPI) Input(sessionID, id, data string) error {
	entry, err := a.lookup(sessionID, id, "terminal.input")
	if err != nil {
		return err
	}
	if len(data) > officialTerminalMaxInputBytes {
		return fmt.Errorf("%w: 单次写入 %d 字节超过上限 %d",
			transport.ErrJTOfficialBadParam, len(data), officialTerminalMaxInputBytes)
	}
	a.mu.Lock()
	term := entry.term
	entry.lastUsed = a.now()
	a.mu.Unlock()
	if term == nil {
		return fmt.Errorf("%w: %s 已不在运行", transport.ErrJTOfficialTerminalGone, id)
	}
	if err := term.Input(data); err != nil {
		return mapPTYError(err)
	}
	return nil
}

// Output 是增量读（游标是绝对字节偏移，来自真实滚动缓冲）。
func (a *officialPTYTerminalAPI) Output(sessionID, id string, since int) (transport.TerminalOutput, error) {
	entry, err := a.lookup(sessionID, id, "terminal.output")
	if err != nil {
		return transport.TerminalOutput{}, err
	}
	a.mu.Lock()
	term := entry.term
	if term != nil {
		entry.lastUsed = a.now()
	}
	a.mu.Unlock()
	if term == nil {
		return transport.TerminalOutput{Output: "", Next: since, Dead: true}, nil
	}
	pty := term.PTY()
	if pty == nil {
		return transport.TerminalOutput{}, fmt.Errorf("%w: %s 不是 PTY 终端", transport.ErrJTOfficialUnsupported, id)
	}
	data, next, dead := pty.Output(int64(since))
	return transport.TerminalOutput{Output: string(data), Next: int(next), Dead: dead}, nil
}

// Resize 是官方 TerminalInput.resize 的真实实现。
func (a *officialPTYTerminalAPI) Resize(sessionID, id string, cols, rows int) error {
	entry, err := a.lookup(sessionID, id, "terminal.resize")
	if err != nil {
		return err
	}
	cols, rows = clampTerminalSize(cols, rows)
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("%w: cols/rows 必须是正整数", transport.ErrJTOfficialBadParam)
	}
	a.mu.Lock()
	term := entry.term
	entry.lastUsed = a.now()
	a.mu.Unlock()
	if term == nil {
		return fmt.Errorf("%w: %s 已不在运行", transport.ErrJTOfficialTerminalGone, id)
	}
	if err := term.Resize(cols, rows); err != nil {
		return mapPTYError(err)
	}
	a.mu.Lock()
	entry.cols, entry.rows = cols, rows
	a.mu.Unlock()
	return nil
}

// lookup 取一条属于该会话的终端；不是本会话的一律 cross_session，不泄漏存在性。
func (a *officialPTYTerminalAPI) lookup(sessionID, id, method string) (*officialPTYEntry, error) {
	if !validJobActionPart(sessionID) {
		return nil, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: %s 缺 id", transport.ErrJTOfficialBadParam, method)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.entries[sessionID+"\x00"+id]
	if entry == nil || entry.closed {
		for _, other := range a.entries {
			if other.id == id && other.sessionID != sessionID && !other.closed {
				return nil, fmt.Errorf("%w: 终端 %s 属于另一个会话", transport.ErrCrossSession, id)
			}
		}
		return nil, fmt.Errorf("%w: %s", transport.ErrJTOfficialTerminalGone, id)
	}
	return entry, nil
}

// screenOf 取有界输出尾部与真实序号（已写出的字节数，单调）。
func (a *officialPTYTerminalAPI) screenOf(entry *officialPTYEntry) (string, int64, bool) {
	a.mu.Lock()
	term := entry.term
	a.mu.Unlock()
	if term == nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		return entry.screen, entry.sequence, entry.state != "running"
	}
	pty := term.PTY()
	if pty == nil {
		return entry.screen, entry.sequence, true
	}
	text, total := pty.Tail(officialTerminalMaxScreenBytes)
	dead := pty.Exited()
	code, hasCode := pty.ExitCode()
	a.mu.Lock()
	if total > entry.sequence {
		entry.sequence = total
	}
	if text != "" {
		entry.screen = text
	}
	if dead {
		entry.state = "exited"
		if hasCode {
			c := code
			entry.exitCode = &c
		}
	}
	a.mu.Unlock()
	return text, entry.sequence, dead
}

// info 是 entry → 官方 WebTerminalInfo 的唯一换算点。
func (e *officialPTYEntry) info() transport.OfficialWebTerminalInfo {
	info := transport.OfficialWebTerminalInfo{
		ID:       e.id,
		Title:    e.title,
		Shell:    e.shell,
		Cwd:      e.cwd,
		Cols:     e.cols,
		Rows:     e.rows,
		State:    e.state,
		ExitCode: e.exitCode,
		Error:    e.errMsg,
	}
	if info.Shell.Args == nil {
		info.Shell.Args = []string{}
	}
	return info
}

// sessionCwd 取会话工作目录（v4 头部记的那个）；不知道就回空串，不猜。
func (a *officialPTYTerminalAPI) sessionCwd(sessionID string) string {
	if a.cwdOf == nil {
		return ""
	}
	cwd, err := a.cwdOf(sessionID)
	if err != nil {
		return ""
	}
	return cwd
}

// layoutPath 与管道式终端分目录：两种后端的布局不混在一个文件里。
func (a *officialPTYTerminalAPI) layoutPath(sessionID string) string {
	return filepath.Join(a.stateDir, sessionID+".json")
}

// persistSession 写这条会话的布局：只写**值得保留**的条目（retained）。
func (a *officialPTYTerminalAPI) persistSession(sessionID string) {
	if !validJobActionPart(sessionID) {
		return
	}
	a.mu.Lock()
	layout := persistedTerminalLayout{Version: 1, SessionID: sessionID}
	for _, e := range a.entries {
		if e.sessionID != sessionID || e.closed || !e.retained {
			continue
		}
		layout.Terminals = append(layout.Terminals, persistedTerminalIdentity{
			ID: e.id, Title: e.title,
			ShellPath: e.shell.Path, ShellName: e.shell.Name, ShellArgs: e.shell.Args,
			Cwd: e.cwd, Cols: e.cols, Rows: e.rows,
			Retained: true, Screen: e.screen, SavedAt: a.now().UnixMilli(),
		})
	}
	path := a.layoutPath(sessionID)
	a.mu.Unlock()

	if len(layout.Terminals) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "transport: 删 PTY 终端布局 %s 失败: %v\n", path, err)
		}
		return
	}
	sort.Slice(layout.Terminals, func(i, j int) bool { return layout.Terminals[i].ID < layout.Terminals[j].ID })
	if len(layout.Terminals) > officialRetainedPerSession {
		sort.Slice(layout.Terminals, func(i, j int) bool { return layout.Terminals[i].SavedAt > layout.Terminals[j].SavedAt })
		layout.Terminals = layout.Terminals[:officialRetainedPerSession]
	}
	raw, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "transport: 编 PTY 终端布局失败: %v\n", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "transport: 写 PTY 终端布局 %s 失败: %v\n", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		fmt.Fprintf(os.Stderr, "transport: 提交 PTY 终端布局 %s 失败: %v\n", path, err)
	}
}

// loadLayouts 恢复被 retain 过的身份（进程已不存在 → state=exited、exitCode=null）。
func (a *officialPTYTerminalAPI) loadLayouts() {
	entries, err := os.ReadDir(a.stateDir)
	if err != nil {
		return
	}
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(a.stateDir, de.Name()))
		if err != nil {
			continue
		}
		var layout persistedTerminalLayout
		if err := json.Unmarshal(raw, &layout); err != nil {
			fmt.Fprintf(os.Stderr, "transport: PTY 终端布局 %s 损坏，忽略: %v\n", de.Name(), err)
			continue
		}
		if !validJobActionPart(layout.SessionID) {
			continue
		}
		for _, t := range layout.Terminals {
			if !t.Retained || !validJobActionPart(t.ID) {
				continue
			}
			entry := &officialPTYEntry{
				id: t.ID, sessionID: layout.SessionID, title: t.Title,
				shell: transport.OfficialTerminalShell{Path: t.ShellPath, Args: t.ShellArgs, Name: t.ShellName},
				cwd:   t.Cwd, cols: t.Cols, rows: t.Rows,
				state: "exited", retained: true, screen: t.Screen,
				errMsg:    "宿主重启：PTY 进程已不存在（保留的是布局与屏幕尾部）",
				createdAt: time.UnixMilli(t.SavedAt), lastUsed: time.UnixMilli(t.SavedAt),
			}
			a.entries[entry.key()] = entry
		}
	}
}

// ---------------------------------------------------------------------------
// 组装：与既有 officialJobTerminalService 同形，serve.go 换一行构造即可
// ---------------------------------------------------------------------------

// officialPTYJobTerminalService 把 job.*（复用既有 broker）与 terminal.*（PTY 后端）合成。
type officialPTYJobTerminalService struct {
	*officialJobService
	*officialPTYTerminalAPI
}

// Close 释放 PTY 终端层。
func (s *officialPTYJobTerminalService) Close() {
	if s != nil && s.officialPTYTerminalAPI != nil {
		s.officialPTYTerminalAPI.Close()
	}
}

// Handler 返回可以直接挂到 transport.Options 上的路由对象。
func (s *officialPTYJobTerminalService) Handler() *transport.OfficialJobTerminal {
	return &transport.OfficialJobTerminal{Svc: s}
}

// newOfficialPTYJobTerminalService 组装官方 11 个方法的 **PTY** 服务面。
//
// 参数与 newOfficialJobTerminalService 一一对应（多一个 scrollbackRows），
// Lead 的接线就是把 serve.go 里那一行构造换成这个函数。
func newOfficialPTYJobTerminalService(
	home string,
	svc *bash.Service,
	jobs *jobActionBroker,
	roster func(sessionID string) []transport.JobInfo,
	cwdOf func(sessionID string) (string, error),
	scrollbackRows int,
) (*officialPTYJobTerminalService, error) {
	terminals, err := newOfficialPTYTerminalAPI(home, svc, cwdOf, scrollbackRows)
	if err != nil {
		return nil, err
	}
	jobsSide := &officialJobService{roster: roster}
	if jobs != nil {
		jobsSide.output = jobs.Output
		jobsSide.stop = jobs.Stop
	}
	return &officialPTYJobTerminalService{officialJobService: jobsSide, officialPTYTerminalAPI: terminals}, nil
}
