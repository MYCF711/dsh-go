# DSH-GO · TUI effort / permission modes / goals 最小宿主契约

> 本文是 W136 / 接口矩阵 P1-B 的 additive helper 接线说明。此次没有修改宿主代码、Transport 分派、canonical 25、快照或 golden。状态按当前源码核对，不把 mock Transport 测试称作真实宿主联调。

## 1. 现有能力与缺口

| 能力 | 当前可复用 Transport | 当前缺口 |
|---|---|---|
| effort levels / forModel | `models.catalog.list {}`；模型行 `reasoningEfforts:false` 或 `{level:wireValue}` | 无 |
| effort current | `session.load {id}` 的 `effort` | `effort` 是最近一次实际请求配置；选择覆盖的恢复需要下述 additive `effortSelection` |
| effort set | 无 | 需要 `session.setEffort` |
| permission modes list/current/set/cycle | `session.load.permission`、`session.setPermission {id,permission}`；枚举 `deny/ask/allow/auto` | 无；它与 `permissions.respond/pending` 的逐次审批完全分开 |
| goal set / clear | `session.setGoal {id,goal}`；`goal:""` 清除 | 现有扁平字符串 store 不含官方 goal snapshot；真实 change 仍需下述状态机与 journal |
| goal get | `session.load.goal` 只能提供字符串，不能提供 `id/revision/phase/maxGoalRounds` | 需要 `session.goal.get`；不得把字符串伪造成 snapshot |
| goal pause / resume | 无 | 需要 `session.goal.pause/resume` |
| goal change | 当前 WS/journal 没有 `goal/change` | 需要宿主真实状态迁移产生并持久化 Transport 帧 |

`settings.form.*` 不需要为本次能力增加字段；模型目录继续使用 `models.catalog.list`，不要把 canonical `models.list` 改成语义不同的持久目录。

## 2. additive HTTP 契约

所有方法继续遵循 `POST /api/<method>`、裸参数、`{result:…}`、四字段错误和既有 cookie 认证。下列方法是独立 additive 扩展，不加入或重排 canonical 25。

### 2.1 `session.setEffort`

请求：

```json
{"id":"session-id","effort":"high"}
```

- `effort` 必须属于该会话当前模型在 `models.catalog.list` 中声明的 `reasoningEfforts` key；不要用 wire value 替代 key。
- `effort:""` 清除会话选择覆盖，回到模型/宿主默认；`null` 不进入线上 JSON。
- 成功：`{result:{ok:true}}`。持久化失败必须返回错误，不得回 `{ok:true}`。
- 写 `<home>/state/` 下既有会话设置存储或同语义的新独立 store，下一轮 runner 真实读取；不得只改 footer，也不得伪造一条历史 `request/header`。
- `session.load` 可继续保留现有 `effort`（最近实际请求配置）。为在“已选择但尚未跑下一轮 / 重开”时恢复选择，追加可选 `effortSelection`：有覆盖时为所选 level，无覆盖时省略。helper 的优先级是 `effortSelection ?? effort`，不会拿进程 `--reasoning` 补未知值。

### 2.2 `session.goal.get`

请求：`{"id":"session-id"}`。

无目标：

```json
{"result":{"goal":null}}
```

有目标：

```json
{
  "result": {
    "goal": {
      "id": "goal-native-id",
      "revision": 4,
      "objective": "finish the adapter",
      "phase": "active",
      "maxGoalRounds": 8
    },
    "roundsStarted": 2
  }
}
```

`goal` 必须使用真实原生 id 与持久 revision；`phase` 为 `active/paused/blocked/complete`。`blocked` 时带真实 `blockedReason{code,message}`。只有宿主真实计量 token/time budget 时才带 `budget{tokensUsed,tokenBudget,timeUsedSeconds}`，否则省略，不补零。`roundsStarted` 未知时省略。

### 2.3 `session.goal.pause` / `session.goal.resume`

请求均为 `{"id":"session-id"}`；成功均为 `{result:{ok:true}}`。

