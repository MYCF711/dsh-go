package plugin

// 插件运行时的装配层：把「已安装的私有包」接进内核，并守住 R2。
//
// 为什么这个模块必须存在：
//   - 装载器（`pluginmgr.Loader`）是**有状态**的宿主服务：它握着 wazero 运行时、
//     实例与工具句柄。直接把全局单例塞给调用方就破了 R2（模块只拿 *kernel.Scope）；
//   - 它又必须在作用域撤销时**真的**卸载（A5「卸载即撤销」）：宿主退出/热重载时
//     实例、线性内存、goroutine 都要还回去，而不是等进程结束；
//   - 所以这里是它唯一的落点：`Register(*Scope)` 里创建装载器、经 `kernel.Provide`
//     暴露、`s.Effect` 登记撤销。没有 `init()` 注册，没有包级可变全局。
//
// 两个服务键（都在本文件里定义，不塞进 internal/spi：它们属于插件运行时的
// 实现细节，不是跨模块契约）：
//
//	"pluginloader"  → *pluginmgr.Loader        装/卸/对账的句柄
//	"plugintools"   → spi.ToolRegistry          静态工具 + 插件工具的合成目录
//
// 模型看到的工具表应当是 `plugintools`（而不是 `tools`）：后者只有静态工具，
// 插件包的工具不在里面。接线见 $S/HS-07/WIRING.md。

import (
	"context"

	"dshgo/internal/kernel"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/spi"
)

// LoaderKey 是插件运行时句柄的服务键。
var LoaderKey = kernel.NewKey[*pluginmgr.Loader]("pluginloader")

// TableKey 是"静态工具 + 插件工具"合成目录的服务键。
//
// 为什么与 "tools" 分开：一个能力只允许一个提供者，而 "tools" 已经由
// modules/tools 提供（静态目录 + 工具流水线）。合成目录是**另一个**服务：
// 它包含静态工具、并且随插件装载集合变化。需要看见插件工具的地方（agent 的
// 工具表、`dshd modules` 诊断）注入这一个键。
var TableKey = kernel.NewKey[spi.ToolRegistry]("plugintools")

// Runtime 返回插件运行时的装配模块。
//
// manager 由宿主在装配前显式打开（不读环境变量、不猜用户目录），
// 这里只把它接到内核作用域上：作用域活多久，运行时活多久。
func Runtime(manager *pluginmgr.Manager) kernel.Module {
	return kernel.Module{
		ID:      "plugin-runtime",
		Backend: kernel.BackendStatic,
		// 需要静态目录：合成表的另一半来自 "tools"。没有它，
		// 插件工具会变成"只有插件、没有内置工具"的另一张表。
		Requires: []string{spi.ToolsKey.String()},
		Provides: []string{LoaderKey.String(), TableKey.String()},
		Builtin:  true,
		Register: func(s *kernel.Scope) error {
			inner, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			loader, err := pluginmgr.NewLoader(manager)
			if err != nil {
				return err
			}
			// 先建表再 provide：任何一步失败都不能留下半装配的服务。
			table := loader.Table(inner)
			if err := kernel.Provide(s, LoaderKey, loader); err != nil {
				return err
			}
			if err := kernel.Provide(s, TableKey, spi.ToolRegistry(table)); err != nil {
				return err
			}
			// A5：作用域撤销 → 全部插件模块关闭（实例、线性内存、goroutine）。
			s.Effect(func() { _ = loader.Close(context.Background()) })
			return nil
		},
	}
}
