# REAL-CHAIN-HEADLESS：真实 `dshd` + 真实 UI `internal/apiclient` 无窗整链

> 2026-11-05 实测结论：**OK=72 FAIL=0 SKIP=0**，`go_test_exit=0`，探针脚本退出码 **0**。
>
> 本次证据目录：[`tmp/real-chain-headless-wefimz9b/`](../tmp/real-chain-headless-wefimz9b/)。
> 机器汇总与全部断言：[`probe.log`](../tmp/real-chain-headless-wefimz9b/probe.log)。
> 探针：[`scripts/real-chain-headless.py`](../scripts/real-chain-headless.py)。

这条链不使用 mockd、ACP、手写服务端帧、截图或可见 GUI。它启动一个本副本新构建的
`dshd serve`，再把 Go 测试文件通过 **`-overlay`** 映射到真实 UI 的
`internal/apiclient` 包中执行。所有 HTTP/WS 调用都经 UI 自己的
`Client / Mux / Event.Chunks / FillGap / ParseApprovalRequest / Approve` 实现；
overlay 源文件和证据只落在宿主 `tmp/real-chain-headless-*/**`，UI 工作树没有新增探针文件。

## 1. 基线、二进制与真实模型

| 项 | 本次真值 |
|---|---|
| `dshd_rev` | `af4a49a2817592d7affb99bb3a16c7e03214ba1c`（base `af4a49a` + 当前共享工作树） |
| `ui_rev` | `3925e84f1524dd101e020d74b1d5bb422d25cf4d`（base `3925e84` + 当前共享工作树） |
| 新二进制 | [`tmp/real-chain-headless-bin/dshd.exe`](../tmp/real-chain-headless-bin/dshd.exe) |
| SHA-256 | `9dc25f172aeea0f2e4a3d42365634cf8b5d86c9eb2f90483234ad73b5dc8bcec` |
| 模型出口 | 本机 magpie `http://127.0.0.1:3425/v1`，`/models` HTTP 200 |
| 实际模型 | `group/deepseek-v4.1-flash1017`，在真实目录中；本次 **0 SKIP** |
| 审批/推理/资源上限 | `--policy ask --reasoning max --max-steps 4` |

第 0 步先跑了 `python scripts/bin-check.py`。初始两个旧位置都不存在二进制，得到
`OK=0 FAIL=1`；这属于“尚未构建”，不是继续使用旧二进制的理由。随后从本副本源码构建，
并把**实际探针路径**传给门禁：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build -o tmp/real-chain-headless-bin/dshd.exe ./cmd/dshd
python scripts/bin-check.py tmp/real-chain-headless-bin/dshd.exe
#   OK   1 个二进制都不比源码旧
# 合计 OK=1 FAIL=0
```

没有使用 `tmp/DSH GO.exe` 或 `tmp/serve-dir/DSH GO.exe`。探针自身每次启动也会再跑一次
`bin-check <实际二进制>`，陈旧/分叉会立即非零中止。

## 2. 启动形状与复跑命令

复跑：

```bash
python scripts/real-chain-headless.py
```

本次宿主 argv 原样记录在 [`command.json`](../tmp/real-chain-headless-wefimz9b/command.json)：

```text
dshd.exe serve --home <本次证据目录>\home \
  --addr 127.0.0.1:0 --policy ask --reasoning max --max-steps 4 \
  --model group/deepseek-v4.1-flash1017 --parent-pid <探针 Python PID>
```

本版 `serve` 没有 `--port` flag；`--addr 127.0.0.1:0` 是其实际的 **port 0** 入口，
由 OS 分配端口。本次 ready URL 为 `http://127.0.0.1:60054`。ready token 只从 stdout
读入并经子进程环境传给真实 UI `Connect`，证据中一律写 `<redacted>`；cookie 也不落证据。
见 [`ready-redacted.json`](../tmp/real-chain-headless-wefimz9b/ready-redacted.json)。

