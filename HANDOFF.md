# DSH → Go 重写 · 交接文件

> 产出于 2026-11-02，`E:\Documents\deepseek-harness\default-workspace` 的分析会话。
> 本文自包含：新会话不需要旧上下文，读完这一份就能开工。

---

## 0. 一句话

**用 Go 重写 DeepSeek Harness 的宿主，保留"一切皆插件"的设计哲学，但插件边界从"运行时 import 一个 JS 包"改成"go-plugin/gRPC 进程 + WASM + 静态模块"三种形态；内核零业务逻辑；工具层为 LLM 设计，不为人类设计。**

目标形态：内存 ~150 MB（现在 870 MB），模型体验与 DSH 等价，桌面 TUI / WebUI 双客户端。

---

## 1. 动因：现在的账（实测）

### 1.1 内存（2026-11-02 13:0x，本机快照）

| 口径 | 数值 | 说明 |
|---|---:|---|
| 任务管理器"内存"列（私有工作集） | **870 MB** | 5 个 Electron 进程 |
| `tasklist` 工作集总量 | 1,471 MB | 共享 DLL 页被重复计数 |
| 其中 Chromium（main+GPU+net+renderer） | **~500 MB** | 换壳即可省掉 |
| 其中 Node 宿主 | **~367 MB** | 重写宿主才省得掉 |

### 1.2 宿主里那 367 MB 的构成

- Electron-as-Node 运行时底噪：空进程 RSS 113 MB（实测）
- 核心模块图（11 个核心包 + openai/anthropic/genai/zod/undici）：+74 MB
- 其余：整棵 cordis 插件树（官方 95 个 + 社区 13 个）+ 会话 + 缓存

### 1.3 重写能省多少（三种范围）

| 范围 | 内存 | 降幅 | token 成本 |
|---|---:|---:|---:|
| A. 只换壳（TUI/WebUI，宿主不动） | ~450–520 MB | −40~48% | 0（现成方案） |
| **B. TUI + Go 宿主** | **~90–150 MB** | **−83~90%** | 1,500–3,000 万 |
| C. 保留 Electron，只重写宿主 | ~800 MB | −8% | 8–17 M（最亏，别做） |

### 1.4 模型的真实开销（13.8 小时会话，3,002 次工具调用）

| 项 | 数值 |
|---|---:|
| 输出 token | 3.62 M |
| 输入 token（非缓存） | 38.8 M（输入:输出 ≈ 10.7:1） |
| 缓存读 | 2.16 B |
| 落地代码行 | 7,100 行（74 write + 42 edit） |
| **有效产出速率** | **262 k 输出 token / 小时** |

生成速度实测（`group/deepseek-v4.1-flash1017`）：**193 tok/s**（含思考）、245 tok/s（纯 content），TTFT 0.9–1.7 s。

### 1.5 工具调用与结果分布（关键设计输入）

| 工具 | 调用次数 | 占比 | 返回字符 | 平均 | 占全部结果 |
|---|---:|---:|---:|---:|---:|
| bash | 2,518 | 83.7% | 1,632,047 | 648 | **85.5%** |
| job_output | 261 | 8.7% | 58,274 | 223 | 3.1% |
| write | 74 | 2.5% | 9,307 | 126 | 0.5% |
| edit | 42 | 1.4% | 4,142 | 99 | 0.2% |
| read | 42 | 1.4% | 103,986 | 2,476 | 5.5% |
| web_fetch | 2 | 0.07% | 28,390 | 14,195 | 1.5% |
| **合计** | 3,002 | — | 1,907,792 | — | ≈ **530 k token** |

**结论：模型 85% 的上下文是 shell 回显。** 这是本次重构最重要的靶子。

### 1.6 工具表本身的成本

DSH 一次请求下发 **59 个工具 = 52,951 字符 = 11,808 token**（实测分词）。

最大：`bash` 1,182 tok、`workflow` 756、`hindsight_capture_initiative` 391；最小：`schedule_list` 31。
平均 896 字符 ≈ 200 token/工具。**工具表是缓存前缀，稳定性比大小更重要。**

### 1.7 DSH 的代码规模

