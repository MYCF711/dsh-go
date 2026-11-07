# GO.1 稳定版 · 问题报告与可执行任务表草案

> 生成时间：2026-11-07 03:35（Asia/Shanghai）
> 用途：交给更强模型生成可直接执行的任务表
> 结论：**GO.1 未封版，当前为 NO-GO**

## 0. 一句话结论

GO.1 的接口、构建、安装、W152 覆盖都已基本通过，**唯一封版阻断是 TUI inline normal-buffer 的物理视口/scrollback 行账本**：纯帧层已经全绿，但真实 ConPTY 下仍会丢行/重复行。任何局部修复都会在 W170、W175、W171 之间互相交换，不能用“某组绿”封版。

## 1. 当前真实基线

### 1.1 最终 full 结果

| 项 | 值 |
|---|---|
| RUN | `w163-final-20261106-2302` |
| 结果 | **NO-GO** |
| `taskFailures` | 9 |
| `assertionFailures` | 9 |
| `hygieneFailures` | 0 |
| `sourceDriftAfterRun` | **true**（不可作封版证据） |
| `goSourceDriftAfterRun` | false |
| `goAllowed` | false |
| W152_COVERAGE | `OK=769 FAIL=0 UNVERIFIED=0` |
| AgentSession | `OK=29 FAIL=0 UNVERIFIED=0` |
| 64 命令矩阵 | `OK=63 FAIL=1`（`/export`） |

证据：
`C:/Users/Administrator/AppData/Local/Temp/W163-evidence/w163-final-20261106-2302/summary.json`

9 个任务红：
`w164-conformance`、`w167-80x24-final`、`w167-120x36-resize-final`、`w168-80x24-final`、`w168-120x36-final`、`w168-minimal-two-turns`、`w170-80x24-final`、`w171-80x24-final`、`w171-120x36-final`。

> 注意：full 跑在 W193–W199 补丁之前，且运行期间源码被并发修改，`sourceDrift=true`，所以这次分数只能当“发现红点”的基线，**不能当最终验收**。

### 1.2 已转绿（定向验证）

| 项 | 结果 | 证据 |
|---|---|---|
| W164 corrected conformance | `OK=120 FAIL=0`，defer `8/8` | `tmp/W194/RESULT.md` |
| W168 minimal | `8/8` | `tmp/W194/RESULT.md` |
| W168 80×24 | `OK=33 FAIL=0` | `tmp/W194/RESULT.md` |
| W168 120×36 | `OK=33 FAIL=0` | `tmp/W194/RESULT.md` |
| W170 80×24 | `OK=44 FAIL=0` | `tmp/W198/RESULT.md`、`tmp/W199/logs/w170-*` |
| W167 80×24 | `OK=75 FAIL=1`（仅 resume 真实红） | `tmp/W198/RESULT.md` |
| W175 row-protocol | `OK=10 FAIL=0` | `tmp/W198/RESULT.md`、`tmp/W199/RESULT.md` |
| W175 frames（W200 shrink4） | `OK=131 FAIL=0` | `tmp/W200/logs/w175-frames-shrink4.log` |

### 1.3 仍红

| 项 | 结果 | 红点 |
|---|---|---|
| W171 80×24 | `OK=40 FAIL=2`（W199 最终） | 丢 `user.text`、`user.long`、`assistant.multiline`；同一代码多次跑出 `40/2`、`41/1`、`40/2` |
| W171 120×36 | `OK=39 FAIL=3`（W199 最终） | 重 `user.text`、`user.long`；块重复 |
| W167 120×36 | `OK=74 FAIL=2` | inline-scrollback 重复族 + resume |
| W170 120×36 | `OK=43 FAIL=1` | `before-round-4-model-overlay-closed` 残留 `["    Reasoning effort"]` |
| W168 | 定向已绿，但 full 旧快照仍红 | 需冻结快照重跑确认 |
| W164 | 定向已绿，但 full 旧快照仍红 | 同上 |

## 2. 根因（已取证）

### 2.1 重绘切片不贴底（停泊光标偏移）

