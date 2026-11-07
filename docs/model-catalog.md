# HOST-MODEL-CATALOG · 官方模型目录管理宿主竖切

> 基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 任务开始时共享工作树（含 W49/W50/W56/W58/W59/W61）。未 commit/push，未启动 GUI。
>
> 范围：持久模型目录的 list/upsert/reorder/test/set-active 五个 additive HTTP 方法，以及 active model id 到 W61 新会话默认、两个 runner 与重开的闭环。不是官方整个设置中心或 UI 已完成声明。

## 1. 官方 gap 与既有真值核对

核对来源：`../dsh-ui/docs/settings-gap-0.2.2.md` §4 #5、§5.5，以及其保存的官方 `dsh-v0.2.2-rc.1` `ui-settings-models` 源码。

- 官方 `DeepSeekModelsEditor.tsx` 的模型行是开放 object：已知字段包括 `id/name/contextWindow/maxTokens/input/reasoningEfforts`，编辑器未认识的字段必须保留。
- 官方 `CustomProviderCard.tsx` 的 endpoint 字段是 `api/baseURL/apiKeyEnv`；credential 经独立写入路径保存，不能作为普通模型字段回读。
- 官方 `ModelListEditor.tsx` / `useModelRows.ts` 要求完整参数与顺序保存，排序只移动行，不重建并丢掉未提交字段。
- 本任务前 canonical `models.list` 是 `internal/transport/handlers_jobs.go` 的网关转发：只回 `{id,owned_by}`，由 `llm.Client.ListModels` 请求 `GET /v1/models`。它是 canonical 25 之一，本任务**没有改它的方法、参数或结果**。
- W61 的默认模型唯一存储已经是 `state/session-model.json`：`\u0000settingsFormDefault` 保存默认覆盖，`\u0000settingsFormRevision` 保存乐观锁；`session.create` 把创建时有效值物化到该会话原有 override，`modelOverrideLoopRunner` 与 `modelRunner` 每轮都经 `resolveServeModel` 读取。

因此新增方法命名为 `models.catalog.list/upsert/reorder/test/set-active`，独立 snapshot/golden；active 不写入 catalog 文件，不制造第二份默认模型真值。

## 2. HTTP 契约

全部遵循 `POST /api/<method>`、裸参数对象、成功 `{result:…}`、失败 `{error:{code,message,hint,retryable}}`。Transport 在既有 cookie 认证之后分派；匿名五个方法均为 401，handler 不会先读参数或触碰存储。`params` 包装、未知顶层参数、错误 JSON 类型与显式 null 可选参数均 fail-loud。

| method | 裸参数 | 成功 result |
|---|---|---|
| `models.catalog.list` | `{}` | `{revision,models,credentials,active}` |
| `models.catalog.upsert` | `{model,credential?,expectedRevision?}` | 写后的 CatalogView |
| `models.catalog.reorder` | `{ids,expectedRevision?}` | 写后的 CatalogView |
| `models.catalog.test` | `{id,baseURL?,api?,credential?}` | `{id,ok,model?,latencyMs}` |
| `models.catalog.set-active` | `{id,expectedRevision?}` | `{activeModel,activeRevision,revision}` |

`CatalogView`：

- `revision` 是目录持久乐观锁；upsert/reorder 的 `expectedRevision` fence 此值。
- `models` 是有序 raw JSON object 数组，完整字段与 object key 顺序随存储 round-trip。
- `credentials` 以 `apiKeyEnv` 为键，只含 `{configured,writable}`；stored credential 或同名环境变量存在时 configured=true，literal 永不下发。
- `active={id,revision}` 是 W61 当前有效默认及其 revision 的只读投影，不是第二份存储。

`set-active` 的 `expectedRevision` fence 的是 **W61 model revision**；结果 `activeRevision` 是写后的 W61 revision，顶层 `revision` 是未改变的目录 revision。`test` 不写目录也不推进任何 revision。

机器夹具：

- `internal/transport/testdata/model-catalog.snapshot.json`：五方法、参数、类型、存储、错误码；Transport 测试同时断言 canonical 快照仍恰 25 个方法。
- `internal/transport/testdata/model-catalog-golden.json`：真实 dshd 执行的 6 个接受步骤与 13 个拒绝步骤；另外真实执行 wrong credential 与空目录两条拒绝，共 15 条。

夹具 SHA-256：snapshot `ab147c122483bf643edd1ec540dc02fd84e431fea590fdc60914d30c62c3d326`；golden `748889921857ee09644d5a95c9fc00b3cfa1359987105bbc1109855ecfd95c6d`。

## 3. 持久化与完整字段 round-trip

目录文件是 `<home>/state/model-catalog.json`，格式：

