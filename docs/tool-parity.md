# 工具契约对拍（dsh-go ⟷ DSH）

> 数据来源：真实会话里的 `request/header.header.tools[]`——**这是模型真正看到的那张表**，
> 不是从文档抄的。判定标准刻意窄：只拦"会让模型调用失败"的差异。
> 复跑：`go test ./cmd/dshd -run TestToolParityWithDSH -v`

## 结论（2026-11-02）

| 我们的工具 | DSH 同名 | 必需参数 | 状态 |
|---|---|---|---|
| `read` | ✅ | `file_path` | **一致** |
| `glob` | ✅ | `pattern` | 超集（我们多 `limit`） |
| `grep` | ✅ | `pattern` | 超集（我们多 `ignore_case`/`limit`） |
| `write` | ✅ | `file_path, content` | **一致** |
| `edit` | ✅ | `file_path, old_string, new_string` | **一致** |
| `bash` | ✅ | `command, description` | 超集（我们多 `idle_seconds`） |
| `job_output` | ✅ | `job_id` | **一致** |
| `job_kill` | ✅ | `job_id` | **一致** |
| `job_list` | ✅ | — | **一致** |

"超集"是安全的：模型按 DSH 习惯传参，我们一定收得下；多出来的可选参数不用也不会出错。

## 对拍抓到的三处真实差异（已修）

1. **`shell` → `bash`**：DSH 的工具名是 `bash`。按 DSH 习惯写提示词的模型会直接喊 `bash`，
   我们原来叫 `shell`，那一刀会打空。
2. **`job_output` 的 `id`/`wait_seconds` → `job_id`/`wait`/`timeout_ms`**：
   参数名不同等于不兼容——模型照 DSH 传 `job_id`，我们会报"缺参数"。
3. **`bash` 缺 `description`**：DSH 把它标成 **required**。原来我们不认这个参数，
   模型每次都得传一个我们看不懂的字段。

第 3 条最值得记：它对功能没影响，但**它是 DSH 的调用契约**。少了它，模型每次 bash 调用都会多一个"多余的参数"。

## 返回结构（第二轮对拍）

数据来源：真实会话里 `tool/call`（有工具名）与 `tool/result`（有 callId）配对后的正文。

**DSH 是 text-first，而且带 XML 包裹**：

| 工具 | DSH 的正文形状 |
|---|---|
| `read` | `<path>…</path>` + `<type>file</type>` + `<content>` + `1: 行内容` … + `</content>` |
| `write` | `<path>…</path>` + `<type>file</type>` + `<content>` + `Created file` / `Updated file` + `</content>` |
| `glob` | 纯文本（`No files found` / 列表） |
| `grep` | 纯文本（`Found 8 matches` + 命中行） |
| `bash` | 原样 stdout/stderr |
| `job_output` | 原样输出 + `[status: completed, exit code: 0]` |
| `job_kill` | `requested cancellation of job <id>` |
| `job_list` | `<id> [<kind>] <status> — <命令>` |

**我们做了什么**：`spi.Result` 新增 `Text` 字段——工具已经渲染好的模型可见正文。
`read` 与 `write` 改成 DSH 同款包裹（含行号前缀、`Created file`/`Updated file`），
`session.ProjectResult` 优先用 `Text`，没有时才退回 `Summary + JSON(Data)`。

这样模型看到的 `read` 结果和 DSH 一致，同时我们内部仍保留结构化 `Data`（给 UI 和测试用）。

## 没有对拍的部分

- **描述措辞**：我们写中文、DSH 写英文。模型理解得了，但不是逐字一致。
- ~~返回结构~~ → `read`/`write` 已对齐 DSH 的 `<path>/<content>` 包裹；`glob`/`grep`/`bash`/`job_*`
  仍是"结构化 Data + 摘要"，没照抄 DSH 的纯文本格式（那些格式没有包裹，模型两种都认）。
- **边界行为**：超大文件、二进制、符号链接、并发写这些，各自实现，没做交叉验证。

## 行为对拍：失败长什么样（2026-11-02 补）

语料实测：**23,407 个 tool/result，367 个 isError=true（1.6%）**。
失败文本的约定：

