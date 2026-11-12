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
   2026-11-05 冻结的共享文件是 `contract-golden.json`、`ws-golden.jsonl`、`ws-golden-b.jsonl`：宿主在 `internal/transport/testdata/`，UI 在 `testdata/`，三份必须逐字节同哈希。manifest 固定两个独立 journal/epoch、A 会话 live 缺 4..5、完整 history、B 会话 pending approval 重放，以及裸 history 请求和 `stale_epoch` 四字段错误。`cmd/mockd -contract-golden <manifest>` 只负责重放线上字节；客户端断言仍必须经 `apiclient.Client/Mux/FillGap/Frame.Event/Chunks`。
4. H14 只要求契约夹具与合成帧重放，**不要求真实模型生成或真实工具审批执行**；真实流归 H6，允许/拒绝执行归 H13，UI 消费归 U1/G2。

## 2. 当前 canonical 契约（H14 冻结目标）

- **HTTP**：`POST /api/<method>`，body 直接是该 method 的参数对象，不包 `{"params":…}`；成功为 `{result:…}`，错误为 `{error:{code,message,hint,retryable}}`。
- **认证**：launch token 经 `/connect` 换 cookie；HTTP 与 WS 同 cookie；先 Connect 再 list/load/subscribe。
- **WS**：路径 `/ws`。客户端控制帧为 `t=sub/unsub/ping`，sub 带 `topic/session:<id>`、`sinceSeq`、`journalEpoch`。服务端事件为 `{seq,type,data,time}`，不是 `{t:"ev",kind:…}`。
- **WS 压缩（0.2.2）**：默认可选协商 RFC7692 `permessage-deflate`，双向 no-context-takeover；旧客户端不带扩展头时握手与线上 JSON 路径不变。宿主/TUI/UI 解压后单消息上限均 1 MiB，控制帧不压缩，宿主入站不收分片，非法扩展 fail-loud。精确握手/参数/帧规则与证据见 [`docs/ws-compression.md`](ws-compression.md)。两仓 API 快照和共享 golden 无需新增字段或压缩字节样本：它们冻结解压后的 canonical JSON，不是 Upgrade 头或 DEFLATE 编码。
- **`approval/request` 的 data 里现在还带 `tool`**（2026-11-04 加，只加不改）：
  `allow_always` 的粒度就是（会话 + 工具名），而 `toolCall` 里只有 title/kind/rawInput，
  客户端看不到自己在记住哪个工具。老客户端忽略这个键即可。
- **`approval/request` 的 data 里有两个时间字段**（2026-11-04 加，只加不改）：
  `timeoutMs`（这次等多久）与 `deadlineMs`（墙钟截止，Unix 毫秒）。
  为什么两个都发：断线重连**重放的是当初那条负载**，只发时长会让客户端把剩余时间重新算成满额
  （屏上还有 30s、宿主 10s 后就拒），那比没有倒计时更糟。
  老宿主没有这两个字段时，客户端**不许编秒数**，只能显示"倒计时不可用"。
- **`session.load` 的 `messages[]` 现在带可选 `reasoning`**（2026-11-04 加，只加不改）：
  一条**只有思考、没有正文**的 assistant，`text` 为空，`reasoning` 就是那次回答的全部内容。
  为什么必须有：`session.history` 的原始帧本来就带 reasoning（`serve_turn.go` 推的
  `assistant/message` 帧也带，正文为空时才有），而 `session.load` 的投影原来只取 text 块——
  读回来是一条**空消息**，断线重连/重开等于把这条回答丢了。
  有正文时宿主**不带** reasoning（与推帧同形：那时思考是草稿，不是已经给用户的回答）；
  空串/缺键都表示"这条没有思考块"。老客户端忽略未知键即可。
  判定"一条消息有没有内容"用宿主同一份判据（正文 / 思考 / 工具调用占一样），
  **三样都没有的消息不会出现在 `messages[]` 里**（以前它会变成一条空气泡）。
  宿主侧快照 `internal/transport/testdata/api.snapshot.json` 已在 `Message` 键表尾部
  加上 `reasoning`（只加不改、顺序未动）。
  **UI 侧不需要为这个可选键改快照**：`../dsh-ui/testdata/api.snapshot.json` 当前已是 25 个 method 并含完整 stream/error 段，但没有宿主那种 `types.Message` 字段键表，不追踪消息内部字段。
  所以这不是"两仓都要改的契约变更"，而是"宿主多给一个可选键、老 UI 忽略即可"。
  UI 什么时候开始消费它，是 UI 那边的独立决定（`transport.Message` 已经有这个字段可用）。
- **`session.load` 的 `messages[]` 里 tool 结果现在带真实 `callId/turn/step`**（TOOL-IDENTITY-LOAD / W187，2026-11-06，只加不改）：
  这三个键是 tool 结果与**它那次调用**的关联身份，值直接来自 v4 `tool/result` 的
  `message.toolCallId` 与顶层 `turn`/`step`——不取 Transport seq、不从 tool/call 索引反推、不合成。
  为什么必须有：界面把 load 里 `role=="tool"` 的消息恢复成 `tool.result` 事件，
  判据在 TUI `view-spec.ts` 的 `dshGoLoadToolResult`——`callId` 非空、`turn`/`step`
  都是有限数字，三者缺一就返回 `undefined`（该函数注释写明这是 intentional no-event）。
  没有它们，断线重连/重开走 `session.load` 时工具结果整条消失（W182 真机红灯）。
  **无真值就整个省略**（JSON 指针 nil 才省略）：不许写 `0`——`0` 是有限数字，
  界面会拿它当"第 0 轮第 0 步"去恢复一条编出来的结果；老日志/非 v4 来源因此仍走
  "这条恢复不出事件"的诚实分支。user/assistant 消息不带这三个键。
  宿主快照 `internal/transport/testdata/api.snapshot.json` 的 `Message` 键表在尾部追加
  `callId/turn/step`；canonical 25、WS/journal 帧与三份共享 golden 均不变。
- **`session.load` 的 `messages[]` 里 tool 结果现在带真实 `isError`/`meta`/`structured`**（TOOL-RESULT-ENVELOPE / W190，2026-11-06，只加不改）：
  这三个键是工具结果**信封**的其余部分，值直接来自 durable `tool/result`：`isError` 取
  `message.isError`（**只认字面量 `true`**），`meta`/`structured` 取顶层同名键的**原样字节**
  （真实 v4 的 `meta` 就是 `{"cwd":…}`；`structured` 的形状由工具自己定，宿主不解释）。
  为什么必须有：界面把 load 里 `role=="tool"` 的消息恢复成 `tool.result` 事件，而成败就是
  事件上的 `isError`（TUI `view-spec.ts`：`message.isError === true || message.error !== undefined`）。
  W187 只补了 `callId/turn/step`，于是**失败**结果能恢复成事件、却被画成成功——
  那比丢消息更糟：用户会以为命令真的跑通了。
  **无真值整个省略**：`false` 由 `omitempty` 省略，`meta`/`structured` 缺键与显式 `null`
  都省略——不写 `false`、不写 `null`、不写空对象（界面靠 `undefined` 才区分"没有"）。
  两个字段用 `json.RawMessage` 而不是 typed 字段还有一个原因：`isError` 不是布尔的外来日志
  （`"true"`/`1`）若让 `encoding/json` 解不开，整条工具结果会连同正文一起消失——
  那是"新增字段反而把老日志弄丢"的回归。user/assistant 消息不带这三个键。
  宿主快照 `internal/transport/testdata/api.snapshot.json` 的 `Message` 键表在尾部追加
  `isError/meta/structured`；canonical 25、WS/journal 帧与三份共享 golden 均不变
  （共享 golden 里没有 `session.load.messages[]`，详见 §4.5）。
