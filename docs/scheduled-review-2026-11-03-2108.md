# 定时方向与派单复核 · 2026-11-03 21:08

> 两个只读子代理并行复核，Lead 同时核对任务表；完成方主动通知，无 wait/poll。未修改产品实现、未启动服务、未杀进程。报告覆盖到宿主 `5937884`、UI `89e42cf`；之后工作树变化仍需下一轮复核。

## 视觉基线与证据边界

- V1、V2、V3、V4A、V4B 均已完成各自视觉验收并登记 DONE；U4P/PERF/RACE 也已完成设计或检测任务。**RACE 是 `bf7e888` 的历史 DONE，不代表 `80a5754` 或宿主 wssession 工作树已复验 race。**
- Lead 复核了 V1 真实 dshd 空会话、V2 两尺寸侧栏、V3 composer/审批、V4A 三栏/resize、V4B 右栏状态和几何证据。
- 多数视觉实现已提交；`docs/visual-v1.md`、`visual-v2.md`、`visual-v3.md`、`visual-v4a.md`、`visual-v4b.md` 与部分补丁/截图仍是未跟踪工作树，不能写成 committed 文档。V2 已全部包含在 `f4440d0`；V4B 为行为 `66ec07d` + 几何/测试 `593f070`，证据文档未提交。
- 最终 `go test ./...` 曾因 tool/call 旧断言失败；并发修改将其明确替换为“call 先显示正在运行、result 补全”的新语义，并给出真实工具轨迹缺失理由。Lead 复跑 `go test ./internal/apiclient`、`go test ./cmd/app`、`go build ./...`、`go vet ./...` 均 exit 0。该变化有实现与新测试，不按删测试变绿处理。随后 Lead 又对 **`80a5754 + 当前视觉工作树`** 执行完整 `go test ./...`、`go build ./...`、`go vet ./...`，均 exit 0；宿主当前工作树执行 `go test ./internal/transport ./cmd/dshd` 与全仓 build/vet，均 exit 0。`89e42cf` 后另复跑 UI `cmd/app`、`apiclient`、`uikernel`、`uiplugins` 与全仓 build/vet，均 exit 0。

## 当前真实缺口

- **U0/H14 仍 IN_PROGRESS**：`9574403` 已补 `session.approve` 和 `counts.total=14`，但 UI 快照仍 `/api/mux`、旧 `kind`、history messages；无共享 golden，History 请求无 journalEpoch，reset 局部 since 未清；`48f036b` 有 409 重建候选实现但无单测/换代真机，不能签闭环；宿主重启 cookie 无自愈。**`80a5754` 已关闭首启 epoch/lastSeq**：订阅前同步 load，真机 since=8、共 4 条与宿主一致。
- **W1/W3/W4/W5/W7 不升级**：FrameSub 重放仍只有工作树 patch；serve 去重/debug、pending cancel、stale_epoch retryable 未见关闭证据。
- **U2 仍 IN_PROGRESS**：`8b3f382` Dispose 红→绿、`89e42cf` 槽排序/过滤通过；授权树、普通禁用/替换、全副作用撤销和公开槽实画仍未闭环。
- **U1-H6/U1-H13 不升级**：进程内 A→B→A、实时 dshd 工具活动/右栏、pending cancel、审批 reset 仍需证据。c60 的 `Stream.Reset` 当前未清 `tools`，必须补第二轮同 Tool+Args 不串旧结果测试。**c60/80a 已有 `tmp/tool-live.png` 实时 tool→轨迹+自动开右栏证据，首启重放重复也已修**；但 reset/History/409、宿主重启、FrameSub、进程内连续流和侧栏真实活动仍未闭环。
- `product-acceptance.md` 的“整进程树性能 ✅”与 PERF 设计中的 G8 未实测冲突，需 UI Lead 改为分项/待验；#20 不应再写“验证中”，应按链路剪断/宿主重启/断线数据分别记录；#24 只证长会话 load/render，不证 1000 条滚动掉帧 ≤1%。

## 宿主任务表调整

- W6：DONE 仅指本次复核，证据为本报告。
- W8/W9：**复用 UI U4P/PERF 设计，不重派**。两份文档当前未提交，状态明确为设计工作树，不冒充实测。
  - W8 验收：`test -f ../dsh-ui/docs/ab-acceptance-plan.md && grep -q T01 ...` exit 0；735 行。
  - W9 验收：`test -f ../dsh-ui/docs/perf-gap-plan.md && grep -q G8 ...` exit 0；308 行。
- W10：维持陈旧二进制误判闭环，不再派宿主 id 修复。
- 新增 W11 READY：在隔离副本执行 bin-check + h-suite fast + race，写 `docs/h-suite-validation-2026-11-03.md`，不修改共享实现。

## 界面任务表调整

- U0 清除已完成的 approve/count14 子项，不重复派快照修复；其余缺口保持 IN_PROGRESS。
- V1/V3/V4B 的最终证据补充到 DONE 行；V2 改为已提交。
- R0：DONE 仅指本次复核，证据为本报告。
- 新增两个无重叠 READY，必须同轮外派并登记实际 owner/base/产出：
  1. **U3A**：官方 48px `searchResultRow` 与真实 workspace/snippet；无 snippet 真值隐藏，两尺寸 ≤8px。
  2. **PERF0**：零改动执行 G1/G6/G7/G8 实测，产出 `scripts/perf-gap.sh` 与 `docs/perf-zero-results.md`，不把设计当结果。

## Lead 行为

本轮两个只读子代理并行并主动通知，无 wait/poll。视觉任务曾有重复派单与并发中间态，现已按唯一 owner/目录验收；未回滚他人改动。`66ec07d` 的滚轮修复记录为 Lead 直接实现，属于流程偏差，后续实现必须外派；Lead 直跑测试属于验收。后续不得因 DONE 行增多而重复派同一目录，也不得让新 READY 长期只有泛称 owner。

## 未验证

完整工作日、10 任务 A/B 实际执行、PERF0 实测、共享 golden、History epoch/reset/409、宿主重启自愈、进程内连续流、pending cancel、stale_epoch 四字段、serve 跨轮去重/debug 统一、U2 完整生命周期、OS 原生输入/高 DPI。
