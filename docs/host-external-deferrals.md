# 宿主外部依赖与平台项处置（HS-15 · HOST-EXTERNAL-DEFERRALS）

> **task_id**：HS-15 · **owner**：`qa` · **model**：`group/deepseek-v4.1-flash-1017`
> **base_rev**：`9cfe2f4949d2d19a243d741160ce290f16d4d3b1`（分支 `task/HS-15`，干净）
> **官方源**：`D:/DeepSeek Harness/Desktop-0.2.2-rc.1/resources/app.asar`（sha256 `5fa8a6b21bceb8f3e99e30efa2270efd0cc773a11e42aa0488f804a64b34e637`，213202918 B）
> **证据根**：`$S = C:/Users/Administrator/AppData/Local/Temp/HOST-STABLE-20261107/HS-15`，官方包解包在 `$S/official-evidence/official/`
> **性质**：**文档处置，非产品实现**。`DOCS_ONLY=1 PRODUCT_UNVERIFIED=1`。

**本文是外部服务 / R6 / 平台项的唯一处置账。** 每一项在这里有且只有一个结论：
`DO_NOT_IMPLEMENT` 或 `POSTPONE`。**没有任何一项可以是 DONE**——外部依赖缺失不是已完成，
空成功、假数据、假流式、假 shareId、假转写、假 diff、假余额一律不算处置。

---

## 0. 结论总表

| # | 项 | 官方落点 | 结论 | 当前宿主行为 | 缺什么（一句） |
|---:|---|---|---|---|---|
| 1 | `dynamicCordisRunner` | `dsh-cordis-host-runner` | **DO_NOT_IMPLEMENT**（R6） | 11 方法逐条 501 `not_implemented` | 宿主不嵌 JS 运行时，无合法执行路径 |
| 2 | PTC runtime | `dsh-ptc-runtime-node`（#72） | **DO_NOT_IMPLEMENT**（R6） | 无运行时；`workflow-ptc` 是 JSON 步骤表 | 同上，且官方执行体是 Node VM/REPL |
| 3 | Windows bash-sandbox | `dsh-bash-sandbox`（#42） | **DO_NOT_IMPLEMENT**（win32 官方 disabled） | bash 直起，零 confinement | 官方 Windows 无命令级 OS 沙箱可对齐 |
| 4 | pwsh/sandbox runner | `dsh-pwsh-sandbox`（#43）+ `dsh-sandbox-local`（#40） | **POSTPONE** | 只有 policy 三档，无 OS runner | 无受限令牌/ACL runner，且官方只承诺 partial |
| 5 | forge | `dsh-experimental-client-ui-forge`（19） | **POSTPONE** | 27 条 spec 全 501 | 无 code host 客户端、无 worktree、无 binding 存储 |
| 6 | liveshare | `dsh-experimental-liveshare`（8） | **POSTPONE** | 同上 | 无 relay、无 owner 凭据、无 OIDC 身份 |
| 7 | speech | `dsh-experimental-api-speech-to-text`（4） | **POSTPONE** | 4 条 501 | 无 ASR 模型/音频解码/VAD/推理管线 |
| 8 | account | `dsh-api-account-controller`（7） | **POSTPONE** | 4 真实现（本地 grant + `/models` 发现），6 条 501 | 无官方 DeepSeek Platform 账号服务与 OAuth |
| 9 | pluginMarket/recommendations | `dsh-plugin-market`（3）+ `dsh-plugin-recommendations`（1） | **POSTPONE** | 4 条 501 | 无市场索引源、无 npm CDN/Git 读取器、无推荐卡记录 |
| 10 | OTel/session-log 上报 | `dsh-otel`（#37）+ `dsh-session-telemetry-otel`（#38）+ `dsh-session-log-deepseek`（#8） | **POSTPONE** | 零 OTel/telemetry 代码 | 无 OTLP 导出器、无 FEEDBACK_ONLY 结算、无上报端点授权 |
| 11 | 远程授权/远程访问 | `dsh-api-remote-access`（#12）+ `dsh-authorization`（#23/24） | **POSTPONE** | 只有 launch token→cookie 与 Peer 凭据 | 无方法级 RBAC 策略、无授权流程 seam |

**计数口径（见 §1.3）**：本文覆盖 **11 个处置项 / 66 个官方 remote 方法**，其中
**53 个在官方 153 桌面清单内**，**13 个是 153 清单外的 raw remote-client 方法**（同样未实现、
必须点名，不得静默漏掉）。

---

## 1. 判据与口径

### 1.1 两种结论的定义

| 结论 | 含义 | 允许出现的理由 |
|---|---|---|
| `DO_NOT_IMPLEMENT` | **永久边界**：由红线（R6 不嵌 JS 运行时）或官方平台开关决定，不是欠账 | 只有用户先改对应红线才重立项；Go 侧实现不得冒充该能力 |
| `POSTPONE` | **后置**：能力可做，但当前缺真实外部依赖/服务/真值 | 必须写清重入条件；重入前保持四字段未实现错误 |

两者都**不是** DONE，也**不是** N/A。文档里任何"某项已同步/已接入/已实现"的写法，
只要该能力依赖外部服务，就是**假数据**。

### 1.2 官方错误形状（唯一信封）

官方桌面端业务失败**不是 HTTP 错误码**，而是 **HTTP 200 + `result.ok:false`**：

```json
{"type":"server-response","rpcId":"<逐字回显>","result":{"ok":false,
 "error":{"code":"<namespace>/<reason>","message":"…","details":{}}}}
```

