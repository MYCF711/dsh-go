# workflow-ptc：程序化工具调用 · 调研与设计

> 状态：**设计稿，未写实现代码**（本轮只读调研；仓库里没有 `.go` 改动）。
> 日期：2026-11-04。基线：当前工作树（`git status`：14 个已改 + 9 个未跟踪，均与本题无关）。
> 上游对应物：`workflow-ptc` + `tool-workflow`（`docs/official-parity.md:30`）。本仓现状：**完全没有**，`docs/official-parity.md:50` 的判断是"撬动最大，但需要一套脚本宿主的设计，不能顺手做"。

---

## 结论先行

**做，但只先做一半：先落"子调用必须重新穿过 `spi.Runner`"这条通道 + 输出预算 + 失败 trace + 闸门红测，脚本语言先用零依赖的声明式 JSON 步骤表；JS 不做（R6），Starlark/子进程留到通道被真机证明之后。**

一句话理由：会出事的从来不是"脚本语言"这一层，而是"脚本里的调用怎么重新过审批、计划模式、预算"。语言换起来便宜，通道换起来贵。

---

## 1. 脚本在哪跑

### 1.1 四种形态的代价

| 形态 | 取消/超时 | 崩溃隔离 | 新依赖 | 与 R6 冲突 | 结论 |
|---|---|---|---|---|---|
| **同进程 + 受限解释器**（声明式 JSON 求值器 / Starlark） | 靠 ctx；**Pipeline 自身没有任何超时** | 无（脚本 bug = 宿主 bug） | 0（自写）或 1（Starlark） | 否 | **选它** |
| 子进程（node/python/自写 `dshd ptc`） | 可 kill，最彻底 | 有 | 需要目标机有运行时 | 否（但审批要跨进程） | 阶段 3 候选 |
| wasm | 可限内存 | 最强 | 需要 wasm 运行时 + 把脚本编译成 wasm | 间接 | 否 |
| 同进程 JS（goja 等） | 同上 | 无 | 1 | **是** | 否 |

### 1.2 同进程的真实代价（有代码证据）

- **没有超时兜底**。`Pipeline.Run` 从头到尾没有设 deadline（`modules/tools/pipeline.go:63-146`），ctx 一路从 loop 传下来（`internal/agent/loop.go:245`）。loop 里唯一显式的取消检查在每步开头（`internal/agent/loop.go:141-143`）——也就是说，**工具一旦卡住，整个 turn 就卡住**，只有客户端取消能救。所以脚本求值器必须自带**步数上限 + 时间上限**，不能指望外层。
- **取消语义会被改写**，这一点必须提前设计：`runTool2` 在拿到 `context.Canceled` 时会把模型看到的文案换成固定的"结果未知，你自己决定要不要重试"（`internal/agent/loop.go:257-261`），**并覆盖工具自己给的 Hint**。所以脚本超时/被取消时，已跑了几步这个事实只能写进 `err.Error()`，写进 Hint 会被丢掉。
- **cwd 是进程级的**：工具（read/write/glob/grep）按进程 cwd 解析相对路径，subagent 为此用了"临时 `os.Chdir` + 一把全局锁"（`cmd/dshd/subagent.go:56-81`）。同进程脚本如果并发执行、或者调用 `working_directory`，会和 subagent 抢这把锁。

### 1.3 子进程的真实代价

本仓已经有两条子进程先例，成本是有数的：

- `serve` 每轮起一个 `dshd run` 子进程（`cmd/dshd/serve_tools.go:17-25`、`:44-60`）——所以"脚本再起子进程"会变成三层。
- `exec_literal` 直接 `exec.CommandContext` 起任意 .exe，没有 shell，输出上限 200000 字节、超时 60s/上限 600s（`modules/tool/execlit/execlit.go:45-51`）。
- 审批能跨进程：`@@APPROVAL@@` 行标记写在 stderr、应答从 stdin 读回（`cmd/dshd/approval_stdio.go:26-27`、`docs/h13-approval.md:76`）。所以"子进程里的工具调用要问人"是**已经解决过的问题**。

代价是：子调用回宿主需要一条新的 RPC 协议（Windows 上要么命名管道要么 stdio 分帧）、孤儿进程回收、以及既有的 `@@APPROVAL@@` 通道被第三层复用后的歧义。

### 1.4 wasm

`go.mod:5-9` 只有两个依赖（zstd、toml）。wasm 路线要再引一个纯 Go 运行时（wazero），并且**模型写的脚本得先能编译成 wasm**——本机没有 TinyGo，也没有 JS→wasm 的引擎。等于把"脚本宿主"换成了"编译器 + 运行时"，比功能本身还大。

### 1.5 硬约束

`AGENTS.md:24`（R6）："不嵌 JS 运行时"。`PLAN.md:440` 更明确："长期仍不做多 provider、`!!js`、cordis 式热重载和**宿主内 JS 桥**"。

