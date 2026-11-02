## 0.2.1-go.9（未发布）· 续

**用户自建模式：`$DSH_HOME/modes/*.toml`。**

- `modes.LoadDir(dir)`：一文件一模式，支持 `copy_of` 从内置继承工具白名单
- `modes.Registry`：内置 + 用户合集；用户模式**优先于内置查**（内置 id 已在装载时挡掉）
- `dshd acp --modes-dir <dir>`（默认 `<home>/modes`）
- 四道校验，全部 fail-loud：
  1. 不能用内置 id（内置只读）
  2. 不能自称 `builtin = true`（内置是编译期属性，不是配置项）
  3. `copy_of` 指向的模式必须存在
  4. 同一 id 出现在两个文件里 → 报错

**实测**
```
$ cat tmp/mode-home/modes/strict.toml
id = "strict"
name = "Strict"
description = "只读 + 写，不给 shell"
tools = ["read", "glob", "write"]

$ dshd acp --mode strict
实际调用: ["write","glob","read"]     ← 没有 bash
write 生效 ✅

$ cat modes/evil.toml
id = "default"
$ dshd acp
dshd: modes/evil.toml: 模式 "default" 是内置的，只读；想改就从它复制一份新 id
```

---
## 0.2.1-go.9（未发布）

**模式（preset）真正落地：工具可见性由模式决定。**

之前 `session/set_mode` 只有个 `default` 的空壳——有接口没实现。

- `internal/modes`：`Mode`（ID/Name/Description/Builtin/Tools/Approval）+ `Builtins()` / `Resolve()` / `OverrideBuiltin()`
  - 内置两个：`default`（全开）、`read-only`（只有 read/glob/grep，approval=deny）
  - **未知模式明确报错**，不静默回落到 default
  - 内置模式只读：用户层想改就往报错里撞（PLAN §6）
- `modes.Filter`：按模式裁剪 registry 的**只读视图**（`Register` 直接拒绝）
- `dshd acp --mode read-only`；`session/set_mode` 真的会换工具表

**实测**
```
# read-only 下模型看不到 bash
prompt1 stopReason: end_turn | 调用过的工具: []

# 未知模式
未知模式被拒 ✅: 未知模式 "turbo"（可选：default, read-only）

# 切回 default
切到 default: {"modeId":"default"}
切换后的工具调用: ["bash"]
切换后的回答片段: "`bash` 已经挂上了。执行结果：AFTER-SWITCH"
```

**这一条为什么重要**：模式在会话内不变，工具表才稳定（PLAN §6），前缀缓存才不会被击穿。
`modeOf()` 只在 `SetMode` 时改变，prompt 路径不碰它。

---
## 0.2.1-go.8（未发布）· 续

**查冷启动变慢的原因：一个 TOML 库的 init 占了 23 ms。**

上一版冷启动从 59.5 ms 涨到 65.9 ms（+11%）。趋势不对就查，别等撞线。

**定位过程**（都是可复现的命令）

```bash
# 1. 和"什么都不做"的 Go 程序比：14.2 ms vs 60.6 ms → 我们有 46 ms 是自己的
GODEBUG=inittrace=1 ./dshd.exe version
# 2. 按包看 init 耗时
#    总 init 26.0 ms，其中 BurntSushi/toml/internal 一个人 23.0 ms
# 3. 决定性实验：把 toml 依赖摘掉再编一版，只测启动
#    with toml 58.1 ms → without toml 39.3 ms
```

**修法**：`BurntSushi/toml` → `pelletier/go-toml/v2`（init 耗时 0 ms）。

| 指标 | 换库前 | 换库后 |
|---|---:|---:|
| `dshd version`（median） | 58.1 ms | **40.0 ms** |
| ACP 就绪（起到 initialize 应答） | 65.9 ms | **55.9 ms** |
| 进程 init 总和 | 26.0 ms | **2.0 ms** |

顺带加了 `DSHD_TIMING=1`：打印 `main 开始 / manifest 已读 / 装配前 / 装配后` 的耗时，
下次再涨不用重新发明这套测量。

---
## 0.2.1-go.8（未发布）· 续

**语义回归：压完之后，模型还记不记得暗号？**

结构对不对（事件数、遮蔽列表、顺序）由 Go 测试保证；这一条只验语义：
把关键信息压没了，结构再对也没用。

`scripts/acp-compact-semantics.mjs`：说一个暗号 → 灌几轮填充 → 让自动压缩触发 → 问暗号。

**第一次跑就抓到了第二个 bug**：
```
[agent] 超预算且无处可压：放宽到只保留最近 3 条
[agent] 自动压缩失败（不影响本轮）：没有可摘要的消息
```

原因：替身被插到 surface 最前面，**但它的 seq 是最后追加的**。
`Plan` 选完区间后按 `[start,end]` 回头筛节点，seq 顺序和 surface 顺序不一致 → 筛出 0 条。

修法：`compact.Select` 直接返回**选中的节点本身**，不再让调用方按 seq 回筛。
`Plan` 保留为薄封装。新增 `TestSelectHandlesCheckpointOrdering` 锁死这个形状。

**修好之后**
```
[agent] 自动压缩：遮蔽 2 条，摘要 239 字
[agent] 超预算且无处可压：放宽到只保留最近 4 条
[agent] 自动压缩：遮蔽 2 条，摘要 165 字
压缩触发次数: 2
回答: "COMPACT-KEY-7"
PASS: 压缩后仍记得暗号
```

---
## 0.2.1-go.8（未发布）

**批量回归抓到一个真 bug：压缩摘要被放在了对话末尾。**

拿 15 个真实会话做"压缩 → 读回"的批量回归，14 个直接失败，症状一致：

```
最后一条消息变了："T13 完成（task-13 已标记为完成…)" → "（批量回归用的固定摘要）"
```

原因：`compaction/summary` 事件是追加到日志**末尾**的，而 `Surface` 按事件顺序铺节点，
于是替身节点排在了整段历史的最后面。模型读到的历史变成：
"…最近 6 条对话… → 然后是一句『前面的历史摘要如下』"——顺序反了。

