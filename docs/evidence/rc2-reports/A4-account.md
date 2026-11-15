# RC2-A4 · account 通知三件套 + 两条删除方法兼容 + 9 条 account 契约变更

- 任务板：`task-4`（owner `a4-account`）
- 工作目录：`D:/DeepSeek Harness/default-workspace/DSH-GO/.agents/worktrees/dsh-bdb66fbf-95ba`
- 官方基线：`0.2.2-rc.2`；本仓版本 `0.2.2-rc.2-gobeta.1`（未改版本号）
- 结论：**GO**。三件套逐字段实现、数据来源=云端（有官方 file:line）、两条 deprecated 方法保留且行为真实、9 条契约变更全部有确定结论、测试红→绿可复现。
- 唯一未验证项：`-race`（本机无 C 工具链，见 §10）。

---

## 1. 官方证据（只读，file:line 已核）

官方 rc.2 把账号能力整包搬进 `dsh-deepseek-account`，`dsh-api-account-controller` 与 `dsh-deepseek-account-platform` 两包删除。

| 内容 | 官方位置 |
|---|---|
| `account/getNotifications` 描述符 | `dsh-deepseek-account/lib/typert.host.js:382`（zod 定义 :70-77） |
| `account/getNotificationContent` 描述符 | `lib/typert.host.js:333`（zod 定义 :43-52） |
| `account/reportNotificationEvent` 描述符 | `lib/typert.host.js:476`（zod 定义 :133-144；事件枚举在 :136） |
| `account/watch` 描述符（结果 = AccountUpdate） | `lib/typert.host.js:640-651`；jSONSchema 明确 `required:["type","state"]` |
| `account/watchExpiry` 描述符 | `lib/typert.host.js:658-668`；jsonSchema `{"type":"string","const":"session-expired"}` |
| `AccountNotification` / `…Action` / `…Batch` / `…Event` 类型声明 | `lib/typert.host.js:833`、`:838`、`:843`、`:848` |
| `AccountUpdate` / `AccountView` 声明 | `lib/typert.host.js:857`、`:865` |
| `SignInAttempt.phase` 的 rc.2 字面量顺序 | `lib/typert.host.js:17`、`:125`、`:160`（`failed,cancelled,initializing,waiting-browser,exchanging,committing,succeeded,expired`） |
| 通知 zod 形状 + 逐条跳过 | `lib/types/notifications.js:13-26`（形状）、`:22-27`（project）、`:48-61`（list + 逐条 skip）、`:71-79`（content，id 必须相符）、`:87-89`（events 只需 `{acked:boolean}`） |
| `next_poll_after` 上界 | `lib/types/notifications.js:19` `z.number().positive().max(2_147_483_647/1_000)` |
| 三个 HTTP 端点 | `lib/types/protocol.js:123-124`（list）、`:135-136`（content?id=）、`:148-152`（events） |
| 信封 / biz_code / 40003 | `lib/types/protocol.js:82`（envelope）、`:222-231`（40003）、`:236`（biz_code≠0 → protocol） |
| 客户端身份头 | `lib/types/client-metadata.js:23-27`（desktop-win/mac）、`:34-45`（五个头）、`:50-58`（`zh→zh_CN`、其余 `en_US`） |
| `harnessOrigin` / `platformOrigin` 默认值 | `lib/index.js:696`（platform）、`:697`（harness） |
| 两个 origin 的解析/校验 | `lib/index.js:920-921` |
| 三方法实现（null/false 语义） | `lib/index.js:1034-1054`（getNotifications）、`:1057-1077`（getNotificationContent）、`:1081-1099`（reportNotificationEvent） |
| 三方法实际用的 origin | `lib/index.js:1040`、`:1063`、`:1087`（全部 `this.harnessOrigin`） |
| watch 的合并优先级 | `lib/index.js:1396-1420`（`observeUpdates`，credentials-changed 优先） |
| 官方升级指南（迁移步骤原文） | `dsh/docs/upgrade-guide/v0.2.2-rc.1/account-notification-fields/guide.md` |

---

## 2. 三个新方法：逐字段契约

### 2.1 `account.getNotifications`

参数（位置 0，唯一）：

| wire 名 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `client` | object | 是 | `{version:string, locale:string, timezoneOffsetSeconds:number}`，三项都必填、`additionalProperties:true`（严格侧 false） |

