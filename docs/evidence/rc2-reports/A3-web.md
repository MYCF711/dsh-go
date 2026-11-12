# A3 · `web.listProviders` + 搜索/抓取 provider 默认 + 10 查询 / 10 来源（官方 0.2.2-rc.2）

> 目标：把宿主（Go 重写 DSH）对齐官方 `0.2.2-rc.2` 的 web 能力面。
> 取证对象：`tmp/GO8-RC2/official-rc2/`（0.2.2-rc.2 真机包）与 `tmp/GO8-RC2/official-rc1/`（0.2.2-rc.1 对照）。
> **上限结论：有代码证据**（不是照抄 release note，见 §3）。
> 本文件只写事实与接线方式；代码在 worktree `dsh-bdb66fbf-95ba`，未 commit/push。

---

## 0. 结论摘要

| 项 | 结论 |
|---|---|
| `web_search` 查询上限 | **4 → 10**（有证据：`dsh-tool-web/src/search.ts:23`，rc.1 编译产物 `lib/index.js:28` 是 4） |
| `web_search` 来源上限 | **8 → 10**（有证据：`src/search.ts:20`，rc.1 `lib/index.js:26` 是 8） |
| schema 广告文案 | `1–${maxQueries} search queries; their results are merged.` → 宿主改为按**生效上限**生成 `1–10 条查询…` |
| 基础组合 `searchProvider` | **`deepseek-official` → `deepseek-search-api`**（`dsh-base/cordis.patch.yml` rc.2 `:487`，rc.1 `:470`） |
| 基础组合 `fetchProvider` | `http`（rc.1 与 rc.2 相同；rc.2 `:488`，rc.1 `:471`） |
| 60s 搜索预算 | rc.1「this shipped DeepSeek route gets 60s」→ rc.2「**whichever provider serves it**」；宿主从"按 provider 判"改为**组合级恒定 60s** |
| `web.listProviders` | 新增方法（rc.1 `dsh-web` 里**根本没有**这个词：`grep -c listProviders official-rc1/.../dsh-web/lib/index.js` = 0），已实现 + 真实清单推导 |
| 环境覆盖 | 保留：`DSHD_WEB_SEARCH_PROVIDER`/`DSH_WEB_SEARCH_PROVIDER`、`DSHD_WEB_FETCH_PROVIDER`/`DSH_WEB_FETCH_PROVIDER`、`DSHD_WEB_SEARCH_TIMEOUT_MS`、`DSHD_WEB_SEARCH_SUMMARY_MAX_CHARS`，新增 `DSHD_WEB_SEARCH_MAX_QUERIES`/`DSHD_WEB_SEARCH_MAX_RESULTS` |

**两条必须先说清的缺口（不是"边界"带过，见 §8）**

1. 本仓**没有**实现官方 `dsh-web-deepseek-api` 的那两个 provider（`deepseek-search-api` / `deepseek-fetch-api`）——`modules/search` 只注册 `searxng` 与 `deepseek-official`，而 `modules/search` 不在本任务写作用域。因此 rc.2 基线默认 provider **在本宿主未注册**；`web.listProviders` 用 `baseline.searchRegistered=false` **如实报告**，不谎称生效，也不把默认 pin 硬改成 `deepseek-search-api`（那会让搜索变成 `WEB_PROVIDER_CONFIGURED_MISSING`，是功能倒退）。
2. 我们的 web 接缝用 `map` 存注册表，**没有保留注册序**；官方 `listProviders` 是"registration order"。清单按 id 排序（确定性优先），已在代码注释与本节注明。

---

## 1. 官方证据（file:line）

### 1.1 `web.listProviders` 契约

