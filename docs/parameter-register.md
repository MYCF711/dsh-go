# 参数总账（parameter register）

> 由 `tmp/GO3-PARAMS/register/gen_register.py` 从三份已完成的清单生成，不手抄；
> 机器可读版本：[`parameter-register.json`](parameter-register.json)（同目录，随仓提交；`tmp/` 是 gitignore 的，不指过去）。
> 官方基线 342 条见 `tmp/GO3-PARAMS/official.md`；宿主 186 条见 `tmp/GO3-PARAMS/host.md`；
> TUI2GO 49 条见 `tmp/GO3-PARAMS/tui.md`（这三份是过程证据，不入库）。

## 0. 怎么读

每条参数一行：`key | 官方源(file:line) | 官方默认 | 宿主读取点(file:line) | 宿主使用点(file:line) | 我们的默认 | verdict`。

| verdict | 含义 |
|---|---|
| `OK` | 同源且真用上（或已修，见「修复记录」） |
| `HARDCODED` | 使用点绕过配置（写死在调用点） |
| `WRONG-DEFAULT` | 默认值与产品/官方语义相反 |
| `UNREAD` | 官方有、宿主没读（结构化未实现，不静默忽略）——**官方 342 条这一轴上已被逐条三分取代，见 §7；本表里保留的 5 条是宿主参数表的旧标签** |
| `ALIAS-MISMATCH` | 同一个东西两种 id/拼写，查表会 miss |
| `N-A` | 不适用（内部安全常量 / 协议形状 / 桌面壳面） |

官方 342 条覆盖轴（§7）用四个状态，不要与上表混用：

| status | 含义 |
|---|---|
| `COVERED` | 宿主有同名/同义读取点 |
| `COVERED-EQUIV` | 宿主有等价能力/参数，只是名字或形状不同（必须给 `hostEquivalent` file:line） |
| `N-A` | 对无头宿主不适用（必须带 `naClass`：desktop-shell / ui-only / telemetry-ui / plugin-market-ui / protocol-shape / naming-conflict） |
| `UNREAD-GAP` | 真能力缺口（必须写缺什么 / 现在降级成什么样 / 有无替代路径） |

## 1. 统计

- 总账条目 **239** 条（宿主 186 / TUI2GO 49 / 官方对齐 4）+ CLI flag **98** 个。
- verdict 分布（entries）：`OK` 134 / `HARDCODED` 2 / `UNREAD` 5 / `DEVIATION-SAFETY` 1 / `N-A` 97
- verdict 分布（CLI flags）：`OK` 22 / `HARDCODED` 0 / `UNREAD` 3 / `DEVIATION-SAFETY` 0 / `N-A` 73
- 词表（`meta.verdictVocabulary`）与实际取值**完全一致**：`OK` / `HARDCODED` / `UNREAD` / `DEVIATION-SAFETY` / `N-A`
- 官方 342 条的覆盖（逐条三分，见 §7）：`COVERED` 63 / `COVERED-EQUIV` 135 / `N-A` 31 / `UNREAD-GAP` 113 / `UNREAD` 0
- 已修条目按任务：`t10` 5 / `t12` 1 / `t2` 8 / `t3` 3 / `t5` 1 / `t7` 5 / `t8` 4 / `t9` 7

## 2. 修复记录（先红后绿）

| # | 任务 | 缺陷 | 先红 | 后绿 | 证据 |
|---|---|---|---|---|---|
| F1 | t9 | serve→run 六参数透传（--auto-compact/--max-chars/--workspace-bytes/--context-window/--max-output/--agents） | serve 侧 `flag provided but not defined` | TestServeHostRunFlagsReachChildArgs 等 4 条 argv 级测试 | `tmp/GO3-PARAMS/fix-passthrough/RESULT.md` |
| F2 | t9 | 压缩元数据 CompactionMeta.Model 写死 "run" | model="run" | 真实 model 参数落盘 | `tmp/GO3-PARAMS/fix-passthrough/RESULT.md` |
| F3 | t9 | acpRunner.keepRecent 死字段 | 改字段使用点不变 | 使用点走 compactSelectOptions() | `tmp/GO3-PARAMS/fix-passthrough/RESULT.md` |
| F4 | t8 | 模型 id 别名：无前缀 id 走“未声明自定义模型”分支多带 max_tokens=128000 | ResolveModelCapacity 无前缀 = {1M,128000} | ResolveModelID 唯一归一点；真实 dshd wire 无 max_tokens | `tmp/GO3-PARAMS/fix-alias/RESULT.md` |
| F5 | t10 | agent.Loop.maxSteps 隐藏 32 步兜底 | 不设上限时跑 32 步报“用满仍未收敛” | 0=不设上限；TestMaxStepsZeroMeansUnlimitedUntilCanceled | `tmp/GO3-PARAMS/official-align/RESULT.md` |
| F6 | t10 | contextWindow 优先级 env 排第一（官方主路径是模型目录） | catalog 2000000 + env 32000 → 32000 | catalog → env 兜底 → 内置表 → 0；两条测试 | `tmp/GO3-PARAMS/official-align/RESULT.md` |
| F7 | t7 | ACP --workspace-bytes 默认 8000 与 defaultWorkspaceBytes(65536) 不同源 | --workspace-bytes 在 acp.go:881 的默认值是 "8000" | TestGuardFlagDefaultsAreSingleSourced 全绿 | `tmp/GO3-PARAMS/register/red-guard.log / green-guard.log` |
| F8 | t7 | doctor --base 不读 DSHD_LLM_BASE（唯一一处绕过 envOr 的入口） | --base 在 doctor.go:266 的默认值是裸字面量 | envOr("DSHD_LLM_BASE", defaultLLMBase) | `tmp/GO3-PARAMS/register/red-guard.log / green-guard.log` |
| F9 | t7 | 网关 URL 字面量 9 处重复（改端口要改 9 个地方） | test 报 2 次出现（main.go/serve_title_auto.go） | defaultLLMBase 单一定义 + 字符串字面量守卫 | `tmp/GO3-PARAMS/register/red-guard.log / green-guard.log` |
| F10 | t7 | --home 两套默认（cwd 相对 tmp/dshd-home vs defaultHome()=~/.dshd） | --home 在 acp/doctor/main 的默认值是 envOr("DSHD_HOME","tmp/dshd-home") | 统一 defaultHome()；TestGuardNoCwdRelativeHomeDefault | `tmp/GO3-PARAMS/register/red-guard.log / green-guard.log` |
| F11 | GO.5/T1 | 官方 OTLP 端点 env（`DSH_TELEMETRY_OTLP_URL` / `DSH_PRODUCT_ANALYTICS_OTLP_URL`）宿主 0 处读取 | `go test ./internal/analytics/ -run 'CollectorURL\|OTLP'` → `undefined: collectorURLEnvNames`（build failed） | `internal/analytics/env.go` 的 `CollectorURLFromEnv`/`WithEnvCollectorURL` 接线；7 条测试全绿（不设不变 / 设了生效且零出站 / 非法 URL fail-loud / 显式配置优先） | `tmp/GO5-DELIVERABLE/env/RESULT.md` |
| F12 | GO.5/T1 | 官方 `DSH_MAX_TOKENS_AS_SUCCESS` 宿主 0 处读取（行为写死为“恒当成功”） | `go test ./internal/agent/ -run MaxTokens` → `undefined: MaxTokensAsSuccess`（build failed） | `internal/agent/finish_policy.go` 解析 false/0/no + loop 副作用前闸门；agent 4 条 + cmd/dshd 结算 1 条（真读 v4 `turn/end.reason.kind`）全绿 | `tmp/GO5-DELIVERABLE/env/RESULT.md` |
| F13 | GO.5/T1 | `DSH_HOME` 命名冲突未成文（宿主 home 根是 `DSHD_HOME`；TUI2GO 的 `DSH_HOME` 是桌面 profile home） | 总账原判 `COVERED-EQUIV`，reason 只说“只部分生效”，没说两个 `DSH_HOME` 撞车 | verdict 改 `N-A`（`naClass=naming-conflict`）+ `docs/go5-env-notes.md` §4 写清两个语义；**不实现别名** | `tmp/GO5-DELIVERABLE/env/RESULT.md` |

## 3. 防再犯守卫

| # | 文件 | 测试 | 拦什么 | 挂在哪个门禁 |
|---|---|---|---|---|
| G1 | `cmd/dshd/parameter_guard_test.go` | `TestGuardFlagDefaultsAreSingleSourced` | 同一旗标在多个入口各写一套默认值（AST 枚举所有 fs.Int/String/Bool 调用，比对源码表达式） | go test ./... |
| G2 | `cmd/dshd/parameter_guard_test.go` | `TestGuardNoDuplicatedGatewayURLLiteral` | 网关端点字面量在同一个包里出现第二份（词法扫描，注释不算） | go test ./... |
| G3 | `cmd/dshd/parameter_guard_test.go` | `TestGuardNoCwdRelativeHomeDefault` | 会话根目录再退化成 cwd 相对路径（tmp/dshd-home） | go test ./... |
| G4 | `cmd/dshd/parameter_guard_test.go` | `TestGuardHostRunOptionsFieldsAreAllForwarded` | hostRunOptions 新增字段忘了接进 childArgs（注入非默认值，断言真的传下去） | go test ./... |
| G5 | `internal/llm/model_alias_guard_test.go` | `TestGuardModelAliasHasSingleImplementation` | 模型 id 前缀归一在 model_alias.go 之外再写一份（"/"+model 形式的后缀拼接） | go test ./... |
| G6 | `internal/llm/model_alias_guard_test.go` | `TestGuardCapacityAndCatalogAgreeOnAlias` | 容量表与目录对同一个别名判据漂移（同一别名必须归一到同一 id/同一窗口） | go test ./... |

守卫的设计原则：**断言源码形状，不断言常量值**。断言“常量 == 2000”只是把默认值抄第二份；
枚举所有入口的默认表达式，才能拦住“新增入口又写一套”。

## 4. 宿主参数总表