| 规则 | 例子 |
|---|---|
| 一律 `Error: ` 前缀 | `Error: cannot modify "…": file has not been read — read the file, then retry` |
| 路径用双引号原样带上 | `Error: offset 1080 is out of range for "…/eval.mjs" (1052 lines)` |
| 说清**下一步怎么做** | `provide a more specific old_string or set replace_all to true` |
| 不要静默返回空结果 | 路径不对 → 报错，不是"0 处命中" |

按这三条改掉的三处：`read` 的 offset 越界、`grep` 的坏路径、失败事件正文为空。

## bash 的 110 个失败样本（2026-11-02）

按首行归类：

| 条数 | 首行 |
|---:|---|
| **84** | `Error: unknown tool "bash"` |
| 13 | `The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown.` |
| 5 | `Error: this destroys the runtime DSH would need in order to undo it; there is no recovery path` |
| 4 | `Error: tool call aborted` |
| 2 | `Error: sandbox mode "workspace-write" is requested but no sandbox backend…` |
| 2 | `Error: redirect overwrites $HOME/.dsh/state/…` |

两个结论：

1. **最常见的"bash 失败"根本不是 bash 失败**，而是工具名不在当前工具表里（84/110）。
   工具集随模式/配置变，模型手里的旧上下文可能还记着 `bash`。
   → 我们的措辞改成 `unknown tool "<name>"`。
2. **取消 ≠ 失败**。DSH 对"记录下来但没收到结果"的调用，写的是
   *outcome is unknown*，不是 failed——因为命令可能已经跑完了。
   → 我们把 `context.Canceled` 单独处理成同一句话 + 引导先看现场。

## edit 的 75 个失败样本（2026-11-02）

| 条数 | 归类 |
|---:|---|
| **34** | `cannot edit "<p>": file changed since it was read — re-read the file, then retry` |
| **29** | `cannot modify "<p>": file has not been read — read the file, then retry` |
| 10 | `old_string was not found in "<p>"` |
| 1 | `old_string matched 2 times in "<p>"; provide a more specific old_string or set replace_all to true` |
| 1 | `unknown tool "edit"` |

**84%（63/75）来自读后写策略。** 这不是"edit 不好用"，而是模型在盲改——
而这条错误信息本身就是它的教程：一句话写清「做了什么（cannot modify）/
为什么不行（file has not been read）/ 下一步（read the file, then retry）」。

我们把这条措辞逐字对齐了（含破折号），并让动词跟着工具走：
`write` 用 `cannot write`、`edit` 用 `cannot edit`。
测试用同构比对（把路径换成占位符）钉住，防止以后"顺手改文案"改跑偏。

## 参数缺失 vs 目标不存在（2026-11-02）

拿 `dshd exec --tool <t> --args '{}'` 逐个打了一遍，发现两类错误被混成一句话：

| 工具 | 空参数时的报错（修前） |
|---|---|
| `read` / `write` / `edit` | `file_path 为空` ✅ |
| `glob` / `grep` | `pattern 为空` ✅ |
| `bash` | `command 为空` ✅ |
| **`job_output` / `job_kill`** | **`没有任务 ""`** ❌ |

最后两条把「你没给参数」说成了「这个任务不存在」。模型看到后会觉得
"id 可能写错了"，于是去 `job_list` 找一个根本不存在的 id——**方向错了**。

修完：

```
$ dshd exec --tool job_output --args '{}'
执行失败: job_id 为空
提示: job_id 不能为空；先用 job_list 看有哪些任务

$ dshd exec --tool job_output --args '{"job_id":"nope"}'
执行失败: 没有任务 "nope"；用 job_list 看有哪些
```

**两类错误的下一步动作不同，就不能用同一句话。**
`TestJobToolsRejectEmptyID` 把两条都钉住：空 id 不许出现「没有任务」，
未知 id 必须出现「没有任务」。

## 「0 命中」也要分清是哪种 0（2026-11-02）

`glob` 原来只说 `匹配 0 个文件`。可模型需要知道的是：**是真的没有，还是被挡住了？**

两件事的下一步动作完全不同：

| 情况 | 模型该做什么 |
|---|---|
| 真的没有这个文件 | 改 pattern / 换 base 路径 |
| 被 `.dshdignore` 挡住了 | 改声明，或者换 `bash` 直接找 |

