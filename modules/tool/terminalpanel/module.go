package terminalpanel

// 模块装配：八个终端工具进工具目录。
//
//	terminal_open                      agent 开一条 origin=agent 的终端（open.go）
//	terminal_send/read/signal/close/list  agent 与这条终端交互（interact.go）
//	user_terminal_list/read            读用户控制的两条只读工具（panel.go）
//
// `terminal_open` 属于官方另一个包（`@deepseek-ai/dsh-tool-terminal`），
// 它**不**受本包 `readUserTerminals` 管辖；理由写在 Register 里。
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
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			p := &panel{cfg: cfg, scope: s}
			// `terminal_open` **总是注册**：它属于官方 `@deepseek-ai/dsh-tool-terminal`
			// （`tool-catalog.md:36`），不受 `readUserTerminals` 管辖——那个开关只管
			// 官方 `dsh-terminal-panel` 的两个用户终端工具（`terminal-panel/src/index.ts:39`）。
			// 把它也一起关掉会让"agent 开终端"这条链在 readUserTerminals:false 的
			// 部署里静默消失，那不是官方语义。
			if err := reg.Register(p.openTool()); err != nil {
				return fmt.Errorf("注册 %s 失败: %w", p.openTool().Name(), err)
			}
			// `terminal_send/read/signal/close/list` 与 `terminal_open` 同属官方
			// `@deepseek-ai/dsh-tool-terminal` 的六件套，同样不受 readUserTerminals 管辖：
			// 开得出来却打不进去/读不回来，等于开了一条只能用一次的终端。
			for _, tool := range p.interactTools() {
				if err := reg.Register(tool); err != nil {
					return fmt.Errorf("注册 %s 失败: %w", tool.Name(), err)
				}
			}
			// 官方：readUserTerminals=false 时两个用户终端工具都**不注册**（removes both）。
			// 注册成"永远报错"比不注册更糟：模型会反复试一个不存在的能力。
			if !cfg.ReadUserTerminals {
				return nil
			}
			for _, tool := range []spi.Tool{p.listTool(), p.readTool()} {
				if err := reg.Register(tool); err != nil {
					return fmt.Errorf("注册 %s 失败: %w", tool.Name(), err)
				}
			}
			return nil
		},
	}
}

// listTool / readTool / openTool 造三个工具（`panel` 的构造点集中在 module 与测试里）。
func (p *panel) listTool() *userTerminalList { return &userTerminalList{p: p} }
func (p *panel) readTool() *userTerminalRead { return &userTerminalRead{p: p} }
func (p *panel) openTool() *terminalOpen     { return &terminalOpen{p: p} }

// interactTools 造 `terminal_send/read/signal/close/list` 五个交互工具。
//
// 顺序照官方注册顺序（tool-terminal/src/index.ts:202-408：send、read、signal、close、list），
// 工具表的顺序是稳定性契约（快照里看得出来），所以不按字母排。
func (p *panel) interactTools() []spi.Tool {
	return []spi.Tool{
		&terminalSendTool{p: p},
		&terminalReadTool{p: p},
		&terminalSignalTool{p: p},
		&terminalCloseTool{p: p},
		&terminalListTool{p: p},
	}
}
