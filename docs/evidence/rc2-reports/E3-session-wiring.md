# E3 · 会话层三条接线（官方 0.2.2-rc.2）

> 任务：`task-18`（RC2-E3）。基线 `0.2.2-rc.2`，版本号 `0.2.2-rc.2-gobeta.1`（未改）。
> 写作用域：`internal/transport/server.go`、`internal/agent/child_session.go`、
> `cmd/dshd/{main.go,serve_tools.go,acp.go}` 及对应 `_test.go`，
> 另新增 `cmd/dshd/turn_error_codes.go`（+ `_test.go`）作为取码唯一入口。
> **未 commit / 未 push。**
> 行号是接线后**实测**行号（`grep -n` 核对过）。

## 0. 结论摘要

| # | 条目 | 状态 | 一句话 |
|---|---|---|---|
| 1 | 空 prompt 续跑（官方 `conversationContinuation` 下游） | **已接线（行为完整）**，`source.kind=retry` 的**落盘**待两处仓外接线 | `handleSessionPrompt` 现在认官方 `mode`、按 durable 事件判准入、拒绝回 `session/agent-busy` + `details.reason`、通过 `PromptContinuation` 把 `retry` 交给宿主 |
| 2 | `subagent/catalog` v3 写入 | **已接线** | 具名委派在子会话日志里落 v3 事实（`version:3` + `name` + `label`），**派发前**用 B3 的两个官方校验器自检；四类拒绝都有用例 |
| 3 | 错误码落盘不折叠 | **已接线（四个写入点全部）** | `turn/end` 与 `tool/result` 的 code 从**真实错误对象 / `spi.Result.Info`** 取，取不到就保持旧形状，**不编 UNKNOWN** |

测试：新增 **17 个测试函数 / 33 个 PASS 点**（接线 + 先红），FAIL=0（详见 §6）。

## 1. 空 prompt 续跑（`internal/transport/server.go`）

### 1.1 接线点

| 位置 | 内容 |
|---|---|
| `internal/transport/server.go:867` `handleSessionPrompt` | 请求结构新增官方 `mode`；先算 content 形状，再决定"续跑判定"还是旧路径 |
| `server.go:90` `Options.PromptContinuation` | **新增 additive 宿主入口**：`func(sessionID, sourceKind, placement string) (PromptResult, error)` |
| `server.go:957` `promptPlacementOf` | 官方 `mode`（`queue`/`steer`）只在 placement 缺省时生效；显式 placement 优先 → canonical 客户端行为不变 |
| `server.go:979` `promptContentShape` | `(content 长度, 是否有正文或附件)`；空 text+空 blocks = 长度 0 = **续跑**，非空 text 按一个块计 |
| `server.go:1008` `serveEmptyPromptContinuation` | 准入判定：`DurableEvents` → `OfficialPromptAdmissionFor`；`Fault != nil` → 按 `Fault.Code`/`Status` 写错误信封 + 平铺 `details` |
| `server.go:1043` `submitPromptContinuation` | 优先 `Options.PromptContinuation`；否则退回 `PromptPlacement("", …)`；都没有 → 501（`ErrPlacementUnsupported`） |
| `server.go:1066` `officialPromptInboxPending` | `nextTurnPending` = `LiveState.QueueIDs` 非空（promptService 的真实队列）；`nextStepUserPending` = durable 推导（下一条） |
| `server.go:1074` `officialNextStepUserPending` | 本轮最后一个 `step/start` 之后还有 `source.kind=user` 的 `user/message` → 它等着下一步边界 |
| `server.go:1097` `officialUserMessageSourceKind` | 读一条 `user/message` 的 `source.kind` |
| `server.go:1498`(`retryableFor`) / `:1510`(`errorHint`)（`session/agent-busy` 分支见 `:1502`/`:1524`） | 新增 `session/agent-busy` 的 hint 与 `retryable=true`（与 `busy` 同类） |

官方证据（逐条，均在 `tmp/GO8-RC2/official-rc2/.../dsh-api-session-controller/lib/index.js`）：
`:4147` 只有**非空** content 才查正文；`:4152` 空 content 的 `source.kind='retry'`；
`:895` 三重前置 + `details.reason='NO_CONTINUABLE_FAILURE_OR_PENDING_INPUT'`。
`mode` 值域 `'queue' | 'steer'`：`typert.host.js:1512` 与 `:3803`（`SessionPromptRequest` 必填四字段）。

