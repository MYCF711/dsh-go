# D4 · 设置表单 retryPolicy 节点 + 插件市场 update/SSH 回退（rc.2）

> 任务板：`task-16`。基线 `0.2.2-rc.2`；工作目录 `.agents/worktrees/dsh-bdb66fbf-95ba`。
> 结论：**retryPolicy 节点已发布**（`llm` 命名空间、`providers.<route>.retryPolicy`，8 码候选 + `min:5e-324` 哨兵 + 官方默认值，读/写路径同一 schema 校验，非法值 fail-loud）；**市场版本行已下发 `update`**（官方三态判据）与**私有 Git 详情 SSH 回退**（真实 git：https 401 被拒 → SSH 重试成功，remote 固定为 SSH 形式）。
> 尚未接线的只有"宿主存储对 `llm` 的持久化"与两处一行级装配——接线行见 §8，由 Lead 合。

## 1. 官方证据（权威 file:line）

| 结论 | 官方证据 |
|---|---|
| 8 个可选重试码 `RETRY_CODE_CHOICES`（定义序） | `official-rc2/.../dsh-llm/src/error.ts:17`（`LLM_FAILURE_CODES` 中 `retryChoice:true` 的 8 项）、`:30`（筛出顺序）、`:76`（只读导出） |
| `RetryPolicySchema`：`normal`/`always` 两 union 分支、`mode` 是 required const、`maxRetries` `step(1).min(0).max(MAX_SAFE_INTEGER)`、`retryableCodes` = `array(union[8×const, string.min(1)]).min(1)`、`backoff.initialDelayMs/maxDelayMs` `.min(Number.MIN_VALUE).max(MAX_TIMER_DELAY_MS)`、`jitterRatio 0..1` | `dsh-llm/src/retry-policy.ts:82-106` |
| 默认值一个字没变：`5 / 500 / 10000 / 0.1 / [EMPTY_RESPONSE,RATE_LIMIT,SERVER,TIMEOUT,TRANSPORT]` | `dsh-llm/src/retry-policy.ts:19-25` |
| 跨字段规则（schema 表达不了，读路径必须另做）：未知键拒绝、副本码非空/不重复、`initialDelayMs <= maxDelayMs`、`always` 忽略残留 normal 字段 | `dsh-llm/src/retry-policy.ts:120-215`（`validateKeys`/`resolveBackoff`/`resolveRetryPolicy`） |
| `MAX_TIMER_DELAY_MS = 2_147_483_647` | `dsh-timeout/src/index.ts:25` |
| UI 消费形状：模式取 union 分支、错误码取 item 的 `list`、"必须为正"哨兵是 `min === Number.MIN_VALUE` | `dsh-client-ui-settings-models/src/client/RetryPolicyEditor.tsx:1-9`、`:69-71`、`:134-144` |
| 节点路径 `providers.<route>.retryPolicy`，命名空间来自 provider 的 settingsNs | `.../ProviderEditor.tsx:273`；宿主报 `settingsNs:"llm"` 见 `internal/llm/provider_registry.go:119` |
| 市场版本行 `update:boolean` = 已装副本是 `update:'git'`、版本更高、且钉住同仓库同 `path:` 目录 | `dsh-plugin-market/src/index.ts:481-487`；语义注释 `src/types.ts`（`MarketListingVersion.update`） |
| 每版安装来源 → spec（npm → `name@version`；git → `git+<url>.git#<commit>[&path:/<dir>]`） | `dsh-plugin-market-index/src/index.ts:329-334`、`types.ts:52-68`（`MarketSource`） |
| 仓库缓存与 SSH 回退：`OPERATION_COMMANDS=5`、`sshUrlOf`、gitEnv（关交互/promisor/partial-clone）、`denied` 时改 remote 为 SSH 重试一次、成功即固定、失败合并两条诊断按 network/denied 归类 | `dsh-plugin-market/src/repository-cache.ts:21`、`:76`、`:100-120`、`:250-280`、`:287-303` |

