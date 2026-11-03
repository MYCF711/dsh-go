# WS 契约夹具（H14）：`testdata/ws-golden.jsonl`

> 这一份文件是**跨仓共享**的：宿主用它做形状断言，界面仓用它写解析器。
> 改它等于改契约——先改 `docs/transport-contract.md`，再两边一起改。

## 为什么要有它

H14 之前，宿主说"我推 `{seq,type,data,time}`"，界面说"我收 `{t:"ev",kind}`"。
**两边都能自测通过**，连起来一个字也通不了。

夹具的作用是把这个分歧变小：**同一批字节，两边都跑一遍**。
它不是某次运行的真实抓包（那种东西会随模型输出漂），而是**约定形状的手写样本**。

## 帧目录

| # | type | 有 seq | 用途 |
|---|---|:--:|---|
| 1 | `turn/start` | ✅ | 一轮开始；界面的"正在思考"从这亮起 |
| 2 | `user/message` | ✅ | 用户这句话（回显，不靠本地乐观更新） |
| 3 | `assistant/message` | ✅ | **只含 reasoning 块**：折叠成"思考中" |
| 4 | `assistant/message` | ✅ | 含 text + tool-call：正文与工具调用同帧 |
| 5 | `tool/call` | ✅ | 工具开始跑（与 4 的 tool-call 对应） |
| 6 | `tool/result` | ✅ | 工具结果 + `view`（card/table/plain 三选一） |
| 7 | `approval/request` | ✅ | **要人拍板**：options 里的 optionId 原样回传 |
| 8 | `turn/done` | ✅ | 这一轮结束；界面在此收尾 |
| 9 | `pong` | ❌ | 心跳；**不许带 seq**（带了会污染连续性判断） |
| 10 | `session/reset` | ❌ | 世代换过了，丢弃旧水位重新 load |

## 三条硬规则（测试逐条钉着）

1. **type 必须在白名单里**。界面靠 type 选渲染器，多一个没约定的词＝未定义行为。
2. **带 seq 的帧，seq 从 1 连续递增**；`pong` 与 `session/reset` **必须没有 seq**。
3. **每帧都要有墙钟 `time`**（毫秒）。没有 time，界面就算不出 tok/s（H2）。

对应的测试：`golden_frames_test.go` 的 `TestGoldenFramesMatchContract`。

## 重放语义

`TestGoldenFramesReplayInOrder` 把夹具喂进 `Journal`，断言：
**帧原样进，事件按序出，seq 与夹具写的一致**。

界面仓应当有一份等价测试：读同一份 `ws-golden.jsonl`，折叠出同样的事件序列。
两边都过，才叫"契约对齐"；只有一边过，说明还有一套私有理解。

## 已知未覆盖

- `turn/error`、`approval/timeout`、`approval/cancel` 只在白名单里，**夹具还没有样本**
  （需要构造真实失败与超时，不是手写就能当真的）。
- 夹具不含真实 `usage` 之外的多模态块（图片等）。