→ **同进程 JS 直接出局。** 与官方 `workflow-ptc` 用 JS 的形态不同，这一点必须在 `docs/tool-parity.md` 里写明"我们不做 JS 脚本"，不要假装一样。

---

## 2. 能调哪些工具 / Caps 怎么传 / 审批与计划模式

### 2.1 现状：三道闸门分别挂在哪

| 闸门 | 挂载点 | 判据 | 证据 |
|---|---|---|---|
| 审批 | `guardedRunner.Run`，**在 kernel 外** | `Caps().NeedsApproval()`（写/执行/联网/破坏性） | `cmd/dshd/acp.go:255-283`、`internal/spi/tool.go:43-45` |
| 计划模式 | 总线 `tools/pre-execute` waterfall，**在 kernel 内** | `Caps` 的 Writes/Executes/Destructive（**不看 Network**） | `modules/tool/plan/plan.go:80-94`、注册在 `:194-196`、被 `modules/tools/pipeline.go:84-88` 调起 |
| 截断/外置 | `Pipeline.spill`，跟着 `Run` 走 | 4000 字符 | `modules/tools/pipeline.go:159-219`、`:23`、`:30-33` |

唯一调用 `Tool.Exec` 的地方是 `modules/tools/pipeline.go:115`；`spi.Runner` 被定义为"唯一允许执行工具的入口"（`internal/spi/tool.go:128-132`）。所以结论很简单：**子调用只要走 `spi.Runner.Run`，三道闸门自动全部生效，一行拦截逻辑都不用重写。**

### 2.2 致命细节：kernel 里 Provide 的那个 Runner 没有审批

链条如下：

1. `modules/tools/registry.go:98-104` 提供 `toolrunner` 服务，实现是**裸 `Pipeline`**——`NewPipeline(reg, s.Bus(), opts)`，不含任何审批。
2. `cmd/dshd/main.go:409-411`、`cmd/dshd/acp.go:441-443` 把那个裸 Pipeline `Inject` 出来。
3. 审批包装 `guardedRunner` 是**在 kernel 外面**手搭的：`cmd/dshd/main.go:473-477`、`cmd/dshd/acp.go:195-202`。
4. 只有 `agent.Loop`（`main.go:493`、`acp.go:207`）和 `subRunner`（`main.go:485-488`）拿到的是包装后的那个。

**推论：任何模块只要 `kernel.Inject(s, spi.ToolRunnerKey)`，拿到的是没有审批的执行器。** 计划模式的闸门还在（那是总线上的，`plan.go:194`），spill 也还在，**只有"问人"这一层静默消失**。

目前没有任何模块 Inject 这个 key（全仓只有 `cmd/dshd/{main,acp}.go` 用它），所以这是一个**还没被踩到的坑**，不是既有 bug。但 workflow 模块最容易踩它——因为它天然需要"再执行一次工具"。

→ **workflow 不许 Inject `ToolRunnerKey`。** 见 2.4。

### 2.3 工具可见性只是"模型看不见"，不是"调不到"

- 模型看到的工具表来自 `modes.Filter(reg, mode)`（`cmd/dshd/acp.go:206`），过滤实现是 `internal/modes/view.go:30-48`。
- 但执行侧从不查这个视图：`guardedRunner` 拿的是全量 `r.reg`（`acp.go:198`），`Pipeline` 也是全量 registry（`pipeline.go:104`）。
- `dshd run` 连过滤都没有：`Tools: reg`（`cmd/dshd/main.go:489-501`）。

→ read-only 模式里模型如果**瞎编**一个被隐藏的工具名，仍然会真的执行。这不是 workflow 引入的问题，但 workflow 会把它放大成"脚本能调到模型根本看不见的工具"。

→ 所以 **workflow 必须自己再拦一层**：子调用名必须在"当前模式可见集合"里。

### 2.4 提案：把"可重入执行器"放进 ctx（唯一需要改的 SPI）

在 `internal/spi/ctx.go`（现在只有 `WithSession`/`WithRunID`，`:14-49`）加一个值：

```go
// SubDispatch 是"给工具用的、已过审批的执行入口"。
// 由宿主在调工具之前放进 ctx；工具拿它做子调用。
type SubDispatch struct {
    Runner  Runner   // 已过审批的 Runner（guardedRunner），不是 kernel 里那个裸 Pipeline
    Depth   int      // 0 = 顶层一次调用；>=1 不许再调 workflow
    Visible []string // 当前模式可见的工具名；空 = 不限制（仅 run 路径）
}
func WithSubDispatch(ctx context.Context, d SubDispatch) context.Context
func SubDispatchFrom(ctx context.Context) (SubDispatch, bool)
```

放置点只有一处：`guardedRunner.Run`（`cmd/dshd/acp.go:255`）——因为 `run` 和 `acp` 用的是同一个类型。`guardedRunner` 再补一个 `visible spi.ToolRegistry` 字段，构造时塞 `modes.Filter(...)` 的结果（`acp.go:197-202`、`main.go:473-477`）。

