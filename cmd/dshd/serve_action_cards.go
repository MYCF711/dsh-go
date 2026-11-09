package main

// actionCards.act 的宿主适配器。
//
// 协议形状/参数校验/四字段错误仍在 internal/transport/handlers_account_official.go；
// 本文件只把真实 actioncards.Service 接到既有的
// `OfficialAccountAPI.ActionCardAct` 回调上。共享 serve_account_api.go 的
// 接线由 WIRING 完成（见 tmp/GO2-TEAM/PARITY-W1-PRESETS-ACTIONS/WIRING.md）：
//
//	account := newAccountOfficialAPI(accountOfficialDeps{home: *home})
//	account.ActionCardAct = actionCardAct(*home)
//	srv.AttachOfficialAccountAPI(account)

import (
	"context"
	"encoding/json"

	"dshgo/internal/actioncards"
	"dshgo/internal/transport"
)

// actionCardAct 返回绑定到某 home 的 OfficialAccountAPI.ActionCardAct 回调。
func actionCardAct(home string) func(context.Context, string, string, string, map[string]json.RawMessage) (transport.OfficialActionCardResult, error) {
	service := actioncards.Open(home)
	return func(ctx context.Context, sessionID, callID, verb string, payload map[string]json.RawMessage) (transport.OfficialActionCardResult, error) {
		result, err := service.Act(ctx, sessionID, callID, verb, payload)
		if err != nil {
			return transport.OfficialActionCardResult{}, err
		}
		out := transport.OfficialActionCardResult{
			Status: result.Status,
			Reason: result.Reason,
		}
		if result.Decision != nil {
			decision := &transport.OfficialActionCardDecision{
				CallID: result.Decision.CallID,
				Action: result.Decision.Action,
				Phase:  result.Decision.Phase,
			}
			if result.Decision.Data != nil {
				raw, err := json.Marshal(result.Decision.Data)
				if err != nil {
					return transport.OfficialActionCardResult{}, err
				}
				decision.Data = raw
			}
			out.Decision = decision
		}
		return out, nil
	}
}

// attachActionCardsView 把真实 actionCards projection 挂到 session.projections。
// WIRING 在 serve.go 的装配段调用；单独成函数，避免共享 serve.go 多 import。
func attachActionCardsView(srv *transport.Server, home string) {
	srv.AttachActionCardsView(actioncards.Open(home).ViewMap)
}
