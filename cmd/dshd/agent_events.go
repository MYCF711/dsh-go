package main

// agent/* 生命周期帧在宿主侧的**唯一落点**。
//
// # 缺口（这个文件要补的那一格）
//
// `internal/agent.Registry` 自己会产出五条边（`agent/created` / `agent/status` /
// `agent/disposed` / `subagent/start` / `subagent/end`，字段名逐条对齐官方），
// 但它的 `RegistryOptions.Emit` 是唯一出口，而生产里唯一的注册表
// （`serve.go` 的 `newGoalRoundDriver`）传的是**零值** `RegistryOptions{}`：
//
//	Emit == nil → 一条边都不发 → 界面永远拿不到 agent 生命周期
//	（`internal/session/known_events.go` 里那份 v4 词汇表解决的是**读**的准入，
//	 不是发射；它登记的 `agent/inbox/spliced` 本宿主至今没有写入点。）
//
// # 官方口径（这一层为什么这么发）
//
// 官方 rc.2 里 `agent/*` 分两类，不是一个平面：
//
//  1. **进程内 cordis 事件**，官方明确**永不持久化**
//     （`dsh-agent/src/runtime-types.ts:291-430`）：
//     `agent/created` `{agent, source, signal?}`(serial)、`agent/disposed` `{agent}`、
//     `agent/status` `{agent, status}`、`agent/inbox/inserted|claimed|discarded`、
//     `agent/pre-step|request|request-error`（带 `next` 的 waterfall）、
//     `agent/assistant-stream`、`agent/turn-stopping`、`agent/error`。共 12 条。
//     消费方全在进程内：`dsh-acp`（`agent/status`/`agent/inbox/claimed`/`agent/error`）、
//     `dsh-action-cards`（`agent/disposed`）、`dsh-agent-team`（`agent/created`）。
//  2. **唯一一条耐久 v4 会话事件**：`agent/inbox/spliced`
//     `{target, start, removedCount?, inserted, outcome?}`
//     （`dsh-agent/src/types.ts:110`，由 `dsh-agent-loop` 的 `session.append` 写盘）。
//
// 子代理那三条（`dsh-subagent`）同样是进程内边：
// `subagent/start` / `subagent/end` 的负载就是官方的 `SubagentRunInfo` /
// `SubagentRunEndInfo`（`dsh-subagent/src/types.ts:95-124`），`subagent/provider-removed` 带字符串。
//
// 宿主把第 1 类**投影进每会话 Transport journal**，理由有三条，都不是"凑一个出口"：
//
//   - 官方的进程内监听方在本宿主里没有对应物（我们没有 cordis 运行时），
//     而真实消费方是界面——它读的就是 Transport journal（`/ws` 与 `session.history`
//     同一个 seq 域，`docs/transport-contract.md` §2）；
//   - serve 的每一轮都是 `dshd run` **子进程**跑的，v4 写锁在子进程手里，
//     父进程里造出来的 agent 状态根本没有 v4 写入端（硬写会变成第二个写者）；
//   - 傍边的既成做法就是这条：`turn/done`、`turn/error`、`goal/change`、`session/title`
//     都是"父进程产生的帧 → journal"，agent 生命周期与它们同类。
//
// # 与官方的两处显式差异（写在这里，不埋在报告里）
//
//   - 官方 `agent/created` 的 `source`（`SessionStartSource`：startup/resume/clear/compact）
//     在注册表这一层**拿不到**：宿主没有"进入会话"的生命周期钩子，只有"这条会话
//     在本进程里第一次跑轮"。编一个 `startup` 就是假数据，所以这个字段**省略**，
//     不填。`signal` 同理（本层没有 factory 取消信号）。
//   - 官方 `agent` 句柄的公开面只有 `{id}`（`dsh-agent/src/types.ts:16-19`），运行时另带
//     `session`。宿主真值里还有父子与深度，所以 `agent` 对象给出 `id` +
//     `parentSessionId` + `depth` + `status`；多出来的三个键是宿主**自己的真值**，
//     不是对官方字段的改写。

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"dshgo/internal/agent"
	"dshgo/internal/transport"
)

// agentEventPub 是 WS 推送出口的**晚绑定**壳。
//
// 注册表必须在 `transport.NewServer` **之前**造好（goal-round 驱动包在 runner 外层，
// 而 runner 是 `NewServer` 的入参），而 `srv.Publish` 只有 `NewServer` 返回之后才有。
// 所以壳先建、`set` 在 serve 开始处理任何请求之前调一次，`emit` 之后再读。
type agentEventPub struct {
	mu sync.RWMutex
	fn func(topic string, frame transport.ServerFrame)
}

func (p *agentEventPub) set(fn func(topic string, frame transport.ServerFrame)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.fn = fn
	p.mu.Unlock()
}

func (p *agentEventPub) publish(topic string, frame transport.ServerFrame) {
	if p == nil {
		return
	}
	p.mu.RLock()
	fn := p.fn
	p.mu.RUnlock()
	if fn != nil {
		fn(topic, frame)
	}
}

