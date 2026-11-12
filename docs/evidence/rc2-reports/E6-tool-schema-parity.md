# E6 · 工具 schema 对拍与补齐（官方 DSH 0.2.2-rc.2）

任务：`task-20`（共享任务板 revision 1 → claimed → completed）。
执行人：`e6-toolschema`。工作目录：`D:/DeepSeek Harness/default-workspace/DSH-GO/.agents/worktrees/dsh-bdb66fbf-95ba`。

---

## 0. 结论摘要（先看这一段）

| 判据 | 结果 |
|---|---|
| `go test ./cmd/dshd -run TestToolParityWithDSH` | **先红 → 现在绿**（`--- PASS: TestToolParityWithDSH`） |
| 三类清单计数（修后） | **A 缺 required = 0 个工具**；B 参数名不一致 = 2 个工具；C 缺能力参数 = 3 个工具 |
| 官方 required 参数 | 修前 2 个工具缺（`pwsh` 缺 `description`；`schedule_create` 缺 `rrule`/`target`），**已全部补齐** |
| 官方工具名 | 新增 `schedule_view` / `schedule_edit`；旧 `schedule_update` / `schedule_delete` 保留为 deprecated 别名转发 |
| 自己加的判据测试 | pwsh 9 条、schedule 12 条，全部通过（含真实进程、真实 jobs 表、真实 rrule 周期计算） |
| 工具表快照 | `-update` 后逐条 diff：**只有预期变化**（2 个新工具 + 5 个工具的参数/描述变化），无意外改动 |

**没有靠放宽夹具、把 required 改成可选、或删测试来变绿**：夹具 `cmd/dshd/testdata/dsh-tools.json` 一行未动（本次是 rc.2 真实会话重采样版），判据测试 `cmd/dshd/parity_test.go` 一行未动。

---

## 1. 判据与夹具口径

- 权威工具表：`tmp/GO8-RC2/official-rc2-docs/dsh/node_modules/@deepseek-ai/dsh/docs/tool-catalog.md`
  （每个包一节 + 每个工具一段完整 JSON schema；本次体检以它为准）。
- 机器可读版：`tmp/GO8-RC2/dsh-tools-rc2.json`（77 条，含 `bash`/`pwsh` 各两条包变体）。
- 判据测试：`cmd/dshd/parity_test.go` 的 `TestToolParityWithDSH`，输入夹具
  `cmd/dshd/testdata/dsh-tools.json` = **rc.2 真实会话重采样**。
  采样顺序是「按 mtime 取最新 60 个会话」（`parity_test.go:108-135` 的注释写清了为什么：
  home 里同时躺着 rc.1 与 rc.2 会话，目录序会把 rc.1 的形状抽进来），`PARITY_REFRESH=1` 可重采样。
  本次没有重采样——夹具已经是 rc.2 形状，本次只改工具侧。
- 体检脚本（本次新增，可复现）：`tmp/GO8-RC2/audit_tools.py`
  （解析 catalog 的 ```json 段 + 我们的 `cmd/dshd/testdata/tools.snapshot.json`，输出 A/B/C 三类清单）。
  输出存档：`tmp/GO8-RC2/logs/E6-audit.txt`、逐工具对照表 `tmp/GO8-RC2/logs/E6-table.md`。

---

## 2. 先红后绿证据

### 2.1 红（改动前，会话开始时的真实输出）

```text
$ go test ./cmd/dshd -run TestToolParityWithDSH
    parity_test.go:263: pwsh	缺参	额外: timeout_seconds
    parity_test.go:263: schedule_create	缺参	额外: allow_writes,daily_at,every_seconds,kind,weekdays,weekly_at
    parity_test.go:266: 与 DSH 的工具契约不一致：
          pwsh: DSH 要求但我们不认的参数 description
          schedule_create: DSH 要求但我们不认的参数 rrule,target
