# R2 代理/团队/工具/CUA 域 rc.1→rc.2 差异

> 提取根（下称 **R2**）：`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`；**R1**：`tmp/GO8-RC2/official-rc1/dsh/node_modules/@deepseek-ai/`。
> rc.2 起官方**随包发了 `src/`（全量 TS 源码）+ `docs/`（全量架构文档，rc.1 无）**，本文以 `lib/*.js`（运行时真代码）为主证据、`src/*.ts` 交叉印证，`docs/*.md` 作为**权威文档**单独标记；行号均在提取根上核对过。
> 一次性口径：`R1` 有 `docs/` 吗 → 无（`diff -rq` 报 `Only in official-rc2/.../dsh: docs`）。
>
> **证据分级（本文每条结论都标注来源类别）**
> - **【文档】** = rc.2 随包发布的官方生成文档：`docs/tool-catalog.md`（工具 schema 全表）、`docs/config-catalog.md`（各包 config 逐字段）、`docs/upgrade-guide/<版本>/`（升级指南）。同一份内容在 `tmp/GO8-RC2/official-rc2-docs/…/dsh/docs/` 与我用的 `official-rc2/…/dsh/docs/` 两处都有，路径前缀下称 **DOCS**（= `tmp/GO8-RC2/official-rc2-docs/dsh/node_modules/@deepseek-ai/dsh/docs/`）。
> - **【源码】** = `R1/R2` 的 `lib/*.js` 或 `src/*.ts`，给 `file:line`。**rc.2 随包发布了全量 TypeScript 源码**（`src/` 的 `.ts` 文件数 ≥ `lib/` 的 `.js` 数，因为 `lib/` 是多模块打成一个 bundle）：`dsh-subagent` 22/25、`dsh-agent-team` 10/13、`dsh-tools` 10/13、`dsh-cua` 25/13、`dsh-office-kit` 8/11、`dsh-tool-office` 3/1。本文主证据用 `lib/*.js`（运行时真实代码，且行号在提取根上可复核），关键契约用 `src/*.ts` 交叉印证。
> - **【未取证】** = 拿不到证据的地方，明确标出，不猜。
>
> **升级指南目录名不能当"增量过滤器"用（实测反例，方法论警告）**：`upgrade-guide/v0.2.1-rc.2/agent-team-subagents` 里列了 `wait_agent` 的 `noProgress` 移除，但 `R1` 源码里 `noProgress` **本来就不存在**（`grep -c noProgress R1 dsh-tool-subagent-control` = 0）→ 该条早于 rc.1，不属于本次增量；反过来 `v0.2.2-alpha.3/subagent-names` 列的是"按名字寻址"，`R1` 无 `names.js`、`R2` 有 → 虽归档在 alpha.3 目录，**确实是 rc.1→rc.2 的增量**。所以本文对每条指南声明都**回查了 rc.1 与 rc.2 源码**，只把"rc.1 无、rc.2 有"的写进增量。

## 0. 结论摘要（≤10 行）

1. **工具面净 +7 / 改名 3 组**：新增 `team_task_create|list|get|update`（4）与 `office_convert|recalculate|render`（3）；控制面参数 `agent_id`（会话 id）**全部改名 `agent`**，且**新 `agent` 同时接受名字或 Session id**、**再传 `agent_id` 直接参数校验失败**（【文档】`DOCS/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:11-12`；【源码】`R2 dsh-tool-subagent-control/lib/index.js:228-236,285-292`）；`list_agents` 删 `scope` 参数、行格式改为 `{name,status,description?}`，文本行 `<name> [<status>] — <description>`；`subagent`/`subagent_fork`/`subagent_oar` 新增可选 `name`，收据 `<started subagent <name>>` + 结果 `{kind:'activation', name}`。
2. **AgentTeam 是重写，不是搬家**：rc.1 `dsh-experimental-agent-team` 是**名册制**（`TeamRoster` + `maxMembers=16` + `TEAM_MEMBER_LIMIT` + disposal 超时）；rc.2 `dsh-agent-team` 只剩**任务板**，且**显式拒绝**旧配键 `maxMembers/maxMessageBytes/disposalTimeoutMs`（`R2 dsh-agent-team/lib/types/index.js:47,81`）——"队友就是子代理，用子代理的上限与 inbox 投递"。
3. **rc.2 Team 只剩 4 个工具**（rc.1 实验包是 10 个：`spawn_teammate/send_message/list_agents/wait_agent/interrupt_agent` + 4 task）；被删的 6 个**收编进标准子代理工具**，其中 `interrupt_agent` 变成**内置默认关闭**（`Config.interrupt`；【源码】base `cordis.patch.yml:389` + Web `standard:98`/`cordis:94`/`ptc:95` 三 preset 全设 `false`，【文档】`DOCS/upgrade-guide/v0.2.2-rc.1/subagent-interrupt-default/guide.md:7-9`）。
4. **子代理从"父授权"转为"Gateway 按目标会话授权"**：`subagents.interruptByParent(child,parent,mode)` → `subagents.interruptByUser(child)`；`subagents/prompt` 请求体删 `parentSessionId` 与 `mode:"continuable"` 两个必填字段。【文档】`DOCS/upgrade-guide/v0.2.1-rc.2/subagent-user-controls/guide.md`（"address only the target Session, rename the interrupt endpoint, require target Session permissions"）。
5. **具名协作是 rc.2 新数据模型**：`ROOT_AGENT_NAME="lead"`、名字正则 `^[a-z][a-z0-9-]{0,31}$`、成员全名 = 从根起的 `/` 路径（`reviewer/checker`），新文件 `lib/types/names.js`（R1 是 `internal.js`，已删）。**并且 `subagent/catalog` 事件载荷升到 `version: 3`**：旧版读不了含 v3 的会话（【文档】同 subagent-names guide 第 5/6 条 + 持久化确认书 `persistence-changes/2026-11-02-subagent-catalog-names.md`；【源码】`R2 dsh-subagent/src/catalog.ts:21,40`，catalog 投影 `stateVersion: 6`（`:138`））。
6. **并行规则：分类器一字未改，声明面扩大**。判据仍是 `tool.isConcurrencySafe(args) === true` 才并行、否则独占（`R2 dsh-tools/lib/pipeline.js:286-289`，与 R1 逐字相同）；新声明并行的有 `glob/grep`、`job_list`、`schedule_list/view`、`cordis_inspect_*`、`team_task_list/get`、`mcp` 三个资源工具、`office_render`；`maxParallelToolCalls` 默认仍 **10**。
7. **审批/错误信封有实质变化**：审批拒绝不再是裸字符串，而是 `error.info{name:"ApprovalError", code:APPROVAL_REJECTED|APPROVAL_CANCELLED|APPROVAL_UNAVAILABLE}`；新增工具上下文错误码 `TOOL_CONTEXT_UNAVAILABLE`；被取消的 body 抛出物统一补 `AbortError`+`TOOL_ABORTED` 身份（`R2 dsh-tools/lib/pipeline.js:612-660,841-856`）。
8. **CUA 9 方法齐备但 Windows 无 provider**：`cuaAccess.*` 全契约带 **win32 分支**，但 `dsh-cua-bundle` 只挂 `dsh-experimental-cua-macos`（`disabled: process.platform !== 'darwin'`），因此 Windows 上 `cua` 提供者为空；helper 是**平台原生二进制**（需构建/预编译）。
9. **Office 换代已完成**：`officeToPdf.generation/render`（2 方法）→ `officeKit.generation/render`（2 方法，render 多 `priority` 参数），并新增 3 个模型工具；引擎是 `@deepseek-ai/libreoffice-kit@0.2.3` + 预编译 `libreoffice-kit-win32-x64`（**本机随包就有**）。
10. **`dsh-error` 不含新错误码**：它只是把 `HarnessError` 从 `dsh-llm` 抽成独立包（`R2 dsh-error/lib/index.js:12-22`），`dsh-llm/lib/index.js:8` 改为从它 import 并原样 re-export；四字段契约（`code/message/details/hint`）**不受影响**，受影响的是工具结果的 `error.info` 新字段。

## 1. 工具面总表（rc.2 全量模型可见工具）

**先说明"审批"列的判据（避免误读）**：官方**没有 per-tool 的审批布尔值**。审批由 `dsh-user-approval` 的 `policy` + `dsh-permission-presets` 的**模式级**配置决定（`R2 dsh-base/cordis.patch.yml:259-277`：`read-only→ask`、`workspace-write→ask`、`danger-full-access→never`），再经流水线 `ctx.approval` 一次性询问（`R2 dsh-tools/lib/pipeline.js:612-660`）。下表"审批"列 = **据 `docs/tool-catalog.md` 的 "Writes / affects" 列推出的"会不会在 ask 模式触发审批"**，标 † 者为推断而非官方逐字。

装载口径：`dsh-base`（所有 profile 共享）+ `dsh-web-app/presets/{standard,ptc,cordis}` + `dsh-sdk-app`。表中"装载"列标 ⓑ=base、ⓟ=standard/ptc preset、ⓢ=sdk-app、ⓧ=仅实验 bundle（默认不装）。

### 1.1 全量工具（rc.2）

