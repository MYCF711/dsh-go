package main

// 官方 #1 `tool-plugin-manager`（`@deepseek-ai/dsh-plugin-manager/tools`）的宿主实现。
//
// 官方原件（逐字，asar 只读解出
// `@deepseek-ai/dsh-plugin-manager/src/tools.ts:88-164`）：
//
//	ctx.tools.register(defineTool({
//	  name: 'plugin_manager',
//	  description: 'List plugins or bundles in the current profile, enable or disable them,
//	                install a bundle, or remove an installed bundle. …',
//	  parameters: {
//	    action: { enum: ['list_plugins','list_bundles','set_plugin','set_bundle',
//	                     'install_bundle','remove_bundle',
//	                     'list_version_exemptions','set_version_exemption'] },
//	    target, enabled, runtimeVersion, acceptRisk, approvedBuilds, registry, offset, limit,
//	  },
//	  …
//	}))
//
// 八条 action 里，本仓有真实支撑的是**六条**：
//
//	 list_plugins            → pluginManager.listPlugins 的真实实现（与 pluginInventory.list 同源）
//	 list_bundles            → pluginManager.listBundles 的真实实现
//	 set_plugin / set_bundle → 同一份持久化启用位（pluginManager.setPluginEnabled/setBundleEnabled）
//	 list_version_exemptions → compatibility.json 的真实读
//	 set_version_exemption   → compatibility.json 的真实写
//
// 两条**没有**真实支撑：`install_bundle` / `remove_bundle`。本仓没有 registry/pnpm/git/tarball
// 下载源，也没有安装事务（见 `internal/transport/handlers_pluginmanager_official.go:193-195`
// 的逐方法缺失说明）。这两条**不是**被悄悄省略，而是**照官方形状回结构化失败**：
// 返回的 `ChangeResult` 里 `changed:false`、`error.code=unsupported-platform`，并带
// `nextStep` 说明"为什么在这里装不了"（官方对"改不了的东西"就是给 ChangeResult 而不是抛异常，
// 客户端据此显示"换插件"而不是"你的请求写错了"）。
//
// 为什么这个工具在 `cmd/dshd` 而不是 `modules/tool/plugin`：
// `modules/tool/plugin/plugin.go` 的 `plugin` 工具管的是**另一层**——`dsh.toml` 里的编译期
// `[[module]]` 白名单（改完下次启动生效）。本文件管的是运行时插件面（state.json + 运行时装载器），
// 两者事实源不同，混进一个 enum 会让模型以为"set_plugin 能改 dsh.toml"。官方 id
// `tool-plugin-manager` 说的是后者，所以这里落后者；前者继续由 `plugin` 工具承担，
// 并在 `docs/base-plugins-partial19-official-shape.md` 里写明两层分工。
//
// 审批：官方在 execute 开头就对**每一次**调用（含 list_*）要一次 danger-full-access 升级审批
// （`tools.ts:112-116`）。本仓的能力面（`spi.Tool.Caps`）不只支持"写"+审批，还能表达
// "这条调用要真实审批"，所以：
//   - 读类 action（list_*）只报 `Writes:false`（不碰任何状态的读不需要打审批弹窗）；
//   - 写类 action（set_*/install_*/remove_*）报 `Writes:true` + `Destructive:true`，
//     由审批管线在**执行之前**拦一次，且**不改变会话档位**（与官方
//     "Approval does not change the session permission mode" 同义）。
//
// 这是与官方的**显式差异**，写在上面，不靠"反正都能跑"糊过去。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

// pluginManagerToolName 是官方工具名（逐字）。
const pluginManagerToolName = "plugin_manager"

// pluginManagerActions 是官方 enum，逐字（顺序也照官方）。
var pluginManagerActions = []string{
	"list_plugins",
	"list_bundles",
	"set_plugin",
	"set_bundle",
	"install_bundle",
	"remove_bundle",
	"list_version_exemptions",
	"set_version_exemption",
}

