# PLAN · dsh-go：用 Go 重写 DSH

> **版本** v1.0（2026-11-02）· 十项方向已与用户逐条拍板
> **上游基线** DSH `0.2.2-rc.1` · 当前本仓版本以源码与 `CHANGELOG.md` 为准（§13 是版本规则，不重复硬编码旧值）
> **证据层**：`HANDOFF.md`（实测账）、`evidence/`（三份审计）、`research/`（本轮六份调研）、`refs/dsc`（只读参考）
> **执行规则**：见 `AGENTS.md` —— 本文件只讲**为什么**，AGENTS 讲**怎么做**
>
> **现行读法（2026-11-03）**：§0–§13 是已拍板架构与质量目标，§14 是最初 M0–M5 范围，§14.1 才是当前执行顺序。P0 的 18 工具、HTTP/WS/桌面对接“不做”等是最初阶段边界，**不是删除后来已实现能力或阻止后续 Transport/UI 工作的理由**。`docs/transport-contract.md` 的 H 表是带日期的证据快照，领任务前必须重读源码、测试和工作树；稳定决策不因旧状态描述改变。
>
> **当前第一门 H14**：先统一 HTTP 裸参数 body、WS `/ws`、服务端 `{seq,type,data,time}` 与两份快照；每会话 Transport journal 统一分配 seq，WS/history 同源，history 返回原始 frames。未经真实 UI apiclient 验证，不得进入 H6/H13 跨仓联调。完整定义见 `docs/transport-contract.md` 与本文件 §14.1。

---

## 0. 一句话与成功判据

**用 Go 重写 DSH 宿主：内核只做插件运行时，任何业务能力——包括 agent loop、工具、会话、传输、UI——都由 manifest 装配、都能被换掉；工具层为模型设计，不为 shell 设计。**

| 指标 | 现状（实测） | 目标 | 验证方式 |
|---|---:|---:|---|
| 常驻私有工作集 | 870 MB | **< 150 MB** | `tasklist` 私有工作集 |
| 冷启动 | 数秒 | **< 100 ms** | 进程起止计时（Go 空进程实测 median 18 ms） |
| 空二进制体积 | — | **≤ 25 MiB**（25×1024×1024 字节） | `go build -ldflags "-s -w"` |
| 2,500 次 shell 调用 | — | **无线程/句柄泄漏** | 压测 + `pprof` 前后对比 |
| 工具表 token | **12,209**（61 个工具，实测分词） | **≤ 6,000** | 同一模型对 schema 分词 |
| 一次会话的 shell 回显占比 | **85.5%** | **< 50%** | 会话日志统计 |

**产品指标**（比上面所有数字都重要）：同一批真实任务，模型发出的 token 更少、返工更少。

---

## 1. 十项已拍板的方向

| # | 决策 | 拍板内容 | 影响 |
|---|---|---|---|
| 1 | 目标形态 | **平行实现，以「能接管」为验收口径**；软着陆——不迁数据，Go 版直接**考古**读旧会话与旧记忆 | 兼容要求 = 「读得懂旧世界」，不是「接管旧状态」 |
| 2 | 「一切皆插件」强度 | **内核四件事写死**，业务能力零内置。与官方一致：`cordis@4.0.5` 的 `Context` 构造函数直接 `new ReflectService/RegistryService/EventsService/LoggerService`（`src/context.ts:79-83`），manifest 里没有它们的 entry | 内核接口不对外冻结，可随版本演进 |
| 3 | 插件生态 | **Go 原生插件，按插件形式安装**（目录 = manifest + 二进制/wasm）；不做 JS 兼容桥；外部生态走 MCP | 内存目标保得住（内嵌 Node 光底噪 113 MB） |
| 4 | 会话格式 | 内核只认 **canonical 事件模型**；v4 是一对 **codec 插件**（默认启用、可开关），**写回兼容**（Node 能读 → 当对拍 oracle）；工具只对齐名字/参数/语义 | 兼容不反向绑架内核 |
| 5 | 基座 | **不 fork dsc、不依赖、不 vendor**；dsc 只作引用（`refs/dsc` + 文档注明出处），主线自研 | dsc 内核有 7 类硬编码业务（`research/11`），契约也不同线 |
| 6 | 客户端 | **ACP → TUI（开发期主力）→ Go 桌面端**（最终）；不保留 Electron + 147k 行 React | 客户端只经 `Transport` + `ViewSpec` 接触内核 |
| 7 | 插件移植 | **后续任务**。P0 只做本体，架构上只留 SPI 插口 | 本体先跑通 |
| 8 | 本体边界 | **干活闭环**：内核 + 18 工具 + agent loop + 会话 v4 + 压缩 + ACP + TUI；不含 team/workflow/schedule/goal | 约 2.5–3 万行 |
| 9 | 自举程度 | **模式（preset）机制必须复刻**；工具按模式可见；**内置模式只读**（用户层 patch 命中即报错），用户只能自建模式；装卸类工具只在用户自建模式下可见 | 官方是「可编辑」的（`dsh-agent-preset-registry`），我们加这层保险 |
| 10 | 追更 | **分支追更**：稳定版 `0.2.2-go.N`、测试版 `0.2.2-beta.N`，前缀升级后 N 从 1 开始，当前 N 看源码与 CHANGELOG | 每次跟进 = 一个明确的上游基线 |

---

## 2. 「一切皆插件」的可执行定义

### 2.1 原意

cordis 里没有"宿主"，只有一堆 fiber；`dsh` 本体只是 bootstrap。所谓"一切皆插件"不是"支持插件"，而是：

> **没有任何一行代码比别的代码有特权。任何能力都能被一个 manifest 项换掉。**

### 2.2 Go 里的翻译

静态编译语言没有 `import()`。等价物是把"插件"从**动态加载**解耦为**可替换**：

| cordis | Go |
|---|---|
| fiber（插件实例） | `Scope`（生命周期 + Disposer） |
| `ctx.effect()` | `scope.Effect(fn)` |
| `ctx.provide('key')` | `kernel.Provide[T](scope, Key[T], impl)` |
| `static inject = []` | manifest 的 `requires` / `provides` |
| `ctx.on(event)` | `bus.On(Event, h)`（随 scope 撤销） |
| `cordis.patch.yml` | `dsh.toml` + 覆盖层（按 id 整块替换 config，非 merge） |
| 动态 `import()` | 三层装载：static / wasm / process |

### 2.3 五条公理

| # | 公理 | 落地 |
|---|---|---|
| A1 | **内核无权** | CI：`go list -deps ./internal/kernel` 不得含 `modules/`、`internal/{agent,tools,session,llm}` |
| A2 | **SPI 之外无通路** | 模块只拿到 `*Scope`（Provide/Inject/On/Effect/Log），拿不到 `*App` |
| A3 | **默认实现也是插件** | `modules/*` 与第三方插件走同一个 `Register(scope)`；`cmd/dshd` 只有 blank import + 组装 |
| A4 | **装配先于执行** | 禁止 `init()` 注册；**激活由能力可用性驱动，不按 manifest 行顺序**（官方踩点，`research/23` §6.1） |
| A5 | **卸载即撤销** | 每个模块的 goroutine/句柄/订阅挂 scope；`Dispose()` 后断言计数归零 |

### 2.4 三条自动验收判据

1. **抽掉实现仍能编译**：在执行者独占的隔离副本中移出 `modules/` 后 `go build ./internal/...` 通过；不得在共享工作树原地删除、移动或改名。
2. **禁用任意模块仍能启动**：给出「缺 X 能力，需要 Y 模块」的 fail-loud 错误，不 panic、不静默降级。
3. **替换任意模块不改宿主**：换 `compaction/basic` → `compaction/gokifu` 只改 manifest 一行。

---

## 3. 三层装载

| 层 | 换模块 | 生效 | 每模块内存 | 用在哪 | 实测开销 |
|---|---|---|---:|---|---|
| **static** | 换 import + 重编译 | 1–3 s | 0 | 内核、官方默认实现（绝大多数） | 0 |
| **wasm** | 改 manifest，重启 | ~50 ms | 0.5–5 MB | 纯逻辑、频繁替换（压缩策略） | +3.5 MB 二进制 |
| **process** | 放二进制，热加载 | 即时 | ~14.4 MiB | 第三方、重依赖、崩溃隔离 | **+14.8 MB 二进制** |

**判定规则**：能 static 就 static；能 wasm 就 wasm；process 只给「语言不同 / 依赖重 / 需崩溃隔离 / 需运行时装卸」。

> go-plugin 的 14.8 MB 几乎全是 grpc+protobuf。可接受（`-s -w` 后整包 14.0 MiB），但**不要**为了让所有插件"可热插拔"而把默认实现做成进程插件。

### 3.1 process / wasm 运行时约束（参考 magpie 机制，不引入 JS 引擎）

| 约束 | 落地规则 | 必须交的验收 |
|---|---|---|
| **背压 credit** | 每个请求先拿有限窗口；chunk 只有在读者处理后才归还对应 credit。慢读者让上游等待，禁止宿主无界缓冲；cancel/timeout 必须归还并解锁 | 小窗口+慢读者：内存有界、顺序不乱；取消后无死等/泄漏 |
| **请求隔离** | wasm 可编译缓存，但每请求独立实例或经完整 reset 的池化实例；process 的请求状态随 ctx 生命周期，禁止全局可变会话。panic/崩溃不得污染下一请求 | 同插件两请求并发/串行，全局状态不串；第一次故意 panic，第二次仍正确 |
| **依赖自包含** | manifest 指向的 wasm/二进制及其依赖必须随插件包交付；禁止跨插件目录 import、隐式 node_modules/全局安装依赖。记录 hash、大小和入口 | 缺依赖/越界引用/ hash 不符在装载前 fail-loud；移到干净目录仍可运行 |
| **账号作用域（启用多账号时）** | provider/account/session id 必须随请求 ctx 显式传递，不使用进程级“当前账号”；缓存键包含账号作用域 | 同 provider 两账号并发，请求、配额、token、日志互不串号 |
| **进程协议选择** | Go 强类型插件默认 go-plugin；简单跨语言插件若另立 JSON-over-stdio 适配器，消息只用 `{id,method,params}` 与 `{id,result|error}`，逐行 JSON、stderr 持续排空且有界。**同一插件不得同时维护两套协议** | 任务书先选一种；stdio 另测半包/粘包、 stderr 洪峰、超时、进程退出和请求 id 对应 |

这些是后续 process/wasm 实现与审查的门槛，**不表示 wasmhost/pluginhost 已完成**。能 static/wasm 就不上 process；stdio 只用于确需低门槛跨语言的独立适配器，不重写 go-plugin 已有的握手、版本协商和健康检查。

---

## 4. 内核：四件事

```go
// 1) scope.go —— 生命周期 + 逆序撤销 + panic 隔离
type Scope struct{ parent *Scope; name string; ctx context.Context; cancel context.CancelFunc; mu sync.Mutex; disposers []func() }
func (s *Scope) Effect(fn func())     // 注册可撤销副作用
func (s *Scope) Child(name string) *Scope
func (s *Scope) Dispose()             // 幂等；先子后父；每个 disposer 包 recover

// 2) registry.go —— 编译期类型安全 + 运行期可替换
type Key[T any] string
func Provide[T any](s *Scope, k Key[T], impl T)
func Inject[T any](s *Scope, k Key[T]) (T, error)   // 缺失 = error，绝不返回零值

// 3) eventbus.go —— 五种分发（对齐 cordis events.ts）
func (b *Bus) Emit(e Event)                      // 广播，不等
func (b *Bus) Serial(e Event) error              // 顺序，全跑完
func (b *Bus) Bail(e Event) error                // 首个非 nil 即停
func (b *Bus) Waterfall(e Event) (Event, error)  // 洋葱，可拦截/改写
func (b *Bus) Parallel(e Event) []error          // 并发，收全部错误

// 4) assemble.go —— manifest → 依赖图 → 拓扑序实例化 → 启动 → 失败回滚
```

**要点**
- `Parallel` 必须在 goroutine 内 `recover`，否则一个监听器 panic 打穿宿主（dsc 踩过：`refs/dsc/core/eventbus.go`）。
- 泛型 Key 让类型错误在编译期暴露；**唯一**允许类型擦除的地方是跨进程插件的字符串 key。
- `Waterfall` 是工具流水线的骨架：审批、沙箱、spill、重复提醒、超时全挂在这条链上。

---

## 5. SPI 分区

DSH 有 104 个 service key。收敛原则：**一个 SPI = 一条能被单独替换的职责边界**，且跨进程可序列化。

| SPI | 语义 | 默认模块 | 层 |
|---|---|---|---|
| `LLM` | messages+tools → 流式事件 | `llm/openai`（magpie） | process |
| `Session` | 事件溯源 + 历史派生 | `session/jsonl` | static |
| `SessionCodec` | v4 编解码（reader/writer） | `codec/v4` | static |
| `Compaction` | 压力判定 + 摘要 + 折叠 | `compaction/basic` | static/wasm |
| `Tool` | Name / Schema / Exec | `tools/*` | static/process |
| `ToolRegistry` | 工具目录 + 中间件链 + 可见性 | kernel 侧薄实现 | static |
| `Approval` | 前置门控 | `policy/approval` | static |
| `Sandbox` | 写路径/命令准入 | `sandbox/local` | static |
| `Exec` | 子进程与持久 shell 会话 | `exec/bash` | static |
| `Job` | 后台任务表 | `job/local` | static |
| `FS` | 读写/搜索 + 观测策略 | `fs/local` | static |
| `AgentLoop` | turn/step 双循环 | `agent/react` | static |
| `Prompt` | 系统提示词装配 | `prompt/default` | static |
| `Instructions` | AGENTS.md 注入链 | `instructions/default` | static |
| `Skill` | 技能发现与注入 | `skill/local` | static |
| `State` | todo / plan 状态机 | `state/*` | static |
| `Mode` | preset 注册表 + 只读保护 | `mode/local` | static |
| `Subagent` | 子代理派生 | `subagent/local` | process |
| `MCP` | MCP server 接入 | `mcp/client` | static |
| `Transport` | 对外协议（ACP / HTTP / WS） | `transport/acp` | static |
| `UI` | 客户端渲染契约（ViewSpec） | `ui/tui` | static |
| `Credential` | 密钥读取与隔离 | `credential/env` | static |
| `Log`/`Metric` | 观测 | `obs/slog` | static |

---

## 6. 模式（preset）与工具可见性

官方机制（`research/23` §1⑧）：`dsh-agent-preset` 的 preset = 一份声明式插件组合行 `{id,name,description,order,plugins[]}`，注册进 `ctx.agentPresets`；Web 面把会话级工具从 host 层禁掉，**让每个会话挂一个 preset**，preset 决定该会话的工具、提示词段、委派后端。会话头记录 `agentPreset`。

我们的等价物：

```toml
# modes/standard.toml —— 内置、只读
id = "standard"
name = "标准"
builtin = true          # 用户层任何 patch 命中这一行 → 启动直接报错
tools = ["shell","read","write","edit","glob","grep","job_*","read_image","web_*","todo_write","present","ask_user_question","working_directory","subagent","history_search"]
prompt_sections = ["core","tools-catalog","env"]
```

| 规则 | 内容 |
|---|---|
| 内置模式 | `builtin = true`，编译进二进制；用户层 patch 命中即**报错退出**，不静默覆盖 |
| 用户模式 | `$DSH_HOME/modes/*.toml`，可从内置模式复制 |
| 工具可见性 | 由模式决定；**模式在一次会话内不变** → 工具表在会话内稳定，不击穿前缀缓存 |
| 自举工具 | `plugin_install/uninstall/list` 只在用户自建模式下可见；内置模式一律不给 |

---

## 7. 工具层（产品本体）

### 7.1 接口

唯一 canonical 接口与 Result 定义是 `internal/spi/tool.go`；执行摘要见 `AGENTS.md` §2。实现必须包含 `Name / Description / Schema / Caps / Exec`，失败返回带 Hint 的 Result 与 error，不得从本计划复制缺少 Description/Caps 的骨架。

**与 DSH 的差异（必须显式处理）**：DSH 的工具结果只有 `{isError, content[], meta?}`（`research/23` §3），会话 v4 只认这个形状。所以 `Result` 在**会话边界**要投影成 `content[]`（`Data` → 紧凑 JSON 块，`Summary` → 首块）。**结构化是宿主内的契约，v4 是边界上的编码**——这条投影函数是 M1 的验收项。

### 7.2 三条硬约束（中间件统一实现，工具作者禁止自己写）

1. **序列化层**：`Data` → JSON，键名 snake_case，禁止自由文本做主载荷。
2. **截断层**：超阈值自动外置 artifact + 指针；替换体本身永不超阈值（防「取回又被外置」死循环）。
3. **提示层**：`error` 必须带 `Hint`；CI 静态检查失败路径。

### 7.3 八条公理

| # | 公理 | 依据 |
|---|---|---|
| 1 | 工具输出必须结构化 | 85.5% 上下文是 shell 文本 |
| 2 | 每个工具输出必须有界 | `bash` 平均 648 字符；`web_fetch` 单次 14,195（该拆） |
| 3 | 结构化优先，shell 兜底 | `read`/`glob`/`grep` 的 token/信息比 >> `ls`/`cat` |
| 4 | 工具表在**会话内**稳定 | 12,209 token/请求走缓存；DSH 实测 4 种工具集变体（54/59/60/61）= 缓存击穿 |
| 5 | 错误信息是提示词 | 模型下一句怎么改取决于错误里写了什么 |
| 6 | 确定性输出 | 反斜杠、绝对路径、随机 id 污染上下文 |
| 7 | 少而稳的 schema 优于军刀 | 每加一个工具都有固定 token 成本 |
| 8 | 模型不该看见用不上的东西 | `restrict(filter)` 让隐藏工具连同 schema 一起消失 |

### 7.4 最初 P0 工具基线：18 个，目标 ≤ 6k token（历史范围，不是当前上限）

| 组 | 工具 |
|---|---|
| 执行 | `shell`（持久会话 + 活跃续命 + 自动转后台 + spill） |
| 任务 | `job_output` `job_kill` `job_list` |
| 文件 | `read` `write` `edit` `glob` `grep`（**独立工具，不合并进 shell**；共享观测策略：改前必须先读） |
| 感知 | `read_image` `web_fetch` `web_search` |
| 组织 | `todo_write` `present` `ask_user_question` `working_directory` |
| 派生 | `subagent` |
| 考古 | `history_search`（读旧会话/旧记忆，软着陆的关键工具） |

**最初 P0 不做（历史范围）**：`workflow`、`team_*`、`schedule_*`、`goal`、`skill`、`github`、`repo_here`、`gitbash_*`、`hindsight_*`（后者走 MCP）。后续已实现的工具不得按这份旧清单删除；新增工具仍须证明真实任务价值、能力声明、schema 稳定性和当前工具表 token 预算。

**shell 的执行模型（重要）**：**用真实 shell 进程**（本机 Git Bash），**不要**用 `mvdan.cc/sh` 在进程内解释。实测 Windows 上 mvdan 的进程替换是静默桩（`cat <(echo x)` 输出字面量 `TODO: support process substitution on Windows`）、POSIX 路径不映射（`cd /tmp` 失败）、外部命令全是 `exit status 127`。mvdan 只用于**静态分析**（判定命令是否写盘，喂给沙箱），不用于执行。

---

## 8. 提示词与指令装配

