# 工具表预算

> 这张表是**产品指标**，不是附注：DSH 最新一次请求的工具表是 61 个 / 54,936 字符 / **12,209 token**，
> 而工具回显占上下文的 **85.5%**。我们给自己定的上限是 **≤ 6,000 token**。

## 当前实测（2026-11-02）

| 项 | 值 |
|---|---:|
| 工具数 | 9（`read` `write` `edit` `glob` `grep` `shell` `job_output` `job_kill` `job_list`） |
| 快照文件 | `cmd/dshd/testdata/tools.snapshot.json`（5,556 字节，2 空格缩进） |
| 线上形态（紧凑 JSON） | **3,760 字节** |
| **token（实测）** | **906** |

## 怎么测的（可复现）

1. `go run ./cmd/dshd tools` → 打印模型可见的工具表（name + description + parameters），顺序按名字排序，字节稳定。
2. 把**紧凑 JSON**（`separators=(',',':')`）作为单条 user 消息发给本机网关：
   ```
   POST http://127.0.0.1:3425/v1/chat/completions
   {"model":"group/deepseek-v4.1-flash1017",
    "messages":[{"role":"user","content":<紧凑 JSON>}],
    "max_tokens":1}
   ```
3. 读 `usage.prompt_tokens` → **906**（含 chat 模板开销，所以工具表本身略少）。

**不许估算。** 换模型、换网关、改描述、加工具之后都要重测并更新本节。

## 预算分配（给自己定的）

| 里程碑 | 工具数 | token 目标 |
|---|---:|---:|
| M1（当前） | 9 | ≤ 1,200 |
| M1 收口 | 18 | ≤ 2,400 |
| P0 完成（M5） | ≤ 24 | **≤ 6,000** |

超了先砍描述、再合并工具，最后才考虑删能力——**先删字，不先删功能**。

## 每个 token 花在哪

- `parameters` 的 `description` 字段：模型看它决定参数怎么填，值得花；
- 顶层 `description`：模型看它决定**要不要用这个工具**，上限 120 字（有测试守着）；
- 反例（不花）：工具名大小写解释、示例长文、内部术语。

## 反例记录

- DSH 的 `bash` 一个工具就占 1,345 token（最长的一条），因为它把 shell 的全部语义都写进了描述；
  我们的 `shell` 只写"什么时候用它"，细节交给报错时的 `Hint`。
