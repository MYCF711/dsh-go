# 定时方向与派单复核 · 2026-11-03 19:08

> 宿主与界面两个只读子代理并行复核，Lead 同步检查任务表；完成方主动通知，无 wait/poll。未启动服务、未重跑模型/真机、未修改实现。以下是 18:28 后的增量复核，既有 PASS 不冒充本轮复验。

## 基线

- 宿主：`782b513`（M286）。
- 界面：`d0f540f`（本轮审查期间持续推进；表内只计已提交证据）。`236ca83` 补 method=14 人类文档与 FrameSub patch，`d0f540f` 仅补 patch README/39 包测试记录，均不是宿主提交，不改变未闭环状态。
- 宿主工作树另有 `internal/transport/wssession.go` 重放修复，**未提交、未计完成**。

## 已确认的新进展

1. UI 已提交 epoch/reset：`6363bf3`；后台水位：`d04bade`；大缺口分批补洞：`7e010ab`；allow_always 第二次自动放行：`72e4e3d`。
2. UI 新增真实 7526 条会话拉 1000 条的渲染证据：`1a7c70f`，104.1 MB；并发补洞兜底：`4040f05`。
3. 凭据失效界面已有明确提示：`b594b2f`，但这**不是宿主重启自愈**。
4. 宿主 `FrameSub` 重放修复只在工作树，不能提前关闭 W1/W7。源码复核另有未复现风险：重放到订阅非原子、pendingReplay+journal replay 可能重/丢；W1 已要求定向不丢/不重测试。

## 新发现/仍未闭环

- **H10 当前回归**：M286 记录 create 返回 id 与子进程实际写入 id 不同，prompt 后原会话 load assistant=0，等待 240s 仍红。已新增 W10，因与 W1/W3 共用 serve 文件而 BLOCKED，不能并行抢写。
- **U0/H14**：UI committed snapshot 仍是 `/api/mux`、旧 `kind` 和 history messages；History 请求未带 epoch；共享 ws-golden 不存在；宿主重启 cookie 仅提示、无自愈。追加源码反证：EpochOf 仅在 `newSession` 接线，首启既有会话仍 epoch=0；`session/reset` 未清 runSubscription 局部 since，验收必须证明重置后 since=0；history 409 只记日志，尚未形成重建闭环。
- **W1/W3/W4/W5**：serve 跨轮重复/debug 旁路、pending cancel、stale_epoch 缺 retryable 均未见本轮关闭证据，继续 IN_PROGRESS。
- **U1-H6**：还需宿主重启恢复、FrameSub 修复提交后的同版本复验，以及进程内 A→B→A 连续流。`product-acceptance.md` #19b/#20 仍有“界面没补/验证中”的旧句，需 UI Lead 按最新 reconnect 与补洞证据校正，不能继续互相矛盾。
- **U1-H13**：allow_always 效果已补证；剩余 pending cancel 与审批恢复。
- **U1.5**：V1/V2/V3/V4A/U2 的 owned paths 无实现变化，仍无当前 rev 的 1920×1032、1182×832 与逐容器 ≤8px 证据。
- **W8/W9/U4P/PERF**：四份计划文件均不存在，无可辨识 owner/产出，继续 READY，不伪造 IN_PROGRESS。

## Lead 方向纠偏

界面链路持续推进，但 U1.5 与验收设计 READY 已连续两次定时复核无 owner/产出。这是**并行优先级偏差**：下一动作必须在 owned paths 不重叠的前提下同轮派出 V1、V2、V3、V4A、U2、U4P、PERF，不得让链路工作占满全部执行容量。宿主 W8/W9 同样应同轮外派。

可见历史记录仍有子代理/并行产出，本轮也无 wait/poll 证据；但不能用“曾经并行过”免除当前 READY 的派发与 owner 回填。若已经外派，应立即补 IN_PROGRESS+owner；若未外派，则按任务表齐派。

## 本轮任务表动作

- PLAN：W2/W7 更新 epoch/reset、分批补洞等证据；W1 标明 wssession 仅工作树；W4/W5 保持 IN_PROGRESS；W6 记录本报告；W8/W9 保持 READY；新增 W10（H10 回归，等 serve 路径释放）。
- UIPLAN：U0/U1-H6/U1-H13 更新新证据与剩余条件；V1–V4A/U2/U4P/PERF 保持 READY 并标记连续未派纠偏；V4B 继续等 main.go owner 主动通知释放；R0 记录本报告。
- `docs/transport-contract.md`：H10 标当前回归，H14 更新已提交进展，H13 补 allow_always 效果证据。

## 当前 READY

- 宿主：W8 A/B 验收设计、W9 性能缺口设计。
- 界面：V1、V2、V3、V4A、U2、U4P、PERF。
- 不把正在修改的 serve/wssession/main.go 重复派给其他写者。

## 未验证

H10 回归修复、共享 golden、History 带 epoch、宿主重启 cookie 自愈、进程内 A→B→A、pending cancel、stale_epoch 四字段、serve 跨轮去重/debug 统一、U1.5 两尺寸/≤8px、完整工作日、10 任务 A/B、`-race`（无 C 工具链）。
