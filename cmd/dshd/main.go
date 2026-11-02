// Command dshd 是宿主入口：只做装配，不含业务分支（A3）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/kernel"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/modules/exec/bash"
	"dshgo/modules/fs/local"
	"dshgo/modules/tool/edit"
	"dshgo/modules/tool/glob"
	"dshgo/modules/tool/grep"
	"dshgo/modules/tool/read"
	"dshgo/modules/tool/write"
	"dshgo/modules/tools"
)

// 这三个由 -ldflags 覆盖（scripts/build.sh），默认值给 go run 用。
var (
	version   = "0.2.1-go.6"
	commit    = "dev"
	buildTime = "unknown"
)

// builtinModules 是编译进二进制的默认实现列表。
// 将来这里由 blank import + 注册表生成，形状不变。
func builtinModules() []kernel.Module {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return []kernel.Module{
		// 默认实现也是插件（A3）：它们与第三方插件走同一个 Register(scope)。
		tools.Module(tools.Options{}),
		bash.Module(wd, bash.Options{}),
		local.Module(wd),
		read.Module(),
		write.Module(),
		edit.Module(),
		glob.Module(),
		grep.Module(),
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "modules":
		err = cmdModules(os.Args[2:])
	case "exec":
		err = cmdExec(os.Args[2:])
	case "tools":
		err = cmdTools(os.Args[2:])
	case "session":
		err = cmdSession(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "acp":
		err = cmdACP(os.Args[2:])
	case "version":
		fmt.Printf("dshd %s\n", version)
		fmt.Printf("commit: %s\n", commit)
		fmt.Printf("built:  %s\n", buildTime)
		fmt.Printf("go:     %s\n", runtime.Version())
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "dshd: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dshd —— dsh-go 宿主

用法:
  dshd modules graph [--manifest dsh.toml]   打印装配图（按能力排序后的激活顺序）
  dshd modules list  [--manifest dsh.toml]   列出可装配模块
  dshd exec --tool <name> --args <json>      装配后执行一个工具
  dshd tools [--manifest dsh.toml]           打印模型看到的工具表（JSON）
  dshd session rewrite --in <f> --out <f>     把 v4 会话读进来再用我们的编码器写出去（验证兼容用）
  dshd run --prompt "<话>" [--max-steps N]    跑一个真实 turn（模型 + 工具 + 结果回灌）
  dshd acp                                    在 stdin/stdout 上跑 ACP 服务端（stdout 只放协议）
  dshd version                               版本
`)
}

func loadMods(manifestPath string) ([]kernel.Module, error) {
	mods := builtinModules()
	m, err := kernel.LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	return m.Apply(mods)
}

func cmdModules(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("用法: dshd modules graph|list")
	}
	fs := flag.NewFlagSet("modules", flag.ContinueOnError)
	manifest := fs.String("manifest", "dsh.toml", "manifest 路径")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	mods, err := loadMods(*manifest)
	if err != nil {
		return err
	}
	switch argv[0] {
	case "list":
		fmt.Printf("%-22s %-9s %-26s %s\n", "MODULE", "BACKEND", "PROVIDES", "REQUIRES")
		for _, m := range mods {
			fmt.Printf("%-22s %-9s %-26s %s\n", m.ID, backendOf(m), join(m.Provides), join(m.Requires))
		}
		return nil
	case "graph":
		order, err := kernel.TopoSort(mods)
		if err != nil {
			return err
		}
		plan := &kernel.Plan{Order: order}
		fmt.Printf("# 激活顺序（由能力依赖决定，不是 manifest 行顺序）\n")
		plan.Write(os.Stdout)
		return nil
	default:
		return fmt.Errorf("未知子命令 modules %s", argv[0])
	}
}

// ToolEntry 是模型看到的工具表里的一行（R4：这张表属于稳定性契约）。
type ToolEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// Caps 是能力声明：审批与沙箱都靠它，所以要进快照（改了必须显式确认）。
	Caps spi.Caps `json:"caps"`
}

// buildToolTable 按名字排序，保证同一装配产生同样的字节。
func buildToolTable(reg spi.ToolRegistry) ([]ToolEntry, error) {
	tools := reg.Tools()
	entries := make([]ToolEntry, 0, len(tools))
	for _, t := range tools {
		if !json.Valid(t.Schema()) {
			return nil, fmt.Errorf("工具 %q 的 schema 不是合法 JSON", t.Name())
		}
		entries = append(entries, ToolEntry{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
			Caps:        t.Caps(),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func cmdTools(argv []string) error {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	manifest := fs.String("manifest", "dsh.toml", "manifest 路径")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	mods, err := loadMods(*manifest)
	if err != nil {
		return err
	}
	k := kernel.New("dshd")
	root := k.Root()
	defer root.Dispose()
	if _, err := kernel.Assemble(root, mods); err != nil {
		return err
	}
	reg, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		return err
	}
	table, err := buildToolTable(reg)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "工具 %d 个，%d 字节\n", len(table), len(out))
	return nil
}

// cmdRun 跑一个真实 turn：装配 → 模型 → 工具 → 回灌 → 收尾。
//
// 这是 M2 的验收动作：它同时踩到装配、工具流水线、LLM 流式、agent 双循环四条线。
func cmdRun(argv []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	manifest := fs.String("manifest", "dsh.toml", "manifest 路径")
	prompt := fs.String("prompt", "", "用户输入")
	model := fs.String("model", envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash1017"), "模型 id")
	base := fs.String("base", envOr("DSHD_LLM_BASE", "http://127.0.0.1:3425/v1"), "OpenAI 兼容端点")
	maxSteps := fs.Int("max-steps", 8, "最多几步")
	home := fs.String("home", envOr("DSHD_HOME", "tmp/dshd-home"), "会话根目录")
	workspaceBytes := fs.Int("workspace-bytes", 8000, "AGENTS.md 进系统提示的字节上限（0=不传）")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *prompt == "" {
		return fmt.Errorf("缺 --prompt")
	}

	mods, err := loadMods(*manifest)
	if err != nil {
		return err
	}
	k := kernel.New("dshd")
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

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	sys := buildSystemPrompt(reg, *model, cwd, *workspaceBytes)
	sessID := "session-" + session.NewID()
	logw, err := session.CreateSession(*home, cwd, sessID, "standard")
	if err != nil {
		return err
	}
	defer logw.Close()
	if err := logw.Append("system/message", session.SystemMessageData(1, 1, sys)); err != nil {
		return err
	}
	if err := logw.Append("turn/start", session.TurnStartData(1)); err != nil {
		return err
	}
	if err := logw.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: *prompt}})); err != nil {
		return err
	}
	var lastCallSeq int64

	loop := &agent.Loop{
		Chat:     llm.New(*base, os.Getenv("DSHD_LLM_KEY")),
		Tools:    reg,
		Runner:   runner,
		Project:  session.ProjectResult,
		Model:    *model,
		System:   sys,
		MaxSteps: *maxSteps,
		Emit: func(e agent.Event) error {
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
				}
				return logw.Append("assistant/message",
					session.AssistantMessageEvent(e.Turn, e.Step, blocks, a.Finish, usage))
			case agent.EventToolCall:
				lastCallSeq = logw.Seq()
				if err := logw.Append("tool/call",
					session.ToolCallEvent(e.Turn, e.Step, e.CallID, e.ToolName, e.Arguments)); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "  → %s %s\n", e.ToolName, truncate(e.Arguments, 120))
			case agent.EventToolResult:
				code := "ok"
				if e.IsError {
					code = "ERR"
				}
				fmt.Fprintf(os.Stderr, "  ← %s [%s]\n", e.ToolName, code)
				var blocks []spi.Block
				if e.Result != nil {
					blocks = session.ProjectResult(*e.Result)
				} else {
					blocks = []spi.Block{{Type: "text", Text: "（无结果）"}}
				}
				return logw.Append("tool/result",
					session.ToolResultEvent(e.Turn, e.Step, e.CallID, blocks, e.IsError, cwd, []int64{lastCallSeq}))
			case agent.EventTurnEnd:
				return logw.Append("turn/end", session.TurnEndData(e.Turn, "completed", ""))
			}
			return nil
		},
	}

	hist, err := loop.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: *prompt}})
	if err != nil {
		return err
	}
	if err := logw.Close(); err != nil {
		return err
	}
	last := hist[len(hist)-1]
	fmt.Println(last.Content)
	fmt.Fprintf(os.Stderr, "会话：%s（%d 事件）\n", logw.Path, logw.Seq())
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// cmdSession 目前只有一个子命令 rewrite：读→写，用来验证我们的编码器，
// 也用来给 Node 侧的 verify-node-reads.mjs 造样本。
func cmdSession(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("用法: dshd session rewrite|compact ...")
	}
	if argv[0] == "compact" {
		return cmdSessionCompact(argv[1:])
	}
	if argv[0] == "info" {
		return cmdSessionInfo(argv[1:])
	}
	if argv[0] != "rewrite" {
		return fmt.Errorf("用法: dshd session rewrite --in <file> --out <file>")
	}
	fs := flag.NewFlagSet("session rewrite", flag.ContinueOnError)
	in := fs.String("in", "", "源会话文件")
	out := fs.String("out", "", "目标会话文件")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return fmt.Errorf("--in 与 --out 都必须给")
	}
	l, err := v4.ReadFile(*in)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(*out); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	w, err := v4.Create(*out, l.Header)
	if err != nil {
		return err
	}
	defer w.Close()
	for i := 0; i < len(l.Events); i += 20 {
		end := i + 20
		if end > len(l.Events) {
			end = len(l.Events)
		}
		if err := w.Append(l.Events[i:end]...); err != nil {
			return err
		}
	}
	fmt.Printf("重写完成：%d 事件 / %d 帧 → %s\n", len(l.Events), w.Frames(), *out)

	return nil
}

func cmdExec(argv []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	manifest := fs.String("manifest", "dsh.toml", "manifest 路径")
	toolName := fs.String("tool", "", "工具名")
	rawArgs := fs.String("args", "{}", "工具参数 JSON")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *toolName == "" {
		return fmt.Errorf("缺 --tool")
	}
	mods, err := loadMods(*manifest)
	if err != nil {
		return err
	}
	k := kernel.New("dshd")
	root := k.Root()
	defer root.Dispose()
	if _, err := kernel.Assemble(root, mods); err != nil {
		return err
	}
	runner, err := kernel.Inject(root, tools.RunKey)
	if err != nil {
		return err
	}
	res, err := runner.Run(root.Context(), *toolName, json.RawMessage(*rawArgs))
	if err != nil {
		fmt.Fprintf(os.Stderr, "执行失败: %v\n", err)
		if res.Hint != "" {
			fmt.Fprintf(os.Stderr, "提示: %s\n", res.Hint)
		}
		return err
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	return nil
}

func backendOf(m kernel.Module) string {
	if m.Backend == "" {
		return string(kernel.BackendStatic)
	}
	return string(m.Backend)
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// cmdSessionCompact 往一个已有会话里**追加**压缩事件（日志 append-only，不删任何东西）。
func cmdSessionCompact(argv []string) error {
	fs := flag.NewFlagSet("session compact", flag.ContinueOnError)
	path := fs.String("in", "", "会话文件（原地追加）")
	from := fs.Int64("from", 0, "遮蔽区间起点 seq")
	to := fs.Int64("to", -1, "遮蔽区间终点 seq")
	summary := fs.String("summary", "", "摘要正文")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *path == "" || *to < 0 || *summary == "" {
		return fmt.Errorf("--in / --to / --summary 都必须给")
	}
	before, err := v4.ReadFile(*path)
	if err != nil {
		return err
	}
	l, err := session.OpenSessionAt(*path)
	if err != nil {
		return err
	}
	id, shadowed, err := session.Compact(l, before.Events, session.Span{Start: *from, End: *to},
		[]spi.Block{{Type: "text", Text: *summary}},
		session.CompactionMeta{Provider: "cli", Model: "manual"})
	if err != nil {
		l.Close()
		return err
	}
	if err := l.Close(); err != nil {
		return err
	}
	fmt.Printf("压缩完成：%s 遮蔽 %d 条事件（日志 %d → %d）\n",
		id, len(shadowed), len(before.Events), l.Seq())
	return nil
}

// cmdSessionInfo 打印一份会话的"账本面"：原始事件与 surface 各多少。
func cmdSessionInfo(argv []string) error {
	fs := flag.NewFlagSet("session info", flag.ContinueOnError)
	path := fs.String("in", "", "会话文件")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("--in 必填")
	}
	l, err := v4.ReadFile(*path)
	if err != nil {
		return err
	}
	byType := map[string]int{}
	for _, ev := range l.Events {
		byType[ev.Type]++
	}
	surface := session.Surface(l)
	summaries := 0
	for _, n := range surface {
		if n.Summary {
			summaries++
		}
	}
	fmt.Printf("文件: %s\n", *path)
	fmt.Printf("会话: %s（cwd=%s）\n", l.Header.ID, l.Header.Cwd)
	fmt.Printf("事件: %d（%d 帧）\n", len(l.Events), l.Frames)
	fmt.Printf("surface: %d 节点，其中摘要 %d\n", len(surface), summaries)
	keys := make([]string, 0, len(byType))
	for k := range byType {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-22s %d\n", k, byType[k])
	}
	return nil
}
