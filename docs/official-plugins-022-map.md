# 官方 0.2.2-rc.1 base 97 项 → DSH-GO 宿主映射（HOST-PLUGIN-SYNC-022-MAP）

> 官方来源：`D:/DeepSeek Harness/Desktop-0.2.2-rc.1/resources/app.asar` → `dsh/node_modules/@deepseek-ai/dsh-base/cordis.patch.yml`。本次只读解包已逐 id 比对 `docs/official-plugins-0.2.2-rc.1.md`：两边均 97 项、集合与顺序全等（§八 原始输出）。
> 代码基线：`af4a49a` + 当前共享工作树。**只读审计**，未改产品/代码/PLAN/LEAD/UIPLAN。TUI 不归宿主，W167/W170 等 TUI 红不在本表。
> 判定口径：`DONE`＝宿主有真实 Go 实现且证据可复跑；`PARTIAL`＝有实现但缺子能力或形态不同（必须写差什么）；`MISSING`＝宿主没有（列实试 grep）；`N/A`＝纯 UI/非宿主。**只认宿主**（`cmd/dshd`、`internal/**`、`modules/**`、`codecs/**`），客户端 `cmd/dshtui` 与 `../dsh-ui` 不计 DONE。
> 官方默认 disabled 的 4 行（`tool-plugin-manager`、`fs-observation-policy`、`tool-executor-ptc`、Windows 上的 `bash-sandbox`）按**能力**映射，不按开关状态。

## 一、统计

| 状态 | 数量 | 占 97 |
|---|---:|---:|
| DONE | 37 | 38.1% |
| PARTIAL | 32 | 33.0% |
| MISSING | 28 | 28.9% |
| N/A | 0 | 0 |
| 合计 | 97 | 100% |

按域（DONE/PARTIAL/MISSING/N-A）：A 运行时装配 #1-16 `2/9/5/0`；B 持久化与模型 #17-32 `6/6/4/0`；C 执行与安全 #33-48 `4/3/9/0`；D 工具与命令 #49-64 `10/2/4/0`；E 派生与压缩 #65-80 `7/7/2/0`；F 工具/web/执行器 #81-97 `8/5/4/0`。
缺口集中面：**沙箱与 telemetry 整族为 0**（#37/38/40/41/42/43）、**统一 storage/投影缓存未建**（#33-36）、**命令层不在宿主**（#57/58/61）、**账号与登录态无**（#23/24/97）。

## 二、主表（97 行，完整）

