# 定时方向与派单复核 · 2026-11-04 22:08

> 基线：宿主 `af4a49a`、UI `3925e84`。22:30 追加 DSH `0.2.2-rc.1` 兼容性审计与修复派单。只做只读复核、Lead 验收、任务表/报告维护；未启动测试 GUI、未杀产品进程、未重启 DSH/magpie。

## 门禁与文档

- 宿主全仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0（含 TUI-ASSETS 集成后）。
- UI 全仓三项均 exit 0（含 TOPBAR-ACTIONS、STATUSBAR-DOCK 集成后）。
- `AGENTS.md` 6995 字节、`../dsh-ui/AGENTS.md` 6949 字节；文档链接缺失 0；无 `dshgogui/dsh-gui` 测试窗口。

## 新验收

- TOPBAR-ACTIONS：patch `f036d386…`，28×28“全局定时任务·只读”入口，真实 ScheduleList，空值不画；两尺寸真命中，全仓绿。
- STATUSBAR-DOCK：patch `8cee0c41…`，dock/pill 收缩、数字格式、缓存负值/溢出防护；320px 窄父列两尺寸实画通过，全仓绿。
- TUI-ASSETS：patch `18aef5d5…`，22 个 25×40 鲸鱼帧、三段开屏/idle 节奏、dark/light 各 81 色；`verify_assets.py` OK=1288 FAIL=0，全仓绿。

## DSH 0.2.2-rc.1 兼容性（22:30）

官方发布：`dsh-v0.2.2-rc.1`，2026-11-04T13:07:14Z；release notes 已保存到 `tmp/compat-0.2.2-rc.1/release-notes.md`，sha256 `e2682d8c…`。当前基线仍是 `0.2.1-rc.5`，未升级版本号、未改 Transport。

三路只读审计：`a533e2a6`（宿主/模型）、`77374fd9`（UI/TUI）、`c11a8dca`（工具/运行时）。已确认并派出隔离修复：

1. **W18 ACP 工具执行准入**（`2cf6990c`）：声明按模式过滤、执行却用全量 registry；直接/嵌套/ask=nil 隐藏工具必须底层 Exec=0。
2. **W19 完整工具参数 fail-fast**（`59ccd3cb`）：合法 call+坏 call 同响应，任何 Runner 执行前必须整体终止，不得先执行合法 call。
3. **W20 权限/生命周期**（`2709cc63`）：setPermission 实际下一轮生效；活动会话 delete 先等收工；Cancel 超时不假空闲。

附带确认：WS 未实现压缩，但压缩是可选协商，不构成本地互联破坏；Node 24.9/Cordis 4.1/JS PTC 协议变化不直接适用于 Go 原生路线。最终分类以三份 file:line 审计报告为准。

## 在途与边界

- MESSAGE-AREA `c7267578`：官方内容列、用户气泡、reasoning settled 预览；不得改壳三栏。
- TUI-MESSAGE `c7f541ad`：Markdown/代码框/工具卡/thinking，保留首屏。
- W4 pending cancel、W5 错误体已 DONE；U1-H13 剩真实 UI 同版本复验。
- WP-C/WP-D/WP-E/A-FEEDBACK 仍契约阻塞，禁止抢跑。

## 23:05 追加：审计与前三项修复收口

- W17 三份审计均完成：宿主16项、UI/TUI18项、工具18项，文件在 `tmp/compat-0.2.2-rc.1/`；真实 v4 两批511文件读取通过。
- W18 ACP准入 patch `1cac95e2…`、W19参数fail-fast `c9b9e50f…`、W20生命周期 `3a3ec56c…` 均已集成，共享全仓 test/vet/build 全绿；W20另有六组定向race通过。
- MESSAGE-AREA 共享增量 `12bcd667…`、TUI-MESSAGE 增量 `f27694a0…` 已集成，避免重复首屏；GUI/TUI两档与全仓门禁通过。
- 当前七路并行：W21 workflow原文、W22 agent原文、W23 tilde、W24 AGENTS预算、W25 GUI草稿、W26 TUI草稿、W27多选推荐。
- 模型容量与断线计时因同文件依赖排在后续，避免与W22/W25/W26抢写。

## 未验证

W21–W27 成品；模型容量与断线计时；真实 dshd+UI 复验、共享 golden、宿主重启 cookie、FrameSub/重放、完整工作日/A/B、附件/引用/durable feedback、settings 11 页、真实 WPS 200。