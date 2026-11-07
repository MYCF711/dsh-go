# TUI placement 真实 ConPTY 红证据（供 W134 接线）

来源：W132 隔离构建 + 当前 backend + `dsh-0.2.2-go.1.exe` 的真实 ConPTY attempt 02。

- `src/dsh-adapter/channel/input-actions.ts:73` 普通输入固定 `dispatchUserText(trimmed, 'followup', ...)`。
- 当前 backend 仍只接受 `placement === 'turn'`，普通 prompt 返回 `{accepted:false,dropped:true}`，TUI 显示 `Held · not delivered`。
- 同一运行中模型选择器已真实选中 Kimi，`state/session-model.json` 持久化为 `group/kimi-k3.1-pro`；网关 `/v1/models` HTTP 200。
- prompt 未送达，trace=0 turns/0 rows，导出只有头部；这些不是网关故障，也不能把空导出冒充回合。

因此 backend placement 必须优先接 followup，再完成 queue/pending/cancel 映射；普通输入不是 turn-only 路径。W134 最终验收必须包含普通 prompt 不再 dropped 的测试，真实 ConPTY由Lead/W132复验。
