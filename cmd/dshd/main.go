// Command dshd 是宿主入口：只做装配，不含业务分支（A3）。
package main

import (
	"bufio"
	"context"
	"dshgo/internal/approval"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	actioncardsmod "dshgo/modules/tool/actioncards"
	"dshgo/modules/tool/ask"
	"dshgo/modules/tool/edit"
	"dshgo/modules/tool/execlit"
	"dshgo/modules/tool/github"
	"dshgo/modules/tool/glob"
	goalmod "dshgo/modules/tool/goal"
	"dshgo/modules/tool/grep"
	"dshgo/modules/tool/hindsight"
	"dshgo/modules/tool/history"
	"dshgo/modules/tool/nowline"
	officetool "dshgo/modules/tool/office"
	planmod "dshgo/modules/tool/plan"
	pluginmod "dshgo/modules/tool/plugin"
	"dshgo/modules/tool/present"
	"dshgo/modules/tool/pwsh"
	"dshgo/modules/tool/read"
	"dshgo/modules/tool/readimage"
	"dshgo/modules/tool/repohere"
	schedule "dshgo/modules/tool/schedule"
	"dshgo/modules/tool/skill"
	subagenttool "dshgo/modules/tool/subagent"
	subagentfork "dshgo/modules/tool/subagentfork"
	team "dshgo/modules/tool/team"
	teamtask "dshgo/modules/tool/teamtask"
	"dshgo/modules/tool/terminalpanel"
	todo "dshgo/modules/tool/todo"
	unlagmod "dshgo/modules/tool/unlag"
	watch "dshgo/modules/tool/watch"
	wdtool "dshgo/modules/tool/wd"
	"dshgo/modules/tool/webfetch"
	"dshgo/modules/tool/websearch"
	workflowmod "dshgo/modules/tool/workflow"
	"dshgo/modules/tool/write"
	wsdeps "dshgo/modules/tool/wsdeps"
	"dshgo/modules/tools"
)

// 这三个由 -ldflags 覆盖（scripts/build.sh），默认值给 go run 用。
var (
	version   = "0.2.2-go.3"
	commit    = "dev"
	buildTime = "unknown"
)

// productName 是**面向用户的产品名**，用于 `--version` 首词和版本资源。
//
// 新宿主产物按用户要求命名为 `dsh-<版本号>.exe`（构建脚本 PRODUCTS 的 artifact
// 字段），不再把 dshd.exe 当作新产物名；`--version` 首词仍与 TUI/版本资源一致。
// 子命令用法里的 `dshd` 是命令行里的 token（脚本、文档照旧），不是产品名。
const productName = "DSH GO"

// builtinModules 是编译进二进制的默认实现列表。
// 将来这里由 blank import + 注册表生成，形状不变。
// 历史检索服务是宿主提供的（它需要知道"当前是哪个会话"）。
var hostHistory = &historyHolder{}

// toolsOptions 是工具流水线的配置，**唯一一份**。
//
// 为什么抽出来：`run` / `exec` 要在**合成目录**（静态 + 插件）上再建一条流水线
// （见 hostToolRunner），而 builtinModules 里那条建在静态目录上。两边各写一份
// Options 一定会漂——最典型的是 SpillDir 写错，于是外置出去的 artifact 模型读不到。
func toolsOptions(home string, maxChars int) tools.Options {
	return tools.Options{
		// artifact 落在 home 下（不是系统 temp）：FS 只放行了这个目录，
		// 放 temp 里模型读不到自己刚被外置的结果。
		SpillDir: filepath.Join(home, "artifacts"),
		MaxChars: maxChars,
		// 官方 0.2.2 #78 `session-checkpoint-policy` 的 `tools/execute` 钩子：
		// 顶层调用的工具体是副作用边界，执行前先让请求前缀耐久；
		// 失败即零执行（pipeline 里 fail-closed，见 modules/tools/pipeline.go）。
		BeforeDispatch: func(_ context.Context, call *spi.Call) error {
			return writeDurableCheckpointForSession(home, call.Session, nil)
		},
	}
}

// hostToolRunner 是宿主**唯一**的工具执行入口（模型与子代理都走它）。
//
// 为什么不是 kernel 里的 "toolrunner"：那条流水线在 modules/tools 的 Register
// 里用**静态目录**构造，构造完 registry 就固定了；插件工具不在里面，于是
// 「模型看得见、runner 回 unknown tool」是必然结果，不是配置问题。
// 这里用同一个构造函数（tools.NewPipeline）、同一份 Options、同一条 bus，
// 只把目录换成合成表——审批、沙箱、超时、外置、R3 兜底一样不少。
func hostToolRunner(root *kernel.Scope, table spi.ToolRegistry, home string, maxChars int) spi.Runner {
	return tools.NewPipeline(table, root.Bus(), toolsOptions(home, maxChars))
}

// builtinModules 按工作区与 home 装配默认实现。
// artifacts 目录要放行给 FS 读：工具输出被外置成 artifact 之后，
// Hint 会告诉模型「用 read 读全文」——不放行那句 Hint 就是假的。
func builtinModules(wd, home string, maxChars int, sub *subProvider) []kernel.Module {
	if sub == nil {
		sub = &subProvider{} // 没绑定：工具会明确报「还没装配好」，不会假装能派
	}
	// 空 home 自举：干净 home 上 <home>/skills 不存在，skill 工具在真机上等于
	// 不可用（列不出任何名字、也取不到正文）。自举只建目录 + 补种缺的 skill，
	// 失败只告警——工具自己会给可执行的 Hint，别让一个目录拖垮整个宿主。
	if err := skill.Bootstrap(filepath.Join(home, "skills")); err != nil {
		fmt.Fprintf(os.Stderr, "skill 自举失败（%v）；skill 工具仍可用，但目录可能不存在\n", err)
	}
	return []kernel.Module{
		// 默认实现也是插件（A3）：它们与第三方插件走同一个 Register(scope)。
		tools.Module(toolsOptions(home, maxChars)),
		// 官方 base 插件 #46：受管 `DSH_*` 环境注册表。装在这里（shell 工具之前）只是
		// 为了让模块图读起来是"先有能力再有用它的工具"——工具是**惰性** inject 的，
		// 真实顺序由能力图决定。装配代码见 shellenv_wiring.go。
		shellEnvModule(home),
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
		wsdeps.Module(),
		todo.Module(),
		// 计划模式：先出方案、人批了再动手。闸门挂在总线的 tools/pre-execute 上，
		// 装配顺序由 Requires("tools") 保证；这里只是挨着 todo 放，好找。
		planmod.Module(filepath.Join(home, "state", "plan")),
		workflowmod.Module(),
		// 插件管理只管 manifest 那层（static）；wasm/process 还没实现，声明它们会被拒。
		pluginmod.Module(filepath.Join(home, "dsh.toml")),
		present.Module(),
		actioncardsmod.Module(home),
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
		unlagmod.Module(filepath.Join(home, "state", "unlag")),
		schedule.Module(filepath.Join(home, "state", "schedules")),
		watch.Module(filepath.Join(home, "state", "watch")),
		nowline.Module(),
		// rc.2 新包 `dsh-terminal-panel` 的两个只读工具（user_terminal_list / read）。
		//
		// 数据源按入口形态解析（见 terminal_bridge.go）：serve 起的工具子进程走 stdio 桥，
		// 独立 run/exec 如实报"这里读不到终端"，子代理会话给官方原文拒绝。
		// `readUserTerminals:false` 时两个工具都不注册（ModuleFromEnv 从宿主环境解析）。
		terminalpanel.ModuleFromEnv(os.Getenv),
		subagentModule(sub),
		subagenttool.Module(),
		subagentfork.Module(inheritFromCtx(home)),
		skill.Module(filepath.Join(home, "skills")),
		hindsight.Module(home),
		// Office 三工具（office_convert / office_recalculate / office_render）。
		// provider 从共享槽取：serve 进程里就是 cmdServe 建的那一份，run/tools/exec
		// 里则由这里建一份 —— 关键是**同一进程只此一份**（同一引擎/同一准入队列/
		// 同一 generation），否则工具面与 Transport 面各排队一半、generation 打架。
		// 见 cmd/dshd/serve_office_kit.go 的 sharedOfficeKitProvider。
		officetool.Module(sharedOfficeKitProvider(home)),
		// MCP 客户端：把 mcp.json 里声明的 server 的 tools/list 真正注册成 spi.Tool
		// （此前只数字数、从不注册）。漏掉 dsh.toml 的白名单条目会让它被静默跳过。
		tools.MCPModule(home),
	}
}

