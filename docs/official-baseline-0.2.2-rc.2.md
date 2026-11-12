# 官方基线 `0.2.2-rc.2`（宿主同步输入 · 机器可核）

> **本文只回答一件事**：官方从 `0.2.2-rc.1` 到 `0.2.2-rc.2` **变了什么**，以及在宿主侧这些变化落成哪几份账。
> 它不宣布任何"已实现"，状态以 `docs/host-stable-gap-register.md`、`docs/parameter-register.md`、`CHANGELOG.md` 为准。
> 生成方式：解析 asar → 提取 `dsh/node_modules/@deepseek-ai/*` → 逐包 diff → 从 `*/lib/typert.host.js` 的
> `createTypertRemoteInfo` 描述符派生方法契约。**证据文件全部随仓**，脚本在 `tmp/GO8-RC2/`（过程件，不入库）。

## 0. 取证源

| 项 | rc.1 | rc.2 |
|---|---|---|
| 安装目录 | `D:/DeepSeek Harness/Desktop-0.2.2-rc.1` | `D:/DeepSeek Harness/Desktop-0.2.2-rc.2` |
| `resources/app.asar` sha256 | `5fa8a6b21bceb8f3e99e30efa2270efd0cc773a11e42aa0488f804a64b34e637` | `49fd1b087190d594b15c44fe6394c6ffd591200aba76e209b5769498f83076ae` |
| `app.asar` 字节数 | 213,202,918 | 208,137,981 |
| `dsh/desktop-runtime.json` → `release.version` | `0.2.2-rc.1` | `0.2.2-rc.2` |
| `hostProtocolVersion` | **4** | **4**（未变） |
| node / pnpm | 24.21.0 / 11.28.5 | 24.21.0 / 11.28.5 |

> rc.1 的哈希与 `docs/official-desktop-api-parity.md` 记录的逐字一致，说明两次取证的是同一份安装；rc.2 的哈希为本次重算。

## 1. 五个面的硬数字

| 面 | rc.1 | rc.2 | 增量 |
|---|---:|---:|---|
| `@deepseek-ai/*` 包 | 331 | 337 | +13 新增 / −7 删除 |
| 官方 base 插件（`dsh-base/cordis.patch.yml` 的 `- id:`） | 97 | **99** | +`agent-team`、+`tool-agent-team` |
| 官方远程方法（`typert.host.js` 派生） | 193 | **223** | +35 / −5 |
| 方法契约变更（同名方法的参数/结果 schema 有变） | — | **33** | 见 `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md` |
| 客户端 UI 插件（`dsh-client-ui-*` + `dsh-experimental-client-ui-*`） | 65 | **66** | +`dsh-client-ui-account-notifications`、+`dsh-client-ui-agent-team`、−`dsh-experimental-client-ui-agent-team` |

### 1.1 方法面：+35

| 方法 | 包 | 面 |
|---|---|---|
| `account.getNotificationContent` / `getNotifications` / `reportNotificationEvent` | `dsh-deepseek-account` | 账号通知（替代 rc.1 的 bonus 通知两条） |
| `agentTeams.setEnabled` | `dsh-agent-team` | AgentTeam 按会话开关 |
| `cuaAccess.applicationIcons` / `applications` / `enablePersistentPtc` / `openSettings` / `ptcConfiguration` / `requestPermission` / `retryApproval` / `revoke` / `systemStatus` | `dsh-cua` | 电脑操作（CUA）9 条 |
| `forgePanel.changeRequestCommits` / `changeRequestReview` / `changeRequestReviewWrite` / `changeRequestViewed` / `diffBases` / `localCommits` / `requestReviews` / `rerunChecks` / `reviewerCandidates` / `setFileViewed` / `updateBranch` / `worktreeCleanup` | `dsh-experimental-client-ui-forge` | Git Worktree 审阅（其中 `worktreeCleanup` 属清理） |
| `liveshareShares.addSessions` | `dsh-experimental-liveshare` | 共享会话增补 |
| `mcpManager.probe` / `restart` / `watch` | `dsh-mcp-manager` | MCP 服务管理 |
| `officeKit.generation` / `render` | `dsh-office-kit` | Office 生成/渲染（替代 `officeToPdf.*`） |
| `subagents.interruptByUser` | `dsh-subagent` | 用户中断子代理（替代 `interruptByParent`） |
| `terminal.watch` | `dsh-api-terminal-controller` | 终端观察（面板接管） |
| `web.listProviders` | `dsh-web` | 搜索/抓取 provider 枚举 |
| `workspaceFiles.search` | `dsh-api-workspace-files` | 工作区文件搜索（内置 ripgrep） |

