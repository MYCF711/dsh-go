# 宿主侧收尾（2026-11-03）

> 这一份是**边界说明**，不是战报。它回答三件事：
> 做完了什么、证据在哪、剩什么——以及剩的那些**该去哪个仓做**。

## 1. 一句话

`docs/transport-contract.md` 的 H 表中，宿主 journal/H6/H8/H13 等已有本仓证据；**H13 的 pending cancel 已收口（见 §5.3）**，本仓剩下的欠账是 §5 表里的「H14/H6 serve 接线」那一条。其余多数条目需要 `../dsh-ui` 一起参与，不能用宿主侧证据替代跨仓验收。

## 2. 证据在哪

```bash
python scripts/h-suite.py           # 13 条探针，一条命令
python scripts/h-suite.py --fast    # 不需要模型的 7 条（约 1 分钟）
```

每条探针都必须交出**机器可读的汇总行**；交不出来（崩了、只写进文件、被跳过）一律算失败。
"没跑出结论"和"通过"是两件事。

## 3. 本轮（M253–M283）落地的能力

| 能力 | 关键证据 |
|---|---|
| H14 每会话 journal：seq 单源、WS 与 history 同源 | `TestJournalSeqIsPerSession`、`TestTurnSeqContinuesFromEvents` |
| history 返回**原始帧** + 世代/水位；`stale_epoch` 409 | `TestHistoryReturnsRawFrames`、`TestHistoryRejectsStaleEpoch` |
| 订阅时世代不符 → `session/reset` | `TestSubscribeWithStaleEpochEmitsSessionReset` |
| 跨仓共享契约夹具 + 重放 | `internal/transport/testdata/ws-golden.jsonl`、`TestGoldenFramesMatchContract` |
| 两会话隔离（真机） | `scripts/h14-isolation.py` 7/7 |
| H13 审批整链：允许/拒绝/超时/重连/以后都允许 | `scripts/h13-*.py` 共 11 项全绿 |
| H6 思考进日志再进流 | `TestReasoningBlockSurvivesEvent`、真机 `流里有 reasoning: True` |
| H12 只有正数才算"知道宽度" | `TestContentWidthContract` |
| 顶层 `content` 那种落法（原来会丢正文） | `TestAssistantMessageBothContentShapes` |
| H8 错误体只有一个构造点、四字段齐全 | `error_contract_test.go` 三条测试（扫全仓 / 语义表 / 扫源码）+ 真机 `stale_epoch` 409，见 §5.2 |

## 4. 四层守卫（都做过"弄坏它必须红"的反证）

| 层 | 守卫 | 不能证明 |
|---|---|---|
| 文本 | `help-guard` | 说明本身对不对 |
| 表格 | `contract-evidence` | 被点名的测试是否覆盖了这条需求 |
| 代码 | `flag-wired` / `method-wired` | 引用/分发到了正确的地方 |
| 行为 | 13 条真机探针 | 界面怎么消费、负载下如何 |

`scripts/guard-selftest.py` 会把每个守卫弄坏一次，**它第一次跑就抓出 `flag-wired` 的假阴性**
（变量名和 flag 名同名时，定义里的字符串被当成了一次"引用"）。

## 5. 剩什么（按仓分责，18:21 复核）

| 条目 | 缺什么 | 在哪个仓 |
|---|---|---|
| ~~H13 pending cancel~~ | **已收口（2026-11-04，见 §5.3）** | ✅ 本仓；跨仓只剩 UI 侧消费待验 |
| H14/H6 serve 接线 | follow 零水位重复（**已修，见 §5.1**）；debug runner 绕过 journal、特殊 replay/reset/pong 形状补测试 | **本仓**；不能用核心 journal 单测代替 |
| H8 stale_epoch | **已收口（见 §5.2）**：四字段由唯一构造点出，扫全仓测试钉住；剩 `../dsh-ui` apiclient 复验 | **本仓**已完成 |
| H14 UI | 快照旧路径/帧/messages、History 带 epoch、共享 golden、reset 与宿主重启 cookie | `../dsh-ui` + 跨仓 |
| H1 / H2 / H9 / H10 | 当前同版本 UI 消费、未知值与真实 home 副本遍历 | `../dsh-ui` + 跨仓 |
| H6 UI 恢复 | reasoning 已有显示证据；仍需断线恢复、去重与同版本复验 | 两仓 |

