# E2 · DeepSeek 搜索/抓取 API 两个真 provider（官方 0.2.2-rc.2）

> 任务：`task-17`。写作用域：`modules/search/**`（主）+ `modules/tool/websearch/**`（协同改动，逐条列在 §10）。
> 结论：`deepseek-search-api` 与 `deepseek-fetch-api` 两个 provider **真实 HTTP 调用**落地，
> rc.2 基础组合默认 `searchProvider=deepseek-search-api` **真正生效**（`baseline.searchRegistered=true`，
> `resolution.search.source=baseline`），全部测试用 `httptest` 桩、**零真出网**（§7 有出网证明）。

---

## 0. 变更文件一览

| 文件 | 类型 | 说明 |
|---|---|---|
| `modules/search/webapi_config.go` | 新增 | 官方 Config 九个设置项 + 默认/边界 + 环境变量面 + 两个能力的 options 快照 |
| `modules/search/webapi_auth.go` | 新增 | 凭据决策树（`resolveTarget` / `ruleSources` / official·account·api-key·misconfigured 四种源 / 缺凭据文案 / `toolsBase`） |
| `modules/search/webapi_transport.go` | 新增 | 一次 POST 的调度：凭据选择 → 限速等待 → 拒绝重定向 → 错误映射（401 账号撤销、`WEB_ABORTED`）→ 解码 |
| `modules/search/webapi_search.go` | 新增 | `deepseek-search-api` provider：请求形状、`mapSearchResponse` 严格校验、滑动窗口限速 |
| `modules/search/webapi_fetch.go` | 新增 | `deepseek-fetch-api` provider：`{url}` → `{content}` |
| `modules/search/search.go` | 改 | 注册两个新 provider + rc.2 默认 pin 规则（条件生效）+ 包文档的配置面 |
| `modules/search/search_test.go` | 改 | 既有装配测试从 rc.1 口径更新到 rc.2 真值（见 §10.4） |
| `modules/search/webapi_red_test.go` | 新增 | **先红**批次（先红后绿证据，§6） |
| `modules/search/webapi_test.go` | 新增 | 第二批判定（端到端 / 校验矩阵 / 账号 401 / live 配置 / 缺凭据不发请求） |
| `modules/tool/websearch/providers.go` | 改 | 13 行凭据/配置项投影 + pin 来源新增 `baseline`（协同，§10.1） |
| `modules/tool/websearch/websearch.go` | 改 | 3 处 provider id / 文案（协同，§10.2） |
| `modules/tool/websearch/providers_test.go` | 改 | 6 处"缺口态"断言改成 rc.2 真值 + 2 个新测试（协同，§10.3） |
| `modules/tool/websearch/providerapi/providerapi_test.go` | 改 | 清单条数/baseline/选中真值 + 新增 `web.listProviders` 取证测试（协同，§10.3） |

未改：`internal/spi/**`、`internal/transport/**`、`cmd/dshd/**`、`AGENTS.md`、`CHANGELOG.md`、其它域。

> **集成注意（多写者同树）**：本工作树里 `modules/tool/websearch/{providers.go,providers_test.go,rc2_red_test.go,providerapi/}`
> 与 `modules/tool/websearch/websearch.go` 的大块 diff **是 A3 的未提交工作**，不是 E2 的。
> E2 在这些文件里的改动只有：`providers.go` 的 13 行环境变量投影 + 2 个 pin 来源分支、
> `websearch.go` 的 3 行文案、`providers_test.go`/`providerapi_test.go` 里逐条列在 §10.3 的断言与 3 个新测试。
> `modules/search/**` 全部是 E2 的（含新增 7 个文件、改 2 个文件）。

---

## 1. 官方证据（只读，逐条对上宿主落点）