### 1.2 方法面：−5

| 方法 | 原因 |
|---|---|
| `account.ackBonusNotified` / `account.getUnnotifiedBonuses` | 账号控制器包被 `dsh-deepseek-account` 取代，改为通知三件套 |
| `officeToPdf.generation` / `officeToPdf.render` | `dsh-office-to-pdf` 删除，改 `officeKit.*` |
| `subagents.interruptByParent` | 改 `subagents.interruptByUser`（中断主体从父代理改用户） |

### 1.3 包面

新增（13）：`dsh-agent-team`、`dsh-client-ui-account-notifications`、`dsh-client-ui-agent-team`、`dsh-computer-use`、
`dsh-cua`、`dsh-cua-bundle`、`dsh-error`、`dsh-mcp-manager`、`dsh-office-kit`、`dsh-terminal-panel`、
`dsh-tool-agent-team`、`dsh-tool-office`、`dsh-util-ripgrep`。

删除（7）：`dsh-api-account-controller`、`dsh-deepseek-account-platform`、`dsh-experimental-agent-team`、
`dsh-experimental-agent-team-profile`、`dsh-experimental-client-ui-agent-team`、`dsh-experimental-tool-agent-team`、
`dsh-office-to-pdf`。

> 读法：`dsh-experimental-*` 的 agent-team 三件套 + `dsh-api-account-controller` + `dsh-office-to-pdf` 都被
> **正式包**取代（`dsh-agent-team` / `dsh-tool-agent-team` / `dsh-deepseek-account` / `dsh-office-kit`），
> 不是能力消失。真实能力增量集中在 **AgentTeam、CUA、Office、MCP 管理、文件搜索、终端面板** 六处。

## 2. `dsh-base` 组合变化（默认值层面，不是只有新增插件）

`dsh-base/cordis.patch.yml`（rc.1 → rc.2）除新增两条插件外，还有 3 处**行为默认值**变化：

1. **账号插件改名**：`@deepseek-ai/dsh-deepseek-account-platform` → `@deepseek-ai/dsh-deepseek-account`（`- id: deepseek-account` 不变）。
2. **新增 AgentTeam 两条**：`agent-team`（`config: { maxTasks: 256 }`）与 `tool-agent-team`。
3. **web_search / web_fetch 默认 provider 改变**：
   - rc.1：`searchProvider: deepseek-official`（Messages 检索）+ `fetch` 默认匿名 http，另有 `apiKeyEnv: DEEPSEEK_API_KEY` 配置块；
   - rc.2：`searchProvider: deepseek-search-api`；fetch 仍默认匿名 `http`；`apiKeyEnv` 配置块删除，
     改为「跟随会话路由」：deepseek-account 路由发账号令牌，否则发对应 DeepSeek API-key 路由的密钥；
     新增 `searchBaseURL` / `searchApiKeyEnv`（fetch 侧 `fetchBaseURL` / `fetchApiKeyEnv`）用于把某能力钉到独立端点。
   - 另：`web_search` 的服务端路由在组合里给到 **60s**（provider 中性的 tool 默认仍是 30s）——与 rc.1 一致，不因这次改动变化。

## 3. 与 rc.1 基线的账本对应关系