- **`session.load` 顶层现在带可选 `effort`**（EFFORT-PROJECTION / W74，2026-11-05，只加不改）：
  真值只取这条会话最新的 `request/header.header.config.reasoningEffort` 或 `model/selection.reasoningEffort`，已知枚举为 `off/low/high/xhigh/max`。最新记录缺键、空串、`null`、非字符串、未知枚举，或旧 v4 没有配置记录时都省略 `effort`，客户端显示未知；不得回退旧记录，也不得拿本进程 `--reasoning`、模型名或目录能力冒充。
  v4 header 与 `request/context` 没有 effort，不写 v4、不改 codec、不改原 `model` 投影。`LoadResult` 键表只在尾部追加 `effort`，canonical 25、WS/journal、共享 golden 均不变。TUI 客户端用可选指针区分缺键与显式空串，切换会话先清旧值；详见 [`docs/tui-effort.md`](tui-effort.md)。
- **type 白名单**：v4 原始事件名 + 显式登记的 `turn/done`、`turn/error`、`approval/request`、`approval/timeout`、`approval/cancel`、`session/reset`、`pong`。新增必须 snapshot-first。
- **连接拓扑**：当前 ServerFrame 不带 topic，每会话一条独立 WS 订阅/连接，按连接归属隔离；不得同连接混订多个会话。
- **seq**：每会话一个 Transport journal，事件只在追加时分配一次单调 seq。不是 v4 `ev.Seq`、全局 `serveTurnSeq` 或连接序号。turn/done、turn/error 与 approval 事件同入此域；pong 无 seq 且不入日志。
- **消息身份（MSGID-IDENTITY / W56，2026-11-05 冻结）**：Transport 信封 `seq` 只用于水位、gap 和重放，不得作为 v4 内容身份。v4 信封本来就有持久 `Event.Seq`（`codecs/v4/read.go`；`session.Log.Append` 分配，`OpenSessionAt` 续写恢复），包括合法的 **`seq=0`**；因此 v4-backed `user/message` 与 `assistant/message` 在 follow/drain 投影成 WS/history 帧时，在 `data` **新增必选 `v4Seq`**，值就是该事件信封的 `Event.Seq`。不得用 Transport seq、墙钟、正文 hash 或进程内序号冒充。
  `data.messageId` 是**可选透传**，只允许取持久 data 已有的 `messageId` / `id` / `message.messageId` / `message.id`。当前真实 `user/message.id` 因而同时投影为 `messageId`；当前 assistant 构造器没有持久 message id，**只保证 `v4Seq`，不得编一个 `messageId`**。已有字段不改名、不改值，v4 旧记录一个字节都不重写。
  `session.load.messages[]` 同样在尾部新增可选 `v4Seq/messageId`：对 v4-backed user/assistant，`v4Seq` 总有值；兼容字段 `seq` 保持原义。老宿主/非 v4 来源可以缺字段。W49 的 Transport journal 仍是 `{seq,type,data,time}`，持久格式不变；新帧只是 data 加法。**恢复旧 journal 时不回写、不补造字段**，这些旧帧与缺字段的非 v4 来源只能走 UI 的 Stream+Reset scoped fallback，不宣称 load↔WS 精确去重。
  `turn/done/error.turnId`、`tool/call.callId`、`tool/result.message.toolCallId`、数值 `turn/step` 以及 reasoning/tool/approval/turn-done 的既有身份全部不变。共享 golden 的 A/B 帧按上述规则补 `v4Seq` 与真实 user `messageId`；B 另有两条同文 user 和两条同文 assistant，必须以不同 `v4Seq` 全部保留。快照、golden 与真实 apiclient 重放先于实现冻结。
- **恢复**：`session.load` 是首屏视图投影，同时返回 `journalEpoch/lastSeq`；不能用于断流补洞。`session.history` 收 `id/sessionId + journalEpoch + sinceSeq + limit`，返回同一 journal 的原始 `frames:[]ServerFrame` 与当前 epoch，不是投影 messages。
- **重启**：epoch 不匹配时宿主发无 seq 的 `session/reset{journalEpoch,lastSeq}`，history 返回 `stale_epoch`；客户端丢弃旧水位并重新 load。reasoning/tool/approval 必须可恢复，pending approval 由 serve 重放真实请求，UI 不编造。
- **断号**：客户端按同一会话 seq 检测，只 history 补缺，不整页 load，不等待宿主新增 gap 帧。

## 3. HOST-PLUGIN-MANAGER 受限本地包扩展（2026-11-05，additive）

这一节只新增 `plugin.*` 方法，不改上面的 25 个 canonical method、既有类型字段、WS 帧或 journal 语义。机器快照是 `internal/transport/testdata/plugins.snapshot.json`，完整生命周期与四字段错误 golden 是 `plugins-golden.json`；它们是独立扩展夹具，不冒充 UI 已消费，也不改写 H14 的三份共享 golden。

### 方法与普通 Transport 形状

| method | 裸参数 | 成功 result |
|---|---|---|
| `plugin.list` | `{}` | `{plugins:[Plugin]}`，按 id 排序 |
| `plugin.install` | `{path}` | `{plugin:Plugin}`；安装后默认 `enabled=false` |
| `plugin.enable` / `plugin.disable` | `{id}` | `{plugin:Plugin}`，真实改写持久状态 |
| `plugin.uninstall` | `{id}` | `{id,uninstalled:true}` |
| `plugin.update` | `{id,path}` | `{plugin:Plugin}`；版本必须按 SemVer 严格大于当前版 |
| `plugin.restore` | `{id,version?}` | `{plugin:Plugin}`；省略 version 时选择 `previousVersion` |
| `plugin.reconcile` | `{}` | `{actions:[{transactionId,action,id,detail}],recovered}` |

`Plugin` 恰好含 `id/version/kind/entry/enabled/source/previousVersion`（无前一版时省略最后一项）。`source` 是安装时本地目录的绝对路径，不是市场 URL。HTTP 仍遵循 `POST /api/<method>`、body 直接是参数对象、成功 `{result:…}`；错误恰好 `{error:{code,message,hint,retryable}}`，禁止包 `params`。本包没有新增 WS type，也没有 UI 实现；客户端不得凭这些快照伪造已安装、已启用或版本字段。

### 本任务私有的包格式，不是官方包生态声明

安装/更新只接受调用方明确给出的本地目录，不访问远端市场。目录唯一 manifest 是 `plugin.json`：

```json
{
  "schema": "dsh.local-plugin@1",
  "id": "acme.audit",
  "version": "1.0.0",
  "kind": "wasm",
  "entry": "plugin.wasm",
  "files": {"plugin.wasm": "<64 位小写 sha256>"}
}
```

- `id` 只允许小写字母/数字开头的 `[a-z0-9._-]` 受限串；version 是 SemVer 2.0；kind 只收 `wasm` 或 `process`。
- `entry` 必须出现在 `files`；除 `plugin.json` 外每个常规文件都必须登记 SHA-256，未登记文件、缺文件、hash 不符、重复 JSON 键、重复 id、非法版本均 fail-loud。
- manifest 路径只收 `/` 分隔的相对干净路径；绝对路径、`..`、反斜杠、盘符、symlink 与越界解析一律拒绝。安装只复制并校验字节，**绝不执行入口或任何第三方代码**。

### 原子性、恢复与边界

管理器根目录由宿主显式注入，测试只能是临时 home 下的私有目录。版本包存 `packages/<id>/<version>`，状态存 `state.json`，事务存 `transactions/`。先完整校验并复制到同文件系统 staging，再原子 rename 成不可变版本目录，最后原子替换状态文件；失败时不切换当前版本。更新/恢复保留旧版本目录，restore 只切换已校验的保留版本。卸载先把包目录原子移入事务区，再提交状态，失败可移回。

重启 reconcile 依据事务记录与**实际持久状态**判断提交点：未提交的 install/update 删除新 staging/版本目录并保留旧版；未提交 uninstall 恢复包目录；已提交事务只做幂等收尾。事务损坏或状态矛盾必须报错，不得删除无法归属的包或静默丢记录。

W48 先完成管理器与适配器，W50 已把真实 dshd CLI 与 serve 主分派接线：`PluginHandler` 只在既有 cookie 认证后调用，CLI 使用显式 root，serve 使用 `<home>/plugins`；真实 CLI 13 调用、serve 26 步、A/B home 与启动 reconcile 均通过，证据 `docs/plugin-manager-glue.md`。W64 已由真实 UI apiclient/壳/设置页消费八方法并做写后 re-list 与危险确认，证据 `../dsh-ui/docs/plugin-manager-ui.md`；**UI 动态加载/执行第三方代码、远端市场、真实用户插件目录、全局环境修改仍未实现/未验证**。