### 5.1 【已收口】follow 零水位重复：一次 prompt 在界面上变成两三turn

**症状（真机，`scripts/tui-session-e2e.py` 四轮）**：四次 `session.prompt`，
v4 日志里恰好 4 条 `user/message`（seq 2/7/15/23，字节级干净）；
界面收到的 journal 里是 10 条 `user/message`、10 个 `turn/start`（seq 1–66）——
第 k 轮多出前 k-1 轮的历史，看起来像"一次 prompt 跑了三四turn"。

**根因（一处）**：`loopRunner.follow` 每轮从 `offset 0 / lastSeq 0` 起跑，
而 `follow` 的出口就是 `emitFrame`（journal 唯一发号入口），于是**每一轮都把整份
会话日志从头再推一遍**。v4 日志本身是 append-only 且正确的，脏的只有"推给界面"
那一路。子进程那条路没有重复写：父进程**只读**日志，不落盘。

**同一处的第二格**：v4 的 seq 从 **0** 起（`session.Log` 的第一条事件就是 `seq=0`），
而 `follow` 的判据是 `ev.Seq <= lastSeq`。`lastSeq` 初值取 0 时，
**每个会话的第一条事件永远推不出去**——e2e 的日志第一条是 `seq=0` 的
`system/message`，四轮里界面上一次都没有；首条若正好是 `turn/start`，
界面就是"有 user/message、没有 turn/start"。

**改法**（`cmd/dshd/serve_tools.go`，只有这一处）：

- `followBaseline` 在**起子进程之前**取水位：解析会话路径 + 文件长度
  （O_APPEND 的下一帧就从 EOF 开始）。必须在 spawn 之前——子进程一起来就写
  `turn/start`/`user/message`，等 follow 的 goroutine 被调度起来再问"现在多长"
  会把这两帧当历史跳过去。
- `follow` 从水位之后读；`lastSeq` 初值 **-1**，`seq=0` 那条不再被吞。
- 水位同时把已解析的 path 带过去，于是 `done` 之后的那个 drain 分支有了路径：
  以前子进程 200ms 内就退出的轮次（快失败、参数错）**整轮一条帧都没有**，只剩
  `turn/done`。这条是修水位时顺带关掉的，`TestLoopRunnerFastChildStillPushesFrames` 钉住。

**证据**：

| 层 | 证据 |
|---|---|
| 单测（旧实现红、新实现绿） | `cmd/dshd/serve_follow_test.go`：`TestLoopRunnerOnlyPushesThisTurnsFrames`（历史重推：旧 `user/message=2`）、`TestLoopRunnerFastChildStillPushesFrames`（旧整轮 0 帧）、`TestLoopRunnerPushesFirstEventOfNewSession`（旧丢 `seq=0`）、`TestFollowBaseline*` |
| 帧序号（真机） | e2e 每轮 `user/message` 条数 `[1,1,1,1]`；WS 抓包 `turn/start` 4 条、`user/message` 4 条 |
| 日志没被改坏 | 同一份 e2e 会话的 v4 日志仍是 26 条事件、4 条 `user/message` |

**边界**：`codecs/v4` 落盘格式一行没动；`serve_journal.go` 的 `emitFrame` 仍是唯一发号入口；
`modelRunner`（`DSHD_SERVE_MODEL_RUNNER=1` 的调试路）行为未变，它本来就不 tail 日志。

### 5.2 【已收口】H8：错误体只有一个构造点，四字段不再靠手抄

**欠的那一条**：本文件 §5 记的「H8 stale_epoch 特殊错误体缺 `retryable`」。
2026-11-04 先按契约核对，不凭记忆：`docs/transport-contract.md` §2 写的就是
`错误为 {error:{code,message,hint,retryable}}`——**四字段**，没有第二个形状。

