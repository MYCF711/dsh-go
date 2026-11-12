# C3 · mcpManager.probe / restart / watch（MCP 服务管理面）对齐报告

- 基线：官方 `0.2.2-rc.2`（`@deepseek-ai/dsh-mcp-manager@0.2.2-rc.2`）
- 本仓版本：`0.2.2-rc.2-gobeta.1`（未动版本号）
- 工作树：`.agents/worktrees/dsh-bdb66fbf-95ba`
- 写作用域（已遵守）：`internal/mcp/**`（新包）、`internal/transport/handlers_mcp_manager_rc2.go(+_test.go)`（新文件）、`modules/tools/mcp.go`、`modules/tools/mcp_client.go`
- 未触碰：`cmd/dshd/serve.go`、`AGENTS.md`、`CHANGELOG.md`、`docs/**`、其它域

---

## 1. 结论

| 方法 | 结论 | 真实现内容 |
|---|---|---|
| `mcpManager.probe` | **真实现** | 独立一次性 stdio 连接（新进程，不注册工具、不占 `serverName`、不动活实例）：`initialize` → `tools/list` 分页 → 有界关闭；整场预算 10s、翻页 16、工具名 512；失败按 `connect`/`list` 分阶段；`closeConfirmed`/`cleanupError` 独立报告；取消/超时都真的收场（杀掉真进程） |
| `mcpManager.restart` | **真实现** | 串行化生命周期操作；先 dispose 旧一代（`CloseWithBudget` 有界确认），再挂全新一代（新重连预算）；**清理未确认则拒绝替换**并发布 `failed/cleanup`；禁用条目只收不挂；未知 server / 已释放 / 取消都有确定错误 |
| `mcpManager.watch` | **真流实现** | `POST /api/mcpManager.watch` → `application/x-ndjson`，每行一份官方 `McpManagedServerView[]`；首帧 = 订阅时完整列表，之后每次状态/条目变化一份完整列表；消费慢时合并为最新；客户端断开或管理器释放即结束。**无定时器、无心跳、无假帧**（变异测试证明：改成定时重发快照，测试立刻红） |

三方法与既有 `mcp.list` / `mcp.reconnect` / `mcp.toggle` **共用同一份状态**：唯一的 `internal/mcp.Registry`。没有第二套状态机（见 §5）。

结构化未实现项集中登记在 §9。

---

## 2. 官方证据（file:line）

### 2.1 契约原件（权威）

`docs/evidence/official-method-contract-delta-0.2.2-rc.2.md`

| 段 | 行 | 内容 |
|---|---|---|
| `### mcpManager/probe` | 1732–1844 | 参数 `[0] name`（json，string）；结果 `oneOf` 两支：成功 `ok=true` + `serverInfo?` + `toolNames` + `truncated` + `durationMs` + `closeConfirmed` + `cleanupError?`（required：`ok,toolNames,truncated,durationMs,closeConfirmed`）；失败 `ok=false` + `stage∈{connect,list}` + `message` + `durationMs` + `closeConfirmed` + `cleanupError?`（required：`ok,stage,message,durationMs,closeConfirmed`） |
| `### mcpManager/restart` | 1846–1863 | 参数 `[0] name`；描述："Rebuild one server from its current entry: dispose the live instance (if any) and mount a fresh one with a fresh reconnect budget. Incomplete cleanup blocks replacement until the Host restarts."；结果 = `typert mode:'strict'` + `jsonSchemaError: 'typert tool schema: unsupported void value'`（即 **void**） |
| `### mcpManager/watch` | 1865–2125 | 无参数；结果 = `McpManagedServerView[]`：`{name, configured, status}`（三项全 required），`status` 是九分支 `oneOf`：`closed{result{closeConfirmed,cleanupError?}}` / `waiting-credentials{lastError}` / `connecting{attempt}` / `connected{toolCount,connectedAt}` / `reconnecting{attempt,maxAttempts,nextRetryAtMs,lastError}` / `failed{gave-up\|reconnect-disabled\|unclosed-generation, lastError?}` / `disabled` / `failed{load,lastError}` / `failed{cleanup,result}` |

