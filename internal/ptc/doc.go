// Package ptc 是 programmatic tool calling 的**执行缝**（service seam），
// 对应官方 `@deepseek-ai/dsh-ptc-runtime`。
//
// # 它是什么（一字不改地照官方定性）
//
// 官方 README 原文：本包是 "Service Definition role of the PTC execution
// capability seam"：一个抽象的 `PtcRuntime` 服务（注册成 `ctx.ptcRuntime`），
// 加一份 provider 与消费者共享的词汇。**本包一行执行代码都没有**：
// `src/index.ts` 只有抽象类与三组保留名集合，`src/types.ts` 只有类型。
//
// 真正跑程序的是 provider：官方的 `@deepseek-ai/dsh-ptc-runtime-node`
// （Node 专属实现，`evaluators/vm-script.ts` 用 `node:vm`）。官方 base 组合里
// `cordis.patch.yml:407-408` 的 `id: ptc-runtime` 挂的**就是它**。
//
// # 为什么这个包能在本仓实现而不撞 R6
//
// R6 是"宿主**不嵌** JS 运行时"，不是"宿主不许有 PTC"。按同一个词面：
//
//   - 本包是纯 Go 契约（类型 + 校验 + 抽象接口），不含任何 JS 引擎；
//   - provider 走**外部受管 Node 进程**，与既有裁定 `dynamicCordisRunner`
//     （Go 核心 + 外部受管 Node 双轨，`internal/cordis/extern` 已落地）同一形状：
//     宿主只写一段 JS **文本**（当数据文件），由用户机器上的 node 执行，
//     进程受管、可杀、有超时、可取消。
//
// 所以"R6 ⇒ PTC 永久不做"这条旧推论是**过度外推**：它把"不许嵌引擎"读成了
// "不许起子进程"。真正被 R6 挡住的是把引擎塞进宿主二进制。
//
// # 本仓已到达的能力边界（如实写在代码里，不写进 README 就完事）
//
//   - 只实现 per-call（`function-body`）一种求值形态；`repl` / `script` 需要
//     `node:vm` 的保留环境，尚未实现，`Resolve` 会明确拒绝而不是假装支持；
//   - 不做 OS 文件沙盒：`SandboxMode()` 报"无 confinement"，显式档位策略被
//     拒绝（官方原文："Providers without confinement reject an explicit policy"）；
//   - 因此这个 provider 是**可选、显式开启**的（见 `modules/tool/runcode`），
//     默认形状与官方 `dsh-base/cordis.patch.yml:523` 的 `disabled: true` 一致。
package ptc
