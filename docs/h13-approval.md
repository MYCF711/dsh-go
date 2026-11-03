# H13：审批走 Transport（契约先行）

> 状态：**契约设计完成，实现未开始**。这条是 §12 里唯一**完全没有**的能力，
> 也是"界面上的允许/拒绝是不是真的"的关键——现在界面点 Allow 只是清掉本地气泡，工具照跑。

## 现在有什么

| 层 | 现状 |
|---|---|
| 工具侧 | `spi.Caps.NeedsApproval()`：只读不问，其余要问 |
| ACP（stdio） | ✅ 完整：`session/request_permission` + `CallClient`，带 title/kind/content/options |
| `dshd run` | `--policy ask` 时会在终端问人 |
| **Transport（HTTP/WS）** | ❌ 一条都没有 |

所以 ACP 客户端（Martty）能审批，**界面不能**。

## 契约：与 ACP 同形，不另造一套

**服务端 → 客户端（WS 帧）**

```json
{"seq":101,"type":"approval/request","time":1793683941007,
 "data":{
   "requestId":"perm-8f3c…",
   "sessionId":"…",
   "toolCall":{
     "toolCallId":"perm-8f3c…",
     "title":"bash: go test ./...",
     "kind":"execute",
     "status":"pending",
     "content":[{"type":"content","content":{"type":"text","text":"…完整说明…"}}],
     "rawInput":{"command":"go test ./..."}
   },
   "options":[
     {"optionId":"allow_once","name":"允许这一次","kind":"allow_once"},
     {"optionId":"allow_always","name":"这个会话里以后都允许","kind":"allow_always"},
     {"optionId":"reject_once","name":"拒绝","kind":"reject_once"}]}}
```

**客户端 → 服务端（HTTP）**

```
POST /api/session.approve   {"requestId":"perm-8f3c…","optionId":"allow_once"}
→ 200 {"result":{"accepted":true,"optionId":"allow_once"}}
```

- `requestId` 不认识 → 404 `no_such_request`
- `optionId` 不在 options 里 → 400 `bad_request`
- 超时（默认 **120s**）→ 服务端自动按 `reject_once` 处理，并推一帧
  `{"type":"approval/timeout","data":{"requestId":…}}`

**为什么用 HTTP 回而不是 WS 回**：WS 是我们的广播通道，客户端到服务端的控制面一直走 HTTP
（`session.prompt`、`session.cancel` 都是），审批不该破例。

## 宿主内部怎么接（不用改子进程协议）

`serve` 起子进程时带 `--policy ask`，但子进程没有终端。做法：

1. `dshd run` 在 `ask` 且非终端时，**把审批请求写到 stderr 的一行**：
   `@@APPROVAL@@ {"requestId":…,"tool":…,"args":…}`
2. 父进程（serve）解析这一行 → 推 WS `approval/request` → 等 `session.approve`（或超时）
3. 父进程把结果写回**子进程 stdin** 的一行：`@@APPROVAL-RESP@@ {"requestId":…,"allow":true}`
4. 子进程读到应答后继续或返回 `策略拒绝了 <tool>`

**为什么走 stderr/stdin 而不是新造 fd**：Windows 上继承额外 fd 麻烦；stderr 已经是"给人看的"通道，
加一个行首标记就能解析，且**不影响 stdout 的 ready 契约**（H11）。

## 验收（写进 §12 的那几条）

| 场景 | 判据 |
|---|---|
| 允许 | `session.approve allow_once` → 工具真的执行，结果出现在会话里 |
| 拒绝 | `reject_once` → 工具**不执行**，返回 `策略拒绝了 …`，会话里有据可查 |
| 超时 | 不应答 120s → 自动拒绝 + `approval/timeout` 帧 |
| 乱回 | 未知 requestId → 404；未知 optionId → 400 |
| 不冒充 | 界面只收到 `approval/request` 才算"在问人"；气泡消失**不等于**已批准 |

## 下一步

先写**会红的测试**（`internal/transport` 层：approve 未知 id 返回 404；serve 层：allow 后工具执行、reject 后不执行），
再实现。**不要先写实现再补测试**——这条涉及"是否真的执行了"，测试写晚了就只剩口头保证。