### 2.2 官方源码

`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-mcp-manager/src/index.ts`

| 行 | 内容 |
|---|---|
| 202–213 | `instances` / `statuses` / `registrations` / `watchers` / `lifetime`(AbortController) / `tail`(settled operation tail) / `stopped` |
| 269–277 | `list()`：配置顺序 → 注册项 → 移除后留存的运行记录；无状态时 enabled→`connecting{attempt:0}`、否则 `disabled` |
| 288–305 | `register()`：插件生命周期注册（本仓未接入，见 §9） |
| 313–338 | `watch()`：`@Remote({ mode: 'stream' })`；`dirty` 标记 + `wake` 唤醒 + `yield this.list()`；`finally` 注销唤醒；abort/ dispose 结束流 |
| 347–355 | `restart()`：`@Remote`；入队后未知 server 抛 `mcp-manager: unknown server "x" — it is neither configured nor registered`，再 `remount` |
| 363–366 | `probeFromClient`：`@Remote('probe')`，把 signal 传给 `probe` |
| 379–403 | `probe()`：已释放/未知/配置正在变/清理未完整 → 抛；`probeServer(...)`；失败清理 → `failed/cleanup` 留存 |
| 388 | `configuration for "x" is changing` |
| 389 | `cleanup for "x" is incomplete; restart the Host` |
| 446–455 | `enqueue()`：串行化全部生命周期操作 |
| 481–493 | `remount()`：先 unmount（失败 → cleanup incomplete 抛错），再按 `enabled` 挂或置 `disabled` |
| 495–524 | `mount()`：`connecting{attempt:0}` → 成功后接状态 cell；启动失败 → `failed/load` |
| 526–540 | `unmount()`：retire → 等全部 probe → dispose → 留存清理失败 |
| 543–546 | `retainCleanupFailure()`：留存第一份诊断 + 未确认关闭 |
| 553–567 | `setStatus()`：未知条目 early-return；`deepEqualJson` 去抖；唤醒 watchers；`mcp-manager/status` 并行事件 |
| 574–603 | `toClientConfig()`：非严格启动（`failOnStartupError:false`）+ 逐条目预算 |
| 87–100 / 127–165 | `ServerEntrySchema` / `validateEntries`（跨传输字段规则） |

`.../dsh-mcp-manager/src/types.ts`：17–21 `McpManagedServerStatus`（+disabled/load/cleanup 三支）、24–31 `McpManagedServerView`、33–47 `mcp-manager/status` 事件（`@mode parallel`）。

`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-mcp-client/src/`

| 文件:行 | 内容 |
|---|---|
| `probe.ts` 20–32 | `McpProbeOptions`：`timeoutMs`/`maxPages`/`maxToolNames`/`signal` |
| `probe.ts` 35–41 | 默认值 `10000` / `16` / `512` |
| `probe.ts` 43–51 | 非法选项抛 `TypeError`（不夹取） |
| `probe.ts` 61–103 | `probeServer` 全流程；66 `clientInfo name='dsh-mcp-probe' version='0.0.1'`；74 `generation.run(...remaining())`；76 `getServerVersion`；82 只在声明了 `tools` 能力时列举；83–94 分页 + 双重截断；96 `durationMs` 在 close 之前取；98 取消文案 `'probe aborted'`；102 `{...result, ...await generation.close()}` |
| `generation.ts` 90–112 | `close()` → `{closeConfirmed, cleanupError?}`；110 `closeConfirmed = transport === undefined \|\| closeObserved`；107 清理有自己的预算 |
| `types.ts` 8–13 | `McpCloseResult`（关闭确认 / 清理错误相互独立） |
| `types.ts` 16–58 | `McpConnectionStatus` 五支（closed/waiting-credentials/connecting/connected/reconnecting/failed） |
| `types.ts` 60–87 | `McpProbeResult = (success\|failure) & McpCloseResult` |
| `status.ts` 35–107 | `McpStatusCell`：`current()`/`closure()`/`onChange()`，观察者异常被容纳 |
| `index.ts` 46/49 | `DEFAULT_TOOL_CALL_TIMEOUT_MS=60000`、`DEFAULT_CLOSE_TIMEOUT_MS=5000` |
| `index.ts` 151–156 | `ReconnectSchema` |
| `connection.ts` 47–52 | `RECONNECT_DEFAULTS = {enabled:true, initialDelayMs:500, maxDelayMs:30000, maxAttempts:10}`（与本仓既有默认值逐字相同） |
| `connection.ts` 70–95 | `resolveReconnectPolicy`：上/下界、`initial <= max`、`maxAttempts>=1` |

