# GO.1 · Lead 可执行任务表（2026-11-07）

> 唯一目标：在不牺牲任何 W167/W168/W170/W171/W175 断言的前提下，使 `0.2.2-go.1` 达到可封版状态。
> 当前判定：**NO-GO**。本表是 GO.1 专项执行入口，不替代 `PLAN.md` §14.1 的宿主长期顺序。

## 0. 路径、状态与共同约束

### 0.1 固定路径

| 变量 | 路径 | 用途 |
|---|---|---|
| `H` | `D:/DeepSeek Harness/default-workspace/DSH-GO` | Go 宿主仓 |
| `U` | `D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src` | TUI 上游 checkout、W163 流水线所在处 |
| `W163H` | `$U/tmp/W163` | 唯一 canonical full/targeted 构建、驱动与证据入口 |
| `LIVE` | `C:/Users/Administrator/AppData/Local/Temp/dsh-TUI-tool-rows-w182` | 当前 W182/W193–W200 候选现场；不是 git 仓库 |
| `G` | `C:/Users/Administrator/AppData/Local/Temp/GO1-<yyyymmdd-hhmmss>` | 本轮独占工作根，Lead 创建并记录 |
| `BASE` | `$G/base` | 接管后封存的只读基线 |
| `RC` | `$G/rc` | Lead 唯一集成候选树 |
| `E` | `C:/Users/Administrator/AppData/Local/Temp/W163-evidence/<fresh-run-id>` | 最终 full 证据根，必须预先不存在 |

Lead 在接管时生成 `$G/env.sh`（以下 `RC` 必须替换成实际绝对路径，后续命令先 `source` 它）：

```bash
export H='D:/DeepSeek Harness/default-workspace/DSH-GO'
export U='D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src'
export W163H="$U/tmp/W163"
export LIVE='C:/Users/Administrator/AppData/Local/Temp/dsh-TUI-tool-rows-w182'
export RC='C:/Users/Administrator/AppData/Local/Temp/GO1-<yyyymmdd-hhmmss>/rc'
```

所有真实 dshd 与 ConPTY 运行都从 C: 证据目录启动；每次运行使用全新 home、run id 和日志目录。

### 0.2 任务状态与派单规则

- 状态只写 `READY / IN_PROGRESS / BLOCKED / DONE`；`DONE` 必须有 rev 或 hash、命令、退出码、原始汇总、证据路径、未验项。
- 每个执行代理使用 `group/deepseek-v4.1-flash-1017`；任务书必须写明目标、可写路径、验收、产出路径、parent/depth/dependencies。
- 同一文件只能有一个写者。子任务在独立副本施工并交 patch；`RC` 只由 Lead 串行集成，禁止 `git add -A`。W200 未明确交付前，`LIVE` 仅其现有 owner 可写，其他任务全部只读。
- 所有子代理交付前必须亲自运行验收并原样贴输出；失败分支必须先在隔离旧实现上证明会红。
- 全局只有 **一个 PTY 锁**。任何 ConPTY 串行执行；PTY 运行期间不得 compile/pack，不得启动第二个 PTY。
- 每个长命令显式 `timeout ≤285s`（W163 自身子命令 ≤300s）；只清理本轮核对过归属的进程树。
- 禁止 reset/checkout 覆盖现场、隐藏 tool/JobCard、blank-spacer、删断言、放宽唯一性、盲更新 golden、纯官方 `log-update` 直接合入。
- 禁止重启 DSH/magpie、修改全局 proxy、push GitHub；commit/push 均不隐含在本表授权内。

### 0.3 锁与证据文件

Lead 在 `$G/` 下维护：

