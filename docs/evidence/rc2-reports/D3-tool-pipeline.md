# D3 · 工具流水线：per-call 并发分类器 + 工具错误身份（官方 0.2.2-rc.2）

> **权威来源**：`tmp/GO8-RC2/reports/R2-agent-team-tools.md` §0 第 6/7 条、§4 的 R2-G09 / R2-G10。
> **官方源码（只读）**：`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-tools/`（下称 **T**），
> 各工具包同根（下称 **R2**）。行号均在本机解包件上核对过。
> **本仓**：`internal/spi/tool.go`（SPI 唯一代码定义）、`internal/agent/{parallel,loop}.go`、`internal/approval/`、
> `modules/tool/{glob,grep,schedule}/`。工作目录 = `.agents/worktrees/dsh-bdb66fbf-95ba`。

---

## 0. 结论摘要（≤10 行）

1. **SPI 新增可选接口** `spi.ConcurrencyClassifier{ ConcurrencySafe(args json.RawMessage) bool }`，判定只走
   `spi.ConcurrencySafe(tool, args)`：**没声明 / 返回 false / panic → 独占**（fail-closed，与官方
   `executionMode` 的 try/catch 逐条对齐）。
2. **`internal/agent/parallel.go` 按 per-call 分类器分组**：独占车道 = 需要审批的 + 没声明/按参数判独占的；
   并发池 = 声明安全且这次参数安全的。**审批优先压过工具自己的并发声明**；`maxParallelToolCalls` 默认仍是 **10**。
3. **`spi.Result.Info`（官方 `error.info{name,code}`）落地**，码表逐字照抄官方 4 类 9 个原词；
   `Result.Hint` 语义与 Transport 四字段错误契约（`code/message/details/hint`）**不动**。
4. **取消身份**：`context.Canceled` → `AbortError`/`ABORTED`；"工具体正常返回但本轮已取消"同样归 `ABORTED`
   （官方取消压过成功）。
5. **已接线声明**：`glob`、`grep`、`schedule_list`（都在我的写作用域）。其余 **15 条**列"待他人接线"，**7 条**
   本仓无对应工具（rc.2 新工具，实现时必须自带声明）。
6. **schedule 四个工具的无会话失败**补 `ToolContextUnavailableError`/`TOOL_CONTEXT_UNAVAILABLE`（官方新码）。
7. **实测**：6×grep（每次 120ms）并发上限 10 = **130.75ms**，`DSHD_MAX_PARALLEL_TOOL_CALLS=1` 串行 = **732.09ms**，
   提速 **5.6×**；tool 消息与 `tool/result` 事件顺序 == 模型顺序。
8. 我的六个包 **204 PASS / 0 FAIL**；`go build ./...` exit 0。
9. **他人中间态红**（已用"撤回我的改动再跑"隔离证明非我引入）：
   ① `modules/tool/office/office.go:290` `undefined: base64Std`（office 任务线半写状态）；
   ② `cmd/dshd` 3 个失败（`TestToolParityWithDSH` / `TestWriteToolsDeclareCaps` / `TestMergedRoutesToOriginalTool`），
   全部落在 team/teamtask 工具契约与工具表，见 §7。
10. **既有契约漂移（只报不改）**：`modules/tools/pipeline.go:62` 把官方**常量名**当**值**用
    （`"TOOL_ABORTED_BEFORE_DISPATCH"` 应为 `"ABORTED_BEFORE_DISPATCH"`）。

---

## 1. 官方证据（file:line）

