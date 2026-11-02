# 20 · DSH 工具表（本机实测）

## 来源与方法

| 项 | 值 |
|---|---|
| 数据源 | `E:/Documents/deepseek-harness/default-workspace/tmp/big-session.jsonl`（53 MB，21,630 行） |
| 取样规则 | 全量扫描 207 条 `type=="request/header"` 事件，取**最后一条**（第 21540 行）的 `data.header.tools`，原样落盘 |
| 落盘 | `research/20-dsh-tool-schemas.json`（61 个工具，54,936 字符，72,040 字节，JSON 可解析） |
| 字符数 | 每个工具自身 `JSON.stringify(tool).length` |
| token | **实测**（非估算系数）：对每个工具单独发一次 `POST http://127.0.0.1:3425/v1/chat/completions`（`max_tokens:1`，取 `usage.prompt_tokens`）减去空消息基线 31 token |
| 校验 | Σ逐工具 = **12,209** token；整数组一次投喂 = 12,278 token（差 69 = 拼接时的结构开销）。比例 4.47 字符/token |
| 参数个数 | `parameters.properties` 键数；"必填"= `parameters.required` 长度 |

### 与任务书"59 个工具"的口径差异（已核对）

会话中实际出现 **4 种**工具集变体（同名工具集按名称序列去重）：

| 变体 | 出现次数 | 首次/末次行号 | 相对全集缺哪些 |
|---|---:|---|---|
| **61 个（最新/全集）** | 187 | 3560..**21540** | — |
| 60 个 | 4 | 387..840 | `watch_doctor` |
| **59 个**（任务书口径） | 2 | **16..94** | `watch_ack`、`watch_doctor` |
| 54 个 | 14 | 1179..3503 | `github`、`repo_here`、`watch_*`（6 个） |

即：59 是**会话开头的子集**，`watch_*` / `github` 系列是随插件加载后追加注册的。按任务书"取最新一次"落盘 → 61 个，且 54/59/60 都是它的子集（`onlyHere=[]`）。工具表是**动态可增删**的，不是编译期常量。

## 全表（按 JSON 内顺序，字母序）