### 2.3 官方文档

- `tmp/GO8-RC2/official-rc2-docs/.../docs/config-catalog.md` 2536–2582：`McpManagedServerEntry` 全字段（`transport/command/args/env/cwd/url/headers/enabled/toolCallTimeoutMs/startupTimeoutMs/closeTimeoutMs/reconnect`）
- `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-mcp-manager/README.md`
  - 108：`list()` 语义（configured/status，清理失败行留存到宿主重启）
  - 110：`restart` 重置预算；**未确认关闭或清理错误会同时阻塞替换与该 server 的 probe**
  - 114：`watch(signal)` 订阅即完整列表、每次变化再来一份、合并未读变化、abort 只结束该流、dispose 结束全部流
  - 128/130/144：两个来源、单条串行 tail、probe 独立并在条目变化/释放时被取消
  - 182：probe 输出上限 16 页 / 512 名

---

## 3. 三方法契约表（逐字段）

### 3.1 `mcpManager.probe`

| 项 | 内容 | 落点 |
|---|---|---|
| 参数 | `name: string`（必填）；本仓额外容忍 `id`（host 级方法不需要会话，忽略） | `handlers_mcp_manager_rc2.go:159` |
| 成功结果 | `ok:true`、`serverInfo?:{name,version}`、`toolNames:string[]`（空也是 `[]`）、`truncated:boolean`、`durationMs:number`、`closeConfirmed:boolean`、`cleanupError?:string` | `internal/mcp/probe.go:119` |
| 失败结果 | `ok:false`、`stage:'connect'\|'list'`、`message:string`、`durationMs`、`closeConfirmed`、`cleanupError?` | 同上 |
| HTTP | 结果两条都 **200**（预期内失败也是结果，官方设计）；宿主侧拒绝（未知 server/未实现/已释放/清理未完成）才是错误码 | `handlers_mcp_manager_rc2.go:182`、`:281` |
| 门（与官方 manager.probe 一致） | 已释放 → `ErrDisposed`；未知 → `ErrUnknownServer`；清理未完成 → `ErrCleanupIncomplete`；本仓另两条：`transport != stdio` → `ErrUnsupportedTransport`、配置读取失败 → 原错 | `modules/tools/mcp.go:1499` |
| 预算 | 整场 10 000ms；翻页 16；工具名 512；清理预算取条目 `closeTimeoutMs`（默认 5 000ms） | `internal/mcp/probe.go:12`、`modules/tools/mcp.go:1499` |
| 不碰活实例 | 每次 probe 起**新进程**，不注册工具、不占 namespace、不动 `ServerPID` | `modules/tools/mcp.go:1560`（`stdioProbeStarter`） |

### 3.2 `mcpManager.restart`

