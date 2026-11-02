## 0.2.1-go.21（未发布）· 续 2

**坏掉的 tool_call：参数不是 JSON 时，执行前就拦。**

三种形态里两种本来就对（`unknown tool "x"`、`file_path 为空`，都带 Hint）。
第三种原来是**直接透传**：

- 多数工具自己 Unmarshal 会失败 → 报错；
- **不校验参数的工具会拿着垃圾照跑**。

改在流水线入口拦一次：

```
执行失败: 参数不是 JSON 对象: json: cannot unmarshal string into Go value of type map[string]jsontext.Value
提示: tool_call.arguments 必须是 JSON 对象（键值对）；常见原因是引号没转义或写成了字符串
```

同时确认坏调用**不会把 turn 搞崩**：三条坏调用之后模型仍能拿到结果继续，
每条 tool 消息都有正文可读。

### 顺带修掉一个测试的假阳性

`TestSurfaceRealCorpus` 变红，报「有 1 个替身来源却没有任何可见摘要」。
查下去：那条消息的正文在**讨论** `compact-checkpoint` 协议（第 10,340 字符处），
`source.kind` 是 `plugin:hindsight`。测试用**子串**判断替身，把"我在说明这个协议"
误算成"这是一份压缩替身"。改成解析 `source.kind` 后通过。

`git stash` 验证过：这不是本次改动引起的——**真实语料每天在长，测试会随新数据变红**。

---
## 0.2.1-go.21（未发布）· 续

**尾部半帧不再泼掉整份历史。**

断电/磁盘满会留下半帧。原来读整份日志直接失败：

```
...session.v4.jsonl.zstd: 偏移 458 处块头被截断
```

**为了最后半帧丢掉前面所有事件，是最坏的结果。** 而这条行为当初是故意的
（`TestCorruptTailIsReported`：「截断的文件必须报错，而不是静默少读几条」）——
担心把坏文件当完整文件用。

两个目标可以同时满足：**留住前面的，并且大声说出来**。

```
事件: 4（5 帧）
⚠ 尾部截断: 偏移 6474 之后读不动了（上面的内容照常可用）
```

- `splitFrames` 对"字节不够"返回 `(frames, truncatedAt, nil)`；
  **真损坏**（魔数不对、保留块类型）仍然报错
- `v4.Log.TailTruncatedAt` 把位置带给调用方；`session info` 打警告
- `TestCorruptTailIsReported` 从"必须报错"改成"必须返回前缀 + 必须报出截断"

顺带验了一件事：两个宿主同时追加**不丢帧**（`O_APPEND` + 单次 write），
而且编码器里没有"失败回滚到原长度"的逻辑，不存在"A 回滚截掉 B 的帧"的风险。

---
## 0.2.1-go.21（未发布）

**边角盘点：四类验过都对，一个假警报。**

| 情况 | 结果 |
|---|---|
| 崩溃恢复（`kill -9` 跑到一半） | 日志可读：5 事件 / 6 帧 / surface 3 节点 ✅ |
| 同会话并发 prompt | 一条被拒（`该会话已有在飞的 prompt`），另一条跑完 ✅ |
| 超长单行（1MB 一行） | 走外置：`spilled:true` + artifact 指针 + 头尾预览 ✅ |
| Windows junction（`mklink /J`） | 遍历不跟进，只报 1 个文件 ✅ |

**没有一条需要改代码。**

### 一个假警报

`ln -s ..` 之后 `glob` 报出三个 `a.txt`，看着像"遍历跟了符号链接"。
查下去发现 **Git Bash 的 `ln -s` 在没权限时静默降级成复制**——磁盘上真有三份。
换成真 junction 后，`os.Lstat` 报的 mode 是 `?rw-rw-rw-`（非普通文件非目录），
我们的遍历本来就只收 `IsRegular()` 的条目，本来就跳过。

教训记进 `docs/edge-cases.md`：**先确认测试夹具造出来的东西是什么**，
否则会往代码里加一段永远不生效的"修复"。

### 还没打的

磁盘满 / 只读、会话文件被截断、极深目录（Windows MAX_PATH）、
同一 workspace 两个宿主进程、模型输出非法 tool_call JSON。

---
## 0.2.1-go.20（2026-11-02）

**审批框与 ACP 审批请求：让用户知道自己在批什么。**

这一版修的不是"模型看不懂"，是"**人不该被误导**"。

### 终端里的审批框

```
需要审批：bash
       命令: rm -rf /tmp/build
       模型自述（仅供参考，不是依据）: 看看临时目录
       → 允许？[y/N]
```

三条规矩：

1. **命令原样、排第一**——眼睛先落到的必须是它；
2. 模型的 `description` 单独一行，标成"仅供参考，不是依据"；
3. **长命令截中间不截尾巴**——原来 `truncate(args,160)` 截的是尾巴，
   一条 200 字符的命令正好把 `rm -rf /` 截没。

`write` / `edit` 改成显示路径与字节数，不再把正文塞进终端。

### ACP 的 `session/request_permission`

原来 `title` 只有工具名：

```json
{"toolCall": {"title": "bash", "kind": "other", "rawInput": {...}}}
```

客户端只显示 title 的话，用户看到的就是"bash"两个字。现在：

```
title: bash: echo approved
kind : execute
content: [{"content":{"text":"命令: echo approved
模型自述（仅供参考，不是依据）: 输出 approved"}}]
```

- `title` 一行摘要、命令在前；多行命令压成单行
- `kind` 按工具映射 `execute` / `edit` / `other`
- `content` 带完整说明；`rawInput` 原样保留

新增 7 条测试（`TestDescribeApproval*` 4 条、`TestApprovalTitle*` 3 条）。

### 实测量

| 指标 | 目标 | 实测 |
|---|---:|---:|
| 常驻内存 | <150 MB | **15.9 MB** |
| 冷启动（紧探针 p50） | <100 ms | **28.8 ms** |
| 工具表 | ≤6k token | **1,418 token** |
| 二进制 | ≤25 MB | **8.4 MB** |

---

### 续

**ACP 的审批请求也带上命令了。**

终端里的审批框上一片已经改过，但走 ACP 的客户端是另一条路。原来它发的是：

