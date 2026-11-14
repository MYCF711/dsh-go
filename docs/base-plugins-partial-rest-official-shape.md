# base 插件面「仍在 PARTIAL」各条的官方形状说明（partial-rest 单）

> 基线：官方 `0.2.2-rc.2`（官方原件根 `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`）。
> 前情：`tmp/GO8-RC2/reports/HOST-partial19.md`（19 条 → 补齐 12 / R6 1 / 拍板 4 / 不该补 1 / 不适用 1）、
> `tmp/GO8-RC2/reports/HOST-partial-rest.md`（本单）。
> 审计：`tmp/GO8-RC2/_audit/plugin-face.md`（99 项）。
> 上一单已定掉、本次**不再论证也不再动**的四条：`#26 llm-pi-ai`（不扩协议）、
> `#29 session-query-sqlite`（不做倒排）、`#40`/`#43` sandbox ACL（不做 partial 隔离）、
> `#42 bash-sandbox`（官方 win32 自己就 disabled）。它们的说明在上一个文档 §2/§3。

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

### 7.3 `#54 skill`：仍然 PARTIAL，理由是"没有第二个 layer"

官方 `dsh-skill/README.md:84` 的分层是"registration 落进**调用上下文 scope** 的层"：
"host rows and repository plugins land in the global layer, **a plugin mounted by an
agent preset's standing composition lands in that preset's layer**"。

本仓没有这个生产者：`internal/modes` 的 preset 只是模式（工具白名单轴），不承载模块
组合；`serve_agent_api_skills.go` 的生产装配只挂一个 filesystem provider
（`skill.Registry` 的全局层）。在没有"per-scope provider"的情况下把分层 API 建出来，
是一个**没有任何生产者**的空层——那违反"不许空壳"。重新评估条件不变：出现
"preset 自带 provider"的装配形状时，按官方 `ScopedLayers`（`src/index.ts:420-440`）补。
"incomplete 持久化"仍是官方 README:148 自述的**开放问题**（"consumers should own that
state"），本仓落在 consumer 侧，不是缺口。

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
