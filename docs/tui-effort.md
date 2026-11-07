# TUI effort 真值投影（W74 / EFFORT-PROJECTION）

> 基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 任务开始时的共享工作树（含 W56/W58/W59/W65/W66/W67/W69/W72）。未 commit/push，未启动 GUI。
>
> 范围：只读投影 `session.load.effort` 与 TUI 消费；不改 canonical 方法、WS、journal、approval、jobs、model-catalog、settingsform。

## 1. 真值来源与未知语义

核对了 `codecs/v4/read.go`、真实 v4 样本、`cmd/dshd/main.go` / `serve.go` / `serve_loop.go` / `serve_tools.go` 与 `sessionSource.resolve`：

| 位置 | 结论 | 用法 |
|---|---|---|
| v4 header | 严格白名单只有身份/cwd/继承等字段，**没有 effort** | 不扩 header、不改 codec |
| `request/header.header.config.reasoningEffort` | 完整请求配置快照里的真值；真实官方会话有 `max` | 参与最新记录判定 |
| `model/selection.reasoningEffort` | 显式模型/effort 选择里的真值 | 参与最新记录判定 |
| `request/context` | 只有 provider/model/contextWindow 等路由与容量 | 只继续供原 `model` 投影，**不猜 effort** |
| `--reasoning` / `DSHD_REASONING_EFFORT` | 当前进程传给后续 run 的开关；Go 写路径本身没有把它写成 v4 配置记录 | **绝不拿来补会话缺值** |

实现按事件顺序找最新一条 `request/header` 或 `model/selection`：

- 已知枚举 `off/low/high/xhigh/max` 原样投影。官方 `dsh-llm-deepseek` 当前列出 `off/low/high/max`，`xhigh` 是本仓现有 `--reasoning` 显式支持的值。
- 最新记录缺 `reasoningEffort`、空串、`null`、非字符串或未知枚举都返回未知；**不向前回退旧值**。
- 没有任何配置记录的旧 v4 同样未知。`off` 只有持久记录明确写出时才是已知值，空串不翻译成 `off`。
- `session.load` 在 `LoadResult` 尾部 additive 增加可选 `effort`；已知时为字符串，未知时省略。canonical 25 方法名、顺序、原字段、WS/journal 全部不变。
- `internal/transport/source.go` 是必要接线点：tail 与普通两条 `LoadSession` 路径都调用可选 `EffortSourcer`，否则字段只存在结构体里、永远到不了线上。

边界：当前 Go `run` 新写、但没有上述配置记录的会话仍显示 `?`；这是诚实未知，不用本进程默认伪造“真值”。

## 2. TUI 消费

- `client.loadStats` 使用 `*string`：缺键是 `nil`，显式 `""` 是非 nil 空串指针，两种线上形态没有在客户端解析时混成一个 Go 零值。
- 每次 load 是 effort 权威快照：最新记录变未知会清掉旧显示；两者在状态行都画 `? effort`。
- `switchTo` 在请求 B 前先清 A 的 effort；B 缺字段、空值或 load 失败都不会串入 A。`/new` 同样清空。
- 状态行与首屏继续使用已有 `model · <effort> effort` 和按显示列裁剪；120×36、80×24 及超长 effort 不增加行数、不溢出。

## 3. 先红后绿（原样关键输出）

完整输出：`effort-projection-w74/evidence/red-dshd.log`、`red-transport.log`、`red-tui.log`。红测只按线上 JSON 和界面行为断言，不是“新字段不存在导致编译失败”。

宿主 tail 路径：

```text
=== RUN   TestLoadEffortUsesLatestSessionRecord
    transport_effort_test.go:40: session.load effort = <nil>，期望最新会话记录 xhigh；线上={"cwd":"C:\\Users\\ADMINI~1\\AppData\\Local\\Temp\\TestLoadEffortUsesLatestSessionRecord1450557151\\002","messages":[],"model":"model-a","total":0,"truncated":false}
--- FAIL: TestLoadEffortUsesLatestSessionRecord (0.03s)
...
FAIL	dshgo/cmd/dshd	0.297s
FAIL
RED_EXIT=1
```

普通 transport 路径：

