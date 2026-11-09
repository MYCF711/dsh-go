// Package actioncards 把官方 `propose_action` 工具注册进宿主工具表。
//
// kind（动作卡类型）不在这个模块里内置：官方 actionCards 是宿主服务，
// 具体 kind 由实验包注册。本仓目前没有实验包，所以注册表初始为空；
// 没有 kind 时 propose_action 会按官方语义拒绝未知 action，而不是静态成功。
package actioncards

import (
	"dshgo/internal/actioncards"
	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// Module 注册 propose_action 工具。
func Module(home string) kernel.Module {
	return kernel.Module{
		ID:       "tool-actioncards",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			return reg.Register(actioncards.Open(home).Tool())
		},
	}
}
