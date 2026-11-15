# A2 · `terminal.watch` 新方法 + terminal attach/create/list 的 rc.2 结构契约

> 取证口径：官方 rc.2 只读镜像 `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`，rc.1 对照树 `tmp/GO8-RC2/official-rc1/...`。
> 行号一律按 `grep -n` / `read` 核过的**实际文件行**；宿主路径相对 `DSH-GO` 仓库根（本任务在 worktree `.agents/worktrees/dsh-bdb66fbf-95ba` 内完成）。
> 门禁（build/vet/test/race）在 **`git archive HEAD` 隔离副本 + 本次 4 个文件** 上跑（`/tmp/rc2-a2-gate`），日志见 §8：共享树上其他并行任务的文件正在半写，不能当门禁。

## 0. 结论摘要

1. **`terminal.watch` 是 stream，不是 unary**：`dsh-api-terminal-controller/lib/typert.host.js:637` 描述符 `mode: 'stream'`（`:640`），`cancellation: {parameter:'signal'}`（`:661`）；`TYPERT.model` 的签名为 `@Remote({ mode: 'stream' }) async *watch(sessionId: SessionId, signal: AbortSignal): AsyncIterable<WebTerminalInfo[]>`（`:742`）；客户端面 `lib/typert.remote-client.js:637-640` 同 `mode: 'stream'`。**一次性帧/单帧响应都不算实现**。
2. **载荷是"整个 roster 的 `WebTerminalInfo[]`"，不是终端输出字节**：官方 `watch` 每次 `yield this.list(sessionId)`（`lib/index.js:1281`），而 `list()` 返回 `[...terminals, ...allocations].map(t => t.info)`（`lib/index.js:1173-1177`）。输出字节属于 `terminal.attach`（`lib/index.js:562` 的 snapshot/output/state 帧）。所以本任务把"观察输出/状态"实现为**观察会话内终端的产生、消失与已发布状态变化**，**没有**往 watch 里塞任何假输出——那会违反官方 result schema。
3. **rc.2 的字段变更只落在 `WebTerminalInfo` 上**：`origin`、`control`（必填）+ `sandboxMode`（可选、仅 agent 终端），`state` 枚举顺序调整；**四个方法的参数逐字节零变化**（`contracts-rc1.json` vs `contracts-rc2.json` 对比，见 §2.1）。`terminal/watch` 在 rc.1 完全不存在（新增方法）。
4. **实现是真的**：会话级真值来源（`Svc.TerminalList`）→ 每会话**一个**共享差分器 → 打开即当前全集、内容变了才发整份、拉模型天然背压（只记版本号，不排队）、取消可释放、断线重连即当前全集。另有推送加速钩子 `PublishTerminalChange`（宿主接线后延迟从 250ms → 一次快照读）。
5. **`cmd/dshd/serve.go` 不用改**：`internal/transport/server.go:592` 已经用 `Handles(name)` 单点分派，`terminal.watch` 加进 `Handles` 后 `/api/terminal.watch` 就是通的（有走真实路由 + 真实 cookie 的测试证明）。可选加速接线见 §6。
6. **未实现项全部结构化**：agent 终端整条线（`openForAgent`/`agentLaunch`/`confine`/`agentShell`/`AgentTerminal` API）与 `control` 状态机迁移——本宿主**没有** agent 终端创建路径，`control` 恒为 `user`，所以"接管"是已达成状态；wire 上的唯一入口 `write.typed` 只做形状校验（非法值 400，不静默吞）。逐条见 §7。

## 1. 官方权威契约（逐字段 + file:line）

### 1.1 `terminal/watch` 描述符（`lib/typert.host.js:637-668`）

