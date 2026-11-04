# 定时方向与派单复核 · 2026-11-04 13:08（恢复后实际执行）

> 基线：宿主 `cd99aaa`、UI `b396f3a`。本轮在长时间暂停后恢复，实际执行时间晚于计划；不宣称 10:08–12:08 的提醒已运行。只做只读复核、隔离副本重建、任务表/报告与 AGENTS 维护；未改产品实现、未杀进程、未启动共享树服务、未提交、未推送。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- `AGENTS.md` 再次被新增提交事故规则推到 7976 字节；已保留全部规则并压缩到 **6999 字节**（上限 7000）。文档链接检查通过。

## 两仓进展

- 宿主：`0c78b13`/`f8bffc6` 思考块进入 ACP load 与 meter 估算；`d676189` fork 保留 finish/tool-result/system/developer 事实；`cd99aaa` 补顶层 finish 兜底测试。
- UI：运行时主题切换、会话删除、F2 压缩上下文、reasoning-only 回答、插件零测试包补齐；`b396f3a` 接上 `session.load.reasoning`。
- 已集成：MODEL-WIRE、WP-C1/C2/C3、WP-D1、STATS-WIRE、C-TERMINAL、FILE-REFS、GOALBAR、QUESTIONS、deliverables 打开浮层。
- 仍缺：完整工具树、真实向前分页/loadOlder、附件/引用、durable feedback、per-file diff/delivery cards、settings 等契约或后续项。

## OPEN-IN-APP 前 owner 失败

- `4dcbbab2` failed before it finished，且没有留下 dsopen/cmd.app 增量或 patch；不可把“已实派”当完成。
- 已从最新共享导出重建隔离副本 `dsh-ui-open-in-app2-isolated`，base `b396f3a`，全仓 test/vet exit 0。
- continuation `4954d975` 已实派，要求：两尺寸非 0 高真实可见、CWD 缺失/相对隐藏、点击走真实 OpenFile/app.OpenPath 打开目录、不能用 OpenExternal 处理裸 Windows 路径；保留 FG-3/FILE-REFS/WP-C1/C2/C3。

## 方向检查

- 仍是 Go 宿主 + Viva 原生界面；没有退回 Electron/WebView 主壳或 TypeScript UI。
- Transport 契约未被本轮绕过；新功能继续隔离施工、Lead 串行集成。
- 共享树只跑定向测试与 build/vet；完整门禁在隔离副本。
- 没有 checkout/reset 覆盖他人、原地移动 modules、按镜像名杀进程或操作 goal 的定时轮次。

## 同轮外派：REASONING-UI

- 从 `b396f3a` 建独立隔离副本（全仓 test/vet exit 0），owner `1e7305b7`。
- 只按真实数据改进思考块：preview取最近完成段落、仅在preview清`<signature>`、时长仅用真实Time/TurnEvent，未知隐藏。
- 与 OPEN-IN-APP 写路径不重叠（前者 dsconversation，后者 dsopen/cmd.app），同轮并行；未验收前不签DONE。

## 未验证

OPEN-IN-APP 续修成品、APK/桌面真实打开、真实 dshd+UI 整链、共享 golden、真机 epoch 换代、宿主重启 cookie、FrameSub 原子性/重放、pending cancel、stale_epoch、U2 生命周期、OS 原生输入/高 DPI、完整工作日/A/B、settings/附件/反馈等未开工契约项。