| 子系统 | 包数 | 行数 | 占比 |
|---|---:|---:|---:|
| 客户端 UI（React 18.3.1） | 59 | 147,899 | 30% |
| 其它平台胶水 | 151 | 143,774 | 29% |
| API/RPC | 10 | 76,917 | 16% |
| 会话 | 28 | 43,757 | 9% |
| 工具 | 31 | 34,383 | 7% |
| agent 逻辑 | 18 | 29,165 | 6% |
| LLM 接入 | 8 | 12,693 | 3% |
| **合计（剔除两个 vendored 大包）** | 307 | **488,588** | — |

`dsh-base`（官方预装宿主侧插件树）= **95 个包 / 135,005 行**。
`dsh-web-app`（UI + RPC 胶水）= 133 个包 / 303,866 行。

### 1.8 版本 churn（决定"追更成本"）

- 发布节奏（npm 实测，共 40 个版本）：alpha 段 1–6 天/版；**rc 段 0.2–6 天/版**
- **0.2.1-rc.1 → rc.5（12 天 4 个版本）**：抽样 20 个包，**18 个字节级零变更**；变化的是 `dsh-api-remotes`（+118/−4）和 UI（+53/−13）
- **0.1.7 → 0.2.0（跨 minor）**：`dsh-tools` +0/−0、`dsh-agent` +0/−0、`dsh-typert-protocol` +0/−0，唯一变化 `dsh-session` **+279/−192**（会话格式 v3→v4）

**结论：宿主契约极稳，追更成本主要在 minor 边界，且主要炸插件和客户端。**

---

## 2. 已定决策（含被否决项）

| # | 决策 | 选择 | 理由 | 否决了什么 |
|---|---|---|---|---|
| 1 | 语言 | **Go 1.27** | 迭代速度（编译亚秒级）是这个项目的瓶颈；60 vs 110 MB 的差别在 870 MB 基线上是噪声；Windows 工具链简单 | Rust（编译 10–60s、借用检查返工烧 token、需 MSVC） |
| 2 | 插件边界 | **go-plugin + gRPC 为主** | 跨语言、进程隔离、可热重载；已有生产级实现（dsc） | cordis 式 `import()`（Go 里不存在）；Rust dylib ABI（生态空白） |
| 3 | 第二装载层 | **WASM（wazero）** | 纯 Go 无 CGO、沙箱、任何语言可写；适合 gokifu 这类纯逻辑模块 | — |
| 4 | 静态层 | **内核 + 默认实现编译进二进制** | 零启动开销、单文件分发 | 全部动态化（启动慢、内存高） |
| 5 | 内核 | **零业务逻辑**（只有事件总线 + 注册表 + 装配器） | 这是"一切皆插件"在编译语言里的等价物 | 大单体 |
| 6 | 强制手段 | `internal/kernel` 的 import 图禁止出现 `modules/`，用 `go list -deps` 挂 CI | 纪律靠工具不靠人 | 靠 code review |
| 7 | 工具层 | **结构化优先，shell 兜底** | 85.5% 的上下文是 shell 文本 | 照抄 dsc 的 shell 中心设计 |
| 8 | 会话格式 | **v4 兼容**（`session.v4.jsonl.zstd`，zstd 多帧追加） | 存量 89 MB 会话可续；可双向接手 | 自研格式 |
| 9 | 客户端协议 | **ACP 先行，HTTP/WS 后补** | ACP 是 stdio JSON-RPC，省 8–10k 行；Martty 可直接连 | 只做 `/api` + WS（要鉴权、注入、静态资源） |
| 10 | 热重载 | **快速重启（10–50 ms）** | 插件多数本就要重启；DSH 热重载出过两次真机事故 | cordis 式热重载 |
| 11 | `!!js` 配置表达式 | **不做** | 牺牲灵活性换确定性；用声明式 schema | `new Function` + `with(eval)` |
| 12 | 桌面壳 | **可保留，作为客户端一种** | 壳只做 spawn + loadURL + 6 个 IPC 消息 | 删掉 Electron（可选） |

---

## 3. 架构

```
┌──────────────── 内核（零业务逻辑）────────────────┐
│ eventbus（emit/serial/bail/waterfall/parallel）  │
│ registry（服务键 → 实现）                         │
│ assembler（读 manifest，装配模块）                │
└───────────────┬──────────────────────────────────┘
                │ SPI（trait 等价物：interface + 默认实现嵌入）
   ┌────────────┼─────────────┬──────────────────┐
静态模块       WASM 模块      进程模块
(编译进)      (wazero)       (go-plugin/gRPC)
默认实现      gokifu 等       第三方能力
```

### 3.1 三层各自的使用场景