| key | 官方源 | 官方默认 | 宿主读取点 | 我们的默认 | verdict | 备注 |
|---|---|---|---|---|---|---|
| `--max-steps@run` | agent.maxSteps / 官方宿主无此暴露项（安全阀，非预算） | — | `cmd/dshd/main.go:603` | defaultTurnMaxSteps = 2000 | **OK** |  |
| `defaultTurnMaxSteps` | agent.maxSteps | — | `cmd/dshd/serve.go:153` | 2000 | **OK** |  |
| `agent.Loop.maxSteps fallback` | agent.maxSteps | — | `internal/agent/loop.go:234` | 32 | **OK** | 0 现在明确表示不设上限（官方 agent loop 无 turn budget）；32 兜底已删。测试 TestMaxStepsZeroMeansUnlimitedUntilCanceled。 |
| `subRunner.timeout (subagent 墙钟)` | subagent.timeoutMs / dsh-subagent 的 timeout | — | `cmd/dshd/main.go:981` | 5 * time.Minute | **UNREAD** | 子代理 5min 墙钟写死在装配点。官方由插件传 timeout；宿主可加 --subagent-timeout，本任务不扩参数面。 |
| `subRunner.maxDepth` | subagent.maxDepth | — | `cmd/dshd/subagent.go:43` | 1 | **UNREAD** | 官方 subagent 有 maxDepth 概念，宿主写死 1（防递归）。做成 --subagent-max-depth 属新增能力，本任务不扩参数面；登记为结构化未实现。 |
| `subRunner.maxSteps` | agent.maxSteps | — | `cmd/dshd/main.go:981` | defaultTurnMaxSteps (2000) | **OK** |  |
| `--max-steps@serve` | agent.maxSteps | — | `cmd/dshd/serve.go:159` | defaultTurnMaxSteps = 2000 | **OK** |  |
| `--max-steps@acp` | agent.maxSteps | — | `cmd/dshd/acp.go:880` | defaultTurnMaxSteps = 2000 | **OK** |  |
| `--max-steps@schedule` | agent.maxSteps | — | `cmd/dshd/schedule.go:39` | defaultTurnMaxSteps = 2000 | **OK** |  |
| `childArgs 透传面（serve → run）` | n/a（宿主内部透传） | — | `cmd/dshd/serve_tools.go:113` | home/session/prompt/policy/max-steps/tools-mode/model/reasoning/record-effort/effort-level/prompt-content-file/turn-id | **OK** |  |
| `childArgs 缺口: --auto-compact` | compaction.autoCompactTokens | — | `cmd/dshd/serve_tools.go:113` | 子进程默认 40000（main.go:619） | **OK** | serve 注册 --auto-compact 并透传给子进程 run；TestServeHostRunFlagsReachChildArgs。 |
| `childArgs 缺口: --max-chars` | tools.spillThresholdChars | — | `cmd/dshd/serve_tools.go:113` | 子进程默认 4000 | **OK** | serve 注册 --max-chars 并透传；argv 级测试。 |
| `childArgs 缺口: --workspace-bytes` | workspace.agentsMdBytes | — | `cmd/dshd/serve_tools.go:113` | 子进程默认 65536 | **OK** | serve 注册 --workspace-bytes 并透传；argv 级测试。 |
| `childArgs 缺口: --context-window / --max-output` | model.contextWindow / model.maxOutputTokens | — | `cmd/dshd/serve_tools.go:113` | 0（按模型容量解析） | **OK** | serve 注册 --context-window/--max-output 并透传；run 侧接真实使用点（request/context、max_tokens）。 |
| `childArgs 缺口: --mode / --modes-dir / --remember-approvals / --agents` | modes.default / rules.path | — | `cmd/dshd/serve_tools.go:113` | 子进程各自默认（default / <home>/modes / false / 工作区 AGENTS.md） | **N-A** | --mode/--modes-dir/--remember-approvals 在 run 侧根本没有参数面（只有 acp 有），属功能缺口不是透传缺口；强塞给 serve 会让子进程报 flag not defined。已在 fix-passthrough 的 RESULT 登记接入前提（sandbox 档位优先级要先定）。 |
| `--policy@run` | permission.defaultPreset | — | `cmd/dshd/main.go:626` | defaultRunPolicy = "allow" | **OK** |  |
| `defaultRunPolicy` | permission.defaultPreset | — | `cmd/dshd/main.go:552` | "allow" | **OK** |  |
| `--policy@serve` | permission.defaultPreset | — | `cmd/dshd/serve.go:169` | "allow" | **OK** |  |
| `--policy@schedule` | permission.defaultPreset | — | `cmd/dshd/schedule.go:38` | "allow" | **OK** |  |
| `--policy@acp 缺失` | permission.defaultPreset | — | `cmd/dshd/acp.go:740` | 无该旗标 | **N-A** | ACP 没有 --policy 是能力面差异（审批由 --mode 的 Approval 或 session.setPermission 定），官方 ACP 也没有该旗标。 |
| `settingsform 默认权限枚举` | permission.defaultPreset（官方 zod enum） | — | `internal/settingsform/registry.go:191` | ["deny","ask","allow","auto"] | **OK** |  |
| `settingsform 默认模型字段约束` | model.defaultModel（官方 schemastery string().min(1).max(256)） | — | `internal/settingsform/registry.go:183` | minLength=1, maxLength=256 | **OK** |  |
| `settingsform namespace 名` | settings 命名空间契约 | — | `internal/settingsform/settingsform.go:14` | NamespaceModel="model", NamespacePermission="permission", FieldDefaultModel="defaultModel", FieldDefaultPreset="defaultPreset" | **N-A** |  |
| `officialCredentialDescribeLimit` | MAX_DESCRIBE_REFS（官方 index.js:55） | — | `internal/transport/handlers_settings_official.go:69` | 64 | **N-A** |  |
| `officialCredentialRefPattern` | credentialRefSchema（官方 index.js:56） | — | `internal/transport/handlers_settings_official.go:65` | ^[A-Za-z_][A-Za-z0-9_]*$ | **N-A** |  |
| `config.FileSchema` | config 文档 schema 版本 | — | `internal/config/tree.go:34` | "dsh.config-tree@1" | **N-A** |  |
| `config store 落点` | config 文件路径（官方为 settings 文档） | — | `cmd/dshd/serve.go:451` | <home>/state/config-tree.json | **OK** |  |
| `config maxDepth` | n/a（防呆） | — | `internal/config/tree.go:38` | 32 | **N-A** | 配置树防呆上限（32 段），不是产品参数。 |
| `config maxKeyLen` | n/a（防呆） | — | `internal/config/tree.go:39` | 128 | **N-A** | 配置键名 128 字节上限（ErrInvalidKey 的判据）。 |
| `maxConfigPatchBody` | n/a（additive 面） | — | `internal/transport/handlers_config_patch.go:36` | 1 << 20 (1 MiB) | **N-A** | additive 面请求体上限（1 MiB），防滥用。 |
| `maxPackageBytes` | n/a | — | `internal/transport/api_descriptors.go:56` | 1 << 20 | **N-A** | 插件包描述文件上限（1 MiB）。 |
| `settingsDefaultKey / settingsRevisionKey` | n/a（宿主内部） | — | `cmd/dshd/serve_settings.go:19` | "\x00settingsFormDefault" / "\x00settingsFormRevision" | **N-A** |  |
| `state/session-model.json` | session.modelOverride | — | `cmd/dshd/serve_model_override.go:38` | path | **OK** |  |
| `state/session-permission.json` | session.permissionOverride | — | `cmd/dshd/serve_permission_override.go:29` | path | **OK** |  |
| `state/session-sandbox.json` | session.sandboxMode | — | `cmd/dshd/serve_sandbox_policy.go:48` | path | **OK** |  |
| `state/session-preset.json` | session.preset | — | `cmd/dshd/serve_agent_api.go:1105` | path | **OK** |  |
| `state/session-effort.json` | session.reasoningEffort | — | `cmd/dshd/serve_effort_override.go:26` | path | **OK** |  |
| `state/model-catalog.json` | models.catalog | — | `cmd/dshd/serve.go:394` | path | **OK** |  |
| `state/approvals` | approval.remember | — | `cmd/dshd/acp.go:985` | <home>/state/approvals | **OK** |  |
| `DSHD_HOME` | home 目录 | — | `cmd/dshd/main.go:293` | ""（serve 必填）/ "tmp/dshd-home"（run/acp/doctor 默认） | **OK** |  |
| `run/acp/doctor 的 home 默认值` | home 目录 | — | `cmd/dshd/main.go:209` | "tmp/dshd-home"（相对路径） | **OK** | 统一走 defaultHome()（env 或 ~/.dshd）；守卫 TestGuardFlagDefaultsAreSingleSourced + TestGuardNoCwdRelativeHomeDefault。 |
| `defaultHome()` | home 目录 | — | `cmd/dshd/main.go:284` | $DSHD_HOME 或 ~/.dshd 或 ".dshd" | **OK** |  |
| `DSHD_LLM_MODEL` | model.defaultModel | — | `cmd/dshd/main.go:601` | "group/deepseek-v4.1-flash-1017" | **OK** |  |
| `DSHD_LLM_BASE` | provider.baseURL | — | `cmd/dshd/serve.go:295` | "http://127.0.0.1:3425/v1" | **OK** |  |
| `硬编码端口 3425` | provider.baseURL | — | `internal/transport/handlers_media_market_official.go:568` | http://127.0.0.1:3425/v1 | **OK** | cmd/dshd 侧只剩 defaultLLMBase 一处定义；守卫 TestGuardNoDuplicatedGatewayURLLiteral（字符串字面量级扫描）。 |
| `cotGatewayDefaultMdl` | model.defaultModel | — | `internal/transport/handlers_media_market_official.go:569` | "group/deepseek-v4.1-flash-1017" | **OK** | transport 侧 cotGatewayModelEnv 读 DSHD_LLM_MODEL、缺省常量与宿主默认同值；注释写清同源关系。 |
| `DSHD_LLM_KEY` | provider.apiKeyEnv | — | `cmd/dshd/serve.go:577` | ""（空默认，envOr 等价于 os.Getenv） | **OK** |  |
| `DSHD_LLM_API` | provider.api | — | `cmd/dshd/serve.go:333` | "" | **OK** |  |
| `DSH_GO_MODEL` | model.defaultModel（legacy 别名） | — | `cmd/dshd/serve.go:192` | "" | **N-A** | serve 侧空默认 = 未设；只用于 explicitServeModel 的 legacy 别名判定，不是默认值。 |
| `DSHD_REASONING_EFFORT` | model.reasoningEffort | — | `cmd/dshd/main.go:613` | ""（空=不带该参数） | **OK** |  |
| `DSHD_CONTEXT_WINDOW` | model.contextWindow | — | `cmd/dshd/transport_source.go:379` | 未设置 → 按 catalog/内置容量解析 | **OK** |  |
| `llm.DefaultCustomContextWindow` | model.contextWindow（未声明时的默认） | — | `internal/llm/capacity.go:5` | 1_000_000 | **N-A** | 未声明自定义模型的默认窗口（十进制 1M），与官方 llm-deepseek 的 DEFAULT_CONTEXT_WINDOW 同值；无配置面。 |
| `llm.DefaultCustomMaxOutput` | model.maxOutputTokens（默认） | — | `internal/llm/capacity.go:6` | 128_000 | **N-A** | 未声明自定义模型的默认输出上限（128K）；内置模型走 0（不带 max_tokens），只有真·自定义模型才用这个默认值。 |
| `builtInModelCapacity 内置表` | models.catalog | — | `internal/llm/capacity.go:58` | {"group/deepseek-v4.1-flash-1017": {ContextWindow: 1_000_000, MaxOutput: 0}} | **N-A** | 内置模型→容量的**数据表**（t8 起是唯一来源，id 列表由同一张表派生），不是参数。 |
| `模型 id 别名一致性` | search.model | — | `modules/search/deepseek.go:34` | "group/deepseek-v4.1-flash-1017"（8 处）vs "deepseek-v4-flash"（modules/search） | **N-A** | modules/search 用 deepseek-v4-flash 是**另一族**模型（搜索子系统），与主模型 id 不同源但不同用途。 |
| `DSHD_SANDBOX_MODE` | sandbox.mode | — | `internal/sandbox/policy.go:19` | read-only \| workspace-write \| danger-full-access | **OK** |  |
| `modes.DefaultID` | modes.default | — | `internal/modes/modes.go:63` | "default"（4 个内置：default / read-only / workspace-write / danger-full-access） | **OK** |  |
| `--mode@acp` | modes.default | — | `cmd/dshd/acp.go:889` | modes.DefaultID | **OK** |  |
| `--tools-mode / --tool-schema / --schema` | tools.tableShape | — | `cmd/dshd/main.go:622` | "" → defaultToolSchema = "compact" | **OK** |  |
| `toolsMode 枚举` | tools.tableShape | — | `cmd/dshd/tool_merged.go:40` | full \| compact \| merged \| lazy | **N-A** |  |
| `--auto-compact@run` | compaction.autoCompactTokens | — | `cmd/dshd/main.go:630` | 40000 | **OK** |  |
| `--auto-compact@acp` | compaction.autoCompactTokens | — | `cmd/dshd/acp.go:883` | 40000 | **N-A** | ACP 的 --auto-compact 默认就是 defaultAutoCompact（与 run/serve 同一常量），不是第二套默认值。 |
| `runCompactor.KeepRecent` | compaction.keepRecent | — | `cmd/dshd/main.go:1616` | 6（兜底 2） | **N-A** | run 自动压缩的保留条数（6，兜底 2）；压缩策略内部预算。 |
| `runCompactor.CompactionMeta` | compaction.model | — | `cmd/dshd/main.go:1615` | {Provider: "dshd", Model: "run"} | **OK** | 压缩元数据的 Model 从字面量 "run" 改为真实 model；先红后绿。 |
| `acpRunner.keepRecent` | compaction.keepRecent | — | `cmd/dshd/acp.go:111` | 8（字段，从未被读取） | **OK** | 死字段接线：使用点改为 r.compactSelectOptions()，默认值取改动前实际生效的 2。 |
| `acp autoCompactNow 选项` | compaction.keepRecent / keepTokens | — | `cmd/dshd/acp.go:1171` | KeepRecent: 2, KeepTokens: 2000, MinToCompact: 2 | **N-A** | ACP 自动压缩的保留预算（KeepRecent/KeepTokens/MinToCompact），与 compact.Options 同源。 |
| `compact.Options 默认值` | compaction.keepRecent / keepTokens | — | `internal/compact/compact.go:35` | KeepRecent=2, KeepTokens=2000, MinToCompact=2 | **N-A** | 内部压缩预算的零值默认（KeepRecent/KeepTokens/MinToCompact），由调用方覆盖。 |
| `compact.Summarize MaxTokens` | compaction.summaryMaxTokens | — | `internal/compact/compact.go:169` | 2000 | **UNREAD** | 摘要生成的单次输出上限 2000 写死；属模型调用参数，可加 --compact-max-tokens。 |
| `compact maxPromptChars` | compaction.summaryPromptChars | — | `internal/compact/compact.go:117` | 24000 | **N-A** | 摘要输入字符预算（24000），内部截断保护。 |
| `compact maxMessageChars` | compaction.summaryMessageChars | — | `internal/compact/compact.go:118` | 1200 | **N-A** | 单条消息进摘要前的截断长度（1200 rune）。 |
| `compact.Summarize 系统提示` | compaction.prompt | — | `internal/compact/compact.go:127` | "你是会话压缩器。只输出摘要正文，不要客套。" + 400 字以内 | **N-A** | 摘要提示词文本，不是参数。 |
| `session.EstimateRunes 比例` | n/a（估算器） | — | `internal/session/guard.go:49` | ascii/4 + other（4 字节≈1 token，CJK 1 字≈1 token） | **N-A** | 启发式 token 估算比例（4 字节≈1 token / CJK 1 字≈1 token）：官方 token meter 同样按估算。 |
| `llm retry 参数组` | llm.retry.* | — | `internal/llm/retry.go:40` | attempts=4, base=500ms, maxWait=8s, budget=20s, jitter=0.5 | **N-A** | HTTP 重放策略（attempts/base/maxWait/budget/jitter）：内部可靠性常量，官方无暴露项。 |
| `llm maxWireAttempts` | llm.wireFallbackAttempts | — | `internal/llm/compat.go:44` | 4 | **N-A** | 可选字段回退次数（stream_options 等），协议兼容用。 |
| `llm maxErrorBodyBytes` | n/a | — | `internal/llm/compat.go:48` | 8 << 10 | **N-A** | 错误响应体读取上限（8 KiB）。 |
| `llm capabilityTTL` | n/a | — | `internal/llm/compat.go:246` | 5 * time.Minute | **N-A** | wire 能力协商结果缓存 5min。 |
| `llm discoverTimeout` | provider.discoverTimeout | — | `internal/llm/discover.go:55` | 15 * time.Second | **N-A** | 模型发现兜底超时 15s。 |
| `llm discoverResponseBodyLimit` | n/a | — | `internal/llm/discover.go:58` | 512 | **N-A** | 模型发现响应体上限 512。 |
| `llm client http.Client.Timeout` | provider.timeout | — | `internal/llm/client.go:53` | 0（无客户端级超时） | **N-A** | 0 = 不设客户端级总超时，超时由每个请求的 ctx 控制（否则长流式响应会被腰斩）。 |
| `llm scanner buffer` | n/a | — | `internal/llm/client.go:307` | 初始 64KiB，上限 4MiB | **N-A** | SSE 行扫描缓冲（64KiB→4MiB）。 |
| `llm model_catalog 测试探针超时` | n/a | — | `internal/llm/model_catalog.go:746` | 5 * time.Second | **N-A** | 目录测试探针 5s 超时。 |
| `llm model_catalog 响应体上限` | n/a | — | `internal/llm/model_catalog.go:829` | 1 << 20 | **N-A** | 目录响应体 1MiB 上限。 |
| `llm model_env_catalog 文档上限` | n/a | — | `internal/llm/model_env_catalog.go:66` | 4 << 20 | **N-A** | env 目录文档 4MiB 上限。 |
| `transport defaultAddr` | server.addr | — | `internal/transport/server.go:34` | "127.0.0.1:0"（端口交给 OS） | **OK** |  |
| `transport ReadHeaderTimeout` | server.readHeaderTimeout | — | `internal/transport/server.go:273` | 10 * time.Second | **N-A** | HTTP 读头超时 10s（服务端防慢速攻击的常量）。 |
| `transport 通用 501 body drain` | n/a | — | `internal/transport/server.go:46` | limit 256KiB / timeout 2s | **N-A** | 501 前排空请求体的上限（256KiB/2s）。 |
| `transport 请求体上限 1MiB（多方法）` | n/a | — | `internal/transport/server.go:428` | 1 << 20 | **N-A** | 十处官方方法的请求体上限；协议级防滥用。 |
| `handlers_agent_official 请求体上限` | n/a | — | `internal/transport/handlers_agent_official.go:350` | 8 << 20 | **N-A** | agent 系方法请求体上限 8 MiB。 |
| `handlers_speech_official 请求体上限` | n/a | — | `internal/transport/handlers_speech_official.go:109` | 16 << 20 | **N-A** | 语音方法请求体上限 16 MiB。 |
| `officialStreamBodyLimit` | n/a | — | `internal/transport/official_stream_face.go:60` | 1 << 20 | **N-A** | 流方法请求体上限 1 MiB。 |
| `officialStreamPollInterval` | n/a | — | `internal/transport/official_stream.go:83` | 100 * time.Millisecond（var，测试可改） | **N-A** | 流帧机兜底轮询 100ms（包级 var，测试可改）。 |
| `officialControlPollInterval` | n/a | — | `internal/transport/handlers_session_official.go:2062` | 500 * time.Millisecond | **N-A** | 控制面采样 500ms。 |
| `officialQuestionClaimPollInterval` | n/a | — | `internal/transport/handlers_agent_official.go:1632` | 100 * time.Millisecond | **N-A** | 问题认领轮询 100ms。 |
| `agentQuestionWaitTimeout` | question.timeoutMs | — | `internal/transport/handlers_questions_official.go:43` | 120 * time.Second | **UNREAD** | question 等待上限 120s 写死（官方由工具传 timeoutMs）；可加 --question-timeout。 |
| `officialWaitDefaultTimeout` | session.wait.timeoutMs | — | `internal/transport/handlers_session_official.go:117` | 30 * time.Second | **N-A** |  |
| `officialWaitMaxTimeout` | MAX_TIMER_DELAY_MS（官方契约） | — | `internal/transport/handlers_session_official.go:119` | 2147483647 * time.Millisecond（2^31-1） | **N-A** |  |
| `officialWaitPollInterval` | n/a | — | `internal/transport/handlers_session_official.go:123` | 100 * time.Millisecond | **N-A** | session.wait 本地采样 100ms。 |
| `officialSearchResultLimit` | session.search.limit | — | `internal/transport/handlers_session_official.go:109` | 20 | **N-A** |  |
| `officialSearchSnippetMaxCodePoints` | 官方 snippet 上限 | — | `internal/transport/handlers_session_official.go:111` | 240 | **N-A** |  |
| `officialSearchQueryMaxChars` | 官方 query 上限 | — | `internal/transport/handlers_session_official.go:113` | 500 | **N-A** |  |
| `officialPageDefaultMessages` | session.messages.pageSize | — | `internal/transport/handlers_session_official.go:115` | 50 | **N-A** |  |
| `heartbeat defaultPingEvery / defaultIdleLimit` | ws.pingInterval / ws.idleTimeout | — | `internal/transport/heartbeat.go:21` | 30s / 90s | **N-A** | WS 心跳 30s / 空闲 90s，传输层常量。 |
| `wsMaxPayload` | ws.maxPayload | — | `internal/transport/ws.go:25` | 1 << 20（帧正文与解压后消息同限） | **N-A** | WS 单帧上限 1 MiB（五处引用同一常量）。 |
| `wsCompressionThreshold` | n/a | — | `internal/transport/ws.go:28` | 512 | **N-A** | 512 字节以下不压缩。 |
| `handlers_jobs 输出限额` | jobs.output.limit | — | `internal/transport/handlers_jobs.go:65` | DefaultJobOutputLimit=16KiB, MaxJobOutputLimit=64KiB | **N-A** | jobs.output 分页上下限（16KiB/64KiB）。 |
| `handlers_jobs wait 上限` | jobs.waitMs | — | `internal/transport/handlers_jobs.go:116` | 10 * time.Second | **N-A** | jobs 等待 10s 钳位。 |
| `handlers_jobs ctx 超时` | n/a | — | `internal/transport/handlers_jobs.go:439` | 5 * time.Second | **N-A** | 另一处 5s 超时；与 10s 钳位不同源但都是内部预算。 |
| `cotTranslationTimeout / cotTranslationMaxChars` | media.cotTranslation.* | — | `internal/transport/handlers_media_market_official.go:561` | 90s / 100000 | **N-A** | CoT 翻译等待 90s / 输入 10 万字符上限。 |
| `scheduleTitleMaxRunes` | schedule.title.maxLength（官方 at most 120） | — | `internal/transport/handlers_schedule_official.go:91` | 120 | **N-A** |  |
| `maxGoalLen` | goal.maxLength | — | `internal/transport/handlers_goal.go:14` | 500 | **N-A** | goal 一句话 500 字符（GoalBar 两三行）。 |
| `officialCredentialDescribeLimit` | MAX_DESCRIBE_REFS | — | `internal/transport/handlers_settings_official.go:69` | 64 | **N-A** |  |
| `approvalTimeout` | approval.timeoutMs | — | `cmd/dshd/serve_approval.go:104` | 120 * time.Second（DSHD_APPROVAL_TIMEOUT_SEC 可覆盖） | **OK** |  |
| `officialSteerInjectTimeout` | session.steer.timeoutMs | — | `cmd/dshd/serve_queue_official.go:43` | 30 * time.Second | **N-A** | steer 注入同步上限 30s。 |
| `cancelSettleWait` | session.cancel.settleMs | — | `cmd/dshd/transport_prompt.go:27` | 15 * time.Second | **N-A** | cancel 收尾等待 15s。 |
| `steerIPCWait` | n/a（内部 IPC） | — | `cmd/dshd/steer_ipc.go:28` | 5 * time.Second | **N-A** | steer IPC 单次等待 5s。 |
| `autoRoundSessionLockWait / Poll` | n/a | — | `cmd/dshd/serve_goal_round.go:283` | 2s / 10ms | **N-A** | goal 自动轮次的会话锁等待 2s / 轮询 10ms。 |
| `serve 启动器若干硬编码等待` | n/a | — | `cmd/dshd/serve.go:156` | 3s（模型发现）/ wait+5s / wait+5s / 7s | **N-A** | 启动期等待（模型发现 3s、wait+5s、7s）：进程编排常量，不是用户参数。 |
| `serve_plugin_market 客户端超时` | pluginMarket.timeoutMs | — | `cmd/dshd/serve_plugin_market.go:25` | 10 * time.Second | **N-A** | 插件市场客户端 10s。 |
| `serve_plugin_loader 热重载周期` | plugin.hmrInterval | — | `cmd/dshd/serve_plugin_loader.go:54` | 2 * time.Second | **N-A** | 插件热重载 2s 轮询。 |
| `serve_forge_state 等待` | n/a | — | `cmd/dshd/serve_forge_state.go:49` | 2 * time.Second | **N-A** | forge 状态等待 2s。 |
| `serve_tools follow 采样` | n/a | — | `cmd/dshd/serve_tools.go:402` | 200 * time.Millisecond | **N-A** | 工具 follow 采样 200ms。 |
| `serve_account_provider lease` | account.leaseSeconds | — | `cmd/dshd/serve_account_provider.go:28` | 30 * 24 * time.Hour | **OK** |  |
| `autoTitle 常量组` | autoTitle.* | — | `cmd/dshd/serve_title_auto.go:78` | maxInputBytes=4096, maxOutputTokens=64, maxBytes=120, targetWords=5, targetCJK=10, timeout=20s | **N-A** | 自动标题的输入/输出预算与超时（4096B/64tok/120B/20s）：官方也是内部常量。 |
| `officialScheduleHistoryMaxRecords / MaxDays` | schedule.history.* | — | `cmd/dshd/serve_schedule_api.go:63` | 100 / 30 | **N-A** | 调度历史保留 100 条 / 30 天：官方同样是服务端常量。 |
| `officialWorkspace 限额` | workspace.* | — | `cmd/dshd/serve_workspace_api.go:63` | EntryCap=512, MaxLines=2000, MaxBytes=4MiB | **N-A** | workspace 列表/读文件的限额（512 条/2000 行/4MiB）：协议防滥用。 |
| `officialTerminal 常量组` | terminal.* | — | `cmd/dshd/serve_terminal_api.go:62` | inputChunk=32B, maxScreen=256KiB, maxCols=500, maxRows=200, followPage=64KiB, followMaxRead=1MiB, followMaxPages=64, maxIDRunes=120, retainedPerSession=16, labelMaxRunes=200, followPoll=100ms | **N-A** | 终端分页与输入上限（11 个常量）：协议防滥用。 |
| `DSHD_TERMINAL_SCROLLBACK_ROWS` | terminal.scrollbackRows | — | `cmd/dshd/terminal_pty.go:61` | 未设置 → 宿主默认 | **OK** |  |
| `transport_source tail 快路阈值` | n/a（性能） | — | `cmd/dshd/transport_source.go:422` | tailThresholdBytes=4MiB, tailFrames=200 | **N-A** | v4 尾巴读取的快路阈值（4MiB/200 帧）。 |
| `attachments 限额` | attachments.* | — | `cmd/dshd/attachments.go:86` | attachmentMaxBytes=8MiB, attachmentMaxPixels=64MiB, promptMaxBlocks=64, promptMaxTextBytes=1MiB | **N-A** | 附件与提示块上限（8MiB/64MiB/64 块/1MiB）。 |
| `agentUploadMaxBytes` | uploads.maxBytes | — | `cmd/dshd/serve_agent_api.go:53` | 25 << 20 (25 MiB) | **N-A** | agent 上传 25MiB 上限。 |
| `jobs 分页快照限额` | n/a | — | `cmd/dshd/jobs_out.go:23` | maxJobPagerSnapshotBytes=1MiB, maxJobPagerSnapshotTries=4 | **N-A** | jobs 分页快照 1MiB / 4 次重试。 |
| `processImageQueryAttempts / Backoff` | n/a（平台细节） | — | `cmd/dshd/jobs_process_windows.go:20` | 3 / 20ms | **N-A** | 进程镜像查询 3 次 / 20ms 退避。 |
| `approval_stdio scanner buffer` | n/a | — | `cmd/dshd/approval_stdio.go:61` | 初始 64KiB，上限 8MiB | **N-A** | stderr 逐行扫描缓冲（64KiB→8MiB）。 |
| `parentwatch 默认组` | n/a（宿主看门狗） | — | `cmd/dshd/parentwatch.go:44` | parent-watch=true, interval=1s, misses=3, minInterval=20ms | **OK** |  |
| `parentwatch defaultParentWatchMisses` | n/a | — | `cmd/dshd/parentwatch.go:46` | 3 | **N-A** | 父进程看门狗 3 次未命中即退出。 |
| `--content-width@serve` | ui.contentWidth | — | `cmd/dshd/serve.go:162` | 0（不知道，界面自算） | **OK** |  |
| `--max-chars@run / @acp` | tools.spillThresholdChars | — | `cmd/dshd/main.go:605` | 4000 | **OK** |  |
| `defaultWorkspaceBytes` | workspace.agentsMdBytes | — | `cmd/dshd/main.go:545` | 65536 | **OK** | ACP 的 8000 已改成同一常量 defaultWorkspaceBytes；守卫 TestGuardFlagDefaultsAreSingleSourced。 |
| `--workspace-bytes@acp` | workspace.agentsMdBytes | — | `cmd/dshd/acp.go:881` | 8000（与 defaultWorkspaceBytes=65536 不同源） | **OK** | ACP 的 8000 已改成同一常量 defaultWorkspaceBytes；守卫 TestGuardFlagDefaultsAreSingleSourced。 |
| `defaultToolTimeout` | tools.timeoutMs | — | `modules/tools/pipeline.go:93` | 30 * time.Minute（DSHD_TOOL_TIMEOUT_SECS 可覆盖） | **OK** |  |
| `pipeline Options.MaxChars 默认` | tools.spillThresholdChars | — | `modules/tools/pipeline.go:125` | 4000 | **N-A** | tools pipeline 的 spill 默认 4000；run 侧 --max-chars 与之同值但由各自常量给出。 |
| `bash idle 预算` | bash.idleTimeout | — | `modules/exec/bash/tools.go:30` | 10 * time.Minute（Options.IdleBudget，无 flag/env） | **N-A** | bash 工具空闲预算 10min（Options.IdleBudget，调用点常量）。 |
| `pwsh 超时` | pwsh.timeoutSeconds | — | `modules/tool/pwsh/pwsh.go:99` | defaultTimeout=120s, maxTimeout=600s | **OK** |  |
| `execlit 超时` | exec.timeoutSeconds | — | `modules/tool/execlit/execlit.go:15` | defaultTimeout=60s, maxTimeout=600s | **OK** |  |
| `modules/search DeepSeekDefaultModel` | search.model | — | `modules/search/deepseek.go:34` | "deepseek-v4-flash" | **OK** |  |
| `DSHD_SEARCH_URL / DSHD_SEARCH_API_KEY / DSHD_SEARCH_MODEL / DSHD_SEARCH_BASE_URL` | search.* | — | `modules/search/search.go:8` | ""（未设置即不启用原生搜索） | **OK** |  |
| `DSHD_WEB_SEARCH_PROVIDER / DSHD_WEB_FETCH_PROVIDER` | webSearch.provider / webFetch.provider | — | `modules/search/search.go:14` | "" | **OK** |  |
| `DSHD_SANDBOX_WORKSPACE / _HOME / _EXTRA_READ_ROOTS / _EXTRA_READ` | sandbox.workspaceRoot 等 | — | `cmd/dshd/serve_tools.go:87` | "" | **OK** |  |
| `DSHD_APPROVAL_STDIO / DSHD_JOBS_STDIO / DSHD_STEER_STDIO` | n/a（内部协议） | — | `cmd/dshd/serve_tools.go:225` | "1" | **OK** |  |
| `DSHD_SERVE_NO_FOLLOW / DSHD_FOLLOW_MODE / DSHD_DEBUG_FOLLOW` | n/a（调试） | — | `cmd/dshd/serve_tools.go:280` | "1" / "resolve\|read" / "" | **OK** |  |
| `DSHD_SERVE_MODEL_RUNNER` | n/a | — | `cmd/dshd/serve.go:341` | "1" 才走无子进程 runner | **OK** |  |
| `DSHD_OFFICIAL_WIRE / DSHD_OFFICIAL_WIRE_SECRET` | officialWire.enabled | — | `cmd/dshd/serve_official_wire.go:42` | "1"（默认关）/ secret | **OK** |  |
| `DSHD_BOOT_FACADE / DSHD_FRONTEND_DIR` | frontend.dir | — | `cmd/dshd/serve_boot_facade.go:33` | "1" / "" | **OK** |  |
| `DSHD_PLUGIN_HMR / DSHD_PLUGIN_CACHE / DSHD_PLUGIN_REGISTRY / DSHD_PLUGIN_REGISTRY_TOKEN / DSHD_PLUGIN_MARKET_*` | plugin.* | — | `cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/serve_plugin_loader.go:80` | "1" / path / url / token / indexes | **OK** |  |
| `DSHD_SKILLS_EXTRA / DSHD_SKILLS_BUNDLED / DSH_BUNDLED_SKILL_DIR` | skills.* | — | `cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/serve_agent_api.go:1210` | "" | **OK** |  |
| `DSHD_GITHUB_API_URL / DSHD_SEARCH_* / DSH_LIVESHARE_RELAY` | *.baseURL | — | `internal/forge/internal/forge/internal/forge/internal/forge/internal/forge/internal/forge/internal/forge/change_request.go:38` | ""（用各自默认） | **OK** |  |
| `DSHD_ACCOUNT_* 组` | account.* | — | `cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/serve_account_provider.go:25` | provider / oidc issuer / client id / secret / scopes / device url / token url / userinfo url / platform origin / local lease seconds | **OK** |  |
| `DSHD_FORGE_PROVIDER_TOKEN / DSHD_FORGE_CHANGE_REQUEST_URLS` | forge.* | — | `cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/serve_forge_state.go:58` | "" | **OK** |  |
| `DSHD_TIMING / DSHD_TRACE_API / DSHD_PERF_120S / DSHD_LIVE_LLM` | n/a（诊断） | — | `cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/timing.go:13` | "" | **OK** |  |
| `DSHD_PARENT_PID` | n/a | — | `cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/parentwatch.go:159` | "" | **OK** |  |
| `DSHD_OFFICE_RENDERER / DSH_OFFICE_SRC / DSH_OFFICE_OUT` | office.renderer | — | `internal/officepdf/provider.go:66` | "" | **OK** |  |
| `DSHD_STEER_MANIFEST / DSHD_STEER_HELPER / DSHD_SUBAGENT_SEED_HOME / DSHD_SUBAGENT_SEED_PARENT / DSHD_SUBAGENT_SEED_CHILD` | n/a（内部） | — | `cmd/dshd/effort_goal_store_test.go:421` | "" | **N-A** |  |
| `DSHD_BASH / DSHD_MODEL_WIRE_ENV / DSHD_MODEL_WIRE_ARGV / DSHD_FAKE_CHILD_*` | n/a | — | `cmd/dshd/serve_loop_test.go:15` | 仅测试 | **N-A** |  |
| `DSHD_BASH` | n/a | — | `cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/cmd/dshd/serve_terminal_api.go:1213` | ""（用 PATH 上的 bash） | **OK** |  |
| `DSHD_SANDBOX_EXTRA_READ` | sandbox.extraReadRoots | — | `internal/sandbox/runtime.go:30` | "" | **N-A** | sandbox 额外可读根的 env 常量名；注释写作 _EXTRA_READ_ROOTS 属笔误，已在 docs/sandbox-policy.md 口径内。 |
| `transport handlers_config ConfigGet/ConfigSet 形状` | config.get | — | `internal/transport/handlers_config.go:23` | 扁平 map[string]string；同时回 values 与 config 两个键 | **OK** |  |
| `config.set 请求形状` | config.set | — | `internal/transport/handlers_config.go:60` | {"values":{...}} 或扁平 kv；空 values → 400 | **N-A** | 协议形状（两种 body 都收），不是参数。 |
| `runIDAnonymousSession` | n/a | — | `internal/agent/loop.go:209` | "anonymous" | **N-A** |  |
| `approval/steer/jobs 帧标签` | n/a（内部协议） | — | `cmd/dshd/approval_stdio.go:27` | "@@APPROVAL@@ " / "@@APPROVAL-RESP@@ " / "@@STEER@@ " / "@@STEER-RESP@@ " / "@@JOBS-OUT@@ " / "@@JOBS-OUT-RESP@@ " | **N-A** |  |
| `turn 事件名` | n/a（Transport 契约） | — | `cmd/dshd/serve_events.go:11` | "turn/done" / "turn/error" / "approval/request" / "approval/timeout" / "approval/cancel" | **N-A** |  |
| `serveJournalVersion / Frame / Reset` | n/a（journal 契约） | — | `cmd/dshd/serve_journal_store.go:22` | 1 / "frame" / "reset" | **N-A** |  |
| `handlers_agent_official 反馈枚举` | feedback.category | — | `internal/transport/handlers_agent_official.go:813` | other / task-result / instruction-following / product-interaction / service-stability / resource-cost | **N-A** |  |
| `officialQueuedPlacement` | session.queue.placement | — | `cmd/dshd/serve_queue_official.go:46` | "queued" | **N-A** |  |
| `sandbox 三档 + 四种 Action + Reason 词表` | sandbox.mode / sandbox.action | — | `internal/sandbox/policy.go:33` | read-only \| workspace-write \| danger-full-access；allow \| deny \| needs-approval；15 个 Reason 常量 | **N-A** |  |
| `EnforcementPolicyOnlyNoOSIsolation` | sandbox.enforcement | — | `internal/sandbox/policy.go:68` | "policy-only-no-os-isolation" | **N-A** |  |
| `--manifest 默认` | modules.manifest | — | `cmd/dshd/main.go:597` | "dsh.toml" | **OK** |  |
| `--session / --in / --out / --id / --cwd / --summary / --dry-run / --force / --advance` | n/a（CLI） | — | `cmd/dshd/main.go:607` | "" / "" / "" / "" / "" / "" / false / false / false | **OK** |  |
| `--keep / --max@session compact` | compaction.keepRecent | — | `cmd/dshd/main.go:1385` | 6 / 0 | **OK** |  |
| `--timeout@wasm call / wasm tool` | wasm.timeoutMs | — | `cmd/dshd/wasm.go:110` | 0（不限）/ 10s | **OK** |  |
| `--timeout@models` | n/a（CLI） | — | `cmd/dshd/models_cmd.go:24` | 15 * time.Second | **OK** |  |
| `--parent-watch / --parent-watch-interval / --parent-pid` | n/a | — | `cmd/dshd/parentwatch.go:277` | true / 1s / 0 | **OK** |  |
| `--remember-approvals@acp` | approval.remember | — | `cmd/dshd/acp.go:891` | false | **OK** |  |
| `--home@analytics / --enabled / --mode / --collector / --retention-days / --sampling-rate / --since` | analytics.* | — | `cmd/dshd/analytics_cmd.go:33` | defaultHome() / "" / "" / "" / 0 / 0 / "" | **OK** |  |
| `--root / --id / --path / --version@plugins` | plugin.root | — | `cmd/dshd/serve_plugins.go:178` | ""（必填）/ "" / "" / "" | **OK** |  |
| `--verify / --record-effort / --effort-level / --turn-id / --prompt-content-file` | n/a（内部） | — | `cmd/dshd/main.go:617` | 内部参数 | **N-A** |  |
| `builtInModelCapacity 精确匹配 + 别名漂移` | models.catalog 的 id 规范化 | — | `internal/llm/capacity.go:58` | switch modelID { case "group/deepseek-v4.1-flash-1017": {1_000_000, 0} } | **OK** | 新增 llm.ResolveModelID 作为唯一归一点；容量表与目录共用。守卫：TestGuardModelAliasHasSingleImplementation / TestGuardCapacityAndCatalogAgreeOnAlias。 |

