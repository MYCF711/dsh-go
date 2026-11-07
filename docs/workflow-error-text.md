# workflow 嵌套失败保留真实输出（v2 冻结语义修正）

> 基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 共享 after 工作树。改动仅限 `modules/tool/workflow/workflow.go`、同包测试与本文；未改 `internal/agent/loop.go` 或其他包，未提交、未推送、未启动 GUI。

## 纠正原因

原始缺陷是 `runOne` 在子调用返回 `(res, err)` 时只把 `err.Error()` 写入 `trace.Brief`，导致 `res.Text` 的真实正文以及 `res.Data / Artifacts / View / Hint` 丢失。**原始“单步失败记 trace、后续继续、整次 `err=nil`”不是缺陷，而是必须冻结的编排语义。**

上一版虽然保留了真实正文，却新增 `nestedCallError`，把普通嵌套工具失败上抛成整次 workflow error，并把原 `TestStepFailureDoesNotAbortScript` 改名、收窄为只测超时。这会破坏后续步骤继续执行，不得集成。

**W22 已修原生 agent 错误投影，不需要 workflow 故意制造顶层 error 来保留正文。** 0.2.2 只要求真实错误正文和结构字段不丢，不要求顶层 `IsError`；失败步骤自己的 `trace.isError=true` 即可。

## v2 冻结契约

普通嵌套工具返回 `res{Text:"actual-body", Data, Artifacts, View, Hint} + err` 时：

1. 当前步骤 `trace.OK=false`、`trace.isError=true`；`Detail` 保留错误原文。
2. `Brief` 优先保留真实正文首行，`Text / Data / Artifacts / Hint / View` 分别保留原 `res` 字段；空 Hint 也原样保留，不伪造子调用 Hint。
3. `runOne` 返回该 trace 和 `nil` hardErr，后续步骤继续执行；失败结果仍可供后续 `${var}` 引用。
4. 最终 `Exec err=nil`、`Data.ok=false`、`failed=1`，结果 `Text` 含 `actual-body`，并含后续成功步骤。
5. 只有参数/script 解码、空步骤、缺 `SubDispatch`、嵌套过深、缺 tool 及既有通道级 hardErr 才返回非 nil error。普通子调用错误不属于 hardErr。
6. `context.Canceled` / `context.DeadlineExceeded` 维持原语义：记失败 trace、继续后续步骤、整次 `err=nil`。
7. 成功路径、步骤顺序、foreach 顺序与 `Stop` 传播不变。

实现上完全移除 `nestedCallError` 及 `Exec` 的普通错误上抛分支，只在 trace 中增加真实结果投影；既有通道级 hardErr 分支保持原样。

## 回归测试与修前红证

原 `TestStepFailureDoesNotAbortScript` 已按基线原样恢复，继续固定“单步失败不是脚本失败”。新增 `TestCancellationDoesNotAbortScript` 显式覆盖 `context.Canceled`。

`TestNestedFailurePreservesTraceAndResult` 使用普通 `errors.New("nested failed")`，同时返回：

- `Text = "actual-body"`；
- 嵌套 `Data`、非空 `Artifacts`、非空 `View`、原始 `Hint`；
- 后续 `after` 步骤可正常成功。

三路断言不是放宽上一版测试，而是同时加强正文、结构与控制流约束：

- `trace`：`runOne err=nil`，`OK=false`、`isError=true`，`Brief/Text/Detail` 与全部结构字段逐项相等，序列化 trace 含 `actual-body` 和 `"isError":true`；
- `result`：整次 `err=nil`，调用序列严格为 `boom → after`，`Data.ok=false`、`failed=1`，两步 trace 均存在，结果 Text 同时含 `actual-body` 和 `after [ok]`；
- `empty hint`：普通失败仍是 soft failure，正文与结构字段不丢，空 Hint 不被伪造。

先改测试、后改实现，在上一版 after 树实跑：

```text
$ go test ./modules/tool/workflow -run '^(TestStepFailureDoesNotAbortScript|TestNestedFailurePreservesTraceAndResult)$' -count=1 -v
=== RUN   TestStepFailureDoesNotAbortScript
--- PASS: TestStepFailureDoesNotAbortScript (0.00s)
=== RUN   TestNestedFailurePreservesTraceAndResult
=== RUN   TestNestedFailurePreservesTraceAndResult/trace
    普通单步失败不得成为 runOne 的 hardErr：nested failed
=== RUN   TestNestedFailurePreservesTraceAndResult/result
    普通单步失败不得成为整次 workflow error：nested failed
=== RUN   TestNestedFailurePreservesTraceAndResult/empty_hint
    空 Hint 的普通失败同样不得成为 hardErr：nested failed
--- FAIL: TestNestedFailurePreservesTraceAndResult
FAIL
exit code: 1
```

红测准确命中上一版新增的整次 error 行为；原冻结测试同时 PASS，证明测试本身可运行。

## 修后验证

以下命令均在最终代码与同包测试上实跑：

```text
go test ./modules/tool/workflow -count=1 -v
PASS：真实正文/结构字段、普通失败继续、Canceled、DeadlineExceeded、成功顺序、foreach、Stop 等全部通过

go test ./modules/tool/workflow -run '^(TestStepFailureDoesNotAbortScript|TestCancellationDoesNotAbortScript|TestNestedFailurePreservesTraceAndResult)$' -count=1 -v
PASS：原单步失败测试及两个补充回归全部通过

go test ./cmd/dshd -run '^TestEveryToolErrorHasHint$' -count=1 -v
PASS：扫描 42 个工具文件、365 个返回 error 的点，每个都带 Hint

go test ./... -count=1
exit 0
go vet ./...
exit 0
go build ./...
exit 0
```

未运行会原地移动 `modules/` 的 `scripts/check.sh`，未运行带全局进程清理的 `scripts/regress.sh`。本任务未做 GUI 或真机验收，验收范围是上述包内与全仓 Go 门禁。