FAIL
FAIL	dshgo/cmd/dshd	0.135s
```

两条都是**会打空真实调用**的差异：
- 官方 `pwsh` 的 required 是 `description` + `command`；我们只认 `command`，模型照官方传参会被 schema 拒；
- 官方 `schedule_create` 的 required 是 `title`+`prompt`+`rrule`+`target`；我们要求 `kind`，模型照官方传参必被拒。

### 2.2 绿（改动后）

```text
$ go test ./cmd/dshd -run TestToolParityWithDSH -v
--- PASS: TestToolParityWithDSH (0.03s)
ok  	dshgo/cmd/dshd	0.078s

    parity_test.go:263: pwsh	超集	额外: timeout_seconds
    parity_test.go:263: schedule_create	超集	额外: allow_writes,daily_at,every_seconds,kind,weekdays,weekly_at
    parity_test.go:263: schedule_edit	一致	额外:
    parity_test.go:263: schedule_list	一致	额外:
    parity_test.go:263: schedule_view	一致	额外:
    parity_test.go:263: schedule_update	DSH 无此工具      ← deprecated 别名，官方无同名（只报告，不判失败）
    parity_test.go:263: schedule_delete	DSH 无此工具      ← 同上
```

日志：`tmp/GO8-RC2/logs/E6-green-parity.log`（在**共享工作树**里跑的，`--- PASS: TestToolParityWithDSH`）。

红/绿两份日志都在：
- 红 `tmp/GO8-RC2/logs/E6-red-parity.log`——把 `modules/tool/pwsh/pwsh.go`、`modules/tool/schedule/schedule.go`
  临时换回 `HEAD` 版本（其余文件不变）复跑同一判据，得到的正是上面那两条失败；
- 绿 `tmp/GO8-RC2/logs/E6-green-parity.log`——当前工作树。

> **注（并行写者）**：中途 `modules/exec/bash/tools.go:460` 被别的 agent 改到中间态
> （`!wait` 用在 `time.Duration` 上，`./cmd/dshd` 整包编不过）。为了不越界改别人的文件，
> 那段时间的门禁在 `tmp/GO8-RC2/verify/`（工作树副本，只修了那一行中间态）里跑；
> 对方修好后，**所有门禁已在共享工作树里重跑并全绿**（见 §2.3）。

### 2.3 共享工作树里的门禁结果（全部在真实工作树跑）

```text
--- PASS: TestToolParityWithDSH            (0.04s)   ← 本任务判据
--- PASS: TestToolTableSnapshot            (0.04s)
--- PASS: TestToolTableIsStableAcrossAssemblies (0.08s)
--- PASS: TestWriteToolsDeclareCaps        (0.04s)
--- PASS: TestEveryToolHasDescription      (0.04s)
--- PASS: TestToolTableTokenBudget         (0.03s)   工具表 59 个，20995 字节，估算 5631 token（上限 6000）
--- PASS: TestCompactTableKeepsContract    (0.05s)
ok  dshgo/cmd/dshd 0.426s

ok  dshgo/modules/tool/pwsh      4.720s   ← 9 条新判据 + 既有沙箱测试
ok  dshgo/modules/tool/schedule  0.529s   ← 12 条新判据 + 既有兼容测试

# merged / lazy / schema 投影 / schedule host 这些会受"工具表变了"影响的定向测试：
ok  dshgo/cmd/dshd 1.001s
    (-run 'TestMerged|TestLazy|TestTightSchema|TestRequiredParamsAreDeclared|TestCompactIsTheDefault|TestSchedulePolicy|TestScheduleHost|TestScheduleRun')
