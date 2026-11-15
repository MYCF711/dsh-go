# base 插件面 PARTIAL 19 条的官方形状（逐条落定）

> 口径：官方 `0.2.2-rc.2`，证据根 `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`。
> 审计基线：`tmp/GO8-RC2/_audit/plugin-face.md`（99 项逐条三档，19 条 PARTIAL）。
>
> **本文的唯一目的**：让每一条"不做 / 后置 / 由别的层承担"的 PARTIAL **落成官方形状的说明**——
> 写出官方到底是什么、本仓停在哪个层级、缺的那一层由谁承担、以及**什么时候该重新评估**。
> "没做"不是结论，"官方自己的形状就是这样"才是。
>
> **"能补"的那 12 条已经补完**（2026-11-14，`feat/host-partial19`）：实现落点、官方契约出处、
> 测试名与门禁全在 `tmp/GO8-RC2/_p19/HOST-partial19.md`。本文 §4 保留每条官方契约的**取证摘录**
> （给复核者对着官方原件读），不再表示"待做"。

## 0. 一句话分档

| 分档 | 条数 | 编号 |
|---|---:|---|
| **已补**（缺口是接线 / 少一段链路 / 少一项能力；落点见配套报告） | 12 | #1 `tool-plugin-manager`、#3 `timer`、#4 `hmr`、#6 `deepseek-llm-api-extensions`、#10 `typert-loader`、#23 `authorization`、#37 `otel`、#53 `agent-instructions`、#54 `skill`、#70 `subagent-fork-in-process`、#73 `tool-subagent-fork`、#79 `spill-policy` |
| **R6 拦**（要嵌 JS 运行时） | 1 | #75 `workflow-ptc` |
| **要用户拍板**（产品取舍 / 平台能力矩阵 / 依赖纪律 / 权限模型） | 4 | #26 `llm-pi-ai`、#29 `session-query-sqlite`、#40 `sandbox`、#43 `pwsh-sandbox` |
| **不该补**（官方自己在 Windows 上就 disabled） | 1 | #42 `bash-sandbox` |
| **不适用**（rc.1 遗留判定，rc.2 + PR #24 已闭合） | 1 | #16 `agent` |

计数自检：12 + 1 + 4 + 1 + 1 = 19，与审计的 19 条逐条对齐（#16 不改代码，只登记"已由 PR #24 闭合"）。

---

## 1. R6：`#75 workflow-ptc`

**官方形状**（`@deepseek-ai/dsh-workflow-ptc/README.md`）：

> "Run JavaScript workflows in fresh Node processes under the calling Session's file sandbox policy. Scripts keep the `agent()`, `parallel()`, `pipeline()`, `phase()` and `log()` hooks…"
> "The engine rejects non-TypeScript PTC providers when it loads."

官方实现依赖 `@deepseek-ai/dsh-ptc-runtime-node`（`dsh-base/cordis.patch.yml:408` 的 `id: ptc-runtime`），
即"在 fresh Node 进程 + VM 里跑脚本"，并把 VM 的 `syncTimeoutMs`、PTC provider 的 `maxPendingCalls`
等一套执行限额交给 Node 侧。官方自己也写明 **VM 不是安全边界**（"it is not a security boundary"）。

**本仓为什么不做**：AGENTS.md 红线 R6 明令"不嵌 JS 运行时"。这条插件的**唯一内容**就是 JS 运行时；
去掉它就只剩"六类钩子"这层宿主语义。本仓已经把这六类钩子实现成宿主原生引擎
（`internal/workflow/engine.go:130 Start`、`:258 Agent`、`:356 Parallel`、`:405 Pipeline`、
`:224 Phase`、`:237 Log`、`:34 DefaultLimits`），脚本是 JSON 操作表
（`modules/tool/workflow/orchestrate.go:3-12` 自述），并发/总量/条目上限与官方口径对齐。

**现在的形状**：`tool-workflow` 工具在（`modules/tool/workflow/workflow.go:59 Name`、`:125 Exec`），
脚本语言差异是**显式差异**而不是缺口——调 `workflow` 工具写 JS 会得到 schema 拒绝（枚举的是宿主操作表），
不会静默成功。

**什么时候重新评估**：用户在当次任务里明确解除 R6，或官方给出"非 JS 的 workflow 引擎协议"。

## 2. 不该补：`#42 bash-sandbox`

**官方形状**（`dsh-base/cordis.patch.yml:249-251`，逐字）：

```yaml
    - id: bash-sandbox
      name: '@deepseek-ai/dsh-bash-sandbox'
      disabled: !!js process.platform === 'win32'
```

