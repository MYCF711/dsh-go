# MAINTENANCE · 当前执行台账

> 本页只解释当前怎么接手，不复制一份会漂移的派工单。宿主顺序与 Lead 派单表以 `PLAN.md` §14.1 为唯一来源；界面派单表以 `UIPLAN.md` §4.1 为唯一来源；跨线契约与证据以 `docs/transport-contract.md` 为唯一通道。每小时复核必须同步更新两张表并补足 READY。

## 原目标与现状

- 宿主：`PLAN.md` §0/§1，Go、内核零业务、能力可替换、工具为模型设计、接管真实任务。
- 界面：`UIPLAN.md` 用户原话/§2.10 与 `../dsh-ui/PLAN.md` §8，Viva 原生、根本依赖先行、逐部件插件化；Web 仅可关闭过渡件。
- 两仓已有大量实现，不是尚未开工，也不是整产品已验收。历史 ✅、截图、ACP/mockd、handler 单测各自只证明一层。
- 并行会话正在推进 H13 审批与 H14 契约迁移；开工先看两仓最新提交、工作树和测试，不回滚他人成果，不按本页旧描述重复实现。


## 2026-11-04 收尾状态（给下一个接手的人）

- **已发版**：`v0.2.1-go.30`（release 页有 3 个平台二进制 + SHA256SUMS）。
  go.29 的主线是上下文预算；**go.30 的主线是静默失败与"只看 text"**：
  审计 36 条路径、22 条真缺陷已修；七处投影统一判据；新增 `scripts/text-only-guard.py` 门禁
  （7 处历史形状逐条造红，白名单 13 条各带理由）。
- **工具表**：默认 `compact`，**57 个工具**，真网关实测 **4,972 token = 预算的 82.9%**（上限 6,000）。
  （工具数从 43 涨到 57，但压表改进把它整体压下来了：43 个时是 5,158 —— 所以"加工具必然涨预算"不成立，
  取决于每个工具的 schema 骨架有多啰嗦。**再加 5~6 个大工具就会击穿 6,000**，那时要么开 lazy，要么再压一轮。）
  另有第四档 `--tools-mode lazy`（常驻语料前 30 名 + `tool_load` 按需取，首步 −26.7%），
  **默认仍是 compact**；翻默认还差"换模型家族复测"这一条硬证据。
- **装载集是会话状态**：不落新文件，回放 v4 日志里已有的 `tool/call`（`cmd/dshd/tool_lazy.go`）。
- **思考块覆盖**：`history` 原始帧 / `load` / `history_search` / 子代理转写 / `fork` / ACP `load` / `Meter.Estimate`
  七处投影现在都认 reasoning（同一个病"只看 text"今天清完）。
- **静默成功审计**：`docs/silent-success-audit.md` 36 条路径全表；真缺陷已修 18 条
  （含空流/半截流、压缩空摘要、`200+result:null`、journal 补洞缺口、`turn/end` 恒 completed、
  `CompactSchema` 两处删参数、`history_search` 只认正文、`session.load` 丢思考、fork 丢思考）。
- **每个 turn 都有结局**：`newTurnCloser` 统一出口，失败/用满步数/panic 都写 `turn/end{kind:error}`。
- **验证口径**：`go test ./...` 48 包；`scripts/check.sh` ALL GREEN（含 15 条审计守卫）；
  `-race` 全仓 47 包零竞争（`CGO_ENABLED=1 CC=<mingw64 gcc>`）。
- **还没闭环的三条**（接手先看这里）：
  1. `lazy` 翻默认的"换模型家族复测"（本机只有两个 deepseek 通道，`group/kimi-k3.1-pro` 上游是坏的）
  2. `../dsh-ui` 是否开始消费 `transport.Message.reasoning`（宿主已给，UI 快照不追踪字段，无强制同步项）
  3. ~~`Meter.Estimate` 与 ACP `session/load` 的思考块~~ **已修（M378/M379）**：
     Surface 节点带 `Reasoning`，估算不再低估（压缩不会提前触发）；ACP load 只有思考的回答不再消失。
     fork 的 finish/isError/cwd/view/turn-step/developer 消息也已在 M380 沿用源事件（截断的回合不再变成"正常收工"）

## 当前依赖顺序

1. **G0 / H14 契约门**：唯一 owner 冻结 `docs/transport-contract.md`、两边快照与 golden；统一 HTTP 裸参数、`/ws`、帧词表、每会话 journal/epoch 和原始 history frames。真实 apiclient 只对契约夹具做合成重放；不在此阶段提前实现真实 H6/H13。
2. **G1 宿主执行链**：H6 真实 text/reasoning/tool 流；H13 serve 推送、子进程应答、允许执行/拒绝不执行/超时/取消；H8 所有 HTTP 错误出口可独立并行。
3. **G2 跨仓消费**：`../dsh-ui` 用真实 apiclient 接真宿主，完成认证、list/load、发送、取消、续历史、断线恢复和 H1–H13 字段/动作；保留两仓 revision 与真实断言。
4. **U2–U4**：插件授权/禁用/替换/卸载、逐部件体验、完整工作日、同任务 A/B 和整进程树性能。判据见 `UIPLAN.md` §4。

## 并行与安全

- 任务书写明 owner、owned paths、两仓 base revision、依赖、失败测试、验收命令与证据路径；同一文件只给一个写入者。契约 owner 冻结后，宿主/UI 实现才可并行。
- 改前重读，发现并发变化先合并；不得 checkout/reset 覆盖他人，不原地移动 `modules/` 或兄弟仓，不按镜像名杀进程。
- `scripts/regress.sh` 有全局 taskkill，`scripts/check.sh` 原地移动 modules，隔离修复前不得在共享树运行。抽模块、旧代码造红、变异只在独占隔离副本。
- 测试使用临时 home、随机端口、无头窗口和自有 PID；不重启/关闭 DSH 或 magpie，不写 profiles/asar/原始会话。
- commit/PR/push 由任务书授权；两仓 push 均需用户当次确认。

## 验收口径

- 分层记录代码存在、单测、宿主执行链、跨仓联调、产品完成；每次写命令、退出码、真实断言和未验证项，不只写 ALL GREEN。
- `-race` 未成功即未验证；token 估算不是同模型真实分词；宿主资源数字不替代 UI+dshd 整树产品验收。
- 跨仓记录必须含 `dshd_rev`、`ui_rev`、配置、启动/清理命令、真实断言、证据路径和未验项。

版本读源码与 CHANGELOG，发布流程遵循 `UPDATING.md`；只 ack 实际处理完毕的 issue/PR。
