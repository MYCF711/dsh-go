package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/acp"
	"dshgo/internal/agent"
	"dshgo/internal/approval"
	"dshgo/internal/compact"
	"dshgo/internal/kernel"
	"dshgo/internal/llm"
	"dshgo/internal/modes"
	"dshgo/internal/prompt"
	"dshgo/internal/sandbox"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// fsSandboxModeSetter 是 FS 的可选能力：把进程级 FS 边界切到会话档位。
// 由 modules/fs/local 实现；ACP 一个进程一个档位，所以切换是安全的。
type fsSandboxModeSetter interface {
	SetSandboxMode(sandbox.Mode) error
}

// fsExtraReadRooter 是 FS 的可选能力：追加一个只读根（additionalDirectories）。
type fsExtraReadRooter interface {
	AddExtraReadRoot(dir string) error
}

// acpRunner 把 ACP 的 prompt 接到真实的 agent 循环与会话日志上。
type acpRunner struct {
	reg spi.ToolRegistry
	// fsRoot 可选：FS 支持换根时，用它把工作区根切到客户端给的 cwd。
	fsRoot spi.RootFollower
	// execCwd 可选：bash 支持换默认目录时，跟着一起换（否则 read 在 B、bash 在 A）。
	execCwd spi.CwdFollower
	// workspace 是这个进程当前服务的工作区（第一个会话定下来，之后不许换）。
	workspace string
	runner    spi.Runner
	base      string
	model     string
	home      string
	steps     int
	// reasoning 是推理强度（空 = 不要），和 run/serve 用同一个开关。
	reasoning string

	// ask 是审批闸门：返回 false 表示用户拒绝这次工具调用。
	ask func(ctx context.Context, sessionID, tool string, args string) (bool, error)
	// ctxWindow 用于 usage_update 的分母。
	ctxWindow int
	// capacity 是这个 runner 固定模型的有效容量；MaxOutput 进入常规请求。
	capacity llm.ModelCapacity
	// system 是装配好的系统提示（会作为事件落盘）。
	system string
	// autoCompact 是自动压缩的 token 阈值（0 = 关闭）。
	autoCompact int
	// keepRecent 是自动压缩时**至少**保留的最近消息数（下限）。
	//
	// 它不再是"保留多少"：真正的保留量由 compact 的 token 预算决定。
	// 踩过：按 8 条留，在一条工具输出就 2,300 token 的会话里顶到 8k 以上，
	// 压了 10 次上下文还是线性涨。
	keepRecent int
	// approvals 记住"这个会话里这个工具已经批准过了"。
	approvals *approval.Memory
	// approvalsStore 非 nil 时，授权跨进程保留（需要 --remember-approvals）。
	approvalsStore *approval.Store
	// modeID 是启动时的默认模式；sessionModes 记每个会话当前模式。
	modeID       string
	sessionModes map[string]string
	modeReg      *modes.Registry

	// sandbox 是 ACP 的策略闸门：会话档位 → 执行口（bash/pwsh/FS 同一个引擎）。
	// nil = 未接线（旧行为）；生产入口在 cmdACP 里 fail-loud 地装上。
	sandbox *sandbox.Gate
	// sandboxFS 把进程级 FS 边界切到会话档位（modules/fs/local 的可选能力）。
	sandboxFS fsSandboxModeSetter
	// fsExtra 把 additionalDirectories 变成 FS 只读根（可选能力）。
	fsExtra fsExtraReadRooter
	// sandboxMode 是**进程级**边界；"" = 还没有会话登记过档位。
	sandboxMode sandbox.Mode
	// sandboxCeiling 是进程上限（DSHD_SANDBOX_MODE）；会话档位不得比它更宽。
	sandboxCeiling sandbox.Mode
	// sandboxSessions 记录已登记会话的档位：A/B 两个会话不允许静默共享一个边界。
	sandboxSessions map[string]sandbox.Mode
	// additionalDirs 是每会话的 additionalDirectories（canonical，只读）。
	additionalDirs map[string][]string

	mu    sync.Mutex
	logs  map[string]*session.Log
	turns map[string]int
	hist  map[string][]llm.Message
	used  map[string]int
	meter map[string]*session.Meter
}

func newACPRunner(reg spi.ToolRegistry, runner spi.Runner, base, model, home string, steps int) *acpRunner {
	r := &acpRunner{
		reg: reg, runner: runner, base: base, model: model, home: home, steps: steps,
		logs: map[string]*session.Log{}, turns: map[string]int{}, hist: map[string][]llm.Message{},
		used: map[string]int{}, meter: map[string]*session.Meter{}, keepRecent: defaultACPKeepRecent,
		approvals: approval.New(), sessionModes: map[string]string{}, modeID: modes.DefaultID,
		sandboxSessions: map[string]sandbox.Mode{}, additionalDirs: map[string][]string{},
	}
	r.setModelCapacity(llm.ModelCapacity{})
	return r
}

// setModelCapacity 在启动参数或测试给出显式声明后统一重算有效容量。
// 只在 Prompt 前调用；模型本身仍由既有路由固定，不在这里选择。
func (r *acpRunner) setModelCapacity(declared llm.ModelCapacity) {
	r.capacity = llm.ResolveModelCapacity(r.model, declared)
	r.ctxWindow = r.capacity.ContextWindow
}

// initSandbox 装上 ACP 的 sandbox 闸门（会话档位 → 执行口）。
//
// 生产入口只在 cmdACP 里调用：装不上就等于没有策略，所以错误必须往上抛。
// ceiling 非空时（进程环境显式给了 DSHD_SANDBOX_MODE）它同时是会话档位的上限。
func (r *acpRunner) initSandbox(workspace, home string, ceiling sandbox.Mode) error {
	if strings.TrimSpace(workspace) == "" {
		return fmt.Errorf("%w: 工作区为空", sandbox.ErrMissingContext)
	}
	if strings.TrimSpace(home) == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("%w: 解析宿主 home: %v", sandbox.ErrMissingContext, err)
		}
		home = h
	}
	base := sandbox.ModeWorkspaceWrite
	if ceiling != "" {
		base = ceiling
	}
	gate, err := sandbox.NewGate(sandbox.ProcessConfig{
		Mode: base, WorkspaceRoot: workspace, HomeDir: home,
	})
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.sandbox = gate
	r.sandboxCeiling = ceiling
	r.mu.Unlock()
	return nil
}

// sandboxModeFor 把会话当前的 mode 映射成真实 runtime 档位。
func (r *acpRunner) sandboxModeFor(sessionID string) (sandbox.Mode, error) {
	m := r.modeOf(sessionID)
	mode, err := m.SandboxMode()
	if err != nil {
		return "", fmt.Errorf("会话 %s 的 mode %q: %w", sessionID, m.ID, err)
	}
	return mode, nil
}

