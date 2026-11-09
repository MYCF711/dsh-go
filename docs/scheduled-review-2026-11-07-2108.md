# 定时方向复核 · 2026-11-07 21:08 Asia/Shanghai

## 结论

当前方向未偏离。最新有效口径是：**宿主稳定版只验宿主完备**；TUI/Ink、官方 Electron/M0、Viva GUI 为独立线，不反向覆盖宿主稳定门。

## 当前执行入口

- `docs/architect-official-sync-blueprint.md`：v11 宿主方案书。
- `docs/architect-official-sync-taskbooks.md`：v11 精确任务书，23 个任务段、完成→解锁、模型与写作用域齐全。
- `AGENTS.md` 与 `LEAD.md` 已指向上述任务书；`docs/lead-dispatch-playbook.md` 归 M0/前端独立线，不是宿主 stable 派单入口。

## 活跃任务与两仓现场

- `list_agents`：仅 lead 运行；`w165-env-catalog` failed；无其他 active durable teammate。
- `job_list`：无后台 job。
- 没有 active HS 子代理；首批 `HS-00`–`HS-03` 尚为 READY，未重复派单。
- DSH-GO HEAD：`9cfe2f4`。共享树含 M0/前端独立线文件：`serve.go`、`serve_frontend*`、`serve_boot_facade.go`、`internal/webfrontend/**`、`index.js` 等；本复核未修改。
- dsh-ui HEAD：`3925e84`；存在大量 GUI 独立线未提交改动，本复核未运行其门禁，也不把改动当完成证据。

## 宿主稳定版下一步（严格按任务书）

首批同轮 4 路，模型全部 `group/deepseek-v4.1-flash-1017`：

1. `HS-00 HOST-BASELINE-RECHECK`：重新核对 97 插件与 153 API，禁止沿用旧计数派单。
2. `HS-01 HOST-SPI-RUNID-REPEAT`：ctxKey、RunID、repeat-reminder、per-call deadline。
3. `HS-02 HOST-MCP-TOOLS-RESOURCES`：MCP 工具真实注册/调用与 resources。
4. `HS-03 HOST-AUTO-TITLE-DELTAS`：自动标题五项 KNOWN-DELTA。

完成通知后 Lead 先集成复验，标 `INTEGRATED_GREEN` 才解锁后续；最多 4 个 active 写任务。

## 表更新

- `PLAN.md` W163 已改为 v11 任务书、首批 4 路 READY 与当前无 HS writer。
- `UIPLAN.md` 当前复核已更新到 21:08，清除把旧 97/153 统计当当前完成度的表述。

## 验证与边界

- 基线 `9cfe2f4` 的 build/vet/test 全 0 是已有 Lead 证据；本定时复核没有重跑全量，不冒充新门禁。
- 未跑 full/stable CI，未发布，未 push。
- 未改产品代码，未 checkout/reset，未杀进程，未重启 DSH/magpie。
- 本轮只改 PLAN/UIPLAN/本报告；doc-links 收尾复跑。