| # | plugin id | 状态 | 证据（file:line / 命令） | 差什么 |
|---:|---|---|---|---|
| 1 | `tool-plugin-manager` | PARTIAL | `modules/tool/plugin/plugin.go:65`（list/enable/disable）、`cmd/dshd/main.go:119` | 工具面无 install/uninstall/update/restore（只在 HTTP/CLI），改完需重启 |
| 2 | `plugin-manager` | PARTIAL | `internal/pluginmgr/manager.go:134` Install、`cmd/dshd/serve_plugins.go:43` 8 方法表 | 只装本地 `dsh.local-plugin@1`；装完不装载、不执行入口 |
| 3 | `timer` | PARTIAL | `internal/transport/heartbeat.go:45`、`cmd/dshd/parentwatch.go:229` | 仅宿主内部 ad-hoc ticker，无面向模块、随 scope 释放的 timer 服务 |
| 4 | `hmr` | MISSING | grep `fsnotify\|hot reload\|hmr` = 0；`modules/tool/plugin/plugin.go:11` 自述无热重载 | 无文件/配置监听与重载队列 |
| 5 | `llm` | DONE | `internal/llm/client.go:60` Stream（SSE）、`internal/llm/compat.go:58` 形状协商 | — |
| 6 | `deepseek-llm-api-extensions` | PARTIAL | `internal/llm/compat.go:58`、`:198-201` | 只协商 3 个可选字段，无按 provider 的扩展字段面 |
| 7 | `session` | DONE | `internal/session/log.go:71` Append、`internal/session/surface.go:39` | — |
| 8 | `session-log-deepseek` | MISSING | grep `upload\|telemetry\|report_event\|ingest` = 0 | 无会话日志增量上报/水位 |
| 9 | `typert` | PARTIAL | `internal/transport/api.go:151` SessionMethods（编译期常量+快照） | 无运行时方法/类型描述符注册 |
| 10 | `typert-loader` | MISSING | grep `typert\|descriptor\|RegisterMethod` = 0 | 无挂载包描述注入层 |
| 11 | `typert-gateway` | PARTIAL | `internal/transport/server.go:366` handleAPI、`internal/transport/ws.go:61` | 无 RemoteStream 双向流、无按描述符参数校验 |
| 12 | `remote-access` | MISSING | 仅 launch token→cookie：`internal/transport/server.go:329` | 无 peer 概念/方法级 RBAC/远程分发 |
| 13 | `session-title` | PARTIAL | `internal/session/fork.go:150` AppendSessionTitle、`internal/transport/handlers_metadata_init.go:46` | 只有显式标题；`source=auto` 从未被写入 |
| 14 | `session-title-llm` | MISSING | grep `auto.?title\|titleLLM\|生成标题` = 0 | 无首条 prompt 生成标题 |
| 15 | `user-questions` | PARTIAL | `modules/tool/ask/ask.go:100` Exec + Stop | 无问答服务/应答通道，答案靠下一条用户消息 |
| 16 | `agent` | PARTIAL | `internal/agent/loop.go:192` Run | 无活体 agent 注册表与 `agent/*` 事件 |
| 17 | `plugin-package-inventory-deepseek` | PARTIAL | `internal/pluginmgr/types.go:18` + `dshd modules list` | 只有本地包清单+编译期模块清单，无官方包元数据树 |
| 18 | `agent-default-model` | DONE | `cmd/dshd/serve_settings.go:161`、`internal/llm/model_catalog.go:469` | effort 无默认字段（只有 flag/会话覆盖） |
| 19 | `jobs` | DONE | `internal/spi/exec.go:35` Job、`internal/transport/handlers_jobs.go:72` | — |
| 20 | `llm-retry` | MISSING | `internal/llm/client.go:67-108` 仅形状协商；`:106-108` 注释写明 429/5xx 只发一次 | 无退避重试、无 step 边界重放 |
| 21 | `config-editor` | PARTIAL | `internal/transport/handlers_config.go:23`、`cmd/dshd/serve_session_ops.go:410` | 只有扁平 key→string，无 profile patch 树与热应用 |
| 22 | `settings` | DONE | `internal/settingsform/settingsform.go:190` Describe + revision 409 | 命名空间写死 model/permission，无插件声明字段 |
| 23 | `authorization` | MISSING | grep `authoriz` → 仅 cookie 鉴权与 Bearer 头 | 无 human-guided 授权流注册表 |
| 24 | `deepseek-account` | MISSING | grep `account\|oauth\|login\|device_code` → 仅本地 key | 无浏览器登录/五头/grant 存取 |
| 25 | `credentials` | PARTIAL | `internal/llm/model_catalog.go:245-251`（stored > env） | 凭据只挂模型目录 `apiKeyEnv`，无通用库/四级优先级 |
| 26 | `llm-pi-ai` | MISSING | `internal/llm/model_catalog.go:480` 只收 `openai-completions` | 无多 provider 适配器注册表 |
| 27 | `session-persistence-jsonl` | DONE | `codecs/v4/frame.go:7` 多帧 zstd、`internal/session/log.go:38` | 只 v4 一代；无 `compression:none`/v1-v3 迁移 |
| 28 | `attachment-local` | DONE | `cmd/dshd/attachments.go:124` Put、`:493` Get | 无内容寻址去重（同字节存两份） |
| 29 | `session-query-sqlite` | PARTIAL | `cmd/dshd/history.go:40` 子串线性扫描；`go.mod` 无 sqlite | 无 FTS5 索引/跨会话检索/游标分页 |
| 30 | `working-directory` | PARTIAL | `internal/spi/exec.go:13` CwdFollower、`modules/exec/bash/service.go:45` | cwd 变更不是耐久事件（重放不可见）；单工作区 |
| 31 | `tool-working-directory` | DONE | `modules/tool/wd/wd.go:33` Name | — |
| 32 | `session-projection` | PARTIAL | `internal/session/surface.go:39` Surface | 无投影注册表/snapshot seq/变更通知 |
| 33 | `storage` | PARTIAL | `internal/settingsform/settingsform.go:61` Store 注入 + 各功能自存 | 无统一 KV SPI（backend 注册/稳定错误码） |
| 34 | `storage-json` | PARTIAL | `internal/schedule/mutations.go:236` 原子 JSON | 无具名 backend/两种布局/version 戳 |
| 35 | `storage-domain` | MISSING | grep `defineDomain\|domainTable\|domain/changed` = 0 | 无 schema 校验的 KV 域 |
| 36 | `session-projection-cache` | MISSING | grep `cache` in `internal/session` = 0；`internal/session/list.go:43` 读整份日志 | 无持久投影缓存/冷 fold 续跑 |
| 37 | `otel` | MISSING | grep `otel\|otlp\|opentelemetry` = 0 | 无 span/metric 通道 |
| 38 | `session-telemetry-otel` | MISSING | grep `telemetry` = 0；无 `/metrics` 路由 | 无日志上报通道（FEEDBACK_ONLY/批次/结算） |
| 39 | `subprocess` | DONE | `internal/spi/exec.go:17` Exec、`modules/exec/bash/process_windows.go:16` Job Object | 无官方 detached spawn helper 协议 |
| 40 | `sandbox` | MISSING | grep `sandbox\|confine\|chroot` → 仅 3 条注释 | 无平台 runner（只读/工作区/全权档） |
| 41 | `sandbox-policy` | MISSING | 同上；`cmd/dshd/main.go:507` 的 `--policy` 只是审批 | 无沙箱模式 + workspace root 注入 |
| 42 | `bash-sandbox` | MISSING | `modules/exec/bash/session.go:80` 直起 bash，零 confinement | 无命令沙箱（上游 Windows 亦 disabled） |
| 43 | `pwsh-sandbox` | MISSING | `modules/tool/pwsh/pwsh.go:31-43` schema 无 sandbox 字段 | 无命令沙箱（上游 Windows 启用） |
| 44 | `approval` | DONE | `cmd/dshd/serve_approval.go:28` broker、`internal/approval/store.go:39` | 形态是 broker+journal，非 answerer waterfall |
| 45 | `permission` | PARTIAL | `internal/modes/modes.go:12` + `cmd/dshd/serve_permission_override.go:21` | preset 只捆 approval 半档，缺 sandbox 模式一侧 |
| 46 | `shell-env` | MISSING | `modules/exec/bash/session.go:80` 无 `c.Env`；grep `DSH_SHELL` = 0 | 无逐调用 env 注入/贡献者注册 |
| 47 | `tool-bash` | DONE | `modules/exec/bash/tools.go:65`、`:48-57` schema | 无 `sandbox_permissions` 升级字段 |
| 48 | `tool-pwsh` | DONE | `modules/tool/pwsh/pwsh.go:57`；`dshd tools` 含 `pwsh` | 无包内测试（仅 CLI 证据） |
| 49 | `tool-jobs` | DONE | `modules/exec/bash/tools.go:210`、`:300`、`:338`（job_output/kill/list） | — |
| 50 | `fs-observation-policy` | DONE | `modules/fs/local/local.go:222` CheckWritable、`:195` Read 登记 | 用 mtime+size 非 hash；观测账实例级 |
| 51 | `tool-fs` | DONE | `modules/tool/read/read.go:38`、`write.go:31`、`edit.go:34` | — |
| 52 | `tool-fs-search` | DONE | `modules/tool/glob/glob.go:42`、`modules/tool/grep/grep.go:52` | — |
| 53 | `agent-instructions` | PARTIAL | `internal/prompt/prompt.go:140` WorkspaceRules、`:45-46` 8000 | 只有全局+工作区两级；缺项目根/`.local` 链，预算 8000≠65536 |
| 54 | `skill` | PARTIAL | 只有 `modules/tool/skill/skill.go:54` 工具形态 | 无 skill 服务/注册表；技能目录不进提示 |
| 55 | `skill-filesystem` | DONE | `modules/tool/skill/skill.go:166` roots（多根+`DSHD_SKILLS_EXTRA`） | 只解析 description 一个 frontmatter 字段 |
| 56 | `tool-skill` | DONE | `modules/tool/skill/skill.go:54` Exec、`:67` list 分支 | — |
| 57 | `commands` | MISSING | 宿主 grep `RegisterCommand\|CommandRegistry` = 0；`cmd/dshtui/app.go:1151` 是客户端硬编码 | 宿主无命令注册表（客户端实现不计 DONE） |
| 58 | `command-feedback` | MISSING | grep `feedback` = 0 | 无命令反馈事件 |
| 59 | `goal` | DONE | `modules/tool/goal/goal.go:176` Name、revision 乐观锁 | — |
| 60 | `goal-round-driver` | MISSING | `RoundsStarted` 只持久化：`cmd/dshd/serve_goal_override.go:200`，从无自增 | 无 turn 后按 active goal 自动续轮 |
| 61 | `command-goal` | MISSING | grep `"/goal"` = 0 | 无 `/goal` 命令 |
| 62 | `plan-mode` | DONE | `modules/tool/plan/plan.go:164` enter_plan_mode、`:132` gate | — |
| 63 | `token-meter` | DONE | `internal/session/tokens.go:21` Observe、`cmd/dshd/context_usage.go:17` | — |
| 64 | `compaction-basic` | DONE | `internal/compact/compact.go:66` Plan、`cmd/dshd/compactauto.go:34` | — |
| 65 | `command-compact` | PARTIAL | `internal/transport/api.go:37` `session.compact`、`cmd/dshd/compactauto.go:34` | 宿主无命令层；`/compact` 只在 TUI 本地 |
| 66 | `subagent` | DONE | `cmd/dshd/subagent.go:44` RunSub | 同步语义，无“立刻返回 id、完成回传” |
| 67 | `subagent-spawn-in-process` | DONE | `cmd/dshd/subagent.go:88` 同进程 `agent.Loop` | 单一半径，无 spawn/fork 双后端 |
| 68 | `subagent-fork-in-process` | PARTIAL | `cmd/dshd/subagent_inherit.go:20` inheritKeep=20 | 只把父会话 20 条 surface 转纯文本，非真 fork、不可续 |
| 69 | `tool-subagent-control` | PARTIAL | `modules/tool/team/spawn.go:211` interrupt_agent（花名册打标记） | 缺在跑子代理的 list/cancel/wait |
| 70 | `tool-subagent` | DONE | `modules/tool/subagent/subagent.go:33` Name | 同 #66 |
| 71 | `tool-subagent-fork` | DONE | `modules/tool/subagentfork/subagentfork.go:38` | 无独立测试文件 |
| 72 | `ptc-runtime` | MISSING | `go.mod` 无 JS 引擎；grep `ptc` 仅注释 | 无程序化工具调用运行时（R6 明确不嵌 JS） |
| 73 | `workflow-ptc` | PARTIAL | `modules/tool/workflow/workflow.go:5-13`（script 是 JSON 步骤表） | 非 JS、无 `agent()` 钩子/并发扇出 |
| 74 | `tool-workflow` | DONE | `modules/tool/workflow/workflow.go:45` Name、`:40` schema | 脚本语言与官方不对齐 |
| 75 | `timeout-policy` | PARTIAL | `modules/tools/pipeline.go:22-23` 无 deadline；超时由各工具自带 | 无统一 per-call deadline 策略层 |
| 76 | `spill-local` | DONE | `modules/tools/pipeline.go:203` spill、`cmd/dshd/main.go:98` SpillDir | 按字符非 token；home 级共享目录 |
| 77 | `spill-policy` | PARTIAL | 同 #76；`internal/spi/tool.go:80` Artifact | 无 token 预算、无图文共享预算，图片不走 spill |
| 78 | `session-checkpoint-policy` | PARTIAL | `cmd/dshd/serve_session_ops.go:129` captureRewindCheckpoint | 只有 rewind 文件快照，非请求前 durability checkpoint |
| 79 | `tool-result-pruner` | DONE | `internal/prune/prune.go:67` Apply | 只裁 Text，不裁 Data/View |
| 80 | `image-offload` | MISSING | grep `offload` = 0；图片一律内联 `internal/spi/tool.go:61` | 无超图预算降级/占位替换 |
| 81 | `tool-todo` | DONE | `modules/tool/todo/todo.go:70` Name | — |
| 82 | `tool-goal` | DONE | `modules/tool/goal/goal.go:176` | — |
| 83 | `repeat-tool-reminder` | MISSING | grep `repeat\|reminder\|duplicate` 生产代码 = 0 | 无重复调用检测/提醒注入 |
| 84 | `web` | PARTIAL | `internal/spi/search.go:13` + `modules/search/search.go:123` | 只有搜索侧抽象，无 fetch provider SPI |
| 85 | `web-search-deepseek` | PARTIAL | `modules/search/search.go:37`（需 `DSHD_SEARCH_URL`） | 无 DeepSeek 原生搜索端点/响应形状 |
| 86 | `web-deepseek-api` | MISSING | grep `deepseek` in web 三包 = 0 | 无 DeepSeek Web API 客户端 |
| 87 | `web-fetch-http` | DONE | `modules/tool/webfetch/webfetch.go:43`、`:106` 512KiB 上限 | 无 SSRF 目的地址校验/连接固定 |
| 88 | `tool-web` | DONE | `cmd/dshd/main.go:122-123`；`dshd tools` 含 `web_fetch`/`web_search` | — |
| 89 | `mcp-resources` | MISSING | `cmd/dshd/mcp.go:325-345` 只有 initialize + tools/list | 无 resources/prompts；MCP 工具也未进注册表（只计数） |
| 90 | `tools` | DONE | `modules/tools/registry.go:36` Register、`modules/tools/pipeline.go:64` Run | — |
| 91 | `tool-executor-native` | DONE | `modules/tools/pipeline.go:64`（pre→exec→spill→post→广播） | 唯一形态，不可切换 executor |
| 92 | `tool-executor-ptc` | PARTIAL | `modules/tool/workflow/workflow.go:87` + `internal/spi/ctx.go:62` SubDispatch | 无代码执行器/`run_code` |
| 93 | `system-prompt` | PARTIAL | `internal/prompt/prompt.go:49` Assemble、`:182` Default | 无 section 注册/order/persona/toolOrder 占位 |
| 94 | `agent-loop` | DONE | `internal/agent/loop.go:192` Run（turn/step/steer） | — |
| 95 | `fs-sandbox` | PARTIAL | `modules/fs/local/local.go:111` within、`:125` withinWrite | 边界硬编码在 fs 内，无独立/可配 sandbox 模块 |
| 96 | `llm-deepseek` | DONE | `internal/llm/client.go:121` Bearer、`internal/llm/model_catalog.go:469` ResolveProvider | 通用 OpenAI 兼容，非 DeepSeek 专属 wire |
| 97 | `llm-deepseek-account` | MISSING | grep `account\|oauth` in `internal/llm`+`cmd/dshd` = 0 | 只有静态 api key，无账号态 |

