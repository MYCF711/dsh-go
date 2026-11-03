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

## 0. 当前状态

> 里程碑与踩坑史已拆到 **`docs/history.md`**（原来这一段有 55 KB，把手册顶到 78 KB、
> 超出工作区指令预算 65 KB——**读不完的手册等于没有手册**）。
> 要看"某一轮做了什么、踩过什么坑"，翻那份；这里只留**现在还成立的结论**。

### 宿主线

- ✅ M0–M3 完成（内核 / 工具 SPI / LLM+会话 codec / ACP），Martty 真客户端验过
- ✅ P0 工具 18/18，现在共 **44 个工具**，覆盖真实语料调用量 **99.7%**
- ✅ 压缩协议 + 自动压缩 + 模式系统 + `history_search` + fork
- ✅ 关掉三件事：`-race`（本机无 C 编译器，改为 `scripts/race-guard.py` 结构性补偿）、
  内存增长（实测是分配器保留不是泄漏）、`subagent_fork`（做了，暗号验证 + 窗口边界都量过）
- ✅ 工具表 44 个 / 23172 字节 / **估算 3892 token**（上限 6000）

### 界面线

- ✅ **`internal/transport` 的 U0 13 个 method 全部实现**（会话 8 + 工作区 1 + 文件 2 + 配置 2），
  WS + `/connect` + 心跳 + 重连水位语义齐全，端到端 `scripts/ui-e2e.py` **14/14**
- ⏳ U1（前端工程）在 `dsh-ui` 仓库，本仓只管契约

### 门禁

`bash scripts/check.sh` 现在 11 项：内核零依赖 / 消模块可编译 / 工具 Hint 全扫描 /
staticcheck(SA*,S1*) / 工具表体积与 token 预算 / 安全默认值 3 条 / 权限分支守卫 /
放宽守卫 / 变异 20 条 / race-guard（并发点登记）/ 二进制体积。

**CI 七条里 5 条严格过关，2 条是代理**（第 6 条估算器 -6%，第 4 条缺 `-race`），
明细在 `docs/ci-rules.md`。

### 界面线现状（2026-11-03 复核）

- ✅ 调研三份已成稿：`research/40`（壳解剖）、`research/41`（选型）、`research/42`（前端资产）
- ✅ `UIPLAN.md` §2.10 **已拍板**：押注 Go 原生（Viva）、仓库独立成 `dsh-ui`
- ✅ **本仓 `internal/transport` 的 U0 已交付**：13 个 method 全部实现 + WS + `/connect`
  + 心跳 + 重连水位，端到端 `scripts/ui-e2e.py` 14/14
- ⏳ U1（前端工程）在 `dsh-ui` 仓库，本仓只管契约
- ⚠️ 上面「两条线」表里那句「界面线：只有规划，一行代码都没有」是**旧话**，
  现在宿主侧这一半已经有了（13 个 method）；界面那一半仍不在本仓

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
- **文档**：`docs/` 下的设计文档要能被人名指出来——**引用一个不存在的文件比没有引用更糟**（`scripts/doc-links.py` 每次都会查）。

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

---

## 12. 界面线 → 宿主线的需求（**唯一通道**）

界面线（`dsh-ui` 仓库，原生 Viva 版）需要宿主侧多做一步的东西，**一律写进下面这张表**，
不要只在对话里口头提——宿主线的会话读的是这份 AGENTS.md，看不到另一个会话的聊天记录。

**约定（两边都要守）**

| 谁 | 做什么 |
|---|---|
| 界面线 | 把需求加进本表：写清**要什么字段/语义**、**为什么界面需要它**、**怎么验**。已经有替代做法的写清替代方案 |
| 宿主线 | 做完把状态改成 ✅，在 `CHANGELOG.md` 记一行（版本号 + 一句话）；没做就保持 ⏳ 并写卡点（卡在哪、需要谁拍板） |
| 界面线 | 表里还是 ⏳ 的，界面侧**显示「未知 / 整段不显示」，绝不编数据**——假数字比没有更糟，这条已经写进界面线的契约文档 |
| 两边 | 改契约（字段名、形状）先改 `testdata/api.snapshot.json`，再改代码（对齐 R4） |

**当前需求**

