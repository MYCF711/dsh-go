# A2 追单：`PublishTerminalChange` 生产未接线 —— 评估与接线结论

> 上游基线 rc.5（`tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/`）。
> 本文件记录"该不该接"的完整判据、实测数字、接线范围与变异证据。
> 追溯对象：`docs/evidence/rc2-reports/A2-terminal.md` §3.8 / §6.2。

## 0. 结论

**接。** 但只接「身份/状态」类变更点（create / agent-create / close / rename / typed 接管），
**不接 resize、不接进程退出**——这两处接上会放大帧数或没有可用的事件点，收益 ≤250ms、
代价是每事件一帧，按铁律 13 属"有代价"⇒ 不做。**250ms 差分轮询保留**：它不只是延迟兜底，
还是"进程退出"的**唯一发现路径**，撤了会丢事件（见 §2.4）。

一句话判据：接线是「把已经存在的那一次重取**提前**」——没订阅者时零成本、
有订阅者时帧数由 payload 去重决定（不变），任何一处漏接也只是退回今天的样子（慢 ≤250ms）。
它不是"轮询 + 事件"的纯冗余叠加，但它**也不能**用来撤掉轮询。

## 1. 事实核对

### 1.1 `PublishTerminalChange` 定义与调用点

| 项 | 事实 |
|---|---|
| 定义 | `internal/transport/handlers_job_terminal_official.go:1612`，签名 `func (h *OfficialJobTerminal) PublishTerminalChange(sessionID string)` |
| 实现 | `h.terminalWatcher().Publish(sessionID)`（`terminalWatcher()` 在 `:1558` 懒建进程内单例事件源） |
| 生产调用点 | **0**。全仓 grep 只命中测试：`internal/transport/handlers_job_terminal_official_test.go:1345,1354,1362,1403,1545,1757` |
| 层是否接线 | 层是接的（`cmd/dshd/serve.go:989` `OfficialJobTerminal: officialJT.Handler()`）；缺的只有 **PTY 层 → handler 的推送回调** |
| `CloseWatch` | 同样无生产调用点（本单不改；见 §5 未验项） |

### 1.2 250ms 在哪：**宿主侧，不是客户端**

| 位置 | 事实 |
|---|---|
| 常量 | `internal/terminal/watch.go:57` `DefaultSetWatchInterval = 250 * time.Millisecond` |
| 节奏 | **定频**：`time.NewTicker(interval)`（`watch.go:335`），不随负载/订阅数自适应；每会话一个共享 goroutine（`watchSession.run`） |
| 谁在轮询 | 宿主 `SetWatcher`（`watch.go:143 Subscribe` / `watch.go:354 poll`），真值来源 = `terminal.list`（`handlers_job_terminal_official.go:1573 terminalSetSource` → `Svc.TerminalList`） |
| 客户端 | **不轮询**。GUI 每会话开一条 `terminal.watch` 长流并在 `NextRoster()` 上阻塞（`dsh-ui/cmd/app/terminal_wiring.go:643-651`），全文无 ticker（`grep NewTicker cmd/app/terminal_wiring.go` 无命中） |

⇒ 上一单的表述"客户端 250ms 差分轮询"**不准确**：轮询与事件都在宿主，
线上只有帧；所以"接线"只是让同一批帧提前到达，不增加线上流量类型。

### 1.3 官方：**推送**，不是轮询

| 官方位置 | 事实 |
|---|---|
| 监听器表 | `@deepseek-ai/dsh-api-terminal-controller/src/index.ts` `private rosters = new Map<SessionId, Set<() => void>>()`（`lib/index.js:1125`） |
| 订阅 | `async *watch(sessionId, signal)`：注册 listener → `dirty` 唤醒 → `yield this.list(sessionId)`（`src/index.ts:344-372`，编译产物 `lib/index.js:1265-1294`） |
| 广播 | `private changed(sessionId) { for (const l of this.rosters.get(sessionId) ?? []) l() }`（`src/index.ts:405-407`，`lib/index.js:1388-1389`） |
| `changed` 调用点 | create 成功 `src/index.ts:427`；close `:369`；owner 销毁 `:400`；进程退出 monitor `:424`；**任意状态发布** `:426`（`terminal.setStateListener`）⇒ rename `src/terminal.ts:321-324`、resize `:152`、typed 接管 `:138`、退出 `:385` 全走它；分配失败 `:555`、`:560` |
| 载荷 | 永远是重新算的整份 `this.list(sessionId)` = `[...terminals, ...allocations].map(t => t.info)`（`lib/index.js:1173-1176`） |

