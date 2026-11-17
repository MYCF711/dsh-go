package main

// 官方 **agent 终端分配** 的宿主实现：`openForAgent` + `agentLaunch`。
//
// # 官方契约（逐条 file:line，全部来自 `@deepseek-ai/dsh-api-terminal-controller`）
//
//	openForAgent(agent, request, signal)     lib/index.js:1213-1240（TS: src/index.ts:222-241）
//	  title = terminalTitle(request.title)   lib/index.js:956-959（去首尾空白；空 或 >120 即抛）
//	  dimensions(cols, rows)                 lib/index.js:1541（cols∈[2,maxCols=500]、rows∈[1,maxRows=200]，越界**抛**而不是夹）
//	  requireCapacity(owner)                 lib/index.js:1391-1397（会话存活终端 >= maxTerminals=8 → terminal/limit-reached）
//	  agent 容量                              lib/index.js:1219-1222（origin=agent && control=agent && state=running 计数 >= maxAgentTerminals=4）
//	  id = randomUUID()                      lib/index.js:1223
//	  → agentLaunch                          lib/index.js:1449-1478（TS: src/index.ts:448-468）
//	      cwd   = request.cwd ?? workingDirectory.ensure(agent, signal)
//	      shell = agentShell(agent, request.preferredShells, signal)     lib/index.js:1512-1521
//	      argv  = confine(agent, [shell.path, ...shell.args], request.policy, signal)  lib/index.js:1522-1530
//	      env   = {...request.env, DSH_SESSION_ID: agent.id, ...history}  :1456-1461
//	      info  = {…, origin:"agent", control:"agent", sandboxMode: policy.mode}  :1462-1467
//	      release = 删 /tmp/dsh-agent-history-<id>（仅 posix + workspace-write）  :1466, :1476-1511
//
// # confine 的语义（**不自造**）
//
//	async confine(agent, argv, policy, signal) {                 lib/index.js:1522-1530
//	  if (policy.mode === "danger-full-access") return argv     ← 全权档**不包** argv
//	  const sandbox = agent.ctx.get("sandbox")
//	  if (sandbox === void 0) throw new Error(`Agent terminals in sandbox mode "${policy.mode}" require a sandbox provider …`)   ← 缺 provider 就抛
//	  return (await sandbox.confine(argv, {...policy, mode: policy.mode}, signal)).argv
//	}
//
// 所以本宿主的 confine 也只有两支：全权档原样返回；其余两档**必须有**一个
// argv 级的沙箱 provider，没有就响亮失败。本仓 `internal/sandbox` 目前是
// **策略层**（`EnforcementPolicyOnlyNoOSIsolation`，`internal/sandbox/policy.go:69`；
// 三档文档 `docs/sandbox-policy.md`）：它约束的是工具调用与 `dshd run` 子进程的
// `DSHD_SANDBOX_MODE`，**不**能约束一条交互式 shell 里敲下的任意命令（那需要
// OS 级隔离）。因此生产装配把 `confiner` 留空：`read-only` / `workspace-write`
// 的 agent 终端会**如实 501 失败**，而不是"A 说 confined、其实全权"地跑起来
// ——后者正是"沙盒不得冒名"禁止的形状。接上真正的 argv 包装器（provider）后
// 只需给 `a.confiner` 赋值，本文件的语义一行都不用改。
//
// # 与 user 终端的边界
//
// 官方 `terminal.create` 只建**用户**终端（"Allocate a user shell … without Agent
// sandbox or approval restrictions"，`lib/index.js:1179`），agent 终端只能走
// `openForAgent`。本文件也不让 `TerminalCreate` 建 agent 终端：两条路径互不冒充。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"dshgo/internal/transport"
	"dshgo/modules/exec/bash"
)

// officialAgentTerminalMaxAgentTerminals 是官方 `terminal.maxAgentTerminals`
// 的默认值（`lib/index.js:1113`：`z.number().step(1).min(1).default(4)`）。
//
// 与 `maxTerminals`（默认 8，`:1112`）是两个独立闸门：会话总容量管"这台机器上
// 一个会话最多几条 conhost"，agent 容量管"模型自己能开几条"。
const officialAgentTerminalMaxAgentTerminals = 4

