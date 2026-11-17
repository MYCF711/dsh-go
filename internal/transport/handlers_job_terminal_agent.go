package transport

// 官方 **agent 终端分配** 的宿主侧契约与接线缝（rc.2 / 0.2.2）。
//
// # 官方那一支是什么（逐条 file:line）
//
//	TerminalController.openForAgent(agent, request, signal)
//	    `@deepseek-ai/dsh-api-terminal-controller/lib/index.js:1213-1240`
//	    （TS 源 `src/index.ts:222`）：title 规范化 → 尺寸校验 → 会话容量
//	    （`requireCapacity`，`:1391-1397`，maxTerminals 默认 8，`:1112`）→
//	    agent 终端容量（`origin==="agent" && control==="agent" && state==="running"`
//	    计数 >= maxAgentTerminals 即 `terminal/limit-reached`，默认 4，`:1219-1222`）
//	    → id = randomUUID() → `agentLaunch`
//	TerminalController.agentLaunch(agent, id, request, signal)
//	    `lib/index.js:1449-1478`（TS 源 `src/index.ts:448-468`）
//	TerminalController.agentShell / confine
//	    `lib/index.js:1512-1521` / `:1522-1530`（TS 源 `src/index.ts:489-506`）
//	terminalTitle / dimensions
//	    `lib/index.js:956-959` / `:1541`
//	AgentTerminalHandle（id/pid/info/revision/write/drain/read/mark/signal/close）
//	    `lib/index.js:22-70`；`close()` 在 `info.control !== "agent"` 时抛
//	    `AgentTerminalError('user-control')`（`src/agent.ts:127-131`）
//	AgentTerminalRequest / SandboxExecutionPolicy / AgentTerminalErrorCode
//	    `src/agent.ts:23-39` / `@deepseek-ai/dsh-sandbox/src/index.ts:48-69` / `src/agent.ts:9`
//
// # 为什么它**没有** wire 方法
//
// 官方 `dsh-api-terminal-controller/lib/typert.host.js:229-768` 只导出 10 个方法
// （attach/close/create/environment/list/rename/retain/shells/watch），**没有**
// `terminal.openForAgent`。`openForAgent` 是**进程内**能力：`dsh-terminal-panel`
// 把它注册成侧栏终端后端（`dsh-terminal-panel/src/index.ts:34-37` 的
// `ctx.terminals.registerBackend({open: (agent, request, signal) =>
// ctx.terminalController.openForAgent(agent, request, signal), …})`），agent 工具
// 经 `ctx.terminals` 消费。所以本仓的对应缝是**Go 级服务面**（下面这个接口），
// 不是新造一个官方没有的 method 名——新造 method 就是自造 wire。
//
// 客户端看到的是**产物**：`terminal.list` / `terminal.watch` / `terminal.attach`
// 推出去的 `WebTerminalInfo.origin="agent"`、`control="agent"`、
// `sandboxMode=<policy.mode>`（`lib/typert.host.js` 的 WebTerminalInfo 三字段）
// ——也就是 GUI #54 的 Agent/沙箱徽标的数据源。
//
// # 未接线就响亮失败
//
// `Svc` 没实现 `OfficialAgentTerminals` 时，`OpenAgentTerminal` 回
// `ErrJTOfficialUnsupported`（→ 501 not_implemented）并说清缺的是
// `openForAgent`/`agentLaunch` 这条分配路径。**绝不**退化成"建一条 user 终端
// 再假装它是 agent 的"：那会让界面画出假徽标，也是官方语义的直接违背
// （`terminal.create` 的官方描述是 "Allocate a user shell … without Agent
// sandbox or approval restrictions"，`lib/index.js:1179`）。

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// 官方 `AgentTerminalError.code` 的四个机器可路由取值。
//
// 前三条来自 `src/agent.ts:9` 的 `AgentTerminalErrorCode`；`terminal/limit-reached`
// 是控制器自己的 `RemoteError` 码（`lib/index.js:1222`、`:1396`）。
const (
	// OfficialAgentTerminalLimitReached 表示会话终端容量或 agent 终端容量到顶。
	OfficialAgentTerminalLimitReached = "terminal/limit-reached"
	// OfficialAgentTerminalUnavailable 表示终端已关闭/还在分配中（读不到）。
	OfficialAgentTerminalUnavailable = "terminal/unavailable"
	// OfficialAgentTerminalUserControl 表示人已接管，agent 不再能关它。
	OfficialAgentTerminalUserControl = "user-control"
	// OfficialAgentTerminalNotRunning 表示终端进程已退出。
	OfficialAgentTerminalNotRunning = "not-running"
)