- `details` **必须是对象**（`internal/officialwire/codes.go:38-47` 已按此实现；nil 补 `{}`）。
- 4xx/5xx 只表达传输/认证/处理器异常（`internal/officialwire/codes.go:146-168` 的
  `TranslateLegacy` 就是这条规则的唯一翻译点）。
- 网关层固定码：`gateway/bad-request`、`gateway/arguments-invalid`、`gateway/unknown-method`、
  `gateway/method-unavailable`、`gateway/protocol`、`gateway/uplink-overflow`、
  `gateway/internal`、`gateway/cancelled`、`gateway/peer-disposed`、
  `gateway/interactions-forbidden`、`remote-access/denied`（`internal/officialwire/codes.go:10-36`，
  照 `dsh-api-gateway/lib/index.js` 抄录）。
- **本宿主对未接线方法只允许两种形状**：
  - 方法在官方面上存在、宿主没能力 → `gateway/method-unavailable`（`internal/officialwire/server.go:391-393`）；
  - URL 合法但方法没挂 → `gateway/unknown-method`（`internal/officialwire/server.go:377-379`）。
- **禁止**：`ok:true` 空载荷、`result:null` 冒充成功、假流式（只发空帧）、
  本地字符串假装已落盘。本仓历史上 `{"result":null}` 会变 500，所以"没有"用
  `{"profile":null}` 这类**具名 null 键**表达（`internal/transport/handlers_account_official.go:497-503`），
  这是与官方 wire 的**显式差异**，不是成功。

### 1.3 计数规则：153 / 66 / 13（当前文档与证据不一致的具体项）

- 官方 **153** 是**桌面客户端方法清单**（`docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt`，
  153 行，`grep -c ""` 实测 153）。本文 11 项在其中的分布是
  **19+11+8+7+3+1+4 = 53**。
- 但官方包的 `lib/typert.remote-client.js` **raw remote-client 面**是 **66** 个方法
  （12+20+12+11+4+1+6）。**差 13 个**，逐个点名如下（全部未实现、全部不得静默漏掉）：

| 命名空间 | 153 清单外方法 | 调用形态 | 当前宿主 |
|---|---|---:|---|
| `dynamicCordisRunner` | `dynamicCordisRunner/inventory` | unary | `gateway/unknown-method` |
| `forgePanel` | `forgePanel/changeRequestStates` | stream | 未接线（`gateway/unknown-method` 或 mux 未建） |
| `liveshareShares` | `liveshareShares/capabilities`、`liveshareShares/list`、`liveshareShares/relay`、`liveshareShares/testRelay` | unary | `gateway/unknown-method` |
| `account` | `account/getState`、`account/hasRunningAccountTasks`、`account/watch`、`account/watchExpiry` | unary / stream | `gateway/unknown-method` |
| `pluginMarket` | `pluginMarket/listIndexes` | unary | `gateway/unknown-method` |
| `speech` | `speech/catalog`、`speech/follow` | unary / stream | `gateway/unknown-method` |

> **这是本文档要暴露的第一处不一致**：`docs/official-desktop-api-parity.md:78-287` 只列 153，
> 而官方包 raw 面是 193（HS-00 登记口径）。只看 153 会得出"外部命名空间只有 53 个方法"的
> 错误结论。**处置口径**：153 是桌面契约，53 是其中属于本文的处置面；13 个附加方法
> 同样是未实现缺口，重入时必须一并纳入方法表（否则官方客户端会拿到
> `unknown-method` 而不是 `method-unavailable`，语义不同）。

### 1.4 UI 不伪造总则

1. 任一方法返回 `ok:false` 时，UI **只能**展示错误（code + message + details.hint），
   **不得**用本地默认值、缓存、示例数据补位。
2. 无真值的能力（账户资料、余额、bonus、share 链接、转写文本、diff、市场条目、推荐卡、
   遥测面板）一律 **HIDE** 或空态；空态文案必须说明"本宿主没有该服务"，不得写"暂无数据"
   冒充"查询成功但为空"。
3. 流式方法未接线时**不得开空流**；UI 要显示"未实现"，而不是转圈。
4. 任何"重试"按钮必须对应真实重试路径；没有重试能力时不得渲染。

---

## 2. `dynamicCordisRunner`（11 桌面 + 1 附加）· **DO_NOT_IMPLEMENT**（R6）

### 2.1 官方证据

- 包：`@deepseek-ai/dsh-cordis-host-runner@0.2.2-rc.1`，描述
  "Dynamic package definition registry, host-half sandbox lifecycle, and invoke handler table
  for model-mounted dual-half packages"（`$S/official-evidence/official/dsh-cordis-host-runner/package.json`）。
- 方法（`dsh-cordis-host-runner/lib/typert.remote-client.js`）：
  `getClientCode`、`inventory`、`invoke`、`reportClientGuardFailure`、`reportRenderFailure`、
  `resolveInspectQuery`、`resolveRequestRun`、`runHostHalf`、`settleUserRun`、`stopFromPanel`、
  `syncInspectManifest`、`undefineFromPanel`（**12**；153 清单是其中 11，缺 `inventory`）。
- 语义证据：`runHostHalf` / `invoke` 的 `sourceLocation` 指向
  `packages/extensions/cordis-host-runner/src/index.ts:544`，方法描述为
  "Invoke an active Host method while rejecting stale Client runs" —— 执行体是**宿主内 JS**。
