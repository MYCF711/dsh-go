package terminal

// 终端 roster 的**父子进程帧协议**（`user_terminal_list` / `user_terminal_read` 的桥）。
//
// # 为什么需要一层帧
//
// 官方的这两个工具属于 `@deepseek-ai/dsh-terminal-panel`，升级指南写明它们"requires
// the Web or Desktop composition, which mounts the terminal controller"——也就是
// 工具与终端控制器必须在**同一个进程**里。本宿主不是：`dshd serve` 持有真 roster
// （`cmd/dshd/serve.go` 的 `officialJT`），而每一轮的工具是在 `dshd run` 子进程里
// 执行的（`cmd/dshd/serve_tools.go` 的 `loopRunner`）。所以工具要读到真终端，
// 就必须有一条父→子的数据通道。
//
// 通道复用仓库里既有的 stdio 帧格式（`@@JOBS@@` / `@@APPROVAL@@` / `@@STEER@@` 同族）：
// 一整行、行首一个 tag、后面是 JSON。方向与审批相同（子进程问、父进程答）：
//
//	子→父（子进程 stderr）  BridgeRequestTag  + BridgeRequest
//	父→子（子进程 stdin）   BridgeResponseTag + BridgeResponse
//
// # 为什么是"问-答"而不是"父进程推快照"
//
// 推快照要缓存，就会引入"轮内过期"（C2 报告 §6.3 方案 B 的代价），而且要回答
// "读屏"时仍然得再问一次。**每次调用都读当前真值**最省状态也最不容易骗人：
// 父进程这一侧调用的就是**同一份** `UserTerminals`（生产中由
// `transport.NewUserTerminalSource(officialJT, …)` 提供，与 `terminal.watch`
// 同一份 roster），子进程只是把这次调用原样转发过去。
//
// # 失败模式（都必须是结构化失败，不许回空列表）
//
//   - 没有父进程（headless `dshd run` / `dshd exec`）：`ErrBridgeUnavailable`；
//   - 父进程没在超时内回答（父进程忙死/卡住）：ctx 到期，错误里带 requestId；
//   - 请求不属于当前子进程的会话：父进程回 `cross_session`（与 jobs 通道同口径）；
//   - 父进程的 roster/读屏自身失败：原样把错误文本回给子进程。
//
// 本文件只有**协议与两侧实现**，不碰 os/exec/stdin：具体接到哪根管道由
// `cmd/dshd/terminal_bridge.go` 决定，这样两份实现都能用内存管道做单测。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 帧标记。与既有通道一样，标记后紧跟 JSON，整帧一行。
const (
	// BridgeRequestTag 是子进程 stderr 上的请求行首。
	BridgeRequestTag = "@@USER-TERMINALS@@ "
	// BridgeResponseTag 是父进程写进子进程 stdin 的应答行首。
	BridgeResponseTag = "@@USER-TERMINALS-RESP@@ "
)

// 请求的两个动作。宁可显式列 op，也不要用"带了 id 就是 read"这种隐式判据：
// 隐式判据会让一个写错的请求变成另一件事，而不是一个错误。
const (
	BridgeOpList = "list"
	BridgeOpRead = "read"
	// BridgeOpOpen 是官方 `openForAgent`（agent 终端分配）的桥 op。
	//
	// 为什么它必须走桥：本宿主的工具跑在 `dshd run` 子进程里，而终端控制器
	// （`serve` 的 `officialPTYTerminalAPI`）在父进程。官方是同一个进程里的
	// `ctx.terminals.registerBackend({open: … openForAgent …})`
	// （`dsh-terminal-panel/src/index.ts:34-37`），我们没有那个前提，所以只能像
	// list/read 一样把这次调用原样转给父进程。
	BridgeOpOpen = "open"

	// 下面五个是 agent 终端**交互**的桥 op（`dsh-tool-terminal` 那五个工具）。
	//
	// 为什么也必须过桥：工具跑在 `dshd run` 子进程里，而真 ConPTY 归 serve 父进程的
	// `officialPTYTerminalAPI` 所有。官方这五个工具消费的是 `ctx.terminals`
	// （`dsh-terminal/src/index.ts:224-293` 的 startSend/read/signal/kill/list），
	// 本宿主对应的就是这里的五个 op。
	//   agent-list   列这条会话里 agent 自己开的终端（官方 list(owner)）
	//   agent-send   写一次输入并等就绪/静默/超时/退出（官方 startSend）
	//   agent-read   回一页有界历史输出（官方 read）
	//   agent-signal 投信号给前台进程组（官方 signal）
	//   agent-close  关掉一条终端（官方 kill）
	BridgeOpAgentList   = "agent-list"
	BridgeOpAgentSend   = "agent-send"
	BridgeOpAgentRead   = "agent-read"
	BridgeOpAgentSignal = "agent-signal"
	BridgeOpAgentClose  = "agent-close"
)

// ErrBridgeUnavailable 表示"这个进程里没有父进程 roster 桥"。
//
// 它是**接线缺失**，不是"没有终端"：调用方必须把它原样报给模型，
// 绝不能翻译成空列表（那会让模型以为用户没有终端，转而用 bash 去猜）。
var ErrBridgeUnavailable = errors.New(
	"这个进程里没有父进程终端 roster 桥（user_terminal_* 只能在 serve 起的工具子进程里读终端）")