// OfficialSandboxExecutionPolicy 对应官方 `SandboxExecutionPolicy`
// （`@deepseek-ai/dsh-sandbox/src/index.ts:48-69`）。
//
// `Mode` 的取值集合与官方 `SandboxMode` 逐字一致（`dsh-sandbox/src/index.ts:38`：
// `read-only | workspace-write | danger-full-access`），也与本仓 `internal/sandbox`
// 的三档同名同形——所以**不做转换表**，转换表正是"两套真值"的来源。
type OfficialSandboxExecutionPolicy struct {
	// Mode 是这次执行的文件效果档位（官方 `mode`）。
	Mode string `json:"mode"`
	// WorkspaceRoot 是 `workspace-write` 可写的绝对根（官方 `workspaceRoot`）。
	WorkspaceRoot string `json:"workspaceRoot"`
	// AdditionalWritableRoots 是额外可写目录（官方 `additionalWritableRoots?`）。
	AdditionalWritableRoots []string `json:"additionalWritableRoots,omitempty"`
	// SessionID 是发起会话的不透明身份（官方 `sessionId?`，供后端做每会话状态）。
	SessionID string `json:"sessionId,omitempty"`
}

// OfficialAgentTerminalRequest 对应官方 `AgentTerminalRequest`（`src/agent.ts:23-39`）。
//
// 字段名与可选性逐条照抄：`title` 必填（1–120 字符，去首尾空白），`cwd?` 缺省
// 用会话工作目录，`cols`/`rows` 必填，`policy` 必填（**confined 档会包 argv**），
// `preferredShells` 必填（按名字优先挑 shell），`env?` 是追加进子进程环境的变量
// （控制器的 `DSH_SESSION_ID` 与 confined POSIX 的 `HISTFILE` 优先级更高）。
type OfficialAgentTerminalRequest struct {
	Title           string                         `json:"title"`
	Cwd             *string                        `json:"cwd,omitempty"`
	Cols            int                            `json:"cols"`
	Rows            int                            `json:"rows"`
	Policy          OfficialSandboxExecutionPolicy `json:"policy"`
	PreferredShells []string                       `json:"preferredShells"`
	Env             map[string]string              `json:"env,omitempty"`
}

// OfficialAgentTerminalError 是一条被拒绝的 agent 终端操作（官方 `AgentTerminalError`
// / `RemoteError` 的同形）。
//
// 用**类型**而不是字符串给调用方分流：官方客户端就是按 `code` 分支的，所以本仓
// 也必须让 code 在 Go 侧可取，而不是只有一句中文。
type OfficialAgentTerminalError struct {
	// Code 是上面四个常量之一（官方 code，不翻译）。
	Code string
	// Message 是给日志与模型看的说明（官方 message 同样是英文/英文派生的中文）。
	Message string
	// Limit 只在 `terminal/limit-reached` 上出现（官方 `{limit: N}`，`lib/index.js:1222`）。
	Limit int
}