```

**没有把 `go test ./cmd/dshd` 整包当门禁**：这个共享工作树里同时有多个 agent 在写，
整包跑期间先后撞到两处别人的编译中间态（先是 `modules/exec/bash/tools.go` 的 `!wait`，
后是 `internal/pluginmgr/official/repository_cache.go` 里 `
` 被写坏），整包因此编不过/挂住。
本任务的判据与全部定向门禁在**它们编译好的窗口里**已经全绿（上面那组），
本任务两个包随时可独立复验（`go test ./modules/tool/pwsh ./modules/tool/schedule`，绿）。

绿测之后我只动过两处**与 schema 无关**的收尾（`gofmt`，以及把 `Data` 里的 `scheduledAt/scheduledAtLocal/updatedAt/currentState`
改成仓库约定的 snake_case）。为证明"判据仍成立"，另做了一次**静态一致性核对**：把 `modules/tool/schedule/schedule.go`
`Schema()` 里六个字面量、`modules/tool/pwsh/pwsh.go` 的 schema 字面量解出来，与 `cmd/dshd/testdata/tools.snapshot.json`
（由真实注册表 `-update` 生成）逐键 JSON 比对 ⇒ `ALL SCHEMAS MATCH SNAPSHOT`（`schedule_create` required=`title,prompt,rrule,target`；
`schedule_view` required=`id`；`schedule_edit` required=`id,action`；`schedule_list/update/delete` 无 required；`pwsh` required=`description,command`）。

---

## 3. 逐工具对照表（catalog ∩ 我们 = 37 个同名工具）

判定口径 = 对拍判据本身：**官方 required ⊆ 我们的 properties**；其余差异只报告。

| # | 工具 | 官方 required | 我们 required | 判定 | 官方有我们没有（可选=能力参数） |
|---|---|---|---|---|---|
| 1 | `ask_user_question` | questions | questions | 一致 | — |
| 2 | `bash` | description, command | command, description | required 齐；缺能力参数 | timeoutMs, workdir |
| 3 | `create_goal` | objective | objective | 一致 | — |
| 4 | `edit` | file_path, old_string, new_string | 同 | 一致 | — |
| 5 | `exit_plan_mode` | — | — | 一致 | — |
| 6 | `get_goal` | — | — | 一致 | — |
| 7 | `glob` | pattern | pattern | 一致 | — |
| 8 | `grep` | pattern | pattern | 一致 | — |
| 9 | `job_kill` | job_id | job_id | 一致 | — |
| 10 | `job_list` | — | — | 一致 | — |
| 11 | `job_output` | job_id | job_id | 一致 | — |
| 12 | `list_agents` | — | — | 一致 | — |
| 13 | `load_workspace_dependencies` | — | — | 一致 | — |
| 14 | `present` | — | — | 一致 | — |
| 15 | **`pwsh`** | **description, command** | **description, command** | **一致（本次修）** | — |
| 16 | `read` | file_path | file_path | 一致 | — |
| 17 | `read_image` | file_path | file_path | 一致 | — |
| 18 | `skill` | — | — | 一致 | — |
| 19 | **`schedule_create`** | **title, prompt, rrule, target** | **同** | **一致（本次修）** | — |
| 20 | **`schedule_list`** | — | — | 一致（本次补 active/limit/offset） | — |
| 21 | **`schedule_view`** | id | id | **新增，一致** | — |
| 22 | **`schedule_edit`** | id, action | id, action | **新增，一致** | — |
| 23 | `send_message` | agent | agent | 一致 | — |
| 24 | `subagent` | description, prompt | description, prompt | required 齐；缺能力参数 | name |
| 25 | `subagent_fork` | description, prompt | description, prompt | 一致 | — |
| 26 | `team_task_create` | subject, description | 同 | 一致 | — |
| 27 | `team_task_get` | task_id | task_id | 一致 | — |
| 28 | `team_task_list` | — | — | 一致 | — |
| 29 | `team_task_update` | task_id, action | 同 | 一致 | — |
| 30 | `todo_write` | todos | todos | 一致 | — |
| 31 | `update_goal` | goal_id, revision, action | 同 | 一致 | — |
| 32 | `wait_agent` | — | — | required 齐；**参数名不一致** | timeout_ms（我们叫 `timeout_seconds`） |
| 33 | `web_fetch` | url | url | 一致 | — |
| 34 | `web_search` | queries | queries | 一致 | — |
| 35 | `workflow` | script, meta | script | required 齐；缺能力参数 | args, run_in_background |
| 36 | `working_directory` | — | — | required 齐；**参数名不一致** | cd（我们叫 `path`） |
| 37 | `write` | file_path, content | 同 | 一致 | — |

（完整机器版见 `tmp/GO8-RC2/logs/E6-table.md`；表外还有 39 个官方工具我们没有同名实现——属另一条任务线的范围，本次只登记不处理。）

---

## 4. 三类清单与处置

### A · 缺 required（修后 **0** 个）

| 工具 | 缺的参数 | 处置 |
|---|---|---|
| `pwsh` | `description` | **已修**：schema required 改成 `["description","command"]`；`description` 进 `Summary`（UI 标题）+ `Data.description`。 |
| `schedule_create` | `rrule`, `target` | **已修**：schema required 改成 `["title","prompt","rrule","target"]`；`rrule` 真解析成下一次触发，`target` 落成宿主认的 `target_kind`/`target_session`。 |

### B · 参数名不一致（**2** 个，均不在本任务写作用域）

| 工具 | 官方名 | 我们的名 | 影响 | 为什么不在这里修 |
|---|---|---|---|---|
| `wait_agent` | `timeout_ms` | `timeout_seconds` | 模型照官方传 `timeout_ms` 会被我们拒（参数不认）。 | 归属 `modules/tool/team`，本任务写作用域是 pwsh/schedule/bash。**建议**：schema 加 `timeout_ms`（毫秒，官方名优先）并保留 `timeout_seconds` 为别名，与本次 pwsh 的处理方式一致。 |
| `working_directory` | `cd` | `path` | 同上。 | 归属 `modules/tool/wd`。**建议**：加 `cd` 别名（官方名优先）。 |

### C · 缺能力参数（**3** 个）

| 工具 | 缺的参数 | 类型 | 处置/原因 |
|---|---|---|---|
| `bash` | `timeoutMs`, `workdir` | 官方可选能力参数 | **未修（越界）**：我们真正的 bash 工具在 `modules/exec/bash/tools.go`（不是 `modules/tool/bash/**`），且该文件此刻正被另一个 agent 改（`git diff` 里 +157 行）。`description` / `run_in_background` 它**已经有**（官方 rc.2 的两项硬要求已满足）。缺 `timeoutMs`（毫秒超时）与 `workdir`（一次性工作目录）。**建议派给该文件 owner**：照本任务 pwsh 的 `resolveTimeout` 与 `backgroundCommandLine` 的绝对化逻辑补。 |
| `subagent` | `name` | 官方可选（成员名） | **未修（越界）**：归属 `modules/tool/subagent`。官方 `name` 是"给子代理起名"，我们的 `id` 是 continuable 会话 id，**不是同一个概念**，不能拿 `id` 顶替。 |
| `workflow` | `args`, `run_in_background` | 官方可选 | **未修（越界）**：归属 `modules/tool/workflow`。`args` 是传给脚本的入参（本仓 workflow 工具目前只吃 `script`/`meta`），`run_in_background` 需要接到 jobs 运行时（可复用本次 pwsh 的 `spi.Exec` 接缝写法）。 |

### 其它登记（不算三类，但值得知道）

- `interrupt_agent` / `spawn_teammate` / `watch_ack` / `watch_doctor` 等是我们**独有**的工具：官方 0.2.2-rc.2 没有同名工具，属能力增量，不违反对拍（判据只拦"我们有的必须 DSH 有同名 + 官方 required 我们必须认"）。
- 官方 39 个我们没有同名实现的工具（`apply_patch`、`office_*`、`terminal_*`、`session_*`、`stagehand_*`、`lsp`、`ralph`、`run_code`、`cordis_*`、`plugin_*` 等）不在本任务范围，未动。

---

## 5. `pwsh` 改动（`modules/tool/pwsh/**`）

### 5.1 schema（逐字对齐官方 rc.2）

```json
properties: description / command / timeoutMs / workdir / run_in_background
            + timeout_seconds（旧别名，deprecated）
            + sandbox_permissions（本仓 per-call 沙箱请求，只加不减）
required:   ["description", "command"]
```

官方 `timeoutMs` 的类型是 **number**（允许小数毫秒），照抄；解析时四舍五入到毫秒、夹到 `[1, 600000]`。

### 5.2 行为

| 参数 | 行为 | 证据测试 |
|---|---|---|
| `description` | 进 `Summary`（`"<description> · pwsh 退出码 N（…）"`，UI 直接显示）+ `Data.description`；v4 落盘的 `tool/call` 入参里本来就有它（不剥参数）。缺失不拒绝（旧调用方没有它），但 schema 标 required。 | `TestDescriptionSurfacesInResult` |
| `timeoutMs` | **官方名优先**：给了它就用它（毫秒），否则回落到旧的 `timeout_seconds`（秒），都没有则默认 120s，上限 600s。生效值写进 `Data.timeout_ms`，模型/人都能看出是哪条生效。 | `TestTimeoutMsTakesEffect`（300ms 掐断 8s 的 `Start-Sleep`）、`TestTimeoutMsWinsOverLegacySeconds`（250ms vs 60s，按 250ms 走）、`TestLegacyTimeoutSecondsStillHonored`（旧别名仍按秒） |
| `run_in_background` | **真后台**：经 `spi.Exec` 接缝登记进宿主**同一张** jobs 表（`job_output`/`job_kill` 立刻能用），毫秒级返回 job id；`Data.job_id` + `Data.background=true`，`Summary`/`Text` 都点名 id。接缝优先用 additive 能力 `RunBackground(cmd)`（与 bash 工具同一方法）；只有 `Run` 时用 1ms idle 让它立刻转后台。没有接缝时 **fail-loud**（不静默前台跑、不编 id）。 | `TestBackgroundRegistersRealJobAndOutputIsReadable`（真 `modules/exec/bash` 的 Service：立刻拿 id、`List()` 查得到、`Output()` 拿到 `a'b done`——顺带证明脚本里的单引号穿过 bash 没被吃掉）、`TestBackgroundFallsBackToIdleSeam`、`TestBackgroundWithoutSeamFailsLoud`、`TestBackgroundCommandLineQuoting` |

### 5.3 实现要点（代码里都写了注释）

- 后台命令行形状：`cd -- '<绝对 workdir>' && 'pwsh.exe' -NoLogo -NoProfile -NonInteractive -Command '<脚本>'`；
  workdir 先 `filepath.Abs`（接缝会话的 cwd 可能与我们的进程 cwd 不同），shell 用 **basename**（让 bash 用它自己的 PATH 再解析一次，避免 Windows 绝对路径在 bash 侧的路径转换不确定）；脚本做 POSIX 单引号转义（`'` → `'\''`），换行原样保留。
- `background.go` 顶部注释写清了架构理由：工具模块不许 import 别的模块（R2）→ 只能走 `spi.Exec`；私表 job 是**假后台**，比"不支持"更坏。
- 与官方的一处**有意保留的差异**：官方说"超时后把命令**转后台**而不是杀掉"。本仓的 jobs 表没有"接管一个已在跑的进程"的 API（`spi.Job` 只有 Output/Kill/List，没有 adopter），所以 `pwsh` 在超时处**终止并报错**（Hint 直接引导改用 `run_in_background`）。这条差异也存在于 bash（它没有 `timeoutMs` 可对拍），属宿主 jobs 能力缺口，不是本工具偷懒——已在 §7 明确登记。

---

## 6. `schedule` 改动（`modules/tool/schedule/**`）

### 6.1 模型面（官方 rc.2 形状）

| 工具 | required | 说明 |
|---|---|---|
| `schedule_create` | `title` `prompt` `rrule` `target` | 走官方形状；旧 `kind`/`every_seconds`/`daily_at`/`weekly_at`/`weekdays` 保留为**兼容别名**（只在没给 `rrule` 时生效），`allow_writes` 是我们原有的无人值守写闸门 |
| `schedule_view` | `id` | **新增**；按 id 看一条（含 prompt / rrule / target / status / next_at / updated_at） |
| `schedule_edit` | `id` `action`(update\|delete) | **新增**；`active` 暂停/恢复、`rrule`/`target` 换规则/换目标、`expectedUpdatedAt` 乐观并发 |
| `schedule_list` | —（全可选） | 补 `active` / `limit` / `offset`；活跃在前，其余按 `scheduled_at` 升序 |
| `schedule_update` | —（旧形状） | **deprecated 别名** → 转发 `applyEdit(action=update)` |
| `schedule_delete` | —（旧形状） | **deprecated 别名** → 转发 `applyEdit(action=delete)` |

### 6.2 rrule → 既有引擎（不新写引擎）

- 解析：`internal/schedule.ParseRRule(text, nil)`（1291 行的 RFC 5545 引擎，支持 TZID/Z/浮动、COUNT/UNTIL/BYDAY/BYSETPOS…）。
- 下一次：`spec.Next(now)`；落盘 `spec.Expression()` **规范化原文**（不是原文回抄、也不是只留 next_at——到点推进必须按规则重算）。
- 落盘键与宿主逐字一致：`kind="rrule"`、`rrule`、`target_kind`/`target_session`/`target_cwd`、`status`、`next_at`… 所以 `dshd schedule due/advance` 与 serve 的官方 schedule API 读的是**同一份字节**。
  证据：`TestRRuleRecordIsUnderstoodByHostEngine` 用 `schedulestore.Load` + `schedulestore.DueAt` 证明宿主引擎认得这条记录、`Ends=false`、算出的下一次仍是 DTSTART 给的 09:00。
- `target` 映射：`this_session` → `target_kind=session` + `target_session=<当前会话>`；`new_once` → `target_kind=new_once`；`new` → `target_kind=new`；后两者同时落 `target_cwd`（本会话工作目录，官方 "New sessions use this session's working directory"）。
- `rrule` 真按周期算下一次的证据：`TestCreateRRuleComputesRealNextOccurrence`
  （`FREQ=DAILY` → 下一次是今天/明天的 09:00 且距现在 ≤ 25h；`FREQ=WEEKLY;BYDAY=MO` → 下一次必须是周一 09:30:00）。
- 坏规则 fail-loud：缺 DTSTART、COUNT 用尽（没有下一次）都会拒登记，Hint 说清缺什么。`TestCreateInvalidRRuleFailsLoud`。

### 6.3 edit 的官方语义

- `action=update`：增量（指针字段区分"没传"与"传空"）；至少给一个字段，否则 Hint 提示。
- `action=delete`：只删登记记录，**不撤回已投递消息**（官方原话照做）。
- `active`：`false` 落 `status=paused`（`internal/schedule.DueAt` 会真的跳过它，证据在 `TestEditPausesAndResumes`），`true` 恢复时**重算 next_at**；规则已经没有下一次时拒绝恢复并指向给新 rrule（官方 "Otherwise provide a new rrule"）。
- `expectedUpdatedAt`：不一致时**不是错误**，返回 `{code:"schedule_conflict"}` 结果且一个字节都不写（官方 200 + union 的语义）；一致才改。
  `updated_at` 改成 **RFC3339 纳秒**戳——秒精度会让同一秒内的两次 edit 看不出变化，乐观并发就漏判。
- 并发声明：`schedule_view`/`schedule_list` = `isConcurrencySafe`（官方一致）；`schedule_edit` 与两个写别名独占（它们是读整份文件→改→写回）。

---

## 7. 工具表快照 diff（`cmd/dshd/testdata/tools.snapshot.json`，只经 `-update` 生成）

`go test ./cmd/dshd -run TestToolTableSnapshot -update` 后逐条语义 diff（脚本比对更新前后两份 JSON）：

| 工具 | 变化 |
|---|---|
| `pwsh` | `+ description`、`+ timeoutMs`、`+ run_in_background`；`required: ["command"] → ["description","command"]`；`timeout_seconds` 描述改为 deprecated 别名 |
| `schedule_create` | `+ rrule`、`+ target`；`required: ["title","prompt","kind"] → ["title","prompt","rrule","target"]`；`kind`/`every_seconds`/`daily_at`/`weekly_at`/`weekdays` 描述改为"已废弃（仅在没有 rrule 时生效）"；描述更新 |
| `schedule_list` | `+ active`、`+ limit`、`+ offset`；描述更新 |
| `schedule_update` | 仅描述改为 deprecated 别名 |
| `schedule_delete` | 仅描述改为 deprecated 别名 |
| `schedule_view` | **新增**：`required: ["id"]`，caps `{}`（只读） |
| `schedule_edit` | **新增**：`required: ["id","action"]`，caps `{writes:true}` |

**没有其它工具的变化**（逐个 `name` 比对：`only_before=[]`）。工具数 57 → 59；compact 工具表估算 **5354 → 5631 token（上限 6000）**，仍有余量（`TestToolTableTokenBudget` 绿）。

---

## 8. 越界说明（必须让 Lead 知道的一处）

`cmd/dshd/snapshot_test.go` 的 `TestWriteToolsDeclareCaps` 要求**每个注册工具**都在它那张 `want` 能力表里显式认领，
否则报 "新工具 %q 没有认领能力"。新增 `schedule_view` / `schedule_edit` 后，不改这张表这条守卫必红（它的报错原文就这么要求）。
所以本任务**只加了 2 行**（外加把那 3 行注释改成官方口径，`git diff` 上一共 +4/−1）：

```go
"schedule_view":   {},
"schedule_edit":   {Writes: true},
```

这是任务书边界之外唯一的改动，性质是"新工具的能力认领"，测试口径一个字没放宽。若 Lead 认为该文件的 owner 应自己改，
把上面三行交给 owner 即可，其余改动不受影响。

---

## 9. 自己加的判据测试（先红后绿之外的第二层证据）

`modules/tool/pwsh/parity_test.go`（9 条）：
`TestSchemaMatchesOfficialRc2`、`TestDescriptionSurfacesInResult`、`TestTimeoutMsTakesEffect`、
`TestTimeoutMsWinsOverLegacySeconds`、`TestLegacyTimeoutSecondsStillHonored`、
`TestBackgroundWithoutSeamFailsLoud`、`TestBackgroundRegistersRealJobAndOutputIsReadable`、
`TestBackgroundFallsBackToIdleSeam`、`TestBackgroundCommandLineQuoting`。

`modules/tool/schedule/parity_test.go`（12 条）：
`TestSchemasMatchOfficialRc2`、`TestCreateRRuleComputesRealNextOccurrence`、`TestCreateRRuleRequiresTarget`、
`TestCreateInvalidRRuleFailsLoud`、`TestRRuleRecordIsUnderstoodByHostEngine`、`TestViewReturnsFullRecord`、
`TestEditPausesAndResumes`、`TestEditConflictChangesNothing`、`TestEditRRuleSwitchesRecurrence`、
`TestEditDeleteAndLegacyAliases`、`TestListFiltersAndPaginates`、`TestViewIsConcurrencySafe`。

既有测试全部保留并通过（`modules/tool/schedule/schedule_test.go`、`version_test.go`、`modules/tool/pwsh/sandbox_test.go` 一行未改）。

---

## 10. 未修项与原因（汇总）

| 项 | 状态 | 原因 / 下一步 |
|---|---|---|
| `bash` 缺 `timeoutMs`、`workdir` | 未修 | 实现在 `modules/exec/bash/tools.go`（不在本任务写作用域），且该文件此刻被别的 agent 改到中间态（编译红）。owner 补时照抄本任务 pwsh 的 `resolveTimeout` / 后台命令行拼装即可。 |
| `wait_agent` 的 `timeout_ms`（我们叫 `timeout_seconds`） | 未修 | 归属 `modules/tool/team`；建议加官方名、旧名留别名。 |
| `working_directory` 的 `cd`（我们叫 `path`） | 未修 | 归属 `modules/tool/wd`；建议同上。 |
| `subagent` 的 `name` | 未修 | 归属 `modules/tool/subagent`；官方的 `name` 与我们的 `id` 不是同一概念，不能顶替。 |
| `workflow` 的 `args`、`run_in_background` | 未修 | 归属 `modules/tool/workflow`；`run_in_background` 可复用本次的 `spi.Exec` 接缝写法。 |
| 官方"超时后转后台"语义 | 有差异（已登记） | `spi.Job` 没有"接管已在跑的进程"的接口；`pwsh` 超时即终止并在 Hint 里引导 `run_in_background`。要完全对齐需给宿主 jobs 加 adopter 能力（跨任务，需 Lead 决定）。 |
| `new_once` 第一次投递的"新建会话" | 部分（宿主侧缺口） | 工具面落 `target_kind=new_once`（`new` 由宿主在投递时真建会话）；`serve_schedule_api.go` 的 run 分支只对 `targetKind=="new"` 建会话，`new_once` 首次投递会落到持有文件的那个会话再固化。修在 `cmd/dshd`（本任务不可改），已登记。 |

---

## 11. 复现命令

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
cd "D:/DeepSeek Harness/default-workspace/DSH-GO/.agents/worktrees/dsh-bdb66fbf-95ba"

# 判据（先红后绿的那条）
go test ./cmd/dshd -run TestToolParityWithDSH -v

# 本次新增的行为测试
go test ./modules/tool/pwsh -run 'TestSchema|TestDescription|TestTimeout|TestBackground' -v
go test ./modules/tool/schedule -run 'TestSchemas|TestCreate|TestRRule|TestView|TestEdit|TestList' -v

# 工具表三条守卫 + 预算 + compact 契约
go test ./cmd/dshd -run 'TestToolTableSnapshot|TestWriteToolsDeclareCaps|TestEveryToolHasDescription|TestToolTableTokenBudget|TestCompactTableKeepsContract' -v

# 快照重生成（确认改动是有意的之后）
go test ./cmd/dshd -run TestToolTableSnapshot -update

# 全表体检（三类清单）
python tmp/GO8-RC2/audit_tools.py
```

---

## 12. 交付物清单

| 文件 | 说明 |
|---|---|
| `modules/tool/pwsh/pwsh.go` | schema/args/Exec 改：`description`、`timeoutMs`、`run_in_background` 分支 |
| `modules/tool/pwsh/background.go` | **新增**：`spi.Exec` 接缝、后台命令行拼装与 POSIX 引号转义 |
| `modules/tool/pwsh/parity_test.go` | **新增**：9 条行为判据 |
| `modules/tool/schedule/schedule.go` | 官方 rc.2 形状（rrule/target）、view/edit、别名转发、list 分页、乐观并发 |
| `modules/tool/schedule/parity_test.go` | **新增**：12 条行为判据 |
| `cmd/dshd/testdata/tools.snapshot.json` | `-update` 生成（逐条 diff 见 §7） |
| `cmd/dshd/snapshot_test.go` | 只加 2 行能力认领（+改 3 行注释；越界说明见 §8） |
| `tmp/GO8-RC2/audit_tools.py` | 体检脚本（可复现三类清单） |
| `tmp/GO8-RC2/logs/` 下 `E6-audit.txt`、`E6-table.md`、`E6-red-parity.log`、`E6-green-parity.log` | 证据存档 |
| 本报告 | `tmp/GO8-RC2/reports/E6-tool-schema-parity.md` |
