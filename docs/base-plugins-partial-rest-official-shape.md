# base 插件面「仍在 PARTIAL」各条的官方形状说明（partial-rest 单）

> 基线：官方 `0.2.2-rc.2`（官方原件根 `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`）。
> 前情：`tmp/GO8-RC2/reports/HOST-partial19.md`（19 条 → 补齐 12 / R6 1 / 拍板 4 / 不该补 1 / 不适用 1）、
> `tmp/GO8-RC2/reports/HOST-partial-rest.md`（本单）。
> 审计：`tmp/GO8-RC2/_audit/plugin-face.md`（99 项）。
> 上一单已定掉的四条：`#26 llm-pi-ai`（不扩协议）、
> `#29 session-query-sqlite`（不做倒排）、`#40`/`#43` sandbox ACL（不做 partial 隔离）、
> `#42 bash-sandbox`（官方 win32 自己就 disabled）。它们的说明在上一个文档 §2/§3。
>
> **2026-11-21 复核（官方 rc.5）：这四条不再按"已定掉"处理。** 它们与 `#72 ptc-runtime`、`#73 workflow-ptc`、
> `#92 tool-executor-ptc` 一起**逐条重核官方源码**，归入 A/B/C 三档，见 §9。§2/§3 的原文保留为 rc.2 当轮记录。

> **2026-11-21 复核（官方 rc.5）**：本文件 §2 / §7.2 / §8.2 的 `#53 agent-instructions` 两条缺口已被后续两轮取代——
> ① fs 操作后的嵌套发现 / change·removal 批次已落地（`internal/ainstr/**`，durable 帧默认开且在 run 与 ACP 两个入口接线）；
> ② 用户全局双链的官方 `<system-reminder>` 帧形状已落地（`internal/prompt/instructions_chains.go` + `prompt.Context.InstructionsInDurableFrame`，
> 系统提示不再装配任何指令段）。现行剩余：替换型基线 intro、父/子 `exec.parent` touch 冒泡、真模型 A/B；
> 以 `docs/host-stable-gap-register.md` §3.3 的 `#53` 行与 `docs/evidence/base-plugins-per-item-0.2.2-rc.2.json` 为准。
> 本节以下的原文保留为当轮记录，不再代表现状。

本文件只承担一件事：**本轮没有收口的那几条，官方到底是什么形状、我们停在哪一层、缺的那一层
由谁承担、什么条件下重新评估**。不是"没做"，是"停在哪一站"写得能核对。

---

## 0. 一句话分档

| # | 插件 | 本轮之后 | 剩下的缺口是"实现"还是"决定" |
|---:|---|---|---|
| 6 | `deepseek-llm-api-extensions` | **仍 PARTIAL** | 一半被 `#8` 的决定锁死（`dsh_session_log` 没有所有者），一半是"不可达分支"（无 sessionId / 无 agentPresets 服务） |
| 53 | `agent-instructions` | **仍 PARTIAL**（第二根已补，见 §7） | 真实现缺口（fs 操作后的嵌套发现 + change/removal 批次；官方 reminder 帧形状） |
| 54 | `skill` | **仍 PARTIAL** | 真实现缺口（host + per-scope 分层）；而"incomplete 持久化"官方自述是**开放问题**，不是缺口 |
| 70 / 73 | `subagent-fork-in-process` / `tool-subagent-fork` | **IMPLEMENTED**（§7 补齐） | 一次性同步 fork 也建 durable 子会话；空种子仍等同 spawn |
| 37 | `otel` | **IMPLEMENTED**（本轮补齐 gzip） | 只剩 `#38` 的消费者，而 `#38` 已判结构化不做（另一个插件） |
| 79 | `spill-policy` | **IMPLEMENTED**（本轮补齐 flag + 图片可读路径） | 只剩"内存渲染图没有路径"这一条 best-effort（官方同语义，已写进代码注释） |

---

## 1. `#6 deepseek-llm-api-extensions`（仍 PARTIAL）

### 1.1 已经补齐的那一半（上一单）

注册表 + 生命周期 + 真实生产注册者：`dsh_plugin_packages` 字段由
`plugin-package-inventory-deepseek` 拥有（`cmd/dshd/serve_llm_extensions.go`，
每次 prepare 现读 Loader/state 投影，字段真的进请求体）。这一半本轮不动。

### 1.2 缺的那一半 A：`dsh_session_log` 没有所有者

官方形状（`dsh-session-log-deepseek/src/index.ts:44`、`:196`、`:250-260`）：

```
inject = ['deepseekLlmApiExtensions', 'sessions']
ctx.deepseekLlmApiExtensions.register('dsh_session_log', { prepare, accept })
// accept 在 2xx 之后写 `session-log-deepseek/delivery-accepted` 水位
```

也就是说：这个顶层字段的**唯一合法所有者**是插件 `#8 session-log-deepseek`。
本仓 `#8` 在上一单被判定为**结构化不做**（`settings` 命名空间 `session-log-deepseek`
的 `enabled=true` 回 501 `not_implemented`，理由是"本仓走用户自配的 OpenAI 兼容端点，
忠实实现会永远不触发；不忠实实现会把整份会话日志发给任意第三方网关"）。

于是 `#6` 少了一个所有者，这是 `#8` 的决定带来的**连带缺口**，不是 `#6` 自己的实现缺口。
重新评估的条件写在 `#8` 那一侧：出现"本仓自己的 DeepSeek Messages 路由"（而不是
OpenAI 兼容 wire）时，`#8` 才有真实 sink，`#6` 的这一半才跟着可做。

### 1.3 缺的那一半 B：`sessionId → preset Loader 树并集`（不可达分支）

官方（`dsh-plugin-package-inventory-deepseek/README.md:39-42`、`src/index.ts:236-247`
`inspectCompositions`）：请求带 `sessionId`、且该会话挂在一个 standing preset 上时，
把该 preset 的独立 Loader 树并进同一份库存。

本仓不可达：`internal/llm.Request` 没有 `sessionId` 字段，也没有 `agentPresets`
服务可以按请求查组合。官方对这类部署的原话是
**"deployments without the service report the host tree only"** —— 我们报的就是
宿主自己的 Loader 树，这是官方承认的合法部署形状，不是"少了功能"。
代码里已按官方原话登记（`cmd/dshd/serve_llm_extensions.go:32-40`）。

重新评估条件：`llm.Request` 增加会话身份 + 真出现"按会话挂 preset 独立 Loader 树"
的装配形状（当前 `internal/modes` 的 preset 只是模式，不带独立插件树）。

---

## 2. `#53 agent-instructions`（仍 PARTIAL）

上一单补齐了 project 链主体（root→cwd、`.git` 定根、`AGENTS.md`/`CLAUDE.md` +
`*.local.md`、组内 trim 去重、三级预算退化、`</system-reminder>` 转义、cwd 变更重载，
默认开）。剩下两条：

### 2.1 fs 操作后的嵌套发现 + change/removal 批次

官方逐条（`dsh-agent-instructions/README.md`）：

- `:34` "After a successful `read`, `write`, or `edit` call reaches a deeper directory,
  the next request includes the newly applicable instruction file; a changed file
  replaces its content, and a file that disappears or duplicates an earlier candidate
  produces a **removal notice**."
- `:168` "After a successful first-party filesystem call reaches a deeper directory,
  the next request includes one retained sourced `user/message` with the newly
  applicable instruction file."
- `:208` "Each batch of confirmed changes or removals is one retained history message …
  Provider failures add no message, and an update omitted by the budget remains
  eligible for a later filesystem touch."
- `:221` "Discovery follows structured fs tools, not shell navigation"（我们已符合这一条）
- 官方实现靠：`tools/result` touch 追踪 + durable 消息投影 + version/digest 缓存
  （`src/index.ts`、`src/state.ts`）。

本仓停在哪：`ProjectInstructions.Build` **每次装配重读**文件系统，所以"文件改了"
在下一轮自然生效、cwd 变了链也跟着变；但"read 到更深目录后**追加**新段"与
"消失/重复候选产生 **removal notice**"这两条**没有**——它们要求一份跨轮的
durable 观察状态（哪些文件在本会话里已经被渲染过、哪些这一轮确认消失）。

缺的那一层由谁承担：宿主的**会话投影 + fs 观察通道**。本仓已有
`modules/fs/local/local.go` 的观测账（`mark`/`Read`/`Write`，mtime+size）与
`internal/session/projection.go` 的投影注册表，缺的是"把 fs 成功操作接到一条
每轮的指令链增量批"这段线，并且它要落成**retained user/message**（新的 durable
事件类型），不是再拼进系统提示。

为什么本轮不做：这是"新增一条 durable 会话消息 + 新的跨轮状态"，不是接线；
改错会让历史投影与 resume 兼容性一起漂。它需要单独一单（含事件类型登记、
投影、resume reconcile、以及"预算内被略过的更新下次仍有资格"的语义）。

### 2.2 用户全局双根链（第二根）——**已在 §7 补齐**

官方逐条：

- `:34` "The first request includes one durable baseline message with the user-global
  chain — `$DSH_HOME/AGENTS.md`, then `<agentsHome>/AGENTS.md` — followed by the
  project chain …"
- `:40` "The harness home holding the user-global `AGENTS.md` is process policy …
  resolves `$DSH_HOME`, or `~/.dsh`; the shared agents root resolves the same way,
  from `$DSH_AGENTS_HOME` or `~/.agents`."
