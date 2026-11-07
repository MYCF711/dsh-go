# TOOL-ARGS-FAILFAST：完整工具参数的全响应执行闸门

## 结论

基线为 `af4a49a2817592d7affb99bb3a16c7e03214ba1c` 加共享导出。此次只改
`internal/agent/loop.go`、`internal/agent/loop_test.go` 与本文，不改 Transport、v4 或其他包。

`Loop.Run` 在收到一次完整的模型响应后，先校验该响应 **全部 completed tool calls** 的
`function.arguments` 是否都是 JSON object。任一参数 malformed 时立即返回带 step、callId
和 JSON 原因的错误；首个 `runTool2`、`Runner.Run`、`EventToolCall` 都尚未发生，因此不会
出现“排在前面的合法 call 已经产生副作用，后面的坏 call 才报错”。

## 上游出处

- 官方版本：DSH `v0.2.2-rc.1`，tag `dsh-v0.2.2-rc.1`，发布时间
  `2026-11-04T13:07:14Z`。
- 官方发布页：<https://github.com/deepseek-ai/deepseek-harness/releases/tag/dsh-v0.2.2-rc.1>
- 发布说明「其他变更 / Chores」原文：
  - 中文：`升级 pi-ai 至 1.3.0，并同步模型目录、端点与推理回放规则；完整工具参数格式错误会在执行任何工具前终止该次响应。`
  - English：`Upgrade pi-ai to 1.3.0 with updated model catalogs, endpoints, and reasoning replay rules; malformed completed tool arguments fail the response before any tool executes.`
- 本次复核使用共享审计已下载的 `release-notes.md`，SHA-256 为
  `e2682d8c712c7a25b3843d5cfecd82bd9549ea3801c18ac22f39e7cd248d81c8`，与 W17 登记前缀一致。

这里移植的是执行顺序契约，不是 pi-ai 包升级，也不改变现有模型路由。

## assistant 原文与错误如何记录

**允许并保留 assistant tool_calls 帧落盘。** 顺序为：完整响应先按原逻辑追加到内存历史并发出
`EventAssistantMsg`，然后执行全响应参数闸门。理由是 `assistant/message` 记录的是“模型确实
产生了这些 tool_calls”这一原始事实，不是“工具已经执行”的证据；append-only 日志不应把已经
收到的模型输出改写成从未发生。

参数校验失败后：

- 不发出 `tool/call`，不发出 `tool/result`，不追加任何 tool 消息；
- 不为合法 call 伪造成功结果，也不为坏 call 补一个看似执行过的错误结果，因此没有假成功历史或
  孤儿 `tool/result`；
- 不在 agent 包新造事件类型或直接写 v4，而是返回错误，例如
  `第 1 步响应终止: 工具调用 "bad_call" 的参数不是 JSON 对象: unexpected end of JSON input`；
- `Loop.Run` 在这条错误路径不发正常 `step/end` / `turn/end`。现有调用方继续负责错误结局：
  `cmd/dshd` 的 `turnCloser` 写 `turn/end{kind:"error"}` 及原始错误详情，serve 的现有出口发
  `turn/error{turnId,error}`。这与模型调用失败等已有错误路径一致，不改变事件规范。

## 校验边界

- `{}` 及包含任意字段的 JSON object 通过；JSON 语法错误、字符串、数组、`null`、空参数均不通过。
  `null` 虽可由 Go 解成 nil map，但它不是 JSON object，必须单独拒绝。
- 校验发生在完整响应汇总之后，不逐流式碎片判错。
- 未知工具、缺 required 字段及其他 schema/执行错误不属于本闸门；参数若是合法 JSON object，
  仍进入原有 Runner，由原有 `tool/result` 错误与后续模型纠正流程处理。
- `modules/tools/pipeline.go` 的单 call 防御保持不变；本包新增的是它之前、覆盖整个响应的顺序保证。
- 全合法多 call 响应仍按原顺序逐个执行并保留原有事件序列。

## 必红测试与修前证据

旧测试 `TestMalformedToolCallsKeepTheTurnAlive` 已按新上游契约拆成更严格的执行闸门、位置、
全合法及非 malformed 错误测试，没有删除断言或把错误继续执行改成宽松通过。

在只加入新测试、尚未修改 `loop.go` 时运行：

```text
$ go test ./internal/agent -run 'TestValidCallBeforeMalformedCallIsNotExecuted|TestMalformedToolCallPositionFailsWholeResponse' -count=1
--- FAIL: TestValidCallBeforeMalformedCallIsNotExecuted (0.00s)
    loop_test.go:398: 坏响应执行了工具：Runner.Run=2，合法工具 Exec=1；两者都必须为 0
--- FAIL: TestMalformedToolCallPositionFailsWholeResponse (0.00s)
    --- FAIL: TestMalformedToolCallPositionFailsWholeResponse/first (0.00s)
        loop_test.go:441: 坏 call 在位置 0 时仍有执行：Runner.Run=3，工具 Exec=2
    --- FAIL: TestMalformedToolCallPositionFailsWholeResponse/middle (0.00s)
        loop_test.go:441: 坏 call 在位置 1 时仍有执行：Runner.Run=3，工具 Exec=2
    --- FAIL: TestMalformedToolCallPositionFailsWholeResponse/last (0.00s)
        loop_test.go:441: 坏 call 在位置 2 时仍有执行：Runner.Run=3，工具 Exec=2
FAIL
FAIL    dshgo/internal/agent    0.105s
FAIL
[exit code: 1]
```

这同时记录了关键反证：合法 call 在坏 call 前面时，旧实现的合法工具实际执行了 **1 次**；
坏 call 位于首位、中间、末尾时，旧实现也都执行了同响应中的合法 call。

修复后由以下测试钉住：

- `TestValidCallBeforeMalformedCallIsNotExecuted`：Runner 与底层 Exec 严格为 0，错误含
  `bad_call` / `unexpected end of JSON input`，只请求模型一次，无工具事件或 tool 历史；
- `TestMalformedToolCallPositionFailsWholeResponse`：坏 call 在首位、中间、末尾都为 0；
- `TestToolArgumentsMustBeJSONObject`：语法错误、字符串、数组、`null`、空参数均 fail-fast；
- `TestAllValidToolCallsRun`：两个合法 call 各执行一次，历史与事件序列完整；
- `TestNonMalformedToolCallErrorsKeepTheTurnAlive`：未知工具、缺 required 字段仍生成可读错误结果并继续。

## 修后验证

在隔离副本、上述共享导出同时存在的工作树上实跑：

```text
go test ./internal/agent ./modules/tools -count=1   # exit 0，两包 ok
go test ./... -count=1                              # exit 0，全仓所有测试包 ok
go vet ./...                                        # exit 0
go build ./...                                      # exit 0
```

未启动 GUI，未 commit / push，未运行会移动模块或杀进程的共享树脚本。

## 未验证项

- 未调用真实模型网关，未做真实 `dshd` + 网关 + v4 文件的端到端故障复现；本包新增证据是 Loop
  内存历史、Runner / Exec 计数与事件序列。落盘错误结局依据现有 sink / closer 源码及全仓测试，
  不外推为真机验收。
- 未与 Node 上游逐事件对照，未重新在线抓取发布页；上游语义依据上述带哈希的官方发布说明副本。
- 未运行 `-race`、跨仓 UI 联调或完整工作日测试；这些不属于本次单包兼容修复的已完成证据。
