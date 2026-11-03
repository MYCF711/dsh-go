# H13：审批走 Transport

> 当前状态（2026-11-03 复核）：**Transport 端点与子进程标记通道已有实现和测试；父进程 serve 桥接、H14 统一序号/重放、界面真实回传与整链验收未闭环。** 详细跨线状态以 `docs/transport-contract.md` 为快照，开工仍须重读最新源码和提交。

## 已有证据与不能外推的结论

| 层 | 当前证据 | 不能据此宣称 |
|---|---|---|
| 工具侧 | `spi.Caps.NeedsApproval()`；ACP 已有 `session/request_permission`；`dshd run --policy ask` 可终端询问 | ACP/终端可用不等于原生 UI 可用 |
| Transport HTTP | M249：`session.approve`、handler、原样转发/未知 requestId=404/未知 option=400 测试 | handler 单测不证明工具实际执行或拒绝 |
| 子进程 | M250：`@@APPROVAL@@` 标记通道与 5 条测试，无应答按拒绝处理 | 子进程有标记不等于 serve 父进程已正确转发 |
| 父进程 | M252 只读核对记录 stderr 是字节缓冲，不是天然消息流 | 尚需验证分帧、requestId 对应、超时/取消、真实 WS/界面链路 |

## 契约：与 ACP 同形，HTTP 控制 + WS 事件

**服务端 → 客户端（WS）**

```json
{"seq":101,"type":"approval/request","time":1793683941007,
 "data":{
  "requestId":"perm-8f3c…","sessionId":"…",
  "toolCall":{"toolCallId":"perm-8f3c…","title":"bash: go test ./…","kind":"execute","status":"pending","rawInput":{"command":"go test ./…"}},
  "options":[
   {"optionId":"allow_once","name":"允许这一次","kind":"allow_once"},
   {"optionId":"allow_always","name":"这个会话里以后都允许","kind":"allow_always"},
   {"optionId":"reject_once","name":"拒绝","kind":"reject_once"}]}}
```

**客户端 → 服务端（HTTP 裸参数 body，不包 `params`）**

```http
POST /api/session.approve
{"requestId":"perm-8f3c…","optionId":"allow_once"}

200 {"result":{"accepted":true,"optionId":"allow_once"}}
```

- 未知 requestId → 404 `no_such_request`。
- 未知 optionId → 400 **`bad_option`**。
- 默认 120s 不应答 → 按 `reject_once` 处理，并推 `approval/timeout{requestId}`。
- `session.cancel` 取消挂起审批时 → 按拒绝处理、工具不得执行，并推 `approval/cancel{requestId}`；同一轮后续可重新 prompt，不残留 busy。
- 审批事件与 turn/done、turn/error 都进入 H14 的**每会话 Transport journal**，使用同一 seq 域；断线由原始 history frames 重放，pending 请求由 serve 恢复，UI 不自行编造。

## 宿主内部桥接

1. `dshd run` 在 ask 且非终端时，向 stderr 写一行：`@@APPROVAL@@ {"requestId":…,"tool":…,"args":…}`。
2. serve 按字节缓冲正确分帧，解析后把 `approval/request` 追加到该会话 journal 并推 WS，等待 `session.approve`/超时/取消。
3. serve 向对应子进程 stdin 写一行：`@@APPROVAL-RESP@@ {"requestId":…,"allow":true}`。
4. 子进程仅在 allow 后执行；reject/timeout/cancel 都不得执行，并返回明确拒绝结果。

stderr 是字节流，必须处理半行、粘包、多条标记和普通诊断混行；不能假设一次 Read 恰好一条 JSON。标记通道不影响 stdout 的 ready 契约（H11）。

## 验收

| 场景 | 判据 |
|---|---|
| 允许 | allow_once → 工具真的执行，结果出现在会话与 UI |
| 拒绝 | reject_once → 工具不执行，拒绝结果可查 |
| 超时 | 120s 不应答 → 自动拒绝 + approval/timeout，工具不执行 |
| 取消 | session.cancel → approval/cancel + 工具不执行；同会话第二轮可发送 |
| 乱回 | 未知 requestId=404 no_such_request；未知 optionId=400 bad_option |
| 重连 | 同 journal/epoch 可补 approval 帧；epoch 变化走 session/reset/stale_epoch，不错误恢复旧审批 |
| 不冒充 | UI 只收到真实 approval/request 才显示待审批；气泡消失或 handler 单测不等于批准 |

## 下一步

H14 契约冻结后，由明确的 owner 串行完成 serve 父进程桥接与 UI 回传；先写能在旧实现失败的集成测试，再实现。真实允许/拒绝/超时/取消属于 H13，不提前塞进 H14 的合成契约测试。

## 实现与真机验收（2026-11-03，M258）

链路三段都接上了：

| 段 | 位置 | 说明 |
|---|---|---|
| 子进程问 | `cmd/dshd/approval_stdio.go` | `DSHD_APPROVAL_STDIO=1` 时 stderr 打 `@@APPROVAL@@`、stdin 读 `@@APPROVAL-RESP@@`；读不到 = 拒绝 |
| 父进程转 | `cmd/dshd/serve_approval.go` | `approvalBroker`：逐行扫 stderr → `approval/request` 帧（进 journal，可重放）；`Resolve` 写回 stdin；120s 超时按**拒绝**并推 `approval/timeout` |
| Transport 收 | `session.approve` | 未知 requestId → 404，未知 optionId → 400 |

### 踩到的一个真 bug

`cmd.Stderr` 先设成 `bytes.Buffer` 之后再调 `cmd.StderrPipe()`，后者直接返回错误
（`exec: Stderr already set`）；那个错误被我吞了，结果**子进程卡在等审批、父进程一无所知**——
真机上表现为"工具调用了但这一轮永远不结束"。修法：先 `StderrPipe` 再决定 `cmd.Stderr`，
拿不到管道时**明确打一行日志并退回缓冲**，不假装有审批。

### 真机判据（`scripts/h13-approval-e2e.py`）

```
OK    决策=allow_once   文件存在=True  工具被拒=False 应答=已回
OK    决策=reject_once  文件存在=False 工具被拒=True  应答=已回
合计 OK=2 FAIL=0
```

第一条看**磁盘上那个文件在不在**；第二条除了"不在"，还要看到 `tool/result` 里
`isError:true` 且正文是"用户拒绝了 …"——否则"文件不存在"可能只是模型压根没试。

### 仍未验

- 120s 超时只在单测里用 30ms 验证过，未在真机上真等两分钟。
- 挂起审批在**断线重连**后能否由 serve 重放（契约要求，本仓未实现）。
- 界面实际消费：本仓没有界面。