| 机制 | 规则 |
|---|---|
| 装配 | section 带 `order` 升序拼接；`complete:true` 的 section 只能有 1 个；变量 `{{name}}` 可选插值 |
| 工具目录 | 自动流入；`toolOrder` 必须恰好含一个 `<unlisted-tools>` 占位；列了不存在的工具名 → 每次装配都拒 |
| AGENTS.md | 注入顺序 **由宽到窄**：`$DSH_HOME/AGENTS.md` → `~/.agents/AGENTS.md` → 项目根 → cwd；每层可有 `.local` 覆盖层 |
| 注入形态 | **一条耐久基线消息**（不是每轮重发）；trim 后内容相同的文件只渲染一次 |
| 预算 | 设计目标为装配总预算 `maxBytes` 65536，降级先丢更宽层；**当前 Go 默认单文件截断 8000 字节**（以 `internal/prompt` 为准），因此项目 AGENTS ≤7000 字节，链接文档由任务显式阅读 |

---

## 9. 会话：canonical 模型 + v4 codec

### 9.1 分层

```
agent loop ──► canonical 事件（宿主自有 schema，含 Result 的结构化字段）
                    │
             SessionCodec (SPI)
                    ├── codec/v4  ← 读写 DSH 的 session.v4.jsonl.zstd（默认启用，可关）
                    └── codec/v5  ← 以后要新形状时加
```

### 9.2 v4 物理契约（照着实现，否则读不到历史）

| 项 | 规则 |
|---|---|
| 路径 | `projectKey(cwd)`：`/ \ :` → 各变一个 `-` 并折叠；`[A-Za-z0-9._-]` 且非 `~` 原样；其余每个 UTF-16 码元 → `~XXXX`（大写 4 位）；去前导 `-`；空 → `root`；截 **251**；包成 `--<key>--` |
| 目录 | `projectDir = root/<key>`；`cwd` 缺失 → `<root>/_no-cwd`；会话目录 = `projectDir/encodeSegment(id)` |
| 文件 | `session.v4.jsonl.zstd`；`.zstd` 后缀唯一决定编码 |
| 帧 | **第 1 帧只装 header 一行**（必须恰好一行、以 `\n` 结尾）；此后每个 durable 批次一帧（多行） |
| 压缩 | 只开 checksum，**不设 level**；magic `4247762216`；追加不重写已提交帧 |
| 批窗口 | 200 ms；`fsync` 后才 resolve；失败回滚到原长度 |
| header | 必填 `type,version,id,createdAt,isSeeded,delegationDepth`；可选 `cwd,parentSession,origin,agentPreset`；**多一个键即非法** |
| 事件 | 62 个 type；不在词汇表 → **拒绝**该日志，除非信封 `ignorable: true` |
| 派生 | 只有 5 类参与：`system/message` `developer/message` `user/message` `assistant/message` `tool/result`，且必须带 `surfaceOp` |
| v4 形状 | `tool/call.arguments` 是 **JSON 字符串**；`tool/result` 是 `role:'tool'` + 顶层 `toolCallId`；`request/header.header.system` 已退休（多一个 `system` 键直接拒） |
| 遮蔽 | 压缩只追加 `compaction/summary`（带 `shadowedRange/shadowedSeqs`），日志永不删；遮蔽发生在 surface 折叠 |

### 9.3 为什么值得兼容

Node 版可当 oracle：同一份会话两边各自派生 messages，逐条对比——这是验证「派生规则没写错」最便宜的测试。彻底迁移后关掉 `codec/v4`（reader 可选保留做考古）。

---

## 10. Agent 循环与压缩

```
turn: 收输入 → 装配 prompt+历史 → step* → 落 turn 结束事件
step: LLM 流式 → 工具调用 → 工具结果 → 再进 LLM（直到无工具调用）
```

- **压缩是插件职责**：`agent/pre-step` 走 waterfall，声明 `Provides compaction` 的模块接管；没有后端时 fail-loud（不静默退化）。
- **超窗兜底在宿主**：压缩后量最终表面，超窗即滑窗（从最旧丢到 90%），只在开头插一行切除说明；**只影响本次请求视图，日志一字不改**。
- **token 计量只有一个数**：`TokenMeter` = 服务端实测锚点（只承担绝对值）+ 启发式增量（只承担增量）。裁剪、压缩、提示文案、超窗判定全部只读它。

---

## 11. 客户端

| 阶段 | 交付 | 验收 |
|---|---|---|
| **M3** | ACP 服务端（stdio JSON-RPC） | **Martty 能连上并对话，流式收到 reasoning** |
| **M4** | TUI（bubbletea） | 日常任务能完成；`ViewSpec` 单一渲染器画 card/table/plain |
| **原生界面** | 独立 `dsh-ui` 仓，Go + Viva | 已拍板，见 `UIPLAN.md` §2.10；不得退回 Wails/Fyne/Gio 选型 spike，不使用 Electron/WebView 主壳 |

客户端只能经 `Transport` + `ViewSpec` 接触内核，所以界面仓与宿主独立构建；跨仓先过 H14 的 HTTP/WS/快照契约门，再谈真实体验。

---

## 12. 配置与 manifest

```toml
# dsh.toml
[profile]
name = "default"

[[module]]
id       = "compaction"
backend  = "wasm"                 # static | wasm | process
path     = "modules/gokifu-compact.wasm"
provides = ["compaction"]
requires = ["session"]

  [module.config]
  budget = 120_000
```

- **不支持 `!!js`**：官方用它做条件激活（`disabled: !!js "!ctx.get('profileContext')"`），我们用 `requires`/`when` 声明替代——语义等价、可静态检查。
- **覆盖层**：`dsh.toml` ← `dsh.local.toml` ← `--patch f`；按 `id` 后写覆盖，**整块替换 config，不 merge**（官方语义）。
- **内置模式只读**：`builtin = true` 的行被用户层命中 → 报错退出。

---

## 13. 版本与追更

- 版本号规则 = 稳定版为上游版本 + `-go.N`，测试版为上游版本 + `-beta.N`；前缀升级后 N 从 1 开始，当前 N 以源码与 CHANGELOG 为准。
- `CHANGELOG.md` 维护映射表：`go/<当前版本> ← rc.5`，列**移植了什么、跳过什么、为什么**。
- 跟进方式：新上游版本 → 新分支 → 只搬「契约变化」（会话格式、工具语义、ACP 方法）+ 我们要的功能；**不搬**客户端 UI 与 provider 生态。
- 先例（`HANDOFF.md` §1.8）：`rc.1 → rc.5` 抽样 20 包 18 个字节级零变更；`0.1.7 → 0.2.0` 只有 `dsh-session` 变了。**追更成本主要在 minor 边界。**

---

## 14. 里程碑

> **当前执行提示（2026-11-03）**：下表是最初 M0–M5 的范围与验收定义，不是让接手者重做 M1/shell 的工作单。当前宿主顺序**只以 §14.1 为准**；跨线证据看 `docs/transport-contract.md`，界面路线看 `UIPLAN.md` §2.10。历史 ✅ 仍须对当前版本复验。

| # | 交付 | 验收（跑得出来） | 规模 |
|---|---|---|---:|
| **M0** | 内核 + 装配器 + manifest + 模块图 | `dshd modules graph` 打出依赖树；在隔离副本抽掉 `modules/` 仍能 `go build ./internal/...`；禁用任意模块报「缺 X」 | 2–3k |
| **M1** | 工具 SPI + 18 工具 + 三条中间件 | 一致性套件全绿；越界写被拒；spill 后能取回；`Result → content[]` 投影有 golden test | 6–8k |
| **M2** | LLM(SSE) + agent loop + 会话 v4 codec | `dshd run "…"` 跑通；**Node 版能读 Go 写的会话**；两边派生的 messages 逐条相同 | 6–8k |
| **M3** | ACP 服务端 | **Martty 连上并对话** | 4–5k |
| **M4** | TUI + 持久 shell + 后台任务 | 用 TUI 完成一次真实任务；内存 < 150 MB | 4–5k |
| **M5** | 压缩协议 + token meter + 模式系统 + `history_search` | gokifu 以 wasm 接入，fold 语义不变；内置模式不可改 | 4–6k |

**最初顺序不可换**：M0 → M1 → M2 → M3。先 `spi/tool.go`，不要先写 loop（`HANDOFF.md` §12）。这是历史依赖记录；当前不得据此重做这些阶段。

### 14.1 当前宿主执行顺序（2026-11-03）

| 门 | 先做 | 通过后才进入 | 证据 |
|---|---|---|---|
| **G0 契约可连接** | **H14**：统一 HTTP 裸参数 body、`/ws`、控制帧、服务端 `{seq,type,data,time}`、14 method 与两份快照/golden；每会话 Transport journal 统一 seq，WS/history 同源且 history 返回原始 frames，journalEpoch/reset 处理重启 | H6/H13 跨仓联调 | 同一 fixture 驱动宿主、UI、mockd；真实 UI apiclient 对**契约夹具**验 create/load/prompt/approve、合成 reasoning/tool/approval 重放、两会话不串流及 stale_epoch 重置；真实生成归 H6，真实审批执行归 H13，不在 G0 循环前置 |
| **G1 宿主执行链** | H6 原始 text/reasoning/tool 流；H13 `approval/request` → `session.approve` → 子进程继续/拒绝；H8 所有错误出口 | 跨仓整链 | 宿主宰临时 home 中端到端测试，允许执行、拒绝不执行、超时、取消均有真实结果 |
| **G2 跨仓消费** | UI 按 H14 适配路径/帧/cookie/每会话连接；接 H1–H13 字段与动作 | 原生产品验收 | 同一新 dshd + 同一 UI + 临时 home；不得用 mockd/ACP 代替 |
| **G3 产品接管** | 真实任务、同任务 A/B、资源与长会话 | 发布候选 | 完整工作日、整进程树内存、冷启动、当前工具表真实分词；所有未验项明示 |

#### Lead 派单表（每次开工、完成通知、定时复核时更新）

状态只用 **READY / IN_PROGRESS / BLOCKED / DONE**；DONE 必须附 rev、命令、真实断言和证据路径。**Lead 只拆包、派单、集成、验收，不自行串行实现。** 所有依赖已满足且 owned paths 不重叠的 READY 必须同轮派给子代理或有上下文成员；派后不 wait/poll/空等，完成方主动通知。Lead 收通知后验收、更新本表并立即派下一 READY；表空先按 G0–G3 未验缺口补表，禁止自行扩范围。

