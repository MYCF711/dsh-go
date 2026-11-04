# 定时方向与派单复核 · 2026-11-04 14:08

> 基线：宿主 `cd99aaa`、UI `4c0936f`。只做只读复核、Lead 验收、任务表/报告与 AGENTS 维护；未改产品实现、未杀进程、未启动共享树服务、未提交、未推送。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- `AGENTS.md` 6999 字节、`../dsh-ui/AGENTS.md` 6993 字节；均在上限内，文档链接检查通过。

## APPROVAL-TIMEOUT：DONE scoped

`4c0936f` 接上 `approval/timeout` 与 `approval/cancel`，避免超时后界面留下僵尸审批条。

Lead 独立复跑 5 条定向，全部 PASS：

1. 超时清掉前台与会话内同一条审批，并给状态提示；
2. 迟到的旧 requestId 不误清下一个未答请求；
3. 载荷解不出 requestId 时仍清，避免永远点不动；
4. cancel 与 timeout 同语义；
5. 别的会话超时不影响本会话。

这只是一层 UI 消费。宿主 `cancel → 拒绝 + 工具不执行 + 第二轮 prompt` 的 pending cancel 整链仍归 W4，不因此签 H13 完成。

## 在途 READY

- **OPEN-IN-APP · `4954d975`**：最新隔离 base `b396f3a`，目前只有 scratch test，无最终实现/patch。要求两尺寸真实可见、绝对 CWD、OpenFile/app.OpenPath 打开目录，不能用 OpenExternal 处理裸 Windows 路径。
- **REASONING-UI · `1e7305b7`**：最新隔离 base `b396f3a`，目前在实现思考摘要/真实时长；未交付 patch。要求只用真实 Time/TurnEvent，未知隐藏。
- 两者均基于 `b396f3a`，最新 UI 已到 `4c0936f`（审批超时改 `cmd/app/main.go`），最终集成必须先对最新树语义合并，不得整包覆盖。

## 方向与一致性

- 仍是 Go 宿主 + Viva 原生界面；没有退回 Electron/WebView 主壳、TypeScript UI 或 8 包上限停工。
- Transport 契约未被绕过；HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、history frames 仍按契约。
- 共享树只跑定向 test/build/vet，完整门禁在隔离副本；没有 checkout/reset、原地移动 modules、按镜像名杀进程。
- 新功能继续隔离施工、Lead 串行集成；在途任务不清理、不重复派同一路径。

## 14:30 追加：REASONING-UI 集成 DONE

- patch 只含 `plugins/dsconversation/**` 与 `docs/reasoning-ui.md`；Lead 复验定向/全仓 test/vet/build 与两尺寸展开均 exit 0，集成后共享树再次全绿。
- 预览取最近完成段落、signature只在preview清理、展开保留原文；时长仅用真实Time/TurnEvent，未知/Busy隐藏，无`time.Now()`。
- reasoning-only、MDSTREAM、WP-C1/C2/C3、FILE-REFS、审批原ID均保留。实时消息时间缺失导致真机“想了N秒”多只在重载后出现，列为未验。

## 14:40 追加：OPEN-IN-APP 集成与 SETTINGS-MINIMAL

- OPEN-IN-APP patch 25允许文件，Lead 复验全仓 test/vet/build与两尺寸三轮真实点击，集成后再次全绿，转 DONE。按钮56×24在标题行不挤页签，点击走OpenFile(file:///)；目录由cmd/app glue交平台文件管理器，绝不再交OpenExternal。`LastOpenedPath`无法记录目录的原因与三条替代真值已列入文档。
- 登记已落地但缺表的 SESSION-ACTIONS（delete/compact/fork）、THEME-RUNTIME、REFERENCE-MENTION，均注明真实范围与未验项。
- 新 READY SETTINGS-MINIMAL（`8486b7ed`）在 `9290bed` 隔离副本施工：只暴露现有主题/模型/权限Action，缺Action整行隐藏，当前态未知不伪造。全仓基线 test/vet已通过。

## 14:45 设置入口裁定：采用 A

- `SETTINGS-MINIMAL` 的 F2「设置」只能由 `dscommands` 命令面板生成；pluginapi 冻结时不加新 Action。
- 裁定 A：允许 `dscommands/commands.go` 最多追加一条最小入口，只在设置服务真实可用时出现；缺失时零新增/零 panic，其他命令顺序逐字不动，并补“有/无设置服务”测试。
- 不接受 B 的 Shift+F2 偏差作为最终交付；若执行者按 B 落盘，Lead 验收时必须补 A 入口后整体复验。

## 15:05 设置进展：浮层已完成，A 入口续修

- 前 owner 交付了四块真实设置浮层（主题/模型/权限/会话）、两尺寸与真进程证据、全仓门禁，但入口落在 Shift+F2，未满足 Lead 已裁定的 A。
- 不签 DONE。已派 continuation `d2ebdd2f` 在同一隔离成品上只补 `plugins/dscommands/commands.go` 的最小「设置」行及有/无服务测试，保留 Shift+F2 为备用。
- 完成标准：F2 命令面板出现真实可执行「设置」；服务缺失时其他命令逐字不变；全仓 test/vet/build 与两尺寸/真进程 F2 证据通过。

## 未验证

OPEN-IN-APP、REASONING-UI 成品；真实桌面目录打开；真实 dshd+UI 整链；共享 golden；真机 epoch 换代；宿主重启 cookie；FrameSub 原子性/重放；pending cancel；stale_epoch；U2 生命周期；OS 原生输入/高 DPI；完整工作日/A/B；settings/附件/durable feedback 等契约项。