// registerSandbox 把一个会话的档位登记到进程边界上。
//
// ACP 是一个进程多个会话，而 FS 的读写没有 ctx、边界只能是进程级的：
// 所以这里显式规定**一个进程只服务一个 sandbox 档位**。出现第二个档位就
// fail-loud，由客户端另起 dshd——不静默共用一个"当前权限"（那正是串扰）。
func (r *acpRunner) registerSandbox(sessionID string) error {
	r.mu.Lock()
	gate := r.sandbox
	r.mu.Unlock()
	if gate == nil {
		return nil
	}
	mode, err := r.sandboxModeFor(sessionID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.sandboxCeiling != "" {
		cmp, cerr := sandbox.CompareModes(mode, r.sandboxCeiling)
		if cerr != nil {
			r.mu.Unlock()
			return cerr
		}
		if cmp > 0 {
			ceiling := r.sandboxCeiling
			r.mu.Unlock()
			return fmt.Errorf("%w: 会话 %s 的 sandbox 档位 %s 比进程上限 %s 更宽",
				sandbox.ErrWidenDenied, sessionID, mode, ceiling)
		}
	}
	if r.sandboxMode != "" && r.sandboxMode != mode {
		_, registered := r.sandboxSessions[sessionID]
		sole := registered && len(r.sandboxSessions) == 1
		if !sole {
			current := r.sandboxMode
			r.mu.Unlock()
			return fmt.Errorf(
				"ACP 一个进程只能服务一个 sandbox 档位：进程边界已是 %s，而会话 %s 需要 %s；"+
					"请为 %s 另起一个 dshd（同一个进程里静默共享一个边界就是 A/B 权限串扰）",
				current, sessionID, mode, mode)
		}
	}
	r.sandboxMode = mode
	r.sandboxSessions[sessionID] = mode
	fs := r.sandboxFS
	r.mu.Unlock()
	if err := gate.SetSessionMode(sessionID, mode); err != nil {
		return err
	}
	if fs != nil {
		return fs.SetSandboxMode(mode)
	}
	return nil
}

// forgetSandbox 清掉一个会话的档位与缓存（关闭/删除时调用）。
func (r *acpRunner) forgetSandbox(sessionID string) {
	r.mu.Lock()
	gate := r.sandbox
	delete(r.sandboxSessions, sessionID)
	delete(r.additionalDirs, sessionID)
	if len(r.sandboxSessions) == 0 {
		r.sandboxMode = ""
	}
	r.mu.Unlock()
	if gate != nil {
		gate.ForgetSession(sessionID)
	}
}

// SetAdditionalDirectories 把一个会话的 additionalDirectories 变成**只读**根。
//
// 语义：读放行（工作区之外）、写一律拒绝（策略层判 extra-read-only）；
// 目录必须是已存在的绝对目录，未知形态 fail-loud。ACP 服务端把它从
// session/new 接到这里的最后一段在 internal/acp（不在本任务写作用域），
// 见 $S/WIRING.md。
func (r *acpRunner) SetAdditionalDirectories(sessionID string, dirs []string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("%w: session id 为空", sandbox.ErrMissingContext)
	}
	canonical := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		clean := strings.TrimSpace(dir)
		if clean == "" {
			continue
		}
		abs, err := filepath.Abs(clean)
		if err != nil {
			return fmt.Errorf("additionalDirectories %q: %w", dir, err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("additionalDirectories %q 不可用: %w", dir, err)
		}
		if !st.IsDir() {
			return fmt.Errorf("additionalDirectories %q 不是目录", dir)
		}
		c, err := sandbox.CanonicalPath(abs)
		if err != nil {
			return err
		}
		canonical = append(canonical, c)
	}
	if len(canonical) == 0 {
		return nil
	}
	r.mu.Lock()
	gate := r.sandbox
	extra := r.fsExtra
	r.mu.Unlock()
	if gate == nil {
		return fmt.Errorf("%w: sandbox 未接线", sandbox.ErrPolicyUnwired)
	}
	if err := gate.SetSessionExtraReadRoots(sessionID, canonical); err != nil {
		return err
	}
	if extra != nil {
		for _, dir := range canonical {
			if err := extra.AddExtraReadRoot(dir); err != nil {
				return err
			}
		}
	}
	r.mu.Lock()
	r.additionalDirs[sessionID] = canonical
	r.mu.Unlock()
	return nil
}

// Prompt 的返回值是**命名**的，因为出口兜底要看见最终的错误：
// turn 开了没闭合是这一层必须自己负责的事（D1），defer 里只能读命名返回值。
func (r *acpRunner) Prompt(ctx context.Context, sessionID, cwd, text string, emit func(agent.Event) error) (stopReason string, usage map[string]any, err error) {
	r.mu.Lock()
	logw := r.logs[sessionID]
	if logw == nil {
		r.mu.Unlock()
		l, err := r.ensureSession(sessionID, cwd)
		if err != nil {
			return "", nil, err
		}
		r.mu.Lock()
		if r.logs[sessionID] == nil {
			r.logs[sessionID] = l
		}
		logw = r.logs[sessionID]
	}
	r.turns[sessionID]++
	turn := r.turns[sessionID]
	r.mu.Unlock()

	// 把"当前会话"指给 history_search：没有这行，模型回查的时候查的是空。
	attachHistory(logw)

	// 每轮开始前自检：token 估算超过阈值就自动压缩（失败只记日志，不打断这一轮）。
	if r.autoCompact > 0 {
		if err := r.autoCompactNow(ctx, sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "自动压缩失败（不影响本轮）：%v\n", err)
		}
	}

	user := []spi.Block{{Type: "text", Text: text}}
	if err := logw.Append("turn/start", session.TurnStartData(turn)); err != nil {
		return "", nil, err
	}
	// 同 run：turn 一开，结局就有唯一责任人。ACP 这边尤其要补——进程不会为此退出，
	// 下一个 prompt 还要接着用同一个会话；日志是唯一留下"这一轮怎么了"的地方。
	tc := newTurnCloser(logw, turn)
	defer func() {
		if p := recover(); p != nil {
			if cerr := tc.ExitOnPanic(p); cerr != nil {
				fmt.Fprintf(os.Stderr, "panic 时补 turn/end 失败（日志里这一轮仍是悬挂）：%v\n", cerr)
			}
			panic(p)
		}
		if cerr := tc.Exit(ctx, err); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if err := logw.Append("user/message", session.UserMessage(user)); err != nil {
		return "", nil, err
	}
	if err := captureRewindCheckpointForLog(r.home, logw); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}
	// 官方 0.2.2 #78 `llm/stream`：请求前缀先耐久，再派发模型请求。
	// 与 run / serve 同一条判据；失败即 fail-closed（这一轮不派发），
	// 出口兜底照旧把 turn/end 补上（D1），不会留下悬挂的 turn。
	acpCheckpoint := session.NewCheckpointRequest("text", []byte(text))
	if _, err := session.WriteDurableCheckpoint(r.home, logw.Path, &acpCheckpoint); err != nil {
		return "", nil, fmt.Errorf("durability checkpoint 落盘失败，模型请求未派发: %w", err)
	}

	var lastCallSeq int64
	var usageOut map[string]any
	sink := func(e agent.Event) error {
		// 先落盘（耐久），再上 wire（瞬态）。
		switch e.Kind {
		case agent.EventAssistantMsg:
			a := e.Assistant
			// 铺块走 serve 那条路的**同一份**逻辑（serve_turn.assistantBlocksFrom）：
			// 思考 → 正文 → 调用。
			//
			// 以前这里自己铺一份、只铺 text + tool_calls：于是 ACP 跑完的一轮在 v4 里
			// **没有思考块**。而 history / fork / session.load 三个投影都已经会认思考块——
			// 每一环都对，只是源里没写，读回来等于那轮模型没想过任何事。
			// 思考正文取自 `a.Reasoning`：它就是 `llm.Response.Reasoning`，也即
			// 事件流里那些 `agent.EventReasoning` 增量拼起来的那段（同一次快照的两面）。
			blocks := assistantBlocksFrom(a.Content, a.Reasoning, a.ToolCalls)
			// v4 usage 与 run 那条路**同一份**投影（assistantUsageFrom）：键名、
			// input 是否含缓存命中、什么时候不写键，都只有一处判断。
			usage := assistantUsageFrom(a.Usage)
			if a.Usage != nil {
				usageOut = map[string]any{"inputTokens": a.Usage.PromptTokens, "outputTokens": a.Usage.CompletionTokens}
			}
			if err := logw.Append("assistant/message",
				session.AssistantMessageEvent(e.Turn, e.Step, blocks, a.Finish, usage)); err != nil {
				return err
			}
			if a.Usage != nil {
				r.observeUsage(sessionID, logw.Path, a.Usage.TotalTokens)
			}
			if a.Usage != nil && r.ctxWindow > 0 {
				// `used` 是**这一次调用的上下文大小**，不是"这个会话累计花了多少 token"。
				// 原来是累加的：跑 10 步就变成真实值的 10 倍，客户端会以为快撑爆了。
				var est int
				if m := r.meterOf(sessionID); m != nil {
					est = m.Estimate(session.HistoryToSurface(r.hist[sessionID]))
				}
				used := contextUsed(a.Usage.PromptTokens, a.Usage.CompletionTokens, a.Usage.TotalTokens, est)
				r.mu.Lock()
				r.used[sessionID] = used
				r.mu.Unlock()
				if err := emit(agent.Event{
					Kind: agent.EventUsage, Turn: e.Turn, Step: e.Step,
					UsageUsed: used, UsageSize: r.ctxWindow,
				}); err != nil {
					return err
				}
			}
		case agent.EventToolCall:
			lastCallSeq = logw.Seq()
			if err := logw.Append("tool/call",
				session.ToolCallEvent(e.Turn, e.Step, e.CallID, e.ToolName, e.Arguments)); err != nil {
				return err
			}
		case agent.EventToolResult:
			var blocks []spi.Block
			if e.Result != nil {
				blocks = session.ProjectResult(*e.Result)
			} else {
				blocks = []spi.Block{{Type: "text", Text: "（无结果）"}}
			}
			if err := logw.Append("tool/result",
				session.ToolResultEventWithView(e.Turn, e.Step, e.CallID, blocks, e.IsError, cwd, []int64{lastCallSeq}, viewOf(e.Result))); err != nil {
				return err
			}
		case agent.EventTurnEnd:
			// 同 run：结局按 finish 写，截断不许记成「完成」；走 closer 才幂等。
			// 这里仍然要往下走到 emit：ACP 客户端那边的 turn 结束是靠
			// session/prompt 的 stopReason 回应的，但 emit 是这一层的统一观测口，
			// 少发一个事件等于偷偷改掉了另一条链路的行为。
			if err := tc.End(e.Finish); err != nil {
				return err
			}
		}
		return emit(e)
	}

	// 模型声明与执行准入共用同一轮、同一个过滤视图。只裁工具表不够：
	// 模型或直接调用方仍能点名隐藏工具，执行入口必须再拒绝一次。
	mode := r.modeOf(sessionID)
	view := modes.Filter(r.reg, mode)
	r.mu.Lock()
	sandboxGate := r.sandbox
	workspace := r.workspace
	r.mu.Unlock()
	var runner spi.Runner = &guardedRunner{
		inner: r.runner, reg: r.reg, view: view, ask: r.ask,
		sessionID: sessionID, approvals: r.approvals,
		modeApproval: mode.Approval,
		sandbox:      sandboxGate, workspace: workspace,
		stripSandboxArg: !acpToolsHaveOwnSandboxGate(),
	}

	loop := &agent.Loop{
		Chat:      llm.New(r.base, os.Getenv("DSHD_LLM_KEY")),
		Tools:     view,
		Runner:    runner,
		Project:   session.ProjectResult,
		Model:     r.model,
		Capacity:  r.capacity,
		Reasoning: r.reasoning,
		System:    r.system,
		SessionID: sessionID,
		MaxSteps:  r.steps,
		Emit:      sink,
		// 官方 0.2.2 #78 `agent/pre-step`：每一步模型请求之前先让请求前缀耐久。
		// ACP 的 v4 不落 step/start，所以每一步的锚点就是该步请求前的事件数；
		// 第一步与 prompt 侧车同锚点（幂等命中）。
		PreStep: worktreePreStep(newWorktreeService(newSessionSource(r.home, "")), sessionID, func(context.Context, int) error {
			return writeDurableCheckpointForSession(r.home, sessionID, nil)
		}),
	}

	// hist 必须在压缩**之后**取：autoCompactNow 会把 r.hist 换成压缩后的历史，
	// 在压缩之前取好的快照会让这一轮仍然用未压缩的历史——
	// 结果是压缩每轮都跑（每次都花一次模型调用），但当轮完全白压。
	r.mu.Lock()
	hist := r.hist[sessionID]
	r.mu.Unlock()

	next, err := loop.Run(ctx, turn, append(hist, llm.Message{Role: "user", Content: text}))
	r.mu.Lock()
	r.hist[sessionID] = next
	r.mu.Unlock()

	if err != nil {
		if ctx.Err() != nil {
			return "cancelled", usageOut, nil
		}
		return "", usageOut, err
	}
	return "end_turn", usageOut, nil
}