// agentEventSink 把注册表的边落成**每会话** journal 帧。
//
// 注册表在**持锁状态下同步**调 `emit`（`internal/agent/registry.go` 的并发约定），
// 所以这里必须一直跑到把帧交给 journal 为止——顺序就是状态迁移的顺序。
// 写失败**不回吞**：stderr 一行，且不假装入账（与 `emitFrame` 同口径）。
type agentEventSink struct {
	journals *transport.Journals
	pub      *agentEventPub
}

// Emit 是 `agent.RegistryOptions.Emit` 的生产实现。
func (s agentEventSink) Emit(ev agent.AgentEvent) {
	payload, err := agentFramePayload(ev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent 生命周期帧投影失败（kind=%s session=%s）：%v\n", ev.Kind, ev.SessionID, err)
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent 生命周期帧序列化失败（kind=%s session=%s）：%v\n", ev.Kind, ev.SessionID, err)
		return
	}
	if _, err := emitFrameAtResult(s.journals, s.pub.publish, ev.SessionID, ev.Kind, data, ev.At); err != nil {
		// 不入账就**不推**：给 WS 一个重启后会消失的假帧，比少一帧更难查。
		fmt.Fprintf(os.Stderr, "agent 生命周期帧入账失败（kind=%s session=%s）：%v\n", ev.Kind, ev.SessionID, err)
	}
}

// agentFramePayload 把一条内部边投影成官方字段名的载荷。
//
// 纯函数：帧里有什么、字段叫什么，全部由它一处决定，测试直接钉它。
func agentFramePayload(ev agent.AgentEvent) (any, error) {
	switch ev.Kind {
	case agent.EventAgentCreated:
		return agentCreatedFrame{Agent: agentHandle(ev)}, nil
	case agent.EventAgentStatus:
		return agentStatusFrame{Agent: agentHandle(ev), Status: string(ev.Status)}, nil
	case agent.EventAgentDisposed:
		return agentDisposedFrame{Agent: agentHandle(ev)}, nil
	case agent.EventSubagentStart:
		if ev.Subagent == nil {
			return nil, fmt.Errorf("subagent/start 边没有委派地址")
		}
		return ev.Subagent.SubagentRun, nil
	case agent.EventSubagentEnd:
		if ev.Subagent == nil {
			return nil, fmt.Errorf("subagent/end 边没有委派地址")
		}
		return subagentEndFrame{
			SubagentRun:          ev.Subagent.SubagentRun,
			StopReason:           ev.Subagent.StopReason,
			LastAssistantMessage: lastAssistantBlocks(ev.Subagent.LastAssistantMessage),
		}, nil
	default:
		// 认不出的边**响亮失败**：静默丢一条生命周期边，读方看到的是"这个 agent 一直活着"。
		return nil, fmt.Errorf("不认识的生命周期边 %q", ev.Kind)
	}
}

// agentHandle 是官方载荷里 `agent` 那个对象。
type agentHandleObject struct {
	// ID 是官方 `Agent` 的公开身份（`dsh-agent/src/types.ts:18`：`readonly id: SessionId`）。
	ID string `json:"id"`
	// ParentID/Depth 是宿主的寻址真值（官方 `resolveChildDepth` 的落点）。
	ParentID string `json:"parentSessionId,omitempty"`
	Depth    int    `json:"depth"`
	// Status 只在 `agent/status` / `agent/created` 上有值。
	Status string `json:"status,omitempty"`
}

func agentHandle(ev agent.AgentEvent) agentHandleObject {
	return agentHandleObject{
		ID:       ev.SessionID,
		ParentID: ev.ParentID,
		Depth:    ev.Depth,
		Status:   string(ev.Status),
	}
}

// 五条边的载荷形状。字段名 = 官方字段名，别改（读方按名字取）。
type (
	agentCreatedFrame struct {
		Agent agentHandleObject `json:"agent"`
	}
	agentStatusFrame struct {
		Agent  agentHandleObject `json:"agent"`
		Status string            `json:"status"`
	}
	agentDisposedFrame struct {
		Agent agentHandleObject `json:"agent"`
	}
	// subagentEndFrame 内嵌官方的 `SubagentRunInfo`（runId/provider/parentSessionId/id/local），
	// 再加 `SubagentRunEndInfo` 多出来的两个字段。
	//
	// `lastAssistantMessage` 必须是**官方的类型**（`readonly ContentBlock[]`），
	// 不是裸字符串：往一个数组字段里塞 string，读方 `[...info.lastAssistantMessage]`
	// 会直接炸或读成空数组——比省略这个字段更糟。注册表这一层只持有文本，
	// 所以这里把它包成**一个 text 块**，且空串时省略（官方同样省略空值）。
	subagentEndFrame struct {
		agent.SubagentRun
		StopReason           string                   `json:"stopReason"`
		LastAssistantMessage []transport.ContentBlock `json:"lastAssistantMessage,omitempty"`
	}
)

