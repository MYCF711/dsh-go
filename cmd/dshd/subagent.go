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
	"encoding/json"
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
	// 同步委派没有两段式面的附加参数（结构化输出只属于 workflow 的 agent()）。
	return s.runSub(ctx, req, spi.ActivationOptions{})
}

// runSub 是 RunSub 与 StartActivation 的**唯一**实现：两条入口的并发闸门、
// 父子会话归属、审批借用必须完全一致，差别只在 options。
func (s *subRunner) runSub(ctx context.Context, req spi.SubagentRequest, options spi.ActivationOptions) (spi.SubagentResult, error) {
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
		// Events 故意留空：子代理的子会话在**委派结束之后**才物化
		// （continuation 里才 CreateSession），执行期间没有可写的会话日志。
		// 硬造一个写入端会写出两段互不相认的 seq；正确的做法是等子会话造好之后
		// 再回放，那是独立的活儿（见报告"缺口"）。
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

	// 结构化输出（官方 `agent(prompt, {schema})`）：把 schema 明确交给子代理，
	// 并在拿到正文后按它**解析**。
	//
	// 为什么要写进 prompt 而不只是事后解析：不告诉它，模型有一半概率回一段
	// 自然语言；事后解析失败就等于"委派白跑"。指令里点名"不要代码围栏"，
	// 因为围栏是模型最常见的加料（解析端也兜一层，见 extractJSONObject）。
	if options.OutputSchema != nil {
		if schemaJSON, err := json.Marshal(options.OutputSchema); err == nil {
			prompt += "\n\n输出要求：只输出一个符合下面 JSON Schema 的 JSON 对象，" +
				"不要加解释、不要加代码围栏。\n" + string(schemaJSON)
		}
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
	// 要了 schema 就把正文里的 JSON 对象取出来。解析不出来**不报错**：
	// "子代理跑完了但结果不合规"是这次委派失败（引擎解成 null），
	// 不是结果通道坏掉（那才是 AGENT_RESULT）。
	if options.OutputSchema != nil {
		if structured, ok := extractJSONObject(text); ok {
			res.Structured = structured
		}
	}
	// durable 子会话：把这一趟真实往返落成一条 durable 子会话。三个来源：
	//
	//   - **可续问**（`req.Continuable`）：官方 `subagents.prompt` 才有地址可投；
	//   - **具名委派**（`req.AgentID` / 显式 `ChildSessionID`）：地址要跨进程认；
	//   - **一次性同步 fork**（`req.Seed` 非空）：官方
	//     `dsh-subagent-fork-in-process` 的语义是"the child works in its own session"
	//     （README.md "What a fork delegation does"），且种子 "becomes part of the
	//     child's own durable transcript"（`src/index.ts:72-78`）——不留会话就等于
	//     把那份种子丢掉。判据是**种子里有东西**：空种子 = 首轮完成之前的 fork，
	//     官方说它 "behaves like a fresh spawn"（README.md "Seed boundary"），
	//     那就不该凭空多出一条会话。
	//
	// **放在成功之后**——委派失败不留孤儿会话（一个空会话既不能续问也没人清理）。
	forkChild := len(req.Seed) > 0
	if reuseChild != "" {
		// 复用：这一趟往返**追加**到同一条子会话（不新建，v4 重建等于抹历史）。
		if err := agent.AppendChildTurn(s.home, "", reuseChild, prompt, text); err != nil {
			return spi.SubagentResult{}, fmt.Errorf("委派已完成但复用子会话没写成: %w", err)
		}
		res.ChildSessionID = reuseChild
	} else if req.Continuable || forkChild || agentID != "" || strings.TrimSpace(req.ChildSessionID) != "" {
		childID, err := s.continuation(ctx, req, prompt, text, agentID)
		if err != nil {
			// 响亮失败：这次委派的结果照实交回去，但"子会话"这件事没成，
			// 绝不能回一个编出来的 child id，也不能假装 fork 的种子落了盘。
			if forkChild && !req.Continuable {
				return spi.SubagentResult{}, fmt.Errorf(
					"委派已完成但 fork 种子的 durable 子会话没建成（不静默丢种子）: %w", err)
			}
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
	// fork 种子：宿主侧唯一一次解码（spi 只约定"每条是一份 v4 事件的 JSON"）。
	// 解不开就响亮失败——子会话少一段父会话真实说过的话，比"这次委派没成"更难查。
	seedEvents, err := seedEventsFromWire(req.Seed)
	if err != nil {
		return "", fmt.Errorf("fork 种子解不开: %w", err)
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
		// fork 种子：父会话**已完成轮次**的真实事件，逐条落进子会话日志。
		//
		// 官方 `dsh-subagent-fork-in-process/src/index.ts:72-78`：种子在创建时取一次，
		// 之后成为子会话自己的 durable transcript（父会话再长也不会进来）。
		// 取种子与"投影成子代理第一段文本"是同一份事实的两个出口：前者落盘保结构，
		// 后者走现有 `Inherit` 通道喂给这一趟的模型调用（见 `inheritFromCtx`）。
		Seed: seedEvents,
	})
	if err != nil {
		return "", err
	}
	return rec.ChildSessionID, nil
}

// extractJSONObject 从子代理正文里取一个 JSON **对象**。
//
// 三种常见形状都要认：纯 JSON、带 ```json 围栏、以及"先解释一句再给 JSON"。
// 只认对象（schema 是 object-rooted）：数组/标量一律当"没拿到"。
func extractJSONObject(text string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		if newline := strings.IndexByte(trimmed, '\n'); newline >= 0 {
			trimmed = strings.TrimSpace(trimmed[newline+1:])
		}
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "```"))
	}
	candidates := []string{trimmed}
	if start, end := strings.IndexByte(trimmed, '{'), strings.LastIndexByte(trimmed, '}'); start >= 0 && end > start {
		candidates = append(candidates, trimmed[start:end+1])
	}
	for _, candidate := range candidates {
		var value map[string]any
		decoder := json.NewDecoder(strings.NewReader(candidate))
		if err := decoder.Decode(&value); err != nil || value == nil {
			continue
		}
		if raw, err := json.Marshal(value); err == nil {
			return raw, true
		}
	}
	return nil, false
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
//
// 它同时提供两面：同步的 RunSub（`subagent` 工具用）与两段式的
// StartActivation（workflow 的 `agent()` 用）。两面背后是**同一个** subRunner：
// 并发闸门、父子会话归属、审批借用必须完全一致，否则"同一个 agent()"在两条
// 路径上的行为会不一样。
type subProvider struct{ impl *subRunner }

