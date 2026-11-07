# TUI `/view` 能力只读审计（W127）

> 日期：2026-11-05。本文是证据审计，不是 `/view` 实现任务书。
> 本任务只写本文；未改 `src/dsh-go/backend.ts`、其他 TUI 源码或 DSH-GO 产品代码。

## 0. 结论先行

1. **在固定上游 dsh-TUI `1bd7cb515b4c6fb52f049f7bde521df48ee2f54b`（包版本 `0.18.0`）及其官方 DSH `0.2.1-rc.5` 依赖组合中，未找到 slash `/view` 的命令注册、执行 handler 或“要求 `view edit/edit_file`”的官方规格。** 因而目前不能给 `/view` 赋予“官方语义”，也不能据此立项写代码。
2. 上游确实存在官方工具 `str_replace_editor`，其 `view` 子命令读取文件/目录，`create`、`str_replace`、`insert` 子命令修改文件。**这是模型工具的 `command: "view"`，不是用户输入的 slash `/view`。** 二者不得混写。
3. 当前缺的不是第二套文件编辑器：
   - 上游后端中立层没有 `files/view/edit/edit_file` 能力，`commands` 也只有 `list()`，没有通用 `execute()`；
   - 当前 dsh-go 没有后端原生命令目录/分发，也没有调用 `fs.read/fs.write`；
   - 宿主已经有 `read` / `edit` / `write` 工具、`modules/fs/local` 以及真实 `fs.read/fs.write` Transport；
   - **若需求是模型侧的 `edit/edit_file` 逐字替换语义，当前另缺一条“会话上下文 + 审批完整”的受管工具执行 Transport**；`fs.write` 不能冒充它。
4. **当前最小且可验收的接线是保持 `/view` 不宣传、不冒充可用，产品代码零改动。** 先补原始命令、完整报错和真实官方 `file:line`。第 6 节只给补证后的条件式接线形状，不是现在可派发的实现任务。

Lead 对 `session-191da5ac` 的现场复核也得出了同一边界：阶段归因曾写“官方要求 view edit（`edit_file`）”，但原始命令行、完整报错和截图已不可读，故只能作为待证实线索，见 `docs/tui-view-session-note.md:3-10`。

## 1. 审计基线与坐标

- `U`：固定上游 `ccch1mneyyy/dsh-TUI` rev `1bd7cb515b4c6fb52f049f7bde521df48ee2f54b`，本地只读快照为 `tmp/tui-real-run/upstream-orig/dsh-TUI-1bd7cb515b4c6fb52f049f7bde521df48ee2f54b/`。`package.json:2-4` 为 `@deepseek-harness-tui/dsh-tui`、`0.18.0`。
- 版本号复核：GitHub `main=90333ade3c2285edf9f9150f041707f78d0c2085` 的 `package.json` 仍为 `0.18.0`，其 parent 正是 `1bd7cb5…`；没有更高版本号需要替换固定审计基线。因此官方行号仍取 `U`，不拿当前工作副本冒充官方。
- `T`：当前工作副本 `../dsh-TUI-upstream-src/`。该目录没有 `.git`，所以当前行号只表示本次读取的工作树，不伪造 rev。
- `E`：当前组合内官方包 `@deepseek-ai/dsh-tool-str-replace-editor@0.2.1-rc.5`，其 `package.json:2-12` 指向官方仓库 `packages/fs/tool-str-replace-editor`；审计读取其发布产物 `lib/index.js`。
- `G`：当前 DSH-GO 工作树，HEAD `af4a49a2817592d7affb99bb3a16c7e03214ba1c`，但已有大量其他会话改动；下列当前行号均以本次读取为准，不回滚、不覆盖。

## 2. 官方证据：命令、能力与真实执行路径

### 2.1 没有已证实的 slash `/view`