结果 `AccountNotificationBatch | null`：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `nextPollAfterSeconds` | number | 是 | `0 < x <= 2147483.647`（官方 `positive().max(2_147_483_647/1_000)`） |
| `notifications` | array | 是 | 服务端顺序；每项见下 |
| （item）`id` | string | 是 | 不透明 ID |
| （item）`createdAt` | string | 是 | 带时区 ISO 时间（官方 `z.iso.datetime({offset:true})`） |
| （item）`displayUntil` | string \| null | 是 | **null = 永不过期**，必须显式 null |
| （item）`title` / `body` | string | 是 | 已按 `x-client-locale` 本地化 |
| （item）`actions` | array | 是 | 每项 `{label:string, style:'primary'\|'secondary', target:{type:'app_route',name:'account'} \| {type:'dismiss'}}` |
| （item）`data` | JSON | 否 | 业务数据（赠金元数据搬到这里）；无值时不出现 |

错误码（本仓四字段）：`not_implemented`(501，无通知后端/未接线) / `bad_request`(400) / `internal`(500，云端不可达或协议坏)。

### 2.2 `account.getNotificationContent`

参数：`id`（位置 0，string，本仓要求非空）+ `client`（同上）。
结果 `AccountNotification | null`（字段与批次 item 完全同形）。官方语义：**不改消费状态**，所以实现只发一个 GET，宿主不写任何本地状态；返回的 `id` 必须与请求一致，否则协议错（`notifications.js:71-79`）。

### 2.3 `account.reportNotificationEvent`

参数：`id`(string) + `event`(`displayed|cta_clicked|dismissed`) + `client`。
结果：**裸 boolean**（`true` = 平台收下了这次事件）。
官方口径：`acked:true` 与 `acked:false` 都算上报完成（迁移指南第 2 条），是否消费由后端决定；失败不回滚本地卡片、不重试。
请求体为 `{notification_id, type}`（`protocol.js:148-152`）。

---

## 3. 数据来源：**云端拉取，没有本地缓存**（有 file:line）

`lib/index.js:1040 / :1063 / :1087` 三处方法体都直接调
`readNotifications/readNotificationContent/sendNotificationEvent(this.harnessOrigin, …)`，
而这三个函数是 `protocol.js:123/135/148` 的纯 HTTP 读取，官方包里**不存在任何本地通知存储**。

因此本仓：

| provider | `getNotifications` / `getNotificationContent` / `reportNotificationEvent` |
|---|---|
| **official + 已登录** | 真打 `harnessOrigin`：`GET /api/v0/users/notifications/list`、`GET …/content?id=`、`POST …/events`；带 `x-dsh-auth-token` 与五个 `x-client-*` 头 |
| **official + 未登录** | `null` / `null` / `false`（官方语义：signed out），且**不发任何请求**（无 profile preflight） |
| **official + 云端 401 / 业务码 40003** | 清掉本地凭据（`clearToken`）后回 `null`/`false`（官方 `expireCredential` + `return null`） |
| **official + 云端不可达 / 协议坏** | **四字段 `internal`(500)**，message 写明 harnessOrigin、方法、HTTP 状态；**绝不返回空列表** |
| **local / self-hosted** | **四字段 `not_implemented`(501)**，message 写明"官方通知只来自 harnessOrigin 的三个端点，本宿主在 local 模式下没有这个后端，要用通知请切 official（`DSHD_ACCOUNT_PROVIDER=official` + `DSHD_ACCOUNT_HARNESS_ORIGIN`）" |

`next_poll_after` 越界、`biz_code≠0`、信封缺 `biz_data`、content id 不符 → 一律报错（不是空结果）。
单条通知畸形/不支持 → **逐条跳过**（官方 `notifications.js:50-59` 行为），信封畸形才整体失败。这两条各有测试。

---

## 4. 9 条 `account.*` 结构契约变更（rc.1 → rc.2）对照