## 2. settingsform：发布出去的 schema 节点（实测 describe 输出）

`settings.describe` 的 `llm` 命名空间（`HostRegistry()`，模拟当前 serve 存储——只认 model/permission）：

```json
llm: { "schema": { "uid": 25, "refs": { … } }, "value": {"providers": {}}, "base": {"providers": {}}, "revision": 0 }
sections.llm: { "writable": false, "fields": 0, "revision": 0 }
namespace 列表: ["llm", "model", "permission"]
```

节点形状（`internal/settingsform/provider_schema.go:168` `RetryPolicyNamespaceSchema`，实测 JSON 关键部分）：

```
root          {"type":"object","meta":{},"dict":{"providers":<dict>}}
providers     {"type":"dict","meta":{"default":{}},"inner":<profile>,"sKey":<string>}
profile       {"type":"object","meta":{},"dict":{"retryPolicy":<union>}}
retryPolicy   {"type":"union","meta":{},"list":[<normal>,<always>]}
normal        {"type":"object","dict":{"mode":<const normal required>, "maxRetries":{step:1,min:0,max:9007199254740991,default:5},
               "retryableCodes":{"type":"array","min":1,"default":[5 个 code],"inner":<union>},
               "backoff":{"type":"object","dict":{"initialDelayMs":{"min":5e-324,"max":2147483647,"default":500},
                                                  "maxDelayMs":{"min":5e-324,"max":2147483647,"default":10000},
                                                  "jitterRatio":{"min":0,"max":1,"default":0.1}}}}}
always        {"type":"object","dict":{"mode":<const always required>, "backoff":<与 normal 共享同一 uid>}}
codes item    {"type":"union","list":[8×{"type":"const","value":<官方顺序>}, {"type":"string","min":1}]}
```

- `5e-324` 是 `math.SmallestNonzeroFloat64` 的 JSON 字面值（`Number.MIN_VALUE` 哨兵），**不是任意正数**：已用 `json.Marshal` 断言 wire 上字面出现 `5e-324`。
- 8 个候选顺序 = `EMPTY_RESPONSE, MALFORMED_RESPONSE, QUOTA, RATE_LIMIT, SERVER, STREAM_CLOSED, TIMEOUT, TRANSPORT`。
- `backoff` 两个分支共享同一 uid（官方就是同一个 `backoffSchema` 实例）。
- 口径：`QUOTA` / `MALFORMED_RESPONSE` / `STREAM_CLOSED` 是 rc.2 新增候选，**接受但宿主无产生点**（宿主只产生 `RATE_LIMIT`/`SERVER`/`TRANSPORT` 等，见 `internal/llm/retry_policy.go:59-62`）；用官方语义"code 不匹配就不重试"兜住，不会误触发。

## 3. 读/写路径用同一份 schema 校验（fail-loud，不静默截断）

- 声明式命名空间（`NamespaceSpec.Schema/Base/Validate`，`internal/settingsform/registry.go:43-56`）：
  - **注册期**校验 `Base`：坏默认值直接注册失败（`TestDeclaredProviderNamespaceRegistrationFailsLoud`）。
  - **读路径**（`internal/settingsform/settingsform.go:290` `readOrigin`）：Store 没有该 ns 时用声明默认值投影（`persisted=false`）；一旦 Store 有值，`base`/`user` 都过同一份 envelope 解释器（`provider_schema.go:452` `validateEnvelopeValue`）+ 语义校验（`provider_schema.go:252` `ProviderNamespaceValidator` → `:290` `ValidateRetryPolicyValue`）。非法值 → 结构化错误、describe 整体失败，**不会回落到默认值**。
  - **写路径**：`Mutate` 支持声明式命名空间的多段 path op（`providers.<route>.retryPolicy`，`settingsform.go:677` `setNestedValue`），先校验后写；Store 不支持该 ns 时返回 `read_only` + Hint（HTTP 405），`writeCalls` 不发生（`TestDeclaredProviderNamespaceWriteIsStructured`）。
  - `Store` 一旦支持 `llm`，同一条写入路径立即是真的可持久化路径（`TestDeclaredProviderNamespaceWritePersistsWhenStoreSupports`：嵌套 set → 重开 describe 读回 → 非法值仍拒）。