| 文件 | 内容 |
|---|---|
| `dispatch.md` | 本表实时状态、owner、依赖、开始/结束时间 |
| `locks.json` | `source_writer`、`pty_owner`、`build_owner`；三者均含 PID/任务 id/时间 |
| `baseline-manifest.json` | 源树、`src/backends/dsh-go/**`、dshd、patch、驱动的逐文件 sha256 |
| `integration-log.md` | 每次集成输入 patch、目标 hash、反向检查、命令与结果 |
| `pty-queue.md` | 串行队列、run id、尺寸、退出码、SUMMARY、证据路径 |

## 1. 主任务表

| ID | 初始状态 | 任务与唯一写作用域 | 前置依赖 | 可并行项 | 完成判据与产出 |
|---|---|---|---|---|---|
| **T00 接管现场** | READY | Lead 核对 W200 当前 owner、活动进程、正在写入的文件和最新 shrink 结果；只写 `$G/{dispatch.md,takeover.md,locks.json}`。 | 无 | T02/T04/T05 的准备可并行 | W200 明确为“已交付”或“保留原 owner，其他人禁写”；记录源文件 hash、未完成命令、patch/日志位置。W200 活跃时不得要求其中止，只约定下一个原子检查点交付；不得另派 `log-update.ts` 写者。 |
| **T01 封存基线** | BLOCKED | 在原 owner 确认的原子检查点，用 W163 `snapshot_source` 同口径把 `LIVE` 保存为 `BASE`；另存 W193–W200 patch、W175 套件、驱动和日志。只写 `$G/base`、`$G/baseline-*`。 | T00；W200 原子检查点 | T02/T04/T05 可对旧证据只读诊断 | `BASE` 只读；manifest 含 source/package/dshd 及 W163 runner/drivers hash；保留 full 必需的 `tmp/W152/recon-commands.md`；不得复制 vendor 符号链接。W200 未停笔前只能另存“非一致证据副本”，不得冒充开发基线。 |
| **T02 单一验收入口** | DONE | targeted 入口已建成：`$G/oracle/run-targeted.sh`、`COMMANDS.md`、W200 驱动、W175 suite、node_modules/vendor junction。 | T00 DONE；T01 DONE | T03/T04/T05 可并行 | 双尺寸基线：W170 80/120=`44/0`、W171 80=`41/1`、120=`40/2`、W167 80/120=`74/2`、W175 frames=`130/1`、row=`9/1`；证据 `$G/T02-RESULT.md`。 |
| **T03 物理视口账本** | PARTIAL | W170 同 hash 双绿：W170 80/120 `44/0`、W171 80/120 `42/0`、W175 `131/0`+`10/0`。但关闭 `/help`/`/jobs` 后有 composer 下方空白带，已单列 `W170-BLANKBAND`。W167 Lead PTY 80 仅 `72/3/1`：model picker、model selection、steer export 红；120 待跑。 | RC `4abdb8e3…`；W170 patch `aa4bb24f…`；W167 driver `aed800df…` | 不得第二个渲染 writer；空白带不得混进 W170 收口 | 下一步：`W170-BLANKBAND` 专项；W167 80 三红；W167 120；再 W168/W164/full。 |
| **T04 W167 序列诊断** | DONE | 只读诊断完成：inline 重复归渲染账本；resume 红为驱动断言错误（等被中断的 `LONG_DONE`，实际 `INTERRUPT_OK` 可见）。 | T00 DONE | R3 可与 R1/R2 并行 | 证据 `$G/diag167/RESULT.md`（382 行，26 项自检 EXIT=0）；基础驱动已改 `NOW_ANSWER`，但 originalId/journal 直证仍待 R3。 |
| **T05 W170 浮层诊断** | BLOCKED | 只读 `ModelPicker.tsx`、相关 `Chat.tsx` 调用和现有帧证据；可在 `$G/diag170/` 写帧级 repro，不改生产文件。 | T00；需要新 PTY 时向 Lead 排队 | 与 T01/T02/T03/T04 并行诊断 | 对“深帧第4轮整块不出现”和“关闭后 Reasoning effort 残留”分别定位到 tail-append、绝对浮层、行身份或账本；给最小帧与建议改动面。产出 `$G/diag170/{RESULT.md,repro.*,logs/**}`。 |
| **T06 RC 集成与静态门** | BLOCKED | Lead 将 T03 patch 单独落到 `$RC`，核对只含认领文件；按 T02 `COMMANDS.md` 编译、typecheck、W175、replay、渲染聚焦回归。 | T02、T03 | 无写并行；T04/T05 可继续只读分析 | patch reverse-check 可还原；源/lib/package hash 固定；所有确定性门全绿后才允许消耗真实 PTY。产出 `$G/rc-static/{RESULT.md,summary.json,logs/**}`。 |
| **T07 定向 PTY 冒烟** | BLOCKED | Lead 独占 PTY 锁，按 §4 顺序先跑 W171 两尺寸各 1 次，再跑 W170 120、W167 120；任一核心行完整性红即停止后续队列。 | T06 | 不与其他 PTY/compile 并行 | 原始日志、raw ANSI、scrollback、result.json 齐全；W171 任一红回 T03；仅浮层/输入红才进入 T08/T09。产出 `$G/pty-smoke/**`。 |
| **T08 W167 条件修复** | BLOCKED | 仅当 T07+T04 证明存在非账本缺陷时派发；优先只改 `PromptInput.tsx` 及测试。若必须改 `Chat.tsx`，该文件归本任务唯一 owner，T09 不得同时改。 | T04、T07 | 与 T09 仅在写文件完全不重叠时并行 | Ctrl+Enter interrupt+followup、export、resume 全部 `FAIL=0`；不得恢复旧 `interruptAndDeliver` 语义。产出 `$G/fix167/{fix.patch,RESULT.md,logs/**}`。 |
| **T09 W170 条件修复** | BLOCKED | 仅当 T07+T05 证明存在非账本缺陷时派发；优先只改 `ModelPicker.tsx` 及测试。需要 `Chat.tsx` 或 `log-update.ts` 时停止并行，交 Lead 排序。 | T05、T07 | 与 T08 仅在写文件完全不重叠时并行 | 深帧第4轮面板出现、关闭无残留；W170 两尺寸各 `44/0`；不得裁切/隐藏内容。产出 `$G/fix170/{fix.patch,RESULT.md,logs/**}`。 |
| **T10 全定向矩阵** | BLOCKED | Lead 串行集成 T08/T09（若存在），重新静态门并冻结 RC；随后按 §4 跑完整 targeted，不再边测边改。 | T07；T08/T09 若触发则必须先集成复验 | 非 PTY 静态审查可并行；PTY/compile 不并行 | W171 两尺寸各连续 3 次 `42/0`；W167/W168/W170/W171/W175/W164 全部达到 §5；任一项不稳定即未通过。产出 `$G/targeted-final/{RESULT.md,summary.json,logs/**}`。 |
| **T11 冻结 full** | BLOCKED | 从 `$U` 调用 W163 full，`--source $RC --go-source $H --jobs 1`；运行期间禁止任何源写、compile、其他 PTY，包含 `$W163H` runner/drivers 写入。 | T10 全绿 | 只允许只读文档审查 | §6 所有值全绿；`sourceDriftAfterRun=false`、`goSourceDriftAfterRun=false`、Lead 自算 `w163HarnessDriftAfterRun=false`、`GO_ALLOWED=1`。产出 `$E/{summary.json,RESULT.md,fixed-snapshot.json,driver-manifest.json,logs/**}`。 |
| **T12 独立封版审核** | BLOCKED | 非实现 owner 审核 patch 面、行账本不变量、测试未放宽、正式 backend 路径、hash 链、full 证据与进程卫生；只写 `$G/release-audit/`。 | T10 后可审代码；T11 后审完整证据 | 代码审查可与 T11 并行，结论须等 T11 | 明确 `GO` 或 `NO-GO`；列出审了哪些文件/命令/证据和全部未验项；有任何实质遗留即 NO-GO。产出 `$G/release-audit/RESULT.md`。 |
| **T13 发布候选构建** | BLOCKED | 在与 T11 完全相同的源码/hash 上构建 `0.2.2-go.1` 与正式 TUI tarball；不得重新挑选补丁。 | T11、T12=GO | 可与文档核对并行，不与 PTY 并行 | `bin-check` 通过；两个 Windows 产物版本信息正确；tarball/backend.js/dshd 与 T11 hash 一致；稳定/测试渠道不混。产出 `$G/release/{SHA256SUMS,bin-check.log,version-info.log,package.json}`。 |
| **T14 发布包真实终验** | BLOCKED | 用 T13 的正式 dshd + 正式 tarball 内的 `lib/types/backends/dsh-go/**`（其源只认 `src/backends/dsh-go/**`）+ 新临时 home 跑真实 ConPTY 启动、对话、text/reasoning/tool、resize、resume/restart、export、quit。 | T13 | 只读审核可并行；第二个 PTY 不可并行 | 全部 `FAIL=0 UNVERIFIED=0`，进程树清理完成；结果与 T13 hash 对齐。产出 `$G/release-smoke/{RESULT.md,summary.json,logs/**}`。 |
| **T15 封版交接** | BLOCKED | Lead 汇总最终 rev/hash、证据、CI 状态和未验项，更新 `PLAN.md` W163/W200、`MAINTENANCE.md`、本任务表。 | T14；稳定版还须 CI 全绿 | 无 | 明确封版或保留 NO-GO；只本地交接，不 push、不重启。任何后续 push 另取用户当次确认。产出 `$G/GO1-HANDOFF.md`。 |