| 证据 | 官方 `file:line` | 能证明什么 |
|---|---|---|
| 内置命令全集 | U `src/commands.ts:60-137` | `LOCAL_COMMANDS` 没有 `view`；`agentview` 是另一条命令，不可缩写成 `/view` |
| 隐藏命令 | U `src/commands.ts:263-276` | 只有 `deepseek`，没有隐藏的 `view` |
| 后端能力总表 | U `src/agent/capabilities.ts:169-175`、`:330-384` | `SessionCapabilities` 没有 `files/view/edit/edit_file` 成员 |
| 原生命令能力 | U `src/agent/capabilities.ts:339-341` | `commands` 只有 `list(): Promise<CommandInfo[]>`，没有 `execute()` |
| 会话提交接口 | U `src/agent/session.ts:64-85` | 后端自有命令若要直接执行，必须由自己的 `submit()` 识别，或经 DSH registry；没有通用文件命令钩子 |

这只是**固定基线内未找到**，不是对所有版本、所有第三方插件作全称否定。要把它升级为确切官方命令规格，必须补出实际注册包及 handler。

### 2.2 DSH registry 命令与后端原生命令是两条路

**DSH registry 路：**

1. U `src/dsh-adapter/channel/skill-catalog.ts:56-78` 把 `ctx.commands.list(agent)` 的描述符合并进目录，标为 `external`。
2. U `src/screens/Chat.tsx:4488-4500` 只对目录里的 `external` 命令调用 `runExternalCommand`；否则返回 `false`。
3. U `src/dsh-adapter/channel/external-commands.ts:202-228` 在授权与身份检查后调用 `service.execute(agent, line, …)`。

dsh-go 是独立 `AgentBackend`，不是这条进程内 DSH `ctx.commands` 组合。仅在 Go 宿主增加同名工具，不会自动在这条 registry 路生成 `/view`。

**后端原生命令路：**

1. U `src/dsh-adapter/channel/core/session-controls.ts:65-79` 调 `session.capabilities.commands.list()`，把结果标成 `skill: true`、`origin: 'backend'`。
2. U `src/components/PromptInput.tsx:2077-2092` 把这类项识别为 `modelRoutedSkill`，不是 registry 的直接执行项。
3. 后端若需本地直接执行，必须在自己的 `submit()` 拦截。官方 Codex 示例在 U `src/backends/codex/session/commands.ts:109-197`：`review/plan/diff/usage` 在 `submit()` 内执行，未识别的 skill 才转成模型输入。

**真实失败分支：只给 dsh-go 增加 `commands.list()`，但不改 `submit()`，不会得到直接文件编辑；slash 文本会沿普通提交路进入模型。** 这是当前接线方案必须显式避免的假绿。

### 2.3 官方确实存在的 `view` 是工具子命令

E `lib/index.js` 给出完整语义：

- `:13-25`：工具描述是 viewing/creating/editing files；文件 `view` 类似 `cat -n`，目录 `view` 列两层非隐藏项；
- `:125-166`：`viewPath` 读文件或目录，并处理 `view_range` 与截断；
- `:181-207`：`createFile` 创建文件，已存在即失败；
- `:209-242`：`replaceInFile` 要求 `old_str` 唯一，进行真实替换；
- `:244-261`：`insertInFile` 真实插入；
- `:263-325`：注册名为 `str_replace_editor`，按 `args.command` 分发 `view/create/str_replace/insert`。

所以，“官方需要编辑源码/文件”只能在这组 **`create/str_replace/insert` 工具子命令**上成立；`view` 本身是读取。没有证据证明 slash `/view` 必须编辑，更没有找到官方 `edit_file` 命令注册。

## 3. 当前 dsh-go：缺的是适配/分发，不是编辑算法

