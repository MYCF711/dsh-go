# E8 · `user_terminal_list` / `user_terminal_read` 的父→子 roster 桥（真机可用）

> 共享任务：`task-22`（revision 1 → claim → complete）。执行人 `e8-termbridge`。
> 工作目录：`.agents/worktrees/dsh-bdb66fbf-95ba`。上游基线 **0.2.2-rc.2**。
> 前置：C2（`tmp/GO8-RC2/reports/C2-terminal-panel.md`，两个工具 + 适配器）、
> A2（`tmp/GO8-RC2/reports/A2-terminal.md`，`WebTerminalInfo.origin/control`、`terminal.watch`）。
> 本文件所有"真机"证据都在本次会话内跑出，日志落在 `tmp/GO8-RC2/logs/E8-*`。

---

## 0. 结论摘要（先看这段）

| 判据 | 结果 |
|---|---|
| 两个工具在真机上能否读到真终端 | **能**：真 `dshd serve` 父进程 + 真 `dshd run` 子进程 + 真 PTY，`user_terminal_list` 读回 `t1 · bash.exe · <cwd> · command running: unknown`，`user_terminal_read` 读回真 PTY 屏幕（含 `E8-PTY-MARKER` 与 `[lines: 1-7 of 7]`） |
| 桥的形态 | 复用既有 stdio 帧通道（与 `@@JOBS@@` / `@@APPROVAL@@` / `@@STEER@@` 同族）：子进程 stderr 问、父进程 stdin 答；**没有**新 IPC、没有 HTTP 回连、没有嵌 JS |
| 数据来源 | 父进程用**与 `terminal.watch` 同一份** roster：`transport.NewUserTerminalSource(officialJT, originFn)`（C2 已交付的适配器），桥只做搬运 |
| 子代理调用 | **官方原文拒绝**，且拒绝发生在读数据之前；真机验证通过 |
| `readUserTerminals:false` | 两个工具**都不注册**（真机 `dshd tools` 61 个工具里没有它们；真子进程调用得到 "当前模式未声明工具"） |
| 先红后绿 | 三段红都留了证据：①工具不存在（未接线）②工具在但桥没开（`ErrBridgeUnavailable`）③桥通了但**子代理没被拒绝**（真缺陷，见 §6） |
| 真机验收 | main 场景 **11/11 PASS**（exit 0），disabled 场景 **3/3 PASS**（exit 0） |
| 门禁 | `go build ./...`=0、`go vet ./...`=0、`go test ./cmd/dshd` 目标 4 条=0、`./internal/terminal`+`./modules/tool/terminalpanel` 绿；`./internal/transport` 有 1 条**并行任务**造成的红（见 §8.3，不是本任务引入） |
| 测试 | 新增 **25** 条用例（`internal/terminal/bridge_test.go` 15、`cmd/dshd/terminal_bridge_test.go` 5、`modules/tool/terminalpanel/config_env_test.go` 5） |
| 需要 Lead 后续动作 | `PLAN.md` §15 与 `docs/ci-rules.md` 第 6 条的工具表上限需要写成 **6400**（依据见 §8.1；文档归 Lead，本次只改测试里的判据） |

---

## 1. 帧协议形状

### 1.1 两个标记

与仓库既有 stdio 通道**同一形状**：一整行、行首一个 tag、后面是 JSON。

| 方向 | 载体 | tag |
|---|---|---|
| 子进程 → 父进程（请求） | 子进程 **stderr** | `@@USER-TERMINALS@@ ` |
| 父进程 → 子进程（应答） | 子进程 **stdin** | `@@USER-TERMINALS-RESP@@ ` |

为什么不碰 stdout：serve 的 stdout 只有一行 `ready` 握手（H11 进程级契约）；工具子进程的 stdout 归模型文本。与 `approval_stdio.go` 文件头同一个理由。

### 1.2 请求 / 应答（`internal/terminal/bridge.go`）

```jsonc
// 子 → 父
{"requestId":"uterm-<pid>-<n>","sessionId":"<会话 id>","op":"list"}
{"requestId":"…","sessionId":"…","op":"read","id":"t1","lines":500}

// 父 → 子（成功）
{"requestId":"…","ok":true,"terminals":[{"id":"t1","title":"bash.exe","cwd":"…",
  "control":"user","state":"running","running":"unknown","exitCode":null}]}
{"requestId":"…","ok":true,"page":{"text":"…","totalLines":7,"lineBegin":1,"lineEnd":7,"truncated":false}}

// 父 → 子（失败，error 一定非空）
{"requestId":"…","ok":false,"error":"cross_session: …"}
```