- `:74` "User-global files are therefore omitted or truncated only when they exceed
  their own budget, with the **harness-home file omitted before the shared-root file**."
- `:224` "The two user-global roots **share one group**, so the shared-root file
  collapses behind a matching harness-home file."
- `:76` 两条链的预算各自独立，一条链渲染的帧与另一条合并时最多 2×maxBytes；
  `:229` "Budget is not shared across chains"。

本仓停在哪：`Context.GlobalRulesPath` 只有**一个点**（`cmd/dshd/acp.go` 传
`<home>/AGENTS.md`），渲染用的是既有 `rulesFromFile` 形状（`全局约定（path）：正文`），
不是官方的 `<system-reminder>` 链。也就是说缺的是：第二根（agents home）、
两根本属同一候选组的 trim 去重、以及"超预算先丢 harness-home"的省略顺序。

为什么本轮不做：这三个都是**渲染形状**层面的改动。一旦按官方把用户全局链做成
reminder 链，生产提示词字节就会翻掉；而本仓有两条按字节钉住当前形状的探针
（`cmd/dshd/prompt_budget_probe_test.go:64/83` 按 `"全局约定（"` 切片、
`internal/prompt/budget_test.go` 断言全局段正文），改形状必须连它们一起改写。
按"不许删测试/放宽断言"的纪律，这属于"要改判据"的改动，得由 Lead 定口径后单独做，
不该夹在本单里顺手翻掉。（能力本身——候选组去重、预算省略顺序、多文件链——已在
`internal/prompt/instructions.go` 里，缺的确实是"第二根 + 形状选择"。）

---

## 3. `#54 skill`（仍 PARTIAL）

### 3.1 缺的那一半：host + per-scope 分层

官方（`dsh-skill/README.md:84`）：

> "The registry is **host+per-scope layered**, the shape the tools registry established:
> a registration is filed into the layer of its calling context's scope — host rows and
> repository plugins land in the global layer, **a plugin mounted by an agent preset's
> standing composition lands in that preset's layer**. A read merges the global layer
> with the viewing scope's chain; the nearest layer wins a duplicate name outright, and
> within one layer duplicates resolve by rank, provider registration order, then
> provider-local order."

本仓停在哪：上一单补齐了 provider 注册面、四组合 invocation policy、层→rank→注册序
合并（**一层**）、同层首次胜出、载入后校验。缺的是"per-scope/preset 层"——
宿主当前没有"preset 常驻组合里挂的 provider"，所以跨层 shadowing 无处发生
（`modules/tool/skill/registry.go` 的注释已写明）。

重新评估条件：出现"preset 自带 skill provider"的装配形状（`internal/modes` 的
preset 若开始承载模块组合，这一层才有第二个 layer）。

### 3.2 "incomplete 持久化"——官方自述是开放问题，不是我们的缺口

官方 `dsh-skill/README.md:66`：provider 失败要被"logged and skipped"，且
"the observation is reported **incomplete** so consumers can distinguish failed
discovery from confirmed absence"；`:94` 第二次变更返回"an incomplete, uncached
observation"；`:140` 只承诺告警与隐藏，不承诺可查询被遮蔽项。

而 `README.md:148`（Dev Note，官方自称非权威）明确把这件的归属写成**开放问题**：

> "An open question is whether the registry should **retain a last-good catalog or
> per-provider diagnostics** for failed providers, or whether **consumers should own
> that state**; the incomplete-observations limitation records the current answer."

本仓的形状正好落在"consumers own that state"这一支：`skill` 工具的 `Data.incomplete`
+ Hint、`skills.list` 观察的 `Incomplete` 字段、`modules/tool/skill/registry.go` 的
`IncompleteObservations` 计数都在。所以这一条不是"缺持久化出口"，而是官方自己也
没定的开放项；本仓的差异已经写进代码注释，不需要再造一条 durable 事件去补一个
官方尚未定义的形状。

---

## 4. `#70 subagent-fork-in-process` / `#73 tool-subagent-fork`（仍 PARTIAL）

### 4.1 已经对齐的那一半（上一单）

官方语义四条都已对齐：只取**已完成轮次**（`completedTurnPrefix`，
`src/index.ts:46-53`；最后一条 `turn/end` 且含它）、in-flight 轮不含、一次性快照、
结构化逐块保真（text/reasoning/tool-call 原样进子会话 v4 日志）。

### 4.2 缺的那一半：同步且 `Continuable=false` 时不留 durable 子会话——**已在 §7 补齐**

官方 `subagent-fork-in-process` 的前提是"fork 出来的子代理是**可续问的进程内会话**"，
所以它总有一份子会话可供后续读取。本仓 `subagent_fork` 的同步委派在
`Continuable=false` 时不留 durable 子会话——种子只在"可续问/具名委派"时可见。

这是**产品决定**而不是实现缺口：要不要为"一次性的同步 fork"也建一份会话文件，
代价是每次 fork 多一份 v4 日志（存储与索引成本），收益是"事后可回看这次 fork 看到
了什么"。官方没有这个问题，因为它的 fork 天生可续。要不要对齐，需要 Lead 定。

---

## 5. 已在代码里显式登记的 best-effort（不是缺口，但要说清）

- `#79` 的图片可读路径：`read_image` 生产者填 `spi.Image.Path`（本轮），所以
  "读图结果超预算"现在真的按预算保留、恢复文件里写真实源文件路径；
  但 **office 渲染出来的 PNG 在内存里**（`modules/tool/office/office.go` 直接
  base64 进 `Result.Images`，没有落盘路径），这类图片仍然走官方
  "The policy never substitutes an unreadable path for an image" 的兜底：
  保留原内容 + 一条 warning。官方同样只承诺"拿不到路径就不外置"，所以我们不造
  假路径，也不为此新增一条落盘通道（那会变成"为了预算再造一份附件库"）。
- `#37` 的会话日志通道：`CreateSessionLogReporter` 是真实现、有测试，但它的官方
  消费者是 `#38 session-telemetry-otel`；`#38` 已判结构化不做（`settings` 命名空间
  `session-telemetry-otel` 的 `FEEDBACK_ONLY` 回 501）。所以通道"可用但当前无生产
  消费者"是 `#38` 决定的结果，不是 `#37` 的缺口——本单不造一个假消费者去点亮它。

---

## 6. 复核命令

```bash
T="D:/DeepSeek Harness/default-workspace/DSH-GO/.agents/worktrees/host-partial-rest"
O="D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai"
# #6：官方所有者声明与我们的不可达分支登记
grep -n "dsh_session_log" "$O/dsh-session-log-deepseek/src/index.ts"
grep -n "host tree only" "$T/cmd/dshd/serve_llm_extensions.go"
# #53：官方双根链 / 预算顺序 / fs 后嵌套发现
grep -n "harness-home file omitted before the shared-root file" "$O/dsh-agent-instructions/README.md"
grep -n "the next request includes the newly applicable instruction file" "$O/dsh-agent-instructions/README.md"
# #54：官方分层 + incomplete 归属（开放问题）
grep -n "host+per-scope layered" "$O/dsh-skill/README.md"
grep -n "consumers should own that state" "$O/dsh-skill/README.md"
# #70/#73：官方 completedTurnPrefix
grep -n "completedTurnPrefix" "$O/dsh-subagent-fork-in-process/src/index.ts"
```

---

## 7. `feat/host-partial5` 之后的现状（2026-11-14，本单补齐 2 条 + 补齐 1 条的半条）

上一单之后只剩 5 条"还能补"，本单逐条核实的结果如下。**状态以逐项表
`docs/evidence/base-plugins-per-item-0.2.2-rc.2.json` 为准**（99 条：86 IMPLEMENTED /
9 PARTIAL / 4 MISSING），本文件只解释"为什么"。

| 插件 | 本单结论 | 依据 |
|---|---|---|
| `#70 subagent-fork-in-process` | **IMPLEMENTED** | §7.1 |
| `#73 tool-subagent-fork` | **IMPLEMENTED**（与 #70 同一处改动） | §7.1 |
| `#53 agent-instructions` | **仍 PARTIAL**（用户全局第二根已补；剩两条真缺口） | §7.2 |
| `#54 skill` | **仍 PARTIAL**（无 preset 级 provider = 没有第二个 layer 可分层） | §7.3 |
| `#6 deepseek-llm-api-extensions` | **仍 PARTIAL**（被 `#8` 锁住，不可单方面补） | §7.4 |

### 7.1 `#70`/`#73`：一次性同步 fork 也留 durable 子会话

缺口原文（§4.2）：同步委派在 `Continuable=false` 时不留子会话，fork 的种子只在
"可续问/具名委派"时才有落点。

官方形状（`dsh-subagent-fork-in-process/README.md` "What a fork delegation does"）：
"One tool call creates a background child … **The child works in its own session**"；
`src/index.ts:72-78`：种子创建时取一次，之后 "becomes part of the child's own durable
transcript"；"Seed boundary"：首轮完成前种子为空，"behaves like a fresh spawn"。

补齐方式（不是新造机制，是把已有的落盘路径接上）：`cmd/dshd/subagent.go` 的 `runSub`
里，落 durable 子会话的判据从 `Continuable || AgentID != "" || ChildSessionID != ""`
扩到 **`|| len(req.Seed) > 0`**——`Seed` 非空就是"这次 fork 真的继承了父会话的已完成
轮次"，那批事件必须有子会话可落（`agent.ChildSpawn.Spawn` 本来就会把 seed + 这一趟
问答写成子会话自己的 v4 日志）。两个边界照官方语义保留：