```json
{"toolCall": {"title": "bash", "kind": "other", "rawInput": {...}}}
```

`title` 只有工具名——**客户端只显示 title 的话，用户看到的就是"bash"两个字**。

改完（真机抓到的原文）：

```
title: bash: echo approved
kind : execute
content: [{"content":{"text":"命令: echo approved
模型自述（仅供参考，不是依据）: 输出 approved"}}]
```

- `title` 一行摘要、命令在前；多行命令压成单行（`
` → ` ; `）
- `kind` 按工具映射成 `execute` / `edit` / `other`
- `content` 带完整说明与"模型自述"标注；`rawInput` 原样保留

新增 `TestApprovalTitleLeadsWithCommand` / `TestApprovalTitleSingleLine` / `TestApprovalKind`。

---
## 0.2.1-go.20（未发布）

**审批框：先给命令，再给"模型自述"，并且标明后者不可信。**

`bash` 的 `description` 参数原来被完全忽略。查下来真正的问题不是"要不要显示"，
是**顺序与标注**——事故形态是模型把 `rm -rf /tmp/x` 描述成"看看临时目录"，
用户点 y 就变成了批准一句话而不是一条命令。

```
需要审批：bash
       命令: rm -rf /tmp/build
       模型自述（仅供参考，不是依据）: 看看临时目录
       → 允许？[y/N]
```

- 命令原样、排第一
- 模型的 description 单独一行并标注"仅供参考，不是依据"
- **长命令截中间不截尾巴**——危险的部分常在结尾（`rm -rf /`、`> /dev/sda`）
- `write` / `edit` 显示路径与字节数，不再把正文塞进终端

新增 `TestDescribeApprovalShowsCommandNotClaim` / `KeepsCommandTail` /
`WriteShowsPathAndSize` / `FallsBackToRawArgs`。

---
## 0.2.1-go.19（2026-11-02）

**协议层不再把宿主的实现细节甩给客户端。**

两个都来自"拿真请求去打边角"。

### `session/load` / `session/resume` 漏宿主路径

```
修前: 载入失败: 读会话失败: 读会话文件失败:
      open D:\...\sessions\--D-...--
ope\session.v4.jsonl.zstd: The system cannot find the file specified
修后: 载入失败: 找不到会话 nope
```

三层嵌套 + 绝对路径。`sessionErrText` 只把 `fs.ErrNotExist` 认成"找不到会话"，
其它错误原样透出——不能把"真出错了"也说成"没有这个会话"。

### 模型调用失败把 Go 的错误长什么样都告诉客户端了

```
修前: 请求模型失败(http://127.0.0.1:1/v1/chat/completions):
      Post "http://...": dial tcp 127.0.0.1:1: connectex: No connection could be made ...
修后: 连不上模型网关 http://127.0.0.1:1/v1：确认网关在跑、--base 地址对
```

`internal/llm.GatewayError` 按种类分开说：

| 情况 | 客户端看到 |
|---|---|
| 连不上 | `连不上模型网关 <base>：确认网关在跑、--base 地址对` |
| 401/403 | `模型网关拒绝认证（401）：检查 API key 与 --base` |
| 404 | `模型网关没有这个接口（404）：--base 是不是少了 /v1？` |
| 429 | `模型网关限流（429）：稍等再试` |
| 5xx | `模型网关出错（500）` |
| 其它 4xx | `模型网关返回 400：<正文最多 200 字>` |

- 原始错误用 `%w` 留着（日志、`errors.Is` 还能用），不进 `Error()`
- 网关正文原来整段 8KB 塞给客户端，现在截到 300 字符
- API key 实测从未泄漏

### 顺带确认本来就对的

`set_mode` 未知模式会列出可选值（`default, read-only`）、`delete` 不存在会说清在哪个 home 下、
`set_mode` 合法返回生效后的 `modeId`。

### 实测量

| 指标 | 目标 | 实测 |
|---|---:|---:|
| 常驻内存 | <150 MB | **15.8 MB** |
| 冷启动（紧探针 p50） | <100 ms | **33.0 ms** |
| 工具表 | ≤6k token | **1,418 token** |
| 二进制 | ≤25 MB | **8.3 MB** |

---

### 续

**模型调用失败时，客户端不再看到 Go 的错误长什么样。**

```
修前: 第 1 步模型调用失败: 请求模型失败(http://127.0.0.1:1/v1/chat/completions):
      Post "http://...": dial tcp 127.0.0.1:1: connectex: No connection could be made ...
修后: 第 1 步模型调用失败: 连不上模型网关 http://127.0.0.1:1/v1：确认网关在跑、--base 地址对
```

`internal/llm.GatewayError` 按种类分开说：

| 情况 | 客户端看到 |
|---|---|
| 连不上 | `连不上模型网关 <base>：确认网关在跑、--base 地址对` |
| 401/403 | `模型网关拒绝认证（401）：检查 API key 与 --base` |
| 404 | `模型网关没有这个接口（404）：--base 是不是少了 /v1？` |
| 429 | `模型网关限流（429）：稍等再试` |
| 5xx | `模型网关出错（500）` |
| 其它 4xx | `模型网关返回 400：<正文最多 200 字>` |

- 原始错误用 `%w` 留着（日志与 `errors.Is` 还能用），不进 `Error()`
- 网关正文原来整段 8KB 塞给客户端，现在截到 300 字符
- API key 实测从未泄漏

新增 `TestGatewayErrorHidesInternals` / `TestGatewayErrorByStatus` / `TestGatewayErrorTruncatesBody`，
并改掉 `TestStreamHTTPError`（401 现在说的"认证"而不是把网关正文抛出来）。

---
## 0.2.1-go.19（未发布）

**`session/load` / `session/resume` 不再把宿主路径漏给客户端。**

打 ACP 边角时看到的：

```
load 不存在 → 载入失败: 读会话失败: 读会话文件失败:
              open D:\...\sessions\--D-...--
ope\session.v4.jsonl.zstd: The system cannot find the file specified
```

三层嵌套 + 宿主绝对路径。路径是实现细节，客户端要的是一句"没有这个会话"：

```
load 不存在   → 载入失败: 找不到会话 nope
resume 不存在 → 恢复失败: 找不到会话 nope
```

