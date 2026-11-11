# E9 · `liveshareShares.addSessions`（rc.2 新增方法）落地报告

> 任务：`task-23`（RC2-E9）。基线：官方 `0.2.2-rc.2`。
> 结论：**GO**。契约层（13 个方法）、宿主真实状态、错误面、真机验收全部落地；先红后绿两段都有日志。
> 写作用域内的四个文件：`internal/transport/handlers_liveshare_official.go(+_test)`、`cmd/dshd/serve_liveshare_api.go(+_test)`。未改 `server.go`/AGENTS/CHANGELOG/docs，未 commit。

---

## 0. 一句话

`liveshareShares.addSessions` 从 **MISSING** 变成 **真实现**：把源会话加进一条**已发布**的 share，
返回该 share 现在服务的全量会话集合（主会话在第一位），幂等、落盘、跨重启可见；
share 不存在 / 会话不存在 / 跨工作区 / 跨 owner 四种失败都是结构化四字段错误 + Hint。

---

## 1. 官方语义（file:line）

官方包 `@deepseek-ai/dsh-experimental-liveshare@0.2.2-rc.2`（rc.2 随包 `src/*.ts`）：

| 事实 | 出处 |
|---|---|
| Remote 声明：`@Remote('addSessions')`，`invocation: direct` | `src/host/share-remote.ts:60-69`（契约 `sourceLocation` = line 67 col 3） |
| 服务接口 | `src/host/share-operations.ts:54` |
| 实现（顺序：validate → 解析 share → 过滤新增 → 持久化 → 装访问层 → accessChanged） | `src/host/owner-share-operations.ts:397-449` |
| 入参/结果类型 | `src/share-api-types.ts:164-174` |
| `sessionId` 是第一项的派生视图（"primary first; the authoritative list"） | `src/share-api-types.ts:100-116`、`src/host/source-sessions.ts:17-27` |
| 四条前置拒绝码：`missing-share-id` / `missing-session-ids`（去重后空、含空串）/ `share-not-found` | `src/host/owner-share-operations.ts:409-427` |
| 幂等：`requested.filter(id => !current.sessionIds.includes(id))`，全已存在则直接返回 | `owner-share-operations.ts:428-429` |
| 同一 workspace 断言（`session-workspace-unknown` / `session-workspace-mismatch`） | `owner-share-operations.ts:123-150`（调用点 :430） |
| **不发 relay 请求**：注册只带主会话、不随加会话变化 | `owner-share-operations.ts:400-407` 注释 + :428-448（无 relay 调用） |
| 落盘后才返回；写失败回滚并报 `owner-state-unavailable` | `owner-share-operations.ts:431-443` |
| 结果 = 该 share 的**全量**源会话集合 | `owner-share-operations.ts:448` |
| 该方法是 **owner-only**（Gateway 拒绝 `share-viewer` peer） | `src/endpoint-classification.ts:241` |
| rc.2 同时把 `CreatedShare.sessionIds` / `OwnerShareView.sessionIds` 变成 required | `share-api-types.ts:152-163`、`:100-116`；契约 `liveshareShares/create`、`liveshareShares/list` 的 `required` |

---

## 2. 契约表（`tmp/GO8-RC2/contracts-rc2.json` → `liveshareShares/addSessions`）

描述：*Add source Sessions to one share this host already publishes.*

**参数**（`parameters[0]` name=`request`, source=json, `additionalProperties: true`）

| 字段 | 类型 | 必填 | 官方描述 | 我方落点 |
|---|---|---|---|---|
| `shareId` | string | ✅ | — | `OfficialLiveshareAddSessionsRequest.ShareID`；`requiredStringField`（非空） |
| `sessionIds` | array\<string\> | ✅ | *Sessions to add; ids already served by the share are ignored.* | `.SessionIDs`；`requiredNonEmptyStringArrayField`（缺/null/非数组/空数组/空串元素 → 400）+ 保序去重 `dedupeStrings`（官方 `[...new Set]`） |