| 项 | 值 | 证据 |
|---|---|---|
| 方法名 | `terminal/watch` | `typert.host.js:637` |
| service | `terminalController` | `:638` |
| 描述 | "Stream the Session's retained terminals as whole sets, without resolving or activating an Agent." | `:639` |
| **mode** | `'stream'` | `:640` |
| invocation | `{ kind: 'direct' }` | `:641` |
| 参数 | 只有 `sessionId`，`source: 'json'`，`typeSymbol: @deepseek-ai/dsh-session/types#SessionId`，`codec.mode: 'strict'`，jsonSchema `{"type":"string"}` | `:642-660`（参数名 `:644`，`source: 'json'` `:646`） |
| **作用域** | **无 `scope` 字段**（对比 `terminal/attach` 有 `scope: {context:'agent', wire:'agentId'}`）→ 这个方法是**按 sessionId 直接寻址，不解析 Agent** | 对照 `:670-680`（attach 的 scope）|
| 取消 | `cancellation: { parameter: 'signal' }` | `:661` |
| 结果 | `z.array(WebTerminalInfo)`，`mode:'strict'`，typeSymbol `…#terminal/watch:result` | `:662-667`（schema 定义 `:173-190`） |
| 结果描述 | "the current set, then a new set after creation, removal or any published state change." | `:664` |
| 签名 | `@Remote({mode:'stream'}) async *watch(sessionId: SessionId, signal: AbortSignal): AsyncIterable<WebTerminalInfo[]>` | `:742` |

### 1.2 结果元素 `WebTerminalInfo`（rc.2，`typert.host.js:173-190`）

| 字段 | 类型 / 取值 | 必填 | 证据 |
|---|---|---|---|
| `id` | string | ✅ | `:175` |
| `title` | string | ✅ | `:176` |
| `shell` | `{path:string, args:string[], name:string}`（三者全必填） | ✅ | `:177-181`（`args` `:179`） |
| `cwd` | string，描述 "Initial working directory; shell directory changes do not update this field." | ✅ | `:182` |
| `cols` | number | ✅ | `:183` |
| `rows` | number | ✅ | `:183` |
| `state` | `running \| failed \| exited` | ✅ | `:184` |
| `exitCode` | `null \| number` | ✅（值可为 null） | `:185` |
| `error` | string | ❌ 可选（`.optional()`） | `:186` |
| **`origin`** | `user \| agent`，描述 "Party that created the terminal." | ✅ | `:187` |
| **`control`** | `user \| agent`，描述 "Input owner; an agent terminal passes to the user on the user's first typed input and never returns." | ✅ | `:188` |
| **`sandboxMode`** | `read-only \| workspace-write \| danger-full-access`，描述 "Sandbox mode an agent terminal started under; user terminals run with system-user permissions and omit it." | ❌ 可选（`.optional()`） | `:189` |

zod 里没有显式 `.required(...)`：zod object 的**非 optional 属性默认必填**，所以 `origin`/`control` 必填、`sandboxMode` 可选（与 jsonSchema 的 `required` 数组一致：`contracts-rc2.json` 的 `required` 为 `[id,title,shell,cwd,cols,rows,state,exitCode,origin,control]`）。

### 1.3 流语义（实现 `lib/index.js:1265-1294`）

| 语义 | 证据 |
|---|---|
| 打开即当前全集：`let dirty = true`，循环第一轮直接 `yield this.list(sessionId)` | `index.js:1267`、`:1278`（`while (!lifetime.aborted)`）、`:1281`（`yield this.list(sessionId)`） |
| 只在变化时再发一帧：`dirty=false` 后阻塞在 `await new Promise(...)`，靠 listener 唤醒 | `:1279-1288` |
| 事件源 = 每会话的 roster 监听器表；roster 变化点 `changed(sessionId)` 遍历唤醒 | 注册 `:1273-1275`；广播 `:1388-1389` |
| 没有自然结束：只有 `signal` 或控制器 lifetime abort 才退出；退出时 `finally` 摘掉 listener，最后一个 listener 走掉就删该会话的 roster（`rosters.delete`） | `:1266`（`AbortSignal.any([signal, this.lifetime.signal])`）、`:1278`、`:1289-1293` |
| 载荷永远重新计算（whole-set），不是增量、不缓存旧值 | `:1281` 每次都调 `this.list(sessionId)` |

### 1.4 消费面（证明"whole-set + 重新订阅"的口径）

| 消费方 | 证据 |
|---|---|
| 客户端面同 `mode:'stream'` | `lib/typert.remote-client.js:637-640` |
| liveshare 把它归为 `owner-only`（访问控制口径） | `dsh-experimental-liveshare/lib/index.js:6569` |
| `dsh-terminal-panel` 未直接调用 `terminal/watch`（它用 retain/attach + 自己的列表），所以不能拿面板代码反推 watch 语义——以 typert + host 实现为准 | `grep -rn "terminal/watch" dsh-terminal-panel/lib` 无命中 |

