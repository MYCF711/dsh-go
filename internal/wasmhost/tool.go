package wasmhost

// 工具路由：一个 wasm 模块可以挂多个工具，客人按请求信封里的 `tool` 字段分流
// （docs/wasm-tool-abi.md §3.2）。
//
// 为什么路由必须带锁：`SetToolName` 在 abi 层是**模块级状态**，没有 per-call
// 参数——"写名字 + 调用"必须原子。两个工具并发时，A 写完名字、B 又写一遍，
// A 就会以 B 的身份跑起来（输出看起来正常，只是答非所问，最难查）。
//
// 这把锁的代价是零：一个实例本来就只能跑一个 call（同步导出不能重入），
// 同一个模块上的两个工具本来也得串行；锁只是把"串行"从实例扩到"写名字 + 调用"。

import (
	"context"
	"errors"
	"time"
)

// ToolRouter 是一个已装载模块的调用路由。
type ToolRouter struct {
	mod *Module
	mu  chan struct{} // 容量 1 的互斥量；不用 sync.Mutex 是为了让 Invoke 支持 ctx 取消等待
}

// NewToolRouter 在已装载模块上建一个路由。
func NewToolRouter(m *Module) *ToolRouter {
	router := &ToolRouter{mod: m, mu: make(chan struct{}, 1)}
	router.mu <- struct{}{}
	return router
}

// Invoke 在锁里写工具名，并按**这个工具**自己的超时调一次客人。
//
// 超时在这里再收一次：模块级 SetTimeout 取的是全部工具里最大的那个，
// 超时短的工具必须自己把 ctx 卡紧，否则 manifest 写的 timeout_ms 就成了摆设。
func (r *ToolRouter) Invoke(ctx context.Context, tool string, timeout time.Duration, args []byte) ([]byte, error) {
	if r == nil || r.mod == nil {
		return nil, &ProtocolError{Step: "call", Reason: "没有已装载的模块：路由建在 nil 上（宿主侧装配错误）"}
	}
	select {
	case <-r.mu:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { r.mu <- struct{}{} }()

	r.mod.SetToolName(tool)
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return r.mod.InvokeTool(ctx, args)
}

// Exports 返回模块导出的函数名（路由不改变导出表，只是省掉调用方拿模块指针）。
func (r *ToolRouter) Exports() []string {
	if r == nil || r.mod == nil {
		return nil
	}
	return r.mod.Exports()
}

// HintForError 把 wasmhost 的错误翻成模型能执行的下一步。
//
// 为什么不用一句话概括所有错误：这四类的**正确反应完全不同**——
// trap 要换做法、超时缩小输入、取消重发、协议错只能报人。
// 给错方向比不给更糟：模型会拿着一句"重试"把同一个坑踩三遍。
//
// 与 cmd/dshd/wasmload.go 的 hintForWasmError 同源；那份在 package main 里，
// 本轮写作用域不含它，所以这里是装载层的规范实现（两份都在测试里钉住文案）。
func HintForError(err error) string {
	switch {
	case errors.Is(err, ErrGuestTrap):
		return "插件内部崩溃（wasm trap），不是你的参数错了：不要原样重试；换一种做法，或改用别的工具"
	case errors.Is(err, context.DeadlineExceeded):
		return "插件执行超时：输入可能太大，缩小范围再试；连续超时要报告给用户" +
			"（超时后这个实例已被关闭，下一次调用会自动重建，不用重启宿主）"
	case errors.Is(err, context.Canceled):
		return "这次调用被取消（会话被中断）：需要时重新发起，不要把它当成插件失败"
	case errors.Is(err, ErrProtocol):
		return "插件与宿主的 ABI 对不上（宿主侧问题）：不要重试，把这条错误报告给用户"
	default:
		return "wasm 插件调用失败：先按错误原文判断，别反复重试同样的参数"
	}
}