### 1.2 `details` 放在信封顶层（有意，不是漏）

本仓错误对象**必须恰好四字段**（`TestEveryHTTPErrorHasFourFieldEnvelope` 逐键钉死），
所以官方 `result.error.details` 落在信封**顶层**的 `details` 键，与 `stale_epoch` 的
`journalEpoch/lastSeq` 同一先例。客户端拿 `{"error":{code,message,hint,retryable},"details":{"reason":"…"}}`。

### 1.3 三条判据的"真值"来源

| 官方字段 | 本仓真值 | 是否写死 |
|---|---|---|
| `continuation.ending` | `host.DurableEvents` → `session.EvaluateContinuation`（B3 的投影折叠） | 否 |
| `inbox.nextTurn` | `OfficialSessionHost.LiveState().QueueIDs`（= `promptService.queues` + now 保留项） | 否 |
| `inbox.nextStep` 有 user 消息 | durable 事件推导：本轮最后 `step/start` 之后仍有 `source.kind=user` 的 `user/message`（steer 注入尚未被消费） | 否（不是常量 false） |

### 1.4 测试（`internal/transport/server_prompt_continuation_test.go`，8 个函数 / 14 PASS 点）

| 用例 | 断言 |
|---|---|
| `TestPromptEmptyContentRejectedWhenNoContinuableFailure` | 非可续码 → 409 `session/agent-busy`、`retryable=true`、`details.reason` 逐字、**不交给 runner** |
| `TestPromptEmptyContentAcceptsContinuableFailure`（6 个可续码，逐码子测试） | 200 `accepted:true`，提交参数 `sourceKind=="retry"`、`placement=="followup"`（官方 queue 的落点） |
| `TestPromptEmptyContentRequiresQueueMode` | `mode:"steer"` 与缺省 mode → 409，且不续跑 |
| `TestPromptEmptyContentBlockedByRealPendingQueue` | `QueueIDs` 非空 → 409（写死 false 过不了这条） |
| `TestPromptEmptyContentBlockedByPendingNextStepUser` | 跑步中一轮里 step 后有 user 消息 → 409 |
| `TestPromptContentWhitespaceRejectedButEmptyAllowed` | 全空白 content → 400 `bad_request`（官方只拒这一种） |
| `TestPromptOfficialModeMapsPlacement` | `mode` → followup/steer；缺省 → turn；显式 `placement` 优先 |
| `TestPromptRejectsUnknownMode` | 未知 mode → 400，且不调 runner |

## 2. `subagent/catalog` v3 写入（`internal/agent/child_session.go`）

### 2.1 接线点

| 位置 | 内容 |
|---|---|
| `child_session.go:100` / `:105` | `ChildSpawnOptions.Name` / `Label`（官方 v3 的 `name` 与 continuable 的 `label`） |
| `child_session.go:169` `childCatalogIdentity` | 名字来源：显式 `Name` → `AgentID`（仅当它本身就是合法 agent 名）→ 都没有则不写 catalog（匿名委派字节不变） |
| `child_session.go:196` `childCatalogPreflight` | **派发前**自检：`ValidateSubagentCatalogPayload`（正则/保留名/continuable label）+ 把候选事实接进父会话事件后交给 `ValidateSubagentCatalogGeneration`（同代唯一） |
| `child_session.go:228` `parentCatalogEvents` | 读父会话 durable 事件（读不到就不判重，只校验载荷规则） |
| `child_session.go:291` | preflight 在建子会话**之前**调用：拒绝时不留空子会话 |
| `child_session.go:336` | `lg.Append(session.EventSubagentCatalog, session.SubagentCatalogV3(...))` |

官方证据：v3 是 native V4 唯一写法（`dsh-subagent/src/catalog.ts:40`）；名字正则与保留名
（`src/names.ts:13,23-25`）；continuable 必带 label（`catalog.ts:76-79` 的 zod refine）；
同代唯一（`src/names.ts:76-114` 的 `SiblingNameReservations.reserve` +
`dsh-session-format-v3-to-v4/lib/index.js:1037-1046`）。

### 2.2 为什么写在**子会话**日志而不是父会话（与官方的有意偏差）