| # | 方法 | rc.1 | rc.2 | 本仓处理 | 结论 |
|---|---|---|---|---|---|
| 1 | `cancelSignIn` | `attempt.phase` 枚举 `initializing,waiting-browser,…,failed` | 同一集合，字面量顺序变为 `failed,cancelled,initializing,…` | 常量表按 rc.2 顺序重排（`handlers_account_official.go` 的 `OfficialSignInPhase*`），`OfficialSignInPhases()` 返回 rc.2 顺序 | 集合等价 ⇒ 校验行为不变；顺序已对齐并有测试 |
| 2 | `getBalance` | `typeSymbol …#account/getBalance:result`（account-controller） | 同名义，包名换 `dsh-deepseek-account` | 无代码改动 | **L1 无对应物**：`typeSymbol` 是 L2 typert 元数据，本仓 L1 wire 不含它（L2 未实现，不声称 wire 兼容） |
| 3 | `getProfile` | 同上 | 同上 | 无代码改动 | 同上 |
| 4 | `getState` | phase 枚举旧顺序 | phase 枚举 rc.2 顺序 | 同 #1 | 已对齐 |
| 5 | `hasRunningAccountTasks` | typeSymbol 换包 | typeSymbol 换包 | 无代码改动 | 同 #2 |
| 6 | `signOut` | phase 枚举旧顺序 | phase 枚举 rc.2 顺序 | 同 #1 | 已对齐 |
| 7 | `startSignIn` | 参数 `callbackOrigin`/`loginSource` 的 typeSymbol 换包；phase 枚举旧顺序 | 换包 + rc.2 顺序 | 同 #1（typeSymbol 部分同 #2） | 已对齐 |
| 8 | **`watch`** | 结果 = 扁平 `AccountView {status,links,attempt}` | 结果 = `AccountUpdate {type:'snapshot'\|'credentials-changed', state:AccountView}` | **真 wire 变更，已改实现**：`OfficialAccountUpdate` + `serveAccountWatch` 只发 `{type,state}`；`type` 非法即断流；rc.1 的 `AccountWatch` 绑定被包成 `type:"snapshot"`；provider 侧新增 `WatchUpdates` 并按"凭据写入/移除 = credentials-changed、初始订阅/登录进度 = snapshot"分类，合并时 credentials-changed 优先 | 已实现 + 测试（含 `handlers_account_parity_test.go` 的旧扁平断言改为新形状） |
| 9 | `watchExpiry` | `{"type":"string","const":"session-expired"}`（typeSymbol 换包） | 同 schema，typeSymbol 换包 | **发现既有缺陷并修复**：本仓原先订阅时先发一帧"基线"（到期时间戳或空串），**违反官方 const**（rc.1 也是同一个 const）。现改为：开流即写响应头 + flush，**只发 `session-expired`**；非该值的帧直接断流不发出 | 已修复 + 测试 |

> 附：#8/#9 之外，`typeSymbol` 换包（#2/#3/#5/#7/#9）在本仓 L1 全部**无对应物**——本仓 `/api` 成功信封只有 `{"result":…}`，不带 typert 的 `typeSymbol`。这 6 条（含 #7 的两个参数）登记为"确定结论：L1 不适用"，不是省略。

---

## 5. 两条被官方删除的方法：保留 + 理由

| 方法 | 官方替代 | 本仓处理 |
|---|---|---|
| `account.getUnnotifiedBonuses` | `account.getNotifications`（赠金元数据进 `notifications[].data`） | **保留**为兼容实现。official provider → 真打 `GET /api/v0/users/notifications/…` 以外的旧端点（`/api/v0/users/get_unnotified_bonuses`，仍保留）；未登录 → `null`（官方 rc.1 语义）；Local → 带 `source:"local"` 的真实本地状态。未接线时四字段 501，message 含 `deprecated` 与替代方法名 |
| `account.ackBonusNotified` | `account.reportNotificationEvent` | **保留**为兼容实现。official → 真打 `POST /api/v0/users/ack_bonus_notified` 并落盘回执；Local → 真写本地已读集合；未接线时四字段 501，message 含 `deprecated` 与替代方法名 |

理由：用户裁定的**能力下限规则**（只能比官方更多，不能更少）。官方删除不等于我们要删；但也不能"留个空壳"——所以两者都保持"要么真实、要么结构化 501 说清淘汰"，并由测试钉住（`TestOfficialAccountDeprecatedBonusMethodsStillServed`、`TestAccountDeprecatedBonusMethodsStayRealOrLoud`）。

注：`cmd/dshd/serve_account_api.go` 里还有一组 PARITY-ACCOUNT 之前的历史实现（`accountOfficialAPI.getUnnotifiedBonuses/ackBonusNotified/signOut` + `loadGrant/saveGrant`）。它们**从未被挂到任何回调**（只有 `discoverModels` 在用），不参与兼容路径；已在代码里加醒目注释说明，清理留作独立任务（见 §10）。

