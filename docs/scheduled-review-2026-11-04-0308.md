# 定时方向与派单复核 · 2026-11-04 03:08

> 基线：宿主 `8bf6363`、UI `6367855`。本轮只做只读复核、Lead 验收和任务表/报告回写；未修改产品实现、未启动共享树服务、未杀进程、未提交，未操作 goal。

## 门禁快照（并行工作树会变化，不能混写）

- **宿主**：当前 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。新增 workflow、session.compact、计划模式跨轮、插件 enable/disable 等，与 Go+Viva 主线不冲突。
- **UI 首轮全量**：build=0、vet=0、test=1；失败为 `dsshell TestDragIgnoresOtherHandleAndRightBar`。
- 随后该 dsshell 用例单独复跑 **PASS**，说明是并行中间态，不另派重复修复。
- FG-5 Lead 复验时，dsstatusbar test/vet 通过，但 app build 被 FG-1 正在编辑的 `startShot` 参数和 `headless_windows.go` 未使用 import 阻断。**当前不能写 UI 全绿**；FG-1/FG-3/FG-4 收口后必须再跑完整门禁。

## 新增 DONE

### FG-2 品牌/导航侧栏（原 owner 失败，Lead 按成品验收）

- dssidebar test/vet 均 exit 0；40px 品牌、38px 新会话、36px 导航/工作区、真实 NewSession/路由/返回、扩展隐藏与两尺寸实图均有证据。
- DONE 仅覆盖导航 UI+真实能力空态，正式插件/任务 catalog 归后续。证据：`../dsh-ui/docs/frame-sidebar.md`。

### FG-5 Dock 与指标真值

- 撤掉 assistant=轮/tool=步及旧墙钟速率；保留真实 total/input/output/上下文与连接态，缓存率/contextWindow 缺失隐藏。
- 通过现有 `main.composer` 的 Order20 将 dock 放到卡片下方；26px、空 0、两尺寸同轴，连接态独立 shell.bottom。
- Lead dsstatusbar test/vet 均 exit 0；证据 `../dsh-ui/docs/frame-dock.md` 与 3 张 PNG。C-STATS/共享槽/真宿主统计仍未验。

## 当前 IN_PROGRESS / BLOCKED

| 项 | 当前状态 |
|---|---|
| **FG-1** | Windows Chrome、右栏 glue 与测试在途；当前 app build 有自身中间态，必须收口 |
| **FG-3** | header/对话-轨迹切换与 rightbar 在途；实现前红测曾阻塞完整包门 |
| **FG-4 continuation** | 原 owner 失败，已在隔离 worktree 复现 auto 文案红；owner `f2cdbb94` 续修。官方 `auto-review` 与宿主 `auto` 语义不同，不能为绿硬套官方原文 |
| **SHORTCUTS** | 后续提交已有面板/overlay 修复和测试，但 Lead 尚未独立核行为证据，保持 IN_PROGRESS |
| **WP-C** | MSGID/WP-B1 已完成，仍等 FG-1 释放 `cmd/app` |
| **WP-D** | 等 FG-3+WP-C 数据形状，避免与 FG-3 抢写 rightbar |
| **WP-E** | 附件/反馈/分支契约未冻结，不画死按钮 |

## U0 / U1-H6 状态纠偏

- U0 `6e4e90f` 的 scoped DONE 保留：History epoch/reset/409 基线重连已提交；W2 仍有 v1 快照、mockd epoch、共享 golden/cookie 与真机换代缺口。
- U1-H6 `2aea9f3` 的 scoped DONE 保留：13 处假阳性修复、31 用例自测失配 0、e2e 22 PASS。宿主重启 cookie、FrameSub、真实侧栏活动、完整工作日不归这个 DONE 自动关闭。

## 纪律与后续派单

- 下一批代码子代理默认隔离 worktree/导出副本，共享树由 Lead 串行集成；当前 FG-1/FG-3/FG-5 不迁移，FG-4 已按隔离规则续修。
- MSGID/WP-B1 已完成，但宿主持久 ID、OS 动作和消息区接线不随之完成；WP-C 不得与 FG-1 抢写。
- 官方外观继续与 H14/U1/U2 并行；没有用 Electron/WebView 主壳、TypeScript 路线或 8 包上限停工。
- 未提交共享契约/快照前，不实现 C-STATS/C-PANEL/附件等新能力。

## 03:50 追加：FG-1/3/4 验收完成，WP-C1 开工

- 三个代理均在最终文档收尾前失败，但成品完整。Lead 复验：FG-1 `cmd/app` test/vet/全仓 build 全绿；FG-3 dstopbar+dsrightbar test/vet 全绿；FG-4 隔离副本 test/vet/build 全绿。
- FG-4 官方/宿主 auto 语义边界已核：官方 auto-review=自动审查，宿主 auto 是另一策略，本地“自动审批”不冒充官方原文。owned 改动已串行集成共享树。
- **集成后 UI 全仓 `go test ./...`、`go vet ./...`、`go build ./...` 全部 exit 0**。此前各包 vet/build 中间态均已收口。
- FG-1/FG-3 的 Lead 验收记录已补 `../dsh-ui/docs/frame-chrome.md`、`frame-header.md`；FG-4 证据在 `frame-composer.md`。
- WP-C1 owner `51f62384` 已在完整隔离副本开工，基线全绿；做渲染 key/本地 echo、代码块/链接真实接线、turn/error/stopped/retry 与失败草稿，剩余 WP-C 不提前签完成。

## 未验证

UI 最终完整门禁、FG-1/FG-3/FG-4 成品、SHORTCUTS 独立行为、C-STATS 真宿主投影、共享 golden、真机 epoch 换代、宿主重启 cookie、FrameSub 原子性/重放、pending cancel、stale_epoch、U2 完整生命周期、真实侧栏活动、OS 原生输入/高 DPI、完整工作日/A/B。
