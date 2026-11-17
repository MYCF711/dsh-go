package terminalpanel

// `terminal_open` —— agent 侧**唯一**能分配一条新终端的工具。
//
// # 官方那一支（逐条 file:line）
//
//	工具面      terminal_open(...)                  tool-terminal/src/index.ts:165-200
//	            name/description/parameters          同上 :166-172
//	            output schema {sessionId,name?,type,pid?,status,motd}
//	                                                 :174-182
//	            模型可见正文 renderSpawn              tool-terminal/src/render.ts:106-112
//	            （UI 侧同一条正则 `^started terminal session …`：
//	              dsh-client-ui-tool/src/client/tool/models/control-details-model.ts:232）
//	执行        ctx.workingDirectory.ensure(owner)   tool-terminal/src/index.ts:188
//	            → ctx.terminals.spawn(owner, …)      :189-193
//	注册表      spawn → 校验 name/类型 → id=pty-N   dsh-terminal/src/index.ts:135-178
//	面板后端    PanelTerminalBackend.spawn           terminal-panel/src/backend.ts:97-123
//	            title = spec.name ?? spec.sessionId  :104
//	            policy = services.policy(session)    :102
//	            → services.open(agent, request, …)   :103-111
//	            open = ctx.terminalController.openForAgent
//	                                                 terminal-panel/src/index.ts:34-37
//	子代理拒绝  SUBAGENT_REFUSAL                     terminal-panel/src/backend.ts:15、:100
//	启动等待    startup(terminal, options, signal)    terminal-panel/src/backend.ts:35-70
//	            （startupSilenceMs 300 / startupTimeoutMs 15000 / probeIntervalMs 1000，
//	              terminal-panel/src/config.ts:45-48）
//
// 本宿主的三处**如实差异**（不是新语义，是因为缺了官方的前提）：
//
//  1. **没有注册表层**。官方的 `id` 是注册表发的 `pty-N`（`dsh-terminal/src/index.ts:147`），
//     在 `backend.spawn` 之前就存在，所以未命名终端的标题用的是它（`backend.ts:104`）。
//     本宿主的终端 id 由父进程在分配时生成（`cmd/dshd/terminal_pty_agent.go` 的
//     `officialAgentTerminalID`），工具无法事先知道，而 `title` 又必须非空
//     （官方 `terminalTitle`：`lib/index.js:956-959`）。所以未给 `name` 时标题用
//     `type`（默认 `shell`）。`title` 是 1–120 字的**显示标签**，不是契约字段，
//     这里只换了一个取值来源。
//  2. **结果里的 sessionId 是宿主终端 id**（`agent-<uuid>`），不是注册表的 `pty-N`：
//     本宿主只有一份身份，就是 `terminal.list`/`terminal.watch`/`terminal.attach`
//     用的那个 id（也是 GUI 侧栏与徽标的数据源）。编一个 `pty-N` 会让模型拿到
//     一个任何方法都查不到的 id。
//  3. **启动等待只能轮询**。官方用 revision/change 通知 + 前台进程探针判断"shell 已就绪"
//     （`backend.ts:43-67`），本宿主的桥只有 `read`（`internal/terminal/bridge.go`），
//     没有通知也没有探针，所以这里按 `startupPollInterval` 轮询读屏，判据仍是官方的
//     "输出静默 `startupSilenceMs`"。拿不到输出时 `motd` 为空串——官方 `renderSpawn`
//     对空 motd 渲染 `(no startup output)`（`render.ts:109`），所以这是官方的合法取值，
//     不是"假装启动成功"。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
	"dshgo/internal/terminal"
)

// 官方工具名与描述（`tool-catalog.md:1397`、`tool-terminal/src/index.ts:166-167`，逐字）。
const (
	openTerminalName        = "terminal_open"
	openTerminalDescription = "Create a persistent, owner-isolated terminal session from a registered backend type. Use this for shell or REPL state that must survive across tool calls."
)

