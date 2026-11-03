# Transport 契约与 H1–H14 证据

> 这是宿主与原生界面的唯一跨线需求/证据通道。稳定契约不得由单方修改；状态是 **2026-11-03 快照**，不是派工单。开工先看两仓源码、测试、工作树和 `PLAN.md` §14.1；发现更新实现时先验证、再更新本表，不回滚、不重复施工。
> 跨仓证据必须同时记录 `dshd_rev`、`ui_rev`、临时 home/配置、启动与清理命令、真实断言、证据路径、未验证项。✅ 只证明所写层级，不自动证明另一端或整链。

## 0. 改完跑什么（一条命令）

```bash
python scripts/h-suite.py          # 7 条真机探针，36 项，对回本表
python scripts/h-suite.py --fast   # 不调模型的（约 30 秒）
```

验收规矩（二进制版本、"没结论≠通过"、守卫必须可反证、`-race`）见
[`docs/verify-rules.md`](verify-rules.md)。

跑之前第一条永远是 `bin-check`：**探针用的二进制比源码旧、或者两份副本分叉，就直接中止**。
2026-11-03 出现过一次：h10 的红被记成"宿主丢消息"，实际是探针用着 70 分钟前的二进制。
**探针跑的是已经不存在的版本，结论却记在当前代码头上**——这比大多数 bug 都贵。

它要求每条探针交出**机器可读的汇总行**；交不出来（崩了、只写进文件、被跳过）一律算失败——
"没跑出结论"和"通过"是两件事。

**2026-11-03 全量记录**：10 条探针 / 60 项断言全绿（help-guard 15、contract-evidence 2、
h11 4、h10 5、h14 7、h13 审批 2+4+3+2、ui-e2e 16）。

### `-race` 已经能跑了（2026-11-03 晚）

装了 MinGW-w64 GCC 16.2.0 之后，`bash scripts/race.sh` **全仓 39 个包全 ok、0 DATA RACE**。
第一次跑就抓出三处测试夹具的竞争（`fakeStdin` / `emitted` / `asked` 都是裸变量），已修。
细节与两个坑见 `docs/race.md`。

下面这段是**装工具链之前**的记录，保留下来是因为它说明了为什么这条长期没验：

### `-race` 为什么跑不起来（2026-11-03 实测）

本机**没有任何 C 工具链**：`go env CGO_ENABLED` = 0，`gcc` / `cc` / `clang` / `cl` 都不存在。
`go test -race` 因此从来跑不起来——按 AGENTS §5，这**就是未验证**，不是"应该没问题"。

替代品 `TestConcurrentJournalAndHubInvariants`：4 会话 × 200 帧并发写、4 个读者并发取、
订阅/退订交错，断言每会话 seq 正好 1..N、退订后不再收帧。
它抓得到**死锁、丢帧、seq 错乱、并发写 map 的 panic**，
**抓不到没有可见症状的数据竞争**——这条边界必须记着。

### 守卫的边界（每层能证明什么、不能证明什么）

| 层 | 守卫 | 能证明 | **不能证明** |
|---|---|---|---|
| 文本 | `help-guard` | 三入口有同一组开关；某几句过期说法不再出现；每个 flag 有说明 | 说明本身是否**正确**（它只查有没有、以及已知错的说法） |
| 表格 | `contract-evidence` | ✅ 点名的测试/脚本**真实存在** | 那个测试是否真的覆盖了这条需求 |
| 代码·开关 | `flag-wired` | 变量被引用过 | 引用到了**正确的地方**（传了但没生效照样绿） |
| 代码·方法 | `method-wired` | 声明/分发/快照三方对齐 | 分发出错时行为对不对 |
| 行为 | 真机探针 | 端到端跑得过 | 界面侧怎么消费；负载/长跑下的表现 |

看这张表的方式：**上面几层便宜、每次能跑；下面那层慢、但只有它算数。**
任何一层都不可以说"所以整链就对了"。

