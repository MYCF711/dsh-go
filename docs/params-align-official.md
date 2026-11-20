# 四类参数对齐官方（0.2.2-rc.2）：上下文窗口 / 最大输出 / 思考值 / 压缩阈值

> 触发：用户质疑「GO 宿主是不是把模型上下文、最大输出 token、思考值、压缩阈值这些参数都忘
> 了对齐官方」。本文是**取证答复**：每个数字都有官方 `file:line`，逐项给「官方值｜本仓值｜
> 是否一致」，不一致的已当场修（见 §6），查不到的明说「查不到」。
>
> 官方包解包在 `tmp/GO3-PARAMS/_official/`（gitignore，不入库）。官方**部署**配置取
> `D:/DeepSeek Harness/home/profiles/desktop/cordis.patch.yml.bak-1794253367009`（官方
> 0.2.2 桌面 profile 的完整补丁层；现行 `cordis.patch.yml` 已被本机续写，故用该备份读官方原件）。
> 本仓行号以本分支 `feat/params-align-official`（origin/main `572bc78`）为准。

## 1. 压缩阈值（`context.compaction.thresholdRatio`）

**「0.7 与 0.8 两个值」的真相：0.8 是默认值 + 部署值；0.7 只出现在 README 的按模型调优示例里，
代码与部署配置里都没有。**

| 项 | 官方值（来源） | 本仓值（来源） | 一致？ |
|---|---|---|---|
| `thresholdRatio` 默认 | **0.8** — `dsh-compaction-basic/lib/index.js:15` `const DEFAULT_THRESHOLD_RATIO = .8;`；README 表 `README.md:68` | **0.8** — `internal/compact/budget.go:25` `DefaultThresholdRatio = 0.8` | ✅ |
| 官方部署实际取值 | **0.8（未配置）** — 官方 profile `:228-229` 只挂 `dsh-compaction-basic`、**无 config**，故走库默认 0.8 | — | ✅ |
| 0.7 出处 | **仅 README 示例** — `dsh-compaction-basic/README.md:56` 的 `modelPolicies[].thresholdRatio: 0.7`（"小窗口按模型调优"示例）；全树 `.js` 与官方 profile 里 0 命中 | — | 不涉及 |
| 触发公式 | `floor(min(W × thresholdRatio, W − O − B))` — `lib/index.js:132`；`O`/`B` 语义 `:128-131` | 同公式 — `internal/compact/budget.go:85-104` | ✅ |
| `headroomTokens` 默认 | **65536** — `lib/index.js:65` | **65536** — `internal/compact/policy.go:36` | ✅ |
| `retainRatio` 默认 | **0.16** — `lib/index.js:17`；`retainTokens = floor((W−O) × 0.16)`（`:137`） | 宿主用**绝对** `KeepTokens=2000`（`internal/compact/compact.go:169` 一带），无比例字段 | ❌（见 §5 缺口1） |
| `compactionRetries` / `maxOverflowRetries` / `auto` | 1 / 1 / true — `lib/index.js:83-85` | 1 / 1 / 默认开（`internal/compact/retry.go`、`--auto-compact`） | ✅ |
| `maxTokens`（摘要调用输出上限） | `config.maxTokens ?? headroomTokens` = **65536** — `lib/index.js:66-67`，进 `spec.maxTokens`（`:961-965`） | 原写死 **2000**（旧 `internal/compact/compact.go`），**已修**为 `Policy.SummarizeMaxTokens()`，默认 65536（`internal/compact/compact.go:138`、`internal/compact/policy.go:255-262`） | 已修 ✅ |
| **实际生效阈值** | `min(800000, W−O−B)`：W=1e6、O=128000、B=65536 → **800000** | 默认即官方口径 **800000** — `compact.HostThreshold`（`internal/compact/budget.go`）只有官方公式一个来源：不写 `--auto-compact` 就是 `floor(min(W×ratio, W−O−B))`，写 `N` 才覆盖成 N；**窗口未知/预算不成立照官方抛错**（`TargetPressureConfigError` 等价物），不再有宿主自造的 40000 兜底 | **已修** ✅（2026-11-20，见 §6 第 6 行） |