| 事实 | 官方 file:line | 宿主落点 |
|---|---|---|
| Search provider id = `deepseek-search-api` | `dsh-web-deepseek-api/src/search.ts:13` | `webapi_config.go` `DeepSeekSearchAPIProviderID` |
| Fetch provider id = `deepseek-fetch-api` | `.../src/fetch.ts:13` | 同上 `DeepSeekFetchAPIProviderID` |
| 官方 base `https://api.deepseek.com/v1/tools`（含 `/v1/tools`） | `.../src/search.ts:16`、`index.ts:127` | `DeepSeekWebAPIBaseURL`、`toolsBase()` |
| `count` 默认 10、`1..50` | `index.ts:100`、`search.ts:19` | `WebAPISettings.Count` + `searchCountMax` |
| `searchRequestsPerSecond` 默认 5、`>=1` | `index.ts:101`、`search.ts:22` | `WebAPISettings.RequestsPerSecond` |
| `sendOriginalQuestion` 默认 true | `index.ts:102` | `WebAPISettings.SendOriginalQuestion` |
| `searchBilling` / `fetchBilling` 闭集 `auto|account|api-key`、默认 `auto` | `index.ts:103-104` | `Billing` 类型 + `parseBilling` |
| `searchBaseURL` / `searchApiKeyEnv` / `fetchBaseURL` / `fetchApiKeyEnv` | `index.ts:105-108` | `WebAPISettings` 四字段 |
| 旧 `baseURL` / `apiKeyEnv` / `apiKey` **不再是 schema 字段**（加载不报错、无效果） | `index.ts:58-63` | 三个 legacy 环境变量：读得到、完全不参与 |
| 每次操作读一次设置（`resolveOptions()`） | `search.ts:86-90`、`fetch.ts:42-43`、`index.ts:170-177` | provider 的 `Resolve func() …Options`，每次入口调一次 |
| 请求体 `{query, count: min(count, maxResults ?? count), need_content:false, ori_question?}` | `search.ts:104-109` | `webAPISearchBody` |
| `ori_question`：空白折叠 + 前 128 字符 + 空白则不发 | `search.ts:154-164`、`index.ts:183-186` | `boundedOriginalQuestion` |
| `mapSearchResponse` 严格判据（url 非空、title/snippet 必须 string、summary 只能 string、date 必须存在且 string\|null、空值字段缺席、`truncated:false`） | `search.ts:48-64` | `mapDeepSeekSearchResponse` / `decodeWebAPISearchItem` |
| 限速：一秒窗口最多 N 次，等待算调用方超时 | `search.ts:121-136`、`index.ts:67-72` | `waitForRequestSlot`（滑动窗口，含首毫秒） |
| 取消/等待可中断 | `search.ts:140-152`、`transport.ts:411-425` | `p.wait()` select ctx；错误 → `WEB_ABORTED` |
| Fetch 请求 `{url}`、响应 `{content:string}`、结果 `{url, statusCode:200, body:{kind:'text',content}, truncated:false}` | `fetch.ts:51-62` | `webAPIFetchBody` / `decodeWebAPIFetchResponse` / `spi.FetchResult` |
| 认证头：API key `Authorization: Bearer`；账号 `x-dsh-auth-token` | `transport.ts:29-47`、`:321-336` | `webAPIAuthHeaders` |
| 协议头 `content-type`/`accept`/`user-agent: deepseek-harness/0.0.1` | `transport.ts:194-206`、`:10` | `dispatchWebAPI` |
| 重定向失败（`redirect:'error'`） | `search.ts:67-68`、`transport.ts:196` | `webAPIClient` 的 `CheckRedirect` → provider error |
| HTTP 错误文案 `DeepSeek API error (HTTP n): detail` + 端点 + 恢复指引 | `transport.ts:217-227`、`:273-299` | `webAPIErrorDetail` / `webAPIEndpointError` |
| 401 + 账号令牌 → 先撤销令牌，再报"重新登录，端点不用改" | `transport.ts:237-245`、`:264-271` | `dispatchWebAPI` 的 401 分支（`reject`） |
| 非法响应体 → `WEB_PROVIDER_ERROR` "DeepSeek returned an invalid response body" | `transport.ts:156-161` | `webAPIInvalidResponse` |
| 缺凭据 → `WEB_PROVIDER_CREDENTIAL_MISSING` + 规则专属文案 | `transport.ts:343-347`、`index.ts:324-353` | `missingMessage` / `ownMissingMessage` |
| 凭据源设置解析失败 → `WEB_PROVIDER_MISCONFIGURED`（走到才失败） | `transport.ts:317`、`index.ts:300-315` | `misconfiguredSource` + `WebProviderMisconfigured` |
| 基础组合默认 `searchProvider: deepseek-search-api` / `fetchProvider: http` | `dsh-base/cordis.patch.yml:487-488`（rc.1 `:470-471`） | `BaseBundleSearchProviderID` / `BaseBundleFetchProviderID` |

---

## 2. 配置面（官方九项 → 宿主环境变量）

