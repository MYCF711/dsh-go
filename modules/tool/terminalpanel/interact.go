package terminalpanel

// `terminal_send` / `terminal_read` / `terminal_signal` / `terminal_close` /
// `terminal_list` —— agent 终端开出来之后的**交互**。
//
// `terminal_open` 在 `open.go`（同属官方 `@deepseek-ai/dsh-tool-terminal`，
// 工具表见 `tool-catalog.md:36`）。六个工具是一个包里的六件套：open 之后模型必须能
// 往里打、读回来、发信号、关掉、列出来，否则"开完终端只能读到这次启动输出"。
//
// # 官方依据（逐条 file:line）
//
//	terminal_send    tool-terminal/src/index.ts:202-304
//	                 描述 :204-205（含 enableRunInBackground 追加的那句）、参数 :206-213、
//	                 结果 oneOf :215-235、正文 renderSend（render.ts:120-131）
//	terminal_read    :306-337（描述 :308、参数 :309-313、结果 :316-326、正文 render.ts:150-158）
//	terminal_signal  :339-362（描述 :341、枚举 :344、结果 :347-355、正文 :356）
//	terminal_close   :364-393（描述 :366、结果 :371-379、正文 :380-385）
//	terminal_list    :395-408（描述 :397、结果 :400-402、正文 render.ts:166-177）
//	全空参数           官方没有参数的工具就是 `parameters: {}`（:398）
//	默认值            read count 500（terminal-panel/src/session.ts:21）、send submit true（:254）
//
// 描述与 schema 逐字来自 `tool-catalog.md:1363-1521`（与源码同源）；本仓唯一的增量是
// `additionalProperties:false`（全仓工具统一严格模式，免得模型臆造参数后以为生效了）。
//
// # 一条**官方的配置分支**：`run_in_background`
//
// 官方 `terminal_send` 的 `run_in_background` 参数与描述里那句话都受
// `enableRunInBackground` 配置管辖（`index.ts:205`、`:210-212`、`:255-258`）：
// 关掉时参数从 schema 里消失、描述里那句也消失，而**带了**这个参数仍会得到官方原文
// `background terminal sends are disabled by tool-terminal configuration`（`:256`）。
//
// 本宿主走的就是这个分支：后台 send 需要把 jobs 的生产者接在"在跑的 send 操作"上
// （官方 `sendSource` 拉的是 `startSend().readOutput()` 的增量，`background.ts:22-31`），
// 而本宿主的 send 在 serve 父进程里同步跑、桥只有一问一答，没有"活操作"可拉。
// 所以这里按官方 disabled 分支发布（少那句话、少那个参数），并在模型真的带参数时
// 回官方原文——**不**谎报"已在后台启动"、也**不**退化成前台假装成功。
//
// # Caps 的依据
//
//	send    Executes       ：它驱动一条**活着的 shell**执行命令（官方后台分支 presentCall
//	                          kind 就是 `execute`，`index.ts:293`）。它自己不写盘、不联网。
//	read    {}             ：只读有界滚动缓冲。
//	list    {}             ：只读 roster。
//	signal  Destructive    ：能终止前台进程（官方 presentCall kind `execute`，`:361`；
//	                          本仓 Caps 里"可能造成不可恢复的后果"覆盖它，与 `job_kill` 同判据）。
//	close   Destructive    ：拆掉终端并终止它的进程树（官方 presentCall kind `delete`，`:392`）。
//
// # 五个工具共用的一条判据：子代理
//
// 官方这五个工具是**owner（精确 Agent）维度**的：注册表按 owner 过滤 `list`、按 owner
// 校验 `expectOwned`（`dsh-terminal/src/index.ts:289-293`、`:368-373`）。所以子代理
// 眼里"自己一条终端都没有"：
//
//	list   → 空列表（官方 renderList 渲染 `(no terminal sessions)`），**不是**错误；
//	其余四个 → 官方注册表的两句话之一：
//	         `PTY session <id> belongs to another agent`（那条终端是主代理的）
//	         `unknown PTY session <id>`（根本没这条）
//
// 本宿主的子代理与主代理共用会话身份（见 `cmd/dshd/terminal_bridge.go` 的
// withSubagentCallContext 注释），所以这条边界只能在工具层按会话来源挡。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"dshgo/internal/spi"
	"dshgo/internal/terminal"
)

