package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/acp"
	"dshgo/internal/agent"
	"dshgo/internal/compact"
	"dshgo/internal/kernel"
	"dshgo/internal/llm"
	"dshgo/internal/prompt"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// acpRunner 把 ACP 的 prompt 接到真实的 agent 循环与会话日志上。
type acpRunner struct {
	reg    spi.ToolRegistry
	runner spi.Runner
	base   string
	model  string
	home   string
	steps  int

	// ask 是审批闸门：返回 false 表示用户拒绝这次工具调用。
	ask func(ctx context.Context, sessionID, tool string, args string) (bool, error)
	// ctxWindow 用于 usage_update 的分母。
	ctxWindow int
	// system 是装配好的系统提示（会作为事件落盘）。
	system string
	// autoCompact 是自动压缩的 token 阈值（0 = 关闭）。
	autoCompact int
	// keepRecent 是自动压缩时保留的最近消息数。
	keepRecent int

	mu    sync.Mutex
	logs  map[string]*session.Log
	turns map[string]int
	hist  map[string][]llm.Message
	used  map[string]int
	meter map[string]*session.Meter
}

func newACPRunner(reg spi.ToolRegistry, runner spi.Runner, base, model, home string, steps int) *acpRunner {
	return &acpRunner{
		reg: reg, runner: runner, base: base, model: model, home: home, steps: steps,
		logs: map[string]*session.Log{}, turns: map[string]int{}, hist: map[string][]llm.Message{},
		used: map[string]int{}, ctxWindow: 128000, meter: map[string]*session.Meter{}, keepRecent: 8,
	}
}

func (r *acpRunner) Prompt(ctx context.Context, sessionID, cwd, text string, emit func(agent.Event) error) (string, map[string]any, error) {
	r.mu.Lock()
	logw := r.logs[sessionID]
	if logw == nil {
		l, err := session.CreateSession(r.home, cwd, sessionID, "standard")
		if err != nil {
			r.mu.Unlock()
			return "", nil, fmt.Errorf("建会话失败: %w", err)
		}
		// 系统提示作为**耐久事件**落盘：DSH 退休 request/header.header.system 后的做法，
		// 这样 resume 之后模型仍然知道自己是谁。
		if err := l.Append("system/message", session.SystemMessageData(1, 1, r.system)); err != nil {
			r.mu.Unlock()
			return "", nil, err
		}
		r.logs[sessionID] = l
		logw = l
	}
	r.turns[sessionID]++
	turn := r.turns[sessionID]
	hist := r.hist[sessionID]
	r.mu.Unlock()

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
	if err := logw.Append("user/message", session.UserMessage(user)); err != nil {
		return "", nil, err
	}

	var lastCallSeq int64
	var usageOut map[string]any
	sink := func(e agent.Event) error {
		// 先落盘（耐久），再上 wire（瞬态）。
		switch e.Kind {
		case agent.EventAssistantMsg:
			a := e.Assistant
			blocks := make([]map[string]any, 0, 1+len(a.ToolCalls))
			if a.Content != "" {
				blocks = append(blocks, session.TextBlock(a.Content))
			}
			for _, tc := range a.ToolCalls {
				blocks = append(blocks, session.ToolCallBlock(tc.ID, tc.Function.Name, tc.Function.Arguments))
			}
			var usage map[string]any
			if a.Usage != nil {
				usage = map[string]any{
					"inputTokens":  a.Usage.PromptTokens,
					"outputTokens": a.Usage.CompletionTokens,
					"totalTokens":  a.Usage.TotalTokens,
				}
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
				r.mu.Lock()
				r.used[sessionID] += a.Usage.TotalTokens
				used := r.used[sessionID]
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
				session.ToolResultEvent(e.Turn, e.Step, e.CallID, blocks, e.IsError, cwd, []int64{lastCallSeq})); err != nil {
				return err
			}
		case agent.EventTurnEnd:
			if err := logw.Append("turn/end", session.TurnEndData(turn, "completed", "")); err != nil {
				return err
			}
		}
		return emit(e)
	}

	var runner spi.Runner = r.runner
	if r.ask != nil {
		runner = &guardedRunner{inner: r.runner, reg: r.reg, ask: r.ask, sessionID: sessionID}
	}

	loop := &agent.Loop{
		Chat:     llm.New(r.base, os.Getenv("DSHD_LLM_KEY")),
		Tools:    r.reg,
		Runner:   runner,
		Project:  session.ProjectResult,
		Model:    r.model,
		System:   r.system,
		MaxSteps: r.steps,
		Emit:     sink,
	}

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
	inner     spi.Runner
	reg       spi.ToolRegistry
	ask       func(ctx context.Context, sessionID, tool string, args string) (bool, error)
	sessionID string
}

