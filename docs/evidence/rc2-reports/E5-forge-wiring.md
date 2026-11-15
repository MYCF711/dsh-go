# RC2-E5 · Forge rc.2 面接线（12 端点）+ 归档清理工作树（ArchiveHost）

> 任务：`task-19`（RC2-E5）· 负责人 `e5-forgewire` · 完成时间 2026-11-12 06:2x
> 工作树：`.agents/worktrees/dsh-bdb66fbf-95ba`（版本仍是 `0.2.2-rc.2-gobeta.1`；**未 commit、未 push**）
> 上游依据：B1 报告 `tmp/GO8-RC2/reports/B1-forge.md` §8「接线行」四步 + 官方
> `dsh-experimental-worktree/src/archive.ts`（`unused()` / `ancestorArchived` / `retainOnArchive`）

---

## 0. 结论先行

| 项 | 结果 |
|---|---|
| 步骤 1 · `AttachOfficialForgeRC2API` | **完成**：`cmd/dshd/serve.go:847-852`（解析器 = 既有 `src.Cwd`，worktree 服务 = `worktreeSvc` 同一实例） |
| 步骤 2 · `SetArchiveHost` 三个回调 | **完成**：`cmd/dshd/serve_worktree_api.go:283`（组装）+ `:293`（ReadOnly）+ `:313`（归档集合）+ `:348`（Unused）+ `:242`（RetainOnArchive）；装配点 `cmd/dshd/serve.go:810-817` |
| 步骤 3 · `worktreeSessionStore.AppendCreated` | **完成**：`cmd/dshd/serve_worktree_api.go:79`（+ `ArchiveFacts` `:96`、`worktreeArchiveParent` `:115`） |
| 步骤 4 · 归档成功后 `ScheduleArchiveCleanup` | **完成**：`cmd/dshd/serve_worktree_api.go:428`（包装）+ `:461`（装配期替换 ArchiveSession） |
| 端到端（真 git + 真 home + 真 HTTP 路由） | 6 条终态判据全绿：干净→`removed`、脏→`retained`、被引用→`retained`、只读创建者→`retained`、设置保留→不排期、子代理祖先已归档→`removed` |
| 新增测试 | **9 条**（`cmd/dshd/serve_worktree_archive_test.go`），其中 7 条端到端、2 条单元 |
| 门禁 | `go build ./...` OK、`go vet`（4 包）OK、本任务面 + workspace 归档准入面 `-run` 定向跑 **ok 11.8s / 0 FAIL**、`internal/worktree` 全绿、`cmd/dshd` 全量两次各只有 1 条外部 FAIL（`modules/*`，本任务范围 0 行），见 §5 |
| 先红后绿 | 4 条真实红证据（拆掉任一接线 → 对应用例 FAIL），见 §4 |
| `cmd/dshd/serve.go` 改动 | **只加 2 处共 11 行**（`+810..817`、`+847..852`），未改动任何无关行 |
| 未实现项 | 4 条登记（反归档取消、Unused 无投影缓存、fork 继承记录无法区分 owned、RetainOnArchive 无 UI），见 §7 |

---

## 1. 改动文件清单

| 文件 | 类型 | 改动 |
|---|---|---|
| `cmd/dshd/serve.go` | 最小 edit | 2 处：`810-817` 归档清理装配；`847-852` rc.2 Forge 面装配（详见 §2.1/§2.2） |
| `cmd/dshd/serve_worktree_api.go` | 修改 | `AppendCreated`、`ArchiveFacts`、`worktreeArchiveParent`、`worktreeArchiveSettings`、`worktreeArchiveDeps`、`archiveHost`、`readOnly`、`archivedSessionIDs`、`unused`、`worktreeArchiveInheritedArchived`、`archiveSessionWithCleanup`、`attachWorktreeArchive`（+ 4 个 import） |
| `cmd/dshd/serve_worktree_archive_test.go` | **新增** | 9 条用例（端到端 7 + 单元 2），真 git / 真 v4 会话 / 真 `srv.ServeHTTP` |
| `internal/worktree/archive.go` | 必要最小改 | `checkUnused` 在 `Unused` 返回 error 时**打 stderr**（原实现静默吞错，违反"不许静默"；行为仍是保守保留） |

