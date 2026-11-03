# dshtui：终端界面（客户端）

`cmd/dshtui` 是宿主线自己的终端客户端，经 **Transport**（HTTP `/api` + `/ws`）说话，
不越界去读内核。它是 `PLAN.md` §11 里 M4（TUI）那条的落点。

## 现在能做什么

```bash
./dshtui --dump-splash          # 开屏（逐格比对用）
./dshtui --dump-splash --color  # 带真彩
./dshtui --addr <宿主 url> --token <token> --send "你好"
```

`--send` 是最小闭环，同时是**链路自检**：建会话 → 订阅 → 发消息 → 按行打印帧。

真机实测（2026-11-04）：

```
会话 27ef982e-9dd7-…
  1 turn/start         {"turn":1}
  2 user/message       {"content":[{"text":"用一句话回答：1+1 等于几",…}]}
  3 assistant/message  {"message":{"content":[{"text":"1+1 等于 2。"…}]}}
  4 turn/end           {"reason":{"kind":"completed"},"turn":1}
  5 turn/done          {"turnId":"turn-1"}
用时 2.482s
```

## 两个自己写的零件，以及为什么

**开屏点阵字体**：`splash_gen.go` 由 `scripts/import-splash.py` 从 dsh-TUI（MIT）
的 `splashFonts.ts` **机械生成**。不手抄的理由很直接——5 行点阵抄错一格，
屏幕上只会像"这个字母有点怪"，没有任何东西会报错。详见 `cmd/dshtui/NOTICE.md`。

**WebSocket 客户端**：`wsclient.go` 自己实现 RFC 6455 的客户端最小集
（握手 + 读文本帧 + 写文本帧 + 应答 ping）。不引库是因为我们只要这几样，
而通用库会把压缩/扩展/子协议/重连一起带进来，二进制体积是实打实的代价
（门禁 ≤25 MiB，当前二进制 2.7 MB）。

**它不做什么**（写在这里免得被误以为支持）：不支持分片重组、扩展、压缩、子协议。
服务端发的都是单个 FIN 文本帧，够用。

### 关于「压缩 / 扩展 / 子协议 / 重连」

前三个是 **WebSocket 的可选能力，双方都声明不用就不需要协商**，不是缺陷：

| 能力 | 状态 | 为什么不需要 |
|---|---|---|
| permessage-deflate（压缩） | 都不用 | 本机回环，帧是几 KB JSON；压缩只省带宽，不省时间。真要上，得服务端也实现扩展协商 |
| 扩展 | 不用 | 扩展全是可选协商；一个都不用是**合法的默认** |
| 子协议 | 不用 | 我们的协议靠帧内 `{type,data}` 区分，不靠 `Sec-WebSocket-Protocol` |
| **重连** | **客户端已实现** | 见下 |

**重连是真需求，而且它不在服务端——服务端早就把难的做好了**（H14）：

- `sinceSeq`：订阅时带上"收到第几条了"，服务端只补之后的
- `session/reset`：世代对不上时明说"基线作废"，客户端据此重建

客户端要做的只是记住 `lastSeq`、断开就重订阅（退避 0.5s→5s 封顶）、看到 reset 就清零基线。
已实现，并有 `TestBackoffGrowsAndCaps` 钉住退避行为。

**分片**：服务端明确拒绝（`FIN=0` 直接报错说明原因），不是静默丢弃。
什么时候才需要它：单帧超过几十 MB（比如图片走 WS）。目前图片走 artifact，不走 WS。

## 还没做

- 全屏 alt-screen 与差分重绘
- 流式 Markdown、工具卡、上下文进度条 / TPS
- 命令（`/resume`、`/compact`…）、@ 文件引用、历史搜索