```json
{
  "version": 1,
  "revision": 4,
  "models": [
    {"id":"model-b","name":"Beta","futureB":{"preserved":true}},
    {"id":"model-a","name":"Alpha Two","futureScalar":42}
  ],
  "credentials": {"LOCAL_MODEL_CATALOG_KEY":"<write-only literal>"}
}
```

实际文件是紧凑 JSON，写入权限 0600。credential 与目录在同一个临时文件 + rename 原子提交中保存；它不是加密保险库，只是不会在 Transport 响应中回显。未被任何模型引用的 stored credential 在下一次 upsert 中清除。环境变量只用于 configured 判定与 test，不会写入文件。

### upsert

- 新 id 追加到末尾；同 id 在原位置做部分更新。
- 已有字段保持原 key 位置；请求中的新 key 按请求顺序追加。未提交的已知字段、未知 object/array/scalar 字段全部保留。
- 可省略的可选字段显式 `null` 表示删除；未知字段的 null 是普通 JSON 值，原样保留。
- `id` 必须是非空、首尾无空白、无控制字符、UTF-8 不超过 256 字节的 string；同 id 是更新，磁盘上重复 id 或 reorder 重复 id 均拒绝。
- `name` 非空 string；`contextWindow/maxTokens` 正整数；`input` 是无重复 `text/image` 数组（空数组也保留）；`reasoningEfforts=false` 或含至少一个非 off level 的 object，wire value 必须非空 string，off 另允许 null。
- `api` 当前只收 `openai-completions`；`baseURL` 是绝对 http(s) URL，不允许 userinfo/query/fragment；`apiKeyEnv` 是 POSIX shell identifier。
- `apiKey/credential/secret/token` 不得混入 model object，避免秘密被当成普通未知字段回显。secret 只能走顶层 `credential`。
- 重复 JSON key、非 object、额外 JSON value、非法已知字段都不改存储。

### reorder

`ids` 必须是非空且与当前目录完全相同的 id 排列：不多、不少、不重复。实现只移动 raw `json.RawMessage` slice，不 unmarshal/rebuild 模型行；测试逐行比较 reorder 前后 raw bytes，并在重开后再次比较。

### revision、写失败与损坏

upsert/reorder 在同一把 store 锁内检查 revision、构造完整下一状态并原子写盘；冲突或写盘失败不推进 revision、不改内存。启动读盘发现损坏、重复 id、非法字段时，list/upsert/reorder/test/set-active 全部失败，原文件不被空目录覆盖。

空目录是合法的初始状态：list 返回 `models=[]`，首次 upsert 可新增；空目录 reorder 返回 409 `empty_catalog`，test/set-active 因 id 不存在失败。非空目录收到 `ids=[]` 返回 400 `empty_order`。

## 4. test 只报告真实本地结果

`models.catalog.test` 对指定 id 发真实 `POST <baseURL>/chat/completions`：

```json
{"model":"<id>","stream":false,"max_tokens":1,"messages":[{"role":"user","content":"Reply with OK."}]}
```

credential 优先级为：本次调用的非空顶层 credential → 目录 stored credential → `apiKeyEnv` 同名环境变量。声明了 `apiKeyEnv` 但三种来源都为空时，在发请求前返回 `credential_missing`。未声明 `apiKeyEnv` 的本地 keyless endpoint 可以测试。

只有 HTTP 200 且 JSON 含 `choices[0].message.role="assistant"` 才返回 `ok=true`；连接失败、非 200、非法 JSON、缺 choices/message 都是 502 `upstream_error`。baseURL/api 配置错误是 400 `invalid_config`。响应只带请求 id、上游实际 model（若提供）与耗时；错误文本会替换 credential literal，不返回 Authorization 或完整响应正文。

验证使用 `httptest` OpenAI 兼容端点，真实检查 Authorization、path、model、stream=false、max_tokens=1，并返回本地 assistant 响应；未调用外部模型。

## 5. set-active 与旧会话/重开语义

`set-active` 先确认目录中确有该 id，再在 W61 同一 `modelOverrideStore.mu` 内检查 expected revision、写 `\u0000settingsFormDefault` 并推进 `\u0000settingsFormRevision`。写失败沿用 W61 回滚；catalog 文件没有 active 字段。

- `session.create` 继续物化创建当时的有效默认；旧会话已有 override 不被 retroactive 改写。
- 默认子进程 runner 的下一轮 argv 与新会话一致；`DSHD_SERVE_MODEL_RUNNER=1` 的真实 OpenAI 请求也携带同一 model id。
- dshd 重开后，catalog revision/order/完整字段与 W61 active/revision 均从原文件恢复；旧会话仍用旧值，重开后新会话用 active。
- A/B 两个临时 home 完全隔离。

