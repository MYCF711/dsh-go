# 10 · dsc 参考实现解剖（Go 重写 DSH 的最接近同构体）

> 源码：`WS/refs/dsc/`（naamfung/dsc，`VERSION=v3.0.0`，master @ 2026-10-31）。
> 取证说明：`E:/…/tmp/dsc.tgz` 传到一半断在 `libs/anthropic-sdk-go` 中途（gzip 报
> `unexpected end of file`，`proto/ plugins/ tools/ tui/ sdk/ workflow/` 全部缺失）。
> 已核验本地残包与上游 `master` 在重叠部分**逐字节相同**（`core/ config/ docs/ cron/
> jobs/ builder/ coderuntime/ libs/` 的 md5 目录和一致，`VERSION` 同为 v3.0.0），
> 故用 codeload 完整包补齐后解到 `refs/dsc/`。下文行号均按该树。
> 非测试代码总量：主模块 `core 22723 / tui 7393 / proto 6408 / plugin 4519 / sdk 2983 /
> session 1796 / jobs 652 / workflow 655 / cron 454 / coderuntime 295 / lualib 142 /
> userquestions 100`，根 9 文件 2682，`plugins/*` 21 个共 ~37k，`libs/` 为 vendored。

---

## 1. 目录结构与依赖

| 路径 | 职责（一句话） |
|---|---|
| `main.go` (1496) | 宿主入口：配置装载、上下文窗口探测、TUI/单发/stdin 三模式分派 |
| `main_assemble.go` | 运行前装配（env 分组、插件二进制解析、mode 预设） |
| `setup.go` / `help.go` / `version.go` | 交互式初始化、`--help` 文本与旗标一致性校验、版本 |
| `graceful_exit.go` + `signal_unix.go` / `signal_windows.go` | SIGINT/TERM/HUP vs `SetConsoleCtrlHandler` 分平台收尸 |
| `core/` (101 文件, 22723) | 宿主内核：插件管理器、事件总线、工具流水线、审批/沙箱、会话计量、后台任务、admin REST、内置工具 |
| `plugin/` (23, 4519) | **fork 的 hashicorp/go-plugin**（`module github.com/hashicorp/go-plugin`），加了 broker 扩展；`plugin/go.mod` 独立模块 |
| `proto/` | `dsc.proto` (568) + 生成物 `dsc.pb.go`/`dsc_grpc.pb.go`；`proto/metadata/` 插件元数据服务 |
| `sdk/` (`module dsc-sdk`, 30 文件, 2983) | 插件作者唯一依赖面：`Config/SDK/Tool/Hook/Command` + 大量 `type X = core.X` 别名转发；`sdk/mcp` (1662) 自带 MCP 客户端（只依赖 stdlib） |
| `session/` (1796) | 事件溯源会话日志：类型词汇表、surface 投影、派生模型历史、JSONL 持久化 |
| `tui/` (13 文件, 7393) | Bubble Tea v2 终端界面（`tui.go` 单文件 4501） |
| `jobs/` (652) | 后台任务注册表（owner 隔离、流式/终值两种输出模型） |
| `cron/` (454) | 定时任务（本地 fork `robfig/cron`） |
| `workflow/` (655) | Lua 工作流引擎：actor 协程排程、`parallel`/`pipeline`/`agent()` |
| `coderuntime/` (295) + `lualib/` (142) | PTC `run_code` 的 Lua 运行时与 Lua↔JSON 转换 |
| `userquestions/` (100) | 向用户提问并阻塞等待的评审通道（plan-review 用） |
| `skills/` | 内置/已安装技能资源（无 Go 代码） |
| `builder/` (1197) | 七端交叉编译 + 打包器（`./builder cross`） |
| `tools/backendcheck` (363) / `tools/tokcal` (129) | 用真实派生历史驱动每个压缩插件二进制做契约校验 / token 系数诊断拟合 |
| `plugins/` (21 个独立 module) | `agent-react-loop 2563`、`dsc-system 2447`（压缩+spill+timeout+reminder+fsobs+skill）、`dsc-billion-context 5962`、`tool-filesystem 4195`、`tool-mcp 1705`、`llm-anthropic/openai/ollama 995/673/397` 等 |
| `libs/` | 本地 replace 的依赖：`anthropic-sdk-go`、`go-openai`、`ollama`、`cron`、`toon-go`、`go-lua`、`jig-lisp`、`sh`(mvdan fork)、`vodka`(自研 web)、`fasttemplate`、`bytebufferpool` |
| `docs/` | `cordis-alignment.md`(113) 是 DSH↔DSC 对照表、`token-accounting.md`(757) 计量口径 |

`go.mod`：`module dsc`，`go 1.26.0`。直接依赖（版本照抄）：

