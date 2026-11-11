// 子代理：把一件独立的事交给另一个 agent 跑完，只把结果拿回来。
//
// 与上游的差别（要说清楚，别装作一样）：上游是**异步**的——立刻返回一个 id，
// 子代理跑完再通过 send_message 回传。我们这里是**同步**的：调用一直等到子代理
// 收尾，直接返回正文。原因：异步需要一条"宿主主动通知客户端"的通道，
// 我们现在的 ACP 实现里没有这条通道；做半套异步反而更难查。
//
// 这个差别写在 docs/tool-parity.md 里。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dshgo/internal/agent"
	"dshgo/internal/kernel"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// subRunner 是 spi.SubagentRunner 的实现。
type subRunner struct {
	chat     agent.Chat
	tools    spi.ToolRegistry
	runner   spi.Runner
	model    string
	maxSteps int
	timeout  time.Duration
	// temperature / stop 是官方每请求采样面（llm.temperature / llm.stop）。
	// 子代理与主代理共享同一份进程级配置：它们打同一个网关，采样面漂移会让
	// "同一次委派"出现两种温度，A/B 就没法比。
	temperature *float64
	stop        []string
	// home 是 durable 子会话的落点（<home>/sessions/...）。
	// 空 = 这次装配不提供可续问（req.Continuable 会**响亮失败**，
	// 而不是回一个指向空目录的 child id）。
	home string
	// depth 是当前嵌套深度：0 表示主代理。子代理内部再调 subagent 会被拒。
	depth int
	// gate 是并发池（官方 subagent.maxActiveSubagents，默认 8）。
	// nil = 不闸门（测试与旧装配的零影响路径）。
	gate *subagentGate
}

const maxDepth = 1

// subCwdMu 让"临时切目录 + 跑子代理"这段临界区串行。
var subCwdMu sync.Mutex