`viewportRepaintPatches` 只在短帧（`height < viewportHeight`）时贴底。带 `strandedPad` 的高帧从视口顶起画，行数不足 `V-1`，**停泊光标停在视口倒数第 N 行**。之后整条相对寻址链偏 N 行。

实测撕裂行：

```text
1 7 1 _ T·RdA C E 2 _a8 0 Xt2 4t_ T E X T _O K
```

W175 `frame 118` 同源：视口 10 行、`strandedPad=3`，只画 8 行，下一帧整体偏 1 行，`ROW_02` 被画两次。

### 2.2 对齐后的物理帧高于逻辑帧

trace：

```text
n=134 diff ph=41 -> nh=45   lph=38 -> lnh=38   vy=8 pad=8
```

逻辑帧高度没变（38→38），`alignIdentifiedFrames` 为了让锚点保持物理位置，把下方行整体下推，物理帧涨到 45。终端没有真的多出这 4 行，后续按帧高差发 LF 让终端滚动，记账整体偏 4 行，仍在屏上的行被当成“已进 scrollback”，下一帧 chrome 直接盖上去。

> 更正：W198 说“前导段 `physicalStart<0` 丢行”不成立；真实帧上首个锚点 `physicalY>=nextY`，`copyRow` 的 `to<0` 分支不会命中。真正问题是**高度膨胀 + 滚动记账偏移**。

### 2.3 差分把“帧画不出来的行”当空行擦掉

某些帧逻辑上还在屏上，帧里却是空白；旧代码把空格写下去，这一行永远不会进 scrollback，文字永久消失。

trace 里同类“同 id 由有字变空”7 次，包括：

```text
BLANK n=220 t:6:0  '  > printf W171_..._JOB_OUTPUT' -> ''
BLANK n=268 t:8:0  '  ● W171_..._JOB'               -> ''
```

### 2.4 具体交换（为什么不能局部补）

| 尝试 | 结果 | 回归 |
|---|---|---|
| 对齐高度闸 | W170 120×36 → `44/0` | W175 row-protocol `10/0 → 9/1`（`W175_REF_U2` 整行丢失） |
| keepPixels | W171 80×24 → `41/1` | W170 关闭残留 `44/0 → 42/2`（浮层 `t:` 行被误保留） |
| 纯官方 0.18.0 `log-update` | — | W175 `122/9`、row `9/1` |

结论：**不能再用局部 if/高度闸/keepPixels 打补丁**，必须重做物理视口账本。

## 3. 可预见难度与风险

| 风险 | 概率 | 影响 | 说明 |
|---|---|---|---|
| 修 W171 导致 W175 row 回归 | 高 | 阻断 | 已发生两次（高度闸、keepPixels） |
| 真实 ConPTY 非确定性 | 高 | 高 | 同代码 80×24 出现 `40/2`、`41/1`、`40/2`；单次绿不可信，必须连续 3 次 |
| 并发编译污染 | 中高 | 高 | W199/W200 同树，`lib/` 被换，f4 结果作废 |
| 冻结 full 时源码漂移 | 中 | 高 | 必须在只读快照上跑，`sourceDrift=false` 是硬门 |
| 上游官方实现直接替换 | 中 | 高 | 纯官方 W175 `122/9`、row `9/1`，不能直接交付 |
| W167 Ctrl+Enter 序列 | 中 | 中 | 隔离探针正确，红在 Tab+Enter+两次 export 后的状态机 |
| W170 `/model` 深帧整块不显示 | 中 | 中 | 帧 91 行、viewportY=68 走 tail-append 分支 |
| 测试口径混入产品判断 | 中 | 中 | W195 曾把 JobCard `│` 行当 tool 行；口径需书面固定 |
| 稳定版打包/版本 | 低 | 中 | `0.2.2-go.1` 双渠道、宿主名已定 |
| 模型网关 id | 低 | 低 | 已切 `group/deepseek-v4.1-flash-1017`，旧 id 404 |

## 4. 建议任务表（可直接派单）

### T0 · FREEZE-SNAPSHOT

