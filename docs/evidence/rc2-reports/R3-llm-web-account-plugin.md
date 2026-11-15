# R3 LLM/Web/账号/插件/设置/定时/Jobs 域 rc.1→rc.2 差异

## 0. 结论摘要（≤10 行）

1. **重试设置的真机制在客户端 + provider schema，不在 `dsh-llm-retry`**：权威文档明确 `dsh-llm-retry` **没有 config**（`Config = Readonly<Record<string, never>>`，config-catalog.md:2397-2398），`retryPolicy` 挂在各 provider 配置下（config-catalog.md:2140-2141「omission uses normal mode with five retries」）。release note「可在模型设置中选择重试条件/次数/等待时间」由 `dsh-client-ui-settings-models/src/client/RetryPolicyEditor.tsx` **纯 schema 驱动**渲染（模式/错误码多选/最大次数/退避三项）；rc.2 对 `dsh-llm` 的改动正是让这个表单拿得到数据：`retryableCodes` 改为「8 个内置 code 的 `z.const` 联合 + 任意非空 string」+ `.min(1)`，`initialDelayMs`/`maxDelayMs` 加 `.min(Number.MIN_VALUE)`。**默认值一个字没变**：5 次 / 500ms→10000ms / jitter 0.1 / 5 个 code。
2. **Web**：`dsh-web` 升级为 `TypertRemoteService` 并新增 `@Remote listProviders`（`{search[],fetch[]}`，每项 `{id,title?,description?}`）+ 新事件 `web/providers-changed`；base 配方 `searchProvider` 从 `deepseek-official` 改为 `deepseek-search-api`，`fetch` 仍是 `http`。
3. **`dsh-tool-web` 默认上限 8→10（来源）/ 4→10（查询）**，权威目录逐字写明「Defaults to 10」（config-catalog.md:4672-4675）。
4. **`dsh-web-deepseek-api` 被重写**：`deepseek-search-api` + `deepseek-fetch-api` 双 provider，配置面从 `{apiKeyEnv,baseURL,count}` 变为 9 项（`count` 默认 10、`searchRequestsPerSecond` 默认 5、`sendOriginalQuestion` 默认 true、`searchBilling`/`fetchBilling` 默认 `auto`、四个 `*BaseURL`/`*ApiKeyEnv`）；旧 `baseURL`/`apiKeyEnv`/`apiKey` 明确"不再是 schema 字段、加载不报错但无效果"（config-catalog.md:4857-4862）。
5. **账号**：两包（`dsh-api-account-controller`、`dsh-deepseek-account-platform`）删除，`DeepSeekAccount` 具体服务自带 `account` Remote 命名空间；新增 3 方法 + **`account.watch` 结果从裸 `AccountView` 改为 `{type,state}` 信封**（wire 破坏性变更）；赠金两方法由通用通知 + `reportNotificationEvent` 取代；**凭据 KEY 未变、不要求重新登录**（upgrade-guide `account-remote-provider/guide.zh.md:14`）。
6. **凭据**：`dsh-credentials` 接缝零代码改动；真改动在 `dsh-credentials-local`——`describe()` 对 `source:'file'` 返回 `masked`（provider 标识 + 5 头 4 尾，中间 `•`，隐藏 <8 字符则整串掩码）。
7. **插件管理**：`listBundles` 行 `updatable: boolean` → `update?: 'registry'|'git'`；`updateBundle` 增 `spec` 参数（Git 固定 commit 升级，且 `updateBundle` **总会**传 `InstallBundleOptions.expected`）；新增诊断码 `unexpected-package`（装了别的包/版本 → 回滚）；`listedByDefault` 非法值不再抛错拖垮整页；Git 连通性检查泛化到任意 host；`plugin_manager` **工具**从未返回该字段、保持不变（upgrade-guide `plugin-update-method/guide.zh.md:16`）。
8. **设置面**：`dsh-settings`/`dsh-config-editor`/`settings.describe|mutate|replace|update` **零改动**（`lib/` 逐字节相同、方法面无增删）；变化全在各插件 Config schema。
9. **定时任务**：`dsh-schedule` **零代码改动**，`schedule.*` 方法面无增删；暂停/恢复"反馈"是客户端行为（`dsh-client-ui-schedule/lib/client.js` 重写），宿主无可派单点。
10. **Jobs**：新增 `JobError` + 7 个 `JOB_*` 码、`waitAborted()` 命名 `AbortError`、`job_follow` 增 `timeout_ms` 校验、`job_list`/`schedule_list`/`schedule_view` 标 `isConcurrencySafe`。**遥测/分析零改动**：`dsh-otel`、`dsh-host-product-telemetry-otel`、`dsh-anonymous-user-id`、`dsh-session-telemetry-otel`、`dsh-client-product-analytics` 的 `lib/` 全部逐字节相同。

> **证据分层（本报告逐条标注）**
> - **【文档】** 权威文档，随 rc.2 asar 发布、由源码生成：`tmp/GO8-RC2/official-rc2-docs/dsh/node_modules/@deepseek-ai/dsh/docs/`
>   - `upgrade-guide/README.md`（19 条 rc.1→rc.2 升级说明索引）；本域 4 条：`v0.2.2-rc.1/{account-notification-fields,account-remote-provider,web-search-billing,plugin-update-method}/guide.zh.md`
>   - `config-catalog.md`（各包 config 声明逐字段 + 默认值 JSDoc，脚本 `gen-config-catalog.ts` 生成并有 `verify-config-catalog` 校验，见 config-catalog.md:8）
>   - 下文简称 **`ug/<条目>:行`** 与 **`catalog:行`**。
> - **【源码】** 提取根 `tmp/GO8-RC2/` 下的 `official-rc1|official-rc2/dsh/node_modules/@deepseek-ai/...`；rc.2 契约优先读 `src/*.ts`，rc.1 只能读 `lib/*.js`；wire 契约以生成的 `lib/typert.host.js` 为准。
> - **【未取证】** 拿不到证据的条目集中列在 §4 末尾，不猜。
> 宿主现状行号相对仓库根 `D:/DeepSeek Harness/default-workspace/DSH-GO`。

## 1. 逐包差异表

统计口径：`lib/`（含 `typert.host.js`/`typert.remote-client.js`）改动文件数；`src/` 新增与 `package.json` 版本 bump **不计**（rc.2 给 334 个包全部追加了 `src/`，rc.1 只有 8 个包带）。

