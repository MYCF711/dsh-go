package main

// `list_subagent_models`：官方 `dsh-tool-subagent/src/list-models.ts:100-135` 的宿主实现。
//
// # 官方契约（逐条）
//
//   - 工具名 `list_subagent_models`，参数只有 `provider` / `model` 两个可选字符串
//     （:116-125），输出是一段纯文本（:126-129），`isConcurrencySafe: () => true`
//     （:131，只读路由与模型元数据，不改父 agent 状态）。
//   - 调用方必须是有 agent 的工具调用（:46 `list_subagent_models requires a calling agent`）。
//   - 读取调用方会话的 durable 授权（:47）；没有授权时，把可选路由**限制在调用方
//     父路由**（:50-56，`parentAgentOptionsForDelegation(exec.agent)`），拿不到
//     父 provider/model 就报 `cannot discover child LLM routes without a parent provider and model`。
//   - 无参数：列出被授权 provider，`<id> — <name>`，空则 `(no LLM providers)`（:65-71）。
//   - 只给 provider：先查该 provider 是否在授权清单里（否则
//     `LLM provider "<p>" is not allowed for this Session`，:73-77），再查是否注册
//     （否则 `LLM provider "<p>" is not registered; available providers: <…>`，:78），
//     然后列模型（:79-87）。
//   - provider+model：`model` 单独出现报 '`model` requires `provider`'（:62-64），
//     空串报 '`provider` must be non-empty' / '`model` must be non-empty'（:72/:88），
//     路由不在授权清单报 `child LLM route "<p>/<m>" is not allowed for this Session`（:89-91），
//     最后输出模型行 + 推理档清单（:92-97）。
//
// # 本仓的对应
//
//   - "是否注册"= 模型目录里的 provider（`owned_by`，缺省 `gateway`）——与
//     `session.modelCatalog`/`session.selectModel` 用的是同一份目录与同一个身份
//     （`serve_session_api_t04.go:30` 的 `officialGatewayProviderID`）。
//   - `gateway` 恒为已注册：它是进程真实可路由的那条 provider（`--model` 一直走它），
//     目录没有声明 provider 时全部模型都归它。所以官方"零 provider"分支在本仓只会在
//     目录为空且连 gateway 都拿不到时出现——这里仍保留那条文案，不编造别的输出。
//   - 模型元数据（名字/推理档）来自目录声明；目录没声明名字就用 id，没声明档就不编
//     `(default)` 标记——官方那份来自 adapter，本仓的真值源是目录（不伪造 adapter 才有的字段）。
//   - 父路由：优先 ctx 上"当前 agent 实际用的模型"（`dshd run`/ACP 装配时绑、
//     subRunner 跑子代理时换成解析后的子路由）；没有时回退到本会话 v4 的
//     `request/context.model`。两个都没有 = 响亮失败，不拿默认模型冒充父路由。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

const subagentModelsToolName = "list_subagent_models"

// subagentModelsToolDescription 逐字来自官方（list-models.ts:111-115）。
const subagentModelsToolDescription = "Inspect the routes available for explicit subagent model selection. " +
	"No arguments: authorized providers. `provider`: that provider's authorized models. " +
	"`provider` + `model`: that model and its reasoning efforts. " +
	"Query when choosing an explicit child route or reasoning effort. " +
	"Omit optional fields rather than sending empty strings."

var subagentModelsToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "provider": {"type": "string", "description": "Registered LLM provider id."},
    "model": {"type": "string", "description": "Exact model id; requires provider."}
  },
  "additionalProperties": false
}`)

// subagentModelsTool 只读：不写盘、不起进程、不联网。
type subagentModelsTool struct{ home string }

func (t *subagentModelsTool) Name() string            { return subagentModelsToolName }
func (t *subagentModelsTool) Description() string     { return subagentModelsToolDescription }
func (t *subagentModelsTool) Schema() json.RawMessage { return subagentModelsToolSchema }
func (t *subagentModelsTool) Caps() spi.Caps          { return spi.Caps{} }

// ConcurrencySafe 对应官方 `isConcurrencySafe: () => true`：只读路由元数据。
func (t *subagentModelsTool) ConcurrencySafe(json.RawMessage) bool { return true }

type subagentModelsArgs struct {
	Provider *string `json:"provider"`
	Model    *string `json:"model"`
}

func (t *subagentModelsTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var args subagentModelsArgs
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return spi.Result{Hint: "参数不合法：只接受可选的 provider 与 model 两个字符串"},
			fmt.Errorf("list_subagent_models 参数不合法: %w", err)
	}
	sessionID := strings.TrimSpace(spi.SessionFrom(ctx))
	if sessionID == "" {
		return spi.Result{Hint: "这个工具需要在有调用方 agent 的工具轮里使用"},
			errors.New("list_subagent_models requires a calling agent")
	}
	text, err := t.list(ctx, sessionID, args)
	if err != nil {
		return spi.Result{Hint: "先补授权（设置里的 subagent-model-selection-settings）或改用不带参数的调用看有哪些路由"}, err
	}
	return spi.Result{Text: text}, nil
}

func (t *subagentModelsTool) list(ctx context.Context, sessionID string, args subagentModelsArgs) (string, error) {
	if strings.TrimSpace(t.home) == "" {
		return "", errors.New("list_subagent_models 需要 home：模型目录与会话日志没有落点")
	}
	rows, err := catalogModelRows(t.home)
	if err != nil {
		return "", err
	}
	routes, recorded, err := session.SessionSubagentModelSelectionPolicy(t.home, sessionID)
	if err != nil {
		return "", fmt.Errorf("读本会话的子代理模型选择授权失败: %w", err)
	}
	selectionEnabled := recorded
	if !recorded {
		// 官方 list-models.ts:50-56：没有 durable 策略时，可选路由就是调用方父路由。
		parentProvider, parentModel, perr := t.parentRoute(ctx, sessionID, rows)
		if perr != nil {
			return "", perr
		}
		routes = []session.AllowedModelRoute{{Provider: parentProvider, Model: parentModel}}
	}

	if args.Model != nil && args.Provider == nil {
		return "", errors.New("`model` requires `provider`")
	}
	if args.Provider == nil {
		providers := t.authorizedProviders(rows, routes)
		if len(providers) == 0 {
			return "(no LLM providers)", nil
		}
		return strings.Join(providers, "\n"), nil
	}
	if *args.Provider == "" {
		return "", errors.New("`provider` must be non-empty")
	}
	providerID := *args.Provider
	allowedRoutes := make([]session.AllowedModelRoute, 0, len(routes))
	for _, route := range routes {
		if route.Provider == providerID {
			allowedRoutes = append(allowedRoutes, route)
		}
	}
	if len(allowedRoutes) == 0 {
		return "", fmt.Errorf("LLM provider %q is not allowed for this Session", providerID)
	}
	available := t.authorizedProviderIDs(rows, routes)
	if !available[providerID] {
		return "", fmt.Errorf("LLM provider %q is not registered; available providers: %s",
			providerID, joinOrNone(sortedKeys(available)))
	}
	if args.Model == nil {
		if selectionEnabled {
			// 官方 :80-82：只列"目录真的广播了、且在授权清单里"的模型。
			advertised := t.modelsOfProvider(rows, providerID)
			matched := make([]catalogModelRow, 0, len(advertised))
			for _, row := range advertised {
				for _, route := range allowedRoutes {
					if route.Model == row.ID {
						matched = append(matched, row)
						break
					}
				}
			}
			if len(matched) == 0 {
				return fmt.Sprintf("(no advertised models for %s)", providerID), nil
			}
			lines := make([]string, 0, len(matched))
			for _, row := range matched {
				lines = append(lines, modelLine(providerID, row))
			}
			return strings.Join(lines, "\n"), nil
		}
		// 官方 :82 的无策略分支：只解析被授权的那一条父路由。
		row, ok := findCatalogModel(rows, allowedRoutes[0].Model)
		if !ok {
			return fmt.Sprintf("(no advertised models for %s)", providerID), nil
		}
		return modelLine(providerID, row), nil
	}
	if *args.Model == "" {
		return "", errors.New("`model` must be non-empty")
	}
	modelID := *args.Model
	allowed := false
	for _, route := range allowedRoutes {
		if route.Model == modelID {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("child LLM route %q is not allowed for this Session", providerID+"/"+modelID)
	}
	row, ok := findCatalogModel(rows, modelID)
	if !ok {
		return "", fmt.Errorf("模型 %q 不在模型目录里（provider %q）；目录只声明了 %s",
			modelID, providerID, strings.Join(t.modelIDsOfProvider(rows, providerID), ","))
	}
	efforts := reasoningLevels(row)
	lines := make([]string, 0, len(efforts))
	for _, level := range efforts {
		// 官方每档带 name/description，本仓目录只声明 id 与 wire；不伪造描述，
		// 也不编 `(default)`（目录没有声明 defaultEffort 这个概念）。
		lines = append(lines, fmt.Sprintf("%s — %s", level, level))
	}
	effortText := "(no advertised reasoning efforts)"
	if len(lines) > 0 {
		effortText = strings.Join(lines, "\n")
	}
	return modelLine(providerID, row) + "\nReasoning efforts:\n" + effortText, nil
}

// parentRoute 解析"调用方 agent 的父路由"。
//
// 顺序：ctx 上当前 agent 模型 → 本会话 v4 里最新一条 `request/context.model`。
// 两个都没有就按官方文案响亮失败（:52-54）——不许拿默认模型或目录第一条冒充。
func (t *subagentModelsTool) parentRoute(ctx context.Context, sessionID string, rows []catalogModelRow) (string, string, error) {
	model := strings.TrimSpace(spi.AgentModelFrom(ctx))
	if model == "" {
		recorded, err := newSessionSource(t.home, "").Model(sessionID)
		if err == nil {
			model = strings.TrimSpace(recorded)
		} else {
			return "", "", fmt.Errorf("读会话 %q 的模型记录失败: %w", sessionID, err)
		}
	}
	if model == "" {
		return "", "", errors.New("cannot discover child LLM routes without a parent provider and model")
	}
	provider, _ := providerOfModel(rows, model)
	return provider, model, nil
}

// authorizedProviders 返回"被授权路由里出现、且真的注册了"的 provider 行，按 id 排序。
func (t *subagentModelsTool) authorizedProviders(rows []catalogModelRow, routes []session.AllowedModelRoute) []string {
	registered := t.registeredProviderNames(rows)
	seen := make(map[string]bool, len(routes))
	lines := make([]string, 0, len(routes))
	for _, route := range routes {
		if seen[route.Provider] || !registered[route.Provider] {
			continue
		}
		seen[route.Provider] = true
		// 官方 `<id> — <name>`；本仓目录没有 provider 名字段，名字 = id
		// （与 session.modelCatalog 的 provider group 同一边界）。
		lines = append(lines, route.Provider+" — "+route.Provider)
	}
	sort.Strings(lines)
	return lines
}

// authorizedProviderIDs 同 authorizedProviders，但给"是否注册"判定用。
func (t *subagentModelsTool) authorizedProviderIDs(rows []catalogModelRow, routes []session.AllowedModelRoute) map[string]bool {
	registered := t.registeredProviderNames(rows)
	out := make(map[string]bool, len(routes))
	for _, route := range routes {
		if registered[route.Provider] {
			out[route.Provider] = true
		}
	}
	return out
}

// registeredProviderNames 是本宿主的 provider 注册表：目录里的 owned_by，
// 加上恒可路由的进程网关（gateway）。
func (t *subagentModelsTool) registeredProviderNames(rows []catalogModelRow) map[string]bool {
	registered := map[string]bool{officialGatewayProviderID: true}
	for _, row := range rows {
		provider, _ := providerOfModel(rows, row.ID)
		registered[provider] = true
	}
	return registered
}

// modelsOfProvider 返回目录里属于该 provider 的模型行（保持目录顺序）。
func (t *subagentModelsTool) modelsOfProvider(rows []catalogModelRow, provider string) []catalogModelRow {
	out := make([]catalogModelRow, 0, len(rows))
	for _, row := range rows {
		owner, _ := providerOfModel(rows, row.ID)
		if owner == provider {
			out = append(out, row)
		}
	}
	return out
}

func (t *subagentModelsTool) modelIDsOfProvider(rows []catalogModelRow, provider string) []string {
	models := t.modelsOfProvider(rows, provider)
	ids := make([]string, 0, len(models))
	for _, row := range models {
		ids = append(ids, row.ID)
	}
	return ids
}

// modelLine 对应官方 `modelLine`（list-models.ts:35-37）。
func modelLine(provider string, row catalogModelRow) string {
	name := row.Name
	if strings.TrimSpace(name) == "" {
		name = row.ID
	}
	return provider + "/" + row.ID + " — " + name
}

func findCatalogModel(rows []catalogModelRow, model string) (catalogModelRow, bool) {
	for _, row := range rows {
		if row.ID == model {
			return row, true
		}
	}
	return catalogModelRow{}, false
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}

// subagentModelsModule 把发现工具挂进工具表。
//
// 为什么 always-on：官方 `modelDiscovery` 是工具实例配置，rc.2 shipped standard
// preset 显式 true（`dsh-web-app/presets/standard.patch.yml:105`）；本仓没有第二份
// 实例配置，等价物就是随 `subagent` 一起注册（同属 deferred 的官方 shipped 形状）。
func subagentModelsModule(home string) kernel.Module {
	return kernel.Module{
		ID:       "tool-subagent-models",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			return reg.Register(&subagentModelsTool{home: home})
		},
	}
}