**核对结论**：`retryable` 那一处早在 M325（`4be4798`）就补上了，但那只是**手写 JSON 里多抄了一个字段**。
真正没关掉的是"错误体有第二个构造点"这件事：

| 手写错误体的地方（改前） | 形态 | 改后 |
|---|---|---|
| `internal/transport/server.go` `handleSessionHistory` 的 409 | 手写 `map[string]any{"error":…}`，code/hint/retryable 自己写死；gap 分支还有**第二份 hint** | `errorBodyFor("stale_epoch", msg, {journalEpoch,lastSeq})`，hint 只按码给一次 |
| `internal/transport/ws.go` `Upgrade` 的 5 条失败路径 | `http.Error`：**text/plain 一行字**，四字段一个都没有 | 统一信封；握手头不全 → `bad_handshake`(400)，ResponseWriter 不支持 Hijack → `internal`(500) |
| `cmd/dshd-bridge/main.go` 3 处（`{ok:false,error:{code,message}}`） | ACP 信封，不是 Transport 契约 | **未动**：不在任务边界（`cmd/dshd/serve*.go` + `internal/transport`），而且是另一套协议 |

顺带关掉的两类"字段在、语义错"：

- **500 却报 `code=error`**：删除/分支/压缩/读写/配置/列工作区/列模型这些宿主侧失败，
  以前落到兜底码 `error`（`retryable=false`）——客户端拿到 500 却以为"重试没用"。
  现在显式 `internal`（`retryable=true`，hint 让客户端直接重试）。
- **空 hint**：`errorCode` 认不出的消息（"缺 path"、"会话不存在"、宿主原文……）
  以前 `hint=""`，而契约说每个错误都要有下一步。现在兜底 hint 非空；参数错统一
  `bad_request`、找不到东西统一 `not_found`/`no_such_session`。

**收口后的形状**（判定只有一处）：

```go
func errorBody(msg string) map[string]any                     // 码从消息映射
func errorBodyFor(code, msg string, extra ...map[string]any)  // 码由调用方显式给
func errorObject(code, msg string) map[string]any             // 四字段，唯一构造点
func retryableFor(code string) bool                           // internal / busy / stale_epoch
func errorHint(code string) string                            // 没有专门 hint 的码落到兜底句
```

**先红后绿**（`internal/transport/error_contract_test.go`）：

| 阶段 | 结果 |
|---|---|
| 先写测试、实现未改 | `FAIL`，158 条断言：40+ 条 `hint` 为空、`ws.go` 5 条 `http.Error`（text/plain）、`server.go` 手写 error map |
| 实现后 | `ok dshgo/internal/transport` |
| 变异 A：删掉 `errorObject` 里的 `retryable` | 红 255 行（`error 里应当恰好 4 个键，实际 3 个`） |
| 变异 B：把 `stale_epoch` 从 `retryableFor` 去掉 | 红：`code="stale_epoch" 的 retryable=false，语义要求 true` |

测试为什么是"扫全仓"而不是逐条补：

- 三台 `Server` 覆盖三类分支：`Options{}`（501 档）、每个回调都失败（4xx/5xx 档）、带 journal（`stale_epoch` 409 档）；
- 25 个 method × 三种坏 body（`{}` / 坏 JSON / 参数齐全但指向不存在的东西）+ 8 条专项探针；
  本次实跑 **270 次请求，其中 255 条错误响应逐个查了四字段**（这个数也进了断言：
  少于 100 条就判"扫描退化成空转"）；
- 不走 `/api` 的出口也打：未知路径、错动词、没 cookie、错 token、`/ws` 无 cookie、`/ws` 握手头不全、`/debug/stats`；
- 每条 ≥400 的响应断言：Content-Type 是 JSON、`error` 是对象、**恰好**四键、四值非空、
  `retryable` 是布尔且与文件里那张**独立语义表**一致；另外断言"每个 method 至少造出一次错误"，
  否则扫描对它形同虚设；