**provider 边界**：当前两个 runner 的 endpoint/credential 仍由既有 `DSHD_LLM_BASE/DSHD_LLM_KEY` 决定。本任务证明 active **model id** 到 runner/真实请求，不声称已把每行 baseURL/apiKeyEnv 接成官方多 provider 路由，也不声称 UI 已消费五方法。

## 6. 错误码

| 情况 | HTTP / code |
|---|---|
| 匿名 | 401 / `unauthorized` |
| 非裸对象、params 包装、未知参数、ids 缺失/数量不符 | 400 / `bad_request` |
| 空/空白/控制字符/超长 id | 400 / `invalid_id` |
| 非法已知字段、秘密混入 model、重复 JSON key | 400 / `invalid_field` |
| test 缺合法 endpoint 或 api 不支持 | 400 / `invalid_config` |
| 声明的 credential 未配置 | 400 / `credential_missing` |
| 非空目录收到空 ids | 400 / `empty_order` |
| 空目录 reorder | 409 / `empty_catalog` |
| 重复 id | 409 / `duplicate_id` |
| catalog/W61 revision 过期 | 409 / `revision_conflict` |
| test/set-active 的 id 不存在 | 404 / `not_found` |
| 本地连接、HTTP 或 OpenAI 响应失败 | 502 / `upstream_error` |
| 损坏存储、读写失败 | 500 / `internal`，retryable=true |

## 7. 本次实际执行证据

证据目录：`C:/Users/Administrator/AppData/Local/Temp/model-catalog-evidence/`。以下命令均由本任务亲自执行；日志保留原样输出。

| 日志 | 实际命令 | 退出码 | 结果 |
|---|---|---:|---|
| `01-red-before-implementation.log` | `go test ./internal/llm ./internal/transport ./cmd/dshd -run 'TestModelCatalog' -count=1 -v` | 1 | 实现前真红：ModelCatalogView/OpenModelCatalog/ModelCatalogMethods/ModelCatalogHandler/newModelCatalogHTTPHandler 均不存在，三包 `[build failed]` |
| `04-core-third-attempt.log` | `go test ./internal/llm -run 'TestModelCatalog' -count=1 -v` | 0 | round-trip、未知字段保序、冲突、坏 id/重复/空目录/非法字段、写失败回滚、真实 test 与 credential redaction 全 PASS |
| `06-real-dshd-first-attempt.log` | `go test ./cmd/dshd -run 'TestModelCatalogRealDSHDAuthenticatedGoldenABRestartAndRuntime' -count=1 -v` | 0 | 真 ready token/cookie、五方法、A/B、15 条真实拒绝、重开与四条真实模型请求全 PASS |
| `05-loop-runner-first-attempt.log` | `go test ./cmd/dshd -run 'TestModelCatalogSetActiveReachesLoopRunnerWithoutRetroactiveRewrite' -count=1 -v` | 0 | 旧会话 base-model、新会话与重开 loop-active 三条真实 argv 全 PASS |
| `07-targeted-with-w61.log` | `go test ./internal/llm ./internal/transport ./cmd/dshd -run 'TestModelCatalog\|TestSettingsForm\|TestSettingsDefaults\|TestSessionAPISnapshot' -count=1 -v` | 0 | 新功能与 W61/cookie/canonical 联合回归全 PASS |
| `08-full-go-test.log` | `go test ./... -count=1` | 0 | 全仓测试全绿；`cmd/dshd 48.853s`、`internal/llm 3.852s`、`internal/transport 1.163s` |
| `09-related-race.log` | `CGO_ENABLED=1 go test -race ./internal/llm ./internal/transport ./cmd/dshd -run 'TestModelCatalog' -count=1` | 0 | MinGW-w64 GCC 16.2.0；新目录/Transport/真实 dshd 测试 race 全绿，无 DATA RACE |
| `10-full-vet-build.log` | `go vet ./... && echo GO_VET_OK && go build ./... && echo GO_BUILD_OK` | 0 | 原样输出 `GO_VET_OK`、`GO_BUILD_OK` |
| `11-full-related-package-race.log` | `CGO_ENABLED=1 go test -race ./internal/llm ./internal/transport ./cmd/dshd -count=1` | 0 | 三个相关包完整 race 全绿：`internal/llm 1.654s`、`internal/transport 2.113s`、`cmd/dshd 235.112s`，无 DATA RACE |
| `12-mutation-guards-must-red.log` | 独立副本删除 credential redaction 后跑核心负例 | 1 | 真红：`upstream credential error leaked ... wrong-secret`，证明回显断言能抓回归 |
| `13-mutation-real-duplicate-must-red.log` | 独立副本删除 duplicate guard 后跑真实 dshd golden | 1 | 真红：duplicate reorder 实测 HTTP 200、want 409，证明重复 id 失败分支不是假绿 |

### 7.1 先红原样片段

