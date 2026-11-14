package settingsform

// 官方委派（subagent）的两个设置命名空间：`subagent` 与
// `subagent-model-selection-settings`。
//
// # 为什么这两条必须存在
//
// 官方 UI 包 `dsh-client-ui-settings-subagent` 的页面在
// `src/client/index.ts:95` 挂了
// `ctx.configForms.whileServed([SUBAGENT_NS, SUBAGENT_MODEL_SELECTION_NS], ...)`，
// 两者**都不服务**时整页不出现（GUI 侧 4 条 MISSING 设置页之一）。
//
// # 权威源（随 rc.2 包发布的 TypeScript 原件）
//
//	<official-rc2>/dsh/node_modules/@deepseek-ai/
//	  dsh-subagent/src/index.ts:195-198
//	    static Config = z.object({
//	      maxDepth: z.number().step(1).min(0).max(Number.MAX_SAFE_INTEGER).default(1).volatile(),
//	      maxActiveSubagents: z.number().step(1).min(1).max(Number.MAX_SAFE_INTEGER).default(8).volatile(),
//	    })
//	  dsh-tool-subagent/src/model-selection-settings.ts:52-55
//	    static Config = z.object({
//	      enabled: z.boolean().default(false).volatile(),
//	      allowedModels: z.array(z.object({
//	        provider: z.string().min(1).required(),
//	        model: z.string().min(1).required(),
//	      })).default([]).volatile(),
//	    })
//	  dsh-client-ui-settings-subagent/src/client/subagent-limits-card-controller.ts:34-40
//	    limitField('maxDepth', 0) / limitField('maxActiveSubagents', 1)
//	  dsh-client-ui-settings-subagent/src/client/subagent-model-selection-card-controller.ts:12
//	    export const SUBAGENT_MODEL_SELECTION_NS = 'subagent-model-selection-settings'
//
// # 线上 envelope（实跑取证）
//
// tmp/GO8-RC2/_hs4/dump-hs4.mjs（按上面 zod 声明逐行重建）→
// _hs4/official-envelopes-hs4.json：
//
//	subagent:
//	  maxDepth            {"type":"number","meta":{"step":1,"min":0,"max":9007199254740991,"default":1}}
//	  maxActiveSubagents  {"type":"number","meta":{"step":1,"min":1,"max":9007199254740991,"default":8}}
//	subagent-model-selection-settings:
//	  enabled        {"type":"boolean","meta":{"default":false}}
//	  allowedModels  {"type":"array","meta":{"default":[]},"inner":<object{provider,model}>}
//
// `Number.MAX_SAFE_INTEGER` = 9007199254740991，宿主用 MaxSafeInteger 同值发布
// （"不设实际上限"在官方就是这么写的，不是本仓另发明的边界）。
//
// # 本仓的消费方
//
//   - `subagent.maxActiveSubagents` → cmd/dshd 的子代理并发池
//     `subagentGate`（cmd/dshd/subagent_gate.go:31-37 的官方默认 8）。
//   - `subagent.maxDepth` → cmd/dshd/subagent.go 的 `maxDepth`（原为硬编码 1，
//     正好等于官方默认值）。
//   - `subagent-model-selection-settings` → **本仓没有消费方**（登记缺口，见下）。
//
// # 缺口（如实登记，不造假的消费者）
//
// `subagent-model-selection-settings` 的官方语义是"新会话可以**按模型路由**授权
// 子代理"：`dsh-tool-subagent/src/model-selection-settings.ts` 在
// `agent/created` 时把允许的 `provider/model` 路由写成会话投影
// `subagent/model-selection-policy`，模型之后就能在委派时点名 `provider`+`model`。
// 本仓的委派是**同步**的 `subRunner`，子代理固定用父进程的同一个模型
// （cmd/dshd/subagent.go:33 `model string`，工具 schema 里根本没有 provider/model
// 参数），也没有"按会话授权路由"的投影通道。所以这一条**只做可读写 + 官方默认值
// （enabled 默认 false，与官方 shipped 组合一致）+ 往返一致**，写进去的值会被下一次
// describe 读回来，但不会有任何行为改变——这是缺口本身，不是静默成功被藏起来：
// 缺口同时登记进 docs/host-stable-gap-register 与交付报告。

const (
	// NamespaceSubagent 是官方委派插件的设置命名空间（逐字）。
	NamespaceSubagent = "subagent"
	// NamespaceSubagentModelSelection 是官方"子代理模型选择"授权命名空间（逐字，
	// 名字带 -settings 后缀：`subagent-model-selection-settings`）。
	NamespaceSubagentModelSelection = "subagent-model-selection-settings"
	// FieldMaxDepth 是嵌套委派的最大深度（逐字）。
	FieldMaxDepth = "maxDepth"
	// FieldMaxActiveSubagents 是同一条激活树里同时在飞的子代理上限（逐字）。
	FieldMaxActiveSubagents = "maxActiveSubagents"
	// FieldSubagentModelSelectionEnabled 是"新会话是否启用按路由选择子代理模型"（逐字）。
	FieldSubagentModelSelectionEnabled = "enabled"
	// FieldAllowedModels 是允许的子代理路由清单（逐字）。
	FieldAllowedModels = "allowedModels"
)