```
charm.land/bubbles/v2 v2.2.1        charm.land/bubbletea/v2 v2.1.0     charm.land/lipgloss/v2 v2.0.6
github.com/atotto/clipboard v0.1.4  github.com/casbin/casbin v1.9.1    github.com/charmbracelet/x/ansi v0.11.9
github.com/flosch/pongo2/v6 v6.1.0  github.com/fsnotify/fsnotify v1.10.1  github.com/garyburd/redigo v1.6.4
github.com/go-playground/validator/v10 v10.30.3  github.com/golang-jwt/jwt/v5 v5.3.1
github.com/golang/gddo v0.0.0-20210115222349-20d68f94ee1f  github.com/hashicorp/go-hclog v1.6.3
github.com/hashicorp/go-plugin v1.8.0  github.com/hashicorp/go-version v1.9.0  github.com/mattn/go-colorable v0.1.14
github.com/mattn/go-isatty v0.0.24  github.com/microcosm-cc/bluemonday v1.0.27  github.com/robfig/cron/v3 v3.0.1
github.com/smartystreets/goconvey v1.8.1  github.com/stretchr/testify v1.11.1  github.com/toon-format/toon-go v0.0.0
github.com/valyala/fasttemplate v1.2.2  github.com/wippyai/go-lua v1.5.17  github.com/yuin/goldmark v1.8.5
github.com/zeebo/blake3 v0.2.4  golang.org/x/image v0.44.0  golang.org/x/sys v0.47.0  golang.org/x/text v0.40.0
golang.org/x/time v0.15.0  google.golang.org/grpc v1.83.0  google.golang.org/protobuf v1.36.12
gopkg.in/vmihailenco/msgpack.v2 v2.9.2  gopkg.in/yaml.v3 v3.0.1
```

`replace`：`go-plugin→./plugin`、`anthropic-sdk-go`/`go-openai`/`ollama`/`cron`/`toon-go`/
`fasttemplate`/`bytebufferpool`/`go-lua` 全部指向 `./libs/*`（离线可复现 + 可改上游）。
模块边界：根 `dsc` ← `sdk(dsc-sdk)` ← `plugins/*`（各自 `go.mod`，用 `replace dsc-sdk => ../../sdk`）；
`core` **不** import `dsc-sdk`，由 `core/plugin_sdk_boundary_test.go::TestPluginsDoNotImportCore` 扫描守卫。

---

## 2. 插件 ABI

### 2.1 proto 服务（`proto/dsc.proto` 568 行，9 个 service / 36 个 RPC）

| service（行） | RPC（请求 → 响应） | 关键字段 |
|---|---|---|
| `DSCPluginService`(7) | Name / Version / Execute(`ExecuteRequest→ExecuteResponse`) / HealthCheck | 通用 dsc 插件：`input`, `params map<string,string>` → `output,status,message` |
| `AgentService`(14) | Run / RunStream(stream) / Name / Version / RegisterServices / SwitchSession / SetPlanMode / SetHistoryInjection / SetUserQuestionsService / Shutdown / InjectMessage / DebugSnapshot（12 个） | `RunStreamResponse`(135): `output,status("streaming"\|"reasoning"\|"tool"\|"todo"\|"success"\|"error"),error,usage,reasoning,tool_name,tool_args,tool_result,turn,step,tool_view`；`SetHistoryInjection.count` 三值语义与 DSH **相反**（proto:280） |
| `LLMService`(38) | Chat / ChatStream(stream) / Name / Version / HealthCheck | `ChatRequest`(163): `messages,tools,max_tokens,session_id,new_user_input`；`ChatStreamResponse`(239): `content,finish_reason,tool_calls,error,usage,reasoning` |
| `ToolService`(46) | ExecuteTool / ListTools / ListContext / SetInterconnect / GetActivity / ApprovalReason（6 个） | `ExecuteToolResponse`(497): `content,error,view_json,images[],notices[]`；`ListContext`(523) 只回一段 string；`GetActivity`(79) 回 `last_activity_unix_ms,output_tail,output_bytes`；`ApprovalReason`(227) 回 `reason`（空=不需审批） |
| `PluginHookService`(370) | BeforeTool / AfterTool / OnEvent / ListContext / RunCommand（5 个） | `BeforeToolResponse`(398): `veto,error,arguments_json`；`AfterToolResponse`(412): `result,error`；`OnEventResponse`(427): `result_json,error`；`RunCommandResponse`(439): `text,error` |
| `PolicyService`(531) | OnEvent(`PolicyEvent→PolicyDecision`)（1 个） | `PolicyEvent.kind ∈ {tool/pre-execute,tool/execute,tool/post-execute}`；`PolicyDecision`(560): `action(allow\|deny\|replace),reason,result,timeout,notice` |
| `UserQuestionsService`(338) | Ask（1 个） | `AskQuestion{id,question,detail,header,options[],multi_select,intent}` → `AskAnswer{id,selected[],custom}`，错误码字段 `error` |
| `PluginNotifyService`(344) | Notify（1 个） | `owner,name,data(JSON)`；`name="job/done"` 时为任务快照 |
| `PluginMetadata.GetInfo`（`proto/metadata/metadata.proto`） | 1 个 | `PluginInfo{type,name,version,api_version,capabilities map,services[],commands[]}` |