// BridgeRequest 是子进程发出的一次调用。
type BridgeRequest struct {
	// RequestID 由子进程生成，应答原样带回：父进程不需要记住任何东西。
	RequestID string `json:"requestId"`
	// SessionID 是发起调用的会话（父进程据此取 roster；不匹配就拒绝）。
	SessionID string `json:"sessionId"`
	// Op 是 BridgeOpList 或 BridgeOpRead。
	Op string `json:"op"`
	// ID 只有 read 用：终端 id。
	ID string `json:"id,omitempty"`
	// Lines 只有 read 用：最新多少行（0 = 由数据源决定，工具层保证为正）。
	Lines int `json:"lines,omitempty"`

	// 以下字段只有 open 用，逐条对应官方 `AgentTerminalRequest`
	// （`dsh-api-terminal-controller/src/agent.ts:23-39`）。
	Title           string            `json:"title,omitempty"`
	Cwd             string            `json:"cwd,omitempty"`
	Cols            int               `json:"cols,omitempty"`
	Rows            int               `json:"rows,omitempty"`
	PolicyMode      string            `json:"policyMode,omitempty"`
	PolicyWorkspace string            `json:"policyWorkspaceRoot,omitempty"`
	PreferredShells []string          `json:"preferredShells,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	// Name 是官方 spawn 请求里的 owner-local 显示名（`dsh-terminal/src/types.ts:29-36`
	// 的 `name?`）。它**不是**标题：官方注册表把 name 与 title 分开存，`terminal_list`
	// 回的是 name（`dsh-terminal/src/index.ts:375-386`）。open 时带上，list 才能如实回。
	Name string `json:"name,omitempty"`
	// Type 是官方 spawn 请求的后端类型（`dsh-terminal/src/types.ts:28-36` 的 `type`）：
	// 注册表按它选后端，`terminal_list` 的快照也回它。空 = 宿主默认（shell）。
	Type string `json:"type,omitempty"`

	// 以下字段只有 agent 终端交互的五个 op 用。

	// Text 是 agent-send 要写进终端的原文（官方 `TerminalSendRequest.text`）。
	Text string `json:"text,omitempty"`
	// Submit 决定写完是否补一个回车（官方默认 true，所以必须能区分"没给"与 false）。
	Submit *bool `json:"submit,omitempty"`
	// Signal 只有 agent-signal 用，取值见 `terminal.AgentSignalAllowed`。
	Signal string `json:"signal,omitempty"`
	// SendOpts / ReadOpts 是官方 terminal-panel 的等待阈值与分页参数。
	//
	// 为什么阈值要过桥而不是在父进程再配一份：官方的这些值属于 `dsh-terminal-panel`
	// 的 config（`terminal-panel/src/config.ts:45-51`），那是**工具包**；本宿主的工具包
	// 与真终端不同进程，配置也只能有一份，所以随请求带过去（父进程原样使用）。
	SendOpts *BridgeSendOptions `json:"send,omitempty"`
	ReadOpts *BridgeReadOptions `json:"read,omitempty"`
}

// BridgeResponse 是父进程的回答。OK=false 时 Error 一定非空。
type BridgeResponse struct {
	RequestID string `json:"requestId"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	// Terminals 只在 list 成功时出现。
	Terminals []BridgeTerminal `json:"terminals,omitempty"`
	// Page 只在 read 成功时出现。
	Page *BridgeScreenPage `json:"page,omitempty"`
	// Terminal 只在 open 成功时出现（分配出来的那条 agent 终端）。
	Terminal *BridgeTerminal `json:"terminal,omitempty"`
	// AgentTerminals 只在 agent-list 成功时出现。
	AgentTerminals []BridgeAgentTerminal `json:"agentTerminals,omitempty"`
	// Send 只在 agent-send 成功时出现。
	Send *BridgeAgentSend `json:"send,omitempty"`
	// Pgid 只在 agent-signal 成功时出现（真实收到信号的进程组）。
	Pgid *int `json:"pgid,omitempty"`
	// Closed 只在 agent-close 成功时出现：true = 这次关掉了，false = 同一个关闭已在飞。
	Closed *bool `json:"closed,omitempty"`
}

