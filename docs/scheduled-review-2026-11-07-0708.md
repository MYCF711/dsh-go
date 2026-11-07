# 定时方向复核 · 2026-11-07 07:08 Asia/Shanghai

## R1/R2 正式交付

- `ledger/R1-RESULT.md`：R1通过，W175 row由9/1到10/0，frames保持131/0；`alignIdentifiedFrames`按durable id对齐物理前驱。
- `ledger/R2-RESULT.md`：R2通过，16/0；停车行恒为V-1，scrollback只随真实LF推进，物理高度不超过高水位。
- `ledger/PTY-MATRIX.md`：W171 80/120各连续3次42/0，最终注释版再复跑仍42/0；源`4abdb8e3…`、lib`3b5bfe01…`、patch`c71a3065…`。
- 因此GO1-T03-R1R2标DONE scoped，不再反复派渲染写者。

## 剩余红项分流

1. W167 80/120 `resume-original-session-remounted`：真实`/resume`后连续`/api/session.load`失败，`/export`没有新文件，exportSessionId=<missing>。转GO1-W167-HOST，限定宿主session load/runner；`reasoning-visible`按模型时序证据，不伪造。
2. W170 80 `before-round-4-model-overlay-visible`：关/help后高水位不缩，/model绝对浮层落入scrollback。转GO1-W170-OVERLAY，优先ink.tsx关浮层清scrollback重开一代；W170 120当前44/0，修复必须同时保证W171/W175不回退。
3. 未跑full，GO.1仍NO-GO；后续由现有GO1 Lead按READY任务派backend/tui，单PTY串行。

## 其他现场

- `host-patches/` 新增HOST-FS-TILDE、HOST-MODE-ADMISSION、HOST-TOOL-SAFETY及RESULT，属于独立宿主审计交付，未混入R1/R2账本或直接集成。
- GUI仍暂停新增任务；桌面审计只读结论保持，未派新实现。
- 本轮只更新PLAN、LEAD账本、UIPLAN与报告；未改产品、未跑PTY、未重派、未push。

## 同轮并发回填核对

- 现有GO1 Lead已把W170 overlay交owner `962fef10`，W167宿主只读诊断交`d8da5737`，reasoning时序诊断交`6deafa0d`；PLAN已从READY同步为IN_PROGRESS，避免重复派单。
- HOST-TOOL-SAFETY已由现有Lead落共享树，目标包/全仓test/vet/build全绿；HOST-MODE为test-only；HOST-FS-TILDE因覆盖既有`~`→home能力被REJECTED。不得再按“待核包”重复集成。
- UIPLAN当前复核已含上述owner与门禁结论，GO.1仍NO-GO。
