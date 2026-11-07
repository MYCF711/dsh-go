# Go 宿主插件运行时方向：吸收另一会话的架构证据

> 来源：`session-191da5ac-bc07-489d-a219-4aa5c87379a1` 的插件架构讨论与实测记录；Lead 已核对当前 `internal/kernel/module.go`。内存数字来自该会话，Lead 本次未独立复跑，不能当作新的性能门禁。

## 结论

1. **必备、常驻、关闭即不可用的能力继续 static。**它们共享一份 Go runtime 与 GC 堆，是最低占用路径。
2. **第三方、重依赖、需要崩溃隔离或独立发布的能力走 `BackendProcess`（go-plugin）。**独立 exe 的价值是隔离和分发，不是省内存。
3. **纯逻辑、频繁替换的小插件可走 WASM。**按 PLAN §3.1 的约束推进，不把类型常量当成实现完成。
4. **内置 `enabled=false` 是能力开关，不是内存优化手段。**当前代码确实跳过装配（`internal/kernel/module.go:156-161`），但另一会话实测工具表 57→2 时 WorkingSet 11.0→11.1 MB、Private 45.4→45.2 MB，差异是噪声级。
5. **“每个用户自己编译一份插件 exe”不是默认分发路线。**优先插件作者预编译、版本化分发；声明式配置与 WASM再覆盖不写 Go 的用户，避免工具链依赖和不可复现的本地构建。

## 当前真实边界

- `BackendStatic / BackendWasm / BackendProcess` 常量存在，但 `implementedBackends` 当前只接受 static/wasm；`process` 会 fail-loud，不会静默降级（`module.go:148-153`）。
- 不能宣称 go-plugin 装载器、第三方进程隔离或插件打包器已经实现。
- 官方 dsc 的 `plugins/<name>/<name>.exe` 与 go-plugin 可作为后续参考，不表示本仓已接通。
- 内存优化应走真实 profile、GC/缓冲/连接与长时工作集数据，不靠“少装几个 static 模块”猜。

## 后续门槛

若后续立项 `BackendProcess`，必须单独任务书覆盖：handshake/版本协商、生命周期与健康检查、RPC 背压与请求隔离、依赖自包含、账号作用域、崩溃不传染、进程回收、真实插件 A/B 与内存/启动开销。不得一次性把全部内置模块改成独立进程。