## 三、DONE（37，可复核）

证据即主表第 4 列；复核命令见 §八（`go test` 全仓 `TEST_EXIT=0 / OK_PKGS=51 / FAIL_PKGS=0`）。
A `2`：#5 llm、#7 session。B `6`：#18 agent-default-model、#19 jobs、#22 settings、#27 session-persistence-jsonl、#28 attachment-local、#31 tool-working-directory。
C `4`：#39 subprocess、#44 approval、#47 tool-bash、#48 tool-pwsh。D `10`：#49 tool-jobs、#50 fs-observation-policy、#51 tool-fs、#52 tool-fs-search、#55 skill-filesystem、#56 tool-skill、#59 goal、#62 plan-mode、#63 token-meter、#64 compaction-basic。
E `7`：#66 subagent、#67 subagent-spawn-in-process、#70 tool-subagent、#71 tool-subagent-fork、#74 tool-workflow、#76 spill-local、#79 tool-result-pruner。
F `8`：#81 tool-todo、#82 tool-goal、#87 web-fetch-http、#88 tool-web、#90 tools、#91 tool-executor-native、#94 agent-loop、#96 llm-deepseek。

## 四、PARTIAL（32，按缺口形态归并；逐项差异见主表第 5 列）

| 缺口形态 | id | 一句话 |
|---|---|---|
| 只有工具/单点，无服务与注册表 | 1,2,15,16,54,69 | plugin 工具只 list/enable/disable；ask 无应答通道；agent 无活体注册表；skill 无 provider；子代理控制只在花名册打标记 |
| 注册表/描述符/统一层缺失 | 9,11,17,21,25,29,30,32,33,34 | typert 是编译期常量；config 只扁平 kv；凭据只挂 apiKeyEnv；history 只子串扫描；cwd 非耐久事件；storage/投影无统一 SPI |
| 预算/语义口径不同 | 53,75,77,78,95 | AGENTS 两级 8000；无 per-call deadline；spill 按字符非 token；checkpoint 是 rewind 快照；fs 边界硬编码 |
| 形态不同但主体在 | 3,6,13,45,65,68,73,84,85,92,93 | 只有内部 ticker；compat 只 3 字段；标题只显式；权限只 approval 半档；`/compact` 只有 RPC；fork 只文本继承；workflow 是 JSON；web 只有搜索侧；无 DeepSeek 搜索；PTC 只声明式；prompt 无 section 注册 |