## 3A. HOST-SETTINGS-FORM 通用设置扩展（2026-11-05，additive）

本节只新增 `settings.*` / `settings.form.*`，不改 canonical 25、既有字段、WS 或 journal。独立机器夹具为 `internal/transport/testdata/settings-form.snapshot.json` 与 `settings-form-golden.json`；详细字段、存储和实证见 [`docs/settings-form.md`](settings-form.md)。官方依据是 0.2.2-rc.1 `ui-settings/src/client/config-form.ts`：`settings.describe()` 读描述符，`settings.mutate(ns, ops, expectedRevision)` 原子写路径操作。

| method | HTTP 裸参数 | 成功 result |
|---|---|---|
| `settings.describe` | `{}` | `{writable,hasDocument,namespaces,sections,errors}`；namespace 含官方 `ns/schema/value/base/user?/autoGenerate/applies/secrets/revision` |
| `settings.mutate` | `{ns,ops,expectedRevision?}` | 单个官方 `SettingsNamespaceView`；`ops[]` 为 `{op:"set",path:[field],value}` 或 `{op:"unset",path:[field]}` |
| `settings.form.describe` | `{}` | 同 describe，显式提供通用 sections/fields 投影 |
| `settings.form.get` | `{ns}` | 一个 `{ns,title,writable,revision,fields,errors}` section |
| `settings.form.set` | `{ns,path,value,expectedRevision?}` | 写后的 section；不是只回 `{ok:true}` |
| `settings.form.reset` | `{ns,path?,expectedRevision?}` | 清一个字段或整个 namespace 后的 section |

首切只暴露 `permission.defaultPreset`（`deny/ask/allow/auto`）与 `model.defaultModel`（UTF-8 字符串，1..256 字节）。field 投影含 `path/type/current/default/overridden/writable`，以及适用的 `enum/minLength/maxLength`；成功响应的 section/top-level `errors` 均为 `[]`，存储/校验失败走 HTTP 四字段错误，不伪造成功值。未知 top-level 参数、`params` 包装、未知字段、非单段路径、坏枚举、长度越界、错误 JSON 类型全部 fail-loud。revision 是持久乐观锁；冲突回 409 `revision_conflict`，不得覆盖新值。

两项默认值复用 `state/session-permission.json` 与 `state/session-model.json` 中 runner 已读取的同一份 map，以不可能与 session id 冲突的保留键存默认值和 revision，不使用 `ui-config.json` 或新平行配置。`session.create` 将**创建当时**的有效默认物化到该新会话的既有 override 键；之后每轮继续由原 `modelOverrideLoopRunner` / `resolveServeModel` 读取，旧会话不 retroactive 改写，reset 也不清旧会话。HTTP 仍先过现有 cookie 认证，未认证 401 四字段。

## 3B. HOST-MODEL-CATALOG 模型目录扩展（2026-11-05，additive）

本节只新增 `models.catalog.*` 五个方法，不改 canonical 25；尤其既有 `models.list` 仍是网关 OpenAI `GET /v1/models` 的 `{id,owned_by}` 投影，不 repurposed 成持久目录。独立机器夹具为 `internal/transport/testdata/model-catalog.snapshot.json` 与 `model-catalog-golden.json`；完整字段、存储和实证见 [`docs/model-catalog.md`](model-catalog.md)。官方依据是 0.2.2-rc.1 `ui-settings-models`：模型行保留 `id/name/contextWindow/maxTokens/input/reasoningEfforts` 及未识别字段，provider profile 使用 `api/baseURL/apiKeyEnv/models`，credential 只回 configured/writable。

| method | HTTP 裸参数 | 成功 result |
|---|---|---|
| `models.catalog.list` | `{}` | `{revision,models,credentials,active}`；`active={id,revision}` 从 W61 同一默认模型存储投影 |
| `models.catalog.upsert` | `{model,credential?,expectedRevision?}` | 写后的 CatalogView；同 id 部分更新保留未提交字段与未知字段，新 id 追加 |
| `models.catalog.reorder` | `{ids,expectedRevision?}` | 写后的 CatalogView；ids 必须是非空精确排列，raw model 对象不重建 |
| `models.catalog.test` | `{id,baseURL?,api?,credential?}` | `{id,ok,model?,latencyMs}`；只由真实 OpenAI 兼容 chat/completions 响应产生 ok |
| `models.catalog.set-active` | `{id,expectedRevision?}` | `{activeModel,activeRevision,revision}`；expectedRevision  fence 的是 W61 model 默认 revision |

目录存 `state/model-catalog.json`，含 version/revision/有序 raw models/credentials；credential 只在同一原子写中保存，list/upsert/reorder/test 响应绝不回传 literal。模型未知字段与对象键顺序保序 round-trip，已存 raw 行 reorder 不改写。空目录可以 list/首次 upsert，但 reorder/test/set-active 必须 fail-loud；坏/重复 id、缺失或多余 order id、非法已知字段、秘密混入 model、损坏 JSON、revision 冲突均返回四字段错误且不改存储。

`set-active` 不写第二个 active 值，只通过 W61 `settingsFormDefault/settingsFormRevision` 更新 `state/session-model.json`。创建当时物化到 session override 的既有语义不变，因此新会话下一轮与重开读取 active，旧会话不 retroactive 改写。W75 已把 active catalog 条目的 `baseURL/api/apiKeyEnv` 与 stored credential 每轮接入直接 modelRunner 和 loopRunner；无 provider 配置的普通 model id 才回退 `DSHD_LLM_BASE/DSHD_LLM_KEY`，credential 不进 argv、不写全局环境，真实 A/B/重开/redaction 证据见 [`docs/model-provider-routing.md`](model-provider-routing.md)。HTTP 全部先过现有 cookie 认证，未认证 401。

## 3C. HOST-TASK-ACTIONS 日程与后台任务动作（2026-11-05，additive）

本节新增 `schedule.delete`、`schedule.clear`、`jobs.stop`，并**复用** canonical `jobs.output` 增加有界游标与会话归属。`SessionMethods()` 仍是原 canonical 25，方法名、顺序、快照、WS/journal 均未改；独立机器夹具为 `internal/transport/testdata/task-actions.snapshot.json` 与 `task-actions-golden.json`。官方依据是 0.2.2-rc.1 `ui-schedule/src/client/clear-ended.ts` 与 `ui-jobs/src/client/index.ts`：批量动作是 Host 判定 ended 后的 deleteInactive + readback，不是清空活跃任务；jobs 观察/停止都带 session 与 job id。

| method | HTTP 裸参数 | 成功 result / 关键错误 |
|---|---|---|
| `schedule.delete` | `{sessionId,id,expectedRevision?}` | `{sessionId,id,deleted:true,remaining,revision}`；未知/重复删除 404，revision 冲突 409，损坏/写失败 500 |
| `schedule.clear` | `{}` | `{deleted,revisions,schedules}`；只删 `status="ended"` 或已到 `ended_at` 的记录，保留 running/pending/active 与周期任务，成功前完成权威 readback；空目录 `deleted:0` |
| `jobs.output` | `{sessionId,id,cursor?,limit?,waitMs?}` | 原字段加 `cursor/nextCursor/hasMore/total`；游标单位是 Unicode code point，不切坏 UTF-8；默认 limit 16384、最大 65536；游标越过当前输出 400，未知 404 |
| `jobs.stop` | `{sessionId,id}` | `{id,sessionId,stopped:true,done:true,exit_code}`；未知 404、已结束/第二次停止 409、跨 home/会话或映像归属不符 403；停止后由 `jobs.output` 读保留输出 |

`expectedRevision` 与 list 行的 `revision` 是对应会话文件原始字节的 SHA-256；`schedule.list` 每行 additive 透出 `sessionId/status/endedAt/revision`，原全局 list 的容错与排序语义不变。单删先校验再独占临时文件、Sync、同目录 rename。批量清先全量预检，任何损坏文件都在首个写入前失败；多文件写入中途失败按逆序回滚已写文件，回滚失败会合并报错，绝不返回假成功。