| 官方字段 | 宿主环境变量 | 默认 | 边界 | 宿主行为 |
|---|---|---|---|---|
| `count` | `DSHD_WEB_SEARCH_COUNT` | 10 | 1..50 | 越界/非法 → 回落 10（不静默改语义） |
| `searchRequestsPerSecond` | `DSHD_WEB_SEARCH_REQUESTS_PER_SECOND` | 5 | >=1 | 非法 → 回落 5 |
| `sendOriginalQuestion` | `DSHD_WEB_SEND_ORIGINAL_QUESTION` | true | bool（1/true/yes/on、0/false/no/off） | 非法 → 回落 true |
| `searchBilling` | `DSHD_WEB_SEARCH_BILLING` | `auto` | `auto│account│api-key` | 非法 → 回落 auto |
| `fetchBilling` | `DSHD_WEB_FETCH_BILLING` | `auto` | 同上 | 同上 |
| `searchBaseURL` | `DSHD_WEB_SEARCH_API_BASE_URL` | 空（= 官方 base） | 合法 http(s) base，含路径前缀 | 非空即整体接管端点 |
| `searchApiKeyEnv` | `DSHD_WEB_SEARCH_API_KEY_ENV` | 空（= `DEEPSEEK_API_KEY`） | 环境变量名 | 非空即整体接管凭据 |
| `fetchBaseURL` | `DSHD_WEB_FETCH_API_BASE_URL` | 空 | 同上 | 同上 |
| `fetchApiKeyEnv` | `DSHD_WEB_FETCH_API_KEY_ENV` | 空 | 同上 | 同上 |
| （宿主接缝）会话路由 | `DSHD_WEB_LLM_PROVIDER` | 空 | `deepseek-account` / `deepseek-official` / 其他 | `auto` 的会话级判定输入 |
| （官方提到）账号路由 base | `DEEPSEEK_BASE_URL` | 空 → `https://api.deepseek.com` | 合法 URL | 账号源端点 = origin + `/v1/tools` |
| 旧 `baseURL` | `DSHD_WEB_BASE_URL` | — | — | **无效果**（只被投影成 `role=legacy` 让人看见） |
| 旧 `apiKeyEnv` | `DSHD_WEB_API_KEY_ENV` | — | — | 无效果 |
| 旧 `apiKey` | `DSHD_WEB_API_KEY` | — | — | 无效果（`TestRC2LegacyFieldsHaveNoEffect` 钉住） |

凭据名：官方 `DEEPSEEK_API_KEY`（`index.ts:130`）；宿主另认既有别名 `DSHD_SEARCH_API_KEY`（与 rc.1 的
`deepseek-official` provider 共用同一字段，见 `modules/search/deepseek.go:77`）。两者喂同一个字段，不是隐藏优先级链。

---

## 3. billing 决策树（官方 `ruleSources`，`index.ts:247-259`）

先判"能力自有端点"（`index.ts:221-231`）：

```
ownBaseURL != "" || ownRef != ""  →  [ api-key(base = ownBaseURL ?? 官方base, ref = ownRef ?? DEEPSEEK_API_KEY) ]
                                     缺 key 文案 = ownMissingMessage（点名字段 searchApiKeyEnv/fetchApiKeyEnv）
否则按 rule：
  billing != auto                 →  billing == account ? [账号] : [API key]
  billing == auto && 路由=deepseek-account  → [账号, API key]      （先账号）
  billing == auto && 路由=deepseek-official → [API key]
  billing == auto && 其他/无路由            → [API key, 账号]      （先 key，没 key 才账号）
```

要点（照官方，不"顺手优化"）：

1. **请求失败不换下一路凭据**：凭据只在发请求**前**按序取第一路有效的（`transport.ts:56-59`）。
2. **账号令牌 401 先撤销再报错**，且文案明确"端点不需要改"。
3. 任一路**设置解析不出来**（端点不是合法 http(s)）→ 走到它就 `WEB_PROVIDER_MISCONFIGURED`，不换端点、不换凭据。
4. 缺凭据的文案按 rule 分五种（`index.ts:324-353`），逐条落在 `missingMessage`，测试逐条钉住关键短语。

> **与任务描述的一处措辞校正**：任务书里写"其他会话/会话外优先登录账号、无账号令牌用 API key"，
> 而 rc.2 **源码与升级指南**都是反过来的——`other-conversation = [officialSource, accountSource]`
> （`index.ts:254`），升级指南原文 "other conversations and operations outside a Session now use the
> signed-in account **when no key exists**"（`ug/v0.2.2-rc.1/web-search-billing/guide.md:12`）。
> 本次以源码 + 指南为准实现（先 API key，再账号），并按源码写了测试。