Python 以 `CREATE_NO_WINDOW` 启动宿主及 Go 测试，并传 `--parent-pid` 启用看门狗。
没有创建窗口、移动窗口、截屏或驱动 GUI。模型预检失败或目录缺少指定模型时会单独输出
`SKIP` 并非零退出；不会用其他实现顶替整链。编译错误、0 条断言、缺少 `RCH_SUMMARY`、
超时或 Go 非零退出都变成 `FAIL`。

## 3. 72 条断言覆盖了什么

下表是行为分组，不把一条断言重复算成多项能力；完整 72 行以 `probe.log` 为准。

| 链路 | 真实响应/副作用断言 | 本次结果 |
|---|---|---|
| connect / token | UI `Connect` 得到 cookie；同一 ready token 第二次 `/connect` 返回 401 `unauthorized` | PASS |
| create / list / load | 新建 main/allow/deny/cancel/A/B 共 6 会话；list 含新 id，最终 `sessions=6`；初始与末次 load 均带真实 `journalEpoch` 和 `permission=ask` | PASS |
| prompt / text | 主会话 `session.prompt` 返回 `turn-1`；UI `Event.Chunks()` 收到 `RC_HEADLESS_TEXT_OK`；load 同时含 user/assistant | PASS |
| 终局帧 | 同一 history 同时保留 v4 `turn/end` 和 Transport `turn/done`，探针不再把两种帧混为一谈 | PASS |
| reasoning | allow 工具轮经 UI chunks 收到 **430 bytes** `BlockReasoning`；原始 history 的 assistant content 含 `type=reasoning` | PASS |
| history 原始帧 | 首轮 6 帧，seq 恰为 1..6，type/time/data 非空；保留 assistant、turn/end、turn/done；原始 JSON 落证据 | PASS |
| stale epoch | `History(epoch+1)` 由 UI 解成 `*APIError{Code:"stale_epoch", Retryable:true}` | PASS |
| 真 WS 断线 | 对 UI `Mux.Dial` 捕获的真实连接执行 `CloseNow()`，事件 channel 在 5 秒上限内关闭 | PASS |
| seq 补缺 | 断线水位 6；断线期间完成新一轮；真实 `FillGap` 精确补 **7..12，n=6**，恢复暗号正文及终局，不用整页 load 代替 | PASS |
| reset / 重连 | 错 epoch 订阅收到无 seq `session/reset{journalEpoch,lastSeq}`；正确 epoch/水位重连后新轮正文与 `turn/done` 到达，旧 seq 重放为 0 | PASS |
| 工具流 | allow 轮 UI chunks 有 3 个 tool 块，history 有 `tool/call` / `tool/result` | PASS |
| allow 真副作用 | `ParseApprovalRequest` 取真实 requestId，`Approve(allow_once)` 被宿主接受；目标文件实际存在且内容含 `RC_HEADLESS_ALLOW_WRITE_OK` | PASS |
| deny 真副作用 | `Approve(reject_once)` 被接受；目标文件 **不存在**；`tool/result` 原始 data 含 `isError:true`，证明“没执行”不是“模型没调用” | PASS |
| pending cancel | 挂起审批时 `session.cancel` 返回 `cancelled:true`；`approval/cancel` 点中同一 requestId；工具文件不存在；Transport 以 `turn/error` 收尾 | PASS |
| cancel 后第二轮 | 同会话第二个 prompt 被接受，收到 `RC_HEADLESS_SECOND_TURN_OK` 和 `turn/done`；home 中 `.lock=0` | PASS |
| A/B 不串 | 两个 prompt 并发，A/B 各收到 6 帧且各自 seq 从 1 开始；双方流与 load 都只含自己的暗号、不含对方暗号 | PASS |

原始帧证据：