// 官方的三条拒绝/校验原文（逐字，会进 Session 日志与模型请求）。
const (
	// SUBAGENT_OPEN_TERMINAL 是官方"子代理不许开终端"（`terminal-panel/src/backend.ts:15`）。
	SUBAGENT_OPEN_TERMINAL = "Subagents cannot open terminals because the user cannot see them; use bash for shell commands instead."
	// ERR_OPEN_INITIATING_AGENT 是"没有发起代理"（`tool-terminal/src/index.ts:121`）。
	//
	// 注意它与 `user_terminal_*` 的那句**不是**同一句：官方两个包各写各的
	// （`terminal-panel/src/tools.ts:34` 是 "user terminal tools require an initiating agent"）。
	ERR_OPEN_INITIATING_AGENT = "terminal tools require an initiating agent"
	// ERR_OPEN_TYPE_EMPTY 是工具层对空 type 的原文（`tool-terminal/src/index.ts:186`）。
	ERR_OPEN_TYPE_EMPTY = "type must be a non-empty string"
	// ERR_OPEN_NAME_EMPTY 是注册表对空 name 的原文（`dsh-terminal/src/index.ts:142`）。
	ERR_OPEN_NAME_EMPTY = "PTY session name must be non-empty"
)

// errNoBackend 是注册表"这个 type 没有注册过后端"的原文
// （`dsh-terminal/src/index.ts:141`：`no PTY backend registered for "<type>"`）。
//
// 本宿主只注册**一个**后端（`backendType`，官方默认 `shell`，`config.ts:40`），
// 所以别的 type 走到这里就是原文那条错误——不是"参数写错"，而是"没有这个后端"。
func errNoBackend(typ string) error {
	return fmt.Errorf("no PTY backend registered for %q", typ)
}

// 官方 terminal-panel 的启动等待默认值（`config.ts:45-48`）。
const (
	officialStartupSilenceMs = 300
	officialStartupTimeoutMs = 15_000
	// openStartupPollInterval 是本宿主的轮询步长（官方用 change 通知，没有这个参数）。
	// 取 25ms：比 shell 打印一行提示符的量级小得多，又不会把父进程的 stdio 桥打满。
	openStartupPollInterval = 25 * time.Millisecond
	// startupProbeLines 是一次启动读屏向数据源要的行数上限。
	// 官方的界是 `maxReadBytes`（字节），本宿主的读屏接口按行分页，所以先要一段
	// 足够覆盖启动横幅的行数，再按 `maxReadBytes` 截字节。
	startupProbeLines = 200
)

// openSchema 与官方 catalog 的 JSON 段逐字一致（`tool-catalog.md:1401-1422`）。
//
// 唯一的本仓增量是 `additionalProperties:false`（全仓工具统一严格模式，
// 免得模型臆造参数后以为生效了）；属性集、类型、required 与官方一字不差。
var openSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "type": {
      "type": "string",
      "description": "Registered terminal backend type, usually \"shell\"."
    },
    "name": {
      "type": "string",
      "description": "Optional owner-local display name such as \"main\" or \"gdb\"."
    },
    "cwd": {
      "type": "string",
      "description": "Initial working directory, relative to the Session current directory when not absolute."
    }
  },
  "required": ["type"],
  "additionalProperties": false
}`)

// openArgs 是官方参数（`tool-terminal/src/index.ts:51-55`）。
type openArgs struct {
	Type string  `json:"type"`
	Name *string `json:"name"`
	Cwd  *string `json:"cwd"`
}

// SessionStatus 的两种官方形状（`tool-terminal/src/index.ts:76-95`）。
//
// 拆成两个类型是因为官方 schema 是 `oneOf`：running 只有 `kind`；exited 必须带
// `exitCode`/`signal`（可空）。用一个 struct + omitempty 会让 exited 丢掉这两个
// required 字段。
type sessionStatusRunning struct {
	Kind string `json:"kind"`
}

type sessionStatusExited struct {
	Kind     string  `json:"kind"`
	ExitCode *int    `json:"exit_code"`
	Signal   *string `json:"signal"`
}

// openResult 是官方 output schema 的字段集合（键名按本仓 Data 约定用 snake_case；
// 模型可见正文是 Text，见 renderOpenText）。
type openResult struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type"`
	PID       *int   `json:"pid,omitempty"`
	Status    any    `json:"status"`
	Motd      string `json:"motd"`
}

