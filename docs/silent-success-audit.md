# 静默成功审计：有产出、但没有有效载荷、而且没人报错

> 2026-11-04。一天里真机撞到两条：网关 200 + 空流被记成「assistant 空消息 + turn completed + 退出码 0」；
> `CompactSchema` 见 `description` 键就删，把 8 个名叫 `description` 的参数一起删了，而 `required` 里还点着它们。
>
> 这两条的共同形状不是"崩溃"，而是 **成功**：退出码 0、HTTP 200、事件写进了日志、函数返回了结果——
> 只是产出里没有任何有效载荷，而且全链路没有一处报错。它们比崩溃贵，因为不触发任何告警。
>
> 本文是这一类路径的系统性清单、逐条判断与修复证据。扫描器：`scripts/silent-success-audit.py`。

## 0. 结论

| 结论 | 条数 |
|---|---|
| 真缺陷（已修，附红→绿） | 14 + 2（原「越界移交」的两条，见 §5.1） |
| 真缺陷（越界，已登记移交；`cmd/dshd/serve*.go` 当时不许动）→ 已由后续会话修复 | 1 → 0 |
| 可接受（说清为什么不伤人） | 18 |
| 需要设计决定（两个方向的代价都写清，交给用户） | 3 |

最值得记住的两条新发现（都不在今天的原始两条里）：

1. **`journal` 容量丢帧后，补洞会静默缺一段**：`session.history` 回 200 + 连续帧，`frames` 里少了最老那截，
   客户端按 seq 看是连续的，永远不知道中间缺过。WS 订阅同理。
2. **`turn/end` 恒写 `completed`**：被 `max_tokens` 截断的回合（模型只说了半句）在日志里也是「完成」，
   `assistant/message` 里的 `finish=max-tokens` 被它盖住。

## 1. 方法（比结论重要）

### 1.1 从「接口边界」入手，不从文件名猜

对每一条边界问三个问题：

1. **空载荷长什么样？**（0 字节 / 0 条 / nil / `{}` / `[]` / 只有 finish 没有内容）
2. **谁会消费它，消费方看到空载荷时会怎样？**（模型看到 `（无输出）`、界面看到 0 行、客户端看到 `result:null`）
3. **空载荷时链路上有没有任何一处报错？**（没有 ⇒ 命中本类）

扫过的 9 类边界：

| # | 边界 | 具体形状 |
|---|---|---|
| 1 | LLM 响应 | 200 + 空流、只有 finish、内容全空白、`choices: []`、usage 全 0 |
| 2 | LLM 流 | 中途 EOF（有内容、无 finish_reason、无 `[DONE]`）、坏分片被跳过 |
| 3 | 工具结果 | `Result{}` + `nil` err、`Data` 空数组 vs `nil`、只有 `View` 没有模型可见正文 |
| 4 | 工具 schema | `required` 点名了 `properties` 里不存在的键、空 schema、压表之后结构被改坏 |
| 5 | 工具表注册 | `Name()` 空串、投影表 `Register`、模块 `Register` 一个都没注册 |
| 6 | 会话写入 | 空 `type`、`data:null`、0 字节帧、`append` 了但 `seq` 没动 |
| 7 | 压缩投影 | 空摘要遮蔽历史、区间里没有模型可见事件却报「压缩成功」、压完 surface 没变 |
| 8 | Transport HTTP | `200 + result:null`、`frames: []` 但 `lastSeq > 0` |
| 9 | Transport journal/WS | 容量丢帧后补洞缺口、`reset`/`pong` 无 seq |

### 1.2 判据

**不是「有没有报错」，是「消费方拿到的字节里有没有信息」**：

- 对模型：正文/工具结果/提示三段里至少一段非空且不是占位符；
- 对客户端：成功信封里 `result` 不是 `null`，数组不是"由于缺口而变短"；
- 对读盘的人：写进去的事件，投影能读出东西（`type` 在词汇表里、`data` 不是 `null`）。

### 1.3 扫描器

`python scripts/silent-success-audit.py`（`--json` 机器可读）。两类检查：

- **数据检查**（会 FAIL）：解析全部 57 份 `json.RawMessage` schema，逐层校验
  `required ⊆ properties`；扫 `return Result{}, nil`、`Append(…, nil)`、`writeJSON(…, nil)`；
  模拟**旧的** `CompactSchema`（见 `description` 就删）并统计它会打断多少条 `required`——这是"为什么不能这么写"的永久反证。
- **守卫检查**（会 FAIL）：15 条已知静默成功路径，源码里必须存在对应校验/报错。
  守卫被删掉时这里立刻红，不靠记忆。（D3 之后新增 3 条：`CompactSchema` 与
  `tightenSchema`/`shapeless` 必须调用 `internal/schema` 那一份判断；另一个方向
  也收紧了——除 `internal/schema` 外任何非测试 Go 文件再出现按 key 名删
  `description`，直接 FAIL，不再是 advisory。）

**它的边界**：静态检查只能证明"守卫在"，不能证明"守卫在运行时真的挡住"。
真正的运行时证据在 `internal/*` 的红测里（见 §3），本报告不把扫描器绿当成运行时绿。

当前输出：57 份 schema 解析成功、0 份解析失败；旧算法会打断 5 条 `required`（对应 8 个名叫 `description` 的参数）；
15 条守卫全在；扫描器不再发现任何按 key 名删 `description` 的删除点（规则由
`internal/schema` 的私有常量 + `IsAnnotationKey` 表达）；`exit 0`。

## 2. 清单全表

图例：**真缺陷** = 有红测 + 修复；**可接受** = 说清为什么不伤人；**设计决定** = 两个方向的代价都写清，交给用户。

### 2.1 LLM 层

| 路径 | 现状 | 判据 | 结论 |
|---|---|---|---|
| 200 + 空流（0 个 chunk） | 旧：`Stream` 返回 `nil` err + 空 `Response`；`agent.Loop` 今天已判错，但 `modelRunner` 直接吃 `nil` err | 调用方拿到 nil err 就认为成功；空响应会被记成 `assistant` 空消息 + `turn/done` | **真缺陷**：`llm.Stream` 自己判错并指路 `dshd models`（红→绿 §3.2） |
| 流中途 EOF（有内容、无 finish、无 `[DONE]`） | 旧：连接正常关闭 ⇒ `readSSE` 返回 nil ⇒ 半句话当完整回答 | 整条流没有"完结凭据"（`finish_reason` 或 `[DONE]`），回答必然不完整 | **真缺陷**：判"流在 finish 之前断开"，半截内容仍带回供落盘（红→绿 §3.2） |
| 只有 finish、没有内容 | `finish=stop` + nil 内容 | 语义空判定在 agent 层：`emptyResponseErr` 已覆盖（今天那条） | 可接受（分层正确：llm 管协议完备性，agent 管"有没有可用产出"；两层都有守卫） |
| `choices` 长度为 0 的 chunk | usage 末片合法地长这样 | 它不产生事件，只填 usage；不产生任何"看起来成功的空消息" | 可接受（OpenAI 兼容接口的正常形状） |
| `usage` 全 0 | agent 不发 `usage/update`；`session.load` 的 `ContextTokens=0` | 0 在 `LoadResult` 里明确表示"不知道"，界面不画百分比 | 可接受（契约写明 0=未知；拿 0 做除法的地方会显出 0%，但那是显示问题不是数据丢失） |
| `finish_reason=length` 有内容 | assistant 消息里记 `max-tokens`；`turn/end` 旧代码恒写 `completed` | `turn/end.reason.kind` 的词汇表里本来就有 `max-tokens`；写 `completed` 等于告诉客户端"这轮说完了" | **真缺陷**：`session.TurnEndKind` 映射，run/acp 两个入口都接上（§3.7） |
| `models` 返回 `data:[]` / `data:null` | `ListModels` 返回空列表 + nil err；`dshd models` 报「通了，但一个模型都没报」；Transport 回 `{"models":[]}` | CLI 已明确报错；但 `data:null`（字段缺失/网关不兼容）与 `[]`（真没有模型）无法区分，界面只能显示同一个空列表 | **设计决定 D2**（CLI 可接受；语义要定，见 §4） |
| `models` 条目 `id` 为空 | 原样透传给界面 | 界面会出现一个选不中的空行；不是"静默丢数据" | 可接受（低危；异常网关才会这样，空行可见） |