---

## 4. 实现要点（为什么这么写）

1. **拆四个文件而不是一个**：配置面 / 凭据树 / 调度 / 两个能力，与官方模块切分（`index.ts` / `transport.ts` /
   `search.ts` / `fetch.ts`）一一对应，改一处不会牵动另外三处。
2. **`Resolve func() …Options`**：官方 `resolveOptions()` 每个操作入口读一次快照，避免"一次搜索混两份设置"。
   宿主这里把这个接缝做成可替换函数，于是"live 配置"（`TestRC2ConfigIsLivePerOperation`）与"直接把配置摆在测试里"
   都走同一条代码。
3. **限速做成可注入时钟**（`Now` / `Wait`）：窗口语义（含首毫秒、等到 oldest+1001ms）能用**精确断言**证明，
   而不是靠"跑一秒看差不多"；另有一条真实时钟测试兜底定时器路径。
4. **HTTP 客户端恒拒绝重定向**（克隆调用方的 client 再覆盖 `CheckRedirect`）：等价官方 `redirect:'error'`，
   且不污染测试共享的 `httptest` 客户端。
5. **错误文案清洗已发出的凭据**（`redactSecret`）：错误体可能回显 key，宿主侧兜一刀。
6. **默认 provider 的装配规则**（`search.go:68-115`）：

```
env 钉死 > （未钉死 && 基线默认 provider 当前可用 → 钉基线默认）> （未钉死 && 不可用 → 空 pin，退回自动选唯一可用）
```

   为什么不是无条件钉：官方的默认值配在 `cordis.patch.yml:487-488`，官方宿主里 DeepSeek 那两条模型路由
   （账号 / API-key）会解析出凭据；本宿主还没有这两条路由插件，Search/Fetch API 的凭据只能来自
   `DEEPSEEK_API_KEY`。**无条件钉**会让"只配了 SearxNG"的部署从"能用"退化成
   `WEB_PROVIDER_CONFIGURED_UNAVAILABLE`（能力倒退）；**条件钉**在正常部署（有 key）下与官方完全一致，
   且 `web.listProviders` 会把 `baseline` + `available` + `resolution` 三条都如实报出来，不撒谎。

---

## 5. 默认 provider 真生效（`web.listProviders` 实测）

`go test ./modules/tool/websearch/providerapi/ -run TestOfficialListProvidersEvidence -v`
（日志 `tmp/GO8-RC2/logs/e2-listproviders.log`；环境仅 `DEEPSEEK_API_KEY=sk-test`）：

```json
"search": [
  { "id": "deepseek-official",   "available": true },
  { "id": "deepseek-search-api", "available": true, "default": true, "selected": true },
  { "id": "searxng",             "available": false }
],
"fetch": [
  { "id": "deepseek-fetch-api", "available": true },
  { "id": "http",               "available": true, "default": true, "selected": true }
],
"resolution": {
  "search": { "configured": "deepseek-search-api", "source": "baseline", "selected": "deepseek-search-api" },
  "fetch":  { "configured": "http",                "source": "baseline", "selected": "http" }
},
"baseline": { "search": "deepseek-search-api", "searchRegistered": true,
              "fetch": "http",                 "fetchRegistered": true }
```

即：**两个新 provider 都在清单里**；`baseline.searchRegistered` 从 `false` 变 `true`；
`resolution.search.selected = deepseek-search-api`（不再走"未注册就如实报告"的兜底）。
`source: "baseline"` 是本次新增的第四种来源（env / baseline / config / auto），见 §10.1。

---

## 6. 先红 → 后绿

1. **先写红测**：`modules/search/webapi_red_test.go`（18 个测试函数 / 31 个子测试），全部只依赖已冻结的公开形状。
   在实现落地前跑：

```
$ go test ./modules/search/ -run TestRC2
modules\search\webapi_red_test.go:54:5: undefined: DeepSeekSearchAPIProviderID
modules\search\webapi_red_test.go:57:5: undefined: DeepSeekFetchAPIProviderID
...（共 10+ 处 undefined，build failed）
FAIL dshgo/modules/search [build failed]
```

   完整日志：`tmp/GO8-RC2/logs/e2-red.log`。

2. **实现后转绿**（最终一次合并跑，日志 `tmp/GO8-RC2/logs/e2-green-final.log`）：