| 项 | 内容 | 落点 |
|---|---|---|
| 参数 | `name: string`（必填） | 同上 |
| 结果 | 官方 void；本仓 `{"result":{"ok":true}}`（与既有 `mcp.reconnect`/`mcp.toggle` 同形，出口不接受 `result:null`） | `handlers_mcp_manager_rc2.go:202` |
| 语义 | 串行 → 判 unknown/disposed → `Stop`（有界确认）→ 清理未完成则发布 `failed/cleanup` 并返回 `ErrCleanupIncomplete`（**不挂新实例**）→ 禁用条目发布 `disabled` 后正常返回 → 否则 `Mount`（新预算），挂载失败原样返回并发布 `failed/load` | `internal/mcp/restart.go:34` |
| 串行化 | `MCPManager.lifecycleMu` 包住 restart/reconnect/toggle（官方 `tail` 的等价物） | `modules/tools/mcp.go:1524` |
| 错误码 | 未知 → 404 `not_found`；清理未完成/配置正在变 → 409 `busy`；已释放 → 503 `internal`；未实现 transport → 501 `not_implemented` | `handlers_mcp_manager_rc2.go:281` |

### 3.3 `mcpManager.watch`

| 项 | 内容 | 落点 |
|---|---|---|
| 参数 | 无（官方）；本仓允许空 body / `{}` / `{"id":…}`（忽略），其它字段 400 | `handlers_mcp_manager_rc2.go:159` |
| 帧 | 每行 = 官方 `McpManagedServerView[]`（**就是契约里那个数组**，不是包装对象） | `handlers_mcp_manager_rc2.go:228` |
| 语义 | 订阅即首帧当前完整列表；每次状态/条目变化再一份完整列表；消费慢 → 合并为最新一份（容量 1 的 latest-wins）；状态与上次相等不发帧 | `internal/mcp/registry.go:114`、`:71`、`:186` |
| 结束 | 客户端断开（`r.Context().Done()`）或管理器释放（registry.Close → close(chan)） | 同上 |
| 传输 | `Content-Type: application/x-ndjson; charset=utf-8`、`Cache-Control: no-store`、`X-Accel-Buffering: no`，逐帧 Flush | `handlers_mcp_manager_rc2.go:228` |
| 假流防护 | 无定时器/心跳；变异"每 100ms 重发快照"→ `TestMCPManagerWatchStreamsRealChanges` 红（证据 §6.3） | — |

---

## 4. 状态机

状态词表与分支 = 官方 `McpConnectionStatus ∪ McpManagedServerStatus`（`internal/mcp/types.go:27`/`:38`），`MarshalJSON` 逐分支输出官方 oneOf 形状（`types.go:173`），非法组合直接让 Marshal 失败（`types.go:93`）。

```
                        ┌───────────────────────────────┐
   mcp.json 有该条目 →  │ connecting{attempt:0}         │  ← 初始登记（enabled）
                        └───────┬───────────────┬───────┘
                       成功 │                    │ 握手/启动失败
                            ▼                    ▼
                  ┌──────────────────┐   ┌────────────────────────────┐
                  │ connected{       │   │ failed{reason:'load',      │
                  │  toolCount,      │   │        lastError}          │
                  │  connectedAt}    │   │  （官方 mount 失败口径）     │
                  └────┬────────┬────┘   └────────────────────────────┘
        子进程退出 │    │        │ toggle/restart/toggle-disable
                   ▼    │        ▼
   ┌────────────────────────┐   ┌─────────────────────────────┐
   │ reconnecting{attempt,  │   │ disabled                     │
   │  maxAttempts,          │   └─────────────────────────────┘
   │  nextRetryAtMs,        │
   │  lastError}            │──重连成功──► connected
   └───────┬────────────────┘
   预算耗尽 │            重连关闭（enabled=false）
            ▼                        ▼
   ┌──────────────────────┐  ┌────────────────────────────────────────┐
   │ failed{reason:       │  │ failed{reason:'reconnect-disabled',    │
   │  'gave-up',          │  │        lastError}                      │
   │  lastError}          │  └────────────────────────────────────────┘
   └──────────────────────┘
   关闭判定（stop/close）：
     closeConfirmed && 无 cleanupError → closed{result}
     首次未确认关闭                    → failed{reason:'unclosed-generation', result}
     已有留存失败                      → failed{reason:'cleanup', result}（留存不被后到的 confirmed 掩盖）
   词表里另保留 waiting-credentials（官方凭据模板态；本仓无凭据 provider，当前无生产者，见 §9）
```

