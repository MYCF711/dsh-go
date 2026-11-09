# 2026-11-08 18:08 定时复核

## 结论

T19W3 正在执行正式发布副本验收。build/vet/full test 已绿，`go test -race -p=1 ./...` 仍在运行；GO_ALLOWED 尚未生成，稳定版未放行、未发布。

## 当前证据

| 项 | 状态 |
|---|---|
| T17W2 | completed，GO_ALLOWED=1；插件竞态修复后的 Windows 全量复验通过 |
| T19W2 | failed；默认16路 race 出现1s截止窗负载 flake，NO-GO |
| T19W3 build / vet / full test | EXIT=0 |
| T19W3 full race | `CGO_ENABLED=1 go test -race -p=1 ./... -count=1` 在途；当前已见 cmd/dshd 433.770s 通过，尚无最终汇总 |
| GO_ALLOWED | 尚未生成 |

## Lead 裁定的门

- 不接受默认并行 race 首轮红、第二轮绿就放行。
- 不提高 ConPTY/bash/tools 的截止窗，不删旧红。
- 正式门改包级串行 `-p=1`，保留原断言和1s超时。
- 串行仍红即产品问题，不能写成环境通过。

## 现场

- T19W3 日志持续更新，不是停摆。
- 18:08 全局孤儿检查仍只有 2026-11-04 的无关 node，本链无新增测试/心跳孤儿，未清理。
- 未 reset/clean、未重启 DSH/magpie、未写 profiles、未自动 commit/push/tag。
- goal 保持 active，本轮未改状态。

## 下一动作

等 T19W3 串行 race 与真实矩阵最终结果。全绿才形成 Windows-only 本地候选；任何红项继续修，不进入发布。push/正式发布仍需用户当次明确授权。

## 18:29 后续

T19W3 已正式 completed，`GO_ALLOWED=1`。发布副本 build/vet/full test/`-p=1` full race 全绿，0.2.2-go.1 Windows-only 本地候选形成。未 commit/push/tag。证据：[T19W3 RESULT](../tmp/GO2-TEAM/T19W3/RESULT.md)。