- **空种子不留会话**（首轮完成前的 fork = fresh spawn），既有行为逐字节不变；
- **种子落不了盘时响亮失败**（错误文案点明丢的是 fork 种子），不静默成功。

证据：`cmd/dshd/subagent_fork_durable_test.go`（真 v4、真 `subRunner`、真 `ChildSpawn`：
父子帧归属、种子逐条同序、种子不含 `turn/*` 边界、这一趟问答在种子之后；空种子零新增；
无 home 时响亮失败）。

### 7.2 `#53 agent-instructions`：第二根补齐，仍缺两条

**已补**（§2.2 原文列出的三件——第二根、同组 trim 去重、省略顺序——全部补上）：`prompt.Context` 增加 `SharedGlobalRulesPath`
（`$DSH_AGENTS_HOME/AGENTS.md` → `~/.agents/AGENTS.md`），`GlobalRules.Build` 按官方
候选组语义渲染两根：同组 trim 内容相同则 shared-root 塌在 harness-home 后面
（README.md:224）、两根合起来超预算时 **harness-home 先整份被丢**再截断留下的那根
（README.md:74）。接线在 `cmd/dshd/acp.go` 的 `buildSystemPromptWithHome`
（`resolveSharedGlobalRulesPath`）；没有 harness home 的装配不解析第二根，保证测试
提示词不随跑它的机器漂移。证据：`internal/prompt/global_chain_test.go`、
`cmd/dshd/global_chain_wiring_test.go`。

**仍缺**（所以维持 PARTIAL）：

1. **fs 操作后的嵌套发现 / change·removal 批次**（§2.1 原文）：要新增一条 durable
   `user/message` 事件类型 + 跨轮观察状态 + 投影与 resume reconcile，不是接线。上一单
   的判断仍然成立：改错会让历史投影与 resume 兼容性一起漂，必须单独一单。
2. **用户全局链的官方帧形状**：官方把两根与 project 链合并进**同一条**
   `<system-reminder>` 帧、且每链一份独立预算（README.md:74-76）。本仓两根渲染在既有
   `全局约定（path）：正文` 块里——换成官方帧会让生产提示词字节整体翻掉，而
   `cmd/dshd/prompt_budget_probe_test.go:64/83` 与 `internal/prompt/budget_test.go`
   按字节钉着现形状。按"不许删测试/放宽断言"的纪律，这属"要改判据"，
   得由 Lead 定口径后单独做（本单**没有**为了变绿去改那些探针）。

### 7.3 `#54 skill`：仍然 PARTIAL —— 官方那个层有真实生产者，我们的宿主表达不出那个生产者

> 2026-11-21 按 **rc.5** 复核（官方根 `tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/`）。
> 本节旧版引的是 rc.2 行号（`src/index.ts:420-440`），rc.5 已位移；下面每个锚点都按 rc.5 重核过。

**官方结构（rc.5，逐层）**

| 层 | 谁往里注册 | 出处 |
|---|---|---|
| 全局层 `layers.global` | 宿主行与仓库插件。base 平面把 `skill` / `skill-filesystem` / `tool-skill` 三行作为**宿主行**插入 profile root；Web 平面的 `skill-office`（bundled provider）也在宿主面 | `dsh-skill/src/index.ts:362-365`（`new ScopedLayers(...)`）、`dsh-base/cordis.patch.yml:312-319`、`dsh-web-app/cordis.patch.yml:308-311` |
| 每个 `ScopeKey` 一层（preset 层） | **preset 常驻编排挂的插件**：`agentPreset.plugins` 是必填行数组，注册时 `createScope(owner, key)` 铸 scope，`mountPreset(ctx, id, plugins)` 把行挂进该 scope ⇒ 里面的 `ctx.skills.registerProvider` / `register` 全部落这一层 | `dsh-agent-preset/src/index.ts:22`、`dsh-agent-preset-registry/src/index.ts:121,126`、`dsh-skill/src/index.ts:411-421`（`registerProvider` 走 `layers.effect(this.ctx, …)` 定层）、`:441`（`register` 用 `scopeOf(this.ctx)`） |

**覆盖规则两条，别混**

- **跨层**：读时 `[global, ...chainLayers(scope)]`，逐层 `merged.set(name, entry)` 后写覆盖
  ⇒ **最近层整条盖掉远层，rank 不参与**（`dsh-skill/src/index.ts:551-565`，`:556` 拼层、`:562` 覆盖）。
- **层内**：先按 `(rank, providerOrder, localOrder)` 排序再 first-wins（`compareIndexedCandidates`
  `:807-811`，`collectLayer` `:567-586`）。runtime 内嵌 skill 固定 `RUNTIME_RANK=250`
  且 `providerOrder=-1`（`:25`、`:700`）；文件系统 rank 100/200/300/400/500/600
  （`dsh-skill-filesystem/src/index.ts:35-39` + `dsh-skill/src/index.ts:28` 的 `BUNDLED_SKILL_RANK`）。

**这一层在官方出厂装配里不是摆设**：base 平面把 `skill-filesystem` 当宿主行
（`dsh-base/cordis.patch.yml:315-316`），而 **Web 平面把宿主这两行关掉、把本地发现整体交给 preset** ——
原文 `dsh-web-app/cordis.patch.yml:542-549`「the base host `skill-filesystem` row is disabled here
(presets own local discovery)」，紧接 `:551-555` 两行 `disabled: true`。所以官方 Web 下
"有哪些本地 skill"是**每个 preset 各自的 scope 结论**（`scope` 缺省时只读全局层）。

rc.5 出厂四个 preset 文件就是这条机制的实证（`dsh-web-app/presets/`）：
`standard.patch.yml:40-46`（`skill-filesystem` + `customSkillDirs` 43-44、`tool-skill` 45-46）、
`ptc.patch.yml:40-43`、`cordis.patch.yml:126-132` 各自挂这两行；
**`minimal.patch.yml` 一行都没有（全文 `skill` 零命中）** ⇒ 官方出厂下 `minimal` 预设的 agent
既没有本地 skill 目录，也没有 `skill` 工具 = **一个 skill 都看不到**。分层在这里是**被用来省掉一整类能力**，
不是纯形状差异。

**我们的结构**

- `modules/tool/skill/registry.go:49-57` 文件头自述：刻意只实现**全局层**，跨层 shadowing 留空；
  `:637-640` 的 `collect` 只做一层，层内键序与官方**逐字同序**（rank → providerOrder → localOrder）。
- 根集合 4/6 已映射（`cmd/dshd/serve_agent_api_skills.go:44-52`）：project-dsh 100、project-agents 200、
  custom 300、user-dsh 400；`user-agents`(500) 未映射、`bundled`(600) 走物化进 `<home>/skills`
  （属 `#55 skill-filesystem` 的口径，不在本节改判）。
- 生产装配只有**一个** provider（文件系统）：`cmd/dshd/serve_agent_api_skills.go:317`、
  `modules/tool/skill/skill.go:157`。
- preset 于我们 = tools×approval×sandbox（`internal/modes`）；`internal/agentpresets/yaml.go:1-20`
  与 `cordis.go:1-8` **只把官方 `read` 载荷渲染成 YAML 给客户端看，不装载任何插件**
  ⇒ 官方那个层在我们这里**没有生产者**（`grep -rn "ScopedLayers\|ScopeKey" --include=*.go` 仍只命中注释）。

**逐条差异**

| # | 官方 | 我们 | 性质 |
|---|---|---|---|
| 1 | 跨层：最近层整条盖远层 | 无层，只按 rank | **形状差异**；无生产者时不可观测 |
| 2 | preset 层由 preset `plugins` 行生产：出厂 `standard`/`ptc`/`cordis` 挂 filesystem，**`minimal` 一行不挂 ⇒ 看不到任何 skill** | preset 不装插件，`plugins` 只出现在展示载荷里；**所有 preset 看同一份目录** | **能力差异**（官方能按 preset 加/减 skill；我们只能"都有"——按能力下限规则"更多"不算欠账，但"按 preset 减"确实做不到） |
| 3 | 层内 `(rank, providerOrder, localOrder)` first-wins | 同键序、同 first-wins，只在 global 层内 | **等价** |
| 4 | 收集缓存 `collectCacheMaxEntries=128`（`:22`/`:358`/`:542-544`） | 缓存在 consumer（`DSHD_SKILLS_CACHE_TTL_MS` 等） | 已登记等价（`registry.go` 文件头末段） |

**代价表（铁律 13：逐条回答"有没有人要为这个改动付什么"）**