转换的唯一写入口是 `mcpServer.publish(...)`（`modules/tools/mcp.go:270`；族函数 `publishConnected:282` / `publishLoadFailure:287` / `publishReconnecting:292` / `publishGaveUp:303` / `publishClosed:311`），它同时写本地诊断副本与 `Registry.Publish`。

---

## 5. "只有一份状态"的证据

| 读/写 | 路径 |
|---|---|
| 写 | `mcpServer.publish` → `Registry.Publish`（去抖 + 唤醒） |
| `mcp.list`（L1 旧面） | `MCPManager.Statuses()` 直接 `registry.List()` 再映射旧字符串（`modules/tools/mcp.go:738`、`legacyStatusName:1451`） |
| `mcpManager.watch`（新面） | `Registry.Subscribe` 同一份（`modules/tools/mcp.go:1482`） |
| `mcpManager.probe` 的清理失败留存 | 写在 `mcpServer.failure` 并经 `publish` 进同一 registry（`modules/tools/mcp.go:1499` → `publishClosed`） |
| `mcpManager.restart` 的清理失败 | `Registry.Publish(failed/cleanup)`（`internal/mcp/restart.go:34`） |
| `mcp.reconnect` / `mcp.toggle` | `connect()` → `publish(connecting)` → `publishConnected`（`modules/tools/mcp.go:528`） |

`mcpServer` 上不再有独立的 `status string/lastError` 字段（已随本任务删除），旧 `.status` 只作为 `mcp.Status` 诊断副本。

---

## 6. 先红后绿（含变异必红）

### 6.1 先红（测试先写、实现后补）

1. `internal/mcp` 测试写完、实现未写：
   `go test ./internal/mcp/` → **build failed**：`undefined: ServerInfo / CloseResult / ProbeSession / Status / Conn / ProbeResult`（日志 `tmp/GO8-RC2/c3-logs/red-1-build.log`）。
2. 实现后首轮真红（4 类）：
   - fixture `select{}` 被测试框架判"全部 goroutine 睡着"并 panic → 子进程当场死，测到的是"断线"而不是"卡住"；改 `time.Sleep` 循环。
   - 超时文案不匹配（`fixture 连接已断开` vs `timed out`）→ 归到 `probeDiagnostic` 三分类。
   - 取消用例的 stage 落到 `connect`（测试二进制启动 + initialize 往返 > 100ms）→ 取消点推迟到 800ms。
   - `maxToolNames` 截断的翻页次数预期错（官方要看到第 max+1 个候选才判截断，会多取一页）→ 修测试并注释理由。
3. transport 测试首轮真红：`/connect` 404 —— 测试客户端跟随了 303 到 `GET /`（404）；`CheckRedirect: ErrUseLastResponse` 修正（cookie 由 jar 在 303 上保存）。
4. 实现过程中 refactor 红：`cannot use "disconnected" as mcp.Status` 等（旧字符串状态替换成官方 `mcp.Status` 的中间态）。

### 6.2 绿（本次实跑）

| 命令 | 结果 |
|---|---|
| `go build ./internal/... ./modules/tools/` | exit 0 |
| `go vet ./internal/mcp/ ./internal/transport/ ./modules/tools/` | exit 0 |
| `go test ./internal/mcp/ -count=1` | `ok`（**62 PASS**，含子测试） |
| `go test ./internal/transport/ -run MCPManager -count=1` | `ok`（**17 PASS**） |
| `go test ./modules/tools/ -count=1` | `ok`（既有 790 行 MCP 测试全绿，无回归） |
| `go test ./internal/transport/ -count=1`（整包） | `ok dshgo/internal/transport 6.4s`（error-contract 源码扫描 `TestNoHandwrittenErrorBodies` 也绿） |
| `go run ./internal/mcp/testdata/c3harness` | `C3 HARNESS OK: probe=ok(2 tools,closeConfirmed) probe_unknown=404/four-field restart=pid-changed restart_old_pid_gone watch=real-frames(reconnecting->connected) toggle=disabled/connected close=no-orphan` |
| `gofmt -l`（本人文件） | 空 |

