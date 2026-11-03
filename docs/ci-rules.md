# PLAN §15 的 CI 七条：现在的覆盖情况（2026-11-03）

| # | 规则 | 怎么管的 | 状态 |
|---|---|---|---|
| 1 | kernel 不依赖业务包 | `check.sh` 的 R1 检查 | ✅ |
| 2 | 删 `modules/` 仍能 `go build ./internal/...` | `check.sh` 的 A3 | ✅ |
| 3 | 每个 `Exec` 的 error 有 Hint | `TestEveryToolErrorHasHint`（扫 32 文件 / 297 处） | ✅ |
| 4 | `go vet` + `staticcheck` + `-race` | vet ✅ / **staticcheck 已装并接入门禁** / `-race` 缺 C 编译器 | ⚠️ 2/3 |
| 5 | 工具 schema 快照 | `TestToolTableSnapshot` + `-update` | ✅ |
| 6 | 工具表 ≤ 6000 token | **本轮新加** `TestToolTableTokenBudget`（本仓估算器）+ 体积守卫 32 KB | ⚠️ 估算器，非真分词 |
| 7 | 二进制 ≤ 25 MB | **本轮新加**：`check.sh` 直接量产物 | ✅ 13 MB |

## 这轮补的

第 7 条之前**完全没人管**——PLAN 写了，代码里没有对应检查。
现在 `check.sh` 会真的构建一次并量字节数：`PASS 二进制体积 13 MB（上限 25 MB）`。

顺带说明为什么是 13 MB 而不是文档里的 8.6 MB：那条数是在 `-s -w` 下的，
门禁量的是**不定裁剪**的产物——它更大，所以是更严的下界。

## 三条还不能算过关的

**第 4 条的 staticcheck**：没装。它在 Windows 上要 `go install honnef.co/go/tools/cmd/staticcheck`，
而本机装依赖要看网络。**没装就是没跑**，不写成"应该没问题"。

**第 4 条的 `-race`**：结构性补偿见 `race-guard.py`（13 处并发点逐个登记并读过），
但那是"有人想过"，不是"机器验过"。

**第 6 条**：PLAN 要求分词实测 ≤6000。我们现在的做法是**体积守卫（32 KB）+ 一次人工实测（4,149 token）**，
不是每次门禁都分词。要真做到得在门禁里跑一次分词器——那需要一个本地 tokenizer，暂时没有。

## 一句话

七条里 **4 条严格过关、3 条是补偿或代理**。这张表的价值在于：
**下次有人说"CI 七条都满足"，他能看到哪三条不是。**

---

## 第 6 条：这轮补成了"每次跑测试都算一遍"

新增 `TestToolTableTokenBudget`：把 compact 形态的工具表序列化，用本仓估算器
（`session.EstimateRunes`：ascii/4 + 非 ASCII 每字 1）算 token，超过 6000 就红。

当前读数：

```
工具表 44 个，14510 字节，估算 3892 token（上限 6000）
```

### 估算器准不准

同一条表，网关实测过 **4,149 token**，估算给 3,892——**低估约 6%**。

所以这条门禁的 6000 阈值相当于实测的 **6,400 左右**。这个偏差是**已知且方向固定**的：
它只会放过"刚好超一点"的表，不会误伤。写在这里，免得下次看到 3892 以为真有那么多余量。

### 为什么不装真分词器

真分词要联网或引依赖。门禁必须能在离线、无凭据的环境跑。
**宁可要一个每次都跑、偏差已知的代理，也不要一个需要条件的真测量**——后者在 CI 上大概率变成"跳过"。

---

## 第 4 条：staticcheck 装上了，第 4 条从 1/3 变 2/3

`go install honnef.co/go/tools/cmd/staticcheck@latest` 成功（有网络）。

### 第一次跑就抓到 17 条，修掉 3 条真问题

只跑 `-checks=SA*,S1*`（真 bug 与可疑写法）时剩 3 条：

| 位置 | 问题 | 处置 |
|---|---|---|
| `internal/llm/errors.go:45` | 无参数的 `fmt.Sprintf` | 改成普通字符串 |
| `cmd/dshd/history_test.go:35` | 读了 `err` 从不使用 | 改成真的断言读得回 |
| `internal/spi/ctx_test.go:21` | nil ctx（SA1012） | **`//lint:ignore`**：这里就是要传 nil 验不 panic |

第三条是**误报**，但误报也要显式写出来——用 `//lint:ignore` 加一句理由，
而不是把整类检查关掉。

### 为什么不跑 U1000（未使用符号）

第一次跑出来的 17 条里 12 条是 U1000：`turnSeq`、`errNoModel`、`noModelRunner`、
`(*Loop).runTool`、`summarize` 等——**都是真死代码**，但删它们要动 12 个文件，
不该和"接入 staticcheck"这一步混在一起。

已记在下面，作为独立的一笔债。

### 实演过

塞回一个无参数 `Sprintf` → `FAIL staticcheck: internal\llm\errors.go:47:11: unnecessary use of fmt.Sprintf (S1039)`，
还原 → ALL GREEN。**它是真门禁，不是装饰。**

### 遗留：12 处未使用符号

```
cmd/dshd/serve.go            errNoModel / noModelRunner / noModelRunner.Run
cmd/dshd/serve_events.go     turnSeq
internal/acp/server.go       msgSeq
internal/agent/loop.go       (*Loop).runTool
internal/session/list.go     summarize
cmd/dshd/meter_calib_test.go stubChat
internal/acp/server_test.go  mu
```

删之前要逐条确认"真没用"——**U1000 也会误报反射/接口实现**，不能批量删。

### 续（第 119 轮）：清理死代码时差点删错

按 U1000 清单动手，**第一刀就删错了一个**：

我删了 `loopRunner.turnSeq` 这个字段，编译立刻报 `r.turnSeq undefined` —— 它在 173 行是**被用的**。
staticcheck 报的 U1000 是**包级的 `var turnSeq atomic.Int64`**（另一处），不是这个字段。
两个同名符号，我按名字搜到就删，没看作用域。

还原之后只留一处真删：`internal/acp/server.go` 的 `msgSeq`（全仓 1 处出现，确属死字段）。

### 剩下的 7 处（未删，逐条列着）

```
cmd/dshd/meter_calib_test.go   stubChat（测试夹具）
cmd/dshd/serve.go              errNoModel / noModelRunner / noModelRunner.Run
internal/acp/server_test.go    mu（测试夹具）
internal/agent/loop.go         (*Loop).runTool
internal/session/list.go       summarize / isCompactCheckpoint
```

这些要么是测试夹具、要么可能是将来要接回来的实现（`noModelRunner` 就是二分时长跑用的）。
**删之前得先知道它为什么在那儿**，不然下次又要写一遍。

### 记一笔

"未使用"这个判断本身就容易出错：**同名不同作用域**、**反射/接口隐式使用**、**留作调试开关**。
这一轮用一次编译失败换来的教训是：**U1000 的输出只是线索，不是判决。**