- 校验规则逐条对齐 `resolveRetryPolicy`：未知键、mode、`maxRetries` 非负安全整数、`retryableCodes` 非空/非空串/不重复、`backoff` 三项界内、`initialDelayMs <= maxDelayMs`、`always` 忽略残留 normal 字段。
- **默认 registry 不变**：`DefaultRegistry()` 仍只有 `model`/`permission`（`TestDefaultRegistryKeepsTwoNamespacesAfterRC2`），`llm` 只由 `HostRegistry()` 显式带上——`settings.describe` 是线上方法，形状变更必须是一次显式装配决定。

## 4. 市场：版本行 `update`（官方三态）

impl：`cmd/dshd/serve_plugin_market.go:158` `marketInstallSpec`（官方 `marketInstallTarget` 移植）、`:197` `marketVersionUpdatable`（官方 `listingOf` 判据逐条）、wire 字段 `internal/transport/handlers_plugin_market_official.go:53`（`Update bool \`json:"update"\``，**总是显式下发、无 omitempty**）。

| 已装副本形态 | 判定 | 用例（`TestMarketVersionUpdatableThreeStates`） |
|---|---|---|
| 未安装 / 来源缺失 | `update=false` | "未安装 → false" |
| registry（`update:'registry'`） | **`update=false`**（官方只在副本是 git commit 时算 origin；"发行版有新版本"由 `installation.status=different-version` 表达） | "registry 副本 → false" |
| git 完整 commit、索引这一版更高、同仓库同 `path:` | `true` | "同仓库另一 commit 且更新 → true" |
| 同仓库不同 `path:` / 不同仓库 / 不更高 / 短 commit / 索引缺 `source` | `false` | 其余 6 条 |

`internal/market` 的 additive 改动：`Version.Source *Source`（`internal/market/store.go:54-73`，类型 `type/url/commit/path/registry`）与 `FindVersion`（`:228`）。老索引缺 `source` → `update=false`，报告口径是**索引缺字段**，不是"官方不支持"。

> ⚠️ 与派单口径的一处**有据偏差**：派单要求"发行版包 → 只有 registry 新版本时 true"，但官方 `dsh-plugin-market/src/index.ts:481-487` 明确 `origin` 只在 `bundle.update === 'git'` 时才有值（`types.ts` 的 `MarketListingVersion.update` 注释同义："the copy came from a Git commit"）。我按官方源码实现并用测试钉住；registry 有新版仍可通过 `installation.status=different-version` 看见（既有行为保留）。若确实要"registry 也可 update"，需要先改官方契约口径（不建议）。

## 5. SSH 回退（真实 git 证据）

实现：`internal/pluginmgr/official/repository_cache.go`（只读 bare 缓存；不碰安装事务）——`SshURLOf:96`、`EnsureCommit:161`、`fetch:232`（SSH 回退落点）、`retryOverSsh:268`、`ReadFile:123`。`cmd/dshd/serve_plugin_market.go:298` `readRepositoryDetail` 在索引无内联 README 时按这一版的 git 来源读仓库 README（官方顺序：内联文档 → 仓库 README → 语言化兜底）。

真实 git 证据（`tmp/GO8-RC2/d4-ssh-evidence.log`，本机 `git version 2.55.0.windows.5`）：

