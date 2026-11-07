# W83 附件、引用与持久化独立审查

## 范围与独立性

- owner：ATTACHMENTS-REVIEW / W83。base `af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 审查启动时共享工作树（含 W76/W77/W78/W80）。
- 只读核对当前宿主和 W79 树 `C:/Users/Administrator/AppData/Local/Temp/DSH-GO-attachments`；不写 W79/W82 实现，不改产品、共享契约或 UI，不 commit/push，不启动 GUI。
- 本任务只新增 `scripts/attachments-review.py`、本文与 `tmp/attachments-review-*` 证据。最终 patch 只包含前两份文本；运行证据不进 patch。
- 探针直接驱动新构建的真实 `dshd serve`，经历 ready token→cookie、真实 HTTP、`run` 子进程、v4、Transport journal 和两次进程重启。模型侧是本探针拥有的回环 OpenAI-compatible SSE 接收端，用来检查真实 runner 的请求；它不是 mockd、不是手写 durable JSON，也不冒充真实模型质量验证。
- Windows 二进制与所有临时 home 都在 C: 证据树，避免 D: Medium/MSYS ACL 假象。进程只由既有 `scripts/_proc.py` 按本探针所属 Job/PID 树回收。

## 验收矩阵

“判据”均对应脚本中的真实断言，而不是静态代码目测。基线列只证明无附件实现真红；集成列在 Lead 集成 W79 后填实测结果。

| 验收面 | 独立判据 / 探针前缀 | 无附件基线 | Lead 集成复核 |
|---|---|---|---|
| 真实 id/name/byteSize/mime/hash/dimensions | PNG 3×2、JPEG 4×3、GIF 5×4、WebP 1×1、UTF-8 文本；独立计算 SHA-256，`put/*real-metadata`、`get/*exact-bytes-and-identity`；扩展名与内容故意相反 | `attachment.put` 真实 501，FAIL | **PASS** |
| 有序块与 durable 身份 | text→PNG→text 附件→file ref→session ref→text；`blocks/load-exact-order`、`blocks/history-same-ordered-content`；load/history 的 attachment/reference id 必须相同 | content 被旧宿主忽略，FAIL | **PASS**（模型线顺序另列 FAIL） |
| A/B home | A id 在 B 必须 404；A session 在 B 不可引用；同字节在两个 home 生成不同随机 id | FAIL | **PASS** |
| 授权 | 无 cookie 的 put/get 均 401 四字段；显式绝对路径才授权本次读取；A2 拒绝 A1 旧 cookie | 无 cookie 分支通过；附件功能 FAIL | **PASS** |
| 大小 | 0 bytes、8 MiB+1 拒绝；8 MiB 精确边界接受；prompt 文本合计 1 MiB+1 拒绝 | FAIL | **PASS** |
| 类型 | PNG/JPEG/GIF/WebP/text；未知二进制、非法 UTF-8、含 NUL 文本拒绝；MIME 不信扩展名 | FAIL | **PASS**（完整坏图检测见下行 FAIL） |
| 路径与逃逸 | 相对路径、目录、symlink、get 的 `../`、Windows `..\`、绝对路径 id、未知合法 id；拒绝后无新 object/staging | FAIL（拒绝后无对象分支通过） | 路径拒绝 **PASS**；截断 PNG 意外落盘导致对象不变断言 **FAIL** |
| 坏图 | 完整 CRC 的零宽 PNG 必须拒绝；只有合法 signature/IHDR、没有 IDAT/IEND 的截断 PNG 也必须拒绝 | FAIL | 零宽 **PASS**；截断 PNG **FAIL** |
| 伪造 reference / 元数据 | 客户端伪造 attachment name/bytes/MIME/hash/dimensions/path、referenceId/name/byteSize 不得落盘；标题不是 session id；不存在目标 fail-loud | FAIL | **PASS** |
| 重复请求 | 同文件重复 put 得不同 id；重复 get 字节一致；同一 in-flight prompt 第二次 409 busy，最终恰一条 durable user 和一次模型请求 | FAIL | **PASS** |
| 重启与持久化 | A1→A2 后 get 字节一致，load/history 有序块与 ref id 逐字段一致；旧 cookie 401；读路径不改 v4/blob | FAIL | **PASS** |
| content 上限与顺序 | 空 content、65 块、全空文本、未知 type 拒绝且无副作用；真实模型线必须含真实文本/图片，图片必须位于 BEFORE/AFTER 之间，而非全部文本之后 | FAIL | 上限/实字节 **PASS**；模型图片位置 **FAIL** |
| 旧 text 兼容 | `session.prompt{text}` 仍 accepted/turn-done；load user 不带 additive `content`；模型 wire content 仍是字符串 | 通过 | **PASS** |
| credential 泄漏边界 | 假 credential 只允许出现在 stub 的 `Authorization` 头；不得进入任何 API 响应、模型 body、home 文件、交接文件或宿主日志 | 通过 | **PASS** |
| 本地路径泄漏边界 | attachment put/get/meta/durable 块不回源绝对路径、不存 `path`；只有用户显式 file reference 允许保留/发送已验证绝对路径 | 附件功能 FAIL | **PASS** |
| 持久完整性 | 专用副本 blob 改 1 bit 必须 500 internal；字节不变但 MIME/宽度被改也必须拒绝；变异前备份并恢复 | 无对象可变异，FAIL | blob **PASS**；meta MIME/尺寸 **FAIL** |
| 交接清理 | `state/prompt-blocks` 在轮次完成后无残留；坏 content 在 durable/model 前失败 | 基线旧路径无交接文件；其余 FAIL | **PASS** |

## 先红证据（最终探针，无附件实现）

构建：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
mkdir -p tmp/attachments-review-bin
go build -o tmp/attachments-review-bin/dshd.exe ./cmd/dshd
sha256sum tmp/attachments-review-bin/dshd.exe
```

原样构建哈希输出：

```text
fe2ade6ab71fd1b6edd083c82401ebf0d24a42090dc3591a02febc716d0aa0af *tmp/attachments-review-bin/dshd.exe
```

执行：

```bash
set -o pipefail
PYTHONDONTWRITEBYTECODE=1 python scripts/attachments-review.py \
  --binary tmp/attachments-review-bin/dshd.exe \
  --out tmp/attachments-review-baseline-final 2>&1 | \
  tee tmp/attachments-review-baseline-final.log
```

原样关键输出（完整逐行输出保存在上述 `.log`，不是事后编辑的汇总）：

```text
AR_BEGIN binary_sha256=fe2ade6ab71fd1b6edd083c82401ebf0d24a42090dc3591a02febc716d0aa0af out=C:\Users\Administrator\AppData\Local\Temp\DSH-GO-attachments-review\tmp\attachments-review-baseline-final
AR_FAIL duplicate/put-new-random-id first= second=
AR_FAIL blocks/load-exact-order content=None
AR_FAIL blocks/history-same-ordered-content raw=None
AR_FAIL model/exactly-one-blocks-request requests=0
AR_FAIL restart/exact-bytes
AR_FAIL integrity/tamper-fixture-present missing tamper id
AR_EVIDENCE C:\Users\Administrator\AppData\Local\Temp\DSH-GO-attachments-review\tmp\attachments-review-baseline-final
ATTACHMENTS_REVIEW_SUMMARY OK=33 FAIL=61 SKIP=0
```

退出码 **1**。其中 `AR_OK legacy/...` 证明探针确实走到了真实 runner，而不是因为宿主完全不能启动就制造一堆假红；`AR_OK auth/put-without-cookie` 与 `auth/get-without-cookie` 也分别证明既有认证边界。所有附件正路径与应有失败分支均为真实 501/错误接受，满足“先红”。

## Lead 集成复核（实测 3 个根因 / 4 条 FAIL）

集成来源：`D:/DeepSeek Harness/default-workspace/DSH-GO` 中由 Lead 集成的 W79 宿主；只读构建，产物写入审查树 C: 证据目录。

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build -o /c/Users/Administrator/AppData/Local/Temp/DSH-GO-attachments-review/tmp/attachments-review-integrated-bin/dshd.exe ./cmd/dshd
sha256sum /c/Users/Administrator/AppData/Local/Temp/DSH-GO-attachments-review/tmp/attachments-review-integrated-bin/dshd.exe
```

原样哈希输出：

```text
435dc5ab6585b804643f552e3b06b567e263e8586c31c39a632cf6028da42c49 */c/Users/Administrator/AppData/Local/Temp/DSH-GO-attachments-review/tmp/attachments-review-integrated-bin/dshd.exe
```

复核命令与结果：退出码 **1**，完整原样输出 `tmp/attachments-review-integrated.log`，机器结果 `tmp/attachments-review-integrated/run-result.json`。

```bash
set -o pipefail
PYTHONDONTWRITEBYTECODE=1 python scripts/attachments-review.py \
  --binary tmp/attachments-review-integrated-bin/dshd.exe \
  --out tmp/attachments-review-integrated 2>&1 | \
  tee tmp/attachments-review-integrated.log