| # | 工具名 | 用途（一句话） | 审批 | rc.1 有? | 装载 | 证据（R2 相对根） |
|---|---|---|---|---|---|---|
| 1 | `plugin_manager` | 安装/卸载/配置插件包 | 是(写盘)† | 是 | ⓑ | `DOCS/tool-catalog.md:60` |
| 2 | `plugin_market` | 插件市场检索与安装 | 是(写盘+网络)† | 是 | ⓟ(disabled) | `DOCS/tool-catalog.md:130` |
| 3 | `list_mcp_resource_templates` | 列 MCP 资源模板 | 否 | 是 | ⓑ | `DOCS/tool-catalog.md:180`、`dsh-mcp-resources/lib/index.js:60` |
| 4 | `list_mcp_resources` | 列 MCP 资源 | 否 | 是 | ⓑ | `DOCS/tool-catalog.md:205` |
| 5 | `read_mcp_resource` | 读一个 MCP 资源 | 否 | 是 | ⓑ | `DOCS/tool-catalog.md:230` |
| 6 | `stagehand_act` | 浏览器动作（实验 stagehand） | 是(网络+副作用)† | 是 | ⓧ | `DOCS/tool-catalog.md:260` |
| 7 | `stagehand_extract` | 浏览器抽取 | 是(网络)† | 是 | ⓧ | `:287` |
| 8 | `stagehand_navigate` | 浏览器导航 | 是(网络)† | 是 | ⓧ | `:356` |
| 9 | `stagehand_observe` | 观察页面 | 是(网络)† | 是 | ⓧ | `:383` |
| 10 | `stagehand_screenshot` | 截图 | 是(网络)† | 是 | ⓧ | `:410` |
| 11 | `stagehand_tabs` | 标签页管理 | 是(网络)† | 是 | ⓧ | `:437` |
| 12 | `propose_action` | 提交一张动作卡（要人点同意） | 是(会话副作用)† | 是 | ⓧ | `DOCS/tool-catalog.md:507` |
| 13 | `ask_user_question` | 问用户（阻塞/定时两模） | 否(但有 Stop) | 是 | ⓟ | `DOCS/tool-catalog.md:550`、`dsh-tool-ask-user/lib/index.js:66,223` |
| 14 | `run_code` | PTC：写一段程序在运行时里调工具 | 是(执行)† | 是 | ⓑ | `DOCS/tool-catalog.md:624`、`dsh-tool-executor-ptc/lib/index.js:285` |
| 15 | `exit_plan_mode` | 提交计划等批准 | 否 | 是 | ⓟ | `DOCS/tool-catalog.md:672` |
| 16 | `bash` | 一次性 bash（POSIX） | 是(执行)† | 是 | ⓑ(非 win32) | `DOCS/tool-catalog.md:699` |
| 17 | **`office_convert`** | Office→pdf/docx/odt/txt/xlsx/ods/csv/pptx/odp | 是(写临时文件)† | **否** | ⓟ+ⓢ | `DOCS/tool-catalog.md:743`、`dsh-tool-office/lib/index.js:455,459` |
| 18 | **`office_recalculate`** | 重算表格公式，输出 xlsx/ods | 是(写临时文件)† | **否** | ⓟ+ⓢ | `DOCS/tool-catalog.md:784`、`dsh-tool-office/lib/index.js:474,478` |
| 19 | **`office_render`** | 把文档/幻灯片/表区渲染成图片看 | 是(读+生成附件)† | **否** | ⓟ+ⓢ | `DOCS/tool-catalog.md:814`、`dsh-tool-office/lib/index.js:402,440` |
| 20 | `present` | 把文件登记为交付物 | 是(交付面)† | 是 | ⓟ | `DOCS/tool-catalog.md:860` |
| 21 | `pwsh` | 一次性 PowerShell（Windows） | 是(执行)† | 是 | ⓑ(win32) | `DOCS/tool-catalog.md:904` |
| 22 | `cordis_inspect_list` | 列运行时插件/服务 | 否 | 是 | ⓧ(creator) | `DOCS/tool-catalog.md:948`、`dsh-tool-cordis/lib/index.js:50` |
| 23 | `cordis_inspect_query` | 查一个客户端/服务状态 | 否 | 是 | ⓧ | `DOCS/tool-catalog.md:961`、`dsh-tool-cordis/lib/index.js:88` |
| 24 | `bash`(persistent) | 常驻 PTY bash | 是(执行)† | 是 | ⓧ | `DOCS/tool-catalog.md:1005` |
| 25 | `pwsh`(persistent) | 常驻 PTY pwsh | 是(执行)† | 是 | ⓧ | `DOCS/tool-catalog.md:1032` |
| 26 | `str_replace_editor` | view/create/唯一替换/插行 | 是(写盘)† | 是 | ⓧ | `DOCS/tool-catalog.md:1059` |
| 27 | `apply_patch` | 打补丁 | 是(写盘)† | 是 | ⓑ(开关) | `DOCS/tool-catalog.md:1165`、`dsh-base/cordis.patch.yml:297-300` |
| 28 | `edit` | 定点改文件 | 是(写盘)† | 是 | ⓑ | `DOCS/tool-catalog.md:1186` |
| 29 | `read` | 读文本文件 | 否 | 是 | ⓑ | `DOCS/tool-catalog.md:1221`、`dsh-tool-fs/lib/index.js:254`、`:338`(并发安全) |
| 30 | `read_image` | 读图片给模型看 | 否 | 是 | ⓑ | `DOCS/tool-catalog.md:1250`、`dsh-tool-fs/lib/index.js:1838`(并发安全) |
| 31 | `write` | 写整个文件 | 是(写盘)† | 是 | ⓑ | `DOCS/tool-catalog.md:1271` |
| 32 | `glob` | 按路径模式找文件（**打包 ripgrep**） | 否 | 是 | ⓑ | `DOCS/tool-catalog.md:1303`、`dsh-tool-fs-search/lib/index.js:765,780`(并发安全) |
| 33 | `grep` | 按内容找文件（**打包 ripgrep**） | 否 | 是 | ⓑ | `DOCS/tool-catalog.md:1328`、`dsh-tool-fs-search/lib/index.js:1076,1095`(并发安全) |
| 34-39 | `terminal_close/list/open/read/send/signal` | 6 个终端工具 | 是(执行)† | 是 | ⓧ(opt-in) | `DOCS/tool-catalog.md:1363-1489` |
| 40 | `user_terminal_list` | 列**用户控制**的终端 | 否 | **否** | ⓟ(新包) | `DOCS/tool-catalog.md:1528`（`dsh-terminal-panel`，新包，非本域深挖） |
| 41 | `user_terminal_read` | 读用户终端输出 | 否 | **否** | ⓟ(新包) | `DOCS/tool-catalog.md:1541` |
| 42 | `create_goal` | 建目标 | 是(会话事件)† | 是 | ⓟ | `DOCS/tool-catalog.md:1572` |
| 43 | `get_goal` | 读目标 | 否 | 是 | ⓟ | `DOCS/tool-catalog.md:1597` |
| 44 | `update_goal` | 改/暂停/续/完成目标 | 是(会话事件)† | 是 | ⓟ | `DOCS/tool-catalog.md:1610` |
| 45-48 | `schedule_create/edit/list/view` | 定时任务 CRUD | 是(写盘，list/view 否)† | 是 | ⓟ | `DOCS/tool-catalog.md:1666-1792`、`dsh-tool-schedule/lib/index.js:581,606`(并发安全) |
| 49 | `lsp` | 语言服务器查询 | 否 | **否** | ⓧ | `DOCS/tool-catalog.md:1819`（`dsh-tool-lsp` 新包，不属本域） |
| 50 | `ralph` | 固定工作流多轮打磨 | 是(执行)† | 是 | ⓧ | `DOCS/tool-catalog.md:1867` |
| 51 | `skill` | 加载技能说明 | 否 | 是 | ⓟ | `DOCS/tool-catalog.md:1898` |
| 52-56 | `session_event_read/search/trace`、`session_search/trace` | 5 个只读会话检索 | 否 | 是 | ⓧ | `DOCS/tool-catalog.md:1923-2134`、`dsh-tool-session-query/lib/index.js:977,993,1017` |
| 57 | `subagent_oar` | 外部 provider 委派（oar 别名） | 是(执行)† | 是 | ⓧ | `DOCS/tool-catalog.md:2158` |
| 58 | `list_subagent_models` | 列子代理可用路由 | 否 | 是 | ⓟ | `DOCS/tool-catalog.md:2207`、`dsh-tool-subagent/lib/index.js:228` |
| 59 | `subagent` | 派一个独立子代理，返回**名字** | 是(执行)† | 是 | ⓟ | `DOCS/tool-catalog.md:2229`、`dsh-tool-subagent/lib/index.js:432`(并发安全，rc.1 `:430` 同) |
| 59b | `subagent_fork` | 同上的 fork 变体（继承上下文） | 是(执行)† | 是 | ⓟ | `R2 dsh-base/cordis.patch.yml:401-407`、`dsh-web-app/presets/standard.patch.yml:104-107` |
| 60 | `interrupt_agent` | 让子代理停当前工作（**内置配置默认不注册**） | 否 | 是 | ⓑ+ⓟ(**关**) | `DOCS/tool-catalog.md:2269`、`dsh-tool-subagent-control/lib/index.js:211,218,283`；【文档】`DOCS/upgrade-guide/v0.2.2-rc.1/subagent-interrupt-default/guide.md:7-9`；【源码】`dsh-base/cordis.patch.yml:389`、`dsh-web-app/presets/{standard:98,cordis:94,ptc:95}.patch.yml` 都写 `interrupt: false` |
| 61 | `list_agents` | 列整棵子代理树（名字/描述/状态） | 否 | 是 | ⓟ | `DOCS/tool-catalog.md:2290`、`dsh-tool-subagent-control/lib/index.js:215`(并发安全) |
| 62 | `send_message` | 给树内某个 agent 发消息 | 否 | 是 | ⓟ | `DOCS/tool-catalog.md:2303` |
| 63 | `wait_agent` | 等 inbox/完成通知/用户插话 | 否 | 是 | ⓟ | `DOCS/tool-catalog.md:2329` |
| 64-66 | `job_kill/job_list/job_output` | 后台任务控制 | 是(kill)/否 | 是 | ⓑ | `DOCS/tool-catalog.md:2353-2391`、`dsh-tool-jobs/lib/index.js:470`(job_list 并发安全) |
| 67 | **`team_task_create`** | 在共享任务板建一条未认领任务 | 是(会话事件)† | **否** | ⓑ | `DOCS/tool-catalog.md:2426`、`dsh-tool-agent-team/lib/index.js:144` |
| 68 | **`team_task_get`** | 读一条任务最新值 | 否 | **否** | ⓑ | `DOCS/tool-catalog.md:2466`、`dsh-tool-agent-team/lib/index.js:231`(并发安全) |
| 69 | **`team_task_list`** | 列任务（含就绪/阻塞/写域告警） | 否 | **否** | ⓑ | `DOCS/tool-catalog.md:2487`、`dsh-tool-agent-team/lib/index.js:180`(并发安全) |
| 70 | **`team_task_update`** | CAS 改一条任务（8 种动作） | 是(会话事件)† | **否** | ⓑ | `DOCS/tool-catalog.md:2530`、`dsh-tool-agent-team/lib/index.js:248` |
| 71 | `todo_write` | 写待办清单 | 是(会话事件)† | 是 | ⓟ | `DOCS/tool-catalog.md:2603` |
| 72 | `working_directory` | 读/改会话目录 | 是(会话态)† | 是 | ⓑ | `DOCS/tool-catalog.md:2653` |
| 73 | `create_worktree` | 建分支+工作树并切目录 | 是(写盘+git)† | 是 | ⓧ | `DOCS/tool-catalog.md:2677` |
| 74 | `workflow` | JS 工作流编排多子代理 | 是(执行)† | 是 | ⓟ | `DOCS/tool-catalog.md:2705` |
| 75 | `load_workspace_dependencies` | 报内置 Python/Node 路径 | 否 | 是 | ⓢ/ⓟ(条件) | `DOCS/tool-catalog.md:2800` |
| 76 | `web_fetch` | 取 URL 正文 | 是(网络)† | 是 | ⓟ | `DOCS/tool-catalog.md:2817`、`dsh-tool-web/lib/index.js:334`(并发安全) |
| 77 | `web_search` | 搜索 | 是(网络)† | 是 | ⓟ | `DOCS/tool-catalog.md:2838`、`dsh-tool-web/lib/index.js:832`(并发安全) |

