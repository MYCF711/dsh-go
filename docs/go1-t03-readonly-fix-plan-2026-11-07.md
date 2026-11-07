# GO.1 · T03 后只读修复方案（待执行）

> 生成时间：2026-11-07 04:55（Asia/Shanghai）
> 状态：**只读方案，未应用任何产品修改**
> 前置：T03 停笔并交付 `T03-RESULT.md`
> 原则：T03 仍独占 `RC/src/ink/log-update.ts`；本方案不另开产品 writer，只在 T03 收口后按序执行

---

## 0. 当前事实

| 项 | 当前值 |
|---|---|
| T03 owner | `e06b5a55`，IN_PROGRESS |
| `RC/src/ink/log-update.ts` | `56fcf2b8d1323be6c7bddcbab9b83488cff02d542b359f87ff371c92c617e6b5` |
| W175 frames | `131/0` ✅ |
| W175 row-protocol | `9/1` ❌ |
| W171 80×24 | 未出最终绿；基线 `41/1` |
| W171 120×36 | 未出最终绿；基线 `40/2` |
| W167 80/120 | 基线 `74/2` |
| W170 80/120 | `44/0` ✅ |
| T03-RESULT | 未交付 |
| GO.1 | **NO-GO** |

W167 只读诊断已完成：

- `inline-scrollback-no-duplicate-or-missing-turns` = **渲染账本**
- `resume-original-session-remounted` = **驱动口径错误**，已修 `$G/oracle/drivers/w167-inline.mjs:484`

---

## 1. 两条修复，不要混在一起

### Fix A · W167 驱动口径（已应用，非产品）

文件：`$G/oracle/drivers/w167-inline.mjs`

原：

```js
const resumed = await until(() => screen().includes(LONG_ANSWER) && screen().includes('❯'), 30_000)
check('resume-original-session-remounted', resumed, `expected=${originalId} visibleLatest=${LONG_ANSWER}`)
```

改为：

```js
const resumed = await until(() => screen().includes(NOW_ANSWER) && screen().includes('❯'), 30_000)
check('resume-original-session-remounted', resumed, `expectedSession=${originalId} nowVisible=${screen().includes(NOW_ANSWER)} longVisible=${screen().includes(LONG_ANSWER)}`)
```

理由（W167 诊断已证）：

- `LONG_DONE` 全缓冲 0 次，raw 80 只有 1 次 reasoning 文本，120 是 0
- 脚本自己在 `:444-446` 已用 `longTurnFinished=false` 通过 ctrl-enter 断言
- resume 实际成功：原 sessionId 重挂、`INTERRUPT_OK` 可见、`userFrames=8->8`

**这条改完，W167 应只剩 inline 账本 1 条红，不是 2 条。**

---

### Fix B · inline 账本（T03 主路径，不另开 writer）

W167 诊断结论：

- 内容层只有 1 条多行 user 帧
- 重复只发生在终端行：80 两份、120 四份
- 第一份是残缺的，120 中间夹进上一轮 `│ ≡ …JOB_OUTPUT`
- 最小事件序列：两次 `ESC[H` 全量重绘，无 `ESC[J`
- BASE 锚点：
  - `base/src/ink/log-update.ts:467-475`：用公式推算 scrollback 行数
  - `base/src/ink/log-update.ts:571-580`：用该值跳过“已进 scrollback”的行
  - `base/src/ink/log-update.ts:453-459`：注释自己承认会 1 行偏移
- W175 frame 118 的 `ROW_02` 两次是同源旁证

**T03 已经重写这一层。Fix B 不是另写一份，而是 T03 交付后的验收与兜底方案。**

---

## 2. T03 交付后的第一轮验收（只跑一次）

### 2.1 冻结检查

```bash
export G="C:/Users/Administrator/AppData/Local/Temp/GO1-20261107-0410"
export RC="$G/rc"
export ORACLE="$G/oracle"
export DSHD="C:/Users/Administrator/AppData/Local/Temp/dsh-TUI-tool-rows-w182/tmp/W172/bin/dshd.exe"

sha256sum "$RC/src/ink/log-update.ts" "$RC/src/ink/frame.ts" "$RC/src/ink/row-identity.ts" "$RC/src/ink/ink.tsx"
```

记录到 `$G/review/t03-post-hashes.txt`；若 T03 仍在写，停止，不跑。

### 2.2 确定性门

```bash
cd "$RC"
WV175_IMPL_ROOT="$RC/src" WV175_OUT="$ORACLE/evidence" WV175_LABEL=t03-post \
  timeout 285 node --import tsx/esm "$ORACLE/W175/frames.mjs"

WV175_IMPL_ROOT="$RC/src" WV175_OUT="$ORACLE/evidence" WV175_LABEL=t03-post \
  timeout 285 node --import tsx/esm "$ORACLE/W175/row-protocol.tsx"
```

期望：

- frames `OK=131 FAIL=0`
- row `OK=10 FAIL=0`

任一红：不启动 PTY，回 T03。

### 2.3 单 PTY 串行

