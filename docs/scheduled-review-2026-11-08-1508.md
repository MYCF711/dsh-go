# 2026-11-08 15:08 定时复核

## 结论

用户已明确 Windows-only 支持范围。T17W 已正式 completed，`GO_ALLOWED=1`：同一最终树的 Windows 全量门、真实 dshd 矩阵、193/97、fresh-home、沙盒、501、文档与孤儿全部 FAIL=0 UNVERIFIED=0。T19W 进入本地候选准备；未 commit/push/发布。

## Windows 最终门

| 门 | 结果 |
|---|---|
| build / vet | EXIT=0 |
| `go test ./... -count=1` | EXIT=0；63 ok / 24 no test / 0 FAIL；含16个历史 tmp 包 |
| MinGW `go test -race ./... -count=1` | EXIT=0；63 ok / DATA RACE=0 / 0 FAIL |
| kernel-deps | 90行，业务包0 |
| fresh-home serve / direct run | OK=5 / OK=11，均 FAIL=0，不预建 artifacts |
| 沙盒 read-only/danger | OK=13 FAIL=0；拒绝不落盘，危险档真实 env 正确 |
| 通用 501 | OK=3000 FAIL=0，0 RST，四字段完整 |
| 193 方法 | PASS=193 / FAIL=0 / DOC_DRIFT=0 / UNVERIFIED=0，无 override |
| 97 插件 | 同序97/97，FAIL=0 UNVERIFIED=0 |
| doc-links / 本链孤儿 | exit0 / 0 |

被验二进制 sha256 `e4d85f3d27bcaab308f19430e94429ab7687a013c3beb7d2cd7c794b3847c164`，bin-check=0。

## 支持范围与现场

- 用户决定“linux不写支持，写支持win”；非 Windows 为 OUT_OF_SUPPORT，不再作为 UNVERIFIED。
- CI 已恢复与 HEAD 逐字节一致，只有 windows check；Linux 验证分支未推送。
- 旧 T17/T17R 失败与 ENV_BLOCKED 证据保留，不删、不冒充 Windows 最终门。
- 15:08 全局孤儿检查仍只有 2026-11-04 的无关 node，本链无新增测试/心跳孤儿，未清理。
- goal 保持 active，本轮未改状态。

## 下一动作

T17W 已 completed，T19W 解锁：按显式清单准备 Windows-only 发布候选，archive 副本重跑 Windows 全量门，定稿版本/CHANGELOG/哈希/回滚。任何 push/tag/正式发布仍需用户当次明确授权；GUI 还需 TUI2Go 真实可玩验收。
