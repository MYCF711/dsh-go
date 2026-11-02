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
	// depth 是当前嵌套深度：0 表示主代理。子代理内部再调 subagent 会被拒。
	depth int
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
	}

	hist, err := loop.Run(ctx, 1, []llm.Message{{Role: "user", Content: prompt}})
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
	return spi.SubagentResult{
		Text:  text,
		Steps: len(hist),
		// 说清楚这次用的是谁的授权：runner 是主会话那个 guarded，
		// 审批记忆按会话记——主会话批准过的工具，子代理不会重问。
		BorrowedApprovals: s.runner != nil,
	}, nil
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
