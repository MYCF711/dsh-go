# RC2-B1 · Forge 审阅面 11 方法 + 快照/失败码契约 + 归档清理工作树

> 任务：`task-5`（RC2-B1）· 负责人：`b1-forge` · 完成时间 2026-11-12 05:5x
> 分支/工作树：`.agents/worktrees/dsh-bdb66fbf-95ba`（版本仍是 `0.2.2-rc.2-gobeta.1`，**未改版本号**、未 commit、未 push）
> 官方基线：`0.2.2-rc.2` 解包树 `tmp/GO8-RC2/official-rc2/...`（只读）

---

## 0. 结论先行

| 项 | 结果 |
|---|---|
| 12 个新端点（`forgePanel.*`） | **12/12 可调用**；契约层全部实现（参数 fail-loud + 官方结果形状 + 官方错误码） |
| 其中**真实现**（真 git / 真 GitHub REST） | **8 个**：`changeRequestCommits`、`changeRequestReviewerCandidates`、`requestReviews`、`rerunChecks`、`updateBranch`、`diffBases`、`localCommits`、`worktreeCleanup` |
| 其中**结构化未实现**（官方码 + 501/union，无假数据） | **4 个**：`changeRequestReview`（threads 需 GraphQL）、`changeRequestReviewWrite`（同上）、`changeRequestViewed`（官方语义=**null**）、`setFileViewed`（`forgePanel/action-failed`） |
| 结构变更（快照 8 字段 + rerun + 失败码 + diff 带 head） | **全部落地**，逐条对照见 §3/§4 |
| 归档清理工作树 | **落地**（`plugin:worktree/created` 记录、非 force 删除、`retainOnArchive` 默认关、失败结构化、分支/提交保留） |
| 门禁（隔离副本 `git archive HEAD` + 本任务文件） | `go build ./...` **EXIT=0**；`go vet`（4 包）**EXIT=0**；测试 **1395 PASS / 0 FAIL / 9 SKIP** |
| 需要 Lead 接线的行 | **4 处**（§8）：`serve.go` 两行 + `serve_worktree_api.go` 一个方法 + 归档准入一处 |

**为什么门禁跑在"隔离副本"**：本仓 rc.2 同步期间有 14 个代理**同时写同一棵工作树**（`internal/session`、`internal/fssearch` 等不时处于半写状态），共享树里的 `go build ./...` 红绿与本任务无关。判据环境 = `git archive HEAD`（rc.1 干净基线）+ 只覆盖本任务涉及的文件，脚本留在 `tmp/GO8-RC2/b1-verify.sh`，可复跑。

---

## 1. 改动文件清单

### 新增
| 文件 | 行数 | 内容 |
|---|---|---|
| `internal/transport/handlers_forge_rc2.go` | ~1180 | 12 个端点的 L1 契约层：方法常量/集合、官方结果类型、`OfficialForgeRC2API` 函数表 + `Attach/Detach`、逐方法参数校验与出口、官方 `forgePanel/*` 错误类型 |
| `internal/transport/handlers_forge_rc2_test.go` | ~700 | 10 组测试（含走 `srv.ServeHTTP` 的路由用例） |
| `internal/forge/rc2.go` | 596 | provider 的 rc.2 面：官方失败码 + commits/reviewerCandidates/requestReviews/rerunChecks/updateBranch（真 REST）+ viewed/setViewed/readReview/writeReview（结构化 unsupported） |
| `internal/forge/rc2_test.go` | 498 | 9 个 httptest（不联网）+ 编译期签名锁 |
| `internal/forge/git_rc2.go` | ~430 | **本机 git 真值**：`Executor.ForkPoint/BaseBranches/LocalCommits`（对齐官方 local-diff.ts） |
| `internal/forge/git_rc2_test.go` | ~330 | 9 个真实 git 仓库测试 |
| `internal/worktree/archive.go` | 563 | 归档清理状态机：`ArchiveHost`、`SetArchiveHost`、`ScheduleArchiveCleanup`、`CleanupOf`、inspection 6 条判据、非 force `worktree remove` |
| `internal/worktree/archive_test.go` | 597 | 11 个用例（真实 git，0 skip） |
| `cmd/dshd/serve_forge_rc2.go` | ~470 | 宿主适配器：provider + git 执行器 + worktree 服务 → `transport.OfficialForgeRC2API`，含官方错误码映射 |
| `cmd/dshd/serve_forge_rc2_test.go` | ~300 | 6 组测试（错误码表、真实 git、能力缺失语义） |