`sessionErrText` 只把 `fs.ErrNotExist` 认成"找不到会话"，其它错误原样透出——
不能把"真出错了"也说成"没有这个会话"。

顺带确认了三个本来就对的边界：
`set_mode` 未知模式会列出可选值（`default, read-only`）、`delete` 不存在会说清在哪个 home 下、
`set_mode` 合法返回生效后的 `modeId`。

---
## 0.2.1-go.18（2026-11-02）

**长会话的成本与语义：三个 bug、一个声明机制、三处措辞。**

### 压缩每轮都跑，但当轮白压

12 轮实测 `used` 线性增长到 25,585 甚至 64,080，而 surface 只有 9,537。根因是
`hist` 在压缩之前取的快照——压缩花了一次模型调用，当轮却仍用未压缩的历史。

修完（同脚本同参数）：轮 11 从 25,585 → **11,285**，轮 12 从 64,080 → **9,468**。

### 保留策略按条数是错的

`KeepRecent = 8` 在"一条工具输出 2,306 token"的会话里能顶到 8k。改成
`KeepTokens = 2000` 为真正的保留量、`KeepRecent` 降为下限 2：同一个 12 轮脚本
**6,995**，而且这次 12 轮根本没超阈值——**一次压缩都没触发**。

### `.dshdignore`

仓库自己声明"别扫我"：

```gitignore
refs
research
evidence
```

`glob`/`grep` 认它，摘要里把"噪声目录"和"声明目录"分开报。
实测：`bash grep -r` 6 次调用 / 28,541 token vs `grep` 工具 1 次 / **8,584** token（3.3×）。
**ignore 对 bash 无效**（真实 shell 不该被宿主改写），这点也写进文档。

### 三处措辞：把两件事说成一件的那些

| 原来 | 现在 |
|---|---|
| `job_output` 空 id → `没有任务 ""` | `job_id 为空` + 提示先 `job_list` |
| `glob` 0 命中 → 只说"没有" | 区分"真的没有"与"被 .dshdignore 挡住" |
| `edit` 不唯一 → 只验文案 | 两条出路（加上下文 / replace_all）实测都能走通 |

### 测量工具也要校准

`measure-start.py` 报 55–115 ms 跳变；换紧探针背靠背比：go.17 **31.7 ms** vs go.18 **32.5 ms**
——没有回归，之前那个 "115 ms" 是探针开销加机器负载。

### 实测量

| 指标 | 目标 | 实测 |
|---|---:|---:|
| 常驻内存 | <150 MB | **15.5 MB** |
| 冷启动（紧探针 p50） | <100 ms | **32.5 ms** |
| 工具表 | ≤6k token | **1,418 token** |
| 二进制 | ≤25 MB | **8.3 MB** |

---

### 续 7

**验证 `edit` 的「不唯一」恢复路径真的能走通。**

报错文案早对齐了，但"照着做能不能成"从没验过。`TestEditAmbiguousHintIsActionable`
把 hint 里给的两条出路各走一遍：

| 照做 | 结果 |
|---|---|
| 加更多上下文 | ✅ 只改第一处 |
| `replace_all: true` | ✅ 两处都改 |

真机观察（让它只改第一个 `TODO`）：模型第 1 步先 `read`，然后自己用上下文拼出唯一片段，
一次成功——**没触发歧义分支**。它还在回答里解释了这么做的原因。
`tool_calls=3 input=7038`。

结论：这条错误路径的正确性靠测试保证，触发率低说明"先读再改"的规则在起作用。

---
### 续 6

**`glob`/`grep` 的「0 命中」要说清是哪种 0。**

```
$ glob {"pattern":"**/*.lua"}
0 个文件（扫过 157），.dshdignore 挡掉 2 个目录
提示: 0 个匹配，但有 2 个目录被 .dshdignore 挡掉了；要搜就把它从 .dshdignore 里去掉，或者用 bash 直接找
```

- `glob` 的摘要现在报「扫过 N」「挡掉 N 个目录」
- `grep` 的 0 命中分支也补上同样的提示
- 没有 `.dshdignore` 时措辞不变（不能因为加了分支就让老路径变味）

理由：「真的没有」和「被声明挡住」的下一步动作不同——前者改 pattern，后者改声明或换工具。
和上一轮的「参数缺失 vs 目标不存在」是同一类错误。

新增 `TestGlobZeroMatchMentionsIgnore`、`TestGrepZeroHitMentionsIgnore`。

---
### 续 5

**`job_output` / `job_kill` 把"你没给参数"报成了"任务不存在"。**

```
$ dshd exec --tool job_output --args '{}'
执行失败: 没有任务 ""            ← 修前
执行失败: job_id 为空            ← 修后
```

模型看到"任务不存在"会去 `job_list` 找一个根本不存在的 id——**方向是错的**。
其他六个工具早就区分开了（`read`/`write`/`edit` 报 `file_path 为空`，
`glob`/`grep` 报 `pattern 为空`，`bash` 报 `command 为空`），只有这两个漏了。

**两类错误的下一步动作不同，就不能用同一句话。**

新增 `TestJobToolsRejectEmptyID`：空 id 不许出现「没有任务」，未知 id 必须出现「没有任务」。

---
### 续 4

**体积账：我们自己的代码只占 0.29 MB / 8.7 MB。**

`go tool nm -size` 按符号归桶：

| 桶 | 大小 |
|---|---:|
| runtime（pclntab + 类型元数据） | 3.07 MB |
| 标准库散件 | 3.01 MB |
| crypto/tls + x509 | 0.88 MB |
| net/http 栈 | 0.32 MB |
| **我们自己的代码** | **0.29 MB** |
| x/net + x/text | 0.27 MB |
| zstd | 0.25 MB |
| toml | 0.15 MB |

**决定不减**：能减的只有 TLS 那串（~1.2 MB ≈ 3 ms），代价是不支持 https——
那就只能连本机 magpie，接不了任何远程 provider。用 3 ms 换掉这个能力，不做。

同时量到：**装配 11 个模块花 0.5 ms**（`DSHD_TIMING=1`），
initialize 往返 33.1 ms 里 30 ms 是加载 8.7 MB 可执行文件。

冷启动这条线到底了。

---
### 续 3

**冷启动抖动定位：不是我们的代码，是二进制体积。**

