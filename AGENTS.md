# AGENTS.md · DSH-GO

> **本仓**：用 Go 重写 DeepSeek Harness（DSH）。上游基线 `0.2.1-rc.5`，本仓版本 **`0.2.1-go.27`**。
> 本文件是操作手册：冲突时以本文件为准；本文件没写的，以对应那条线的 PLAN 为准。

## 两条线（先认清在改哪条，否则一定改错地方）

| | **宿主线** | **界面线** |
|---|---|---|
| PLAN | `PLAN.md`（十项决策，用户逐条拍板，**不要再讨论**） | `UIPLAN.md`（Q1–Q12，**未拍板项不许开工**） |
| 目录 | `internal/` `modules/` `codecs/` `cmd/dshd/` | `internal/transport/` `ui/` `cmd/dshui/` |
| 语言 | Go | Go（契约 + 壳）+ TypeScript（界面） |
| 验收 | `scripts/check.sh`、`go test ./... -race`、P0 四条判据 | U0 的 12 个 method 快照 + U1「自己用它干一天活」 |
| 现状 | M0–M3 完成，P0 工具 18/18 | **只有规划，一行代码都没有** |

**两条线只在一个地方握手：`Transport` 契约**（HTTP `/api` + WS `remote.mux` + 一份 `ViewSpec`）。
宿主不 import 界面，界面不 import 宿主。**跨线改动 = 先改契约快照，再两边各改各的。**

> 官方壳的实测解剖在 `research/40-official-shell.md`，前端资产盘点在 `research/42-frontend-inventory.md`。
> 两条最容易搞错的认知，先记死：
> ① **官方的主界面不是 Electron**——它是纯 Web（`dsh-app://app/` → `dsh-web-frontend/dist`，`sandbox:true`），Electron 只是个 WebView 宿主；
> ② **GUI 不用 ACP**——官方 Web 面吃的是 v4 的 62 种原始事件 + journal 4 种增量 + 助手 3 种 chunk，ACP 只是同一底层的另一种投影。

---

## 0. 当前状态（2026-11-02）