| 包 | lib 改动文件数 | 要点 | 证据 file:line |
|---|---|---|---|
| `dsh-llm` | 8 | `HarnessError` 迁出到新包 `dsh-error` 并 re-export；新增 `LLM_FAILURE_CODES`（52 码）与 `RETRY_CODE_CHOICES`（8 码）；`RetryPolicySchema` 收严 + 把候选值/下界发布给设置表单 | 【源码】`official-rc2/.../dsh-llm/src/error.ts:17`、`:30`、`:76`；`src/retry-policy.ts:82-98` |
| `dsh-llm-retry` | **0** | 逐字节相同；**且本包无 config**（`Config = Readonly<Record<string, never>>`），重试策略由 provider 拥有 | 【文档】catalog:2391-2399；【源码】`official-rc2/.../dsh-llm-retry/src/types.ts:16-48`（与 rc.1 `lib/index.js` 同） |
| `dsh-llm-deepseek` | 1 | 抽出 credential 助手导出（`resolveDeepSeekApiKey`/`deepSeekApiKeyConfigured`/`resolveDeepSeekAccountToken`/`rejectDeepSeekAccountToken`）供 web provider 复用；`LlmError.fromFailure` | 【源码】`official-rc2/.../dsh-llm-deepseek/src/config.ts:55`、`:110`、`:338` |
| `dsh-llm-pi-ai` | 1 | `retryPolicy` 仍在每 provider profile（与 rc.1 同形）；「omission uses normal mode with five retries」 | 【文档】catalog:2021（节）、`:2140-2141`（字段与默认说明）；【源码】`official-rc2/.../dsh-llm-pi-ai/src/config.ts:206-207`、`:379` |
| `dsh-llm-deepseek-account` / `-api-key` / `dsh-deepseek-llm-api-extensions` | 1 / 1 / **0** | 仅随 `dsh-llm` 导出同步 | — |
| `dsh-web` | 4 | 基类 `Service`→`TypertRemoteService`；新增 `@Remote listProviders`；注册/注销后发 `web/providers-changed`；provider 增加可选 `description` | 【源码】`official-rc2/.../dsh-web/src/index.ts:82`、`:151-157`；`src/types.ts:11-22`、`:172-192`；【文档】catalog:4797-4811 |
| `dsh-web-search-deepseek` | 1 | provider 增 `description`（8 语言）；401 时先 `rejectAccountToken` 撤销令牌再抛 `WEB_PROVIDER_*`；本包 config 未变（`model` 默认 `deepseek-v4-flash`、`maxTokens` 4096、`maxUses` 5、`apiVersion` `2023-06-01`） | 【源码】`official-rc2/.../dsh-web-search-deepseek/src/provider.ts:207`、`:305`、`:341`；`src/index.ts:123`；【文档】catalog:4936-4951 |
| `dsh-web-fetch-http` | 1 | provider 增 `description`（免费/可达内网、不支持 JS/登录页/PDF）；本包 config 未变 | 【源码】`official-rc2/.../dsh-web-fetch-http/src/provider.ts:35-36`；【文档】catalog:4909-4921 |
| `dsh-web-deepseek-api` | 1 | 重写为 `deepseek-search-api` + `deepseek-fetch-api`；配置面大改；`count` 默认 10（1..50）、限速默认 5 rps；旧 `baseURL`/`apiKeyEnv`/`apiKey` 降级为无效 legacy 字段 | 【文档】catalog:4857-4896、`ug/web-search-billing/guide.zh.md:12-16`；【源码】`official-rc2/.../dsh-web-deepseek-api/src/index.ts:61-109`；`src/search.ts:16`、`:19`、`:22` |
| `dsh-tool-web` | 1 | `WEB_SEARCH_MAX_RESULTS` 8→**10**、`WEB_SEARCH_MAX_QUERIES` 4→**10**，成为 Config 默认值；上限在**校验期**判定（schema 无 `maxItems`） | 【文档】catalog:4666-4684；【源码】`official-rc2/.../dsh-tool-web/src/search.ts:17-20`、`:42-62`；`src/index.ts:61-62`；rc.1：`official-rc1/.../dsh-tool-web/lib/index.js:26`、`:28` |
| `dsh-deepseek-account` | 9 | 重写：`super(ctx,'deepseekAccount',{namespace:'account'})`；拆出 `client-metadata/details/logout/notifications/protocol`；新增通知三方法；`watch` 改 `{type,state}` 信封；新增 `harnessOrigin` | 【文档】`ug/account-notification-fields/guide.zh.md:12-18`、`ug/account-remote-provider/guide.zh.md:12-14`；catalog:899-930；【源码】`official-rc2/.../dsh-deepseek-account/src/index.ts:34`、`:136`、`:256-329`、`:621-624`；`src/types.ts:44-49`、`:73-112`；`src/protocol.ts:127`、`:142`、`:158-160` |
| `dsh-api-account-controller` | **整包删除** | rc.1 的 `account.*` Remote 门面（含赠金两方法） | 【文档】`ug/account-remote-provider/guide.zh.md:12`；【源码】`official-rc1/.../dsh-api-account-controller/lib/index.js:233-244`、`lib/typert.host.js:6-17`、`:88-107` |
| `dsh-deepseek-account-platform` | **整包删除** | rc.1 的账号服务实现（含赠金读写） | 【源码】`official-rc1/.../dsh-deepseek-account-platform/lib/index.js:644`、`:664` |
| `dsh-credentials` | **0** | 只有 `@module` 注释措辞变化（"its value"→"its full value"）；`CredentialInfo.masked` 契约早已存在 | 【源码】`official-rc2/.../dsh-credentials/src/types.ts:62-75` |
| `dsh-credentials-local` | 1 | 新增 `maskCredential()`；`describe()` 对 `source:'file'` 返回 `masked`；env/dotenv 层不返回 | 【源码】`official-rc2/.../dsh-credentials-local/src/index.ts:522-530`、`:648-657` |
| `dsh-settings` | **0** | 逐字节相同 | 【源码】`official-rc2/.../dsh-settings/src/schema.ts:19-52`、`src/index.ts:302` |
| `dsh-config-editor` | **0** | 逐字节相同 | — |
| `dsh-settingsform` | 不存在 | 两版本都无此包名 | — |
| `dsh-client-ui-settings-models`（UI 线，但决定本域契约能否被消费） | 1（客户 bundle） | 新增 `RetryPolicyEditor.tsx`：**纯 schema 驱动**的重试设置卡片（模式分支、错误码多选、最大次数、退避三项），边界/默认值全读 schema | 【源码】`official-rc2/.../dsh-client-ui-settings-models/src/client/RetryPolicyEditor.tsx:1-9`、`:22-45`、`:69-71`、`:134-144`；`src/client/locales.ts:63-85` |
| `dsh-plugin-manager` | 9 | `update` 字段、`spec` 参数、`unexpected-package`、`listedByDefault` 降级、Git 检查泛化、`pinnedGitOrigin`、`expected` 安装后校验与回滚重装 | 【文档】`ug/plugin-update-method/guide.zh.md:12-16`；catalog:2758-2794；【源码】`official-rc2/.../dsh-plugin-manager/src/types.ts:106-114`；`src/index.ts:502-503`、`:565-575`、`:809-819`、`:827-843`；`src/install-spec.ts:108`；`src/operations.ts:61`、`:290-300`、`:498-515`；`src/tools.ts:27`；`src/diagnostic.ts:60`；`src/github-connection.ts:44`、`:58-102` |
| `dsh-plugin-market` | 6 | 版本行新增 `update:boolean`（Git 固定 commit 只能升到同仓库目录的另一个 commit）；仓库缓存 fetch 在 `denied` 时切换 remote 到 SSH 形式重试（`OPERATION_COMMANDS` 4→5） | 【源码】`official-rc2/.../dsh-plugin-market/src/index.ts:481-487`；`src/repository-cache.ts:21`、`:76`、`:287-303` |
| `dsh-plugin-market-index` | 1 | 随 `pinnedGitOrigin` 导入变化 | 【源码】`official-rc2/.../dsh-plugin-market/src/index-url.ts` |
| `dsh-plugin-detail` / `dsh-plugin-package-inventory-deepseek` | **0** / **0** | 逐字节相同 | — |
| `dsh-plugin-recommendations` | 3 | 跟随 `pluginManager` 的 `update` 语义同步 | 【源码】`official-rc2/.../dsh-plugin-recommendations/src/index.ts`、`src/skill.ts` |
| `dsh-host-plugin-inventory` | 2 | `lib/index.js` **无改动**；仅重生成的 `typert.*` 变化。`PluginInventoryEntry`（`fiberPhase`/`diagnostic`）rc.1 已有 | 【源码】`official-rc2/.../dsh-host-plugin-inventory/src/types.ts:17-28` |
| `dsh-schedule` | **0** | 逐字节相同；`schedule.*` 方法面无增删；config 默认值未变（`deliveryHistoryDays` 30、`deliveryHistoryRecords` 200、`newSessionIntervalSeconds` 60） | 【文档】catalog:3080-3098 |
| `dsh-jobs` | 3 | 新增 `src/error.ts`（`JOB_ERROR_CODES` 7 码 + `JobError extends HarnessError`）并 re-export | 【源码】`official-rc2/.../dsh-jobs/src/error.ts:16-24`、`:30` |
| `dsh-jobs-local` | 1 | 6 处裸 `Error` → `JobError(<code>)`；`wait aborted` → 命名 `AbortError` 的 `waitAborted()` | 【源码】`official-rc2/.../dsh-jobs-local/src/index.ts:121`、`:219`、`:234`、`:411`、`:512`、`:531` |
| `dsh-tool-jobs` | 5 | `job_id` 非空校验改 `ToolArgsError`；`job_follow` 增 `timeout_ms` 正值校验；`job_list` 标 `isConcurrencySafe` | 【源码】`official-rc2/.../dsh-tool-jobs/src/index.ts:461`、`:552` |
| `dsh-tool-schedule` | 2 | `schedule_list`、`schedule_view` 标 `isConcurrencySafe: () => true` | 【源码】`official-rc2/.../dsh-tool-schedule/src/index.ts:406`、`:426` |
| `dsh-user-questions` / `dsh-action-cards` | 3 / 4 | 方法面无增删（`userQuestions.answer|attachWait`、`actionCards.act` 全在），内部投影/等待实现变化 | 【源码】`official-rc2/.../dsh-user-questions/src/timed-wait.ts`、`src/projection.ts`；`dsh-action-cards/src/projection.ts` |
| `dsh-host-product-telemetry-otel` / `dsh-otel` / `dsh-anonymous-user-id` / `dsh-session-telemetry-otel` / `dsh-client-product-analytics` | **0** ×5 | 全部逐字节相同；`productAnalytics.*` 方法面无增删；分析侧 config 里**没有** `mode/local` 字段（见 §4 未取证） | 【文档】catalog:557-578、`:1866-1895` |
| `dsh-product-analytics` | 不存在 | 两版本都无此包名；`productAnalytics.*` 的方法面由 `dsh-api-remotes` 客户端侧聚合 | 【源码】`official-rc2/.../dsh-api-remotes/src/client/index.ts:200` |

