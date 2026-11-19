# PTC `vm-script` 形态：官方语义与**隔离层级**（宿主）

> 本文是 `internal/ptc/nodeprovider` 的 `executionMode: vm-script` 的说明与**如实登记**。
> 一句话：**我们实现了 `node:vm` 的 Script 求值形态与按 owner 保留的上下文；它给的是上下文
> 隔离，不是安全隔离。** 防护只来自「外部受管进程 + 整棵进程树可杀 + 超时 + 输出/帧/堆上限 +
> 环境变量擦除」，与项目里「沙盒不得冒名」同一条纪律。
>
> 官方基线：`0.2.2-rc.2`；源码对照用解包树
> `tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/dsh-ptc-runtime-node/`（行号即该树内的行号）。

---

## 1. 官方语义（`file:line`）

`vm-script` 不是一个开关，是**两种设置**一起定义的形态：

| 事实 | 官方落点 | 语义 |
|---|---|---|
| 形态取值 | `src/config.ts:10-11` | `executionMode?: 'per-call' \| 'persistent-in-memory' \| 'vm-script'`，默认 `'per-call'` |
| 保留开关 | `src/config.ts:12-13`、`:37` | `scriptPersistence?: boolean`，默认 **`false`**；"Retain a Script process and context per stateOwner; only supported with executionMode: vm-script" |
| 绑定校验 | `src/config.ts:57` | `scriptPersistence` 非 vm-script → 直接抛 `scriptPersistence requires executionMode: vm-script` |
| 形态→格式 | `src/execution.ts:8-12`、`:161-163` | `vm-script` → 程序格式 `script`；`per-call` → `function-body`；`persistent-in-memory` → `repl` |
| 求值器 | `src/evaluators/vm-script.ts:13-26` | `createVmScriptEvaluator`：一个 evaluator = 一个 vm context + 可选 import handler |
| 建 context | `src/evaluators/vm-script.ts:32-42` | 空原型 globals → `createContext(globals)` → 在 context 里跑 `new Script('globalThis')` 拿 realm → **把外层 `globalThis` 的自有属性描述符原样拷进来**（跳过 `global` 与 realm 已有的键）→ `globals.global = realm` |
| 求值 | `src/evaluators/vm-script.ts:51-61` | `filename = resolve(process.cwd(), 'ptc-program.js')`；`new Script(code, { filename, importModuleDynamically })` → `await script.runInContext(context)`；**末表达式的 Promise 会被 await** |
| 顶层 await | `src/evaluators/vm-script.ts:63-72` + `src/evaluators/vm-script-await.ts:193-290` | 若源码含 `await`，先跑 Node REPL 的 TLA lowering（`processTopLevelAwait`，基于 `acorn` + `acorn-walk`，并在 `:255` 处"没有真 await 或出现顶层 return 就不转换"） |
| 非严格 | `README.md:72`、`README.zh.md:72` | "Script syntax is **non-strict** unless the program opts into strict mode" |
| 受管进程 | `src/worker.ts:276-379` | `ctx.subprocess.spawn` + 控制/输出分离管道；argv 走 `ctx.sandbox.confine`（`:287`）；堆上限 `--max-old-space-size`（`:283`）；env 只留启动所需（`:290-292`） |
| 输出账本 | `src/worker.ts:101-104`、`:145-152` | `OutputLedger(maxOutputBytes)`；超了 `output-limit` 并终止 |
| 超时/取消 | `src/worker.ts:126-133` | `setTimeout(spec.timeoutMs)` → `timeout`；`signal` → `abort` |
| 结算后清理 | `src/worker.ts:153-182` | 保留与否：`preserved = retainsState && !discard && kind !== 'output-limit'`（`:163`）；中断时先试合作 revoke（`:203-221`），不成才终止（`:166`） |
| 单飞 | `src/worker.ts:76-79` | `assertIdle()`：同一 owner 并发运行直接抛错 |
| owner 复用 | `src/execution.ts:88-112` | `owners: WeakMap<object, ProgramWorker>`；owner 为 `undefined` → 临时进程；策略变了要求先 release |
| REPL 形态 | `src/evaluators/repl.ts:13-45` | `persistent-in-memory` 用 `node:repl` 的 `useGlobal:false` 上下文，与 vm-script 是**另一个**求值器 |

