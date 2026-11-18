package main

// 子代理「模型选择」的委派侧消费方：授权校验 + 路由解析。
//
// # 官方契约（rc.2，随包 TypeScript）
//
// 判定与文案的唯一来源是 `dsh-tool-subagent/src/model-selection.ts`：
//
//   - `hasDelegationModelRequest`（:76-80）：provider / model / reasoning_effort
//     出现任意一个就算"显式选择"；一个都没有时子代理沿用既定默认（父路由），
//     本文件同样**不读目录、不读策略**，行为与改动前逐字节一致。
//   - `requestedAgentOptions`（:100-128）：
//       1. 未显式选择 → 返回配置默认；
//       2. `!enabled` → `child model selection is disabled for this tool instance`；
//       3. 三个字段**出现但为空串** → `child LLM `<field>` must be non-empty`；
//       4. provider/model 必须成对；只给 reasoning_effort → 单独一条错误；
//       5. `assertAllowedModelSelection` 校验白名单；
//       6. 显式给了模型但省略 effort → **清掉**继承来的父 effort（用模型默认档）。
//   - `assertAllowedModelSelection`（:136-150）：
//       没有 durable 策略 → 只允许 `provider/model` 等于**父路由**；
//       有策略 → 必须是清单里的精确一条；
//       否则 `child LLM route "<p>/<m>" is not allowed for this Session`，
//       无策略时再追加 `; without a recorded model-selection policy, explicit selection
//       is limited to the parent route`。
//   - `preflightChildLlmRoute`（:173-193）：路由必须能被真实 provider 解析
//       （`cannot select child LLM values without an effective provider and model`）。
//
// # 本仓的等价物
//
// 本宿主的 provider 身份 = 模型目录的 `owned_by`（缺省 `gateway`，见
// `serve_session_api_t04.go:30` 与 `ResolveModelSelection` 的同一条注释），
// 客户端按"模型 → 目录 provider 配置"取（`resolveServeProvider`）。
// 所以 preflight 在本仓是：目录能解析出这条路由就必须一致、解析不出时只有
// 进程网关（gateway）能接——其余一律响亮失败，不静默退回父模型。
//
// `enabled` 这一层：官方 `modelSelection` 是工具实例配置，rc.2 shipped standard
// preset 显式为 true（`dsh-web-app/presets/standard.patch.yml:104`）。本仓没有
// 第二份工具实例配置，等价物就是"始终启用"——与 shipped 组合一致。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"dshgo/internal/agent"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// childLlmRoute 是一次委派**实际生效**的路由。
type childLlmRoute struct {
	provider string
	model    string
	// reasoning 是这次子代理请求带的推理档；空 = 一行都不带（模型默认档）。
	reasoning string
	chat      agent.Chat
}

// hasDelegationModelRequest 是官方 `hasDelegationModelRequest` 的逐条等价
// （model-selection.ts:76-80）：看的是"字段是否出现"，空串也算出现。
func hasDelegationModelRequest(req spi.SubagentRequest) bool {
	return req.Provider != nil || req.Model != nil || req.ReasoningEffort != nil
}

// resolveChildRoute 解析这次委派的路由；错误全部来自官方判据（文案逐字）。
//
// 没显式选择时直接返回父路由（chat/model 原样），**不碰**目录与策略：
// 既有调用方的请求形状、客户端对象、日志字节一个都不变。
func (s *subRunner) resolveChildRoute(ctx context.Context, req spi.SubagentRequest) (childLlmRoute, error) {
	route := childLlmRoute{model: s.model, chat: s.chat}
	if !hasDelegationModelRequest(req) {
		return route, nil
	}
	// 官方 `!enabled` 分支。本仓 shipped 组合是启用的，只有装配坏了（没有 chat）
	// 才会走到这里——那也必须响亮失败，不能拿一个大模型兜底。
	if s.chat == nil {
		return childLlmRoute{}, errors.New("child model selection is disabled for this tool instance")
	}
	// 官方 assertNonEmpty（:83-87）：出现空串就是错误，不许"空串 = 没给"。
	if req.Provider != nil && *req.Provider == "" {
		return childLlmRoute{}, errors.New("child LLM `provider` must be non-empty")
	}
	if req.Model != nil && *req.Model == "" {
		return childLlmRoute{}, errors.New("child LLM `model` must be non-empty")
	}
	if req.ReasoningEffort != nil && *req.ReasoningEffort == "" {
		return childLlmRoute{}, errors.New("child LLM `reasoning_effort` must be non-empty")
	}
	if req.Provider == nil || req.Model == nil {
		if req.ReasoningEffort == nil {
			return childLlmRoute{}, errors.New("child LLM `provider` and `model` must be supplied together")
		}
		return childLlmRoute{}, errors.New(
			"child LLM `reasoning_effort` requires both `provider` and `model`; supply both or omit all three fields")
	}
	provider, model := *req.Provider, *req.Model
	effort := ""
	if req.ReasoningEffort != nil {
		effort = *req.ReasoningEffort
	}

	rows, err := s.delegationCatalog()
	if err != nil {
		return childLlmRoute{}, err
	}
	parentProvider, _ := providerOfModel(rows, s.model)
	routes, recorded, err := s.delegationPolicy(ctx, req)
	if err != nil {
		return childLlmRoute{}, err
	}
	if err := assertAllowedModelSelection(recorded, routes, parentProvider, s.model, provider, model); err != nil {
		return childLlmRoute{}, err
	}

	owner, declared := providerOfModel(rows, model)
	switch {
	case declared && owner != provider:
		// 本宿主的 provider 身份就是目录 owned_by：模型属于 A 却点名 B，客户端会
		// 从 B 的配置却发 A 的模型 id——那是把路由配置错配成"看起来成功"的请求。
		return childLlmRoute{}, fmt.Errorf(
			"模型 %q 在目录里的 provider 是 %q，与显式选择的 %q 不一致（本宿主的 provider 身份 = 目录 owned_by）",
			model, owner, provider)
	case !declared && provider != officialGatewayProviderID:
		// 官方 preflight 要求路由能被真实 provider 解析；本仓的 provider 表来自目录，
		// 目录里没有这条模型 = 没有能出来的客户端（gateway 之外的 provider 配置
		// 都按模型 id 存），所以响亮失败而不是退回父模型。
		return childLlmRoute{}, fmt.Errorf(
			"LLM provider %q 没有目录声明能提供模型 %q；可用路由：%s",
			provider, model, availableRouteList(rows))
	}

	route.provider = provider
	route.model = model
	// 显式选择但没有 effort = 用所选模型自己的默认档（官方 :125-126 的
	// `reasoningEffort: request.reasoning_effort === undefined ? undefined : ...`），
	// 子代理不会继承父档。
	route.reasoning = effort
	if effort != "" {
		for i := range rows {
			if rows[i].ID != model {
				continue
			}
			levels := reasoningLevels(rows[i])
			if len(levels) == 0 {
				break
			}
			known := false
			for _, level := range levels {
				if level == effort {
					known = true
					break
				}
			}
			if !known {
				// 与 `ResolveModelSelection` 的同一条判据（serve_session_api_t04.go）：
				// 目录声明了档位就必须是其中一档，未知档不许当成自定义串发出去。
				return childLlmRoute{}, fmt.Errorf(
					"模型 %q 没有 reasoningEffort=%q 的声明（目录只声明了 %s）",
					model, effort, strings.Join(levels, ","))
			}
			break
		}
	}
	if provider != officialGatewayProviderID {
		cfg, hasProvider, err := resolveServeProvider(s.home, model)
		if err != nil {
			return childLlmRoute{}, err
		}
		if !hasProvider {
			return childLlmRoute{}, fmt.Errorf(
				"模型 %q 的 provider %q 在目录里没有可用的 baseURL/凭据：子代理不能静默退回父模型",
				model, provider)
		}
		client := cfg.Client()
		client.OnExtensionError = reportLLMExtensionError
		route.chat = client
	}
	return route, nil
}