```
$ go test ./modules/search/ ./modules/tool/websearch/... -count=1 -v
→ PASS=70  FAIL=0  子测试 34   （search 35 + websearch 35，含 providerapi）

$ go build $(go list ./... | grep -v /tmp/)   → 0（97 个真包）
$ go vet   $(go list ./... | grep -v /tmp/)   → 0
$ HTTP_PROXY=http://127.0.0.1:1 go test ./modules/search/ ./modules/tool/websearch/... → 0（零真出网）
```

3. **红的三种性质都如实记录**：
   - 编译红（新 API 尚未存在）→ 实现后消失；
   - 断言红（我按"读代码猜"写的两条断言与官方文案不符）→ 以源码/文案为准**改测试**，不是改实现：
     `TestRC2BillingDecisionTree` 的账号端点必须是 `origin + /v1/tools + /search`（`index.ts:374-377`）；
     `TestRC2MissingCredentialMessages` 的 `other-conversation` 文案本来就不点 `searchBilling`（`index.ts:339-342`）；
   - 既有测试红（A3 的 rc.1 口径断言 + 本包旧装配断言）→ 见 §10.3 / §10.4，逐条改成 rc.2 真值并写明理由。
     现场日志：`tmp/GO8-RC2/logs/e2-green-search1.log`（`TestNewWebProvidersFromEnv: 注册的 provider 不对:
     [deepseek-official deepseek-search-api searxng]`）、`e2-green-websearch1.log`（A3 清单/baseline/选中 6 处）。

---

## 7. httptest 证据（全部本地桩，禁真出网）

| 测试 | 钉住的官方事实 |
|---|---|
| `TestRC2DeepSeekAPIProviderIDs` | 两个 id、base、count=10、rps=5、默认 pin 常量（`search.ts:13-22`、`cordis.patch.yml:487-488`） |
| `TestRC2RegistersDeepSeekAPIProviders` | 装配后搜索 3 个 / 抓取 1 个 provider 在册 |
| `TestRC2DefaultSearchProviderTakesEffect` | 有凭据→默认生效；无凭据→不硬钉（仍是 `WEB_PROVIDER_UNAVAILABLE`） |
| `TestRC2SearchAPIOfficialRequestShape` | 路径 `/search`、`Bearer`、`content-type/accept/user-agent`、body `query/count/need_content=false/ori_question`、响应字段映射与"空值缺席" |
| `TestRC2SearchAPICountBounds` | `min(count, maxResults)`、count 缺省、上限 50（`search.ts:106`、`index.ts:100`） |
| `TestRC2SearchAPIRateLimitsRequests` | 假时钟：limit=2 时第 3 次等待**恰好 1001ms**（`search.ts:134`） |
| `TestRC2SearchAPIRateLimitWaitAborts` | 等待被取消 → `WEB_ABORTED` |
| `TestRC2SearchAPIRateLimitRealClock` | 真实时钟：2 rps 跑 3 次 ≥ 900ms（定时器路径） |
| `TestRC2FetchAPIOfficialRequestShape` | 路径 `/fetch`、body `{url}`、响应 `{content}`、结果 `{url,200,text}`（`fetch.ts:51-62`） |
| `TestRC2SearchAPIErrorMapping` | HTTP 500 + 上游 detail + 端点 + 恢复字段名；非法响应体；取消 → `WEB_ABORTED`（`transport.ts:217-262`） |
| `TestRC2SearchAPIRejectsRedirect` | 重定向失败（`redirect:'error'`） |
| `TestRC2BillingDecisionTree` | 8 个子测试覆盖 auto×三条路由 / account / api-key / 无凭据错误码 |
| `TestRC2OwnEndpointAndKeyTakePrecedence` | 自有 `*BaseURL`+`*ApiKeyEnv` 压过 billing（`index.ts:221-231`） |
| `TestRC2OwnApiKeyEnvOnlyUsesOfficialBase` | 只设 `*ApiKeyEnv` → 用官方 base + 该引用 |
| `TestRC2MissingCredentialMessages` | 五种规则文案的关键短语（`index.ts:324-353`） |
| `TestRC2SendOriginalQuestionFalseOmitsField` | 关掉就不发 `ori_question` |
| `TestRC2LegacyFieldsHaveNoEffect` | 旧 `baseURL/apiKeyEnv/apiKey` 无效果（`index.ts:61-62`） |
| `TestRC2SearchAPIResponseValidation` | 12 个响应形状（含 `summary:null` 非法、缺 `date` 非法、空字段折叠缺席） |
| `TestRC2FetchAPIResponseValidation` | 5 个响应形状（`content` 必须 string；空串合法） |
| `TestRC2AccountTokenRejectedIsRevoked` | 401 + 账号 → 撤销**那一个**令牌 + 文案含"重新登录"（`transport.ts:237-245`） |
| `TestRC2MisconfiguredBaseURL` | 端点写坏 → `WEB_PROVIDER_MISCONFIGURED` 且点名字段 |
| `TestRC2ConfigIsLivePerOperation` | 每次操作重读设置（`resolveOptions()`） |
| `TestRC2MissingCredentialSendsNothing` | 缺凭据时**一个请求都不发** |
| `TestRC2SeamSearchEndToEnd` / `TestRC2SeamFetchEndToEnd` | 装配 → 选择 → 真 HTTP → 接缝截断/正文 |

