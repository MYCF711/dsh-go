# 宿主侧收尾（2026-11-03）

> 这一份是**边界说明**，不是战报。它回答三件事：
> 做完了什么、证据在哪、剩什么——以及剩的那些**该去哪个仓做**。

## 1. 一句话

`docs/transport-contract.md` §12 里，**本仓能独自验的条目已全部验过并改 ✅**；
剩下的每一条都要 `../dsh-ui` 一起参与，不在本仓范围内。

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

## 5. 剩什么（都不在本仓）

| 条目 | 缺什么 | 在哪个仓 |
|---|---|---|
| H1 usage / H2 time | 界面消费这两个数、未知时不补零 | `../dsh-ui` |
| H9 / H10 | 界面只用契约名调一次；真实 home 全量遍历 | `../dsh-ui` + 一次只读遍历 |
| H14 | 界面换 `/ws` + 裸参数 + 消费 `frames` | `../dsh-ui` |
| H6 | 界面实际渲染 reasoning | `../dsh-ui` |

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