- 红线：`AGENTS.md` R6 "不嵌 JS 运行时、不 fork/vendor dsc"。

### 2.2 官方错误形状

- 无专属码族：宿主半的失败是**普通 `Error`**，gateway 折叠为 `gateway/internal`
  （`dsh-api-gateway/lib/index.js:1704-1725` 的 `rpcFailure` 默认分支）。
- `inventory` 的 `latestRun.error` 是**数据结构**而非错误：
  `{phase: approval|host-load|host-apply|client-load|client-apply|client-render, message, stack?, pluginId, packageId, pluginRunId}`
  （`typert.host.js` 内联 schema）。
- 宿主侧当前 501 文案点名 R6 与方法名（`internal/transport/handlers_media_market_official.go:928-934`）。

### 2.3 缺什么

- 宿主内 JS 执行沙箱、Cordis 生命周期（host-half/client-half 双半）、审批与 render 失败回执。
- **不是**能力欠账：R6 明确禁止，Go-native 实现不得冒充这 11（+1）个方法。

### 2.4 重入条件

**只有用户先修改 R6**（允许宿主嵌 JS 运行时或 fork/vendor dsc）才重立项。重入时必须：
① 独立进程/独立运行时设计 + 资源限额；② 与 R1/R2 的边界证据；③ 12 个方法逐条真实现 + 失败矩阵。
在此之前，任何"已支持动态 Cordis 插件"的 UI 都是伪造。

### 2.5 UI 不伪造要求

- 插件页/动态插件入口 **HIDE**；不得显示"可运行""已加载"。
- 若官方 UI 渲染 `inventory` 行，本宿主必须走 `ok:false` 分支显示未实现，不得造空列表冒充"没有插件"。

---

## 3. PTC runtime（#72 `ptc-runtime` + #73 `workflow-ptc`）· **DO_NOT_IMPLEMENT**（R6）

### 3.1 官方证据

- 包：`@deepseek-ai/dsh-ptc-runtime-node@0.2.2-rc.1`，描述
  "Node PTC execution with async function, REPL and VM Script evaluators and optional retained state"。
- 实现：`dsh-ptc-runtime-node/lib/index.js:24`（`launch.executable` 必填）、`:39`（persistent 必须
  node-script）、`:95`（scriptPersistence 仅 vm-script）、`:466`（control message 跨版本校验）。
- **官方 base 开关**：`dsh-base/cordis.patch.yml:397-398` `ptc-runtime` → `dsh-ptc-runtime-node`，
  **没有 `disabled`**；被禁的是 `:504-506` `tool-executor-ptc`（`disabled: true`）。
- `workflow-ptc`（`:400-403`）是 `provider: spawn` 的 JS 工作流。
- 红线：R6 不嵌 JS 运行时。

### 3.2 官方错误形状

- 无专属码族；普通 `Error` → `gateway/internal`。
- PTC 程序结果里的错误是**数据**：`{kind: 'exception'|'invalid-output'|'output-limit', message}`（`:829` 校验）。

### 3.3 缺什么

- Node VM/REPL 执行、持久状态、输出账本（output ledger）、RPC 背压与限额。
- **纠正历史错误**：不得再写"72 官方默认 disabled"。官方 `ptc-runtime` 无 disabled，
  被禁的是 `tool-executor-ptc`——两者不是同一行。

### 3.4 重入条件

同 §2.4：用户先改 R6。重入需 VM/REPL 执行器、限额（`maxOutputBytes`/`maxPendingCalls`/
`maxOldGenerationSizeMb`）、跨版本 control message 校验与失败矩阵。Go-native workflow 只能自称
JSON 步骤表，**不得**冒充 PTC runtime。

### 3.5 UI 不伪造要求

- `ptc`/`workflow-ptc` 执行入口 HIDE；工具列表不得出现"PTC 可用"。
- 已有 `workflow` 工具若被 UI 展示，必须标注"JSON 步骤表，非 JS 工作流"。

---

## 4. Windows `bash-sandbox`（#42）· **DO_NOT_IMPLEMENT**（win32 官方 disabled）

### 4.1 官方证据

- 开关：`dsh-base/cordis.patch.yml:249-253`
  ```yaml
  - id: bash-sandbox
    name: '@deepseek-ai/dsh-bash-sandbox'
    disabled: !!js process.platform === 'win32'
    config:
      timeoutMs: 60000
  ```
- 同一份 base 在 `:281-283` 连 `tool-bash` 也 `disabled: process.platform === 'win32'`。
- 包描述："confines every command via ctx.sandbox, reports denial/enforcement result facts"。
- 结论：**官方桌面端在 Windows 上没有命令级 OS 沙箱在跑**。本仓在 win32 上做 bash-sandbox
  没有可对齐的真值。

### 4.2 官方错误形状

- 不是错误，是**结果事实**：`result.sandbox = {mode, enforcement, denied}`
  （`dsh-bash-sandbox/lib/index.js:18,30,41,72,88,103-111,145-146`）。
- seam 级拒绝：`SandboxUnavailableError`（`SANDBOX_UNAVAILABLE`）、
  `SandboxUnsupportedPolicyError`（`SANDBOX_UNSUPPORTED_POLICY`）、
  `SandboxInvalidRootError`（`SANDBOX_INVALID_ROOT`）——`dsh-sandbox/lib/index.js:176,343,350`。

### 4.3 缺什么

- Windows 上不需要"缺"：官方本就不启用。若跨平台另评，需要 bash 侧受限启动器与 denial 签名表。