### 官方自己怎么写隔离层级（原文）

`README.md:72`（vm-script 段末句）：

> Script syntax is non-strict unless the program opts into strict mode;
> **the context is not a security boundary**, and Node access remains subject to the process sandbox.

`README.zh.md:72`（同句中译）：

> 除非程序显式启用严格模式，否则 Script 使用非严格语法；**context 不是安全边界**，
> Node 访问仍受进程沙箱约束。

`README.md:218`（known limitations 第一条）：

> **Confinement inherits the selected backend's limits** — full and partial enforcement are
> reported separately; **sandbox policy and managed-process containment are distinct guarantees**.

`README.md:122`：

> The host owns policy, deadlines, binding lookup and process cleanup. The child owns program
> evaluation and binding proxies; **model-written code is an untrusted peer** even when its
> messages use the expected control descriptor.

Node 官方文档（`node:vm` 首段）也是同一句：该模块 **"not a security mechanism"**，不要用它跑
不可信代码。两层意思是一样的：**vm 给的是上下文隔离，进程与外部限额才是防护。**

---

## 2. 本仓实现了什么

| 层 | 落点 | 行为 |
|---|---|---|
| 形态与配置 | `internal/ptc/nodeprovider/provider.go`（`Config.validate`、`formatFor`） | `per-call` / `vm-script` 可装配；`persistent-in-memory` **装配期拒绝**；`scriptPersistence` 只能配 vm-script（对齐 `config.ts:57`） |
| 格式声明 | `Provider.ProgramFormat()` | `vm-script` → `script`；`per-call` → `function-body` |
| 上下文 | `worker.go` 的 `createScriptContext` | `createContext(Object.create(null))` → realm → 拷外层 `globalThis` 自有描述符（跳 `global` 与 realm 已有键）→ `globals.global = realm`（对齐 `vm-script.ts:32-42`） |
| 求值 | `worker.go` 的 `runScript` | `new Script(code, {filename, importModuleDynamically: USE_MAIN_CONTEXT_DEFAULT_LOADER})` → `await runInContext(context)`；末表达式是 Promise 就等（对齐 `vm-script.ts:51-61`） |
| console | `worker.go` 的 `installScriptGlobals` | context 自带的 console **写不到任何地方**，显式换成捕获 shim（官方 `src/process.ts:115` 同形） |
| 绑定 | `worker.go` 的 `makeNamespace` / `makeErrorClass` | 空原型命名空间；`ErrorClass` 是**真的构造器**（`class extends Error`），`e instanceof ToolCallError` 成立，同时也是 context 里的全局（官方 `src/process.ts:129-134`） |
| 无损完成值 | `worker.go` 的 `assertLossless` | realm 无关的普通对象判定（官方 `workflow-ptc/src/realm.ts` 的 `hasPlainPrototype` 同判据）+ 稀疏数组/符号键/不可枚举键一律 `invalid-output` |
| 保留环境 | `runner.go` 的 `session` + `retained.go` | `scriptPersistence:true` 时按 owner 复用进程与 context；`Release(owner)` 释放；同一 owner 并发运行 → **误用**（`assertIdle`）；超时/取消/超限 → 终止进程并清状态，消息里写明 `retained variables were cleared`（官方 `worker.ts:167-169`） |
| 环境变量 | `worker.go` 的 `clearProgramEnvironment` | 程序可见 `process.env` 只留 `PATH/PATHEXT/SYSTEMROOT/WINDIR/TEMP/TMP`（官方 `src/environment.ts:5,13-19`） |
| 输出归属 | `worker.go` 的 `evaluationContext`（`AsyncLocalStorage`） | 只有当前求值发起的字节算这一轮输出；常驻会话里上一轮留下的定时器输出被丢弃（官方 `src/execution.ts:130` "captured output is discarded"；官方 worker `worker.ts:2` 是同一个机制） |
| 限额与收树 | `runner.go`（`execute`/`terminate`）、`process_windows.go` | 输出字节、单帧字节、V8 老年代堆、elapsed 预算；结束/超时/取消都 `TerminateJobObject` 收整棵进程树 |

