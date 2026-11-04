# 工具表常驻集（lazy 模式的前 N 名）

> 本文件由 `python scripts/tool-table-hot.py --write docs/tool-table-hot.md` 生成，**别手改**。

语料：`D:/DeepSeek Harness/home/sessions`，402 个会话（尾部不完整 0），81035 次 `tool/call`。

常驻集取我们自己的工具里调用次数前 30 名——覆盖 99.43% 的调用；表外 27 个只占 0.57%。

| # | 工具 | 调用次数 | 占比 | 累计 | 去留 |
|---:|---|---:|---:|---:|---|
| 1 | `bash` | 56334 | 69.52% | 69.52% | 常驻 |
| 2 | `read` | 6923 | 8.54% | 78.06% | 常驻 |
| 3 | `edit` | 5196 | 6.41% | 84.47% | 常驻 |
| 4 | `write` | 2122 | 2.62% | 87.09% | 常驻 |
| 5 | `job_output` | 1773 | 2.19% | 89.28% | 常驻 |
| 6 | `grep` | 1617 | 2.00% | 91.28% | 常驻 |
| 7 | `read_image` | 1505 | 1.86% | 93.13% | 常驻 |
| 8 | `glob` | 878 | 1.08% | 94.22% | 常驻 |
| 9 | `send_message` | 704 | 0.87% | 95.08% | 常驻 |
| 10 | `todo_write` | 441 | 0.54% | 95.63% | 常驻 |
| 11 | `web_fetch` | 317 | 0.39% | 96.02% | 常驻 |
| 12 | `subagent_fork` | 306 | 0.38% | 96.40% | 常驻 |
| 13 | `subagent` | 255 | 0.31% | 96.71% | 常驻 |
| 14 | `pwsh` | 250 | 0.31% | 97.02% | 常驻 |
| 15 | `github` | 244 | 0.30% | 97.32% | 常驻 |
| 16 | `get_goal` | 239 | 0.29% | 97.62% | 常驻 |
| 17 | `wait_agent` | 204 | 0.25% | 97.87% | 常驻 |
| 18 | `team_task_update` | 146 | 0.18% | 98.05% | 常驻 |
| 19 | `list_agents` | 141 | 0.17% | 98.22% | 常驻 |
| 20 | `team_task_get` | 132 | 0.16% | 98.39% | 常驻 |
| 21 | `job_kill` | 130 | 0.16% | 98.55% | 常驻 |
| 22 | `create_goal` | 110 | 0.14% | 98.68% | 常驻 |
| 23 | `schedule_create` | 108 | 0.13% | 98.82% | 常驻 |
| 24 | `update_goal` | 105 | 0.13% | 98.94% | 常驻 |
| 25 | `hindsight_search_knowledge_pages` | 80 | 0.10% | 99.04% | 常驻 |
| 26 | `team_task_create` | 77 | 0.10% | 99.14% | 常驻 |
| 27 | `web_search` | 67 | 0.08% | 99.22% | 常驻 |
| 28 | `hindsight_list_knowledge_pages` | 58 | 0.07% | 99.29% | 常驻 |
| 29 | `ask_user_question` | 57 | 0.07% | 99.36% | 常驻 |
| 30 | `spawn_teammate` | 53 | 0.07% | 99.43% | 常驻 |
| 31 | `team_task_list` | 43 | 0.05% | 99.48% | 表外 |
| 32 | `present` | 42 | 0.05% | 99.53% | 表外 |
| 33 | `schedule_update` | 39 | 0.05% | 99.58% | 表外 |
| 34 | `unlag_refresh` | 39 | 0.05% | 99.63% | 表外 |
| 35 | `job_list` | 37 | 0.05% | 99.68% | 表外 |
| 36 | `hindsight_ingest_document` | 35 | 0.04% | 99.72% | 表外 |
| 37 | `schedule_delete` | 27 | 0.03% | 99.75% | 表外 |
| 38 | `working_directory` | 23 | 0.03% | 99.78% | 表外 |
| 39 | `watch_status` | 18 | 0.02% | 99.80% | 表外 |
| 40 | `hindsight_capture_initiative` | 17 | 0.02% | 99.82% | 表外 |
| 41 | `repo_here` | 17 | 0.02% | 99.84% | 表外 |
| 42 | `schedule_list` | 17 | 0.02% | 99.87% | 表外 |
| 43 | `skill` | 16 | 0.02% | 99.89% | 表外 |
| 44 | `load_workspace_dependencies` | 10 | 0.01% | 99.90% | 表外 |
| 45 | `watch_start` | 9 | 0.01% | 99.91% | 表外 |
| 46 | `hindsight_read_knowledge_page` | 8 | 0.01% | 99.92% | 表外 |
| 47 | `watch_doctor` | 8 | 0.01% | 99.93% | 表外 |
| 48 | `interrupt_agent` | 5 | 0.01% | 99.93% | 表外 |
| 49 | `watch_stop` | 5 | 0.01% | 99.94% | 表外 |
| 50 | `watch_ack` | 3 | 0.00% | 99.94% | 表外 |

表外（27 个，只在系统提示的目录里留名字+一句话）：

- `enter_plan_mode`
- `exec_literal`
- `exit_plan_mode`
- `hindsight_capture_initiative`
- `hindsight_ingest_document`
- `hindsight_read_knowledge_page`
- `history_search`
- `interrupt_agent`
- `job_list`
- `load_workspace_dependencies`
- `now_line`
- `plugin`
- `present`
- `repo_here`
- `schedule_delete`
- `schedule_list`
- `schedule_update`
- `skill`
- `team_task_list`
- `unlag_refresh`
- `watch_ack`
- `watch_doctor`
- `watch_start`
- `watch_status`
- `watch_stop`
- `workflow`
- `working_directory`

表字节（名字+描述+参数，真正发给模型的那部分）：全表 16877，常驻 9697，表外 7180。