// 官方工具名与描述（逐字；`tool-catalog.md:1391-1521`、`tool-terminal/src/index.ts`）。
const (
	sendTerminalName   = "terminal_send"
	signalTerminalName = "terminal_signal"
	closeTerminalName  = "terminal_close"
	listTerminalName   = "terminal_list"
	readTerminalName   = "terminal_read"

	// 官方 :204-205（`enableRunInBackground:false` 分支：没有后台那句）。
	sendTerminalDescription = "Send text to a persistent terminal. By default Enter is submitted and the call waits for a prompt, stdin wait, output silence, timeout, or session exit."
	// 官方 :341。
	signalTerminalDescription = "Send an allowed signal to the current foreground process group of a persistent terminal."
	// 官方 :366。
	closeTerminalDescription = "Close one persistent terminal and wait until its captured owned process tree is gone."
	// 官方 :397。
	listTerminalDescription = "List persistent terminal sessions owned by the current agent."
	// 官方 :308。
	readTerminalDescription = "Read a bounded page of retained output from a persistent terminal without sending input."
)

// 官方拒绝/校验原文（会进 Session 日志与模型请求，逐字）。
const (
	// ERR_AGENT_SESSION_ID 是工具层对空 sessionId 的原文（`tool-terminal/src/index.ts:126-128`）。
	ERR_AGENT_SESSION_ID = "sessionId must be a non-empty string"
	// ERR_BACKGROUND_DISABLED 是官方 disabled 分支的原文（`:256`）。
	ERR_BACKGROUND_DISABLED = "background terminal sends are disabled by tool-terminal configuration"
	// agentForeignSessionFmt / agentUnknownSessionFmt 是注册表 expectOwned 的两句话
	// （`dsh-terminal/src/index.ts:370-372`）。
	agentForeignSessionFmt = "PTY session %s belongs to another agent"
	agentUnknownSessionFmt = "unknown PTY session %s"
)

// 两条"下一步"兜底话术（R3：每个 error 分支都要有 Hint）。
//
// 为什么用 firstNonEmpty 包一层而不是直接覆盖：`agentControls` /
// `subagentForeignTerminal` 给的是**更贴切**的那句（接线问题怎么说、子代理该怎么转交），
// 这里只在它们没给的时候补一句，绝不把更贴切的那句盖掉。
// 这种"赋值一行 + return"的形状也正是 R3 静态扫描认的那种（`cmd/dshd/r3_hint_test.go`）。
const (
	agentRetryHint   = "先确认会话仍在、宿主终端控制器还活着，再重试；反复失败就新开一条终端。"
	agentHandoffHint = "子代理看不到主会话的终端台账：把你需要的信息交给主代理，由它去操作。"
)

// firstNonEmpty 返回第一个非空串（R3 兜底话术用）。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// defaultAgentReadCount 是官方 read 的默认行数（`terminal-panel/src/session.ts:21`）。
const defaultAgentReadCount = 500

// 模型面 schema：与官方 catalog 的 JSON 段逐字一致（唯一增量 additionalProperties:false）。
var (
	sendSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "sessionId": {
      "type": "string",
      "description": "Terminal session id returned by terminal_open or terminal_list."
    },
    "text": {
      "type": "string",
      "description": "UTF-8 text to write to the terminal."
    },
    "submit": {
      "type": "boolean",
      "description": "Submit Enter after text (default true). Set false for control characters or incomplete REPL input."
    }
  },
  "required": ["sessionId", "text"],
  "additionalProperties": false
}`)

	readAgentSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "sessionId": {
      "type": "string",
      "description": "Terminal session id."
    },
    "offset": {
      "type": "number",
      "description": "Newest-relative line offset (default 0)."
    },
    "count": {
      "type": "number",
      "description": "Requested line count (default 500; backend caps apply)."
    }
  },
  "required": ["sessionId"],
  "additionalProperties": false
}`)

	signalSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "sessionId": {
      "type": "string",
      "description": "Terminal session id."
    },
    "signal": {
      "type": "string",
      "description": "Signal to deliver. Shell-targeted SIGKILL is rejected; use terminal_close.",
      "enum": ["SIGINT", "SIGTERM", "SIGKILL", "SIGTSTP", "SIGHUP"]
    }
  },
  "required": ["sessionId", "signal"],
  "additionalProperties": false
}`)

	closeSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "sessionId": {
      "type": "string",
      "description": "Terminal session id."
    }
  },
  "required": ["sessionId"],
  "additionalProperties": false
}`)

	listAgentSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
)