**修法**：替身在 `Surface` 里插回**它遮蔽的那段起点**（`shadowedRange.start`），
而不是按事件自己的 seq 追加。同时过滤掉"被后一次压缩遮蔽掉的摘要"（级联压缩）。

- `internal/session/compact_corpus_test.go`：批量回归，四条不变量
  1. 事件数 +3
  2. 被遮蔽的 seq 仍能从日志里读到（append-only）
  3. surface 变少
  4. **最后一条消息不变**（← 这条抓出了 bug）
- 单例测试的期望也改了：摘要现在在最前面，而不是跟在最近消息后面

**回归结果**
```
批量回归：通过 14 / 跳过 1 / 失败 0
surface 816 → 7（遮蔽 790 条）、3273 → 7（遮蔽 1681 条）
```

**教训**：单例验证只能证明"在一条数据上能跑通"。这次是批量跑才暴露出**顺序**问题——
而顺序错了，模型看到的历史就是颠倒的，压缩等于把上下文搞乱。

---
## 0.2.1-go.7（未发布）· 续 10

**补齐三个 ACP 方法：`session/load`、`session/delete`、`session/set_mode`。**

- `session/load`：载入并**重放**历史（`resume` 不重放，两者语义不同）。
  重放走新增的无状态投影 `updateFor`——prompt 那条路径需要跨事件拼 msgID，这里一条事件一个 update
- `session/delete`：删文件 + 内存 + 授权记录。**cwd 可省略**——SDK 的 `deleteSession` 只给 sessionId，
  由 `session.FindByID` 在 `<home>/sessions/*/<segment>/` 下找；找到多个就报错，不随便挑一个删
- `session/set_mode`：目前只有 `default`；未知模式**明确报错**，不静默接受

**实测（官方 SDK 客户端）**
```
原会话: acp-27022ec8-...
load 重放的消息块: 3 | 首块: "Runtime: dsh-go.
Mod"
set_mode → {"modeId":"default"}
delete 完成
会话文件已删除 ✅
```

**顺带修了一个"保护过度"的 bug**：`session/delete` 一开始被 `HistoryGuard` 拦了——
因为路径里有 `sessions` 目录。但我们自己的 home 本来就叫 sessions。
新增 `UnderDSHHome()` 只判"是不是在 `$DSH_HOME` 下面"，内部操作用它，
CLI 的 `--force` 启发式保持不变。

---
## 0.2.1-go.7（未发布）· 续 9

**修 `usage_update` 的 `used`：它以前是"累计花了多少"，应该是"现在占了多少"。**

老代码：`r.used[sessionID] += a.Usage.TotalTokens` —— 跑三步就是真实值的三倍，
客户端会以为上下文快撑爆了，进而触发不必要的压缩或报错。

- `contextUsed(prompt, completion, total, estimate)`：一次调用的上下文大小
  1. 有 prompt/completion 就用它们的和（服务端实测最可信）
  2. 只有 total 就退回 total
  3. 启发式估算更大时**取估算**——宁可早报警，别等真撑爆
- `--context-window`（默认 128000）：usage_update 的分母，现在可以改
- `session.HistoryToSurface`：把 canonical 历史转成 surface 消息给 meter 用（seq 记 -1）

**实测（三轮对话，官方 SDK 客户端）**
```
used: 4126 → 4167 → 4324   ✅ 缓慢增长（上下文变大）
（修复前会是 4126 → 8293 → 12417，线性累加）
```

---
## 0.2.1-go.7（未发布）· 续 8

**`dshd run` 补上审批守卫：以前它对写类工具是完全放行的。**

- `dshd run --policy ask|allow|deny|auto`（默认 `auto`）
  - `auto`：**终端里就问，非终端一律拒绝**——没人能回答的时候，"默认同意"是最坏的选择
  - 复用 ACP 那条 `guardedRunner`：按工具自己声明的 `Caps()` 判定，不按名字猜
- `makeAskFuncWith(policy, interactive)`：把"是不是终端"显式传进来，两个分支都能测

**实测**

```
# deny：模型想跑 bash
→ 被拒；模型回："如果你要我重试，请批准上面这条 echo HELLO…由于该命令未被执行，我无法给你真实的输出"
  （没有编造输出，这是关键）

# allow：正常执行
call: bash {"command": "echo HELLO-42", "description": "输出 HELLO-42"} → 结果含 HELLO-42 ✅

# 未知策略
dshd: 未知 --policy "yolo"（可选 ask/allow/deny/auto）
```

---
## 0.2.1-go.7（未发布）· 续 7

**审批跨进程保留：`--remember-approvals`（默认关）。**

上一轮把审批记忆做成"进程内、不落盘"，理由是"resume 后重问一次更安全"。这一轮补上**选择权**：
想要跨进程保留就显式打开，不打开就维持原样。

- `internal/approval.Store`：写到 `<home>/state/approvals/<sessionID>.json`，原子替换
- **不写进 v4 日志**：DSH 的 `approval/policy` 事件形状是 `{"policy":"never","source":...}`，
  **没有工具维度**。往里加 `tool` 键属于自造字段——一旦 DSH 解析更严，整份日志会被判坏。
  所以授权状态放旁边，会话日志保持 100% DSH 干净
- `dshd acp --remember-approvals`：默认关闭；打开后 `allow-always` 落盘，resume 时读回

**实测（两个独立进程）**
```
[A] session/new → 用 bash → 问 1 次（选 allow-always）
[B] session/resume → 用 bash → 问 0 次
PASS: 授权跨进程保留
```

状态文件内容：
```json
{ "allowed": ["bash"], "updatedAt": "2026-11-02T19:14:25+08:00" }
```
不开这个开关时，`state/` 目录根本不会出现（已验证）。

---
## 0.2.1-go.7（未发布）· 续 6

**返回结构对拍：`read` / `write` 改成 DSH 的 XML 包裹。**

从真实会话里配对 `tool/call` 与 `tool/result` 后看到的 DSH 形状：

```
read : <path>a.md</path><type>file</type><content>1: ...</content>
write: <path>...</path><type>file</type><content>Created file</content>
```

