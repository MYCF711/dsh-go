# 定时方向与派单复核 · 2026-11-04 09:08

> 基线：宿主 `b906a8e`、UI `7483501`。本轮只做只读复核、任务表与报告回写；未修改产品实现、未启动共享树服务、未杀进程、未提交，未操作 goal。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 宿主 `cbc5238`/`70b47be` 补齐 MODEL-WIRE 漏提交文件；`08f607d` 补 ContextWindow 定义。`headcheck.sh`/干净检出规则用于防止“工作树绿、HEAD不编译”，不能只看共享工作树。

## 已收口

- WP-D1：当前窗口搜索、Step Inspect、Turn↔Step、1000条有界布局已集成，最终全仓门禁全绿。
- STATS-WIRE：contextWindow 已贯通 dock，文本百分比、超容量异常、A/B/reset/压缩回落已验；圆环/turn/step/cache/decode仍缺。
- MODEL-WIRE：默认 loopRunner/modelRunner 下一轮覆盖、A/B、清空、重启、损坏store、v4不变已验。
- C-TERMINAL：open/input/output、ANSI、输入框/F6 scoped DONE；resize/重连另验。
- GOALBAR：`c56f5b8`+`efcece8` 已真机可见，scoped DONE。
- QUESTIONS：`144b943` 已支持 F7/F8 和真实点击，scoped DONE。
- DELIVERABLES：`b8f9f8d` 已支持点击文件在右侧浮层看内容；diff/delivery cards/最终回复引用仍缺。

## WP-C3

- owner `f631e4d5` 在最新隔离副本实现现有真值上的工具状态与专用卡。
- 只显示可证明的 preparing/running/ok/error/stopped，未知neutral；交错同参调用按 MessageKey/CallID 独立，不改 dstool Renderer 分派。
- 无 diff、parentCallId、subCalls 真值时不伪造；最终 patch 尚未交付，保持 IN_PROGRESS。

## OPEN-IN-APP：不能算完成

- `b1d4189` 有代码，但 SlotMainHeader 不渲染；`4515ea6` 已撤下按钮，避免每帧占死位。
- `7483501` 找到根因：flex shrink 会从未设 MinHeight 的孩子扣高。
- 新增 OPEN-IN-APP IN_PROGRESS：重新装配后必须实画按钮并真实点击打开；根因找到不等于功能完成，未过前保持未装配。

## 纪律

- 官方外观与 Transport 继续并行；没有退回 Electron/WebView 主壳或 TypeScript UI。
- 新功能继续隔离施工、Lead 串行集成；不得把“本地绿”“提交记录”“代码存在”当真实打开或完整产品完成。
- WP-C 的完整工具树/向前历史/附件反馈、WP-D 的完整历史、WP-E 的持久反馈仍按契约阻塞。

## 09:30 追加：WP-C3 集成与 FILE-REFS

- WP-C3 patch 仅13个允许文件，Lead 复验两包/全仓 test/vet/build与两尺寸实图；集成后共享树再次全绿，转 DONE。工具真值状态、交错CallID、edit真实old/new均保留，完整工具树仍后续。
- FILE-REFS owner `c55e44d7` 已在 `aeadda9` 隔离副本开工：只把 write/edit 已证明的路径变成最终回复可点链接，Cwd缺失/未知路径/代码块不猜，复用真实OpenFile。
- OPEN-IN-APP 的根因记录按 `98c9645` 纠正为 CWD 未等到，不是 SlotMainHeader 塌陷；仍需重新装配和真实点击验收。

## 未验证

WP-C3 成品、OPEN-IN-APP 重装与真实点击、真实 dshd/桌面整链、共享 golden、真机 epoch 换代、宿主重启 cookie、FrameSub 原子性/重放、pending cancel、stale_epoch、U2 完整生命周期、OS 原生输入/高 DPI、完整工作日/A/B。