## 2. 方法/参数契约变化表

### 2.1 方法面（193 → 223 中本域部分）

| 方法或参数 | rc.1 | rc.2 | 默认值 | 证据 |
|---|---|---|---|---|
| `web.listProviders` | **不存在** | 新增（`WebRuntime` 上 `@Remote`，无参，同步返回） | — | 【源码】`official-rc2/.../dsh-web/src/index.ts:151-157` |
| `web.listProviders` 结果 | — | `{search: WebProviderInfo[], fetch: WebProviderInfo[]}`；每项 `{id:string, title?:string, description?:LocalizedText}`；按注册顺序；**不按可用性过滤** | — | 【源码】`official-rc2/.../dsh-web/src/types.ts:172-192` |
| `account.getNotifications` | — | 参数 `(client: {version,locale,timezoneOffsetSeconds})`；结果 `{nextPollAfterSeconds:number>0, notifications[]} \| null` | — | 【文档】`ug/account-notification-fields/guide.zh.md:12`；【源码】`official-rc2/.../dsh-deepseek-account/src/index.ts:256-274`；`lib/typert.host.js:70-97` |
| `account.getNotificationContent` | — | 参数 `(id:string, client)`；结果 `AccountNotification \| null`（读已消费通知不改变消费状态） | — | 同上 `guide.zh.md:12`；【源码】`:282-301`；`lib/typert.host.js:43-69` |
| `account.reportNotificationEvent` | — | 参数 `(id:string, event:'displayed'|'cta_clicked'|'dismissed', client)`；结果 `boolean`（无重试，`acked:true/false` 都结束本次尝试） | — | 同上 `guide.zh.md:12`、`:23`；【源码】`:310-329`；`lib/typert.host.js:133-144` |
| `AccountNotification` 形状 | — | `{id, createdAt, displayUntil:string\|null, title, body, actions:[{label,style:'primary'|'secondary',target:{type:'app_route',name:'account'}\|{type:'dismiss'}}], data?:json}` | — | 【源码】`official-rc2/.../dsh-deepseek-account/src/types.ts:73-99`；`lib/typert.host.js:52-69` |
| **`account.watch` 结果** | 裸 `AccountView`（`{status,links,attempt}`），每项即当前状态 | **`AccountUpdate` = `{type:'snapshot'|'credentials-changed', state:AccountView}`**：首订阅与登录进度用 `snapshot`；凭据写入/删除用 `credentials-changed` | — | 【文档】`ug/account-notification-fields/guide.zh.md:14`、`:22`；【源码】`official-rc2/.../dsh-deepseek-account/src/types.ts:44-49`、`src/index.ts:621-624`、`lib/typert.host.js:640-657`；rc.1 平铺形状见 `official-rc1/.../dsh-api-account-controller/lib/typert.host.js:156-170` |
| `account.getUnnotifiedBonuses` | 参数 `(client)`；结果 `{accountId, bonuses:[{title?,orderId,campaign?,amount?,currency?,grantedAt?,expiresAt,message}]} \| null` | **删除**（赠金元数据改放 `AccountNotification.data`；`accountId` 字段移除） | — | 【文档】`ug/account-notification-fields/guide.zh.md:12`、`:23`；【源码】rc.1 `lib/typert.host.js:88-107` |
| `account.ackBonusNotified` | 参数 `(accountId:string, orderId:string, client)`；结果 `boolean` | **删除**（改 `reportNotificationEvent(id,'displayed'|…, client)`，后端判定消费） | — | 同上 `guide.zh.md:12`、`:23`；rc.1 `lib/typert.host.js:6-17` |
| `account.*` Host-only 方法 | 不存在于 Remote 导出 | `resolveToken` / `rejectToken` / `getPlatformSession` / `getDeviceIdentity` **明确不得**成为 Remote 导出 | — | 【文档】`ug/account-remote-provider/guide.zh.md:12`、`:22` |
| `credentials.describe` 结果项 | `{configured, source?, writable}` | 追加 `masked?: string`（**仅** `source:'file'`；env/dotenv 层无） | — | 【源码】`official-rc2/.../dsh-credentials/src/types.ts:66-75`；`dsh-credentials-local/src/index.ts:648-657` |
| `credentials.set` / `unset` | `(ref,value)` / `(ref)` | **不变**（`set('')` 抛错，清空走 `unset`） | — | 【源码】`official-rc2/.../dsh-credentials-local/src/index.ts:659-668` |
| `pluginManager.listBundles` 行字段 | `updatable: boolean` | **改为** `update?: 'registry'\|'git'`，不可升级时**整键省略**；原 `updatable:true` 现等价 `update:'registry'` | — | 【文档】`ug/plugin-update-method/guide.zh.md:12`、`:20`；【源码】`official-rc2/.../dsh-plugin-manager/src/types.ts:106-112`、`src/index.ts:502-503`、`lib/typert.host.js:116` |
| `pluginManager.updateBundle` | `(name, version, {registry?, requestId?})` | 增 `spec?: string`（`update:'git'` 的包必填；须与已装副本同仓库目录；`update:'registry'` 的包传了即 `invalid-spec`） | — | 【文档】`ug/plugin-update-method/guide.zh.md:12`；【源码】`src/index.ts:733`、`:809-819`、`lib/typert.host.js:441`、`:1209-1216` |
| `ManagementError.code` | 9 码 | 增 `unexpected-package` | — | 【文档】`ug/plugin-update-method/guide.zh.md:14`；【源码】`src/diagnostic.ts:60`、`src/tools.ts:27` |
| `plugin_manager` **工具**（模型面） | 不含 `updatable` | **仍不含**，不受影响 | — | 【文档】`ug/plugin-update-method/guide.zh.md:16` |
| `job.list/kill/follow` | — | 方法面不变；`job_follow` 增 `timeout_ms` 正值校验；`job_list` 标并发安全 | — | 【源码】`official-rc2/.../dsh-tool-jobs/src/index.ts:461`、`:552` |
| `schedule.*` | — | **完全不变**；仅 `schedule_list`/`schedule_view` 标并发安全 | — | 【源码】`official-rc2/.../dsh-tool-schedule/src/index.ts:406`、`:426`；`tmp/GO8-RC2/methods-diff.json` |
| `settings.describe/mutate/replace/update` | — | **无变化** | — | §1 `dsh-settings` 行；`tmp/GO8-RC2/methods-diff.json` |

### 2.2 配置/设置项（"新增设置项"清单）

