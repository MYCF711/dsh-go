# HOST-PROVIDER-ROUTING · active catalog 模型的每轮 provider 路由

> 基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 任务开始时共享工作树（含 W63/W67/W69/W72）。本任务未 commit/push，未启动 GUI，未改 Transport、jobs、effort、settingsform、UI 或 `docs/transport-contract.md`。
>
> 范围：关闭 W63 文档中“active 只换 model id，endpoint/credential 仍由 DSHD_LLM_BASE/KEY 决定”的边界。两个 serve runner 现在都会把当前模型的 `baseURL/api/apiKeyEnv` 与 stored credential 用于真实请求；普通 model id 保持旧行为。

## 1. 每轮解析顺序

两个 runner 都先经既有 `resolveServeModel(home,sessionID,fallback)` 取得本轮 model id，再经同一 `resolveServeProvider` 重新读取 `<home>/state/model-catalog.json`。没有跨轮缓存的 active provider，也没有进程级“当前 provider”。

`ModelCatalogStore.ResolveProvider(id)` 的规则：

| catalog 条目 | 行为 |
|---|---|
| id 不存在 | 解析器返回 `not_found`；runner 为兼容旧会话与普通自定义 model id，保持 legacy `DSHD_LLM_BASE/DSHD_LLM_KEY` |
| `api/baseURL/apiKeyEnv` 全部缺省 | 普通 model id，保持 legacy |
| 声明任一 provider 字段 | 必须完整通过 provider 校验，绝不因错误配置回退到另一 provider |
| `api` 缺省 | 按既有 catalog test 语义补 `openai-completions` |
| `baseURL` 缺失或非法 | `invalid_config`，本轮在请求/spawn 前失败 |
| 声明 `apiKeyEnv` 但 credential 未配置 | `credential_missing`，本轮在请求/spawn 前失败 |
| 目录 JSON 损坏、重复/非法字段、坏 URL | 保留读取错误并带 `state/model-catalog.json` 路径，本轮失败，不覆盖原文件 |

credential 只从两个既有来源读取，优先级为：

1. catalog 的 stored credential（`<home>/state/model-catalog.json` 的 0600 write-only 存储）；
2. stored 为空时，读取 `apiKeyEnv` 同名环境变量。

`ModelProviderConfig.credential` 不导出，也没有 JSON 字段；调用方只能经 `Credential()` 把它交给本轮 client 或受管子进程。公开 catalog list 仍只含 `{configured,writable}`。

## 2. 两个 runner 的真实接线

### 直接 `modelRunner`

provider 条目使用本轮局部变量：

```go
llm.New(provider.BaseURL, provider.Credential()).Stream(ctx,
    llm.Request{Model: model, Messages: history, Stream: true}, nil)
```

没有 provider 配置的模型仍使用原来的 `r.base + os.Getenv("DSHD_LLM_KEY")`。A/B 并发时 base/credential 都是调用栈局部值，不写共享 runner，也不写全局环境。

### 默认 `loopRunner` / `dshd run` 子进程

provider 分支在每轮独立的 `loopRunner` 副本上：

- argv 追加 `--base <catalog baseURL>`，model 仍由既有 `--model` 传递；credential 绝不进 argv。
- 构造独立 `exec.Cmd.Env`：先删除继承的 `DSHD_LLM_BASE/DSHD_LLM_KEY`（Windows 名称按不区分大小写处理），再只给这个子进程写入解析后的两个值，保证每个键恰好一份。
- 不使用 `os.Setenv`，不修改父进程环境。另一个会话可同时用另一 endpoint/credential 起子进程。
- follow/journal、approval broker、jobs stdin/stdout 协议、jobActions、turn 收尾继续复用既有组件；真实 dshd 测试不是只测 argv 捕获器。

无 provider 配置的分支仍直接调用原 `loopRunner.Run`，argv 不新增 `--base`。

## 3. 旧会话、新会话与 SetModel

