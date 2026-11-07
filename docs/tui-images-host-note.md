# dsh-TUI 图片链路：宿主能力说明

本说明只记录 dsh-TUI P1-C additive helpers 复核到的 DSH-GO 宿主能力边界；本轮不修改宿主实现。

## 已有且可直接复用

- Transport 提供单数方法 `attachment.put{path}` 与 `attachment.get{id}`（get 也接受 `attachmentId`）。契约见 `internal/transport/api.go` 的 `MethodAttachmentPut/MethodAttachmentGet`、`AttachmentStore`，HTTP 分派见 `internal/transport/server.go`。
- put 只接受用户显式给出的绝对路径，由宿主读取并校验真实字节，返回随机 `att-…` id、MIME、SHA256、byteSize 与图片尺寸。支持 PNG/JPEG/GIF/WebP 和 UTF-8 text/plain，单附件上限 8 MiB，图片总像素上限 64 × 1024 × 1024 像素（67,108,864）（`64 << 20`）。
- get 返回 `{attachment,dataBase64}`，并复核对象身份、真实长度、SHA256、MIME 与尺寸。对象位于当前 home 的 `state/attachments/objects/<id>/`，同一 home 重开后仍可按 id 读取；不同 home 不共享对象。
- `session.prompt` 已接受有序 `content`：`text`、`attachment`、`reference`。附件块线上只需 `attachmentId`，宿主重新读取 store 并补齐元数据，客户端不能把路径字符串或伪造元数据当图片。
- 默认 `loopRunner.RunContent` 经 `loadPromptPartsForRun` 逐块生成模型 parts，因此 `BEFORE/text → image attachment → AFTER/text` 在当次模型请求中保持原序；v4 也持久化同一组有序 attachment blocks，供 load/history 与 UI 重开后的 lazy ImageRef 使用。

## 当前缺失或需区分的宿主能力

1. **没有 `attachments.list/put/get/remove` 这组复数 API，也没有 `attachment.list/remove`。**
   - 当前只能 put/get；不能枚举宿主对象，也不能通过 Transport 删除对象。
   - 因而 prompt 失败后的孤儿对象回收、保留策略与列表界面仍是宿主后续项；TUI 不会伪造 list/remove 成功。

2. **put 没有 bytes/base64 入口。**
   - 通用 channel 声明 `SessionCapabilities.images` 后提供的是内存 `ImageRef` 和 `local-*` staging id，而宿主 put 只收绝对路径。
   - TUI additive helper 的适配必须是 `ImageRef.read()` → 权限受控的临时文件 → `attachment.put{path}` → finally 清理临时文件 → 使用宿主返回的真实 `att-…` id。`local-*` id、源路径和 data URL 字符串都不能直接作为 durable attachment block 发送。

3. **有序 content 的 busy steer 已由 W138 接入生产。**
   - `promptService.SubmitBlocksPlacement` 要求 busy steer 使用独立 `SteerContent`；W138 已在 modelRunner、默认/routed loopRunner 与 run 子进程 IPC 实现生产 `SteerContent`，按当前 turn 下一 step 保序注入。
   - 普通 turn、busy steer，以及最终经 `RunContent` 执行的 followup/中断后新轮，都可复用现有有序 parts；仍不得把传输或 runner 的真实 unsupported 改报为已接收。

4. **UI durable replay 不等于模型历史图片回放。**
   - v4 attachment blocks 与 attachment store 足以让 UI 在同一 home 重开后重新 get 图片字节。
   - 但续会话的 `session.History` 当前在 `internal/session/history.go` 的 `userMessage/joinText` 中只投影 text 块；后续轮次的模型历史不会自动重放旧图片。
   - 若产品要求后续轮次继续看到旧图，需要宿主另做模型历史投影，并保持块序、附件完整性与像素/字节预算；不能由 TUI 的 ImageRef facade 冒充该能力。

5. **ImageLimitsView 的部分数值是适配层推导，不是独立宿主 RPC。**
   - 宿主可证实：4 种 raster MIME、单图 8 MiB、单图 64 × 1024 × 1024 像素（67,108,864）、prompt 总块数 1..64、文本合计 1 MiB。
   - 宿主没有独立的 `images.limits` RPC，也没有独立的图片数量、消息图片总字节或单边像素字段。TUI 为满足 `ImageLimitsView` 使用的 64 张、64×8 MiB、单边 64 × 1024 × 1024 像素（67,108,864）是由上述硬上限推导的保守上界；提交仍须另外保证 text/image/reference 的总块数不超过 64。
   - 宿主重开会使旧 cookie 失效；durable attachmentId 持久不代表旧 Transport 授权可复用，重连后必须重新认证。

## 本轮适配交付边界

TUI 只新增 `images.ts`、`attachments.ts` 及测试和 `src/dsh-go/` 镜像；不修改 DSH-GO。helper 单测证明 RPC 参数、内容块转换、lazy durable facade、重开与 A/B 命名空间行为，不冒充真实模型或可见 GUI 验收。backend capability/submit/translator 接线由 backend owner 另行完成。
