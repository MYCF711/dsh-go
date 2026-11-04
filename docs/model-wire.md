# MODEL-WIRE：会话模型覆盖接入下一轮

## 范围与 revision

- 成品施工 revision：`1206b2c` + Lead 提供的共享导出；隔离目录 `dsh-go-model-wire`。
- **最终集成 base：`9d2c62cd3ae06990c057d12f35f9ab2d5ae6d179`（短 rev `9d2c62c`，`feat/terminal`）**；集成分目录 `dsh-go-model-wire-final`。
- 本交付未 commit、未 push。成品差异为 `model-wire-final.patch`，最终集成差异为 `model-wire-integrated-final.patch`。
- 只改 `cmd/dshd/serve.go`、`serve_model_override.go`、`serve_turn.go`，新增 `serve_loop.go` 与对应测试、本文档；未改 UI、Transport 快照、`serve_tools.go`、journal/epoch/MSGID、审批或 permission 实现。

### `serve.go` 冲突处理

原补丁在最终 base 上仅 `cmd/dshd/serve.go` 因 terminal 后续接线无法直接 apply：新版在模型覆盖初始化之前加入 `bash.NewService`/`defer Close`，并在 `transport.Options` 中加入 `TerminalOpen`、`TerminalInput`、`TerminalOutput`。集成时其余六个文件直接采用已验收成品；`serve.go` 不回退 terminal，也不覆盖整文件，只手工合入三个 MODEL-WIRE 语义点：损坏 store 的启动诊断、`modelRunner` 默认 base/model、默认 `modelOverrideLoopRunner` 包装。terminal、jobs、permission、schedule、approval 与 journal/epoch 的现有接线原样保留。

## 行为

`session.setModel{id,model}` 仍只写 `<home>/state/session-model.json`。每轮开始时 `resolveServeModel` 重新 Load 这份小文件并取当前 session 的覆盖：

- A 设置 `model-x` 后，A 的下一次真实 turn 使用 `model-x`；没有覆盖的 B 继续使用 `--model` 默认值。
- 空 model 删除覆盖，A 的下一轮回默认值。
- 新 store/新 runner 从同一 home Load 后，覆盖仍作用于下一轮。
- JSON 损坏或读取失败时，错误包含 `session-model.json` 路径；本轮在起子进程或发 HTTP 请求前返回 `turn/error`，不按默认模型静默成功。serve 启动仍可提供 HTTP 服务，stderr 明示修复前模型轮次会报错。

默认 `loopRunner` 由 `modelOverrideLoopRunner` 包装。它每轮新建轻量副本，只替换该轮的 `model`，不改共享 `base.model`，避免并发会话串模型；journal、broker、pub、policy、reasoning、maxSteps 原样传递，jobs 仍归原 base/broker。`DSHD_SERVE_MODEL_RUNNER=1` 的 `modelRunner` 用同一解析函数填充真实 `llm.Request.Model`，并把 serve 的默认 base/model 显式传给它。

set/Get 不写 v4；真实 turn 自身原有的 v4 append 行为不变。

## 先红证据（旧实现）

以下均在实现前运行，退出码为真实结果：

1. `go test ./cmd/dshd -run 'Test(ModelRunner|DefaultLoopRunner|ModelOverride)' -count=1` → **exit 1**：`undefined: modelOverrideLoopRunner`，默认 runner 没有消费点。
2. 暂时把编译红的 `serve_loop_test.go` 移到包外后运行 `go test ./cmd/dshd -run '^TestModelRunnerSessionModelOverrideLifecycle$' -count=1` → **exit 1**：`A 设置后的下一轮 model = "default-model"，要 model-x`。测试随即恢复该文件。
3. 同一隔离方式运行 `go test ./cmd/dshd -run '^TestModelRunnerRejectsCorruptModelOverrideStore$' -count=1` → **exit 1**：旧实现返回 `<nil>`，损坏 store 后仍发默认模型请求。

红测不是 JSON 存在性检查：默认路径重 exec 测试可执行文件并记录真实 `run` argv；modelRunner 路径通过 httptest OpenAI 兼容端点捕获线上 JSON 请求并检查本轮 prompt 与 model。

## 最终验证

环境：Windows，`go version go1.27.3 windows/amd64`，`PATH` 前置 `/c/Program Files/Go/bin`。Lead 给定最终 base 的全仓 test/vet 基线为 exit 0；下表则是 **`9d2c62c` 最终集成目录内实际重跑** 的结果，不沿用成品目录的历史 ✅。

| 命令 | 退出码 | 结果 |
|---|---:|---|
| `go test ./cmd/dshd -run 'Test(DefaultLoopRunner\|ModelRunner\|ModelOverride)' -count=1` | 0 | `ok dshgo/cmd/dshd`；MODEL-WIRE 定向测试全过 |
| 五个必验测试逐项 `-v` 重跑（下节列出的 lifecycle ×2、corrupt ×2、v4 ×1） | 0 | 五项均 `--- PASS`，默认 runner 日志含四次真实 argv |
| `go test ./cmd/dshd ./internal/transport -count=1` | 0 | `ok dshgo/cmd/dshd`、`ok dshgo/internal/transport` |
| `go vet ./cmd/dshd ./internal/transport` | 0 | 无输出 |
| `go test ./... -count=1` | 0 | 全仓包通过，无失败包 |
| `go vet ./...` | 0 | 无输出 |
| `go build ./...` | 0 | 无输出 |
| `git diff --check -- <本任务认领路径>` | 0 | 无空白错误 |

关键测试：

- `TestDefaultLoopRunnerSessionModelOverrideLifecycle`：真实子进程 argv 依次断言 A=`model-x`、B=默认、清空 A=默认、重启 A=`model-after-restart`；同时钉住 `--session`、`--policy`、`--max-steps`。
- `TestModelRunnerSessionModelOverrideLifecycle`：真实 HTTP `llm.Request` 做同样四段断言，并检查本轮 prompt。
- `TestDefaultLoopRunnerRejectsCorruptModelOverrideStore` / `TestModelRunnerRejectsCorruptModelOverrideStore`：明确错误，子进程/请求计数为零。
- `TestModelOverrideSetGetDoesNotWriteV4`：A/B 会话文件在 set/Get/clear/restart Load 前后逐字节一致。

## 未验项

- 未跑真实 magpie/上游模型生成；模型出口由 httptest 与真实子进程 argv 证明，未把它外推成网关可用性或完整 UI 联调。
- 未跑 `-race`、h-suite、原生 UI 真机或完整工作日；普通全仓 test/vet/build 不替代这些层级。
- 未验证 modelRunner 原有工具循环能力；本次只验证其 `llm.Request` 模型选择，默认工具循环仍由既有 `loopRunner` 子进程负责。