### 2.2 工具层

| 路径 | 现状 | 判据 | 结论 |
|---|---|---|---|
| `spi.Result{}` + `err == nil` | 旧：模型只看到 `（无输出）`，无 Hint；日志 `ok=true` | 模型不知道这次调用做没做、该不该重试；R3 明确禁止"空 Result 丢 Hint" | **真缺陷**：流水线补 Summary + 可执行 Hint + Warn 日志（红→绿 §3.4） |
| 只返回 `View`（无模型可见正文） | 旧：`View` 被当成"有产出"，模型拿到 `（无输出）` | `View` 是给客户端画卡片的，模型看不见；对模型来说这就是空结果 | **真缺陷**（同一处修复：`View` 不计入载荷；红测覆盖 §3.4） |
| `Data` 空数组 vs `nil` | `nil` = 没有数据（跳过）；`[]` = 集合为空（打印 `[]`） | `[]` 是答案（"没有匹配"），不能当"工具没产出" | 可接受（差异被保留，已有测试固定） |
| schema 里 `required` 点名了不存在的键 | 全仓 57 份 schema 当前 **0 处**；但 `agent.CompactSchema` 的旧算法会**造出**这个形状（8 个参数叫 `description`，其中 5 个在 `required` 里） | 模型看到"必填、但没有类型"——凭空少一条契约，全链路不报错 | **真缺陷**：`CompactSchema` 按位置区分注解与参数名（红→绿 §3.1） |
| `sameShape` 比较把「多一个 `description` 参数」判成同构 | 旧 `stripDescriptions` 也是"见键就删"；合并层据此把不同工具并族 | 判据是"两张 schema 的结构是否真的一样"；少一个参数就不是 | **真缺陷**（潜在，当前合并族里没有触发实例）：同一套按位置判断（红→绿 §3.8） |
| 工具 `Name()` 为空 | `tools.Registry.Register` 返回「工具没有名字」；三个投影表也拒绝注册 | 空名字进表 ⇒ 模型看不到、调不到，且注册调用方若吞错就静默 | 可接受（守卫已在，扫描器盯住） |
| 工具 Schema 为 `nil` / 空对象 | 57 份全部可解析且有 `properties`；零参工具的空对象是合法契约 | 非零参工具若 schema 为空，模型只能瞎猜——但当前无实例 | 可接受（当前无实例；新增工具时由 `tool_schema_test.go` 的结构比对兜住） |
| 压表规则有两份实现（`agent.CompactSchema` / `cmd/dshd.tightenSchema`） | 两套变换、三份「见键就删」的写法；`sameShape` 的 `stripDescriptions` 与 `CompactSchema` 语义重复 | 同一规则两处实现必然漂移；今天两条 bug 恰好是同一形状在两处各犯一次（修了主路、留了岔路） | **已解决（D3）**：判断收进 `internal/schema`，重复实现删除（见 §4 D3） |

### 2.3 会话 / 日志层

| 路径 | 现状 | 判据 | 结论 |
|---|---|---|---|
| `Log.Append("", data)` | 旧：落一条 `type:""` 的事件；`surface` 永远跳过它 | seq 动了、文件多了字节，但任何投影都读不出来 | **真缺陷**（潜在，当前无调用点）：写盘前拒绝（红→绿 §3.5） |
| `Log.Append(type, nil)` | 旧：落成 `data:null` | 文件里有这一行、`seq` 也动了，投影拿不到任何字段 | **真缺陷**（潜在）：写盘前拒绝（同一红测） |
| 压缩摘要为空 | `session.Compact` 本身不校验；CLI 手工路径靠 `--summary` 非空，auto 路径靠 `Summarize` 判空 | 遮蔽历史之后 surface 变成一个空节点：历史"变短了、什么也没留下"，而 `compaction/end` 说成功 | **真缺陷**（潜在，守的是唯一写入口）：拒绝并说明（红→绿 §3.3） |
| 压缩区间里没有模型可见事件 | 旧：只有 `tool/call` 也报「压缩成功」；`surface` 一个节点没少（反而多一条摘要） | 判据是"模型看得见的东西真的变少了"，不是"事件条数减少" | **真缺陷**（低危，manual 路径可达）：拒绝（红→绿 §3.3） |
| 压缩后 `surface` 没变却说「已压缩」（auto 路径） | `compact.Plan` 从 surface 选 ≥2 个节点；新增的可见事件守卫再兜一层 | 有下界保证：遮蔽 ≥1 个可见节点 + 1 个替身 ⇒ surface 必减 | 可接受（有结构性保证；`dshd run` 的「surface N → M」日志会暴露异常） |
| 写进去 0 字节 | `v4.Writer.Append` 至少写一行 JSON + `\n`；`writeFrame` 还有"帧 < 8 字节即报错" | 0 字节帧在编解码层就不成立 | 可接受（结构上不可能） |
| `append` 了但 `seq` 没动 | `Log.Append` 在写盘**成功后**才自增；失败直接返回 | 拒绝时 seq 不动，已有测试断言 | 可接受（结构上不可能） |
| 回合失败时没有 `turn/end`（悬挂回合） | `agent.Run` 出错后调用方直接返回，不补结局事件 | 进程退出码非 0 / ACP 回 error，**不静默**；但日志里 turn 没有闭合 | **已解决（D1，2026-11-04）**：run / ACP / serve 子进程三条路的入口都并到同一个出口，失败补 `turn/end{reason.kind=error/aborted}`（见 §4 D1 结论） |
| `modelRunner` 用 `m.Text == ""` 过滤历史 | assistant 只有 tool_calls 时 `Text` 为空 ⇒ 整条消息被丢掉，后续 tool 结果找不到对应调用 | 模型会看到"孤立的工具结果"；历史语义被悄悄改掉 | **真缺陷（已修，见 §5.1）**：原在 `cmd/dshd/serve_turn.go`，当时按边界不许改；后来放开，判据换成 `assistantHasPayload`（正文/思考块/tool_calls 任一） |
| `modelRunner` 空响应 | 旧：只写 user 消息 + `turn/done` | 与 LLM 层第 1 条同形 | **真缺陷（已修，间接）**：§3.2 的 llm 守卫让 `Stream` 直接报错 ⇒ `turn/error`；未改 `serve_turn.go` |

### 2.4 Transport 层

| 路径 | 现状 | 判据 | 结论 |
|---|---|---|---|
| `200 + result:null` | 旧：`writeJSON(w, 200, nil)` 输出 `{"result":null}`；当前没有 handler 这么写 | 客户端按契约读 `result`，拿到 null 会当空处理；HTTP 200 让重试/告警都认为成功 | **真缺陷**（潜在）：出口守卫，nil 与 typed-nil 都变 500 + 四字段错误（红→绿 §3.6） |
| `frames: []` 但 `lastSeq > 0` | 两种情况：(a) `sinceSeq == lastSeq`，正常；(b) 容量丢帧后 `sinceSeq` 比最老保留帧还旧——旧代码静默给短数组 | (b) 里帧连续、seq 在涨、HTTP 200，客户端看不出缺口 | **真缺陷**：`Journal.Window` 的 `Covered` + history 回 `stale_epoch`、WS 发 `session/reset`（红→绿 §3.9） |
| `sinceSeq == lastSeq` 时 `frames: []` | 正常 | 没有新帧就该是空数组；`lastSeq` 用来确认水位 | 可接受（契约正常路径） |
| 列表接口返回 nil 切片（`session.list` / `jobs.list` / `workspace.list` / `schedule.list`） | 四个 handler 都显式把 nil 归一成 `[]` | `null` 会让客户端 for 一下当空，分不清"没数据"与"接口坏了" | 可接受（已有归一，源码确认） |
| journal `limit` 截断 | `Since` 最多给 limit 条 | 响应带 `lastSeq`，客户端可继续拉 | 可接受（分页语义明确） |
| `session/reset` / `pong` 没有 seq | 有意为之 | 它们不是事件流里的一帧；给假 seq 反而污染连续性判断 | 可接受（契约写明按 type 区分） |