### 4.4 重入条件

- **Windows：不重入**，除非官方恢复 win32 启用并提供可对齐的 runner。
- **非 Windows（Linux/macOS）另评**：需 bwrap/landlock/seatbelt 真实探针 + `enforcement` 事实 +
  失败签名表；不得从 Windows 结论外推"官方也没有沙箱"。

### 4.5 UI 不伪造要求

- 权限三档 UI 在 Windows 上**只能**展示 approval 半档 + "无命令级沙箱"说明
  （`docs/sandbox-policy.md:70-74` 已固定该口径）；不得显示"已沙箱"。

---

## 5. `pwsh/sandbox` runner（#40 `sandbox` + #43 `pwsh-sandbox`）· **POSTPONE**

### 5.1 官方证据

- `sandbox`（#40）：`dsh-base/cordis.patch.yml:240-241` → `@deepseek-ai/dsh-sandbox-local`，
  **总是装配**（无 disabled）；`sandbox-policy`（`:243-247`）注入 `mode` 与 `workspaceRoot`。
- `pwsh-sandbox`（#43）：`:255-257` `disabled: process.platform !== 'win32'` —— **win32 启用、
  非 win32 关闭**。
- 官方 runner 自报**部分强制执行**：`dsh-sandbox-local/lib/index.js:190-195`
  `STATIC_ENFORCEMENT = {bwrap:"full", landlock:"full", seatbelt:"full", "windows-acl":"partial"}`；
  `README.zh.md:134` 明写 `enforcement: 'partial'`。
- Windows ACL 边界：`dsh-sandbox-windows-acl/README.zh.md:127`
  "**写入与删除受限；读取、网络与进程可见性不受限。**"
- 额外可写目录在 Windows 上被拒：`dsh-sandbox-local/lib/index.js:360`（`SandboxUnsupportedPolicyError`）；
  runner 不可用时 `:495` `SandboxUnavailableError`。

### 5.2 官方错误形状

- 同 §4.2：结果事实 `{mode, enforcement, denied}` + 三个 seam 错误码
  （`SANDBOX_UNAVAILABLE` / `SANDBOX_UNSUPPORTED_POLICY` / `SANDBOX_INVALID_ROOT`）。
- 失败必须 **fail-closed**：`dsh-sandbox-local` 的注释明确 "silent unconfined passthrough is forbidden"
  （`dsh-sandbox/lib/index.js:355-358`）。

### 5.3 缺什么

- 受限令牌/ACL runner（Windows）、bwrap/landlock/seatbelt（Linux/macOS）、
  功能探针（probe）、拒绝签名表、额外可写目录策略、清理与回滚。
- 本仓当前只有 policy 三档（`HOST-SANDBOX-INTEGRATION`：policy production DONE scoped，
  整体 **PARTIAL / policy-only**），**没有 OS 隔离**。

### 5.4 重入条件

① `docs/sandbox-policy.md` §5 的 policy 生产验收全绿；② 独立 OS 方案设计（含读/网络/进程
可见性**不受限**的显式声明）；③ 独立探针（不触碰用户文件）与回滚方案；④ 三档各自断言：
只读不写盘、工作区外拒写、全权放行。**不得**拿现有 Job Object 顶替 OS runner。

### 5.5 UI 不伪造要求

- 三档只显示真实 mode；`denied` 必须来自结果事实，不得由工具名猜。
- 未批准时**零进程**（`docs/sandbox-policy.md:88-92`）；UI 不得在拒绝后显示"运行中"。

---

## 6. `forge`（19 桌面 + 1 附加）· **POSTPONE**

### 6.1 官方证据

- 包：`@deepseek-ai/dsh-experimental-client-ui-forge@0.2.2-rc.1`；方法
  `dsh-experimental-client-ui-forge/lib/typert.remote-client.js`：
  `branches`、`cancelWorktree`、`changeRequestChecks`、`changeRequestDiff`、`changeRequestStates`
  （stream，153 外）、`changeRequestText`、`continueInProject`、`dismiss`、`follow`、`localDiff`、
  `localText`、`markReady`、`merge`、`refresh`、`requestWorktree`、`restoreWorktree`、
  `retryWorktree`、`stopCreation`、`submitReview`、`track`（**20**）。
- 依赖服务：`@deepseek-ai/dsh-experimental-forge`（"Provider-neutral change request registry with
  shared status polling"）、GitHub provider、worktree 服务、forgeSession 绑定存储。
- 本仓已逐方法写清 `need`/`note`：`internal/transport/handlers_forge_liveshare_official.go:223-475`。

### 6.2 官方错误形状

- 无专属码族：provider registry 缺 provider 时抛普通 `Error`
  （`dsh-experimental-forge/lib/index.js:402` `forge: <missing message>`）→ gateway 折叠
  `gateway/internal`。
- 本仓 501 文案必须点名缺哪个服务（已在 spec 表 `need` 字段逐条给出），不得只写"未实现"。

### 6.3 缺什么

- code host 客户端（GitHub API + `gh` 登录态）、change request 绑定存储、
  worktree 服务（分支命名 `dsh-<session 前 8 位>-<4 位随机>`）、inspection 调度器、
  headSha 栅栏、评审写的幂等与错误矩阵。
- 本仓 Transport 按 R7 不 import `modules`，连"本机 git"都没有。

### 6.4 重入条件

