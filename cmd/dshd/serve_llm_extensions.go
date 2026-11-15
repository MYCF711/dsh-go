package main

// 官方 base 插件 #6 `deepseek-llm-api-extensions` 的**生产注册者**，以及它的第一个
// 真实字段所有者 #17 `plugin-package-inventory-deepseek`（字段 `dsh_plugin_packages`）。
//
// 为什么需要这个文件：`internal/llm/provider.go:235` 的注册表、`:276` 的 prepare、
// `internal/llm/client.go:137` 的接线都是真的，但**全仓没有任何生产调用者**——
// 注册表恒空，`PrepareRequestExtensions` 恒返回空（审计 `plugin-face.md` 主表 #6）。
// 这一层就是那个缺失的调用者：装配期注册一次，之后这个进程里每条 DeepSeek 官方
// 协议的模型请求都带真实库存。
//
// 官方原件（只读）：
//   - `@deepseek-ai/dsh-deepseek-llm-api-extensions/README.md`:31-40
//     注册表只负责"加法与生命周期"，字段语义归各所有者；贡献者失败只省掉它
//     自己那一个字段；`register(field, provider)` 一个字段一个所有者。
//   - 同包 `README.md`:60 "**Official DeepSeek requests only** —— 注册表故意没有
//     provider 中立的转发，也没有 pi-ai 适配器集成"：所以字段只挂 DeepSeek 的两条
//     协议（`deepseek-completions` 与账号路由 `deepseek-account`），不进 OpenAI 面。
//   - 同包 `src/index.ts:96` `prepare` 每次请求快照已注册 provider；`:127` 单个
//     provider 抛错 → 只从这一条请求里省掉该字段并记一条 warn，其余照发。
//   - `@deepseek-ai/dsh-plugin-package-inventory-deepseek/README.md`:37
//     "Every request re-reads active non-group entries from the host Loader tree."
//     → 这里**不在装配期冻结库存**，每次 prepare 现读 manager + loader 投影。
//   - 同包 `README.md`:40 排序/去重/取舍：去重键 `name\u0000version`、
//     "sorted with a locale-independent comparison"、name-only 排在同名带版本之前、
//     "Disabled, pending, failed, disposed, unloading, structural rows ... are excluded"。
//   - 同包 `src/types.ts`：字段值形状 `{ version: 1, packages: [{ name, version? }] }`，
//     `version` 只在是**非空字符串**时出现。
//
// 为什么注册在 serve 的装配期、而不是 `init()`（R2）：注册表是进程级的，一条装配
// 之后这个进程里所有 DeepSeek 请求都带它，这正是官方 cordis 服务 effect 域的效果。
//
// 本仓**不产出**的那一半（官方也只在其服务存在时才产出）：官方会在请求携带
// `sessionId`、且该会话挂在一个 standing preset 上时，把该 preset 的独立 Loader
// 树并进同一份库存（`plugin-package-inventory-deepseek/README.md`:39-42、
// `src/index.ts:236-247` 的 `inspectCompositions`）。本仓的 `llm.Request` 没有
// sessionId，也没有 agentPresets 服务可按请求查组合，所以这里只报宿主自己的
// Loader 树——官方对这种部署的说明正是 "deployments without the service report
// the host tree only"，不是缺口，是不可达分支。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"dshgo/internal/llm"
	"dshgo/internal/transport"
)

// requestExtensionPluginPackages 是字段名**逐字**（官方 `src/types.ts` 的
// 声明合并键 `dsh_plugin_packages`，不能改名：接收方按名字寻址）。
const requestExtensionPluginPackages = "dsh_plugin_packages"

// dshPluginPackageIdentity 是一条活跃包身份（官方 `src/types.ts`
// `DeepSeekPluginPackageIdentity`）：`version` 只在是非空字符串时出现。
type dshPluginPackageIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// dshPluginPackageInventory 是 version-1 字段值（官方 `src/types.ts`
// `DeepSeekPluginPackageInventoryExtension`）：`version` 恒为 1。
type dshPluginPackageInventory struct {
	Version  int                        `json:"version"`
	Packages []dshPluginPackageIdentity `json:"packages"`
}

// pluginEntryActive 报告一条 Loader 条目是否属于官方口径的"活跃条目"。
//
// 官方判据（`plugin-package-inventory-deepseek/README.md`:38）：
// "Entries are included only while their root fiber is `ACTIVE` and their effective
// Loader state is enabled." 两个条件都要：
//   - Enabled 是 state.json 的生效启用位（`officialPluginEntries` 的真实来源）；
//   - FiberPhase == "active" 是"这个进程里真的装载了"（`pluginRT.Loaded()` 的真实来源）。
//
// 本仓只报这两种真值（见 serve_plugins_official.go 的 officialPluginEntries）：
// 装载了 → "active"，否则 → null。没有被挂载的包既不假装 pending、也不谎报 active，
// 所以这里判空的条目（禁用/未装载）一律不进库存。
func pluginEntryActive(entry transport.OfficialPluginEntry) bool {
	if !entry.Enabled || entry.FiberPhase == nil {
		return false
	}
	return *entry.FiberPhase == "active"
}

