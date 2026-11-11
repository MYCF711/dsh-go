# D2 · 凭据掩码 + JobError 错误码（官方 0.2.2-rc.2）

> 基线：`0.2.2-rc.2`；分支/工作树 `dsh-bdb66fbf-95ba`；共享任务板 `task-13`。
> 官方源码只读镜像：`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/`。
> 研究底稿：`tmp/GO8-RC2/reports/R3-llm-web-account-plugin.md` §0 第 6/10 条、§2.1、R3-G08、R3-G15。

## 0. 结论摘要（≤10 行）

1. **凭据掩码已上线**：`credentials.describe` 对本地存储层（`source=='catalog'`，官方 `source:'file'`）返回 `masked`，算法逐字符复刻官方 `maskCredential()`（含 `hidden<8` 整串、`providerEnd` 无分隔符为 0、按码点计三个边界）。env/dotenv 层不返回；`set`/`unset` 契约不变、unset 后掩码消失。
2. **明文不出 `internal/llm`**：新口是"注入 mask 函数、只回掩码串"，明文只作为 mask 入参在包内出现一次；端到端用例对整份 describe 响应做负向扫描（完整密钥 + 3 段明文片段）。
3. **JobError 落地**：新增包 `internal/jobs`，7 个官方 `JOB_*` 码（顺序/字面值逐字）+ `JobError`（`Code()`/类型名 `JobError`）+ `AbortError`（`wait aborted`）+ `ToolArgsError`（`INVALID_ARGS`）。
4. **真实产生点 4 个**：`JOB_NOT_FOUND`（未知 id）、`JOB_FOREIGN_SESSION`（跨实例 id）、`JOB_LIMIT_REACHED`（`jobs.maxConcurrentJobsPerOwner`）、`JOB_BACKGROUND_DISABLED`（新增部署开关 `DSHD_RUN_IN_BACKGROUND`，等价官方 `enableRunInBackground:false`）。
5. **3 个官方 composition 专有码**（CONTROLLER_UNAVAILABLE / OWNER_NOT_LIVE / OWNER_REGISTRY_UNAVAILABLE）宿主**没有对应条件**——已登记在码表 + 投影表 + 用例里，并在 §3.3 逐条写清为什么，不假装。
6. **不折叠**：`*jobs.JobError` 在两条 wire 面（四字段 unary 面 / 官方 job.* 的 `gateway/*` 流面）各有档位，7 码逐个有投影用例，断言"不许落到 internal / gateway/internal"。
7. `job_output` 的 `timeout_ms` 加官方正值校验（`0`/负数 → `ToolArgsError` + `INVALID_ARGS`，不再静默改成默认值）；等待被 ctx 取消 → `AbortError`（`{name:'AbortError', code:'ABORTED'}`），不再当成超时。
8. 测试：`go test ./internal/jobs ./internal/llm ./internal/transport ./modules/exec/bash` = **862 PASS / 0 FAIL**；`go vet` 四个包 EXIT=0；非 tmp 包 `go build` EXIT=0。

## 1. 任务书路径更正（供账本修订）

任务书写的 `internal/jobs/**` **在仓内不存在**（main 与工作树都没有）。实际结构：

| 任务书假设 | 实际位置 |
|---|---|
| `internal/jobs/**`（job 实现） | `modules/exec/bash/{service,session,tools}.go`（执行层）+ `internal/spi/exec.go`（接口/错误）+ `internal/transport/{handlers_jobs.go,handlers_job_terminal_official.go}`（wire 面）+ `cmd/dshd/serve_terminal_api.go`（官方 job.* 帧机） |
| `cmd/dshd/serve_account_provider.go`（凭据描述） | 与凭据无关（该文件是 account provider 适配）。凭据后端在 `internal/transport/handlers_settings_official.go` 的 `catalogCredentialStore`，装配点 `cmd/dshd/serve.go:804` |
| `internal/transport/handlers_settings_credential_mask.go`（新） | 按任务书新建，成立 |

Lead 已裁定（2026-11-12）：新建 `internal/jobs` 作为码表/错误类型的唯一 Go 归属；允许最小改 `modules/exec/bash/tools.go`（+`service.go` 一行）与 `internal/transport/handlers_jobs.go`、`handlers_job_terminal_official.go`；`internal/session/error_codes.go` 的 7 码表（B3 定稿）**未动**；`internal/spi/**` 与 `internal/agent/parallel.go` **未动**。

## 2. 凭据掩码

### 2.1 官方证据