① 冻结官方 zod 参数/结果与服务凭证模型；② 真实 code host 成功 **+ 失败** 矩阵；
③ 无真值时 UI HIDE。**不得**造 diff/check/review 成功，不得本地合并冒充远端 PR 状态。

### 6.5 UI 不伪造要求

- Forge 面板（PR 状态、checks、merge 按钮）无真值 **HIDE**；
- `checks` 必须是 headSha 上的完整快照，禁止用常量/合并状态编造；
- `follow` 不得开空流（官方是"先当前状态再后续状态"，没有状态机就是假流式）。

---

## 7. `liveshare`（8 桌面 + 4 附加）· **POSTPONE**

### 7.1 官方证据

- 包：`@deepseek-ai/dsh-experimental-liveshare@0.2.2-rc.1`
  "Opt-in LiveShare host bundle: share a Session through a separate Relay at the view or
  bounded-write tier, with forking and attachment uploads as independent switches"。
- 方法（`lib/typert.remote-client.js`，**12**）：`audit`、`capabilities`（153 外）、`create`、
  `identity`、`list`（153 外）、`relay`（153 外）、`setCapabilities`、`setRelay`、`stop`、
  `testPublish`、`testRelay`（153 外）、`updatePermissions`。
- 依赖：relay 地址、owner 凭据（`owner-credential.json` / `LIVESHARE_OWNER_CREDENTIAL_FILE`）、
  OIDC 身份（`LIVESHARE_OWNER_PROVIDER_ID` / `TENANT_ID` / `SUBJECT_ID`）、
  `owner-shares.json` 状态文件。

### 7.2 官方错误形状（**本项码族最完整**）

`lib/index.js:964` 注释："Failures are thrown as `RemoteError` values with a `liveshareShares/` code"。
实有码（逐条 file:line）：

| code | 触发 | 依据 |
|---|---|---|
| `liveshareShares/missing-session-id` | create 缺 sessionId | `:1107` |
| `liveshareShares/relay-url-required` | 未保存 relay 地址 | `:1111` |
| `liveshareShares/relay-upgrade-required` | relay 不绑定 owner 身份 | `:1116` |
| `liveshareShares/owner-login-required` | 无已验证 owner 凭据 | `:1117` |
| `liveshareShares/invalid-ttl` | ttl 越界 | `:1120`、`:1298` |
| `liveshareShares/relay-unavailable` | relay 不可达 | `:1159` |
| `liveshareShares/owner-state-unavailable` | 状态文件写失败 | `:1192,1229,1262,1274,1329` |
| `liveshareShares/share-not-found` | shareId 未发布 | `:1245`、`:1300`、`:1363` |
| `liveshareShares/missing-share-id` | 缺 shareId | `:1295`、`:1361` |
| `liveshareShares/missing-permission` | 无权限/生命周期变更 | `:1297` |
| `liveshareShares/invalid-capabilities` | capabilities 非法 | `:1367` |

### 7.3 缺什么

- relay 服务、owner 身份验证、share 生命周期、capabilities/permissions 授权矩阵、审计源。
- 本仓 8 条 501 文案逐条点名缺什么（`handlers_forge_liveshare_official.go:406-470`）。

### 7.4 重入条件

① 冻结威胁边界（owner/non-owner、peer 身份、relay 生命周期）；② 真实 relay + 凭据 + OIDC；
③ `testPublish` **必须真的发一次**（不得无发送报成功）；④ 上面 11 个码逐个有失败用例。

### 7.5 UI 不伪造要求

- 无 relay 时分享入口 HIDE；**禁止**本地编 `shareId`/链接。
- `testPublish`/`testRelay` 不得显示"可用"；`audit` 不得返回空数组冒充"没有审计事件"
  （空数组语义是"审计过、无事件"，与"无审计源"不同）。

---

## 8. `speech`（4 桌面 + 2 附加）· **POSTPONE**

### 8.1 官方证据

- 包：`@deepseek-ai/dsh-experimental-api-speech-to-text@0.2.2-rc.1`
  "Authenticated experimental speech transcription for browser clients"。
- 方法（`lib/typert.remote-client.js`，**6**）：`cancelPreparation`、`catalog`（153 外）、
  `configure`、`follow`（stream，153 外）、`prepare`、`transcribe`。
- 实现：`lib/index.js:198`（音频大小/编码）、`:213`（推理失败）。

### 8.2 官方错误形状

- `speech/invalid-audio`（details `{reason:"encoding-or-size"}`，`:198`）
- `speech/transcription-failed`（details `{reason}`，`:213`）
- 两者都是 `RemoteError`，走 §1.2 信封。

### 8.3 缺什么

- ASR 模型文件、预处理器/VAD、模型下载源与加载器、音频解码、推理管线、取消语义。
- 本仓 4 条 501 文案逐条点名（`handlers_media_market_official.go:448-515`）。

### 8.4 重入条件

① 冻结官方音频格式/上限/隐私边界；② 真实转写链可用；③ 6 个方法（含 `catalog`/`follow`）
逐条成功+失败。**空 transcript 不算成功**，回显原文冒充翻译/转写同样禁止。

### 8.5 UI 不伪造要求

- 麦克风/转写按钮 HIDE；不得显示"正在转写"的假进度；
- `follow` 是 stream，未接 mux 前不得开空流。

---

## 9. `account`（7 桌面 + 4 附加）· **POSTPONE**

### 9.1 官方证据

