# W77 · Jobs 自然退出后的最终输出持久化

> 日期：2026-11-05。基线：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当前共享工作树（含 W69/W70/W76）。
> 结论：`cmdRun` 内后台 job 自然成功或自然失败/异常退出后，即使从未调用 `jobs.stop`，最终 `done/exitCode`、保留输出和 rune 页仍能在 `cmdRun` 进程退出后由另一个 serve 经 HTTP `jobs.output` 重复读取。读取、注入和写盘失败均返回错误，不再静默丢弃。

## 1. 范围与实现

本任务相对当前共享基线只改：

- `cmd/dshd/main.go`：在 `root.Dispose()` **之前**取得 `spi.Job` 实例；Dispose 收掉进程树后，使用该实例读取最终任务快照。注销或读取失败汇入命名返回值 `retErr`。
- `cmd/dshd/jobs_out.go`：`persistJobOutputs` 改为接收已保留的 `spi.Job` 并返回 `error`；各任务的读取/写盘错误带任务 id 聚合返回。
- `cmd/dshd/jobs_natural_exit_test.go`：新增真实成功/失败红测与 nil-service/read/write fail-loud 测试。
- 本文。

没有修改 `jobs_actions.go`、`serve_loop.go`、Transport canonical、UI 或 `modules/exec/bash`。与共享基线逐文件 `cmp` 的结果为：

```text
UNCHANGED cmd/dshd/jobs_actions.go
UNCHANGED cmd/dshd/serve_loop.go
UNCHANGED modules/exec/bash/service.go
UNCHANGED modules/exec/bash/session.go
UNCHANGED modules/exec/bash/process_windows.go
UNCHANGED modules/exec/bash/process.go
```

### 根因

`kernel.Provide` 注册的 disposer 会从部署级服务表删除 `spi.JobKey`。旧 `cmdRun` defer 先调用 `root.Dispose()`，再调用 `persistJobOutputs(root, …)`；后者此时 `Inject` 必得 `ErrMissingService`，而旧实现遇到该错误或 nil 服务直接无错误返回。因此只有 `jobs.stop` 已经写盘的记录可读，自然退出路径没有最终快照。

`modules/exec/bash.Service.Close` 关闭会话但保留任务表，故正确顺序是：**先保留服务实例 → Dispose 等收工 → 从任务表读最终快照并原子写盘**，而不是 Dispose 后重新注入。

## 2. 真实红测：先红，且成功/失败两条都红

测试：`TestJobsRealDSHDNaturalExitReadback`。

两条真实命令：

```bash
printf 'NATURAL_SUCCESS_A甲🙂乙B'; sleep 0.2
printf 'NATURAL_FAILURE_失敗🙂乙B'; sleep 0.2; exit 7
```

测试只调用 `jobs.output`，从不调用 `jobs.stop`。它先在 cmdRun 内等 job 自然结束并核对原始输出与退出码，再放行模型响应、`cmd.Wait()` 等 cmdRun 真正退出，最后启动一个没有该 live run 的新 serve，经 HTTP 读回。旧实现原样输出（完整日志：`C:/Users/Administrator/AppData/Local/Temp/jobs-natural-exit-w77-evidence/red-natural-before.txt`）：

```text
=== RUN   TestJobsRealDSHDNaturalExitReadback/success
    jobs_natural_exit_test.go:149: NATURAL_JOB session=session-natural-success id=job-7fe5f059668fd5c2-1 done=true exit=0 output="NATURAL_SUCCESS_A甲🙂乙B"
    jobs_natural_exit_test.go:173: POST jobs.output status=404 want=200 body={"error":{"code":"not_found","hint":"检查请求路径与 HTTP 方法；要找的会话/文件/id 可能已经不在了（先 session.list 或 workspace.list 确认）","message":"no such job: session=session-natural-success id=job-7fe5f059668fd5c2-1","retryable":false}}
=== RUN   TestJobsRealDSHDNaturalExitReadback/failure
    jobs_natural_exit_test.go:149: NATURAL_JOB session=session-natural-failure id=job-c33fbc9f83cc773d-1 done=true exit=7 output="NATURAL_FAILURE_失敗🙂乙B"
    jobs_natural_exit_test.go:173: POST jobs.output status=404 want=200 body={"error":{"code":"not_found","hint":"检查请求路径与 HTTP 方法；要找的会话/文件/id 可能已经不在了（先 session.list 或 workspace.list 确认）","message":"no such job: session=session-natural-failure id=job-c33fbc9f83cc773d-1","retryable":false}}
--- FAIL: TestJobsRealDSHDNaturalExitReadback (5.26s)
    --- FAIL: TestJobsRealDSHDNaturalExitReadback/success (0.51s)
    --- FAIL: TestJobsRealDSHDNaturalExitReadback/failure (0.50s)
FAIL
FAIL	dshgo/cmd/dshd	5.357s
FAIL
```