### 2.5 模块装配

| 路径 | 现状 | 判据 | 结论 |
|---|---|---|---|
| `enabled = false` 但模块照样装配 | 历史 bug，`kernel.Apply` 已修复并有注释/测试 | 配置说关、实际开着——最典型的"改了配置什么都没发生" | 可接受（源码已确认：`Apply` 里真的 `continue`） |
| manifest 白名单漏列模块 | 会 `slog.Warn`「manifest 是白名单，这个模块没列出来，已跳过」 | 少一个工具不能无声 | 可接受（有告警） |
| 空能力名 / 两个模块提供同一能力 / 缺依赖 | `TopoSort` 全部返回错误 | 装配期硬错误 | 可接受（已有守卫） |
| `enabled = true` 但 `Register` 什么也没注册 | wasm 侧：装载成功但 0 能力会打 `Warn` 并说明"只能 `dshd wasm call` 手动调"；static 侧：所有 `Module()` 的成功路径都至少注册一个工具或提供一个服务 | 没有通用守卫（要改 kernel 裁决逻辑，越界）；当前无触发实例 | 可接受（有告警 + 当前无实例；新增模块时靠 review） |

## 3. 修复明细（先红后修）

每条都先写会红的测试，贴红的那次输出，再修。

### 3.1 `agent.CompactSchema` 删掉名叫 `description` 的参数

**红**（`go test ./internal/agent -run TestCompactSchemaKeepsParamNamedDescription`）：

```
--- FAIL: TestCompactSchemaKeepsParamNamedDescription (0.00s)
    compact_schema_test.go:83: 名叫 description 的参数被删了：模型只看到 required 里一个没类型的名字
        {"additionalProperties":false,"properties":{"command":{"type":"string"},"options":{"items":{"properties":{},"required":["description"],"type":"object"},"type":"array"}},"required":["command","description"],"type":"object"}
```

**修**（`internal/agent/loop.go:366`）：`compactNode` 带 `inPropertyMap` 上下文——
`properties` / `patternProperties` / `$defs` / `definitions` 这一层的键是**参数名**，一个都不删；
其余层的 `description` 才是注解。

**绿**：`ok dshgo/internal/agent`，且 `cmd/dshd` 的
`TestCompactTableKeepsContract` / `TestTightSchemaKeepsDescriptionParam` 仍绿（投影表结构没变）。

> 补充：今天那条"已修"落在 `cmd/dshd/tightenSchema`（serve 主路），
> `agent.CompactSchema` 是 Loop 自己的岔路（`CompactTools=true` 时生效，当前显式关着）。
> 修了主路、留了岔路——扫描器 + 本修复把两条都补齐了。
>
> **D3 更新（2026-11-04）**：上面这份 `compactNode` 已移入 `internal/schema.stripNode`，
> `agent.CompactSchema` 只保留一行转调；判断函数仍是同一份（`IsAnnotationKey`）。

### 3.2 流在 `finish` 之前断了 / 200 + 空流

**红**（`go test ./internal/llm`）：

```
--- FAIL: TestStreamWithoutFinishOrDoneIsTruncation (0.01s)
    client_test.go:223: 流没有 finish 就结束，必须报错；实际把 "半句" 当成了完整回答
--- FAIL: TestStreamEmpty200IsError (0.01s)
    client_test.go:244: 200 + 空流必须报错，不能记成「assistant 说了空话 + turn completed」
```

**修**（`internal/llm/client.go:95`）：记 `sawDone`；流结束时若既没有 `finish_reason` 也没有 `[DONE]`——
空载荷报「空响应 + 指路 `dshd models`」，有载荷报「流在 finish 之前断开、回答不完整、先重试」。
半截内容仍随 `Response` 返回，供落盘/诊断。

**绿**：`ok dshgo/internal/llm`、`ok dshgo/internal/agent`、`ok dshgo/internal/compact`。
另加 `TestStreamFinishWithoutDoneIsComplete`：只给 `finish_reason`、不发 `[DONE]` 的网关不算断流（不误伤）。

### 3.3 压缩：空摘要 / 区间里没有模型可见事件

**红**（`go test ./internal/session`）：

```
--- FAIL: TestCompactRejectsEmptySummary (0.18s)
    --- FAIL: TestCompactRejectsEmptySummary/nil (0.04s)
        compact_test.go:112: 空摘要（nil）必须报错
    --- FAIL: .../空切片、/只有空白、/类型不是_text  同样
--- FAIL: TestCompactRejectsRangeWithoutVisibleEvents (0.05s)
    compact_test.go:140: 只有 tool/call 的区间不许报压缩成功：surface 根本不会变小
```

**修**（`internal/session/compact.go`）：
- 摘要里没有非空 `text` 块 ⇒ 拒绝（`摘要为空——遮蔽之后这段历史会变成空白`）；
- 区间里没有 surface 可见事件（`isSurfaceVisible`：5 类消息 + `compaction/summary`）⇒ 拒绝
  （`压了 surface 不会变，摘要反而凭空多一条`）；
- 两者都在**写任何事件之前**返回，`seq` 不动（测试断言）。

**绿**：`ok dshgo/internal/session`（含级联压缩、区间校验等原有测试）。

### 3.4 工具成功但什么都没返回

**红**（`go test ./modules/tools`）：

```
--- FAIL: TestEmptySuccessGetsNoticeAndHint (0.06s)
    2026/11/04 10:46:22 INFO 工具调用 tool=silent ms=0 ok=true args_bytes=0
    pipeline_test.go:193: 空结果必须有一句人话，得到 Summary=""
```

**修**（`modules/tools/pipeline.go:172`）：执行成功且 `isEmptySuccess` 时补
`Summary="工具 %q 执行成功，但没有返回任何模型可见的输出"` + Hint（"别假设它做了事，用只读手段核实"）+ Warn 日志。
`View` **不计入**载荷（客户端能画卡片、模型看不见）；`Data=[]` 计入（空集合是答案）。

**绿**：`ok dshgo/modules/tools`（三个子用例：全空 / `Data=[]` / 只有 `View`）。

### 3.5 日志写入空事件 / null 负载

**红**（`go test ./internal/session -run TestAppendRejectsEmptyEvent`）：

```
--- FAIL: TestAppendRejectsEmptyEvent (1.74s)
    log_test.go:140: 空事件类型必须报错
```

**修**（`internal/session/log.go:70`）：`eventType` 空白、或 `data` 序列化成 `null` ⇒ 写盘前拒绝，
`seq` 不推进、文件不多一行。

**绿**：`ok dshgo/internal/session`。

### 3.6 `200 + result:null`

**红**（`go test ./internal/transport -run TestWriteJSONRejectsNilResult`）：

```
--- FAIL: TestWriteJSONRejectsNilResult (0.00s)
    envelope_test.go:48: nil 载荷不该回 200：{"result":null}
```

**修**（`internal/transport/server.go` 的 `writeJSON`）：nil 与 typed-nil（序列化成 `{"result":null}`）
都改回 500 + 四字段错误信封，新增稳定错误码 `empty_result` 与对应 hint。

**绿**：`ok dshgo/internal/transport`。

### 3.7 `turn/end` 恒写 `completed`

**红**：这一条的红是**编译级**——测试引用 `session.TurnEndKind`，旧代码没有这个函数：

```
# dshgo/internal/session [dshgo/internal/session.test]
./log_test.go:xxx: undefined: TurnEndKind
```

行为证据是旧调用点源码（`git show HEAD:cmd/dshd/main.go`、`acp.go`）：
两处 `EventTurnEnd` 都硬写 `session.TurnEndData(turn, "completed", "")`，**不看 `e.Finish`**。

