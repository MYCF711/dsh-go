# TUI 断线暂停会话运行计时

本页记录 TUI-DISCONNECT-CLOCK 的 scoped 修复。基线是 `af4a49a` 加当前共享工作树（含 TUI-SEND-ACK）；实现只落在 `cmd/dshtui/**`，不改 Transport 或其他包。

## 1. 连接状态与代际

当前共享源码没有任务描述里的 `handleKeyFrame` / `uiElapsed` / `lastRunningActivity`；对应实际路径是 `client.Subscribe` 的自动重连循环，以及 `statusRow` 的 `now-start`。本轮在实际路径上修复：

- `SubscribeWithEvents` 与原 `Subscribe` 共用同一个 `subscribe` 帧循环。`--send` 仍使用原入口，不需要消费连接事件；全屏 TUI 与 `--script` 消费新增的 `connectionEvent`。
- 首次 dial 成功发 `wsOpen`；WS close、`ReadText` 错误发 `wsDown`；每次重连 dial 失败继续发同代 `wsDown`；重连成功后连接代际递增并发新 `wsOpen`。
- 每个事件同时携带 `sid`、单调 `subscription`、单调 `generation`。app 只接受当前会话、当前订阅、当前连接代际的 down；旧连接迟到的 close 与旧订阅事件都不能污染新连接。
- 新连接 open 清除旧 down。重复 open 不重置活跃分段，重复 down 不重复结算。
- `/new` 与 A/B 切换会关闭旧订阅并换事件 channel；主循环每轮重新读取当前 channel，避免缓存旧订阅。主动关闭引起的 read error 不伪报网络 down。

## 2. 累计活跃时间

真实 HTTP `accepted` 仍只负责建立原来的 `busy`、`activeTurnID` 与轮次起点。运行显示改用两个字段：

- `runningAccum`：此前已经结算的活跃段总和；
- `runningSince`：当前连接可用的活跃段起点，down 时为零。

down 到来时先把 `now-runningSince` 加入 `runningAccum`，随后冻结；down 期间的墙钟推进不参与显示。新连接 open 只把 `runningSince` 设为恢复时刻，从累计值继续，既不计入断线时长，也不清零。新一轮 accepted 才重置本轮累计值；会话切换清空旧会话作用域。

状态行在断线时显示 `运行中 <累计值> · 断线暂停`。连接事件不修改 `busy`、审批 pending、`activeTurnID`，也不伪造 turn/done。发送确认、 durable 正文、Markdown、首屏、工具卡及折叠逻辑未替换。

## 3. 先红后绿

新增 `cmd/dshtui/disconnect_clock_test.go`，先对旧实现运行，编译期即确认连接事件契约不存在；加入只固定契约、不实现行为的脚手架后，得到以下实际行为红：

```text
TestDisconnectClockFreezesAndResumesAcrossConnectionGenerations
  FAIL：close 后显示 运行中 1m42s，而不是冻结的 12s
TestConnectionEventsAreScopedToSessionAndSubscription
  FAIL：同 sid 旧订阅可以影响当前 down 状态
TestSubscribeReportsOpenCloseReadErrorAndReconnectFailure
  FAIL：SubscribeWithEvents 尚未实现
```

实现后定向测试与完整包测试均通过。覆盖：

- open、close、read error、重连失败、重连成功的事件顺序和连接代际；
- elapsed 在 down 期间冻结、open 后恢复，连续两次断线不清零；
- 第一代连接迟到的 close 不暂停第二代；
- A/B 与同 sid 旧订阅事件隔离；
- busy、审批和真实 turn 不被连接事件伪改；
- 120×36 / 80×24 的 down/open 快照行数与显示宽度不溢出。

## 4. 本轮实际验证

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./cmd/dshtui -run 'TestDisconnectClock|TestConnectionEvents|TestSubscribeReports' -count=1
# ok dshgo/cmd/dshtui

go test ./cmd/dshtui -count=1
# ok dshgo/cmd/dshtui

go vet ./cmd/dshtui   # exit 0
go test ./... -count=1
# 全仓 exit 0；cmd/dshtui、cmd/dshd、internal/...、modules/... 均通过

go vet ./...          # 全仓 exit 0
go build ./...        # 全仓 exit 0

go test ./cmd/dshtui -run 'TestDisconnectClock|TestConnectionEvents|TestSubscribeReports' -count=20
# ok dshgo/cmd/dshtui（20 次重复）

go run ./cmd/dshtui --dump-screen --size 120x36   # 只读快照 36 行
go run ./cmd/dshtui --dump-screen --size 80x24    # 只读快照 24 行
```

未 commit/push，未启动 GUI，未做真实宿主交互终验；验证层级是可控 WS 生命周期测试、包内行为/快照测试与全仓 Go 门禁。Transport 契约和其他共享改动不由本页宣称完成。