用"同体积的空程序"做对照（25 次取 p50/max）：

| 二进制 | 大小 | p50 | max |
|---|---:|---:|---:|
| noop（空 main） | 1.2 MB | 11.6 | 14.6 |
| noop-big（空 main + 11MB embed） | 12.8 MB | 46.5 | 76.7 |
| dshd（未裁剪） | 12.6 MB | 41.8 | 63.8 |
| dshd（`-s -w`） | 8.8 MB | **31.7** | 65.7 |

- **抖动来自加载 12MB 可执行文件**（page-in + 杀软扫描），空程序一样抖
- **成本 ≈ 2.5 ms/MB**；`-s -w` 值 11 ms
- `GODEBUG=inittrace=1`：所有包的 init 合计 ≈ 4 ms，不是瓶颈
- 所以"ACP 就绪 57–61 ms" = 30 ms 加载 + ~27 ms 我们自己的装配与应答

详见 `docs/cold-start.md`。下一切片：**减体积**（每 MB ≈ 2.5 ms）。

---
### 续 2

**保留策略改成按 token 预算：`KeepRecent` 从"留几条"降级为"至少留几条"。**

上一轮修好"压缩当轮生效"之后，上下文仍在 11k 打转——因为按 8 条留，
而一条大工具输出的估算就是 2,306 token。**按条数留 ≈ 按体积留，是错的。**

- `Options.KeepTokens`（默认 2000）成为真正的保留量；`KeepRecent` 默认降到 2，只当下限
- `keepFrom()` 从末尾往前累计，装不下就切；ACP 与 `run` 在"超窗但预算内能留的等于全部"时
  退到 `KeepTokens: 1`（只按最小条数留）
- 新增 `TestKeepByTokenBudgetNotCount` / `TestKeepManySmallMessages`

实测（同一个 12 轮脚本）：

| 版本 | 轮 11 | 轮 12 | 触发 |
|---|---:|---:|---:|
| 修 hist 之前 | 25,585 | 64,080 | 10 |
| 修 hist 之后 | 11,285 | 9,468 | 10 |
| **按 token 预算** | 6,430 | **6,995** | 0 |

第三组"0 次触发"不是 bug：这组参数下 12 轮没超过 4000 阈值（每轮读被截成头+尾 ≈ 750 token）。
**能少压就少压**——每次压缩是一次额外的模型调用。
3 轮强制触发脚本仍正常（压 2 次，`COMPACT-KEY-7` 还在）。

---
### 续

**长会话实测挖出大 bug：压缩每轮都跑，但当轮白压。**

12 轮会话（`scripts/acp-long-session.mjs`，每轮读 150 行）第一次跑出来的 `used`：
4,305 → 6,397 → 8,500 → … → **25,585** → **64,080**，线性增长；而查 surface 只有 **9,537 token**。
两个数对不上。

根因在 ACP 的 prompt 处理：

```go
hist := r.hist[sessionID]        // 压缩前取快照
r.autoCompactNow(ctx, sessionID) // 把 r.hist 换成压缩后的历史
loop.Run(ctx, turn, append(hist, ...))  // 用的还是旧快照
```

每一轮：压缩跑一次（花一次模型调用）→ 当轮仍用未压缩的历史 → 下一轮再压。
12 轮压了 10 次，全部只对下一轮有效。

修完（同脚本同参数）：

| 轮 | 1 | 2 | 3 | … | 11 | 12 |
|---|---:|---:|---:|---:|---:|---:|
| 修前 | 4,305 | 6,397 | 8,500 | … | 25,585 | 64,080 |
| **修后** | 4,216 | 6,503 | 8,803 | … | **11,285** | **9,468** |

11k 见顶，不再线性增长；暗号两版都记得（修的是成本，不是正确性）。

**下一步（本轮不做）**：`KeepRecent` 按**条数**（8 条）留，而一条大工具输出就 2,300 token，
所以上下文只能停在 11k 打转。应该改成按 token 预算留。

---
## 0.2.1-go.18（未发布）

**`.dshdignore`：仓库自己声明"别扫我"，glob/grep 认它。**

```gitignore
refs
research
evidence
```

```
. 下 /func TestMain/ 命中 1 处（扫描 154 文件，跳过 1 个噪声目录 + 2 个 .dshdignore 目录）
```

- 新增 `internal/fsutil/ignore.go`：前缀 + glob（`*` 不跨目录、`**` 跨目录），不支持 `!` 否定
- `WalkFilesWith` 在目录层就剪枝（不必进去数文件），并把"被忽略的目录数"记下来
- `grep` 的摘要把两类跳过**分开说**——混成一个数字，模型没法判断是哪一层挡掉的
- 文件不存在不是错误；空规则等于没配

### 但实测：同一件事，选哪个工具差 3.3 倍

| 走法 | 调用 | input token |
|---|---:|---:|
| 模型写 `bash grep -r .` | 6 | 28,541 |
| 模型用 `grep` 工具 | **1** | **8,584** |

`.dshdignore` 对 bash 里的命令无效（真实 shell 不该被宿主改写）。
所以它保住的是"用我们工具时不误扫"，不是"模型一定会用我们的工具"。

**新增测试**：4 个（缺失文件不报错、注释/空行/斜杠归一、`**` 命中目录及子项、遍历真的剪枝）。

---
## 0.2.1-go.17（2026-11-02）

**看清了一次真实的"多调"：是仓库里的歧义，不是工具设计问题。**

任务"这个仓库有多少个 Go 测试函数"：模型第 1 次 `bash grep -r` 得到 **9,852**，
第 3 次自己加上 `--exclude-dir=refs` 得到 **252**（正确值）。
`refs/` 有 3,569 个文件、9,600 个 vendored 测试函数，把答案放大了 **39 倍**。
整轮 6 次调用 / 30,795 token，而正确答案本可以在 1 次调用里拿到。

**试过但没用的修法**：把 `grep`/`glob` 描述改成明说"自动跳过 .git/refs/node_modules，
比 bash grep -r 准"——同一个任务仍然 6 次调用 / 30,795 token。
描述仍然保留（更准确），但不宣称它有用。

### 基线读数

