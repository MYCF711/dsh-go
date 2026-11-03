# H3：`messages[].view` 的现状与做法（2026-11-03 查证）

## 查到的三件事

1. **`spi.ViewSpec` 已经存在**（`internal/spi/tool.go:105`），形状就是界面要的：
   `kind` / `title` / `badge` / `fields` / `columns` / `rows` / `body`
2. **三个工具已经在产出它**：`modules/exec/bash/tools.go`、`modules/tool/grep/grep.go`、`modules/tool/read/read.go`
3. **但它从来没有被写进会话日志**：全仓 `grep '"view"'` 在 `internal/session` 与 `cmd/dshd` 下**零命中**

第 3 条决定了一件事：

> **H3 不是"透传"，是"先落盘再透传"。**

界面线在 §12 里写的需求读起来像"你把已有的透传一下"，
实际上 `ViewSpec` 活在**执行期内存**里（`spi.Result.View`），轮次结束就没了。

## 所以要做两步

| 步 | 改哪里 | 做什么 |
|---|---|---|
| ① | `internal/session/log.go` 的 `ToolResultEvent(...)` | 多收一个 `view *spi.ViewSpec` 参数，非 nil 时写进 `data.view` |
| ② | `cmd/dshd/transport_source.go` 的 `messagesOf` | 读 `data.view` → `transport.Message.View` |

①要改函数签名，调用点在 `internal/agent` 里，属于**跨包改动**——不能派给执行者做单文件活，
得按"先改签名再改调用点"两步走，或者由我先加一个不破坏签名的变体。

## 为什么不直接把 ViewSpec 塞进 blocks

`blocks` 是 `spi.Block`（发给模型看的正文），`view` 是**给客户端看的渲染声明**。
两件事混在一起，会让模型读到一堆渲染用的列名/行数据——**上下文预算会涨，而且模型会以为那是内容**。

## 已经摸到的（2026-11-03 第二轮查证）

- `internal/agent/loop.go:259` 发事件时**带着 `Result`**：`Event{Kind: EventToolResult, ..., Result: &shown}`
- `spi.Result` 里就有 `View *ViewSpec` —— **所以 view 在事件里是拿得到的**
- `ToolResultEvent(...)` 的调用点只有 `internal/session/fork.go:99` + 三个测试
- `cmd/dshd/main.go:519` 那个分支只是**打给人看**的（`→ bash ...` / `← bash [ok]`），不是落盘

**落盘发生在哪，还没定位**——这是下一步唯一要查的事。
在找到"agent.Event → 会话帧"的那个转换点之前，动手就是猜。

## 下一步

先确认 `internal/agent` 里 `ToolResultEvent` 的调用点有几处、能不能加参数而不动别处，
再决定①由我做还是派给执行者。**在没有查清调用面之前不动手。**