- ✅ **M0 完成**：内核四件事（Scope/Registry/Bus/Assemble）+ 工具 SPI + 装配器 + CLI；`scripts/check.sh` 全绿，CI（windows-latest，含 `-race`）绿
- 🚧 **M1 收口**：工具层已齐（`read/write/edit/glob/grep/shell/job_*` 共 9 个），流水线 + 投影 + schema 快照 + token 实测（**906**）都已落地
- 🚧 **M2 进行中**：`codecs/v4` **双向可用** —— 解码吃了 116 个真实会话，编码产物 Node（`node:zlib` zstd）逐帧读通
- **M2 基本完成**：`dshd run` 端到端可跑 —— 模型 → 工具 → 结果回灌 → 落 v4 会话；Go 读回与 Node 读通都验过
- 🚧 **M3 进行中**：`dshd acp` 可用 —— `initialize`/`session/new`/`session/prompt`/`session/cancel` 通，4 种 update 流式（含 reasoning）
- ✅ **官方 ACP SDK 客户端已连上**（`scripts/acp-sdk-client.mjs`）：reasoning/text/tool_call 都通，stopReason=end_turn
- ✅ 审批链路：`write`/`edit`/`shell`/`job_kill` 走 `session/request_permission`；拒绝时模型能读懂并改招
- ✅ **M3 完成**：ACP + SDK 客户端 + 审批 + usage_update + 跨进程 `session/resume`
- ✅ 工具能力声明（`spi.Caps`，**必填**）：审批按 `NeedsApproval()` 判定，不再按名字猜
- ✅ surface 投影：`shadowedSeqs` 遮蔽 + `compact-checkpoint` 替身 + `surfaceOp:replace`；`system/message` 已进历史
- ✅ 提示词装配（`internal/prompt`）：确定性拼接、按 Caps 分工具、AGENTS.md 有界注入（默认 8 KB）
- ✅ **P0 四条判据已验收**（见 `docs/p0-acceptance.md`）：内存 15.3 MB / ACP 就绪 59.5 ms / 工具表 906 token / 二进制 8.1 MB
- ✅ 压缩协议（`session.Compact` + `dshd session compact|info`）：追加三条压缩事件、精确遮蔽、日志不删
- ✅ 自动压缩（`internal/compact` + `session compact --auto`）：区间自算、提示词有界、真机跑通（surface 111→10）
- ✅ 自动压缩已接进 ACP：`--auto-compact <tokens>`（默认 40000），token meter 带锚点校准，超预算无处可压时放宽 keep
- ✅ `session.HistoryGuard`：拒绝改写 `$DSH_HOME` 下的历史（要 `--force`）
- ✅ 审批记忆（`internal/approval`，会话×工具，进程内不落盘）+ `allow-always` 选项：实测 3 次调用只问 1 次
- ✅ `session/fork`（`session.Fork` + CLI + ACP 方法）：继承 surface、记 parentSession、不继承授权；实测子会话能答出父会话的暗号
- ✅ 工具契约对拍（`docs/tool-parity.md` + `TestToolParityWithDSH`）：发现并修掉 `shell→bash`、`id→job_id`、bash 缺 `description`
- ✅ 返回结构对拍：`read`/`write` 采用 DSH 的 `<path>/<content>` 包裹（`spi.Result.Text` 优先投影）
- ✅ 审批跨进程保留（`--remember-approvals`，默认关；状态放 `<home>/state/approvals`，不污染 v4 日志）：实测两个进程只问 1 次
- ✅ `dshd run --policy ask|allow|deny|auto`：`run` 以前对写类工具完全放行，现在按 Caps 审批（auto=终端问、非终端拒）
- ✅ `usage_update.used` 改为"本次调用的上下文大小"（原来是累计值，跑三步就翻三倍）；`--context-window` 可配
- ✅ ACP 三方法：`session/load`（重放历史）/ `session/delete`（cwd 可省，靠 FindByID）/ `session/set_mode`
- ✅ 压缩批量回归（15 个真实会话）：**发现并修掉"摘要被放到末尾"的顺序 bug**（现在插回遮蔽起点）
- ✅ 压缩语义回归（`scripts/acp-compact-semantics.mjs`）：**又抓到一个 bug**——替身 seq 顺序与 surface 顺序不一致导致筛空；`compact.Select` 改为直接返回节点
- ✅ 冷启动定位：`BurntSushi/toml` 的 init 占 23 ms，换成 `pelletier/go-toml/v2` 后 init 26→2 ms、ACP 就绪 65.9→**55.9 ms**
- ✅ 模式落地（`internal/modes`）：`default` / `read-only`，工具可见性按模式裁剪，`set_mode` 真的换表
- ✅ 用户自建模式（`$DSH_HOME/modes/*.toml`，支持 `copy_of` 继承）：四道 fail-loud 校验，实测 strict 模式下无 bash
- ✅ 模式决定审批倾向（`approvalDecision`）：deny 不问直接拒、allow 不问放行、deny 压过记忆；实测 no-exec 模式零打扰
- ✅ 模式跟着会话走：写进 `header.agentPreset`、resume 采纳、fork 继承（三个 Go 测试钉住）
- ✅ `history_search`（`spi.HistorySearch` + `modules/tool/history`）：在原始日志里回查含被压缩内容，带 `hidden` 标记
- ✅ history 接线统一为 `attachHistory`（run / acp prompt / acp resume 三处）；`read-only` 模式允许 `history_search`
- ✅ `dshd run --auto-compact`：`agent.Loop.Compact` 每步钩子 + `runCompactor`，与 ACP 同一套零件
- ✅ `session/new` 支持 `mode` 参数（建会话即定模式，没有"先建后切"的窗口）
- ✅ 端到端回归（`scripts/e2e-regression.mjs`，13 项全通过）+ `docs/e2e-report.md`
- ✅ `session/cancel` 真机回归 + `TestSessionUsableAfterCancel`（取消后 busy 必须清）
- ✅ 断连语义：EOF → 取消在飞 turn → 等收尾（≤3s）→ `Shutdown()` 刷盘；`TestEOFDisconnectsAndCancels`
- ✅ `session/list` 读磁盘（项目过滤 + 一句话摘要 + 坏文件也列出来）+ `TestListSessions*` 四条
- ✅ 行为对拍：从 2.3 万个真实结果里挖失败形状，修掉 read offset 越界静默返空、grep 坏路径返 0 命中、失败事件正文为空
- ✅ bash 失败样本归类：84/110 是 `unknown tool`（措辞已对齐）；取消改为 "outcome is unknown" 语义
- ✅ edit 失败归类：84% 是读后写策略；措辞逐字对齐 DSH（含 `cannot write`/`cannot edit` 动词区分）
- ✅ **Martty 真客户端验收**（`docs/martty.md`）：`initialize ok → dsh-go-acp`，流式 reasoning 显示为「推理：…」，usage/模式/Caps 全对
- ✅ `v4.NormalizeCwd`：去掉 `\?\` 前缀（Martty 传的 workspace 带它，会写出真实 DSH 永远不会产生的 projectKey）
- ✅ 工具回显账本（`docs/token-budget.md`）：实测占 57.4%，`bash` 占其中 64.5%，DSH 上限约 50k 字符
- ✅ 修掉截断层空转（只换 Data 不换 Text）+ 指针不可读（artifact 移到 `<home>/artifacts` 并放行只读）
- ✅ 阈值 A/B（各 3 次）：2,000 与 4,000 中位几乎相同（8,645 vs 8,669），四档全答对；**真正的方差是"模型调几次工具"**（1 次≈8.6k，3 次≈15k）
- ✅ `bash` 现在尊重 ctx 取消（原来取消要陪着 `sleep 20` 等 20 秒）；`--max-chars` 可配
- ✅ 工具结果不再 JSON 转义：`bash` 的 `Text` 是原始输出，外置时 Data 去掉 preview（省掉双层转义与重复指针）
- ✅ 截断说明带可执行的 `offset`/`limit`（模型一次就能补读，不用试探）；无行号时退回字符数说明
- ✅ 压缩触发点实测（四档：1→2 次、4000→1 次、12000/40000→0 次；四档都保住暗号）；默认值在短会话里等于关闭，这是设计而非 bug
- ✅ `dshd run` 结束打 `stats: tool_calls=N input=X output=Y`，把"调用次数"变成一等指标
- ✅ 基线读数：基座 ≈4,200 token，第一次工具调用 ≈+4,300；该用工具的任务不多调（不存在"过度用工具"通病）
- ✅ 一次真实"多调"的解剖：`refs/` 3,569 文件让答案从 252 变 9,852（39×），模型第 3 次才自己排除；**改工具描述实测无效**
- ✅ `stats` 改 `defer`：失败的那一轮（最贵的）也有账
- ✅ `.dshdignore`（前缀 + glob，目录层剪枝）：glob/grep 认它，摘要里把"噪声目录"和"声明目录"分开报
- ✅ 实测对比：`bash grep -r` 6 次 / 28,541 token vs `grep` 工具 1 次 / 8,584 token（3.3×）；**ignore 对 bash 无效**
- ✅ 长会话实测（12 轮）：**发现压缩每轮都跑但当轮白压**（hist 在压缩前取快照）；修完 used 从线性 25,585 降到 **11k 见顶**
- ✅ `scripts/acp-long-session.mjs`：12 轮 + 每轮真读 150 行，输出每轮 used/耗时曲线
- ✅ 保留策略改按 **token 预算**（`KeepTokens` 默认 2000；`KeepRecent` 降为下限 2）：12 轮实测 11,285 → **6,995**，且这次只触发 0 次压缩
- ✅ 冷启动抖动定位（`docs/cold-start.md`）：**抖动来自 12MB 二进制加载**，空程序一样抖；成本 ≈2.5 ms/MB，`-s -w` 值 11 ms；我们的代码 vs 同体积空程序 = 41.8 vs 46.5 ms（更快）
- ✅ 体积账（`docs/cold-start.md`）：自身代码 0.29 MB / 8.7 MB；runtime 3.07 MB 不可减；**决定不减**（只剩 TLS ~1.2MB≈3ms 可减，代价是放弃 https）
- ✅ 装配 11 个模块 = **0.5 ms**；initialize 往返 33.1 ms 里 30 ms 是加载可执行文件
- ✅ 参数缺失 vs 目标不存在：`job_output`/`job_kill` 原把空 id 报成「没有任务」，已分开（其余六个工具本来就对）
- ✅ `glob`/`grep` 的「0 命中」分清"真的没有"与"被 .dshdignore 挡住"（摘要带「扫过 N」和「挡掉 N 个目录」）
- ✅ `edit` 的「不唯一」两条出路实测可走通（加上下文 / replace_all 各验一遍）；真机上模型先读再拼上下文，很少触发这条错误
- ✅ ACP 边界：`load`/`resume` 对不存在的会话不再漏宿主路径（原来三层嵌套 + 绝对路径）；`set_mode` 未知模式会列可选值（本来就对）
- ✅ 模型调用失败按种类说（连不上/401/404/429/5xx），不再把 Go 的 `dial tcp ... connectex` 与接口路径甩给客户端；网关正文截到 300 字符；API key 从未泄漏
- ✅ 审批框改为"先命令、后模型自述（标注不可信）"，长命令截中间不截尾巴；`write`/`edit` 显示路径与字节数
- ✅ ACP 的 `session/request_permission` 也带命令：`title` 是 `bash: <命令>`、`kind` 是 `execute`/`edit`、`content` 带"模型自述"标注
- ✅ 边角盘点（`docs/edge-cases.md`）：崩溃恢复 / 并发 prompt / 1MB 单行 / Windows junction **四类本来就对，零改动**
- ✅ 一个假警报：Git Bash 的 `ln -s` 会静默降级成复制，"遍历跟了符号链接"是夹具的问题，不是代码
- ✅ 双宿主并发写：两个 writer 各 20 帧，**一帧不丢**（`O_APPEND`）；编码器里没有截断式回滚，无"A 截掉 B"风险
- ✅ **尾部半帧不再泼掉整份历史**：`splitFrames` 返回 `truncatedAt`，`Log.TailTruncatedAt` 带给调用方，`session info` 打警告；真损坏仍报错
- ✅ 坏 tool_call：参数不是 JSON 时在**流水线入口**拦下（原来直接透传给工具，不校验参数的会拿着垃圾照跑）；坏调用不会让 turn 崩
- ✅ 修掉 `TestSurfaceRealCorpus` 的假阳性：用子串判 `compact-checkpoint` 会把"正文里讨论这个协议"误算成替身，改为解析 `source.kind`
- ✅ `--home` 指到文件上：`checkHome` 在建任何东西之前拦下，说清「这是文件、要的是目录」（原来报 `mkdir <父目录>: cannot find the path`）
- ✅ 极深目录本来就对：60 层 / 1,193 字符路径，glob 正常命中（Go 用长路径前缀）
- ✅ `docs/edge-cases.md` 上列的七类边角**全部验完**
- ✅ 同一份会话**只允许一个写入者**（`<会话文件>.lock`）：两个进程各从 len(events) 编号会写出重复 seq（实测 9 条里 3 个重复），seq 是 surface 与遮蔽的排序依据
- ✅ `dshd doctor`（`docs/doctor.md`）：home 可写 / 会话可读 / 写锁 / 网关 / 模块五项，每项给实测值 + 下一步；✗ 时退出码 1
- ✅ 故障演练（`scripts/doctor-drills.py`）：断电 / 僵尸锁 / 网关 500 / 只读 home，**四项 doctor 全指出来**，退出码也对
- ✅ 修 doctor 自身 bug：警告项原来渲染成 `✓` 而结尾说"上面 ! 那行"，对不上号；现在 Warn 优先画 `!`
- ✅ 运行日志（`<home>/logs/<日期>.log`，一行一 JSON）：会话开始（id/path/cwd/mode）+ 每次工具调用（tool/ms/ok/错误原文）；`run` 与 `acp` 都写；`run` 结束打日志路径（出错也打）
- ✅ doctor 新增「运行日志」项：数警告 + 带出最后一条错误原文 + 给文件路径
- ✅ 日志写不了时**不静默**：`run` 打「警告: 这次没有事后可查的记录——<原因>」但照常干活；doctor 那一项报 ✗
- ✅ 修 doctor 误导：日志目录读不了原来也说「还没有日志（跑一次就会写）」，而它永远不会写
- ✅ 演练五项全中（新增「日志写不了」）
- ✅ 协作一致性实测（`docs/collab.md` + `scripts/collab-probe.py`）：三条疑点里**一条真问题**（换 cwd 找不到会话，已修：`session.ResolvePath` 回退按 id）、两条本来就对
- ✅ 实测顺带挖出后门：`OpenSessionAt`（ACP resume / CLI compact 的入口）**不抢写锁**，绕过了 M30 的防线；已修 + `TestOpenSessionAtAlsoClaimsLock`
- ✅ 顺序保证（`scripts/probe-no-persist.py`）：会话存不下来时**不调模型**——`run` 与 ACP 两条路都是 0 次网关请求
- ✅ 纠正过度宣称：`icacls /deny (W)` 只挡住"在目录里新建文件"，挡不住建子目录/往子目录写；那条演练没覆盖"会话存不下来"
- ✅ 写不下去的三种情况（`docs/edge-cases.md`）：句柄已关报错且历史不坏、重开无重复 seq（本来就对）；只读文件的 resume 错误改成「能读、打不开写入、已有 N 条不受影响」；失败不留锁
- ✅ 第 7 次夹具骗人：`attrib +R` 命令没执行成功却以为造好了只读文件；现在先自证夹具生效再测
- ✅ 会话列表去重（B 线 ④）：同一个 session id 在多个项目目录下时合并成一条 + `copies` 字段（原来出现两行同样 id，按 id 删还会报「多处命中」）
- ✅ A 线最后一条（`scripts/probe-log-failure.py`）：日志**文件**只读时——打「没有事后可查的记录」警告，同时工具照常执行、stats 有账（`TestDiagFailsWhenLogFileReadOnly`）
- ✅ A 线三条都验完：日志目录被占 / 会话存不下来 / 日志文件只读
- ⛔ **不追**（用户拍板 2026-11-03）：真·磁盘满（与"文件只读"同一代码路径，实测收益≈0）；跨机器共用 home（很小众，且属"多机同步"另一个产品问题）。理由记在 `docs/doctor.md` 末尾，**别再当待办翻出来**
- ✅ soak 长跑（`scripts/soak.py` + `docs/soak.md`）：120 轮 RSS 20.2 MB / 句柄 196 / 每轮 1s 不变；**曲线在 t30 后走平**（连续 45 轮句柄不动）→ 趋稳，不是泄漏
- ✅ 踩到的坑：自己写 ACP 客户端时**必须应答 `session/request_permission`**，否则整轮永久挂起且**毫无报错**（看起来像模型不响应）
- ✅ P0 工具第 11 个 `working_directory`（`modules/tool/wd`）：报告/切换**进程** cwd，并明确告知"已开着的 bash 会话有自己的 cwd"
- ✅ 修掉白名单静默丢模块：`dsh.toml` 没列的模块原来一声不吭跳过，现在打 WARN
- ⚠️ 记一笔：**新加模块必须同时写进 `dsh.toml`**，否则它不会激活
- ✅ P0 工具第 12 个 `todo_write`（`modules/tool/todo`）：整份替换、漏字段报错、按状态计数、多项 in_progress 给提醒
- ✅ P0 工具第 13 个 `present`（`modules/tool/present`）：只声明不复制；不存在/是目录/空文件三种都拦下并给下一步
- ✅ P0 工具第 14 个 `ask_user_question`（`modules/tool/ask`）：契约是 **questions 数组**（id/question/header/options/multi_select）
- ✅ `spi.Result` 新增 **`Stop`**：为 true 时 agent 循环在这一步后结束本轮（`TestStopEndsTurnImmediately`）——否则模型会自问自答
- ⚠️ 对拍两次拦住：单 question 形状错、Description 超 120 字
- ✅ P0 工具第 15 个 `web_fetch`（`modules/tool/webfetch`）：只收 http/https、512KB 上限、非 2xx 带状态码与解读、`Caps{Network:true}`
- ⚠️ 顺序调整：`read_image` 不只是工具——图片进上下文要同时改 `spi.Block`、LLM 消息体、ACP 的 `image` 能力声明，先放后面单独做
- ✅ P0 工具第 16 个 `web_search`（`modules/tool/websearch` + `modules/search`）：搜索做成 `spi.SearchProvider`，默认实现读 `DSHD_SEARCH_URL`
- ✅ **没后端时明确报错**而不是返回"搜过了没结果"（后者会让模型以为网上真没有）；真机两条路都验过（ERR / ok）
- ✅ P0 工具第 17 个 `read_image`（`modules/tool/readimage`）：按内容判类型、4MB 上限、图片不进日志（`json:"-"`）
- ✅ 图片进模型整条链：`spi.Result.Images` → 循环紧跟一条 **user 消息**（tool 消息不能带图）→ `llm.Message` 有图时 content 变内容块数组
- ✅ 决定性验证：把纯蓝图改名 `red.png`，模型答「蓝色」并指出文件名与内容不符——它看到的是像素
- ✅ P0 工具第 18 个 `subagent`（`modules/tool/subagent` + `cmd/dshd/subagent.go`）：**PLAN §7.4 的 18 个 P0 工具至此全部落地**
- ✅ 与上游的差别如实标注：上游**异步**（返回 id 后回传），我们**同步**（等到收尾直接返回）——因为 ACP 侧没有"宿主主动通知客户端"的通道
- ✅ 递归在结构上挡住：子代理的工具表里删掉 `subagent` 自己（`noSubagent`）
- ⚠️ 真机踩到两个自己的 bug：`cwd` 参数是摆设（runner 没读）；子循环没设 `Project` 导致子代理看不到工具正文、撞满步数
- ✅ **P0 工具集 18/18 完成并发版 `0.2.1-go.27`**（工具表 2,657 token，P0 目标 ≤6k）
- ✅ **ACP 的 cwd 现在真的生效**：`FS.SetRoot` + `spi.RootFollower`；实测进程在 A、客户端要求 B，B 里的文件读得到
- ✅ `bash` 的默认目录也跟着换（`spi.CwdFollower` + `Service.SetCwd`）——否则同一会话里 read 在 B、bash 在 A
- ✅ `dshd run` 的自动压缩也带实测锚点了（以前每步新建空 meter，永远按启发式比例猜）
- ✅ **改成「它干活、我监工」**：`pwsh`（250 次/33,408 调用）与 `github`（124 次）两个工具由 dshd 自己写，Lead 只接线与验收
- ✅ 派活粒度决定成败：给"做一个功能"→24 步零产出；给"只写一个文件"→7 步交出 270 行
- 真机踩到：api.github.com 回 502 且**正文为空**，工具原来的 Hint 说"看正文 message"是句空话 → 已改成"对方的问题，别连着重试"
- ✅ `repo_here`（12 次）也由 dshd 自己写：它**主动给 remote 里的凭据打码**，我没要求
- ✅ **派活必须带 `--workspace-bytes 41000`（完整 AGENTS.md）**：之前写 0 = 不传手册，
  它连着三次把 Description 写超限，因为那条约束只在 AGENTS.md 里。带手册后一次就 51 字
- ✅ `exec_literal`：跑 .exe 时参数原样传、不被 Git-Bash 转换（`ipconfig /all` 的老痛点）
- ⛔ 用户已拍板（2026-11-03）：**放开 PLAN §9 的 team/goal 一族**，除界面相关插件外，当前实例在用的都要给
- ✅ **给执行者单独的瘦手册**：`docs/WORKER.md`（6 KB）+ `--agents` 参数。
  实测系统提示 16,692 → **4,833 token**（-71%）；它不需要里程碑史、界面线规划、主理鱼流程
- ✅ `scripts/desc-len.py`：给执行者量的工具，四次超限之后才想明白——**约束要可执行，不能只写"≤120 字"**
- ✅ **team 一族最小闭环落地**（dshd 自己写，749 行）：`team_task_create/list/get/update` 四个工具，
  板子落 `<home>/state/team-tasks.json`。真机验过 create→list→claim→complete 全链
- 它这次**自己跑了 desc-len.py**（前四次的教训生效），四个 Description 全在限制内
- ✅ **会话间通信落地**：`spi.WithSession/SessionFrom`（工具靠 ctx 知道"我是哪个会话"）+ 文件信箱
  `<home>/state/mail/<会话>.jsonl`；`send_message` / `wait_agent` / `list_agents` 由 dshd 自己写
- ✅ 真机验过**发信**：A 会话发给 B，B 的信箱文件出现 `{"from": A, "message": ...}`
- ✅ **收信侧也验通了**：`session/resume` 的 cwd 改成可选（按 id 兜底），实测 B 只给 id 就能恢复，
  `wait_agent` 把信箱里那条读走（cursor 0→1，日志 ok:true）
- ✅ 对拍拦住一次真错：DSH 的 `send_message` 参数是 `agent_id`/`message`，我们写成了 `to`/`text`
- ⚠️ 我的 python 字符串替换**静默失效**过一次（签名写错 → 什么都没替换 → 以为改好了），
  以后替换必须断言命中
- ✅ **目标一族落地**（dshd 自己写，514 行）：`get_goal` / `create_goal` / `update_goal`，
  落 `<home>/state/goals/<会话>.json`；revision 做 CAS（拿旧值改会被拒）
- ✅ 真机全链：create → get → update(complete)，revision 1→2，phase active→complete
- 监工补四条测试：旧 revision 拒绝 / 重复 create 拒绝 / blocked 必带理由 / 无会话拒绝
- ✅ **定时任务落地**（dshd 自己写，874 行）：`schedule_create/list/update/delete`，
  落 `<home>/state/schedules/<会话>.json`；**只登记与算 NextAt，不起定时器**（唤醒由宿主以后做）
- ✅ 真机：三种 kind 的 NextAt 都算对（every +300s / daily 23:59 今晚 / weekly 周一三五 08:00）
- ✅ 对拍又拦下一处：`team_task_get/update` 的 id 参数在 DSH 里叫 `task_id`，update 还带 `expected_revision`
- ✅ **定时任务真的会到期了**（`internal/schedule` + `dshd schedule list|due`）：
  `DueAt` 判到期、`nextAfter` 算下一次（every 要一格一格追上、daily/weekly 按本地时区）、
  `Advance` 原子推进 `next_at`。**不做常驻 ticker**——`run` 一次性、`acp` 没有宿主主动发起对话的通道，
  硬起 goroutine 只会得到一个自己调模型却没人能插话的东西
- ✅ 七条测试钉住时间算术（every 追上 / daily 过点 / weekly 跳天 / 坏输入兜底不 panic）
- ✅ **`dshd run --session <id>`**：续已有会话（cwd 可省，按 id 找）。
  **踩到并修掉**：只打开日志不读历史 → 模型会说"这是本会话的第一条消息"；
  现在先 `v4.ReadFile` + `session.History` 再追加新消息
- ✅ **`dshd schedule run`**：到期的任务真的变成一轮对话（复用 cmdRun，审批/压缩/日志/落盘全一致），
  跑完推进 `next_at`。真机：模型在**原会话**里答出任务提示词，`next_at` 05:00 → 05:50
- ✅ **盯仓库落地**（dshd 自己写，572 行）：`watch_start/status/stop/ack`，
  落 `<home>/state/watch/watchers.json`；`watch_ack` 把 watermark 推进到 max(numbers)（只增不减）
- ✅ 真机：登记 → 状态 → ack 12,15 → watermark=15
- ⚠️ 它写的 `created_at` 用 UTC（`...Z`），与其它模块的本地 `+08:00` 不一致——一致性债，先记
- ⚠️ **工具表 5,451 token，已接近 P0 的 6k 上限**：再加工具前必须先算预算，
  要么砍描述，要么把低频工具做成「一个工具多 action」
- ✅ **工具表预算审计**（`docs/tool-table-budget.md`）：73% 的字节是**参数 JSON**，描述只占 8%。
  去掉参数里的 `description` 键省 39%，仅截短描述只省 5%——差 34 个点是那 20 字节的"键壳"
- ✅ 加了 `--tool-schema-compact`（默认关）+ 三条测试；A/B 实测省 24% input，但这**不是**默认值的理由
- ✅ **A/B 抓到一个真设计债**：压缩表下模型 `claim` 漏填 `owner`（原来是靠参数说明提醒的）→
  一被拒就多跑两轮。改成"不给 owner 就用会话 id 认领"后复测通过——**要求写进参数说明，
  等于把自己的设计债推给模型的上下文预算**
- ⚠️ 工具表仍 5,451 token；`--tool-schema-compact` 样本只有 3 个任务，还不足以改默认
- ✅ **第二刀：把可推导的参数变成可选**：`update_goal.goal_id`（一会话一目标）、
  `watch_ack/stop.repo`（只有一个仓库时）；三条测试钉住边界（单仓库可省 / 多个必须指定 / 空时说清先登记）
- ✅ 压缩表 A/B 复测：17,714 → **13,586（-23%）**，且两个任务全对；累计 4/4
- ✅ 工具表 41 个 / 30,416 字节 / **5,438 token**（比上轮 5,451 少 13：可选参数让 required 变短）
- ✅ **第三刀**：`schedule_update/delete.id`（只有一条时可省；多条时**把候选 id 列在拒绝里**，
  模型下一句就能重试对）——真机验过两种情形
- ✅ 工具表 41 个 / 30,332 字节 / **5,432 token**
- ✅ **第四刀**：`job_output/job_kill.job_id`（只有一个后台任务时可省；多个时列出候选）。
  真机：起 `sleep 20` → `job_output {}` → `job_kill {}` 全通
- ⚠️ 旧测试 `TestJobToolsRejectEmptyID` 断言"空 id 要说参数为空"——新契约下改成
  "还没有任务、先起一个"，测试跟着改了（**意图没变：不许把"没给参数"报成"任务不存在"**）
- ✅ 工具表 41 个 / 30,240 字节 / **5,426 token**
- ✅ **工具表默认翻成 compact**（`--tool-schema` 默认 `compact`，要完整表写 `full`）：
  `scripts/tool-table-ab.py` 跑了 10 个任务 × 2 种表 = 20 次，**9/10 对 9/10，输入省 22%**
  （199,112 → 155,853）；唯一失败的两个模式都失败，是我的提示词用了占位符
- ⚠️ 顺序不能反：**先修掉「claim 漏 owner」那个设计债，再压表，最后才敢调默认**
- ✅ 真机默认跑多步任务：建任务 → 认领（不填 owner，靠会话兜底）→ 全 ok，input=13,477
- ✅ **给默认值上了两道闸**：`TestCompactTableKeepsContract` 逐个工具比对压表前后的结构
  （去掉 description 后必须完全相等；**参数区 16,088 → 7,167 字节，省 55%**）；
  `scripts/check.sh` 断言 `run --help` 里必须是 `default "compact"`，工具表超 32 KB 打 WARN
- ✅ 默认值写进 `const defaultToolSchema`，注释里写清"能成立的前提是工具兜住了可推导参数"
- ✅ **安全默认值也上了闸**（`scripts/check.sh`）：`run --policy` 不许默认 `allow`、
  `--workspace-bytes` 不许默认 0、`acp` 不许默认记住授权——这三条改坏**是多给权限**，不只是多花 token
- ✅ 默认值收进 `const defaultRunPolicy / defaultWorkspaceBytes`，注释写明"改坏了是多给权限"
- ✅ **有意放宽的地方给补偿**：`schedule run` 默认 `allow`（无人值守不给就永远跑不动），
  所以每次真跑都打一行「这 N 轮用的是 --policy=allow，写操作没有逐次询问」
- ⚠️ 第三次踩同一个坑：python 里写 `\\n` 进 Go 字符串变成了真换行。以后一律 `chr(92)+'n'`
- ✅ **有意的放宽全部清点**（`docs/deliberate-relaxations.md`）：四处，各写清放宽了什么 / 为什么 /
  怎么让人看见。规矩记死：**有意的放宽必须配可见性，看不见的放宽与失误没有区别**
- ✅ **`dshd run` 现在会回显工具的 Summary**（以前只有 `[ok]`，那条话只进了模型上下文）：
  实测 `← subagent [ok] 子代理完成「回答 1+1」（3 步，25 字；沿用主会话审批记忆）`
- ✅ 子代理的 `BorrowedApprovals` 进了 `spi.SubagentResult`：复用主会话审批记忆是一次权限面扩大，
  必须回传给调用方，不能埋在行为里
- ✅ **定时任务的写权限改成显式 opt-in**（`allow_writes`，默认 false）：
  到点只跑只读工具，写类工具默认被拒。理由：授权在登记时给、提示词是模型写的、执行时人不在场——
  默认只读把"给不给写权限"拉回**有人在场的那一刻**
- ✅ 真机：只读任务尝试 `write` → `← write [ERR] Error: 策略拒绝了 write`，`out.txt` 没有生成
- ✅ **拒绝措辞分两种**：「人拒绝」vs「策略不让人被问到」——后者换问法没用，
  以前都说"用户拒绝了"，模型会反复重试。现在分开了
- ✅ **`watch_ack` 跳号现在会说出来**：ack 会把水位推到 `max(numbers)`，跳过的一批**永远不会再被当成新活**——
  这类"跳过即消失"的副作用比多写一个文件更难发现。现在跳过多少/哪些进 Summary（连续区间压成 `4-14`），
  `Data` 带 `skipped`/`skipped_count`，Hint 直说"现在就是最后的机会"
- ✅ 真机：`← watch_ack [ok] o/n 水位 3 → 15（跳过 11 个编号：4-14）`
- ⚠️ 只是**报告**不是阻止；真正阻止需要 check 阶段记下"见过的编号"，还没做
- ✅ **跳号从"报告"升级成"拦"**：跳过 >3 个编号必须带 `confirm_skip`，否则拒绝且**不写盘**。
  阈值 3 的理由：批量记账常空一两个号不该打扰；跳 4 个以上意味着"把范围推到很后面"
- ✅ 测试抓到第一版不是原子的（先 save 后校验 → "拒绝后水位变成 15"）；重排后又自己写出
  `n <= w.Watermark` 的越界（挪到落盘前时它还是旧值），测试再次抓到
- ✅ 真机：`← watch_ack [ok] 水位 0 → 3（跳过 2 个编号：1-2）` / `← watch_ack [ERR] 跳过了 11 个编号，需要 confirm_skip`
- ⚠️ 靠阈值不靠事实；精确做法要 check 阶段把"见过的编号"落盘，还没做
- ✅ **第 42 个工具 `spawn_teammate`**（dshd 自己写，202 行）：team 一族补齐——之前只有
  send_message/wait_agent/list_agents/team_task_*，缺的正是"把队友建出来"。语料里 44 次，是最大缺口
- ✅ 真机：`← spawn_teammate [ok] 已建队友 alice（fresh，第 1 个）`，重名返回 `[ERR] 队友 alice 已经存在（建于 …+08:00）`
- ⚠️ **我自己接线写错**：把信箱目录当成了名单目录，队友名单落进 `state/mail/agents.json`。
  靠 `find state -name agents.json` 才发现——**build 过 ≠ 落对地方**
- ✅ 工具表 42 个：完整表 6,048 token（超 P0 的 6k），**默认压缩表 4,149**
- ✅ **新增 `docs/dshd-shortboards.md`**：记它作为执行者的真实短板，比单测值钱
- ✅ **`team_task_update` 的 delete 上了两道闸**：别人认领的任务不能直接删（要 `force`，
  且 Hint 指名道姓）；删之前**先归档**到 `team-tasks.deleted.jsonl`——任务板是共享的，
  删完就没人能回答"谁、什么时候、为什么删的"
- ✅ 真机：`← team_task_update [ERR] … 已被认领，需要 force`
- ✅ **测试也由 dshd 写**（9 次调用，4 个测试）：再用**变异测试**验它不是废的——
  去掉守卫 → 只有 Refuses 那条红；去掉归档 → 只有 Archives 那条红。全绿 ≠ 有用
- ✅ **派活模板写进 `docs/WORKER.md` §6.5**：「接口都给全了吗 / 第一步是不是 write /
  怎么知道做对了」三问，外加"自检必须验真实落点，不能只 build"
- ✅ 第三次踩 `
` 坑（python 写进 Go 的 rune 字面量）
- ✅ **`scripts/mutate.py`**（dshd 写）：把实现临时改坏，看**对应的那条**测试会不会红。
  `--list` / `--only` / 退出码（MISSED|BAD→1）；每次**必定恢复**源码（跑完 `git status` 为空）
- ✅ **它第一版有假阳性**：编译不过的变异被报成 `CAUGHT`——包根本没编译，测试一行没跑。
  返工后先扫 `[build failed]`/`undefined:`/`syntax error`，命中就报 `BAD`。
  **验收工具自己有假阳性，等于没有验收**
- ✅ 三组验证：正常变异→CAUGHT/退出 0；不影响断言的变异→MISSED/退出 1；编译不过→BAD/退出 1
- ✅ 门禁加了「变异清单可解析」一条（`check.sh`）
- ✅ **变异条目扩到 6 条**（team-task delete 2 / watch 跳号 2 / schedule 只读默认 1 / goal revision 1），
  全部 CAUGHT；`MUTATE=1 bash scripts/check.sh` 可一次性跑完并复查源码是否恢复干净
- ✅ 顺带修了自己门禁里的一个错计数（`grep -c CAUGHT` 数到了明细行，改成打印 `CAUGHT=N` 汇总）
- ⚠️ 又一课：`python ... | tail` 之后 `$?` 是 **tail 的**退出码。判退出码永远不要过管道
- 下一步 **M83**：把这 6 条里"只测一个函数"的补上跨函数联动（比如 schedule run 那条 deny 分支现在没有测试）

### 界面线现状（2026-11-03）

- ✅ 调研落盘（三份全部完成）：`research/40-official-shell.md`（壳解剖）、`research/41-gui-stack-options.md`（选型与先例）、`research/42-frontend-inventory.md`（前端资产）
- ✅ `UIPLAN.md` 已成稿，**Q1–Q12 待用户拍板**
- 🚧 **代码零行**：`internal/transport/`、`ui/`、`cmd/dshui/` 都还不存在；`spi.ViewSpec` 有定义、无渲染器
- ✅ 唯一已验证的原型是 `cmd/dshd-bridge/main.go`（595 行，把 dshd 伪装成 DSH `/api` + `events.mux`，dsh-TUI 能连）

---

## 1. 六条红线（违反即返工，不看理由）

| # | 红线 | 怎么验 |
|---|---|---|
| **R1** | **内核零业务**：`internal/kernel` 不认识 agent/tool/session/llm | `go list -deps ./internal/kernel \| grep -E 'modules/\|internal/(agent\|tools\|session\|llm)'` 必须为空 |
| **R2** | **SPI 之外无通路**：模块只拿到 `*Scope`（Provide/Inject/On/Effect/Log），拿不到 `*App`/全局单例 | code review：`modules/` 下不得出现 `kernel.App`、包级变量、`init()` |
| **R3** | **工具结果结构化 + 有界 + 错误带 Hint** | 单测 + AST 扫描：`Exec` 的每个 `error` 分支必须设 `Hint` |
| **R4** | **工具 schema 稳定**：一次会话内不变；变更必须显式更新快照 | `testdata/tools.snapshot.json` 的 diff 必须出现在 PR 里 |
| **R5** | **会话日志 append-only**：日志永不改、永不删；滑窗/压缩/截断都是派生期投影 | 读路径不得写盘；`open(write)` 才能发布新帧 |
| **R6** | **不引 JS 运行时、不 fork/vendor dsc、不用 mvdan 执行命令** | `go.mod` 无 js 运行时；无 `dsc` module；`exec` 只走真实 shell 进程 |
| **R7** | **界面线不越界**：界面不得直读会话日志；宿主不得 import 界面；跨线只经 `Transport` + `ViewSpec` | `grep -r 'codecs/v4' ui/` 为空；`go list -deps ./internal/transport \| grep -E 'ui/\|cmd/dshui'` 为空 |

**R6 的依据**（别重新踩）：
- 内嵌 Node 光底噪 113 MB，直接击穿 150 MB 目标。
- dsc 的内核有 7 类硬编码业务、工具层是 text-first（`research/11`），fork 后要改的比自研多。
- `mvdan.cc/sh` 在 Windows 上是静默桩：`cat <(echo x)` 输出字面量 `TODO: support process substitution on Windows`，`cd /tmp` 失败，外部命令 `exit status 127`。它只准用于**静态分析**（判定是否写盘 → 喂沙箱）。

> **R6 的适用范围还没拍板。** 它的敌人是「把 V8 塞进 dshd 进程」（113 MB 底噪）。WebView 里的 JS 跑在**操作系统的渲染进程**里，dshd 自身不加一行 JS 引擎——这一类算不算违反 R6，见 `UIPLAN.md` §7 **Q3**。
> **拍板前不许按"不受限"开工**；要动界面代码，先看到 Q3 的结论。

---

## 2. 代码地图与写作用域

| 路径 | 职责 | 谁可以改 |
|---|---|---|
| `cmd/dshd/` | main：manifest → kernel → blank import 默认模块 | 只有装配代码，禁止业务分支 |
| `internal/kernel/` | scope / registry / eventbus / assembler **四件事** | 改动需在 PLAN 里有理由 |
| `internal/spi/` | 全部接口 + 数据类型 + 服务键，**零实现** | 加接口要同步加"默认实现 + 替换样本"测试 |
| `internal/fsutil/` | 文件遍历（噪声目录 + `.dshdignore`）、二进制判定 | 零业务；只给 glob/grep 这类工具共用 |
| `<home>/artifacts/` | 工具大输出的外置工件 | 宿主写、模型只读（`FS.WithExtraReadRoot` 放行） |
| `internal/session/` | 事件溯源 + canonical 模型 + 会话列举 | 唯一允许直接读日志的包 |
| `internal/agent/` | turn/step 双循环 | 不许内联压缩逻辑 |
| `internal/acp/` | ACP 服务端（stdio JSON-RPC） | 按 `research/22` 的方法表 |
| `internal/llm/` | OpenAI 兼容 SSE 客户端 | 先只做 magpie |
| `internal/prompt/` | 系统提示装配（确定性、可组合、有界） | 加段必须同时加"空段消失"与"确定性"测试 |
| `internal/compact/` | 压缩策略（选区间 + 生成摘要） | 只做策略；写事件一律走 `session.Compact` |
| `internal/approval/` | 审批记忆（会话×工具） | 默认只在内存；落盘必须由 `--remember-approvals` 显式打开，且**不写进 v4 日志** |
| `modules/*` | 静态默认实现，一模块一目录一个 `Register(scope)` | 新模块必须过 `PLAN.md` §2.4 的三条判据 |
| `codecs/v4/` | DSH 会话 v4 编解码 | 字节级对齐，见 §4 |
| `wasmhost/` `pluginhost/` | wazero / go-plugin 装载器 | 载入层只做装载，不含业务 |
| `internal/modes/` | 模式定义 + 工具可见性视图（只读） | 内置模式 `builtin=true` 只读；未知模式必须报错，不许回落 |
| `docs/` | 设计文档 | 决策变更必须同时改**对应那条线**的 PLAN |
| `research/` `evidence/` `refs/` | **只读**：调研与参考 | 只许追加新调研，不许改结论 |
| `cmd/dshd-bridge/` | 把 dshd 伪装成 DSH `/api` + `events.mux` 的桥（595 行，第三方客户端能用） | 只做协议翻译，不含业务；**是 U0 的参考实现** |
| `internal/transport/` | 界面线唯一的宿主侧入口：HTTP `/api` + WS `remote.mux` + `/connect` + `/plugins` | 只认 `spi`，不认 `ui/`；method 变更必须更新 `testdata/api.snapshot.json` |
| `ui/` | 前端工程（TypeScript）；构建产物 `//go:embed` 进二进制 | 不得读日志文件；插件只能经 slot 授权树挂载 |
| `cmd/dshui/` | 桌面壳（窗口 / 目录选择 / 拖入真实路径 / 外链白名单） | **≤1000 行**，不许出现业务分支 |
| `UIPLAN.md` | 界面线的 PLAN（Q1–Q12 待拍板） | 决策变更必须同时改它；**未拍板的 Q 不许动手** |

---

## 3. 写工具的规矩（这是产品本体）

```go
type Tool interface {
    Name() string                                   // 全局唯一、稳定
    Description() string                            // 「什么时候用它」，≤120 字
    Schema() json.RawMessage                        // 稳定：无时间戳/绝对路径/随机 id
    Exec(ctx context.Context, args json.RawMessage) (Result, error)
}
```

**每个工具的 checklist**

1. `Data` 是结构化 JSON（键名 snake_case），`Summary` 只给人看。
2. 输出有界：超过阈值走中间件外置成 artifact + 指针；**替换体本身不许超阈值**。
3. 每个 `error` 带 `Hint`，写清"下一步该怎么改"（模型下一句就靠这句话）。
4. 失败返回 `(Result{}, err)`，不要返回半截结果 + nil error。
5. 路径一律正斜杠、相对 workspace 根；绝不把 `os.Getwd()` 的原生反斜杠喂给模型。
6. 写类工具必须声明 `Caps()`（`writes`/`executes`/`network`/`destructive`），**必填**——审批与沙箱都按它判定，不许按工具名猜（dsc 的反例：`sandbox.go:52`）。
7. 共享文件观测策略：**改前必须先读**（read-before-write），由 `FS` 中间件统一实现，工具自己别写。

**中间件顺序（固定）**：`pre-execute`（审批/沙箱，可 veto）→ `execute`（超时 + 活跃续命）→ `post-execute`（spill/裁剪/重复提醒）→ `result`（广播）。

---

## 4. 会话 v4：字节级清单（写错就读不到历史）

| # | 规则 |
|---|---|
| 1 | `projectKey`：`/ \ :` 各变一个 `-` 并折叠；`[A-Za-z0-9._-]` 且非 `~` 原样；其余每个 UTF-16 码元 → `~XXXX`（**大写** 4 位 hex）；去前导 `-`；空 → `root`；截 **251**；包成 `--<key>--` |
| 2 | `encodeSegment(id)`：`.`→`~002E`、`..`→`~002E~002E`，其余不安全码元（含 `~`）→`~XXXX`，空串报错 |
| 3 | `cwd` 缺失 → `<root>/_no-cwd`；文件 `session.v4.jsonl.zstd` |
| 4 | **第 1 帧只装 header 一行**，必须恰好一行且以 `\n` 结尾；list/stat 只解首帧 |
| 5 | 之后每个 durable 批次一帧；批窗口 **200 ms**；`fsync` 后才 resolve；失败回滚到原长度 |
| 6 | zstd **只开 checksum，不设 level**；magic `4247762216`；追加不重写已提交帧 |
| 7 | header 键白名单：`type,version,id,createdAt,isSeeded,delegationDepth` + 可选 `cwd,parentSession,origin,agentPreset`；**多一个键即非法** |
| 8 | 事件不在 62 个 type 词汇表里 → 拒绝该日志，除非信封 `ignorable: true` |
| 9 | 只有 5 类参与派生：`system/message` `developer/message` `user/message` `assistant/message` `tool/result`，必须带 `surfaceOp` |
| 10 | `tool/call.arguments` 是 **JSON 字符串**；`tool/result` 是 `role:'tool'` + 顶层 `toolCallId`/`content`/`isError` |
| 11 | `request/header.header.system` 已退休：多一个 `system` 键，当前代码直接拒 |
| 12 | 压缩只追加 `compaction/summary`（`shadowedRange` + `shadowedSeqs`），**日志永不删**；遮蔽只发生在 surface 折叠 |

> 先写 golden test（拿 `$DSH_HOME/sessions/` 里的真会话做输入），再写编码器。反过来的顺序一定会返工。
>
> 改了会话格式必须跑一次 Node 侧验证：`node scripts/verify-node-reads.mjs <file>`（本机需 `ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe"`）。

---

## 5. 工作流

**开工前**
1. 读 `PLAN.md` §1（十项决策）与 §14（当前里程碑）。
2. 读对应的 `research/` 文档；**不要凭记忆写格式**。
3. 确认要改的文件不在别人（或别的子代理）的作用域里。

**写代码时**
- TDD 顺序：先写 golden/一致性测试 → 再写实现。会话、工具投影、提示词装配三处**必须**如此。
- 每加一个 SPI 接口，同时交：默认实现 + 一个替换样本 + 禁用后的 fail-loud 测试。
- 每个新依赖要在 `go.mod` 注释里写「为什么标准库不够」。

**发布件**：`bash scripts/build.sh` 出三平台二进制 + SHA256SUMS（不发 npm）。

**状态断言用 Go 测**：node ACP 脚本适合"真跑一轮对话"；断言 header/文件/状态一律写 Go 测试（踩过：node 侧连挂四次没暴露真因）。

**回归**：`scripts/e2e-regression.mjs`（ACP 全方法，16 项）。写断言时贴着实际语义——踩过"恒真断言"和"delete 断言写反"。

**压缩改动必跑**：`scripts/acp-compact-semantics.mjs`（压完之后模型还记不记得关键信息）。

**启动变慢先看**：`DSHD_TIMING=1 ./dshd.exe version` 与 `GODEBUG=inittrace=1`——上一版就是靠这个抓到 23 ms 的 init。

**交付前（缺一不可）**
```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./... && go vet ./...
go test ./... -race
go list -deps ./internal/kernel | grep -E 'modules/|internal/(agent|tools|session|llm)'   # 必须无输出
mv modules /tmp/modules.check && go build ./internal/... ; mv /tmp/modules.check modules  # 抽掉实现仍能编译
```
- 工具表 token：用真实分词跑一次，贴数字（不许估算）。
- 二进制体积：`-ldflags "-s -w"` 后贴数字（目标 ≤ 25 MB）。

---

## 6. 版本、提交、文档

- **版本**：`<上游版本>-go.<上游 rc 序号>`。当前 `0.2.1-go.26`。发布前更新 `CHANGELOG.md` 的映射表（移植了什么 / 跳过什么 / 为什么）。
- **提交**：`M<里程碑>: <一句话>`，例如 `M1: tools/read 与观测策略`。一个提交只做一件事。
- **决策**：任何改变 §1 决策的实现，**先改 `PLAN.md` 并写理由**，再动代码。禁止"代码先行、文档追认"。
- **文档**：`docs/cordis-alignment.md` 是 cordis 概念 → Go 概念的逐条对照，是设计约束，不是宣传材料。

---

## 7. 基线数字（拍脑袋前先看这张表）

| 项 | 值 | 来源 |
|---|---:|---|
| **DSH 内存（实测 2026-11-03，9 进程 / 2 窗口）** | **1,603.8 MB** | 任务管理器（用户截图） |
| 其中最大单进程 | **831.1 MB**（Electron 主 + Node 宿主合体） | 同上 |
| 其余 8 个渲染进程 | 378.6 / 182.0 / 43.0 / 42.8 / 42.7 / 42.3 / 38.4 / 2.9 MB | 同上（**每开一个窗口多一个**） |
| 空转 CPU | **2.0%**（没在跑任务） | 同上 |
| 工具表（最新一次请求） | 61 个 / 54,936 字符 / **12,209 token** | `research/20` |
| **dsh-go 工具表（实测 2026-11-03）** | 42 个 / 31,630 字节 / 完整表 **6,048** token，**默认压缩表 4,149** | 网关实测 |
| 工具契约对拍 | 6 项完全一致、3 项超集 | `docs/tool-parity.md` |
| **系统提示成本（实测 2026-11-02）** | 无 AGENTS 1,909 / 8KB 上限 **4,077** / 全量 14KB 6,248 inputTokens | 本机 `dshd run` |
| 工具集变体 | 4 种（54/59/60/61） | 同上 |
| 3,002 次调用的工具分布 | `bash` 83.7% / `job_output` 8.7% / 其余 7.6% | `HANDOFF.md` §1.5 |
| 工具回显占上下文 | **85.5%** | 同上 |
| 会话样本 | 94 MB，zstd 多帧，62 事件类型 | `research/21` |
| Go 空进程冷启动 | median **18.1 ms** / p95 22.7 ms | `research/30` |
| **本机启动成本 vs 体积（实测 2026-11-02）** | 1.2MB→11.6ms / 8.8MB→31.7ms / 12.8MB→46.5ms（≈2.5 ms/MB） | `docs/cold-start.md` |
| **dsh-go 冷启动（实测 2026-11-03）** | `measure-start.py` 报 55–115（含探针开销）；**紧探针 p50 27.0–34.8 ms**（本机底噪，见 `docs/cold-start.md`） | `docs/cold-start.md` |
| 进程 init 总和 | **2.0 ms**（换掉 BurntSushi/toml 之前是 26.0 ms） | `GODEBUG=inittrace=1` |
| **dsh-go 二进制（实测 2026-11-02）** | `-ldflags "-s -w"` → **8.4 MB**（目标 ≤25 MB） | `scripts/build.sh` |
| **dsh-go 内存（实测 2026-11-03）** | 整轮峰值 **15.7 MB**（DSH 本体 870 MB） | `scripts/measure-mem.py` |
| **`dshd version` 启动（实测 2026-11-02）** | median **42.8 ms**（不含装配，纯进程+init） | 本机 12 次 |
| go-plugin / wazero 二进制开销 | **+14.8 MB** / +3.5 MB | 同上 |
| 进程插件工作集 | **14.4 MiB** | 同上 |

**这些数字是验收基准，不是参考值。** 新测的数据要更新这张表并注明日期。

---

## 8. 本机环境（照抄即可）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"     # go 1.27.3，未入 PATH
# GOPROXY=https://goproxy.cn,direct  （proxy.golang.org 不通）
```

| 资源 | 路径 | 用法 |
|---|---|---|
| DSH 产物（asar） | `D:/DeepSeek Harness/Desktop/resources/app.asar/dsh` | **bash 读不了**，要 `ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" script.mjs` |
| 存量会话 | `D:/DeepSeek Harness/home/sessions/**` | **只读**，考古与 golden test 的输入 |
| 模型网关 | `http://127.0.0.1:3425/v1`（magpie，OpenAI 兼容） | LLM 客户端联调 |
| Martty（真 ACP 客户端） | `C:/Users/Administrator/AppData/Roaming/npm/martty-cli.cmd` | `--agent <dshd> --agent-arg acp --check-runtime` 验握手；见 `docs/martty.md` |
| dsc 参考 | `refs/dsc/` | **只读**；借鉴处必须注明 `refs/dsc/<file>:<line>` |

**行尾**：仓库统一 LF（`.gitattributes` 的 `eol=lf`）。CRLF 会让 `gofmt -l` 把所有文件判成"未格式化"，CI 直接红——编辑器别开自动转 CRLF。

**真机禁忌**（与 `$DSH_HOME/AGENTS.md` 一致，这里只强调三条）：
1. 不重启、不关闭 DSH；不改 `D:/DeepSeek Harness/home/profiles/*/cordis.patch.yml`。
2. asar 与 profiles 一律只读。
3. `E:/Documents/deepseek-harness/` 是本会话之外的旧工作区，**不要写**。

---

## 9. 明确不做（有人提就复述对应的 PLAN）

**宿主线**：JS 兼容桥、`!!js` 配置表达式、cordis 式热重载、team/workflow/schedule/goal/skill（P0）、多 provider、HTTP/WS 兼容层、把默认实现做成进程插件。

**界面线**（比宿主更容易被"顺手加一下"拖垮，逐条记死）：

| 不做 | 为什么 |
|---|---|
| **Electron 壳** | 官方壳 174 MB asar / ~500 MB Chromium 主进程就是本项目的起点动因。壳只值 200 行——一个 WebView 窗口 + 三个原生能力 |
| **复刻官方 55 个 `dsh-client-ui-*`** | 23.7 万行里 32% 是第三方库与共享模块，另有 9 个包可直接砍（`research/42`）。**机制抄，粒度不抄** |
| plugin-market / schedule / goal / workflow / agent-team 面板 | 宿主侧 P0 就不做这些能力，界面自然没有对应物 |
| 侧栏内嵌浏览器（`<webview>`） | 等于壳里再装一个浏览器，攻击面与复杂度极高，且与主流程无关 |
| PDF / Excel 内联预览 | 官方为它付了 14.2 MB 前端字节；交给系统默认程序 |
| 账号 / 充值 / 登录页 | 平台生意，不是 agent 客户端的能力 |
| 自动更新 / 崩溃上报 / 埋点 / 托盘 | 与「不上报、单二进制、用户自己管更新」的定位冲突 |
| monaco / 完整编辑器 | 我们是 agent 客户端，不是 IDE |
| 移动端 | 生态里已有 `dsh-Go`（Dart）；Transport 做对了，移动端是别人的事 |

---

## 10. 主理鱼（superagent）接手流程

本仓挂着一条主理鱼：它盯 issue/PR，有动静就唤醒会话，**唤醒后按本文件办事**。被唤醒时照这个顺序走：

1. **先读 `MAINTENANCE.md` 的「当前状态」**——知道现在走到哪个里程碑，别从零猜。
2. **先判断这条 issue 属于哪条线**（见开头「两条线」表）。判断不了就看它碰的路径。
3. **按类型处置**：

| 类型 | 怎么做 |
|---|---|
| bug（能复现） | 先写一个**在旧代码上会红**的测试 → 修 → `scripts/check.sh` → PR |
| bug（复现不了） | 回一条「需要什么」（版本、复现步骤、报错原文），**不猜着改** |
| 范围外功能请求 | 回「待定」并说明为什么不在当前里程碑，**不要顺手加功能** |
| **界面线请求** | 先查 `UIPLAN.md` §7 那个 Q 拍板了没有。**没拍板一律回「待定，等 Qn 结论」**，不许为了"让用户高兴"先动手 |
| 需要人拍板 | `ask_user_question` 问一次、问全 |

4. **动代码前**复核对应的 PLAN：宿主线看 `PLAN.md` §1，界面线看 `UIPLAN.md` §7。动了决策就先改 PLAN 再改代码。
5. **交付**：分支 + PR + `scripts/check.sh` 全绿 + `CHECKLIST.md` A 表逐条打勾。
6. **收尾**：`watch_ack` 打标（`主理鱼:已处理`）；新踩的坑写进本文件 §7 或 `docs/`。

**唤醒回合里的禁令**：不重启/不关闭 DSH、不写 `D:/DeepSeek Harness/home/profiles/`、不为了「让 CI 过」而放宽门禁。

---

## 11. 界面线的规程（只在改 `ui/` / `internal/transport/` / `cmd/dshui/` 时适用）

**开工前**

1. 读 `UIPLAN.md` §7，确认要碰的那个 Q **已经拍板**。没拍板 → 停，回去问。
2. 读 `research/40-official-shell.md` §11 的「必须复刻 10 条 / 可以砍 10 条 / 可以换 9 条」——**不要重新发明壳**。
3. 契约改动（`/api` 的 method、WS 帧形状、`ViewSpec` 字段）**先改快照，再改代码**：`testdata/api.snapshot.json` 的 diff 必须出现在 PR 里（对齐 R4）。

**写代码时**

| 规矩 | 内容 |
|---|---|
| 契约先行 | `internal/transport` 写完之后，UI 才可能写。**顺序反过来一定返工**（前端会逼出 `fs.read` 这类私有后门） |
| 端口的铁律 | 宿主一律 `--port 0` 由 OS 分配；壳从 `ready{url}` 拿，**不许写死端口** |
| 认证的铁律 | launch token → `/connect` 换 cookie；**cookie 永不进 WebView**（渲染进程能读 token = 一个 XSS 交掉整台机器） |
| 事件用 v4 原始词汇 | 不许为 GUI 造第二套事件名。压缩/遮蔽语义只有一份真相 |
| 断流必须能续 | WS 断开后靠 `revision`/`index` 连续性判定 + 尾页补洞；**不做"重连就整体刷新"**（长会话会闪） |
| 前端产物 embed | `//go:embed ui/dist`；二进制里**不许**出现 `node_modules` |
| 壳是薄的 | `cmd/dshui` ≤1000 行。要加第二个原生能力，先问"浏览器能不能做" |

**交付前（界面线专属）**

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./... && go vet ./...
go test ./... -race
cd ui && npm run build && cd ..          # 产物必须能构建，且体积 ≤ 3 MB
grep -r 'codecs/v4' ui/ | wc -l          # 必须 0（R7）
go list -deps ./internal/transport | grep -E 'ui/|cmd/dshui'   # 必须无输出（R7）
```

- 前端产物体积、`dshd` 二进制体积（含 embed 后）两个数字都要贴。
- **U1 的唯一验收是"自己用它干一天活"**——不是截图好看，不是测试全绿。