字段与工具面一一对应：`terminals[]` = `terminal.PanelTerminal`，`page` = `terminal.ScreenPage`（C2 定的契约），**不新增第三套形状**。

### 1.3 时序（serve 形态，一次工具调用）

```
工具（dshd run 子进程）          父进程（dshd serve）
  user_terminal_list
    → BridgeClient.ListUserTerminals
    → stderr: @@USER-TERMINALS@@ {…,op:"list"}
                                                       approvalBroker.WatchSession 逐行
                                                        → serveUserTerminalBridgeLine
                                                        → terminal.BridgeServer.Handle
                                                        → transport.NewUserTerminalSource
                                                        → officialJT.TerminalList(sessionID)   ← 真 roster
    ← stdin:  @@USER-TERMINALS-RESP@@ {ok:true,terminals:[…]}
    → BridgeClient.Deliver（由 approval_stdio.go 的 stdin 分发器投递）
    → 工具渲染官方格式（`id · title · cwd · command running: X`）
```

`read` 与 `list` 同一套帧，只是 `op` 不同、应答带 `page`。父进程**不缓存 roster**：每次调用都读当前真值，因此不存在"轮内快照过期"的语义（C2 报告 §6.3 方案 B 的代价）。

### 1.4 谁能打开这条通道

父进程在起工具子进程时显式声明（与 `DSHD_JOBS_STDIO` / `DSHD_STEER_STDIO` 同形）：

```go
cmd.Env = append(cmd.Env, "DSHD_USER_TERMINALS_STDIO=1")
```

子进程只有看到它，才把 stderr 当通道（`terminalBridgeWriter()` 返回非 nil）。**没开就绝不写**：独立 `dshd run` 的 stderr 是人看的诊断，不能混协议帧。

---

## 2. 桥的失败模式（全部结构化，不许回空列表）

| 场景 | 子进程看到什么 | 父进程行为 |
|---|---|---|
| 没有桥（独立 `dshd run` / `dshd exec`、未开 env） | `terminal.ErrBridgeUnavailable`：*"这个进程里没有父进程终端 roster 桥（user_terminal_* 只能在 serve 起的工具子进程里读终端）"* | — |
| `dshd exec --tool user_terminal_list`（无会话） | 工具层先报官方原文 `user terminal tools require an initiating agent`（会话检查在数据源之前） | — |
| 父进程没装桥（老装配/测试） | 该行不被认领 → 落到后面的审批分发 → 子进程按 `timeoutMs`（默认 30s）超时失败，错误里带 `requestId` | 不写任何应答 |
| 请求的 `sessionId` ≠ 本轮子进程的会话 | `cross_session: 请求属于会话 "A"，本子进程是 "B"` | **拒绝在读数据之前**（数据源调用次数为 0，测试钉住） |
| 父进程已退出（写 stderr 失败/EPIPE） | 立刻 `ErrBridgeUnavailable`（不等满超时） | — |
| 父进程数据源自己失败（PTY 层报错） | 原样错误文本（"列终端失败: …" / "读终端屏幕失败: …"） | 读真值失败，报原文 |
| 未知 `op` / read 缺 `id` / 帧不是 JSON | 结构化 `ok:false` + 原因（坏帧也回一句，免得对面只能等到超时） | 应答即错 |
| 应答 `ok:true` 但缺 `page` | "父进程应答缺 page（终端 t1）：协议违约"（**不当空屏幕**） | 不可能发生（编码路径保证），测试兜底 |
| 应答投给不存在的 `requestId`（迟到/编造） | 不影响任何人（`Deliver` 返回 false，丢弃） | — |

上表每一行都有对应用例：`internal/terminal/bridge_test.go`（协议层 15 条）+ `cmd/dshd/terminal_bridge_test.go`（接线层 5 条）。

---

## 3. 真机验收（硬证据）

### 3.1 怎么跑