// sendArgs 是官方参数（`tool-terminal/src/index.ts:61-65`）。
type sendArgs struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
	Submit    *bool  `json:"submit"`
	// RunInBackground 不在本部署的 schema 里（官方 disabled 分支），但模型仍可能带；
	// 带了就回官方原文，不静默忽略。
	RunInBackground *bool `json:"run_in_background"`
}

// readAgentArgs 是官方参数（`:67-70`）。offset/count 是 JSON number（官方 schema 的
// 类型就是 number），非整数由这里按官方原文拒绝。
type readAgentArgs struct {
	SessionID string   `json:"sessionId"`
	Offset    *float64 `json:"offset"`
	Count     *float64 `json:"count"`
}

// signalArgs 是官方参数（`:72-74`）。
type signalArgs struct {
	SessionID string `json:"sessionId"`
	Signal    string `json:"signal"`
}

// closeArgs 是官方参数（`:57-59`）。
type closeArgs struct {
	SessionID string `json:"sessionId"`
}

// sendResultData 是 send 的结构化载荷（键名 snake_case，本仓 Data 约定；
// 字段集与官方 oneOf 的 foreground 分支一致，`index.ts:217-234`）。
type sendResultData struct {
	Kind          string `json:"kind"`
	Viewport      string `json:"viewport"`
	WaitReason    string `json:"wait_reason"`
	SessionStatus any    `json:"session_status"`
	Truncated     bool   `json:"truncated"`
}

// agentReadResult 是 read 的载荷（官方 `{text,totalLines,lineBegin,lineEnd,truncated}`，
// `index.ts:316-326`）。
type agentReadResult struct {
	Text       string `json:"text"`
	TotalLines int    `json:"total_lines"`
	LineBegin  int    `json:"line_begin"`
	LineEnd    int    `json:"line_end"`
	Truncated  bool   `json:"truncated"`
}

// signalResult 是 signal 的载荷（官方 `{delivered,targetPgid}`，`:347-355`）。
type signalResult struct {
	Delivered  bool `json:"delivered"`
	TargetPgid int  `json:"target_pgid"`
}

// closeResult 是 close 的载荷（官方 `{sessionId,outcome}`，`:371-379`）。
type closeResult struct {
	SessionID string `json:"session_id"`
	Outcome   string `json:"outcome"`
}

// agentSnapshotData 是 list 每行的载荷（官方 SESSION_SNAPSHOT，`:97-109`）。
type agentSnapshotData struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type"`
	PID       int    `json:"pid,omitempty"`
	Status    any    `json:"status"`
}

// sessionStatusOf 把宿主状态投影成官方 status 的两种 JSON 形状（`index.ts:76-95`）。
//
// 复用 open.go 的两个类型：running 只有 kind；exited 必带 exitCode 与 signal（可空）。
func sessionStatusOf(status terminal.AgentTerminalStatus) any {
	if status.Kind == "running" {
		return sessionStatusRunning{Kind: "running"}
	}
	return sessionStatusExited{Kind: "exited", ExitCode: status.ExitCode, Signal: status.Signal}
}