| 当前事实 | 当前 `file:line` | 缺口判断 |
|---|---|---|
| 能力装配只有 `native/permissions/models` | T `src/dsh-go/backend.ts:331-345` | 无 `commands`，也无文件能力可声明；上游本来就没有 files 能力类型 |
| 普通提交直接 `session.prompt` | T `src/dsh-go/backend.ts:396-433` | 没有后端原生命令识别；只加目录会落入上节假绿 |
| Transport 有通用裸参数 `call(method, params)` | T `src/dsh-go/transport.ts:256-288` | 调用既有 `fs.read/fs.write` **不需要改 transport.ts** |
| 当前 backend 未调用文件方法 | T `src/dsh-go/backend.ts:331-478` | 缺文件命令适配层；不是缺 HTTP 客户端 |
| manifest 尚无 `commands` | T `src/dsh-go/manifest.ts:4-11`；正式包 T `src/backends/dsh-go/manifest.ts:4-11` | 若未来确有 backend-native commands，两个包的能力声明需同步 |
| 正式 bundle 是独立文件集 | T `src/backends/dsh-go/index.ts:1-11` | 新适配不能只留在 `src/dsh-go`，正式 mirror 也要由 Lead 串行同步 |

**冲突边界：**`src/dsh-go/backend.ts` 正由另一会话修改 models/placement。本文不建议任何新 owner 并发编辑它；后续若补证成立，也只能由该 owner 完工后，由 Lead 串行集成。正式 `src/backends/dsh-go/backend.ts` 同样不多人并发写。

## 4. 宿主现有实现：什么能复用，什么不能混称

### 4.1 模型工具与 FS：已有真实查看/创建/逐字编辑

- `read`：G `modules/tool/read/read.go:38-44` 定义只读工具；`:61-85` 校验并经 `spi.FS.Read` 读取；`:94-102` 对越过文件末尾的 offset 真报错；`:124-150` 返回行号正文与 ViewSpec。
- `edit`：G `modules/tool/edit/edit.go:34-39` 定义真实 `edit` 工具及逐字替换 schema；`:55-78` 校验参数并检查可写；`:80-110` 处理读取、无匹配、多匹配和写入；`:113-123` 返回替换数与前后字节数。
- `write`：G `modules/tool/write/write.go:31-36` 定义新建/整文件覆盖；`:49-91` 真实写入并返回 created/bytes。
- 唯一受管执行入口：G `internal/spi/tool.go:121-132` 明确必须经 `spi.Runner`，不能直接 `Tool.Exec`；G `modules/tools/pipeline.go:62-117` 执行策略前置、参数对象校验、未知工具失败和工具执行。
- 会话上下文：G `internal/agent/loop.go:119-126` 用 `spi.WithSession` 绑定会话；G `modules/tools/pipeline.go:63-66` 从 ctx 取会话。未来若暴露工具 Transport，必须保留这条链。

`modules/fs/local` 已经实现而不是待写：

- 根边界：G `modules/fs/local/local.go:151-185`；
- 读与观测登记：`:187-210`；
- 未读拒绝、读后变化拒绝：`:212-244`；
- 写入并刷新观测：`:262-277`；
- 换工作区根清观测账：`:85-107`。

### 4.2 Transport `fs.read/fs.write`：真实读写，但不是 `edit` 工具

- 方法存在：G `internal/transport/api.go:70-71`。
- `fs.read` handler：G `internal/transport/handlers_fs.go:23-50`；`fs.write` handler：`:53-88`。空 `content` 是合法清空文件，成功返回 `written:true`，所以它不是只读预览。
- 生产接线：G `cmd/dshd/serve.go:429-434`。
- 生产读取：G `cmd/dshd/serve_session_ops.go:119-151`，有 4 MiB 上限、目录/不存在/过大等分支。
- 生产写回：G `cmd/dshd/serve_session_ops.go:154-172`，直接 `os.WriteFile` 整文件写入，注释明确这是**人手编辑**，故意不套模型的 read-before-write。

**不得混称：**`fs.write` 可以承担“人已经编辑好完整正文后的写回”，但不提供 `edit` 的 `old_string/new_string` 唯一匹配、未读/陈旧版本拒绝、工具审批和 Result.Hint。若在 TUI 内先读全文、再自行实现逐字替换、最后 `fs.write`，那就是新增第二套编辑器，本方案禁止。

