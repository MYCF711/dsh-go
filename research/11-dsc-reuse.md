# 11 · dsc 复用评估：哪些能拿、哪些要改、哪些必须重写

> 基线 `refs/dsc`（v3.0.0）。行数为**非测试**代码。结论按"能不能原样进新仓"分三档：
> **直接拿**（不改语义、改 import 即可）、**要改**（机制对、契约/硬编码要动）、**重写**（形状与目标不符）。

## 0. 一句话结论

**可以作基座，但只能作"机制基座"不能作"契约基座"**：dsc 把 DSH 的**运行时机制**（五模式事件总线、
三槽工具流水线、审批/沙箱、活跃续命看门狗、事件溯源会话、滑窗、PTC Lua 运行时）实现得比多数
Go 项目完整：**直接可用 5.8k 行**（§1.1），**小改可用约 2 万行**（§1.2，实际动手约 4-6k 行）；
但它的**对外契约**（go-plugin 子进程 ABI、
text-first 工具结果、21 种自造会话事件、内核内置 9 族工具）与 DSH 不是同一条线，
整体 fork 后打补丁会比"拿机制 + 重写契约"更贵。

---

## 1. 子系统裁决表

### 1.1 直接拿（5.8k 行，机制纯、无业务词）

| 模块 | 行数 | 为什么能直接拿 |
|---|---|---|
| `core/eventbus.go` | 286 | 五模式（emit/serial/bail/waterfall/parallel）已照 Cordis 实现，含 listener panic recover 与 `Parallel` 的 goroutine 内 recover。零业务。 |
| `core/activity.go` | 139 | `WithIdleDeadline`/`TouchActivity`/`ErrIdleDeadline` 三元组 + `renewIdleFromActivity`（跨进程轮询续命）。这是"活动续命"少见的正确解：ctx value 过不了 gRPC，所以由宿主反查 `GetActivity`。 |
| `core/context_ceiling.go` + `core/overflow_adjust.go` | 330+188 | 滑窗（只换起点不改正文）+ 90%/80% 双水位 + 孤儿 tool_result 修复 + 抽查超窗后收紧。 |
| `core/view.go` + `sdk/view.go` | 45+50 | `ToolView{kind,title,badge,fields,columns,rows,body}` 与 card/table/plain 三个构造器，显示契约干净。 |
| `jobs/` | 652 | 后台任务注册表：owner 隔离、流式 vs 终值两种输出模型、killed 与 failed 区分。 |
| `cron/` | 454 | 定时任务，独立小包。 |
| `coderuntime/` + `lualib/` | 295+142 | Lua 协程 yield/resume 驱动工具调用（脚本无感 async），只开 base/string/table/math。 |
| `workflow/` | 655 | actor 排程器 + `parallel`/`pipeline`（无跨阶段屏障）+ `agent()` 扇出。 |
| `plugins/dsc-system/{spill,reminder,timeout}.go` | 258+276+85 | 外置/重复提醒/活跃续命超时三件策略，正好演示"策略全在插件、宿主只执行裁决"。 |
| `sdk/mcp/` | 1662 | 自带 MCP 客户端（streamable-HTTP + stdio），只依赖 stdlib。 |
| `plugins/tool-filesystem/encoding*.go` | 113+75+38+101=327 | 两级 UTF-8 还原（整段严格解码失败再逐行），中文 Windows 混码页输出的正解。 |

### 1.2 要改（表内 20.6k 行；机制对、契约/硬编码要动，实际动手约 4-6k 行）