- **目标**：冻结当前 W182 候选树（含 W193/W194/W196/W197/W198/W199 的最终状态），生成只读快照与哈希清单。
- **owned paths**：`tmp/snapshot-*`、`tmp/W163/frozen-*`
- **验收**：
  - `sourceTreeSha256`、`packageSourceSha256`、`dshdSha256` 写入 JSON
  - 快照只读
  - 不再接受任何并发写入
- **禁止**：reset、checkout、复制 `vendor` 符号链接、并发 PTY。

### T1 · RENDER-LEDGER（最高优先）

- **目标**：重写 inline normal-buffer 重绘账本，满足三条不变量：
  1. 对齐后物理帧高度 `<=` 逻辑帧高度
  2. 重绘切片必须贴底，停泊光标在视口最后一行
  3. scrollback 推进以真实终端滚动为准，不按逻辑高度差发 LF
- **owned paths**：`src/ink/log-update.ts`、`src/ink/frame.ts`、`src/ink/row-identity.ts`、必要的 `MessageList.tsx`/`Chat.tsx`
- **依赖**：T0
- **红测**：W171 80×24 当前 `40/2`；120×36 当前 `39/3`
- **验收**：
  - W171 80×24 连续 3 次 `42/0`
  - W171 120×36 连续 3 次 `42/0`
  - W175 frames `131/0`
  - W175 row-protocol `10/0`
  - 不出现 `ROW_02` 重复、`W175_REF_U2` 丢失
- **证据**：每轮原样 `SUMMARY`、`result.json`、`scrollback-*.txt`、`conpty.raw.log`
- **禁止**：blank-spacer、隐藏 tool/JobCard、删断言、放宽唯一性、单次绿当稳定。

### T2 · W167-SEQUENCE

- **目标**：修 `Ctrl+Enter` 在 “Tab 排队 + Enter steer + 两次 `/export`” 后的状态机；保持 W169 已验收语义（interrupt + followup）。
- **owned paths**：`src/components/PromptInput.tsx`、`src/screens/Chat.tsx`、`src/ink/*`、W167 driver（仅测试口径）
- **依赖**：T1
- **验收**：
  - W167 80×24 `75/1`（只保留已证 resume 真实红，或全绿）
  - W167 120×36 行丢失/重复全绿
  - `ctrl-enter-interrupt-notice`、`ctrl-enter-real-assistant-visible` 绿
  - `resume-original-session-remounted` 若仍红，必须判定产品/驱动并书面记录
- **禁止**：改回旧 `interruptAndDeliver` 语义、隐藏内容。

### T3 · W170-MODEL-OVERLAY

- **目标**：修 `/model` 深帧第 4 轮整块面板不出现（帧 91 行、viewportY=68 的 tail-append/绝对浮层分支）。
- **owned paths**：`src/components/ModelPicker.tsx`、`Chat.tsx`、`log-update.ts`
- **依赖**：T1
- **验收**：
  - W170 80×24 `44/0`
  - W170 120×36 `44/0`
  - 关闭后无 `Reasoning effort` 残留
- **禁止**：靠裁切/隐藏内容通过。

### T4 · W168-EXPORT-REGRESSION

- **目标**：确认 W194 的 turn ordinal 修复在冻结快照下仍绿；不得回退。
- **验收**：
  - W168 minimal `8/8`
  - W168 80×24 `33/0`
  - W168 120×36 `33/0`
- **注意**：`src/dsh-go/` 与 `src/backends/dsh-go/` 是两份重复后端，产品走后者；不要同时改两份导致漂移。

### T5 · FULL-FREEZE

- **目标**：在冻结快照上重跑 W163 full。
- **命令**：
  ```bash
  python -B tmp/W163/run_final.py --mode full \
    --source <frozen-source> \
    --go-source D:/DeepSeek Harness/default-workspace/DSH-GO \
    --run-id <fresh> --jobs 1
  ```
- **验收**：
  - `taskFailures=0`
  - `assertionFailures=0`
  - `sourceDriftAfterRun=false`
  - `goSourceDriftAfterRun=false`
  - `GO_ALLOWED=1`
  - W152 `769/0`
  - AgentSession `29/0`
  - 64 命令 `64/0`
- **禁止**：运行中改源码、并发编译、并发 PTY、看门狗超时算通过。

### T6 · RELEASE

