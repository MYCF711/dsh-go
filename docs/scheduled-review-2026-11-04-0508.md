# 定时方向与派单复核 · 2026-11-04 05:08

> 基线：宿主 `51dd2d2`、UI `4e4146f`。本轮只做只读复核、任务表与报告回写；未修改产品实现、未启动共享树服务、未杀进程、未提交，未操作 goal。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 这是当前共享树门禁，不代表 WP-C1 rebase 后仍绿，也不代表真实 Transport/完整产品验收完成。

## A/B 收口状态

- A-BRAND：`e21767e` 接消费方、`3e9b070` 修测试归属，scoped DONE；此前遗留测试导致的红已关闭。
- SHORTCUTS：F1 打开、真实 Notify/Invalidate、520px overlay 像素、Esc 关闭，scoped DONE。
- B-FILES：`6afbf8a`/`f4698b7` + 宿主 `cb34dce`/`8c61670`，可从一层列表进入子目录，scoped DONE。
- B-PREVIEW：`c771c9b` 点文件看全文，scoped DONE。
- A-TOOL/A-TRAJ 仍只签各自插件/分组范围；A-FEEDBACK 仅本地 👍/👎，durable 反馈仍归 WP-E。

## WP-C1 rebase

- 原 WP-C1 包已由 Lead 复验四包测试/vet/build 与两尺寸真实动作证据。
- 原 patch 含基线导出杂项，不能整份覆盖当前 A/B 结构。
- rebase owner `60957d21` 在 `3102e05` 之后的隔离副本进行语义合并；当前已有 MessageKey、Outgoing、CodeBlock/链接和 turn/error 新文件。
- 合并策略正确：保留 `dstool.Service` 分派，在 dsconversation 内做稳定 MessageKey→局部 int 适配；保留 dsbrand/message-feedback/FG，不回退旧工具卡。
- 最终文档和 rebased patch 尚未交付，保持 IN_PROGRESS；WP-C 继续 BLOCKED。

## 新增 C-JOBS

- UI `4e4146f` 落地 `jobs.list/jobs.output` 协议层，提交说明明确“接线等子进程通道”。
- 宿主 `internal/transport/handlers_jobs.go` 当前未提交，说明宿主侧仍在施工。
- 新增 C-JOBS IN_PROGRESS：先冻结 jobs 快照/golden，再接真实子进程 output、状态/错误/退出与重连；协议层存在不签 jobs DONE。

## C-STATS 与真链路

- 宿主 `111e250` 已提供 `contextWindow`（0=未知），但 UI dock 尚未接入；turn/step、cache 分母和真宿主统计仍未闭环。契约 §4 已更新为“提供端部分进展”，不能显示假百分比。
- 宿主 `86991b2` 记录 dsh-ui headless 连本宿主订阅+发送+回答全通；W7 已登记该证据，但仍需同版本独立复核，不自动关闭 FrameSub/cookie/完整工作日。
- 宿主 `cfda363` 将 busy 错误改为 code=busy/hint/retryable=true，属于可操作性改进；不能替代 H14 全部恢复链。

## 纪律

- 新 A/B/C 工作必须登记 owner/paths/依赖；不得以提交先行绕过任务表或契约门。
- 下一批代码任务继续默认隔离 worktree；共享树由 Lead 串行集成，禁止用旧基线覆盖新结构。
- 没有退回 Electron/WebView 主壳、TypeScript UI 或 8 包上限停工；官方外观、Transport 和 U2 仍并行推进。

## 05:40 追加：WP-C1 集成与下一批

- WP-C1 rebased patch 仅 25 个允许文件，Lead 复验隔离全仓 test/vet/build 和两尺寸真实动作均 exit 0；`git apply --check` 通过后串行集成，**共享树 test/vet/build 再次全绿**。WP-C1 转 DONE。
- `42c55e6`/`f01ee06` 补 jobs 全链路与两层测试，`d71c65e` 真机 jobs.list 返回 job-1；C-JOBS 转 scoped DONE，长期重连/多任务面板未验。
- 新一批已在最新共享导出建立两个全绿隔离基线：API-TIME owner `61e11d08` 只补 live Event.Time；WP-C2 owner `47c119b1` 做已加载 Turn 导航、后台正文缓存/A→B→A 与显式回底。WP-C 其余工具树/向前历史/附件反馈仍 BLOCKED。
- 当前 goal 未完成：真实 dshd 长链、live time 合并、完整 WP-C/WP-D/WP-E、共享契约与完整工作日仍待验收。

## 05:55 追加：API-TIME 与新增 B 项

- API-TIME patch 仅 apiclient/history/mux、time 测试与文档。Lead 复跑 apiclient、直接消费包、全仓 test/vet/build 全绿，集成共享树后再次全绿，转 DONE。
- WP-C2 最终 rebase 必须包含 API-TIME，并将 applyEvent、history replay、recordTurnEventsLocked 统一接到 Event.Time；不能在 Event.Time==0 时二次猜 data.time。
- 新增 B-SCHEDULE（`1f6d629`+`809353f`）与 B-DELIVERABLES（`a59b9da`）登记 scoped DONE；日程编辑/长期历史和交付物打开/diff/下载未验。
- 当前 `a59b9da` 共享基线全仓 test/vet/build 全绿；完整 WP-C/WP-D/WP-E 与真实长链仍待验收。

## 未验证

WP-C1 rebase 全仓门禁与真实动作、C-JOBS 子进程通道、UI contextWindow 消费、共享 golden、真机 epoch 换代、宿主重启 cookie、FrameSub 原子性/重放、pending cancel、stale_epoch、U2 完整生命周期、OS 原生输入/高 DPI、完整工作日/A/B。