| 模块 | 行数 | 要改什么 |
|---|---|---|
| `core/tool_pipeline.go` | 727 | 骨架（三槽 waterfall + veto + notices + 后台分派）可用；但**结果类型**是 `(string,error)`，改结构化就得动签名与返回值。`executeToolBody` 里 `run_code` 名字与 PTC 判定要外提。 |
| `core/approval.go` + `authorization.go` + `sandbox.go` | 521+157+204 | 语义方向完全对（scopes 声明 + 粒度策略 + 升级阶梯 + fail-closed 账本）；**必须改**：`sandbox.go:52` 的 `writeCapableExecutors={"shell"}`、`:95` 的 `writeCallInfo` 只认 `str_replace_editor`、`approval.go:150` 的 `toolName=="shell"`——都是"宿主认识具体工具名"，与 §14 红线自相矛盾，应改成工具自声明写能力。Windows 上也没有 OS 级隔离，只有参数路径判定 + Job Object。 |
| `core/notify.go` | 792 | 事件跨进程分发 + 压缩岗位解析可用；但"压缩岗位唯一"、"多候选必须点名"（`:157`/`:184`）是 dsc 自定的编制规则，要按目标契约复核。 |
| `core/token_meter.go` + `token_coef.go` + `token_calibration.go` | 595+507+382 | "实测锚点 + 启发式增量"口径是对的、注释里的历史事故也值得抄；但要换成目标侧的分词契约，系数表与自动拟合需要重标定。 |
| `session/` | 1796 | 事件溯源 + surface 投影 + 稳定 id（`event-<seq>`）+ jsonl 日志结构可直接用；但**21 种事件类型词汇**要按目标契约对齐，`DeriveMessages`/`DeriveMessagesLimited` 的边界语义要重写。 |
| `core/manager.go` | 2998 | 五类插件加载 + PENDING 反应式重算 + 能力依赖解析 + 5 个热重载函数（`:808/:878/:981/:1099/:1160/:1228`）是**最有价值的单文件**；要改的是类型判定与 `validatePluginDirectoryName` 的 `<type>-<name>` 目录约定。 |
| `tui/` | 7393 | 若目标有 TUI：`renderViewSpec` 单一渲染器（`tui.go:2581`）可留；`tui.go` 4501 里内嵌的斜杆命令 switch 必须改成声明驱动。 |
| `plugin/` | 4519 | fork 的 go-plugin（`module github.com/hashicorp/go-plugin`）加了 broker 扩展。要不要它取决于插件进程模型决策，见 §3 第 1 条。 |

### 1.3 必须重写（≈11k 行，形状与目标不符；其中 6.8k 是 proto 生成码）

| 模块 | 行数 | 原因 |
|---|---|---|
| `proto/dsc.proto` + 生成物 | 568 + 6832 | 9 个 service/36 RPC 是围绕 go-plugin 子进程设计的。目标若是 in-process 插件模型，这套 ABI 整体作废（生成码 6832 行跟着废）。 |
| `core/dsc_plugin_tool.go` | 992 | 插件安装器绑死了目录名 `<type>-<name>`、`.exe` 后缀、`plugins/` 布局、`install/uninstall/load/unload/upgrade` 六个工具名。 |
| `main.go` 的 LLM 端点探测段 | ~450（`main.go:85-465`） | 探 `/v1/models` 的 `n_ctx`、按 provider 分支、`max_tokens` 探测——provider 特定业务。 |
| `core/dsc_plugin_tool.go` 之外的内置工具族 | ~2700 | 见 §2 Q1 清单，必须搬成插件（或至少搬出内核）。 |
| TUI 斜杆命令 switch（`tui.go:3154-3345`） | ~190 | 内置了 `/mode {minimal,standard,creation,ptc}` 等预设名。 |
| Web/HTTP 呈现层 | 0 | dsc 只有 TUI，没有任何 web 前端；`libs/vodka` 是自研 web 框架但只用于 admin REST。 |

---

## 2. 三个问题

### Q1 · dsc 的 host 是不是「内核零业务逻辑」？

**不是**。它对"策略归插件"这条执行得相当好（审批理由、超时预算、spill 阈值、重复阈值、压缩策略、
沙箱被判定的工具族都在插件侧），但内核里仍有 7 类硬编码业务：