## 五、MISSING（28，按 P0/P1/P2）

**P0（0 项）**：本表没有“不补就无法封版”的宿主项——GO.1 的阻断点在 TUI/PTY 侧（W163 等），不在宿主插件面。

| 优先级 | id | 缺口 | 影响 |
|---|---|---|---|
| P1（5） | 14 session-title-llm | 无首条 prompt 自动生成标题 | 会话列表只剩显式标题/首句，侧栏可读性 |
| P1 | 20 llm-retry | 429/5xx 只发一次，无退避 | 网关抖动直接变一轮失败，用户可见 |
| P1 | 41 sandbox-policy | 无沙箱模式 + workspace root 注入 | 权限 UI 只有 approval 半档，`read-only/workspace-write/danger` 无宿主真值 |
| P1 | 80 image-offload | 无超图预算降级 | 附件/图片一多，整轮请求失败而非降级 |
| P1 | 83 repeat-tool-reminder | 无重复调用检测 | 同一工具反复调用烧步数与成本，无提醒 |
| P2（23） | 4,8,10,12,23,24,26,35,36,37,38,40,42,43,46,57,58,60,61,72,86,89,97 | hmr、会话日志上报、typert-loader、remote-access、authorization、deepseek-account、llm-pi-ai、storage-domain、projection-cache、otel、telemetry、sandbox、bash/pwsh-sandbox、shell-env、commands、command-feedback、goal-round-driver、command-goal、ptc-runtime、web-deepseek-api、mcp-resources、llm-deepseek-account | 生态/观测/开发体验与架构欠账；不阻断当前封版 |

