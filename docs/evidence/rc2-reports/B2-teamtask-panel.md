# B2 · teamtask 面板：Go 宿主团队任务板对齐官方 0.2.2-rc.2 契约

> 线：B2（宿主 Team 任务板）
> 作用域：`modules/tool/teamtask/**`（唯一实现包）+ 本报告文件
> 基线：官方 `0.2.2-rc.2`，证据根 `tmp = D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai`
> 工作树：`.agents/worktrees/dsh-bdb66fbf-95ba`（未 commit / 未 push，按任务要求）
> 结论：**四工具契约增量全部落地，18/18 测试绿（9 新增契约测试 + 9 既有测试），`go build`/`go vet` 绿。**

---

## 1. 官方证据（逐条 file:line）

| # | 契约点 | 官方证据（相对 `$tmp/`） |
|---|---|---|
| 1 | 四个工具的参数与输出 schema | `dsh-tool-agent-team/src/index.ts:117-234`（create :117-141 / list :143-182 / get :184-198 / update :200-234） |
| 2 | `TASK_VIEW_SCHEMA`（id/revision/subject/description/status/owner/blockedBy/writeScopes/ready/writeScopeWarnings） | `dsh-tool-agent-team/src/index.ts:36-51` |
| 3 | `TASK_LIST_VALUE_SCHEMA`（`{tasks[], nextCursor?}`） | `dsh-tool-agent-team/src/index.ts:53-60` |
| 4 | `expected_revision` **必填** + action 8 值 | `dsh-tool-agent-team/src/index.ts:205`、`:206-211`；`dsh-agent-team/src/types.ts:151-160`、`:162-172` |
| 5 | CAS 比对与 `TEAM_TASK_STALE_REVISION` | `dsh-agent-team/src/task-board.ts:128-133` |
| 6 | `reassign`：仅 lead、仅 pending/in_progress、省略 owner = 卸任、指派需 ready | `dsh-agent-team/src/task-board.ts:188-203`（lead 判据 `:135`、卸任 `:196-199`、blocked `:200`） |
| 7 | 每次变更 `revision+1`、新建 `revision: 1` | `dsh-agent-team/src/types.ts:63`、`src/task-board.ts:69`、`:218-221` |
| 8 | `ready = status==pending && 所有 blockedBy 已 completed` | `dsh-agent-team/src/task-view.ts:22-24`（`taskReady`）；视图返回体 `projectTaskView` :35-56，`ready:` 在 **:53** |
| 9 | `writeScopeWarnings`：与**其它 in_progress 任务**按路径段重叠，文案 `write scopes overlap with <id>` | `dsh-agent-team/src/task-view.ts:12-14`（`scopesOverlap`）、`:36-42`（告警推导，文案在 :40） |
| 10 | `writeScope` 归一化与非法判定（`\`→`/`、去开头 `./`、去尾 `/`；绝对路径/盘符/`.`/`..`/空段非法） | `dsh-agent-team/src/validation.ts:26-34`；错误码 `TEAM_INVALID_WRITE_SCOPE` |
| 11 | `list` 五参数（status/owner/unowned/ready/cursor/limit）与互斥、分页、nextCursor | `dsh-tool-agent-team/src/index.ts:146-157`、`:161-180`（互斥 `:167`、limit 校验 `:164-165`、分页 `:176-180`） |
| 12 | `create` 参数含 `write_scopes` | `dsh-tool-agent-team/src/index.ts:124-128` |
| 13 | `edit` 可整份替换 `write_scopes` | `dsh-agent-team/src/task-board.ts:159-172` |
| 14 | agent 名字形状 `^[a-z][a-z0-9-]{0,31}$`、根名 `lead` | `dsh-subagent/src/names.ts:13-16` |
| 15 | `maxTasks` 默认 256（本包保持） | `dsh-agent-team/src/task-board.ts:33,59-62`（历史包 `app.asar … :1439/:1443/:1458/:1224`） |
| 16 | `force` **不在官方 schema** | `dsh-tool-agent-team/src/index.ts:203-217`（参数集合里没有 `force`） |

> 说明：任务书给的 `task-view.ts:12-14/22-24/35-56` 与 `validation.ts:26-34` 行号已按任务书口径采用并复查；`task-board.ts`、`types.ts`、`index.ts`、`names.ts` 的行号本人在提取根上逐条核过（`names.ts:13-16` 是本次实测确认，早先报告里的 `:10/:12` 口径偏了两行，本报告与代码注释均用 `:13-16`/`:16`）。

---

## 2. 参数对照表（工具面，宿主现状 → 官方）

| 工具 | 参数 | 官方 | 改造前宿主 | 改造后宿主 | 差异说明 |
|---|---|---|---|---|---|
| `team_task_create` | `subject` / `description` | required | required | 不变 | — |
| | `blocked_by` | array | 有 | 不变 | — |
| | `write_scopes` | array | **无** | **新增**（归一化后落盘） | 官方 `index.ts:124-128` |
| `team_task_list` | `status` | enum 3 值 | 有 | 不变 | — |
| | `owner` | string | **无** | **新增**（按 owner 名字精确匹配；与 `unowned` 互斥） | 官方按名字→id 解析，宿主 owner 本身就是名字，见 §6 遗留 |
| | `unowned` | bool | **无** | **新增** | 与 `owner` 同时给 → 参数校验失败 |
| | `ready` | bool | **无** | **新增** | 按投影的 `ready` 过滤 |
| | `cursor` | int ≥0，默认 0 | **无** | **新增** | 负数 → 报错；越界 → 空页（不报错） |
| | `limit` | int 1..100，默认 50 | **无**（旧 `maxListItems=200` 硬截断） | **新增**（默认 50 上限 100，分页取代 200 硬截断） | — |
| `team_task_get` | `task_id` | required | 不变 | 不变 | — |
| `team_task_update` | `task_id` | required | required | 不变 | — |
| | `expected_revision` | **required int** | 可选、**不校验** | **必填 + 真 CAS** | 官方 `index.ts:205`、`task-board.ts:128-133` |
| | `action` | 8 值含 `reassign` | 7 值（缺 `reassign`） | **8 值**（顺序与官方一致） | 官方 `index.ts:206-211` |
| | `owner` | claim 名字 / reassign 名字（省略=卸任） | 仅 claim | claim + reassign | 官方 `index.ts:216`、`task-board.ts:188-203` |
| | `subject` / `description` | edit | edit | 不变 | — |
| | `blocked_by` | set_dependencies 整份替换 | 同 | 不变 | — |
| | `write_scopes` | edit 整份替换 | **无** | **新增** | 官方 `index.ts:215` |
| | `force` | **官方无** | 有（delete 用） | 保留，schema/注释标注 **"宿主扩展（官方无此参数）"** | 官方 `index.ts:203-217` 无此键 |

---

## 3. 输出对照表（每条任务视图 / 每类结果）

### 3.1 任务视图（`Data.task` / `Data.tasks[]`）

| 键 | 官方类型 | 改造前宿主 | 改造后宿主 |
|---|---|---|---|
| `id` | string required | 有 | 有 |
| `revision` | integer required | **无** | **有**（从 1 起，每次变更 +1 连续；旧文件读到 0 自动补 1） |
| `subject` / `description` / `status` | string required | 有 | 有 |
| `owner` | string 可选 | 有（空串表示无主） | 有（保持始终存在；官方在无主时省略该键，宿主保留空串以兼容旧消费者） |
| `blockedBy` | array required | **无（只有 snake）** | **有** |
| `writeScopes` | array required | **无** | **有**（归一化、去重） |
| `ready` | boolean required | **无** | **有**（官方 `task-view.ts:22-24` 语义） |
| `writeScopeWarnings` | array required | **无** | **有**（官方文案 `write scopes overlap with <id>`） |
| deprecated 兼容键 | — | `blocked_by`、`created_at`、`updated_at` | 保留全部；另加 `write_scopes`、`write_scope_warnings` 两个 snake 别名（"只能更多不能更少"） |

> 能力下限说明：官方 `additionalProperties: false`，宿主同一对象里多给 snake_case 键属于**超集**；官方消费方按 camelCase 读不受影响，旧消费方按 snake_case 读也不破。

### 3.2 各工具 Data 形状

| 工具 | 官方值 | 宿主改造后 | 旧键是否保留 |
|---|---|---|---|
| create | 任务视图 | `{id, status, task:<view>, total}` | 是（`id`/`status`/`task`/`total` 全在，`task` 现在是视图） |
| list | `{tasks[], nextCursor?}` | `{tasks:[view], count, total, truncated, nextCursor?, status?, note?}` | 是（`count/total/truncated/note/status` 全在） |
| get | 任务视图 | `{task:<view>, unfinished_blockers}` | 是 |
| update | 任务视图 | `{action, task:<view>, total}` | 是 |
| update(delete) | 墓碑视图 | `{action:"delete", deleted_id, revision, total}` | 是（旧 `deleted_id/total` 保留，新增 `revision`） |

---

## 4. 实现要点（对应任务书 7 条）

| 任务书 | 落地位置（`modules/tool/teamtask/teamtask.go`） | 行为 |
|---|---|---|
| 1 `.expected_revision` 必填 + 真 CAS | schema `:596`（required 含 `expected_revision`）、`:1017` execUpdate、CAS `:1128-1141` | 缺失 → `TEAM_INVALID_ARGUMENT` + Hint 指向 `team_task_get`；不匹配 → `TEAM_TASK_STALE_REVISION`，Hint 给当前/期望 revision，**先比对再落盘**（错误分支早于任何 save/archive） |
| 2 动作集 + `reassign` | `actions` 常量（8 值同官方顺序）、`:1180-1210` reassign 分支 | lead 判定见下；`owner` 省略=卸任（退回 pending）；指定 owner 时要求 pending/in_progress 且 `taskReady`；非法名字（空串/大写/空格/连字符开头/超 32）→ `TEAM_MEMBER_NOT_FOUND`/`TEAM_INVALID_ARGUMENT` 语义 Hint |
| 3 `force` 保留 + 标注宿主扩展 | schema `force.description`、`updateArgs.Force` 注释 | 行为不变（delete 专享逃生门，照样进归档）；说明文字明写"宿主扩展（官方 team_task_update 没有这个参数）" |
| 4 视图字段对齐 | `taskView :121`、`projectView :307`、`taskReady :294`、`scopesOverlap :287` | 见 §3.1；`ready= pending && 全 blocker completed`；告警只看其它 `in_progress` 任务，按路径段比较（`a/b` 与 `a/bc` 不算重叠） |
| 4 `write_scopes` 落盘 + 校验 | `normalizeWriteScope :374`、`normalizeWriteScopes :401`；create/edit 接线 | 逐字复刻 `validation.ts:26-34`：`\`→`/`、去开头 `./`、去尾 `/`；空/绝对/盘符/`.`/`..`/空段 → `TEAM_INVALID_WRITE_SCOPE` + Hint；**先校验后落盘**，被拒不改文件 |
| 5 `list` 参数与分页 | schema `:596`、execList `:784` | 过滤顺序 status→owner→unowned→ready，分页 `slice(cursor,cursor+limit)`，`cursor+limit<total` 才给 `nextCursor`；`owner`+`unowned` 互斥 → 参数校验失败 |
| 6 `create` 加 `write_scopes` | execCreate `:688` | 落盘 `write_scopes`，输出 `writeScopes`/`write_scopes` |
| 7 `maxTasks` 256 不变 | `defaultMaxTasks` 未动 | `maxMembers` 不在本包，未触碰 |

### lead 判定口径（宿主近似，必须说清）

官方判据是"调用者 == 子代理树根"（`task-board.ts:135` `const lead = caller === root`），本包是纯 Go、不拿 `agentTeams`/`subagents` 服务，所以：

- `board.leadSession`（装配期读 `DSHD_TEAM_LEAD_SESSION`）非空 → **只有该会话是 lead**；
- 未接线（空）→ **单根任务板，当前会话即 lead**（保序旧调用方与旧测试不破）。

这不是官方等价物，登记在 §6。

---

## 5. 先红 → 后绿证据

### 5.1 先红（新测试跑在 HEAD 旧实现上）

做法：`git show HEAD:modules/tool/teamtask/*.go` + 旧测试文件复制成临时子包 `modules/tool/teamtask/redcheck/`，新测试原样拷入（仅去掉唯一依赖新字段 `board.leadSession` 的"非 lead 拒绝"断言，其余不动），跑完删除该临时目录（不影响交付树）。

```
$ go test ./modules/tool/teamtask/redcheck/ -count=1 -run TestTeamTask -v
--- FAIL: TestTeamTaskUpdateRequiresExpectedRevision
--- FAIL: TestTeamTaskUpdateRejectsStaleRevision
--- FAIL: TestTeamTaskUpdateReassign
--- FAIL: TestTeamTaskViewHasWriteScopesAndReady
--- FAIL: TestTeamTaskWriteScopeOverlapWarns
--- FAIL: TestTeamTaskListOwnerUnownedReadyFilters
--- FAIL: TestTeamTaskInvalidWriteScopeRejected
--- FAIL: TestTeamTaskRevisionContinuityAndLegacyDefault
--- FAIL: TestTeamTaskSchemasMatchOfficialContract
FAIL   （红 9 / 9）
```

代表性失败原因（原样）：

- 缺 CAS 前置仍成功：`缺 expected_revision 必须报错，实际成功: map[... "action":"claim" ... "Status":"in_progress"]`
- 视图没有 revision：`视图里没有 revision: map[...]`
- 没有 `reassign`：`未知 action "reassign" (hint 未知 action：可选 claim|release|complete|reopen|edit|set_dependencies|delete…)`
- 告警字段不存在：`interface conversion: interface {} is nil, not []interface {}`（`writeScopeWarnings` 缺失）
- schema 不符：`action enum` 少 `reassign`、`expected_revision` 不在 required、`list` 缺 5 参数、`create` 缺 `write_scopes`

### 5.2 后绿（本包实测）

```
$ go build ./modules/tool/teamtask/    # OK
$ go vet   ./modules/tool/teamtask/    # OK
$ go test  ./modules/tool/teamtask/ -count=1
ok  	dshgo/modules/tool/teamtask	3.210s
EXIT=0
```

用例计数（`-v` 统计）：**18 PASS / 0 FAIL**（9 新增 + 9 既有）：

| 测试 | 覆盖任务书第几条 |
|---|---|
| `TestTeamTaskUpdateRequiresExpectedRevision` | 1（缺参报错 + Hint + 字节级确认不落盘） |
| `TestTeamTaskUpdateRejectsStaleRevision` | 1（错 revision 报错、任务与文件都不变、`TEAM_TASK_STALE_REVISION`） |
| `TestTeamTaskUpdateReassign` | 2（改 owner、非法目标、非 lead 拒绝、卸任、blocked、已完成不可转派） |
| `TestTeamTaskViewHasWriteScopesAndReady` | 4（写域归一化/落盘、ready 随 blocker 变化、camel+snake 双键） |
| `TestTeamTaskWriteScopeOverlapWarns` | 4（两个 in_progress 写域重叠 → 含对方 id；`a` vs `abc` 不误报；完成后不再告警） |
| `TestTeamTaskListOwnerUnownedReadyFilters` | 5（互斥报错、owner/unowned/ready 过滤、limit/cursor/nextCursor/truncated、非法参数） |
| `TestTeamTaskInvalidWriteScopeRejected` | 4（绝对/盘符/`..`/`.`/空段/空串 → 报错 + Hint + 不落盘；edit 同规则） |
| `TestTeamTaskRevisionContinuityAndLegacyDefault` | 4（1→2→…→6 连续；旧文件 revision 补 1 后可 CAS） |
| `TestTeamTaskSchemasMatchOfficialContract` | 2/3/5/6（required、8 值 enum 顺序、list 5 参数、create write_scopes、force 标注"宿主扩展"） |
| `TestDelete*`（4 个）、`TestTaskBoard*`（3 个）、`TestClaim*`（2 个） | 既有测试，**断言未放宽** |

### 5.3 既有测试必改点（仅补必填参数，断言不动）

CAS 变必填后，旧测试里 9 处 `team_task_update` 调用缺 `expected_revision`，按新契约补参数（每处注释都写了官方 file:line，`index.ts:205` / `task-board.ts:69,128-133`）：

| 文件 | 改动 |
|---|---|
| `delete_test.go` | `claimTask` 助手改为先 `get` 取 revision 再 claim；4 处 delete 调用补 `expected_revision`；`force` 处补"宿主扩展"注释 |
| `max_tasks_test.go` | 腾位置那次 delete 补 `expected_revision:1` |
| `teamtask_test.go` | 2 处 claim 补 `expected_revision:1`（无身份拒绝用例确保"拒绝原因只是缺身份"） |

没有删除任何测试、没有放宽任何断言、没有改 golden。

---

## 6. 遗留 / 未实现项（诚实清单）

1. **lead 判定是宿主近似**（最重要）：官方 = 调用者等于子代理树根（`task-board.ts:135`）；宿主 = 显式 `DSHD_TEAM_LEAD_SESSION` 或"未接线则当前会话即 lead"。未接线时**任何会话都能 reassign**。需要上层把真正的 root 会话注入 `DSHD_TEAM_LEAD_SESSION`（`Module(dir)` 签名不能改，故走环境变量）。
2. **reassign 目标存在性无法校验**：官方 `resolveAssignee` 要求目标是 root 或 continuable descendant，否则 `TEAM_MEMBER_NOT_FOUND`（`task-board.ts:232-238`）。本包只做名字**形状**校验（`names.ts:16`）；名字不存在也能转派成功。需要子代理目录支撑。
3. **状态机授权矩阵只对齐了 CAS + reassign**：官方 `claim` 要求 `pending && ready && 未被他人认领`，`release` 要求 `in_progress`，`complete` 要求 `in_progress`，`reopen` 要求 `completed`（`task-board.ts:142-187`，含 `TEAM_TASK_ALREADY_CLAIMED`/`TEAM_TASK_BLOCKED`/`TEAM_TASK_INVALID_TRANSITION`）。宿主保持旧宽松行为（claim 不检查 ready、可在 completed 上 claim），因为既有调用方与既有测试按宽松语义写的。收紧前需要一轮显式拍板。
4. **`delete` 与官方不同**：官方留墓碑（`status:"deleted"`、revision+1、`get` 仍可取、有依赖者则 `TEAM_TASK_HAS_DEPENDENTS` 拒绝，`task-board.ts:204-212`）。宿主保持"真删 + 追加 `team-tasks.deleted.jsonl` 归档"（旧测试锁定"删完 get 报错"）；**未实现 `TEAM_TASK_HAS_DEPENDENTS`**，所以可以删掉仍被其它任务 `blocked_by` 的任务，之后依赖方 `ready` 恒为 false（blocker 找不到）。建议下一轮补这道闸。
5. **`list` 的 `owner` 过滤不做名字→id 解析**：官方先 `resolveName` 再按 `ownerId` 过滤（`index.ts:168-174`）。宿主 owner 字段本身就是名字/会话 id，直接精确匹配；缺点是"名字与 id 都算 owner"时不做归一。
6. **`force` 是宿主扩展**：官方无此参数（`index.ts:203-217`）。保留是为了单板宿主的可操作性（删别人认领的任务），已在 schema description 与代码注释双处标注，并强制进归档。
7. **`maxMembers` 不在本包**：任务书第 7 条已界定；本包只保留 `maxTasks=256`，未新增/删除 `maxMembers` 相关代码（那是 `modules/tool/team/` 的范围）。
8. **`team_task_get` 对已删任务报"找不到"**而非官方墓碑视图（同 4）。
9. **`unfinished_blockers` 为空时是 `null`**（旧行为，未改）。

### 需要上层支撑（本包不改 `internal/`、`cmd/`）

| 需求 | 为什么需要上层 |
|---|---|
| 注入 `DSHD_TEAM_LEAD_SESSION` | 本包拿不到"活子代理树根"；需 `cmd/dshd` 装配时按会话根设置环境变量，否则 lead 判定永远是"未接线"分支 |
| 子代理目录查询（名字→是否存在） | reassign 的 `TEAM_MEMBER_NOT_FOUND` 需要 `list_agents` 等价的活目录；`spi` 无此接口，且不允许在本包引依赖 |
| 官方 `TEAM_TASK_HAS_DEPENDENTS` / 墓碑语义 | 若要对齐官方 delete，需要删除语义从"删条目"改成"墓碑 + 归档"；会改既有测试锁定行为，需上层拍板 |

---

## 7. 验证命令与结果（原样）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
cd "D:/DeepSeek Harness/default-workspace/DSH-GO/.agents/worktrees/dsh-bdb66fbf-95ba"

go build ./modules/tool/teamtask/     # exit 0
go vet   ./modules/tool/teamtask/     # exit 0
go test  ./modules/tool/teamtask/ -count=1
# ok  	dshgo/modules/tool/teamtask	3.210s   (EXIT=0)

go test ./modules/tool/teamtask/ -count=1 -v | grep -cE '^--- (PASS|FAIL)'
# 18      （18 PASS / 0 FAIL）
```

格式化：5 个 `.go` 文件在 `tr -d '\r'` 后 `gofmt -l` 均干净（工作树整体是 CRLF，未改动其它文件的行尾风格）。

`gofmt`/构建范围外说明：`go build ./cmd/dshd/` 当前在本工作树**是红的**，原因是并行线新增的未跟踪文件 `cmd/dshd/serve_forge_rc2.go`（forge review write 签名不匹配），与本包无关（`git status` 显示该文件为 `??`，属其它 agent 的在建改动）；`go build ./modules/tools/ ./internal/kernel/` 均绿，`cmd/dshd/main.go` 里 `teamtask.Module(...)` 的调用点未改动签名（`Module(dir string)` 保持），本包变更对装配面零破坏。

---

## 8. 改动文件清单

| 文件 | 类型 | 行数 | 说明 |
|---|---|---|---|
| `modules/tool/teamtask/teamtask.go` | 改 | 864→1328 | CAS、reassign、视图投影、写域校验、list 过滤分页、schema |
| `modules/tool/teamtask/contract_test.go` | 新增 | 830 | 9 个契约测试（先红后绿，含 3 个额外守护：revision 连续性/旧文件迁移、schema 契约、告警 snake 别名） |
| `modules/tool/teamtask/delete_test.go` | 改 | 158→188 | 仅补 `expected_revision` 参数 + 注释（断言未变） |
| `modules/tool/teamtask/max_tasks_test.go` | 改 | 118→120 | 仅补 `expected_revision` |
| `modules/tool/teamtask/teamtask_test.go` | 改 | 68→72 | 仅补 `expected_revision` |
| `tmp/GO8-RC2/reports/B2-teamtask-panel.md` | 新增 | — | 本报告（唯一作用域外文件） |

未 commit / 未 push（按任务要求）。未改 `internal/`、`cmd/`、`docs/`、`AGENTS.md`、`CHANGELOG`。