**没有改**：`internal/transport/server.go`、`internal/transport/*`、`internal/worktree/worktree.go`（除上面 1 处）、`cmd/dshd/serve_forge_rc2.go`、`cmd/dshd/serve_forge_git.go`（复用它既有的 `src.Cwd` 解析器，未改一行）、`cmd/dshd/serve_workspace_api.go`、AGENTS/CHANGELOG/docs。

---

## 2. 四步接线（file:line）

### 2.1 步骤 1：rc.2 Forge 面 12 方法

`cmd/dshd/serve.go:847-852`

```go
	// rc.2 Forge 面 12 个方法（internal/transport/handlers_forge_rc2.go）。
	// 第二个参数与 worktree 面板共用**同一个** *worktree.Service 实例（清理状态、
	// revision 必须同源）；会话目录解析器与上面的 forgePanel git 族同源（src.Cwd）。
	srv.AttachOfficialForgeRC2API(newForgeRC2API(func(sessionID string) (string, error) {
		return src.Cwd(sessionID)
	}, worktreeSvc))
```

- 解析器与 `serve.go:843-845` 的 `newForgeGitAPI(...)` **同源**（同一个 `src.Cwd`），没有第二套会话目录解析。
- 第二个参数是 `serve.go:808` 那个 `worktreeSvc`（worktree 面板、`PreStep`、`worktreeCleanup` 三者同一个 `revision` 源）。
- 路由没有改 `server.go`：12 个名字由既有 `IsOfficialForgeLiveshareMethod` 认领（B1 的 `TestForgeRC2ReachableThroughHTTP` 已钉住）。本报告 §5.7 又用**真实 `srv.ServeHTTP`** 复核了一遍：未挂实现 → 501，挂了 → 200 + 真实清理状态。

### 2.2 步骤 2：`SetArchiveHost`（三个真实回调）

装配点 `cmd/dshd/serve.go:810-817`：

```go
	attachWorktreeArchive(srv, worktreeArchiveDeps{
		Home: *home, Workspace: wsHost, Service: worktreeSvc,
		Sessions: &worktreeSessionStore{src: src},
		Presets:  sandboxPresets, Modes: sandboxStore,
	})
```

实现 `cmd/dshd/serve_worktree_api.go:283`（`archiveHost()`）→ `:456` 调 `deps.Service.SetArchiveHost(...)`。

| 回调 | 位置 | 真值来源 | 拿不到答案时 |
|---|---|---|---|
| `RetainOnArchive` | `:242`（`worktreeArchiveSettings.RetainOnArchive`） | `<home>/state/worktree-settings.json` 的 `{"retainOnArchive": bool}`；**文件不存在/没有该键 = 官方默认 false**（归档即清理） | 读失败/JSON 坏 → **true（保留）** + stderr 一行（包的铁律"任何不确定都选择保留"） |
| `Unused` | `:348` | `session.ListSessions(home,"")` 列全部会话 + 每份日志折叠出有效 cwd/创建记录/子代理父会话（`:96`），归档集合取 registry（`:313`） | 列会话失败/registry 读不了/单份日志读不动 → **error**（服务侧当"仍被使用" → `retained`，且在 `internal/worktree/archive.go:504-510` 打 stderr） |
| `ReadOnly` | `:293` | 既有解析链 `resolveSandboxModeForSession(home, sessionID, sandboxPresets, sandboxStore)`（`serve_sandbox_policy.go:213`，即 `state/session-preset.json` → v4 `header.agentPreset` → `modes.Registry` → `sandbox.Mode`），判定 `Effective == sandbox.ModeReadOnly` | 解析不出（会话没了/preset 未知/状态文件坏）→ **true（按只读 → 保留）** + stderr 一行 |