// pluginManagerSchema 逐字复刻官方 parameters（类型/枚举/说明）。
//
// 与工厂输出的一个差别必须显式：官方 zod 对 `target`/`enabled` 等 8 个参数**全部可选**，
// 由 execute 里的分支按 action 现查"这个 action 需要哪个参数"。本仓工具契约（`spi.Tool.Schema`）
// 也支持"全部可选 + 运行期必填校验"，所以这里同样全部 optional，运行期缺参数
// 返回与官方一致的错误文案（见 `requireTarget` 等）。
var pluginManagerSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["list_plugins", "list_bundles", "set_plugin", "set_bundle", "install_bundle", "remove_bundle", "list_version_exemptions", "set_version_exemption"], "description": "Management operation."},
    "target": {"type": "string", "description": "Plugin entry id, bundle package name, or installation spec, according to action. For an Official catalog entry, pass list_bundles installTarget.spec to install_bundle."},
    "enabled": {"type": "boolean", "description": "Required for set operations; defaults to true for installation. For set_version_exemption, true grants and false revokes."},
    "runtimeVersion": {"type": "string", "description": "For set_version_exemption: exact DSH version from list_version_exemptions. Target must be the manifest package-name@version, not an alias or version range."},
    "acceptRisk": {"type": "boolean", "description": "For granting an exemption: true only after warning the user about possible crashes and data loss and receiving explicit permission for this exact plugin/runtime pair. General installation permission is not enough."},
    "approvedBuilds": {"type": "array", "items": {"type": "string"}, "description": "For install_bundle: pass names from pendingBuilds only after the user explicitly approves running their install scripts in the conversation. This grants persistent permission for this profile."},
    "registry": {"type": "string", "description": "For install_bundle: the npm registry URL asked first, when the user names one; otherwise the configured registry is asked, and its configured fallbacks while a registry is unreachable."},
    "offset": {"type": "number", "description": "Zero-based list offset; defaults to 0."},
    "limit": {"type": "number", "description": "List page size, from 1 to 100; defaults to 25."}
  },
  "required": ["action"],
  "additionalProperties": false
}`)

// pluginManagerArgs 是官方参数面。指针用于区分"没给"与"给了 false/0"。
type pluginManagerArgs struct {
	Action         string    `json:"action"`
	Target         *string   `json:"target"`
	Enabled        *bool     `json:"enabled"`
	RuntimeVersion *string   `json:"runtimeVersion"`
	AcceptRisk     *bool     `json:"acceptRisk"`
	ApprovedBuilds *[]string `json:"approvedBuilds"`
	Registry       *string   `json:"registry"`
	Offset         *int      `json:"offset"`
	Limit          *int      `json:"limit"`
}

// pluginManagerTool 是官方 `plugin_manager` 工具的宿主实现。
//
// api 是宿主装配好的官方 pluginManager 实现集合（`newOfficialPluginManagerAPI`）。
// nil = 这个进程没有插件运行时（例如 `dshd run` 单轮子进程）：**不是静默给空数据**，
// 每条 action 都回点名缺失的四字段错误。
type pluginManagerAPI struct {
	transport.OfficialPluginManagerAPI
}

type pluginManagerTool struct{ api pluginManagerAPI }

func (t *pluginManagerTool) Name() string { return pluginManagerToolName }

func (t *pluginManagerTool) Description() string {
	return "List plugins or bundles in the current profile, enable or disable them, " +
		"inspect or set version exemptions. Every mutating action requires approval for this call; " +
		"approval does not change the session permission mode. Changes affect every session in this profile. " +
		"List first to obtain exact identifiers. Installing or removing bundles is unavailable in this host " +
		"(no registry/git/tarball source and no install transaction) and reports a structured unsupported failure."
}

func (t *pluginManagerTool) Schema() json.RawMessage { return pluginManagerSchema }

// Caps：按 action 分两种声明——读类不写、写类按"写 + 破坏性"报。
//
// 不用一个恒 true 的 Writes 糊住全部 action：那样一次 `list_plugins` 也要过审批弹窗，
// 与官方"list 是 read 卡片"（`tools.ts:162` `kind: args.action.startsWith('list_') ? 'read' : 'other'`）不一致。
func (t *pluginManagerTool) Caps() spi.Caps { return spi.Caps{Writes: true, Destructive: true} }

func (t *pluginManagerTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a pluginManagerArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数形状：{"action":"list_plugins"}`}, err
	}
	action := strings.TrimSpace(a.Action)
	if !pluginManagerActionKnown(action) {
		return spi.Result{Hint: "action 只能是 " + strings.Join(pluginManagerActions, " / ")},
			fmt.Errorf("未知 action: %q", a.Action)
	}
	if err := ctx.Err(); err != nil {
		return spi.Result{Hint: "调用已取消，没有做任何改动"}, err
	}
	if t.api.ListPlugins == nil && t.api.ListBundles == nil && t.api.ListVersionExemptions == nil &&
		t.api.SetPluginEnabled == nil && t.api.SetBundleEnabled == nil && t.api.SetVersionExemption == nil {
		return spi.Result{Hint: "这个进程没有插件运行时（没有 OfficialPluginManagerAPI）：plugin_manager 需要 dshd serve 的插件面，单轮子进程里没有"},
			fmt.Errorf("plugin_manager: 没有插件运行时")
	}

	switch action {
	case "list_plugins", "list_bundles":
		return t.list(a, action)
	case "set_plugin", "set_bundle":
		return t.set(a, action)
	case "list_version_exemptions":
		return t.listExemptions()
	case "set_version_exemption":
		return t.setExemption(a)
	case "install_bundle", "remove_bundle":
		return t.unsupported(action)
	}
	return spi.Result{Hint: "action 只能是 " + strings.Join(pluginManagerActions, " / ")},
		fmt.Errorf("未知 action: %q", action)
}