func (g *guardedRunner) Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
	if t, ok := g.reg.Get(name); ok && t.Caps().NeedsApproval() {
		ok, err := g.ask(ctx, g.sessionID, name, string(args))
		if err != nil {
			return spi.Result{Hint: "审批失败：客户端可能断开了；重试或改用只读工具"}, fmt.Errorf("审批失败: %w", err)
		}
		if !ok {
			return spi.Result{Hint: "用户拒绝了这次调用：换成只读方式确认，或先说明你要做什么再请求"},
				fmt.Errorf("用户拒绝了 %s", name)
		}
	}
	return g.inner.Run(ctx, name, args)
}

// Resume 从 v4 文件恢复历史，并以 append 模式续写同一个文件。
func (r *acpRunner) Resume(ctx context.Context, sessionID, cwd string) error {
	path, err := session.SessionPath(r.home, cwd, sessionID)
	if err != nil {
		return err
	}
	back, err := v4.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读会话失败: %w", err)
	}
	logw, err := session.OpenSession(r.home, cwd, sessionID)
	if err != nil {
		return err
	}
	hist := session.History(back)

	r.mu.Lock()
	if old := r.logs[sessionID]; old != nil {
		_ = old.Close()
	}
	r.logs[sessionID] = logw
	r.hist[sessionID] = hist
	r.mu.Unlock()
	return nil
}

func (r *acpRunner) Close(sessionID string) error {
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
	model := fs.String("model", envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash1017"), "模型 id")
	base := fs.String("base", envOr("DSHD_LLM_BASE", "http://127.0.0.1:3425/v1"), "OpenAI 兼容端点")
	home := fs.String("home", envOr("DSHD_HOME", "tmp/dshd-home"), "会话根目录")
	steps := fs.Int("max-steps", 8, "每轮最多几步")
	workspaceBytes := fs.Int("workspace-bytes", 8000, "AGENTS.md 进系统提示的字节上限（0=不传）")
	autoCompact := fs.Int("auto-compact", 40000, "surface 估算超过多少 token 就自动压缩（0=关闭）")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	mods, err := loadMods(*manifest)
	if err != nil {
		return err
	}
	k := kernel.New("dshd-acp")
	root := k.Root()
	defer root.Dispose()
	if _, err := kernel.Assemble(root, mods); err != nil {
		return err
	}
	reg, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		return err
	}
	runner, err := kernel.Inject(root, spi.ToolRunnerKey)
	if err != nil {
		return err
	}

	assembled := buildSystemPrompt(reg, *model, mustGetwd(), *workspaceBytes)
	acpRunner := newACPRunner(reg, runner, *base, *model, *home, *steps)
	acpRunner.system = assembled
	acpRunner.autoCompact = *autoCompact
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
			"toolCall": map[string]any{
				"toolCallId": "perm-" + session.NewID(),
				"title":      tool,
				"kind":       "other",
				"status":     "pending",
				"rawInput":   json.RawMessage(args),
			},
			"options": []any{
				map[string]any{"optionId": "allow-once", "name": "允许这一次", "kind": "allow_once"},
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
		return resp.Result.Outcome.Outcome == "selected" && resp.Result.Outcome.OptionID == "allow-once", nil
	}
	return srv.Serve(context.Background())
}

// buildSystemPrompt 用现有工具表装配系统提示（确定性：同输入同输出）。
func buildSystemPrompt(reg spi.ToolRegistry, model, cwd string, workspaceBytes int) string {
	return prompt.Default(prompt.Context{
		Model:             model,
		Cwd:               cwd,
		Platform:          runtime.GOOS + "/" + runtime.GOARCH,
		Date:              time.Now().Format("2006-01-02"),
		Tools:             reg.Tools(),
		MaxWorkspaceBytes: workspaceBytes,
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

	span, n, ok := compact.Plan(surface, compact.Options{
		KeepRecent: r.keepRecent, MinToCompact: 2,
	})
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
		span, n, ok = compact.Plan(surface, compact.Options{KeepRecent: keep, MinToCompact: 2})
		if !ok {
			return nil
		}
		fmt.Fprintf(os.Stderr, "超预算且无处可压：放宽到只保留最近 %d 条\n", keep)
	}
	items := make([]session.SurfaceMsg, 0, n)
	for _, sm := range surface {
		if sm.Seq >= span.Start && sm.Seq <= span.End {
			items = append(items, sm)
		}
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
