# 定时方向与派单复核 · 2026-11-03 18:08–18:28

> 两个只读子代理并行复核宿主与界面，Lead 同时核对任务表；完成方主动通知，无 wait/poll。未启动服务、未重跑模型/真机、未修改实现。以下是**现有证据与源码复核**，不是新一轮 E2E。

## 最终基线

- 宿主：`b6d1b10`（M284）。
- 界面：`125e533`；其后工作树有 epoch/reset 改动，**未提交、未验，不计完成证据**。
- 主要证据：`docs/transport-contract.md`、`docs/host-side-closeout.md`、`../dsh-ui/docs/product-acceptance.md`、`docs/reconnect.md`、最近提交与相关源码/测试。

## 方向结论

1. **未退回 Electron/WebView/旧 TypeScript 路线**；Go 宿主与 Viva 原生界面方向保持。
2. **主体进展真实存在**：宿主 journal/审批/思考与通用信封有测试和真机记录；UI 已有裸参数、`/ws`、新帧、真实 text/reasoning/tool、allow/deny、取消、多会话，以及 `0ee04cd` 的原始 history 补洞。
3. **完成声明曾超过证据范围**：独立源码复核发现 serve 跨轮重复/debug 旁路、H8 stale_epoch 缺 retryable、H13 pending cancel 未实现，因此 W1/W3/W5 已从 DONE 撤回为 IN_PROGRESS，W4 不得签 DONE。
4. **H14 尚未跨仓闭环**：UI committed snapshot 仍是 `/api/mux`、旧 `kind` 和 history messages；History 请求还需带 epoch，缺共享 golden、完整 reset/stale_epoch 与宿主重启 cookie 证据。工作树 epoch/reset 不计完成。`product-acceptance.md` #20/§三.2 仍写“验证中”，与已提交 reconnect 报告冲突，应由 UI Lead 改成“链路断可重连；宿主重启不能自愈”。
5. **U1.5 没有两尺寸完成证据**：未找到当前 rev 的 1920×1032、1182×832 对拍与逐容器 ≤8px 表；V1–V4A 不得标 DONE。

## Lead 与并行行为

- 可见并行记录：UI `acb34c7` 写明同一轮并行子代理，`a3bd655` 为三条并行子任务合并，progress 有子代理 A/B/C。
- 本轮也实际并行委派两个只读子代理并收到主动通知；未发现明确 wait/poll 证据。
- 宿主最近提交未记录派工方式，只能标“派工记录缺失/未验”，不能猜 Lead 自行串行。
- 任务表中 V1–V4A、U2、U4P、PERF 等 READY 暂无可见 owner/派出回执：若已外派必须补 IN_PROGRESS+owner；若未外派则同轮齐派，不能由 Lead 接包自行实现。

## 已更新的派单与证据文档

### PLAN §14.1

- W0/W2/W7：IN_PROGRESS，记录快照/golden/epoch/reset/宿主重启等剩余缺口。
- W1/W3：IN_PROGRESS，明确 serve 零水位重复、debug 绕过 journal、跨轮去重与恢复缺口。
- W4：IN_PROGRESS，缺 cancel→拒绝+不执行+approval/cancel+第二轮 prompt。
- W5：IN_PROGRESS，stale_epoch 特殊错误体补 retryable 与四字段测试。
- W6：DONE 仅指本复核，不覆盖之后新代码。
- W8/W9：READY，同轮外派 A/B 与性能缺口设计。

### UIPLAN §4.1

- U0/U1-H6/U1-H13：IN_PROGRESS，保留已有证据并记录剩余条件。
- V1/V2/V3/V4A/U2/U4P/PERF：READY，无路径重叠时同轮全部派出。
- V4B：BLOCKED，等 `cmd/app/main.go` 当前 owner 主动通知释放，避免并行抢写。
- U2 扩展至 `internal/uikernel/**`、`internal/uiplugins/**`、`pluginapi/**`；V1 明确为 `dsconversation+dstopbar`。
- R0：DONE 仅指本报告；共享 progress/官方规格/任务表由 Lead 串行回填。

### 跨线证据

- `docs/transport-contract.md`：H6/H8/H13/H14 按源码反证降级；H1 补 UI 真机数字证据。
- `docs/h13-approval.md`、`docs/host-side-closeout.md`：补 pending cancel、serve、stale_epoch 等本仓剩余项，不再宣称宿主全部完成。

## 当前可并行 READY

- 宿主：W8（10 任务 A/B 设计）、W9（性能缺口测量设计）。
- 界面：V1、V2、V3、V4A、U2、U4P、PERF。
- 正在修改的 epoch/mockd/main.go 不重复派；已有 W1/W3/W4/W5 owner 不重复派。

## 未验证/未闭环

完整工作日、10 任务 A/B、宿主重启 cookie 自愈、完整 stale_epoch/reset、共享 golden、serve 跨轮去重与 debug 统一、pending cancel、stale_epoch 四字段、进程内 A→B→A 连续流、U1.5 两尺寸/≤8px、`-race`（本机无 C 工具链）。
