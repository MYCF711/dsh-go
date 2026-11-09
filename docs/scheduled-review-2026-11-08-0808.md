# 2026-11-08 08:08 定时复核

## 结论

团队方向未偏离，T00/T01/T03/T06 已交付，I03 正在串行集成，T07 已接续执行。T06 虽然主结果 `OK=37 FAIL=0`，但 5 跑 1 红暴露 F4 稳定版 blocker，已追加 T08/I08 并接入 I06，不得按单次绿直接放行。

## 当前任务

| 项 | 真实状态 |
|---|---|
| T00 | completed；RESULT/baseline 已交付，build/vet/kernel-deps exit0 |
| T01 | completed；17 方法；实际 patch `bf59efc3…`、39894B，位于 `.agents/worktrees/t01-workspace-initdefault/tmp/GO2-TEAM/T01/` |
| T03 | completed；实际 patch `11b2ac0e…`；I03 in_progress，共享树 apply-check exit0 |
| T06 | completed；`OK=37 FAIL=0`，真实 create/续轮/pause/resume/重启/A-B 证据 |
| T02/T04/T05 | in_progress |
| T07 | in_progress（T06 完成后自动接续） |
| I01/I02/I04/I05 | pending，等 integrator 串行处理 |
| HS-17/18/19、GUI | 未启动 |

## 新发现与处置

### F4 · 稳定版 blocker（已创建 T08/I08）

- `jobs_process_windows.go:13-23` 只调一次 `QueryFullProcessImageName`。
- 遇 Windows `ERROR_GEN_FAILURE(31)` → `registerTurnRun` 失败 → goal disarm，且只写 stderr；journal/API/UI 无错误。
- T06 定稿后 5 跑 1 中；普通 `session.prompt` 共用该注册路径，续轮则静默失败。
- 已创建 T08（t19，session，T04 后）与 I08（t20，integrator），I06 已加入 I08 依赖。
- 验收要求：仅 ERROR_GEN_FAILURE 有界退避；持续失败必须有 goal/change 或 transport 错误帧；禁止只写 stderr。

### 其他发现

- F1：`DSHD_SERVE_MODEL_RUNNER=1` 调试路径不发 tools，不用于工具/goal 证据；本轮只登记，不改产品。
- F2：`serve.go` 旧注释与实际续轮行为不符；只登记，不在本轮越界修。
- F3：仓根 90 个 worktree 导致 rewind 捕获告警；HS-17 必须使用干净临时 workspace。
- T03 `deleteInactive` 官方原文只删 ended，paused 保留，任务表已纠正。
- T01/T03 的 WIRING/消息存在旧 hash 元数据，均以实际 patch 文件哈希为准并通知 integrator。

## 偏差检查

- 未重复派单；T08/I08 是对新实证缺陷的增量任务，不重做 T06。
- T03 在共享树的写入属于 I03 integrator 合法串行集成，不判陌生 writer 违规。
- 未见 reset/clean、危险杀进程、重启 DSH/magpie、写 profiles、自动 push。
- goal 仍 active/armed，本轮未改状态。

## 下一动作

继续等待 T02/T04/T05/T07 与 I03 主动交付；T08 在 T04 后启动。I06 必须等 I01–I05、I08、T06、T07 全部完成，不能提前解锁 HS-17/18。