// 官方 `dimensions` 的下限与标题上限（同一处官方常量的宿主副本用**引用**而不是
// 第二份字面量：cols 上限/rows 上限复用 serve_terminal_api.go 的
// officialTerminalMaxCols / officialTerminalMaxRows，它们本来就出自
// `lib/index.js:1114-1115` 的同一条 config）。
const (
	// officialAgentMinCols 对应官方 `dimensions` 的 cols 下限（`lib/index.js:1541`）。
	officialAgentMinCols = 2
	// officialAgentMinRows 对应官方 `dimensions` 的 rows 下限（同上）。
	officialAgentMinRows = 1
	// officialAgentTitleMaxRunes 是官方标题上限（`lib/index.js:957` 的 `> 120`）。
	officialAgentTitleMaxRunes = 120
)

// officialAgentTerminalIDPrefix 让宿主生成的终端 id 一眼可辨来源。
//
// 官方 id 是裸 `randomUUID()`（`lib/index.js:1223`）；本仓的 id 会进
// `terminal.close`/`retain` 的参数与落盘布局，加一个短前缀不改变契约形状
// （id 是调用方/宿主生成的不透明字符串），但让日志里"这是宿主为 agent 开的"
// 立刻可判。前缀不含 `validJobActionPart` 禁止的字符。
const officialAgentTerminalIDPrefix = "agent-"

// officialSandboxConfiner 对应官方 `agent.ctx.get("sandbox").confine(argv, policy, signal)`
// （`lib/index.js:1526-1529`）。
//
// 返回的 argv 是**包装后**的完整命令行（例如 `sandbox-exec … -- <shell> …`）。
// 实现方负责 policy 的语义；本层只负责"没有 provider 就不许假装有"。
type officialSandboxConfiner interface {
	ConfineArgv(ctx context.Context, argv []string, policy transport.OfficialSandboxExecutionPolicy) ([]string, error)
}

// 编译期证明：PTY 后端就是官方 agent 终端分配的服务面（与 user 终端同一份 roster）。
var _ transport.OfficialAgentTerminals = (*officialPTYTerminalAPI)(nil)

