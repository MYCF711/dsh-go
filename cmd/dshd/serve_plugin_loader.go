package main

// 插件运行时的**生产装配点**。
//
// 这一层只做三件事，都是"接线"而不是"实现"：
//
//  1. 在显式 home 下打开插件管理器，并挂上运行时装载器（`pluginmgr.NewLoader`）；
//     挂上之后，既有 `plugin.*` Transport 方法（enable/disable/uninstall/...）
//     自动装载/卸载——**不需要改 serve_plugins.go**，因为状态与运行时的同步
//     写在 Manager 里（见 internal/pluginmgr/manager.go 的 setEnabled / Uninstall）；
//  2. 把 `plugintools` 合成目录（静态工具 + 插件工具）交给需要"看见插件工具"的
//     地方（agent 的工具表、`dshd` 诊断）。这是 serve.go 里唯一需要改的一行，
//     见 $S/HS-07/WIRING.md；
//  3. 启动时 `Sync` 一次：state.json 说 enabled 的包必须真的在跑，不许出现
//     "配置开着、工具表里没有"。
//
// 为什么不在包初始化时做（`init()`）：R2 禁止模块经全局单例拿依赖，而且
// `init()` 里的失败没人能接（进程直接死，连四字段错误都没有）。这里的每个入口
// 都是显式调用、显式返回错误。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/spi"
)

// pluginRuntime 是"插件管理器 + 运行时装载器"的宿主句柄。
type pluginRuntime struct {
	manager *pluginmgr.Manager
	loader  *pluginmgr.Loader
	// hmr 是插件热重载引擎。**默认关闭**：零值 HMRConfig 即 disabled，
	// Enabled()==false 时不要 Run（Run 会直接回 ErrHMROff）。
	hmr *pluginmgr.HMR
}

// newPluginRuntime 在已打开的管理器上挂运行时。manager 必须非 nil：
// 插件运行时没有"默认 home"，也没有环境变量兜底（那会把测试写到真实用户目录）。
//
// HMR 在这里**恒为关闭**：装配点（serve.go）只有在显式 `DSHD_PLUGIN_HMR=1`
// 时才走 newPluginRuntimeWithHMR。默认路径下 Enabled() 恒 false。
func newPluginRuntime(manager *pluginmgr.Manager) (*pluginRuntime, error) {
	return newPluginRuntimeWithHMR(manager, pluginmgr.HMRConfig{})
}

// newPluginRuntimeWithHMR 是带热重载配置的构造函数。hmrConfig 必须显式传入
// （零值 = 关闭）：没有"缺省打开"的分支。
func newPluginRuntimeWithHMR(manager *pluginmgr.Manager, hmrConfig pluginmgr.HMRConfig) (*pluginRuntime, error) {
	if manager == nil {
		return nil, fmt.Errorf("插件运行时需要已打开的管理器（manager 不能为空）")
	}
	loader, err := pluginmgr.NewLoader(manager)
	if err != nil {
		return nil, err
	}
	hmr, err := pluginmgr.NewHMR(loader, hmrConfig)
	if err != nil {
		return nil, err
	}
	return &pluginRuntime{manager: manager, loader: loader, hmr: hmr}, nil
}

// pluginHMRConfig 返回插件热重载配置。**默认关闭**；只有 `DSHD_PLUGIN_HMR`
// 显式为真（1/true/yes/on）才构造开启配置。
//
// 为什么必须是显式动作：热重载是"改一个文件就让运行中的插件集合变化"的能力。
// 本机历史上"整树热重载"把 idle 子代理的 inbox 投影冲掉、宿主直接 exit(1)
// （$DSH_HOME/AGENTS.md 铁律 3）。所以要开只能有人明确写下一个开关，不存在
// "为了方便默认打开"的分支。
//
// root 是插件管理器根（`<home>/plugins`），不是 home 本身；HMR 只扫
// `<root>/packages/<id>/<version>/plugin.json`，绝不写任何文件。
func pluginHMRConfig(root string) pluginmgr.HMRConfig {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DSHD_PLUGIN_HMR"))) {
	case "1", "true", "yes", "on":
		return pluginmgr.HMRConfig{
			Enabled:  true,
			Root:     root,
			Interval: 2 * time.Second,
		}
	default:
		return pluginmgr.HMRConfig{}
	}
}

// openPluginRuntime 在显式 home 的 plugins 子目录下打开运行时。
//
// 与 serve.go 里既有的 `pluginmgr.Open(filepath.Join(*home, "plugins"))` 同一个
// 目录：装到哪、就从哪装。home 为空是配置错误，不是"用默认值"。
func openPluginRuntime(home string) (*pluginRuntime, error) {
	if strings.TrimSpace(home) == "" {
		return nil, fmt.Errorf("插件运行时需要显式 home（--home / DSHD_HOME）：不读环境变量默认值，也不猜用户目录")
	}
	manager, err := pluginmgr.Open(filepath.Join(home, "plugins"))
	if err != nil {
		return nil, err
	}
	return newPluginRuntime(manager)
}

// Manager 暴露管理器给既有的 HTTP/CLI 适配器（它们的状态变更会自动驱动装载）。
func (r *pluginRuntime) Manager() *pluginmgr.Manager { return r.manager }

// HMR 是只读句柄；Enabled()==false 时不要 Run。
func (r *pluginRuntime) HMR() *pluginmgr.HMR { return r.hmr }

// Table 返回"静态工具 + 插件工具"的合成目录。
//
// inner 是 modules/tools 提供的静态目录（可为 nil：只暴露插件工具）。
func (r *pluginRuntime) Table(inner spi.ToolRegistry) spi.ToolRegistry {
	return r.loader.Table(inner)
}

// Sync 让运行时与已安装状态对齐（启动路径）。返回对齐后的装载集合。
func (r *pluginRuntime) Sync(ctx context.Context) ([]pluginmgr.LoadedPlugin, error) {
	return r.loader.SyncAll(ctx)
}

// Loaded 返回当前装载集合（诊断用）。
func (r *pluginRuntime) Loaded() []pluginmgr.LoadedPlugin { return r.loader.Loaded() }

// Call 是"真实工具调用"的宿主入口：按名字查到插件工具并执行它。
//
// 为什么留这个入口而不是让调用方自己拿表：装载/卸载是并发的，调用方拿到的
// 表可能是旧的；这里每次现查，禁用之后同一个名字立刻查不到。
func (r *pluginRuntime) Call(ctx context.Context, tool string, args json.RawMessage) (spi.Result, error) {
	table := r.loader.Table(nil)
	entry, ok := table.Get(tool)
	if !ok {
		return spi.Result{
			Hint: "先 plugin.install 装包、再 plugin.enable 启用；plugin.list 看已安装的包与启用位",
		}, fmt.Errorf("没有名为 %q 的插件工具（现有：%v）", tool, toolNames(table))
	}
	return entry.Exec(ctx, args)
}

// Close 卸载全部插件并关闭运行时。可重复调用。
func (r *pluginRuntime) Close(ctx context.Context) error {
	if r == nil || r.loader == nil {
		return nil
	}
	return r.loader.Close(ctx)
}

func toolNames(table spi.ToolRegistry) []string {
	tools := table.Tools()
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name())
	}
	return out
}
