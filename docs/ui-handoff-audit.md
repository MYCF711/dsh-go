# 界面接入审计：宿主 `dshd serve` 现在对外是什么，界面接上还差什么

> **性质**：只读调查。本文件是本次任务唯一新建的文件；`internal/**`、`cmd/**`、`modules/**` 与 `../dsh-ui` 一个字节未改。
> **证据规则**：每条结论后面跟 `文件:行` 或"命令 + 原始输出"。没跑过、没读到的，一律标 **未核实**，不猜。
> **审计时间**：2026-11-04 03:44–04:10（Asia/Shanghai）。

## 审计环境（复现前提）

| 项 | 值 | 说明 |
|---|---|---|
| 宿主仓 rev | `4b6dff1`（工作树脏：29 个未提交改动） | `git rev-parse --short HEAD`；审计针对**工作树**，不是 HEAD |
| 界面仓 rev | `746520e`（工作树脏） | `../dsh-ui`，本次只读 |
| Go | `go1.27.3 windows/amd64` | `go version` |
| 审计用二进制 | `%TEMP%\dsh-audit\dshd.exe`，由源码现建 | 仓内 `dshd.exe` 已被 `cmd/dshtui/app.go`、`internal/wasmhost/wasmhost.go` 超过（`find cmd internal modules codecs -name '*.go' ! -name '*_test.go' -newer dshd.exe` 命中两个文件），按 `scripts/bin-check.py` 的判据算陈旧，不在陈旧二进制上下结论 |
| 临时 home | `%TEMP%\dsh-audit\{home,home2}`，另有一个只读指向 `../dsh-ui/tmp/aba-home` | 全部在仓库外；`--addr 127.0.0.1:0` 由 OS 分配端口 |
| 进程回收 | 只按 WINPID 杀掉 `%TEMP%\dsh-audit\dshd` 三个进程 | 未触碰 `../dsh-ui/tmp/dshd-serve`（PID 1787）等他人进程 |

---

## 0. 结论速览

| # | 问题 | 一句话答案 | 详见 |
|---|---|---|---|
| 1 | 对外提供什么 | **15 个 method**（`session.*` 10 + `workspace.list` + `fs.*` 2 + `config.*` 2），`POST /api/<method>`，body 是**裸参数对象**；错误信封 `{error:{code,message,hint,retryable}}`，但 `stale_epoch` 这个出口只有三字段 | §1 |
| 2 | 认证怎么走 | stdout 一行 `ready {"url":…,"token":…}`（**只有这两个字段**）；`GET /connect?token=` → **303 + `Set-Cookie: dshd_session=…; Path=/; HttpOnly; SameSite=Strict`**；token **一次性**，客户端**不许跟随重定向** | §2 |
| 3 | WS 最小姿势 | 连 `/ws`（带同一个 cookie）→ 发 `{"t":"sub","topic":"session:<id>","sinceSeq":N,"journalEpoch":E}` → 服务端回 `{seq,type,data,time}`；`{"t":"ping"}` → `{"seq":0,"type":"pong"}`；**服务端从不主动发 ping**；**坏帧没有错误帧，只有静默** | §3 |
| 4 | load vs history | `load` = 首屏**投影**（messages，v4 seq 域）+ 报出 `journalEpoch/lastSeq`；`history` = 补洞**原始帧**（journal seq 域）。**两套 seq 不能混用**——混用会 409 或静默丢帧 | §4 |
| 5 | 界面仓现状 | Go 1.27.3 + Viva（纯 Go GUI），有完整 `internal/apiclient`（含 `/ws` 客户端），14 个 method 有调用点，**`session.compact` 零调用**；自己的快照和 README 还写着 `/api/mux` | §5 |
| 6 | 差几件事 | 12 项，其中 4 项是**实测确认的跨仓不一致**（含 1 项静默丢帧）；归属与验证方式见 §6 | §6 |

**最可能卡住的一件事**：**宿主重启后 `journalEpoch` 撞号，新帧被 hub 按旧水位静默过滤掉**——客户端不报错、收不到 `session/reset`、也检测不到 gap。实测 8 秒 0 帧，而 `session.history` 里那条帧明明在。见 §4.5。

---

## 1. 宿主 Transport 现在对外提供什么

### 1.1 method 列表：15 个

定义在 `internal/transport/api.go:12-30`，`SessionMethods()` 按固定顺序返回同一组（`api.go:33-40`），契约快照 `internal/transport/testdata/api.snapshot.json` 逐字对齐（由 `api_snapshot_test.go` 从代码反射生成比对，不是手抄）：

