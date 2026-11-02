package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"

	"dshgo/internal/acp"
	"dshgo/internal/agent"
	"dshgo/internal/kernel"
	"dshgo/internal/llm"
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

	mu    sync.Mutex
	logs  map[string]*session.Log
	turns map[string]int
	hist  map[string][]llm.Message
}

func newACPRunner(reg spi.ToolRegistry, runner spi.Runner, base, model, home string, steps int) *acpRunner {
	return &acpRunner{
		reg: reg, runner: runner, base: base, model: model, home: home, steps: steps,
		logs: map[string]*session.Log{}, turns: map[string]int{}, hist: map[string][]llm.Message{},
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
		r.logs[sessionID] = l
		logw = l
	}
	r.turns[sessionID]++
	turn := r.turns[sessionID]
	hist := r.hist[sessionID]
	r.mu.Unlock()

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

	loop := &agent.Loop{
		Chat:     llm.New(r.base, os.Getenv("DSHD_LLM_KEY")),
		Tools:    r.reg,
		Runner:   r.runner,
		Project:  session.ProjectResult,
		Model:    r.model,
		System:   "你在 dsh-go 宿主里工作。需要信息就调用工具，不要猜。",
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

	srv := &acp.Server{
		In:      os.Stdin,
		Out:     os.Stdout,
		Agent:   newACPRunner(reg, runner, *base, *model, *home, *steps),
		Name:    "dsh-go-acp",
		Version: version,
	}
	return srv.Serve(context.Background())
}