```

以下 FAIL 行逐字来自该日志：

```text
AR_FAIL reject/bad-png-missing-image-data status=200 want=400 error=None raw=b'{"result":{"attachment":{"id":"att-2bef9320cf9634d7b98c8d2c5b27000a","name":"bad-truncated.png","byteSize":33,"mime":"image/png","sha256":"a028b6ebdebaa6454c84b50895cbd9a67cd0eead60e89b38afaa295df129f1b2","width":3,"height":2}}}\n'
AR_FAIL reject/no-new-objects-or-staging added=['att-2bef9320cf9634d7b98c8d2c5b27000a'] staging=0
AR_FAIL model/image-preserves-content-block-position before=[0] image=[1] after=[0]
AR_FAIL integrity/mime-dimension-tamper-rejected status=200 want=500 error=None raw=b'{"result":{"attachment":{"id":"att-1c86cac76f0b28a2a8a84bc516b6c109","name":"pixel.png","byteSize":85,"mime":"image/gif","sha256":"6df6fe64d72fd3a8f15662daf4b73fa0e99dbeddf2b1845626b11fa5fe7fea45","width":13,"height":2},"dataBase64":"iVBORw'
AR_EVIDENCE C:\Users\Administrator\AppData\Local\Temp\DSH-GO-attachments-review\tmp\attachments-review-integrated
ATTACHMENTS_REVIEW_SUMMARY OK=151 FAIL=4 SKIP=0
```

### AR-1：只有 IHDR 的截断 PNG 被当成有效图片并持久化

- fixture 只有 33 字节：合法 PNG signature 与 width=3/height=2 的 IHDR，**没有 IDAT/IEND**。真实 `attachment.put` 返回 200、width=3/height=2 并生成对象；第二个 FAIL 是同一根因引起的“拒绝请求不得新增对象”断言，不另算一个实现 bug。
- 当前 `cmd/dshd/attachments.go:199-204` 只调用 `image.DecodeConfig`，它足以证明尺寸头可读，不足以防无图像数据的坏图。契约写的是拒绝坏图，不能只拒绝坏尺寸。
- 对照：同一次运行中完整 CRC 的零宽 PNG 得到真实 400 `bad_request`，说明失败分支不是测试夹具普遍误判。

### AR-2：模型线的图片被移到所有文本之后，丢失有序块位置

- durable load/history 的六块顺序与 reference id 全部通过；问题只发生在当前轮真实 runner 模型请求。
- 实际 wire 只有两个 part：part 0 同时包含 `W83_BEFORE`、附件说明、两个 reference 与 `W83_AFTER`，part 1 才是 PNG。原输入要求 PNG 位于 BEFORE 与文本附件/reference/AFTER 之间，实际模型看到图片时已经在 AFTER 之后。逐字证据 `tmp/attachments-review-integrated/model-blocks-request.json:10-20`。
- 对应实现线索：`cmd/dshd/prompt_content.go:55-96` 聚合整段 text 并另收 images，`internal/llm/types.go:47-54` 固定先写整段 text、再追加全部 image。

### AR-3：持久 meta 的 MIME/尺寸可与真实 blob 矛盾，get 仍返回 200

- 在专用副本中先把 blob 改 1 bit：`integrity/blob-tamper-rejected` 真实 500 `internal`，证明 hash 防篡支路有效。
- 恢复原 blob 后只改 meta：`image/png`→`image/gif`、width 3→13；SHA256/byteSize 仍匹配真实 PNG，但 get 返回伪造的 `mime=image/gif,width=13` 与 PNG base64，状态 200。
- `cmd/dshd/attachments.go:272-284` 校验 id、hash 和 byteSize 后直接信任 meta，未重新检测 MIME/dimensions。客户端伪造字段在 prompt 入口会被重建（已通过），但持久损坏/篡改后的“真实 MIME/dimensions”仍没有保证。
- 变异后已恢复备份，`integrity/restored-original-readable` 通过；未碰任务目录以外任何文件。

**当时结论：集成版不能签收为附件审查全绿。151 项通过不抵消上述 4 条真实 FAIL；本审查不替 W79/W82 修改实现。**

## Lead 最终复验（W84/W89/W90/W91/W98 后，155/0）

上述 3 个根因随后分别由单文件任务修复：W84 复核真实 MIME/尺寸并拒绝空/超限对象，W89 完整校验图片结构与解码，W90/W91/W98 以 `Message.Parts` 保持模型输入块顺序。

Lead 在共享树重新构建 C: 二进制并先通过 `bin-check OK=2 FAIL=0`，实跑：

```bash
python scripts/attachments-review.py \
  --binary C:/Users/Administrator/AppData/Local/Temp/main-parts-wire-shared/dshd.exe \
  --out tmp/attachments-review-w98-shared