| 证据 | 内容 |
|---|---|
| `dsh-web/lib/typert.host.js:27-41` | 方法描述符：`'web/listProviders'`，`invocation: {kind:'direct'}`，**`parameters: []`**，`result.mode: 'strict'`，`typeSymbol: '@deepseek-ai/dsh-web/types#WebProviderList'`，`sourceLocation {file:"packages/web/web/src/index.ts",line:152,column:3}` |
| `dsh-web/lib/typert.host.js:6-22` | 结果 zod：`{ search: [{id: string, title?: string, description?: string|{en,...}}], fetch: [...] }` |
| `dsh-web/src/index.ts:151-157` | `@Remote listProviders(): WebProviderList` → `{search: [...values].map(providerInfo), fetch: ...}` |
| `dsh-web/src/index.ts:146-150` | jsDoc：「List every registered search and fetch provider for provider pickers. **Availability does not filter the lists.**」 |
| `dsh-web/src/index.ts:246-252` | `providerInfo()`：`{id}` + 仅在存在时带上 `title`/`description`（缺席 ≠ 空串） |
| `dsh-web/src/types.ts:196-205` | `WebProviderInfo = { id, title?, description? }`（`id` 是 `searchProvider`/`fetchProvider` 选的值） |
| `dsh-web/src/types.ts:206-217` | `WebProviderList = { search: readonly WebProviderInfo[], fetch: readonly WebProviderInfo[] }`；「Availability does not filter the lists: a registered provider whose `available()` answers false is still listed」 |
| `dsh-web/src/index.ts:73-81` | 选择语义（执行期解析、与注册顺序无关） |
| `dsh-web/src/index.ts:274-297` | `resolveProvider()`：`WEB_PROVIDER_CONFIGURED_MISSING` / `WEB_PROVIDER_CONFIGURED_UNAVAILABLE` / `WEB_PROVIDER_UNAVAILABLE` / `WEB_PROVIDER_AMBIGUOUS` |
| `dsh-web/src/index.ts:100-101, 204, 237` | env 覆盖：`DSH_WEB_SEARCH_PROVIDER` / `DSH_WEB_FETCH_PROVIDER` 喂 `searchProvider`/`fetchProvider` 同一字段 |
| `dsh-web/lib/index.js`（rc.1） | **`listProviders` 出现 0 次** → 该方法是 rc.2 新增；rc.2 才有的 `lib/typert.host.js` / `lib/typert.remote-client.js` / `lib/types/` 也证明它升级为 typert Remote 方法（`TypertRemoteService`，`src/index.ts:82`） |
| `dsh-web/src/index.ts:161-169` | 注册/注销发 `web/providers-changed`（宿主侧以"每次调用重新投影"等价满足，见 §8） |

### 1.2 搜索/抓取 provider 真实集合（rc.2）

| provider id | 包 | 声明 title/description | `available()` |
|---|---|---|---|
| `deepseek-official` | `dsh-web-search-deepseek`（`src/provider.ts:27`） | description 有（`:207-216`，8 语） | `:227-233`：有 apiKey 或可解析凭据/端点 + 端点可解析 + maxTokens/maxUses 正整数 |
| `deepseek-search-api` | `dsh-web-deepseek-api`（`src/search.ts:13`） | description 有（`:75-83`） | `:92-97`：有 DeepSeek web 凭据 + 每个 source baseURL 可解析 + count/requestsPerSecond 正整数 |
| `deepseek-fetch-api` | `dsh-web-deepseek-api`（`src/fetch.ts:13`） | description 有（`:31-40`） | `:45-49`：同上（无 count/速率项） |
| `http` | `dsh-web-fetch-http`（`src/provider.ts:30`） | description 有（`:36-45`） | `:51-53`：`return true`（匿名抓取永远可用） |