| 问题 | 答案 |
|---|---|
| 解析顺序会变吗？ | **只建层、不加生产者 → 不变**（每层只有 global 非空）——但那正是**零收益空壳**，仓规明令不做。要让层真有东西必须同时补 preset 插件装配（把本地发现搬进 preset 层），那时：① **按 preset 增减技能**成为可能，照官方出厂对齐就等于让 `minimal` 类预设**看不到任何本地 skill**（官方 `minimal.patch.yml` 现状，`dsh-web-app/presets/`），而这正是用户今天能看到的技能 ⇒ **已装技能的可见性会变**；② 同名解析从"全局 rank 最小"变成"近层整条盖"，preset 若挂 `includeDefaultRoots:false` 的自定义目录，用户项目级/用户级 override 会被 preset 层同名项盖掉。**⇒ 照官方形状补 = 有代价，且落在已装技能的可见性与优先级上。** |
| 会变慢吗？ | **会**。官方每 scope 一份收集缓存（`collectCacheMaxEntries` 默认 128，`:22`/`:358`/`:542-544` 就是为此存在），冷启动按 preset 数 × 根数重复扫盘；我们今天是 1 次全量扫盘 + consumer 侧 TTL 缓存。 |
| 会多出用户可见的设置项/提示吗？ | **会**。要让用户真正用上，preset 得长出 `plugins` 行 / `customSkillDirs` 字段（官方 preset 装载失败还会告警 `agent preset <id>: <problem>`，`dsh-agent-preset-registry/src/index.ts:134-135`），那是新的用户可见配置面与错误面。 |

**结论：不补，维持 PARTIAL。** 纯分层 = 空层（违反"不许空壳"、零收益）；照官方形状补 = 三条代价里
第一条直接改变已装技能的可见性/优先级 ⇒ 铁律 13 判"有代价 ⇒ 不做"。**不是账本记错**：官方 Web 出厂
确实按 preset 分视图，我们确实做不到；只是今天没有任何我们能表达的装配形状能把这个差异显出来。
**重新评估条件（比旧版更具体）**：出现"宿主真能把 preset 的插件行装载起来"（`internal/agentpresets`
从渲染升级为装配，或外部 Cordis 运行时接进 preset 作用域）时，`skill` 与 `agent-preset*` 一起做，
按官方 `dsh-scope/src/store.ts:159-216`（`ScopedLayers` 的 `global`/`peek`/`chainLayers`/`merge`）补层。
"incomplete 持久化"仍是官方 README:148 自述的**开放问题**（"consumers should own that state"），
本仓落在 consumer 侧，不是缺口。

### 7.4 `#6 deepseek-llm-api-extensions`：被 `#8` 锁住，不可单方面补

残余缺口 A（§1.2）：`dsh_session_log` 这个顶层字段的**唯一合法所有者**是插件
`#8 session-log-deepseek`（官方 `dsh-session-log-deepseek/src/index.ts:44` 的
`ctx.deepseekLlmApiExtensions.register('dsh_session_log', …)`）。本仓 `#8` 已判
**结构化不做**（settings 命名空间 `session-log-deepseek.enabled=true` 回 501
`not_implemented`，理由是没有本仓自己的 DeepSeek Messages sink）。所以这一半**不是
`#6` 自己能补的**：替它造一个注册者就是给一个不存在的 sink 编 payload（假数据），
本单**不做**。重新评估条件仍写在 `#8` 那侧：出现"本仓自己的 DeepSeek Messages 路由"
（而不是 OpenAI 兼容 wire）时，`#6` 这一半才跟着可做。

残余缺口 B（§1.3）是不可达分支，官方自己也承认这种部署合法（"deployments without the
service report the host tree only"），维持原判。

---

## 8. 2026-11-15 复核（残余缺口 + 已知缺陷单 `feat/host-residual`）

**本单没有改任何插件状态**：口径仍是 99 = **86 IMPLEMENTED / 9 PARTIAL / 4 MISSING**，
方法面仍是 223 = **220 / 3 / 0**。下面只是把"还能不能补"的三处判断写到能核对。

### 8.1 `#54 skill`：判定为**依赖锁定**（不是"再努一把就能补"）

**问：官方到底有没有第二级（per-scope）层？有，而且是独立机制，不是可选优化。**
逐行取证（官方 rc.2 解包树 `.../@deepseek-ai/dsh-skill/src/index.ts`）：

- `:345-353` 类注释："Layered registry … **host+per-scope shape** … host rows and
  repository plugins land in the global layer, while **a plugin mounted by an agent
  preset's standing composition lands in that preset's layer**. A read merges the
  global layer with the viewing scope's chain — **the nearest layer's entry wins**"
- `:362` `private readonly layers = new ScopedLayers<SkillLayer>(scope => new SkillLayer(scope), …)`
  —— 层不是"多一层去重表"，是 `@deepseek-ai/dsh-scope` 的 scope 链（`:16` import
  `ScopedLayers, scopeChainOf, scopeOf`），`list/get` 都带 `scope`（`:117`）。
- `:381-386` 注册落层：scoped context 落该 scope 的层、无 scope 落 global、
  **同层**重名才抛错。

**本宿主缺的不是 `skill` 的实现，是"preset 承载组合"这个上游生产者**：

- `internal/modes/modes.go:16-30`：preset（本仓叫 mode）的全部字段是
  `ID / Name / Description / Builtin / Tools`（工具白名单）`/ Approval / Sandbox`——
  **没有模块/插件/provider 组合轴**，所以"preset 常驻编排里挂的 provider"在本仓
  无处产生；
- `cmd/dshd/serve_agent_api_skills.go:88-93`：生产装配只挂**一个** filesystem
  provider（`skill.Registry` 的全局层），`Registry.RegisterProvider` 真调用者是
  它自己（`:317/:327`）与内置文件系统（`modules/tool/skill/skill.go:157`）——
  都在全局层。

**结论（依赖锁定）**：把 `ScopedLayers` 等价物建出来，本仓不会有任何注册者，
层恒为空——那正是"建层就是空壳"，本单**不做**。解锁条件（可验收）：`internal/modes`
的 preset 开始承载 provider/模块组合（即出现"某个 preset 自带 skill provider"的装配
形状）；届时按官方 `scopeChainOf` 语义补，并让 `skills.list` 与 `skill` 工具同时按
scope 读。`incomplete` 持久化仍是官方 README:148 自述的开放问题，**不是**本仓缺口。

### 8.2 `#53 agent-instructions`：两条剩余缺口的"卡在哪"不变（维持 PARTIAL）

复核 §7.2 之后没有新证据推翻原判：

1. **fs 操作后的嵌套发现 / change·removal 批次**——需要新增一条 durable
   `user/message`（官方 README:34/:168/:208 "one retained sourced `user/message`"）
   + 跨轮观察状态 + 投影与 resume reconcile；本仓 `Build` 每次重读能覆盖"文件改了"，
   但覆盖不了"这一趟 read 到了更深的目录"与"候选文件消失要发 removal notice"。
   这不是接线，是新增事件形状，必须单独一单（改错会让历史投影与 resume 兼容性一起漂）。
2. **用户全局链的官方帧形状**——官方把两根与 project 链合并进**同一条**
   `<system-reminder>` 帧、每链独立预算（README.md:74-76）；本仓两根渲染在既有的
   `全局约定（path）：正文` 块里。换帧会整体翻掉生产提示词字节，而
   `cmd/dshd/prompt_budget_probe_test.go` 与 `internal/prompt/budget_test.go` 按字节
   钉着现形状 ⇒ 属"改判据"，需 Lead 定口径后单独做（本单**没有**为了变绿去动那些探针）。
   能力侧（两根、同组 trim 去重、harness-home 先丢、每链预算）已齐，缺的只是帧形状。

### 8.3 方法面那 3 条 `STRUCTURED_UNIMPLEMENTED`：**保持现状**，且这是正确形态

`account.getNotifications` / `account.getNotificationContent` /
`account.reportNotificationEvent`（`internal/transport/handlers_account_official.go:602/604/606`）
在默认 `provider=Local`（`cmd/dshd/serve_account_provider.go:24`）下回**四字段 501**，
message 里点名缺什么与切换方式（`internal/account/notifications.go:146-156` 的
`noNotificationBackend`：官方通知只来自 harnessOrigin 的
`/api/v0/users/notifications/{list,content,events}`，即 DeepSeek Platform）。

**为什么不补**：本地没有通知后端，唯一的"实现"办法是**在本地伪造一条通知流**——
那是假数据；而这条路径在 `DSHD_ACCOUNT_PROVIDER=official` 下是**真实现**
（`cmd/dshd/serve_account_api.go:134-180`，真打云端）。所以它不是 MISSING、也不是待补，
是"默认部署没有这个后端 + 说清怎么切"。要把它变成 IMPLEMENTED，前提是出现"本仓自己
的账号/通知服务"，属产品取舍，不在本单。

---

## 9. 2026-11-21 复核（官方 rc.5）：剩余 8 条 PARTIAL 的三档裁定

> **官方源码根**：`tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/`（**rc.5**）。本节每条官方
> `file:line` 都在那棵树里重核过，与 §7/§8 引的 rc.2 行号不同属正常位移，不等于行为变了。
> **三档口径**：**A 已等价**（能力其实已到位，账本记录过时／误判 → 只改说明）／
> **B 不该补**（缺口是真的，补它有代价 → 写清谁付什么 + 重新评估条件）／
> **C 真缺且零代价**（该补 → 本单**只标注**，不动手）。
> **status 一律不动**：99 = 89 IMPLEMENTED + 10 PARTIAL + 0 MISSING。本单不改代码、不改测试、不动 golden，
> 唯一动作是改账本说明。

### 9.0 结论（8 行）

