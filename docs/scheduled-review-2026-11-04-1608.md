# 定时方向与派单复核 · 2026-11-04 16:08

> 基线：宿主 `457e432`、UI `d64fe99`。只做只读复核、Lead 验收、任务表/报告与 AGENTS 维护；未杀进程、未启动共享树服务、未提交、未推送。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- `AGENTS.md` 6994 字节、`../dsh-ui/AGENTS.md` 6993 字节；文档链接与 diff-check 通过。

## W13：job_list Schema 导致的 WPS 503 已修复

- 原始 `job_list` Schema 一直完整；坏在默认 compact 投影同时删掉顶层 `type:"object"` 与空 `properties`，实际发出 `{"additionalProperties":false}`。
- 根修：无参/空对象保留 `type:"object"` 与 `properties:{}`；非空对象仍可省顶层 type，压缩收益保留。
- 新增单点 + 全表 57 工具守卫；真实 `dshd tools --tools-mode compact` 输出 `job_list`：
  `{"additionalProperties":false,"properties":{},"type":"object"}`。
- 全仓 test/vet/build 全绿。真实 WPS 200 未重放；运行中的 `DSH GO.exe` 未替换，需要部署流程重建。
- 插件侧另发现 `sanitizeSchema({additionalProperties:false})` 输出 `type:"string"`（falsy 判断），已记录在 `docs/tool-schema-503.md`，未直接改 `node_modules`。

## UI 新证据

- `d64fe99` 新增 `scripts/accept-all.sh`：8 条真机验收统一入口，记录 **8/8 PASS / FAIL=0 / SKIP=0**，约 10 分钟；同轮修掉 4 条脚本“FAIL 仍 exit 0”的假绿。
- `5b1aa71` 补 compact 真压：记录 **14 PASS / 0 FAIL / 0 SKIP**，折叠 4 条、摘要 1108 字、压完 `session.load` 仍 200。
- `21c98db` 已入库 OPEN-IN-APP + REASONING-UI；`5d517e0` 契约快照 v1→v2。
- 上述 `PRODUCT-ACCEPT` / `COMPACT-PROOF` 已登记为 scoped DONE；Lead 本轮未整跑 10 分钟真机，依赖真实模型网关的结论不外推。
- SETTINGS-A 仍在共享工作树未提交，但定向/全仓门禁全绿；不作为新一轮重复实现。

## REAL-CHAIN：在途

- owner `d48be6d4` 的隔离副本 base 仍是 `6bea0bc`，共享已到 `d64fe99`；当前只见 `scripts/real-chain*` 与 testdata 增量，未交付 patch/docs。
- 要求临时 home/port0、只回收自己 PID、真实 UI 连接与发送、F2 设置、历史 reload；无真实触发的 reasoning/tool/审批/cancel 标 SKIP，不得用 mockd/手写帧冒充。
- 最终集成必须对最新树语义合并，不能整包覆盖。

## 方向与风险

- 仍是 Go 宿主 + Viva 原生界面；未退回 Electron/WebView/TypeScript UI。
- Transport 仍按裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history frames；本轮未绕过。
- 共享树只跑定向 test/build/vet；完整门禁在隔离副本；无 checkout/reset、原地移动 modules、按镜像名杀进程。
- `accept-all.sh` 的 8/8 是仓库记录，Lead 未复跑；不得当作真实工作日/桌面整链完成。

## 16:53 追加：REAL-CHAIN 39 PASS 已独立复跑

- 集成 real-chain patch 后，Lead 自己跑 `bash scripts/real-chain.sh`，run `20261104-164728`：**39 PASS / 0 FAIL / 0 SKIP，exit 0**，证据 17 张真窗口截图 + 日志已回写 `testdata/real-chain/evidence/20261104-164728`。
- 覆盖：真 dshd+真 UI 连接、F2 设置、重启后 load、真实 Enter 审批工具、reasoning、真实 Esc 取消。
- 真实链路抓到：实时 `system/message` 被画成助手正文（4894 字系统提示），已派 `16fa3c2a` 在隔离修 `internal/apiclient`，要求 system/developer 帧在 `Chunks()` 返回 nil 且不影响历史原始帧。
- 宿主全仓测试曾在 16:43 因另一会话写 `main.go` 的中间态红过一次（parentwatch 接线）；单独复跑该测试通过，随后全仓 test/vet/build 再次 **exit 0**，不是 schema 修复回归。

## 17:17 追加：MUX-SYSTEM 修复并真机复验

- patch SHA256 `1c0e72f6…`，Lead 复验 5 条定向与全仓 test/vet/build 全绿后集成。
- `Chunks()` 对 system/message、developer/message 返回 nil，原始 Data 不动；未知 kind 只要 role 自称 system/developer 也兜底不产块。
- 修后重跑 real-chain `20261104-171207`：**39 PASS / 0 FAIL / 0 SKIP，exit 0**；新 `p4-00-approval.png` 中 4894 字系统提示已消失，只剩思考摘要、正文与审批卡。
- 未验：session.load 若把 system 放进 messages 的另一条路径；mockd/ACP；developer 真机语料。已写入 `docs/system-message.md`。

## 未验证

REAL-CHAIN 的逐 token 流式/审批 Deny/allow_always/120s 超时/关键几何；`accept-all.sh` 本轮独立复跑；真实 WPS 200；真实 dshd 失败轮次/重连；共享 golden；真机 epoch 换代；宿主重启 cookie；FrameSub 原子性/重放；pending cancel；stale_epoch；U2 生命周期；OS 原生输入/高 DPI；完整工作日/A/B；settings 11 页完整对齐；附件/引用/durable feedback/完整工具树。