// collectActivePluginPackages 把**真实投影**收敛成字段值里的 packages。
//
// 数据来源只有一处（不另造第二份库存）：
//   - entries = `officialPluginEntries(rt)`（state.json 生效位 + 本进程真实装载集合）；
//   - bundles = `officialPluginBundles(manager)`（state.json + 包目录投影，含版本）。
//
// 规则逐条对应官方 `plugin-package-inventory-deepseek/src/index.ts:257-275`：
//   - 只收活跃条目（见 pluginEntryActive）；
//   - 去重键 `name\u0000version`（同一次请求里同名同版只留一条）；
//   - 排序先 name 再 version，用 Go 的字节比较（与 locale/ICU 数据无关，
//     对应官方的 `compareWireText`）；version 为空 = name-only，字节上空串最小，
//     所以 name-only 天然排在同名带版本条目之前；
//   - `version` 只在非空字符串时带上（官方 `identityFromManifest`）。
func collectActivePluginPackages(entries []transport.OfficialPluginEntry, bundles []transport.OfficialBundle) []dshPluginPackageIdentity {
	versions := make(map[string]string, len(bundles))
	for _, bundle := range bundles {
		if name := strings.TrimSpace(bundle.Name); name != "" && strings.TrimSpace(bundle.Version) != "" {
			versions[name] = bundle.Version
		}
	}

	unique := make(map[string]dshPluginPackageIdentity, len(entries))
	for _, entry := range entries {
		if !pluginEntryActive(entry) {
			continue
		}
		name := strings.TrimSpace(entry.EntryID)
		if name == "" {
			continue
		}
		identity := dshPluginPackageIdentity{Name: name}
		if version := versions[name]; strings.TrimSpace(version) != "" {
			identity.Version = version
		}
		unique[identity.Name+"\x00"+identity.Version] = identity
	}

	packages := make([]dshPluginPackageIdentity, 0, len(unique))
	for _, identity := range unique {
		packages = append(packages, identity)
	}
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Name != packages[j].Name {
			return packages[i].Name < packages[j].Name
		}
		return packages[i].Version < packages[j].Version
	})
	return packages
}

// activePluginPackages 每次请求现读一次真实库存（官方"每请求重读"语义）。
//
// 取不到就返回错误：由注册表把**这一个字段**从本次请求里省掉（官方同款），
// 绝不阻塞模型请求本身，也绝不拿上一轮的旧值冒充这一次的库存。
func activePluginPackages(rt *pluginRuntime) ([]dshPluginPackageIdentity, error) {
	if rt == nil || rt.Manager() == nil {
		return nil, fmt.Errorf("插件运行时未接线，拿不到活跃包清单")
	}
	entries, err := officialPluginEntries(rt)
	if err != nil {
		return nil, err
	}
	bundles, err := officialPluginBundles(rt.Manager())
	if err != nil {
		return nil, err
	}
	return collectActivePluginPackages(entries, bundles), nil
}

// pluginPackagesRequestExtension 构造这一个字段的 provider（官方由 #17 插件自己
// 注册；本仓的字段所有者就是 #17 的宿主实现）。
//
// 只返回 value，不带 accept：官方 `dsh_plugin_packages` 的 provider 也只返回
// `{ value }`（`plugin-package-inventory-deepseek/src/index.ts:283-289`）——
// 库存是只读元数据，没有"2xx 之后才提交"的状态。
func pluginPackagesRequestExtension(rt *pluginRuntime) llm.RequestExtension {
	return llm.RequestExtension{
		Field: requestExtensionPluginPackages,
		Prepare: func(ctx context.Context, _ *llm.Request) (json.RawMessage, func() error, error) {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			packages, err := activePluginPackages(rt)
			if err != nil {
				return nil, nil, err
			}
			value, err := json.Marshal(dshPluginPackageInventory{Version: 1, Packages: packages})
			if err != nil {
				return nil, nil, err
			}
			return value, nil, nil
		},
	}
}

// registerPluginPackagesExtension 是**装配期唯一一次**注册：把
// `dsh_plugin_packages` 挂到 DeepSeek 的两条官方协议上。
//
// 注册两次（每个协议一次）而不是用 `API: ""`：空 API 表示"所有协议"，那会把
// DeepSeek 的 provider 元数据塞进 OpenAI / pi-ai 面的请求体，正是官方
// "Official DeepSeek requests only" 要排除的形状。
//
// 失败必须返回：重复注册/字段名非法是装配 bug，不能静默跳过（否则又是"注册了
// 但值恒为空"）。调用方（serve.go）记一条明确的 stderr 并继续起服务——这条字段
// 缺席不影响模型请求本身。
func registerPluginPackagesExtension(rt *pluginRuntime) error {
	for _, api := range []string{llm.ProviderAPIDeepSeekCompletions, llm.ProviderAPIDeepSeekAccount} {
		extension := pluginPackagesRequestExtension(rt)
		extension.API = api
		if err := llm.RegisterRequestExtension(extension); err != nil {
			return fmt.Errorf("注册 %s（协议 %s）失败: %w", requestExtensionPluginPackages, api, err)
		}
	}
	return nil
}