**结果**（required = `shareId`,`sessionIds`，`additionalProperties: true`）

| 字段 | 类型 | 必填 | 我方落点 |
|---|---|---|---|
| `shareId` | string | ✅ | `OfficialLiveshareShareSessions.ShareID`；`validateOfficialShareSessions` 拒空 |
| `sessionIds` | array\<string\> | ✅ | `.SessionIDs`；校验非 nil、非空、无空串、**无重复**、**必须包含全部请求 id**（少了＝假成功 → 500） |

**错误面（四字段 code/message/hint/retryable，全部有 Hint）**

| 场景 | HTTP | code | 判定来源 |
|---|---|---|---|
| shareId/sessionIds 形状错 | 400 | `bad_request` | （transport 先于 501） |
| share 不在本宿主的 owner 记录 | 404 | `not_found` | `owner-shares.json` |
| 会话不存在 | 404 | `no_such_session` | `session.FindByID` |
| 会话与主会话不同工作区 | 400 | `bad_request` | 会话文件的 projectKey |
| 跨 owner / 跨 relay 改别人的 share | 403 | `permission_denied` | share 记录上的 `relay` vs 当前 effective relay |
| owner 状态文件坏了 | 500 | `internal` | 读/解析失败（不装成"没有分享"） |
| 宿主回假结果（少 id / 重复 / nil） | 500 | `internal` | transport 结果校验 |

---

## 3. 改动清单

### 3.1 `internal/transport/handlers_liveshare_official.go`（13 个方法的 L1 契约层）

| 位置 | 改动 |
|---|---|
| `:3-46` | 文件头：12→13 个方法，补官方 `share-remote.ts` 行号表；说明"两张方法表"的原因 |
| `:68` | 新常量 `MethodLiveshareSharesAddSessions = "liveshareShares.addSessions"`（官方顺序在 create 之后） |
| `:84-99` | `OfficialLiveshareMethods()` 保持 rc.1 的 12 个（写作用域外的 31 计数测试钉住它） |
| `:103-105` | `OfficialLiveshareRC2Methods()` = `{addSessions}` |
| `:108-119` | `IsOfficialLiveshareMethod` 认 13 个（→ `IsOfficialForgeLiveshareMethod` 自动带进 `/api` 路由，无需改 `server.go`） |
| `:167-186` | 新类型 `OfficialLiveshareAddSessionsRequest` / `OfficialLiveshareShareSessions` |
| `:203`、`:216` | `CreatedShare.SessionIDs`、`OwnerShareView.SessionIDs`（rc.2 required，主会话第一位） |
| `:348` | `OfficialLiveshareAPI.AddSessions` 回调（nil = 501 点名） |
| `:445-459` | 分派分支：解析 → 未接线 501 `AddSessions` → 调用 → 结果校验 → 200 |
| `:698-720` | 导出错误构造器：`OfficialLiveshareSessionNotFound`(404/no_such_session)、`OfficialLiveshareBadRequest`(400)、`OfficialLiveshareForbidden`(403/permission_denied) |
| `:796-829` | `parseLiveshareAddSessions` + `dedupeStrings` |
| `:1049-1075` | `requiredNonEmptyStringArrayField` |
| `:1204-1267` | `validateShareSessionIDs`（create/list 共用）、`validateOfficialShareSessions`（addSessions） |

### 3.2 `cmd/dshd/serve_liveshare_api.go`（宿主真实状态）