## 2. 四个方法的字段差异表（rc.1 → rc.2，逐字段）

对比来源：`tmp/GO8-RC2/contracts-rc1.json` vs `contracts-rc2.json`（同一 asar 两次抽取，字段级 JSON diff 脚本逐条比对）。

### 2.1 参数：**零变化**（三个既有方法）

| 方法 | rc.1 参数 | rc.2 参数 | 差异 |
|---|---|---|---|
| `terminal/create` | `[0] agent`（`source:'lookup'`）、`[1] request` = `{shellPath?, id, cols, rows}`（required: id/cols/rows） | 同上 | 无 |
| `terminal/attach` | `[0] agent`（lookup）、`[1] id`（json string） | 同上 | 无 |
| `terminal/list` | `[0] sessionId`（json string） | 同上 | 无 |
| `terminal/watch` | —（rc.1 无此方法） | `[0] sessionId`（json string） | **新增方法** |

> 也就是说：`terminal.attach/(create|list)` 的"结构契约变更"**全部在结果侧**，参数（含 wire 名 `agent`/`sessionId`）一个字都没动。这也是本任务不需要改参数解析的直接依据。

### 2.2 结果：`WebTerminalInfo` 逐字段（attach / create / list 三处同形）

| 字段 | rc.1 | rc.2 | 变化 |
|---|---|---|---|
| `id` | string, required | 同 | — |
| `title` | string, required | 同 | — |
| `shell.path/args/name` | object, required, `args` string[] | 同 | — |
| `cwd` | string, required | 同 | — |
| `cols` / `rows` | number, required | 同 | — |
| `state` | string, required, enum `["failed","running","exited"]` | required, enum **`["running","failed","exited"]`** | **仅顺序变化**（值集合不变） |
| `exitCode` | `null \| number`, required | 同 | — |
| `error` | string，可选 | 同 | — |
| **`origin`** | **不存在** | string, enum `user\|agent`, **required** | **新增必填** |
| **`control`** | **不存在** | string, enum `user\|agent`, **required** | **新增必填** |
| **`sandboxMode`** | **不存在** | string, enum `read-only\|workspace-write\|danger-full-access`，**可选**（user 终端省略） | **新增可选** |
| `required` 数组 | 8 项（到 `exitCode`） | 10 项（+`origin`、+`control`） | **+2** |

### 2.3 `terminal/attach` 的两处 info 都要改

`terminal/attach` 的 result 是 4 分支 `oneOf`：`snapshot` / `output` / `state` / `ack`（`contracts-rc2.json`）。其中 **`oneOf[0].info`（snapshot 帧）与 `oneOf[2].info`（state 帧）都带 `WebTerminalInfo`**，两处 `required` 都变成 10 项（`origin`/`control` 在第 9、10 位）。所以 attach 的对齐必须同时覆盖**首帧**和**终态帧**，只改 snapshot 会漏。

### 2.4 `terminal/watch`：新增（rc.1 无）

- 形态：stream（§1.1）；结果：`WebTerminalInfo[]`（§1.2）；参数：`{sessionId}`；**无 Agent 作用域**。
- 迁移说明：本仓既有实现无同名方法（`docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt` 里无 `terminal/watch`），所以是**纯新增**，不改既有方法名，符合"既有方法不改名、官方同名走新增"。

## 3. `terminal.watch` 实现策略

### 3.1 形态（wire）

- HTTP 面：`POST /api/terminal.watch`，`200 + application/x-ndjson`，**一行一帧 = 一整份 `WebTerminalInfo[]`**，与既有 6 条官方流方法同形状；正常结束 = 连接关闭；运行期失败 = 最后一行 `{"type":"error","error":{四字段}}`。
- mux 面：`OpenFrameStream(ctx, "terminal/watch", args)`（`internal/transport/handlers_job_terminal_official.go:1592` 起）——与 HTTP 共用同一台帧机，opening 帧预取，错误在 ready 之前变成 gateway 错误帧。
- 不是 unary：不返回 `{result:…}` 一次性帧（那会让客户端把"流正常结束"当成"只有一帧"）。

### 3.2 事件源（真值 → 变化 → 帧）