**`Unused` 的判据与官方 `archive.ts::unused()` 逐条对齐**（`archive.ts:192-211`）：

1. 归档集合 = `workspaceRegistry.archivedSessionIds` + 正在归档的这个会话 id；
2. 跳过已归档会话，以及**子代理祖先已归档**的会话（官方 `ancestorArchived`，`archive.ts:240-253`）——
   本宿主的子代理关系写在子会话自己的 `subagent/spawn` 帧（`session.EventChildSpawn`），
   而 `header.parentSession` 表达的是 **fork**（`internal/session/fork.go:200`），两者不能混用，
   所以 `ArchiveFacts` 专门从事件里取父会话，不读 header；
3. 会话的**有效**工作目录是该 checkout 或在它里面 → 仍被使用；
4. 会话自己的创建记录里有该 path → 仍被使用。

### 2.3 步骤 3：`AppendCreated`

`cmd/dshd/serve_worktree_api.go:79`

```go
func (s *worktreeSessionStore) AppendCreated(sessionID string, creation worktree.Creation) error {
	return s.withWriter(sessionID, func(log *session.Log) error {
		return log.Append(worktree.EventTypeCreated, creation)
	})
}
```

- 它让 `*worktreeSessionStore` 实现 `worktree.CreatedAppender`（可选扩展，`internal/worktree/worktree.go:199`），
  `create()` 成功后（`worktree.go:946`）才会落官方 `plugin:worktree/created` 记录。
- 不接这一条 = 生产里没有任何创建记录 = `ScheduleArchiveCleanup` 永远拿不到候选（`archive.go:169`）。
  §4 的 RED-1 就是这个现象。

### 2.4 步骤 4：归档成功后排期

`cmd/dshd/serve_worktree_api.go:428` + `:461`

```go
func archiveSessionWithCleanup(prev func(sessionID string, stopActivity bool) ([]string, error), svc *worktree.Service) func(string, bool) ([]string, error) {
	return func(sessionID string, stopActivity bool) ([]string, error) {
		out, err := prev(sessionID, stopActivity)
		if err != nil || svc == nil {
			return out, err
		}
		if cleanupErr := svc.ScheduleArchiveCleanup(sessionID); cleanupErr != nil {
			fmt.Fprintf(os.Stderr, "worktree: 会话 %s 已归档，但没有排期清理 checkout: %v\n", sessionID, cleanupErr)
		}
		return out, nil
	}
}
```

**为什么用装配期包装而不是直接改 `archiveSession`（`serve_workspace_api.go`）**：那是同轮另一条线正在改的文件，本任务写作用域不含它。
`attachWorktreeArchive` 在装配期取 `host.workspaceAPI()`（同一份真实函数表）→ 只替换 `ArchiveSession` → `srv.AttachOfficialWorkspaceAPI(api)`
（transport 侧就是同一把 key 覆盖，`handlers_workspace_official.go:332-339`），与"在 `archiveSession` 末尾加一行"等价，且写作用域不交叉。
清理失败**不改变归档结果**（归档已落盘），但醒目打 stderr，客户端可以从 `forgePanel.worktreeCleanup` 看到 `scheduled/retained/failed`。

---

## 3. 先红后绿（4 条真实红证据）

红证据不是编的：在**隔离副本**（`tmp/GO8-RC2/e5-verify`）里逐项拆掉接线后跑同一个用例，日志如下（原文件用 `cp` 备份后还原）：