**修**：`internal/session/log.go:241` 新增 `TurnEndKind`（`length → max-tokens`，
`aborted/blocked/error/interrupted/forked/max-tokens` 原样，其余 `completed`），
`cmd/dshd/main.go:749`、`cmd/dshd/acp.go:189` 两个入口改用它。

**绿**：`ok dshgo/internal/session`、`go build ./...`。

### 3.8 `sameShape` 把参数名 `description` 当注解

**红**（`go test ./cmd/dshd -run TestSameShapeKeepsDescriptionParam`）：

```
--- FAIL: TestSameShapeKeepsDescriptionParam (0.06s)
    tool_merged_shape_test.go:19: 多一个 description 参数的两张 schema 不是同构
```

**修**（`cmd/dshd/tool_merged.go:610`）：`stripDescriptions` 改成 `stripDescNode(v, inPropertyMap)`，
与 `agent.CompactSchema` 同一套按位置判断；参数名保留、注解删。

**绿**：`ok dshgo/cmd/dshd`（含 `TestCompactTableKeepsContract`）。

> **D3 更新（2026-11-04）**：上面这份 `stripDescriptions` 已在 D3 收敛里整份删除，
> `sameShape` 的 `shapeless` 直接转调 `internal/schema.Compact`（与
> `agent.CompactSchema` 同一实现）。本节保留当时的修复记录，最终形态见 §4 D3。

### 3.9 补洞缺口

**红**（`go test ./internal/transport`）：

```
--- FAIL: TestServeConnSignalsGapAfterTrim
    wssession_test.go:130: 水位滑出内存时必须先发 session/reset，实际第一帧 {Seq:3 Type:assistant/text Data:{"text":"x"} Time:2}
--- FAIL: TestHistorySignalsGapAfterTrim
    （200 + 从 seq 3 开始的连续帧；1..2 无声消失）
```

**修**（`internal/transport/journal.go` 新增 `Window`——帧/水位/世代/缺口一次原子拿全，避免"检查通过、取帧却少一帧"）：
- `session.history`：`sinceSeq` 比最老保留帧还旧 ⇒ 409 `stale_epoch`，message/hint 说清"滑出内存、重新 load"；
- WS `sub`：同样情况 ⇒ 先发 `session/reset`，且**不再重放**保留帧，只接新帧。

**绿**：`ok dshgo/internal/transport`（原有 stale_epoch、重放、空数组序列化测试全绿）。

## 4. 需要设计决定

### D1 回合失败时要不要补一条 `turn/end{error}`？

**现状**：`agent.Run` 返回错误后，`dshd run` / ACP 直接返回，不写 `turn/end`。进程退出码非 0、ACP 回 error 帧，
所以**不是静默**；但日志里这一轮没有闭合（悬挂 turn），与 `research/21` 记的「turn/end 恒以某 reason 收口」不一致。

- **方向 A（补）**：失败路径写 `turn/end{reason:{kind:"error"/"aborted", error:{...}}}`。代价：要决定
  `context.Canceled` 是 `aborted` 还是 `interrupted`；`dshd run` 的错误已经打到 stderr，日志里再写一份有重复；
  还要保证"写结局失败"不能盖住原始错误。
- **方向 B（不补）**：维持现状，由退出码 / `turn/error` 帧承担结局。代价：任何只读 v4 日志的消费者
  （回放、统计、摘要）都得自己处理悬挂回合；DSH 语料里没有这种形状，导出的会话可能与上游不一致。

我倾向 A，但它是会话格式语义的决定，不是 bug 修复——交给用户。

#### D1 结论：**补**（2026-11-04 拍板）

> 原文保留。理由：只读日志的消费者（回放、UI、事后取证、`history_search`）现在必须
> 自己处理"turn 没闭合"的悬挂态；而"退出码/错误帧"只在**当场**有，日志里没有。
> 日志是 append-only 的事实来源，它必须能自洽——一个 turn 开了就一定要有一条结局。

**不改 v4 落盘格式**（R5）：只往既有的 `turn/end` 里取合适的 `reason.kind` 放已有的
`reason.error.message` / `reason.reason`，事件帧结构、字段名、seq 语义一律不动。

**出口只有一个**：`cmd/dshd/main.go` 的 `turnCloser`。`turn/start` 之后立刻建它，
`Emit` 的 `EventTurnEnd` 走 `End(finish)`，函数出口 `defer` 走 `Exit(ctx, err)`，
panic 展开走 `ExitOnPanic(v)` 后**原样 panic 回去**。三条路都进同一个 `write`，
靠 `closed` 标志保证一条 turn 只有一条 `turn/end`。

**kind 表**（只用 v4 词汇表里已有的值，没有新造 kind）：

| 结局 | `reason.kind` | 原因字段 | 说明 |
|---|---|---|---|
| 模型收工（stop / tool_calls / awaiting_user） | `completed` | — | 与原来一致 |
| 被 `max_tokens` 截断（finish=length） | `max-tokens` | — | 与原来一致 |
| 步数用满未收敛 | `error` | `error.message` = "turn 用满了 N 步仍未收敛…" | **不新造 `max-steps`**：v4 没有这个词，而"没收敛"由原文说清 |
| 模型报错 / 空响应 / 压缩失败 / Emit 写盘失败 | `error` | `error.message` = 原始错误 | 失败原因不归纳、不翻译 |
| 取消（`context.Canceled`） | `aborted` | `reason` = "这一轮被取消…" | v4 的 `aborted` 本来带原因 |
| 超时（`context.DeadlineExceeded`） | `aborted` | `reason` = "这一轮超时被中止…" | 同上；没用 `interrupted` 是因为它不带原因字段 |
| panic | `error` | `error.message` = "宿主 panic，这一轮没有跑完：…" | panic 仍原样抛出 |
| turn 开了、没有任何人写结局、也没有错误 | `error` | `error.message` = "宿主没有写出结局…（宿主缺陷）" | 宁可显眼地不对，也不伪装成 `completed` |

**不写 turn/end 的路径清单**（改前，`文件:行`）：

| 路径 | 触发条件 |
|---|---|
| `cmd/dshd/main.go:767` `loop.Run` 返回后直接 `return err` | 模型调用失败、空响应、`--max-steps` 用满、压缩失败、Emit 写盘失败 |
| `cmd/dshd/acp.go:225` 同上（`Prompt` 返回） | 同一组条件；另有 `ctx.Err() != nil` 时回 `"cancelled", nil, nil` 的那条 |
| `cmd/dshd/acp.go:115` 之后的 `user/message` 写盘失败 | 立刻 `return`，turn 已开 |
| panic / 提前 return（两侧） | 任何在两处 `turn/start` 之后的崩溃 |
| `cmd/dshd/serve_tools.go:156` `loopRunner` 的子进程被 `exec.CommandContext` 杀掉 | 取消、父进程退出、崩溃——子进程根本没机会跑自己的出口 |

「工具失败」**不在**表里，这是有意的：工具错误被记成 `tool/result{isError:true}` 后循环
继续，turn 仍会正常收工；把它算成"回合失败"会把一次可恢复的工具错误伪装成整轮失败。

**serve 子进程**：子进程自己就是 `dshd run`，上面那个出口对它同样生效。父进程只在
`werr != nil || ctx.Err() != nil` 时回看一次日志（`settleTurn`），给"硬杀/崩溃"留下的
悬挂 turn 补一条 `turn/end`——`aborted`（被取消）或 `error`（非零退出，带子进程原因）。
补写前会抢写锁；锁文件里的 pid 正好是刚退出的那个子进程时才清掉它（别人的锁一概不碰）。

**红→绿**：
- 红（HEAD 的 git archive 副本 + 新测试）：`TestRunClosesTurnOnEveryExit` 的 3 条失败
  子用例与 `TestACPPromptClosesTurnOnEveryExit` 的 3 条全部报
  `turn/start 1 条、turn/end 0 条`；
