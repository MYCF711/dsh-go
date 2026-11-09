# Web 端实例接手记录 · 2026-11-08 00:35

## 结论

web 端独立实例已关闭；Team Mode 的 T15A/T11 已由本 Lead 接手，T13 独立审查仍待 T11 通过。

## 方案状态

- DSH-GO 宿主稳定版：v12 任务书；当前 `HS-07W`/`HS-08`/`HS-GOAL-TRUTH`/`HS-10` 在途，`HS-05` DELIVERED(policy-only)，`HS-16A`/`HS-07` INTEGRATED_GREEN(scoped)。stable 仍 NO-GO。
- web 独立线：Team Mode T14A/B/C 已由原会话交付并 Lead 复验；T15A 正在进行（F1 静态 inject、F2 `symbols.original`、F3 raw dispose）；T11 旧报告 FAIL 30/37、3 红、4 阻断；T12B/T13 依赖 T11。
- M1-E2E 日志显示 `ALL DONE`；M0 stream family 与 TUI 子进程随 web 实例关闭而停止。

## 关闭的 web 实例

- 根进程：`DeepSeek Harness.exe` PID **38484**，profile `web-latest2`，端口 **61528**。
- 子进程：36952（m1-e2e）、27544（TUI build/serve）、14148（run-green9）、47804（M0 stream family）。
- 关闭命令：递归枚举该树后 `Stop-Process -Force`；关闭后 38484 与 61528 均不再存在。
- **未触碰**当前桌面宿主 36100/15816；当前会话仍在运行。
- 现场快照：`C:/Users/Administrator/AppData/Local/Temp/web-takeover-process-tree.json`。

## 接手任务

| task_id | owner | 模型 | 写作用域 | 状态 |
|---|---|---|---|---|
| `TM-T15A` | `359e65b6-e431-4135-9687-b30a58362582` | `group/deepseek-v4.1-flash-1017` | Team Mode `src/index.js`、`src/host/activation.js`、`tests/rc1-*`、`artifacts/rc1/**`、`tmp/team-mode-plan/**`、`TASKS.md` | IN_PROGRESS |
| `TM-T11` | 同 T15A | 同上 | 真实 rc1 Host/Client 探针与报告 | BLOCKED(T15A) |
| `TM-T13` | 独立审查者（未派） | 只读审查 | `docs/REVIEW*.md`、最终证据 | BLOCKED(T11) |
| `TM-T12B` | 待派 | 文档/打包 | `docs/`、`HANDOFF.md` | BLOCKED(T11) |

## T15A 验收门

1. `npm run build` exit 0。
2. `node --test tests/*.test.mjs` exit 0。
3. Host rc1 探针 24/24，exit 0。
4. Client rc1 探针 13/13，exit 0。
5. 合并报告 37/37，Tier B 无 blocked，cleanShutdown。
6. 红项原文保留；不得把 mock 当真实挂载，不得改 live profile 或重启 DSH/magpie。

## 边界

未改 DSH-GO 产品代码、未 commit/push、未重启当前桌面宿主；只关闭用户授权的 web 实例进程树。Team Mode 是独立项目，不是 DSH-GO 的 worktree。