**审计附带发现（本次不改）：**G `cmd/dshd/serve_session_ops.go:126-172` 直接使用 `filepath.Abs/os.ReadFile/os.WriteFile`，没有调用 `modules/fs/local.Resolve` 或 `CheckWritable`。因此不能把 `fs.read/fs.write` 描述成已继承 `modules/fs/local` 的根边界和观测策略；尤其不能把 `fs.write` 直接暴露成模型编辑替代品。这是独立安全隐患，需另任务核边界与测试。

### 4.3 当前确实没有通用工具执行 Transport

G `internal/transport/api.go:12-147` 的方法表有 `fs.read/fs.write`，没有 `tool.execute/tool.call`。G `cmd/dshd/main.go:1080-1115` 虽有 CLI `exec --tool` 并复用 Runner，但它是另起进程组装宿主，不是当前已认证会话的 HTTP 能力，也不自动携带 TUI 会话/审批上下文。

所以：

- 做人手整文件写回：复用 `fs.read/fs.write`；
- 做官方工具式 `edit/edit_file`：应复用宿主**现有 `edit` 工具 + 受管 Runner**，而不是直接调 `Tool.Exec`、直接写 FS 或在 UI 重写算法；
- 后一种 Transport 当前缺失，但在没有官方命令证据前不立项新增。

## 5. 真实失败分支与必须保持的行为

| 分支 | 代码证据 | 必须得到的结果 |
|---|---|---|
| 未知名称不是 external 命令 | U `src/screens/Chat.tsx:4488-4500` | 返回 `false`，不能假装执行了文件操作 |
| 只有 `commands.list`，没有 submit 分发 | U `src/dsh-adapter/channel/core/session-controls.ts:65-79`；T `src/dsh-go/backend.ts:396-433` | 文本进入普通 prompt；测试必须防止把它记成直接编辑成功 |
| `fs.read` 未接线/空 path/不存在 | G `internal/transport/handlers_fs.go:23-48` | 分别 501/400/404；不得统一伪造 200 空内容 |
| `fs.read` 目录、超 4 MiB、其他读错 | G `cmd/dshd/serve_session_ops.go:134-150`；G `internal/transport/handlers_fs.go:41-48` | 明确错误；目录/过大不能当空文件 |
| `fs.write` 未接线/空 path/写失败 | G `internal/transport/handlers_fs.go:61-88` | 501/400/500（不存在为 404）；失败不得返回 `written:true` |
| `edit` 未读或读后文件已变 | G `modules/tool/edit/edit.go:71-78`；G `modules/fs/local/local.go:229-243` | 拒绝写入并给可执行 Hint；不能盲目覆盖 |
| `edit` 空 old、无操作、无匹配、多匹配 | G `modules/tool/edit/edit.go:61-69`、`:86-98` | 全部真错误；多匹配只有显式 `replace_all` 才放行 |
| `edit` 写入失败 | G `modules/tool/edit/edit.go:108-111` | 返回错误和 Hint，不能报替换成功 |
| Runner 策略拒绝、参数非对象、未知工具 | G `modules/tools/pipeline.go:85-113` | 在执行前失败；不得绕过审批直接 `Tool.Exec` |
| Transport 非四字段错误体 | T `src/dsh-go/transport.ts:186-208` | 抛 malformed envelope；不能猜成功 |

## 6. 条件式最小接线与文件级 owner

### 6.1 现在唯一 READY 的方案

- **产品代码零改动；保持 `/view` 不出现在 dsh-go 可用能力里。**
- owner=Lead：向 `session-191da5ac` 补原始命令、完整报错、实际 backend、注册包和官方 handler `file:line`。
- owner=本文：冻结否定性证据和后续判据。
- `src/dsh-go/backend.ts` 不属于任何新 owner 的可写集。

### 6.2 若补证是某个真实 DSH registry 命令

