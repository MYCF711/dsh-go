package timer

// 定时器服务的**内核装配层**（官方 #3 `timer` = `@deepseek-ai/cordis-plugin-timer`）。
//
// 为什么要这一层：`internal/timer` 的实现是真的（`Scope` 的 After/Every/Child/
// Release 都有测试），但在补这一层之前它**零生产 importer**——全仓 grep
// `internal/timer` 只在包内命中，也就是说"随 scope 释放的定时器"这件能力
// 没有任何模块拿得到。官方 #3 的形状是"挂上插件，任何消费者都能
// `ctx.timeout/ctx.interval`"，所以这里补的就是那个**挂载点**：
//
//	"timer" → *timer.Scope     根定时器（随内核作用域释放）
//
// R2 约束照旧：模块只拿 `*kernel.Scope`，经 `kernel.Provide` 暴露，撤销时
// `Release()` 收回全部 timer（Release 会**等到**正在跑的回调结束才返回，
// 所以"释放完成"真的等于"不会再有人碰我的状态"，见 scope.go 的文件注释）。
//
// 官方逐个 API 的对应关系（`@cordisjs/plugin-timer` README "API" 表）：
//
//	ctx.timeout(callback, delay)  → Scope.After
//	ctx.timeout(delay)            → Scope.After + 一个已关闭的 channel（等待型）
//	ctx.interval(callback, delay) → Scope.Every
//	ctx.setTimeout / setInterval  → 上面两个的 deprecated 别名（官方保留，这里也保留）
//	throttle / debounce           → **不在本文件**：那是包装函数语义，不是定时器
//	                                生命周期；需要时在消费者侧用 Every+状态实现，
//	                                 不在这里凭空造一个"看起来像官方"的 API。
//
// 与官方的一个显式差异：官方的句柄是"disposer 函数"，本仓是 `(ID, error)` +
// `Cancel(id)`。`ID` 让"取消一个已经不存在的 timer"可以被回答（`Cancel` 返回
// bool），而 disposer 只能猜。这个差异写在 `Timer` 接口上，不藏。

import (
	"errors"
	"fmt"
	"time"

	"dshgo/internal/kernel"
)

// Key 是定时器服务的服务键。
var Key = kernel.NewKey[*Scope]("timer")

// Module 返回定时器服务的装配模块。
//
// Builtin：定时器是基础设施（消费者按需注入），不是可选的第三方插件。
func Module() kernel.Module {
	return kernel.Module{
		ID:       "timer",
		Backend:  kernel.BackendStatic,
		Provides: []string{Key.String()},
		Builtin:  true,
		Register: func(s *kernel.Scope) error {
			scope := NewScope()
			if err := kernel.Provide(s, Key, scope); err != nil {
				return err
			}
			// 作用域撤销 = 根 scope 释放：所有 timer 停掉，并且**等到**
			// 正在跑的回调退出（Release 的语义，见 scope.go）。
			s.Effect(func() { scope.Release() })
			return nil
		},
	}
}

// Delayed 是官方 `ctx.timeout(delay)` 的宿主形状：等待 delay 之后关闭 channel。
//
// 它返回两个东西：channel（等）和取消函数（不等了就撤销）。取消之后 channel
// **不会**被关闭——那正是"取消成功"的可观测证据（关闭了就等于"到点了"）。
type Delayed struct {
	// Done 在到点后关闭；取消后保持不关闭。
	Done <-chan struct{}
	// Cancel 撤销这次等待；返回是否真的撤掉了（已经到点/已经撤过 → false）。
	Cancel func() bool
}

// Wait 是官方 `ctx.timeout(delay)` 的宿主实现。
//
// 与官方的一个差别必须说清：官方返回 `Promise<void>`，本仓返回 channel +
// 取消函数。理由与 ID/Cancel 相同——取消一个等待必须能被观察，而 Go 的
// Promise 等价物（channel）没有"被取消"这个状态。
func (s *Scope) Wait(delay time.Duration) (*Delayed, error) {
	if s == nil {
		return nil, fmt.Errorf("timer: nil scope（装配时没有注入 timer 服务）")
	}
	done := make(chan struct{})
	id, err := s.After(delay, func() { close(done) })
	if err != nil {
		return nil, err
	}
	return &Delayed{Done: done, Cancel: func() bool { return s.Cancel(id) }}, nil
}

// ErrClosed 是"这个 scope 已经释放"。对外暴露成 errors.Is 友好的判据，
// 让消费者不用依赖内部错误变量。
var ErrClosed = ErrScopeReleased

// IsClosed 报告"这次失败是因为 scope 已释放"。
func IsClosed(err error) bool { return errors.Is(err, ErrScopeReleased) }
