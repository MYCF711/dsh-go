# HOST-SETTINGS-FORM · 通用 ConfigForm 宿主竖切

> 基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 任务开始时共享工作树（含 W34/W38/W49/W50/W56/W58）。未 commit/push，未启动 GUI。
>
> 范围：只实现宿主通用 ConfigForm 与两个真实默认设置，不声称官方设置中心所有页面已经完成。

## 1. 官方形状核对

核对来源是 `../dsh-ui/docs/settings-gap-0.2.2.md` 与其保存的官方 `dsh-v0.2.2-rc.1` 源码：

- `ui-settings/src/client/config-form.ts`：`remote.settings.describe()` 读取命名空间；`remote.settings.mutate(namespace, ops, revision)` 写入并返回 `SettingsNamespaceView`。
- `config-form-types.ts`：描述符含 `value/base/user/revision/writable`；`set` 是 `{op:"set",path:[field],value}`，`unset` 是 `{op:"unset",path:[field]}`。
- `ui-permission-presets/src/client/settings-store.ts`：官方权限行写 `permission.defaultPreset`，语义是**后续新会话默认**；当前会话权限仍是另一入口。

本宿主因此同时提供官方 `settings.describe/settings.mutate` 和显式通用表单 `settings.form.describe/get/set/reset`。六个方法都在独立扩展面，`SessionMethods()` 与 `api.snapshot.json` 的 canonical 25 未增加、未改名、未改字段。

## 2. HTTP 契约

全部使用现有 `POST /api/<method>`、裸参数对象、成功 `{result:…}`、失败 `{error:{code,message,hint,retryable}}`。新 handler 在现有 cookie 认证之后分派；匿名调用六个方法均为 401，handler 不会先读取参数或触碰存储。`params` 包装、未知顶层字段、未知 op 字段和错误 JSON 类型均拒绝。

| method | 裸参数 | 成功 result |
|---|---|---|
| `settings.describe` | `{}` | `{writable,hasDocument,namespaces,sections,errors}` |
| `settings.mutate` | `{ns,ops,expectedRevision?}` | 一个 `SettingsNamespaceView` |
| `settings.form.describe` | `{}` | 同 `settings.describe` |
| `settings.form.get` | `{ns}` | 一个 section |
| `settings.form.set` | `{ns,path,value,expectedRevision?}` | 提交后的 section |
| `settings.form.reset` | `{ns,path?,expectedRevision?}` | 清字段/整个 namespace 后的 section |

`NamespaceView` 字段为 `ns/schema/value/base/user?/autoGenerate/applies/secrets/revision`。`schema` 使用官方客户端测试同类的 schemastery envelope；`applies="live"` 指后续新会话创建时消费，不是 retroactive 修改旧会话。`hasDocument=false`，本任务没有伪造原生设置文档。

每个 section 为 `{ns,title,writable,revision,fields,errors}`。每个 field 提供：

- `name/path/type/current/default/overridden/writable`；
- 适用时的 `enum` 或 `minLength/maxLength`；
- section 与顶层成功响应的 `errors` 都是 `[]`。失败不走成功 result，而走 HTTP 四字段错误。

独立 additive 夹具：

- `internal/transport/testdata/settings-form.snapshot.json`：方法、参数、类型、存储、错误码；测试核对六个方法且 canonical 快照仍恰 25 个。
- `internal/transport/testdata/settings-form-golden.json`：真实 dshd 进程逐项执行的接受/拒绝序列，不是未使用的装饰文件。

夹具 SHA-256：snapshot `5800abbeb446dd88349a7061c9127effb8acddd1114982088e92aef7309b9e38`；golden `dac7c05db509d77ec3acb5d39a6c49b3a310d835e3ba3e8b8fbfb4860fe679b1`。独立解析核对原样输出：`SETTINGS_FIXTURES_OK canonical=25 additive=6 accepted=4 rejected=13`。

## 3. 首切两个真实设置

| ns / field | 类型与约束 | 启动默认 | 真实存储与运行消费 |
|---|---|---|---|
| `permission.defaultPreset` | string；`deny/ask/allow/auto` | `dshd serve --policy`，当前为 `allow` | `state/session-permission.json`；新会话 `session.load.permission` 与下一轮子进程 `--policy` |
| `model.defaultModel` | string；非空白，UTF-8 1..256 字节 | `dshd serve --model`，当前为 `group/deepseek-v4.1-flash-1017` | `state/session-model.json`；`resolveServeModel` 与真实 OpenAI 兼容请求的 `model` |

