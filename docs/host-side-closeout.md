# 宿主侧收尾（2026-11-03）

> 这一份是**边界说明**，不是战报。它回答三件事：
> 做完了什么、证据在哪、剩什么——以及剩的那些**该去哪个仓做**。

## 1. 一句话

`docs/transport-contract.md` 的 H 表中，宿主 journal/H6/H8 等已有本仓证据；**H13 仍有 pending cancel 缺口，不能称本仓事项全部完成**。其余多数条目需要 `../dsh-ui` 一起参与，不能用宿主侧证据替代跨仓验收。

## 2. 证据在哪

```bash
python scripts/h-suite.py           # 13 条探针，一条命令
python scripts/h-suite.py --fast    # 不需要模型的 7 条（约 1 分钟）
```

每条探针都必须交出**机器可读的汇总行**；交不出来（崩了、只写进文件、被跳过）一律算失败。
"没跑出结论"和"通过"是两件事。

## 3. 本轮（M253–M283）落地的能力

| 能力 | 关键证据 |
|---|---|
| H14 每会话 journal：seq 单源、WS 与 history 同源 | `TestJournalSeqIsPerSession`、`TestTurnSeqContinuesFromEvents` |
| history 返回**原始帧** + 世代/水位；`stale_epoch` 409 | `TestHistoryReturnsRawFrames`、`TestHistoryRejectsStaleEpoch` |
| 订阅时世代不符 → `session/reset` | `TestSubscribeWithStaleEpochEmitsSessionReset` |
| 跨仓共享契约夹具 + 重放 | `internal/transport/testdata/ws-golden.jsonl`、`TestGoldenFramesMatchContract` |
| 两会话隔离（真机） | `scripts/h14-isolation.py` 7/7 |
| H13 审批整链：允许/拒绝/超时/重连/以后都允许 | `scripts/h13-*.py` 共 11 项全绿 |
| H6 思考进日志再进流 | `TestReasoningBlockSurvivesEvent`、真机 `流里有 reasoning: True` |
| H12 只有正数才算"知道宽度" | `TestContentWidthContract` |
| 顶层 `content` 那种落法（原来会丢正文） | `TestAssistantMessageBothContentShapes` |

## 4. 四层守卫（都做过"弄坏它必须红"的反证）

| 层 | 守卫 | 不能证明 |
|---|---|---|
| 文本 | `help-guard` | 说明本身对不对 |
| 表格 | `contract-evidence` | 被点名的测试是否覆盖了这条需求 |
| 代码 | `flag-wired` / `method-wired` | 引用/分发到了正确的地方 |
| 行为 | 13 条真机探针 | 界面怎么消费、负载下如何 |

`scripts/guard-selftest.py` 会把每个守卫弄坏一次，**它第一次跑就抓出 `flag-wired` 的假阴性**
（变量名和 flag 名同名时，定义里的字符串被当成了一次"引用"）。

## 5. 剩什么（按仓分责，18:21 复核）

| 条目 | 缺什么 | 在哪个仓 |
|---|---|---|
| H13 pending cancel | cancel→拒绝、工具不执行、`approval/cancel`、第二轮 prompt | **本仓**；补失败测试与实现后再验 |
| H14/H6 serve 接线 | follow 零水位重复、debug runner 绕过 journal；特殊 replay/reset/pong 形状补测试 | **本仓**；不能用核心 journal 单测代替 |
| H8 stale_epoch | 特殊错误体缺 `retryable` | **本仓**；统一 errorBody 并补四字段测试 |
| H14 UI | 快照旧路径/帧/messages、History 带 epoch、共享 golden、reset 与宿主重启 cookie | `../dsh-ui` + 跨仓 |
| H1 / H2 / H9 / H10 | 当前同版本 UI 消费、未知值与真实 home 副本遍历 | `../dsh-ui` + 跨仓 |
| H6 UI 恢复 | reasoning 已有显示证据；仍需断线恢复、去重与同版本复验 | 两仓 |

## 6. 两条已知未验证（写清楚，不让它悄悄变成"应该没问题"）

1. **`-race` 从未跑起来**：本机无 C 工具链（`CGO_ENABLED=0`，`gcc`/`clang`/`cl` 都没有）。
   替代品 `TestConcurrentJournalAndHubInvariants` 能抓死锁/丢帧/seq 错乱，
   **抓不到没有可见症状的数据竞争**。
2. **`allow_always` 只在内存、只在本进程**：重启后重新问。刻意不落盘（跨进程生效会静默放行
   用户不预期的会话），但界面若要"永久记住"，需要另立需求。

## 7. 下一步建议

1. 界面仓按 `ws-golden.jsonl` 写解析器，跑它对同一份夹具的折叠测试——**两边都过才叫契约对齐**
2. 用同一份新二进制 + 临时 home 跑完整工作日，做 A/B 与进程树性能
3. 门禁：把 `h-suite.py` 接进 CI 的 fast 档（真机档太慢，放夜间）

## 8. 【已收口】h10 那次红：不是宿主，是陈旧二进制

结论：**`session.load` 没问题**。红的原因是 `scripts/h10-chain.py` 用的是
`tmp/serve-dir/dshd.exe`（17:35 编译），而 `tmp/dshd.exe` 是 18:48 的。
同一个脚本换成新二进制 → **6/6 全绿**。

顺带改了两处让这类事不再难查：

- h10 里加上「prompt 被接受」的断言：如果 prompt 根本没被接受，后面 load 不到
  assistant 是**必然**，不该记成"宿主丢了消息"
- h10 的等待窗口（120s）必须**明显短于套件超时**，否则超时后套件拿到的是
  "没有汇总行"，看起来像探针崩了；套件现在也会先大字报一句
  「N 条探针没有给出结论」

下面是当晚查错过程的原始记录，保留是因为**那段推理本身有价值**：

## 8.1 原始记录（问题描述，已解决）

`scripts/h10-chain.py` 的「prompt 之后能 load 到 assistant 消息」**从绿变红**，
把等待从 60 秒加到 240 秒仍然红——**所以这次不是探针耐心的问题**。

直接复现（临时 home、`--policy allow`、一个新会话）：

- `session.prompt` 之后 `session.history` 里**有** `turn/start … assistant/message … turn/done`（20 帧）
- 但 `session.load` 里 assistant 消息仍然是 0
- serve 的 stderr 打出 `会话：…\session-e19bce7d…`，而 `session.create` 返回的是**另一个** id

初步判断：`serve` 起子进程时 `--session <id>` 没让子进程落回那个会话，
于是"新会话写在别处、load 原会话什么都没"。
**尚未定位到具体那一段代码，也未修**，下一轮从这里开始：

1. 读 `cmd/dshd/serve_tools.go` 的 args 组装与 `cmd/dshd/main.go` 的 `--session` 处理
2. 判据：`session.create` 拿到的 id 必须与子进程 stderr 里那行「会话：」的 id 相同
3. 修好后 `h10-chain.py` 回到 5/5