为什么不做成 kernel 服务：因为审批是**会话级**的（`modeApproval`、`approvals *approval.Memory`、`sessionID` 三个字段都在 `guardedRunner` 上，`acp.go:242-253`），而 ACP 一个进程服务多会话（历史按 sessionID 分域，`cmd/dshd/acp.go:220-227`），kernel 装配期拿不到会话。ctx 是唯一按调用携带会话信息的地方（同理：`spi.WithSession`，`internal/spi/ctx.go:14-20`）。

一旦拿到，子调用就是一次普通的 `Runner.Run(ctx, name, args)`：

- 审批：`guardedRunner.Run` 再走一遍（`acp.go:256-282`）；
- 计划模式：`Pipeline` 的 pre-execute 再走一遍（`pipeline.go:84`）；
- 外置/裁剪：再走一遍（`pipeline.go:127-132`）；
- 递归：`Depth >= 1` 时拒绝调 `workflow`（先例：`cmd/dshd/subagent.go:39` `maxDepth = 1`）。

### 2.5 Caps 怎么传

**不传。** 子调用由 `Pipeline` 自己按名字 `reg.Get(call.Name)` 取 Caps（`pipeline.go:104-105`），计划模式闸门再取一次（`plan.go:84-89`），审批再取一次（`acp.go:256`）。workflow 工具本身只声明 `spi.Caps{}`——和 `subagent` 完全同一条论证（`modules/tool/subagent/subagent.go:34`，以及 `cmd/dshd/snapshot_test.go:163-165` 的注释："真正干什么由它调用的工具各自声明"）。

**这条论证只在 2.4 的通道成立时才成立**，所以它必须被测试守住，而不是靠注释。

### 2.6 审批的真实成本与放大效应

- 一次批准只覆盖这一轮；但选了 `allow_always` 就写进会话级记忆（`internal/approval/memory.go:22-46`，调用点 `acp.go:330`、`acp.go:541`）——**按工具名，不按调用**。
- 没人回答按拒绝处理，默认等 120s（`docs/h13-approval.md:40`、`cmd/dshd/serve_approval.go:64-69`）。

推论：
1. `allow_once` 只覆盖**那一次**——脚本里调同一个写工具 50 次就是 50 次提问（`acp.go:257-268`：只有 `remembered` 为真才跳过提问，而记忆只在 `allow_always` 时写入）。反过来，一旦选了 `allow_always`，**该会话该工具此后全部放行**，脚本能把它放大成批量操作。两种结果都很糟：前者卡死，后者失控。所以**每次 workflow 调用里"需要审批的子调用"设上限（建议 3）**，超了立刻停并给 Hint。
2. 脚本里出现 3 个不同的写工具 = 最多 3 次提问 × 120s = 最坏 6 分钟卡死一轮。这也支持"上限"这个设计。

---

## 3. 失败语义

先说一个**必须知道的坑**：

```go
// internal/agent/loop.go:250-253（错误分支）
shown := spi.Result{
    Summary: "Error: " + err.Error(),
    Hint:    res.Hint,
}
```

错误返回时，`Result.Text` 和 `Result.Data` **被整个丢掉**，模型只看到 `Summary + Hint`；写进 v4 的也是这份 `shown`（`cmd/dshd/acp.go:176-186`）。所以"部分成功 + 失败位置"这种信息，**只能走 Hint/Summary，或者干脆不返回 error**。

据此定三层语义：

### 3.1 单步失败（最常见）：默认 abort，但**整次调用不返回 error**

- 语义参照 `exec_literal` 已经写清楚的那条："非零退出码是**程序失败**而不是**工具失败**：输出照回，Hint 给下一步"（`modules/tool/execlit/execlit.go:13`）。脚本里第 3 步失败属于**业务失败**：脚本跑完了，其中某某没跑通。
- 具体返回：
  - `Err = nil`
  - `Summary`：`workflow：3/5 步，第 3 步 grep 失败`
  - `Data`：`{ok:false, failedStep:2, steps:[{i,tool,ok,ms,bytes,pointer}...], output:null}`
  - `Text`：人类/模型可读的 trace（见 §4 预算），失败那步带上它的 `Hint` 原文（`Pipeline` 保证非空：`pipeline.go:118-124`）
  - `Hint`：`第 3 步 grep 失败：<原文>；前两步的结果在 <artifact>；改参数重跑，或把第 3 步换成 read`
- 步级 `on_error: "continue"` 时，该步结果变成 `{ok:false, hint}` 绑进变量，脚本继续；**失败仍然出现在 trace 里**，绝不允许静默吞掉。

### 3.2 脚本级失败：返回 error

解析失败、未知工具、工具不在当前模式可见集合、预算耗尽、`SubDispatchFrom` 拿不到、递归超深——这些是"这段脚本根本不能跑"，没有部分结果可言，按 R3 返回 `Result{Hint}, err`（`internal/spi/tool.go:30-31`、`AGENTS.md:21` R3）。