```bash
# 1) 真二进制（按 AGENTS.md：从 C: 证据目录启动，避免 D: Medium 完整性级别的 MSYS 事故）
export PATH="/c/Program Files/Go/bin:$PATH"
go build -o "/c/Users/Administrator/AppData/Local/Temp/e8-bin/dshd.exe" ./cmd/dshd

# 2) 真 serve（临时 home + --addr 127.0.0.1:0）+ 真 PTY + 真子进程；模型这一层是脚本替身
#    （只替模型，不替终端/桥/工具，见 tmp/GO8-RC2/e8/stub_llm.py 文件头）
python tmp/GO8-RC2/e8/real_e2e.py --bin "/c/Users/Administrator/AppData/Local/Temp/e8-bin/dshd.exe" --scenario main
python tmp/GO8-RC2/e8/real_e2e.py --bin "/c/Users/Administrator/AppData/Local/Temp/e8-bin/dshd.exe" --scenario disabled
```

驱动脚本的每一跳都是真件：真 `dshd serve`（父进程，持有 `officialJT`/PTY）→ `GET /connect?token=` 换 cookie → `session.create` → `terminal.create`（**真 Git Bash PTY**）→ `terminal.input`（往 PTY 打 `echo E8-PTY-MARKER`）→ `terminal.list`（同一份 roster）→ `session.prompt` → **真 `dshd run` 子进程**里执行**真工具** → 结果进 v4 会话日志 → 脚本从 `/api/session.history` 读回工具结果做断言。

### 3.2 main 场景：11/11 PASS，exit 0

日志：`tmp/GO8-RC2/logs/E8-lived-main.log`（`MAIN_EXIT=0`）；工具结果原文：`tmp/GO8-RC2/logs/E8-lived-main-tools.txt`；帧（journal）：`E8-lived-main-history.json`；serve stderr：`E8-lived-main-serve.log`。

```
[PASS] serve 打出 ready 行  — http://127.0.0.1:54862
[PASS] /connect 换到 cookie
[PASS] session.create 得到会话  — fc74d12c-751f-c9a8-edef-c6efa42d1019
[PASS] terminal.create 建出真 PTY 终端  — state=running origin=user control=user
       cwd=C:\Users\ADMINI~1\AppData\Local\Temp\e8-termlink-main-8sa8gfxl\cwd
[PASS] terminal.list 的 roster 里有 t1  — roster ids=['t1']
[PASS] 主 turn 收工（出现 MAIN-DONE）  — 26 帧
[PASS] user_terminal_list 在真子进程里读回真 roster（t1 + running 三态）
[PASS] user_terminal_read 读回真 PTY 屏幕（E8-PTY-MARKER）
[PASS] 子代理调用被官方原文拒绝（拒绝语随子代理回答回到主会话）
[PASS] 子代理没有读到主会话的终端 roster（官方禁止的那件事没发生）
[PASS] serve 没有终端桥写回失败
=== 汇总 ===  OK=11 FAIL=0     MAIN_EXIT=0
```

工具结果原文（从 `session.history` 的 `tool/result` 帧里取，逐字）：

```
# user_terminal_list（callId=call-e8-list，isError=false）
t1 · bash.exe · C:\Users\ADMINI~1\AppData\Local\Temp\e8-termlink-main-8sa8gfxl\cwd · command running: unknown

# user_terminal_read（callId=call-e8-read，isError=false）
<ESC>[?9001h…<ESC>[H<ESC>]0;MINGW64:/tmp/e8-termlink-main-8sa8gfxl/cwd<BEL>…
WeiCong@Wei MINGW64 /tmp/e8-termlink-main-8sa8gfxl/cwd
$ echo E8-PTY-MARKER
E8-PTY-MARKER
…
[lines: 1-7 of 7]
```

要点：

- `running: unknown` 是**如实**的（本宿主没有前台进程探针，官方为"观测不到"准备的正是 `unknown`；给 `yes` 就是编的，C2 §4 已登记）。
- `read` 给的是**真 PTY 屏幕**（含 ANSI 序列，这是"没有屏幕模型"的如实表现，C2 §7 G2 已登记），位置行 `[lines: 1-7 of 7]` 是官方渲染格式。
- 桥的帧在 serve stderr 里能看到（`child(stderr):` 原样回显）：
  `@@USER-TERMINALS@@ {"requestId":"uterm-19108-4","sessionId":"…","op":"list"}`。

### 3.3 disabled 场景：3/3 PASS，exit 0（`readUserTerminals:false`）

日志：`tmp/GO8-RC2/logs/E8-lived-disabled.log`（`DISABLED_EXIT=0`）。