## 六、N/A（0 项）

0 项，并且不应有：base 97 全是宿主侧服务/工具，UI/TUI 插件不在 base 名单（`docs/official-plugins-0.2.2-rc.1.md` 首注）。`tool-bash`/`bash-sandbox` 在 Windows 上被官方 `disabled`，但那是**平台开关**不是“非宿主”，按能力仍映射（#42/#47）。

## 七、第一批同步建议（8 项，按宿主稳定性/GO.1 影响排序；**全部宿主侧，不改 TUI**）

| 序 | 项（现状） | 为什么先做 | 落点 | 验收 |
|---:|---|---|---|---|
| 1 | 20 llm-retry（MISSING） | 网关 429/5xx 直接让整轮失败，稳定性收益最大；纯 `internal/llm`，零界面影响 | `internal/llm/client.go` 外加重试器（仅幂等重放，退避+取消归还） | 先红：429 断言只请求 1 次；后绿：429→成功、取消不重试、v4 历史不重复 |
| 2 | 75 timeout-policy（PARTIAL） | 单个工具挂死会拖死整轮；现在只有 bash/web/MCP 各自超时 | `modules/tools/pipeline.go` 包一层 per-call deadline，统一映射成模型可见错误 | 定向：挂死假工具在 N 秒后返回 hint，turn 继续；取消不误报超时 |
| 3 | 80 image-offload（MISSING） | 附件链已落地（W79-W98），超图预算请求会硬失败 | surface 折叠处替换最旧图片为占位+指针，日志不动 | 先红：构造超预算请求看 4xx；后绿：请求成功且 v4 原文不变 |
| 4 | 83 repeat-tool-reminder（MISSING） | 同一工具连续调用烧步数/成本，官方阈值 `[3,5,8]` | `pipeline` post-execute 注入提醒 | 计数 3/5/8 各注入一次；不同工具/不同参数不累计 |
| 5 | 14 session-title-llm（MISSING） | 会话列表可读性；失败可回退不影响主链 | 首轮后一次 LLM 调用写 `session/title`（`source=auto`） | 失败回退不写；A/B 会话不串；`session/title` 事件可被 list 读到 |
| 6 | 78 session-checkpoint-policy（PARTIAL） | 已有 rewind 快照，补齐“请求前已落盘”语义才能说 durability | `cmd/dshd/serve_session_ops.go` 复用 capture，扩到模型请求前 | 断电/杀进程后重启，已发请求前的帧完整 |
| 7 | 41 sandbox-policy（MISSING） | 权限三档需要宿主真值，否则 UI 只能画半档 | 新增 policy 层：mode→(fs 写边界, approval)，注入 runtime context | 三档各自断言：只读不写盘、工作区外拒写、全权放行；`session.setPermission` 落真值 |
| 8 | 60 goal-round-driver（MISSING） | goal 存储与计数已在，只差驱动；补齐后 goal 才闭环 | turn 结束后按 active goal 推进 `RoundsStarted`，到上限/暂停停 | 上限 N 轮自停；pause 后不再推进；崩溃重启不重复计轮 |

