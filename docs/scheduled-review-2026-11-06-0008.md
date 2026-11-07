# 定时方向复核 · 2026-11-06 00:08 Asia/Shanghai

## 当前方向

- TUI/宿主与 GUI 双线并行，不再使用旧的“Viva让路”口径。GO.1 只有在全部TUI接口真实可玩后才覆盖为首个稳定版。
- TUI：W134 placement 已完成，Node125/125、typecheck、boundary由Lead复验全绿；W146 正用新backend+新W125宿主复跑普通prompt/trace/export/compact。
- 宿主：W138生产steer、W144 effort/goal、W145 fork/rewind/transcript均在隔离副本施工，文件与测试持续更新。
- GUI：W118–W123已收口。定时复核发现新派SHELL/GENERAL与既有W103/W104/W106重叠，已立即纠偏为plugins tabs与provider-card，不重做已完成几何/general导航。

## 门禁与风险

- 最近一次宿主集成全仓 test/vet/build 全绿（W125后）；UI W122/W123集成后全仓全绿。当前隔离副本的新改动未集成前不得外推。
- 真实ConPTY W132=OK20/FAIL2/UNVERIFIED3，两个失败由placement未接造成；W134已修源码，W146正在复验，不能提前宣称可玩。
- 未见Electron/WebView回退、原地移动modules、共享树reset或按镜像名杀进程；旧脚本中的危险命令未执行。
- PLAN中重复的W138 READY已删除；W139–W143保持READY，下一空位同轮外派。

## 后续

1. 收W146真实TUI结果，普通prompt/trace/export正文必须转绿。
2. 收W138/W144/W145并集成复验，再派images/jobs/context等后续。
3. GUI两项纠偏任务只改plugin_manager_draw.go/model_catalog_draw.go，验收不接收重复设置补丁。
