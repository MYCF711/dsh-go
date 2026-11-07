# 2026-11-06 18:08 定时复核

## GO.1 当前状态

- W173共享版已GO：真实80×24 OK73/0/U7、120×36 OK74/0/U6；三root全内容零丢失/零重复/精确顺序，A-B/tool配对/quit/进程清理与hash稳定全过。
- 严格preDone仍为UNVERIFIED；真实首次Export均postDone，未混称pre/post；确定性pre/post/duplicate done OK11/0，minimal OK8/0。
- W163-PREP已交付`python tmp/W163/run_final.py`：主smoke16/0，full=41验收+3构建，覆盖W152/W161/W164/W167–W171，动态计数和source-drift门有效。

## 唯一代码阻断 W172

- 多个候选独立帧/真实ConPTY仍不可集成；最新已证明不是单一shrink/reanchor问题。
- 下一投影帧会删除已显示assistant并让后续user上移，cells无row ID，已批准扩到MessageList/historyPaint与row identity协议。
- 禁止通用blank-spacer、隐藏tool或删除内容；两尺寸before/resume/restart/new-prompt/resize必须同绿才可集成。

## GUI

- 按用户最新GO.1优先要求暂停新增任务；现有补丁可收尾，集中验收后置。
- Sidebar首版Windows56已退回并标BLOCKED(GO.1)，不能误记完成。
- 未见reset、进程误杀、Electron/WebView回退或goal停摆；goal=active revision12。
