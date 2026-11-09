# dsh-TUI 兼容性

本仓支持官方终端界面 **dsh-TUI**（`@deepseek-harness-tui/dsh-tui`）直连。

> 这是**兼容性说明**，不是版本号。本仓版本只有稳定 `0.2.2-go.N` 与测试 `0.2.2-beta.N` 两种形状。

## 0.19.0 当前状态（2026-11-08 00:45）

- 官方包 `@deepseek-harness-tui/dsh-tui@0.19.0` 已从 npm 拉到临时目录并解包；`node bin/dsh-tui.js --help` exit 0，可作为普通终端程序在 ELYTerminal 内启动。
- **不能直接连 Go 宿主**：0.19.0 CLI 只列 `dsh | claude | codex`，没有内置 `dsh-go` 后端；`--backend dsh-go` 当前只会提示未检测到官方 `dsh` CLI。**第三方 backend 接口没有取消**：0.19.0 仍保留 `lib/types/dsh-adapter/backend-assembly.js` 与 `backend-registry.js`，安装声明 `dsh.bundle` 的第三方 bundle 并重启后会进入 picker；缺的是我们的 `dsh-go` bundle。
- 0.18.0 的 `src/backends/dsh-go/` 是本地 W109 集成目录，`src/dsh-adapter/backends.generated.ts` 由 Lead 在本地生成，**不是官方 0.18 npm 包自带**；0.19.0 官方包 `GENERATED_BACKENDS` 只有 `claude`、`codex`，所以 dsh-go 没有随之进入 0.19.0。
- ELYTerminal 是**终端模拟器**，不是 TUI 框架：TUI 应作为程序运行在它内部，不是基于它的 UI 重新做 TUI。

## 怎么连

```bash
# 1) 起宿主
./dshd.exe serve --home <home> --addr 127.0.0.1:0 --policy ask
#    → ready {"url":"http://127.0.0.1:<port>","token":"<token>"}

# 2) TUI 直连（backend=dsh-go）
node ../dsh-TUI/bin/dsh-tui.js --backend dsh-go --base <url> --token <token>
```

## 已验证

| 项 | 状态 |
|---|---|
| 独立宿主客户端（不依赖官方 `@deepseek-ai/dsh` 运行时） | 进行中 |
| `/connect` 换 cookie、`POST /api/<method>`、`/ws` 订阅 | 宿主侧已实现并有门禁 |
| 官方 0.2.2 wire（envelope、200-on-error、mux 七帧、`$events`） | 已用未修改的官方客户端包联调通过 |
| TUI 四个已知渲染问题（`/permission` 双行、工具卡括号、粘贴残留） | 已修；「页边距卡死」四条 harness 未复现 |

## 已知限制

- TUI 的 `dsh-go` 后端由本仓 `../dsh-tui2go` 以第三方 `dsh.bundle` 形式提供，`src/backend.ts` 已是 T4 真实现（6,660 行 TS、30 个测试文件），不再是 T1 脚手架。
- **独立入口已落地**：`src/standalone/`（`args` / `host` / `main` / `screen` / `tui`），支持 `--base/--token` 远程形态与省略两者自启 `dshd serve`；真实 TTY 验收（`TUI2GO-PLAY`）已 GO。
- **尚未支持**：`--resume <sessionId>`（会话级续接仍是缺口，见 [handoff-2026-11-09.md](handoff-2026-11-09.md) §3）。
- 宿主侧未完成的接口见 [official-desktop-api-parity.md](official-desktop-api-parity.md) §7。
