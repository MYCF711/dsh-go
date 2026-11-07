# TUI 发送确认、草稿与 durable 正文

本页记录 TUI-SEND-ACK 的 scoped 修复。基线是 `af4a49a` 加当前共享工作树（含 TUI-MESSAGE）；只改 `cmd/dshtui/**` 与本页，不改 Transport 或其他包。

## 1. 状态与顺序

`submit` 不再直接画用户消息，也不再同步等待 HTTP：

1. 普通草稿进入 `pendingPrompt` 后，原文仍留在输入行，编辑、Enter、Esc 与搜索入口锁定；滚动、Ctrl+T/Ctrl+O 和退出仍可用。状态行显示 `发送中 · 草稿只读`。
2. HTTP goroutine 只把 `promptOutcome{id,sid,ack,err}` 写入 channel；界面状态仍只由主事件循环修改。`--script` 消费同一个 channel，不另造提交捷径。
3. 等 HTTP 时到达的 WS 帧按接收顺序暂存。宿主可能先写 durable `user/message` 再返回 HTTP，因此既不能提前显示，也不能丢弃这帧。
4. 只有响应里的 `accepted:true` 才清草稿、恢复输入并登记 `activeTurnID`，随后按原顺序释放暂存帧。正文唯一来源仍是 durable `user/message`；accepted 本身不产生用户气泡。
5. HTTP 错误或 `accepted:false` 保留原草稿并显示 `发送失败…草稿已保留，可重试`，不伪造用户行，也不删除既有 transcript。若等待期间已经收到 durable 帧，它们属于真实历史，仍会落定，而不是随本地失败回滚。
6. 每个本地提交都有单调 `id`，结果同时核对 `id+sid`。迟到的旧 accepted/error 不能清新一轮草稿。带显式 `turnId` 的旧 `turn/done`/`turn/error` 也不能结束当前轮。

斜杠命令和 `/resume` 选择不是宿主 prompt，仍按原路径立即执行；Markdown、首屏、审批、搜索、滚动和工具折叠实现未替换。

## 2. 去重边界

- durable 用户帧优先按 `data.id` / `data.message.id` 去重，其次使用显式 `v4Seq`，老宿主缺内容身份时用 Transport `seq`；切换会话会重置这个作用域。
- `pendingEcho` 只用于 accepted 前、且帧完全没有 `id/seq` 时的本次提交 fallback。它不是“正文已上屏”的标记，也不是全局正文过滤器。
- 同文发送两次是两条合法消息，必须有两个不同 durable 身份并显示两次；同一 durable 的确认前暂存、确认后重放和迟到重放只能显示一次。
- 没有任何 `id/seq` 的跨确认重放无法与同文新消息可靠区分，因此不宣称能按正文精确去重；真实 Transport 帧有序号，正常 durable 事件还有消息 id。

## 3. 先红后绿证据

新增 `cmd/dshtui/send_ack_test.go`。首轮在旧实现上运行四条兼容红测：

```text
TestSubmitAcceptedWaitsForDurableUser       FAIL：无 durable 帧已有 1 条用户正文
TestSubmitFailureKeepsDraftWithoutPhantom   FAIL：失败后草稿变成 ""
TestPromptPendingInputIsReadOnly             FAIL：pending 时 Enter 触发 submit
TestDurableUserDedupesByIdentityNotText      FAIL：期望 2 条，实际 3 条
```

修复后扩成 11 条定向测试，覆盖：

- 延迟 accepted 前无用户行、草稿只读，确认前 durable 不丢且不重复；
- accepted 后清草稿，durable 正文恰好一次，accepted=false 也算失败；
- HTTP 失败保留草稿、错误可见、既有历史不变，同一草稿可直接重试；`/resume` 非序号回退也保留草稿；
- durable 重放按身份去重，同文两次发送都保留；
- 旧 HTTP accepted 不污染第二次提交，旧 `turn/done` 不串新 turn；
- pending 长草稿在 120×36 / 80×24 不溢出。

本轮实际命令与结果：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./cmd/dshtui -count=1   # ok dshgo/cmd/dshtui
go vet ./cmd/dshtui             # exit 0
go test ./... -count=1          # 全仓 exit 0
go vet ./...                    # 全仓 exit 0
go build -o /tmp/tui-send-ack-build/ ./...   # 全仓 exit 0
```

用单独构建的 `dshtui.exe` 走只读 dump 入口（未启动 GUI）：

```text
screen 120x36:     OK rows=36 maxCols=120/120
transcript 120x36: OK rows=36 maxCols=120/120
screen 80x24:      OK rows=24 maxCols=80/80
transcript 80x24:  OK rows=24 maxCols=80/80
```

`-race` 未验证：首次运行为 `go: -race requires cgo`，随后检查到 `CGO_ENABLED=0` 且 PATH 中无 GCC。此项不写成通过，也不影响上面已实际完成的普通 test/vet/build 结论。

## 4. 验收边界

本轮未 commit/push，未启动 GUI，未做真实宿主交互终验；验证层级是包内行为测试、全仓 Go 门禁和只读快照。Transport 契约、宿主执行链及其他共享改动均不由本页宣称完成。
