# 已知缺陷：chat-import 导入的会话**续不了聊**（2026-11-19 发现）

> 状态：**未修**。登记时间 2026-11-19，由端到端验证（`r5-verify-import`）实测发现，Lead 复核。
> 影响面：`dsh-go#71` + `dsh-ui#168` 刚合入的 chat-import 功能，**最核心的用途（导入历史后接着聊）当前不可用**。

## 1. 现象（实测，不是推断）

用真 dshd + 按官方结构造的导出 ZIP 导入会话后：

```
session.prompt  ->  accepted: true
60 秒内 v4 日志 ->  一条事件都没有增加
journal         ->  落下 turn/error
```

`turn/error` 正文：

```
缺失 sandbox 策略上下文: 会话 … 的 preset 覆盖与 header.agentPreset 都是空的
```

手工跑子进程复核，同样结果：

```
dshd run --session <id>   ->  退出码 1，同一句错误
```

## 2. 因由链（逐跳有 `file:line`）

| # | 位置 | 做了什么 |
|---|---|---|
| 1 | `internal/session/import.go:34` | 建导入会话时**显式传 `agentPreset=""`**（对齐官方 `dsh-…/importer.ts:562`） |
| 2 | `cmd/dshd/mode_prompt.go:46-58` | 取会话 preset：preset 覆盖与 `header.agentPreset` 都空 → 得到空 |
| 3 | `cmd/dshd/serve_sandbox_policy.go:173-197` | **fail-loud** 拒绝（`internal/sandbox/policy.go:91`） |
| 对照 | `internal/session/log.go:31-33`、`cmd/dshd/acp.go:1850` | **普通建会话必带 preset**（`r.modeOf(…).ID`） |

## 3. 已核实的官方事实（决定它是 parity 还是取舍）

在 rc.5 解包树（`tmp/GO8-RC5/official-rc5/`）里查过：

- 官方 `importer.ts:562` **同样**传空 `agentPreset` —— 所以第 1 步不是我们的偏差
- 官方 **`dsh-sandbox/src` 里没有任何 `agentPreset` 引用** —— 官方沙箱策略**不读 preset**
- 在 `dsh-agent-preset-registry/src`、`dsh-agent-default-model/src` 里**没有** `defaultPreset` / `fallbackPreset` / `preset ?? 'default'` 这类兜底

2026-11-19 追加两条（把推论往前推了一步）：

- 官方 `dsh-agent/src/index.ts:86` 的 `meta.agentPreset` 是**可选**的（`readonly agentPreset?: string`）
  -> **没有 preset 的会话在官方是合法状态**，官方不会因此拒绝建会话或拒绝跑回合
- 官方 `dsh-sandbox/src/index.ts:49-50` 的 `SandboxPolicy` 把 `mode` 当**入参**带着；
  该包里没有任何从 preset 推导 mode 的逻辑 -> **官方是"调用方决定 mode"，不是"preset 决定 mode"**

**推论（明确标为推论，不是结论）**：把「preset 为空」当成**硬失败**，是**我们宿主自己加的约束**，官方没有这条。若成立，这属于 parity 缺陷，改法是让沙箱策略在 preset 为空时按官方的方式解析，而不是 fail-loud。

**最后一块拼图已找到（2026-11-19 续查）**：官方把「沙箱档位」放在**独立的服务**里，
和 agent preset **不是一个东西**——

- 官方包 **`dsh-permission-presets`**（`src/index.ts:20-45`）是独立服务：
  自带 `PermissionCatalog` / `PermissionSelection` / `PresetOption`，
  选择落成一条 **durable、log-only** 的会话事件，然后调
  `setSandboxMode`（来自 `dsh-sandbox-policy`）与 `setApprovalPolicy`（来自 `dsh-user-approval`）
- `dsh-sandbox-policy` 才是持有并设置 mode 的地方；`dsh-sandbox` 只把 `mode` 当入参带

