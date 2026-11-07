# DSH-GO · TUI context / MCP / auth / sideQuery 宿主契约与接线说明

> W141 / 接口矩阵 P1-E。TUI 侧只新增 additive helpers、测试与 `src/dsh-go/` 镜像；本次不修改 `backend.ts`、`manifest.ts`、Transport、canonical 25 或 Go 宿主。下文的“可运行”指 helper 与现有真实 API 的适配已可接，不代表已经完成产品接线或真实 dshd / ConPTY 验收。

## 1. 当前能力边界

| 能力 | 当前可复用的真实宿主能力 | 当前缺口与 helper 行为 |
|---|---|---|
| context summary | `session.load {id,tailMessages:1}` 的 `contextTokens/contextWindow` | 可直接运行。缺 `contextTokens`、负值或非整数均抛错；`contextWindow` 缺失/0 时不提供 `max`。只产生一个来自真实总数的 `context`/`used` 聚合 category，不编造 sections/files/skills/tools。 |
| context full | 无 | 调 additive `context.usage`；方法缺失或响应畸形原样失败，不以 summary 冒充 full。 |
| MCP status / reconnect / toggle | 当前宿主无 MCP Transport，也未找到可复用的 MCP runtime | 等 `mcp.list/reconnect/toggle`。helper 不缓存或编造服务器，不把传输错误变成空列表或成功。 |
| auth status / API-key login / logout | `session.load.model`（或 backend 已确认的 live model override）、`models.catalog.list`、`models.catalog.upsert {model:{id},credential}` | 只对目录中声明了 `apiKeyEnv` 的当前模型工作。configured 是宿主真实投影；环境凭据与 stored credential 不可区分时不猜来源。无引用的 legacy 模型只报告环境状态未知。 |
| OAuth | 无 DSH-GO 可消费的宿主 OAuth provider / token 路径 | `oauthProvider` 保持 absent，login 不调用宿主 OAuth 回调，不把别的 provider 的 token 写成 DSH-GO 凭据。 |
| sideQuery / recap / btw | 普通 `session.prompt` 会推进并持久化主会话；当前没有 ephemeral/read-only/tool-less fork 或独立 runner | 必须注入显式标记的真实隔离 runner；未提供即构造失败，backend 不得声明 sideQuery。绝不用普通 `session.prompt` 代替。 |

## 2. context additive full-report API

新增 `POST /api/context.usage`，沿用 cookie 认证、裸参数、`{result:…}` 和四字段错误，不加入或重排 canonical 25。

请求：

```json
{"id":"session-id","detail":"full"}
```

result 直接采用中立 `ContextUsageView` 的线上形状：

```json
{
  "used": 50000,
  "max": 128000,
  "categories": [{"name":"messages","tokens":30000,"kind":"used"}],
  "sections": [{"name":"system","tokens":5000}],
  "files": [{"path":"D:/work/AGENTS.md","tokens":1000}],
  "skills": [{"name":"review","tokens":500}],
  "tools": [{"name":"read","tokens":100},{"name":"search","server":"docs","tokens":200}]
}
```

`used/categories` 必填；`max` 若提供必须是正整数；所有 token 数必须是非负安全整数。可选 sections/files/skills/tools 只有宿主有真实计量时才返回。`contextTokens` 的现有 summary 语义不变。

## 3. MCP additive API

三个方法都带当前 session id，使宿主可以按实际绑定路由；若实现选择全局 MCP registry，也必须验证该 id 对应的真实会话，不能忽略参数后把别的会话结果投影进来。

| method | 裸参数 | 成功 result |
|---|---|---|
| `mcp.list` | `{id}` | `{servers:[{name,status,toolCount?}]}`；真实空目录才是 `[]` |
| `mcp.reconnect` | `{id,name}` | `{ok:true}`，只有重连动作已被宿主接受/完成才返回 |
| `mcp.toggle` | `{id,name,enabled}` | `{ok:true}`，只有真实 enabled 状态写入成功才返回 |

`name/status` 必须非空字符串，`toolCount` 必须是非负整数。未知名称、连接失败、状态写失败均使用标准错误；HTTP 200 但无 `{ok:true}` 不会被 TUI 当作成功。不要复用 `settings.form.*` 的修订号成功来冒充 MCP 已重连。

## 4. auth 的真实存储语义与限制

helper 只适配已经存在的模型目录 API-key 凭据：