| # | 插件 | 档 | 一句话依据 |
|---:|---|---|---|
| 26 | `llm-pi-ai` | **B** | 官方出厂 **dormant**（零路由）；差额是 pi-ai 的 **7 条 wire 协议 + 44 条 catalog provider + OAuth 登录**（`dsh-llm-pi-ai/src/catalog.ts:338-346`、`src/provider-data.generated.ts:9-50`、`src/login.ts:1-6`）。补它＝重写整个第三方协议生态；用户 2026-11-15 已拍板不扩 |
| 29 | `session-query-sqlite` | **B** | 官方 base **和** web 两层 patch 都是 `openAt: never`（检索报 `SESSION_QUERY_SEARCH_DISABLED`，`src/index.ts:334-342`），我们 scan 档出厂**更强**；差额只在官方非默认 opt-in 的 FTS5（`src/schema.ts:126-127`）。补它＝零依赖宿主引入 SQLite；用户已拍板不做 |
| 40 | `sandbox` | **B** | 官方 Windows **默认**就真隔离：`id: sandbox` 绑 `dsh-sandbox-local`、`win32: ['windows-acl']`（`dsh-base/cordis.patch.yml:240-247`、`dsh-sandbox-local/src/index.ts:184`）。我们只有宿主自有受控根 + 默认策略层；补它＝对用户**真实工作区**落驻留 ACE/Low（官方 `README.zh.md:130-131`：`icacls` 回收不了/1332，且对别家 Low 进程放宽） |
| 42 | `bash-sandbox` | **A** | win32 官方该行 `disabled: true`（`dsh-base/cordis.patch.yml:251`）**且** `tool-bash` 也 disabled（`:281-283`）。我们是 Windows-only ⇒ 本平台**零行为可对齐**；我们反而还有三档闸门 + 官方 escalation（多不少） |
| 43 | `pwsh-sandbox` | **B** | win32 官方**默认开**（`:255-256`），standard/cordis/ptc 的 `tool-pwsh` 把每条 pwsh 送进 ACL runner（`dsh-pwsh-sandbox/src/index.ts:1-8`；**minimal 例外**：换 `dsh-tool-pwsh-persistent` 走 PTY，`src/index.ts:1-6` 对 `sandbox/confine/ctx.shell` 零命中）。我们探针不通就退回策略层，生产 `enforcement` = `policy-only-no-os-isolation`；补它＝同一份驻留 ACL（+ 受控令牌下的控制台限制） |
| 72 | `ptc-runtime` | **B** | 契约已全、per-call + vm-script 都在（官方默认也是 `per-call`，`dsh-ptc-runtime-node/src/config.ts:10-19`）。差额 3 条：REPL 形态、TLA lowering、**程序进程没有文件 confinement**（官方 `src/worker.ts:287` 把 argv 送进 `ctx.sandbox.confine`）。前两条没有消费者（补了是空壳），第三条与 #40 同价 |
| 73 | `workflow-ptc` | **B** | 官方 Web 把宿主行关掉、按 preset 挂（`dsh-web-app/cordis.patch.yml:598`、`presets/standard.patch.yml:111-116`），脚本是 JS 跑在 PTC node 里（`README.zh.md:11`）。我们 Go 原生引擎语义齐，但脚本是 JSON 操作表、`run_in_background` 明确拒绝（`modules/tool/workflow/workflow.go:46`） |
| 92 | `tool-executor-ptc` | **B** | 官方 base `disabled: true`（`:522-523`）、只在 `ptc` 预设开（`presets/ptc.patch.yml:127-128`）；一旦开就**取代**原生执行器（只 `run_code` 可直接调 + 生成 SDK 段 + `tool/ptc-dispatch` 事件 + `reset`）。我们是加法（并列、无 SDK 段），补它＝改模型可见工具面与提示字节 |

**合计：A 1 条（#42）、B 7 条、C 0 条。**（另有 1 个 **C 子项**附在 #92 下，见 §9.4 —— 那是文案与装配不符，不是能力缺口。）

### 9.1 官方出厂到底开不开（rc.5 装配矩阵）

| id | `dsh-base/cordis.patch.yml` | `dsh-web-app/cordis.patch.yml` | 四个 preset 的 patch |
|---|---|---|---|
| `llm-pi-ai` | **dormant 挂载** `:135-136`；注释 `:128-134`（"mounted dormant: zero routes … until a `llm-pi-ai:` settings section supplies provider profiles"） | 无覆盖 | 无 |
| `session-query-sqlite` | `path: ':memory:'` + `openAt: never` `:157-161`；注释 `:149-156`（search → `SESSION_QUERY_SEARCH_DISABLED`，SQLite 从不打开） | 同值重申 `:27-30` | 无 |
| `sandbox` | `:240-241` 绑 `dsh-sandbox-local`；`sandbox-policy` `:243-247`（`mode: DSH_PERMISSION_MODE ?? 'workspace-write'`、`workspaceRoot: process.cwd()`） | 无覆盖 | 无 |
| `bash-sandbox` | `:249-252`，**win32 → disabled**（`:251`） | 无 | 无 |
| `pwsh-sandbox` | `:255-256`，**非 win32 → disabled**（`:256`，即 Windows 上默认开） | 无 | 无 |
| `ptc-runtime` | `:407-408` 绑 `dsh-ptc-runtime-node`，**无 config**（全部取默认：`executionMode: per-call`、`scriptPersistence: false`） | 无覆盖 | 无 |
| `workflow-ptc` | `:410-411` **开**（`config.provider: spawn`） | **`:598` `disabled: true`**（宿主行关掉） | standard `:111-116` 开、cordis `:107-112` 开、**ptc `:108-116` 自己又 `disabled: true`**、minimal **零命中** |
| `tool-executor-ptc` | `:522-523` **`disabled: true`** | 无覆盖 | 只有 `ptc.patch.yml:127-128` 开 |

> #42/#43 的定性还依赖同批两行：`tool-bash` 在 win32 也 disabled（`:281-283`）、`tool-pwsh` 只在 win32 开（`:285-287`）。
> 结论：**官方 Windows 用户在任何 preset 里都没有 bash 工具**（standard/ptc/cordis 的 `tool-bash` 带 win32 disable；minimal 的 `terminal-bash` 与 `persistent-bash` 同样 `disabled: platform === 'win32'`，`presets/minimal.patch.yml:42-51`）。pwsh 侧 standard/ptc/cordis 的 `tool-pwsh` 被 ACL runner 包着，**minimal 例外**（`persistent-pwsh` 是 PTY 工具、不经过 `ctx.sandbox`，`minimal.patch.yml:66-74`）。我们两个工具都挂（`cmd/dshd/main.go:152`、`:185`）。

### 9.2 逐条（官方是什么／出厂开不开／我们是什么／差值／代价）

#### 9.2.1 `#26 llm-pi-ai` —— **B 不该补**

- **官方是什么（rc.5）**：一个**通用 pi-ai 适配器**，一个实例持有 provider 路由字典（`src/index.ts:1-10`）；路由要么命名 pi-ai 已装的 provider（继承端点/协议/模型目录），要么手写声明（`:13-52` 的 YAML 示例含 `api`、`compat`、`models`、`retryPolicy`）。
  - 协议面 **7 条**：`src/catalog.ts:338-346` `COMPAT_GATES` 的键 = `mistral-conversations`／`openai-completions`／`openai-responses`／`azure-openai-responses`／`openai-codex-responses`／`anthropic-messages`／`bedrock-converse-stream`。
  - provider 目录 **44 条**：`src/provider-data.generated.ts:9-50`（生成件，每个带 env 名单与 `defaultBaseURL`；anthropic/google/openai/groq/mistral/bedrock… 都在）。
  - 认证：`src/login.ts:1-6`（把 harness 的 notice/prompt 词汇翻成 pi-ai 的 `AuthInteraction`）、`:20-25`（`openai` 的 ChatGPT 登录需要安装 UUID，官方明确不提供 → 该路由只能用 API key）。
  - 生命周期：`src/index.ts:310-312` 原文"A bare mount (zero routes) is the dormant posture: nothing registers until a settings section supplies profiles"；`:280` 注册可配置 provider 目录、`:305` 注册模型发现。
- **出厂开不开**：**dormant**。base 挂载但零路由（`:128-134`），web/preset 不覆盖。**但 dormant ≠ 用户碰不到**——同一条注释写明"Supplying those profiles is exactly what the web Models page does"：用户在 Models 页加一个 provider，路由就 live。
- **我们现在**：`internal/llm/provider.go:133` 三条内置 wire（`openai-completions`、`deepseek-completions`、`deepseek-account`）、`:139` 可运行时注册、`:158` 未知 api fail-loud 不回落；`internal/llm/provider_registry.go:71` `ListProviders`、`:82` `ListConfigurableProviders`、`:136`/`:146` 两条 `DiscoverModelsForProvider*`。`internal/llm/model_catalog.go:1276-1278` 把 `model.api` 当闭合集合校验 ⇒ `api:"anthropic-messages"` 直接 `invalid_field`（负例 `internal/llm/model_catalog_test.go:156`）。
- **差值**：出厂装配**不比官方少**（官方 dormant 零路由，我们 3 条内置）。差在"用户要声明非 OpenAI 协议的路由"这一刻：官方 7 协议 + 44 provider + OAuth 登录，我们 3 条、其余当场判非法。
- **代价（谁付什么）**：① **用户可见配置面变大**——Models 页要长出协议选择、每协议的 `retryPolicy`/`compat`/`models` 字段与 OAuth/device-code 登录失败面（官方 `src/config.ts:105-261` + `src/login.ts` 就是这个面）；② **工程面**：7 条 wire 的请求/流式/工具调用/错误映射逐条重写 + 44 条目录跟进，是本批 8 条里最大的一件；③ 默认装配零变化（不付延迟、不付字节）。**⇒ 有代价。** 且用户 2026-11-15 已拍板不扩四协议（`docs/handoff-2026-11-15.md:53`）。
- **重新评估条件**：用户明确要在本宿主里直接吃 Anthropic/Gemini/Bedrock 这一族端点；或出现可复用的 Go 侧多协议 SDK（届时只接线、不重写 wire）。