## 2. T03 必须冻结的账本契约

| 编号 | 不变量 | 必测证据 |
|---|---|---|
| L1 | 每个物理行最多映射一个持久 row id；同一 durable row 在 viewport+scrollback 合计恰好一次。 | W171 user/assistant/tool/JobCard 唯一性；W175 `ROW_02` 不重复。 |
| L2 | 对齐本身不得创造终端从未接收的物理行；稳定/收缩帧的物理高度不得超过逻辑高度。增长帧若保留旧行，必须逐行对应真实的终端驻留/输出证据，不能用全局高度闸换丢行。 | W199 `n=134 ph=41→45 / logical=38→38` 回放；W175 `W175_REF_U2` 必须保留。 |
| L3 | 重绘切片始终贴底；完成后停泊光标固定在视口最后一行。 | W199 frame 118、viewport=10、strandedPad=3；绝对/相对寻址混用回放。 |
| L4 | scrollback 只按真实终端输出造成的滚动推进，不按逻辑帧高度差伪造 LF。 | 对 raw ANSI 逐 escape 回放，核对屏幕与 scrollback 两侧行数。 |
| L5 | 空白差分不得擦除仍然存活的 durable 行；只有明确删除、替换或对应浮层 epoch 关闭才允许清除。 | W199 `t:6:0`、`t:8:0` 由有字变空回放。 |
| L6 | 浮层行与 durable transcript 行分账；保留旧 transcript 不得保留已关闭浮层像素。 | W170 打开/深帧/关闭；`Reasoning effort` 无残留。 |
| L7 | resume/restart/resize 只改变投影，不复制 durable 身份；A/B 会话互不串行。 | W167/W171 before/after resume/restart 与动态 resize。 |

