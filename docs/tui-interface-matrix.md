# DSH-GO ↔ dsh-TUI 接口矩阵（W174 · W152 正式包 / W167 后续 NO-GO）

> 日期：2026-11-06（Asia/Shanghai）。本任务只刷新本文，没有修改产品代码、PLAN 或 UIPLAN，也没有重跑真实 ConPTY。
> 目标：保留 W130 的 **64 条命令**与 AgentSession 能力基线，以 W152 全接口正式包终验为当前完整实测基线，再叠加 W161–W173 的后续修复、复核与未收口项；W148 只保留为 hash 固定的历史快照。

## 0. 基线、口径与结论

### 0.1 坐标与证据别名

- **T**：`../dsh-TUI-upstream-src/`，包 `@deepseek-harness-tui/dsh-tui` **0.18.0**。官方命令审计固定基线仍为 `1bd7cb515b4c6fb52f049f7bde521df48ee2f54b`；当前工作副本没有 `.git`，本文不虚构新 rev。
- **G**：本仓 HEAD `af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当前共享工作树。共享树有大量其他会话改动，HEAD 不是洁净或稳定版证明。
- **B**：T 的正式 registry `src/backends/dsh-go/`。W147 已完成首轮正式 backend/events/manifest 接线；W155/W159 又接入 MCP/sideQuery 与 rename/color/init，W157/W158/W160/W162/W165/W166 继续修复真实链路。 scoped 接线不等于最终整包验收，最终封版只以 P 的 W163 为准。
- **P**：[PLAN.md](../PLAN.md)，当前权威任务状态见 W152/W161–W173（`P:515-530`）；本文不修改 P，也不以本文覆盖其封版门。
- **E152**：[W152 最终全接口真实验收](../../dsh-TUI-upstream-src/tmp/W152/RESULT.md)；**C152**：[W152 64 命令与 AgentSession 逐项矩阵](../../dsh-TUI-upstream-src/tmp/W152/COMMAND_MATRIX.md)。
- **E153**：[W153 真实验收](../../dsh-TUI-upstream-src/tmp/W153/RESULT.md)；其 OK258/FAIL9 是后续 W157/W158 修复前的历史快照，不再作为当前未完成口径。
- **E161**：[W161 restart 复核](../../dsh-TUI-upstream-src/tmp/W161/RESULT.md)；**E164**：[W164 旧 conformance 四红审计](../../dsh-TUI-upstream-src/tmp/W164/RESULT.md)。
- **E167**：[W167 真实 inline / 窄终端 ConPTY 补验](C:/Users/Administrator/AppData/Local/Temp/dsh-TUI-narrow-inline-w167/tmp/W167/RESULT.md)；**E168**：[W168 当前 export projection 复核](../../dsh-TUI-upstream-src/tmp/W168/RESULT.md)；**E169**：[W169 三键与 direct now 复核](../../dsh-TUI-upstream-src/tmp/W169/RESULT.md)。
- **E148**：[W148 历史真实 ConPTY / wire / v4 证据](../../dsh-TUI-upstream-src/tmp/tui-steer-conformance/RESULT.md)；**E146**：[W146 历史前置复验](../../dsh-TUI-upstream-src/tmp/tui-current-conformance/RESULT-W134.md)。二者不得覆盖较新的 W152/W161/W169。
- **R1508**：[2026-11-06 15:08 复核](scheduled-review-2026-11-06-1508.md)，记录 W161/W162/W164/W165/W166 已收口及 W167–W173 的后续分派（`R1508:9-14`）。
- **OPS**：[session operations host note](tui-session-operations-host-note.md)；**EG**：[effort/modes/goals host note](tui-effort-modes-goals-host-note.md)；**IMG**：[images host note](tui-images-host-note.md)；**JOBS**：[jobs host note](tui-jobs-host-note.md)；**CTX**：[context/auth host note](tui-context-host-note.md)；**META**：[metadata/init host note](tui-metadata-init-host-note.md)；**TRACE**：[trace capability note](tui-trace-capability.md)；**VIEW**：[view capability note](tui-view-capability.md)。

状态含义：

| 状态 | 本文判据 |
|---|---|
| **IMPLEMENTED · W152** | W152 固定正式包中有真实 dshd/ConPTY 或该命令官方机制所需的本地/fixture 证据；后续当前树变化在证据列另写，不把固定包结果冒充 W163 整包复验。 |
| **IMPLEMENTED · scoped** | helper、宿主或正式 backend 已有实现和 Lead scoped 复验，但最新组合整包尚未验收。 |
| **FAIL · current** | 较新的当前树证据仍明确失败；旧快照的 PASS 不得抵销。 |
| **PARTIAL / MISSING** | 只有部分语义，或正式 backend 与宿主仍没有可用产品路径；不由其他 capability 冒充。 |
| **NOT-OFFICIAL** | 固定上游没有该通用接口，或 requirement map 明确为 DSH/其他 backend 专属；W152 的负向 gate OK 不把它变成 dsh-go 正向能力。 |

### 0.2 结论先行

1. **W152 = `DONE · NO-GO`，机器计数 `OK=645 FAIL=5 UNVERIFIED=0`，64 命令 `63/1/0`、AgentSession `29/0/0`，`W152_GO_ALLOWED=0`。** 唯一命令 FAIL 是当时驱动下的 `/restart`；其余 4 个 FAIL 来自无 catalog + `DSH_GO_MODEL` 的 baseline/placement 探针。完整逐项结果见 `E152:3-9,56-95,162-168` 与 `C152:5-104`。
2. **W152 的两个旧阻塞不能继续写成当前产品缺陷。** W161 证明 restart 原红是占位断言与 `/q` 后主屏采样造成的假阳性，生产 Go/TS 零改；Lead 使用全新 home 复跑 **OK8/0**。W162 已修无 catalog 显式模型回退，Lead **Node393/393 + typecheck/boundary/compile 全 0**；W165 已补无 catalog 环境模型真实 effort 元数据，Lead 全仓 TEST/VET/BUILD 全 0（`P:517-518,530`；`E161:3-27`）。
3. **W164 审计已完成，W166 已修其唯一真实产品竞态。** W164 将旧四红判为三项旧断言/级联与一项公开 `turn.end/status=idle` 早于宿主可提交屏障；W166 暂缓公开完成至 raw `turn/done` 或终态 `turn/error`，Lead **Node397/397 + typecheck/boundary/compile 全 0**，ab-race 修后 OK7/0（`P:528-529`；`E164:3-16,109-177`）。
4. **W167 是固定 W160 副本的后续 NO-GO：80×24 `69/5`、120×36 `75/8`，direct now wire 另为 16/0。** 其问题已分流：W169 当前三键/direct now 全 OK、无需产品修复；W170 输入修复真实双尺寸各 **OK44/0**，Lead reverse-check + Win32 + **Node397/397** 全绿但仅 scoped；W168 当前 export projection 仍 FAIL 并交 W173；W171 当前审计 inline scrollback，W172 修 inline replay/resize（`P:520-527`；`E167:3-7,53-83`）。
5. **当前仍未封版。** W163 当前口径为 `BLOCKED(W168,W170,W171,W172,W173)`：W169 已关闭，W170 已 scoped 绿但整包未验，W168/W171/W172/W173 尚未全部关闭。必须用新 dshd、W162+W165+W166 及后续修复的新 tarball、每轮唯一 home 重跑 W152 全套与修正后的 restart/conformance；最终状态、依赖和是否允许覆盖 GO.1 一律以 P 的 W163 为准（`P:519,522,524-527`）。
6. **64 命令基线不变，但不能写成“当前 64/64 已全绿”。** 下表仍是 **64 = 29 any + 21 capability + 14 dsh**。W152 的 63/1 是固定包实测；W161 已纠正 restart，W168 却又在当前树证实 `/export` 内容保留失败，W171/W172 还有 inline 渲染问题。通用 `commands` 只有 `list()`，没有通用 `commands.execute()`；固定上游也没有 slash `/view`（`VIEW:27-70`）。
7. **W149/W150 旧 absent 口径作废。** W149 的 context full/MCP/sideQuery 与 W150 的 rename/color/workspace.init 已完成宿主 scoped 交付，W155/W159 已接正式 backend/events/manifest，W152 对相应命令与 AgentSession 能力均有真实 OK；不得再写“宿主未收口”或“capabilities 仍 absent”（`P:509,511,515,531,534`；`C152:76-104`）。
8. **W148 保留为历史快照。** 其 `81/0/1`、真实 ConPTY 35+25、独立 now wire 16、v4 audit 5、旧包 images UNVERIFIED 和最终 bin-check 漂移均原样保留；后续 W152/W169 的结果不倒填进 W148。

## 1. requirement map 全量命令矩阵

下表保留 W130 的 64 行与 `LOCAL_COMMANDS` 一一对应关系。除明确标注后续证据外，`W152` 表示该固定正式包的真实验收，不表示本轮重跑，也不代替 W163 最新组合整包终验。

### 1.1 `any`：29 条

| 命令 | 上游真实调用与证据 | 当前状态 | 当前证据与未验项 |
|---|---|---|---|
| `/new` | `channel.newSession()` → `open({kind:'create'})`；T `Chat.tsx:3346-3373`，`core/session-switch.ts:458-479` | **IMPLEMENTED · W152** | W152 真实创建与新 id 通过；W148 的 1→2 仅作历史（`C152:7`；`E148:157-158`）。 |
| `/clear` | `channel.clear()`，仅清当前视图；`Chat.tsx:3375-3385`，`core/local-actions.ts:69-79` | **IMPLEMENTED · local/W152** | 官方 view-only，W152 本地机制通过；不需要删除 v4 的宿主 API（`C152:8`）。 |
| `/export` | `channel.exportSession()`；`Chat.tsx:3958-3965`，`core/reports.ts:58-83` | **FAIL · current projection** | W152/W148 的旧导出 PASS 保留；W168 当前 W162+W166 树在 80 `25/8`、120 `26/7`，缺已完成 TEXT/JOB_PARENT assistant 与 reasoning，preDone/postDone 一致；W173 正在修跨 root turn 行身份（`E168:3-5,18-55`；`P:521-522`）。 |
| `/trace` | `openScene({kind:'trace'})`；`Chat.tsx:3408-3413`，`core/compose.ts:725-744` | **IMPLEMENTED · W152** | W152 基础 trace 通过；完整 raw history/step 由 W157/W152 收口，导出内容保留仍按 `/export` 的 W168/W173 单列（`C152:18`；`TRACE:9-15`）。 |
| `/status` | 本地汇总 session/cwd/model/mode/effort/tokens；`Chat.tsx:3692-3717` | **IMPLEMENTED · local/W152** | 通用本地汇总与 ready/load 真值，未知值保持 unknown（`C152:23`）。 |
| `/cost` | `channel.costReport ?? estimateCost(model,usage)`；`Chat.tsx:3720-3767` | **IMPLEMENTED · local/W152** | 通用 usage/价格估算，没有 backend-reported cost，不伪称有（`C152:24`）。 |
| `/settings` | `setSettingsOpen(true)`；`Chat.tsx:3778-3812` | **IMPLEMENTED · local/W152** | TUI settings surface；G additive settings/model catalog 不等于新增通用命令（`C152:27`）。 |
| `/star` | `host.github.star(dshRepository)`；`Chat.tsx:3835-3840` | **IMPLEMENTED · local/W152** | TUI 宿主 GitHub action，与 dshd 无关；W152 fixture 12/0（`C152:29`；`E152:82`）。 |
| `/doctor` | `channel.doctorInfo()`；`Chat.tsx:3855-3857`，`core/reports.ts:42-55` | **IMPLEMENTED · local/W152** | 通用 Node/backend/model/cwd/session 报告（`C152:30`）。 |
| `/panel` | `channel.setActivePanel/openPanel`；`Chat.tsx:3989-4019` | **IMPLEMENTED · local/W152** | 面板壳与 jobs/agents 数据分别验收；W152 jobs/agentview/managers 均过（`C152:19-21,36`）。 |
| `/activity` | `setActivityFrames` + `writeActivityFrames`；`Chat.tsx:3098-3141` | **IMPLEMENTED · local/W152** | TUI 本地偏好与动画，不经 dshd（`C152:37`）。 |
| `/theme` | `channel.themes/setTheme/switchTheme`；`Chat.tsx:3259-3301` | **IMPLEMENTED · local/W152** | TUI theme host；与 G `config.*` 不是同一接口（`C152:39`）。 |
| `/lang` | `setLang/writeLang`；`Chat.tsx:3227-3257` | **IMPLEMENTED · local/W152** | TUI 本地 i18n（`C152:41`）。 |
| `/thinking` | `setThinkingFold/writeThinkingFold`；`Chat.tsx:3528-3533` | **IMPLEMENTED · local/W152** | UI fold 偏好；真实 reasoning stream 另见 AgentSession 矩阵（`C152:44`）。 |
| `/tokens` | `localTokenUsage(usageTotals,state)`；`Chat.tsx:3535-3555` | **IMPLEMENTED · local/W152** | 通用 usage 投影（`C152:45`）。 |
| `/update` | `host.onUpdate()`；`Chat.tsx:4222-4231` | **IMPLEMENTED · local/W152** | TUI 进程更新器；W152 notice/spawn fixture 通过（`C152:55`；`E152:83-84`）。 |
| `/vim` | `writeVimMode(true)` + reload；`Chat.tsx:4391-4398` | **IMPLEMENTED · local/W152** | TUI 本地编辑器偏好（`C152:56`）。 |
| `/terminal-setup` | 本地 terminal installer；`Chat.tsx:4400-4405` | **IMPLEMENTED · local/W152** | 安装终端 binding，不经 dshd（`C152:57`）。 |
| `/connect` | `host.remotes()?.report() ?? ['connect-none']`；`Chat.tsx:4484-4486` | **IMPLEMENTED · local/W152** | 官方行为是 remote report/无 remote 提示，不是新建 remote 的 dsh-go API（`C152:58`）。 |
| `/help` | `setHelpOpen(true)`；`Chat.tsx:3424-3426` | **IMPLEMENTED · local/W152** | 通用 UI；W170 还复验直接输入后 Help 不受碎片污染（`C152:61`；`P:525`）。 |
| `/tips` | `setShowTips(true)`；`Chat.tsx:4480-4482` | **IMPLEMENTED · local/W152** | 通用 UI（`C152:62`）。 |
| `/kernel` | `host.onBackToKernel()`；`Chat.tsx:4355-4357` | **IMPLEMENTED · local/W152** | 返回 backend picker（`C152:63`）。 |
| `/dsh` | `host.onSelectBackend('dsh')`；`Chat.tsx:4359-4366` | **IMPLEMENTED · local/W152** | 切到进程内 DSH，不表示 dsh-go 获得 DSH registry（`C152:64`）。 |
| `/codex` | `host.onSelectBackend('codex')`；同上 | **IMPLEMENTED · local/W152** | TUI 宿主 backend 切换（`C152:65`）。 |
| `/claude` | `host.onSelectBackend('claude')`；同上 | **IMPLEMENTED · local/W152** | TUI 宿主 backend 切换（`C152:66`）。 |
| `/restart` | `host.onRestart()`；`Chat.tsx:4368-4373` | **IMPLEMENTED · ConPTY/W161** | W152 原 FAIL 已证为驱动假阳性；修正驱动验证新 PID、同 durable session、历史与输入框重挂载。Lead 全新 home OK8/0；每次运行必须使用唯一 home（`E161:3-27`；`P:517`）。 |
| `/exit` | `exitApp()`；`Chat.tsx:3687-3690` | **IMPLEMENTED · W152** | W152 独立生命周期退出 4/0（`C152:68`；`E152:73`）。 |
| `/quit` | 同 `/exit` | **IMPLEMENTED · W152** | W152 独立退出 4/0；W168 80×24 另有一次 quit 驱动项未在 20s 观察到 exit，保留给 W163 复验，不改写 W152（`C152:69`；`E168:61`）。 |
| `/q` | 同 `/exit` | **IMPLEMENTED · W152** | W152 独立退出 4/0；不得用退出后的主屏内容判 restart（`C152:70`；`E161:5-9`）。 |

### 1.2 typed capability / backend-level：21 条

| 命令 | gate 与上游真实调用 | 当前状态 | 当前证据与未验项 |
|---|---|---|---|
| `/compact` | `caps.compact.run()`；`Chat.tsx:3388-3406`，`core/actions.ts:38-44` | **IMPLEMENTED · W152** | 真实 compact 与 durable 变化在 W148/W152 均通过（`C152:9`；`E148:166-169`）。 |
| `/resume` | backend `catalog.list/info/preview` + `open({kind:'resume'})`；`core/session-switch.ts:481-511` | **IMPLEMENTED · W152** | W152 通过；W167 也在两尺寸恢复原非空 session（`C152:10`；`E167:72`）。 |
| `/rename` | `caps.rename.set(title)`；`core/actions.ts:147-153` | **IMPLEMENTED · W152** | W142 helpers + W150 真实 METADATA_INIT 86/0 + W159 正式 capability 已收口；W152 真实 rename/local surface OK，不再 absent（`P:511,531,535`；`C152:11`）。 |
| `/recap` | `caps.sideQuery.run(...)`；`core/actions.ts:120-130` | **IMPLEMENTED · W152** | W149 独立无工具 runner + W155 正式 side.query/cancel/close 已收口；W152 side commands 8/0，未用普通 `session.prompt` 冒充（`P:509,534`；`C152:12`）。 |
| `/rewind` | `caps.rewind.preview(anchor)/run(anchor,mode)`；`core/sessions.ts:273-317` | **IMPLEMENTED · W152** | W145 checkpoint/files-first/partial、W147 正式接线与 W152 metadata-color-rewind 7/0（`P:500,505,515`；`C152:13`）。 |
| `/fork` | `caps.fork.run(anchor?)`；`core/sessions.ts:183-213` | **IMPLEMENTED · W152** | W145 anchor/title 持久化、W147 接线与 W152 capability 实测通过（`P:500,505`；`C152:15`）。 |
| `/btw` | `caps.sideQuery.run(...)`；`core/actions.ts:131-140` | **IMPLEMENTED · W152** | 同 `/recap`：真实隔离 runner、AbortSignal side.cancel、reset/dispose side.close 与 W152 side commands 均过（`P:509,534`；`C152:17`）。 |
| `/agentview` | `openAgentView` + backend catalog；`Chat.tsx:3601-3608` | **IMPLEMENTED · W152** | W152 session managers 8/0（`C152:19`；`E152:71`）。 |
| `/bg` | `channel.backgroundToAgentView()`；`Chat.tsx:3610-3616` | **IMPLEMENTED · W152** | 打开仍附着的 agent manager，不声称 detach（`C152:20`）。 |
| `/background` | 同 `/bg` | **IMPLEMENTED · W152** | 同 `/bg`（`C152:21`）。 |
| `/context` | `caps.context.usage('summary')`；`core/compose.ts:606-624` | **IMPLEMENTED · W152** | W141 summary + W149 context full + W155 正式接线已收口；W152 backend/ConPTY 均 OK。丰富 breakdown 无真值仍保持 absent，不由 summary 编造（`P:508-509,534`；`C152:22`）。 |
| `/init` | `caps.init.run()`；`core/compose.ts:507-515` | **IMPLEMENTED · W152** | W142 helpers + W150 workspace.init 原子不覆盖三态 + W159 正式 capability；W152 真实 OK，不再 absent（`P:511,531,535`；`C152:32`）。 |
| `/agents` | `caps.subagents.listChildren/listLines`；`core/compose.ts:1040-1045` | **IMPLEMENTED · W152** | W140 真实 tool.call/result 派生、W147 接线与 W152 capability 通过；仍无 roster/history/messaging，`interrupt=false`（`P:506`；`C152:33`）。 |
| `/jobs` | activity task events + `caps.tasks.readOutput`；`core/compose.ts:582-602` | **IMPLEMENTED · W152** | W140 jobs 真实 list/output/stop、W147 events 与 W152 capability 通过；W167 两尺寸 jobs 也 OK（`P:506`；`C152:35`；`E167:71`）。 |
| `/color` | `caps.color.set(color)`；`core/actions.ts:154-160` | **IMPLEMENTED · W152** | W142 helpers + W150 durable per-session accent/change frames + W159 正式 capability；W152 metadata/color OK，不再 absent（`P:511,531`；`C152:40`）。 |
| `/model` | `caps.models.list/current/set/display`；`core/actions.ts:56-73` | **IMPLEMENTED · W152** | W152 canonical `/model` 通过；W162 无 catalog 显式 env 回退与 W165 ephemeral 真实模型元数据已 scoped 绿，整包归 W163。字面 `/models` 不是别名（`C152:42`；`P:518,530`；`E167:67-70`）。 |
| `/effort` | `caps.effort.levels/current/set`；`core/actions.ts:75-118` | **IMPLEMENTED · W152** | W152 默认 effort 已复验绿；W160 修 catalog active 默认模型，W165 无 catalog 真实 effort max/xhigh 与 v4 selection 已 Lead 全仓绿，最终组合仍归 W163（`E152:52-54`；`P:516,530`）。 |
| `/login` | `caps.auth.status/login/reconnect/oauthProvider`；`core/compose.ts:517-545` | **IMPLEMENTED · W152** | W152 login/default-model 5/0；仅适配 API-key 当前模型，OAuth 仍 absent，不猜测环境/stored 来源（`C152:48`；`CTX:54-65`）。 |
| `/logout` | `caps.auth.logout`；`core/compose.ts:546-555` | **IMPLEMENTED · local/W152** | W152 按官方本地机制/fixture 判 OK，AgentSession auth 也 OK；这不证明 native `/logout` channel/core 接缝或真实凭据全流程已单独验收（`C152:49,76`；`CTX:56-65,92-99`）。 |
| `/mcp` | `caps.mcp.status/reconnect/toggle`；`core/compose.ts:631-652` | **IMPLEMENTED · W152** | W149 stdio initialize/tools 分页/reconnect/toggle 持久化 + W155 正式 servers/actions；W152 真实 OK，不再 MISSING/absent（`P:509,534`；`C152:52`）。 |
| `/home` | `openHome` + backend catalog/list；`core/compose.ts:803-825` | **IMPLEMENTED · W152** | W152 session managers 通过；restart 必须使用全新 home 的驱动纪律见 W161（`C152:60`；`P:517`）。 |

### 1.3 `dsh` 专属：14 条，当前均为 NOT-OFFICIAL

这些命令在上游存在，但 `localCommandRequirement` 只在 `input.dsh` 时提供。dsh-go 的 `native.kind='dsh-go'`，没有进程内 DSH registry/services。W152 已在真实 DSH-GO ConPTY 中对 **14/14** 验证“不可用提示 + 不进入模型 + 宿主零写”；该负向 OK 不改变 NOT-OFFICIAL 边界，也不新增 `commands.execute`（`E152:90-95`）。

| 命令 | 上游真实调用与证据 | 当前状态 | dsh-go 边界 |
|---|---|---|---|
| `/tree` | DSH session family tree + rewind/fork/adopt；`Chat.tsx:3672-3677` | **NOT-OFFICIAL** | 通用 fork/rewind 可独立适配，但不冒充 DSH tree/adopt。 |
| `/config` | `ctx.config` provider/server；`Chat.tsx:3842-3853` | **NOT-OFFICIAL** | G 扁平 UI 配置不是 DSH config service。 |
| `/reload` | `host.reload()` + services；`Chat.tsx:4233-4334` | **NOT-OFFICIAL** | 进程内 DSH composition reload。 |
| `/setup` | `host.setup()` wizard；`Chat.tsx:3822-3833` | **NOT-OFFICIAL** | G model catalog 不等于 DSH setupHost。 |
| `/migrate` | `host.migration.import()`；`Chat.tsx:3859-3911` | **NOT-OFFICIAL** | 外部 migration host。 |
| `/team` | `channel.team` + `host.team.invite/list`；`Chat.tsx:3940-3956` | **NOT-OFFICIAL** | jobs/workflow 不等于 Agent Team 契约。 |
| `/preset` | DSH presets + llm route/effort；`Chat.tsx:3143-3188` | **NOT-OFFICIAL** | permission/effort 不是 DSH 工具 preset。 |
| `/balance` | `host.llm.subscription()`；`Chat.tsx:3769-3776` | **NOT-OFFICIAL** | token usage 不是订阅余额。 |
| `/provider` | `channel.providerRoutes/switchRoute/setRateLimit`；`Chat.tsx:3518-3526` | **NOT-OFFICIAL** | G model catalog/provider routing 是独立能力。 |
| `/add-dir` | `host.workspaces.attach/detach`；`Chat.tsx:4195-4200` | **NOT-OFFICIAL** | G `workspace.list` 只是目录列表。 |
| `/hooks` | `host.workspaces.hooks()`；`Chat.tsx:4202-4207` | **NOT-OFFICIAL** | 无通用 external-backend hooks capability。 |
| `/skills` | `channel.skills` + DSH host commands；`Chat.tsx:3482-3516` | **NOT-OFFICIAL** | 模型工具列表不等于 DSH skills service。 |
| `/plugin` | `ctx.plugins` + install/remove；`Chat.tsx:3913-3938` | **NOT-OFFICIAL** | G additive plugin HTTP handler 不是 DSH Cordis registry。 |
| `/workspace` | DSH workspace registry/list/switch/runCommand；`Chat.tsx:3618-3650` | **NOT-OFFICIAL** | G `workspace.list` 不能替代多 workspace ledger。 |

### 1.4 capability snapshot 追加命令与线索

| 名称 | 官方接口 | 当前状态 | 当前证据与边界 |
|---|---|---|---|
| `/permission` | `caps.modes.list/current/set/cycle`；T `src/channel/capabilities.ts:63-67` | **IMPLEMENTED · W152** | W136/W144 modes 复用真实 permission API，W147 接线，W152 AgentSession modes OK；与逐次 approval 分开（`P:502,505`；`C152:87`）。 |
| `/goal` | `caps.goals.get/set/pause/resume/clear`；T `src/agent/capabilities.ts:346-359` | **IMPLEMENTED · W152** | W144 goal snapshot/pause/resume/change Transport/events、W147 接线与 W152 goals OK；不由 ACK 本地合成 change（`P:502,505`；`C152:82`）。 |
| `/channel` | Claude `channels.json` relay profile 管理 | **NOT-OFFICIAL** | Claude-specific surface；不能把 Go model catalog 改名冒充。 |
| `commands.list` | `caps.commands.list()`；T `src/agent/capabilities.ts:339-341` | **MISSING** | 仍无已验收的 backend-native skill catalog；W152 的 64 slash 命令矩阵不等于该 capability 已存在。 |
| `commands.execute` | **通用 AgentSession 没有此接口**；DSH registry 另有 `service.execute(...)` | **NOT-OFFICIAL** | 不新增同名私有 capability 冒充官方（`VIEW:41-57`）。 |
| slash `/view` | 固定上游 LOCAL_COMMANDS、hidden commands、SessionCapabilities 均无此命令 | **NOT-OFFICIAL** | `str_replace_editor.command='view'` 是工具子命令，不是 slash `/view`（`VIEW:27-70`）。 |

## 2. 非 slash 的 AgentSession / capability 矩阵

W152 的 AgentSession 能力/方法矩阵为 **29/0/0**，逐项见 `C152:72-104`。下表把后续修复与当前渲染/投影红单独列出，不用 29/0 掩盖 W168/W171。

| 接口 | 当前状态 | 当前真实实现、证据与缺口 | 下一道门 |
|---|---|---|---|
| `history()/subscribe()` | **IMPLEMENTED · W152** | W152 history/durable-replay OK；W171 已证明 inline 重复/缺失时 journal 每项仍全 1，问题不是 durable 丢失（`C152:103`；`P:526`）。 | W171/W172 验 inline replay/resize/restart 唯一性；W163 整包复验。 |
| `submit(...,'turn')` | **IMPLEMENTED · W152** | W152 普通 turn 与 open/create 通过；W164 找到的公开 idle 早于 raw done 竞态已由 W166 修复（`C152:98`；`P:528-529`）。 | 最新组合整包与旧 conformance 修正驱动复验。 |
| `submit(...,'followup')` | **IMPLEMENTED · ConPTY** | W152 placement 25/0；W169 忙碌 Tab 为真实 followup OK20/20，独立后续 turn（`C152:99`；`E169:18,51-57,118`）。 | 保持 FIFO、pending 与草稿清空回归。 |
| `submit(...,'steer')` | **IMPLEMENTED · ConPTY** | W152 steer v4 5/0；W169 忙碌 Enter 为真实 steer OK18/18，同 turn 下一 step（`C152:100`；`E169:19,59-65,122`）。 | W163 保留 W158 当前 turnId 与 W138 生产 steer 回归。 |
| `submit(...,'now')` | **IMPLEMENTED · 独立 wire** | W169 direct now OK21/21：明确 `placement:"now"`，旧轮 error、新 turn/done，TS `session.cancel=0`；独立 v4/transport 审计 OK21/0（`E169:24,77-92,127-134`）。 | 不得用 Ctrl+Enter 代替 direct now 证据；保留 stale cancel 防护。 |
| Ctrl+Enter UI 键位 | **IMPLEMENTED · ConPTY/W169** | 当前官方语义为 `session.cancel(cause=interrupt)` 后 `interruptAndDeliver` 重新投递 **followup**：turn-1 取消、turn-2 完成；OK21/21，无 Steer、无草稿粘尾。W158 旧“Ctrl+Enter=now”假设作废，当前无需产品修复（`E169:3-26,67-75`）。 | W169 已关闭；与 Tab=followup、Enter=steer、direct now wire 分开验收。 |
| queue / pending / retract | **IMPLEMENTED · W152** | W134 账本与 W152 placement/pending/retraction 通过；queue 仍是进程内存态，不承诺宿主重启恢复（`P:494,499`；`C152:99`）。 | 最新整包回归，不新增未实现的持久化承诺。 |
| `cancel(cause)` | **IMPLEMENTED · W152** | W152 cancel 与 now wire OK；W169 Ctrl+Enter cancel receipt 为 confirmed/turn-1/stillQueued=[]（`C152:102`；`E169:67-75`）。 | 保留 confirmed/unknown/failed 与身份映射。 |
| `permissions` | **IMPLEMENTED · W152** | W152 AgentSession permissions OK；approval 与 `/permission` modes 仍分开（`C152:90`）。 | W163 重跑允许/拒绝/取消等既定矩阵。 |
| `transcript.record/hasOlder/older` | **IMPLEMENTED · W152** | W145 durable oldest-first/cursor/A-B/restart + W147 接线 + W152 transcript OK。W168 只确认 transcript cursor 不在 export 输入链，未单独重验 cursor（`P:500`；`C152:97`；`E168:30,62`）。 | 当前每请求全量解码 v4，非增量 I/O；长会话归 W163。 |
| `images` | **IMPLEMENTED · W152** | W139 helpers、W147 正式 images.content/transcript 与 W152 images/durable/A-B 均 OK；W148 旧包拒绝图片只保留为历史（`P:504-505`；`C152:83`；`E148:19-25`）。 | 最新组合整包复验；不得倒填 W148。 |
| `commands` | **MISSING** | 无已验收的 backend-native list；官方通用 execute 不存在。 | 有真实 backend-native 命令语义后再实现 list。 |
| `questions` | **IMPLEMENTED · W152** | W141 secret question bridge、W147 正式 questions 与 W152 questions OK（`P:505,508`；`C152:91`）。 | 保持 secret 不入日志/事件。 |
| `pendingRetraction` | **IMPLEMENTED · W152** | W152 pendingRetraction OK，保持 clientMessageId↔turnId 与 cancel receipt 映射（`C152:89`）。 | 最新整包回归。 |
| `subagents` | **IMPLEMENTED · W152** | W140 真实生命周期派生、W147 接线与 W152 subagents OK；无独立 roster、child history、messaging，`interrupt=false`（`P:506`；`C152:95`）。 | 不伪造独立 child 控制面。 |
| `tasks` | **IMPLEMENTED · W152** | W140 jobs helpers、W147 events 与 W152 tasks OK；tool/result/turn 帧只触发刷新，不改名冒充 task journal（`P:506`；`C152:96`）。 | W167 jobs 已过；长时连续输出仍由整包/产品验收覆盖。 |
| `mcp` | **IMPLEMENTED · W152** | W149 三项真实宿主 API + W155 正式 backend/events/manifest + W152 mcp OK，不再 MISSING/absent（`P:509,534`；`C152:85`）。 | 最新组合整包复验 list/reconnect/toggle。 |
| `context` | **IMPLEMENTED · W152** | W141 summary、W149 full、W155 正式接线与 W152 context OK；无真值的丰富 breakdown 不编造（`P:508-509,534`；`C152:79`）。 | 最新整包回归 summary/full 的真实边界。 |
| `auth` | **IMPLEMENTED · W152** | W141 API-key helpers、W147 auth/questions 与 W152 auth OK；OAuth absent，native `/logout` 接缝不因 local/fixture OK 而宣称单独完成（`P:505,508`；`C152:76`）。 | 真实凭据流程与 logout 接缝按各自边界验收。 |
| `goals` | **IMPLEMENTED · W152** | W136/W144 + W147 + W152 goals OK（`P:502,505`；`C152:82`）。 | 最新整包 `/goal` 回归。 |
| `effort/modes` | **IMPLEMENTED · W152** | W136/W144/W147 + W152 effort/modes OK；W160/W165 又修默认与无 catalog 真实 effort，W165 Lead 全仓全绿（`C152:80,87`；`P:516,530`）。 | W163 验最新无 catalog 与 catalog 组合，不只验 scoped。 |
| `ViewSpec → tool presentation` | **IMPLEMENTED · W152** | W143 card/table/plain 映射、W147 events 与 W152 ViewSpec/raw 能力通过；不发明 slash `/view`（`P:505,517`；`C152:72-104`）。 | 最新整包真实 tool result 保真。 |
| raw history / step stream / TTFT | **IMPLEMENTED · W152** | W153 旧 step/TTFT/effort FAIL 已由 W157 修生产 sink，W158 修 steer turnId，W152 AgentSession 29/0；W166 修公开 finalizing 时机（`P:513,529,532-533`）。 | 当前 export 内容保留 FAIL 归 W168/W173，不反写成 durable/step sink 又缺失。 |
| export projection 行身份 | **FAIL · current** | W168 证明 projection 仅按 `(turn,step)` 跨 root turn 复用/覆盖 assistant 与 reasoning，reports 忠实导出已丢 rows；不是 Markdown filter、transcript cursor 或 W166 时机问题（`E168:34-55`）。 | W173 owner `401658c6` 修 rootTurn/v4Seq/run 身份，两尺寸全内容与 preDone/postDone 复验（`P:522`）。 |
| inline normal-buffer / replay / resize | **FAIL · current** | W167 有残字与 scrollback 重复/缺失；W171 当前审计进一步证明 journal 不重复、PID 更换后 userFrames 不增，指向 inline normal-buffer/replay 几何，非 durable（`E167:75-80`；`P:526`）。 | W171 owner `83d0aa3a` 完成审计；W172 owner `d12081a1` 修 renderer，两尺寸/resize/restart 复验（`P:526-527`）。 |
| `settings.form.* / models.catalog.* / plugins.*` | **PARTIAL** | G additive handlers 存在，不等于 requirement map 的通用 capability 已被正式 B 消费。 | 独立适配，不改 canonical 25，不冒充 DSH registry。 |

### `follow` 一词的歧义

G `serve_loop.go` 的 `follow` 是从 v4 日志持续读取并推 journal，不是 `AgentInput` 的 `followup` placement。判断排队只能看 submit/placement/pending.changed/stillQueued 与真实队列；不得用 `follow` 字样代替 placement 证据。

## 3. 当前证据、owner 与封版台账

| 工作项 | owner / 当前状态 | 真实证据与当前判定 | 未完成或下一步 |
|---|---|---|---|
| **W152** | `f68777e3` / **DONE · NO-GO** | OK645/FAIL5/UNVERIFIED0；64 命令 63/1、AgentSession 29/0；最终自检 31/0、owned dshd=0（`E152:3-9,162-168`；`P:515`）。 | 固定快照不许覆盖 GO.1；两个旧阻塞已由 W161/W162 后续处理，不能把旧 NO-GO 原样当当前根因。 |
| **W161 restart** | `5dbe2aa6` / **DONE · 无产品缺陷** | 原驱动假阳性；修正后原驱动 OK8/0、两轮 OK32/0、A-B OK16/0，Node391/391+构建全 0；Lead 全新 home OK8/0（`E161:3-27`；`P:517`）。 | 每轮唯一 home；W163 用修正驱动复验。 |
| **W162 无 catalog 启动** | `74734c87` / **DONE scoped · Lead 全绿** | load.model→显式 env→有效 catalog active；无 catalog baseline36/0、placement25/0、A-B16/0；Lead Node393/393+构建全 0（`P:518`）。 | 与 W165/W166 及后续修复组成新 tarball，整包归 W163。 |
| **W165 无 catalog 真实 effort** | `83a3d79b` / **DONE scoped · Lead 全仓全绿** | 仅 catalog 物理缺失且显式 env/--model 时从真实 `/v1/models` 形成 ephemeral 投影；A/B34/0、ConPTY14/0，effort 与 selection 生效（`P:530`）。 | 不创建 catalog；只读/set 不持久化，upsert/reorder 才持久化；整包归 W163。 |
| **W166 turn finalizing** | `9e842f55` / **DONE scoped · Lead 全绿** | v4 turn/end 暂存至 raw turn/done 或终态 turn/error；ab-race OK7/0、W158 38/0、W161 16/0；Lead Node397/397+构建全 0（`P:529`）。 | W164 的旧三条断言保持红，不能为变绿放宽；整包归 W163。 |
| **W164 conformance 审计** | `7fc2878f` / **DONE** | OK3/FAIL1；steer/text-identities/cancel.receipt 为旧断言或级联，唯一真实 A-shared-2 竞态交 W166；最终自检 OK19/0（`E164:3-16`；`P:528`）。 | 审计已关闭，不再写“四红未查”。 |
| **W167 窄终端/inline** | `2c22b0f2` / **DONE · NO-GO** | 固定 W160 副本 80=69/5、120=75/8，now wire16/0；基础 inline/prompt/tool/composer/Tab/Enter/model/context/effort/jobs/resume/restart/quit 均 OK（`E167:3-7,53-83`；`P:520`）。 | FAIL 分派 W168/W169/W170/W171/W172；后续结果逐项更新，不用旧副本反写当前。 |
| **W168 export projection** | `c591e0e3` / **DONE · 当前 FAIL 已归档** | 当前 W162+W166 树 80=25/8、120=26/7；assistant/reasoning 跨 root turn 丢失，minimal OK6/FAIL2（`E168:18-55`；`P:521`）。 | W173 修 projection；修复前 `/export` 不得标当前 PASS。 |
| **W169 三键/direct now** | `512dc88d` / **DONE · 当前全 OK** | final OK34/0；Tab20/0、Enter18/0、Ctrl+Enter21/0、direct now21/0、v4/transport21/0；生产零改（`E169:3-26,110-139`；`P:524`）。 | 已关闭，不再列为 W163 当前阻塞。 |
| **W170 直接输入碎片** | `27398757` / **DONE scoped · Lead 全绿** | patch `e0c7a267…`；真实双尺寸各 OK44/0，Lead reverse-check + Win32 + Node397/397 全绿；普通 Escape 1s/64B 不变（`P:525`）。 | 仅 scoped 绿，整包未验，仍列 W163 当前阻塞；8 个非输入门禁失败归其他 owner，不外推。 |
| **W171 inline 审计** | `83d0aa3a` / **IN_PROGRESS** | 120×36 已实证重复/缺失；journal 全 1、PID 更换、userFrames 不增，非 durable（`P:526`）。 | 完成 80/v4 审计并给 W172 精确复现。 |
| **W172 inline renderer** | `d12081a1` / **IN_PROGRESS** | 修 inline normal-buffer/replay/resize；保持最新 W162+W166，不改 parse-keypress/backend/events/Go（`P:527`）。 | 两尺寸、resize、restart 唯一性真实 ConPTY；未关闭。 |
| **W173 projection** | `401658c6` / **IN_PROGRESS** | 用 rootTurn/v4Seq/run 身份替代仅 `(turn,step)` 复用，保持真实顺序、不覆盖不重复（`P:522`）。 | minimal two-turns + W168 双尺寸全内容、preDone/postDone 与全量构建；未关闭。 |
| **W163 最终封版** | Lead / **BLOCKED(W168,W170,W171,W172,W173)** | W169 已关闭；W170 scoped 绿但整包未验；W162/W165/W166 已关闭，W167 已交付 NO-GO 并分派后续（`P:519-527`）。 | 新 dshd + 最新 tarball + 每轮唯一 home，重跑 W152 全套、W161 restart 与修正 conformance；最终只以 P 的 W163 为准。 |

## 4. W148 历史真实 ConPTY / wire 状态

以下只引用 W148 最终记录，不把后续结果倒填进旧包。W146 的早期 steer unsupported/Held 与独立 now 未验是更旧宿主快照；W148 已明确说明不得混用（`E148:26-33`）。本节是 hash 固定的旧包历史证据，不是当前 W152/W162/W166 包结果。

| 功能 | W148 历史真实状态 | W148 精确证据 |
|---|---|---|
| 普通 prompt | **PASS** | 4 轮真实 assistant `CURRENT_CONPTY_OK[_2/_3/_4]`，每轮回 idle（`E148:142-151`）。 |
| models | **PASS** | 真实 gatewayCount=6，selector 可见，切换 Kimi 且 override 落盘（`E148:128,136-141`）。 |
| new | **PASS** | `before=1 after=2`，返回新 session id（`E148:157-158`）。 |
| resume | **PASS** | browser 打开并回到原 session id，resume export 含真实内容（`E148:159-165`）。 |
| export | **PASS** | 真实 Markdown 与 User/Assistant 内容、SHA256 均记录（`E148:154-156`）。 |
| compact | **PASS** | 真实会话文件 hash 改变，compaction/start、summary、end 完整（`E148:166-169,377-395`）。 |
| followup | **PASS** | pending 可见、定向 retract、requeue、后续真实 assistant/export（`E148:238-252`）。 |
| steer | **PASS** | 生产 steer `unsupported="" held=false`，同 turn 下一 step 的 v4 顺序 5 项审计全过（`E148:245-254,318-340`）。 |
| now / Ctrl+Enter（历史口径） | **PASS · 但旧键位解释已纠正** | W148 记录 Ctrl+Enter 用户语义与独立 direct now wire 16/16，但把 Ctrl+Enter 当 now 的旧解释不再采用。W169 当前树证实 Ctrl+Enter=interrupt+followup，direct now 是另一条 wire；详见 §2。 |
| quit | **PASS** | baseline/placement 均 exitCode=0、cursor restore、无 fatal、自有进程清空（`E148:170-174,263-267`）。 |
| trace | **PASS · 基础** | 真实 turn/user/assistant 节点（`E148:152-153`）；不等于后续 W143/W152 全量 raw/step 证据。 |
| images | **UNVERIFIED** | helpers 已进包并有字节核验，但生产 backend 未声明 images，submit 仍返回 `image input is not connected`（`E148:19-25,119-123`）。 |

W148 最终行为证据为 `STEER_SUMMARY OK=81 FAIL=0 UNVERIFIED=1`，其中真实 ConPTY 是 baseline `35/0/1` + placement `25/0/0`，独立 now wire `16/0/0` 与 v4 audit `5/0/0` 另列，产物门禁 `16/0` 不重复计入行为总数（`E148:3-17`）。其 backend.js SHA256 为 `0b395bad1f79995f0f9ce8e997444c34b9995c45e5c0e88dcf3e24599de5b716`，宿主源/副本 SHA256 为 `fd87927fff221a8c040e954173d1adc195907da0265af43bbc72bc09840dfd9b`（`E148:35-47`）。收尾时宿主因 `internal/transport/source.go` 漂移得到 `FINAL_HOST_BIN_CHECK OK=1 FAIL=1`，TUI 源也新增 W142/W143 文件；所以不能把这轮结果外推成当前整树或稳定版全绿（`E148:26-33,397-439`）。

上述 images `UNVERIFIED` 只描述 **W148 旧包**。W147 后已接 images.content/transcript，W152 images/durable/A-B 已有真实 OK；既不能拿旧包拒绝覆盖当前实现，也不能把当前结果倒填成 W148 已通过（`C152:83`；`E148:19-25`）。

W148 记录的真实退出/汇总标记包括：`BASELINE_DRIVER_EXIT=0`、`PLACEMENT_DRIVER_EXIT=0`、`V4_AUDIT_EXIT=0`、`WIRE_DRIVER_EXIT=0`、`SESSION_INFO_EXIT=0`（`E148:221-223,314-316,338-340,373-375,394-395`）。编译实际命令为 `timeout 600 npm run compile`，记录 `NPM_COMPILE_EXIT=0`（`E148:68-86`）。最终宿主漂移复查保留 `OK=1 FAIL=1`，不得写成最终 bin-check 全绿（`E148:397-410`）。

## 5. 未验项与交接

- **W163 最新组合整包未验，GO.1 未封版。** 当前阻塞为 W168、W170、W171、W172、W173；W169 已关闭。W170 虽已真实双尺寸各 44/0 且 Lead reverse/Win32/Node397/397 全绿，仍只证明输入修复 scoped，不证明 W152 全套在最新 tarball 上通过。最终依赖、封版状态和 GO.1 只以 P 的 W163 为准（`P:519,524-527`）。
- **W152 的 645/5 与 63/1 不得改写成当前 645/0 或 64/0。** W161/W162/W165/W166 是后续证据；W163 必须用新 dshd、新 tarball、新 hash 和唯一 home 重跑。W152 的 NO-GO 记录、W161 的 restart 纠正和 W162/W165/W166 的 scoped 绿各自成立，不互相篡改（`E152:3-9`；`P:515,517-519,529-530`）。
- **当前 export 与 inline renderer 未收口。** W168 当前 projection FAIL 已归档并交 W173；W171 审计与 W172 修复仍在途。不能用 W152/W148 的旧 export PASS 或 W166 的 finalizing 修复抵销这些红（`E168:3-5,44-55`；`P:521-522,526-527`）。
- **W167 原始计数保留，但当前状态逐项判定。** W169 已关闭 Ctrl+Enter 项，W170 已修直接输入 scoped，W168/W171/W172/W173 尚未全部收口；`/models` 无别名不是 `/model` 回归，驱动假阳性也已单列（`E167:53-83,145-179`；`P:520-527`）。
- **W149/W150 及其后续接线已完成，不再列 absent。** MCP、sideQuery、context full、rename/color/workspace.init 已有 W149/W150 宿主、W155/W159 正式接线与 W152 真实 OK；但 `commands.list`、OAuth、DSH 专属 registry、真实 native logout 接缝等各自边界没有被这些结果自动填平（`P:509,511,531,534`；`C152:76-104`）。
- **W148 是历史快照。** 81/0/1、35+25 ConPTY、独立 now wire 16、v4 audit 5、旧 images UNVERIFIED、最终 bin-check OK1/FAIL1 和 hash 均保留；后续修复不改写旧记录，也不复用旧 hash 冒充当前包。
- **其他既有边界不变。** queue 不承诺宿主重启恢复；transcript 当前每请求全量解码 v4；subagents 无独立 roster/history/messaging/interrupt；完整工作日、整树性能、全仓 race 与所有终端/后端组合不因本文刷新而获得新结论。
- **本任务没有新增实机、全仓 Go 测试、race 或构建结论。** 本任务自身只要求读回、`doc-links` 与矩阵链接/证据指针检查；上述测试计数均来自已列证据与 Lead 记录。

**给 Lead 的当前口径：完整正式包基线采用 W152 `OK645/FAIL5 · NO-GO`、64 命令 63/1、AgentSession 29/0；restart 采用 W161 无产品缺陷与 Lead 全新 home OK8/0，无 catalog 启动/真实 effort/turn finalizing 分别采用 W162 Lead Node393/393+构建全 0、W165 Lead 全仓全绿、W166 Lead Node397/397+构建全 0，W164 审计已完成。后续 W167 固定副本为 80=69/5、120=75/8 NO-GO；W169 当前三键/direct now 全 OK 且已关闭，W170 输入修复真实双尺寸各 44/0、Lead reverse+Win32+Node397/397 全绿但整包未验，W168 当前 projection FAIL 交 W173，W171/W172 仍在审计/修复。W163 当前 `BLOCKED(W168,W170,W171,W172,W173)`，最终以 P 为准；64 命令基线不变，W148 历史快照不改写。**