### 修改
| 文件 | 改动 |
|---|---|
| `internal/transport/handlers_forge_change_request_official.go` | `ChangeRequestSnapshot` 投影新增 8 必填字段 + 可选 `baseSha`；`ChangeRequestCheck` 新增必填 `rerun`；新增 `OfficialForgeChangeRequestReviewer`/`Issue`/`Repository` 类型 |
| `internal/transport/handlers_forge_liveshare_official.go` | 路由认领：`IsOfficialForgeLiveshareMethod` 增加 rc.2 判据；`serveOfficialForgeLiveshareAPI` 增加 rc.2 分派（**server.go 不用改**） |
| `internal/forge/change_request.go` | `checks()` 填 8 个新字段 + `checks[].rerun`；`ChangeRequestDiff` 必须带 head（不匹配 → `stale_head`）并改走 `compare/{base}...{head}`；新增 `ChangeRequestDiffAtHead` |
| `internal/worktree/worktree.go` | `EventTypeCreated="plugin:worktree/created"`、`Creation`、`CreatedAppender`（Store 可选扩展，不破既有实现）、`Snapshot.Creations`、`State.BaseCommit`、`create()` 落 created 记录 |
| `internal/forge/change_request_test.go` | fixture 加 `compare` 路由与 `base.sha` |
| `tmp/GO8-RC2/b1-verify.sh` | 隔离验收脚本（新增，便于 Lead 复跑） |

---

## 2. 官方依据（file:line）

**随包文档**（`tmp/GO8-RC2/official-rc2-docs/.../docs/upgrade-guide/v0.2.2-rc.1/`）
- `forge-change-request-overview/guide.md:1-26` —— 快照 7 必填概览字段、`ChangeRequestCheck.rerun`、`ForgeActionFailure.unknown`、provider 可选方法
- `forge-pinned-diff/guide.md:1-26` —— `diff(ref, headSha)`、`ForgeFailure.stale`、`threadsTruncated`、`forgePanel/stale-head`
- `forge-review-reads/guide.md:1-19` —— `ForgeFailure.unsupported`、`readReview` 缺失 → `unsupported`
- `archive-worktrees/guide.md:1-21` —— `plugin:worktree/created`、非 force 删除、`retainOnArchive`、Keep worktrees/Restore worktree

**类型权威** `@deepseek-ai/dsh-experimental-forge/src/types.ts`
`ChangeRequestReviewer:77`、`ChangeRequestIssue:95`、`ChangeRequestSnapshot:109-180`、`ForgeFailure:183-193`、`BranchUpdateRequest:218`、`ForgeRerunRequest:226`、`ForgeReviewersRequest:232`、`ForgeReviewerCandidate:238`、`ForgeReviewData:361`、`ForgeReviewWriteResult:404`、`ForgeActionFailure:414-424`、`ForgeCommit:427`、`ForgeCommitList:441`、`ForgeViewedState/Files:454/457`、`ForgeViewedRequest:467`、`ForgeProvider:488-579`

**Remote 语义** `@deepseek-ai/dsh-experimental-client-ui-forge/src/index.ts`
`worktreeCleanup:210`、`diffBases:236`、`localCommits:323`、`changeRequestCommits:384`、`changeRequestViewed:478`、`setFileViewed:500`、`changeRequestReview:527`、`changeRequestReviewWrite:586`、`rerunChecks:648`、`requestReviews:666`、`reviewerCandidates:687`、`updateBranch:708`；
错误映射 `readError:1082`（too-large/stale/其余）、`reviewError:1101`（unsupported→review-unsupported）、`actionError:1139`（stale→stale-head、unknown→action-unknown、其余→action-failed）