⇒ 官方是**纯推送**，无轮询兜底。本宿主的形状必须自证：一个轮询、一个事件源，
接上后两者落到**同一份** `terminal.list` 真值（不建第二份状态）。

### 1.4 当前延迟的真实量级：250ms 是**最坏**，不是平均

定频 ticker ⇒ 变化点相对 tick 的相位均匀 ⇒ 延迟 ~ U(0, 250ms]。

**实测**（本单新写探针，`internal/terminal` 包内临时测试，n=40，相位用随机 sleep 打散，
数据 `tmp/GO8-RC5/_pt2/latency.txt`、日志 `tmp/GO8-RC5/_pt2/probe.log`）：

```
poll     n=40 avg=122.8ms p50=135.8ms p95=217.9ms max=236.0ms
publish  n=40 avg=  0.0ms  p50=  0.0ms  p95=  0.0ms  max=  0.0ms
```

⇒ 平均 ≈ 123ms、p95 ≈ 218ms、最坏 250ms（实测 236ms）；接推送后 <1ms（同进程内）。

### 1.5 用户可见面到底损失了什么（纠正"新建终端"的前提）

- **客户端自己建的终端不慢**：`terminal.create` 的响应直接回填本地 `state.public.Info`
  （`dsh-ui/cmd/app/terminal_wiring.go:443-448`，随后 `runTerminalWatch` 只在 roster 里"刷新"它），
  所以发起方立刻看到。
- **真正慢的是 agent 开的终端**：`TerminalOpenForAgent`（`cmd/dshd/terminal_pty_agent.go:106`，
  插入点在 `:203`）不经客户端 RPC，客户端只能等 roster；而 GUI 正是靠 roster 把
  agent 终端变成右栏 tab（`dsh-ui/cmd/app/terminal_wiring.go:710 syncAgentTerminals`）。
  **这是本单的主要收益点。**
- 次要：第二个窗口/客户端看同一会话；rename 与 typed 接管的徽标。
- `docs/evidence/rc2-reports/A2-terminal.md` §7 写的"agent 终端整条线未实现"**已过期**——
  `terminal_pty_agent.go` 已存在且是生产路径，本单的接线点因此比那份报告 §6.2 列的清单更宽。

## 2. 代价表（铁律 13 纯正向闸，逐条）

| 问题 | 答案 | 证据 |
|---|---|---|
| **会多发事件吗？多多少？** | **不会多发帧**，只多"重取"。`Publish` 无订阅者时是一次 map miss（`watch.go:208-222`，`sess == nil` 直接 return）；有订阅者时是容量 1 的**非阻塞**投递（合并）。帧数由 payload 去重决定：`mergePoll` 用 `bytes.Equal` 判定，相等就不广播（`watch.go:385-393`）。即"同一个变化"接线前后各 1 帧，只是早到 | 既有测试 `TestOfficialJobTerminal_TerminalWatchCoalescesToLatest`（`internal/transport/..._test.go:1373`，`WatchInterval: time.Hour` 只靠推送驱动）+ `..._test.go:1318`"集合没变不许发帧" |
| **会打乱既有轮询语义（重复处理/顺序错）吗？** | **不会**。tick 与 kick 由**同一个** `watchSession.run()` goroutine 串行消费（`watch.go:334-352`），`mergePoll` 在一把锁下做 revision 单调递增 + 去重；不存在并发重取、重排、重复帧 | 同上两条用例；`-race` 见 §4 |
| **会让本来不动的路径开始动吗？** | **不会**。没订阅者 ⇒ `Publish` 立即返回；事件源本身也是**懒建**的（`terminalWatcher()` 的 `watchOnce`），没调用过 watch 的宿主不多一个 goroutine、不多一次重取。空闲宿主零新增工作 | `watch.go:208-222`、`handlers_job_terminal_official.go:1558-1566` |
| **接线后 250ms 轮询能撤吗？撤了会丢事件吗？** | **不能撤，撤了会丢事件**。① 进程退出是**惰性发现**的：只有 `screenOf()` 里读 `pty.Exited()` 才会把 `entry.state` 改成 `exited`（`terminal_pty.go:1089-1095`），而 `screenOf` 只在 list/attach 读路径被调 ⇒ 撤掉轮询，"shell 自己退出"可能**永不**被发现；② 无人值守回收由后台 `sweepLoop` 直接 `delete(a.entries, key)`（`terminal_pty.go:491-496`），没有任何可挂的推送点。⇒ 差分器是**兜底兼发现机制**，不是纯延迟优化 | 本单实测：`WatchInterval: time.Hour` 下退出/回收永远不出现（探针里靠 Publish 才收到） |
| **是否"保留轮询 + 增事件"= 纯冗余增加流量？** | **不是**。轮询节奏与轮询流量接线前后**完全不变**（还是 4 次/s/被观察会话的 `terminal.list`）；新增的只是"变化点那一次重取"，而它本来也会在 ≤250ms 内被轮询做掉。所以不是"叠加两条通道"，而是"把同一条通道的触发点从定时改成事件，定时那条留作兜底" | 代码路径：`watch.go:334-352` 两种触发走同一个 `poll()` |
| **有没有人要为这个改动付什么？** | 有一处**必须付**、所以**不做**：resize。见 §3.2 | |