| 设置项 | rc.1 | rc.2 | 默认值（权威口径） | 证据 |
|---|---|---|---|---|
| `retryPolicy`（每 provider，**不属于 `dsh-llm-retry`**） | 在 provider config 下 | 同上；本包/docs 均确认「策略执行器无 config，provider 拥有 `retryPolicy`」 | 省略 = `normal` + 5 次重试 | 【文档】catalog:2397-2399、`:2140-2141`；【源码】`official-rc2/.../dsh-llm-deepseek/src/config.ts:55`、`dsh-llm-pi-ai/src/config.ts:206-207` |
| `retryPolicy.retryableCodes` schema | `z.array(z.string())` | `z.array(z.union([...8 个 z.const, z.string().min(1)])).min(1)`（把候选发布给表单的「Error codes」多选） | `[EMPTY_RESPONSE, RATE_LIMIT, SERVER, TIMEOUT, TRANSPORT]`（未变） | 【源码】`official-rc2/.../dsh-llm/src/retry-policy.ts:93-96`、`:19-25`；消费端 `dsh-client-ui-settings-models/src/client/RetryPolicyEditor.tsx:69-71` |
| 可选重试条件（`RETRY_CODE_CHOICES`） | 无此常量 | 8 个：`EMPTY_RESPONSE`、`MALFORMED_RESPONSE`、`QUOTA`、`RATE_LIMIT`、`SERVER`、`STREAM_CLOSED`、`TIMEOUT`、`TRANSPORT`；表单另有「Custom error code」输入 | — | 【源码】`official-rc2/.../dsh-llm/src/error.ts:30`、`:47`、`:55-63`、`:76`；UI 文案 `locales.ts:63-79` |
| `retryPolicy.maxRetries` | `min(0)` step 1 | 不变 | 5 | 【源码】`retry-policy.ts:92` |
| `retryPolicy.backoff.initialDelayMs` / `maxDelayMs` | 仅 `max(MAX_TIMER_DELAY_MS)` | 追加 `.min(Number.MIN_VALUE)`（表单据此判「必须为正」，见 UI 的 `min === Number.MIN_VALUE` 分支） | 500 / 10000 | 【源码】`retry-policy.ts:84-88`；UI `RetryPolicyEditor.tsx:134-144` |
| `retryPolicy.backoff.jitterRatio` | `0..1` | 不变 | 0.1 | 【源码】`retry-policy.ts:87` |
| `web.searchProvider`（base 配方） | `deepseek-official` | **`deepseek-search-api`** | — | 【源码】`official-rc2/.../dsh-base/cordis.patch.yml:484-488`（rc.1 `:467-471`） |
| `web.fetchProvider`（base 配方） | `http` | `http`（值未变，注释改述为「anonymous local fetcher」） | — | 同上 `:487-488` |
| `web-deepseek-api` 行 config | `apiKeyEnv: DEEPSEEK_API_KEY` | **整块删除** | — | rc.1 `official-rc1/.../dsh-base/cordis.patch.yml:478-481` vs rc.2 `:495-496` |
| `web-search-deepseek` 行 config | `apiKeyEnv: DEEPSEEK_API_KEY` | 不变 | — | rc.2 `official-rc2/.../dsh-base/cordis.patch.yml:490-493` |
| `web-deepseek-api.count` | `1..50` 默认 10 | 不变 | **10** | 【文档】catalog:4864-4865；【源码】`official-rc2/.../dsh-web-deepseek-api/src/index.ts:100`、`src/search.ts:19` |
| `web-deepseek-api.searchRequestsPerSecond` | **无** | `min(1)` step 1 | **5** | 【文档】catalog:4866-4871；【源码】`:101`、`src/search.ts:22` |
| `web-deepseek-api.sendOriginalQuestion` | **无** | boolean（触发问句前 128 字符作 `ori_question`） | **true** | 【文档】catalog:4872-4873；【源码】`:102` |
| `web-deepseek-api.searchBilling` / `fetchBilling` | **无** | `'auto'\|'account'\|'api-key'` | **`auto`** | 【文档】catalog:4874-4881、`ug/web-search-billing/guide.zh.md:12`；【源码】`:103-104` |
| `web-deepseek-api.searchBaseURL` / `searchApiKeyEnv` | **无** | 各自钉死 Search 端点/密钥；只设一个时另一个回官方基址 / `DEEPSEEK_API_KEY` | 未设 | 【文档】catalog:4882-4888；【源码】`:105-106` |
| `web-deepseek-api.fetchBaseURL` / `fetchApiKeyEnv` | **无** | 同上（Fetch） | 未设 | 【文档】catalog:4889-4895；【源码】`:107-108` |
| `web-deepseek-api.baseURL` / `apiKeyEnv` / `apiKey` | rc.1 是 schema 字段（`apiKeyEnv` 默认 `DEEPSEEK_API_KEY`，`baseURL` 默认 `https://api.deepseek.com/v1/tools`） | **降级为 legacy**：加载不报错、无效果 | — | 【文档】catalog:4857-4862；rc.1 源码 `official-rc1/.../dsh-web-deepseek-api/lib/index.js:389-393` |
| `tool-web.searchMaxResults` | 默认 **8** | 默认 **10**（“Defaults to 10”） | **10** | 【文档】catalog:4672-4673；【源码】`official-rc2/.../dsh-tool-web/src/index.ts:61`、`src/search.ts:20` |
| `tool-web.searchMaxQueries` | 默认 **4** | 默认 **10**（“Defaults to 10”） | **10** | 【文档】catalog:4674-4675；【源码】`src/index.ts:62`、`src/search.ts:23` |
| `tool-web` 其余项（未变） | `search`/`fetch` 默认 true、`searchSummaryMaxChars` 800、`fetchTimeoutMs`/`searchTimeoutMs` 30000、`fetchMaxOutputChars` 200000 | 不变 | 同上 | 【文档】catalog:4666-4684 |
| `deepseek-account.harnessOrigin` | **无**（rc.1 只有 `platformOrigin`） | 新增，仅用于通知 list/content/events | **`https://harness.deepseek.com`** | 【文档】catalog:899-903、`ug/account-notification-fields/guide.zh.md:16`；【源码】`official-rc2/.../dsh-deepseek-account/src/index.ts:44`、`:75`、`:140-141` |
| `deepseek-account.platformOrigin` | `https://platform.deepseek.com` | 不变（登录/资料/余额/退出/页面） | `https://platform.deepseek.com` | 同上 `guide.zh.md:16` |
| `client-ui-account-notifications.notificationPollIntervalMs` | 包不存在 | 新增（初始轮询间隔，成功响应后被服务端 `nextPollAfterSeconds` 取代） | 文档未写数值（`ug` 提到「初始十分钟间隔」，见 §4 未取证） | 【文档】catalog:604-609；`ug/account-notification-fields/guide.zh.md:25` |
| `ui-settings-account` 的 `bonusAckRetryDelayMs` / `bonusAckRetryMaxDelayMs` | 有 | **移除**（事件上报不再重试） | — | 【文档】`ug/account-notification-fields/guide.zh.md:25` |
| `schedule` 配置（未变，供对照） | — | `deliveryHistoryDays` 30 / `deliveryHistoryRecords` 200 / `newSessionIntervalSeconds` 60 | 同左 | 【文档】catalog:3080-3098 |

### 2.3 新增事件 / 错误码

| 项 | rc.1 | rc.2 | 证据 |
|---|---|---|---|
| Cordis 事件 `web/providers-changed` | 无 | 有（payload-free；注册与注销提交后各发一次；监听器失败被吞，不能否决变更） | 【源码】`official-rc2/.../dsh-web/src/types.ts:11-22`、`src/index.ts:159-174` |
| Cordis 事件 `deepseek-account/model-sign-in-required` | 无 | 有（另有 `signed-out`、`session-expired`） | 【源码】`official-rc2/.../dsh-deepseek-account/src/types.ts:101-112`、`src/account-tasks.ts:24`、`:27` |
| `JOB_ERROR_CODES` | 无（裸 `Error`） | 7 码：`JOB_BACKGROUND_DISABLED`、`JOB_CONTROLLER_UNAVAILABLE`、`JOB_NOT_FOUND`、`JOB_FOREIGN_SESSION`、`JOB_OWNER_NOT_LIVE`、`JOB_OWNER_REGISTRY_UNAVAILABLE`、`JOB_LIMIT_REACHED` | 【源码】`official-rc2/.../dsh-jobs/src/error.ts:16-24` |
| `wait aborted` 错误 | `name` 为 `Error` | `name='AbortError'`（供工具层分类） | 【源码】`official-rc2/.../dsh-jobs-local/src/index.ts:121-125` |
| `WEB_ABORTED` 归类 | 解析函数抛 `AbortError` 但 signal 未取消 → `WEB_PROVIDER_ERROR` | → **`WEB_ABORTED`**；凭据读取错误保留原 `Error` 作 `cause`、诊断保留其 `name`（不再多包一层 `Error:`） | 【文档】`ug/web-search-billing/guide.zh.md:16`、`:40` |
| 新包 `dsh-error` | 无 | `HarnessError` 基类迁入；`dsh-llm`/`dsh-jobs`/`dsh-web` 从它取 | 【源码】`official-rc2/.../dsh-llm/src/error.ts:10-11` |

## 3. 必须实现的宿主缺口（R3-G01…）

> 每条标注证据层级；"宿主现状"行号已核对。