**rc.1 有、rc.2 没有的工具**：`spawn_teammate`（`R1 dsh-experimental-tool-agent-team/lib/index.js:251`；rc.2 由 `subagent` 取代）。其余 rc.1 工具名 rc.2 全在（逐名核对 `R1/R2` 的 `defineTool({name:"…"})` 集合，R2 是 R1 的超集）。

**参数/schema 字段级增删（只列有变化的）**：

| 工具 | 变化 | rc.1 形状 | rc.2 形状 | 证据 |
|---|---|---|---|---|
| `send_message` | 改名+语义 | `agent_id`(会话 id，"direct continuable child 或 direct parent") | `agent`(**名字或 Session id**，"your subagent tree 内任意 agent")；传 `agent_id` → 参数校验失败【文档】 | `R1 dsh-tool-subagent-control/lib/index.js:161-166` vs `R2 …:228-236`；`DOCS/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:11` |
| `interrupt_agent` | 改名 | `agent_id`(必填) | `agent`(名字或 Session id，必填) | `R1 …:195-200` vs `R2 …:285-292` |
| `interrupt_agent` | 内置默认关 | 恒注册 | `Config{interrupt: default true}`；**base + Web standard/cordis/ptc 三 preset 都设 false** → 只有显式改回才可见 | 【文档】`DOCS/upgrade-guide/v0.2.2-rc.1/subagent-interrupt-default/guide.md:7-9`；【源码】`R2 dsh-base/cordis.patch.yml:389`、`R2 dsh-web-app/presets/standard.patch.yml:98`、`cordis.patch.yml:94`、`ptc.patch.yml:95`（R1 三个 preset **无** `interrupt` 键） |
| `list_agents` | 删参数+换行格式 | `scope: children|descendants`；行 `{kind,id,label,parent?,depth?,status}` | 无参数（整树）；行 `{name,status,description?}`；文本行 `<name> [<status>] — <description>`；`status` 可为 `corrupt/unsupported/unavailable` | `R1 …:47-53,114` vs 【文档】`DOCS/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:13`；内部 seam 仍是 `kind:'child'|'diagnostic'`（【源码】`R2 dsh-subagent/src/list-children.ts:110-121`） |
| `subagent`/`subagent_fork`/`subagent_oar` | 加 `name` | 无 `name` | 可选 `name`；收据 `started subagent <name>`；结果 `{kind:'activation', name}`；历史子代理保留 Session id 作名字 | 【文档】`DOCS/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:12`；【源码】`R2 dsh-tool-subagent/lib/index.js:425-432`（`value.name` 进 `presentationMeta`） |
| `wait_agent` | 参数未变（`timeout_ms` 10000..3600000，默认 30000） | 同 | 同；语义："任务板变更**不**结束等待，只有 inbox 输入才结束" | 【文档】`DOCS/tool-catalog.md:2329-2350`；"noProgress 移除"属 rc.1 之前的变更（`R1` 已无 `noProgress`，**不计入本次增量**） |
| `subagent` | 宿主 ↔ 官方字段差异 | 宿主：`cwd/description/id/prompt/continuable`（`id`=具名标签、`continuable`=留可续子会话） | 官方：`cwd/description/name/prompt`（必填 `description,prompt`）；**没有** `id`、**没有** `continuable`（可续是默认形态） | 【文档】`DOCS/tool-catalog.md:2229-2266`；宿主 `modules/tool/subagent/subagent.go:48-62` |
| `subagent_fork` | 路由字段 | — | 显式**不启用** route 字段（为 KV cache 复用） | `R2 dsh-base/cordis.patch.yml:401-407` |
| `glob`/`grep` | 并发语义 | 无 `isConcurrencySafe` | `isConcurrencySafe:()=>true` | `R1 dsh-tool-fs-search/lib/index.js`（无此键） vs `R2 …:780,1095` |
| `office_*` | 全新 | 无 | 3 个工具（见 §1.1 #17-19） | `R2 dsh-tool-office/lib/index.js:402,455,474` |
| `team_task_*` | 全新 | 无（rc.1 只有实验包里的同名工具，默认不装） | 4 个工具（见 §3.2） | `R2 dsh-tool-agent-team/lib/index.js:144-314` |
| `read_image` | 并发语义 | `isConcurrencySafe:()=>true` | 同 | `R1/R2 dsh-tool-fs/lib/index.js:337/338` |

## 2. 逐包差异表

`改动文件数` = `diff -rq` 行数（含 README/package.json/src 目录新增，故比"逻辑改动"多；括注逻辑要点）。

| 包 | 改动文件数 | 要点 | 证据（R2 相对根，除注明 R1） |
|---|---|---|---|
| **dsh-agent-team**（新包） | 新（rc.1 无） | 任务板服务：`agentTeams` 单方法 `setEnabled`；配置只有 `maxTasks:default 256` 与 `defaultEnabled:default false`；**显式拒绝** `maxMembers/maxMessageBytes/disposalTimeoutMs` | 【源码】`dsh-agent-team/lib/types/index.js:47,68-72,81,127`、`lib/typert.host.js:20`；【文档】`DOCS/config-catalog.md:175-192` |
| **dsh-tool-agent-team**（新包） | 新 | 4 个 `team_task_*` 工具 + `team:policy` 系统提示段；`Config = {}`，拒绝旧键 `interrupt/freshProvider/forkProvider` | 【源码】`dsh-tool-agent-team/lib/index.js:25-31,34-46,144,180,231,248`；【文档】`DOCS/upgrade-guide/v0.2.1-rc.2/agent-team-subagents/guide.md:14-17`、`DOCS/tool-catalog.md:2424-2600` |
| dsh-experimental-agent-team（删） | - | rc.1 名册制：`TeamRoster`、`maxMembers=16`、`TEAM_MEMBER_LIMIT`、`TeamLifecycle`+`disposalTimeoutMs` | `R1 dsh-experimental-agent-team/lib/index.js:1423,845-856,1027,176-253` |
| dsh-experimental-tool-agent-team（删） | - | rc.1 10 个工具（含 `spawn_teammate`） | `R1 dsh-experimental-tool-agent-team/lib/index.js:251,293,321,330,352,365,400,444,457` |
| **dsh-subagent** | 23 | 新增名字体系 `names.js`（`lead`、路径、同名预订）；`internal.js` 删除；`continuation-messages.js` 全面改写（"结果自动送达"取代"必须 send_message 回报"、通知带 `childName` 与 `stopReason`）；`manager.js` 从 `lineage` 改为 `root` 视角 + `waitForTreeWork`；`subagent/catalog` 载荷升 **version 3**（投影 `stateVersion:6`）；`maxDepth` 默认仍 1、`maxActiveSubagents` 默认仍 **8**（两项 rc.1 已存在，非新增） | 【源码】`R2 dsh-subagent/lib/types/names.js:10,12,18,27,38`、`src/catalog.ts:21,40,138`、`lib/types/continuation-messages.js:26-43,79-118`、`lib/index.js:2962-2963`、`R1 lib/index.js:2992-2994`(rc.1 同两项配置)；【文档】`DOCS/config-catalog.md:3715-3722` |
| **dsh-tools** | 13 | 新增 `ToolContextUnavailableError`(`TOOL_CONTEXT_UNAVAILABLE`)；新增 `error-codes.js`（`APPROVAL_ERROR_NAME`/`APPROVAL_REJECTED/CANCELLED/UNAVAILABLE`）；审批拒绝带 `info{name,code}`；取消后无身份抛出物补 `AbortError`+`TOOL_ABORTED`；**并发分类逻辑与 rc.1 逐字相同** | `dsh-tools/lib/types/contracts.js:42,52`；`lib/types/error-codes.js:6-16`；`lib/pipeline.js:612-660,841-856,286-289` |
| **dsh-tool-executor-ptc** | 11 | 删 `PtcCalls`（按 sandbox 模式切换串行化的旧机制）；新增按超时折算的调用配额 `ToolCallLimitExceededError`（`maxSubCallsPerMinute`）与 `CodeResetFailedError`(`CODE_RESET_FAILED`) | `lib/index.js:265-300`(createRunCodeTool 新签名)、`lib/types/errors.js`、`lib/types/config.js` |
| dsh-tool-executor-native | 2 | 仅 package.json 版本/依赖 | — |
| dsh-tool-fs | 6 | **参数校验改用 `ToolArgsError`**（rc.1 一律 `new Error(...)`）；改从 `dsh-error` 取 `HarnessError`；`read` 的提示文案扩为"cat, sed, head, or tail"；`apply_patch` 描述去掉 "Codex-style" 字样。`read` 并发安全声明 rc.1/rc.2 都在（R1 `:337` / R2 `:338`） | `R2 dsh-tool-fs/lib/index.js:2,221-232,249,516,1383,1426`；`ToolArgsError` 定义在 `R2 dsh-tools/lib/types/schema.js:250`（rc.1 已存在，非新增类型） |
| **dsh-tool-fs-search** | 6 | 从 `@vscode/ripgrep` 直连改为经 `@deepseek-ai/dsh-util-ripgrep`（`resolveRgPath`/`spawnRipgrep`）；**新增并发安全** | R1 `dsh-tool-fs-search/package.json`(deps `@vscode/ripgrep`) vs R2(дeps `dsh-util-ripgrep`)；`R2 lib/index.js:6,765,780,1076,1095` |
| **dsh-util-ripgrep**（新包） | 新 | 统一打包 ripgrep 的定位与 spawn（argv 直传，无 shell） | `dsh-util-ripgrep/`（`R2 dsh-tool-fs-search/lib/index.js:6` import） |
| dsh-tool-bash / dsh-tool-pwsh / dsh-tool-str-replace-editor / dsh-tool-present / dsh-tool-goal / dsh-tool-todo / dsh-repeat-tool-reminder / dsh-tool-terminal / dsh-tool-ask-user / dsh-tool-call-timeout-policy / dsh-tool-workflow / dsh-workflow / dsh-tool-executor-native | 各 2-7 | 逻辑基本未变（多数只有 README/package.json/src 目录）；`dsh-repeat-tool-reminder` 有 lib 改动（提醒文案/节流） | `diff -rq` 逐包输出见 `tmp/GO8-RC2/diff-rq.txt` |
| dsh-workflow-ptc | 3 | lib/index.js 有小改（错误身份对齐） | — |
| **dsh-agent** | 7 | `lib/index.js`+`lib/types/index.js` 改动：Agent 身份/选项面（`subagentDepth` 相关）微调 | `dsh-agent/lib/index.js` |
| **dsh-agent-loop** | 6 | ①运行上下文改为 `<system-reminder>` 包裹 + 段落级 `deepEqualJson` 比较 + 后续快照声明"取代早前版本（含压缩摘要里的）"并转义 `</system-reminder>`；②`cancel()` 用 try/finally 保证 abort 一定发生；③`wakeDriver()` 新增 `lifetimeSignal.aborted` 早退；④dispose 路径对 `machine.cancel` 包 try/catch 收集失败；⑤`maxParallelToolCalls` 默认仍 10 | `dsh-agent-loop/lib/index.js:204-215,323-350,586-593,628,632-634,1496-1502,1322` |
| dsh-subagent-spawn-in-process / dsh-subagent-fork-in-process | 各 5 | 只有 README/package.json/src（逻辑未变） | `diff -rq` |
| **dsh-cua**（新包） | 新 | 9 个 `cuaAccess.*` 方法 + 完整 native 协议/错误码/配置面；provider 抽象在 `dsh-computer-use`；**rc.2 另有 3 项行为变更**（窗口化输入/能力协商、AX 读上限 5000/50、悬停默认 AX-only + `HOVER_UNAVAILABLE`） | 【源码】`dsh-cua/lib/typert.host.js:125-347`、`lib/config.js:5-35`；【文档】`DOCS/upgrade-guide/v0.2.2-rc.1/{cua-native-platform,cua-ax-read-limits,cua-hover-ax-only}/guide.md` |
| **dsh-cua-bundle**（新包） | 新 | 只装 `dsh-computer-use`(registry) + `dsh-experimental-cua-macos`(**darwin only**) + `dsh-cua`(UI 面) | `dsh-cua-bundle/cordis.patch.yml:1-9` |
| **dsh-computer-use**（新包） | 新 | "独占命名"的 computer-use provider 注册表（第二次注册即失败） | `dsh-computer-use/lib/index.js:19`、`package.json` |
| **dsh-office-kit**（新包） | 新 | 2 方法 `generation`/`render`；`OfficeKitError` 11 个码；无 Kit 也能注册、每次转换以 `unavailable` 拒绝并重定位；配置键 `outputDirectory`/`outputRetentionMs`/`maxOutputStoreBytes`；引擎定位器把 ASAR 路径映射到 `.asar.unpacked` | 【源码】`dsh-office-kit/lib/typert.host.js:71,88`、`lib/types/errors.js`、`lib/types/locate.js:1-60`；【文档】`DOCS/upgrade-guide/v0.2.2-alpha.3/office-kit-package/guide.md`、`DOCS/config-catalog.md:2604-2660` |
| **dsh-tool-office**（新包） | 新 | 3 工具（`office_render` 并发安全、另两个独占）；"仅当成功加载过 Office 技能后、或上一请求头声明过 Office 工具时"才对 Agent 声明 | `dsh-tool-office/lib/index.js:270,400,402,440,455,459,474,478`；`dsh/docs/tool-catalog.md:741-857` |
| dsh-office-to-pdf（删） | - | rc.1 的 `officeToPdf.generation/render` 提供者 | `R1 dsh-web-app/cordis.patch.yml:283-284` |
| **dsh-skill-office** | 14 | 技能资产重组：新增 `assets/office/references`、`office-export-pdf`、`office-recalculate`、`office-render` 四个资产目录；4 个 SKILL.md 改写 | `dsh-skill-office/assets/`（`diff -rq` 逐条） |
| **dsh-error**（新包） | 新 | 仅 1 个 23 行文件：`HarnessError` 抽包；`dsh-llm` 改为 import 并 re-export | `dsh-error/lib/index.js:12-22`；`dsh-llm/lib/index.js:8` |
| **dsh-mcp-manager**（新包） | 新 | 3 方法 `probe`/`restart`/`watch`(stream)；服务还有本地 `list`/`register`/`probe`；状态机 8 态 | `dsh-mcp-manager/lib/typert.host.js:85,119,150`、`lib/types/*` |
| dsh-mcp-client | 7 | 新增 `lib/types/`（`McpProbeResult` 等类型下沉） | `R2` 目录新增 |
| dsh-mcp-resources | 3 | 3 个资源工具**新增并发安全** | `R2 dsh-mcp-resources/lib/index.js:60,71,89` vs `R1`（无） |