- `spi.Result` 新增 `Text`（工具渲染好的模型可见正文）；`session.ProjectResult` **Text 优先**，
  没有时才退回 `Summary + JSON(Data)`
- `read`：包裹 + **行号前缀**（`1: `），offset/limit 生效
- `write`：包裹 + `Created file` / `Updated file`

模型看到的样子和 DSH 一致，内部仍保留结构化 `Data`（UI/测试用）。
其余工具（glob/grep/bash/job_*）保持"结构化 + 摘要"——DSH 那几种没有包裹，两种形状模型都认。

---
## 0.2.1-go.7（未发布）· 续 5

**工具契约对拍：拿 DSH 发给模型的那张表，逐项比。**

数据来源是真实会话的 `request/header.header.tools[]`——**模型真正看到的东西**，不是文档。

| 结果 | 工具 |
|---|---|
| 完全一致（6） | `read` `write` `edit` `job_output` `job_kill` `job_list` |
| 超集（3） | `glob`（多 limit）、`grep`（多 ignore_case/limit）、`bash`（多 idle_seconds） |

**修掉的三处真差异：**

1. `shell` → **`bash`**：DSH 的工具名是 bash，按 DSH 习惯写的调用会打空
2. `job_output`/`job_kill` 的 `id` → **`job_id`**，`wait_seconds` → **`wait` + `timeout_ms`**
3. `bash` 补上 **`description`（DSH 标为 required）**——功能上无关，但那是 DSH 的调用契约

新门 `TestToolParityWithDSH`：只拦会让模型调用失败的差异（同名工具缺失、DSH 必需参数我们不认），
措辞与可选参数只报告不判错。

工具表 token：**906 → 1,294**（+43%，因为补了 description 与 job 参数），仍只占 6k 预算的 22%。

---
## 0.2.1-go.7（未发布）· 续 4

**`session/fork`：从已有会话派生新会话，继承 surface 不继承授权。**

- `session.Fork`：读源日志 → 派生 surface → 写成一份**新的合法 v4 日志**
  - header 记 `parentSession`（血缘可查），cwd 可改
  - 继承的是 surface 而不是原始事件：被压缩遮蔽过的东西不会跟过去
  - 压缩替身改成 `source.kind = "compact-checkpoint"` 的 user 消息
- `dshd session fork --in <f> --home <h> [--cwd] [--id]`
- ACP：`session/fork`（比 DSH 多实现一个方法，属超集）

**实测（跨会话继承，官方 SDK 客户端）**
```
parent: "记住暗号 FORK-KEY-7" → ok
fork → session-e1a9d319-...
child : "刚才的暗号是什么" → "FORK-KEY-7"
PASS
```

**一个意外收获**：真实语料里 DSH **自己**也用 `user/message` + `source.kind = "compact-checkpoint"`
来表示压缩替身——我为了 fork 选的那个编码，正好和上游一致。之前 `TestSurfaceRealCorpus`
的断言"可见摘要 ≤ compaction/summary 次数"因此是错的，已改成把这两种来源都算上。

---
## 0.2.1-go.7（未发布）· 续 3

**审批记忆：说过"以后都允许"就真的不再问。**

- `internal/approval.Memory`：按 **会话 × 工具** 记批准，进程内、不落盘
  - 不落盘是刻意的：resume 之后要重新批准——宁可多问一次，也不让旧会话带着别人的授权继续改文件
  - 批准 `shell` 不等于批准 `write`
- `session/request_permission` 增加第三个选项 `allow-always`（kind `allow_always`，ACP 标准里本来就有四类，
  DSH 只发两个；我们多发一个，对只认两个的客户端无影响）
- `guardedRunner` 先查记忆，命中就**跳过**协议往返

**实测（3 次 shell 调用，客户端第一次就选 allow-always）**
```
prompt → end_turn ×3
审批请求次数: 1
第一次请求的选项: allow-once|allow-always|reject-once
结果: PASS（记住后不再打扰）
```

---
## 0.2.1-go.7（未发布）· 续 2

**压缩接进 agent 循环：每轮前自检，超预算自动压。**

- `session.Meter`（PLAN §10 的 token 单一口径）
  - 启发式：ASCII 4 字节 ≈ 1 token、非 ASCII 1 字 ≈ 1 token、每条消息 +4 结构开销
  - **锚点校准**：喂过一次服务端 `usage.totalTokens` 之后，按「实测/估算」的刻度缩放
  - 没锚点时纯启发式，够触发压缩、不够做账（这点写进代码注释）
- `session.HistoryGuard`：`dshd session compact` 拒绝改动 `$DSH_HOME` 下的历史，
  路径里带 `sessions` 段也要求 `--force`
- `dshd acp --auto-compact <tokens>`（默认 40000，0=关）
  - 每轮开始前：读日志 → 派生 surface → 估算 → 超阈值就摘要+落事件 → 重新派生历史
  - **超预算但"保留最近 N 条"把可压区间挤没时，放宽到只保留一半**
    （宁可摘要粗一点，也不能让这一轮超窗）
  - 每步 assistant 的 `usage.totalTokens` 喂回 meter 做锚点

**实测（3 轮真机对话，阈值故意设 1）**
```
prompt → end_turn
prompt → end_turn
[agent] 超预算且无处可压：放宽到只保留最近 2 条
[agent] 自动压缩：遮蔽 2 条，摘要 248 字
prompt → end_turn
自动压缩触发次数: 1
```
落盘结果：16 事件 / surface 6 节点（1 摘要）/ Node 读 17 帧 ✅

---
## 0.2.1-go.7（未发布）

**自动压缩：区间自己算，摘要模型写。**

- `internal/compact`
  - `Plan`：只压前缀；**最近的 `KeepRecent` 条一定留**；开头的 `system/message` 不压
    （压掉它模型就忘了自己是谁——DSH 的节点 0 保护）
  - `Summarize`：有界提示词（单条 1,200 字、总量 24,000 字，超了写"省略 N 条"）
  - 更早的摘要节点标成 `[earlier-summary]`，不伪装成用户原话
- CLI：`dshd session compact --auto [--keep N] [--max N] [--dry-run]`

