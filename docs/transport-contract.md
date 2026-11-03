# Transport 契约与 H1–H14 证据

> 这是宿主与原生界面的唯一跨线需求/证据通道。稳定契约不得由单方修改；状态是 **2026-11-03 快照**，不是派工单。开工先看两仓源码、测试、工作树和 `PLAN.md` §14.1；发现更新实现时先验证、再更新本表，不回滚、不重复施工。
> 跨仓证据必须同时记录 `dshd_rev`、`ui_rev`、临时 home/配置、启动与清理命令、真实断言、证据路径、未验证项。✅ 只证明所写层级，不自动证明另一端或整链。

## 0. 改完跑什么（一条命令）

```bash
python scripts/h-suite.py          # 7 条真机探针，36 项，对回本表
python scripts/h-suite.py --fast   # 不调模型的（约 30 秒）
```

它要求每条探针交出**机器可读的汇总行**；交不出来（崩了、只写进文件、被跳过）一律算失败——
"没跑出结论"和"通过"是两件事。

**2026-11-03 全量记录**：10 条探针 / 60 项断言全绿（help-guard 15、contract-evidence 2、
h11 4、h10 5、h14 7、h13 审批 2+4+3+2、ui-e2e 16）。

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
- **type 白名单**：v4 原始事件名 + 显式登记的 `turn/done`、`turn/error`、`approval/request`、`approval/timeout`、`approval/cancel`、`session/reset`、`pong`。新增必须 snapshot-first。
- **连接拓扑**：当前 ServerFrame 不带 topic，每会话一条独立 WS 订阅/连接，按连接归属隔离；不得同连接混订多个会话。
- **seq**：每会话一个 Transport journal，事件只在追加时分配一次单调 seq。不是 v4 `ev.Seq`、全局 `serveTurnSeq` 或连接序号。turn/done、turn/error 与 approval 事件同入此域；pong 无 seq 且不入日志。
- **恢复**：`session.load` 是首屏视图投影，同时返回 `journalEpoch/lastSeq`；不能用于断流补洞。`session.history` 收 `id/sessionId + journalEpoch + sinceSeq + limit`，返回同一 journal 的原始 `frames:[]ServerFrame` 与当前 epoch，不是投影 messages。
- **重启**：epoch 不匹配时宿主发无 seq 的 `session/reset{journalEpoch,lastSeq}`，history 返回 `stale_epoch`；客户端丢弃旧水位并重新 load。reasoning/tool/approval 必须可恢复，pending approval 由 serve 重放真实请求，UI 不编造。
- **断号**：客户端按同一会话 seq 检测，只 history 补缺，不整页 load，不等待宿主新增 gap 帧。

## 3. H 表（需求 + 最近证据快照）

