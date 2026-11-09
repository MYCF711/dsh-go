package main

// 官方 pluginManager.* 的生产适配层：把 `internal/pluginmgr/official` 已有的
// 真实事务/状态适配，与只有 cmd/dshd 才拿得到的**运行时 Loader** 接起来。
//
// 为什么这一层在 cmd/dshd 而不在 internal/pluginmgr/official：
//   - `internal/pluginmgr/**` 是 HS-07 的写作用域，HS-16C 不得改；
//   - Loader 句柄由 serve 的 `pluginRuntime` 持有（newPluginRuntime），
//     `internal/pluginmgr/official` 只拿到 Manager，看不到 live 装载集合；
//   - 于是这里只做“读真实状态”与“把真实状态写进 warnings”，不复制任何
//     存储/事务逻辑。
//
// 本文件是 **WIRING.md 的对象**：serve.go 需要把
// `officialpluginmgr.New(pluginManager)` 换成 `newOfficialPluginManagerAPI(pluginRT)`。
// 未接这一行时 handler 仍会回 per-method 501 并点名缺什么，不会静默成功。

import (
	"errors"
	"fmt"
	"strings"

	"dshgo/internal/pluginmgr"
	officialpluginmgr "dshgo/internal/pluginmgr/official"
	"dshgo/internal/transport"
)

// newOfficialPluginManagerAPI 构造官方 pluginManager 的**真实**实现集合。
//
// rt 为 nil 时返回零值：所有方法回本方法四字段 501，由 handler 的点名逻辑
// 说明缺少运行时/管理器，而不是给假数据。
func newOfficialPluginManagerAPI(rt *pluginRuntime) transport.OfficialPluginManagerAPI {
	if rt == nil || rt.Manager() == nil {
		return transport.OfficialPluginManagerAPI{}
	}
	manager := rt.Manager()
	base := officialpluginmgr.New(manager)
	api := base

	// 真实 bundle 清单：Manager.Bundles() 是 state.json + 包目录的只读投影。
	api.ListBundles = func() ([]transport.OfficialBundle, error) {
		return officialPluginBundles(manager)
	}

	// 真实 Loader 条目：enabled 来自 state.json，fiberPhase 来自当前进程
	// 正在跑的 wasm 实例集合（Loaded()）；两者分账，不互相冒充。
	api.ListPlugins = func() ([]transport.OfficialPluginEntry, error) {
		return officialPluginEntries(rt)
	}

	// 官方 pluginInventory.list 与 ListPlugins 共用同一份 Manager/Loader。
	// 本仓有持久化 profile 管理（Manager 的 state.json），所以
	// managementAvailable 是真实 true；agentPresets 不产出（见 transport
	// handlers_plugin_inventory_official.go 的说明），meta 也省略。
	api.PluginInventoryList = func() (transport.OfficialPluginInventory, error) {
		entries, err := officialPluginEntries(rt)
		if err != nil {
			return transport.OfficialPluginInventory{}, err
		}
		management := true
		out := transport.OfficialPluginInventory{
			ManagementAvailable: &management,
			Entries:             make([]transport.OfficialPluginInventoryEntry, 0, len(entries)),
		}
		for _, entry := range entries {
			out.Entries = append(out.Entries, transport.OfficialPluginInventoryEntry{
				EntryID:    entry.EntryID,
				ModuleName: entry.ModuleName,
				Enabled:    entry.Enabled,
				FiberPhase: entry.FiberPhase,
				Diagnostic: entry.Diagnostic,
			})
		}
		return out, nil
	}

	// JS/Cordis 安装必须是**结构化**失败（application=failed,
	// error.code=unsupported-platform），不能塌成 HTTP 400 bad_request：
	// 前者是官方 ChangeResult 的合法管理性失败，客户端能显示“换插件”；
	// 后者会被当成“请求写错了”，用户会一直改参数。
	baseInstall := base.InstallBundle
	api.InstallBundle = func(p transport.OfficialInstallBundleParams) (transport.OfficialChangeResult, error) {
		if result, handled := officialUnsupportedInstall(p); handled {
			return result, nil
		}
		result, err := baseInstall(p)
		if err != nil {
			return result, err
		}
		return officialLoaderAwareChange(rt, result, p.Spec), nil
	}

	// updateBundle 的 result 里带一个 ChangeResult；loader 状态同样适用。
	baseUpdate := base.UpdateBundle
	api.UpdateBundle = func(p transport.OfficialUpdateBundleParams) (transport.OfficialUpdateResult, error) {
		result, err := baseUpdate(p)
		if err != nil {
			return result, err
		}
		result.Result = officialLoaderAwareChange(rt, result.Result, p.Name)
		return result, nil
	}

	// enable/disable 是真实装载/卸载；把“现在是否在跑”写回 warnings，
	// 而不是继续沿用 HS-07 之前的“本仓没有运行时加载器”。
	baseSetBundle := base.SetBundleEnabled
	api.SetBundleEnabled = func(p transport.OfficialSetEnabledParams) (transport.OfficialChangeResult, error) {
		result, err := baseSetBundle(p)
		if err != nil {
			return result, err
		}
		return officialLoaderAwareChange(rt, result, p.Name), nil
	}
	baseSetPlugin := base.SetPluginEnabled
	api.SetPluginEnabled = func(p transport.OfficialSetEnabledParams) (transport.OfficialChangeResult, error) {
		result, err := baseSetPlugin(p)
		if err != nil {
			return result, err
		}
		return officialLoaderAwareChange(rt, result, p.Name), nil
	}

	// remove 之后 Loader 里的实例已经被 Manager.Uninstall 回收；warnings 如实说明。
	baseRemove := base.RemoveBundle
	api.RemoveBundle = func(p transport.OfficialBundleNameParams) (transport.OfficialChangeResult, error) {
		result, err := baseRemove(p)
		if err != nil {
			return result, err
		}
		return officialLoaderAwareChange(rt, result, p.Name), nil
	}

	// 豁免写 compatibility.json 是真实持久化；但本仓不会热重载已装插件。
	// 底层 warning 还留着 HS-07 之前“没有运行时插件加载器”的过时前缀，
	// 这里只改写措辞，不改变“豁免已持久化、下次 install/update 生效”的事实。
	baseExemption := base.SetVersionExemption
	api.SetVersionExemption = func(p transport.OfficialVersionExemptionParams) (transport.OfficialChangeResult, error) {
		result, err := baseExemption(p)
		if err != nil {
			return result, err
		}
		result.Warnings = rewriteStaleLoaderWarnings(result.Warnings)
		return result, nil
	}

	return api
}