```text
=== RUN   TestLoadSessionWithoutTailCarriesOptionalEffort
    load_effort_test.go:28: 普通 LoadSession 路径 effort=<nil>，线上={"messages":[],"total":0,"truncated":false}
--- FAIL: TestLoadSessionWithoutTailCarriesOptionalEffort (0.00s)
FAIL
FAIL	dshgo/internal/transport	0.040s
FAIL
RED_EXIT=1
```

TUI：

```text
=== RUN   TestLoadStatsPreservesMissingAndEmptyEffort/缺失
    effort_test.go:34: stats 没有 Effort 可选字段，缺失与空值无法区分
=== RUN   TestSwitchSessionEffortDoesNotLeak
    effort_test.go:75: A 的真实 effort 没上状态行：" model-A · ? effort · 工具 0 · ctx ? · 结果已折叠 Ctrl+T                                                  会话 session- "
FAIL
FAIL	dshgo/cmd/dshtui	0.066s
FAIL
RED_EXIT=1
```

实现后同组测试真实通过：宿主 `ok dshgo/cmd/dshd 0.337s`、transport `ok dshgo/internal/transport 0.044s`、TUI `ok dshgo/cmd/dshtui 0.081s`；已知五枚枚举、旧 v4、未知值、空值、缺失/空值区别、A/B 与两档宽度均 PASS。

## 4. 真实 dshd + ready token/load 证据

### 配置

- 新二进制：`effort-projection-w74/bin/dshd-effort.exe`；`python scripts/bin-check.py <path>` 原样输出 `OK 1 个二进制都不比源码旧 / 合计 OK=1 FAIL=0`。
- 临时 home：`effort-projection-w74/home/`；回环 `--addr 127.0.0.1:0`，只终止探针自己启动的 PID。
- A/B 由真实 `session.Log` 写 v4：A=`high`、B=`xhigh`；宿主进程默认分别故意用 `max/low` 对照，证明没有拿进程值冒充。
- 官方 v4 副本保持字节不变，源/副本 SHA-256 都是 `37b6d0e1b70a2fd0aef0bc8ef4deb41fcfeb51a824ab45e8283d1409132f83db`，经同一 dshd load 得 `effort=max`。
- 续写走真实 `session.OpenSessionAt`：旧前缀逐字节不变；A 追加最新 header=`low`，B 只追加普通 user 事件并保持 `xhigh`。

命令：

```bash
python C:/Users/Administrator/AppData/Local/Temp/effort-projection-w74/probe/real_effort_probe.py
```

最终摘要原样关键输出（完整输出与逐次 load JSON 在 `effort-projection-w74/evidence/real-effort-final.log`、`load-*.json`）：

```text
LOAD phase=initial sid=session-A status=200 summary={"model": "model-A", "effort": "high", "total": 1, "messages": 1}
OK A-high-not-process-max effort=high
LOAD phase=initial sid=session-B status=200 summary={"model": "model-B", "effort": "xhigh", "total": 1, "messages": 1}
OK B-xhigh-not-process-max effort=xhigh
LOAD phase=initial sid=session-old status=200 summary={"model": "model-old", "effort": "<missing>", "total": 0, "messages": 0}
OK old-v4-missing result-effort=<missing>
OK unknown-enum-missing result-effort=<missing>
OK empty-effort-missing result-effort=<missing>
LOAD phase=official sid=session-1710f716-f138-466d-aa9e-2b63ec80ffb7 status=200 summary={"model": "group/deepseek-v4.1-flash1017", "effort": "max", "total": 182, "messages": 5}
OK official-request-header-max effort=max
LOAD phase=reopen sid=session-A status=200 summary={"model": "model-A", "effort": "high", "total": 1, "messages": 1}
OK reopen-A-stable-not-new-process-low effort=high
LOAD phase=reopen sid=session-B status=200 summary={"model": "model-B", "effort": "xhigh", "total": 1, "messages": 1}
OK reopen-B-stable-not-new-process-low effort=xhigh
APPEND_OK sid=session-A before=673 after=817 prefix=true beforeSHA=e5426aab1d017f07ad6be53fe21141144b56546cfa2b6599ab86bb9ef38da173 afterSHA=30f10ccae2043f04219294656c4dcbd3797626a2771ac7f0abd31089a02d7d70
APPEND_OK sid=session-B before=668 after=845 prefix=true beforeSHA=58afa6977bcc9bb9ca1d25c4ab2db7c16db3c0c5781ebf7e0d7cc2f7ef575a7a afterSHA=8a2ec9c74d5828ad36ef28df5445ef3fa44ceb76f9cca94c03df099670a4c73a
LOAD phase=continued sid=session-A status=200 summary={"model": "model-A", "effort": "low", "total": 1, "messages": 1}
OK continued-latest-header-low effort=low
LOAD phase=continued sid=session-B status=200 summary={"model": "model-B", "effort": "xhigh", "total": 2, "messages": 2}
OK continued-unrelated-event-keeps-xhigh effort=xhigh
EFFORT_REAL_SUMMARY OK=34 FAIL=0 TOTAL=34
REAL_PROBE_EXIT=0
```