| 事实 | 官方 file:line |
|---|---|
| `CredentialInfo.masked?: string`（"Provider-generated masked preview of a stored value; never the full value"） | `dsh-credentials/src/types.ts:62-75` |
| `maskCredential()` 算法 | `dsh-credentials-local/src/index.ts:522-530` |
| `describe()` 只在 `source:'file'` 填 `masked`（env 层与 dotenv 层不填） | `dsh-credentials-local/src/index.ts:648-657` |
| 值本身不在任何 describe 投影里 | 同上（`CredentialInfo` 无 value 字段） |

### 2.2 算法（逐字符，实现见 `handlers_settings_credential_mask.go` 的 `officialMaskedPreview`）

```
chars      := []rune(value)                       // 按码点，不是字节/UTF-16 码元
providerEnd := (首个 '-' 或 '_' 的下标) + 1        // 找不到时 findIndex=-1，+1 之后正好 0
hidden     := len(chars) - providerEnd - 5 - 4
if hidden < 8 → strings.Repeat("•", len(chars))   // 整串掩码（长度=码点数）
else          → chars[:providerEnd+5] + "•"×hidden + chars[len-4:]
```

### 2.3 掩码示例（本报告所有示例都是**已脱敏**的合成串，非真实密钥）

| 输入（合成） | 码点数 / providerEnd / hidden | 输出 |
|---|---|---|
| `sk-proj-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890` | 44 / 3 / 32 | `sk-proj-••••••••••••••••••••••••••••••••7890` |
| `sk-abc` | 6 / 3 / -6 | `••••••`（整串） |
| `sk-12345678901234567`（hidden==8 边界） | 20 / 3 / 8 | `sk-12345••••••••4567`（**保留**前后缀） |
| `sk-1234567890123456`（hidden==7） | 19 / 3 / 7 | `•••••••••••••••••••`（整串） |
| `ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn`（无分隔符） | 40 / 0 / 31 | `ABCDE•••••••••••••••••••••••••••••••klmn` |
| `ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789`（下划线） | 40 / 4 / 27 | `ghp_ABCDE•••••••••••••••••••••••••••6789` |

### 2.4 四类必测用例 → 测试名

1. **普通长密钥**：`TestOfficialMaskedPreviewLongKey`（并钉长度守恒：掩码码点数 == 明文码点数）。
2. **短密钥整串**：`TestOfficialMaskedPreviewShortKeyAllMasked`。
3. **边界 8**：`TestOfficialMaskedPreviewHiddenBoundary`（hidden==8 保留前后缀、hidden==7 整串——官方判据是 `<8`）。
4. **非 file 来源**：`TestMergeOfficialCredentialMasksSkipsEnvLayer`（env 层多给的掩码被丢掉）、`TestOfficialCredentialsDescribeEnvLayerHasNoMask`（真 store + `t.Setenv`，`source:'env'` 且**无** `masked` 键）。
5. 追加：`TestOfficialMaskedPreviewNoProviderSeparator`、`TestOfficialMaskedPreviewCodePoints`（多字节按码点）、`TestOfficialCredentialsDescribeReturnsMasked`（端到端）、`TestOfficialCredentialsUnsetClearsMasked`、`TestOfficialCredentialMaskNeverTouchesProcessEnv`（同 ref 双源时 masked 描述 stored 值，且不含任一层明文片段）。

### 2.5 "绝不泄露完整旧密钥"的证据

- 结构上：`masked` 是唯一新字段；`OfficialCredentialInfo` 仍无 value 字段，`ModelCredentialView` 也不回值。
- `internal/llm/credential_mask.go`：`DescribeCredentialMasks(refs, mask func(string) string)`——明文只作为 mask 的**入参**出现，返回值是 mask 的输出；`mask==nil` 返回空表（**不**退化成回明文）；错误串只报 ref 位置与长度（回显 ref 名字会变成泄漏路径，`TestDescribeCredentialMasksNeverReturnsPlaintext` 钉住）。
- 端到端负向扫描：`TestOfficialCredentialsDescribeReturnsMasked` 对整份 describe JSON 断言不含完整密钥、也不含 `DEADBEEF`/`CAFEBABE`/`0123456789` 三段片段。
- 既有单测 `TestOfficialCredentialsSingleTruthWithCatalogUpsert` 已从"只允许三字段"更新为 rc.2 契约"允许四字段（含 masked）+ 不许有 value"，并断言掩码逐字符正确。

## 3. Job 错误码

### 3.1 官方证据