**policy 分支覆盖（`lib/index.js:19-42` 的 9 个可配字段）**

| 官方字段 | 本仓接线 | 覆盖 |
|---|---|---|
| `thresholdRatio` | `Policy.ThresholdRatio` / `--compact-model-policies` | ✅ |
| `headroomTokens` | `Policy.HeadroomTokens` / `--compact-headroom-tokens` | ✅ |
| `maxTokens` | `Policy.MaxTokens` → `SummarizeMaxTokens()`（本次修好；此前被误用成预留额度） | ✅（本次修） |
| `retainRatio` / `retainTokens` | 只有 `Options.KeepTokens`（绝对，默认 2000），无按窗口比例解析 | ❌ 缺口1 |
| `compactionRetries` / `maxOverflowRetries` | `RetryPolicy` + env | ✅ |
| `modelPolicies` | `ParseModelPolicies` + `Resolve`（精确 > p/* > */m > */*） | ✅ |
| `summarizationProvider` / `summarizationModel` | 摘要固定用当前模型，无独立摘要目标 | ❌ 缺口3 |
| `auto` | `--auto-compact`（0/false=关） | ✅（语义等价） |

## 2. 上下文窗口（`contextWindow`）

**官方没有「200000 / 262144 / 524288 多档内置表」**：这三个数全是 README 的**用户配置示例**
（`dsh-llm-pi-ai/README.md:60`、`:71`、`dsh-llm-deepseek-account/README.md:49`），不是出厂默认。
官方真正的出厂值只有两处：

| 官方层 | 官方值（来源） | 本仓值（来源） | 一致？ |
|---|---|---|---|
| 官方**桌面 profile（同一条 magpie 网关）** | `group/deepseek-v4.1-flash-1017` = **1000000**（profile `:440`）；`group/kimi-k3.1-pro` = **1000000**（profile `:448`） | flash = **1000000**（`internal/llm/capacity.go:86`）；kimi **原先缺失 → 已补**（`capacity.go:93`） | 已补齐 ✅ |
| pi-ai 适配器默认窗口 | **1e6** — `dsh-llm-pi-ai/lib/index.js:960`（`DEFAULT_CONTEXT_WINDOW`），`:1058` schema 默认，`:1158` 物化 | **1_000_000** — `internal/llm/capacity.go:24` `DefaultCustomContextWindow` | ✅ |
| llm-deepseek 适配器默认窗口 | **1e6** — `dsh-llm-deepseek/lib/index.js:38`、`:330`、`:452` | 同上（宿主不区分适配器，未声明模型统一 1e6） | ✅ |
| llm-deepseek **内置模型目录** | `deepseek-flash` / `deepseek-v4-pro` = **1048576** — `dsh-llm-deepseek/lib/index.js:17`（`OFFICIAL_CONTEXT_WINDOW = 1048576`）、`:19-32`（`DEFAULT_MODELS`） | 未加入内置表：本机不路由官方 API 的这两个 id（网关路由是 `group/*`），加进去反而会给 magpie 路由一个偏大的窗口（1.048M > 网关声明 1M） | **查不到本机路由依据，未加**（见 §5 缺口4） |
| 解析优先级 | `entry.contextWindow ?? base.contextWindow ?? request.defaultContextWindow` — `dsh-llm-pi-ai/lib/index.js:695-696`；目录声明必须为正整数（核心校验 `dsh-llm/lib/index.js:2124-2130`，`INVALID_MODEL_CONTEXT` 在 `:2128`） | `catalog 声明 > 内置表（base）> DSHD_CONTEXT_WINDOW > 未声明默认 1e6 > 0` — `cmd/dshd/transport_source.go:366-400`（**本次修**：env 原先压过内置表，与官方把 `defaultContextWindow` 排在 `base.contextWindow` 之后相反） | 已修 ✅ |
| 目录声明非法时的行为 | `contextWindow` 非正整数 → `INVALID_MODEL_CONTEXT`（`dsh-llm/lib/index.js:2128`） | `ValidateDeclaredContextWindow` + `InvalidModelContextError`（`internal/llm/capacity.go` 尾部），0=未声明是既有契约 | ✅ 等价 |

## 3. 最大输出 token（`maxTokens` / 请求里的 `max_tokens`）

**结论：官方**永远**发 `max_tokens`。本仓原「内置模型 `MaxOutput: 0` = 不发」正好是错的，已修。**

| 项 | 官方值（来源） | 本仓值（来源） | 一致？ |
|---|---|---|---|
| 适配器声明默认输出上限 | pi-ai **128000**（`dsh-llm-pi-ai/lib/index.js:962`、`:1059`、`:1158`）；llm-deepseek **256000**（`dsh-llm-deepseek/lib/index.js:40`、`:329`、`:450`、`:536` `defaultMaxTokens: configured?.maxTokens ?? connection.maxTokens`） | 未声明自定义模型 = **128000**（`capacity.go:25`）；语义按 pi-ai（宿主走 OpenAI 兼容面） | ✅ |
| 核心是否物化进请求 | **是** — `dsh-llm/lib/index.js:2188-2193`：`config.maxTokens === undefined && info.defaultMaxTokens !== undefined ? { ...config, maxTokens: info.defaultMaxTokens } : config` | 是 — `internal/agent/loop.go:373` `MaxTokens: capacity.MaxOutput` | ✅ |
| 适配器是否发出 | **是** — `dsh-llm-deepseek/lib/index.js:1732` `max_tokens: options.maxTokens ?? model?.maxTokens ?? connection.maxTokens`；pi-ai 把 `options.maxTokens` 交给 `streamSimple`（`dsh-llm-pi-ai/lib/index.js:2028`） | 是（`internal/llm/compat.go` 的 `max_tokens` 字段；拼写协商保留） | ✅ |
| 官方部署值（本机路由） | **128000** — profile `:441`（flash）、`:449`（kimi） | 内置模型原 **0** → **已修 128000**（`capacity.go:86-98`，值取 `DefaultCustomMaxOutput`） | 已修 ✅ |
| `reservedCompletionTokens(header, defaultMaxTokens)` | `header.config.maxTokens ?? defaultMaxTokens ?? 0` — `dsh-compaction-basic/lib/index.js:757-759`；只认这两层 | `ReservedCompletionTokens` 同语义（`internal/compact/budget.go:27-40`）；**但 `Policy.Budget` 原让 `modelPolicies[].maxTokens` 顶掉预留**，与官方 `resolveCompactSpec(policy, W, reserved)` 的第三参数只来自请求面相反（`:124-128`、`:959-961`） → **已修**（`internal/compact/policy.go:237-247`） | 已修 ✅ |
| 摘要调用输出上限 | `config.maxTokens ?? headroomTokens` = 65536（`lib/index.js:66-67`、`:961-965`） | 原写死 2000 → **已修** 65536 | 已修 ✅ |

## 4. 思考值（`reasoningEffort` / `reasoningEfforts`）

| 项 | 官方值（来源） | 本仓值（来源） | 一致？ |
|---|---|---|---|
| 可选档位集合（pi-ai / OpenAI 兼容面） | **7 档**：`off, minimal, low, medium, high, xhigh, max` — `dsh-llm-pi-ai/lib/index.js:299-308`（`THINKING_LEVELS`，按升级顺序）；`reasoningEfforts` 的 key 只能取这 7 个（`:1034` `z.dict(..., z.union(THINKING_LEVELS))`） | 校验只要求"非空 string、至少一个非 off 档"，**不限制档名**（`internal/llm/model_catalog.go:1374-1401`）→ 是官方的**超集**（可写 `deep`/`custom` 等自定义档，测试 `internal/llm/model_catalog_test.go:53`、`cmd/dshd/effort_goal_test.go:40` 依赖） | ⚠️ 超集（见 §5 缺口5） |
| 可选档位（llm-deepseek 适配器） | **4 档**：`off, low, high, max` — `dsh-llm-deepseek/lib/index.js:472-513`、校验 `:1720-1726` | 同超集校验 | ⚠️ 同上 |
| 「no level beyond off」规则 | 有 — `dsh-llm-pi-ai/lib/index.js:598`：只有 `off` 一个档 → 拒绝（`reasoningEfforts offers no level beyond "off"`）；空字典/空串/非 off 写 null 也拒（`:590`、`:596-597`） | **有** — `internal/llm/model_catalog.go:1399`「至少要有 off 以外的一个 level」；off:null 允许、非 off null/空串拒绝（`:1388-1397`） | ✅ |
| 默认档（官方部署） | **max** — 官方 profile `:8` `agent-default-model.config.reasoningEffort: max`（provider magpie / model `group/deepseek-v4.1-flash-1017`，`:6-8`）；该模型声明 `xhigh, max`（profile `:443-445`），max 是合法档 | 原默认 **空 = 不发** — `cmd/dshd/{main,acp,serve}.go` 的 `--reasoning` 默认 `os.Getenv("DSHD_REASONING_EFFORT")` → 已修为 `defaultReasoningEffortFromEnv()`，默认 **max**（`cmd/dshd/host_run_options.go:56-76`） | 已修 ✅ |
| 默认档（适配器层） | llm-deepseek：thinking 未关时默认 **high**（`dsh-llm-deepseek/lib/index.js:1720`、`:540-544` 的 `defaultEffort`）；pi-ai：路由 `reasoning` 未配置时**无默认**，显式档原样发（`:1993`） | 宿主走 OpenAI 兼容面 → pi-ai 语义（不配就不发）但把官方 profile 的出厂档 max 作为进程默认（见上行） | 与部署一致 ✅ |
| 模型没声明时的行为 | 显式档必须命中 `efforts`，否则 `UNSUPPORTED_REASONING_EFFORT`（`dsh-llm/lib/index.js:2194-2206`；pi-ai 侧 `dsh-llm-pi-ai/lib/index.js:1810-1814`） | 会话档（`session.setEffort`）必须命中 catalog 的 `reasoningEfforts` key，否则 `ErrEffortInvalid`（`cmd/dshd/serve_effort_override.go:113-218`）；进程默认 `--reasoning` 是 wire 值，不校验 catalog（官方默认选择也不在每次请求时重新校验，但档位来自声明过的 selection） | 会话面等价 ✅ / 进程默认面更宽松（超集） |
| 模型档位声明从哪来 | 官方 profile 给本机两个 magpie 模型显式声明：flash `{xhigh, max}`（`:443-445`）、kimi `{medium, high, xhigh, max}`（`:451-455`） | 宿主没有"内置档位表"：档位来自 `<home>/state/model-catalog.json`（用户/界面写）或 env 模型发现（`internal/llm/model_env_catalog.go`） | ❌ 缺口6（无内置声明） |

## 5. 未覆盖 / 查不到（不编）

1. **缺口1 `retainRatio`/`retainTokens`（仍未改）**：官方默认 0.16（`dsh-compaction-basic/lib/index.js:17`、`:137`），本仓只有绝对 `KeepTokens=2000`。官方在 `resolveCompactSpec` 里强制 `retainTokens < thresholdTokens`（`lib/index.js:136`，违反时抛 `TargetPressureConfigError`）。缺口2 修完后出厂路径已满足该不变式（1M 窗口：retain=`floor((1e6−128000)×0.16)`=139520 < 阈值 800000），但 `--auto-compact=N` 这个显式覆盖仍能把它推翻（N 小于消息预算的 16% 时）。要接就得先决定「留存按**生效**阈值钳制，还是直接 fail-loud」，那是取舍不是翻译，**不在本轮**——保持现状，等这条取舍被裁定后再一起动。
2. **缺口2 生效阈值 40000 vs 官方 800000 — 已修（2026-11-20）**：根因不是「默认值偏小」，而是**宿主自己编了一个出厂绝对阈值**：`defaultAutoCompact = 40000`（官方既无此字段也无此默认）被拿去和官方口径取小，1M 窗口因此被截到 4 万（官方 800000，**早 20 倍**），表现为「聊不了几句上下文就被压走」。修法是把假默认值**删掉**，不是改大它：`compact.HostThreshold(override, budget)` 现在只有一个真值来源——官方公式 `floor(min(W × ratio, W − O − B))`；`--auto-compact=N` 只作**显式覆盖**（`compact.AutoCompactUnset` = 不覆盖 = 官方口径，`AutoCompactOff` = 关闭）；**窗口未知/非法、预算不成立一律返回错误**（官方 `TargetPressureConfigError`，`lib/index.js:126-131`，窗口未知那条在 `:958`「no context capacity ... configure contextWindow」），由两个入口各自**报一次配置错误、跳过压缩、继续这一轮**（官方 pre-step 的 catch 分支，`:844-851`）——既不退回编造的绝对值，也不静默不压缩。`dshd run` 另外补了「用与请求同一个有效窗口」（`llm.ResolveModelCapacity`），否则命令行/界面起的会话（`--context-window 0`）永远算不出阈值。判据见 `cmd/dshd/compact_threshold_official_test.go`（四格表）与 `internal/compact/budget_test.go`；把 40000 兜底或假默认值加回去的两种变异各自变红。
3. **缺口3 `summarizationProvider`/`summarizationModel`（未改）**：官方允许摘要走独立目标（`lib/index.js:78-79`、`:141-142`）；本仓 `compact.Summarize` 固定用当前模型（`internal/compact/compact.go`），无独立目标配置。**不改成"只加一个摘要模型名"**：宿主是单通道（`llm.New(base, model)` 一个 base + 一份凭据），没有官方的多 provider 路由表；只翻译 model 那一半就是半实现——网关不路由该 id 时压缩会静默失败（`autoCompactNow` 失败只打 stderr，上下文继续涨），而官方还要求摘要目标按**自己的**容量算 `maxTokens`/窗口。要接就得先有"多个可路由目标 + 各自容量"这一层，属设计工作，不在本轮。
4. **缺口4 官方 deepseek 目录 1048576**：`deepseek-flash`/`deepseek-v4-pro` 的 1048576（`dsh-llm-deepseek/lib/index.js:17-32`）**没有**加进 `builtInModels`——本机没有路由这两个 id 的证据，加进去会给 magpie 上的同名模型一个偏大窗口。若将来直连 `api.deepseek.com/anthropic`，应按该文件补 1048576/256000。
5. **缺口5 档位名白名单**：官方限制 7 档（pi-ai）或 4 档（deepseek），本仓校验不限制档名（超集）。收紧会删掉现有测试里 `deep`/`custom` 档的覆盖（`internal/llm/model_catalog_test.go:53`、`cmd/dshd/effort_goal_test.go:40`），属"删能力"，按能力下限规则**不收紧**，登记为已知超集。
6. **缺口6 内置档位声明（未改）**：官方 profile 给两个 magpie 模型声明了档位（`:443-445`、`:451-455`），本仓没有编译期档位表，只能靠用户目录/env 发现；默认模型在只有 `models.list` 的 home 上不会显示可选档位。**不补编译期表**：一旦补上，`session.setEffort` 就会按这张表**收紧**校验（当前是官方超集，`--reasoning=""`/`off` 与 `deep`/`custom` 等自定义档都还能用，`internal/llm/model_catalog_test.go:53`、`cmd/dshd/effort_goal_test.go:40` 依赖），按能力下限规则"只能更多更强"，收紧校验属于删能力；只做展示不参与校验又是半实现。等"档位声明参与校验"这件事被单独裁定后再动。
7. **查不到**：pi-ai 的**已安装目录**（peer dep `@mariozechner/pi-ai` 的模型表）不在解包产物里——`tmp/GO3-PARAMS/_official/dsh-llm-pi-ai/` 只有适配器本体，`app.asar.unpacked/dsh/node_modules/@deepseek-ai/` 也只有原生包。因此"pi-ai 安装目录里每个第三方模型的 contextWindow"**查不到**，本文只列官方代码/官方 profile 里可核的值。搜过：`_official/**/*.js|json|md`、`app.asar.unpacked/dsh/node_modules/**`、官方 profile 与其备份。

## 6. 本次改动（每处附官方依据 + 能抓到的测试）

| # | 改动 | 官方依据 | 抓它的测试 |
|---|---|---|---|
| 1 | 内置容量表：flash `MaxOutput 0 → 128000`；新增 `group/kimi-k3.1-pro`（1000000/128000）；新增 `KnownModelCapacity` | 官方 profile `:438-453`；pi-ai `lib/index.js:962/:1158`；核心物化 `dsh-llm/lib/index.js:2188-2193`；适配器发出 `dsh-llm-deepseek/lib/index.js:1732` | `internal/llm.TestBuiltInCapacityMatchesOfficialProfile`、`TestResolveModelCapacity`（3 例改期望）、`internal/agent.TestRegularRequestUsesModelCapacity`（2 例改期望）、`cmd/dshd.TestACPKnownModelUsesOfficialProfileCapacity`、`TestRunRequestContextOmitsUndeclaredWindow` |
| 2 | `sessionSource.ContextWindow` 优先级：`catalog > 内置表(base) > DSHD_CONTEXT_WINDOW > 1e6 默认 > 0` | pi-ai 解析链 `lib/index.js:695-696`；官方 profile `:440/:448` | `cmd/dshd.TestLoadContextWindowBuiltInDeclarationBeatsEnv`（新）、`TestLoadContextWindowEnvFallbackWhenCatalogMisses`（改用未声明模型，仍抓 env 兜底） |
| 3 | `--reasoning` 默认 `""` → **max**，三入口共用 `defaultReasoningEffortFromEnv()`；总账守卫加 `reasoning` 单源项 | 官方 profile `:8`；核心物化 `dsh-llm/lib/index.js:2188-2210`；适配器落 wire `dsh-llm-pi-ai/lib/index.js:1993` | `cmd/dshd.TestDefaultReasoningEffortMatchesOfficialProfile`、`TestDefaultReasoningEffortFromEnv`（新）、`TestGuardFlagDefaultsAreSingleSourced`（新增 reasoning 项）、`TestEffortGoalRealDSHD...`（清档后回落到 max） |
| 4 | 压缩：`Policy.Budget` 的预留额度只认请求面（不再被 `modelPolicies[].maxTokens` 顶掉） | `dsh-compaction-basic/lib/index.js:124-128`、`:757-759`、`:959-961` | `internal/compact.TestPolicyBudgetReservationIgnoresPolicyMaxTokens`（新） |
| 5 | 压缩：摘要调用输出上限 `2000 → Policy.SummarizeMaxTokens()`（默认 65536），三个入口（run/acp/`/compact`）都接线 | `lib/index.js:66-67`、`:961-965` | `internal/compact.TestPolicySummarizeMaxTokens`、`TestSummarizeOutputCap`（新） |
| 6 | **压缩：删除宿主自造的出厂阈值，生效阈值只来自官方公式**。`defaultAutoCompact = 40000` 已删；`compact.AutoCompactUnset`（不写 `--auto-compact`）= 官方 `floor(min(W×ratio, W−O−B))`（1M 窗口 = **800000**）；`--auto-compact=N` = 显式覆盖；`0/false` = 关闭；**窗口未知/预算不成立 = 返回错误**（官方 `TargetPressureConfigError`），两个入口各报一次后跳过压缩、继续本轮。`dshd run` 侧补 `llm.ResolveModelCapacity` 解析有效窗口；serve 透传用 `auto` 拼写保住「不覆盖」这一态 | `dsh-compaction-basic/lib/index.js:132`（公式）、`:126-131` 与 `:958`（fail-loud）、`:844-851`（告警一次→跳过） | `cmd/dshd.TestCompactThresholdOfficialFormula`（四格表，新）、`TestAutoCompactOffKeepsRunCompactorDisabled`（新）、`TestServeAutoCompactPassthroughKeepsTriState`（新）、`TestRunCompactorUsesResolvedWindow`（新）、`TestCompactThresholdLargeWindowFollowsOfficial`、`TestCompactThresholdUnknownWindowFailsLoud`（改用空模型 id：原夹具其实没走到未知窗口分支）、`internal/compact.TestHostThresholdFollowsOfficialFormula`、`TestHostThresholdFailsLoudWithoutWindow`、`TestHostThresholdRefusesWhenDisabled`、`TestHeadroomShrinksThresholdForSmallWindow`、`TestHeadroomZeroDisablesReserve` |

## 7. 门禁

见本次交付说明：完整 `bash scripts/check.sh`（含 race-guard）+ `python scripts/gap-register-counts.py --check`，
以及 `git status --short` / `git diff --stat`。门禁数字记在 `docs/evidence/`（若需要）。