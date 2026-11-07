# 2026-11-07 03:08 定时复核

## 封版状态

- GO.1 仍 NO-GO：最新 full `w163-final-20261106-2302` 因 `sourceDrift=true` 不能作封版证据。
- 已绿：W164 `120/0`、W168 `33/0`×2、W170 80×24 `44/0`、W167 80×24 `75/1`、W175 row-protocol `10/0`。
- 仍红：W170 120×36 `43/1`、W171 80×24 `41/1`、W171 120×36 `38/5`；根因均为行完整性：`alignIdentifiedFrames` 在 `physicalStart<0` 丢前导段、整屏重绘拿到落后转录帧。

## 当前任务

- W199 IN_PROGRESS（owner `97d77a06`）：只修行完整性、120×36 收口，禁 hiding/blank-spacer/删行；目标 W171 双尺寸 `42/0`、W170 120 关闭残留、W167 120 行完整。
- 未 reset、未重启 DSH/magpie、未推远端；GUI 暂停新增任务。

## 封版门

W199 全绿并冻结快照重跑 full，`sourceDrift=false` 且整包全绿，才允许覆盖 GO.1；当前不发布。