#### 9.2.2 `#29 session-query-sqlite` —— **B 不该补**

- **官方是什么（rc.5）**：SQLite FTS5 全文索引。`src/index.ts:88-89` `OpenAt = 'startup' | 'first-search' | 'never'`（**包自身默认 `startup`**，`:208`）；`src/schema.ts:126-127` `CREATE VIRTUAL TABLE … USING fts5(...)`；`openAt: never` 时 `src/index.ts:334-342` 在参数归一化与任何 SQLite 工作之前就拒绝全文调用，错误码 `SESSION_QUERY_SEARCH_DISABLED`；规模 `index.ts` 1131 / `query.ts` 477 / `schema.ts` 173 行。
- **出厂开不开**：**关**。base `:157-161` + web `:27-30` 都是 `path: ':memory:'` + `openAt: never`；base 注释 `:149-156` 写明：`ctx.sessionQuery` 仍挂载（精读/标题/血缘可用），只有 search 报错，**Web 侧边栏只按标题与工作区名匹配**；要做内容检索的部署在后续 patch 层把 `openAt` 覆盖成 `first-search`/`startup`。
- **我们现在**：`internal/session/query.go:28-29` `QueryEngineScan`（唯一引擎，自述"不是 FTS5"）、`:130` `SearchSessions`（跨会话子串扫描、游标分页、`Hidden` 标注压缩遮蔽、`ScannedSessions` 把成本暴露出来）、`:53` `EncodeQueryCursor`；页大小默认 20 / 上限 100（`:31`/`:33`，与官方逐字一致）；`go.mod` 无 sqlite 依赖。
- **差值**：**出厂我们更强**（官方默认 search 直接报错，我们有可用的跨会话检索）。差额只在"官方非默认 opt-in 下的 FTS5"：索引、分词、排名/相关度、前缀查询、过滤。
- **代价（谁付什么）**：① **依赖**——零依赖 Go 宿主引入 SQLite（CGO 或大体积纯 Go 引擎），与 `CGO_ENABLED=0` 的构建/门禁、二进制体积/内存是硬约束；② **结果可见变化**——今天的子串扫描顺序与 excerpt 会变成 FTS5 的排名/分词结果，老用户看到的东西变了；③ **索引面**——派生索引的建/重建/迁移/损坏回退全要新写（官方三文件 1781 行就是这个量级），且要守住 R5「读路径不写盘」的边界。
- **重新评估条件**：`ScannedSessions` 观测到跨会话扫描成为真实卡顿点，且出现体积可接受的纯 Go FTS（或用户接受 CGO）。

#### 9.2.3 `#40 sandbox` —— **B 不该补**

- **官方是什么（rc.5）**：**真实的 Windows OS 隔离**。`dsh-sandbox-local/src/index.ts:184` 的 win32 链是 `['windows-acl']`，无链 fail-closed（`:100-117`、`:173`）；runner 用 `WRITE_RESTRICTED` 受限令牌 + Low 完整性 + 每工作区 SID 的 ACE + 每会话私有 temp SID（`dsh-sandbox-windows-acl/src/index.ts:4-28`、`:263-264`），`enforcement` 自标 **partial**（`sandbox-local/src/index.ts:206`），语义是"写入与删除受限；读取、网络与进程可见性不受限"（`dsh-sandbox-windows-acl/README.zh.md:127`）。
- **出厂开不开**：**开**（默认装配，所有平面）。`dsh-base/cordis.patch.yml:240-247`：`sandbox` 绑 `dsh-sandbox-local`，`sandbox-policy` 默认 `workspace-write` + `workspaceRoot: process.cwd()`。（边界：PTY 类工具 `dsh-tool-bash-persistent`/`dsh-tool-pwsh-persistent`/`dsh-tool-terminal` 不经 `ctx.sandbox`，但它们**只在 minimal preset 里挂**（`presets/minimal.patch.yml:41-75`），standard/cordis/ptc 的 shell 走 `tool-pwsh`→`pwsh-sandbox`。）
- **我们现在**：`internal/sandbox/policy.go:68`/`:77` 两个**如实**的 enforcement 值；`internal/sandbox/runner.go:326` `EnforcementFor`、`:339` `windows-acl`；`internal/sandbox/runner_windows.go` = `CreateRestrictedToken(WRITE_RESTRICTED)` + Low IL + `SetEntriesInAclW` 直授随机会话 SID；**作用域只有宿主自有的 `<home>/sandbox-roots/<session-id>/`**，默认不启用（`internal/sandbox/runner.go:39` `EnvACLRoot`），没配时 `cmd/dshd/serve_sandbox_policy.go:326 sandboxEnforcementNow` 一律报 `policy-only-no-os-isolation`。另外我们的默认审批策略是 `allow`（`cmd/dshd/serve.go:197`），即默认既不隔离也不弹审批。
- **差值**：官方在**用户真实工作区**里默认就限写/限删；我们只有在显式开受控根、且只在那个人造根里才限。
- **代价（谁付什么）**：① **对真实目录的驻留安全描述符**——官方自述 `README.zh.md:130`："安全描述符改动是对真实目录的驻留改动 … 手工 `icacls` 清理无法在本平台回收它们（`ERROR_NONE_MAPPED` 1332）"；`:131`："常驻 Low 标签的生命期长于 DSH，并会向其他 Low 完整性的进程放宽该目录树"⇒ **同用户下任何别家 Low-IL 沙箱/受保护模式阅读器都能写/删我们的工作区**。这一条落在用户机器上、且不可逆地留痕；② **控制台限制**——`:129`：`CREATE_NO_WINDOW`/`CREATE_NEW_CONSOLE` 起的子进程在 DLL 初始化期 `0xC0000142` 死亡，只能共享宿主控制台（管道 stdio 不受影响）；③ **行为变化**——默认真隔离后，"写工作区外"从今天的放行变成拒绝，用户会第一次见到这一类失败；④ 还要一套可回滚的 grant 生命周期（官方 `README.zh.md:66` / `:145` 的 `AclWriteGrant` + `dispose()`）。
- **重新评估条件**：出现**非驻留**的 Windows 强制手段（专用本地用户 + 可完全回收的 ACL 是候选）；或用户明确接受"驻留 ACE + 常驻 Low 标签"这份代价。

#### 9.2.4 `#42 bash-sandbox` —— **A 已等价（平台条件使然）**

- **官方是什么（rc.5）**：一个**吃沙箱的 bash 执行器**——注册成 `ctx.shell` 取代本地执行器，要求 `ctx.sandbox` + `ctx.sandboxPolicy`，把精确 bash argv 走 `ctx.sandbox`，并上报 mode/enforcement/denial（`dsh-bash-sandbox/src/index.ts:1-10`、`:30-36`）。
- **出厂开不开**：**win32 关**（`dsh-base/cordis.patch.yml:249-252`，`:251 disabled: !!js process.platform === 'win32'`）；同批 `tool-bash` 在 win32 也关（`:281-283`）。
- **我们现在**：Windows 上我们**有** bash 工具（`cmd/dshd/main.go:152`），带三档策略闸门 + 官方 escalation 契约：`modules/exec/bash/tools.go:117` `sandbox.GateFromEnv`、`:145` 官方 `justification` 描述、`:171-173` `Justification` 字段；`modules/exec/bash/sandbox_scope.go:20-21` `SandboxCall`（准入层配对校验）；`internal/sandbox/escalation.go` 逐字移植官方四个错误码/阶梯/标记行。
- **差值**：**在我们唯一支持的平台上为零**——官方连插件行和 `tool-bash` 一起关掉，没有"被沙箱包着的 bash"这个行为可对齐；我们反而多出"bash 工具本身 + 策略闸门 + escalation"。账本里 `md §2.4` 那格写 `OFFICIAL_UNSUPPORTED` 是**实质正确的**；逐项表只能取 `IMPLEMENTED/PARTIAL/MISSING` 三值（`scripts/gap-register-counts.py` 的 `PLUGIN_STATUSES`），所以 `PARTIAL` + `audit_status=OFFICIAL_UNSUPPORTED` 才是它的准确表达。
- **代价**：不适用（没有要做的事）。**重新评估条件**：支持范围扩到 Linux/macOS（那时官方形态是"bash 执行器走 ctx.sandbox"，才是 B 甚至 C）；或 Windows 上官方把该行重新打开（版本升级时重核 `dsh-base/cordis.patch.yml`）。

#### 9.2.5 `#43 pwsh-sandbox` —— **B 不该补**