同一份 patch 的 `:281-283` 把 `tool-bash` 在 win32 上也 disabled。也就是说：**官方桌面端在我们的支持平台（Windows）上，
命令级 OS 沙箱整行关掉、连 bash 工具本身都不挂**。

本仓支持范围只有 Windows（AGENTS.md §0，用户 2026-11-08 14:46 裁定）。
所以"Windows 上没有 bash 的 OS 级隔离"不是本仓的欠账，**就是官方在同一平台上的形状**。
本仓相对官方是**多**而不是少：官方在 Windows 上连策略闸门都没有（整行 disabled），
本仓的 bash 工具仍在跑三档策略闸门（`modules/exec/bash/sandbox_scope.go:21 SandboxCall`、
`modules/exec/bash/tools.go:117 sandbox.GateFromEnv`）。

**落成官方形状的说明**：见 `docs/sandbox-policy.md` §1（已改写：官方 Windows disabled 事实 + 本仓增量），
以及本文 §3 #40/#43 的 OS runner 拍板项。**Linux/macOS 一旦进入支持范围，这条要重新评估**——
那时官方的 `dsh-bash-sandbox`（bwrap/Landlock/Seatbelt）是默认开启的行。

## 3. 要用户拍板

### 3.1 `#40 sandbox` + `#43 pwsh-sandbox`——要不要做 OS 级 runner

**官方形状**：

- `id: sandbox` = `@deepseek-ai/dsh-sandbox-local`（`dsh-base/cordis.patch.yml:240-241`）。它按平台选 runner：
  Linux bwrap/Landlock、macOS Seatbelt、Windows **ACL restricted token**（`@deepseek-ai/dsh-sandbox-windows-acl`），
  一个都不行时 **fail-closed** 报 `SANDBOX_UNAVAILABLE`，绝不无约束地跑。
- `id: pwsh-sandbox` 在 Windows 上**启用**（`:255-257`），每个命令是一个 fresh `pwsh -Command`，
  经 `ctx.sandbox` 受限；结果带 `sandbox: { mode, denied }` 与 enforcement（full/partial）。
- `dsh-sandbox-windows-acl/README.md` 自述**保证是 partial**：只约束写/删（workspace + 私有 temp），
  **读、网络、进程可见性都不受限**；硬链接会绕过（"hard links alias file objects"）。

**本仓现状**：策略层完整、enforcement 诚实自报 `policy-only-no-os-isolation`（见 `docs/sandbox-policy.md` §1 的 `file:line`）。

**要做的话必须做对的三件事**（这是"先给方案"的内容）：

1. **ACL restricted token runner**：`CreateRestrictedToken`（`DISABLE_MAX_PRIVILEGE`）+ 每次执行给 workspace 与私有 temp
   挂 capability SID allow ACE + 父目录删除权 deny ACE + Low integrity label；`TMP`/`TEMP` 重写；任何
   Win32 步骤失败 → 拒绝 spawn（fail-closed），不许退回无约束执行。
2. **证据纪律**：core / 生产策略 / OS 隔离**三层分别出证据**，结果里标 `full`/`partial`，
   partial 时必须写明"读/网络/进程不受限"；`docs/sandbox-policy.md` §4 已经有这条纪律，落地时按它逐项补证据。
3. **回滚**：一个环境开关（默认关闭）就退回当前策略层；不允许"影子沙箱"和"半开着"。

**要用户决定什么**：
(a) 是否把 `policy-only-no-os-isolation` 升级到 Windows ACL partial 隔离；
(b) 若做，是否接受"只约束写/删、读与网络不受限"这个**官方 partial 口径**（不接受就得走容器/远程执行器，那是换能力不是补能力）；
(c) `read-only` 档在 ACL 下"什么都不给"是否就是产品要的语义。

### 3.2 `#26 llm-pi-ai`——要不要多协议 + pi-ai 目录

**官方形状**（`dsh-llm-pi-ai/src/provider.ts:24-52`）：四条 wire 协议
`openai-completions` / `openai-responses` / `anthropic-messages` / `google-generative-ai`，
加 pi-ai 的 provider 族 + 生成式模型目录 + OAuth/交互式 key 登录（`login.ts`、`auth.ts`，全套 4639 行 TS）。
官方 README 明确：这是"**用户设置文档**决定哪些 provider 跑"的能力面。