首次探针曾因把 `ready {json}` 整行直接交给 `json.loads` 而真红（`PROBE_EXCEPTION JSONDecodeError`，尚未发出 load）；保留在 `real-effort-probe.log`。修正的是探针前缀解析，不是放宽产品断言。

## 5. TUI dump 与真实输入

新 `dshtui-effort.exe` 分别跑 `--dump-screen --size 120x36`、`--size 80x24`，完整屏幕在 `tui-dump-120x36.txt`、`tui-dump-80x24.txt`。无会话记录的快照仍必须是未知：

```text
TUI_DUMP_OK tui-dump-120x36.txt rows=36 maxWidth=120 status=' ? · ? effort · 工具 0 · ctx ?'
TUI_DUMP_OK tui-dump-80x24.txt rows=24 maxWidth=80 status=' ? · ? effort · 工具 0 · ctx ?'
TUI_DUMP_SUMMARY OK=2 FAIL=0
```

真实字节输入→按键解码→行编辑的 9 条测试（插入/删除、词跳、CJK、emoji ZWJ、水平视口等）与 effort A/B、宽度测试同次全 PASS，`ok dshgo/cmd/dshtui 0.092s`。完整 TUI 包测试、全仓 test/vet/build 与相关 race 见本文件第 6 节。

## 6. 全量门禁

以下都是本任务在当前工作树实际执行的命令；完整输出在 `effort-projection-w74/evidence/`。

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go test ./... -count=1
go vet ./...
go build ./...
CGO_ENABLED=1 PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH" \
  go test -race ./cmd/dshd ./internal/transport ./cmd/dshtui -count=1
```

原样关键输出：

```text
ok  	dshgo/cmd/dshd	49.624s
ok  	dshgo/cmd/dshtui	1.609s
ok  	dshgo/internal/transport	1.076s
...
FULL_TEST_EXIT=0
$ go vet ./...
VET_EXIT=0
$ go build ./...
BUILD_EXIT=0
ok  	dshgo/cmd/dshd	224.177s
ok  	dshgo/internal/transport	2.576s
ok  	dshgo/cmd/dshtui	2.804s
RELATED_RACE_EXIT=0
```

race 使用真实 `gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r3) 16.2.0`，不是 CGO 关闭后的跳过。全仓 `go test`、vet、build 均退出 0，相关三包 race 均为 0 且没有 DATA RACE。另跑 `go list -deps ./internal/kernel`，原样汇总 `KERNEL_DEPS_OK`；最终定向复验为：

```text
ok  	dshgo/cmd/dshd	0.523s
ok  	dshgo/internal/transport	0.065s
ok  	dshgo/cmd/dshtui	0.081s
```

## 7. 未验与边界

- 未启动可见 GUI，未做完整工作日；本任务证明真实 dshd HTTP 投影与 TUI 无头布局/输入。
- 未让 Go `run` 新增 v4 effort 写记录（超出允许范围）；因此“无记录=?”仍是明确行为。
- 未改 `docs/tui-visual.md`、`docs/dshtui.md`、`cmd/dshtui/splash.go` 中历史“effort 没投影”的旧叙述（不在允许路径）；当前有效契约以本文件与 `docs/transport-contract.md` 为准。
