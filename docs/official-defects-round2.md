# 官方缺陷名录 · 第二轮逐条结案（rc.5 基线）

> **上一轮的产出**：[`official-defects-catalogue.md`](official-defects-catalogue.md)（26 条，逐条 `file:line` + 原文引用）
> 与 [`official-defects-fixes.md`](official-defects-fixes.md)（已修 2 条 + 4 条「有代价所以不修」的具体代价）。
> **这份文件补的是名录里"只记录、没结案"的 16 条**：每条给出四种判定之一，不留"待评估"。
>
> **基线**：官方 `0.2.2-rc.5`，解包在 `tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/`。
> **不要用 `tmp/GO3-PARAMS/_official/`（rc.1）**，行号与部分取值都不同。

## 这 16 条是怎么选出来的（先纠正上一轮的口径）

上一轮名录自己的汇总把 26 条分成四堆：已修 2（#1 #2）、有代价明确不修 7（#3 #7 #15 #20 #22 #24 #26）、
零收益 1（#16）、**其余 16 条只写了「我们已比官方对 / 无对应」**（#4 #5 #6 #8 #9 #10 #11 #12 #13 #14
#17 #18 #19 #21 #23 #25）。这 16 条就是本轮要结案的对象。

**上一轮转述里的三处误判，本轮已改**（详见下面主表与 §3）：

1. **#12 不是「无对应」**：官方那半（长行让整次搜索失败）我们确实没有，但**同一个位置我们有自己的两处缺陷**，
   而且都是零代价可修的。判定从"不修（无对应）"翻成"**零代价 → 已修**"。
2. **#4 不是「我们的 ExitCode 没有 null 态所以无对应」**：我们有等价态（`-1`），并且**照官方那样把它渲染成了数字字面量**。
   但它的正确修法要动契约（见主表），所以是"有代价"，不是"无对应"。
3. **#10 被误记成「有代价 → 不修」**：我们根本没有临时文件，这条在宿主上**没有对应物**，
   它既不用修、也不该占"有代价"的名额（"有代价"要写清谁付什么，这条没人付）。

## 主表（16 条结案）

判定的四种结果：① 零代价 → 修掉 ② 有代价 → 不修 ③ 不动（官方对/我们本就对/本仓无此路径） ④ 降级（官方这条不算缺陷）