| 层 | 实现 | 位置 |
|---|---|---|
| 真值来源 | `h.Svc.TerminalList(sessionID)`（与 `terminal.list` 同一份真值，避免"面板有、列表没有"）→ 逐条校验 → `json.Marshal`（结构体字段顺序固定 ⇒ 同一集合必得同一串字节，可做差分） | `handlers_job_terminal_official.go:1509` `terminalSetSource` |
| 事件源 | `terminal.SetWatcher`：**每会话一个共享差分器**（N 个客户端看同一会话只有 1 个轮询器），首次差分前同步取一次真值；`Publish(sessionID)` 可把"可能变了"变成立刻重取 | `internal/terminal/watch.go`（`SetWatcher` `:93`、`Subscribe` `:143`、`Publish` `:208`） |
| 变化判定 | 规范化字节比较（`bytes.Equal`），**内容变了才发帧**；内容没变时 Publish 也不发帧（不许拿"重取了一次"冒充"变了"） | `watch.go:354` `poll` / `:364` `mergePoll` |
| 载荷 | 官方就是重新算一遍 `list()`；本实现每次变化帧都是**重新序列化的整份集合**，不缓存旧值、不发增量 | `handlers_job_terminal_official.go:1592` / `:1617` 帧机 |

### 3.3 首帧与断线恢复

- 首帧：`Subscribe` 在返回前**同步**取一次真值（`watch.go:143`），所以"打开即当前全集"不需要等第一个 tick；取不到就**不建立订阅**并结构化报错（不拿空集合冒充"没有终端"）。
- 断线恢复：官方 result **没有 `from`/游标参数**，恢复语义就是"重连拿现在是什么"。本实现每次订阅首帧 = 当前全集，天然满足；无需 journal、无需补发。

### 3.4 背压

- **拉模型**：帧由 `Next(ctx)` 按需从订阅取，HTTP 写端慢不会让宿主攒队列。
- **只记版本号**：订阅状态是 `seen int64`（`watch.go:426-448`），不排队——中间版本被合并（whole-set 语义下合法），**最新版本一定拿得到**；内存 O(订阅者数)，不随输出量增长。
- 有测试钉：三版连续变化后消费者只取一次 ⇒ 必须拿到最新版，且旧版不补发（`TestSetWatcherCoalescesAndKeepsLatest`）。

### 3.5 取消与资源回收

- 流取消 = 请求 ctx（HTTP 断开由 net/http 取消）/ mux 载体关闭 → `Next` 返回结束 → `Close()` 退订；
- 最后一个订阅者离开 ⇒ 该会话的差分 goroutine 立刻退出并从表里摘掉（`dropSession`），**没有"永远空转的会话"**；
- 订阅的会话 ctx 同时用于取消正在跑的快照读取（慢读不挂 goroutine）。
- 有测试钉：客户端断开后 `Subscribers==0` 且 `Sessions==0`（`TestOfficialJobTerminal_TerminalWatchCancelReleasesSubscription`）。

### 3.6 错误语义

| 场景 | 行为 | 理由 |
|---|---|---|
| 首帧真值失败（session 不存在 / 底层炸 / 能力缺失） | 写响应头**之前**回结构化四字段：404 `not_found` / 501 `not_implemented` / 500 `internal` | 不许用空集合冒充"没有终端" |
| 运行期瞬时失败（1~2 次） | 静默重试（不发帧） | 一次锁竞争不该让面板整块消失 |
| 运行期**连续** 3 次失败（`DefaultSetWatchMaxErrors`） | 发一帧 error 并结束流 | 面板停住必须能诊断，不能像"没有变化" |

### 3.7 为什么不是"假流"

- 没有硬编码帧、没有心跳空帧、没有按时间空转发帧；每一帧都是当次真值序列化结果。
- 无变化时**不发**帧（有测试：集合不变时 120ms 内必须收不到帧）。
- 缺能力时 501 而不是"零值成功"（`Svc=nil` 场景有测试）。

### 3.8 与官方的差异（唯一一条）与收敛路径

官方是**推送**（roster 监听器），本宿主当前没有 roster 变更通知点，因此默认用**差分**（250ms，`DefaultSetWatchInterval`）。差异只在**延迟**，不在语义（帧内容、首帧、取消、背压、恢复都一致）。宿主加 3~4 行 `PublishTerminalChange`（§6.2）即可把延迟压到 0，差分器退化为兜底。

