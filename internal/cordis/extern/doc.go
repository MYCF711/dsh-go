// Package extern 在**宿主二进制不包含任何 JS 引擎**的前提下，把 Cordis 插件
// 的 JS 半边交给一个**外部受管 Node 进程**执行。
//
// # 为什么是外部进程（与 R6 的关系）
//
// R6 禁止宿主"嵌 JS 运行时"。本包不嵌：
//
//   - 宿主是纯 Go 二进制，`go list -deps ./internal/cordis/extern` 里没有任何
//     JS 引擎（无 goja/otto/v8/quickjs），也不引入 cgo；
//   - JS 由**用户机器上已有的 `node`** 承担，是独立子进程，宿主只通过
//     stdin/stdout 说话；
//   - 子进程挂在 Windows Job Object 上（`KILL_ON_JOB_CLOSE`），宿主退出/取消时
//     整棵树被回收，不留孤儿；
//   - 单次调用有超时与取消，超时只影响那一次调用，不污染进程。
//
// # 非官方 wire（必须写清）
//
// 官方 Cordis 是**进程内**调用，没有可抄的 RPC wire。本包定义的协议是**我们
// 自己的**，不得称为"官方 wire"，也不得据此声称与官方 wire 兼容：
//
//	传输：newline-delimited JSON（一行一帧），stdin 请求、stdout 响应；
//	      stderr 只放日志，永不混进协议流。
//	请求：{"jsonrpc":"2.0","id":<uint64>,"method":"<string>","params":<json|null>}
//	响应：{"jsonrpc":"2.0","id":<uint64>,"result":<json>}
//	     {"jsonrpc":"2.0","id":<uint64>,"error":{"code":<int>,"message":"...","data":<json>}}
//	错误码：-32700 解析失败 / -32601 方法不存在 / -32603 内部错误 /
//	        -32000 插件抛错（data.kind="plugin-error"）。
//
// # 无 node 时的响亮降级
//
// 找不到或跑不了 `node` 时，`Start` 返回 `*UnavailableError`（`Code` 为
// `cordis/node-missing` 或 `cordis/node-unusable`，带可执行 `Hint`）。调用方
// 必须把它当成"这条能力当前不可用"显式告知用户，**不得**静默回空成功、也不得
// 用 Go 原生内核假装 JS 插件已经跑起来了。
package extern