```
[PASS] session.create 得到会话  — d9f4d4f2-531c-4f35-be10-5b6d027ea017
[PASS] 关闭开关后 `dshd tools` 里没有两个终端面板工具  — 工具数=61
[PASS] 关闭开关后真子进程里调用该工具是 unknown tool
=== 汇总 ===  OK=3 FAIL=0     DISABLED_EXIT=0
```

真子进程里的工具结果（`E8-lived-disabled-tools.txt`）：

```
{"text":"Error: 当前模式未声明工具 \"user_terminal_list\""}
{"text":"提示：只调用本轮工具表中列出的名字；隐藏或未知工具都不会执行"}
{"text":"Error: 当前模式未声明工具 \"user_terminal_read\""}
```

即：`readUserTerminals:false` 时两个工具**确实不存在**（官方 "removes both"），而不是"注册成永远报错"。

开关来源：`DSHD_TERMINAL_PANEL_READ_USER_TERMINALS`（`modules/tool/terminalpanel/config.go` 新增 `ConfigFromEnv` / `ModuleFromEnv`，三个官方键一一对应；非法值让**装配失败**而不是静默回默认值）。为什么不走 `dsh.toml` 的 config：manifest 的 `config` 只写进 `kernel.Module.Config`，而模块 `Register` 只拿 `*Scope`，没有下发通道；宿主既有的模块级开关做法就是入口解析环境变量（`officialPTYScrollbackRowsFromEnv`、`compactHeadroomFromEnv` 同形）。接官方 config 树是另一件事，见 §8.2。

---

## 4. 同进程形态 ↔ serve 形态：同一份适配器

任务书要求"同进程形态 `kernel.Provide(root, terminal.UserTerminalsKey, …)`；serve 形态走桥"。落地为**一个适配器、两套搬运**：

```
transport.NewUserTerminalSource(officialJT, originFn)        ← 唯一的数据翻译层（C2 交付）
        ├── 同进程形态：provideUserTerminalSource(root, jt, home)   → kernel.Provide(…)
        └── serve 形态：installUserTerminalBridge(broker, jt, home) → BridgeServer（同一份适配器）
                                                                     ↑ 子进程 BridgeClient
```

`cmd/dshd/terminal_bridge_test.go` 的 `TestTerminalPanelToolsReadRealRosterInProcess` 就是同进程形态的判据：真 `kernel.Assemble(builtinModules(...))` + 真两个工具 + 真流水线，断言官方渲染正文、子代理拒绝与"拒绝前不读数据"。

今天的入口里**没有**同进程形态（PTY 控制器只在 `cmdServe` 里造，工具在子进程里跑），所以这条接线点是留给测试与将来 in-process 组合的——不是死代码：它和 serve 形态共用同一个适配器，两条路的 roster 与拒绝语义逐字节相同。

---

## 5. 先红后绿（三段红，都有日志）

| 阶段 | 红的样子 | 证据 |
|---|---|---|
| ① 未接线 | 工具表里根本没有 `user_terminal_*`（C2 交付时 `grep -rn 'terminalpanel' cmd/` 零命中） | C2 报告 §8 门禁段 |
| ② 有工具、没桥 | 真机上工具**真的执行了**，但结构化失败：`Error: 列终端失败: 这个进程里没有父进程终端 roster 桥…`（没有回空列表——这一条正是"不许假 roster"的证明） | `tmp/GO8-RC2/logs/E8-red-no-env-tools.txt`、`E8-red-no-env-history.json`、`E8-red-no-env-serve.log` |
| ③ 桥通了、子代理没被拒绝 | 子代理里的 `user_terminal_list` **读到了主会话的 roster**（`SUBAGENT-TOOL-RESULT-BEGIN t1 · bash.exe · …`）——官方明确禁止 | 见 §6（同一轮验收里先红后绿） |

②的原因是父进程没给子进程 `DSHD_USER_TERMINALS_STDIO=1`（子进程不知道 stderr 上有通道），补上行后立刻绿。③是真缺陷，单独一节。

---

## 6. 真缺陷：子代理内的工具调用**沿用父会话 id**（官方判据在宿主里无事实可查）

### 6.1 判据（官方）

- 官方：`agent.session.header.origin === 'subagent'` → 直接拒（`terminal-panel/src/tools.ts:35`），拒绝语逐字 `Subagents cannot read the user's terminals, which belong to the main conversation; report what you need to the main agent instead.`（`tools.ts:15`）。
- 官方模型：**子代理有自己的会话**（`origin=subagent`），所以"这条会话是不是子代理"是一个会话属性。