func pluginManagerActionKnown(action string) bool {
	for _, known := range pluginManagerActions {
		if known == action {
			return true
		}
	}
	return false
}

// list 实现官方 list_plugins / list_bundles 的**分页信封**：
// `{entries, total, nextOffset}`，offset 默认 0、limit 默认 25 且必须 1..100（官方 `tools.ts:130-141`）。
func (t *pluginManagerTool) list(a pluginManagerArgs, action string) (spi.Result, error) {
	offset := 0
	if a.Offset != nil {
		offset = *a.Offset
	}
	limit := 25
	if a.Limit != nil {
		limit = *a.Limit
	}
	// 官方错误原文：'offset must be a non-negative integer and limit must be an integer from 1 to 100'
	if offset < 0 || limit < 1 || limit > 100 {
		return spi.Result{Hint: "offset 要 ≥0、limit 要在 1..100 之间"},
			fmt.Errorf("offset must be a non-negative integer and limit must be an integer from 1 to 100")
	}

	var rows []any
	switch action {
	case "list_plugins":
		if t.api.ListPlugins == nil {
			return spi.Result{Hint: "宿主没有挂接运行时 Loader 条目（OfficialPluginManagerAPI.ListPlugins）"},
				fmt.Errorf("plugin_manager list_plugins: 未接线")
		}
		entries, err := t.api.ListPlugins()
		if err != nil {
			return spi.Result{Hint: "读运行时 Loader 条目失败；先 pluginManager.listPlugins 的同一份状态是否可用"}, err
		}
		rows = make([]any, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, e)
		}
	default:
		if t.api.ListBundles == nil {
			return spi.Result{Hint: "宿主没有挂接已安装 bundle 清单（OfficialPluginManagerAPI.ListBundles）"},
				fmt.Errorf("plugin_manager list_bundles: 未接线")
		}
		bundles, err := t.api.ListBundles()
		if err != nil {
			return spi.Result{Hint: "读已安装 bundle 清单失败；检查 profile 的 state.json 是否可读"}, err
		}
		rows = make([]any, 0, len(bundles))
		for _, b := range bundles {
			rows = append(rows, b)
		}
	}

	total := len(rows)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := rows[offset:end]
	var nextOffset *int
	if end < total {
		n := end
		nextOffset = &n
	}
	envelope := map[string]any{"entries": page, "total": total, "nextOffset": nextOffset}
	body, err := json.Marshal(envelope)
	if err != nil {
		return spi.Result{Hint: "结果序列化失败（不应发生）"}, err
	}
	return spi.Result{
		Summary: fmt.Sprintf("%s: %d/%d 条（offset=%d, limit=%d）", action, len(page), total, offset, limit),
		Text:    string(body),
		Data:    envelope,
	}, nil
}