- 包：`@deepseek-ai/dsh-api-account-controller@0.2.2-rc.1` "Expose safe account operations over
  authenticated Remote"；base 行 `dsh-base/cordis.patch.yml:120-123` → `dsh-deepseek-account-platform`
  （`desktopPlatform` 仅 darwin/win32 桌面 profile 注入）。
- 153 方法（7）：`ackBonusNotified`、`cancelSignIn`、`getBalance`、`getProfile`、
  `getUnnotifiedBonuses`、`signOut`、`startSignIn`。
- 153 外（4）：`getState`、`hasRunningAccountTasks`、`watch`（stream）、`watchExpiry`（stream）。
- 结果形状：`AccountView = {status: "signed-out"|"credential-stored", links:{usageUrl,topUpUrl}, attempt?}`
  （`dsh-api-account-controller/lib/typert.host.js:317` 等）；`getProfile`/`getBalance`/`getUnnotifiedBonuses`
  是 `T | null`。

### 9.2 官方错误形状

- 无专属码族：控制器失败抛普通 `Error` → `gateway/internal`。
- 状态是**数据**：`status:"signed-out"` 与 `status:"credential-stored"` 必须区分，
  不得把未登录渲染成余额 0。

### 9.3 缺什么

- 官方 DeepSeek Platform 账号服务、OAuth 登录/取消、余额与 bonus 真值、桌面平台凭据。
- 本仓 4 项真实现（`ackBonusNotified`、`getUnnotifiedBonuses`、`signOut`、`llm.discoverModels`
  的 `/models` 发现），其余 6 条 501（`internal/transport/handlers_account_official.go:407-429`；
  501 文案见 `:517-545` 等）。

### 9.4 重入条件

① 真实 OAuth/账号服务与错误语义可用；② 7+4 个方法逐项成功+失败；③ 无账号真值时 UI HIDE。

### 9.5 UI 不伪造要求

- 未登录显示 `signed-out` 空态与登录入口；**禁止**编造头像/昵称/余额/bonus。
- 401 走 `remote-access/denied{action:"request",reason:"unauthorized"}`（`codes.go:152-157`），
  不得显示通用错误。

---

## 10. `pluginMarket` / `pluginRecommendations`（3+1 桌面；+1 附加）· **POSTPONE**

### 10.1 官方证据

- `@deepseek-ai/dsh-plugin-market@0.2.2-rc.1` "Configured plugin market indexes for the Web
  Discover page and the plugin_market agent tool"，方法 `readEntryDetail`、`search`、`setIndexes`
  （+ `listIndexes` 153 外）。
- `@deepseek-ai/dsh-plugin-recommendations@0.2.2-rc.1` "Official plugin suggestions with explicit
  user activation and persistent conversation decisions"，方法 `describe`（1）。

### 10.2 官方错误形状

`plugin-market/*`（`dsh-plugin-market/lib/index.js`）：

| code | 触发 | 依据 |
|---|---|---|
| `plugin-market/invalid-index-url` | 非 http(s)/带凭据/重复 | `:273-290`、`:1159` |
| `plugin-market/settings-unavailable` | 无 Settings 或 profile 条目 | `:1152`、`:1164` |
| `plugin-market/invalid-query` | offset/limit 非法 | `:1179`、`:1183` |
| `plugin-market/unknown-index` | 索引不存在 | `:1175-1176` |
| `plugin-market/fetch-failed` | 取索引失败 | `:1176` |
| `plugin-market/invalid-index` | 索引文档不合法 | `:1176` |
| `plugin-market/unknown-plugin` | 未列出的 plugin/version | `:1231-1232`、`:1238` |
| `plugin-market/detail-unavailable` | 长文档不可得 | `:1232` |

`pluginRecommendations` 无专属码；普通 `Error` → `gateway/internal`。

### 10.3 缺什么

- 市场索引源设置（可写位置 + `plugin-market/changed` 事件）、索引文档取回与 schema 校验、
  模糊匹配/排序/分页、安装状态计算、npm 文件 CDN / Git 提交读取器、推荐卡记录与卡文本目录。

### 10.4 重入条件

① 固定外部夹具与真实服务分层验收；② `setIndexes` 必须真落盘（不得只改本地字符串）；
③ 空市场与"查询失败"必须区分。无真值 UI HIDE，不生成自造推荐卡。

### 10.5 UI 不伪造要求

- 发现页/市场列表无真源时 HIDE；不得展示示例插件；
- 推荐卡不得由本地规则生成冒充官方推荐。

---

## 11. OTel / session-log 上报（#37 `otel` + #38 `session-telemetry-otel` + #8 `session-log-deepseek`）· **POSTPONE**

### 11.1 官方证据

- `dsh-base/cordis.patch.yml:202-203` `otel` → `@deepseek-ai/dsh-otel`
  "Cordis service for independent ordinary-event and byte-bounded Session-log OTLP channels"。
- `:218-233` `session-telemetry-otel` → FEEDBACK_ONLY 默认、`shutdownTimeoutMillis: 3000`、
  `maxRequestBytes: 4000000`、exporter `url: https://dsh-otel-collector.deepseeksvc.com/v1/logs`、
  gzip、`timeoutMillis: 1000`、processor `scheduledDelayMillis: 10000` / `maxQueueSize: 2048` /
  `maxExportBatchSize: 2048` / `exportTimeoutMillis: 1500`。
