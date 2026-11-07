# WS permessage-deflate（0.2.2）

> 范围：宿主 `internal/transport/ws.go`、TUI `cmd/dshtui/{client.go,connection.go}`、UI `internal/apiclient/mux.go` 及各自测试。
> 基线：宿主 `af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 当前共享工作树；UI `3925e84f1524dd101e020d74b1d5bb422d25cf4d` + 当前共享工作树。未 commit/push，未启动 GUI。

## 1. 默认策略（exact）

- **默认 offer，协商成功才压缩。** 宿主不强制客户端使用压缩；TUI 与 UI apiclient 默认发送 `permessage-deflate` offer，旧服务端不回扩展头时继续使用未压缩连接。
- **旧客户端完全不需要新字段或新配置。** 没有 `Sec-WebSocket-Extensions` 的握手仍返回原来的 101，不带扩展响应头；路径仍为 `/ws`，控制 JSON、ServerFrame、HTTP API 均不变。
- 只实现 **RFC7692 permessage-deflate、15 位窗口、双向 no-context-takeover**。每条消息独立压缩/解压，不保留跨消息字典，也不把上一会话/上一代连接的字典带入重连。
- 宿主与 TUI 对 **≥512 字节且压缩后更小** 的文本消息设 RSV1；其他文本消息保持原始正文。UI 使用 `coder/websocket.CompressionNoContextTakeover`（库默认阈值 512 字节）。因此“已协商”不等于“每一帧都必须压缩”，接收方必须同时接受 RSV1=0/1。
- 没有新增全局开关、HTTP 参数、JSON 字段或持久化状态。这里的“可选”指逐连接握手协商，不是运行期修改 serve 业务配置。

## 2. 握手规则（exact）

客户端典型 offer：

```http
Sec-WebSocket-Extensions: permessage-deflate; client_no_context_takeover; server_no_context_takeover
```

宿主成功时只回：

```http
Sec-WebSocket-Extensions: permessage-deflate; client_no_context_takeover; server_no_context_takeover
```

参数处理：

| offer 参数 | 宿主策略 |
|---|---|
| `client_no_context_takeover` / `server_no_context_takeover` | 接受；响应显式要求双向不接管上下文 |
| `client_max_window_bits`（无值或 8..15） | 接受；响应不选择该参数 |
| `server_max_window_bits=15` | 接受 |
| `server_max_window_bits=8..14`、无值或非法值 | 不接受；当前压缩器不假装支持更小窗口 |
| 重复参数、带值的 no-context 参数、未知参数、空扩展、只有未知扩展 | 400 `bad_handshake` 四字段错误信封，fail-loud |
| 多份 offer 中含未知扩展，但另有合法 permessage-deflate | 选择合法 deflate；不回未知扩展 |

TUI 与 UI 也验证服务端响应；未 offer 的扩展、未知参数或不符合已协商模式的响应会使拨号失败，不静默按错误模式解码。

## 3. 帧、控制面与分片（exact）

- 应用协议仍是客户端 `t=sub/unsub/ping`、服务端 `{seq,type,data,time}`。压缩发生在 WebSocket 消息层，**解压后的 JSON 字节就是原来的 JSON 字节**。
- 宿主出站始终是单个 FIN=1 文本帧；客户端发往宿主也必须是单个 FIN=1 帧。宿主与 TUI 对 FIN=0 明确报“分片”错误，不做无界重组。UI 的分片接收由 `coder/websocket` 管理，但发往宿主时同样受宿主“单帧”限制。
- RSV1 只允许用于已协商连接的文本消息；RSV2/RSV3 永远拒绝。未协商连接收到 RSV1 会失败。
- ping/pong/close 永远不压缩，FIN=1，正文最多 125 字节。宿主收到 ping 原样回 pong，忽略 pong，收到 close 回 close 后返回 `io.EOF`；TUI 同样处理控制帧。应用层 `{t:"ping"}` 与 `{type:"pong"}` 仍是普通 JSON 文本消息，不与 WS 控制帧混为一谈。
- 宿主支持 RFC6455 的 16 位与 64 位长度编码，非最短编码拒绝。未压缩大文本帧不再被旧的 65,535 字节写出限制截断，而是统一受下表的 1 MiB 消息上限约束。

## 4. 有界内存与大 history（exact）

| 路径 | 上限与行为 |
|---|---|
| 宿主读 WS | 线上帧正文最多 **1 MiB**；若 RSV1=1，解压后也最多 **1 MiB** |
| 宿主写 WS | 先限制原始 JSON 最多 **1 MiB**，再尝试压缩；超出在触碰 socket 前报错 |
| UI apiclient | `Conn.SetReadLimit(1 << 20)`；`coder/websocket` 在 inflate 后限流，超限以 **1009 MessageTooBig** 关闭，不使用 `-1` |
| TUI 已协商连接 | 线上帧与解压后均最多 **1 MiB**；超限/坏 deflate/分片返回错误并进入既有重连逻辑 |
| TUI 未协商连接 | 返回旧 `*wsConn`，保留旧 32 MiB 读上限与旧读写路径；宿主本身仍只发不超过 1 MiB 的帧 |

解压使用 `io.LimitReader(limit+1)`，读到 `limit+1` 即拒绝；不是先 `io.ReadAll` 再检查长度。DEFLATE 写出使用 sync flush，线上移除标准 `00 00 ff ff` 尾；读取侧补消息尾及一个最终空块，以区分正常 sync-flush 结束和真正截断。最终空块只存在于内存读取流，不上 wire。

`session.history` 的分页、原始 `frames`、`journalEpoch/lastSeq` 不变。大 history 仍由多次有界 history/重放完成，不能借压缩把单条超大消息或整段历史无界展开。

## 5. 快照与 golden（exact / derived）

本轮 **没有修改** 两仓 `api.snapshot.json`、`contract-golden.json`、`ws-golden.jsonl`、`ws-golden-b.jsonl`：扩展协商发生在 HTTP Upgrade 头与 WS 帧层，不改变快照里的 method/schema，也不需要新造 golden 事件。

双仓共享文件当前逐项同 SHA-256：

```text
f90c8a8cfb66d42d31c8dc446e30ef3d2b4730eb51e0d22065ec99d70459f1e8  contract-golden.json
254c474897aa446e605553254068cf4f9f26a0f73ed1952bd5fbed98326db4bc  ws-golden.jsonl
59f1c11ca40501de2b200e486777202dfe5dedff28f8382790df61db73cd624e  ws-golden-b.jsonl
```

- **exact**：宿主测试直接比较压缩/未压缩线上 JSON 与 `json.Marshal(ServerFrame)`；UI 测试经真实 `Mux` 比较 A/B 两会话的 `seq/type/data/time`，覆盖 text、reasoning、tool、approval、reset，并加入派生的 140 KB 级大 history 帧。
- **derived**：由“解压后 JSON 字节相同 + golden 文件哈希未变 + journal/epoch 测试全绿”推导原始 history 语义不变。没有把压缩字节本身写入 golden，因为 DEFLATE 字节不是 Transport canonical 数据模型。

## 6. 先红后绿与本轮实证（exact）

红测先于实现运行，实际看到：宿主/UI/TUI 均无压缩协商响应或 offer、非法扩展被接受、宿主大帧写失败、TUI 压缩炸弹/分片被当文本。实现后执行：

```bash
# 宿主定向与重复
go test ./internal/transport -count=1
go test ./cmd/dshtui -count=1
go test ./internal/transport ./cmd/dshtui -run 'Compression|Deflate' -count=20
go test ./cmd/dshtui -run 'TestTUIDeflate' -count=10