官方是 `parent.append('subagent/catalog', …)`（`catalog.ts:155`）。本宿主的 spawn 发生在
父会话那一轮**正在跑**的时候，父日志由那一轮持着写锁（`session.OpenSessionAt` 抢的是同一把
writer claim），从 spawn 里再开一次父日志会直接失败。子会话日志才是本函数独有的写入端；
而本宿主的目录投影本来就是**扫描 durable 子会话**得到的
（`cmd/dshd/serve_session_api_t04.go` 的 `SubagentCatalog`），事实放子会话里同样可读。

**代价（已登记，见 §5）**：同代唯一只能对**父会话日志里可见**的事实判重；兄弟事实在各自
子会话里，跨子会话重名不会被这里拦住。

### 2.3 测试（`internal/agent/child_session_catalog_test.go`，3 个函数 / 7 PASS 点）

| 用例 | 断言 |
|---|---|
| `TestSpawnRejectsInvalidCatalogName/大写开头…` | `^[a-z][a-z0-9-]{0,31}$` 拒绝，且会话数不增加（拒绝在建子会话前） |
| `…/保留名_lead` | 保留名拒绝 |
| `…/同代重名` | 父日志已有 `helper` 事实 → 拒绝（`duplicate catalog child name`） |
| `…/continuable_缺_label` | 无 label 且无 AgentID → 拒绝 |
| `TestSpawnWritesCatalogV3` | `subagent/spawn` 仍是第一条事件；写下的 v3 事实过得了官方校验器；`name=reviewer`、`mode=continuable`、`label` 非空、`childId/childCreatedAt` 与落盘记录一致 |
| `TestSpawnAnonymousKeepsLegacyLog` | 匿名委派不写 catalog（既有日志字节不变） |

## 3. 错误码落盘不折叠（`cmd/dshd`）

### 3.1 接线点（四个生产写入点）

| 写入点 | 改动 |
|---|---|
| `cmd/dshd/main.go:1170`（`cmdRun` 的 `tool/result`） | `ToolResultEventWithView` → `toolResultEventOf(...)`（失败且带身份时用 `session.ToolResultEventWithError`，并补回 `view`） |
| `cmd/dshd/main.go:1263`（`turnCloser.Exit`） | 真失败且错误对象带码 → `writeFailure`（`:1293`）走 `session.TurnEndErrorData`；取消/超时/无码错误仍走 `TurnEndData` |
| `cmd/dshd/serve_tools.go:564`（`settleTurn` 宿主补写结局） | `session.TurnEndData` → `settleTurnEndPayload(...)`（带码时 `TurnEndErrorData`，`message` 保留原来那句解读） |
| `cmd/dshd/acp.go:464`（ACP 的 `tool/result`） | 同 `main.go:1170` |

取码唯一入口：**新文件** `cmd/dshd/turn_error_codes.go`
（`turnEndErrorOf:82`、`settleTurnEndPayload:118`、`toolFailureOf:137`、`toolResultEventOf:165`）。
取码顺序全部来自**真实对象**：

1. `spi.Result.Info{Name,Code}` —— 官方 `error.info` 的 Go 等价物（`internal/approval/denial.go`
   的 `ApprovalError`、`internal/agent/parallel.go` 的 `AbortError`、`modules/tool/schedule`
   的码）。**此前落盘时整条丢掉**，这是本轮补上的真实缺口；
2. `interface{ Code() string }` —— 官方 `HarnessError{code}` 的 Go 形状
   （`internal/llm` 的两个 typed error 已是此形；`SubprocessError`/`JobError`/
   `TerminalBackendCleanupError` 落地时同形即自动生效）；
3. `interface{ AttachmentError() (string,int) }` —— 本仓 `attachmentStoreError` 已实现；
4. 都取不到 → **保持旧形状**（`TurnEndData` / `ToolResultEventWithView`），不编 `UNKNOWN`。

`view` 在带 error 的路径上**必须补回**（`ToolResultEventWithError` 签名不含 view），已补。

### 3.2 测试（`cmd/dshd/turn_error_codes_test.go`，6 个函数 / 12 PASS 点）

