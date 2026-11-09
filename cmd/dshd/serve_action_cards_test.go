package main

// 真实 Transport + 真实 v4 会话 + 真实 actioncards.Service 的 actionCards.act
// 契约测试：不是 mock 回调，decision 真的写进会话日志。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/actioncards"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

func seedActionCardSession(t *testing.T, home, sessionID string) {
	t.Helper()
	lg, err := session.CreateSession(home, filepath.Join(home, "cwd"), sessionID, "default")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"action": "create_page", "title": "创建页面", "brief": "在右侧生成页面", "details": map[string]any{"slot": "page"},
	})
	if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, "call-card-1", "propose_action", string(args))); err != nil {
		t.Fatalf("tool/call: %v", err)
	}
	if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, "call-card-1",
		[]spi.Block{{Type: "text", Text: "Card shown"}}, false, "", nil)); err != nil {
		t.Fatalf("tool/result: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func actionCardsServer(t *testing.T, home string) (*httptest.Server, string) {
	t.Helper()
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialAccountAPI(transport.OfficialAccountAPI{ActionCardAct: actionCardAct(home)})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv.MintInternalCookie()
}

func postActionCard(t *testing.T, base, cookie, body string) map[string]any {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+"/api/actionCards.act", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "dshd_session", Value: cookie})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST actionCards.act: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("响应不是 JSON：%v %s", err, raw)
	}
	result, _ := envelope["result"].(map[string]any)
	if result == nil {
		t.Fatalf("响应缺 result：%s", raw)
	}
	return result
}

func TestActionCardsActRealSideEffectOverHTTP(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, "action-side-effect.txt")
	service := actioncards.Open(home)
	if _, err := service.Register(actioncards.Kind{
		Action: "create_page", Invitation: "点击创建页面。",
		Verbs: map[string]actioncards.VerbPolicy{"create": {Writes: true}},
		Act: func(_ context.Context, proposal actioncards.Proposal, verb string, _ map[string]json.RawMessage) (actioncards.KindResult, error) {
			if verb != "create" {
				return actioncards.Refused("unavailable"), nil
			}
			if err := os.WriteFile(marker, []byte(proposal.CallID), 0o600); err != nil {
				return actioncards.KindResult{}, err
			}
			return actioncards.Settled(actioncards.Decision{
				CallID: proposal.CallID, Action: proposal.Action, Phase: "created",
			}), nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	seedActionCardSession(t, home, "s-http")
	ts, cookie := actionCardsServer(t, home)
	body := `{"agentId":"s-http","callId":"call-card-1","verb":"create","payload":{}}`
	result := postActionCard(t, ts.URL, cookie, body)
	if result["status"] != "settled" {
		t.Fatalf("actionCards.act 结果 = %+v", result)
	}
	decision, _ := result["decision"].(map[string]any)
	if decision["phase"] != "created" || decision["callId"] != "call-card-1" {
		t.Fatalf("decision = %+v", decision)
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "call-card-1" {
		t.Fatalf("HTTP 动作没有真实副作用：%v %q", err, body)
	}
	view, err := service.View("s-http")
	if err != nil || view.Decisions["call-card-1"].Phase != "created" {
		t.Fatalf("HTTP 动作没有写进会话投影：%+v %v", view, err)
	}
}

func TestActionCardsActRefusesUnknownAndDismissesUnstarted(t *testing.T) {
	home := t.TempDir()
	// 没有注册任何 kind：未知 action 必须结构化 refused，不能 501/静态成功。
	seedActionCardSession(t, home, "s-http-2")
	ts, cookie := actionCardsServer(t, home)
	result := postActionCard(t, ts.URL, cookie,
		`{"agentId":"s-http-2","callId":"call-card-1","verb":"create","payload":{}}`)
	if result["status"] != "refused" || result["reason"] != "unavailable" {
		t.Fatalf("未知 action = %+v", result)
	}
	// Not now 对未启动卡仍可用（官方共享 dismiss）。
	result = postActionCard(t, ts.URL, cookie,
		`{"agentId":"s-http-2","callId":"call-card-1","verb":"dismiss","payload":{}}`)
	if result["status"] != "settled" {
		t.Fatalf("dismiss = %+v", result)
	}
	decision, _ := result["decision"].(map[string]any)
	if decision["phase"] != "dismissed" {
		t.Fatalf("dismiss decision = %+v", decision)
	}
	// 不存在的 callId 也是结构化的 unknown。
	result = postActionCard(t, ts.URL, cookie,
		`{"agentId":"s-http-2","callId":"no-such-call","verb":"dismiss","payload":{}}`)
	if result["status"] != "refused" || result["reason"] != "unknown" {
		t.Fatalf("unknown call = %+v", result)
	}
}