| # | method | 宿主实际解码的入参 | 成功时 `result` | 常错码 | 证据 |
|---|---|---|---|---|---|
| 1 | `session.list` | 无 | `{sessions:[{id,title,cwd,updatedAt}]}` | 500 | `server.go:540-555` |
| 2 | `session.load` | `id`\|`sessionId`、`limit`\|`tailMessages`（`limit` 优先，`sessionId` 优先） | `LoadResult`（见 §1.3） | 404 | `server.go:415-452` |
| 3 | `session.history` | `id`\|`sessionId`、`sinceSeq`、`limit`、`journalEpoch` | `{frames:[ServerFrame],lastSeq,journalEpoch}` | 409 `stale_epoch` / 404 | `server.go:481-537` |
| 4 | `session.prompt` | `id`\|`sessionId`、`text` | `{accepted:true,turnId}` **立刻返回** | 409（上一轮没结束） | `server.go:336-356` |
| 5 | `session.cancel` | `id`\|`sessionId` | `{cancelled:true}` | 409 | `server.go:359-375` |
| 6 | `session.approve` | `requestId`、`optionId` | `{accepted:true,optionId}` | 404 `no_such_request` / 400 `bad_option` | `server.go:454-479` |
| 7 | `session.create` | `mode`（= header 的 agentPreset，空则 default） | `{sessionId}` | 400 | `server.go:378-393`、`cmd/dshd/serve_create.go:37-60` |
| 8 | `session.delete` | **只认 `sessionId`**（不认 `id`） | `{deleted:true}` | 400 缺参 / 404 / 500 | `handlers_extra.go:13-41` |
| 9 | `session.fork` | **只认 `sessionId`**、`title` | `{sessionId}` | 400 / 404 / 500 | `handlers_extra.go:43-76` |
| 10 | `session.compact` | `sessionId`\|`id`、`keep`（≤0 转 6） | `CompactResult{compacted,from,to,shadowed,summaryChars}`；`compacted:false` **不是错误** | 400 / 500 | `handlers_extra.go:78-118`、`api.go:46-52` |
| 11 | `workspace.list` | 无 | `{entries:[{name,isDir,size}]}`（空也给 `[]`，不给 `null`） | 500 | `handlers_workspace.go:15-40` |
| 12 | `fs.read` | `path` | `{path,content,size}` | 404 文件不存在 / 400 / 500 | `handlers_fs.go:21-49` |
| 13 | `fs.write` | `path`、`content`（缺省即空串＝清空文件） | `{written:true}` | 404 / 400 / 500 | `handlers_fs.go:59-86` |
| 14 | `config.get` | 无 | `{values:{k:v}}` | 500 | `handlers_config.go:23-42` |
| 15 | `config.set` | **`{"values":{k:v}}`**（嵌套一层） | `{updated:N}` | 400 缺 values | `handlers_config.go:56-81` |

> 命名不一致的地方已经在实测中撞到：`session.delete` / `session.fork` 只认 `sessionId`，而 `load/history/prompt/cancel/compact` 两个名字都收。见 §6 第 7 项。

### 1.2 HTTP 形状

- 路由只有三条 + 一个调试口（`server.go:177-195`）：
  - `GET /connect?token=<launch>`（`server.go:184`）
  - `WS /ws`（`server.go:188-189`）
  - `POST /api/<method>`（`server.go:190-191` → `handleAPI`）
  - `GET /debug/stats`（`server.go:186-187`，需 cookie，回 goroutine/堆数字）
  - 其它 → 404 + 错误信封（`server.go:192-194`）
- **body 是裸参数对象，不包 `params`**：handler 一律 `json.NewDecoder(r.Body).Decode(&in)` 直接解平铺结构（`server.go:343`、`:417-425`、`:455-460`）。快照与此一致（`docs/transport-contract.md:76`）。
- 非 POST 打 `/api/*` → 404。实测：`curl -o /dev/null -w "%{http_code}" -b jar.txt $URL/api/session.list` → `404`。
- 未知 method → **501** + `not_implemented`。实测：

  ```
  POST /api/session.bogus
  HTTP/1.1 501 Not Implemented
  {"error":{"code":"not_implemented","hint":"宿主这一侧还没接线，换一个 method 或等它实现","message":"not implemented","retryable":false}}
  ```

### 1.3 信封字段

- 成功：`{"result": …}`——**任何类型**都套，不只 map（`server.go:686-715`，注释里写明这正是 2026-11-03 H8 被打回 🟡 的那条）。
- 失败：`{"error":{"code","message","hint","retryable"}}`（`server.go:620-633`）。`retryable` 的判据是 `code == "internal"`（`server.go:631`）——即**只有 internal 才可重试**。
- `hint` 按码给一句"下一步"（`server.go:636-656`）；未知码给空串。
- **例外（实测确认）**：`session.history` 的 409 是手写信封，**只有 code/message/hint，没有 retryable**，另外在顶层多带 `journalEpoch/lastSeq`（`server.go:506-517`）：

  ```json
  {"error":{"code":"stale_epoch","hint":"丢弃旧水位，重新 session.load 拿 journalEpoch/lastSeq","message":"journal 世代或水位对不上，需要重新 load 基线"},"journalEpoch":2,"lastSeq":0}
  ```

  这与 `docs/transport-contract.md:98`（H8 🟡「stale_epoch 特殊出口未闭环」）完全对上，本次是**真机复现**，不是纸面结论。
- `LoadResult` 的字段（`api.go:91-122` + 快照）：`messages,total,truncated,model,permission,contentWidth,usageTotal,contextTokens,journalEpoch,lastSeq`。其中 `model/permission/contentWidth/usageTotal/contextTokens/journalEpoch/lastSeq` 全是 `omitempty`——**缺字段 = 不知道**，界面不许补 0（`api.go:96-121`）。

---

## 2. 认证怎么走

### 2.1 `ready` 行字段：只有 url 和 token

`cmd/dshd/serve.go:58-67`：

```go
type readyLine struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}
```

打印在 `serve.go:234-240`，**stdout 只有这一行**；policy 诊断走 stderr（`serve.go:241-243`，注释记着 H11 的教训）。实测：

```
$ dshd.exe serve --home %TEMP%\dsh-audit\home --addr 127.0.0.1:0
--- stdout ---
ready {"url":"http://127.0.0.1:50361","token":"26e7…97db"}      # 64 位 hex
--- stderr ---
policy deny（无人值守；写操作会被拒绝）；每轮最多 6 步
```

token 与 cookie 都是 32 字节随机数的 hex（`server.go:35-37`、`:579-585`）。

### 2.2 用 token 换 cookie