| 位置 | 改动 |
|---|---|
| `:114-116` | `brokenLiveshareAPI` 补 `AddSessions`（状态坏时也结构化报错，不 501/不假成功） |
| `:154` | `liveshareTransportAPI(svc, home)`（多接一个 `home`，13 个回调不变形） |
| `:154-215` | `liveshareHost{svc, home, sessions}`；`Create` 回 `sessionIds:[主会话]` 并清同 id 旧侧存；`List` 把会话集合并进每条 share；`Stop` 清侧存 |
| `:179-181` | `AddSessions` 回调 → `host.AddSessions` |
| `:445-505` | `readLiveshareOwnerShares`（只读 `owner-shares.json` 的 shareId/sessionId/sessionIds/relay；文件不存在=空，坏了=错误） |
| `:508-670` | `liveshareSessionStore`：`<home>/liveshare/owner-share-sessions.json`，原子写 0600，`resolve/set/forget`，主会话永远第一位 |
| `:672-760` | `liveshareHost.AddSessions`：四类失败 + 幂等 + 落盘（逐条对齐官方顺序） |
| `:776-782` | `liveshareSessionProject`（会话文件 → projectKey = "同一工作区"） |

### 3.3 为什么 owner 状态分两个文件（必须写清的设计取舍）

官方把多会话存进 `owner-shares.json` 的同一记录（`OwnerShare.sessionIds`）。本仓
`internal/liveshare`（`state.go:21-33`）只建模单个 `SessionID`，而**它不在本任务的写作用域里**，
所以本任务用**同目录伴随状态** `<home>/liveshare/owner-share-sessions.json`
（`{"version":1,"shares":{"share-x":["s1","s2"]}}`）存"每条 share 服务哪些会话"：

- 它是真实落盘的 owner 状态（原子写、0600），`list` / `addSessions` / 重启后读的都是它；
- `resolve()` 的取值顺序是 **侧存 → 记录里的 `sessionIds`（将来）→ 只有主会话**，
  所以 `internal/liveshare` 一旦长出 `SessionIDs`，只需把那边的 `Create` 填上初值，
  本文件可以整体删掉，语义不变（不需要迁数据）；
- 之所以不直接往 `owner-shares.json` 里塞额外键：`internal/liveshare.save()` 用结构体
  重新 marshal，未知键会被下一次 `create/list/stop` 静默丢弃 → 状态会悄悄丢。

---

## 4. 先红证据

### 4.1 契约层（`internal/transport`）

```
$ go test ./internal/transport/ -run 'TestOfficialLiveshareAddSessionsIsPartOfTheFamily' -count=1
EXIT=1
--- FAIL: TestOfficialLiveshareAddSessionsIsPartOfTheFamily (0.00s)
    handlers_liveshare_official_test.go:397: liveshareShares.addSessions 没被 IsOfficialLiveshareMethod 认领（rc.2 新增的第 13 个方法）
FAIL
```

日志：`tmp/GO8-RC2/_e9/red-1-transport.log`

### 4.2 宿主真实状态（`cmd/dshd`）

把 `AddSessions` 接线临时摘成 `nil`（只动我自己的文件，跑完立刻恢复）后：

```
$ go test ./cmd/dshd/ -run 'TestLiveshareHostAddSessions' -count=1
EXIT=1
--- FAIL: TestLiveshareHostAddSessionsEndToEnd     addSessions = 501 ... 需要 OfficialLiveshareAPI.AddSessions，但宿主没有接线
--- FAIL: TestLiveshareHostAddSessionsErrors       状态 = 501，want 404
--- FAIL: TestLiveshareHostAddSessionsBrokenOwnerState  状态 = 501，want 500
FAIL
```

日志：`tmp/GO8-RC2/_e9/red-2-host.log`

---

## 5. 绿证据（测试）