**新增守卫的规矩**：任何守卫都要在 `scripts/guard-selftest.py` 里加一条
"弄坏它、它必须红"。跑不红的守卫等于没有守卫——而且它看起来正在检查，
没有任何外部信号能反驳这一点。本轮自测第一次跑就抓出 `flag-wired` 的假阴性：
变量名和 flag 名同名时，定义里那个字符串被当成了"引用"。

**关于红的第一判断**：连着跑比单跑更容易红，因为网关还在处理上一条的请求。
看到 `6/7` 先看**是哪一条**——如果前面全过、只差"两边都收到 turn/done"，
那是**探针的耐心**不够，不是宿主串流。2026-11-03 就误判过一次：
把 120 秒的超时当成宿主的 bug。修探针时要求它同时**说清哪一边没等到**。

## 1. 变更与验收顺序

1. 一个契约 owner 串行修改本文件、两份快照和 golden，记录两仓 base revision；冻结后宿主与 UI 才能按各自文件范围并行。共享 mockd/快照不多人同时改。
2. 快照：宿主 `internal/transport/testdata/api.snapshot.json`；UI `../dsh-ui/testdata/api.snapshot.json`。快照不替代行为测试。
3. 同一 golden 驱动宿主、UI apiclient、mockd。跨仓调用必须经真实 UI `internal/apiclient`，不得用手写 JSON 绕过客户端制造假绿。
4. H14 只要求契约夹具与合成帧重放，**不要求真实模型生成或真实工具审批执行**；真实流归 H6，允许/拒绝执行归 H13，UI 消费归 U1/G2。

## 2. 当前 canonical 契约（H14 冻结目标）

- **HTTP**：`POST /api/<method>`，body 直接是该 method 的参数对象，不包 `{"params":…}`；成功为 `{result:…}`，错误为 `{error:{code,message,hint,retryable}}`。
- **认证**：launch token 经 `/connect` 换 cookie；HTTP 与 WS 同 cookie；先 Connect 再 list/load/subscribe。
- **WS**：路径 `/ws`。客户端控制帧为 `t=sub/unsub/ping`，sub 带 `topic/session:<id>`、`sinceSeq`、`journalEpoch`。服务端事件为 `{seq,type,data,time}`，不是 `{t:"ev",kind:…}`。
- **`approval/request` 的 data 里有两个时间字段**（2026-11-04 加，只加不改）：
  `timeoutMs`（这次等多久）与 `deadlineMs`（墙钟截止，Unix 毫秒）。
  为什么两个都发：断线重连**重放的是当初那条负载**，只发时长会让客户端把剩余时间重新算成满额
  （屏上还有 30s、宿主 10s 后就拒），那比没有倒计时更糟。
  老宿主没有这两个字段时，客户端**不许编秒数**，只能显示"倒计时不可用"。