| ID | 状态 | 执行者与依赖 | owned paths | 交付与验收 |
|---|---|---|---|---|
| W0 | DONE · W33/W34/W49/W56 Lead 分层验收 | H14契约门：golden/交接/重启原始history/MSGID全收口 | `docs/transport-contract.md`、两仓快照/golden、`docs/message-identity.md` | 25方法与双会话golden一致；W34交接/time；W49 `OK=84 FAIL=0`恢复16帧；W56真实Event.Seq投影v4Seq，load↔WS精确去重、同文不同身份保留、身份比对OK=76；双仓门禁与相关race绿 |
| W1 | DONE scoped · W34/W49/W37 Lead 分层验收；完整工作日归W7 | journal+serve umbrella：交接、跨进程恢复与120s scoped性能收口 | `internal/transport/`、`cmd/dshd` journal/serve、`docs/perf-120s.md` | W34交接/乱序/pending/time count100；W49恢复16帧OK=84；W37真实120.019718s、5会话4415帧、OK=136 FAIL=0、journal21/process5/selftest10全绿。只证明当前合成流scoped阈值，不报历史提升百分比，不外推真实模型/可见GUI/完整工作日 |
| W2 | DONE · W33/W45/W49/W56 Lead 分层验收 | UI契约：快照/golden/重启认证/原始history/MSGID全收口 | `../dsh-ui/internal/apiclient/`、`cmd/mockd/`、测试/快照 | UI快照25方法与共享golden一致；旧cookie/token401、新token303、stale/reset通过；W49跨进程16帧恢复OK=84；W56真实apiclient乱序重放同文user/assistant各两条恰4条、身份OK=76；全仓及race绿 |
| W3 | DONE scoped · W34 Lead 集成验收；真实UI整链归W7 | H6 serve跨轮工具/debug统一journal/time | `cmd/dshd/serve_journal_regression_test.go`、`internal/transport/serve_journal_handoff_test.go`、`docs/serve-journal.md` | 跨轮同工具只call,result,call,result且seq1..4/time>0；debug两轮同journal/history/time；交接/乱序count100与定向race绿。真实dshd+UI联合长跑与重启history不在本scoped项 |
| W4 | DONE · `dfd73da` M398 | H13 pending cancel 已收口：cancel→拒绝、工具不执行、`approval/cancel`、本轮收工后放锁 | `cmd/dshd/serve_approval*.go`、`serve_tools.go`、`transport_prompt.go`、`scripts/h13-cancel-e2e.py` | 新增267行定向测试；真机先红 `OK=6 FAIL=1`（缺cancel帧）后绿 **`OK=9 FAIL=0`**，证据 `docs/host-side-closeout.md` §5.3；当前宿主全仓 test/vet/build exit 0。UI同版本消费/reset恢复归U1-H13，不外推 |
| W5 | DONE · `0426d9e` M399 | H8 错误体已统一为 `errorObject` 单构造点，含 stale_epoch/WS文本旁路 | `internal/transport/server.go`、`handlers_extra.go`、`ws.go` 与授权测试 | 全仓测试实跑270次请求逐条断言四字段（code/message/hint/retryable），修后宿主全仓 test/vet/build exit 0；真实UI消费归跨仓验收，不把契约错误体完成外推整链 |
| W6 | DONE · 18:08 复核 | Lead 核对两仓方向、官方结构树主库与桥接当前状态；宿主 `af4a49a` / UI `3925e84` | 只读两仓AGENTS/PLAN/契约/状态，更新派单表，不改产品代码 | 报告 `docs/scheduled-review-2026-11-05-1808.md`；已建 `$DSH_HOME/dsh-ui-official/` 主库与 `$DSH_HOME/DSH-UI-official.md` 入口，63项清单/5结构文档/57素材，镜像哈希一致；UI settings与上游typecheck exit0，W109/W110/W111在途 |
| W7 | BLOCKED(无打扰真实UI窗口；W36/W49/W56 scoped DONE) | 跨仓真实链 umbrella：进程内/重启history/MSGID通过，产品终验未完成 | `scripts/real-chain-headless.py`、`scripts/restart-recovery.py`、证据 | W36 OK=72、W49 OK=84、W56身份OK=76；剩余可见GUI、完整工作日/A/B与真实OS输入，不用历史22/39/85 PASS代替当前整链 |
| W8 | DONE · 复用 UI U4P 设计（base `bf7e888`，owner `f29400bd`，文档已提交 `bc79784`） | 不再另写宿主重复方案；A/B 尚未执行 | 唯一计划 `../dsh-ui/docs/ab-acceptance-plan.md` | 验收命令 `test -f ../dsh-ui/docs/ab-acceptance-plan.md && grep -q T01 ../dsh-ui/docs/ab-acceptance-plan.md` **exit 0**；`wc -l`=735，10 任务/控制变量/最多 3 次/成功率/返工/token/日志模板齐全。**只设计未执行**；官方 Electron 需真人启动，后续执行另列 U4，不在本仓复制计划 |
| W9 | DONE · 复用 UI PERF 设计（base `bf7e888`，owner `04e622a3`，文档已提交 `bc79784`） | 不再另写宿主重复方案；10 项尚未实测 | 唯一计划 `../dsh-ui/docs/perf-gap-plan.md` | 验收命令 `test -f ../dsh-ui/docs/perf-gap-plan.md && grep -q G8 ../dsh-ui/docs/perf-gap-plan.md` **exit 0**；`wc -l`=308，10 缺口/整进程树/计时边界/证据路径齐全，零改动可测 G1/G6/G7/G8。后续实测由 PERF0 承担，不冒充实测完成 |
| W11 | DONE · 原 owner `9dff3286` 失败，Lead 接管 · base `4f7c885` 两态 | A=committed、B=+wssession patch；隔离执行，不写共享实现 | `docs/h-suite-validation-2026-11-03.md` | 两态 `bin-check`/`build`/`vet`/`test`/`h-suite --fast`/`race` 均 **exit 0**；h-suite fast 非模型探针 **40/40**，6 条真实模型探针按设计 SKIP；race 合计 OK=1 FAIL=0。当前 HEAD `83eaa92` 另跑 test/build/vet exit 0，但未重跑 race/h-suite；不能外推。原 owner 未完成原因未查明 |
| W12 | DONE · 原包 `ff844228`、final `61ab70a0`，Lead 集成验收 | 默认 loopRunner/modelRunner 下一轮覆盖已接入并集成宿主共享树 | `cmd/dshd/serve_model_override.go`、`serve.go`、`serve_turn.go`、`serve_loop*.go` 及测试、`docs/model-wire.md` | integrated patch SHA256 `6e5b3cad…`；Lead 五项关键测试逐项 PASS，最终及共享树 test/vet/build 全绿。A/B、清空、重启、损坏store、v4不变闭环；真实 magpie/UI 真机、-race/h-suite 与完整工作日另验 |
| W13 | DONE · Lead 修复并验收（2026-11-04） | compact 投影删空 type/properties，导致 job_list 被 WPS 503；根修后真实 wire 已完整 | `cmd/dshd/tool_merged.go`、`cmd/dshd/tool_schema_test.go`、`internal/schema/schema.go`、`docs/tool-schema-503.md` | 新增单点+57工具全表守卫；`go run ./cmd/dshd tools --tools-mode compact` 实测 job_list=`{"additionalProperties":false,"properties":{},"type":"object"}`；全仓 test/vet/build 全绿。真实WPS 200未重放（用户证据已给），运行中DSH GO.exe未替换 |
| W14 | DONE · owner `c6a929b8`，Lead 集成验收 | TUI 首屏按 MIT 上游 `ccch1mneyyy/dsh-TUI` 源码一比一移植 | `cmd/dshtui/**`、`cmd/dshtui/assets/**`、`docs/tui-visual.md` | patch SHA256 `718bb111…`，16文件。鲸鱼精灵与上游 `whaleFrames.ts` 逐字节（25×40→13行半块）、标题块56列、阶梯98/56/40；消息`❯/●/thinking`、上下文条/状态行/输入；120×36/80×24/40×10行数准确；`go test ./cmd/dshtui` 103 PASS、全仓 test/vet/build 绿。未做动画/OSC11/富文本；effort 仍 ? |
| W15 | DONE · owner `c7f541ad`，Lead 集成验收 | TUI 消息层按 MIT 上游源码移植：Markdown、代码块、工具卡、thinking | `cmd/dshtui/**`、`cmd/dshtui/testdata/**`、`docs/tui-message.md` | 原包 `b13bce9d…`；共享增量 SHA256 `f27694a0…`，19文件，避免重复首屏。段落/标题/列表/行内code+bold、围栏语言框、工具卡真实状态/参数/显式耗时/两级折叠、thinking；`--dump-transcript`；共享120×36/80×24及转储均36/24行；`go test ./cmd/dshtui`+全仓test/vet/build全绿 |
| W16 | DONE · owner `095a3630`，Lead 集成验收 | TUI 上游素材：22鲸鱼帧+三段开屏/idle节奏+Gentle Mist主题 | `cmd/dshtui/assets/**`、`docs/tui-assets.md` | patch SHA256 `18aef5d5…`，27新增文件。22帧均25×40逐帧sha256；classic18/heart7/sleep11步及独立层节奏；dark/light各81色。`python cmd/dshtui/assets/verify_assets.py` OK=1288 FAIL=0，三类变异全拦截；全仓test/vet/build全绿；未改Go代码 |
| W17 | DONE · 三路只读审计 `a533e2a6`/`77374fd9`/`c11a8dca` | DSH `0.2.2-rc.1` 兼容性审计完成：官方release vs 当前 `0.2.1-rc.5` | `tmp/compat-0.2.2-rc.1/{host-transport-model.md,ui-tui.md,tools-runtime.md}` | 官方 tag `dsh-v0.2.2-rc.1`，notes sha256 `e2682d8c…`；宿主16项、UI/TUI18项、工具18项均file:line+分级+必红测试；真实v4两批511文件读取PASS；定向Go测试全绿；裸参数/ws/journal/epoch未改，基线未升 |
| W18 | DONE · owner `2cf6990c`，Lead 集成验收 | ACP 工具声明/执行共享模式视图，隐藏/未知工具fail closed | `cmd/dshd/acp.go`、`tool_execution_guard_test.go`、`docs/tool-execution-guard.md` | patch SHA256 `1cac95e2…`，3文件。每轮同一view供Loop.Tools与执行守卫；ask=nil也过闸；直接/嵌套/ask=nil隐藏及未知底层Exec=0，允许工具/审批记忆/嵌套语义保留；修前四路红、修后绿；全仓test/vet/build全绿 |
| W19 | DONE · owner `59ccd3cb`，Lead 集成验收 | 完整工具参数格式错误在执行任何工具前终止响应 | `internal/agent/loop.go`、`loop_test.go`、`docs/tool-args-failfast.md` | patch SHA256 `c9b9e50f…`，3文件。保留assistant原始tool_calls事实；任一非JSON object在首个runTool2/Runner/EventToolCall前终止；混合及首/中/尾执行数严格0，全合法正常2次；旧测试替换为加强断言；全仓test/vet/build全绿 |
| W20 | DONE · owner `2709cc63`，Lead 集成验收 | setPermission执行一致、delete先收工、Cancel超时不假空闲 | `cmd/dshd/{serve.go,serve_loop.go,serve_session_ops.go,transport_prompt.go}`、测试、`docs/serve-lifecycle.md` | patch SHA256 `3a3ec56c…`，9文件。真实argv/load/set共用store，A/B/base/清空/重启一致；delete屏障挡新prompt，取消并等真收工后RemoveAll，超时保留目录/live turn；Cancel超时不清busy/结果保留；定向race 6组绿，全仓test/vet/build绿 |
| W21 | DONE · 修正owner `61dd68c3`，Lead 集成验收 | workflow 嵌套失败保留真实 Text/Data/Artifacts/View，恢复冻结语义 | `modules/tool/workflow/workflow.go`、`workflow_test.go`、`docs/workflow-error-text.md` | v2 patch SHA256 `2b4a05ad…`，3文件。普通单步失败整体err=nil、后续继续、Data.ok=false/failed=1；trace actual-body/Data/Artifacts/View/Hint及OK=false/isError=true；仅通道hardErr上抛；原单步失败测试原样恢复；全仓test/vet/build全绿 |
| W22 | DONE · owner `6d60ef25`，Lead 集成验收 | 原生工具失败投影保留原始res正文/结构字段并附加err | `internal/agent/loop.go`、`loop_test.go`、`docs/agent-error-text.md` | patch SHA256 `03c89908…`，3文件。shown从原res复制，Text/Data/Artifacts/View/Hint保留；仅无Text合成错误；actual-body在模型消息/tool-result均存在且IsError=true；W19闸与取消语义保留；全仓test/vet/build全绿 |
| W23 | DONE (security fix 2026-11-07) · owner `cc44281c`，Lead 集成验收 | 本地文件前导 `~`/`~/` 展开且写不越界 | `modules/fs/local/local.go`、`tilde_test.go`、`docs/tilde-paths.md` | patch SHA256 `167fca19…`；2026-11-07 审计发现 `~` 写 home 绕过 `within`，已加 `withinWrite`：`Read/Stat` 可读 home，`CheckWritable/Write` 只允许工作区根；`go test ./modules/fs/local`+vet exit 0 |
| W24 | DONE · owner `e7b443c5`，Lead 集成验收 | 全局/项目AGENTS各自maxBytes，合计≤2倍 | `internal/prompt/**`、`cmd/dshd/acp.go`提示装配、`docs/prompt-budget.md` | patch SHA256 `368d4ee8…`，4文件。全局home/AGENTS与项目各8000独立截断，默认合计≤16000，自定义≤2N；任一超大不挤另一份；无全局逐字兼容，ExtraRulesPath不变；全仓test/vet/build全绿 |
| W25 | DONE · owner `ccf3b0b9`，Lead 集成验收 | GUI accepted后才清草稿，发送中只读，失败保留可重试 | `plugins/dscomposer/**`、`cmd/app/wpc1_send.go`及相关测试、`docs/gui-send-ack.md` | patch SHA256 `e27d5002…`，8文件。真实Viva ReadOnly；Sending保留草稿/禁止重入；Prompt成功或durable user/turn确认后清；失败保留Retry；A/B/迟到/重复确认不重复清或复制user；全仓test/vet/build全绿 |
| W26 | DONE · owner `b2ad83a1`，Lead 集成验收 | TUI accepted前不显示/清草稿，发送中只读 | `cmd/dshtui/**`、`docs/tui-send-ack.md` | 最终patch SHA256 `121d0059…`，6文件，最终副本与共享逐文件一致。异步HTTP+pending只读；accepted:true才清草稿并释放暂存帧；durable user/message唯一正文；失败可重试无幻影；id+sid/turnId隔离迟到结果；11条定向+全仓全绿 |
| W27 | DONE · owner `c3a8dfb4`，Lead 集成验收 | 多选问题全部recommended预选，单选行为不变 | `plugins/dsquestions/**`、`docs/questions-recommended.md` | patch SHA256 `6f9bc5e1…`；全部推荐预选/无推荐全不选；切换不发送、确认一次；A/B批次隔离；两尺寸headless+全仓全绿 |
| W28 | DONE · owner `ebc564c3`，Lead 集成验收 | 未声明自定义模型容量1M/128K，显式覆盖优先 | `internal/llm/capacity.go`、`internal/agent/loop.go`、`cmd/dshd/acp.go`容量、`docs/model-capacity.md` | patch SHA256 `cc2ead60…`，7文件。单一ResolveModelCapacity；未声明自定义十进制1M/128K并接MaxTokens；32K/8K/较小端点覆盖，A→B→A模型/会话隔离，内置已知模型128K/MaxOutput0不变；ACP新增--max-output；Transport/models.list/auto-compact未改；全仓全绿 |
| W29 | DONE · owner `3f32f4a6`，Lead 集成验收 | 文件链接Windows标签/列后缀/无效#L/行高亮 | `plugins/dsconversation/file_refs.go`、`wpc1.go`、`cmd/app/wpc1_actions.go`、测试、`docs/file-links-rc1.md` | patch SHA256 `98db5976…`，7文件。官方 `:24:5`/`:24-30`与Windows编码路径；无效裸#L仍打开；应用内预览定位、7行高亮/700ms淡出、刷新offset不变；越界0高亮、http(s)只网页；全仓test/vet/build全绿 |
| W30 | DONE · owner `34ebf92d`，Lead 集成验收 | schedule整单元JSON版本白名单，legacy可读/save写当前 | `internal/schedule/{schedule.go,version.go}`、`modules/tool/schedule/schedule.go`、测试、`docs/schedule-version.md` | patch SHA256 `2cc27634…`，6文件。current=1/白名单{1}；无版本才legacy；0/999/null/非法类型fail-loud且不覆盖原文件；save/Advance写1；allow_writes legacy仍false；全仓test/vet/build全绿 |
| W31 | DONE · owner `8f708f11`，Lead 集成验收 | GUI断线状态复位与运行计时暂停/恢复 | `internal/apiclient/mux.go`、`cmd/app/{main.go,disconnect_clock.go,wpc2_runtime.go}`、测试、`docs/gui-disconnect-clock.md` | patch SHA256 `7a5af03c…`，9文件。topic+generation open/down；迟到close丢弃、A/B隔离；5s→断线100s仍5s→重连5s→7s；真实turn/start重锚；Snapshot.Disconnected/Status真值；新增测试count=10及全仓全绿 |
| W32 | DONE · owner `481bc5aa`，Lead 集成验收 | TUI断线状态与运行计时暂停/恢复 | `cmd/dshtui/{app.go,client.go,connection.go,layout.go,script.go}`、测试、`docs/tui-disconnect-clock.md` | patch SHA256 `b256a18d…`，7文件。sid+订阅代际+WS代际open/down；旧close/旧订阅隔离；runningAccum/runningSince断线冻结、重连累计；新增定向count=20、全仓全绿，120×36/80×24不溢出 |
| W33 | DONE · owner `a4b23ef7`，Lead 集成验收 | H14共享快照/golden、mockd epoch与真实apiclient重放 | 宿主 `docs/transport-contract.md`、`internal/transport/testdata/**`；UI `testdata/**`、`cmd/mockd/**` | host patch `7c1bbda3…`、UI patch `693ffaf2…`。三份golden双仓同哈希；宿主5组golden、UI真实apiclient+mockd五组全PASS；25方法/ws帧/journalEpoch/raw history/turn-error/reset/stale_epoch；MSGID无v4Seq/messageId未编字段；两仓全仓绿，实链time缺口归W34 |
| W34 | DONE · owner `cea9c6f3`，Lead 集成验收 | serve跨轮去重、debug统一journal/time、重放补洞与envelope time | `cmd/dshd/serve_approval.go`、`internal/transport/wssession.go`、两份新测试、`docs/serve-journal.md` | patch SHA256 `51c69802…`，5文件。replay闸→Subscribe→二次Window→journal重放；乱序补洞/重复去重/pending按requestId去重；reset/pending time>0；count100、W20/pending cancel12项、两组定向race及全仓绿；真实apiclient golden双会话通过，未外推性能/重启 |
| W35 | DONE · owner `3c1827c3`，Lead 保存证据并复核 | 两仓当前全仓race验证，只读不改产品 | `docs/race-current.md`、`tmp/race-current/**` | MinGW16.2/CGO1：HOST_RACE 59包 exit0/0 DATA RACE；UI_RACE 40包exit1，`cmd/app` 6段报告（非6根因），F2单次3段、WPC1 count5复现1段；kernel 93行依赖仅自身，CGO0两仓build exit0；报告sha256 `012baa7a…` |
| W36 | DONE · owner `a1ce9cfe`，Lead 保存证据并复核 | 同一新dshd+真实UI apiclient+临时home无窗整链 | `scripts/real-chain-headless.py`、`docs/real-chain-headless.md`、`tmp/real-chain-headless-*/**` | `RCH_SUMMARY OK=72 FAIL=0 SKIP=0`，go_test/script exit0。bin-check OK=1；真magpie、reasoning430B、allow真写/deny不执行/pending cancel+第二轮；FillGap 7..12六帧、stale/reset/A-B隔离；首跑65/6证据保留；未验宿主重启归W45 |
| W37 | DONE scoped · owner `53191b86`，Lead 独立复核并集成 | 真实cmdServe/Transport/journal 5会话WS×120s性能复测 | `cmd/dshd/perf_120s_probe_test.go`、`scripts/perf-120s*.py`、`docs/perf-120s.md`、隔离证据 `tmp/perf-120s-final/delivery/` | patch SHA256 `0f6ec3a9…`。墙钟120.019718s，`OK=136 FAIL=0`，RTT p95=87.6644ms、分发p95=120ms、WS峰值219.27MiB、Private421.91MiB、handles1172/线程98；4415帧无断号/串流。Lead复核JOURNAL21/PROCESS5/SELFTEST10与全仓门禁全0；非真实模型/GUI/生产无插桩 |
| W38 | DONE · owner `1d8d3476`，Lead 集成验收 | WS permessage-deflate可选压缩与旧客户端兼容 | `internal/transport/ws.go`、`cmd/dshtui/{client.go,connection.go}`、UI `internal/apiclient`、测试、`docs/ws-compression.md` | host patch `cbb8c71d…`、UI patch `7a614260…`。默认offer/双向no-context-takeover、旧路径回退、1MiB解压上限/坏帧/分片/炸弹拒绝；20轮压缩+10轮重连与coder互通；Lead两仓全仓test/vet/build及相关race均0，快照/golden哈希不变 |
| W39 | DONE · owner `be5dfcd9`，Lead 集成验收 | TUI鲸鱼开屏三段+idle独立层动画 | `cmd/dshtui/{animation.go,animation_app.go,app.go,layout.go,screen.go,splash.go}`、测试、`docs/tui-animation.md` | patch SHA256 `3589c6be…`，8文件。classic18/heart7/sleep11步与ms节奏；tail/fin/heart/sleep/blink独立层、10s睡眠/唤醒；单timer无goroutine；16新增测试+全仓绿；Lead用MinGW补 `go test -race ./cmd/dshtui` exit0/0 race；资产OK=1288 |
| W40 | DONE · owner `cac6720d`，Lead 保存报告并复核 | 官方0.2.2设置中心缺口只读审计 | `../dsh-ui/docs/settings-gap-0.2.2.md`、`../dsh-ui/tmp/settings-gap/**` | 官方11个ui-settings包≠11导航页，实为5 section/12通用动作；当前四页仅外观完整，模型截断6条/权限当前会话；P0拆日程真值/模型全量/宿主插件与模型目录；96引用校验+两尺寸14图+六关联包绿 |
| W41 | DONE · owner `1cd970b3`，Lead 保存报告并复核 | 插件授权/禁用/替换/卸载只读审计 | `../dsh-ui/docs/plugin-lifecycle-audit.md`、`../dsh-ui/tmp/plugin-lifecycle/**` | 225行、193个file:line链接0错、29包测试绿。公开槽10/12实画、核心5个强制装回、Provides/Requires不授权、Dispose/注册回滚残留；当前第三方真实替换官方=0；PL-1..PL-5后续已拆，静态装配不冒充动态生命周期 |
| W42 | DONE · owner `ab33c637`，Lead 集成验收 | 插件公开槽/核心禁用/依赖/Dispose核心闭环 | `cmd/app/**`、`internal/uikernel/**`、`internal/uiplugins/**`、`plugins/dsdecl/**`、`docs/plugin-lifecycle-core.md` | patch SHA256 `977e9011…`，12文件。公开槽Go/JSON注册期fail-loud；核心5个enabled:false生效；Load原子回滚、Unload/Replace依赖重注与回滚；Dispose清运行态/Effect取消且幂等；正常/-shot退出真实Dispose；两尺寸+替换恢复+全仓绿 |
| W43 | DONE · owner `ef0420e4`，Lead 集成验收 | 修UI全仓race发现的6段DATA RACE报告 | `cmd/app/{main.go,wpc1_send.go,session_compact.go,session_fork.go}`等6文件、`docs/ui-race-fix.md` | patch SHA256 `6d55b209…`，7文件。Paint所有权UI队列、完整不可变快照、后台WaitGroup收工、Outgoing逐帧复制；F2/WPC1各-race-count20绿；Lead全仓 `go test -race ./...` exit0/0 DATA RACE，普通test/vet/build全绿；真实GUI/真dshd未验 |
| W44 | DONE · owner `c291b881`，Lead 集成验收 | plugingen自有生成/幂等/负例/scratch构建测试 | `cmd/plugingen/{main.go,scan.go,main_test.go}`、`docs/plugingen-tests.md` | patch SHA256 `d3c10802…`，4文件。首次/二次字节+mtime幂等、7负例先红后修；scratch真实go mod tidy/test/build且Registered精确；产品go.mod/go.sum/plugins_gen.go未变；全仓test/vet/build全绿 |
| W45 | DONE · owner `11e69e3d`，Lead 保存失败证据并复核 | 跨进程宿主重启cookie/epoch/reset与v4连续性验证 | `scripts/restart-recovery.py`、`docs/restart-recovery.md`、`tmp/restart-recovery-20261105-010633/**` | 实跑 `OK=79 FAIL=5 SKIP=0`，exit1（4条旧history内容FAIL+phaseB非零，不是5 bug）。旧cookie/token401、新token303、stale/reset/epoch变化/新prompt/v4 append-only全通过；**新epoch history=0，旧text/reasoning/tool/approval原始帧缺失**，最小复现与VERIFY_SUMMARY OK=40已保存；修复归W49 |
| W46 | DONE · owner `ead56792`，Lead 集成验收 | 自动化任务页接入全局只读ScheduleList真值 | `plugins/dssidebar/{frame.go,frame_test.go,schedule_truth*}`、`docs/schedule-truth-panel.md` | patch SHA256 `9cd21016…`，5文件。nil=宿主未提供、空=当前没有、非空Title/Kind/NextAt；每帧≤1次、A/B/Action撤掉无残留；32条两尺寸滚动+真实headless点击；无mutation死按钮；全仓全绿 |
| W47 | DONE · owner `4419a098`，Lead 集成验收 | 模型目录24+全量可达、滚动/键盘跟随 | `plugins/dssettings/**`、`docs/models-all-reachable.md` | patch SHA256 `aedc3b0e…`；共享已有部分用逐文件delta集成。24条顺序/逐条SetModel/空串清覆盖、A/B/未知不勾、1182/1920滚轮HitTest+键盘末项；全仓全绿；加载/空/错误因公共契约无状态只能合并不可用，独立三态归W51 |
| W48 | DONE scoped · owner `c3ce5cee`，Lead 集成验收；主分派归W50 | 宿主插件包list/install/enable/disable/uninstall/update/restore/reconcile竖切 | `internal/pluginmgr/**`、`cmd/dshd/serve_plugins*.go`、additive snapshot/golden、`docs/host-plugin-manager.md` | patch SHA256 `bd250f6f…`，12文件。严格manifest/hash/路径/symlink；staging+rename+原子state，更新失败保旧/restore/uninstall trash回滚；helper进程exit86后reconcile；8方法HTTP/CLI适配器与全仓绿。**真实dshd主分派尚未接，不能称产品完成** |
| W49 | DONE · 重启接续owner `5fcd0c2c`，Lead 集成验收 | 跨进程Transport journal原始帧持久化 | `internal/transport/{journal.go,journal_persistence_test.go}`、`cmd/dshd/{serve.go,serve_journal.go,serve_journal_store.go}`、测试、`docs/journal-restart.md` | patch SHA256 `b071b6fe…`，7文件。每会话append-only `transport-journal.v1.jsonl`，原始seq/type/data/time，启动只读恢复，失败不占seq，torn-tail下一次append核对后仅截未提交区间；真实探针 `OK=84 FAIL=0 SKIP=0`、FINAL_VERIFY OK=17，16帧逐字恢复；Lead全仓门禁/定向race全绿 |
| W50 | DONE · owner `b9cb1528`，Lead 集成验收 | 插件管理真实主分派/serve接线与CLI | `cmd/dshd/{main.go,serve.go}`、`internal/transport/server.go`、两份glue测试、`docs/plugin-manager-glue.md` | patch SHA256 `b8255eec…`，6文件。真实dshd CLI 13调用OK、serve 26步OK；8方法未认证全401、认证后全生命周期、A/B home与重开reconcile；CLI/serve同 `<home>/plugins`；未改25 canonical；Lead全仓test/vet/build及定向race均0 |
| W51 | DONE · 重启接续owner `405ac20e`，Lead 三方合并验收 | 模型目录loading/loaded-empty/error/ready显式状态 | `pluginapi/{actions.go,types.go}`、`cmd/app/{main.go,models_state.go}`、`plugins/dssettings/**`、`docs/models-list-state.md` | patch SHA256 `3d33e076…`，11文件；声明归位与非冲突文件采用完成副本，model.go手工合并保留W53关于区。真实Error/新鲜度、在途去重+代际、错误保留旧缓存、重试/Action缺失隐藏；两尺寸四态+24条滚动/A-B/空串；全仓test/vet/build全绿 |
| W52 | DONE · owner `32d2cd34`，Lead 集成验收 | 设置通用页真实快捷键参考入口 | `plugins/dssettings/{model.go,settings.go}`、`plugins/dsshortcuts/**`、`docs/shortcut-reference-row.md` | patch SHA256 `839d4fe9…`，8文件。Action nil整区隐藏；点击先关设置再开同一F1面板，点击/F1截图逐字节一致；两尺寸/24模型滚动通过。Lead合并W55后为内建ds-shortcuts补可选服务元数据，授权与全仓test/vet/build全绿 |
| W53 | DONE · 重启接续owner `ab02b8af`，Lead 集成验收 | 设置关于/版本真实构建行 | `cmd/app/version.go`、`plugins/dssettings/{model.go,version.go}`、测试、`docs/product-version-row.md` | patch SHA256 `4459621d…`，14文件含前置设置改动，按完成副本delta集成。Version与brand/vsinfo同源、Commit ldflags/VCS、GoVersion runtime；逐字段缺失隐藏/全空整区隐藏；真实ldflags注入与dev/VersionInfo通过；两尺寸40位Commit不溢出，快捷键/24模型保留；全仓全绿 |
| W54 | DONE · owner `74c2c5a2`，Lead 集成验收 | JSON单件禁用/恢复与失败回滚 | `plugins/dsdecl/**`、`internal/uidecl/**`、测试、`docs/json-lifecycle.md` | patch SHA256 `acde795e…`，9文件；因共享前置差异按完成文件delta集成。声明ID白名单/enabled:false、A/B隔离、恢复不重复、普通坏文件隔离且Errors可查、Reload明确restart-required、Dispose清空；公开无消费槽仍拒绝；全仓test/vet/build全绿 |
| W55 | DONE · owner `9ab64333`，Lead 集成验收 | 插件Provides/Requires服务授权边界 | `pluginapi/plugin.go`、`internal/uikernel/{kernel.go,service_auth_test.go,official_service_auth_test.go}`、`docs/plugin-service-auth.md` | patch SHA256 `88a799ba…`，5文件。Provides写/Requires读白名单，注册后越权读nil写不变，Load/Replace回滚，官方23插件装配通过；Lead合W52后补第5个内建可选读取ds-shortcuts→shortcut register；全仓test/vet/build全绿 |
| W56 | DONE · owner `a6cd71f5`，Lead 双仓集成验收 | MSGID：assistant/user真实v4Seq/messageId与load↔WS内容去重 | `cmd/dshd/{message_identity.go,transport_source.go,serve_tools.go}`、`internal/transport/api.go`、UI `internal/apiclient/client.go`、双仓快照/golden、`docs/message-identity.md` | host patch `a1e05d65…`、UI patch `7979f947…`。真实Event.Seq含0，user id透传，assistant不造messageId；同文各两条乱序重放恰4条，旧帧scoped fallback；真实重启OK=84、身份OK=76；Lead双仓全仓及相关race全绿 |
| W57 | DONE · owner `09f7a06e`，Lead 集成验收 | 插件父子槽授权、single/keyed与循环装配防护 | 新增 `pluginapi/slot.go`、`internal/uikernel/{kernel.go,slot_auth.go}`、测试、`docs/plugin-slot-auth.md` | patch SHA256 `8c33a91c…`，6文件。父子授权、many/single/keyed、缺/重复key、直接/间接环、32节点上限；动态Children逐次复核且不再静默截断；Dispose/Unload/Replace回滚；唯一父槽推导防省略绕过；官方23插件装配与全仓全绿 |
| W60 | DONE · owner `00be4723`，Lead 集成验收 | JSON声明/配置真实热重载与失败保留旧工作集 | `plugins/dsdecl/**`、`internal/uidecl/**`、`internal/uikernel/{kernel.go,replace_preflight.go}`、`cmd/app/{json_hot_reload.go,main.go}`、测试、`docs/json-hot-reload.md` | patch SHA256 `3a734d2e…`，17文件。一次性不可变候选、克隆完整预检、失败不Register/Dispose旧版；250ms SHA轮询+200ms debounce，Paint队列原子Replace，Effect恰好一次；两尺寸+并发Paint/Send/F2；Lead全仓test/vet/build及race全绿 |
| W61 | DONE · owner `462ff39a`，Lead 集成验收 | 官方 `settings.describe/settings.mutate` + `settings.form.describe/get/set/reset`兼容别名 | `internal/settingsform/**`、`cmd/dshd/{serve.go,serve_settings.go}`、`internal/transport/{api.go,server.go}`、additive snapshot/golden、`docs/settings-form.md` | patch SHA256 `7d3b28e5…`，12文件，serve冲突按纯additive完成文件合并。permission.defaultPreset/model.defaultModel复用既有存储与runner；A/B/cookie/set→重开→reset/revision409/写失败回滚；真实SSE `REAL_RUNTIME_TURN_DONE OK=1`；Lead全仓门禁/相关race全绿 |
| W62 | DONE · owner `ad4544ca`，Lead 集成验收 | UI官方 `settings.describe/settings.mutate` 真实消费与通用设置页 | `internal/apiclient/settings_form.go`、`pluginapi/{actions.go,types.go}`、`cmd/app/{settings_form.go,main.go}`、`plugins/dssettings/form*.go`、`docs/settings-form-ui.md` | patch SHA256 `be1b8893…`，16文件。describe驱动任意字段，dirty/set/unset/discard/真实错误retry/409重describe保留草稿新revision重试；两尺寸鼠标/纯键盘、257B/500/A-B/writable全绿；保留模型四态/24滚动/快捷键/关于；Lead全仓门禁/race全绿 |
| W63 | DONE · owner `c4293ed3`，Lead 集成验收 | additive `models.catalog.list/upsert/reorder/test/set-active`；canonical `models.list`不动 | `internal/llm/model_catalog.go`、`cmd/dshd/serve_models.go`、`internal/transport/{api.go,server.go}`、additive snapshot/golden、`docs/model-catalog.md` | patch SHA256 `56db5f8b…`，12文件。完整字段/键序round-trip、同id保留未知字段、reorder只移动raw、revision/回滚；真实OpenAI test+credential redaction；set-active只写W61同一store；GOLDEN6/REJECT15/RUNTIME4/LOOP3全绿；Lead全仓门禁/race全绿 |
| W67 | DONE · owner `c1dc9e4e`，Lead 集成验收；底层树加固归W69/W70 | 官方schedule delete/clear-ended与serve jobs父子协议 | `internal/schedule/mutations.go`、`cmd/dshd/{jobs_actions.go,jobs_out.go,jobs_process_windows.go}`、`internal/transport/handlers_jobs.go`、additive snapshot/golden、`docs/task-actions.md` | patch SHA256 `c3a8d7f3…`，25文件。delete revision fence/原子写；clear-ended全预检/多文件回滚/readback；rune分页/停止后输出；精确PID映像复核、真实sleep停止/跨会话403/未知404/二次409；Lead全仓门禁/相关race全绿。通用树安全由W69加固 |
| W64 | DONE · owner `921158c8`，Lead 集成验收 | UI插件目录真实管理与状态错误投影 | `internal/apiclient/plugin_manager.go`、`pluginapi`动作、`cmd/app/plugin_manager.go`、`plugins/dssettings/plugin_manager*.go`、`docs/plugin-manager-ui.md` | patch SHA256 `0417dbd4…`，15文件。8方法真实list/写后re-list，危险确认/失败保留现状/list-only retry；enabled≠UI动态加载执行；两尺寸鼠标键盘全生命周期、401/404/500/A-B/长列表；Lead全仓门禁/race全绿 |
| W71 | DONE · owner `487fdf09`，Lead 集成验收 | UI模型目录编辑与新会话默认active | `internal/apiclient/model_catalog.go`、`pluginapi`动作、`cmd/app/model_catalog.go`、`plugins/dssettings/model_catalog*.go`、`docs/model-catalog-ui.md` | patch SHA256 `ae96a367…`，14文件。raw/未知字段/键序保留、双向reorder、真实test/latency、写后重读active+两种revision、409保草稿、Secure credential；两尺寸28条/新模型全字段/401/409/500/A-B；Lead全仓门禁/race全绿 |
| W58 | DONE · owner `30a89079`，Lead 集成验收 | TUI CommonMark `~~删除线~~`与单`~`兼容 | `cmd/dshtui/{markdown.go,style.go,markdown_strike_test.go}`、帧夹具、`docs/tui-markdown-strike.md` | patch SHA256 `0d262068…`，5文件。恰好双~~ SGR9，单~/长运行/未闭合/路径保留；bold/code/link嵌套不吞字；TERM=dumb/NO_COLOR/=0降级dim+可见~~正文~~；120×36/80×24 plain/color×2共8组确定性FAIL=0；全仓门禁全绿 |
| W59 | DONE scoped · owner `0271b3e0`，Lead 集成验收；第九款归W65 | TUI前8款开屏文字字体按本地日期轮换 | `cmd/dshtui/{splash.go,splash_gen.go,layout.go}`、`scripts/import-splash.py`、测试/快照、`docs/tui-daily-fonts.md` | patch SHA256 `d7e08aa3…`，9文件。bold/square/bevel/wide/dot/stencil/classic/slab真实字形/度量/日期/时区/pin/布局与fail-loud；wide阈值114；8款×小终端/dump确定性，全仓全绿。**官方全集还有shadow，当前%8仅scoped，不能称9款完成** |
| W65 | DONE · owner `b54ad23d`，Lead 集成验收 | 补第九款shadow与官方%9轮换 | `cmd/dshtui/{splash.go,splash_gen.go,splash_daily_test.go,animation_test.go}`、`scripts/import-splash.py`、`docs/tui-shadow-font.md` | patch SHA256 `6e656ef0…`，6文件。shadow 18字/10列/uniformKerning=1，day8=shadow/day9=bold；titleWidth88/130并排/87鲸鱼独占；前8款asset不变；三条动画测试pin bold固定原几何；9款×尺寸/dump确定性+全仓全绿 |
| W69 | DONE · owner `688ee872`，Lead 集成验收 | SPI/bash后台任务有界游标、PID元数据与归属进程树停止 | `internal/spi/exec.go`、`modules/exec/bash/{service.go,session.go,process*.go}`、测试、`docs/jobs-process-tree.md` | patch SHA256 `c7ff76e5…`，11文件。JobPager/JobController additive，实例限定job id，1MiB保留/64–256KiB页；Windows Job Object+Unix PGID，PID/代次/映像复核，旧Kill同走安全Stop；真实root+parent+child在Stop/Dispose后退出；Lead全仓门禁/race全绿 |
| W70 | DONE · owner `82a706b5`，Lead 集成验收 | W67 serve协议与W69安全执行的联合收口 | `cmd/dshd/{jobs_out.go,jobs_integration_test.go,serve_jobs_e2e_test.go}`、`internal/transport/handlers_jobs.go`、`modules/exec/bash/tools.go`、`docs/task-actions.md` | patch SHA256 `bae911b1…`，7文件。stop优先Controller，Pager最多1MiB/64KiB页/4次一致快照有界字节→rune适配，旧fallback保留；HTTP rune契约不变+truncated；真实parent/child/grandchild三PID退出、CJK/emoji/跨实例/ended幂等；Lead全仓门禁/race全绿 |
| W66 | DONE · owner `f100868b`，Lead 集成验收 | TUI OSC 11背景查询/设置与dark-light主题适配 | `cmd/dshtui/{app.go,theme.go,osc.go,osc_test.go}`、`docs/tui-osc-theme.md` | patch SHA256 `dcd3a71e…`，5文件。OSC11+DA1、400ms、1–4hex、BEL/ST、Rec.601>140；显式>OSC>COLORFGBG>dark；非TTY零探测、单stdin泵、迟到OSC不入草稿；改动前后两尺寸dump逐字节一致；Lead全仓门禁/TUI race全绿 |
| W72 | DONE · owner `4969780a`，Lead 集成验收 | TUI grapheme行中编辑、长文本视口与F5–F7/F12真实解码 | `cmd/dshtui/{app.go,input.go,layout.go,keys.go,screen.go}`、测试、`docs/tui-input-editor.md` | patch SHA256 `2b874138…`，8文件。grapheme行中插入/删除、Home/End/Ctrl+A/E、Ctrl/Alt词跳、CJK/ZWJ emoji水平视口；pending全键只读、accepted清光标、失败/Retry保留；F键只解码无动作；真实行为红后修，Lead全仓门禁/TUI race全绿 |
| W74 | DONE · owner `742a7f59`，Lead 集成验收 | 宿主effort真值投影与TUI状态行显示 | `cmd/dshd/transport_source.go`、`internal/transport/{api.go,source.go}`、`cmd/dshtui/{client.go,app.go}`、契约/测试、`docs/tui-effort.md` | patch SHA256 `aad5c0ae…`，12文件。最新request/header config或model/selection的effort，off/low/high/xhigh/max；旧/空/未知省略且不向前回退；真实dshd `OK=34 FAIL=0`，A/B/重开/续写/官方max；TUI缺省/切会话/宽度通过；Lead全仓门禁/race全绿 |
| W75 | DONE · owner `4768f65f`，Lead 集成验收 | active catalog模型真实provider路由到两个runner | `internal/llm/{model_catalog.go,client.go}`、`cmd/dshd/{serve_model_override.go,serve_loop.go,serve_turn.go}`、测试、`docs/model-provider-routing.md` | patch SHA256 `1f82d16f…`，9文件。每轮ResolveProvider，stored/env credential；直接runner显式llm.New，loop argv --base+独立Cmd.Env，不os.Setenv/key不入argv；普通/旧会话legacy，坏配置fail-loud，统一redaction；DIRECT7/LOOP5/REAL7全绿；Lead全仓门禁/race全绿 |
| W73 | DONE · owner `a402124a`，Lead 集成验收 | UI日程delete/clear-ended与jobs output/stop真实动作 | `internal/apiclient/{jobs.go,fs.go}`、`pluginapi`动作、`cmd/app/task_actions.go`、`plugins/dssidebar/task_actions.go`、`docs/task-actions-ui.md` | patch SHA256 `b4b2ed3f…`，22文件。schedule revision fence/clear-ended确认/写后重列/409保选择；jobs当前会话/rune分页/truncated/stop两阶段/输出+列表readback/失败不删行；A/B/错误/两尺寸键鼠；Lead全仓门禁/race全绿 |
| W76 | DONE scoped · owner `7ebf35ab` + W77/W78，Lead 在C:独立复跑签收 | 当前版本真实整链回归（非可见GUI终验） | `scripts/real-chain-headless.py`、`docs/real-chain-current.md`、`docs/jobs-natural-exit.md`、`docs/msys-startup-acl.md` | patch `d657463b…`+`4af144a0…`+`1d122d81…`。Lead在C:新构建+bin-check后实跑：核心77/0、current 76/0、`OK=153 FAIL=0 SKIP=0`，exit0；jobs三级PID启动/停止/readback成功。D: Medium红保留并证明为二进制位置完整性/ACL环境，非产品回归；可见GUI/完整工作日归W7 |
| W77 | DONE scoped · owner `ea615107`，Lead 集成验收；启动ACL调查归W78 | jobs自然成功/失败退出后的持久输出 | `cmd/dshd/{main.go,jobs_out.go,jobs_natural_exit_test.go}`、`docs/jobs-natural-exit.md` | patch SHA256 `4af144a0…`。Dispose前捕获Job、Dispose后读最终快照，注入/读页/写盘错误汇入retErr；自然exit0/7从先404到HTTP幂等readback，CJK/emoji/rune页与fail-loud通过。Lead定向 `TestJobsRealDSHDNaturalExitReadback|TestPersistJobOutputsFailLoud` exit0；W76自然ended readback已不再404 |
| W78 | DONE · owner `1b3798e5`，Lead 集成验收 | MSYS BaseNamedObjects启动拒绝的D:/C:同版对照与最小复现 | `scripts/real-chain-headless.py`诊断、`docs/msys-startup-acl.md`、证据 `msys-startup-acl-w78-evidence/` | patch SHA256 `1d122d81…`。同二进制C:=High S-1-16-12288、D:=Medium S-1-16-4096；同用户/session、in_job，双向复制证明位置而非内容，D cwd/home非根因，不改产品/ACL。C:153/0，D:保留148/5真红+环境标注不跳过；全仓门禁/race绿，AGENTS已写入新规则 |
| W79 | DONE scoped · owner `bdace012`，Lead 集成；完整图片校验归W89 | 附件宿主store/content核心 | `attachments-host.patch` SHA256 `22174ac4…`、20文件；证据 `attachments-w79-evidence/` | 宿主20文件已集成；W84 store加固、W85 content子进程复验均已合入；宿主全仓门禁/race绿。**截断图片与模型保序仍由W89/W90收口，不冒充全绿** |
| W80 | DONE · owner `ae2fd602`，Lead 集成验收 | TUI状态区model/effort/context/tools/session真实鼠标交互 | `cmd/dshtui/{app.go,layout.go,screen.go,status_actions.go,status_actions_test.go}`、`docs/tui-status-actions.md` | patch SHA256 `7ebbd2e8…`，6文件。五区同源hit box，真实SGR左键/1003 hover/固定/再点/Esc/切会话关闭；未知不编0，tools仅为已接收tool/call数；输入/发送/审批/OSC隔离，四尺寸dump逐字节不变；Lead全仓门禁/TUI race全绿 |
| W82 | DONE scoped · owner `bb9dda01`，Lead 按单文件增量集成 | 附件 UI 公共契约/状态/Actions 接线 | UI apiclient/pluginapi/cmd/app/dscomposer/dsconversation、`docs/attachments-ui.md` | 38文件补丁作参考，不整包覆盖；Lead已集成 `AttachmentData/AttachmentItem/ContentBlock`、Snapshot.Attachments、五个Actions、cmd/app状态与composer接线；UI全仓test/vet/build与相关race绿。W82测试未全收编，真实模型/可见GUI未验 |
| W83 | DONE · owner `7c4b966a`，Lead 集成 | 附件独立安全/真实整链审查 | `scripts/attachments-review.py`、`docs/attachments-review.md`；patch SHA256 `c7ca0bd2…` | 无附件基线`OK=33 FAIL=61`；集成版`OK=151 FAIL=4`定位3根因。W84/W89/W90/W91/W98修复后，Lead在C:新二进制独立复跑 **`OK=155 FAIL=0 SKIP=0`**，证据`tmp/attachments-review-w98-shared`；真实可见GUI仍属W7 |
| W84 | DONE · owner `439b9ccf`，Lead 集成 | 只修 `cmd/dshd/attachments.go` Get完整性 | 仅此文件；patch SHA256 `a3118d3f…`，改后源码 `4cb22401…` | 外部探针先红：篡改meta MIME/尺寸、空对象、超8MiB可过；修后真实字节重算MIME/尺寸、8MiB+1有界读、空/超限拒绝；WebP三变体/A-B/symlink/同长blob篡改全PASS，宿主门禁exit0 |
| W85 | DONE · owner `181dfdb1`，Lead 集成 | 只修 `cmd/dshd/prompt_content.go` 子进程复验 | 仅此文件；patch SHA256 `d67d6ad3…`，改后源码 `6f99e18d…` | 外部探针先红：1–64/1MiB/全空、伪造reference metadata、非法referenceId；修后重建块并验`ref-`+32hex，真实文本/图片/durable顺序通过；宿主定向与全仓门禁exit0 |
| W86 | DONE · owner `c587f358`，Lead 集成 | 只实现 `internal/apiclient/attachments.go` 单文件 | 仅此文件；patch SHA256 `df2c0905…`，177行 | 裸path/id、cookie、`{attachment,dataBase64}`、真实len/SHA/id强校验、四字段、A-B、8MiB原附件；Lead已补additive快照例外与Message.Content，UI全仓门禁exit0；旧W82草稿三项真红反证保留 |
| W87 | DONE · owner `822ac038`，Lead 集成 | 只实现 `plugins/dscomposer/attachments.go` 单文件 | 仅此文件；patch SHA256 `f5dc9a8c…`，395行 | 路径Field、drawCard→drawAttachments、composerSendDisabled、submitAttachments全部接线；外部overlay先红后绿，完整包/UI全仓/race绿；真实dshd/可见GUI未验 |
| W88 | DONE · owner `72e58469`，Lead 集成 | 只实现 `plugins/dsconversation/attachments.go` 单文件 | 仅此文件；patch SHA256 `03d3133e…`，107行 | 真实durable块序、name/bytes/dimensions/MIME/id/ref，缺值不编0；无真实bytes时明确“元数据卡·不支持预览”不画假图；A/B/重开稳定，UI全仓门禁exit0；mask接线待Lead（尚未做） |
| W89 | DONE · owner `46e43c6e`，Lead 集成 | 只修 `cmd/dshd/attachments.go` 完整图片校验与失败不落盘 | 仅此文件；patch SHA256 `9c1e1f40…`，改后源码 `d7a3d2a1…` | 先红152/3：截断PNG/无IDAT/坏CRC/GIF尾均落盘；修后W83 `OK=154 FAIL=1`，两目标转绿、0新增失败。PNG chunk/CRC/IDAT/IEND、JPEG/GIF完整解码、WebP RIFF/VP8X/ANIM、64M像素上限；校验先于MkdirAll；宿主全仓/race绿 |
| W90 | DONE · owner `a9f52498`，Lead 集成 | 只改 `internal/llm/types.go` 有序多部分序列化 | 仅此文件；patch SHA256 `5a85a91e…` | 新增Message.Parts/MessagePart/TextPart/ImagePart；Parts空旧字节完全不变，非空按序接管；外部overlay三段保序、10组旧golden、两份变异真红；宿主全仓test/vet/build绿。prompt_content接线归W91 |
| W91 | DONE · owner `ead2738e`（重启后重派），Lead 集成 | 只改 `cmd/dshd/prompt_content.go` 保序Parts接线 | 仅此文件；patch SHA256 `0de0548c…`，改后源码 `e9cf51b9…` | 新增loadPromptPartsForRun，text/plain附件→TextPart、图片→ImagePart、reference→TextPart原序；旧函数兼容；外部overlay BEFORE/image/AFTER先红后绿，宿主全仓/vet/build绿；main接线归W98 |
| W92 | DONE · owner `44c05aaf`，Lead 集成 | 只改 `internal/apiclient/client.go` 的 Message.Content 字段 | 仅此文件；patch SHA256 `ce831d44…` | 可选 additive `Content []ContentBlock`；load/history保留有序块，旧纯文本不输出content且旧字段语义不变；外部overlay先红后绿；UI全仓/vet/build绿 |
| W93 | DONE · owner `4e9ac7c4`，Lead 集成 | 只改 `plugins/dsconversation/mask.go` 消费有序内容 | 仅此文件；patch SHA256 `8e78489d…` | user且len(Content)>0走`secretmask.Mask(orderedUserContent(...))`，无Content回退Text，assistant不变；先红缺附件/引用，改后顺序/身份/A-B/重开通过；UI全仓test/vet/build绿，真机GUI未验 |
| W94 | DONE · owner `a92bb914`，Lead 集成 | 只新增 `docs/official-parity-current.md` 官方55部件重审 | 仅此文档；patch SHA256 `269c722c…`，524行 | 新计数 DONE scoped 26 / PARTIAL 10 / MISSING 16 / N-A 3；逐项file:line/未验/下一任务，6个设置MISSING全为宿主无字段；真实dshd/可见GUI未验 |
| W98 | DONE · owner `4f6964d8`，Lead 集成并独立复验 | 只改 `cmd/dshd/main.go` 使用保序Parts | 仅此文件；patch SHA256 `a46259e0…`；共享C:二进制 `f9801000…` | 先红`OK=154 FAIL=1`，Lead集成后`ATTACHMENTS_REVIEW_SUMMARY OK=155 FAIL=0 SKIP=0`；BEFORE→image→AFTER保持、真实模型单请求、旧prompt路径不变；bin-check OK=2、宿主全仓test/vet/build exit0，证据`tmp/attachments-review-w98-shared` |
| W95 | DONE scoped · 由W105 codeload替代Git pack完成 | 上游 dsh-TUI 下载/安装/构建与启动审计 | `../dsh-TUI-upstream-src/**`、`tmp/upstream-tui-build/**` | 主rev `1bd7cb5…`、vendor/dsh-std `614dfa1…`已下载展开；install/typecheck/smoke exit0；build编译成功但103门禁8 FAIL，忠实记录不强行修绿；W96协议文档已交付，W97/W107/W108开始桥接 |
| W96 | DONE · owner `1258b084`，Lead 按真实文档验收 | 上游TUI与宿主Transport/ACP协议兼容矩阵 | `../dsh-TUI-upstream/docs/upstream-tui-protocol.md`；312行，SHA256 `e01cd82a…` | 已完整交付：默认上游是JS Cordis `ctx.agents`/session/event/assistant-stream，非ACP/HTTP/WS；最小为第三方`dsh-go` AgentBackend bundle，P0/P0-7与P1/P1-9逐项列出，含file:line/启动/重连/未验项；静态审计，未构建桥包 |
| W97 | DONE scoped · W107/W108/W109/W110/W111全收口 | 新增第三方 `dsh-go` AgentBackend bundle，桥上游AgentSession/shared channel到DSH-GO Transport | 上游`src/dsh-go/**`、正式`src/backends/dsh-go/**`、真实conformance | transport/events/backend/registry全绿；真实dshd `OK=88 FAIL=0`、真实ConPTY TUI `OK=9 FAIL=0`，12项P0全PASS；`--backend dsh-go`真实启动/连接/退出。未验build/clean、>256分页、长跑、attachments/images、可见GUI；P1逐token/raw v4/placement未接 |
| W107 | DONE · owner `9efdb1e0`，Lead 复验 | `src/dsh-go/transport.ts` SHA256 `448a7093…`、测试与文档/result | 真实HTTP/WS测试5/5 PASS，连续三轮15/15；ready one-shot cookie、裸API/四字段、WS sub/ping、epoch/lastSeq、history补洞、stale/reset、63-bit epoch、退避、seq0、replay/live去重、dispose两竞态修复；typecheck/smoke exit0；真实dshd整链归W110 |
| W108 | DONE · owner `961a8ac0`，Lead 复验 | `src/dsh-go/events.ts` + 同目录测试与文档 | 文件SHA256 `29555863…`；测试12/12 | load/WS/v4/turn/approval/reset→AgentEvent；独立AgentEvent.seq、(role,v4Seq)主去重/messageId fallback、reset清状态、未知custom、不制造delta/presenter/attempt；typecheck/smoke/verify:boundary均exit0 |
| W109 | DONE · owner `dfcb9d72`，Lead 集成复验 | `dsh-go` AgentBackend/AgentSession/SessionCatalog装配与`--backend dsh-go` | `src/dsh-go/backend.ts` 753行定稿、`docs/dsh-go-backend.md` SHA256 `7e3d4ece…`、result JSON | R1-R4全修；全套24/24、typecheck/smoke、registry 66/66、boundary 1833/7707、contribution 65/65全绿；Lead已把四个测试登记到`scripts/run-ci-group.mjs` session-workspace，--list四项存在且24/24通过；真实dshd/TTY归W110 |
| W110 | DONE · owner `bd22a131`，Lead 回读复验 | 真实dshd+上游TUI conformance与ConPTY | `docs/dsh-go-conformance.md`、result JSON、wire-06、ConPTY-07 | 最终`W110_SUMMARY OK=88 FAIL=0`、`W110_TUI_SUMMARY OK=9 FAIL=0`；12项P0全PASS，真实create/list/load/text/tool/审批/取消/补洞/重启reset/A-B/resume/身份；TUI `bin/dsh-tui.js --backend dsh-go`真实创建会话/连接/quit回收；dshd SHA256 `f9801000…`；未验build/clean、>256分页、长跑、attachments/images、可见GUI |
| W111 | DONE · owner `26897ea0`，Lead 集成复验 | dsh-go正式manifest与verify-backend-registry parity | `src/backends/dsh-go/**`、官方generated index、`docs/dsh-go-registry.md` SHA256 `d2be5627…`、result `b311ff0b…` | registry 66/66、contribution 65/65、assembly ALL PASS、完整backend 24/24、typecheck/smoke/selection全绿；正式bundle自包含，删除手工seed，官方生成器刷新索引；result JSON回读11/11段输出逐字一致、19/19文件hash一致；真实dshd联调归W110 |
| W112 | DONE scoped · owner `7a4e1bbd`，Lead 集成；L0由W116/W117收尾 | dsshell官方框架几何与真实L0 caption两尺寸复验 | `plugins/dsshell/shell.go`、`layout_drag_test.go`、`official_layout_test.go`；patch SHA256 `ad5f32e9…` | 拖拽侧栏264..420、右栏300..70%、BottomHeight=0不伪造24、去DBG；先红后绿，全仓门禁绿；两尺寸Viva/GDI/真实HWND菜单与OS min/max/restore通过。**L0未签DONE**：caption #151517≠#1b1b1c、toggle/collapsed未接，W116/W117处理 |
| W116 | DONE · owner `a4f27440`，Lead 集成并与W117闭环 | 官方caption L0底色/toggle/collapsed动作 | `cmd/app/frame_chrome.go`、`frame_chrome_test.go`；patch SHA256 `97d7f62c…` | caption #1b1b1c、官方JSX/hash、toggle与collapsed new-chat、menuStart84；W117已接m.chrome.toggleSidebar/sidebarCollapsed，生产不再HIDE；真实track=0/恢复与旧hit区分通过，全仓门禁绿。高DPI/第三方主题/OS级collapsed键鼠未验 |
| W117 | DONE · owner `e2ead0e4`，Lead 集成并补chrome两行接线 | 生产ToggleSidebar action/state与chrome接线 | `pluginapi/actions.go`、`pluginapi/plugin.go`、`cmd/app/main.go`及测试；patch SHA256 `a6eff039…` | Actions.ToggleSidebar、Snapshot.SidebarCollapsed、model.collapsed/toggleSidebar、Env.Has与dsshell track=0/恢复；Lead接m.chrome两字段并补newConfig nil兜底，测试按hit.ID区分旧侧栏与官方collapsed New Session；两尺寸A→B→A几何/命中/像素/A-A hash一致，全仓门禁绿 |
| W113 | DONE scoped · owner `cecff235`，Lead 集成 | 官方左侧栏frame几何S1-S4与导航/品牌/搜索/图标 | `plugins/dssidebar/frame.go`等6文件；patch SHA256 `4b989965…` | 默认导航仅plugins/schedules，隐藏伪viewOptions/NewSession nil、品牌与按钮共用真实NewSession、Windows几何、官方NewChat/PluginPinwheel/Clock/Search JSX；两尺寸Viva/主题/矢量/Hit/键鼠/A-B，全仓门禁绿。未验BrandWordmark/整栏收起/root main[key]/S5-S8/OS键鼠/真实dshd组合帧 |
| W114 | DONE scoped · owner `074fec6e`，Lead 集成；root exactly-one归W115 | 侧栏active-panel挂载/无标题/footer/设置图标 | `plugins/dssidebar/sidebar.go`、`footer.go`及4测试；patch SHA256 `3781bb47…` | Summary→派生首句CJK、footer固定计数移除/断线真值/官方settings.jsx hash对账完成；两尺寸active/设置/AB/全仓门禁绿。保留sentinel真红：conversation残留2509像素，root main[key] exactly-one未在本单冒充完成 |
| W115 | DONE scoped · owner `b8e680fe`，Lead 集成 | root `main[key]` exactly-one：active-panel与conversation不可共同绘制 | `pluginapi/plugin.go`、`plugins/dsshell/shell.go`、`plugins/dssidebar/sidebar.go`、`cmd/app/main.go`等11文件；patch SHA256 `0f7c0614…` | 真实SlotMain+Snapshot.ActivePanel、dsshell SlotKeyed、main:<key>/conversation单分支、dssidebar逐路由keyed painter、main.go抑制conversation AfterDraw并保存/恢复ScrollState；最终两尺寸residual=0、A/B不串、设置mask/F1/F2/collapsed通过、offset/history/A-A2 hash一致；全仓test/vet/build绿。真实Viva headless，不冒称可见OS GUI |
| W118 | DONE scoped · owner `d129928f`，Lead 按文件集成 | 官方主会话区Hero/Chat/Trajectory结构，最小自制 | `plugins/dsconversation/conversation.go`等6文件；patch SHA256 `b30072cb…` | 官方Conversation/Chat/flow、16/32 padding、历史→流式→RunningStatus、RunningWhale、34回底、右侧Turn rail、Hero双轴居中完成；W115 residual=0、ScrollState/历史A-B-C-A exact_pixels=true；全仓门禁绿。仅Viva headless，未起真实dshd/模型/可见OS；Turn process标题为cmd/app旧测试derived兼容，未恢复横排导航/footer |
| W119 | DONE scoped · owner `b3a105c4`，Lead 按文件集成并原生复验 | 官方Composer结构，最小自制 | `plugins/dscomposer/composer.go`及测试；patch SHA256 `61690c14…` | 官方attachments→DraftEditor→controls、附件卡/停止r3 rect/附件-only SendContent/草稿A-B-A/idle Esc/无Session HIDE完成；共享未跟踪测试导致apply冲突，已备份后仅同步composer.go与*_test.go。全仓test/vet/build绿；共享树两尺寸composer_native真实Win32/PrintWindow均exit0。未验真实dshd/模型/附件上传/系统文件框/物理键鼠/IME；Lexical336/reference/queue等未做 |
| W120 | DONE scoped · owner `f4855564`，Lead 按文件集成 | 官方右栏DockKit最小真树，最小自制 | `plugins/dsrightbar/rightbar.go`及测试；patch SHA256 `5b3aef60…` | 每Session retained surface、单pane多tab id/kind/active/close/reopen、guide seed、无Session完全为空、tab strip无独立标题、28×28 fullscreen/collapse与click-through防护完成；A/B编辑与选择隔离、1000条可见行/全量可搜；全仓门禁绿。split/float/拖拽/完整DockKit持久化/完整T1-T6未实现，可见OS/真实dshd未验 |
| W121 | DONE scoped · owner `a264f143`，Lead 按文件集成 | 官方顶栏views tabs/actions/corner，最小自制 | `plugins/dstopbar/topbar.go`等5文件；patch SHA256 `95d6be41…` | 注册描述驱动tabs、Chat固定/单tab隐藏/失效回Chat、无dsrightbar不死tab、切view不OpenSession/Cancel/SetDraft、标题缺真值HIDE、actions/utilities/corner分离、28×28官方镜像ExpandButton完成；两尺寸Viva/A-B/全仓门禁绿。Lead把旧测试改为B/C草稿为空、A恢复，匹配W119按Session隔离。dsopen席位/developerTools配置/第三方view/pin/More/可见OS未验 |
| W122 | DONE scoped · owner `b5aefb4a`，Lead集成并更新旧几何契约 | 官方Composer dock与状态栏，最小自制 | `plugins/dsstatusbar/statusbar.go`等7文件；patch SHA256 `f87827e8…`；Lead更新uikernel/dsshell旧断言 | 正常态shell.bottom移除，断线留sidebar.settings；22px视觉/26px dock/28px命中、Usage/Context真实上8px弹层、14/r5.5/stroke2环、无真值HIDE完成；Lead把装配守卫改为禁止固定bottom、拖拽几何改为BottomHeight=0全高。全仓test/vet/build exit0；两尺寸Viva/native由owner验收，未冒充可见OS/IME |
| W123 | DONE scoped · owner `6c596c71`，Lead完成topbar/dsopen集成并核对TREE_DIFF=0 | dsopen utilities真实可用性与空态隐藏 | `plugins/dsopen/open.go`等5文件；patch SHA256 `f932bcc4…`；topbar集成patch `8f7f4700…` | 真实painter移至`conversation.session.header.utilities`，topbar Children+SlotDeclarations+env.Has接入，无import cycle/marker；blank/sessionless/非conversation/缺装/noAction/noCWD均HIDE，缺装gap=8，真实目录/文件/外链边界保留；共享树无overlay全仓test/vet/build exit0 |
| W124 | IN_PROGRESS · 另一主会话 `session-191da5ac` | TUI `/model` 真实模型目录/切换 | 上游`src/dsh-go/backend.ts`及宿主模型Transport | 记录实证原“列表/新会话不可用”；当前工作树已见models.list/current/setModel与目录Transport，需由该会话完成真实TUI复验；Lead不重复派写 |
| HOST-API-PARITY | IN_PROGRESS · 已接线 82/153；Lead 集成 | Go宿主对齐官方桌面端153方法 | [docs/official-desktop-api-parity.md](docs/official-desktop-api-parity.md) §7 进度；契约镜像`/tmp/official-api-src` | 已接线 82：session 15、job/terminal 11、agent 24、workspace 16、pluginManager 11、settings/credentials 5；其中按口径 501 的有 session follow/observe*/openWorkspacePath、commands.execute、subagents.*、userQuestions.attachWait、workspaceFiles.changes、setVersionExemption。剩余 63 多为实验/账户/市场（forgePanel 19、dynamicCordisRunner 11 与 R6 冲突、liveshare 8、account 7、schedule 5、speech 4…）。L2 remote.mux 未开始，**不是 wire 兼容**。集成门：build 0、vet 0、transport ok 1.27s、cmd/dshd ok 172.6s |
| HOST-SANDBOX-POLICY | IN_PROGRESS · core owner `428a8566`，OS研究`1ae269af`；Lead已立契约 | Go宿主沙盒权限三档与一次性升级 | [docs/sandbox-policy.md](docs/sandbox-policy.md)；core=`internal/sandbox/**`，integration见LEAD | 现状只有approval半档+FS路径边界+进程树管理；bash/pwsh与宿主同权限，Job Object非隔离。core实现三档/one-shot/A/B/fail-loud，随后生产接线；OS Windows隔离先只读研究再实施，不提前宣称完成 |
| W125 | DONE scoped · 宿主+W134上游+W138生产steer均已集成 | TUI `/queue`、`/follow` placement | 宿主patch `f18cdfc6…`；上游W134；生产W138 `d3b016dc…` | session.prompt placement与cancel cause/turnId、followup FIFO、now真cancel等收工、user保留/其它清队列、stale cancel防护已落地；W134真实提交与pending/cancel映射，W138生产steer下一step IPC。queue仍进程内存态不承诺宿主重启恢复，最终真实ConPTY由W146/W148收口 |
| W130 | DONE · owner `4affb076`，Lead核读 | GO完整兼容dsh-TUI接口矩阵 | `docs/tui-interface-matrix.md` 568行 | 64=29 any+21 capability+14 dsh全覆盖；当前capabilities=models,native,permissions、35命令；model owned_by未解析、placement/images拒绝、compact/fork/jobs/effort桥未接；P0=placement→compact→刷新包→命令级ConPTY；本轮9/9仅旧包mount/quit，不能外推可玩 |
| W131 | DONE scoped · owner `da4f6230`，Lead已接capability并复验 | dsh-go `/compact`独立helper | 双目录`compact.ts/test`；Lead接backend capabilities | 裸{id,keep}、compacted=false/true、错误/畸形成功均覆盖；Node10/10、typecheck、boundary全过；Lead已在两个backend.ts接compact并补owned_by解析。真实dshd压缩恢复/ConTTY仍待W132 |
| W132 | DONE scoped · owner `a718abee`，两FAIL交W134 | 刷新当前TUI包并命令级ConPTY | 隔离`tui-current-package/conformance` | 新backend.js SHA256 `39a5045e…`含models，C:dshd `570333e3…`启动时bin-check绿；模型选择器真实选中并持久化Kimi，网关200。corrected ConPTY OK=20 FAIL=2 UNVERIFIED=3：普通prompt与真实trace因followup未接而红，其余通过；交付前源码漂移使旧stable bin-check变红，接口收口后重建，不冒充稳定版 |
| W133 | DONE · Lead | 双渠道版本与宿主产物名 | `0.2.2-go.1`、`0.2.2-beta.1`；`dsh-<版本号>` | 前缀升级后序号从1；稳定版dshd/dshtui/ACP/UA与文档同步，Windows资源重建，`dist/windows-amd64/dsh-0.2.2-go.1.exe`实测版本行正确，build.sh三平台exit0，宿主全仓test/vet/build exit0；GO.1只有在全部TUI接口真实可玩后才覆盖为首个稳定版 |
| W134 | DONE scoped · owner `1be08143`，Lead独立复验全绿 | 上游placement/queue/pending/cancel receipt | 双backend.ts、placement.ts/tests、backend.test.ts | turn/steer/followup/now真实提交，clientMessageId↔turnId账本、FIFO pending.changed/retraction、confirmed/unknown/failed与stillQueued映射；501/409语义保留，`!!`复用官方followup；owned_by回归。Node125/125、typecheck、boundary全绿；真实ConPTY由W146复验，生产steer由W138补 |
| W135 | DONE scoped · owner `d915364f`，宿主W145待补 | fork/rewind/transcript helpers | 双`session-operations.ts/test`及宿主契约note | Node36/36、typecheck/boundary全过；plain fork可接现有宿主，title/anchor须回显，rewind等preview/apply，截断大会话等transcript.page；完整baseline transcript可接但initialMessages须等于实际replay数，未冒充真实产品验收 |
| W138 | DONE scoped · owner `be77ca70`，Lead集成TEST/VET/BUILD=0 | 生产steer真实step inbox | patch SHA256 `d3b016dc…`，15 files | modelRunner/默认/routed loopRunner text+content Steer，下一step FIFO，run子进程IPC按session+turn+PID代次，ErrSteerLate；早到/在飞/多steer/单步/content/取消/错误/A-B/routed覆盖；定向三包、全仓、steer race均绿。真实magpie/ConPTY待W148 |
| W136 | DONE scoped · owner `d6a81f6f`，宿主W144在途 | effort/modes/goals helpers | 双`effort.ts/modes.ts/goals.ts`及测试、`docs/tui-effort-modes-goals-host-note.md` | helper34/34、boundary全绿；全量typecheck只受W134在途placement.js缺失阻塞，backend.test另等pendingRetraction接线，均不属W136。宿主setEffort、goal snapshot/pause/resume/change已交W144 |
| W137 | DONE · owner `7dca650c`，Lead已合并beta最终版 | beta trace/export合规覆盖 | patch `2b4f4f6c…`已合主线 | 自包含fixture、公开loadBackend、正式translator、projection/export/stale/写失败断言；CI两组登记；Node27/27、typecheck、boundary全过，未放宽island |
| W139 | DONE scoped · owner `c17288fa` | images/attachments全链接口 | 双images/attachments及测试、`docs/tui-images-host-note.md` | images34/34、attachments26/26、placement16/16、旧桥23/23、tsc/boundary全绿，镜像EXACT；data URL→受控临时文件→attachment.put、lazy durable get、BEFORE/image/AFTER保序、重开/A-B。backend/events接线待W147，真实dshd/ConTTY未验 |
| W147 | DONE scoped · owner `c527448d`，Lead独立Node377/377+typecheck/boundary/compile全0 | 全helpers统一backend接线 | 双backend.ts/events.ts/manifest.ts、backend-integration.test.ts | fork/rewind/transcript、effort/modes/goals、images.content/transcript、tasks/subagents、context/auth、ViewSpec/step/TTFT已正式接线；W149前MCP/sideQuery、W150前rename/color/init absent；真实dshd/ConPTY由W153验收 |
| W140 | DONE scoped · owner `71793045` | jobs/subagents真实面板与事件 | 双tasks/subagents/task-events及测试、`docs/tui-jobs-host-note.md` | 新增52/52、全部dsh-go Node291/291、tsc/boundary全绿；jobs真实list/output/stop、rune tail/truncated、A/B、subagent从真实tool.call/result派生，interrupt=false不伪造roster/history。backend接线待W147，实时面板未验 |
| W148 | DONE scoped · owner `a41f808c` | W138生产steer与独立now真实ConPTY复验 | `tmp/tui-steer-conformance/RESULT.md`、steer-final-summary.json | STEER_SUMMARY OK81/FAIL0/UNVERIFIED1；生产steer同turn下一step与v4顺序硬证、独立now wire、baseline/compact/followup/models/new/resume/quit全绿；唯一未验images backend接线，交W147 |
| W141 | DONE scoped · owner `decca259` | context/MCP/auth/sideQuery helpers | 双16 TS文件、`docs/tui-context-host-note.md` | Node54/54、tsc、boundary全绿，镜像8 EXACT；context summary/auth可接，MCP待三API，sideQuery无真实隔离runner保持absent，auth native logout仍需channel/core接缝 |
| W149 | DONE scoped · owner `7fcceecb`，Lead集成TEST/VET/BUILD=0 | context full/MCP/sideQuery宿主能力 | patch `78070f8a…`，17 scoped files | 真实context OK9/0、MCP OK12/0、side OK17/0；stdio MCP initialize/tools分页/reconnect/toggle持久化，side独立无工具runner NDJSON/cancel/close/A-B且父home全文件hash零写；丰富breakdown无真值保持absent，W155接backend，W152真实TUI终验 |
| W142 | DONE scoped · owner `d9ce3e33` | rename/color/init helpers | 双rename/color/init及测试、`docs/tui-metadata-init-host-note.md` | Node48/48、tsc/boundary全绿，镜像EXACT；精确title/accent readback、早到WS echo缓冲、refresh/mutation串行/stale/dispose、init逐项created/unchanged/failed；宿主W150前capability保持absent |
| W150 | DONE scoped · owner `dcf41914`，Lead集成TEST/VET/BUILD=0 | rename/color/workspace.init宿主能力 | patch `c7b21e9b…`，16 files | 真实METADATA_INIT OK86/0；精确title/titleExplicit/titleSource、per-session color、init原子不覆盖三态；v4+index事务、durable+journal/WS同mutation锁、OpenSession锁内快照；Lead补回W149 Options/路由后合并与全仓全绿，W159启用capability |
| W151 | DONE · owner `d2dc7c71` | 刷新64命令/AgentSession矩阵 | `docs/tui-interface-matrix.md` SHA256 `4a3e362f…` | 64命令基线完整，W148 81项组成拆分、W139-W145 scoped/W147静态接线/W149-W150边界分层；doc-links、12链接、152证据指针全过；Lead后续只需同步W147验收状态 |
| W153 | DONE · owner `03e3b91c`，OK258/FAIL9/UNVERIFIED0 | W147新增能力真实ConPTY验收 | `tmp/W153/RESULT.md`4718行、summary.json、C:证据 | 主链/图片/jobs/effort选择/goal/fork/rewind/transcript/context/auth/ViewSpec/A-B均过；真实FAIL=生产step/delta/end+TTFT缺失、实际effort v4 selection=0，交W157；另ConPTY驱动selector未关2项与并发bin-check drift 1项如实保留；进程卫生全绿 |
| W154 | DONE · owner `9fb0d3a5` | 同步矩阵W147验收分层 | `docs/tui-interface-matrix.md` SHA256 `85fc02ad…` | 42处旧口径统一：W147 DONE scoped+Lead Node377/377/构建绿，W153真实验收在途；64命令/12链接/189证据指针全过，W149/W150与logout边界未误升级 |
| W152 | DONE · owner `f68777e3`，NO-GO：OK645/FAIL5/UNVERIFIED0 | 全接口正式包真实ConPTY终验 | `tmp/W152/RESULT.md`、summary、COMMAND_MATRIX、C-EVIDENCE.zip | 64命令63/1、AgentSession29/0；默认effort已复验绿；仅两阻塞：无catalog+DSH_GO_MODEL启动FAIL4交W162，restart原驱动FAIL1由W161重新实证；最终自检31/0、owned dshd=0，未允许覆盖GO.1 |
| W160 | DONE scoped · owner `297f46cb`，Lead独立Node391/391+typecheck/boundary/compile全0 | 修默认模型effort.noteModel缺失 | 双backend.ts+integration测试、`tmp/W160/RESULT.md` | load.model空时仅取本次catalog active.id且必须存在，ready.model同步，A/B/stale隔离；真实不同默认A/B OK28/0、ConPTY effort OK9/0，low实际model/selection均生效；W152重固定终验，其他surface不越界 |
| W161 | DONE · owner `5dbe2aa6`，生产无restart缺陷；Lead全新home复跑OK8/0 | 核/restart真实生命周期 | `tmp/W161/RESULT.md`、修正W152驱动、A-B与两轮矩阵 | 原红为DSH-GO占位断言+/q后主屏采样假阳性；修正后原驱动OK8/0、两轮OK32/0、A-B OK16/0，Node391/391+构建全0；Lead首次复用固定home红4，换全新RUN后OK8/0，证明驱动必须唯一home；生产Go/TS零改 |
| W162 | DONE scoped · owner `74734c87`，Lead集成Node393/393+typecheck/boundary/compile全0 | 无catalog时DSH_GO_MODEL显式模型回退 | patch `0ad727f8…`，双backend+integration | load.model→当前open显式env→有效catalog active顺序；无catalog baseline36/0、placement25/0、A-B16/0，W160 catalog28/0+ConPTY9/0；无catalog effort元数据由W165补，未伪造 |
| W163 | IN_PROGRESS · `HS-WORKTREE`/`HS-15` 文档集成；`HS-16A`/`HS-07`/`HS-07W`/`HS-08`/`HS-09`/`HS-12`/`HS-13`/`HS-16D` INTEGRATED_GREEN(scoped)；`HS-GOAL-TRUTH`/`HS-10` 在途；`HS-05` DELIVERED(policy-only) | 最终封版复验 | `docs/host-worktree-discipline.md`、`docs/host-external-deferrals.md`、`$S/HS-07W/hs-07w.patch`、`$S/HS-08/HS-08.patch`、`$S/HS-09/HS-09.patch`、`$S/HS-12/HS-12.patch`、`$S/HS-13/HS-13.patch`、`$S/HS-16D/HS-16D.patch` | HS-12 轮次已补 `NewWithAPI`+`DSHD_LLM_API`，build/vet0；AutoTitle TempDir flake 1/3 单跑绿。HS-16D uplink/mux open、HS-09 job.list 单帧仍残。HS-05 policy-only。稳定版NO-GO |
| W164-WEB | IN_PROGRESS · web 实例 PID 38484 已关闭；Team Mode T15A/T11/T13 DONE(scoped)；TUI 0.19 适配器 BLOCKED | Web 线接手与真实验收 | `projects/dsh/plugins/团队模式/TASKS.md`、`artifacts/rc1/**`、`tmp/team-mode-plan/t15a-*`、`docs/web-takeover-2026-11-08.md` | Host24/24、Client13/13、合并37/37、TierB 无阻断；T13 T16/T18 APPROVE，Lead 443/443、build、pack 全 exit0；T12B 完成；TUI-019-GO-ADAPTER 仍阻塞 |
| W176 | DONE · owner `9fab9f4e`，Early四项均驱动FAIL，唯一产品FAIL仍属W172 | 核查early full宿主与placement红灯 | `tmp/W176/RESULT.md`、summary、交叉审计OK | model-v1 15/0、placement-v2 12/0、negative-v4 9/0、no-catalog-v1 19/0；Early300s实际4.683s上游turn/error，/tree raw已有unavailable，placement固定1500ms过短；四驱动修复交W179，no-catalog-v2屏幕残字归W172，其他W162/W165/W166/W170/W173不回退 |
| W177 | DONE · owner `c8626fab`，目标复跑6/6 exit0、W177_ALL_GREEN=1 | 修W163流水线缺文件与资源 | `tmp/W177/RESULT.md`、w177.patch、final RUN `w177-20261106-182203` | Tab20/0、Enter18/0、Ctrl21/0、now21/0、v4 audit21/0，star12/12+update全PASS；修W169前缀物化/audit父目录/star官方tsx loader/update exit标记/recon命令构建源；仅改tmp/W163，无新产品FAIL，early400单列；目标复跑非新full，不更新GO_ALLOWED |
| W178 | DONE · owner `7489156e`，OK14/0，无产品回归 | 核查restart effort与conformance红灯 | `tmp/W178/RESULT.md`、audit-summary | W161加模型切换完成屏障后33/0，session-effort真实low且restart保留；W164按HTTP host receipt+raw done/error+defer断言后120/0，A/B/gap raw done实际12/9/14ms，原undefined等待是驱动错；修复交W179合入W163 |
| W179 | DONE · owner `f3d7d104`，W161/W164合计OK161/0，W179_ALL_GREEN=1 | 合入W178驱动修复并复跑 | `tmp/W179/RESULT.md`、RUN `w179-20261106-190002-3412` | W161=33/0，W164=120/0+defer8/8；setModel完成通知+overlay关闭后唯一/effort，HTTP receipt唯一关联turnId与raw done/error、raw end<outcome<public end；生产7902文件零diff，进程/孤儿0。W176四驱动未含本单，转W181 |
| W181 | IN_PROGRESS · 旧owner已交OK140/FAIL1，DeepSeek workflow独立复验在途 | 合入W176四驱动并复跑 | `tmp/W181/RESULT.md`+备份`w181-pre-deepseek-backup` | 最终UI37/0、placement32/0、negative20/0、no-catalog51/1；唯一FAIL仍W180第二轮残字，未删断言；compact turn-5通过，生产7902文件零diff，进程0。旧证据hash f127cd72…已备份，DeepSeek workflow `workflow-31029409e1764a13`继续独立收口 |
| W175 | DONE · owner `35f8f420`，当前NO-GO，验收套件已就绪 | row identity协议独立验收 | `tmp/W175/RESULT.md`、run.sh、frames/row-protocol/conpty/oracle | frozen-d63记录帧FAIL7/MUTATION_MISS0，stable123/8；row协议9/1（refs flushed但normal buffer user/assistant=0）；真实80=58/6/U1、120=63/12/U0；c9b预检121/10且HIST_B回归不可集成。owner冻结包后`bash tmp/W175/run.sh <package> <fresh-label>`复跑 |
| W163-PREP | DONE · owner `63fe04d4`，主smoke全绿，full入口可用 | 并行准备最终封版流水线 | `tmp/W163/run_final.py`、README/RESULT/VALIDATION | 新C:dshd go.1/bin-check1/0，正式pack 3697 files/backend hash一致，真实80×24 ConPTY16/0；full=41验收+3构建，64命令/29 AgentSession，覆盖W152/W161/W164/W167–W171；唯一RUN/home、子命令≤300s、source-drift/UNVERIFIED/FAIL均NO-GO；最新W173 minimal8/0，full待W172稳定后Lead重跑 |
| W172-VALIDATION | DONE · owner `1e5a79d6`，Request changes，不可集成 | W172独立帧与ConPTY验收 | `tmp/W172-validation/RESULT.md`、REPRO、failure-set | 最终src d63/lib6def一致且tsc noEmit/emit=0；帧original120/8→d63 123/5，剩resize2+thirdparty/reanchor2+大shrink1；真实80=43/5/U1、120=49/6/U0，audit=10/4与12/5，tool-call/result=0但journal/v4单份；merge条件与最小帧已交W172 |
| W173-VALIDATION | DONE · owner `7014ba20`，owner在途NO-GO；candidate绿不代表共享版 | W173独立行身份与Export验收 | `tmp/W173-validation/RESULT.md`、summary | 坏基线OK3/FAIL4；candidate矩阵OK11/0、真实80=71/0/U7、120=72/0/U6但基于旧lib；preDone有效样本0/6=UNVERIFIED；owner source仍重复声明故其副本NO-GO。Lead随后已集成candidate并重验，共享版由W173-SHARED-VALIDATION独立收口 |
| W173-SHARED-VALIDATION | DONE scoped · owner `9b0b89bf`，共享W173=GO（不等于GO.1封版） | 共享集成版同版source+lib最终Export验收 | `tmp/W173-shared-validation/RESULT.md`、summary | A-80 OK73/0/U7、B-120 OK74/0/U6；三root v4 turn=1，missing/unexpected/duplicate=0、exactOrder=true，TEXT/job/multiline/reasoning/tool配对/A-B/quit/owned全绿，source/lib/package跑前跑后无drift；真实首次Export均postDone，严格preDone UNVERIFIED，确定性pre/post/duplicate done OK11/0、minimal8/0 |
| W167 | DONE · owner `2c22b0f2`，W160 NO-GO：80=69/5、120=75/8，now wire16/0 | 窄终端/inline/resize真实ConPTY补验 | `tmp/W167/RESULT.md`、REPRO、evidence | 基础inline/prompt/tool/composer/Tab/Enter/model/context/effort/jobs/resume/restart/quit均OK；FAIL=输入碎片W170、resize/scrollback W171/W172、Ctrl+Enter W169、Export W168；/models无别名非回归，驱动假阳性已单列；交付74/0，自有进程空 |
| W168 | DONE · owner `c591e0e3`，当前树FAIL，交W173修projection | 最新W162+W166 Export完成回合复验 | `tmp/W168/RESULT.md` SHA256 `cd546403…`、summary | 80 OK25/FAIL8、120 OK26/FAIL7；缺TEXT/JOB_PARENT assistant与reasoning，W166 timing OK；minimal重复v4 turn=1致行覆盖OK6/FAIL2。**oracle纠偏**：6个preDone sentAt全晚于rawDoneTime，不能证明raw done不修复；根因仍由minimal+最终导出成立，真正pre窗口由W173-VALIDATION重验 |
| W173 | DONE scoped · Lead集成独立candidate，identity/golden/Node397+typecheck/boundary/compile全0 | 修projection跨root turn行身份 | `/tmp/w173-projection-candidate.patch`+`scripts/verify-projection-turn-identity.ts` | rootTurn+turn+step区分连续run，streaming/reasoningStep/attempt/map/reset同步；共享identity OK、5 golden 83 rows OK、Node397/397与全量构建全绿；真实Export独立验收在途，preDone窗口硬门槛 |
| W174 | DONE · owner `a4cf745d`中断后Lead独立验收 | 刷新64命令/AgentSession矩阵到当前 | `docs/tui-interface-matrix.md` | 已更新W152/W161/W162/W165/W166/W167/W168/W169/W170与MCP/context/metadata，保留W148历史；Lead `doc-links=0`，MATRIX_LINKS=20 MISSING=0 |
| W169 | DONE · owner `512dc88d`，当前产品全OK，无需修复 | 最新树Ctrl+Enter与placement语义复验 | `tmp/W169/RESULT.md`、summary、final OK34/0 | Tab followup20/20、Enter steer18/18、Ctrl+Enter interrupt+followup21/21，direct now wire21/21、v4/transport21/0；export无草稿粘尾；W158旧Ctrl+Enter=now假设作废，W167旧根因UNVERIFIED不倒推，生产零改 |
| W170 | DONE scoped · owner `27398757`，真实双尺寸各OK44/0，Lead reverse+Win32+Node397/397+构建全绿 | 修ConPTY直接输入Escape碎片污染 | patch `e0c7a267…`，仅parse-keypress.ts+verify-win32-input.tsx | Win32分片hold有界30s，普通Escape仍1s/64B不变；真实4轮直接长文本/中文/emoji/Help/model/resize逐字匹配，compile/package/smoke全0；input-terminal另4项和build另8项均非解析面，W167完整矩阵留W163 |
| W171 | DONE · owner `83d0aa3a`，FAIL：OK25/FAIL6，交W172 | 复核inline scrollback重复内容 | `tmp/W171/RESULT.md`、REPRO、final OK29/0 | 两尺寸before-resume缺text/job/multiline assistant，after-resume各1，after-restart user/assistant唯一但tool-call=0；journal/v4角色计数正确，restart userFrames不增；稳定采样排除瞬态双份，指向src/ink/log-update.ts shrink/anchoredPad/viewportY/repaint，不改core rows/Go |
| W172 | DONE · owner `9bdaf923`，明确未完成/补丁不可合并，协议实现交W180 | 修inline replay/resize渲染重复与缺失 | `tmp/W172/RESULT.md`、实验patch `c6151292…` | compile/geometry/thirdparty/long-scrollback/resize/trace-scene部分PASS，但真实120=41/4、80=39/3，verify-jobs-panel D1为候选引入回归；cells无row identity，log-update-only无法保留旧assistant并放对新user，禁止引用早期45/45 |
| W180 | DONE scoped · DeepSeek，协议/帧/编译GO；真实ConPTY NO-GO交W182 | 实现row identity协议 | `tmp/W180/RESULT.md`、最终patch `cd881a24…`、frozen-w180-5 | row id=ChatRow.id，WeakMap+Frame.rowIds+物理帧对齐，冷replay连续flush；W175帧131/0、row协议10/0、typecheck/compile/geometry/jobs D1全过。真实80=58/6/U1、120=68/7/U0；toolCall/toolResult不可见、120 after-resize assistant丢失、frame109/110 role loss。live projection实验未纳入且致before-resume重复，交W182隔离 |
| W181 | FAILED · DeepSeek子代理返回null，无可验收结果 | 合入W176四驱动并复跑 | 旧Kimi证据`tmp/W181/RESULT.md`+backup | 旧证据140/1仅参考；DeepSeek复跑未返回RESULT，不能关闭。重派W183单写W163，不复用旧绿 |
| W183 | FAILED · 无最终RESULT，仅production hash落盘 | 重跑W176四驱动真实复验 | `tmp/W183` | 未返回目标组结果，不能关闭；重派W189单写W163 |
| W189 | DONE · owner `8bac9d59`，四组55/0、verify63/0；no-catalog v2保留1产品FAIL | 重跑W176四驱动真实复验 | `tmp/W189/RESULT.md`、summary、123文件hash | ui15/0、placement12/0（sleep60/journal seq9/callId/raw done/2s行）、negative9/0、no-catalog v1 19/0；v2 21/1唯一残字归W182；驱动改实时模型id解析，旧id 404原样保留；生产diff4属W187/llm并发，不报0 |
| W184 | DONE · owner `b7197fa7`，已落共享树，LLM测试PASS | OpenAI请求严格兼容与可选字段协商 | `tmp/W184/W184.patch`、compat.go、llm-test.log | 默认wire字节不变；400/422仅在响应明确指向可选字段时一次最小回退，401/403/404/429/5xx不重试，最多4次收敛；mock严格网关/凭据脱敏/usage/tool/reasoning通过；`go test ./internal/llm` PASS、vet/build exit0 |
| W182 | DONE scoped · owner `fa4d61a1`，toolCall行已真机修好，整体NO-GO交W185/W186 | 修tool行真实终端可见性 | `tmp/W182/RESULT.md`、patch `2af9cf98…` | 80=60/4/U1、120=69/6；ctrl-o `> printf`与before-resume toolCall转绿；帧131/0、row10/0、compile/typecheck/jobs D1/geometry全过。resume toolResult根因在Go load缺callId/turn/step，120 after-resize assistant与frame109/110 role loss仍红；禁止集成 |
| W185 | FAILED · 无可收产物 | 补session.load tool身份字段 | 无tmp/W185 | 未改源码，重派W187 |
| W187 | DONE · owner `63784967`，Lead复验transport=0、cmd targeted=0 | 补session.load tool身份字段 | `tmp/W187/RESULT.md`、W187.patch `cdd7abca…`、契约§4.4 | Message新增真实CallID+Turn/Step *int，loadToolResultOf只取事件自身身份；无真值省略不写0/不合成；探针recovered0→1，full cmd140s绿、vet/build0；剩余isError/meta/structured交W190 |
| W190 | DONE · owner `1ea4639c`，Lead复验transport=0、cmd targeted=0 | 补load工具失败与元数据字段 | `tmp/W190/W190.patch`、`green-test.log` | Message新增IsError/Meta/Structured，真实值透传、缺值省略不伪造；patch reverse-check0，targeted test全绿 |
| W191 | DONE scoped · 默认值已落共享树，最终CI复验 | 更新宿主默认模型id | DSH-GO cmd/dshd、cmd/dshtui、internal/llm | 所有当前默认值已改为`group/deepseek-v4.1-flash-1017`；env/flag覆盖保持；新dshd构建成功 |
| W192 | DONE scoped · W163 config已切新id，smoke由最终full复验 | W163夹具切新模型id/动态解析 | `tmp/W163/config.json`、drivers、W189 runner | config新id，旧id仅W189注释；active驱动优先实时/v1/models；未单独smoke，最终full覆盖 |
| W193 | DONE scoped · owner `987dc208`，patch `55cb843e…` 5文件；80=64/0，120=74/1 | 续修W188视口/tool result | `tmp/W193`、`w193-final-tui.patch` | Chat浮层`onHeight`接线修`/new`覆盖；`/restart` tool行随新dshd全绿；80 frame异常2→0；120 frame125 user.tool一帧丢失仍真红，未粉饰；view-spec/events消费callId/turn/step+isError/meta，未回退 |
| W163-FINAL | FAILED · RUN `w163-final-20261106-2302` exit1，9任务FAIL/sourceDrift | 最终 full CI | C:W163-evidence/`w163-final-20261106-2302`、summary.json | 构建/安装/769覆盖/29 AgentSession绿；W164旧id404、W167x2、W168x3、W170、W171x2红；W193已补80 overlay/restart，120 frame125仍真红；sourceDrift=true，不能作为封版证据；W194/W195收口后重跑 |
| W194 | DONE scoped · patch `9d4dbbdb…` 141行；W164=120/0、W168=33/0×2 | 定向复验W164/W167/W168/W170 | `tmp/W194/RESULT.md`、w194-targeted.patch、tgz | 根因=dshd每turn恒报turn:1，投影器(turn,step)键覆盖上轮；translator按到达序生成durable ordinal；W164 defer8/8、W168 minimal8/8/80=33/0/120=33/0；W167 export转绿，剩model-picker/inline抖动；W170 `/model`顶部裁切非本delta |
| W195 | NO-GO · 根因已定位，源未改、patch 0字节 | 修W171 scrollback唯一性 | `tmp/W195/RESULT.md`、conpty.raw.log | 活屏在排队面板/queued增高时整屏重写抹转录；JobCard与tool行重复致toolCall2/Result1；W195不隐藏内容故拒凑绿，交W196按Lead裁定修 |
| W196 | DONE scoped · owner `2c59b256`，w196.patch `43f6fa96…` | W171排队残屏+JobCard去重 | `tmp/W196`、w196.patch、final2/final3 logs | 80=42/0；120最终40/2（排队面板打开瞬间assistant.text/user.job时序丢失），不再误报42/0；JobCard唯一呈现、queued增高保转录；frames/row协议/typecheck/compile通过 |
| W197 | DONE scoped · owner `c3433842`，patch `b38e8eb3…` | 修 /model 裁切并收口W167/W170 | `tmp/W197/RESULT.md`、w197-model-overlay.patch | W170短会话4连12/0，深帧仍整块不出现；W167=74/2，只剩Ctrl+Enter序列；model-picker/durable/models别名/inline/export均转绿；log-update pad钳回heights，未动布局/文案 |
| W198 | DONE scoped · patch `710ea6ac…` 97行；W170 80=44/0、W167 80=75/1 | 收口W167/W170/W171三缺口 | `tmp/W198`、w198-final-tui.patch | W170 80达标，120=43/1残留；W167 Ctrl+Enter三项绿，80=75/1；W171 80=41/1、120=38/5；行完整性同族红未修，交W199；W175 row10/0，compile/typecheck0 |
| W199 | PARTIAL/QUARANTINED · patch `a4313ed8…`；f4-170/167两项因与W200并发编译污染作废 | 修行完整性根因并收口120 | `tmp/W199/RESULT.md`、w199-row-integrity.patch | 重绘贴底/高度闸/keepPixels已落；W170 120曾44/0；W171 80仍40/2、RESULT占位未填；verify4最后两项不可信，不采信，由W200干净树重跑 |
| W200 | DONE scoped · 原owner已停笔，LIVE只读；GO.1仍NO-GO | 用官方上游log-update替换自研重绘 | LIVE `log-update.ts`=`7752fc38…`、`ink.tsx`=`560053ee…`；GO1根`GO1-20261107-0410` | W200最终基线W171 80=41/1、120=40/2，W175 frames130/1、row9/1，W167双尺寸74/2；T00/T01/T02已完成，后续账本迭代归GO1-T03，禁止另起写者/第二PTY |
| GO1-T03 | PARTIAL · 已停笔但NOT DONE，禁止当封版证据 | 物理视口账本T03 | `GO1-20261107-0410/T03-RESULT.md`、patch `w201-t03-ledger.patch` | 源`aeb0b1e7…`/lib`6a6aab5a…`；frames131/0、W16780=76/0、W170双尺寸44/0；row9/1、W17180=41/1、120=41/1与40/2。仅改log-update.ts两处，R1/R2接手 |
| GO1-T03-R1R2 | DONE scoped · owner `b94e828f`，R1/R2 RESULT与hash链齐 | R1 identity+R2停泊账本同lane | `ledger/{R1-RESULT.md,R2-RESULT.md,PTY-MATRIX.md}`、patch `t03-r1-r2.patch` `c71a3065…` | R1 row9/1→10/0、R2 16/0、frames131/0；W171两尺寸各连续3次42/0，最终注释版复跑仍42/0。剩W167宿主/驱动与W170 ink浮层独立后续，不回写账本；尚不能full |
| GO1-W167-DRIVER | IN_PROGRESS · driver owner `e22ae81b`；含2条封版口径BLOCKER | W167 resume/export与reasoning驱动判据 | `review/AUDIT-TUI-KNOWN-ISSUES.md`、最新`leadw167b-w167-80-a.log` | 最新80=73/3/0，120未跑。审计另证：B1 reasoning零帧UNVERIFIED与full要求0冲突；B2 80列picker须↓滚到kimi而非判不存在；selection误比INITIAL_MODEL；steer首轮为模型答LONG_DONE。必须先修夹具/口径再判产品，不得把红/UNVERIFIED刷绿 |
| GO1-W175-REAL-MATRIX | READY · qa，依赖当前W170/W171/W167同hash稳定 | W175真实80/120与两个post-hoc audit | `$G/qa/**`、真实dshd+ConPTY | 静态frames131/0与row10/0不等于真实矩阵；按任务表§5跑W17580/120及audit，FAIL=0 UNVERIFIED=0，当前未跑 |
| GO1-BINARY-MATRIX-REFRESH | READY · qa/backend，单PTY串行 | 用含stderr修复的新dshd重跑冻结矩阵 | `bin/dshd-fresh.exe`=`b78cfd15…`，已实测含`DSHD_TRACE_API` | 08:44最新W167已用新binary；W170/W171此前矩阵仍用旧5c7b1105，冻结full前必须同hash新binary重跑并确认屏上无旧stderr噪声 |
| GO1-W170-OVERLAY | DONE known issue · owner `962fef10`；空白带单列 | W170 overlay修复及W171回归收口 | `ledger/W170-OVERLAY-RESULT.md`、patch `aa4bb24f…` | 同hash W17080/120=44/0、W17180/120=42/0、W175=131/0+10/0；React#300已修且restart 27断言全过。**可见缺陷：关/help或/jobs后composer下方空白带仍存在**，转W170-BLANKBAND，不能当完全关闭 |
| GO1-W170-BLANKBAND | READY · 只读评估，未派产品写者 | 关闭面板后的composer空白带 | 先只读`rc/src/ink/log-update.ts`、`frame-geometry.ts`；R1/R2高水位方案评估 | 必须给出无空白带且W171/W175/W167不回退的方案与先红证据；不得通过裁切/隐藏面板或删高水位断言凑绿，阻塞GO.1直至用户/Lead明确接受或修复 |
| W186 | FAILED · 无可收产物 | 修TUI视口与tool result剩余 | 无tmp/W186 | 未改源码，重派W188 |
| W188 | PARTIAL · 有64/3/4真实实验现场，无RESULT/patch | 修TUI视口与tool result剩余 | `tmp/W188/evidence/exp2.log` | view-spec/events已有改动；after-restart toolCall/Result丢失疑旧dshd缺W187字段，frame125 loss与new-session观察仍红；由W193续修 |
| W164 | DONE · owner `7fc2878f`，OK3/FAIL1，真实FAIL交W166 | 审计旧conformance四红 | `tmp/W164/RESULT.md`、summary、final OK19/0 | placement.steer/text-identities/cancel.receipt均为旧断言或级联，真实行为OK；A-shared-2为真实公开idle早于raw done竞态，ab-race 3/3复现交W166；AB.no-fabricated-stream也是旧零流断言；生产零改，进程卫生全绿 |
| W166 | DONE scoped · owner `9e842f55`，Lead集成Node397/397+typecheck/boundary/compile全0 | 修公开turn.end早于host-ready竞态 | patch `a2037f52…`，仅双events产品改动+测试 | v4 turn/end暂存至raw turn/done或终态turn/error，reason/time保留、reset/A-B/重复done正确；ab-race修后OK7/0、W15838/0、W16116/0；W164旧3断言保持红，Lead合并后全量门禁全绿 |
| W165 | DONE scoped · owner `83a3d79b`，Lead集成TEST/VET/BUILD=0 | 无catalog环境模型真实元数据 | patch `820a5768…`，10 files | 仅catalog物理缺失且显式env/--model时从当前/v1/models精确发现ephemeral投影；隐式默认不发现、存在/models=[]/损坏不回退；真实A/B34/0、ConPTY14/0，effort max/xhigh与selection生效且不创建catalog；只读/effort set不持久化，upsert/reorder才持久化，Lead全仓全绿 |
| W159 | DONE scoped · owner `03df4a47`，Lead独立Node389/389+typecheck/boundary/compile全0 | 启用rename/color/init正式capability | 双backend/events/manifest与integration tests | start/reset refresh+seed、live frame代次排队、change去重、dispose idle；精确rename/color/A-B/reopen/init三态；真实Transport30/30、registry66、mirror全绿；W152最终ConPTY |
| W158 | DONE scoped · owner `ffb77829`，Lead独立Node386/386+typecheck/boundary/compile全0 | 修steer当前turnId拒绝/真实TUI复验 | 双placement.ts/test仅4文件、`tmp/W158/RESULT.md` | joinsActiveTurn仅允许steer共享当前active turnId并保留cancel/retraction；真实backend38/0、正式ConPTY26/0、v4 11/11+5/5，steer同turn step2，followup/now独立turn；W152打tarball终验 |
| W157 | DONE scoped · owner `ef7ea27b`，Lead集成TEST/VET/BUILD=0 | 修生产cmdRun step/TTFT/实际effort持久化 | patch `856ee7af…`，16 files，dshd `841e2cae…` | 修后backend OK69/0、durable OK20/0；真实steer/A/B/restart REAL75/0+AUDIT33/0，首步与steer step effort均真值，v4Seq/time映射零错，W149/W145/W138与全仓全绿；v4 envelope为成功边界时间、TTFT由即时journal；W153 placement steer turnId拒绝交W158 |
| W155 | DONE scoped · owner `86bd19e3`，Lead独立Node384/384+typecheck/boundary/compile全0 | W149 MCP/sideQuery正式backend接线 | 双backend/events/manifest/transport/side-query及测试 | context full、mcp真实servers/actions、side.query非流式+真实HTTP NDJSON、AbortSignal side.cancel、reset/dispose side.close、A-B/迟到text屏蔽；未用普通session.prompt；真实ConPTY归W152 |
| W142 | DONE scoped · 已由W150宿主+W159正式capability收口 | rename/color/init | 双helpers及宿主metadata/init Transport | helpers24/24、W150真实86/0与全仓、W159 Lead Node389/389与构建全0；W152真实metadata7/0，禁止按旧READY重复派单 |
| W143 | DONE scoped · owner `c3755f4a` | ViewSpec/raw history/step stream保真 | 双view-spec/trajectory-history/step-stream及测试 | Node32/32、tsc/boundary全绿，6组MIRROR_OK；ViewSpec card/table/plain映射、W145 transcript.page、raw history tool+step/delta/TTFT/A-B；load缺callId/turn/step仍跳过，events接线待W147 |
| W144 | DONE scoped · owner `2bb665a8`+rebase `492ae9d9`，Lead集成TEST/VET/BUILD=0 | effort.set与完整goal Transport/events | rebased patch `de808be7…`，20 paths | setEffort catalog/store/runner/v4 selection、goal snapshot/pause/resume/change全绿；保留W138 Steer与W145 checkpoint，新增steer+effort+checkpoint同turn回归；全仓-p=1复验全绿（wsdeps环境性红单跑PASS后复跑过），W147接线与真实ConPTY待做 |
| W145 | DONE scoped · owner `143ff0fc`，Lead集成TEST/VET/BUILD=0 | fork title/anchor、rewind、transcript.page | patch SHA256 `5f0a6156…`，16 scoped paths | fork anchor/title真实持久化与逐字回显；rewind真实checkpoint/files-first/partial；transcript.page durable oldest-first/cursor/A-B/restart；全仓与真实OS两次进程OK12/12，W135 helper36/36。未做TS接线/真实模型ConPTY；transcript每请求全量解码v4非增量I/O |
| W146 | DONE scoped · owner `0341f633` | placement后真实TUI可玩性复验 | `tmp/tui-current-conformance/RESULT-W134.md`、机器summary | W134快照P0D OK57/FAIL0/UNVERIFIED2：普通4轮真实assistant、trace/export、compact真实写盘、models/new/resume/quit、followup pending/retract/requeue、Ctrl+Enter now均过；生产steer与独立now wire未验，交W148。收尾并发Go/images漂移明确，未外推后续源码 |
| W126 | DONE · owner `66194003`，Lead核读 | TUI `/trace` trajectorySource/保存export缺口 | `docs/tui-trace-capability.md` SHA256 `7ee820e4…` | 官方/trace不要求transcript；通用fold已存在，golden得到11 raw/7节点含成功工具与ok/error turn；/trace没有保存写文件，Markdown属独立/export。当前缺dsh-go Channel/ConPTY验收与完整raw/step/stream，W128补P0测试；正式registry为src/backends/dsh-go |
| W127 | DONE · owner `a9d01b3c`，Lead核读 | `/view`官方存在性否定审计 | `docs/tui-view-capability.md`、`docs/tui-view-session-note.md` | 固定TUI0.18.0+官方0.2.1-rc.5未找到slash /view注册/handler或view edit规格；真实存在的是str_replace_editor工具子命令view只读、create/str_replace/insert修改。commands仅list无execute；当前产品代码零改动，不新写第二套编辑器/Transport。附带serve_session_ops直接OS读写问题另记未动 |
| W128 | DONE scoped · owner `69751b41`，Lead独立复跑并处理island边界 | dsh-go trace/export P0可重复验收 | 保留`src/dsh-go/trajectory.test.ts` SHA256 `a87d54d9…`；backend重复测试备份后移除 | 真实golden→translator→fold得到11 raw/7节点/成功工具/ok+error turn；/export真实Markdown/stale不写/写失败null。Node 4/4、typecheck过；backend内直接import dsh-adapter触发island红，Lead未放宽门禁，移除重复测试后source 2/2与verify:boundary均exit0。ConPTY/完整raw history/step stream未验 |
| W129 | DONE · 另一主会话架构证据，Lead吸收为方向文档 | 插件static/process/wasm与内存实测边界 | `docs/plugin-runtime-direction.md` | enabled=false是真能力开关但57→2工具内存差异噪声级；process价值是隔离/独立分发不是省内存；当前process未实现且fail-loud；不把“每用户自己编译exe”当默认路线，不立项全量拆进程 |
| W103 | DONE · owner `731c6885`，Lead 集成 | 设置模态几何与mask一比一（实现+直接视觉测试） | `plugins/dssettings/settings.go`、`settings_visual_test.go`；patch SHA256 `faf69dda…` | 官方800px/min(800,H-48)/双轴居中、nav188/row40、左navTitle+右header54/close28、深浅mask与Backdrop；1182 `{191,24,800,784}`、1920 `{560,116,800,800}`；先红旧720/nav32/无mask，改后全仓test/vet/build绿。Derived：ShadowXS、overlayTop24、长文550；完整焦点圈/inert未验 |
| W104 | DONE scoped · owner `6647890f`，Lead 集成 | 官方5 section/11 general slot/HIDE/键盘/A-B模型与状态测试 | `plugins/dssettings/model.go`、`settings_center_state_test.go`；patch SHA256 `fb8b7479…`，+320行 | BuildOfficialSections/buildOfficialSections已入：5 section/order/来源、11 slot/默认12行、transcript-view 1slot/2行、缺真值HIDE、SetPermission不冒充默认preset、PluginList不冒充inventory；UI全仓test/vet/build绿。**尚未接settings.go实际绘制**，迁移归W106 |
| W106 | DONE · owner `b40c7414`，Lead 集成 | 设置官方导航实际渲染迁移与旧测试收编 | `plugins/dssettings/settings.go` + 11个受影响测试；patch SHA256 `f17ed8ac…` | observe/Open/show已接buildOfficialSections；顶级仅有真值general/models；appearance/shortcut/current-version归general；defaultPreset真实describe归general.permission，409新revision/500retry/unset/readOnly/HIDE通过；自造顶级页与旧专用分支删除，W103几何/mask不变；两尺寸Viva/键鼠/A-B、全仓test/vet/build绿 |
| W99 | DONE · owner `8e29b78c`，Lead 集成 | 官方设置模态结构树 | `../dsh-ui/docs/official-settings-tree.md`；patch SHA256 `e07640a9…`，314行 | 官方5 sections=account/general/models/plugins/agent-presets；11 general slot可形成12行；设置→内置插件仅只读清单，独立main:plugins不混层；当前默认设置/目录与新会话默认/会话/关于多造或错层；doc-links PASS |
| W100 | DONE · owner `c6def731`，Lead 集成 | 官方主壳结构树 | `../dsh-ui/docs/official-shell-tree.md`；patch SHA256 `ce634077…`，232行 | Windows40px caption、左栏、Conversation/Hero/Chat/Composer/dock、右栏DockKit、主区Trajectory/ToolCallTree；188条证据链接/43 CSS selector全命中；官方Trajectory属主区conversation.view，不是右栏时间线；无官方固定24px statusbar |
| W105 | DONE scoped · owner `b13e12f5`，Lead 按真实交付验收 | codeload tarball下载并构建上游TUI（替代失败的Git pack） | `../dsh-TUI-upstream-src/**`、`tmp/upstream-tui-build/RESULT.json`、`docs/upstream-tui-build.md` | 主tar SHA256 `40ebf690…`、vendor `385d4971…`；install=0/typecheck=0/smoke=0，build=1：103门禁8 FAIL（contract/plugin-grant-lifecycle/minimal-ui-naming/terminal-images/image-preview/composer-image-tokens/i18n/protocol-single-source）；无.git导致3项，其余原样保留；RESULT `completed_with_build_gate_failures`，未伪造git历史或改源码 |
| W101 | DONE · owner `19e3e821`，Lead 集成 | 官方素材复用清单与提取 | `../dsh-ui/docs/official-assets-reuse.md`、`../dsh-ui/assets/official/**`；patch SHA256 `6c4f0216…` | 56份素材/支持文件+manifest，45官方JSX/10 asar原文件/1 CSS APNG；Lead复核manifest 56/56、文件57、sha256全匹配、doc-links PASS；15个dsassets同物逐字节一致，Montserrat三字体闭环；未做GUI像素/Viva动画验收 |
| W102 | DONE · owner `006269a7`，Lead 集成 | 当前GUI结构差距矩阵 | `../dsh-ui/docs/gui-structure-gap.md`；patch SHA256 `dcc6b23e…`，239行/64节点 | 已证实官方设置壳=800px/nav188/row40/居中+mask，当前=720/nav160/row32/top64/无mask；当前7入口只有「模型」是官方顶级section，外观/权限/关于在general页，默认设置与目录与新会话默认非官方顶级导航，插件管理混淆两种官方层级；matrix_rows=64/links153/errors0，doc-links PASS |
| W81 | BLOCKED(W79+W82集成后同轮外派) | durable 👍/👎 feedback宿主持久化与UI回读 | 宿主feedback additive存储/Transport/契约；UI `plugins/dsconversation/feedback*`、apiclient/cmd/app | 真实message/v4Seq身份、A/B/重启回读、覆盖/清除/错误；本地拇指不算durable，不伪造状态 |
| W10 | DONE · rev `24272b5`+`5bba1ab` | 陈旧二进制导致假回归；无需改宿主实现 | `scripts/bin-check.py`、`scripts/h10-chain.py`、`scripts/guard-selftest.py` | 命令 `python scripts/h10-chain.py`（M289记录）→ **OK=6 FAIL=0**，含 prompt accepted+原会话 load assistant>0；`python scripts/bin-check.py` 为套件首门且有摸旧必红反证。证据 `docs/host-side-closeout.md` §8、`docs/verify-rules.md` §1。原 id 判据由该行为链替代，**不声称 id 相等已验**；跨仓/真实 home 遍历另验 |