// BridgeTerminal 是 `PanelTerminal` 的线上形状（字段名与工具面 Data 一致）。
type BridgeTerminal struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Cwd      string `json:"cwd"`
	Control  string `json:"control"`
	State    string `json:"state"`
	Running  string `json:"running"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

// BridgeScreenPage 是 `ScreenPage` 的线上形状。
type BridgeScreenPage struct {
	Text       string `json:"text"`
	TotalLines int    `json:"totalLines"`
	LineBegin  int    `json:"lineBegin"`
	LineEnd    int    `json:"lineEnd"`
	Truncated  bool   `json:"truncated"`
}

// bridgeTerminalsOf 把工具面投影成线上形状。
func bridgeTerminalsOf(in []PanelTerminal) []BridgeTerminal {
	if in == nil {
		return nil
	}
	out := make([]BridgeTerminal, 0, len(in))
	for _, t := range in {
		// 字段名与类型逐字相同（panel.go:79 与 bridge.go:96），所以是类型转换，
		// 不是逐字段搬运：任何一个字段改名/改类型，这一行会编译失败而不是静默丢字段。
		out = append(out, BridgeTerminal(t))
	}
	return out
}

// panelTerminalsOf 是 bridgeTerminalsOf 的逆变换。
func panelTerminalsOf(in []BridgeTerminal) []PanelTerminal {
	if in == nil {
		return nil
	}
	out := make([]PanelTerminal, 0, len(in))
	for _, t := range in {
		// 反向同理：线上形状与工具面形状字段完全一致，用转换而不是重铺一遍。
		out = append(out, PanelTerminal(t))
	}
	return out
}

// bridgeScreenPageOf 把整页投影成线上形状。
func bridgeScreenPageOf(p ScreenPage) *BridgeScreenPage {
	return &BridgeScreenPage{
		Text: p.Text, TotalLines: p.TotalLines,
		LineBegin: p.LineBegin, LineEnd: p.LineEnd, Truncated: p.Truncated,
	}
}

// screenPageOf 是 bridgeScreenPageOf 的逆变换。
func screenPageOf(p *BridgeScreenPage) ScreenPage {
	if p == nil {
		return ScreenPage{}
	}
	return ScreenPage{
		Text: p.Text, TotalLines: p.TotalLines,
		LineBegin: p.LineBegin, LineEnd: p.LineEnd, Truncated: p.Truncated,
	}
}

// BridgeAgentStatus / BridgeAgentTerminal / BridgeAgentSend 是 agent 终端交互的线上形状。
//
// 字段与官方一一对应：
//
//	kind/exitCode/signal   TerminalSessionStatus（`dsh-terminal/src/types.ts:24-26`）
//	sessionId/name/type/pid/status  TerminalSessionSnapshot（`:118-130`）
//	viewport/waitReason/status/truncated  TerminalSendResult（`:66-76`）
//
// 为什么 exited 的 exitCode/signal 不带 omitempty：官方 schema 里它们是 exited 分支的
// **必填**字段（可空），省掉就成了另一种形状（`tool-terminal/src/index.ts:89-92`）。
type BridgeAgentStatus struct {
	Kind     string  `json:"kind"`
	ExitCode *int    `json:"exitCode"`
	Signal   *string `json:"signal"`
}

// BridgeAgentTerminal 是 terminal_list 的一行。
type BridgeAgentTerminal struct {
	SessionID string            `json:"sessionId"`
	Name      string            `json:"name,omitempty"`
	Type      string            `json:"type"`
	PID       int               `json:"pid,omitempty"`
	Status    BridgeAgentStatus `json:"status"`
}

// BridgeAgentSend 是一次 send 的结算。
type BridgeAgentSend struct {
	Viewport   string            `json:"viewport"`
	WaitReason string            `json:"waitReason"`
	Status     BridgeAgentStatus `json:"status"`
	Truncated  bool              `json:"truncated"`
}

// BridgeSendOptions 是官方 terminal-panel 的等待阈值（`terminal-panel/src/config.ts:47-51`）。
type BridgeSendOptions struct {
	PromptSilenceMs int `json:"promptSilenceMs"`
	IdleSilenceMs   int `json:"idleSilenceMs"`
	ProbeIntervalMs int `json:"probeIntervalMs"`
	TimeoutMs       int `json:"timeoutMs"`
	MaxReadBytes    int `json:"maxReadBytes"`
}

// BridgeReadOptions 是一次回读的分页参数。
//
// Offset/Count 用 float64 而不是 int：官方 schema 的类型是 `number`
// （`tool-terminal/src/index.ts:311-312`），而校验在**后端**（`terminal-panel/src/session.ts:344-345`
// 的 `Number.isSafeInteger`）。用 int 会让 "1.5" 在解码阶段就被 Go 拒掉，模型收到的
// 是另一句错误——校验点必须只有一个，且吐官方的原话。
type BridgeReadOptions struct {
	Offset       float64 `json:"offset"`
	Count        float64 `json:"count"`
	MaxReadBytes int     `json:"maxReadBytes"`
}

// bridgeAgentStatusOf 把宿主状态投影成线上形状。
func bridgeAgentStatusOf(st AgentTerminalStatus) BridgeAgentStatus {
	return BridgeAgentStatus{Kind: st.Kind, ExitCode: st.ExitCode, Signal: st.Signal}
}

// agentStatusOf 是逆变换。
func agentStatusOf(st BridgeAgentStatus) AgentTerminalStatus {
	return AgentTerminalStatus{Kind: st.Kind, ExitCode: st.ExitCode, Signal: st.Signal}
}

// bridgeAgentTerminalOf 把一条快照投影成线上形状。
func bridgeAgentTerminalOf(snap AgentTerminalSnapshot) BridgeAgentTerminal {
	return BridgeAgentTerminal{
		SessionID: snap.SessionID,
		Name:      snap.Name,
		Type:      snap.Type,
		PID:       snap.PID,
		Status:    bridgeAgentStatusOf(snap.Status),
	}
}

// agentTerminalOf 是逆变换。
func agentTerminalOf(snap BridgeAgentTerminal) AgentTerminalSnapshot {
	return AgentTerminalSnapshot{
		SessionID: snap.SessionID,
		Name:      snap.Name,
		Type:      snap.Type,
		PID:       snap.PID,
		Status:    agentStatusOf(snap.Status),
	}
}

// bridgeAgentSendOf 把一次 send 结算投影成线上形状。
func bridgeAgentSendOf(res AgentSendResult) *BridgeAgentSend {
	return &BridgeAgentSend{
		Viewport:   res.Viewport,
		WaitReason: res.WaitReason,
		Status:     bridgeAgentStatusOf(res.Status),
		Truncated:  res.Truncated,
	}
}

// agentSendResultOf 是逆变换。
func agentSendResultOf(res *BridgeAgentSend) AgentSendResult {
	return AgentSendResult{
		Viewport:   res.Viewport,
		WaitReason: res.WaitReason,
		Status:     agentStatusOf(res.Status),
		Truncated:  res.Truncated,
	}
}

// agentSendRequestOf 把线上请求换算成宿主调用参数（阈值缺项一律 0，由父进程判非法）。
func agentSendRequestOf(req BridgeRequest) AgentSendRequest {
	out := AgentSendRequest{
		Text:                req.Text,
		Submit:              req.Submit == nil || *req.Submit,
		PolicyMode:          req.PolicyMode,
		PolicyWorkspaceRoot: req.PolicyWorkspace,
	}
	if o := req.SendOpts; o != nil {
		out.PromptSilence = time.Duration(o.PromptSilenceMs) * time.Millisecond
		out.IdleSilence = time.Duration(o.IdleSilenceMs) * time.Millisecond
		out.ProbeInterval = time.Duration(o.ProbeIntervalMs) * time.Millisecond
		out.Timeout = time.Duration(o.TimeoutMs) * time.Millisecond
		out.MaxReadBytes = o.MaxReadBytes
	}
	return out
}

// agentReadRequestOf 把线上请求换算成宿主调用参数。
func agentReadRequestOf(req BridgeRequest) (AgentReadRequest, error) {
	o := req.ReadOpts
	if o == nil {
		return AgentReadRequest{}, errors.New("agent-read 请求缺分页参数（read）：协议违约")
	}
	out := AgentReadRequest{MaxReadBytes: o.MaxReadBytes}
	// 官方校验在后端（`terminal-panel/src/session.ts:344-345`）：offset 非负安全整数、
	// count 正安全整数。这里只做"能不能落成 int"，语义判据留给真实实现（它有官方原话）。
	offset, ok := safeInt(o.Offset)
	if !ok {
		return AgentReadRequest{}, fmt.Errorf("%w: 请求体里的 offset=%v 不能落成整数", errBridgeBadFrame, o.Offset)
	}
	count, ok := safeInt(o.Count)
	if !ok {
		return AgentReadRequest{}, fmt.Errorf("%w: 请求体里的 count=%v 不能落成整数", errBridgeBadFrame, o.Count)
	}
	out.Offset, out.Count = offset, count
	return out, nil
}

// errBridgeBadFrame 是"父进程收到一帧不完整请求"的哨兵：它是协议违约，不是
// 模型参数问题（模型参数由工具层与真实实现各自校验，且吐官方原话）。
var errBridgeBadFrame = errors.New("终端桥请求帧不完整")

// safeInt 把 JSON number 落成 int；非整数或越界返回 false。
func safeInt(v float64) (int, bool) {
	if v != float64(int(v)) {
		return 0, false
	}
	return int(v), true
}

// maxBridgeReadLines 是 read 请求行数的防御上限。
//
// 工具层已经把 lines 校验成正整数；这里的上限只防"对面写了个 1e9"时
// 让分页逻辑去遍历一个没意义的数字（屏幕实际行数仍然决定结果）。
const maxBridgeReadLines = 100_000

// AgentOpenRequest 是 `open` op 的参数，字段与官方 `AgentTerminalRequest` 同名同义
// （`dsh-api-terminal-controller/src/agent.ts:23-39`）。
//
// 为什么在本包再声明一份而不是复用 transport 的类型：`internal/transport` 依赖本包
// （PID/roster 的类型在那里），反向 import 就是环。字段名保持一致，换算点只有一处
// （`transport.UserTerminalSource.OpenAgentTerminal`）。
type AgentOpenRequest struct {
	// Title 是 1–120 字符的标签页标题（官方去首尾空白后判）。
	Title string
	// Cwd 是绝对初始目录；空 = 会话工作目录。
	Cwd string
	// Cols / Rows 是初始窗口尺寸。
	Cols int
	Rows int
	// PolicyMode 是官方 SandboxMode（read-only | workspace-write | danger-full-access）。
	PolicyMode string
	// PolicyWorkspaceRoot 是官方 SandboxExecutionPolicy.workspaceRoot。
	PolicyWorkspaceRoot string
	// PreferredShells 是按顺序尝试的 shell 名（官方 findShell 语义：忽略大小写与 .exe）。
	PreferredShells []string
	// Env 是追加进子 shell 环境的变量（官方 `env?`）。
	Env map[string]string
	// Name 是 owner-local 显示名（官方 spawn 请求的 `name?`，**不是**标题）。
	// `terminal_list` 回的就是它（`dsh-terminal/src/index.ts:375-386`）；空 = 没给。
	Name string
	// Type 是注册表里的后端类型（官方 spawn 请求的 `type`，`dsh-terminal/src/types.ts:28-36`）。
	// `terminal_list` 的快照要回它；空 = 宿主用自己的默认值（shell）。
	Type string
}

// AgentTerminalOpener 是"这一侧能分配 agent 终端"的可选能力。
//
// 数据源实现了它，桥的 `open` 才可用；没实现 → 明确失败（缺的正是官方
// `openForAgent` 那条分配路径），**不**退化成"建一条 user 终端再谎报 agent"。
type AgentTerminalOpener interface {
	OpenAgentTerminal(ctx context.Context, sessionID string, req AgentOpenRequest) (PanelTerminal, error)
}

// BridgeServer 是**持有真 roster 的那一侧**（serve 父进程）。
//
// 它只做翻译：协议请求 → `UserTerminals` 调用 → 协议应答。不缓存、不持有状态，
// 所以父进程这一侧不可能出现"工具看到的是旧 roster"。
type BridgeServer struct {
	source UserTerminals
}

// NewBridgeServer 用一个数据源造桥的服务端。
//
// source 必须是**与 `terminal.watch` 同一份**真值（生产中就是
// `transport.NewUserTerminalSource(officialJT, originFn)`）；传 nil 时
// 每个请求都会得到明确的"数据源没接"错误，而不是空列表。
func NewBridgeServer(source UserTerminals) *BridgeServer {
	return &BridgeServer{source: source}
}

// Handle 处理一行子进程 stderr 输出。
//
// 返回 (应答整行（含 tag，不含换行）, 是否本协议的行)。不是本协议的行返回 ("", false)，
// 调用方据此继续走原有的审批/任务分发。
//
// 参数 sessionID 是**当前子进程所属的会话**（父进程从 WatchSession 拿到）：
// 请求里带的会话必须与它一致，否则拒绝。理由与 jobs 通道相同——猜任何一个都会
// 把 A 会话的终端名册发给 B 会话。
func (s *BridgeServer) Handle(ctx context.Context, sessionID, line string) (string, bool) {
	if !strings.HasPrefix(line, BridgeRequestTag) {
		return "", false
	}
	payload := strings.TrimPrefix(line, BridgeRequestTag)

	var req BridgeRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		// 坏帧也要回一句：不然子进程只能等到超时，日志里什么都没留下。
		return bridgeReplyLine(BridgeResponse{Error: "请求帧不是合法 JSON: " + err.Error()}), true
	}
	reply := BridgeResponse{RequestID: req.RequestID}
	switch {
	case req.RequestID == "":
		reply.Error = "请求帧缺 requestId；子进程无法把这条应答配上"
	case strings.TrimSpace(sessionID) != "" && req.SessionID != "" && req.SessionID != sessionID:
		reply.Error = fmt.Sprintf("cross_session: 请求属于会话 %q，本子进程是 %q", req.SessionID, sessionID)
	case req.SessionID == "":
		reply.Error = "请求帧缺 sessionId；父进程不知道要读哪个会话的终端"
	case s == nil || s.source == nil:
		reply.Error = "宿主没有提供终端数据源（terminal.UserTerminals）：这是父进程接线缺失，改参数救不了"
	default:
		reply = s.serve(ctx, req)
	}
	return bridgeReplyLine(reply), true
}

// serve 是成功解析之后的分派。
func (s *BridgeServer) serve(ctx context.Context, req BridgeRequest) BridgeResponse {
	reply := BridgeResponse{RequestID: req.RequestID}
	switch req.Op {
	case BridgeOpList:
		terminals, err := s.source.ListUserTerminals(ctx, req.SessionID)
		if err != nil {
			reply.Error = "列终端失败: " + err.Error()
			return reply
		}
		reply.OK = true
		reply.Terminals = bridgeTerminalsOf(terminals)
	case BridgeOpRead:
		id := strings.TrimSpace(req.ID)
		if id == "" {
			reply.Error = "read 请求缺 id（终端 id 由 user_terminal_list 给出）"
			return reply
		}
		lines := req.Lines
		if lines <= 0 {
			lines = 1
		}
		if lines > maxBridgeReadLines {
			lines = maxBridgeReadLines
		}
		page, err := s.source.ReadUserScreen(ctx, req.SessionID, id, lines)
		if err != nil {
			reply.Error = "读终端屏幕失败: " + err.Error()
			return reply
		}
		reply.OK = true
		reply.Page = bridgeScreenPageOf(page)
	case BridgeOpOpen:
		opener, ok := s.source.(AgentTerminalOpener)
		if !ok {
			// 数据源没有分配能力：这正是官方 `openForAgent` 那条路径缺位，
			// 必须说清（工具会把这句原样报给模型），不能回一条假终端。
			reply.Error = "这个宿主的终端数据源没有接 agent 终端分配（openForAgent/agentLaunch）：改参数救不了"
			return reply
		}
		terminalInfo, err := opener.OpenAgentTerminal(ctx, req.SessionID, AgentOpenRequest{
			Title: req.Title, Cwd: req.Cwd, Cols: req.Cols, Rows: req.Rows,
			PolicyMode: req.PolicyMode, PolicyWorkspaceRoot: req.PolicyWorkspace,
			PreferredShells: req.PreferredShells, Env: req.Env, Name: req.Name, Type: req.Type,
		})
		if err != nil {
			reply.Error = "分配 agent 终端失败: " + err.Error()
			return reply
		}
		wire := BridgeTerminal(terminalInfo)
		reply.OK = true
		reply.Terminal = &wire
	case BridgeOpAgentList, BridgeOpAgentSend, BridgeOpAgentRead, BridgeOpAgentSignal, BridgeOpAgentClose:
		controls, ok := s.source.(AgentTerminalControls)
		if !ok {
			// 数据源没有交互能力：这正是官方 `ctx.terminals` 那五个方法（startSend/read/
			// signal/kill/list）在本宿主缺位。必须说清，不能回空列表/假 delivered。
			reply.Error = "这个宿主的终端数据源没有接 agent 终端交互（terminal.AgentTerminalControls）：" +
				"send/read/signal/close/list 全部不可用，改参数救不了"
			return reply
		}
		reply = serveAgentTerminalOp(ctx, controls, req, reply)
	default:
		reply.Error = fmt.Sprintf("未知 op %q（只认 %s / %s / %s / %s / %s / %s / %s / %s）",
			req.Op, BridgeOpList, BridgeOpRead, BridgeOpOpen,
			BridgeOpAgentList, BridgeOpAgentSend, BridgeOpAgentRead, BridgeOpAgentSignal, BridgeOpAgentClose)
	}
	return reply
}

// serveAgentTerminalOp 是 agent 终端五个 op 的分派（成功时回填各自的载荷）。
func serveAgentTerminalOp(ctx context.Context, controls AgentTerminalControls, req BridgeRequest, reply BridgeResponse) BridgeResponse {
	switch req.Op {
	case BridgeOpAgentList:
		sessions, err := controls.AgentTerminalList(ctx, req.SessionID)
		if err != nil {
			reply.Error = "列 agent 终端失败: " + err.Error()
			return reply
		}
		out := make([]BridgeAgentTerminal, 0, len(sessions))
		for _, snap := range sessions {
			out = append(out, bridgeAgentTerminalOf(snap))
		}
		reply.OK = true
		reply.AgentTerminals = out
	case BridgeOpAgentSend:
		id := strings.TrimSpace(req.ID)
		if id == "" {
			reply.Error = "agent-send 请求缺 id（终端 id 由 terminal_open / terminal_list 给出）"
			return reply
		}
		res, err := controls.AgentTerminalSend(ctx, req.SessionID, id, agentSendRequestOf(req))
		if err != nil {
			// 官方这层会拒绝"人已接管"（TAKEN_OVER）、"策略变了"（POLICY_CHANGED）、
			// "已有在跑的 send"（SEND_ACTIVE）等；原样带回，不翻译成通用失败。
			reply.Error = err.Error()
			return reply
		}
		reply.OK = true
		reply.Send = bridgeAgentSendOf(res)
	case BridgeOpAgentRead:
		id := strings.TrimSpace(req.ID)
		if id == "" {
			reply.Error = "agent-read 请求缺 id（终端 id 由 terminal_open / terminal_list 给出）"
			return reply
		}
		readReq, err := agentReadRequestOf(req)
		if err != nil {
			reply.Error = err.Error()
			return reply
		}
		page, err := controls.AgentTerminalRead(ctx, req.SessionID, id, readReq)
		if err != nil {
			reply.Error = err.Error()
			return reply
		}
		reply.OK = true
		wire := bridgeScreenPageOf(page)
		reply.Page = wire
	case BridgeOpAgentSignal:
		id := strings.TrimSpace(req.ID)
		if id == "" {
			reply.Error = "agent-signal 请求缺 id（终端 id 由 terminal_open / terminal_list 给出）"
			return reply
		}
		pgid, err := controls.AgentTerminalSignal(ctx, req.SessionID, id, req.Signal)
		if err != nil {
			reply.Error = err.Error()
			return reply
		}
		reply.OK = true
		reply.Pgid = &pgid
	case BridgeOpAgentClose:
		id := strings.TrimSpace(req.ID)
		if id == "" {
			reply.Error = "agent-close 请求缺 id（终端 id 由 terminal_open / terminal_list 给出）"
			return reply
		}
		closed, err := controls.AgentTerminalClose(ctx, req.SessionID, id)
		if err != nil {
			reply.Error = err.Error()
			return reply
		}
		reply.OK = true
		reply.Closed = &closed
	}
	return reply
}

// bridgeReplyLine 把应答编成一行（含 tag，不含换行）。
//
// 编码失败（理论上只会来自非法 UTF-8）也要给出一行合法应答：回
// `{"ok":false,"error":…}`，而不是让子进程等到超时。
func bridgeReplyLine(reply BridgeResponse) string {
	raw, err := json.Marshal(reply)
	if err != nil {
		fallback, ferr := json.Marshal(BridgeResponse{
			RequestID: reply.RequestID,
			Error:     "应答编码失败: " + err.Error(),
		})
		if ferr != nil {
			return BridgeResponseTag + `{"ok":false,"error":"应答编码失败"}`
		}
		return BridgeResponseTag + string(fallback)
	}
	return BridgeResponseTag + string(raw)
}

// BridgeClient 是**子进程这一侧**：把 `UserTerminals` 的三个方法转发给父进程。
//
// 它满足 `terminal.UserTerminals`，所以在子进程里可以直接
// `kernel.Provide(root, terminal.UserTerminalsKey, client)`——工具层完全不知道
// 数据是从另一条管道来的。
type BridgeClient struct {
	// write 往子进程 stderr 写一整行（含换行）。nil = 没有父进程桥。
	write func(line []byte) error
	// origin 回答"这条会话是不是子代理"。
	//
	// **刻意留在进程内**（不走桥）：子代理拒绝必须在数据源不可用时也成立——
	// 否则桥一断，子代理就从"被拒绝"变成"读不到"，那是把权限问题的表现改成
	// 了可用性问题。事实来源是 `agent.FindChild`（接线见 cmd/dshd/terminal_bridge.go）。
	origin func(ctx context.Context, sessionID string) (string, error)

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan BridgeResponse
	seq     atomic.Uint64
}

// NewBridgeClient 造一个客户端。write=nil 表示这个进程里没有桥（headless）。
func NewBridgeClient(write func(line []byte) error, origin func(ctx context.Context, sessionID string) (string, error)) *BridgeClient {
	return &BridgeClient{write: write, origin: origin}
}

// 编译期钉住：子进程这侧也必须满足工具层的接口（含 agent 终端分配）。
var (
	_ UserTerminals         = (*BridgeClient)(nil)
	_ AgentTerminalOpener   = (*BridgeClient)(nil)
	_ AgentTerminalControls = (*BridgeClient)(nil)
)

// Available 报告这个客户端背后有没有父进程桥。
func (c *BridgeClient) Available() bool { return c != nil && c.write != nil }

// Deliver 把父进程写来的一行应答投给等待中的调用。
//
// 返回是否认得出（是应答帧）。没人等（调用方已超时走了）就丢掉——
// **绝不阻塞**：这里是 stdin 分发 goroutine 上的一环，卡住会连带审批/steer 收不到。
func (c *BridgeClient) Deliver(line string) bool {
	if !strings.HasPrefix(line, BridgeResponseTag) {
		return false
	}
	var resp BridgeResponse
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, BridgeResponseTag)), &resp); err != nil {
		return false // 坏行当没看见：等的人会走超时，而不是收到半份数据
	}
	if c == nil || resp.RequestID == "" {
		return false
	}
	c.mu.Lock()
	ch := c.pending[resp.RequestID]
	delete(c.pending, resp.RequestID)
	c.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- resp:
	default:
	}
	return true
}

// ListUserTerminals 把"列这条会话的终端"转发给父进程。
func (c *BridgeClient) ListUserTerminals(ctx context.Context, sessionID string) ([]PanelTerminal, error) {
	resp, err := c.call(ctx, BridgeRequest{SessionID: sessionID, Op: BridgeOpList})
	if err != nil {
		return nil, err
	}
	return panelTerminalsOf(resp.Terminals), nil
}

// ReadUserScreen 把"读一条终端的屏幕"转发给父进程。
func (c *BridgeClient) ReadUserScreen(ctx context.Context, sessionID, id string, lines int) (ScreenPage, error) {
	if lines <= 0 {
		lines = 1
	}
	resp, err := c.call(ctx, BridgeRequest{SessionID: sessionID, Op: BridgeOpRead, ID: id, Lines: lines})
	if err != nil {
		return ScreenPage{}, err
	}
	if resp.Page == nil {
		// 父进程说 ok 却没给页面：这是协议违约，不能当成"空屏幕"（空屏幕会让
		// `user_terminal_read` 以为终端什么都没打印）。
		return ScreenPage{}, fmt.Errorf("父进程应答缺 page（终端 %s）：协议违约", id)
	}
	return screenPageOf(resp.Page), nil
}

// OpenAgentTerminal 把"分配一条 agent 终端"转发给父进程（官方 `openForAgent`）。
//
// 父进程没有分配能力时，错误原样带回来（见 BridgeServer 的 open 分支：它会给一句
// 说明"这个宿主的数据源没接 openForAgent"，而不是回一条假终端）。
func (c *BridgeClient) OpenAgentTerminal(ctx context.Context, sessionID string, req AgentOpenRequest) (PanelTerminal, error) {
	resp, err := c.call(ctx, BridgeRequest{
		SessionID: sessionID, Op: BridgeOpOpen,
		Title: req.Title, Cwd: req.Cwd, Cols: req.Cols, Rows: req.Rows,
		PolicyMode: req.PolicyMode, PolicyWorkspace: req.PolicyWorkspaceRoot,
		PreferredShells: req.PreferredShells, Env: req.Env,
		Name: req.Name, Type: req.Type,
	})
	if err != nil {
		return PanelTerminal{}, err
	}
	if resp.Terminal == nil {
		// 父进程说 ok 却没给终端：协议违约，不许当成"分配成功但没信息"。
		return PanelTerminal{}, errors.New("父进程应答缺 terminal（open）：协议违约")
	}
	return PanelTerminal(*resp.Terminal), nil
}

// AgentTerminalList 把"列这条会话里 agent 自己开的终端"转发给父进程。
func (c *BridgeClient) AgentTerminalList(ctx context.Context, sessionID string) ([]AgentTerminalSnapshot, error) {
	resp, err := c.call(ctx, BridgeRequest{SessionID: sessionID, Op: BridgeOpAgentList})
	if err != nil {
		return nil, err
	}
	out := make([]AgentTerminalSnapshot, 0, len(resp.AgentTerminals))
	for _, wire := range resp.AgentTerminals {
		out = append(out, agentTerminalOf(wire))
	}
	return out, nil
}

// AgentTerminalSend 把一次交互输入转发给父进程，并把它的结算原样带回。
//
// 等待发生在**父进程**：那里才有真终端。本客户端只是把官方 terminal-panel 的阈值
// 一起带过去（见 BridgeSendOptions 的注释）。
func (c *BridgeClient) AgentTerminalSend(ctx context.Context, sessionID, id string, req AgentSendRequest) (AgentSendResult, error) {
	submit := req.Submit
	resp, err := c.call(ctx, BridgeRequest{
		SessionID: sessionID, Op: BridgeOpAgentSend, ID: id,
		Text:   req.Text,
		Submit: &submit,
		// 策略比较用的就是 open 那三个字段（同一个官方 SandboxExecutionPolicy 语义）。
		PolicyMode:      req.PolicyMode,
		PolicyWorkspace: req.PolicyWorkspaceRoot,
		SendOpts: &BridgeSendOptions{
			PromptSilenceMs: int(req.PromptSilence / time.Millisecond),
			IdleSilenceMs:   int(req.IdleSilence / time.Millisecond),
			ProbeIntervalMs: int(req.ProbeInterval / time.Millisecond),
			TimeoutMs:       int(req.Timeout / time.Millisecond),
			MaxReadBytes:    req.MaxReadBytes,
		},
	})
	if err != nil {
		return AgentSendResult{}, err
	}
	if resp.Send == nil {
		// 父进程说 ok 却没给结算：协议违约，不许当成"没有输出"。
		return AgentSendResult{}, errors.New("父进程应答缺 send（agent-send）：协议违约")
	}
	return agentSendResultOf(resp.Send), nil
}

// AgentTerminalRead 把一次有界回读转发给父进程。
func (c *BridgeClient) AgentTerminalRead(ctx context.Context, sessionID, id string, req AgentReadRequest) (ScreenPage, error) {
	resp, err := c.call(ctx, BridgeRequest{
		SessionID: sessionID, Op: BridgeOpAgentRead, ID: id,
		ReadOpts: &BridgeReadOptions{
			Offset:       float64(req.Offset),
			Count:        float64(req.Count),
			MaxReadBytes: req.MaxReadBytes,
		},
	})
	if err != nil {
		return ScreenPage{}, err
	}
	if resp.Page == nil {
		return ScreenPage{}, fmt.Errorf("父进程应答缺 page（agent-read，终端 %s）：协议违约", id)
	}
	return screenPageOf(resp.Page), nil
}

// AgentTerminalSignal 把一次信号投递转发给父进程，返回真实收到的进程组 id。
func (c *BridgeClient) AgentTerminalSignal(ctx context.Context, sessionID, id, signal string) (int, error) {
	resp, err := c.call(ctx, BridgeRequest{SessionID: sessionID, Op: BridgeOpAgentSignal, ID: id, Signal: signal})
	if err != nil {
		return 0, err
	}
	if resp.Pgid == nil {
		return 0, fmt.Errorf("父进程应答缺 pgid（agent-signal，终端 %s）：协议违约", id)
	}
	return *resp.Pgid, nil
}

// AgentTerminalClose 转发一次关闭；true = 这次关掉了，false = 同一个关闭已在飞。
func (c *BridgeClient) AgentTerminalClose(ctx context.Context, sessionID, id string) (bool, error) {
	resp, err := c.call(ctx, BridgeRequest{SessionID: sessionID, Op: BridgeOpAgentClose, ID: id})
	if err != nil {
		return false, err
	}
	if resp.Closed == nil {
		return false, fmt.Errorf("父进程应答缺 closed（agent-close，终端 %s）：协议违约", id)
	}
	return *resp.Closed, nil
}

// SessionOrigin 在**本进程内**回答会话来源（见结构体注释：不走桥）。
func (c *BridgeClient) SessionOrigin(ctx context.Context, sessionID string) (string, error) {
	if c == nil {
		return "", errors.New("终端数据源为 nil")
	}
	if c.origin == nil {
		return "", errors.New("宿主没有提供会话来源判定（子代理拒绝需要它）：接线缺失")
	}
	return c.origin(ctx, sessionID)
}

// call 发一次请求并等应答。
func (c *BridgeClient) call(ctx context.Context, req BridgeRequest) (BridgeResponse, error) {
	if !c.Available() {
		return BridgeResponse{}, ErrBridgeUnavailable
	}
	req.RequestID = c.nextRequestID()

	raw, err := json.Marshal(req)
	if err != nil {
		return BridgeResponse{}, fmt.Errorf("编码终端桥请求失败: %w", err)
	}

	ch := make(chan BridgeResponse, 1)
	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[string]chan BridgeResponse{}
	}
	c.pending[req.RequestID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, req.RequestID)
		c.mu.Unlock()
	}()

	line := BridgeRequestTag + string(raw) + "\n"
	c.writeMu.Lock()
	werr := c.write([]byte(line))
	c.writeMu.Unlock()
	if werr != nil {
		// 写不出去基本就是父进程没了（EPIPE）：立刻报明，而不是等满一个超时。
		return BridgeResponse{}, fmt.Errorf("%w: 写不出请求（父进程可能已退出）: %w", ErrBridgeUnavailable, werr)
	}

	select {
	case resp := <-ch:
		if !resp.OK {
			if resp.Error == "" {
				resp.Error = "父进程回了失败但没有说明原因"
			}
			return resp, errors.New(resp.Error)
		}
		return resp, nil
	case <-ctx.Done():
		return BridgeResponse{}, fmt.Errorf("等父进程终端数据超时（%v，requestId=%s）: %w",
			ctx.Err(), req.RequestID, ctx.Err())
	}
}

// nextRequestID 生成一个只在"这个子进程 + 这一次调用"里唯一的 id。
//
// 带 pid 是为了在 A/B 两个子进程同时打到同一个父进程时也不会撞号——
// 撞号的表现是"A 的应答被投给 B 的等待者"，那是最坏的一类错。
func (c *BridgeClient) nextRequestID() string {
	n := c.seq.Add(1)
	return "uterm-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(n, 10)
}