生产 `jobs.output` 的 scoped 回调把 `sessionId/cursor/limit/waitMs` 一并传到当前 `cmdRun`，IPC 也只携带当前页；旧 `JobsOutput` 回调仅作兼容分页 fallback。父进程按 session 登记自己启动的子进程 PID/完整 executable/stdin，应答同时核对随机 requestId、sessionId、job id；每次 stop 前用 `QueryFullProcessImageName` 复核精确 PID。禁止按镜像名查找、全局 `taskkill` 或操作其他会话。最终输出以原子写保存在当前 home 的 `state/jobs/<sessionId>/<id>.json`，停止和进程退出后仍可读，A/B home 不共享。

真实 dshd + 临时 home 的 ready token→cookie 已验 schedule A/B、删除/重开、重复删除、冲突、clear-ended/readback；W69/W70 又以 Job Object/process group 与有界 Pager 完成 parent/child/grandchild 进程树停止和输出回读。W73 已由真实 UI apiclient/壳/自动化与后台任务页消费删除、clear-ended、rune 分页、truncated 与 stop/readback，证据 `../dsh-ui/docs/task-actions-ui.md`。完整命令、先红输出、日志与未验项见 [`docs/task-actions.md`](task-actions.md)；**可见 live GUI 与完整工作日仍未验**。

## 3D. 409 的 `details.reason`（2026-11-12，additive）

GP 报告 §5 第 3 条：409 的**两种来源**曾经只能靠 message 文本区分（界面里就是
`strings.Contains(err.Error(), "滑出内存")`），文案一改就失效，而且判错的代价两个方向
都难看——当成换代会重建基线（同一次点击又能再点），当成到头了会少一页。本层给 409 加一个
**稳定机器字段** `details.reason`，**纯 additive**：`error` 仍然恰好四字段（`code`/
`message`/`hint`/`retryable`，`TestEveryHTTPErrorHasFourFieldEnvelope` 钉住），
reason 放在**信封顶层**的 `details` 里（与 `journalEpoch`/`lastSeq`/`capableWhen`
平铺同一层，不是 error 对象的一部分）。

### 线上形状

```json
{"error":{"code":"stale_epoch","message":"…","hint":"…","retryable":true},
 "journalEpoch":7,"lastSeq":1024,
 "details":{"reason":"history_evicted"}}
```

### 取值表（`ConflictReasons()` 是宿主侧唯一来源）

| `details.reason` | 四字段 `code` | 语义 | 客户端下一步 |
|---|---|---|---|
| `stale_epoch` | `stale_epoch` | journal 换代，或客户端水位比当前还大 | 丢掉旧水位，重新 `session.load` 拿基线 |
| `history_evicted` | `stale_epoch` | `sinceSeq` 之前的历史已滑出内存 journal | **不要重建**：这就是"没有更早的了"（分页到头） |
| `revision_conflict` | `revision_conflict` | 乐观并发 revision 过期（读到的是旧版本） | 重新读最新 revision，再基于新内容重试 |
| `job_finished` | `job_finished` | 任务已结束，不能重复停止/操作 | 只读 `jobs.output` |
| `job_owner_not_live` | `job_owner_not_live` | 任务 owner 已不在（官方 `JOB_OWNER_NOT_LIVE`） | 换会话 / 等 owner 恢复 |
| `stale_head` | `stale_head` | forge 写入基于旧 head（远端已前进） | 重新读 PR head 再提交 |
| `agent_busy` | `busy` | 会话/管理器正忙（上一轮还没结束） | 等 `turn/done`，或先 `session.cancel` |
| `NO_CONTINUABLE_FAILURE_OR_PENDING_INPUT` | `session/agent-busy` | 官方续跑不可用（**官方逐字值**，`dsh-api-session-controller/lib/index.js:895`） | 看 `hint`：先等当前轮/排队项消化完 |
| `conflict` | 其它（`conflict`/`unsupported`/`error` 等兜底） | 叫不出更细名字的 409 | 按 `hint` |

产出点（每个都有一条断言，见 `TestConflictProducersCarryReason` 与
`TestHistoryConflictReasons`）：`server.go` 的 `handleSessionHistory`（两种 reason）、
`serveEmptyPromptContinuation`（官方 reason）、`writePromptError`；`handlers_agent_official.go`；
`handlers_config_patch.go`；`handlers_jobs.go`；`handlers_job_terminal_official.go`；
`handlers_forge_write_official.go` / `handlers_forge_rc2.go`；`handlers_session_official.go`；
`context_mcp_sidequery.go`；`handlers_mcp_manager_rc2.go`；`handlers_cua_rc2.go`。
收口在**唯一出口** `writeJSON`（`withConflictReason`）：任何 409 都带 reason，
不靠每个 handler 记得加。

### 兼容规则（老客户端 / 老宿主）

- **老客户端**：忽略顶层 `details` 即可。`error` 四字段、状态码、`message` 文案、
  `journalEpoch`/`lastSeq` 等兄弟字段**全部没变**——这是 additive，不是破坏性变更。
- **老宿主**（本字段之前）：响应里没有 `details`。客户端必须**先判结构、再兜底文本**，
  且兜底只用于跨版本过渡；支持的最低宿主版本 ≥ 本字段之后，文本匹配即可删
  （界面侧判据写在 `../dsh-ui/cmd/app/frames.go` 的 `journalEvictedError` 注释里）。
- **两个 reason 共用一个 code 是刻意的**：`stale_epoch` 是既有客户端的码分支，
  不能为了细分改码；细分只在 `details.reason` 里做。同理 `busy` → `agent_busy`、
  `session/agent-busy` → 官方逐字值。
- **`details` 里其它键**原样保留；调用点显式给的 reason 优先于按 code 的默认推导。

### 机器快照与断言

- 宿主快照 `internal/transport/testdata/api.snapshot.json` 的 `conflict_reasons`
  是取值表本身（从 `ConflictReasons()` 反推，不手抄）；UI 侧
  `testdata/api.snapshot.json` 的 `envelope.details.reason` 是**可选**字段
  （老宿主不带，别写成必填）。
- `TestConflictReasonsTable`（取值非空/不重名/三个必需语义都在）、
  `TestConflictReasonForCode`（code → reason 逐行 + 产出点）、
  `TestWithConflictReason`（信封合并规则：调用点优先、兄弟字段不丢、error 仍四字段）、
  `TestConflictProducersCarryReason`（每个 409 产出点一条断言）、
  `TestHistoryConflictReasons`（两种 `stale_epoch` 真的分得开、成功响应不带 `details`）。
- 出口不变量：`TestEveryHTTPErrorHasFourFieldEnvelope` 对**每一条 409 响应**断言
  `details.reason` 存在且取值在 `ConflictReasons()` 内。

## 4. H 表（需求 + 最近证据快照）