| # | 状态 | 要什么 | 为什么界面需要 | 怎么验 |
|---|---|---|---|---|
| H1 | ✅ 2026-11-03 | `session.load` 的每条 `assistant/message` 带 `usage{inputTokens,outputTokens,totalTokens,cacheReadTokens}`，响应顶层再给一份合计 | 底部状态栏要显示 `↑输入 ↓输出 · tok/s · 上下文`。现在界面只能从**磁盘日志**里解，真宿主接上后没这个字段就只能退回只显示轮数/步数 | `curl -XPOST /api/session.load` 返回的 usage 里 `outputTokens > 0` |
| H2 | ✅ 2026-11-03 | 事件的墙钟时间 `time`（epoch 毫秒），每条事件都要有 | `tok/s` = outputTokens ÷ 相邻两条 assistant 事件的时间差。没有 `time` 就算不出速率（宿主从不提供耗时） | 同会话相邻两条 `assistant/message` 的 `time` 单调不减；差值 ∈ (0,120]s 时能算出 >0 的速率 |
| H3 | ⏳ | `session.load` 的 `messages[].view`：把工具的 `spi.ViewSpec` 原样透传（`kind` = card/table/plain + 各自字段） | 界面按 `kind` 选渲染器（卡片/表格/原文）。没有它，工具卡只能是「工具名 + 一行摘要」 | `bash` → `kind:"card"` 且有 `fields`；`grep`/`ls`/`glob` → `kind:"table"` 且有 `columns`/`rows`；`read`/`write` → `kind:"plain"` 且有 `body` |
| H4 | ⏳ | `session.load`（或 `session.list`）带当前会话的 `model`（模型 id 字符串） | 顶栏要显示「这是哪个模型在答」。没有就本该显示模型的位置整段不画 | 有值时顶栏出现模型名；空值时不出错、不显示占位符 |
| H5 | ⏳ | 当前权限状态（permission preset 名，例如 `default` / `readonly`） | 顶栏要一个权限徽标。**不许拿 `mode` 冒充权限**：那是两件事 | 返回一个非空 preset 名；界面按名字显示，未知名字显示原文不猜含义 |
| H6 | 🟡 已修一半（2026-11-03） | WS `/api/mux` 的助手增量里保留 `reasoning` 块 | 真实日志 reasoning 9960 : text 1140，思考比正文多八倍 | **已修**：follow 在 `turn/done` 一到就 `return`，最后那帧 `assistant/message` 从来进不了流（实测：会话文件有 seq=3 `assistant/message`，流里没有）。补了收尾补读后，流里**已经能收到 `assistant/message`**。**仍未验证 `reasoning`**：本次会话写下的 assistant 事件里没找到 reasoning 块，需在真实 DSH 语料或会思考的模型上再验一次 |
| H7 | ✅ 2026-11-03（判据已澄清） | `assistant/message` 的 content 落法统一（或两种都兼容） | 只读一处会让整个会话的助手消息一条都不显示 | **真实语料实测**：6 个文件 822 条 `assistant/message`，顶层键是 `message/step/stream/turn/usage`，**内容在 `data.message.content`**（块类型 reasoning:712 / text:49 / tool-call:912）。`textOf` 已改成**两种落法都认**并有单测（4 种组合）。真机 20 个会话：**18 个 assistant 数 > 0**，另 2 个是 `user=0、总事件 3` 的**空会话**（没发过言，本就该 0）——所以判据应为「**有 user/message 的会话，assistant 数必须 > 0**」，按此 18/18 全过 |
| H8 | ✅ 2026-11-03 | 统一响应信封：成功 `{result:{…}}`、失败 `{error:{code,message,hint,retryable}}` | 照约定写的第三方客户端会两个都读不到 | 已在 `writeJSON` 一处收口（成功自动包 `result`；错误改成结构化对象并带稳定 `code`），真机验证：`session.list`/`load`/`create` 成功与 401 各一次形状正确 |
| H9 | ✅ 2026-11-03 | 入参名统一：`session.load` 的 `id` / `tailMessages`、`session.prompt` 的 `id` / `text`。**现状**：宿主读 `sessionId` / `limit` | 名字对不上就是静默 400/空结果。界面已经两个名字都发，但这意味着**契约有两份真相**，早晚出事 | `curl` 用快照里的名字调 `session.load`，参数生效（不再缺参） |
| H10 | 🟡 宿主侧：加了排查开关（见下） | `session.load` 要能加载 `session.list` 返回的任何 id | 界面点进去 404 = 接不上真宿主 | **宿主侧 2026-11-03 复现（把真实 home 的整个项目目录复制到 `tmp/h10c` 再 serve）**：`session.list` → 首个 id `session-c0a5847a-…` → `session.load` **200**。同一条命令在上一版二进制上对 `{"id":…}` 是 404，那是入参名（M228 已修）。**现在请用带排查开关的方式再跑一次**：`DSHD_DEBUG_RESOLVE=1 ./dshd.exe serve --home <你的 home> 2>resolve.log`，然后 load 一次，把 `resolve[start]` / `resolve[glob]` / `resolve[miss]` 三行贴回来——它会打印 `home` / `id` / `segs` / `cwd` / `matches`，一眼能看出是 home 不对、seg 不对还是 glob 没命中 |
| H11 | ✅ 2026-11-03 | `dshd serve` 的 **stdout 只放那一行 `ready {...}`**。**现状**：紧接着还打了一行 `policy deny（无人值守…）` | 壳（或任何客户端）按行解析握手时，第二行会被当成协议内容。`serve.go` 自己的注释就写着"stdout 只放那一行 ready"，实现和注释不一致 | `dshd serve … 2>/dev/null | head -1` 必须是纯 JSON 的 `ready {...}` |
| H12 | ⏳ | 给界面一份**会话内容列宽**（`ConversationWidth`），或让 `session.load` 带 `contentWidth` | 官方气泡的宽度上限是 `min(748px*0.702, 82%)`（`.userStack`），而界面在槽位里只拿得到 `f.Bounds()`（整窗 1180）和壳算的 `MainWidth`（含左右 padding）。照抄官方比例会把气泡推得过宽/过窄——**这是"外观原生"唯一还差的一个数字** | 界面按它设气泡上限后，与官方截图同宽同向 |

> H1–H7 的界面侧代码都已经写好并跑通（数据来源暂时是 `dsh-ui` 自己的 `cmd/mockd` 解真实日志）。
> 宿主侧一旦提供，界面侧只需把数据源从 mockd 换到 `internal/transport`，**渲染代码一行不用改**。
