# 23 · 「一切皆插件」在本机上的真实形状

> 证据：asar 内 `@deepseek-ai/*` 的 `lib/*.js` 与各包 `README.md`。
> 下文行号除注明 `client` 者外，均指该包 `lib/index.js`；`client` 指 `lib/client.js`。
> 装配文件：`dsh-base/cordis.patch.yml`（529 行，**一次 insert 到空 profile 根**）。

## 0. 结论先行：插件的三层形态

| 层 | 是什么 | 谁装配 | 证据 |
|---|---|---|---|
| **宿主插件** | 一个 npm 包，`name` 即 cordis 插件，开机由 `cordis-plugin-loader` 按 profile 的 patch 树加载 | `dsh-base` 的 `dsh.bundle.patch` + 各 mode bundle + 用户 profile patch，**后写覆盖前写（按 id 逐行整块替换 config）** | `dsh-base/package.json` 的 `dsh.bundle.patch`；`cordis.patch.yml:1-13` |
| **服务** | 插件 `extends Service` 并用 `super(ctx,'key')` 注册到 cordis，消费方用 `ctx.get('key')` / `inject` 依赖 | 运行时按「服务可用性」激活，**行顺序无加载语义** | `cordis.patch.yml:12-13`；104 个 key（§2） |
| **客户端插件** | 同一包里多一个 `./client` 导出（浏览器 bundle），由宿主经 `/plugins` 服务、浏览器 `__ModuleLoader__` 懒加载 | `dsh-client-modules` 扫描 `dsh.client` 生成启动图 | `dsh-client-modules/README.md:12,34` |

## 1. `dsh-base` 的 bundle patch：96 行 / 94 个包

实测：`- id:` 行 **96** 条，去重后 **94** 个包名（`dsh-plugin-manager` 与 `dsh-tool-subagent` 各占 2 行，后者是 `/list-agents` 子路径插件）。
→ 任务书说的「95 个包」与实测差 1；**以 96 行/94 包为准**（口径：`dsh-base/cordis.patch.yml` 的 `insert` 列表）。

格式：`entry-id → 包名 — 职责`；`(off)` = 该行带 `disabled: true` 或平台条件。

**① 加载器 / 配置 / 包治理（7）**
- `tool-plugin-manager → dsh-plugin-manager/tools — 给模型用的插件管理工具（默认禁用）(off)`
- `plugin-manager → dsh-plugin-manager — Web 侧栏/代理安装、移除、选择 profile 插件`
- `timer → cordis-plugin-timer — 可随 fiber 释放的定时器服务`
- `hmr → dsh-hmr — 插件代码与 profile 配置的统一重载队列（无 profileContext 时禁用）`
- `config-editor → dsh-config-editor — 保存并应用当前 profile 配置`
- `settings → dsh-settings — 用 Config 派生表单实时编辑插件配置`
- `plugin-package-inventory-deepseek → dsh-plugin-package-inventory-deepseek — 活跃 Loader 包清单元数据`

**② LLM（7）**
- `llm → dsh-llm — provider 中立的模型调用服务（流式请求、适配器注册、模型元数据）`
- `deepseek-llm-api-extensions → dsh-deepseek-llm-api-extensions — 官方 DeepSeek 请求扩展字段注册表`
- `llm-retry → dsh-llm-retry — 在耐久 step 边界上做 provider 路由重试`
- `llm-pi-ai → dsh-llm-pi-ai — pi-ai 多 provider 适配器（默认休眠，零路由）`
- `llm-deepseek → dsh-llm-deepseek-api-key — DeepSeek API-key 认证/模型发现`
- `llm-deepseek-account → dsh-llm-deepseek-account — DeepSeek 账号认证与模型门禁`
- `agent-default-model → dsh-agent-default-model — 新建 agent 的默认 provider/model`

**③ 会话与持久化（12）**
- `session → dsh-session — 事件源会话日志 + 内存 store（`ctx.sessions`）`
- `session-log-deepseek → dsh-session-log-deepseek — 会话日志增量上报官方`
- `session-persistence-jsonl → dsh-session-persistence-jsonl — JSONL+zstd 会话落盘（root=$DSH_HOME/sessions）`
- `session-query-sqlite → dsh-session-query-sqlite — FTS5 全文检索（默认 `openAt: never` 关）`
- `session-projection → dsh-session-projection — 会话投影注册表`
- `session-projection-cache → dsh-session-projection-cache — 投影的持久化 checkpoint 缓存`
- `session-checkpoint-policy → dsh-session-checkpoint-policy — 语义化 durability checkpoint`
- `session-title → dsh-session-title — 日志派生的会话标题`
- `session-title-llm → dsh-session-title-first-prompt-llm — 首条消息 LLM 生成标题`
- `attachment-local → dsh-attachment-local — 图片附件本地存储（$DSH_HOME 下）`
- `working-directory → dsh-working-directory — 单会话工作目录切换（重放可见）`
- `user-questions → dsh-user-questions — 瀑布式问答服务（工具/权限/本地应答）`