## 3. 方法契约（新增 / 改名 / 删除）

### 3.1 `agentTeams/setEnabled`（新增，唯一 Team 远程方法）

- 服务/名字：`service: 'agentTeams'`，`invocation: direct`，`scope: {context:'agent', wire:'agentId'}`
- **参数**（逐字段，`R2 dsh-agent-team/lib/typert.host.js:20-70`）：
  - `agent`: `SessionId`（lookup，wire `agentId`）——"exact live root whose tree receives the selection"，**必须是树根**
  - `enabled`: `boolean`——"selected Team mode"
- **结果**：`z.void()`（`lib/typert.host.js:13`）
- **错误码**：`TEAM_LEAD_REQUIRED`（非根调用，`lib/index.js:791`）；持久化失败冒泡
- **语义（逐字翻译关键点，`lib/index.js:783-797`）**：选择在 await 持久化**之前**生效；工具在"step preparation"时解析可用性；prompt provider 在被求值时读取选择；**任务变更在排队操作开始时检查当前模式，已准入的操作会跑完**；重复设置同值不重复 append（`if (this.isEnabled(root) !== enabled)`）
- **事件落盘**：`root.session.append("team/mode", {enabled})`（`lib/index.js:793`）

### 3.2 Team 任务板（4 个工具 + 服务方法）

服务方法（非远程，工具直接调）：`isEnabled(caller)`、`setEnabled(agent,enabled,signal)`、`createTask(caller,request,signal)`、`getTask(caller,id)`、`listTasks(caller)`、`updateTask(caller,request,signal)`（`lib/index.js:773-836`）。

**工具参数（逐字段，`R2 dsh-tool-agent-team/lib/index.js`）**

- `team_task_create`（:144-178）：`subject`(string,必填)、`description`(string,必填)、`blocked_by`(array<string>,可选)、`write_scopes`(array<string>,可选)
- `team_task_list`（:180-230）：`status`(enum pending|in_progress|completed,可选)、`owner`(string,可选，与 `unowned` 互斥)、`unowned`(bool,可选)、`ready`(bool,可选)、`cursor`(int,默认0，非负安全整数)、`limit`(int,1..100,默认50)
- `team_task_get`（:231-247）：`task_id`(string,必填)
- `team_task_update`（:248-314）：`task_id`(必填)、`expected_revision`(int,**必填**，CAS 前置)、`action`(enum **claim|release|edit|set_dependencies|complete|reopen|reassign|delete**，必填)、`subject`(edit)、`description`(edit)、`blocked_by`(set_dependencies，整份替换)、`write_scopes`(edit)、`owner`(reassign，名字；省略=卸任)

**结果 schema**（`TASK_VIEW_SCHEMA`，`lib/index.js:52-105`）：`id, revision, subject, description, status, owner?(从 ownerId/ownerName 投影), blockedBy[], writeScopes[], ready, writeScopeWarnings[]`；`team_task_list` 包成 `{tasks[], nextCursor?}`。

**错误码全表**（`R2 dsh-agent-team/lib/index.js`，共 16 个）：`TEAM_INVALID_CONFIG`(:705,753)、`TEAM_LEAD_REQUIRED`(:568,791)、`TEAM_DISABLED`(:436)、`TEAM_TASK_LIMIT`(:451,453)、`TEAM_TASK_NOT_FOUND`(:415,478,503,629)、`TEAM_TASK_STALE_REVISION`(:504)、`TEAM_TASK_DELETED`(:505)、`TEAM_TASK_UNAUTHORIZED`(:509)、`TEAM_TASK_ALREADY_CLAIMED`(:514)、`TEAM_TASK_BLOCKED`(:515,577)、`TEAM_TASK_INVALID_TRANSITION`(:527,553,561,569)、`TEAM_TASK_DEPENDENCY_CYCLE`(:417,626)、`TEAM_TASK_HAS_DEPENDENTS`(:587)、`TEAM_MEMBER_NOT_FOUND`(:619)、`TEAM_INVALID_ARGUMENT`(:396,397,535)、`TEAM_INVALID_WRITE_SCOPE`(:408)、`TEAM_TASK_PERSISTENCE_UNCONFIRMED`(:101)。

**权威/状态机（宿主必须逐条实现，`lib/types/task-board.js:101-210`）**：`lead = caller===root`；`authorizeOwner()` = 只有 owner 或 lead 可改；`claim` 要求 status=pending 且 blockers 全 completed，且不允许抢他人任务；`release/complete/reopen/edit/set_dependencies/delete` 需 owner 或 lead；**只有 lead 能 `reassign`**，且只对 pending/in_progress；`delete` 前若仍有依赖者 → `TEAM_TASK_HAS_DEPENDENTS`；每次变更 `revision+1` 且必须连续。

**持久化模型（宿主投影必须复刻，`lib/types/projection.js`）**：session 事件 `team/mode {enabled}`（:72）与 `team/task {version:2, teamId, task}`（:77）；投影 key `agentTeam`，`stateVersion: 7`（:170-171）；读取旧事件时 `version!==2` 视为失败（:81-84）；`enabled` 首次由任务事件隐式置 true（:107）。

### 3.3 `subagents/interruptByUser`（新增，取代 `interruptByParent`）

- **参数**（逐字段，`R2 dsh-subagent/lib/typert.host.js:54-85`）：`childSessionId`: `SessionId`（json，必填，唯一参数）
- **结果**：`{accepted: true}`（`SubagentInterruptReceipt`，"acknowledgement that the cancel signal was admitted, not that the target is quiescent"）
- **授权**：由 Gateway 在**目标会话**上授权（描述逐字："authorized on the target Session by the Gateway. No catalog, history, persistence, or parent Agent lookup runs. Absent, idle, and already-completed targets are accepted no-ops"）
- **对照 rc.1**：`subagents/interruptByParent(childSessionId, parentSessionId, mode:"continuable")`——三个参数，按"声称的父权威"授权（`R1 dsh-subagent/lib/typert.host.js` 中 `interruptByParent` 三个参数定义）
- **本地工具路径**：`interrupt_agent({agent})` → `resolveName(caller, name)` → `ctx.subagents.interrupt(targetId, {kind:"ancestor", agent:caller})`（`R2 dsh-tool-subagent-control/lib/index.js:308-314`）；`kind:"parent"` 出现在另一处归档路径（`R2 dsh-subagent/lib/index.js`，`child.cancel({kind:"parent"})`）

