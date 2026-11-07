# HOST-TASK-ACTIONS：日程与后台任务真实动作

> 基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 任务开始时的共享工作树（含 W49/W50/W56/W58/W59/W61/W63/W65，以及已集成的 W67 `c3a8d7f3…`、W69 `c7ff76e5…`）。W70 只合并任务动作与安全执行；未 commit/push，未启动 GUI，未改 SPI/bash 执行实现、schedule/settings/model、canonical25/ws/journal/approval/model-catalog/settingsform。
>
> 官方核对：DSH `0.2.2-rc.1` 的 `ui-schedule/src/client/clear-ended.ts:22-44` 是 Host `deleteInactive` 后 readback；`ui-jobs/src/client/index.ts:33-50` 的 observe/kill 都带 session 与 job id。不是“清空全部日程”，也不是按进程名杀任务。

## 1. 交付形状

| 动作 | 真实行为 |
|---|---|
| `schedule.delete` | 按 `sessionId+id` 删除当前 home 的持久记录；可带文件 SHA-256 `expectedRevision`。原子替换后才回 `deleted:true`；未知/重复删除 404，冲突 409，损坏/写失败 500 |
| `schedule.clear` | 对应官方 clear-ended/deleteInactive：仅删 `status="ended"` 或已到 `ended_at` 的记录；保留 running/pending/active 与 every/daily/weekly 周期任务。空目录 `deleted:0`，成功后附权威 `schedules` readback |
| `jobs.output` | 复用已有方法。生产 scoped 路径把 `sessionId/cursor/limit/waitMs` 传到 cmdRun；有 `spi.JobPager` 时先在其 **字节** 域有界读取当前保留快照，再转换成 HTTP **rune** 页，两个游标绝不直接互传。IPC 与 HTTP 应答均只传当前页；结束后读取当前 home 的保留输出 |
| `jobs.stop` | 按 `sessionId+id` 找到本 serve 注册的 cmdRun，每次停止前复核精确 PID 的完整映像；子进程优先调用 `spi.JobController.Stop` 停止已核对归属的进程树，只有旧模块没有 Controller 时才走 `Job.Kill` fallback。已结束、未知、跨实例/跨会话、二次 stop 均保持明确错误码 |

canonical `SessionMethods()` 保持 25 个，原 `schedule.list`/`jobs.list` 方法及全局快照语义不变。新增方法是独立 additive 面；`jobs.output` 只加字段，不另建重复方法。独立夹具：

- `internal/transport/testdata/task-actions.snapshot.json`
- `internal/transport/testdata/task-actions-golden.json`

HTTP 全部是 cookie 后的 `POST /api/<method>` 裸参数。`params` 包装、未知字段、null、数组、标量、错误类型均为 400；错误对象恰好 `{code,message,hint,retryable}`。未认证一律 401。

## 2. 持久化、游标与归属

### 日程

- 每个会话文件的 `revision` 是原文件字节 SHA-256；list 行 additive 带 `sessionId/status/endedAt/revision`，供删除 fence 使用。
- 单删：读与版本校验 → revision fence → 独占 `.tmp` → 文件 Sync → 同目录 rename。已有 `.tmp` 不覆盖、不删除，因为无法证明归属。
- 批量清：先读完并校验所有会话文件；任何坏文件都在第一个写入前失败。多文件中途写失败时逆序回滚已写文件；回滚也失败会合并报错，不写成功响应。
- `next_at` 已过不等于 ended（可能只是尚未 Advance），因此不用于批量清判据。非法 `ended_at` 同样 fail-loud。

### 后台任务