// guardedRunner 在调用工具前按**工具自己的能力声明**决定要不要问用户。
//
// 这里不按工具名判断：名字表是 dsc 踩过的坑（sandbox.go:52），
// 一个改了名字的工具就会绕过审批。
type guardedRunner struct {
	inner spi.Runner
	// reg 用于读取工具的 Caps；view 是本轮模型声明与执行准入共用的快照。
	// 非 ACP 调用方没有模式视图时 view 为空，准入退回 reg，原有行为不变。
	reg       spi.ToolRegistry
	view      spi.ToolRegistry
	ask       func(ctx context.Context, sessionID, tool string, args string) (bool, error)
	sessionID string
	approvals *approval.Memory
	// modeApproval 是当前模式的审批倾向（""=沿用宿主 / ask / allow / deny）。
	modeApproval string
	// policyDeniesUnattended 表示"策略在无人值守时不问、直接拒"（--policy deny 或非终端 auto）。
	// 它与"人看了之后说不"要分开说——对模型来说前者换问法没用，后者可以再来一次。
	policyDeniesUnattended bool
	// sandbox 是会话级 sandbox 闸门；nil = 未接线（旧行为，不做 per-call 判定）。
	sandbox *sandbox.Gate
	// workspace 是 ACP 进程的 canonical 工作区，给不实现 spi.SandboxScope 的执行类工具兜底。
	workspace string
	// stripSandboxArg 表示工具自己的 sandbox 闸门没有接线（进程环境未设
	// DSHD_SANDBOX_MODE）：这时 sandbox_permissions 由本闸门消费，不能再交给
	// 工具——否则工具会按"策略层未接线"的防降级规则 fail-loud。
	stripSandboxArg bool
}