实现说明必须先定义 `logical row id → physical row → terminal scroll state` 的单向账本，再写代码；不得继续用高度闸、keepPixels 或单场景 if 交换分数。

## 3. Lead 每轮推进表

| Lead 轮次 | 进入条件 | 本轮同轮派发/执行 | Lead 在等待期做什么 | 出门与下一轮 |
|---|---|---|---|---|
| **R0 接管** | 用户启动 GO.1 收口 | 主责 T00；只读盘点 W200 最新日志、进程与文件 hash | 建 `dispatch.md`、锁文件和任务书，不跑 full | T00 完成→R1；W200 仍活跃则原 owner 直接承接 T03，绝不另起渲染写者 |
| **R1 并行准备** | T00 DONE | T02、T04、T05 同轮派发；现有 W200 作为 T03 继续；T01 等其原子检查点再封存 | 准备 RC 集成清单、核对 W163 canonical drivers、W175 独立入口与 dshd hash | T03 当前实验交付且 T01/T02 完成→R2；诊断任务不阻塞核心实现 |
| **R2 核心迭代** | 基线与 oracle 可用 | T03 唯一 lane 施工（优先原 W200 owner）；T04/T05 继续；Lead 不并行改渲染文件 | 审核诊断、准备条件任务书；每次交付先跑确定性门 | 确定性门全绿→T06→R3；任何 W175 交换回归留在 R2，不进 RC |
| **R3 真实冒烟与分诊** | T06 DONE | Lead 串行 T07；无其他 PTY/compile | 整理下一 RC hash、按输出分诊，不凭代理自述 | W171/W175 红→R2；仅 W167/W170 独立缺陷→R4；全部冒烟绿→R5 |
| **R4 条件修复** | T07 已分诊 | 文件不重叠时同轮派 T08、T09；否则按 `log-update/Chat`→`ModelPicker`→`PromptInput` 顺序串行集成 | 对每个 patch 单独做 reverse-check、静态门和最小复现 | 条件修复集成后回 R3 快速冒烟；不得直接 full |
| **R5 全定向** | 所有已知产品缺陷已修或冒烟已绿 | Lead 执行 T10；独立 reviewer 可开始 T12 代码审查 | 只监控单 PTY 队列和证据完整，不改源码 | §5 全绿→R6；核心红→R2，浮层/输入红→R4 |
| **R6 冻结 full** | T10 DONE | Lead 执行 T11；reviewer 完成 T12 | full 期间只做只读审查；记录无写锁、无漂移 | full 全绿且 reviewer=GO→R7；否则按 §7 回退，不构建发布包 |
| **R7 发布与交接** | T11+T12 GO | 串行 T13→T14；随后 T15 | 核 hash、版本、进程卫生、CI 与 HANDOFF | 全部通过才宣布 GO.1 封版；不自动 push、不重启 |

