# 定时方向与派单复核 · 2026-11-04 06:08

> 基线：宿主 `c3e49c9`、UI `eab8fe4`。本轮只做只读复核、任务表与报告回写；未修改产品实现、未启动共享树服务、未杀进程、未提交，未操作 goal。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- `eab8fe4` 补上 apiclient.ScheduleList；`c3e49c9` 修正跨仓 doc-links 校验边界。全绿仍不等于真实产品完成。

## WP-C1 / API-TIME / C-JOBS

- WP-C1 rebased patch 仅 25 个允许文件，Lead 复验隔离与集成后的全仓门禁和两尺寸真实动作，均通过，已 DONE。
- API-TIME 已提供 Event.Time 并集成共享树；WP-C2 最终 rebase 必须包含它，并把 applyEvent/history/recordTurnEventsLocked 统一接到 Event.Time。
- C-JOBS 已 scoped DONE：`42c55e6` 全链路、`f01ee06` 两层测试、`d71c65e` 真机 job-1。
- B-SCHEDULE/B-DELIVERABLES/B-FILES/B-PREVIEW 均只签各自 scoped 范围，不冒充完整管理器/编辑器。

## WP-C2

- owner `47c119b1` 在隔离副本开发 Turn shell/导航、后台正文缓存/A→B→A 与显式回底。
- 当前已有 `wpc2_runtime.go`、Turn reducer/headless 测试等；最终 `docs/wp-c2.md` 与 patch 尚未交付，保持 IN_PROGRESS。
- WP-C 的完整工具树、向前历史、附件/引用/宿主反馈仍 BLOCKED；不由 Turn 分组提前代替。

## 新发现：MODEL-WIRE，不可立即做模型下拉

`648bdea` 增加 `session.setModel`、modelOverrideStore 和快照方法，但源码核查：

- `modelOverrides.Set` 会写 `state/session-model.json`；
- 全仓未见 `modelOverrides.Get` 的消费点；
- 因而下一轮 request/context 不会仅因调用 setModel 就使用新模型。

这属于“写了不用”。已新增：

- PLAN **W12 IN_PROGRESS**：宿主先让 setModel 改变下一轮、清空回默认、重启持久且不写 v4；
- UIPLAN **MODEL-WIRE IN_PROGRESS**：通过前 FG-4 的模型只读不得升级为假下拉；
- 契约 §4 **MODEL-WIRE**：快照/store 存在不算行为完成，catalog 与失败回滚另验。

## 纪律

- 新功能继续按“源码消费链”验收，不能只查方法名、快照或文档 ✅。
- 下一批代码任务继续默认隔离 worktree；共享树由 Lead 串行集成，禁止旧基线覆盖新结构。
- 官方外观与 Transport 并行推进，没有退回 Electron/WebView 主壳或 TypeScript UI。

## 06:30 追加：WP-C2 原包验收与 rebase

- WP-C2 原包 patch/全仓 test/vet/build 由 Lead 独立复验全绿；Turn 分组、后台温切0 HTTP/0 cancel、Outgoing精确对账、回看/回底和两尺寸 `-count=3` 证据齐全。
- 当前共享树 `git apply --check` 因 main.go 后续变化失败，未强行套用。已在 `efd46ab` 最新导出建立全绿 rebase 副本，owner `2d8dd86a` 负责语义合并。
- rebase 同时必须完成 API-TIME 壳侧接入：live applyEvent=ev.Time、history=Frame.Event、recordTurnEventsLocked 直接消费解析值，Event.Time==0 不再二次猜 data.time。完成后仍需全仓门禁和两尺寸真实动作复验，当前不签 DONE。

## 未验证

WP-C2 rebase 与全仓门禁、API-TIME 壳侧统一消费、MODEL-WIRE 下一轮真实覆盖、catalog/选择回滚、共享 golden、真机 epoch 换代、宿主重启 cookie、FrameSub 原子性/重放、pending cancel、stale_epoch、U2 完整生命周期、OS 原生输入/高 DPI、完整工作日/A/B。