func (g *guardedRunner) Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
	var tool spi.Tool
	known := false
	if g.reg != nil {
		tool, known = g.reg.Get(name)
	}
	// ask=nil 的 ACP 路径原来不做审批；这里只新增可见性准入，不能悄悄新增审批。
	needsApproval := g.ask != nil && known && tool.Caps().NeedsApproval()
	needAsk := false
	if needsApproval {
		// 「以后都允许」**不能被子调用继承**。
		//
		// 理由：记忆是按（会话, 工具）记的，而一段 workflow 里同一个写类工具
		// 可以跑 N 次。若子调用也吃这份记忆，人点一次"允许"就等于给 N 次写盘
		// 盖了章——批准被脚本放大了，而人只看到一次弹窗。
		// 所以嵌套调用一律重新问（无人值守策略下就是一律拒）。
		nested := spi.SubDispatchFrom(ctx).Depth > 0
		remembered := !nested && g.approvals != nil && g.approvals.Allowed(g.sessionID, name)
		allow, ask := approvalDecision(g.modeApproval, remembered)
		if !allow && !ask {
			// deny 的既有结果优先于可见性错误；两种路径都不会落到底层执行器。
			return spi.Result{Hint: "当前模式的审批策略是 deny：换成只读工具，或让用户切到别的模式"},
				fmt.Errorf("模式策略拒绝了 %s", name)
		}
		needAsk = ask
	}

	// 工具表过滤只是模型声明，不是执行授权。无论是否配置审批，隐藏和未知工具
	// 都必须在这里失败；workflow 的 SubDispatch 放回 g 自己，因此子调用也过这一闸门。
	view := g.view
	if view == nil {
		view = g.reg
	}
	if view == nil {
		return spi.Result{Hint: "当前环境没有可用工具表；不要继续尝试未声明的工具"},
			fmt.Errorf("当前模式未声明工具 %q", name)
	}
	if _, ok := view.Get(name); !ok {
		return spi.Result{Hint: "只调用本轮工具表中列出的名字；隐藏或未知工具都不会执行"},
			fmt.Errorf("当前模式未声明工具 %q", name)
	}

	// sandbox 策略先于任何进程/写盘动作：deny 在这里返回，执行器一次都不会被调用。
	pending, err := g.evaluateSandbox(ctx, known, tool, name, args)
	if err != nil {
		return spi.Result{Hint: sandboxRejectHint(err)}, err
	}
	if pending != nil {
		if g.ask == nil {
			// 需要一次性审批却没有审批通道：fail-closed，不许静默按 allow 执行。
			return spi.Result{Hint: "这次调用请求了更宽的 sandbox 权限，但当前没有审批通道；去掉 sandbox_permissions 后重试"},
				fmt.Errorf("%w: 没有审批通道", sandbox.ErrNeedsApproval)
		}
		needAsk = true
	}

	// 给"工具里再调工具"留一条**同样受管**的通道：放进去的是 g 自己，
	// 所以子调用照样过可见性准入、审批、计划模式闸门和 spill。
	// ask=nil 原来没有 guardedRunner，也没有 SubDispatch；保持这条行为不变。
	if g.ask != nil && spi.SubDispatchFrom(ctx).Runner == nil {
		ctx = spi.WithSubDispatch(ctx, spi.SubDispatch{Runner: g})
	}

	if needAsk {
		ok, aerr := g.ask(ctx, g.sessionID, name, string(args))
		if aerr != nil {
			return spi.Result{Hint: "审批失败：客户端可能断开了；重试或改用只读工具"}, fmt.Errorf("审批失败: %w", aerr)
		}
		if !ok {
			// 拒绝可能是"人说不"，也可能是"策略不让人被问到"（--policy deny / 非终端 auto）。
			// 两种情况对模型的要求不一样：前者要重新征求同意，后者换问法也没用。
			if g.modeApproval == "deny" || g.policyDeniesUnattended {
				return spi.Result{Hint: "当前策略不允许这类调用（不是有人拒绝了你）：改用只读工具，或让用户放宽审批策略"},
					fmt.Errorf("策略拒绝了 %s", name)
			}
			return spi.Result{Hint: "用户拒绝了这次调用：换成只读方式确认，或先说明你要做什么再请求"},
				fmt.Errorf("用户拒绝了 %s", name)
		}
	}
	if pending != nil {
		// 批准只属于**这一次调用**：call id 与批准一起放回 ctx，再用同一个策略引擎复核。
		// 复核必须 allow；这一步同时证明绑定没有漂（命令/路径/会话/操作）。
		ctx = sandbox.WithCallID(sandbox.WithEscalation(ctx, pending.Approval.Decide(true)), pending.CallID)
		confirmed, cerr := g.evaluateSandbox(ctx, known, tool, name, args)
		if cerr != nil {
			return spi.Result{Hint: sandboxRejectHint(cerr)}, cerr
		}
		if confirmed != nil {
			return spi.Result{Hint: "一次性审批没有通过策略复核（命令/路径/调用对不上）；重新请求审批，不要复用旧批准"},
				fmt.Errorf("%w: 批准后复核仍未放行", sandbox.ErrApprovalBinding)
		}
	}
	if g.stripSandboxArg {
		args = stripSandboxPermissions(args)
	}
	return g.inner.Run(ctx, name, args)
}

// acpToolsHaveOwnSandboxGate 报告 ACP 进程里的工具是否自带 sandbox 闸门。
// bash/pwsh 的闸门是装配期从进程环境解析的；环境没设就是 nil。
func acpToolsHaveOwnSandboxGate() bool {
	return strings.TrimSpace(os.Getenv(sandbox.EnvMode)) != ""
}

// stripSandboxPermissions 把已由宿主消费掉的 sandbox_permissions 从工具参数里去掉。
//
// 只在工具自己没有闸门时调用：那时把请求字段交给它只会得到"策略层未接线"的
// fail-loud，而这次调用已经由 guardedRunner 用同一个策略引擎判定过。
func stripSandboxPermissions(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	if _, ok := obj["sandbox_permissions"]; !ok {
		return raw
	}
	delete(obj, "sandbox_permissions")
	b, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return b
}

