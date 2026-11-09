# 2026-11-08 09:08 定时复核

## 结论

团队方向未偏离。I01/I02/I03 已完成，I04 在途；T05 已按干净树纠偏交付，T08/T09/T10 三个实测 blocker 正在修复。宿主 stable 仍 NO-GO，不能因 scoped/mux 证据提前进入 HS-17/18。

## 当前状态

| 项 | 状态 |
|---|---|
| T00/T01/T02/T03/T04/T05/T06/T07 | completed |
| I01/I02/I03 | completed，均有真实进程或三方合并证据 |
| I04 | in_progress，接 T04 与 settingsStore/stream/attachWait mux |
| I05 | pending，等 I04；T05 patch 已满足干净树要求 |
| T08 | in_progress，修 Windows goal 静默停轮 |
| T09 | in_progress，修 L1 settings 全路径 secret 泄漏 |
| T10 | 已创建，修 ConPTY 长输入根因 |
| I06/HS-17/18/19、GUI | 未启动 |

## T05 交付与剩余门

- 旧 `go2-t05` 保留 216+dirty，仅作证据；最终交付在 `go2-t05-clean`。
- 实际 patch `71e0b1ea32e15fd5c85fe1e58698689875a130ca1ebaa167b6bb300287c62499`，4861 行，4 owned 文件。
- 共享树 apply-check 预期冲突，I05 必须三方合并。
- job.list 真流、mux 四方法、attach uplink、HTTP 回落和心跳 flake 修复已有 scoped 证据。
- **I05 仍必须**：注册 job/terminal 7 个官方 unary；核官方 args/sessionId 注入；真实 L2 11 法、mux、uplink、HTTP 回落；MinGW 相关 race。

## 三个稳定版 blocker

1. **F4 / T08**：Windows `ERROR_GEN_FAILURE` 导致 goal 静默 disarm；T06 5 跑 1 红。
2. **Settings secret / T09**：I02 证明 L1 全路径可泄漏 Secret:true 值；Lead R4 决定保持字段形状但剥离值。
3. **ConPTY / T10**：T05 证明 50+ 字节输入可丢、60+ 稳定失败并污染后续输入；桥接 32 字节分片只是缓解，必须修 `internal/terminal` 根因。

三者及 I08/I09/I10 均已加入 I06 前置，缺一个都不得放行。

## 合规与偏差

- T07 45 条暂存孤儿已人工确认；两个 pluginmanager 文件归 HS-16C。
- HS-16D 是内容已集成/发布暂存待办，不盲目 git add。
- HS-GOAL-TRUTH 依赖 HS-11；历史 tmp 包不排除。
- T05 旧污染树没有用于 patch，纠偏有效。
- 未见 reset/clean、危险杀进程、重启 DSH/magpie、写 profiles、自动 push。
- goal 保持 active/armed，本轮未改状态。

## 下一动作

等待 I04、T08、T09、T10 主动交付；integrator 随后串行处理 I05/I08/I09/I10。I06 必须刷新 T07 账本并附 Lead 裁决，再进入 HS-17/18。