**"零真出网"证明**：把 `HTTP_PROXY/HTTPS_PROXY` 指向死端口再跑（Go 对 loopback 直连，桩不受影响；
任何打到 `api.deepseek.com` 的尝试都会失败）：

```
$ HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 go test ./modules/search/ ./modules/tool/websearch/ -count=1
ok  dshgo/modules/search          1.133s
ok  dshgo/modules/tool/websearch  1.071s
```

---

## 8. 限速证据（两档）

1. **可精确断言的一档**（假时钟，`TestRC2SearchAPIRateLimitsRequests`）：`requestsPerSecond=2` 连发三次，
   第 3 次等待 `1001ms`——正是官方的 `oldest + 1001 - now`（`search.ts:134`，含首毫秒的窗口）。
2. **真实定时器一档**（`TestRC2SearchAPIRateLimitRealClock`，`-short` 下跳过）：同配置连发三次，总耗时 ≥ 900ms。
3. 限速实例属于 provider 实例（官方 `search.ts:72-73` 的 `starts` 字段），即"这个组合一秒内最多发 N 次"；
   进程内 `dshd run` 子进程各建一份接缝，互不协调——与官方注释"Other processes and clients on the same
   account share the account limit without coordination"（`index.ts:67-71`）一致。

---

## 9. 与官方的差异（逐条给理由，不含"边界"敷衍）

| # | 差异 | 理由 / 影响 |
|---|---|---|
| 1 | `available()` 用"真能取到凭据"判定，官方 `available()` 只看源存在（令牌读取留给 `ready()`） | 宿主 `spi.SearchProvider.Available()` 同时驱动**选择**与工具可见性；乐观判定会把肯定取不到凭据的 provider 选上。净效果与官方 `ready()` 一致 |
| 2 | 默认 provider 是**条件生效**（有凭据才钉），官方无条件钉在配置里 | 宿主没有账号/API-key 两条模型路由插件，DeepSeek 凭据只能来自 `DEEPSEEK_API_KEY`；无条件钉会让 SearxNG-only 部署能力倒退（§4.6、§5） |
| 3 | 配置从环境变量读，非法值回落默认；官方由 schema 拒绝加载 | 宿主没有 cordis schema 层；本仓既有约定是"写坏配置不该把能力变成永远拒绝"（`websearch.go:intFromEnv` 同规则） |
| 4 | `ori_question` 由显式接缝注入，官方从会话最后一条 user prompt 读 | `spi.SearchProvider.Search(ctx, query, limit)` 没有该入参（§11.3） |
| 5 | 重定向错误文案是 Go `url.Error` 文本 | 语义等价（都失败、都是 `WEB_PROVIDER_ERROR`），只是底层错误文本不同 |
| 6 | 凭据名另认宿主别名 `DSHD_SEARCH_API_KEY` | 与既有 `deepseek-official` provider 共用同一字段；不引入隐藏优先级 |
| 7 | `count`/`rps` 用 int；官方 zod `.step(1)` 也是整数 | 等价 |

---

## 10. 协同改动（`modules/tool/websearch/**`，逐条）

> 原则：只改"清单/选择投影的真值"与"文案里的 id 列表"，不动工具行为、不动限速/预算/上限逻辑。

### 10.1 `providers.go`
1. `credentialEnvNames` 追加 13 行：十个 rc.2 配置/接缝变量（count、rps、sendOriginalQuestion、两个 billing、
   两个 API base、两个 apiKeyEnv、LLM 路由）+ 三个 legacy 变量（`role=legacy`，让界面能说"你设的这条不生效"）。
