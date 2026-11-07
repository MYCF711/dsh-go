# GO.1 · T03 只读审查任务书（给更强模型）

> 生成时间：2026-11-07 04:45（Asia/Shanghai）
> 审查对象：T03 物理视口账本重构
> 模式：**只读审查 / 根因裁决 / 出专案**
> 结论目标：回答“T03 是否就是根因；做完 T03 后是否只剩验证；如果不是，独立根因是什么”

---

## 0. 一句话任务

在**不修改任何产品源码、不启动 PTY、不编译、不打包**的前提下，审查 T03 当前 `log-update.ts` 重构是否足以同时关闭：

- W171 80×24 `41/1`
- W171 120×36 `40/2`
- W175 row-protocol `9/1`
- W167 80/120 inline 重复

并输出一份可直接交给 Lead 执行的任务专案：

- 若 T03 是根因：给出“T03 收口 → 冻结 full → 发布”的执行表；
- 若 T03 只是部分根因：列出独立缺陷、最小文件:line、建议 owner、红测与验收；
- 若 T03 不是根因：明确否决当前方向，给替代账本/替代实现路径。

---

## 1. 固定路径与当前快照

| 变量 | 路径 | 说明 |
|---|---|---|
| `G` | `C:/Users/Administrator/AppData/Local/Temp/GO1-20261107-0410` | 本轮独占工作根 |
| `BASE` | `$G/base` | T01 封存的 W200 最终只读基线，9126 文件 |
| `RC` | `$G/rc` | T03 唯一集成候选树；**审查期间可能仍被 T03 写** |
| `ORACLE` | `$G/oracle` | T02 单一 targeted 入口 |
| `LIVE` | `C:/Users/Administrator/AppData/Local/Temp/dsh-TUI-tool-rows-w182` | W200 原始现场，只读，不碰 |
| `DSHD` | `C:/Users/Administrator/AppData/Local/Temp/dsh-TUI-tool-rows-w182/tmp/W172/bin/dshd.exe` | 真实宿主 |
| `REVIEW` | `$G/review` | **唯一可写目录**，输出审查报告 |

### 1.1 当前源码快照（审查开始前必须重算）

| 文件 | 生成时 sha256 |
|---|---|
| `RC/src/ink/log-update.ts` | `6513e2cebc720b32c6d4fd3d4d659c0b3fcc5b9072a8c8be49c6c185d5f03509` |
| `RC/src/ink/frame.ts` | `912e6a49b3fd1db5b1fa6506a872cc362370e4bc4e07951934ac414b289e838b` |
| `RC/src/ink/row-identity.ts` | `412a003214e29af08733ea8df8cfa4a86a83c9b9cc0f134339b8dd1ce7037970` |
| `RC/src/ink/ink.tsx` | `560053ee3c9d468887e77f8baf9e71fc3873d0c11c8392106eb8cbc271a7e46a` |
| `dshd.exe` | `5c7b1105c0dd18c75775636d83a06626514c04e28c9a0048f493f284210500d2` |

> T03 仍在施工，以上哈希可能在审查期间变化。**先复制一份只读审查快照，再记录哈希；报告必须写明快照哈希。**

建议：

```bash
export G="C:/Users/Administrator/AppData/Local/Temp/GO1-20261107-0410"
mkdir -p "$G/review/snapshot"
cp -a "$G/rc/src" "$G/review/snapshot/src"
cp -a "$G/rc/lib" "$G/review/snapshot/lib"
cp "$G/rc/package.json" "$G/review/snapshot/package.json"
cp "$G/rc/tsconfig.json" "$G/review/snapshot/tsconfig.json"
sha256sum "$G/rc/src/ink/log-update.ts" "$G/rc/src/ink/frame.ts" "$G/rc/src/ink/row-identity.ts" "$G/rc/src/ink/ink.tsx" > "$G/review/source-hashes.txt"
```

---

## 2. 已确认的当前基线

### 2.1 T01/T02 已 DONE

- T01：BASE 封存，sourceTree `a5bfce72…`，9126 文件，W193–W200 共 8 个补丁归档
- T02：`$G/oracle/run-targeted.sh` 单 PTY 串行入口

### 2.2 T02 基线（W200 最终树）