JS 仍然只作为**文本**送进外部 node 进程（`worker.go` 的 `workerSource` 是个数据常量，
`New` 时写到临时目录的 `.cjs`），宿主不解释、不链接、不 fork 任何 JS 引擎（R6）。

---

## 3. 隔离层级：做到哪一层、没做到哪一层

| 层级 | 本仓 | 依据 / 说明 |
|---|---|---|
| **进程隔离**（受管子进程、整棵进程树可杀、超时、取消、堆/输出/帧上限） | **做到** | `runner.go:terminate` + `process_windows.go`（Job Object `KILL_ON_JOB_CLOSE`）；`--max-old-space-size`；`OutputLedger` 等价物 |
| **上下文隔离**（独立 global/realm；顶层声明跨次保留；`require` 不可见） | **做到** | `createScriptContext`；`require` 不在 `globalThis` 上，所以 context 里 `typeof require === "undefined"` |
| **环境变量擦除**（程序视野内无凭据类变量） | **做到** | `clearProgramEnvironment`；PATH 等启动路径保留，否则嵌套子进程起不来 |
| **文件/网络 confinement**（按会话档位挡路径） | **没做到** | `SandboxMode()` 报 false，显式档位请求被 `ptc.ResolveSandbox` 拒绝；程序里 `await import('node:fs')` 按**进程权限**生效 |
| **安全边界**（把不可信代码关住） | **没做到，也不是 vm 的承诺** | 官方 `README.md:72` "the context is not a security boundary"；`createScriptContext` 原样拷入外层 `globalThis` 描述符，所以 context 里的 `process`/`Buffer`/`fetch` 就是外层真实对象，`process.exit()` 一样能杀进程 |
| **进程树之外的后代**（真正的沙箱逃逸面） | **没做到** | 与官方同一条已知限制（`README.md:220` "escaped descendants on a fallback platform may remain outside the managed range"）；我们没有 fallback 平台检测 |
| **平台 sandbox runner**（官方 `ctx.sandbox.confine`） | **不做** | 等 OS runner 裁定；见缺口登记 §HOST-PTC |

**结论口径**：`vm-script` 把「程序的作用域」和「宿主的进程」分开，**不**把「不可信代码」和
「宿主的资源」分开。任何"vm 沙箱"的说法都是冒名——测试
`TestVMScriptContextIsScopeIsolationNotSecurityBoundary` 断言的正是它**没挡住**什么
（context 里能拿真实 `process`、能 `await import('node:fs')`），
`TestVMScriptDescriptors` 断言执行说明里**不许**出现 "sandboxed" 之类冒名词。

---

## 4. 与官方的差异（诚实清单，都是"少了什么"而不是"多了什么"）

1. **没有移植顶层 await lowering**：官方 `vm-script-await.ts` 依赖 `acorn` + `acorn-walk`，
   我们不去手写一个 JS parser。裸顶层 `await` 会按 `exception` 失败，消息点名缺口并给出
   可用写法（`(async () => { ... })()` 作为末表达式，运行时会 await 它——这正是官方
   `workflow-ptc` 交给 script 形态的形状，`src/host.ts:293`）。
2. **`persistent-in-memory`（Node REPL）未做**：装配期拒绝，不静默降级。
3. **超时/取消一律清状态**：官方先尝试合作 revoke（`worker.ts:203-221`）再决定是否保留；
   我们的 worker 没有合作停止通道，所以直接终止进程树并写明 `retained variables were cleared`。
   保守方向，不假装保留。
4. **没有 import handler**：官方可注入 runner-local 模块解析（`ProgramImportHandler`），
   我们只有 Node 默认加载器（`USE_MAIN_CONTEXT_DEFAULT_LOADER`）。
5. **没有 `ctx.sandbox.confine`**：官方起进程前先按策略限制 argv（`worker.ts:287`）。
6. **`process.env` 擦除发生在 worker 内**（官方在子进程 env 构造处就过滤，`worker.ts:290-292`），
   效果相同：程序取不到凭据类变量。

---

## 5. 测试与变异