---

## 6. 代码落点

| 文件 | 改动 |
|---|---|
| `internal/account/notifications.go`（新） | `Notification`/`NotificationAction`/`NotificationBatch` 模型；`Notifications`/`NotificationContent`/`ReportNotificationEvent` 三个真实实现；`harnessOrigin` 客户端头（五个 `x-client-*`）；信封/biz_code/40003 判定；`next_poll_after` 上界；逐条跳过；`ErrNoNotificationBackend`/`ErrNotificationProtocol`/`ErrNotificationUnauthorized`/`ErrNotificationBadInput` |
| `internal/account/account.go` | `Options.HarnessOrigin` + `Service.harness`（默认 `DefaultHarnessOrigin`，env `DSHD_ACCOUNT_HARNESS_ORIGIN`）；`Update` 类型 + `UpdateSnapshot`/`UpdateCredentialsChanged`；`updateSub` + `notifyKindLocked`；`WatchUpdates`；`Provider` 接口加 4 个方法；`WatchExpiry` 去掉基线帧；凭据写入/移除点改为 `notifyKindLocked(UpdateCredentialsChanged)` |
| `internal/account/providers.go` | `doJSONRaw` 回传 HTTP 状态码（通知层要区分 401）；`completeOfficial`/`clearToken` 标 credentials-changed |
| `internal/account/oidc.go` | `completeOIDC` 标 credentials-changed |
| `internal/transport/handlers_account_official.go` | 3 个 Method 常量 + `OfficialAccountMethods()` 14→17 + `IsOfficialAccountMethod`；`OfficialAccountNotification*`/`OfficialAccountUpdate` 类型与常量；`accountWatchUpdates`（rc.2 绑定优先、rc.1 绑定包装）；`serveAccountWatch` 改发 `{type,state}` 且校验 type；`serveAccountWatchExpiry` 开流 flush + 只允许 `session-expired`；三个通知 handler（严格参数、结果 `{"batch":…}`/`{"content":…}`、违约 → 500）；`normalize*` 校验；`OfficialSignInPhases()` 按 rc.2 顺序；两条 deprecated 方法加注释与替代方法名 |
| `cmd/dshd/serve_account_api.go` | `bindAccountNotifications`（三件套 → provider，错误映射 501/400/500）、`bindAccountWatchUpdates`（`WatchUpdates` → `AccountUpdate` 流）；`accountErrorAPI` 补齐 4 个回调；历史 grant-doc 组加"未绑定"注释；头部写清 rc.2 绑定面 |
| 测试 | `internal/transport/handlers_account_official_test.go`（+约 590 行：三方法参数/形状/null/事件枚举/违约、watch 四种、watchExpiry const 两种、phase 顺序、deprecated）；`internal/account/notifications_test.go`（新，约 700 行）；`cmd/dshd/serve_account_api_test.go`（+约 380 行，全部打 httptest 假 Platform）；`internal/account/account_test.go`、`internal/transport/handlers_account_parity_test.go` 的 rc.1 基线断言按 rc.2 契约改写 |

---

## 7. 测试红 → 绿记录

**红 1（编译级：实现缺席）**

```
go vet ./internal/account/
vet.exe: internal\account\notifications_test.go:628:45: undefined: Update
```

**红 2（wire 变更级：旧实现 + 新契约）** —— 把 parity 测试更新前跑出：

```
--- FAIL: TestOfficialAccountStateAndStreams
    handlers_account_parity_test.go:57: watch 帧：{Status: Links:{UsageURL: TopUpURL:} Attempt:<nil> Source:}
```

即旧实现发的是扁平 `AccountView`，rc.2 要求 `{type,state}`。

**红 3（守卫缺席级：三个守卫各自可被变异打红）** —— 在隔离副本里分别删掉守卫后跑：

