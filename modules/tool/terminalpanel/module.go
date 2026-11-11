package terminalpanel

// 模块装配：把两个只读工具注册进工具目录。
//
// # 为什么不在这里 Inject 终端数据源
//
// 别的工具（history/subagent/goal）在 Register 里 `kernel.Inject` 一下就拿到服务，
// 因为它们的提供者也在模块列表里。终端数据源不是：
//
//	cmd/dshd 的模块列表（`cmd/dshd/main.go` 的 `hostModules`）在进程启动时装配，
//	而持有真 roster 的终端控制器（`cmd/dshd/terminal_pty.go` 的
//	`officialPTYTerminalAPI` / `transport.OfficialJobTerminal`）在 **serve 起来之后**
//	才构造。Register 时那个键还不存在，硬 Inject 只会让整个宿主起不来。
//
// 所以数据源走**惰性解析**：`panel.source()` 在每次调用时 `kernel.Inject`。
// 好处是失败被推迟到"真的要用"那一刻，并且能给出结构化说明（缺什么、为什么、
// 下一步该找谁）；坏处是"没有控制器的入口（headless / `dshd tools`）里工具仍在
// 目录上"——这一点在报告里写明，等 serve 的接线落地后由宿主决定是否按能力裁剪。

import (
	"fmt"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// Module 返回装配用的模块描述。
//
// cfg 请从 `DefaultConfig()` 出发改；零值 Config 会因配置非法在 Register 中失败
// （不是静默变成"没有工具的模块"）。
func Module(cfg Config) kernel.Module {
	return kernel.Module{
		ID:       ModuleID,
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			if err := cfg.validate(); err != nil {
				return fmt.Errorf("tool-terminalpanel: %w", err)
			}
			// 官方：readUserTerminals=false 时两个工具都**不注册**（removes both）。
			// 注册成"永远报错"比不注册更糟：模型会反复试一个不存在的能力。
			if !cfg.ReadUserTerminals {
				return nil
			}
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			p := &panel{cfg: cfg, scope: s}
			for _, tool := range []spi.Tool{p.listTool(), p.readTool()} {
				if err := reg.Register(tool); err != nil {
					return fmt.Errorf("注册 %s 失败: %w", tool.Name(), err)
				}
			}
			return nil
		},
	}
}

// listTool / readTool 造两个工具（`panel` 的构造点集中在 module 与测试里）。
func (p *panel) listTool() *userTerminalList { return &userTerminalList{p: p} }
func (p *panel) readTool() *userTerminalRead { return &userTerminalRead{p: p} }