| # | 状态快照 | 需求、已有证据与剩余完成判据 |
|---|---|---|
| **H1 usage** | 🟡 宿主侧已验（M245），界面消费未验 | `usageTotal{inputTokens,outputTokens,totalTokens,cacheReadTokens}` 是**全会话累计**，`contextTokens` 是**最近一条** totalTokens（累计一直涨、上下文随压缩回落，两件事）。证据：`TestLoadSessionUsageTotalIndependentOfTail`（tail=1 与 50 合计一致）、真机同会话两次 load 得到同一组 `input 7409 / output 211 / total 7620`。**仍未验**：界面实际消费这两个数、未知时不补零 |
| **H2 time** | 🟡 宿主侧已验（M247），界面消费未验 | 消息与**流帧**都带墙钟 `time`。证据：40,231 条真实事件 `time<=0` 0 条、回退 0 处；WS 修前 4/5（`turn/done` 终局帧漏 time）→ 修后 5/5 且单调不减；`TestServerFrameCarriesTime`。**仍未验**：界面遇到缺时/非正间隔时显示未知；相邻 assistant 差值含等待与工具耗时，只能当区间估算 |
| **H3 ViewSpec** | ✅ 提供端历史证据 | `messages[].view` 透传 card/table/plain，nil 省略；联调仍需核三种 view 与无 view 回退。 证据：`TestLoadPassesViewThrough` |
| **H4 model** | ✅ 提供端历史证据 | 取最近一条非空 `request/context.model`；UI LoadResult/顶栏实际消费，空值不画。 证据：`TestLoadCarriesModelOrOmitsIt` |
| **H5 permission** | ✅ 提供端字段存在 | 是当前进程 policy（deny/ask/allow/auto），不是 mode；UI 显示真实 policy，空值不画。 证据：`TestLoadCarriesProcessPolicy` |
| **H6 原始流** | ✅ 宿主侧闭环（2026-11-03 M265） | 两处修复：① `dshd run` 原来在 `Emit` 里**没有 reasoning 分支**，思考被当场丢弃（这就是"27,983 条 assistant 里 0 条带思考块"的真正原因，与模型无关）；② 网关支持 `reasoning` 参数但宿主从不发。现在 `Loop.Reasoning` ← `DSHD_REASONING_EFFORT`，思考作为 content **首块**写入同一条 `assistant/message` 证据：`TestReasoningBlockSurvivesEvent` | 真机（`DSHD_REASONING_EFFORT=max`）：盘上 `assistant/message 含 reasoning 块: true`；经 serve 的帧里 `流里有 reasoning: True`，顺序为 reasoning → text。仍未验：界面实际渲染 reasoning |
| **H7 content** | ✅ 两种落法有单测 | 固定已完成且原日志确有 assistant 的样本验 `data.content` 与 `data.message.content`；失败/取消/在途可合法无 assistant。 证据：`TestAssistantMessageBothContentShapes`、`TestAssistantMessageKeepsToolCalls`。**2026-11-03 补测时发现顶层 `content` 那种落法根本没被认**（正文投影成空串），已修。|
| **H8 HTTP 响应** | ✅ 宿主侧闭环（2026-11-03 M262） | 任意成功类型包 `result`；**所有错误出口四字段**——含序列化失败：`writeJSON` 的兜底正文改走 `errorBody`，`encode_failure` 有可照做的 hint。测试 `TestWriteJSONEncodeFailureHasFourFields`（用 chan 触发真失败）、`TestWriteJSONEnvelopesAnyType`（map/struct/slice/pointer）、`TestEncodeFailureHintIsActionable` | 仍未验：真实 apiclient 对契约夹具复验（跨仓）。 |
| **H9 load 入参** | 🟡 宿主侧已验（M243），界面只发契约名未验 | `id`/`sessionId`、`limit`/`tailMessages` 都收；sessionId 优先 id，limit 优先 tailMessages。证据：`TestLoadAcceptsContractNames`（只发 id/tailMessages 时 limit=2 生效）。**仍未验**：界面只用契约名调一次 |
| **H10 list→load** | 🟡 宿主侧 5/5（M244），跨仓未验 | `scripts/h10-chain.py`：同一新二进制 + 全新临时 home，create 给 id、list 含它、**list 里每个 id 用两种入参名（`sessionId` / `id`+`tailMessages`）都能 load**、prompt 后能 load 到 assistant、响应是 `{result:…}` 信封 —— OK=5 FAIL=0。**仍未验**：真实 home 全量 id 遍历（home 只读，要把项目目录复制到临时 home）与界面消费 |
| **H11 ready stdout** | ✅ 进程级 4/4 | 完整 stdout 恰好一行 ready，后续为合法 JSON 且含 url/token；policy 诊断只去 stderr。 证据：`scripts/h11-stdout.py` |
| **H12 contentWidth** | ✅ 宿主侧闭环（2026-11-03 M263） | 只有**正数**才算"知道宽度"：0 与负数都按未知处理、字段直接不出现（负数放过去会让界面画出负宽度容器：怪但没崩，没人会去查宿主）。值以裸数字传输 证据：`TestContentWidthContract` | 真机：`--content-width 748` → `contentWidth=748`；不传 → 字段不存在。单测三条（0/748/负数）+ 一条"必须是数字不是字符串"。仍未验：原生壳提供真实列宽并消费 |
| **H13 真审批** | ✅ 宿主侧整链（2026-11-03 M258–M261） | `session.approve`；子进程 `@@APPROVAL@@` 标记通道；父进程 broker 逐行扫 stderr → `approval/request`（进 journal 可重放）；120s 超时按拒绝并推 `approval/timeout`；`allow_always` 按「会话+工具」记忆且只在内存/本进程。证据：`scripts/h13-approval-e2e.py`（允许→文件真出现 2/2）、`scripts/h13-timeout.py`（4/4）、`scripts/h13-replay.py`（断线重连 3/3）、`scripts/h13-allow-always.py`（只问一次 2/2）、`TestBrokerResolveWritesAllowBack`。**仍未验**：界面实际回传（本仓无界面） |
| **H14 契约门** | 🟡 宿主侧闭环（M253–M277），**界面仓那一半未动** | 已落地：每会话 journal（seq 单源）、history 返回原始 `frames`+`journalEpoch`/`lastSeq`、`stale_epoch` 409、订阅过期发 `session/reset`、`load` 带水位与世代、共享夹具 `internal/transport/testdata/ws-golden.jsonl` 与重放测试、两会话隔离。证据：`TestJournalSeqIsPerSession`、`TestHistoryReturnsRawFrames`、`TestSubscribeWithStaleEpochEmitsSessionReset`、`TestGoldenFramesMatchContract`、`scripts/h14-isolation.py`（7/7）。**仍未验**：界面仓换 `/ws`+裸参数+消费 frames（不在本仓） |

H14 通过后才能做 H6/H13 的线上联调。H8 可与 H14 并行；其余独立字段消费按任务书并行，不从本表符号自行推断任务。