- 注释约束（同段）：**只有显式用户反馈才释放 Session-log 前缀**，普通活动永不触发；
  `DSH_TELEMETRY_OTLP_URL` 覆盖端点；非空 `DSH_TELEMETRY_DISABLED`（含 `0`/`false`）整体退出；
  Resource 带匿名 `user.id`（`$DSH_HOME/.anonymous-user-id`）；字节有界批量可能拆成多次 HTTP；
  串行发送、每次 1s 传输超时 + 1.5s 看门狗；外层 3s 关停期限会停止排队发送，
  大前缀可能**未发送**。
- `:43-44` `session-log-deepseek` → `@deepseek-ai/dsh-session-log-deepseek`
  "Incremental lossless session-log request extension for the official DeepSeek LLM API"。
- 实现：`dsh-otel/lib/index.js:99`（HTTP ≥300 抛错）、`:201-212`（配置校验）；
  `dsh-session-telemetry-otel/lib/index.js:21,25,42,49,108-119,136`（mode 解析、URL 校验、user.id）。

### 11.2 官方错误形状

- 无 API 方法码族（不是桌面方法）：OTLP 导出失败走**回调 + 日志**
  （`dsh-otel/lib/index.js:144,301,306`），不向调用方抛业务错误。
- 配置非法一律 **fail-loud**（`:108-119` 的 `throw new Error(...)`），不得静默降级。

### 11.3 缺什么

- OTLP 导出器（HTTP + gzip + 批量）、FEEDBACK_ONLY 结算（只有反馈才释放前缀）、
  匿名 user id、字节有界批处理、串行发送与关停期限、`session-log-deepseek` 的增量请求扩展与水位。
- 本仓零 OTel/telemetry 代码（`docs/official-plugins-022-map.md:61-62` 的 grep 结果为 0）。

### 11.4 重入条件

① 产品先确定"上报什么、为什么上报"（当前无上报定位）；② 端点与用户授权（`DSH_TELEMETRY_DISABLED`
语义）冻结；③ 隐私边界与匿名 id 文档化；④ 失败可观测但不影响主链。**未授权前不得采集**。

### 11.5 UI 不伪造要求

- 设置页遥测开关在无真实导出器时 HIDE 或禁用并说明；
- 不得显示"已上报"；`FEEDBACK_ONLY` 未实现时不得把普通活动当反馈上报。

---

## 12. 远程授权 / 远程访问（#12 `remote-access` + #23/24 `authorization`/`deepseek-account`）· **POSTPONE**

### 12.1 官方证据

- `dsh-base/cordis.patch.yml:57-58` `remote-access` → `@deepseek-ai/dsh-api-remote-access`
  "Method-level Remote access control for non-owner Peers and the per-Peer access snapshot"。
- `:117-118` `authorization` → `@deepseek-ai/dsh-authorization`
  "Authorization seam (ctx.authorization): plugin-owned flows that obtain a credential through a
  conversation with the human"。
- 访问决策：`dsh-api-remote-access/lib/index.js:220`（"throws `remote-access/denied` for a denied
  call and drops a denied event"）、`:224`（owner 与 `remoteAccess/follow` 放行）、
  `:270`（owner baseline）、`:354-372`（decide/explain）、`:62-66`（deny 理由）。
- 网关：`dsh-api-gateway/lib/index.js:1217` 抛 `remote-access/denied`
  （details `{action, reason}`）。
- 授权流程错误：`dsh-authorization/lib/index.js` 的 `AuthorizationError` 码
  `DUPLICATE_FLOW`（`:77`）、`NO_FLOW`（`:141`）、`UNKNOWN_METHOD`（`:143`）、
  `ALREADY_IN_FLIGHT`（`:144`）、`CANCELLED`（`:216`）、`NOT_COMMITTED`（`:247-248`）。

### 12.2 官方错误形状

- `remote-access/denied`：details `{action, reason}`；reason 取
  `no-allow` / `policy-failed` / `explain()` 的理由 / 无策略（`:215-220`）。
- 401/403 在本仓翻译成 `remote-access/denied{reason:"unauthorized"|"forbidden"}`
  （`internal/officialwire/codes.go:152-157`）。
- `AuthorizationError` 是**授权流程**错误，不是远程方法错误。

### 12.3 缺什么

- 方法级 RBAC（principal/binding/role/condition）、Peer 概念与每 Peer 快照、
  授权流程 seam（对话式取凭据）、按方法的 allow/deny/follow 策略。
- 本仓只有：launch token → cookie（`internal/transport/server.go:329`）、
  `internal/officialwire/auth.go` 的 Peer 凭据（v1/v2、`interactions=answer`、
  `Auth.Issue`/`Peer`）、以及 `gateway/unknown-method` 之外的码翻译。
  **没有**方法级策略表、没有 `remoteAccess/follow` 事件流、没有条件求值。

### 12.4 重入条件

① 冻结 principal 种类（owner/peer）与资源模型；② 每方法的 action 表与 deny 理由；
③ 授权流程（对话式凭据）与 `NOT_COMMITTED` 语义；④ owner/non-owner 与 follow 的端到端矩阵。
未完成前不得声称"与官方桌面端权限模型一致"。

### 12.5 UI 不伪造要求

- 非 owner 的 Peer 不得看到 owner 专属面板；
- 拒绝必须展示 `reason`；不得把 `policy-failed` 渲染成"无权限"（语义不同：前者是策略求值失败）；
- 授权对话取消/超时必须落到 `CANCELLED`，不得静默成功。

---

## 13. 当前文档 / 证据不一致（先红清单）

以下不是本文的编辑错误，而是**现存文档与官方证据对不上**的具体项，必须由对应 owner 修：

