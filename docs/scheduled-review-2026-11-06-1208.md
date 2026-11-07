# 2026-11-06 12:08 定时复核

## TUI / 宿主

- W152 主链 baseline36/placement25/steer5/now16/extended27 已绿，但发现两个真实FAIL，不封版。
- 默认session.load无model导致effort.levels空：W160已修为仅取本次catalog active.id且必须存在，Node391/391、typecheck/boundary/compile全0，原repro已 levels=[off,low,high]+session-effort low；ConPTY/A-B实际selection和最终hash在途。
- W160导致验收物漂移已书面确认，W152必须将旧hash标superseded并重固定，不混合结果。
- `/restart` 在非空durable session后换到新dshd PID且sessionId保持，但60s只显示Restarting，未重新挂载；W161已READY，W160释放backend单写者后派修。

## GUI

- 中文IME验收 DONE：OK14/FAIL0/UNVERIFIED1，两尺寸组合/caret/草稿/快捷键/catalog保存全过；官方普通IME Esc预期未验。
- BUG-UI-LAYER-001已集成并Lead全仓全绿：overlay独立native layer+命令真mask，两尺寸Hero/conversation residual=0、原生各OK16/0。
- Fields与composer hotkey已Lead全仓全绿；高DPI、真实多tab、第三方overlay动态热替换仍未验。

## 偏差

未见Electron/WebView回退、共享树reset、goal操作、DSH/magpie重启或按镜像名杀进程。GO.1未封版。