**本地 git 语义** `client-ui-forge/src/local-diff.ts`
`forkPoint:115`、`chosenForkPoint:137`、`baseBranches:159`、`localCommits:337`；`parse.ts:parseBaseBranches:47`、`BASE_REF_PREFIXES:36`、`remoteOrder:133`

**typert 描述符**（`dsh-experimental-client-ui-forge/lib/typert.host.js`）
`changeRequestCommits:1167`、`changeRequestReview:1288`、`changeRequestReviewWrite:1374`、`changeRequestViewed:1546`、`diffBases:1649`、`localCommits:1788`、`requestReviews:2132`、`rerunChecks:2251`、`reviewerCandidates:2387`、`setFileViewed:2456`、`updateBranch:2678`、`worktreeCleanup:2763`

**forge 服务的"provider 缺失"语义** `dsh-experimental-forge/src/index.ts`
`act():541-556`（缺失动作 → `{code:'failed'}` → action-failed）、`setViewed:390-397`、`commits:404`（缺失 → undefined → **null**）、`viewedFiles:373`（缺失 → **null**）、`reviewerCandidates:267`（缺失 → `unsupported` → reviewers-failed）、`readReview:335`、`writeReview:354`

**归档清理** `@deepseek-ai/dsh-experimental-worktree/src/`
`archive.ts`（WorktreeReclamation：retainOnArchive、后台 inspection、revision 单调、cleanupOf 等待、unused() 判据）、`removal.ts:inspectRemoval`（6 条判据 + 非 force remove）、`types.ts:WorktreeCleanup:59-68`、`projection.ts:10`（`CREATED_RECORD='plugin:worktree/created'`）、`README.md:39`（逐条行为说明）

---

## 3. 逐方法契约表（12 条）

L1 入参 = 官方 `parameters[].name`（位置参数的 wire 名），本仓 `/api/<method>` 的裸 JSON 对象键就是它。所有方法：未知字段 → 400；缺必填/类型错/枚举错 → 400（`bad_request` 四字段）；错误信封 = `code/message/hint/retryable`。