| 层 | 换模块怎么做 | 生效速度 | 每模块内存 | 用在哪 |
|---|---|---|---|---|
| 静态 | 换 blank import + 重编译 | 1–3 s | 0 | 内核、官方默认实现 |
| WASM | 改 manifest，重启 | ~50 ms | 0.5–5 MB | 频繁替换的纯逻辑（压缩策略） |
| 进程 | 放下二进制，热加载 | 即时 | 10–20 MB | 工具类、外部集成、第三方 |

### 3.2 `extends BasicCompactionEngine` 在 Go 里的等价物

```go
// spi/compaction.go
type CompactionEngine interface {
    ShouldCompact(*SessionView) bool
    Compact(*SessionView) (Plan, error)
    FoldSpan(Span) (FoldResult, error)   // gokifu 只覆盖这个
    Budget() Budget
}

// modules/compaction/basic —— 默认实现（= BasicCompactionEngine）
type Basic struct{}
func (Basic) ShouldCompact(v *SessionView) bool { return v.Tokens > 80_000 }
func (Basic) Compact(v *SessionView) (Plan, error) { return DefaultSummary(v), nil }
func (Basic) FoldSpan(s Span) (FoldResult, error) { return TrivialFold(s), nil }
func (Basic) Budget() Budget { return Budget{Max: 128_000} }

// 用户的 gokifu：嵌入 Basic，只覆盖想改的
type Gokifu struct{ Basic }
func (g Gokifu) FoldSpan(s Span) (FoldResult, error) { ... }
```

**Go 的 struct 嵌入 ≈ JS 的 extends**，这是选 Go 的额外理由。

### 3.3 manifest（对应 `cordis.patch.yml`）

```toml
[[module]]
id       = "compaction"
provides = ["compaction"]
backend  = "wasm"          # static | wasm | process
path     = "modules/gokifu-compact.wasm"
[module.config]
budget    = 120000
fold_mode = "incremental"
```

---

## 4. 工具 SPI（这是本次重构的灵魂）

### 4.1 接口

```go
type Tool interface {
    Name() string
    Schema() json.RawMessage                       // 必须稳定，不含时间戳/路径
    Exec(ctx context.Context, args json.RawMessage) (Result, error)
}

type Result struct {
    Summary   string        // 一行，给人（TUI/WebUI 渲染用）
    Data      any           // 结构化，给模型
    Artifacts []Artifact    // 大结果落盘引用
    Truncated *Truncation   // {Kept, Total, Pointer}
    Hint      string        // 失败/为空时的下一步建议
}
```

### 4.2 三条硬约束（中间件统一实现，禁止各工具自己写）

1. **序列化层**：`Data` → JSON，键名统一，禁止自由文本
2. **截断层**：超阈值的 `Data` 自动外置为 artifact + 指针（DSH 的 spill / dsc 的 spill policy）
3. **提示层**：`error` 必须带 `Hint`，CI 检查

### 4.3 八条公理（每条有实测依据）

| # | 公理 | 依据 |
|---|---|---|
| 1 | 工具输出必须结构化 | 85.5% 上下文是 shell 文本 |
| 2 | 每个工具输出必须有界 | bash 平均 648 字符（限流生效）；`web_fetch` 单次 14,195（该拆） |
| 3 | 结构化优先，shell 兜底 | `read`/`glob`/`grep` 的 token/信息比远高于 `ls`/`cat` |
| 4 | 工具表必须稳定 | 11.8k token/请求走缓存；动态增删会击穿 72 万 token 前缀 |
| 5 | 错误信息是提示词 | 模型下一句怎么改，取决于错误里写了什么 |
| 6 | 确定性输出 | 反斜杠、绝对路径、随机 id 会污染上下文 |
| 7 | 少而稳的 schema 优于多功能军刀 | 59 个工具 = 11.8k token，每加一个都有固定成本 |
| 8 | 模型不该看见用不上的东西 | DSH 的 `skill` 只有 74 token 就是在做这个 |

---

## 5. 先例盘点：谁做过，做到哪

