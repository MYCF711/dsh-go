# AUDIT-HOST-KNOWN-ISSUES · 宿主/GO.1 前子代理交付的漏登记项

> 模型 `group/deepseek-v4.1-flash-1017` · **只读**：未改产品/文档/任务表，未 commit，只写本文件。判定 **2 BLOCKER + 4 P1 + 6 P2**。
> "表" = `LEAD.md` / `PLAN.md` / `UIPLAN.md` / `docs/go1-lead-execution-task-table-2026-11-07.md`（下称 task-table）。TUI 侧已由 `$G/review/AUDIT-TUI-KNOWN-ISSUES.md` 覆盖，本文件不重复。

## 原样运行输出（本机 2026-11-07 08:4x，go1.27.3 windows/amd64）

```console
$ export PATH="/c/Program Files/Go/bin:$PATH"; cd "/d/DeepSeek Harness/default-workspace/DSH-GO"
$ go test ./internal/transport -run 'TestServerLog|TestAPI' -count=1
ok  	dshgo/internal/transport	0.100s
$ go test ./internal/llm ./internal/prompt ./cmd/dshd -run 'TestResolveModelCapacity|TestACPUndeclaredCustomModelUsesDecimalCapacityDefaults|TestGlobalAndWorkspaceRulesHaveIndependentBudgets|TestDefaultLoopRunnerSessionPermissionOverrideLifecycle|TestServeDelete|TestCancelTimeout' -count=1
ok  	dshgo/internal/llm	0.045s
ok  	dshgo/internal/prompt	0.043s
ok  	dshgo/cmd/dshd	0.480s
$ go test ./modules/fs/local -count=1        # 共享树现状（含未跟踪 tilde_test.go）
ok  	dshgo/modules/fs/local	0.248s
$ grep -c "KNOWN-ISSUE" LEAD.md; grep -c "P1\b" LEAD.md; grep -c "HOST-" docs/go1-lead-execution-task-table-2026-11-07.md; grep -c "HOST-" UIPLAN.md
1
0
0
0
```

独立副本探针（`C:/Users/Administrator/AppData/Local/Temp/audit-host-tilde`，`replace dshgo =>` 共享树，只读引用；root=`<tmp>/workspace`、home=`<tmp>/home`，`local.New(root, local.WithHome(home))`）：

```console
$ go run .
workspace root = C:/Users/ADMINI~1/AppData/Local/Temp/tilde-probe1770934671/workspace
host home      = C:/Users/ADMINI~1/AppData/Local/Temp/tilde-probe1770934671/home
Resolve("plain.txt") -> C:/Users/ADMINI~1/AppData/Local/Temp/tilde-probe1770934671/workspace/plain.txt  [inside(plain.txt)]
Resolve("~/escaped.txt") -> C:/Users/ADMINI~1/AppData/Local/Temp/tilde-probe1770934671/home/escaped.txt  [OUTSIDE(../home/escaped.txt)]
Resolve("../outside.txt") -> err=路径 "../outside.txt" 越出工作区 C:/Users/ADMINI~1/AppData/Local/Temp/tilde-probe1770934671/workspace；只允许操作工作区内的文件
Write("~/escaped.txt") -> <nil>; home 内文件内容="written outside workspace" readErr=<nil>
```

## BLOCKER