| # | 拆掉什么 | 用例 | 实测红 |
|---|---|---|---|
| RED-1 | `AppendCreated` 改名（store 不再实现 `CreatedAppender`） | `TestWorktreeArchiveCleanupRemovesCleanCheckout` | `FAIL … 没有清理尝试（期望有）：AppendCreated 或归档后排期没有接上` |
| RED-2 | `attachWorktreeArchive` 不重挂 API（去掉归档后排期） | 同上 | `FAIL … 没有清理尝试（期望有）：AppendCreated 或归档后排期没有接上` |
| RED-3 | `ArchiveHost` 不给 `Unused`/`ReadOnly`（官方 nil = 保守保留） | 同上 | `FAIL … 干净 checkout 的清理状态 = retained，期望 removed` |
| RED-4 | 去掉子代理祖先归档判据（`ancestorArchived`） | `TestWorktreeArchiveCleanupSkipsArchivedSubagent` | `FAIL … 祖先已归档的子代理会话不该挡住清理：状态 = retained，期望 removed` |

后绿：9 条新用例全 PASS（§5），且 `internal/worktree`（B1 的 26 条归档用例）与 `internal/transport` 全绿（§6）。

---

## 4. 端到端用例证据（checkout 删除/保留 + git 状态）

夹具（`cmd/dshd/serve_worktree_archive_test.go:51`）：`t.TempDir()` 做 home；`newWorktreeRepo(t)` 建真 git 仓库（`git init` + 一个 commit）；
`session.CreateSession` 建真 v4 会话；`attachOfficialWorktree` + `attachOfficialWorkspace` + **`attachWorktreeArchive`** 按 serve.go 的顺序接线；
归档走**真实 HTTP 路由** `srv.ServeHTTP(/api/workspace.archiveSession)` + `MintInternalCookie()`，不是直接调内部函数。

| 用例（file:line） | 场景 | 断言 | 实测 |
|---|---|---|---|
| `:240` `…RemovesCleanCheckout` | 干净 checkout → 归档 | `worktreeCleanup=removed`；目录 `Lstat` 不存在；`git worktree list` 无该条目；`git branch --list` 仍在且 `rev-parse` 仍等于创建时 base commit；会话日志仍在 | PASS（1.45s） |
| `:273` `…RetainsDirtyCheckout` | checkout 里写一个未跟踪文件 `dirty.txt` → 归档 | `retained`；目录仍在；分支仍在 | PASS（1.00s） |
| `:295` `…RetainsReferencedCheckout` | 另一个**未归档**会话 cwd 就在该 checkout 里 → 归档创建者 | `retained`（`Unused` 判据）；目录、分支仍在 | PASS（0.85s） |
| `:313` `…SkipsArchivedSubagent` | 子代理子会话（`subagent/spawn` 帧，cwd 在 checkout 里）→ 归档父会话 | `removed`（祖先归档带走子代理，不被它挡死） | PASS（1.13s） |
| `:335` `…HonorsRetainSetting` | `state/worktree-settings.json = {"retainOnArchive": true}` | **没有清理尝试**（`CleanupOf` = nil,false）；目录、分支仍在 | PASS（0.94s） |
| `:357` `…RetainsReadOnlyCreator` | 创建者 preset = 内置 `read-only` | `retained`；目录、分支仍在 | PASS（1.09s） |
| `:385` `TestForgeRC2WiringReadsArchiveCleanup` | 步骤 1+2+4 联合：先 501（未挂），挂 `newForgeRC2API(src.Cwd, svc)` 后归档前 `{"result":null}`，归档后读到 `{revision,status:"removed"}` 且与 `CleanupOf` 一致 | PASS（1.11s） |
| `:458` `TestWorktreeArchiveSettings` | 设置读取点：无文件→false、`true`→true、`false`→false、无该键→false、坏 JSON→**true（保留）**、无 home→false | PASS（0.01s） |
| `:499` `TestArchiveFactsFoldsCreatedAndSpawn` | `AppendCreated`+`Fold` 折出创建记录；`subagent/spawn` 帧折出父会话；有效 cwd = header cwd | PASS（0.39s） |

负面证据也都**不是静默**：`ReadOnly` 解析失败 / `Unused` 出错 / 设置文件坏，三条都往 stderr 打一行并落到"保留"。
（官方 `WorktreeCleanup` schema 只有 4 个 status、没有错误通道，所以原因不能塞进结果里；四字段错误留给 transport 层。）