**本仓现状**：`internal/llm/provider.go:133 providerAdapters` 内置两条兼容协议
（`openai-completions` + `deepseek-completions`，以及账号路由 `ProviderAPIDeepSeekAccount`），
加**运行时注册面** `:139 RegisterProviderAdapter` / `:158 LookupProviderAdapter`，
加 provider 枚举/可配置面 `internal/llm/provider_registry.go:68 ListProviders` / `:79 ListConfigurableProviders`。
即"缺的是协议族与目录数据，不是机制"。

**要用户决定什么**：补齐到官方四协议是**产品取舍**（会显著扩大支持面：anthropic 的 messages 形状、Gemini 的
`generateContent`、OpenAI responses、各自的工具调用/思考预算/thought signature 兼容层）。三选一：
(i) 补齐四协议（不含 pi-ai 生成式目录，目录改成可配置的静态表）；
(ii) 只补 `anthropic-messages`（企业部署最常见）；
(iii) 维持现状（本仓定位是 DeepSeek 宿主，第三方 provider 走运行时注册面接入）。

### 3.3 `#29 session-query-sqlite`——要不要 FTS5 / 引入 SQLite 依赖

**官方形状**：`dsh-session-query-sqlite` 用 SQLite **FTS5**（Node 内置 `node:sqlite`）建一份**独立派生索引库**，
`unicode61` 分词、按 highlighted-match span 数 + 文档长度排名、游标绑定到归一化请求
（`SESSION_QUERY_STALE_CURSOR`）、`openAt: startup|first-search|never`。
**关键事实**：官方 base 组合出厂是 **`openAt: never`**（`dsh-base/cordis.patch.yml:157-161`，
注释原文："while search calls fail with SESSION_QUERY_SEARCH_DISABLED and SQLite is never opened;
the Web sidebar search matches titles and workspace names only"）。

**本仓现状**：`internal/session/query.go:130 SearchSessions` 有跨会话检索 + 真游标分页
（`:53 EncodeQueryCursor`），引擎是字面子串扫描（`:22` 自述 `Engine` 恒为 `scan`），
返回的是**官方形状的 envelope**（带 engine 字段，读方能看出是 scan 而不是 FTS5）；`go.mod` 零依赖。

**要用户决定什么**：本仓"零依赖"纪律 vs 排名检索。
(i) 引入 `modernc.org/sqlite`（纯 Go、无 CGO，但会把依赖树从 0 拉到几十个包）建 FTS5 派生索引；
(ii) 自研倒排索引（tokenizer + posting list + 排名 + 游标），不引依赖但要自己承担正确性与性能；
(iii) 维持 scan 引擎（官方出厂也是关的，能力面不低于官方）。

---

## 4. 已补的 12 条的官方契约（实现依据，逐条 file:line）

> 这一节是给实现/复核用的契约摘录；"做了什么、怎么证明、还差什么"写在
> `tmp/GO8-RC2/_p19/HOST-partial19.md`。