| 工具 | 字符 | token | 参数 | 必填 | 一句话用途 |
|---|---:|---:|---:|---:|---|
| ask_user_question | 1249 | 267 | 1 | 1 | 向用户提问（带 options 可选项），一个 id 可含多问 |
| bash | 5296 | 1155 | 7 | 2 | `bash -c` 执行命令并回 stdout/stderr；含超时/工作目录/后台/沙箱/豁免理由 |
| create_goal | 577 | 114 | 2 | 1 | 创建持久 goal，使会话跨自动续轮持续工作 |
| edit | 1151 | 255 | 6 | 3 | 按字面串替换编辑 UTF-8 文本文件 |
| exit_plan_mode | 472 | 107 | 1 | 1 | 仅 plan 模式：提交计划供批准并退出 plan 模式 |
| get_goal | 170 | 36 | 0 | 0 | 读当前 goal（含 `update_goal` 需要的 id/revision） |
| gitbash_native | 3661 | 793 | 10 | 3 | 直投 Windows .exe 的精确 argv（不做 MSYS 转换），可声明 stdout/stderr 编码 |
| gitbash_to_native_path | 1866 | 404 | 1 | 1 | MSYS 路径 → Windows 原生路径的字面转换 |
| github | 583 | 135 | 4 | 1 | 调 GitHub REST API（凭据留在插件内，写操作需 `confirm:true`） |
| glob | 750 | 171 | 2 | 1 | 按路径 glob 找**文件**（含隐藏/ignored），按 mtime 排序 |
| grep | 702 | 158 | 3 | 1 | ripgrep 正则搜文件内容，返回行号 |
| hindsight_capture_initiative | 1753 | 399 | 3 | 2 | 把新特性/举措登记为知识页（计划变更时带 `relates_to_page_id` 复用同页） |
| hindsight_diagnose | 397 | 82 | 0 | 0 | 输出 Hindsight 运行时诊断（bank/workspace/harness/endpoint），不含密钥 |
| hindsight_ingest_document | 762 | 157 | 2 | 2 | 把外部文档/长期笔记写入仓库记忆；也是"纠错"机制 |
| hindsight_list_knowledge_pages | 622 | 127 | 0 | 0 | 列出仓库知识页 id/标题/一句话摘要 |
| hindsight_list_tags | 1123 | 266 | 3 | 0 | 列 bank 内真实存在的 tag 及计数（可 prefix 过滤） |
| hindsight_read_chunk | 780 | 171 | 1 | 1 | 按 chunk_id 读记忆的**原文**（逐字层） |
| hindsight_read_knowledge_page | 650 | 143 | 1 | 1 | 按 id 读整页知识 |
| hindsight_read_memory | 772 | 171 | 1 | 1 | 按 id 读单条记忆全文（含 recall 不显示的字段） |
| hindsight_read_memory_chain | 1254 | 320 | 3 | 1 | 按单个 tag 读整族记忆，绕开 recall 的 5 条截断 |
| hindsight_read_source | 858 | 177 | 1 | 1 | 沿 consolidated 观察的 source 跳转取原始文本 |
| hindsight_reflect | 723 | 155 | 1 | 1 | 对全量记忆做 agentic 综合，回答"为什么" |
| hindsight_search_knowledge_pages | 1133 | 239 | 1 | 1 | 知识页混合检索（全文+语义） |
| hindsight_sync_status | 455 | 98 | 0 | 0 | 报告记忆库同步状态（gitlog seed / 深化 / 抽取） |
| interrupt_agent | 302 | 67 | 1 | 1 | 打断某 teammate 当前轮次，保留其 pending inbox |
| job_kill | 370 | 79 | 2 | 1 | 请求取消后台 job |
| job_list | 168 | 37 | 0 | 0 | 列后台 job（id/kind/状态） |
| job_output | 615 | 134 | 3 | 1 | 读后台 job 输出；`wait` 阻塞至完成或超时 |
| list_agents | 285 | 54 | 0 | 0 | 列 Lead 与全部 teammate 及可寻址状态 |
| load_workspace_dependencies | 632 | 133 | 0 | 0 | 取捆绑 Python/库目录及 Node/pnpm 路径 |
| present | 777 | 162 | 1 | 1 | 声明已有文件为最终交付物（不改内容，只登记） |
| read | 438 | 105 | 3 | 1 | 读 UTF-8 文本文件，返回带行号内容 |
| read_image | 389 | 88 | 1 | 1 | 读 PNG/JPEG/WebP/GIF 并返回图像本身（大图自动降采样） |
| repo_here | 198 | 41 | 0 | 0 | 报告当前目录的 GitHub 仓库（owner/name、默认分支、可见性） |
| schedule_create | 3143 | 741 | 8 | 2 | 建本会话提醒；6 种定时参数只能给一个 |
| schedule_delete | 295 | 65 | 1 | 1 | 删除提醒（已入队的消息不撤回） |
| schedule_list | 137 | 28 | 0 | 0 | 列本会话活跃提醒 |
| schedule_update | 2403 | 595 | 8 | 1 | 原地改提醒（保 id），定时参数最多给一个 |
| send_message | 461 | 96 | 2 | 2 | 给另一 Team 成员发一条消息（运行中则最近 step 收到） |
| skill | 332 | 69 | 1 | 1 | 加载 skill 全文指令 |
| spawn_teammate | 625 | 136 | 4 | 3 | 创建具名持久 teammate（仅 Lead 可调） |
| subagent | 1161 | 237 | 3 | 2 | 派独立子代理（自有上下文，只回结果，不回过程） |
| subagent_fork | 1268 | 260 | 3 | 2 | 派继承本会话上下文的子代理 |
| team_task_create | 613 | 130 | 4 | 2 | 在共享任务板建一条无主 pending 任务 |
| team_task_get | 252 | 54 | 1 | 1 | 读某任务最新完整值（改前必读） |
| team_task_list | 704 | 160 | 5 | 0 | 列共享任务：就绪度/属主/revision/阻塞/写域告警 |
| team_task_update | 1088 | 245 | 8 | 3 | 以 `expected_revision` 做 CAS 更新任务 |
| todo_write | 856 | 195 | 1 | 1 | 记录/更新任务清单 |
| unlag_refresh | 196 | 54 | 1 | 0 | 本机第三方插件：按一下刷新按钮 |
| update_goal | 984 | 214 | 6 | 3 | 更新 goal（edit/pause/resume/complete/blocked） |
| wait_agent | 469 | 98 | 1 | 0 | 等待 teammate 状态/消息/任务板变化（短超时） |
| watch_ack | 594 | 156 | 4 | 0 | 标记 issue/PR 已处理：打 ack label + 推进本地水位 |
| watch_doctor | 297 | 67 | 1 | 0 | 只读自检 watcher（gh 登录/仓库权限/ack label/隔离目录） |
| watch_start | 1526 | 371 | 13 | 0 | 观察 GitHub 仓库并在 issue/PR 变化时唤醒本会话 |
| watch_status | 162 | 35 | 0 | 0 | 只读：列活跃 watcher 及上次轮询/唤醒/错误 |
| watch_stop | 207 | 46 | 1 | 1 | 停止观察某仓库 |
| web_fetch | 242 | 58 | 1 | 1 | 抓取指定 HTTP(S) URL 并解码为文本 |
| web_search | 323 | 71 | 1 | 1 | 联网搜索，返回摘要答案 + 来源 URL 列表 |
| workflow | 3389 | 769 | 4 | 2 | 写 JS 脚本编排大规模子代理（fan-out/phase/结构化结果） |
| working_directory | 360 | 67 | 1 | 0 | 读或 `cd` 当前工作目录 |
| write | 854 | 192 | 4 | 2 | 创建或整体替换 UTF-8 文本文件 |

## 给 Go 宿主的 3 条要点

1. **工具表是缓存前缀，逐字节稳定比大小更重要**：61 个工具 = 12.2 k token / 请求。同一会话内只要 schema 不变，前缀可复用；任何字段顺序抖动都会打断前缀缓存。`request/header` 快照（见 21 篇）就是为此存在的。
2. **成本高度不均**：`bash`(1155) + `gitbash_native`(793) + `workflow`(769) + `schedule_create`(741) + `schedule_update`(595) 五个占 4,053 token（33%）。`schedule_*` 的 741/595 token 几乎全花在"6 种定时参数互斥"的说明文字上——Go 端如果要压缩工具表，先压这里。
3. **形态是 `{name, description, parameters}`（OpenAI function 风格），不是 Anthropic `input_schema`**：字段只有 3 个，没有 `strict`/`additionalProperties:false` 之类的可选项；`parameters` 是完整 JSON Schema。Go 端别照 Anthropic 的形状定义结构体。