| 用例 | 断言 |
|---|---|
| `TestTurnEndExitKeepsRealErrorCode`（4 类各一子测试） | `AttachmentError→ATTACHMENT_CORRUPT`、`SubprocessError→SUBPROCESS_RUNNER_FAILED`、`JobError→JOB_NOT_FOUND`、`TerminalBackendCleanupError→CLEANUP_FAILED`：`turn/end.reason.error.code` 逐字相等、`name` = 类型名、`!= UNKNOWN` |
| `TestTurnEndExitWithoutCodeKeepsLegacyShape` | 无码错误不出现 `code` 键（宁缺不编） |
| `TestTurnEndExitCancelledStaysAborted` | 取消仍 `aborted` + `reason`，不带 error 对象 |
| `TestSettleTurnEndKeepsErrorCode` | 补写的 `turn/end` 保留 `CLEANUP_FAILED` 且 message 保留解读前缀 |
| `TestToolResultKeepsErrorCode`（2 子测试） | `Result.Info` 的 `JOB_LIMIT_REACHED` 与错误对象的 `ATTACHMENT_READ_FAILED` 都原样落盘；`message.isError=true` 同时成立；`view` 不丢 |
| `TestToolResultWithoutIdentityKeepsLegacyShape` | 无身份时不写 `error` 键 |

## 4. 先红证据

`tmp/GO8-RC2/_e3-red/red.log`（**隔离的旧实现**，不碰共享源码；目录以 `_` 开头所以 `./...` 不会扫到，
带 `e3red` build tag，按需显式跑：`go test -tags e3red ./tmp/GO8-RC2/_e3-red/`）：

```
--- FAIL: TestOldTurnEndWriteDropsCode
    旧写入点丢掉了真实 code：{"reason":{"error":{"message":"附件坏了","name":"Error"},"kind":"error"},"turn":1}
--- FAIL: TestOldToolResultWriteDropsErrorIdentity
    旧写入点没有 error 身份（官方 error.info 全丢）：{"message":{…"isError":true…},…}（无 error 键）
--- FAIL: TestOldSpawnWritesNoCatalogV3
    旧实现不写 subagent/catalog，但期望里要求它存在：这是 v3 写入的缺失证据
```

另两条的"红"是**入口不存在**（与 B3 报告同形的编译红/源码红）：

- 条目 1：接线前 `handleSessionPrompt` 既没有 `mode` 字段、也没有任何准入分支
  （`git diff internal/transport/server.go` 显示本轮新增 `promptPlacementOf`/`promptContentShape`/
  `serveEmptyPromptContinuation`/… 全部为新符号）；空 content 会一路落到
  `PromptBlocksPlacement` → `cmd/dshd/prompt_content.go:200` 的 `content 块数量必须为 1..64`
  拒绝——那个 400 正是官方 `:4147` 要放宽的。
- 条目 2：接线前 `Spawn` 只写 `subagent/spawn` + user + assistant，v3 事实根本不存在
  （第三条红用例即此）。

## 5. 未实现项 / 需裁定项（显式登记）

| 项 | 状态 | 卡在哪 | 解除条件 |
|---|---|---|---|
| `source.kind="retry"` 的**durable 写入** | **未接线** | 落盘 `user/message` 的是 `cmd/dshd/serve_turn.go` 的 `persistModelEvents`，而 `session.UserMessage()` 把 `source.kind` 写死成 `"user"`（`internal/session/log.go:145`，B3 定稿区）；本轮写作用域既不含 `serve_turn.go`/`serve.go`/`transport_prompt.go`，也不含 `internal/session/**` | ①`internal/session` 加一个 additive 构造器（`UserMessageWithSource(kind, blocks)`）；②`transport_prompt.go` 加 `SubmitContinuation`；③`serve.go` 一行接 `PromptContinuation`；④`serve_turn.go` 把 sourceKind 透传到 `persistModelEvents`。①②④都要 Lead 重派（跨两个 owner 的写作用域） |
| 续跑被接受但没有 `PromptContinuation` 时 | 退回 `PromptPlacement("")`：**仍然续跑**（一轮跑起来），但 durable 消息是 `{content:[{text:""}], source.kind:"user"}`，不是官方 `{content:[], source.kind:"retry"}` | 同上 | 同上 |
| `subagent/catalog` 事实的**归属** | 写在子会话日志（不是父日志） | 父日志由包含本次 spawn 的那一轮持锁（见 §2.2） | 若将来宿主给"父会话写入"一条不抢锁的通道（例如由 loop 在 step 边界代写），再迁回父日志 |
| 跨子会话**重名**判重 | 只对父日志里可见的事实判重 | 兄弟事实各自在子会话日志里 | 同上（迁回父日志后自然成立） |
| 传输面 `subagentCatalog` 暴露 `name` | 未做 | `cmd/dshd/serve_session_api_t04.go:344` 的 `SubagentCatalog` 只给 `{id,createdAt,mode}`，补 `name` 要读子会话事实 | 另派（`cmd/dshd/serve_session_api_t04.go` 不在本轮写作用域） |
| `inbox.nextStep` 的完整投影 | 用 durable 推导，不建独立 inbox 投影 | 本宿主没有 next-step inbox（`serve_queue_official.go` 顶部声明） | 若将来引入 next-step inbox，只换 `officialPromptInboxPending` 一处 |