// set 实现官方 set_plugin / set_bundle：目标与 enabled 都必填（官方 `tools.ts:144-148`
// 原文 'target and enabled are required'），结果原样透出 ChangeResult。
func (t *pluginManagerTool) set(a pluginManagerArgs, action string) (spi.Result, error) {
	if a.Target == nil || a.Enabled == nil {
		return spi.Result{Hint: action + " 需要 target 与 enabled（enabled=true 开 / false 关）"},
			fmt.Errorf("target and enabled are required")
	}
	// 官方字段是 PluginEntryId（branded string）；空串不是合法 id。
	target := strings.TrimSpace(*a.Target)
	if target == "" {
		return spi.Result{Hint: "target 是插件条目 id / bundle 包名；先用 list_plugins / list_bundles 拿到确切值"},
			fmt.Errorf("target and enabled are required")
	}

	var (
		res transport.OfficialChangeResult
		err error
	)
	switch action {
	case "set_plugin":
		if t.api.SetPluginEnabled == nil {
			return spi.Result{Hint: "宿主没有挂接持久化启用位（OfficialPluginManagerAPI.SetPluginEnabled）"},
				fmt.Errorf("plugin_manager set_plugin: 未接线")
		}
		res, err = t.api.SetPluginEnabled(transport.OfficialSetEnabledParams{Name: target, Enabled: *a.Enabled})
	default:
		if t.api.SetBundleEnabled == nil {
			return spi.Result{Hint: "宿主没有挂接持久化启用位（OfficialPluginManagerAPI.SetBundleEnabled）"},
				fmt.Errorf("plugin_manager set_bundle: 未接线")
		}
		res, err = t.api.SetBundleEnabled(transport.OfficialSetEnabledParams{Name: target, Enabled: *a.Enabled})
	}
	if err != nil {
		return spi.Result{Hint: "改启用位失败；先 list 确认 target 存在，再检查 profile 状态文件是否可写"}, err
	}
	return changeResult(action, res), nil
}

// listExemptions 实现官方 list_version_exemptions：
// 结果恒带 `runtimeVersion`（官方 `tools.ts:126-127` `{ runtimeVersion: getDshRuntimeVersion(), ...manager.listVersionExemptions() }`），
// 再接上 compatibility.json 的真实读写结果（`exemptions` + `warnings`）。
func (t *pluginManagerTool) listExemptions() (spi.Result, error) {
	if t.api.ListVersionExemptions == nil {
		return spi.Result{Hint: "宿主没有挂接版本豁免存储（compatibility.json）；internal/pluginmgr/official 已实现，缺的是装配"},
			fmt.Errorf("plugin_manager list_version_exemptions: 未接线")
	}
	ex, err := t.api.ListVersionExemptions()
	if err != nil {
		return spi.Result{Hint: "读 compatibility.json 失败；坏文件按既有语义降级成'没有豁免 + warnings'，见 internal/pluginmgr/exemption.go"}, err
	}
	envelope := map[string]any{
		"runtimeVersion": dshRuntimeVersion(),
		"exemptions":     ex.Exemptions,
		"warnings":       ex.Warnings,
	}
	if envelope["exemptions"] == nil {
		envelope["exemptions"] = map[string][]string{}
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return spi.Result{Hint: "结果序列化失败（不应发生）"}, err
	}
	return spi.Result{
		Summary: fmt.Sprintf("版本豁免 %d 条（runtime %s）", len(ex.Exemptions), dshRuntimeVersion()),
		Text:    string(body),
		Data:    envelope,
	}, nil
}