约定要点：`Tool`(189) 携带 `name,description,parameters_json(schema 字符串),capabilities[],hidden,scopes[],management`；
`capabilities` 里的 `requires-never-approval` 是门控 token；`scopes` 是权限范围声明
（`process.spawn`/`net.egress`/`fs.write`）；`hidden` 表示"注册但不上目录"。

### 2.2 SDK 暴露给插件作者的类型（`sdk/`）

| 类型 | 位置 | 说明 |
|---|---|---|
| `Config{Name,Version,Type,APIVersion,Commands,Requires,Provides}` | `sdk/sdk.go:42` | `APIVersion` 必须 ∈ [1.0,2.0)；`Requires []CapabilityRequirement{Type,Capability}` 编码成 `requires/<type>/<cap>` 键（`sdk/metadata.go:96`）；`Provides map[string]string` 是普通能力键 |
| `Type` 常量 | `sdk/sdk.go:23-39` | `tool` / `llm` / `agent` / `policy` / `dsc`（通用） |
| `SDK` | `sdk/sdk.go:100` | 链式：`Tool/ToolProvider/LLM/Agent/Policy/Hook/OnEvent/Context/BeforeTool/AfterTool/SetInterconnect/AgentBroker/OnStart/OnStop`，末尾 `Serve()`(272) |
| `Tool` | `sdk/tool.go:23` | `Name,Description,Schema json.RawMessage,Handler func(ctx,args)(string,error),ViewFn,ImagesFn,Context/ContextFn,Capabilities,Hidden,Scopes,Management,ApprovalFn` |
| `Hook` | `sdk/hook.go:27` | `BeforeTool(ctx,tool,args)(rewritten,err)`、`AfterTool(ctx,tool,args,result,toolErr)(newResult,newErr)`、`OnEvent(ctx,event,data)(result,err)`、`ContextFn()string`、`RunCommand(ctx,name,args)(text,err)` |
| `Command` | `sdk/command.go:20` | `{Name,Summary,Usage}`——斜杆命令是**插件声明**的，宿主不内置 |
| 视图 spec | `sdk/view.go:23/34/44` | `CardView(title,badge,fields)` / `TableView(title,badge,columns,rows)` / `PlainView(title,badge,body)`，序列化为 `core.ToolView{kind,title,badge,fields,columns,rows,body}`（`core/view.go:14`） |
| 计量 | `sdk/tokenmeter.go` | `TokenMeter = core.TokenMeter`（别名）、`NewTokenMeter/Estimate*/PromptSideTokens/FormatPressure/ToolsFingerprint/LastUsageAnchor` |
| 协议转发 | `sdk/protocol.go` | `Message/ToolCall/LLMTool/ChatResponse/ChatStreamResponse/ToolView/Agent/RunStreamResponse` 全是 `= core.X`；`Handshake`、事件名常量、`TouchActivity/AppendOutput/WithIdleDeadline`、`SlashErr/CanonicalPath`、`SaveImageAttachment` |
| 会话路径 | `sdk/session.go` | `SessionDir/SessionKeyForProject/ProjectSessionKey/TempDir/SkillsDir/MemoryDir`（唯一实现，禁止插件自拼） |
| MCP | `sdk/mcp` (1662) | 自带 streamable-HTTP / stdio 客户端，只依赖 stdlib，避免 `core→sdk/mcp→sdk→core` 循环 |

关键设计：`Tool.Handler` 返回 **`(string, error)`**——这是后面"结构化优先"问题的根。

---

## 3. 五类插件：接口、加载、热重载

### 3.1 差异表

| 类型 | 插件必须实现 | 宿主登记入口（`core/`） | 热重载函数 |
|---|---|---|---|
| `agent` | `core.Agent`（Run/RunStream/SwitchSession/RegisterServices/…） | `loadAgentAndGetBroker` `manager.go:2070`（**先**拉起，因 broker 由它提供；`LoadFromConfig:1840` 只允许一个，多余的 warn） | `hotReloadAgent` `manager.go:981` |
| `llm` | `core.LLMProvider` | `loadLLMEntryLocked` `manager.go:703`，再挂成 broker 上的聚合 gRPC | `hotReloadLLM` `manager.go:1099` |
| `tool` | `ToolService` + `PluginMetadata` + `PluginHookService` | `stageToolPlugin` `manager.go:2289` → `commitToolPluginLocked` `:2312`（两段式：先在准备区 listStagedTools，最后提交注册表） | `hotReloadTool` `:1160` |
| `policy` | `PolicyService` | `registerPolicyLocked` `manager.go:2339` → `bridgePolicyToPipeline`(`tool_pipeline.go:348`) 把 RPC 桥成 3 个 waterfall 监听器 | `hotReloadPolicy` `:1228` |
| `dsc`（通用） | 任意组合；`declaredServices()`(`sdk/sdk.go:366`) 报 `tool/hook/policy` | `registerDscCoreLocked` `manager.go:2356`，按 `PluginInfo.services` 机械桥接 | `hotReloadPlugin` `:878` |

