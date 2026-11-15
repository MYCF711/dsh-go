# PLAN §15 的 CI 七条：现在的覆盖情况（2026-11-03）

| # | 规则 | 怎么管的 | 状态 |
|---|---|---|---|
| 1 | kernel 不依赖业务包 | `check.sh` 的 R1 检查 | ✅ |
| 2 | 隔离副本移出 `modules/` 仍能 `go build ./internal/...` | `check.sh` 的 A3；共享树禁止原地移动 | ✅ |
| 3 | 每个 `Exec` 的 error 有 Hint | `TestEveryToolErrorHasHint`（扫 32 文件 / 297 处） | ✅ |
| 4 | `go vet` + `staticcheck` + `-race` | vet ✅ / **staticcheck 已装并接入门禁** / **`-race` 已真跑**（GO.7/T3 强制开 cgo，抓到 4 处真实竞态并修掉）；**跑不起来判 FAIL，SKIP 不算通过** | ✅ 3/3 |
| 5 | 工具 schema 快照 | `TestToolTableSnapshot` + `-update` | ✅ |
| 6 | 工具表 ≤ 6000 token | **本轮新加** `TestToolTableTokenBudget`（本仓估算器）+ 体积守卫 32 KB | ⚠️ 估算器，非真分词 |
| 7 | 二进制 ≤ 25 MiB（25×1024×1024 字节） | `check.sh` 量**发布形状**（`-trimpath -ldflags "-s -w"`，与 `scripts/build.sh:43` 同形状）；未裁剪体积另打 **WARN** 保留可见性 | ✅ 18 MB（发布形状，2026-11-09 实测 19,837,952 B） |

## 这轮补的

第 7 条之前**完全没人管**——PLAN 写了，代码里没有对应检查。
现在 `check.sh` 会真的构建一次并量字节数：`PASS 二进制体积 13 MB（上限 25 MB）`。

顺带说明为什么是 13 MB 而不是文档里的 8.6 MB：那条数是在 `-s -w` 下的，
门禁量的是**不定裁剪**的产物——它更大，所以是更严的下界。

## 口径变更（2026-11-09，GO.7/T3；Lead 拍板）

**第 7 条改成量"发布形状"，未裁剪体积降为 WARN。**

- **原来**：`check.sh` 量的是 `go build -o x.exe ./cmd/dshd`（**不带任何 ldflags**）。
  13 MB 时代它相当于"更严的下界"，一直绿；产品长到 28 MB 之后它变成**假红**——
  而**发布件**（`scripts/build.sh:43` 的 `-trimpath -ldflags "-s -w -X …"`）一直是 **19.9 MB，达标**。
- **现在**：判定按发布形状（`-trimpath -ldflags "-s -w"`）对 25 MiB；
  **同时**另量一次未裁剪体积并打 `WARN`（附两个数字），保持增长可见。
- **为什么不选"砍 3.3 MB 瘦身"**：用户 2026-11-08 的能力下限裁定写明"不许用削减能力消错"，
  而当前体积大户（wazero / office-pdf / speech）都是真实能力。
- **不许把那条 WARN 删掉**：删了就等于"体积悄悄涨到一个没人注意的数"，
  那正是这条门禁当初存在的理由（`check.sh` 里写了同一句话）。
- 依据与两个数字：`tmp/GO7-RELEASE/ci/RESULT.md` §5 / §10。

## `-race`：从"没跑"改成"跑不起来就红"（2026-11-09，GO.7/T3）

- **病**：Windows 上 `CGO_ENABLED` 默认是 **0**，`go test -race` 只回一句 `-race requires cgo`；
  旧 `check.sh` 把这种情况降级成 `SKIP -race 不可用` + 普通 `go test`，输出里**长得像通过**。
  GO.7 期间这条门禁事实上一次没跑——强制开 cgo 之后立刻抓到 **4 处真实 DATA RACE**
  （`internal/account` 的订阅者拿到活结构指针，是真 bug）。
- **改法**：`check.sh` 里"`-race` 跑不起来" → **FAIL**（不再 SKIP/PASS）；
  `.github/workflows/ci.yml` 的那一步显式 `env: CGO_ENABLED: "1"`，
  并新增一步探测 C 工具链（找不到 gcc 时打 `::warning::` 说明"接下来的 check.sh 会因此判 FAIL"）。
- **跑法只有一种**：`scripts/race.sh`（它自己把 MinGW 加进 PATH 并设 `CGO_ENABLED=1`），
  或手工 `CGO_ENABLED=1` + `DSH_MINGW`。见 `docs/race.md`。


## 当前未闭环摘要（2026-11-03 复核）

- staticcheck 已安装并接入门禁；本段后面“没装”的说法是历史记录。
- ~~`-race` 未实际成功~~（2026-11-09 起已实际跑通：GO.7/T3 强制开 cgo，抓到 4 处真实竞态并修掉，终态 `go test -race ./...` PASS）；race-guard 仍是结构补偿，与 `-race` 是补充关系不是替代。
- 工具表每次跑的是估算器；当前版本、当前工具集的同模型真实分词仍需独立测量，旧 42 工具实测不能证明后续 47/49 工具。
- A3 判据仍成立，但当前 `check.sh` 原地移动 modules，共享工作树不得运行；只在隔离副本验证。
- 因而不写“七条全满足”或固定几条过关，逐次以实际 PASS/FAIL/SKIP/WARN 输出为准。

---

## 第 6 条：这轮补成了"每次跑测试都算一遍"

新增 `TestToolTableTokenBudget`：把 compact 形态的工具表序列化，用本仓估算器
（`session.EstimateRunes`：ascii/4 + 非 ASCII 每字 1）算 token，超过 **6400** 就红。

当前读数：

```
工具表 44 个，14510 字节，估算 3892 token（当时上限 6000；0.2.2-rc.2 起 6400）
```

### 估算器准不准

历史一个样本中，网关实测 **4,149 token**、估算 3,892，样本内低估约 6%。这不能外推为固定误差方向、误差上界或“6400 估算等于某个实测值”的换算关系；工具数/描述/模型变化后必须重新实测。

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

---

## "门禁全绿但 go test 红"——查清了，是我观测的一个假象

上一轮我发现 `go test ./...` 红而 `check.sh` 说 ALL GREEN，当时怀疑门禁漏了全量测试。
**查了一下：没有漏。**

```bash
# scripts/check.sh:37-45
race_out="$(go test ./... -race 2>&1)"        # 有 CGO 时跑 -race
go test ./... && pass "go test（未覆盖竞态）" || bad "go test"   # 没有就跑普通版
```

**它确实跑全量 `go test ./...`。** 而且实测过：把 `source.go` 里一个空 id 的守卫改坏，
`check.sh` 当场 `FAIL go test`；还原后 `ALL GREEN`。

那上一轮为什么两边不一致？

因为 `TestToolParityWithDSH` **对着正在运行的 DSH 取工具表**。DSH 那边只要工具集变一下，
这条测试的结果就跟着变。我分别在两个时刻跑了两条命令，**它们看到的 DSH 不是同一个状态**。

### 这条要记下来

**一个依赖外部进程状态的测试，会让"门禁"和"手动 go test"给出不同答案。**
不是门禁的错，是那个测试本身的确定性不足。

- 它有价值：能抓到真实的契约漂移（上一轮就抓到 `target`）
- 它也有代价：结果不可复现，**别人复跑时红不红取决于他那台机器上的 DSH**

结论：**保留它，但要知道它不是确定性测试。** 真要复现问题，得先固定 DSH 的版本或快照。