# UI 定向与重复
go test ./internal/apiclient -count=1
go test ./internal/apiclient -run 'TestMuxCompression' -count=20

# 两仓全量（各自仓根目录）
go test ./... -count=1
go vet ./...
go build ./...

# 定向 race（两仓均使用本机已有 MinGW-w64）
PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH" CGO_ENABLED=1 CC=gcc \
  go test -race ./internal/transport ./cmd/dshtui -count=1
PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH" CGO_ENABLED=1 CC=gcc \
  go test -race ./internal/apiclient -count=1
```

结果：上述命令退出码均为 0；宿主全仓、UI 全仓 test/vet/build 绿，定向 race 0 DATA RACE。TUI 真实压缩 socket 重连连续 10 轮通过：第二次订阅 `sinceSeq=2`，`open(generation=1) → down(1) → open(2)`，reset 不回退代际。

另有仓外临时模块 `C:/Users/Administrator/AppData/Local/Temp/ws-compression-interop`，以真实宿主 `transport.Upgrade` 对 UI 同款 `coder/websocket` 做双向互通：大 sub 客户端→宿主、大 reasoning/text ServerFrame 宿主→客户端。`go test -count=1 -v` 为 `PASS`。它只增加测试证据，没有给宿主新增依赖。

## 7. 未验证与边界（unverified）

- 未启动可见 GUI，未跑真实 `dshd serve + cmd/app + 临时 home + 模型/真实工具审批` 的整天产品联调；本轮是 WS/真实客户端夹具、TUI socket 与库级互通证据，不外推为完整 H6/H13/U1 产品完成。
- 未做压缩率、CPU、内存或长工作日性能基准；不宣称“开启后一定更小/更快”。小帧或压缩后更大的帧在宿主/TUI 会保持未压缩。
- 未实现 context takeover、其他扩展、宿主入站分片重组；这些场景按上文明确拒绝，而不是静默支持。
- `-race` 覆盖本轮相关宿主包与 UI apiclient，不等同两仓所有包的 race 全量复验。
- `wss` 的 TLS 配置与代理路径未在本轮改变或验证。