| 账本 | rc.1 口径 | rc.2 要做的动作 |
|---|---|---|
| `docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt`（193） | 方法全集 | **不改**（历史基线）；rc.2 全集见 `…-rc.2.txt`（223） |
| `docs/evidence/official-base-plugins-0.2.2-rc.1.txt` 不存在（旧清单在 `docs/official-plugins-0.2.2-rc.1.md` 表里） | 97 插件 | rc.2 id 全集已落 `docs/evidence/official-base-plugins-0.2.2-rc.2.txt`（99） |
| `docs/evidence/official-ui-plugins-0.2.2-rc.1.txt`（65） | UI 插件 | rc.2 版 `…-rc.2.txt`（66） |
| `docs/host-stable-gap-register.md` | 193 方法 + 97 插件逐条状态 | 按 223/99 重开一轮（RC2 方法/插件逐条状态），旧表保留为 rc.1 证据 |
| `docs/parameter-register.md` | 官方 342 条覆盖轴 | 以 rc.2 官方源码重导出（锚点+默认值），再重判覆盖率 |
| `docs/official-desktop-api-parity.md` | rc.1 方法/插件/传输对照 | rc.2 增量追加（wire 主版本仍 4 是最重要的兼容结论） |

## 4. 版本号方案（用户 2026-11-12 指令）

- **途中版本**：`0.2.2-rc.2-gobeta.<N>`；**稳定版**：`0.2.2-rc.2-go.<N>`。
- 唯一出处仍是 `cmd/dshd/main.go` 的 `version` 与 `cmd/dshtui/brand.go`（同值，`scripts/build.sh` 用同一个 `VER` 注入）。
- 该形态要求 `cmd/dshtui/brand_test.go`（现只认 `-go.`）与 `cmd/dshtui/internal/vsinfo`（只解析 `-go.`）同时支持 `-gobeta.`，
  否则 `-gobeta.N` 的产物 VS 版本资源会退化成整串；这属于**版本形态适配**，见执行方案 §S0。

## 5. 落地状态（2026-11-12 本轮交付，机器可核）

> 判据一律**机器生成**，不手抄：方法面落点见 `docs/evidence/official-method-coverage-0.2.2-rc.2.md`，
> 参数面见 `docs/parameter-register.md`（rc.2 基线，405 条），门禁见下。

| 面 | 结论 | 证据 |
|---|---|---|
| 远程方法 223 | **223/223 都有生产落点**（0 MISSING / 0 TEST_ONLY）。其中 `workspace.rename` 在盘点里被标成"落点标注未实现"，那是**未挂 API 时的结构化 501 兜底**，生产路径走 `officialWorkspaceHost.renameWorkspace` | `docs/evidence/official-method-coverage-0.2.2-rc.2.md` |
| 新增 35 方法 | 全部有结论：真实现 27；另有 4 条 Forge 审阅（`changeRequestReview`/`changeRequestReviewWrite`/`changeRequestViewed`/`setFileViewed`）按官方形状**结构化未实现**（threads/viewed marks 仅 GraphQL，REST 拿不到）；CUA 9 条在 Windows 上 7 真做 + 2 有条件真做 | 各任务报告 `tmp/GO8-RC2/reports/*.md` |
| 删除 5 方法 | 全部保留为**兼容别名**并标 `Deprecated`（能力下限：只能更多不能更少），内部转发到官方新方法 | 各域代码注释 + 报告 |
| 结构契约 33 条 | 逐字段对齐（含 `WebTerminalInfo` 三字段、`ChangeRequestSnapshot` 7 概览字段、`BundleInfo.update` 三态、`credentials.describe.masked`、JobError 7 码、`error.info{name,code}`） | `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md` |
| base 插件 99 | 新增 `agent-team` / `tool-agent-team` 已实现（`agentTeams.setEnabled` + 任务板 + 按名寻址）；其余 97 个沿用 rc.1 状态 | `docs/evidence/official-base-plugins-0.2.2-rc.2.txt` |
| 参数 405 条 | 官方轴 342 保留 + rc.2 新增 63；覆盖率轴 `COVERED 76 / COVERED-EQUIV 230 / N-A 49 / DEFERRED 8 / OFFICIAL_UNSUPPORTED 18 / UNREAD-GAP 24`（24 条都写了缺什么与谁补） | `docs/parameter-register.md`、`docs/evidence/param-register-rc2-stats.txt` |
| 门禁 | `go build ./...` = 0、`go vet ./...` = 0、`go test ./... -count=1` = 0（89 包）；参数两闸 `validate_register.py` / `audit_refs.py` = 0 | 本轮实跑 |