执行面测试在 `internal/ptc/nodeprovider/vmscript_test.go`（无 node 的机器上 `t.Skip`，
跳过 ≠ 通过）：

| 断言 | 钉住的事实 |
|---|---|
| `TestVMScriptDescriptors` | `script` 格式、`RetainsState` 跟随 `scriptPersistence`、执行说明**不许冒名** |
| `TestVMScriptCompletionValueAndDeclarations` | 末表达式完成值；顶层 `return` 不支持的失败归类 |
| `TestVMScriptAwaitsFinalExpressionPromise` | `(async () => …)()` 形状被 await（官方 workflow 交给 script 的正是它） |
| `TestVMScriptBareTopLevelAwaitNamesTheLimitation` | 裸 TLA 失败消息点名 `vm-script-await.ts` 与可用写法 |
| `TestVMScriptNonStrictUnlessOptedIn` | 非严格默认 + `'use strict'` 可选 |
| `TestVMScriptBindingsAndErrorClass` | 绑定往返、错误类是**真构造器**（`instanceof` 成立）且是 context 全局 |
| `TestVMScriptContextIsScopeIsolationNotSecurityBoundary` | 上下文里能拿真实 `process`/`Buffer`、能 `await import('node:fs')`、`require` 不可见 |
| `TestVMScriptInvalidOutputIsRealmAware` | realm 对象判定正确；类实例/稀疏数组/符号键/不可枚举键按 `invalid-output` |
| `TestVMScriptRetainsStateAcrossCalls` | 顶层声明与同一进程跨次保留 |
| `TestVMScriptWithoutPersistenceIgnoresStateOwner` | 默认 `false` 时忽略 owner、每次新进程 |
| `TestVMScriptRetainedConcurrentRunIsMisuse` | 同 owner 并发 = 误用（`assertIdle`） |
| `TestVMScriptReleaseClearsRetainedState` | `Release` 之后状态清空、换进程 |
| `TestVMScriptTimeoutClearsRetainedState` | 超时清状态 + 消息写明 |
| `TestVMScriptExceptionKeepsRetainedProcess` | 程序自己抛**不**清状态（官方只清超时/取消/超限） |
| `TestVMScriptProgramEnvironmentIsScrubbed` | 凭据类 env 不可见、PATH 保留 |
| `TestVMScriptBackgroundOutputDoesNotLeakIntoNextCall` | 上一轮后台定时器的输出**不许**变成这一轮的输出（输出归属） |
| `TestVMScriptOutputLimit` / `TestVMScriptWorkerExit` | 限额与底座死亡归类 |
| `TestStateOwnerKey` | owner 身份语义（Go 无 WeakMap 的等价退化） |

变异（在独占 worktree 里做，逐条造红再复原）见交付报告；判据是"每条断言都能被对应实现点
单独打红"，不是"跑过一遍"。

---

## 6. 三条 PTC PARTIAL 的残余（做完 vm-script 之后）

| 条 | 残余 |
|---|---|
| `ptc-runtime` | 仍有：`persistent-in-memory`（Node REPL）未做；**无文件 confinement**（显式档位被拒）；顶层 await lowering 未移植。**已收窄**：`vm-script` + `scriptPersistence` 真做了（含保留环境与 `Release`）。 |
| `tool-executor-ptc` | 残余与形态无关（不做执行器替换、无 system prompt 的 `tools:sdk` 段、无 `ptc-dispatch` 事件、图片型子结果、`reset(agent)`、沙盒升级审批）；**新增可做项**：`run_code` 目前固定 per-call，尚未把 provider 的 `script` 形态接进来。 |
| `workflow-ptc` | JS 工作流脚本的运行底座**现在有了**（`script` 形态 + 官方 `host.ts:293` 的 `(async () => …)()` 包装形状可直接跑）；仍缺：`workflow` 工具仍走声明式 JSON 步骤表，未把 JS 脚本接到这条 provider 上。 |

---

## 7. 自己复现

```bash
export PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH"
go test ./internal/ptc/... -count=1 -run 'VMScript|Run|New|Descriptors|StateOwner' -v
bash scripts/check.sh              # 全量门禁（含 race-guard）
python scripts/gap-register-counts.py --check
```