func main() {
	// 版本号只有一个出处：把 main.version 注入需要"当前 runtime 版本"的下游
	// （插件兼容性判定、ACP 握手）。见 cmd/dshd/host_version.go。
	wireHostVersion()
	timingf("main 开始")
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "models":
		err = cmdModels(os.Args[2:])
	case "modules":
		err = cmdModules(os.Args[2:])
	case "exec":
		err = cmdExec(os.Args[2:])
	case "tools":
		err = cmdTools(os.Args[2:])
	case "wasm":
		err = cmdWasm(os.Args[2:])
	case "session":
		err = cmdSession(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "acp":
		err = cmdACP(os.Args[2:])
	case "schedule":
		err = cmdSchedule(os.Args[2:])
	case "analytics":
		err = cmdAnalytics(os.Args[2:])
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "plugins":
		// runPluginsCLI 自己写成功/四字段错误信封；不能再让 main 的错误
		// 出口追加一行文本或改写退出码，否则 CLI 的 JSON 契约就被破坏。
		if code := cmdPlugins(os.Args[2:]); code != 0 {
			os.Exit(code)
		}
	// `version` 与 `--version` 同一件事：TUI 只认 `--version`（它是旗标式 CLI），
	// 宿主是子命令式。两边都认同一个探针，scripts/build.sh 才能用一条命令
	// 核对"产物名 = 版本行首词"，否则那一栏对一个程序是假的。
	case "version", "--version", "-V":
		timingf("enter version")
		fmt.Printf("%s %s\n", productName, version)
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
  dshd plugins --root <dir> <action> [flags] 管理受限本地插件包；root 必须显式给出
  dshd wasm inspect --module <path>          魔数/sha256/导出函数名（按名字排序）
  dshd wasm call --module <path> --fn <f>    调用导出函数；--args 2,3 --timeout 2s 可选，返回值打成一行（如 = 5）
  dsh-go version                             版本
`)
}

const pluginsUsage = `dshd plugins --root <dir> <action> [flags]

action 与参数：
  list
  install --path <本地私有包目录>
  enable|disable|uninstall --id <插件 id>
  update --id <插件 id> --path <本地私有包目录>
  restore --id <插件 id> [--version <保留版本>]
  reconcile

--root 必须显式给出；不会从真实用户 home 推导。成功在 stdout 输出
{"result":…}，失败在 stderr 输出四字段 error 并退出 1。
`

func cmdPlugins(argv []string) int {
	if len(argv) == 1 && (argv[0] == "--help" || argv[0] == "-h" || argv[0] == "help") {
		fmt.Fprint(os.Stdout, pluginsUsage)
		return 0
	}
	return runPluginsCLI(argv, os.Stdout, os.Stderr)
}

// defaultHome 是子命令（modules/tools/exec）用的 home：
// 它们没有 --home 旗标，但 artifact 目录要有一个确定位置。
// defaultLLMBase 是 OpenAI 兼容端点的**唯一**默认值。
//
// 以前这个 URL 在 run/acp/serve/doctor/models 里各写一遍（9 处），
// 于是 doctor 忘了包 envOr（设了 DSHD_LLM_BASE 也不生效）、
// 其它入口改端口要改 9 个地方。参数单源守卫
// （parameter_guard_test.go TestGuardNoDuplicatedGatewayURLLiteral）钉住它只出现一次。
const defaultLLMBase = "http://127.0.0.1:3425/v1"

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
	return loadModsIn(manifestPath, home, maxChars, sub, cwdOrDot())
}

// loadModsIn 是 loadMods 的同一份实现，工作目录由调用方给定。
//
// run 入口用它把官方 `workingDirectory.defaultDirectory`（DSHD_DEFAULT_WORKDIR）
// 变成**工具的根**：read/edit/glob/grep/bash 的相对路径都按它解析。不这么做就会出现
// 「系统提示里写着 Workspace: A，工具却在 B 里干活」——那正是 ACP 那条注释里
// 实测踩过的坑（文件不存在 → 在宿主启动目录里翻）。
func loadModsIn(manifestPath, home string, maxChars int, sub *subProvider, workdir string) ([]kernel.Module, error) {
	mods := builtinModules(workdir, home, maxChars, sub)

	// wasm 模块在 Apply **之前**装：装不起来（缺文件/魔数不对/digest 不符）
	// 就该在启动时炸，而不是等到第一次调用——那正是 digest 校验存在的意义。
	// 注意 kernel 不认识 wasmhost：装载这一步留在宿主这一层（kernel 红线）。
	wasmMods, err := loadWasmModules(manifestPath)
	if err != nil {
		return nil, err
	}
	mods = append(mods, wasmMods...)

	m, err := kernel.LoadManifest(manifestPath)
	if err != nil {
		closeLoadedWasm(context.Background())
		return nil, err
	}
	applied, err := m.Apply(mods)
	if err != nil {
		// Apply 失败也要回收已经装起来的 wasm 句柄，否则进程会攥着它们。
		closeLoadedWasm(context.Background())
		return nil, err
	}
	return applied, nil
}

// loadModsWithPlugins = loadMods + 插件运行时模块（`plugin-runtime`）。
//
// 返回的 *pluginRuntime 是**本进程唯一**的插件管理器句柄（一个进程只 open 一次
// `<home>/plugins`）：既用来在启动路径上 Sync，也由模块把装载器接到内核作用域上。
//
// 为什么加在 Apply **之后**：插件运行时不是可配置项。放进 builtinModules 会被
// dsh.toml 的白名单静默丢掉（Apply 对没列出的模块只打一行 Warn 就跳过）——
// 那正是「装好了插件、模型却永远看不见」的复现路径。
func loadModsWithPlugins(manifestPath, home string, maxChars int, sub *subProvider) ([]kernel.Module, *pluginRuntime, error) {
	return loadModsWithPluginsIn(manifestPath, home, maxChars, sub, cwdOrDot())
}

// loadModsWithPluginsIn 是 loadModsWithPlugins 的同一份实现，工作目录由调用方给定
// （run 入口传官方 `workingDirectory.defaultDirectory`，见 loadModsIn）。
func loadModsWithPluginsIn(manifestPath, home string, maxChars int, sub *subProvider, workdir string) ([]kernel.Module, *pluginRuntime, error) {
	mods, err := loadModsIn(manifestPath, home, maxChars, sub, workdir)
	if err != nil {
		return nil, nil, err
	}
	rt, err := openPluginRuntime(home)
	if err != nil {
		return nil, nil, err
	}
	return append(mods, pluginmod.Runtime(rt.Manager())), rt, nil
}

// syncPlugins 让运行时与 state.json 对齐（启动路径：enabled 的包必须真在跑）。
//
// 失败**不阻塞**：一个坏插件不该让整轮 turn 起不来，其他插件照常装载
// （SyncAll 自己会跑完全部再一起返回）。但必须**逐条**报出插件 id 与原因：
// 只打一行 joined error，人在日志里分不出是哪个包坏了、为什么坏。
func syncPlugins(ctx context.Context, rt *pluginRuntime, w io.Writer) {
	if rt == nil {
		return
	}
	loaded, err := rt.Sync(ctx)
	for _, p := range loaded {
		fmt.Fprintf(w, "插件 %s@%s：已装载，工具 %s\n", p.ID, p.Version, join(p.Tools))
	}
	for _, one := range flattenErrors(err) {
		fmt.Fprintf(w, "插件装载失败（不影响其他插件）：%v\n", one)
	}
}

// flattenErrors 把 errors.Join 的树摊平成一列，便于逐条报错。
//
// 为什么要摊平：errors.Join 的 Error() 是换行拼起来的一整块，人读到的是
// "一大段"，而运维要的是"哪几个插件、各自为什么"。每一条本身就是 loadRecord
// 返回的带 id/版本的错误。
func flattenErrors(err error) []error {
	if err == nil {
		return nil
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		return multi.Unwrap()
	}
	return []error{err}
}

// writeModules 把装配结果写出去：先把激活顺序/清单写给人看，再单列 wasm 小节。
//
// 为什么单独一段：wasm 模块**不提供任何能力**，混在普通模块表里会让人以为
// 它已经在给工具了。单开一节并写明"只能 dshd wasm call 手动调"才不会误会。
func writeModules(w io.Writer, kind string, mods []kernel.Module) error {
	switch kind {
	case "list":
		fmt.Fprintf(w, "%-22s %-9s %-26s %s\n", "MODULE", "BACKEND", "PROVIDES", "REQUIRES")
		for _, m := range mods {
			fmt.Fprintf(w, "%-22s %-9s %-26s %s\n", m.ID, backendOf(m), join(m.Provides), join(m.Requires))
		}
	case "graph":
		order, err := kernel.TopoSort(mods)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "# 激活顺序（由能力依赖决定，不是 manifest 行顺序）\n")
		(&kernel.Plan{Order: order}).Write(w)
	default:
		return fmt.Errorf("未知子命令 modules %s", kind)
	}
	writeWasmSection(w)
	return nil
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
	return writeModules(os.Stdout, argv[0], mods)
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
	return toolTableOf(reg.Tools())
}

// toolTableOf 是同一个投影，只是入口是"一列工具"——merged 模式下去掉成员的
// 表也走它，两条路必须产出同一种形状。
func toolTableOf(tools []spi.Tool) ([]ToolEntry, error) {
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
	// 表形态：真正发给模型的是 compact。打印 full 会让人以为成本是 32 KB，
	// 而实际发出去的是压缩后的——门禁按"发出去的那个"算才有意义。
	schema := fs.String("schema", defaultToolSchema, "打印哪种形态（旧名，等价于 --tools-mode）：compact|full|lazy")
	mode := fs.String("tools-mode", "",
		"工具表形态：compact（默认，= 去参数说明 + 去冗余 schema 骨架）| full | merged（watch/hindsight/schedule/team_task 四族各合成一个工具，多一个 action 参数）| lazy（只常驻语料前 30 名，其余用 tool_load 取）")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	tm, err := resolveToolsMode(*mode, *schema)
	if err != nil {
		return err
	}
	mods, pluginRT, err := loadModsWithPlugins(*manifest, defaultHome(), 0, nil)
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
	// 诊断要和模型看到的是**同一张表**：先装载，再从合成目录投影。
	// 不 Sync 的话这里永远少掉插件工具，而 run 里它们又都在——
	// 一份诊断对不上真值，比没有诊断更坏。
	syncPlugins(context.Background(), pluginRT, os.Stderr)
	reg, err := kernel.Inject(root, pluginmod.TableKey)
	if err != nil {
		return err
	}
	// 与 cmdRun 走**同一条**投影链（见 projectTable）：合并 → 收紧 → lazy。
	// 分成两套写法的话，`dshd tools` 打印的就不是真正发出去的那张表了。
	view, _, lazyRef, warns, err := projectTable(tm, reg)
	if err != nil {
		return err
	}
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "工具表："+w)
	}
	table, err := toolTableOf(view.Tools())
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "工具 %d 个，%d 字节（形态 %s）\n", len(table), len(out), tm)
	if lazyRef != nil {
		dir := lazyRef.Directory()
		fmt.Fprintf(os.Stderr, "lazy：常驻 %d 个，表外 %d 个；系统提示目录 %d 字节\n",
			len(lazyRef.hot), len(lazyRef.names), len(dir))
	}
	return nil
}

// defaultToolSchema 是工具表的默认形态。
//
// compact = 投影时去掉参数的说明文字（结构不动），实测输入省 22%，
// 10 个任务的正误与完整表一致（scripts/tool-table-ab.py，20 次采样）。
// 要改这个默认值，先重跑那个脚本——别凭感觉。
//
// 注意：这条默认能成立，前提是工具自己兜住了"可推导的参数"
// （claim 认领到会话、update_goal.watch.schedule.job 的 id 可省），
// 不再依赖说明文字提醒模型该填什么。
const defaultToolSchema = "compact"

// 两个默认值。工具表改坏了只是多花 token，这两个改坏了是**动权限**，所以单列出来。
//
//   - defaultRunPolicy=allow：**默认完全权限**。
//     这条改过：早先默认是 `auto`（终端里问、非终端直接拒），理由是"绝不能把写文件
//     和跑命令的授权静默发出去"。2026-11-04 用户明确要求改掉——
//     「把默认审批改成完全权限，不要老是弹权限申请」。
//     代价说清楚：`run` 一跑起来就带写盘与执行权限，没有事前拦截。
//     要逐次询问就显式写 `--policy ask`；要只读就 `--policy deny`。
//     这条改动同时写在 CHANGELOG 里，别把它当成"历史默认值"读。
//   - defaultWorkspaceBytes=65536：默认把 AGENTS.md 灌进系统提示，按**官方
//     dsh-base 的每条指令链预算**取值（@deepseek-ai/dsh-agent-instructions：
//     "dsh-base already includes it with a 65,536-byte budget for each
//     instruction chain ... at most twice maxBytes"）。两条链各自 65,536，
//     合计最多 131,072。
//     设 0 等于不传手册，模型会凭空猜约定（踩过：它连"描述 ≤120 字"都不知道）。
const (
	defaultRunPolicy      = "allow"
	defaultWorkspaceBytes = 65536
)

// workspaceBytesForPrompt 把 CLI 的「0=不传」映射成 prompt 包的关闭哨兵。
//
// prompt 包的零值语义是"用官方默认预算"，所以 0 不能直接透传；负值才是这条
// 指令链不注入。flag 帮助写的是 0=不传，这里就按帮助的语义做。
func workspaceBytesForPrompt(n int) int {
	if n == 0 {
		return -1
	}
	return n
}

// emitJobs 把后台任务清单写一行到 stderr，标记 `@@JOBS@@`。
//
// 为什么走 stderr 而不新开通道：和审批同一个理由（见 approval_stdio.go）——
// Windows 上继承额外 fd 麻烦、stderr 本来就通着、**不能碰 stdout**
// （那里只有一行 ready 握手，是 H11 进程级验过的契约）。
//
// 为什么由子进程推、而不是父进程问：子进程的工具在**它自己的 kernel Scope** 里跑
// （`spi.Job` 是那里提供的），父进程没有那个实例。推一次比"问-答"少一套状态机，
// 而界面本来就是轮询 `jobs.list`。
func emitJobs(root *kernel.Scope) {
	job, err := kernel.Inject(root, spi.JobKey)
	if err != nil || job == nil {
		return
	}
	list := job.List()
	if len(list) == 0 {
		return // 没有后台任务就不发，省得每轮都写一行
	}
	b, err := json.Marshal(list)
	if err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "@@JOBS@@ %s\n", b)
}

// cmdRun 跑一个真实 turn：装配 → 模型 → 工具 → 回灌 → 收尾。
//
// 这是 M2 的验收动作：它同时踩到装配、工具流水线、LLM 流式、agent 双循环四条线。
func cmdRun(argv []string) (retErr error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	manifest := fs.String("manifest", "dsh.toml", "manifest 路径")
	prompt := fs.String("prompt", "", "用户输入")
	promptContentFile := fs.String("prompt-content-file", "", "serve 父进程交接的有序 content blocks（内部参数）")
	turnID := fs.String("turn-id", "", "serve 当前 turn 的宿主身份（steer IPC 内部参数）")
	model := fs.String("model", envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017"), "模型 id")
	base := fs.String("base", envOr("DSHD_LLM_BASE", defaultLLMBase), "OpenAI 兼容端点")
	maxSteps := fs.Int("max-steps", defaultTurnMaxSteps, "最多几步")
	home := fs.String("home", defaultHome(), "会话根目录")
	maxChars := fs.Int("max-chars", defaultMaxChars, "工具输出超过多少字符就外置成 artifact（越小越省 token，越大越少重读）")
	workspaceBytes := fs.Int("workspace-bytes", defaultWorkspaceBytes, "AGENTS.md 进系统提示的字节上限（0=不传）")
	resumeSession := fs.String("session", "", "续这个会话（空=新建）；cwd 可省，按 id 找")
	// 工具表默认压掉参数说明：20 次 A/B（10 个任务 × 两种表）结果一致，输入省 22%。
	// 要完整表就显式写 --tool-schema=full——比如排查"模型没填对参数"时。
	// 推理强度：空 = 不要（默认），xhigh/max = 向网关要推理内容。
	// 为什么要有 flag 而不是只有环境变量：界面线/脚本要能显式打开它，
	// 而环境变量在 --help 里看不见，等于"藏起来的开关"。
	reasoningEffort := fs.String("reasoning", os.Getenv("DSHD_REASONING_EFFORT"),
		"推理强度：空=不要（默认）| xhigh | max。要了才把 reasoning 写进日志与流")
	// serve 父进程把本轮解析出的 catalog level 与 wire 一起交给真正发请求的
	// cmdRun。selection 在每个 step/start 处落盘，不能由父进程回合后按 store 补造。
	recordEffort := fs.Bool("record-effort", false, "serve 内部：逐步记录真实模型请求 selection")
	effortLevel := fs.String("effort-level", "", "serve 内部：本次请求实际使用的 catalog effort level")
	toolSchema := fs.String("tool-schema", defaultToolSchema, "工具表形态（旧名，等价 --tools-mode）：compact=压掉参数说明与冗余 schema 骨架 | full=完整")
	// merged 是第三档：把同族工具（watch/hindsight/schedule/team_task）各合成
	// 一个工具，多一个 action 参数。只改模型看到的表，执行仍按原工具名走。
	toolsMode := fs.String("tools-mode", "",
		"工具表形态：compact（默认）| full | merged（四族合成一个工具+action：表更小，但模型要自己挑 action，选错就白跑一步）| lazy（只常驻 30 个，其余先 tool_load 取；默认关闭，见 docs/tool-table-lazy.md）")
	// 规则文件：默认工作区的 AGENTS.md；给"执行者"喂精简手册时指到 docs/WORKER.md。
	fs.StringVar(rulesPath, "agents", "", "规则文件路径（默认工作区 AGENTS.md）")
	policy := fs.String("policy", defaultRunPolicy, "审批策略：ask/allow/deny/auto（auto=终端问、非终端拒）")
	// --auto-compact 同时接受整数与 true/false（"关掉自动压缩"最自然的是
	// --auto-compact=false）。默认值与 serve 侧同源，见 host_run_options.go。
	autoCompact := defaultAutoCompact
	fs.Var(autoCompactValue{target: &autoCompact}, "auto-compact",
		"每步前估算超过多少 token 就自动压缩（0/false=关闭，true=默认 40000）")
	// 显式容量声明：serve 会把这两个值原样透传进来（它自己也从同名 flag 解析）。
	// contextWindow 落到 v4 request/context（界面/context.usage 的分母），
	// maxOutput 落到请求的 max_tokens。
	ctxWindow := fs.Int("context-window", 0, "显式上下文窗口（写进 v4 request/context）；0=按模型容量解析")
	maxOutput := fs.Int("max-output", 0, "显式单次最大输出 token；0=按模型容量解析")
	// 官方 context.compaction.headroomTokens / modelPolicies：三个入口同一份解析
	// （compact_policy_wiring.go），serve 会原样透传这两个值。
	compactHeadroomDefault, err := compactHeadroomFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	compactHeadroom := fs.Int("compact-headroom-tokens", compactHeadroomDefault,
		"窗口下方预留多少 token 的安全带（官方 headroomTokens，默认 65536；0=不留）")
	compactPoliciesRaw := fs.String("compact-model-policies", os.Getenv(EnvCompactModelPolicies),
		"按 provider/model 覆盖压缩阈值（JSON 数组；空=不覆盖）")
	// 官方 context.runtimeContext.perStep（默认 true）：每一步重新投影运行时事实。
	// 关掉时请求消息与既有形状逐字节一致（老会话排查提示词漂移时能一键回退）。
	runtimeContextOn := fs.Bool("runtime-context", runtimeContextEnabled(os.Getenv),
		"每步重新投影运行时事实（时间/工作目录/平台）；false=用装配期快照（官方默认 true）")
	// 父进程看门狗：serve 起的工具轮就是子进程 `dshd run`（serve_tools.go 的 childArgs），
	// 所以 run 也得挂同一组开关——否则 serve 死了、它还在跑。
	pw := addParentWatchFlags(fs)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if os.Getenv("DSHD_STEER_STDIO") == "1" && *turnID == "" {
		return fmt.Errorf("steer IPC 要求显式 --turn-id")
	}
	// 非法压缩配置在**发任何模型请求之前**报出来（静默回退会让人以为
	// headroom 生效了，实际跑的是默认 64K）。
	compactPolicy, _, err := compactPolicyFromFlags(*model, *compactHeadroom, *compactPoliciesRaw, compact.RetryPolicyFromEnv(os.Getenv))
	if err != nil {
		return err
	}
	runPrompt := *prompt
	var runContent []session.ContentBlock
	var runParts []llm.MessagePart
	if *promptContentFile != "" {
		if *prompt != "" {
			return fmt.Errorf("--prompt 与 --prompt-content-file 不能同时指定")
		}
		runContent, runParts, err = loadPromptPartsForRun(*home, *promptContentFile)
		if err != nil {
			return err
		}
	}
	if runPrompt == "" && len(runContent) == 0 {
		return fmt.Errorf("缺 --prompt")
	}
	tm, err := resolveToolsMode(*toolsMode, *toolSchema)
	if err != nil {
		return err
	}
	if err := checkHome(*home); err != nil {
		return err
	}
	// 和 serve 同一份 SpillDir 逻辑：run 直连全新 home 时，sandbox 的
	// extra read root（<home>/artifacts）还不存在；必须在模块装配前自举。
	if err := ensureSpillDir(*home); err != nil {
		return err
	}
	// spill 保留期回收（官方 session.spill.cleanupPeriodDays，默认 30 天）：
	// run 与 serve 同源，一次性宿主也顺手清一遍过期的外置文件。
	sweepSpillArtifactsBestEffort(*home)

	// 看门狗尽早起：参数校验通过就挂上，别等到建完会话目录才开始盯。
	watch := pw.start(os.Stderr)
	defer watch.Stop()
	// run 是被 ctx 驱动的：父进程没了就把 ctx 取消，让本轮跑完手上的活再退。
	// 直接 os.Exit 会丢掉"这一轮已经做的事"的记录，而记录是这个仓库最在意的部分。
	runCtx, stopOnParentGone := context.WithCancel(context.Background())
	defer stopOnParentGone()
	go func() {
		select {
		case <-watch.Done():
			fmt.Fprintf(os.Stderr, "父进程已退出（%s），本轮收尾后停止"+string(rune(10)), watch.Reason())
			stopOnParentGone()
		case <-runCtx.Done():
		}
	}()

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
	// 官方 `workingDirectory.defaultDirectory`（DSHD_DEFAULT_WORKDIR）在这里定一次：
	// 工具的根、系统提示里的 Workspace、新会话 header.cwd 三处用的是**同一个值**。
	// 未配置 = 进程 cwd（与以前逐字节一致）。
	workDir, err := sessionWorkDir()
	if err != nil {
		return err
	}
	mods, pluginRT, err := loadModsWithPluginsIn(*manifest, *home, *maxChars, subp, workDir)
	if err != nil {
		return err
	}
	k := kernel.New("dshd")
	root := k.Root()
	var jobsPersistSession string
	defer func() {
		// Provide 的 disposer 会注销 spi.JobKey；必须在 Dispose 前保留实例，
		// 再等 Close 收工后读最终 done/exit，不能从已撤销的内核重新 Inject。
		var job spi.Job
		if jobsPersistSession != "" {
			injected, err := kernel.Inject(root, spi.JobKey)
			switch {
			case err != nil:
				retErr = errors.Join(retErr, fmt.Errorf("保留任务输出前注入 jobs 能力失败: %w", err))
			case injected == nil:
				retErr = errors.Join(retErr, fmt.Errorf("保留任务输出前注入 jobs 能力失败: 服务为 nil"))
			default:
				job = injected
			}
		}
		root.Dispose()
		if job != nil {
			if err := persistJobOutputs(job, *home, jobsPersistSession); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("保留 cmdRun 任务输出失败: %w", err))
			}
		}
	}()
	if _, err := kernel.Assemble(root, mods); err != nil {
		return err
	}
	// 插件运行时：state.json 里 enabled 的包在这里**真的装载**。
	// 顺序有讲究——装配之后（装载器要把静态目录合成进表）、投影工具表之前
	// （合成表是动态视图，先装载才看得见）。装载失败逐条报出，不阻塞这一轮。
	syncPlugins(runCtx, pluginRT, os.Stderr)
	// 模型看到的工具表是**合成表**（静态 + 插件）。以前这里是 spi.ToolsKey，
	// 于是插件工具一条也进不了表：装得上、跑得起来、模型看不见。
	reg, err := kernel.Inject(root, pluginmod.TableKey)
	if err != nil {
		return err
	}
	// 装配完整性检查：toolrunner 必须存在（模块没接上要在这里就炸）。
	// 但**不用它**去执行：它绑的是静态目录，插件工具在里面查不到，
	// 会变成「模型看得见、runner 回 unknown tool」。执行入口见 hostToolRunner。
	if _, err := kernel.Inject(root, spi.ToolRunnerKey); err != nil {
		return err
	}
	runner := hostToolRunner(root, reg, *home, *maxChars)

	// 新会话的工作目录与工具根是同一个值（装配期就定死的 workDir，见上面）。
	// `--home` 是会话根，不是工作目录——两者互不替代。
	cwd := workDir
	// 工具表投影（只这一层）：合并 → 收紧 → lazy，见 projectTable。
	// 系统提示里的工具清单也要跟着换——否则模型从提示里看到旧的 17 个名字，
	// 从 tools 字段看到 4 个，两边对不上。
	table, mergedRef, lazyRef, warns, err := projectTable(tm, reg)
	if err != nil {
		return err
	}
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "工具表："+w)
	}
	sys, err := buildSystemPrompt(table, *model, cwd, workspaceBytesForPrompt(*workspaceBytes))
	if err != nil {
		return err
	}
	// lazy 的系统提示目录**故意不在这里拼**：它要等装载集回放完（见下面那一段），
	// 否则上一轮已经装载过的工具会在目录里被重复列一遍。
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
	jobsStdio := os.Getenv("DSHD_JOBS_STDIO") == "1"
	steerStdio := os.Getenv("DSHD_STEER_STDIO") == "1"
	// rc.2 终端面板的父→子 roster 桥（`user_terminal_list` / `user_terminal_read`）。
	// 应答走**同一根 stdin**，所以缺了 startStdinDispatcher 就没人读——工具只能等到超时。
	termStdio := userTerminalsViaStdio()
	if jobsStdio || steerStdio || termStdio {
		// 所有父→子请求共享唯一 stdin 读端，避免 buffered reader 抢读/丢行。
		startStdinDispatcher()
	}
	// 数据源无条件挂上（不只是有桥时）：没有桥时 list/read 结构化说明"要 serve"，
	// 但**子代理判定**仍走本地真实谱系——子代理必须在任何形态下都被拒绝。
	if err := provideUserTerminalBridge(root, *home); err != nil {
		return fmt.Errorf("终端面板数据源挂载失败: %w", err)
	}
	if jobsStdio {
		// jobs 通道必须在 agent loop 开始前建立；旧实现到 loop 结束才启动，
		// 导致真正运行中的任务既查不了输出也停不了。
		go serveJobsOut(root, *home, sessID)
		jobsPersistSession = sessID
	}
	var steerInbox *agent.SteerInbox
	if steerStdio {
		steerInbox = agent.NewSteerInbox()
		defer steerInbox.Close()
		go serveSteerIPC(runCtx, steerReqCh, os.Stderr, steerInbox, *home, sessID, *turnID)
	}

	// 续会话时必须把**已有历史**读进来，否则模型看到的是一张白纸：
	// 它会在旧日志上追加新消息，却不知道前面聊过什么（实测会回答"这是第一条消息"）。
	//
	// 这份解析结果**两处用**：续会话的历史、lazy 的装载集回放。所以只读一次
	// （长会话的日志可能几十 MB，读两遍纯属浪费）。读不出来不是致命错误：
	// 没历史 = 白纸，装载集空 = 回到"什么都没装载"，两条都各自有降级路径。
	back, backErr := v4.ReadFile(logw.Path)
	var baseHist []llm.Message
	if backErr == nil && *resumeSession != "" {
		baseHist = session.History(back)
	}
	// lazy 的装载集是**会话状态**，不是进程状态：serve 每轮起一个新的 run 进程，
	// 上一轮 tool_load 取过的工具这一轮必须直接可用——否则模型凭上一轮的记忆
	// 直接喊它会被挡回，白跑一次 tool_load（docs/tool-table-lazy.md §6 第一条）。
	// 状态就存在这份 append-only 日志里（每次 tool_load 都是一条 tool/call 事件），
	// 这里只是回放它：不额外落盘、不新增事件、读路径不写盘。
	//
	// lazy 的目录是系统提示的一部分：被藏起来的工具，它的描述也一起藏了，
	// "缺什么先 tool_load"这条规则没有别的地方可放。放在回放**之后**拼，
	// 是因为 Directory 会跳过已装载的——那一刻表里得已经装好了。
	if lazyRef != nil {
		if backErr != nil {
			// 只警告、不中断：回到未装载，模型下一步 tool_load 就能拿回来。
			fmt.Fprintf(os.Stderr, "lazy：会话日志读不出来（%v），装载集按空处理\n", backErr)
			// 读坏了的日志不能当输入递下去：回放的唯一输入必须是"这个会话
			// 真读出来的那份日志"，坏了就等于这个会话什么都没装载过。
			back = nil
		}
		dir, got := prepareLazy(lazyRef, back)
		if len(got) > 0 {
			fmt.Fprintf(os.Stderr, "lazy：从会话回放装载集 %d 个：%s\n",
				len(got), strings.Join(got, ", "))
		}
		sys += "\n\n" + dir
	}
	logSessionStart(*home, sessID, logw.Path, cwd, modes.DefaultID)
	// 把"当前会话"指给 history_search——`run` 以前没这一步，模型回查只会得到
	// "没有活动会话"。ACP 那边早就指了，这里补上。
	attachHistory(logw)

	// 子调用（workflow 里发的那些）不属于 agent 循环的事件流，循环不会为它们写 v4。
	// 结果就是：编排跑了 5 次 grep，回去看 session.history 却什么都没有。
	// 这里补一条**只给 Nested** 的写入点——顶层调用仍由循环自己写，
	// 不加这个判断就会把同一次调用记两遍（v4 是单写者模型）。
	//
	// 实现在 nested_v4.go：并行工具调用之后，两个调用可能各自带着子调用同时
	// 走到这里，而 v4 是单写者模型（seq 分配与写帧都没有锁）——那里用一把锁
	// 把"一次调用的 call+result"成对写完。
	kernel.OnTyped[*spi.Call](root.Bus(), root, spi.EventToolResult, newNestedV4(logw).record)

	if err := logw.Append("system/message", session.SystemMessageData(1, 1, sys)); err != nil {
		return err
	}
	if err := logw.Append("turn/start", session.TurnStartData(1)); err != nil {
		return err
	}
	// v4 `request/context`：这一轮请求用的模型（`session.load.model`、`context.usage`
	// 都只读这条事件，而宿主以前从不写它——我们自己的会话在界面上"没有模型"）。
	//
	// contextWindow 只在**调用方显式声明**时才写：它是 `context.usage` 的分母，
	// 而"按模型名猜一个分母"会让百分比变成假精确（比不给更糟）。没有显式声明时
	// 界面用 `session.load.contextWindow`（transport_source.ContextWindow：
	// env → catalog 声明 → 内置容量），那条链有真值来源。
	//
	// 这也是 `--context-window` 的真实使用点：只 append 进 argv 不接使用点，
	// 就是一个看起来可调的假开关。
	requestContext := map[string]any{"model": *model}
	if *ctxWindow > 0 {
		requestContext["contextWindow"] = *ctxWindow
	}
	if err := logw.Append("request/context", requestContext); err != nil {
		return err
	}
	// turn 一开，结局就有**唯一**责任人：从这里到函数返回之间的每一种走法
	// （正常收工、模型报错、步数用满、取消、panic）都必须留下一条 turn/end。
	//
	// 注册在这个位置是有意的：defer 是后进先出，所以它比上面那个
	// `defer logw.Close()` 先跑——补写结局时文件还开着。
	tc := newTurnCloser(logw, 1)
	defer func() {
		if r := recover(); r != nil {
			// panic 也是"这一轮没跑完"的一种。先补结局，再把 panic 原样抛回去：
			// 吞掉它会让一个真 bug 变成一次"看起来很正常的失败"。
			if cerr := tc.ExitOnPanic(r); cerr != nil {
				fmt.Fprintf(os.Stderr, "panic 时补 turn/end 失败（日志里这一轮仍是悬挂）：%v\n", cerr)
			}
			panic(r)
		}
		// 出口兜底：已经写过就什么都不做（幂等）；没写过就按实际情况补一条。
		// 写结局失败**不许盖住原始错误**——原始错误才是这一轮为什么死的那个原因。
		if cerr := tc.Exit(context.Background(), retErr); cerr != nil && retErr == nil {
			retErr = cerr
		}
	}()
	if len(runContent) > 0 {
		err = logw.Append("user/message", session.UserContentMessage(runContent))
	} else {
		err = logw.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: *prompt}}))
	}
	if err != nil {
		return err
	}
	if err := captureRewindCheckpointForLog(*home, logw); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}
	// 官方 0.2.2 #78 `llm/stream`：请求前缀先耐久，再派发模型请求。
	// 与 serve 的四个 prompt 入口同一条判据；失败即 fail-closed（这一轮不派发）。
	runCheckpoint, err := durableCheckpointRequestForRun(runPrompt, runContent)
	if err != nil {
		return err
	}
	if err := writeDurableCheckpointForSession(*home, sessID, &runCheckpoint); err != nil {
		return err
	}
	var lastCallSeq int64
	// delta index 在同一步的 reasoning/text 间共享。
	deltaIndexes := map[string]int{}
	stepKey := func(turn, step int) string { return fmt.Sprintf("%d/%d", turn, step) }
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
		policyDeniesUnattended: deniesSilently(*policy, isTerminal(os.Stdin)),
	}
	// merged 的路由器在守卫**外层**：先把 action 还原成原工具名，再交给守卫。
	// 顺序反了就会拿合并名去查 Caps（查不到 → 审批判定跑偏），
	// 而且守卫的"以后都允许"记忆是按工具名记的，两边名字必须一致。
	loopRunner := spi.Runner(guarded)
	// 用 mergedRef 而不是再断言 table 的类型：table 现在可能被 tightenTable 包过一层，
	// 而路由必须拿**合并层自己**（它才是 action → 原工具名 的那张表）。
	if mergedRef != nil {
		mr := &mergedRunner{inner: guarded, table: mergedRef}
		mergedRef.bindRunner(mr.Run) // Exec 兜底：即便有人绕过 Runner，也还是走同一条路由
		loopRunner = mr
	}
	// lazy 在最外层：tool_load 要先被接住（它不在 registry 里），
	// 没装载的表外工具也要在执行前挡回。放进守卫里面的话，tool_load 会被
	// 当成"未知工具"打回，而"没装载"的调用会直接被真 registry 执行掉。
	if lazyRef != nil {
		loopRunner = &lazyRunner{inner: loopRunner, table: lazyRef}
	}

	// 官方 `retryPolicy`（每 provider 一份）：策略由 serve 侧从模型目录读出、
	// 经 DSHD_LLM_RETRY_POLICY 交给这一轮的 run 子进程。写错必须 fail-closed
	// （一个请求都不发），不能静默回落到默认重试参数。
	chat, err := newTurnChat(*base, os.Getenv("DSHD_LLM_KEY"), os.Getenv("DSHD_LLM_API"), os.Getenv)
	if err != nil {
		return err
	}
	// 官方每请求采样面（llm.temperature / llm.stop）。解析放在建循环之前、发请求
	// 之前：配置写错必须 fail-closed（一个请求都不发），而不是静默用默认值跑一轮。
	sampling, err := samplingFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	// 官方 llm.resolveModelInfo.contextWindowValidation：声明了就必须是正整数，
	// 否则 INVALID_MODEL_CONTEXT。`--context-window 0` 是宿主既有的"按模型容量
	// 解析"（docs/model-capacity.md §1），负值只能是写错了。
	if err := llm.ValidateDeclaredContextWindow(*model, *ctxWindow); err != nil {
		return err
	}
	// meter 跨步复用：每步都用服务端给的 totalTokens 校准一次估算。
	// 以前每步新建一个空 meter，等于永远按启发式比例猜。
	meter := &session.Meter{}
	// 子代理复用同一套零件：同一个模型、同一张工具表、同一个执行器。
	// 差别只有系统提示与步数上限——但步数上限也走**同一个默认值**：子代理干的
	// 同样是"模型-工具"循环，8 步的墙在这里一样会把能跑完的任务砍成
	// "用满仍未收敛"。真正的护栏是下面的墙钟 timeout（子代理不是无人值守的
	// 无限任务），步数只是死循环安全阀。
	// 官方 subagent.maxActiveSubagents：并发子代理上限（默认 8；desktop profile
	// 覆盖为 20）。解析在建循环之前、发请求之前：写错就 fail-closed。
	subGate, err := subagentGateFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	subp.Bind(&subRunner{
		chat: chat, tools: table, runner: loopRunner,
		model: *model, maxSteps: defaultTurnMaxSteps, timeout: 5 * time.Minute,
		temperature: sampling.Temperature, stop: sampling.Stop,
		// 可续问子会话的落点。空着会让 Continuable 明确失败——所以必须给。
		home: *home,
		gate: subGate,
	})
	loop := &agent.Loop{
		Chat:   chat,
		Steers: steerInbox,
		// 工具写的耐久会话事件（workflow 的 4 类 `tool-workflow/*` 帧）落到本会话
		// 的 v4 日志：serve 的 follow 会把它镜像成 journal 帧，GUI 再投影出节点。
		// 不给它，workflow 就会"跑得起来但界面上什么都没有"。
		Events: newSessionEventSink(logw),
		// 容量声明由调用方给（serve 透传 --context-window/--max-output）；
		// 循环里唯一的解析点是 llm.ResolveModelCapacity，这里不重复算。
		Capacity: llm.ModelCapacity{ContextWindow: *ctxWindow, MaxOutput: *maxOutput},
		Compact:  runCompactor(chat, logw, autoCompact, compactPolicy, llm.ModelCapacity{ContextWindow: *ctxWindow, MaxOutput: *maxOutput}, *model, meter),
		// 官方 context.runtimeContext.perStep：每步重投影运行时事实（不重组提示）。
		RuntimeContext: runtimeContextProvider(*runtimeContextOn, sessID),
		Tools:          table,
		Runner:         loopRunner,
		Project:        session.ProjectResult,
		Model:          *model,
		System:         sys,
		SessionID:      sessID,
		// 官方 0.2.2 #78 `agent/pre-step`：每一步模型请求之前先让请求前缀耐久。
		// 第一步与 prompt 侧车同锚点（幂等命中），第二步起各自落一份。
		PreStep: worktreePreStep(newWorktreeService(newSessionSource(*home, "")), sessID, func(ctx context.Context, step int) error {
			return writeDurableCheckpointForSession(*home, sessID, nil)
		}),
		// 这里显式关掉循环自带的压表：表在 cmdRun 里已经被 tightenTable 投影过一遍。
		// 两条路都调用 internal/schema 里同一条判断（区分"注解"与"名叫 description 的参数"），
		// 但 tightenSchema 还会额外删冗余骨架；同时开会把后一遍的骨架删除覆盖掉，
		// 所以只留 cmdRun 这一遍。
		CompactTools: false,
		// 只有显式设了才要推理：某些网关不认这个参数，默认不带更安全。
		Reasoning: *reasoningEffort,
		// 官方每请求采样面（llm.temperature / llm.stop）：只按环境变量显式配置，
		// 不设时是 nil/空切片 = 报文里一个键都不出现（见 env_llm.go）。
		Temperature: sampling.Temperature,
		Stop:        sampling.Stop,
		MaxSteps:    *maxSteps,
		Emit: func(e agent.Event) error {
			switch e.Kind {
			case agent.EventStepStart:
				if *recordEffort {
					// 这里的 model/level 与下方 loop.Request 使用同一份本轮值；
					// 每个首步、工具后续步、steer 后续步都在真实调用前各记一次。
					selection := map[string]any{"model": *model}
					if *effortLevel != "" {
						selection["reasoningEffort"] = *effortLevel
					}
					if err := logw.Append("model/selection", selection); err != nil {
						return fmt.Errorf("写第 %d 步真实 effort selection: %w", e.Step, err)
					}
				}
				return logw.Append("step/start", map[string]any{"turn": e.Turn, "step": e.Step})
			case agent.EventReasoning, agent.EventText:
				key := stepKey(e.Turn, e.Step)
				index := deltaIndexes[key]
				deltaIndexes[key] = index + 1
				return logw.Append(string(e.Kind), map[string]any{
					"turn": e.Turn, "step": e.Step, "index": index,
					"text": e.Text, "model": *model,
				})
			case agent.EventStepEnd:
				return logw.Append("step/end", map[string]any{"turn": e.Turn, "step": e.Step})
			case agent.EventUserMsg:
				if e.User == nil {
					return fmt.Errorf("steer user/message 缺持久数据")
				}
				return logw.Append("user/message", e.User.Data)
			case agent.EventAssistantMsg:
				a := e.Assistant
				// 铺块只有一份：serve_turn.assistantBlocksFrom（思考 → 正文 → 调用）。
				//
				// 这里以前自己攒一个 stepReasoning、再拼一遍 blocks——run / serve / acp
				// 三条路里最后一份复制的实现。复制的代价这个仓库刚付过两次：同一条规则
				// 两处各写一遍，就会两处各错一次（CompactSchema / stripDescriptions 都
				// 删过名叫 description 的参数）。所以思考**不另攒**，直接取 a.Reasoning
				// （= llm.Response.Reasoning，也即事件流里 EventReasoning 拼起来的那段），
				// 与 ACP 写盘 sink 同源。
				blocks := assistantBlocksFrom(a.Content, a.Reasoning, a.ToolCalls)
				// usage 的键名与「input 要不要减掉缓存命中」只在一处判断：
				// assistantUsageFrom（run / acp 共用）。写在这里的第二次复制
				// 就是下一次两处各错一次的来源。
				usage := assistantUsageFrom(a.Usage)
				if a.Usage != nil {
					// stats 行报的是 provider 真正的 prompt 大小（含缓存命中），
					// 与写进 v4 的 inputTokens（未命中部分）是两个口径，故意不共用。
					inTokens += a.Usage.PromptTokens
					outTokens += a.Usage.CompletionTokens
					// 用服务端实测值校准估算器：下一轮压缩判断才有锚点。
					if back, err := v4.ReadFile(logw.Path); err == nil {
						meter.Observe(a.Usage.TotalTokens, meter.Estimate(session.Surface(back)))
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
				// 把 Summary 也打出来：那是工具想告诉人的一句话
				// （"已推进 3 条"、"子代理完成…；沿用主会话审批记忆"）。
				// 只打 [ok] 等于人看不到任何结果，只有模型看得到。
				summary := ""
				if e.Result != nil {
					summary = strings.TrimSpace(e.Result.Summary)
				}
				if summary != "" {
					fmt.Fprintf(os.Stderr, "  ← %s [%s] %s\n", e.ToolName, code, truncate(summary, 100))
				} else {
					fmt.Fprintf(os.Stderr, "  ← %s [%s]\n", e.ToolName, code)
				}
				var blocks []spi.Block
				if e.Result != nil {
					blocks = session.ProjectResult(*e.Result)
				} else {
					blocks = []spi.Block{{Type: "text", Text: "（无结果）"}}
				}
				// rc.2：失败结果的 code 不能被折叠（见 turn_error_codes.go）。
				if err := logw.Append("tool/result",
					toolResultEventOf(e, blocks, cwd, []int64{lastCallSeq})); err != nil {
					return err
				}
				emitJobs(root)
				return nil
			case agent.EventTurnEnd:
				// step/end 只消费 agent 在真实完成边界发出的事件；模型错误、
				// malformed response 等失败路径绝不在这里补造成功 step/end。
				// 结局写 finish 对应的 kind，不是恒写 completed：
				// 被 max_tokens 截断的回合要留成 max-tokens，否则客户端会把它当完整回答。
				//
				// 走 closer 而不是直接 Append：出口兜底靠它的 closed 标志判"写过没有"，
				// 两条路各自直接写就会给同一轮写出两条 turn/end。
				return tc.End(e.Finish)
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

	runMessage := llm.Message{Role: "user", Content: runPrompt}
	if *promptContentFile != "" {
		runMessage = llm.Message{Role: "user", Parts: runParts}
	}
	hist := append(append([]llm.Message(nil), baseHist...), runMessage)
	hist, err = loop.Run(runCtx, 1, hist)
	if err != nil {
		return err
	}
	// 把后台任务清单推给父进程（serve 的 jobs.list 读它）。
	// **失败不影响主流程**：这只是给界面看的信息，不是本轮结果。
	emitJobs(root)
	if err := logw.Close(); err != nil {
		return err
	}
	last := hist[len(hist)-1]
	fmt.Println(last.Content)
	fmt.Fprintf(os.Stderr, "会话：%s（%d 事件）\n", logw.Path, logw.Seq())
	return nil
}

// turnCloser 是"开了的 turn 一定闭合"的唯一出口（D1）。
//
// 为什么需要它：`turn/end` 原来只在模型自己收工时由 Emit 写一条。任何**别的**
// 结束方式——步数用满、模型报错、空响应、取消、panic——都直接 return，日志里
// 于是留下一个开了没结局的 turn。只读日志的消费者（回放、UI、history_search、
// 事后取证）拿不到退出码、也拿不到 ACP 的错误帧，只能看见"悬挂"，却无从判断
// 这一轮到底跑完没有。进程侧不静默 ≠ 日志侧不静默。
//
// 用法只有三步：
//
//	tc := newTurnCloser(logw, turn)          // turn/start 之后
//	return tc.End(e.Finish)                  // Emit 的 EventTurnEnd
//	defer tc.Exit(ctx, err)                  // 函数出口（panic 时 ExitOnPanic）
//
// 三条路都走 write，靠 closed 保证**一条 turn 只有一条 turn/end**：重复写会让
// 只读日志的消费者数错轮次（"这一轮结束两次"）。
type turnCloser struct {
	logw   *session.Log
	turn   int
	closed bool
}

func newTurnCloser(logw *session.Log, turn int) *turnCloser {
	return &turnCloser{logw: logw, turn: turn}
}

// End 写"跑完了"的结局：kind 由模型的 finish 映射（截断仍是 max-tokens）。
func (c *turnCloser) End(finish string) error {
	return c.write(session.TurnEndKind(finish), "")
}

// Exit 是统一出口：已经写过就什么都不做，否则按**实际**情况补一条。
//
// err 为 nil 且 ctx 没结束、却走到这里，说明宿主自己漏写了结局——那不是
// "这一轮跑完了"，所以记成 error 而不是 completed：宁可让日志显眼地不对，
// 也不让一个真缺陷被伪装成正常收工。
func (c *turnCloser) Exit(ctx context.Context, err error) error {
	if c == nil || c.closed {
		return nil
	}
	// rc.2：真失败且错误对象自带稳定码时，落盘要保留 code（不折叠成 UNKNOWN）；
	// 取消/超时与无码错误仍走既有的 aborted/error + turnEndForExit 判定。
	if failure, ok := turnEndFailureOfExit(ctx, err); ok {
		return c.writeFailure(failure)
	}
	kind, detail := turnEndForExit(ctx, err)
	return c.write(kind, detail)
}

// ExitOnPanic 在 panic 展开时补一条 error；调用方负责把 panic 原样抛回去。
func (c *turnCloser) ExitOnPanic(v any) error {
	if c == nil || c.closed {
		return nil
	}
	return c.write("error", fmt.Sprintf("宿主 panic，这一轮没有跑完：%v", v))
}

func (c *turnCloser) write(kind, detail string) error {
	if c == nil || c.closed {
		return nil
	}
	if err := c.logw.Append("turn/end", session.TurnEndData(c.turn, kind, detail)); err != nil {
		return err
	}
	c.closed = true
	return nil
}

// writeFailure 写一条**保留 code** 的失败轮（rc.2）。
//
// 与 write 共用 closed 幂等：同一轮只能有一条 turn/end，否则只读日志的消费者
// 会把一轮数成两轮。
func (c *turnCloser) writeFailure(failure session.TurnEndError) error {
	if c == nil || c.closed {
		return nil
	}
	if err := c.logw.Append("turn/end", session.TurnEndErrorData(c.turn, failure)); err != nil {
		return err
	}
	c.closed = true
	return nil
}

// turnEndForExit 给出口错误挑 kind。
//
// 只用 v4 词汇表里已有的值（completed / aborted / blocked / error / max-tokens /
// interrupted / forked），**不新造 kind**：
//   - 取消/超时 → aborted + reason（v4 的 aborted 本来带一句原因；TurnEndData
//     把 detail 放进 reason，形状不变）；
//   - 其余失败 → error + error.message（失败原因原文，不做归纳）；
//   - 不写 completed、也不写 max-tokens：那两种是"这一轮按预期结束"的信号，
//     把失败记成它们等于把错误藏进一个看起来正常的值里。
//
// 步数用满**也走 error**，不是新造一个 max-steps：v4 没有这个词汇，
// 而"没收敛"这件事由 error.message 原文（"turn 用满了 N 步仍未收敛"）说清。
func turnEndForExit(ctx context.Context, err error) (kind, detail string) {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "aborted", turnAbortReason(err)
		}
		return "error", err.Error()
	}
	if ctx != nil && ctx.Err() != nil {
		return "aborted", turnAbortReason(ctx.Err())
	}
	return "error", "宿主没有写出结局：turn 开了，但没有任何结局事件（宿主缺陷，不是这一轮跑完了）"
}

func turnAbortReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "这一轮超时被中止（上下文到期），结果不完整"
	}
	return "这一轮被取消（上下文已结束），结果不完整"
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
	mods, pluginRT, err := loadModsWithPlugins(*manifest, defaultHome(), 0, nil)
	if err != nil {
		return err
	}
	k := kernel.New("dshd")
	root := k.Root()
	defer root.Dispose()
	if _, err := kernel.Assemble(root, mods); err != nil {
		return err
	}
	// 与 run 走同一条路：装载 → 合成目录（静态 + 插件）→ 同一条流水线。
	// 两条命令各写一套的话，`dshd exec` 就成了"模型能调、人调不了"的第二个实现。
	syncPlugins(root.Context(), pluginRT, os.Stderr)
	reg, err := kernel.Inject(root, pluginmod.TableKey)
	if err != nil {
		return err
	}
	runner := hostToolRunner(root, reg, defaultHome(), 0)
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
	model := fs.String("model", envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017"), "摘要模型")
	base := fs.String("base", envOr("DSHD_LLM_BASE", defaultLLMBase), "OpenAI 兼容端点")
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
		// 走和 Transport 的 session.compact 同一段代码：两条入口不该有两套压缩。
		if *dryRun {
			surface := session.Surface(before)
			span, n, ok := compact.Plan(surface, compact.Options{
				KeepRecent: *keep, MinToCompact: 2, MaxMessages: *maxMessages,
			})
			if !ok {
				fmt.Printf("无需压缩：surface %d 节点，保留最近 %d 条后没有可压的前缀\n", len(surface), *keep)
				return nil
			}
			fmt.Printf("计划：压缩 %d 条（seq %d..%d），保留最近 %d 条\n", n, span.Start, span.End, *keep)
			return nil
		}
		res, err := autoCompact(context.Background(), *path, *keep, *maxMessages, *model, *base)
		if err != nil {
			return err
		}
		if !res.Compacted {
			fmt.Printf("无需压缩：保留最近 %d 条后没有可压的前缀\n", *keep)
			return nil
		}
		fmt.Printf("压缩完成：遮蔽 %d 条事件（seq %d..%d），摘要 %d 字\n",
			res.Shadowed, res.From, res.To, res.Summary)
		return nil
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
	home := fs.String("home", defaultHome(), "新会话写到哪里")
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
// deniesSilently 判断这条策略是不是"不问、直接拒"。
// 用于把"策略拒绝"和"人拒绝"分开说——两者对模型的下一步要求不同。
func deniesSilently(policy string, interactive bool) bool {
	switch policy {
	case "deny":
		return true
	case "auto":
		return !interactive
	}
	return false
}

func makeAskFuncWith(policy string, interactive bool) (func(ctx context.Context, sessionID, tool, args string) (bool, error), error) {
	switch policy {
	case "allow":
		return func(context.Context, string, string, string) (bool, error) { return true, nil }, nil
	case "deny":
		return func(context.Context, string, string, string) (bool, error) { return false, nil }, nil
	case "ask", "auto":
		// 宿主把我当子进程养着（serve 会设这个环境变量）：走父子标记通道，不问终端。
		if approvalViaStdio() {
			return markerAsk, nil
		}
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
func runCompactor(chat agent.Chat, logw *session.Log, budget int, policy compact.Policy,
	capacity llm.ModelCapacity, model string, meter *session.Meter) func(context.Context, int, []llm.Message) ([]llm.Message, error) {
	if budget <= 0 {
		return nil
	}
	// 有效阈值 = min(绝对阈值 --auto-compact, 官方 W − O − B 口径)。run 这条
	// 路径与 ACP 走同一个 compact.HostThreshold，两个入口不会压出两种结果。
	threshold := compact.HostThreshold(budget, policy.Budget(capacity.ContextWindow, capacity.MaxOutput))
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
		if meter.Estimate(surface) <= threshold {
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
		// 摘要失败按官方 `compactionRetries`（DSHD_COMPACT_RETRIES，默认 1）重试。
		text, err := compact.SummarizeWithRetry(ctx, chat, model, items, compact.RetryPolicy{
			Retries:         policy.CompactionRetries,
			OverflowRetries: policy.MaxOverflowRetries,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "自动压缩失败（不影响本轮）：%v\n", err)
			return nil, nil
		}
		if _, _, err := session.Compact(logw, back.Events, span,
			[]spi.Block{{Type: "text", Text: text}},
			// Model 必须是**真实模型 id**：v4 里"这条摘要用什么模型压的"只此一份，
			// 写死 "run" 会让它永远无法回答（同一条元数据在 compactauto.go:66 用
			// 调用方给的 model、acp.go:1202 用 r.model，只有这里曾经是字面量）。
			session.CompactionMeta{Provider: "dshd", Model: model}); err != nil {
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

// ---- 官方 0.2.2 #78 `session-checkpoint-policy`：CLI 入口的 durability 侧车 ----
//
// 官方原件（asar 只读解出，dsh-session-checkpoint-policy/lib/index.js）：
//
//	:26-31  llm/stream 之前 flush；:61-66 tools/execute 之前 flush + aborted 判定；
//	:67-70  agent/pre-step 每一步之前 flush。
//
// serve 那条入口已经由 withDurableCheckpointOptions（serve_checkpoint.go）包住；
// `dshd run` 与 `dshd acp` 之前**没有**侧车，所以「模型请求发出前前缀已经耐久」
// 在这两个入口上根本不成立。这里补的就是它们，判据与 serve 完全同源：
//   - 会话解析不出来 → 不落盘也不报错（内层给出的四字段错误更准确）；
//   - 落盘失败 → 响亮失败，调用方据此**不派发**；
//   - 成功 → 同一锚点重复调用是幂等命中（同一轮只落一份）。

// durableCheckpointRequestForRun 把 `dshd run` 的请求压成有界身份。
//
// 与 serve 的 checkpointRequestForBlocks 同一形状：有 content blocks 用 blocks
// 的 canonical JSON，纯文本用 text。两条入口的请求身份必须同源同形，
// 否则崩溃恢复时「同一轮」在两个入口上对不上。
func durableCheckpointRequestForRun(prompt string, blocks []session.ContentBlock) (session.CheckpointRequest, error) {
	if len(blocks) == 0 {
		return session.NewCheckpointRequest("text", []byte(prompt)), nil
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return session.CheckpointRequest{}, fmt.Errorf("durability checkpoint 序列化内容块: %w", err)
	}
	return session.NewCheckpointRequest("blocks", raw), nil
}

// writeDurableCheckpointForSession 是「按会话 id 找文件再落盘」的唯一实现
// （run / acp 的 prompt、pre-step、tools/execute 三条时机共用）。
//
// req 为 nil 表示这次 flush 没有请求身份（tools/execute 与 agent/pre-step
// 官方就只是 flush，没有请求载荷）：侧车仍记前缀字节与事件数，
// 同一锚点先到的身份会赢（重试时文案可能改过，锚点相同就是同一轮）。
func writeDurableCheckpointForSession(home, sessionID string, req *session.CheckpointRequest) error {
	if sessionID == "" {
		return fmt.Errorf("durability checkpoint 需要会话 id")
	}
	path, err := newSessionSource(home, "").resolve(sessionID)
	if err != nil {
		// 不吞错也不改写语义：会话不存在时内层的错误更准确（ErrNoSuchSession）。
		return nil
	}
	if _, err := session.WriteDurableCheckpoint(home, path, req); err != nil {
		return fmt.Errorf("durability checkpoint 落盘失败: %w", err)
	}
	return nil
}