### 5.1 未收口的已知项（不虚报）

> 2026-11-12 第二轮（gobeta.1 之后的收口轮）关掉了前三项，见 §5.2。

1. ~~**GraphQL 专有能力**~~ → **已关闭**：`internal/forge/graphql.go` 补了最小 GraphQL 客户端（零新依赖），
   `changeRequestReview` / `changeRequestReviewWrite` / `changeRequestViewed` / `setFileViewed` **4 条全部真做**；
   真机只读验收：对 `cli/cli#15001` 读到 2 条 review thread（真实行号/评论/`viewerCanReview`）与 5 个文件的 viewed 状态，
   换 head 后返回官方 `stale`。写路径（7 个 mutation）只有离线桩证据——**写会改远端，本轮不做真机写入**，
   这是明确的未取证项。
2. **CUA 的 AX/窗口控制**：官方在 Windows 上没有任何 CUA provider（`dsh-cua-bundle` 只挂 darwin），宿主如实报告平台不支持；能做的（系统状态、应用决策、图标、设置页、审批状态机）都是真的。
3. ~~**`retryPolicy` 只读**~~ → **已关闭**：新增 `internal/settings` 命名空间存储（revision 栅栏 + 原子写 + 损坏 fail-loud），
   `settings.mutate`/`form.set|reset` 可写 `llm.providers.<route>.retryPolicy` 且**下一轮真生效**；
   假网关计数证明 `maxRetries=1 → 2 次请求`、`maxRetries=3 → 4 次请求`（口径：官方式 `maxRetries`，尝试次数 = +1）。
   8 类非法值 → 四字段错误 + Hint；`expectedRevision` 过期 → 409 `revision_conflict`。
4. **`workspaceFiles.search.graceMs`**：官方是 ripgrep 子进程的宽限时间；宿主是纯 Go 实现（不启子进程），该配置没有对应层，登记为 `OFFICIAL_UNSUPPORTED`。
5. ~~**`-race` 未跑**~~ → **已补跑（2026-11-12 07:4x）**：MinGW 在 `C:/toolchains/mingw64`（不在 PATH 里，子代理一律报 "gcc not found"）。用
   `export PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH"; CGO_ENABLED=1 go test -race ./... -count=1`
   全量通过：**89 包 ok / RACE=0 / 0 FAIL**（日志 `/tmp/g8-race.log`）。后续门禁必须继续用这条命令，别再用"本机无 gcc"当理由。
6. ~~**反归档取消**~~ → **已关闭**：`workspace/session-unarchiving` 事件已实现并在 WS/journal 可见（真 `/ws` 收到帧）；
   与归档清理状态机协同：checkout 已被清理 → 走官方 `worktree.Service.RestoreWorktree`，成功打印重建行、
   失败 500 且 message 含真实目录/git 错误/当前归档集，可 `forgePanel.restoreWorktree` 重试（不静默成功）。
   **取消语义已补齐**（第三轮）：`internal/worktree` 新增 `CancelArchiveCleanup`，逐条对齐官方 `archive.ts:87-93`
   （无 attempt → 置 `cancelled` → `await done` → 收走 attempt），置位与两处准入判据共用同一把锁 →
   **取消与准入不可能同时为真**；真 git 端到端三条：取消成功（目录/`worktree list`/分支全不变）、
   取消来晚（已删 → `RestoreWorktree` 恢复，注册回 1 条且 HEAD 是原分支）、来晚+恢复失败（500 带真实
   `fatal: … is already used by worktree`）。