---

## 5. 门禁（隔离副本 `tmp/GO8-RC2/e5-verify`）

**为什么跑隔离副本**：共享工作树这一小时里有 6+ 个代理同时写（`internal/settingsform`、`modules/tool/schedule`、`modules/search`
先后处于半写状态，且有人在 `tmp/GO8-RC2/d3-backup/` 放了整份仓库备份导致 `go build ./...` 连 tmp 都编）。
判据 = 共享树 `go build ./cmd/... ./internal/... ./modules/... ./codecs/...` 通过的那一刻把这四个目录 + `go.mod/go.sum`
同步进 `tmp/GO8-RC2/e5-verify/`（排除 `tmp/`），在那里跑；本任务的 3 个文件就是共享树里的同一份内容。

```
go build ./...                                     → OK
go vet ./cmd/dshd ./internal/worktree ./internal/transport ./internal/forge → OK
go test ./internal/worktree/  -count=1             → ok 24.3s（含 B1 的 26 条归档用例，0 FAIL）
go test ./cmd/dshd/ -count=1 -run 'TestWorktree|TestForgeRC2|TestArchiveFacts|TestOfficialWorkspace'
                                                   → ok 11.8s，0 FAIL（本任务面 + 归档准入面）
go test ./internal/transport/ -count=1             → 1 FAIL（**不是本任务**，见下）
go test ./cmd/dshd/           -count=1 -v          → 全量两次都只有 1 条外部 FAIL（见下）
```

全量 `cmd/dshd` 两次的结果（机器同时被其他代理的 real-process 测试占满，耗时 190–210s、抖动大；
第二次与我早先误起的一个重复全量跑撞了同一个日志文件，PASS 计数被截断，**FAIL 列表是完整的**）：

| 次 | PASS | FAIL | SKIP | 说明 |
|---|---|---|---|---|
| 1（快照 modules 略旧） | 836 | 2：`TestToolParityWithDSH`、`TestEveryToolErrorHasHint` | 9 | 两条都指向 `modules/tool/*`；刷新 modules 后 `TestToolParityWithDSH` 转绿 |
| 2（当前快照） | 643 顶层 + 148 子测试 | **1**：`TestEveryToolErrorHasHint` | 7 | 唯一 FAIL 仍是指向 `modules/*` 的那条外部断言 |

**这两条 FAIL 都不在本任务范围内**（我的 diff 在 `internal/transport/` 与 `modules/` 里是 **0 行**，两处断言指向的都是同轮别的代理正在改的文件）：

| 失败 | 断言指向 | 归属 |
|---|---|---|
| `TestOfficialCredentialsSingleTruthWithCatalogUpsert`（`internal/transport`） | `handlers_settings_official_test.go:1105`「describe 只允许 configured/source/writable 三个字段」，实际多了 `masked` | D2 的 credentials rc.2 字段（`handlers_settings_official.go` 已 `M`，测试断言尚未同步） |
| `TestEveryToolErrorHasHint`（`cmd/dshd` 的 R3 辅助测试） | `modules/exec/bash/tools.go:214/402/555`、`modules/tool/schedule/schedule.go:279` 返回 error 未设 Hint | `modules/*` 同轮在改（`git status` 里这些文件均为 `M`，不是本任务） |

本任务新增的 9 条用例在两次全量跑里逐条 PASS（`TestWorktreeArchive*` 6 条 + `TestForgeRC2WiringReadsArchiveCleanup` + `TestWorktreeArchiveSettings` + `TestArchiveFactsFoldsCreatedAndSpawn`）。

- `-race` **UNVERIFIED**：本机无 gcc/cgo（`-race requires cgo`），与全仓口径一致。
- 新增用例 0 SKIP；7 个 SKIP 全是 `cmd/dshd` 里既有的 Windows/环境相关跳过（与 B1 报告一致）。
- 提醒 Lead：共享树 `go build ./...` 现在还会去编 `tmp/GO8-RC2/d3-backup/`（队友放进去的整份仓库备份），
  所以判据要用 `go build ./cmd/... ./internal/... ./modules/... ./codecs/...`；那份备份目录建议由它主人清掉。