没有使用 `state/ui-config.json`，没有新建平行配置值。两个既有 map 中增加两个不可能与 session id 冲突的保留键：

- `\u0000settingsFormDefault`：默认覆盖；删除它表示继承启动默认。
- `\u0000settingsFormRevision`：与该默认覆盖同一次原子提交的持久 revision。

`session.create` 在创建后读取当时的有效默认，并总是物化到新会话原有的 session override 键（包括尚未显式覆盖的启动默认）。之后：

1. 既有 `modelOverrideLoopRunner` 仍优先使用会话覆盖并传子进程 argv；
2. `modelRunner` 仍经 `resolveServeModel` 取会话值；
3. 改默认只影响之后创建的会话，旧会话不被 retroactive 修改；
4. reset 清默认覆盖、恢复启动默认，但不清旧会话的已物化值。

revision 按 namespace 独立持久化并递增。`expectedRevision` 在既有 store 锁内与写入一起原子检查；冲突返回 409 `revision_conflict`，不会覆盖新值。多 namespace 不是一个跨文件事务。写盘失败恢复内存旧 map 并返回 `internal`；启动时既有 JSON 读盘失败也会让 describe/get/set/create 明确失败，不拿空 map 冒充默认值。

## 4. 校验与错误

| 情况 | HTTP / code |
|---|---|
| 匿名 | 401 / `unauthorized` |
| 非裸对象、`params` 包装、未知参数、null ops、未知 op/字段类型 | 400 / `bad_request` |
| 空、嵌套、null path | 400 / `invalid_path` |
| 未知设置 field | 400 / `unknown_field` |
| permission 非枚举值 | 400 / `invalid_enum` |
| model 空白或超过 256 字节 | 400 / `out_of_range` |
| value 非 string | 400 / `invalid_type` |
| 未知 namespace | 404 / `not_found` |
| revision 过期 | 409 / `revision_conflict` |
| 持久存储损坏/写盘失败 | 500 / `internal`，`retryable=true` |

`settings.form.reset` 只有**省略** path 才清整个 namespace；显式 `path:null` 拒绝，避免把坏请求放大成全表清空。所有拒绝都在写存储之前完成；真实 golden 还断言失败后当前值不变。

## 5. 本次实际执行证据

证据目录：`C:/Users/Administrator/AppData/Local/Temp/settings-form-evidence/`。以下均为本任务亲自执行，不是历史记录。

| 日志 | 实际命令 | 退出码 | 结果 |
|---|---|---:|---|
| `01-red-before-implementation.log` | `go test ./internal/settingsform ./internal/transport -run 'TestDescribeGetSetMutateResetAndReopen|TestSettingsFormHandlerMountedAfterCookieAuthentication' -count=1 -v` | 1 | 实现前真实红：`undefined Document/New`、`unknown field SettingsFormHandler`，两包 `FAIL [build failed]` |
| `04-settings-targeted-final-pre-full.log` | `go test ./cmd/dshd ./internal/settingsform ./internal/transport -run 'TestSettings|TestDescribe|TestValidation|TestRevision' -count=1 -v` | 0 | 核心、校验、写失败回滚、启动损坏、cookie、快照与真实进程全 PASS |
| `10-final-full-go-test.log` | `go test ./... -count=1` | 0 | 当前文件状态全仓所有包通过；`cmd/dshd 104.157s`、`internal/settingsform 0.373s`、`internal/transport 1.376s` |
| `06-full-vet-build.log` | `go vet ./... && go build ./...` | 0 | 全仓 vet/build 通过（命令无成功正文） |
| `08-real-serve-final.log` | `go test ./cmd/dshd -run 'TestSettingsFormRealDSHDAuthenticatedGoldenABRestartReset|TestSettingsDefaultsReachExistingTurnRunnerAcrossABResetAndReopen' -count=1 -v` | 0 | 真 ready token/cookie、A/B、get/set、两次重开、reset、真实 runner argv、真实本地 SSE 模型请求与 turn/done 全 PASS |
| `09-final-vet-build-markers.log` | `go vet ./... && echo GO_VET_OK && go build ./... && echo GO_BUILD_OK` | 0 | 原样输出 `GO_VET_OK`、`GO_BUILD_OK` |
| `11-final-related-race.log` | `CGO_ENABLED=1 go test -race ./internal/settingsform ./internal/transport ./cmd/dshd -count=1` | 0 | MinGW-w64 GCC 16.2.0；原样输出 `ok dshgo/internal/settingsform 1.224s`、`ok dshgo/internal/transport 2.505s`、`ok dshgo/cmd/dshd 279.841s`，无 DATA RACE |