| # | 方法 | 入参 | 结果（官方形状） | 错误码（本仓 L1 出口） | 实现 |
|---|---|---|---|---|---|
| 1 | `changeRequestCommits` | `sessionId`,`url` | `{headSha, commits[{sha,parent,title,author,authoredAt}], truncated}` **或 null** | 401/502 `forgePanel/diff-failed`；409 `forgePanel/stale-head`；413 `forgePanel/diff-too-large` | **真**（REST `/pulls/{n}/commits`） |
| 2 | `changeRequestReview` | `sessionId`,`url`,`headSha`,`fresh`(bool) | `{headSha, viewer, threads[], truncated, pendingReview, viewerCanReview}` | 501 `forgePanel/review-unsupported`；409 `stale-head`；502 `review-failed` | **结构化未实现**（threads 解析态仅 GraphQL） |
| 3 | `changeRequestReviewWrite` | `sessionId`,`url`,`write`(7 分支 union) | `{ok:true}` 或 `{ok:false, code:"failed"\|"unknown"\|"stale"\|"unsupported", message}`（**200**） | 参数错 400；宿主异常/未确认 → union `unknown` | **结构化未实现**（union `unsupported`） |
| 4 | `changeRequestViewed` | `sessionId`,`url`,`headSha` | `{headSha, files{path→viewed\|unviewed\|dismissed}, truncated}` **或 null** | 409 `stale-head`；502 `viewed-failed` | **null**（官方语义：provider 不保存已读标记） |
| 5 | `diffBases` | `sessionId`,`query`(可空串) | `{automatic{name,worktree}\|null, remote[{ref,name}], local[{ref,name}]}` | 400 `forgePanel/not-a-checkout`；502 其余 git 失败 | **真**（真实 git `for-each-ref`/`merge-base`） |
| 6 | `localCommits` | `sessionId`,`base`(string\|null) | `{headSha, commits[], truncated}` | 400 `not-a-checkout`；422 `base-missing`/`no-fork-point` | **真**（真实 git `log fork..HEAD`） |
| 7 | `requestReviews` | `sessionId`,`url`,`reviewers[{kind,login}]` | `{}`（官方 `z.void`） | 409 `stale-head`；502 `action-failed`/`action-unknown` | **真**（REST `requested_reviewers`，team 拆 slug） |
| 8 | `rerunChecks` | `sessionId`,`url`,`request{headSha}` | `{}` | 409 `stale-head`；502 `action-failed`/`action-unknown` | **真**（REST `/check-runs/{id}/rerequest`） |
| 9 | `reviewerCandidates` | `sessionId`,`url`,`query` | `[{login,name,avatarUrl,suggested}]` | 502 `forgePanel/reviewers-failed` | **真**（REST `/assignees` + 大小写不敏感过滤） |
| 10 | `setFileViewed` | `sessionId`,`url`,`request{path,viewed,headSha}` | `{}` | 409 `stale-head`；502 `action-failed`（能力缺失也是这一码，正文点名） | **结构化未实现**（GraphQL mutation） |
| 11 | `updateBranch` | `sessionId`,`url`,`method`(merge\|rebase),`headSha` | `{}` | 409 `stale-head`；502 `action-failed`/`action-unknown` | **真**（REST `PUT update-branch`） |
| 12 | `worktreeCleanup` | `sessionId`,`revision`(number\|null) | `{revision, status:"scheduled"\|"retained"\|"removed"\|"failed"}` **或 null** | 500 `internal` | **真**（归档清理状态机） |

**三种"没有数据"被显式区分**（这是本轮最容易退化成假成功的地方）：
1. **官方结果允许 null** 的 3 条（commits / viewed / worktreeCleanup）→ 写 `{"result":null}`。`writeJSON` 的空值守卫会把 `{"result":null}` 变成 500，所以这三处显式走 `writeForgeRC2NullResult` 并注释理由（官方语义里 null = "provider 不提供这条读"，是确定的答案，不是静默成功）。
2. **union 结果**（reviewWrite）→ HTTP 200 + `{ok:false,code}`；能力缺失是 `code:"unsupported"`，不是 HTTP 错误。
3. **能力缺失**（review / setFileViewed / reviewerCandidates）→ 501 或 502 + 官方 `forgePanel/*` 码；与"整表没挂"的 `not_implemented` 可分辨。
4. 宿主返回 nil 冒充数据（如 review 结果为 nil）→ **500 internal**，不许当成"没有线程"。

---

## 4. 结构变更对照（任务书 §2）