### 3.3 取消/超时

ctx 被取消时，`runTool2` 会把 Hint 换成固定文案（`loop.go:257-261`），**脚本自己给的 Hint 会被覆盖**。所以已完成到第几步必须写进 `err.Error()`，例如：

```
workflow 被中断：已完成 2/5 步（grep、read），第 3 步 read 中断，结果未知
```

**最后一个坑**：error 路径下 `Text`、`Data`、`Artifacts` 全都进不了 `shown`（`loop.go:250-253`），所以"结果在哪个文件"这件事必须写进 `err.Error()` 或 `Hint`：`workflow 被中断：已完成 2/5 步；部分结果见 <artifact 路径>`。这是本设计里最别扭的一处，明确写出来，不要假装干净。

### 3.4 Stop 必须传播

子调用返回 `Result.Stop`（`internal/spi/tool.go:66-68`）时，外层 loop 只认**顶层**那个 Result 的 Stop（`loop.go:281`、`:216-221`）。脚本里调 `ask_user_question`（`modules/tool/ask/ask.go:194`）或 `exit_plan_mode`（`modules/tool/plan/plan.go:171`），Stop 会被吞掉，模型就能"自问自答"、或者在没人批的情况下静默退出计划模式（`exit_plan_mode` 的 `Exec` 会先 `leave()`，`plan.go:166`）。

→ workflow 遇到任何子调用 `Stop=true`：立刻中止，设自己的 `Stop=true` 返回。**同时**把这两个工具放进黑名单（纵深防御）。

---

## 4. 输出预算

### 4.1 现状的三层口径（都必须对齐）

| 层 | 阈值 | 行为 | 证据 |
|---|---|---|---|
| spill（事后，落盘） | 4000 **字符** | Data 外置成 artifact + 指针；Text 砍到 3000 rune（`MaxChars*3/4`） | `modules/tools/pipeline.go:23`、`:30-33`、`:200-218`、`:225`；默认值来源 `cmd/dshd/main.go:357`、`cmd/dshd/acp.go:403` |
| prune（事前，投影） | 16 KB / 200 行 | 头 60% / 尾 40%，中间一行指针；**不超预算原样返回** | `internal/prune/prune.go:26-34`、`:79-81`、`:142-163`；调用点 `internal/session/project.go:33` |
| compact（历史） | 尾巴 2000 token；摘要输入 24000 字符 / 单条 1200 字符 | 只压前缀 | `internal/compact/compact.go:38-40`、`:52-63`、`:116-119` |

`prune` 的注释（`prune.go:26-31`）已经写明为什么 16KB 而不是 8KB：4000 字符的中文约 12KB，定 8KB 会"在已经截断过的内容上再截一刀"。

### 4.2 脚本侧的四条预算

1. **每步喂给脚本的体积**：子调用返回的是**已经过 spill 的** Result——超过 4000 字符的那份，`Data` 已经变成 `{spilled:true, bytes, artifact}`（`pipeline.go:206-210`）。这有个硬后果：**脚本拿不到全文，只能拿到指针**。想要正文就自己再调 `read`（别指望读 artifact 路径——那是文件系统能力，脚本不该有）。
2. **脚本可见视图**：每步结果压成 `{ok, bytes, summary, text≤800 rune, pointer}`。默认 800 可配；否则 3 个 3000 字符的结果拼起来就是 9000。
3. **单次 run 的总量**：子调用次数 ≤ 100（foreach 展开后）、累计喂给脚本 ≤ 64 KB、步数 ≤ 50、审批类子调用 ≤ 3、时间 ≤ 120s。超限 → 3.2 的脚本级失败，Hint 说清"缩小范围/分两次"。
4. **返回给模型**：`Text` ≤ **3000 rune**。这个数字不是拍的——它就是 spill 的正文预算（`4000*3/4`，`pipeline.go:225`）。只要不超过它，就形成一条**可验证的链路**：

   ```
   workflow.Text ≤ 3000 rune  ⇒ spill 不触发（pipeline.go:170-172 的 len(res.Text) > 4000 为假）
                              ⇒ prune 走快路径原样返回（prune.go:79-81）
                              ⇒ 模型看到的正是脚本产出的那一段
   ```

   `Data` 只放 trace（每步 ≤ 120 字符摘要），确保 ≤ 4000 字节不触发 Data 外置。

### 4.3 超了怎么办：自己外置，别让上层瞎截

返回体超 3000 rune 时，workflow 自己把完整返回写进 `<home>/artifacts/workflow-<id>.json`，然后 Text 只放"头 + 尾 + 指向那个文件"。

