# ACP 工具执行准入

> 2026-11-04 · base `af4a49a` · 0.2.2 审计准入漏洞修复

## 根因

`acpRunner.Prompt` 原来只用 `modes.Filter` 裁剪 `agent.Loop.Tools`，也就是只裁剪**模型声明**。执行侧却有两条绕路：

1. 配置审批时，`guardedRunner` 持有全量 `r.reg`，不是模型看到的模式视图；末尾还会把 `reg.Get` 未命中的名字交给底层 Runner。
2. `ask == nil` 时完全不包 `guardedRunner`，直接执行裸 Runner。

`modes.View.Get` 虽已按 `Mode.Allows` 过滤，但它没有进入执行链。因此，直接点名当前模式未声明的工具，或在 `workflow` 的子步骤中点名隐藏工具，都可能越过模式限制并落到 `Tool.Exec`。工具表过滤只是提示约束，不能充当执行授权。

## 修复

- `Prompt` 每轮只解析一次当前模式，并只构造一个 `view`；`Loop.Tools` 和 `guardedRunner.view` 使用**同一个视图对象及同一轮模式快照**。
- 无论 `ask` 是否为 nil，ACP 执行都经过 `guardedRunner`。守卫在审批询问和底层 Runner 之前显式检查 `view.Get(name)`：隐藏、未知工具以及空工具表均失败关闭，返回带 Hint 的错误，不进入底层执行器。
- `guardedRunner.reg` 仍只负责读取全量 registry 中的 `Caps`，避免把审批判断建立在不完整的工具元数据上；能否执行由共享 `view` 决定。非 ACP 调用方没有模式视图时仍用原 registry，不改变其正常工具路径。
- 宿主注入的 `SubDispatch.Runner` 仍是同一个 `guardedRunner`，所以 workflow 子调用重复经过同一视图和审批链，不继承顶层越权。
- 既有 `deny` 决策保持优先并直接拒绝，以保留原有审批结果；它同样不会执行工具。可见性拒绝发生在交互式审批之前，隐藏工具不会先弹审批。
- `ask == nil` 只跳过审批，不跳过可见性检查；可见写工具仍不新增审批。该路径原来没有宿主注入的 SubDispatch，这一行为也保持不变。

## 边界与不变量

- 修改仅涉及 ACP 执行准入、对应测试和本文档；未改工具 schema、内置/用户模式定义、审批决策函数、v4、Transport 或模块流水线。
- 允许工具的参数、结果和事件投影不变；顶层审批记忆、嵌套调用不继承“以后都允许”的规则不变。
- 视图按本轮固定；本轮声明和执行不会因中途模式变化而使用不同工具集。
- 隐藏与未知工具使用同一准入错误，不通过执行结果泄露被隐藏工具是否存在。
- 该修复证明宿主 ACP Runner 这一层的准入，不外推为真实模型网关、GUI 或跨仓产品验收。

## 必红证据

新增 `cmd/dshd/tool_execution_guard_test.go`，通过假 OpenAI 兼容端点驱动真实 `acpRunner.Prompt` / `agent.Loop`，使用真实 workflow 子分派，并同时记录底层 Runner 调用和 `Tool.Exec` 次数；不是只检查错误字符串。

修复前运行：

```text
go test ./cmd/dshd -run '^TestACPToolExecutionGuard$' -count=1
```

退出码 **1**，四个拒绝子测试按预期失败：

| 用例 | 修前实测 |
|---|---|
| 直接调用隐藏工具 | `Runner calls=[hidden], hidden Exec=1` |
| workflow 嵌套隐藏工具 | `Runner calls=[workflow hidden], hidden Exec=1` |
| `ask=nil` 直接调用隐藏工具 | `Runner calls=[hidden], hidden Exec=1` |
| 未知工具 | `Runner calls=[not_registered]` |

允许工具（`ask=true/false`）及 workflow 嵌套允许工具三个对照组修前即通过，证明测试场景本身能够正常执行，拒绝断言不是“永远不成功”。

## 修后验证

- 聚焦测试 `TestACPToolExecutionGuard` 与既有 `TestACPPromptPassesModeApproval`、审批及嵌套审批测试：exit **0**。
- 隐藏工具直接/嵌套/无审批路径均为底层 Runner **0 次**、隐藏 `Exec` **0 次**；未知工具底层 Runner **0 次**。
- workflow 隐藏子步骤记录 `failed=1`，允许的外层 workflow 执行一次；允许子工具及其审批仍正常。
- 模型请求只声明 `visible`、`workflow`，但直接指定隐藏名字仍被执行入口拒绝，证明声明与执行不是只靠同一份名单“碰巧一致”。

实际门禁：

```text
go test ./cmd/dshd ./internal/modes ./modules/tools
# exit 0: cmd/dshd 19.509s；internal/modes、modules/tools 通过

go test ./...
# exit 0：全仓包通过

go vet ./...
# exit 0

go build ./...
# exit 0
```
