# Transport 契约与 H1–H14 证据

> 这是宿主与原生界面的唯一跨线需求/证据通道。稳定契约不得由单方修改；状态是 **2026-11-03 快照**，不是派工单。开工先看两仓源码、测试、工作树和 `PLAN.md` §14.1；发现更新实现时先验证、再更新本表，不回滚、不重复施工。
> 跨仓证据必须同时记录 `dshd_rev`、`ui_rev`、临时 home/配置、启动与清理命令、真实断言、证据路径、未验证项。✅ 只证明所写层级，不自动证明另一端或整链。

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
| **H1 usage** | 🟡 两端有分项证据，同版本整链待验 | `usageTotal{inputTokens,outputTokens,totalTokens,cacheReadTokens}` 是全会话累计；`contextTokens` 是最近一条 totalTokens。tail=1/50 合计不变的单测/真机记录已有。UI 状态栏已有真机数字证据；仍需同版本联合复核与未知不补零。 |
| **H2 time** | 🟡 宿主侧有证据，UI 待验 | 消息与流帧带墙钟 time；40,231 真实事件缺时/回退为 0，终局帧 time 已补。缺时/非正间隔 UI 显示未知；相邻 assistant 差值含等待/工具，只能称区间估算。 |
| **H3 ViewSpec** | ✅ 提供端历史证据 | `messages[].view` 透传 card/table/plain，nil 省略；联调仍需核三种 view 与无 view 回退。 |
| **H4 model** | ✅ 提供端历史证据 | 取最近一条非空 `request/context.model`；UI LoadResult/顶栏实际消费，空值不画。 |
| **H5 permission** | ✅ 提供端字段存在 | 是当前进程 policy（deny/ask/allow/auto），不是 mode；UI 显示真实 policy，空值不画。 |
| **H6 原始流** | ✅ 宿主侧闭环（2026-11-03 M265） | 两处修复：① `dshd run` 原来在 `Emit` 里**没有 reasoning 分支**，思考被当场丢弃（这就是"27,983 条 assistant 里 0 条带思考块"的真正原因，与模型无关）；② 网关支持 `reasoning` 参数但宿主从不发。现在 `Loop.Reasoning` ← `DSHD_REASONING_EFFORT`，思考作为 content **首块**写入同一条 `assistant/message` | 真机（`DSHD_REASONING_EFFORT=max`）：盘上 `assistant/message 含 reasoning 块: true`；经 serve 的帧里 `流里有 reasoning: True`，顺序为 reasoning → text。仍未验：界面实际渲染 reasoning |
| **H7 content** | ✅ 两种落法有单测 | 固定已完成且原日志确有 assistant 的样本验 `data.content` 与 `data.message.content`；失败/取消/在途可合法无 assistant。 |
| **H8 HTTP 响应** | ✅ 宿主侧闭环（2026-11-03 M262） | 任意成功类型包 `result`；**所有错误出口四字段**——含序列化失败：`writeJSON` 的兜底正文改走 `errorBody`，`encode_failure` 有可照做的 hint。测试 `TestWriteJSONEncodeFailureHasFourFields`（用 chan 触发真失败）、`TestWriteJSONEnvelopesAnyType`（map/struct/slice/pointer）、`TestEncodeFailureHintIsActionable` | 仍未验：真实 apiclient 对契约夹具复验（跨仓）。 |
| **H9 load 入参** | ✅ 宿主单测，UI 待验 | id/sessionId、limit/tailMessages 均收；sessionId 优先 id，limit 优先 tailMessages。仅 id/tailMessages 时 limit=2 已测；UI 只发契约名复验。 |
| **H10 list→load** | 🟡 宿主 5/5，跨仓待验 | 同新二进制/临时 home：create→list→每个 id 两种名字 load→prompt→再 load 已验。脚本绕开 UI apiclient，不能证明跨仓；真实 home 副本遍历与 UI 消费待做。 |
| **H11 ready stdout** | ✅ 进程级 4/4 | 完整 stdout 恰好一行 ready，后续为合法 JSON 且含 url/token；policy 诊断只去 stderr。 |
| **H12 contentWidth** | ✅ 宿主侧闭环（2026-11-03 M263） | 只有**正数**才算"知道宽度"：0 与负数都按未知处理、字段直接不出现（负数放过去会让界面画出负宽度容器：怪但没崩，没人会去查宿主）。值以裸数字传输 | 真机：`--content-width 748` → `contentWidth=748`；不传 → 字段不存在。单测三条（0/748/负数）+ 一条"必须是数字不是字符串"。仍未验：原生壳提供真实列宽并消费 |
| **H13 真审批** | 🟡 端点与子进程通道有证据，父进程/整链待验；依赖 H14 | 设计见 `docs/h13-approval.md`。M249 有 approve 端点与 3 单测；M250 有子进程标记通道与 5 测试；M252 记录父进程分帧缺口。仍需 serve 正确桥接、允许执行、拒绝不执行、120s 超时、session.cancel 及 UI 回传；handler/子进程单测不算整链。 |
| **H14 契约门** | 🟡 核心迁移已落地，接线/验收中 | M253 已有每会话 journal/世代号核心与 6 测试；UI 已有 `/ws`+裸参数迁移及真模型/工具到界面的提交。仍需按 §1–§2 冻结完整契约，核对接线、history 原始 frames、真实 apiclient 对契约夹具的合成 reasoning/tool/approval 重放、两会话与 stale_epoch。**真实生成归 H6，真实审批执行归 H13，UI 产品消费归 G2。** **同机隔离已实测（M267）**：`scripts/h14-isolation.py` 开两个会话、两条独立 WS、同时发——A/B 各自 `seq` 从 1 开始、互不出现对方暗号、两边都收到 `turn/done`，**OK=7 FAIL=0**。|

H14 通过后才能做 H6/H13 的线上联调。H8 可与 H14 并行；其余独立字段消费按任务书并行，不从本表符号自行推断任务。