// Error 实现 error（消息里带官方 code，便于从日志一眼认出）。
func (e *OfficialAgentTerminalError) Error() string {
	if e == nil {
		return ""
	}
	if e.Limit > 0 {
		return fmt.Sprintf("%s: %s（上限 %d）", e.Code, e.Message, e.Limit)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// WireCode 是四字段错误体里该用的 code。
//
// 官方 code 自带命名空间（`terminal/limit-reached`），与网关词表
// （`gateway/bad-request` 这类）同族，所以原样透出，不折叠成 `bad_request`——
// 折叠会让客户端把"容量到顶"当成"参数写错"。
func (e *OfficialAgentTerminalError) WireCode() string {
	if e == nil || e.Code == "" {
		return "internal"
	}
	return e.Code
}

// HTTPStatus 是这条拒绝在 HTTP 面上的状态码。
//
// 409：容量到顶 / 控制权已交出——**状态没坏，只是现在不行**；
// 404：终端不在了 / 已退出——换 id 或重新开。
func (e *OfficialAgentTerminalError) HTTPStatus() int {
	if e == nil {
		return 500
	}
	switch e.Code {
	case OfficialAgentTerminalUserControl:
		return 409
	case OfficialAgentTerminalLimitReached:
		return 409
	case OfficialAgentTerminalUnavailable, OfficialAgentTerminalNotRunning:
		return 404
	}
	return 500
}

// OfficialAgentTerminals 是本宿主对官方 `terminalController.openForAgent` 的
// 服务面（**进程内**能力，不是 wire 方法；理由见文件头）。
//
// 实现方必须是**与 `terminal.list`/`terminal.watch` 同一份 roster**（同一批
// `OfficialWebTerminalInfo`）：另建一份"agent 终端表"就会出现侧栏有、attach 找不到。
type OfficialAgentTerminals interface {
	// TerminalOpenForAgent 对应官方 `openForAgent(agent, request, signal)`
	// （`lib/index.js:1213-1240`）：分配并启动一条 agent 终端，返回它的 info。
	// 失败必须是 `*OfficialAgentTerminalError` 或 `ErrJTOfficialUnsupported`。
	TerminalOpenForAgent(ctx context.Context, sessionID string, req OfficialAgentTerminalRequest) (OfficialWebTerminalInfo, error)
	// TerminalAgentClose 对应官方 `AgentTerminalHandle.close()`（`lib/index.js:66-69`）：
	// 只在 `control==="agent"` 时允许关；已被人接管 → `user-control`。
	TerminalAgentClose(ctx context.Context, sessionID, id string) error
}

// agentCapability 取 agent 终端分配能力（没有就是"未接线"，由调用点响亮失败）。
func (h *OfficialJobTerminal) agentCapability() (OfficialAgentTerminals, bool) {
	if h == nil || h.Svc == nil {
		return nil, false
	}
	capability, ok := h.Svc.(OfficialAgentTerminals)
	return capability, ok
}

// OpenAgentTerminal 是宿主内**唯一**的 agent 终端分配入口。
//
// 它不做 wire 暴露（官方也没有对应 method，见文件头），供宿主自己的 agent 工具
// 层调用。参数校验在这一层（`OfficialJobTerminal` 的职责就是"路由与参数校验"），
// 分配与 confine 在终端服务面。
//
// 未接线时的失败形状（**响亮，不静默降级**）：`ErrJTOfficialUnsupported`，
// 消息点名缺的是 `openForAgent`/`agentLaunch`，与 `session.create` 的
// `workspaceId` 分支同口径（`server.go:1245`）。
func (h *OfficialJobTerminal) OpenAgentTerminal(ctx context.Context, sessionID string, req OfficialAgentTerminalRequest) (OfficialWebTerminalInfo, error) {
	capability, ok := h.agentCapability()
	if !ok {
		return OfficialWebTerminalInfo{}, fmt.Errorf(
			"%w: %s 未接线——这个宿主的 terminal 服务面没有 openForAgent/agentLaunch 对应的 "+
				"agent 终端分配路径（官方 dsh-api-terminal-controller/lib/index.js:1213-1240）",
			ErrJTOfficialUnsupported, officialAgentOpenMethod)
	}
	return openOfficialAgentTerminal(ctx, capability, sessionID, req)
}

// officialAgentOpenMethod 是 agent 终端分配在日志/错误里的方法名。
//
// 官方没有同名 wire 方法（它不是 typert 方法，见文件头），所以这里用的是**进程内**
// 的名字，取官方 `openForAgent` 的原词。
const officialAgentOpenMethod = "terminal.openForAgent"

// openOfficialAgentTerminal 是"校验 + 分配 + 形状回查"的唯一实现。
//
// 两个入口共用它：`OfficialJobTerminal.OpenAgentTerminal`（进程内 Go 调用）与
// `UserTerminalSource.OpenAgentTerminal`（工具子进程经 stdio 桥调用，见
// `internal/terminal/bridge.go` 的 open op）。共用一份校验，才不会出现"桥上能过、
// 直调过不了"的两套判据。
func openOfficialAgentTerminal(ctx context.Context, capability OfficialAgentTerminals, sessionID string, req OfficialAgentTerminalRequest) (OfficialWebTerminalInfo, error) {
	const method = officialAgentOpenMethod
	if err := validateOfficialAgentTerminalRequest(method, sessionID, req); err != nil {
		return OfficialWebTerminalInfo{}, err
	}
	if capability == nil {
		return OfficialWebTerminalInfo{}, fmt.Errorf(
			"%w: %s 未接线——这个宿主的 terminal 服务面没有 openForAgent/agentLaunch 对应的 "+
				"agent 终端分配路径（官方 dsh-api-terminal-controller/lib/index.js:1213-1240）",
			ErrJTOfficialUnsupported, method)
	}
	info, err := capability.TerminalOpenForAgent(ctx, sessionID, req)
	if err != nil {
		return OfficialWebTerminalInfo{}, err
	}
	// 官方 origin/control/sandboxMode 是 agent 终端的**契约形状**：分配出来的
	// 东西不是 agent 终端就是实现 bug，必须在交给调用方之前响亮失败，而不是让
	// 界面把一条 user 终端画成 agent 的。
	if err := validateOfficialWebTerminalInfo(info); err != nil {
		return OfficialWebTerminalInfo{}, err
	}
	if info.Origin != OfficialTerminalOriginAgent || info.Control != OfficialTerminalControlAgent {
		return OfficialWebTerminalInfo{}, fmt.Errorf(
			"宿主给出的 agent 终端 origin/control 必须是 agent/agent（拿到 %q/%q）：openForAgent 的产物定义在官方 lib/index.js:1464-1465",
			info.Origin, info.Control)
	}
	if info.SandboxMode != req.Policy.Mode {
		return OfficialWebTerminalInfo{}, fmt.Errorf(
			"宿主给出的 agent 终端 sandboxMode 必须等于请求策略的 mode（请求 %q，拿到 %q）：官方 lib/index.js:1476",
			req.Policy.Mode, info.SandboxMode)
	}
	return info, nil
}

// CloseAgentTerminal 是 `AgentTerminalHandle.close()` 的宿主侧入口。
//
// 未接线 → 501 同形；拒绝（人已接管）→ `*OfficialAgentTerminalError{user-control}`。
func (h *OfficialJobTerminal) CloseAgentTerminal(ctx context.Context, sessionID, id string) error {
	const method = "terminal.agentClose"
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("%w: %s 缺 sessionId", ErrJTOfficialBadParam, method)
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: %s 缺 id", ErrJTOfficialBadParam, method)
	}
	capability, ok := h.agentCapability()
	if !ok {
		return fmt.Errorf("%w: %s 未接线——这个宿主的 terminal 服务面没有 openForAgent/agentLaunch 对应的 "+
			"agent 终端分配路径（官方 dsh-api-terminal-controller/lib/index.js:1213-1240）",
			ErrJTOfficialUnsupported, method)
	}
	return capability.TerminalAgentClose(ctx, sessionID, id)
}

// officialAgentTerminalMaxTitleRunes 是官方标题上限（`lib/index.js:957`：`title.length > 120`）。
const officialAgentTerminalMaxTitleRunes = 120

// officialAgentTerminalMaxCols / MaxRows 是官方 dimensions 的上限
// （`lib/index.js:1541`）；下限分别是 2 与 1（同处，不是 0）。
const (
	officialAgentTerminalMinCols = 2
	officialAgentTerminalMaxCols = 500
	officialAgentTerminalMinRows = 1
	officialAgentTerminalMaxRows = 200
)

// validateOfficialAgentTerminalRequest 是官方 openForAgent 的入参校验
// （`terminalTitle` `lib/index.js:956-959` + `dimensions` `:1541`）。
//
// 顺序与官方一致：先 title 再尺寸——官方也是先 `terminalTitle(request.title)`
// 后 `this.dimensions(...)`（`:1215-1216`），所以"标题空 + 尺寸也错"报的是标题。
func validateOfficialAgentTerminalRequest(method, sessionID string, req OfficialAgentTerminalRequest) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("%w: %s 缺 sessionId", ErrJTOfficialBadParam, method)
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || utf8.RuneCountInString(req.Title) > officialAgentTerminalMaxTitleRunes {
		return fmt.Errorf("%w: %s 的 title 必须是 1–%d 个字符（去首尾空白后非空），收到 %q",
			ErrJTOfficialBadParam, method, officialAgentTerminalMaxTitleRunes, req.Title)
	}
	if req.Cols < officialAgentTerminalMinCols || req.Cols > officialAgentTerminalMaxCols ||
		req.Rows < officialAgentTerminalMinRows || req.Rows > officialAgentTerminalMaxRows {
		return fmt.Errorf("%w: %s 的尺寸越界：cols 必须在 [%d,%d]、rows 必须在 [%d,%d]（收到 %d×%d）",
			ErrJTOfficialBadParam, method,
			officialAgentTerminalMinCols, officialAgentTerminalMaxCols,
			officialAgentTerminalMinRows, officialAgentTerminalMaxRows, req.Cols, req.Rows)
	}
	if req.Policy.Mode == "" {
		return fmt.Errorf("%w: %s 缺 policy.mode（官方 SandboxMode 三档之一 %v）",
			ErrJTOfficialBadParam, method, officialTerminalSandboxModes)
	}
	if !officialTerminalEnumHas(officialTerminalSandboxModes, req.Policy.Mode) {
		return fmt.Errorf("%w: %s 的 policy.mode=%q 不在官方枚举 %v 里",
			ErrJTOfficialBadParam, method, req.Policy.Mode, officialTerminalSandboxModes)
	}
	return nil
}
