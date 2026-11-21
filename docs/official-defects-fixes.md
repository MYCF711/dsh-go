# 修了什么（零代价两条）

## 修 1 · bash 后台任务输出的头部裁剪点落在 UTF-8 字符边界

**官方：**`dsh-subprocess-local/src/output.ts:174-179` 按字节精确认尾
（`this.chunks[0] = head.subarray(excess)`，注释还写着 "must hold the LAST maxBytes
regardless of how the stream was chunked"），出口 `:265-267`
（`buffer.subarray(...).toString('utf8')`）——窗口首字节落在多字节字符中间就吐一个替换字符。
**同一个发行版里 `dsh-jobs-local` 对同一件事反而做对了**：`src/ring.ts:26-32` 的 `utf8Tail`
把起点推过续字节 `10xxxxxx`，注释写明 "the surviving text never starts inside a code point"。
**我们：**照抄的是错的那一版——`modules/exec/bash/session.go` 的
`drop := len(t.buf) - maxRetainedJobOutput` 之后直接 `copy`。于是
`bash{command: <打印中文>}` 溢出 1 MiB 后，模型看到的第一行以 U+FFFD 开头，会当成
「命令吐了乱码」而不是「输出被截断、原文在 spill 里」。中文输出是本仓常态。

**改动：**
- `modules/exec/bash/session.go` 新增 `dropHeadLocked(drop)`：裁剪点前推过 UTF-8 续字节
  （`utf8.RuneStart`），`append` 改走它。
- `modules/exec/bash/retention_params.go` 的 `trimSettledLocked`（结算裁剪）同一条路径，一并改走它。

**为什么零代价：**
- 不变量不变：`base` 与 `buf` 同时减去同一个 `drop`，所以 `total - base == len(buf)` 恒成立；
  最多为对齐多丢 0~3 字节（一个 4 字节字符的续字节上限），有测试钉住。
- 纯 ASCII 流一位不动：`utf8.RuneStart(b)` 对 ASCII 恒真，循环一次都不进。
  既有 `TestTaskOutputRetentionIsBounded`（`strings.Repeat("x", …)`，断言 `base==123`）**零改动通过**。
- 没有任何上限 / 超时 / 交互变化；模型看到的信息只多不少（少一个 U+FFFD，多一个真正的首字符）。

**测试：**`modules/exec/bash/retention_utf8_test.go`（4 条）
- `TestTaskRetentionCutsOnRuneBoundary`：1MiB 窗口 + 3 字节/字的真载荷，夹具自证
  `naiveDrop%3 != 0`（否则构造不出缺陷现场）→ 断言保留窗口是合法 UTF-8、不以 U+FFFD 开头、
  是原载荷的连续子串、多丢 ≤3 字节、`total-base == buffered`
- `TestSettledTrimCutsOnRuneBoundary`：结算裁剪同一套断言
- `TestRetentionASCIIIsNotOverDropped`：ASCII 载荷裁剪量与改前逐数一致
- `TestDropHeadLockedBounds`：`drop=0` / `drop=len(buf)` 的边界

**变异：**把对齐关掉（`&& false`）→ `TestTaskRetentionCutsOnRuneBoundary` 报
`保留窗口不是合法 UTF-8：前 24 字节 ad e4 b8 ad …`（`\xad` 就是切在字符中间），
`TestSettledTrimCutsOnRuneBoundary` 报 `"\xad中中中中中"`。**两条都红**；还原后
`session.go` sha256 与改前一致（`e789f71…`），整包 `go test ./modules/exec/bash/` 13.1s ok。

---

## 修 2 · edit 只替换命中区间，不再重写全文的行尾

**官方：**`dsh-fs-local/src/fsio.ts:721-726`（`detectLineEndings`）只采样**前 4096 字符**
数多数票（`return crlfCount > lfCount ? 'CRLF' : 'LF'`），再按这个票数把**全文**重新编码
（`:736-738` `restoreLineEndings` = `normalizeLineEndings(content).split('\n').join('\r\n')`）。
混合行尾的文件因此被整体改写：前 4096 字符全 CRLF、后面 3000 行全 LF 的文件，
一次单行编辑就把那 3000 行全变成 CRLF——而判定依据只是一个采样。
**我们：**连这一步都没有：`modules/tool/edit/edit.go` 无条件
`strings.ReplaceAll(string(data), "\r\n", "\n")` 之后整串写回，等于把**整个文件**改成 LF。
改一行 → git diff 显示全文都改了。