rc.2 `dsh-web-deepseek-api/src/index.ts:64-109` 配置面：`count`(1..50，默认 10)、`searchRequestsPerSecond`(默认 5，账户 5 QPS)、`sendOriginalQuestion`(默认 true)、`searchBilling`/`fetchBilling`(`auto|account|api-key`，默认 `auto`)、`searchBaseURL`/`searchApiKeyEnv`/`fetchBaseURL`/`fetchApiKeyEnv`。
rc.1 同包只有 `count` 与 `sendOriginalQuestion`（rc.1 `lib/index.js` grep：`count`×3、`sendOriginalQuestion`×2，无 `*BaseURL`/`*ApiKeyEnv`/`*Billing`/`searchRequestsPerSecond`）——与任务书"新增 4 个配置项"一致。

### 1.3 10/10 上限的代码出处（关键：release note 与代码对齐）

| 证据 | 内容 |
|---|---|
| rc.2 `dsh-tool-web/src/search.ts:20` | `export const WEB_SEARCH_MAX_RESULTS = 10` |
| rc.2 `dsh-tool-web/src/search.ts:23` | `export const WEB_SEARCH_MAX_QUERIES = 10` |
| rc.2 `dsh-tool-web/src/index.ts:61-62` | `searchMaxResults: z.number().default(WEB_SEARCH_MAX_RESULTS)`、`searchMaxQueries: z.number().default(WEB_SEARCH_MAX_QUERIES)` |
| rc.1 `dsh-tool-web/lib/index.js:26` / `:28` | `const WEB_SEARCH_MAX_RESULTS = 8;` / `const WEB_SEARCH_MAX_QUERIES = 4;` |
| rc.1 `dsh-tool-web/lib/index.js:880-881` | `searchMaxResults: z.number().default(8)`、`searchMaxQueries: z.number().default(4)` |
| rc.2 `dsh-tool-web/src/search.ts:57-60` | 硬校验：`queries must contain at most ${maxQueries} ${noun}`（**超限报错，不静默截断**） |
| rc.2 `dsh-tool-web/src/search.ts:370` | 模型可见 schema：`` `1–${maxQueries} search queries; their results are merged.` `` |
| rc.2 `dsh-tool-web/src/search.ts:304-312` | 多查询轮转合并时按 `maxResults` 截断并置 `truncated` |
| rc.2 `dsh-web/src/index.ts:185-188, 300-303` | seam 强制 `maxResults`：`capSources()` 截 `sources[]` 并置 `truncated` |
| rc.2 `dsh-tool-web/src/search.ts:104` | 截断文案 `(Showing the first ${n} sources. Refine the query for more.)` |
| release note | `docs/upgrade-guide/README.md:92`「One web_search call accepts 10 queries and returns 10 sources by default」；`upgrade-guide/v0.2.1-rc.3/web-search-defaults/guide.md:12`「the `searchMaxQueries` default rises from 4 to 10 and the `searchMaxResults` default rises from 8 to 10」 |

> release note 的说法与代码**一致**；但注意它挂在 `v0.2.1-rc.3` 目录下，而 rc.1（0.2.2-rc.1）编译产物仍是 4/8——所以**以代码为准**取"rc.1=4/8、rc.2=10/10"这条基线，本仓旧实现抄的正是 rc.1 的 4/8。

---

## 2. 默认值变化表（rc.1 → rc.2）

`dsh-base/cordis.patch.yml` 的 web 段落逐行对照：