// TerminalOpenForAgent 是官方 `openForAgent` + `agentLaunch` 的宿主实现。
//
// 顺序严格照官方：title → 尺寸 → 会话容量 → agent 容量 → id → cwd → shell → confine
// → env → 起 PTY。每一步失败都**不**留下半条终端（没有 entry、没有 conhost）。
func (a *officialPTYTerminalAPI) TerminalOpenForAgent(ctx context.Context, sessionID string, req transport.OfficialAgentTerminalRequest) (transport.OfficialWebTerminalInfo, error) {
	if a == nil || a.bash == nil {
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf(
			"%w: agent 终端分配没有 PTY 后端（bash.Service 为空）", transport.ErrJTOfficialUnsupported)
	}
	if !validJobActionPart(sessionID) {
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}

	// 1. 标题（官方 terminalTitle，lib/index.js:956-959）：去首尾空白，空/超长即抛。
	title := strings.TrimSpace(req.Title)
	if title == "" || utf8.RuneCountInString(req.Title) > officialAgentTitleMaxRunes {
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf(
			"%w: title 必须是 1–%d 个字符（官方 terminalTitle，lib/index.js:957），收到 %q",
			transport.ErrJTOfficialBadParam, officialAgentTitleMaxRunes, req.Title)
	}

	// 2. 尺寸（官方 dimensions，lib/index.js:1541）：越界**抛**，不夹。
	if req.Cols < officialAgentMinCols || req.Cols > officialTerminalMaxCols ||
		req.Rows < officialAgentMinRows || req.Rows > officialTerminalMaxRows {
		return transport.OfficialWebTerminalInfo{}, fmt.Errorf(
			"%w: 尺寸越界：cols∈[%d,%d]、rows∈[%d,%d]（收到 %d×%d）——官方这里是报错不是截断（lib/index.js:1541）",
			transport.ErrJTOfficialBadParam,
			officialAgentMinCols, officialTerminalMaxCols,
			officialAgentMinRows, officialTerminalMaxRows, req.Cols, req.Rows)
	}

	// 3+4. 容量（官方 requireCapacity + agent 容量，lib/index.js:1219-1222 / :1391-1397）。
	if err := a.requireAgentTerminalCapacity(sessionID); err != nil {
		return transport.OfficialWebTerminalInfo{}, err
	}

	// 5. id（官方 `lib/index.js:1223` 的 randomUUID()：在 launch **之前**就已确定，
	//    所以 HISTFILE 的名字里能带它）。
	id, err := officialAgentTerminalID()
	if err != nil {
		return transport.OfficialWebTerminalInfo{}, err
	}

	// 6. cwd（官方 `request.cwd ?? workingDirectory.ensure(...)`）。
	cwd, err := a.agentTerminalCwd(sessionID, req.Cwd)
	if err != nil {
		return transport.OfficialWebTerminalInfo{}, err
	}

	// 7. shell（官方 agentShell：preferredShells 按名字优先，否则环境默认）。
	shell, err := agentTerminalShell(req.PreferredShells)
	if err != nil {
		return transport.OfficialWebTerminalInfo{}, err
	}

	// 8. confine（官方 :1522-1530）：全权原样；confined 必须有 provider，否则抛。
	argv, err := a.confineAgentArgv(ctx, append([]string{shell.Path}, shell.Args...), req.Policy)
	if err != nil {
		return transport.OfficialWebTerminalInfo{}, err
	}

	// 9. env（官方 :1456-1461）：request.env → DSH_SESSION_ID → HISTFILE 依次覆盖。
	history, release := officialAgentTerminalHistory(req.Policy.Mode, id)
	env := officialAgentTerminalEnv(os.Environ(), req.Env, sessionID, history)

	// 10. 起真 PTY：**用 confine 的产物**当命令行（包装器的 argv 就是
	// [wrapper, …, shell, …]），但 info.shell 仍报 **shell 本身**——官方的 shell
	// 字段描述的是"用哪个 shell"（WebTerminalInfo.shell），不是包装后的命令行。
	term, err := a.bash.OpenPTY(bash.PTYOptions{
		Cwd:            cwd,
		Path:           argv[0],
		Args:           argv[1:],
		Cols:           req.Cols,
		Rows:           req.Rows,
		ScrollbackRows: a.scrollbackRows,
		Env:            env,
	})
	if err != nil {
		return transport.OfficialWebTerminalInfo{}, mapPTYError(err)
	}

	now := a.now()
	entry := &officialPTYEntry{
		id: id, sessionID: sessionID,
		title: title,
		shell: transport.OfficialTerminalShell{Path: shell.Path, Args: shell.Args, Name: shell.Name},
		cwd:   cwd, cols: req.Cols, rows: req.Rows,
		term: term, state: "running",
		// rc.2 三字段：官方 agentLaunch 的 info（lib/index.js:1462-1467）。
		origin:      transport.OfficialTerminalOriginAgent,
		control:     transport.OfficialTerminalControlAgent,
		sandboxMode: req.Policy.Mode,
		// 注册表层两个字段 + 启动策略根：terminal_list 的快照与 send 的
		// POLICY_CHANGED 判据要用（见 officialPTYEntry 的注释）。
		agentName:  req.Name,
		agentType:  req.Type,
		policyRoot: req.Policy.WorkspaceRoot,
		release:    release,
		createdAt:  now, lastUsed: now,
	}
	a.mu.Lock()
	a.entries[entry.key()] = entry
	a.mu.Unlock()
	a.persistSession(sessionID)
	return entry.info(), nil
}