// agentControls 解析"能交互的 agent 终端"数据源：会话身份 + 数据源 + 交互能力。
//
// 返回 (controls, sessionID, origin, fail, err)：fail/err 非空表示应当直接交出去。
func (p *panel) agentControls(ctx context.Context) (terminal.AgentTerminalControls, string, string, spi.Result, error) {
	src, sessionID, origin, fail, err := p.identity(ctx, agentTerminalFamily)
	if err != nil {
		if fail.Hint == "" {
			fail.Hint = "先确认会话仍在、宿主终端控制器还活着，再重试。"
		}
		return nil, "", "", fail, err
	}
	controls, ok := src.(terminal.AgentTerminalControls)
	if !ok {
		return nil, "", "", spi.Result{
				Summary: "宿主没有 agent 终端交互路径",
				Hint: "这是接线问题：宿主需要把 ctx.terminals 的 startSend/read/signal/kill/list " +
					"接到 terminal.AgentTerminalControls（生产中由 transport.UserTerminalSource 提供），改参数救不了。",
			}.WithErrorInfo(spi.ErrorNameToolContextUnavailable, spi.CodeToolContextUnavailable),
			errors.New("宿主没有 agent 终端交互路径（terminal.AgentTerminalControls）")
	}
	return controls, sessionID, origin, spi.Result{}, nil
}

// agentInteractiveSessionID 取参数里的 sessionId 并做官方校验
// （`tool-terminal/src/index.ts:125-130`：空串抛原文）。
func agentInteractiveSessionID(raw string, example string) (string, spi.Result, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", spi.Result{Hint: "sessionId 不能为空：" + example},
			errors.New(ERR_AGENT_SESSION_ID)
	}
	return id, spi.Result{}, nil
}

// subagentForeignTerminal 是"子代理碰到了主代理的终端"的官方答复。
//
// 判据来自 list：这条 id 在会话里存在 → 官方 FOREIGN_SESSION 那句；不存在 →
// NO_SESSION 那句。与官方 expectOwned 的两次分支一一对应
// （`dsh-terminal/src/index.ts:368-373`）。
func (p *panel) subagentForeignTerminal(ctx context.Context, controls terminal.AgentTerminalControls, sessionID, id string) (spi.Result, error) {
	sessions, err := controls.AgentTerminalList(ctx, sessionID)
	if err == nil {
		for _, snap := range sessions {
			if snap.SessionID == id {
				return spi.Result{
					Hint: "这是主代理的 agent 终端：子代理只在被派活的那一段里，看不到也碰不到主会话的终端。" +
						"把你需要的信息交给主代理，由它去操作。",
				}, fmt.Errorf(agentForeignSessionFmt, id)
			}
		}
	}
	return spi.Result{Hint: "先调 terminal_list 取当前 id（终端可能已经被关掉，或 id 写错了）。"},
		fmt.Errorf(agentUnknownSessionFmt, id)
}

// sendCallContext 给一次 send 套上"比它自己的等待上限更宽"的上限。
//
// 官方 send 的上限是 timeoutMs（`terminal-panel/src/config.ts:50`=30s），而调用方
// 那一层只负责取消（`exec.signal`）。本宿主的桥是同步一问一答，外层如果比内层先到期，
// 就会把"send 自己判定的 timeout"换成"桥超时"——所以这里多留一截。
func (p *panel) sendCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := time.Duration(p.cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(ctx, timeout+10*time.Second)
}

// terminalSendTool 是 `terminal_send`。
type terminalSendTool struct{ p *panel }

func (t *terminalSendTool) Name() string            { return sendTerminalName }
func (t *terminalSendTool) Description() string     { return sendTerminalDescription }
func (t *terminalSendTool) Schema() json.RawMessage { return sendSchema }

// Caps 见文件头那张表：它驱动一条活着的 shell（官方后台分支 kind=execute）。
func (t *terminalSendTool) Caps() spi.Caps { return spi.Caps{Executes: true} }