- **只认 GET**：非 GET 直接 404（`server.go:246-249`）。
- 成功返回 **HTTP 303 See Other** + `Location: /` + `Cache-Control: no-store`，并用 `http.SetCookie` 下发（`server.go:269-278`）：

  ```
  HTTP/1.1 303 See Other
  Cache-Control: no-store
  Location: /
  Set-Cookie: dshd_session=e27a…a74d; Path=/; HttpOnly; SameSite=Strict
  Content-Length: 0
  ```

  属性：`Path=/`、`HttpOnly`、`SameSite=Strict`；**没有 `Secure`、没有 `Max-Age`/`Expires`**（会话 cookie，随进程/浏览器关闭失效）。
- **一次性**：`s.used` 为真后再用同一个 token 一律 401，且不再发放新 cookie（`server.go:252-258`）。实测第二次 → `401 unauthorized`。
- 之后所有 `/api/*` 与 `/ws` 都只认这个 cookie（`server.go:287-290`、`:232-236`、`:557-576`）。cookie 名固定 `dshd_session`（`server.go:33`）。
- **客户端必须不跟随 303**：`Location: /` 是个不存在的路由，跟过去只会拿到 404，cookie 反而被丢掉。界面侧已经踩过并写死（`../dsh-ui/internal/apiclient/client.go:174-183` 把 `CheckRedirect` 换成 `http.ErrUseLastResponse`）。

### 2.3 能跑通的复现（curl，已实测）

```bash
# 1) 起宿主（临时 home，端口交给 OS）
dshd.exe serve --home /tmp/dsh-audit/home --addr 127.0.0.1:0 > out.txt 2> err.txt &
URL=$(sed -n 's/^ready {"url":"\([^"]*\)".*/\1/p' out.txt)
TOKEN=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' out.txt)

# 2) 换 cookie（--cookie-jar 存下来；不要加 -L）
curl -s -c jar.txt "$URL/connect?token=$TOKEN"
#  → HTTP/1.1 303 See Other + Set-Cookie: dshd_session=…

# 3) 带着 cookie 调任何 /api
curl -s -b jar.txt -X POST "$URL/api/session.list" -d '{}'
#  → {"result":{"sessions":[]}}
```

原始输出（本次实跑）：

```
=== 1. 无 cookie 调 /api/session.list ===
HTTP/1.1 401 Unauthorized
{"error":{"code":"unauthorized","hint":"先 GET /connect?token=… 换 cookie，再调这个接口","message":"unauthorized","retryable":false}}
=== 2. /connect 换 cookie ===
HTTP/1.1 303 See Other
Set-Cookie: dshd_session=e27a6922…a563a74d; Path=/; HttpOnly; SameSite=Strict
=== 3. 带 cookie 调 /api/session.list ===
HTTP/1.1 200 OK
{"result":{"sessions":[]}}
=== 4. 重复用同一 token ===
HTTP/1.1 401 Unauthorized
=== 5. /ws 无 cookie ===
HTTP/1.1 401 Unauthorized
```

### 2.4 同一个流程的 python 版（已实测）

```python
import json, urllib.request, http.cookiejar
base, token = "http://127.0.0.1:50361", "<ready 里的 token>"

# 用 opener 携带 cookie；urllib 默认不跟 303 之外的跳转，这里显式兜住
jar = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k): return None
op.add_handler(NoRedirect())

try:
    op.open(f"{base}/connect?token={token}")          # 303 会被 NoRedirect 拦下
except urllib.error.HTTPError as e:
    assert e.code in (303, 200), e.code                  # 303 = 成功

req = urllib.request.Request(f"{base}/api/session.list", data=b"{}",
                             headers={"Content-Type": "application/json"}, method="POST")
print(json.loads(op.open(req).read()))                   # {"result": {"sessions": []}}
```

> 注意：token 一次性。想同时用 curl 和界面打同一个宿主，就先用 curl 换一次 cookie，再把这个 cookie 值直接注入两边（界面仓有 `-cookie` 开关，见 §5.4）。

---

## 3. WS 订阅的最小正确姿势

### 3.1 客户端帧：只有三种

`internal/transport/frame.go:47-73`：

| t | 必填 | 语义 |
|---|---|---|
| `sub` | `topic`（必须 `session:<非空 id>`）、`sinceSeq`（≥0，可省）、`journalEpoch`（可省，0=首次） | 订阅并重放 `seq > sinceSeq` 的帧 |
| `unsub` | `topic`（只查非空，不查前缀） | 退订 |
| `ping` | 无 | 回 `pong`，不改状态 |

校验（`frame.go:91-141`、`wssession.go:206-248`）：
- 正文必须是 **JSON 对象**，`{` 开头（`frame.go:98-100`）；
- `topic` 前缀必须是 `session:`，且 id 非空（`frame.go:172-183`）；
- `sinceSeq < 0` 直接报错，**要从头就整个省略该字段**（`frame.go:115-117`）；
- **不认识的 `t` 不许当 ping 放过**（`frame.go:26-28` 的注释就是为此写的）——但见 §3.6，宿主对它的处理方式并不友好。

即最小可用订阅帧：

```json
{"t":"sub","topic":"session:c9892c30-aa21-c559-1093-4e45c2195353","sinceSeq":0,"journalEpoch":1}
```

### 3.2 服务端帧：`{seq,type,data,time}`

`frame.go:76-85`。**没有 `topic` 字段**——连接本身就是会话归属，所以**一条 WS 只订一个会话**（`docs/transport-contract.md:80`）。同一连接混订两个会话，帧会串台且无从分辨。

`type` 白名单（`docs/transport-contract.md:79`）：v4 原始事件名 + `turn/done`、`turn/error`、`approval/request`、`approval/timeout`、`approval/cancel`、`session/reset`、`pong`。宿主实际产出的两类结局帧常量在 `cmd/dshd/serve_events.go:10-13`。