// lastAssistantBlocks 把注册表持有的文本包成官方 ContentBlock 数组。
//
// 没有文本 → nil（omitempty 后整个键不出现），与官方"没有末条输出就不发这个字段"一致。
// 这里**不**用文本有没有内容去决定要不要发这一帧：帧的存废由 `subagent/end`
// 这条边自己决定（stopReason 才是它的主体），文本只是可选附件。
func lastAssistantBlocks(lastText string) []transport.ContentBlock {
	var blocks []transport.ContentBlock
	if lastText != "" {
		blocks = append(blocks, transport.ContentBlock{Type: "text", Text: lastText})
	}
	return blocks
}

// ── 真实装配点用的两个小工具 ────────────────────────────────────────────

// ensureAgentRegistered 确保一条**真实存在**的会话在活体注册表里。
//
// 幂等（已登记直接返回）；父子归属只认盘上的 durable `subagent/spawn` 记录，
// 不从调用方参数推——推出来的父子关系会让 `subagents.*` 的 403 判据失真。
//
// 为什么需要"先 Create 再 Start"：`Registry.Start` 对未知会话的语义是
// "自动登记 + 只发一条 status 边"（`TestAgentRegistryStartAutoRegisters` 钉着），
// 那条边回答不了"这个 agent 是什么时候出现的"。真实装配点上先 `Create` 一次，
// `agent/created` 才有真实发射点——否则界面上永远没有"创建"这一格。
func ensureAgentRegistered(reg *agent.Registry, home, sessionID string) error {
	return ensureAgentRegisteredAtDepth(reg, home, sessionID, 0)
}

// maxAgentChainDepth 是向上追父子链的层数上限。
//
// 真实的委派深度本宿主只有 1 层（`cmd/dshd/subagent.go` 的 `maxDepth`），官方也由
// provider 的 `depthLimit` 收口；给到 32 是**给坏数据兜底**：`subagent/spawn` 记录
// 万一被写成环，没有这道上限就是无限递归（栈爆 = 整个宿主倒，不是一行错误）。
const maxAgentChainDepth = 32

func ensureAgentRegisteredAtDepth(reg *agent.Registry, home, sessionID string, depth int) error {
	if reg == nil || sessionID == "" {
		return nil
	}
	if _, ok := reg.Get(sessionID); ok {
		return nil
	}
	if depth > maxAgentChainDepth {
		return fmt.Errorf("会话 %q 的父子链超过 %d 层：durable `subagent/spawn` 记录可疑（要么是环，要么被写坏）"+
			"——不编父子关系，交给调用方响亮报错", sessionID, maxAgentChainDepth)
	}
	parent := ""
	if rec, found, err := agent.FindChild(home, sessionID); err != nil {
		// 认不出归属（同名会话散在多个项目目录）**不吞**：吞掉就等于把这个 child
		// 登记成根 agent，`subagents.*` 的父校验会跟着错。
		return fmt.Errorf("认会话 %q 的父子归属失败: %w", sessionID, err)
	} else if found {
		parent = rec.ParentSessionID
		if err := ensureAgentRegisteredAtDepth(reg, home, parent, depth+1); err != nil {
			return err
		}
	}
	if _, err := reg.Create(sessionID, parent); err != nil {
		return fmt.Errorf("登记 agent（session=%s）：%w", sessionID, err)
	}
	return nil
}

// subagentStopReasonFromTurnEnd 把子会话日志里最后一条 `turn/end` 的 `reason.kind`
// 映射成官方五个 stopReason 之一（`dsh-subagent/src/lifecycle.ts:80-105` 的 switch）。
//
// 逐条对齐（kind → stopReason）：
//
//	max-tokens            → max-tokens
//	aborted / interrupted → aborted
//	error                 → error
//	blocked               → refusal   （pre-step 拒绝 = 工作被拒，不是做完了）
//	completed / 空        → completed
//	其它（含新增变体）    → error     （官方 default 分支原话：把说不出名字的原因当成功
//	                                  是最坏的一种谎）
//
// `found == false`（这条子会话日志里**根本没有**闭合的 turn）返回 `aborted`：
// 官方用 `droppedUnrun` 区分"干净收工"与"被丢弃的未跑输入"，宿主在这一层看不到那份
// 记账，所以只能选**不会把没干完的活报成完成**的那一个。
func subagentStopReasonFromTurnEnd(kind string, found bool) string {
	if !found {
		return agent.SubagentStopAborted
	}
	switch kind {
	case "max-tokens":
		return agent.SubagentStopMaxTokens
	case "aborted", "interrupted":
		return agent.SubagentStopAborted
	case "error":
		return agent.SubagentStopError
	case "blocked":
		return agent.SubagentStopRefusal
	case "", "completed":
		return agent.SubagentStopCompleted
	default:
		return agent.SubagentStopError
	}
}