### 6.2 本宿主的事实

- `cmd/dshd/subagent.go` 的 `RunSub` 在**同一进程**里起一个嵌套 `agent.Loop`，并且**不设** `Loop.SessionID` → `agent.Loop.Run` 把 ctx 里的会话身份原样带下去 = **父会话 id**（`internal/agent/loop.go:271` 只按 `l.SessionID != ""` 覆盖）。
- 于是子代理里的工具调用：`spi.Call.Session` = 父会话 id，`agent.FindChild(home, 父会话 id)` = false → 判成 `user` → **不拒绝**。
- 真机证据（本次验收第一轮）：子代理里的 `user_terminal_list` 返回了真 roster：
  `SUBAGENT-TOOL-RESULT-BEGIN t1 · bash.exe · C:\…\cwd · command running: unknown …`
  也就是官方禁止的"子代理读用户的终端"在宿主里**真的发生了**。

### 6.3 修复（两层事实，缺一不可）

`cmd/dshd/terminal_bridge.go` 新增 ctx 标记 + origin 判定分两层：

```go
type subagentCtxKey struct{}
func withSubagentCallContext(ctx context.Context) context.Context   // 由 RunSub 打标
func inSubagentCall(ctx context.Context) bool

func terminalSessionOrigin(home string) func(ctx, sessionID) (string, error) {
    return func(ctx context.Context, sessionID string) (string, error) {
        if inSubagentCall(ctx) { return terminal.PanelOriginSubagent, nil }   // ① 进程内事实
        _, isChild, err := agent.FindChild(home, sessionID)                    // ② 盘上谱系
        …
    }
}
```

- ① **进程内事实**：`RunSub` 在自己那一趟嵌套循环前打 `withSubagentCallContext(ctx)`（`cmd/dshd/subagent.go` 1 行：`loop.Run(withSubagentCallContext(ctx), …)`）。这是本宿主唯一能回答"我在不在子代理里"的地方；它同时覆盖**所有**嵌套工具调用（不只终端工具）。
- ② **盘上谱系**：`agent.FindChild` 仍然保留，覆盖另一种形态——独立进程直接跑一条子会话（`dshd run --session <child-id>`），此时进程本身就是子会话，ctx 里没有标记，只有 v4 `subagent/spawn` 帧。
- 子代理判定**不走桥**：桥断了、父进程没起、甚至没有桥，子代理也必须被拒绝（否则权限问题会被表现成可用性问题）。

### 6.4 真机验证（同一条命令，修复前后对照）

修复前（第一轮 main 场景）：

```
[FAIL] 子代理没有读到主会话的终端 roster（官方禁止的那件事没发生）
  — {"text":"SUBAGENT-TOOL-RESULT-BEGIN t1 · bash.exe · C:\\…\\cwd · command running: unknown …"}
```

修复后（`E8-lived-main.log`，exit 0）：

```
[PASS] 子代理调用被官方原文拒绝（拒绝语随子代理回答回到主会话）
  — {"text":"SUBAGENT-TOOL-RESULT-BEGIN Error: Subagents cannot read the user's terminals, which
     belong to the main conversation; report what you need to the main agent instead. …"}
[PASS] 子代理没有读到主会话的终端 roster（官方禁止的那件事没发生）
```

单测侧：`cmd/dshd/terminal_bridge_test.go` 的 `TestTerminalSessionOriginUsesRealChildLineage`（真 `agent.ChildSpawn` 落盘真 v4 帧 + `FindChild`）与 `TestTerminalPanelToolsReadRealRosterInProcess`（子代理拒绝 + 数据源调用次数为 0）。

---

## 7. 改动清单

### 7.1 任务书列出范围内的