### 5.2 第二轮收口（2026-11-12 07:4x–07:5x）

| 缺口 | 结论 | 证据 |
|---|---|---|
| Forge 审阅面（4 方法） | ✅ 真做（GraphQL 客户端，零新依赖）；真机只读已取证 | `tmp/GO8-RC2/reports/F1-forge-graphql.md` |
| `retryPolicy` 写入面 | ✅ 可写并真生效（重试次数实测变化） | `tmp/GO8-RC2/reports/F2-retrypolicy-write.md` |
| `workspace/session-unarchiving` | ✅ 事件 + 工作树恢复协同（WS 可见） | `tmp/GO8-RC2/reports/F3-unarchive-event.md` |
| `-race` 全量 | ✅ **MinGW 路径下 90 包全绿 / RACE=0 / 0 FAIL**（第一轮 89 包 + 本轮新增 `internal/settings`）。第三轮（G1/G2 落地 + 两条误报修复）后**再次全量复跑仍为 RACE=0 / 90 包** | `/tmp/g8-race.log`、`/tmp/g8i-race.log`、`/tmp/g8l-race.log`（命令见下） |

### 5.2b 第三轮收口（2026-11-12 08:3x）

| 缺口 | 结论 | 证据 |
|---|---|---|
| `CancelArchiveCleanup`（归档清理取消） | ✅ 官方语义对齐 + 真 git 三条端到端 | `tmp/GO8-RC2/reports/G1-cancel-cleanup.md` |
| Forge `unresolvedThreads`/`threadsTruncated` REST 下界 | ✅ 改 GraphQL 真值；与 `changeRequestReview` 共用同一份 thread 读取（查询文档逐字相同）；降级时只允许 `0+true`，绝不允许 `0+false` | `tmp/GO8-RC2/reports/G2-checks-threads.md` |

真机取证（`cli/cli#15001`，head `ebec7950…`）：`unresolvedThreads=0`、`threadsTruncated=false`（9 checks / rollup success），
与 `gh api graphql` 独立核对的 `reviewThreads.totalCount=2`（全 resolved）一致。

### 5.3 发布链路已验证（2026-11-12 08:2x，未改版本号）

`bash scripts/build.sh` 在 `0.2.2-rc.2-gobeta.1` 下**实跑通过**（这是 `-gobeta.` 形态第一次走完整发布链）：

| 产物 | 大小 | 自检 |
|---|---|---|
| `dist/windows-amd64/dsh-0.2.2-rc.2-gobeta.1.exe` | 21 MiB（≤25 MiB 规则内） | `DSH GO 0.2.2-rc.2-gobeta.1` / commit `731f247` |
| `dist/linux-amd64/…`、`dist/darwin-arm64/…` | 20 / 19 MiB | 交叉编译通过 |
| `dist/windows-amd64/DSH TUI GO.exe`（及另两平台） | 7.6 MiB | `DSH TUI GO 0.2.2-rc.2-gobeta.1 (731f247)` |

- 8 行 `dist/SHA256SUMS`（6 个平台产物 + 2 份无平台目录副本），`dist/` 已被 `.gitignore` 忽略，不入库。
- `.syso` 版本资源随 `build.sh` 重新生成（`gobeta.1`），已提交保持一致（提交 `89208b4`）。
- 也就是说：**要出稳定版只需改版本号再跑同一条命令**，链路本身不再是未知数。

`-race` 的正确命令（MinGW 不在 PATH，子代理会误报 "gcc not found"）：

```bash
export PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH"
CGO_ENABLED=1 go test -race ./... -count=1
```