- 第一个请求打到**真实 HTTP 认证服务器**（401 + `WWW-Authenticate`），git 自行判 `denied`（诊断：`could not read Username … terminal prompts disabled`）；
- 仓库缓存把 `remote.origin.url` 从 `http://127.0.0.1:PORT/o/r.git` 换成 `ssh://git@127.0.0.1/o/r.git` 并**再 fetch 一次**（ssh 端用替身命令 `exec git upload-pack <本地仓库>`，仍是 git 自己的 fetch 协议）；
- 结果：`TestRepositoryCacheSshRetryAfterDenied` PASS（README 读出），`TestRepositoryCacheRemoteFixedToSsh` PASS（remote 已固定为 SSH 形式）；两次都失败时 `TestRepositoryCacheMergesBothDiagnostics` PASS（合并 https 拒绝 + SSH 拒绝两条诊断，reason=denied）；连不上时 `TestRepositoryCacheNetworkFailureIsClassified` PASS（reason=network，**不**做 SSH 回退，与官方"只有 denied 才回退"一致）；文件不存在 `TestRepositoryCacheMissingFileIsInvalid` PASS（reason=invalid，不是凭据问题）。
- 失败向 Transport 投影：`cmd/dshd/serve_plugin_market.go:322` `marketRepositoryError` → `PluginMarketError{Code:"detail_unavailable"}`；四字段由 `errorBodyFor/errorObject` 统一给出（`hint` 走 `errorHint` 兜底，专属文案见 §8）。

## 6. 先红证据

| 文件 | 内容 |
|---|---|
| `tmp/GO8-RC2/d4-red1.log` | 先写测试、API 尚不存在：`unknown field Schema in struct literal of type NamespaceSpec` 等 build 失败 |
| `tmp/GO8-RC2/d4-red2.log` | 只加常量桩后 **12 个测试 FAIL**：`describe 没有 "llm" 命名空间`、`providers.<route>.retryPolicy 必须是 union，得到 0`、`retryableCodes 候选 = []`、`initialDelayMs 的 meta.min …`、`store 不支持 llm 时写入必须报错，不许假成功`、`code = "not_found"，期望 "read_only"` 等 |

## 7. 测试结果（本次实跑）

| 命令 | 结果 |
|---|---|
| `go test ./internal/settingsform/ -count=1` | **ok**，25 个 PASS（含 7 个新 retryPolicy schema 测试 + 3 个声明式命名空间测试） |
| `go test ./internal/pluginmgr/official/ -run 'TestSshURLOf|TestRepositoryCache' -v` | **ok**，6 个 PASS（真实 git，约 10.7s） |
| `go test ./cmd/dshd/ -run 'TestMarket' -count=1` | **ok**，6 个 PASS |
| `go test ./internal/market/ ./internal/transport/ ./internal/pluginmgr/... -count=1` | **ok**（transport 5.9s / pluginmgr 16.5s / official 47.1s） |
| `go vet ./internal/settingsform/ ./internal/market/ ./internal/pluginmgr/official/ ./internal/transport/ ./cmd/dshd/` | **ok**（EXIT=0） |
| `go build ./cmd/... ./internal/... ./modules/... ./codecs/...` | **ok**（`go build ./...` 会被**别的 agent** 留在 `tmp/GO8-RC2/d3-backup/**` 的 .go 文件污染而红，与本次改动无关，已按约定避开） |

## 8. 交 Lead 合的接线行（我未改这些文件）

1. **L1 `settings.describe` 服务（关键，UI 读的就是这条）** — `cmd/dshd/serve_settings.go:216-218`：
   ```go
   func newSettingsFormHTTPHandler(store *serveSettingsStore) http.Handler {
       return newSettingsFormHTTPHandlerWithService(settingsform.New(store))          // 改前
       return newSettingsFormHTTPHandlerWithService(settingsform.NewService(store, settingsform.HostRegistry())) // 改后
   }
   ```
2. **官方 settings.update/replace 服务** — `cmd/dshd/serve.go:803` 上下文：
   ```go
   srv.AttachOfficialSettingsCredentialAPI(transport.OfficialSettingsCredentialAPI{
       Settings:    settingsform.New(settingsStore),                                             // 改前
       Settings:    settingsform.NewService(settingsStore, settingsform.HostRegistry()),          // 改后
   ```
