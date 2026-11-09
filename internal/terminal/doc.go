// Package terminal 是**真伪终端（PTY）**核心：在 Windows 上用 ConPTY 承载一条
// 交互式 shell 进程，把它当成"屏幕 + 键盘"来用。
//
// # 为什么不能用管道冒充
//
// 管道式 shell（`bash -s` + stdin/stdout 管道）能跑命令，但它**不是终端**：
//
//   - 子进程的 stdin 不是 tty，`isatty` 为假，行编辑/补全/`stty` 全部不工作；
//   - 没有窗口尺寸，`resize` 无处可去（没有 SIGWINCH / 没有控制台缓冲）；
//   - 输出没有屏幕模型，也没有滚动缓冲，`screen` 只能是"原始字节尾部"；
//   - 没有会话/作业控制，Ctrl-C 之类的中断字符不是终端语义。
//
// 所以本包只做真东西：ConPTY（`CreatePseudoConsole` + `PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE`
// 启动子进程），resize 走 `ResizePseudoConsole`，退出码走真实进程句柄。
//
// # 平台矩阵
//
//	Windows 10 1809+ / Server 2019+   ConPTY，全能力
//	其它平台                          如实返回 ErrUnsupported（调用方翻成官方 501）
//
// **没有实现就说没有**：非 Windows 不会退化成管道实现来假装能用。
//
// # 归属
//
// 本包只负责一个 PTY 会话的字节流、尺寸、退出码与**有界**滚动缓冲。
// 进程树归属（Job Object / 进程组）由执行层（modules/exec/bash）的 processControl
// 负责，它通过 Kill/Close 两个方法接管本包启动的树。
package terminal
