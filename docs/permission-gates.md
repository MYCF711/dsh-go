# 权限分支：一张必须对得上的表

## 为什么要单独一张表

其他地方的 bug 会报错。权限分支的 bug **不一定报错**：

```go
// internal/spi/tool.go
func (c Caps) NeedsApproval() bool {
    return c.Writes || c.Executes || c.Network || c.Destructive
}
```

把最后一个字段删掉，**所有测试照样绿**，而从此任何"可能不可恢复"的工具都不再问人。

这类改动的共同点：**它不制造失败，它消除失败的机会**。所以必须靠"每个判定都得有一条测试钉着"来防，不能靠"跑一遍测试看看"。

## 现在有哪 10 个判定

| 判定 | 钉住它的测试 | 它在决定什么 |
|---|---|---|
| `spi.Caps.NeedsApproval` | `TestNeedsApproval` | 所有工具的第一道筛：要不要问人 |
| `cmd/dshd.approvalDecision` | `TestApprovalDecision` | 模式倾向 × 记忆 → 放行/问/拒 |
| `guardedRunner.Run` | `TestGuardedRunnerRefusalDoesNotExecute` | **执行点**：被拒时一次都不能跑 |
| `cmd/dshd.deniesSilently` | `TestDeniesSilently` | 是"人拒绝"还是"策略不让人被问" |
| `cmd/dshd.makeAskFuncWith` | `TestMakeAskFuncPolicies` | 四种 `--policy` 各自怎么答 |
| `cmd/dshd.schedulePolicy` | `TestSchedulePolicyDefaultsToReadOnly` | 定时任务默认只读 |
| `modes.Builtins` | `TestReadOnlyModeDeniesApprovals` | read-only 模式的审批倾向必须是 deny |

另有 3 个**有理由豁免**（登记表里写明了理由）：ACP 的 prompt 处理器、`cmdSchedule` 的循环体、提示词里的工具分组——它们只传递或展示判定，不做判定。

## 怎么维持

`scripts/perm-guard.py` 自动扫"函数体里出现 `"allow"` / `"deny"` / `NeedsApproval` / `modeApproval`"的函数，
然后要求每个都在 `scripts/perm-gates.json` 里登记。三种失败：

```
✗ 新的权限分支没登记：cmd/dshd/zz_sneaky.go::sneakyGate
✗ internal/spi/tool.go::NeedsApproval 的测试 TestThisTestDoesNotExist 找不到
✗ 豁免项已不存在，删掉它：…
```

**新加一个放行分支却不登记，门禁会红**——这就是它存在的全部意义。

## 一条必须记住的验收方法

给权限测试做变异时，变异**必须能编译**。

我第一次写的变异是：

```go
if false {          // 原：if !ok {
```

结果编译报 `declared and not used: ok`，被 `mutate.py` 判成 `BAD`（不是 `CAUGHT`）。
这说明工具判得对：**编译不过的变异什么都没证明**。

正确的写法是让它编译通过、但语义反过来：

```go
_ = ok
return g.inner.Run(ctx, name, args)    // 原：拒绝并返回 error
```

这次 `CAUGHT`。区别在于：后者真的跑到了测试断言那一步。