2. `Role` 字段的取值说明扩到 `selection / endpoint / credential / model / billing / limit / route / legacy`。
3. `searchPinSource` / `fetchPinSource` 新增 `baseline` 来源：pin 等于 rc.2 基础组合默认值且 env 没钉时，
   来源报 `baseline`（否则会谎报成 `config`）。

### 10.2 `websearch.go`
- `searchHint` 里两处 provider id 列表补 `deepseek-search-api`（`WebProviderConfiguredMissing` / `WebProviderAmbiguous`），
  一处"该配什么"补 DeepSeek 搜索的两条路。**3 行**，只改文案，不改判定。

### 10.3 测试（A3 的缺口态断言 → rc.2 真值）
- `providers_test.go`：清单 `deepseek-official,searxng` → `deepseek-official,deepseek-search-api,searxng`；
  抓取清单 `http` → `deepseek-fetch-api,http`；`baseline.searchRegistered` `false` → `true`；
  选择来源 `auto/deepseek-official` → `baseline/deepseek-search-api`；
  "钉死未注册 id"的样例从 `deepseek-search-api`（现已注册）换成真正不存在的 `no-such-provider`；
  若干 `Search[1]` / `Fetch[0]` 位置断言改成 `entryOf(..., id)`（清单按 id 排序，位置本来就脆）。
  新增 2 个测试：`TestRC2BaseBundleDefaultsHaveSingleSource`（三处字面量同源守卫）、
  `TestRC2NewProvidersAppearInCatalog`。
- `providerapi/providerapi_test.go`：条数 `2/1` → `3/2`；baseline 已注册；选中值 → `deepseek-search-api`；
  `source=baseline`。新增 `TestOfficialListProvidersEvidence`（§5 的 JSON 取证）。

### 10.4 `modules/search/search_test.go`（本域既有测试）
`TestNewWebProvidersFromEnv` 原断言"只有 deepseek-official/searxng、选中 deepseek-official、两个都配就 ambiguous"，
按 rc.2 真值重写：注册集合、默认 pin 生效、SearxNG-only 部署仍自动选中 SearxNG、无凭据时不硬钉、
抓取侧 pin 为 `http`（`http` provider 由 `modules/tool/webfetch` 注册，本函数只造搜索侧）。

---

## 11. 未实现项（逐条：缺什么、缺在哪、要补什么）

1. **登录账号令牌源未接**（影响 `account` billing 与 `auto` 的账号回落）。
   - 现状：`WebAPIAccount` 接缝已就位（`Configured` / `ResolveToken` / `RejectToken` / `BaseURL`），生产装配留空
     → `account` billing 必报 `WEB_PROVIDER_CREDENTIAL_MISSING`（文案指导登录），`auto` 走 API key。
   - 缺口位置：`internal/spi` 没有账号服务键（`keys.go` 只有 tools/fs/exec/jobs/history/search/subagent），
     `modules/search` 拿不到 `internal/account.Service` 的令牌。要补：`internal/spi` 新增账号键 + `cmd/dshd`
     的模块装配注入。两者都**不在本任务写作用域**。
2. **会话模型路由未由 serve 逐轮注入**（影响 `auto` 的会话级判定）。
   - 现状：路由 id 由 `DSHD_WEB_LLM_PROVIDER` 传入，生产默认空 → 会话外语义（先 key 后账号）。
   - 缺口位置：`cmd/dshd/serve_loop.go:147-168` 只往子进程传 `DSHD_LLM_BASE/KEY/API`，没有 provider id
     （宿主 llm provider id 是 API 形状，还没有官方 `deepseek-account`/`deepseek-official` 路由插件）。
     要补：路由插件 + serve 传 id（`cmd/dshd/**`、`internal/llm/**`，本任务外）。
3. **`ori_question` 生产未接线**。
   - 现状：`DeepSeekSearchAPI.OriginalQuestion` 接缝已就位；生产装配不注入 → 不发该字段（官方在没有 prompt 时同样不发）。
   - 缺口位置：`internal/spi.SearchProvider.Search(ctx, query, limit)` 没有"触发问题"入参，工具层也无处取会话最后一条
     user prompt。要补：SPI 可选接口或工具层注入（`internal/spi/**`、`modules/tool/websearch/**` 的接口面，本任务外）。
4. **provider 的 `title`/`description`（官方 8 语言）未投影**：`spi.SearchProvider`/`FetchProvider` 没有元数据字段，
   清单里字段缺席（符合官方"没声明就缺席"语义），界面显示 id。A3 §8.4 已登记同一缺口。