- 绿（工作树）：`ok dshgo/cmd/dshd`，同一组用例含"正常收敛 → completed""被截断 →
  max-tokens"两条成功路径，且断言 `turn/end` **恰好一条**（幂等）。

**还没覆盖的**：写结局本身失败（磁盘满/只读）时日志仍然是悬挂的——这一条无法在日志里
表达，只能在 stderr 说（`cmdRun` 不会用这个错误盖住原始错误）。进程被**硬杀**
（SIGKILL，或 `dshd run` 直接 Ctrl+C 的默认终止）时进程内没有机会跑任何 defer：
serve 子进程由父进程的 `settleTurn` 补；独立的 `dshd run` 无法自补，只能靠退出码/重跑。

### D2 `models.list` 的 `data:null` 与 `data:[]` 要不要区分？

**现状**：两者都变成空列表。`dshd models` 会报「通了，但一个模型都没报」；界面显示空下拉。

- **方向 A（区分）**：`data:null`（字段缺失）当作"网关坏了/不兼容"，返回错误；`[]` 当作"真的没有模型"。
  代价：契约要加一个字段或状态，客户端也要改；某些网关把空列表写成 `null` 会被误判。
- **方向 B（不区分）**：保持现状。代价：用户看到"没有可用模型"，分不清是网关没配还是接口坏了。

### D3 压表逻辑现在有两份 —— **已解决（2026-11-04）**

> 原文保留，后面接结论。原文写的是"要不要合并"，答案是**部分合并**：
> 变换不合并，规则合并。

`internal/agent.CompactSchema` 与 `cmd/dshd` 的 `tightenSchema` 是两套独立实现，都各自"按位置删 description"。
今天两条 bug 恰恰是同一形状在两处各犯一次。

- **方向 A（合并）**：把"哪些键是参数名"的判断收敛到一个共享函数（`internal/spi` 或新的小包），两处都调它。
  代价：`cmd/dshd` 会依赖 agent 包（或新包），需要确认依赖方向不违反 R1/R2；要重跑工具表面积/字节快照。
- **方向 B（保持两份）**：各自带测试 + 扫描器盯住。代价：下次改 schema 规则仍可能只改一处。

#### D3 结论：选 A 的**规则**那一半，不合并**变换**

新增低层包 `internal/schema`（只依赖 `encoding/json`），内容就三件事：

| 导出 | 作用 | 谁调 |
|---|---|---|
| `IsAnnotationKey(k string, inPropertyMap bool) bool` | 唯一判断：这个 `description` 是注解还是参数名 | `cmd/dshd.tightenObject` |
| `IsPropertyMapKey(k string) bool` | 「名字 → schema」词汇表（properties/patternProperties/$defs/definitions） | `internal/schema` 内部 |
| `Compact(raw)` | 「只删注解」这一档变换的**唯一**实现（内部递归函数不导出） | `internal/agent.CompactSchema`（转调）、`cmd/dshd.shapeless` |

依赖方向：`internal/agent → internal/schema`、`cmd/dshd → internal/schema`，
`internal/schema → 只有标准库`。没有 `internal/* → cmd/*` 的边。`go list -deps` 核过。

**为什么变换不整体合并（这是 (c) 那一半）**：它们是两种变换，不是同一个东西的两种写法。
差异是实测的（一次性探针，逐条跑两份实现对照），收敛前后一个字都没变：

| 输入形状 | `agent.CompactSchema` | `cmd/dshd.tightenSchema` | 是不是同一个东西 |
|---|---|---|---|
| 顶层 `"type":"object"` | 保留 | 删 | 否 |
| 空 `properties:{}` / `required:[]` / `additionalProperties:true` | 保留 | 删 | 否 |
| `additionalProperties:false` | 保留 | 保留 | 是 |
| 名叫 `description` 的**参数** | 保留（修好后） | 保留（修好后） | 是（这就是 D3 要收敛的） |
| 参数上的 `description` **注解** | 删 | 删 | 是 |
| `properties` → 参数 → `items` 里的注解 | 删 | 删 | 是 |
| `$defs` / `patternProperties` / `anyOf` / 元组 `items` 里的注解 | 删 | **留**（只递归 properties/items） | **否** |
| `default` 里的同名键（是数据，不是注解） | **删** → 已改成**保留**（2026-11-04，见 §5.2） | 留 | **否**（前者是既有的过度删除，这次不改）→ **已解决** |
| 数字字面量 | 过 `float64`：`9007199254740993` → `…992`，`1e+04` → `10000` | 逐字节不变（`json.RawMessage` 承载子节点） | **否** |
| 非对象根 / 非法 JSON | 递归/原样返回 | 原样返回 | 否 |

一句话：`CompactSchema` 是"一个任何地方都能用的通用压表"，`tightenSchema` 是
"工具表专档、逐字节保守"，两者都不能退让到对方那一侧（让 `tightenSchema` 走 `any`
会动数字；让 `CompactSchema` 删骨架就改变它的对外语义）。所以只收敛**规则**。

> 2026-11-04 补：表里 `default` 那一行不是"两种变换的差异"，而是 `CompactSchema`
> 自己的一个缺陷——它在数据位置上删了值里的字段。已修，见 §5.2；`tightenSchema`
> 那半格（留）正是"应该的样子"。

#### 删掉的重复实现（收敛后改动的行数）

- `cmd/dshd/tool_merged.go`：+36 / −68（净 −32 行）—— 删 `stripDescriptions`、`stripDescNode`、
  `schemaPropertyMapKey`（3 个函数、48 行含注释）；`shapeless` 改成转调 `schemarule.Compact`；
  `tightenObject` 的 `delete(obj, "description")` 改成走 `IsAnnotationKey`。
  顺带把 `newMergedTable` 里的局部变量 `schema` 改名 `params`：测试包里有同名类型 `schema`，
  包名与它冲突，所以导入用别名 `schemarule`。
- `internal/agent/loop.go`：+10 / −64（净 −54 行）—— 删 `compactNode`、`isPropertyMapKey`，
  `CompactSchema` 变成一行转调（签名不动，外部调用方零改动）；`toolDefs` 里的
  `schema :=` 局部变量顺势改名 `params`（与包名同名会遮蔽）。
- 新增 `internal/schema/schema.go`（约 115 行含注释）、`internal/schema/schema_test.go`。
- 新增 `cmd/dshd/tool_merged_rule_test.go`（反漂移测试）；`scripts/silent-success-audit.py`
  守卫从 12 条到 15 条、`scan_desc_deletion` 从 advisory 升级为 FAIL 级守卫。

#### 防再漂（三道）

1. **规则只有一处判断**：`internal/schema.IsAnnotationKey`。扫描器新增 3 条守卫，
   要求 `CompactSchema`（`schema.Compact`）、`tightenObject`（`schemarule.IsAnnotationKey`）、
   `shapeless`（`schemarule.Compact`）都真调那一份。
2. **行为互相对照**：`TestDescriptionRuleIsOneJudge` 把同一张判据表喂给两处实现；
   任何一处退回"见键就删"就红（证据见下）。
3. **第三份实现会被抓住**（反证过）：在隔离副本里加一个 `if k == "description"` 的
   `badStrip`，`python scripts/silent-success-audit.py --root /tmp/hc-d3-guard` 直接
   `exit 1`：
   ```
   FAIL description-deleted-without-position @ internal/agent/zz_bad.go:6
   ```
   这条在旧版只是 advisory（只 note、不红），所以"修好了"之后第三份仍可以悄悄长出来。

#### 红 → 绿证据

反漂移测试 `TestDescriptionRuleIsOneJudge`（`cmd/dshd/tool_merged_rule_test.go`）
把同一张 schema 喂给两处实现，断言四件事：名叫 `description` 的参数（顶层与数组元素里）
必须还在、`required ⊆ properties`、注解必须删、压完是固定点；外加 `sameShape` 的三个方向。

旧实现（`git archive 62ac2ca` 的隔离副本 `/tmp/hc-d3-old`，把那天的代码原样 checkout）上红：