| 命令 | 结果 |
|---|---|
| `go build ./...` | EXIT=0 |
| `go vet ./internal/transport/ ./cmd/dshd/` | EXIT=0 |
| `go test ./internal/transport/ -run 'Liveshare' -count=1 -v` | **PASS=18**（顶层）+ **PASS=100**（子用例），FAIL=0 |
| `go test ./cmd/dshd/ -run 'TestLiveshareHost' -count=1 -v` | **6/6 PASS**（其中 3 条是本任务新增：`AddSessionsEndToEnd` / `AddSessionsErrors` / `AddSessionsBrokenOwnerState`） |
| `go test ./internal/transport/ -count=1`（整包，含写作用域外的 31 计数测试） | `ok` EXIT=0 |
| `go test ./cmd/dshd/ -count=1`（整包） | **3 FAIL，全部是 E8（task-22）在飞的 `terminalpanel` 工具面**：`TestWriteToolsDeclareCaps`（`user_terminal_list` 没认领能力）、`TestEveryToolHasDescription`（两个工具 Description 超长）、`TestToolTableTokenBudget`（64 个工具 6131 token > 6000）。均与本任务无关（`cmd/dshd/main.go:176` 注册 `terminalpanel.ModuleFromEnv`）。日志 `tmp/GO8-RC2/_e9/test-cmddshd-full.log` |

新增用例（逐条对应任务书要求的五类）：

| 用例 | 覆盖 |
|---|---|
| `TestOfficialLiveshareAddSessionsIsPartOfTheFamily` | 认领 + 路由 + 未接线 501 点名 `AddSessions` |
| `TestOfficialLiveshareAddSessionsParamsFailLoud` | 13 条参数错误 + `request` 额外字段合法（`additionalProperties:true`） |
| `TestOfficialLiveshareAddSessionsRejectsFakeHostResults` | 假结果 6 条（缺 shareId/nil/空数组/少 id/重复/空串）+ 404/403 四字段透出 |
| `TestOfficialLiveshareCreateAndListRequireSessionIDs` | rc.2 的 `sessionIds` required（create/list）与"主会话第一位" |
| `TestLiveshareHostAddSessionsEndToEnd` | 成功、去重、幂等、`list` 可见、落盘、**重启后可见**、stop 清侧存 |
| `TestLiveshareHostAddSessionsErrors` | share 不存在 404、会话不存在 404、跨工作区 400（状态不变）、**非 owner 403**（状态不变、切回原 relay 后可成功） |
| `TestLiveshareHostAddSessionsBrokenOwnerState` | owner 状态坏了 500（不装成"没有分享"） |

---

## 6. 真机验收（硬要求）

脚本：`tmp/GO8-RC2/_e9/serve-smoke.sh`（每个外部进程都有 trap 回收）
全量输出：`tmp/GO8-RC2/_e9/serve-smoke.log`（EXIT=0）

**链路**：临时 home + 真实 `dshd serve`（`--addr` 默认 `127.0.0.1:0`）＋ 真实 `dsh-relayd`（`127.0.0.1:0`）
→ 播种真实会话文件 → `POST /api/liveshareShares.create` → `addSessions` → `list` → 错误面 → 落盘 → 收进程。

关键输出（原文摘录）：