### R3-G01 `web.listProviders` 缺失（新方法，0 实现）
- **官方证据**：`official-rc2/.../dsh-web/src/index.ts:151-157`；结果类型 `src/types.ts:172-192`；事件 `src/types.ts:11-22`。
- **宿主现状**：`internal/transport/` 下没有任何 `web.*` 方法常量（`grep 'Method = "web\.'` 为空）；`internal/spi/search.go:174-209` 只有内部 `WebProviders` 注册表，无 wire 出口。
- **缺什么**：`POST /api/web.listProviders`（裸参数 `{}`）→ `{search:[…],fetch:[…]}`，每项 `{id,title?,description?}`；`description` 是 `LocalizedText`，缺省时字段省略；无 provider 时必须是 `[]` 不是 `null`。
- **先红测试点**：`TestWebListProviders_Shape`（1 search + 1 fetch → 精确 JSON，两键恒为数组）；`TestWebListProviders_Empty`（`{"search":[],"fetch":[]}`）。
- **风险**：低（纯读）。`description` 的 8 语言文案不要写死在宿主，透传 provider 侧注册值。

### R3-G02 默认搜索 provider 仍是 `deepseek-official`（官方已切 `deepseek-search-api`）
- **官方证据**【源码】`official-rc2/.../dsh-base/cordis.patch.yml:487`（rc.1 `:470`）。
- **宿主现状**：`modules/tool/websearch/websearch.go:57` `baseBundleSearchProvider = "deepseek-official"`；`modules/search/deepseek.go:30` `DeepSeekProviderID = "deepseek-official"`；`docs/parameter-register.md:418`、`:604` 亦以 `deepseek-official` 为基线。
- **缺什么**：基础包默认 provider id 改 `deepseek-search-api`；`deepseek-official` 保留为可选 legacy。
- **先红测试点**：`TestBaseBundleSearchProviderDefault`（默认 id == `deepseek-search-api`，且 `deepseek-official` 仍可选）。
- **风险**：中——必须先落 R3-G04，否则切了默认会直接不可用。

### R3-G03 `web_search` 默认上限 4/8 → 10/10 未同步
- **官方证据**【文档】catalog:4672-4675（“Defaults to 10”）；【源码】`official-rc2/.../dsh-tool-web/src/search.ts:17-20`、`src/index.ts:61-62`（rc.1 `official-rc1/.../dsh-tool-web/lib/index.js:26`、`:28`）。
- **宿主现状**：`modules/tool/websearch/websearch.go:41-42` `maxQueries = 4`、`maxResults = 8`，且为**编译期常量不可配置**（`:145-146` 超限 Hint、`:178` 传给 `runSearchQueries`）。
- **缺什么**：默认 10/10；两项变可配置；非法值（非正整数）报错而非静默夹取。
  两条别做错的契约细节：① **输入 schema 里没有 `maxItems`**——上限在**校验期**判定（`official-rc2/.../dsh-tool-web/src/search.ts:42-62`），不要给 `queries` 加 `maxItems`；② 重复查询**不是错误**，按首次出现顺序去重（`search.ts:62` `[...new Set(queries)]`）——宿主 `websearch.go:149-162` 已同语义（`seenQuery` + `TrimSpace`），无需改。
- **先红测试点**：`TestWebSearchDefaultCaps`（10 条执行 / 11 条报错且提示含 `10`）；`TestWebSearchCapsConfigurable`（`searchMaxResults=3` → 来源截断 3）；`TestWebSearchSchemaHasNoMaxItems`；`TestWebSearchDedupesQueries`。
- **风险**：低-中。上限翻倍会抬高单次调用的上下文成本（官方有意放宽），宿主已有 `searchSummaryMaxChars` 兜底。

### R3-G04 缺 `deepseek-search-api` / `deepseek-fetch-api` 两个 provider（`dsh-web-deepseek-api` 重写）
- **官方证据**【文档】catalog:4857-4896、`ug/web-search-billing/guide.zh.md:12-16`、`:30-40`；【源码】`official-rc2/.../dsh-web-deepseek-api/src/index.ts:64-109`、`:248-296`、`src/search.ts:16-22`。
- **宿主现状**：无这两个 provider id；现有 DeepSeek 搜索走 Messages 路线（`modules/search/deepseek.go:30`）。
- **缺什么**：`deepseek-search-api`（`/search`）/`deepseek-fetch-api`（`/fetch`）双 provider；`searchBilling`/`fetchBilling` 三态选凭据（`auto`：DeepSeek 模型路由行为不变，其他会话与会话之外在无密钥时改用已登录账号）；四个 `*BaseURL`/`*ApiKeyEnv` 各自独立钉端点与密钥且**优先于** billing；`count` 1..50 默认 10；`searchRequestsPerSecond` 默认 5（等待计入调用方超时）；`sendOriginalQuestion` 默认 true（前 128 字符作 `ori_question`）；legacy `baseURL`/`apiKeyEnv`/`apiKey` 接受但无效果。
  TS 侧另有构造契约变化（宿主若内聚同形则适用）：顶层 `baseURL` 移除，`auth` 变 `{sources, missingCredentialMessage?}`，来源为有序列表（顺序替代隐式"账号优先"），`auth.invalidRoute` → 位置化的 `{kind:'misconfigured', message}`。
- **先红测试点**：`TestDeepSeekSearchAPI_RequestShape`（URL 拼接 / `count` / `ori_question` 截断 / 客户端头）；`TestDeepSeekWebAPI_BillingPrecedence`（3 种 billing × 有无账号令牌 × 有无 API key 的组合）；`TestDeepSeekWebAPI_LegacyFieldsIgnored`；`TestDeepSeekSearchAPI_RateLimit`（1 秒 6 次，第 6 次等待）。
- **风险**：高（本域最大工作量，依赖两条凭据链与真实端点才能验收错误分流）。

### R3-G05 账号 3 个新方法缺失 + 仍暴露 2 个已删除方法
- **官方证据**【文档】`ug/account-notification-fields/guide.zh.md:12-25`；【源码】`official-rc2/.../dsh-deepseek-account/src/index.ts:256-329`、`lib/typert.host.js:43-144`；方法面 `tmp/GO8-RC2/methods-diff.json`。
- **宿主现状**：`internal/transport/handlers_account_official.go:56`（`account.ackBonusNotified`）、`:60`（`account.getUnnotifiedBonuses`）仍注册；实现 `cmd/dshd/serve_account_api.go:210-260`；三个新方法不存在。
- **缺什么**：三方法（见 §2.1）；`event` 枚举三值；通知项 `actions[].target` 两形态；401 → 失效当前授权后回 `null`/`false`；列表解码**逐条跳过**格式错误项、但列表外层格式错误令请求失败；**下线**两赠金方法（官方 rc.2 无这两个键）。
- **先红测试点**：`TestAccountGetNotifications_NullWhenSignedOut`、`TestAccountReportNotificationEvent_FalseOnSignedOut`、`TestAccountMethodsRemoved`（方法表不含两赠金方法）、`TestAccountNotificationSchema`（对照 `lib/typert.host.js:52-69` 逐键）、`TestNotificationListSkipsMalformedEntry`。
- **风险**：中-高。删除两方法会破坏现有 UI 调用；平台端点需网络可达（`src/protocol.ts:127`、`:142`、`:158-160`）。

### R3-G06 账号服务形状与 Host-only 方法边界（含 `harnessOrigin` 拆分）
- **官方证据**【文档】`ug/account-remote-provider/guide.zh.md:12`、`:14`、`:18-22`；`ug/account-notification-fields/guide.zh.md:16`；【源码】`official-rc2/.../dsh-deepseek-account/src/index.ts:136` `super(ctx,'deepseekAccount',{namespace:'account'})`、`:34` `KEY = credentialKey('deepseek-account-platform','default')`。
- **宿主现状**：方法集中在 `internal/transport/handlers_account_official.go`，实现挂 `cmd/dshd/serve_account_api.go:89-98`（`accountOfficialAPI`，经 `transport.OfficialAccountAPI` 注入）；无"单服务自持 namespace"形状，新增方法要在"常量表 + wire 类型 + 回调接口 + 实现"四处登记。
- **缺什么**：① `deepseekAccount` 单服务自持 `account` 命名空间；② **`resolveToken`/`rejectToken`/`getPlatformSession`/`getDeviceIdentity` 不得出现在 Remote 方法表**（Host-only）；③ `harnessOrigin`（默认 `https://harness.deepseek.com`）只服务通知三端点，登录/资料/余额/退出/页面仍走 `platformOrigin`（默认 `https://platform.deepseek.com`）；④ 凭据 KEY 保持 `deepseek-account-platform/{default,device}` 不变——**已登录态不需要迁移**。
- **先红测试点**：`TestAccountNamespaceCoverage`（官方 `account/*` 集合与宿主方法表完全相等，无多无少）；`TestHostOnlyAccountMethodsNotRemote`（四方法不出现在方法表/`remote.mux` 暴露面）；`TestNotificationOriginSeparateFromPlatformOrigin`。
- **风险**：中（重构面）。KEY 不变这条必须守住。