```
$ go test ./cmd/dshd -run TestDescriptionRuleIsOneJudge -v -count=1
=== RUN   TestDescriptionRuleIsOneJudge
=== RUN   TestDescriptionRuleIsOneJudge/agent.CompactSchema
    zz_d3_rule_test.go:76: 名叫 description 的参数被删了：模型只看到 required 里一个没类型的名字
        {"properties":{"command":{"type":"string"},"options":{"items":{"properties":{},"required":["description"],"type":"object"},"type":"array"}},"required":["command","description"],"type":"object"}
=== RUN   TestDescriptionRuleIsOneJudge/tightenSchema
=== NAME  TestDescriptionRuleIsOneJudge
    zz_d3_rule_test.go:120: 嵌套元素里多一个 description 参数也不是同构
--- FAIL: TestDescriptionRuleIsOneJudge (0.00s)
    --- FAIL: TestDescriptionRuleIsOneJudge/agent.CompactSchema (0.00s)
    --- PASS: TestDescriptionRuleIsOneJudge/tightenSchema (0.00s)
FAIL
FAIL	dshgo/cmd/dshd	0.087s
exit=1
```

注意红的是两个不同的子测试：`CompactSchema` 那条是"参数被删、required 悬空"，
`sameShape` 那条是"多一个 description 参数判成同构"——正是 D3 里说的
"同一形状在两处各犯一次"。`tightenSchema` 子测试在那份副本上已经是绿的：
它在 8470b98 里被单独修过，这也正是 D3 存在的理由——**修了一处，另一处靠自觉**。

收敛后同一份测试在共享树上绿：

```
$ go test ./cmd/dshd -run TestDescriptionRuleIsOneJudge -v -count=1
--- PASS: TestDescriptionRuleIsOneJudge (0.00s)
    --- PASS: TestDescriptionRuleIsOneJudge/agent.CompactSchema (0.00s)
    --- PASS: TestDescriptionRuleIsOneJudge/tightenSchema (0.00s)
```

行为不变由四条既有结构比对守住（全绿，未改一行断言）：
`TestCompactTableKeepsContract`（归一化后逐工具相等 + 投影必须是不动点）、
`TestTightSchemaKeepsDescriptionParam`、`TestRequiredParamsAreDeclared`、
`ToolTableSnapshot` 快照（`cmd/dshd/testdata/tools.snapshot.json` 无 diff：两份变换对
**本仓真实 schema** 的产物一个字节都没变）。

**没验证的**：`-race`；真机工具表 A/B（`scripts/tool-table-ab.py`）。这次收敛不改任何
被送出去的字节，所以没有理由重跑 A/B——但如果哪天要动 `tightenObject` 的删除规则，
必须重跑。

## 5. 未改但已登记（越界 / 后续）

> **2026-11-04 更新**：前两条（`serve_turn.go` 的历史过滤、`ServerFrame{Seq: 1}`）
> 当时因为 `cmd/dshd/serve*.go` 被别人占着而移交，**现已修复**——原条目保留不动，
> 证据见 §5.1。后两条仍然登记着。

| 项 | 为什么没改 | 建议 |
|---|---|---|
| `cmd/dshd/serve_turn.go`：`modelRunner` 用 `m.Text == ""` 过滤历史，丢掉只有 tool_calls 的 assistant 消息 | 边界明确不许动 `cmd/dshd/serve*.go`（别人在改） | 改成按 role + `ToolCalls` 判断；补一条"历史里 tool 结果必须能找到对应调用"的测试 → **已解决（§5.1）** |
| `cmd/dshd/serve_turn.go`：`ServerFrame{Seq: 1}` 硬编码，绕过 journal | 同上 | 走 `emitFrame`；否则客户端会看到重复 seq=1、也没有 time → **已解决（§5.1）** |
| `cmd/dshd/serve.go`：`session.compact` 与正在跑的回合会争 `.lock`，报"正被另一个进程写入" | 同上；而且错误措辞把同进程说成"另一个进程" | 由宿主侧串行化压缩与回合，或把措辞改成"这个会话正在被写入" |
| 模块装配的"`Register` 一个都没注册"通用守卫 | 要改 `internal/kernel` 的裁决逻辑（红线 R1/边界） | 若要做，在 `Assemble` 后按模块统计"新增工具数/提供能力数"，为 0 且非纯拦截器模块时 Warn |

### 5.1 移交的两条：已解决（2026-11-04）

改动只有 `cmd/dshd/serve_turn.go` 一个文件（外加新测试 `cmd/dshd/serve_turn_history_test.go`）；
`internal/` 一行没动，`transport.Message` 也没动。

**缺陷比"少一条消息"更深**：`modelRunner` 的历史原来是从 `sessionSource.Messages`
（`transport.Message`）来的，而那个投影**只有 `Text`，没有 `tool_calls`、也没有 `tool_call_id`**。
所以就算保住那条 assistant 消息，工具结果照样是孤立的——`role:tool` 没有 `tool_call_id`，
上游换个严格的实现直接 400。修法是从 `session.Surface` 派生（`run` / `acp` 用的同一份投影：
tool-call 块 → `Message.ToolCalls`，`tool/result` → `Message.ToolCallID`），
压缩遮蔽也按 Surface 折叠（旧路径根本无视压缩）。

**判据只有一份**：`assistantHasPayload(text, reasoning, toolCalls)`，三处调它——
历史过滤、这一轮要不要推帧、要不要落盘。分开写两份的下场是两边迟早漂。
思考块要单独带出来：`llm.Message` 没有 `Reasoning` 字段，`type=reasoning` 的块在投影时被丢弃，
所以把"哪条 assistant 事件带思考块"射成 `seq → 文本`（`reasoningBySeq`）。

**反方向的孤立也补上了**：`assistant` 有 `tool_calls`、却没有对应的 `tool/result`
（`maxSteps` 用满停在工具调用上、半截日志）——带着它发请求，上游要么 400，
要么把下一条用户消息当成工具结果。所以摘掉没结果的调用；摘空了就整条不发。

**红（HEAD `8470b98` 的 `serve_turn.go`，隔离副本里跑同一份测试）**：

```
--- FAIL: TestModelRunnerHistoryKeepsToolCallOnlyAssistant
    历史组装不对：
        得到 user:"看看目录里有几个文件" → tool():"68 个文件" → user:"接着干"
        要   user:"看看目录里有几个文件" → assistant[call_1:bash] → tool(call_1):"68 个文件" → user:"接着干"
--- FAIL: TestModelRunnerHistoryKeepsReasoningOnlyAssistant
    只有思考块的消息被丢了：
        得到 user:"先想想" → user:"想好了吗"
        要   user:"先想想" → assistant<空> → user:"想好了吗"
--- FAIL: TestModelRunnerAssistantFrameUsesJournalSeq
    assistant/message 帧的 seq = 1，要 2（journal 垫了一帧；写死 1 就是绕过了 journal）
--- FAIL: TestModelRunnerPersistsReasoningOnlyResponse
    只有 reasoning 的回答没落盘（事件 1 条，没有 assistant/message）
```

红里那个 `tool()` 值得看：旧的 `tool` 消息连 `tool_call_id` 都是空的。
同一份测试在修复后全绿（`go test ./cmd/dshd -count=1` 整包 ok）。

**绿**：7 条新测试。除上面四条，还有两条"不许误伤"——真正空的消息
（无正文、无思考块、无 tool_calls）仍然被过滤，有正文的照旧；外加一条
"没有结果的工具调用要被摘掉"。

**真机（同一条会话、同一个网关、同一个取证代理，只换二进制）**：