| # | 官方缺陷（一句话） | 本仓落点 | 判定 | 依据（谁付什么 / 在哪一行对上） |
|---|---|---|---|---|
| 4 | `exitCode: null` 渲染成 `[exit code: null]`；反解析 `\d+` 不匹配 → 回落成"干净退出 0" | `modules/exec/bash/tools.go:178`（`ExitCode int`）、`:309`（`退出码 %d`）、`:331`（`exit %d`）、`:664`（`[exit %d]`）；`modules/exec/bash/session.go:140`（`rc := -1`） | **② 有代价 → 不修** | 危险的半条（反解析把"解析不出"当成功）我们**没有**：全仓没有一处把渲染文本反解析回退出码。但"不知道退出码"这个态我们有，就是 `-1`，且它也**被渲染成数字字面量**。想改对必须给 `spi.ExecResult` / `spi.JobInfo` / `spi.JobOutput` 加 `HasExitCode` 布尔并透到 Transport 帧——因为 `-1` 与**真实退出码 `0xFFFFFFFF` 同值**：Go 在 Windows 上 `syscall_wait_windows` 的 `WaitStatus.ExitStatus()` 是 `return int(w.ExitCode)`（`syscall_windows.go:1156`，`ExitCode` 是 `uint32`），进程 `ExitProcess(0xFFFFFFFF)` 读回来就是 `-1`；`os/exec_posix.go:157-161` 又在"进程没起来"时返回 `-1`。**代价 = 一份跨层契约变更 + 所有消费方各加一个分支**，收益 = 一个只在 `Wait` 非 `ExitError` 时才可达的文案。⇒ 不修，登记为契约层待办 |
| 5 | 错误路径丢掉已计费 usage（`EMPTY_RESPONSE` 还在默认可重试集里 ⇒ 每次重试真实计费、6 次尝试一次都不报账） | `internal/llm/client.go:371`（idleErr）、`:373`（裸读错）、`:379`（空响应）、`:382`（流中途断） | **③ 不动（已对齐）** | 四条失败 return 交的都是 `acc.snapshot()` / `snap`，`Usage` 在 snapshot 里一起回去；对照官方正确的那一支 `dsh-llm-pi-ai/src/stream.ts:226`。**别为了"对齐官方"把 snapshot 换成 `nil` 或空 `Response`** |
| 6 | 空白凭据：`describe()` 说已配置 + 给掩码，`resolve()` 说 blank（判据是 `value.length === 0` 而不是 trim 后为空） | `internal/llm/model_catalog.go:394`（stored）、`:399`（env）；`internal/llm/credential_mask.go:44` | **③ 不动（已对齐）** | 两处判据都是 `strings.TrimSpace(...) != ""`，同源同口径；掩码前也再判一次（注释写明"给它一个掩码会让界面把未配置显示成配过"）。**别把 trim 判据退回去** |
| 8 | `apply_patch` 每次应用静默吃掉文件末尾一个空行（`split` 只 pop 一个尾空元素、收尾只补一个） | 本仓无此路径：`modules/tool/` 下没有 patch/apply/hunk 工具（`ls` 可核） | **③ 不动（无此路径）** | 我们没有 hunk 补丁工具，`edit` 只做文本替换（行尾问题已在 #1 修掉）。**规则留档：将来实现补丁工具时用 `hadTrailingLF` 布尔，别抄 `pop()/push()`** |
| 9 | Windows 写已有文件走 `ReplaceFileW` 遇瞬时共享冲突（杀软/索引器/git 持有）直接失败并报成永久 `FS_PERMISSION_DENIED`；同一产品另一条写入路径对这些错误码有 8 次 20→200ms 退避 | `modules/fs/local/local.go:435`（单次 `os.WriteFile`，无原子性、无重试） | **② 有代价 → 不修** | 加退避重试 = **真·永久失败时错误晚约 1 秒才报**（8 次 × 20→200ms 递增），只读文件/ACL 拒绝这类"重试也没用"的错要白等；改原子写（temp + rename）在 Windows 上会与"目标正被打开"冲突（`os.Rename` 直接失败），且重建目标会丢 ACL/硬链接。**换来换去 ⇒ 不做**（若将来真加，必须重试与原子写一起上，别只抄一半） |
| 10 | `writeFileAtomic` 的失败清理 `await rm(temp)` 自己可能抛错，把原始失败原因整段替换掉 | 本仓无此路径：`modules/fs/local/local.go:435` 没有临时文件 | **③ 不动（无此路径）** | 没有 temp 就没有"清理失败盖掉根因"。**规则留档：实现原子写时 `rm` 必须 `.catch()` 包住并保留 `cause`**（官方自己在 `dsh-fs-local/src/fsio.ts:535` 做了双错因拼接） |
| 11 | `read` 的 offset 越界在**已存在**文件上返回 `FS_NOT_FOUND`（与"文件真的不存在"同一个机器可路由错误码） | `modules/tool/read/read.go:97-101` | **③ 不动（已对齐）** | 越界走普通 error + 可执行 Hint，并且 Hint 里给出**真实行数**与"要读末尾就用 `offset=total`"；错误码不会与"真不存在"混。比官方好，别改回去 |
| 12 | grep 不带 `--max-columns`：`--json` 把匹配行完整文本塞进 `data.lines.text` ⇒ 几条几 MB 的长行就堆过 20MB 原始输出上限 ⇒ **整次搜索失败**，而 `GREP_MAX_LINE_BYTES=2000` 根本没机会执行 | `modules/tool/grep/grep.go:44`（`maxLineBytes=400`）、`:50`/`:74-83`（`previewLine`）、`:252`、`:264-266`（`sc.Err()`） | **① 零代价 → 已修** | 官方那半（整次失败）我们本来就没有：截断发生在收集之前。**但同一位置有两处自己的缺陷**：① `line[:400]` 按**字节**切，中文/emoji 行切成半个字符 ⇒ 非法 UTF-8 ⇒ 投影层把 Data 序列化成 JSON 时变成 U+FFFD（`internal/session/project.go:31`），模型看到"这一行有乱码"而不是"被截断了"；② 截断**不标**，模型会把半截行整行引用。另外探针实测到更贵的一处：**单行 >1 MiB 时 `bufio.Scanner` 直接停，该文件后面的行一条都没扫**，返回 count=0、skipped=0、Hint 还在说「没有命中：确认 base 路径对不对」——把"没扫到"报成"不存在"。三处一起按官方 `previewLine`（`search-core.ts:297-302`，注释写明 "the cut preserves UTF-8 boundaries"）对齐。详见 §1 |
| 13 | 工具超时分类只在下游正常返回时成立：包装层只替换 `next()` 的 resolve、没有 catch ⇒ 任一层抛错就穿透成 `TOOL_ABORTED` | `modules/tools/pipeline.go:575`（到点 `&ToolTimeoutError{...}`）、`:558`（父 ctx 结束原样返回以保住"取消"语义） | **③ 不动（已对齐）** | 我们是**自持 deadline**（goroutine + select），不是"包装 resolve"：预算到点一律 `ToolTimeoutError`，父 ctx 取消走另一条分支，两者在注释里明写要分开。比官方好，别改回去 |
| 14 | compaction 入口在每个 step 全量回扫整个 session log；且昂贵检查排在廉价阈值判断**之前** | `cmd/dshd/main.go:1924`（`runCompactor` 每步 `v4.ReadFile(logw.Path)` 读全量日志 → `session.Surface` → `meter.Estimate`） | **② 有代价 → 不修** | "昂贵检查在前"这条我们不适用（估算本来就依赖 surface，没有更便宜的判据能算同一个数）。但**每步全量重读确实一样贵**（O(日志字节)/step ⇒ O(N²)/会话）。要修得把估算源换成增量面或内存里的 `hist`——那会改**压缩触发时机**：估高 ⇒ 提前压（丢掉还有用的上下文 + 白花一次摘要钱），估低 ⇒ 该压不压（超窗，整轮失败）。没人能证明"只是等价变化" ⇒ 不做，单独登记为 P1 性能项 |
| 17 | `pre-step` 被 `reject` 时已 `claim` 的用户消息被**永久销毁**（`claim()` 先耐久摘队列，之后任何监听者 reject 都不写回、不进日志） | 本仓无此路径：`internal/agent/loop.go:226` 的 `PreStep` 只有 `error` 语义，没有 `{kind:'reject'}`；CLI 在 `Run` **之前**已耐久写 `user/message`（`cmd/dshd/main.go:1096`/`:1098`，`cmd/dshd/checkpoint_cli_test.go:208` 钉着"请求前最后一帧是 user/message"） | **③ 不动（无此路径）** | 我们没有耐久 inbox、也没有"blocked/reject"语义 ⇒ "决定不开步"不可能销毁输入。**规则留档：「决定不开步」不许等于「销毁输入」——将来做 blocked 语义时先写回队列** |
| 18 | 轮次驱动卸载时 `cancel({kind:'parent'})` 不带 `keepInbox` ⇒ `inbox.clear()` 把两条队列全清，用户排队的 follow-up 全消失 | 本仓无此路径：全仓没有 `keepInbox` / `inbox.clear`（grep 0 命中） | **③ 不动（无此路径）** | 同上；我们的 steer 走 `Steers.drain()`（`internal/agent/loop.go:342`），没有"卸载即清队"的路径 |
| 19 | `SessionStore.create()` 的"调用方 fiber 所有权"契约与实现不符（`ctx.effect` 挂在服务自己的 fiber 上） | 本仓无此路径：没有 `SessionStore` / `ctx.effect`；`CreateSession` 返回调用方自持的 `*Log`，由调用方显式 `Close` | **③ 不动（无此路径）** | 生命周期归属本来就是调用方，不存在"承诺了却挂错 fiber" |
| 21 | `turnOutline` 投影无界增长，且每个事件都整体复制一次数组（10,000 轮 = 10,000 条常驻 + 累计 O(N²)，还是可持久化投影） | 本仓无此路径：没有 `turnOutline` 投影（grep 0 命中）；同类 O(N²) 在 #14 的每步 `Surface` 重算里 | **③ 不动（无此路径）** | 不存在的投影没有"无界增长"。**规则留档：将来做列表投影用环形/分块并显式设上限**，别做"每事件整数组复制 + 全量持久化" |
| 23 | pi-ai 把捕获的错误压平成 `error.message`，丢掉 `Error` 与 `cause` 链 ⇒ 只能靠正则猜失败类别（官方自认 `XXX(pi-ai upstream)`） | 本仓无此路径：按 JSON 字段取值（`internal/llm/client.go:379`/`:382` 用结构化 `*StreamIdleError` 与字段判断）；`internal/llm` 里只有 1 个 `regexp`（`model_catalog.go:26`，凭据 ref 的形状校验），没有一个是在猜错误类别 | **③ 不动（无此路径）** | 我们不做"错误字符串→类别的正则猜"，前向兼容 |
| 25 | Win32 错误码在 `CloseHandle` **之后**才 `GetLastError()`（`WAIT_FAILED` 分支被 `closeHandle` 覆写）；`releaseLockHandleWin32` 无论哪步失败都把 syscall 报成 `ReleaseSemaphore` | 本仓无此路径：全仓没有手写 `GetLastError()`（grep 0 命中）；`internal/session/lockread_windows.go:17-22` 直接用 `windows.CreateFile` 返回的 `err` | **③ 不动（无此路径）** | `golang.org/x/sys/windows` 每次调用**直接返回** errno，不存在"锁在线程局部存储里被后续调用覆写"。**规则留档：真要读 `GetLastError` 必须在任何后续 Win32 调用之前读** |