## 4. rc.2 形状对齐实现（attach / create / list）

### 4.1 结构体与补齐点

| 改动 | 位置 |
|---|---|
| `OfficialWebTerminalInfo` 新增 `Origin`/`Control`/`SandboxMode`（`sandboxMode` omitempty、user 终端不出现） | `handlers_job_terminal_official.go:238-254` |
| `aligned()`：只填空值——`origin` 空 ⇒ `user`；`control` 空 ⇒ 跟随 `origin`；`shell.args` nil ⇒ `[]` | `:265` |
| `MarshalJSON`：在**序列化边界**统一补齐 | `:302` |
| 枚举校验 `validateOfficialWebTerminalInfo`（state/origin/control/sandboxMode），非法值 → **500**（宿主 bug，不是 501/400） | `:313` |
| create 结果 / list 元素逐条校验 | create `:1229`、list `:1302`（watch 的真值来源同样逐条校验 `:1524`） |

**为什么在 `MarshalJSON` 而不是各 handler**：`terminal/attach` 的 snapshot/state 帧由宿主帧机直接产出（`cmd/dshd/serve_terminal_api.go:742` 的 `terminalInfo()`）并交给本层**原样转发** `json.RawMessage`——handler 改不了字段。放在序列化边界是唯一能一次覆盖全部出口（create/list 的 unary、attach 的两处 info、watch 的整份集合）的位置。

### 4.2 默认值的事实依据（不是编的）

- 官方 user 终端：`origin:"user"`、`control:"user"`（`lib/index.js:1444-1445`，`userLaunch` 的 info 字面量）；agent 终端：`origin:"agent"`、`control:"agent"`、`sandboxMode: request.policy.mode`（`:1474-1476`）。
- 本宿主**只有用户终端**：`terminal.create` 的官方语义就是"Allocate a user shell … without Agent sandbox or approval restrictions"（`lib/index.js:1179` JSDoc），`terminal.open` 也是人开的；仓内没有 `openForAgent`/`agentLaunch` 对应的 agent 终端分配路径（`grep -rn "openForAgent\|agentLaunch" cmd/ internal/ modules/` 无命中）。
- 因此 `origin=user / control=user / 省略 sandboxMode` 是**当前事实**。将来接 agent 终端时宿主显式填这三字段即可；`aligned()` 只填空值、绝不覆盖（有测试：`origin=agent` + `sandboxMode=workspace-write` 原样保留）。

### 4.3 `control` 状态机在我们宿主里在哪、`typed` 接管有没有 wire 入口（回答 Lead 的问题）

- **状态机位置：不存在。** 本宿主没有 agent 终端，`control` 恒为 `user`（仓库内无 `control` 状态字段，`grep -rn "control" internal/terminal cmd/dshd/terminal_pty.go` 无终端控制权状态）。官方状态机是 `info.control` 的 `agent → user` 单向迁移（`lib/index.js:612-617`：`if (command.typed === true && this.info.control === "agent") { this.info = {...this.info, control: "user"}; this.broadcastState(); }`），且**永不回转**（`AgentTerminalError("user-control")` 在 `:67`/`:804` 拒绝 agent 再写）。
- **wire 入口存在，但只有一个**：mux 上行 `terminal/attach` 的 `write` 帧字段 `typed:true`（官方 uplink 命令；HTTP 面无上行通道）。本宿主的 attach 上行处理在 `cmd/dshd/serve_terminal_api.go:587` `Push`，它**不解析 `typed`**（解码结构体里没有该字段，`json.Unmarshal` 会静默忽略未知字段 = 丢字段）。
- **本次怎么处理**：在 transport 的上行外壳 `officialUplinkHeadStream.Push`（`handlers_job_terminal_official.go:1112`）加了 `validateOfficialUplinkTyped`（`:1135`）——`typed` 必须是布尔（否则 400 四字段），合法值放行、不迁移状态（因为**没有状态可迁**：本宿主终端本来就是 user 控制）。这样既没有"静默丢字段"，也没有"假装接管生效"。有测试 `TestOfficialJobTerminal_UplinkTypedIsValidatedNotDropped` 钉三条：`typed:true` 放行、`typed:"yes"` 拒绝、无 `typed` 放行。
- 真正的接管要在"宿主有 agent 终端"之后做（同时需要 `control` 落到 info + 迁移后广播 watch 帧）——列入 §7 未实现项。

