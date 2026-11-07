# W69 · Jobs 有界输出与可验证进程树

## 范围与结论

本改动只补 `internal/spi` 与 `modules/exec/bash` 的执行层事实，不改 Transport、`cmd/dshd`、schedule、settings 或 model。

- 旧 `spi.Job.Output/Kill/List` 签名保留；新增可选能力 `spi.JobPager`、`spi.JobController`。
- 后台任务输出按绝对字节游标分页，每任务最多保留最后 **1 MiB**，每页默认 64 KiB、最大 256 KiB，单次 wait 最大 30 秒。
- Windows 每个 bash 会话使用一个匿名 Job Object；Linux/Darwin 使用独立 process group。停止前重新核对 root PID、创建代次和映像。
- `Service.Close` 收掉当前会话、后台会话和终端会话；同一 session 去重。任务记录及已保留输出仍可读。
- 不把 root-only `Process.Kill` 称作进程树完成。旧 `Kill` 只是同一安全 `Stop` 的兼容别名。

## Additive SPI

```go
type JobPager interface {
    OutputPage(id string, cursor int64, limit int, wait time.Duration) (JobOutputPage, error)
}

type JobController interface {
    Stop(id string) error
    Metadata(id string) (JobMetadata, error)
}
```

原有 `Job` 不变，调用方按需断言：

```go
pager, ok := job.(spi.JobPager)
controller, ok := job.(spi.JobController)
```

`RunBackground` 即使命令很快结束也返回 job id，因此快速任务的输出和元数据不会从任务表消失。

## 输出页与游标

`JobOutputPage` 字段：

| 字段 | 含义 |
|---|---|
| `offset` | 本页第一个字节的绝对偏移 |
| `nextCursor` | 下一页应传入的游标；`offset + len(output)` |
| `total` | 任务产生过的用户输出总字节数，含已淘汰前缀，不含内部结束哨兵 |
| `truncated` | 保留窗口已淘汰前缀；旧游标不再假装仍指向完整历史 |
| `done` / `exitCode` / `cwd` | 同一 task 的结束快照 |

规则：

1. `cursor` 和 `limit` 都按字节，不按 rune；`cursor=0` 从头读，`limit=0` 使用 64 KiB。
2. `limit<0` 报错；超过 256 KiB 被钳制。`wait<0` 当作 0，超过 30 秒被钳制。
3. `cursor<0` 或 `cursor>total` 返回 `ErrJobInvalidCursor`。
4. 当 `cursor<retainedOffset`，从 `retainedOffset` 返回并置 `truncated=true`。例如 1 MiB+123 字节后，游标 0 返回 `offset=123`，不是伪造成完整第 0 字节。
5. 有新输出或任务结束时，长轮询立即返回；结束哨兵可能在新输出之后另一次到达，调用方应继续用 `nextCursor` 读到 `done=true`。
6. 结束后缓冲不再变化，同一游标重复读取得到同一页；`cursor=total` 返回空输出和最终 `done/exitCode`。
7. 旧 `Output` 仍返回字符串并裁掉末尾换行，但它只能给出保留窗口内的内容；需要 `total/truncated` 时必须用 `OutputPage`。

实现使用 close-and-replace channel 广播通知，多个读者不会互相消费同一个信号；task、Service 和 processControl 各有自己的锁。

## 实例、会话与进程元数据

任务 id 为 `job-<owner>-<seq>`。`owner` 是每次 `NewService` 生成的 128-bit 实例代次，因此不同 Service 不再都生成 `job-1`。把 A 的完整 id 传给 B：

- A/B 不可能命中同一个本地 id；
- B 返回 `ErrJobWrongInstance`，不会杀 B 自己的任务；
- 本实例但不存在的 id 返回 `ErrJobNotFound`。

`Service` 天然属于一个 kernel Scope。`SessionScope` 只记录装配时真实的 `Scope.Name()`；裸 `NewService` 没有外部 session id，该字段为空，不伪造会话归属。`OwnerID` 才是停止隔离边界。

`JobInfo` / `JobMetadata` 的真实字段：

| 字段 | Windows | Linux | Darwin |
|---|---|---|---|
| `root_pid` | 本次启动的 bash PID | 同左 | 同左 |
| `root_image` | `QueryFullProcessImageName` | `/proc/<pid>/exe` | `ps -o comm=` |
| `creation_id` | `GetProcessTimes` FILETIME | `/proc/<pid>/stat` field 22 starttime | `sysctl kern.proc.pid` 的秒+微秒 |
| `process_tree` | `windows-job-object` | `unix-process-group` | `unix-process-group` |
| `process_group_id` | 0 / 省略 | root PID | root PID |

`List` 与 `Metadata` 共用同一构造路径，不补猜不到的字段。输出保留起点另见 `retainedOffset/total/truncated`。

## 安全停止与收工

### Windows

`startSession` 在向 bash stdin 写任何用户命令之前：

