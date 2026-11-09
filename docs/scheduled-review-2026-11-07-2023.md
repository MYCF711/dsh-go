# 定时方向复核 · 2026-11-07 20:23 宿主稳定门

## 口径修正

用户明确：**稳定版只负责宿主完备；TUI 与官方壳的问题不是稳定版范围，也不是宿主能处理的。**

因此以下从稳定版门移出，转 TUI/产品线：

- W167 picker / selection / steer 残留
- W170-BLANKBAND
- TUI-DSH-GO-WORKSPACE
- TUI-SETTINGS-PAGE-MARGIN-HANG
- TUI-MODE-DESC-DUP
- 上游 dsh-TUI issue #1903
- 官方 Electron 壳直连（M0）

## 宿主独立门禁

本轮实跑：

```text
go build ./...  EXIT=0
go vet ./...    EXIT=0
go test ./...   EXIT=0
```

HEAD：`9cfe2f4`（官方 0.2.2 接口面对齐 + dsh-TUI 直连，570 文件）。

## 宿主仍差

### 插件面

`docs/official-plugins-022-map.md`：

- DONE 37
- PARTIAL 32
- MISSING 28
- P0=0，P1=5

P1：

- `session-title-llm`
- `llm-retry`
- `sandbox-policy`
- `image-offload`
- `repeat-tool-reminder`

平台型（sandbox runner、bash/pwsh-sandbox、ptc-runtime）需要显式记录“不做/后置”，不能只用生态欠账带过。

### API 面

`docs/official-desktop-api-parity.md`：

- 官方 153 方法
- 已接线 82
- 未覆盖 63

其中实验/R6 冲突项可显式后置，但每个方法必须有确定结论，不能空成功、假数据或静默 501。

### 已知宿主缺口

- `mcp.go` 只统计 MCP 工具，未注册进工具注册表
- terminal 不是 PTY；stream 方法在 HTTP 面为一次性或 501
- pluginManager 无运行时插件加载器
- schedule 缺 create/edit/view/run/history 5 个方法
- `runIDKey` / `subDispatchKey` 同值（潜伏）
- 沙盒只有 policy 层，无 OS 隔离

## 结论

**不能更新稳定版。** 宿主 build/vet/test 绿只是基础门；插件 28 MISSING/32 PARTIAL、API 63 未覆盖、stable CI 未跑，均未达到宿主完备。

可以更新：本地 beta / play。  
不能更新：`0.2.2-go.1` 稳定版、GitHub stable。

## 文档

- `UIPLAN.md` 当前复核已改为 20:23 宿主稳定门。
- `PLAN.md` W163 已标明 TUI 线移出、宿主缺口未清。
- 本文件为 20:23 证据；`doc-links` 待收尾复跑。
