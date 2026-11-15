package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"crypto/rand"
	"encoding/binary"

	"dshgo/internal/agent"
	"dshgo/internal/config"
	"dshgo/internal/cua"
	"dshgo/internal/llm"
	"dshgo/internal/modes"
	"dshgo/internal/native"
	"dshgo/internal/officekit"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/session"
	"dshgo/internal/settingsform"
	"dshgo/internal/transport"
	"dshgo/internal/updater"
	"dshgo/modules/exec/bash"
	"dshgo/modules/tool/websearch/providerapi"
)

// errNoModel 是 serve 的 turnRunner 给出的回答：**故意**失败。
//
// 这不是"忘了做"。真起一轮要有模型、工具流水线、审批与落盘那一整套
// （cmdRun / cmdACP 里的东西），而 serve 这一版只负责把链路打通：
// 界面能连上、能列会话、能读首屏与历史、能发 prompt 并拿到 turnID。
//
// 如果这里假装成功（返回 nil 而不产生任何事件），界面会看到
// "prompt 发出去了、turnID 也有了，但 WS 上永远没人说话"——那种沉默
// 比一条明确的错误难查十倍。所以 runner 直接拒绝，让"没接模型"这件事
// 在第一时间以错误的形式说出来。
var errNoModel = errors.New("serve 还没接模型；prompt 用来看链路，不产生真实回答")

// noModelRunner 是 turnRunner 的空实现：每次 Run 都拒绝。
//
// promptService 会把这个错误丢掉（它只负责清 busy），但接口语义是对的：
// 这一轮**失败了**，不是"还在跑"。
type noModelRunner struct {
	// journals 是所有帧的序号来源（H14）。这一类型通常没有流事件，
	// 但结局帧仍要与其它帧同一个域，否则客户端补洞时缺了"这一轮结束了"。
	journals *transport.Journals
	// pub 是推事件的出口。为 nil 时只记错误，不推。
	pub func(topic string, f transport.ServerFrame)
}

func (r noModelRunner) Run(_ context.Context, sessionID, turnID, _ string) error {
	err := errNoModel
	// prompt 已经回过 200 了，界面唯一的消息来源就是这里。
	// 不推这一帧，界面会永远停在"正在思考"。
	emitTurnFrame(r.journals, r.pub, sessionID, turnID, err)
	return err
}

// announcePolicy 把子进程实际使用的审批策略打到屏幕上。
//
// 为什么必须打：serve 无人值守，用户看不见这条，就会把"工具被拒"当成
// "模型不会用工具"。**看不见的放宽与失误没有区别。**
//
// 这句话还必须跟着**实际策略**变：以前写死"写操作会被拒绝"，默认改成 allow
// 之后它就成了假消息——屏幕上说拒绝、实际放行，比不打印更糟。
func announcePolicy(w io.Writer, policy string, maxSteps int) {
	switch policy {
	case "allow":
		fmt.Fprintf(w, "policy allow（无人值守，完全权限；要逐次询问请写 --policy ask）；每轮最多 %d 步\n", maxSteps)
	case "ask":
		fmt.Fprintf(w, "policy ask（无人值守；审批会转给界面，没人应答就按拒绝处理）；每轮最多 %d 步\n", maxSteps)
	default:
		fmt.Fprintf(w, "policy %s（无人值守；写操作会被拒绝）；每轮最多 %d 步\n", policy, maxSteps)
	}
}

// announceToolsMode 把子进程实际使用的工具表形态打到屏幕上。
//
// 和审批策略同一条理由：**看不见的形态与没打开没有区别**。lazy 与 compact 下
// 模型的行为不一样（表外工具先被挡回、要多一次 tool_load），出问题时第一句
// 该问的是"这个进程到底跑的是哪一档"——而不是去猜"模型为什么说没有这个工具"。
func announceToolsMode(w io.Writer, mode string) {
	if mode == "" {
		mode = defaultToolSchema
	}
	if mode == toolsModeLazy {
		fmt.Fprintf(w, "工具表 lazy（只常驻语料前 %d 名；表外工具先 tool_load 取，"+
			"装载集按会话从 v4 日志回放）\n", len(defaultHotTools))
		return
	}
	fmt.Fprintf(w, "工具表 %s\n", mode)
}

// readyLine 是打到 stdout 的那一行，壳按它解析。
//
// 字段顺序由结构体顺序决定（encoding/json 的保证），壳可以按字面解析；
// 但更稳的做法是按 JSON 解析——这里保持最小形状，不额外加字段。
type readyLine struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// cmdServe 实现 `dshd serve --home <dir> [--addr 127.0.0.1:0]`。
//
// 起来之后往 stdout 打**一行** ready 信息，格式必须完全一致（壳要解析它）：
//
//	ready {"url":"http://127.0.0.1:52341","token":"<一次性 token>"}
//
// 然后阻塞到 Ctrl-C / SIGTERM，收到信号后 Close() 再退出。
//
// 三个要点：
//   - 端口默认交给 OS（--addr 留空 → transport 用 127.0.0.1:0），不许写死；
//   - URL 只用 Start() 的返回值，绝不自己拼（壳与宿主必须看到同一个地址）；
//   - stdout 只放那一行 ready。日志/提示一律走 stderr，免得壳把诊断当协议解析。
//
// newEpochFunc 造一个"跨进程唯一且单调"的 epoch 发生器。
//
// 单独成函数是为了可测：两次调用必须给出不相交的区间——这正是
// "宿主重启后 epoch 撞号、客户端静默丢帧"那个 bug 的判据。
func newEpochFunc() func() int64 {
	// 基准用**随机 63 位**，不用时间：时间是"两次启动可能落在同一毫秒"的，
	// 而这里要的是"绝不可能撞"（撞了客户端会静默丢帧）。同一进程内再自增。
	var b [8]byte
	base := time.Now().UnixNano() // 随机源不可用时的兜底，绝不会走到
	if _, err := rand.Read(b[:]); err == nil {
		base = int64(binary.LittleEndian.Uint64(b[:]) & 0x7FFF_FFFF_FFFF_FFFF)
	}
	var n int64
	return func() int64 { n++; return base + n }
}

