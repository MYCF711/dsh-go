package session

// 子代理「模型选择」授权的 durable 事实 + 投影（官方 @deepseek-ai/dsh-tool-subagent）。
//
// # 官方原件（rc.2，随包发布的 TypeScript）
//
//   - 事件声明：`dsh-tool-subagent/src/model-selection-state.ts:9-22`
//     `SessionEventMap['subagent/model-selection-policy'] = { allowedModels: AllowedModelRoute[] }`
//     —— 注释原文："Appended before the first model request; absence restricts explicit
//     selection to the current parent route. Log-only: it carries no `surfaceOp` and never
//     enters model history."
//   - 路由形状：`model-selection.ts:9-20`
//     `{provider: string.min(1), model: string.min(1)}`
//   - 校验：`model-selection.ts:42-62`（assertAllowedModelRoutes：非空 id、同一路由不许重复）
//   - 投影：`model-selection-state.ts:33-47`
//     key `subagentModelSelectionPolicy`、stateVersion 1、
//     stateSchema `array({provider,model}.strict()).min(1).nullable()`、
//     init `() => null`、apply **只认第一条** policy 事件（`policy !== null` 即原样返回），
//     事件里的 routes 为空时抛 `subagent/model-selection-policy requires at least one route`。
//   - 读取：`model-selection-state.ts:55-60`（返回 detached 副本；undefined = 无授权，
//     显式选择只能点名父路由）。
//
// # 本仓落点
//
// 官方把这份策略当**宿主私有投影**（`satisfies ProjectionDefinition<...>`，没有 wire view），
// 所以这里注册进 `DefaultProjectionRegistry`（`View` 为 nil = 不上线），授权判定走
// `SubagentModelSelectionPolicyOf` 的同一条折叠逻辑——投影与消费方只有一份真值。

import (
	"encoding/json"
	"fmt"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/storage"
)

const (
	// EventSubagentModelSelectionPolicy 是官方 durable 事件名（逐字）。
	EventSubagentModelSelectionPolicy = "subagent/model-selection-policy"
	// ProjectionKeySubagentModelSelectionPolicy 是官方投影键（逐字）。
	ProjectionKeySubagentModelSelectionPolicy = "subagentModelSelectionPolicy"
)

// AllowedModelRoute 是一条被授权的子代理 LLM 路由（官方 AllowedModelRoute）。
type AllowedModelRoute struct {
	// Provider 是目录里的 provider 身份（本宿主 = 目录 owned_by，缺省 gateway）。
	Provider string `json:"provider"`
	// Model 是 provider 拥有的精确模型 id。
	Model string `json:"model"`
}

// ModelRouteKey 是官方 `modelRouteKey`：`provider\0model`（model-selection.ts:33-35）。
func ModelRouteKey(route AllowedModelRoute) string {
	return route.Provider + "\x00" + route.Model
}

// ValidateAllowedModelRoutes 逐条复刻官方 `assertAllowedModelRoutes`
// （model-selection.ts:42-62）：非空 provider/model、同一路由不许重复。
//
// 为什么不加严：官方只查这两件事；多查一条就会让官方写下的合法清单在本仓打不开。
func ValidateAllowedModelRoutes(routes []AllowedModelRoute) error {
	seen := make(map[string]bool, len(routes))
	for _, route := range routes {
		if strings.TrimSpace(route.Provider) == "" || strings.TrimSpace(route.Model) == "" {
			return fmt.Errorf("subagent model selection requires non-empty provider and model ids")
		}
		key := ModelRouteKey(route)
		if seen[key] {
			return fmt.Errorf("subagent model selection repeats route %q", route.Provider+"/"+route.Model)
		}
		seen[key] = true
	}
	return nil
}

// ParseAllowedModelRoutes 把设置文档里的 JSON 值解析成路由清单并校验。
//
// 设置层（`subagent-model-selection-settings.allowedModels`）是 `[]any`（写盘再读回
// 之后没有 Go 类型），这里负责形状转换；语义校验与官方是**同一个函数**
// （ValidateAllowedModelRoutes），不写第二份判据。
func ParseAllowedModelRoutes(raw any) ([]AllowedModelRoute, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("subagent model selection requires an array of routes")
	}
	routes := make([]AllowedModelRoute, 0, len(list))
	for _, candidate := range list {
		route, ok := candidate.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("subagent model selection requires non-empty provider and model ids")
		}
		provider, providerOK := route["provider"].(string)
		model, modelOK := route["model"].(string)
		if !providerOK || !modelOK || provider == "" || model == "" {
			return nil, fmt.Errorf("subagent model selection requires non-empty provider and model ids")
		}
		routes = append(routes, AllowedModelRoute{Provider: provider, Model: model})
	}
	if err := ValidateAllowedModelRoutes(routes); err != nil {
		return nil, err
	}
	return routes, nil
}

