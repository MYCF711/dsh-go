# 真实语料里的工具使用频率

> 由 dshd 自己统计：171 个真实 v4 会话、33,408 次 tool/call、60 种工具（2026-11-03）。
> 采集脚本 `tmp/toolfreq.mjs`（它自己写的，用 node:zlib 解 zstd 后扫事件）。

## 我们已有 vs 我们缺

| 工具 | 次数 | dsh-go 有吗 |
|---|---:|---|
| `bash` | 26774 | ✅ |
| `read` | 1469 | ✅ |
| `edit` | 1221 | ✅ |
| `write` | 995 | ✅ |
| `job_output` | 697 | ✅ |
| `pwsh` | 250 | ❌ |
| `web_fetch` | 238 | ✅ |
| `send_message` | 194 | ❌ |
| `github` | 124 | ❌ |
| `read_image` | 116 | ✅ |
| `team_task_update` | 104 | ❌ |
| `wait_agent` | 101 | ❌ |
| `get_goal` | 95 | ❌ |
| `list_agents` | 87 | ❌ |
| `todo_write` | 78 | ✅ |
| `subagent` | 78 | ✅ |
| `team_task_get` | 76 | ❌ |
| `glob` | 75 | ✅ |
| `team_task_create` | 58 | ❌ |
| `grep` | 57 | ✅ |
| `job_kill` | 50 | ✅ |
| `spawn_teammate` | 44 | ✅（M79） |
| `unlag_refresh` | 39 | ❌ |
| `create_goal` | 37 | ❌ |
| `team_task_list` | 32 | ❌ |
| `update_goal` | 30 | ❌ |
| `ask_user_question` | 29 | ✅ |
| `web_search` | 28 | ✅ |
| `present` | 27 | ✅ |
| `hindsight_search_knowledge_pages` | 22 | ❌ |
| `working_directory` | 20 | ✅ |
| `schedule_create` | 15 | ❌ |
| `hindsight_ingest_document` | 15 | ❌ |
| `schedule_update` | 14 | ❌ |
| `watch_status` | 12 | ❌ |
| `repo_here` | 12 | ❌ |
| `schedule_list` | 11 | ❌ |
| `skill` | 11 | ❌ |
| `job_list` | 9 | ✅ |
| `watch_start` | 8 | ❌ |
| `watch_doctor` | 6 | ❌ |
| `hindsight_reflect` | 6 | ❌ |
| `hindsight_read_chunk` | 6 | ❌ |
| `hindsight_list_knowledge_pages` | 5 | ❌ |
| `hindsight_sync_status` | 4 | ❌ |
| `gitbash_native` | 4 | ❌ |
| `load_workspace_dependencies` | 4 | ❌ |
| `hindsight_diagnose` | 4 | ❌ |
| `subagent_fork` | 3 | ❌ |
| `watch_stop` | 3 | ❌ |
| `watch_ack` | 3 | ❌ |
| `gitbash_to_native_path` | 2 | ❌ |
| `schedule_delete` | 2 | ❌ |
| `hindsight_read_memory` | 2 | ❌ |
| `hindsight_capture_initiative` | 1 | ❌ |
| `bash_shell` | 1 | ❌ |
| `shell` | 1 | ❌ |
| `read_file` | 1 | ❌ |
| `hindsight_list_tags` | 1 | ❌ |
| `hindsight_read_memory_chain` | 1 | ❌ |