| 项目 | 语言 | 星 | 状态 | 定位 | 可借鉴 |
|---|---|---:|---|---|---|
| **[naamfung/dsc](https://github.com/naamfung/dsc)** | Go | 18 | 活跃（10-31） | **最接近的目标形态**：go-plugin+gRPC、五类插件热重载、`docs/cordis-alignment.md` 把 cordis 概念逐条映射成 Go | **首选参考**：目录结构、cordis 对照表、`proto/dsc.proto` 插件 ABI、21 个插件样本 |
| **[JopenChen/dsh-go](https://github.com/JopenChen/dsh-go)** | Go | 15 | 停更（9-12） | 纯进程内参考实现，逐词对译 DSH 能力接缝；1.2 MB Go ≈ 3.5–4 万行 | `pkg/` 包名就是能力清单（session/agent/plan/goal/todo/skills/tools/llm） |
| **[openma-ai/Martty](https://github.com/openma-ai/Martty)** | Rust | 81 | 活跃 | ACP 客户端 TUI，可直连 DSH | **P0 的验收客户端**：你的 ACP 服务端能被它连上就算过 |
| **[ccch1mneyyy/dsh-TUI](https://github.com/ccch1mneyyy/dsh-TUI)** | TS | — | 活跃 | 终端 UI 插件（npm `@deepseek-harness-tui/dsh-tui` 0.18.0） | TUI 交互设计；89.5k 行 JS（自写约 63k） |
| **[ccch1mneyyy/dsh-Go](https://github.com/ccch1mneyyy/dsh-Go)** | **Dart** | 5,030 | 活跃 | **手机端**（不是 Golang，名字误导） | 移动端产品形态参考 |
| **[xizheyin/deepseek-harness-rs](https://github.com/xizheyin/deepseek-harness-rs)** | Rust | 147 | **停更（8-29）** | 独立重写 agent，不是 DSH 兼容宿主 | **反面教材**：追"行为"追不动，证明兼容派最难 |

### 5.1 dsc 的能力覆盖（对 DSH 官方 29 个工具）

**19 直接等价 / 5 形态不同 / 5 缺失**。详见 `evidence/dsh-vs-dsc-coverage.md`。

关键缺口：`read`/`write`/`edit`/`glob`/`grep` 被合并进 `shell` + `str_replace_editor`（**这是反模式，按第 4 节公理应反过来**）。

---

## 6. 环境（已装好，可直接用）

| 项 | 值 |
|---|---|
| Go | **1.27.3**，`C:\Program Files\Go\bin\go.exe`（本会话未入 PATH，需手动加或用全路径） |
| GOPATH / GOMODCACHE | `C:\Users\Administrator\go` / `...\go\pkg\mod` |
| GOPROXY | `https://goproxy.cn,direct`（实测 200） |
| 已验证依赖 | `tetratelabs/wazero v1.12.0`、`klauspost/compress v1.20.3`（zstd）、`modelcontextprotocol/go-sdk v1.8.0`、`coder/websocket v1.8.15`、`BurntSushi/toml v1.6.0` |
| 本机 DSH | `D:\DeepSeek Harness\Desktop\resources\app.asar`（182 MB，asar 内 `dsh/node_modules/@deepseek-ai/*`，307 包） |
| 会话样本 | `D:\DeepSeek Harness\home\sessions\...`（89 MB，zstd 多帧） |
| 模型网关 | `http://127.0.0.1:3425`（magpie，**Go + Wails 写的**，OpenAI 兼容） |

> 读 asar 内的文件：bash 直接读不了（Not a directory）。用 `ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" script.mjs`，脚本里 `fs` 可正常读 asar 路径。

---

## 7. P0：范围与验收

### 7.1 范围（25k–30k 行 Go，150–300 万 token ≈ 6–11 小时机器时间 + 联调）

| 模块 | 行数 | 说明 |
|---|---:|---|
| agent loop + 流式 | 3–5k | 核心，不可省 |
| 5 个工具（shell/read/write/edit/job） | 4–6k | 覆盖 97.6% 调用 |
| 会话存储（v4 + zstd 多帧） | 3–4k | 必须双向兼容 |
| ACP 服务端（stdio JSON-RPC） | 4–6k | 入口协议 |
| LLM 客户端（OpenAI 兼容 SSE） | 2–3k | 走 magpie |
| 审批/沙箱/配置 | 3–5k | 可先简化 |
| MCP 客户端 | 1–2k | sidecar 插槽 |

### 7.2 验收（缺一不可）

1. **连得上**：Martty 能连上并对话，流式收到 reasoning
2. **工具一致**：`shell`/`read`/`write`/`edit`/`job_*` 行为、审批、沙箱与 DSH 一致
3. **会话互通**：Node 版写的 v4 会话能续写；Go 版写的 Node 能读
4. **压缩连续**：gokifu 的 fold 语义不变，跨压缩的失败记忆还在
5. **资源达标**：`tasklist` 私有工作集 < 150 MB，冷启动 < 100 ms，2,500 次 shell 调用无进程泄漏

### 7.3 明确不做（P0）

- HTTP/WS 兼容层（P2）
- 桌面壳对接（P2）
- 多 provider（先只做 OpenAI 兼容 → magpie）
- team/goal/schedule 全量（P4）

---

## 8. 建议目录骨架

```
cmd/dshd/                  main：只做装配
internal/kernel/           事件总线、注册表、装配器（禁止 import modules/）
internal/spi/              全部接口（compaction/tool/provider/store/transport）
internal/session/          事件溯源 + v4 格式 + zstd
internal/agent/            turn/step 双循环
internal/acp/              ACP 服务端
internal/llm/              OpenAI 兼容 SSE 客户端
modules/                   静态默认实现（compaction/basic、tools/*）
wasmhost/                  wazero 装载器
plugins/                   进程插件（go-plugin/gRPC）
docs/cordis-alignment.md   照 dsc 那份写，作为设计约束
dsh.toml                   manifest
```

**第一条 CI 规则**：`go list -deps ./internal/kernel | grep modules/` 必须为空。

---

## 9. 风险与开放问题

| 风险 | 说明 | 缓解 |
|---|---|---|
| 行为对齐 > 接口对齐 | dsh-rs 写了 15–18 万行仍停更 | P0 只做 25k 行，先跑通再说 |
| minor 版本订阅费 | 每 2–3 周一次，15–25 万 token/次 | 锁定 0.2.1，只在 minor 升级时做增量移植 |
| 生态反转 | 自建后拿不到新的 JS 插件 | 用 MCP 继承现成生态 |
| gokifu 无法外置 | 它寄生在会话事件流上 | 必须原生化，约 2–3k 行 |

**开放问题（开工前要拍板）**：

1. **fork dsc 还是自研？** 建议先跑 2 小时 dsc，用 gokifu 场景试它；够用就 fork，不够再自研。
2. **P0 先做 ACP 还是先做工具层？** 建议 ACP（否则没有可运行的验证回路）。
3. **桌面壳要不要一开始就接？** 建议不要，P2 再说。
4. **hindsight 13 个工具怎么办？** 建议 MCP sidecar（Go SDK 已就位）。
5. **team 9 个工具？** dsc 有 `core/agent_team.go`，先实测再定。

---

## 10. 第一个小时的行动清单

```bash
# 1. 环境
export PATH="/c/Program Files/Go/bin:$PATH"
go version        # 期望 go1.27.3

# 2. 拉 dsc 当参考（GitHub 直连慢，用 API 或等网络好时）
#    重点读：docs/cordis-alignment.md、proto/dsc.proto、plugins/tool-filesystem/main.go、core/eventbus.go

# 3. 建骨架
mkdir dsh-go && cd dsh-go
go mod init dsh
mkdir -p cmd/dshd internal/{kernel,spi,session,agent,acp,llm} modules wasmhost plugins docs

# 4. 先写 spi/tool.go + spi/compaction.go（不要先写 agent loop）

# 5. 第一个可运行的里程碑：ACP echo
#    Martty 连上来 → 回一句 "ok" → 就算 P0 的 5%
```

---

## 11. 证据文件索引

| 文件 | 内容 |
|---|---|
| `evidence/node-binding-audit.md` | DSH 的 Node 绑定面审计：22 个原生二进制、API 密度、插件加载机制、UI 技术栈（20 KB） |
| `evidence/dsh-vs-dsc-coverage.md` | 59 个 DSH 工具 ↔ dsc 21 个插件的能力覆盖对照 |
| `evidence/dsh-tool-names.txt` | 本机真实请求下发的 59 个工具名（设计基线） |

> 原始测量脚本留在旧工作区 `E:\Documents\deepseek-harness\default-workspace\tmp\`（asar-scan2.mjs / loc-buckets.mjs / mem-parse.mjs / mem-modules.mjs / tool-tokens2.mjs / tokps2.mjs），新工作区若不需要复测可以不带。

---

## 12. 一句话交接

> **别从 agent loop 开始写，从 `spi/tool.go` 开始。**
> 决定这个产品好不好用的不是循环，是工具给模型返回了什么——而实测告诉我们，现在 85.5% 的返回是 shell 回显。
