// Command dshd 是宿主入口：只做装配，不含业务分支（A3）。
package main

import (
	"bufio"
	"context"
	"dshgo/internal/approval"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/compact"
	"dshgo/internal/kernel"
	"dshgo/internal/llm"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/modules/exec/bash"
	"dshgo/modules/fs/local"
	"dshgo/modules/search"
	"dshgo/modules/tool/ask"
	"dshgo/modules/tool/edit"
	"dshgo/modules/tool/execlit"
	"dshgo/modules/tool/github"
	"dshgo/modules/tool/glob"
	goalmod "dshgo/modules/tool/goal"
	"dshgo/modules/tool/grep"
	"dshgo/modules/tool/history"
	"dshgo/modules/tool/nowline"
	"dshgo/modules/tool/present"
	"dshgo/modules/tool/pwsh"
	"dshgo/modules/tool/read"
	"dshgo/modules/tool/readimage"
	"dshgo/modules/tool/repohere"
	schedule "dshgo/modules/tool/schedule"
	subagenttool "dshgo/modules/tool/subagent"
	team "dshgo/modules/tool/team"
	teamtask "dshgo/modules/tool/teamtask"
	todo "dshgo/modules/tool/todo"
	wdtool "dshgo/modules/tool/wd"
	"dshgo/modules/tool/webfetch"
	"dshgo/modules/tool/websearch"
	"dshgo/modules/tool/write"
	"dshgo/modules/tools"
)

// 这三个由 -ldflags 覆盖（scripts/build.sh），默认值给 go run 用。
var (
	version   = "0.2.1-go.27"
	commit    = "dev"
	buildTime = "unknown"
)

// builtinModules 是编译进二进制的默认实现列表。
// 将来这里由 blank import + 注册表生成，形状不变。
// 历史检索服务是宿主提供的（它需要知道"当前是哪个会话"）。
var hostHistory = &historyHolder{}

// builtinModules 按工作区与 home 装配默认实现。
// artifacts 目录要放行给 FS 读：工具输出被外置成 artifact 之后，
// Hint 会告诉模型「用 read 读全文」——不放行那句 Hint 就是假的。
func builtinModules(wd, home string, maxChars int, sub *subProvider) []kernel.Module {
	if sub == nil {
		sub = &subProvider{} // 没绑定：工具会明确报「还没装配好」，不会假装能派
	}
	return []kernel.Module{
		// 默认实现也是插件（A3）：它们与第三方插件走同一个 Register(scope)。
		tools.Module(tools.Options{
			// artifact 落在 home 下（不是系统 temp）：FS 只放行了这个目录，
			// 放 temp 里模型读不到自己刚被外置的结果。
			SpillDir: filepath.Join(home, "artifacts"),
			MaxChars: maxChars,
		}),
		bash.Module(wd, bash.Options{}),
		local.Module(wd, local.WithExtraReadRoot(filepath.Join(home, "artifacts"))),
		read.Module(),
		write.Module(),
		edit.Module(),
		glob.Module(),
		grep.Module(),
		historyModule(hostHistory),
		search.Module(),
		history.Module(),
		wdtool.Module(),
		todo.Module(),
		present.Module(),
		ask.Module(),
		webfetch.Module(),
		websearch.Module(),
		readimage.Module(),
		pwsh.Module(),
		github.Module(),
		repohere.Module(),
		execlit.Module(),
		teamtask.Module(filepath.Join(home, "state")),
		team.Module(filepath.Join(home, "state", "mail")),
		goalmod.Module(filepath.Join(home, "state", "goals")),
		schedule.Module(filepath.Join(home, "state", "schedules")),
		nowline.Module(),
		subagentModule(sub),
		subagenttool.Module(),
	}
}