| 项 | rc.1（行号） | rc.2（行号） | 宿主实现 |
|---|---|---|---|
| `web.config.searchProvider` | `deepseek-official`（`:470`） | `deepseek-search-api`（`:487`） | `modules/tool/websearch/websearch.go` 的 `baseBundleSearchProvider` 改为 rc.2 值；**真实 pin 仍只来自 env**（`modules/search` 用 `spi.WebSearchPinFromEnv`，未钉死 = 官方"自动选择唯一可用"） |
| `web.config.fetchProvider` | `http`（`:471`） | `http`（`:488`） | 不变（`webfetch.HTTPProvider` id `http`） |
| `web-search-deepseek.config.apiKeyEnv` | `DEEPSEEK_API_KEY`（`:476`） | `DEEPSEEK_API_KEY`（`:493`） | 不变（宿主 `DEEPSEEK_API_KEY`/`DSHD_SEARCH_API_KEY`） |
| `web-deepseek-api.config` | `apiKeyEnv: DEEPSEEK_API_KEY`（`:481`） | **无 config**（`:495-496`）；改为自身 `searchBaseURL/searchApiKeyEnv/fetchBaseURL/fetchApiKeyEnv`（`src/index.ts:86-96`） | 本仓无该 provider（§8 缺口 1） |
| `tool-web.config` | `fetch: true`、`searchTimeoutMs: 60000`（`:486-490`） | `fetch: true`、`searchTimeoutMs: 60000`（`:501-505`） | 搜索预算 60s（`baseBundleSearchBudget`） |
| 60s 预算归属 | 注释「this shipped DeepSeek route gets 60s while the provider-neutral tool default remains 30s」（`:462-464`）→ **按 provider** | 注释「This composition gives web_search 60s **whichever provider serves it**; the provider-neutral tool default remains 30s」（`:479-480`）→ **按组合** | `searchBudget()` 不再按 provider 判：env/显式 > 组合级 60s |
| 搜索默认语义 | —— | 注释 `:467-476`：search 默认 DeepSeek Search API、fetch 默认匿名 http；`deepseek-official` 作为 legacy 仍可选 | 宿主 provider 集合见 §8 |

rc.2 相对 rc.1 新增的**配置项**（`dsh-web-deepseek-api/src/index.ts:64-109`）：`searchBaseURL`、`searchApiKeyEnv`、`fetchBaseURL`、`fetchApiKeyEnv`（+ `searchRequestsPerSecond`、`searchBilling`、`fetchBilling`）。旧 `baseURL`/`apiKeyEnv`/`apiKey` 不再是 schema 字段（加载不报错但无效果，`src/index.ts:58-62`）。

---

## 3. 上限结论：**有证据**（10 / 10）

- **查询上限 10**：rc.2 `dsh-tool-web/src/search.ts:23`（常量）+ `src/index.ts:62`（Config 默认值）+ `:57-60`（硬校验）+ `:370`（schema 文案）。rc.1 = 4（`lib/index.js:28` / `:881`；rc.2 对应 `lib/index.js:28` / `:881` 均为 10）。
- **来源上限 10**：rc.2 `src/search.ts:20`（常量）+ `src/index.ts:61`（Config 默认值）+ `:304-312`（合并截断）+ `dsh-web/src/index.ts:300-303`（seam 再截一次并置 `truncated`）。rc.1 = 8（`lib/index.js:26` 与 `:880`；rc.2 对应位置均为 10）。
- **不是 provider 侧上限**：`maxResults` 是 `dsh-tool-web` 层的 bound，经 `WebSearchRequest.maxResults` 传给 seam，**由 seam 强制**（`dsh-web/src/types.ts` 的 `WebSearchRequest` jsDoc：「`maxResults` is a `dsh-tool-web`-layer bound passed through unchanged and enforced on the way back by the seam」，`src/index.ts:185-188`）。provider 的 `count`（默认 10，1..50）只是成本/延迟优化，**不能替代**上限。
- 宿主落点：`modules/tool/websearch/websearch.go` 的 `defaultMaxQueries=10` / `defaultMaxResults=10`，经 `queryLimit()` / `sourceLimit()` 生效（`Tool.MaxQueries`/`MaxResults` > `DSHD_WEB_SEARCH_MAX_QUERIES`/`_MAX_RESULTS` > 默认 10）；超限**报错不静默截断**（与官方 `parseSearchArgs` 同语义），来源超限由 `WebProviders.Search` 截断并置 `Truncated`。

---

## 4. 实现清单

### 4.1 改动文件（全部在本任务写作用域内）

