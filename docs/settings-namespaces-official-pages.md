# 官方 4 条设置页的 8 个设置命名空间（0.2.2-rc.2）

> 2026-11-15 落地。补齐的是**官方 UI 页面的存在条件**：官方客户端用
> `ctx.configForms.whileServed([ns...])` 决定设置页出不出来，宿主不服务那条命名空间
> 时页面**永远不显示**（不是灰色、不是禁用，是整页没有）。此前宿主只发布
> model / permission / llm + 7 个 general slot 的 UI 命名空间，因此 GUI 仓
> `internal/settingsform/settingsform.go:14-15` 那 4 条实测为 MISSING。

## 1. 页面 → 命名空间 → 字段（逐条对照官方源码）

| 官方页面包 | 命名空间 | 字段（逐字） | 权威源（rc.2 随包 `src/**`） |
|---|---|---|---|
| `dsh-client-ui-settings-agent-loop` | `agent-loop` | `maxParallelToolCalls` | `dsh-agent-loop/src/index.ts:294-306`、`src/constants.ts:6`（默认 10） |
| `dsh-client-ui-settings-shell` | `bash-sandbox` | `timeoutMs` / `maxOutputBytes` | `dsh-bash-local/src/index.ts:100-107`（120000 / 64000） |
| 同上 | `pwsh-sandbox` | `timeoutMs` / `maxOutputBytes` | `dsh-pwsh-local/src/index.ts:126-133`（同上，另有 `pwshPath`） |
| `dsh-client-ui-settings-subagent` | `subagent` | `maxDepth` / `maxActiveSubagents` | `dsh-subagent/src/index.ts:195-198`（1 / 8，范围 0..MAX_SAFE_INTEGER、1..MAX_SAFE_INTEGER） |
| 同上 | `subagent-model-selection-settings` | `enabled` / `allowedModels` | `dsh-tool-subagent/src/model-selection-settings.ts:52-55`（false / `[]`） |
| `dsh-client-ui-settings-web-search` | `web` | `searchProvider` / `fetchProvider` | `dsh-web/src/index.ts:88-91`（无默认值） |
| 同上 | `web-deepseek-api` | `count` / `searchRequestsPerSecond` / `sendOriginalQuestion` / `searchBilling` / `fetchBilling` / `searchBaseURL` / `searchApiKeyEnv` / `fetchBaseURL` / `fetchApiKeyEnv` | `dsh-web-deepseek-api/src/index.ts:99-109`、`src/search.ts:19,22` |
| 同上 | `web-search-deepseek` | `apiKey` / `apiKeyEnv` / `baseURL` / `model` / `apiVersion` / `maxTokens` / `maxUses` | `dsh-web-search-deepseek/src/index.ts:71-79`、`src/provider.ts:37,40,46` |

命名空间名的取证点（官方客户端自己写的常量，不是本仓猜的）：
`shell-card-controller.ts:11,13` 是 `bash-sandbox` / `pwsh-sandbox`；
`subagent-model-selection-card-controller.ts:12` 是 `subagent-model-selection-settings`；
`web-search-card-controller.ts:52-56` 是 `web` / `web-deepseek-api` / `web-search-deepseek`。

代码落点：`internal/settingsform/{agent_loop,shell_sandbox,subagent,web_namespaces}_schema.go`，
注册在 `internal/settingsform/registry.go` 的 `HostRegistry()`。

## 2. 形状取证（不是"照着文档抄"）

- envelope 夹具：`internal/settingsform/testdata/host-settings-pages-official-envelopes.json`。
- 取证脚本：`tmp/GO8-RC2/_hs4/dump-hs4.mjs`，输出 `tmp/GO8-RC2/_hs4/official-envelopes-hs4.json`。
  - `bash-sandbox` / `pwsh-sandbox` / `web` 直接 require 官方**编译产物**
    （`dsh-bash-local/lib/index.js` 的 `LocalBashExecutor.Config.toJSON()` 等），
    再走官方 `plainSchema` / `volatileForm`（删 `meta.volatile`、secret 删 default/required）。
  - `agent-loop` / `subagent` / `web-deepseek-api` / `web-search-deepseek` /
    `subagent-model-selection-settings` 按官方 `src` 的 zod 声明逐行重建
    （那几个包 require 时缺第三方依赖：`zod` / `eventsource-parser` / `@js-temporal/polyfill`）。
- 对拍用例：`internal/settingsform/host_settings_pages_test.go` 的
  `TestHostSettingsPageSchemasMatchOfficialEnvelopes`（逐节点相同）。

`settings.describe` 行数因此从 **11 → 19**（namespaces 与 sections 各 19）。
受影响的既有断言（`internal/settingsform/retry_policy_schema_test.go`、
`cmd/dshd/serve_settings_api_test.go`、`cmd/dshd/serve_settings_ui_test*.go`）已同步
更新，并在注释里写清"为什么多一行"。

## 3. 消费方（写进去到底有没有人读）