**改动：**`modules/tool/edit/edit.go` 新增 `splicePreservingEOL` / `matchStarts` /
`normSpansToRaw`，替换那行 `strings.Replace(content, …)`：
- 匹配判定**完全不变**（仍在规范化文本上数出现次数、判定唯一性），只是把命中区间的
  **规范化坐标精确落回原文坐标**，然后只在原文上把那几段换掉，其余字节原样写出。
- 行尾风格只用来决定**新插入**的换行怎么写（多数票，判据与官方 `detectLineEndings` 一致），
  不再重写任何既有行。

**为什么零代价：**
- 「找不到 / 不唯一 / replace_all」的判定口径一个字没改 → 失败路径的 Hint 与错误文本全不变。
- 纯 LF 文件走的路径**恒等**（无 `\r\n` 时坐标映射是恒等映射），行为与改前逐字节相同。
- 「读后写」观测按 `mtime + size` 登记（`modules/fs/local/local.go:372-410`），不是内容哈希，
  所以写回内容的字节变化不影响 `CheckWritable`，不会引入假的 `FS_STALE_VERSION`。
- 不增删任何信息、不加交互、不改上限：`bytes_after` 从「LF 化之后的假数字」变成**真实落盘字节数**。

**测试：**`modules/tool/edit/eol_test.go`（7 条）
- `TestEditKeepsCRLFFileByteExact`：CRLF 文件 → 期望字节里带 `\r`，只改命中处
- `TestEditKeepsMixedLineEndings`：`one\r\ntwo\r\nthree\nfour\n` 只改 `one`，后面 lone-LF 行一个字节不动
  （这正是官方 `detectLineEndings` 会踩的坑）
- `TestEditNewLinesFollowFileStyle`：多行 `new_string` 进 CRLF 文件 → 新行是 `\r\n`；进 LF 文件 → 不凭空出 `\r`
- `TestEditLFOnlyIsUnchanged`：纯 LF 文件行为与改前完全一致
- `TestEditReplaceAllKeepsCRLF`：`replace_all` 逐个命中、逐个保留，且 `bytes_after` 与落盘一致
- `TestEditMatchesNormalizedOldStringInCRLFFile`：带 `\n` 的 `old_string` 仍能命中 CRLF 文件（匹配层没被弄坏）
- `TestMatchStartsAndSpanMapping`：非重叠口径 + 恒等映射 + CRLF 偏移映射的单元级守卫

**变异：**换回 `strings.Replace(content, …)` → **5 条红**：
`CRLF 文件被整体改写了` / `混合行尾被改写` / `新行没跟随 CRLF` /
`replace_all 破坏了 CRLF: "Y\nmid\nY\ntail\n"` / `跨行替换结果错: "X\nline3\n"`。
还原后 `edit.go` sha256 与改前一致（`897a730…`），整包 `go test ./modules/tool/edit/` ok。

---

## 没动的东西（明确记录）

- `internal/llm/*`：官方 #5（错误路径丢 usage）、#7（Retry-After）都核过；我们 #5 已对，#7 有代价。
- `internal/compact/compact.go`：官方 #15（摘要输入无窗口适配）我们照抄了，改它有代价（丢中段 + 改摘要质量 + 补降级路径）。
- `internal/prompt/instructions.go`：官方 #22（指令读取无聚合预算）我们照抄了，改它会动整套 golden。
- `modules/tool/read`：官方有 50KB/2000 字符上限、我们没有。**没补**——补上限等于让模型看到的信息变少，不是零代价。
- `internal/session/projection*.go`：官方 #16 我们照抄了弱的那面，但该层**没有生产调用点**，修了零收益。
- 序列化 / 版本号 / 工具描述 / schema / 快照：一个字没改。