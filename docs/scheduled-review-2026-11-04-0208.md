# 定时方向与派单复核 · 2026-11-04 02:08

> 基线：宿主 `0d93243`、UI `9a155c7`。本轮只做只读复核、Lead 验收和任务表/报告回写；未修改产品实现、未启动共享树服务、未杀进程、未提交，未操作 goal。

## 门禁与路线

- 两仓当前工作树 `go test ./...`、`go build ./...`、`go vet ./...` 均 **exit 0**。
- 宿主新增 dshtui/TUI、原生插件对账、后台 bash、计划模式；没有引入 Electron/WebView 主壳或 TypeScript UI。独立 TUI 不替代 Go+Viva 原生 GUI。
- UI 仍使用 Go+Viva；新增确定性视觉测量，不再依赖模型识图验收。

## 本轮新增 DONE

### TOOL-V 确定性视觉测量

- `scripts/visual-diff.py` + `docs/visual-measurement.md` + 机读证据；Lead 复跑 help、样例、自 diff、1 像素反证和 `verify_results.py` 全部 exit 0。
- 旧 `tmp/official-*.png` 均不作官方基线；真实官方当前窗口已只读捕获，但**同状态官方首屏仍无参考图**。
- 异会话样例 58.36% 差异只作测量，不是功能相似度。

### G7FIX 首载消息空白

- 根因：声明期按旧 Offset 虚拟化，Viva Follow 到布局结束才跳尾；空 Hero 帧提前消费 pendingScroll，首批消息后没有第二帧。
- 修复只改 `cmd/app/main.go`：首批非空消息布局后补一次 Invalidate；插件未改。
- Lead 独立复跑 `TestFirstLoadedMessagesGetFollowUpFrame` 与整包测试均 exit 0。
- 首开 tail40/tail200 中栏唯一色数由旧 1 恢复为 **226**；计数 40/200、总 2065 与宿主一致；直接打开与切回完整中栏 diff=0。
- 证据：`../dsh-ui/docs/blank-center.md`、`tmp/blank-center/**`。只证真实 v4 副本+mockd 首载/切回，实时 WS 与真 dshd 整链未验。

## 当前 IN_PROGRESS

| 项 | 当前事实与下一步 |
|---|---|
| **MSGID（P0）** | UI 将 Transport journal seq 当 v4 消息身份，可能覆盖同号旧消息；工具按最近一条配对且 Reset 未清 tools。owner `6cebe676`，先修身份/配对/重置，再谈美化 |
| **MDSTREAM** | 流式正文当前用 `f.Wrap` 纯文本，官方 streaming 同样走 Markdown；owner `8ffd3d07` 正在补测试与实画 |
| **CGAP / FRAME-GAP** | 消息区与应用框架源码审计中；报告未交付前不据摘要签 DONE |
| **SHORTCUTS** | `f298be8` 键路由通，`9a155c7` 明确面板渲染仍未成功；新增任务行，必须真实打开/关闭和实画 |
| **U0/U2/U1-H6/U1-H13** | 01:15 被过早改 DONE，已撤回；reset/History epoch/golden/cookie、完整授权/替换/实画、Stream.Reset/真实侧栏、审批 reset 等仍开 |
| **W0–W5/W7** | serve 去重/debug、pending cancel、stale_epoch、共享 golden/快照、FrameSub 等仍未闭环；**脏 wssession 重放 patch 已实测触发 137,246 次重连/120s，禁止污染性能或真机结论** |

## 已关闭的独立子项

- A→B→A 连续流对账 8=8、首启水位/epoch、实时 tool→轨迹+自动开右栏、窄窗右栏消失、G7 首载空白。
- U3A 搜索结果卡、W11 两态隔离验证、PERF0 四项实测（其中 G7 当时 FAIL 已由 G7FIX 修复首载空白）。
- 上述子项完成不提升 U0/U1/H14 整链状态。

## 任务表动作

- R0/W6：更新到 02:08，本报告为证据。
- TOOL-V、G7FIX：DONE，记录 Lead 命令、真实断言、证据路径与未验边界。
- 新增 SHORTCUTS IN_PROGRESS，防止“调试码清理”被当功能完成。
- MSGID 保持 P0 最高优先级；MDSTREAM/CGAP/FRAME-GAP 不提前签收。
- 无新增 READY 需要抢派；当前在途包路径不重叠，完成方主动通知，Lead 不 wait/poll。

## 02:20 追加：CGAP / MDSTREAM 完成