| # | 状态快照 | 需求、已有证据与剩余完成判据 |
|---|---|---|
| **H1 usage** | 🟡 宿主侧已验（M245）；UI 状态栏有真机数字，未知/同版本联合待验 | `usageTotal{inputTokens,outputTokens,totalTokens,cacheReadTokens}` 是**全会话累计**，`contextTokens` 是**最近一条** totalTokens（累计一直涨、上下文随压缩回落，两件事）。证据：`TestLoadSessionUsageTotalIndependentOfTail`（tail=1 与 50 合计一致）、真机同会话两次 load 得到同一组 `input 7409 / output 211 / total 7620`。UI `tmp/h1-statusbar.png` 已有非零真值与空 home 隐藏记录；**仍未验**：当前同版本联合复核及未知字段不补零的全部分支 |
| **H2 time** | 🟡 宿主侧已验（M247），界面消费未验 | 消息与**流帧**都带墙钟 `time`。证据：40,231 条真实事件 `time<=0` 0 条、回退 0 处；WS 修前 4/5（`turn/done` 终局帧漏 time）→ 修后 5/5 且单调不减；`TestServerFrameCarriesTime`。**仍未验**：界面遇到缺时/非正间隔时显示未知；相邻 assistant 差值含等待与工具耗时，只能当区间估算 |
| **H3 ViewSpec** | ✅ 提供端历史证据 | `messages[].view` 透传 card/table/plain，nil 省略；联调仍需核三种 view 与无 view 回退。 证据：`TestLoadPassesViewThrough` |
| **H4 model** | ✅ 提供端历史证据 | 取最近一条非空 `request/context.model`；UI LoadResult/顶栏实际消费，空值不画。 证据：`TestLoadCarriesModelOrOmitsIt` |
| **H5 permission** | ✅ 提供端字段存在 | 是当前进程 policy（deny/ask/allow/auto），不是 mode；UI 显示真实 policy，空值不画。 证据：`TestLoadCarriesProcessPolicy` |
| **H6 原始流** | ✅ 真实生成/UI显示、断线、跨进程原始history与MSGID已验；**完整工作日另验** | W36 真实dshd+UI apiclient `OK=72 FAIL=0`；W34修进程内交接/去重/time；W49新增每会话append-only Transport journal。W56以真实 `Event.Seq` 投影user/assistant `v4Seq`，透传已有user `messageId`，同home重启 `OK=84 FAIL=0 SKIP=0`，追加身份比对 `OK=76 FAIL=0`，16帧逐字恢复且load/history同身份；证据 [`docs/message-identity.md`](message-identity.md)、`tmp/message-identity-restart-final/` |
| **H7 content** | ✅ 两种落法有单测 | 固定已完成且原日志确有 assistant 的样本验 `data.content` 与 `data.message.content`；失败/取消/在途可合法无 assistant。 证据：`TestAssistantMessageBothContentShapes`、`TestAssistantMessageKeepsToolCalls`。**2026-11-03 补测时发现顶层 `content` 那种落法根本没被认**（正文投影成空串），已修。|
| **H8 HTTP 响应** | ✅ 宿主侧已收口（2026-11-04）；剩 `../dsh-ui` apiclient 按码复验 | 错误体**只有一个构造点**：`errorObject` 出 `{code,message,hint,retryable}`；`retryable` 只由 `retryableFor` 按码判（`internal`/`busy`/`stale_epoch` 为 true），`hint` 只由 `errorHint` 给且**兜底句非空**；`/ws` 握手失败的 400 也走同一信封（码 `bad_handshake`），不再是 `http.Error` 的纯文本。证据：`TestEveryHTTPErrorHasFourFieldEnvelope`（三档 `Server` × 25 个 method × 3 种坏 body + 8 条专项 + 不走 `/api` 的出口；本次实跑 270 次请求 / 255 条错误响应，断言恰好四键、四值非空、`retryable` 与独立语义表一致、每个 method 至少造出一次错误）、`TestNoHandwrittenErrorBodies`（扫源码：禁 `http.Error(` 与手写 error map）、`TestRetryableDecisionCoversEveryCode`；变异反证：删 `retryable` → 红 255 行，`stale_epoch` 不可重试 → 红。真机：`session.history` 带过期 `journalEpoch` → 409 `{"error":{"code":"stale_epoch",…,"retryable":true},"journalEpoch":…,"lastSeq":…}`（顶层两键不变）。**仍未验**：界面 apiclient 按码分支消费 |
| **H9 load 入参** | 🟡 宿主侧已验（M243），界面只发契约名未验 | `id`/`sessionId`、`limit`/`tailMessages` 都收；sessionId 优先 id，limit 优先 tailMessages。证据：`TestLoadAcceptsContractNames`（只发 id/tailMessages 时 limit=2 生效）。**仍未验**：界面只用契约名调一次 |
| **H10 list→load** | 🟡 M289/M290 已澄清陈旧二进制误判；跨仓/真实 home 待验 | M286 的红曾疑似 create id 与子进程 id 不同；M289 查明探针使用 17:35 旧二进制而源码已 18:48，**同新二进制恢复 6/6，非宿主 bug**。M290 把 `scripts/bin-check.py` 设为套件首门并有摸旧必红反证。不得再按 id bug 派修复；仍需真实 home 副本遍历与 UI 消费 |
| **H11 ready stdout** | ✅ 进程级 4/4 | 完整 stdout 恰好一行 ready，后续为合法 JSON 且含 url/token；policy 诊断只去 stderr。 证据：`scripts/h11-stdout.py` |
| **H12 contentWidth** | ✅ 宿主侧闭环（2026-11-03 M263） | 只有**正数**才算"知道宽度"：0 与负数都按未知处理、字段直接不出现（负数放过去会让界面画出负宽度容器：怪但没崩，没人会去查宿主）。值以裸数字传输 证据：`TestContentWidthContract` | 真机：`--content-width 748` → `contentWidth=748`；不传 → 字段不存在。单测三条（0/748/负数）+ 一条"必须是数字不是字符串"。仍未验：原生壳提供真实列宽并消费 |
| **H13 真审批** | ✅ 宿主与真实 UI apiclient 链已收口；**可见 GUI 整链未验** | 宿主 `dfd73da` M398 已关闭 pending cancel：cancel→拒绝、工具不执行、`approval/cancel`、本轮收工后放锁，真机 `OK=9 FAIL=0`（`docs/host-side-closeout.md` §5.3）。2026-11-05 `REAL-CHAIN-HEADLESS` 再经真实 UI `ParseApprovalRequest/Approve` 验 allow 真写、deny 不执行、pending cancel 同 requestId、第二轮 `turn/done`、无 `.lock`，`RCH_SUMMARY OK=72 FAIL=0 SKIP=0`（`docs/real-chain-headless.md`）。allow/deny/timeout/replay/allow_always 沿用既有证据；pending approval 重放的 envelope `time>0` 缺口归 W34，不外推可见 GUI/跨进程重启 |
| **H14 契约门** | ✅ **共享golden、进程内交接、跨进程原始history与MSGID已收口** | 两快照25方法一致，三份golden双仓同哈希并经真实UI apiclient重放；W34修replay/sub/time；W49以显式每会话append-only journal恢复原始history；W56冻结并投影真实v4内容身份，load↔WS同一条只显示一次、同文不同身份都保留、乱序重放不串、旧帧明确scoped fallback。最终真实重启 `OK=84 FAIL=0 SKIP=0`、身份比对 `OK=76 FAIL=0`，双仓全量test/vet/build与相关race绿。证据 [`docs/message-identity.md`](message-identity.md)、`tmp/message-identity-restart-final/`；不以load投影替代history |

H14 通过后才能做 H6/H13 的线上联调。H8 可与 H14 并行；其余独立字段消费按任务书并行，不从本表符号自行推断任务。

### 4.1 CONTRACT-GOLDEN 冻结与本轮实证（2026-11-05）

- **版本/范围**：`dshd_rev=af4a49a2817592d7affb99bb3a16c7e03214ba1c`、`ui_rev=3925e84f1524dd101e020d74b1d5bb422d25cf4d`，均为 base+当时共享工作树+本契约 patch；未 commit/push。宿主副本 `C:/Users/Administrator/AppData/Local/Temp/DSH-GO-contract-golden`，UI 副本 `C:/Users/Administrator/AppData/Local/Temp/dsh-ui-contract-golden-isolated`。没有改 `internal/apiclient` 产品代码、serve 产品代码或其他插件，没有启动 GUI。
- **共享字节**（宿主 `internal/transport/testdata/` 与 UI `testdata/` 同相对文件名）：`contract-golden.json` SHA-256 `f90c8a8cfb66d42d31c8dc446e30ef3d2b4730eb51e0d22065ec99d70459f1e8`；`ws-golden.jsonl` `254c474897aa446e605553254068cf4f9f26a0f73ed1952bd5fbed98326db4bc`；`ws-golden-b.jsonl` `59f1c11ca40501de2b200e486777202dfe5dedff28f8382790df61db73cd624e`。跨仓 `sha256sum` 三次成对相同。
- **快照独立核对**：Python 同时读两份 `api.snapshot.json`，断言 25 个方法集相同、flat 参数、`/ws`、server frame 四键且无 `t/kind/topic`、错误恰好 `code/message/hint/retryable`、history 是 `frames+journalEpoch+lastSeq` 而非 messages；退出码 0，汇总 `SNAPSHOT_CANONICAL_OK methods=25 stream=/ws frame=seq,type,data,time errors=4 history=raw+epoch`。