func (t *terminalSendTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var args sendArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"sessionId": "终端 id", "text": "要写入的文本", "submit"?: true}`},
			fmt.Errorf("参数不合法: %w", err)
	}
	// 官方 disabled 分支：参数不在 schema 里，但带了就回原文（`:255-258`）。
	if args.RunInBackground != nil && *args.RunInBackground {
		return spi.Result{
				Hint: "本部署按官方 enableRunInBackground:false 发布：去掉 run_in_background 在前台等结果，" +
					"或用 job_* 那一套去跑普通命令；终端 send 目前没有后台形态。",
			},
			errors.New(ERR_BACKGROUND_DISABLED)
	}
	id, fail, err := agentInteractiveSessionID(args.SessionID, `先用 terminal_open 开一条，或 terminal_list 取现有 id`)
	if err != nil {
		return fail, err
	}
	controls, sessionID, origin, fail, err := t.p.agentControls(ctx)
	if err != nil {
		fail.Hint = firstNonEmpty(fail.Hint, agentRetryHint)
		return fail, err
	}
	if origin == terminal.PanelOriginSubagent {
		res, serr := t.p.subagentForeignTerminal(ctx, controls, sessionID, id)
		res.Hint = firstNonEmpty(res.Hint, agentHandoffHint)
		return res, serr
	}

	// 策略：拿**会话当前**的档位与工作区去和终端启动时的比（官方 samePolicy，:333）。
	mode, root, perr := openSandboxPolicy()
	if perr != nil {
		return spi.Result{Hint: "解析会话沙箱策略失败：先确认这一轮的环境里有 DSHD_SANDBOX_MODE / DSHD_SANDBOX_WORKSPACE。"}, perr
	}
	submit := true
	if args.Submit != nil {
		submit = *args.Submit
	}
	req := terminal.AgentSendRequest{
		Text:                args.Text,
		Submit:              submit,
		PromptSilence:       time.Duration(t.p.cfg.PromptSilenceMs) * time.Millisecond,
		IdleSilence:         time.Duration(t.p.cfg.IdleSilenceMs) * time.Millisecond,
		ProbeInterval:       time.Duration(t.p.cfg.ProbeIntervalMs) * time.Millisecond,
		Timeout:             time.Duration(t.p.cfg.TimeoutMs) * time.Millisecond,
		MaxReadBytes:        t.p.cfg.MaxReadBytes,
		PolicyMode:          mode,
		PolicyWorkspaceRoot: root,
	}
	callCtx, cancel := t.p.sendCallContext(ctx)
	defer cancel()
	res, serr := controls.AgentTerminalSend(callCtx, sessionID, id, req)
	if serr != nil {
		// 观方那几句拒绝（人已接管 / 策略变了 / 已退出 / 已有在跑的 send）逐字交出去——
		// 模型据此换下一步（terminal_read / 新开一条 / 等等）。
		return spi.Result{
				Hint: agentSendHint(serr),
			},
			fmt.Errorf("写终端 %s 失败: %w", id, serr)
	}
	return spi.Result{
		Summary: fmt.Sprintf("已写入终端 %s（wait=%s）", id, res.WaitReason),
		Data: sendResultData{
			Kind:          "foreground",
			Viewport:      res.Viewport,
			WaitReason:    res.WaitReason,
			SessionStatus: sessionStatusOf(res.Status),
			Truncated:     res.Truncated,
		},
		Text: renderSendText(res.Viewport, res.WaitReason, res.Status, res.Truncated, t.p.cfg.MaxReadBytes),
	}, nil
}

// agentSendHint 按官方那几句拒绝给出下一步（Hint 是本仓增量，官方只有一句英文）。
func agentSendHint(err error) string {
	switch {
	case strings.Contains(err.Error(), "taken over"):
		return "人已经接管这条终端：你只能读（terminal_read）或另开一条（terminal_open）。"
	case strings.Contains(err.Error(), "sandbox settings changed"):
		return "会话沙箱档位在这条终端开出来之后变了，它不再收输入：新开一条（terminal_open），旧的那条仍可 read/signal/close。"
	case strings.Contains(err.Error(), "already has an active send"):
		return "这条终端上已经有一个 send 在等：等它返回（同一轮里别并发发），或先 terminal_read 看现在的屏幕。"
	case strings.Contains(err.Error(), "has exited"):
		return "这条终端的 shell 已经退出：terminal_list 确认状态，必要时 terminal_open 另开一条。"
	case strings.Contains(err.Error(), "unknown PTY session") || strings.Contains(err.Error(), "belongs to another agent"):
		return "id 不对或终端已关：先 terminal_list 取当前 id。"
	}
	return "先 terminal_list 确认这条终端还在、控制权还归你，再重试；反复失败就 terminal_open 另开一条。"
}

// terminalReadTool 是 `terminal_read`。
type terminalReadTool struct{ p *panel }

func (t *terminalReadTool) Name() string            { return readTerminalName }
func (t *terminalReadTool) Description() string     { return readTerminalDescription }
func (t *terminalReadTool) Schema() json.RawMessage { return readAgentSchema }

// Caps 全空：只读有界滚动缓冲，无副作用。
func (t *terminalReadTool) Caps() spi.Caps { return spi.Caps{} }

// ConcurrencySafe 恒真：只读，不写任何共享状态。
func (t *terminalReadTool) ConcurrencySafe(json.RawMessage) bool { return true }

func (t *terminalReadTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var args readAgentArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"sessionId": "终端 id", "offset"?: 0, "count"?: 500}`},
			fmt.Errorf("参数不合法: %w", err)
	}
	id, fail, err := agentInteractiveSessionID(args.SessionID, `先用 terminal_open 开一条，或 terminal_list 取现有 id`)
	if err != nil {
		return fail, err
	}
	// 官方默认值（offset 0、count 500，`terminal-panel/src/session.ts:21`、`:342-343`）。
	offset, count := 0, defaultAgentReadCount
	if args.Offset != nil {
		v, verr := agentLineNumber(*args.Offset, true)
		if verr != nil {
			return spi.Result{Hint: "offset 是从最新一行往回数的行偏移（默认 0，只能是非负整数）"}, verr
		}
		offset = v
	}
	if args.Count != nil {
		v, verr := agentLineNumber(*args.Count, false)
		if verr != nil {
			return spi.Result{Hint: "count 是请求的行数（默认 500，只能是正整数；后端的行数/字节上限仍然生效）"}, verr
		}
		count = v
	}
	controls, sessionID, origin, fail, err := t.p.agentControls(ctx)
	if err != nil {
		fail.Hint = firstNonEmpty(fail.Hint, agentRetryHint)
		return fail, err
	}
	if origin == terminal.PanelOriginSubagent {
		res, serr := t.p.subagentForeignTerminal(ctx, controls, sessionID, id)
		res.Hint = firstNonEmpty(res.Hint, agentHandoffHint)
		return res, serr
	}
	callCtx, cancel := t.p.callContext(ctx)
	defer cancel()
	page, rerr := controls.AgentTerminalRead(callCtx, sessionID, id, terminal.AgentReadRequest{
		Offset: offset, Count: count, MaxReadBytes: t.p.cfg.MaxReadBytes,
	})
	if rerr != nil {
		return spi.Result{Hint: "先 terminal_list 确认这条终端还在；读不动的终端也可以 terminal_close 掉再新开。"},
			fmt.Errorf("读终端 %s 失败: %w", id, rerr)
	}
	return spi.Result{
		Summary: fmt.Sprintf("读终端 %s：offset %d，返回 %d 行 / 共 %d 行",
			id, page.LineBegin, page.LineEnd-page.LineBegin, page.TotalLines),
		Data: agentReadResult{
			Text:       page.Text,
			TotalLines: page.TotalLines,
			LineBegin:  page.LineBegin,
			LineEnd:    page.LineEnd,
			Truncated:  page.Truncated,
		},
		Text: renderReadText(page, t.p.cfg.MaxReadBytes),
	}, nil
}