## 4. 单 PTY 队列与快速失败顺序

T02 必须把 targeted 做成 `$W163H/run_final.py` 的受支持模式，默认只选 W164/W167/W168/W170/W171，并提供 `--only <逗号任务名>` 与 `--w171-repeat <1|3>`；每次 repeat 都生成新任务名、run id、home 和证据目录，但复用同一次冻结构建。禁止再从 W200 `final-suite.sh` 派生第二套运行链，禁止从旧 `LIVE` 借 `lib/`。

```bash
cd "D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src"
# 快速冒烟：只跑已知红任务，W171 两尺寸 + W170/W167 120×36
python -B tmp/W163/run_final.py --mode targeted --jobs 1 \
  --source "$RC" --go-source "$H" --run-id "<fresh-smoke>" \
  --only w171-80x24-final,w171-120x36-final,w170-120x36-final,w167-120x36-resize-final \
  --w171-repeat 1

# 全定向：同一冻结 formal build 中，W171 两尺寸各串行 3 次，其余 GO.1 定向一次
python -B tmp/W163/run_final.py --mode targeted --jobs 1 \
  --source "$RC" --go-source "$H" --run-id "<fresh-targeted>" --w171-repeat 3
```

W200 自定义驱动只作差异证据：T02 必须逐项审计其与 canonical drivers 的差异，把有效的新断言/取证回移到 `$W163H/drivers/**`；旧 W152/W163 runtime 绝对路径只能由 `materialize_drivers` 统一替换，W196 `write-probe` 默认不得进入正式 targeted/full。W175 不在 W163 full 内：静态 frames/row 与真实双尺寸 W175 都由 T02 在独立证据目录另跑，真实被测根使用含 src+lib 的 `build-w163-final`，不是只含 lib 的 npm 解包目录。T10 与 T11 必须核对 renderer/backend/dshd hash 同源。