### 3.4 `subagents/prompt`（改签名）

- **参数**：`request` 对象字段 **删** `parentSessionId`、**删** `mode:"continuable"`；保留 `requestId`、`childSessionId`、`delivery:"queue"|"steer"`、`content[]`（text|image）、`clientTimeZone?`（`R2 dsh-subagent/lib/typert.host.js:86-140`）
- **结果**：`{messageId}` 不变
- **错误码**：`subagent/delivery-unavailable`（冷恢复且已满）、`subagent/unauthorized`
- **授权语义变化**："through its live subagent tree root"（rc.1 是 "through the exact live direct parent"），"Access is checked on the target Session before attachment admission or restoration"

### 3.5 `cuaAccess.*`（9 个新方法，`R2 dsh-cua/lib/typert.host.js:125-347`）

| 方法 | 参数 | 结果 | 备注/行号 |
|---|---|---|---|
| `applicationIcons()` | — | `Record<exePath, dataURL PNG>`（无 path 的应用跳过） | :125 |
| `applications()` | — | `{allowedApps[], blockedApps[], persistentAvailable}` | :140 |
| `enablePersistentPtc()` | — | `void`（把内存态 PTC 持久化，走 profile editor） | :155 |
| `openSettings(permission, locale)` | `permission: "accessibility"|"screenRecording"`；`locale: string`（客户端语言） | `void` | :170 |
| `ptcConfiguration()` | — | `{persistent: bool, writable: bool}` | :219 |
| `requestPermission(permission, locale)` | 同 `openSettings` 前两参 | `CuaSystemPermissionState`（"requesting does not establish consent"） | :234 |
| `retryApproval(id)` | `id: CuaApplicationId` | `void`（只清拒绝记录，不授权、不重放） | :283 |
| `revoke(id)` | `id` | `void`（删已存+会话内授权并 join 在途操作） | :315 |
| `systemStatus()` | — | `CuaSystemPermissionState` | :347 |

**`CuaSystemPermissionState` 是两个 discriminated union**（`lib/typert.host.js:88-110`）：
- `darwin`: `{platform:"darwin", accessibility:bool, screenRecording:bool, helper:{pid,path?,name?}, parent:{pid,path?,name?}, launchApplication?:{name,path,icon?}}`
- **`win32`**: `{platform:"win32", sessionLocked: bool|null, captureAvailable: bool, elevated: bool, helper:{pid,path?,name?}}`

**CUA 错误码（19 个，`R2 dsh-cua/lib/index.js` 的 `new NativeError(...)`）**：`APP_ACCESS_DENIED`、`APP_ACCESS_REQUIRED`、`APP_ACCESS_REVOKED`、`APP_APPROVAL_UNAVAILABLE`(:821)、`INVALID_ARGUMENT`、`MOUSE_APPROVAL_CANCELLED`、`MOUSE_APPROVAL_UNAVAILABLE`、`MOUSE_CONTROL_DENIED`、`MOUSE_CONTROL_PENDING`、`MOUSE_CONTROL_UNAVAILABLE`、`NATIVE_BUSY`、`NATIVE_CANCELLED`、`NATIVE_CLEANUP`、`NATIVE_CLOSED`、`NATIVE_DISPOSED`、`NATIVE_EOF`、`NATIVE_EXITED`、`NATIVE_PROTOCOL`、`NATIVE_REQUEST_LIMIT`、`NATIVE_RESPONSE_LIMIT`、`NATIVE_TIMEOUT`、`SCREENSHOT_REQUIRED`、`STALE_SNAPSHOT`。

**权限/审批模型**：`CuaConfigFields.requireSystemPointerApproval`(默认 true，鼠标动作要审批)、`agentPreset`(默认 `"ptc"`)、`allowedApps[{id:64hex,name,executablePath}]`、`helperPath`、一堆超时/上限（`R2 dsh-cua/lib/config.js:5-35`）。应用级授权是**持久**的（`enablePersistentPtc`/`revoke`/`retryApproval` 操作同一份存储）。

**宿主依赖**：helper 是**平台原生可执行文件**，由 `native-build.js` 用编译锁构建成内容寻址路径（`R2 dsh-cua/lib/native-build.js:1-60`），或走预编译包；`dsh-cua/package.json` **没有** optionalDependencies（只有 `dsh-computer-use`/`dsh-storage-domain` 等 JS 依赖），说明 Windows helper 不在本 asar 内提供。

### 3.6 `officeKit.generation` / `officeKit.render`（新增，取代 `officeToPdf.*`）

- `officeKit/generation()`（`R2 dsh-office-kit/lib/typert.host.js:71-87`）：参数无；结果 `string`（"provider lifetime, replaced with rendering, font, or engine configuration"）；`cancellation: signal`
- `officeKit/render(workspaceFileScope, path, priority)`（`R2 dsh-office-kit/lib/typert.host.js:88-160`）：
  - `workspaceFileScope`: `SessionId`（lookup，wire `workspaceFileScopeId`，与 `workspaceFiles` 共用）
  - `path`: string（绝对或工作区相对）
  - `priority`: enum `foreground|background`
  - 结果：`RenderedDocumentBytes`（**PDF 字节** + 源身份 + 缺失字体族），typert 用 `jsonSchemaError: 'unsupported selected reference Uint8Array'` + 自定义 `encode/decode`（:152-157）
- **错误码**（`R2 dsh-office-kit/lib/index.js`）：`busy`、`failed`、`input-too-large`、`invalid-output`、`invalid-request`、`output-too-large`、`source-changed`、`unavailable`、`unsupported-format`（`OfficeKitError`，`lib/types/errors.js:1-15`）
- **引擎**：`@deepseek-ai/libreoffice-kit@0.2.3`（`dsh-office-kit/package.json` dependencies），平台引擎包 `@deepseek-ai/libreoffice-kit-win32-x64`（本机 asar 内**已存在**，含 `bin/ program/ sources/ prebuilds.json`，`package.json` 声明 `os:["win32"],cpu:["x64"]`）；定位时把 ASAR 路径首段 `.asar` → `.asar.unpacked`（`lib/types/locate.js:1-60`）
- **删除项**：`officeToPdf.generation` / `officeToPdf.render`（`tmp/GO8-RC2/methods-diff.json` 的 `removed`）

### 3.7 `mcpManager.probe` / `restart` / `watch`（新增）

- `mcpManager/probe(name)`（`R2 dsh-mcp-manager/lib/typert.host.js:85-118`）：参数 `name: string`；结果 `McpProbeResult` = `{ok:true, serverInfo?, toolNames[], truncated, durationMs, closeConfirmed, cleanupError?}` | `{ok:false, stage:"connect"|"list", message, durationMs, closeConfirmed, cleanupError?}`；`cancellation: signal`
- `mcpManager/restart(name)`（:119-149）：参数 `name`；结果 `void`；语义"dispose 活实例 + 用当前 entry 重挂载（重置重连预算）；cleanup 不完整则**阻塞替换直到宿主重启**"
- `mcpManager/watch()`（:150-230）：`mode:'stream'`，参数无，`cancellation: signal`；结果 `McpManagedServerView[]`，每项 `{name, configured, status}`；`status.state` 取值 **`closed | waiting-credentials | connecting | connected | reconnecting | failed | disabled`**，`failed.reason ∈ gave-up | reconnect-disabled | unclosed-generation | load | cleanup`；`connected` 带 `{toolCount, connectedAt}`，`reconnecting` 带 `{attempt, maxAttempts, nextRetryAtMs, lastError}`
- 服务本地方法还有 `list()`、`register(name, entry)`（返回 disposer）、`probe(name, options)`（`lib/typert.host.js` 的 `model.services[0].members`）
- **与 rc.1 的关系**：rc.1 **没有** `dsh-mcp-manager` 包；`dsh-mcp-client` 从"只有 lib/index.js"变为"lib + lib/types"（把 `McpProbeResult` 等类型下沉到 types），`dsh-mcp-resources` 只多了并发声明。即：rc.2 把 MCP 的**连接监管**从 client 里抽成独立 manager 服务，并第一次对外暴露管理面。

### 3.8 `dsh-error`：无新错误码

- 内容仅 `HarnessError extends Error { code; constructor(message, code, options) ; name = new.target.name }`（`R2 dsh-error/lib/index.js:12-22`，与 rc.1 `dsh-llm` 内的定义逐字一致）
- `dsh-llm/lib/index.js:8` 改为 `import { HarnessError } from "@deepseek-ai/dsh-error"`，并在导出列表里**原样 re-export**（`dsh-llm/lib/index.js:2539` 的 export 里含 `HarnessError`）→ 对消费方**零破坏**
- **对宿主四字段契约（`code/message/details/hint`）的影响**：**无**。四字段是本仓 Transport 的契约，官方远程错误仍是 `RemoteError(code, message, details)` 形状。
- **但有 4 个新错误码/身份落在工具结果层**（这才是要接的）：`TOOL_CONTEXT_UNAVAILABLE`（`R2 dsh-tools/lib/types/error-codes.js:14`）、`APPROVAL_REJECTED`/`APPROVAL_CANCELLED`/`APPROVAL_UNAVAILABLE`（:8-12）配合 `error.info.name="ApprovalError"`；被取消的 body 补 `AbortError` + `TOOL_ABORTED`（`R2 dsh-tools/lib/pipeline.js:478-484`）。

### 3.9 具名寻址 + 目录载荷 v3（本域新增的**跨方法契约**，不在 223 方法里但宿主必须实现）

**【文档】** `DOCS/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:7-31`（权威，逐条）＋**【源码】** `R2 dsh-subagent/src/names.ts`、`src/catalog.ts`。

