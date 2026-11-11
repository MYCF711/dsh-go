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
		out = append(out, BridgeTerminal{
			ID: t.ID, Title: t.Title, Cwd: t.Cwd,
			Control: t.Control, State: t.State, Running: t.Running, ExitCode: t.ExitCode,
		})
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
		out = append(out, PanelTerminal{
			ID: t.ID, Title: t.Title, Cwd: t.Cwd,
			Control: t.Control, State: t.State, Running: t.Running, ExitCode: t.ExitCode,
		})
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

// maxBridgeReadLines 是 read 请求行数的防御上限。
//
// 工具层已经把 lines 校验成正整数；这里的上限只防"对面写了个 1e9"时
// 让分页逻辑去遍历一个没意义的数字（屏幕实际行数仍然决定结果）。
const maxBridgeReadLines = 100_000

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
	default:
		reply.Error = fmt.Sprintf("未知 op %q（只认 %s / %s）", req.Op, BridgeOpList, BridgeOpRead)
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

// 编译期钉住：子进程这侧也必须满足工具层的接口。
var _ UserTerminals = (*BridgeClient)(nil)

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