| 组 | 结果 |
|---|---|
| W170 80×24 | `OK=44 FAIL=0` |
| W170 120×36 | `OK=44 FAIL=0` |
| W171 80×24 | `OK=41 FAIL=1`（after-resume user.multiline=2） |
| W171 120×36 | `OK=40 FAIL=2`（before-resume user/job=2；after-restart-new-prompt assistant.multiline=2） |
| W167 80×24 | `OK=74 FAIL=2`（inline 多行 user 重复；resume remounted） |
| W167 120×36 | `OK=74 FAIL=2`（同上） |
| W175 frames | `OK=130 FAIL=1`（`shrink-anchoredPad-viewportY/reanchor-stable-11` frame 118，ROW_02 两次） |
| W175 row-protocol | `OK=9 FAIL=1`（`W175_REF_U2=0`） |

### 2.3 T03 当前中间结果

- W175 frames：`OK=131 FAIL=0`（已从 130/1 到 131/0）
- W175 row-protocol：仍 `OK=9 FAIL=1`
- W171 双尺寸：尚未出最终 `SUMMARY`
- T03 当前 `log-update.ts` 已重写，但尚未交付 `T03-RESULT.md`

> 不能把 `131/0` 当 T03 完成；row 仍红，W171 未跑完。

---

## 3. 审查必须回答的问题

### Q1. T03 是否就是根因？

按证据给出四选一：

- **A. 是唯一根因**：T03 收口后，W171 双尺寸、W175 row、W167 inline 重复都应同时绿；
- **B. 部分根因**：T03 修掉 W171/W175，但 W167 resume 是独立 remount/驱动口径；
- **C. 不是根因**：T03 对 W171/W175 无效，真正根因在 Chat/PromptInput/queue/floater/row identity；
- **D. 无法判定**：现有证据不足，列出所需最小证据。

### Q2. 若 T03 不是唯一根因，独立缺陷是什么？

每个独立缺陷必须给：

- 现象（哪个组、哪条断言、原始失败行）
- 最小事件序列（raw ANSI 帧号/事件序号）
- 文件:line（精确到函数）
- 归属层：渲染账本 / row identity / 输入状态机 / queue/floater / resume remount / 驱动口径 / 其他
- 建议 owner 与写作用域
- 最小红测与验收命令

### Q3. T03 当前 patch 是否引入了回归？

至少核对：

- W170 80/120 是否仍 `44/0`
- W168 80/120 是否仍 `33/0`
- W164 是否仍 `120/0`
- W175 frames 是否 `131/0`
- W175 row 是否 `10/0`（当前 9/1，若不能到 10/0，说明 T03 未完成）
- 是否篡改/放宽断言、golden、唯一性
- 是否产生 source/lib drift

### Q4. 是否存在“交换回归”？

已知历史交换：

- 高度闸：W170 到 `44/0`，W175 row 掉 `9/1`
- keepPixels：W171 到 `41/1`，W170 回退 `42/2`
- 纯官方：W175 `122/9`、row `9/1`

审查必须判断 T03 是否又用某组绿换另一组红；若是，直接判 NO-GO。

---

## 4. 必读文件与证据

### 4.1 任务/报告

- `$G/T01-RESULT.md`
- `$G/T02-RESULT.md`
- `$G/dispatch.md`
- `$G/baseline-manifest.json`
- `$G/artifacts/patches/w193…w200*.patch`
- `$G/oracle/COMMANDS.md`
- `docs/go1-problem-report-2026-11-07.md`
- `docs/go1-lead-execution-task-table-2026-11-07.md`（只读；可能被其他 owner 改写）

### 4.2 T03 证据

- `$G/oracle/evidence/t03-baseline-frames.log`
- `$G/oracle/evidence/t03-ledger1-frames.log`
- `$G/oracle/evidence/t03-row*-row-protocol.json`
- `$G/oracle/evidence/t03-dump-row-protocol.json`
- `$G/oracle/w171/**/logs/result.json`、`scrollback-*.txt`、`conpty.raw.log`
- `$G/oracle/logs/w171-*.log`、`w167-*.log`、`w170-*.log`
- `$G/oracle/w167/runs/**/logs/`（W167 raw/scrollback）

### 4.3 W167 只读诊断

- `$G/diag167/RESULT.md`（若已生成）
- `$G/diag167/bisect-*.txt`
- `$G/diag167/replay-*.mjs`
- `$G/diag167/viewport-*.txt`

### 4.4 T03 源码

- `RC/src/ink/log-update.ts`
- `RC/src/ink/frame.ts`
- `RC/src/ink/row-identity.ts`
- `RC/src/ink/ink.tsx`
- 对应测试（若 T03 新增）

### 4.5 官方基线

- `D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src/src/ink/log-update.ts`
  - sha256 `c1b3ab31d004cb70d0c4b3a7b21cec8e0610461d0cd7ff9c3353e64f66ce12c0`
  - 1248 行
  - commit `1bd7cb515b4c6fb52f049f7bde521df48ee2f54b`