- 第二条测试扫源码（`internal/transport/*.go` + `cmd/dshd/serve*.go`，跳过 `_test.go`）：
  出现 `http.Error(` 或手写 `map[string]any{"error"` 就红。

**真机（新二进制 + 临时 home + 端口 0）**：先 `python scripts/bin-check.py`（OK=2 FAIL=0），
再起 `tmp/serve-dir/DSH GO.exe serve --home tmp/h8-closeout-home --policy allow`：

```
发送：session.history {"id":"6976ec09-…","sinceSeq":0,"journalEpoch":3846833278451831749}
      （该会话真实 journalEpoch=3846833278451819404）
响应：HTTP 409
{"error":{"code":"stale_epoch","hint":"丢弃旧水位，重新 session.load 拿 journalEpoch/lastSeq，再从 lastSeq 往后订阅（补洞只覆盖内存里还在的帧）","message":"journal 世代或水位对不上，需要重新 load 基线","retryable":true},"journalEpoch":3846833278451819404,"lastSeq":0}
```

同一进程上另两条出口：

```
GET /ws（缺握手头）→ 400
{"error":{"code":"bad_handshake","hint":"WS 握手要带 Upgrade: websocket、Connection: Upgrade、Sec-WebSocket-Key 三个头；先 GET /connect 换 cookie 再连 /ws","message":"缺少 Upgrade: websocket","retryable":false}}

session.load {}（参数错）→ 404
{"error":{"code":"no_such_session","hint":"先 session.list 拿 id；确认 --home 指向的目录里有这个会话","message":"没有这个会话","retryable":false}}
```

**边界**：`codecs/v4`、`internal/kernel`、`modules/`、`cmd/dshtui/`、`internal/schema/` 一行没动；
帧形状与 seq 语义没动（只改错误体构造）：
`internal/transport/testdata/api.snapshot.json` 与 golden 帧**没有更新**——快照只记 method 面与类型键表、
不含错误信封，`TestSessionAPISnapshot` / `TestGoldenFramesMatchContract` 全绿。

**仍未验**：`../dsh-ui` 的 apiclient 按码分支读这四字段（跨仓，属界面仓复验范围）；
`encode_failure`/`empty_result` 判为不可重试，是"宿主 bug 重试无用"的取舍，
若界面希望自动重试一次，需要单独拍板。

### 5.3 【已收口】H13 pending cancel：审批还挂着的时候取消

**欠的那一条**：本文件 §5 记的「H13 pending cancel：cancel→拒绝、工具不执行、
`approval/cancel`、第二轮 prompt」。

**契约里用的是哪个词**：`approval/cancel`。不是自创——`docs/transport-contract.md`
§2 的 type 白名单第 102 行写的就是它（与 `approval/request`、`approval/timeout` 并列），
`docs/h13-approval.md` 的验收表也写的是这条。负载与 `approval/request` **同形**
（带 `requestId`），客户端靠 `requestId` 收掉那个气泡，不需要第二条解码路径。

**病根（三处，都在 `cmd/dshd/`）**：

| 现象 | 根因 |
|---|---|
| 挂着的审批没人结算 | `serve.go` 的 `Cancel` 只调 `prompts.Cancel`：子进程被杀，而 broker 表里那条请求还写着"等你批准"——界面留一个永远答不上的气泡，journal 里没有结局帧，重连还会把它重放出来 |
| 界面上没有哪一帧说"这件事结束了" | 取消路径从来没推过帧；只有 120s 后那条超时 goroutine 会推，而它推的是 `approval/timeout`（**另一件事**） |
| 取消之后可能起不来第二轮 | ① 被杀掉的子进程走的是 `TerminateProcess`，**defer 一行都跑不到**，它 `claimWriter` 建的那把 `.lock` 留在磁盘上；② `Cancel` 取消上下文后**立刻**清 busy，下一轮的子进程可以赶在 `settleTurn` 清锁之前去抢那把锁，直接报"会话正被另一个进程写入" |