- W61/W63 的 `session-model.json` 语义不变：旧会话保留创建时 model id；`set-active` 只影响之后的新会话。
- 旧会话的 model id 即使未收录 catalog，也仍走原 `DSHD_LLM_BASE/KEY`，不会被新目录 retroactive 改到别的 provider。
- 新会话与显式 `session.setModel` 都在下一轮按当时 catalog 重新解析；已收录 provider 模型使用自己的 endpoint/credential。
- dshd 重开后不依赖内存缓存，catalog、session override 与 active default 都从原存储恢复。
- catalog API 对明确不存在的 id 仍是 `not_found`；`models.catalog.test/set-active` 的既有 not-found 行为不变。

## 4. 错误与秘密清洗

`llm.RedactCredential` 是 catalog test 与真实流式请求共用的清洗函数：

- `Client.Stream` 在把上游非 200 响应正文交给 `GatewayError` 前替换 credential literal。
- `models.catalog.test` 的连接、HTTP、响应解析错误使用同一替换。
- `modelRunner` 在 `turn/error` 与返回 error 前再经过同一清洗；loop 的 child stderr、turn/error 与运维日志也清洗。
- 测试端点故意回显错误 credential；断言返回 error 与 Transport 帧含 `[redacted]` 且不含 literal。
- 测试记录只保存 `AuthorizationOK` 布尔值，不把 Authorization 写入测试日志。

## 5. 真实验证矩阵

所有测试使用临时 home 和本地 `httptest` OpenAI 兼容端点，未调用外部模型。两个独立端点分别检查：

- 请求路径恰为 `/v1/chat/completions`；
- JSON `model` 恰为当前 catalog id；
- `Authorization` 恰为 `Bearer <该模型 stored credential>`；
- legacy 请求只能到第三个 decoy/legacy 端点，provider 请求不得落到它。

| 层 | 覆盖 | 原样日志 |
|---|---|---|
| 实现前 llm 红测 | `ResolveProvider` 与共享 redaction 尚不存在，编译失败 | `C:/Users/Administrator/AppData/Local/Temp/provider-routing-evidence/01-red-llm.log` |
| 实现前 runner 红测 | A 直接请求打错 endpoint；loop 缺 `--base`；缺 credential 仍 spawn；坏配置 fallback | `02b-red-runners.log` |
| 实现前真实 dshd 红测 | `want A` 的实际请求打到 legacy | `03-red-real-loop.log` |
| llm 绿测 | stored/env credential、普通模型、not-found、缺 base/key、坏 URL、目录损坏、共享 redaction | `04-green-llm-first.log` |
| 最终 provider 全套 | llm 解析/redaction；直接 A/B/重开/legacy/plain；loop argv/env A/B/重开/legacy/plain；缺 key 不 spawn；错误 credential/坏配置 | `13-final-provider-tests.log` |
| 并发与真实链 | 直接同实例并发 A/B `OK=7 FAIL=0`；真实 dshd loop 旧会话/A/B/重开/显式 SetModel/并发 `OK=7 FAIL=0` | `08-concurrent-ab.log` |
| W63/W61 联合回归 | model catalog、两个旧 runner、settings default、lazy argv | `07-targeted-regression.log` |
| 全仓 test | `go test ./... -count=1` exit 0 | `09-full-go-test.log` |
| 全仓 vet/build | `go vet ./...` → `GO_VET_OK`；`go build ./...` → `GO_BUILD_OK` | `10-full-vet-build.log` |
| kernel 红线 | `go list -deps ./internal/kernel` 无 modules/业务包，`KERNEL_DEPS_OK` | `12-kernel-deps.log` |
| 相关 race | MinGW-w64 GCC 16.2.0，`CGO_ENABLED=1 go test -race ./internal/llm ./internal/transport ./cmd/dshd -count=1` exit 0，无 DATA RACE | `11-related-race.log` |

日志文件名均位于 `C:/Users/Administrator/AppData/Local/Temp/provider-routing-evidence/`。

### 5.1 先红原样输出

