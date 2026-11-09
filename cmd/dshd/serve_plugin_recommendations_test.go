package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/actioncards"
	"dshgo/internal/market"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/recommendations"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

const recPlugin = "acme.audit"

func writeRecommendationPackage(t *testing.T, dir, id, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("recommendation test package")
	sum := sha256.Sum256(payload)
	manifest := map[string]any{
		"schema": "dsh.local-plugin@1", "id": id, "version": version,
		"kind": "wasm", "entry": "plugin.wasm",
		"files": map[string]string{"plugin.wasm": hex.EncodeToString(sum[:])},
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func recommendationIndex(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(market.Document{
			Version: 1,
			Plugins: []market.Plugin{{
				Name: recPlugin, Title: map[string]string{"en": "Audit"}, Description: map[string]string{"en": "security audit"},
				Labels: []string{"official"}, Versions: []market.Version{{Version: "1.0.0"}},
			}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func seedRecommendationSession(t *testing.T, home, sessionID, callID string) {
	t.Helper()
	lg, err := session.CreateSession(home, filepath.Join(home, "cwd"), sessionID, "default")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{
		"action": recommendations.Action, "title": "Audit", "brief": "安全审计",
		"details": map[string]any{"name": recPlugin},
	})
	if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, callID, "propose_action", string(args))); err != nil {
		t.Fatal(err)
	}
	if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, callID,
		[]spi.Block{{Type: "text", Text: "Card shown"}}, false, "", nil)); err != nil {
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
}

func recommendationHTTPServer(t *testing.T) (*httptest.Server, string, *recommendations.Service) {
	t.Helper()
	home := t.TempDir()
	index := recommendationIndex(t)
	marketStore := market.Open(home)
	marketStore.Client = index.Client()
	if err := marketStore.SetURLs([]string{index.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	pkg := writeRecommendationPackage(t, filepath.Join(home, "pkg"), recPlugin, "1.0.0")
	manager, err := pluginmgr.Open(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatalf("pluginmgr.Open: %v", err)
	}
	if _, err := manager.Install(pkg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cards := actioncards.Open(home)
	service, err := recommendations.New(home, manager, marketStore, cards)
	if err != nil {
		t.Fatalf("recommendations.New: %v", err)
	}
	if err := service.EnsureSkill(t.Context()); err != nil {
		t.Fatalf("EnsureSkill: %v", err)
	}
	seedRecommendationSession(t, home, "s-rec", "call-rec")
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialPluginRecommendationsAPI(transport.OfficialPluginRecommendationsAPI{Describe: service.Describe})
	srv.AttachOfficialAccountAPI(transport.OfficialAccountAPI{ActionCardAct: actionCardAct(home)})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv.MintInternalCookie(), service
}

func postRecommendationHTTP(t *testing.T, base, cookie, method, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/api/"+method, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "dshd_session", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("响应不是 JSON：%v %s", err, raw)
	}
	return resp.StatusCode, envelope
}

func TestRecommendationDescribeAndActReal(t *testing.T) {
	api, cookie, _ := recommendationHTTPServer(t)
	status, envelope := postRecommendationHTTP(t, api.URL, cookie, "pluginRecommendations.describe",
		`{"agentId":"s-rec","callId":"call-rec"}`)
	if status != http.StatusOK {
		t.Fatalf("describe = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["status"] != "available" || result["title"] != "Audit" || result["enabled"] != false {
		t.Fatalf("describe result = %+v", result)
	}

	status, envelope = postRecommendationHTTP(t, api.URL, cookie, "actionCards.act",
		`{"agentId":"s-rec","callId":"call-rec","verb":"enable","payload":{}}`)
	if status != http.StatusOK {
		t.Fatalf("act = %d %+v", status, envelope)
	}
	actResult, _ := envelope["result"].(map[string]any)
	if actResult["status"] != "settled" {
		t.Fatalf("act result = %+v", actResult)
	}
	decision, _ := actResult["decision"].(map[string]any)
	if decision["phase"] != "enabled" {
		t.Fatalf("decision = %+v", decision)
	}
	// 再 describe 必须 enabled=true（真实 manager 状态）。
	_, envelope = postRecommendationHTTP(t, api.URL, cookie, "pluginRecommendations.describe",
		`{"agentId":"s-rec","callId":"call-rec"}`)
	result, _ = envelope["result"].(map[string]any)
	if result["enabled"] != true {
		t.Fatalf("启用后 describe = %+v", result)
	}
}

func TestRecommendationUnknownCall(t *testing.T) {
	api, cookie, _ := recommendationHTTPServer(t)
	status, envelope := postRecommendationHTTP(t, api.URL, cookie, "pluginRecommendations.describe",
		`{"agentId":"s-rec","callId":"missing"}`)
	if status != http.StatusOK {
		t.Fatalf("unknown describe = %d", status)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["status"] != "refused" || result["reason"] != "unknown" {
		t.Fatalf("unknown = %+v", result)
	}
}