修完两个工具都分开说了：

```
$ glob {"pattern":"**/*.lua"}
. 下匹配 **/*.lua：0 个文件（扫过 157），.dshdignore 挡掉 2 个目录
提示: 0 个匹配，但有 2 个目录被 .dshdignore 挡掉了；要搜就把它从 .dshdignore 里去掉，或者用 bash 直接找
```

```
$ grep {"pattern":"zzz_no_such_symbol"}
. 下 /zzz_no_such_symbol/ 命中 0 处（扫描 156 文件，跳过 1 个噪声目录 + 2 个 .dshdignore 目录）
提示: 0 处命中，但有 2 个目录被 .dshdignore 挡掉了；确认要找的东西不在那些目录里，或把它从 .dshdignore 去掉
```

没有 `.dshdignore` 时仍是原来的措辞（`没有匹配：确认 pattern 的相对基准…`）——
**不能因为加了新分支就让老路径的说法变味。**

钉住的两条测试：`TestGlobZeroMatchMentionsIgnore`、`TestGrepZeroHitMentionsIgnore`。

## 「不唯一」的两条出路必须真的能走通（2026-11-02）

`edit` 遇到多处匹配时的报错：

```
执行失败: old_string 不唯一（2 处）
提示: old_string 在文件里出现 2 次；给它更多上下文让它唯一，或显式传 replace_all=true
```

文案早就对齐了，但**"照着做能不能成"从来没验过**——文案说得再清楚，
如果照着做仍然失败，模型会在同一个地方卡住（真实语料里 edit 失败 75 次，这条占 1 次）。

`TestEditAmbiguousHintIsActionable` 把两条出路各走一遍：

| 照做 | 结果 |
|---|---|
| 加更多上下文（`alpha
TODO
beta` → `alpha
DONE
beta`） | ✅ 只改第一处，第二处原样 |
| 显式 `replace_all: true` | ✅ 两处都改 |

### 真机观察：这条错误很少被触发

给一个两行重复 `TODO` 的文件，让它"只改第一个"：

```
tool_calls=3 input=7038 output=368
```

模型第 1 步就 `read`，然后**自己用上下文拼出唯一片段**（`alpha
TODO
beta`）——一次成功，
没走到歧义分支。它回答里还解释了这一步：

> 由于两行文本完全一样，我把上下文 `alpha`、`beta` 一起用来匹配，以保证只改到第一个。

所以这条错误路径的正确性靠测试保证，**触发率低是好事**——说明"先读再改"这条规则在起作用。

## ACP 方法的边界（2026-11-02）

拿一个真会话把边角都打了一遍：

| 请求 | 返回 | 评价 |
|---|---|---|
| `session/set_mode` 未知模式 | `-32602 未知模式 "no-such-mode"（可选：default, read-only）` | ✅ 直接给出可选值 |
| `session/set_mode` 合法 | `{"modeId":"read-only"}` | ✅ |
| `session/delete` 不存在 | `删除失败: 找不到会话 nope（在 <home> 下）` | ✅ |
| **`session/load` 不存在** | `载入失败: 读会话失败: 读会话文件失败: open D:\...\sessions\--D-...--
ope\session.v4.jsonl.zstd: ...` | ❌ |
| **`session/resume` 不存在** | 同上 | ❌ |

后两条把**宿主路径**和**三层嵌套的错误链**直接给了客户端。路径是宿主的实现细节，
客户端要的只是一句"没有这个会话"。

改完：

```
load 不存在    → -32602: 载入失败: 找不到会话 nope
resume 不存在  → -32602: 恢复失败: 找不到会话 nope
```

`sessionErrText` 只把 `fs.ErrNotExist` 认成"找不到会话"，其它错误原样透出——
**不能把"真出错了"也说成"没有这个会话"**（又是同一个判据：两种后续动作不同）。

`TestSessionErrTextHidesPaths` 把两条都钉住。

## 模型调用失败：客户端不该看到 Go 的错误长什么样（2026-11-02）

把 `--base` 指到一个不存在的端口，再发一次 prompt，客户端原来收到的是：

