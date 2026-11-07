# 2026-11-06 17:08 定时复核

## 当前最高优先级

- 用户已明确“优先推进GO.1”“加快并行”，goal=active revision12；W172/W173修复、独立验收和W163流水线同跑。
- GUI现有补丁可收尾，暂停新增GUI任务与逐部件OS验收；MAINTENANCE已同步此方向。

## TUI / GO.1

- W172独立坏基线OK118/FAIL4，candidate `7807bb9b…` 修掉ROW_02后为OK120/FAIL2；120→80 resize seam使TOOL_WV172_06从1变2，original同场景OK，当前patch不可集成。
- W173独立验收从真实HEAD建坏基线，避免在途重复声明误判；另发现W168的6个preDone实际均在rawDone后，最终必须硬断言sentAt<rawDoneTime，错过窗口=UNVERIFIED。
- W163-PREP发现旧final_verify/write_result硬编码645/5、restart FAIL和旧hash，正重写动态计数，并区分no-catalog与preseed正链。
- GO.1未封版，W163仍BLOCKED(W172,W173)。

## GUI

- 右栏自动fullscreen已集成BUILD/VET=0；其他现有实现继续收尾，不新增任务。
- Sidebar rail已按当前官方纠正：Windows titlebar collapsedWidth=0，56仅普通Web。
- 未见reset、进程误杀、Electron/WebView回退或goal停摆。