```bash
cd "$RC"
export GO1_RC="$RC" GO1_ORACLE_ROOT="$ORACLE" GO1_DSHD="$DSHD"

W171_RUN=t03-post-80 W171_COLS=80 W171_ROWS=24 \
  timeout 285 node "$ORACLE/drivers/w171-inline.mjs" > "$ORACLE/logs/t03-post-171-80.log" 2>&1
W171_RUN=t03-post-120 W171_COLS=120 W171_ROWS=36 \
  timeout 285 node "$ORACLE/drivers/w171-inline.mjs" > "$ORACLE/logs/t03-post-171-120.log" 2>&1

W167_RUN=t03-post-80 W167_COLS=80 W167_ROWS=24 \
  timeout 285 node "$ORACLE/drivers/w167-inline.mjs" > "$ORACLE/logs/t03-post-167-80.log" 2>&1
W167_RUN=t03-post-120 W167_COLS=120 W167_ROWS=36 \
  timeout 285 node "$ORACLE/drivers/w167-inline.mjs" > "$ORACLE/logs/t03-post-167-120.log" 2>&1

W170_RUN=t03-post-80 W170_COLS=80 W170_ROWS=24 \
  timeout 285 node "$ORACLE/drivers/w170-direct.mjs" > "$ORACLE/logs/t03-post-170-80.log" 2>&1
W170_RUN=t03-post-120 W170_COLS=120 W170_ROWS=36 \
  timeout 285 node "$ORACLE/drivers/w170-direct.mjs" > "$ORACLE/logs/t03-post-170-120.log" 2>&1
```

期望：

| 组 | 期望 |
|---|---|
| W175 frames | `131/0` |
| W175 row | `10/0` |
| W171 80/120 | `42/0` |
| W167 80/120 | `74/0`（驱动修后；inline 绿） |
| W170 80/120 | `44/0` |

一次全绿还不算稳定；W171 双尺寸各再跑 2 次，连续 3 次。

---

## 3. 如果 T03 后 W167 inline 仍红

不要开第二个 writer。按以下顺序定位：

### 3.1 先确认不是驱动/计数

- 用 W167 诊断的 `verify-claims.py` 重跑
- 确认 journal `user/message=9`、多行 user 帧=1
- 确认重复只在 `scrollback-full-buffer.txt`，不在 export/journal

### 3.2 只读定位到函数

- 抓 `DSH_TUI_DEBUG=1` 的 raw
- 看 `ink.tsx:1085` 的 `[REPAINT] full reset · <reason>`
- 确认是否又走 `ESC[H` + LF + 无 `ESC[J`
- 对照 T03 当前 `log-update.ts` 的对应分支

### 3.3 兜底补丁面（只在 T03 未覆盖时）

只允许 `src/ink/log-update.ts`：

1. `viewportY` 改为**真实滚动计数**，不再用 `heightsViewportY` 公式单独推算
2. 任何 `ESC[H` 全量重绘，先 `ESC[J` 擦除覆盖段，或只画 `[viewportY, screen.height)`
3. 提交帧按**视觉行数**结算可擦区，不按逻辑高度差
4. 加一个 replay 测试：W167 raw offsets 60080/62282，断言 `user.multiline` 只 1 份

禁止：

- 改 `MessageList.tsx` / `PromptInput.tsx` / `Chat.tsx`（内容单份，改了只掩盖）
- 改 `ink.tsx` / `terminal.ts` / `screen.ts` / `render-to-screen.ts`
- 改 `channel/**` / `dsh-adapter/**` / `lib/**`
- blank-spacer、隐藏 tool/JobCard、删断言、放宽唯一性

---

## 4. 合并与冻结

1. T03 patch / 兜底 patch 单独落到 `RC`
2. `patch -p1 --dry-run` + reverse-check
3. 只 add 认领文件；不 add `lib/**`、`node_modules/**`、`tmp/**`
4. `pnpm compile` 重建 `lib`
5. 冻结 RC：
   - `src`、`lib`、`package.json`、dshd 四类 hash
6. 跑 T02 入口的完整 targeted
7. 全绿后才进 T11 full

---

## 5. 回滚

- BASE 永久保留：`$G/base`
- 任何 patch 失败：`RC` 从 `$G/base` 重新复制，不 reset 共享树
- 不覆盖 `LIVE`
- 不删 T03/W167 证据

---

## 6. 停止条件

出现任一条，不继续推进：

- W171 出现 `40/2`、`41/1` 抖动
- W175 row 回退 `9/1`
- W170 从 `44/0` 回退
- W168 从 `33/0` 回退
- W167 inline 红仍在，且修复需要第二个 writer
- `sourceDrift=true`
- 单 PTY 队列超时 / 第二个 PTY 混入

---

## 7. 当前结论

- Fix A 已应用（驱动口径），不跑复验，等 T03 停笔
- Fix B 归 T03 主路径；本方案只准备验收与兜底
- 当前 GO.1 仍 **NO-GO**
- T03 未交付前，不启动任何产品修复 writer、不跑 full