- **CGAP DONE**：`../dsh-ui/docs/conversation-gap.md` 交付 28 项源码矩阵、前 5 差异、契约真值和 WP-A~E。MSGID 对应 WP-A；MDSTREAM 只覆盖 WP-B 的流式入口，不冒充整个渲染组件包完成。
- **MDSTREAM DONE**：Lead 复跑两包测试、全仓 vet/build 均 exit 0；流式与定稿共用 Markdown，未闭合围栏不吞字，重复 8 帧不重排。证据 `../dsh-ui/docs/streaming-markdown.md`。
- **WP-B1 已实派** `02dfd1ff`：代码块 banner/复制/wrap/链接 hit；WP-C/WP-D/WP-E 分别因 MSGID+WP-B1、MSGID、附件/反馈契约未冻结而 BLOCKED，未抢跑。
- CGAP 的后续契约需求已集中登记到 `docs/transport-contract.md` §4；Time/turn/error/toolCallId 等宿主已有字段不再误列为宿主待开发。

## 02:30 追加：FRAME-GAP 与 FG-1～5

- **FRAME-GAP DONE**：`../dsh-ui/docs/frame-gap.md` 交付 118 行结构矩阵与五包任务书，72 个相对链接均有效；官方当前会话图只用于结构，不作整屏相似度。
- **FG-1～5 已同轮实派**：`564bdf64` Windows Chrome、`8f80db61` 品牌/导航侧栏、`48b92856` header/页签/右栏、`26dddd99` Composer、`f355333d` dock/指标。`cmd/app` 仅 FG-1，其他包不写共享 pluginapi/apiclient/契约。
- 正式纠正旧口径：V4B 的“真实 tool 自动开”不再是最终官方行为，FG-1/FG-3 改默认收起+按会话记忆；dsstatusbar 的 assistant/tool 计数不得冒充官方轮/步。
- C-PANEL/C-TEAM/C-STATS/C-WORKSPACE/C-EXTENSION-VIEWS 已加入 `docs/transport-contract.md` §4，未冻结前不画静态真值或死按钮。

## 02:41 追加：MSGID 适配层 DONE

- Lead 复跑 `go test ./internal/apiclient`、`cmd/app`/`dsconversation`/`dsrightbar` 与全仓 build 均 exit 0；12 条身份测试覆盖 Transport/v4 分离、messageId/turn/callId、result 先到、Reset 清 tools。
- 签收范围仅是适配层：缺真值时用 Stream+Reset+role 局部键防误覆盖；宿主 assistant data 仍无 v4 seq/messageId，**不签 load↔WS 持久精确去重**，后续字段归 W0 冻结。
- `6e4e90f` 的 U0 scoped DONE 保留，但 W2 仍有 v1 快照、mockd epoch、共享 golden/cookie 和真机换代缺口，不能外推 H14 全链。
- 当前全仓 vet 被 FG-5 `metrics_test.go` 的 `sessionStats` 未定义中间态阻断；MSGID/WP-B1 不改越权文件，FG-5 收口后必须复跑。

## 02:45 追加：WP-B1 DONE

- Lead 复跑 `go test ./internal/mdlite`、包 vet、dsconversation 全包和 app build 均 exit 0；代码块 banner/原字节复制/反馈/wrap/横滚/状态恢复、链接命中和边界均有真实无头断言与 4 张 PNG。
- 组件不执行 OS、不猜路径，OS 剪贴板/打开动作和消息区接线仍归 WP-C。
- MSGID/WP-B1 已完成，但 WP-C 仍等 FG-1 释放 `cmd/app`，不得抢写。
- 当前全仓 vet 的在途阻断为 FG-1 `frame_chrome_test.go` 的 `frameChrome` 与 FG-2 `frame_test.go` 的 `logoRowHeight` 未定义；各 owner 正在收口，不把这轮中间态归责于 MSGID/WP-B1，也不写当前全仓 vet 通过。

## 03:15 追加：FG-2 成品验收 / FG-4 隔离续修

- FG-2 原 owner 失败，但实现、测试、两图完整。Lead 复跑 dssidebar test/vet 均 exit 0，并新建 `../dsh-ui/docs/frame-sidebar.md` 记录路由、空态、几何与未验项；DONE 仅覆盖导航 UI+能力空态，不冒充正式 catalog。
- FG-4 原 owner 失败，当前共享树仍有真实红：`auto` 显示“自动审查”，测试要求“自动审批”。已把同一改动复制到隔离 worktree `C:/Users/Administrator/AppData/Local/Temp/dsh-ui-fg4-isolated` 并复现红，continuation owner `f2cdbb94` 先核官方文案再续修，最终交 patch。
- 下一批代码任务默认隔离 worktree；当前 FG-1/FG-3/FG-5 不迁移、不清理。FG-4 已按新规则执行，避免共享树红测继续卡住其他 owner。

## 未验证

共享 golden、History epoch/409 换代真机、宿主重启 cookie、FrameSub 原子性/重放语义、serve 去重/debug、pending cancel、stale_epoch、U2 完整生命周期、流式 Markdown 实画、消息身份跨轮对账、快捷键面板实画、官方同状态首屏参考、完整工作日/A/B、OS 原生输入/高 DPI。