```
-32603 第 1 步模型调用失败: 请求模型失败(http://127.0.0.1:1/v1/chat/completions):
       Post "http://...": dial tcp 127.0.0.1:1: connectex: No connection could be made
       because the target machine actively refused it.
```

三个问题：接口路径（`/chat/completions`）漏出去、Go 的原始网络错误原样透出、
整句话**没有一个字告诉用户该做什么**。

改完（`internal/llm.GatewayError`，按种类分开说）：

| 情况 | 客户端看到 |
|---|---|
| 连不上 | `连不上模型网关 http://127.0.0.1:1/v1：确认网关在跑、--base 地址对` |
| 401 / 403 | `模型网关拒绝认证（401）：检查 API key 与 --base` |
| 404 | `模型网关没有这个接口（404）：--base 是不是少了 /v1？` |
| 429 | `模型网关限流（429）：稍等再试` |
| 5xx | `模型网关出错（500）` |
| 其它 4xx | `模型网关返回 400：<正文最多 200 字>` |

- 原始错误用 `%w` 留着（日志、`errors.Is` 都还能用），只是不进 `Error()`
- 网关正文原来整段 8KB 塞给客户端，现在截到 300 字符——正文里可能有 HTML 错误页、堆栈、内部主机名
- **API key 从来没漏过**（实测传了 `DSHEDD_LLM_KEY=sk-...`，响应里没有）

判据还是那条：**两类问题的下一步动作不同，就不能用同一句话。**
「连不上」去查网关起没起，「401」去查 key——都不是「请求模型失败」。

## 审批框里给用户看的是"要执行什么"，不是"模型说它要干什么"（2026-11-02）

`bash` 的 schema 里有个 `description` 参数，代码一直**忽略**它。查这件事的时候把整条审批链路
摆出来看，发现真正的问题不是"要不要显示 description"，而是**显示的顺序和标注**。

事故形态很具体：模型把 `rm -rf /tmp/x` 的 description 写成"看看临时目录"。
如果审批框只显示 description，用户点 y 是在批准一句话，不是在批准一条命令。

现在的样子：

```
需要审批：bash
       命令: rm -rf /tmp/build
       模型自述（仅供参考，不是依据）: 看看临时目录
       → 允许？[y/N]
```

三条规矩：

1. **命令原样、排第一**——眼睛先落到的必须是它；
2. **模型的 description 单独一行，并且标成"模型自述（仅供参考，不是依据）"**；
3. **命令太长时截中间而不是截尾巴**——危险的部分常在结尾（`rm -rf /`、`> /dev/sda`）。

顺带把 `write` / `edit` 的审批框也改了：显示**路径与字节数**，不再把正文整个塞进终端。

```
需要审批：write
       写入 a.txt（5 字节，整体覆盖）
```

钉住四条测试（`TestDescribeApproval*`）：命令排在描述之前、尾巴不被截掉、
write 不显示正文、解析失败时原样显示参数。

## ACP 侧的审批请求也是同一件事（2026-11-02）

上一节改的是终端里的审批框。可**走 ACP 的客户端**（Martty 这类）走的是另一条路：
`session/request_permission`。原来它发出去的东西是：

```json
{"toolCall": {"title": "bash", "kind": "other", "rawInput": {...}}}
```

`title` 只有工具名 `bash`。**如果客户端只显示 title，用户看到的就是"bash"两个字**——
比终端那版更糟：终端至少还糊着 JSON。

改完（真机抓到的原文）：

```
title: bash: echo approved
kind : execute
content: [{"type":"content","content":{"type":"text",
          "text":"命令: echo approved
模型自述（仅供参考，不是依据）: 输出 approved"}}]
```

三个字段各司其职：

| 字段 | 作用 | 为什么必须这样 |
|---|---|---|
| `title` | 一行摘要，**命令在前**；多行命令压成一行 | 很多客户端只显示这一行 |
| `kind` | `execute` / `edit` / `other` | 让客户端用对的图标与措辞，而不是一律"其它" |
| `content` | 完整说明，含"模型自述"标注 | 想细看的客户端有得看 |
| `rawInput` | 原样参数 | 机器可读，保持不变 |

钉住三条测试：title 带命令且不混模型描述、多行压成单行且内容不丢、kind 映射正确。