加载顺序是**能力依赖驱动**而非配置顺序：agent 先起拿 broker → provider 全量加载并经
`resolveRequiredDeps` 解析 `requires/*` → 聚合 LLM/Tool/UserQuestions/Notify 挂到 broker
→ 依赖不满足则置 `PENDING`（`markPendingLocked:595`），由 `repairPendingLocked` 反应式提升。
压缩能力是 agent 的**强依赖**：无当值后端时 agent 停留 PENDING，`main` 调
`CompactionRoleProblem()`(`notify.go:232`) 直接拒绝启动。

### 3.2 热重载的实现手法（`manager.go:808` 起）

```
HotReload(name, newBinary)                       // :808
 ├ reloadMu.Lock()                              // 全局串行化"重载"这一动作，不是数据锁
 ├ RLock 快照 typ/execDir/handshake/broker/refs → RUnlock   // :815-822 短读锁
 └ switch typ → hotReload{Plugin,Agent,LLM,Tool,Policy}
```

以 `hotReloadPlugin`(:878) 为例，"不持全局锁 + 失败不伤旧实例"由三段构成：

1. **准备阶段（完全不持 `m.mu`）**：`plugin.NewClient(...)`→`Client()`→`Dispense()`→类型断言、
   以及（若旧实例登记过工具）在**新进程**上 `stageToolPlugin` 重新挂 broker、列工具清单。
   任何一步失败：`newClient.Kill()` + `setReloadFailed(name,err)`(:863 只短暂持锁改状态)
   然后 return——**旧进程与旧映射一行未动**，服务继续。
2. **交换阶段（短临界区）**：`m.mu.Lock()` 后**重新校验** `m.typeMap[name]` 是否仍是预期类型
   (`:943`)，不等则 `newClient.Kill()` 并报 "unloaded during reload; abort"；
   然后 `runStopHooksLocked` → 换 `clients/plugins/typeMap` → 刷新 hook client →
   `commitDscToolPluginLocked` 重登工具 → `oldClient.Kill()`。
3. **收尾**：`recordLoadedBinaryLocked`(:161 in hot_reload_watch) 记录运行中二进制路径，
   `transitionLocked(StateReady)`，`go monitorExit`。

触发机制 `core/hot_reload_watch.go`：`hotReloadWatcher` 用 fsnotify + 周期扫描双路，
`throttled()`(:104) 保证 500ms 最小扫描间隔；`collectUpgrades`(:128) 在**读锁快照下**收集
"目录内最高版本 > 当前运行版本"的候选，再在锁外逐个 `HotReload`。
版本语义见 `core/hot_reload_version.go`：文件名 `^(.+)-v(\d+)\.(\d+)…(\..+)?$`，
**基线名（无 `-vN`）视为 0.0.0**——这对 Windows 很关键：正在运行的 `.exe` 不可覆盖，
只能以新版本文件名启动新进程再杀旧的。

---

## 4. 事件总线

### 4.1 五种模式（`core/eventbus.go`，签名照抄）

```go
type Listener          func(ctx EventContext) (any, error)                    // :38
type WaterfallListener func(ctx EventContext, next func(EventContext) error) error // :42

func (b *EventBus) On(name EventName, fn Listener) func()                      // :120
func (b *EventBus) OnAny(fn Listener) func()                                   // :101
func (b *EventBus) OnWaterfall(name EventName, fn WaterfallListener) func()    // :140
func (b *EventBus) Emit(name EventName, ctx EventContext)                      // :197 顺序、忽略返回值
func (b *EventBus) Parallel(name EventName, ctx EventContext) error            // :212 并发、errors.Join
func (b *EventBus) Serial(name EventName, ctx EventContext) error              // :238 顺序、遇错即停
func (b *EventBus) Bail(name EventName, ctx EventContext) (any, error)         // :250 首个非 nil 即短路
func (b *EventBus) Waterfall(name EventName, ctx EventContext, next func(EventContext) error) error // :266
```

`Waterfall` 从链尾向前包装成洋葱（`:280-284`），listener 不调 `next` 即 veto；
`next` 为 nil 时不调用兜底。所有模式都对 listener panic 做 recover
（`callListener:89`，`Parallel` 在 goroutine 内单独 recover —— 否则会 crash 整个宿主）。