- **官方是什么（rc.5）**：`dsh-bash-sandbox` 的 pwsh 双胞胎——同样注册成 `ctx.shell`、把精确 pwsh argv 走 `ctx.sandbox`（Windows 上就是 ACL 受限令牌链），并上报 mode/enforcement/denial（`dsh-pwsh-sandbox/src/index.ts:1-8`、`helpers.ts` 的 denial/runner-failure 分类）。
- **出厂开不开**：**Windows 上默认开**（`:255-256`=`disabled: !!js process.platform !== 'win32'`）。消费它的 `tool-pwsh` 在 standard/ptc/cordis 三个 preset 里带同一平台条件挂载（`presets/standard.patch.yml:24-26`、`ptc.patch.yml:24-26`、`cordis.patch.yml:27-29`；宿主平面那行被 web `:519-520` 关掉）⇒ **这三条路径上官方 Windows 的每条 pwsh 都在 ACL runner 里**。**但 minimal 是例外**：它不挂 `tool-pwsh`，改挂 `dsh-tool-pwsh-persistent`（`presets/minimal.patch.yml:66-74`），后者是 PTY 工具、整文件对 `sandbox`/`confine`/`ctx.shell` **零命中**（`dsh-tool-pwsh-persistent/src/index.ts:1-6` 自述 over the owner-scoped PTY seam）⇒ 官方自己也有一条未受限的 pwsh 路径。
- **我们现在**：`modules/tool/pwsh/sandbox_runner.go:66-74` 起 runner 前先跑能力探针（`ProbeExecutable`），探不通就**如实退回**策略层并把原因写进 `runner_note`；`modules/tool/pwsh/pwsh.go:406-408` 受控根模式下 `run_in_background` 明确拒绝、`:440` enforcement 默认 `policy-only-no-os-isolation`；工具默认挂（`cmd/dshd/main.go:185`），默认审批 `allow`。
- **差值**：官方默认 preset（standard/cordis/ptc）的 pwsh 默认被 OS 限写/限删，我们默认既无 OS 隔离也无审批；官方 minimal 的 persistent pwsh 与我们一样不经过 sandbox（这条不是我们的欠账，但也说明「官方处处受限」不成立）。
- **代价（谁付什么）**：与 #40 同一份 ①②③（驻留 ACE/Low、控制台限制、默认拒绝面）。**另需订正旧记录**：账本此前写"只造受限令牌、不降完整性仍 `0xC0000142` ⇒ 挡住它的是受限令牌本身，需要另一轮独立设计"。官方自己的边界文档把同一个码归因于**控制台隔离不可用**（`dsh-sandbox-windows-acl/README.zh.md:129`：`CREATE_NO_WINDOW`/`CREATE_NEW_CONSOLE` 的子进程在 DLL 初始化期死亡，管道 stdio 不受影响）⇒ 探针失败**可能**只是"新控制台"这一层，而不是"受限令牌跑不了 pwsh"。但即便探针通了，**受控根模式只允许写受控根**——要让 pwsh 正常在真实工作区干活，仍旧回到 #40 的驻留 ACL。
- **重新评估条件**：①先做**零代价的探路**——探针改成共享宿主控制台（不新建控制台）+ 管道 stdio，看 pwsh 能否在受限令牌下起来；②#40 的重新评估条件成立时一起做。

#### 9.2.6 `#72 ptc-runtime` —— **B 不该补**

- **官方是什么（rc.5）**：两层。**契约层** `dsh-ptc-runtime` 是纯抽象：`src/index.ts:43`/`:58`/`:67` 三组共享保留名、`:105-167` 抽象类（`language`/`isolation`/`programFormat`/`retainsState`/`release`/`executionInstructions`/`sandboxMode`/`timeout`/`resolve`/`run`），`src/types.ts` 只有类型。**执行层** `dsh-ptc-runtime-node`：`src/config.ts:10-19` 三个 `executionMode`（默认 `per-call`）+ `scriptPersistence`（默认 false）+ `:57` 交叉校验；三个求值器 `evaluators/index.ts:7-12`（`function-body`/`repl`/`script`）；`evaluators/vm-script.ts:63-72` 里含 `await` 就走 TLA lowering（`vm-script-await.ts:1-6`、`:193-290`，基于 acorn）；`evaluators/repl.ts:13-45` 是 `node:repl`；`src/worker.ts:287` 把 argv 送进 `ctx.sandbox.confine`（除 `danger-full-access`）、`:283` 堆上限、`:101-104` 输出账本。
- **出厂开不开**：**开**（`dsh-base/cordis.patch.yml:407-408` 挂 `dsh-ptc-runtime-node`，无 config ⇒ 全默认 `per-call`）。消费者两个：`tool-executor-ptc`（默认关）与 `workflow-ptc`（按 preset 挂）。
- **我们现在**：契约 `internal/ptc/runtime.go:49`（同名访问器齐：`Language`/`Isolation`/`ProgramFormat`/`RetainsState`/`ExecutionInstructions`/`SandboxMode`/`Timeout`）、`:85` `ResolveTimeout`（三态）、`:133` `ResolveSandbox`、`:144` `ValidateProgramFormat`；词汇与校验 `internal/ptc/identifiers.go:21/55/79/114`。provider `internal/ptc/nodeprovider/provider.go:85` `DefaultConfig`（per-call）、`:105` `ModePerCall`、`:226` `New`（`persistent-in-memory` 装配期拒绝）、`:278` `Language`、`:286` `ProgramFormat`、`:348` `SandboxMode() → ("", false)`、`:356` `Resolve`；外部受管 node、帧式控制、整棵进程树可杀、`worker.go:324-377` 的 vm context。
- **差值（3 条）**：① `persistent-in-memory`（REPL）没有；② 顶层 `await` lowering 没有；③ **程序进程没有文件 confinement**——官方 worker 的 argv 默认被 `ctx.sandbox.confine` 包住，我们 `SandboxMode()` 恒 false，PTC 程序按宿主用户权限跑（本仓代码自己写明了：`modules/tool/runcode/runcode.go:57-63`）。
- **代价（谁付什么）**：① 前两条**没有消费者**——`run_code` 固定 `per-call`（`modules/tool/runcode/runcode.go:199` 用 `nodeprovider.DefaultConfig()`），`workflow` 走 Go 原生引擎，所以补了就是死代码（仓规：不建空壳）；要真可用必须先生长出 `executionMode` 配置面 = **新的用户可见配置项**（官方 `config.ts:10-19` 三个模式 + `:57` 交叉校验）。② 第三条与 #40 同一份代价（真实工作区驻留 ACL）；退一步"只允许写受控根"则是**行为变化**——今天程序能写任意可写路径，之后不能。
- **重新评估条件**：出现需要 REPL 的消费者（例如把 PTC 从 `ptc` 预设路径真正接进本仓）；或 #40 的重新评估条件成立。

#### 9.2.7 `#73 workflow-ptc` —— **B 不该补**

- **官方是什么（rc.5）**：JavaScript 工作流跑在**全新 Node 进程**里、按调用 Session 的文件沙箱策略执行；脚本保留 `agent()`/`parallel()`/`pipeline()`/`phase()`/`log()` 钩子（`README.zh.md:11`）。装配面：`src/index.ts:104` `inject = ['subagents','ptcRuntime','sandboxPolicy','workingDirectory']`、`:107` `provider: spawn` 默认、`:118` 要求 `ctx.ptcRuntime.language === 'typescript'`、`:161-167` 通过 `ptcRuntime` + `sandboxPolicy.resolve` 起跑；`host.ts:285-291` 若运行时没有 `sandboxMode` 且策略不是 `danger-full-access` 就**拒绝**、`:292` 交付的脚本形如 `(async () => {\n${PROGRAM}\n})()`；六钩子在 `runtime.ts:86-90`；`run_in_background` 由 `dsh-tool-workflow/src/index.ts:177` 提供（默认 true）。
- **出厂开不开**：base 开（`:410-411`），**但 Web 把宿主行关掉**（`dsh-web-app/cordis.patch.yml:598`），改由 preset 在作用域里挂：standard `:111-116` 开、cordis `:107-112` 开、**ptc `:108-116` 自己 `disabled: true`**、minimal **零命中**。⇒ 官方 Web 是"**按 preset 决定有没有 workflow**"。
- **我们现在**：Go 原生引擎语义齐——`internal/workflow/engine.go:34` `DefaultLimits`（0/1000/4096 逐条官方）、`:130` `Start`、`:258` `Agent`、`:356` `Parallel`（真并发 + 并发闸）、`:405` `Pipeline`（无 barrier）、`:224` `Phase`、`:237` `Log`；工具 `modules/tool/workflow/workflow.go:40` schema、`:125` `Exec`，**默认挂**（`cmd/dshd/main.go:172`）。脚本是**声明式 JSON 操作表**（`modules/tool/workflow/orchestrate.go:1-22`），`run_in_background` 明确拒绝（`workflow.go:46`）。
- **差值**：① 脚本语言（官方 JS 可写循环/条件/计算；我们只有静态 op 序列 + `${save}` 引用）；② `run_in_background`（官方默认 true，我们拒绝）；③ 按 preset 增删 workflow 的能力（官方 Web 靠 preset 层，我们的 preset 不装插件——与 `#54 skill` 同一个上游限制）；④ 官方 workflow 运行受 session 文件策略约束，我们的子代理各自走工具审批。
- **代价（谁付什么）**：① 把 `script` 换成 JS = **改模型可见的 schema/Description 字节**并换掉模型已学会的调用形状（`workflow.go:40` + `orchestrate.go:1-22` 的语法说明全部重写）；② 接线还要 guest 六钩子桥回宿主（官方 `runtime.ts:86-90`）、`meta`/`args`/`agent({schema})` 语义、取消与清理；③ `run_in_background` 要长出工作流后台任务面（jobs 归属、界面卡片、取消）；④ 要对齐"受文件策略约束"还得先有 confinement。
- **重新评估条件**：模型在 JSON 操作表上反复表达不出需要的编排（真实 A/B 证据）；或 PTC 的 confinement 先落地（那时接线成本才可算）。