### 5.1 先红原样片段

```text
internal\settingsform\settingsform_test.go:11:24: undefined: Document
internal\settingsform\settingsform_test.go:48:9: undefined: New
FAIL	dshgo/internal/settingsform [build failed]
internal\transport\server_settings_form_test.go:29:30: unknown field SettingsFormHandler in struct literal of type Options
FAIL	dshgo/internal/transport [build failed]
FAIL
[exit code: 1]
```

### 5.2 失败分支真实响应原样片段

以下来自 `04-settings-targeted-final-pre-full.log`，不是手写的预期 JSON：

```text
GOLDEN_REJECT_bad enum {"error":{"code":"invalid_enum","hint":"只提交 field.enum 原样列出的值","message":"permission.defaultPreset 的值 \"block\" 不在 [deny ask allow auto] 中","retryable":false}}
GOLDEN_REJECT_out of range {"error":{"code":"out_of_range","hint":"缩短模型 id 后重试","message":"model.defaultModel 长度 257 超过上限 256 字节","retryable":false}}
GOLDEN_REJECT_unknown field {"error":{"code":"unknown_field","hint":"只修改 settings.describe 返回的字段","message":"permission 没有字段 \"missing\"","retryable":false}}
GOLDEN_REJECT_nested path {"error":{"code":"invalid_path","hint":"使用 describe 中 field.path 的原样单段路径","message":"设置路径必须是单段非空字段，收到 [defaultPreset nested]","retryable":false}}
REAL_REVISION_CONFLICT {"error":{"code":"revision_conflict","hint":"重新 settings.describe/get，基于最新 revision 再提交","message":"permission revision 已变化：expected=2 actual=3，未覆盖新值","retryable":false}}
```

### 5.3 运行时同一存储原样片段

来自 `08-real-serve-final.log`：

```text
spawn pid=32524 argv=[run --home C:\Users\ADMINI~1\AppData\Local\Temp\TestSettingsDefaultsReachExistingTurnRunnerAcrossABResetAndReope1196403316\001 --session session-a --prompt next-prompt --policy deny --max-steps 6 --model model-A]
spawn pid=41148 argv=[run --home C:\Users\ADMINI~1\AppData\Local\Temp\TestSettingsDefaultsReachExistingTurnRunnerAcrossABResetAndReope1196403316\001 --session session-b --prompt next-prompt --policy ask --max-steps 6 --model model-B]
    serve_settings_test.go:109: SETTINGS_RUNNER_SUMMARY OK=1 FAIL=0
    serve_settings_test.go:381: REAL_RUNTIME_MODEL session=4c7cb3bd-659d-bc59-94e7-87557c175624 model=runtime-settings-model
    serve_settings_test.go:405: REAL_RUNTIME_TURN_DONE OK=1 FAIL=0
    serve_settings_test.go:406: SETTINGS_FORM_REAL_SUMMARY OK=1 FAIL=0
--- PASS: TestSettingsFormRealDSHDAuthenticatedGoldenABRestartReset (4.88s)
PASS
ok  	dshgo/cmd/dshd	5.497s
```

真实进程测试同时断言：A home 设置不改变 B home；新会话 `session.load.permission=deny`；模型 session override 为 `settings-model-A`；set 后重开仍为 revision 3；reset 后恢复启动值并再次重开仍为 revision 4。测试只回收自己启动的进程，home/端点全部临时化。

## 6. 边界与未验项

- 未启动 GUI、未改 UI；UI apiclient/pluginapi/dssettings 消费是后续任务，不能把宿主夹具称为可见设置页完成。
- 真实模型请求使用本地 OpenAI 兼容 SSE 端点，证明 dshd 实际请求模型与 turn 完成；未调用外部模型、未做完整工作日或真实工具审批长跑。
- 首切只有 model/permission 两个 namespace。agent-loop、shell、subagent、web-search、session-log 没有用空 schema 冒充已实现。
- 未新增 WS type 或 document-updated 推送；客户端可在写响应中折叠描述符，重连后重新 describe。
- 未提供新的 CLI 子命令；现有 CLI flag 继续作为启动默认来源，HTTP 复用真实存储与运行链。