**实测（真实会话副本，345 事件 / 111 surface 节点）**
```
--dry-run  → 计划：压缩 102 条（seq 9..313），保留最近 8 条
--auto     → 摘要 730 字 → 遮蔽 305 条事件（日志 345 → 348）
压缩后     → surface 111 → 10 节点（1 个摘要 + 9 个近期）
Node 读    → 192 帧 / 349 行 / 320,684 字节 ✅
```

摘要质量抽样：目标、已确认的约束、关键文件路径（`internal/gateway/sniff.go` 的 `usageSniffer`）都保住了。

**踩坑**：第一次 `MaxTokens: 600`，模型把预算全花在思维链上，`content` 空、`finish_reason=length`。
现在的错误信息会直接说这件事（"推理型模型可能把预算花在了思维链上"），并把预算提到 2000。

---


**压缩协议落地：能往真实会话里追加 `compaction/summary` 了。**

- `session.Compact`：一次追加三条事件 `compaction/start` → `compaction/summary` → `compaction/end`
  - `shadowedSeqs` 是**精确列表**（不是范围近似）
  - 日志 append-only：17 条 → 20 条，被遮蔽的事件一条不少
  - 返回 `compactionId` 与被遮蔽列表
- `session.OpenSessionAt`：按路径续写（CLI 用，不要求文件在 `<home>/sessions` 布局里）
- CLI：
  - `dshd session compact --in <f> --from N --to M --summary "..."`（原地追加）
  - `dshd session info --in <f>`：打印事件数 / surface 节点数 / 摘要数 / 各类型计数
- 三条测试：遮蔽+追加、空区间报错、**级联压缩**（第二次把第一次的摘要也遮蔽掉，只剩最后一个可见）

**实测（真实会话副本，原件只读）**
```
压缩前: 事件 17（6 帧）  surface 4 节点，摘要 0
compact --from 0 --to 10 → 遮蔽 11 条
压缩后: 事件 20（9 帧）  surface 2 节点，摘要 1
Node 读: 9 帧 / 21 行 / 16527 字节  ✅
```

**下一步**：接 LLM 生成摘要 + 阈值触发（`agent/pre-step` 里按 surface token 数判断），
现在是"协议通了、还需要人喊一声"。

---
## 0.2.1-go.6 — 2026-11-02

**修一个只有"下载发布件跑一次"才会发现的问题：没有 `dsh.toml` 时加载 0 个模块。**

- 现象：把 `v0.2.1-go.5` 的 windows 二进制下载到空目录执行 `dshd tools`，报
  `缺 "tools" 能力：没有模块 provides 它；在 dsh.toml 里启用提供该能力的模块`
- 根因：`LoadManifest` 对不存在的文件返回**空 manifest**，而 `Apply` 把 manifest 当白名单，
  空清单的结果就是"一个模块都不开"——二进制离开了仓库就废了
- 修复：`Manifest.Present` 标记文件是否真的存在；**不存在 = 默认全开**，存在 = 白名单
  （空文件依旧表示"什么都不开"）
- 新增两条锁死语义的测试：`TestMissingManifestEnablesAll` / `TestEmptyManifestDisablesAll`
- `CHECKLIST.md` B 表加一条：**把发布件下载到空目录跑一次**

教训：`scripts/check.sh` 全绿、CI 全绿、150 个测试全绿，都拦不住这个——因为测试全在仓库根跑，
永远有 `dsh.toml`。**发布件必须在"什么都没有的目录"里验一次。**

---
# CHANGELOG

格式：`## <版本> — <日期>`，每条注明**上游映射**。语义化：修 bug → `go.N+1`；跟上游新版本 → `<新版本>-go.1`。

## 上游映射表

| 本仓版本 | DSH 上游 | 移植了什么 | 跳过什么 |
|---|---|---|---|
| `0.2.1-go.5` | `0.2.1-rc.5` | 会话 v4 契约、61 工具 schema、ACP 方法表、cordis 装配语义 | Electron/React 前端、provider 生态、`!!js` |

---

## 0.2.1-go.5 · M5-a — 2026-11-02

**打包与 P0 验收：三条资源线第一次全部有实测数字。**

- `scripts/build.sh`：一次出三个目标（windows/amd64、linux/amd64、darwin/arm64），
  `-trimpath` + `-s -w` + 注入 `commit`/`buildTime`，产出 `dist/` 与 `SHA256SUMS`
- `dshd version` 现在打四行：版本、commit、构建时间、Go 版本
- `scripts/measure-mem.py` / `scripts/measure-start.py`：内存与冷启动的可复现测量
- `docs/p0-acceptance.md`：四条判据逐条记录命令与数字，**含"没达成的部分"**

**实测（2026-11-02）**

| 指标 | 目标 | 实测 | 余量 |
|---|---:|---:|---|
| 常驻内存（整轮峰值） | <150 MB | **15.3 MB** | 9.8× |
| 冷启动（进程起到 initialize 应答） | <100 ms | **59.5 ms**（median，min 58.7） | 1.7× |
| 工具表 | ≤6k token | **906 token** | 6.6× |
| 二进制 | ≤25 MB | **8.1 MB**（windows/amd64） | 3× |

三个平台的产物：windows 8.1 MB / linux 7.9 MB / darwin-arm64 7.4 MB。

对照 DSH 本体 870 MB，差 **57 倍**。

---
## 0.2.1-go.5 · M4-c — 2026-11-02

**提示词装配：系统提示从一句硬编码变成可组合的段。**

- `internal/prompt`
  - `Provider` 接口 + `Assemble`：按顺序拼接非空段，**确定性**（同输入同字节）
  - `Runtime`：模型 / 工作区 / 平台 / 日期（路径强制正斜杠）
  - `ToolGuide`：按 **Caps** 把工具分成"只读"与"要批准"，并写清被拒后怎么做
  - `WorkspaceRules`：把工作区 `AGENTS.md` 带进来，**有界 + 标注截断 + 写明字节数**
- `dshd run` / `dshd acp` 都改用装配结果；系统提示随之作为事件落盘
- 新参数 `--workspace-bytes`（默认 8000，0 = 不传）