| 工作树 | 本次实际命令 | 退出码 | 真实断言/输出 |
|---|---|---:|---|
| 宿主 | `go test ./internal/transport -run 'TestGolden' -count=1 -v` | 0 | `TestGoldenFramesMatchContract`、`ReplayInOrder`、`GapUsesHistoryNotReload`、`HTTPBareParamsAndErrors`、`MessageIdentityTruth` 全 PASS；覆盖两 journal/epoch、原始帧分批重放、缺 4..5 补洞、裸 HTTP、四字段 stale、已有 MSGID 真值 |
| UI | `go test ./cmd/mockd -run 'TestContract|TestDecodeParams' -count=1 -v` | 0 | 真实 `apiclient` 的 `HTTP裸参数与原始history`、`stale_epoch四字段经APIError冒泡`、`两会话隔离与断号检测`、`FillGap补原始帧与内容重放`、`旧epoch订阅只收reset` 全 PASS；`TestContractSnapshotRemainsCanonical` PASS |
| UI | `go test ./cmd/mockd ./internal/apiclient -count=1` | 0 | `ok cmd/mockd 9.930s`、`ok internal/apiclient 6.448s`；不是手写 JSON 自证 |
| 宿主 | `go test ./... -count=1`；`go vet ./...`；`go build ./...` | 0 / 0 / 0 | 全仓测试含 `cmd/dshd 35.611s`、`internal/transport 0.818s`，随后 vet/build 均成功 |
| UI | `go test ./... -count=1`；`go vet ./...`；`go build ./...` | 0 / 0 / 0 | 全仓测试含 `cmd/mockd 10.069s`、`internal/apiclient 12.012s`，随后 vet/build 均成功 |

**本节历史边界（W33/W49 当时）**：① 当时是 H14 合成夹具与真实 apiclient 重放，不是实际模型生成、工具允许/拒绝执行或 GUI 消费；② 当时 mockd 的 golden `session.load` 只提供 epoch/lastSeq 基线；③ 当时 `v4Seq/messageId` 仍缺，归 W56；④ W34 已补真实 `session/reset` 与 pending approval 重放的 envelope `time>0`；⑤ W49 已用显式持久 journal 修复跨进程原始 history。W56 已按 §2 新契约补真实身份、mockd load 投影与真实重启证据，见 §4.2；旧帧缺字段仍只走 scoped fallback。

### 4.2 MSGID-IDENTITY（2026-11-05，已验证）

- **范围/版本**：`dshd_rev=af4a49a2817592d7affb99bb3a16c7e03214ba1c`、`ui_rev=3925e84f1524dd101e020d74b1d5bb422d25cf4d`，均为 base+当前共享工作树+本任务 patch；未 commit/push、未启动 GUI。实现只涉及 §2 冻结的 user/assistant 投影、UI apiclient、mockd 与共享夹具。
- **exact**：`v4Seq` 来自持久 v4 `Event.Seq`（含 0），`messageId` 只透传已有持久 id；load 与 WS 同一身份去重，同文不同身份保留，A/B 与迟到重放隔离。真实 apiclient 对 B 的四条内容按 5→2→4→3→2→5 重放后仍恰四条。
- **derived**：W49 journal 持久格式不变，旧帧不回写；缺字段时只走 Stream+Reset scoped fallback，不宣称精确去重。reasoning/tool/approval/turn-done 既有身份不变。
- **证据**：双仓全量 test/vet/build exit 0；宿主 `cmd/dshd`、`internal/transport` 与 UI `internal/apiclient`、`cmd/mockd`、`plugins/dsconversation` race exit 0。真实新 dshd + UI apiclient 同 home 两进程探针 `OK=84 FAIL=0 SKIP=0`，追加 76 项身份断言全绿，重启前 16 帧逐字恢复。命令、先红输出、完整日志路径、golden 哈希与未验项见 [`docs/message-identity.md`](message-identity.md)。
- **unverified**：可见 GUI 与完整工作日不在本任务；非 v4/旧 journal 缺字段分支不承诺跨 load/WS 精确去重。

### 4.3 EFFORT-PROJECTION（2026-11-05，已验证）

- **范围/版本**：`dshd_rev=af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当前共享工作树 + W74 patch；未 commit/push、未启动 GUI。只新增 `session.load.effort` 与 TUI 消费，canonical 25、原字段、WS/journal 不变。
- **exact**：真实新 dshd + ready token→cookie→load，A=`high`、B=`xhigh`；换进程默认重开仍各读自己的记录，append-only 续写后 A 最新 header=`low`、B 普通追加仍=`xhigh`。未改字节的官方 v4 副本投出 `max`。旧 v4、未知枚举、空值均省略字段，进程 `--reasoning max/low` 不补缺。汇总 `EFFORT_REAL_SUMMARY OK=34 FAIL=0 TOTAL=34`。
- **TUI**：缺键与显式空串保留区别；A/B 不串值，最新未知清旧值；120×36/80×24 dump 行数与显示列不溢出，无记录仍 `? effort`，真实输入编辑回归通过。
- **证据**：先红输出、定向绿测、真实 load JSON、append 前后 SHA-256、两档 dump 与全量门禁命令/原样汇总见 [`docs/tui-effort.md`](tui-effort.md) 及 `effort-projection-w74/evidence/`。
- **unverified**：可见 GUI 与完整工作日不在本任务；Go `run` 没有写配置记录的会话仍诚实显示 `?`，不宣称 `--reasoning` 已变成会话持久配置。

### 4.4 TOOL-IDENTITY-LOAD（W187，2026-11-06，已验证）

- **范围/版本**：`dshd_rev=af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当前共享工作树 + W187 patch；未 commit/push、未启动 GUI、未 reset。只改 `internal/transport/api.go`、`cmd/dshd/transport_source.go`、同包测试与本文件，**不碰** TUI / `dsh-ui` / `internal/llm`。
- **exact**：真实 v4 日志（`user/message` → `assistant/message`(tool-call) → `tool/call` → `tool/result`(turn=2,step=3,callId=`call_w187`,View) → `assistant/message`）经真实 `dshd serve` 的 `POST /api/session.load` 返回
  `{"seq":3,"role":"tool","text":"68 个文件","tool":"bash","args":"{\"command\":\"ls\"}","time":1793973841678,"view":{"kind":"card","title":"目录","body":"68 个文件"},"callId":"call_w187","turn":2,"step":3}`；
  TUI 产品源码 `dshGoLoadToolResult` 对该条返回 `{"type":"tool.result","seq":902,"turn":2,"step":3,"callId":"call_w187",…,"text":"68 个文件","presentation":{"card":"generic","title":"目录",…}}`。代码里 `turn/step` 是 `*int`：缺键省略，存在即原值（含合法的 0），与 `v4Seq` 同一套"指针非 nil 才是真值"的规矩。
- **red（先红）**：同一探针在未改代码上 `recovered=0 toolMessages=1`，TUI 返回 `undefined`（工具结果整条丢弃）；Go 守卫 `TestLoadKeepsToolResultIdentity` 红在 `callId 必须是真实调用 id "call_w187"，实际 null`。
- **honesty**：`TestLoadToolResultWithoutIdentityOmitsFields`（真实 v4 老日志形状）与 `TestLoadPassesToolIdentityThrough`（HTTP 响应 JSON）钉住"无真值不写 0"：缺键的 `tool/result` 只出正文，`callId/turn/step` 三个键整个不出现，user/assistant 也不带。
- **证据**：`go test ./internal/transport ./cmd/dshd -count=1` exit 0（`ok dshgo/internal/transport 0.858s`、`ok dshgo/cmd/dshd 88.851s`）、`go vet ./internal/transport ./cmd/dshd` exit 0、`go build ./...` exit 0；原样输出、patch 与 SHA-256 见 `tmp/W187/RESULT.md`。
- **unverified**：可见 TUI 真机（80/120 列）与完整工作日不在本任务；`isError`/`meta`/`structured` 的投影已由 W190 按 §2 补齐（见 §4.5），本条不再把它们列为缺口；非 v4/老日志缺字段时仍只能"恢复不出事件"，不宣称可恢复。