// TerminalAgentClose 是官方 `AgentTerminalHandle.close()`（`lib/index.js:66-69`）：
// 只在 `control==="agent"` 时允许关；人已接管 → `user-control` 拒绝（终端留给人）。
func (a *officialPTYTerminalAPI) TerminalAgentClose(ctx context.Context, sessionID, id string) error {
	entry, err := a.lookup(sessionID, id, "terminal.agentClose")
	if err != nil {
		return err
	}
	a.mu.Lock()
	control := entry.control
	a.mu.Unlock()
	if control != transport.OfficialTerminalControlAgent {
		// 官方 `src/agent.ts:127-131`：`control !== 'agent'` → AgentTerminalError('user-control')。
		return &transport.OfficialAgentTerminalError{
			Code:    transport.OfficialAgentTerminalUserControl,
			Message: fmt.Sprintf("人已接管终端 %s（control=%s），agent 不能关它；要关请由人从界面关", id, control),
		}
	}
	return a.TerminalClose(ctx, sessionID, id)
}

// requireAgentTerminalCapacity 是官方两道容量闸门（`lib/index.js:1219-1222` 与 `:1391-1397`）。
//
// 先查会话总容量（官方的 requireCapacity 在 openForAgent 里也先于 agent 容量调用，
// `:1219` 在 `:1220` 之前），再查 agent 容量——顺序不同会让"两条都满"时报错内容
// 与官方不一致。
func (a *officialPTYTerminalAPI) requireAgentTerminalCapacity(sessionID string) error {
	a.mu.Lock()
	total, agentControlled := 0, 0
	for _, e := range a.entries {
		if e.sessionID != sessionID || e.closed {
			continue
		}
		total++
		if e.origin == transport.OfficialTerminalOriginAgent &&
			e.control == transport.OfficialTerminalControlAgent &&
			e.state == "running" {
			agentControlled++
		}
	}
	limit, agentLimit := a.maxTerminals, a.maxAgentTerminals
	a.mu.Unlock()
	if limit > 0 && total >= limit {
		return &transport.OfficialAgentTerminalError{
			Code:    transport.OfficialAgentTerminalLimitReached,
			Message: fmt.Sprintf("会话 %s 已有 %d 条存活终端（官方 terminal.maxTerminals）", sessionID, total),
			Limit:   limit,
		}
	}
	if agentLimit > 0 && agentControlled >= agentLimit {
		return &transport.OfficialAgentTerminalError{
			Code:    transport.OfficialAgentTerminalLimitReached,
			Message: fmt.Sprintf("会话 %s 已有 %d 条 agent 控制的终端（官方 terminal.maxAgentTerminals）", sessionID, agentControlled),
			Limit:   agentLimit,
		}
	}
	return nil
}

// agentTerminalCwd 实现官方 `request.cwd ?? workingDirectory.ensure(agent, signal)`。
//
// 显式 cwd 必须是**绝对路径**：官方字段描述是 "Absolute initial directory"
// （`src/agent.ts:26`）。相对路径在 CreateProcess 上会按宿主自己的当前目录解析，
// 那就是"我以为在会话目录里、其实在别处"——宁可 400。
func (a *officialPTYTerminalAPI) agentTerminalCwd(sessionID string, requested *string) (string, error) {
	if requested != nil && strings.TrimSpace(*requested) != "" {
		cwd := *requested
		if !filepath.IsAbs(cwd) {
			return "", fmt.Errorf("%w: cwd 必须是绝对路径（官方 AgentTerminalRequest.cwd），收到 %q",
				transport.ErrJTOfficialBadParam, cwd)
		}
		return filepath.Clean(cwd), nil
	}
	if a.cwdOf == nil {
		return "", fmt.Errorf("%w: 没有会话工作目录来源，且请求没给 cwd", transport.ErrJTOfficialUnsupported)
	}
	cwd, err := a.cwdOf(sessionID)
	if err != nil {
		return "", fmt.Errorf("%w: 解析会话 %s 的工作目录失败: %v", transport.ErrJTOfficialUnsupported, sessionID, err)
	}
	if strings.TrimSpace(cwd) == "" {
		return "", fmt.Errorf("%w: 会话 %s 的工作目录是空串（官方 cwd 必填）",
			transport.ErrJTOfficialUnsupported, sessionID)
	}
	return cwd, nil
}

