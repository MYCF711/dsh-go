# HOST-PERF-120S（W37）：五会话真实 120 秒宿主性能证据

> 最终范围结论：**PASS（本探针定义的合成流负载与阈值）**。最终 run 是
> `tmp/perf-120s-final/delivery/`，不是前面的失败/候选 run。未 commit/push，未启动 GUI，
> 未修改产品实现、canonical 契约或其他任务文件。

## 0. 最终结果与版本

| 项 | 本次真值 |
|---|---|
| `dshd_rev` | `af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当时共享工作树 + W37 测试探针 |
| 测试宿主 SHA-256 | `178f348f586c8d5e3f0539956ceed7145317e9ecba17ca1bba247000c21b00ac` |
| 墙钟（UTC） | `2026-11-05T01:25:34.386295+00:00` → `2026-11-05T01:27:34.408282+00:00` |
| 实际墙钟 | **120.01971790000971 秒**（打印值 `120.019718`），不是缩短或估算 |
| 汇总 | **`PERF_120S_SUMMARY OK=136 FAIL=0 SESSIONS=5 WALL_SECONDS=120.019718 RTT_N=1150 DISPATCH_N=4430 RESOURCE_N=463`** |
| 独立磁盘复核 | **`JOURNAL_CHECK OK=21 FAIL=0`** |
| 独立进程复核 | **`PROCESS_CHECK OK=5 FAIL=0 SPAWN=441 UNIQUE_PID=413 REUSE=28`** |
| 全仓门禁 | **`GATE_SUMMARY TEST=0 VET=0 BUILD=0`** |

机器汇总：[summary.json](../tmp/perf-120s-final/delivery/summary.json)；
原始控制台：[delivery-console.log](../tmp/perf-120s-final/delivery-console.log)；
独立复核控制台：[delivery-journal-process-check.log](../tmp/perf-120s-final/delivery-journal-process-check.log)。

### 运行的是哪一层

`go test -c` 把 `cmd/dshd/perf_120s_probe_test.go` 加进当前 `cmd/dshd` 测试二进制。
`DSHD_PERF_120S=1 ... serve` 直接调用产品自己的 `cmdServe`；每轮子进程重 exec 同一测试
二进制，合成追加 v4 事件。以下都是真实执行链，不是 mockd、手写 journal 或微基准：

- 临时 home、`127.0.0.1:0`、ready 一次性 token → HttpOnly cookie；
- 裸参数 HTTP、五条独立 `/ws`、双向 no-context-takeover `permessage-deflate`；
- `loopRunner` 子进程、v4 append/follow/drain、Transport journal 发号及同步持久化；
- `session.history` 原始帧、stale epoch/reset、pending approval broker/stdin 应答、断线重放。

**合成替代的是模型生成和工具副作用。** 没有调用 magpie/真实模型，没有执行真实 write，
没有 GUI；资源值是这份 test-instrumented `cmdServe` 进程树的绝对值，不能外推成无插桩
`go build` 二进制、真实模型工作日或可见 GUI 的性能结论。token/cookie 只经内存传递，
证据里只有 `<redacted>`。

## 1. 负载与计时

- A/B/C/D/E 五会话并发，每个会话独立 session id、journal epoch、WS 和从 1 开始的 seq。
- 每轮 9 个 v4 事件 + `turn/done`；相邻 append 间隔 90ms，子进程末尾再活 260ms，确保
  live follow 与最终 drain 都参与。每轮包含 text、两个 reasoning 块、tool call/result。
- 五会话各做一次 approval/gap/reconnect 演练。演练逐会话串行，其他 worker 暂停新轮次，
  但五条 WS、采样和宿主持续运行；演练后恢复五会话并发。这样不会把共享 broker 的 stdin
  应答路由与本次性能判据混在一起。
- 在 15/35/55/75/95 秒附近分别对 A–E 断线：pending approval 保留 → 错 epoch history/订阅
  → `session/reset` → 正确重连收到 `seq=0` pending approval → 应答后再次断线 →
  history 补真实缺口 → 重连重放迟到帧。第二次断线使用读线程 join 后的新水位。
- 启动后有快速失败门：五会话都必须先收到正序帧和 pong，否则 15 秒内真红，不空跑 120 秒。

实际命令：

```bash
PYTHONDONTWRITEBYTECODE=1 python scripts/perf-120s.py \
  --evidence-dir tmp/perf-120s-final/delivery
PYTHONDONTWRITEBYTECODE=1 python scripts/perf-120s-journal-check.py \
  tmp/perf-120s-final/delivery