目录必须与 tools 模块的 SpillDir 一致（`cmd/dshd/main.go:86`：`filepath.Join(home, "artifacts")`），**不能放系统 temp**——原因写在 `main.go:84-85`：FS 只放行了这个目录，放 temp 里模型读不到自己刚被外置的结果；而且 `local` 模块专门为此加了读根（`main.go:90` `local.WithExtraReadRoot`）。

---

## 5. 最小可用形态（证明这条路通的那一件）

**证明目标不是"能跑脚本"，是"脚本里的第 N 次调用仍然过闸门、仍然受预算、失败仍然可解释"。** 所以最小形态 = 3 步、有数据依赖、只读、有归并。

### 5.1 脚本（13 行）

参数就是工具参数，`steps` 是脚本本体：

```json
{
  "steps": [
    {"as": "files", "tool": "glob",
     "args": {"pattern": "internal/spi/*.go"}},
    {"as": "hits", "foreach": "${files.paths}", "tool": "grep",
     "args": {"path": "${item}", "pattern": "WithSession"}, "collect": "matches"},
    {"return": "${hits}"}
  ]
}
```

语义（阶段 1 的全部语言，只有五个概念）：`tool` / `args`（字符串里 `${...}` 插值）/ `as`（绑定）/ `foreach`（对数组展开，当前元素为 `${item}`）/ `collect`（把每轮结果的某个字段铺平进 `as`）/ `return`。

### 5.2 期望输出（本机真实数据，已用 `dshd exec --tool grep` 核对过）

这个脚本展开后是 1 次 glob + 10 次 grep（`internal/spi` 下有 10 个 `.go`），= **11 次工具调用，1 次模型往返**。

模型看到的 `Text`（≤3000 rune）：

```
workflow：2 个调用步 / 11 次子调用 / ok / 5 条
files.count=10
hits=[{"path":"internal/spi/ctx.go","line":14,"text":"// WithSession 把会话 id 绑到 ctx 上；空 id 原样返回。"},
      {"path":"internal/spi/ctx.go","line":15,"text":"func WithSession(ctx context.Context, id string) context.Context {"},
      {"path":"internal/spi/ctx_test.go","line":10,...},
      {"path":"internal/spi/ctx_test.go","line":28,...},
      {"path":"internal/spi/ctx_test.go","line":36,...}]
```

（真实命中数 5、扫描 10 个文件，来自本次实跑；`grep` 的 `Data.matches` 形状见 `modules/tool/grep/grep.go:73-85`。）

**诚实说明**：这个例子用**一次** `grep path=internal/spi` 也能得到同样的 5 条。它不是"非它不可"，它是**通道成立的最小反证**：有数据依赖、有 N 次子调用、有归并、有预算。真正只能靠编排的任务形态（例如 `git status --porcelain` → 逐文件 `git diff --stat`）在 §8 里说明为什么放到阶段 2。

---

## 6. 风险：哪些地方会绕过现有的闸门

按严重程度排：

| # | 绕过点 | 机理（证据） | 处置 |
|---|---|---|---|
| **R1** | **`kernel.Inject(ToolRunnerKey)` = 没有审批** | `registry.go:103` 提供裸 Pipeline；`guardedRunner` 在 kernel 外（`main.go:473-477`、`acp.go:195-202`） | 禁注入；只从 ctx 取（§2.4）；加红测：把 SubDispatch 换成裸 Runner 必须被测试抓到 |
| **R2** | **模式白名单只拦模型，不拦调用** | 可见性只在 `modes.Filter`（`acp.go:206`、`view.go:44-48`）；执行用全量 `r.reg`（`acp.go:198`、`pipeline.go:104`） | workflow 自己对子调用名查 `Visible`；顺带把这条洞记进 `docs/unverified.md` |
| **R3** | 计划模式被"跑一半才拒" | 闸门在 pre-execute（`plan.go:194-196`、`pipeline.go:84`），只有真跑到那一步才触发 | **静态预检**：执行第 1 步之前，按 registry 的 Caps 扫一遍全部 step，命中写/执行/破坏性直接整段拒（`plan.blockedHint` 的语义） |
| **R4** | `exit_plan_mode` 在脚本里被调 | `Exec` 先 `leave()`（`plan.go:166`），而 Stop 被吞（`loop.go:281`） | 传播 Stop（§3.4）+ 黑名单 |
| **R5** | `ask_user_question` 在脚本里被调 | 同上，`ask.go:194` 的 Stop 被吞 → 模型自问自答 | 传播 Stop + 黑名单 |
| **R6** | `allow_always` 被脚本放大 | 记忆按 (session, tool)（`memory.go:34`、`acp.go:257`）；一次批准 = 之后全放行 | 每次 run 需要审批的子调用 ≤ 3；Summary 里报告实际审批次数 |
| **R7** | `working_directory` 在脚本里换 cwd | 改进程 cwd（`cmd/dshd/snapshot_test.go:107`），与 subagent 的 chdir 互斥锁（`cmd/dshd/subagent.go:56-81`）打架 | 黑名单（阶段 1）；或只在脚本内维护逻辑 cwd（阶段 2） |
| **R8** | 子调用无审计痕迹 | 子调用不经过 loop 的 Emit，v4 里只有一条 `tool/result`（`acp.go:176-186`）；官方有专门的 `tool/ptc-dispatch-start` / `tool/ptc-dispatch`（`research/21-dsh-session-v4.md:118-119`） | 阶段 2：把 trace 写进 v4（`Log.Append` 不校验 type，`internal/session/log.go:62-78`，所以加事件类型是加法；surface 只认 5 类，`internal/session/surface.go:165-197`，未知类型自然忽略） |
| **R9** | 图片/Stop/Images 通道错位 | `Images` 由 loop 在顶层挪成一条 user 消息（`loop.go:197-207`、`:274-280`）；脚本里调 `read_image` 图片会丢 | `read_image` 进黑名单；文档写明 |
| **R10** | 递归 | workflow 里调 workflow | `Depth` 上限 1（先例 `subagent.go:39`） |
| **R11** | **本仓没有 sandbox** | `internal/` 下无 sandbox 包（目录列表见 §8），全仓 `sandbox` 只出现在注释里（`internal/spi/tool.go:27`、`modules/fs/local/local.go:3`） | 脚本**故意不给**文件/网络/进程能力；它只能调工具，工具的 Caps 就是它的全部权力边界。这是"用工具权限当沙箱"，不是 OS 级隔离——必须在文档里说死 |