```
===== 3) 起真实 dshd serve（临时 home，端口由 OS 分配） =====
ready={"url":"http://127.0.0.1:52943","token":"d4f0…0882"} pid=207

===== 5) liveshareShares.create（主会话 s1；body 走 UTF-8 文件） =====
{"result":{"link":"http://127.0.0.1:49232/s/share-a3aa8eb9…","shareId":"share-a3aa8eb9cc4df95da405c8e2c550b1a7",
 "sessionId":"s1","sessionIds":["s1"],"sessionMode":"view","access":{...}}}
HTTP=200

===== 6) liveshareShares.addSessions（把 s2 加进这条已发布的 share） =====
{"result":{"shareId":"share-a3aa8eb9cc4df95da405c8e2c550b1a7","sessionIds":["s1","s2"]}}
HTTP=200

===== 7) liveshareShares.list（会话集合应变成 [s1,s2]） =====
{"result":{"shares":[{"shareId":"share-a3aa8eb9…","sessionId":"s1","sessionIds":["s1","s2"],
 "sessionTitle":"E9 多会话真机",...}]}}
HTTP=200
list sessionId   = s1
list sessionIds  = ['s1', 's2']
list sessionTitle= E9 多会话真机 (UTF-8 往返)

===== 8) 幂等：同一请求再来一次 =====
{"result":{"shareId":"share-a3aa8eb9…","sessionIds":["s1","s2"]}}
HTTP=200

===== 9) 错误面：会话不存在 =====
{"error":{"code":"no_such_session","hint":"先 session.list 拿 id；确认 --home 指向的目录里有这个会话",
 "message":"会话 \"nope\" 不存在（本 home 的 sessions 目录里没有它）：…","retryable":false}}
HTTP=404

===== 10) 错误面：share 不存在 =====
{"error":{"code":"not_found","hint":"检查请求路径与 HTTP 方法；…（先 session.list 或 workspace.list 确认）", …}}
HTTP=404

===== 11) 错误面：跨工作区 =====
{"error":{"code":"bad_request","hint":"检查入参名字（id/sessionId 都认）与类型",
 "message":"会话 \"sx\" 不在 share 主会话 \"s1\" 所在的工作区（--D-tmp-e9-proj-b-- ≠ --D-tmp-e9-proj-a--）：…","retryable":false}}
HTTP=400

===== 12) 落盘证据（<home>/liveshare/） =====
owner-share-sessions.json
{ "version": 1, "shares": { "share-a3aa8eb9…": ["s1","s2"] } }

===== 13) 收进程 =====
kill dshd EXIT=0 / kill relayd EXIT=0 → dshd 已退出 / relayd 已退出
SCRIPT_EXIT=0
```

补注：第 5 步曾经把中文标题写在 curl 命令行参数里，日志里出现 `E9 ���Ự����`——那是 MSYS 把
命令行参数转成 ANSI 码页的**本地工具效应**，不是产品缺陷；改成 UTF-8 文件（`--data-binary @file`）
后同一标题往返完好（见第 7 步 `E9 多会话真机`）。中文分享标题本身不需要任何修复。

首次真机运行的完整日志（同一脚本）里 shareId 是 `share-d237…`，结论一致。

---

## 7. 剩余差距（已知，不阻塞本任务）

1. **`internal/liveshare` 没有 `SessionIDs`**：本任务用同目录伴随状态绕开（§3.3）。建议 Lead 集成时
   给 `StoredShare` 加 `SessionIDs []string \`json:"sessionIds"\`` + `Create` 填初值 + `AddSessions` 方法，
   然后删掉 `cmd/dshd` 的侧存（`resolve` 已按"侧存→记录"顺序，删侧存不改语义）。
2. **`OfficialForgeLiveshareMethods()` 仍是 31（19+12）**：加 rc.2 的 addSessions 后应为 32。
   那个文件与 `handlers_forge_liveshare_official_test.go`（`len==31` + 逐名清单）都在本任务写作用域外，
   所以没有改；`IsOfficialLiveshareMethod` 已经认 13 个、路由/分派全部正常。
   集成时把那张测试的 `31→32`、`want` 清单加 `"liveshareShares.addSessions"`，再把
   `OfficialLiveshareRC2Methods()` 合进 `OfficialLiveshareMethods()` 即可。
3. **viewer 侧不参与 addSessions**：官方明确"relay 注册只带主会话、加会话不发 relay 请求"，
   所以本仓也没有对 relay 做任何写入；viewer 的准入面仍由既有 `setCapabilities` / `updatePermissions` 管。
4. **`OwnerShareView` 的 rc.2 新字段 `viewerPluginAccess` / `viewerPlugins`** 未补（官方
   `share-api-types.ts:115-118`）。它们属于 list 的结构面、与本方法无关，不在本任务范围。
5. `owner-shares.json` 里的 `sessionIds` 只读不写（见 1）；读得到就优先用，所以将来折入不需要迁移。
