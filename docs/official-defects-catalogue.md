# 官方缺陷名录（rc.5 基线）

> **这份文件为什么在这里**：它原本只写在 `tmp/` 下，而 `tmp/` 是 gitignore 的 —— 也就是说
> 一次全项目复查的结论会随工作树一起消失，下一个人得从头再查一遍。**调查结论比当轮的修复更值钱。**
>
> **判断标准**（`$DSH_HOME/AGENTS.md` 铁律 13 / 本仓 `AGENTS.md` §0.6）：
> 先查官方怎么做 → 再问能不能更好（**官方有 bug 就直接修**）→ 最后过「纯正向」闸门：
> **「有没有人要为这个改动付什么」**，答不出「没人付代价」就不做。
>
> **基线**：官方 `0.2.2-rc.5`，解包在 `tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/`。
> **不要用 `tmp/GO3-PARAMS/_official/`，那是 rc.1**，行号与部分取值都不同。
>
> **本名录 26 条**，其中**已修 2 条**（零代价）、**4 条有代价明确不修**、4 条*我们本来就没照抄*
> （标「别改回去」）、其余待评估。修与不修的理由见 [`official-defects-fixes.md`](official-defects-fixes.md)。

# GO8-RC5 · 官方缺陷清单（rc.5 自查）

- 官方基线：`tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/*/src/*.ts`（v0.2.2-rc.5，2238 个 `.ts`、41.7 万行）
- 我方：worktree `fix/official-defects`（从 `origin/main` = `ccc4327` 起）
- 判据：**「有没有人要为这个改动付什么」**。答不出「没人付代价」就不修。
- 分工：5 条并行深挖（agent-loop / 工具执行 / 上下文预算 / LLM / FS+沙箱），全部 `file:line` + 原文引用回验，主会话逐条复核。

## 主表