| 文件 | 性质 | 内容 |
|---|---|---|
| `cmd/dshd/terminal_bridge.go` | **新增** 327 行 | 父进程半边（`installUserTerminalBridge` / `serveUserTerminalBridgeLine` / `writeReply`）+ 子进程半边（`provideUserTerminalBridge` / `deliverUserTerminalBridgeResponse` / `terminalSessionOrigin`）+ 子代理 ctx 标记 + 同进程形态 `provideUserTerminalSource` |
| `cmd/dshd/terminal_bridge_test.go` | **新增** 322 行（5 条用例） | 真 `WatchSession` 逐行分发 → 真适配器 → 应答写回；跨会话拒绝；没装桥不吞行；真子会话谱系；同进程形态端到端 |
| `internal/terminal/bridge.go` | **新增** 435 行 | 帧协议（tag/请求/应答/编解码）+ `BridgeServer` + `BridgeClient`（满足 `terminal.UserTerminals`）+ 失败语义 |
| `internal/terminal/bridge_test.go` | **新增** 436 行（15 条用例） | 协议往返、行数收敛、缺 id/未知 op/坏帧、跨会话、无数据源、无桥、写失败立即报、超时、迟到应答、缺 page 协议违约 |
| `modules/tool/terminalpanel/config.go` | 改（+81 行） | `ModuleID` 常量；`EnvReadUserTerminals`/`EnvMaxReadBytes`/`EnvTimeoutMs`；`ConfigFromEnv`（非法值报错）；`ModuleFromEnv`（配置非法让**装配失败**） |
| `modules/tool/terminalpanel/module.go` | 改 1 行 | `ID: ModuleID`（id 只有一个出处） |
| `modules/tool/terminalpanel/config_env_test.go` | **新增** 120 行（5 条用例） | 默认值 / 三个键 / 垃圾值报错 / `readUserTerminals:false` 两个工具都不在 / 坏配置让装配失败 |
| `cmd/dshd/main.go` | 改 | ① `builtinModules` 注册 `terminalpanel.ModuleFromEnv(os.Getenv)`；② `cmdRun` 里 `termStdio := userTerminalsViaStdio()` + 把它并进 `startStdinDispatcher` 条件 + `provideUserTerminalBridge(root, *home)`（**无条件挂**：没桥也挂，否则子代理拒绝会退化成"读不到"） |
| `cmd/dshd/serve.go` | 改 5 行 | `officialJT` 造好后 `installUserTerminalBridge(broker, officialJT, *home)`（父进程装桥的唯一接线点） |

### 7.2 任务书清单**外**的 7 个文件（Lead 已接受为"接线必需的最小 additive"，逐条说明）

| 文件 | 加了什么 | 少这一行为什么桥就断 |
|---|---|---|
| `cmd/dshd/serve_approval.go` | `WatchSession` 里 1 个分支：`if serveUserTerminalBridgeLine(b, sessionID, line) { continue }`（+4 行注释） | 子进程 stderr 的**唯一读端**就是这个逐行分发。不加这一行，请求没人接（工具只会等到超时） |
| `cmd/dshd/approval_stdio.go` | stdin 分发器 1 个 case：`case strings.HasPrefix(line, terminal.BridgeResponseTag): deliverUserTerminalBridgeResponse(line)`（+ import） | 子进程的 stdin 也只有一个读端（"所有父→子请求共享唯一 stdin 读端"，见该文件注释）。不加这个 case，应答被当未知行丢掉 |
| `cmd/dshd/serve_tools.go` | 起工具子进程时 `cmd.Env = append(cmd.Env, "DSHD_USER_TERMINALS_STDIO=1")`（+3 行注释） | 子进程据此才把 stderr 当通道。缺它 → 真机红②（`ErrBridgeUnavailable`）。**该文件另有并行任务的改动**（`settleTurnEndPayload`），与本任务无关 |
| `cmd/dshd/serve_loop.go` | 同上，`runProviderLoop` 那条 spawn 路径也补同一行 | 走 provider 路由的轮次不补就会静默没有桥（"某一种轮次可用、另一种不可用"是最难查的形态） |
| `cmd/dshd/subagent.go` | `loop.Run(withSubagentCallContext(ctx), …)`（1 行 + 3 行注释） | 见 §6：缺它子代理会**真的读到**用户终端（官方禁止）。这是本次发现并修掉的真缺陷 |
| `dsh.toml` | 1 条 `[[module]] id="tool-terminalpanel" backend="static"` | manifest 在场时是**白名单**：不在里面就整模块被跳过（`internal/kernel/module.go:120-130` 只打一行 Warn）。不加 → 真机工具表里没有这两个工具 |
| `cmd/dshd/testdata/tools.snapshot.json` | `-update` 重生成 | R4 快照门。注意：该文件**同时包含并行任务已落地的工具**（`office_*`、`schedule_view/edit`、`interrupt_agent` 移除），我的增量只有 `user_terminal_list` / `user_terminal_read` 两条（逐条 diff 已核对：added = 7、removed = 1，其中 5 个 added 属并行任务） |