#### 9.2.8 `#92 tool-executor-ptc` —— **B 不该补**（含 1 个 C 子项）

- **官方是什么（rc.5）**：一个**执行器**（不是工具）：`src/index.ts:112-113` `apply()` 末尾 `ctx.tools.setExecutor(new PtcExecutor(...))`；`:34-38` 注册两个 system prompt 段 `tools:ptc-only` / `tools:sdk`（SDK 由工具 schema 生成，TS 渲染 335 行、Python 817 行）；`:38-56` `describe` 只投影 `run_code`、`reject` 把非嵌套的直接调用挡掉（官方 README.zh:12「只有 `run_code` 能被直接调用」、`:59` 直呼 SDK 工具在策略之前解析为 `UNKNOWN_TOOL`）；`:73-96` `reset(agent)`（要求 `retainsState`，并向会话注入一条"环境已重置"的用户消息）；`ptc.ts:560`/`:587` 追加 `tool/ptc-dispatch` / `tool/ptc-dispatch-start` 事件；`README.zh:67` 更宽 `sandbox_permissions` 要求 `justification` 且**在程序启动前**审批。
- **出厂开不开**：**关**（`dsh-base/cordis.patch.yml:522-523` `disabled: true`），只有 `ptc.patch.yml:127-128` 把它打开（同时把 `workflow-ptc`/`tool-workflow` 在该 preset 里关掉）。
- **我们现在**：`modules/tool/runcode/runcode.go:63` `OptInEnv = "DSHD_PTC"`（默认不挂，挂载点 `cmd/dshd/main.go:229`）、`:118-124` Description、`:199` `DefaultConfig()`（per-call）、`:214` `Exec`、`:253` `Bindings`、`:287` `allowanceFor`（官方总额度算式）、`modules/tool/runcode/scheduler.go:113` `call`（先计数后派发、`ToolCallError` 类型契约、fail-closed 并发分类）。
- **差值**：① **不做执行器替换**（我们是加法：`run_code` 与原生工具并列）⇒ 官方"只有 `run_code` 可直接调"的强制面我们没有；② 没有 `tools:sdk`/`tools:ptc-only` 段；③ 不发 `ptc-dispatch`/`-start` 事件；④ 没有 `reset(agent)`；⑤ 图片型子结果的 deferred user-message、沙盒升级审批进程序启动前，未接。
- **代价（谁付什么）**：① 执行器替换会让**原生工具从"直接可调"变成"只能从程序里调"**——这是模型可见行为变化，且与"官方默认关、只在 ptc 预设开"的部署形态绑死；② SDK 段进系统提示 = **改模型可见字节**；③ `ptc-dispatch` 事件要登记 v4 事件 + 投影 + 界面消费；④ `reset` 需要 `retainsState` 消费者；⑤ 升级审批会让 PTC 用户每次提权多一次弹窗。
- **重新评估条件**：用户明确要"ptc 预设"这种部署形态（PTC 取代原生执行器）；或我们要把 PTC 变成默认能力（那时执行器替换与 SDK 段都必须一起做）。
- **C 子项**：见 §9.4。

### 9.3 B 类代价汇总（逐条回答"谁付什么"）

| # | 插件 | 改变用户可见行为？ | 变慢/变贵？ | 多 UI/多配置面？ | 动 golden/模型可见字节？ | 用户已拍板？ |
|---:|---|---|---|---|---|---|
| 26 | `llm-pi-ai` | 只在声明非 OpenAI 协议时要紧（今天是硬错误） | 否 | **是**：协议选择 + 每协议 retry/compat/models + OAuth 登录失败面 | 否 | **是**（2026-11-15 不扩四协议） |
| 29 | `session-query-sqlite` | **是**：检索排名/excerpt 变 | **是**：依赖体积 + 首次检索延迟 | 否 | 否 | **是**（不引入 SQLite） |
| 40 | `sandbox` | **是**：工作区外写/删从放行变拒绝 | 每次起进程多一层 token/ACL | 否 | 否 | **是**（不做 ACL partial 隔离） |
| 42 | `bash-sandbox` | 不适用（无差） | — | — | — | — |
| 43 | `pwsh-sandbox` | **是**：同上 + 新拒绝面 | 同上 | 否 | 否 | **是**（同 #40） |
| 72 | `ptc-runtime` | 是（程序 fs 写入范围变窄） | 否 | **是**：新增 `executionMode` 配置 | 否 | 否（PTC 是加法能力） |
| 73 | `workflow-ptc` | **是**：脚本语言换 JS | 否 | 是（后台任务卡片） | **是**：schema/Description 字节 | 部分（旧记录是"R6 不嵌 JS"，该理由已被 §HOST-PTC 推翻） |
| 92 | `tool-executor-ptc` | **是**：原生工具不再直接可调 | 否 | 是（提权审批弹窗） | **是**：SDK 段进系统提示 | 否 |

**A 类的动作与 B 类不同**：#42 只需改说明（见 §9.2.4），不需要任何"重新评估条件"以外的承诺。

### 9.4 C 类（真缺且零代价）—— **0 条整项，1 个 C 子项**

**C 子项（附属于 `#92 tool-executor-ptc`）**：`run_code` 的 Description 写着
"Call tools as `await tools.name(args)` **per the declarations in the system prompt**."
（`modules/tool/runcode/runcode.go:118-124`），而本仓**没有**那个 SDK 段——在我们的加法形态里，
工具本来就在**原生工具表**里对模型可见，所以这句话是个**假指针**（不是能力缺口：模型仍能调）。
改成"用你已经看到的原生工具表"为什么**零代价**：

- 默认装配**一个字节都不变**：`run_code` 默认不挂（只 `DSHD_PTC=1` 才挂，`cmd/dshd/main.go:229`）；
- 只影响已经显式开 PTC 的请求，且是**把错话改对**（模型本来就看得见原生工具表）；
- 没有测试钉住这句：`modules/tool/runcode/runcode_test.go:135` 只断言 Description 含 `TypeScript`；
- 不变慢、不多 UI、不动 golden。

**本单只标注，不动手**（用户口径：C 类补不补等下一单）。其余 7 条**没有**零代价项：
它们的差值都要么改模型可见字节、要么引入依赖、要么要驻留 ACL、要么要新增配置面。

### 9.5 复核命令（照抄可跑）

```bash
O="D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai"
# 出厂装配：8 条 + 两条 platform 对照
grep -n "llm-pi-ai\|session-query-sqlite\|id: sandbox$\|sandbox-policy\|bash-sandbox\|pwsh-sandbox\|ptc-runtime\|workflow-ptc\|tool-executor-ptc\|tool-bash\|tool-pwsh" "$O/dsh-base/cordis.patch.yml"
grep -n "session-query-sqlite\|workflow-ptc" "$O/dsh-web-app/cordis.patch.yml"
grep -n "workflow-ptc\|tool-workflow\|tool-executor-ptc" "$O"/dsh-web-app/presets/*.patch.yml
# 26：dormant 与协议/provider 目录
grep -n "dormant posture" "$O/dsh-llm-pi-ai/src/index.ts"
sed -n '338,346p' "$O/dsh-llm-pi-ai/src/catalog.ts"
grep -c "env: \[" "$O/dsh-llm-pi-ai/src/provider-data.generated.ts"   # 44
# 29：openAt 语义与 FTS5
grep -n "openAt\|SESSION_QUERY_SEARCH_DISABLED" "$O/dsh-session-query-sqlite/src/index.ts" | head
grep -n "USING fts5" "$O/dsh-session-query-sqlite/src/schema.ts"
# 40/43：win32 链与驻留代价
grep -n "win32: \[" "$O/dsh-sandbox-local/src/index.ts"
sed -n '127,132p' "$O/dsh-sandbox-windows-acl/README.zh.md"
# 72/73/92：默认形态、confinement、执行器替换
grep -n "executionMode\|scriptPersistence" "$O/dsh-ptc-runtime-node/src/config.ts" | head -4
grep -n "sandbox.confine" "$O/dsh-ptc-runtime-node/src/worker.ts"
grep -n "setExecutor\|ptc-dispatch\|tools:sdk" "$O/dsh-tool-executor-ptc/src/index.ts" "$O/dsh-tool-executor-ptc/src/ptc.ts" | head
grep -n "ptcRuntime.language\|sandboxMode" "$O/dsh-workflow-ptc/src/index.ts" "$O/dsh-workflow-ptc/src/host.ts" | head
# 我们这边
grep -n "var providerAdapters\|func LookupProviderAdapter" internal/llm/provider.go
grep -n "QueryEngineScan\|func SearchSessions" internal/session/query.go
grep -n "EnforcementFor\|EnvACLRoot" internal/sandbox/runner.go
grep -n "OptInEnv\|DefaultConfig()" modules/tool/runcode/runcode.go
grep -n "run_in_background" modules/tool/workflow/workflow.go
```