func (s *subRunner) RunSub(ctx context.Context, req spi.SubagentRequest) (spi.SubagentResult, error) {
	if s.depth >= maxDepth {
		return spi.SubagentResult{}, fmt.Errorf("子代理不能再派子代理")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return spi.SubagentResult{}, fmt.Errorf("prompt 为空")
	}

	// 官方 subagent.maxActiveSubagents：真正在跑的孩子的上限（默认 8）。
	// 拿不到槽就**等**，拿不到且 ctx 被取消就当场收工——不先把活全跑起来。
	if release, err := s.gate.acquire(ctx); err != nil {
		return spi.SubagentResult{}, fmt.Errorf("等子代理并发额度时被取消: %w", err)
	} else {
		defer release()
	}

	// 具名委派（官方 `agentLoop.agents[].id`）：先认地址再干活。
	//
	// 为什么先认：同一个 (父会话, agent id) 已经有一条子会话时，这一趟要**续在
	// 同一条**上（历史连得上、subagents.prompt 的地址不变），而不是再开一条。
	// 地址只能从盘上认——serve 每一轮都是新的 `dshd run` 子进程。
	agentID := strings.TrimSpace(req.AgentID)
	var agentParent, reuseChild string
	if agentID != "" {
		agentParent = strings.TrimSpace(req.ParentSessionID)
		if agentParent == "" {
			agentParent = strings.TrimSpace(spi.SessionFrom(ctx))
		}
		if agentParent == "" {
			return spi.SubagentResult{}, fmt.Errorf(
				"具名委派需要父会话 id：ctx 上没有会话身份，也没有显式 ParentSessionID")
		}
		child, ok, err := agent.FindAgentChild(s.home, agentParent, agentID)
		if err != nil {
			return spi.SubagentResult{}, err
		}
		if ok {
			// 索引是缓存、会话才是事实：盘上不在了就重新 spawn 一条，
			// 而不是往一个不存在的地址上写。
			if _, ferr := session.FindByID(s.home, child); ferr == nil {
				reuseChild = child
			}
		}
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// 子代理的工作目录：req.Cwd 相对当前目录解析。
	//
	// 为什么用进程级 chdir：工具表里的 read/write/glob/grep 都是按**进程**
	// cwd 解析相对路径的，没有"每个 agent 一份 cwd"的机制。所以这里临时切过去、
	// 跑完切回来，并用一把锁把并发子代理串起来——两个子代理同时换 cwd 会互相打架。
	//
	// 局限（写进 docs/tool-parity.md）：同一进程里两个**会话**并发时，
	// 其中一个的 chdir 会影响另一个。要根治得让每个 agent 带自己的 cwd。
	if wd := strings.TrimSpace(req.Cwd); wd != "" {
		abs, err := filepath.Abs(wd)
		if err != nil {
			return spi.SubagentResult{}, fmt.Errorf("cwd 解析失败: %w", err)
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return spi.SubagentResult{}, fmt.Errorf("cwd 不是目录: %s", req.Cwd)
		}
		subCwdMu.Lock()
		defer subCwdMu.Unlock()
		prev, err := os.Getwd()
		if err != nil {
			return spi.SubagentResult{}, err
		}
		if err := os.Chdir(abs); err != nil {
			return spi.SubagentResult{}, fmt.Errorf("切到 %s 失败: %w", req.Cwd, err)
		}
		defer os.Chdir(prev)
	}

	// 子代理的工具表里去掉 subagent 自己——递归在这里被**结构上**挡住，
	// 不只靠 depth 判断。
	reg := noSubagent{inner: s.tools}

	loop := &agent.Loop{
		Chat:   s.chat,
		Tools:  reg,
		Runner: s.runner,
		// 投影必须和主代理**用同一个**。少了它，工具结果只剩 Summary，
		// 子代理看不到正文 → 以为没读到 → 一遍遍重试，最后撞满步数。
		Project:  session.ProjectResult,
		Model:    s.model,
		System:   subSystemPrompt,
		MaxSteps: s.maxSteps,
		// 采样面沿用父进程的显式配置（nil/空 = 不发，与主代理一致）。
		Temperature: s.temperature,
		Stop:        s.stop,
	}

	// 继承父会话的最近若干条：子代理要的是"刚才在聊什么"，
	// 不是"从头再来一遍"——所以有上限，不做全量。
	if inh := strings.TrimSpace(req.Inherit); inh != "" {
		prompt = "以下是父会话最近说过的话（供参考，不要照抄）：\n" + inh + "\n\n当前任务：" + prompt

	}

	// 子代理内部标记：官方靠"子代理有自己的会话 + origin=subagent"判定，
	// 本宿主的嵌套循环沿用父会话 id，只能由发起方把这件事标出来
	// （`user_terminal_*` 等"只许主代理用"的工具靠它拒绝，见 terminal_bridge.go）。
	hist, err := loop.Run(withSubagentCallContext(ctx), 1, []llm.Message{{Role: "user", Content: prompt}})
	if err != nil {
		return spi.SubagentResult{}, err
	}

	// 取最后一条助手正文。中间步骤一概不回——这正是子代理省上下文的意义。
	var text string
	for _, m := range hist {
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			text = m.Content
		}
	}
	if strings.TrimSpace(text) == "" {
		return spi.SubagentResult{Steps: len(hist)}, fmt.Errorf("子代理没有产出正文")
	}
	res := spi.SubagentResult{
		Text:  text,
		Steps: len(hist),
		// 说清楚这次用的是谁的授权：runner 是主会话那个 guarded，
		// 审批记忆按会话记——主会话批准过的工具，子代理不会重问。
		BorrowedApprovals: s.runner != nil,
	}
	// 可续问：把这一趟真实往返落成一条 durable 子会话，官方 `subagents.prompt`
	// 才有地址可投。**放在成功之后**——委派失败不留孤儿会话（一个空会话既不能
	// 续问也没人清理）。
	if reuseChild != "" {
		// 复用：这一趟往返**追加**到同一条子会话（不新建，v4 重建等于抹历史）。
		if err := agent.AppendChildTurn(s.home, "", reuseChild, prompt, text); err != nil {
			return spi.SubagentResult{}, fmt.Errorf("委派已完成但复用子会话没写成: %w", err)
		}
		res.ChildSessionID = reuseChild
	} else if req.Continuable || agentID != "" || strings.TrimSpace(req.ChildSessionID) != "" {
		childID, err := s.continuation(ctx, req, prompt, text, agentID)
		if err != nil {
			// 响亮失败：这次委派的结果照实交回去，但"可续问"这件事没成，
			// 绝不能回一个编出来的 child id。
			return spi.SubagentResult{}, fmt.Errorf("委派已完成但可续问子会话没建成: %w", err)
		}
		// 具名委派：把地址记进盘，下一轮（另一个进程）按同一个名字还认得到。
		if agentID != "" {
			if err := agent.RecordAgentChild(s.home, agentParent, agentID, childID); err != nil {
				return spi.SubagentResult{}, fmt.Errorf("委派已完成但具名地址没记下来: %w", err)
			}
		}
		res.ChildSessionID = childID
	}
	return res, nil
}