执行代理先按 `AGENTS.md` §0 核对最新源码和其他会话改动，再把现有工作标成 IN_PROGRESS+owner；不得重复派发或实现。H8 只在写路径不冲突时并行；H6/H13 的线上联调不得在 H14 前靠私有帧格式“先跑起来”。**下一批代码子代理默认各自隔离 worktree/导出副本并交付 patch，共享工作树只由 Lead 串行集成与验收；当前在途任务不迁移、不清理，避免实现前红测互相阻塞完整包门。**

---

## 15. 工程纪律

```
cmd/dshd/          main：manifest → kernel → blank import 默认模块
internal/kernel/   四件事；禁止 import 任何业务包
internal/spi/      全部接口 + 数据类型（零实现）
internal/session/  事件溯源 + canonical 模型（唯一允许直接读日志的包）
internal/agent/    turn/step
internal/acp/      ACP 服务端
internal/llm/      SSE 客户端
modules/           静态默认实现（一模块一目录，一个 Register）
codecs/v4/         v4 编解码插件
wasmhost/          wazero 装载器
pluginhost/        go-plugin 装载器
modes/             内置模式（builtin，只读）
dsh.toml           manifest
docs/              设计文档（cordis-alignment、tool-spi 等）
```

**CI 七条**

1. `go list -deps ./internal/kernel` 不得含 `modules/`、`internal/{agent,tools,session,llm}`。
2. 在执行者独占的隔离副本中移出 `modules/` 后 `go build ./internal/...` 通过；共享工作树禁止删除、移动或改名。
3. 每个 `Tool.Exec` 的 error 路径必须给 `Hint`（AST 扫描）。
4. 完整 CI 目标为 `go vet` + `staticcheck` + `-race` 全绿；共享树未实际跑成的项必须标未验证，不得用 race-guard 或普通 test 冒充。
5. 工具 schema 快照：变更必须显式更新 `cmd/dshd/testdata/tools.snapshot.json`（防缓存击穿）。
6. 工具表 token 总量 ≤ 6000（分词实测，不估算）。
7. `dshd` 二进制体积 ≤ 25 MiB（25×1024×1024 字节，`-s -w`）。