| 事实 | 官方 file:line |
|---|---|
| `JOB_ERROR_CODES` 7 码（顺序即契约） | `dsh-jobs/src/error.ts:16-24` |
| `JobError extends HarnessError{ name='JobError', code }` | `dsh-jobs/src/error.ts:30` |
| `JOB_CONTROLLER_UNAVAILABLE` 抛出点 | `dsh-jobs-local/src/index.ts:217-221` |
| `JOB_LIMIT_REACHED` 抛出点 | `dsh-jobs-local/src/index.ts:232-237` |
| `JOB_OWNER_REGISTRY_UNAVAILABLE` / `JOB_OWNER_NOT_LIVE` | `dsh-jobs-local/src/index.ts:376`、`:380` |
| `JOB_NOT_FOUND` / `JOB_FOREIGN_SESSION` | `dsh-jobs-local/src/index.ts:411`、`:419` |
| `waitAborted()` → `name='AbortError'`、消息 `wait aborted` | `dsh-jobs-local/src/index.ts:121` |
| `timeout_ms` 正值校验（`!finite || <=0` → `ToolArgsError`） | `dsh-tool-jobs/src/index.ts:460-461` |
| `ToolArgsError` = name `ToolArgsError` + code `INVALID_ARGS` + 消息 `invalid arguments: …` | `dsh-tools/src/schema.ts:462-470` |
| `JOB_BACKGROUND_DISABLED` 抛出点（`enableRunInBackground:false`） | `dsh-tool-bash/src/index.ts:525`、`dsh-tool-pwsh/src/index.ts:548` |

### 3.2 码表与宿主产生点

| 官方码 | 官方条件 | 宿主产生点（真实路径） | 用例 |
|---|---|---|---|
| `JOB_BACKGROUND_DISABLED` | 工具配置关掉后台执行 | `DSHD_RUN_IN_BACKGROUND=0` + `run_in_background:true`（`modules/exec/bash/tools.go`） | `TestBackgroundDisabledCarriesJobBackgroundDisabled` |
| `JOB_CONTROLLER_UNAVAILABLE` | jobs 服务在、无 controller 服务该 owner | **无**（见 §3.3） | `TestEveryJobCodeHasTrigger` |
| `JOB_NOT_FOUND` | id 没有对应任务 | `spi.ErrJobNotFound`（id 形状不是 `job-<别的owner>-<n>`） | `TestJobOutputUnknownIDCarriesJobNotFound` |
| `JOB_FOREIGN_SESSION` | 任务属于另一个会话 | `spi.ErrJobWrongInstance`（`job-<别的ownerID>-<n>`） | `TestJobOutputForeignInstanceCarriesJobForeignSession` |
| `JOB_OWNER_NOT_LIVE` | owner Agent 不在了 | **无**（见 §3.3） | `TestEveryJobCodeHasTrigger` |
| `JOB_OWNER_REGISTRY_UNAVAILABLE` | 组合里没有 agent registry | **无**（见 §3.3） | `TestEveryJobCodeHasTrigger` |
| `JOB_LIMIT_REACHED` | owner 并发上限已满 | `jobs.maxConcurrentJobsPerOwner` 超限（`modules/exec/bash/service.go`） | `TestBackgroundLimitCarriesJobLimitReached` |

### 3.3 三个"官方 composition 专有"码的如实登记（未假装实现）

- **JOB_CONTROLLER_UNAVAILABLE**：官方语义是"jobs 服务已组装，但**没有** attached job controller serve 这个 owner"。宿主是单进程 kernel，`spi.Job` 的实现者自己就是 controller（`spi.JobController` 可选；缺 controller 时 `cmd/dshd/jobs_out.go` 走 legacy `Kill` 回落，不报错）。**没有能产生该码的路径**，强造只会是假分支。
- **JOB_OWNER_NOT_LIVE**：官方按 Agent 生命周期判定 owner 是否存活；宿主按 **exec 服务实例**隔离 owner（`OwnerID`），会话结束即杀任务，不存在"owner 不在了但任务还在"的状态。
- **JOB_OWNER_REGISTRY_UNAVAILABLE**：官方靠 agent registry 把 sessionId 解析成 Agent；宿主不靠 registry 解析 owner。

三者仍是**必须存在于码表与投影表里的 wire 契约**（客户端按 code 分支），所以：码表逐字保留、两条 wire 面各有档位、`TestEveryJobCodeHasTrigger` 逐码有用例；只是产生点标为"无"。

### 3.4 两条 wire 面的投影（`officialJobErrorSurface`，`internal/transport/handlers_jobs.go`）