// continuation 是**真实 spawn** 的落点：建 durable 子会话 + 父子帧 + 委派往返。
//
// 为什么父会话 id 从 ctx 取：`dshd serve` 每一轮都是 `dshd run --session <id>`
// 子进程，Loop 把会话身份绑在每一步的 ctx 上（agent.Loop.SessionID →
// spi.WithSession），所以工具这条链上 ctx 里本来就有"我在为哪个会话干活"。
// 显式 ParentSessionID 优先（调用方可能替别的会话派活）。
func (s *subRunner) continuation(ctx context.Context, req spi.SubagentRequest, prompt, answer, agentID string) (string, error) {
	parentID := strings.TrimSpace(req.ParentSessionID)
	if parentID == "" {
		parentID = strings.TrimSpace(spi.SessionFrom(ctx))
	}
	if parentID == "" {
		return "", fmt.Errorf("可续问委派需要父会话 id：ctx 上没有会话身份，也没有显式 ParentSessionID")
	}
	home := strings.TrimSpace(s.home)
	if home == "" {
		return "", fmt.Errorf("这次装配没有 home：可续问子会话落不了盘")
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	rec, err := (agent.ChildSpawn{
		Home:            home,
		ParentSessionID: parentID,
		Cwd:             cwd,
	}).Spawn(agent.ChildSpawnOptions{
		ChildSessionID: strings.TrimSpace(req.ChildSessionID),
		// 具名委派：宿主按官方规则派生 `${id}-session-<uuid>`
		// （显式 ChildSessionID 仍然优先，对应官方的 sessionId）。
		AgentID: agentID,
		Prompt:  prompt,
		Answer:  answer,
	})
	if err != nil {
		return "", err
	}
	return rec.ChildSessionID, nil
}

const subSystemPrompt = `你是一个子代理。你只负责交给你的那件事，做完就给结论。
你看不到派你来的那个会话的历史，所以任务描述里的信息就是全部信息。
- 需要查证就用工具。
- 不要复述过程，直接给结果。
- 做不到就直说做不到，并说清缺什么。`

// noSubagent 过滤掉工具表里的 subagent，避免递归。
type noSubagent struct{ inner spi.ToolRegistry }

func (n noSubagent) Tools() []spi.Tool {
	all := n.inner.Tools()
	out := make([]spi.Tool, 0, len(all))
	for _, t := range all {
		if t.Name() == "subagent" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (n noSubagent) Get(name string) (spi.Tool, bool) {
	if name == "subagent" {
		return nil, false
	}
	return n.inner.Get(name)
}

// Register 永不改写底层表：子代理只能看，不能往宿主的工具表里加东西。
func (n noSubagent) Register(spi.Tool) error {
	return fmt.Errorf("子代理不能注册新工具")
}

// subProvider 是"先装配、后绑定"的壳：模块注册时还不知道 chat/工具表/执行器
// （它们在 Assemble 之后才有），所以先放一个空壳进去，装好再 Bind。
type subProvider struct{ impl *subRunner }

func (p *subProvider) RunSub(ctx context.Context, req spi.SubagentRequest) (spi.SubagentResult, error) {
	if p.impl == nil {
		return spi.SubagentResult{}, fmt.Errorf("子代理运行器还没装配好")
	}
	return p.impl.RunSub(ctx, req)
}

func (p *subProvider) Bind(r *subRunner) { p.impl = r }

// subagentModule 把子代理能力提供给工具层。
func subagentModule(p *subProvider) kernel.Module {
	return kernel.Module{
		ID:       "subagent",
		Backend:  kernel.BackendStatic,
		Provides: []string{"subagent"},
		Register: func(s *kernel.Scope) error {
			return kernel.Provide(s, spi.SubagentKey, spi.SubagentRunner(p))
		},
	}
}