| 文件 | 性质 | 内容 |
|---|---|---|
| `modules/tool/websearch/websearch.go` | 改 | 上限 4/8→10/10 + env/字段覆盖 + schema 按生效上限生成；`baseBundleSearchProvider` → `deepseek-search-api`；`searchBudget()` 改组合级 60s |
| `modules/tool/websearch/websearch_test.go` | 改（2 行） | 旧断言 `provider.limits[0] != maxResults` 跟随常量改名；`TestInputLimits` 不再写死"4 条"，改用 `defaultMaxQueries` |
| `modules/tool/websearch/rc2_red_test.go` | 新增 | **先红**差异测试（6 条，只用既有形状） |
| `modules/tool/websearch/providers.go` | 新增 | `Catalog` 推导：真实装配 + 真实环境 → 清单/可用性/选择/基线/上限/凭据状态（**不 import transport**） |
| `modules/tool/websearch/providerapi/providerapi.go` | 新增 | 宿主→官方 wire 映射 + `ProviderAPI(getenv)`（唯一依赖 `internal/transport` 的一层） |
| `modules/tool/websearch/providers_test.go` | 新增 | 清单推导测试（10 条） |
| `modules/tool/websearch/providerapi/providerapi_test.go` | 新增 | wire 形状/序列化/活状态测试（5 条） |
| `internal/transport/handlers_web_providers_rc2.go` | 新增 | `web.listProviders` 方法常量、严格无参、官方结果类型 + 宿主附加字段、Attach/Detach/Serve |
| `internal/transport/handlers_web_providers_rc2_test.go` | 新增 | wire 层测试（6 条：形状/严格无参/未接线四字段/错方法/空数组/错误传播） |
| `internal/spi/search_catalog.go` | **新增（超出写作用域，见 §9）** | 接缝只读投影：`SearchProviderStatuses()`/`FetchProviderStatuses()`/`SearchPin()`/`FetchPin()` |

### 4.2 关键设计决策

1. **清单必须由真实装配推导，不能静态列表**。`websearch.HostProviders(getenv)` 直接调用生产那两个模块的构造函数与注册表达式：
   - 搜索：`search.NewWebProvidersFromEnv(getenv)`（`modules/search/search.go:47`）
   - 抓取：`seam.RegisterFetch(&webfetch.HTTPProvider{})`（与 `modules/tool/webfetch/webfetch.go:204` 同一表达式）
   可用性直接取各 provider 的 `Available()`，不二次推导。
2. **为什么要在 serve 进程里重装一遍接缝**：工具执行在子进程（`dshd run`），`web.listProviders` 在 HTTP 进程（`dshd serve`），两者不共享内存注册表；但 `dsh serve` 用 `os.Environ()` 拉起子进程（`cmd/dshd/serve_loop.go:147-168`，只替换 `DSHD_LLM_*`/`DSHD_LLM_RETRY_POLICY`），搜索侧的 `DSHD_SEARCH_URL`/`DEEPSEEK_API_KEY`/`DSHD_SEARCH_API_KEY`/pin 变量原样继承 → **同一份装配代码 + 同一份环境 = 同一份真值**。
3. **`baseline` 与 `resolution` 分开**：`baseline` 报 rc.2 要求什么（`deepseek-search-api`/`http`）以及本宿主是否注册；`resolution` 报当下真实选择（含 `WEB_PROVIDER_*` 失败码）。两者混一个字段就只能撒谎。
4. **不把默认 pin 硬改成 `deepseek-search-api`**：该 provider 本仓未实现，硬改会让 `SelectSearch()` 返回 `WEB_PROVIDER_CONFIGURED_MISSING`（搜索直接不可用）。合规做法是把 rc.2 的默认值作为"基线"暴露 + 等 provider 落地后由 `modules/search` 用它做默认 pin（见 §8 接线）。
5. **`providerapi` 单独一层**：领域投影（`websearch`）不依赖 HTTP 协议层；官方 wire 形状只有一处定义，改协议不动领域逻辑，且领域测试不会被协议层并行改动牵连。
6. **顺序差异如实写出**：接缝 `map` 无注册序 → 按 id 排序；官方是注册序。
7. **宿主附加字段**（官方没有，按"能力下限：只能更多不能更少"）：每条 `available`/`default`/`selected`；顶层 `resolution`/`baseline`/`limits`/`credentials`。这些是附加键，官方 zod `z.object` 会 strip 未知键，不会破坏官方客户端读取 `search`/`fetch`。

