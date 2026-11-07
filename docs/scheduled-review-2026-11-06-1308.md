# 2026-11-06 13:08 定时复核

## TUI / 宿主

- W152最终干净快照 NO-GO：OK645/FAIL5，64命令63/1、AgentSession29/0。
- W161已证明restart红为驱动假阳性：生产Go/TS零改，两轮OK32/0、A-B OK16/0；Lead首次复用固定home红4，换唯一RUN后OK8/0，已回填驱动唯一home要求。
- W162 `74734c87` 正在独立副本修无catalog+DSH_GO_MODEL启动，顺序为load.model→显式env→catalog兜底。
- W164 `7fc2878f` 正在审计旧conformance四红，不凭旧驱动判回归，也不凭新证据忽略。
- W163已READY，等待W162+W164后重跑W152全套，GO.1未封版。

## GUI

- 插件多tab `79e523ca` 已确认官方settings.plugins.tab槽名并跑出未知槽位真实红，正在扩展注册契约。
- 动态overlay `038ea406` 正在验动态Children/热替换/Order重排与layer/焦点/mask。
- 右栏 `4185d591` 发现W120既有未提交实现，已转为审计/复验，禁止重复开发。
- goal已恢复active revision9，TUI与GUI并行；未见reset、进程误杀或方向回退。