## 3. 接线范围

### 3.1 接了（对齐官方 `changed()` 的调用点）

| 变化点 | 位置 | 对应官方 |
|---|---|---|
| user 终端 create 成功 | `cmd/dshd/terminal_pty.go:680`（`TerminalCreate` 尾部） | `src/index.ts:427` |
| **agent 终端 create 成功** | `cmd/dshd/terminal_pty_agent.go:209`（`TerminalOpenForAgent` 尾部） | 同上（`allocate`） |
| close 完成 | `cmd/dshd/terminal_pty.go:765`（`TerminalClose`；`TerminalAgentClose` 委托到它 ⇒ 一处覆盖两条关闭路径） | `src/index.ts:369` |
| rename | `cmd/dshd/terminal_pty.go:814` | `src/terminal.ts:321-324` → `changed` |
| typed 接管（`control` agent→user） | `cmd/dshd/terminal_pty.go:873` | `src/terminal.ts:138` → `changed` |

接线机制（不用改 `serve.go`）：`officialPTYJobTerminalService.Handler()` 是拿到 handler 的
**唯一入口**，在 `cmd/dshd/terminal_pty.go:1265-1272` 里**同一个函数**既造 handler 又把
`setRosterPublisher(h.PublishTerminalChange)` 接上。官方形状也是"控制器自己既是 roster 所有者
又是监听器持有者"，拆成两处必然留一个能接错的位置；放进 `Handler()` 让它结构上只可能接对。
`rosterPublisher == nil` 是**合法状态**（事件源退回差分），所以任何遗漏都只降级成 ≤250ms。

### 3.2 **故意不接**（有代价 ⇒ 不做）

| 不接的点 | 理由 |
|---|---|
| **resize**（`terminal_pty.go:905` 区域 `Resize` 尾部） | resize 是**抖动型**变更：GUI 每次 xterm 尺寸变化就发一次 `terminal.resize`（`dsh-ui/cmd/app/terminal_wiring.go:240-287`，无 debounce），拖分栏 1 秒可产生几十次。接推送会把"每 250ms 一帧"放大到"每事件一帧整份集合"（GUI 每帧 `m.inv.Invalidate()`，`:687`），而**发起方已经本地回填了 cols/rows**（同文件 `:279-283`），别的观察者 250ms 内照样拿到 ⇒ 收益 ≈0、代价是帧数放大。官方推 resize 是因为它的客户端可能不持有尺寸；我们的 250ms 内到，不需要拿帧数换。 |
| **进程退出（`state=running→exited`）** | 没有可挂的事件点：退出是**惰性发现**的（`screenOf` 里读 `pty.Exited()`，`terminal_pty.go:1065-1100`），而 `screenOf` 正是 `terminal.watch` 真值来源 `TerminalList` 的内部调用 ⇒ 从那里推送等于"轮询自己触发自己"，只会多一次被去重的重取。要把它压到 0 需要真正的进程退出监听器（等价官方 `terminal.monitor`，`src/index.ts:424`），那是另一个改动。现状 ≤250ms。 |
| **retain** | 只动保留位与落盘；`retained` 不在 `info()` 的字段里（`terminal_pty.go:1101-1120`）⇒ 集合字节不变，报了也只会被去重成不发帧。 |
| **`sweep` 回收（unattended TTL）** | 分钟级事件，250ms 无关紧要；且回收路径 `delete(a.entries,key)` 在 `a.mu` 内，加推送要额外拆临界区，改动面大于收益。 |
| **`Close()` 整层销毁** | 服务在退出，订阅者随连接一起走（`dropSession`），没有观察者。 |