// officialPluginBundles 把 Manager 的真实 bundle 投影翻成官方 listBundles 行。
//
// 本仓的包没有 patch rows（官方 bundle 的第二层），所以 rows/overrides 真实
// 为空；`official=false` 是因为它们不是 DSH 随安装包提供的 optional bundle。
func officialPluginBundles(manager *pluginmgr.Manager) ([]transport.OfficialBundle, error) {
	states, err := manager.Bundles()
	if err != nil {
		return nil, err
	}
	out := make([]transport.OfficialBundle, 0, len(states))
	for _, state := range states {
		out = append(out, transport.OfficialBundle{
			Name:         state.Name,
			Official:     false,
			Availability: "profile",
			Version:      state.Version,
			Enabled:      state.Enabled,
			Installed:    true,
			Source:       state.Source,
			Optional:     false,
			Removable:    true,
			// updatable 只对“registry spec 的第三方 profile 依赖”为真；
			// 本仓一律从本地目录安装，listVersions 也会回 not-updatable。
			Updatable: false,
			Rows:      []transport.OfficialBundleRow{},
			Overrides: []string{},
		})
	}
	return out, nil
}

// officialPluginEntries 把真实 Loader 的装载集合翻成官方 listPlugins 行。
//
// fiberPhase 只报两种真值：
//   - active：当前进程真的装载了这个 wasm（Manager 的 enable/install 已提交）；
//   - null：不在当前 Loader 里（禁用/未装载；没有伪造 pending/loading）。
//
// 已安装的包都会出现在这里（与官方“当前 Loader 条目”一致）：enabled 来自
// state.json，fiberPhase 来自 live Loader；两者分账，客户端能分辨“配置开着但
// 当前进程没跑”和“真的在跑”。
func officialPluginEntries(rt *pluginRuntime) ([]transport.OfficialPluginEntry, error) {
	if rt == nil {
		return nil, fmt.Errorf("插件运行时未接线")
	}
	states, err := rt.Manager().Bundles()
	if err != nil {
		return nil, err
	}
	loadedByID := make(map[string]pluginmgr.LoadedPlugin, len(states))
	for _, plugin := range rt.Loaded() {
		loadedByID[plugin.ID] = plugin
	}
	out := make([]transport.OfficialPluginEntry, 0, len(states))
	for _, state := range states {
		// 官方 listPlugins 的 fiberPhase 是 null | failed | active | pending |
		// loading | unloading。本仓只有“当前进程真的装载了/没有”这一份真值：
		// 装载了就是 active，其余一律 null（不伪造 pending/loading）。
		var fiberPhase *string
		diagnostic := ""
		if plugin, loaded := loadedByID[state.Name]; loaded {
			active := "active"
			fiberPhase = &active
			if plugin.Version != state.Version {
				diagnostic = fmt.Sprintf("运行时装载的是 %s，state.json 记录 %s：下一次 reload 会收敛",
					plugin.Version, state.Version)
			}
		}
		out = append(out, transport.OfficialPluginEntry{
			EntryID:    state.Name,
			ModuleName: state.Entry,
			Enabled:    state.Enabled,
			FiberPhase: fiberPhase,
			Diagnostic: diagnostic,
			PatchID:    state.Name,
		})
	}
	return out, nil
}