| R3 | DONE · 01:08 定时复核 | Lead；宿主 `9cfe2f4` / UI `3925e84` | 只读两仓、任务表、worktree、进程 | 方向未偏离：Go 宿主+Viva 原生；无 Electron/WebView 回退；TUI 0.19 适配器 BLOCKED；HS-09/12 在途；T13 APPROVE；宿主 stable NO-GO。报告 `docs/scheduled-review-2026-11-08-0108.md` |
| R4 | DONE · 02:08 定时复核 | Lead；宿主 `9cfe2f4` / UI `3925e84` | 只读两仓、任务表、worktree、进程 | 方向未偏离；TUI2Go 无包；HS-09/12/13/16D 已集成；HS-10/GOAL 在途；HS-12 轮次、HS-16D uplink/mux、HS-09 job.list 仍残；宿主 stable NO-GO。报告 `docs/scheduled-review-2026-11-08-0208.md` |
| R5 | DONE · 03:08 定时复核 | Lead；宿主 `9cfe2f4` / UI `3925e84` | 只读两仓、任务表、TUI2Go 产物 | TUI2Go T1–T3/T5A–T5E/T6 DONE；T4/T7-PREP 在途；HS-14 READY 未派；P1/P2/P3 已登记；宿主 stable NO-GO。报告 `docs/scheduled-review-2026-11-08-0308.md` |
| R29 | DONE · 02:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读重启现场、session lock独立复验与后续依赖 | t164/t156/t166/t168已完成，t154由Lead复跑count20+race5通过；t167在修，t158/t159待最终复验。报告 `docs/scheduled-review-2026-11-09-0208.md` |
| R28 | DONE · 01:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、最终QA五路修复与TUI2GO桌面链 | t143 NO-GO，t153–t157在修；t160用 `/k` 保留原生窗口并带stderr降噪，未发布。报告 `docs/scheduled-review-2026-11-09-0108.md` |
| R27 | DONE · 00:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、W2 pre-step、可见权限回读 | 当前173/193；W2已真机14/20，t138只补worktree pre-step；t137补权限/registry双证。报告 `docs/scheduled-review-2026-11-09-0008.md` |
| R26 | DONE · 23:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、W2/W3/W4、Speech/Native与TUI输入；回收t105孤儿 | W2五路已齐待集成；W3/Native/Speech推进；Shift+Tab等未闭环；t105测试树已回收。报告 `docs/scheduled-review-2026-11-08-2308.md` |
| R25 | DONE · 22:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、W1总集成、workspace IPC修复 | W1四路已齐，t95恢复并见真实探针；t99修IPC，试玩未开放；W2/W3/W4等W1完成。报告 `docs/scheduled-review-2026-11-08-2208.md` |
| R24 | DONE · 21:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、141台账、t89/t90；补W1四路 | W0 35项全落地；t91–t94并行、t95集成待解锁；试玩仍等剩余TTY帧。报告 `docs/scheduled-review-2026-11-08-2108.md` |
| R23 | DONE · 20:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、试玩修复、A1/Cordis/W0现场 | 宿主步数与C1完成；TUI完成自述但RESULT/patch未同步；A1在途；试玩未重新开放。报告 `docs/scheduled-review-2026-11-08-2008.md` |
| R22 | DONE · 19:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、T19W3、TUI2GO-PLAY 与 T20 签收 | **宿主与 TUI2Go 双绿**；GUI-P0/P1 已并行启动，未 commit/push。报告 `docs/scheduled-review-2026-11-08-1908.md` |
| R21 | DONE · Windows-only 稳定候选 | Lead；AgentTeams `dsh-go2-stable` | T19W3 发布副本正式验收 | **GO_ALLOWED=1**；build/vet/full test/`-p=1` full race 全绿；0.2.2-go.1 本地候选完成，未 commit/push/tag；下一项 TUI2Go 实玩。证据 `tmp/GO2-TEAM/T19W3/RESULT.md` |
| R20 | DONE · 18:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、T19W3 `-p=1` race 与进程 | build/vet/full test 绿；串行 race 在途；GO=1 自述收到但 RESULT 未同步，候选未确认、未发布。报告 `docs/scheduled-review-2026-11-08-1808.md` |
| R19 | DONE · 17:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、T17W2 race/矩阵日志与进程 | full race 首轮 ConPTY/bash 超时，第二轮绿；QA 汇总中，RESULT/GO 未同步；T19W2 未启动。报告 `docs/scheduled-review-2026-11-08-1708.md` |
| R18 | DONE · 16:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、I17、T17W2 前置与进程 | T19RACE 完成；I17 自述完成但 RESULT 未同步且仍 in_progress；T17W2/T19W2 未解锁，stable NO-GO。报告 `docs/scheduled-review-2026-11-08-1608.md` |
| R17 | DONE · 15:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、T17W RESULT/GO_ALLOWED、CI 与进程 | **T17W completed，GO_ALLOWED=1**；Windows 全量 test/race 与真实矩阵全绿；T19W 解锁，未 commit/push。报告 `docs/scheduled-review-2026-11-08-1508.md` |
| R16 | DONE · 14:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、I15C、T17R 预检与进程 | T18RR/I15B/T15C 完成；I15C 在途且 RESULT 未同步；T17R 未启动；Linux ENV_BLOCKED；T19 封锁。报告 `docs/scheduled-review-2026-11-08-1408.md` |
| R15 | DONE · 13:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、T17 修复 DAG、T15 worktree 与进程 | T18R/I16 完成；T15 worktree 有 patch/探针但 RESULT/同步未完成；Linux ENV_BLOCKED；T17R/T19 未解锁。报告 `docs/scheduled-review-2026-11-08-1308.md` |
| R14 | DONE · 12:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、I14、T07R 账本与进程 | I14 完成；T07R RESULT 已落且 DRIFT=0，但任务仍 in_progress；I06B/HS-17/18 未启动。报告 `docs/scheduled-review-2026-11-08-1208.md` |
| R13 | DONE · 11:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、T12/T13 产物与进程 | T12/T13 实现完成；I12 在途、I13 待串行；I06 未解锁；本链无新增孤儿。报告 `docs/scheduled-review-2026-11-08-1108.md` |
| R12 | DONE · 10:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、I09、T12/T13、孤儿进程 | I04/I05/I08/I09 completed；settings 安全关闭；T12/T13 在途；2 个 terminal 心跳孤儿按 PID 清理；stable NO-GO。报告 `docs/scheduled-review-2026-11-08-1008.md` |
| R11 | DONE · 09:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、T05 干净树与 RESULT、三项 blocker | I01/I02/I03 completed；I04 在途；T05 干净树交付；T08/T09/T10 在途，I08/I09/I10 均入 I06；stable NO-GO。报告 `docs/scheduled-review-2026-11-08-0908.md` |
| R10 | DONE · 08:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队/goal、T01/T03/T06 产物、F4 源码 | T00/T01/T03/T06 completed；I03 在途；T07 接续；F4 已立 T08/I08 并加入 I06，单次 37/0 不放行。报告 `docs/scheduled-review-2026-11-08-0808.md` |
| R9 | DONE · 07:08 定时复核 | Lead；AgentTeams `dsh-go2-stable` | 只读团队状态、goal、两仓与 T00 产物 | T00 持续写入但未交付，其他 17 项按依赖 pending；无停摆/重复派单/共享树多写者；goal active，未改状态。报告 `docs/scheduled-review-2026-11-08-0708.md` |
| R8 | DONE · 06:08 定时复核 | Lead；宿主 `9cfe2f4` / UI `3925e84` | 只读两仓、HS-16B worktree、任务表、UI 主库 | HS-05/14-WIRE/16C 已集成；HS-16B 在途；按用户指令完成后暂停待重启；宿主 stable NO-GO。报告 `docs/scheduled-review-2026-11-08-0608.md` |
| R7 | DONE · 05:08 定时复核 | Lead；宿主 `9cfe2f4` / UI `3925e84` | 只读两仓、HS-14/HS-16C worktree、公开 CI | HS-14/HS-10/GOAL 已集成；HS-16C 在途；TUI2Go standalone DONE；宿主 stable NO-GO；公开 CI 仍红。报告 `docs/scheduled-review-2026-11-08-0508.md` |
| R6 | DONE · 04:08 定时复核 | Lead；宿主 `9cfe2f4` / UI `3925e84` | 只读两仓、公开仓、CI、worktree | TUI2Go `v0.1.0` 已发；宿主 GO.1 公开仓 CI 重跑；HS-10/GOAL 在途；0.19 profile 上游阻塞；宿主 stable NO-GO。报告 `docs/scheduled-review-2026-11-08-0408.md` |