```text
internal\llm\model_catalog_test.go:29:44: undefined: ModelCatalogView
internal\llm\model_catalog_test.go:43:11: undefined: OpenModelCatalog
FAIL	dshgo/internal/llm [build failed]
internal\transport\server_model_catalog_test.go:18:16: undefined: ModelCatalogMethods
internal\transport\server_model_catalog_test.go:30:30: unknown field ModelCatalogHandler in struct literal of type Options
FAIL	dshgo/internal/transport [build failed]
cmd\dshd\serve_models_test.go:337:35: undefined: newModelCatalogHTTPHandler
FAIL	dshgo/cmd/dshd [build failed]
FAIL
[exit code: 1]
```

### 7.2 失败分支真实响应原样片段

来自 `06-real-dshd-first-attempt.log`，不是手写预期 JSON：

```text
MODEL_CATALOG_REJECT_duplicate reorder {"error":{"code":"duplicate_id","hint":"每个模型 id 只能出现一次；修复重复项后重试","message":"reorder 含重复 id \"model-a\"","retryable":false}}
MODEL_CATALOG_REJECT_empty order {"error":{"code":"empty_order","hint":"ids 必须包含目录全部模型，不能发送空数组","message":"reorder ids 不能为空","retryable":false}}
MODEL_CATALOG_REJECT_stale upsert {"error":{"code":"revision_conflict","hint":"重新 models.catalog.list，基于最新 revision 再提交","message":"模型目录 revision 已变化：expected=3 actual=4，未覆盖新值","retryable":false}}
MODEL_CATALOG_REJECT_wrong credential {"error":{"code":"upstream_error","hint":"这是本地端点/协议的真实失败；检查 OpenAI 兼容端点、模型 id 与 credential","message":"模型测试端点回 401：{\"error\":\"received Bearer [redacted]\"}","retryable":true}}
MODEL_CATALOG_REJECT_empty catalog {"error":{"code":"empty_catalog","hint":"先 upsert 至少一个模型；空目录只允许 list 与首次新增","message":"模型目录为空，不能 reorder","retryable":false}}
```

独立变异副本还去掉了两处守卫，确认测试不是只会 PASS：

```text
model_catalog_test.go:310: upstream credential error leaked or wrong code: 模型测试端点回 401：{"error":"bad credential wrong-secret"} code=upstream_error
--- FAIL: TestModelCatalogTestUsesRealOpenAIEndpointAndNeverEchoesCredential (0.05s)

serve_models_test.go:228: POST models.catalog.reorder status=200 want=409 body={"result":{"revision":5,...}}
--- FAIL: TestModelCatalogRealDSHDAuthenticatedGoldenABRestartAndRuntime (2.54s)
FAIL
[exit code: 1]
```

变异只发生在 `C:/Users/Administrator/AppData/Local/Temp/model-catalog-mutation-check/`，未改共享实现。

### 7.3 runner 与重开原样片段

```text
MODEL_CATALOG_RUNTIME session=fe6114a1-716b-2476-7a2b-1e2613507892 model=model-a
MODEL_CATALOG_REOPENED_RUNTIME session=a12b3021-a018-5062-73a6-39593b678534 model=group/deepseek-v4.1-flash1017
MODEL_CATALOG_REOPENED_RUNTIME session=539e4904-5373-812b-8695-32dd7c40e6d2 model=model-a
MODEL_CATALOG_GOLDEN_SUMMARY OK=6 FAIL=0
MODEL_CATALOG_REJECT_SUMMARY OK=15 FAIL=0
MODEL_CATALOG_RUNTIME_SUMMARY OK=4 FAIL=0
--- PASS: TestModelCatalogRealDSHDAuthenticatedGoldenABRestartAndRuntime (3.64s)
PASS
ok  	dshgo/cmd/dshd	3.736s
```

loop runner 证据：

```text
spawn pid=27992 argv=[run ... --session session-old ... --model base-model]
spawn pid=38816 argv=[run ... --session session-new ... --model loop-active]
spawn pid=31716 argv=[run ... --session session-reopened ... --model loop-active]
MODEL_CATALOG_LOOP_SUMMARY OK=3 FAIL=0
```

canonical 四夹具在任务前后 SHA-256 逐项 `OK`；`TestSessionAPISnapshot` 与 additive snapshot 测试均 PASS，未改 canonical 25、WS/journal 或共享 golden。

## 8. 未验项与交付边界

- 未启动 GUI，未改 UI/apiclient/pluginapi/dssettings；不能把这五个宿主方法称为可见官方设置页完成。
- 未接 per-model provider 路由到两个 runner；当前只证明 active model id，endpoint/credential 仍按既有进程环境。
- 真实测试只打本地 httptest 端点，未调用外部模型、未做完整工作日或性能基准。
- 未新增 WS type、通知推送或 CLI 子命令；客户端在写响应中取得新 revision，重连后重新 list。