失败分支的 `exit=7` 和失败文本都是命令实际产生的事实，没有写成成功，也没有用预期输出替换非预期输出。

## 3. 修复后原样输出

自然成功/失败 readback（`green-natural-after-first.txt`）：

```text
=== RUN   TestJobsRealDSHDNaturalExitReadback
=== RUN   TestJobsRealDSHDNaturalExitReadback/success
    jobs_natural_exit_test.go:149: NATURAL_JOB session=session-natural-success id=job-a96d0c8ca4774914-1 done=true exit=0 output="NATURAL_SUCCESS_A甲🙂乙B"
=== RUN   TestJobsRealDSHDNaturalExitReadback/failure
    jobs_natural_exit_test.go:149: NATURAL_JOB session=session-natural-failure id=job-a691a615ef1d7e8d-1 done=true exit=7 output="NATURAL_FAILURE_失敗🙂乙B"
--- PASS: TestJobsRealDSHDNaturalExitReadback (4.18s)
    --- PASS: TestJobsRealDSHDNaturalExitReadback/success (0.50s)
    --- PASS: TestJobsRealDSHDNaturalExitReadback/failure (0.49s)
PASS
ok  	dshgo/cmd/dshd	4.247s
```

断言同时覆盖：最终 `done=true`、原样输出、真实 `exit_code`、HTTP 重复读取整包一致、CJK/emoji rune 分页、`home/state/jobs/<session>/<job>.json` 可读。

fail-loud（`persist-fail-loud.txt`）：

```text
=== RUN   TestPersistJobOutputsFailLoud
=== RUN   TestPersistJobOutputsFailLoud/nil-service
=== RUN   TestPersistJobOutputsFailLoud/read
=== RUN   TestPersistJobOutputsFailLoud/write
--- PASS: TestPersistJobOutputsFailLoud (0.01s)
    --- PASS: TestPersistJobOutputsFailLoud/nil-service (0.00s)
    --- PASS: TestPersistJobOutputsFailLoud/read (0.00s)
    --- PASS: TestPersistJobOutputsFailLoud/write (0.00s)
PASS
ok  	dshgo/cmd/dshd	0.066s
```

## 4. `0xC0000142` 启动异常调查

### 原始失败事实（不改写）

Lead 证据：`D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/real-chain-current-wjxbvezg/` 与 `C:/Users/Administrator/AppData/Local/Temp/real-chain-current-lead-driver.log`。

- cmdRun PID `35212`；bash root PID `18936`。
- root 映像：`C:/Program Files/Git/usr/bin/bash.exe`；`creation_id=1dd6ccdf4c2b7cd`；`process_tree=windows-job-object`。
- `wire-0083.json` 记录 `done=true`、`exit_code=3221225794`（`0xC0000142`）、`total=167`，首页是 5 个空格；不是预期的 `A甲🙂乙B`。
- `job-owned-pids.json` 为 `parent=0 child=0 grandchild=0`，helper 未启动。
- 原 home 没有 `state/jobs/...` 文件；cmdRun 结束后 `jobs.output=not_found`。

真实启动/输出路径已核对：`startSession` 以 `--noprofile --norc -s` 启动 bash，stdout/stderr 共用一根 `os.Pipe`；`attachProcessTree` 创建匿名 Job Object、设置 `KILL_ON_JOB_CLOSE` 并绑定刚启动的 root PID；`waitLoop` 使用 `exec.ExitError.ExitCode()` 保留 OS 退出码，`readLoop` 将实际字节追加到有界任务缓冲。该 `3221225794` 因而没有被本任务改写为成功。

### 本轮复跑事实

- 修复前现有 W70 stop/三级树基线先跑通过：parent `26148`、child `3328`、grandchild `41148` 均真实启动并退出（`baseline-stop-tree.txt`）。
- 三轮完整 W76 的 current jobs 均为 `OK=76 FAIL=0`，每次都得到三级 PID、停止整树并在 cmdRun 退出后幂等读回。
- 另外 `TestJobsRealDSHDNaturalExitReadback` + `TestJobsRealDSHDOutputStopAndOwnedProcessTree` 连续 `-count=3` 全部通过（`real-jobs-repeat-3.txt`）。自然失败继续原样保留 `exit=7`。
- 本轮日志没有 `3221225794` / `0xC0000142`。

因此本轮**没有复现可归属到产品启动、继承句柄或 Job Object 实现的缺陷**，不改 `modules/exec/bash`。同时也不把“外部 OS 瞬时失败”伪装成已证实根因：当前准确结论是“原始启动异常保留证据，本轮多次真实重跑未复现”。没有简单重试后删红、放宽断言或把失败输出改写成预期成功。

