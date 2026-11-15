package main

// `dsh-terminal-panel`（官方 0.2.2-rc.2）两个只读工具的**父→子 roster 桥**。
//
// # 为什么需要这一层
//
// 官方这两个工具（`user_terminal_list` / `user_terminal_read`）的前提是
// "the Web or Desktop composition, which mounts the terminal controller"——
// 工具与终端控制器同进程。本宿主不是：
//
//	cmd/dshd/serve.go        ← 持有真 roster（`officialJT`，PTY 终端的唯一所有者）
//	cmd/dshd/serve_tools.go  ← 每轮起一个 `dshd run` 子进程，工具在**子进程**里执行
//
// 所以工具要读到真终端，就必须有一条父→子通道。仓库里既有的形状是 stdio 帧
// （`@@JOBS@@` / `@@APPROVAL@@` / `@@STEER@@`）：子进程往 stderr 写一行带标记的
// JSON，父进程的 `approvalBroker.WatchSession` 逐行分发，需要回答的再写回子进程
// stdin。本文件复用同一套通道，**不新起 IPC、不嵌 JS、不开 HTTP 回连**。
//
// # 链路
//
//	工具（dshd run 子进程，modules/tool/terminalpanel）
//	  → terminal.BridgeClient：把 ListUserTerminals / ReadUserScreen 编成一行
//	  → 子进程 stderr：`@@USER-TERMINALS@@ {requestId,sessionId,op,…}`
//	  → 父进程 serve_approval.go 的逐行分发（一行钩子）
//	  → serveUserTerminalBridgeLine（本文件）
//	  → terminal.BridgeServer
//	  → transport.NewUserTerminalSource(officialJT, originFn)   ← 与 terminal.watch 同一份 roster
//	  → 父进程写回子进程 stdin：`@@USER-TERMINALS-RESP@@ {requestId,ok,…}`
//	  → approval_stdio.go 的分发器 → BridgeClient.Deliver → 等待中的工具调用
//
// # 为什么子代理判定不走桥
//
// 官方判据是 `agent.session.header.origin === 'subagent'`（`terminal-panel/src/tools.ts:35`），
// 本宿主的真值来源是子会话日志里的 `subagent/spawn` 帧（`internal/agent.FindChild`）。
// 这个判定**留在子进程本地**（`terminalSessionOrigin`）：桥断了、父进程没起、
// 甚至没有桥（独立 `dshd run`），子代理也必须被拒绝——否则子代理会从"被拒绝"
// 退化成"读不到"，把权限问题的表现改成了可用性问题。
//
// # 失败模式
//
//   - 父进程没有装桥（`terminalBridges` 里没有这个 broker）：行照旧落到审批分发，
//     子进程按超时/写失败结构化报错；
//   - 没有 `DSHD_USER_TERMINALS_STDIO=1` 的子进程：`ErrBridgeUnavailable`（明确说
//     "这只能在 serve 起的工具子进程里读"，不是空列表）；
//   - 应答写不回子进程（子进程已退出）：父进程 stderr 留一行，工具侧走超时，
//     **绝不给一份陈旧数据冒充成功**。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dshgo/internal/agent"
	"dshgo/internal/kernel"
	"dshgo/internal/terminal"
	"dshgo/internal/transport"
)

// userTerminalsStdioEnv 与 `DSHD_JOBS_STDIO` / `DSHD_APPROVAL_STDIO` 同形：
// 父进程显式打开，子进程据此知道"stderr/stdin 上有一条 roster 通道"。
const userTerminalsStdioEnv = "DSHD_USER_TERMINALS_STDIO"

// terminalBridgeHandleTimeout 是父进程处理一次桥请求的上限。
//
// roster/读屏都是内存操作（PTY 的屏幕是有界滚动缓冲），正常在毫秒级。设上限只为
// 一件事：这条分发跑在**子进程 stderr 的唯一读端**上，卡住会连带审批/后台任务
// 清单都收不到。超时后工具侧会拿到一条明确失败，而不是永远等下去。
const terminalBridgeHandleTimeout = 10 * time.Second

// ---------------------------------------------------------------------------
// 父进程半边（serve）
// ---------------------------------------------------------------------------

// userTerminalBridge 是 serve 进程里的桥：协议服务端 + 写回子进程的出口。
type userTerminalBridge struct {
	server *terminal.BridgeServer
}

// terminalBridges 按 broker 索引。
//
// 为什么是外部表而不是 struct 字段：`approvalBroker` 的定义在 serve_approval.go，
// 本任务不改它的形状（只加一行分发钩子）。生产里一个 serve 进程只有一个 broker
// （serve.go 里 `newApprovalBroker` 只调一次），表里因此最多一两条；
// 测试里可能有多份，按 broker 索引才不会互相串。
var terminalBridges sync.Map // *approvalBroker → *userTerminalBridge