func (p *subProvider) RunSub(ctx context.Context, req spi.SubagentRequest) (spi.SubagentResult, error) {
	if p.impl == nil {
		return spi.SubagentResult{}, fmt.Errorf("子代理运行器还没装配好")
	}
	return p.impl.RunSub(ctx, req)
}

func (p *subProvider) Bind(r *subRunner) { p.impl = r }

// subActivation 是一次"已发布、可等待"的委派（spi.SubagentActivation 的实现）。
//
// 为什么字段都在结构体上而不是闭包：Await 会被并发调用（引擎可能在扇出里
// 等多个 child），而 done 关闭之后读 result/err 是有 happens-before 的
// （写发生在 close 之前），所以不需要额外加锁。
type subActivation struct {
	childID string
	done    chan struct{}
	cancel  context.CancelFunc
	result  spi.SubagentResult
	err     error
}

// ChildSessionID 是发布时就定下的子会话 id（官方 childId）。
func (a *subActivation) ChildSessionID() string { return a.childID }

// Await 等子代理收尾；ctx 取消时立刻返回，不吞掉取消原因。
func (a *subActivation) Await(ctx context.Context) (spi.SubagentResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-a.done:
		return a.result, a.err
	case <-ctx.Done():
		// 取消是**发得出去的**：先停掉真正在跑的那个 child，再返回。
		// 只返回不取消会留下一个没人等的 goroutine（它还会继续吃模型配额）。
		a.cancel()
		return spi.SubagentResult{}, ctx.Err()
	}
}

