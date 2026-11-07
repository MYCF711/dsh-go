# 全局与项目 AGENTS.md 提示预算（0.2.2）

## 来源与装配顺序

`prompt.Default` 按以下顺序拼接非空段：

1. `Runtime`
2. `ToolGuide`
3. `GlobalRules`：全局约定
4. `WorkspaceRules`：工作区约定

ACP 的全局来源是 **`<acpRunner.home>/AGENTS.md`**。`cmdACP` 在创建 runner 后把同一个 `acpRunner.home` 传给提示装配，不调用 `os.UserHomeDir`，也不读取真实用户 home。调用 `prompt` 包的其他装配方可以显式设置 `Context.GlobalRulesPath`：空串表示不注入；绝对路径直接使用，相对路径按 `Context.Cwd` 解析。

非 ACP 的 `buildSystemPrompt` 保留原入口，不传 home，因此不会隐式增加全局文件。全局路径为空、文件不存在、读取失败或文件为空时，全局段整段消失；没有全局文件时，项目提示输出与原有单链行为一致。项目文件缺失同样不影响全局段。

## 两条独立预算

| 规则链 | 上下文字段 | 默认正文上限 |
|---|---|---:|
| 全局 | `MaxGlobalBytes` | 8000 字节 |
| 项目 | `MaxWorkspaceBytes` | 8000 字节 |

两条链分别读取、分别截断，**不共用一个池，也不按全局/项目顺序争抢额度**。任一文件超大，最多只截断自己，另一份仍按自己的上限保留。默认两份正文合计最多 **16000 字节**；两条链都自定义为 `N` 时，合计最多 **2×N**。字段也允许不同值，此时合计上限是两者之和。

ACP 的 `--workspace-bytes=N` 会同时赋给 `MaxGlobalBytes` 与 `MaxWorkspaceBytes`，所以该入口始终满足“各 N、合计最多 2×N”。经 `Default`/`Assemble` 装配时，未设置或非正的预算字段采用各自默认值 8000。

这里的“正文”仅指从规则文件读取并注入的内容切片，**不包含**段标题、文件路径和“以上内容已截断”说明。限制单位是字节，不是 rune 或 token；16000/2×N 也不是整个 system prompt 的总大小上限，因为 `Runtime`、`ToolGuide` 和各段包装不在该预算内。

## ExtraRulesPath 与边界

`ExtraRulesPath` 只覆盖项目链，语义不变：

- 绝对路径直接使用，相对路径按 `Cwd` 解析；
- 指定文件存在且非空时，用它替代项目 `AGENTS.md`，不会同时注入两份项目规则；
- 指定文件不存在、读取失败或为空时，回落到 `<Cwd>/AGENTS.md`；
- 无论覆盖还是回落，全局 `AGENTS.md` 都继续按自己的预算注入。

全局段位于项目段之前；空段不产生额外空行。截断保留原有说明，提示模型需要细节时再用 `read` 工具读取，而不是把剩余内容隐式塞进下一轮。

## 验证

`internal/prompt/budget_test.go` 使用 `t.TempDir()` 覆盖：默认各 8000、自定义相同/不同值、双向超大互不挤占、合计上限、全局/项目缺失、无全局时逐字节兼容，以及 `ExtraRulesPath` 的绝对覆盖、相对覆盖和缺失回落。

```bash
go test -count=1 ./internal/prompt ./cmd/dshd
go test -count=1 ./...
go vet ./...
go build ./...
```
