# 本地附件、内容块与引用（W79）

## 契约

- `POST /api/attachment.put`，裸参数 `{"path":"C:/explicit/file.png"}`。用户显式给出的本地绝对路径就是**本次读取授权**；没有后台扫描、目录遍历或按标题猜文件。
- `POST /api/attachment.get`，参数 `{"id":"att-…"}`（也收 `attachmentId`）。返回 `attachment` 与 `dataBase64`。
- `attachment`：`id/name/byteSize/mime/sha256/width/height`。宽高仅图片非零；`byteSize` 是真实字节数，SHA256 对真实字节计算，`att-` 后是 128-bit 随机数的 hex。
- 单文件上限 **8 MiB**。只接受非空常规文件；拒绝相对路径、目录、符号链接、0 bytes、超限、非 UTF-8/含 NUL 的文本、未知二进制和坏图。
- 类型由字节检测，不由扩展名或标题推断。支持 PNG/JPEG/GIF/WebP 与 UTF-8 `text/plain`。PNG/JPEG/GIF 用解码器读尺寸；WebP 解析 RIFF 的 VP8/VP8L/VP8X 真实画布尺寸。
- 对象只写 `<home>/state/attachments/objects/<id>/{blob,meta.json}`，staging 位于同一 home；目录 0700、文件 0600。不同 home 无共享索引，A id 在 B home 不可读。
- 未接实现为 501；未认证 401；参数/坏图/超限时 `bad_request`，不存在时 `not_found`，存储完整性/写入失败时 `internal`。所有错误仍是 `{error:{code,message,hint,retryable}}` 四字段。

## 有序 prompt 与 durable 身份

`session.prompt` 新增可选 `content`，但旧 `{"text":"…"}` 请求和回调完全不变：

```json
{
  "sessionId": "session-a",
  "content": [
    {"type":"text","text":"before"},
    {"type":"attachment","attachmentId":"att-…"},
    {"type":"reference","referenceType":"file","path":"C:/explicit/file.png"},
    {"type":"reference","referenceType":"session","sessionId":"session-target"},
    {"type":"text","text":"after"}
  ]
}
```

- 1–64 块，文本合计不超过 1 MiB。附件块只信 `attachmentId`；宿主从 store 重读并校验 SHA256 后补齐元数据，客户端伪造的 name/bytes/dimensions 不会落盘。
- file reference 重新验证真实常规文件；session reference 用 `session.FindByID` 验证当前 home 的真实对象。`ref-` 后也是 128-bit 随机数；标题不是 id。
- 父进程把已解析块写入当前 home 的受控交接文件，同一个 dshd 的 `run` 子进程再次校验后追加 `user/message`。v4 `content` 数组原序保存完整块；append-only，读路径不写盘。
- `Message.content` 是 load 的 additive 字段。只有含 attachment/reference 的 durable user 才输出它，旧纯文本消息继续省略；`Message.text` 仍只是 text 块投影，不放附件名。
- WS/history 使用同一原始帧，因此与 load、进程重开恢复相同 attachment/reference id。
- 当前轮模型输入由真实块派生：文本附件附真实文本，图片附真实 base64 图像，引用附已验证身份。附件二进制不塞进 v4，避免把日志变成第二份 blob store。

## 验证

- `internal/transport/attachments_test.go`：真实 HTTP/cookie、put/get、块序与旧 prompt 回调。
- `cmd/dshd/attachments_test.go`：四种图片真实尺寸、文本、SHA/bytes、A/B、路径/坏图/超限、引用和 v4→load。
- `internal/transport/testdata/attachments.{snapshot,golden}.json` 与 UI 对应文件逐字节一致；canonical 方法表仍为 25，原共享 WS golden 不变。`Message` 的 canonical 快照只追加 `content`。
- UI `internal/apiclient/attachments_real_test.go` 以新构建 dshd、临时 home、ready/token 跑真实 put→prompt→load/history→get、异常、A/B 与重开；不用 mockd。运行方式：

```bash
DSHD_W79_BIN='C:/path/to/fresh/dshd.exe' go test ./internal/apiclient \
  -run '^TestRealDshdAttachmentsReferencesLifecycle$' -count=1 -v
```

真实模型只在最终整链显式 opt-in：`DSHD_W79_REAL_MODEL=1`，测试名为 `TestRealModelAttachmentChain`。