## 5. 测试：先红 → 后绿

所有命令在隔离副本 `/tmp/rc2-a2-gate`（= `git archive HEAD` + 本次 4 个文件）上跑。

### 5.1 红（证明新测试真的咬住契约）

| 红 | 手法 | 结果 |
|---|---|---|
| **红-1：新 API 不存在** | 把实现换回 `HEAD` 版（rc.1 形状），保留新测试 | `go test ./internal/transport/ -run 'TestOfficialJobTerminal_TerminalWatch'` → `EXIT=1`，`h.HandleTerminalWatch undefined` / `h.PublishTerminalChange undefined`（`FAIL [build failed]`），日志 `/tmp/a2-red1.log` |
| **红-2：去掉 rc.2 字段补齐** | 实现里 `MarshalJSON` 不再调 `aligned()`（退化成 rc.1 形状） | `EXIT=1`，三条红：`TerminalWatchStreamsWholeSets`（`origin:"" control:""`）、`RC2TerminalInfoShape`（`terminal.create 的 origin/control 要是 user/user`）、`RC2AttachSnapshotInfoShape`，日志 `/tmp/a2-red2.log` |

### 5.2 绿（当前）

| 门 | 命令 | 结果 |
|---|---|---|
| build | `go build ./...` | `EXIT=0`（`/tmp/a2-final-build.log`） |
| vet | `go vet ./...` | `EXIT=0`（`/tmp/a2-final-vet.log`） |
| transport 全包 | `go test ./internal/transport/ -count=1 -v` | **PASS=530 FAIL=0 SKIP=0**，`ok … 4.6s`（`/tmp/a2-final-transport.log`） |
| terminal 全包（含真 PTY） | `go test ./internal/terminal/ -count=1 -v` | **PASS=20 FAIL=0 SKIP=0**，`ok … 19.1s`（`/tmp/a2-final-terminal.log`） |
| 宿主消费面 | `go test ./cmd/dshd/ -run 'Terminal|Official' -count=1` | `ok dshgo/cmd/dshd 48.2s`（`/tmp/a2-test-dshd.log`） |
| race（transport，含新 watch 用例） | `CGO_ENABLED=1 go test -race ./internal/transport/ -run TestOfficialJobTerminal -count=1` | **PASS=37 FAIL=0**（`/tmp/a2-final-race-transport.log`；MinGW `/c/toolchains/mingw64`） |
| race（terminal 事件源） | `CGO_ENABLED=1 go test -race ./internal/terminal/ -run TestSetWatcher -count=1` | **PASS=11 FAIL=0**（`/tmp/a2-final-race-terminal.log`） |

### 5.3 新增用例清单

transport（`internal/transport/handlers_job_terminal_official_test.go`）：

- `TestOfficialJobTerminal_TerminalWatchStreamsWholeSets` —— 首帧=当前全集；字段 rc.2 必填齐全；`origin/control=user`；`sandboxMode` 省略；**集合不变不发帧**；变化/新增/删除都发整份。
- `TestOfficialJobTerminal_TerminalWatchCoalescesToLatest` —— 背压合并到最新版、不补发旧版。
- `TestOfficialJobTerminal_TerminalWatchCancelReleasesSubscription` —— 客户端断开 ⇒ 订阅/会话都释放。
- `TestOfficialJobTerminal_TerminalWatchParamErrors` —— 缺参/空参/别名冲突/未知字段/多段 JSON 全 400。
- `TestOfficialJobTerminal_TerminalWatchUnwiredIs501` —— 未接线 501 且点名方法。
- `TestOfficialJobTerminal_TerminalWatchSourceErrorBeforeFirstFrame` —— 首帧失败 500/501/404，不造假空集合。
- `TestOfficialJobTerminal_TerminalWatchEmptySetIsArray` —— 空集合首帧是 `[]` 不是 `null`。
- `TestOfficialJobTerminal_OpenFrameStreamRoutesTerminalWatch` —— mux 面同一台帧机（opening 帧 + 变化帧）。
- `TestOfficialJobTerminal_TerminalWatchRouteIsWiredThroughServer` —— **走真实路由 + 真实 cookie**：`/api/terminal.watch` 能开流并收到变化帧（证明 `serve.go` 不需要改）。
- `TestOfficialJobTerminal_RC2TerminalInfoShape` / `_RC2AttachSnapshotInfoShape` / `_RC2TerminalInfoInvalidHostDataIs500` —— rc.2 字段形状、agent 终端值不被覆盖、非法枚举 500。
- `TestOfficialJobTerminal_UplinkTypedIsValidatedNotDropped` —— `typed` 形状校验。
- 既有用例加固（未放宽）：`TerminalCreateShape` 的必填字段清单加入 `origin/control`；`AllMethodsUnwiredIs501` 加入 `terminal.watch`。

