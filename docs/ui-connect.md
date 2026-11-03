# 把 Go 界面接到我们宿主上：第一次尝试的记录（2026-11-03）

## 做了什么

```bash
./dshd.exe serve --home tmp/uiconn --addr 127.0.0.1:7799 --policy allow
cd ../dsh-ui && go run ./cmd/app -addr http://127.0.0.1:7799 -smoke
```

## 拿到了什么

- ✅ 宿主起来了：`ready {"url":"http://127.0.0.1:7799",...}`
- ✅ **界面进程真的起来了**：`app.exe` PID 36332，工作集 55 MB（WebView 窗口已开）
- ✅ `dsh-ui` 能编译（`go build ./...` → OK，23 个 Go 文件 / 4,157 行）

## 没拿到什么（这才是重点）

**没有任何证据表明它成功对话过。** 临时 home 里：

```
tmp/uiconn/sessions/   → 不存在
tmp/uiconn/logs/       → 不存在
```

`-smoke` 本该自动发一条消息，但什么都没发生。

## 两个已知障碍

1. **默认端口被占**：`cmd/app` 默认连 `127.0.0.1:7788`，而那上面有别的进程
   （`bind: Only one usage of each socket address`）→ 必须显式 `-addr`
2. **空 home 里没有会话**：`-smoke` 大概率需要先选中一个会话才能发，
   空 home 下它没有可发的地方——这让"能不能对话"验不出来

## 结论（按 probe-checklist 第 5 条）

**报「未验证」，不报「打通」。**

起来 ≠ 连上 ≠ 能对话。这三件事在进程表上长得一模一样：
都是 `app.exe` 在跑、都有窗口、都可能显示"没有会话"。

## 下一轮怎么验

1. 先用 curl 在 `tmp/uiconn` 里建一个会话并灌一条消息（宿主侧已知可行，M175/M177 验过）
2. 再起 `cmd/app`，带 `-session <id>` 直接打开它
3. **判据**：宿主侧 `logs/` 出现记录，或该会话文件多出新的 user/message 帧
   —— 这证明"界面真的调用了宿主"，而不是"窗口打开了"