---

## 5. 先红 → 绿记录

### 5.1 先红（实现前，`go test ./modules/tool/websearch/ -run TestRC2`）

```
--- FAIL: TestRC2AcceptsTenQueries (0.00s)
    rc2_red_test.go:57: rc.2 允许 10 条查询；旧实现（上限 4）会拒掉: 查询过多: 10
--- FAIL: TestRC2RejectsElevenQueriesLoudly (0.00s)
    rc2_red_test.go:77: Hint 要点名 rc.2 的真实上限 10: "一次最多 4 条查询：把次要的合并进第一条"
--- FAIL: TestRC2SchemaAdvertisesTenQueries (0.00s)
    rc2_red_test.go:85: schema 的查询说明必须写 1–10（旧实现写 1–4）: { … "description": "1–4 条查询；结果会合并去重" … }
--- FAIL: TestRC2ReturnsTenSources (0.00s)
    rc2_red_test.go:102: rc.2 单次最多返回 10 个来源（旧实现 8）；实际 8
--- FAIL: TestRC2BaseBundleSearchProvider (0.00s)
    rc2_red_test.go:112: rc.2 基础组合的 searchProvider 是 deepseek-search-api（cordis.patch.yml:487）；当前 "deepseek-official"
--- FAIL: TestRC2BaseBudgetIsCompositionLevel (0.00s)
    rc2_red_test.go:124: provider "deepseek-search-api" 的预算该是组合级 60s（rc.2）；实际 30s
FAIL	dshgo/modules/tool/websearch	0.056s
```

6/6 红，且都是**行为**红（能编过），不是编译错误——符合本仓"先红"纪律。

### 5.2 绿（实现后）

```
ok  	dshgo/modules/tool/websearch	             1.094s   （27 个测试函数，含 6 条先红转绿）
ok  	dshgo/modules/tool/websearch/providerapi     0.191s   （5 条 wire 映射）
ok  	dshgo/internal/transport	                 0.108s   （TestOfficialWebListProviders* 6/6 PASS）
ok  	dshgo/internal/spi	                     0.877s   （新增只读投影未破坏既有用例）
ok  	dshgo/modules/search	                     2.345s   （回归）
ok  	dshgo/modules/tool/webfetch	             1.227s   （回归）
```

---

## 6. 测试与门禁

| 门禁 | 命令 | 结果 |
|---|---|---|
| 目标包 build | `go build ./internal/spi/ ./modules/search/ ./modules/tool/webfetch/ ./modules/tool/websearch/ ./modules/tool/websearch/providerapi/ ./internal/transport/` | **EXIT=0** |
| 目标包 vet | `go vet`（同一组包） | **EXIT=0** |
| websearch 全包 | `go test ./modules/tool/websearch/ -count=1` | **ok 1.094s**（27 个测试函数：既有 14 + 本任务新增 13，含 6 条先红） |
| providerapi | `go test ./modules/tool/websearch/providerapi/ -count=1` | **ok 0.191s**（5 条） |
| transport wire | `go test ./internal/transport/ -run 'TestOfficialWebListProviders' -count=1` | **ok 0.108s**（6/6 PASS） |
| spi 回归 | `go test ./internal/spi/ -count=1` | **ok 0.877s** |
| search / webfetch 回归 | `go test ./modules/search/ ./modules/tool/webfetch/ -count=1` | **ok 2.345s / 1.227s** |
| gofmt | `gofmt -l <本任务全部文件>` | 空 |