### 4.2 与 DSH 事件的对照

| DSH | DSC 常量 | 模式 | 位置 |
|---|---|---|---|
| `agent/pre-step` | `EventAgentPreStep` | waterfall | `core/agent_events.go:29` |
| `agent/request-error` | `EventAgentRequestError` | waterfall | `:43` |
| `agent/status` | `EventAgentStatus` | emit | `:18` |
| `agent/error` | `EventAgentError` | emit | `:24` |
| `tools/pre-execute` | `EventToolPreExecute` | waterfall | `core/tool_pipeline.go:30` |
| `tools/execute` | `EventToolExecute` | waterfall | `:36` |
| `tools/post-execute` | `EventToolPostExecute` | waterfall | `:38` |
| `tools/result` | `EventToolResult` | emit | `:41` |
| `tools/change` | `EventToolsChange` | emit | `:44` |
| `approval/asked` / `decided` / `policy` | 同名常量 | emit | `core/approval.go:250/253/256` |
| `userquestion/asked` | `EventUserQuestionAsked` | emit | `core/userquestion_events.go` |
| `job/done` | `JobDoneEvent` | emit | `core/jobs.go` |

模式映射表 `core/agent_events.go:133`：只有 `agent/pre-step`、`agent/request-error` 是
`waterfall`，其余缺省 `emit`。**模式由事件名决定，listener 不感知**（对齐 Cordis）。

### 4.3 跨进程 Hook.OnEvent 投递

宿主 `dispatchEventToPlugins`（`core/notify.go:619`）：
1. `hookClientsNamedSnapshot()` 取按加载顺序的插件 hook 客户端；
2. `eventDispatchMode[name]` 定模式，缺省 emit；
3. 对 `agent/pre-step` / `agent/request-error` 先 `ResolveCompactionRole()`(:133) 定"压缩岗位"，
   `request-error` **只**发给岗位持有者（否则每个候选都真压一遍再被丢弃）；
4. `pre-step` 完整消息列表只发给岗位持有者与声明了 `pre-step-messages`(:92) 的观察者，
   其余发 `reducedPreStepPayload`(:586 去掉 `messages_json` 的轻载荷)；
5. 单次分发统一 `context.WithTimeout(30s)`；
6. waterfall 分支从 `len-1` 反向遍历，把上一个插件返回的 `result_json` 作为下一个的 `data_json`
   —— **`next()` 闭包无法跨进程，洋葱模型改由宿主串联近似**（这是 DSC 与 DSH 最本质的语义差）；
   插件侧只需 `return (resultJSON, err)`，`err != nil` 视为 veto。

另有互通三机制：插件经 `SetInterconnect` 拿到宿主聚合 LLM/Tool/Agent/Notify 的 serviceID
再 `broker.Dial`；插件反向发布事件走 `PluginNotifyService.Notify`。

---

## 5. 工具流水线

`core/tool_pipeline.go`。入口 `ExecuteToolWithView`(:114)（`ExecuteTool`:101 是薄封装）：

```
run_in_background=true → startBackgroundTool(:487) 立即回 job_id
auto-background 能力    → runWithGrace(:112 in auto_background.go) 前台宽限
  ↓
pre-execute  Waterfall(:136) → 兜底 next 里先跑插件 BeforeTool(runPluginBeforeTool notify.go:272)
execute      Waterfall(:152) → 兜底 next 里跑 executeToolBody(:186)；ctx 从 EventContext 回读
post-execute Waterfall(:165) → 兜底 next 里跑插件 AfterTool(notify.go:320)
result       Emit(:175 → emitToolResult:332)
```

| 插槽 | 语义 | 实现 |
|---|---|---|
| `tools/pre-execute` | 守卫。waterfall，不调 next 即 veto（execute 不运行）。宿主在此预注册 4 个 listener，顺序即"水位"：`approvalEscalation` → `toolApprovalGate` → `neverApprovalGate` → `sandboxPolicy`（`manager.go:499-502`）；policy 插件的桥接 listener 由 `registerPolicyLocked` 后追加，插件 BeforeTool 在兜底 next 内 | `tool_pipeline.go:136` |
| `tools/execute` | 分发槽。listener 可换装 `EventContext.Context` 来安装执行域（timeout-policy 的 `WithIdleDeadline`）；deny 同样阻止执行 | `:152`，桥 `:380-433` |
| `tools/post-execute` | 观测/改写。失败也转发（"失败是权威观察"）；`action=replace` 改写 `inv.Result`；`notice` 机械收集进 `inv.Notices` | `:165`，桥 `:434-468` |
| `tools/result` | 纯通知 emit，载荷 `ToolResultInfo{ tool_name,result,error,view_json,images }`(:322) | `:332` |