// openStartup 是启动等待的三个界（官方 `StartupOptions`，`backend.ts:26`）。
type openStartup struct {
	silence time.Duration
	timeout time.Duration
	poll    time.Duration
	maxRead int
}

// startup 解析启动等待参数：panel 上的字段（测试钩子）优先，否则用官方默认值。
func (p *panel) startup() openStartup {
	o := openStartup{
		silence: officialStartupSilenceMs * time.Millisecond,
		timeout: officialStartupTimeoutMs * time.Millisecond,
		poll:    openStartupPollInterval,
		maxRead: p.cfg.MaxReadBytes,
	}
	if p.startupSilence > 0 {
		o.silence = p.startupSilence
	}
	if p.startupTimeout > 0 {
		o.timeout = p.startupTimeout
	}
	if p.startupPoll > 0 {
		o.poll = p.startupPoll
	}
	if o.maxRead <= 0 {
		o.maxRead = terminal.DefaultPanelReadBytes
	}
	return o
}

// terminalOpen 是 `terminal_open`。
type terminalOpen struct{ p *panel }

func (t *terminalOpen) Name() string            { return openTerminalName }
func (t *terminalOpen) Description() string     { return openTerminalDescription }
func (t *terminalOpen) Schema() json.RawMessage { return openSchema }

// Caps 只声明 Executes：它**起一个真的 shell 进程**（官方 presentCall kind 是
// `execute`，`tool-terminal/src/index.ts:196-199`）。它自己不写文件、不联网；
// 终端里敲出来的命令由那个 shell 的沙箱档位负责，不由本工具声明。
func (t *terminalOpen) Caps() spi.Caps { return spi.Caps{Executes: true} }