## 5. TUI2GO 链路总表

| key | 插件读取点 | 我们的默认 | verdict | 备注 |
|---|---|---|---|---|
| `DSH_GO_HOME` | `public/dsh-tui2go/src/backend.ts:312` | (无默认值，必填) | **OK** | launcher 注入 PLAY/dsh-go-fresh（run-019.cmd:8）；与 DSH_HOME（PLAY/home，装 TUI profile 用）是两套根，不冲突。 |
| `DSH_GO_DSHD` | `public/dsh-tui2go/src/backend.ts:314` | 默认 'dshd'（PATH 查找） | **OK** | launcher 钉绝对路径 PLAY/bin/dshd.exe（run-019.cmd:7），detect 也用同一个（backend.ts:359）。 |
| `DSH_GO_POLICY` | `public/dsh-tui2go/src/backend.ts:284` | 插件回落 'ask'；launcher 现显式 'allow'（= 宿主产品默认 serve.go:169） | **OK** | 原判 OK 不成立（插件默认 ask 与产品默认 allow 相反）；t11 让 launcher 显式带 allow，t12 让插件在未配置时**一个 --policy 都不传**，默认由宿主决定。 |
| `DSH_GO_MODEL` | `public/dsh-tui2go/launcher/run-019.cmd:47` | launcher: 'deepseek-v4.1-flash-1017'（无 group/ 前缀） | **OK** | launcher 改用规范 id group/deepseek-v4.1-flash-1017；宿主另有唯一别名归一点兜底。 |
| `DSH_GO_DEBUG_LOG` | `public/dsh-tui2go/launcher/run-019.cmd:56-58` | launcher 默认开启：PLAY/logs/tui2go-debug.jsonl | **OK** | 默认关；仅 DSH_GO_DEBUG=1 时开且启动前截断。 |
| `DSH_GO_QUIT_TRACE` | `public/dsh-tui2go/src/backend.ts:407` | '1' 才开，默认关 | **OK** | 只写 stderr 一行，不影响行为。 |
| `DSH_PLAY_PROFILE / DSH_HOME（installer）` | `public/dsh-tui2go/launcher/install-play.mjs:53` | `DSH_PLAY_PROFILE` > `DSH_HOME/profiles/dsh-tui` > 从脚本自身位置推导（`<PLAY>/home/profiles/dsh-tui` 且旁边有 `bin/dshd.exe` 或 `npm/node_modules`）> fail-loud 报错并点名该设哪个变量 | **OK** | **GO.5/T5 已去绝对 fallback**：`locateProfile()` 三级推导，全部不成立时打 `[install-play] ERROR: cannot locate a dsh-tui play profile` 并列出试过的路径、给出三种修法（`DSH_PLAY_PROFILE` / `DSH_HOME` / `bootstrap-play.mjs`），`exit 2`。`grep -F 'C:\\' 'D:\\' launcher/` 0 命中。 |
| `DSH_TUI2GO_PRISTINE` | `public/dsh-tui2go/launcher/overlay/_pristine.mjs:64` | 未设 = 用默认 pristine 缓存根 | **OK** | 只影响离线取官方原始包的缓存位置，不进运行时链路。 |
| `DSHD_LLM_BASE` | `public/dsh-tui2go/launcher/run-019.cmd:48` | 'http://127.0.0.1:3425/v1' | **OK** | 显式写死但等于默认值，属冗余；真要换网关时这里才是唯一开关。 |
| `DSHD_LLM_MODEL` | `public/dsh-tui2go/launcher/run-019.cmd:47-48` | 未设（launcher 不设） | **OK** | launcher 走 DSH_GO_MODEL → 插件再传 --model，所以 DSHD_LLM_MODEL 空着是对的；但它同时是宿主 explicitServeModel 判据之一（serve.go:189），日后若两边都设要注意优先级。 |
| `DSHD_CONTEXT_WINDOW` | `public/dsh-tui2go/src/context.ts:47` | 未设 | **OK** | 不设才对：设了会把 catalog/内置表的真值压掉（TUI 端 context.ts:47 把 0 当“不知道”，不画进度条）。 |
| `PLAY（play 根）` | `public/dsh-tui2go/launcher/run-019.cmd:15-19` | 派生的 play 根：`%DSH_GO_PLAY%` > `%TEMP%\TUI2GO-PLAY`，再用 `%%~fI` 归一化（不再写死任何盘符） | **OK** | **GO.5/T2 已修**：默认值改为 `%TEMP%\TUI2GO-PLAY`，`DSH_GO_PLAY` 可预置；`TUI-GO.vbs` 读同一个变量（不再是第二份字面量）。证据：`grep -F 'C:\' 'D:\' launcher/run-019.cmd launcher/TUI-GO.vbs` 0 命中 + 副本真实链。 |
| `HOME / USERPROFILE` | `public/dsh-tui2go/launcher/run-019.cmd:23-24` | 均重定向到 PLAY | **OK** | 把 Node/npm 的用户态一起隔离到 play 根（install-play 取 pristine 时用得到）；**GO.5/T2 起由 PLAY 派生**（`set "HOME=%PLAY%"` / `USERPROFILE=%PLAY%`），隔离不变式未动。 |
| `DSH_HOME` | `public/dsh-tui2go/launcher/run-019.cmd:25` | PLAY\home | **OK** | TUI profile（含被 overlay 打过补丁的 0.19 包）在这里；**GO.5/T2 起由 PLAY 派生**（`set "DSH_HOME=%PLAY%\home"`）。命名冲突（宿主 home 根是 `DSHD_HOME`）见 `docs/go5-env-notes.md` §4。 |
| `PATH 前缀` | `public/dsh-tui2go/launcher/run-019.cmd:66` | '%PLAY%\npm\node_modules\.bin;%PATH%'（node 先 `where`，只在 PATH 完全没有时才回退 `%ProgramFiles%\nodejs`） | **OK** | **GO.5/T2 已修**：不再无条件 prepend `C:\Program Files\nodejs`；`where node` 命中就用宿主自己的 node（nvm/fnm/便携版都行），都没有才回退，再没有 `exit /b 3` 并说明装什么（package.json engines 要求 node ^22.19 \|\| >=24）。 |
| `插件工作目录` | `public/dsh-tui2go/launcher/run-019.cmd:74` | `%~dp0..`（launcher/ 的上一级 = 插件根），并用 `package.json` 存在性校验 | **OK** | **GO.5/T2 已修**：`cd /d "%~dp0.."` 从脚本自身位置推导，插件复制到任何目录都成立；找不到 `package.json` 时 `exit /b 4` 点名路径，不静默继续。 |
| `install-play 每次启动都跑 --ensure` | `public/dsh-tui2go/launcher/run-019.cmd:77` | node %~dp0install-play.mjs --ensure | **HARDCODED** | 幂等，但每次启动都会 walk src+launcher/overlay 做 sha256、按需 npm run build（install-play.mjs:36-52,123-126）→ 冷启动多一段可观测延迟；失败即 exit /b 拦住 TUI。 |
| `TUI 入口路径` | `public/dsh-tui2go/launcher/run-019.cmd:79` | %DSH_HOME%\profiles\dsh-tui\node_modules\@deepseek-harness-tui\dsh-tui\bin\dsh-tui.js --backend dsh-go | **OK** | 路径形状仍钉死 npm 包内层级（`%DSH_HOME%\profiles\dsh-tui\node_modules\@deepseek-harness-tui\dsh-tui\bin\dsh-tui.js`），但根已经由 PLAY 派生，**GO.5/T2 起不再是整机绝对路径**；0.19 → 0.20 若改 bin 落点仍要同步改这一行。 |
| `监听端口` | `public/dsh-tui2go/src/backend.ts:286` | '127.0.0.1:0'（OS 分配） | **OK** | 无固定端口，不会撞端口；连接信息来自 dshd 的 ready 行。 |
| `elyterminal 路径` | `public/dsh-tui2go/launcher/TUI-GO.vbs:39-48` | `%DSH_ELYTERMINAL%` > `<脚本>\..\..\tools\…`（打包） > `<脚本>\..\..\..\tools\…`（工作区） > 无终端则直接跑 cmd 且不失败 | **OK** | **GO.5/T2 已修**：三级推导（环境变量 → 包内 → 工作区），全部找不到时降级为「直接启动 launcher 的普通控制台」而不是静默失败；play 目录仍作为 `-d` 传入。 |
| `VBS 等待/激活时间` | `public/dsh-tui2go/launcher/TUI-GO.vbs:31-36` | `%DSH_GO_TUI_WAIT_MS%`（默认 4000）→ AppActivate(play 的 basename，10×500ms 重试) → `%DSH_GO_TUI_SETTLE_MS%`（默认 600）→ SendKeys（特殊字符转义） | **OK** | **GO.5/T2 已修**：等待/沉降可配置（默认值不变），窗口标题不再是第二份字面量而是 play 根的 basename（与 `-d` 同源）；`SendKeys` 对 `+^%~(){}[]` 做转义，且 AppActivate 失败时不盲打。 |
| `readyTimeoutMs（dshd ready 行等待）` | `public/dsh-tui2go/src/backend.ts:300` | 10_000 | **OK** | 超时后 terminate(child) 再抛错（backend.ts:313-316），不留孤儿。 |
| `terminate SIGKILL 宽限` | `public/dsh-tui2go/src/backend.ts:257` | 2_000 | **OK** |  |
| `detect: dshd --version` | `public/dsh-tui2go/src/backend.ts:362` | timeout 3_000 / maxBuffer 64 KiB | **OK** |  |
| `prefs lastUsed 保留条数` | `public/dsh-tui2go/src/backend.ts:236` | 200 | **OK** | 按时间戳排序后截断，不是 LRU 语义但同官方。 |
| `会话预览 limit` | `public/dsh-tui2go/src/backend.ts:1141` | Math.max(0, Math.min(20, limit ?? 6)) | **OK** |  |
| `session.create mode` | `public/dsh-tui2go/src/backend.ts:1083` | 'default' | **OK** |  |
| `context summary 取样` | `public/dsh-tui2go/src/context.ts:105` | session.load { tailMessages: 1 } | **OK** | summary 只取 1 条消息，靠 contextTokens/contextWindow 两个计数器，不拉全量。 |
| `contextWindow=0 的语义` | `public/dsh-tui2go/src/context.ts:47` | 0 → 不下发 max（界面不画占用条） | **OK** | 另有 context.usage 的 max 不得为 0（context.ts:60）。 |
| `权限模式 id 集` | `public/dsh-tui2go/src/modes.ts:22-43` | deny\|ask\|allow\|auto（4 档，去掉了 cycle） | **OK** | 不返回 cycle 是刻意的（modes.ts:54）：0.19 会把 cycle 再画一遍导致每行重复。 |
| `权限模式文案（ask 档）` | `public/dsh-tui2go/src/i18n.ts:84-86` | label '工作区读写' / desc '写入需先读该文件'（官方 `permission-preset-workspace-write*` 逐字） | **OK** | **GO.5/T3 已修**：4 档文案改由 `src/i18n.ts` 提供并逐字照抄官方 0.19 `permission-preset-*`（ask 从自造的「工作区内修改 / 需要审批的调用会先询问」改为官方文案）；`modes.list()` 每次从活的 `locale()` 现算，吃 `/lang`。`labelProvenance()` 可机读断言「官方 key vs derived」。 |
| `推理档位文案` | `public/dsh-tui2go/src/i18n.ts:126-130` | {off:关闭/Off(derived), low:低, medium:中, high:高, xhigh:超高, max:最高}（后五档 = 官方 `effort-tier-*`） | **OK** | **GO.5/T3 已修**：low/medium/high/xhigh/max 逐字照抄官方 `effort-tier-*`（i18n.js:198-202）；官方**没有** off 档 key，故 off 显式标 `{ derived: true }` 并说明为什么不复用 `picker-effort-default`。同样吃 `/lang`。 |
| `effort 默认档来源` | `public/dsh-tui2go/src/effort.ts:72-80` | catalog 行自己的 defaultReasoningEffort（缺失即 undefined，声明不合阶梯则抛错） | **OK** |  |
| `historyLimit（session.history 默认分页）` | `public/dsh-tui2go/src/transport.ts:259` | 256 | **OK** |  |
| `重连退避` | `public/dsh-tui2go/src/transport.ts:260-261,421-423` | initial 100 ms / max 5_000 ms，delay = min(max, initial * 2^attempt) | **OK** | **无尝试次数上限**（scheduleReconnect 只按回合累加 attempt，transport.ts:623-633）——官方也这样：宿主长时间掉线会一直以 5s 间隔重试。 |
| `WS maxPayload` | `public/dsh-tui2go/src/transport.ts:430` | 1024 * 1024（1 MiB） | **OK** | 单帧超 1 MiB（例如超长工具结果）会由 ws 层断开并走重连；宿主侧没有对应上限，属潜在可见断流点。 |
| `dispose 时 socket 关闭等待` | `public/dsh-tui2go/src/transport.ts:510-514` | 1_000 ms 后 terminate | **OK** |  |
| `HTTP 调用重试` | `public/dsh-tui2go/src/transport.ts:275-309` | 无（call() 不重试任何方法） | **OK** |  |
| `overlay 补丁数量` | `public/dsh-tui2go/launcher/install-play.mjs:151` | `scripts` 表 **9** 条（GO.5/T3 退场 permission-modes 后 10 → 9），`run()` 逐条 `execFileSync` 执行 `overlay/<name>` | **OK** | 目录里共 **11** 个 `.mjs` = 9 个补丁 + `_patch.mjs`（引擎）+ `_pristine.mjs`（原始字节源）；T3 退场 `permission-modes.mjs` 后由 12 → 11。 |
| `overlay: 4 档权限循环（permission-modes）` | `public/dsh-tui2go/src/modes.ts:28` | backend `capabilities.modes` 由 `src/modes.ts` 的 `permissionModes()` 提供（deny/ask/allow/auto），overlay 补丁已退场 | **OK** | **GO.5/T3 已收口（补丁退场）**：这个循环现在由 backend capability 提供（`src/modes.ts:28 permissionModes`），TUI 一看到 `capabilities.modes` 就 `delete` 自己的原生 `cycleMode`（dsh-adapter/channel/core/compose.js:1074），Shift+Tab 走 `modes.cycle() ?? modes.list()`（actions.js:28）。旧 overlay 对 DSH-GO 会话本就是死补丁（`modes:` 配置只喂原生 roster，extensions.js:179），已从 `install-play.mjs` 的 `scripts` 表移除并加 `retired` 迁移把旧 profile 原子回写官方 pristine。真实链：`tmp/GO5-DELIVERABLE/i18n/RESULT.md`。 |
| `overlay: flow-close 里的 timeoutMs: 8000` | `public/dsh-tui2go/launcher/overlay/flow-close.mjs:6` | 8000（出现在 NEEDLE 字符串里） | **OK** | 纯锚点：匹配不到就 AnchorMissing 报错退出；我们只在其后插一行 dispatchOverlay({type:'close-if',kind:'workspace-flow'})（:7），不写任何数字。 |
| `overlay: 其余 8 个补丁的字符串常量` | `public/dsh-tui2go/launcher/overlay/command-bridge.mjs:3-12` | 命令名/能力键/函数名等锚点（如 ['trace','any']、goals: input.dsh ...、backendStart = { ...prepared, ... }） | **OK** | channel-gate/command-bridge/workspace-capability/promptinput-gate/placeholder-workspace/core-ipc/workspace-action-readiness 逐个看过：无一设置超时、窗口、阈值；只有 permission-modes 改语义。 |
| `overlay: workspace-attach-trace 落盘` | `public/dsh-tui2go/launcher/overlay/workspace-attach-trace.mjs:51-27` | 每次 workspace 附着 appendFileSync 一行 JSONL 到 $DSH_GO_DEBUG_LOG | **HARDCODED** | 与 DSH_GO_DEBUG_LOG 同源：日志关掉它也就静默；开着时和 transport 的调试写共享同一文件，同样无轮转。 |
| `overlay: workspace 端口桥` | `public/dsh-tui2go/launcher/overlay/workspace-action-readiness.mjs:14,29` | globalThis.__DSH_GO_WORKSPACE_PORT__ | **OK** | 与 src/workspace.ts:63 常量同名，缺端口时一律 refuse（fail-loud），不静默返回空数组。 |
| `overlay 引擎: npm pack 超时/缓冲` | `public/dsh-tui2go/launcher/overlay/_pristine.mjs:100-101` | timeout 300_000 ms / maxBuffer 64 MiB | **OK** | 只在取 pristine 源码时用；300s 超时对慢网偏紧，但失败只影响 --restore/--audit 路径。 |
| `overlay 引擎: .bak 后缀` | `public/dsh-tui2go/launcher/overlay/_patch.mjs:66` | .dsh-go-<patch>.bak（每个补丁各一个后缀） | **OK** | 会在 dsh-tui 包里留 9 个 .bak 与若干 .dsh-tui2go-* 标记（GO.5/T3 退场 permission-modes 后 10 → 9）；`--restore` 依赖这些后缀，改名等于丢回滚点。 |
| `alias: launcher 模型 id vs 宿主导航/内置表键` | `public/dsh-tui2go/launcher/run-019.cmd:47` | 'deepseek-v4.1-flash-1017' vs 'group/deepseek-v4.1-flash-1017' | **OK** | launcher 改用规范 id group/deepseek-v4.1-flash-1017；宿主另有唯一别名归一点兜底（TestGuardCapacityAndCatalogAgreeOnAlias）。 |
| `alias: effort 阶梯的按模型查表` | `public/dsh-tui2go/src/effort.ts:100` | rows.get(ref.model)（catalog 行 id 作键） | **UNREAD** | TUI 线：catalog 为空时无法证伪；写入 catalog 后与 t8 的别名归一同源。 |
| `官方 0.19 无 dsh-go backend` | `public/dsh-tui2go/package.json:4` | dsh-tui@0.19.0 只带 backends/claude、backends/codex、backends/shared | **OK** | 所以 backend.ts 的移植基线只能是 0.18.0（src/backend.ts:4 注释已写明）；0.19 侧只提供 plugin/adapter 契约，不提供 dsh-go 参考实现——升级到 0.20 时没有官方 diff 可抄，只能看声明文件。 |