**汇总**：① 零代价已修 **1** 条（#12）；② 有代价不修 **3** 条（#4 #9 #14）；③ 不动 **12** 条；
④ 降级 **0** 条（本轮 16 条逐条核完，没有一条"官方其实没错"；上一轮已结案的另外 10 条本轮不重裁）。

## §1 修了什么（#12，零代价）

**改动**（全部在 `modules/tool/grep/grep.go`，一个文件）：

1. `maxLineChars` → **`maxLineBytes`**（名字骗了人：判据一直是字节）。
2. 新增 `previewLine(line, maxBytes)`：裁点前推过 UTF-8 续字节（`utf8.RuneStart`），超预算就补官方那句
   ` (line truncated)`。逐条对齐官方 `search-core.ts:297-302` + `dsh-output-retention` 的 `trimTrailingPartialUtf8`。
3. 扫描循环补 `sc.Err()`：某行超 `scannerBufMax`（1 MiB）时计 `PartialFiles`，并把一句
   「N 个文件里有一行超过 1024 KiB 的扫描上限，那些文件后面的行没扫到（结果可能不全，别据此断定「不存在」）」
   **顶到 Hint 最前面**（它推翻了后面那句"确认 base 路径对不对"）。

**为什么零代价**：

- **不变量不动**：`maxLineBytes` 仍是 400（字节预算不变，比官方 2000 更靠前）；ASCII 行保留字节数与改前**逐数一致**
  （有测试钉着）；`FilesScanned` / `FilesSkipped` / `Truncated` / 命中数 / 上限语义**一个字没改**
  （`PartialFiles` 是**新增**字段，`omitempty`，不进任何一个既有判据）。
