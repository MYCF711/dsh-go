# 2026-11-07 00:08 定时复核

## 封版状态

- 最新 full `w163-final-20261106-2302` 仍为 NO-GO：`sourceDrift=true`，不能作为封版证据。
- 已绿：W152_COVERAGE `769/0`、AgentSession `29/0`；W164 `120/0`、W168 `33/0`×2、W171 80×24 `42/0`。
- 仍红：W171 120×36 `41/1` 与 `40/2` 两次，均为排队面板打开瞬间的转录行时序；W167 model-picker/inline；W170 `/model` 顶部裁切。

## 当前任务

- W194 DONE：translator 用 durable ordinal 修 `(turn,step)` 覆盖，W164/W168 绿。
- W196 DONE scoped：JobCard 唯一呈现、排队增高保留转录；80×24 GO，120×36 仍红。
- W197 IN_PROGRESS：`/model` 面板裁切探针已到 `12/0`，继续 W167/W170 定向复验与基线断言纠正。

## 边界

- 未 reset、未重启 DSH/magpie、未推远端；GUI 仍暂停新增任务。
- GO.1 只能在 W167/W170/W171 全部收口后，用冻结快照重跑 full；当前不覆盖稳定版。