| 任务 | tool_calls | input token |
|---|---:|---:|
| 不需要工具（两题） | 0 | 4,205–4,206 |
| 需要 1 次工具（三题） | 1 | 8,510–8,614 |
| 歧义任务 | 6 | 30,795 |

- 基座 ≈ **4,200 token**（系统提示 + 工具表 + 用户消息）
- 第一次工具调用 ≈ **+4,300 token**
- 该用工具的任务没有多调 → **不存在"过度用工具"这个通病**

### 顺带修的

`stats` 原来打在成功分支，**恰好把最贵的跑法（步数用满）排除在统计外**。
改成 `defer`，失败的那一轮也有账。

---
## 0.2.1-go.17（未发布）

**压缩触发点实测 + `run` 打一行账。**

`--auto-compact` 四档各跑一次（3 轮对话 + 暗号 `COMPACT-KEY-7`）：

| 阈值 | 触发次数 | 压完还记得暗号 |
|---:|---:|---|
| 1 | 2 | ✅ |
| 4,000 | 1 | ✅ |
| 12,000 | 0 | ✅ |
| 40,000（默认） | 0 | ✅ |

- 3 轮会话到不了 12,000 → 默认值在短会话里等于关闭。这**不是 bug**：
  压缩的意义在长会话，短会话压了只会丢信息。
- 压了也不丢关键信息（4/4），包括连压 2 次那档。

`dshd run` 结束时多一行：

```
stats: tool_calls=2 input=13004 output=434
```

`tool_calls` 放最前是故意的——它是这一轮最贵的一项（每次 5–6k token）。

---
## 0.2.1-go.16（2026-11-02）

**截断说明从"中间省略 N 字符"改成可以直接执行的 offset/limit。**

```
…（第 102–1172 行已省略；取回：read offset=102 limit=1071；
   或从 tmp/omit2/artifacts/spill-0001-….json 取全文）…
```

- 认得 `N: 内容` 这种渲染格式，从头部最后一行号 + 尾部第一行号推出省略区间
- 没有行号的输出（`bash` 原始输出）不瞎编行号，退回字符数 + 指针
- 真机验证：模型复述了 102–1172 并主动提议补读

**为什么值**：一次工具调用的固定开销 5–6k token（`docs/token-budget.md` §6），
把"猜区间再试探"变成"一次精确调用"，省的就是这一次。

新增测试：`TestOmittedNoticeGivesExactOffsets`、`TestOmittedNoticeFallsBackWithoutLineNumbers`。

---
### 续 2

**工具结果不再被 JSON 转义：`bash` 直接给原始输出。**

模型原来看到的是 `Summary` + `JSON(Data)`：

```
退出码 0，输出 10691 字节
{"bytes":11068,"preview":"{\"output\":\"第 1 行…\"}"}   ← 双层转义
（已截断：保留 999/11068 字节，全文在 …）
提示：结果已外置到 …                                      ← 重复
```

改法三处：

1. `bash` 给 `Result.Text` = `[exit N] [cwd X]` + 原始输出（不转义）
2. 外置时 `Data` 只留 `{spilled, bytes, artifact}`，**去掉 preview**
   （正文由 Text 承担，预览是同一段内容的第二份拷贝，而且多一层转义）
3. 正文已带指针时不再补同义 Hint

**新增测试**：`TestShellViewIsRawTextNotJSON`、`TestSpilledShellViewStaysRaw`。

**顺带修的测试**：`TestLargeDataIsSpilled` 用了 `MaxChars: 64`，可一个指针体
（含 artifact 路径）就 116 字节——那条"替换体不许超阈值"的规则在 64 字节下物理上不可能满足。
改成 400，并写了注释。

---
### 续

**阈值 A/B：调低阈值没有实测收益；真正的变量是"模型调几次工具"。**

同一道题（17 KB 日志，NEEDLE 在第 386 行）四档阈值各跑 3 次：

| max-chars | 输入 token 中位 | 工具调用 | 答对 |
|---:|---:|---|---:|
| 2,000 | **8,645** | 3/1/1 | 3/3 |
| 4,000 | **8,669** | 1/2/1 | 3/3 |
| 8,000 | 13,292 | 2/2/1 | 3/3 |
| 50,000 | **8,653** | 2/1/1 | 3/3 |

- 四档全答对 → 头 + 尾 + 指针的组合够用
- 2,000 与 4,000 中位几乎相同 → 语料模拟里那 18 个点在这道题上没兑现
- **调 1 次工具 ≈ 8.6k token，调 3 次 ≈ 15k** —— 一次调用的固定开销比阈值大一个数量级

### 顺带修的：`bash` 不尊重 ctx 取消

取消一个 `sleep 20` 的调用，要**陪着等满 20 秒**才返回（e2e 里量到 19,954 ms）。
现在 `Exec` 用 select 等 ctx：取消立刻返回，命令可能仍在 shell 里跑 ——
那是诚实的"结果未知"，上层会把它报成 outcome is unknown 而不是失败。

```
PASS  cancel 是快的（<5s）  — 3 ms
```

另加 `--max-chars`（`run` 与 `acp` 都能配），方便做这类 A/B。

---
### 本版主线：截断层空转

**工具的截断层一直是空转的：只换了 Data，模型看到的 Text 原样全量。**

从真实语料量出来的账（116 会话 / 24,837 次调用）：

| 项 | 字符 | 占比 |
|---|---:|---:|
| 工具回显 | 35.0M | **57.4%** |
| 对话正文 | 26.1M | 42.6% |

`bash` 一个工具占工具回显的 **64.5%**；`p50` 只有 431 字符，大头全在尾部。
DSH 自己的上限约 **50,000 字符**（几乎等于不设限）。

### 两个 bug

1. **spill 只换 `Data`，不换 `Text`。** `ProjectResult` 优先用 `Text`，
   所以阈值 4,000 时模型仍会看到 9,361 字符——截断等于没做。
   现在 `Text` 也压：留头 + 留尾 + 中间一行指针（尾部常有关键信息）。
2. **指针指向模型读不到的地方。** 外置目录在系统 temp，而 `Hint` 写着「用 read 读全文」；
   `FS` 只放行工作区，那句话是假的。
   现在 artifact 落到 `<home>/artifacts`，`FS` 加 `WithExtraReadRoot`（**只放读**）。