| 契约点 | 官方规定 | 宿主影响 |
|---|---|---|
| 名字格式 | 根名固定 `lead`；子名 `^[a-z][a-z0-9-]{0,31}$`（禁 `lead`）；全名 = 从根起 `/` 拼接（`reviewer/checker`）；只在**创建者的直接子级**内唯一 | `internal/agent/child_agents.go` 的 `(父会话, agentId)` 表必须加"名字→子会话"索引 |
| 名字解析 | 控制工具的 `agent` 参数**同时接受名字与 Session id**；传 `agent_id` 键 → 参数校验失败 | 解析器要双形态；错误信息要指出该用 `agent` |
| 历史行 | 未命名的历史子代理保留 Session id 作名字；历史任务板的 `owner` 用 `ownerName ?? ownerId` 显示；**老投影会拒绝命名任务** | 读旧会话要兼容；写新会话要写 v3 |
| Team 归属字段 | 工具结果里 `ownerId` **改为 `owner`（owner 的名字）**；`team_task_list/update` 的 `owner` 参数接受名字；"无 owner" 用 `unowned:true` 而不是 `owner:"unowned"`；宿主代码内部仍读 `ownerId` | 宿主 Team 服务保留 `ownerId` 为事实，工具面必须投影成 `owner` |
| 目录载荷 | `subagent/catalog` 写 **version 3**（新增 `name`）；`catalog` 投影 `stateVersion: 6`；`SubagentActivation.name` **必填**，`SubagentActivationSpec.name` 可选 | **前向不兼容**：升级必须整机一起升，旧进程读不了含 v3 的会话（doc 第 5/6 条） |
| 文案 | 消息签名 `Agent <name> sent a message`、结算通知 `Background subagent <name>`、委派收据 `started subagent <name>`、身份上下文 `Your name is "<name>"` | 宿主若有等价文案要同步；模型侧靠它认人 |

## 4. 必须实现的宿主缺口（R2-G01…R2-G16）

> 宿主现状一律指**主树**（`D:/DeepSeek Harness/default-workspace/DSH-GO/`，非本 worktree 的 `.agents/worktrees/…` 副本）。

### R2-G01　Team 数据模型从"名册制"改"任务板制"

- **官方证据**：`R2 dsh-agent-team/lib/types/index.js:47`（`REMOVED_CONFIG_KEYS=['maxMembers','maxMessageBytes','disposalTimeoutMs']`）、`:81`（带 `TEAM_INVALID_CONFIG` 拒绝）、`:68-72`（配置只剩 `maxTasks`/`defaultEnabled`）；【文档】`DOCS/config-catalog.md:175-192`（Config 只有 `defaultEnabled`+`maxTasks`，**没有 `maxMembers`**）、`DOCS/upgrade-guide/v0.2.1-rc.2/agent-team-subagents/guide.md:17`（"`agent-team` rejects `maxMembers`, `maxMessageBytes`, and `disposalTimeoutMs`; `tool-agent-team` rejects `interrupt`, `freshProvider`, and `forkProvider`"）
- **宿主现状**：`modules/tool/team/max_members_test.go:14-40`（断言 `defaultMaxMembers==16`）、`modules/tool/team/spawn.go:74`（`spawn_teammate`）、`docs/parameter-register.md:620`（登记 `agentTeam.maxMembers` 默认 16）——**对齐的是 rc.1 实验包**
- **缺什么**：`maxMembers` 概念整体下线（保留会与官方语义冲突且模型能看到不存在的工具）；`<home>/state/team/agents.json` 名册不再是团队的事实源（子代理目录才是）；`spawn_teammate` 从工具表移除或降级为宿主自有工具并明确标注"非官方"
- **先红测试点**：`team_max_members_test` 改成"宿主不再暴露 `maxMembers` 配置"；`TestNoSpawnTeammateInOfficialSurface`（官方工具表比对）
- **风险**：**高**——删 `spawn_teammate` 会改变模型可见工具面；建议保留实现但默认不注册，并在 `docs/parameter-register.md` 把该行改为"rc.2 已删除"

### R2-G02　`team_task_update` 的 CAS 与动作集

- **官方证据**：`R2 dsh-tool-agent-team/lib/index.js:248-298`（`expected_revision` **必填**、`action` 8 值含 `reassign`）、`lib/types/task-board.js:108-112`（revision 不匹配 → `TEAM_TASK_STALE_REVISION`）
- **宿主现状**：`modules/tool/teamtask/teamtask.go:405-407`（"可选：…**本实现暂不校验**"）、`:408`（action 只有 7 值，**无 `reassign`**）、`:411`（多一个官方没有的 `force`）、`required` 只有 `["task_id","action"]`（:417）
- **缺什么**：`expected_revision` 升为必填 + 真 CAS；补 `reassign`（仅 lead，`owner` 为**名字**且需 `resolveName` 到可续子代理）；删/标注 `force`；补 owner-only/lead-only 授权矩阵
- **先红测试点**：`TestTeamTaskUpdateRejectsStaleRevision`、`TestTeamTaskUpdateReassignRequiresLead`、`TestTeamTaskUpdateRequiresExpectedRevision`
- **风险**：中——老调用方（模型按旧 schema 发 `expected_revision` 可省）会立刻报错，需一次 schema 快照更新

### R2-G03　Team 任务视图字段缺 `writeScopes`/`writeScopeWarnings`/`ready`/`owner`

- **官方证据**：`R2 dsh-tool-agent-team/lib/index.js:52-105`（`TASK_VIEW_SCHEMA`）、`lib/types/task-view.js:28-51`（`ready` 与写域重叠告警推导）
- **宿主现状**：`modules/tool/teamtask/teamtask.go:75-79` 的 `TeamTask` 只有 `subject/description/owner/blocked_by`（+id/status/revision），**没有写完作用域、没有 ready、没有告警**
- **缺什么**：`write_scopes[]` 落盘与投影、`ready = pending && 所有 blocker completed`、写域重叠告警（`scopesOverlap` 按路径段比较）
- **先红测试点**：`TestTeamTaskViewHasWriteScopesAndReady`、`TestTeamTaskWriteScopeOverlapWarns`
- **风险**：低（纯增量），但不做则与官方 `team_task_*` 结果 schema 不一致，模型按官方文档读会缺字段

### R2-G04　`subagents.interruptByParent` → `interruptByUser`

- **官方证据**：`tmp/GO8-RC2/methods-diff.json` 的 `removed: ["subagents.interruptByParent"]` / `added: ["subagents.interruptByUser"]`；`R2 dsh-subagent/lib/typert.host.js:54-85`（单参数 `childSessionId`，Gateway 按目标会话授权）
- **宿主现状**：`docs/host-stable-gap-register.md:510`（`subagents.interruptByParent` IMPLEMENTED）、`cmd/dshd/serve_agent_api.go:815`（`subagentsInterruptByParent(childSessionID, parentSessionID)`）、`cmd/dshd/serve_subagents_api_test.go:388-419`（父/假父/未登记三态测试）
- **缺什么**：新增 `subagents.interruptByUser(childSessionId)`（**不再要求父 id**，改由 Gateway 的会话访问权校验）；旧方法按官方删除（或保留但登记为宿主私有，不列入官方 223）
- **先红测试点**：`TestSubagentsInterruptByUserSingleParam`（只给孩子 id 即可中断）、`TestSubagentsInterruptByUserDeniesForeignSession`（越权 → `subagent/unauthorized`）
- **风险**：中——授权口径变宽/变窄都会影响安全边界；必须与 Gateway 的会话访问权判定同源

### R2-G05　`subagents.prompt` 请求体删两个字段

- **官方证据**：`R2 dsh-subagent/lib/typert.host.js:86-140`（`required: ["requestId","childSessionId","delivery","content"]`）
- **宿主现状**：`docs/parameter-register.md:391` 记录宿主按 `(parentSessionId, childSessionId)` 地址认领；`cmd/dshd/serve_subagents_api.go:83`（`Deliver`）
- **缺什么**：请求解码去掉 `parentSessionId`/`mode`（**关键**：`mode` 在 rc.1 是 `strictObject` 的必填 `const`，rc.2 的 strict codec 会因多余字段/缺字段行为不同——需按 rc.2 的 `strictObject` 允许集实现）；授权改为"目标会话在活树根之下"
- **先红测试点**：`TestSubagentsPromptRejectsParentSessionId`（多余字段按 strict 处理策略）、`TestSubagentsPromptAuthorizesViaTreeRoot`
- **风险**：中——wire 兼容；223 方法清单里这两个 change 属于"同 method 不同 body"的静默不兼容

### R2-G06　子代理"具名 + 多层"数据模型（names.js 全量）

- **官方证据**：`R2 dsh-subagent/lib/types/names.js:10`（`ROOT_AGENT_NAME='lead'`）、`:12`（`^[a-z][a-z0-9-]{0,31}$`）、`:18`（`isAgentName`，禁 `lead`）、`:27`（`joinAgentPath`，根=空串）、`:38`（`agentAddress`）、`:48`（`parentAgentPath`）、`SiblingNameReservations`（同名预订，避免并发兄弟撞名）；`maxDepth` 默认 **1**（`lib/index.js:2962-2963`，与 rc.1 相同）
- **宿主现状**：`internal/agent/child_agents.go:3-58` 只有 `(父会话, agentId) → 子会话 id` 的 durable 地址表；工具参数是 `id`（`modules/tool/subagent/subagent.go:52-55`），**没有名字、没有路径、没有 `lead` 概念、没有同名预订**
- **缺什么**：名字→id 解析（`resolveName` 等价物）、成员全名 = 从根起的 `/` 路径、`lead` 保留、`agent-N` 自动编号、同父下并发创建的名字预订、`list_agents` 输出 `{name(path), description, status}` 而非 `{id,label}`
- **先红测试点**：`TestAgentNameValidation`（正则/`lead`/大小写）、`TestAgentPathJoinAndParent`、`TestSiblingNameReservationRejectsDuplicate`、`TestListAgentsRendersTreePath`
- **风险**：**高**——这是 rc.2 协作面的地基；`agent_id → agent(name)` 的改名会让旧调用全部失效，必须与 G07/G08 同批上线

### R2-G07　控制工具参数 `agent_id` → `agent`

- **官方证据**：`R2 dsh-tool-subagent-control/lib/index.js:228-236`（`send_message.agent`）、`:285-292`（`interrupt_agent.agent`）、`DOCS/tool-catalog.md:2269-2350`
- **宿主现状**：`modules/tool/team/spawn.go:264`（`interrupt_agent` 走的是**名册**不是树）、`cmd/dshd/snapshot_test.go`/`tool_lazy.go` 的官方工具名表未见 `send_message/list_agents/wait_agent` 的 rc.2 形状（需按 rc.2 对齐）
- **缺什么**：三个控制工具统一 `agent`（名字），并补 `resolveName`；`list_agents` 去 `scope` 参数（rc.1 的 `children|descendants` 删除）
- **先红测试点**：`TestSendMessageAcceptsAgentName`、`TestListAgentsHasNoScopeParam`
- **风险**：中（schema 快照 + 模型提示同步）