// confineAgentArgv 是官方 `confine`（`lib/index.js:1522-1530`）的逐条实现。
//
//	danger-full-access → argv 原样（官方 :1524）
//	其余两档           → 必须有一个 argv 级 provider；没有就抛（官方 :1527 的 throw）
func (a *officialPTYTerminalAPI) confineAgentArgv(ctx context.Context, argv []string, policy transport.OfficialSandboxExecutionPolicy) ([]string, error) {
	if policy.Mode == transport.OfficialSandboxModeDangerFullAccess {
		// 官方 `lib/index.js:1524`：`if (policy.mode === "danger-full-access") return argv`。
		return argv, nil
	}
	if a.confiner == nil {
		// 官方原文（`lib/index.js:1527`）：
		//   Agent terminals in sandbox mode "<mode>" require a sandbox provider in the execution environment
		return nil, fmt.Errorf(
			"%w: Agent terminals in sandbox mode %q require a sandbox provider in the execution environment"+
				"（官方 lib/index.js:1527；本宿主的 internal/sandbox 目前只有策略闸门、没有 argv 级 OS 隔离，"+
				"装不上包装器就不能假装 confined 跑起来了）",
			transport.ErrJTOfficialUnsupported, policy.Mode)
	}
	confined, err := a.confiner.ConfineArgv(ctx, argv, policy)
	if err != nil {
		return nil, err
	}
	if len(confined) == 0 {
		return nil, fmt.Errorf("沙箱 provider 返回了空 argv（policy.mode=%s）：confine 的结果必须是一条可执行的命令行", policy.Mode)
	}
	return confined, nil
}

// agentTerminalShell 实现官方 `agentShell`（`lib/index.js:1512-1521`）：
// preferredShells 里**按顺序**取第一个能被探测到的 shell，都没有才用环境默认。
func agentTerminalShell(preferred []string) (officialShell, error) {
	if len(preferred) > 0 {
		shells, err := discoverOfficialShells()
		if err != nil {
			// 探测失败不等于"没有 prefer 的 shell"：官方这里会让 shells() 的错误上抛
			// （`lib/index.js:1514` 的 await 没有 try）。所以原样上抛，别静默用默认 shell。
			return officialShell{}, err
		}
		for _, name := range preferred {
			kind := officialShellKind(name)
			if kind == "" {
				continue
			}
			for _, candidate := range shells {
				if officialShellKind(candidate.Path) != kind {
					continue
				}
				return officialShell{OfficialTerminalShell: candidate}, nil
			}
		}
	}
	return officialDefaultShell()
}