> 环境说明（不是本任务的红）：整包 transport 第一次全跑时 `TestOfficialSessionEventsEventAdmission` / `TestOfficialStreamEventAdmission` 短时红，那是同工作树另一 agent 在飞的 session/follow 事件接纳改动（`official_stream.go`/`handlers_session_official.go` 处于 `M` 状态）；把本人两个文件临时移出后重跑仍红、紧接着再跑即绿，最终整包 `ok`。本任务未改这两个文件。

### 6.3 变异必红（守卫自检）

| 变异 | 期望 | 实测 |
|---|---|---|
| `Registry.Publish` 去掉 `prev == st` 去抖 | `TestRegistryPublishDeduplicates` 红 | 红 |
| probe 去掉"最后一页仍有游标 → truncated" | `TestProbeTruncatesByPageCap` 红 | 红 |
| watch 改成每 100ms 重发当前快照（假流） | `TestMCPManagerWatchStreamsRealChanges` 红（"没有状态变化却收到一帧"） | 红 |

三次变异后均已还原并复跑为绿。

---

## 7. 真实进程控制

| 能力 | 实现 |
|---|---|
| 启动 | `mcpHandshake` 起子进程 + `initialize` + `notifications/initialized`；probe 用**独立**一条（`stdioProbeStarter`，`modules/tools/mcp.go:1560`） |
| 停止 / 清理确认 | `mcpClient.CloseWithBudget(budget, ctx)`（`modules/tools/mcp_client.go:323`）：关 stdin → `TerminateJobObject`（Windows 一次收全部后代）→ `Process.Kill` 兜底 → 在预算内等 `cmd.Wait`；**结果**为 `{closeConfirmed, cleanupError}`，重复调用返回首次判定（零值看起来像"未确认"，会让重启被永久拒绝） |
| 超时 | probe 整场预算 + 清理预算分离（清理用 `context.WithoutCancel` 派生，探针预算耗尽**不会**把清理也掐掉）；握手 `mcpHandshakeTimeout` 10s；工具调用 `toolCallTimeoutMs` |
| 取消 | probe 的 `signal`/ctx 取消 → 当前阶段失败 + 仍然 close；`watch` 用 `r.Context()`；`restart` 开始前已取消则不动进程 |
| 清理失败留存 | `mcpServer.retainCleanupFailure`：留存第一份 `cleanupError`，且后到的"确认关闭"不许掩盖先前的未确认；留存后 `probe`/`restart` 都被硬门拦住（官方同判据） |
| 孤儿进程 | harness 真机验证 `restart` 后旧 PID 消失、`Close` 后最终 PID 消失；`TestClientProbeTargetRealProcessTimeout/Cancel` 验证超时/取消后子进程被回收 |

---

## 8. 接线行（`cmd/dshd/serve.go` 未改，交由 Lead 一行合入）

`cmd/dshd/serve.go:494` 现状：

```go
HandlerWrap: frontendMount(front, officialWireMount(officialWireDeps{
    Home: *home, Journals: journals, Attachments: attachments, Events: approvalEvents,
})),
```

改为（**只改这一处**；`mcpServers` 是 `*cmd/dshd.mcpManager`，嵌入 `*tools.MCPManager`，四个方法自动提升）：

```go
HandlerWrap: transport.MCPManagerMountAround(transport.MCPManagerDeps{
    List:      mcpServers.List,
    Probe:     mcpServers.Probe,
    Restart:   mcpServers.Restart,
    Subscribe: mcpServers.Subscribe,
}, frontendMount(front, officialWireMount(officialWireDeps{
    Home: *home, Journals: journals, Attachments: attachments, Events: approvalEvents,
}))),
```