```text
=== RUN   TestLoopRunnerCatalogProviderArgvEnvironmentABReopenAndLegacy
spawn pid=34568 argv=[run ... --model model-a]
    serve_provider_routing_test.go:339: session-a missing --base provider endpoint: [run ... --model model-a]
--- FAIL: TestLoopRunnerCatalogProviderArgvEnvironmentABReopenAndLegacy (0.09s)
=== RUN   TestLoopRunnerInvalidProviderDoesNotSpawn
spawn pid=26172 argv=[run ... --model missing-key]
    serve_provider_routing_test.go:363: missing loop credential error=<nil>
--- FAIL: TestLoopRunnerInvalidProviderDoesNotSpawn (0.07s)
FAIL
FAIL	dshgo/cmd/dshd	0.541s
FAIL
[exit code: 1]
```

真实 dshd 默认 loop：

```text
=== RUN   TestProviderRoutingRealDSHDLoopABAndRestart
    serve_provider_routing_test.go:419: request routed to legacy, want A (model="model-a")
--- FAIL: TestProviderRoutingRealDSHDLoopABAndRestart (2.51s)
FAIL
FAIL	dshgo/cmd/dshd	2.572s
FAIL
[exit code: 1]
```

llm 解析器红测：

```text
internal\llm\model_provider_test.go:33:25: store.ResolveProvider undefined (type *ModelCatalogStore has no field or method ResolveProvider)
internal\llm\model_provider_test.go:38:138: undefined: RedactCredential
FAIL	dshgo/internal/llm [build failed]
FAIL
[exit code: 1]
```

### 5.2 并发 A/B 与重开原样输出

```text
=== RUN   TestModelRunnerCatalogProviderABLegacyAndReopen
    serve_provider_routing_test.go:198: PROVIDER_DIRECT_AB_REOPEN_SUMMARY OK=7 FAIL=0
--- PASS: TestModelRunnerCatalogProviderABLegacyAndReopen (0.38s)
=== RUN   TestProviderRoutingRealDSHDLoopABAndRestart
    serve_provider_routing_test.go:469: PROVIDER_REAL_LOOP_AB_RESTART_SUMMARY OK=7 FAIL=0
--- PASS: TestProviderRoutingRealDSHDLoopABAndRestart (3.73s)
PASS
ok  	dshgo/cmd/dshd	4.194s
```

### 5.3 全仓门禁原样输出

```text
ok  	dshgo/cmd/dshd	48.206s
ok  	dshgo/internal/llm	1.372s
ok  	dshgo/internal/transport	1.229s
```

```text
GO_VET_OK
GO_BUILD_OK
KERNEL_DEPS_OK
```

相关 race：

```text
gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r3) 16.2.0
ok  	dshgo/internal/llm	1.517s
ok  	dshgo/internal/transport	2.150s
ok  	dshgo/cmd/dshd	241.398s
```

最终 provider 全套（含失败分支与真实 loop）：

```text
--- PASS: TestModelCatalogResolveProviderCredentialSourcesAndValidation (0.07s)
--- PASS: TestStreamAndCatalogTestShareCredentialRedaction (0.05s)
--- PASS: TestModelRunnerCatalogProviderABLegacyAndReopen (0.45s)
--- PASS: TestModelRunnerProviderFailuresAreLoudAndRedacted (0.34s)
--- PASS: TestLoopRunnerCatalogProviderArgvEnvironmentABReopenAndLegacy (0.55s)
--- PASS: TestLoopRunnerInvalidProviderDoesNotSpawn (0.02s)
--- PASS: TestProviderRoutingRealDSHDLoopABAndRestart (5.40s)
PASS
ok  	dshgo/cmd/dshd	6.845s
```

## 6. 交付边界

- 只证明宿主两个 serve runner 与本地真实 OpenAI 兼容端点；未启动 GUI，未做外部 provider、完整工作日或性能验收。
- canonical `models.list`、Transport schema/golden、UI/apiclient 均未改。
- stored credential 沿用 W63 的 0600 存储，不宣称它是加密保险库；本任务关闭的是路由、并发串台与输出回显边界。
