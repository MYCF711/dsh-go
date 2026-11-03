# 宿主侧欠界面的东西（U0 落地清单）

> 来源：`dsh-ui/docs/u0-handoff.md`（界面那边写好的交接文档）+ `dsh-ui/docs/verdict.md`（三实验判定）。
> 本仓 **`internal/transport/` 一行代码都还没有**。这份清单是它落地时要兑现的承诺。

## 一、只做会话那 6 个 method + WS

界面自己给的优先级：**先做 `session.list / load / history / prompt / cancel / create` 六个 + WS 流**。
`delete` / `fork` / `workspace.*` / `fs.*` / `config.*` 都还没接 UI，**留到 U2**。

少做一半，早点能用。

## 二、每个 method 的"必须保证"（这些才是契约，字段名是次要的）

| method | 必须保证 | 我们现在的状态 |
|---|---|---|
| `session.list` | 每个会话给 `id/title/cwd/updatedAt`，**`title` 要能看懂** | `session.ListSessions` 已有一句话摘要 ✅ |
| `session.load` | `messages[]`（`seq`/`role`/`text`）+ `total` + `truncated`；**`role` 区分 user/assistant/tool**，tool 带 `tool`/`args`/`summary` | `session.History` 有 canonical 模型 ✅，要加 `truncated` |
| `session.history` | **断流后按 seq 增量补**，不是整页重拉 | 无 ❌ |
| `session.prompt` | **立刻返回** `accepted`+`turnId`，回答走 WS，**不许阻塞** | `acp` 侧已有异步 turn ✅，要拆出 transport 版 |
| `session.cancel` | 取消后 **`busy` 必须清**，否则下一次 prompt 永远 409 | `TestSessionUsableAfterCancel` 已钉住这个语义 ✅ |
| `session.create` | 返回新 `id` | ✅ |

## 三、WS 帧形状（界面说"这个不能改"）

```jsonc
// 客户端发
{"t":"sub", "topic":"session:<id>", "sinceSeq": 123}   // sinceSeq 可省 = 从头
{"t":"unsub", "topic":"..."}
{"t":"ping"}
```

**事件本体必须用 v4 的原始词汇**，不许为 GUI 造第二套名字——压缩/遮蔽语义只有一份真相。

## 四、三条不可协商的铁律

1. **端口一律 `--port 0`**，由 OS 分配；壳从 `ready{url}` 拿。写死端口 = 多开必冲突。
2. **launch token → `/connect` 换 cookie**；**cookie 永不进 WebView**（渲染进程能读 token = 一个 XSS 交掉整台机器）。
3. 本机回环**不是**免认证的理由——现在 mockd 没有认证，U0 落地时这三条要一起上。

## 五、界面踩过、宿主必须避开的三个坑

### ① 审批的 id 必须**原样**回

界面对 `request_permission` 的应答里带着它发出去的 id；宿主自己重新生成一个，
界面就认不出来，**整轮永久挂起且毫无报错**（看起来像模型不响应）。

### ② `busy` 标志必须有唯一的结束点

`prompt` 进来置忙、turn 结束清忙。**异常路径也要清**——否则下一次 prompt 永远 409，
而用户只看到"发不出去"。

### ③ 全量解码真实会话要 6 秒

`session.load` 如果每次都全量解码 94 MB 语料里的某个大会话，界面点开要等 6 秒。
必须先读 header + 尾页，按需再补。

## 六、验收线（界面那边量过的）

| 项 | 数字 |
|---|---|
| 宿主内存 | 15.7 MB |
| 界面内存 | 75.5 MB（图形栈地板 67 MB） |
| **全链** | **≤ 90 MB**（相对官方 1,603.8 MB 是 17.6×） |
| 冷启 | 171 ms |

## 七、这件事跟"自己开发自己"的关系

`internal/transport` 是**纯协议翻译**：只认 `spi`，不认 `ui/`。
这正好是可以整块交给 dshd 的那类活——接口全部由上面这张表定义死，验收也能量化
（13 个 method → 先 6 个；契约快照；`--port 0`；token/cookie；三个坑各一条测试）。

任务书怎么写，见 `docs/WORKER.md` §6.5 的三问。

---

## 断线重连：必须带 `lastSeq`（界面侧义务）

协议已经支持了，但**没有任何一处写明界面重连时要带水位**。
界面作者不知道要传，就会每次从 0 重放——看起来"能用"（消息都在），
但那一屏是「全部重新加载」，不是「接着刚才看」。

### 约定

1. 界面收到每一帧都记下 `seq`（服务端保证同一条连接上严格递增）。
2. WS 断开后重连，第一个帧必须是：

   ```json
   {"t":"sub","topic":"session:<id>","sinceSeq":<最后一次见到的 seq>}
   ```

3. 宿主只会推 `seq > sinceSeq` 的帧；**已见过的不会重发**。
4. 若界面拿不到水位（首次连接、或换了会话），传 `0`——这是唯一允许的"重放全部"。

### 为什么这条不能靠"界面试试看"

重连有两种坏法，长的完全不一样：

| 做法 | 现象 | 用户感受 |
|---|---|---|
| 不带水位（每次 0） | 消息重复、滚动条跳回顶部 | 「界面又抽风了」 |
| 带了但记错 | **少一段**，中间有空缺 | 「它怎么跳过了我刚才说的话」 |

第二种更难查：没有报错，没有重复，只是少了。所以水位必须**每帧落一次**，
不能"断线的时候再回头算"。

### 宿主侧已保证的

- `Hub` 只推 `seq > sinceSeq`（`TestHubRespectsSinceSeq`）
- 晚到的旧帧丢弃（`TestHubDropsLateOldFrames`）
- 连接死掉自动摘订阅（`TestServeConnUnsubscribesOnClose`）
- 读侧 90s 无帧 → `ErrConnIdle`（`TestServeConnReturnsIdleAfterSilence`）

也就是说：**界面只要老实记 seq，重连就是无损的**。缺的那一半在界面侧。