// setExemption 实现官方 set_version_exemption：
// target / runtimeVersion / enabled 三者必填（官方原文 'target package-name@version, runtimeVersion, and enabled are required'），
// 授予豁免必须显式 `acceptRisk:true`（官方的风险提示语义，见 schema 里 acceptRisk 的说明）。
func (t *pluginManagerTool) setExemption(a pluginManagerArgs) (spi.Result, error) {
	if a.Target == nil || a.RuntimeVersion == nil || a.Enabled == nil {
		return spi.Result{Hint: "set_version_exemption 需要 target（package-name@version）、runtimeVersion、enabled"},
			fmt.Errorf("target package-name@version, runtimeVersion, and enabled are required")
	}
	target := strings.TrimSpace(*a.Target)
	runtimeVersion := strings.TrimSpace(*a.RuntimeVersion)
	if target == "" || runtimeVersion == "" {
		return spi.Result{Hint: "target / runtimeVersion 都不能为空"},
			fmt.Errorf("target package-name@version, runtimeVersion, and enabled are required")
	}
	// 授予（enabled=true）必须有 acceptRisk=true；撤销不需要。
	if *a.Enabled && (a.AcceptRisk == nil || !*a.AcceptRisk) {
		return spi.Result{Hint: "授予豁免要 acceptRisk=true：先向用户说明可能崩溃与数据丢失，并拿到**这个插件 × 这个运行时版本**的明确同意；撤销不需要"},
			fmt.Errorf("granting a version exemption requires acceptRisk=true after explicit user permission")
	}
	if t.api.SetVersionExemption == nil {
		return spi.Result{Hint: "宿主没有挂接版本豁免存储（compatibility.json）；internal/pluginmgr/official 已实现，缺的是装配"},
			fmt.Errorf("plugin_manager set_version_exemption: 未接线")
	}
	// 官方签名是 setVersionExemption(target, runtimeVersion, enabled, acceptRisk)；
	// 本仓 handler 的参数结构体把 acceptRisk 做成可选指针，这里如实透传。
	res, err := t.api.SetVersionExemption(transport.OfficialVersionExemptionParams{
		PackageVersion: target,
		RuntimeVersion: runtimeVersion,
		Enabled:        *a.Enabled,
		AcceptRisk:     a.AcceptRisk,
	})
	if err != nil {
		return spi.Result{Hint: "写兼容性豁免失败；检查 compatibility.json 是否可写"}, err
	}
	return changeResult("set_version_exemption", res), nil
}

// unsupported 是官方 `install_bundle` / `remove_bundle` 的宿主形状。
//
// 为什么不是错误而是 ChangeResult：官方对"应用性失败"用的就是 ChangeResult
// （`application` + `error` + `nextStep`），客户端据此显示"这个装不了"，
// 而不是把 tools 调用变成异常。本仓没有下载源与安装事务，所以：
//
//	changed=false, application='failed',
//	error.code='unsupported-platform', error.diagnostic=<缺什么>,
//	nextStep=<用户该怎么做>, stage='resolve', target=''
//
// 这四个字段都是**真话**：不是假数据、不是静默成功。
func (t *pluginManagerTool) unsupported(action string) (spi.Result, error) {
	what := "安装"
	if action == "remove_bundle" {
		what = "卸载"
	}
	res := transport.OfficialChangeResult{
		Changed:     false,
		Application: "failed",
		Stage:       "resolve",
		Error: &transport.OfficialChangeError{
			Code:       "unsupported-platform",
			Diagnostic: "本仓没有 bundle 下载源（registry/pnpm/git/tarball），也没有可回滚的" + what + "事务",
		},
	}
	body, err := json.Marshal(changeEnvelope(action, res))
	if err != nil {
		return spi.Result{Hint: "结果序列化失败（不应发生）"}, err
	}
	return spi.Result{
		Summary: action + " 不支持：本仓没有下载源与安装事务（结构化失败，未做任何改动）",
		Text:    string(body),
		Data:    changeEnvelopeOnce(body),
	}, nil
}