1. `CreateJobObject(nil, nil)` 创建匿名 Job；
2. 设置 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`；
3. 仅对刚启动的 root PID 打开句柄并 `AssignProcessToJobObject`；
4. 查询并保存 root 映像和创建 FILETIME。

`Stop` 再按 PID 查询，要求 active + 创建代次相同 + 映像相同，然后 `TerminateJobObject`。不是镜像名匹配，不是全局 `taskkill`，也不调用 `/T`。Job 内由 bash/helper 派生的普通子进程随 Job 一起终止；句柄关闭同样收工。

### Unix

启动设置 `Setpgid=true`，root PID 即独立 PGID。`Stop` 核对创建代次和映像后才对 **负 PGID** 发 `SIGKILL`，不对单个镜像名或系统进程表做 kill。Dispose 只在 root 身份仍匹配时给 PGID 发信号，避免已消失 PGID 被复用后误伤。

### 拒绝与幂等

- 重复 stop、自然结束后 stop：`ErrJobFinished`；
- root 已退出：`ErrJobProcessExited`（若 task 已先结算，则公共 Stop 返回 `ErrJobFinished`）；
- 创建代次或映像变化：`ErrJobProcessReused`；
- 跨实例：`ErrJobWrongInstance`；未知：`ErrJobNotFound`；
- 无法查询或建立安全归属：`ErrProcessTreeUnsafe`，启动/停止 fail closed。

PID 复用测试通过篡改保存的创建代次、同时保持真实 PID 存活来确定性击中身份不匹配分支，并断言真实进程未被杀；没有宣称能强迫 OS 按需复用某个 PID。

## 真实测试与红绿证据

新增测试覆盖：分页边界、结束后幂等读取、wait 输出/done 两阶段、保留窗口截断页、重复 stop、未知、跨实例、自然结束、PID 代次不匹配、真实 root 已退出、真实元数据、8 个并发读者与 Stop、真实 helper 父子树 Stop/Dispose。

真实 helper 在 `t.TempDir()` 生成源码并构建。parent 只启动同目录的自身 child，双方写入 PID 和 heartbeat；测试记录 PID、完整映像和创建代次。收尾先由所属 Service 收树，备用清理也只终止身份仍匹配的已记录 helper PID。

### 先红

1. 新测试在旧实现上编译失败，原样输出存于 `C:\Users\Administrator\AppData\Local\Temp\jobs-process-tree-red-initial.txt`：`svc.OutputPage undefined`、`b.Stop undefined`、`undefined: processIdentity`。
2. 在独立临时副本 `jobs-process-tree-root-only-mutation` 只把 `Stop` 换回旧 root-only 杀法，测试真实失败，原样输出存于 `jobs-process-tree-red-root-only.txt`：
   - `TestStopRejectsPIDReuse` 得到 `err=nil`；
   - `TestRealHelperProcessTreeStopAndDispose/stop` 中 bash root 退出，但 helper parent 仍 `Active:true`；
   - 同测试的 Dispose 分支通过，说明断言能区分根关闭与 Job 树收工。
3. 红测遗留 parent PID 22212、child PID 39824 经分别只读 `tasklist /FI` 检查均“没有运行的任务匹配指定标准”。
4. 集成首轮还发现旧错误文案兼容回归：`TestJobToolsRejectEmptyID` 红；恢复“没有任务”后重跑通过。输出在 `jobs-process-tree-targeted-1.txt`。

### 绿

以下均为本工作树实跑，不是从历史记录复制：

```text
go test ./internal/spi ./modules/exec/bash -count=3
ok dshgo/internal/spi
ok dshgo/modules/exec/bash 29.249s

go test ./... -count=1
全部包 ok / [no test files]，exit 0

go vet ./...
exit 0（无输出）

go build ./...
exit 0（无输出）

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c ... ./modules/exec/bash
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -c ... ./modules/exec/bash
LINUX_DARWIN_COMPILE_OK

CGO_ENABLED=1 go test -race ./internal/spi ./modules/exec/bash -count=1
ok dshgo/internal/spi 1.053s
ok dshgo/modules/exec/bash 7.987s
```

race 使用临时目录中经发布方 SHA256 校验的 MinGW GCC 16.2.0；本机原先 `gcc: command not found`。Zig 链接探测及 TSan 初始化失败原样保留在 `jobs-process-tree-race-zig.txt`、`jobs-process-tree-race-link-probe.txt`，最终绿以 `jobs-process-tree-race-mingw.txt` 为准，未把失败探测写成通过。

原始全量输出分别在 Temp 下 `jobs-process-tree-targeted-3.txt`、`jobs-process-tree-final-full-test.txt`、`jobs-process-tree-final-full-vet.txt`、`jobs-process-tree-final-full-build.txt`、`jobs-process-tree-race-mingw.txt`。定向 verbose 输出 `jobs-process-tree-targeted-1.txt` 记录了真实 root/helper parent/helper child PID 与创建代次及三者退出。

## 尚未由本层改变的接线边界

- `cmd/dshd`/Transport 仍使用旧全量 `Output` 和旧投影字段；要暴露分页/停止能力，需后续接线任务显式消费 `JobPager/JobController`，本任务没有偷改 Transport。
- 当前 `modules/exec/bash/tools.go` 的 `job_kill` Hint 仍写“只杀掉 bash 进程”，集成时应由该文件 owner 改为进程树语义。
- 本层保证 job 结束后、同一 Service 仍存活时可读，并保证 Dispose 收工；不把任务输出持久化到宿主进程重启之后。既有 serve 子进程退出后无法查询的旧 Transport 生命周期不因这份执行层 patch 自动改变。