| 阶段 | 串行顺序 | 通过条件 | 失败后的动作 |
|---|---|---|---|
| 静态 | typecheck→compile→replay→W175 frames→W175 row-protocol→渲染聚焦回归 | 全部 exit 0，W175=`131/0`、`10/0` | 回 T03；不启动 PTY |
| 冒烟 1 | W171 80×24 一次→W171 120×36 一次 | 均 `42/0` | 任一红立即停止，回 T03 |
| 冒烟 2 | W170 120×36→W167 120×36 | 无浮层/输入/行完整性红 | 按 T04/T05 分诊；核心红仍回 T03 |
| 稳定性 | W171 80×24 再跑 2 次→W171 120×36 再跑 2 次 | 连同冒烟合计各连续 3 次 `42/0` | 一次红即不稳定；不得取最好一次或平均 |
| 全定向 | W170 80/120→W167 80/120→W168 minimal/80/120→W164→W175 80/120→两个 post-hoc audit | §5 全绿 | 只修失败所属层，修后重跑整个受影响矩阵 |

每次运行必须保存：`SUMMARY`、退出码、`result.json`、`scrollback-*.txt`、`conpty.raw.log`、使用中的 `src/lib` hash、dshd hash、home/run id。看门狗超时与源/lib 漂移会使该次证据无效，不算通过，也不直接算产品断言。

## 5. T10 定向封版门

| 套件 | 必须达到 |
|---|---|
| W171 80×24 | 连续 3 次 `OK=42 FAIL=0 UNVERIFIED=0` |
| W171 120×36 | 连续 3 次 `OK=42 FAIL=0 UNVERIFIED=0` |
| W175 frames / row-protocol | `131/0` 与 `10/0`；无 `ROW_02` 重复、无 `W175_REF_U2` 丢失 |
| W175 真实 80×24 / 120×36 + 两个 post-hoc audit | `run.sh` 全部 exit 0、必需 `UNVERIFIED=0`；逐帧角色无丢失/复制，tool call/result 配对，journal/v4 身份一致；仅 README 明示的 80 可选 resize 可例外 |
| W170 80×24 / 120×36 | 各 `44/0`；深帧面板可见，关闭无 `Reasoning effort` 残留 |
| W167 80×24 / 120×36 | 最终均 `FAIL=0 UNVERIFIED=0`；**不得把 `75/1` 当封版通过**；若原断言错误，须先用 raw/事件证据证明并修正驱动 |
| W168 minimal / 80×24 / 120×36 | `8/8`、`33/0`、`33/0` |
| W164 corrected conformance | `120/0`，defer `8/8` |
| 构建与聚焦回归 | typecheck、compile、边界、渲染相关回归均 exit 0；源码与 lib/package hash 在跑前跑后不变；W163/W175 runner、drivers、oracle hash 在跑前跑后不变 |

## 6. T11 full 封版门

```bash
cd "D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src"
python -B tmp/W163/run_final.py --mode full \
  --source "$RC" \
  --go-source "D:/DeepSeek Harness/default-workspace/DSH-GO" \
  --run-id "<fresh-go1-run-id>" --jobs 1
```

| 字段 | 必须值 |
|---|---|
| `taskFailures` / `assertionFailures` / `hygieneFailures` | `0 / 0 / 0` |
| `UNVERIFIED` | `0` |
| `sourceDriftAfterRun` / `goSourceDriftAfterRun` | `false / false` |
| `w163HarnessDriftAfterRun` | `false`；Lead 对 `run_final.py`、config、`drivers/**` 跑前/跑后自行计算，不能只依赖 runner 的 source drift |
| `GO_ALLOWED` | `1` |
| W152_COVERAGE | `OK=769 FAIL=0 UNVERIFIED=0` |
| AgentSession | `OK=29 FAIL=0 UNVERIFIED=0` |
| 64 命令矩阵 | `OK=64 FAIL=0` |