**实测：AGENTS.md 的价格（同一个问题"9 的平方根"）**

| 系统提示 | system 字节 | inputTokens |
|---|---:|---:|
| 一句话硬编码 | ~60 | **1,909** |
| 装配 + 8 KB 上限（默认） | 8,983 | **4,077** |
| 装配 + 全量 14 KB | 14,853 | **6,248** |

结论：带全部 AGENTS.md 每轮多花 **4,339 token**（×3.3）。默认取 8 KB——规则主体还在，代价砍掉 35%。

---
## 0.2.1-go.5 · M4-b — 2026-11-02

**surface 投影：压缩遮蔽、替身节点、system 消息进历史。**

- `codecs/v4`：`Event` 现在解析信封上的 `surfaceOp` / `sourceEventSeqs` / `ignorable`；
  `SurfaceOpKind()` 区分 `"append"` 与 `{op:"replace", startSeq, endSeq}`
- `session.Surface(log)`：
  - 只有 5 类事件参与派生；其余只审计不派生
  - `compaction/summary` 与 `compaction/prune` 的 `shadowedSeqs` **从 surface 消失**（日志本身一条不少）
  - `compaction/summary.summary` 作为 `compact-checkpoint` 替身节点进入 surface
  - `surfaceOp: replace` 把 `[startSeq,endSeq]` 摘掉
- `session.History` 改为走 Surface；**`system/message` 不再被丢**——resume 之后模型仍知道自己是谁
- `dshd run` / `dshd acp`：系统提示作为**耐久事件**落盘（DSH 退休 `request/header.header.system` 后的做法）

**真实语料实测**

| 会话 | 可派生 | surface | 压缩 |
|---|---:|---:|---|
| session-00b50089 | 7,216 | **1,378** | 22 次 |
| session-8cc34e32 | 2,283 | **686** | 20 次 |
| session-c0a5847a | 803 | **365** | 9 次（全部 summary） |

**一个只有真数据才会暴露的现象**：9 次压缩却只看到 **1 个**摘要节点——因为后一次压缩会把上一次的摘要节点也遮蔽掉（**级联压缩**）。同理 `compaction/prune` 是纯裁剪，不产生替身，所以"压缩次数"和"可见摘要数"本来就不该相等。

写出的新会话示例（9 事件 → 5 节点）：
```
[0] system/message    src=system     "你在 dsh-go 宿主里工作…"
[1] user/message      src=user       "记住这个暗号：BLUE-FALCON-42…"
[2] assistant/message src=assistant  "OK"
[3] user/message      src=user       "刚才我让你记住的暗号是什么"
[4] assistant/message src=assistant  "BLUE-FALCON-42"
```

---
## 0.2.1-go.5 · M4-a — 2026-11-02

**工具能力声明落地：审批不再按工具名猜（R3.6 的账还了）。**

- `spi.Tool` 增加**必填**的 `Caps() Caps`：`{writes, executes, network, destructive}` + `NeedsApproval()`
  - 必填是刻意的：漏声明编译期就炸，不会静默变成"只读工具"
- 9 个工具逐个认领：`write`/`edit` = writes；`shell` = writes+executes；`job_kill` = destructive；其余只读
- `guardedRunner` 改成查 `Caps().NeedsApproval()`，**删掉 `needPermission` 名字表**
  （dsc 的反例：`sandbox.go:52` 按名字判断，改个名字就绕过审批）
- 工具表快照现在含 `caps`；`TestWriteToolsDeclareCaps` 做成门：新工具必须在那张表里被显式认领

**实测（官方 SDK 客户端）**

| 工具 | caps | 审批请求 |
|---|---|---:|
| `glob` | `{}` | **0** |
| `shell` | `{writes, executes}` | **1**（allow-once） |

---
## 0.2.1-go.5 · M3-d — 2026-11-02

**`session/resume`：会话状态跨进程活着——历史从 v4 文件读回来。**

- `internal/session/history.go`
  - `History(log)`：从 5 类耐久事件派生模型可见历史（user / assistant 含 tool-call / tool）
  - `OpenSession`：校验 header 后以 **append** 模式续写同一个文件（seq 从已有条数继续）
  - `SessionPath`：按 projectKey + segment 定位文件
- `internal/acp`：`session/resume` 方法 + `Agent.Resume` 接口
- `cmd/dshd/acp.go`：resume 时读文件 → 派生历史 → 接回内存

**实测（跨进程，官方 SDK 客户端）**
```
[A] session/new: acp-fd9146af-...
[A] prompt "记住暗号 BLUE-FALCON-42" → OK
[A] close + 退出进程

[B] session/resume 同一 id → 200 OK
[B] prompt "刚才的暗号是什么" → "BLUE-FALCON-42"
PASS
```

**真实语料派生**：3 个历史会话分别派生出 210 / 94 / 187 条消息（user 7/6/8，assistant 79/40/70，tool 124/48/109）。

**踩坑**：`OpenSession` 忘了设 `now` 时钟，第一次 `Append` 就 nil 调用 panic；panic 又让文件句柄没关，Windows 上表现为 `TempDir` 清理失败——两个现象一个根因。

---
## 0.2.1-go.5 · M3-c — 2026-11-02

**审批链路打通 + 用量上报：客户端第一次真正有能力说"不"。**

- `internal/acp`
  - `Server.CallClient`：agent → client 的请求（带 id 等待方表），支持 ctx 超时
  - Serve 现在区分「请求」与「应答」：无 `method` 的消息投递给等待方
  - 新增 `usage_update` 分支（`{used,size}`）
- `cmd/dshd/acp.go`
  - `guardedRunner`：`write` / `edit` / `shell` / `job_kill` 执行前先发
    `session/request_permission`（`allow-once` / `reject-once` 两个选项，与 DSH 一致）
  - 用户拒绝时返回带 Hint 的结果：模型能读到"用户拒绝了这次调用"，并自己换只读方式
  - 每会话累计 token，assistant 消息后发 `usage_update`
- `agent.EventUsage`：用量事件（只给 ACP/UI 看，不落会话）

**实测（官方 SDK 客户端）**