要点：
- `MCPManagerMountAround(deps, inner)` 与 `frontendMount`/`officialWireMount` 同型（`func(*transport.Server) http.Handler`），所以前端仍在最外层、official wire 次之、管理面在它们与 `Server` 之间；
- deps 全 nil 时 `MCPManagerMountAround` 直接返回 `inner`，**一个字节都不变**（默认不改变既有行为）；
- 想在开关后面才挂（例如 `DSHD_OFFICIAL_WIRE` 之外再加一个 env）只需在 deps 组装处判空即可，不必再动 transport。

可选（本任务**未做**，因为 `internal/transport/server.go` 不在写作用域）：为两个错误码补专属 hint，需要 `server.go` 两行 —
- `errorHint` 的 `switch` 加 `case "mcp_cleanup_incomplete": return "…；重启宿主后再试"`、`case "mcp_disposed": return "宿主正在停机，等它重启后再调用"`
- `retryableFor` 保持 `false`（默认分支即 false，无需加行）
- 同时把 `writeMCPManagerError`（`handlers_mcp_manager_rc2.go:281`）里 `KindCleanupIncomplete`/`KindDisposed` 的码换成新码。

---

## 9. 未实现项与显式差异（登记）

| # | 项 | 现状 | 缺什么 |
|---|---|---|---|
| 1 | `streamable-http` transport | 条目写 `transport: streamable-http` 时：`StartAll` 跳过并发布 `failed/load`（`UnsupportedTransport`），`probe`/`restart` 回 501 `not_implemented` + 四字段 | 缺 HTTP 传输实现（`Conn` 的 http 版）。这是**结构化未实现**，不是假成功 |
| 2 | `${credential:REF}` 模板 | 未实现；子进程沿用既有 `mcpChildEnv`（清洗凭据形状的父环境变量 + 显式 env 覆盖） | 缺凭据 provider 与模板展开（`McpTemplateBinding` 等价物） |
| 3 | `waiting-credentials` 状态 | 词表与 JSON 形状已实现（`types.go:27`） | 无生产者（依赖 #2） |
| 4 | 插件 `register()` 贡献 server | registry 支持 `Track(configured=false)` 形态 | 本宿主没有"插件运行时可注册 MCP server"的入口；无生产者 |
| 5 | `mcp-manager/status` 事件面 | 未实现 | 本仓无 cordis 并行事件总线；等价能力由 `watch` 流承担，不另造事件 |
| 6 | 条目变更实时 reconcile（官方 458–479） | 只有 `reconnect`/`toggle`/`restart` 会动实例；`mcp.json` 外部改动仍需重启宿主 | 缺配置热重载 → reconcile（既有行为，本任务未改） |
| 7 | `mcpManager.restart` 结果形状 | `{"result":{"ok":true}}`；官方是 void（typert unsupported void value） | 契约差异已登记；本仓出口禁止 `result:null` |
| 8 | `mcpManager.probe` 的可选参数 | 未暴露 `timeoutMs`/`maxPages`/`maxToolNames`（官方 Remote 面也不传，走默认值） | 若要开放，加 `mcpManagerParams` 三个可选字段并传给 `ProbeOptions` |
| 9 | `mcp.list` 旧状态字符串 | 保持 `connected/disabled/connecting/error`（映射 `legacyStatusName`，`modules/tools/mcp.go:1451`） | 官方九状态只从 `mcpManager.*` 出去，老客户端不被打断 |
| 10 | 错误码复用 | `cleanup-incomplete`/`config-changing` → 409 `busy`；`disposed` → 503 `internal` | 专属码需要改 `server.go`（写作用域外，见 §8 可选补丁） |
| 11 | `mcpManager/watch` 的 Accept 协商 | 永远 NDJSON（它本来就是 stream 方法） | 若客户端想先拿 unary 快照，用 `deps.List` 对应的入口（本任务未暴露 L1 方法） |
| 12 | `retainCleanupFailure` 的**失败分支**自动化覆盖 | 逻辑已实现（`modules/tools/mcp.go:325`；状态发布在 `:311`），正常分支由 harness 的 restart/toggle 覆盖 | 缺口：要走到失败分支需要一个"`Process.Kill` 也杀不掉、`cmd.Wait` 超预算"的子进程，真机上不可构造；该函数同时被 restart 路径调用，但没有独立的红测试 |
| 13 | 全仓门禁 | 未跑 `go build ./...` / `go test ./...` 全量 | 工作树与另外两个 agent 的在飞改动共享（`handlers_agent_team_rc2*`、`handlers_plugin_update_rc2_test.go`、account/forge/session 若干 `M` 文件）；本任务只对本包/受影响包跑门禁 |

