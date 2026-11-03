# H6 / H7 的真实结构（2026-11-03 直接从真实语料数出来）

扫 6 个真实会话文件、822 条 `assistant/message` 事件：

```
顶层键: message / step / stream / turn / usage / interrupted
块类型: reasoning:712   text:49   tool-call:912
```

## 两个结论

### ① H7 是真的：内容在 `data.message.content`，不在 `data.content`

真实事件的顶层**没有** `content` 键，正文与思考块都在 `message.content[]` 里。

所以：**任何只读 `data.content` 的代码，在真实语料上会一条助手消息都读不到。**
这正是 H7 描述的"整会话助手消息一条都不显示"。

### ② H6 的 reasoning 确实存在，而且比正文多得多

`reasoning:712` vs `text:49` —— **思考块是正文的 14 倍**（H6 说 8 倍，同一量级）。

## 那个 0 是怎么来的（探针错了）

派给 dshd 的 `scripts/reasoning-shape.py` 扫了 **27,983 条** `assistant/message`，
报告"带思考块 0 条"。原因它只看了 `data.content[]`——**那个键在真实语料里不存在**。

> **探针在错误的位置上找东西，"没有"和"不存在"长得一模一样。**

## 对交付的影响

| 项 | 真实情况 |
|---|---|
| H6（WS 保留 reasoning） | 我们 follow **原样转发 `ev.Data`**，所以 reasoning 天然在流里；本轮活体没复现，是因为那次回答模型没思考 |
| H7（content 落法） | 真实语料是 `data.message.content`；要确认我们的 `textOf` 两个都认 |

## 下一步

1. 确认 `cmd/dshd/transport_source.go` 的 `textOf` 对 `message.content` 有效（真实语料 load 已能出消息，大概率已覆盖，但要写成断言）
2. 让 dshd 修 `reasoning-shape.py` 的键路径，重跑拿到 reasoning 的**键路径与样本**
3. H6 收尾：等一次真带思考的回答，抓 WS 帧确认 reasoning 原样在