- **type 白名单**：v4 原始事件名 + 显式登记的 `turn/done`、`turn/error`、`approval/request`、`approval/timeout`、`approval/cancel`、`session/reset`、`pong`。新增必须 snapshot-first。
- **连接拓扑**：当前 ServerFrame 不带 topic，每会话一条独立 WS 订阅/连接，按连接归属隔离；不得同连接混订多个会话。
- **seq**：每会话一个 Transport journal，事件只在追加时分配一次单调 seq。不是 v4 `ev.Seq`、全局 `serveTurnSeq` 或连接序号。turn/done、turn/error 与 approval 事件同入此域；pong 无 seq 且不入日志。
- **消息身份（MSGID P0，待契约 owner 冻结字段）**：Transport seq 只用于水位、gap 和重放，不得作为 v4 内容身份。load↔WS 去重需要 data 内携带原始 `v4Seq` 或 `messageId`（工具另需 `turnId/callId`）；当前 `cmd/dshd/serve_journal.go:21-33` 只透传原始 data，而 `internal/session/log.go:183-198` 的 assistant data 没有 v4 seq/messageId。**字段未冻结前，UI 只能用 scoped fallback 防止误覆盖，不得宣称缺真值时可精确去重**；新增字段必须先改契约、两边快照/golden 与还原测试。
- **恢复**：`session.load` 是首屏视图投影，同时返回 `journalEpoch/lastSeq`；不能用于断流补洞。`session.history` 收 `id/sessionId + journalEpoch + sinceSeq + limit`，返回同一 journal 的原始 `frames:[]ServerFrame` 与当前 epoch，不是投影 messages。
- **重启**：epoch 不匹配时宿主发无 seq 的 `session/reset{journalEpoch,lastSeq}`，history 返回 `stale_epoch`；客户端丢弃旧水位并重新 load。reasoning/tool/approval 必须可恢复，pending approval 由 serve 重放真实请求，UI 不编造。
- **断号**：客户端按同一会话 seq 检测，只 history 补缺，不整页 load，不等待宿主新增 gap 帧。

## 3. H 表（需求 + 最近证据快照）