func main() {
	timingf("main 开始")
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
	case "schedule":
		err = cmdSchedule(os.Args[2:])
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "version":
		timingf("enter version")
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
  dshd doctor [--home X] [--base URL]        自检：home/会话/写锁/网关/模块，每项给下一步
  dshd version                               版本
`)
}

// defaultHome 是子命令（modules/tools/exec）用的 home：
// 它们没有 --home 旗标，但 artifact 目录要有一个确定位置。
func defaultHome() string {
	if v := os.Getenv("DSHD_HOME"); v != "" {
		return v
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".dshd"
	}
	return filepath.Join(h, ".dshd")
}

func cwdOrDot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func loadMods(manifestPath, home string, maxChars int, sub *subProvider) ([]kernel.Module, error) {
	mods := builtinModules(cwdOrDot(), home, maxChars, sub)
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
	mods, err := loadMods(*manifest, defaultHome(), 0, nil)
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
	mods, err := loadMods(*manifest, defaultHome(), 0, nil)
	if err != nil {
		return err
	}
	timingf("run: manifest 已读")
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
	maxChars := fs.Int("max-chars", 4000, "工具输出超过多少字符就外置成 artifact（越小越省 token，越大越少重读）")
	workspaceBytes := fs.Int("workspace-bytes", 8000, "AGENTS.md 进系统提示的字节上限（0=不传）")
	resumeSession := fs.String("session", "", "续这个会话（空=新建）；cwd 可省，按 id 找")
	// 规则文件：默认工作区的 AGENTS.md；给"执行者"喂精简手册时指到 docs/WORKER.md。
	fs.StringVar(rulesPath, "agents", "", "规则文件路径（默认工作区 AGENTS.md）")
	policy := fs.String("policy", "auto", "审批策略：ask/allow/deny/auto（auto=终端问、非终端拒）")
	autoCompact := fs.Int("auto-compact", 40000, "每步前估算超过多少 token 就自动压缩（0=关闭）")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *prompt == "" {
		return fmt.Errorf("缺 --prompt")
	}
	if err := checkHome(*home); err != nil {
		return err
	}

	// 从这里开始，所有 slog 落到 <home>/logs/<日期>.log。
	// 它不替代 stderr——只是让"事后能查"这件事成立。
	// 拿不到日志时**明说**：静默降级会让人以为有记录，出事才发现是空的。
	if d := diagOrWarn(*home); d != nil {
		defer d.Close()
		defer func() {
			if d.Path != "" {
				fmt.Fprintf(os.Stderr, "日志: %s"+string(rune(10)), d.Path)
			}
		}()
	}

	subp := &subProvider{}
	mods, err := loadMods(*manifest, *home, *maxChars, subp)
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
	// --session：续一个已经存在的会话，而不是新建。
	// 定时任务的"到期后跑一轮"就是靠这个——它必须落在**原会话**里，
	// 新建会话会让上下文断掉（模型不记得之前聊过什么）。
	sessID := *resumeSession
	var logw *session.Log
	if sessID == "" {
		sessID = "session-" + session.NewID()
		logw, err = session.CreateSession(*home, cwd, sessID, modes.DefaultID)
	} else {
		var path string
		path, err = session.ResolvePath(*home, cwd, sessID)
		if err == nil {
			logw, err = session.OpenSessionAt(path)
		}
	}
	if err != nil {
		return err
	}
	defer logw.Close()

	// 续会话时必须把**已有历史**读进来，否则模型看到的是一张白纸：
	// 它会在旧日志上追加新消息，却不知道前面聊过什么（实测会回答"这是第一条消息"）。
	var baseHist []llm.Message
	if *resumeSession != "" {
		if back, rerr := v4.ReadFile(logw.Path); rerr == nil {
			baseHist = session.History(back)
		}
	}
	logSessionStart(*home, sessID, logw.Path, cwd, modes.DefaultID)
	// 把"当前会话"指给 history_search——`run` 以前没这一步，模型回查只会得到
	// "没有活动会话"。ACP 那边早就指了，这里补上。
	attachHistory(logw)

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
	// 一轮的累计账（给 stats 行用）
	var toolCalls, inTokens, outTokens int

	// 审批守卫：`dshd run` 原来对写类工具是完全放行的，现在按 --policy 判定。
	askFn, err := makeAskFunc(*policy)
	if err != nil {
		return err
	}
	guarded := &guardedRunner{
		inner: runner, reg: reg, ask: askFn,
		sessionID: sessID, approvals: approval.New(),
	}

	chat := llm.New(*base, os.Getenv("DSHD_LLM_KEY"))
	// meter 跨步复用：每步都用服务端给的 totalTokens 校准一次估算。
	// 以前每步新建一个空 meter，等于永远按启发式比例猜。
	meter := &session.Meter{}
	// 子代理复用同一套零件：同一个模型、同一张工具表、同一个执行器。
	// 差别只有系统提示与步数上限（它只干一件小事）。
	subp.Bind(&subRunner{
		chat: chat, tools: reg, runner: guarded,
		model: *model, maxSteps: 8, timeout: 5 * time.Minute,
	})
	loop := &agent.Loop{
		Chat:      chat,
		Compact:   runCompactor(chat, logw, *autoCompact, *model, meter),
		Tools:     reg,
		Runner:    guarded,
		Project:   session.ProjectResult,
		Model:     *model,
		System:    sys,
		SessionID: sessID,
		MaxSteps:  *maxSteps,
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
					inTokens += a.Usage.PromptTokens
					outTokens += a.Usage.CompletionTokens
					// 用服务端实测值校准估算器：下一轮压缩判断才有锚点。
					if back, err := v4.ReadFile(logw.Path); err == nil {
						meter.Observe(a.Usage.TotalTokens, meter.Estimate(session.Surface(back)))
					}
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
				toolCalls++
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

	// 一轮的账：工具调用次数与 token。
	// 实测每多一次工具调用约多 5–6k token（docs/token-budget.md §6），
	// 所以 tool_calls 比"输出多长"更值得盯。
	//
	// 用 defer：**失败的那一轮才最需要看账**（步数用满、一直调工具）。
	// 放在成功分支里的话，恰好把最贵的那些跑法排除在统计外。
	defer func() {
		fmt.Fprintf(os.Stderr, "stats: tool_calls=%d input=%d output=%d\n",
			toolCalls, inTokens, outTokens)
	}()

	hist := append(append([]llm.Message(nil), baseHist...), llm.Message{Role: "user", Content: *prompt})
	hist, err = loop.Run(context.Background(), 1, hist)
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
	if argv[0] == "fork" {
		return cmdSessionFork(argv[1:])
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
	mods, err := loadMods(*manifest, defaultHome(), 0, nil)
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
	summary := fs.String("summary", "", "摘要正文（人工给）")
	auto := fs.Bool("auto", false, "自动选区间并用模型生成摘要")
	keep := fs.Int("keep", 6, "--auto 时保留最近多少条")
	maxMessages := fs.Int("max", 0, "--auto 时表面超过多少条才触发（0=不设）")
	model := fs.String("model", envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash1017"), "摘要模型")
	base := fs.String("base", envOr("DSHD_LLM_BASE", "http://127.0.0.1:3425/v1"), "OpenAI 兼容端点")
	dryRun := fs.Bool("dry-run", false, "只打印计划，不写盘")
	force := fs.Bool("force", false, "确认要改动 sessions 目录下的文件")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("--in 必填")
	}
	if ok, reason := session.HistoryGuard(*path, os.Getenv("DSH_HOME"), *force); !ok {
		hint := "（复制一份再操作，别动别人的历史）"
		if !*force {
			hint = "；确认无误加 --force"
		}
		return fmt.Errorf("%s%s", reason, hint)
	}
	if !*auto && (*to < 0 || *summary == "") {
		return fmt.Errorf("要么给 --from/--to/--summary，要么用 --auto")
	}
	before, err := v4.ReadFile(*path)
	if err != nil {
		return err
	}
	if *auto {
		surface := session.Surface(before)
		span, n, ok := compact.Plan(surface, compact.Options{
			KeepRecent: *keep, MinToCompact: 2, MaxMessages: *maxMessages,
		})
		if !ok {
			fmt.Printf("无需压缩：surface %d 节点，保留最近 %d 条后没有可压的前缀\n", len(surface), *keep)
			return nil
		}
		fmt.Printf("计划：压缩 %d 条（seq %d..%d），保留最近 %d 条\n",
			n, span.Start, span.End, *keep)
		if *dryRun {
			return nil
		}
		items := make([]session.SurfaceMsg, 0, n)
		for _, m := range surface {
			if m.Seq >= span.Start && m.Seq <= span.End {
				items = append(items, m)
			}
		}
		text, err := compact.Summarize(context.Background(), llm.New(*base, os.Getenv("DSHD_LLM_KEY")), *model, items)
		if err != nil {
			return err
		}
		*summary = text
		*from = span.Start
		*to = span.End
		fmt.Printf("摘要 %d 字\n", len([]rune(text)))
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
	mode := l.Header.AgentPreset
	if mode == "" {
		mode = "（未记）"
	}
	fmt.Printf("会话: %s（cwd=%s，mode=%s）\n", l.Header.ID, l.Header.Cwd, mode)
	fmt.Printf("事件: %d（%d 帧）\n", len(l.Events), l.Frames)
	if l.TailTruncatedAt > 0 {
		// 不能装作没事：用户要知道这份日志的尾巴是坏的（断电/磁盘满留下的半帧）。
		fmt.Printf("⚠ 尾部截断: 偏移 %d 之后读不动了（上面的内容照常可用）\n", l.TailTruncatedAt)
	}
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

// cmdSessionFork 从一个会话派生新会话（继承 surface，不继承审批记忆）。
func cmdSessionFork(argv []string) error {
	fs := flag.NewFlagSet("session fork", flag.ContinueOnError)
	in := fs.String("in", "", "源会话文件")
	home := fs.String("home", envOr("DSHD_HOME", "tmp/dshd-home"), "新会话写到哪里")
	cwd := fs.String("cwd", "", "新会话的工作目录（默认沿用源会话）")
	id := fs.String("id", "", "新会话 id（默认自动生成）")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("--in 必填")
	}
	src, err := v4.ReadFile(*in)
	if err != nil {
		return err
	}
	targetCwd := *cwd
	if targetCwd == "" {
		targetCwd = src.Header.Cwd
	}
	newID := *id
	if newID == "" {
		newID = "session-" + session.NewID()
	}
	forked, err := session.Fork(src, *home, targetCwd, newID)
	if err != nil {
		return err
	}
	if err := forked.Close(); err != nil {
		return err
	}
	back, err := v4.ReadFile(forked.Path)
	if err != nil {
		return err
	}
	surface := session.Surface(back)
	summaries := 0
	for _, n := range surface {
		if n.Summary {
			summaries++
		}
	}
	fmt.Printf("派生完成：%s\n", forked.Path)
	fmt.Printf("  源会话: %s（parentSession 已写入 header）\n", src.Header.ID)
	fmt.Printf("  surface: %d 节点（摘要 %d）/ 事件 %d\n", len(surface), summaries, len(back.Events))
	return nil
}

// makeAskFunc 把 --policy 变成 guardedRunner 用的判定函数。
//
// auto 的语义：终端里有人就问，非终端（管道 / CI / 脚本）一律拒绝——
// 没人能回答的时候，"默认同意"是最坏的选择。
func makeAskFunc(policy string) (func(ctx context.Context, sessionID, tool, args string) (bool, error), error) {
	return makeAskFuncWith(policy, isTerminal(os.Stdin))
}

// makeAskFuncWith 把"是不是终端"显式传进来，方便测试两个分支。
func makeAskFuncWith(policy string, interactive bool) (func(ctx context.Context, sessionID, tool, args string) (bool, error), error) {
	switch policy {
	case "allow":
		return func(context.Context, string, string, string) (bool, error) { return true, nil }, nil
	case "deny":
		return func(context.Context, string, string, string) (bool, error) { return false, nil }, nil
	case "ask", "auto":
		if policy == "auto" && !interactive {
			return func(context.Context, string, string, string) (bool, error) { return false, nil }, nil
		}
		return func(ctx context.Context, sessionID, tool, args string) (bool, error) {
			// 展示的是"要执行什么"，不是模型对它的描述（见 approval_view.go）。
			q := string(rune(10))
			body := strings.ReplaceAll(describeApproval(tool, args), q, q+"       ")
			fmt.Fprintf(os.Stderr, "需要审批：%s%s       %s%s       → 允许？[y/N] ", tool, q, body, q)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			line = strings.ToLower(strings.TrimSpace(line))
			return line == "y" || line == "yes", nil
		}, nil
	default:
		return nil, fmt.Errorf("未知 --policy %q（可选 ask/allow/deny/auto）", policy)
	}
}

// isTerminal 判断是不是交互终端（Windows 上用 CharacterDevice 位）。
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// runCompactor 给 `dshd run` 提供每步前的自动压缩。
//
// 和 ACP 那条用的是同一套：读 log → Surface → compact.Select → Summarize →
// session.Compact 写事件 → 从日志重建历史。区别只在于触发时机（每步 vs 每轮）。
func runCompactor(chat agent.Chat, logw *session.Log, budget int, model string, meter *session.Meter) func(context.Context, int, []llm.Message) ([]llm.Message, error) {
	if budget <= 0 {
		return nil
	}
	return func(ctx context.Context, step int, hist []llm.Message) ([]llm.Message, error) {
		back, err := v4.ReadFile(logw.Path)
		if err != nil {
			return hist, nil // 读不了就别拦着干活
		}
		surface := session.Surface(back)
		// 用**带实测锚点**的估算。run 这条路径以前每步新建一个空 meter，
		// 等于永远用启发式比例猜——长会话里猜偏了就会要么该压不压、要么提前压。
		if meter == nil {
			meter = &session.Meter{}
		}
		if meter.Estimate(surface) <= budget {
			return nil, nil // 没超预算，历史不动
		}
		// KeepRecent 是下限（至少留 6 条），真正的保留量由 KeepTokens 定。
		// run 这条路径不暴露 token 预算：默认 2000 够用，超了也还有下面的兜底。
		items, span, ok := compact.Select(surface, compact.Options{KeepRecent: 6, MinToCompact: 2})
		if !ok {
			// 已经超预算，但"预算内能留的"就是全部——这时必须让步：
			// 只按最小条数留，剩下的都压掉。宁可摘要粗，也不能让这一步超窗。
			items, span, ok = compact.Select(surface, compact.Options{KeepRecent: 2, KeepTokens: 1, MinToCompact: 2})
		}
		if !ok {
			return nil, nil // 真没得压
		}
		text, err := compact.Summarize(ctx, chat, model, items)
		if err != nil {
			fmt.Fprintf(os.Stderr, "自动压缩失败（不影响本轮）：%v\n", err)
			return nil, nil
		}
		if _, _, err := session.Compact(logw, back.Events, span,
			[]spi.Block{{Type: "text", Text: text}},
			session.CompactionMeta{Provider: "dshd", Model: "run"}); err != nil {
			fmt.Fprintf(os.Stderr, "写压缩事件失败（不影响本轮）：%v\n", err)
			return nil, nil
		}
		after, err := v4.ReadFile(logw.Path)
		if err != nil {
			return nil, nil
		}
		fmt.Fprintf(os.Stderr, "自动压缩（第 %d 步）：surface %d → %d 节点\n",
			step, len(surface), len(session.Surface(after)))
		return session.History(after), nil
	}
}

// checkHome 在建任何东西之前先把 --home 看一眼。
//
// 实测踩过：`--home tmp/afile/home`（afile 是个普通文件）报的是
//
//	mkdir tmpfile: The system cannot find the path specified.
//
// 用户看不出到底是"作业目录本身有问题"还是"权限不够"。
func checkHome(home string) error {
	st, err := os.Stat(home)
	if err != nil {
		return nil // 不存在没关系，建的时候会说
	}
	if !st.IsDir() {
		return fmt.Errorf("--home %q 是一个文件，不是目录；换一个目录路径（或先删掉它）", home)
	}
	return nil
}
