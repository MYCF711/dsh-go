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

## 没有对拍的部分

- **描述措辞**：我们写中文、DSH 写英文。模型理解得了，但不是逐字一致。
- **返回结构**：只比了入参契约。工具 `Data` 的字段名没比——那要跑同一批输入两头对比，
  目前没有 DSH 的侧信道可调（它是个正在运行的 GUI 应用）。
- **边界行为**：超大文件、二进制、符号链接、并发写这些，各自实现，没做交叉验证。