## 6. 官方对齐更正（t10）

| key | 官方源 | 我们的默认 | verdict | 说明 |
|---|---|---|---|---|
| `agentLoop.maxStepsPerTurn` | @dsa/dsh-agent-loop/README.md:220（"No built-in turn budget — tool calls or steering continue the current turn; a policy that bounds runaway turns must cancel from an existing lifecycle extension point such as agent/turn-stopping"）；全树 grep -rE 'maxSteps|stepLimit|maxTurns|maxStepsPerTurn' _official --include=*.js → 0 命中 | 2000 | **DEVIATION-SAFETY** | 不是“对齐官方”，是**安全阀**：官方没有上限，我们保留 2000 只挡死循环。观测数据：官方真实会话单轮步数 p50=13 / p99=938 / max=1318（tmp/GO3-PARAMS/official.md:20），2000 高于 max 且留 682 步余量，不限制真实使用。要彻底与官方一致得把这个值设为 0（=不限），但那会让无人值守入口真的可能永不收敛；采用“默认 2000 安全阀 + 0 表示不限”的形状，能力比官方只多不少。 |
| `agentLoop.loopMaxStepsFallback` | @dsa/dsh-agent-loop/README.md:220（无内置 turn budget） | 0 = 不设上限 | **OK** | 修复前：MaxSteps<=0 在循环里**静默降级成 32 步**（第二套默认值，调用方没给就撞墙且屏幕上查不到原因）。现在 0 明确表示“不限”，与官方一致；安全阀只来自调用方显式传的 2000。测试：internal/agent/loop_test.go TestMaxStepsZeroMeansUnlimitedUntilCanceled（跑到 40+ 步后由 ctx 取消结束，不是 32 步报错）。 |
| `usage.assistantUsage.shape` | usage.fields.streamShape（app.asar:dsh/node_modules/@deepseek-ai/dsh-api-session-controller/lib/typert.host.js:159 ｜ declaration :4008）：{inputTokens, outputTokens, totalTokens?, cacheReadTokens?, cacheWriteTokens?, reasoningTokens?}；四桶必填的是**客户端投影** usage.fields.projectionShape（@dsa/dsh-token-meter/lib/types/usage-projection.js:30）；usage.fields.promptTokens = input+read+write（usage-projection.js:58）；usage.placeholderPolicy = null 表示“还没有 prompt 输入”，永不渲染成 0.0（@dsh-client-ui-chat/lib/client.js:8785） | 键名照 stream shape；inputTokens = prompt - cacheRead - cacheWrite（未命中部分）；provider 没给的缓存计数**不写键、不写 0**；provider 明说 0 才写 0；无 usage 整体不写 | **OK** | 判断：**保持现状（缺键）**，不改成“没给也写 0”。依据是官方 wire 形状三处可选 + 官方对“0.0%”的判定：0.0% 只应出现在 promptTokens>0 && cacheReadTokens===0，把未知写成 0 就是可观测的谎报（official.md §3② 结论行）。四桶“永远齐”是客户端投影的 required 形状，不是 wire 的要求；宿主若强行补 0，反而破坏“不知道 vs 实测 0”的区分（界面的 cacheKnown 就是按键在不在判的）。现有测试已覆盖：assistant_usage_test.go 的 WritesDeepSeekCacheRead |
| `context.occupancy.contextWindowPrecedence` | llm.piAi.contextWindowResolution = entry.contextWindow ?? base.contextWindow ?? request.defaultContextWindow（@dsa/dsh-llm-pi-ai/lib/index.js:694）；DSH_CONTEXT_WINDOW 只出现在 dsh-sdk-minimal 且只喂 defaultContextWindow（@dsa/dsh-sdk-minimal/cordis.patch.yml:30 ｜ README.md:35）；context 占用只由 request/context 事件设置（@dsa/dsh-token-meter/lib/types/usage-projection.js:150） | catalog → DSHD_CONTEXT_WINDOW（兜底）→ 内置表 → 0 | **OK** | 修复前顺序是 env → catalog → 内置表 → 0，env 排第一 = 让运维开关压过真实模型元数据，与官方相反。现在目录声明优先，env 只在目录没这个模型时兜底。测试：cmd/dshd/transport_context_window_test.go TestLoadContextWindowCatalogBeatsEnv（目录 2000000 + env 32000 → 2000000）与 TestLoadContextWindowEnvFallbackWhenCatalogMisses（目录缺档 + env 32000 → 32000）。 |

## 7. 官方 342 条的覆盖情况（逐条三分：COVERED / COVERED-EQUIV / N-A / UNREAD-GAP）

> 本节由 `tmp/GO3-PARAMS/register.json` 直接生成（生成器 `tmp/GO3-PARAMS/triage/gen_section7.py`）——行号取 t3 刷新后的真值；判定源是 `official-coverage-triage.json`（210 条）+ `triage/na-subclass/na-subclass.json`（71 条 N-A 补子类/改判），回写脚本 `register/apply_triage.py`（就地更新）。
> 旧口径只有一个 `UNREAD` 大桶（210 条 note 全是同一句模板），不符合「逐条判定」；现在每条都有具体 verdict + 理由 + 宿主侧 file:line 证据。

| status | 含义 | 条数 |
|---|---|---|
| `COVERED` | 宿主有同名/同义读取点 | 60 |
| `COVERED-EQUIV` | 宿主有等价能力/参数，只是名字/形状不同（带 `hostEquivalent` file:line） | 139 |
| `N-A` | 对无头宿主不适用（按子类分组，**每条都带 `naClass`**） | 30 |
| `UNREAD-GAP` | 真能力缺口（缺什么 / 现在降级成什么样 / 替代路径） | 113 |
| **合计** | | **342** |

**统计演进**

| 口径 | COVERED | COVERED-EQUIV | N-A | UNREAD-GAP | UNREAD |
|---|---|---|---|---|---|
| 判定前 | 61 | — | 71 | — | 210 |
| t1 三分后 | 61 | 120 | 94（含 71 条无子类） | 67 | 0 |
| t5 补子类后 | 60 | 139 | 30 | 113 | 0 |

按域分布：

| 域 | 合计 | COVERED | COVERED-EQUIV | N-A | UNREAD-GAP |
|---|---|---|---|---|---|
| `agent-loop` | 65 | 17 | 30 | 6 | 12 |
| `llm` | 34 | 2 | 22 | 1 | 9 |
| `context` | 21 | 4 | 5 | 0 | 12 |
| `session` | 37 | 6 | 16 | 4 | 11 |
| `tools-sandbox-approval` | 35 | 7 | 14 | 1 | 13 |
| `transport-http` | 24 | 4 | 12 | 1 | 7 |
| `tui-capability` | 26 | 7 | 11 | 6 | 2 |
| `env` | 15 | 0 | 10 | 4 | 1 |
| `live-profile-desktop` | 24 | 10 | 7 | 3 | 4 |
| `schedule-plugins-account-telemetry` | 61 | 3 | 12 | 4 | 42 |

---

### COVERED（60 条，宿主有同名/同义读取点）

- **agent-loop**（17）：`agentLoop.agents`、`agentLoop.agents[].provider`、`agentLoop.agents[].model`、`agentLoop.agents[].reasoningEffort`、`agentLoop.agents[].maxTokens`、`retry.mode`、`steering.steer`、`steering.inject`、`subagent.maxDepth`、`toolCallTimeoutPolicy.enforcement`、`timeout.maxTimerDelayMs`、`toolResult.prune.thresholdChars`、`bash.timeoutMs`、`pwsh.timeoutMs`、`tool.web.search`、`tool.web.fetch`、`tool.fsSearch.timeoutMs`
- **llm**（2）：`llm.deepseek.models`、`llm.defaultModel`
- **context**（4）：`context.window`、`context.compaction.auto`、`context.compaction.maxTokens`、`context.agentInstructions.maxBytes`
- **session**（6）：`session.persistence.root`、`session.persistence.compression`、`session.titleLlm.autoTitle`、`session.titleLlm.maxOutputTokens`、`session.titleLlm.timeoutMs`、`session.query.path`
- **tools-sandbox-approval**（7）：`sandbox.modes`、`sandbox.mode`、`sandbox.workspaceRoot`、`approval.policy`、`permission.defaultPreset`、`skillFs.watch`、`terminal.scrollback`
- **transport-http**（4）：`webserver.port`、`webserver.compression`、`sessionController.waitTimeoutMs`、`workspaceFiles.maxBytes`
- **tui-capability**（7）：`context.occupancy.source`、`models.reasoningEffort`、`modes.agentPresets.default`、`modes.agentPreset.plugins`、`queue.placement`、`queue.actions`、`session.state`
- **live-profile-desktop**（10）：`agentDefaultModel.provider`、`agentDefaultModel.model`、`agentDefaultModel.reasoningEffort`、`llmPiAi.providers.magpie.baseURL`、`llmPiAi.providers.magpie.models[0].contextWindow`、`llmPiAi.providers.magpie.models[0].maxTokens`、`llmPiAi.providers.magpie.models[1].contextWindow`、`llmPiAi.providers.magpie.models[1].maxTokens`、`subagent.maxDepth`、`permission.defaultPreset`
- **schedule-plugins-account-telemetry**（3）：`analytics.enabled`、`telemetry.session.mode`、`hmr.root`

---

### COVERED-EQUIV（139 条，宿主等价能力，名字/形状/数值不同）

> 每条都给了宿主侧读取点（`hostEquivalent`），可直接 `grep -n` 复核。

#### agent-loop（30 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `agentLoop.agents[].cwd` | spi.SubagentRequest.Cwd（modules/tool/subagent/subagent.go:24 schema / :42 字段 / :82 透传）→ cmd/dshd/subagent.go:72-95 解析并 chdir | subagent/subagent_fork 工具都暴露 cwd 入参，宿主在子代理执行期间进程级 chdir 到该目录（并用 subCwdMu 串行化），语义与官方一致。 |
| `agentLoop.agents[].resumeSessionId` | --session 续会话（cmd/dshd/main.go:607 `resumeSession := fs.String("session", ...)` / :774 / :817）+ subagents.prompt 续问（cmd/dshd/serve_subagents_api.go:83 Deliver） | 宿主有两种续接：主会话用 --session 续已持久化会话（cwd 可省、按 id 全盘找），子代理用 subagents.prompt 往已 spawn 的 child 会话投递；合起来覆盖官方 resumeSessionId 的“续一个已存在会话而不是新建”。 |
| `agentLoop.agents[].sessionId` | ChildRecord.ChildSessionID（internal/agent/child_session.go:51-56）+ subagents.prompt 的 (parentSessionId, childSessionId) 地址（cmd/dshd/serve_subagents_api.go:83） | 宿主用 childSessionId 表达“这是哪个子代理会话”，且是 durable 的（写进 v4 的 subagent/spawn 父子帧），续问时按 (parentSessionId, childSessionId) 认领；与官方 exact session identity 同一能力，只是 id 由宿主生成而非调用方给。 |
| `agentLoop.turnBudgetEnforcement` | agent.Loop.MaxSteps（internal/agent/loop.go:162 字段 / :234-240 maxSteps() / :274-275 超限报错） | 官方只提供扩展点（插件在 agent/turn-stopping 上取消），宿主把这件事做成了原生硬上限：Loop.MaxSteps 用满即报 turn 用满了 N 步，效果等价且不依赖插件钩子。 |
| `bash.maxOutputBytes` | modules/exec/bash/session.go:33 `maxRetainedJobOutput = 1024 * 1024` + modules/tools/pipeline.go:22-24 MaxChars=4000 + :525-575 spill | 宿主用两层有界：执行层保留窗口 1MiB（maxRetainedJobOutput）+ 工具层 spill 4000 字符截断并外置；官方 64000 字节内联上限在宿主是“更早的字符阈值 + 更大保留窗口”的组合。 |
| `bash.maxSpillBytes` | modules/tools/pipeline.go:120-122 SpillDir 默认 `<temp>/dshd-spill` + :693 writeSpill / :547-556 Artifacts | 宿主把外置统一交给 tools.Pipeline.writeSpill（落 <temp>/dshd-spill 下的 artifact 并回指针），但没有 64MB 这个大小上限——能力在、上限缺席（记录在 reason，不额外记一次缺口）。 |
| `bash.maxTimeoutMs` | modules/exec/bash/tools.go:26-31 `func (o Options) idle() time.Duration { ... return 10 * time.Minute }` + :74 schema `idle_seconds` maximum 900 | 宿主 bash 工具没有“单次调用硬上限”这个参数，但把等价预算给了 idle 超时：默认 10 分钟（=600000ms，与官方默认同值），且 schema 允许 idle_seconds 1..900 覆盖。 |
| `pwsh.maxOutputBytes` | modules/tool/pwsh/pwsh.go:47 `maxOutput = 64 << 10` + :187 limitWriter + :198 截断提示 | 宿主 pwsh 工具用 limitWriter 把单次输出钳到 64 KiB（与官方 64000 同量级），并在截断时补一行中文提示告诉模型怎么收窄。 |
| `pwsh.maxSpillBytes` | modules/tools/pipeline.go:525-575（所有工具结果统一 spill，含 pwsh）+ :120-122 默认 spill 目录 | pwsh 结果和其它工具一样走宿主统一 spill 层（超 4000 字符外置成 artifact + 指针），官方 64MB 的 spill 文件上限在宿主不存在，但“外置而不是无限内联”的能力在。 |
| `pwsh.maxTimeoutMs` | modules/tool/pwsh/pwsh.go:49-50 `defaultTimeout = 120 * time.Second` / `maxTimeout = 600 * time.Second` + :167-175 夹取 | 宿主 pwsh 的 maxTimeout 就是 600s（=官方 600000ms），默认 120s，入参 timeout_seconds 可覆盖并夹到上限。 |
| `retry.initialDelayMs` | internal/llm/retry.go:47 `defaultRetryBase = 500 * time.Millisecond` | 退避基值 500ms 与官方默认一致，落在 defaultRetryBase；只是宿主不可由配置覆盖（编译期常量）。 |
| `retry.jitterRatio` | internal/llm/retry.go:52-53 `defaultRetryJitter = 0.5` / :57 jitter 字段 / :131-140 向下抖动实现 | 宿主有抖动比例字段且默认 0.5（只向下抖，(1-jitter)*d ~ d），官方默认 0.1；同一能力、不同默认值，注释里写清了为什么只向下抖。 |
| `retry.maxDelayMs` | internal/llm/retry.go:50 `defaultRetryMaxWait = 8 * time.Second` | 宿主有“单次等待上限 + Retry-After 天花板”，值是 8s 而不是官方 10s；语义相同（超过就不再等），数值不同。 |
| `retry.maxRetries` | llm.retryConfig.attempts（internal/llm/retry.go:44 `defaultRetryAttempts = 4` / :84-92 defaultRetryConfig） | 宿主有一等公民的重试策略（指数退避 + 抖动 + 单次等待上限 + 总预算），次数为 4 次尝试（1 首发 + 3 重试）对比官方 5 次重试——能力在、默认值不同。 |
| `retry.retryableCodes` | internal/llm/retry.go:199-204 retryableStatus（429 / 5xx）+ :22-36 分类注释 | 宿主按“可安全重放”的失败类判重试：429/5xx/连接层瞬时错误重试，401/403/404/400/422 一次都不重试，且流已开始吐字后不重放；与官方按 LlmFailure 码（RATE_LIMIT/SERVER/TIMEOUT/TRANSPORT）分类等价，但 EMPTY_RESPONSE 在宿主是 turn 级硬失败而不是重试项。 |
| `steering.cancelClearsInbox` | internal/agent/loop.go:245-247 `defer l.Steers.Close()` + :127-131 drainOrClose 关闭即停止 | 宿主的取消路径同样清空未消费的 steer 项：Run 用 defer 关闭信箱，close 后队列整体丢弃（drainOrClose 在关闭时返回“不再继续”），等价于官方 cancel() 默认清 inbox。 |
| `steering.followup` | agent.SteerInbox（internal/agent/loop.go:85-131 Submit/Close/drain/drainOrClose）+ :163-164 注入点 + :290 调用点 + :430-431 emitSteers | 宿主的 steer 信箱就是官方 followup 的落点：turn 内 FIFO 接收用户消息，在当前 turn 的下一模型步边界注入，Run 返回时关闭并清空未消费项——语义与官方“排队到下一个 turn 边界领取”一致。 |
| `subagent.providers` | modules/tool/subagent/subagent.go:1（spawn）+ modules/tool/subagentfork/subagentfork.go:67（fork），均在 cmd/dshd/main.go 的模块表注册 | 官方两个 in-process provider（spawn / fork）在宿主各有一个工具实现：subagent 走真实 spawn（可选 durable 子会话），subagent_fork 走 fork 语义；provider 名不同但能力面一致。 |
| `tool.fsSearch.globMaxResults` | modules/tool/glob/glob.go:33-34 `defaultLimit = 200` / `maxLimit = 1000` + :25 schema limit | 宿主 glob 工具默认返回 200 条（官方默认 100），上限可由入参 limit 收到 1000；语义相同（内联展示条数），默认值不同。 |
| `tool.fsSearch.grepMaxLineBytes` | modules/tool/grep/grep.go:43 `maxLineChars = 400` + :194-196 逐行截断 | 宿主对每行预览有 400 字符上限（官方 2000 字节），截断点更靠前；能力相同、阈值不同。 |
| `tool.fsSearch.grepMaxMatches` | modules/tool/grep/grep.go:40-41 `defaultLimit = 100` / `maxLimit = 500` + :198-201 达上限即停 | 宿主 grep 默认 100 条、上限 500 条（官方 250 默认）；同样是“内联保留多少条命中”的预算，数值不同。 |
| `tool.fsSearch.sampleOverCapGlobResults` | modules/tool/glob/glob.go:55-59 payload.Truncated 字段 + :33-34 defaultLimit/maxLimit（超限即截断，无采样实现） | 官方默认 false 的语义是“超上限时保留 mtime 序的头部而不是采样一页”；宿主 glob 同样是超限即截断（truncated=true + 保留前 N 条），没有采样分支，等价于该参数取 false。 |
| `tool.web.fetchMaxOutputChars` | modules/tool/webfetch/provider.go:30-31 MaxBodyChars 字段 / :45 `defaultMaxBodyChars = 200000` / :65-70 maxBodyChars() / :186-188 截断 | 宿主 webfetch 解码后的字符上限默认 200000，与官方默认逐字一致，落在 defaultMaxBodyChars。 |
| `tool.web.fetchTimeoutMs` | modules/tool/webfetch/provider.go:32-33 Timeout 字段 / :46 `defaultProviderTimeout = 30 * time.Second` / :72-77 timeout() / :102 context.WithTimeout | 宿主 webfetch 的 HTTPProvider 有兜底超时 30s（=官方 30000ms），且可被 Provider.Timeout 覆盖。 |
| `tool.web.searchMaxQueries` | modules/tool/websearch/websearch.go:38 `maxQueries = 4` + :79-81 超限 Hint | 宿主 web_search 一次最多 4 条查询，与官方默认一致；超限直接回 Hint 让模型合并，而不是静默丢弃。 |
| `tool.web.searchMaxResults` | modules/tool/websearch/websearch.go:38-39 `maxQueries = 4` / `maxResults = 8` + :102 runSearchQueries(..., maxResults) | 宿主 web_search 的合并来源上限就是 8，与官方默认一致，落在 maxResults 常量（不可配置）。 |
| `toolResult.prune.headChars` | prune.headShare（internal/prune/prune.go:41-43 headShare=6 / shareBase=10）+ :142-166 cutLines 留头 | 宿主 prune 包同样“留头留尾、中间换一行说明”，但按比例（头 60%）而不是固定 4096 字符；默认预算 16KB/200 行。 |
| `toolResult.prune.marker` | internal/prune/prune.go:163 `notice := fmt.Sprintf("…（第 %d–%d 行已省略，共 %d 行）…", ...)` + :35-39 NoPointer | 宿主有等价的省略标记（一行中文占位，带被省略的行区间与总行数，并附“原文在哪/有没有原文”的指针），只是文案不是官方那句英文常量。 |
| `toolResult.prune.tailChars` | internal/prune/prune.go:156 `tail := usable - head`（尾段保留）+ :166-175 拼接 | 同 headChars：尾巴按剩余 40% 保留（tail := usable - head），不是固定 1024 字符，但“保住错误/结论那段”的语义一致。 |
| `toolResult.spill.maxInlineTokens` | tools.Pipeline.Options.MaxChars（modules/tools/pipeline.go:22-24 默认 4000 字符）+ :525-575 spill() + :582-600 spillText() | 宿主有 spill 层（超阈值外置成 artifact + 指针），但预算口径是字符而不是 token：阈值 4000 字符、正文留头 3/4 留尾 1/4；官方 12500 token 的内联预算在宿主用更严格的字符阈值实现。 |