| # | 结论 | 官方证据（相对 **T** / **R2**） |
|---|---|---|
| E1 | 分类器是**可选**的逐调用方法，形状 `isConcurrencySafe?(args: unknown): boolean` | `T src/contracts.ts:84`、`T src/schema.ts:509` |
| E2 | 判定点：没声明 / 非 `true` / 抛错 → **exclusive** | `T src/pipeline.ts:107-114`（`lib/pipeline.js:284-291`；任务书写的 286-289 是同一段） |
| E3 | 未知/隐藏/不可解析参数同样归 exclusive | `T src/pipeline.ts:105-108`（`resolveExecution` 返回 undefined 即 exclusive） |
| E4 | 独占调用是**屏障**，并发调用进滚动池，且**每次启动前重新分类** | `T lib/types/batch.js:44-60`（主循环）、`:118-135`（`fillPool` 重分类） |
| E5 | 并发上限默认 **10** | `R2 dsh-agent-loop/lib/index.js:1322`（`z.number().step(1).min(1).default(10)`） |
| E6 | 结果对象 `{isError, content, error:{message, info:{name,code}}}` | `T lib/types/batch.js:232-245`、`T lib/pipeline.js:841-856` |
| E7 | 落盘 `tool/result` 的 `error` 就是那个 `info` | `T lib/types/batch.js:238`（`...result.error?.info ? { error: result.error.info } : {}`） |
| E8 | 审批四结局 → 三码（allowed-once / rejected / cancelled / unavailable） | `T lib/pipeline.js:618-660` |
| E9 | 码表原词 | `T lib/types/error-codes.js:8,10,12,14,16` |
| E10 | `TOOL_ABORTED` 的**值**是 `"ABORTED"`；派发前是 `"ABORTED_BEFORE_DISPATCH"` | `T lib/types/contracts.js:19,21` |
| E11 | 缺上下文专用码/类 | `T lib/types/contracts.js:42-56`、`lib/types/error-codes.js:15-16` |
| E12 | 取消压过成功；被取消 body 的抛出物补 `AbortError`+`ABORTED` | `T lib/pipeline.js:478-484`、`:927-944`、`:946-960` |
| E13 | 取消判定：已带身份的不算"未分类取消" | `T lib/pipeline.js:857-866`（`isUnclassifiedCancellation`） |
| E14 | 各工具声明（共 **25** 条真实声明：22 `true` + 3 `false`） | 见 §2.4 表 |

**为什么不用"只读就安全"这类推断**：官方判据是**声明**，不是 Caps 推断。同一个工具不同参数副作用不同
（`office_convert` 写输出目录 vs `office_render` 只渲染），所以判据必须取 args。

---

## 2. 交付一：per-call 并发分类器

### 2.1 SPI 新签名（`internal/spi/tool.go:67`、`:77`）

```go
// 可选接口：没有它 = 独占（fail-closed）。故意不加进 Tool 接口——
// Tool 是全仓所有工具（含 wasm 插件样本）的公共形状，加方法会让全部既有实现编译不过。
type ConcurrencyClassifier interface {
    ConcurrencySafe(args json.RawMessage) bool
}

// 唯一判定入口：nil / 未实现 / false / panic → 独占。
func ConcurrencySafe(t Tool, args json.RawMessage) (safe bool)
```

- `internal/spi/tool.go:67` `type ConcurrencyClassifier interface`
- `internal/spi/tool.go:77` `func ConcurrencySafe(t Tool, args json.RawMessage) (safe bool)`（`defer recover → false`）

**为什么是可选接口而不是 `Tool` 的必选方法**（与 R2-G09 的措辞有一处差异，理由写在这里）：
`Tool` 是本仓唯一的工具形状，必选方法会让 `modules/tool/**` 全部实现、`internal/wasmhost` 插件样本、
`modules/tools/registry` 的假工具在同一次改动里全部编译失败；而这条语义的**缺省值本来就是独占**，
可选接口让"没接线 = 独占"成为零动作的默认值，工具可以按域逐个接线（清单见 §2.4 与 §6）。
语义与官方完全一致：`!tool.isConcurrencySafe` 与 `tool.isConcurrencySafe(args) !== true` 两条分支
都映射成同一个 `modeExclusive`。

### 2.2 判定表（`internal/agent/parallel.go:130` `executionMode`）

判据**顺序固定**（换顺序就是换语义）：

| 顺序 | 条件 | 结果 | 官方对应 | 本仓落点 |
|---|---|---|---|---|
| 1 | `Loop.Tools == nil` | 独占 | `resolveExecution → undefined` | `parallel.go:132` |
| 2 | 工具表里查不到该名字 | 独占 | `!tool?.isConcurrencySafe`（undefined） | `parallel.go:135-138` |
| 3 | `tool.Caps().NeedsApproval()`（且工具已声明安全也一样） | **独占** | 官方无此闸（它的 prepare 串行） | `parallel.go:139-142` |
| 4 | `spi.ConcurrencySafe(tool, args)` 为 `true` | **并发池** | `=== true → {kind:'parallel'}` | `parallel.go:143-145` |
| 5 | 其余（未声明 / false / panic） | 独占 | 其余 → `{kind:'exclusive'}` | `parallel.go:146` |

调度落点（`parallel.go:335` `executeToolCalls`，车道划分在 `:347-354`）：

| 车道 | 收谁 | 并发度 | 顺序 |
|---|---|---|---|
| 车道 B（独占） | 判定表 1/2/3/5 的调用 | 同一时刻 1 个 | 模型顺序逐条 |
| 车道 A（并发池） | 判定表 4 的调用 | 最多 `maxParallelToolCalls`（默认 10） | 完成乱序、**结算按模型顺序** |
| 退化路径 | `limit<=1` 或只有 1 条调用 | 0 个 goroutine，逐条 | 模型顺序 |