还有一处潜在污染：超时 goroutine 没有任何取消信号。每轮都是**新子进程**、requestId
又从 `perm-1` 重新编，取消后第 120 秒那行 `allow:false` 会写进**下一个**子进程的 stdin，
命中它这一轮某个真实请求——用户看到的是"我明明没答，它自己拒了"。

**改法**：

- `serve_approval.go`：`pending` 从"两张表（应答通道 + 重放负载）"合成**一张**
  `requestId → *pendingAsk{payload, ch, stop}`；三条结算路径（回答 / 超时 / 取消）
  统一走 `claim()`（一次性认领，认领不到就闭嘴）。新增
  `CancelSession(sessionID) int`：先 `reply(false)` 写回拒绝，再推 `approval/cancel`；
  关闭 `stop` 让超时 goroutine 收工。新增 `cancelTurn(broker, prompts, sid)`：
  **先**结算审批、**再**取消轮次（顺序反了就是僵尸）。
- `transport_prompt.go`：`liveTurn` 加 `done`；`Cancel` 发完取消信号后**等这一轮真的
  收工**（上限 15s）再清 busy。不持 `p.mu` 等（`clear` 要同一把锁）。
- `serve_tools.go`：新增 `dropStaleLock(path, pid)`（只删 **pid 精确匹配**的锁）与
  `dropChildLock`；子进程非零退出/被取消时**先清锁再补 `turn/end`**，`openForSettle`
  也复用它。

**先红后绿**：

| 层 | 红 | 绿 |
|---|---|---|
| 真机（旧二进制） | `python scripts/h13-cancel-e2e.py` → `OK=6 FAIL=1`，红在「出现 approval/cancel」；`turn/error` 里能看到子进程确实卡在 `@@APPROVAL@@` 上被杀 | 同一脚本 → `OK=9 FAIL=0`（见下） |
| 单测（旧实现隔离在 `/tmp` 的 `git archive HEAD` 副本 + 照抄旧语义的桩） | 6 条新测试**全部红**，红的理由都对：`cancel 该结算 1 条挂起审批，实际 0`、`取消之后界面该收到 approval/cancel，实际 [approval/request]`、`Cancel 在上一轮收工之前就返回了`、`刚退出的子进程留下的锁还在` | `go test ./cmd/dshd -run 'Cancel\|StaleLock\|Broker'` 全绿 |

**诚实记账**：那次真机红里，"第二轮"这三条是**绿的**——被杀子进程的锁被
`settleTurn` 顺手清掉了，探针的第二次 prompt 没踩进窗口。所以"第二轮起不来"**没有被
真机复现**，它是一个窗口（`settleTurn` 要把整份 v4 日志读一遍再补 `turn/end`，
大会话上是几百毫秒级），由 `TestCancelWaitsForTurnToFinish` 与
`TestDropStaleLockOnlyForExitedChild` 钉住语义，不靠"这次没炸"当结论。

**真机证据（新二进制 + 临时 home `tmp/h13-cancel/home` + 端口 0 + `--policy ask`）**：

```
先 python scripts/bin-check.py          → OK=2 FAIL=0
python scripts/h13-cancel-e2e.py        → OK=9 FAIL=0
  OK   出现 approval/request
  OK   session.cancel 返回 cancelled
  OK   出现 approval/cancel                      ① 界面上有一帧明确的结局
  OK   approval/cancel 点名了那条 requestId
  OK   取消后文件不存在（工具没执行）              ② tmp/h13-cancel/home/canceled.txt 不在
  OK   第一轮收工后没有残留的会话写锁
  OK   取消之后第二轮 prompt 被接受                ③④
  OK   第二轮拿到 assistant 消息
  OK   第二轮没撞上会话写锁（没被搞死）
python scripts/tui-session-e2e.py       → OK=35 FAIL=0（审批那条老路没被打坏）
go test ./... -count=1                  → 全绿（含 cmd/dshd 46s）
```

**边界**：`codecs/v4`、`internal/kernel`、`internal/transport`、`modules/`、`cmd/dshtui/`、
`internal/schema/` 一行没动；帧形状只多用了白名单里已有的 `approval/cancel`，
快照/golden 无需更新（`TestGoldenFramesMatchContract` 绿）。`approval/cancel` 的负载
复用 `approval/request` 的原始字节，不新增字段。