// delegationCatalog 读一次真实模型目录（只在显式选择时调用）。
//
// 目录损坏是真错误（`catalogModelRows` 的原话）：把"读不懂目录"降级成"没有模型"
// 会让授权校验在一个假目录上通过或拒绝。
func (s *subRunner) delegationCatalog() ([]catalogModelRow, error) {
	if strings.TrimSpace(s.home) == "" {
		return nil, errors.New("子代理模型选择需要 home：模型目录没有落点，无法校验路由")
	}
	return catalogModelRows(s.home)
}

// delegationPolicy 读调用方会话的 durable 授权；拿不到会话身份就响亮失败。
func (s *subRunner) delegationPolicy(ctx context.Context, req spi.SubagentRequest) ([]session.AllowedModelRoute, bool, error) {
	parentID := strings.TrimSpace(req.ParentSessionID)
	if parentID == "" {
		parentID = strings.TrimSpace(spi.SessionFrom(ctx))
	}
	if parentID == "" {
		return nil, false, errors.New(
			"显式选择子代理模型需要调用方会话（没有授权边界，无法判断这组路由是否被授权）")
	}
	if strings.TrimSpace(s.home) == "" {
		return nil, false, errors.New("读子代理模型选择授权需要 home（会话日志没有落点）")
	}
	return session.SessionSubagentModelSelectionPolicy(s.home, parentID)
}

// assertAllowedModelSelection 逐条等价官方 `assertAllowedModelSelection`
// （model-selection.ts:136-150），错误文案逐字。
func assertAllowedModelSelection(
	recorded bool,
	routes []session.AllowedModelRoute,
	parentProvider, parentModel, provider, model string,
) error {
	allowed := false
	if !recorded {
		allowed = provider == parentProvider && model == parentModel
	} else {
		for _, route := range routes {
			if route.Provider == provider && route.Model == model {
				allowed = true
				break
			}
		}
	}
	if allowed {
		return nil
	}
	message := fmt.Sprintf("child LLM route %q is not allowed for this Session", provider+"/"+model)
	if !recorded {
		message += "; without a recorded model-selection policy, explicit selection is limited to the parent route"
	}
	return errors.New(message)
}

// availableRouteList 是"可被显式选择的路由"清单（诊断用，按 provider 排序）。
func availableRouteList(rows []catalogModelRow) string {
	if len(rows) == 0 {
		return "(none)"
	}
	seen := make(map[string]bool, len(rows))
	list := make([]string, 0, len(rows))
	for _, row := range rows {
		provider, _ := providerOfModel(rows, row.ID)
		entry := provider + "/" + row.ID
		if seen[entry] {
			continue
		}
		seen[entry] = true
		list = append(list, entry)
	}
	return strings.Join(list, ", ")
}

// agentModelRunner 把"当前 agent 实际用的模型"绑到工具调用的 ctx 上。
//
// 谁需要：`list_subagent_models`（官方在无 durable 策略时只能列父路由，
// 见 list-models.ts:50-56）。宿主在三个入口各包一次：run 的顶层循环、ACP 的
// 顶层循环、subRunner 跑子代理时的子循环（那里绑的是**解析后的子路由**，
// 否则嵌套在子会话里的发现工具会报外层模型）。
type agentModelRunner struct {
	inner spi.Runner
	model string
}

func (r agentModelRunner) Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
	if r.inner == nil {
		return spi.Result{}, errors.New("工具执行器没有装配")
	}
	return r.inner.Run(spi.WithAgentModel(ctx, r.model), name, args)
}