| # | 状态快照 | 需求、已有证据与剩余完成判据 |
|---|---|---|
| **H1 usage** | 🟡 宿主侧已验（M245）；UI 状态栏有真机数字，未知/同版本联合待验 | `usageTotal{inputTokens,outputTokens,totalTokens,cacheReadTokens}` 是**全会话累计**，`contextTokens` 是**最近一条** totalTokens（累计一直涨、上下文随压缩回落，两件事）。证据：`TestLoadSessionUsageTotalIndependentOfTail`（tail=1 与 50 合计一致）、真机同会话两次 load 得到同一组 `input 7409 / output 211 / total 7620`。UI `tmp/h1-statusbar.png` 已有非零真值与空 home 隐藏记录；**仍未验**：当前同版本联合复核及未知字段不补零的全部分支 |
| **H2 time** | 🟡 宿主侧已验（M247），界面消费未验 | 消息与**流帧**都带墙钟 `time`。证据：40,231 条真实事件 `time<=0` 0 条、回退 0 处；WS 修前 4/5（`turn/done` 终局帧漏 time）→ 修后 5/5 且单调不减；`TestServerFrameCarriesTime`。**仍未验**：界面遇到缺时/非正间隔时显示未知；相邻 assistant 差值含等待与工具耗时，只能当区间估算 |
| **H3 ViewSpec** | ✅ 提供端历史证据 | `messages[].view` 透传 card/table/plain，nil 省略；联调仍需核三种 view 与无 view 回退。 证据：`TestLoadPassesViewThrough` |
| **H4 model** | ✅ 提供端历史证据 | 取最近一条非空 `request/context.model`；UI LoadResult/顶栏实际消费，空值不画。 证据：`TestLoadCarriesModelOrOmitsIt` |
| **H5 permission** | ✅ 提供端字段存在 | 是当前进程 policy（deny/ask/allow/auto），不是 mode；UI 显示真实 policy，空值不画。 证据：`TestLoadCarriesProcessPolicy` |
| **H6 原始流** | 🟡 真实生成与 UI 显示已有证据；**跨轮去重/恢复未闭环** | M264/M265 已让 reasoning 进入日志与流，`TestReasoningBlockSurvivesEvent` 和真机 reasoning→text 有证据；UI 产品清单已有 text/reasoning/tool 消费。**2026-11-03 18:21 源码复核**：serve follow 从零水位重发旧事件、debug runner 绕过 journal，且整条 message 到齐不等于逐 token 增量；剩余跨轮不重复、统一 journal/time、断线恢复与同版本 UI 复验 |
| **H7 content** | ✅ 两种落法有单测 | 固定已完成且原日志确有 assistant 的样本验 `data.content` 与 `data.message.content`；失败/取消/在途可合法无 assistant。 证据：`TestAssistantMessageBothContentShapes`、`TestAssistantMessageKeepsToolCalls`。**2026-11-03 补测时发现顶层 `content` 那种落法根本没被认**（正文投影成空串），已修。|
| **H8 HTTP 响应** | 🟡 通用信封/encode_failure 已修；**stale_epoch 特殊出口未闭环** | M262 与 `TestWriteJSONEncodeFailureHasFourFields`、`TestWriteJSONEnvelopesAnyType`、`TestEncodeFailureHintIsActionable` 证明通用 result/error。**2026-11-03 18:21 复核**：`server.go` stale_epoch 特构造 error 只有 code/message/hint，缺 `retryable`，现有测试未查四字段；需统一 errorBody、补失败测试与真实 apiclient 复验 |
| **H9 load 入参** | 🟡 宿主侧已验（M243），界面只发契约名未验 | `id`/`sessionId`、`limit`/`tailMessages` 都收；sessionId 优先 id，limit 优先 tailMessages。证据：`TestLoadAcceptsContractNames`（只发 id/tailMessages 时 limit=2 生效）。**仍未验**：界面只用契约名调一次 |
| **H10 list→load** | 🟡 M289/M290 已澄清陈旧二进制误判；跨仓/真实 home 待验 | M286 的红曾疑似 create id 与子进程 id 不同；M289 查明探针使用 17:35 旧二进制而源码已 18:48，**同新二进制恢复 6/6，非宿主 bug**。M290 把 `scripts/bin-check.py` 设为套件首门并有摸旧必红反证。不得再按 id bug 派修复；仍需真实 home 副本遍历与 UI 消费 |
| **H11 ready stdout** | ✅ 进程级 4/4 | 完整 stdout 恰好一行 ready，后续为合法 JSON 且含 url/token；policy 诊断只去 stderr。 证据：`scripts/h11-stdout.py` |
| **H12 contentWidth** | ✅ 宿主侧闭环（2026-11-03 M263） | 只有**正数**才算"知道宽度"：0 与负数都按未知处理、字段直接不出现（负数放过去会让界面画出负宽度容器：怪但没崩，没人会去查宿主）。值以裸数字传输 证据：`TestContentWidthContract` | 真机：`--content-width 748` → `contentWidth=748`；不传 → 字段不存在。单测三条（0/748/负数）+ 一条"必须是数字不是字符串"。仍未验：原生壳提供真实列宽并消费 |
| **H13 真审批** | 🟡 宿主 allow/deny/timeout/replay/allow_always 已验；**pending cancel 未闭环** | `session.approve`；子进程标记通道；父进程 broker 推 `approval/request` 并入 journal；120s 超时拒绝；allow_always 仅进程内。证据：`scripts/h13-approval-e2e.py`、`h13-timeout.py`、`h13-replay.py`、`h13-allow-always.py`、`TestBrokerResolveWritesAllowBack`；UI 已有 allow/deny 真机记录，`72e4e3d` 另有 allow_always 第二次自动放行证据。**2026-11-03 18:08 复核**：`serve_approval.go` 尚无取消挂起审批分支，缺 cancel→拒绝+不执行+`approval/cancel`+第二轮 prompt 证据，故不签整链完成 |
| **H14 契约门** | 🟡 epoch/reset、后台水位、分批补洞已提交；**快照/golden/serve/重启凭据未闭环** | UI `6363bf3`、`d04bade`、`7e010ab` 等已有新证据；`80a5754` 已关闭首启 epoch/lastSeq；剩余 reset 后局部 since=0、history 409 换代真机、History 请求带 epoch、UI 快照旧 `/api/mux`/`kind`/messages、共享 golden 与特殊事件样本、**MSGID 所需 v4/messageId/callId 内容身份字段**、FrameSub 重放修复提交、serve 跨轮去重/debug 统一、宿主重启 cookie 自愈与同版本联合复验。未提交 wssession 改动不计完成 |

