package mcp

import (
	"context"
	"fmt"
)

// Controller 是宿主侧为 `mcpManager.restart` 提供的唯一动作面。
//
// 它只有五种动作：判断是否已释放、判断条目在不在、判断条目是否启用、
// 收掉旧一代（并回报清理是否确认）、挂起新一代。restart 的全部决策都写在
// 本包（Restart），宿主只提供真实进程动作——这样"什么时候不许替换"
// 只有一份实现，不会在宿主各处抄出不同版本。
type Controller interface {
	// Disposed 报告管理器是否已经释放。
	Disposed() bool
	// Exists 报告 name 是否仍是配置项或注册项。
	Exists(name string) bool
	// Enabled 报告条目是否要求一个在跑的实例。
	Enabled(name string) bool
	// Stop 收掉活着的实例（没有也算成功），返回清理结果与"本地清理是否完成"。
	// 第二个返回值为 false 时，替换必须被拒绝（官方 unclosed-generation 语义）。
	Stop(name string) (CloseResult, bool)
	// Mount 挂起新一代实例；失败原样返回。
	Mount(name string) error
	// Publish 发布一次状态（用于把清理失败摊在管理面上）。
	Publish(name string, status Status) error
}

// Restart 是官方 `mcpManager.restart` 的实现：
// 从当前条目重建一个 server——dispose 活着的实例（如果有），再挂一个全新的，
// 带上全新的重连预算。**清理未确认就禁止替换**，直到宿主重启
// （dsh-mcp-manager/src/index.ts:347-355 与 remount:481-493）。
func Restart(ctx context.Context, ctrl Controller, name string) error {
	if ctrl == nil {
		return fmt.Errorf("restart: 缺 Controller")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ctrl.Disposed() {
		return ErrDisposed
	}
	if !ctrl.Exists(name) {
		return UnknownServer(name)
	}

	closed, cleanupComplete := ctrl.Stop(name)
	if !cleanupComplete {
		// 把清理结果发布出去：管理面要能看见"为什么不能替换"，而不是只拿到一句错误。
		// Publish 失败不掩盖主错误（主错误更可操作）。
		_ = ctrl.Publish(name, Status{State: StateFailed, Reason: ReasonCleanup, Close: closed})
		return CleanupIncomplete(name)
	}
	if ctrl.Disposed() {
		return ErrDisposed
	}
	if !ctrl.Enabled(name) {
		// 禁用条目：旧一代照样收掉，新一代不挂。restart 本身不是错误。
		return ctrl.Publish(name, Status{State: StateDisabled})
	}
	return ctrl.Mount(name)
}