#### llm（22 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `llm.callConfig.adapterDefaults` | internal/agent/loop.go:315-317 `MaxTokens: capacity.MaxOutput` / `Reasoning: l.Reasoning`（注释：只有显式要了才带）+ internal/llm/provider.go:87-96 adapter 的 MaxTokensField 拼写 | 官方让 adapter 只允许给 reasoningEffort / maxTokens 兜默认；宿主同形：loop 先按模型解析出容量再填 MaxTokens，reasoningEffort 只有显式给了才带（避免打挂不支持该参数的网关），语义逐条对上。 |
| `llm.callConfig.routing` | llm.Request.Model（internal/llm/types.go）+ resolveServeProvider（cmd/dshd/model_steer.go:231 `base, credential, api = provider.BaseURL, provider.Credential(), provider.API`） | 宿主请求必须带 model，provider 由 resolveServeProvider 从模型目录/别名解析出 (baseURL, credential, API adapter)，等价于官方 provider + model 必填的路由对。 |
| `llm.deepseek.defaultContextWindow` | internal/llm/capacity.go:6 `DefaultCustomContextWindow = 1_000_000` + ResolveModelCapacity 兜底 | 宿主对未声明容量的自定义模型兜底 1000000，与官方默认逐字一致。 |
| `llm.deepseek.defaultMaxTokens` | internal/llm/capacity.go:5-8 `DefaultCustomMaxOutput = 128_000` + :79-104 ResolveModelCapacity 兜底 | 宿主对未声明容量的自定义模型给 maxOutput 兜底 128000（官方 256000）；语义相同（路由省略时填输出上限），数值不同。 |
| `llm.deepseek.imageOffload.byteQuantum` | internal/session/image_offload.go:84 `DeepSeekImageOffloadByteQuantum = 64 * 1024 * 1024` | 宿主保留 raw 表示的退役字节量子 64 * 1024 * 1024，与官方 67108864 逐字一致。 |
| `llm.deepseek.imageOffload.countQuantum` | internal/session/image_offload.go:88 `DeepSeekImageOffloadCountQuantum = 20` | 宿主保留 deepseek 路由的张数量子 20（另有 pi-ai 路 10），与官方默认一致。 |
| `llm.deepseek.maxImagesPerRequest` | internal/session/image_offload.go:82 `DeepSeekMaxImagesPerRequest = 600` | 宿主保留单请求图片张数上限 600，与官方默认逐字一致。 |
| `llm.deepseek.maxInlineRequestImageBytes` | internal/session/image_offload.go:80 `DeepSeekMaxInlineRequestImageBytes = 20 * 1024 * 1024` | 宿主保留 base64 内联图片预算 20 * 1024 * 1024，与官方 20971520 逐字一致。 |
| `llm.deepseek.maxRequestFilesBytes` | internal/session/image_offload.go:78 `DeepSeekMaxRequestFilesBytes = 128 * 1024 * 1024` | 宿主 image-offload 移植里保留了 deepseek raw 表示的单请求字节上限 128 * 1024 * 1024，与官方 134217728 逐字一致。 |
| `llm.deepseek.modelsTimeoutMs` | internal/llm/discover.go:51-55 `const discoverTimeout = 15 * time.Second` + :116 `reqCtx, cancel := context.WithTimeout(ctx, discoverTimeout)` | 宿主模型发现（GET /models）有 15s 墙钟上限（官方 5000ms），语义相同（单次发现超时）、数值更宽。 |
| `llm.deepseek.officialModelContextWindow` | internal/llm/capacity.go:38-48 `builtInModels = []builtInModel{{ID: "group/deepseek-v4.1-flash-1017", Capacity: ModelCapacity{ContextWindow: 1_000_000, ...}}}` | 官方给内置模型声明 1048576（1 MiB）；宿主给内置模型 group/deepseek-v4.1-flash-1017 声明 1000000，并明确以 magpie 网关 /v1/models 的实测 context_length 为准（十进制 1M）。能力等价、口径按实测修正。 |
| `llm.deepseek.publicBaseURL` | cmd/dshd/main.go:290 `const defaultLLMBase = "http://127.0.0.1:3425/v1"` + :602 `base := fs.String("base", envOr("DSHD_LLM_BASE", defaultLLMBase), ...)` | 官方原生 DeepSeek 端点是编译期常量 https://api.deepseek.com/anthropic；宿主把端点做成可配的 --base / DSHD_LLM_BASE（单源常量 defaultLLMBase=本机网关），能力等价但默认值不同（本机网关 vs 官方直连）。 |
| `llm.deepseekLlmApiExtensions.mounted` | internal/llm/provider.go:104-115 deepSeekCompletionsAdapter.UsageExtensions() + internal/llm/usage_cache.go:30-40 usageCacheReadPaths/usageCacheWritePaths | 官方 base 里挂了 DeepSeek Chat-API 扩展（usage 的 cache 计数等）；宿主把同一批扩展做成 adapter 的 UsageExtensions 白名单 + usage_cache 的叶子路径解析，实测形状对齐。 |
| `llm.defaultModelProvider` | cmd/dshd/main.go:601 / :1387 / cmd/dshd/serve.go:181 `envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017")` + cmd/dshd/serve.go:192 explicitServeModel | 官方 base bundle 的默认路由是 deepseek-official；宿主有等价的“默认模型”单源默认值（--model / DSHD_LLM_MODEL，默认 group/deepseek-v4.1-flash-1017），provider 由该模型解析。 |
| `llm.discoverModels.fields` | transport.OfficialDiscoveredModel（cmd/dshd/serve_account_api.go:347-350 / :362-373 两处投影） | 宿主 discoverModels 回的就是官方五个字段（id / name? / contextWindow? / maxTokens? / inputModalities?），逐条有落点。 |
| `llm.modelInfo.fields` | transport.OfficialDiscoveredModel / 模型信息投影（cmd/dshd/serve_account_api.go:362-373 `ID/Name/ContextWindow/MaxTokens/InputModalities`） | 宿主模型信息投影有官方同形字段（id/name/contextWindow/maxTokens/inputModalities），provider 由路由隐含；description/systemPromptUpdate/toolUpdate 等可选字段宿主不填充但形状兼容。 |
| `llm.piAi.baseUrlResolution` | internal/llm/provider_registry.go:39 DefaultBaseURL 字段 / :120 `DefaultBaseURL: d.baseURL, BaseURLEnv: ...` + cmd/dshd/model_steer.go:231 provider.BaseURL 优先 | 官方的 baseURL 优先级链（entry.baseURL ?? base.baseUrl ?? providerBaseUrl）在宿主落到 provider 注册表的 DefaultBaseURL/BaseURLEnv 与 --base 覆盖链上：模型目录声明 → provider 默认 → 全局 --base。 |
| `llm.piAi.contextWindowResolution` | internal/llm/capacity.go:79-104 ResolveModelCapacity（declared > built-in > default） | 宿主有同形的优先级链：显式声明（--context-window / 会话声明）> 内置模型容量 > 未声明自定义模型的 1M 兜底，并且要求正整数（<=0 视为未声明）。 |
| `llm.piAi.maxTokensResolution` | internal/llm/capacity.go:79-104 ResolveModelCapacity（MaxOutput 分支） | 同 contextWindowResolution：maxOutput 走同一条 declared > built-in > default 解析链，且 <=0 视为未声明。 |
| `llm.piAi.providers` | internal/llm/provider_registry.go:31-42 ProviderInfo/ConfigurableProvider + :104-130 registeredProviderAPIs / 解析；投影到 cmd/dshd/serve_provider_registry.go:38 | 官方“没有 provider profile 就没有路由”；宿主有等价的 provider 注册表（adapter → (baseURL, credential, API, settingsNs)），未注册的 provider 在 listProviders/discover 都是结构化错误。 |
| `llm.piAi.reasoningEfforts` | cmd/dshd/serve_effort_override.go:128 `ReasoningEfforts json.RawMessage` / :136-142 解析校验 / :218 level 失配报错 + cmd/dshd/serve_session_api_t04.go:184 | 官方按模型声明 thinking level 字典（off 可留空，至少一个非 off）；宿主有同形的 per-model reasoningEfforts（JSON object）+ 会话级 effort 校验，level 不在当前模型字典里就报错。 |
| `llm.streaming` | llm.Client.Stream（internal/llm/client.go）+ internal/llm/provider.go:98-101 `RequiredFields() []string { return []string{"model", "messages", "stream"} }` | 官方“恒 stream、没有非流式开关”；宿主同样只有流式入口（Client.Stream + 逐事件回调），adapter 的 RequiredFields 里 stream 是必发字段，没有第二个调用形状。 |

#### context（5 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `context.compaction.retainRatio` | internal/compact/compact.go:21-27 KeepTokens 字段（默认 2000）+ :46-56 withDefaults | 宿主保留尾巴按**绝对 token 预算**（KeepTokens 默认 2000）而不是消息预算的 16%；注释里写清了为什么按体积留而不是按条数留。 |
| `context.compaction.retainTokens` | internal/compact/compact.go:21-27 KeepTokens + Options 传入（cmd/dshd/acp.go:1158 compactSelectOptions 是唯一一份参数） | 官方的绝对保留预算就是宿主的 KeepTokens（默认 2000，可由调用方覆盖），语义逐条一致。 |
| `context.compaction.thresholdRatio` | cmd/dshd/host_run_options.go:41 `defaultAutoCompact = 40000` + cmd/dshd/acp.go:1190 `if est := m.Estimate(surface); est <= r.autoCompact` | 宿主自动压缩的触发是**绝对 token 阈值**（--auto-compact，默认 40000）而不是窗口比例 0.8；能力等价（到预算就压），但口径不同——换模型窗口时不会自动跟着缩放。 |
| `context.measurement.cacheCountsAreOptional` | internal/llm/usage_cache.go:21-30（ok 语义 + 绝不写 0）+ :31-40 usageCacheReadPaths/usageCacheWritePaths | 官方“provider 没给 cacheReadTokens/cacheWriteTokens 就当 0，绝不编造”；宿主的用法完全一致：usageCacheReadPaths 返回 ok 才写键，注释明写“绝不写 0 冒充实测”。 |
| `context.sessionReference.candidateLimit` | cmd/dshd/serve_agent_api.go:58-59 `agentSessionReferenceLimit = 20` + :1779-1781 截断 | 宿主会话引用候选上限 20（官方 50），语义相同（有界候选列表）、数值更紧。 |

#### session（16 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `attachment.maxImageBytes` | cmd/dshd/attachments.go:30 `attachmentMaxBytes = 8 << 20` + :147 超限拒绝 | 宿主单张图片字节上限 8MiB（官方 50MiB），语义相同（超限即拒）、阈值更严。 |
| `fileReference.excludedDirectories` | internal/fsutil/walk.go:38-42 DefaultSkipDirs + :15-27 说明（.git / node_modules / refs 占绝大多数文件） | 宿主文件遍历有默认跳过目录集（.git / node_modules / refs 等），@ 引用与 glob 都走它；官方是“跳过的目录”参数，能力等价。 |
| `fileReference.maxResults` | cmd/dshd/serve_agent_api.go:56 `const agentReferenceLimit = 50` + :1347-1349 达上限即停 | 宿主 @ 引用的候选条数上限是 50（官方 maxResults=20），语义相同（有界列表）、数值更宽；另有一条独立的 agentSessionReferenceLimit=20 用于会话引用。 |
| `jobs.retainBytes` | modules/exec/bash/session.go:33 `maxRetainedJobOutput = 1024 * 1024` + :297-301 裁剪 | 宿主给每个运行中任务保留 1MiB 滚动窗口（官方 262144 字节），语义相同（有界保留、超出丢最旧）、数值更宽。 |
| `session.checkpoints` | internal/session/checkpoint.go:52-53 checkpointFormatVersion / :237 写 / :399-400 版本校验 + internal/agent/loop.go:283-289 PreStep 每步请求前落盘 | 官方 checkpoint-policy 的语义是“每次模型请求前与每次顶层派发前打一个耐久检查点”；宿主有独立的 durable checkpoint 侧车（checkpointFormatVersion + 版本失配即判损坏），由 loop 的 PreStep 在每步模型请求前落盘。 |
| `session.formatVersion` | codecs/v4（header.Version / 读路径 v4.ReadFile）+ internal/session/projection.go:67 `FormatVersion int` / :79 / :97 版本比对 | 官方 SESSION_FORMAT_VERSION=4 就是 v4 逻辑会话表示；宿主整个会话层就建在 codecs/v4 上（日志头 Version 字段 + 投影 FormatVersion 逐条比对），版本语义一致。 |
| `session.query.defaultLimit` | internal/session/query.go:31 `QueryDefaultLimit = 20` + :141-143 生效 | 宿主检索默认页大小 20，与官方默认逐字一致（QueryDefaultLimit）。 |
| `session.query.maxLimit` | internal/session/query.go:33 `QueryMaxLimit = 100` + :145-147 夹紧 | 宿主单页上限 100，与官方默认逐字一致（QueryMaxLimit，超出夹紧）。 |
| `session.query.openAt` | internal/session/query.go:27-34 `QueryEngineScan = "scan"` + :130 SearchSessions | 官方默认 'never'（FTS 关闭时检索报 SESSION_QUERY_SEARCH_DISABLED）；宿主没有 sqlite/FTS5，但提供了 scan 档跨会话检索（Engine 恒为 scan，诚实标注不是 fts5），比“默认关闭”更强，且不存在需要 opt-in 的开关。 |
| `session.query.snippetChars` | internal/session/query.go:344-375 excerptOf（before=80 / after=120 + 省略号） | 宿主摘录按“命中位置前 80 字符 + 后 120 字符”取（合计约 200 字符，官方 240），两侧加省略号；能力相同、分档不同。 |
| `session.title.fallbackMaxBytes` | cmd/dshd/serve_title_auto.go:164 autoTitleTruncateUTF8（官方 truncateTitleUtf8 移植）+ :80 autoTitleMaxBytes=120 | 机械标题的字节上限在宿主是 120 字节（官方 40），走同一条按字节取最长前缀且不拆码点的截断逻辑；能力相同、阈值更宽。 |
| `session.title.fallbackMaxWords` | cmd/dshd/serve_title_auto.go:192 autoTitleFromOutput（官方 titleFromOutput 移植）+ :80 `autoTitleMaxBytes = 120` | 宿主有机械标题兜底（首个非空行 + 去包裹 + 按字节截断），但词数上限不是独立参数：它由 autoTitleMaxBytes=120 字节统一钳制，非 CJK 约等于 5 个词的目标。 |
| `session.title.maxTitleBytes` | cmd/dshd/serve_title_auto.go:186-190 normalizeAutoTitle(input, maxBytes) + :80 autoTitleMaxBytes=120 | 最终落库标题的字节上限在宿主是 120 字节（官方 80），截断点同一处（normalizeAutoTitle → truncateTitleUtf8），且注释说明选择更宽一档的理由。 |
| `session.titleLlm.maxInputBytes` | cmd/dshd/serve_title_auto.go:78 `autoTitleMaxInputBytes = 4096` + :134-136 超限报错 | 宿主标题请求的 prompt 材料上限 4096 字节，与官方默认逐字一致，且超限直接报错（不是静默截断）。 |
| `session.titleLlm.targetCjkCharacters` | cmd/dshd/serve_title_auto.go:84 `autoTitleTargetCJK = 10` + :101 systemPrompt | 宿主标题 LLM 的 CJK 目标字符数 10，与官方默认逐字一致。 |
| `session.titleLlm.targetWords` | cmd/dshd/serve_title_auto.go:83 `autoTitleTargetWords = 5` + :101 systemPrompt 拼装 | 宿主标题 LLM 的 systemPrompt 里明确要求“非 CJK 约 5 个词”，与官方 targetWords=5 逐字一致。 |

#### tools-sandbox-approval（14 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `approval.failureMode` | cmd/dshd/acp.go:572 `fmt.Errorf("%w: 没有审批通道", sandbox.ErrNeedsApproval)` + modules/exec/bash/tools.go:259 / modules/tool/pwsh/pwsh.go:263 按 ErrNeedsApproval 拒绝 | 官方 fail-closed：答不出来 = unavailable，动作不执行；宿主同样在拿不到审批通道时直接报错并拒绝执行（工具不落副作用）。 |
| `permission.presets` | internal/sandbox/policy.go:124 `return nil, fmt.Errorf("%w: %q（可选：read-only, workspace-write, danger-full-access）", ErrUnknownMode, cfg.Mode)` + internal/modes/modes.go:26 | 官方 schema 两档、随附 base bundle 三档（含 read-only）；宿主同样实现三档沙箱（read-only / workspace-write / danger-full-access）并在装配期做枚举校验。 |
| `skillFs.customSkillDirs` | modules/tool/skill/skill.go:398-403 `for _, p := range strings.Split(os.Getenv("DSHD_SKILLS_EXTRA"), ";")` | 官方允许追加自定义 skill 目录（默认空）；宿主用环境变量 DSHD_SKILLS_EXTRA（分号分隔）追加额外根，能力等价、入口是环境变量而不是配置文件。 |
| `skillFs.includeDefaultRoots` | modules/tool/skill/skill.go:393-405 roots()（先 t.dir，再 DSHD_SKILLS_EXTRA） | 官方默认包含内置 skill 根；宿主的 roots() 默认就带主 skill 目录（t.dir，由装配期给出），再叠加额外根，行为一致。 |
| `terminal.maxBufferedBytes` | cmd/dshd/serve_terminal_api.go:66 `officialTerminalMaxScreenBytes = 256 * 1024` + cmd/dshd/terminal_pty.go:359 `MaxBufferedBytes: officialTerminalMaxScreenBytes` | 宿主每终端输出缓冲 256KiB（官方 2MiB），语义相同（有界环形保留）、阈值更小；env.MaxBufferedBytes 直接回该常量。 |
| `terminal.maxCols` | cmd/dshd/serve_terminal_api.go:68 `officialTerminalMaxCols = 500` + :1340-1342 截断 | 宿主 create 接受的最大列数 500，与官方默认逐字一致（超出即截断，不报错）。 |
| `terminal.maxInputBytes` | cmd/dshd/serve_terminal_api.go:53 `officialTerminalMaxInputBytes = 64 * 1024` + :989-992 超限报错 + :64 officialTerminalInputChunkBytes=32 分片 | 宿主单次 Input 上限 64KiB，与官方 65536 逐字一致（超限报 bad param）；另有一层 32 字节的 ConPTY 分片写。 |
| `terminal.maxRows` | cmd/dshd/serve_terminal_api.go:69 `officialTerminalMaxRows = 200` + :1343-1345 截断 | 宿主 create 接受的最大行数 200，与官方默认逐字一致。 |
| `terminal.shellCandidates` | cmd/dshd/serve_terminal_api.go:1293 `func discoverOfficialShells()` + :1309 `candidates = []string{"sh", "bash", "zsh", "dash", "fish"}` + cmd/dshd/terminal_pty.go:367-372 TerminalShells | 官方给一串候选 shell（zsh/bash/fish/pwsh/powershell/cmd）按序探测；宿主是真实探测（discoverOfficialShells，默认 shell 第一，其余按可执行名去重），能力等价且更贴近本机实际。 |
| `tool.fs.applyPatch` | modules/tool/read/read.go:1、modules/tool/write/write.go:1、modules/tool/edit/edit.go:1（只有 read/write/edit 三个 fs 工具，无 apply_patch） | 官方 base bundle 把 apply_patch 摘掉（默认 false），只留 edit；宿主同样不提供 apply_patch 工具（只有 read/write/edit），默认行为一致。 |
| `tool.repeatReminder.argumentsPreviewChars` | modules/tools/repeat_reminder.go:46 `repeatArgumentsPreviewChars = 500` + :98 装配 | 宿主参数预览 500 字符，与官方默认逐字一致。 |
| `tool.repeatReminder.thresholds` | modules/tools/repeat_reminder.go:53 `func repeatThresholds() []int { return []int{3, 5, 8} }` + :97 装配 | 宿主重复调用提醒的阈值就是 [3,5,8]，与官方默认逐字一致，且第一个阈值给 gentle 文案。 |
| `tool.todo.allowParallelInProgress` | modules/tool/todo/todo.go:122-136（统计 in_progress，>1 且 >3 条只给 Hint，不拒绝） | 官方允许同时多个 in_progress（默认 true）；宿主的 todo 工具同样不禁止（只在“超过 1 项且清单 >3 条”时给一句提示），默认行为一致。 |
| `tools.executorMode` | internal/spi/tool.go:17 `type Tool interface`（Name/Schema/Caps/Exec）+ internal/kernel 的注册与派发链；无 PTC 执行器 | 官方默认 native（PTC 执行器挂载但在 base 里禁用）；宿主只有 native 执行器（Go 进程内派发），与官方默认一致——PTC 那套 JS 执行器按 R6 明确不嵌。 |

