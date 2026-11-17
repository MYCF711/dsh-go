package transport

// agent 终端**交互**（send / read / signal / close / list）的宿主服务面适配。
//
// # 这不是方法面
//
// 官方这五个工具属于 `@deepseek-ai/dsh-tool-terminal`，消费的是进程内的
// `ctx.terminals`（`dsh-terminal/src/index.ts:224-293` 的 startSend / read / signal /
// kill / list），**没有**同名 wire 方法——rc.2 的 `terminal/*` 方法集合仍是
// create / attach / list / watch / close / environment / shells / rename / retain。
// 所以本文件不加路由、不加方法名、不进 `Handles`，只把
// `OfficialJobTerminal`（真 ConPTY roster）适配成工具层要的
// `terminal.AgentTerminalControls`。
//
// # 为什么还要一层 UserTerminalSource 转发
//
// 工具跑在 `dshd run` 子进程里（经 `internal/terminal/bridge.go` 的五个新 op），
// 也支持同进程形态。两种形态共用同一个数据源对象（`UserTerminalSource`）：
//
//	同进程：UserTerminalSource → OfficialJobTerminal（本文件的 AgentTerminal*(...)）
//	子进程：BridgeClient → 父进程 BridgeServer → UserTerminalSource → 同上
//
// 所以"数据从哪来"只有一份实现，桥只是通道。
//
// # 未接线就响亮失败
//
// roster 没实现 `AgentTerminalInteractions` 时，五个方法一律回
// `ErrJTOfficialUnsupported` 并说清缺的是什么能力——绝不返回空列表、假
// `delivered:true` 或假"已关闭"。这与 `OpenAgentTerminal` 的未接线行为同源
// （`handlers_job_terminal_agent.go` 的文件头）。

import (
	"context"
	"errors"
	"fmt"

	"dshgo/internal/terminal"
)

// AgentTerminalInteractions 是 agent 终端交互需要的宿主服务面子集。
//
// 参数与返回都用 `terminal` 包的类型（本包已 import 它，没有环）：桥的两侧、
// 同进程形态、真实现三方共用同一套语义，避免"再翻译一次"漂移。
type AgentTerminalInteractions interface {
	// AgentTerminalList 列这条会话里 agent 开出来的终端。
	AgentTerminalList(sessionID string) ([]terminal.AgentTerminalSnapshot, error)
	// AgentTerminalSend 写一次输入并等到就绪/静默/超时/退出。
	AgentTerminalSend(ctx context.Context, sessionID, id string, req terminal.AgentSendRequest) (terminal.AgentSendResult, error)
	// AgentTerminalRead 回一页有界历史输出。
	AgentTerminalRead(ctx context.Context, sessionID, id string, req terminal.AgentReadRequest) (terminal.ScreenPage, error)
	// AgentTerminalSignal 把信号投给前台进程组，返回真实收到的进程组 id。
	AgentTerminalSignal(ctx context.Context, sessionID, id, signal string) (int, error)
	// AgentTerminalClose 关闭一条 agent 终端；false = 同一个关闭已在飞。
	AgentTerminalClose(ctx context.Context, sessionID, id string) (bool, error)
}

// 编译期钉住：数据源必须同时满足工具层的两个接口（只读 + 交互）。
var (
	_ terminal.UserTerminals         = (*UserTerminalSource)(nil)
	_ terminal.AgentTerminalControls = (*UserTerminalSource)(nil)
)

// interactions 取 roster 的交互能力；没有就说清缺什么。
func (s *UserTerminalSource) interactions() (AgentTerminalInteractions, error) {
	if s == nil || s.roster == nil {
		return nil, errors.New("终端数据源没有 roster（接线缺失）")
	}
	controls, ok := s.roster.(AgentTerminalInteractions)
	if !ok {
		return nil, fmt.Errorf(
			"%w: 这个宿主的终端服务面没有 agent 终端交互（send/read/signal/close/list，"+
				"对应官方 ctx.terminals 的 startSend/read/signal/kill/list）", ErrJTOfficialUnsupported)
	}
	return controls, nil
}

// AgentTerminalList 是官方 `ctx.terminals.list(owner)` 的宿主入口。
func (s *UserTerminalSource) AgentTerminalList(_ context.Context, sessionID string) ([]terminal.AgentTerminalSnapshot, error) {
	controls, err := s.interactions()
	if err != nil {
		return nil, err
	}
	return controls.AgentTerminalList(sessionID)
}

// AgentTerminalSend 是官方 `ctx.terminals.startSend(owner, id, request)` 的宿主入口。
func (s *UserTerminalSource) AgentTerminalSend(ctx context.Context, sessionID, id string, req terminal.AgentSendRequest) (terminal.AgentSendResult, error) {
	controls, err := s.interactions()
	if err != nil {
		return terminal.AgentSendResult{}, err
	}
	return controls.AgentTerminalSend(ctx, sessionID, id, req)
}

// AgentTerminalRead 是官方 `ctx.terminals.read(owner, id, request)` 的宿主入口。
func (s *UserTerminalSource) AgentTerminalRead(ctx context.Context, sessionID, id string, req terminal.AgentReadRequest) (terminal.ScreenPage, error) {
	controls, err := s.interactions()
	if err != nil {
		return terminal.ScreenPage{}, err
	}
	return controls.AgentTerminalRead(ctx, sessionID, id, req)
}

// AgentTerminalSignal 是官方 `ctx.terminals.signal(owner, id, signal)` 的宿主入口。
func (s *UserTerminalSource) AgentTerminalSignal(ctx context.Context, sessionID, id, signal string) (int, error) {
	controls, err := s.interactions()
	if err != nil {
		return 0, err
	}
	return controls.AgentTerminalSignal(ctx, sessionID, id, signal)
}

// AgentTerminalClose 是官方 `ctx.terminals.kill(owner, id, reason)` 的宿主入口。
func (s *UserTerminalSource) AgentTerminalClose(ctx context.Context, sessionID, id string) (bool, error) {
	controls, err := s.interactions()
	if err != nil {
		return false, err
	}
	return controls.AgentTerminalClose(ctx, sessionID, id)
}