| 场景 | 结果 |
|---|---|
| 允许 | `审批请求=1 shell:allow` → 执行 `echo hello-dsh-go` → 回答带原文输出 |
| 拒绝 | `审批请求=1 shell:deny` → 模型："你拒绝了这次调用，所以没有执行…我可以用只读命令代替" |
| 用量 | `usage_update: used=1266 size=128000` |

**未做**：`session/resume`（恢复已持久化会话）。

---
## 0.2.1-go.5 · M3-b — 2026-11-02

**官方 ACP SDK 当客户端连上来了——不是自测，是第三方库验收。**

- `scripts/acp-sdk-client.mjs`：用 `@agentclientprotocol/sdk@1.4.0` 的 `ClientSideConnection` +
  `ndJsonStream` 驱动 `dshd acp`（SDK 在 asar 里，需 Electron 的 Node 模式）

**实测两次**

| 场景 | 结果 |
|---|---|
| 纯问答 | `stopReason=end_turn`，reasoning 1 分片 / 64 字，text 2 分片 / 16 字，3.0s |
| 带工具 | `stopReason=end_turn`，`tool_call` = glob，text 1 分片，回答"68 个 .go 文件"，2.5s |

`initialize` 回 `dsh-go-acp 0.2.1-go.5 protocol=1`，`session/new` 回 2 个 configOptions，
两个 `session/update` 变体（thought / message）与 `tool_call` 都被 SDK 正常解析。

**P0 判据①达成**：第三方 ACP 客户端能连、能收到 reasoning、能拿到 stopReason。

---
## 0.2.1-go.5 · M3-a — 2026-11-02

**ACP 服务端上线：stdio JSON-RPC，reasoning 与 text 都以 ACP 变体流出去。**

- `internal/acp`
  - JSON-RPC 2.0 over stdio（**stdout 只放协议**，scanner 上限 8 MB）
  - 已实现：`initialize` / `authenticate` / `session/new` / `session/list` / `session/close` /
    `session/prompt` / `session/cancel` / `session/set_config_option`；未挂载的方法明确回 `-32601`
  - **prompt 异步执行**：否则客户端在 prompt settle 前发的 `session/cancel` 进不来（读循环被堵死）
  - 投影只发 ACP 定义过的 4 个变体：`agent_thought_chunk` / `agent_message_chunk` /
    `tool_call`（`rawInput` 解析成对象）/ `tool_call_update`（completed/failed）
  - `stopReason`：正常 `end_turn`，取消 `cancelled`；usage 回 `{inputTokens, outputTokens}`
- `cmd/dshd/acp.go`：把 ACP prompt 接到真实 agent 循环 + v4 会话日志（先落盘，再上 wire）
- `scripts/acp-smoke.py`：按真实客户端方式对话的冒烟脚本

**实测（真机 magpie）**
```
initialize: dsh-go-acp 0.2.1-go.5 protocol=1
session/new: acp-970e325c-...
[think] The user asks a simple question: square root of 9...
[text] 3（若指代数平方根 √9，结果为 3；数学上 9 的平方根是 ±3）
prompt: stopReason=end_turn updates=4 用时=1.00s usage={inputTokens:1234, outputTokens:85}

另一次带工具：glob → completed → “当前目录下有 68 个 .go 文件”（updates=3，19.78s）
```

**未做（M3 后续）**：`session/resume`、`session/request_permission`、`usage_update`、
`config_option_update`、以及 agent→client 的 `fs/*` `terminal/*`（DSH 自己也不用这些）。

---
## 0.2.1-go.5 · M2-d — 2026-11-02

**会话写入路径接通：`dshd run` 真的落一个 v4 文件，Go 与 Node 都读得动。**

- `internal/session/log.go`
  - `CreateSession(home, cwd, id, preset)` → `<home>/sessions/<projectKey>/<segment>/session.v4.jsonl.zstd`
  - `Append(type, data)` 分配 seq（文件内连续）并落一帧
  - 事件构造器按**真实语料**抄形状：`user/message`（content/source/role/id + surfaceOp）、
    `assistant/message`（turn/step/message/stream/usage + surfaceOp）、`tool/call`（arguments 是 JSON 字符串）、
    `tool/result`（message.role=tool + toolCallId + isError + sourceEventSeqs）
- agent 循环新增 `assistant/message` 事件（带完整响应与 usage），供会话层落盘
- `dshd run` 现在边跑边写会话，结束时打印路径与事件数

**实测**
```
dshd run --prompt "用 glob 数 .go 文件" → 12 事件，路径 .../--D-DeepSeek~0020Harness-default-workspace-DSH-GO--/session-.../session.v4.jsonl.zstd
Go 读回：12 事件 / 2 帧
Node  读：13 帧 / 13 行 / 4485 字节，header OK
```

**已知差异（待对齐）**
- `assistant/message.stream` 只写 `{type:"finish",reason}`，缺 `replayState` 等回放细节；
- `usage` 写的是**单步** token，DSH 的真实语料里 `totalTokens` 是**累计**值；
- 一事件一帧（DSH 是 200ms 批窗口），本地写放大但语义等价。

---
## 0.2.1-go.5 · M2-c — 2026-11-02

**LLM 流式客户端（OpenAI 兼容），真机跑通 reasoning 增量。**

- `internal/llm`
  - `Client.Stream(ctx, req, onEvent)`：SSE 解析 → 增量事件（reasoning / content / tool_call / done）→ 完整响应
  - `reasoning_content` 与 `reasoning` 两种字段名都认（DeepSeek 系与部分网关不同名）
  - 工具调用按 `index` 拼碎片：id / name / arguments 三段都可能分片到达
  - `stream_options.include_usage` 默认打开，`usage` 从最后一片读
  - 坏分片直接报错，**不静默跳过**；回调返回错误即中止（下游可以随时叫停）
- 8 条 httptest 单测（不碰网络）：内容拼装、reasoning 顺序、工具调用拼装、usage、401、坏分片、回调中止、ctx 取消
- `TestLiveMagpie`（默认跳过，`DSHD_LIVE_LLM=1` 才跑）