| 码 | HTTP 状态 | 四字段 `code`（unary/任务动作面） | gateway 码（官方 job.* 流面） |
|---|---|---|---|
| `JOB_NOT_FOUND` | 404 | `not_found` | `gateway/session-not-found` |
| `JOB_FOREIGN_SESSION` | 403 | `cross_session` | `gateway/forbidden` |
| `JOB_LIMIT_REACHED` | 429 | `job_limit_reached` | `gateway/service-unavailable` |
| `JOB_BACKGROUND_DISABLED` | 501 | `job_background_disabled` | `gateway/method-unavailable` |
| `JOB_CONTROLLER_UNAVAILABLE` | 501 | `job_controller_unavailable` | `gateway/invocation-unavailable` |
| `JOB_OWNER_NOT_LIVE` | 409 | `job_owner_not_live` | `gateway/context-unavailable` |
| `JOB_OWNER_REGISTRY_UNAVAILABLE` | 501 | `job_owner_registry_unavailable` | `gateway/context-unavailable` |

- 既有 sentinel 分支**全部保留**（`*jobs.JobError` 包着 sentinel，`errors.Is` 仍成立），JobError 分支放在 switch 之前（更精确优先）。
- gateway 码只用官方已存在的词（`gateway/cancelled`、`gateway/context-unavailable`、`gateway/invocation-unavailable` 等在 `dsh-api-gateway/lib/*.js` 里都有），不自造。
- 取消类：`context.Canceled` / `DeadlineExceeded` / `jobs.WaitAborted()` 由 `gateway/internal` 改为 **`gateway/cancelled`**（四字段侧 `internal` → `cancelled`）——"调用方自己走了"不是宿主 bug，官方也把它命名为 AbortError。

### 3.5 `waitAborted()` → 命名 `AbortError`

- 类型 `jobs.AbortError`（Go 类型名即落盘 `name`），消息逐字 `wait aborted`，**不带** JOB_* 码（官方也没有）。
- 真实产生点：`job_output` 的 `wait:true` 等待被 ctx 取消 → 返回 `jobs.WaitAborted()`，结果 `error.info = {name:'AbortError', code:'ABORTED'}`（`TestJobOutputWaitAbortIsNamedAbortError`）。宿主 `spi.Job.Output` 无 ctx 参数，所以在工具层用"100ms 短轮询 + ctx 选择"实现同一语义——取消不再被吞成一次超时。

### 3.6 `timeout_ms` 正值校验

- 只在**显式给出**时校验（官方 `args.timeout_ms !== undefined` 同一条判据；缺省走默认 30000ms）。
- `0` / 负数 → `jobs.ToolArgsError{Violations:["invalid timeout_ms: expected a positive number of milliseconds, got ..."]}`（name `ToolArgsError` / code `INVALID_ARGS`），结果带 `error.info`。
- 用例：`TestJobOutputRejectsNonPositiveTimeout`（0 与 -5 两条）。
- **未改** `job_output` 的 JSON schema（仍是 `"minimum":0,"maximum":120000`）：该 schema 正被 E6/task-20 的工具表对拍重采样（`cmd/dshd/testdata/tools.snapshot.json` 已在改），此处只做行为校验，避免与其冲突；schema 侧建议由 E6 一并收口（官方 schema 对 `timeout_ms` 无边界、校验在 execute 期）。

## 4. 先红证据

- 红测日志：`tmp/GO8-RC2/logs/d2-red.log`（`EXIT=1`）。四个包全部 build failed：
  - `internal/jobs`: `undefined: Code / Codes / CodeBackgroundDisabled …`（包还不存在）
  - `internal/llm`: `store.DescribeCredentialMasks undefined`
  - `modules/exec/bash`、`internal/transport`: 因 `jobs` / `DescribeCredentialMasks` 缺失而 build failed
- 绿测日志：`tmp/GO8-RC2/logs/d2-green.log`（`PASS=862 FAIL=0`，四个包 `ok`）。
- 回归日志：`tmp/GO8-RC2/logs/d2-transport-full2.log`（transport 全量 `EXIT=0`）。

## 5. 改动文件

新增：