terminal（`internal/terminal/watch_test.go`，11 条）：首帧当前全集、Publish 推送且无变化不发帧、合并到最新、取消释放、3 订阅者共用 1 次真值读、连续失败才上报（单次抖动不上报、恢复后继续）、首帧失败不建订阅、重连从当前全集开始、Close 结束一切、空 sessionId 拒绝、无来源 fail-loud。

## 6. 接线（Lead 集成）

### 6.1 必须改的接线：**无**

`terminal.watch` 已由既有单点分派覆盖，**不需要动 `cmd/dshd/serve.go`**：

```go
// cmd/dshd/serve.go:765（既有，只读）
OfficialJobTerminal: officialJT.Handler(),
```
```go
// internal/transport/server.go:592-594（既有，只读；Handles 已含 terminal.watch）
if s.opt.OfficialJobTerminal != nil && s.opt.OfficialJobTerminal.Handles(name) {
    s.opt.OfficialJobTerminal.Serve(w, r)
    return
}
```

mux 面同样不需要改：`cmd/dshd/serve_official_wire_stream.go:52` 的 `OpenOfficialFrameStream` 对 `Handles(...)` 的方法直接转给 `OfficialJobTerminal.OpenFrameStream`（`internal/transport/official_stream_face.go:160-165`），`terminal/watch` 走的就是这条。

### 6.2 可选（把 watch 延迟压到 0，各 1 行）

在宿主 roster/状态变化点各加一行 `jt.PublishTerminalChange(sessionID)`；不接也正确（差分兜底，慢 ≤ `WatchInterval`）：

| 变化点 | 位置 |
|---|---|
| create 成功 | `cmd/dshd/terminal_pty.go:482` `officialPTYTerminalAPI.TerminalCreate` 返回前 |
| close 完成 | `cmd/dshd/terminal_pty.go:626` `TerminalClose` |
| rename | `cmd/dshd/terminal_pty.go:682` `TerminalRename` |
| 进程退出（`state=running → exited`） | `cmd/dshd/terminal_pty.go:291`、`:311`、`:437`、`:839`（四处 `entry.state = "exited"`） |
| resize（cols/rows 变化） | `officialPTYTerminalAPI.Resize` |
| control 转移（将来有 agent 终端时） | attach uplink 的 `write.typed` 处理处 |

### 6.3 可选（关停时释放）

`server` 关停时调一次 `officialJT.CloseWatch()`（幂等）；不调也不会泄漏（最后一个订阅者走了会话自动摘掉）。

## 7. 未实现项与原因（结构化）