- HTTP `cursor/nextCursor` 单位仍是 **Unicode code point**，不是可能切开中文/emoji 的任意字节。默认 limit=16384，最大 65536；负数或越过当前保留文本返回 400。
- HTTP `total` 是**当前保留文本**的 rune 数，`hasMore` 只表示这个保留文本内是否还有下一页。W69 SPI `JobOutputPage.Total` 则是含已淘汰前缀的**总字节数**；二者单位和覆盖范围都不同，禁止互相赋值。
- W69 每任务最多保留最后 1 MiB；内部 ended 哨兵参与当次有界截断时，最终用户文本可能略小于 1 MiB。HTTP additive `truncated:true` 明确表示更早前缀已不可用；此时 `total` 仍只描述保留窗口，绝不伪造成完整历史 rune 数。保留窗口内连续分页仍用 rune 游标；若客户端跨多次读取且窗口同时继续淘汰，应依据 `truncated` 重新建立保留文本基线，不能把旧 rune 水位当成绝对历史水位。
- Pager 适配器每次最多组装 1 MiB 字节、64 KiB 一页、最多四次一致快照重试；先拼齐字节再解 rune，避免 SPI 页边界切断中文/emoji。超过 1 MiB 的其他 Pager 保留窗口、无进展页或持续变化的快照 fail-loud，不无界分配、不伪绿。`waitMs` 保持旧 `Output(wait)` 等 ended 的语义。
- 旧 `JobsOutput` 回调与没有 `spi.JobPager` 的旧 `spi.Job` 仍走原 `Output` 兼容分页；生产 `JobsOutputScoped` 要求 sessionId，并把分页参数传到子进程。旧 `job_kill` Hint 已改为真实归属进程树停止语义。
- 应答关联随机 requestId、sessionId、job id。旧 run 的延迟注销不能覆盖新 run；旧应答不能投递给另一请求。跨实例 `spi.ErrJobWrongInstance` 映射 `cross_session`，未知 id 映射 `not_found`，自然结束/二次 stop 映射 `job_finished`。
- 保留文件为 `<home>/state/jobs/<sessionId>/<id>.json`，同样使用独占临时文件、Sync、rename，并保留 `truncated` 真值。A/B home 没有共享缓存。
- Windows 每次 stop 前以 `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` + `QueryFullProcessImageName` 核对精确 PID；执行层再核对 root 创建代次/映像并用 Job Object 收树（Unix 为独立 process group）。实现没有镜像名枚举、全局 `taskkill` 或按名字杀 DSH/magpie 的路径。
- 真实进程树验收不是只看 `done`：真实 dshd→cmdRun→bash 启动 helper parent→child→grandchild，三个 PID 都写入临时 home；stop 后逐个复核进程已消失才通过。测试清理也只处理这些 PID 文件登记且映像复核匹配的 helper。

## 3. 先红证据（原样输出）

命令：

```bash
go test ./internal/transport -run 'TestJobsOutputCursor|TestTaskActionArguments' -count=1 -v
```

旧实现真实退出码 1；不是假定会红，也不是编译跳过：

```text
=== RUN   TestJobsOutputCursorAndLimitAreEnforced
transport: /api/jobs.output
    task_actions_test.go:47: 分页不对：map[done:true exit_code:0 id:j1 output:abcdef]
--- FAIL: TestJobsOutputCursorAndLimitAreEnforced (0.00s)
=== RUN   TestJobsOutputCursorUsesRunesWithoutSplittingUTF8
transport: /api/jobs.output
    task_actions_test.go:63: UTF-8 被切坏或游标单位漂移：map[done:true exit_code:0 id:j1 output:甲🙂乙]
--- FAIL: TestJobsOutputCursorUsesRunesWithoutSplittingUTF8 (0.00s)
=== RUN   TestTaskActionArgumentsFailBeforeCapability
transport: /api/schedule.delete
    task_actions_test.go:79: schedule.delete 旧实现/错误参数 status=501 want=400 body={"error":{"code":"not_implemented","hint":"宿主这一侧还没接线，换一个 method 或等它实现","message":"not implemented","retryable":false}}
transport: /api/schedule.clear
    task_actions_test.go:79: schedule.clear 旧实现/错误参数 status=501 want=400 body={"error":{"code":"not_implemented","hint":"宿主这一侧还没接线，换一个 method 或等它实现","message":"not implemented","retryable":false}}
transport: /api/jobs.stop
    task_actions_test.go:79: jobs.stop 旧实现/错误参数 status=501 want=400 body={"error":{"code":"not_implemented","hint":"宿主这一侧还没接线，换一个 method 或等它实现","message":"not implemented","retryable":false}}
--- FAIL: TestTaskActionArgumentsFailBeforeCapability (0.00s)
FAIL
FAIL	dshgo/internal/transport	0.095s
FAIL
[exit code: 1]
```

真实链开发中还先抓到了两个非预期红：每轮 `modelOverrideLoopRunner` 漏拷 broker（运行中 output 404），以及任务刚注册尚无输出时游标越界 500。分别修复逐轮接线和明确 400/测试等待真实输出后，才进入下面的绿色验收；这些红没有算作正常“未知任务”分支。

## 4. 绿色行为证据

### store 与 Transport

- `TestDeletePersistsAcrossReopenAndRepeatedDelete`：删除后重开、未知/重复删除、保留未删条目的字段。
- `TestDeleteRevisionConflictAndCorruptionDoNotOverwrite`：冲突和损坏文件原字节不变。
- `TestClearEndedReadbackABAndEmpty`：A/B home 隔离、保留活跃、空目录。
- `TestClearEndedPreflightAndWriteFailureRollback`：坏文件全量预检；第二个文件写失败回滚第一个文件。
- `TestJobsOutputCursorAndLimitAreEnforced` / `...UsesRunesWithoutSplittingUTF8` / `...RejectsCursorBeyondCurrentOutput`：分页、有界、UTF-8 与错误游标。
- `TestTaskActionsGoldenOverHTTP`：8 组 additive 成功/四字段错误真实 HTTP 重放。

