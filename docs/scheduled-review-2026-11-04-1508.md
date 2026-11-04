# 定时方向与派单复核 · 2026-11-04 15:08

> 基线：宿主 `93160a4`、UI `108ee62`。只做只读复核、Lead 验收、任务表/报告与 AGENTS 维护；未改产品实现、未杀进程、未启动共享树服务、未提交、未推送。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- `AGENTS.md` 6809 字节、`../dsh-ui/AGENTS.md` 6993 字节；文档链接与 diff-check 通过。

## 已验收

### TURN-END · `108ee62`

失败轮次不再显示成“跑完了”。Lead 复跑 5 条：

1. `error` 不得记成 done；
2. `aborted` 映射 stopped；
3. `completed` / `max_tokens` 才是 done；
4. `blocked` 映射 error；
5. 同一结局只记录一次。

全仓 test/vet 通过；真实 dshd 失败轮次截图与重连仍另验。

### SESSION-ACTIONS

- `ce44c8f` session.delete、`31aecd9` session.compact、`82436e8` session.fork；
- `58e99e4` 真宿主 delete/fork/compact 验收 **12 PASS**，空会话分支文案已改成用户能懂的话；
- fork apiclient 成功/错误 5 类测试通过。

仍不冒充完整 WP-E：指定 seq 分支、长期会话未验。

### OPEN-IN-APP 与 REASONING-UI

- OPEN-IN-APP：标题行 56×24 真实可见、不挤页签；只有绝对 CWD + OpenFile 才画；点击走真实 file 链接，目录由 glue 交平台文件管理器，不再交 OpenExternal。
- REASONING-UI：最近完成段落预览、signature 仅 preview 清理、真实时长；未知/Busy 隐藏。
- 两者均已集成并全仓复验通过。

## SETTINGS-MINIMAL：浮层完成，A入口未交付

- 前 owner 已交付四块真值设置浮层、两尺寸和真进程证据、全仓门禁；入口落在 Shift+F2，未满足 A。
- Lead 裁定 A 后，continuation `d2ebdd2f` 正在同一隔离成品上补 `dscommands` 的最小「设置」行和“有/无服务”测试。
- 当前隔离工作树已见 commands/settings 测试改动，但最终 patch 尚未交付；不签 DONE。

## 风险与方向

- 宿主 `fbc4ac1`/`3c9e457` 做静默失败/“只看 text”收口，并新增 text-only 门禁；这是宿主侧质量线，尚未作为产品跨仓验收。
- 仍是 Go 宿主 + Viva 原生界面；未退回 Electron/WebView/TypeScript UI。
- Transport 未绕过：HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、history frames 保持。
- 共享树只跑定向 test/build/vet；完整门禁在隔离副本；无 checkout/reset、原地移动 modules、按镜像名杀进程。

## 15:30 追加：SETTINGS-A 集成 DONE，REAL-CHAIN 实派

- SETTINGS-A patch 11允许文件，Lead 复验F2末行可执行、无服务18条逐字不变、4种坏值不panic；定向/全仓 test/vet/build与集成后全绿，转 DONE。浮层四块仅消费真值，Shift+F2保留备用。
- 当前任务表已清空可交付项；新 READY REAL-CHAIN（`d48be6d4`）在 `6bea0bc` 隔离副本做真 dshd+真 UI 进程验收：临时home/port0、只回收自己PID、真实UI连接与发送、F2设置、历史reload；reasoning/tool/审批/cancel无触发则SKIP并写原因，不用mockd/手写帧冒充。
- 该包只写 scripts/docs/testdata，不改生产代码；完成前不把“测试全绿”当真实桌面整链完成。

## 未验证

SETTINGS-A 最终入口与成品；真宿主 dshd 端到端；真实桌面目录打开/OS 鼠标；共享 golden；真机 epoch 换代；宿主重启 cookie；FrameSub 原子性/重放；pending cancel；stale_epoch；U2 生命周期；OS 原生输入/高 DPI；完整工作日/A/B；settings 11 页完整对齐；附件/引用/durable feedback/完整工具树。