**依赖白名单**（实测版本，`research/30`）：`hashicorp/go-plugin v1.8.0`、`tetratelabs/wazero v1.12.0`、`klauspost/compress v1.20.3`、`BurntSushi/toml v1.6.0`、`mvdan.cc/sh/v3 v3.14.1`（仅解析）、`charmbracelet/bubbletea v1.3.10`、`charmbracelet/lipgloss v1.1.0`、`modelcontextprotocol/go-sdk v1.8.0`、`golang.org/x/sync v0.24.0`、`coder/websocket v1.8.15`。新增依赖要写「为什么标准库不够」。

---

## 16. 风险与明确不做

| 风险 | 表现 | 缓解 |
|---|---|---|
| 行为对齐追不动 | 参考 dsh-rs：15–18 万行仍停更 | P0 只做 18 工具 + 1 provider；追同一任务成功率，不追功能面 |
| 会话双写不兼容 | Node 读不了 Go 写的帧 | 先写 golden test（字节级），再写编码器 |
| 工具体验退化 | 返工率上升 | 固定 10 个真实任务做 A/B（同模型同提示） |
| 生态反转 | 拿不到 JS 插件 | MCP 继承；不自建 JS 桥 |
| manifest 膨胀 | 装配图失控 | 一模块 = 一可独立替换的职责；默认实现收敛到 23 个 SPI |