### R2-G08　`interrupt_agent` 可关闭 + 标准 preset 默认关

- **官方证据**：`R2 dsh-tool-subagent-control/lib/index.js:211,218,283`（`Config{interrupt: default true}`，`false` 时 `if (!interrupt) return;` 不注册）；【文档】`DOCS/upgrade-guide/v0.2.2-rc.1/subagent-interrupt-default/guide.md:7-9`（**base bundle + Web `standard`/`cordis`/`ptc` 三 preset 都设 `config.interrupt:false`**；插件自身默认仍 true；服务层中断 API 仍可用）；【源码】`R2 dsh-base/cordis.patch.yml:389`、`R2 dsh-web-app/presets/standard.patch.yml:98`、`cordis.patch.yml:94`、`ptc.patch.yml:95`（`R1` 三个 preset 均无该键）
- **宿主现状**：宿主 `interrupt_agent` 恒注册（`modules/tool/team/spawn.go:264`）
- **缺什么**：装配级开关 + 默认形状对齐（standard 不注册 `interrupt_agent`，能力改由 `send_message` 推进）；关闭时**系统提示里的指引也要同步消失**（官方 `subagent:identity` provider 按工具是否可见拼指引，`lib/index.js:265-276`）
- **先红测试点**：`TestInterruptAgentNotRegisteredWhenDisabled`、`TestPromptOmitsInterruptGuidanceWhenDisabled`
- **风险**：中——"少一个工具"必须由配置解释清楚，不能变成静默缺能力

### R2-G09　并行分类器：宿主缺"逐调用分类器"，且新增并发安全面

- **官方证据**：分类逻辑 `R2 dsh-tools/lib/pipeline.js:286-289`（`!tool?.isConcurrencySafe` → exclusive；`=== true` 才 parallel；**抛错也归 exclusive**）；声明面新增 `dsh-tool-fs-search/lib/index.js:780,1095`、`dsh-tool-jobs/lib/index.js:470`、`dsh-tool-schedule/lib/index.js:581,606`、`dsh-tool-cordis/lib/index.js:50,88`、`dsh-tool-agent-team/lib/index.js:215,240`、`dsh-mcp-resources/lib/index.js:60,71,89`、`dsh-tool-office/lib/index.js:440`（office_render 安全、convert/recalculate 不安全 :459,478）；`maxParallelToolCalls` 默认 **10**（`R2 dsh-agent-loop/lib/index.js:1322`，与 R1 `:1309` 同）
- **宿主现状**：`internal/spi/tool.go:17-32`（`Tool` 接口只有 `Name/Description/Schema/Caps/Exec`，**没有 per-call 分类器**）；`internal/agent/parallel.go:52`（`DefaultMaxParallelToolCalls = 10`，值对）；并发判定靠什么见 `internal/agent/parallel.go` 的 `blocksOnHuman` 车道
- **缺什么**：`Tool` 增加 `ConcurrencySafe(args) bool`（缺省 = 不安全，fail-closed；实现必须能接收 args）；把 `glob/grep/job_list/schedule_list/view/team_task_list/get/office_render/mcp 资源工具` 标为安全；`office_convert/recalculate` 必须独占（会写临时文件/同一输出目录）
- **先红测试点**：`TestExecutionModeFailsClosedOnPanic`（分类器 panic → 独占）、`TestGrepGlobRunInParallel`（一批 6 个 grep 并发且结果保序）、`TestOfficeConvertIsExclusive`
- **风险**：低-中；**保序**由现有 worker/结算分离保证，不要动

### R2-G10　工具结果错误身份：`error.info{name,code}` + 4 个新码

- **官方证据**：`R2 dsh-tools/lib/types/error-codes.js:6-16`（`APPROVAL_ERROR_NAME`/`APPROVAL_REJECTED`/`APPROVAL_CANCELLED`/`APPROVAL_UNAVAILABLE`/`TOOL_CONTEXT_UNAVAILABLE`）、`lib/pipeline.js:612-660`（四种审批结果各自带 `approvalDenial(...)`）、`:841-856`（`approvalDenial` 造 `info`）、`:478-484`（取消抛出物补 `AbortError`+`TOOL_ABORTED`）、`lib/types/contracts.js:42-56`（`ToolContextUnavailableError`）
- **宿主现状**：`internal/spi/tool.go:51-69`（`Result` 无错误身份字段）、`internal/spi/tool.go:59`（只有 `Hint`）
- **缺什么**：`Result`/`tool/result` 事件增 `error.info{name,code}`（v4 是边界编码，需检查投影）；审批三态分别给专用码（现在是否只是字符串需核 —— 未取证）；被取消 body 结果的 `name/code` 归一
- **先红测试点**：`TestApprovalRejectedCarriesApprovalErrorCode`、`TestToolContextUnavailableCode`、`TestCancelledBodyGetsAbortErrorIdentity`
- **风险**：中——`tool/result` 是 v4 契约，加字段要走快照；模型侧可据此区分"人拒绝"与"没有审批通道"

### R2-G11　CUA：9 方法 + Windows provider 空缺

> 行为面（窗口化输入、能力协商、AX 上限、悬停语义）另见 **G15**，本项只讲方法面与降级。

- **官方证据**：方法面 `R2 dsh-cua/lib/typert.host.js:125-347`（9 个，§3.5）；`win32` 状态分支已定义（`:100-110`）；bundle 只挂 macOS provider（`R2 dsh-cua-bundle/cordis.patch.yml:5-8`：`dsh-experimental-cua-macos` `disabled: platform !== 'darwin'`）；helper 需平台原生二进制（`lib/native-build.js:1-60`）
- **宿主现状**：`grep -rn "cuaAccess" --include=*.go cmd/ internal/ modules/` → **0 命中**（完全未实现；`docs/host-stable-gap-register.md` 的 193 清单里也没有它们，因为是 rc.2 新增）
- **缺什么**：9 个方法的**结构化未实现**（四字段 + `code:"cua/unavailable"` 之类，形状按 `CuaApplicationState`/`CuaSystemPermissionState` 给空值集合）+ 至少 `systemStatus` 返回 Windows 分支真实观测（`sessionLocked`/`captureAvailable`/`elevated` 可从 Win32 取）；`applications`/`applicationIcons` 可返回空集 + `persistentAvailable:false`（**不得假数据**）
- **先红测试点**：`TestCuaAccessMethodsReturnStructuredUnavailable`（9 条，字段齐全、非法静默成功）、`TestCuaSystemStatusWin32Shape`（平台分支形状）
- **风险**：中——**唯一不能盲实现的是动作类能力**（截图/鼠标/键盘）；方法面可以按"结构化未实现"先落地

### R2-G12　Office：`officeToPdf.*` → `officeKit.*` + 3 个工具

- **官方证据**：`R2 dsh-office-kit/lib/typert.host.js:71-160`（2 方法，`render` 多 `priority`）；`R2 dsh-tool-office/lib/index.js:402,455,474`（3 工具）＋`:400` 的"技能加载后才声明"闸；引擎 `@deepseek-ai/libreoffice-kit@0.2.3` + 本机已有 `libreoffice-kit-win32-x64`（`libreoffice-kit-win32-x64/package.json`）
- **宿主现状**：`cmd/dshd/serve_office_pdf.go:3`（`officeToPdf.generation/render` 的宿主实现）、`internal/officepdf/provider.go`、`internal/transport/handlers_media_market_official.go:64`（`MethodOfficeToPdfRender`）、`:701-732`（**明确 501**："本仓没有 Office 渲染器…不拿文本抽取或空 PDF 冒充渲染结果"）
- **缺什么**：方法常量改 `officeKit.generation/render`（旧两个删除）；3 个工具（`office_convert/recalculate/render`）；LibreOfficeKit 引擎接入（本机有 win32-x64 预编译包 → **可做的真实路径**）；`OfficeKitError` 9 码映射
- **先红测试点**：`TestOfficeKitGenerationRenderRoutes`、`TestOfficeToolsAbsentUntilOfficeSkillLoaded`、`TestOfficeConvertRealDocxToPdf`（真跑一次才叫绿；没有引擎时必须回 `unavailable` 而不是假 PDF）
- **风险**：**高投入**——引擎接入是最大工作量；可先做"方法 + 工具形状 + `unavailable`"，再做真转换

### R2-G13　MCP 管理面 3 方法

- **官方证据**：`R2 dsh-mcp-manager/lib/typert.host.js:85,119,150`（`probe`/`restart`/`watch`，`watch` 是 stream，状态机 8 态）
- **宿主现状**：`cmd/dshd/mcp.go:28,46,51`（只有 `List`/`Reconnect`/`Toggle`，对应 `mcp.list`/`mcp.reconnect`/`mcp.toggle`——**宿主自有方法**）
- **缺什么**：`mcpManager.probe(name)`（不改活实例、连一次列工具再关，返回 stage 分类结果）、`mcpManager.restart(name)`（重建实例 + 重置重连预算，cleanup 不完整则拒绝）、`mcpManager.watch`（stream 推送整表）；`restart` 不能直接等同 `Reconnect`
- **先红测试点**：`TestMcpManagerProbeReportsConnectStage`、`TestMcpManagerWatchStreamsFullList`、`TestMcpManagerRestartResetsReconnectBudget`
- **风险**：中——`watch` 是 stream 方法，宿主 journal/订阅模型要支持（与 `terminal.watch` 同批）

### R2-G14　`subagent/catalog` 载荷 v3 + 具名寻址（前向不兼容，最高优先）

- **官方证据**：【文档】`DOCS/upgrade-guide/v0.2.2-alpha.3/subagent-names/guide.md:7-31`（名字/解析/行格式/owner/载荷 v3/文案，7 条迁移步骤）；【源码】`R2 dsh-subagent/src/catalog.ts:21,40`（`version: 3` + `name`）、`src/catalog.ts:138`（`stateVersion: 6`）、`src/names.ts:10-48`
- **宿主现状**：`internal/agent/child_agents.go:3-58` 只有 `(父会话, agentId) → childSessionId`；`modules/tool/subagent/subagent.go:52-55` 用 `id`；无 `name`、无路径、无 `lead`、无同名预订
- **缺什么**：① `subagent/catalog` 事件写 v3（含 `name`）并把 catalog 投影 stateVersion 提到 6；② 名字校验/路径拼接/`lead` 保留/`agent-N` 编号/同父并发预订；③ 控制工具 `agent` 双形态解析（名字或 Session id）；④ `list_agents` 行 `{name,status,description?}` + 文本行格式；⑤ 委派收据与消息/结算文案改名字；⑥ Team 工具结果 `owner`（名字）与内部 `ownerId` 的双表示
- **先红测试点**：`TestSubagentCatalogWritesNameVersion3`、`TestOlderHostRejectsV3Catalog`（前向不兼容守卫）、`TestControlToolResolvesNameOrSessionId`、`TestAgentNamePathRoundTrip`、`TestListAgentsRowShape`
- **风险**：**最高**——载荷版本升级是**整机不可逆**：一旦宿主写了 v3，rc.1 形态的旧进程/回滚版本读不了该会话（doc 第 5、6 条明确）。必须先做版本门槛与回滚方案，再落盘