### 4.5 TOOL-RESULT-ENVELOPE（W190，2026-11-06，已验证）

- **范围/版本**：`dshd_rev=af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当前共享工作树 + W190 patch；未 commit/push、未启动 GUI、未 reset。只改 `internal/transport/api.go`、`cmd/dshd/transport_source.go`、同包测试、`internal/transport/testdata/api.snapshot.json` 与本文件；**不碰** TUI（`view-spec.ts` 只读引用）、`dsh-ui`、`PLAN` 表。
- **exact**：真实 v4 日志（失败 `tool/result`：`turn=2,step=3,callId=call_w190_fail,isError=true,meta{cwd},structured{exitCode:2,stderr:"boom"}`）经真实 `dshd serve` 的 `POST /api/session.load` 返回
  `{"seq":3,"role":"tool","text":"exit status 1","tool":"bash","args":"{\"command\":\"false\"}","time":…,"view":{…},"callId":"call_w190_fail","turn":2,"step":3,"isError":true,"meta":{"cwd":"…"},"structured":{"exitCode":2,"stderr":"boom"}}`；
  TUI 产品源码 `dshGoLoadToolResult` 对该条返回 `{"type":"tool.result",…,"isError":true,…,"errorText":"exit status 1","meta":{…},"structured":{…}}`，探针汇总 `W190_TUI_PROBE tag=after toolMessages=3 recovered=2 failures=0`。
- **red（先红）**：同一探针在未改代码（`tmp/W190/dshd-before.exe`）上 `isError:false`、`meta/structured/errorText` 全 `undefined`（`W190_FAIL` × 5，exit 1）——失败结果从 load 恢复后被画成成功；Go 守卫 `TestLoadKeepsToolResultErrorEnvelope` 红在 `isError 必须是 true…实际 null`、`TestLoadPassesToolResultStructuredThrough` 红在 `structured 必须原样透传，实际 null`。
- **honesty**：`TestLoadToolResultOmitsFalseEnvelopeFields`（缺键 / 显式 `false` / 显式 `null` 三种形状）与 `TestLoadPassesToolResultEnvelopeThrough`（HTTP 响应 JSON，含 user/assistant 与成功结果）钉住"无真值不写键"；探针里那条成功结果只带真实 `meta`，`isError`/`structured` 都不出现；没有关联身份的老结果仍 `undefined`（不编身份）。
- **证据**：`go test ./internal/transport ./cmd/dshd -count=1`、`go vet ./internal/transport ./cmd/dshd`、`go build ./...` 均 exit 0；原样输出、patch 与 SHA-256 见 `tmp/W190/RESULT.md`。
- **unverified**：可见 TUI 真机（80/120 列）与完整工作日不在本任务；durable `error` 键（`isError=true` 时可能带的原始错误体）**没有**投影——TUI 退化成用正文当 `errorText`，这是独立账；`structured` 目前没有生产者在写，本任务只保证"有真值就原样透传"；`-race` 未跑（无 C 工具链要求，本任务未新增 goroutine）。

### 3.2 WS-COMPRESSION（2026-11-05）

- **范围/版本**：`dshd_rev=af4a49a2817592d7affb99bb3a16c7e03214ba1c`、`ui_rev=3925e84f1524dd101e020d74b1d5bb422d25cf4d`，均为 base+当前共享工作树+本轮 WS 压缩 patch；未 commit/push、未启动 GUI。实现范围只含宿主 WS、TUI 客户端与 UI apiclient，不改 serve 业务。
- **exact**：可选 permessage-deflate、无协商旧路径、双向重放、A/B 双会话、控制帧、坏扩展/坏帧、1 MiB 解压上限、TUI 断线代际均有行为测试；宿主与 UI 同款 `coder/websocket` 另做双向互通。两仓全量 test/vet/build 与相关包 race 均退出 0。命令、红测记录、hash 与派生大 history 样本见 [`docs/ws-compression.md`](ws-compression.md) §4–§6。
- **derived**：共享 golden 三文件双仓 SHA-256 仍与 §3.1 相同，两仓快照未改；压缩/未压缩测试比较解压后的 canonical JSON，因此不改变 journal/epoch/reset 或原始 history 数据模型。
- **unverified**：未做真实模型/工具审批与可见 GUI 整链、压缩率/CPU/长工作日基准；本轮不外推 H6/H13/U1 完成，详细边界见 [`docs/ws-compression.md`](ws-compression.md) §7。

## 5. 后续契约需求登记（来自 CGAP，未冻结、不得冒充已实现）

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
| 模型选择（MODEL-WIRE） | **DONE scoped `ff844228`/`61ab70a0`**：默认 loopRunner/modelRunner 下一轮使用覆盖，空值回默认，重启持久；`25b374b` models.list 提供真目录，UI `0747a0a` 有选择入口 | 只写 store/列目录不算切换；真实 magpie/UI 真机与完整工作日另验，不把 scoped DONE 外推为全产品完成 |
| Dock 指标（C-STATS） | contextWindow 已由 `111e250` 提供，STATS-WIRE 已贯通 UI 并显示真实文本百分比（无圆环）。**turn/step/cache/decode 真值仍未提供** | 用 assistant/tool 数冒充轮/步、缺分母显示缓存率、used>capacity钳100%或伪圆弧 |
| 工作区（C-WORKSPACE） | OS 目录选择、workspace add/remove、归档/pin/rename、跨会话搜索 | 把标题包含过滤称为语义搜索，或不改目录却显示已切换 |
| 扩展视图（C-EXTENSION-VIEWS） | 第三方 view descriptor 与实际 painter/数据、按会话选择持久化 | 注册前显示主题鱼/压缩，卸载后残留入口 |

宿主已经提供但 UI 适配层丢掉的 `time`、`turn/error{turnId,error}`、`toolCallId/parentCallId`、原始 Turn/Assistant 事件，不属于新增方法：优先在 MSGID/WP-C 保留并消费，不要误列成宿主待开发能力。

## 13. W79 本地附件与有序内容块（additive）

唯一实现说明见 [`docs/attachments.md`](attachments.md)，UI 消费说明见 `dsh-ui/docs/attachments-ui.md`。

- `attachment.put{path}` / `attachment.get{id}` 是独立 additive 方法，**不加入 canonical 25**。用户显式绝对路径只授权本次读取；宿主读真实字节、SHA256 与随机 `att-` id，返回 name/byteSize/mime 和 PNG/JPEG/GIF/WebP 真实宽高。上限 8 MiB，拒绝相对路径、非常规文件、0 bytes、未知类型、坏图与超限。
- blob/meta 仅存当前临时 home 的 `<home>/state/attachments/objects/<id>/`，A/B home 隔离。get 返回同一 id 的真实 `dataBase64`；所有失败保持四字段错误。
- `session.prompt` 可带有序 `content`：text、attachment、reference。attachment 由 id 反查真实 store；file/session reference 只接受真实存在对象，宿主分配并持久化 `ref-` 稳定 id。旧纯文本 prompt 请求、回调和 durable 投影不变。
- durable `user/message.content` 保留块顺序与完整身份；load、原始 history/WS 与进程重开恢复同一 attachment/reference id。`Message.text` 仍只投影 text 块，不拼附件名。`Message.content` 是 canonical 快照的唯一加法；canonical 方法表和原共享 WS golden 不变。
- 两仓独立 `attachments.snapshot.json` / `attachments-golden.json` 逐字节一致。真实验收必须经 UI `internal/apiclient` 对新 dshd ready/cookie 完成 put→prompt→load/history→get、异常、A/B 和重开；mockd 不算此链证据。

## 14. 官方桌面端接口对齐（2026-11-07，additive，未冻结形状）

官方桌面端 0.2.2-rc.1 的 typert 方法全集、优先级与逐命名空间缺口见 [`docs/official-desktop-api-parity.md`](official-desktop-api-parity.md)，方法名清单固化在 [`docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt`](evidence/official-desktop-api-methods-0.2.2-rc.1.txt)。本节只登记**契约口径**：

- **两级口径**：L1 = 官方 153 个方法的**参数与结果字段**按 zod 契约实现，落在现有 `POST /api/<method>` + 每会话 WS journal 上；L2 = 官方 `remote.mux` 多路复用 + typert Connection envelope 的 wire 兼容。**L2 尚未开始，不得声称本宿主已与官方桌面端 wire 兼容。**
- **既有方法不改名不删除**：当前 canonical 25 与后续 additive 方法（含 `attachment.*`、`session.*` 扩展、`settings.*`、`models.catalog.*`、`jobs.*`、`terminal.*`、`workspace.*`）是 TUI/GUI 生产依赖；官方同名方法以新增方式加入，语义冲突以官方字段为准并在本文标注。
- **流式方法不伪造**：`session.follow`/`session.observeEvent`/`session.observeState`/`session.observeProjections`/`workspaceFiles.changes`/`toolResults.watch` 现在是**真实现**（两条面同一台帧机，见 §14.1）；宿主缺能力时仍是四字段未实现错误，**绝不发假 ready 帧、绝不发空流**。`session.events` 官方是 unary（`Promise<SessionEventsPage>`），本仓早已是真实一次性读；`job.follow` 见 §14.1 的未实现项。
- **缺真实能力返未实现**：任何官方方法缺少宿主子系统支撑时，必须返回四字段错误并登记到对齐清单，不返回假数据、不静默成功。
- **验收**：每个方法至少一次真实成功路径 + 一次错误路径，A/B 会话不串；全仓 `go test ./...`、`go vet ./...`、`go build ./...` exit 0 不替代真实 dshd 端到端。

### 14.1 官方流式方法的实现与 `sinceSeq` 语义（HOST-STREAM-METHODS，2026-11-07，additive）

**官方 `mode:'stream'` 的方法共 6 个**（`session.events` 不在其中：官方它是 `Promise<SessionEventsPage>`，一次性读）：

| 方法 | 官方契约（file:line） | 帧形状 |
|---|---|---|
| `session.follow` | `dsh-api-session-controller/lib/typert.host.js:2019`（signature `:2866`） | `{type:"snapshot", header, projections, cursor, records, hasMore}` → `{type:"event", event}` |
| `session.observeEvent` | 同文件 `:2184`（`:2831`） | `{type:"ready"}` → `{type:"event", event}` |
| `session.observeProjections` | 同文件 `:2055`（`:2838`） | `{type:"ready"}` → `{type:"projection", seq, changes:[{key,val}]}` |
| `session.observeState` | 同文件 `:2252`（`:2852`） | 完整 `SessionState`（`{cursor,active,running,queue,pendingInput}`，**没有 type 判别式**） |
| `workspaceFiles.changes` | `dsh-api-workspace-files/lib/typert.host.js:160` | `{kind:"ready"}` → `{kind:"change", change:{absolutePath,version}` \| `{absolutePath,absent:true}}` |
| `toolResults.watch` | `dsh-api-remotes/lib/client.js:17415` | `{available:false}` \| `{available:true, value}` |

**两条传输面共用一台帧机**（`internal/transport/official_stream*.go`），所以不存在"HTTP 对了、mux 错一格"：

- **HTTP**：`POST /api/<method>` 返回 `200` + `application/x-ndjson; charset=utf-8`，**一行 = 官方一帧**，无包壳；正常结束 = 连接关闭；中途失败 = 最后一行 `{"type":"error","error":{code,message,hint,retryable}}`（与 `side.query` 的长连接约定一致）。选 NDJSON 不选 SSE 的理由：本仓已有 NDJSON 长连接先例，同一进程里两套长连接形状只会让客户端多一份解析器。
- **mux**：`/api/remote.mux` 的 `open` 端点用官方斜杠写法（`session/follow`），`item.value` 就是上表那一帧。接线：`officialwire.StreamOpener` 由 `transport.(*Server).OpenOfficialFrameStream` 提供（清单见 HOST-STREAM-METHODS-RESULT.md）。
- **参数错误**（未知字段、缺 `address`、`sinceSeq < -1`…）在**写响应头之前**返回四字段 JSON + 状态码；mux 面回 `gateway/method-unavailable` / `gateway/bad-request` 错误帧。**"宿主没这个能力"绝不发空 end**——空 end 会被客户端当成"流正常结束了"。

#### `sinceSeq` 的排他/包含语义（唯一口径）

游标是 **durable v4 事件 seq**（不是 Transport journal seq、不是连接序号、不是页内下标）：同一客户端会把 follow 的 `cursor` 交给 `session.events(beforeSeq)`、`session.wait(afterSeq)`、`toolResults.watch(seq)`，用另一套序号顶上会让跨方法游标静默错位。

- `sinceSeq >= 0`：**排他**下界 =「我已经收到第 `sinceSeq` 帧了」，只交付 `(sinceSeq, …]`。
- `sinceSeq == -1`（`transport.SinceNothing`）：**「我什么都没拿」**，从第 0 帧起交付。

**为什么必须有 -1**：`Window` 的判据是 `Seq > sinceSeq`。如果"从头开始"只能写 `0`，而 `0` 表示"第 0 帧我有"，那么**第 0 帧永远取不到**（v4 事件 seq 从 0 开始；空会话 `logEnd = -1`）。这个坑在三条面上统一用 -1 表达，不许各处自己解释：

1. WS `sub`（`internal/transport/frame.go` + `wssession.go`）：`sinceSeq: -1` 等价于省略；`< -1` 仍是 `ErrBadSeq`。
2. mux 尾部流（`Server.OpenOfficialJournalTail`）：`sinceSeq: -1` = 从第一帧起。
3. 官方 follow（`session.follow` 的 additive 字段 `sinceSeq`）：见下。

`Window` 的 `Covered` 同步修正：`sinceSeq < 0` 时只有内存里**从第 1 帧起都在**才算补得回（以前一律判成"假缺口"，界面表现为莫名整屏重建）。

#### 断线重连：钉切点、不重放历史

官方用 `throughSeq`（**inclusive** cut）+ `beforeSeq`（**exclusive** 上界）表达，`{throughSeq: N, beforeSeq: 0}` = "切点钉在 N、别再给历史"。本仓额外接受 `sinceSeq`（语义 = `throughSeq: sinceSeq, beforeSeq: 0`），因为它就是 mux/WS 面的同一个名字；三者同时出现且矛盾 → `400`（**不猜客户端的心智模型**）。

#### 背压与隔离

- 帧机是**拉模式**：调用方不取下一帧，宿主就不产下一帧。慢客户端只卡自己那条流（mux 侧还有 256 格发送队列 + 256 KiB 上行配额）。
- 等待用 `Journal.Wait()` 广播唤醒（不是忙等）；ticker 只是兜底（数据源不在本进程 journal 上时）。
- 每条流绑定一个 `sessionId`（`workspaceFiles.changes` 绑 `workspaceFileScopeId`，`toolResults.watch` 绑 `sessionId`）；A/B 两个 `Server` 各挂各的宿主能力，没有"当前会话"这种全局态。

#### 与官方的已知差异（不冒充官方 wire 兼容）

- `workspaceFiles.changes` 用**轮询 `FilesStat` 版本令牌**替代官方 `fs.watch`：语义一致（版本变了就是一次失效），延迟是一个轮询间隔（`officialStreamPollInterval`，缺省 100ms）。
- 目录目标（官方"目录的直接子项"）**未实现**：官方帧必填 `absolutePath`，而本宿主 `FilesList` 只给工作区相对路径。明确回官方码 `workspace-file/watch-unsupported` 并点名缺什么，**不拿相对路径冒充绝对路径**。
- `session.follow` 的 `assistantStream:true` 被接受但**不产生额外帧**：本宿主没有 process-local 助手呈现帧（助手正文走 `assistant/message` durable 事件）。
- `session.follow` 的 snapshot `header` 需要宿主提供 `OfficialSessionHeaderSource`（v4 首帧原样）。**没有就 501 点名缺它**，绝不拿 `createdAt:0` 编一个（客户端会显示 1970 年）。
- 流不重放历史时的 `records` 是**空数组**（不是 `null`），`hasMore=false`。