| 变异 | 变红的测试 |
|---|---|
| 删 `watchExpiry` 的 const 守卫 | `TestOfficialAccountWatchExpiryRejectsBaselineFrame`：`非 session-expired 的值被发出去了：""` |
| 删 `AccountUpdate.type` 守卫 | `TestOfficialAccountWatchRejectsUnknownUpdateType`：`非法 type 的帧被发出去了："{\"type\":\"renamed\",…}"` |
| 删 `nextPollAfterSeconds` 校验 | `TestOfficialAccountNotificationProviderViolationIsLoud/nextPollAfterSeconds_不是正数`：`状态码 200，期望 500` |
| Local 模式改成静默回空批次 | `TestNotificationBackendRequiresOfficialProvider/local`：`getNotifications: &{NextPollAfterSeconds:600 Notifications:[]}` |

**绿（恢复实现后）**

```
go build ./...                                  EXIT=0
go vet ./...                                    EXIT=0
go test ./internal/account/   -count=1          ok  dshgo/internal/account   1.581s
go test ./internal/transport/ -count=1          ok  dshgo/internal/transport 4.734s
go test ./cmd/dshd/           -count=1          ok  dshgo/cmd/dshd         299.354s
go test ./internal/llm/       -count=1          ok  dshgo/internal/llm       4.647s
```

（全量门禁在 `git archive HEAD` 的**隔离副本** `C:/dsh-a4-check` 里跑——共享工作树里还有其它并行任务的半成品，`check.sh`/`regress.sh` 有全局 taskkill 与原地 `mv modules`，按 AGENTS.md 不在共享树跑全量。同样的三包在工作树里也已绿：`account 1.117s / transport 5.272s / cmd/dshd -run Account 0.247s`。）

本任务新增/改写的测试（全部通过）：

- `internal/transport`：`TestOfficialAccountNotificationParamContract`、`…EventEnum`、`…BatchShape`、`…ContentShape`、`…NullIsWrapped`、`TestOfficialAccountReportNotificationEventBoolean`、`TestOfficialAccountNotificationProviderViolationIsLoud`、`TestOfficialAccountWatchEmitsAccountUpdate`、`…LegacyBindingIsWrapped`、`…RejectsUnknownUpdateType`、`TestOfficialAccountWatchExpiryConst`、`…RejectsBaselineFrame`、`TestOfficialSignInPhasesMatchRC2Order`、`TestOfficialAccountDeprecatedBonusMethodsStillServed`
- `internal/account`：`TestOfficialProviderNotifications`、`…SkipsMalformedEntries`、`…EnvelopeFailureIsError`、`…Unauthorized`、`TestOfficialProviderNotificationContent`、`TestOfficialProviderReportNotificationEvent`、`TestNotificationBackendRequiresOfficialProvider`、`…WithoutGrantIsNull`、`TestHarnessOriginIsSeparateFromPlatformOrigin`、`TestDefaultHarnessOrigin`、`TestHarnessOriginFromEnv`、`TestWatchUpdatesClassifiesCredentialChanges`、`TestWatchExpiryOnlyEmitsSessionExpired`
- `cmd/dshd`：`TestAccountNotificationTrioIsBound`、`TestAccountNotificationsLocalModeIsLoudNotEmpty`、`TestAccountNotificationsRealPlatformRoundTrip`、`TestAccountWatchUpdatesCarryCredentialChange`、`TestAccountDeprecatedBonusMethodsStayRealOrLoud`、`TestAccountNotificationsFailLoudWhenHarnessUnreachable`

---

## 8. 接线行

**无需新增任何 attach 调用。** 既有接线已覆盖：

- `cmd/dshd/serve.go:831` `accountAPI := newAccountOfficialAPI(accountOfficialDeps{home: *home, catalog: modelCatalog})`
- `cmd/dshd/serve.go:833` `srv.AttachOfficialAccountAPI(accountAPI)`
- `internal/transport/server.go:547` `if IsOfficialAccountMethod(name) { s.serveOfficialAccountAPI(w, r, name) }`

四个新回调（`AccountGetNotifications` / `AccountGetNotificationContent` / `AccountReportNotificationEvent` / `AccountWatchUpdates`）都在 `newAccountOfficialAPI` 内部挂上，所以 `serve.go` 一行都不用改（符合任务约束）。

**可选（不阻塞，留给 Lead 决定）**：`cmd/dshd/serve_account_provider.go` 的 `account.Options{…}` 里可以显式加一行

```go
HarnessOrigin: os.Getenv(account.EnvHarnessOrigin),
```

理由：目前 `internal/account.New` 自己会读同一个环境变量，功能已通（`TestHarnessOriginFromEnv` 钉住），显式传只是让配置入口集中在 `serve_account_provider.go` 一处。该文件不在本任务写作用域，故未改。