## 4. 验证

### 4.1 新增用例（`cmd/dshd/terminal_publish_wire_test.go`，LF）

| 用例 | 证明什么 | 判据为什么不靠时序运气 |
|---|---|---|
| `TestOfficialTerminalWatchPushesRosterChanges` | 走**生产接线**（`svc.Handler()`）；建终端 → 立刻收到含该终端的整份集合；改名 → 立刻收到新 title；关闭 → 立刻收到空集合；且整份集合的 rc.2 必填字段一个不少 | handler 的 `WatchInterval` 设 **1h**，差分检测在用例寿命内不可能兜底 ⇒ 来帧只可能来自推送 |
| `TestOfficialTerminalRosterPublishPoints` | 钉住**哪些**点报：create=1、resize 仍=1（不报）、retain 仍=1（不报）、rename=2、typed 接管=3、close=4 | 直接注入计数出口，不经网络、不经轮询，纯同步断言 |

### 4.2 变异（造红，逐条跑过，跑完已复原）

| 变异 | 结果 |
|---|---|
| A：注释掉 `Handler()` 里的 `setRosterPublisher` | `TestOfficialTerminalWatchPushesRosterChanges` **红**：`新建终端后的集合：流提前结束（没有新集合；推送没接线或没落在变化点上）`（5.04s 超时） |
| B：删掉 `TerminalCreate` 的 `publishRosterChange` | 两条都**红**：前者同上；后者 `terminal.create 之后推送次数应为 1，实际 0（[]）` |
| C：在 `Resize` 上加 `publishRosterChange`（即"照抄官方"的那版） | `terminal.resize 之后推送次数应为 1，实际 2（[s1 s1]）` ⇒ 用例守住了"不放大帧数"这条边界 |

### 4.3 门禁

```
go build ./...                                        EXIT=0
go vet ./...                                          EXIT=0
go test ./internal/terminal/ ./internal/transport/    ok 16.4s / ok 7.7s
go test ./cmd/dshd/                                   ok 305.9s   （全包，非子集）
go test -race ./cmd/dshd/ -run 'TestOfficial|TestPTY' ok 73.6s    （0 DATA RACE）
go test -race ./internal/transport/ ./internal/terminal/ 见下
```

日志：`tmp/GO8-RC5/_pt2/{build,vetall,unit,cmddshd,race,mutA,mutB,mutC}.log`。
未删/未放宽任何既有测试与断言。

## 5. 未验项与遗留（如实记录，不冒充通过）

1. **真机时延未测**：上面的 0ms 是进程内事件源到订阅者的时延；真实链路上还要加
   WS/NDJSON 写端与客户端解码，未在真机 GUI 上量"tab 出现"的端到端时间（预计 +1 帧写盘时间）。
2. **`CloseWatch()` 仍无生产调用点**：服务关停不显式关事件源；最后一批订阅者断开时
   `dropSession` 会摘掉会话与 goroutine（`watch.go:278-296`），所以不是泄漏，但"关停时显式
   释放"这条没做（本单不动，避免顺手改）。
3. **`serve.go:989` 若有人再调一次 `Handler()`**，后一个 handler 会覆盖前一个的推送出口
   （last-wins，已在 `Handler()` 注释里写明）；当前生产只有一处调用。
4. `docs/evidence/rc2-reports/A2-terminal.md` §6.2/§7 的两处描述已被本单取代
   （§6.2 的清单更宽、§7 的"agent 终端未实现"已过期），未回改那份报告以免动历史证据。