#### transport-http（12 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `gateway.maxMissedHeartbeats` | internal/officialwire/mux.go:140 missed 计数 + :508-512 阈值 2 | 宿主连续 2 次心跳未到即判定对端死亡，与官方默认 2 一致（硬编码在 mux 的读写循环里）。 |
| `gateway.streamInboxBytes` | internal/officialwire/mux.go:40 UplinkBytes 256KiB + internal/officialwire/codes.go:25 CodeUplinkOverflow + internal/officialwire/mux.go:373 触发点 | 宿主每条流的上行配额 256 KiB，与官方 262144 逐字一致，溢出映射成 gateway/uplink-overflow（不关 socket）。 |
| `gateway.websocketCompression` | internal/transport/ws.go:29 wsDeflateOfferResponse + :110 negotiateDeflate | 宿主 WS 协商 RFC7692 permessage-deflate（双向 no_context_takeover），与官方默认 deflate 一致；没有可接受的 deflate 时明确 400 而不是静默降级。 |
| `gateway.websocketHeartbeatIntervalMs` | internal/officialwire/mux.go:20 `DefaultHeartbeat = 2 * time.Second` + :59-61 兜底 | 宿主官方 wire 的服务端 ping 间隔就是 2s，与官方默认逐字一致（DefaultHeartbeat）。 |
| `jobController.observeFlushMs` | cmd/dshd/serve_terminal_api.go:87 `officialFollowPollInterval = 100 * time.Millisecond` | 宿主的 job.follow 帧机就是“观察任务输出”的通道，兜底采样间隔 100ms，与官方 observeFlushMs=100 同值；实现是轮询采样（不是定时 flush），但对调用方语义一致。 |
| `jobController.observeMaxFrameBytes` | cmd/dshd/serve_terminal_api.go:71 `officialFollowPageBytes = 64 * 1024` | 宿主 job.follow 单帧最多 64KiB，与官方 65536 逐字一致。 |
| `webApp.defaultHost` | internal/transport/server.go:34 defaultAddr + cmd/dshd/serve.go:185 --addr 默认空 → defaultAddr | 官方 web bundle 默认绑 127.0.0.1；宿主的默认监听地址就是 127.0.0.1:0（环回 + OS 分配端口），默认主机语义一致。 |
| `webApp.defaultPort` | internal/transport/server.go:34 defaultAddr（端口 0）+ :284 回报真实地址；cmd/dshd-bridge/main.go:551 的 3080 默认 | 官方 web bundle 默认 3080；宿主默认端口是 0（OS 分配，启动后把真实地址回报给调用方），TUI 桥另有 3080 默认。能力等价（可指定端口）、默认值不同。 |
| `webserver.host` | internal/transport/server.go:34 `defaultAddr = "127.0.0.1:0"` + :255-260 兜底 + cmd/dshd/serve.go:185 --addr 旗标 | 宿主 HTTP 监听地址由 --addr 给，默认 127.0.0.1:0（环回 + OS 分配端口），与官方“默认环回、保持在本机”一致；差别是宿主没有显式拒绝通配地址（0.0.0.0 仍可绑）。 |
| `workspaceFiles.maxEntries` | cmd/dshd/serve_workspace_api.go:63 officialWorkspaceEntryCap = 512 + :71 officialDirectoryEntryCap | 宿主每层最多 512 项（官方 2000），语义相同（目录列举条数上限）、数值更紧。 |
| `workspaceFiles.maxFileBytes` | cmd/dshd/serve_workspace_api.go:68 `officialWorkspaceMaxBytes = 4 << 20` + :1414 生效 | 宿主 workspaceFiles.read 的单次字节上限 4MiB（官方 32MiB），语义相同（可打开文件大小上限）、阈值更严。 |
| `workspaceFiles.maxLines` | cmd/dshd/serve_workspace_api.go:66 `officialWorkspaceMaxLines = 2000` + :1407 | 宿主一页最多 2000 行（官方 5000），语义相同（limit 不得超上限）、数值不同。 |

#### tui-capability（11 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `approval.displayShape` | cmd/dshd/approval_view.go:9-40 describeApproval + cmd/dshd/serve_approval.go 的 outcome/policy 字段 | 官方审批提示展示 outcome / policy / reason / displayReason；宿主的 describeApproval 生成“命令原文 + 模型自述（明确标注不可当依据）”的展示串，并有 outcome（allowed/rejected/cancelled）、policy（ask/never）与拒绝原因字段。 |
| `models.catalogEntry` | transport.OfficialDiscoveredModel 投影（cmd/dshd/serve_account_api.go:347-350 / :362-373）+ internal/llm/model_catalog.go:864-870 | 官方模型目录项 id / name / description? / contextWindow? / maxTokens? / inputModalities?；宿主 discoverModels 与 models.list 的投影逐字段对应（description 由 name 兜底）。 |
| `models.selection` | cmd/dshd/main.go:1019-1022 选择三元组 + cmd/dshd/model_steer.go:221-232 provider 解析 | 官方每会话可变的模型选择是 provider / model / reasoningEffort 三元组；宿主会话头里存 model + reasoningEffort，provider 由模型解析（resolveServeProvider），三者在一次选择里都可变。 |
| `modes.agentPresets.selectedDefault` | cmd/dshd/acp.go:761-766 会话头 agentPreset 恢复 + cmd/dshd/serve_agent_api.go:156-157 preset 读写 API | 官方允许运行时覆盖默认 agent preset；宿主把 preset 记在会话头（header.agentPreset）并在恢复时优先用它，等价于“每个会话有自己的当前 preset”。 |
| `modes.planMode` | modules/tool/plan/plan.go:5 包注释 / :190 Hint / :211 `func (t *exitTool) Name() string { return "exit_plan_mode" }` | 官方 plan mode 是挂载插件 + 模型可见 section，只能经 exit_plan_mode 或切模式结束；宿主有完整的 plan 工具（plan / exit_plan_mode，Result.Stop 让本轮停在方案上）。 |
| `queue.updateResult` | internal/transport/handlers_session_official.go:312 Accepted 字段 + cmd/dshd-bridge/main.go:418 | 官方队列变更只回 {accepted:true}；宿主的队列更新结果同样只有 Accepted 字段，没有额外负载。 |
| `session.pendingInput` | internal/transport/handlers_session_official.go:759 officialPendingInput + :258 PendingInput 字段 | 官方 pendingInput 词表 approval / question / other；宿主把真实挂起的审批帧翻成同一套官方词表（officialPendingInput），并参与 session 视图。 |
| `usage.fields.projectionShape` | cmd/dshd/assistant_usage.go:49-61 四个桶写入口 + internal/transport/api.go:363-368 UsageMsg 的 v4 键名 | 官方投影四键（uncachedInputTokens / outputTokens / cacheReadTokens / cacheWriteTokens）；宿主的 usage 对象是 {inputTokens(=未命中), outputTokens, totalTokens, cacheReadTokens?, cacheWriteTokens?}，只差键名（uncachedInputTokens 在 v4 线里叫 inputTokens），四个语义全在。 |
| `usage.fields.promptTokens` | cmd/dshd/assistant_usage.go:35-61 assistantUsageFrom（inputTokens = 未命中，另写 cacheReadTokens/cacheWriteTokens） | 官方定义 promptTokens = inputTokens + cacheReadTokens + cacheWriteTokens（未命中 + 命中 + 写入）；宿主在 assistantUsageFrom 里把 inputTokens 写成“未命中部分”（prompt_tokens 减掉命中/写入），并写出 cacheRead/cacheWrite 两个键，三者相加即官方口径。 |
| `usage.fields.streamShape` | internal/transport/api.go:363-368 UsageMsg + cmd/dshd/assistant_usage.go:49-61 + internal/llm/provider.go:110-115 | 官方流式形状 inputTokens / outputTokens / totalTokens? / cacheReadTokens? / cacheWriteTokens? / reasoningTokens?；宿主四个必填+两个可选全有（reasoningTokens 只在 adapter 的 UsageExtensions 里收集，未投影进 usage 键——属可选键缺席）。 |
| `workspace.documentsDirectory` | cmd/dshd/serve_workspace_api_documents_windows.go:19 officialDocumentsDirectory + serve_workspace_api.go:150 注入 | 官方把用户 Documents 根放进工作区视图；宿主实现了 officialDocumentsDirectory（Windows 已知文件夹查询 + 其它平台兜底）并注入 workspace 视图。 |

#### env（10 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `DEEPSEEK_API_KEY` | modules/search/deepseek.go:77 DEEPSEEK_API_KEY / DSHD_SEARCH_API_KEY + LLM 侧 DSHD_LLM_KEY | 官方按请求从凭据库解析该 key（llm-deepseek / web-search-deepseek / web-deepseek-api 共用）；宿主在搜索侧直接读 DEEPSEEK_API_KEY（或 DSHD_SEARCH_API_KEY），LLM 侧用 DSHD_LLM_KEY，能力等价、变量名不同。 |
| `DSH_CONTEXT_WINDOW` | cmd/dshd/host_run_options.go:58 --context-window + internal/llm/capacity.go:79-104 ResolveModelCapacity | 官方 DSH_CONTEXT_WINDOW 只作 sdk-minimal 的兜底容量；宿主把容量做成 --context-window 旗标（0 = 按模型解析），优先级形状一致（显式声明 > 目录/内置 > 兜底），但没有同名环境变量。 |
| `DSH_HOME` | **不实现**（`N-A`，`naClass=naming-conflict`）：home 根 = `cmd/dshd/main.go:292-296 defaultHome`（`--home` > `DSHD_HOME` > `~/.dshd`） | **官方名 `DSH_HOME` 在宿主不实现 —— 命名冲突（见 `docs/go5-env-notes.md` §4）**：官方的 `DSH_HOME` 是**整个 DSH 的数据根**（`显式路径 > $DSH_HOME > ~/.dsh`）；**宿主 home 用 `DSHD_HOME`**，而 TUI2GO 的 `run-019.cmd` 里 **`DSH_HOME` 是桌面 profile home**（`%PLAY%\home`）。两个语义撞车，简单别名会让宿主会话库被指到桌面 profile home。`DSH_HOME` 在宿主的既有读取只有 3 处删除保护/历史守卫（`cmd/dshd/acp.go:1328`、`cmd/dshd/main.go:1397`、`cmd/dsh-relayd/main.go:92`），维持不变。 |
| `DSH_MAX_TOKENS_AS_SUCCESS` | `internal/agent/finish_policy.go:34 MaxTokensAsSuccess` + `internal/agent/loop.go:349` 闸门 | **官方名 `DSH_MAX_TOKENS_AS_SUCCESS` 在宿主已真读（GO.5/T1）**：只把 `false`/`0`/`no`（大小写不敏感）当 false，其余一律按官方默认 true。true（默认）：finish=length 照常收口，v4 记 `turn/end{kind:"max-tokens"}`；false：同一响应在**任何副作用之前**判失败，v4 记 `turn/end{kind:"error"}` 且 error.message 点名该变量。 |
| `DSH_PERMISSION_MODE` | cmd/dshd/acp.go:92 sandboxCeiling（DSHD_SANDBOX_MODE）+ :129 上限语义 | 官方 DSH_PERMISSION_MODE 设 sandbox-policy.mode（danger-full-access 时审批变 never）；宿主用 DSHD_SANDBOX_MODE 作进程上限，会话档位不得比它更宽，且 danger-full-access 有独立审批语义。 |
| `DSH_PRODUCT_ANALYTICS_OTLP_URL` | `internal/analytics/env.go:58 CollectorURLFromEnv` → `internal/analytics/policy.go:56 CollectorURL` | **官方名 `DSH_PRODUCT_ANALYTICS_OTLP_URL` 在宿主已真接（GO.5/T1）**：与 `DSH_TELEMETRY_OTLP_URL` 同一条解析器，且作为**更具体的名字优先**（`DSHD_PRODUCT_ANALYTICS_OTLP_URL` > `DSH_PRODUCT_ANALYTICS_OTLP_URL` > `DSHD_TELEMETRY_OTLP_URL` > `DSH_TELEMETRY_OTLP_URL`）。优先级：**显式配置 > env > 默认（空 = 本地零出网）**；env 只改端点不改 mode，非法 URL fail-loud。 |
| `DSH_TELEMETRY_OTLP_URL` | `internal/analytics/env.go:58 CollectorURLFromEnv` → `internal/analytics/policy.go:56 CollectorURL` | **官方名 `DSH_TELEMETRY_OTLP_URL` 在宿主已真接（GO.5/T1）**：解析进 `Policy.CollectorURL`（只允许 http/https，`mode=local` 时即使有值也不导出）。优先级：**显式配置（policy.json 非空 `collectorUrl`）> env > 默认（空 = 本地零出网）**；读到 env **不会**把 mode 抬成 `local+export`（默认仍一个字节都不外发），非法 URL 在 `analytics.Open` fail-loud 并点名变量。与官方的差异只剩默认值。 |
| `DSH_WEB_FETCH_PROVIDER` | internal/spi/search.go:388 双前缀解析（喂同一字段；都设时 DSHD_* 先命中） | **官方名 `DSH_WEB_FETCH_PROVIDER` 在宿主也生效**：与搜索同一条解析器，双前缀都认（都设时 `DSHD_WEB_FETCH_PROVIDER` 先命中，官方名兜底），照官方文档设能生效。 |
| `DSH_WEB_SEARCH_PROVIDER` | internal/spi/search.go:375 双前缀解析（喂同一字段；都设时 DSHD_* 先命中） | **官方名 `DSH_WEB_SEARCH_PROVIDER` 在宿主也生效**：解析器同时认两个前缀（`DSHD_WEB_SEARCH_PROVIDER` 与官方 `DSH_WEB_SEARCH_PROVIDER` 喂同一个字段；都设时按解析顺序以 `DSHD_*` 为准），语义与官方"等价于 searchProvider 配置值"一致——照官方文档设能生效。 |
| `llm.contextWindow.precedence` | internal/llm/capacity.go:79-104 ResolveModelCapacity + cmd/dshd/transport_source.go:388 调用点 | 官方优先级“模型目录条目 > adapter 默认（llm-deepseek 1e6）> DSH_CONTEXT_WINDOW（仅 sdk-minimal）”；宿主的 ResolveModelCapacity 就是“显式声明 > 内置模型容量 > 未声明自定义模型 1M 兜底”，单点解析、不缓存。 |

#### live-profile-desktop（7 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `llmPiAi.providers.magpie.api` | internal/llm/provider.go:65 openAICompletionsAdapter.API() + :113-130 adapterRegistry | 桌面 profile 给 magpie 渠道配 api=openai-completions；宿主的 provider 适配器表里有同名的 openai-completions 适配器（请求字段面由 adapter 声明）。 |
| `llmPiAi.providers.magpie.models[0].id` | cmd/dshd/main.go:601 `envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017")` | 桌面 profile 的默认模型就是 group/deepseek-v4.1-flash-1017；宿主的 --model 默认值逐字相同。 |
| `llmPiAi.providers.magpie.models[0].reasoningEfforts` | cmd/dshd/serve_effort_override.go:128 ReasoningEfforts 字段 + :218 字典校验 | 桌面 profile 给该模型声明 reasoningEfforts 字典（xhigh/max）；宿主的模型目录同样存 reasoningEfforts 对象，并校验会话 effort 必须命中该字典。 |
| `permission.presets` | internal/sandbox/policy.go:124 三档 preset 校验 + cmd/dshd/serve_sandbox_policy.go:341 | 桌面 profile 定义三档 permission preset（read-only / workspace-write / danger-full-access，各带 sandbox + approval）；宿主同样实现这三档并在装配期校验枚举。 |
| `terminal.shell` | cmd/dshd/serve_terminal_api.go:1293 discoverOfficialShells + :1286 按名解析 | 桌面 profile 固定终端 shell（Git Bash 的 path/name/args）；宿主有等价的 shell 探测与指定能力（terminal.shellCandidates + TerminalShells）。 |
| `transcriptView` | cmd/dshtui/app.go:77 `showToolResults bool`（默认 false = 折叠 = 官方 compact） | 官方桌面壳的 transcript 呈现档位（默认 compact = 工具结果折叠）。**宿主 TUI 就是这个默认**：工具结果行默认折叠，Ctrl+T 展开（showToolResults 默认 false）；官方 compact 的语义被完整实现，只是没有"档位"这个词——开关是同一个。 |
| `uiTheme.preference` | cmd/dshtui/theme.go:108 `var themeName = "dark"` + :115 覆盖优先级注释 | 官方桌面壳的主题偏好（默认 dark）。**宿主有 Go TUI，主题是真实能力**：themeName 默认就是 dark，覆盖优先级 `--theme` > `DSH_TUI_THEME` > `COLORFGBG` > dark，与官方同值同语义；区别是现代码在 cmd/dshtui 而不是桌面壳窗口。 |

#### schedule-plugins-account-telemetry（12 条）

| 官方 key | 宿主等价物（file:line） | 差异要点 |
|---|---|---|
| `account.allowLoopbackHttp` | internal/webfrontend/webfrontend.go:42（只监听 loopback 明文）+ internal/transport/server.go:34 defaultAddr | 官方开关"允许 loopback 的明文 http origin"（默认 false）；宿主本来就只监听 loopback 明文、没有 TLS 分支，等价于该开关恒开。 |
| `account.platformOrigin` | internal/account/account.go:180 PlatformOrigin + cmd/dshd/serve_account_provider.go:46 DSHD_ACCOUNT_PLATFORM_ORIGIN | 官方 platform origin 默认 https://platform.deepseek.com；宿主有同一语义的 PlatformOrigin（可由 DSHD_ACCOUNT_PLATFORM_ORIGIN 覆盖），并在 account client 里做 trimRight 归一。 |
| `hmr.debounce` | internal/pluginmgr/hmr.go:127-129 `cfg.Interval = time.Second`（去抖间隔） | 官方给 100ms 去抖；宿主的 HMR 有同语义的去抖间隔（Interval，默认 1s，可由 DSHD_PLUGIN_HMR 配置驱动），能力等价、默认更保守。 |
| `schedule.deliveryHistoryDays` | cmd/dshd/serve_schedule_api.go:65 `officialScheduleHistoryMaxDays = 30` | 官方给投递历史 30 天保留期；宿主日程服务有同名同值的保留天数常量（officialScheduleHistoryMaxDays = 30），超期记录在 deleteInactive 时清。 |
| `schedule.deliveryHistoryRecords` | cmd/dshd/serve_schedule_api.go:63 `officialScheduleHistoryMaxRecords = 100`（官方 200） | 官方给投递历史 200 条上限；宿主有同语义的条数上限，取 100（更紧），同样在裁剪时生效。 |
| `schedule.listLimit` | internal/transport/handlers_schedule_official.go:94 `scheduleHistoryLimitMax = 100` | 官方给日程列表默认页大小 100；宿主 history 的 limit 上限就是 100（默认即上限），语义一致。 |
| `schedule.maxListLimit` | internal/transport/handlers_schedule_official.go:94 `scheduleHistoryLimitMax = 100`（官方 500） | 官方列表页上限 500；宿主把上限压到 100（更紧），超出即夹紧——能力等价、阈值更严。 |
| `schedule.maxTitleLength` | internal/transport/handlers_schedule_official.go:91 `scheduleTitleMaxRunes = 120` | 官方标题上限 120 字符；宿主同一个 120 的 rune 上限，在 create 与 patch 两处都校验。 |
| `schedule.newSessionIntervalSeconds` | cmd/dshd/serve_schedule_api.go:1137 + internal/transport/handlers_schedule_official.go:98（60s） | 官方给"新会话投递最小间隔"60s；宿主由入口强制同一常量（MinScheduleEverySeconds = 60），并在 schedule.deliveryLimits 里回给客户端。 |
| `systemPrompt.includeHarnessIdentity` | cmd/dshd/acp.go:1099 buildSystemPromptWithHome（身份段恒含） | 官方开关注入"harness 身份"段（默认 true）；宿主的系统提示恒由 buildSystemPromptWithHome 拼装（工具说明 + AGENTS.md + home 规则），身份段恒在，只是没有开关。 |
| `telemetry.host.endpoint` | internal/analytics/policy.go:52 CollectorURL（与 session 遥测同一端点配置） | 官方 host 遥测端点；宿主同一个 CollectorURL（product analytics 走 local+export 档），语义一致。 |
| `telemetry.session.exporter.url` | internal/analytics/policy.go:52 / :95 CollectorURL（http/https 校验） | 官方 session 遥测导出到 OTel collector 端点；宿主的 analytics 策略有同语义的 CollectorURL（只允许 http/https，local 档不导出），端点可配。 |

---

### N-A（31 条，按子类分组 —— 每条都有 `naClass`，`naBreakdownUntriaged` 为空）

| 子类 | 条数 | 含义 |
|---|---|---|
| `desktop-shell` | 2 | desktop-shell（窗口/主题/托盘/快捷键/启动器/自动更新） |
| `ui-only` | 7 | ui-only（文案/布局/渲染档位/可见性开关） |
| `telemetry-ui` | 4 | telemetry-ui（产品遥测/会话上传/匿名 id 的展示面） |
| `plugin-market-ui` | 3 | plugin-market-ui（插件市场的客户端 UI） |
| `protocol-shape` | 14 | protocol-shape（纯 wire 形状/镜像常量/实现形状不适用） |
| `naming-conflict` | 1 | naming-conflict（官方 env 名与本地同名变量语义撞车，显式不做别名） |
| **合计** | **31** | |

#### desktop-shell（窗口/主题/托盘/快捷键/启动器/自动更新） —— 2 条

| 官方 key | 域 | 为什么对无头宿主不适用 |
|---|---|---|
| `experimentalInspector.disabled` | `live-profile-desktop` | 桌面壳的实验性 inspector（开发者工具面板）开关；无头宿主没有可检查的渲染进程。 |
| `sessionInspector.disabled` | `live-profile-desktop` | 桌面壳的会话 inspector 面板开关（查看某会话的原始事件）；无头宿主没有该面板——原始 v4 日志本来就是可读文件。 |

#### ui-only（文案/布局/渲染档位/可见性开关） —— 7 条

| 官方 key | 域 | 为什么对无头宿主不适用 |
|---|---|---|
| `performanceUsage` | `live-profile-desktop` | 桌面 profile 的状态栏计费粒度档位（detailed 等）；无头宿主不渲染状态栏。 |
| `trace.maxResultsPerSession` | `tui-capability` | 官方 trajectory（轨迹）视图每会话保留 1000 行，是客户端渲染的调试视图；宿主没有 trajectory 视图/接口（grep 0 命中），该上限无对象。 |
| `usage.display.foldedReplies` | `tui-capability` | “完成的一轮折叠后保留哪些 assistant 回复可见”（默认 all）是纯客户端展示策略，宿主不渲染会话气泡。 |
| `usage.display.performanceUsage` | `tui-capability` | 这是客户端“状态栏/详情区计费粒度”的渲染档位（detailed 等），宿主是无头服务，不渲染状态栏；TUI 侧另线（tui-line）自己决定档位。 |
| `usage.display.transcriptView` | `tui-capability` | work-details 呈现模式（detailed/折叠等）是客户端渲染选择，无头宿主不渲染 transcript。 |
| `usage.fields.cacheHitPercent` | `tui-capability` | 这是 TUI 里 formatCacheHitPercent() 的**格式化函数**（四舍五入、promptTokens=0 时返回 null）；宿主只写数字键（inputTokens/cacheReadTokens/cacheWriteTokens），百分比由客户端渲染，属 ui-only。 |
| `usage.placeholderPolicy` | `tui-capability` | 官方规定“百分比为 null 表示还没有 prompt 输入，绝不渲染成 0.0”；这是客户端格式化策略，宿主只保证“没实测就不写键”，渲染判定在 TUI。 |