**并行环境说明（重要，供 Lead 判读）**：本会话所有子代理共用**同一棵 worktree**（`git worktree list` 只有 `dsh-bdb66fbf-95ba`），其它任务（forge/terminal/account/session/plugin 等）在同期改同一棵树。期间 `go build ./...` 全树多次因**别人的半成品**报错（例：`internal/forge/rc2.go:504 p.checkRuns undefined`、`internal/transport/handlers_job_terminal_official.go` `containsString redeclared`、`internal/session/subagent_catalog.go:112`、`internal/worktree/worktree.go:319 pathKey undefined`、`handlers_account_official_test.go` 未定义 `MethodAccountGetNotifications`、`handlers_session_official_test.go:1034 undefined: errorEnvelope`、`handlers_account_official.go:971 newline in rune literal`）。。**这些与 A3 无关**；A3 的门禁只在目标包上跑（上面表格全部为 0 退出并在最后一次复跑通过）。全树门禁需等各任务收口后由 Lead 统一跑。

---

## 7. 接线行（我未改 `cmd/dshd/serve.go` / `internal/transport/server.go`，由 Lead 集成）

### 7.1 `internal/transport/server.go`（分派；放在 LLM provider 分支 `:491-494` 之后）

```go
	// GO.8-RC2：官方 web.listProviders（web 能力接缝的 provider 清单）。
	if IsOfficialWebProviderMethod(name) {
		s.ServeOfficialWebProviderAPI(w, r)
		return
	}
```

> 位置要求：必须早于下方 `switch Method(name)` 的"未接线 501"分支。已核对：`internal/transport/*.go`（非测试）里**没有任何** `web.` 前缀的分派或占用（`grep` 为空），因此不会与既有方法族抢路径；不接线时它会落到 `switch` 的未实现分支（501），不会静默成功。

### 7.2 `cmd/dshd/serve.go`（真实现注入；紧随 `:830` 的 `srv.AttachOfficialLLMProviderAPI(newLLMProviderAPI(modelCatalog))`）

```go
	srv.AttachOfficialWebProviderAPI(providerapi.ProviderAPI(os.Getenv))
```

### 7.3 `cmd/dshd/serve.go` import

```go
	"dshgo/modules/tool/websearch/providerapi"
```

### 7.4 未接线时是什么表现

`POST /api/web.listProviders` → **501 + 四字段错误**，`hint` 明确写「接线：`srv.AttachOfficialWebProviderAPI(providerapi.ProviderAPI(os.Getenv))`」。**不会**返回空清单假装成功（`TestOfficialWebListProvidersNotWired` 钉住）。

---

## 8. 未覆盖项 / 与官方差异（逐条，不含"边界"敷衍）

1. **`deepseek-search-api` / `deepseek-fetch-api` provider 未实现**（官方 `dsh-web-deepseek-api` 重写后的两个 provider）。
   - 影响：rc.2 基线默认搜索 provider 在本宿主**未注册**，因此 rc.2 的默认值只能作为"基线"报告，不能真生效。
   - 需要：在 `modules/search`（本任务写作用域外）新增该 provider（Search API：`count` 1..50 默认 10、`searchRequestsPerSecond` 默认 5、`searchBilling`/`fetchBilling` `auto|account|api-key`、`searchBaseURL`/`searchApiKeyEnv`/`fetchBaseURL`/`fetchApiKeyEnv`），并把默认 pin 改成 `deepseek-search-api`。
   - 在此之前**不改**默认 pin（改了就是搜索不可用）。