**真机实测（本机 magpie）**
```
模型=group/deepseek-v4.1-flash1017 时长=1.819s 分片=6
reasoning=290 字节 content=109 字节 finish="stop"
usage: prompt 41 / completion 125 / total 166
```

---
## 0.2.1-go.5 · M2-b — 2026-11-02

**会话 v4 编码器：写出来的东西 Node 读得动。**

- `codecs/v4/write.go`
  - `Create` / `OpenAppend` / `Append` / `Close`
  - 每帧用 `EncodeAll`（**完整独立帧**，不是 Flush），只开 CRC、不设 level
  - header 按 DSH 的**字段顺序**序列化，可选字段缺省即不写
  - `Event.Raw` 优先：已有事件按原始字节回写，保真
  - 每次写帧后 `fsync`（DSH 的契约是「fsync 之后才 resolve」）
  - `OpenAppend` 先校验整份文件再追加，损坏的文件拒绝追加
- `scripts/verify-node-reads.mjs`：用 `node:zlib` 的 zstd（**DSH 自己用的就是它**）逐帧解我们的文件，并复刻 `assertZstdHeaderFrame` 的断言
- 测试：往返、首帧单行、CRC 标志、append-only 前缀不变、header 字段顺序

**实测（真实会话 → Go 写 → Node 读）**

| 原文件 | 事件 | 帧 | Node 读取 |
|---|---:|---:|---|
| 413 B | 4 | 2 | 2 帧 / 5 行 |
| 421 B | 4 | 2 | 2 帧 / 5 行 |
| 425,769 B | 399 | 21 | 21 帧 / 400 行 |
| 431,346 B | 287 | 16 | 16 帧 / 288 行 |
| 11,071,420 B | 13,869 | 695 | 695 帧 / 13,870 行 |
| 19,040,594 B | 24,478 | 1,225 | 1,225 帧 / 24,479 行 |

12 个真实会话的「读→写→再读」事件字节逐条相同。

---
## 0.2.1-go.5 · M2-a — 2026-11-02

**会话 v4 解码器：先拿真实语料验，再写代码。**

- `codecs/v4/path.go`：`ProjectKey` / `EncodeSegment` 逐字实现 AGENTS §4 的转义规则
- `codecs/v4/frame.go`：**只解帧结构不解压**地切帧（FHD/FCS/DictID/块头），因为「首帧只装 header」这条契约需要精确的帧边界
- `codecs/v4/read.go`：`ReadFile` / `Read`
  - header 键**白名单**，多一个键即报错
  - 首帧必须恰好一行且以 `
` 结尾，否则拒
  - 版本必须是 4
- 依赖 `github.com/klauspost/compress`（纯 Go zstd；cgo 绑定在 CI 上要 C 工具链）

**实测**
```
TestReadRealCorpus: 116 个文件 / 66,877 帧 / 123,790 事件 —— 全部解析通过
TestProjectKeyMatchesRealDirectories: 4 个真实目录名逐字复现
```

**注意**：语料数字两次运行略有变化（66,821 → 66,877 帧），因为 DSH 正在写当前会话——
这反过来证明解码器能吃「正在被追加的文件」。

**本机基线（同日实测）**：二进制 `-s -w` 后 **3.7 MB**；`dshd version`（装配 9 模块）冷启动 median **35.2 ms**。
对照 Go 空进程 18.1 ms —— 装配本身约 17 ms，还在预算内（目标 <100 ms）。

---
## 0.2.1-go.5 · M1 收口 — 2026-11-02

**工具表有了快照与预算：R4 落到测试里，token 有了实测数字。**

- `spi.Tool` 增加 `Description()`：模型靠它决定「要不要用这个工具」，属稳定性契约
- `dshd tools`：打印模型可见的工具表（name + description + parameters），按名字排序、字节稳定
- `cmd/dshd/testdata/tools.snapshot.json`：9 个工具的工具表快照
- 三条测试守着 R4：快照一致、两次装配字节相同、每个工具都有 ≤120 字的描述
- `docs/tool-budget.md`：实测 **9 工具 / 3,760 字节 / 906 token**（对照 DSH 的 61 / 54,936 / 12,209）

**实测方法**：紧凑 JSON 作为单条 user 消息发给本机网关，读 `usage.prompt_tokens`。

---
## 0.2.1-go.5 · M1-d — 2026-11-02

**shell + 后台任务：真实 bash 进程（R6 的第一步落地）。**

- `modules/exec/bash`
  - 常驻 bash 进程（`--noprofile --norc -s`），**cwd 跨命令保留**
  - 结束哨兵 `DSHD:<seq>:<rc><cwd>`；输出与退出码从同一条流里解析（stdout/stderr 合流，M1 取舍）
  - **自动转后台**：闲置超过 `idle_seconds`（默认 600s）就把整个会话交给后台任务，下次调用自动换新会话
  - **会话自愈**：命令把 shell 打死（如裸 `exit`）时报错并重建，不伪装成功
- `shell` / `job_output` / `job_kill` / `job_list` 四个工具（`exec` 模块提供 `exec` 与 `jobs` 两个服务）
- 大输出不需要工具自己截断：流水线的截断层会把 >4000 字符的结果外置成 artifact

**实测（CLI）**
```
dshd exec --tool shell --args '{"command":"echo hello && pwd"}'
→ 退出码 0，输出 hello / D:/DeepSeek Harness/default-workspace/DSH-GO
```

**已知取舍**：`export` 不跨命令保留；`job_kill` 只杀 bash 进程，其子进程可能存活。

---
## 0.2.1-go.5 · M1-c — 2026-11-02

**glob / grep：独立工具，不是 shell 的包装。**

- `internal/fsutil`：共享遍历规则（跳过 `.git` `node_modules` `refs` `dist` `bin` `tmp`）+ 二进制判定
- `tool-glob`：`**` 跨目录、`*`/`?`/字符类（单段 `path.Match`）；结果排序稳定、上限 200（可到 1000）、截断给 Hint
  - **刻意不支持花括号展开**：少一种隐式语法，少一类"为什么没匹配上"