### 4.1 `ChangeRequestSnapshot` 新增字段（官方 jsonSchema 全部 `required`）
| 官方字段 | 本仓 transport 字段 | 值来源（真实可得性） |
|---|---|---|
| `reviewers` | `Reviewers []OfficialForgeChangeRequestReviewer` | REST：`requested_reviewers` + `requested_teams`（kind=team，login=`org/slug`）+ `/reviews` 已评审者（`verdict` 由最新一条 review 状态映射、`reviewedSha`=`commit_id`）；作者排除；按 login 去重 |
| `reviewersTruncated` | `ReviewersTruncated bool` | `false`（REST 全量返回上述三处） |
| `closingIssues` | `ClosingIssues []OfficialForgeChangeRequestIssue` | `[]`（REST 无 `closingIssuesReferences`，官方 migration 允许 host 不给时用空数组 + false，**不编造 issue**） |
| `closingIssuesTruncated` | `ClosingIssuesTruncated bool` | `false` |
| `branchUpdates` | `BranchUpdates []string` | `pr.mergeable_state=="behind"` 且 open → `["merge","rebase"]`，否则 `[]`（REST 只有 behind 一个"落后"信号；冲突/无权时不给） |
| `canRequestReviews` | `CanRequestReviews bool` | 派生：`state=="open" && token!=""`（无权限位，注释已标注派生） |
| `canRerunChecks` | `CanRerunChecks bool` | 存在 `rerun != nil` 的失败 check 且无 pending |
| `threadsTruncated` | `ThreadsTruncated bool` | `true`（REST 无 review-thread 解析态，`unresolvedThreads=0` 只能当下界；注释禁止把它说成"已统计"） |
| `baseSha`（**可选**） | `BaseSHA *string`（`omitempty`） | `pr.base.sha` |
| `checks[].rerun`（**必填**） | `OfficialForgeChangeRequestCheck.Rerun *string` | 失败族 conclusion 且有 check run id → 十进制 id；其余 `null` |

### 4.2 失败码
| 官方 | 本仓 | 映射 |
|---|---|---|
| `ForgeFailure.code` += `stale` | `forge.FailureStale` | `stale_head` → `stale` → L1 `409 forgePanel/stale-head` |
| `ForgeFailure.code` += `unsupported` | `forge.FailureUnsupported` | `unsupported` → 501（review-unsupported）/ 502（其余）/ **null**（commits、viewed，按官方 provider 缺失语义） |
| `ForgeActionFailure.code` += `unknown` | `forge.ActionUnknown` | `provider_unavailable` → `unknown` → `502 forgePanel/action-unknown`（正文要求"先到 host 核对，别直接重试"） |

### 4.3 diff 必须带 head
- `ChangeRequestDiff`：`params.HeadSHA` 非空且 ≠ 当前 `pr.head.sha` → `ForgeHTTPError{Status:409,Code:"stale_head"}` → L1 若走 rc.2 契约即 `forgePanel/stale-head`。
- 读 patch 改走 `GET /repos/{o}/{r}/compare/{baseSha}...{headSha}`（Accept `application/vnd.github.v3.diff`），不再用 `/pulls/{n}` 的 diff——官方 `forge-pinned-diff` 指南点名的 >300 文件场景因此可读全。
- 新增 `ChangeRequestDiffAtHead(ctx, params)`：`headSha` 必填，语义同官方 `diff(ref, headSha)`。

---

## 5. 归档清理工作树（任务书 §3）

实现位置 `internal/worktree/archive.go`（563 行），语义逐条对齐官方 `archive.ts` + `removal.ts`：

1. **`plugin:worktree/created` 记录**：`create()` 成功后追加（`EventTypeCreated`，data = `{path,branch,baseCommit,repositoryRoot}`）。写入走 `Store` 的**可选扩展** `CreatedAppender`——宿主 store 不实现就不写（旧记录行为不变，既有测试不动）。`Fold` 把它折进 `Snapshot.Creations`（同一 path 去重；旧 `plugin:client-ui-forge/worktree` 的 created **不**产生 Creation，对齐官方"release 前创建的永不被扫"）。
2. **清理准入**：`ScheduleArchiveCleanup(sessionID)` 立刻返回 `scheduled`，inspection 在后台；`CleanupOf(sessionID, revision)` 支持"null 立刻读 / 给定 revision 等结算"。revision 在 Service 生命周期内单调递增。
3. **保留判据**（任一命中 → `retained`）：`retainOnArchive()` 为真（**默认关**）/ 创建者会话不存在 / 创建者沙箱策略只读 / `Unused` 返回 false **或 error**（拿不到答案一律保守保留）/ inspection 不通过。
4. **inspection 6 条**（`archive.go:340/358/376/385/397/414`）：池内路径 + `rev-parse --show-toplevel` 仍是该 root → `worktree list --porcelain -z` 条目含 `branch refs/heads/<b>` 且不含 `detached`/`locked*`/`prunable*` → `symbolic-ref HEAD` 匹配 → `status --porcelain=v1 -z --untracked-files=all --ignore-submodules=none` **为空** → `rev-parse --git-path modules` 不存在（submodule 保留）。
5. **移除**：`git worktree remove -- <path>`（**非 force**，路径内无 `--force`/`prune`）；成功 → `removed`；异常 → `failed`（可能留半个目录），**分支与提交永不删**。
6. **并发**：同一 path 的清理用 per-path 互斥 + 可选 `Host.Exclusive` 包裹 inspection+remove；同一会话重复归档把旧 attempt 标 cancelled。
7. `forgePanel.worktreeCleanup` 由宿主适配器把 `CleanupOf` 结果投影成 `{revision,status}`；没有 worktree 服务 → 官方 **null**。

