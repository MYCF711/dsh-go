package actioncards

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dshgo/internal/spi"
)

var proposeActionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "description": "Action name from the skill."},
    "title": {"type": "string", "description": "Card title."},
    "brief": {"type": "string", "description": "What the action does, in the user's language."},
    "details": {"type": "object", "additionalProperties": true, "description": "Fields the skill lists for this action."}
  },
  "required": ["action", "title", "brief", "details"],
  "additionalProperties": false
}`)

type proposeActionTool struct{ service *Service }

func (t *proposeActionTool) Name() string { return ToolName }

func (t *proposeActionTool) Description() string {
	return "Show the user one optional action card under this answer. Call only as the skill that names the action instructs."
}

func (t *proposeActionTool) Schema() json.RawMessage { return proposeActionSchema }

// Caps：工具本身不写盘/不起进程/不联网；真正动作要等用户在卡片上点击。
func (t *proposeActionTool) Caps() spi.Caps { return spi.Caps{} }

type actionCardError struct {
	code string
	msg  string
}

func (e *actionCardError) Error() string { return e.msg }

// Code 返回官方 ActionCardError.code（测试/诊断可读）。
func (e *actionCardError) Code() string { return e.code }

func (t *proposeActionTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	sessionID := spi.SessionFrom(ctx)
	if strings.TrimSpace(sessionID) == "" {
		return spi.Result{Hint: "propose_action 需要会话身份；当前上下文没有 session id。"},
			&actionCardError{code: "ACTION_CARD_NO_SESSION", msg: "propose_action 没有会话身份，拒绝生成卡片"}
	}
	var args struct {
		Action  string          `json:"action"`
		Title   string          `json:"title"`
		Brief   string          `json:"brief"`
		Details json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return spi.Result{Hint: `参数形状是 {"action":"…","title":"…","brief":"…","details":{…}}；四个字段都必填。`},
			&actionCardError{code: "ACTION_CARD_ARGUMENTS", msg: "propose_action 参数不是合法 JSON 对象：" + err.Error()}
	}
	action := strings.TrimSpace(args.Action)
	title := strings.TrimSpace(args.Title)
	brief := strings.TrimSpace(args.Brief)
	if action == "" || title == "" || brief == "" {
		return spi.Result{Hint: "action/title/brief 都必须是非空字符串；title 与 brief 用用户语言写。"},
			&actionCardError{code: "ACTION_CARD_BLANK", msg: "propose_action 的 action/title/brief 不能为空"}
	}
	if len(strings.TrimSpace(string(args.Details))) == 0 || strings.TrimSpace(string(args.Details)) == "null" {
		return spi.Result{Hint: "details 必须是对象；没有字段就传 {}。"},
			&actionCardError{code: "ACTION_CARD_ARGUMENTS", msg: "propose_action 的 details 必填且必须是 JSON 对象"}
	}
	var details map[string]any
	if err := json.Unmarshal(args.Details, &details); err != nil || details == nil {
		return spi.Result{Hint: "details 必须是 JSON 对象（不能是数组/null）。"},
			&actionCardError{code: "ACTION_CARD_ARGUMENTS", msg: "propose_action 的 details 不是 JSON 对象"}
	}
	kind, ok := t.service.kind(action)
	if !ok {
		registered := "none"
		if actions := t.service.Actions(); len(actions) > 0 {
			registered = strings.Join(actions, ", ")
		}
		return spi.Result{Hint: "不要编 action；只能调用已加载 skill 注册的 action。当前可用：" + registered},
			&actionCardError{code: "ACTION_CARD_UNKNOWN", msg: fmt.Sprintf("Unknown action %q. Registered actions: %s.", action, registered)}
	}
	if kind.Skill != "" {
		events, err := t.service.readEvents(sessionID)
		if err != nil {
			return spi.Result{Hint: "先确认会话可读；propose_action 需要读取本轮 skill 加载记录。"}, err
		}
		_, loaded := t.service.latestTurnAndSkillLoads(events)
		if !loaded[kind.Skill] {
			return spi.Result{Hint: "先加载 skill " + kind.Skill + "，再调用 propose_action。"},
				&actionCardError{code: "ACTION_CARD_SKILL", msg: fmt.Sprintf("Load skill %s first, then call propose_action.", kind.Skill)}
		}
	}
	if kind.Details != nil {
		if err := kind.Details(args.Details); err != nil {
			return spi.Result{Hint: "details 不符合该 action 的 schema：" + err.Error()},
				&actionCardError{code: "ACTION_CARD_DETAILS", msg: "Invalid details: " + err.Error()}
		}
	}
	invitation := kind.Invitation
	return spi.Result{
		Summary: "已向用户展示可选动作卡：" + title,
		Data: map[string]any{
			"action":     action,
			"title":      title,
			"invitation": invitation,
		},
		Text: "Card shown below your answer. Do not restate it. " + invitation,
	}, nil
}