```

二进制 SHA256：`f98010000b4c140ca960b6f1071e16ae8aa5aeebca11d268f1005959ea9915e7`。原样关键输出：

```text
AR_OK model/image-preserves-content-block-position before=[0] image=[1] after=[5]
AR_OK duplicate/one-durable-user-one-model-request users=1 model=1
AR_OK integrity/blob-tamper-rejected status=500 want=500
AR_OK integrity/mime-dimension-tamper-rejected status=500 want=500
ATTACHMENTS_REVIEW_SUMMARY OK=155 FAIL=0 SKIP=0
```

同时 `go test ./cmd/dshd -run 'PromptContent|Attachment' -count=1`、`go test ./... -count=1`、`go vet ./...`、`go build ./...` 均 exit 0。**附件宿主与模型保序 scoped 审查现在全绿；可见 GUI、真实模型理解质量与完整工作日仍不在本探针结论内。**

## 判读边界

- 本探针证明宿主真实 Transport/run/v4/journal 行为，不证明 W82 可见 UI、真实模型多模态理解质量或完整工作日。
- 本地 file reference 的绝对路径是用户显式引用语义的一部分；把它发送给当前模型不是 attachment 源路径暗漏。附件本身则只保留受控 id 与 basename。
- blob/meta 变异只发生在本探针专用 `tamper-copy`，运行前写备份，断言后立即恢复并确认可读；不碰任何任务产物以外路径。
- Windows POSIX mode 位不能替代 ACL 审查；本任务不把 `0700/0600` 在 Windows 上的表面值当作 ACL 已完成证明。