### B1 · `~` 是未登记的工作区越界**写**通道，而它被记成"REJECTED 对照补丁"
- `modules/fs/local/local.go:160-172`：命中 `~`/`~/` 后 `if withinHome(slash, home) { return slash, nil }` —— **不再过 `f.within`**；`:184` 的契约文案却仍是「只允许操作工作区内的文件」。
- `cmd/dshd/main.go:102`：`local.Module(wd, local.WithExtraReadRoot(filepath.Join(home,"artifacts")))` —— 生产装配**不注入 `WithHome`**，home 缺省 `os.UserHomeDir()`＝真实 `C:/Users/Administrator`。故 `write`/`edit` 传 `~/.ssh/authorized_keys` 之类可落到工作区外。
- `modules/fs/local/tilde_test.go:13-14`（root=`…/workspace`、home=`…/home`，不同目录）+ `:75-84` 断言 `Write("~/created/new.txt")` 成功且文件落在 home —— 越界被写成**通过条件**；该测试文件还是 `??` 未跟踪。
- `LEAD.md:103`：「补丁与共享树既有 `~`→home 通道冲突…保留现状不覆盖」；`PLAN.md:402` W23 DONE：「本地文件前导 `~`/`~/` 展开且**保持沙箱边界**」；`docs/tilde-paths.md:24,27`：「home 通道只由显式 tilde 触发…可以经该受限通道解析」「`Write` 继续共用 `Resolve`」。三份文档三种口径。
- 唯一闸门是 `Caps{Writes}`→审批，而 H13 已有「以后都允许」（`PLAN.md:384`）→ 一次授权后 `~` 可永久静默越界。
- 建议：**BLOCKER**。LEAD 开 KNOWN-ISSUE 行并写决定行：(a) 落 `host-patches/HOST-FS-TILDE.patch`（展开后仍走 `within`）；或 (b) 认定 home 可写 → 改 `local.go:184` 契约文案 + 审批记忆不得覆盖 `~`。不得继续以"REJECTED / 仅作对照"结案。

### B2 · 宿主 lane 不进封版门：`KNOWN-ISSUE`=1、`P1`=0、task-table 的 `HOST-`=0
- `LEAD.md:86` 是唯一 `DONE (known issue)`（行内注解，无严重度、无阻塞判定）；`LEAD.md` 中 `P1` 计数 **0**；`task-table` 与 `UIPLAN.md` 中 `HOST-` 计数 **0**（上节原样输出）。
- `AGENTS.md` §5（2026-11-07 新增）：出现「仍/边界/UNVERIFIED/known issue/降级/临时」必须"在 `LEAD.md` 建 `KNOWN-ISSUE`/`P1`/`BLOCKER` 行，写 owner、证据、是否阻塞 GO.1"。当前 11 条 `HOST-*`（`LEAD.md:90` + `:102-111`）一条都没进。
- 后果：`task-table:72` T12 的审核面只有 patch 面/行账本/TUI 证据；T11 full 全绿也不代表宿主缺陷关闭。
- 建议：**BLOCKER（流程）**。封版前先建 §2.3 `KNOWN-ISSUE` 表，把本文件各项逐行入表并写明阻塞/不阻塞。

## P1

### P1-1 · HOST-STDERR-NOISE 记 DONE，但无 artifact、无 hash、不进任务表（Q3）
- `LEAD.md:90` 只有「成功调用默认静默；非 2xx 打 `status`；`DSHD_TRACE_API=1` 才打成功；transport test=0 build=0」——无命令、无原始输出、无证据路径；`$G/host-patches/` 只有 FS-TILDE / MODE / TOOL-SAFETY 三份 RESULT。
- 实现确实在位（我实跑绿）：`internal/transport/server.go:376` 非 2xx 打 `status`、`:392` 仅 `apiTraceEnabled()` 时打成功、`:1141` 读 `DSHD_TRACE_API`。
- 但矩阵用的 dshd 不含它（`review/AUDIT-TUI-KNOWN-ISSUES.md:39`：两个二进制 `grep DSHD_TRACE_API`=0）→ 该 DONE 未覆盖任何真实二进制。
- 建议：**P1**；补 `HOST-STDERR-NOISE-RESULT.md`（命令/退出码/原样输出），重编 dshd 后重跑矩阵。

