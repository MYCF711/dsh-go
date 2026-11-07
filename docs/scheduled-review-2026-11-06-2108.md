# 2026-11-06 21:08 定时复核

## 模型纠偏

- 用户明确：本会话Lead继续Kimi，所有子代理使用DeepSeek V4.1F（`group/deepseek-v4.1-flash1017`）。
- 已写入`$DSH_HOME/LEAD.md` §1.1：主会话模型不限；子代理必须通过支持model/provider override的入口显式指定DeepSeek，嵌套派单同规。
- W180/W181已由workflow `workflow-31029409e1764a13`并行恢复，任务表记录同一model。

## GO.1

- W179已交付W161=33/0、W164=120/0+defer8/8，合计161/0；生产7902文件零diff。
- W181预跑ui37/0、placement32/0、negative20/0，最终四组复跑在途；W180 row identity与D1回归修复在途。
- W163 early full仍NO-GO；W180/W181完成后必须新建快照跑`python tmp/W163/run_final.py`，全绿才覆盖GO.1。

## GUI

- GO.1优先，暂停新增任务与逐部件OS验收；现有实现与集中验收后置。
- 未见reset、进程误杀、Electron/WebView回退或goal停摆；goal=active revision12。