// Exec 分配一条 agent 终端。
func (t *terminalOpen) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var args openArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"type": "shell", "name"?: "main", "cwd"?: "."}`},
			fmt.Errorf("参数不合法: %w", err)
	}
	// 官方顺序：工具层先判空 type（`tool-terminal/src/index.ts:186`），
	// 再交给注册表（`dsh-terminal/src/index.ts:140-141`）。
	if args.Type == "" {
		return spi.Result{Hint: `type 不能为空：本宿主注册的后端类型是 ` + t.p.cfg.BackendType + `，写成 {"type":"` + t.p.cfg.BackendType + `"}`},
			errors.New(ERR_OPEN_TYPE_EMPTY)
	}
	if args.Type != t.p.cfg.BackendType {
		return spi.Result{Hint: fmt.Sprintf(
				"本宿主只注册了 %q 这一个终端后端（官方 dsh-terminal-panel 的 backendType）；换 type 救不了，要用 shell 就写 %q",
				t.p.cfg.BackendType, t.p.cfg.BackendType)},
			errNoBackend(args.Type)
	}
	// 注册表的 name 校验（`dsh-terminal/src/index.ts:142`）：给了就必须非空。
	if args.Name != nil && *args.Name == "" {
		return spi.Result{Hint: `name 要么不写，要么非空（例如 "main" / "gdb"）`},
			errors.New(ERR_OPEN_NAME_EMPTY)
	}

	src, sessionID, _, fail, err := t.p.resolve(ctx, agentTerminalFamily)
	if err != nil {
		if fail.Hint == "" {
			fail.Hint = "先确认会话仍在、宿主终端控制器还活着，再重试。"
		}
		return fail, err
	}
	// 官方把 openForAgent 注册成 `ctx.terminals` 的后端能力（terminal-panel/src/index.ts:35）。
	// 本宿主对应的是 `terminal.AgentTerminalOpener`：数据源没有它就说明这台宿主
	// **没有** agent 终端的分配路径——必须响亮失败，绝不"建一条 user 终端冒充"。
	opener, ok := src.(terminal.AgentTerminalOpener)
	if !ok {
		return spi.Result{
				Summary: "宿主没有 agent 终端分配路径",
				Hint: "这是接线问题：宿主需要把 openForAgent/agentLaunch 的服务面接到 terminal.AgentTerminalOpener" +
					"（生产中由 transport.UserTerminalSource 提供），改参数救不了。",
			}.WithErrorInfo(spi.ErrorNameToolContextUnavailable, spi.CodeToolContextUnavailable),
			errors.New("宿主没有 agent 终端分配路径（terminal.AgentTerminalOpener）")
	}

	req, err := t.p.openRequest(sessionID, args)
	if err != nil {
		return spi.Result{Hint: "先确认会话有工作目录，再重试；cwd 写相对路径时按会话当前目录解析。"}, err
	}

	callCtx, cancel := t.p.callContext(ctx)
	defer cancel()
	info, oerr := opener.OpenAgentTerminal(callCtx, sessionID, req)
	if oerr != nil {
		// 原样把官方的拒绝交出去（confined 档的 `require a sandbox provider …`、
		// 容量到顶的 `terminal/limit-reached`、未接线的 openForAgent 说明都一样）。
		// **不**在这里换成另一个档位重试：那正是"静默降级"。
		return spi.Result{Hint: "分配 agent 终端失败：先确认会话在 serve 里（工具子进程没有父进程桥时开不了终端），" +
				"再确认沙箱档位——除 danger-full-access 外的档位需要 argv 级 sandbox provider，本宿主没有，会如实拒绝。"},
			fmt.Errorf("分配 agent 终端失败: %w", oerr)
	}

	// 名字与标题：官方 title = name ?? 注册表 id；本宿主没有注册表层，
	// 未命名时用 type（见文件头的差异说明 1）。
	name := ""
	if args.Name != nil {
		name = strings.TrimSpace(*args.Name)
	}

	motd := readStartup(ctx, src, sessionID, info.ID, t.p.startup())
	status := any(sessionStatusRunning{Kind: "running"})
	if info.State != "running" {
		status = sessionStatusExited{Kind: "exited", ExitCode: info.ExitCode, Signal: nil}
	}
	return spi.Result{
		Summary: openSummary(info, name),
		Data: openResult{
			SessionID: info.ID,
			Name:      name,
			Type:      args.Type,
			Status:    status,
			Motd:      motd,
		},
		Text: renderOpenText(info.ID, name, args.Type, motd, t.p.cfg.MaxReadBytes),
	}, nil
}

// openSummary 是给人看的一行（TUI/桌面端）。
func openSummary(info terminal.PanelTerminal, name string) string {
	label := info.ID
	if name != "" {
		label = fmt.Sprintf("%s（%s）", info.ID, name)
	}
	return "已开启 agent 终端 " + label
}

// openRequest 把工具参数 + 面板配置装配成官方 `AgentTerminalRequest`
// （`src/agent.ts:23-39`；装配点在 `terminal-panel/src/backend.ts:103-111`）。
func (p *panel) openRequest(sessionID string, args openArgs) (terminal.AgentOpenRequest, error) {
	cwd, err := p.openCwd(args.Cwd)
	if err != nil {
		return terminal.AgentOpenRequest{}, err
	}
	mode, root, err := openSandboxPolicy()
	if err != nil {
		return terminal.AgentOpenRequest{}, err
	}
	title := p.cfg.BackendType
	if args.Name != nil {
		title = strings.TrimSpace(*args.Name)
	}
	if title == "" {
		// 官方 title 必须 1–120 字（`lib/index.js:956-959`）；name 已被上面的
		// ERR_OPEN_NAME_EMPTY 拦过，这里只剩"name 全空白"的情形。
		return terminal.AgentOpenRequest{}, errors.New(ERR_OPEN_NAME_EMPTY)
	}
	return terminal.AgentOpenRequest{
		Title: title,
		Cwd:   cwd,
		Cols:  p.cfg.Cols,
		Rows:  p.cfg.Rows,
		// 注册表层的两个字段（官方 spawn 请求的 type/name）：`terminal_list` 的快照要回
		// 它们。name 用**调用方给的原值**，未给就是空——标题与名字是两件事，不能拿
		// title（未命名时是 type）冒充 name。
		Type: p.cfg.BackendType,
		Name: func() string {
			if args.Name == nil {
				return ""
			}
			return strings.TrimSpace(*args.Name)
		}(),
		// 官方把会话解析出的 SandboxExecutionPolicy 原样带给 openForAgent
		// （`terminal-panel/src/backend.ts:102`）；档位只认三档字面量。
		PolicyMode:          mode,
		PolicyWorkspaceRoot: root,
		PreferredShells:     p.cfg.PreferredShells,
		Env:                 p.cfg.Environment,
	}, nil
}

// sessionCwd 返回"会话当前目录"。
//
// 官方的来源是 `ctx.workingDirectory.ensure(agent, signal)`
// （`tool-terminal/src/index.ts:188`）。本宿主的工具跑在 `dshd run` 子进程里，
// 父进程在 spawn 时把策略根写进 `DSHD_SANDBOX_WORKSPACE`（`serve_tools.go` 的
// `sandboxChildRoots`），那就是这一轮的工作区；没有它时退回本进程的工作目录
// （`dshd run` 就是从工作区起的）。
func sessionCwd() (string, error) {
	if raw := strings.TrimSpace(os.Getenv(sandbox.EnvWorkspaceRoot)); raw != "" {
		return raw, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("取不到会话工作目录: %w", err)
	}
	return wd, nil
}

// openCwd 解析初始目录：绝对路径原样，相对路径按会话当前目录解析。
//
// 官方 `cwd === undefined ? cwd : resolve(cwd, args.cwd)`（`index.ts:192`）；
// 未给 cwd 时用会话当前目录，不给空串（父进程那边 cwd 必填，空串会被判非法）。
func (p *panel) openCwd(requested *string) (string, error) {
	base, err := sessionCwd()
	if err != nil {
		return "", err
	}
	if requested == nil || strings.TrimSpace(*requested) == "" {
		return filepath.Clean(base), nil
	}
	raw := *requested
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	return filepath.Join(base, raw), nil
}

// openSandboxPolicy 解析这次分配要带的 `SandboxExecutionPolicy`
// （官方 `services.policy(session)` = `ctx.sandboxPolicy.resolve({session})`，
// `terminal-panel/src/index.ts:36`）。
//
// 本宿主的落点是**同一份**策略环境：父进程在起工具子进程时把会话档位写进
// `DSHD_SANDBOX_MODE`（`serve_tools.go` 调 `sandboxWriteChildEnv`），bash/pwsh 的
// 沙箱闸门读的也是它（`modules/exec/bash/tools.go:116` 的 `sandbox.GateFromEnv`）。
// 所以这里不另建一套"终端档位"，否则会话说 A、终端说 B。
//
// 环境里没有这个变量时回落 workspace-write——与 `sandbox.BoundaryFromEnv`
// （`internal/sandbox/runtime.go:187-192`）同一口径：那是收窄，不是放宽。
// 但**未知字面量一律失败**（不猜）：`ParseMode` 就是这么判的。
func openSandboxPolicy() (string, string, error) {
	mode := sandbox.ModeWorkspaceWrite
	if raw := strings.TrimSpace(os.Getenv(sandbox.EnvMode)); raw != "" {
		parsed, err := sandbox.ParseMode(raw)
		if err != nil {
			return "", "", fmt.Errorf("%s: %w", sandbox.EnvMode, err)
		}
		mode = parsed
	}
	root, err := sessionCwd()
	if err != nil {
		return "", "", err
	}
	return string(mode), root, nil
}

// readStartup 等 shell 打出启动输出并静默 `silence` 后返回那段屏幕。
//
// 判据照官方 `startup`（`terminal-panel/src/backend.ts:35-70`）：输出静默
// `startupSilenceMs` 就算就绪，超过 `startupTimeoutMs` 就带着"已经拿到的东西"返回。
// 与官方的差别只有"怎么知道有新输出"：官方有 revision/change 通知，本宿主的桥只能
// 轮询读屏（见文件头差异 3）。
//
// 拿不到输出就返回空串：官方 `renderSpawn` 会把空 motd 渲染成 `(no startup output)`
// （`render.ts:109`），所以这是官方的合法取值，不是"假装成功"。
func readStartup(ctx context.Context, src terminal.UserTerminals, sessionID, id string, opt openStartup) string {
	if src == nil || id == "" {
		return ""
	}
	deadline := time.Now().Add(opt.timeout)
	var (
		last       string
		lastChange time.Time
	)
	for {
		page, err := src.ReadUserScreen(ctx, sessionID, id, startupProbeLines)
		if err == nil {
			if page.Text != last {
				last, lastChange = page.Text, time.Now()
			}
			// 官方的"打印过"判据是**渲染后的屏幕**非空（`backend.ts:38-41`：
			// `printed = read({offset:0,count:MAX,maxBytes}).text !== ''`）：官方 read 走
			// xterm 缓冲区重放，模式序列/清屏会被渲染成空文本。本宿主是原始字节尾部，
			// ConPTY 一启动就会吐 `[?9001h…`；把它当成"已经打印"会让 startup 提前
			// 返回，模型于是拿到一条 shell 还没起来的终端——往里打字会被丢掉
			// （2026-11-18 整链用例实测：send 的输入完全消失）。所以这里按**可见文本**判。
			printed := strings.TrimSpace(stripTerminalEscapes(page.Text)) != ""
			if printed && !lastChange.IsZero() && time.Since(lastChange) >= opt.silence {
				return boundStartup(last, opt.maxRead)
			}
		}
		now := time.Now()
		if !now.Before(deadline) {
			return boundStartup(last, opt.maxRead)
		}
		wait := opt.poll
		if remain := deadline.Sub(now); remain < wait {
			wait = remain
		}
		select {
		case <-ctx.Done():
			return boundStartup(last, opt.maxRead)
		case <-time.After(wait):
		}
	}
}

// stripTerminalEscapes 去掉 ANSI 转义序列（CSI / OSC / 两字节转义），只留可见字节。
//
// 用途只有一个：判断"屏幕上到底有没有可见内容"（官方那句 `printed` 的等价物，
// 见 readStartup）。**不**用于渲染：正文仍然是原始字节尾部（含 ANSI），
// 那是 `terminal_read` / `user_terminal_read` 已有的口径（本宿主没有屏幕模型）。
func stripTerminalEscapes(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != 0x1b {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[': // CSI：ESC [ 参数 终止字节（0x40-0x7e）
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
		case ']': // OSC：ESC ] … BEL(0x07) 或 ST(ESC \)
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j++
					break
				}
				j++
			}
			i = j
		default:
			// 其余转义：ESC [中间字节 0x20-0x2f]* 终止字节 0x30-0x7e。
			// 例如 `ESC ( B` 是选字符集的 3 字节序列，不能只吃掉 ESC + 一个字符。
			j := i + 1
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
				j++
			}
			i = j
		}
	}
	return b.String()
}

// boundStartup 按 `maxReadBytes` 截启动输出（官方 `read({… maxBytes})`，`backend.ts:69`）。
func boundStartup(text string, maxBytes int) string {
	tail, _ := terminal.NewestBytes(text, maxBytes)
	return tail
}

// renderOpenText 与官方 `renderSpawn` 逐字对齐（`tool-terminal/src/render.ts:106-112`）：
//
//	started terminal session <sessionId>[ (<name>)][type: <type>]
//	<motd 或 "(no startup output)">
//
// 超 `maxBytes` 时用官方的 `fitWithPrefix` 形状（`:68-73`）：保前缀 + 保尾部 +
// 补 `\n[output truncated]`。
func renderOpenText(id, name, typ, motd string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = terminal.DefaultPanelReadBytes
	}
	label := id
	if name != "" {
		label = id + " (" + name + ")"
	}
	prefix := "started terminal session " + label + " [type: " + typ + "]\n"
	body := motd
	if body == "" {
		body = "(no startup output)"
	}
	complete := prefix + body
	if len(complete) <= maxBytes {
		return complete
	}
	const marker = "\n[output truncated]"
	fixed := len(prefix) + len(marker)
	if fixed >= maxBytes {
		trimmed, _ := terminal.NewestBytes(prefix+marker, maxBytes)
		return trimmed
	}
	tail, _ := terminal.NewestBytes(body, maxBytes-fixed)
	return prefix + tail + marker
}
