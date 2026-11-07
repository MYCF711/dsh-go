# 定时方向与派单复核 · 2026-11-05 15:08

> 实际复核 15:08–15:45（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未推远端。

## 0. 结论

**两仓门禁全绿，方向未偏离；失败子代理的产出已逐一核对，未把“agent failed”当成“无产出”。** W96 协议文档、W99 设置结构树、W100 主壳结构树均真实完成并集成；W103/W104 前 owner 没有修改目标源码，已重派；W95 多次 Git 断流后无 HEAD，改用 W105 codeload tarball。

## 1. 门禁

| 仓 | test | vet | build |
|---|---:|---:|---:|
| 宿主 `DSH-GO` | 0 | 0 | 0 |
| UI `dsh-ui` | 0 | 0 | 0 |

命令均为 `go test ./... -count=1`、`go vet ./...`、`CGO_ENABLED=0 go build ./...`。W98 已另行在 C: 新二进制独立复跑附件审查 `OK=155 FAIL=0 SKIP=0`。

## 2. 失败子代理 salvage

| 任务 | 真实状态 | 处理 |
|---|---|---|
| W96 协议审计 | `docs/upstream-tui-protocol.md` 完整 312 行，SHA256 `e01cd82a…` | DONE。确认上游默认 JS Cordis 进程内 API，非 ACP/HTTP/WS；W97 必须做 `dsh-go` AgentBackend bundle |
| W99 设置结构树 | 314 行，patch `e07640a9…` | DONE 并集成；官方 5 sections、11 general slot/可形成12行、设置插件与 main:plugins 分层明确 |
| W100 主壳结构树 | 232 行，patch `ce634077…` | DONE 并集成；官方 Trajectory 属主区，不是右栏固定时间线；无官方固定 24px statusbar |
| W103 设置模态 | 前 owner failed；settings.go/model.go 与共享哈希完全一致，目标未改 | 重派 `731c6885`，仍限 settings.go + settings_visual_test.go |
| W104 设置导航 | 前 owner failed；同样未修改目标源码 | 重派 `6647890f`，仍限 model.go + settings_center_state_test.go |
| W95 上游下载 | 多次 Git pack early EOF，仓库无 HEAD，仅有 W96 文档 | BLOCKED 原路径；W105 `b13e12f5` 改 codeload tarball，不再 fetch/clone |

## 3. 当前在途与后续

| ID | 状态 | owner/依赖 |
|---|---|---|
| W103 | IN_PROGRESS | `731c6885`：官方 800px/nav188/row40/居中+mask |
| W104 | IN_PROGRESS | `6647890f`：官方 section/general item 重排 |
| W105 | IN_PROGRESS | `b13e12f5`：codeload 源码包 + submodule tarball + pnpm build/typecheck/smoke |
| W97 | BLOCKED(W95+W96) | W96 已 DONE，等 W105 构建后实现 `dsh-go` AgentBackend bundle |
| W81 | BLOCKED | durable feedback 契约与实现 |
| W7 | BLOCKED | 无打扰真实 UI 完整工作日/A/B/OS 输入 |

## 4. 方向与安全核对

- 保持 Go 宿主 + Viva 原生界面；没有退回 Electron/WebView 主壳、旧 TypeScript 路线、8 包上限或 Q 表停工。
- 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、turn/error、每会话 journal/epoch、原始 history、stale_epoch。
- W99/W100/W101/W102 证明 GUI 复刻已转为“官方结构树 + CSS + 素材优先”，不再凭截图自造层级。
- 未 checkout/reset 共享源码、未移动 `modules/`、未按镜像名杀进程、未重启 DSH/magpie；此前只停止过本任务自己的停滞 Git 下载进程。
- Lead 只做拆包、派单、集成、验收；失败任务先核对真实文件再重派，未自行串行实现。

## 5. 未验项

- W103/W104/W105 成品；W97 桥接；W81 feedback；W7 可见 GUI 工作日/A-B。
- 上游 TUI 的真实 build、submodule、pnpm smoke 与 DSH-GO 联调仍未完成，不得把协议文档称为“已兼容”。
- 官方结构树一比一实现尚未开始，文档完成不等于 GUI 视觉/行为完成。
