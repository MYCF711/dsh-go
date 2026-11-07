# 2026-11-06 07:08 定时复核

## TUI / 宿主

- W149 已 Lead 全仓 TEST/VET/BUILD=0：context.usage 真值边界、stdio MCP、隔离 side.query/cancel/close 与父 home 零写均有真实证据。
- W155 `86bd19e3` 正在接 context full、MCP、sideQuery 到正式 backend；W150 `abf2b87a` 继续 rename/color/init。
- W153 已实测 baseline OK36/0/0、placement OK25/0、steer v4 OK5/0、now wire OK16/0；effort/goal/context/auth、图片 store/get/有序 content 已过。
- DeepSeek 图片轮超时未算通过，正用 Kimi+有界 cancel 复跑；jobs、step/TTFT 与最终报告未收口。W152 等待 W153+W155+W150。

## GUI

- model-catalog、plugins、Fields 均已 Lead 全仓全绿；真实 OS 验收已完成两尺寸 general/models/plugins、微软拼音保存读回、provider test/set-active 与 A/B。
- 1920×1032 连续4次 OS Tab 的5张截图 changed_pixels=0，无可见焦点圈，必须记 FAIL；headless 焦点 PASS 不抵消。
- 所有实测窗口为96 DPI，高 DPI 为 UNVERIFIED。GUI-SETTINGS-FOCUS-OS-FIX 已 READY，待最终报告/最小复现派单。

## 偏差

未见 Electron/WebView 回退、共享树 reset、goal 操作、DSH/magpie 重启或按镜像名杀进程。GO.1 未封版。