| # | 官方缺陷 | 官方 file:line | 我们是否照抄了 | 修它有没有代价 |
|---|---|---|---|---|
| 1 | 写回 = 重写全文行尾：只采样前 4096 字符数多数票，再按票数把**整个文件**重新编码 | `dsh-fs-local/src/fsio.ts:721-726` + `:736-738` | **照抄了，而且更糟**：`modules/tool/edit/edit.go` 无条件把全文 CRLF→LF 后整串写回 | **零代价 → 已修** |
| 2 | jobs 输出尾部按字节精确认尾后 `toString('utf8')`，切在多字节字符中间吐 U+FFFD | `dsh-subprocess-local/src/output.ts:174-179` + `:265-267`（**同发行版正确版**：`dsh-jobs-local/src/ring.ts:26-32`） | **照抄了错的那版**：`modules/exec/bash/session.go` 的 `drop := len(t.buf) - max` 直接 `copy` | **零代价 → 已修** |
| 3 | 后台任务「进程根本没起来」记成 `killed`，与用户主动 kill 同值；官方自己写 TODO 承认 | `dsh-tool-bash/src/background.ts:60-62`（自认 TODO `:55-59`）；成因 `dsh-bash-local/src/index.ts:330-331` | 没照抄（我们报 `failed`，见 `cmd/dshd/serve_terminal_api.go:173,175`），但反向丢了 `killed` 档 | **有代价 → 不修**：状态枚举多一值，所有消费方要处理 |
| 4 | `exitCode: null` 渲染成 `[exit code: null]`；反解析 `\d+` 不匹配 → 回落成「干净退出 0」 | 渲染 `dsh-tool-bash/src/render.ts:57-61`（`else if (result.exitCode !== 0)`，`null !== 0` 为真）；反解析 `dsh-shell/src/render.ts:40-42` → `exitCode: 0`；pwsh 同 `dsh-tool-pwsh/src/render.ts:77-81` | 无对应（我们的 `ExitCode int` 没有 null 态） | 不修（无对应）；规则化：渲染层不得把 null 渲染成数字字面量，反解析层不得把「解析不出」当成功 |
| 5 | 错误路径丢掉已计费 usage：`refusal` 特意先发 `usage` 再抛，`EMPTY_RESPONSE` / `MALFORMED_RESPONSE` 先抛 → 永远走不到后面那句 `yield usage`；而 `EMPTY_RESPONSE` 在默认可重试集里（`dsh-llm/src/retry-policy.ts:19-25`）⇒ 每次退化补全被重试 5 次、每次真实计费、6 次尝试的 usage 一次都不上报 | `dsh-llm-deepseek/src/translate.ts:157-163` vs `:173`；对照 pi-ai 同失败是会报账的 `dsh-llm-pi-ai/src/stream.ts:226` | **没照抄**：`internal/llm/client.go:371,379,383` 所有失败路径都把带 `Usage` 的 snapshot 一起交回 | 不修（我们无此缺陷）；登记为「别照着改回去」的正面样本 |
| 6 | 空白凭据状态混淆：`describe()` 说「已配置」并给掩码预览，`resolve()` 说 blank —— 判据是 `value.length === 0` 而不是 trim 后为空，正面违背 seam 自己写的不变量 | `dsh-credentials-local/src/index.ts:284`（parse）、`:660`（set）、`:648-650`（describe）；被违背的不变量 `dsh-credentials/src/index.ts:158-162` | **没照抄**：`internal/llm/model_catalog.go:392-397` 用 `strings.TrimSpace(...) != ""`；`internal/llm/credential_mask.go:46-49` 注释写明判据 | 不修（我们已对）；别为「对齐官方」把 trim 判据退回去 |
| 7 | `Retry-After` 被当成「替代退避」而不是下限；且超过 `maxDelayMs` 时 `return next()` **一次都不重试**——恰好发生在最需要重试的 429 上 | `dsh-llm-retry/src/index.ts:224-235`；契约自述 `dsh-llm/src/retry-policy.ts:31` | **部分照抄**：`internal/llm/retry.go:200-213` 超上限 `return 0, false` 与官方一致（(a) 我们取 `max(自身退避, Retry-After)`，比官方对） | **有代价 → 不修**：改成 clamp 后重试 = 多打一次必失败的请求；改成等满 = 单次等待拉长 3 倍。换来换去 ⇒ 不做 |
| 8 | `apply_patch` 每次应用静默删掉文件末尾一个空行（`split` 只 pop 一个尾空元素，收尾只补一个 ⇒ ≥2 个 `\n` 结尾的文件每打一次补丁永久少一个空行） | `dsh-fs-patch/src/apply.ts:66-67` + `:114-115` | 无对应（我们没有 apply_patch / hunk 补丁工具） | 不修（无对应）；将来实现时用 `hadTrailingLF` 布尔，别抄 pop/push |
| 9 | Windows 上写已有文件走 `ReplaceFileW` 遇瞬时共享冲突（杀软/索引器/git 持有）直接失败并报成永久性 `FS_PERMISSION_DENIED`；**同一产品**另一条写入路径对同一批错误码有 8 次 20→200ms 退避重试 | 无重试：`dsh-fs-local/src/fsio.ts:657-668`（`if (!isENOENT(error)) throw error`）；有重试：`dsh-atomic-write/src/index.ts:19,26-50` | 无对应（我们的写是单次 `os.WriteFile`，`modules/fs/local/local.go:435`，既无原子性也无重试） | 不修（无对应）；补原子写时必须**同时**带上退避重试，别把官方缺重试一起抄进来 |
| 10 | `writeFileAtomic` 的失败清理 `await rm(temp)` 自己可能抛错，把原始失败原因整段替换掉（Windows 上不是理论问题）；官方自己在 `dsh-fs-local` 另一处特意做了双错因拼接 | 吞错：`dsh-atomic-write/src/index.ts:88-94`；正确版：`dsh-fs-local/src/fsio.ts:535`（`write failed (...) and temp cleanup failed (...)`）。另 `:80-82` 有 durability TODO | 无对应 | 不修（无对应）；实现时 `rm` 必须 `.catch()` 包住并保留 `cause` |
| 11 | `read` 的 offset 越界在**已存在**文件上返回 `FS_NOT_FOUND`——与「文件真的不存在」同一个机器可路由的错误码 | 越界：`dsh-tool-fs/src/read-render.ts:96-98`；真不存在：`dsh-tool-fs/src/read-target.ts:28` | **没照抄**：`modules/tool/read/read.go:97-102` 用普通 error + 可执行 Hint | 不修（我们已对） |
| 12 | grep 不带 `--max-columns`：`--json` 会把匹配行**完整文本**塞进 `data.lines.text`，几条几 MB 的长行就能堆过 20MB 原始输出上限 ⇒ **整次搜索失败**（`SEARCH_RAW_OUTPUT_OVERFLOW`），而每行截断的 `GREP_MAX_LINE_BYTES=2000` 根本没机会执行 | `dsh-tool-fs-search/src/grep.ts:113-118`（argv）+ `:155-157`；超限即整单失败 `search-core.ts:144-149` | 无对应（我们纯 Go 实现，`modules/tool/grep/grep.go:42-44` 先按 2MB/文件、400 字符/行截断） | 不修（无对应） |
| 13 | 工具超时分类只在下游**正常返回**时成立：包装层只替换 `next()` 的 resolve，没有 catch。`tools/execute` 瀑布里任一层抛错就穿透，被外层兜成 `TOOL_ABORTED` 或普通写盘错误 ⇒ 任何按 `TOOL_TIMEOUT` 路由的重试/沙箱插件都看不见 | `dsh-tool-call-timeout-policy/src/index.ts:67-79`（只有 `finally`，无 `catch`） | **没照抄**：`modules/tools/pipeline.go:505,575` 到点一律 `&ToolTimeoutError{...}`，注释 `:494-497` 明确区分「预算到点 vs 父 ctx 取消」 | 不修（我们已对） |
| 14 | compaction 入口状态检查在**每个 step** 上全量回扫整个 session log：提前 `break` 要求 `latestEndSeedSeq !== undefined`，而它只在带 seed 构造的会话里才成立 ⇒ 普通新会话每次回扫到 seq 0；且这个昂贵检查排在廉价阈值判断**之前**（`:328` vs `:342`） | `dsh-compaction-basic/src/region.ts:572,595-597`；调用点 `src/index.ts:328`；阈值 `:342` | 无对应（我们没有自动判压路径，压缩只从 CLI/界面显式触发） | 不修（无对应）；补自动判压时按收紧版实现 |
| 15 | 摘要请求把被压区间**逐条不截断**地重放给同一路由模型，且 overflow 路径把尾巴收到只剩最后 1 个节点 ⇒ **已经超窗的会话永久压不动**：压缩在最需要它的那一刻必然自己也超窗 | `dsh-compaction-basic/src/region.ts:553-557` + `src/summarizer.ts:145-158`；overflow 收紧 `src/index.ts:305` | **照抄了，且是主动对齐的**：`internal/compact/compact.go:123` 注释明写「摘要输入**不设**字符上限」 | **有代价 → 不修**：加摘要输入预算就要丢中段（改变摘要质量），marker 还必须落在 tool-call/result 对边界上；还要补「摘要仍失败」的降级路径 |
| 16 | 同一份「派生缓存坏了怎么办」三个读面三种答案：`hydratePrepared` 丢掉缓存从日志重折（对），`restore()` 直接抛（错），`viewCheckpoint()` 半软半硬 | 对：`dsh-session-projection-cache/src/index.ts:240-245`；错：`dsh-session-projection/src/index.ts:735-738`；半软：`:674-680` | **照抄了弱的那面**：`internal/session/projection.go:305-310` + `projection_cache.go` 的 `Restore` 直接 `return err`。**但该层当前没有生产调用点**（`OpenProjectionCache` 只被测试调用） | 不修（零收益）：错误路径才能走到、改起来零代价，但没人付也没人受益；登记为接线时按「丢缓存重折」实现 |
| 17 | `pre-step` 被 `reject` 时，已 `claim` 的用户消息被永久销毁：`claim()` 先**耐久**地从队列摘掉，之后任何监听者返回 `{kind:'reject'}`（归档门、子代理归档门在正常路径就会返回），这批消息既不写回队列也不进日志 | `dsh-agent-loop/src/agent.ts:293` + `:354-356`；`src/inbox.ts:109-110`；内证：`dsh-goal-round-driver/src/index.ts:406` 自带 `restoreOtherClaimed` 兜这个洞 | 无对应（`internal/agent/loop.go:226` 的 PreStep 只能返回 error，没有 blocked 语义、没有耐久 inbox） | 不修（无对应）；规则化：**「决定不开步」不许等于「销毁输入」** |
| 18 | 轮次驱动卸载时 `cancel({kind:'parent'})` 不带 `keepInbox` ⇒ `inbox.clear()` 把两条队列全清，用户排在目标轮后面的 follow-up 全部消失；同文件里两处正确做法（精确删一条 / `keepInbox: true`）摆着 | `dsh-goal-round-driver/src/index.ts:473-474`；`dsh-agent-loop/src/agent.ts:194` | 无对应 | 不修（无对应） |
| 19 | `SessionStore.create()` 的「调用方 fiber 所有权」契约与实现不符：`ctx.effect` 挂的是 sessions 服务自己的 fiber，调用方 fiber 卸载既不停止通知也不 `session/disposed` | 承诺 `dsh-session/src/index.ts:1185-1186` + `README.md:32`；实现 `:1212`（`this.ctx.effect(...)`），行内注释 `:1208` 把错的写了两遍 | 无对应（我们 `CreateSession` 返回调用方自持的 `*Log`，显式 Close） | 不修（无对应） |
| 20 | token meter 每次 `measure()` 都 `structuredClone` + `deepFreeze` **全部** surface 节点，把 `_sync()` 增量省下的时间原样还回去 ⇒ 每步 O(节点数)、每轮 O(N²)，正好在上下文最紧时最贵 | `dsh-token-meter/src/index.ts:188-195`；调用频率 `dsh-compaction-basic/src/index.ts:325,348,363` | 没照抄（Go 无 clone/freeze 必要），但**我们在另一处付了更贵的账**：`cmd/dshd/main.go:1316-1317` 每个 `assistant/message` 把整个会话日志**从磁盘重读一遍** | **有代价 → 不修**（我们那处）：改锚点语义属于语义变更，要真机 resume 复验；单独登记为 P1 |
| 21 | `turnOutline` 投影无界增长且每个事件都整体复制一次数组：10,000 轮 = 10,000 条常驻 + 累计 O(N²)，而它还是**可持久化投影**（checkpoint 每次要把整个数组序列化） | `dsh-session-turn-outline/src/projection.ts:102,115` | 无对应 | 不修（无对应）；将来做列表投影用环形/分块并显式设上限 |
| 22 | 指令文件读取没有聚合预算——**官方自己写了 TODO 承认**：候选集 = 祖先链 × 4 个候选名 × 每文件 1MiB，深目录下上百 MB 读进内存后才被裁到 64KiB | `dsh-agent-instructions/src/files.ts:393-397`（`TODO(total-instruction-read-bound)`）、`:478-479`；单文件上限 `src/config.ts:14` | **照抄了**：`internal/prompt/instructions.go:314-317,374`；`internal/ainstr/ainstr.go:675` 同一套 | **有代价 → 不修**：加聚合预算必须定「超了丢谁」，而候选顺序是由宽到具体（丢窄的违背「更具体的优先」）；会改 `internal/prompt/*_chain_test.go` 整套对拍 golden |
| 23 | pi-ai 把捕获的错误压平成 `error.message`，丢掉原始 `Error` 与 `cause` 链 ⇒ 只能靠正则猜失败类别（官方自己写 `XXX(pi-ai upstream)` 承认，注释里点名「we are left pattern-matching terse words here」） | `dsh-llm-pi-ai/src/stream.ts:41-45` + `classifyPiAiError` `:51-79` | 无对应（我们按字段取 JSON，天然前向兼容） | 不修（无对应） |
| 24 | 同一个「太大」在五个层里用五种单位判定（pruner 码点 / instruction 字节 / output-retention 字节 / spill-policy token / compaction 路由 token），换算随语言漂移 4 倍：8,192 码点的中文 ≈ 8,192 token，ASCII ≈ 2,048 | `dsh-compaction-tool-result-pruner/src/config.ts:10-14` vs `dsh-agent-instructions/src/config.ts:14` vs `dsh-spill-policy/src/index.ts:28` vs `dsh-output-retention/src/index.ts:240` | 照抄（`internal/prune/prune.go:41` 码点，且注释已自陈口径问题） | **有代价 → 不修**：统一到 token = 改全部出厂阈值 = 改产品行为（什么时候开始裁），所有 golden 与用户观感都变 |
| 25 | Win32 错误码在 `CloseHandle` **之后**才 `GetLastError()`：`WAIT_FAILED` 分支被 `closeHandle` 覆写；`releaseLockHandleWin32` 无论哪一步失败都把 syscall 报成 `ReleaseSemaphore` | `dsh-session-persistence-jsonl/src/win32.ts`（`acquireLockHandleWin32` / `releaseLockHandleWin32`） | 无对应（我们用 Go syscall） | 不修（无对应）；低危但真：错误不可诊断。规则化：**`GetLastError` 必须在任何后续 Win32 调用之前读** |
| 26 | 未知 `event` 被宽容跳过（注释说 Anthropic 允许），未知 `delta` / `stop_reason` 却升级成 `MALFORMED_RESPONSE`——把**已经完整收到的正文**连同 usage 一起丢掉 | `dsh-llm-deepseek/src/translate.ts:123-126`（跳过）vs `:87`、`:101`（打死整条流） | 没照抄（我们只声明用得到的 JSON 字段，未知字段静默忽略） | **有代价 → 不修**：保留未知 stop_reason 原词要给闭合联合类型的 `finish` 加字段，会动装配层契约 |

## 汇总

- **零代价、已修**：#1、#2（详见 `tmp/GO8-RC5/_hunt/fixes.md`）
- **有代价、明确不修**：#3 #7 #15 #20 #22 #24 #26
- **我们照抄了但零收益**：#16（该层未接生产）
- **我们已比官方对，登记为「别照着改回去」**：#5 #6 #11 #12 #13 #23
- **官方自认缺陷（未修，只是把坑标出来）**：#3（TODO）、#10（durability TODO）、#22（TODO）、#23（XXX）
- **无对应，纯记录**：#4 #8 #9 #10 #14 #17 #18 #19 #21 #25