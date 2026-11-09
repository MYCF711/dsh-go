package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func recommendationServer(t *testing.T, api OfficialPluginRecommendationsAPI) (*httptest.Server, string) {
	t.Helper()
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if api.Describe != nil {
		srv.AttachOfficialPluginRecommendationsAPI(api)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv.MintInternalCookie()
}

func postRecommendation(t *testing.T, base, cookie, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/api/pluginRecommendations.describe", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "dshd_session", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("响应不是 JSON：%v %s", err, raw)
	}
	return resp.StatusCode, envelope
}

func TestPluginRecommendationsDescribeHTTP(t *testing.T) {
	var gotAgent, gotCall string
	api := OfficialPluginRecommendationsAPI{
		Describe: func(_ context.Context, agentID, callID string) (map[string]any, error) {
			gotAgent, gotCall = agentID, callID
			return map[string]any{"status": "available", "title": "Audit", "description": "security audit", "enabled": false}, nil
		},
	}
	ts, cookie := recommendationServer(t, api)
	status, envelope := postRecommendation(t, ts.URL, cookie, `{"agentId":"s-1","callId":"call-1"}`)
	if status != http.StatusOK {
		t.Fatalf("describe = %d %+v", status, envelope)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["status"] != "available" || result["title"] != "Audit" || result["enabled"] != false {
		t.Fatalf("result = %+v", result)
	}
	if gotAgent != "s-1" || gotCall != "call-1" {
		t.Fatalf("宿主参数 = %q %q", gotAgent, gotCall)
	}

	for _, body := range []string{
		`{"agentId":"s-1"}`,
		`{"agentId":"s-1","callId":""}`,
		`{"agentId":"s-1","callId":"call-1","extra":1}`,
	} {
		status, _ := postRecommendation(t, ts.URL, cookie, body)
		if status != http.StatusBadRequest {
			t.Fatalf("非法参数应 400，得到 %d：%s", status, body)
		}
	}
}

func TestPluginRecommendationsDescribeRefusesAndUnwired(t *testing.T) {
	api := OfficialPluginRecommendationsAPI{
		Describe: func(context.Context, string, string) (map[string]any, error) {
			return map[string]any{"status": "refused", "reason": "unavailable"}, nil
		},
	}
	ts, cookie := recommendationServer(t, api)
	status, envelope := postRecommendation(t, ts.URL, cookie, `{"agentId":"s-1","callId":"call-1"}`)
	if status != http.StatusOK {
		t.Fatalf("refused 应是 200 union，得到 %d", status)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["status"] != "refused" || result["reason"] != "unavailable" {
		t.Fatalf("refused = %+v", result)
	}

	unwired, unwiredCookie := recommendationServer(t, OfficialPluginRecommendationsAPI{})
	status, _ = postRecommendation(t, unwired.URL, unwiredCookie, `{"agentId":"s-1","callId":"call-1"}`)
	if status != http.StatusNotImplemented {
		t.Fatalf("未接线应 501，得到 %d", status)
	}

	errorAPI := OfficialPluginRecommendationsAPI{
		Describe: func(context.Context, string, string) (map[string]any, error) {
			return nil, errors.New("目录读取失败")
		},
	}
	errorServer, errorCookie := recommendationServer(t, errorAPI)
	status, _ = postRecommendation(t, errorServer.URL, errorCookie, `{"agentId":"s-1","callId":"call-1"}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("宿主错误应 500，得到 %d", status)
	}
}
