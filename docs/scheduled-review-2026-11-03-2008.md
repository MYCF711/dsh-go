# 定时方向与派单复核 · 2026-11-03 20:08

> 两个只读子代理并行复核，Lead 同步检查任务表；完成方主动通知，无 wait/poll。未启动服务、未重跑模型/真机、未修改实现。只核 19:20 后增量，既有 PASS 不冒充本轮复验。

## 基线

- 宿主：`c4f1d63`（M291）。
- 界面：`42e1649`（本轮审查时的最新提交；后续工作树不自动计完成）。
- AGENTS 验收硬规则已合并：`AGENTS.md` §5，6906 字节；`docs/verify-rules.md` 已改为案例/任务书细节。

## 已确认的新进展

1. **H10 假回归已澄清**：M286 的 id 异常来自 17:35 陈旧二进制 vs 18:48 源码，不是当前宿主 bug；M289 同新二进制恢复 6/6，M290 将 `scripts/bin-check.py` 设为套件首门并有摸旧必红反证。W10 改 DONE（探针误判闭环），不再派宿主 id 修复。
2. **宿主 `-race` 已有真实记录**：M287/M288 安装工具链后 39 包通过，抓到并修三处测试夹具竞争；成品 CGO=0。旧的“无 C 工具链”仅作历史。**这不等于 UI race 已验**；UI 验收文档的“没有 gcc”已过期，应改“现有工具链下尚未复跑”。
3. **UI 连续链路（有保留）**：准工作日 e2e `194dc78` 记录 11 PASS / 0 FAIL / 1 SKIP；但审批是 UI 发 prompt+收帧、**脚本 curl 回传**，不是 UI 自己回传；隔离首断言存在空 load false-PASS 风险，日志未记 dshd_rev/ui_rev，cancel 仍 SKIP。须修探针并记录两仓 rev 后复验，不能冒充完整工作日。
4. **U2 转 IN_PROGRESS**：`53b0900` 有 uikernel 装配测试，`8ee7032` 修 uiplugins skip 漏分支并加配置路径测试；完整授权/替换/卸载/Dispose/公开槽仍未闭环。
5. 其他新增：测试固定端口根治、壳纯函数测试、UI bin-check 移植。静置内存记录所引 `tmp/idle.log` 未见连续采样，且最终 68.6 MB 与摘要 58/59 MB 口径不一致，**本轮不采信为 PERF 证据**，需补原始日志。

## 仍未闭环

- W0/W1/W3/W4/W5：serve 去重/debug、pending cancel、stale_epoch 四字段、快照/golden未见关闭证据；wssession 重放仍工作树。
- U0/U1-H6：History 请求带 epoch、首启既有会话非零 epoch、reset 后 since=0、history 409 重建、UI 快照与共享 golden、宿主重启 cookie、进程内 A→B→A 仍需证据。
- U1-H13：allow/deny/allow_always/e2e 审批有证据，pending cancel 与审批 reset 恢复仍未闭环。
- V1/V2/V3/V4A：无当前 rev 的 1920×1032、1182×832 与逐容器 ≤8px 完成证据，继续 READY。
- U4P/PERF、W8/W9：四份计划文件均不存在，继续 READY；e2e/静置/长会话证据不等于这些计划已产出。

## Lead 方向与派单

- 本轮两个只读子代理并行，无 wait/poll；未找到 Lead 自行串行的直接证据。
- U2 已有实际进展，改 IN_PROGRESS；V1/V2/V3/V4A/U4P/PERF 仍无对应 owner/产出，连续链路推进不能继续挤占这些 READY。
- 下一动作：READY 在 owned paths 不重叠时同轮外派，并立即回填 IN_PROGRESS+实际 owner/session+base rev+产出路径。W8/W9 同轮外派；新增 RACE 用现有 C 工具链实测 UI 并发面；V4B 继续等 main.go owner 主动释放。
- 不把 H10 误判、未提交 patch、文档 patch README 或单场景性能记录当成产品 DONE。

## 20:18 追加：视觉任务真正开工

- 另一会话已登记 V1/V2/V3/V4A/U4P/PERF 的实际 owner，base `bf7e888`，均转 IN_PROGRESS；RACE 已有两仓原始日志并标 DONE。
- 本 Lead 早先重复派出的 V1/V2/V3/V4A 已不在活动列表；保留另一 owner 的写入，不回滚、不重复派。V4B 随后由 `10c2a409` 接手，已出现先红测试与 main.go 修复，转 IN_PROGRESS。
- V2 出现用 `env.Data.Busy` 画活动绿点的实现；Busy 已证会漏报，任务表已明确禁止，验收时不得通过。
- UIPLAN 摘要已改为“每行一个 owner、完成主动通知”，删除已过期的“下一动作必须派出/base 42e1649”。
- 后续工作树已出现 V1 hero/brand、V2 sidebar 测试、V4A shell 测试，以及 U4P/PERF 两份计划；均为未提交产出，等 owner 主动通知后验收，不提前 DONE。V4A 仍缺两尺寸真实截图与逐容器实测表。

## 本轮任务表动作

- PLAN：W10 改 DONE（假回归与探针门禁闭环）；W7 补准工作日 e2e 证据；W6 记录本报告；W8/W9 保持 READY；其余焦点项不升级。
- UIPLAN：U2 转 IN_PROGRESS；U1-H6/U1-H13 补 e2e 证据；R0 记录本报告；V1/V2/V3/V4A/U4P/PERF 保持 READY 并要求同轮齐派。
- `docs/transport-contract.md`：H10 从 🔴 改为“陈旧二进制误判已澄清，跨仓/真实 home 待验”。

## 未验证

共享 golden、History epoch、首启/reset/409 全链、宿主重启自愈、进程内连续流、pending cancel、stale_epoch 四字段、serve 跨轮去重/debug 统一、U1.5 两尺寸/≤8px、完整工作日、10 任务 A/B。