- **没有人变慢**：裁点最多前推 3 字节（一个 4 字节字符的续字节上限），`sc.Err()` 是一次已存在的错误值检查。
- **信息只多不少**：多了一个"被截断了"的标记（官方也有这句）、多了"结果可能不全"的一句话；
  少掉的是**被切碎的半个字符**——那不是信息，是乱码（U+FFFD）。
- **不增交互、不动安全面、不改任何既有断言**：`modules/tool/grep/grep_test.go` 11 条**零改动**通过。

**测试**：`modules/tool/grep/preview_line_test.go`（5 条）+ `modules/tool/grep/partial_scan_test.go`（3 条）
- `TestPreviewLineKeepsUTF8Boundary`：200×「中」= 600 字节，夹具**自证** `utf8.RuneStart(line[400])==false`
  （不成立就 `t.Fatal`，不允许用例空转）→ 断言预览是合法 UTF-8、无 U+FFFD、是原行的连续前缀、
  正文落在 `[397,400]` 且是 3 的整数倍
- `TestPreviewLineMarksTruncation`：裁过的必须带标记；**没超预算的一个字节都不许加**
- `TestPreviewLineASCIIBudgetUnchanged`：ASCII 行保留恰好 400 字节（与改前逐数一致）
- `TestPreviewLineBoundaryEdges`：`maxBytes<=0` 原样返回 / 恰好等于上限不标 / 4 字节 emoji 同样对齐
- `TestGrepLongLineIsValidUTF8AndMarked`：端到端跑 `Exec`，把 `payload` 重新 `json.Marshal`，
  断言**模型真正看到的那份 JSON** 里没有 `\ufffd` 且有标记
