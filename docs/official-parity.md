# 官方原生插件 vs dsh-go（对账）

> 依据：本机 `home/cordis.patch.yml` 里**实际启用的插件列表**（163 行，31 个 id）
> ＋ `"./DSH GO.exe" tools` 的 53 个工具。不是拍脑袋，是可复查的两份清单。

## 一、已经对上的（不用再做）

| 官方插件 | 能力 | dsh-go |
|---|---|---|
| `tool-bash` | 跑命令 | ✓ `bash` |
| `tool-pwsh` | PowerShell | ✓ `pwsh` |
| `tool-fs` | 读写改 | ✓ `read` / `write` / `edit` |
| `tool-fs-search` | 找文件、搜内容 | ✓ `glob` / `grep` |
| `tool-jobs` | 后台任务 | ✓ `job_output` / `job_kill` / `job_list` |
| `tool-schedule` | 定时任务 | ✓ 四个 `schedule_*` |
| `tool-skill` + `skill-filesystem` | 技能 | ✓ `skill`（刚补了"列清单"与多根） |
| `tool-goal` | 目标 | ✓ `get/create/update_goal` |
| `compaction` | 上下文压缩 | ✓ `internal/compact` |
| `delegation` + `tool-subagent` + `-fork` + `-list-agents` | 子代理 | ✓ `subagent` / `subagent_fork` / `list_agents` / `spawn_teammate` |
| `tool-ask-user` | 问人 | ✓ `ask_user_question` |
| `tool-todo` | 待办 | ✓ `todo_write` |
| `tool-web` | 抓网页 / 搜 | ✓ `web_fetch` / `web_search` |
| `present` | 交付文件 | ✓ `present` |

## 二、**真缺**的（官方有原生插件，我们没有）

> **2026-11-04 更新**：下面两项已补齐 —— 计划模式（`modules/tool/plan`，M306）
> 与工具结果裁剪（`internal/prune`，M307）。表格保留原始对账，状态以本节为准。

| 缺的 | 官方插件 | 影响 |
|---|---|---|
| ~~**计划模式**~~ ✅ 已补 | `planning` + `plan-mode` | M306 落地：状态按会话落盘，跨轮生效（serve 每轮是新子进程） |
| ~~**程序化工具调用**~~ ✅ 已补（有限形态） | `workflow-ptc` + `tool-workflow` | M312：`workflow` 工具，参数名对齐 DSH（`script`/`meta`），但 **script 内容是 JSON 步骤表，不是 JS**（本仓 R6 不嵌 JS 运行时）。子调用走 ctx 里的 `spi.SubDispatch`，照样过审批/闸门/spill |
| ~~**工具结果裁剪**~~ ✅ 已补 | `tool-result-pruner` | M307/M308：`internal/prune` 按字节/行预算裁，接在 `session.ProjectResult`，默认 16KB 高于 spill 最坏产出 |
| **斜杠命令** | `command-goal` / `command-compact` 等 | 宿主没有 `/compact`、`/resume`、`/new` 这类入口，只能靠自然语言 |
| ~~**插件管理**~~ ⚠️ 部分 | `tool-plugin-manager` / `tool-plugin-market` | M318：`plugin` 工具可 list/enable/disable **manifest 那层**（static 模块），改完下次启动生效；wasm/process 与"市场"仍缺。顺带修了 `enabled=false` 只解析不生效的老 bug |
| **棋谱压缩** | `gokifu-compact`（第三方，顶替了官方 compaction） | 那是你自己那套上下文系统。dsh-go 的压缩是通用摘要，不懂"棋谱/节点/死路" |

## 三、**有但对不齐**的

| 项 | 官方 | dsh-go | 差在哪 |
|---|---|---|---|
| `bash` 后台 | `run_in_background: true` 立刻转后台 | **只能等 `idle_seconds` 超时**才转 | 想要"起一个长任务马上继续干"，做不到 |
| 子代理控制 | `tool-subagent-control`（取消/中断在跑的子代理） | 只有 `interrupt_agent`（团队线，**打标记**） | 没有真正"让这个子代理停下"的手段 |
| 提示层 | `persona` / `agent-instructions` / `time-context` | 有 AGENTS.md 注入 + `now_line` | 没有 persona 注入、没有时间上下文注入 |

## 四、按我的判断排个序

1. **`run_in_background`** —— 半天的活，直接补上官方那套语义（现在只能靠超时转后台，很别扭）
2. **计划模式** —— "先方案后动手"是安全阀，尤其对有写权限的 agent
3. **工具结果裁剪** —— 长会话的钱都花在这；我们现在只有事后截断
4. **斜杠命令** —— TUI 一起做，成本会低很多
5. **workflow-ptc** —— 撬动最大，但它要一套"脚本宿主"的设计，不能顺手做
6. **插件管理/市场** —— 等 `wasm`/`process` 装载落地再谈，否则"管理"的是编译期模块

---

## 2026-11-04 追加：三层装载现在到哪了

| 层 | 状态 | 证据 |
|---|---|---|
| `static`（编译进二进制） | ✅ 一直在用 | 57 个工具、`dsh.toml` 白名单 |
| `wasm`（wazero） | 🟡 **能装载、不提供能力** | `backend = "wasm"` + `path` + 可选 `digest`；`modules graph` 单列一节；digest 不符启动即失败；`dshd wasm inspect/call` 可手动调；体检里也有一项 |
| `process`（外部进程插件） | ❌ 仍被明确拒绝 | manifest 写 `backend="process"` 会当场报错，不静默降级 |

wasm 还差的那一步很具体：**工具契约跨边界**（把 `Exec(args) → Result` 序列化进/出线性内存），
没有它，wasm 模块就只能是"能调用的函数"，变不成模型能用

### 补记（2026-11-04）：wasm 插件现在能当工具用

`provides = ["tool"]` / `["tool:<名>"]` 的 wasm 模块会注册成模型可见的工具；
一个模块可挂多个工具（`[[module.tool]]`），`call` 按信封里的 `tool` 字段分流。
trap / 超时后**自动重建实例**，不需要重启宿主。

不写 manifest 也能验一个插件：`dshd wasm tool --module x.wasm --tool echo --args '{"text":"hi"}'`。