### 真实进程

`TestTaskActionsRealDSHDReadyTokenScheduleAB` 构建真实 dshd，启动两个临时 home 的 serve，读取 ready token、换 cookie，验证未认证 401、list 真值、删除、重复删除、revision 冲突、clear-ended/readback 与 A/B 隔离。

`TestJobActionBrokerRunningEndedUnknownCrossSessionAndSecondStop` 使用临时 home helper，覆盖两页输出、未知 id、跨会话拒绝、已结束拒绝、第一次/第二次 stop、停止后输出、helper 退出后 readback、B home 不可见与错误映像拒绝。

`TestJobsRealDSHDOutputStopAndOwnedProcessTree` 经真实 dshd ready token→cookie→cmdRun→bash 启动 helper parent→child→grandchild：模型工具调用真起后台任务，HTTP 以 rune 页读取 `A甲🙂乙B`，跨会话 403、未知 404、首次 stop 200、第二次 409，并逐个核对三个登记 helper PID 消失；停止后与退出后均读回 `乙B`，同游标重复读取一致。

W70 另以 `TestServeJobsOutIntegrationPrefersPagerAndController` / `...LegacyWithoutPagerOrController` 分别证明新能力优先和旧能力 fallback；`...PagerWaitPreservesLegacyEndedWait` 证明已有输出时仍等待 ended；`...PagerTruncationAndEndedIdempotence` 与 `...RealW69RetentionWindow` 覆盖假/真 Pager 的截断、CJK/emoji 跨字节页、ended 幂等和 1 MiB 有界窗口；`...CrossInstanceKeepsCrossSessionCode` 与 `...PagerWindowIsBounded` 覆盖错误码和适配硬上限。

## 5. W67 历史门禁原始日志

以下是 W67 交付时的实跑汇总（不是 W70 本次门禁；W70 原样输出见 §7）：

```text
TARGETED_GREEN_EXIT=0
ok  dshgo/internal/schedule  0.450s
ok  dshgo/internal/transport 0.112s
ok  dshgo/cmd/dshd           13.416s

GO_TEST_EXIT=0
GO_VET_EXIT=0
GO_BUILD_EXIT=0

# go test -race ./internal/schedule ./internal/transport ./cmd/dshd -count=1
ok  dshgo/internal/schedule  1.509s
ok  dshgo/internal/transport 2.364s
ok  dshgo/cmd/dshd           280.778s
[status: completed, exit code: 0]
```

证据目录：`tmp/task-actions-final/`（本任务产物）。完整原始输出见：

- `red-transport.log`
- `targeted-green.log`
- `targeted-race.log`
- `related-race.log`
- `all-test-vet-build.log`
- `canonical-hashes.txt`

最终 patch 与哈希由任务指定的绝对路径交付：`C:/Users/Administrator/AppData/Local/Temp/task-actions-final.patch`、`task-actions-final.patch.sha256`。

## 6. 边界

- 本包证明宿主存储、Transport、父子进程协议与真实 dshd 行为；不证明可见设置/任务页已接按钮，未启动 GUI。
- `schedule.clear` 不推断 unknown 状态为 ended；现有周期任务没有 Host ended 真值时会保留，这是刻意的安全语义。
- 保留输出证明当前 home/当前会话在停止和 cmdRun 退出后可读；没有把它外推成跨 home 共享或全局任务恢复服务。
- 测试没有重启/关闭 DSH 或 magpie，没有按镜像名清理进程，也没有改 profiles/asar/原始会话。

## 7. W70 集成先红后绿证据

### 7.1 旧共享实现真实先红

命令：

```bash
go test ./cmd/dshd -run 'TestServeJobsOutIntegration' -count=1 -v
```

以下是未改实现时的原样输出；旧无 Pager/Controller 兼容用例同轮真实 PASS，另外四项真实 FAIL，不是编译错误造红：

