# 2026-11-06 08:08 定时复核

## TUI / 宿主

- W155 已 Lead 独立 Node 384/384、typecheck、boundary、compile 全0，MCP/sideQuery 正式接线完成 scoped。
- W153 已归档 OK258/FAIL9：主链、images/jobs/effort选择/goal/fork/rewind/transcript/context/auth/ViewSpec通过；生产step/TTFT与实际effort selection缺失是产品FAIL。
- W157 `ef7ea27b` 已修生产cmdRun/modelRunner sink，中期新dshd上 backend OK69/0、durable OK20/0，starts9/deltas18/ends9、TTFT832–3026ms、off selection8。
- W157最终真实steer/A/B/restart、全仓与patch尚未交付，不能合入；W150 rename/color/init仍在途。W152等待W150+W157及最终整包。

## GUI

- GUI OS验收 DONE：OK16/FAIL1/UNVERIFIED3。唯一产品FAIL为1920原生Tab无可见焦点圈；高DPI、完整root多tab、composer关闭焦点恢复未验。
- `27d9a2ff` 已定位为FocusVisible未用于绘制，补官方2px business-primary焦点圈到nav/close/action rows与plugins tabs/retry；两尺寸图元、Tab/ShiftTab/Enter/关闭恢复已绿。
- 最终原生两尺寸截图、catalog/plugins/Fields和全仓仍在跑，未合入，不能用headless替代OS最终证据。

## 偏差

未见Electron/WebView回退、共享树reset、goal操作、DSH/magpie重启或按镜像名杀进程。GO.1未封版。