1. 从 backend 当前已确认模型（优先）或 `session.load.model` 定位模型；模型未知时不拿 active/default 冒充当前会话模型。已注入 live model getter 但它返回空串时同样失败，绝不回退到可能属于上一轮的 load.model。
2. `models.catalog.list` 必须返回该模型的 `apiKeyEnv` 与 `credentials[apiKeyEnv]={configured,writable}`。缺真值即失败或明确“环境状态未知”。
3. login 经 backend question bridge 发 `question.request`，问题带 `secret:true,purpose:'auth'`；秘密只存在于回答与 `models.catalog.upsert` 请求，不进事件、日志、session prompt 或错误文本。
4. 写 `{model:{id},credential:<secret>}` 后必须在新 CatalogView 中确认同一 ref 且 `configured=true`。
5. logout 写 `credential:""` 清除 stored credential，再确认投影。若 `configured` 仍为 true，说明同名环境变量仍提供凭据；helper 明确报告“stored 已清、env 仍有效”，不宣称完整登出。
6. 当前 runner 每轮解析目录 provider/credential，因此 `reconnect()` 不重启主会话、不取消在途 turn，也不伪发 auth 事件。

`/login` 的通用 channel 可以消费 `auth.login/status/reconnect`。但当前 `core/compose.ts` 的 `/logout` 只走宿主 `dshAuth.logout(oauthProvider)`，不会调用 backend-native `auth.logout`；本 helper 因而显式导出 `logout`，但**通用 channel owner 还需增加 native logout 接缝**，仅接 backend capability 不会让 `/logout` 调到它。本任务没有修改该共享宿主代码。

## 5. sideQuery 隔离 runner 契约

`createDshGoSideQuery(sourceSessionId, runner)` 只接受：

```ts
interface DshGoSideQueryRunner {
  readonly isolation: 'ephemeral-session' | 'independent-runner'
  run(request: {
    sessionId: string
    prompt: string
    signal: AbortSignal
    onText(delta: string): void
  }): Promise<string>
}
```

宿主/runner owner 必须兑现：

- 从 source 会话的同一持久化边界读取上下文，在独立 runner 或真实 ephemeral session 中执行单次、无工具、只读回答；父会话 v4、Transport journal、queue、goal、title 均不写。
- `sessionId` 是源会话 id，不是让 runner 把 prompt 发回该会话。A/B 调用必须各自隔离，取消只终止自己的旁路执行。
- `signal.aborted` 后停止模型请求/子进程并释放临时订阅；迟到 text 不再回调。helper 也会在取消后屏蔽迟到 delta，并返回 `{answer:null}`。
- 非流式 runner 可只返回完整字符串；流式 runner 经 onText 给增量并返回最终全文。空答案与失败不能变成成功。

当前宿主没有满足这些条件的 runner，所以产品侧应保持 `capabilities.sideQuery` absent。若后续走 HTTP/WS，需要另设 side request identity、事件流与 cancel RPC；不得复用父会话 `session.prompt`、父会话 journal 或普通 `session.cancel`。

## 6. backend owner 接线清单（本任务未执行）

- **context 现在可接 summary**：构造 `createDshGoContextCapability(this.transport,this.sessionId)` 并放到 `capabilities.context`；每次新 baseline/reset 不用缓存旧 usage。`detail='full'` 等 `context.usage` 上线。
- **MCP 等三方法上线后**：接 `createDshGoMcpCapability(...)` 到 `capabilities.mcp`，再由 owner 更新 capability 声明并做 list/action 错误与真实状态验收。
- **auth 可复用现有目录 API**：每会话创建 `createDshGoAuth(...)`，`model` 读取 backend 的 live currentModel，`emit` 接真实 session emit，`locale` 接 host.locale。将 `auth.capability` 放入 capabilities；在 `questions.respond/cancel` 中先尝试 `respondQuestion/cancelQuestion`，未识别的 requestId 才交原生审批；dispose 时先 `withdrawAll()/dispose()`。native `/logout` 仍需上述 channel/core 接缝。
- **sideQuery 暂不接**：只有拿到满足第 5 节的真实 runner 后才创建 helper、放入 `capabilities.sideQuery`，并在 reset/dispose 时 `close()`。没有 runner 时工厂会 fail-loud。

所有接线完成后仍需真实 dshd 的未配置→登录→下一真实请求→登出、MCP list/reconnect/toggle、recap/btw 父记录零写入、A/B、取消与 ConPTY 验收；本轮 Node mock Transport/runner 测试不冒充这些证据。
