package settingsform

// 官方 `@deepseek-ai/dsh-agent-preset-registry` 的设置命名空间。
//
// 权威源（随 rc.2 包发布的 TypeScript 原件）：
//
//	dsh-agent-preset-registry/src/index.ts:52-59
//	  export class AgentPresetRegistry extends TypertRemoteService {
//	    static Config = z.object({
//	      default: z.string().required(),
//	      selectedDefault: z.string().volatile(),
//	    })
//	  }
//	  get defaultId(): string { return this.config.selectedDefault.get() ?? this.config.default }
//
//	dsh-client-ui-agent-preset/src/client/settings-store.ts:13-30
//	  export const AGENT_PRESET_SETTINGS_NS = 'agent-preset-registry'
//	  ctx.remote.settings.update(AGENT_PRESET_SETTINGS_NS, { selectedDefault: id }, expectedRevision)
//
// 也就是说管理界面写的是**设置命名空间里的 `selectedDefault` 字段**，而不是
// preset 自身的属性；宿主在"会话创建时"用 `selectedDefault ?? default` 决定
// 默认 preset。本仓此前只发布 model/llm 等命名空间，官方那条写路径无通道，
// 于是界面上的"默认 preset"只能显示不能改。
//
// 本仓的真实映射：一个 preset 就是一个 mode（工具可见性 / 审批 / 沙箱档，
// 见 internal/modes）。`default` 就是 `modes.DefaultID`（"default"），
// selectedDefault 一旦写下一个真实存在的 preset id，后续新建会话的
// header.agentPreset 与 `agentPresets.list.isDefault` 都跟着它走；未知 id 在
// **写路径**就被拒绝（fallback 会让它变成静默成功）。

const (
	// NamespaceAgentPreset 是官方 agent-preset registry 的设置命名空间（逐字）。
	NamespaceAgentPreset = "agent-preset-registry"
	// FieldSelectedDefault 是"后续会话的默认 preset"字段（逐字）。
	FieldSelectedDefault = "selectedDefault"
	// AgentPresetFallback 是官方 `Config.default` 在本仓的真实值：
	// 没有 selectedDefault（或它已失效）时用的 preset = modes.DefaultID。
	//
	// 这里不复述 modes 包是为了不让设置 schema 依赖运行时模式表；
	// cmd/dshd 有一条断言把这个字面量与 modes.DefaultID 钉在一起。
	AgentPresetFallback = "default"
	// AgentPresetIDMaxLength 是 preset id 的上限（modes id 是任意非空字符串，
	// 这里只挡住"用设置文档塞超长串"这一种滥用）。
	AgentPresetIDMaxLength = 128
)

// agentPresetNamespace 是这条命名空间的声明（schema/sections/校验同源）。
func agentPresetNamespace() uiNamespace {
	field := uiStringField(FieldSelectedDefault, AgentPresetFallback).withRequired(false)
	return uiNamespace{
		ns:    NamespaceAgentPreset,
		title: "Default agent preset",
		fields: []uiField{
			field,
		},
		// 官方 `defaultId` 的回落值：没写过就是宿主默认 preset。
		base: map[string]any{FieldSelectedDefault: AgentPresetFallback},
	}
}

// agentPresetNamespaceSpec 把声明翻成注册表能收的 NamespaceSpec。
//
// 字段级校验（类型/长度）由 uiNamespace.validate 负责；"这个 id 是不是真实存在
// 的 preset"需要 home 下的 modes 目录才能判，所以在 serve 的 store 写路径上
// 另做一次（见 cmd/dshd/agent_preset_default.go）。
func agentPresetNamespaceSpec() NamespaceSpec {
	namespace := agentPresetNamespace()
	return NamespaceSpec{
		NS:       namespace.ns,
		Title:    namespace.title,
		Applies:  "live",
		Fields:   namespace.fieldSpecs(),
		Schema:   namespace.schema(),
		Base:     deepCloneMap(namespace.base),
		Validate: namespace.validate,
	}
}