| # | id | 官方契约要点 | 官方证据 |
|---|---|---|---|
| 1 | `tool-plugin-manager` | `plugin_manager` 工具 8 个 action：`list_plugins`/`list_bundles`/`set_plugin`/`set_bundle`/`install_bundle`/`remove_bundle`/`list_version_exemptions`/`set_version_exemption`；参数 `action`(required)/`target`/`enabled`/`runtimeVersion`/`acceptRisk`/`approvedBuilds`/`registry`/`offset`/`limit`；`list` 默认 25 条、1..100 | `dsh-plugin-manager/src/tools.ts:88-99` |
| 3 | `timer`（`@deepseek-ai/cordis-plugin-timer`） | `ctx.timeout(cb,delay)`/`timeout(delay)`/`interval`/`throttle`/`debounce`（+ deprecated `setTimeout`/`setInterval`），句柄登记在当前 fiber，fiber 释放时自动清；interval 迭代器按请求顺序每 tick 一个 | `cordis-plugin-timer/README.md`"API" 表、`src/index.ts:1-40` |
| 4 | `hmr` | 官方 `disabled: !!js "!ctx.get('profileContext')"`；**profile 配置默认热重载，module roots 是 opt-in**（`root: []`）；配置监听只盯目标路径 + 必需父目录；安装不经该队列 | `dsh-base/cordis.patch.yml:27-32`、`dsh-hmr/README.md` |
| 6 | `deepseek-llm-api-extensions` | 注册表只做加法与生命周期；字段语义归所有者。非空的所有者之一是 `dsh_plugin_packages`：`{"version":1,"packages":[{name,version?}]}`，每次请求重读活跃条目，去重键 `name\0version`，name 再 version 排序，name-only 排前，locale 无关比较 | 包 README"Service"、`dsh-plugin-package-inventory-deepseek/src/index.ts:195-215` + `src/types.ts:10-17` |
| 10 | `typert-loader` | Loader 条目挂载 → 解析 package.json `exports["./typert"]` → import → 校验 `TYPERT`（`package`/`face:"host"`/`schemas`/`model.{services,events,objects}`/`invocations`）→ 注册进 `ctx.typert`；条目卸载即撤；无该导出静默跳过；显式 `packages` 条目解析失败要 loud | `dsh-typert-loader/README.md`"What gets registered"、"Observable behavior and failures" |
| 23 | `authorization` | `registerFlow({key,label,methods,run})`；`begin(key,{method?,interaction})` → `{status:'authorized'|'cancelled'}`；`cancel(key)`；`list()`/`describe(key)` 带 `inFlight`；错误码 `NO_FLOW`/`ALREADY_IN_FLIGHT`/`NOT_COMMITTED`/`UNKNOWN_METHOD`；notice/prompt 只回发起 surface；flow 未 commit 不许报 authorized | `dsh-authorization/README.md`"Running an attempt"、"What can go wrong" |
| 37 | `otel` | 每消费者**独立通道**（自己的 exporter/resource/instrumentation scope/queue）；`createEventReporter` 按 count 批、`createSessionLogReporter` 一条事件一条 record（`eventName:"session-log"`、`sessionId`、`content` 为 JSON 字符串、≤4,000,000 字节）；OTLP/HTTP **JSON**（`JsonLogsSerializer`）；普通通道 shutdown 取消在飞请求与重试等待；不共享请求/队列 | `dsh-otel/README.md`"Use this package"、`src/transport.ts` |
| 53 | `agent-instructions` | user-global 链（`$DSH_HOME/AGENTS.md` → `<agentsHome>/AGENTS.md`）+ project 链（project root→session cwd，broad→specific）；候选 `AGENTS.md`/`CLAUDE.md` + 覆盖 `AGENTS.local.md`/`CLAUDE.local.md`；`.git` 定项目根；组内 trim 后内容相同只渲染一次；两条链各自 `maxBytes`，宽的整文件先丢、最具体的最后截断；cwd 变更后下一次请求重载 | 包 README"What the agent gets"、"Observing the budget" |
| 54 | `skill` | `ctx.skills.registerProvider` / `register`（`runtime` 保留名，同层同名 first-wins + 警告）；`{modelInvocable,userInvocable}` 四组合；合并按层→rank→注册顺序→provider 内顺序；`get(name)` 校验返回 name 与选中候选一致，不一致使缓存失效；provider 失败记日志并跳过、observation 标 incomplete | 包 README"What the registry gives you"、"Observable success and failures" |
| 70/73 | `subagent-fork-in-process` / `tool-subagent-fork` | 子代理用父**已完成轮次**做种子，"the child sees every finished turn and none of the in-flight one"；**一次性快照**，父后续轮次不再进入子会话；首个完成轮之前种子为空 = 等同 spawn；子代理仍有全新工具作用域、不继承父权威 | `dsh-subagent-fork-in-process/README.md`"Seed boundary" |
| 79 | `spill-policy` | `maxInlineTokens`（估算 token，**省略即关闭保留策略**）；文本 + 每张图片（整留或整去）+ 描述文本 + notice **共享**一个预算；保留原始顺序、头尾各拿剩余预算一半；notice 报 omitted 字节数**与 omitted image 数**；恢复文件保留全部文本与**每张图片的可读路径**（附件字节不复制）；拿不到路径/定价/后端 → 保留原内容 + warning | 包 README"What the model sees"、"Best-effort failure behavior"、`src/notice.ts:24-27` |

## 5. 不适用：`#16 agent`

审计取的是 `cmd/dshd/serve_goal_round.go:124 agent.NewRegistry(agent.RegistryOptions{})`（`Emit` 为 nil）。
这在 rc.2 分支上**已经不是现状**：PR #24（`9ff6553` 合入）之后注册表只在 `cmd/dshd/serve.go:493` 构造一次并带 `Emit`，
生命周期边落在 `cmd/dshd/agent_events.go`，钉它的测试是 `cmd/dshd/agent_events_wiring_test.go`
（`:322` 用零值 `RegistryOptions{}` 复现"修复前的生产装配同形"，证明这条测试真的会因为 `Emit` 缺失变红）。
契约（`docs/transport-contract.md` §2 的 `agent/*` 白名单）同批更新。**本轮不动它。**