---

## 9. 与官方 wire 的显式差异（都是有意为之，均已登记）

1. **`null` 结果的包装**：本仓成功信封不接受 `{"result":null}`（出口会把 null 变成 500），所以 `getNotifications`/`getNotificationContent` 的 null 用 `{"batch":null}` / `{"content":null}` 表达——与既有 `profile`/`balance`/`bonuses`、`session.goal.get` 的 `{"goal":null}` 同形。
2. **`batch.source`**：本仓在批次上附加 `source`（`"official"`）。官方 schema 是 `additionalProperties:true`，多这个字段合法；作用是让调用方能确认数据来自官方 Platform 而非本地伪造（与 `getProfile`/`getBalance` 的 `source` 一致）。
3. **`id` 必须非空**：官方 `z.string()` 允许空串；本仓在 transport 层要求非空（空 id 既不能回报事件也无从定位），属**更严**而非更松，方向符合能力下限规则。
4. **流的中途违约只能断流**：ndjson 长流已经开始（200 + header）后，遇到非法帧（`AccountUpdate.type` 越界、`watchExpiry` 非 `session-expired`）时无法再回四字段错误，所以选择**不发出非法帧 + 终止流**（不悄悄改写成合法值）。这也是 `watchExpiry` 去掉基线帧后必须"开流即 flush"的原因。
5. **L2 未实现**：本文件仍是 L1（`POST /api/<method>` + 裸参数 + `{"result":…}`）。`remote.mux` + typert Connection envelope 未收口，因此不声称与官方桌面端 wire 兼容——`typeSymbol` 类变更（§4 的 #2/#3/#5/#7/#9）在 L1 无对应物也正是这个原因。

---

## 10. 未验证 / 遗留（显式登记，不埋）

1. **`-race` 未验证**：本机 `go test -race` 直接失败——`-race requires cgo`，而 `CGO_ENABLED=1` 后报 `cgo: C compiler "gcc" not found`（`/c/msys64/mingw64/bin/gcc.exe`、`/c/mingw64/bin/gcc.exe`、`TDM-GCC` 均不存在，`which gcc cc clang` 全空）。所以并发正确性只有逻辑保证（`pending` 只在 `s.mu` 下读写、`wake` 容量 1、`WatchUpdates` 的 goroutine 只通过 channel 交接），**没有 race 检测证据**。需要带 C 工具链的环境补跑：`CGO_ENABLED=1 go test -race ./internal/account/ ./internal/transport/`。
2. **`cmd/dshd/serve_account_api.go` 的历史 grant-doc 组未清理**（`loadGrant`/`saveGrant`/`accountOfficialAPI.getUnnotifiedBonuses/ackBonusNotified/signOut`）。它们没有被任何回调引用，只是死代码 + 一个旧解析器。删它会扩大本任务 diff，故只加注释标记、留作独立清理任务。
3. **`DSHD_ACCOUNT_HARNESS_ORIGIN` 未在 `serve_account_provider.go` 显式传**（见 §8 可选行）。当前靠 `internal/account.New` 读同一 env，功能已验证。
4. **`../dsh-ui` 侧无消费点**：`grep -rln "getNotifications\|account\.watch\|watchExpiry\|getUnnotifiedBonuses" ../dsh-ui --include=*.go` 无命中，说明 UI 目前还没接这三件套，也**没有**需要跟着改 `AccountUpdate` 的现有消费点（将来接的时候按 `{type,state}` 读）。这条只是现状说明，不是缺陷。
5. **跨仓库端到端未跑**：本任务只到"宿主机 + 真实 HTTP 往返（httptest 假 Platform）"这一层，没有对着真实 `harness.deepseek.com` 跑过（无真实账号凭据，且不该拿用户凭据做测试）。真实链验收归 Lead 的集成轮。

---

## 11. 复核命令

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./... && go vet ./...
go test ./internal/account/ ./internal/transport/ ./cmd/dshd/ -count=1
```

只看本任务：

```bash
go test ./internal/transport/ -run 'Account|SignIn|Notification' -count=1 -v
go test ./internal/account/   -run 'Notification|Watch|Harness'    -count=1 -v
go test ./cmd/dshd/           -run 'Account'                        -count=1 -v
```
