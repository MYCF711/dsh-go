# 2026-11-08 19:08 定时复核

## 结论

宿主稳定候选与 TUI2Go 真实可玩 **双门均已 GO_ALLOWED=1**。T20 规划已由 Lead 从 BLOCKED 改为 READY 并签收，GUI-P0 与 GUI-P1 已同轮派出。未 commit/push/tag/发布。

## G1 宿主

- T19W3 completed，GO_ALLOWED=1。
- 发布副本 build/vet/full test 全绿。
- `CGO_ENABLED=1 go test -race -p=1 ./... -count=1`：63 ok / 8 no test / FAIL=0 / DATA RACE=0。
- 插件竞态修复与193/97/fresh-home/沙盒/501/doc-links全绿。
- 版本 `0.2.2-go.1`，Windows-only；未 commit/push/tag。

## G2 TUI2Go

- TUI2GO-PLAY completed，GO_ALLOWED=1。
- 宿主 `DSH GO 0.2.2-go.1`，sha `db2acbd30c979bd5d326820c7f5f16da2ccf16e97176b0a9d86f88eda7715f6e`，bin-check=0。
- TUI 0.19 + public/dsh-tui2go 0.1.0 local link，空临时home、独立端口。
- 真实 ConPTY 打字/编辑；模型两回合 `Paris`/`42`，有 reasoning，tool_calls=0未假造。
- `/kernel` 可切换；`/quit` exit0；无 dshd/dsh-tui 孤儿。
- 上游 workspace:* 问题通过本地 bundle link 规避，不宣称 npm 开箱即用。

## GUI 启动

- `../dsh-ui/docs/go2-gui-restart-dag.md` 已更新为双绿签收，旧 Linux/dump-only 门仅作历史。
- GUI-P0（evidence）：冻结结构树/素材复核，只读主库，不重拆 asar。
- GUI-P1（workspace）：Transport/APIClient 真实消费面与必要修复。
- P0/P1 并行；P2 等双验收，P3/P4 等 P2，P5 最后。

## 现场

- 未重跑已完成的宿主门或 TUI 交互；只核 RESULT/帧/日志并签收。
- 未 reset/clean、未重启 DSH/magpie、未写 profiles、未自动 push。
- goal 保持 active，本轮未改状态。