**veto 的三条路径**：① waterfall listener 返回 error（policy `deny` → `errors.New(reason)`，
reason 原文透传模型）；② 插件 `BeforeTool` 返回 `veto=true`（`notify.go:285`，文本 "core vetoed X: …"）；
③ 沙箱/审批门直接返回拒绝文案。

**审批门**（`core/approval.go`）：两个入口共用一套账本——
`toolApprovalGate`(:313) 处理"工具声明需审批"（进程内工具实现 `ApprovalRequester`(:305)，
插件工具走 `RemoteTool.approvalReason` → `ToolService.ApprovalReason` RPC → `Tool.ApprovalFn`），
`approvalEscalation`(:393) 处理"沙箱拒绝后的 `sandbox_permissions`+`justification` 升级重试"
（`widenable`(:185) 封闭阶梯：read-only→{workspace-write,full}、workspace→{full}，
顶档不可再宽）。粒度 `DSC_APPROVAL_GRANULARITY=tool|scope`（`authorization.go:40`）：
工具级每次问不记账，范围级问一次即 `GrantScopes` 记账。策略 `ask|never`（`:20`），
`never` 是**自动拒绝**不是自动通过；用户手打命令时前端注入 `WithUserGrant`(:271)。
`neverApprovalGate`(`never_approval_gate.go`) 对声明 `requires-never-approval` 的工具做前置门控。

**超时（看门狗 + 活动续命）**：`core/activity.go`。
`WithIdleDeadline(ctx,idle)`(:57) 用 `context.WithCancelCause` + `time.AfterFunc`
装一个可重置定时器，触发时 cause = `ErrIdleDeadline`；`TouchActivity`(:73) 重置；
`IdleDeadlineExceeded`(:137) 判 cause。**跨进程的坑**：ctx value 过不了 gRPC，
插件里的 `TouchActivity` 摸到空句柄 → 宿主改用 `renewIdleFromActivity`(:101)
轮询 `GetActivity` RPC（间隔 `budget/4` 夹在 [100ms,5s]）替执行方续命。
三处预算来源：策略插件裁决 `TimeoutSpec`（`dsc-system/timeout.go:26` 的 `timeoutPolicies`
表，shell/subagent 各 10min）→ 宿主机械安装；工具自带 `TimeoutProvider`；
都没管时 `executeToolBody`(:219-230) 给未声明 auto-background 的工具补 `autoBackgroundGrace`，
超时文案明说"不能转后台所以取消了，长任务请传 run_in_background"(:277-280)。

**spill 外置**（`plugins/dsc-system/spill.go`）：post-execute 结果变换器，纯文本超
`DSC_SPILL_THRESHOLD`（默认 4000 **字符**）即写 `temp/spill/<session>/spill-<n>.txt`，
定位符为文件路径，返回 `...(中间省略 N 字符。完整内容已存于 %s…)`(:256)；
豁免取回工具 `str_replace_editor` 的 `view` 命令防"外置↔取回"死循环；写失败绝不掩盖原结果。

**重复调用提醒**（`plugins/dsc-system/reminder.go`）：advisory 形态，post-execute 计数
`(tool, 深度规范化参数)` 连续相同次数，命中 `DSC_REPEAT_THRESHOLDS`（默认 `3,5,8`）
经 `PolicyDecision.notice` 注入；首阈值简短、后续列出工具/次数/参数预览；
用户插话（`agent/pre-step` 的 `user_input=true`）重置链；`deny`/失败调用**照样计数**。

---

## 6. 沙箱

`core/sandbox.go`（204 行），**工具级 + 参数路径判定**，不是 OS 级隔离。

| 档位 | 常量 | 判定点 |
|---|---|---|
| `full-access` | `SandboxFullAccess`(:25) | `sandboxCheck` 直接放行(:63) |
| `workspace-write`（缺省） | `SandboxWorkspaceWrite`(:27) | 写语义调用要求路径在 `WorkspaceRoot` 内，否则拒绝并给出**具体路径与根**(:82) |
| `read-only` | `SandboxReadOnly`(:29) | 任何写语义调用拒绝；`writeCapableExecutors`(:52 目前仅 `{"shell":true}`)整体禁用 |

判定逻辑只有两处：`writeCallInfo`(:95) 目前**只认 `str_replace_editor`**（`command != "view"` 视为写），
参数解析失败按"写"处理（fail-closed）；`inWorkspace`(:145) 先 `MapWorkspacePath` 归并
`/workspace` 别名与 WSL `/mnt/<drive>/` 形态，再 `CanonicalPath` 穿透 junction/symlink
（Windows 用 `GetFinalPathNameByHandle`），最后前缀包含判定。执行在
`sandboxPolicy`(:185) 这个 pre-execute waterfall listener，被拒时输出
`sandboxDenialMarker` + 错误正文 + `escalationHintMarker`(:140，直接写"从当前档可用的那一个档位名"，
避免模型自己推阶梯)。运行时切换 `SetSandboxPolicy`(:172)，TUI `/sandbox` 调它。
配置 `DSC_SANDBOX=full|workspace|readonly`（`manager.go:493`）。