### R3-G07 `account.watch` 信封未实现（wire 破坏性变更）
- **官方证据**【文档】`ug/account-notification-fields/guide.zh.md:14`、`:22`；【源码】`official-rc2/.../dsh-deepseek-account/src/types.ts:44-49`、`src/index.ts:621-624`、`lib/typert.host.js:640-657`（`{type, state}`，`required:["type","state"]`）。
- **宿主现状**：`internal/transport/handlers_account_official.go:65-66` 只登记方法名；`:789` 明确返回结构化未实现错误「account.watch 未接线：宿主没有账号状态流」。
- **缺什么**：实现时**必须用 rc.2 的 `{type,state}` 信封**，不是 rc.1 的裸 `AccountView`：首订阅/登录进度发 `snapshot`，凭据写入与删除发 `credentials-changed`；每次事件带完整最新 `state`；进行中读到凭据变更要丢弃该次读（`src/index.ts:646-649`）。
- **先红测试点**：`TestAccountWatchEnvelope`（首帧 `type=='snapshot'` 且含 `state`）；`TestAccountWatchCredentialsChanged`（`credentials.set` 后收到 `credentials-changed`）；`TestAccountWatchNoFlatState`（帧里不许出现顶层 `status`）。
- **风险**：中。若宿主按 rc.1 平铺形状实现，UI 会整页读不到状态。

### R3-G08 `credentials.describe` 缺 `masked`
- **官方证据**【源码】`official-rc2/.../dsh-credentials/src/types.ts:66-75`（字段契约）、`dsh-credentials-local/src/index.ts:522-530`（算法）、`:650`（只在 `source:'file'` 填）。
- **宿主现状**：`internal/transport/handlers_settings_official.go:107-111` `OfficialCredentialInfo` 只有 `configured`/`source`/`writable`。
- **缺什么**：`masked?: string`；算法逐字符一致——`providerEnd = 首个 '-' 或 '_' 的下标+1`，`hidden = 码点数 - providerEnd - 9`，`hidden < 8` → 整串 `•`（长度=码点数），否则 `前(providerEnd+5) 码点 + '•'×hidden + 末 4 码点`；仅 `source:'file'` 出现。`set` 覆盖、`unset` 清除后 `masked` 消失且 `configured` 回落 env 层。
- **先红测试点**：`TestDescribeMasked_ShortSecretAllMasked`、`TestDescribeMasked_OpenAIForm`（前缀+5、尾 4）、`TestDescribeMasked_EnvLayerHasNoMask`、`TestUnsetClearsMasked`、以及「describe 全 JSON 不出现完整密钥」的负向扫描。
- **风险**：低，但算法必须逐字符一致。

### R3-G09 `pluginManager.listBundles` 字段 `updatable:boolean` 过期
- **官方证据**【文档】`ug/plugin-update-method/guide.zh.md:12`、`:20`；【源码】`official-rc2/.../dsh-plugin-manager/src/types.ts:106-112`、`src/index.ts:502-503`、`lib/typert.host.js:116`。
- **宿主现状**：`internal/transport/handlers_pluginmanager_official.go:658` `Updatable bool \`json:"updatable"\``；`internal/pluginmgr/bundles.go:202` 注释仍用 "updatable"。
- **缺什么**：删 `updatable`；加 `update`，值 `registry`（第三方 profile 依赖且 source 是自己的 registry spec）或 `git`（source 是 40 位 commit 的 Git spec），不可升级时整键省略。
- **先红测试点**：`TestListBundles_UpdateField`（三类来源分别断言 `"update":"registry"` / `"update":"git"` / 无该键且无 `updatable`）。
- **风险**：中（宿主 UI 若读 `updatable` 会一并失效，需同期通知 UI 线）。

### R3-G10 `pluginManager.updateBundle` 缺 `spec` 参数与 Git 升级路径
- **官方证据**【文档】`ug/plugin-update-method/guide.zh.md:12`、`:14`；【源码】`official-rc2/.../dsh-plugin-manager/src/index.ts:809-819`（`planGitUpdate`）、`:827-843`（`gitHostUnreachable`：只有 `network`/`timeout` 短路，认证需求留给 pnpm）、`src/install-spec.ts:108`（`pinnedGitOrigin`）、`lib/typert.host.js:441`、`:1209-1216`。
- **宿主现状**：`internal/pluginmgr/official/adapter.go:362`、`:394` 只有 registry 升级路径，无 `spec` 解析。
- **缺什么**：`spec?: string`；`pinnedGitOrigin()`（host 小写、去协议/用户/`.git`，`#` 后恰一个 40 位 commit + 至多一个 `path:`，重复参数即无效）；`spec` 与已装副本 origin 不等 → `invalid-spec`；`update:'registry'` 的包传 `spec` 也 `invalid-spec`。
- **先红测试点**：`TestPinnedGitOriginEquivalence`（github: 简写 / ssh 形式 / https / 带 `.git` / 带 `#path:` 五组等价）、`TestUpdateBundle_GitSpecMismatch`、`TestUpdateBundle_GitSpecRequired`、`TestUpdateBundle_RegistryRejectsSpec`。
- **风险**：中（需真实 Git 远端验 `network`/`timeout` 分流）。

### R3-G11 插件页健壮性：缺"单条元数据错误降级为诊断"
- **官方证据**【源码】`official-rc2/.../dsh-plugin-manager/src/index.ts:565-575`（rc.1 对应处是 `throw new Error('dsh.bundle.listedByDefault must be a boolean')`）。
- **宿主现状**：`internal/transport/handlers_pluginmanager_official.go:652` 只有 wire 字段 `ListedByDefault *bool`；全仓**没有**从 bundle manifest 读 `dsh.bundle.listedByDefault` 的解析（`grep` 仅命中该字段）。故宿主**无该崩溃路径**，但也**不产出诊断**——官方 rc.2 的"降级并告知"语义缺失。
- **缺什么**：读 `dsh.bundle.listedByDefault`；非法（存在但非 boolean）→ 该项 `meta.error` 拼接诊断（已有 error 用 `; ` 连接），**不得**中断 `listBundles`；`missing` 分支不再输出 `listedByDefault`。同族要求：单个 bundle 的元数据读取失败只能降到该项 `error`。
- **先红测试点**：`TestListBundles_InvalidListedByDefaultDegrades`（坏 + 好各一，整体成功、坏项带 `meta.error`、正常项照常）；`TestListBundles_MissingHasNoListedByDefault`。
- **风险**：低。release note「可选插件加载失败不再拖垮整页」在插件管理面的可验证落点；宿主本来不崩，收益是"诊断可见"而非"修崩溃"。

### R3-G12 缺 `unexpected-package` 诊断码与安装后校验/回滚
- **官方证据**【文档】`ug/plugin-update-method/guide.zh.md:14`；【源码】`official-rc2/.../dsh-plugin-manager/src/types.ts:37-43`、`src/diagnostic.ts:60`、`src/tools.ts:27`、`src/operations.ts:61`、`:290-300`、`:347-351`、`:498-515`。
- **宿主现状**：`internal/pluginmgr/official/adapter.go` 无 `expected` 概念、无该码。
- **缺什么**：`runProfilePnpm(..., {expected:{name,version}})`；安装后对每个新装/更新的直连依赖校验"装的就是要求的包 + 版本 + 声明了 bundle"；不符 → `unexpected-package` 回滚（pnpm 前拒绝不改任何东西；pnpm 后拒绝还原 manifest+lockfile 并重装还原的 lockfile）；**`updateBundle` 总传 `expected`**；模型侧文案照 `src/tools.ts:27`。
- **先红测试点**：`TestInstallBundle_UnexpectedPackageRollback`（装错包 → 码正确且 profile 字节还原）、`TestInstallBundle_ExpectedUnchangedStillChecked`。
- **风险**：中-高（失败路径 + 文件原子性）。

