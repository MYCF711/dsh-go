# DSH 59 工具 ↔ dsc 21 插件 · 能力覆盖对照

> 生成时间：2026-11-02 14:2x
> DSH 侧数据：本机真实会话 `request/header` 里下发的 59 个工具（`tmp/dsh-tool-names.txt`）
> dsc 侧数据：`README.md`（61 KB）+ GitHub 文件树（3,569 文件）+ `plugins/tool-filesystem/main.go`、`core/tool_registry.go`
> **未逐条运行验证**，⚠️ 项需要实测确认。

---

## 一、DSH 官方工具（29 个）

| # | DSH 工具 | dsc 对应 | 状态 |
|---:|---|---|---|
| 1 | `bash` | `tool-filesystem` → `shell`（mvdan POSIX 解释器，coreutils 进程内实现） | ✅ |
| 2 | `read` | `str_replace_editor` 的 `view` / `shell cat` | ⚠️ 形态不同 |
| 3 | `write` | `str_replace_editor` 的 `create` | ⚠️ 形态不同 |
| 4 | `edit` | `str_replace_editor` 的 `str_replace` / `insert` | ⚠️ 形态不同 |
| 5 | `glob` | `shell` 内建 `find`（进程内，GNU 子集） | ⚠️ 无独立工具 |
| 6 | `grep` | `shell` 内建 `grep`（进程内，-r/-n/-i/…） | ⚠️ 无独立工具 |
| 7 | `read_image` | `core/read_image_tool.go` | ✅ |
| 8 | `web_fetch` | `tool-browser-use` | ✅ |
| 9 | `web_search` | `tool-browser-use` | ✅ |
| 10 | `subagent` | `core/subagent.go` | ✅ |
| 11 | `workflow` | `workflow/`（go-lua 多 Agent 编排） | ✅ |
| 12 | `job_output` | `core/jobs.go` | ✅ |
| 13 | `job_kill` | `core/jobs.go` | ✅ |
| 14 | `job_list` | `core/jobs.go` | ✅ |
| 15 | `skill` | `dsc-system` → `skill` / `install_skill` / `uninstall_skill` | ✅ |
| 16 | `present` | — | ❌ |
| 17 | `schedule_create` | `core/cron_tools.go` | ✅ 名称不同 |
| 18 | `schedule_delete` | 同上 | ✅ |
| 19 | `schedule_list` | 同上 | ✅ |
| 20 | `schedule_update` | 同上 | ✅ |
| 21 | `create_goal` | `session/goal.go` + `agent-react-loop/plan_goal.go` | ✅ |
| 22 | `get_goal` | 同上 | ✅ |
| 23 | `update_goal` | 同上 | ✅ |
| 24 | `todo_write` | `session/todo.go` + `tui/todopanel.go` | ✅ |
| 25 | `exit_plan_mode` | `session/plan.go` | ✅ |
| 26 | `working_directory` | —（用 `workdir` 参数替代） | ❌ |
| 27 | `load_workspace_dependencies` | — | ❌ |
| 28 | `repo_here` | — | ❌ |
| 29 | `github` | — | ❌ |

**小计：19 ✅ / 5 ⚠️ / 5 ❌**

---

## 二、你装的插件贡献的工具（30 个）

| 来源 | 工具数 | dsc 对应 | 状态 |
|---|---:|---|---|
| `hindsight-coding-agents` | 13 | `tool-memory-service`（SQLite 记忆，语义不同） | ❌ 需 sidecar |
| `dsh-experimental-agent-team` | 9 | `core/agent_team.go` | ⚠️ 有 Go 对应，工具名待实测 |
| `dsh-unlag`（watch_*） | 5 | — | ❌ |
| `dsh-gitbash-shell` | 2 | —（dsc 的 shell 原生 POSIX 语义，不需要转换层） | ❌ 不需要 |
| `dsh-unlag`（unlag_refresh） | 1 | — | ❌ |

**小计：0 ✅ / 1 ⚠️ / 4 组无对应**

---

## 三、按调用量算的真实覆盖

本机 13.8 小时会话的 3,002 次工具调用：

| 工具 | 次数 | 占比 | dsc |
|---|---:|---:|---|
| `bash` | 2,513 | 83.7% | ✅ |
| `job_output` | 261 | 8.7% | ✅ |
| `write` | 74 | 2.5% | ⚠️（`str_replace_editor create`） |
| `edit` | 42 | 1.4% | ⚠️（`str_replace_editor str_replace`） |
| `read` | 40 | 1.3% | ⚠️（`view`） |
| 其余 54 个 | 72 | 2.4% | 见上表 |

**前 5 个 = 97.6%，dsc 全部有等价能力。**

---

## 四、缺口清单与补齐成本

| 缺口 | 影响 | 补法 | 估算 |
|---|---|---|---|
| `read`/`write`/`edit`/`glob`/`grep` 不是独立工具 | 模型体验差异：结构和 token 效率 | 包一层 thin tool 复用现有实现 | 200–400 行 Go |
| `present` | 交付物展示 | 写个工具 + TUI 卡片 | 100–200 行 |
| `repo_here` / `github` / `load_workspace_dependencies` | DSH 生态便利工具 | 或直接用 MCP server | 各 50–150 行 |
| `working_directory` | 会话级 cwd | dsc 用 `workdir` 参数，可接受 | 0 |
| hindsight 13 个工具 | 你的记忆插件 | 做成 MCP sidecar（Go SDK 现成） | 1–3 天 |
| unlag/watch 5 个 | 轮询守护 | 重写或丢弃 | 看需求 |
| team 9 个 | 多 agent 协作 | `core/agent_team.go` 已有，需核对工具面 | 实测后定 |

---

## 五、结论

1. **dsc 覆盖了 DSH 官方 29 个工具中的 24 个**（19 直接 + 5 形态不同），缺的 5 个全是生态便利工具，不是核心能力。
2. **你 97.6% 的调用面完全覆盖。**
3. **真正的缺口是你的插件**（hindsight / watch / unlag），而它们本来就该走 sidecar。
4. **成本最低的下一步**：把 `read/write/edit/glob/grep` 包成独立工具（200–400 行），TUI/模型体验立刻接近 DSH。

---

## 附：置信度

| 结论 | 依据 | 置信度 |
|---|---|---|
| 官方 29 工具的归属分类 | 本机真实请求头 | 高 |
| dsc 有 shell / str_replace_editor / cron / goal / todo / subagent / workflow | README + 文件树 + main.go | 高 |
| dsc 的 `read/write/glob/grep` 形态 | README 描述 + `tool-filesystem/main.go` 只注册 `shell` | 中（需实测） |
| team 工具面对应 | 仅见 `core/agent_team.go` | 低（需实测） |