// SubagentModelSelectionPolicyData 组装 durable 事件负载（官方 `{allowedModels}`）。
func SubagentModelSelectionPolicyData(routes []AllowedModelRoute) map[string]any {
	return map[string]any{"allowedModels": routes}
}

// AppendSubagentModelSelectionPolicy 在**第一次模型请求之前**把本会话的授权路由写进 v4 日志。
//
// 空清单按官方投影的同一判据拒绝（`model-selection-state.ts:42-44`）：写一条空授权进去
// 等于给会话一个"什么都不能选"的策略，读侧还要再解释一次，不如在写侧当场失败。
func AppendSubagentModelSelectionPolicy(log *Log, routes []AllowedModelRoute) error {
	if log == nil {
		return fmt.Errorf("写 %s 需要会话日志写入端", EventSubagentModelSelectionPolicy)
	}
	if err := ValidateAllowedModelRoutes(routes); err != nil {
		return err
	}
	if len(routes) == 0 {
		return fmt.Errorf("subagent/model-selection-policy requires at least one route")
	}
	return log.Append(EventSubagentModelSelectionPolicy, SubagentModelSelectionPolicyData(routes))
}

// decodeSubagentModelSelectionPolicyData 校验一条 durable 事件的负载。
//
// 严格两件事与官方一致：`allowedModels` 必须是非空数组、每项 id 非空且不重复。
// 多一个键不拒绝（官方 zod 对负载用的是非 strict 的 object？——不，官方只解构
// `allowedModels`，多余键不参与判定；本仓同样只读这一个键，不擅自加严）。
func decodeSubagentModelSelectionPolicyData(raw json.RawMessage) ([]AllowedModelRoute, error) {
	var payload struct {
		AllowedModels *[]AllowedModelRoute `json:"allowedModels"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("%s 负载解不开: %w", EventSubagentModelSelectionPolicy, err)
	}
	if payload.AllowedModels == nil {
		return nil, fmt.Errorf("%s 缺少 allowedModels 数组", EventSubagentModelSelectionPolicy)
	}
	routes := *payload.AllowedModels
	if err := ValidateAllowedModelRoutes(routes); err != nil {
		return nil, err
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("subagent/model-selection-policy requires at least one route")
	}
	return routes, nil
}

// SubagentModelSelectionPolicyOf 折出本会话的授权路由。
//
// 语义逐条照抄官方投影 apply（model-selection-state.ts:38-46）：
//   - 第一条 `subagent/model-selection-policy` 生效；
//   - 之后再有同类事件一律**忽略**（官方 `policy !== null` 直接返回）；
//   - 负载不合法 = 响亮失败（官方 apply 抛错；这里返回 error，调用方必须拒绝委派）。
//
// 返回的 `recorded=false` 表示日志里没有这条 durable 决定 → 官方语义是
// "显式选择只能点名父路由"。
func SubagentModelSelectionPolicyOf(events []v4.Event) ([]AllowedModelRoute, bool, error) {
	for i := range events {
		if events[i].Type != EventSubagentModelSelectionPolicy {
			continue
		}
		routes, err := decodeSubagentModelSelectionPolicyData(events[i].Data)
		if err != nil {
			return nil, false, err
		}
		detached := make([]AllowedModelRoute, len(routes))
		copy(detached, routes)
		return detached, true, nil
	}
	return nil, false, nil
}

// ReadSubagentModelSelectionPolicy 从一份会话日志路径读策略（官方 stateOf(session) 的等价）。
func ReadSubagentModelSelectionPolicy(path string) ([]AllowedModelRoute, bool, error) {
	raw, err := v4.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return SubagentModelSelectionPolicyOf(raw.Events)
}

// SessionSubagentModelSelectionPolicy 按会话 id 读策略（跨 projectKey 按 id 找文件）。
func SessionSubagentModelSelectionPolicy(home, sessionID string) ([]AllowedModelRoute, bool, error) {
	if strings.TrimSpace(home) == "" {
		return nil, false, fmt.Errorf("读子代理模型选择策略需要 home（会话日志没有落点）")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, false, fmt.Errorf("读子代理模型选择策略需要会话 id")
	}
	path, err := FindByID(home, sessionID)
	if err != nil {
		return nil, false, err
	}
	return ReadSubagentModelSelectionPolicy(path)
}

// SessionExists 报这个 home 下是否存在该 id 的会话日志。
//
// 只给"官方语义要求父会话缺席时安静跳过"的调用方用（子会话继承授权：
// 官方 `ctx.sessions.get(parentId) === undefined` 就不继承）。授权消费方自己
// 不该用它来做存在性判断——拿不到日志就是响亮失败，不许当成"没有策略"。
func SessionExists(home, sessionID string) bool {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(sessionID) == "" {
		return false
	}
	_, err := FindByID(home, sessionID)
	return err == nil
}

// InheritSubagentModelSelectionPolicy 让 durable 子会话继承父会话的授权
// （官方 `origin === 'subagent'` 分支，model-selection-settings.ts:64-69）。
//
// 父会话不存在 = 官方 `ctx.sessions.get(parentId) === undefined`：不继承、不写，
// 不算错误（外部/历史子会话可能没有本机父日志）。父日志存在但**读坏 / 策略负载
// 坏**必须响亮失败——静默跳过等于子代理在更小的授权面上跑，那是看不见的降级。
func InheritSubagentModelSelectionPolicy(log *Log, home, parentSessionID string) error {
	parent := strings.TrimSpace(parentSessionID)
	if parent == "" || !SessionExists(home, parent) {
		return nil
	}
	routes, recorded, err := SessionSubagentModelSelectionPolicy(home, parent)
	if err != nil {
		return fmt.Errorf("读父会话 %q 的子代理模型选择授权失败: %w", parent, err)
	}
	if !recorded {
		return nil
	}
	if err := AppendSubagentModelSelectionPolicy(log, routes); err != nil {
		return fmt.Errorf("子会话继承父会话 %q 的子代理模型选择授权失败: %w", parent, err)
	}
	return nil
}

// subagentModelSelectionPolicySchema 是官方
// `array({provider,model}.strict()).min(1).nullable()`（model-selection-state.ts:24-27）。
//
// 状态在 checkpoint 里是 JSON 反序列化后的形状（`[]any` + `map[string]any`），
// 所以这里按那个形状严格校验：多余键拒绝（官方 `.strict()` 同样是拒绝）。
var subagentModelSelectionPolicySchema = storage.Nullable(storage.SchemaFunc(func(value any) error {
	list, ok := value.([]any)
	if !ok {
		return fmt.Errorf("subagentModelSelectionPolicy 期望数组或 null，收到 %T", value)
	}
	if len(list) == 0 {
		return fmt.Errorf("subagentModelSelectionPolicy 至少要有一条路由")
	}
	seen := make(map[string]bool, len(list))
	for index, candidate := range list {
		route, ok := candidate.(map[string]any)
		if !ok {
			return fmt.Errorf("subagentModelSelectionPolicy 的第 %d 项不是对象", index)
		}
		for key := range route {
			if key != "provider" && key != "model" {
				return fmt.Errorf("subagentModelSelectionPolicy 的第 %d 项有未声明的字段 %q", index, key)
			}
		}
		provider, providerOK := route["provider"].(string)
		model, modelOK := route["model"].(string)
		if !providerOK || !modelOK || provider == "" || model == "" {
			return fmt.Errorf("subagentModelSelectionPolicy 的第 %d 项缺少非空 provider / model", index)
		}
		key := provider + "/" + model
		if seen[key] {
			return fmt.Errorf("subagentModelSelectionPolicy 重复了路由 %q", key)
		}
		seen[key] = true
	}
	return nil
}))

// SubagentModelSelectionUnit 是官方 `subagentModelSelectionProjectionDefinition`
// （model-selection-state.ts:33-47）的 Go 形状。
//
// View 为 nil：官方这份投影是 **host-only**（源码注释原文 "Host-only projection"），
// 不投影到 `session.projections` 的 wire 值——这里不发明一个官方没有的线上字段。
func SubagentModelSelectionUnit() ProjectionUnit {
	return ProjectionUnit{
		Key:          ProjectionKeySubagentModelSelectionPolicy,
		StateVersion: 1,
		Schema:       subagentModelSelectionPolicySchema,
		Init:         func(v4.Header, int64) any { return nil },
		Apply:        applySubagentModelSelectionPolicy,
	}
}

// applySubagentModelSelectionPolicy 复刻官方 apply 的"只认第一条"。
//
// Apply 没有 error 返回（本仓投影单元的签名），所以坏负载在这里的处置是
// **fail-closed**：保持 nil（= 没有授权 = 显式选择只能点名父路由），
// 而严格的授权读取（SubagentModelSelectionPolicyOf）会把同一条坏负载报成 error，
// 委派路径拿到的是 loud 失败，不是静默放行。
func applySubagentModelSelectionPolicy(state any, event v4.Event) any {
	if state != nil {
		return state
	}
	if event.Type != EventSubagentModelSelectionPolicy {
		return state
	}
	routes, err := decodeSubagentModelSelectionPolicyData(event.Data)
	if err != nil {
		return state
	}
	list := make([]any, 0, len(routes))
	for _, route := range routes {
		list = append(list, map[string]any{"provider": route.Provider, "model": route.Model})
	}
	return list
}