H14 通过后才能做 H6/H13 的线上联调。H8 可与 H14 并行；其余独立字段消费按任务书并行，不从本表符号自行推断任务。

## 4. 后续契约需求登记（来自 CGAP，未冻结、不得冒充已实现）

以下是 `../dsh-ui/docs/conversation-gap.md` §3.2 的跨仓需求。每项必须先由唯一契约 owner 冻结字段/方法、两仓快照与 golden，再让宿主和 UI 并行实现；不得只画本地假状态。

| 能力 | 最小真值需求 | 禁止替代 |
|---|---|---|
| 完整工具树与 Inspect | `callId,parentCallId,phase,argsRaw,result,isError,error,startedAt,endedAt,subCalls` 与原始 Inspect 载荷 | 用 `Tool+Args` 当唯一身份、把最近结果塞给最近调用 |
| 向前历史与 Turn 大纲 | `loadOlder(beforeSeq,limit)` 返回 prepend/hasMore；`loadThrough(turn/start seq)`；`turnOutline{turn,seq,promptPreview,responsePreview}` | 用断号 `session.history(sinceSeq)` 冒充向前分页，或编造未加载预览 |
| 本地提交对账 | prompt 接收并回传 `rpcId`，durable user event 携带同一 rpcId；失败返回可恢复草稿/附件状态 | 永久 `Seq:-1`、失败同时丢草稿和留下假成功气泡 |
| 附件与引用 | 有序 content blocks；attachment id/name/bytes/dimensions；授权读取；file/session reference 稳定身份 | 假缩略图、0 bytes、从标题反推 session id |
| 消息反馈与分支 | messageId；`messageFeedback.list/put/retract` 与 version；`session.fork` 收 `fromSeq` | 只存在内存的 👍/👎、fork 整个会话冒充指定 Turn 分支 |
| Trajectory 计时/统计 | start/end/token timestamps、TTFT/decode、requestNumber、usage routes 与错误详情 | 用墙钟虚构耗时、把消息数当 Turn 数、编 0 tok/s |
| 面板导航（FRAME-GAP C-PANEL） | `panel.open` 路由与已注册 panel registry | 把当前 profile/第三方扩展硬编码成官方固定入口 |
| 团队/子智能体（C-TEAM） | `agentPreset` 与 mode/模型真值；每个会话稳定关联 | 把单一进程 `serveTurnSeq` 或静态“3 个子智能体”当团队真值 |
| 模型选择（MODEL-WIRE） | `session.setModel{id,model}` 后下一轮 request/context 必须使用覆盖，空值回默认，重启持久；catalog 另列。**`648bdea` 目前只有 Set/store，未见 Get 消费** | 只写 `state/session-model.json` 或只加快照就画可点下拉；把上一轮 model 当选择目录 |
| Dock 指标（C-STATS） | turn/assistant-step 真值、cache 命中分子/分母、contextWindow/百分比容量。**`111e250` 已提供 contextWindow（0=未知）；turn/step/cache 分母与 UI 归约仍未闭环** | 用 assistant 消息数当轮、tool 消息数当步、缺分母仍显示缓存率或上下文百分比 |
| 工作区（C-WORKSPACE） | OS 目录选择、workspace add/remove、归档/pin/rename、跨会话搜索 | 把标题包含过滤称为语义搜索，或不改目录却显示已切换 |
| 扩展视图（C-EXTENSION-VIEWS） | 第三方 view descriptor 与实际 painter/数据、按会话选择持久化 | 注册前显示主题鱼/压缩，卸载后残留入口 |

宿主已经提供但 UI 适配层丢掉的 `time`、`turn/error{turnId,error}`、`toolCallId/parentCallId`、原始 Turn/Assistant 事件，不属于新增方法：优先在 MSGID/WP-C 保留并消费，不要误列成宿主待开发能力。