// installUserTerminalBridge 在 serve 里装上桥（拿到 `officialJT` 之后调用）。
//
// home 用于把"这条会话是不是子代理"接成 `agent.FindChild`（与工具层的
// `originFn` 同一个事实来源）。虽然子进程会先在本地判一次，父进程这一侧仍然
// 要有同一个 origin：**同一份适配器两种形态**（同进程 / 过桥）不能有两种胜负判据。
func installUserTerminalBridge(b *approvalBroker, jt transport.UserTerminalRoster, home string) (*userTerminalBridge, error) {
	if b == nil {
		return nil, errors.New("审批 broker 为 nil，终端桥没有可挂的分发点")
	}
	if jt == nil {
		return nil, errors.New("终端服务为 nil，终端桥没有数据来源")
	}
	src := transport.NewUserTerminalSource(jt, terminalSessionOrigin(home))
	br := &userTerminalBridge{server: terminal.NewBridgeServer(src)}
	terminalBridges.Store(b, br)
	return br, nil
}

// uninstallUserTerminalBridge 摘掉桥（serve 退出/测试收尾用）。
func uninstallUserTerminalBridge(b *approvalBroker) {
	if b != nil {
		terminalBridges.Delete(b)
	}
}

// serveUserTerminalBridgeLine 是 `approvalBroker.WatchSession` 里的那一行钩子。
//
// 返回 true = 这一行是本协议的帧，已被处理（调用方 continue）；false = 不是，
// 继续走原有的审批/任务分发。**顺序无关**：本协议的行首标记与其它三个都不同。
func serveUserTerminalBridgeLine(b *approvalBroker, sessionID, line string) bool {
	if b == nil || !strings.HasPrefix(line, terminal.BridgeRequestTag) {
		return false
	}
	v, ok := terminalBridges.Load(b)
	if !ok {
		// 没装桥（老装配/测试）：交给后面的分发器，被子进程当成"没人回答"。
		return false
	}
	br, _ := v.(*userTerminalBridge)
	if br == nil || br.server == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), terminalBridgeHandleTimeout)
	defer cancel()
	reply, handled := br.server.Handle(ctx, sessionID, line)
	if !handled {
		return false
	}
	if err := br.writeReply(b, sessionID, reply); err != nil {
		fmt.Fprintf(os.Stderr, "终端桥：应答写不回子进程（%v）；这一轮 user_terminal_* 会按超时失败\n", err)
	}
	return true
}

// writeReply 把一行应答写回**这个会话当前那一轮**的子进程 stdin。
//
// 优先用 jobActions 里登记的 per-turn stdin（按会话找到唯一那一轮），拿不到才退回
// broker 上"最后一条 stdin"。为什么要分开：A/B 两个会话可以同时在一轮里，
// broker 的 `stdin` 只有一个槽——写错了字（另一个会话的子进程）虽然不会让它读错
// 数据（requestId 对它不认得，会被忽略），但本会话的工具就只能等到超时。
func (br *userTerminalBridge) writeReply(b *approvalBroker, sessionID, line string) error {
	if b.actions != nil {
		if run := b.actions.liveRun(sessionID); run != nil && run.stdin != nil {
			run.writeMu.Lock()
			defer run.writeMu.Unlock()
			_, err := fmt.Fprintln(run.stdin, line)
			return err
		}
	}
	b.mu.Lock()
	stdin := b.stdin
	b.mu.Unlock()
	if stdin == nil {
		return errors.New("这条会话没有正在运行的子进程（没有 stdin）")
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, err := fmt.Fprintln(stdin, line)
	return err
}

// ---------------------------------------------------------------------------
// 子进程半边（dshd run / dshd exec）
// ---------------------------------------------------------------------------

// terminalBridgeClient 是本进程当前的客户端，给 stdin 分发器投应答用。
//
// 为什么是进程级一个：一个 `dshd run` 只有一条 stdin、一个当前轮、一个会话。
// 用原子指针（而不是普通变量）是因为分发 goroutine 与装配路径是两个 goroutine。
var terminalBridgeClient atomic.Pointer[terminal.BridgeClient]

// userTerminalsViaStdio 报告父进程有没有开这条通道。
func userTerminalsViaStdio() bool { return os.Getenv(userTerminalsStdioEnv) == "1" }

// terminalSessionOrigin 造"这条会话是不是子代理"的判定闭包。
//
// 事实来源是 `internal/agent.FindChild`——它扫的是子会话日志里的 `subagent/spawn`
// 帧（读整份日志有成本，所以这里按 sessionID 记忆：一个进程里会话来源不会变）。
func terminalSessionOrigin(home string) func(ctx context.Context, sessionID string) (string, error) {
	var mu sync.Mutex
	seen := map[string]string{}
	return func(ctx context.Context, sessionID string) (string, error) {
		// 子代理内部**必须**先判：本宿主的子代理是同一进程里的嵌套 agent.Loop，
		// 沿用父会话 id（见 withSubagentCallContext 的注释），所以"会话 id 是不是
		// 子会话"这一步在子代理内部根本问不出答案。
		if inSubagentCall(ctx) {
			return terminal.PanelOriginSubagent, nil
		}
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			// 空会话由工具层按官方 `require an initiating agent` 处理，这里不猜。
			return "", nil
		}
		mu.Lock()
		if v, ok := seen[sessionID]; ok {
			mu.Unlock()
			return v, nil
		}
		mu.Unlock()

		_, isChild, err := agent.FindChild(home, sessionID)
		if err != nil {
			return "", err
		}
		origin := terminal.PanelOriginUser
		if isChild {
			origin = terminal.PanelOriginSubagent
		}
		mu.Lock()
		seen[sessionID] = origin
		mu.Unlock()
		return origin, nil
	}
}

