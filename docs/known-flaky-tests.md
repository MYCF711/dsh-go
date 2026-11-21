# 负载型 flake 名录

> **这份文件为什么存在**：本会话陆续碰到**四个**「全量测试里红、单独跑全过」的用例。
> 它们的症状都是「某个数字恰好卡在整秒上」（30.01s / 62.86s / 21.39s），
> 而**根因各不相同**。这份名录记下每个的根因与已用的诊断方法，避免下次把它们当成新的 bug 重新查一遍，
> 也避免用「把 timeout 抬大」这种掩盖式修法。

## 判据：怎么一眼认出负载型 flake

| 信号 | 含义 |
|---|---|
| 失败耗时**恰好卡在整秒**（30.01s / 62.86s） | 撞的是某个**等待上限**，不是逻辑错 |
| 单独跑 `-count=3` 或 `-count=5` **全过** | 是并发负载打掉了等待窗 |
| 失败消息描述的是**测试自己的等待**（「没就绪」「被断开了」「没发出请求」） | 很可能是测试把「没等到」报成了「被测行为不对」 |

**抬 timeout 是不允许的修法** —— 它只是让问题更晚暴露，还拖慢 CI。

## 四个成员

| # | 用例 | 根因 | 状态 |
|---|---|---|---|
| 1 | `cmd/app` `TestTerminalHoldsTeardownReleasesEverything` | 假宿主 `terminal.watch` **只发订阅那一刻的快照**（tab1 还没 create 是空），客户端晚 3–8ms 读到它时终端已建好，壳的 roster 对账判 `closed`+`missingTerminal` 并取消视图 ⇒ **phase 永远到不了 connected**，顶满 30s | **已修**（假宿主改真订阅 + `waitStreams` 改事件驱动） |
| 2 | `internal/officialwire` `TestMuxHeartbeatKeepsResponsivePeer` | **把「读超时」当成「连接被断开」** —— 心跳 25ms、每片 `readFrame(50ms)`，负载下 ping 晚到几十毫秒属正常，旧代码把任何 `err` 都判成断开 | **已修**（`errors.Is(err, os.ErrDeadlineExceeded)` 时 `continue`；总窗口不变） |
| 3 | `plugins/dssidebar` `TestSettingsItemWriteFailureDrawsSaveFailed` | **等待写成按次数而非按时间**：`for i:=0;i<400;i++ { pump() }` 微秒就跑完，而 `pump()` 不让出调度 ⇒ 异步 goroutine 可能一次机会都没拿到 | **已修**（改时间上限 + `runtime.Gosched()`） |
| 4 | `internal/terminal` `TestPTYLongPasteExactBytes` | **未定性** —— 它等的是「子进程创建就绪文件」这个**真实异步事件**（`deadline 30s` + 10ms 轮询），逻辑上没有可疑处；单独 `-count=3` = 9.5s 全过，3 份并发单测也全过，**要全量 101 包同时压 PTY 才复发** | **未修**，见下 |

## 三个已修的，根因其实是同一类思维错误

| 用例 | 把什么混成了什么 |
|---|---|
| 1 | 过期快照的「**缺席**」混成「**已消失**」 |
| 2 | 「**读超时**」混成「**连接关闭**」 |
| 3 | 「**没等到**」混成「**没发生**」 |

**这和产品代码里修过的 `ok:false` 混同、`missingTerminal` 误判是同一个错误的两种表现** —— 一个在测试里，一个在产品里。

## 第 4 个：已知的边界与下一步

**现在掌握的事实**：

- `waitSinkReady` 等 `readyFile` 出现（`deadline 30s` + 10ms 轮询），**这是合理的异步等待**，不是空转
- 单独跑 9.5s 绿、3 份并发绿
- 只有全量跑（101 包，`internal/terminal` 自己就有多个 PTY 用例各起子进程）才红

**所以最可能的原因是「子进程启动在重负载下超 30s」**，但那**尚未验证**。

**下一步不该做的**：把 30s 抬到 120s —— 那既没定性也没修。

**下一步该做的**：全量跑时抓 `screen` 的输出（`t.Fatalf("原生字节汇没就绪；screen=%q")` 会把当时的屏幕内容打出来），看子进程是**根本没起来**还是**起来了但没写就绪文件**。两者修法完全不同。

## 诊断方法（可复用）

```bash
# 1. 先确认是不是负载型：单独多跑几次
go test ./<pkg>/ -run '<TestName>' -count=5

# 2. 抓失败耗时：恰好整秒 = 撞等待上限
go test ./... -count=1 | grep -E '^--- FAIL'

# 3. 造负载复现（比全量快）
for i in 1 2 3; do (go test ./<pkg>/ -run '<TestName>' -count=1 >/tmp/p$i.log 2>&1) & done; wait

# 4. 定性后用变异证明修的是同一个失败
#    例：把等待压到 1ms 制造同样条件 —— 修复版应仍绿，退回旧判据应红且消息与原来一致
```

**第 4 步是关键**：它证明「修的就是那个失败」，而不是「把测试改松了」。