**④ 类型反射 / 远端 / 存储 / 遥测（10）**
- `typert → dsh-typert-registry — 运行时类型反射注册表（Zod 工厂 + Remote 描述符）`
- `typert-loader → dsh-typert-loader — 把挂载包的 host 反射自动灌进注册表`
- `typert-gateway → dsh-api-gateway — 类型化 Client↔Host 调用/流（校验、取消、重连、事件转发）`
- `remote-access → dsh-api-remote-access — 非 owner Peer 的方法级 RBAC`
- `storage → dsh-storage — 存储中枢（命名后端）`
- `storage-json → dsh-storage-json — JSON 后端（整单元/逐记录文件）`
- `storage-domain → dsh-storage-domain — schema 校验、发变更的 KV 域`
- `otel → dsh-otel — 独立的 OTLP 上报通道`
- `session-telemetry-otel → dsh-session-telemetry-otel — 会话遥测的 OTel 后端`
- `subprocess → dsh-subprocess-local — OS 进程/真实终端的本地 subprocess provider`

**⑤ 沙箱 / 审批 / 权限（9）**
- `sandbox → dsh-sandbox-local — 各平台进程隔离后端（Linux/macOS/Windows）`
- `sandbox-policy → dsh-sandbox-policy — 每调用沙箱策略解析 + 当前模型上下文`
- `bash-sandbox → dsh-bash-sandbox — 受沙箱约束的 bash 执行器（win32 禁用）(off)`
- `pwsh-sandbox → dsh-pwsh-sandbox — 受沙箱约束的 PowerShell 执行器（非 win32 禁用）(off)`
- `fs-sandbox → dsh-fs-sandbox — 强制沙箱的 `ctx.fs` 后端（把写限制在会话 workspace）`
- `fs-observation-policy → dsh-fs-observation-policy — 读后写策略（改前必须先读）`
- `approval → dsh-user-approval — 渠道无关的一次性审批 seam（fail-closed）`
- `permission → dsh-permission-presets — 用户可见权限预设（沙箱模式×审批策略）`
- `shell-env → dsh-shell-env — 受管的 DSH_* shell 环境`

**⑥ 模型可见工具（17）**
- `tools → dsh-tools — 工具注册表与执行流水线（`ctx.tools`）`
- `tool-fs → dsh-tool-fs — read / read_image / write / edit`
- `tool-fs-search → dsh-tool-fs-search — glob / grep`
- `tool-bash → dsh-tool-bash — bash 一次性执行 + 后台 job + 沙箱升级（win32 禁用）(off)`
- `tool-pwsh → dsh-tool-pwsh — PowerShell 等价工具（非 win32 禁用）(off)`
- `tool-jobs → dsh-tool-jobs — job_output / job_list / job_kill`
- `jobs → dsh-jobs-local — 进程内后台 job 注册表（工具背后的服务）`
- `tool-working-directory → dsh-tool-working-directory — 读/切当前目录`
- `tool-todo → dsh-tool-todo — todo_write（整表替换，按会话归属）`
- `tool-goal → dsh-tool-goal — get_goal / create_goal / update_goal`
- `tool-skill → dsh-tool-skill — skill 目录与加载器`
- `tool-web → dsh-tool-web — web_search / web_fetch`
- `tool-subagent → dsh-tool-subagent — subagent 委派`
- `tool-subagent-fork → dsh-tool-subagent — 同一包的 fork 变体（子代理继承父会话）`
- `tool-subagent-control → dsh-tool-subagent-control — send_message / interrupt_agent / list_agents`
- `tool-subagent-list-agents → dsh-tool-subagent-control/list-agents — 子路径插件：list_agents 单独注册`
- `mcp-resources → dsh-mcp-resources — 按需发现/读取 MCP resources（工具形式）`