实测一帧（真实 turn/error）：

```json
{"seq":1,"type":"turn/error","data":{"turnId":"turn-1","error":"连不上模型网关 ：确认网关在跑、--base 地址对"},"time":1793735262292}
```

### 3.3 订阅时序（宿主侧的真实顺序）

`wssession.go:110-167` 的 `sub` 分支按这个顺序做四件事：

1. **核世代**：`journalEpoch != 0` 且与本进程 journal 不符 → 先发一条**无 seq** 的 `session/reset{journalEpoch,lastSeq}`（`wssession.go:113-127`）。
2. **重放挂起的审批**：`PendingApprovals` 给出的帧逐条写回，**这些帧不带 seq**（`wssession.go:128-136`、`cmd/dshd/serve_approval.go:219-225`）。
3. **重放 journal**：`j.Since(sinceSeq, 0)` 全量重放；`sinceSeq=0` **也重放**（注释 `wssession.go:145-147` 记着"只重放 sinceSeq>0 是错的"这个坑）。
4. **Subscribe**：起点用重放的最后一条，之后只推更新的（`wssession.go:167`）。先重放后订阅，避免新帧插到旧帧前面。

世代不符时**跳过第 3 步**（`wssession.go:149-150`），只发 reset。

### 3.4 心跳：服务端不发，客户端自己发

- 协议层心跳是**应用层**的 `{"t":"ping"}` → `{"type":"pong"}`（`wssession.go:172-180`），不是 WS 控制帧。
- 服务端的心跳只在连接实现 `Pinger` 时才启动（`heartbeat.go:34-41`），常量是 30 秒 ping / 90 秒读侧空闲判死（`heartbeat.go:20-29`）。
- **`WSConn` 没有实现 `Ping()` 方法**（`internal/transport/ws.go` 全文无 `func (c *WSConn) Ping`，`grep -n "Pinger|func (c \*WSConn) Ping" internal/transport/*.go` 只命中注释与接口定义）→ 真实 `/ws` 连接上**服务端从不主动发 ping**，30s/90s 那两个数对真连接不生效。
- 实测：订阅后静置 35 秒，收到 **0 条**服务端帧：

  ```
  RESULT: 35 秒内收到 0 条服务端帧
  ```

**结论**：存活检测责任在客户端。半开连接（进程被挂起、网线拔掉）在 TCP 层不会报错，界面如果不自己发 ping，表现就是"没新消息"——正是 `heartbeat.go:16-19` 描述的那个故障。

### 3.5 错误怎么表示

| 层 | 表现 | 证据 |
|---|---|---|
| 升级前 | 没有 cookie → **HTTP 401 + 错误信封**（还是普通 HTTP，不是 101） | `server.go:232-236`，实测 `HTTP/1.1 401` |
| 握手缺要素 | 400 纯文本（`http.Error`），**不是** JSON 信封 | `ws.go:50-67` |
| 帧不合法 | **没有错误帧，也不 close**：`ServeConn` 直接 return，`handleWS` 用 `go ServeConn(...)` 丢掉了返回值和错误 | `wssession.go:105-107`、`:182-186`、`server.go:241` |
| 单帧过大 | 读侧上限 1 MiB（`ws.go:23`、`:185-187`）；写侧只支持 16 位长度、**>65535 字节直接返回错误**（`ws.go:210-221`），而 `Push` 的错误会让 hub 把这条订阅摘掉（`hub.go:146-163`）——即一帧过大就能悄悄掐死连接 | `ws.go:206-221` |

实测坏帧（发 `{"t":"bogus"}`）：

```
--- 发一个 t=bogus 的帧（契约外的类型）---
recv after bogus: TIMEOUT —— 连接没关、也没有错误帧
```

也就是说：**订阅写错、帧类型写错，界面看到的不是报错，是"安静"**。这一条对调试成本的影响比它看起来大。

### 3.6 三条实测（同一会话，带水位重订）

```
== A. sinceSeq=0, 无 epoch（首次订阅/从头） ==
sent: {"t": "sub", "topic": "session:c989…", "sinceSeq": 0}
recv: {"seq":1,"type":"turn/error","data":{…},"time":1793735262292}      ← 历史被重放
recv: TIMEOUT

== B. sinceSeq=1, epoch=1（水位就是最新） ==
sent: {"t": "sub", "topic": "session:c989…", "sinceSeq": 1, "journalEpoch": 1}
recv: TIMEOUT                                                             ← 不重放、只等新帧（正确）

== C. sinceSeq=0, epoch=999（老世代） ==
sent: {"t": "sub", "topic": "session:c989…", "sinceSeq": 0, "journalEpoch": 999}
recv: {"seq":0,"type":"session/reset","data":{"journalEpoch":1,"lastSeq":1}}   ← 先喊换代
recv: TIMEOUT                                                             ← 且不拿旧水位补
```

注意 C 里的 `"seq":0`：契约文字说 `session/reset`/`pong` **没有 seq**（`docs/transport-contract.md:81`、`wssession.go:121-123`），但线上字段是 `"seq":0` —— 因为 `ServerFrame.Seq` 的 tag 没有 `omitempty`（`frame.go:77`）。客户端判据只能是 **`seq <= 0` = 控制帧、不参与水位**（界面已经这么写，见 §5.5），不能判"字段不存在"。

---

## 4. `session.load` 与 `session.history` 的分工

### 4.1 分工表