1. **`docs/official-plugins-022-map.md:170`** 写
   "`72 ptc-runtime`（R6 禁 JS，官方 base 默认 disabled）" —— **错误**。
   官方 `dsh-base/cordis.patch.yml:397-398` 的 `ptc-runtime` **没有 `disabled`**；
   `disabled: true` 的是 `:504-506` 的 `tool-executor-ptc`。二者不是同一行。
   任务书 §3.2 先红里 `assert '72 ptc-runtime（R6 禁 JS，官方 base 默认 disabled）' not in mapping`
   就是钉这一条（现仍为红）。
2. **`docs/official-desktop-api-parity.md`（78-287 行）** 只列 153，未说明
   官方 raw remote-client 面在本文 7 个命名空间里有 **66** 个方法；13 个 153 清单外方法
   （§1.3 表）在任何地方都没有处置行。这会让"153 名义全集"被误读成"外部命名空间只有 53 个"。
3. **`docs/architect-official-sync-taskbooks.md` §5** 的计数（forge 19 / Cordis 11 / liveshare 8 /
   account 7 / speech 4 / pluginMarket 3 / pluginRecommendations 1）与 raw 面（20/12/12/11/6/4/1）
   不一致，且未写"这是 153 子集"的口径。本文 §1.3 补上口径，但任务书本身仍应加一句。
4. **`docs/official-plugins-022-map.md:6`** 说 4 行官方默认 disabled
   （`tool-plugin-manager`、`fs-observation-policy`、`tool-executor-ptc`、Windows 的 `bash-sandbox`），
   与 `cordis.patch.yml` 实测一致；但同文件 `:19` 的"沙箱与 telemetry 整族为 0"没有
   区分"官方 Windows 本就没有 bash-sandbox"（§4）与"沙箱整族未做"（§5）。
   两件事结论不同：前者是 DO_NOT_IMPLEMENT，后者是 POSTPONE。
5. **任务书 §1.1** 把账户组记成"4 真实现+6 未实现"，但未点名是哪 4 个；
   本文 §9.3 已点名（`ackBonusNotified`/`getUnnotifiedBonuses`/`signOut`/`llm.discoverModels`），
   任务书可回填。

> 上述 1 属于 `HOST-PLATFORM-022-DISPOSITION` 的写作用域（map 纠错），**HS-15 不越权修改**；
> 2–5 属文档口径回填，本文已给出可引用的结论。

---

## 14. 未验项

- `DOCS_ONLY=1`：本文**没有**跑产品测试、没有编译（Go/TS 均未跑），
  所有"当前宿主行为"均来自基线 `9cfe2f4` 的源码阅读，不是运行证据。
- 13 个 153 清单外方法只核对了官方 `typert.remote-client.js` 与调用形态，
  **没有**发起真实 `POST /api/<namespace>/<method>` 探测；`gateway/unknown-method` 是源码推断。
- 官方 `dsh-experimental-forge-github` / `dsh-plugin-market` 的 606/620 个解包文件里包含
  其嵌套依赖，本文只引用了顶层 `lib/`，未逐个审计依赖包。
- 官方 OTel 端点的可达性与数据接收方（`dsh-otel-collector.deepseeksvc.com`）**未验证**，
  本文只按配置与注释记录。
- `account` 的"4 真实现"只核对了 handler 的 nil 分支与 501 文案，未跑真实账号链路。
- Windows ACL runner 的 partial 语义只按官方 README 与 `STATIC_ENFORCEMENT` 记录，
  本机未跑受限令牌探针。

---

## 15. 复跑与门禁

```bash
export S="C:/Users/Administrator/AppData/Local/Temp/HOST-STABLE-20261107/HS-15"
cd "$S"

# 先红（文档/JSON 缺失或任一项无确定结论、或出现空成功冒充）：EXIT=1
timeout 120 python -B validate-decisions.py > logs/red-validate.log 2>&1; echo "RED_EXIT=$?"

# 后绿（写入本文与 external-decisions.json 后）：EXIT=0
timeout 120 python -B validate-decisions.py > logs/green-validate.log 2>&1; echo "GREEN_EXIT=$?"

# JSON 可解析
python -B -c "import json;d=json.load(open(r'$S/external-decisions.json',encoding='utf-8'));print('JSON_OK items=',len(d['items']))"

# 共享树文档门（只读跑，不改共享树）
cd "D:/DeepSeek Harness/default-workspace/DSH-GO"
timeout 300 python -B scripts/doc-links.py > "$S/logs/doc-links.log" 2>&1; echo "DOCLINKS_EXIT=$?"
```

**期望**：`RED_EXIT=1`（先红必须红）、`GREEN_EXIT=0`、`JSON_OK items=11`、`DOCLINKS_EXIT=0`
（共享树基线 19 引用、缺失 0、语法坏 0）。

---

## 16. 结论

11 个处置项**全部**有确定结论：**3 个 DO_NOT_IMPLEMENT**（R6 两项 + Windows bash-sandbox）、
**8 个 POSTPONE**。**没有任何一项**被写成 DONE，也没有任何一项用空成功/假数据冒充能力。
66 个官方 remote 方法（53 桌面 + 13 附加）逐条落在上述处置下；
13 个附加方法在官方 raw 面上存在，当前宿主返回 `gateway/unknown-method`，
重入时必须一并纳入方法表。

本文是文档处置，**不是产品实现**：`DOCS_ONLY=1 PRODUCT_UNVERIFIED=1`。