// officialShellKind 是官方 `shellKind`（`dsh-api-terminal-controller/src/shells.ts:44-46`）：
// 取可执行名、小写、去掉 `.exe`。preferredShells 的匹配就按它比，不按完整路径。
func officialShellKind(path string) string {
	name := path
	if i := strings.LastIndexAny(name, `/\\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	return strings.TrimSuffix(name, ".exe")
}

// officialAgentTerminalPlatform 是官方 `subprocess.terminalEnvironment().platform`
// （`lib/index.js:1454`）的宿主对应物。
//
// 本宿主只有 Windows 上有 ConPTY（`internal/terminal/pty_other.go` 在其它平台
// 如实回 ErrUnsupported），所以这里的取值域就是 `"windows"` / `"posix"`；官方用的
// 是同一组字面量（`:1457-1458`）。
func officialAgentTerminalPlatform() string {
	if runtime.GOOS == "windows" {
		return "windows"
	}
	return "posix"
}

// officialAgentTerminalHistory 是官方 `agentLaunch` 的 HISTFILE 规则（`lib/index.js:1454-1459`）：
//
//	platform === "posix" && mode !== "danger-full-access"
//	    → HISTFILE = (mode === "workspace-write" ? /tmp/dsh-agent-history-<id> : "")
//	只有 keepsHistory（= posix + workspace-write）才返回 release（`:1466`，删那个文件）
//
// Windows 上两支都不成立（`confined` 为假）→ env 里没有 HISTFILE、也没有 release。
// 这条规则在本宿主**没有生产路径**（Windows 才起得了 PTY），保留实现 + 契约测试是
// 为了让"官方语义只有一份"：将来若接 posix PTY，这里不用重新推导。
func officialAgentTerminalHistory(mode, id string) (map[string]string, func()) {
	return officialAgentTerminalHistoryForPlatform(officialAgentTerminalPlatform(), mode, id)
}

// officialAgentTerminalHistoryForPlatform 是上一条规则的实现，platform 显式传入。
//
// 为什么要能传 platform：本宿主只在 Windows 上起得了 PTY，posix 那一支是**契约对齐**
// （没有生产路径）。把它写成纯函数，契约表才测得出来；否则"官方那两条规则"在本仓
// 永远只有一行注释，没有断言。
func officialAgentTerminalHistoryForPlatform(platform, mode, id string) (map[string]string, func()) {
	if platform != "posix" || mode == transport.OfficialSandboxModeDangerFullAccess {
		return nil, nil
	}
	historyFile := "/tmp/dsh-agent-history-" + id
	if mode != transport.OfficialSandboxModeWorkspaceWrite {
		// read-only：confined 但不给历史文件（官方把 HISTFILE 置空串 = 不写不读）。
		return map[string]string{"HISTFILE": ""}, nil
	}
	return map[string]string{"HISTFILE": historyFile}, func() {
		// 官方 removeHistory（`:1487-1511`）：`rm -f -- <path>`，失败只告警。
		// 宿主没有远端执行环境，直接删本机文件；失败**不**影响终端生命周期。
		_ = os.Remove(historyFile)
	}
}

// officialAgentTerminalEnv 组装子 shell 的完整环境（官方 `{...request.env,
// DSH_SESSION_ID, ...history}`，`lib/index.js:1456-1461`）。
//
// 优先级严格按官方展开顺序：base（宿主环境）< request.env < DSH_SESSION_ID < history。
// 最后一项赢，所以 `HISTFILE` 不会被 request.env 覆盖——这是官方 "the controller's
// DSH_SESSION_ID, and HISTFILE … take precedence"（`src/agent.ts:35-36`）的原话。
func officialAgentTerminalEnv(base []string, requested map[string]string, sessionID string, history map[string]string) []string {
	out := make([]string, 0, len(base)+len(requested)+2)
	out = append(out, base...)
	keys := make([]string, 0, len(requested))
	for key := range requested {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+requested[key])
	}
	if sessionID != "" {
		out = append(out, "DSH_SESSION_ID="+sessionID)
	}
	histKeys := make([]string, 0, len(history))
	for key := range history {
		histKeys = append(histKeys, key)
	}
	sort.Strings(histKeys)
	for _, key := range histKeys {
		out = append(out, key+"="+history[key])
	}
	return out
}

// officialAgentTerminalID 是官方 `randomUUID()`（`lib/index.js:1223`）的等价物：
// UUID v4 文本，带一个可辨来源的前缀（见 officialAgentTerminalIDPrefix）。
func officialAgentTerminalID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成 agent 终端 id 失败: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // variant 10
	var sb strings.Builder
	sb.Grow(len(officialAgentTerminalIDPrefix) + 36)
	sb.WriteString(officialAgentTerminalIDPrefix)
	sb.WriteString(hex.EncodeToString(raw[0:4]))
	sb.WriteByte('-')
	sb.WriteString(hex.EncodeToString(raw[4:6]))
	sb.WriteByte('-')
	sb.WriteString(hex.EncodeToString(raw[6:8]))
	sb.WriteByte('-')
	sb.WriteString(hex.EncodeToString(raw[8:10]))
	sb.WriteByte('-')
	sb.WriteString(hex.EncodeToString(raw[10:16]))
	return sb.String(), nil
}