| | `session.load` | `session.history` |
|---|---|---|
| 用途 | **首屏** | **补洞**（断线/断号之后） |
| 返回 | `messages[]`（已投影成"能画的样子"）+ `total/truncated` + 元信息 | `frames[]`：**同一 journal 的原始帧** |
| seq 域 | **v4 日志 seq**（`Message.seq`） | **Transport journal seq**（`ServerFrame.seq`） |
| 水位 | 顺带报出 `journalEpoch` + `lastSeq` | 收 `journalEpoch` + `sinceSeq`，回新的 `lastSeq/epoch` |
| 丢什么 | 投影只保留 text；reasoning/tool/approval 的原始形状会被压扁 | 什么也不丢（原帧） |
| 证据 | `api.go:91-122`、`server.go:415-452`、`internal/transport/source.go:71-137` | `api.go:124-132`、`server.go:481-537` |

一句话判据：**要"能画的东西"用 load；要"能重放的帧"用 history。** 用 load 补洞会丢 reasoning/tool/approval（`api.go:126-127` 的注释就是为此写的）；用 history 当首屏会在宿主重启后拿到空（见 §4.5）。

### 4.2 两套 seq 是硬的（这是最容易接错的地方）

- `Message.Seq` 是 **v4 日志序号**：`api.go:56-57` 定义，来源 `Source.Messages` 要求"必须按 Seq 升序"（`source.go:10`），由宿主从 v4 事件投影出来。
- `ServerFrame.Seq` 是 **Transport journal 序号**：`journal.go:8-19` 的注释把为什么必须另起一套讲透了——之前 v4 seq / 全局 `serveTurnSeq` / 客户端自己数三套混用，导致"断号检测"是假的。
- `LoadResult.journalEpoch/lastSeq` 来自 journal，**不是** messages 的 seq（`server.go:444-450`）。

实测（同一个真实会话，`../dsh-ui/tmp/aba-home` 只读）：

```
load(messages[].seq) = 8, 12, 13, 17, 18      ← v4 域
load.total = 8, truncated = True
load.journalEpoch = 1
load.lastSeq      = （字段不出现 = 0）          ← journal 域，新进程还没发过帧
history(sinceSeq=0) = {"frames":null,"lastSeq":0,"journalEpoch":1}
```

**把 `messages[].seq` 当成 WS 水位就会当场炸**（实测）：

```
POST /api/session.history {"id":"3b61…","sinceSeq":18}      # 18 来自 messages[].seq
→ HTTP 409
{"error":{"code":"stale_epoch","hint":"丢弃旧水位，重新 session.load 拿 journalEpoch/lastSeq","message":"journal 世代或水位对不上，需要重新 load 基线"},"journalEpoch":1,"lastSeq":0}
```

### 4.3 `journalEpoch` / `session/reset` 什么时候出现

- **`journalEpoch` 出现在四处**：`load` 响应（`api.go:120`）、`history` 响应（`api.go:131`）、`history` 请求参数（`server.go:492`）、`sub` 帧（`frame.go:72`）。
- **`session/reset` 只在一种情况出现**：`sub` 带了**非 0** 且和本进程 journal 不一致的 `journalEpoch`（`wssession.go:115-127`）。它**不会**被主动推给已订阅的连接。
- `Journals.Reset` 在生产代码里**没有调用点**（`grep -rn '\.Reset(' cmd internal | grep -i journal` 只命中 `journal.go:148` 的定义与测试）→ 所谓"换代"实际只有一种来源：**宿主进程重启**。
- **epoch 是进程内计数器**：`cmd/dshd/serve.go:125-126`

  ```go
  var epochSeq int64
  journals := transport.NewJournals(func() int64 { epochSeq++; return epochSeq })
  ```

  即**每次重启都从 1 重新发号**。这是 §4.5 那个坑的根。

- history 的 HTTP 面有对应判据：`(journalEpoch != 0 && journalEpoch != j.Epoch()) || sinceSeq > j.LastSeq()` → 409 `stale_epoch`（`server.go:506-517`）。

### 4.4 空 journal 的两个线上形状

- `frames` 为空时序列化成 **`null`**，不是 `[]`（`journal.go:80-95` 的 `Since` 返回 nil 切片）。实测：`{"frames":null,"lastSeq":0,"journalEpoch":1}`。
- `lastSeq` 为 0 时**字段整个不出现**（`api.go:120-121` 的 `omitempty`）。实测首屏 load 里没有 `lastSeq` 键。

客户端必须把"字段缺失"和"null"都当 0 / 空处理，不能只认严格形状。

### 4.5 最可能卡住的那件事：重启后 epoch 撞号 → 新帧被静默吞掉

**场景**：客户端手里有 `epoch=1, lastSeq=1`（重启前）。宿主重启，同一会话的新 journal 又拿到 `epoch=1`（进程内计数器重来）。客户端带 `{"sinceSeq":1,"journalEpoch":1}` 重新订阅：

1. 世代**匹配** → 不发 `session/reset`（`wssession.go:115-127` 的条件不成立）；
2. 重放：`j.Since(1,0)` 为空 → 没有历史可补（`wssession.go:151-165`）；
3. `hub.Subscribe(topic, 1, sub)` → 之后 `Publish` 只发 `frame.Seq > 1` 的帧（`hub.go:136`）；
4. 新 journal 的 seq 从 **1** 开始 → **第一条新帧 seq=1，被自己过滤掉**。

实测（同一 home 重启宿主后）：

```
sent: {"t":"sub","topic":"session:c989…","sinceSeq":1,"journalEpoch":1}
prompt: {'result': {'accepted': True, 'turnId': 'turn-1'}}
RESULT: 8 秒内收到 0 条帧                      ← 客户端一个字都没收到

事后 history(sinceSeq=0):
{"result":{"frames":[{"seq":1,"type":"turn/error","data":{…},"time":1793735562155}],"lastSeq":1,"journalEpoch":1}}
```