顺带修掉 `run` 的模块装配错用 `defaultHome()`（artifact 地点与 FS 放行目录不一致）。

### 实测

```
 ← read [ok]
  …（中间省略 18067 字符，全文在 tmp/spill-home2/artifacts/spill-0001-…json）…
 全文在 `…/artifacts/spill-0001-…json`… 要我按段把它读全吗？
```

模型看到截断、知道去哪取、知道要分段取。详见 `docs/token-budget.md`。

---
## 0.2.1-go.15（2026-11-02）

**Martty 真客户端连上了，并抓到一个只有真客户端才会暴露的 bug。**

`martty@0.3.3` 装好后指向我们的 agent：

```
$ martty --agent …/dshd.exe --agent-arg acp --check-runtime
initialize ok in 0.1s → dsh-go-acp
```

真跑一轮（睡莲题），屏幕上出现了流式推理：

```
 推理：睡莲每天翻倍，所以"满池塘"前一天，必然正好盖住一半…
  1 turn · 0 step | TTFT avg 1.1s
 ↑4.2K · ↓105 | 3.4%/128K | 1 turn · 0 steps | Cache hit 0%
                             ╰· standard · Workspace Write   dsh-go-acp ·
```

—— `agent_thought_chunk` / `usage_update` / 模式 / Caps 标签全对上了。
**判据 ① 从此有真客户端证据，不再只是 SDK 脚本**（见 `docs/martty.md`）。

### 抓到的 bug：`~003F`

第一轮跑完，会话落到了 `--~003F-D-DeepSeek~0020Harness-…`。
`~003F` 是 `?` 的编码——Martty 传的 workspace 带着 Windows 扩展长度前缀 `\?\D:\…`，
我们原样拿去算 `projectKey` 了。

真实 DSH 永远不会产生这种目录名（116 个真实会话目录**零个**带 `~003F`），
所以后果是**两边互相看不见对方的会话**。

- 新增 `v4.NormalizeCwd`（去 `\?\` / `\.\`），`session/new` 收到 cwd 先归一化
- 修完目录变成 `--D-DeepSeek~0020Harness-default-workspace-DSH-GO--`

写测试时又踩了一次：反引号 `` `\?\` `` 看着像四个字符，实际三个
（Go raw string 不处理转义）。改用 `"\\?\\"` 才写对。

---
## 0.2.1-go.14（2026-11-02）

**edit 的 75 个失败：84% 来自读后写策略，而那条错误信息就是它自己的教程。**

```
 34  cannot edit "<p>": file changed since it was read — re-read the file, then retry
 29  cannot modify "<p>": file has not been read — read the file, then retry
 10  old_string was not found in "<p>"
  1  old_string matched 2 times in "<p>"; provide a more specific old_string or set replace_all to true
  1  unknown tool "edit"
```

我们原来写的是中文（`还没读过 a.txt；先 read 它再改`），意思对，但：
- 没有 `Error: ` 前缀（客户端识别不了）
- 没有动词（看不出是 write 还是 edit 被拦）
- 措辞和 DSH 不一致 → 对拍结论没法复用

现在逐字对齐（含破折号），并且动词跟着工具走：`write` 用 `cannot write`、
`edit` 用 `cannot edit`。`FS.CheckWritable(path, op)` 加了一个 op 参数，
只是为了措辞——策略本身没变。

**测试用同构比对**（把路径换成占位符再比）钉住这两句，防止以后"顺手改文案"改跑偏。

---
## 0.2.1-go.13（2026-11-02）

**bash 的 110 个失败样本：最常见的"bash 失败"其实不是 bash 失败。**

```
  84  Error: unknown tool "bash"
  13  The tool call was interrupted after it was recorded, but no result was durably recorded…
   5  Error: this destroys the runtime DSH would need in order to undo it…
   4  Error: tool call aborted
```

- **84/110 是"工具不在表里"**：工具集会随模式/配置变，模型手里的上下文可能还记着 `bash`。
  我们的措辞改成 DSH 的 `unknown tool "<name>"`，并提示"先看当前模式给了哪些工具"。
- **取消 ≠ 失败**：DSH 对"记录了但没收到结果"的调用写的是 *outcome is unknown*。
  我们原来把它当 `context.Canceled` 错误上报，措辞是"失败"——
  这会让模型以为"命令没执行"，从而重试一个已经生效的命令。
  现在单独处理成同一句话 + 引导先看现场（文件/进程/日志）再决定。

**新增测试**：`TestUnknownToolWording`、`TestCancelledToolIsUnknownNotFailed`。

---
## 0.2.1-go.12（2026-11-02）

**行为对拍：从 2.3 万个真实工具结果里挖出「失败长什么样」，照着重修了三个地方。**

真机语料：**23,407 个 tool/result，其中 367 个 isError=true**（1.6%）。失败文本一律
`Error: <具体原因>`，而且**原因写得很实**：

```
Error: offset 1080 is out of range for "…/eval.mjs" (1052 lines)
Error: cannot modify "…/PLAN.md": file has not been read — read the file, then retry
Error: cannot write "…/go.mod": file changed since it was read — re-read the file, then retry
Error: old_string matched 2 times in "…"; provide a more specific old_string or set replace_all to true
Error: grep search failed (exit 2): rg: <stderr>
```

对照之下我们有三个问题：

1. **`read` 的 offset 越过末尾时静默返回空内容** —— 模型会以为"这一段是空的"。
   一个错误答案比报错糟糕得多。现在报 `offset 99999 超出范围："README.md" 只有 38 行`。
2. **`grep` 搜不存在的路径返回"0 处命中"** —— 模型会以为"这个目录里没有这个符号"，
   真实情况是路径就错了。现在报错并提示先 glob 确认。
3. **工具失败时发给客户端的那条记录没有正文** —— 模型拿到的是 `工具失败：…`，
   但 ACP 的 `tool_call_update` 发的是工具原始 Result（Summary 空），
   界面上就是**一行红色、什么都不说**。现在两边是同一份文本，统一用 `Error: ` 前缀。

**新增测试**：`TestReadOffsetOutOfRange`、`TestGrepMissingPathErrors`、`TestToolErrorCarriesReasonToClient`。

**方法论**：这一轮没写一行"猜"的逻辑——全部结论来自真实语料里 367 个失败样本的形状。

---
## 0.2.1-go.11（2026-11-02）

**`session/list` 改成读磁盘：会话面板要的是"所有会话"，不是"这个进程开过的"。**

- `session.ListSessions(home, cwd)`：扫 `<home>/sessions/*/*/session.v4.jsonl.zstd`
  - `cwd` 非空时只算这个项目的目录（用 `projectKey` 换算，不扫全盘）
  - 每行带 `sessionId / cwd / agentPreset / parentSession / updatedAt / events / summary`
  - 按 `updatedAt` 倒序
- 摘要规则：**优先最后一条助手消息**，没有再退到第一条用户消息，80 字截断
- 读不动的会话**照样列出来**并带 `readable:false` + `error`——面板要能显示坏文件，不能假装它不存在
- 刚建、可能还没落盘的会话由 runner 补进列表，避免"刚建的在面板里看不见"

**实测**
```
全部会话: 1
 - session-31be4b64… preset=default events=5 摘要="LIST-ONE"