| 项 | 状态 | 原因 / 依据 | 现在 wire 上的表现 |
|---|---|---|---|
| agent 终端整条线（`openForAgent`、`agentLaunch`、`confine`、`agentShell`、`preferredShells`、`agentWrite`、`readScreen`、`inspectActivity`、`inspectForeground`、`markScreen`、`drain`、`maxAgentTerminals=4`） | **未实现** | 本宿主没有"Agent 请求终端"的入口：没有对应的模型工具、也没有 agent 作用域的终端分配路径（`terminal.create` 官方语义就是 user shell）。这些方法也**不是 wire 方法**（是 `AgentTerminal` handle 的方法，`typert.host.js` 的 methods 表里没有它们），所以不属于 223 方法面。 | 不出现；`origin` 恒 `user`、`control` 恒 `user`、`sandboxMode` 恒省略 |
| `control` 状态机迁移（`typed:true` 接管） | **无状态可迁** | 同上：没有 agent 终端就没有"从 agent 交给 user"这件事。wire 入口（mux uplink `write.typed`）已做形状校验（`:1135`），非法值 400，不静默丢。 | 合法 `typed` 被接受（语义上已达成），非法值 400 |
| 官方 roster **推送**（零延迟） | 差分 + 推送钩子 | 宿主目前没有 roster 变更通知点；加 §6.2 的 1 行即可收敛。 | 语义与官方一致，延迟 ≤250ms（可配置） |
| `dsh-terminal`/`dsh-terminal-bash` 的错误码面（`CLEANUP_FAILED`、`STARTUP_EXITED`、`STARTUP_TIMEOUT`、`SESSION_CLOSING`、`SESSION_EXITED`、`NO_SANDBOX`、`SUBPROCESS_INVALID_ARGV`、`AbortError`） | **未实现** | 这些码属于 terminal/attach 的**运行期错误帧**与 tool 结果错误码，落点在 `modules/exec/bash/**` 与 `cmd/dshd/terminal_pty.go`——**不在本任务的写作用域**（本任务只允许改 `handlers_job_terminal_official.go(_test.go)` 与 `internal/terminal/**`）。本层现在把这些失败翻成既有的 4xx/5xx 词表（`gateway/*`），不冒充官方码。 | 结构化四字段 + `gateway/*` 码 |
| `terminal/attach` 上行的 `write/resize` 完整官方语义 | 部分（既有实现） | 上行只在 mux 面存在（HTTP 无上行通道），这是既有事实；本任务只新增 `typed` 的形状校验，未改既有 ack/refusal 语义。 | `ack` 帧照旧 |
| watch 的 `inputWindow`（attach 才有的字段） | 不适用 | 官方 `terminal/watch` 的 result **没有** `inputWindow`（只有 attach 的 snapshot 帧有），所以 watch 不出现该字段是对的。 | — |

**没有任何"静默成功/假数据"路径**：`Svc=nil` ⇒ 501；首帧失败 ⇒ 4xx/5xx；宿主给出非法枚举 ⇒ 500；运行期真值来源连续失败 ⇒ error 帧结束流。

## 8. 证据文件

- 隔离门禁副本：`/tmp/rc2-a2-gate`（`git archive HEAD` + 4 个文件）
- 日志：`/tmp/a2-red1.log`、`/tmp/a2-red2.log`、`/tmp/a2-final-build.log`、`/tmp/a2-final-vet.log`、`/tmp/a2-final-transport.log`、`/tmp/a2-final-terminal.log`、`/tmp/a2-final-race-transport.log`、`/tmp/a2-final-race-terminal.log`、`/tmp/a2-test-dshd.log`
- 契约机读证据：`tmp/GO8-RC2/contracts-rc1.json` / `contracts-rc2.json`（字段 diff 见 §2）；人读版 `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md` §1（2193 行起的 `terminal/watch` 段）与 §3（`terminal/attach`、`terminal/create`、`terminal/list` 三张表）

## 9. 改动清单

| 文件 | 类型 | 内容 |
|---|---|---|
| `internal/transport/handlers_job_terminal_official.go` | 改 | `MethodTerminalWatch`(12 方法)、`OfficialWebTerminalInfo` rc.2 三字段 + `aligned`/`MarshalJSON`/`validate`、watch 帧机与 handler、`PublishTerminalChange`/`CloseWatch`、`typed` 校验、`Handles`/`Serve`/`OpenFrameStream`/`OfficialJobTerminalMethods` 接线 |
| `internal/transport/handlers_job_terminal_official_test.go` | 改 | 新增 12 条 watch/rc.2 用例 + NDJSON 客户端夹具；既有两条用例加固（未放宽） |
| `internal/terminal/watch.go` | 新 | `SetWatcher`/`SetSubscription`/`SetSource`：会话级 whole-set 变化事件源（共享差分器、合并背压、取消/回收、错误阈值） |
| `internal/terminal/watch_test.go` | 新 | 11 条事件源语义用例（含 `-race`） |

未改：`cmd/dshd/serve.go`、`AGENTS.md`、`CHANGELOG.md`、`docs/**`；未 commit / 未 push。