**Windows 落地**：没有 OS 沙箱，靠 ① 参数路径白名单化判定 ② `WorkspaceRoot` 真实路径解析
③ process_attr/proc_job（`core/proc_attr_windows.go`、`proc_job_windows.go`）给子进程加 Job Object。
"未知写路径的解释器"是明确的已知缺口，注释里写了长期正解是让工具自声明写能力(`:50-51`)。

---

## 7. 会话与 prompt 装配

**格式**：`sessions/<key>.jsonl`，每行一个事件 `{seq,time,type,data,surface?}`（`session/persist.go:25`）。
`data` 按 `type` 判别（`unmarshalData:45` 的判别联合），遇到**不认识的事件类型直接拒绝重建**，
不静默丢弃。不做版本头/迁移（高速迭代，无向后兼容）。

**事件词汇**（`session/session.go:21-…`）：`turn/start|end`、`step/start|end`、`user/message`、
`assistant/chunk`(log-only)、`assistant/message`、`tool/call`(log-only)、`tool/result`、
`compaction/summary`、`plan/mode`、`history/limit`、`goal/change`、`todo/write`、
`approval/asked|decided|policy`、`llm/attempt`、`request/header`、`system/message`。
分两类：**surface**（参与派生模型历史，`surfaceEventTypes`）与 **log-only**（回放/UI/计量）。

**历史派生**：`DeriveMessages()`(:344) 从 surface 节点投影；`DeriveMessagesLimited(n)`(:364)
按"最近的 user 边界"截取，保证 tool_call ↔ tool_result 配对完整；
消息 id = `event-<seq>`(:573)，**永不随派生范围变化**（压缩块靠它指认，不能用下标）。
投喂模型前 `toonizeToolContent`(:581) 把结构化 JSON 工具结果转 TOON（键排序、纯函数、
确定性），非 JSON 原样保留。

**滑窗**：`core/context_ceiling.go`。`contextCeilingRatio=0.90`(:41)、无实测锚点时 0.80(:48)；
`CapMessagesToWindow`(:155) 从头丢到落进上限、顺带修工具配对(`skipOrphanResults:222`)、
插入一行 `[滑窗] 已省略更早的 N 条…`(:241)。**滑窗只换起点不改正文**——任何正文改写算压缩不算滑窗。
`enforceRequestCeiling`(:293) 是"绝不发出必然被拒请求"的最后一道确定性防线；
provider 报超窗后 `overflow_adjust.go` 收紧预算再走滑窗。

**压缩接管协议**：压缩是**插件能力**，agent 只调度、且强依赖它。
- 能力声明 `Provides: {"compaction":"true"}`；岗位解析唯一入口
  `ResolveCompactionRole`(`core/notify.go:133`) → `pickCompactionProviderLocked`(:157)：
  配置点名且合格者当值 → 未点名时唯一候选自动当值 → 多候选未点名则**空缺**并要求点名。
- 交接面是 `agent/pre-step` 的 waterfall：岗位持有者收到完整 `messages_json` + `tool_tokens`，
  返回改写后列表；`agent/request-error` 返回 `{"retry":true}` 触发溢出重压后重试。
- 两个阶段（`PreStepStageCompaction` / `PreStepStageProjection`，`sdk/protocol.go:57`）：
  压缩是单一写者，投影（`pre-step-transform` 能力）可叠加且必须无损（不改条数/角色序）。
- 状态**只写旁挂文件** `<session>.<plugin>.json`（`dsc-system/compaction-state.go`），
  **不得**写会话日志——否则"滑窗窗口内全是原始消息、日志一字不改"的不变量失效。

**TokenMeter**（`core/token_meter.go` 595 行）：`TokenMeasurement`(:33) 有
`TotalTokens/PromptTokens/CompletionTokens/CacheRead/CacheCreation/ContextWindow/Pressure/ProjectedTokens/Source`。
核心是"实测锚点 + 启发式增量"：`SampleUsage(promptSideTokens, sentEstimate)`(:102) 记录
同一请求的服务端用量与该次发出的表面估算，`Project(currentEstimate)`(:128) 返回
`pressure + (current - sampled)`；没有基线时直接返回入参估算，不拿旧实测糊弄。
`PromptSideTokens`(:153) **就是 PromptTokens**（OpenAI 兼容端点已含缓存，不再叠加 cache_read）。
估算按字符类别加权（`token_coef.go`，可经 `DSC_TOKEN_COEF` 注入、运行期自动拟合）。