---

## 6. 先红后绿（证据）

| 层 | 先红（实测，不是编的） | 后绿 |
|---|---|---|
| transport 契约层 | 隔离副本首跑：`TestForgeRC2ParamsFailLoud/未知字段` 误判 200（commits 分支漏调 `params.finish()`）、`TestForgeRC2NullableResults` 得到 500 `empty_result`（typed-nil 指针被 `writeJSON` 空值守卫拦下）、`worktreeCleanup revision 非数字` 收下了字符串 `"2"` | 修 3 处后：`-run ForgeRC2` 全绿；再补 `TestForgeRC2ReachableThroughHTTP`（走 `srv.ServeHTTP`，证明**不改 server.go** 也已可路由：12 个名字全部非 404、未挂实现 → 501，挂了实现 → 200 + `{"result":null}`） |
| forge provider（子代理 A） | ① 新测试 + rc.1 旧代码 → `undefined: ForgeFailureOf / ActionFailureOf / FailureStale…`（build failed）；② 保留 rc2.go 但把 `checks()` 新字段与 diff head 校验退回 rc.1 → `TestSnapshotRC2Fields: baseSha=<nil>`、`TestChangeRequestDiffStaleHead: 期望 409 实际 <nil>` 红 | 30 → 39 测试全绿（含我补的 9 条真实 git） |
| worktree 归档（子代理 B） | ① 只有测试时 build failed（`undefined: Creation/ArchiveHost/CleanupOf`）；② 加壳后 6 个清理用例 FAIL「没有清理尝试」+ `CleanupOfWaitsOnRevision`/端到端 FAIL | 实现后 `-run 'Archive\|Cleanup\|Created'` 12 RUN / 12 PASS / 0 SKIP |
| 本机 git（我） | `diffBases`/`localCommits` 首跑 9 条全红：`context deadline exceeded`（测试助手把 `Executor.Timeout` 留 0 —— 那是"立刻超时"，默认值只在 `NewExecutor` 里填）+ 2 处 fixture 语义错（未配 `origin`、`release` 建在 HEAD 而非 fork） | 修助手与 fixture 后 9/9 绿（真实 git 仓库，Windows git 2.55） |

---

## 7. 验证命令与结果

环境：`export PATH="/c/Program Files/Go/bin:$PATH"`；判据在隔离副本（`bash tmp/GO8-RC2/b1-verify.sh`，= `git archive HEAD` + 本任务 17 个文件）里跑：

```
go build ./...                                       → EXIT=0
go vet ./internal/forge ./internal/worktree ./internal/transport ./cmd/dshd → EXIT=0
go test ./internal/forge/     -count=1 -v            → PASS=39  FAIL=0 SKIP=0  ok  5.9s
go test ./internal/worktree/  -count=1 -v            → PASS=26  FAIL=0 SKIP=0  ok 20.6s
go test ./internal/transport/ -count=1 -v            → PASS=528 FAIL=0 SKIP=0  ok  4.1s
go test ./cmd/dshd/           -count=1 -v            → PASS=811 FAIL=0 SKIP=9  ok 166.9s
                                          合计 1395 PASS / 0 FAIL / 9 SKIP
```

