# PLAN · dsh-go：用 Go 重写 DSH

> **版本** v1.0（2026-11-02）· 十项方向已与用户逐条拍板
> **上游基线** DSH `0.2.1-rc.5` · **本仓版本** `0.2.1-go.5`（见 §13）
> **证据层**：`HANDOFF.md`（实测账）、`evidence/`（三份审计）、`research/`（本轮六份调研）、`refs/dsc`（只读参考）
> **执行规则**：见 `AGENTS.md` —— 本文件只讲**为什么**，AGENTS 讲**怎么做**

---

## 0. 一句话与成功判据

**用 Go 重写 DSH 宿主：内核只做插件运行时，任何业务能力——包括 agent loop、工具、会话、传输、UI——都由 manifest 装配、都能被换掉；工具层为模型设计，不为 shell 设计。**

| 指标 | 现状（实测） | 目标 | 验证方式 |
|---|---:|---:|---|
| 常驻私有工作集 | 870 MB | **< 150 MB** | `tasklist` 私有工作集 |
| 冷启动 | 数秒 | **< 100 ms** | 进程起止计时（Go 空进程实测 median 18 ms） |
| 空二进制体积 | — | **< 25 MB** | `go build -ldflags "-s -w"` |
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
| 10 | 追更 | **分支追更**：版本号对齐上游——`0.2.1-rc.5` → **`0.2.1-go.5`** | 每次跟进 = 一个明确的上游基线 |

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

1. **抽掉实现仍能编译**：删 `modules/` 后 `go build ./internal/...` 通过。
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

```go
type Tool interface {
    Name() string
    Schema() json.RawMessage                       // 必须稳定：无时间戳/绝对路径/随机 id
    Exec(ctx context.Context, args json.RawMessage) (Result, error)
}

type Result struct {
    Summary   string        // 一行，给人
    Data      any           // 结构化，给模型（JSON）
    Artifacts []Artifact    // 大结果外置引用
    Truncated *Truncation   // {Kept, Total, Pointer}
    Hint      string        // 失败/为空时的下一步
    View      *ViewSpec     // card / table / plain，客户端渲染
}
```

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

### 7.4 P0 工具集：18 个，目标 ≤ 6k token

| 组 | 工具 |
|---|---|
| 执行 | `shell`（持久会话 + 活跃续命 + 自动转后台 + spill） |
| 任务 | `job_output` `job_kill` `job_list` |
| 文件 | `read` `write` `edit` `glob` `grep`（**独立工具，不合并进 shell**；共享观测策略：改前必须先读） |
| 感知 | `read_image` `web_fetch` `web_search` |
| 组织 | `todo_write` `present` `ask_user_question` `working_directory` |
| 派生 | `subagent` |
| 考古 | `history_search`（读旧会话/旧记忆，软着陆的关键工具） |

**不做（P0）**：`workflow`、`team_*`、`schedule_*`、`goal`、`skill`、`github`、`repo_here`、`gitbash_*`、`hindsight_*`（后者走 MCP）。

**shell 的执行模型（重要）**：**用真实 shell 进程**（本机 Git Bash），**不要**用 `mvdan.cc/sh` 在进程内解释。实测 Windows 上 mvdan 的进程替换是静默桩（`cat <(echo x)` 输出字面量 `TODO: support process substitution on Windows`）、POSIX 路径不映射（`cd /tmp` 失败）、外部命令全是 `exit status 127`。mvdan 只用于**静态分析**（判定命令是否写盘，喂给沙箱），不用于执行。

---

## 8. 提示词与指令装配

| 机制 | 规则 |
|---|---|
| 装配 | section 带 `order` 升序拼接；`complete:true` 的 section 只能有 1 个；变量 `{{name}}` 可选插值 |
| 工具目录 | 自动流入；`toolOrder` 必须恰好含一个 `<unlisted-tools>` 占位；列了不存在的工具名 → 每次装配都拒 |
| AGENTS.md | 注入顺序 **由宽到窄**：`$DSH_HOME/AGENTS.md` → `~/.agents/AGENTS.md` → 项目根 → cwd；每层可有 `.local` 覆盖层 |
| 注入形态 | **一条耐久基线消息**（不是每轮重发）；trim 后内容相同的文件只渲染一次 |
| 预算 | `maxBytes` 65536；降级顺序「先丢更宽的，最后才截断最具体的」 |

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
| **P2** | Go 桌面端 | 选型 spike（Wails v3 / Fyne / Gio）后再定；**不使用 Electron** |

客户端只能经 `Transport` + `ViewSpec` 接触内核，所以桌面端选型不阻塞内核。

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

- 版本号 = 上游版本 + `-go.N`：`0.2.1-rc.5` → **`0.2.1-go.5`**。
- `CHANGELOG.md` 维护映射表：`go/0.2.1-go.5 ← rc.5`，列**移植了什么、跳过什么、为什么**。
- 跟进方式：新上游版本 → 新分支 → 只搬「契约变化」（会话格式、工具语义、ACP 方法）+ 我们要的功能；**不搬**客户端 UI 与 provider 生态。
- 先例（`HANDOFF.md` §1.8）：`rc.1 → rc.5` 抽样 20 包 18 个字节级零变更；`0.1.7 → 0.2.0` 只有 `dsh-session` 变了。**追更成本主要在 minor 边界。**

---

## 14. 里程碑

| # | 交付 | 验收（跑得出来） | 规模 |
|---|---|---|---:|
| **M0** | 内核 + 装配器 + manifest + 模块图 | `dshd modules graph` 打出依赖树；删 `modules/` 仍能 `go build ./internal/...`；禁用任意模块报「缺 X」 | 2–3k |
| **M1** | 工具 SPI + 18 工具 + 三条中间件 | 一致性套件全绿；越界写被拒；spill 后能取回；`Result → content[]` 投影有 golden test | 6–8k |
| **M2** | LLM(SSE) + agent loop + 会话 v4 codec | `dshd run "…"` 跑通；**Node 版能读 Go 写的会话**；两边派生的 messages 逐条相同 | 6–8k |
| **M3** | ACP 服务端 | **Martty 连上并对话** | 4–5k |
| **M4** | TUI + 持久 shell + 后台任务 | 用 TUI 完成一次真实任务；内存 < 150 MB | 4–5k |
| **M5** | 压缩协议 + token meter + 模式系统 + `history_search` | gokifu 以 wasm 接入，fold 语义不变；内置模式不可改 | 4–6k |

**顺序不可换**：M0 → M1 → M2 → M3。先 `spi/tool.go`，不要先写 loop（`HANDOFF.md` §12）。

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
2. 删 `modules/` 后 `go build ./internal/...` 通过。
3. 每个 `Tool.Exec` 的 error 路径必须给 `Hint`（AST 扫描）。
4. `go vet` + `staticcheck` + `-race` 全绿。
5. 工具 schema 快照：变更必须显式更新 `testdata/tools.snapshot.json`（防缓存击穿）。
6. 工具表 token 总量 ≤ 6000（分词实测，不估算）。
7. `dshd` 二进制体积 ≤ 25 MB（`-s -w`）。

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

**P0 明确不做**：HTTP/WS 兼容层、桌面端对接、多 provider、team/workflow/schedule/goal/skill、`!!js`、cordis 式热重载、JS 兼容桥。

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