// terminalBridgeWriter 返回"把一行写给父进程"的写口；没有桥时 nil。
//
// 写 stderr：与审批/jobs 同一个理由（见 approval_stdio.go 的文件头）——Windows 上
// 继承额外 fd 麻烦、stderr 本来就通着、**不能碰 stdout**（serve 那边 stdout 是
// ready 握手契约，工具子进程的 stdout 归模型文本用）。
func terminalBridgeWriter() func([]byte) error {
	if !userTerminalsViaStdio() {
		return nil
	}
	return func(line []byte) error {
		_, err := os.Stderr.Write(line)
		return err
	}
}

// provideUserTerminalBridge 在子进程里挂上工具面需要的数据源。
//
// **没有桥也照样挂**：客户端在没有桥时对 list/read 给 `ErrBridgeUnavailable`，
// 但 `SessionOrigin` 仍是真实判定。这样两种入口的语义都正确：
//
//	serve 起的子进程  → 读到真 roster
//	独立 run/exec     → "这里读不到终端（要 serve）"，而不是假装没有终端
//	子代理会话        → 官方原文拒绝（与有没有桥无关）
func provideUserTerminalBridge(root *kernel.Scope, home string) error {
	if root == nil {
		return errors.New("内核作用域为 nil，终端数据源无处挂")
	}
	client := terminal.NewBridgeClient(terminalBridgeWriter(), terminalSessionOrigin(home))
	terminalBridgeClient.Store(client)
	if err := kernel.Provide[terminal.UserTerminals](root, terminal.UserTerminalsKey, client); err != nil {
		return fmt.Errorf("终端数据源注册失败: %w", err)
	}
	return nil
}

// deliverUserTerminalBridgeResponse 把父进程写来的一行交回等待中的调用
// （由 approval_stdio.go 的 stdin 分发器调用）。
func deliverUserTerminalBridgeResponse(line string) bool {
	client := terminalBridgeClient.Load()
	if client == nil {
		return false
	}
	return client.Deliver(line)
}

// provideUserTerminalSource 是**同进程形态**的接线：终端控制器与本进程在同一个
// kernel 里（测试夹具 / 将来的 in-process 组合）。
//
// 与 serve 形态共用同一个适配器（`transport.NewUserTerminalSource`）和同一个
// origin 判定，所以两种形态读到的 roster 与拒绝语义逐字节相同——这正是
// "同一份真值"能成立的原因。
func provideUserTerminalSource(root *kernel.Scope, jt transport.UserTerminalRoster, home string) error {
	if root == nil {
		return errors.New("内核作用域为 nil，终端数据源无处挂")
	}
	if jt == nil {
		return errors.New("终端服务为 nil，同进程形态没有数据来源")
	}
	if err := kernel.Provide[terminal.UserTerminals](root, terminal.UserTerminalsKey,
		transport.NewUserTerminalSource(jt, terminalSessionOrigin(home))); err != nil {
		return fmt.Errorf("终端数据源注册失败: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 子代理上下文标记（官方 `agent.session.header.origin === 'subagent'` 的等价物）
// ---------------------------------------------------------------------------

// subagentCtxKey 是"这次工具调用发生在子代理内部"的 ctx 标记。
type subagentCtxKey struct{}

// withSubagentCallContext 由 `subRunner.RunSub` 在自己的嵌套 agent 循环前打上。
//
// # 为什么必须有它
//
// 官方判据是**会话头**：子代理有自己的会话、`origin === 'subagent'`
// （`terminal-panel/src/tools.ts:35`）。本宿主的子代理不是新会话：`RunSub` 在
// 同一进程里起一个嵌套 `agent.Loop`，而且**不设** `Loop.SessionID`——工具拿到
// 的会话身份与主代理完全一样。于是"我在不在子代理里"这件事，除了"谁发起的"
// 这一层，工具侧没有任何事实可查。
//
// 真机证据（E8 首轮验收）：没有这个标记时，子代理里的 `user_terminal_list`
// 读到了**主会话**的终端 roster（`t1 · bash.exe · …`），也就是官方明确禁止的
// "子代理读用户的终端"。有了它，同一次调用按官方原文拒绝。
//
// 另一个来源仍然保留：`agent.FindChild`（真 v4 `subagent/spawn` 帧）。它覆盖
// 另一种形态——独立进程直接跑一条子会话（`dshd run --session <child-id>`）时，
// 进程本身就是那条子会话，ctx 里没有"我在子代理里"的标记，只有盘上的谱系。
func withSubagentCallContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, subagentCtxKey{}, true)
}

// inSubagentCall 报告 ctx 是不是子代理内部。
func inSubagentCall(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(subagentCtxKey{}).(bool)
	return v
}