另有 `cmd/dshd/snapshot_test.go` 与 `cmd/dshd/tool_budget_test.go` 的改动，见 §8.1（Lead 拍板的判据修正）。

### 7.3 没有改的

`internal/transport/server.go`（**没有**新方法路由——这两个是工具不是方法，rc.2 的 `terminal/*` 方法集合里没有 `user` 前缀者，见 C2 报告 E17）、`internal/transport/handlers_terminal_panel_rc2.go`（复用 C2 的适配器，未改）、AGENTS/CHANGELOG/docs（未改）。

---

## 8. 需要登记 / 未实现

### 8.1 工具表门禁的两条判据修正（Lead 2026-11-12 已拍板）

| 门 | 改法 | 依据 |
|---|---|---|
| `TestEveryToolHasDescription`（120 字上限） | 只约束**宿主自撰**描述；**逐字来自官方 catalog** 的描述登记在 `officialVerbatimDescriptions` 里并豁免长度（日志会打印官方出处） | 官方 rc.2 自己就发 298 字（`tool-catalog.md:1528`）与 173 字（`:1541`）。把"照抄官方"判红等于逼我们篡改官方文案，违反"素材复用/官方形状"（AGENTS.md §7）。"逐字"由 `modules/tool/terminalpanel/tools_test.go` 的 `TestDescriptionsMatchCatalog` 钉住 |
| `TestToolTableTokenBudget`（6000 → **6400**） | 上限提到 6400；注释写清"随**官方工具数**走，不是给我们自己加功能" | 官方 rc.2 工具面比 rc.1 净增 7 个（`team_task_*`、`office_*`×3、`user_terminal_*`×2、`schedule_view/edit`），都有 catalog 依据。现值 **64 个 / 22996 字节 / 估算 6131 token**（真机同树实测） |

> **需要 Lead 同步的文档**：`PLAN.md` §15 与 `docs/ci-rules.md` 第 6 条仍是 6000，需写成 **6400**（这两个文档归 Lead，本任务未改）。

### 8.2 未实现（如实登记，不假装）

| # | 缺口 | 影响 | 何时能补 |
|---|---|---|---|
| G1 | 两个工具的配置来自**宿主环境变量**，不是官方 config 树（`config.patch.*`） | 运维要按文档设 `DSHD_TERMINAL_PANEL_*`；官方 profile patch 里写 `readUserTerminals:false` 在宿主里还不生效 | 需要"manifest/config 树 → 模块配置"的下发通道（`kernel.Module.Config` 现在不传给 `Register`），属配置层任务 |
| G2 | 没有前台进程探针 → `running` 恒 `unknown`（非 running 状态给 `no`） | 模型无法从列表判断"前台有没有命令在跑" | 需要 ConPTY 侧前台探测（C2 §7 G1，未变） |
| G3 | 没有屏幕模型 → "渲染屏幕"是"有界滚动缓冲尾部按换行分页"（真机输出里能看到 ANSI 序列） | 全屏程序（vim/top）读出来是控制序列原文 | 需要 `TerminalScreenPage` 的生产方（C2 §7 G2，未变） |
| G4 | `control` 恒 `user`（没有 agent 终端创建车道） | 筛选逻辑按**值**写，接入 agent 终端时工具侧一行不用改 | 等 `openForAgent` 车道（C2 §7 G4，未变） |
| G5 | 桥的请求/应答都**不压缩**，单帧上限受父进程 stderr 扫描器 1 MiB 限制 | `read` 的应答走父→子 stdin（子进程扫描器 8 MiB，`maxReadBytes` 默认 256 KiB，够）；但如果将来父进程要读子里写的大帧就会撞上限 | 需要时给帧加分片（当前 `lines` 已被 `maxBridgeReadLines=100000` 与工具 `maxReadBytes` 双重夹住） |
| G6 | 同进程形态今天**没有生产入口**（PTY 控制器只在 `cmdServe` 造） | 不影响的真机链路；接线点留给测试/将来 in-process 组合 | 若将来 serve 支持"进程内跑工具"，直接复用 `provideUserTerminalSource` |

### 8.3 门禁结果（本次会话实测）

