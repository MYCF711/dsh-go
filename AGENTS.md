# AGENTS.md · DSH-GO

> **本仓**：用 Go 重写 DeepSeek Harness（DSH）的宿主。上游基线 `0.2.1-rc.5`，本仓版本 **`0.2.1-go.23`**。
> **先读** `PLAN.md` §1 的十项决策——那是用户逐条拍板的结论，**不要再讨论、不要"顺便优化"**。
> 本文件是操作手册：冲突时以本文件为准；本文件没写的，以 `PLAN.md` 为准。

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
- 下一步 **M38**（A 线）：磁盘写满时正在写的会话会怎样（Can't 直接造，考虑用只读的会话文件模拟）

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

**R6 的依据**（别重新踩）：
- 内嵌 Node 光底噪 113 MB，直接击穿 150 MB 目标。
- dsc 的内核有 7 类硬编码业务、工具层是 text-first（`research/11`），fork 后要改的比自研多。
- `mvdan.cc/sh` 在 Windows 上是静默桩：`cat <(echo x)` 输出字面量 `TODO: support process substitution on Windows`，`cd /tmp` 失败，外部命令 `exit status 127`。它只准用于**静态分析**（判定是否写盘 → 喂沙箱）。

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
| `docs/` | 设计文档 | 决策变更必须同时改 `PLAN.md` |
| `research/` `evidence/` `refs/` | **只读**：调研与参考 | 只许追加新调研，不许改结论 |

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

- **版本**：`<上游版本>-go.<上游 rc 序号>`。当前 `0.2.1-go.23`。发布前更新 `CHANGELOG.md` 的映射表（移植了什么 / 跳过什么 / 为什么）。
- **提交**：`M<里程碑>: <一句话>`，例如 `M1: tools/read 与观测策略`。一个提交只做一件事。
- **决策**：任何改变 §1 决策的实现，**先改 `PLAN.md` 并写理由**，再动代码。禁止"代码先行、文档追认"。
- **文档**：`docs/cordis-alignment.md` 是 cordis 概念 → Go 概念的逐条对照，是设计约束，不是宣传材料。

---

## 7. 基线数字（拍脑袋前先看这张表）

| 项 | 值 | 来源 |
|---|---:|---|
| DSH 内存（私有工作集） | 870 MB | 本机 `tasklist` |
| 其中 Chromium / Node 宿主 | ~500 / ~367 MB | 同上 |
| 工具表（最新一次请求） | 61 个 / 54,936 字符 / **12,209 token** | `research/20` |
| **dsh-go 工具表（实测 2026-11-02）** | 10 个 / **1,418 token**（含 history_search） | magpie 实测 |
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
| **dsh-go 内存（实测 2026-11-03）** | 整轮峰值 **15.5 MB**（DSH 本体 870 MB） | `scripts/measure-mem.py` |
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

## 9. 明确不做（有人提就复述 §1）

Electron / 147k 行 React 前端、JS 兼容桥、`!!js` 配置表达式、cordis 式热重载、team/workflow/schedule/goal/skill（P0）、多 provider、HTTP/WS 兼容层、把默认实现做成进程插件。

---

## 10. 主理鱼（superagent）接手流程

本仓挂着一条主理鱼：它盯 issue/PR，有动静就唤醒会话，**唤醒后按本文件办事**。被唤醒时照这个顺序走：

1. **先读 `MAINTENANCE.md` 的「当前状态」**——知道现在走到哪个里程碑，别从零猜。
2. **按类型处置**：

| 类型 | 怎么做 |
|---|---|
| bug（能复现） | 先写一个**在旧代码上会红**的测试 → 修 → `scripts/check.sh` → PR |
| bug（复现不了） | 回一条「需要什么」（版本、复现步骤、报错原文），**不猜着改** |
| 范围外功能请求 | 回「待定」并说明为什么不在当前里程碑，**不要顺手加功能** |
| 需要人拍板 | `ask_user_question` 问一次、问全 |

3. **动代码前**复核 `PLAN.md` §1：这条改动动到十项决策了吗？动了就先改 PLAN 再改代码。
4. **交付**：分支 + PR + `scripts/check.sh` 全绿 + `CHECKLIST.md` A 表逐条打勾。
5. **收尾**：`watch_ack` 打标（`主理鱼:已处理`）；新踩的坑写进本文件 §7 或 `docs/`。

**唤醒回合里的禁令**：不重启/不关闭 DSH、不写 `D:/DeepSeek Harness/home/profiles/`、不为了「让 CI 过」而放宽门禁。