#### telemetry-ui（产品遥测/会话上传/匿名 id 的展示面） —— 4 条

| 官方 key | 域 | 为什么对无头宿主不适用 |
|---|---|---|
| `DSH_CLIENT_VERSION` | `env` | 官方把 service/app 版本交给宿主产品遥测；宿主的产品遥测默认零外连且没有“客户端版本”上报字段，该版本上报面不适用。 |
| `DSH_TELEMETRY_DISABLED` | `env` | 官方的语义是“launchers 把遥测那一行 patch 成 disabled，配置值无法关掉一行”，属于官方启动器/设置 UI 的行级开关；宿主没有遥测行（默认零外连），该行级开关不适用。 |
| `DSH_TELEMETRY_MODE` | `env` | 官方遥测策略是 FEEDBACK_ONLY（会话分享/反馈级别的产品遥测）；宿主的 analytics 策略只有 local（零外连，默认）与 local+export 两档，没有“会话分享反馈”这套产品遥测语义，该模式值无对应物。 |
| `sessionLogDeepseek.mounted` | `schedule-plugins-account-telemetry` | 官方默认挂载 `session-log-deepseek`：把会话日志上传到 DeepSeek 用于训练/改进。无头宿主按 W0 裁定默认零外连、不做会话上传，该插件没有对应物。 |

#### plugin-market-ui（插件市场的客户端 UI） —— 3 条

| 官方 key | 域 | 为什么对无头宿主不适用 |
|---|---|---|
| `clientUiPluginManager.registryProbeCacheTtlMs` | `schedule-plugins-account-telemetry` | 官方给探测结果 300000ms 缓存；宿主没有市场页面缓存这一层。（宿主侧唯一相关的是 pluginManager 的服务端 registry 通道，已在 pluginManager.* 里登记为 UNREAD-GAP。） |
| `clientUiPluginManager.registryProbeEnabled` | `schedule-plugins-account-telemetry` | 官方默认开：客户端 UI 会主动探测插件 registry（拿市场列表/更新提示）；宿主没有渲染层也没有市场页面，该探测不适用。（宿主侧唯一相关的是 pluginManager 的服务端 registry 通道，已在 pluginManager.* 里登记为 UNREAD-GAP。） |
| `clientUiPluginManager.registryProbeTimeoutMs` | `schedule-plugins-account-telemetry` | 官方给 registry 探测 1500ms 预算；宿主没有客户端 market 页面，也就没有这次探测。（宿主侧唯一相关的是 pluginManager 的服务端 registry 通道，已在 pluginManager.* 里登记为 UNREAD-GAP。） |

#### protocol-shape（纯 wire 形状/镜像常量/实现形状不适用） —— 14 条

| 官方 key | 域 | 为什么对无头宿主不适用 |
|---|---|---|
| `DSH_PRIMARY_RUNTIME` | `env` | 官方标注该变量只被 dsh-sdk-app 读（外部主运行时根）；宿主没有 sdk-app 包这条装配路径，也没有“外部主运行时根”的绑定面，不适用。 |
| `agentLoop.maxParallelToolCallsConstant` | `agent-loop` | 这不是独立可配参数，只是官方把 schema 默认值再导出一份常量（lib/index.js:1101 对 1309 的镜像）。宿主没有并行工具执行，故没有对应常量；能力缺口的实体登记在 agentLoop.maxParallelToolCalls，此处不重复计一次缺口。 |
| `agentLoop.stepsPerTurn` | `agent-loop` | 官方自己在 whereApplied 里写明 absent — no such parameter，README 原文 No built-in turn budget；宿主侧对应物不是该参数，而是宿主自加的安全阀 --max-steps（默认 2000），两者不是同一个东西，不能算等价覆盖。 |
| `jobs.pumpPollMs` | `session` | 这是“轮询拉取任务输出”的间隔；宿主的任务输出是事件驱动推送（close-and-replace 广播 + 条件变量），没有轮询泵。job.follow 那一层有 100ms 兜底轮询，但那是官方帧机的采样间隔，不是这个参数。 |
| `llm.modelDiscovery.namespaceRequired` | `llm` | 这是官方插件注册面的形状要求（registerModelDiscovery 在 settings namespace 为空时抛 INVALID_DISCOVERY）；宿主没有“插件注册 discovery”这一层（R2 明确禁止 init 注册，provider 在装配期显式注册），故该 namespace 约束不适用。 |
| `session.journalEpoch` | `session` | 官方传输里没有 journalEpoch 字段（官方 whereApplied 明写 UNKNOWN / no journalEpoch field to mirror）；宿主额外引入了 journalEpoch 作为重启后水位失效的判据，属于宿主自加扩展，不是“官方参数未读”。 |
| `session.query.journalMode` | `session` | 这是 SQLite 的 WAL journal 模式；宿主没有 sqlite（检索走内存扫描 + 文件读），该引擎级参数不适用。 |
| `session.query.persistedReadConcurrency` | `session` | 这是 sqlite 侧“持久化 inspect 的并发度”参数；宿主没有 sqlite inspect 通道（检索是进程内扫 v4 日志），该并发参数不适用。 |
| `sessionController.nativeOpen` | `transport-http` | 官方默认 None（= 不做 native open 集成），是“用系统程序打开文件”的桌面集成开关；无头宿主没有桌面壳，且默认就是关闭，不构成缺口。 |
| `tool.fsSearch.graceMs` | `agent-loop` | 这是“终止 rg 子进程前的宽限”参数；宿主 glob/grep 是进程内纯 Go 实现（不 spawn rg），没有子进程可终止，因此该参数在宿主实现形状下不适用。 |
| `tool.fsSearch.rawOutputMaxBytes` | `agent-loop` | 这是 rg 子进程 stdout 的原始字节上限（20MB，超了报 SEARCH_RAW_OUTPUT_OVERFLOW）；宿主没有 rg 子进程与 raw stdout，改用“单文件读取上限 2MB”的进程内边界，形状不同不可等价。 |
| `tool.fsSearch.searchMetaMaxBytes` | `agent-loop` | 官方给“序列化后的 presentationMeta”设 64KB 上限，这是官方检索工具的结果投影形状；宿主结果结构是 spi.Result{Text,Data,Hint,Artifacts}，没有 presentationMeta 这一层，故不适用。 |
| `tool.fsSearch.stderrMaxBytes` | `agent-loop` | rg 子进程 stderr 的尾部预算；宿主进程内实现没有子进程 stderr 通道，不适用。 |
| `tools.nativeExecutorConfig` | `tools-sandbox-approval` | 官方明确 native 执行器不接受任何配置（默认 {}，whereApplied='the native executor takes no configuration'）；宿主 native 执行器同样没有配置面，这个空对象没有对应项可读，不是缺口。 |

#### naming-conflict（官方 env 名与本地同名变量语义撞车） —— 1 条

| 官方 key | 域 | 为什么不做（不是缺口，是撞车） |
|---|---|---|
| `DSH_HOME` | `env` | 官方 `DSH_HOME` 是**整个 DSH 的数据根**（`显式路径 > $DSH_HOME > ~/.dsh`）；宿主 home 根走 **`DSHD_HOME`**（`--home` > `DSHD_HOME` > `~/.dshd`），而 TUI2GO 的 `run-019.cmd` 里 **`DSH_HOME` 是桌面 profile home**（`%PLAY%\home`）。两个语义撞车：一旦宿主把 `DSH_HOME` 当 home 根，launcher 会把宿主会话库指到桌面 profile home（会话/插件/策略互相污染），且官方与宿主的覆盖顺序本来就不同（`显式路径 > $DSH_HOME > ~/.dsh` vs `--home > $DSHD_HOME > ~/.dshd`）。结论：**宿主 home 用 `DSHD_HOME`；TUI2GO 里的 `DSH_HOME` 是桌面 profile home**，不做别名。详见 `docs/go5-env-notes.md` §4。 |

---

### UNREAD-GAP（113 条，真能力缺口 —— 可直接拿去派单）

> 排序：域 → key。每条含「缺什么 / 现在降级成什么样 / 替代路径」。同一族的条目在 `gap` 里互相点名（例如 pluginManager.* 全部指向"没有 registry 客户端"）。

#### agent-loop（12 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 1 | `agentLoop.agents[].id` | 缺调用方指定/复用的稳定 agent id。降级：每次委派都得到一个新 child 会话 id，跨轮无法按名字指向同一子代理。替代路径：continuable=true 后用回填的 childSessionId 续问（subagents.prompt）。 |
| 2 | `agentLoop.maxParallelToolCalls` | 缺并行工具执行（N 路并发 + 结果按 call 顺序回填）。降级：串行，N 个工具调用的墙钟 = 各自耗时之和；多工具步（并行读文件/多路搜）明显变慢。替代路径：modules/tool/workflow 可把多步写成 JSON 步骤表一次往返，但仍是串行。 |
| 3 | `bash.graceMs` | 缺“优雅终止 → 宽限 → 强杀”的升级链。降级：Stop/Kill 立刻终止整棵进程树，子进程来不及收尾（写盘、清理临时文件、上报退出码）。替代路径：命令自己 trap SIGTERM 也没用（Job Object 直接 Terminate）。 |
| 4 | `retry.pluginConfig` | 缺每 provider 独立的重试配置（次数/退避/可重试码）。降级：全进程一套硬编码默认值，任何 provider 想放宽/收紧都必须改代码重编译。替代路径：无。 |
| 5 | `steering.cancelKeepInboxOption` | 缺“取消 turn 但保留队列”的开关。降级：取消 = 丢弃所有 pending（用户中途插话若在取消瞬间会被静默丢弃）。替代路径：无；需要保留只能重发。 |
| 6 | `subagent.maxActiveSubagents` | 缺并发子代理与活跃数上限。降级：完全串行，多路委派的墙钟线性叠加；同时没有上限保护（越深越慢而不是越多）。替代路径：无。 |
| 7 | `tool.web.searchSummaryMaxChars` | 缺 provider 摘要的字符上限。降级：摘要原样进结果，长摘要由外层 tools.Pipeline 的 4000 字符 spill 兜底（比官方 800 宽 5 倍）。替代路径：外层 spill 会外置全文并留指针。 |
| 8 | `tool.web.searchTimeoutMs` | 缺 web_search 自己的超时预算。降级：依赖调用方 ctx / 工具框架超时，provider 挂住时会一直等到外层超时。替代路径：无（webfetch 侧有 30s 默认，可参照移植）。 |
| 9 | `workflow.maxConcurrentAgents` | 缺 PTC 并发 agent 池（含 0=unbounded 语义）。降级：workflow 只能串行跑步骤表，且没有 run_agent 之类子代理步骤。替代路径：用 workflow 串行编排 + 多次 subagent 调用。 |
| 10 | `workflow.maxItemsPerCall` | 缺每调用项数上限。降级：foreach 全量展开，超大数组会一次性跑完（只有正文 3000 rune 的回包上限）。替代路径：无。 |
| 11 | `workflow.maxTotalAgents` | 缺 workflow agent 总量预算。降级：无总量限制，全靠调用方自律；异常脚本可能长时间占用。替代路径：无。 |
| 12 | `workflow.syncTimeoutMs` | 缺 workflow 级同步等待预算。降级：受外层工具超时（tools timeout-policy）约束，workflow 自身不能设上限。替代路径：调用方在 ctx 里设期限。 |

#### llm（9 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 13 | `llm.callConfig.controls` | 缺 temperature 与 stop 两个采样参数（也无法透传到 wire）。降级：请求体不带 temperature / stop，模型只能用服务端默认温度，也没法用停用词截断输出。替代路径：无（只能靠提示词约束）。 |
| 14 | `llm.deepseek.fileExpiresAfterSeconds` | 缺 Files API 上传与到期回收。降级：图片全量内联/本地存储，本地附件不会自动过期（靠 session.spill.cleanupPeriodDays 之类的清理另算）。替代路径：无（不需要 Files API 也能工作，属能力面差异）。 |
| 15 | `llm.deepseek.filesApiTimeoutMs` | 缺 Files API 上传超时（以及整条上传通道）。降级：无该路径；图片走内联 base64，受 maxInlineRequestImageBytes 约束。替代路径：无。 |
| 16 | `llm.deepseek.modelsMaxAgeMs` | 缺模型列表缓存与 TTL。降级：每次列模型都打一次上游 /models，频繁刷新会放大延迟与限流风险。替代路径：调用方自行缓存结果。 |
| 17 | `llm.deepseek.streamIdleTimeoutMs` | 缺流空闲看门狗（300s 无数据即判失败）。降级：上游半开连接时请求会一直挂着，直到调用方 ctx/工具超时；期间界面停在“正在生成”。替代路径：外层 tools timeout-policy 的 per-call 预算（DSHD_TOOL_TIMEOUT_SECS）。 |
| 18 | `llm.piAi.streamIdleTimeoutMs` | 缺 per-provider 流空闲超时。降级：挂住的流只能等外层 ctx 取消；与 llm.deepseek.streamIdleTimeoutMs 同一缺口。替代路径：无。 |
| 19 | `llm.resolveModelInfo.contextWindowValidation` | 缺 modelInfo 的 contextWindow 校验错误码。降级：非法值（0/负数）被当成未声明，回退内置默认，用户看不到“你写错了”。替代路径：无。 |
| 20 | `llm.stop` | 缺 stop 序列。降级：只能靠提示词或客户端截断，模型可能继续说；结构化输出场景少了一道硬闸。替代路径：无。 |
| 21 | `llm.temperature` | 缺 temperature 透传。降级：所有请求使用服务端默认温度，无法做确定性（temperature=0）复现，也无法调创造性。替代路径：无。 |

#### context（12 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 22 | `context.compaction.compactionRetries` | 缺摘要收敛重试。降级：摘要调用抖动（429/空响应）时整次压缩失败，本轮无法腾出空间。替代路径：靠 llm 层重试（internal/llm/retry.go）兜住一部分瞬时错误。 |
| 23 | `context.compaction.headroomTokens` | 缺窗口 headroom 预留。降级：压缩后仍可能因输出/工具结果把窗口顶满（下一次请求 400 风险），没有“留 64K”的保护带。替代路径：无（可把 auto-compact 阈值调小近似）。 |
| 24 | `context.compaction.maxOverflowRetries` | 缺溢出后重试。降级：上游返回上下文溢出时这一轮直接失败，需要用户重发。替代路径：无。 |
| 25 | `context.compaction.modelPolicies` | 缺 per-model 压缩策略覆盖。降级：所有模型共用一套阈值（1M 窗口与 32K 窗口用同一 auto-compact），小窗口模型容易顶满。替代路径：无。 |
| 26 | `context.compaction.reservedCompletionTokens` | 缺完成 token 预留。降级：压缩阈值不扣除 max_tokens，长输出场景仍可能溢出。替代路径：无（可把 auto-compact 调小近似）。 |
| 27 | `context.measurement.pressureTokens` | 缺 prompt 侧压力口径（input + cacheRead + cacheWrite）。降级：界面显示的已用量包含输出 token，比真实占用偏大；与官方 pressureTokens 不可比。替代路径：无。 |
| 28 | `context.measurement.projectedTokens` | 缺 projectedTokens 投影（下一次请求的占用预测）。降级：界面只能显示“上一次真实用量”，看不到“下一条消息会占多少”，压缩时机只能靠固定阈值。替代路径：无。 |
| 29 | `context.measurement.usageBuckets` | 缺 uncachedInputTokens 与 cacheWriteTokens 两个桶。降级：缓存写入量与“未命中缓存的输入”在界面不可见，缓存命中率只能算一半。替代路径：internal/llm/usage_cache.go 已有 cacheWritePaths 解析（叶子路径在），只是没进投影。 |
| 30 | `context.runtimeContext.perStep` | 缺每步 runtime context 重投影。降级：系统提示里的时间/环境信息在会话开始就固定，长会话中“现在几点/在哪个目录”这类事实会过期。替代路径：模型自己调工具查（date/pwd）。 |
| 31 | `context.sessionReference.maxReferenceBytes` | 缺单条引用的字节上限。降级：无自动引用。替代路径：无。 |
| 32 | `context.sessionReference.maxReferences` | 缺会话引用自动附带（最多 3 条）。降级：用户必须手工 @ 选，模型不会自动拿到相关会话上下文。替代路径：@ 引用 + 手工粘贴。 |
| 33 | `context.sessionReference.referenceContextFraction` | 缺引用上下文预算（20% 窗口）。降级：无自动引用。替代路径：无。 |

#### session（11 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 34 | `attachment.imageCompressionConcurrency` | 缺图片压缩并发度（与归一化同一缺口）。降级：无压缩。替代路径：无。 |
| 35 | `attachment.maxImagesPerMessage` | 缺每消息图片张数上限。降级：一条消息可以塞任意多图（受 promptMaxBlocks=64 间接限制），内存与请求体不可控。替代路径：无。 |
| 36 | `attachment.normalizedImageMaxBytes` | 缺归一化后的字节预算（与重编码一体）。降级：同上，只有接收上限。替代路径：无。 |
| 37 | `attachment.normalizedImageMaxPixels` | 缺图片归一化（重采样到目标像素）。降级：大图原样内联，请求体膨胀（只能靠 maxImageBytes 拒收，用户体验是“这张图太大”而不是“已自动压缩”）。替代路径：无（需引入 Go 图像缩放）。 |
| 38 | `goal.defaultMaxGoalRounds` | 缺默认轮次上限（256）。降级：goal 一旦 active 就没有轮次天花板，只能人工 complete/pause；失控循环只能靠人盯。替代路径：调用方显式给 maxGoalRounds。 |
| 39 | `jobs.maxConcurrentJobsPerOwner` | 缺 per-owner 并发任务上限。降级：模型可以无限起后台任务，没有背压，机器可能被拖慢。替代路径：无（只能靠提示词自律）。 |
| 40 | `jobs.settledRetainBytes` | 缺“已结束任务”的独立保留量。降级：结束的任务继续按 1MiB 占内存，长跑多任务的会话内存随任务数增长。替代路径：无。 |
| 41 | `session.persistence.listConcurrency` | 缺列会话的并发度。降级：会话多时列会话是纯串行 IO，界面“最近会话”列表随文件数线性变慢。替代路径：无（可参考 fsutil.WalkFiles 的并发遍历自己加）。 |
| 42 | `session.projectionCache.writeEveryEvents` | 缺写回批量（每 N 条事件 / 每 T 毫秒落一次）。降级：每次投影写都同步落盘，长会话高频事件下 fsync 次数按事件数线性放大。替代路径：无（storage.Domain 的写链可复用，但需要调用方自己攒批）。 |
| 43 | `session.projectionCache.writeIntervalMs` | 缺写回定时器（5s 间隔）。降级：与 writeEveryEvents 同一根因——没有后台写回，只有同步写。替代路径：无。 |
| 44 | `session.spill.cleanupPeriodDays` | 缺 spill 文件保留期与自动清理。降级：外置 artifact 永久堆积（临时目录随使用增长，磁盘只增不减）。替代路径：人工清 %TEMP%\dshd-spill。 |

#### tools-sandbox-approval（13 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 45 | `approval.memory` | 缺“每次请求都重问”的严格语义（宿主默认按会话记忆）。降级：批准一次后同类调用静默放行，审计面收窄（日志里看不到第二次询问）。替代路径：关闭记忆需要改代码，没有反向开关。 |
| 46 | `fs.diffBasisMaxBytes` | 缺 diff 基线快照（以及随之而来的“基线太大就不做 diff”保护）。降级：编辑直接落盘，没有可回看的基线；大文件编辑不产生 diff 证据。替代路径：靠会话日志里的 write 事件重建。 |
| 47 | `sandbox.additionalWritableRoots` | 缺工作区外的追加可写根。降级：要写工作区外的目录只能整档升到 danger-full-access（权限一次性放大到全盘），没有“只多这一个目录”的细粒度。替代路径：无。 |
| 48 | `sandboxLocal.probeTimeoutMs` | 缺外部沙箱 runner（真隔离）。降级：进程内策略闸门 + Job Object 强杀，不是 OS 级隔离；能防误写，防不住恶意代码。替代路径：无（平台项 40/42/43 已登记后置）。 |
| 49 | `sandboxLocal.runnerCommand` | 缺可配置的沙箱 runner（与 probeTimeoutMs 同一缺口）。降级：无外部 runner，命令直接在宿主进程树里跑。替代路径：无。 |
| 50 | `skill.collectCacheMaxEntries` | 缺 skill 收集缓存。降级：每次列/找 skill 都打一遍文件系统，skill 多时列表变慢。替代路径：无。 |
| 51 | `skillFs.watchUsePolling` | 缺 skill 目录监听（原生事件与轮询都没有）。降级：skill 变更在会话内不可见，必须重启。替代路径：无。 |
| 52 | `terminal.activityPollIntervalMs` | 缺终端活跃度采样与闲置判定。降级：无法区分“有人在用”和“挂着没人管”，回收只能靠人工。替代路径：无。 |
| 53 | `terminal.cleanupRetryMs` | 缺清理重试节奏。降级：清理失败就泄漏（句柄/临时文件），没有兜底重试。替代路径：重启宿主。 |
| 54 | `terminal.disposeGraceMs` | 缺销毁宽限。降级：终端被立刻杀掉，shell 里的收尾脚本（写历史、清理临时文件）来不及跑。替代路径：命令自己 trap 也无用（PTY 关闭即收到挂断）。 |
| 55 | `terminal.maxTerminals` | 缺每会话终端数上限。降级：可以无限开 PTY，每个都占一个 conhost 进程与缓冲，机器资源可被打满。替代路径：无（官方上限 8 可直接移植）。 |
| 56 | `terminal.unattendedTimeoutMs` | 缺无人值守终端寿命上限。降级：后台终端可永久存活（进程级泄漏），没有人回收。替代路径：人工 kill。 |
| 57 | `tools.ptc.maxParallelSubCalls` | 缺 PTC 并行子调用池。降级：子调用只能串行（workflow 的 JSON 步骤表 + 串行 for）。替代路径：无。 |