```json
// HEAD 的二进制：第二轮请求
"messages": [
 {"role":"user","content":"用 bash 执行 ls | wc -l，把结果告诉我"},
 {"role":"tool","content":"[exit 0] ...\n3"},   // ← 没有 tool_call_id，前面也没有 assistant tool_calls
 {"role":"assistant","content":"结果为 **3**。..."},
 {"role":"user","content":"刚才 ls | wc -l 的结果是多少？..."}
]
// 修复后的二进制：第二轮请求
"messages": [
 {"role":"user","content":"用 bash 执行 ls | wc -l，把结果告诉我"},
 {"role":"assistant","tool_calls":[{"id":"call_00_egcqA5rgUJwFz2KFyrRp6124",
   "type":"function","function":{"name":"bash","arguments":"{\"command\": \"ls | wc -l\", ...}"}}]},
 {"role":"tool","tool_call_id":"call_00_egcqA5rgUJwFz2KFyrRp6124","content":"[exit 0] ...\n3"},
 {"role":"assistant","content":"结果为 **3**。..."},
 {"role":"user","content":"刚才 ls | wc -l 的结果是多少？..."}
]
```

`seq` 那条也在同一台机器上核过：`session.history` 回的是
`1 assistant/message time=1793762732610` 与 `2 turn/done`——帧带 `time`、补洞读得到，
说明它真的进了 journal（硬编码那版 `time` 是 0，journal 里根本没有它）。
单元测试用的是"先垫一帧"的会话，所以能把"写死 1"和"journal 发号"分开。

**真机没有复现出"行为怪"**：模型两轮都答了 `3`。原因是孤立结果的内容里就写着 `3`，
模型猜得出来。所以这条缺陷的可见症状不是每次都会错，而是**请求本身不合法**——
把它当成"这次没炸"来判绿，正是本报告 §1.1 说的那种错。

### 5.2 同形的两条：子代理转写丢消息、`Compact` 删 `default` 里的数据（2026-11-04，已解决）

两条是同一个判据错误的第三、第四次出现：**"这段内容存不存在"用错了信号**——
一处按正文判（`Text == ""`），一处按键名判（见到 `description` 就删），
都没看"它在什么位置 / 它是什么角色"。两条都在共享树上先红后修。

#### A. `cmd/dshd/subagent_inherit.go`：`if m.Text == "" { continue }`

`subagent_fork` 的继承文本原来走 `sessionSource.Messages`（`transport.Message`，
**只有 `Text`**），再按 `Text == ""` 过滤。于是只有 `tool_calls` 或只有 `reasoning`
的 assistant 消息整条消失。症状比 `serve_turn.go` 那条更隐蔽：子代理不报错，
只是"不知道父会话查过什么"，然后把已经做过的事重做一遍。

**复用点（一个字没重写）**：`serve_turn.go` 里已有的两份——
`assistantHasPayload(text, reasoning, toolCalls)`（"有没有内容"的唯一判据）与
`reasoningBySeq(log)`（`llm.Message` 装不下 reasoning，要另解事件）。
转写改成从 `session.Surface(log)` 派生（与 `modelRunner.history` 同一份投影，
压缩遮蔽也按 Surface 折叠），不再经过 `transport.Message`。

**没有复用的那半，以及为什么**：`historyForModel` 还会摘掉"有调用、没有结果"的
工具调用——那是**请求**约束（发给上游要么 400、要么把下一条用户消息当工具结果）。
转写是给人看的：摘掉它就是这次要修的那种丢数据，所以保留调用本身，
把它写成一句人话 `[调用 bash：{"command":"ls"}]`。

**转写的额外两条约束**：
1. `tool_calls` → `[调用 名字：参数]`，`reasoning` → `[思考：…]`（带正文：子代理需要知道
   父会话**为什么**这么干。这与请求路径相反——那里思考只能出"有没有"，把心里话当
   回答发出去是错的）。
2. 一条消息一行（换行与连续空白折成空格）。理由在 `modules/tool/subagentfork.inheritNote`：
   它按**非空行数**报"继承 N 条历史"，正文带换行时这个数字是假的，模型会以为拿到
   的历史比实际更多。`keep = 20` 改成"先判有没有内容、再取最后 20 条"。

**红（修之前，共享树，同一份测试）**：

```
$ go test ./cmd/dshd -run TestSubagentInherit -count=1 -v
    subagent_inherit_test.go:55: 转写：
        user: 看看目录里有几个文件
        顺便说一下大小
        tool: 68 个文件
    subagent_inherit_test.go:58: 用户消息没进转写（或多行正文没压成一行）：
        ...
--- FAIL: TestSubagentInheritKeepsToolCallsAndReasoning
```

两条 assistant 一条都没有，用户消息还被换行数成了两条——两个症状在同一份输出里。

**绿（修之后）**：

```
$ go test ./cmd/dshd -run TestSubagentInherit -count=1 -v
        user: 看看目录里有几个文件 顺便说一下大小
        assistant: [调用 bash：{"command":"ls"}]
        tool: 68 个文件
        assistant: [思考：先想一下：用户要的是文件数]
--- PASS: TestSubagentInheritKeepsToolCallsAndReasoning
--- PASS: TestSubagentInheritWithoutSession
```

判据（任务书的三条）钉在新测试 `cmd/dshd/subagent_inherit_test.go`：
只有 `tool_calls` 的必须出现、只有 `reasoning` 的必须出现、三样都没有的才算空
（断言 `assistant: ` 恰好两行）；外加"一条消息一行"与"没有会话时返回空串"。

#### B. `internal/schema.Compact`：`default` 里名叫 `description` 的**数据**被删

`stripNode` 递归一切对象，所以进到 `default` 值内部也按 schema 删注解——
可 `default` 的值是**实例数据**，里面的键是默认值对象的字段。删掉一个键等于
把默认值改成了另一个值，两端都不报错。

**修法：按位置判，不再按键名判**。新增 `isDataKey(k)`（不导出，只服务这一份递归），
覆盖 `default` / `const` / `enum` / `examples` 四个数据位置：**整棵子树原样保留，
不再往里递归**。判断规则写进了 `stripNode` 的注释，三条边界：

| 位置 | 这一层的键是什么 | 压不压 |
|---|---|---|
| `properties` / `patternProperties` / `$defs` / `definitions`（`IsPropertyMapKey`） | 参数名 / 定义名 | **键一个都不删**（名叫 `description` 的参数或定义也留），值继续按 schema 压 |
| `default` / `const` / `enum` / `examples`（`isDataKey`） | 数据字段名 | **整棵子树原样保留**，不递归 |
| `items` / `prefixItems` / `anyOf` / `allOf` / `oneOf` / `not` / `if` / `then` / `else` / `contains` / `propertyNames` / `additionalProperties` / `unevaluated*` / `contentSchema` 等等 | schema 本身或 schema 数组 | 继续压，**这一档的 `description` 是注解，删** |

取舍写在代码注释里：这一档**不逐个列关键字**，而是"认得的名字映射按名字走、
认得的数据按数据走、其余对象一律按 schema 走"。漏列一个 schema 关键字的后果是
该删的注解没删（压表胖一点，**不丢数据**）；反过来把不认识的对象当数据去删，
就是这两条 bug。

**红（修之前，共享树，先写测试）**：

```
$ go test ./internal/schema -count=1
--- FAIL: TestCompactKeepsDescriptionInsideDefault
    schema_test.go:155: default 里的 description 字段被删了（那是数据，不是注解）：
        {"items":[{}],"nested":{"properties":{"description":{"type":"string"}}},"type":"ga"}
--- FAIL: TestCompactKeepsDescriptionInsideDataKeys
    schema_test.go:209: const 里的 description 字段被删了：{"value":"a"}（err=<nil>）
```

注意红输出里连 `default.items[0]` 都成了 `{}`：数组元素里的数据字段同样被删。

**绿**：

```
$ go test ./internal/schema -count=1 -v
--- PASS: TestCompactKeepsParamNamedDescription
--- PASS: TestCompactKeepsDescriptionInsideDefault
--- PASS: TestCompactKeepsDescriptionInsideDataKeys
--- PASS: TestCompactStripsAnnotationsInSchemaPositions   # 反方向：schema 位置上的注解一个不留
--- PASS: TestCompactBadInputPassthrough
--- PASS: TestCompactShrinks
```