别的项目的会话数: 0
```

**一个刻意的取舍**：每份会话都要读整份日志才能算摘要与事件数。真实语料是 100MB 级，
所以设了 2MB 上限——超了只标"太大先跳过"，不为了面板好看去读 100MB。

---
## 0.2.1-go.10（2026-11-02）· 续 3

**客户端断连时的清理语义。**

以前 `Serve` 读到 stdin EOF 就直接返回，进程退出——在飞的 turn 没人取消，
会话日志也没有显式关闭（批窗口里压着的帧可能丢）。

现在：

1. **EOF → 取消所有在飞的 turn**（不取消它们会继续往没人读的管道里写）
2. **等它们收尾**（最多 3 秒）——取消的尾巴事件还要落盘
3. **`Agent.Shutdown()`**：把这个进程开过的会话日志全部 Close（flush + fsync）

**实测（真机，bash 正在 sleep 15 时直接断连）**
```
进程退出: code=0 用时 4 ms
会话: acp-acbb6c52…（事件 3，4 帧）
  system/message 1 · turn/start 1 · user/message 1
Node 读取成功：4 帧 / 4 行        ← 文件没撕裂
```

关键是那条 `user/message`：它是断连**前一刻**写进去的，本来还压在 200 ms 批窗口里。
`Shutdown()` 把它刷了出去——不然用户会看到"最后一句话不见了"。

**新增测试**：`TestEOFDisconnectsAndCancels`（关闭客户端写端 → Serve 必须干净返回，
不许被在飞的 turn 卡死）。

---
## 0.2.1-go.10（2026-11-02）· 续 2

**取消链路的回归：13 项 → 16 项。**

`session/cancel` 一直有实现、服务端单测也有，但**从没在真机上验过**。
取消是最容易出问题的一条：prompt 在飞、bash 正在 sleep、cancel 进来。

**实测**
```
PASS  cancel 让 prompt 以 cancelled 结束  — cancelled
PASS  cancel 是快的（<5s）             — 1 ms
PASS  取消后会话仍可用                  — end_turn
```

第三条是新加的 Go 测试（`TestSessionUsableAfterCancel`）：
**取消之后 busy 标志必须清掉**，否则下一次 prompt 会被
"该会话已有在飞的 prompt" 挡死——而这正是最容易漏的地方。

**一个语义要写清**：cancel 让 prompt **立刻**返回（1 ms），但 shell 里正在跑的
`sleep 20` 不会被强杀。宿主选择"先响应取消、不等工具收尾"，
因为让用户等 20 秒才收到 cancelled 更糟。

---
## 0.2.1-go.10（2026-11-02）· 续

**端到端回归：ACP 全方法 + 会话格式 + P0 四条，一次跑完。**

- `scripts/e2e-regression.mjs`：一个进程跑 `new/prompt/set_mode/load/fork`，
  第二个进程验 `resume`，第三个验 `delete`。**13 项全通过**。
- `docs/e2e-report.md` 记录结果，包括两个写错的断言。

**写回归时踩的两个坑（都值得记）**

1. **恒真断言**：先写成 `record('会话文件存在', true, …)`，永远 PASS——等于没检查。
   改成真的递归去找文件。
2. **delete 的断言写反**：断言"删完不该剩文件"，结果剩 1 个（fork 出来的子会话）。
   代码是对的，断言错了。改成"少了一个"。

**P0 四条（今天三次测量取中位）**

| 判据 | 目标 | 实测 | 余量 |
|---|---:|---:|---:|
| 内存峰值 | <150 MB | 15.3 MB | 9.8× |
| 冷启动 | <100 ms | 62.4 ms | 1.6× |
| 工具表 | ≤6k token | 1,401 token | 4.3× |
| 二进制 | ≤25 MB | 8.7 MB | 2.9× |

**冷启动波动**：同机三次测得 56.5 / 62.4 / 65.1 ms。波动来自机器负载。
基线只记"某次 median"，不是承诺值——判断回归要比同一天连续多次的中位。

---
## 0.2.1-go.10（2026-11-02）

**`session/new` 支持 `mode` 参数：建会话时就定模式。**

之前只能"先建会话、再 `set_mode`"。这中间有个**窗口**：会话以默认模式活着，
哪怕只有几毫秒——如果客户端在这之间就发了 prompt，模型拿到的是全权限工具表。

- `SessionNewRequest.Mode`（可选）
- 服务端在建会话**之前**解析并应用；未知模式当场 `-32602`，不会建出一个
  "标着只读、实际全权限"的会话

**实测**
```
session/new {mode: "read-only"} → 工具调用: []          ← 模型看不到 bash
会话: acp-fbe53a3b…（mode=read-only）                    ← header 里就是对的
session/new {mode: "turbo"}     → 未知模式被拒 ✅: 未知模式 "turbo"（可选：default, read-only）
```

**顺序很重要**：先定模式、再建文件。反过来就一定会有人写出"先建后切"的代码，
而那个窗口在生产里迟早被踩到。

---
## 0.2.1-go.9（2026-11-02）· 续 6

**`dshd run` 也有自动压缩了——两套行为收敛成一套。**

- `agent.Loop.Compact`：每步开始前的钩子，返回 nil 表示"这轮不用改"
  - 放在 Loop 里是因为：压缩是策略，但"什么时候压"只有跑 turn 的人知道（预算、会话文件在哪）
- `runCompactor`：读 log → Surface → 估算 → `compact.Select` → `Summarize` → `session.Compact` 落事件 → 从日志重建历史
  - 和 ACP 那条是同一套零件，区别只在触发时机（每步 vs 每轮）
- `dshd run --auto-compact <tokens>`（默认 40000）
- 顺带把 `run` 的 `agentPreset` 从写死的 `"standard"` 改成 `modes.DefaultID`

**实测**
```
自动压缩（第 5 步）：surface 10 → 8 节点
自动压缩（第 6 步）：surface 10 → 9 节点
（26 事件，压缩事件已落盘）
```

**踩到的一个坑**：`Summarize` 的第二个参数是**模型 id**，我一开始传了 `"run"`，
magpie 直接回 `knows no model "run"`。压缩失败被降级成一行 stderr 就继续跑了——
这个 fail-soft 是对的（压缩失败不该打断用户这一轮），但错误信息必须让人看见。

---
## 0.2.1-go.9（2026-11-02）· 续 5

**`history_search` 的三处接线 + 一个模式归属的修正。**

- `attachHistory(log)` 统一入口：`run`、`acp` 的 prompt、`acp` 的 resume 三处都调它。
  之前只有 ACP 的 prompt 路径指了会话，`run` 里回查会撞上"没有活动会话"；
  resume 之后也是同一个坑。
- **`read-only` 模式加上 `history_search`**：它只读日志，属于"看"的能力。
  以前从只读会话里连翻旧账都做不到——把只读做成了"什么都干不了"。

**实测**
```
# dshd run 里
"…这条命中就是**你刚才这句话本身**…"