- `tool-grep`：Go 正则、`include` 文件过滤、`ignore_case`、`limit` 100（可到 500）
  - 只回命中行（带行号），不回上下文噪声；单文件 >2 MiB 与二进制跳过并计数
  - 返回 `ViewSpec=table`（文件/行/内容），客户端直接渲染成表

**实测（CLI）**
```
glob internal/**/*.go            → 10 个文件（命中上限，truncated=true）
grep "func Assemble" include=**/*.go → internal/kernel/assemble.go:22（扫描 41 文件）
```

---
## 0.2.1-go.5 · M1-b — 2026-11-02

**文件组（read / write / edit）与读后写观测策略。**

- `modules/fs/local`：`spi.FS` 默认实现
  - 路径归一：统一正斜杠、相对工作区根；`..` 与工作区外绝对路径一律拒绝
  - **读后写**：文件存在时必须先 `read`；读过之后文件被改过要重读（stat 先于读，宁可多读一次）
  - 新建文件不需要先读；自己写的文件视为已观测
- `spi/keys.go`：服务键集中定义（模块间只经 spi 相遇，不互相 import）
- `tool-read` 迁到 `spi.FS`（读即登记观测）
- `tool-write`：新建/覆盖；覆盖前过读后写策略
- `tool-edit`：逐字替换；**默认要求唯一匹配**，多处必须显式 `replace_all`；空 old_string、无变化编辑一律拒绝
- `dsh.toml` 增加 `fs` / `tool-write` / `tool-edit`

**实测（CLI 跨进程）**
```
dshd exec write 新文件      → 新建 tmp/rbw.txt（4 字节）
dshd exec write 覆盖未读     → 还没读过 tmp/rbw.txt；先 read 它再改（读后写策略：不做盲改）
```

---
## 0.2.1-go.5 · M1-a — 2026-11-02

**工具流水线（三条硬约束）与结果投影。**

- `modules/tools/pipeline.go`：唯一执行入口 `Runner`
  - 四槽：`tools/pre-execute`（waterfall，可 veto）→ `execute` → `tools/post-execute`（可改写）→ `tools/result`（广播）
  - **序列化层**：`Data` 必须是合法 JSON，否则转成带 Hint 的错误
  - **截断层**：超 4000 字符外置成 artifact + 指针；替换体 ≤ 阈值 1/4（防「取回又被外置」死循环）；落盘失败保留内联，绝不把成功变失败
  - **提示层**：失败没给 Hint 就兜底并记账（`Violations()`），日志告警
- `internal/kernel.OnTyped[T]`：载荷类型化订阅，类型不符返回明确错误而不是静默跳过
- `internal/session/project.go`：`Result → content[]` 投影（摘要→数据→截断→提示，顺序固定、同输入同字节）+ `Flatten`
- 模块 `tools` 同时提供 `tools` 与 `toolrunner` 两个服务：只给目录不给 Runner 等于允许绕过全部中间件
- CLI `dshd exec` 改走 Runner，端到端覆盖中间件

**实测**：43 个测试全绿；门禁 15 项 ALL GREEN；`dshd exec --tool read` 经流水线返回结构化结果。

---
## 0.2.1-go.5 · M0 — 2026-11-02

**内核四件事 + 工具 SPI + 装配器，门禁全绿。**

- `internal/kernel`
  - `Scope`：`Effect` / 级联撤销 / `-race` 安全的锁序 / disposer panic 隔离
  - `Provide` / `Inject`：泛型键、**部署级命名空间**（一个能力一个提供者）、缺能力 `ErrMissingService` fail-loud
  - `Bus`：emit / serial / bail / waterfall / parallel，五模式齐备，监听器 panic 一律转错误
  - `Assemble`：能力解析 → 拓扑排序 → 逐模块实例化 → **失败回滚**
- `internal/spi/tool.go`：`Tool` / `Result`（Summary·Data·Artifacts·Truncated·Hint·View）/ `ViewSpec`（card·table·plain）/ `ToolRegistry`
- `modules/tools`：工具目录（重名即错、顺序稳定）
- `modules/tool/read`：第一个真实工具，结构化 `Data` + 截断 `Hint` + plain `View`
- `dsh.toml`：按 id 命中、`enabled` 过滤、config **整块替换**、未知 id 报错
- CLI：`dshd modules graph|list`、`dshd exec --tool --args`、`version`
- 门禁 `scripts/check.sh`：build / vet / test / R1 / A3 / 文档 / 凭据，9 项 ALL GREEN

**实测**
- 禁用提供者模块 → `模块 "tool-read" 需要能力 "tools"，但没有模块提供它`
- 未知模块 id → `模块 "ghost" 没有编译进二进制；可用模块: [tool-read tools]`
- `dshd exec --tool read` → 结构化 Data + Hint + View

**未验证**：`-race`（本机 `CGO_ENABLED=0`、无 gcc；CI 上待验）

---

## 0.2.1-go.5 · 初始化 — 2026-11-02

- 方案：`PLAN.md` v1.0（十项方向已拍板、架构、M0–M5、CI 七条）
- 手册：`AGENTS.md`（六条红线、代码地图、会话 v4 字节级清单）
- 调研（6 份，1,424 行，全部带 `文件:行号`）：
  - `research/10-dsc-reference.md`、`research/11-dsc-reuse.md`
  - `research/20-dsh-tool-schemas.{json,index.md}`（61 工具 / 12,209 token 实测）
  - `research/21-dsh-session-v4.md`（62 事件全表 + zstd 多帧物理格式）
  - `research/22-dsh-acp.md`（28 个 method + 15 个 update 变体）
  - `research/23-dsh-plugin-surface.md`（96 条 bundle entry + 104 个 service key）
  - `research/30-go-env-probe.md`（11 个依赖实测；go-plugin/wazero 端到端跑通）
- 审计：`evidence/`（Node 绑定面、dsh↔dsc 覆盖、59 个真实工具名）
- 维护：`MAINTENANCE.md` / `CHECKLIST.md` / `UPDATING.md` / `scripts/check.sh` / CI
- 结论：M0 未开始；下一步 `go mod init dshgo` + `internal/kernel` + `internal/spi/tool.go`
