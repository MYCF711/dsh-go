# 定时方向与派单复核 · 2026-11-05 00:08

> 基线：宿主 `af4a49a`、UI `3925e84`。在 23:08 实测基础上继续复核并响应“增加更多可并行任务”。不操作 goal，不启动可见窗口，不杀进程，不改共享产品代码。

## 方向与门禁

- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 全部 exit 0；AGENTS 6995/6949 字节，文档链接缺失 0。
- 路线仍为 Go 宿主 + Go/Viva 原生界面；未改 Transport canonical，未用 mockd/ACP/截图冒充真实整链。
- W29 文件链接（`98db5976…`）、W31 GUI断线时钟（`7a5af03c…`）、W32 TUI断线时钟（`b256a18d…`）已独立复验并集成。
- W18–W32 的 0.2.2 修复除当轮在途项外均已回填真实 patch/断言/未验项。

## 当前七路并行

| ID | owner | 范围 |
|---|---|---|
| W33 CONTRACT-GOLDEN | `a4b23ef7` | H14 两仓快照/golden、mockd epoch、真实 apiclient 重放 |
| W35 RACE-CURRENT | `3c1827c3` | 两仓全仓 race，只读验证 |
| W36 REAL-CHAIN-HEADLESS | `a1ce9cfe` | 新 dshd + 真实 apiclient + 临时 home，无窗整链 |
| W39 TUI-ANIMATION | `be5dfcd9` | 22 帧开屏三段与 idle 独立层 |
| W40 SETTINGS-GAP-AUDIT | `cac6720d` | 官方11页 vs 当前4真值页 |
| W41 PLUGIN-LIFECYCLE-AUDIT | `1cd970b3` | 公开槽/禁用/替换/Dispose覆盖矩阵 |
| W42 PLUGIN-LIFECYCLE-CORE | `ab33c637` | 公开槽、核心禁用、依赖与 Dispose 核心闭环 |

## 新发现与纠偏

- HOST_RACE：59 包（49 ok + 10 no test），exit 0，0 DATA RACE；日志 `tmp/race-current/host-race.log`。
- UI_RACE：40 包 exit 1，`cmd/app` 共 6 个 DATA RACE：`TestF2NewSessionWhileApprovalPending` 5 段、`TestWPC1HeadlessRealActionsAndTwoSizeEvidence` 1 段。**W43 必须修，但不能与 W42 同写 `cmd/app`，标 BLOCKED(W42)，完成后立即派。**
- 插件审计已证实：`shell.leading`/`statusbar` 公开槽无消费；核心5个 `enabled:false` 不生效；Dispose 有残留；禁用依赖可致装配失败。W42 正在做核心闭环，不能只写审计不修。
- 设置审计把已有终端/任务真值与官方完整页分开，禁止把“有基础能力”冒充“官方完整 READY”。

## 明确阻塞 / 后续同轮外派

- W34 serve 去重/debug/journal：等 W33 契约冻结。
- W37 性能实测：等 W35/W36 资源窗口，避免并发污染指标。
- W38 WS 压缩：等 W33 契约冻结；W31/W32 已完成，不再阻塞客户端。
- W43 UI race 修复：等 W42 的 `cmd/app` 写锁释放。
- WP-C/WP-D/WP-E/A-FEEDBACK 仍契约阻塞；不伪造 loadOlder/Inspect/附件/durable feedback。

## 00:42 追加：W35 完成

- HOST_RACE 59包 exit0、0 DATA RACE；UI_RACE 40包exit1、6段报告（不冒称6个根因）。
- 证据已保存：`docs/race-current.md`（sha256 `012baa7a…`）与 `tmp/race-current/**` 原始日志。
- F2单次复现3段；WPC1单次曾0段、count=5复现1段，波动如实保留。
- kernel依赖93行仅kernel自身；两仓CGO_ENABLED=0 build均exit0。
- W43仍等W42释放 `cmd/app`，不能用W35的宿主0 race掩盖UI失败。

## 01:05 追加：H14/真实链路/插件审计收口

- W33 已集成：host patch `7c1bbda3…`、UI patch `693ffaf2…`；三份共享golden双仓同哈希，真实apiclient重放通过。
- W36 已保存：`RCH_SUMMARY OK=72 FAIL=0 SKIP=0`，证据 `docs/real-chain-headless.md`、`tmp/real-chain-headless-wefimz9b/`；首跑65/6反证保留。
- W41 已保存：`docs/plugin-lifecycle-audit.md` 与探针日志；当前第三方真实替换官方=0，公开槽/核心禁用/Dispose缺口进入W42。
- 当前六路：W34 serve journal、W39 TUI动画、W40设置审计、W42插件核心、W44 plugingen测试、W45重启恢复。
- 契约 H13 已更正为“宿主+真实UI apiclient收口，可见GUI未验”，不再保留pending cancel未闭环的旧说法；pending approval重放time归W34。

## 01:45 追加：新一批集成与重启缺口

- W39动画 `3589c6be…`、W42插件核心 `977e9011…`、W44 plugingen `d3c10802…`、W46日程 `9cd21016…`、W47模型 `aedc3b0e…` 均已Lead复验并集成，两仓test/vet/build全绿；W39定向race也已exit0。
- W45按 `OK=79 FAIL=5 SKIP=0` 记账，证据 `docs/restart-recovery.md`、`tmp/restart-recovery-20261105-010633/`。旧cookie/token401、新token/reset/stale/v4追加均通过；新epoch旧history原始text/reasoning/tool/approval缺失，修复归W49，不改写成通过。
- 当前六路：W34 serve journal、W38 WS压缩、W43 UI race、W48宿主插件管理、W52快捷键、W55服务授权。
- W49/W50/W51/W53按文件与接口依赖阻塞；W54 JSON生命周期READY；W37继续等低负载窗口。

## 未验证

W34/W38/W43/W48/W52/W55 成品；W37性能、W49 journal重建、W50插件管理glue、W51模型三态、W53版本行、W54 JSON生命周期；可见GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。