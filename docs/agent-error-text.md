# AGENT-ERROR-TEXT：失败工具保留真实结果文本

> 基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当前共享导出（含 W19 全响应参数闸）。
> 本任务只改 `internal/agent/loop.go`、`internal/agent/loop_test.go` 与本文；不改其他包、Transport 或 v4 契约。

## 结论

DSH 0.2.2 的失败工具结果语义要求：工具已经返回的真实正文不能因为同时返回 `err` 而被合成错误替换。

`runTool2` 现在从原始 `res` 复制出 `shown`，再走原有投影与 `tool/result` 事件：

- `Text` 非空：原样保留，模型 tool 消息由同一个 `shown` 投影，客户端事件也拿到同一份真实正文；
- `Data`、`Artifacts`、`View`：随原始 `res` 保留，不再因重建 only Summary/Hint 的 Result 而丢失；
- `Hint`：随原始 `res` 保留；原始 `err` 继续放在 `Event.Err`，并设置 `Event.IsError=true`；
- `Text` 为空：才沿用 `Summary = "Error: " + err.Error()` 的合成错误回退，避免模型和客户端只看到无原因的红色失败记录。

取消/中断分支没有改动：`context.Canceled` 与 `context.DeadlineExceeded` 仍使用现有英文
`outcome is unknown` 摘要和中文“结果未知”Hint，不把可能已执行的命令说成确定失败。

W19 的 `validateCompletedToolCalls` 全响应参数闸及其调用位置保持不变：任一 completed call
参数不是 JSON object 时，仍在首个工具事件或 `Runner.Run` 前终止整个响应。

## 必红测试与修前证据

新增 `TestToolErrorPreservesActualResult`，让工具返回：

- `Text = "actual-body"`；
- 非空 `Data`、`Artifacts`、`View` 与原始 `Hint`；
- 同时返回非空 `err`。

测试走完整 `Loop.Run`，并使用生产投影 `session.ProjectResult`，同时检查内存历史中的模型
tool 消息与 `EventToolResult`。还新增 `TestToolErrorWithoutTextUsesSyntheticError`，钉住无
`Text` 时的合成错误与 Hint 回退。

只加入测试、尚未修改 `loop.go` 时实跑：

```text
$ go test ./internal/agent -run 'TestToolErrorPreservesActualResult|TestToolErrorWithoutTextUsesSyntheticError' -count=1
--- FAIL: TestToolErrorPreservesActualResult (0.00s)
    loop_test.go:251: 模型 tool 消息丢了工具真实 Text："Error: synthetic failure must not replace actual output\n提示：保留原始提示"
FAIL
FAIL    dshgo/internal/agent    0.039s
FAIL
[exit code: 1]
```

这证明旧实现确实用合成错误覆盖了 `actual-body`；不是仅靠字段推断的回归测试。

## 修后验证

```text
go test ./internal/agent -run 'TestToolErrorPreservesActualResult|TestToolErrorWithoutTextUsesSyntheticError|TestCancelledToolIsUnknownNotFailed|TestValidCallBeforeMalformedCallIsNotExecuted|TestMalformedToolCallPositionFailsWholeResponse' -count=1
# ok dshgo/internal/agent；exit 0

go test ./internal/agent -count=1
# ok dshgo/internal/agent；exit 0

go test ./... -count=1
# 全仓所有测试包 ok；exit 0

go vet ./...
# exit 0

go build ./...
# exit 0
```

未 commit / push，未启动 GUI，未运行会移动 `modules/` 或杀进程的全局脚本。

## 未验证项

- 未调用真实模型网关，未做真实 `dshd` + UI + v4 文件的端到端故障复现；本任务证据是 Loop
  内存历史、生产投影和 `tool/result` 事件。
- 未运行 `-race`、跨仓 UI 联调或完整工作日测试，不把单包与全仓 Go 门禁外推为真机验收。