// officialUnsupportedInstall 在真实安装事务开始前识别“合法但本仓不支持”的
// 插件形态（JS/Cordis/process），返回官方 ChangeResult 的 failed 分支。
//
// 只在 kind=path 时预检：registry/git/tarball 仍由适配器回结构化 501，
// 这里不能把“没下载源”误报成“插件形态不支持”。
func officialUnsupportedInstall(p transport.OfficialInstallBundleParams) (transport.OfficialChangeResult, bool) {
	if p.Registry != nil {
		return transport.OfficialChangeResult{}, false
	}
	spec := strings.TrimSpace(p.Spec)
	if pluginmgr.SourceKind(spec) != "path" {
		return transport.OfficialChangeResult{}, false
	}
	_, err := pluginmgr.ValidateSource(spec)
	if err == nil {
		return transport.OfficialChangeResult{}, false
	}
	var apiErr *pluginmgr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_plugin" {
		// 坏 manifest/路径不存在等仍交给适配器走原有错误映射，不在这里吞。
		return transport.OfficialChangeResult{}, false
	}
	diagnostic := apiErr.Message
	if strings.TrimSpace(apiErr.Hint) != "" {
		diagnostic += "；" + apiErr.Hint
	}
	return transport.OfficialFailedChange("install", spec, "unsupported-platform", diagnostic), true
}

// officialLoaderAwareChange 用**当前 Loader 的真实状态**修正 ChangeResult 的
// warnings：HS-07W 之后“本仓没有运行时插件加载器”已经过时，必须按实际
// 装载结果说话，否则 applied 仍可能被读成“假 installed”。
func officialLoaderAwareChange(rt *pluginRuntime, result transport.OfficialChangeResult, target string) transport.OfficialChangeResult {
	result.Warnings = rewriteStaleLoaderWarnings(result.Warnings)
	if result.Application != "applied" || rt == nil {
		return result
	}
	id := strings.TrimSpace(result.Bundle)
	if id == "" {
		id = strings.TrimSpace(target)
	}
	loaded := false
	version := ""
	for _, plugin := range rt.Loaded() {
		if plugin.ID == id {
			loaded = true
			version = plugin.Version
			break
		}
	}
	switch {
	case result.Stage == "remove":
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("插件 %s 已从 profile 与当前进程运行时移除", id))
	case result.Enabled != nil && *result.Enabled && loaded:
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("插件 %s@%s 已装载到当前进程的运行时（真实 wasm 实例）", id, version))
	case result.Enabled != nil && *result.Enabled:
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("插件 %s 的 profile 启用位已写入，但当前进程没有装载运行时实例（只持久化了状态）", id))
	case result.Enabled != nil && !*result.Enabled:
		if loaded {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("插件 %s 仍出现在当前运行时（禁用尚未生效）", id))
		} else {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("插件 %s 已从 profile 禁用，当前进程运行时中也没有它", id))
		}
	}
	return result
}

// rewriteStaleLoaderWarnings 只改 HS-07 之前的过时措辞，不吞掉警告本身。
//
// 这些 warning 由 internal/pluginmgr（HS-07 写作用域）返回，HS-16C 不改那个包；
// 适配层在出口处把“没有运行时插件加载器”改成真实边界“不会热重载已装插件”，
// 保留“豁免已持久化、下次 install/update 生效”的核心事实。
func rewriteStaleLoaderWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return warnings
	}
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		warning = strings.ReplaceAll(warning,
			"本仓没有运行时插件加载器：", "")
		warning = strings.ReplaceAll(warning,
			"本仓没有运行时插件加载器", "")
		warning = strings.TrimSpace(warning)
		if warning != "" {
			out = append(out, warning)
		}
	}
	return out
}