- 9 个 SKIP 全部是 `cmd/dshd` 里**既有**的 Windows/环境相关跳过，与本任务无关（本任务新增用例 0 SKIP）。
- 本任务新增用例：transport 11 组（含 24 个参数子用例）、forge 9、worktree 11、cmd/dshd 6。
- `-race` **UNVERIFIED**：本机无 gcc/cgo（`-race requires cgo`），与全仓口径一致；并发正确性靠 mutex + `TestCleanupOfWaitsOnRevision`（含宿主时钟注入）+ transport 层的并发无关纯函数性。
- 共享工作树里 `go build ./...` 期间出现过别人半写导致的瞬时红（`internal/session`、`internal/fssearch`），**不是本任务引入**；因此以隔离副本为准，且已在本任务包上复核过一次「全树共享树」的 `go build ./internal/forge ./internal/worktree ./internal/transport ./cmd/dshd` = 0。

---

## 8. 接线行（Lead 集成，本轮**未改** `serve.go`）

### 8.1 `cmd/dshd/serve.go`（现有 forge 装配段 822-828 行附近，加 1 行）
```go
// rc.2 Forge 面 12 个方法（internal/transport/handlers_forge_rc2.go）。
// 第二个参数与 worktree 面板共用**同一个** *worktree.Service 实例（清理状态、revision 必须同源）。
srv.AttachOfficialForgeRC2API(newForgeRC2API(func(sessionID string) (string, error) {
    // 与 824 行 newForgeGitAPI 用的会话目录解析器**同源**
    ...
}, worktreeService))
```
- 路由**不需要改** `server.go`：12 个名字已由 `IsOfficialForgeLiveshareMethod` 认领（server.go:588 → `serveOfficialForgeLiveshareAPI` → `ServeOfficialForgeRC2`），已用 `TestForgeRC2ReachableThroughHTTP` 走 `srv.ServeHTTP` 钉住。
- 传 nil worktree 服务时只有 `worktreeCleanup` 退化成官方 `null`，其余 11 条照常工作。

### 8.2 `cmd/dshd/serve.go`（worktree 服务装配，加 1 处）
```go
worktreeService.SetArchiveHost(worktree.ArchiveHost{
    RetainOnArchive: func() bool { return /* Plugins 页 "Keep worktrees when archiving" 开关，默认 false */ },
    Unused:          /* 宿主会话查询：除 archivingSessionID 外，没有未归档会话（含 fork 祖先判据）引用 path */,
    ReadOnly:        func(sessionID string) bool { /* 创建者沙箱策略 == read-only */ },
})
```
`Unused`/`ReadOnly` **必须非 nil**（nil 一律当 `retained`，即不删——保守方向）。

### 8.3 `cmd/dshd/serve_worktree_api.go`（加 1 个方法）
```go
// AppendCreated 写官方 plugin:worktree/created 记录（worktree.CreatedAppender 可选扩展）。
// 不加它 = 生产里没有 created 记录 = 归档永远没有可清理的候选（官方语义：没有事件就不扫）。
func (s *worktreeSessionStore) AppendCreated(sessionID string, creation worktree.Creation) error {
    return s.withWriter(sessionID, func(log *session.Log) error {
        return log.Append(worktree.EventTypeCreated, creation)
    })
}
```

### 8.4 `cmd/dshd/serve_workspace_api.go`（归档准入，加 1 行）+ `internal/transport` 已有 `worktree/cleanup` 事件（可选）
```go
// archiveSession 成功归档后：
_ = worktrees.ScheduleArchiveCleanup(sessionID)   // 立刻返回；inspection 在后台
```
- 清理状态由客户端用 `forgePanel.worktreeCleanup`（`revision` 非 null 时等待结算）轮询/等待。

---

## 9. 未实现项与差距登记（**不许**当成 DONE 的部分）