# read-only 模式下
工具: ["history_search"]
回答: "查到 2 条命中，都是当前会话里的用户消息：
      - seq 2 — user/message（user）："记编号 RO-KEY-3。只回 ok。"
      - seq 6 — user…"
```

**一个反复出现的形状**：能力接进一条路径就宣布完成，忘了其它入口。
这轮把"指会话"收敛成一个函数，两个入口都调它，并加了测试钉住。

---
## 0.2.1-go.9（2026-11-02）· 续 4

**`history_search`：压缩把细节压掉了，这个工具把它找回来。**

日志是 append-only 的——摘要遮蔽只是**派生期**的事，原文永远在。
之前模型没有任何办法回查被遮蔽的内容，现在有了。

- `spi.HistorySearch` + `HistoryKey`：宿主提供的检索服务（动态指向"当前会话"）
- `modules/tool/history`：工具 `history_search {query, limit}`
  - 扫**全部事件**（含被遮蔽的），返回 `seq / type / role / excerpt`
  - `hidden` 字段标出这条是否已被压缩遮蔽——模型据此知道它不在表面上
- `dsh.toml` 加上 `history` 与 `tool-history` 两个模块

**实测（自动压缩遮蔽了原文之后）**
```
[agent] 自动压缩：遮蔽 2 条，摘要 199 字
本轮工具: ["history_search"]
回答片段: "完整编号是 **ARCHIVE-7F3**。
          （回查日志 seq 2 的原始用户消息确认，该条已被遮蔽但原文仍在。）"
```

模型不但找到了，还说清了它从哪来——`hidden` 这个字段起了作用。

**工具的 10 vs 9**：对拍门现在区分「核心 9 个必须与 DSH 同名」与「我们独有的扩展只报告」。
`history_search` 属于后者（DSH 的 61 个工具里没有它）。工具表 token **1,294 → 1,401**（+8%）。

---
## 0.2.1-go.9（2026-11-02）· 续 3

**模式跟着会话走：写进 header、resume 采纳、fork 继承。**

之前模式只在内存里。后果很实在：
- 一个 `read-only` 会话 **resume 之后会悄悄变成 `default`**——权限反而变大；
- 从 `read-only` fork 出来的子会话也是 `default`。

修法：用 v4 header 的 `agentPreset`（白名单键，不是自造字段）记模式。

- 建会话时写入当前模式
- `Resume` 从 header 采纳模式（认不出就回落 default，但会保留原值不覆盖）
- `Fork` 把**父会话当前生效的模式**写进子 header
- `session info` 现在也打印 `mode=...`
- 抽出 `ensureSession`（幂等建/开），让这条链路能被 Go 测试直接钉住

**三个测试**
```
TestEnsureSessionRecordsMode     模式写进 header.agentPreset
TestResumeAdoptsModeFromHeader   resume 后仍是 read-only
TestForkInheritsAgentPreset      fork 出来的子会话继承 read-only
```

**一个方法论上的教训**：这套验证我先用 node ACP 脚本写，连续失败四次
（`ACP connection closed`，真实原因一直没暴露出来）。最后换成 Go 测试，二十分钟搞定。
**状态断言该用 Go 测**；node 脚本留给"真的跑一轮对话"这种端到端。

---
## 0.2.1-go.9（2026-11-02）· 续 2

**模式决定审批倾向：工具可见 ≠ 工具能执行。**

之前 `Mode.Approval` 只是个字段，安全全靠"裁剪工具表"一层。现在两层分开：

- `approvalDecision(modeApproval, remembered) (allow, needAsk)`
  - 模式 `deny` → **连问都不问**，直接拒
  - 模式 `allow` → 不问，直接放行
  - 没策略/`ask` → 看记忆：记得就放行，不记得才问
- **顺序是有意的**：模式的 deny 压过记忆。模式是会话级策略（用户显式选的），
  记忆是单个工具的便利——策略不该被便利覆盖。

**实测（自定义模式 `no-exec`：工具全可见、approval=deny）**
```
模型请求的工具: ["bash"]
工具结果状态: ["failed"] | 审批请求次数: 0
PASS: deny 模式下没问、也没执行
```

注意模型**请求了** bash（工具是可见的），但执行被策略挡住，
而且**没有打扰用户**——"不许"和"要问"是两件事。

---
## 0.2.1-go.9（2026-11-02）· 续

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
## 0.2.1-go.9（2026-11-02）

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