### P1-2 · 四项"READY"其实已实现，且只存在于工作树（Q4）
- `LEAD.md:105-108` 全为 `READY | —`；实际：X1＝`cmd/dshd/serve_loop.go:240-242` 每轮读同一 store（`docs/serve-lifecycle.md` 详述）；S1＝`cmd/dshd/serve_lifecycle_test.go` 的 `TestServeDeleteWaitsForLiveToolBeforeRemoveAll`/`TestServeDeleteTimeoutKeepsSessionAndLiveTurn`；A2＝`internal/prompt/budget_test.go:43`；M3＝`internal/llm/capacity.go`+`cmd/dshd/acp_capacity_test.go`。上节实跑三个包均 `ok`。
- 其中 `internal/llm/capacity.go`、`internal/prompt/budget_test.go`、`cmd/dshd/serve_lifecycle_test.go`、`docs/serve-lifecycle.md` 全是 `??` 未跟踪 → HEAD 单独编译不含这些能力，且账本写 READY 会诱导重复派单（违反 `LEAD.md` §3.4）。
- 建议：**P1**；改 `DONE (working-tree)`，列出 4 个 untracked 路径，先 commit 再派新任务。

### P1-3 · HOST-ATTACHMENT-50M 只有 READY，没有 P1/阻塞判定（Q4）
- `cmd/dshd/attachments.go:30` `attachmentMaxBytes = 8 << 20`；`../dsh-ui/internal/apiclient/attachments.go:147` `attachmentGetResponseLimit = 12 << 20`，注释写死「宿主允许 8 MiB 原始附件」。
- 上游要求 50 MiB 且两份审计已判 **P1**：`review/PLUGIN-AUDIT-COMPOSER.md:114,117`、`review/PLUGIN-AUDIT-TUI.md:113`；`LEAD.md:109` 仅 `READY`＋「UI 响应上限 `12<<20` 同步核」。
- 建议：**P1**；写清"是否阻塞 GO.1"，并注明两仓必须同轮改（否则 50 MiB 必红）。

### P1-4 · HOST-MODE-ADMISSION「DONE (test-only)」双义：DONE 但零产物落地，覆盖实际由 W18 提供
- `LEAD.md:104`：「补丁仅 rename + 2 条守卫测试…**未应用到共享树**」；`cmd/dshd/mode_admission_test.go` 不存在；`cmd/dshd/acp.go:285` 字段仍是 `view`（patch 的 `visible` 未落地）。
- 覆盖没有缺口：`cmd/dshd/tool_execution_guard_test.go:187`（直接调用拒绝，Exec=0）、`:207`（workflow 嵌套拒绝）已钉住同一语义。
- 建议：**P1**（状态失真）；改口径为 `DONE (no-op；W18 已覆盖)` 并归档 patch，或把 2 条测试并入 W18 文件——不要让"patch 未应用"以 DONE 结案。

## P2

