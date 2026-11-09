package actioncards

import (
	"encoding/json"
	"fmt"
	"strings"

	"dshgo/codecs/v4"
)

// Project 把一段 v4 事件折叠成官方 actionCards view。
//
// 只认三类事实：
//   - tool/call(name=propose_action) 记下候选 proposal；
//   - tool/result 成功才把候选升为卡片；同一轮最多一张，新卡把未开始的旧卡
//     标成 replaced（与官方 projection.js 一致）；
//   - plugin:action-cards/decision 记录决定。
//
// 参数非法/工具失败的事件不会变成卡片；损坏的 decision 被跳过而不是让
// 整个会话投影失败（官方对 malformed record 就是跳过）。
func Project(events []v4.Event) View {
	view := View{Proposals: map[string]Proposal{}, Decisions: map[string]Decision{}}
	pending := map[string]Proposal{}
	admittedTurn := int64(-1)
	for _, event := range events {
		switch event.Type {
		case "turn/start":
			pending = map[string]Proposal{}
		case "tool/call":
			var data struct {
				Turn      int64  `json:"turn"`
				CallID    string `json:"callId"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal(event.Data, &data) != nil || data.Name != ToolName || data.CallID == "" {
				continue
			}
			proposal, ok := parseProposal(data.CallID, data.Turn, data.Arguments)
			if !ok {
				continue
			}
			pending[data.CallID] = proposal
		case "tool/result":
			var data struct {
				Message struct {
					ToolCallID string `json:"toolCallId"`
					IsError    bool   `json:"isError"`
				} `json:"message"`
			}
			if json.Unmarshal(event.Data, &data) != nil {
				continue
			}
			proposal, ok := pending[data.Message.ToolCallID]
			if !ok {
				continue
			}
			delete(pending, data.Message.ToolCallID)
			if data.Message.IsError {
				continue
			}
			if admittedTurn == proposal.Turn {
				continue
			}
			for callID, earlier := range view.Proposals {
				if _, decided := view.Decisions[callID]; !decided {
					view.Decisions[callID] = Decision{CallID: earlier.CallID, Action: earlier.Action, Phase: "replaced"}
				}
			}
			view.Proposals[proposal.CallID] = proposal
			admittedTurn = proposal.Turn
		case "plugin:action-cards/decision":
			var decision Decision
			if json.Unmarshal(event.Data, &decision) != nil || decision.CallID == "" || decision.Action == "" || decision.Phase == "" {
				continue
			}
			view.Decisions[decision.CallID] = decision
		}
	}
	return view
}

func parseProposal(callID string, turn int64, rawArguments string) (Proposal, bool) {
	if strings.TrimSpace(rawArguments) == "" {
		return Proposal{}, false
	}
	var args struct {
		Action  string          `json:"action"`
		Title   string          `json:"title"`
		Brief   string          `json:"brief"`
		Details json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal([]byte(rawArguments), &args); err != nil {
		return Proposal{}, false
	}
	args.Action = strings.TrimSpace(args.Action)
	args.Title = strings.TrimSpace(args.Title)
	args.Brief = strings.TrimSpace(args.Brief)
	if args.Action == "" || args.Title == "" || args.Brief == "" {
		return Proposal{}, false
	}
	details := map[string]any{}
	if len(strings.TrimSpace(string(args.Details))) > 0 {
		if err := json.Unmarshal(args.Details, &details); err != nil || details == nil {
			return Proposal{}, false
		}
	}
	return Proposal{
		CallID: callID, Turn: turn, Action: args.Action,
		Title: args.Title, Brief: args.Brief, Details: details,
	}, true
}

// VerifyDecision 是 Record 前对 kind 写回决定的硬校验。
func VerifyDecision(decision Decision) error {
	if strings.TrimSpace(decision.CallID) == "" {
		return fmt.Errorf("actioncards: decision.callId 不能为空")
	}
	if strings.TrimSpace(decision.Action) == "" {
		return fmt.Errorf("actioncards: decision.action 不能为空")
	}
	if strings.TrimSpace(decision.Phase) == "" {
		return fmt.Errorf("actioncards: decision.phase 不能为空")
	}
	if derivedPhases[decision.Phase] {
		return fmt.Errorf("actioncards: phase %q 由日志派生，不能直接记录", decision.Phase)
	}
	return nil
}
