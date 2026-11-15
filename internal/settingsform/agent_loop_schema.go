package settingsform

// 官方 `@deepseek-ai/dsh-agent-loop` 的设置命名空间 `agent-loop`。
//
// # 为什么这个命名空间必须存在
//
// 官方 UI 包 `dsh-client-ui-settings-agent-loop` 的页面在
// `src/client/index.ts:60` 挂了 `ctx.configForms.whileServed(['agent-loop'], ...)`：
// **宿主不服务这条命名空间，页面就永远不出现**（不是"显示成灰色"，是整页没有）。
// 本仓此前只发布 model / permission 等 11 条，所以官方那条"并行工具调用上限"页
// 在 GUI 里是 MISSING（证据：GUI 仓 `internal/settingsform/settingsform.go:14-15`
// 的 4 条 MISSING 里就有它）。
//
// # 权威源（随 rc.2 包发布的 TypeScript 原件）
//
//	<official-rc2>/dsh/node_modules/@deepseek-ai/
//	  dsh-agent-loop/src/index.ts:294-306
//	    static Config = z.object({
//	      maxParallelToolCalls: z.number().step(1).min(1)
//	        .default(DEFAULT_MAX_PARALLEL_TOOL_CALLS).volatile(),
//	      agents: z.array(z.object({...})).default([]),
//	    })
//	  dsh-agent-loop/src/constants.ts:6
//	    export const DEFAULT_MAX_PARALLEL_TOOL_CALLS = 10
//	  dsh-client-ui-settings-agent-loop/src/client/agent-loop-card-controller.ts:14,38-50
//	    export const AGENT_LOOP_NS = 'agent-loop'
//	    new SettingsFormModel(scope, [settingsNumberField('maxParallelToolCalls')])
//
// 页面的 `AgentLoopSettings` 只有 `maxParallelToolCalls` 一个字段，而且源码注释
// 明确写了「The Host section carries only this field — the composed `agents` array
// is deliberately not part of it」——原因是 schemastery 的 `.volatile()` 才把字段
// 放进设置面，`agents` 没有 `.volatile()`（`index.ts:299-305`），官方 section 里
// 也确实没有它。所以这里**只发这一个字段**，一个都不发明。
//
// # 线上 envelope（实跑取证）
//
// 取证脚本 tmp/GO8-RC2/_hs4/dump-hs4.mjs（按上面 zod 声明逐行重建，
// 输出 _hs4/official-envelopes-hs4.json）：
//
//	{"type":"number","meta":{"step":1,"min":1,"default":10}}
//
// # 本仓的消费方（真生效，不是只读能写）
//
// `internal/agent` 的并行执行上限 `Loop.MaxParallelToolCalls`
// （internal/agent/parallel.go:47-53 的官方默认 10 与 69-96 的解析），由
// cmd/dshd 在装配每一轮循环时从这份设置读出来（见 cmd/dshd/settings_effective.go）。

const (
	// NamespaceAgentLoop 是官方 agent-loop 插件的设置命名空间（逐字）。
	NamespaceAgentLoop = "agent-loop"
	// FieldMaxParallelToolCalls 是同一步里并发安全工具调用的上限（逐字）。
	FieldMaxParallelToolCalls = "maxParallelToolCalls"
	// DefaultMaxParallelToolCalls 是官方默认值（constants.ts:6）。
	DefaultMaxParallelToolCalls = 10
	// MinMaxParallelToolCalls 是官方 schema 的下限（index.ts:295 的 `.min(1)`）。
	//
	// 1 = 严格串行；官方没有上限（`.max()` 未声明），所以这里也不设上限。
	MinMaxParallelToolCalls = 1
)

// agentLoopNamespace 是 `agent-loop` 的声明（schema / sections / base 同源）。
func agentLoopNamespace() uiNamespace {
	return uiNamespace{
		ns:    NamespaceAgentLoop,
		title: "Agent loop",
		fields: []uiField{
			// 官方是 `z.number().step(1).min(1).default(10)`：有下限、**没有上限**
			// （源码里根本没写 `.max()`）。所以 envelope 上不许出现 meta.max——
			// 发一个假的 max 就是自己发明官方边界（夹具会对拍出来）。
			uiMinOnlyIntegerField(FieldMaxParallelToolCalls, MinMaxParallelToolCalls,
				DefaultMaxParallelToolCalls),
		},
		base: map[string]any{FieldMaxParallelToolCalls: DefaultMaxParallelToolCalls},
	}
}

// buildAgentLoopNamespaceSpecs 把声明转成注册表能收的 NamespaceSpec。
func buildAgentLoopNamespaceSpecs() []NamespaceSpec {
	return []NamespaceSpec{uiNamespaceSpec(agentLoopNamespace())}
}