// defaultTurnMaxSteps 是**每轮步数上限**的唯一默认值：run / serve / acp / schedule
// 四个入口共用同一个数，不留第二套默认。
//
// # 为什么是 2000，不是 8/12
//
// 官方 40 个真实会话 3060 轮里，**1519 轮**的最大 step ≥ 9；全体 assistant step
// 分位是 p50=13 / p90=90 / p95=174 / p99=938 / max=1318，>200 的样本 11507 个。
// 也就是说"8 步"会砍掉一半以上的真实轮次——用户看到的是
// `turn 用满了 8 步仍未收敛（模型一直在请求工具）`，而那一轮本来能跑完。
//
// 步数是**安全阀**，不是预算：正常情况下模型自己会在没有工具调用时收敛
// （loop 里 `len(resp.ToolCalls)==0` 就结束并写 turn/end）。只有模型真的陷入
// 循环时才需要这个上限把人从死循环里救出来，所以它必须显著高于真实分布的
// p99，而不是刚好卡在分布中间。2000 仍远低于"无限"：一次真死循环会在 2000 步
// 内被截断，且错误文案照旧是"用满仍未收敛"（不吞错、不改结局语义）。
//
// 能力下限规则（AGENTS.md）：只能比官方宿主更多、更强，不能更少、更弱。
const defaultTurnMaxSteps = 2000

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	home := fs.String("home", envOr("DSHD_HOME", ""), "会话根目录（必填）")
	// 留空 = 让 transport 用 127.0.0.1:0，端口由 OS 分配。
	serveSteps := fs.Int("max-steps", defaultTurnMaxSteps, "每轮最多几步（子进程 run 的上限）")
	// H12：会话内容列宽（逻辑像素）。0 = 不知道，界面自己算。
	// 为什么不给默认值：官方是 min(748*0.702, 82%)，直接抄 748 会让别的窗口尺寸下偏宽/偏窄。
	serveWidth := fs.Int("content-width", 0, "会话内容列宽（逻辑像素）；0 = 不知道，界面自己算")
	// ask 现在是**能用**的：子进程走 @@APPROVAL@@ 标记通道，把问题转给界面（H13）。
	// 这条帮助文字以前写着"ask 等于永远挂起"，那是 H13 之前的事实，
	// 留着它会让人以为审批不能用——**过期的帮助文本比没有帮助更糟**。
	// 默认 allow：用户 2026-11-04 要求「默认审批改成完全权限，不要老是弹权限申请」。
	// 以前默认 deny——那会让界面连上来之后所有写工具都失败，而失败的样子很像
	// "模型不会用工具"。要逐次询问写 --policy ask，要只读写 --policy deny。
	servePolicy := fs.String("policy", "allow", "子进程用哪个审批策略：allow（默认，完全权限）/ask（转给界面）/deny")
	// 工具表形态：默认 compact（和 run 一致）。lazy 只显式打开——它改的是**模型
	// 行为**（表外工具要多一次 tool_load），不是纯字节账，见 docs/tool-table-lazy.md §3。
	serveToolsMode := fs.String("tools-mode", "",
		"工具表形态：compact（默认）| full | merged | lazy（只常驻最常用的 30 个，其余先用 tool_load 取；装载集按会话从 v4 日志回放，默认关闭）")
	// 模型必须能选：serve 起子进程跑真模型，用户换模型却只能去改环境变量，
	// 那是"藏起来的开关"。默认值和 run 保持一致。
	// 宿主旋钮：自动压缩阈值 / 工具输出外置阈值 / AGENTS.md 字节上限 /
	// 显式容量 / 规则文件。以前 serve 侧连这些旗标都没有，于是生产链上它们
	// 恒为子进程 run 的写死默认值（40000 / 4000 / 65536 / 0 / 0），
	// **从 serve 侧根本调不了**。默认值与 run 同源（见 host_run_options.go）。
	serveRunOpts := addHostRunFlags(fs)
	serveModel := fs.String("model", envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017"), "模型 id（透传给子进程）")
	// 推理强度透传给子进程；空 = 不要。界面线要能看到 reasoning 就得从这里打开。
	serveReasoning := fs.String("reasoning", os.Getenv("DSHD_REASONING_EFFORT"),
		"推理强度：空=不要（默认）| xhigh | max")
	addr := fs.String("addr", "", "监听地址（默认 127.0.0.1:0，端口由 OS 分配）")
	// 父进程看门狗（--parent-watch / --parent-watch-interval / --parent-pid）：
	// 三个入口共用同一组开关，见 parentwatch.go。
	pw := addParentWatchFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicitServeModel := strings.TrimSpace(os.Getenv("DSHD_LLM_MODEL")) != "" || strings.TrimSpace(os.Getenv("DSH_GO_MODEL")) != ""
	fs.Visit(func(current *flag.Flag) {
		if current.Name == "model" {
			explicitServeModel = true
		}
	})
	if *home == "" {
		return errors.New("缺 --home")
	}
	// 和 run/acp 同一条防线：--home 指到文件上要在建任何东西之前就说清楚。
	if err := checkHome(*home); err != nil {
		return err
	}
	// 沙盒 extra read root 是 <home>/artifacts；它在全新 home 上还不存在，
	// 而 sandbox 对不存在的 root fail-closed。serve 必须自举自己的 SpillDir，
	// 否则第一条 prompt 就会因为"还没外置过文件"而失败。
	if err := ensureSpillDir(*home); err != nil {
		return err
	}
	// spill 保留期回收（官方 session.spill.cleanupPeriodDays，默认 30 天）：
	// 启动时扫一遍 <home>/artifacts，删掉过期的外置文件；失败只记日志。
	sweepSpillArtifactsBestEffort(*home)
	// 看门狗要**尽早**起：启动阶段也算数。父进程在 serve 还没 ready 之前就死了，
	// 否则会留下一个"起来得正好、但没人要"的服务。
	watch := pw.start(os.Stderr)
	defer watch.Stop()
	// 工具表形态：和 run 共用同一个解析（新名优先，旧名 compact 兜底），
	// 拼错的形态在这里就报错——不能静默退回某一档，那等于让 --tools-mode 变成假开关。
	//
	// serve 这一侧只做"解析 + 原样传给子进程"：工具表的投影（合并/收紧/lazy）
	// 和 lazy 的装载集回放都在**子进程**里，而那个子进程就是 cmdRun 本身
	// （见 serve_tools.go 的 childArgs）。父进程不再建第二张表——
	// 否则它就是同一件事的第二个实现，迟早和 run 漂开。
	toolsMode, err := resolveToolsMode(*serveToolsMode, defaultToolSchema)
	if err != nil {
		return err
	}

	// CLI 用显式 --root；serve 的唯一推导是必需的 --home 下的固定子目录。
	// Open 在返回前完成启动 reconcile 与当前包校验，损坏/悬挂事务必须在
	// listener ready 之前失败，不能先对外暴露一个状态未定的插件面。
	pluginManager, err := pluginmgr.Open(filepath.Join(*home, "plugins"))
	if err != nil {
		return fmt.Errorf("插件管理器启动 reconcile 失败: %w", err)
	}
	// 给 serve 的管理器挂上运行时装载器：`plugin.enable` / `plugin.disable`
	// 经 HTTP（GUI 的插件管理面）真的装载/卸载，而不是只改一个布尔位。
	//
	// 为什么 serve 也要挂（serve 自己并不执行工具）：
	//   - 不挂的话，坏插件在 HTTP 上会返回"启用成功"，用户要到下一轮才发现
	//     模型看不见它——这正是本任务要消灭的静默成功；
	//   - 挂上之后 enable 先装载再提交（失败则状态不动、四字段错误带原因）。
	//
	// 为什么**不**在这里 Sync：serve 不跑工具，把 enabled 的 wasm 全编译一遍
	// 只是为了占内存。真正把工具交到模型手上的那一侧是每轮的 `dshd run`
	// 子进程，它在启动时 Sync（见 cmd/dshd/main.go 的 syncPlugins）。
	pluginRT, err := newPluginRuntimeWithHMR(pluginManager, pluginHMRConfig(filepath.Join(*home, "plugins")))
	if err != nil {
		return fmt.Errorf("插件运行时挂载失败: %w", err)
	}
	defer func() { _ = pluginRT.Close(context.Background()) }()

	// 官方 #6 `deepseek-llm-api-extensions` + #17 `plugin-package-inventory-deepseek`
	// 的生产注册点（装配期一次）：本进程之后发出的每条 DeepSeek 官方协议模型请求
	// 都带 `dsh_plugin_packages`（活跃包库存，每次请求现读 manager+loader 投影）。
	// 见 serve_llm_extensions.go。注册失败只让这一个字段缺席，不拦 HTTP 服务：
	// 但必须说出来，否则"注册了却发不出去"又会变成一次静默成功。
	if err := registerPluginPackagesExtension(pluginRT); err != nil {
		fmt.Fprintf(os.Stderr, "DeepSeek 请求扩展注册失败（%v）；dsh_plugin_packages 不会出现在模型请求里\n", err)
	}

	// 插件热重载（HS-14）：**默认关闭**。pluginHMRConfig 的零值就是 disabled，
	// 只有显式 `DSHD_PLUGIN_HMR=1` 才走这个分支；关闭时连 Run 都不调用
	// （Run 对关闭状态直接回 ErrHMROff，那是调用方的 bug 而不是"空转成功"）。
	//
	// HMR 只读 `packages/*/*/plugin.json` 并调 Loader.Reload，没有任何写文件
	// 的代码路径；宿主的整树热重载（profiles/*/cordis.patch.yml）不归它管。
	if pluginRT.HMR().Enabled() {
		hmrCtx, hmrCancel := context.WithCancel(context.Background())
		defer hmrCancel()
		defer pluginRT.HMR().Stop()
		go func() { _ = pluginRT.HMR().Run(hmrCtx) }()
		fmt.Fprintf(os.Stderr, "插件热重载已开启（DSHD_PLUGIN_HMR=1，根 %s，周期 2s）\n", filepath.Join(*home, "plugins"))
	}

	// 终端服务：serve 模式不装配模块，所以单独建一份（和 run/acp 那边各自一份同构）。
	bashSvc := bash.NewService("")
	defer bashSvc.Close()

	// 会话级模型覆盖：启动时先检查一次；损坏文件不拦 HTTP 服务，但后续模型
	// 轮次会明确报错，绝不悄悄按默认模型跑出一个假成功。
	modelOverrides := newModelOverrideStore(*home)
	modelLoadErr := modelOverrides.Load()
	if modelLoadErr != nil {
		fmt.Fprintf(os.Stderr, "会话模型覆盖读盘失败（%v）；修复前模型轮次会报错\n", modelLoadErr)
	}

	// 会话级审批策略覆盖（同款；读盘失败就全按 --policy 走）。
	permissionOverrides := newPermissionOverrideStore(*home)
	permissionLoadErr := permissionOverrides.Load()
	if permissionLoadErr != nil {
		fmt.Fprintf(os.Stderr, "会话权限覆盖读盘失败（%v），本轮全部按 --policy 走\n", permissionLoadErr)
	}
	// 通用设置直接复用上面两份 runner 已读取的存储，不读 ui-config。
	settingsStore := newServeSettingsStore(*serveModel, *servePolicy, modelOverrides, permissionOverrides)
	settingsStore.setLoadErrors(modelLoadErr, permissionLoadErr)

	// 更新渠道：官方把它归**壳配置**（`dsh-client-ui-settings-general/src/types.ts:60`
	// 「UI choices persist in the shell configuration, independently of Host settings」），
	// 所以它**不是** settings 命名空间，落 `<home>/state/update-channel.json`。
	//
	// 本构建自带的渠道由打包决定；本仓只有 stable 一种产物，所以 Packaged=stable。
	updateChannel, channelErr := updater.New(updater.Options{
		Dir:      filepath.Join(*home, "state"),
		Packaged: updater.ChannelStable,
	})
	if channelErr != nil {
		// 壳配置目录算不出来就是装配错误：响亮失败，不静默退化成"没有更新渠道"。
		return fmt.Errorf("初始化更新渠道服务: %w", channelErr)
	}
	_ = updateChannel // 在 srv 建好后挂上（见下面 AttachDesktopUpdateChannel）

	// 更新状态：渠道服务的**兄弟**——那边管"我选哪个渠道"，这边管"现在有没有新版本、
	// 更新入口在哪"（官方 `dsh-client-ui-settings-general/src/types.ts:30-35`）。
	//
	// 更新来源是**我们自己的 GitHub 仓**：`GET /repos/MYCF711/dsh-go/releases`
	// （用户 2026-11-13 指令「走我们的 go 仓库更新」），不是官方的 Electron 更新通道。
	// 运行中版本从 `main.version` 注入——版本号唯一出处是 `cmd/dshd/main.go`。
	//
	// 出网策略：进程启动不发请求，只有界面问 `desktopUpdate.status`（观察）或
	// 用户点 `desktopUpdate.open`（重试）才查；TTL 缓存 + 单飞 + ETag 条件请求。
	githubToken := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if githubToken == "" {
		githubToken = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}
	updateStatus, statusErr := updater.NewStatusService(updater.StatusOptions{
		Version: version,
		Channel: updateChannel,
		Token:   githubToken,
	})
	if statusErr != nil {
		// 运行版本解析不了 / 没有渠道来源都是装配错误：响亮失败，
		// 不静默退化成"永远查不到更新"（那种沉默比一条启动错误难查十倍）。
		return fmt.Errorf("初始化更新状态服务: %w", statusErr)
	}
	// 渠道一变过滤规则就变了（stable 不看 prerelease），缓存的结论立刻作废。
	unsubscribeChannelChange := updateChannel.Subscribe(func(updater.State) { updateStatus.Invalidate() })
	defer unsubscribeChannelChange()
	// 持久模型目录与 active 默认共用 W61 store；目录本身不保存第二个 active。
	// 损坏目录只让目录 API fail-loud，绝不拿空目录覆盖原文件。
	modelCatalog := openHostModelCatalog(*home)
	if _, err := modelCatalog.List(); err != nil {
		fmt.Fprintf(os.Stderr, "模型目录读盘失败（%v）；修复前目录 API 会报错\n", err)
	}
	if modelCatalog.Missing() && explicitServeModel {
		discoveryCtx, cancelDiscovery := context.WithTimeout(context.Background(), 3*time.Second)
		applied, discoveryErr := configureEnvModelCatalog(discoveryCtx, modelCatalog, *serveModel,
			envOr("DSHD_LLM_BASE", defaultLLMBase), os.Getenv("DSHD_LLM_KEY"))
		cancelDiscovery()
		if discoveryErr != nil {
			fmt.Fprintf(os.Stderr, "env 模型元数据发现失败（%v）；catalog/effort 能力保持未知\n", llm.RedactCredential(discoveryErr.Error(), os.Getenv("DSHD_LLM_KEY")))
		} else if applied {
			fmt.Fprintf(os.Stderr, "已从当前 provider 投影 env 模型 %q 的真实 catalog 元数据（未创建 catalog 文件）\n", *serveModel)
		} else {
			fmt.Fprintf(os.Stderr, "provider 未返回 env 模型 %q 的元数据；catalog/effort 能力保持未知\n", *serveModel)
		}
	}

	// 会话级 effort 覆盖只存 catalog key；损坏文件在后续读写中持续 fail-loud。
	effortOverrides := newEffortOverrideStore(*home)
	if err := effortOverrides.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "会话 effort 覆盖读盘失败（%v）；修复前 effort API/runner 会报错\n", err)
	}

	// 会话级 goal 是真实 snapshot；旧扁平文件在 Load 内同步迁移并持久 id/revision。
	goalOverrides := newGoalOverrideStore(*home)
	if err := goalOverrides.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "会话 goal 读盘失败（%v），界面不显示目标条\n", err)
	}

	// 会话数据的来源：宿主这一侧才被允许读 v4 日志（红线 R7）。
	// cwd 传空 = 按 id 全盘找，与 `session.resume` 的兜底一致。
	src := newSessionSource(*home, "")
	src.perms = permissionOverrides
	src.goals = goalOverrides
	src.efforts = effortOverrides
	src.catalog = modelCatalog
	// session.load 的 contextWindow 要按"这一轮会用的模型"解析：新会话的 v4 里
	// 还没有 request/context 事件，得靠会话覆盖或宿主默认 --model 才画得出上下文占用。
	src.models = modelOverrides
	src.defaultModel = *serveModel
	attachments := newAttachmentStore(*home)
	mcpServers := openMCPManager(*home)
	mcpServers.StartAll()
	defer mcpServers.Close()
	sideQueries := newSideQueryService(*home, *serveModel, envOr("DSHD_LLM_BASE", defaultLLMBase), envOr("DSHD_LLM_API", ""), *serveReasoning, src)
	defer sideQueries.Close()

	// Prompt / Cancel 走同一个服务，busy 的唯一结束点由它保证。
	// runner 现在固定拒绝（见 errNoModel 的注释）。
	// runner 要先建好（prompt 服务要它），但推事件的出口得等 srv 建完才有——
	// 所以用指针，建完再赋。
	// 子进程跑 run：工具循环、审批、压缩、落盘全部走既有的那一套。
	// DSHD_SERVE_MODEL_RUNNER=1 时走"不起子进程"的 runner。
	// 只为二分长跑时的内存增长：增长到底来自"跑一轮"，还是"子进程 + follow"。
	var runner turnRunner
	// 优雅退出时要停的自动标题服务（只有 modelOverride 路径有）。
	var moRunner *modelOverrideLoopRunner
	useModelRunner := os.Getenv("DSHD_SERVE_MODEL_RUNNER") == "1"
	// 一个 serve 进程内所有会话共用一张 journal 表：里面按 sessionID 分域，
	// 所以"共用表"不等于"共用序号"（H14）。
	// epoch 必须是**跨进程唯一**的。
	//
	// 踩过（界面审计实测复现）：原来是进程内自增（1,2,3…），宿主重启后
	// 同一会话又拿到 epoch=1。客户端带着旧水位 {sinceSeq:1, journalEpoch:1} 重订，
	// 世代"匹配"所以不发 session/reset，而 hub 按 seq > sinceSeq 过滤，
	// 把新 journal 的 seq=1 丢掉 —— 8 秒 0 帧，history 里明明有那条事件。
	// 客户端无报错、无 gap，界面表现就是"发出去没反应"。
	//
	// 改成拿启动时刻做基准：两次启动撞上同一纳秒不可能，而它仍然是单调递增的整数。
	journals := transport.NewJournals(newEpochFunc())
	// 跨进程恢复的是原始 Transport 帧，不是 v4 首屏投影。启动时只读恢复；
	// 新 epoch 仍由本进程分配，旧 epoch/水位继续走 reset/stale_epoch。
	if _, err := restoreServeJournals(*home, journals); err != nil {
		return fmt.Errorf("启动恢复 Transport journal 失败：%w", err)
	}
	// 沙盒策略层：档位存 <home>/state/session-sandbox.json；preset 目录可扩展。
	sandboxStore := newSandboxModeStore(*home)
	if err := sandboxStore.Load(); err != nil {
		return err
	}
	diskPresets, err := modes.LoadDir(filepath.Join(*home, "modes"))
	if err != nil {
		return err
	}
	sandboxPresets, err := modes.NewRegistry(diskPresets)
	if err != nil {
		return err
	}

	goalOverrides.journal = &goalChangeJournal{journals: journals}
	jobActions := newJobActionBroker(*home)
	lr := &loopRunner{home: *home, policy: *servePolicy, maxSteps: *serveSteps,
		journals: journals, reasoning: *serveReasoning, model: *serveModel,
		toolsMode: toolsMode, jobActions: jobActions,
		runOpts:        serveRunOpts,
		sandboxPresets: sandboxPresets, sandboxStore: sandboxStore}
	// 官方每请求采样面：与 run/acp 是同一份环境变量解析（env_llm.go）。
	// 配置写错在 serve 启动时就 fail-loud，而不是让每个 turn 各自失败一次。
	sampling, err := samplingFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	mr := &modelRunner{home: *home, journals: journals, model: *serveModel, reasoning: *serveReasoning, recordEffort: true,
		base: envOr("DSHD_LLM_BASE", defaultLLMBase), catalog: modelCatalog,
		temperature: sampling.Temperature, stop: sampling.Stop}

	// 审批的父进程半边（H13）：整进程一个 broker，每轮换 stdin。
	// emit 用 lr 的方法，所以帧一样进 journal、一样可重放。
	broker := newApprovalBroker(nil, nil)
	// 官方 wire 的 $events 事件源：审批帧同时喂给它，官方 GUI 才能收到
	// waterfall 并回答（`$events/result`）。不接这条，官方侧只会看到 ready。
	approvalEvents := newApprovalEventSource(broker)
	broker.setEmit(func(sid, typ string, data json.RawMessage) {
		lr.emit(sid, typ, data)
		approvalEvents.OnApprovalFrame(sid, typ, data)
	})
	// 子进程推上来的后台任务清单 → 存到 lr，jobs.list 从这里读。
	broker.onJobsForSession = func(sessionID string, jobs []transport.JobInfo) {
		for i := range jobs {
			jobs[i].SessionID = sessionID
		}
		lr.jobsMu.Lock()
		lr.jobs = jobs
		lr.jobsMu.Unlock()
	}
	broker.actions = jobActions
	lr.broker = broker
	if useModelRunner {
		runner = mr
	} else {
		moRunner = &modelOverrideLoopRunner{base: lr, permissions: permissionOverrides, recordEffort: true, catalog: modelCatalog}
		runner = moRunner
	}
	// 活体 agent 注册表（官方 `ctx.agents` 的宿主落点）：**整个 serve 进程一份**，
	// 注册表的每条边都经 agentEventSink 落进**该会话**的 Transport journal
	// ——界面读的就是它（`/ws` 与 `session.history` 同一个 seq 域）。
	//
	// 以前这里传的是零值 `RegistryOptions{}`：`Emit == nil` 于是五条生命周期边
	// 一条都不发，界面上 agent 面永远是空的。这一份同时交给 goal-round 驱动
	// （排轮判据）与 agent 面（`subagents.*`），不再各造一张表。
	//
	// pub 晚绑定：注册表必须早于 `NewServer`（runner 要它），而 `srv.Publish`
	// 只有 `NewServer` 返回之后才有值。
	agentPub := &agentEventPub{}
	agents := agent.NewRegistry(agent.RegistryOptions{
		Emit: agentEventSink{journals: journals, pub: agentPub}.Emit,
	})
	// 官方 goal-round-driver（官方插件 #60）：真实 turn 正常收工后按 active goal 推进轮次。
	// 只包一层 turnRunner（Run/RunContent/Steer/SteerContent 原样透传），不新建 turn、
	// 不排 prompt；启动先重算一次，把上个进程漏记的轮次补上。
	goalRounds := newGoalRoundDriver(goalOverrides, *home, agents)
	if err := goalRounds.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "goal 轮次游标读盘失败（%v）；修复前不推进轮次\n", err)
	}
	if err := goalRounds.Reconcile(); err != nil {
		fmt.Fprintf(os.Stderr, "goal 轮次重启重算失败（%v）\n", err)
	}
	runner = goalRounds.wrap(runner)
	prompts := newPromptService(runner)
	// now 与普通 session.cancel 共用同一条真实取消链：先拒 pending approval，
	// 再 cancel 并等 runner/follow/v4 收工；不能只杀 context 留下审批僵尸。
	prompts.interrupt = func(sessionID string) error {
		return cancelServeTurn(broker, prompts, sessionID)
	}

	// 建会话的能力在起来之前就确定（cwd 定一次，不随后续请求漂移）。
	// 空 = 未配置官方 workingDirectory.defaultDirectory，走原来的进程 cwd 路径。
	defaultWorkDir, err := defaultWorkDirFromEnv()
	if err != nil {
		return err
	}
	create, err := serveCreate(*home, defaultWorkDir)
	if err != nil {
		return err
	}

	// 官方 0.2.2 job.*/terminal.* 需要在 Options 里注入，所以要在 NewServer 之前造好。
	officialJT, err := newOfficialPTYJobTerminalService(*home, bashSvc, jobActions, func(string) []transport.JobInfo {
		lr.jobsMu.Lock()
		defer lr.jobsMu.Unlock()
		return lr.jobs
	}, src.Cwd, officialPTYScrollbackRowsFromEnv())
	if err != nil {
		return err
	}
	defer officialJT.Close()

	// rc.2 `dsh-terminal-panel` 的两个只读工具（`user_terminal_list` / `user_terminal_read`）
	// 跑在**每轮的工具子进程**里，而真 roster 只在这个进程里。装一条父→子 stdio 帧桥：
	// 子进程问、这里答，答的就是 `terminal.list` / `terminal.watch` 那一份（见 terminal_bridge.go）。
	if _, err := installUserTerminalBridge(broker, officialJT, *home); err != nil {
		return fmt.Errorf("终端面板桥安装失败: %w", err)
	}

	// 配置树（HS-14）：`config.patch.*` 的唯一真值。启动即打开，损坏 fail-loud。
	configPath := filepath.Join(*home, "state", "config-tree.json")
	configTree, err := config.OpenStore(configPath)
	if err != nil {
		return fmt.Errorf("配置树打开失败: %w", err)
	}
	// 官方 #4 `hmr` 的**前半**（profile 配置热重载，官方默认开）。
	//
	// 在这之前 `Store.Reload()` 没有任何调用者：改了配置树文件，进程内看到的
	// 还是旧树，必须重启。这里接上一个独立的轮询组件（`serve_config_hmr.go`），
	// 只读、只 Reload、只广播 revision 变化，**不重装任何插件**。
	//
	// 关闭方式：`DSHD_PROFILE_HMR=0`（见 configHMRConfigFromEnv 的注释）；
	// 它拒绝监听 `profiles/*/cordis.patch.yml`（铁律 3 的那个文件）。
	configHMR, err := NewConfigHMR(configTree, configHMRConfigFromEnv(configPath))
	if err != nil {
		return fmt.Errorf("配置热重载装配失败: %w", err)
	}
	if configHMR.Enabled() {
		hmrCtx, hmrCancel := context.WithCancel(context.Background())
		defer hmrCancel()
		// 消费者：把新的 revision 写进日志，并把"谁需要知道"留给以后接——
		// 现在没有任何内存投影依赖配置树（读的都是 Store.Tree() 的实时快照），
		// 所以这里只观测，不假装重建了什么。
		offChange := configHMR.OnChange(func(c ConfigHMRChange) {
			fmt.Fprintf(os.Stderr, "profile 配置已热重载：revision %d → %d\n", c.From, c.To)
		})
		defer offChange()
		go func() { _ = configHMR.Run(hmrCtx) }()
	}
	configDescriptors := transport.NewRegistry()
	if err := configDescriptors.RegisterAll(transport.ConfigPatchDescriptors()); err != nil {
		return fmt.Errorf("config.patch 描述符注册失败: %w", err)
	}

	front, err := newFrontend()
	if err != nil {
		return err
	}

	// 官方 deliverables 三条变更路由的后端与"桌面打开"通道：native 服务要在
	// Options 之前造好，才能让 Changes.Open 与 session.openWorkspacePath 用**同一个**
	// 实例（两处各造一个服务，行为一致但状态面会分叉）。
	changesStore := newWorkspaceChangesStore(*home)
	nativeSvc := native.NewService()
	srv, err := transport.NewServer(withOfficialSessionOptions(*home, withSessionOperationOptions(*home, transport.Options{
		Source: src,
		// 只把适配器交给 Transport；它们在既有 cookie 认证之后才会被调用。
		PluginHandler:       newPluginHTTPHandler(pluginManager),
		ConfigPatchHandler:  transport.NewConfigPatchHandler(configTree, nil),
		Descriptors:         configDescriptors,
		SettingsFormHandler: newSettingsFormHTTPHandler(settingsStore),
		ModelCatalogHandler: newModelCatalogHTTPHandler(modelCatalog, settingsStore),
		Attachments:         attachments,
		// 官方 deliverables 的 /api/changes.summary|diff|open。记录与快照对象由
		// 每一轮的 `dshd run` 子进程写进 <home>/state/workspace-changes/<sid>/，
		// 这里只读；没有记录的 seq 回 404（不伪造空摘要）。
		Changes: transport.ChangesAPI{
			Summary: changesStore.Summary,
			Diff:    changesStore.Diff,
			Open:    changesOpenOnDesktop(changesStore, nativeSvc),
		},
		// 官方 0.2.2 wire（M1）：默认关，DSHD_OFFICIAL_WIRE=1 才挂载。
		// 返回 nil 时 transport 一个字节都不变（见 Options.HandlerWrap）。
		// 静态前端必须在**最外层**：GET /?token= 要先到前端，再到 wire。
		// 未设置 DSHD_FRONTEND_DIR（或目录不存在）时 front == nil，包装退化成
		// 原样，既有行为逐字节不变。
		HandlerWrap: transport.MCPManagerMountAround(transport.MCPManagerDeps{
			// 三个 mcpManager.*（probe/restart/watch）与 mcp.list/reconnect/toggle
			// 共用**同一份**服务状态：都取自 mcpServers 的嵌入式 tools.MCPManager。
			List:      mcpServers.MCPManager.List,
			Probe:     mcpServers.MCPManager.Probe,
			Restart:   mcpServers.MCPManager.Restart,
			Subscribe: mcpServers.MCPManager.Subscribe,
		}, frontendMount(front, officialWireMount(officialWireDeps{
			Home:        *home,
			Journals:    journals,
			Attachments: attachments,
			Events:      approvalEvents,
		}))),
		ContextUsage: src.ContextUsage,
		MCPList: func(sessionID string) ([]transport.MCPServerView, error) {
			if _, err := src.resolve(sessionID); err != nil {
				return nil, err
			}
			return mcpServers.List()
		},
		MCPReconnect: func(_ context.Context, sessionID, name string) error {
			if _, err := src.resolve(sessionID); err != nil {
				return err
			}
			return mcpServers.Reconnect(name)
		},
		MCPToggle: func(_ context.Context, sessionID, name string, enabled bool) error {
			if _, err := src.resolve(sessionID); err != nil {
				return err
			}
			return mcpServers.Toggle(name, enabled)
		},
		SideQuery:  sideQueries.Run,
		SideCancel: sideQueries.Cancel,
		SideClose:  sideQueries.CloseSession,
		// 后台任务：清单是**子进程推上来的那份**；输出要**当场问子进程**。
		//
		// **JobsOutput 只在轮次进行中可用**：每轮起一个新的子进程（`loopRunner`），
		// 跑完就退，`spi.Job` 也跟着没了。轮次之外调它会拿到
		// 「这条会话的子进程还没起来（没有 stdin）」——那不是 bug，是真实限制
		// （后台任务跟着子进程活，「任务还在跑但子进程已退」本身就说不通）。
		JobsList: func() []transport.JobInfo {
			lr.jobsMu.Lock()
			defer lr.jobsMu.Unlock()
			return lr.jobs
		},
		JobsOutput: func(id string, wait time.Duration) (transport.JobOutput, error) {
			ctx, cancel := context.WithTimeout(context.Background(), wait+5*time.Second)
			defer cancel()
			payload, err := broker.askJobsOut(ctx, id)
			if err != nil {
				return transport.JobOutput{}, err
			}
			var out transport.JobOutput
			if err := json.Unmarshal([]byte(payload), &out); err != nil {
				return transport.JobOutput{}, err
			}
			if out.ID == "" {
				out.ID = id
			}
			return out, nil
		},
		JobsOutputScoped: func(sessionID, id string, cursor, limit int, wait time.Duration) (transport.JobOutput, error) {
			ctx, cancel := context.WithTimeout(context.Background(), wait+5*time.Second)
			defer cancel()
			return jobActions.Output(ctx, sessionID, id, cursor, limit, wait)
		},
		JobsStop: func(sessionID, id string) (transport.JobStopResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			return jobActions.Stop(ctx, sessionID, id)
		},
		// 定时任务：直接读 `<home>/state/schedules/`（和 `dshd schedule list` 同一个来源）。
		// **不复用 lr**：它不是子进程推的，是磁盘上的文件，每次问都能拿到最新的。
		ScheduleList: func() []transport.ScheduleItem {
			return serveScheduleList(*home)
		},
		ScheduleDelete: func(sessionID, id, expectedRevision string) (transport.ScheduleDeleteResult, error) {
			return serveScheduleDelete(*home, sessionID, id, expectedRevision)
		},
		ScheduleClear: func() (transport.ScheduleClearResult, error) {
			return serveScheduleClear(*home)
		},
		// 会话级模型覆盖：写 <home>/state/session-model.json。
		// **不写 v4**（R5 读路径不写盘）；这是界面状态，进了日志会污染重放。
		SetSessionModel: func(sessionID, model string) error {
			return modelOverrides.Set(sessionID, model)
		},
		// 审批策略覆盖：写 <home>/state/session-permission.json（白名单在 handler 里校）。
		// 官方 0.2.2 三档：策略半边由 serve_sandbox_policy.go 实现，这里只做装配。
		SetSessionPermissionMode: func(sessionID, perm string) (map[string]any, error) {
			res, err := sandboxSetPermission(*home, sessionID, perm, sandboxPresets, sandboxStore, permissionOverrides)
			if err != nil {
				return nil, err
			}
			return permissionModeResponse(res), nil
		},
		SetSessionPermission: func(sessionID, perm string) error {
			_, err := sandboxSetPermission(*home, sessionID, perm, sandboxPresets, sandboxStore, permissionOverrides)
			return err
		},
		// 模型列表：由宿主转发（网关地址与凭据只在这一侧，R7 界面不直连）。
		// 和 `run`/`acp` 用同一组环境变量，免得界面看到的模型和实际能跑的不是一套。
		ModelsList: func(ctx context.Context) ([]transport.ModelEntry, error) {
			cli := llm.New(envOr("DSHD_LLM_BASE", defaultLLMBase),
				envOr("DSHD_LLM_KEY", ""))
			cli.UserAgent = "dshd/" + version
			models, err := cli.ListModels(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]transport.ModelEntry, 0, len(models))
			for _, m := range models {
				out = append(out, transport.ModelEntry{ID: m.ID, OwnedBy: m.OwnedBy})
			}
			return out, nil
		},
		// goal：存 <home>/state/session-goal.json（截断在上层 handler 里做）。
		SetSessionGoal: func(sessionID, goal string) error {
			return goalOverrides.Set(sessionID, goal)
		},
		SetSessionEffort: func(sessionID, effort string) error {
			return setServeEffort(effortOverrides, *home, sessionID, effort, *serveModel, modelCatalog)
		},
		GoalGet:    goalOverrides.Snapshot,
		GoalPause:  goalOverrides.Pause,
		GoalResume: goalOverrides.Resume,
		// 长连接终端：底层 `modules/exec/bash` 的 session 本来就是持久 PTY，
		// 缺的只是"开一个留住它"的入口。**Serve 自己建一个 Service**——
		// serve 模式不装配模块（agent 跑在子进程里），所以这里要单独一份。
		TerminalOpen: func(cwd string) (string, error) {
			t, err := bashSvc.OpenTerminal(cwd)
			if err != nil {
				return "", err
			}
			return t.ID(), nil
		},
		// 官方 terminal.create 的 PTY 终端按 id 唯一命中时优先；跨会话撞名 →
		// ErrJTOfficialBadParam（400，不猜）；没有该 id → ErrJTOfficialTerminalGone
		// → 回落既有管道终端。其它错误不吞（写失败必须 400/500，不是 not_found）。
		TerminalInput: func(id, data string) error {
			if officialJT != nil {
				err := officialJT.InputByID(id, data)
				if err == nil {
					return nil
				}
				if !errors.Is(err, transport.ErrJTOfficialTerminalGone) {
					return err
				}
			}
			t := bashSvc.Terminal(id)
			if t == nil {
				return fmt.Errorf("没有这个终端: %s", id)
			}
			return t.Input(data)
		},
		TerminalOutput: func(id string, since int) (transport.TerminalOutput, error) {
			if officialJT != nil {
				out, err := officialJT.OutputByID(id, since)
				if err == nil {
					return out, nil
				}
				if !errors.Is(err, transport.ErrJTOfficialTerminalGone) {
					return transport.TerminalOutput{}, err
				}
			}
			t := bashSvc.Terminal(id)
			if t == nil {
				return transport.TerminalOutput{}, fmt.Errorf("没有这个终端: %s", id)
			}
			out, next, dead := t.Output(since)
			return transport.TerminalOutput{Output: string(out), Next: next, Dead: dead}, nil
		},
		Addr: *addr,
		// H14：把序号域交给 server——WS 推送与 session.history 必须读同一份，
		// 否则客户端补洞时补到的是另一个 seq 域的数据。
		Journals: journals,
		// H13：界面的允许/拒绝落到这里，由 broker 写回子进程 stdin。
		Approve: broker.Resolve,
		// 断线重连时把还挂着的审批重放给新连接：请求没被回答，子进程就一直等。
		PendingApprovals: broker.PendingFrames,
		// H5：把这一进程真正的审批策略报给界面。**不是 mode**。
		Policy:       *servePolicy,
		ContentWidth: *serveWidth,
		// List 复用 transport_list.go 的转换：列表只有一份真相，
		// 不要在这里写第二套"读磁盘 → SessionSummary"。
		List: func() ([]transport.SessionSummary, error) {
			return listSessions(*home, "")
		},
		Load: func(sessionID string, limit int) (transport.LoadResult, error) {
			return transport.LoadSession(src, sessionID, limit)
		},
		History: func(sessionID string, sinceSeq, limit int) (transport.Page, error) {
			return transport.HistorySession(src, sessionID, sinceSeq, limit)
		},
		ConfigGet: func() (map[string]string, error) { return serveConfigGet(*home) },
		ConfigSet: func(kv map[string]string) error { return serveConfigSet(*home, kv) },
		FSRead: func(path string) (transport.FSContent, error) {
			return serveFSRead(path)
		},
		FSWrite: func(path, content string) error {
			return serveFSWrite(path, content)
		},
		WorkspaceList: func(path string) ([]transport.WorkspaceEntry, error) {
			return serveWorkspaceList(path)
		},
		Delete: func(sessionID string) error {
			// 先摘活体 agent、**再**删目录：`agent/disposed` 要进的是这条会话**自己的**
			// Transport journal，而目录删掉之后那个文件就不存在了——顺序反了就成了
			// "边发了但没人收得到"（写盘按设计 fail-loud，只会在 stderr 刷一行）。
			// 放在删除之前还有第二个好处：删除中途失败（取消超时等）时，注册表里
			// 也不会留一个已经没人认领的 agent；下一条消息进来会重新登记。
			if err := agents.Dispose(sessionID); err != nil {
				fmt.Fprintf(os.Stderr, "agent 收尾失败（session=%s）：%v\n", sessionID, err)
			}
			return serveDelete(*home, sessionID, broker, prompts)
		},
		Fork: func(sessionID, title string) (string, error) {
			return serveFork(*home, sessionID, title)
		},
		// Compact：界面上的"压缩"。走和 CLI `dshd session compact --auto`
		// **同一段代码**（autoCompact），否则两条入口迟早压出两种结果。
		Compact: func(sessionID string, keep int) (transport.CompactResult, error) {
			path, err := session.ResolvePath(*home, "", sessionID)
			if err != nil {
				return transport.CompactResult{}, err
			}
			res, err := autoCompact(context.Background(), path, keep, 0,
				envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017"),
				envOr("DSHD_LLM_BASE", defaultLLMBase))
			if err != nil {
				return transport.CompactResult{}, err
			}
			return transport.CompactResult{
				Compacted: res.Compacted, From: res.From, To: res.To,
				Shadowed: res.Shadowed, Summary: res.Summary,
			}, nil
		},
		Prompt: func(sessionID, text string) (transport.PromptResult, error) {
			// Submit 立刻返回，回答走 WS；这里绝不能等模型。
			// ctx 用 Background：turn 的生命周期长于这次 HTTP 请求，
			// 绑在请求上会在响应写完时把刚要开始的一轮掐掉。
			return prompts.Submit(context.Background(), sessionID, text)
		},
		PromptPlacement: func(sessionID, text, placement string) (transport.PromptResult, error) {
			return prompts.SubmitPlacement(context.Background(), sessionID, text, placement)
		},
		PromptBlocks: func(sessionID string, blocks []transport.ContentBlock) (transport.PromptResult, error) {
			// 官方 PromptContentPart（file/receiptId、image/mediaType+data）先解析成
			// 宿主附件块，再走与旧 attachment 块完全同一条 ResolveBlocks。
			normalized, err := attachments.NormalizeOfficialContent(sessionID, blocks)
			if err != nil {
				return transport.PromptResult{}, err
			}
			resolved, err := attachments.ResolveBlocks(normalized)
			if err != nil {
				return transport.PromptResult{}, err
			}
			return prompts.SubmitBlocks(context.Background(), sessionID, resolved)
		},
		PromptBlocksPlacement: func(sessionID string, blocks []transport.ContentBlock, placement string) (transport.PromptResult, error) {
			normalized, err := attachments.NormalizeOfficialContent(sessionID, blocks)
			if err != nil {
				return transport.PromptResult{}, err
			}
			resolved, err := attachments.ResolveBlocks(normalized)
			if err != nil {
				return transport.PromptResult{}, err
			}
			return prompts.SubmitBlocksPlacement(context.Background(), sessionID, resolved, placement)
		},
		Cancel: func(sessionID string) error {
			// H13 pending cancel：挂着的审批**先**按拒绝结算（并推 approval/cancel），
			// 再取消这一轮。cancelServeTurn 额外保留超时的真实 live 状态。
			return cancelServeTurn(broker, prompts, sessionID)
		},
		CancelPlacement: func(sessionID, cause, turnID string) (transport.CancelResult, error) {
			// queued-only retraction 没有当前审批可结算；其它取消仍走同一 broker。
			if cause == "" {
				cause = "user"
			}
			if turnID != "" && prompts.currentTurnID(sessionID) != turnID {
				return prompts.CancelPlacement(sessionID, cause, turnID)
			}
			if cause != "user" && cause != "interrupt" && cause != "switch" && cause != "dispose" {
				return prompts.CancelPlacement(sessionID, cause, turnID)
			}
			if broker != nil {
				broker.CancelSession(sessionID)
			}
			return prompts.CancelPlacement(sessionID, cause, turnID)
		},
		// Create 真建一份 v4 会话，并把创建当时的通用默认物化到既有
		// session override 键；后续每轮仍由原 runner 读同一存储。
		Create: createWithSettingsDefaults(create, settingsStore),
		// 官方 job.*/terminal.*（11 个方法）的路由层。
		OfficialJobTerminal: officialJT.Handler(),
	}), prompts, settingsStore))
	if err != nil {
		return err
	}
	// 注册表的 WS 推送出口在这里接上：`NewServer` 已返回、服务还没开始收请求，
	// 所以这一次赋值对所有请求 goroutine 都是 happens-before（壳里另有锁）。
	agentPub.set(srv.Publish)
	// 其余官方 0.2.2 方法接线（additive；宿主实现全在 cmd/dshd 与
	// internal/pluginmgr/official，Transport 只做协议翻译）。
	// rc.2：同一个 core 同时提供 `subagents.*` 与 `agentTeams.setEnabled` /
	// `subagents.interruptByUser`（Team 分派面），两边共享同一份进程内状态。
	agentAPICore := newAgentOfficialAPICore(agentOfficialDeps{
		home: *home, goals: goalOverrides, journals: journals, src: src,
		sandboxPresets: sandboxPresets, sandboxStore: sandboxStore,
		// 活体注册表是**同一份**：subagents.* 的委派 epoch（subagent/start|end）
		// 与排轮判据读的是同一张表，不是第二份影子状态。nil 时那些方法明确
		// no-op（不编边），不会静默成功。
		agents: agents,
		// Team 选择变更（`team/mode`）要同时进每会话 journal（可恢复）与 WS
		// （在线订阅者立刻看到同一帧）。不接的话在线界面要等下一次轮询。
		pub: srv.Publish,
		submit: func(sessionID, text string) (transport.PromptResult, error) {
			return prompts.Submit(context.Background(), sessionID, text)
		},
		// 带 placement 的同一入口：subagents.prompt 的 delivery:"steer" 走它。
		// 不接时 steer 回 400（响亮失败），queue 仍可用。
		submitPlacement: func(sessionID, text, placement string) (transport.PromptResult, error) {
			return prompts.SubmitPlacement(context.Background(), sessionID, text, placement)
		},
		// 官方 subagents.interruptByParent 的真实取消通道。缺它时该方法
		// 回响亮 501（说清缺这行），不会静默成功。
		interrupt: func(sessionID string) error {
			_, err := prompts.CancelPlacement(sessionID, "interrupt", "")
			return err
		},
	})
	srv.AttachOfficialAgentAPI(agentAPICore.bind())
	srv.AttachOfficialAgentTeamRC2API(agentAPICore.bindTeamRC2())
	// 壳侧更新渠道：官方归壳配置，不走 settings 命名空间。
	srv.AttachDesktopUpdateChannel(updateChannel)
	// 壳侧更新状态：与渠道是兄弟，同样是官方 preload 桥的 Go 等价物。
	srv.AttachDesktopUpdateStatus(updateStatus)
	// 官方 0.2.2-rc.2 电脑操作面（9 个 cuaAccess.*）。默认实现就是真 Service：
	// 平台/权限状态、应用决策、图标、设置页、审批状态机都是真的；AX/窗口控制在
	// 无 provider 的平台按官方形状回结构化未实现。
	srv.AttachOfficialCUARc2API(transport.NewOfficialCUARc2APIFromService(cua.NewService(cua.Options{HomeDir: *home})))
	srv.AttachOfficialSettingsCredentialAPI(transport.OfficialSettingsCredentialAPI{
		// rc.2 起 describe 要带 `llm` 命名空间（`providers.<route>.retryPolicy`），
		// 所以用 HostRegistry 的 Service；默认 Registry 的行为一字未变。
		Settings:    settingsform.NewService(settingsStore, settingsform.HostRegistry()),
		Credentials: transport.NewCatalogCredentialStore(modelCatalog),
	})
	wsHost := attachOfficialWorkspace(srv, *home, src, prompts)
	attachOfficialWorkspaceFollow(srv, wsHost)
	worktreeSvc := attachOfficialWorktree(srv, src)
	prompts.worktree = worktreeSvc
	// 官方 rc.2「归档会话时清理工作树」：给**同一个** worktree 服务注入 ArchiveHost
	// （保留策略 / 是否仍被引用 / 创建者会话的沙箱策略），并在
	// workspace.archiveSession 成功之后自动排期清理（inspection 在后台）。
	attachWorktreeArchive(srv, worktreeArchiveDeps{
		Home: *home, Workspace: wsHost, Service: worktreeSvc,
		Sessions: &worktreeSessionStore{src: src},
		Presets:  sandboxPresets, Modes: sandboxStore,
	})
	nativeSvc = attachOfficialNative(srv, nativeSvc, func() (string, error) {
		return ensureSettingsDocument(*home, settingsStore)
	})
	_ = nativeSvc
	attachOfficialRemoteAccessFollow(srv)
	attachOfficialSchedule(srv, *home, func(sessionID, text string) (transport.PromptResult, error) {
		return prompts.Submit(context.Background(), sessionID, text)
	})
	// 官方 liveshareShares 12 方法：owner 状态在 <home>/liveshare/owner-shares.json；
	// relay 地址/凭据来自 DSH_LIVESHARE_RELAY / DSH_LIVESHARE_OWNER_TOKEN(_FILE)。
	liveshareSvc, err := attachOfficialLiveshare(srv, *home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "liveshare: 12 个方法已挂上，但 owner 状态不可用: %v\n", err)
	}
	_ = liveshareSvc
	srv.AttachOfficialPluginManagerAPI(newOfficialPluginManagerAPI(pluginRT))
	marketAPI := newPluginMarketAPI(*home, pluginRT.Manager())
	srv.AttachOfficialPluginMarketAPI(marketAPI)
	srv.AttachOfficialPluginRegistryProbeAPI(marketAPI)
	if recommendationsAPI, recErr := newPluginRecommendationsAPI(*home, pluginManager); recErr != nil {
		fmt.Fprintf(os.Stderr, "recommendations: %v", recErr)
	} else {
		srv.AttachOfficialPluginRecommendationsAPI(recommendationsAPI)
	}
	srv.AttachOfficialForgeWriteAPI(newForgeWriteAPI(*home))
	srv.AttachOfficialForgeChangeRequestAPI(newForgeChangeRequestAPI())
	srv.AttachOfficialForgeGitAPI(newForgeGitAPI(func(sessionID string) (string, error) {
		return src.Cwd(sessionID)
	}))
	// rc.2 Forge 面 12 个方法（internal/transport/handlers_forge_rc2.go）。
	// 第二个参数与 worktree 面板共用**同一个** *worktree.Service 实例（清理状态、
	// revision 必须同源）；会话目录解析器与上面的 forgePanel git 族同源（src.Cwd）。
	srv.AttachOfficialForgeRC2API(newForgeRC2API(func(sessionID string) (string, error) {
		return src.Cwd(sessionID)
	}, worktreeSvc))
	if forgeHost, ferr := newForgePanelHost(*home); ferr == nil && forgeHost != nil {
		srv.AttachOfficialForgePanelAPI(forgeHost.api())
	}
	srv.AttachOfficialLLMProviderAPI(newLLMProviderAPI(modelCatalog))
	// 官方 0.2.2-rc.2 `web.listProviders`：清单来自 web 接缝的**真实注册表**
	// （谁注册了、谁现在可用、配置钉的是哪个），不是写死的一份。
	srv.AttachOfficialWebProviderAPI(providerapi.ProviderAPI(os.Getenv))
	accountDeps := accountOfficialDeps{home: *home, catalog: modelCatalog}
	accountAPI := newAccountOfficialAPI(accountDeps)
	accountAPI.ActionCardAct = actionCardAct(*home)
	srv.AttachOfficialAccountAPI(accountAPI)
	// 官方 base 插件 #23 `authorization`：把真实登录流注册进通用 flow 注册表。
	//
	// 补这一段之前，非 account 的凭据流**无处注册**、UI 也列不出"现在能授权
	// 什么"。provider 建立失败时注册表仍然存在但没有 flow（`begin` 回 NO_FLOW，
	// 不静默成功）——这是配置错误下的正确形状，而不是假装能登录。
	if authProvider, providerErr := accountProviderFromDeps(accountDeps); providerErr == nil {
		authz, err := newAuthorizationRegistry(authProvider)
		if err != nil {
			return fmt.Errorf("authorization flow 注册失败: %w", err)
		}
		srv.AttachAuthorizationRegistry(authz)
	} else {
		authz, err := newAuthorizationRegistry(nil)
		if err != nil {
			return err
		}
		srv.AttachAuthorizationRegistry(authz)
	}
	// Office 面（rc.2 新权威面 officeKit.* + 兼容保留的 officeToPdf.*）共用**同一个**
	// provider：同一引擎、同一准入队列、同一 generation、同一产物仓。
	// provider 没有引擎也照样建（官方语义「It now registers without Kit」），
	// 具体操作才按 unavailable 失败 → Transport 出 501 + capableWhen。
	officeKitProvider := officekit.Open(officekit.Options{Home: *home,
		OutputDirectory: filepath.Join(*home, "office-kit", "outputs")})
	defer officeKitProvider.Close()
	// 登记为进程内共享实例：工具面（office_convert/recalculate/render）从这一份取，
	// 避免「Transport 一套引擎状态、工具又一套」（见 cmd/dshd/serve_office_kit.go）。
	shareOfficeKitProvider(*home, officeKitProvider)
	srv.AttachOfficialOfficeKitAPI(newOfficeKitAPI(officeKitProvider, *home).withSessionCwd(src.Cwd))
	srv.AttachOfficialOfficePDFAPI(newOfficePDFAPIWith(*home, officeKitProvider))
	srv.AttachOfficialSpeechAPI(newSpeechAPI())
	attachActionCardsView(srv, *home)
	if err := attachOfficialAnalytics(srv, *home); err != nil {
		fmt.Fprintf(os.Stderr, "analytics: %v\n", err)
	}
	srv.AttachOfficialCordisAPI(newCordisService(*home))
	if useModelRunner {
		mr.pub = srv.Publish
	} else {
		lr.pub = srv.Publish
	}
	goalOverrides.journal.pub = srv.Publish

	base, err := srv.Start()
	if err != nil {
		return err
	}

	// 壳解析这一行；格式一字不差。
	line, err := json.Marshal(readyLine{URL: base, Token: srv.Token()})
	if err != nil {
		_ = srv.Close()
		return err
	}
	fmt.Fprintf(os.Stdout, "ready %s\n", line)
	// 打 stderr，不打 stdout：stdout 只放那一行 ready（壳按行解析握手）。
	// H11（2026-11-03 界面线报）：这行原来打在 stdout，客户端把第二行当成了协议内容。
	announcePolicy(os.Stderr, lr.policy, lr.maxSteps)
	// 形态也播报：lazy 下"模型说没有这个工具"是**预期行为**（它该先 tool_load），
	// 看不出这一档就会把机制当成故障。
	announceToolsMode(os.Stderr, lr.toolsMode)

	// 阻塞到 Ctrl-C / SIGTERM，或者**父进程没了**。用信号而不是 stdin EOF：
	// serve 可能由壳拉起，stdin 会立刻是 EOF，那不代表"该关了"。
	//
	// 父进程那条分支是 2026-11-04 那 33 个孤儿进程的修法：启动它的脚本/壳
	// 崩了之后，serve 原来会一直活着占端口。现在它自己收尾——先 Close（关端口、
	// 把还没写出去的帧落盘），再退出，原因已经在触发时打到 stderr 了。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	var by string
	select {
	case sig := <-stop:
		by = sig.String()
	case <-watch.Done():
		by = watch.Reason()
	}
	signal.Stop(stop)

	// 优雅退出：取消并等待在途的自动标题后台写（stop 幂等；主 turn 仍非阻塞，
	// Kick 从不等待）。必须在 srv.Close() 之前收口：标题 goroutine 可能正在写
	// 会话审计，若晚于 Close/会话清理才落笔，会变成 "session.v4.jsonl.zstd:
	// The system cannot find the file specified" / TempDir 非空一类竞态。
	if moRunner != nil {
		if titles := moRunner.autoTitleService(); titles != nil {
			titles.stop()
		}
	}

	if err := srv.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "serve 已关闭（%v）\n", by)
	return nil
}

// permissionModeResponse 是 session.setPermission 的唯一响应映射。
// 审批输入（deny/ask/allow/auto）回显审批值；sandbox 三档输入才回真实
// sandbox mode。mode/effective_mode 永远是真实 sandbox，不能与 permission 混用
// （t165：deny 被回成 workspace-write）。
func permissionModeResponse(res sandboxPermissionResult) map[string]any {
	permission := string(res.Mode)
	if res.Approval != "" {
		permission = res.Approval
	}
	return map[string]any{
		"ok":             true,
		"permission":     permission,
		"mode":           res.Effective,
		"effective_mode": res.Effective,
		"preset":         res.Preset,
		"enforcement":    res.Enforcement,
	}
}