**⑦ 压缩 / 保留 / 编排（11）**
- `token-meter → dsh-token-meter — 重放感知的 token 与上下文压力计量`
- `compaction-basic → dsh-compaction-basic — 压力升高时自动摘要旧历史`
- `command-compact → dsh-command-compact — `/compact` 斜杠命令`
- `tool-result-pruner → dsh-compaction-tool-result-pruner — 工具输出裁剪`
- `image-offload → dsh-compaction-image-offload — 图片预算超限时的降级处理`
- `spill-local → dsh-spill-local — 溢出文本落到会话私有文件`
- `spill-policy → dsh-spill-policy — 共享 text/image token 预算 + 可读恢复文件`
- `ptc-runtime → dsh-ptc-runtime-node — 在全新 Node 进程里跑 TS 程序（带会话 fs 沙箱）`
- `workflow-ptc → dsh-workflow-ptc — 经 PTC 运行时跑 workflow 编排`
- `tool-workflow → dsh-tool-workflow — 模型可见的 workflow 工具`
- `timeout-policy → dsh-tool-call-timeout-policy — 协作式超时（把 timeout 映射为模型错误）`

**⑧ 子代理 / 目标 / 计划（9）**
- `agent → dsh-agent — Agent 句柄、活体注册表、`agent/*` 事件词汇`
- `agent-loop → dsh-agent-loop — 默认 agent 驱动器（turn/step 怎么跑）`
- `subagent → dsh-subagent — 子代理委派 seam`
- `subagent-spawn-in-process → dsh-subagent-spawn-in-process — 进程内 spawn 后端`
- `subagent-fork-in-process → dsh-subagent-fork-in-process — 进程内 fork 后端（继承父轮次）`
- `goal → dsh-goal — 会话内唯一持久目标`
- `goal-round-driver → dsh-goal-round-driver — 目标自动续轮驱动`
- `command-goal → dsh-command-goal — `/goal` 命令`
- `plan-mode → dsh-plan-mode — plan 模式 + `/plan` + 用户审阅退出`

**⑨ 提示词 / 指令 / 命令 / 技能（7）**
- `system-prompt → dsh-system-prompt — 系统提示词装配（section/variable/tool 源）`
- `agent-instructions → dsh-agent-instructions — AGENTS.md/CLAUDE.md 注入（65536B 预算）`
- `skill → dsh-skill — skill provider 注册表`
- `skill-filesystem → dsh-skill-filesystem — 本地文件系统 skill provider`
- `commands → dsh-commands — 人类斜杠命令注册表（不产生模型消息）`
- `command-feedback → dsh-command-feedback — `/feedback` + sessionFeedback Remote`
- `repeat-tool-reminder → dsh-repeat-tool-reminder — 识别重复工具循环并提示模型`

**⑩ Web / 搜索（4）**
- `web → dsh-web — ctx.web（可换 provider 的搜索+抓取）`
- `web-search-deepseek → dsh-web-search-deepseek — DeepSeek 原生搜索 provider`
- `web-deepseek-api → dsh-web-deepseek-api — DeepSeek Search/Fetch API provider`
- `web-fetch-http → dsh-web-fetch-http — 匿名公网抓取（同源重定向、纯文本解码）`

**⑪ 认证 / 后台作业（3）**
- `authorization → dsh-authorization — 需要与人对话才能拿到的凭据的授权流注册表`
- `credentials → dsh-credentials-local — 文件凭据库 + 环境分层`
- `deepseek-account → dsh-deepseek-account-platform — 系统浏览器登录并保存账号凭据`

## 2. cordis service key 全表（104 个）

扫描规则：`super(ctx,'key')` 或 `static provide`（排除 `lib/client*` 当重复）。
`←` 后为提供者包:行。**同一 key 可被多个包提供**（`jobs`、`llm`、`timer`、`remote` 等）。

| 分组 | key ← 提供者 |
|---|---|
| **会话** | `sessions←dsh-session:1739` · `sessionPersistence←dsh-session-persistence:265` · `sessionProjections←dsh-session-projection:52` · `sessionProjectionCache←dsh-session-projection-cache:146` · `sessionQuery←dsh-session-query:1048` · `sessionReferenceResolver←dsh-session-reference:442` · `sessionSkillCatalog←dsh-api-session-controller:2334` · `sessionFileReferences←dsh-api-session-controller:1833` · `sessionTelemetry←dsh-session-telemetry:275` · `sessionTitle←dsh-session-title:214` · `sessionFeedback←dsh-command-feedback:132` · `sessionController←dsh-api-session-controller:2952` |
| **Agent** | `agents←dsh-agent:332` · `agentLoop←dsh-agent-loop:1572` · `agentPresets←dsh-agent-preset-registry:451` · `agentTeams←dsh-experimental-agent-team:1452` · `subagents←dsh-subagent:2992` · `subagentModelSelection←dsh-tool-subagent/lib/model-selection-settings.js:48` |
| **LLM** | `llm←dsh-llm:1816`（另 `dsh-repeat-tool-reminder:1495`） · `tokenMeter←dsh-token-meter:692` |
| **工具** | `tools←dsh-tools:2798` · `toolResultPruner←dsh-compaction-tool-result-pruner:71` · `spillStore←dsh-spill:54` · `ptcRuntime←dsh-ptc-runtime:171` · `workflowEngine←dsh-workflow:61` · `mcpResources←dsh-mcp-resources:116` |
| **文件系统** | `fs←dsh-fs:60` · `workingDirectory←dsh-working-directory:25` · `workspaceFiles←dsh-api-workspace-files:395` · `fileReferences←dsh-file-reference:58` · `attachments←dsh-attachment:207` |
| **沙箱/审批** | `sandbox←dsh-sandbox:285` · `sandboxPolicy←dsh-sandbox-policy:111` · `approval←dsh-user-approval:76` · `permissionPresets←dsh-permission-presets:170` · `authorization←dsh-authorization:64` |
| **执行** | `shell←dsh-shell:85` · `shellEnv←dsh-shell-env:49` · `subprocess←dsh-subprocess:88` · `jobs←dsh-jobs:118`（另 `dsh-api-job-controller/client.js:204`） · `terminals←dsh-terminal:58` · `terminalController←dsh-api-terminal-controller:771` · `webTerminals←dsh-api-terminal-controller/client.js:956` · `jobController←dsh-api-job-controller:319` |
| **存储** | `storage←dsh-storage:109` · `spillStore`/`sessionQuery` 见上 · `workingDirectory` 见上 |
| **配置/插件** | `settingsController←dsh-api-settings-controller:383` · `settingsSchema←dsh-client-ui-settings/client.js:926` · `configForms←dsh-client-ui-settings/client.js:1270` · `pluginManager←dsh-plugin-manager:1605` · `pluginMarket←dsh-plugin-market:375` · `pluginInventory←dsh-host-plugin-inventory:97` · `pluginPackages←dsh-app-boot:3498` · `pluginRegistryProbe←dsh-client-ui-plugin-manager:80` · `credentials←dsh-credentials:111` · `credentialsController←dsh-api-settings-controller:144` |
| **传输/类型** | `typert←dsh-typert-registry:409` · `typertGateway←dsh-api-gateway:767` · `remote←dsh-api-gateway/lib/client.node.js:1442` · `remoteAccess←dsh-api-remote-access:153` · `connection←dsh-client-connection:710` · `clientModules←dsh-client-modules:538` · `webServer←dsh-host-webserver:286` · `hmr←dsh-hmr:783` · `cordisInspect←dsh-cordis-host-runner:728` · `dynamicCordisRunner←dsh-cordis-host-runner:1585` · `timer←cordis-plugin-timer:6`（另 `dsh-cordis-client-runner/client.js:7230`） |
| **提示词/技能/命令** | `systemPrompt←dsh-system-prompt:226` · `skills←dsh-skill:132` · `commands←dsh-commands:259` · `skills`/`commands` 消费方见 23 篇 §6 |
| **UI（皆 client）** | `uiConversation←dsh-client-ui-conversation/client.js:4041` · `uiSession←dsh-client-ui-session/client.js:798` · `uiWorkspace←dsh-client-ui-workspace/client.js:1380` · `conversation←…/client.js:4643` · `commandUi←dsh-client-ui-commands/client.js:1405` · `inputTriggers←dsh-client-ui-input-trigger/client.js:1544` · `slots←dsh-client-ui-renderer/client.js:1348` · `shortcuts←dsh-client-shortcuts/client.js:2483` · `modelDirectories←dsh-client-ui-model-selection/client.js:999` · `fileUpload←dsh-client-file-upload/client.js:158` · `fileUploads←dsh-client-file-upload:167` · `workspaces←dsh-api-workspace-controller/client.js:389` · `workspaceController←dsh-api-workspace-controller:857` · `workspaceRegistry←dsh-workspace:376` · `worktrees←dsh-experimental-worktree:206` · `directoryPicker←dsh-host-directory-picker:40` · `directoryPickerController←dsh-api-workspace-controller:474` |
| **其余** | `accountController←dsh-api-account-controller:202` · `deepseekAccount←dsh-deepseek-account:36` · `deepseekLlmApiExtensions←dsh-deepseek-llm-api-extensions:44` · `officeToPdf←dsh-office-to-pdf:453` · `otel←dsh-otel:387` · `productAnalytics←dsh-client-product-analytics:323` · `productTelemetry←dsh-host-product-telemetry-otel:41` · `messageFeedback←dsh-message-feedback:168` · `userQuestions←dsh-user-questions:490` · `planMode←dsh-plan-mode:148` · `goals←dsh-goal:592` · `schedule←dsh-schedule:1992` · `web←dsh-web:57` · `compaction←dsh-compaction:171` · `speechToText←dsh-experimental-speech-to-text:18` · `speechController←dsh-experimental-api-speech-to-text:141` · `translator←dsh-experimental-translator:805` · `cotTranslation←dsh-experimental-client-ui-cot-translation:164` |

## 3. 工具注册的真实调用链

```text
插件作者:  defineTool({ name, description, parameters, output:{schema,render,presentationMeta?}, execute, finalizeContent?, timeoutMs? })
   ↓  ctx.tools.register(definition)          dsh-tools/lib/index.js:2974
注册校验:  name 必须字符串 / output 必须 {schema,render,presentationMeta?} / run_code 是保留名禁止注册  (:2977-2981)
   ↓  存入 scoped layer                      :2982  layer.tools.insert(name, definition)
请求时投影: ToolDefinition → ToolSchema = { name, description, parameters }（output/execute/finalizeContent/timeoutMs/演示回调不上 wire）
调用流水线: tools/pre-execute(可重排 allow/deny/ask) → guard(单调拒绝，后来的监听器翻不回来)
            → tools/execute → tools/post-execute → tools/result
```

| 事实 | 内容 | 证据 |
|---|---|---|
| 参数 DSL | `string/number/integer/boolean/null/array/object/json/oneOf`；编译成 JSON Schema（wire 形态） | `dsh-tools/README.md:64` |
| 结构化结果字段 | **`{ isError:boolean, content:ContentBlock[], meta?:JsonValue }`**（没有 `structuredContent` / `artifacts`） | `lib/index.js:1382-1409`（`content`、`meta`、`isError` 三处投影） |
| 规范值 vs 记录值 | `execute` 返回**规范 JSON 值**；会话只记 `render()` 后的内容 + `presentationMeta`；改内容不会改规范值 | `README.md:36` |
| 截断/溢出 | 不在工具层：`spill-policy`（token 预算 + 可读恢复文件）+ `tool-result-pruner`（超限裁剪） | §1⑦ |
| 超时 | `timeoutMs` 仅声明式，注册表**从不强制**；由 `dsh-tool-call-timeout-policy` 包装 | `README.md:235` |
| 可见性 | `ctx.tools.restrict(filter)` 收窄；被隐藏的工具连同 schema 一起从请求里消失 | `README.md:85,219` |
| 演示模式 | `mode: native`（所有可见 schema）或 `ptc`（只给 `run_code` + 生成 SDK）；`presentAs` 可按 agent 覆盖 | `README.md:68,2916` |
| run_code 默认值 | 120,000 ms，上限 600,000 ms（含嵌套工具与审批等待） | `README.md:136` |

## 4. 客户端插件的双面包协议

| 项 | 契约 | 证据 |
|---|---|---|
| 声明 | 包的 `package.json` 里 `dsh.client: { platform: 'web', inject: [...依赖的客户端包], external?: [...] }` | `dsh-client-ui-chat/package.json`；`client-modules/README.md:34` |
| 双入口 | `main: lib/index.js`（宿主半）+ `exports["./client"] → lib/client.js`（浏览器半，必备构建产物） | 同上 `exports` |
| 包体形态 | 每个 `client.js` 是**一个立即执行的 factory 注册**：`window.__ModuleLoader__.load({ id:"<包名>", factory:(require)=>{…} })`，函数体里是整包 CJS（顶部 `let x = require("@deepseek-ai/dsh-client-store")` 这类 external 请求） | `dsh-client-ui-chat/lib/client.js:1-12` |
| 惰性 | 执行 bundle **只注册 factory**；所有模块体副作用（含 CSS 注入）在 factory 闭包里，首次 import/物化时才跑，结果记忆化在 `loadCache` | `client-modules/README.md:72` |
| 解析顺序 | 平台种子表 → 已记忆记录 → 启动图 row → 已注册 factory；都不中则抛错；require 环直接抛（factory 形式 CJS 无法给部分导出） | 同上 `:72` |
| 宿主注入的 external 表 | shell 种下的**冻结** `PLATFORM_MODULES` 表；`dsh.client.external` 只能追加"非基线"的精确请求，每个必须有动态包 row 或静态表 key 供货；只 type import 会被擦除 | 同上 `:50` |
| 服务端 | 宿主半扫描启用的 Loader 条目 → 生成启动图 → 每个 bundle 经 `/plugins`（`/plugins/??a&b&rev=…` combo，超 3 KiB 再分段） | 同上 `:12,78` |
| 启动图 | `window.__DSH_BOOT__` 的 `WebBootEntry` 行，`<` 被转义；`window.__ModuleLoader__` 先作为队列 facade 注入 | 同上 `:68,84` |
| 版本闸 | 图带 `release = {version, commit}`；不同 release 的图整帧丢弃，只提示重载 | 同上 `:46` |

## 5. 系统提示词装配

| 机制 | 内容 | 证据 |
|---|---|---|
| 注册表 | `ctx.systemPrompt`：**section**（静态/上下文解析文本 + `order`）、**variable**（`{{name}}`，`interpolate:false` 可关）、**tool 源** | `dsh-system-prompt/README.md:28,56,66` |
| 排序 | 按 `order` 升序拼接，同 order 用 name 的 code-unit 序 | 同上 `:56` |
| 固定槽位 | 首方开场白 `You are an AI agent powered by DeepSeek Harness.` = **order −1000**；`personaPrefix` = 0；`personaSuffix` = **10200** | 同上 `:46-49` |
| `complete` | 某 section 标 `complete:true` 则成为完整提示词；**多于一个**则装配失败 | 同上 `:56` |
| 工具目录 | `ctx.tools` 注册的工具 schema **自动**流入装配；`toolOrder` 指定模型看到的顺序，必须恰好一个 `'<unlisted-tools>'` 占位；列了不存在的名字 ⇒ 每次 `assemble()` 都拒 | `dsh-tools/README.md:30`；`system-prompt/README.md:41,52` |
| 刷新 | `refreshContext(assembly, context)` 在 pre-step / 路由准备后刷新运行时事实，保留已接受内容 | `system-prompt/README.md:68` |
| AGENTS.md 注入顺序 | ① `$DSH_HOME/AGENTS.md` → ② `<agentsHome>/AGENTS.md`（`$DSH_AGENTS_HOME` 或 `~/.agents`）→ ③ 项目链：项目根（`.git` 标记）→ 会话 cwd，**由宽到窄**；每层 `AGENTS.md`/`CLAUDE.md` + `AGENTS.local.md`/`CLAUDE.local.md` 覆盖层 | `dsh-agent-instructions/README.md:34,38,40,67-68` |
| 注入形态 | **一条耐久 baseline 消息**（不是每轮重发）；trim 后内容相同的文件只渲染一次；cwd 变更、read/write/edit 摸到更深目录时才追加；文件消失/重复发"移除通知" | 同上 `:34` |
| 预算 | `maxBytes` 默认 **65536**，按"先丢更宽的、最后才截断最具体的"顺序降级；`maxSourceBytes` 默认 1 MiB | 同上 `:12,30,65-66` |

## 6. Go 宿主最容易踩空的 5 点

1. **激活是"服务可用性驱动"，不是行顺序**：`cordis.patch.yml:12-13` 明写。Go 端若按 yml 顺序初始化，会在 `session-persistence` 先于 `session` 时炸。
2. **patch 语义是"整块替换 config"而非 merge**（`cordis.patch.yml:6-8`），且按 `id` 后写覆盖。这是"一切皆插件"真正的可组合性来源，也是 profile 覆盖的唯一手段。
3. **客户端插件不是"再写一个包"，而是同包的 factory 注册 + 冻结 external 表**：跨包 import 必须在 `PLATFORM_MODULES` 或 `dsh.client.external` 里被显式允许，否则运行期解析失败。
4. **工具结果只有 `{isError, content[], meta?}`**：没有 `structuredContent`/`artifacts`；`data.error` 必须与 `isError:true` 同时出现（会话 v4 强制，见 21 篇 §3.4）。
5. **AGENTS.md 是一条耐久消息，且顺序由宽到窄**：`$DSH_HOME` → `~/.agents` → 项目根 → cwd。顺序反了会得到"具体规则被宽泛规则覆盖"的模型行为差异。