不建议现在做（写清理由，避免被当成漏项）：`40 sandbox`/`42`/`43`（平台 runner，成本高且上游 Windows 关 bash-sandbox）、`57/58/61 commands`（命令层在 TUI，宿主补注册表要先定契约）、`37/38 otel`（产品无上报定位）、`72 ptc-runtime`（R6 禁 JS，官方 base 默认 disabled）。

## 八、原样证据（本次实跑，命令 + 原始输出）

```text
$ # 1) 官方 97 项核对（只读解 asar，不落盘、不写 tmp）
$ python - <<'PY' …（解析 app.asar header → dsh/node_modules/@deepseek-ai/dsh-base/cordis.patch.yml）
official id count: 97
doc id count: 97
only-official: []
only-doc: []
same order: True

$ # 2) 宿主装配面
$ ./dshd.exe modules list | tail -n +2 | wc -l
38
$ ./dshd.exe tools | grep -c '"name": "'
57
工具 57 个，29283 字节（形态 compact）        ← stderr

$ # 3) 全仓门禁
$ go build ./...            BUILD_EXIT=0
$ go vet ./...              VET_EXIT=0
$ go test ./... -count=1    TEST_EXIT=0  OK_PKGS=51  FAIL_PKGS=0  NOTEST=26
$ go list -deps ./internal/kernel | grep -c "dshgo/"
1

$ # 4) 定向包（DONE 行证据主体，第一批）
$ go test ./internal/session ./internal/compact ./internal/prune ./internal/pluginmgr ./modules/tools ./modules/tool/workflow ./modules/tool/skill ./modules/tool/goal ./modules/tool/plan ./modules/tool/todo ./internal/agent ./internal/prompt ./internal/approval ./internal/modes ./internal/settingsform -count=1
ok  	dshgo/internal/session	13.030s
ok  	dshgo/internal/compact	0.217s
ok  	dshgo/internal/prune	0.189s
ok  	dshgo/internal/pluginmgr	2.643s
ok  	dshgo/modules/tools	0.301s
ok  	dshgo/modules/tool/workflow	0.140s
ok  	dshgo/modules/tool/skill	0.519s
ok  	dshgo/modules/tool/goal	0.445s
ok  	dshgo/modules/tool/plan	0.519s
ok  	dshgo/modules/tool/todo	0.112s
ok  	dshgo/internal/agent	0.302s
ok  	dshgo/internal/prompt	0.597s
ok  	dshgo/internal/approval	0.468s
ok  	dshgo/internal/modes	0.468s
ok  	dshgo/internal/settingsform	0.244s
EXIT=0

$ # 5) 定向包（第二批）
$ go test ./cmd/dshd ./internal/transport ./internal/llm ./internal/schedule ./internal/spi ./modules/exec/bash ./modules/fs/local ./modules/tool/webfetch ./modules/tool/websearch ./modules/tool/subagent ./modules/tool/team ./modules/search ./modules/tool/read ./modules/tool/write ./modules/tool/edit -count=1
ok  	dshgo/cmd/dshd	129.707s
ok  	dshgo/internal/transport	1.193s
ok  	dshgo/internal/llm	1.259s
ok  	dshgo/internal/schedule	0.863s
ok  	dshgo/internal/spi	0.158s
ok  	dshgo/modules/exec/bash	7.873s
ok  	dshgo/modules/fs/local	0.873s
ok  	dshgo/modules/tool/webfetch	0.261s
ok  	dshgo/modules/tool/websearch	0.161s
ok  	dshgo/modules/tool/subagent	0.338s
ok  	dshgo/modules/tool/team	0.805s
?   	dshgo/modules/search	[no test files]
ok  	dshgo/modules/tool/read	0.644s
ok  	dshgo/modules/tool/write	0.603s
ok  	dshgo/modules/tool/edit	0.720s
DONE

$ # 6) 缺席项反证（0 = 生产代码无实现；命中文件数）
$ for k in otel telemetry offload resources/list RegisterCommand danger-full-access fsnotify; do printf "%-18s %s\n" "$k" "$(grep -rl "$k" --include=*.go internal cmd modules | wc -l)"; done
otel               0
telemetry          0
offload            0
resources/list     0
RegisterCommand    0
danger-full-access 0
fsnotify           0
# repeat 命中的 2 个文件均在 *_test.go（`repeated page`/`repeated separator`），生产代码为 0
```

## 九、未验与限制

1. 官方语义取自 asar 内 `cordis.patch.yml` 的逐行注释与包名；**未逐包读 README/源码**（本机 asar 内 `dsh/node_modules/...` 不能直接当目录读），因此“官方某插件是否还包含未写进注释的子能力”存在残余不确定。
2. `PARTIAL` 的“差什么”按能力面判定，不含官方内部实现细节（如 typert 的 Zod 工厂、settings 的 per-entry 表单 Schema）。
3. 本表只做宿主映射与缺口分级：**没有**给出工作量估算、没有改 LEAD/PLAN/UIPLAN、没有派单。
4. 两处动线外发现（未修，交 Lead 决定）：① `internal/spi/ctx.go:33` 与 `:69` 的 `runIDKey`/`subDispatchKey` 同为 `ctxKey(1)`，一旦同 ctx 设 run id，`SubDispatchFrom` 会退化成零值；② `cmd/dshd/mcp.go:341-368` 只统计 MCP 工具数量，**MCP 工具未注册进工具注册表**，模型实际调不到（与 #89 相邻，但属独立缺陷）。