**帧确实进了 journal，但永远没推给这条连接**。客户端这边：没有 reset、没有 error、没有 gap（它以为 `lastSeq=1` 就是最新），界面上表现为"发出去的消息没有回应"，而宿主日志一切正常。

这是本轮审计里唯一一个**四层（契约/宿主/重放/客户端）单独看都对、合起来会静默丢数据**的点。判据与修法见 §6 第 1 项。

### 4.6 正确接法（可直接照抄的顺序）

```
1) load(id, tail)                  → 画首屏；记下 journalEpoch(E) 与 lastSeq(L)（缺省当 0）
2) 连接 /ws（带 cookie）
3) sub {topic:"session:<id>", sinceSeq:L, journalEpoch:E}
4) 收帧：seq>0 的正常上屏；seq<=0 的是控制帧：
     - type=="pong"        → 心跳回包，忽略
     - type=="session/reset"→ 丢水位，回到 1)
5) 发现 seq 断号（收到 s，上一帧是 p，s > p+1）→
     history(id, sinceSeq=p, limit=..., journalEpoch=E) 分批补，别整页 load
     409 stale_epoch → 丢水位，回到 1)
6) 重连时不要用 messages[].seq 当 sinceSeq
```

界面仓已经按这个顺序写了（证据见 §5.5）。

---

## 5. 界面仓现状（`../dsh-ui`）

> 本节只写**读到的**。没读到的在 §5.6 明说。

### 5.1 语言 / 框架

- **Go 1.27.3**，模块 `github.com/MYCF711/dsh-ui`（`../dsh-ui/go.mod:1-3`）。
- GUI 用 **`github.com/vivaui/viva v0.2.3`**（纯 Go UI 工具包，走 `purego`，`go.mod` 直接依赖）；`cmd/app/frame_chrome.go:6-9` 导入 `viva/app`、`viva/components`、`viva/text`、`viva/ui`。
- WS 客户端用 **`github.com/coder/websocket v1.8.15`**（`internal/apiclient/mux.go:25`）。
- **没有 TypeScript**：`find . -name '*.ts' -o -name '*.tsx' -o -name 'package.json'`（排除 tmp）零命中。

### 5.2 apiclient：有，而且是完整的

`../dsh-ui/internal/apiclient/`：

| 文件 | 行数 | 管什么 |
|---|---|---|
| `client.go` | 382 | `/connect`、`/api/<method>` 的 Call、list/load/create/cancel/prompt、错误信封解析 |
| `mux.go` | 1042 | `/ws` 客户端：订阅、重放、断号检测、重连、事件归约 |
| `history.go` | 119 | `session.history` + `FillGap` 分批补洞 |
| `approval.go` | 153 | `session.approve` 与审批帧解析 |
| `config.go` | 33 | `config.get` / `config.set` |

### 5.3 调了哪些 method（有调用点 = 真在用）

| method | 调用点 |
|---|---|
| `session.list` | `client.go:316` |
| `session.load` | `client.go:328`（同时发 `id/sessionId/tailMessages/limit` 四个键，两边都兼容） |
| `session.create` | `client.go:347` |
| `session.cancel` | `client.go:367` |
| `session.prompt` | `client.go:376` |
| `session.history` | `history.go:52` |
| `session.approve` | `approval.go:141` |
| `config.get` | `config.go:18` |
| `config.set` | `config.go:32` |

**没有调用点的**：`session.delete`、`session.fork`、`workspace.list`、`fs.read`、`fs.write`（这 5 个只在 `cmd/mockd/main.go:693-720` 的 switch 里出现，不在客户端）；**`session.compact` 全仓零命中**（`grep -rn "session.compact" --include=*.go .` → 无结果）。

即：15 个 method 里，界面**真在用 9 个**，**11 个里有 6 个没有客户端调用**（`session.delete/fork/compact`、`workspace.list`、`fs.read/write`）。

### 5.4 连没连 `/ws`：连了

- 路径拼接：`mux.go:89` `u.Path = strings.TrimRight(u.Path, "/") + "/ws"`；文件头注释 `mux.go:4-13` 明确写着"WS 路径是 `/ws`（不是 `/api/mux`）""服务端帧是 `{seq,type,data,time}`""一个会话一条连接"。
- 订阅调用：`cmd/app/main.go:529` `m.mux.Subscribe(connCtx, "session:"+id, since)`。
- 认证：先 `/connect` 换 cookie（`main.go:1555` `m.cli.Connect(ctx, *token)`），再把 cookie 交给 Mux（`main.go:1561` `m.mux.Cookies = m.cli.Cookies()`）；也支持 `-cookie` 直接注入（`main.go:1546-1553`）。
- 订阅帧带世代：`mux.go:156` `clientFrame{T:"sub", Topic:topic, SinceSeq:sinceSeq, JournalEpoch:epoch}`。

### 5.5 契约细节的消费情况（读到的部分）

| 契约点 | 界面怎么处理 | 证据 |
|---|---|---|
| `seq<=0` 是控制帧 | 不参与水位；`session/reset` 照样交给上层 | `mux.go:220-227` |
| `pong` | 直接 `continue` | `mux.go:217-219` |
| 断号 | 客户端自己算 gap，发 `Event{Kind:"gap", Gap:{fromSeq,toSeq}}`，不指望服务端 | `mux.go:236-241` |
| 重复/倒退 seq | 静默丢弃 | `mux.go:229-231` |
| 409 `stale_epoch` | 丢水位 + 局部 `since` 归零 + **重连**（注释里写清"必须重连，否则新 journal 的 seq 被当重复帧丢光"） | `main.go:598-620` |
| 首屏 → 水位 | load 后把 `lastSeq/journalEpoch` 记进 `seqs/epochs` 再订阅 | `main.go:286-294`、`:349-360` |
| 补洞 | `FillGap` 分批、每批带 epoch、失败退回 load | `history.go:78-119` |