### R3-G13 Git 连通性检查覆盖面（私有 Git / SSH-only）
- **官方证据**【源码】`official-rc2/.../dsh-plugin-manager/src/github-connection.ts:44`（`checkGithubConnection` 变薄包装）、`:58-102`（`checkGitConnection` 任意 host 生效）、`:61-63`（简写扩到 `github|gitlab|bitbucket|gist`）；安装路径对所有 git spec 调用它。
- **宿主现状**：`internal/pluginmgr/` 下**没有任何** `git ls-remote` 预检（`grep 'ls-remote'` 为空），也无 `checkGitConnection` 等价物。
- **缺什么**：任意 host 的 `git ls-remote` 预检；4 种 host 简写；只有 `network`/`timeout` 中止（认证需求 → `unknown` → 交给 pnpm 与 SSH 传输探测）；日志落 `$PROFILE/.plugin-manager/logs/github-connection-*/git.log`，命令行里的 URL 用户信息脱敏。
- **先红测试点**：`TestCheckGitConnection_HostShorthand`（`gitlab:o/r#<commit>` → `https://gitlab.com/o/r`）、`TestCheckGitConnection_AuthFailureNotFatal`、`TestCheckGitConnection_NetworkFailsFast`、`TestCheckGitConnection_ArgvRedacted`。
- **风险**：低-中（需起本地 git 服务或打桩）。

### R3-G14 `dsh-plugin-market` 缺 Git 版本行 `update` 与 SSH 回退
- **官方证据**【源码】`official-rc2/.../dsh-plugin-market/src/index.ts:481-487`（版本行 `update:boolean` = `pinnedGitOrigin(install.spec) === 已装副本 origin` 且版本更高）、`src/repository-cache.ts:21`（`OPERATION_COMMANDS` 4→5）、`:76`（`sshUrlOf`）、`:287-303`（`denied` 时把 `remote.origin.url` 改成 `ssh://git@host/path` 重试一次，失败合并两条诊断并按 `network`/`denied` 归类）。
- **宿主现状**：`internal/market/` 只有 `store.go`（无仓库缓存与 SSH 回退，`grep 'ssh\|insteadOf'` 为空）；版本行无 `update`。
- **缺什么**：版本行 `update`；仓库缓存 SSH 回退（成功后退回把 remote 固定为 SSH 形式，供后续读取）。
- **先红测试点**：`TestMarketVersionRowUpdate`（同目录更高 commit → true；不同目录 → false）、`TestRepositoryCache_SshRetry`（首次 `denied` → 改 remote 重试成功 → 断言远端 URL 已换 SSH）、`TestSshUrlOf`（带凭据/query 返回 undefined）。
- **风险**：中（需 SSH 环境；可先用伪 `denied` 打桩验分支）。

### R3-G15 Jobs 错误码全缺（`JobError` / 7 码）
- **官方证据**【源码】`official-rc2/.../dsh-jobs/src/error.ts:16-24`、`:30`；`dsh-jobs-local/src/index.ts:219`、`:234`、`:411`、`:512`、`:531`。
- **宿主现状**：全仓无 `JOB_*` 码（`grep` 为空），错误以普通 error 返回；`internal/transport/handlers_job_terminal_official.go:52` 另有 `already-finished` 的自定义成功语义。
- **缺什么**：7 码 + `JobError` 承载 `code`；`job` 工具面按码给模型下一步；`wait aborted` 必须 `name='AbortError'`。
- **先红测试点**：`TestJobErrorCodes`（7 个触发点各命中对应码）、`TestJobFollowWaitAbortedName`、`TestJobListConcurrencySafe`。
- **风险**：低（码是 wire 契约，需与 UI 错误展示对齐）。

### R3-G16 重试设置的可选值/边界未接线（`retryPolicy` schema → 表单）
- **官方证据**【文档】catalog:2397-2399（本包无 config）、catalog:2140-2141（provider 拥有、"omission uses normal mode with five retries"）；【源码】`official-rc2/.../dsh-llm/src/retry-policy.ts:82-98`、`src/error.ts:76`；UI 消费端 `official-rc2/.../dsh-client-ui-settings-models/src/client/RetryPolicyEditor.tsx:1-9`（"built from the `retryPolicy` schema the owning namespace publishes"）、`:69-71`（`choicesOf` 读 item schema 的 `list`）、`:134-144`（`min === Number.MIN_VALUE` 是"必须为正"的 schema 哨兵）、`src/client/ProviderEditor.tsx:273`（**schema 路径 = `providers.<route>.retryPolicy`，来自 provider 自己的 settings 命名空间**）。
- **宿主现状（两处）**：
  1. `internal/llm/retry_policy.go:201-215` 把 `retryableCodes` 当任意字符串数组校验（只查非空/无重复），不校验是否属官方候选集；`:59-62` 注释明确宿主只能产生 `RATE_LIMIT`/`SERVER`/`TRANSPORT`，`EMPTY_RESPONSE`/`TIMEOUT` 无产生点；`retryPolicy` 由**模型目录条目**承载（`internal/llm/model_catalog.go:1027`、`:1114`），不在 `settings.describe` 命名空间。
  2. `settings.describe` 的命名空间 schema 是**写死**的：`internal/settingsform/settingsform.go:545-562` 的 `schemaFor` 只区分两个命名空间——权限命名空间返回完整的 schemastery envelope（`uid`/`refs`/`const`/`union`/`list`，见 `:547-557`），**其余全部返回单个 `defaultModel` 字符串字段**（`:559-561`）。因此 provider 命名空间现在**没有任何** `providers.<route>.retryPolicy` 节点。注意 `FieldSpec` 本身是扁平的（`internal/settingsform/registry.go:24-38`：只有 `string|boolean|integer` + `Enum`/`Minimum *int64`/`Maximum *int64`），既表达不了 union 分支、嵌套 `backoff`、也表达不了 `min: 5e-324` 这种浮点下界——但 wire 侧的 `Schema any`（`settingsform.go:78`、`:105`）允许直接下发 schemastery envelope，所以不必动 `FieldSpec`，只要让 `schemaFor` 由声明生成而非写死。
- **缺什么**：provider 命名空间的 schema JSON 必须带 `providers.<route>.retryPolicy`：① `type:'union'`，`list` 为两个模式分支（`normal`/`always`），每分支 `dict` 列出字段；② 字段 `meta.default` 给默认值（maxRetries 5、initialDelayMs 500、maxDelayMs 10000、jitterRatio 0.1）；③ 两个 delay 的 `meta.min` 必须是 `5e-324`（`Number.MIN_VALUE`）——UI 用该字面值判定"必须为正"，换成别的正数会退化成通用 min 文案；④ `retryableCodes` 形如 `{type:'array', item:{type:'union', list:[8 个 const]}}`，**必须带 `list`**，否则「Error codes」多选渲染不出来；⑤ `retryableCodes` 至少 1 项（`.min(1)`）；⑥ 明确 `QUOTA`/`MALFORMED_RESPONSE`/`STREAM_CLOSED` 三个新候选"接受但无产生点"的口径。
- **先红测试点**：`TestRetryPolicySchemaPublishesChoices`（`retryableCodes` 的 item 节点含 `list`，恰为官方 8 码顺序）、`TestRetryPolicySchemaPositiveBoundSentinel`（两个 delay 的 `min == 5e-324`）、`TestRetryPolicySchemaDefaults`、`TestRetryPolicySchemaPath`（节点确实在 `providers.<route>.retryPolicy`）、`TestRetryPolicyEmptyCodesRejected`、`TestRetryPolicyUnknownCodeAccepted`（自定义码仍接受）。
- **风险**：中-高。这是 release note「可在模型设置中选择重试条件/次数/等待时间」在**宿主**能否成立的唯一路径；不做则宿主能力弱于官方（违反"只能更强不能更弱"）。`schemaFor` 由写死改为声明生成会同时改动设置面其他命名空间，需与设置线串行。

### R3-G17 新增 `web-deepseek-api` 设置项缺 wire 出口（9 项）
- **官方证据**【文档】catalog:4863-4896；【源码】`official-rc2/.../dsh-web-deepseek-api/src/index.ts:99-109`。
- **宿主现状**：无对应 provider，故无这些设置项。
- **缺什么**：随 R3-G04 落地；`searchApiKeyEnv`/`fetchApiKeyEnv` 必须带 `role:'credential-ref'` 语义（设置界面按凭据引用渲染、不落明文）。
- **先红测试点**：`TestWebDeepSeekAPISettingsSchema`（9 项名字/类型/默认值/上下界逐项）；`TestCredentialRefRolePreserved`。
- **风险**：中（与 G04 绑死）。