1. **9 族内置工具挂在 `Manager` 上**，不是插件（注册点 `core/manager.go:439-458`）：
   `subagent`(`core/subagent.go:259`)、`workflow`(`core/workflow.go:223`)、`run_code`(`core/run_code.go:191`)、
   `job_output/job_list/job_kill`(`core/jobs.go:520`)、`cron_add/cron_list/cron_remove/cron_set_enabled`
   (`core/cron_tools.go:364`)、`install/uninstall/list/upgrade/load/unload_dsc_plugin`(`core/dsc_plugin_tool.go:992`)、
   `context_search`(`core/context_search_tool.go:410`)、`read_image`(`core/read_image_tool.go:238`)、
   `lsp_diagnostics`(`core/lsp.go:288`)。合计约 2.7k 行内核业务。
2. **沙箱认识工具名**：`writeCapableExecutors = {"shell":true}`(`core/sandbox.go:52`)；
   `writeCallInfo` 只解析 `str_replace_editor`(`:95-109`)。注释自认这是"最小封堵"，长期正解是自声明。
3. **审批认识工具名**：升级提示里 `toolName=="shell"` 决定说 "command" 还是 "operation"(`core/approval.go:149-150`)。
4. **TUI 认识预设名**：`/mode minimal|standard|creation|ptc` 写死在前端 switch(`tui/tui.go:3203`)。
5. **装配策略写在内核**：只允许一个 agent（`manager.go:1872` 多余者 warn 丢弃）、
   primary LLM 由 `default_llm` 选（`manager.go:1944`）、压缩岗位唯一且多候选必须点名
   （`notify.go:157/184`）——这些是"编制规则"，不是机制。
6. **PTC 判定认识工具名**：`runCodeToolName = "run_code"` 保留名 + `isPTC()` 门控
   (`core/run_code.go:17`、`core/tool_pipeline.go:189-191`)。
7. **token 估算系数表**（`core/token_coef.go` 507 行）写在内核，虽然可由 env 注入与在线拟合。

→ 改造方向明确：把 1、4 搬出去；2、3、6 换成声明式接口（dsc 自己已经在 `Scopes`/`Management`/
`Capabilities` 上走对了一半）；5 换成岗位声明（"编制由岗位性质决定"这条 AGENTS 规则已经写出来了，
只是还没落到 agent/LLM 上）。

### Q2 · dsc 的工具层是不是「结构化优先」？工具结果是什么形态？

**不是结构化优先，是 text-first + 显示层结构化。** 三层证据：

1. **插件侧签名只有一个字符串返回**：`Handler func(ctx, args json.RawMessage) (string, error)`
   (`sdk/tool.go:27`)；宿主侧 `ToolDefinition.Execute(ctx,args) (string, error)`(`core/tool.go:9`)。
2. **协议层**：`ExecuteToolResponse{ content string, error string, view_json string, images[]string,
   notices[]string }`（`proto/dsc.proto:497-511`）。`content` 是必答的纯文本，
   `view_json` 是"可选、缺失时 TUI 回退通用展示"。
3. **`view_json` 不参与模型投影**：唯一消费者是 TUI 的 `renderViewSpec`(`tui/tui.go:2581`)；
   会话里 `tool/result` 事件存的是 `content`，模型拿到的是 `content` 再经
   `toonizeToolContent`(`session/session.go:581`) 做**字符串内部**的 TOON 紧凑化——
   本质仍是文本，只是把"JSON 字符串"压成"更省 token 的文本表"。

所以 dsc 的答案：**模型可见 = 纯文本 content（内容可以是工具自选的 JSON/TOON/人话）；
结构化 = 仅显示层 view spec，与模型无关。** 若目标要真正的结构化优先，最小改动是：
把 `Execute` 返回类型换成带类型的结果体，让 `view` 从"显示层"上移为"协议层"、
`content` 降级为渲染产物（缓存/惰性生成）。这会波及 `core/tool_pipeline.go` 的 `ToolInvocation`、
`sdk/tool.go`、21 个插件（~37k 行）的 Handler 签名、以及 TUI/前端渲染——是本方案里
**改动面第二大**的一项。