复用该真实命令包及其 `ctx.commands.execute` 执行链，先核它对 host/tool 的真实依赖；不得在 dsh-go 另写同名命令冒充官方。owner 应是该命令包/组合根的单一集成人，宿主只在官方 handler 确需而当前缺失时另开契约任务。此路线本身不要求改 `backend.ts`。

### 6.3 若产品另行明确要“后端原生的人手文件查看 + 整文件写回”（derived，不冒充官方 `/view`）

最小 UI 适配可完全避开共享 Transport：

| 文件 | 单一 owner / 顺序 | 职责 |
|---|---|---|
| 新 `src/dsh-go/file-commands.ts` | 未来 UI owner；可独立施工 | 命令目录与 `submit` 分发；经既有 `transport.call('fs.read'/'fs.write')`，不写替换算法 |
| 新 `src/dsh-go/file-commands.test.ts` | 同一 UI owner | 覆盖未识别透传、read/write 成功、400/404/500/501、写失败不清草稿 |
| `src/dsh-go/backend.ts`、两个 manifest、正式 mirror | **Lead 在 models/placement owner 完工后串行集成** | 只接能力装配和 submit 委托；不交给第二个并发写者 |
| `src/dsh-go/transport.ts`、宿主代码 | 无改动 | 通用 `call` 与 `fs.*` 已足够 |

此路线仅适用于完整正文的人手编辑；若需求写清 `edit/edit_file`，不得走这条捷径。

### 6.4 若补证明确要求模型工具的 `edit/edit_file` 语义

这才需要新增一个 additive 工具执行 Transport，且唯一实现必须委托**当前会话的受管 Runner + 现有 `edit` 工具**：

| 文件/文件组 | 单一 owner / 顺序 | 职责 |
|---|---|---|
| `docs/transport-contract.md`、`internal/transport/api.go`、API 快照/golden | 先由唯一契约 owner 串行冻结 | additive 方法名、sessionId、args、Result/Hint/error 形状；不得改 canonical 25 |
| 新 `internal/transport/handlers_tool.go`、新 `cmd/dshd/serve_tool_execute.go` 及各自测试 | 同一宿主 owner | 只做协议适配与 `spi.WithSession`；调用受管 Runner，不实现编辑算法 |
| `internal/transport/server.go`、`cmd/dshd/serve.go` | 同一宿主 owner 串行接线 | 注入真实 runner/审批链；没有 runner 时 501，不降级为裸 Pipeline |
| 新 `src/dsh-go/tool-commands.ts` 与测试 | 后续 UI owner | 命令解析、Transport 调用与错误呈现；零文件替换算法 |
| 两个 backend/manifest/mirror | Lead 最后串行集成 | 避免与 models/placement 并发冲突 |

在契约 owner 证明“serve 父进程如何取得与当前会话一致的受管 Runner、审批如何回到同一 TUI”之前，这条仍是**未就绪设计**，不能写成已完成方案，也不能用 `cmdExec` 另起进程冒充同会话。

## 7. 验证命令与本次原样输出

### 7.1 本次已运行的现有测试