两条车道**同时**跑：独占车道在等人（审批）时，可并发的只读调用不陪等。

### 2.3 与官方的两处有意差异（都留痕，不是省事）

| 差异 | 官方 | 本仓 | 理由 |
|---|---|---|---|
| D1 屏障 vs 双车道 | 独占调用是屏障：它之后的并发调用等它结算（`batch.js:44-60`） | 独占调用进独立车道，与并发池**同时**跑 | 本仓的审批是 `Runner` 内部**同步等人**（`guardedRunner.Run → g.ask`），拿不到官方"prepare 严格按模型顺序串行、只有 dispatch 并发"的切分点。用双车道保住"审批单线程"这条硬不变量，同时不让只读调用陪等（GO.6 已有契约 + 用例 `TestApprovalClassToolsAreSerialized`） |
| D2 一次分类 vs 启动前重分类 | 并发池每启动下一个调用前重新分类（`batch.js:118-135`），因为注册表可能中途变 | 整批开始前分类一次 | 本仓 `Loop.Tools` 在一轮内不可变（注册发生在装配期），且 `Runner` 内部还有一层工具表准入；重分类只会改变"谁和谁同池"，不会改变安全性。已在 §7 登记为已知偏差 |
| D3 分类器看到的参数 | 解析并校验后的 `arguments` | 模型给的原始 `call.Function.Arguments` 字节 | 本仓的参数校验在 `Runner`（`ValidateToolCallArguments`）；分类器解析失败就必须返回 false，与官方"invalid → exclusive"同向。`cmd/dshd` 的 `stripSandboxPermissions` 发生在这之后，分类器看到的是模型原文 |

### 2.4 分类器声明清单（官方 25 条 → 本仓状态）

官方逐条（**R2** 解包件，`isConcurrencySafe: () => …`）：