- **目标**：构建 `0.2.2-go.1` 稳定版。
- **验收**：
  - `bin-check` 通过
  - `DSH GO.exe` / `dsh-0.2.2-go.1.exe` 版本信息正确
  - 正式 TUI 包 sha256 与 full 证据一致
  - 真实 dshd + 正式 `src/backends/dsh-go` + ConPTY 可玩
  - 双渠道：稳定 `0.2.2-go.1`、测试 `0.2.2-beta.1` 不混
- **禁止**：未过 CI 推 GitHub；本机不重启 DSH/magpie。

## 5. 当前现场与关键哈希

| 对象 | 路径/哈希 |
|---|---|
| 宿主仓 | `D:/DeepSeek Harness/default-workspace/DSH-GO` |
| TUI 候选树 | `C:/Users/Administrator/AppData/Local/Temp/dsh-TUI-tool-rows-w182` |
| 新 dshd | `tmp/W172/bin/dshd.exe` sha256 `5c7b1105c0dd18c75775636d83a06626514c04e28c9a0048f493f284210500d2` |
| 官方 log-update | `dsh-TUI-upstream-src/src/ink/log-update.ts` sha256 `c1b3ab31d004cb70d0c4b3a7b21cec8e0610461d0cd7ff9c3353e64f66ce12c0`（1248 行） |
| W193 patch | `w193-final-tui.patch` sha256 `55cb843e970e5a3b2b960173f06c73c0ba196f3bf637452f51c8e28168f05fd6` |
| W194 patch | `w194-targeted.patch` sha256 `9d4dbbdb3fd6ae5e1b66153f6a1d272f71034627f5e616bd4aaf483ebf62c18c` |
| W196 patch | `w196.patch` sha256 `43f6fa96de8f99ac3b312f02e4f49f0cd067748088a5460ff9631ce4938ee9b4` |
| W197 patch | `w197-model-overlay.patch` sha256 `b38e8eb351e41f8aab440cc1d43327b38201afa32cfca7e2109c042a321f6116` |
| W198 patch | `w198-final-tui.patch` sha256 `710ea6ac24e9c0cf1c6dc048d8ee5851ba39ea53cbfc5c07b24754b1d6e55291` |
| W199 patch | `w199-row-integrity.patch` sha256 `43ba15b07b6a19ebbb68f4dd97a5b8c79579305198967183b750b48e45003910` |
| W200 | 进行中（shrink5），无最终 patch；`log-update.ts` sha256 `71e28546ae8c1a228191e853edb2bb62171caf590c992e055290718e74bbd18b`（会变）。最新：W170 80=`44/0`、W170 120=`43/1`、W171 80=`40/2`、W171 120 复跑中、W175 frames=`130/1`、row=`10/0` |

## 6. 明确不能做的事

- 不能隐藏 tool/JobCard 内容、不能 blank-spacer、不能删断言、不能放宽唯一性。
- 不能 reset/checkout 共享树、不能覆盖他人改动、不能原地移动 `modules/`。
- 不能并发跑多个 PTY、不能在 PTY 跑分时 `pnpm compile`。
- 不能拿一次 `42/0` 当稳定；至少连续 3 次双尺寸。
- 不能拿 `sourceDrift=true` 的 full 当封版证据。
- 不能把纯官方 `log-update` 直接合入（W175 `122/9`、row `9/1`）。
- 不能推 GitHub、不能重启 DSH/magpie、不能杀非自有进程。

## 7. 建议给更强模型的首个执行提示

```text
你在 GO.1 的 RENDER-LEDGER 阶段。先读本报告的 §2、§4 T1/T2/T3 和 §5 现场。
不要重跑 full，不要 reset，不要并发 PTY。
先冻结 W182 候选树，再只改 log-update/frame/row-identity 的物理视口账本：
1) 对齐后物理高度不得大于逻辑高度；
2) 重绘切片必须贴底、停泊光标在最后一行；
3) scrollback 只按真实终端滚动推进。
然后串行跑 W171 80/120 各 3 次、W167 80/120、W170 80/120、W175 frames/row。
任何一次需要交换另一组回归的修法都拒绝，回到账本重做。
```