3. **既有断言随之从 2 → 3** — `cmd/dshd/serve_settings_test.go:247`：
   ```go
   if len(result["namespaces"].([]any)) != 2 || len(result["sections"].([]any)) != 2 || len(result["errors"].([]any)) != 0 {
   ```
   → 两处 `2` 改 `3`，并加注释「rc.2 起 `llm` 命名空间随 retryPolicy 发布」。
   （`cmd/dshd/serve_settings.go:242` 的提示文案 "ns 必须是 describe 返回的 model 或 permission" 与 `:68`/`:110` 的 "只使用 model 或 permission" 也宜同步提一句 `llm`，纯文案。）
4. **（可选，可选）`detail_unavailable` 的专属 Hint** — `internal/transport/server.go` 的 `errorHint` 里，`case "internal":`（:1550）之后、switch 的收尾 `}`（:1552）之前插入：
   ```go
   	case "detail_unavailable":
   		return "索引没有内联文档时宿主会按这一版的 git 来源读仓库：https 被拒会再用 ssh 重试一次；两次都失败说明本机对仓库既没有 https 也没有 ssh 凭据（或仓库地址/commit 不对）"
   ```
（`internal/transport/handlers_plugin_market_official.go` 的 `Update` 字段已按你批准加好，仅此一处、无别的改动。）

接线后 `settings.describe` 的期望：namespaces = `["llm","model","permission"]`、sections 3 个、`sections.llm.writable=false`、`llm.value={"providers":{}}`；`llm` 的 schema 里 `providers.<任意 route>.retryPolicy` 可解析（dict，路由不需要预先枚举）。

## 9. 未实现 / 差异（如实登记）

1. **`llm` 命名空间的写持久化未接线**：宿主设置存储（`cmd/dshd/serve_settings.go`）只认 `model`/`permission`，写在 `internal/settingsform` 内回结构化 `read_only`+Hint（不静默丢弃、不假成功）。provider retryPolicy 目前的**真写入点仍是模型目录**（`models.catalog.upsert` 的 `model.retryPolicy`，`internal/llm/model_catalog.go:1117`），粒度是"每模型"而非官方"每 provider 路由"。要变成官方可写语义，需 serve 存储为 `llm` 增加读写分支（把 `providers.<route>.retryPolicy` 映射到目录/独立文档）——超出本任务写作用域，作为后续项。
2. **`llm` profile 只发布 `retryPolicy`**：`baseURL`/`apiKeyEnv`/`models` 等在宿主由模型目录承载，这里不假装它们可写（否则就是虚设表单）。
3. **官方客户端 `layoutOf(ns)` 只认 `llm-deepseek`/`llm-pi-ai`**（`ProviderEditor.tsx:162-166`）：宿主沿用既有 `settingsNs:"llm"`（`internal/llm/provider_registry.go:119`），官方桌面 UI 会把该命名空间当 `unknown` layout 从而不渲染 retry 编辑器；这是我们 UI 侧（`../dsh-ui`）与官方客户端的命名差异，宿主 schema 本身已按官方形状发布。若要对官方 UI 完全可比，需要改 `settingsNs` 或客户端映射（另一条线）。
4. **仓库缓存未做**：磁盘 LRU 淘汰、体积**轮询**中断（官方有 `sizePollMs` 轮询与 500MiB 缓存上限）、跨进程锁；已有的是每仓库进程内互斥（`lockFor`，官方 `locks` map 的最小版）+ 单次 fetch 后体积上限 + 单命令超时 + 失败分类。`--filter=blob:none` 懒取 blob 依赖服务器支持（官方同）。
5. **市场详情只读 README 常见文件名**（`README.md`/`readme.md`/`Readme.md`/`README`），未实现官方 `dsh-plugin-detail` 的包文档（`package.json`/`locale/*.json`）与详情图片路由。
6. `internal/market` 的 `Version.Source` 与 `FindVersion` 是 additive；`market.Plugin.Repository` 未改动（详情回退只用每版 git `source`）。