**最初 P0 明确不做（历史范围）**：HTTP/WS 兼容层、桌面端对接、多 provider、team/workflow/schedule/goal/skill、`!!js`、cordis 式热重载、JS 兼容桥。后续 Transport 与 Viva 原生界面已成为现行路线；不得据此删除或阻止后续实现。长期仍不做多 provider、`!!js`、cordis 式热重载和宿主内 JS 桥。

---

## 17. 证据索引

| 路径 | 内容 |
|---|---|
| `HANDOFF.md` | 交接文件（实测账 + 已定 12 条 + 架构） |
| `evidence/node-binding-audit.md` | Node 绑定面审计（22 个原生二进制、API 密度、插件加载） |
| `evidence/dsh-vs-dsc-coverage.md` | 59 工具 ↔ dsc 21 插件覆盖对照 |
| `evidence/dsh-tool-names.txt` | 本机真实下发的工具名 |
| `research/10-dsc-reference.md` | dsc v3.0.0 全量解剖（396 行，带 `文件:行号`） |
| `research/11-dsc-reuse.md` | dsc 复用裁决（直接拿 5.8k / 要改 20.6k / 重写 11k） |
| `research/20-dsh-tool-schemas.json` | 61 个工具的真实 schema（54,936 字符 / 12,209 token） |
| `research/21-dsh-session-v4.md` | 会话 v4 物理格式 + 62 事件全表 + 派生规则 |
| `research/22-dsh-acp.md` | ACP 28 个 method + 15 个 update 变体 + 桥接链路 |
| `research/23-dsh-plugin-surface.md` | 96 条 bundle entry、104 个 service key、工具注册链、提示词装配 |
| `research/30-go-env-probe.md` | Go 依赖与工具链实测（版本/体积/冷启动/三个坑） |
| `refs/dsc/` | dsc 源码（只读参考，Apache-2.0，不 vendor） |
| `probe/` | Go 依赖验证工程（一次性，可删） |