---

## 5. 只读方法

只允许：

1. 读取源码、日志、result.json、raw ANSI、scrollback。
2. 用 `diff`/`git diff --no-index` 对比 `BASE/src` 与 `RC/src`。
3. 用现有 replay 脚本做**只读回放**，不写产品源码；输出只写 `$G/review/**`。
4. 静态检查函数调用链、状态机、row id 分配、viewport 账本。
5. 读取 W167 诊断输出，不重跑 PTY。

禁止：

- 启动 PTY / ConPTY
- `pnpm compile` / `npm pack` / `go build`
- 修改 `RC`、`BASE`、`LIVE` 任何文件
- 修改断言、golden、唯一性
- 删除/隐藏 tool/JobCard 内容、blank-spacer
- 并发 compile/PTY
- 推远端、重启 DSH/magpie

> 如果必须跑测试才能判定，只允许在 `$G/review/repro` 的独立副本里跑，且必须先报告 Lead；默认不跑。

---

## 6. 必交报告结构

输出：`$G/review/T03-READONLY-REVIEW.md`

必须包含：

### 6.1 结论摘要（一页内）

- T03 根因裁决：A/B/C/D
- 是否可进入“只剩验证”：是/否
- 若否，独立缺陷数量
- 建议下一步（最多 3 条）

### 6.2 快照与哈希

- 审查时 `RC` 四文件 sha256
- 审查快照路径
- 审查窗口内是否检测到 T03 继续写入（hash 变化）

### 6.3 证据链

逐条红：

| 组 | 断言 | 原始失败 | 根因层 | 文件:line | 证据路径 |
|---|---|---|---|---|---|

### 6.4 T03 patch 审查

- 改了哪些函数/状态机
- 是否满足 L1–L7
- 是否引入交换回归
- 是否删/放宽断言
- 是否还有未覆盖场景

### 6.5 独立缺陷专案

每个独立缺陷给：

- ID（T03-A/B/C）
- 目标
- 写作用域（精确文件）
- 依赖
- 红测
- 验收命令/期望值
- 风险与禁止项
- 预计验证顺序

### 6.6 封版路径

- 若 T03 绿：T03 收口 → 静态门 → 冻结 full → T12 审计 → T13 发布
- 若 T03 部分绿：先 T03，再 T08/T09，重跑
- 若 T03 不绿：否决当前 patch，给替代实现

### 6.7 未验项

必须逐条列出，禁止“应该”“大概”。

---

## 7. 判定标准

T03 通过（可进入下一阶段）必须同时满足：

- W171 80×24 连续 3 次 `OK=42 FAIL=0 UNVERIFIED=0`
- W171 120×36 连续 3 次 `OK=42 FAIL=0 UNVERIFIED=0`
- W175 frames `OK=131 FAIL=0`
- W175 row-protocol `OK=10 FAIL=0`
- W170 80/120 `OK=44 FAIL=0`
- W167 80/120 只剩已证独立 resume 红，或全绿
- typecheck/compile exit 0
- 跑前跑后 source/lib hash 固定
- 无并发 PTY/compile、无 source drift

任一条不满足：T03 未完成，不得进 full。

---

## 8. 给审查模型的最终指令

```text
你是 GO.1 的只读审查 owner，不是实现 owner。
先复制 RC/src、RC/lib、package.json 到 $G/review/snapshot 并记录 hash。
只读审查 T03 的 log-update/frame/row-identity/ink.tsx 改动，
对照 T02 基线、W167 诊断、W175 frames/row、W171 raw/scrollback。
不启动 PTY、不 compile、不修改任何产品文件。
输出 $G/review/T03-READONLY-REVIEW.md：
1) T03 是唯一根因 / 部分根因 / 不是根因 / 无法判定；
2) 若部分或不是，列出独立缺陷和最小文件:line；
3) 给出可执行专案（任务 ID、写作用域、依赖、红测、验收、禁止项）；
4) 明确 GO/NO-GO，不得把中间绿当封版。
```

---

## 9. 当前结论（给 Lead 的短版）

- T03 不是已证实唯一根因，只是最高概率候选。
- W175 frames 已绿 `131/0`；row 仍 `9/1`；W171 双尺寸未出最终绿。
- W167 inline 重复可能同账本；resume 红可能是独立 remount/驱动口径。
- 审查报告出来前，不并行开第二个产品 writer、不跑 full、不封 GO.1。