```bash
$ export PATH="/c/Program Files/Go/bin:$PATH"
$ go build ./...                                                  # EXIT=0   E8-gate-target.log
$ go vet ./...                                                    # EXIT=0   E8-gate-target.log
$ go test -count=1 ./cmd/dshd -run 'TestEveryToolHasDescription|TestToolTableTokenBudget|TestToolTableSnapshot|TestWriteToolsDeclareCaps'
ok  dshgo/cmd/dshd  0.169s                                        # EXIT=0
--- PASS: TestToolTableSnapshot / TestWriteToolsDeclareCaps / TestEveryToolHasDescription / TestToolTableTokenBudget
    豁免日志：工具 "user_terminal_list" 的描述逐字来自 官方 tool-catalog.md:1528（298 字，逐字）…
    工具表 64 个，22996 字节，估算 6131 token（上限 6400）

$ go test -count=1 ./internal/transport ./internal/terminal ./modules/tool/terminalpanel
ok   dshgo/internal/terminal          8.866s
ok   dshgo/modules/tool/terminalpanel 0.090s
FAIL dshgo/internal/transport        6.550s   # ← 1 条红，见下（并行任务）
```

`./internal/transport` 的唯一红：`TestOfficialForgeLiveshareAllReturnNotImplemented`（"测试数据应覆盖 31 个方法，实际 32"）。**不是本任务引入**：该测试与它依赖的 `internal/transport/handlers_forge_liveshare_official{,_test}.go` 是并行 RC2-transport 任务正在改的文件（`git status` 里这三个文件都是 `M`），本任务的改动全部落在 `cmd/dshd/`、`internal/terminal/`、`modules/tool/terminalpanel/`、`dsh.toml` 与 testdata。

`./cmd/dshd` 全量：`tmp/GO8-RC2/logs/E8-test-cmddsh-full.log`（收尾时跑的完整包，见该日志与 §7.1）。

---

## 9. 复现步骤（从零到真机绿色）

```bash
cd .agents/worktrees/dsh-bdb66fbf-95ba
export PATH="/c/Program Files/Go/bin:$PATH"

# 包级：协议 + 接线 + 配置
go test -count=1 ./internal/terminal/ ./modules/tool/terminalpanel/ ./cmd/dshd/ -run 'TestBridge|TestTerminal|TestConfigFromEnv|TestModuleFromEnv|TestToolTable'

# 真机：真 serve + 真 PTY + 真子进程（模型替身只替模型）
go build -o "/c/Users/Administrator/AppData/Local/Temp/e8-bin/dshd.exe" ./cmd/dshd
python tmp/GO8-RC2/e8/real_e2e.py --bin "/c/Users/Administrator/AppData/Local/Temp/e8-bin/dshd.exe" --scenario main
python tmp/GO8-RC2/e8/real_e2e.py --bin "/c/Users/Administrator/AppData/Local/Temp/e8-bin/dshd.exe" --scenario disabled
```

真机跑完不留进程：驱动脚本在 `finally` 里 terminate 自己起的 serve 与模型替身，并删掉临时 home/cwd（`--keep` 才保留）。

### 证据文件索引

| 文件 | 内容 |
|---|---|
| `tmp/GO8-RC2/logs/E8-lived-main.log` | main 场景 11/11 + `MAIN_EXIT=0` |
| `tmp/GO8-RC2/logs/E8-lived-main-tools.txt` | 三个工具结果原文（list/read/subagent） |
| `tmp/GO8-RC2/logs/E8-lived-main-history.json` | 该轮的 journal 帧（含 `tool/call`、`tool/result`、`turn/end`） |
| `tmp/GO8-RC2/logs/E8-lived-main-serve.log` | serve stderr（含 `@@USER-TERMINALS@@` 帧回显、`child(stderr)` 行） |
| `tmp/GO8-RC2/logs/E8-lived-disabled.log` / `-tools.txt` / `-tooltable.json` | disabled 场景 3/3 + 61 工具表 |
| `tmp/GO8-RC2/logs/E8-red-no-env-tools.txt` | 红②：工具真执行但结构化报"没有父进程终端 roster 桥" |
| `tmp/GO8-RC2/logs/E8-gate-target.log` / `E8-gate-pkgs.log` | build/vet/目标测试与三个包的门禁输出 |
| `tmp/GO8-RC2/e8/stub_llm.py` / `real_e2e.py` | 模型替身 + 真机驱动（脚本本身在 tmp，不进 build） |