**这正是 §4.5 那个坑的合谋方**：它实现完全符合契约，问题在契约本身（epoch 只保证进程内唯一，跨重启会撞号）。

### 5.6 未读到 / 未核实

- **没跑过界面**：本次只读代码，没有启动 `app.exe`、没有截图、没有真机联调。§5 里所有"它会怎么处理"都是读代码得出，**不是运行时验证**。
- 界面仓 `git status` 有 19 个已修改文件未提交（`main.go`、`mux.go`、多个 `plugins/*`），这些改动是否已构建进 `app.exe` **未核实**。
- 界面插件层（`plugins/ds*`）的渲染是否消费了 `view/permission/usageTotal/time` 等字段，**本次未逐项核对**（只核了 apiclient 层）。
- 界面仓自己的契约快照**已经过时**：`testdata/api.snapshot.json:31` 仍写 `"path": "/api/mux"`，与代码实际打的 `/ws` 矛盾；`README.md:14` 也还写着"13 个 method + `/api/mux`"。这两个是**文档层**的陈旧，不是代码陈旧。

---

## 6. 结论：界面要接上还差哪几件事

按"会不会让用户/开发者看起来像卡住"排序。P0 = 会静默丢数据或静默失败；P1 = 会报错但能查；P2 = 能力缺口。

| # | 事项 | 归属 | 现状证据 | 怎么验证 | 级别 |
|---|---|---|---|---|---|
| 1 | **`journalEpoch` 跨重启撞号** → 新帧被 hub 按旧 `sinceSeq` 过滤，客户端无感 | **契约侧**（epoch 语义）+ **宿主侧**（换 epoch 应保证与既往不同，例如用启动时间/随机数） | `serve.go:125-126` 是纯计数器；`hub.go:136` 按 `seq > sinceSeq` 过滤；§4.5 实测 8 秒 0 帧而 history 里帧在 | 重启宿主后按 §4.5 复现；修完再跑同一脚本应收到帧或收到 `session/reset` | **P0** |
| 2 | `config.get` 响应键不符：宿主回 `{"values":{…}}`，界面读 `"config"` → **静默拿到空配置** | 界面侧（改 tag）或契约侧（冻结键名） | 宿主 `handlers_config.go:41`；界面 `../dsh-ui/internal/apiclient/config.go:16`；实测 `{"result":{"values":{"ui.sidebarWidth":"260"}}}` | 真机：写一个键、重启界面、看布局是否记住 | **P0** |
| 3 | `config.set` 入参形状不符：宿主只认 `{"values":{…}}`，界面发扁平 kv → **400 且 hint 为空** | 界面侧或契约侧 | 宿主 `handlers_config.go:62-73`；界面 `config.go:32`；实测扁平 → `{"code":"error","hint":"","message":"缺 values：要给至少一项"}`，包 values → `{"updated":1}` | 同上（一次保存就能看出来） | **P0** |
| 4 | `stale_epoch` 的 409 信封缺 `retryable`（唯一一个不符合四字段的出口） | **宿主侧** | `server.go:506-517` 手写信封；`docs/transport-contract.md:98` 已记；本次实测复现 | `go test ./internal/transport` 里应有一条断言四字段的用例（现在没有）；或 curl 复现 | P1 |
| 5 | 空 journal 的 `frames` 是 `null` 不是 `[]` | 宿主侧（一行 nil→empty）或契约侧（写明允许 null） | `journal.go:80-95`；实测 `{"frames":null,…}` | 对空 journal 调 history，看形状 | P1 |
| 6 | `lastSeq` 为 0 时字段整个消失（`omitempty`） | 契约侧（写明"缺=0"）或宿主侧（去掉 omitempty） | `api.go:120-121`；实测首屏 load 无该键 | 对空 journal 会话调 load | P1 |
| 7 | `session.delete` / `session.fork` **不认 `id` 别名**，只认 `sessionId` | 宿主侧（统一 `fillID`） | `handlers_extra.go:22-24`、`:57-59`；实测 `{"id":"nope"}` → `{"message":"缺 sessionId"}` | 两个 method 各发一次 `{"id":…}` | P1 |
| 8 | 坏帧/未知 `t` **没有错误帧也不 close**，订阅写错表现为"安静" | **宿主侧** | `wssession.go:105-107`、`:182-186`；`server.go:241` 丢掉返回错误；实测 6 秒无响应且连接仍开 | 发 `{"t":"bogus"}` 看是否收到错误帧/close | P1 |
| 9 | 服务端**从不发 ping**（`WSConn` 未实现 `Pinger`），90 秒空闲判死对真连接不生效 | 宿主侧（实现 `Ping()`）或契约侧（写明"心跳由客户端负责"） | `heartbeat.go:34-41`；`ws.go` 无 `Ping` 方法；实测 35 秒 0 帧 | 订阅后静置 35 秒，数收到的帧 | P2 |
| 10 | **单帧 >65535 字节写不出去**（读侧 1 MiB、写侧只支持 16 位长度），推送失败会被 hub 摘掉订阅 | 宿主侧 | `ws.go:23`、`:185-187`、`:210-221`；`hub.go:146-163` | 造一个 70 KB 的 tool 结果，看连接是否被掐 | P2 |
| 11 | `session.compact` 界面**零调用**（宿主已实现，`compacted:false` 是正常结果） | **界面侧** | 宿主 `handlers_extra.go:78-118`、`serve.go:191-206`；界面 `grep -rn "session.compact" ../dsh-ui --include=*.go` 无命中 | 界面上点"压缩"是否发请求（现在没有入口） | P2 |
| 12 | `session.delete/fork`、`workspace.list`、`fs.read/write` 界面**无调用点** | **界面侧** | §5.3 表格 | 同 grep；再对真机点一次对应功能 | P2 |