## 5. W76 完整真实整链

本次新二进制：

```text
SHA256 9fc4558a8f11b6c65647bd41bd905c59ce5323ee2186b0231486268ea6103334
  OK   1 个二进制都不比源码旧
合计 OK=1 FAIL=0
```

命令均为：

```bash
python scripts/real-chain-headless.py \
  --binary C:/Users/Administrator/AppData/Local/Temp/jobs-natural-exit-w77-evidence/bin/dshd.exe \
  --ui C:/Users/Administrator/AppData/Local/Temp/dsh-ui-current-chain
```

| 轮次 | 证据目录（本隔离副本 `tmp/` 下） | core | current | 合计 / 脚本退出 |
|---|---|---:|---:|---|
| 1 | `real-chain-current-urp3de0q` | 77/0 | 76/0 | **153/0，exit 0** |
| 2 | `real-chain-current-xn044eu2` | 79/0 | 76/0 | 155/0，exit 0 |
| 3 | `real-chain-current-6f3g2v3k` | 75/2 | 76/0 | 151/2，exit 1 |

第一轮原样汇总：

```text
RCH_SUMMARY OK=77 FAIL=0 SKIP=0
RCC_SUMMARY OK=76 FAIL=0 SKIP=0
CORE_RESULT OK=77 FAIL=0 SKIP=0 combined_go_test_exit=0
CURRENT_RESULT OK=76 FAIL=0 SKIP=0 combined_go_test_exit=0
SECRET_SCAN_OK literals=3 leaked_files=0
CORE_OK=77 CORE_FAIL=0 CURRENT_OK=76 CURRENT_FAIL=0
OK=153 FAIL=0 SKIP=0
CLEANUP {"dshd_exit": 1, "owner_verified": true, "taskkill_exit": 0, "home_entries": 32}
```

第二轮真实模型重复发起一次 deny 审批，探针按实际审批数记为 79，不伪写为 77；jobs 仍 76/0。第三轮真实模型响应及 history 没有 reasoning，原有 `real-reasoning-through-ui-chunks` 与 `history-preserves-reasoning` 两项真红；jobs 仍 76/0。这三轮原始完整输出分别保存在 `real-chain-run1.txt`、`real-chain-run2.txt`、`real-chain-run3.txt`，未用后续重跑覆盖。

第一轮的最终保留文件也实际存在：`home/state/jobs/84b534d4-8bf5-eda1-66a4-a51304c199e0/job-a70c5543727fc973-1.json`，内容为 `output=A甲🙂乙B done=true exit_code=1 total=5`。

## 6. 全仓门禁与相关 race

全仓命令：`go test ./... -count=1`、`go vet ./...`、`go build ./...`。原样汇总：

```text
GO_TEST_EXIT=0
GO_VET_EXIT=0
GO_BUILD_EXIT=0
```

其中 `cmd/dshd` 全量测试 `61.121s`，`modules/exec/bash` `8.663s`；逐包完整输出在 `full-test.txt`，vet/build 完整输出分别在 `full-vet.txt`、`full-build.txt`。

相关 race 使用 `C:/toolchains/mingw64/bin/gcc.exe`（GCC 16.2.0）与 `CGO_ENABLED=1`：

```text
gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r3) 16.2.0
ok  	dshgo/cmd/dshd	228.686s
ok  	dshgo/modules/exec/bash	6.918s
ok  	dshgo/internal/transport	2.095s
RELATED_RACE_EXIT=0
```

## 7. 证据、补丁与清理边界

原始证据根：`C:/Users/Administrator/AppData/Local/Temp/jobs-natural-exit-w77-evidence/`。

- 红：`red-natural-before.txt`；绿：`green-natural-after-first.txt`、`persist-fail-loud.txt`。
- 启动/进程树：`baseline-stop-tree.txt`、`real-jobs-repeat-3.txt`。
- 整链：`real-chain-run1.txt`、`real-chain-run2.txt`、`real-chain-run3.txt` 及上述各 `tmp/real-chain-current-*` 目录。
- 门禁：`full-test.txt`、`full-vet.txt`、`full-build.txt`、`related-race.txt`、`gates-summary.txt`、`kernel-deps.txt`。

补丁：`C:/Users/Administrator/AppData/Local/Temp/jobs-natural-exit-final.patch`；SHA-256 在同目录 `jobs-natural-exit-final.patch.sha256`。

未 commit/push，未启动 GUI，未重启或关闭 DSH/magpie，未改 Transport/UI。清理只发生测试或 W76 驱动对自己启动并核对映像的 PID；没有按镜像名批杀，也没有移动/删除/改名非本任务产物。
