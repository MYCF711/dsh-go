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

## 还没做

- 全屏 alt-screen 与差分重绘
- 流式 Markdown、工具卡、上下文进度条 / TPS
- 命令（`/resume`、`/compact`…）、@ 文件引用、历史搜索