### Q3 · 以 dsc 为基座改成「DSH 兼容宿主」还差哪几块，各自量级

| # | 缺口 | 现状 | 量级 |
|---|---|---|---|
| 1 | **插件 ABI 取向**：go-plugin 子进程 vs 进程内 ctx | dsc 是子进程 + gRPC，`proto` 568 + 生成码 6832 | 若坚持子进程：改 proto 字段 ~300 行，**保留**6.8k 生成码；若要 in-process 或要跑 TS 插件：需嵌 goja/quickjs bridge，**新增 3000-5000 行**，且 `plugin/` 4519 与 `proto` 全部弃用 |
| 2 | **工具结果结构化上移**（Q2） | text-first | 宿主侧 ~800-1200 行 + 插件侧签名波及 ~37k 行（机械改动） |
| 3 | **会话事件契约对齐** | 21 种自造事件；`session/` 1796 行 | 投影/派生/持久化重写 ~1500-2500 行 |
| 4 | **事件面补齐** | 只对齐 12 个 DSH 事件（`core/agent_events.go` + `tool_pipeline.go` + `approval.go`） | 目标侧事件面通常 40-60 个 → 定义 + 分发点 + 跨进程投递 ~1200-1800 行 |
| 5 | **内核去业务化** | 9 族内置工具 ~2700 行悬挂在内核 | 搬成插件（可复用 dsc 的插件骨架，每个 100-400 行）+ 新增"内核工具注册缝" ~300 行 |
| 6 | **沙箱/审批去工具名** | `sandbox.go`/`approval.go` 认识 `shell`、`str_replace_editor` | 改声明式：~400-600 行（工具侧加写能力声明，宿主侧删分支） |
| 7 | **呈现层** | 只有 TUI（7393）；web 层为零 | 若要 web：宿主侧 API/SSE ~1500-2500 行 + 前端全新 |
| 8 | **压缩岗位编制的通则化** | 只有 compaction 一个岗位，规则硬编码在 `notify.go:133-232` | 抽成通用"岗位（role）+ 编制（N）"抽象：~400-700 行 |

**总量级**：保留 go-plugin 子进程模型时，可复用约 60-65%（≈1.4 万行直接搬 + ≈7 千行改造），
净新增/重写约 **1.5 万-2.5 万行**；若改成 in-process 插件模型，`plugin/`+`proto/` 共 1.1 万行作废，
净重写量升到 **2.5 万-3.5 万行**。建议前者。

---

## 3. 三个最值得直接搬的模块

1. **`core/eventbus.go`（286 行）** —— 五模式事件总线，签名与 Cordis `events.ts` 一一对应，
   panic 隔离到位（`Parallel` 在 goroutine 内 recover，否则 crash 宿主）。改 import 即用。
2. **`core/tool_pipeline.go`（727 行）+ `core/activity.go`（139 行）** —— 三槽 waterfall 流水线
   （pre/execute/post + result emit）、veto 三条路径、`notices` advisory 收集、后台/宽限期分派，
   配 `WithIdleDeadline`+`renewIdleFromActivity` 的"活跃续命"看门狗。这套是 dsc 最硬的部分：
   它把"超时"从"固定墙钟"变成"无活动多久才判死"，且解决了 ctx value 过不了 gRPC 的真问题。
3. **`session/`（1796 行）+ `core/context_ceiling.go`（330 行）** —— 事件溯源会话日志
   （append-only + surface 投影 + `event-<seq>` 稳定消息身份 + jsonl）与滑窗收口
   （只换起点不改正文，孤立 tool_result 一并修，插一行"已省略 N 条"）。压缩之所以能做成"旁挂状态、
   不写会话日志"，前提就是消息身份稳定——这两块必须一起拿。