调用只请求宿主状态迁移。非法状态迁移、持久化失败、revision 冲突必须走真实错误；成功响应本身不被 TUI 当作 change event。

### 2.4 既有 `session.setGoal` 的保留语义

helper 的 `goals.set` 仍发送 `{id,goal:objective}`，`goals.clear` 仍发送 `{id,goal:""}`，不新增平行 set/clear 方法。宿主可在保持该 HTTP 形状不变的前提下，把底层扁平 map 升级为上述 snapshot 状态机：

- 首次设置产生真实 `create`，替换产生真实 `edit`，清除产生真实 `clear`；
- pause/resume 分别产生真实 `pause/resume`；
- objective 的 trim/长度规则仍由宿主权威执行；helper 不用请求值猜截断后的 snapshot；
- 当前 helper 明确拒绝 `tokenBudget`，所以本次无需为 `session.setGoal` 增加 budget 字段。

## 3. 真实 `goal/change` WS / history 契约

每次已提交的目标状态迁移，由宿主追加到该会话同一个 Transport journal，并以普通 `{seq,type,data,time}` 帧推送；`session.history`、断线补洞和重连必须读到同一事实。`type="goal/change"`，`data` 示例：

```json
{
  "kind": "goal/change",
  "version": 1,
  "operation": "pause",
  "goal": {
    "id": "goal-native-id",
    "revision": 5,
    "objective": "finish the adapter",
    "phase": "paused",
    "maxGoalRounds": 8
  },
  "roundsStarted": 2
}
```

clear 帧使用 `operation:"clear"` 且 `goal:null`（或省略 goal）。续轮使用 `operation:"round"` 与真实非负整数 `round`，不重复 snapshot。其他操作必须带完整 snapshot。宿主按既有 v4/Transport 事件接入顺序完成 type 白名单、additive snapshot/golden 与真实 A/B、history、重连验证；不得由 TUI 在 HTTP ACK 后本地合成事件。

## 4. 已可运行与待宿主

- **现在即可由 backend owner 接线运行**：effort 目录解析 / levels / forModel、baseline current；modes list/current/set/cycle；goal set/clear 的既有 HTTP 调用；goal 真实帧 parser 与订阅清理逻辑。
- **待宿主 API**：`session.setEffort`、可选 `session.load.effortSelection`、`session.goal.get/pause/resume`。
- **待宿主真实事件与状态机**：完整 goal snapshot、pause/resume 状态迁移及 `goal/change` 的 WS/journal/history/reconnect。没有这些时，helper 保持无 goal 事件，不用 `session.load.goal` 或 RPC ACK 伪造。
- 本任务只跑 Node 数据 helper 测试与 TypeScript/boundary 门禁；没有启动 dshd、没有真实宿主联调，也不声称 `/effort` 或 `/goal` 已在产品中可用。

## 5. backend owner 接线点（不在本任务修改）

1. 构造 session 时各创建一份 `createDshGoEffort` / `createDshGoModes` / `createDshGoGoals`；将三个 `.capability` 分别放进 `capabilities.effort/modes/goals`。
2. `session.load` baseline 同时喂给 effort/modes 的 `seed`；启动后调用 effort `refresh()`。模型切换成功调用 `noteModel` 并按 owner 既有模型目录生命周期刷新。
3. effort/modes 的 `options.emit` 接现有 session `emit`；它们的本地状态与事件只在 Transport 写成功后推进。
4. goals `subscribe(event => emit([event], false))` 只订阅一次；每个真实 WS/history 帧交给 `acceptFrame`。对 `frame.type === "goal/change"` 不再走默认 custom 翻译；无效帧没有 goal 事件。session dispose 先退订并调用 `goals.dispose()`。
5. 如需启动恢复 goal，调用 `get()` 取得权威 snapshot 后由 owner 直接建立 baseline 状态（不要凭一次读取新发一条 live change）；`get()` 自身绝不发 change，避免把一次读取伪装成一次状态迁移。
