# serve 会话生命周期：权限覆盖、取消与删除

> 范围：0.2.2 审计的生命周期缺口，基线 `af4a49a` + 共享导出。本文只记录
> `cmd/dshd` serve 的宿主语义；Transport 契约与 v4 格式均未改变。

## 根因

1. **权限只接了一半。** `session.load` / `session.setPermission` 使用
   `permissionOverrideStore`，但默认执行路径 `modelOverrideLoopRunner.Run` 每轮复制
   `base.policy`。因此界面可以显示 `allow/ask/auto`，下一次 `dshd run` 却仍收到
   启动时的 `--policy`。
2. **删除把“请求取消”误当成“已经收工”。** 旧 `session.delete` 直接对会话目录
   `RemoveAll`，没有取消并等待 live turn；即使另行调用 Cancel，旧实现等待超时后仍
   清 busy。运行中的子进程、工具最后一次写 v4、写锁清理因而可能与删除并发。
3. **取消超时伪造了空闲。** `liveTurn.done` 本该是 runner 真返回后的唯一收工信号，
   旧 `promptService.Cancel` 却在 15 秒超时后继续 `clear`。后续 prompt 可以插队，
   而原工具仍未退出，busy 与真实资源状态相反。

## 修复后的不变量

### 每轮权限

- 生产路径的 `sessionSource` 与 `modelOverrideLoopRunner` 持有**同一个**
  `permissionOverrideStore`；load、set 与下一次执行不再有第二份内存真相。
- 每轮新建轻量 `loopRunner`，只把该会话解析出的权限写入 `next.policy`，再透传为
  子进程 `--policy`。`base.policy` 从不改写，A 的覆盖不会污染 B 或后续默认值。
- 空覆盖回到 `--policy` 默认值；`deny/ask/allow/auto` 的取值白名单和
  `<home>/state/session-permission.json` 持久化格式不变。重启仍由现有 `Load`
  恢复；不写 v4。
- 直接构造 runner 且未注入 store 的旧测试/内部调用者按同一 JSON 文件解析；
  生产服务不使用这条旁路。

### Cancel

1. 先按 `dfd73da` 语义拒掉该会话的 pending approval，推 `approval/cancel`，再调用
   live turn 的 cancel；审批拒绝不会让工具获得允许。
2. 等 `liveTurn.done`。只有 runner 返回后的 defer 才清 busy，然后关闭 done；
   正常结束、错误、取消共用这一出口。
3. 等待超时返回可由 `errors.Is` 识别的 `ErrCancelTimeout`，**保留** `bySID` 中的
   原 turn 与 busy。此时新 prompt 仍得到 `ErrBusy`；工具最终结果仍可由原 runner
   写入/发布，runner 最终返回后状态自动恢复为空闲。
4. `cancelServeTurn` 识别“pending approval 已拒，但同一 turnID 仍活动”的组合情况，
   恢复超时错误。旧 `cancelTurn` 为“审批已拒且轮次恰好结束”吞掉 `ErrNoSuchTurn`
   的兼容语义不变。

### Delete

顺序固定为：

```text
FindByID → beginDelete（挡新 prompt / 并发 delete）
→ cancelServeTurn → 等 runner 真收工 → 再次 Stat
→ RemoveAll(会话目录) → release 删除屏障
```

- 删除屏障覆盖“旧轮刚收工、目录尚未删除”的窗口，防止新轮在此处启动。
- 活动轮次超时：返回包装 `ErrCancelTimeout`，目录和 busy 都保留，可在真实收工后
  重试 delete；绝不返回 `{deleted:true}`。
- 不存在：返回包装 `transport.ErrNoSuchSession`，保持既有 404 映射。
- 已完成/本来空闲：没有 live turn 是可删除状态，正常删除；但直接 Cancel 一个已完成
  会话仍返回 `ErrNoSuchTurn`，不伪称取消过一轮。