const (
	// DefaultMaxDepth 是官方 `maxDepth` 默认值（index.ts:196 的 `.default(1)`）。
	DefaultMaxDepth = 1
	// MinMaxDepth 是官方下限（index.ts:196 的 `.min(0)`）：0 = 不允许再往下委派。
	MinMaxDepth = 0
	// DefaultMaxActiveSubagents 是官方 `maxActiveSubagents` 默认值（index.ts:197 的 `.default(8)`）。
	DefaultMaxActiveSubagents = 8
	// MinMaxActiveSubagents 是官方下限（index.ts:197 的 `.min(1)`）。
	//
	// 不能是 0：0 个并发槽 = 委派永远拿不到槽，那是"禁掉委派"而不是"限额"。
	MinMaxActiveSubagents = 1
	// DefaultSubagentModelSelectionEnabled 是官方默认（model-selection-settings.ts:53 的 `.default(false)`）。
	//
	// **与官方 shipped 组合一致：默认关闭。** 用户裁定「官方默认关的就默认关，
	// 只是能被打开」——这里就是这种：默认关，但写 true 会真的存进去（缺口是
	// "存了没人消费"，不是"不许打开"）。
	DefaultSubagentModelSelectionEnabled = false
)

// subagentNamespace 是 `subagent` 的声明。
func subagentNamespace() uiNamespace {
	return uiNamespace{
		ns:    NamespaceSubagent,
		title: "Subagent limits",
		fields: []uiField{
			uiIntegerField(FieldMaxDepth, MinMaxDepth, MaxSafeInteger, DefaultMaxDepth),
			uiIntegerField(FieldMaxActiveSubagents, MinMaxActiveSubagents, MaxSafeInteger, DefaultMaxActiveSubagents),
		},
		base: map[string]any{
			FieldMaxDepth:           DefaultMaxDepth,
			FieldMaxActiveSubagents: DefaultMaxActiveSubagents,
		},
	}
}

// subagentModelSelectionNamespace 是 `subagent-model-selection-settings` 的声明。
func subagentModelSelectionNamespace() uiNamespace {
	return uiNamespace{
		ns:    NamespaceSubagentModelSelection,
		title: "Subagent model selection",
		fields: []uiField{
			uiBooleanField(FieldSubagentModelSelectionEnabled, DefaultSubagentModelSelectionEnabled),
		},
		// allowedModels 是"对象数组"，L1 的 sections 表单装不下（FieldSpec 只有
		// 三种标量），但官方 schema 里它是真字段，所以只发 envelope；
		// 语义校验（非空 provider/model、路由不重复）走 check。
		schemaOnly: []schemaOnlyField{{
			name: FieldAllowedModels,
			node: func(b *schemaEnvelope) int {
				// 官方 AllowedModelRouteSchema（model-selection.ts:17-20）：
				// provider/model 都是 `z.string().min(1).required()`。
				provider := b.stringNode(map[string]any{"min": 1, "required": true})
				model := b.stringNode(map[string]any{"min": 1, "required": true})
				item := b.node("object", map[string]any{"default": map[string]any{}},
					map[string]any{"dict": map[string]any{"provider": provider, "model": model}})
				return b.arrayNode(item, map[string]any{"default": []any{}})
			},
		}},
		base: map[string]any{
			FieldSubagentModelSelectionEnabled: DefaultSubagentModelSelectionEnabled,
			// 官方默认是空数组（`default([])`）：一个路由都不授权。
			FieldAllowedModels: []any{},
		},
		check: checkAllowedModels,
	}
}

// checkAllowedModels 是官方 `assertAllowedModelRoutes`
// （dsh-tool-subagent/src/model-selection.ts:42-62）的等价校验：
//
//   - 必须是数组；
//   - 每一项必须是对象，且 provider / model 都是**非空**字符串；
//   - 同一个 provider/model 路由不许重复（官方原话：repeats route）。
//
// 官方在装配期抛错，本仓在写路径当场 `invalid_value`（并说清是哪一项）——
// 写一份坏清单进去等于让"授权列表"变成不可解释的状态，那是静默成功。
func checkAllowedModels(value map[string]any) error {
	raw, present := value[FieldAllowedModels]
	if !present || raw == nil {
		return nil
	}
	routes, ok := raw.([]any)
	if !ok {
		return newInvalidTypeError([]string{NamespaceSubagentModelSelection, FieldAllowedModels}, "array", raw)
	}
	seen := make(map[string]bool, len(routes))
	for index, candidate := range routes {
		route, ok := candidate.(map[string]any)
		if !ok {
			return &Error{
				Code: "invalid_value",
				Message: joinPath([]string{NamespaceSubagentModelSelection, FieldAllowedModels}) +
					" 的第 " + formatInt(int64(index)) + " 项不是对象",
				Hint:   "每项形如 {\"provider\": \"...\", \"model\": \"...\"}",
				Status: 400,
			}
		}
		provider, providerOK := route["provider"].(string)
		model, modelOK := route["model"].(string)
		if !providerOK || !modelOK || provider == "" || model == "" {
			return &Error{
				Code: "invalid_value",
				Message: joinPath([]string{NamespaceSubagentModelSelection, FieldAllowedModels}) +
					" 的第 " + formatInt(int64(index)) + " 项缺少非空的 provider / model",
				Hint:   "官方 AllowedModelRouteSchema 要求两个字段都非空",
				Status: 400,
			}
		}
		key := provider + "/" + model
		if seen[key] {
			return &Error{
				Code: "invalid_value",
				Message: joinPath([]string{NamespaceSubagentModelSelection, FieldAllowedModels}) +
					" 重复了路由 " + quoteValue(key),
				Hint:   "同一 provider/model 只许出现一次（官方断言 repeats route）",
				Status: 400,
			}
		}
		seen[key] = true
	}
	return nil
}

// buildSubagentNamespaceSpecs 返回两条委派命名空间（顺序与官方页面的服务顺序一致）。
func buildSubagentNamespaceSpecs() []NamespaceSpec {
	return []NamespaceSpec{
		uiNamespaceSpec(subagentNamespace()),
		uiNamespaceSpec(subagentModelSelectionNamespace()),
	}
}