```text
=== RUN   TestServeJobsOutIntegrationPrefersPagerAndController
    jobs_integration_test.go:230: 具备 spi.JobPager 时仍走了旧 Output，字节游标能力没有接入
    jobs_integration_test.go:238: spi.JobController.Stop 调用次数=0，want=1
    jobs_integration_test.go:241: 具备 Controller 时旧 Kill 被调用 1 次，want=0
--- FAIL: TestServeJobsOutIntegrationPrefersPagerAndController (0.03s)
=== RUN   TestServeJobsOutIntegrationLegacyWithoutPagerOrController
--- PASS: TestServeJobsOutIntegrationLegacyWithoutPagerOrController (0.04s)
=== RUN   TestServeJobsOutIntegrationPagerTruncationAndEndedIdempotence
    jobs_integration_test.go:284: 截断用例没有使用 Pager
    jobs_integration_test.go:288: 保留前缀已淘汰但 HTTP 未明确 truncated=true: map[id:job-ended output:map[cursor:1 done:true exit_code:0 hasMore:true id:job-ended nextCursor:2 output:🙂 sessionId:session-integration total:3] requestId:integration-request sessionId:session-integration]
--- FAIL: TestServeJobsOutIntegrationPagerTruncationAndEndedIdempotence (0.01s)
=== RUN   TestServeJobsOutIntegrationCrossInstanceKeepsCrossSessionCode
    jobs_integration_test.go:303: 跨实例 Stop=false code="not_found"，want=cross_session error="后台任务属于另一个 exec 服务实例"
    jobs_integration_test.go:303: 跨实例 Stop=true code="not_found"，want=cross_session error="后台任务属于另一个 exec 服务实例"
--- FAIL: TestServeJobsOutIntegrationCrossInstanceKeepsCrossSessionCode (0.02s)
=== RUN   TestServeJobsOutIntegrationPagerWindowIsBounded
    jobs_integration_test.go:314: 超过适配上限的 Pager 窗口必须 fail-loud，不能无界读取或伪造页面: {RequestID:integration-request SessionID:session-integration ID:job-huge Output:0x3a62ac68cb0 Stopped:false Code: Error:}
    jobs_integration_test.go:317: 有界检查必须发生在 Pager 元数据探测之后
--- FAIL: TestServeJobsOutIntegrationPagerWindowIsBounded (0.01s)
FAIL
FAIL	dshgo/cmd/dshd	0.205s
FAIL

RED_TEST_EXIT=1
[exit code: 1]
```

真实多级链第一次到达 stop 时还抓到 Pager 与旧 `Output(wait)` 的等待语义差异，原样关键输出为：

```text
serve_jobs_e2e_test.go:115: POST jobs.stop status=500 want=200 body={"error":{"code":"internal","hint":"可以直接重试；反复失败就看 dshd 的 stderr","message":"子进程未确认任务停止完成","retryable":true}}
--- FAIL: TestJobsRealDSHDOutputStopAndOwnedProcessTree (18.90s)
FAIL
FAIL	dshgo/cmd/dshd	19.010s
FAIL

REAL_TREE_TEST_EXIT=1
[exit code: 1]
```

没有放宽 `stopped/done` 断言；修复为先在 Pager 当前 total 等 ended，再组装保留快照。大输出测试还实测确认最终保留文本可能因 ended 哨兵而略小于 1 MiB，`Metadata.Total-RetainedOffset` 与旧 `Output` 字节数一致，文档和判据均按这个事实书写。

### 7.2 最终相关包与全仓门禁

相关包原样汇总：

```text
ok  	dshgo/internal/transport	0.986s
ok  	dshgo/modules/exec/bash	7.726s
ok  	dshgo/cmd/dshd	38.832s

RELATED_TEST_EXIT=0
```

全仓原样退出标记（完整逐包输出在日志）：

```text
GO_TEST_EXIT=0
GO_VET_EXIT=0
GO_BUILD_EXIT=0
[status: completed, exit code: 0]
```

相关 race 原样输出（MinGW GCC 16.2.0，非“工具链缺失=跳过”）：

```text
CC=/c/Users/Administrator/AppData/Local/Temp/jobs-process-tree-mingw-tool/mingw64/bin/gcc.exe
gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r3) 16.2.0
$ go test -race ./internal/transport ./modules/exec/bash ./cmd/dshd -count=1
ok  	dshgo/internal/transport	2.152s
ok  	dshgo/modules/exec/bash	12.650s
ok  	dshgo/cmd/dshd	214.608s
RELATED_RACE_EXIT=0
[status: completed, exit code: 0]
```

W70 原始日志均在本机任务产物目录 `C:/Users/Administrator/AppData/Local/Temp/`：

- `jobs-integration-red.log`
- `jobs-integration-targeted.log`（含首次测试归属路径红，未算作绿）
- `jobs-integration-real-tree.log`（真实 stop done 等待红）
- `jobs-integration-real-tree-2.log`（三级 helper 全退出绿）
- `jobs-integration-real-retention.log` / `...-2.log`（测试初判与真实窗口不符的红）
- `jobs-integration-real-retention-3.log`（真实 W69 保留窗口绿）
- `jobs-integration-related-green.log`
- `jobs-integration-all-test-vet-build.log`
- `jobs-integration-related-race.log`

最终增量补丁为 `C:/Users/Administrator/AppData/Local/Temp/jobs-integration-final.patch`，同目录 `.patch.sha256` 记录其 SHA-256。补丁起点是上述共享状态中的 W70 触碰文件，不重复携带 W67/W69 已集成实现。
