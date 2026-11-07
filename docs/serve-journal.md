# SERVE-JOURNAL：H14 后实链缺口收口

> 2026-11-05。基线是 `af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当时共享工作树（含 H14 golden、W20）。本页只记录本轮实际跑过的断言，不用历史 ✅ 替代本轮结果。

## 1. 改动范围与不变量

产品代码只改：

- `internal/transport/wssession.go`：替换旧的“先 replay、后 Subscribe”交接；补控制帧 envelope time。
- `cmd/dshd/serve_approval.go`：pending approval 的无 seq 状态重放补 `time>0`，原始 `data/requestId` 不变。

新增 `internal/transport/serve_journal_handoff_test.go`、`cmd/dshd/serve_journal_regression_test.go` 与本页。

共享树已经包含 `followBaseline`（起子进程前取 EOF、`lastSeq=-1`）和 debug `modelRunner → emitFrame` 的修复，所以 `serve_events.go`、`serve_turn.go`、`serve_tools.go` 本轮不重复改写；新增测试只是加强锁定它们。

未改 v4、`journalEpoch/lastSeq`、turn/done|error、approval/cancel/pending cancel、契约、快照、golden、UI 或其他包。三份冻结 golden 的 SHA-256 复核仍为：

- `contract-golden.json`：`f90c8a8cfb66d42d31c8dc446e30ef3d2b4730eb51e0d22065ec99d70459f1e8`
- `ws-golden.jsonl`：`254c474897aa446e605553254068cf4f9f26a0f73ed1952bd5fbed98326db4bc`
- `ws-golden-b.jsonl`：`59f1c11ca40501de2b200e486777202dfe5dedff28f8382790df61db73cd624e`

## 2. 交接算法（不是旧 +30 行重放 patch）

1. 首次 `Window` 判断 epoch/容量缺口；需要 rebuild 时仍只发无 seq 的 `session/reset`，以当前 `lastSeq` 为新基线。
2. 打开每连接的 replay 闸，随后先 `Subscribe`。重放期间的实时 `Push` 只进缓冲，不写 socket。
3. 订阅后第二次取 `Window`：第一次快照到 Subscribe 之间的追加进入重放集；第二次快照之后的追加已由 Hub 缓冲。若交接期间又换代或滑窗，连接明确报错并重连，不拿错位数据充数。
4. 先写重放，再把 journal 补到最新。缓冲只记录“有 Publish 来过”，实际帧始终从 journal 取；即使实时 Publish 先发 seq=N+1、再发 seq=N，也会先补 N，再发 N+1。`seq<=deliveredSeq` 不重复交付。最后一个“无新 Publish + journal 已补齐 + 关闭 replay 闸”在同一状态锁内完成，之后实时 Publish 仍走同一 journal catch-up。
5. pending approval 若与本次 journal 重放集的 `requestId` 相同，只交付 journal 那一份；不在重放区间的 pending 仍按原契约以 `seq=0` 状态帧恢复。
6. socket 的唯一写字节入口给缺时控制帧补墙钟毫秒；已有 journal 时间原样保留。`seq/data` 不改。

## 3. 先红：旧共享实现的真实失败

命令：

```bash
go test ./internal/transport -run 'TestServeConn(ReplaySubscribeHandoffLosesNothing|ZeroWatermarkReplayHasSingleCopy|ReplayDeduplicatesPendingAlreadyInJournal|PendingReplayWithoutJournalCopyHasTime|ResetHasPositiveEnvelopeTime)' -count=1 -v
go test ./cmd/dshd -run 'TestServe(FollowAcrossTurnsDoesNotRepeatSameTool|DebugRunnerAllFramesShareJournalAndTime|PendingApprovalReplayHasTimeAndOriginalIdentity)' -count=1 -v
```

修复前结果：`internal/transport` **exit 1**，`cmd/dshd` **exit 1**。实际失败为：

- `TestServeConnReplaySubscribeHandoffLosesNothing`：第一条重放期间 `Append+Publish` 的第二条帧收不到，2 秒超时。
- `TestServeConnReplayDeduplicatesPendingAlreadyInJournal`：同一 `perm-1` 同时出现 `{seq:0,time:0}` pending 与 `{seq:1,time:1234}` journal 两份。
- `TestServeConnPendingReplayWithoutJournalCopyHasTime`、`TestServeConnResetHasPositiveEnvelopeTime`：envelope `time=0`。
- `TestServePendingApprovalReplayHasTimeAndOriginalIdentity`：broker 的 pending 帧 `time=0`。

交接初版另做一次自纠红测：`TestServeConnLiveOutOfOrderPublishFillsFromJournal` 在 replay 完成后先 Publish seq=3、再 Publish seq=2，初版只记最高水位，实际先出 3 且过滤 2，**exit 1**。因此最终实现不把 Publish 到达顺序当事实，实时也改为 journal catch-up；不是只修订阅那一瞬。

同一轮红测中，`TestServeFollowAcrossTurnsDoesNotRepeatSameTool`、`TestServeDebugRunnerAllFramesShareJournalAndTime`、`TestServeConnZeroWatermarkReplayHasSingleCopy` 已在共享修复上通过；它们是回归锁定，不冒充本轮“红后修复”。

## 4. 后绿：命令与真实断言

| 验证 | 本轮结果 | 实际断言 |
|---|---|---|
| 上述两条定向测试 | 两包均 **exit 0** | 交接帧 `1,2` 各一次；零水位 `1,2,3` 各一次；pending 不双份；reset/pending `time>0`；跨轮工具与 debug 全帧同 journal |
| `go test ./internal/transport ./cmd/dshd -count=1` | **exit 0**（0.807s / 28.023s） | 两个完整相关包无回归 |
| 交接/实时乱序/零水位/pending 四项 `-count=100` | **exit 0**（15.215s） | 重复 Publish 不重复交付，乱序 Publish 先补洞，100 轮无漏帧 |
| `go test ./... -count=1` | **exit 0** | 全仓 59 包枚举内测试通过；含 v4、W20、H14 golden |
| `go vet ./...` | **exit 0** | 全仓 vet |
| `go build ./...` | **exit 0** | 全仓构建 |
| MinGW `CGO_ENABLED=1 go test -race ./internal/transport -run TestServeConn -count=1` | **exit 0** | WS 交接定向 race 通过，0 DATA RACE |
| MinGW `CGO_ENABLED=1 go test -race ./cmd/dshd -run <本轮三项>` | **exit 0** | 跨轮/debug/pending 定向 race 通过，0 DATA RACE |

race 使用实际存在的 `C:/toolchains/mingw64/bin/gcc.exe`，不是工具链缺失的 SKIP。

### 跨轮与 debug 的精确判据

- 同一 runner 连续两轮都写 `read{"path":"same.txt"}`：Transport 只出现 `tool/call,tool/result,tool/call,tool/result` 四条，seq 恰好 `1..4`、全 `time>0`，`pub` 与 journal history 逐帧相等。
- debug runner 连续两轮：只出现 `assistant/message,turn/done,assistant/message,turn/done`，seq 恰好 `1..4`、全 `time>0`，history 与推送逐帧相等。
- replay 结束后并发 Publish 乱序：journal 已有 `2,3`，先 Publish 3，客户端仍依次收到 `2,3`；随后迟到 Publish 2 不再出帧。

### W20 / pending cancel 定向复验

```bash
go test ./cmd/dshd -run 'TestServeDelete|TestDefaultLoopRunnerSessionPermissionOverrideLifecycle|TestCancel|TestBrokerCancel' -count=1 -v
```

**exit 0（0.900s）**。实际通过 12 项：pending cancel 拒绝/会话隔离/旧 timeout 停止、cancel 等真收工、cancel 超时保留 live、delete 等活动工具/超时保留目录/完成与缺失会话、permission A/B/ask/deny/auto 下一轮 argv、最终工具结果保留。

## 5. 真实 apiclient：双会话重放与断号补洞

在未改 UI 产品源码的工作树 `D:/DeepSeek Harness/default-workspace/dsh-ui`（HEAD `3925e84f1524dd101e020d74b1d5bb422d25cf4d` + 共享 H14）运行：

```bash
go test ./cmd/mockd -run 'TestContractGoldenThroughRealAPIClient|TestContractSnapshotRemainsCanonical' -count=1 -v
go test ./internal/apiclient -count=1
```

分别 **exit 0**（0.156s / 5.519s）。实际通过：

- 两个独立 session/epoch 各自从 seq=1 重放，不串流；A 会话 live 故意缺 `4..5`，客户端恰好生成一个 gap。
- `apiclient.FillGap` 经真实 HTTP `session.history` 补回原始 `4..5`，seq/type/time/data 与共享 golden 一致；text/reasoning/tool call/result 与结束帧由真实 `Frame.Event/Chunks` 消费。
- B 会话 pending approval 保留原负载、`seq=0`；旧 epoch 订阅只收 `session/reset`，reset `time>0`。
- `stale_epoch` 四字段经 `APIError` 冒泡，随后真实 Load 重建基线。

这层使用的是**真实 UI apiclient + 共享 golden/mockd**，证明客户端重放、隔离、补洞与帧消费；宿主新交接由上面的内存 Conn 并发测试和 race 证明。两层没有合并冒充“同一真实 dshd + apiclient 联合长跑”。

## 6. 未验边界

- 未启动 GUI，未做真实模型生成、真实工具允许/拒绝执行或完整工作日。
- 未重跑 120 秒性能探针，因此不写“旧 137,246 次重连风暴已在真机性能层消失”的结论；本轮证明的是新交接路径不重复/不丢及定向 race，不是性能 A/B。
- 未做同一真实 dshd 进程重启后的 cookie 自愈与同版本 UI 联合复验。
- 未跑全仓 `-race ./...`；本轮只有上述两组定向 race。共享全仓 race 历史结果不代替本轮。
- MSGID 缺口不变：真实 data 仍无 `v4Seq/messageId`，不宣称 load↔WS 精确内容身份去重已经完成。
