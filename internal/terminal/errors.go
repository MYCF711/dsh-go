package terminal

import "errors"

// 稳定哨兵。调用方按类别判定，**不靠错误字符串猜**：
//
//   - ErrUnsupported → 本平台没有 PTY 实现（官方形状：501 not_implemented）；
//   - ErrStart       → 配置/启动失败（参数错或进程起不来）；
//   - ErrBadSize     → 尺寸非法（调用方传了 0/负数）；
//   - ErrClosed      → 会话已被关闭；
//   - ErrExited      → 进程已退出（自然退出或被终止）。
var (
	// ErrUnsupported 表示本平台/本机没有可用的伪终端实现。
	// 这是**能力缺失**，不是环境错误：调用方必须原样暴露，禁止降级成管道冒充。
	ErrUnsupported = errors.New("本平台没有可用的伪终端（PTY）实现")
	// ErrStart 表示 PTY 会话启动失败（配置非法或子进程起不来）。
	ErrStart = errors.New("PTY 启动失败")
	// ErrBadSize 表示行列尺寸非法。
	ErrBadSize = errors.New("PTY 尺寸不合法")
	// ErrClosed 表示会话已经关闭。
	ErrClosed = errors.New("终端已经关闭")
	// ErrExited 表示子进程已经退出。
	ErrExited = errors.New("终端进程已退出")
)