## 6. 验证（本机实测，2026-11-12 06:0x–06:2x）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go vet ./internal/transport/ ./internal/agent/ ./cmd/dshd/      # EXIT=0（06:2x 末次窗口）
go build ./internal/agent/ ./internal/transport/ ./cmd/dshd/    # EXIT=0
go test ./internal/transport/ -count=1                         # ok  6.298s（整包；06:2x 复跑）
go test ./internal/agent/ -count=1                             # ok  5.013s（整包；06:2x 复跑）
go test ./cmd/dshd/ -count=1                                   # 2 FAIL，均与本轮无关（见下）
go test ./cmd/dshd/ -run 'TestTurnEnd|TestSettleTurnEnd|TestToolResult' -count=1   # ok（12 PASS 点）
```

| 范围 | 测试函数 | PASS 点 | FAIL |
|---|---|---|---|
| 条目 1（transport） | 8 | 14 | 0 |
| 条目 2（agent） | 3 | 7 | 0 |
| 条目 3（cmd/dshd） | 6 | 12 | 0 |
| 合计新增 | **17** | **33** | **0** |

`go test ./cmd/dshd/` **整包**（06:1x 窗口，239.571s）跑起来了：**2 个 FAIL，都与本轮无关**——
`TestToolTableSnapshot` 与 `TestWriteToolsDeclareCaps` 都指向队友新加的 `schedule_edit` 工具
（"新工具 schedule_edit 没有认领能力"），是工具表快照待更新的既有红，不是本轮的写入点。
本轮的 12 个 PASS 点在该整包里全绿。
06:0x 与 06:2x 还各撞到一次队友半写窗口（`modules/tool/schedule`：`undefined: normalizeTarget/
rruleNext/applyTarget/utcStamp/pickID/defaultPageSize`、`t.editLegacy` 参数不匹配；
`modules/exec/bash/tools.go:460` 的 `!` 运算符误用、`internal/transport/handlers_job_terminal_official.go`
的 `undefined: jobs`、`internal/settingsform` 的 `undefined: deepCloneMap`），那是并行走廊的
正常噪声，重跑即可；**本轮改动本身在这三个包上一直是 `build`/`vet`/`test` 全绿**
（06:0x–06:1x 三个绿窗口，报告 §6 的数字全部来自这几次真实运行）。
`go build ./...` 当前两处红同样不是本轮的：
`modules/tool/schedule`（同上）与 `tmp/GO8-RC2/d3-backup/internal/agent`（队友的备份副本，
被 `./...` 扫到；`tmp/` 下副本建议排除）。

## 7. 风险 / 兼容

1. **空 content 的语义变了**：以前空 text 会当成"一条空消息"或 400；现在它走**续跑判定**，
   不可续 → `session/agent-busy`(409)。既有 RPC 客户端若真靠"空 text 起一轮"，
   会看到 409 而不是新轮——这是官方 rc.2 的行为，非本仓自选。
2. **`mode` 只补缺省**：显式 `placement` 仍然优先，canonical 25 个方法的行为面不变。
3. **catalog v3 前向不兼容**（B3 已裁决）：rc.2 起写的会话旧宿主读不了；本宿主 v0/1/2/3 全可读。
4. **catalog 不写匿名委派**：匿名委派的子会话日志与改动前逐字节一致（`FindChild` 等读者零改动）。
5. **`tool/result` 从"没有 error 键"变成"有 error 键"**：只在失败且能取到官方身份时发生
   （此前 `spi.Result.Info` 整条丢失）。读侧 `validateSessionEventData` 要求
   `error` 与 `message.isError=true` 同时成立——写侧已同时给出（`ToolResultEventWithError`）。
6. **`turnCloser` 多一条出口**（`writeFailure`）与 `write` 共用 `closed`：同一轮仍只有一条 `turn/end`。
