package actioncards

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/session"
	"dshgo/internal/spi"
)

func seedSession(t *testing.T, home, id string, append func(*session.Log)) {
	t.Helper()
	lg, err := session.CreateSession(home, filepath.Join(home, "cwd"), id, "default")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if append != nil {
		append(lg)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func appendProposal(t *testing.T, lg *session.Log, turn int, callID, action string, details map[string]any) {
	t.Helper()
	if details == nil {
		details = map[string]any{}
	}
	if err := lg.Append("turn/start", session.TurnStartData(turn)); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"action": action, "title": "Title " + action, "brief": "Brief " + action, "details": details,
	})
	if err := lg.Append("tool/call", session.ToolCallEvent(turn, 1, callID, ToolName, string(args))); err != nil {
		t.Fatalf("tool/call: %v", err)
	}
	if err := lg.Append("tool/result", session.ToolResultEvent(turn, 1, callID,
		[]spi.Block{{Type: "text", Text: "ok"}}, false, "", nil)); err != nil {
		t.Fatalf("tool/result: %v", err)
	}
}

func TestActRealSideEffectAndRecovery(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, "side-effect.txt")
	service := New(home)
	dispose, err := service.Register(Kind{
		Action:     "create_page",
		Invitation: "点击后创建。",
		Verbs:      map[string]VerbPolicy{"create": {Writes: true}},
		Act: func(_ context.Context, proposal Proposal, verb string, _ map[string]json.RawMessage) (KindResult, error) {
			if verb != "create" {
				return Refused("unavailable"), nil
			}
			if err := os.WriteFile(marker, []byte(proposal.CallID), 0o600); err != nil {
				return KindResult{}, err
			}
			return Settled(Decision{
				CallID: proposal.CallID, Action: proposal.Action, Phase: "created",
				Data: map[string]any{"title": proposal.Title},
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer dispose()

	seedSession(t, home, "s1", func(lg *session.Log) {
		appendProposal(t, lg, 1, "call-1", "create_page", map[string]any{"slot": "page"})
	})

	result, err := service.Act(context.Background(), "s1", "call-1", "create", nil)
	if err != nil {
		t.Fatalf("Act create: %v", err)
	}
	if result.Status != StatusSettled || result.Decision == nil || result.Decision.Phase != "created" {
		t.Fatalf("Act result = %+v", result)
	}
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "call-1" {
		t.Fatalf("kind 没有真实副作用：%v %q", err, body)
	}
	view, err := service.View("s1")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if view.Decisions["call-1"].Phase != "created" {
		t.Fatalf("投影没有 decision：%+v", view)
	}

	// 重启（新 Service）后从 v4 恢复；重复动作必须是 settled，不重复写。
	restarted := New(home)
	if _, err := restarted.Register(Kind{
		Action: "create_page", Verbs: map[string]VerbPolicy{"create": {Writes: true}},
		Act: func(context.Context, Proposal, string, map[string]json.RawMessage) (KindResult, error) {
			t.Fatal("已 settled 的卡不应再次执行 kind")
			return KindResult{}, nil
		},
	}); err != nil {
		t.Fatalf("restart Register: %v", err)
	}
	again, err := restarted.Act(context.Background(), "s1", "call-1", "create", nil)
	if err != nil || again.Status != StatusSettled || again.Decision == nil || again.Decision.Phase != "created" {
		t.Fatalf("重启后重复动作 = %+v, %v", again, err)
	}
	view, _ = restarted.View("s1")
	if view.Decisions["call-1"].Phase != "created" {
		t.Fatalf("重启后 decision 丢了：%+v", view)
	}
}

func TestActUnknownActionIsStructuredRefusal(t *testing.T) {
	home := t.TempDir()
	service := New(home)
	seedSession(t, home, "s2", func(lg *session.Log) {
		appendProposal(t, lg, 1, "call-unknown", "not_registered", nil)
	})
	result, err := service.Act(context.Background(), "s2", "call-unknown", "run", nil)
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if result.Status != StatusRefused || result.Reason != "unavailable" {
		t.Fatalf("未知 action 必须是 refused/unavailable，实际 %+v", result)
	}
	// 没有 kind 的未启动卡仍可 Not now；这是官方共享 dismiss 语义。
	result, err = service.Act(context.Background(), "s2", "call-unknown", VerbDismiss, nil)
	if err != nil || result.Status != StatusSettled || result.Decision == nil || result.Decision.Phase != "dismissed" {
		t.Fatalf("dismiss unstarted = %+v, %v", result, err)
	}
}

func TestToolValidationAndSkillGate(t *testing.T) {
	home := t.TempDir()
	service := New(home)
	if _, err := service.Register(Kind{
		Action: "deploy", Skill: "deploy-skill", Invitation: "点击部署。",
		Details: func(raw json.RawMessage) error {
			var details struct {
				Target string `json:"target"`
			}
			if err := json.Unmarshal(raw, &details); err != nil {
				return err
			}
			if strings.TrimSpace(details.Target) == "" {
				return errors.New("target 必填")
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	seedSession(t, home, "s3", func(lg *session.Log) {
		if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
			t.Fatalf("turn/start: %v", err)
		}
	})
	tool := service.Tool()
	ctx := spi.WithSession(context.Background(), "s3")
	args := json.RawMessage(`{"action":"deploy","title":"Deploy","brief":"部署","details":{"target":"prod"}}`)
	if _, err := tool.Exec(ctx, args); err == nil {
		t.Fatal("skill 未加载时 propose_action 竟然成功")
	} else {
		var coded interface{ Code() string }
		if !errors.As(err, &coded) || coded.Code() != "ACTION_CARD_SKILL" {
			t.Fatalf("错误码不是 ACTION_CARD_SKILL：%v", err)
		}
	}

	// 真实记录一次 skill 加载（tool/call + 成功 tool/result），再调用。
	seedSkillLoad(t, home, "s3", "skill-1", "deploy-skill")
	result, err := tool.Exec(ctx, args)
	if err != nil {
		t.Fatalf("skill 已加载后 Exec: %v", err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["action"] != "deploy" || data["invitation"] != "点击部署。" {
		t.Fatalf("工具结果 = %+v", result.Data)
	}
	if !strings.Contains(result.Text, "Card shown below your answer") {
		t.Fatalf("工具文本不对：%q", result.Text)
	}

	unknown := json.RawMessage(`{"action":"nope","title":"T","brief":"B","details":{}}`)
	if _, err := tool.Exec(ctx, unknown); err == nil || !strings.Contains(err.Error(), "Unknown action") {
		t.Fatalf("未知 action 错误不对：%v", err)
	}
	badDetails := json.RawMessage(`{"action":"deploy","title":"T","brief":"B","details":{}}`)
	if _, err := tool.Exec(ctx, badDetails); err == nil || !strings.Contains(err.Error(), "Invalid details") {
		t.Fatalf("details 校验错误不对：%v", err)
	}
}

func seedSkillLoad(t *testing.T, home, sessionID, callID, skill string) {
	t.Helper()
	path, err := session.FindByID(home, sessionID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	lg, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatalf("OpenSessionAt: %v", err)
	}
	defer lg.Close()
	args, _ := json.Marshal(map[string]any{"name": skill})
	if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, callID, "skill", string(args))); err != nil {
		t.Fatalf("skill tool/call: %v", err)
	}
	if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, callID,
		[]spi.Block{{Type: "text", Text: "loaded"}}, false, "", nil)); err != nil {
		t.Fatalf("skill tool/result: %v", err)
	}
}

func TestProjectionAdmitsOnePerTurnAndReplacesEarlier(t *testing.T) {
	home := t.TempDir()
	seedSession(t, home, "s4", func(lg *session.Log) {
		appendProposal(t, lg, 1, "call-a", "create_page", nil)
		appendProposal(t, lg, 2, "call-b", "create_page", nil)
	})
	service := New(home)
	view, err := service.View("s4")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(view.Proposals) != 2 || view.Proposals["call-b"].CallID != "call-b" {
		t.Fatalf("两轮各一张卡都应保留（旧卡标 replaced，不删）：%+v", view.Proposals)
	}
	if view.Decisions["call-a"].Phase != "replaced" {
		t.Fatalf("旧卡应被标记 replaced：%+v", view.Decisions)
	}
}

func TestAcceptedThenRecordWritesDecision(t *testing.T) {
	home := t.TempDir()
	service := New(home)
	if _, err := service.Register(Kind{
		Action: "build", Verbs: map[string]VerbPolicy{"run": {Writes: true}},
		Act: func(context.Context, Proposal, string, map[string]json.RawMessage) (KindResult, error) {
			return Accepted(), nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	seedSession(t, home, "s5", func(lg *session.Log) {
		appendProposal(t, lg, 1, "call-bg", "build", nil)
	})
	result, err := service.Act(context.Background(), "s5", "call-bg", "run", nil)
	if err != nil || result.Status != StatusAccepted || result.Decision != nil {
		t.Fatalf("Act accepted = %+v, %v", result, err)
	}
	if err := service.Record(context.Background(), "s5", Decision{
		CallID: "call-bg", Action: "build", Phase: "done", Data: map[string]any{"ok": true},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	view, err := service.View("s5")
	if err != nil || view.Decisions["call-bg"].Phase != "done" {
		t.Fatalf("Record 后投影 = %+v, %v", view, err)
	}
	if err := service.Record(context.Background(), "s5", Decision{
		CallID: "call-bg", Action: "never_registered", Phase: "done",
	}); err == nil {
		t.Fatal("未注册 action 的 Record 竟然成功")
	}
}