// agentLineNumber 把 JSON number 落成行参数：offset 允许 0，count 必须 > 0；
// 两者都必须是整数（官方 `Number.isSafeInteger`，句见 `terminal-panel/src/session.ts:344-345`）。
func agentLineNumber(v float64, allowZero bool) (int, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || math.Abs(v) > math.MaxInt32 {
		if allowZero {
			return 0, errors.New("PTY read offset must be a non-negative safe integer")
		}
		return 0, errors.New("PTY read count must be a positive safe integer")
	}
	n := int(v)
	if allowZero && n < 0 {
		return 0, errors.New("PTY read offset must be a non-negative safe integer")
	}
	if !allowZero && n <= 0 {
		return 0, errors.New("PTY read count must be a positive safe integer")
	}
	return n, nil
}

// terminalSignalTool 是 `terminal_signal`。
type terminalSignalTool struct{ p *panel }

func (t *terminalSignalTool) Name() string            { return signalTerminalName }
func (t *terminalSignalTool) Description() string     { return signalTerminalDescription }
func (t *terminalSignalTool) Schema() json.RawMessage { return signalSchema }

// Caps 见文件头：能终止前台进程 → Destructive（与 job_kill 同判据）。
func (t *terminalSignalTool) Caps() spi.Caps { return spi.Caps{Destructive: true} }