```

探针内部先执行并记录：

```bash
go test -c -o tmp/perf-120s-final/delivery/bin/dshd-perf.test.exe ./cmd/dshd
python scripts/bin-check.py tmp/perf-120s-final/delivery/bin/dshd-perf.test.exe
# 输出：OK 1 个二进制都不比源码旧；合计 OK=1 FAIL=0
```

完整 argv、配置、阈值和环境见 [commands-config.json](../tmp/perf-120s-final/delivery/commands-config.json)、
[environment.json](../tmp/perf-120s-final/delivery/environment.json)、
[revisions.json](../tmp/perf-120s-final/delivery/revisions.json) 和
[source-manifest.json](../tmp/perf-120s-final/delivery/source-manifest.json)。

## 2. 帧、RTT 与分发延迟

分位数算法唯一口径：**nearest-rank**。样本升序，`rank=ceil(p/100*N)`（1-based），不插值；
N=0 时分位数为 `null` 且判 FAIL，不存在 0 样本绿灯。

- RTT：每条 WS 应用层 `ping` 发出到对应 `pong` 返回的 `perf_counter` 差值。
- 分发：同机 `receivedMs - ServerFrame.time`；包含 journal append 后的调度、WS 编码/压缩、
  socket 与客户端读取。重放帧保留原 `time`，所以迟到重放也计入，不重新编时间。
- 分发表 N=4430 = 4415 条正序帧 + 15 条带时间的控制/状态帧；RTT 另计，不混入分发。

### 聚合绝对值（ms）

| 指标 | N | p50 | p95 | p99 | max |
|---|---:|---:|---:|---:|---:|
| WS ping/pong RTT | 1150 | 8.131200 | 87.664400 | 140.673000 | 307.042000 |
| journal→客户端分发 | 4430 | 22.000000 | 120.000000 | 212.000000 | 795.000000 |

RTT max 是如实保留的离群值；阈值判据是预登记的 p95，不宣称每一帧都小于 250ms。

### 每会话绝对值

| 会话 | 正序帧 / turn | RTT N / p50 / p95 / p99 / max（ms） | 分发 N / p50 / p95 / p99 / max（ms） |
|---|---|---|---|
| A | 881 / 88 | 230 / 7.2468 / 84.2194 / 132.6992 / 160.6782 | 884 / 22 / 121 / 219 / 656 |
| B | 881 / 88 | 231 / 10.0178 / 92.8846 / 140.6730 / 187.1150 | 884 / 23 / 127 / 206 / 602 |
| C | 881 / 88 | 230 / 8.6178 / 93.3797 / 132.8660 / 191.8710 | 884 / 21 / 125 / 223 / 622 |
| D | 891 / 89 | 230 / 7.2161 / 79.4853 / 142.4993 / 306.4992 | 894 / 23 / 113 / 206 / 606 |
| E | 881 / 88 | 229 / 8.1097 / 87.6644 / 146.9716 / 307.0420 | 884 / 21 / 114 / 227 / 795 |

每会话首帧 seq 都是 1；A–E epoch 分别为 `940289563828990220` 到
`940289563828990224`。`seqErrors/crossTalk/unexpectedClose` 全部为空。

原始数据：[rtt.jsonl](../tmp/perf-120s-final/delivery/rtt.jsonl)、
[dispatch.jsonl](../tmp/perf-120s-final/delivery/dispatch.jsonl)、
[frames.jsonl](../tmp/perf-120s-final/delivery/frames.jsonl)。

## 3. 整进程树资源

采样目标间隔 250ms；最终稳态样本 463 个，最大相邻间隔 **0.8350609s**（判据 ≤1s）。
CPU 由相邻样本的 `user+system` 秒数差 / 墙钟差计算；`100%` 表示一个逻辑核，另给 16 核
归一值。Working Set 取 Windows `wset`，Private Bytes 取 `private`，树值是各存活进程求和，
不是系统唯一页去重值。子进程退出后不伪造后续指标。

| 指标 | 启动 | 稳态/峰值 | 结束 | 阈值与判定 |
|---|---:|---:|---:|---|
| 进程数 | 2 | 最大 7 | 2 | 根 + conhost + 五个 turn 子进程 |
| CPU p50 / p95 / p99 / max（单核%） | 基线 | 13.8937 / 112.9956 / 175.2242 / 251.2652 | 6.6726 | p95 ≤500，PASS |
| CPU p95 / max（16 核%） | — | 7.0622 / 15.7041 | — | 派生归一值，不是第二份样本 |
| Working Set 峰值 | 25,886,720 B | **229,916,672 B（219.265625 MiB）** | 70,275,072 B | ≤512 MiB，PASS |
| Private Bytes 峰值 | 58,732,544 B | **442,404,864 B（421.910156 MiB）** | 101,224,448 B | ≤512 MiB，PASS |
| handles 峰值 | 276 | **1172** | 430 | ≤4096，PASS |
| 线程峰值 | 16 | **98** | 33 | ≤512，PASS |
| GDI / USER 对象峰值 | 0 / 1 | **0 / 1** | 0 / 1 | `user32.GetGuiResources` 实采 |
| goroutines | 11 | — | 6 | `/debug/stats` 实值 |
| FD | — | `null` | — | Windows psutil 无 `num_fds`，不冒充 0 |

稳态首末增量：Private Bytes **+40,734,720 B（38.847656 MiB）**（≤128 MiB）、
handles **+155**（≤256）、线程 **+17**（≤64）。这些只说明本次 120 秒和阈值内未观察到
泄漏/风暴，不证明更长工作日无泄漏。每会话 881–891 帧，低于预登记的 1500 帧风暴上限。

原始每进程样本：[resources.jsonl](../tmp/perf-120s-final/delivery/resources.jsonl)；
启动/结束快照：[process-tree-start.json](../tmp/perf-120s-final/delivery/process-tree-start.json)、
[process-tree-end.json](../tmp/perf-120s-final/delivery/process-tree-end.json)。

## 4. journal、原始 history、迟到帧与断号恢复

A/B/C/D/E 的 history 分别为 881/881/881/891/881 帧，合计 **4415**；441 个 turn。
独立脚本把每条磁盘 `transport-journal.v1.jsonl` 的 base64 `data` 解码，与 HTTP history 和
WS 同 seq 帧比较，要求 `seq/type/time/data` 全等：

- 五个 journal 文件恰好对应五个 session；seq 都是连续 `1..lastSeq`。
- history 是 `frames[]`，不是投影 `messages[]`；4415 条 data 逐字节匹配。
- WS 覆盖全部磁盘正序帧；缺帧、数据不符、重复正序 seq 都是 0。
- 五个磁盘 journal 合计 16,339,364 bytes。

五次迟到恢复：

| 会话 | 第二次断线水位 | 补回/重放 seq | 结果 |
|---|---:|---|---|
| A | 125 | 126–131（6 帧） | text/reasoning/tool/end/done 全恢复 |
| B | 264 | 265–271（7 帧） | 同上 |
| C | 414 | 415–421（7 帧） | 同上 |
| D | 574 | 575–581（7 帧） | 同上 |
| E | 716 | 717–721（5 帧） | assistant/reasoning、tool/result、end/done 全恢复 |

每会话还各验证：错误 epoch history 返回 409 `stale_epoch/retryable=true`，错误 epoch
订阅返回无 seq `session/reset{真实journalEpoch,lastSeq}`，pending approval 以 `seq=0`
恢复且 requestId 不变。A→E 的独立连接切换与唯一 marker 对账均未发现跨会话暗号；
未用同一条 WS 混订多个会话，也不宣称这是 GUI 切换测试。

证据：[journal-validation.json](../tmp/perf-120s-final/delivery/journal-validation.json)、
[drills.jsonl](../tmp/perf-120s-final/delivery/drills.jsonl)、
`history-A.json` … `history-E.json`、`drill-A-offline-history.json` …
`drill-E-offline-history.json`（均在最终 run 目录）。

## 5. 所有 PID 归属与清理

最终 run 有 **441 条 spawn = 441 条 turns = 441 个子进程世代**。Windows 复用了 28 个
PID 值（413 个唯一子 PID），因此不能只按 PID 合并：`pid-generations.json` 以
`PID+createTime` 记录 **443 个世代**（441 子进程 + 根 + conhost）。每个 spawn PID 都在
资源样本中出现，argv 的临时 home、session id 与 marker 全部同会话匹配。

- 根 PID **32764**，映像严格等于最终 run 的 `bin/dshd-perf.test.exe`。
- 收尾时存活子 PID **33260** 是该根树下的 `conhost.exe`；不把它误判为外部 DSH。
- 只对该已核对根执行 `taskkill /PID 32764 /T /F`，退出 0；`survivingOwnedPids=[]`。
- 未按镜像名批杀，未触碰 DSH/magpie/其他会话，未删除 home 或其他任务路径。

证据：[process-validation.json](../tmp/perf-120s-final/delivery/process-validation.json)、
[pid-generations.json](../tmp/perf-120s-final/delivery/pid-generations.json)、
[spawn-attribution.jsonl](../tmp/perf-120s-final/delivery/spawn-attribution.jsonl)、
[cleanup.json](../tmp/perf-120s-final/delivery/cleanup.json)。

## 6. 阈值、失败证据与反证

阈值在运行前写入配置，未因结果改宽：RTT p95 ≤250ms、分发 p95 ≤1500ms/p99 ≤3000ms、
整树 CPU p95 ≤500 单核%、Working Set/Private ≤512MiB、handles ≤4096、线程 ≤512、
稳态 Private 增长 ≤128MiB、handles 增长 ≤256、线程增长 ≤64、每会话帧数 ≤1500、
资源样本 ≥400、采样间隔 ≤1s、每会话 RTT 样本 ≥60、墙钟 ≥120s。最终全部通过。

探针自身失败也保留，不用后来的绿灯覆盖：

| run | 原样结果 / 保留原因 |
|---|---|
| `run/` | `OK=28 FAIL=41 WALL_SECONDS=0`：`kernel32.GetGuiResources` 初始化错；根随后由 parent-watch 退出，复核 `NoSuchProcess` |
| `final/` | `OK=47 FAIL=53 WALL_SECONDS=120.023364`：WS 二字节头解析和 setPermission 探针错，0 帧不报绿 |
| `verified/` | 主汇总曾 `136/0`；事后发现 JSONL 初始化竞争使 `turns` 少 2 行，因此不作最终证据 |
| `final-run/` | `OK=134 FAIL=2`：第二次断线沿用旧水位，A/C 重复 116、405/406；独立复核 `JOURNAL_CHECK OK=19 FAIL=2` |
| `delivery/` | 修复后最终 `136/0`；独立 journal/process 复核全绿 |

最终自测还真实执行了最近邻分位数、WS extended-length/deflate、800 条并发 JSONL 写入，
并注入 0 RTT 样本、119.999 秒墙钟、超阈 p95 和 A/B 串流，逐项出现指定 FAIL：

```text
SELFTEST_OK ws-byte-header-extended-length-and-deflate {'plainBytes': 384, 'compressedWireBytes': 91, 'jsonBytes': 2064}
SELFTEST_OK jsonl-concurrent-init-no-lost-rows {'rows': 800, 'unique': 800}
SELFTEST_RAW red-zero-rtt OK=55 FAIL=2 expected_red=metric/rtt-nonzero-samples
SELFTEST_RAW red-short-wall OK=56 FAIL=1 expected_red=metric/wall-at-least-120-seconds
SELFTEST_RAW red-p95 OK=56 FAIL=1 expected_red=metric/dispatch-p95-threshold
SELFTEST_RAW red-cross-talk OK=56 FAIL=1 expected_red=metric/A/no-cross-talk
SELFTEST_SUMMARY OK=10 FAIL=0
```

完整自测输出：[selftest-v3-console.log](../tmp/perf-120s-final/selftest-v3-console.log)。

## 7. 全仓门禁原样结果

最终 run 和独立复核完成后，在共享树只跑定向全仓命令（未运行会移动目录/全局 taskkill
的脚本）：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./... -count=1
go vet ./...
go build ./...
```

原样尾部：

```text
ok  	dshgo/cmd/dshd	58.661s
GATE_TEST_EXIT=0
GATE_VET_EXIT=0
GATE_BUILD_EXIT=0
GATE_SUMMARY TEST=0 VET=0 BUILD=0
```

完整日志：[final-gate-test.log](../tmp/perf-120s-final/final-gate-test.log)、
[final-gate-vet.log](../tmp/perf-120s-final/final-gate-vet.log)、
[final-gate-build.log](../tmp/perf-120s-final/final-gate-build.log)。

## 8. 历史对照与未验边界

历史文档提到 `137,246` 次重连/120s，但那是**不同 rev、不同脏 `wssession` 工作树**的事故，
原文也明确禁止拿它污染性能结论；`docs/serve-journal.md` 同样写明当时未重跑 120 秒探针。
因此本文**没有旧基线、不计算提升百分比**，只报告本次绝对值与预登记阈值。

未验证：真实模型/工具副作用、无插桩生产二进制、可见 GUI、跨进程重启、完整工作日、
真实 UI apiclient 联合长跑及 `-race ./...`。本次 PASS 不外推这些层级。
