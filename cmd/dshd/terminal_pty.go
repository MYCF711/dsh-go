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
}

// newOfficialPTYTerminalAPI 起 PTY 终端层。
//
// svc 必须是 terminal.open 用的**同一个** bash.Service（否则官方列表与既有终端两套实例）。
// scrollbackRows<=0 时按官方默认 1000 行。
func newOfficialPTYTerminalAPI(home string, svc *bash.Service, cwdOf func(string) (string, error), scrollbackRows int) (*officialPTYTerminalAPI, error) {
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
	}
	if err := os.MkdirAll(a.stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("建 PTY 终端状态目录失败: %w", err)
	}
	a.loadLayouts()
	return a, nil
}

// Close 关掉本层启动的**全部** PTY 会话（只关自己记录的，不扫别人的）。
func (a *officialPTYTerminalAPI) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	targets := make([]*officialPTYEntry, 0, len(a.entries))
	for _, e := range a.entries {
		targets = append(targets, e)
	}
	a.entries = map[string]*officialPTYEntry{}
	a.mu.Unlock()
	for _, e := range targets {
		a.closeEntry(e)
	}
}

func (a *officialPTYTerminalAPI) closeEntry(entry *officialPTYEntry) {
	entry.closed = true
	if entry.term != nil {
		if err := a.bash.CloseTerminal(entry.term.ID()); err != nil {
			entry.errMsg = err.Error()
		}
		entry.term = nil
	}
	entry.state = "exited"
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