## 10. 最小验证命令（接线前即可跑）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
# 1) schema 节点发布 + 8 码候选 + 5e-324 哨兵 + 默认值 + 读/写路径校验
go test ./internal/settingsform/ -run 'TestRetryPolicy|TestDeclaredProviderNamespace|TestDefaultRegistryKeepsTwo' -count=1 -v
# 2) 市场 update 三态 + wire 显式字段 + 私有仓库详情回退
go test ./cmd/dshd/ -run 'TestMarket' -count=1 -v
# 3) SSH 回退真实 git（https 401 → ssh:// 重试成功 → remote 固定为 SSH）
go test ./internal/pluginmgr/official/ -run 'TestSshURLOf|TestRepositoryCache' -count=1 -v
```

接线后补一条端到端（Lead 侧）：起 `dshd serve --port 0`（临时 home）→ `POST /api/settings.describe` → 断言 `namespaces` 含 `llm` 且 `schema.refs` 里 `providers.<route>.retryPolicy` 为 union（同一份断言可复用 `internal/settingsform/retry_policy_schema_test.go` 的 `envelopeNode` 逻辑）。

## 11. 改动文件清单

| 文件 | 改动 |
|---|---|
| `internal/settingsform/provider_schema.go`（新） | 官方 retryPolicy schema envelope（8 码/正下界/默认值）、envelope 解释器、`ValidateRetryPolicyValue`、`ProviderNamespaceValidator`、`read_only` 错误、`buildProviderNamespaceSpec` |
| `internal/settingsform/registry.go` | `NamespaceSpec.Schema/Base/Validate`、注册期默认值校验、`HostRegistry()`（`llm` 命名空间）、`cloneNamespaceSpec` 深拷贝 |
| `internal/settingsform/settingsform.go` | `readOrigin`（声明式回落 + 读路径校验）、`makeView(persisted)`（只读投影）、`Mutate` 多段 path op、写路径 `read_only` 映射、`validateUser` schema 分支、`deepClone*`/`setNestedValue`/`deleteNestedValue`、`schemaFor` 声明优先 |
| `internal/settingsform/settingsform_test.go` | `makeView` 两个调用点补 `persisted=true`（无断言放宽） |
| `internal/settingsform/retry_policy_schema_test.go`（新） | 13 个测试：路径/候选序/哨兵/默认值/空码拒绝/非正延迟/自定义码接受/always 残留字段/只读写入/可写持久化/注册 fail-loud/默认 registry 不变 |
| `internal/market/store.go` | `Version.Source *Source`（additive）、`type Source`、`FindVersion` |
| `internal/pluginmgr/official/repository_cache.go`（新） | 官方 repository-cache 移植：bare+promisor 缓存、gitEnv、`SshURLOf`、`denied`→SSH 重试一次、失败合并诊断/归类、超时/体积上限 |
| `internal/pluginmgr/official/repository_cache_test.go`（新） | 6 个测试（真实 git + 真实 401 服务器 + 替身 ssh 的 upload-pack） |
| `internal/transport/handlers_plugin_market_official.go` | 仅 `OfficialMarketPluginVersion.Update bool \`json:"update"\`` + 注释（Lead 已批准；无其它改动） |
| `cmd/dshd/serve_plugin_market.go` | `marketInstallSpec`/`marketVersionUpdatable`/`installedGitOrigin`、版本行 `update`、详情 git 仓库回退与四字段错误投影、bundles/repositories 接缝 |
| `cmd/dshd/serve_plugin_market_test.go`（新） | 6 个测试：spec 形状、update 三态、wire 显式字段、仓库回退、失败四字段、无 git 来源 none |