#### transport-http（7 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 58 | `cordis.hostRunner.clientInspectTimeoutMs` | 缺 inspect 专用超时。降级：inspect 挂住时一直等到外层 ctx 取消（可能几分钟）。替代路径：调用方设 deadline。 |
| 59 | `cordis.hostRunner.vmTimeoutMs` | 缺脚本 VM 默认超时（5s）。降级：脚本死循环只能靠调用方 ctx（外层工具超时）打断；没有 per-script 预算。替代路径：调用方自己设 ctx deadline。 |
| 60 | `sessionController.listWorkSliceMs` | 缺列会话的工作切片预算。降级：会话多时一次请求同步扫完全部会话文件，HTTP 请求被拖长（没有增量/分片返回）。替代路径：无（可加 ctx 期限近似）。 |
| 61 | `webserver.compressionLevel` | 缺运行期 gzip 压缩（级别可调）。降级：只有 dist 里预先压好的资源被压缩，API/WS 文本响应（JSON 帧）全程不压缩，长会话历史响应体积翻倍。替代路径：为 dist 预压缩（已有），API 侧无。 |
| 62 | `webserver.compressionThresholdBytes` | 缺压缩阈值。降级：与 compressionLevel 同一根因（无运行期压缩）。替代路径：无。 |
| 63 | `webserver.tls` | 缺 HTTPS 监听。降级：只能明文 HTTP（本机环回够用，跨机/隧道场景无 TLS）。替代路径：反向代理（nginx/caddy）终结 TLS。 |
| 64 | `workspaceController.documentsLookupTimeoutMs` | 缺 Documents 查找超时。降级：域账号/网络重定向的 Documents 目录查询可能阻塞（启动期一次），没有超时兜底。替代路径：无。 |

#### tui-capability（2 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 65 | `context.occupancy.display` | 缺 contextPressure 三数视图。降级：界面能显示“已用/上限”，但没有“prompt 压力”和“下一次请求预计占用”，上下文占用条只能事后看。替代路径：无（与 context.measurement.* 同一缺口族）。 |
| 66 | `placement.goValuesMapping` | 缺 placement → 官方双队列的**真机会话**等价性验证。降级：映射按字段名实现（turn=旧语义、steer=agent.steer、followup=排队下一轮），queue/now 的真实落点未证；语义错位只会在真机上表现为“消息插错位置”。替代路径：无（需一次真实会话对照实验）。 |

#### env（1 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 67 | `DSH_SYSTEM_PROMPT` | 缺“替换系统提示 persona 前缀”的入口。降级：persona 只能靠改 AGENTS.md（会被拼进提示，但不是替换前缀）；不同 profile 想换人格必须改文件。替代路径：AGENTS.md 覆盖（不精确）。 |

#### live-profile-desktop（4 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 68 | `llmPiAi.providers.magpie.streamIdleTimeoutMs` | 缺 per-provider 流空闲超时（600s）。降级：上游半开连接时请求一直挂着，直到调用方 ctx 或工具超时。替代路径：外层工具超时兜底。 |
| 69 | `sessionLogDeepseek.enabled` | 缺会话日志上传管道（含开关）。降级：v4 日志只落本地，没有"上传/不上传"的选择——想外传只能自己拷文件。替代路径：无（要上传得先实现出站管道 + 脱敏 + 用户授权）。 |
| 70 | `subagent.maxActiveSubagents` | 缺并发子代理上限（20）。降级：可无限并行派生，机器资源与 API 配额无背压。替代路径：无。 |
| 71 | `toolWeb.searchTimeoutMs` | 缺 web_search 超时。降级：搜索挂住时只能等外层工具超时（默认更宽），单次慢查询会拖住整轮。替代路径：无。 |

#### schedule-plugins-account-telemetry（42 条）

| # | 官方 key | 缺什么 / 降级 / 替代路径 |
|---|---|---|
| 72 | `account.attemptTimeoutMs` | 缺设备登录的单次尝试预算。降级：登录请求受 20s client 超时约束，长流程（等用户在浏览器点授权）会被截断。替代路径：无。 |
| 73 | `account.balanceTimeoutMs` | 缺余额查询独立超时。降级：与账号 API 共用 20s，余额慢时整条链路一起失败。替代路径：无。 |
| 74 | `account.inferenceOrigin` | 缺"账号侧推理端点 origin"配置。降级：账号 API 与推理端点各自配置，切到自建/官方端点时不会联动，容易一半指向 A 一半指向 B。替代路径：分别设 DSHD_ACCOUNT_PLATFORM_ORIGIN 与 DSHD_LLM_BASE。 |
| 75 | `account.logoutMaxRetries` | 缺登出重试。降级：网络抖动时登出失败即返回错误，本地凭据可能已清、服务端会话仍在。替代路径：手动再登出一次。 |
| 76 | `account.logoutRetryDelayMs` | 缺登出重试间隔（同一缺口族）。降级：无重试。替代路径：无。 |
| 77 | `account.requestTimeoutMs` | 缺可配的账号请求超时。降级：写死 20s（比官方 30s 更紧），慢网络下的登录/余额查询可能先超时。替代路径：无（external 部分只能改代码）。 |
| 78 | `agentTeam.disposalTimeoutMs` | 缺团队销毁超时。降级：销毁慢（成员多/文件大）时会一直阻塞调用方。替代路径：无。 |
| 79 | `agentTeam.maxMembers` | 缺团队人数上限。降级：可以无限加成员，团队拓扑与消息扇出无界。替代路径：无。 |
| 80 | `agentTeam.maxMessageBytes` | 缺团队消息字节上限。降级：单条消息可无限大，撑爆状态文件。替代路径：无。 |
| 81 | `agentTeam.maxTasks` | 缺团队任务数上限。降级：任务表可无限增长（内存 + 文件）。替代路径：无。 |
| 82 | `analytics.environmentQueryMaxOutputBytes` | 缺环境查询输出上限（同一缺口族）。降级：无该通道。替代路径：无。 |
| 83 | `analytics.environmentQueryTimeoutMillis` | 缺环境采集（os/arch/版本等遥测属性）。降级：事件里没有环境维度，无法按平台切片分析。替代路径：无。 |
| 84 | `hmr.ignored` | 缺 HMR 忽略清单。降级：插件根下任何文件变动都会触发重载判定（含 node_modules），大插件树下噪声大。替代路径：把插件根限制在无 node_modules 的目录。 |
| 85 | `pluginManager.detailFetchTimeoutMs` | 缺详情抓取超时（与 detailMaxBytes 同一缺口）。降级：无该通道。替代路径：无。 |
| 86 | `pluginManager.detailMaxBytes` | 缺包详情抓取（含字节上限）。降级：远程包的 README/元数据拿不到。替代路径：无。 |
| 87 | `pluginManager.fallbackRegistries` | 缺 registry 源列表（含回退）。降级：无 registry 通道，装不了任何远程包。替代路径：无。 |
| 88 | `pluginManager.idleTimeoutMs` | 缺空闲子进程回收（pnpm 通道专属）。降级：无子进程可回收。替代路径：无。 |
| 89 | `pluginManager.inspectTimeoutMs` | 缺 inspect 子进程预算。降级：inspect 只在本地目录上工作，无子进程可管。替代路径：无。 |
| 90 | `pluginManager.lockWaitMs` | 缺安装锁等待（pnpm 通道专属）。降级：本地目录安装不需要锁等待；并发安装靠 job 串行。替代路径：无（也不需要）。 |
| 91 | `pluginManager.outputBytes` | 缺安装输出尾部缓冲。降级：安装失败只有结构化错误，没有原始 stdout 尾巴可贴。替代路径：无。 |
| 92 | `pluginManager.pnpmCommand` | 缺 pnpm 安装通道（子进程 + 参数注入）。降级：只能装本地目录包，registry 规格直接返回结构化未实现。替代路径：无（要真装 npm 包得先做 registry 客户端）。 |
| 93 | `pluginManager.toolsMounted` | 缺"是否挂载插件工具"的开关（宿主恒挂载，官方默认不挂）。降级：插件一装好工具就出现在模型工具表里，没有下线开关。替代路径：卸载插件。 |
| 94 | `pluginManager.versionListLimit` | 缺版本列表查询（含条数上限）。降级：插件市场里看不到可安装版本。替代路径：无。 |
| 95 | `pluginManager.versionLookupConcurrency` | 缺并发版本查询。降级：无该查询通道。替代路径：无。 |
| 96 | `systemPrompt.includeRuntimeContext` | 缺 runtime context 段（工作目录/时间/环境）。降级：模型不知道"现在几点、在哪个目录"，长会话里这些事实会过期。替代路径：模型自己调工具查（date/pwd）。 |
| 97 | `systemPrompt.personaPrefix` | 缺可配 persona 前缀。降级：换人格要改 AGENTS.md（全局生效，不能按 profile 切换）。替代路径：AGENTS.md 覆盖（不精确）。 |
| 98 | `systemPrompt.toolOrder` | 缺工具顺序覆盖。降级：提示里工具顺序只能由装配顺序决定（想调顺序得改代码）。替代路径：无。 |
| 99 | `telemetry.anonymousUserId` | 缺匿名用户 id。降级：事件无法跨会话去重/统计独立用户数。替代路径：无。 |
| 100 | `telemetry.host.exportTimeoutMillis` | 缺 host 遥测导出管道（20000）。降级：产品遥测只有本地落盘与可选单次导出，没有队列/批量/定时/关停冲刷。替代路径：本地 JSONL 可外部采集。 |
| 101 | `telemetry.host.maxExportBatchSize` | 缺 host 遥测导出管道（512）。降级：产品遥测只有本地落盘与可选单次导出，没有队列/批量/定时/关停冲刷。替代路径：本地 JSONL 可外部采集。 |
| 102 | `telemetry.host.maxQueueSize` | 缺 host 遥测导出管道（2048）。降级：产品遥测只有本地落盘与可选单次导出，没有队列/批量/定时/关停冲刷。替代路径：本地 JSONL 可外部采集。 |
| 103 | `telemetry.host.scheduledDelayMillis` | 缺 host 遥测导出管道（30000）。降级：产品遥测只有本地落盘与可选单次导出，没有队列/批量/定时/关停冲刷。替代路径：本地 JSONL 可外部采集。 |
| 104 | `telemetry.host.shutdownTimeoutMillis` | 缺 host 遥测导出管道（21000）。降级：产品遥测只有本地落盘与可选单次导出，没有队列/批量/定时/关停冲刷。替代路径：本地 JSONL 可外部采集。 |
| 105 | `telemetry.host.timeoutMillis` | 缺 host 遥测导出管道（15000）。降级：产品遥测只有本地落盘与可选单次导出，没有队列/批量/定时/关停冲刷。替代路径：本地 JSONL 可外部采集。 |
| 106 | `telemetry.session.exporter.timeoutMillis` | 缺导出超时（1000ms）。降级：导出挂住时只能等调用方 ctx 取消。替代路径：无。 |
| 107 | `telemetry.session.maxRequestBytes` | 缺遥测请求字节上限（OTel 批量专属）。降级：无批量，也就没有该限制。替代路径：无。 |
| 108 | `telemetry.session.processor.exportTimeoutMillis` | 缺 OTel 批处理队列/批量/定时导出（1500）。降级：事件逐条落 JSONL，没有内存缓冲，因此没有队列溢出、批量、定时这三种行为。替代路径：无。 |
| 109 | `telemetry.session.processor.maxExportBatchSize` | 缺 OTel 批处理队列/批量/定时导出（2048）。降级：事件逐条落 JSONL，没有内存缓冲，因此没有队列溢出、批量、定时这三种行为。替代路径：无。 |
| 110 | `telemetry.session.processor.maxQueueSize` | 缺 OTel 批处理队列/批量/定时导出（2048）。降级：事件逐条落 JSONL，没有内存缓冲，因此没有队列溢出、批量、定时这三种行为。替代路径：无。 |
| 111 | `telemetry.session.processor.scheduledDelayMillis` | 缺 OTel 批处理队列/批量/定时导出（10000）。降级：事件逐条落 JSONL，没有内存缓冲，因此没有队列溢出、批量、定时这三种行为。替代路径：无。 |
| 112 | `telemetry.session.shutdownTimeoutMillis` | 缺 OTel SDK 关停冲刷（3000ms）。降级：进程退出时没有"把队列里的事件刷出去"这一步（因为本来就没有队列）。替代路径：无。 |
| 113 | `workingDirectory.defaultDirectory` | 缺新会话默认工作目录。降级：新会话一律用进程 cwd（--home 是会话根，不是工作目录），想换目录只能每次进会话后改。替代路径：启动时切 cwd。 |

## 8. 写作用域外：TUI2GO 侧（owner=tui-line）

本任务的写作用域是 `cmd/`、`internal/`、`docs/`、`tmp/GO3-PARAMS/`；TUI2GO 只在确有 ALIAS-MISMATCH 时改。

**GO.5 起这一节基本清空**：/T2 可搬移 launcher（8 处机器绝对路径清零）、/T3 官方 i18n +
permission-modes overlay 退场、/T5 play 根自举（`launcher/bootstrap-play.mjs`）把原来的 12 条
`HARDCODED` 收成 **2 条**，逐条见 §5 与 §12。仍然 `HARDCODED` 的只剩：

| key | 位置 | 我们的默认 | verdict |
|---|---|---|---|
| `install-play 每次启动都跑 --ensure` | `public/dsh-tui2go/launcher/run-019.cmd:77` | node %~dp0install-play.mjs --ensure | **HARDCODED** |
| `overlay: workspace-attach-trace 落盘` | `public/dsh-tui2go/launcher/overlay/workspace-attach-trace.mjs:51-27` | 每次 workspace 附着 appendFileSync 一行 JSONL 到 $DSH_GO_DEBUG_LOG | **HARDCODED** |

仍为 `UNREAD` 的（官方有、宿主没读，结构化未实现）：

| key | 位置 | 我们的默认 | verdict |
|---|---|---|---|
| `alias: effort 阶梯的按模型查表` | `public/dsh-tui2go/src/effort.ts:100` | rows.get(ref.model)（catalog 行 id 作键） | **UNREAD** |

## 9. 未做 / 边界

- 官方 342 条这一轴上**没有 `UNREAD` 了**：原 210 条已逐条三分（t1），剩下 71 条 `N-A` 也补齐了子类
  并复核改判（t5：17 条改 `COVERED-EQUIV`、45 条改 `UNREAD-GAP`、9 条留 `N-A` 带子类）；
  t7 再按 t4 交叉核对改判 3 条（`uiTheme.preference` / `transcriptView` → `COVERED-EQUIV`，
  `sessionLogDeepseek.enabled` → `UNREAD-GAP`）。
  真缺口 `UNREAD-GAP` 113 条，每条写清了缺什么 / 现在降级成什么样 / 有无替代路径，可直接派单。
- env 15 条的实际分布是 **`COVERED` 3 / `COVERED-EQUIV` 6 / `N-A` 5 / `UNREAD-GAP` 1**（GO.5/T1 之后；
  此前是 `COVERED-EQUIV` 10 / `N-A` 4 / `UNREAD-GAP` 1）：
  GO.5/T1 把 `DSH_TELEMETRY_OTLP_URL` / `DSH_PRODUCT_ANALYTICS_OTLP_URL` / `DSH_MAX_TOKENS_AS_SUCCESS`
  做成**官方名真读取**（→ `COVERED`），把 `DSH_HOME` 判成 **`N-A`（`naming-conflict`）**。
  `N-A` 5 = `DSH_HOME` / `DSH_TELEMETRY_MODE` / `DSH_TELEMETRY_DISABLED` / `DSH_CLIENT_VERSION` / `DSH_PRIMARY_RUNTIME`，
  GAP 1 = `DSH_SYSTEM_PROMPT`。全部 15 条的 `reason` 都写明「官方名是否生效 + 实际入口」。
  `DSH_WEB_SEARCH_PROVIDER` / `DSH_WEB_FETCH_PROVIDER` 双前缀也生效（复核后保持 `COVERED-EQUIV`）。
- 宿主参数表（§4）里仍有 5 条旧 `UNREAD` 标签（`subRunner.timeout`、`subRunner.maxDepth`、
  `compact.Summarize MaxTokens`、`agentQuestionWaitTimeout` 等）：它们是**宿主自己写死的值**、
  没有 CLI/env 入口，与官方 342 条那一轴是两回事，仍按「结构化未实现」登记。
- `N-A` 条目里有一部分是**内部安全常量**（重试、缓冲、轮询间隔）。把它们做成配置项只会增加参数面，
  不会让产品更接近官方——官方那些值也是库内常量。
- TUI2GO 侧 `src/` 的改动只做了 t8/t11/t12 三处（别名、调试日志、policy），其余 HARDCODED 由 TUI 线负责。

## 10. 本轮刷新（t3：行号漂移 + 词表归一）

> 由 `tmp/GO3-PARAMS/register/refresh_all.py` 刷新；校验器 `tmp/GO3-PARAMS/register/validate_register.py`。

- **evidence 校验**：`python tmp/GO3-PARAMS/register/validate_register.py` → `OK=306 MISMATCH=0 NO-REF=4 SKIP-NO-IDENT=20 SKIP-WHITELIST=7`，退出码 **0**。
- 判据两条：① 文件存在且行号在范围内；② ±2 行窗口内含 key 的标识符之一（大小写不敏感）。
- 累计改了 **261** 处引用：行号**真的变了** 236 处，另有 25 处只补了目录（裸 `main.go:1012` → `cmd/dshd/main.go:1012`，方便人直接 `sed`）。详情见 `tmp/GO3-PARAMS/register-refresh/RESULT.md` 与本轮 `tmp/GO5-DELIVERABLE/verify/RESULT.md`。
- `ALIGNED` 已归一（原词保留在各条 note）；`DEVIATION-SAFETY` 是 t10 要求新增的合法值，已进词表；98 个 CLI flag 全部补 `verdict`。

## 11. 本轮更新（GO.5 / T1：官方 env 收口）

> 详细说明与自查命令见 [`go5-env-notes.md`](go5-env-notes.md)；证据 `tmp/GO5-DELIVERABLE/env/RESULT.md`。

- **3 条官方 env 从「0 处读取」变成真读取**（verdict `COVERED-EQUIV` → `COVERED`）：
  - `DSH_TELEMETRY_OTLP_URL` / `DSH_PRODUCT_ANALYTICS_OTLP_URL` → `internal/analytics/env.go` 的
    `CollectorURLFromEnv` 喂 `Policy.CollectorURL`。优先级 **显式配置 > env > 默认（空 = 本地零出网）**；
    读到 env 只改端点、不改 `mode`（默认仍一个字节都不外发）；非法 URL 在 `analytics.Open` fail-loud 并点名变量。
  - `DSH_MAX_TOKENS_AS_SUCCESS` → `internal/agent/finish_policy.go` 的 `MaxTokensAsSuccess`（只认 `false`/`0`/`no`）+
    `internal/agent/loop.go` 副作用前闸门。true（默认）：`turn/end{kind:"max-tokens"}`；false：`turn/end{kind:"error"}`。
- **1 条改判 `N-A`（新增子类 `naming-conflict`）**：`DSH_HOME` —— 官方 `DSH_HOME` 是 DSH 数据根，
  宿主 home 根走 `DSHD_HOME`，而 TUI2GO 的 `DSH_HOME` 是桌面 profile home；两个语义撞车，显式不做别名（见 §9 与 notes §4）。
- **2 条复核后保持** `COVERED-EQUIV`：`DSH_WEB_SEARCH_PROVIDER` / `DSH_WEB_FETCH_PROVIDER`（`internal/spi/search.go:375/388` 双前缀）。
- 计数：`COVERED` 60 → **63**，`COVERED-EQUIV` 139 → **135**，`N-A` 30 → **31**（`naming-conflict` 1），`UNREAD-GAP` 113 不变；总数仍 342。
- 门禁：`go build ./... && go vet ./...`、`go test ./...` 全绿；先红后绿见 §2 的 F11/F12/F13。

## 12. 本轮更新（GO.5 / T4：可搬移 + 总账收口）

> 证据：`tmp/GO5-DELIVERABLE/verify/RESULT.md`（副本链 + 原目录链 + 官方 env 真实链 + 两道闸）。

- **总账重锚**：T2 重写 `launcher/run-019.cmd` / `launcher/TUI-GO.vbs`、T3 退场
  `launcher/overlay/permission-modes.mjs` 并新增 `src/i18n.ts`、T5 重写
  `launcher/install-play.mjs` 之后，8 条 `file:line` 漂移**全部按 key 重新 grep 定位**
  （`tmp/GO5-DELIVERABLE/verify/reanchor_register.py`，写回前对每行做 `assert` 硬校验）：
  `DSH_GO_MODEL` / `HOME / USERPROFILE` / `DSH_HOME` / `PATH 前缀` / `install-play --ensure` /
  `alias 模型 id` / `overlay: 4 档权限循环`（NOFILE）/ `overlay 引擎: .bak 后缀`（NOFILE）。
- **两份总账逐字节一致**：`docs/parameter-register.json` 与
  `tmp/GO3-PARAMS/register.json` 同一 sha256（`157f2e99…`），统一 LF；
  `meta.validate` 记录本轮闸值（`OK=306 MISMATCH=0`、可疑 0、cwd 自检 ok）。
- **verdict 归位**（T2/T3/T5 已修，总账不再挂着旧的 `HARDCODED`）：`HARDCODED` 14 → **2**（其中 TUI2GO 侧 12 条 → 2 条），`OK` 122 → **134**；`fixedBy` 新增 `t2` 8 条、`t3` 3 条、`t5` 1 条。
  剩下 2 条 `HARDCODED` 是**真行为**（每次启动跑 `--ensure`；debug 落盘路径固定），不是漂移。
- 门禁：`validate_register.py` → `MISMATCH=0` EXIT=0；`audit_refs.py` → 可疑 **0** EXIT=0。
