package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/modules/tool/terminalpanel"
)

var update = flag.Bool("update", false, "重写 golden 快照")

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

func assembleRegistry(t *testing.T) spi.ToolRegistry {
	t.Helper()
	// 工具表必须与**开发者环境**无关：`tool-terminalpanel` 的 readUserTerminals
	// 是从宿主环境变量解析的（见 modules/tool/terminalpanel/config.go）。外面
	// 恰好导出了 `DSHD_TERMINAL_PANEL_READ_USER_TERMINALS=0` 时，这两个工具会消失，
	// 快照就以一种完全看不出原因的方式变红。这里钉死"官方默认部署"的取值。
	t.Setenv(terminalpanel.EnvReadUserTerminals, "true")
	k := kernel.New("snapshot")
	t.Cleanup(k.Root().Dispose)
	if _, err := kernel.Assemble(k.Root(), builtinModules(".", t.TempDir(), 0, 0, nil)); err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestToolTableSnapshot 是 R4 的落点：模型看到的工具表（名字 + 描述 + 参数 schema）
// 一旦变化，必须先更新快照，且 diff 必须出现在提交里。
func TestToolTableSnapshot(t *testing.T) {
	reg := assembleRegistry(t)
	table, err := buildToolTable(reg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join("testdata", "tools.snapshot.json")
	if *update {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("已更新 %s（%d 个工具）", path, len(table))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到快照 %s：首次生成请跑 go test ./cmd/dshd -run TestToolTableSnapshot -update", path)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != string(got)+"\n" {
		t.Fatalf("工具表变了（R4）。确认是有意的改动后跑：\n  go test ./cmd/dshd -run TestToolTableSnapshot -update\nthen diff testdata/tools.snapshot.json")
	}
}

// TestToolTableIsStableAcrossAssemblies 保证同一装配跑两次产生同样字节。
func TestToolTableIsStableAcrossAssemblies(t *testing.T) {
	a, err := json.Marshal(buildToolTableMust(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(buildToolTableMust(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("两次装配的工具表不一致：顺序或指针地址泄漏进 schema")
	}
}

func buildToolTableMust(t *testing.T) []ToolEntry {
	t.Helper()
	table, err := buildToolTable(assembleRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	return table
}

// TestWriteToolsDeclareCaps 是 R3.6 的门：能力声明不许靠猜，新工具必须在这里被显式认领。
func TestWriteToolsDeclareCaps(t *testing.T) {
	want := map[string]spi.Caps{
		"read":           {},
		"glob":           {},
		"grep":           {},
		"job_list":       {},
		"history_search": {}, // 只读：在原始日志里按关键词回查
		"job_output":     {},
		"write":          {Writes: true},
		"edit":           {Writes: true},
		"bash":           {Writes: true, Executes: true},
		"job_kill":       {Destructive: true},
		// 只改进程自己的 cwd：不写盘、不起进程、不联网。
		// 它能让后续 write 落到别处，但 write 自己那道审批才是闸门。
		"working_directory": {},
		// 只记清单在内存里，不写盘、不起进程、不联网。
		"todo_write": {},
		// 计划模式的两个工具：只改进程内的模式位，不写盘、不起进程、不联网。
		// 它们必须自己是"干净"的——否则在最严的模式里连退出都退不出来。
		"enter_plan_mode": {},
		"exit_plan_mode":  {},
		// 只 stat 文件、登记路径，不写不跑不联。
		"present": {},
		// 只把问题返回给模型并结束本轮，不碰文件系统。
		"ask_user_question": {},
		// 联网取网页正文。联网本身就是它的功能，不是副作用。
		"web_fetch": {Network: true},
		// 联网搜索，同样按 Network 走审批。
		"web_search": {Network: true},
		// 只读图片文件并把图交给模型，不写不跑不联。
		"read_image": {},
		// 跑 PowerShell：起进程 = Executes（写盘拦不住，Writes 由工具自己声明）。
		"pwsh": {Executes: true, Writes: true},
		// 调 GitHub REST API：联网 + 可能写，两样都得审批。
		"github": {Network: true, Writes: true},
		// 只读 git 配置（remote/branch），不起副作用。
		"repo_here": {},
		// 起 .exe（参数不做转换）。它自己多声明了 Writes——任意程序都可能写盘，
		// 这个保守判断是对的，比我在任务书里写的更安全。
		"exec_literal": {Writes: true, Executes: true},
		// 纯读时钟。
		"now_line": {},
		// 团队任务板：读两个、写两个。
		"team_task_create": {Writes: true},
		"team_task_list":   {},
		"team_task_get":    {},
		"team_task_update": {Writes: true},
		// 会话间通信：发信写盘，读信与列会话只读。
		"send_message": {Writes: true},
		// 写 <home>/state/team/agents.json：登记队友名单。
		"spawn_teammate": {Writes: true},
		"wait_agent":     {},
		"list_agents":    {},
		// 目标：读一个、写两个。
		"get_goal":    {},
		"create_goal": {Writes: true},
		"update_goal": {Writes: true},
		// 定时任务：写的有 create / edit（含两个 deprecated 别名 update、delete），
		// 只读的有 list / view（官方 rc.2 的 schedule_list 与 schedule_view）。
		"schedule_create": {Writes: true},
		"schedule_list":   {},
		"schedule_view":   {},
		"schedule_edit":   {Writes: true},
		"schedule_update": {Writes: true},
		"schedule_delete": {Writes: true},
		// 盯仓库：三个写、一个读。
		"watch_start":  {Writes: true},
		"watch_status": {},
		"watch_stop":   {Writes: true},
		"watch_ack":    {Writes: true},
		// 体检：不写盘，但**起 gh 子进程且 gh 要联网**。漏报就会在 deny 策略下
		// 静默放行一次联网调用。
		"watch_doctor": {Executes: true, Network: true},
		// 派子代理：用的是同一张工具表与同一个审批守卫，能力不放大。
		// 它自己不算"写盘/起进程/联网"，那些由被派出去的工具各自声明。
		"subagent": {},
		// 编排工具自己不写盘、不起进程：真正干活的是子调用，
		// 而子调用走的是同一套受管通道（审批、闸门、spill 全都还在）。
		"workflow": {},
		// 插件管理改的是 dsh.toml（manifest 白名单），所以按"写"报。
		"plugin": {Writes: true},
		// 中断队友（`interrupt_agent`）在 rc.2 起**默认不注册**：官方把
		// `tool-subagent-control.interrupt` 的内置值设为 false（升级指南
		// subagent-interrupt-default），模型默认拿不到这个工具，所以这里也不认领。
		// 打开它（`DSHD_TOOL_SUBAGENT_CONTROL_INTERRUPT=1`）后能力仍是"只改标记、
		// 不杀进程"，判据见 modules/tool/team 的测试。
		// 刷新按钮：只动 <home>/state/unlag.json 这一份宿主状态，不算写用户文件。
		"unlag_refresh": {},
		// 带上下文的子代理：和 subagent 一样本身不写盘/不联网，
		// 真正干什么由它调用的工具各自声明。
		"subagent_fork": {},
		// 只读自己的技能目录，不写盘、不起进程、不联网。
		"skill": {},
		// 只把卡片提案交给会话投影；真正的动作要等用户点击后才由 kind 执行，
		// 工具本身不写盘、不起进程、不联网。
		"propose_action": {},
		// 查运行时依赖：**起 python/node 子进程**读版本，所以 Executes 为真。
		// 不写盘、不联网（子进程只读版本号）。
		"load_workspace_dependencies": {Executes: true},
		// 写本机记忆目录（<home>/memory），不联网、不起进程。
		"hindsight_ingest_document": {Writes: true},
		// 本地记忆：读三个、写两个（建目录写文件，不联网、不起进程）。
		// Office 三工具（rc.2 官方 tool-office 面）：render 只起原生引擎进程（Executes），
		// convert / recalculate 还要把产物写进宿主私有产物仓（Writes+Executes）。
		// 都不写工作区、不联网，所以不声明 Network。
		"office_render":      {Executes: true},
		"office_convert":     {Writes: true, Executes: true},
		"office_recalculate": {Writes: true, Executes: true},
		// rc.2 官方 `dsh-terminal-panel` 的两个**只读**工具：
		// 只读 serve 父进程的终端 roster / 有界滚动缓冲尾部，不写盘、不起进程、
		// 不联网（数据走既有 stdio 帧，不是网络）。见 terminal_bridge.go。
		"user_terminal_list": {},
		"user_terminal_read": {},
		// 记一个倡议：和 ingest 一样建目录写文件，不联网、不起进程。
		"hindsight_capture_initiative":     {Writes: true},
		"hindsight_list_knowledge_pages":   {},
		"hindsight_search_knowledge_pages": {},
		"hindsight_read_knowledge_page":    {},
	}
	seen := map[string]bool{}
	for _, e := range buildToolTableMust(t) {
		w, ok := want[e.Name]
		if !ok {
			t.Fatalf("新工具 %q 没有认领能力：请在上面这张表里显式写清它会不会写盘/起进程/联网", e.Name)
		}
		seen[e.Name] = true
		if e.Caps != w {
			t.Errorf("%q 的 caps = %+v，期望 %+v", e.Name, e.Caps, w)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("能力表里的 %q 没有出现在工具表里（删了工具忘了删表？）", name)
		}
	}
}

// officialVerbatimDescriptions 是"描述逐字来自官方 catalog，因此**豁免** 120 字
// 上限"的工具（名字 → 官方出处）。
//
// 为什么必须有这份名单，而不是直接调高上限：官方 rc.2 的 `dsh-terminal-panel`
// 自己就发 **298 字**的 `user_terminal_list` 描述（`tool-catalog.md:1528`）与
// **173 字**的 `user_terminal_read`（`:1541`）。把"照抄官方原文"判红，等于逼我们
// 篡改官方文案——那违反"素材复用/官方形状"的根本原则（AGENTS.md §7）。
//
// 所以判据改成**只约束宿主自撰描述**：那才是这条门禁真正要管的东西（"别自己
// 写长文挤工具表预算"）。逐字来自官方的描述登记在这里，长度由官方原文钉住。
// "逐字"这件事本身不由本文件证明：`modules/tool/terminalpanel/tools_test.go` 的
// `TestDescriptionsMatchCatalog` 把两条描述与官方 catalog 逐字比对；本表只是
// 允许它们超过自撰上限的那一张清单。
var officialVerbatimDescriptions = map[string]string{
	"user_terminal_list": "官方 tool-catalog.md:1528（298 字，逐字）",
	"user_terminal_read": "官方 tool-catalog.md:1541（173 字，逐字）",
}

// TestEveryToolHasDescription 是模型体验的底线：没有描述的工具模型只能猜什么时候用。
//
// 长度上限 120 字只针对**宿主自撰**的描述；逐字照抄官方的登记在
// officialVerbatimDescriptions 里（见该表的注释）。
func TestEveryToolHasDescription(t *testing.T) {
	for _, e := range buildToolTableMust(t) {
		if strings.TrimSpace(e.Description) == "" {
			t.Errorf("工具 %q 没有 Description", e.Name)
		}
		if utf8.RuneCountInString(e.Description) > 120 {
			if src, ok := officialVerbatimDescriptions[e.Name]; ok {
				t.Logf("工具 %q 的描述逐字来自 %s，豁免宿主自撰描述的 120 字上限", e.Name, src)
				continue
			}
			t.Errorf("工具 %q 的 Description 太长（%d 字），会挤占工具表预算", e.Name, utf8.RuneCountInString(e.Description))
		}
		var v any
		if err := json.Unmarshal(e.Parameters, &v); err != nil {
			t.Errorf("工具 %q 的 parameters 不是合法 JSON: %v", e.Name, err)
		}
	}
}