// StartActivation 发布一个 child 并**立即**返回句柄（job 式两段：先发布再等）。
//
// 与 RunSub 的差别只有时序：RunSub 一直阻塞到结果；这里先返回
// ChildSessionID，让引擎能在子代理**开跑之前**发出 `workflow/agent-start`
// （官方的顺序，见 dsh-workflow-ptc/src/runtime.ts:192-193）。
//
// 三条语义必须成立（否则 childId 就是一个编出来的地址）：
//   - 强制 Continuable：每个 `agent()` 都是一条真实 durable 子会话；
//   - 强制 ChildSessionID：id 在**发布时**就定，子会话按它建；
//   - 父会话 id 取显式值，否则取 ctx 上的会话身份；都没有就响亮失败
//     （没有父就没有授权边界，编一个父等于没有归属）。
func (p *subProvider) StartActivation(ctx context.Context, req spi.SubagentRequest, options spi.ActivationOptions) (spi.SubagentActivation, error) {
	if p.impl == nil {
		return nil, fmt.Errorf("子代理运行器还没装配好")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt 为空")
	}
	if strings.TrimSpace(req.ChildSessionID) == "" {
		req.ChildSessionID = session.NewID()
	}
	req.Continuable = true
	if strings.TrimSpace(req.ParentSessionID) == "" {
		req.ParentSessionID = strings.TrimSpace(spi.SessionFrom(ctx))
	}
	if strings.TrimSpace(req.ParentSessionID) == "" {
		return nil, fmt.Errorf("可续问委派需要父会话 id：ctx 上没有会话身份，也没有显式 ParentSessionID")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	childCtx, cancel := context.WithCancel(ctx)
	activation := &subActivation{
		childID: strings.TrimSpace(req.ChildSessionID),
		done:    make(chan struct{}),
		cancel:  cancel,
	}
	impl := p.impl
	go func() {
		defer close(activation.done)
		defer cancel()
		// panic 是**基础设施**故障（child 已发布但结果不可用）→ 作为 error 抛给
		// 引擎，映射成 AGENT_RESULT。普通失败（模型错误/没产出正文）走
		// SubagentResult.Failed：那是"子代理没成"，工作流把该项解成 null。
		defer func() {
			if recovered := recover(); recovered != nil {
				activation.err = fmt.Errorf("子代理运行器 panic: %v", recovered)
			}
		}()
		result, err := impl.runSub(childCtx, req, options)
		if err != nil {
			if childCtx.Err() != nil {
				activation.err = childCtx.Err()
				return
			}
			activation.result = spi.SubagentResult{Failed: true}
			return
		}
		activation.result = result
	}()
	return activation, nil
}

// subagentModule 把子代理能力提供给工具层。
func subagentModule(p *subProvider) kernel.Module {
	return kernel.Module{
		ID:      "subagent",
		Backend: kernel.BackendStatic,
		// 两面都提供：同步 RunSub（subagent 工具）与两段式 StartActivation
		// （workflow 的 agent()）。分成两个能力名而不是一个接口：用方只需要
		// 自己那面，实现方（本文件）保证两面同源。
		Provides: []string{"subagent", "subagent-activation"},
		Register: func(s *kernel.Scope) error {
			if err := kernel.Provide(s, spi.SubagentKey, spi.SubagentRunner(p)); err != nil {
				return err
			}
			return kernel.Provide(s, spi.SubagentActivationKey, spi.SubagentActivator(p))
		},
	}
}