工具表字节没变：`cmd/dshd/testdata/tools.snapshot.json` 里 `default` / `const` /
`examples` 出现 0 次、`enum` 9 次且都是标量数组，所以这条修复对**本仓真实工具 schema**
的产物一个字节都没动（`internal/agent`、`cmd/dshd` 整包测试照旧全绿）。

#### 同形但没改（越界，登记在此）

- `cmd/dshd/history.go` 的 `eventText`：只拼 `type=="text"` 的块，
  `history_search` 里 `text == ""` 直接跳过——**只有 `tool_calls` 的 assistant 在历史检索里
  查不到**（"上次那个 ls 是哪一轮"搜不出来）。→ **已解决（§5.3，2026-11-04）**
- `cmd/dshd/transport_source.go` 的 `messagesOf`：`transport.Message` 没有 reasoning 字段，
  只有思考的回答在 `session.load` 投影里是**一条空消息**（`serve_turn` 推的帧带
  `reasoning`，读回来就没了）。→ **已解决（§5.3，2026-11-04）**
- `modules/tool/subagentfork` 的 `inheritNote` 按非空行数报"继承 N 条"——
  本次靠"转写一条消息一行"让它算准，模块本身没动（它不认识会话，也不该为这件事改）。

### 5.3 同形的两条投影丢失：history_search 看不见只有调用的轮次、load 读回空消息（2026-11-04，已解决）

边界放开后，两条一起修。它们仍然是同一个判据错误：**"这条消息有没有内容"用错了信号**——
`history_search` 只看 `type=="text"` 的块，`messagesOf` 只看"JSON 解得开"。

#### A. `history_search` 对"只有工具调用"的轮次失明

`eventText` 原来只拼 text 块。模型问"上次那个 `ls` 是哪一轮"时，那一轮的 assistant
只有 `tool-call` 块、正文为空，`eventText` 返回空串，`Search` 直接跳过——
这个工具存在的意义就是"找回过期信息"，却恰好对这种轮次失明。

**修法：可检索正文 = 正文 + 思考 + 工具调用**，形态与 `subagent_inherit.go` 的转写
**共用同一份**（新增的 `toolCallText` / `reasoningText` 放在 `subagent_inherit.go`，
`inheritLine` 与 `eventText` 都调它；`flattenText` 也是同一份）。于是片段里是
`[调用 bash：{"command":"ls"}]`、`[思考：…]`，不是整段事件 JSON 的转义符。
判据因此与 `assistantHasPayload` 对齐：三样都没有的事件仍然返回空串。

#### B. `session.load` 里"只有思考"的回答是一条空消息

`serve` 推的 `assistant/message` 帧本来就带 `reasoning`（正文为空时才带），
但 `transport.Message` 没有这个字段，`messagesOf` 又只取 text 块——
读回来是 `{"seq":1,"role":"assistant","text":""}`，界面上一个空气泡。
断线重连/重开走 `session.load` 时，这条回答就丢了。

**契约决定（只加不改）**：给 `transport.Message` **尾部**加可选字段
`Reasoning string json:"reasoning,omitempty"`；不改任何既有键、不重排既有字段。
**没有**把思考拼进 `Text`：那会把模型的草稿当成"已经给用户的回答"，
和 `serve` 推帧的取舍（有正文时不带 reasoning）也不一致。`messagesOf` 只在
`Text` 为空时填 `Reasoning`，与推帧同形。快照 `internal/transport/testdata/api.snapshot.json`
的 `Message` 键表尾部因此多一个 `reasoning`（只加）；UI 侧快照同步时也只加这个键。

**判据没有第二份**：`messagesOf` 调 `assistantHasPayload(text, reasoning, toolCalls)`
判"这条算不算内容"，思考文本来自 `reasoningBySeq(&v4.Log{Events: events})`——
与 `serve_turn.go`、`subagent_inherit.go` 同一对函数。工具调用数从同一批内容块里数。
延迟到"第一条正文为空的 assistant"才解思考块：绝大多数消息有正文，不必为它们多解一遍。

**红（改之前，共享树，同一份测试）**：

```
--- FAIL: TestHistorySearchFindsToolCallOnlyAssistant
    history_test.go:181: 只有 tool_calls 的 assistant 查不到：history_search 对'只有工具调用'的轮次失明（eventText 只拼了 text 块）
--- FAIL: TestHistorySearchFindsReasoningOnlyAssistant
    history_test.go:230: 片段里没有可读的思考形态："先想一下：用户要的是文件数"
--- FAIL: TestLoadKeepsReasoningOnlyAssistant
    transport_source_test.go:136: session.load 里这条是空消息（text 与 reasoning 都空）：
        serve 推的帧带 reasoning，读回来不能没有：{"seq":1,"role":"assistant","text":"","time":1793766760801}
--- FAIL: TestLoadDropsTrulyEmptyAssistant
    transport_source_test.go:172: 三样都没有的 assistant 仍然算内容（空消息 {Seq:1 Role:assistant Text: ...}）
```

最后一条不是笔误：旧投影只要求"JSON 解得开"就收下，所以**真正空的消息**也会变成
一条空气泡。这次按 `assistantHasPayload` 一并收紧了——"不许误伤"的方向是
"真空仍然不算内容"，不是"真空继续算"。

**绿**：

```
--- PASS: TestHistorySearchFindsToolCallOnlyAssistant
--- PASS: TestHistorySearchFindsReasoningOnlyAssistant
--- PASS: TestHistoryEventTextIgnoresTrulyEmptyAssistant   # 反方向：三样都没有仍然没有可检索正文
--- PASS: TestLoadKeepsReasoningOnlyAssistant
--- PASS: TestLoadDropsTrulyEmptyAssistant                # 反方向：真空仍然不算内容，user 消息不误伤
--- PASS: TestSubagentInheritKeepsToolCallsAndReasoning   # 共用形态后转写字节没变
```

**同类还剩一条（没改，登记）**：`messagesOf` 里**只有 tool_calls、没有正文/思考**的
assistant 仍会投影成 `text` 为空的消息——`transport.Message` 没有 `ToolCalls` 字段，
它的工具名/参数挂在下一条 `tool/result` 上（`Tool`/`Args`），所以信息不算全丢；
要让它带上调用本身，是比"加一个可选字符串字段"更大的契约增量，需要 UI 快照与渲染
一起冻结，本轮不做。

## 6. 复跑与验收

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
export GOCACHE=/tmp/gocache-silent
python scripts/silent-success-audit.py     # 15 条守卫全在，exit 0
go build ./... && go vet ./...
go test ./... -count=1
git archive HEAD | tar -x -C /tmp/hc-ss && (cd /tmp/hc-ss && go build ./...)
```

本次结果：扫描器 0 FAIL；`go build` / `go vet` 通过；`go test ./... -count=1` 全部 `ok`（含 `cmd/dshd`、
`internal/{agent,llm,session,transport}`、`modules/tools`）；HEAD 隔离副本 `go build ./...` 退出码 0。

**D3 复跑（2026-11-04，`GOCACHE=/tmp/gocache-d3`）**：扫描器 15 条守卫 0 FAIL；`go build ./...` /
`go vet ./...` 通过；`go test ./... -count=1` 全部 `ok`；`git archive HEAD` 隔离副本 `go build ./...`
退出码 0。工具表快照 `cmd/dshd/testdata/tools.snapshot.json` 无 diff。

**没有验证的**：真机（需要新 `dshd` + 网关）；`-race`；界面线消费这些新错误码/`session/reset` 的行为。
单测只证明"守卫在对应的函数里生效"，不证明"真实网关/真实 UI 会按预期处理"。

**一个有意收紧的点，真机要确认**：`llm.Stream` 现在要求整条流至少出现一次 `finish_reason` 或 `[DONE]`。
如果本机网关在某些渠道上两者都不发（目前没有证据），那一轮会被判成"流中途断开"而不是"完成"。
这是刻意的取舍：宁可报一次可重试的错误，也不把半句话记成完整回答。真机跑一轮即可确认；
若确有这种网关，放宽点应该加在"内容看起来完整"的判定上，而不是删掉这条守卫。
