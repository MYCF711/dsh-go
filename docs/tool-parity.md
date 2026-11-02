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
