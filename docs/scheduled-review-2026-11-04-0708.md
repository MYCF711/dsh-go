# 定时方向与派单复核 · 2026-11-04 07:08

> 基线：宿主 `13a83e3`、UI `b43dcdc`。本轮只做只读复核、Lead 集成验收、任务表与报告回写；未修改产品实现、未启动共享树服务、未杀进程、未提交，未操作 goal。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 宿主已到 `v0.2.1-go.28`，包含 wasm 工具、审批倒计时、permission 等；版本发布不代替 UI/跨仓产品验收。

## WP-C2 已集成 DONE

- 原交付、rebase、final integration 均通过独立验收；最终 patch SHA256 `65a69ef6…`，37 个允许文件。
- `main.go` 最终只保留最新 jobs 差异，permission/schedule/deliverables、A/B/FG、API-TIME 均未回退。
- Lead 复验隔离全仓 test/vet/build、WP-C1/WP-C2/Event.Time 三轮均 exit 0；集成后共享树再次全绿。
- 已落地：已加载 Turn shell/footer/导航、后台 canonical Stream 与温切0 HTTP/0 cancel、Outgoing精确对账、审批原ID、回看/回底、loadOlder缺真值、live/history统一Event.Time。
- 真实 dshd/桌面整链、rpcId、loadOlder/loadThrough 仍未验，不扩大 DONE 范围。

## 新一批同轮外派

| 包 | owner | 范围与验收 |
|---|---|---|
| **WP-D1** | `d82f6584` | 当前窗口搜索、Step展开/Inspect、Turn↔Step联动、1000条可见行；两尺寸真实测试。完整历史/工具树留 WP-D |
| **STATS-WIRE** | `1faeb455` | 把宿主 contextWindow 贯通 apiclient/pluginapi/cmd/dsstatusbar；只有真值显示百分比/环，未知不钳100%，A/B/reset/压缩回落 |

两个最新隔离 base `b43dcdc` 的全仓 test/vet 均 exit 0，写路径不重叠。

## PERMISSION-COMMANDS / C-JOBS

- PERMISSION-COMMANDS scoped DONE：宿主 `ec053e2`/`a3f63f6` 补 setPermission 与 LoadResult.permission；UI `b43dcdc` 五条命令真实接线，PR #3 已合并。
- C-JOBS 补充 `c2aadc7`/`b60dfe6`：侧栏“后台任务 / sleep”与真机截图已有证据；jobs.output 实时架构、长期重连仍另验。

## MODEL-WIRE 仍未闭环

- `648bdea` 有 setModel/store，但此前未见 `modelOverrides.Get` 消费；`89a7ae2` 又记录宿主无模型注册表。
- 在下一轮 request/context 真正覆盖、catalog、清空回默认与失败回滚证实前，Composer 不做假模型下拉。

## 纪律

- 继续按消费链验收，不能只查方法名、快照或“本地绿”。
- 下一批继续隔离 worktree；共享树由 Lead 串行集成。当前没有退回 Electron/WebView 主壳或 TypeScript UI。
- WP-C 的完整工具树/向前历史/附件反馈、WP-D 的完整历史、WP-E 的持久反馈仍按契约阻塞，不由局部 UI 提前关闭。

## 07:40 追加：STATS-WIRE 成品与 MODEL-CATALOG

- STATS-WIRE 原成品由 Lead 复验相关/全仓 test/vet/build、容量异常、A/B/reset 和真实实画，全部通过；但代理声称的 `stats-wire-final.patch` 实际不存在，未直接集成。
- 已在含最新模型选择的 `59fa701` 共享导出建立全绿 final 副本，owner `ab8c56c4` 负责语义合并并生成真实可校验 patch。通过前 STATS-WIRE 仍 IN_PROGRESS。
- `25b374b` 提供 models.list，UI `0747a0a` 列出网关真实模型；`dscommands/cmd/app` 测试通过。**目录已通，但 `modelOverrides.Get` 仍无消费点，选择后下一轮未证实生效**。MODEL-WIRE 保持 P0 IN_PROGRESS，不把模型入口当切换完成。

## 07:50 追加：MODEL-WIRE 实派与 jobs.output

- 源码复核确认 `modelOverrides.Get` 仍无消费，默认 loopRunner 继续固定 --model。已在宿主 `1206b2c` 建立全绿隔离副本，P0 owner `ff844228` 负责把覆盖接到下一轮；A/B、清空、重启、v4不变与失败回滚均有验收要求。
- UI models.list/命令面板复用，不重做。完成前仍不签模型切换完成。
- 宿主 `92432b9` 已打通 jobs.output 父进程问/子进程答；C-JOBS 记录已更新，UI output 实时展示与长期重连仍需单独验收。

## 未验证

WP-D1/STATS-WIRE 成品、MODEL-WIRE、真实 dshd/桌面整链、jobs.output 实时架构、共享 golden、真机 epoch 换代、宿主重启 cookie、FrameSub 原子性/重放、pending cancel、stale_epoch、U2 完整生命周期、OS 原生输入/高 DPI、完整工作日/A/B。