旧 full `w163-final-20261106-2302` 仅作红点基线，不能回填到新 hash。

## 7. 失败路由表

| 现象 | 唯一返回任务 | Lead 动作 |
|---|---|---|
| W171 丢行/重复、W175 frames/row 任一回归 | T03 | 保存 raw 与最小回放；拒绝局部交换补丁；RC 保持在独立副本，不污染共享树 |
| W170 仅深帧缺失或关闭残留，且 W171/W175 全绿 | T09 | 限定浮层文件；若需改账本则回到 T03，不并行抢写 |
| W167 仅 Ctrl+Enter/export/resume 状态红，且行完整性全绿 | T08 | 按 T04 的最小事件序列修；若又是行重复/丢失则回 T03 |
| W168 或 W164 在相同 RC 重新变红 | 新建 scoped backend/export 任务 | 只改 `src/backends/dsh-go/**` 与对应测试；不得同时改 `src/dsh-go/**` 镜像目录 |
| source/lib/package/dshd 漂移、并发编译污染 | 当前证据作废 | 隔离 RUN，保留日志；重新冻结后用新 run id，不删旧证据 |
| PTY 超时、第二个 PTY、非自有进程混入 | 当前运行无效 | 只清理本轮 PID 树；修队列/锁后再跑，不把超时算绿 |
| 同一核心修复改善一组但使另一组变红 | T03 | 整项拒绝，不“择优”集成；回到账本契约与最小帧 |
| full 任一红 | 对应 scoped 任务 | 不进入 T13；修复后从 T06 静态门开始，不可只重跑 full 碰运气 |

## 8. 派单完成回报模板

```text
ID / owner / model：
base rev 或 source hash：
改动文件（逐文件）：
patch 路径与 sha256：
先红命令、退出码、原始 FAIL：
后绿命令、退出码、原始 OK=n FAIL=m UNVERIFIED=x：
src/lib/package/dshd hash（跑前/跑后）：
证据目录：
未验项：
自有进程与孤儿清理结果：
```

## 9. T03 只读审查专案（R1–R3）

> 审查报告：`$G/review/T03-READONLY-REVIEW.md`；判定 B（部分根因），当前 T03 未完成。

| ID | owner / 写作用域 | 前置 | 红测与实施 | 验收 |
|---|---|---|---|---|
| **R1 · ID-ALIGN** | T03 owner；`src/ink/log-update.ts`，必要时 `frame.ts`/`row-identity.ts` | T03 当前快照 | `Frame.rowIds` 进入旧/新物理行映射；删除 provisional assistant 后不得覆盖存活 `W175_REF_U2`；新增“同下标不同 durable id”反例先红后绿 | W175 row `10/0`、frames `131/0` |
| **R2 · PARK-LEDGER** | 同一 T03 owner；仅 `log-update.ts` 与专用测试 | R1 同 lane | 固化 F02 `prevH=30,H=29,V=24,viewportY=7`；重绘后 cursor 必须 `V-1`；账本只按真实输出滚动推进，或给逐操作等价证明 | 几何反例全绿；frames `131/0`、row `10/0`；typecheck/compile=0 |
| **R3 · W167-RESUME-ORACLE** | 独立驱动 owner；`$G/oracle/drivers/w167-inline.mjs` | 无，可与 R1/R2 并行 | 不再等被中断的 `LONG_DONE`；用已完成 `INTERRUPT_OK` 并直证 original session id；保留 inline 重复断言 | remount=OK、inline duplicate=FAIL；后续真实 W167 resume 验证原会话 |

停止条件：frames 从 `131/0` 回落、row 仍 `9/1`、W171 任一红、T03 hash 改变未重跑 R4，均不得进 full。
