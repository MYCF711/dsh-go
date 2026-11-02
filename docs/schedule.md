# 定时任务：从"登记"到"到期"

## 两层分工

| 层 | 做什么 | 在哪 |
|---|---|---|
| 工具层 | `schedule_create/list/update/delete`，只**登记**并算出 `next_at` | `modules/tool/schedule` |
| 时间层 | 判"现在谁到期了"、算"下一次"，并原子推进 `next_at` | `internal/schedule` |
| 入口 | `dshd schedule list` / `dshd schedule due [--advance]` | `cmd/dshd/schedule.go` |

## 为什么不做常驻 ticker

宿主的两种形态都不支持"到点自己发一轮对话"：

- `dshd run` 是一次性的：跑完就退出，没有可以唤醒的会话。
- `dshd acp` 虽然是长连，但 ACP 里**没有宿主主动发起 turn 的通道**——
  协议是客户端驱动（`session/prompt`），服务端只能响应。

硬起一个 goroutine 定时调模型，会得到一个**自己跟自己说话**的东西：
它在跑 turn，客户端却不知道，也没法插话或取消。这比定时任务不触发更糟。

所以这里只回答两个问题，把"怎么处理"留给调用方：

```
$ dshd schedule due
到期 acp-x  每小时体检
  提示: 跑 doctor 看看
（只看了没动；加 --advance 才会把 next_at 推到下一次）
```

调用方可以是一行 cron、一个 shell 循环，或者以后的 GUI 客户端。

## 时间算术的三条坑

1. **`every` 落后好几格**：不能只 `+EverySec`，要一格一格推到 `now` 之后——
   否则机器休眠一小时后醒来，任务会被追认成"只触发一次"，而不是补齐。
2. **`daily` 当天没过点算今天**：`23:59` 在凌晨查应当返回**当天** 23:59，不是明天。
3. **坏输入不许返回零值时间**：零值 = 1970 年 = 立刻永久触发。
   测试 `TestBadInputsDoNotPanic` 专门钉这条。

## 推进为什么要原子

`Advance` 先写 `.tmp` 再 `rename`。理由与工具模块一致：
进程被砍在半路时，宁可留一份旧的完整文件，也不要留半份 JSON——
半份 JSON 会让 `Load` 报错，而报错会让**所有**定时任务停摆。

## 最后一公里：`dshd schedule run`

```
$ dshd schedule run --home <dir>
跑 session-…f1ac  到期自检
到期                                  ← 模型在**原会话**里回答
stats: tool_calls=0 input=5274
  已推进 1 条                        ← next_at 05:00 → 05:50
```

它复用 `cmdRun`（同进程调用），所以审批、压缩、日志、落盘全与普通一轮一致——
不是另写一套"定时专用执行器"。

### 踩到的坑：续会话不等于看得见历史

`dshd run --session <id>` 第一版只把日志打开（append），没读历史。
结果模型在旧日志上追加消息，却回答"这是本会话的第一条消息，上下文里没有任何暗号"。

**日志是给人看的档案，不是模型的上下文**——这两件事必须显式连起来：
先 `v4.ReadFile` + `session.History`，再把新消息追加上去。

这也是为什么"看起来接上了"最危险：文件在长、event 在写、`stats` 有数，
只有模型是瞎的。