| 文件 | 作用 |
|---|---|
| `internal/transport/handlers_settings_credential_mask.go` | `officialMaskedPreview`（官方算法逐字符）、`OfficialCredentialMasker`、`catalogCredentialStore.CredentialMasks`、`mergeOfficialCredentialMasks` |
| `internal/transport/handlers_settings_credential_mask_test.go` | 掩码 4 类用例 + 端到端 + 无泄漏负向扫描 |
| `internal/llm/credential_mask.go` | `DescribeCredentialMasks`（注入 mask、只回掩码、明文不出包） |
| `internal/llm/credential_mask_test.go` | 取值口 4 用例（仅 stored 层、不返回明文、nil mask、unset 后消失） |
| `internal/jobs/error.go` | 7 码表 + `JobError` + `AbortError` + `ToolArgsError` + `From`/`CodeOf` + `ValidateWaitTimeout` |
| `internal/jobs/error_test.go` | 码表逐字/顺序、身份三面、sentinel 映射、每码一条触发、AbortError、timeout 校验 |
| `modules/exec/bash/job_error_codes_test.go` | 真实工具路径的 6 条码/取消/参数用例 |
| `internal/transport/handlers_jobs_error_codes_test.go` | 两条 wire 面投影响应 + 取消→`gateway/cancelled` |

修改：

| 文件 | 改动 |
|---|---|
| `internal/transport/handlers_settings_official.go` | `OfficialCredentialInfo.Masked` 字段（+注释）；`officialCredentialsDescribe` 合并掩码（约 8 行） |
| `internal/transport/handlers_settings_official_test.go` | 单值真值用例的三字段断言 → rc.2 四字段（含 masked 逐字符 + 不许有 value） |
| `modules/exec/bash/tools.go` | `EnvRunInBackground`/`runInBackgroundEnabled`、`asJobError`/`withJobErrorInfo`；shell 后台路径（部署开关 + 上限码 + info）；`job_output`（timeout 校验 + ctx 取消 → AbortError + `waitJobOutput`）；`job_kill` 失败带码 |
| `modules/exec/bash/service.go` | 并发上限错误改为 `jobs.New(JOB_LIMIT_REACHED, …)`（保留原中文文案与数字） |
| `internal/transport/handlers_jobs.go` | `jobs` import；`writeTaskActionError` 先查 JobError；新增 `officialJobErrorSurface` 投影表 |
| `internal/transport/handlers_job_terminal_official.go` | `jobs` import；`writeJTOfficialError`/`officialJobTerminalFault` 先查 JobError；取消 → `gateway/cancelled` / `cancelled`；`officialJobTerminalFaultCode` 增 429/408 两档 |

**未动**（按边界）：`internal/spi/**`、`internal/agent/parallel.go`、`internal/session/error_codes.go`（B3 的 7 码表保持原样）、`internal/transport/server.go`、`cmd/dshd/serve.go`、AGENTS/CHANGELOG/docs。

## 6. 未实现项 / 边界（如实登记）

1. **3 个官方 composition 专有码无宿主产生点**（§3.3）：`JOB_CONTROLLER_UNAVAILABLE`、`JOB_OWNER_NOT_LIVE`、`JOB_OWNER_REGISTRY_UNAVAILABLE`。码表/投影/用例已就位；要真有产生点必须先有 agent registry 与多 controller 的宿主组合模型，不在本任务范围。
2. **`job_output` schema 的 `minimum:0` 未改**：官方 schema 无边界、校验在 execute 期；宿主行为已按官方拒绝 0，但 schema 仍写着 `minimum:0`（模型可能先按 schema 发 0 再拿到参数错误）。该文件正被 E6/task-20 的快照重采样覆盖，建议由 E6 一并收口（改 schema 需同步 `cmd/dshd/testdata/tools.snapshot.json`）。
3. **`job_list` 的 `ConcurrencySafe` 未声明**：按任务书归 D3；`tmp/GO8-RC2/reports/D3-tool-pipeline.md` §待接线表第 5 行仍写着"待接线：`modules/exec/bash/tools.go:466`"。该文件本轮已在我手上，但为避免与 D3/E6 的接线重复，**未动**——需要时是一行 `func (t *jobListTool) ConcurrencySafe(json.RawMessage) bool { return true }`。
4. **AbortError 只覆盖工具层等待**：官方 `jobs.wait()` 的取消同样报 `AbortError`；宿主 `spi.Job.Output` 无 ctx（`cmd/dshd/serve_terminal_api.go` 的帧机在自己的 ctx 上收尾），所以只在 `job_output` 工具与 transport 的错误投影两处落地。给 `spi.Job` 加 ctx 属于 SPI 变更（本轮边界禁止），登记为后续项。
5. **工作树污染（非本任务）**：`tmp/b2scratch/**`、`tmp/GO8-RC2/d3-backup/**` 下留有 `.go` 文件，会进 `go build ./...` 并让整仓构建失败（AGENTS 明令 tmp 下不许留 .go）。本任务未删（属他人产物）：非 tmp 包 `go build` 为 EXIT=0，四个目标包测试全绿。