宿主（真实覆盖 edit/read/write/local FS/Transport 的成功与失败分支）：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./modules/tool/edit ./modules/fs/local ./modules/tool/read ./modules/tool/write ./internal/transport
```

原样输出：

```text
ok  	dshgo/modules/tool/edit	(cached)
ok  	dshgo/modules/fs/local	(cached)
ok  	dshgo/modules/tool/read	(cached)
ok  	dshgo/modules/tool/write	(cached)
ok  	dshgo/internal/transport	0.863s
```

dsh-go Transport：

```bash
node --import tsx/esm --test src/dsh-go/transport.test.ts
```

原样输出：

```text
✔ ready line, one-shot connect cookie, bare HTTP params and four-field errors (46.401ms)
✔ WS ping/sub/unsub, gap history, replay-live dedupe, bounded reconnect and A/B watermarks (332.5956ms)
✔ stale_epoch and session/reset reload exact 63-bit epochs and rebase without stale delivery (116.8253ms)
✔ dispose while gap history is in flight suppresses late delivery (81.211ms)
✔ concurrent dispose calls share the socket-close completion barrier (94.1093ms)
✔ real empty load omits lastSeq under Go omitempty and subscribes from zero (23.7046ms)
ℹ tests 6
ℹ suites 0
ℹ pass 6
ℹ fail 0
ℹ cancelled 0
ℹ skipped 0
ℹ todo 0
ℹ duration_ms 839.3973
```

### 7.2 本次静态断言

对固定上游全部 `src/**/*.ts(x)` 检查命令形 `/view` 字面量、`edit_file`、`LOCAL_COMMANDS`、`SessionCapabilities` 主体、官方工具子命令、当前 dsh-go 能力与宿主方法表。宽泛子串扫描会误命中 `sessions/view.js` 路径和 rewind 的 `files` 字段，所以最终判据分别收窄为**命令形字面量**和**接口主体成员**。

原样输出：

```text
official_src_slash_view_literal: PASS (command-like literals=0; path/comment substrings excluded)
official_src_edit_file: PASS (occurrences=0)
official_local_view: PASS (LOCAL_COMMANDS has no view row)
official_typed_file_capability: PASS (SessionCapabilities body has no files/view/edit member)
official_commands_list_only: PASS (commands exposes list(), no execute())
official_tool_view: PASS (view belongs to str_replace_editor tool)
official_tool_mutations: PASS (create/str_replace/insert are tool subcommands)
current_dsh_go_no_commands: PASS (capability initializer has no commands)
current_dsh_go_no_fs_calls: PASS (backend has no fs.read/fs.write call)
host_fs_transport: PASS (fs.read/fs.write exist; tool.execute absent)
AUDIT_SUMMARY OK=10 FAIL=0 TOTAL=10
```

判据自纠：第一版宽泛扫描曾得 `AUDIT_SUMMARY OK=8 FAIL=2 TOTAL=10`；两个 FAIL 都是审计脚本误报（`/view` 命中普通路径/注释、`files` 命中 rewind/context 字段），不是产品失败。最终只采用上面收窄后的结果。

### 7.3 补证后实现必须再跑的命令

UI 适配（文件名按实际选定路线替换）：

```bash
node --import tsx/esm --test src/dsh-go/transport.test.ts src/dsh-go/file-commands.test.ts
pnpm_config_verify_deps_before_run=false pnpm typecheck
```

若新增工具执行 Transport，必须先有名为 `TestToolExecute…` 的真实测试，再运行；0 匹配不算绿：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./internal/transport ./cmd/dshd -run 'TestToolExecute' -count=1
go test ./modules/tool/edit ./modules/fs/local ./modules/tool/read ./modules/tool/write ./internal/transport ./cmd/dshd
```

真实链不能只跑 mock：先 `python scripts/bin-check.py` 确认新二进制，再用新 dshd + 临时 home + 真实 ConPTY TUI 逐条输入已补证的命令，覆盖查看、允许写、拒绝不写、未读、陈旧版本、多匹配、404、500、断线与重连。新探针必须贴本次 `OK=n FAIL=m`，不能以“命令能输入”或 UI 只读预览作为编辑成功。

## 8. 未验证项与禁止结论

- 未找到原始 `/view` 输入和报错，故本文没有复跑一个已证实的官方 `/view`，也不宣称它绝对不存在于所有插件/版本。
- 本次测试证明现有宿主文件工具、FS 和 Transport 层，不证明存在一个上游文件命令，也不证明整链 `/view` 可用。
- 不把 `str_replace_editor.command="view"` 当 slash `/view`；不把 `fs.read` 当编辑；不把 `fs.write` 当 `edit/edit_file`；不新增第二套文件编辑器。
- 不直接 `Tool.Exec`，不绕过 Runner 的审批/沙箱/Hint，不用只加 `commands.list()` 制造“命令已接通”的假象。
- 不修改、不授权并发修改 `src/dsh-go/backend.ts`；任何后续接线均需等当前 models/placement owner 完工后由 Lead 串行集成。