- [`history-main-initial.json`](../tmp/real-chain-headless-wefimz9b/history-main-initial.json)：正文、双终局与首轮连续 seq。
- [`history-main-disconnected.json`](../tmp/real-chain-headless-wefimz9b/history-main-disconnected.json)：真实断线期间的后续帧。
- [`history-allow.json`](../tmp/real-chain-headless-wefimz9b/history-allow.json)：reasoning、审批、工具调用/结果和正文。
- [`history-deny.json`](../tmp/real-chain-headless-wefimz9b/history-deny.json)：拒绝后的真实工具错误。

这些文件含测试会话内容，但不含 launch token/cookie。

## 4. 临时 home、退出码与清理

[`cleanup.json`](../tmp/real-chain-headless-wefimz9b/cleanup.json) 记录了完整 home 形状（只记相对路径和大小，
不读取或转储 token）。本次共 **7 个文件**：

- `logs/dshd-2026-11-05.log`：5,746 bytes；
- 6 个各自独立的 `sessions/<工作区编码>/<session-id>/session.v4.jsonl.zstd`：
  main 19,479；allow 8,027；deny 8,202；cancel 14,216；A 6,651；B 6,652 bytes。

进程退出真值：

| 项 | 值 |
|---|---|
| 真实 UI Go 测试 | `go_test_exit=0`，`PASS`，`TestRealChainHeadless` 11.86s |
| 探针机器汇总 | `RCH_SUMMARY OK=72 FAIL=0 SKIP=0` |
| 探针脚本汇总/退出 | `OK=72 FAIL=0 SKIP=0`，exit 0 |
| dshd PID | 36356；`QueryFullProcessImageNameW` 与本次新二进制逐字核对，`owner_verified=true` |
| 清理 | 只对上述根 PID 树执行 `taskkill /PID 36356 /T /F`，`taskkill_exit=0` |
| dshd 最终 exit | 1；这是收尾强制终止的结果，不是测试失败 |

没有按镜像名批杀，没有触碰正在运行的 DSH/magpie，没有 commit/push。

## 5. 首跑红与判据修正（保留反造假记录）

第一次实跑为 **65 OK / 6 FAIL**，证据保留在
[`tmp/real-chain-headless-nkoh4k_a/probe.log`](../tmp/real-chain-headless-nkoh4k_a/probe.log)。
它证明探针确实执行并能抓红，不是只会打印全绿：

1. `rchEndAfter` 把 v4 `turn/end` 当成 Transport 终局，导致 main 与第二轮的 `turn/done`
   断言误红；已把等待终局与检查 v4 结局分开，**没有删除任一帧的断言**。
2. 极短主回答没有 reasoning；同一真模型的 allow 工具轮实际有 reasoning。
   将 reasoning 断言放到产生它的真实工具轮，仍要求 UI chunks 与 history 同时命中。
3. 当前 `LoadResult.ID` 为空，A/B 隔离不该拿它当判据；改为检查双方 load 的 user/assistant
   正文及双向暗号隔离。
4. `session.load.model` 当前为空，保留 `RCH_NOTE` 和下方未验项，没有为变绿伪造模型字段。

第二次实跑才是本文顶部的 72/0/0。

## 6. 未验项与边界

- **不验可见 GUI/像素/键盘**：这是有意的无窗链，不声称截图、Viva 渲染或 OS 输入已验。
- **未重启宿主**：任务要求同一新建 dshd；本次验证错 epoch 的 `stale_epoch` 与 `session/reset`，
  不声称跨进程重启后的新 epoch/cookie 自愈已验。
- **断线补洞为 6 帧单批次**：证明真实 `FillGap` 按 seq 恢复；没有制造 >200 帧分批或 4096 帧容量淘汰，
  也没有用手写服务端跳号来伪造 Mux `gap` 事件。
- **reasoning 不是每个回答都必须出现**：极短主回答没有思考块；实际工具轮的 430 bytes 已验证，
  不把“没产生 reasoning 的回合”判成产品缺陷。
- `session.load.model=""`：本次只观察，不属于这条链的 H4 验收；`permission=ask` 已验。
- 未做完整工作日、长跑 soak、`-race`、真实桌面消费或性能验收；这些不能由 12 秒无窗探针外推。