2. **注册序**：官方清单按注册序；本接缝（`internal/spi` 的 map）无注册序，按 id 排序返回。若要完全对齐，需要给 `spi.WebProviders` 加顺序切片（改动 `internal/spi/search.go` 结构体，超出写作用域）。
3. **`web/providers-changed` 事件未实现**：官方注册/注销时发事件让客户端重读；本仓没有跨进程事件面，`web.listProviders` 是拉取式（每次调用重新投影，状态永远是当下真值）。客户端若依赖事件刷新，需要自行轮询或等事件面落地。
4. **`title`/`description` 缺席**：官方 4 个 provider 都声明了多语 description（§1.2），我们的 2 个搜索 provider 与 `http` 不声明（`spi.SearchProvider`/`FetchProvider` 接口没有元数据字段）→ 清单里字段缺席（符合官方"没声明就缺席"语义），界面会显示 id。要补齐需要给 SPI 接口加可选 `Title()`/`Description()` 并让 provider 实现（`internal/spi` + `modules/search`，均超出写作用域）。
5. **`ready()`（按模型路由的凭据可用性）未投影**：官方 `searchReady(llmProvider)` / `fetchReady(llmProvider)` 会影响工具在某一步是否可见；我们的 `SearchReady()` 是全局的。清单里的 `available` 是本地 `Available()`，不含"某条 chat route 上能否认证"。
6. **`web_fetch` 的 provider 面**：本次只报告，不改 `modules/tool/webfetch` 的工具行为（其 `FetchProviderID` 与 `http` 一致）。
7. **`dsh-web` 的 `TypertRemoteService`/typert 描述符**：本仓 `/api/<method>` 的 L1 面已覆盖；L2（remote.mux/typert Connection wire）未在本任务范围（AGENTS 已注明 L2 覆盖面 PARTIAL）。
8. **`searchBaseURL`/`searchApiKeyEnv` 等 4 个新配置项**在宿主无对应物（依赖缺口 1 的 provider）。

---

## 9. 超出写作用域的最小改动（请 Lead 复核）

| 文件 | 为什么必须改 | 改动量 | 冲突风险 |
|---|---|---|---|
| `internal/spi/search_catalog.go`（**新增文件**） | 官方 `listProviders` 要报"每条 provider 真实的 `available()`"。注册表 `WebProviders.search/.fetch` 是该包的私有 map，**包外枚举不到**，拿不到 provider 实例就拿不到它的 `Available()`；在调用方按环境变量重新推导等于把三份判定逻辑复制出去，第一次改动就会漂。 | +118 行，**纯新增、只读**：`SearchProviderStatuses()`、`FetchProviderStatuses()`、`SearchPin()`、`FetchPin()`。**未修改** `internal/spi/search.go` 一个字符。 | 无：该路径不在其它任务的写作用域内（见 `team_task_list` 的 8 条 write_scopes），且是新增文件，不会与任何人的编辑冲突。 |

若 Lead 不接受该新增文件，替代方案（按代价排序）：
1. 把 `web.listProviders` 的 `available` 字段降级为"仅报选中 provider 的可用性 + 能力级 `resolution.errorCode`"（信息量下降，但仍在写作用域内）；
2. 把 `Catalog` 推导搬进 `internal/transport`（会让 transport 依赖 `internal/spi`，破坏现有"transport 只做协议翻译、真值由宿主注入"的分层）。

---

## 10. 复现命令

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
cd "D:/DeepSeek Harness/default-workspace/DSH-GO/.agents/worktrees/dsh-bdb66fbf-95ba"

go build ./internal/spi/ ./modules/search/ ./modules/tool/webfetch/ \
         ./modules/tool/websearch/ ./modules/tool/websearch/providerapi/ ./internal/transport/
go vet   ./internal/spi/ ./modules/tool/websearch/ ./modules/tool/websearch/providerapi/ ./internal/transport/

go test ./modules/tool/websearch/ -count=1 -v
go test ./modules/tool/websearch/providerapi/ -count=1 -v
go test ./internal/transport/ -run 'TestOfficialWebListProviders' -count=1 -v
```