| # | 工具 | 官方 file:line | 值 | 本仓状态 |
|---|---|---|---|---|
| 1 | `glob` | `dsh-tool-fs-search/lib/index.js:780` | true | **已接线** `modules/tool/glob/glob.go:55` |
| 2 | `grep` | `dsh-tool-fs-search/lib/index.js:1095` | true | **已接线** `modules/tool/grep/grep.go:68` |
| 3 | `schedule_list` | `dsh-tool-schedule/lib/index.js:581` | true | **已接线** `modules/tool/schedule/schedule.go:208`（`kind==kindList`） |
| 4 | `schedule_view` | `dsh-tool-schedule/lib/index.js:606` | true | 本仓无 view 工具（schedule 只有 create/list/update/delete）→ N/A |
| 5 | `job_list` | `dsh-tool-jobs/lib/index.js:470` | true | 待接线：`modules/exec/bash/tools.go:466`（`jobListTool`） |
| 6 | `read` | `dsh-tool-fs/lib/index.js:338` | true | 待接线：`modules/tool/read/read.go` |
| 7 | `read_image` | `dsh-tool-fs/lib/index.js:1838` | true | 待接线：`modules/tool/readimage/readimage.go:38` |
| 8 | `web_search` | `dsh-tool-web/lib/index.js:334` | true | 待接线：`modules/tool/websearch/websearch.go:153` |
| 9 | `web_fetch` | `dsh-tool-web/lib/index.js:832` | true | 待接线：`modules/tool/webfetch/webfetch.go:51` |
| 10 | `cordis_inspect_list` | `dsh-tool-cordis/lib/index.js:50` | true | 本仓无该工具 → N/A（实现时必须声明） |
| 11 | `cordis_inspect_query` | `dsh-tool-cordis/lib/index.js:88` | true | 同上 |
| 12 | `team_task_list` | `dsh-tool-agent-team/lib/index.js:215` | true | 待**他人**接线：`modules/tool/teamtask/teamtask.go:49`（该域正被并行任务改） |
| 13 | `team_task_get` | `dsh-tool-agent-team/lib/index.js:240` | true | 同上 |
| 14 | `list_mcp_resources` | `dsh-mcp-resources/lib/index.js:60` | true | 待**他人**接线：`modules/tools/mcp.go:1240` |
| 15 | `list_mcp_resource_templates` | `dsh-mcp-resources/lib/index.js:71` | true | 待**他人**接线：`modules/tools/mcp.go:1241` |
| 16 | `read_mcp_resource` | `dsh-mcp-resources/lib/index.js:89` | true | 待**他人**接线：`modules/tools/mcp.go:1242` |
| 17 | `office_render` | `dsh-tool-office/lib/index.js:440` | **true** | 待**他人**接线：`modules/tool/office/office.go`（在建） |
| 18 | `office_convert` | `dsh-tool-office/lib/index.js:459` | **false** | 待**他人**接线：必须显式 `false`（写临时/输出目录） |
| 19 | `office_recalculate` | `dsh-tool-office/lib/index.js:478` | **false** | 待**他人**接线：必须显式 `false` |
| 20 | `subagent` | `dsh-tool-subagent/lib/index.js:432`（`src/index.ts:273`） | true | 待接线：`modules/tool/subagent/subagent.go` |
| 21 | `list_subagent_models` | `dsh-tool-subagent/lib/index.js:228` | true | 本仓无该工具 → N/A |
| 22 | `list_agents` | `dsh-tool-subagent-control/lib/index.js:35` | true | 待**他人**接线：`modules/tool/team/team.go:28`（team/** 正被改） |
| 23 | `session_trace` | `dsh-tool-session-query/lib/index.js:977` | true | 本仓无该工具 → N/A |
| 24 | `session_event_trace` | `dsh-tool-session-query/lib/index.js:993` | true | 同上 |
| 25 | `session_event_read` | `dsh-tool-session-query/lib/index.js:1017` | true | 同上 |

> 计数说明：`dsh-tool-cordis/lib/types/index.js:28,53` 还有两条同值声明，是类型/声明文件的重复体，
> 不计入 25 条工具声明。任务书列的 12 条都在上表内（其中 `schedule_view`/`cordis_inspect_*`/
> `list_subagent_models`/`session_*` 本仓尚无对应工具）。

---

## 3. 交付二：工具错误身份

### 3.1 形状与落点

- 内存里：`spi.Result.Info *spi.ErrorInfo`（`internal/spi/tool.go:116`、`:178`），
  `{"name": "...", "code": "..."}`，成功结果留 `nil`。
- 补身份的唯一姿势：`Result.WithErrorInfo(name, code)`（`internal/spi/tool.go:195`）——**只补 Info，其它字段一个不动**。
- 落盘（待他人接线，见 §6）：`tool/result.error` = 这个 `info`（官方 `batch.js:238`）。本仓已有对应构造器
  `internal/session/error_codes.go:150 ToolResultEventWithError(... TurnEndError)`，其 `errorPayload()` 正是
  `{name, code, reason?, message?}`；`internal/session/log.go:191 ToolResultEvent` 负责普通形状。

### 3.2 码表（逐字，`internal/spi/tool.go:126-154`）

| 常量 | 值 | 官方原件 | 出现在什么场景 |
|---|---|---|---|
| `ErrorNameApproval` | `ApprovalError` | `error-codes.js:8` | 审批三态的类名 |
| `CodeApprovalRejected` | `APPROVAL_REJECTED` | `error-codes.js:10` | 人看过并拒绝 |
| `CodeApprovalCancelled` | `APPROVAL_CANCELLED` | `error-codes.js:12` | 审批请求被取消（**不是**拒绝） |
| `CodeApprovalUnavailable` | `APPROVAL_UNAVAILABLE` | `error-codes.js:14` | 没有审批服务/会话/通道能回答 |
| `ErrorNameToolContextUnavailable` | `ToolContextUnavailableError` | `contracts.js:48,55` | 工具缺上下文（调用方 Agent、注入服务、打开的轮次） |
| `CodeToolContextUnavailable` | `TOOL_CONTEXT_UNAVAILABLE` | `error-codes.js:16` | 同上；**不是参数问题**，模型改参数没用 |
| `ErrorNameAbort` | `AbortError` | `pipeline.js:482,938,957` | 取消 |
| `CodeAborted` | `ABORTED` | `contracts.js:19`（常量名 `TOOL_ABORTED`） | 工具体**已被调用**后取消 |
| `CodeAbortedBeforeDispatch` | `ABORTED_BEFORE_DISPATCH` | `contracts.js:21` | 派发前取消（本仓在 `modules/tools/pipeline.go` 的 BeforeDispatch 钩子） |

> ⚠️ 常量名 ≠ 值：`TOOL_ABORTED = 'ABORTED'`、`TOOL_ABORTED_BEFORE_DISPATCH = 'ABORTED_BEFORE_DISPATCH'`。
> 消费方按**值**分支。SPI 里按值定义，并在 `internal/spi/tool_test.go` 里逐字钉住。

### 3.3 映射表（官方错误 → 本仓产生者 → 接线状态）

| 官方结局/异常 | 身份 | 本仓产生者（现状） | 接线状态 |
|---|---|---|---|
| `rejected`（用户点了拒绝） | `ApprovalError` + `APPROVAL_REJECTED` | `cmd/dshd/acp.go:672-673`（`用户拒绝了 %s`） | **待接线**：改用 `approval.Rejected(hint).Failed(res)` |
| `cancelled`（审批取消/整轮取消待审批） | `ApprovalError` + `APPROVAL_CANCELLED` | `cmd/dshd/serve_approval.go:466 CancelSession`（`reply(false)` + `approval/cancel`）→ 落到 acp 的拒绝分支 | **待接线**：CancelSession 路径标记"取消"而非"拒绝" |
| `unavailable`（无审批通道/无人值守策略/超时无人答） | `ApprovalError` + `APPROVAL_UNAVAILABLE` | `cmd/dshd/acp.go:646-648`（sandbox 一次性审批无通道）、`:662-663`（ask 失败/断开）、`:669`（策略 deny）、`serve_approval.go` 超时按拒绝结算 | **待接线**：四处分别给 `Unavailable(...)`；超时建议也归 unavailable（**这是本仓选择，官方无逐字对应**，见 §7） |
| 缺上下文 | `ToolContextUnavailableError` + `TOOL_CONTEXT_UNAVAILABLE` | `modules/tool/schedule/schedule.go:217 contextUnavailable`（4 个工具的无会话路径） | ✅ **本次已接线**（我的写作用域） |
| 工具体已调用后被取消 | `AbortError` + `ABORTED` | `internal/agent/parallel.go:214 failureOutcome`（`errors.Is(err, context.Canceled)`）与 `:181-188`（正常返回但 `ctx.Err()!=nil` → 取消压过成功） | ✅ **本次已接线** |
| 派发前取消 | `AbortError` + `ABORTED_BEFORE_DISPATCH` | `modules/tools/pipeline.go:79 abortedBeforeDispatchResult`（code 走 `Result.Data`） | ⚠️ **既有漂移**：常量值写成 `"TOOL_ABORTED_BEFORE_DISPATCH"`（官方**常量名**），应为 `"ABORTED_BEFORE_DISPATCH"`；且身份还在 `Data` 而非 `Info`。不在我的写作用域，只报不改 |
| 工具自己的失败（无专用类） | `Info == nil` | 各工具 | 语义保持：失败仍靠 `Hint` + 文本 |

**四字段错误契约不变**：`code/message/details/hint` 只在 Transport（`internal/transport/server.go:1275`）与
`internal/officialwire`，本次**一行未改**；`tool/result.error` 是另一条信封（`name/code`），两者不混用。

**`Result{Hint}` 语义保留**：补身份是 `WithErrorInfo`（值拷贝 + 只设 Info），
`modules/tool/schedule` 的 4 个 Hint 文案原样；`internal/agent/parallel.go` 的取消路径仍保留
"结果未知：先读一下现场…" 这条 Hint（用例 `TestCancelledToolResultCarriesAbortIdentity` 直接钉它）。

### 3.4 取消语义的边界（写清，不含糊）

- **`context.Canceled` → `ABORTED`**：这一轮被取消。
- **`context.DeadlineExceeded` → 不给身份**：本仓的 `modules/tools` timeout-policy 用 `WithTimeout` 派生
  ctx，工具超时会表现为 `DeadlineExceeded`，那是"这条工具超时"，不是"这一轮被取消"。混成一个身份会让模型
  对超时也去"重新发起整轮"。文案（"结果未知，你自己判断"）两条路径**保持原样**，只有身份分开。
- **取消压过成功**（官方 `pipeline.js:479`）：工具体返回成功、但这一轮在它返回前已取消 → 记 `ABORTED` +
  `IsError`。理由：那个成功可能只写了一半。
- **派发前 vs 已调用**：本仓的 `Runner` 不向外暴露 `bodyInvoked`，所以"成功但已取消"只能归 `ABORTED`；
  真正的"派发前取消"由 `modules/tools` 的 BeforeDispatch 钩子产出 `ABORTED_BEFORE_DISPATCH`
  （这是既有实现，不在本次范围）。

---

## 4. 并发收益实测

用例：`internal/agent/parallel_test.go TestSixGrepCallsRunInParallelAndKeepModelOrder`
（同一工具名 `grep`、6 条不同参数、每条睡 120ms；`grep` 声明 `alwaysSafe`，与真实 grep 一致）。

| 配置 | 墙钟 | 同时峰值 | tool 消息顺序 | `tool/result` 事件顺序 |
|---|---|---|---|---|
| 默认（`DSHD_MAX_PARALLEL_TOOL_CALLS` 未设 → 10） | **130.7531ms** | 6 | c1..c6 == 模型顺序 | c1..c6 == 模型顺序 |
| `DSHD_MAX_PARALLEL_TOOL_CALLS=1` | **732.0867ms** | 1 | c1..c6 | c1..c6 |

- **提速 5.6×**（732.09 / 130.75）；断言：`parallel*2 < serial` 且 `serial ≥ 0.8×6×120ms`、`parallel ≤ 3×120ms`。
- 每条 tool 消息的正文与**自己的参数**一一对应（`p1..p6`），证明"乱序完成、顺序结算"。
- 另有用例：`TestUndeclaredToolsAreExclusive`（2×150ms 未声明工具 → 峰值 1、≥280ms）、
  `TestPerCallClassifierDecidesByArguments`（同工具只读参数峰值 2/<260ms；写参数峰值 1/≥280ms）。

---

## 5. 测试清单

`go test -count=1 -v ./internal/spi/ ./internal/approval/ ./internal/agent/ ./modules/tool/glob/ ./modules/tool/grep/ ./modules/tool/schedule/`
→ **204 PASS / 0 FAIL**（`tmp/GO8-RC2/d3-verbose.log`；红证据 `tmp/GO8-RC2/d3-red.log`、`d3-red2.log`）。

| 文件 | 新增/改动 | 用例 |
|---|---|---|
| `internal/spi/tool_test.go`（新） | + | `TestConcurrencySafeDefaultsToExclusive`、`TestConcurrencySafeHonoursClassifier`、`TestConcurrencySafeReceivesPerCallArguments`、`TestConcurrencySafePanicIsExclusive`、`TestOfficialErrorIdentities`、`TestResultWithErrorInfoKeepsExistingFields` |
| `internal/agent/parallel_test.go` | 改探针 + 新增 | 探针加 `safe func(args) bool`；新增 `TestUndeclaredToolsAreExclusive`、`TestPerCallClassifierDecidesByArguments`、`TestSixGrepCallsRunInParallelAndKeepModelOrder`、`TestApprovalOverridesDeclaredConcurrencySafety`、`TestCancelledToolResultCarriesAbortIdentity`、`TestSuccessAfterCancellationBecomesAborted`；既有 11 个用例全部保留并改成显式声明（`sleepTool`/`readOnly`/取消探针） |
| `internal/approval/denial_test.go`（新） | + | `TestDenialIdentitiesMatchOfficial`、`TestDenialIsBothErrorAndResultIdentity` |
| `modules/tool/glob/glob_test.go` | + | `TestGlobDeclaresConcurrencySafe` |
| `modules/tool/grep/grep_test.go` | + | `TestGrepDeclaresConcurrencySafe` |
| `modules/tool/schedule/schedule_test.go` | + | `TestScheduleListIsConcurrencySafeWritesAreNot`、`TestScheduleWithoutSessionCarriesToolContextUnavailable` |

**证据文件**：`tmp/GO8-RC2/d3-red.log`、`d3-red2.log`、`d3-green1.log`、`d3-verbose.log`、
`d3-broad.log`（modules 全量：38 ok / 1 fail=office 他人中间态）、`d3-cmd.log`（cmd/dshd 全量）、
`d3-isolated-cmd.log`（隔离实验）、`d3-backup/`（隔离实验用的文件备份）。

**先红后绿证据**：

1. `tmp/GO8-RC2/d3-red.log` —— 新用例先写，5 个包 **build failed**（`undefined: spi.ConcurrencyClassifier` /
   `ConcurrencySafe` / `Resu1t.Info` 等），exit 1。
2. `tmp/GO8-RC2/d3-red2.log` —— SPI 落地、调度器未改时的**行为红**：
   `TestUndeclaredToolsAreExclusive`（"同时跑了 2 个，必须是 1 个"）、
   `TestPerCallClassifierDecidesByArguments/写参数独占`（"同时跑了 2 个"）、
   `TestSixGrepCallsRunInParallelAndKeepModelOrder`（保序断言暴露探针没声明安全）、
   `TestCancelledToolResultCarriesAbortIdentity`（`Info:<nil>`）、
   `TestSuccessAfterCancellationBecomesAborted`（`Info:<nil>`）。
3. `tmp/GO8-RC2/d3-green1.log` / `d3-verbose.log` —— 实现后 **全绿**。

---

## 6. 接线行（待他人接线）

### 6.1 错误身份 → v4 `tool/result`（owner：session/transport 线）

| 文件:行 | 现状 | 要做什么 |
|---|---|---|
| `cmd/dshd/main.go:1169` | `Append("tool/result", session.ToolResultEventWithView(...))` | `e.Result.Info != nil` 时改用 `session.ToolResultEventWithError(..., session.TurnEndError{Name: info.Name, Code: info.Code})`（官方 `error` = `info`，`batch.js:238`） |
| `internal/session/error_codes.go:80-88` | `knownErrorCodes` 四族 | 加 `APPROVAL_REJECTED/CANCELLED/UNAVAILABLE`、`TOOL_CONTEXT_UNAVAILABLE`、`ABORTED`、`ABORTED_BEFORE_DISPATCH` 两族（码面登记，不是闸门） |
| `internal/transport`（如需） | 工具结果不外传 `error` | 只在 UI/客户端要读 `error.info` 时才扩展；四字段错误契约**不要**动 |

### 6.2 审批三态（owner：cmd/dshd 审批线）

| 文件:行 | 现状 | 要做什么 |
|---|---|---|
| `cmd/dshd/acp.go:672-673` | `Hint + fmt.Errorf("用户拒绝了 %s")` | `approval.Rejected("用户拒绝了 "+name).Failed(res)` |
| `cmd/dshd/acp.go:669` | 策略 deny | `approval.Unavailable("当前模式的审批策略是 deny")` |
| `cmd/dshd/acp.go:662-663` | ask 返回错误（客户端断开） | `approval.Unavailable("审批通道失败")` |
| `cmd/dshd/acp.go:646-648` | sandbox 一次性审批无通道 | `approval.Unavailable("没有审批通道")` |
| `cmd/dshd/serve_approval.go:466-491` | `CancelSession` → `reply(false)`，与"人拒绝"不可区分 | 取消路径要能区分 `APPROVAL_CANCELLED`（例如在 payload 里带 `cancelled:true`，或由 acp 侧按 session cancel 状态判定） |
| `cmd/dshd/serve_approval.go:101-110` | 超时按拒绝结算 | 建议归 `APPROVAL_UNAVAILABLE`（**本仓选择，无官方逐字对应**） |

`internal/approval/denial.go` 已提供唯一构造点：`Rejected/Cancelled/Unavailable(reason)`、`Denial.Info()`、
`Denial.Failed(res) (spi.Result, error)`。

### 6.3 并发声明（owner：各工具域）

| 文件:行 | 工具 | 要做什么 |
|---|---|---|
| `modules/tool/read/read.go` | `read` | `func (t *Tool) ConcurrencySafe(json.RawMessage) bool { return true }` |
| `modules/tool/readimage/readimage.go:38` | `read_image` | 同上 |
| `modules/tool/webfetch/webfetch.go:51` | `web_fetch` | 同上 |
| `modules/tool/websearch/websearch.go:153` | `web_search` | 同上 |
| `modules/exec/bash/tools.go:466` | `job_list` | 同上 |
| `modules/tool/subagent/subagent.go` | `subagent` | 同上（官方 `subagent` 也是 true） |
| `modules/tool/teamtask/teamtask.go:49` | `team_task_list` / `team_task_get` | 同上（**他人域**，正被改） |
| `modules/tool/team/team.go:28` | `list_agents` | 同上（**他人域**，正被改） |
| `modules/tools/mcp.go:1240-1242` | `list_mcp_resources` / `list_mcp_resource_templates` / `read_mcp_resource` | 给这三个工具各加声明（**他人域**） |
| `modules/tool/office/office.go`（在建） | `office_render` = true、`office_convert` = **false**、`office_recalculate` = **false** | 建的时候就要写（**他人域**） |
| 尚未实现 | `cordis_inspect_list/query`、`schedule_view`、`list_subagent_models`、`session_trace/session_event_trace/session_event_read` | 实现时必须声明（官方全部 true） |

> **接线方式**：加一个方法即可，不需要动 `spi.Tool` 接口；`spi.ConcurrencySafe` 自动生效。
> 声明前先确认该工具**并发安全**：不写共享可变状态、不写同一路径、不抢同一通道。

---

## 7. 已知偏差 / 未验证 / 风险

| 项 | 状态 | 说明 |
|---|---|---|
| `-race` | **UNVERIFIED（环境限制）** | 本机 PATH 无 gcc，`CGO_ENABLED=1 go test -race` 报 `cgo: C compiler "gcc" not found`。以 `-count=1` 全量 + 既有 goroutine 泄漏断言（`TestBatchCancellationStopsEverythingWithoutLeak`、`TestToolPanicPropagatesOnCallerGoroutine`）替代；有 C 工具链的机器需补跑 `scripts/race.sh` |
| D1 双车道 vs 官方屏障 | 有意差异 | 见 §2.3；不变量"审批单线程"优先 |
| D2 一次分类 | 有意差异 | 见 §2.3；一轮内 `Loop.Tools` 不可变 |
| 审批超时 → `UNAVAILABLE` | 本仓选择 | 官方无逐字对应；三态之外只有 `unavailable` 语义最接近"没有人能回答" |
| 未声明工具变慢 | 预期副作用 | 默认独占意味着 `read`/`web_fetch` 等**在接线前**从并发池退回独占车道：更快是接线后的事，安全性先保住。清单见 §6.3 |
| `modules/tools/pipeline.go:62` | **既有缺陷，只报** | `ToolAbortedBeforeDispatch = "TOOL_ABORTED_BEFORE_DISPATCH"` 是官方**常量名**；官方值是 `"ABORTED_BEFORE_DISPATCH"`（`contracts.js:21`）。且身份写在 `Result.Data` 而不是 `Result.Info`。不在我的写作用域 |
| `modules/tool/office/office.go:290` | **他人中间态红** | `undefined: base64Std`（office 任务线半写状态）。`go test ./modules/...` 因此 38 ok / 1 fail；与本次改动无关（我未 touch office） |
| `cmd/dshd` 3 个失败 | **他人中间态红（已隔离证明）** | ① `TestToolParityWithDSH`：`parity_test.go:233/236` 与 DSH 工具契约不一致（`send_message` 缺 `agent` 等）→ team 工具线；② `TestWriteToolsDeclareCaps`：`snapshot_test.go:207` "能力表里的 `interrupt_agent` 没有出现在工具表里" → 工具表/能力表不同步；③ `TestMergedRoutesToOriginalTool`：`tool_schema_test.go:588` `team_task action=update` 缺 `expected_revision` → teamtask 线。**隔离证据**：把本次 12 个文件全部还原到 `HEAD` 后单跑这 3 个用例，**仍然全红**（`tmp/GO8-RC2/d3-isolated-cmd.log`），随后按备份原样恢复（逐文件 `diff` 一致） |
| `internal/spi/search_catalog.go`（untracked） | 非我改动 | 并行写者新增的文件，`git status` 里会出现在 `internal/spi/` 下；我的改动清单见 §8 |

---

## 8. 改动文件清单

| 文件 | 改动 |
|---|---|
| `internal/spi/tool.go` | +`ConcurrencyClassifier`（:67）、+`ConcurrencySafe`（:77）、+`ErrorInfo`（:116）、+9 个官方码常量（:126-154）、+`Result.Info`（:178）、+`Result.WithErrorInfo`（:195）；`Tool` 接口**未改** |
| `internal/spi/tool_test.go`（新） | 6 个契约用例 |
| `internal/agent/parallel.go` | +`toolExecutionMode`/`executionMode`（:96/:130）；`execToolCall` 拆 `failureOutcome`（:173/:214）并补取消身份/取消压过成功；`executeToolCalls` 按 mode 分组（:335/:347）；删 `blocksOnHuman`（逻辑并入 `executionMode`）；文件头不变量 2 改写 |
| `internal/agent/parallel_test.go` | 探针加 `safe`；+6 个用例 |
| `internal/approval/denial.go`（新） | `Code` 三值、`Denial`（error + `Info()` + `Failed`）、`Rejected/Cancelled/Unavailable` 构造器 |
| `internal/approval/denial_test.go`（新） | 2 个用例 |
| `modules/tool/glob/glob.go` | +`ConcurrencySafe`（:55，true） |
| `modules/tool/glob/glob_test.go` | +1 用例 |
| `modules/tool/grep/grep.go` | +`ConcurrencySafe`（:68，true） |
| `modules/tool/grep/grep_test.go` | +1 用例 |
| `modules/tool/schedule/schedule.go` | +`ConcurrencySafe`（:208，仅 `kindList`）、+`contextUnavailable`（:217）并接到 4 个工具的无会话路径 |
| `modules/tool/schedule/schedule_test.go` | +2 用例 |
| `internal/agent/loop.go` | **未改**：取消身份落在 `parallel.go` 的 `execToolCall`（`loop.go` 只有 `project`/`Event` 定义，无需要改动的判定点）；`Event.Result` 已能携带 `Info`，投影接线见 §6.1 |
