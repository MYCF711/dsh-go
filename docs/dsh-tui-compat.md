# dsh-TUI 兼容性

本仓支持官方终端界面 **dsh-TUI**（`@deepseek-harness-tui/dsh-tui`）直连。

> 这是**兼容性说明**，不是版本号。本仓版本只有稳定 `0.2.2-go.N` 与测试 `0.2.2-beta.N` 两种形状。

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

- TUI 的 `dsh-go` 后端当前仍经官方运行时加载，**独立入口在开发中**；脱离官方运行时后即可纯本宿主直连。
- 宿主侧未完成的接口见 [official-desktop-api-parity.md](official-desktop-api-parity.md) §7。