**prompt 装配**：system prompt 是 surface 的 node 0（`system/message`，后续变化用 replace 覆盖 node 0，
空 content 为占位，`ProjectSystemPrompt:459`）；插件片段经 `PluginHookService.ListContext`
聚合（`manager.go:1444`，按**插件名升序**拼接，保证前缀缓存稳定）。

---

## 8. TUI

库：`charm.land/bubbletea/v2`（Elm 架构）+ `bubbles/v2`（textarea/viewport/spinner/key）+
`lipgloss/v2`；`goldmark` 渲染 markdown；`atotto/clipboard`。TUI 跑在**宿主进程**内
（不进插件子进程，因为它要直接操作 raw mode 与 stdout）。
结构：`tui.go`(4501) 是 Model/Update/View 与全部渲染；`question.go`(617) 提问面板、
`markdown.go`(430)/`stream_markdown.go`(124) 增量渲染、`thinking.go`(334) 思考块、
`todopanel.go`(132) 待办、`scrollbar.go`、`diffview.go`、`image_ref.go`、`plugin_admin.go`、
`config_picker.go`、`at_complete.go`、`wakeup.go`。

斜杆命令（`dispatchSlashCommand:3154`，内置于**界面层**）：`/help /clear /skills /mode
{minimal|standard|creation|ptc} /sessions /plan[ on|off] /session <id>|new|delete
/export /config /plugins /crons /cron … /settings history|mouse /mcp … /approval
/sandbox /quit|/exit`。**插件命令不在这个 switch 里**：`buildHelpText` 末尾动态追加
`pluginCommands()`，宿主按 `metadata.commands` 声明清单把 `/<name>` 转发到
`PluginHookService.RunCommand`（`sdk/hook.go:120`），Unimplemented 时明确报"命令不可用"。

**结构化视图渲染器**：`renderViewSpec(viewJSON)`(`tui/tui.go:2581`) 单一入口按
`ToolView.Kind` 派发到 `renderCardView`(:2602) / `renderTableView`(:2673) / `renderPlainView`(:2722)，
视图缺失或解析失败回退通用展示（`renderToolResultFrame`）。视图只影响显示，不影响模型输入。

---

## 9. PTC `run_code` 与 workflow

**`run_code`**（`core/run_code.go` 191 行）：宿主内置工具，**只在 PTC 模式**可执行
（`executeToolBody:189-191` 拦 native 模式）。执行流程：
1. 由工具目录生成 Lua SDK —— `coderuntime.GenerateSDK`(`coderuntime/sdk.go:20`) 给每个工具
   生成同名全局函数 `<tool>{args}`，内部 `local ok,r = tool("name", args)`，失败 `error(r,0)`；
   只对合法 Lua 标识符生成，其余仍可 `tool(name,args)`。跳过 `run_code` 自身与一切
   `ViewImageExecutor`（Lua 绑定只能回字符串，图像会静默丢失）。
2. `coderuntime.Run(ctx, Options{Script, Tool, Bindings, Timeout})`(`coderuntime/coderuntime.go:69`)：
   `lua.NewState()` + 只开 `base/string/table/math`（**无 os/io**）；
   整段程序跑在**一个 coroutine** 里，Go 侧 `tool` 绑定用 `L.Yield(request)` 挂起线程、
   驱动方执行宿主工具后 `L.Resume(co, ok, result)` 续行——"await"由框架隐式完成，
   脚本不必感知协程。
3. 结果 `Result{Value,StopReason(completed|error|cancelled),Error,ToolCalls[],Logs[]}`
   以 JSON 返回，**失败建模为字段而非 Go error**；再经 `runCodeView:110` 渲染成
   `plain` 视图（badge = stop_reason，色 green/red/yellow）。

**workflow**（`workflow/` 655 行 + `core/workflow.go` 223 行）：另一个宿主内置工具，
Lua 编排脚本扇出 subagent。`Meta{name,description,when_to_use,phases}` 必填，
`Start` 先做 `checkScriptSyntax`(`workflow/engine.go:23` 只 parse 不跑)。脚本同步模型、协程排程在
引擎内：actor 排程器 `sched`(`workflow/engine.go:45` 带 `sem chan struct{}` 信号量、`ready/wait/groups`
队列、`doneCh` 回传)；`agent(prompt,opts)` 同步扇出一个 subagent 返回文本（失败 nil）；
`parallel(thunks)` 受并发上限并发、按序返回 `{ok,value}`；`pipeline(items,...stages)`
**无跨阶段屏障**逐项跑 `(previous,item,index)`；`phase(title)`/`log(msg)` 记进度。
失败以 `stopReason=error` 兑现、取消以 `cancelled` 兑现，`result` 不 reject；
致命错误（`INVALID_ARGUMENT`/`AGENT_CAP`/`ITEM_CAP`）逸出组合器。
`background:true` 时转入 job 注册表，返回 job_id 由 `job_output`/`job_kill` 管理。
