# 模型容量解析（0.2.2）

> 基线：`af4a49a` + 集成 W22/W24 后的共享工作树。本文只覆盖 `internal/llm` 的容量解析、`agent.Loop` 常规请求和 ACP 的窗口/输出声明，不代表完整移植上游 pi-ai 模型目录。

## 1. 唯一解析点与优先级

`llm.ResolveModelCapacity(modelID, declared)` 返回 `llm.ModelCapacity{ContextWindow, MaxOutput}`。解析不读写全局缓存，也不选择模型；每轮 `agent.Loop` 只解析一次，所有 step 复用同一份结果。

按字段应用以下顺序：

1. 调用方显式声明的正整数优先；32K/8K 及更小端点的值原样保留，不被默认值抬高。
2. 当前内置已知模型使用既有兼容值。
3. 只有未声明容量的自定义模型补十进制 **1,000,000 context / 128,000 maxOutput**（不是 1,048,576 / 131,072）。

当前声明入口：

- 常规 agent 调用：`agent.Loop.Capacity`；解析后的 `MaxOutput` 写入 `llm.Request.MaxTokens`，因此未声明自定义模型的常规请求带 `max_tokens:128000`。
- ACP：`--context-window` 与 `--max-output`。两者缺省为 0，表示按模型解析；显式正值优先。有效 `ContextWindow` 同时作为 `usage_update.size`。

## 2. 当前“已知 / 自定义”的边界

本仓没有上游 pi-ai 目录，`models.list` 也只有 `id/owned_by`，没有 custom 或容量元数据。本包不伪造这份目录，采用明确的现有声明面：

| 模型 ID | 未显式声明时的结果 |
|---|---|
| `group/deepseek-v4.1-flash-1017`（当前内置默认模型） | 保持 `ContextWindow=128000`、`MaxOutput=0`；常规请求仍不新增 `max_tokens` |
| 其他非空自由文本 `--model` | 视为未声明自定义模型，得到 `1000000/128000` |
| 空模型 ID | 不补默认值，保留调用方声明；空零值仍是未知 |

因此，“网关列表里存在某个 ID”不等于“宿主知道它的容量”。较小端点必须经上述声明入口显式给出容量，否则会按自定义默认处理。后续若引入真实模型目录，应在同一个解析点补目录优先级，而不是在 ACP、Transport 或请求构造处另写一套默认值。

## 3. 明确不变的部分

- 不改 Transport API、事件、快照或 golden；Transport `contextWindow=0` 仍表示未知，本包不会把 1M 填进去。
- 不改 `/v1/models` 或 `models.list` 字段；内部解析器不扩展现有 `ModelEntry`。
- 不改模型选择、会话模型覆盖或路由。
- `--auto-compact=40000` 保持原默认值。1M context 不表示自动压缩已经延到 1M；压缩阈值是否调整是独立产品变更。
- 压缩、标题等直接构造的特殊 LLM 请求不经过 `agent.Loop`，其既有 `MaxTokens` 策略不被本包重写。
- 未移植上游完整的 catalog、`modelOverrides`、`configuredMaxTokens` 或动态声明配置系统。

## 4. 必红行为与验证

修复前实际执行：

```bash
go test ./internal/agent -run '^TestRegularRequestUsesModelCapacity$' -count=1 -v
go test ./cmd/dshd -run 'Capacity' -count=1 -v
```

旧实现按预期失败：自定义常规请求 `max_tokens=0`（期望 128000）；ACP 自定义模型 context 与 `usage_update.size` 都是 128000（期望 1000000），请求同样缺少 128K 输出。内置已知模型控制组保持通过。

修复后覆盖：

- 未声明自定义模型得到十进制 1M/128K，真实 ACP HTTP 请求带 `max_tokens=128000`。
- 显式 32K/8K 原样生效，`--context-window` 使用的同一入口优先于默认值。
- A→B→A 不同模型及不同会话的请求和 `usage_update` 不串值。
- 内置已知模型保持 128K 窗口且请求不新增输出上限。
- 空 ID、逐字段声明和更小端点覆盖都有独立解析测试。

定向命令：

```bash
go test ./internal/agent ./internal/llm ./cmd/dshd -count=1
```

结果：三个包均 `ok`，退出码 0。

全仓门禁：

```bash
go test ./... -count=1
go vet ./...
go build ./...
```

结果：三条命令均退出码 0；`go test ./...` 覆盖全部列出包，无失败项。本文件不以外层共享脚本的 `ALL GREEN` 代替这三条实际命令。

## 5. 未验证 / 不外推

- 未调用真实 magpie 或远程模型验证其是否接受并实际执行 128K 输出；`httptest` 证明的是宿主真实请求体和 ACP 事件链。
- 未做真实端点容量探测。1M/128K 是未声明自定义模型的约定默认，不是网关自述。
- 未跑 `-race`、完整工作日、GUI、跨仓 UI 或真实长上下文 A/B；模型/会话隔离目前由无共享缓存设计与顺序行为测试证明。
- 未验证所有上游已知模型的目录容量；当前只保留本仓内置默认模型的既有值。
- 本包未提交、未 push，也未替换运行中的二进制。
