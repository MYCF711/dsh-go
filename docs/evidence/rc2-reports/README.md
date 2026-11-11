# rc.2 同步任务报告（2026-11-12 轮次）

> 本目录是 **0.2.2-rc.2 宿主同步** 这一轮的逐任务报告（Lead 派单的产物）。
> 每条都含：官方 `file:line` 证据、先红/转绿记录、测试计数、接线行、未实现项。
> 结论汇总只认三处：`docs/official-baseline-0.2.2-rc.2.md` §5（落地状态）、
> `docs/go8-rc2-execution-blueprint.md`（工作流与任务表）、`docs/parameter-register.md`（参数轴）。

| 报告 | 任务 | 一句话结论 |
|---|---|---|
| `R1-session-transport.md` | 研究 | 会话/传输域差异；wire 主协议未变，v4 事件层新增 `team/mode` 与 catalog v3 |
| `R2-agent-team-tools.md` | 研究 | 代理/团队/工具/CUA 域；工具面 +7 改名 3 组、按名寻址、并发分类器 |
| `R3-llm-web-account-plugin.md` | 研究 | LLM/Web/账号/插件/设置/定时/Jobs；重试默认值未变，web 上限 10/10 |
| `R4-param-register-rc2.md` | 研究 | 参数总账 342→405、锚点存活 327/379、默认值真变化 5 条 |
| `A1-fs-search.md` | task-1 | `workspaceFiles.search` 纯 Go 等价实现（51+33 测试），与官方 ripgrep 的 9 条差异登记 |
| `A2-terminal.md` | task-2 | `terminal.watch` 真 NDJSON 流 + `WebTerminalInfo` 三字段 |
| `A3-web.md` | task-3 | `web.listProviders` + 上限 10/10（有代码证据）+ 默认 provider 切换 |
| `A4-account.md` | task-4 | 通知三件套（云端真实调用）+ 2 条删除方法兼容 |
| `B1-forge.md` | task-5 | Forge 12 端点：8 真做 / 4 结构化；快照 7 字段 + stale/unknown |
| `B2-team-subagent.md` | task-6 | AgentTeam 任务板制 + 按名寻址 + interrupt 默认关 |
| `B3-session-events.md` | task-7 | `team/mode`、catalog v3、continuation、runtime-context、错误码不折叠 |
| `B4-plugin-update.md` | task-8 | 插件 `update` 三态 + `unexpected-package` 回滚 |
| `C1-cua.md` | task-11 | CUA 9 方法（Windows 7 真做 + 2 有条件真做） |
| `C2-terminal-panel.md` | task-12 | `user_terminal_list/read` 工具 + 官方原文拒绝子代理 |
| `C3-mcp-manager.md` | task-9 | `mcpManager.probe/restart/watch`（真事件流） |
| `C4-office-kit.md` | task-10 | officeKit + 3 工具，**真 LibreOffice 渲染**（docx→PDF 21379 B） |
| `D1-param-register-impl.md` | task-14 | 参数总账 rc.2 重导出（405 条，两闸 EXIT=0） |
| `D2-credentials-jobs.md` | task-13 | 凭据 `masked` + JobError 7 码 |
| `D3-tool-pipeline.md` | task-15 | per-call 并发分类器（6×grep 5.6×）+ `error.info` 身份 |
| `D4-settings-market.md` | task-16 | `retryPolicy` 节点（`llm` ns）+ 市场 `update` + SSH 回退 |
| `E2-deepseek-web-api.md` | task-17 | `deepseek-search-api`/`deepseek-fetch-api` 真 provider（默认已生效） |
| `E3-session-wiring.md` | task-18 | 空 prompt 续跑准入 + catalog v3 写入 + 错误码落盘 |
| `E5-forge-wiring.md` | task-19 | Forge rc.2 接线 + 归档清理工作树（真 git 端到端） |
| `E6-tool-schema-parity.md` | task-20 | 工具 schema 对拍 rc.2（`TestToolParityWithDSH` 转绿） |
| `E7-office-wiring.md` | task-21 | Office 接线（真 dshd serve 出 21379 B PDF） |
| `E8-terminal-bridge.md` | task-22 | 父→子 roster 桥（真 PTY 11/11 + disabled 3/3） |
| `E9-liveshare-addsessions.md` | task-23 | `liveshareShares.addSessions`（真 relay + 真 serve 冒烟） |