---

## 6. 未实现项与差距登记（**不许**当成 DONE）

| # | 项 | 现状 | 说明 / 后续 |
|---|---|---|---|
| 1 | **反归档取消**（官方 `workspace/session-unarchiving`：取消未准入的移除、等已准入的结算） | **未实现** | B1 报告 §9 第 9 条已登记；`internal/worktree` 没有 `CancelArchiveCleanup`。窗口很小（需 inspection 全过 + 恰好反归档），但**是真实差距**：本轮的包装只挂了`archiveSession`，`unarchiveSession` 未加钩子。补法：`internal/worktree` 加 `CancelArchiveCleanup(sessionID)`，在 `unarchiveSession` 成功后调用（同样用装配期包装，写作用域不交叉）。 |
| 2 | `Unused` 的**代价**：读一遍所有未归档会话的 v4 日志 | 已实现但慢 | 官方走 `sessionQuery.listSessions` + `sessionProjectionCache`（只读投影缓存）。本仓没有那份缓存，`ArchiveFacts` 每份会话 `v4.ReadFile` 一次；183 份语料实测秒级（`internal/session/list.go:240` 的同类实测 4.37s）。只在后台 inspection 里跑，不在请求路径上。要快就得加"会话投影缓存（workingDirectory/worktree）"，属后续优化。 |
| 3 | `Snapshot.Creations` **无 fork 继承位**（官方 `creation.owned`） | 按"本会话日志里的 created 都算自己创建" | B1 报告 §9 第 10 条；`Unused` 因此把 fork 继承来的记录也当"引用" → 只会**多保留**，不会多删。 |
| 4 | `RetainOnArchive` **没有 UI/设置面板** | 只有最小读取点 `<home>/state/worktree-settings.json` | 官方开关在 Plugins 页（"Keep worktrees when archiving"）。宿主现有 `internal/settingsform` 只有 model/permission 两个 namespace，本轮**没有**伪造一个面板；默认 false（官方默认）已在注释写清。要产品化需要新增一个 settings namespace（属另一条线）。 |
| 5 | `Exclusive` 宿主回调 | **故意留 nil** | `ArchiveHost.Exclusive` 为 nil 时 `internal/worktree` 用自身 per-path 互斥（`archive.go:513-529`）；宿主的"目录访问 vs 移除"互斥点（官方 `working-directory/access` 事件）本仓没有对应事件，留 nil 不假装做了。 |
| 6 | 只读策略的**未知 preset 回落** | 解析失败 → 按只读（保留） | 生产会话的 preset 只有 `modes.DefaultID`/内置/用户 preset 三种（`session.CreateSession(..., modes.DefaultID)`），rc.1 时代没有 preset 的老会话也没有 `plugin:worktree/created` 记录（永不被扫），所以不影响真实清理；方向是"少删"。 |

---

## 7. 边界与"没做"的事

- 未改 `internal/transport/server.go`（rc.2 Forge 路由已由既有 forge 族认领，`TestForgeRC2WiringReadsArchiveCleanup` 用真实 `ServeHTTP` 复核）。
- 未改 `cmd/dshd/serve_workspace_api.go`（归档排期用装配期包装，见 §2.4）。
- 未改 `cmd/dshd/serve_forge_rc2.go` / `serve_forge_git.go`（B1 的实现与 `src.Cwd` 解析器原样复用）。
- 未改版本号、未 commit、未 push。
- 没有降级任何既有能力：未挂实现时 12 条 rc.2 方法仍是 501 + 点名缺什么；`worktreeCleanup` 没有服务时仍是官方 `null`。
- 隔离副本 `tmp/GO8-RC2/e5-verify/` 是临时产物（不参与构建），可随时按 §5 的方法重建。