**所以这是一个 parity 缺陷，不是取舍**：我们宿主把沙箱档位的来源读成了 `agentPreset`，
而官方用的是 `permissionPresets` 那份自己的选择（带默认档）。

## 6. 本仓的偏差位置（精确到函数）

`cmd/dshd/serve_sandbox_policy.go` 的 `sandboxPresetIDForSession` 依次取：

1. `state/session-preset.json` 的显式选择（**本仓自有机制，非官方**）
2. 否则读 v4 `header.AgentPreset`
3. 两处都空 → **fail-loud**

第 2 步就是把 agent preset 当成沙箱档位来源——官方没有这层对应关系。
导入的会话两处都空，于是必然落到第 3 步。

## 7. 官方默认档怎么来（诊断完成，2026-11-19）

`dsh-permission-presets/src/index.ts:218-226`：

```js
const inferredDefault = this.derive(EMPTY_KNOBS)
const defaultPreset = config.defaultPreset.get() ?? inferredDefault
if (defaultPreset === CUSTOM_PRESET) {
  throw new Error('permission: composed sandbox and approval defaults match no preset; configure defaultPreset explicitly')
}
```

**官方不会因为「这个会话没记录过档位」而失败**：它先用 `derive(EMPTY_KNOBS)` 从
**进程组合出来的 sandbox / approval 默认值**推一个 `inferredDefault`，
配置里没显式指定 `defaultPreset` 就用它。只有在「组合默认值匹配不上任何预置档」时
才抛错，且错误文案明确要求显式配置 —— **这是"配置不自洽"的失败，不是"会话缺记录"的失败。**

另有两条硬约束（同文件 `:209-217`）：`custom` / `auto` 是保留名不许当表项；
`ctx.shell.sandboxMode` 为 undefined 时直接抛「mounted bash executor does not confine」。

## 8. 修法（方向已闭合，尚未实现）

把 `sandboxPresetIDForSession` 的第 3 步从「两处都空 → fail-loud」改成
**先用本仓组合默认值推一个默认档**（对应官方 `derive(EMPTY_KNOBS)`），
只有推出来的结果匹配不上任何预置档时才 fail-loud（对应官方那条 `CUSTOM_PRESET` 抛错）。
`agentPreset` 不再充当沙箱档位的兜底来源。

**为什么这次仍不动手**：这要改沙箱解析链，动之前必须先把本仓「组合默认值」到底由谁给
（`DSHD_SANDBOX_MODE`？profile？还是 `internal/sandbox` 的缺省）逐处核清，
否则改出来的"默认档"是自造的。`docs/sandbox-policy.md` 的「不假装隔离」原则要求：
改完之后**该失败的地方仍然要失败** —— fail-loud 要搬家，不能消失。

**但这一步没有查到官方确切的分支**，所以不凭推论改产品：那个分支怎么走要嘛继续在官方源码里逐层找，要嘛由用户裁定导入历史归哪个 preset。两者都不适合猜。

## 4. 为什么没有当场修

改它有两种可能的方向，**且没有客观优劣**：

1. **修沙箱策略**：preset 为空时按官方方式解析（前提是先查到官方确切分支）
2. **导入时给一个显式 preset**：要给「导入的历史会话」定归属，这是产品取舍

方向 1 是查证后的 parity 修复；方向 2 是取舍。**在没查到官方分支之前选任何一个都是猜。**

## 5. 关联

- 上一轮的端到端验证产出：宿主 95/95、9 个边界变体全对、GUI 真机链 PASS（`dsh-ui#169`）
- **这条例外没有推翻那些结论**：导入本身是对的（事件逐条对、标题进读索引、错误码全对），坏的是**导入之后的续聊**
- 同类 fail-loud 设计见 `docs/sandbox-policy.md`——那条设计原则是「不假装隔离」，本条要改也必须守住它：**不许为了让续聊能跑而把沙箱降级成静默放行**
