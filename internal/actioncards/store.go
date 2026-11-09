package actioncards

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
)

func (s *Service) readEvents(sessionID string) ([]v4.Event, error) {
	path, err := session.FindByID(s.home, sessionID)
	if err != nil {
		return nil, fmt.Errorf("actioncards: 找不到会话 %q: %w", sessionID, err)
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("actioncards: 读会话 %q 失败: %w", sessionID, err)
	}
	return log.Events, nil
}

// Record 是 kind 在后台工作结束时写回决定的导出入口。
//
// 它和 Act 共用同一条 v4 append 路径；先校验 action 曾注册过（retired 的
// action 仍允许写 in-flight 结果，与官方 retired 语义一致），避免写出一条
// 没有任何 kind 能解释的决定。
func (s *Service) Record(_ context.Context, sessionID string, decision Decision) error {
	if err := VerifyDecision(decision); err != nil {
		return err
	}
	s.mu.RLock()
	_, live := s.kinds[decision.Action]
	retired := s.retired[decision.Action]
	s.mu.RUnlock()
	if !live && !retired {
		return fmt.Errorf("actioncards: 不能记录未注册 action %q 的决定", decision.Action)
	}
	return s.appendDecision(sessionID, decision)
}

func (s *Service) appendDecision(sessionID string, decision Decision) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := VerifyDecision(decision); err != nil {
		return err
	}
	path, err := session.FindByID(s.home, sessionID)
	if err != nil {
		return fmt.Errorf("actioncards: 找不到会话 %q: %w", sessionID, err)
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		return fmt.Errorf("actioncards: 打开会话 %q 追加决定失败: %w", sessionID, err)
	}
	data := map[string]any{
		"callId": decision.CallID,
		"action": decision.Action,
		"phase":  decision.Phase,
	}
	if decision.Data != nil {
		data["data"] = decision.Data
	}
	if err := logw.Append("plugin:action-cards/decision", data); err != nil {
		_ = logw.Close()
		return fmt.Errorf("actioncards: 写会话 %q 决定失败: %w", sessionID, err)
	}
	if err := logw.Close(); err != nil {
		return fmt.Errorf("actioncards: 关闭会话 %q 失败: %w", sessionID, err)
	}
	return nil
}

// latestTurnAndSkillLoads 返回最新一轮轮次号与该轮成功加载的 skill 名。
func (s *Service) latestTurnAndSkillLoads(events []v4.Event) (int64, map[string]bool) {
	var turn int64 = -1
	for _, event := range events {
		if event.Type != "turn/start" {
			continue
		}
		var data struct {
			Turn int64 `json:"turn"`
		}
		if json.Unmarshal(event.Data, &data) == nil {
			turn = data.Turn
		}
	}
	// 第二遍按 callId 关联 skill 工具的成功结果；只认最新一轮。
	success := map[string]bool{}
	pending := map[string]string{}
	for _, event := range events {
		switch event.Type {
		case "tool/call":
			var data struct {
				Turn      int64  `json:"turn"`
				CallID    string `json:"callId"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal(event.Data, &data) != nil || data.Name != "skill" {
				continue
			}
			var args struct {
				Name string `json:"name"`
			}
			if json.Unmarshal([]byte(data.Arguments), &args) == nil && data.Turn == turn {
				pending[data.CallID] = strings.TrimSpace(args.Name)
			}
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
			if name, ok := pending[data.Message.ToolCallID]; ok {
				if !data.Message.IsError {
					success[name] = true
				}
				delete(pending, data.Message.ToolCallID)
			}
		}
	}
	return turn, success
}