// changeResult 把宿主真实的 ChangeResult 翻成工具结果：`nextStep` 由宿主给出的
// `RestartReason` 翻译（官方 `withNextStep` 语义：换不了/要重启时说清下一步）。
func changeResult(action string, res transport.OfficialChangeResult) spi.Result {
	envelope := changeEnvelope(action, res)
	body, err := json.Marshal(envelope)
	if err != nil {
		return spi.Result{Hint: "结果序列化失败（不应发生）"}
	}
	summary := action + "："
	if res.Changed {
		summary += "已生效"
	} else {
		summary += "未改动"
	}
	if envelope["nextStep"] != nil {
		summary += "；" + fmt.Sprint(envelope["nextStep"])
	}
	return spi.Result{Summary: summary, Text: string(body), Data: envelope}
}

// changeEnvelope 把官方 ChangeResult 包成工具输出，并按官方 `withNextStep` 补 `nextStep`。
//
// 官方语义（`dsh-plugin-manager/src/tools.ts:130-150` 的 `withNextStep`）：一个**没有应用**的
// 变更要带上 `nextStep`，说明"要做什么、或者该告诉用户什么"；已应用的变更不带。
func changeEnvelope(action string, res transport.OfficialChangeResult) map[string]any {
	envelope := map[string]any{
		"changed":     res.Changed,
		"application": res.Application,
		"stage":       res.Stage,
		"target":      res.Target,
	}
	if res.RestartReason != "" {
		envelope["restartReason"] = res.RestartReason
	}
	if res.Enabled != nil {
		envelope["enabled"] = *res.Enabled
	}
	if res.Error != nil {
		envelope["error"] = res.Error
	}
	if len(res.Warnings) > 0 {
		envelope["warnings"] = res.Warnings
	}
	if res.Bundle != "" {
		envelope["bundle"] = res.Bundle
	}
	if res.Version != "" {
		envelope["version"] = res.Version
	}
	next := nextStepFor(action, res)
	if next != "" {
		envelope["nextStep"] = next
	}
	return envelope
}

// nextStepFor 是官方 `withNextStep` 的宿主口径：只有"没有应用"时才给下一步。
func nextStepFor(action string, res transport.OfficialChangeResult) string {
	if res.Changed {
		return ""
	}
	if res.Error != nil {
		return "这次没有应用（" + res.Error.Code + "）。改用本仓支持的插件通道（pluginManager.* / 已安装 bundle 的 set_bundle）或告诉用户这一点"
	}
	switch res.RestartReason {
	case "":
		return "这次没有产生改动（目标已经是这个状态）"
	case "restart":
		return "改动已写入，但需要重启宿主后生效"
	}
	return "这次没有应用（原因：" + res.RestartReason + "）"
}

// changeEnvelopeOnce 把已经序列化过的信封还原成 Data（避免 Data 与 Text 不一致）。
func changeEnvelopeOnce(body []byte) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(body, &out)
	return out
}

// dshRuntimeVersion 回答官方 `getDshRuntimeVersion()`：当前运行时的版本串。
//
// 本仓的版本只有一处真值（`cmd/dshd/main.go` 的 `version`，由
// `cmd/dshd/version_guard_test.go` 守着），这里直接引用它——不另造一份。
func dshRuntimeVersion() string { return version }

// pluginManagerModule 装配 `plugin_manager` 工具。
//
// api 为宿主装配好的官方 pluginManager 实现集合；nil 时工具仍注册，但每条 action
// 都回点名缺失的四字段错误（见 Exec 的第一道检查）——这样"工具不在表里"与
// "工具在了但宿主没接"能被区分开。
func pluginManagerModule(api pluginManagerAPI) kernel.Module {
	return kernel.Module{
		ID:       "tool-plugin-manager",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			return reg.Register(&pluginManagerTool{api: api})
		},
	}
}
