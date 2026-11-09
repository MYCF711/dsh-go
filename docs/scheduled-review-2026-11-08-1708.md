# 2026-11-08 17:08 定时复核

## 结论

I17 已正式完成。T17W2 的全量 race 第一轮出现 ConPTY 长粘贴与 bash setcwd 超时，第二轮全量 race 已绿；QA 仍在跑 pluginmgr 分片和最终汇总，尚未写 GO_ALLOWED，T19W2 未启动。首轮红没有被删除或覆盖，稳定版仍未放行。

## 当前证据

| 项 | 状态 |
|---|---|
| build / vet / full test | EXIT=0 |
| full race 第一轮 | EXIT=1：`TestPTYLongPasteExactBytes/n=65536` 等不到完成标记；`TestSetCwdAffectsNewShell` 10分钟超时 |
| full race 第二轮 | `RACE_RERUN_EXIT=0`，全包通过、DATA RACE=0 |
| pluginmgr 官方包 race×20 / I17 | 已通过；作业完成态原子性修复已集成 |
| fresh-home serve/run | 已跑，待 RESULT 汇总 |
| sandbox / 501 / 193 / 97 | 已跑，待 RESULT 汇总 |
| pluginmgr race 分片 | 10份日志已生成，QA 仍在最终核验 |
| T17W2 GO_ALLOWED | 尚未生成 |
| T19W2 | pending |

## 判定边界

- 第二轮绿不自动覆盖第一轮红；必须由 QA 将首轮红分类为测试环境/超时问题并给出同码证据，或另派产品修复。
- 不能把历史 T17W 的 GO=1 或 I17 的 scoped 绿直接当发布通过。
- 未 commit/push/tag/发布。

## 进程与偏差

- 16:51 后 QA 仍在持续产生日志，不是停摆。
- 17:08 检查除旧无关 node 外，出现一个非本链 `bash ... npm publish`（PID 46640，17:07 创建）；未授权、未清理，与本 GO.2 任务路径无关。
- 未 reset/clean、未重启 DSH/magpie、未写 profiles、未自动 push。
- goal 保持 active，本轮未改状态。

## 下一动作

等 T17W2 正式 RESULT/GO_ALLOWED：若全绿，T19W2 新建发布副本重跑全量门；若首轮红被认定产品问题，则先修再验，不进入发布。