5. **`web/providers-changed` 事件未实现**：本仓 `web.listProviders` 是拉取式（每次调用重投影），A3 §8.3 已登记。
6. **`ready(llmProvider)` 的会话级可用性未投影**：清单里 `available` 是本地 `Available()`，不含"这条 chat 路由上能不能认证"。
   A3 §8.5 已登记；与 §11.2 同一缺口。
7. **`count` 的"更小请求上限"只来自调用方 `limit`**：官方还有 `request.maxResults`（与 `spi` 的 `limit` 语义一致，已覆盖）。

---

## 12. 外部红 / 未验证（**不是**本任务改动造成）

1. **`go build ./...` 全仓口径红**：`tmp/GO8-RC2/d3-backup/**` 是别的 agent 放在 `tmp/` 里的**源码副本**，
   会被 `./...` 当包编译（`undefined: Loop / Event …`）。本任务验证口径改为
   `go list ./... | grep -v "/tmp/"`（**96 个真包**）：`go build` = 0、`go vet` = 0
   （日志 `tmp/GO8-RC2/logs/e2-build-real.log`、`e2-vet-real.log`）。
2. **并发写者的中间态**（不同时刻抓到；我的包不受影响，恢复后重跑全绿）：
   - `internal/transport/server.go`：`syntax error: unexpected keyword case`、随后 `s.officialHost returns 1 value`（已恢复）；
   - `modules/tool/schedule/**`：`t.view/t.edit/normalizeTarget/rruleNext/updateArgs… undefined`（反复在改）；
   - `internal/transport/handlers_job_terminal_official.go`（`undefined: jobs`）与
     `internal/settingsform/settingsform.go`（`undefined: deepCloneMap`）也各自短时间处于中间态。
   依赖这些包的 `modules/tool/websearch/providerapi` 在那些时刻会 `build failed`；**恢复后多次重跑均绿**
   （`tmp/GO8-RC2/logs/e2-green-websearch-v.log`、`e2-listproviders.log`、`e2-nonetwork.log`）。
3. **全量测试结果**（`go list ./... | grep -v "/tmp/"` 的 96 包，`tmp/GO8-RC2/logs/e2-full-suite.log`）：
   **86 个包 ok**；唯一红包是 `cmd/dshd`，红点是**工具表**：
   `TestToolTableSnapshot`（工具表变了）、`TestWriteToolsDeclareCaps`（新工具 `schedule_edit` 没有认领能力）——
   两者都是 schedule 域新增工具（`modules/tool/schedule/**`、`cmd/dshd/snapshot_test.go`、
   `cmd/dshd/testdata/tools.snapshot.json` 当前都在被并发改动）造成，**与本任务无关**：E2 没有新增/删除任何工具，
   只新增两个 web provider。
4. **`-race` 未验证**：本机 `CGO_ENABLED=1 go test -race` 报 `cgo: C compiler "gcc" not found`
   （`tmp/GO8-RC2/logs/e2-race.log`）；按本仓规矩记 **UNVERIFIED**，有 MinGW 工具链时补跑
   `scripts/race.sh`。限速窗口的 `starts` 切片有 `sync.Mutex` 保护，读路径无共享写。
5. **真实端点联调未做**（按任务要求"禁止真出网测试"）：`DeepSeekWebAPIBaseURL` 只在
   "没配 `*BaseURL`" 时作为默认值，未被任何测试真实访问。

---

## 13. 复核命令

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
cd "D:/DeepSeek Harness/default-workspace/DSH-GO/.agents/worktrees/dsh-bdb66fbf-95ba"

# 本任务两个包
go build ./modules/search/ ./modules/tool/websearch/... && go vet ./modules/search/ ./modules/tool/websearch/...
go test ./modules/search/ ./modules/tool/websearch/... -count=1

# 出网证明（桩不受影响，外部端点必失败）
HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 go test ./modules/search/ ./modules/tool/websearch/ -count=1

# 全仓口径（排除 tmp/ 里别人的源码副本）
go list ./... | grep -v "/tmp/" > /tmp/pkgs.txt
go build $(cat /tmp/pkgs.txt) && go vet $(cat /tmp/pkgs.txt)
```

日志：`tmp/GO8-RC2/logs/e2-*.log`（本 worktree）。
**同一份报告与全部日志已镜像到主工作树**：`D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/GO8-RC2/reports/E2-deepseek-web-api.md`
与 `D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/GO8-RC2/logs/e2/`（Lead 无需切 worktree）。
