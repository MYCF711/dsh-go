# 2026-11-08 16:08 定时复核

## 结论

T19W 的发布副本曾红，稳定版仍 NO-GO。T19RACE 已完成原子化修复；I17 有完成自述，但 RESULT 尚未同步且 t54 仍 in_progress，暂不记正式完成。T17W2 与 T19W2 尚未解锁。T17W 的 GO_ALLOWED=1 仅是竞态修复前历史证据，不能直接发布。

## 当前状态

| 项 | 状态 |
|---|---|
| T19W | failed；发布副本 full race 首轮抓到 active:true+完成结果，NO-GO |
| T19RACE | completed；patch `1146261051d40a5039d6cd80ffc4507345c464ecb25831a8de41779ed4ade38d` |
| I17 | 自述完成，但 RESULT 未同步、t54 in_progress；需补正式交付后验收 |
| T17W2 | pending；等 I17 正式交付后重跑 Windows 全量 test/race 与真实矩阵 |
| T19W2 | pending；T17W2 GO=1 后新建发布副本重跑全部门 |

## 修复证据

- `finishJob` 在同一 `jobsMu` 临界区完成 close(done)+map 删除；不是先删再完成。
- 确定性 hook 停在 close 后/delete 前，`jobsMu.TryLock()` 必须失败；旧顺序红、修复后绿。
- base 全包 race×100、共享树定向×100和全包×1已有证据。
- 复用 base×100 前须逐字节核对共享树最终文件，不机械重跑约4.2小时。

## 偏差与进程

- 未把第二轮绿用于覆盖 T19W 首轮红；未删旧失败证据。
- GO2-TEAM `.go` scratch=0。
- 16:08 孤儿检查仍只有 2026-11-04 的无关 node，本链无新增测试/心跳孤儿，未清理。
- 未 reset/clean、未危险杀进程、未重启 DSH/magpie、未写 profiles、未自动 push/tag。
- goal 保持 active，本轮未改状态。

## 下一动作

I17 完成后由 QA 跑 T17W2 全量复验；再由 integrator 用 T19W2 新发布副本重跑 build/vet/full test/MinGW full race 与真实矩阵。任何门曾红即 NO-GO，push/发布仍需用户当次明确授权。