- `TestGrepReportsPartialScan`：1.2 MiB 单行 + 下一行 `NEEDLE` ⇒ `count=0` / `PartialFiles=1` /
  Hint 以「1 个文件」开头且**保留**原来那句下一步建议
- `TestGrepPartialScanLeavesNormalScanAlone`：正常文件 `PartialFiles=0`、Hint 为空（一个字不许加）
- `TestGrepPartialScanCoexistsWithHits`：有命中时也要说清"可能不全"

**变异**（已进 `scripts/mutations.json`，`python scripts/mutate.py --only grep-` 三条全 CAUGHT）：
- `grep-preview-cuts-mid-rune`（`line[:cut]` → `line[:maxBytes]`）→
  `预览不是合法 UTF-8：前 24 字节 e4 b8 ad …` / `TestGrepLongLineIsValidUTF8AndMarked` 同时红
- `grep-preview-hides-truncation`（去掉标记）→ `被裁过的行必须带标记，得到 "xxxxxxxxxxxxxxxxxxxxxxxx" 的尾部`
- `grep-partial-scan-silent`（`partial++` → `partial += 0`）→ `只扫了一半的文件数 = 0，期望 1`

源码复原后 `sha256sum -c` 与改前一致；整包 `go test ./modules/tool/grep/` 19 条 ok（11 旧 + 8 新），
`-race` 定向通过。

## §2 没动的东西（明确记录）

- `internal/agent/loop.go`、`internal/goal/`、`internal/session/*`：本仓根本没有 #17/#18/#19/#21 那几条路径（耐久 inbox /
  `keepInbox` / `SessionStore` / `turnOutline`），**不造出来再去修**。
- `modules/fs/local/local.go`：没有跟 #9/#10 一起上原子写 + 退避重试——那是"换来换去"（永久失败晚报 ~1s、
  Windows 上 temp+rename 与打开中的目标冲突），违反"纯正向"闸门。
- `internal/llm/*`：`#4/#5/#6` 都核过。#5/#6 我们本来就对；#4 的正确修法要动 `spi` + Transport 契约（见主表）。
- `cmd/dshd/main.go` 的每步全量重读（#14/#20）：是真实性能债，但改它会改压缩触发时机，**没有等价性证明就不动**。
- 序列化 / 版本号 / 工具描述 / schema / 快照：一个字没改。

## §3 上一轮误判的更正清单

| 条目 | 上一轮说法 | 本轮更正 | 依据 |
|---|---|---|---|
| #12 | 「不修（无对应）」 | **零代价 → 已修**：官方那半我们确实没有，但同一位置我们有自己的缺陷（按字节切字 + 不标截断 + 长行静默漏扫） | §1 |
| #4 | 「不修（无对应）；我们的 `ExitCode int` 没有 null 态」 | **有代价 → 不修**：我们有等价态 `-1`，且照官方那样渲染成数字；正确修法要加 `HasExitCode` 跨层字段（`-1` 与真实退码 `0xFFFFFFFF` 同值） | 主表 #4 |
| #10 | 「有代价 → 不修」（转述口径） | **不动（无此路径）**：没有临时文件就没有"清理失败盖根因"，这条不占"有代价"的名额 | 主表 #10 |
| #8 #9 | 「我们没照抄」 | #8/#9 是**无此路径**（没有补丁工具 / 没有原子写），不是"没照抄某处"；动作相同（不动），但性质不同 | 主表 #8 #9 |
| 计数 | 「16 条待评估」 | 条数对，**成员不同**：真正的 16 条 = 名录汇总里的「已比官方对 6」+「无对应 10」；#3 #7 #15 #20 #22 #24 #26 上一轮已有「有代价」结论、#16 已有「零收益」结论，都不在本轮范围内 | 本文开头 |