| # | 项 | 现状 | 说明 / 后续 |
|---|---|---|---|
| 1 | `changeRequestReview`（评审线程读） | **结构化未实现** | 官方用 GraphQL `reviewThreads`；本 provider 是 REST-only。返回 `501 forgePanel/review-unsupported`（官方 `unsupported` 语义），**不返回空线程列表**。要做需引入 GraphQL 客户端（无 `gh` 依赖也要自己拼 query + 分页 + `maxCachedReviewBytes`/`reviewRefreshMs` 缓存语义） |
| 2 | `changeRequestReviewWrite`（reply/resolve/edit/delete/add-pending/submit/discard） | **结构化未实现** | GraphQL mutation；按官方 union 返回 `{ok:false,code:"unsupported",message}`（200）。参数（7 分支 union）已 fail-loud 校验 |
| 3 | `changeRequestViewed` | **官方 null** | GraphQL `PullRequestFile.viewerViewedState`；provider 缺这条读时官方结果就是 null，本层照做 |
| 4 | `setFileViewed` | **502 `forgePanel/action-failed`** | GraphQL `markFileAsViewed`；缺失 → `502 forgePanel/action-failed`（官方 `failed` + "keeps no viewed marks"） |
| 5 | `reviewers[].codeOwner` / `suggested` | 恒 `false` | REST 无 code-owner/建议位（GraphQL only），已注释，不发假 true |
| 6 | `closingIssues` | 恒 `[]` + `truncated=false` | REST 无 `closingIssuesReferences`；官方 migration 允许空数组，**不解析正文猜 "#N"**（会造假 issue） |
| 7 | `threadsTruncated` | 恒 `true` | REST 无解析态，`unresolvedThreads=0` 只能当下界；注释禁止说成"已统计" |
| 8 | `canRerunChecks` | 简化判据（全局无 pending） | 官方按"同一 rerun 身份"分组；REST 侧每个 check run 自带 id，可后续收紧 |
| 9 | 归档清理的**反归档取消** | **未实现** | 官方 `workspace/session-unarchiving` 会取消未准入的移除并等已准入的结算；本实现只在"同一会话再次归档"时把旧 attempt 标 cancelled。`internal/worktree` 目前没有 `CancelArchiveCleanup`；窗口很小（需 inspection 全过 + 恰好反归档），但**是真实差距**，建议 B2 补一个方法并在反归档准入处调用 |
| 10 | `Snapshot.Creations` 无 fork 继承位 | 按"本会话日志里的 created 都算自己创建" | 官方 `owned=false` 用于 fork 继承前缀；宿主若把 fork 继承事件原样写进子会话，需自行过滤（B 已在报告点名） |
| 11 | `checksRollup` 聚合顺序依赖（failure→success 会收成 success） | **rc.1 既有缺陷，未改** | 改它会动 `MergeReady` 语义，需 Lead 裁定；本轮不动 |
| 12 | `internal/transport/handlers_forge_write_official.go` 的 5xx 映射只覆盖 502/503/504 | **rc.1 既有，未改** | 500/501 会落到 `provider_error`；与本轮 12 方法无关（它们走 rc2 层自己的映射），登记待裁定 |

---

## 10. 边界与"没做"的事

- 未改 `cmd/dshd/serve.go`、`internal/transport/server.go`、`AGENTS.md`、`CHANGELOG.md`、`docs/**`。
- 未改版本号（仍是 `0.2.2-rc.2-gobeta.1`）、未 commit、未 push。
- 未动 `internal/worktree/worktree_test.go` 的任何断言（B 明确：实现改动不影响旧用例；新增断言全在新文件）。
- 未降级任何既有能力：12 个名字在**没挂实现**时也返回 501 + 点名缺什么（不是 404、不是空结果）。
- 隔离验收副本 `tmp/GO8-RC2/b1-verify/` 是临时产物，可随时用 `bash tmp/GO8-RC2/b1-verify.sh` 重建（不参与构建）。