### R2-G15　CUA 的 rc.2 行为面（窗口化输入 / 能力协商 / 悬停语义）

- **官方证据**：【文档】`DOCS/upgrade-guide/v0.2.2-rc.1/cua-native-platform/guide.md`（`appHandle`/`windowId`、`window.getScreenshot|click|scroll|drag`、`hover`→`window.mouseMove`、`getAXState`→`getAccessibilityTree`、元素 ID 由数字索引变**不透明字符串** + `snapshotId`、`pressKey(key, modifiers)`、`getCapabilities().methods`、`dispatched` 取代 `verified`、`INVALID_ARGUMENT` 取代未知键名的 `NATIVE_ERROR`）；`…/cua-ax-read-limits/guide.md`（macOS 默认 `maxElements` 2000→**5000**、`maxDepth` 25→**50**，且支持**单次读取覆盖**）；`…/cua-hover-ax-only/guide.md`（`mouseMove` 默认走 AX 备用 UI，无路由时返回 **`HOVER_UNAVAILABLE`**，`useSystemPointer:true` 才动真光标且要审批）
- **宿主现状**：`grep -rn "cuaAccess" --include=*.go` = 0（9 方法全未实现）；`internal/` 无 native helper
- **缺什么**：本项**不是**要宿主实现 CUA，而是要求：① 上面这些行为**登记进 docs/**，避免将来做 CUA 时按 rc.1 语义实现；② `HOVER_UNAVAILABLE` 与 `INVALID_ARGUMENT` 进错误码表（【未取证】：`HOVER_UNAVAILABLE` 只出现在 macOS provider 侧，本 asar 的 `dsh-cua` 包里 grep 不到，故标"文档有、源码未定位"）
- **先红测试点**：无（不实现能力时不需要红测）；只做登记
- **风险**：低（现在），但漏登记会在将来产生"按旧语义实现"的返工

### R2-G16　Office 的宿主侧安全边界（LibreOffice 在宿主进程内解析不可信文档）

- **官方证据**：【文档】`DOCS/upgrade-guide/v0.2.2-alpha.3/office-operation-tools/guide.md:22`（逐字："The Office tools parse untrusted documents with the native LibreOffice Kit engine **in the Host process, outside the shell sandbox and PTC permissions, with the Host user's permissions**；the filesystem backend still authorizes inputs"）；`office-kit` 行新增 `outputDirectory`（默认 `$DSH_HOME/office-kit/outputs`）、`outputRetentionMs`、`maxOutputStoreBytes`；provider **无 Kit 也能注册**，每次转换以 `OfficeKitError{code:'unavailable'}` 拒绝并**重新定位 Kit**（同 guide 第 3 段）；`DOCS/config-catalog.md:2604-2660`
- **宿主现状**：`internal/officepdf/provider.go`、`cmd/dshd/serve_office_pdf.go:3`、`internal/transport/handlers_media_market_official.go:701-732`（当前明确 501）
- **缺什么**：① 在 `docs/sandbox-policy.md` 与文档里**显式登记**"Office 转换不受 shell 沙盒/PTC 权限约束"这一官方口径（否则会与 R1/R6 的沙盒声明冲突）；② 传入路径仍走文件系统授权；③ 输出目录/保留期/容量三个配置键；④ 无引擎时必须 `unavailable` 而不是假结果
- **先红测试点**：`TestOfficeKitErrorCodeUnavailableWithoutEngine`、`TestOfficeConvertRejectsUnreadableInput`
- **风险**：中——安全边界是文档级承诺，写错等于虚假声明

## 5. 可等价 / 不适用项

| 项 | 结论 | 理由与证据 |
|---|---|---|
| `dsh-error` 包 | **可等价，不需开工** | 只是 `HarnessError` 抽包，定义与 rc.1 内联版逐字相同（`R2 dsh-error/lib/index.js:12-22`）；`dsh-llm` 原样 re-export（`dsh-llm/lib/index.js:8,2539`）。宿主 `internal/` 已有等价错误基类（无需新增包） |
| `dsh-computer-use`（provider 注册表） | **不适用（当前）** | 它是"独占命名注册"的抽象（第二次注册即失败，`dsh-computer-use/lib/index.js:19`），Windows 上没有 provider 可注册；只有接入真实 helper 时才需要 |
| `dsh-cua` 的动作类能力（截图/鼠标/键盘/clipboard） | **不适用（盲实现边界外）** | 需要平台原生 helper（`lib/native-build.js`）+ 真实桌面；宿主只能做方法面与 `systemStatus` 观测 |
| `dsh-experimental-cua-macos` | **不适用** | darwin-only，`disabled: process.platform !== 'darwin'`（`R2 dsh-cua-bundle/cordis.patch.yml:5-6`） |
| `stagehand_*`（6 个浏览器工具） | **本域不认领** | 属 `dsh-experimental-browser-use-stagehand-native`，默认不装（`DOCS/tool-catalog.md:258-437`），不在 base/preset 装载面 |
| `ralph` / `cordis_inspect_*` / `terminal_*` / `session_*` / `lsp` / `create_worktree` | **本域不改** | 逻辑未变或属其它域；仅并发声明变化已在 G09 覆盖 |
| `dsh-tool-bash-persistent` / `pwsh-persistent`（`bash`/`pwsh` 同名工具） | **不冲突** | 与一次性版同名但不同包，装载互斥（`DOCS/tool-catalog.md:1005,1032`）；宿主单实现即可 |
| `office_recalculate` | **可后置** | 依赖同一 LibreOfficeKit，但主要价值在公式重算；先做 convert/render 即可覆盖大部分场景（`DOCS/tool-catalog.md:784-812`） |
| rc.1 的 `dsh-experimental-agent-team` 名册/生命周期/超时销毁 | **不适用（已删）** | rc.2 用"子代理 + inbox"替代；`disposalTimeoutMs` 被显式拒绝（`R2 dsh-agent-team/lib/types/index.js:47`） |
| 宿主 `spawn_teammate` / `agents.json` 名册 | **不适用（保留为宿主私有）** | 官方 rc.2 无对应工具；建议保留实现、默认不注册、在 `docs/parameter-register.md:620` 标注 rc.2 已删除 |
| `wait_agent` 的 `noProgress` 移除 / `list_agents` 的 `scope` 移除 | **不计入本次增量** | 【文档】`DOCS/upgrade-guide/v0.2.1-rc.2/agent-team-subagents/guide.md:14-16` 列了它们，但【源码】`R1` 早已没有 `noProgress`（`grep -c` = 0）、`R1` 才有 `scope`（rc.2 才删）→ 前者属 rc.1 之前，后者属本次增量（已计入 G07） |
| `dsh-subagent` 的 `maxActiveSubagents`(8) / `maxDepth`(1) | **未变，不需开工** | 【文档】`DOCS/config-catalog.md:3715-3721`；【源码】`R1 dsh-subagent/lib/index.js:2992-2994` 与 `R2 …:2962-2963` 同值同默认 |
| `office_recalculate` | **可后置** | 依赖同一 LibreOfficeKit，但主要价值在公式重算；先做 convert/render 即可覆盖大部分场景（`DOCS/tool-catalog.md:784-812`） |

## 6. 待用户裁定

1. **CUA 降级边界**：Windows 无 provider、无 helper 二进制。三条可选：(a) 9 方法全部回"结构化未实现"（形状正确、`persistentAvailable:false`），但 `systemStatus` 用 Win32 真实观测填 win32 分支；(b) 只做 (a) 里的 `systemStatus`+`applications`，其余 7 个 501；(c) 完全不做（与 rc.2 官方能力差距最大）。**建议 (a)**，因为它能真实回答"这台机器能不能被 computer-use"，而不假装能操作。请裁定。
2. **`spawn_teammate` 去留**：删（干净对齐）／保留为宿主私有（不列入官方工具表）／保留并默认可注册（会与官方 `team_task_*` 语义打架）。**建议保留但默认不注册**。请裁定。
3. **Office 引擎接入深度**：只做方法+工具形状+`unavailable`（1-2 天）／接入本机 `libreoffice-kit-win32-x64` 做真转换（工作量大、需真实 docx 回归样本）。**建议先做形状再做真转换，分两次验收**。请裁定顺序。
4. **`interrupt_agent` 默认值**：官方 standard preset 传 `interrupt:false`（宿主若对齐，模型将失去"打断子代理"的能力，只能靠 `send_message` 排队）。是否照抄？（照抄 = 与官方一致但能力变小；不照抄 = 比官方多）。**建议照抄官方**，并在 CHANGELOG 写明原因。
5. **`team_task_update` 加必填 `expected_revision`** 会让按旧 schema 生成的模型调用立刻报错。是否接受一次"先红"的破坏性对齐？（**建议接受**，官方 CAS 是任务板正确性的关键。）
6. **`subagent/catalog` v3 的前向不兼容**：官方明确"旧版本读不了含 v3 载荷的会话"（【文档】subagent-names guide 第 5/6 条）。写 v3 = 放弃"回滚到 rc.1 语义的宿主"这条路。是否接受？若接受，回滚方案只能是"保留旧二进制但禁止打开新会话"还是"导出/降级工具"？**需要用户定**，这是本次唯一不可逆项。
7. **Office 转换的安全口径**：官方让 LibreOffice 在**宿主进程内、壳沙盒之外、以宿主用户权限**解析不可信文档（【文档】office-operation-tools guide:22）。宿主是照抄（简单、与官方一致，但把解析风险放在主进程）还是加一层独立进程隔离（更安全、偏离官方形状、工作量 +1 档）？**建议照抄 + 在 `docs/sandbox-policy.md` 显式登记该例外**，避免虚假的沙盒承诺。
8. **`agent` 参数双形态（名字或 Session id）**：官方同时接受两者，且传旧的 `agent_id` 直接失败。宿主是否也接受 Session id（兼容旧脚本），还是只收名字（更严格、逼模型用名字）？**建议双形态照抄**，因为官方 `list_agents` 也可能返回 Session id 形式的名字（未命名历史行）。