- `RemoveAll` 前再次 `Stat`，避免等待期间被外部删除后利用 `RemoveAll` 对不存在路径
  返回 nil 的特点假报成功。并发 delete 或屏障期间的新 prompt 返回 `ErrBusy`。

这里的“真实收工”以 `turnRunner.Run` 返回为边界：默认 `loopRunner` 在该边界前等待
子进程、follow/stderr 收尾并执行既有的 turn/end 与写锁清理。无法响应取消的工具可以
让会话持续 busy；宿主不会用超时强行抹掉状态，也不会在工具仍运行时删除目录。

## 测试

新增/扩展的行为测试：

- `TestDefaultLoopRunnerSessionPermissionOverrideLifecycle`：load 投影与真实子进程
  argv 一致；A/B、base、清空、重启分别断言。
- `TestServeDeleteWaitsForLiveToolBeforeRemoveAll`：工具被阻塞时 delete 不返回、目录
  仍在；释放后最后写入先成功，delete 才删除目录；删除窗口新轮被挡。
- `TestServeDeleteTimeoutKeepsSessionAndLiveTurn`：超时返回 `ErrCancelTimeout`，目录、
  busy 与最终结果保留；真收工后重试删除成功。
- `TestServeDeleteCompletedAndMissingSession`：空闲删除、完成后 Cancel、缺失会话错误。
- `TestCancelTimeoutKeepsLiveTurnAndFinalToolResult`：Cancel 超时报错且仍 busy，工具
  最终结果不丢，收工后可提交下一轮。
- `TestCancelServeTurnTimeoutWithPendingApprovalKeepsLiveState`：pending cancel 帧与
  拒绝语义不回退，工具未收工时仍报告超时并保留活动态。

旧实现必红证据：

```text
TestDefaultLoopRunnerSessionPermissionOverrideLifecycle:
  下一轮子进程 --policy 要是 allow，实际 argv 含 --policy deny
TestCancelTimeoutKeepsLiveTurnAndFinalToolResult:
  等满 15s 后仍返回 nil，并继续清 busy
活动 delete 测试在旧签名下无法注入 live turn，编译即红；修复后生产 Delete 与测试
共同使用带 broker/promptService 的同一入口。
```

已执行的定向与包级验证：

```bash
go test ./cmd/dshd -run 'TestDefaultLoopRunnerSessionPermissionOverrideLifecycle|TestCancelTimeoutKeepsLiveTurnAndFinalToolResult|TestServeDelete' -count=1 -v
# PASS，0.651s

go test ./cmd/dshd -count=1
# ok dshgo/cmd/dshd 25.444s

go test ./... -count=1
# 全仓 exit 0；cmd/dshd 33.066s，其余包全部 ok 或无测试文件

go vet ./... && go build ./...
# 两条均 exit 0

export PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH"
CGO_ENABLED=1 go test -race ./cmd/dshd \
  -run 'TestDefaultLoopRunnerSessionPermissionOverrideLifecycle|TestCancelTimeoutKeepsLiveTurnAndFinalToolResult|TestServeDelete|TestCancelServeTurnTimeoutWithPendingApprovalKeepsLiveState' \
  -count=1
# ok dshgo/cmd/dshd 6.879s（MinGW GCC 16.2.0，仅上述定向用例）
```

## 未验证边界

- 未启动 GUI、未访问真实用户 home、未调用真实模型或 magpie；权限测试使用测试进程
  拦截真实子进程 argv，生命周期测试使用假 runner 与临时 home。
- 未做真实子进程 + 真实长工具的 delete/cancel 真机复验，也未做 UI 断线重连后的
  展示验收；这里证明的是宿主状态机、目录顺序和 argv，不是跨仓产品整链。
- 未改变 Transport 方法、响应字段、错误码表、v4 编码或审批帧格式；UI 对新增
  `ErrCancelTimeout` 错误文案的展示仍属跨仓复验项。
- `-race` 只覆盖上述定向用例，未重跑全仓 race；普通全仓 test/vet/build 已完成。