## 4. 可等价/不适用项

| 项 | 结论 | 理由 |
|---|---|---|
| `dsh-llm-retry` 全部改动 | **不适用，且它不是设置项** | `lib/` 逐字节相同；【文档】catalog:2397-2399 明确本包无 config（`Config = Readonly<Record<string, never>>`），`retryPolicy` 由 provider 拥有（catalog:2140-2141）。宿主已按官方默认值实现（`internal/llm/retry_policy.go:48-51`、`:63-65`）。 |
| `dsh-schedule` 的"暂停/恢复反馈" | **不适用（UI 侧）** | `dsh-schedule` `lib/` 逐字节相同；`schedule.*` 方法面无增删。改动在 `dsh-client-ui-schedule/lib/client.js`（`toast.*` 文案键、`host.pause`/`host.resume` 标签、删除/清理 toast 与确认流、`CatalogFeedback`），属 `../dsh-ui` 线。宿主只需保持 `schedule.edit` 的 `ok/value.code`（`schedule_conflict`、`schedule_execution_pending`、`input-invalid`+`details.code`）不变——rc.2 未改。 |
| `dsh-jobs` 的"停止任务/关闭会话提前释放资源" | **不适用（本域无对应改动）→ 见未取证** | `dsh-jobs` 只新增 `JobError`；`dsh-jobs-local` 只换错误类型与 `waitAborted`。**未发现**与"提前释放"对应的落点（`ensureOwnerCleanup` 两侧都在）。若 release note 属实，应在 `dsh-session`/`dsh-agent`/`dsh-api-job-controller` 域。 |
| `dsh-otel` / `dsh-host-product-telemetry-otel` / `dsh-anonymous-user-id` / `dsh-session-telemetry-otel` / `dsh-client-product-analytics` | **不适用（零改动）** | 五包 `lib/` 全部逐字节相同；`productAnalytics.*` 方法面无增删；分析侧 config 无 `mode` 字段（catalog:567-577）。 |
| `dsh-settings` / `dsh-config-editor` / `settings.*` | **不适用（零改动）** | `lib/` 逐字节相同，方法面无增删；设置面变化全部来自各插件 Config schema。 |
| `dsh-plugin-detail` / `dsh-plugin-package-inventory-deepseek` / `dsh-deepseek-llm-api-extensions` | **不适用（零改动）** | `lib/` 逐字节相同。 |
| `dsh-host-plugin-inventory` 的 `fiberPhase`/`diagnostic` | **已具备，不适用** | rc.1 就有这两字段，rc.2 只重生成 wire 文件；宿主 `internal/transport/handlers_plugin_inventory_official.go:51-52` 已实现。 |
| `dsh-credentials` 6 个"文件改动" | **不适用（实质零改动）** | 6 项里 3 个 README、2 个 `lib` 文件仅一句注释措辞 + sourceMappingURL、1 个 package.json。真正的掩码在 `dsh-credentials-local`。 |
| `dsh-user-questions` / `dsh-action-cards` | **不适用（方法面零变化）** | 方法面无增删；宿主已有等价实现（`internal/actioncards/registry.go`、`:projection.go`；`userQuestions` 在 `internal/transport/handlers_agent_official.go`）。 |
| `dsh-settingsform` | **不存在** | 两版本都无此包名。 |
| `plugin_manager` **工具**的 `updatable` | **不适用** | 【文档】`ug/plugin-update-method/guide.zh.md:16` 明确该工具从未返回此字段，保持不变。 |
| `web-search-billing` 的 TS 构造选项迁移 | **部分不适用** | `DeepSeekSearchApiOptions`/`FetchApiOptions` 的 `baseURL`/`auth` 重构是**库内 TS API**；宿主是 Go 侧同形实现，只需保留"有序凭据来源"语义，不需要照搬类名。`AbortError → WEB_ABORTED` 这条**适用**（见 §2.3）。 |

### 未取证（拿不到证据，不猜）

1. **`productAnalytics` 的"离线默认 local"**：`dsh-otel`、`dsh-host-product-telemetry-otel`、`dsh-client-product-analytics` 的源码与 config 里**没有** `mode`/`local` 字段（catalog:567-577、:1873-1895）；判定依据只有"整包 `lib/` 逐字节相同"——即 **rc.1→rc.2 未变**，但"默认 local"这句话在 rc.2 侧没有可直接引用的字面。归 CLI/组合配置面另查。
2. **`ug` 提到的"初始十分钟间隔"**：`ug/account-notification-fields/guide.zh.md:25` 说「初始十分钟间隔不适用时，设置其新增的 `notificationPollIntervalMs`」，但 config-catalog:605-609 只给类型不给默认值。600000ms 这个数**未取证**（可能写在 `ui-account-notifications` 的调用方组合里）。
3. **"停止任务或关闭会话时资源提前释放"**：本域 7 个相关包（`dsh-jobs`、`dsh-jobs-local`、`dsh-tool-jobs`、`dsh-api-job-controller`、`dsh-schedule`、`dsh-tool-schedule`、`dsh-session`-相邻）里没找到对应改动，也未在 19 条 `ug` 中找到该条目（`ug` 19 条全表见 `upgrade-guide/README.md`，本域只有 4 条）。需另派域复核。
4. **`dsh-error` 是否应成为宿主错误基类**：`ug` 无 `dsh-error` 专项条目（它只在 `attachment-error-codes` 里被顺带提到为 `HarnessError` 的新归属）；宿主 Go 侧是否需要"同一个基类"语义**未取证**，只能确认包与 import 迁移事实。
5. **`pluginManager` 的 `gitTransportTimeoutMs`/`githubConnectionTimeoutMs` 默认毫秒值**：catalog:2766-2771 只给含义不给数值（宿主若要对齐需另查 rc.2 源码常量，本次未取）。

## 5. 待用户裁定

1. **是否切默认搜索 provider 到 `deepseek-search-api`**：(a) 先做 R3-G04 再切（官方形状，工作量大）；(b) 保留 `deepseek-official` 为默认、把新 provider 作为可选项（偏离官方默认值但风险低）。官方文档 `ug/web-search-billing` 已把新 provider 的计费语义写死，长期必须做。
2. **`account.ackBonusNotified` / `getUnnotifiedBonuses` 何时下线**：官方 rc.2 已删；宿主已实现且可能被 TUI/GUI 调用。是否允许一个 beta 周期内两者并存（先加新方法、后删旧方法）？
3. **`account.watch` 信封变更的兼容策略**：rc.2 是纯增量（`type` 必填 + `state` 嵌套）。宿主目前是"未接线"状态，直接按新形状实现即可；但如果 `../dsh-ui` 已按 rc.1 平铺形状写好，需要确认谁先改。
4. **`retryPolicy` 的归属与发布位置**：官方把它作为 provider 配置、由设置的 schema 表单渲染；宿主现在放模型目录条目（`internal/llm/model_catalog.go:1027`）。要不要把 provider 配置迁进 `settings.describe` 命名空间并补 R3-G16 的 schema 元数据（含 `min: 5e-324` 哨兵）？这是本域唯一"能力可能弱于官方"的点。
5. **`harnessOrigin` 是否在宿主引入**：rc.2 把通知请求从 `platformOrigin` 拆到 `harnessOrigin`（默认 `https://harness.deepseek.com`）。宿主是否引入第二个 origin，还是让通知继续走 platformOrigin？影响配置面与私署部署。
6. **`QUOTA`/`MALFORMED_RESPONSE`/`STREAM_CLOSED` 三个新重试候选**：接受但不生效（与 rc.1 对 `EMPTY_RESPONSE`/`TIMEOUT` 的处理一致），还是补产出点（需要新的失败分类）？
7. **`dsh-error` 语义是否落到宿主**：只保证错误码值一致，还是也要体现"同一基类"（影响四字段错误的 `code` 归一）？
8. **"停止任务/关闭会话提前释放资源"**（release note 条目）是否另派域复核（见 §4 未取证 3）。
9. **`web.listProviders` 的 `description` 8 语言文案归属**：照抄进 Go 常量，还是只透传 provider 侧注册值（不内置翻译）？