另外两条**不是缺陷但必须知道**的约束：

- **一条 WS 只能订一个会话**（`ServerFrame` 没有 `topic`，`frame.go:76-85`）。想做"多会话后台流"，只能开多条连接，不能在同一连接上 `sub` 两个 topic。
- **journal 是内存的、容量 4096 帧**（`journal.go:33-37`、`:68-72`），宿主重启即空 → 断线时间超过宿主进程生命周期就补不回来，只能重新 `load` 重建基线。

---

## 附录 A：本次审计用到的命令（可原样复跑）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
mkdir -p /tmp/dsh-audit && go build -o /tmp/dsh-audit/dshd.exe ./cmd/dshd

# 起宿主（临时 home；端口 OS 分配）
cd /tmp/dsh-audit && nohup ./dshd.exe serve --home /tmp/dsh-audit/home --addr 127.0.0.1:0 \
    > out.txt 2> err.txt &

URL=$(sed -n 's/^ready {"url":"\([^"]*\)".*/\1/p' out.txt)
TOKEN=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' out.txt)
curl -s -c jar.txt "$URL/connect?token=$TOKEN"                 # 303
curl -s -b jar.txt -X POST "$URL/api/session.list" -d '{}'      # {"result":{"sessions":[]}}

# WS：订阅 + 心跳（python websockets 库）
python ws_probe.py "ws://127.0.0.1:PORT/ws" "<dshd_session 值>" "session:probe"
```

回收：`ps -W | grep dsh-audit/dshd` 取 **WINPID（第 4 列）**，`taskkill.exe //PID <winpid> //T //F`。用 MSYS 的 PID 调 taskkill 会报"找不到进程"（本次踩过）。

## 附录 B：未核实清单

1. 界面真机运行（`app.exe` 对真 `dshd serve`）——本次**没跑**，只读代码。
2. `session.approve` / `approval/request` / `approval/timeout` / `approval/cancel` 的**真机执行链**——只读代码与单测名，未真跑审批。
3. `session.compact` 的真机行为（宿主的 `Compact` 已接线，但没实际压过一次）。
4. `prompt` 在"上一轮没结束"时返回的具体 `code`（`errorCode` 里没有 busy 映射，推算是 `error` + `retryable:false`；而界面快照 `testdata/api.snapshot.json` 写的是 `busy/retryable=true`）——**未实测**，只做了代码推断。
5. 界面仓 19 个未提交改动是否已进 `app.exe`。
6. 界面插件层对 `view/permission/time/usageTotal` 的消费细节。
7. 迁到真实 home（180+ 会话）时的性能与分页表现。

## 附录 C：证据文件索引

| 主题 | 宿主文件 | 界面文件 |
|---|---|---|
| 方法/形状 | `internal/transport/api.go`、`testdata/api.snapshot.json` | `internal/apiclient/client.go` |
| 认证 | `internal/transport/server.go:244-279`、`cmd/dshd/serve.go:58-67,234-243` | `internal/apiclient/client.go:158-212` |
| WS | `internal/transport/frame.go`、`ws.go`、`wssession.go`、`heartbeat.go`、`hub.go` | `internal/apiclient/mux.go` |
| 首屏/补洞 | `internal/transport/history.go`、`source.go`、`journal.go` | `internal/apiclient/history.go` |
| 事件类型 | `cmd/dshd/serve_events.go`、`serve_journal.go`、`serve_approval.go` | `internal/apiclient/approval.go` |
| 契约文本 | `docs/transport-contract.md`（§2 是关键，§3 是 H 表） | `../dsh-ui/testdata/api.snapshot.json`（已过时） |

---

## 附录 C：界面真机跑通了（2026-11-04，本轮实测，推翻"未核实"）

审计里"界面真机运行"一项当时没能验。现在验了，用的是**界面仓自己的二进制**：

```bash
# 宿主
dshd serve --home tmp/uismoke --policy allow     # ready {"url":"http://127.0.0.1:64558","token":"…"}
# 界面（dsh-ui 仓，headless 冒烟模式）
cd ../dsh-ui && ./app.exe -addr http://127.0.0.1:64558 -token <token> -smoke -prompt "就回答两个字：收到"
```

界面输出（节选）：

```
插件 [ds-core ds-shell ds-commands ds-composer … ds-rightbar]，主题 ds-dark
mux 已订阅 session:d7ae3bd8-e2fd-3b65-63bb-083d00e9bb31 since=0 epoch=1652759093579083598
```

宿主侧的会话文件里，同一个会话：

```
turn/start      {"turn": 1}
user/message    {"content":[{"type":"text","text":"就回答两个字：收到"}], …}
assistant/message {"message":{"content":[{"type":"text","text":"收到"}],…}}
turn/end        {"reason":{"kind":"completed"},"turn":1}
```

**结论**：
- 界面能连、能订阅（`/ws` + cookie）、能发、能收到回答——**整条链路通**。
- `epoch=1652759093579083598` 是随机基准那个值，正好印证 M323 的 journalEpoch 修正确实作用到真客户端上（旧实现会是 `1`）。
- 界面仓 `go build ./...` 也是干净的。

仍未验的（不在这一跑的范围）：图形界面肉眼交互、审批在界面上的真实点选、`session.compact` 由界面触发。