**仍未验 / 该别人接的**：

1. `../dsh-ui` 侧消费 `approval/cancel`（本仓没有界面）——TUI 只处理
   `approval/request` / `approval/timeout` / `approval/answered`，收到 `approval/cancel`
   会走 default 分支（不崩，但也不会收掉那个气泡）。**这是界面仓的事**。
2. `docs/transport-contract.md` 的 H13 行与 `docs/h13-approval.md` 的状态头仍写着
   "pending cancel 未闭环"——契约表由唯一 owner 改，本轮没动。
3. `scripts/h13-cancel-e2e.py` 还没接进 `h-suite.py` / 门禁（脚本目录不在本轮边界内）。
   **谁会跑**：H13 任何改动后手动跑；接套件时它属于"要模型"的慢档。

## 6. 两条已知未验证（写清楚，不让它悄悄变成"应该没问题"）

1. **`-race` 从未跑起来**：本机无 C 工具链（`CGO_ENABLED=0`，`gcc`/`clang`/`cl` 都没有）。
   替代品 `TestConcurrentJournalAndHubInvariants` 能抓死锁/丢帧/seq 错乱，
   **抓不到没有可见症状的数据竞争**。
2. **`allow_always` 只在内存、只在本进程**：重启后重新问。刻意不落盘（跨进程生效会静默放行
   用户不预期的会话），但界面若要"永久记住"，需要另立需求。

## 7. 下一步建议

1. 界面仓按 `ws-golden.jsonl` 写解析器，跑它对同一份夹具的折叠测试——**两边都过才叫契约对齐**
2. 用同一份新二进制 + 临时 home 跑完整工作日，做 A/B 与进程树性能
3. 门禁：把 `h-suite.py` 接进 CI 的 fast 档（真机档太慢，放夜间）

## 8. 【已收口】h10 那次红：不是宿主，是陈旧二进制

结论：**`session.load` 没问题**。红的原因是 `scripts/h10-chain.py` 用的是
`tmp/serve-dir/DSH GO.exe`（17:35 编译），而 `tmp/DSH GO.exe` 是 18:48 的。
同一个脚本换成新二进制 → **6/6 全绿**。

顺带改了两处让这类事不再难查：

- h10 里加上「prompt 被接受」的断言：如果 prompt 根本没被接受，后面 load 不到
  assistant 是**必然**，不该记成"宿主丢了消息"
- h10 的等待窗口（120s）必须**明显短于套件超时**，否则超时后套件拿到的是
  "没有汇总行"，看起来像探针崩了；套件现在也会先大字报一句
  「N 条探针没有给出结论」

下面是当晚查错过程的原始记录，保留是因为**那段推理本身有价值**：

## 8.1 原始记录（问题描述，已解决）

`scripts/h10-chain.py` 的「prompt 之后能 load 到 assistant 消息」**从绿变红**，
把等待从 60 秒加到 240 秒仍然红——**所以这次不是探针耐心的问题**。

直接复现（临时 home、`--policy allow`、一个新会话）：

- `session.prompt` 之后 `session.history` 里**有** `turn/start … assistant/message … turn/done`（20 帧）
- 但 `session.load` 里 assistant 消息仍然是 0
- serve 的 stderr 打出 `会话：…\session-e19bce7d…`，而 `session.create` 返回的是**另一个** id

初步判断：`serve` 起子进程时 `--session <id>` 没让子进程落回那个会话，
于是"新会话写在别处、load 原会话什么都没"。
**尚未定位到具体那一段代码，也未修**，下一轮从这里开始：

1. 读 `cmd/dshd/serve_tools.go` 的 args 组装与 `cmd/dshd/main.go` 的 `--session` 处理
2. 判据：`session.create` 拿到的 id 必须与子进程 stderr 里那行「会话：」的 id 相同
3. 修好后 `h10-chain.py` 回到 5/5