func (t *terminalSignalTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var args signalArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"sessionId": "终端 id", "signal": "SIGINT"}`},
			fmt.Errorf("参数不合法: %w", err)
	}
	id, fail, err := agentInteractiveSessionID(args.SessionID, `先用 terminal_list 取当前 id`)
	if err != nil {
		return fail, err
	}
	// 官方枚举由 schema 拦（`index.ts:344`）；本仓的管线不保证执行 enum，所以再挡一层。
	if !terminal.AgentSignalAllowed(args.Signal) {
		return spi.Result{Hint: "signal 只能是 SIGINT / SIGTERM / SIGKILL / SIGTSTP / SIGHUP（官方枚举）"},
			fmt.Errorf("signal must be one of SIGINT, SIGTERM, SIGKILL, SIGTSTP, SIGHUP（收到 %q）", args.Signal)
	}
	controls, sessionID, origin, fail, err := t.p.agentControls(ctx)
	if err != nil {
		fail.Hint = firstNonEmpty(fail.Hint, agentRetryHint)
		return fail, err
	}
	if origin == terminal.PanelOriginSubagent {
		res, serr := t.p.subagentForeignTerminal(ctx, controls, sessionID, id)
		res.Hint = firstNonEmpty(res.Hint, agentHandoffHint)
		return res, serr
	}
	callCtx, cancel := t.p.callContext(ctx)
	defer cancel()
	pgid, serr := controls.AgentTerminalSignal(callCtx, sessionID, id, args.Signal)
	if serr != nil {
		return spi.Result{Hint: signalHint(serr)}, fmt.Errorf("给终端 %s 投递 %s 失败: %w", id, args.Signal, serr)
	}
	return spi.Result{
		Summary: fmt.Sprintf("已把 %s 投给终端 %s 的前台进程组 %d", args.Signal, id, pgid),
		Data:    signalResult{Delivered: true, TargetPgid: pgid},
		Text:    renderSignalText(args.Signal, pgid),
	}, nil
}

// signalHint 按官方那几句拒绝给下一步。
func signalHint(err error) string {
	switch {
	case strings.Contains(err.Error(), "SIGKILL the terminal shell"):
		return "官方不允许对 shell 本身 SIGKILL：要整条拆掉就用 terminal_close；只想打断当前命令用 SIGINT。"
	case strings.Contains(err.Error(), "unsupported on Windows"):
		return "Windows 上没有这条信号（官方原文如此）：只有 SIGINT / SIGTERM / SIGKILL 可用，而 SIGKILL 对 shell 会被拒——用 terminal_close。"
	case strings.Contains(err.Error(), "taken over"):
		return "人已经接管这条终端：只剩 terminal_read 可用；要停它请由人从界面操作。"
	case strings.Contains(err.Error(), "not running") || strings.Contains(err.Error(), "has exited"):
		return "这条终端的 shell 已经退出，没有前台进程可投：terminal_close 收掉它。"
	}
	return "先 terminal_list 确认这条终端还在、控制权还归你。"
}

// terminalCloseTool 是 `terminal_close`。
type terminalCloseTool struct{ p *panel }

func (t *terminalCloseTool) Name() string            { return closeTerminalName }
func (t *terminalCloseTool) Description() string     { return closeTerminalDescription }
func (t *terminalCloseTool) Schema() json.RawMessage { return closeSchema }

// Caps 见文件头：拆终端 + 终止进程树 → Destructive（官方 kind=delete）。
func (t *terminalCloseTool) Caps() spi.Caps { return spi.Caps{Destructive: true} }

func (t *terminalCloseTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var args closeArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"sessionId": "终端 id"}`}, fmt.Errorf("参数不合法: %w", err)
	}
	id, fail, err := agentInteractiveSessionID(args.SessionID, `先用 terminal_list 取当前 id`)
	if err != nil {
		return fail, err
	}
	controls, sessionID, origin, fail, err := t.p.agentControls(ctx)
	if err != nil {
		fail.Hint = firstNonEmpty(fail.Hint, agentRetryHint)
		return fail, err
	}
	if origin == terminal.PanelOriginSubagent {
		res, serr := t.p.subagentForeignTerminal(ctx, controls, sessionID, id)
		res.Hint = firstNonEmpty(res.Hint, agentHandoffHint)
		return res, serr
	}
	callCtx, cancel := t.p.callContext(ctx)
	defer cancel()
	closed, cerr := controls.AgentTerminalClose(callCtx, sessionID, id)
	if cerr != nil {
		return spi.Result{Hint: "先 terminal_list 确认这条终端还在：已关过/从未存在的 id 会得到 unknown PTY session。"},
			fmt.Errorf("关终端 %s 失败: %w", id, cerr)
	}
	outcome := "already-closing"
	if closed {
		outcome = "closed"
	}
	return spi.Result{
		Summary: fmt.Sprintf("终端 %s：%s", id, outcome),
		Data:    closeResult{SessionID: id, Outcome: outcome},
		Text:    renderCloseText(id, closed),
	}, nil
}