- **P2-1 · 第三份参数判据放行 `null`（HOST-TOOL-SAFETY 已披露，未进表）**：`cmd/dshd/tool_merged.go:326-332`「`len(strings.TrimSpace(string(args))) > 0` 才 Unmarshal，`in==nil` → 空 map」→ `null` 放行；调用点 `tool_merged.go:444`，与 agent/pipeline 两份判据不同源，故 `HOST-TOOL-SAFETY-RESULT.md:47,53` 的「判据只有一份」不成立（`RESULT.md:265-269` 自认未改）。→ P2。
- **P2-2 · HOST-TOOL-SAFETY 剩两项未验**：真实链未跑（`RESULT.md:270-273`：真实网关吐 malformed arguments、真实 `web_fetch` 404 正文）、`-race` 未跑（`:264`）；`go test ./...` 只有「51 个包 ok」摘要，原始日志写在 `/tmp/host-toolsafety-fulltest.log`（Windows 不可复核）。→ P2。
- **P2-3 · HOST-FS-TILDE 的 8 条未验随 patch 一起被 REJECTED 带走，全部未登记**：`RESULT.md:204-213`（`~`×glob 交互、UNC/`\\?\`、home 是 junction、`-race`、`extra` 读写不对称）。其中"`home/artifacts` 作 extra 却可写"是既有越权面：`cmd/dshd/main.go:102` + `local.go:152`（Read/Write 共用 `Resolve`），`RESULT.md:208` 已指 `tools-runtime.md §4`。→ P2。
- **P2-4 · W167-RESUME-LOAD 记 `DONE (driver) | Host 无缺陷`，同报告证实启动时 `session.load` 被调两次**：`LEAD.md:89` 与 `review/W167-RESUME-LOAD-DIAG.md:16`（`rc/src/backends/dsh-go/auth.js:41` 一次、`backend.js:1007` 一次）；该重复 load 不在任何表。→ P2（归 TUI，非宿主缺陷）。
- **P2-5 · HOST-PLUGIN-MANAGER-022 / HOST-SETTINGS-SCHEMA 仍 READY，UI 侧审计的 P0/P1 结构缺口无任何表行**：`review/PLUGIN-AUDIT-SETTINGS.md:17`（插件富文本详情 MISSING）、`:19`（`settings.section` 注册槽 MISSING，标 **P0（结构）**）、`:20`（11 包 PARTIAL 5 / MISSING 6）；`LEAD.md:110-111` 只写"先核 update/restore/富文本详情"、"agent-loop/shell/subagent/web-search 命名空间"。→ P2。
- **P2-6 · task-table 完全没有 HOST 行，T12/T15 按现表不会审宿主**：`task-table:72`（T12 审核面）、`:75`（T15 汇总只提 W163/W200）；`LEAD.md:90,102,104` 三条 DONE 与任务表脱钩。→ P2（与 B2 同源，单列便于 Lead 改表）。

## 点名回答（Q1–Q5）

1. **Q1 已做但带限制却没写成 KNOWN-ISSUE/P1**：B1（`~` 越界写入）、P1-3（50 MiB）、P2-2/P2-3（两份 host RESULT 的未验项）；TUI 侧 `W170-BLANKBAND`／空白带的不完整登记见 `AUDIT-TUI-KNOWN-ISSUES.md` P1-5，本文件不重复。
2. **Q2 三个边界**：`HOST-FS-TILDE` **不清**（B1，三份文档三种口径，且无人认领残留）；`HOST-MODE` **不清**（P1-4，`DONE` 与"未应用到共享树"并存）；`HOST-STDERR-NOISE` **清楚**（默认静默／非 2xx 打 `status`／`DSHD_TRACE_API=1` 才打成功，我实跑绿），缺的是证据与新二进制，见 P1-1。
3. **Q3 声称全绿但缺退出码/真实断言**：有 3 处 —— `HOST-STDERR-NOISE`（`LEAD.md:90` 无 artifact/hash）、`HOST-MODE-ADMISSION`（DONE 但树里零落地）、`HOST-TOOL-SAFETY` §5.3（只有"51 包 ok"摘要，日志路径不可复核）。其余三份 host RESULT（FS-TILDE/TOOL-SAFETY/MODE）先红后绿都贴了原样输出与退出码，合格。
4. **Q4 六项是否被漏**：**没有一项彻底漏掉**（`LEAD.md:105-111` 都有行），但**没有一项有 P1/KNOWN-ISSUE**；其中 X1/A2/S1/M3 已实现却写 READY（P1-2），50 MiB 与 plugin/settings 只有 READY（P1-3/P2-5）。
5. **Q5**：本文件每项均给 `文件:行` + 原文摘录 + 建议状态；未列的任何项＝无证据，不据此断言。

## 未验项

- 只读：未跑 PTY、未 compile 产品、未改源码；结论来自源码、既有 artifact 与上节实跑。
- `~` 在真实 `dshd` + 真实 home 下的一次真实工具调用未跑（只需 `write` 工具传 `~/x` 并查看落盘位置；走审批，需独占会话）。
- 未核 `CHANGELOG.md` / `HANDOFF.md` / `MAINTENANCE.md` 是否另有宿主登记。