**R3 的一个附带发现**（既有问题，不是 workflow 引入的）：计划模式是**进程内单例**（`modules/tool/plan/plan.go:43-47`，注释自己写了"不落盘"），而 `serve` 每轮起一个新的 `dshd run` 子进程（`cmd/dshd/serve_tools.go:44-60`）。所以在 Web/桌面这条真实产品路径上，**计划模式活不过一轮**：这一轮进的计划模式，下一轮就没了。workflow 的方案依赖 `Active()`，因此这个洞会被放大。修它要动会话级状态，超出本文范围，记在这里。

---

## 7. 分阶段计划

### 阶段 1：通道 + 只读串行编排（1 个 PR，可独立验收）

**改哪些文件**

| 文件 | 改什么 |
|---|---|
| `internal/spi/ctx.go` | 加 `SubDispatch` + `WithSubDispatch` / `SubDispatchFrom`（约 30 行） |
| `internal/spi/ctx_test.go` | 三条：空 ctx、带深度、保留 session 键 |
| `cmd/dshd/acp.go` | `guardedRunner` 加 `visible spi.ToolRegistry`；`Run` 里 `ctx = spi.WithSubDispatch(...)`；构造处传 `modes.Filter`（`:197-202`） |
| `cmd/dshd/main.go` | run 路径同样处理（`:473-477`）；`builtinModules` 加 `workflow.Module()`（`:77-127`） |
| `modules/tool/workflow/workflow.go` | 工具 + 求值器（`tool/args/as/foreach/collect/return`）+ 预算 + 黑名单 + 静态预检 |
| `modules/tool/workflow/workflow_test.go` | 见下面失败判据 |
| `cmd/dshd/snapshot_test.go` | `want` 表加 `"workflow": {}`（不加就红，`:186-193`） |
| `cmd/dshd/testdata/tools.snapshot.json` | `go test ./cmd/dshd -run TestToolTableSnapshot -update` 后 diff 入库（R4） |
| `dsh.toml` | 加 `[[module]] id="tool-workflow"`——manifest 在场时是白名单，不列出来会被静默跳过（`internal/kernel/module.go:109-126`） |
| `docs/tool-parity.md` | 写明"不做 JS 脚本，与官方形态不同" |