// evaluateSandbox 做一次 per-call 判定：返回非 nil 表示"待审批"。
// allow 返回 (nil,nil)，deny 返回 ErrDenied，输入不合法返回原始错误。
func (g *guardedRunner) evaluateSandbox(ctx context.Context, known bool, tool spi.Tool, name string, args json.RawMessage) (*sandbox.Decision, error) {
	if g.sandbox == nil || !known || tool == nil {
		return nil, nil
	}
	var (
		op         sandbox.Operation
		path       string
		command    string
		permission sandbox.Permission
	)
	if scope, ok := tool.(spi.SandboxScope); ok {
		sc, err := scope.SandboxCall(args)
		if err != nil {
			return nil, err
		}
		op = sandbox.Operation(sc.Operation)
		path = sc.Path
		command = sc.Command
		permission = sandbox.Permission(sc.Permission)
	} else {
		// 兼容路径：只按 Caps 分类，路径取官方工具参数名 file_path；
		// 认不出来就不参与闸门（由工具/FS 自己兜底），绝不按工具名猜权限。
		permission = sandboxPermissionFromArgs(args)
		switch {
		case tool.Caps().Executes:
			op = sandbox.OperationExecute
			path = g.workspace
			command = string(args)
		case tool.Caps().Writes:
			if p := sandboxPathFromArgs(args); p != "" {
				op = sandbox.OperationWrite
				path = p
			}
		}
	}
	var d sandbox.Decision
	var err error
	switch op {
	case sandbox.OperationExecute:
		d, err = g.sandbox.EvaluateCommand(ctx, command, path, permission)
	case sandbox.OperationWrite, sandbox.OperationRead:
		d, err = g.sandbox.EvaluateFS(ctx, op, path, permission)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch d.Action {
	case sandbox.ActionAllow:
		return nil, nil
	case sandbox.ActionNeedsApproval:
		return &d, nil
	default:
		return nil, d.Error()
	}
}

// sandboxPermissionFromArgs 读工具参数里的 sandbox_permissions。
// 未知值原样交给策略层报错（fail-loud），不在这里静默丢弃。
func sandboxPermissionFromArgs(raw json.RawMessage) sandbox.Permission {
	var a struct {
		SandboxPermissions string `json:"sandbox_permissions"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return ""
	}
	return sandbox.Permission(a.SandboxPermissions)
}

// sandboxPathFromArgs 取官方文件工具的路径参数（write/edit 的 file_path）。
func sandboxPathFromArgs(raw json.RawMessage) string {
	var a struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return ""
	}
	return strings.TrimSpace(a.FilePath)
}

// sandboxRejectHint 把策略错误翻成模型能照做的下一步。
func sandboxRejectHint(err error) string {
	switch {
	case errors.Is(err, sandbox.ErrDenied):
		return "当前会话的 sandbox 档位拒绝了这次调用（read-only 不能写/执行，workspace-write 只能碰工作区）；改到工作区内，或让用户显式切到更宽的档位"
	case errors.Is(err, sandbox.ErrNeedsApproval):
		return "这次调用请求了更宽的 sandbox 权限，但审批没有完成；不要原样重试"
	case errors.Is(err, sandbox.ErrApprovalBinding):
		return "一次性审批与这次调用不匹配（换过命令/路径/调用）；重新请求审批，不要复用旧批准"
	case errors.Is(err, sandbox.ErrPolicyUnwired), errors.Is(err, sandbox.ErrMissingContext):
		return "宿主没有把 sandbox 策略接进这次调用；报告宿主，不要重试"
	default:
		return "sandbox 策略拒绝了这次调用；按错误里的原因调整后再试"
	}
}

// Resume 从 v4 文件恢复历史，并以 append 模式续写同一个文件。
func (r *acpRunner) Resume(ctx context.Context, sessionID, cwd string) error {
	// 不能只按 (cwd, id) 算：projectKey 是按 cwd 字符串算的，
	// 换个盘符/挂载点/另一台机器就算不出来了，而 session id 是全局唯一的。
	path, err := session.ResolvePath(r.home, cwd, sessionID)
	if err != nil {
		return err
	}
	back, err := v4.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读会话失败（%s）: %w", path, err)
	}
	hist := session.History(back)

	// 会话里记着当初用的模式（header.agentPreset）。恢复它——
	// 否则一个 read-only 会话 resume 之后会悄悄变成 default，权限反而更大。
	//
	// 解析不出来必须响亮失败：静默回落到默认模式就是一次权限放大。
	if p := back.Header.AgentPreset; p != "" {
		m, rerr := r.registry().Resolve(p)
		if rerr != nil {
			return fmt.Errorf("会话 %s 记录的 mode %q 现在解析不了（%v）；不能回落到默认模式（那会放大权限），请把它改成存在的 mode id",
				sessionID, p, rerr)
		}
		r.mu.Lock()
		r.sessionModes[sessionID] = m.ID
		r.mu.Unlock()
	}
	// 档位检查必须在打开写端之前：错误路径不能把一个还持着写锁的 Log 留在外面。
	if err := r.registerSandbox(sessionID); err != nil {
		return err
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		return err
	}

	r.mu.Lock()
	if old := r.logs[sessionID]; old != nil {
		_ = old.Close()
	}
	r.logs[sessionID] = logw
	r.hist[sessionID] = hist
	r.mu.Unlock()
	attachHistory(logw) // resume 之后也要能回查

	// 把上次留下的授权读回来（只有显式开了 --remember-approvals 才会有记录）。
	if r.approvalsStore != nil && r.approvals != nil {
		tools, err := r.approvalsStore.Load(sessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读授权记录失败（按未授权处理）：%v\n", err)
		}
		for _, t := range tools {
			r.approvals.Remember(sessionID, t)
		}
	}
	return nil
}

// Fork 从当前会话派生一个新会话：继承 surface，不继承审批记忆。
func (r *acpRunner) Fork(ctx context.Context, sessionID, cwd string) (string, error) {
	r.mu.Lock()
	srcLog := r.logs[sessionID]
	r.mu.Unlock()
	if srcLog == nil {
		return "", fmt.Errorf("没有活跃会话 %s（要先 resume 或 prompt 过）", sessionID)
	}
	src, err := v4.ReadFile(srcLog.Path)
	if err != nil {
		return "", err
	}
	newID := "session-" + session.NewID()
	// 子会话继承父会话**当前生效**的模式（不是 header 里的旧值）。
	parentMode := r.modeOf(sessionID)
	src.Header.AgentPreset = parentMode.ID
	forked, err := session.Fork(src, r.home, cwd, newID)
	if err != nil {
		return "", err
	}
	if err := forked.Close(); err != nil {
		return "", err
	}

	back, err := v4.ReadFile(forked.Path)
	if err != nil {
		return "", err
	}
	writer, err := session.OpenSession(r.home, cwd, newID)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.logs[newID] = writer
	r.hist[newID] = session.History(back)
	r.turns[newID] = 0
	r.sessionModes[newID] = parentMode.ID
	r.mu.Unlock()
	// fork 出来的会话继承父会话**当前**档位；registerSandbox 会拒绝任何放大。
	if err := r.registerSandbox(newID); err != nil {
		return "", err
	}
	return newID, nil
}

func (r *acpRunner) Close(sessionID string) error {
	r.forgetSandbox(sessionID)
	if r.approvals != nil {
		r.approvals.Forget(sessionID)
	}
	r.mu.Lock()
	l := r.logs[sessionID]
	delete(r.logs, sessionID)
	delete(r.hist, sessionID)
	delete(r.turns, sessionID)
	r.mu.Unlock()
	if l != nil {
		return l.Close()
	}
	return nil
}

// cmdACP 在 stdio 上跑 ACP 服务端。stdout 只放协议。
func cmdACP(argv []string) error {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	manifest := fs.String("manifest", "dsh.toml", "manifest 路径")
	model := fs.String("model", envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017"), "模型 id")
	// 三个入口（run/serve/acp）要有同一个开关：ACP 是 Martty 那条路，
	// 这里缺了就意味着"同一个模型，换个客户端就没有 reasoning"——那种差异最难解释。
	reasoning := fs.String("reasoning", os.Getenv("DSHD_REASONING_EFFORT"),
		"推理强度：空=不要（默认）| xhigh | max")
	base := fs.String("base", envOr("DSHD_LLM_BASE", defaultLLMBase), "OpenAI 兼容端点")
	maxChars := fs.Int("max-chars", defaultMaxChars, "工具输出外置阈值（字符）")
	home := fs.String("home", defaultHome(), "会话根目录")
	steps := fs.Int("max-steps", defaultTurnMaxSteps, "每轮最多几步")
	workspaceBytes := fs.Int("workspace-bytes", defaultWorkspaceBytes, "AGENTS.md 进系统提示的字节上限（0=不传）")
	fs.StringVar(rulesPath, "agents", "", "规则文件路径（默认工作区 AGENTS.md）")
	// 与 run/serve 同一份默认值与同一套写法（整数或 true/false），见 host_run_options.go。
	autoCompact := defaultAutoCompact
	fs.Var(autoCompactValue{target: &autoCompact}, "auto-compact",
		"surface 估算超过多少 token 就自动压缩（0/false=关闭，true=默认 40000）")
	ctxWindow := fs.Int("context-window", 0, "显式上下文窗口（usage_update 分母）；0=按模型容量解析")
	maxOutput := fs.Int("max-output", 0, "显式单次最大输出 token；0=按模型容量解析")
	modeID := fs.String("mode", modes.DefaultID, "启动模式 id（见 --modes-dir 与内置）")
	modesDir := fs.String("modes-dir", "", "用户自建模式目录（默认 <home>/modes）")
	remember := fs.Bool("remember-approvals", false, "把\"以后都允许\"记到磁盘，resume 后仍然生效")
	// 父进程看门狗：三个入口共用同一组开关，见 parentwatch.go。
	pw := addParentWatchFlags(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}

	if err := checkHome(*home); err != nil {
		return err
	}
	// ACP 也会长跑：客户端是编辑器/壳的一部分，它崩了 stdin 并不会立刻 EOF
	// （管道还挂在别的进程手里），所以光靠"读不到输入"退不出去。
	watch := pw.start(os.Stderr)
	defer watch.Stop()
	// 同一套诊断日志：ACP 模式下 stdout 是协议，只能靠文件事后查。
	// 拿不到也明说——stderr 还没被协议占用，说得出话。
	if d := diagOrWarn(*home); d != nil {
		defer d.Close()
	}
	mods, err := loadMods(*manifest, *home, *maxChars, nil)
	if err != nil {
		return err
	}
	k := kernel.New("dshd-acp")
	root := k.Root()
	defer root.Dispose()
	timingf("acp: 装配前")
	if _, err := kernel.Assemble(root, mods); err != nil {
		return err
	}
	timingf("acp: 装配后")
	reg, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		return err
	}
	runner, err := kernel.Inject(root, spi.ToolRunnerKey)
	if err != nil {
		return err
	}

	acpRunner := newACPRunner(reg, runner, *base, *model, *home, *steps)
	assembled := buildSystemPromptWithHome(reg, *model, mustGetwd(), acpRunner.home, *workspaceBytes)
	acpRunner.reasoning = *reasoning
	acpRunner.system = assembled
	acpRunner.autoCompact = autoCompact
	acpRunner.setModelCapacity(llm.ModelCapacity{ContextWindow: *ctxWindow, MaxOutput: *maxOutput})
	dir := *modesDir
	if dir == "" {
		dir = filepath.Join(*home, "modes")
	}
	userModes, err := modes.LoadDir(dir)
	if err != nil {
		return err
	}
	modeReg, err := modes.NewRegistry(userModes)
	if err != nil {
		return err
	}
	m, err := modeReg.Resolve(*modeID)
	if err != nil {
		return err
	}
	// FS 若支持换根/切档位/追加只读根，就把 ACP 的会话语义接进来。
	if fs, err := kernel.Inject(root, spi.FSKey); err == nil {
		if rf, ok := fs.(spi.RootFollower); ok {
			acpRunner.fsRoot = rf
		}
		if setter, ok := fs.(fsSandboxModeSetter); ok {
			acpRunner.sandboxFS = setter
		}
		if extra, ok := fs.(fsExtraReadRooter); ok {
			acpRunner.fsExtra = extra
		}
	}
	// sandbox 闸门：装不上就是没有策略，所以这里 fail-loud。
	ceiling := sandbox.Mode("")
	if raw := strings.TrimSpace(os.Getenv(sandbox.EnvMode)); raw != "" {
		parsed, err := sandbox.ParseMode(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", sandbox.EnvMode, err)
		}
		ceiling = parsed
	}
	if err := acpRunner.initSandbox(mustGetwd(), "", ceiling); err != nil {
		return fmt.Errorf("ACP sandbox 装配失败: %w", err)
	}
	if ex, err := kernel.Inject(root, spi.ExecKey); err == nil {
		if cf, ok := ex.(spi.CwdFollower); ok {
			acpRunner.execCwd = cf
		}
	}
	acpRunner.modeReg = modeReg
	acpRunner.modeID = m.ID
	if *remember {
		acpRunner.approvalsStore = &approval.Store{Dir: filepath.Join(*home, "state", "approvals")}
	}
	srv := &acp.Server{
		In:      os.Stdin,
		Out:     os.Stdout,
		Agent:   acpRunner,
		Name:    "dsh-go-acp",
		Version: version,
	}
	// 审批闸门：会改世界的工具先问客户端（ACP 的 session/request_permission）。
	acpRunner.ask = func(ctx context.Context, sessionID, tool, args string) (bool, error) {
		raw, err := srv.CallClient(ctx, acp.MethodRequestPermission, map[string]any{
			"sessionId": sessionID,
			// 客户端界面上显示什么，决定了用户点"允许"时知不知道自己在批什么。
			// 三个字段各司其职：
			//   title   —— 一行摘要，**命令在前**（很多客户端只显示这一行）
			//   kind    —— 让客户端用对的图标/措辞（execute / edit）
			//   content —— 完整说明，含"模型自述（仅供参考）"的标注
			"toolCall": map[string]any{
				"toolCallId": "perm-" + session.NewID(),
				"title":      approvalTitle(tool, args),
				"kind":       approvalKind(tool),
				"status":     "pending",
				"content": []any{map[string]any{
					"type":    "content",
					"content": map[string]any{"type": "text", "text": describeApproval(tool, args)},
				}},
				"rawInput": json.RawMessage(args),
			},
			"options": []any{
				map[string]any{"optionId": "allow-once", "name": "允许这一次", "kind": "allow_once"},
				map[string]any{"optionId": "allow-always", "name": "这个会话里以后都允许", "kind": "allow_always"},
				map[string]any{"optionId": "reject-once", "name": "拒绝", "kind": "reject_once"},
			},
		})
		if err != nil {
			return false, err
		}
		var resp struct {
			Result struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return false, err
		}
		if resp.Result.Outcome.Outcome != "selected" {
			return false, nil
		}
		switch resp.Result.Outcome.OptionID {
		case "allow-once":
			return true, nil
		case "allow-always":
			if acpRunner.approvals != nil {
				acpRunner.approvals.Remember(sessionID, tool)
				if acpRunner.approvalsStore != nil {
					if err := acpRunner.approvalsStore.Save(sessionID, acpRunner.approvals.Snapshot(sessionID)); err != nil {
						fmt.Fprintf(os.Stderr, "记下授权时出错（本次仍生效）：%v\n", err)
					}
				}
			}
			return true, nil
		default:
			return false, nil
		}
	}
	// 父进程没了要能把 Serve 从"卡在读 stdin"里叫醒：它的扫描循环只在读到
	// **下一行**时才看 ctx，而父进程死掉之后多半永远不会再来一行。
	// 所以两手一起：取消 ctx（让在飞的轮次按取消收尾），再关掉 stdin
	// （让 Scan 立刻返回）。收尾仍然走下面同一条路——日志照落盘。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-watch.Done():
			cancel()
			if err := os.Stdin.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "父进程看门狗：关 stdin 失败（%v），靠 ctx 取消收尾\n", err)
			}
		case <-ctx.Done():
		}
	}()
	serveErr := srv.Serve(ctx)
	// 父进程看门狗触发的收尾是**预期**：不是错误，退出码给 0。
	// 原因那一行已经在触发时打到 stderr 了，别再盖一条"读 stdin 失败"上去。
	parentGone := false
	select {
	case <-watch.Done():
		parentGone = true
	default:
	}
	if parentGone {
		serveErr = nil
	}
	// 客户端断连之后仍要把日志落盘：批窗口里可能还压着没写出去的帧。
	if cerr := acpRunner.Shutdown(); cerr != nil && serveErr == nil {
		serveErr = cerr
	}
	return serveErr
}

// rulesPath 指向要注入的规则文件；空 = 用工作区自己的 AGENTS.md。
// 用途：给"写代码的执行者"喂精简手册（docs/WORKER.md），而不是维护者那本。
var rulesPath = flag.String("agents", "", "规则文件路径（默认工作区 AGENTS.md）")

// buildSystemPrompt 保留非 ACP 入口的原行为：没有显式 home，就不注入全局规则。
func buildSystemPrompt(reg spi.ToolRegistry, model, cwd string, workspaceBytes int) string {
	return buildSystemPromptWithHome(reg, model, cwd, "", workspaceBytes)
}

// buildSystemPromptWithHome 分别给 <home>/AGENTS.md 与项目规则同样的正文预算。
func buildSystemPromptWithHome(reg spi.ToolRegistry, model, cwd, home string, workspaceBytes int) string {
	globalRulesPath := ""
	if home != "" {
		globalRulesPath = filepath.Join(home, "AGENTS.md")
	}
	return prompt.Default(prompt.Context{
		Model:             model,
		Cwd:               cwd,
		Platform:          runtime.GOOS + "/" + runtime.GOARCH,
		Date:              time.Now().Format("2006-01-02"),
		Tools:             reg.Tools(),
		GlobalRulesPath:   globalRulesPath,
		MaxGlobalBytes:    workspaceBytes,
		MaxWorkspaceBytes: workspaceBytes,
		ExtraRulesPath:    *rulesPath,
	})
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// observeUsage 把服务端实测值喂给 meter（PLAN §10：实测只负责绝对值）。
func (r *acpRunner) observeUsage(sessionID, path string, realTotal int) {
	back, err := v4.ReadFile(path)
	if err != nil {
		return
	}
	est := session.Meter{}
	if m := r.meterOf(sessionID); m != nil {
		est = *m
	}
	r.mu.Lock()
	m := r.meter[sessionID]
	if m == nil {
		m = &session.Meter{}
		r.meter[sessionID] = m
	}
	r.mu.Unlock()
	m.Observe(realTotal, est.Estimate(session.Surface(back)))
}

func (r *acpRunner) meterOf(sessionID string) *session.Meter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.meter[sessionID]
}

// defaultACPKeepRecent 是自动压缩时至少保留的最近消息数。
//
// 与改动前**实际生效**的值一致（旧代码在 Select 里写死 2，而结构体字段
// keepRecent 赋的 8 从未被读取）。改成常量 + 字段之后，默认行为一字不变，
// 但"这个数从哪来"只有一个答案。
const defaultACPKeepRecent = 2

// compactSelectOptions 是 autoCompactNow 选区间用的参数（唯一一份）。
//
// 抽出来是为了让 keepRecent 真的有使用点：字段被赋值、被读取、被测试看见，
// 才不会退化成一个"看起来可调"的死字段。
func (r *acpRunner) compactSelectOptions() compact.Options {
	return compact.Options{
		KeepRecent:   r.keepRecent,
		KeepTokens:   2000,
		MinToCompact: 2,
	}
}

// autoCompactNow 是"每轮前的自检"：估算超阈值就压缩并落事件。
func (r *acpRunner) autoCompactNow(ctx context.Context, sessionID string) error {
	path := ""
	r.mu.Lock()
	if l := r.logs[sessionID]; l != nil {
		path = l.Path
	}
	r.mu.Unlock()
	if path == "" {
		return nil
	}
	back, err := v4.ReadFile(path)
	if err != nil {
		return err
	}
	surface := session.Surface(back)
	m := r.meterOf(sessionID)
	if m == nil {
		m = &session.Meter{}
	}
	if est := m.Estimate(surface); est <= r.autoCompact {
		return nil
	}

	// 下限给 2（defaultACPKeepRecent）：压缩的收益来自"按体积留"，条数下限只
	// 保证模型眼前不是空的。这个值读 r.keepRecent，不再是一份孤立字面量——
	// 以前字段赋了 8 却没人读，等于一个假装可调的旋钮。
	items, span, ok := compact.Select(surface, r.compactSelectOptions())
	if !ok {
		// 已经超预算了，但"保留最近 N 条"把可压区间挤没了——这时必须让步：
		// 压掉较早的一半。宁可摘要粗一点，也不能让这一轮超窗。
		keep := len(surface) / 2
		if keep < 1 {
			keep = 1
		}
		if keep >= len(surface) {
			return nil
		}
		// 兜底：只按条数留，不再看 token 预算——已经超窗了，能压多少压多少。
		items, span, ok = compact.Select(surface, compact.Options{KeepRecent: keep, KeepTokens: 1, MinToCompact: 2})
		if !ok {
			return nil
		}
		fmt.Fprintf(os.Stderr, "超预算且无处可压：放宽到只保留最近 %d 条\n", keep)
	}
	text, err := compact.Summarize(ctx, llm.New(r.base, os.Getenv("DSHD_LLM_KEY")), r.model, items)
	if err != nil {
		return err
	}
	r.mu.Lock()
	logw := r.logs[sessionID]
	r.mu.Unlock()
	if _, shadowed, err := session.Compact(logw, back.Events, span,
		[]spi.Block{{Type: "text", Text: text}},
		session.CompactionMeta{Provider: "dshd-acp", Model: r.model, LLMStreamCall: true}); err != nil {
		return err
	} else {
		fmt.Fprintf(os.Stderr, "自动压缩：遮蔽 %d 条，摘要 %d 字\n", len(shadowed), len([]rune(text)))
	}

	// 重新派生历史，让本轮看到压缩后的表面。
	after, err := v4.ReadFile(path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.hist[sessionID] = session.History(after)
	r.mu.Unlock()
	return nil
}

// contextUsed 决定 usage_update 里的 used：**这次调用的上下文大小**，不是累计值。
//
// 三条规则：
//  1. 有 prompt/completion 就用它们的和（服务端实测，最可信）；
//  2. 只有 total 就用 total；
//  3. 启发式估算更大时取估算——宁可早报警，也不要等到真撑爆。
func contextUsed(prompt, completion, total, estimate int) int {
	used := prompt + completion
	if used == 0 {
		used = total
	}
	if estimate > used {
		used = estimate
	}
	return used
}

// Load 载入会话并把历史**重放**给客户端（resume 不重放）。
func (r *acpRunner) Load(ctx context.Context, sessionID, cwd string, emit func(agent.Event) error) error {
	path, err := session.SessionPath(r.home, cwd, sessionID)
	if err != nil {
		return err
	}
	back, err := v4.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读会话失败: %w", err)
	}
	// 思考块得回原始事件取：session.Surface 只投影 text + tool_calls
	// （`llm.Message` 没有 reasoning 字段，`type:"reasoning"` 的块在投影那步就丢了）。
	// 与 fork 共用同一份识别规则（session.ReasoningBlocksBySeq），不写第二份。
	//
	// 不取会怎样：**只有思考**的回答在重放时一个事件都发不出来（正文为空、
	// 没有工具调用），断线重连/重开之后那一轮就像没发生过；有正文的回答也丢了
	// "它为什么这么干"。
	reasoning := session.ReasoningBlocksBySeq(back)
	for _, node := range session.Surface(back) {
		m := node.Message
		// 顺序与 serve_turn.assistantBlocks / fork.copyNode 一致：思考 → 正文 → 调用。
		for _, text := range reasoning[node.Seq] {
			if err := emit(agent.Event{
				Kind: agent.EventReasoning, Text: text, Turn: 1, Step: 1,
			}); err != nil {
				return err
			}
		}
		if m.Content != "" {
			kind := agent.EventText
			if m.Role == "user" {
				kind = agent.EventText
			}
			if err := emit(agent.Event{Kind: kind, Text: m.Content, Turn: 1, Step: 1}); err != nil {
				return err
			}
		}
		for _, tc := range m.ToolCalls {
			if err := emit(agent.Event{
				Kind: agent.EventToolCall, CallID: tc.ID, ToolName: tc.Function.Name,
				Arguments: tc.Function.Arguments, Turn: 1, Step: 1,
			}); err != nil {
				return err
			}
		}
	}
	return r.Resume(ctx, sessionID, cwd)
}

// Delete 删除一个会话的文件与内存状态。
//
// cwd 可能为空（SDK 的 deleteSession 只带 sessionId）：这时按会话 id 在
// `<home>/sessions/*/<segment>/` 下找，找到唯一一个就删。
func (r *acpRunner) Delete(ctx context.Context, sessionID, cwd string) error {
	var path string
	if cwd != "" {
		p, err := session.SessionPath(r.home, cwd, sessionID)
		if err != nil {
			return err
		}
		path = p
	} else {
		p, err := session.FindByID(r.home, sessionID)
		if err != nil {
			return err
		}
		path = p
	}
	// 只拒绝"删到 DSH 本体历史"这一种情况；路径里有 sessions 不算理由——
	// 我们自己的 home 本来就叫 sessions。
	if session.UnderDSHHome(path, os.Getenv("DSH_HOME")) {
		return fmt.Errorf("拒绝删除 DSH_HOME 下的会话：%s", path)
	}
	_ = r.Close(sessionID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if r.approvalsStore != nil {
		_ = r.approvalsStore.Delete(sessionID)
	}
	return nil
}

// SetMode 切换会话模式。未知模式明确报错，不静默接受。
//
// 解析走会话的 registry（内置 + 用户自建）：sandbox 档位是 mode 的一部分，
// 用户自建模式也必须能在这里选中，否则它的 sandbox 语义永远落不到执行口。
func (r *acpRunner) SetMode(ctx context.Context, sessionID, modeID string) (string, error) {
	m, err := r.registry().Resolve(modeID)
	if err != nil {
		return "", err
	}
	if _, err := m.SandboxMode(); err != nil {
		return "", err
	}
	r.mu.Lock()
	r.sessionModes[sessionID] = m.ID
	r.mu.Unlock()
	if err := r.registerSandbox(sessionID); err != nil {
		return "", err
	}
	return m.ID, nil
}

// modeOf 返回会话当前的模式（没设过就用默认）。
//
// 模式在会话内不变（PLAN §6）：换了模式，工具表会变，前缀缓存就废了。
// 所以这里只在 SetMode 显式切换时变，prompt 路径不碰。
func (r *acpRunner) modeOf(sessionID string) modes.Mode {
	r.mu.Lock()
	id := r.sessionModes[sessionID]
	r.mu.Unlock()
	if id == "" {
		id = r.modeID
	}
	m, err := r.registry().Resolve(id)
	if err != nil {
		m, _ = r.registry().Resolve(modes.DefaultID)
	}
	return m
}

// registry 返回模式表（内置 + 用户自建）。没配就只读内置。
func (r *acpRunner) registry() *modes.Registry {
	if r.modeReg != nil {
		return r.modeReg
	}
	reg, err := modes.NewRegistry(nil)
	if err != nil {
		// 内置模式自己冲突只可能是代码 bug；退化成只用内置。
		reg, _ = modes.NewRegistry(nil)
	}
	return reg
}

// approvalDecision 决定"这次要不要问"。
//
// 顺序是有讲究的：**模式的 deny 压过记忆**，但模式的 allow 压不过记忆里的拒绝
// （记忆里只有"允许"，所以实际上 allow 只是省一次问）。
//
// 为什么 deny 优先：模式是会话级策略（用户或宿主显式选的），
// 记忆是单个工具的便利。策略不该被便利覆盖。
func approvalDecision(modeApproval string, remembered bool) (allow bool, needAsk bool) {
	switch modeApproval {
	case "deny":
		return false, false // 直接拒，连问都不问
	case "allow":
		return true, false // 不问，直接放行
	}
	if remembered {
		return true, false
	}
	return false, true
}

// 关于工作目录：工具的根目录在**装配期**就定下来了（builtinModules(cwdOrDot(), …)），
// 所以 ACP 的 session/new 里带的 cwd 只决定会话落在哪个 projectKey 下，
// **不会**改工具解析相对路径的根。实测踩过：Martty 的 --workspace 指向某目录、
// 而 dshd 是从别的目录启动的，模型就会在自己的仓库里翻文件、报"文件不存在"。
//
// 结论（写进 docs）：**启动 dshd 时 cwd 就是工作区**。客户端要换工作区，
// 请从那个目录启动它（子进程继承 cwd），而不要指望 session/new 的 cwd 生效。
// ensureSession 在磁盘上把会话建出来（幂等：已存在就打开）。
//
// 抽出来是为了能直接测"模式有没有写进 header"——这条链路
// 靠 node 脚本验证太脆，用 Go 测试钉住更可靠。
// adoptWorkspace 把进程的工作区切到 want。
//
// 已经切到别处、而且那个会话还活着时不切——返回一个说得清的错误，
// 让客户端知道"一个 dshd 只能服务一个工作区"，而不是两个会话互相串台。
func (r *acpRunner) adoptWorkspace(want string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workspace == "" {
		r.workspace = want
	} else if !samePath(r.workspace, want) {
		return fmt.Errorf("这个 dshd 进程已经在工作区 %s；一个进程只服务一个工作区，"+
			"请为 %s 另起一个 dshd", r.workspace, want)
	}
	if r.fsRoot != nil {
		if err := r.fsRoot.SetRoot(want); err != nil {
			return fmt.Errorf("工作区不可用: %w", err)
		}
	}
	if r.sandbox != nil {
		// 策略层的工作区根必须和 FS 的根是同一条 canonical 路径。
		if err := r.sandbox.SetWorkspaceRoot(want); err != nil {
			return fmt.Errorf("工作区不可用: %w", err)
		}
	}
	if r.execCwd != nil {
		_ = r.execCwd.SetCwd(want)
	}
	return nil
}

func samePath(a, b string) bool {
	ca, erra := filepath.Abs(a)
	cb, errb := filepath.Abs(b)
	if erra != nil || errb != nil {
		return a == b
	}
	return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
}

func (r *acpRunner) ensureSession(sessionID, cwd string) (*session.Log, error) {
	// 客户端带来的工作目录要真的生效：否则它说"我在 A 目录"，工具却在宿主
	// 启动目录里干活（实测踩过：模型报"文件不存在"，然后在 203 个文件里翻，
	// 全是宿主仓库的文件）。
	//
	// 但工具的根是**进程级**的：一个 ACP 进程里同时开 A/B 两个会话时，
	// 后开的会把先开的那个的根顶掉——那不是"小瑕疵"，是让 A 会话突然在 B 里干活。
	// 所以这里宁可**明确拒绝**，也不静默串台。
	if want := strings.TrimSpace(cwd); want != "" {
		if err := r.adoptWorkspace(want); err != nil {
			return nil, err
		}
	}
	// 建会话之前就把档位钉到进程边界上：文件一存在，"这个会话是什么档位"
	// 就必须已经是确定的，不能留一个"先建完全权限、再切只读"的窗口。
	if err := r.registerSandbox(sessionID); err != nil {
		return nil, err
	}
	p, err := session.SessionPath(r.home, cwd, sessionID)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(p); statErr == nil {
		l, err := session.OpenSession(r.home, cwd, sessionID)
		if err == nil {
			logSessionStart(r.home, sessionID, l.Path, cwd, r.modeOf(sessionID).ID)
		}
		return l, err
	}
	l, err := session.CreateSession(r.home, cwd, sessionID, r.modeOf(sessionID).ID)
	if err != nil {
		return nil, fmt.Errorf("建会话失败: %w", err)
	}
	logSessionStart(r.home, sessionID, l.Path, cwd, r.modeOf(sessionID).ID)
	// 系统提示作为**耐久事件**落盘：DSH 退休 request/header.header.system 后的做法，
	// 这样 resume 之后模型仍然知道自己是谁。
	if err := l.Append("system/message", session.SystemMessageData(1, 1, r.system)); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// ListSessions 列磁盘上的会话（客户端会话面板用）。
//
// 先把自己刚建、可能还没落盘的会话补进去，避免"刚建的会话在面板里看不见"。
func (r *acpRunner) ListSessions(ctx context.Context, cwd string) ([]session.Listing, error) {
	list, err := session.ListSessions(r.home, cwd)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	known := make(map[string]bool, len(list))
	for _, l := range list {
		known[l.SessionID] = true
	}
	for id := range r.logs {
		if known[id] {
			continue
		}
		list = append([]session.Listing{{SessionID: id, Readable: true, Events: -1}}, list...)
	}
	r.mu.Unlock()
	return list, nil
}

// Shutdown 把所有还开着的会话日志关闭（别把没落盘的帧丢了）。
func (r *acpRunner) Shutdown() error {
	r.mu.Lock()
	logs := make([]*session.Log, 0, len(r.logs))
	for _, l := range r.logs {
		logs = append(logs, l)
	}
	r.logs = map[string]*session.Log{}
	r.mu.Unlock()

	var firstErr error
	for _, l := range logs {
		if err := l.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// approvalTitle 是客户端界面上那一行字。
//
// 很多 ACP 客户端只显示 title，所以它必须**先给命令**——
// 只写个 "bash" 等于什么都没说，用户是在盲签。
func approvalTitle(tool, args string) string {
	switch tool {
	case "bash":
		var a struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(args), &a); err == nil && a.Command != "" {
			return "bash: " + oneLine(a.Command, 120)
		}
	case "write", "edit":
		var a struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal([]byte(args), &a); err == nil && a.FilePath != "" {
			verb := "写入"
			if tool == "edit" {
				verb = "修改"
			}
			return verb + " " + a.FilePath
		}
	}
	return tool
}

// approvalKind 让客户端用对的语义（执行 / 编辑 / 其它）。
func approvalKind(tool string) string {
	switch tool {
	case "bash":
		return "execute"
	case "write", "edit":
		return "edit"
	}
	return "other"
}

// oneLine 把多行命令压成一行：title 只有一行的高度。
func oneLine(s string, n int) string {
	cr, lf := string(rune(13)), string(rune(10))
	s = strings.ReplaceAll(s, cr+lf, " ")
	s = strings.ReplaceAll(s, lf, " ; ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