// terminalListTool 是 `terminal_list`。
type terminalListTool struct{ p *panel }

func (t *terminalListTool) Name() string            { return listTerminalName }
func (t *terminalListTool) Description() string     { return listTerminalDescription }
func (t *terminalListTool) Schema() json.RawMessage { return listAgentSchema }

// Caps 全空：只读 roster。
func (t *terminalListTool) Caps() spi.Caps { return spi.Caps{} }

// ConcurrencySafe 恒真：只读一次 roster。
func (t *terminalListTool) ConcurrencySafe(json.RawMessage) bool { return true }

func (t *terminalListTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	if extra := extraArgs(raw); len(extra) > 0 {
		return spi.Result{Hint: "terminal_list 不接受参数：直接发 {} 即可"},
			fmt.Errorf("多余参数: %s", strings.Join(extra, ","))
	}
	controls, sessionID, origin, fail, err := t.p.agentControls(ctx)
	if err != nil {
		fail.Hint = firstNonEmpty(fail.Hint, agentRetryHint)
		return fail, err
	}
	// 官方注册表按 owner 过滤：子代理名下没有终端 → 空列表（不是错误，`render.ts:167`）。
	if origin == terminal.PanelOriginSubagent {
		return spi.Result{
			Summary: "0 个 agent 终端",
			Data:    []agentSnapshotData{},
			Text:    renderListText(nil, t.p.cfg.MaxReadBytes),
		}, nil
	}
	callCtx, cancel := t.p.callContext(ctx)
	defer cancel()
	sessions, lerr := controls.AgentTerminalList(callCtx, sessionID)
	if lerr != nil {
		return spi.Result{Hint: "宿主列 agent 终端失败；先确认 serve 侧的终端控制器还活着，再重试。"},
			fmt.Errorf("列 agent 终端失败: %w", lerr)
	}
	out := make([]agentSnapshotData, 0, len(sessions))
	for _, snap := range sessions {
		out = append(out, agentSnapshotData{
			SessionID: snap.SessionID,
			Name:      snap.Name,
			Type:      snap.Type,
			PID:       snap.PID,
			Status:    sessionStatusOf(snap.Status),
		})
	}
	return spi.Result{
		Summary: fmt.Sprintf("%d 个 agent 终端", len(out)),
		Data:    out,
		Text:    renderListText(sessions, t.p.cfg.MaxReadBytes),
	}, nil
}