**验收命令**（共享树只跑这些定向测试，见 `AGENTS.md:56-66`）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./... && go vet ./...
go test ./internal/spi -run SubDispatch -v
go test ./modules/tool/workflow -v
go test ./cmd/dshd -run 'TestToolTableSnapshot|TestWriteToolsDeclareCaps|TestToolTableTokenBudget|TestToolTableIsStableAcrossAssemblies' -v
go run ./cmd/dshd exec --tool workflow --args "$(cat tmp/workflow-example.json)"   # §5 的脚本；exec 走 loadMods，所以 dsh.toml 必须先加这一项
python scripts/guard-selftest.py
```

**失败判据**（每条都要能"弄坏必红"，`AGENTS.md:67`）

1. **审批不可绕过**：`--policy deny` 下跑含 `write` 的脚本 → 落盘文件不存在 **且** 子调用 0 次。反证：把 SubDispatch 换成裸 `ToolRunnerKey` 的 Pipeline，这条测试必须红。
2. **计划模式**：`enter_plan_mode` 之后跑同一个脚本 → 目标路径不存在、`Result.Text` 里出现"计划模式"字样、子调用 0 次（静态预检生效，不是跑一半）。
3. **可见性**：模式白名单里没有 `write` 时，脚本调 `write` → 拒绝且 0 落盘。反证：删掉 `Visible` 检查，必须红。
4. **预算**：构造一个返回 50 KB 的脚本 → `len([]rune(Text)) <= 3000`、`Artifacts` 非空、`Data` ≤ 4000 字节。
5. **失败语义**：第 3 步给个不存在的工具名 → `err == nil`、`Data.ok == false`、`Data.failedStep == 2`、`Hint` 含失败工具的原始 Hint。
6. **递归**：脚本里调 `workflow` → 拒绝。
7. **token 预算**：`TestToolTableTokenBudget` 仍 ≤ 6000（当前 5,451，`docs/tool-table-budget.md:3`，只剩 549）。超了就先砍 `foreach`/`on_error` 这些字段的说明文字，不要把工具砍掉。

### 阶段 2：写类/foreach/审计

- `on_error`、`foreach` 的嵌套引用、`when` 条件（只加一个，不要变成表达式语言）。
- 子调用 trace 落 v4：新增 `tool/ptc-dispatch-start` / `tool/ptc-dispatch`（对齐 `research/21-dsh-session-v4.md:118-119`），由 workflow 通过一个 `spi.LogWriter` 键写入，不让模块自己开文件。
- 验收：`go test ./cmd/dshd -run PTC` + `dshd session` 回放能看到子调用事件；判据——事件条数 = 子调用次数，且 surface 投影不变（`surface.go:165-197` 只认 5 类）。

### 阶段 3：语言（只有在阶段 1/2 被真机证明之后才做）

三个选项，按代价排：

1. **Starlark**（`go.starlark.net`）：纯 Go、无 cgo、有 `SetMaxExecutionSteps`、没有隐式 I/O，`go.mod` 从 2 个依赖变 3 个。模型对 Python 语法的先验比这套 JSON 强得多。**唯一需要评审的是"新增第三方依赖"本身**，不是能力。
2. **子进程**（复用 `dshd` 自己或 node）：才有任意语言与真隔离，但要一套 RPC + 复用 `@@APPROVAL@@`（`cmd/dshd/approval_stdio.go:26-27`）。
3. **JS**：不做（`AGENTS.md:24`、`PLAN.md:440`）。

---

## 8. 这个方案没解决什么

- **不解决并行/异步**。`loop` 的工具调用是严格串行的（`loop.go:194-211`），脚本阶段 1 也串行。官方的 fan-out/phase（`research/20-dsh-tool-schemas.index.md:90`）没有。真做并行要先解决：审批并发、v4 seq 顺序、以及 `subagent` 那条进程级 cwd 锁（`cmd/dshd/subagent.go:56-81`）。
- **不解决"任意计算"**。阶段 1 没有算术、没有字符串处理、没有条件分支（阶段 2 才有 `when`）。想让脚本"把 10 个 JSON 合并求和"，做不到——模型仍要多轮。
- **不解决沙箱**。本仓**没有任何执行隔离包**（`internal/` 只有 acp/agent/approval/compact/fsutil/kernel/llm/modes/prompt/prune/schedule/session/spi/subagent/transport）。脚本的权力边界 = 它被允许调用的工具的 Caps 之和。`bash` 一旦在白名单里，脚本就等价于任意代码执行。
- **不解决跨会话/跨 cwd**。cwd 是进程级的（R7）。
- **不解决 `serve` 链路的审批闭环**。`docs/h13-approval.md:3` 明确写着 pending approval 的 cancel→拒绝路径尚未闭环；脚本会把这个缺口放大（最多 3 次提问，每次 120s）。
- **不解决工具表预算**。5,451/6,000（`docs/tool-table-budget.md:3`），450 token 左右的新 schema 是硬挤进去的；`docs/tool-table-budget.md:37-60` 的 A/B 已经证明"砍参数说明会换来回合数上升"，所以不能靠压缩表省钱。
- **不解决审计**。阶段 2 之前，子调用在 v4 里不可见（R8）。
- **不解决与官方 `workflow-ptc` 的行为对齐**。官方是 JS，我们不是；只能对齐"能力形态"，不能对齐"脚本长相"。这一条要写进 `docs/tool-parity.md`，别让人以为能直接跑官方脚本。

---

## 9. 未核实

| 项 | 为什么没核实 | 影响 |
|---|---|---|
| 官方 `workflow-ptc` 的真实行为（脚本 API、并发语义、`tool/ptc-dispatch` 的字段） | 只读了二手材料（`research/21-dsh-session-v4.md:118-119`、`research/20-dsh-tool-schemas.index.md:90`），没读官方源码 | 字段名可能与 v4 实测不一致，阶段 2 落事件前必须实测一份真会话 |
| 目标部署机有没有 node/python | 只在本机验过（node v24.20.0 / Python 3.13.15）；执行机可能是别的机器 | 影响阶段 3 只能选 Starlark，不能选"子进程跑 node" |
| 并行 fan-out 的收益 | 没做 A/B | 阶段 1/2 不做并行是有理由的（见 §6 R7 的 cwd、§6 R8 的 seq 顺序），但"并行能省多少"没有数字 |
| `workflow` 在 read-only 模式下的实际行为 | `dshd run` 不做模式过滤（`main.go:489-501`），ACP 才过滤（`acp.go:206`），两条路径不一致 | 阶段 1 验收要在 **ACP 路径**上验可见性，`run` 路径验不了 |
| 4000 字符的默认值是否适合脚本输出 | 沿用现有默认（`main.go:357`），没有重新标定 | 脚本返回的正文预算（3000 rune）是推导值，不是实测最优点 |

---

## 附录 A · 证据索引

- **工具契约**：`internal/spi/tool.go:17-32`（Tool）、`:35-45`（Caps/NeedsApproval）、`:51-69`（Result/Stop）、`:122-132`（Registry/Runner 唯一入口）
- **agent 循环**：`internal/agent/loop.go:131`（步循环）、`:141-143`（ctx 检查）、`:194-211`（串行工具调用）、`:245-267`（失败只剩 Summary+Hint）、`:274-281`（Stop）、`:322-343`（工具表来自 registry 视图）
- **流水线**：`modules/tools/pipeline.go:63-146`（Run）、`:84`（pre-execute）、`:104-115`（registry 查找 + 唯一 Exec 点）、`:118-124`（Hint 兜底）、`:159-219`（spill）、`:224-246`（Text 截断）
- **闸门**：`modules/tool/plan/plan.go:80-94`（按 Caps 拦）、`:194-196`（挂到总线）、`cmd/dshd/acp.go:255-283`（审批）
- **Runner 供给**：`modules/tools/registry.go:93-105`（裸 Pipeline）、`cmd/dshd/main.go:473-477`、`cmd/dshd/acp.go:195-202`（审批在 kernel 外）
- **预算**：`internal/prune/prune.go:26-34`、`:79-81`、`internal/compact/compact.go:38-40`、`internal/session/project.go:33`
- **落盘/外置目录**：`cmd/dshd/main.go:84-90`、`modules/tools/pipeline.go:335-353`
- **审批记忆/超时**：`internal/approval/memory.go:22-46`、`cmd/dshd/serve_approval.go:64-69`、`docs/h13-approval.md:3`、`:40`
- **门禁**：`cmd/dshd/snapshot_test.go:23-35`、`:39-67`、`:93-201`、`cmd/dshd/tool_budget_test.go:16-31`、`docs/tool-table-budget.md:3`
- **上游形态**：`docs/official-parity.md:30`、`:50`、`research/21-dsh-session-v4.md:118-119`、`research/20-dsh-tool-schemas.index.md:90`
- **规矩**：`AGENTS.md:21`（R3）、`:24`（R6）、`:67`（守卫可反证）、`PLAN.md:440`（不做宿主内 JS 桥）

---

## 落地记录（2026-11-04，M312/M315）

**已实现**：`workflow` 工具（`modules/tool/workflow/`），参数名对齐官方 `script` / `meta`，
内容是**声明式 JSON 步骤表**（不是 JS，本仓 R6）。

| 要求 | 落点 |
|---|---|
| 子调用必须重新穿过受管通道 | `spi.SubDispatch` 由 `guardedRunner.Run` 放进 ctx；workflow 只从 ctx 取，**不许 Inject 裸 Pipeline** |
| 失败不丢现场 | 单步失败记进 trace、`err=nil`、脚本继续；整体 `Data.ok=false` |
| 输出预算 | 正文 ≤3000 rune（低于 spill 4000 字符阈值，链路可验证） |
| 递归 | `spi.MaxSubDepth = 3`，超过就拒 |
| `meta` 回显 | 原样带回 `Data.meta`，供调用方对账 |
| 每步时长 | `trace.MS` |

**仍未解决（本轮如实记下）**：

1. ~~**子调用不进 v4 日志。**~~ **已解决（M316）**：`spi.Call` 加 `Nested` 标记
   （Pipeline 从 ctx 的 SubDispatch 层数推出），宿主订阅 `tools/result`，
   **只给 Nested 的写** v4——顶层仍由循环自己写，所以不会重复。
   真机验过：一次 2 步 workflow → 会话里 `tool/call` 3 条、`tool/result` 3 条
   （1 条顶层 + 2 条子调用），正好对得上。
2. **`Stop` 被子调用吞掉**：`exit_plan_mode` / `ask_user_question` 若出现在 script 里，
   它们的 `Stop` 不会传出去（`loop.go:281` 只看顶层结果）。已在 Hint 里避免引导，
   但没在代码上拦。
3. **`allow_always` 会被脚本放大**：一次批准 → 脚本里同一工具跑 N 次。
   这是 `SubDispatch` 设计上的取舍（子调用照样要过审批，但"以后都允许"是按工具记的）。
