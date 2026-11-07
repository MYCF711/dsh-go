# 官方 0.2.2-rc.1 插件清单（dsh-base 97 项）

> 来源：`D:/DeepSeek Harness/Desktop-0.2.2-rc.1/resources/app.asar` → `dsh/node_modules/@deepseek-ai/dsh-base/cordis.patch.yml`。
> 版本：`@deepseek-ai/dsh-base 0.2.2-rc.1`。这是 **base profile**，不是用户 profile 的 31 项子集，也不含 UI/TUI 插件。
> 用途：宿主同步清单；`DONE/PARTIAL/MISSING` 由映射审计回填，不能凭本表直接宣称完成。

| # | plugin id | package | 宿主状态 |
|---:|---|---|---|
| 1 | `tool-plugin-manager` | `@deepseek-ai/dsh-plugin-manager/tools` | PARTIAL: 8 local plugin.* methods; no official npm ecosystem |
| 2 | `plugin-manager` | `@deepseek-ai/dsh-plugin-manager` | PARTIAL: local dsh.local-plugin@1 manager |
| 3 | `timer` | `@deepseek-ai/cordis-plugin-timer` | 待核 |
| 4 | `hmr` | `@deepseek-ai/dsh-hmr` | 待核 |
| 5 | `llm` | `@deepseek-ai/dsh-llm` | 待核 |
| 6 | `deepseek-llm-api-extensions` | `@deepseek-ai/dsh-deepseek-llm-api-extensions` | 待核 |
| 7 | `session` | `@deepseek-ai/dsh-session` | 待核 |
| 8 | `session-log-deepseek` | `@deepseek-ai/dsh-session-log-deepseek` | 待核 |
| 9 | `typert` | `@deepseek-ai/dsh-typert-registry` | 待核 |
| 10 | `typert-loader` | `@deepseek-ai/dsh-typert-loader` | 待核 |
| 11 | `typert-gateway` | `@deepseek-ai/dsh-api-gateway` | 待核 |
| 12 | `remote-access` | `@deepseek-ai/dsh-api-remote-access` | 待核 |
| 13 | `session-title` | `@deepseek-ai/dsh-session-title` | 待核 |
| 14 | `session-title-llm` | `@deepseek-ai/dsh-session-title-first-prompt-llm` | 待核 |
| 15 | `user-questions` | `@deepseek-ai/dsh-user-questions` | 待核 |
| 16 | `agent` | `@deepseek-ai/dsh-agent` | 待核 |
| 17 | `plugin-package-inventory-deepseek` | `@deepseek-ai/dsh-plugin-package-inventory-deepseek` | 待核 |
| 18 | `agent-default-model` | `@deepseek-ai/dsh-agent-default-model` | 待核 |
| 19 | `jobs` | `@deepseek-ai/dsh-jobs-local` | 待核 |
| 20 | `llm-retry` | `@deepseek-ai/dsh-llm-retry` | 待核 |
| 21 | `config-editor` | `@deepseek-ai/dsh-config-editor` | 待核 |
| 22 | `settings` | `@deepseek-ai/dsh-settings` | 待核 |
| 23 | `authorization` | `@deepseek-ai/dsh-authorization` | 待核 |
| 24 | `deepseek-account` | `@deepseek-ai/dsh-deepseek-account-platform` | 待核 |
| 25 | `credentials` | `@deepseek-ai/dsh-credentials-local` | 待核 |
| 26 | `llm-pi-ai` | `@deepseek-ai/dsh-llm-pi-ai` | 待核 |
| 27 | `session-persistence-jsonl` | `@deepseek-ai/dsh-session-persistence-jsonl` | 待核 |
| 28 | `attachment-local` | `@deepseek-ai/dsh-attachment-local` | 待核 |
| 29 | `session-query-sqlite` | `@deepseek-ai/dsh-session-query-sqlite` | 待核 |
| 30 | `working-directory` | `@deepseek-ai/dsh-working-directory` | 待核 |
| 31 | `tool-working-directory` | `@deepseek-ai/dsh-tool-working-directory` | 待核 |
| 32 | `session-projection` | `@deepseek-ai/dsh-session-projection` | 待核 |
| 33 | `storage` | `@deepseek-ai/dsh-storage` | 待核 |
| 34 | `storage-json` | `@deepseek-ai/dsh-storage-json` | 待核 |
| 35 | `storage-domain` | `@deepseek-ai/dsh-storage-domain` | 待核 |
| 36 | `session-projection-cache` | `@deepseek-ai/dsh-session-projection-cache` | 待核 |
| 37 | `otel` | `@deepseek-ai/dsh-otel` | 待核 |
| 38 | `session-telemetry-otel` | `@deepseek-ai/dsh-session-telemetry-otel` | 待核 |
| 39 | `subprocess` | `@deepseek-ai/dsh-subprocess-local` | 待核 |
| 40 | `sandbox` | `@deepseek-ai/dsh-sandbox-local` | 待核 |
| 41 | `sandbox-policy` | `@deepseek-ai/dsh-sandbox-policy` | 待核 |
| 42 | `bash-sandbox` | `@deepseek-ai/dsh-bash-sandbox` | 待核 |
| 43 | `pwsh-sandbox` | `@deepseek-ai/dsh-pwsh-sandbox` | 待核 |
| 44 | `approval` | `@deepseek-ai/dsh-user-approval` | 待核 |
| 45 | `permission` | `@deepseek-ai/dsh-permission-presets` | 待核 |
| 46 | `shell-env` | `@deepseek-ai/dsh-shell-env` | 待核 |
| 47 | `tool-bash` | `@deepseek-ai/dsh-tool-bash` | DONE scoped: bash |
| 48 | `tool-pwsh` | `@deepseek-ai/dsh-tool-pwsh` | DONE scoped: pwsh |
| 49 | `tool-jobs` | `@deepseek-ai/dsh-tool-jobs` | DONE scoped: job_* |
| 50 | `fs-observation-policy` | `@deepseek-ai/dsh-fs-observation-policy` | 待核 |
| 51 | `tool-fs` | `@deepseek-ai/dsh-tool-fs` | DONE scoped: read/write/edit |
| 52 | `tool-fs-search` | `@deepseek-ai/dsh-tool-fs-search` | DONE scoped: glob/grep |
| 53 | `agent-instructions` | `@deepseek-ai/dsh-agent-instructions` | 待核 |
| 54 | `skill` | `@deepseek-ai/dsh-skill` | 待核 |
| 55 | `skill-filesystem` | `@deepseek-ai/dsh-skill-filesystem` | 待核 |
| 56 | `tool-skill` | `@deepseek-ai/dsh-tool-skill` | 待核 |
| 57 | `commands` | `@deepseek-ai/dsh-commands` | 待核 |
| 58 | `command-feedback` | `@deepseek-ai/dsh-command-feedback` | 待核 |
| 59 | `goal` | `@deepseek-ai/dsh-goal` | 待核 |
| 60 | `goal-round-driver` | `@deepseek-ai/dsh-goal-round-driver` | 待核 |
| 61 | `command-goal` | `@deepseek-ai/dsh-command-goal` | 待核 |
| 62 | `plan-mode` | `@deepseek-ai/dsh-plan-mode` | DONE scoped: plan mode |
| 63 | `token-meter` | `@deepseek-ai/dsh-token-meter` | 待核 |
| 64 | `compaction-basic` | `@deepseek-ai/dsh-compaction-basic` | DONE scoped: compact |
| 65 | `command-compact` | `@deepseek-ai/dsh-command-compact` | 待核 |
| 66 | `subagent` | `@deepseek-ai/dsh-subagent` | PARTIAL: subagent/teammate |
| 67 | `subagent-spawn-in-process` | `@deepseek-ai/dsh-subagent-spawn-in-process` | 待核 |
| 68 | `subagent-fork-in-process` | `@deepseek-ai/dsh-subagent-fork-in-process` | 待核 |
| 69 | `tool-subagent-control` | `@deepseek-ai/dsh-tool-subagent-control` | PARTIAL: interrupt_agent |
| 70 | `tool-subagent` | `@deepseek-ai/dsh-tool-subagent` | PARTIAL: subagent tool |
| 71 | `tool-subagent-fork` | `@deepseek-ai/dsh-tool-subagent` | PARTIAL: subagent_fork |
| 72 | `ptc-runtime` | `@deepseek-ai/dsh-ptc-runtime-node` | 待核 |
| 73 | `workflow-ptc` | `@deepseek-ai/dsh-workflow-ptc` | PARTIAL: PTC limited |
| 74 | `tool-workflow` | `@deepseek-ai/dsh-tool-workflow` | PARTIAL: JSON steps, not JS |
| 75 | `timeout-policy` | `@deepseek-ai/dsh-tool-call-timeout-policy` | 待核 |
| 76 | `spill-local` | `@deepseek-ai/dsh-spill-local` | 待核 |
| 77 | `spill-policy` | `@deepseek-ai/dsh-spill-policy` | 待核 |
| 78 | `session-checkpoint-policy` | `@deepseek-ai/dsh-session-checkpoint-policy` | 待核 |
| 79 | `tool-result-pruner` | `@deepseek-ai/dsh-compaction-tool-result-pruner` | DONE scoped: internal/prune |
| 80 | `image-offload` | `@deepseek-ai/dsh-compaction-image-offload` | 待核 |
| 81 | `tool-todo` | `@deepseek-ai/dsh-tool-todo` | DONE scoped: todo_write |
| 82 | `tool-goal` | `@deepseek-ai/dsh-tool-goal` | DONE scoped: goal tools |
| 83 | `repeat-tool-reminder` | `@deepseek-ai/dsh-repeat-tool-reminder` | 待核 |
| 84 | `web` | `@deepseek-ai/dsh-web` | 待核 |
| 85 | `web-search-deepseek` | `@deepseek-ai/dsh-web-search-deepseek` | 待核 |
| 86 | `web-deepseek-api` | `@deepseek-ai/dsh-web-deepseek-api` | 待核 |
| 87 | `web-fetch-http` | `@deepseek-ai/dsh-web-fetch-http` | 待核 |
| 88 | `tool-web` | `@deepseek-ai/dsh-tool-web` | DONE scoped: web_fetch/web_search |
| 89 | `mcp-resources` | `@deepseek-ai/dsh-mcp-resources` | 待核 |
| 90 | `tools` | `@deepseek-ai/dsh-tools` | 待核 |
| 91 | `tool-executor-native` | `@deepseek-ai/dsh-tool-executor-native` | 待核 |
| 92 | `tool-executor-ptc` | `@deepseek-ai/dsh-tool-executor-ptc` | 待核 |
| 93 | `system-prompt` | `@deepseek-ai/dsh-system-prompt` | 待核 |
| 94 | `agent-loop` | `@deepseek-ai/dsh-agent-loop` | 待核 |
| 95 | `fs-sandbox` | `@deepseek-ai/dsh-fs-sandbox` | 待核 |
| 96 | `llm-deepseek` | `@deepseek-ai/dsh-llm-deepseek-api-key` | 待核 |
| 97 | `llm-deepseek-account` | `@deepseek-ai/dsh-llm-deepseek-account` | 待核 |

## 统计

- 总项：97
- 已显式映射：28
- 待核：69

> UI/TUI 插件不在本表；W167/W170 等 TUI 红不归宿主。