| 命名空间.字段 | 消费方 | 生效点 |
|---|---|---|
| `agent-loop.maxParallelToolCalls` | `agent.Loop.MaxParallelToolCalls`（`internal/agent/parallel.go`） | `cmd/dshd/main.go` 建循环时解析（每轮一个 `dshd run` 进程） |
| `subagent.maxActiveSubagents` | `subagentGate`（`cmd/dshd/subagent_gate.go`） | `subagentGateFromSettings`：设置 > `DSHD_SUBAGENT_MAX_ACTIVE_SUBAGENTS` > 官方默认 8 |
| `subagent.maxDepth` | `subRunner.maxDepthLimit()`（`cmd/dshd/subagent.go`） | 装配期传入；`0` 是官方合法值（不许再委派），用指针与"没设置"区分 |
| `pwsh-sandbox.timeoutMs` / `maxOutputBytes` | `modules/tool/pwsh` 的 `DefaultTimeout` / `DefaultMaxOutputBytes` | `pwsh.WithTimeoutBudget` / `WithMaxOutputBytes`（`cmd/dshd/settings_effective.go: pwshBudgetOptions`） |
| `web.*` / `web-deepseek-api.*` / `web-search-deepseek.*`（有环境变量的那些） | `modules/search` 的 provider（全部只从同一个 getenv 接缝读） | `settingsEnvOverlay`：把"设置优先的 getenv"传给 `search.ModuleWithEnv`（映射表在 `settings_effective.go`） |

优先级统一为 **设置（用户显式写过） > 环境变量 > 消费方默认**。
"用户显式写过"用 `sections[].fields[].overridden` 判定——base 里的默认值**不**
参与覆盖，否则界面一次保存就会把部署环境变量整体盖掉。

生效时机：解析在**进程装配期**。`dshd run` 每轮一个新进程，所以下一轮即生效；
`dshd serve` 是长驻进程，改设置要重启进程。官方的 `applies` 是 `live`（下一次请求），
本仓在 serve 路径上不等价——这是已登记的语义差异，不写成 live。

## 4. 缺口（如实登记，不造假消费者）

1. **`bash-sandbox.timeoutMs` / `maxOutputBytes` 没有消费方。**
   宿主 bash 执行器（`modules/exec/bash`）的旋钮是 IdleBudget（多久无输出转后台，
   默认 10 分钟）、GraceBudget（`DSHD_BASH_GRACE_MS`，默认 3000ms）与 retention
   保留量，**没有**"前台命令总超时"与"每路输出内存上限"这两个语义。硬接会换语义：
   `timeoutMs` 接到 ctx 超时会把长命令在 120s 掐掉；`maxOutputBytes` 接到 retention
   会把 1MiB 运行窗口改成 64KB。所以两条字段**可读写、有官方默认值、能往返，但不改变
   行为**。
2. **`subagent-model-selection-settings` 没有消费方（两个字段都是）。**
   官方语义是"新会话按模型路由授权子代理"：`agent/created` 时把允许的
   `provider/model` 写成会话投影 `subagent/model-selection-policy`，模型之后可在委派
   时点名路由。本仓委派是同步 `subRunner`，子代理固定用父进程的同一个模型
   （`cmd/dshd/subagent.go` 的 `model string`，工具 schema 里没有 provider/model 参数），
   也没有"按会话授权路由"的投影通道。`enabled` 默认 **false**（与官方 shipped 组合
   一致：官方就是默认关闭），写 true 会真的存进去，但不会有行为改变。
   `allowedModels` 是**对象数组**，L1 的 `sections[].fields` 只有三种标量，所以它只发
   schema（官方 envelope 有它）、语义校验按官方 `assertAllowedModelRoutes` 做
   （非空 provider/model、路由不重复），但不出现在 sections 投影里。
3. **`web-search-deepseek.apiKey` / `apiKeyEnv` 没有真正的凭据解析器。**
   官方按 credential-ref 名字去凭据服务取值；本仓
   `modules/search/deepseek.go` 固定读 `DEEPSEEK_API_KEY` / `DSHD_SEARCH_API_KEY`
   两个环境变量。两个字段可读写、能往返，`apiKey` 走 secret 槽位（只报"设没设"），
   但不改变请求实际带的凭据。
4. **`bash-sandbox` / `pwsh-sandbox` 的发布面比官方 served schema 窄。**
   官方 served schema 是执行器 Config 的**全部** volatile 字段
   （`cwd` / `timeoutMs` / `maxTimeoutMs` / `maxOutputBytes` / `maxSpillBytes` /
   `graceMs`，pwsh 另有 `pwshPath`）。本仓只发布页面编辑的两个字段——官方客户端
   自己把这条写成 "The shell fields this page edits — a subset of the served schema
   by design"（`shell-card-controller.ts:21-27`）。夹具对这两条只保留两个字段，
   `TestHostSettingsPagesPublishPageEditableShellFields` 钉住"发布面就是这两个"，
   防止有人悄悄扩大发布面却不登记消费者。

## 5. 门禁

```console
CGO_ENABLED=0 go build ./...                              EXIT=0
go vet ./...                                              EXIT=0
CGO_ENABLED=0 go test -timeout 20m ./... -count=1          EXIT=0（见交付报告）
go test ./internal/settingsform/ -run HostSettingsPage     PASS
gofmt -l cmd internal modules codecs                      空
python scripts/text-only-guard.py                          PASS（白名单 46/46，漂移 0）
python scripts/gap-register-counts.py --check              PASS
python scripts/single-source.py                            PASS
python scripts/race-guard.py                               PASS
staticcheck -checks=SA*,S1* ./...                          EXIT=0
```