---

## 10. 交付物清单（file:line）

新增：

- `internal/mcp/types.go`：27 状态词表、38 失败原因、47 `CloseResult`、64 `ServerInfo`、73 `Status`、93 `Validate`、173 `MarshalJSON`、220 `View`
- `internal/mcp/registry.go`：19 `Registry`、44 `Track`、71 `Publish`、92 `List`、99 `Lookup`、114 `Subscribe`、141 `Close`、186 `notifyLocked`（合并语义）
- `internal/mcp/probe.go`：12 默认值、34 `ProbeOptions`、49 `Resolve`、82 `ProbeSession`、96 `ProbeTarget`、101 `ProbeResult`、119 `MarshalJSON`、182 `ProbeServer`、224 `probeWithSession`、270 `probeDiagnostic`、287 `Conn`、297 `Starter`、304 `ClientProbeTarget`、343 `Connect`
- `internal/mcp/restart.go`：14 `Controller`、34 `Restart`
- `internal/mcp/errors.go`：12 `Kind`、21 哨兵错误、46 `codedError`（官方原文 + `errors.Is`）、88 `KindOf`
- `internal/mcp/*_test.go`：62 条 PASS（types 5 / registry 9 / probe 10 / restart 7 / errors 2 / 真进程 fixture 7 + 子测试）
- `internal/transport/handlers_mcp_manager_rc2.go`：29 方法常量（点号 + 认斜杠）、43 `MCPManagerDeps`、55 `MCPManagerMethods`、60 `IsMCPManagerMethod`、84 `MCPManagerMount`、94 `MCPManagerMountAround`、115 `ServeHTTP`、131 `serveMCPManagerAPI`（先认证后解析）、159 参数解析、182 probe、202 restart、228 watch（真流）、281 错误映射
- `internal/transport/handlers_mcp_manager_rc2_test.go`：17 条 PASS（含组合、认证、四字段、真流三态、客户端断开退订）
- `internal/mcp/testdata/c3harness/main.go`：真进程 + 真 HTTP 端到端 harness（`go run`，Go 工具链忽略 testdata）

修改：

- `modules/tools/mcp.go`：`mcpServer` 状态改为官方 `mcp.Status` + `publish` 族（270/282/287/292/303/311）、`stop` 返回可判定关闭（375）、`StartAll`（481）、`connect`（528）、`Statuses` 读 registry（738）、`Close` 留痕 + 关 registry（909）、管理面 API（1451 `legacyStatusName` / 1466 `Views` / 1470 `List` / 1482 `Subscribe` / 1499 `Probe` / 1524 `Restart` / 1535 `requireServer` / 1560 `stdioProbeStarter` / 1588 `mcpRestController`）、配置新增 `transport/url/headers/closeTimeoutMs` 与 `closeBudget()`（缺省行为逐字节不变）
- `modules/tools/mcp_client.go`：306 `Close`、323 `CloseWithBudget`（有界 + 判定 + 幂等记住首判）
