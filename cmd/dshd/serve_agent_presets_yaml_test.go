package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/modes"
	"dshgo/internal/transport"
)

// TestAgentPresetReadYAMLShape 钉住生产投影：content 必须是官方 entry-list
// YAML（顶层数组、行有 name/config），不是 TOML 原文。
func TestAgentPresetReadYAMLShape(t *testing.T) {
	f := newAgentAPIFixture(t)
	doc, err := f.core.agentPresetReadYAML(modes.DefaultID)
	if err != nil {
		t.Fatalf("读默认 preset YAML: %v", err)
	}
	if doc.AgentPreset != modes.DefaultID || doc.Name != "Default" {
		t.Fatalf("元数据 = %+v", doc)
	}
	if !strings.HasPrefix(doc.Content, "- id: ") {
		t.Fatalf("content 不是 entry-list YAML：%q", doc.Content)
	}
	if strings.Contains(doc.Content, "id = ") || strings.Contains(doc.Content, "tools = ") {
		t.Fatalf("content 仍是 TOML：%q", doc.Content)
	}
	if !strings.Contains(doc.Content, `name: "dsh-go/agent-preset"`) ||
		!strings.Contains(doc.Content, "tools: []") {
		t.Fatalf("YAML 缺官方行字段：\n%s", doc.Content)
	}
	if _, err := f.core.agentPresetReadYAML("nope"); err == nil {
		t.Fatal("未知 preset 竟然读到了 YAML")
	}

	dir := filepath.Join(f.home, "modes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 modes: %v", err)
	}
	tomlBody := "id = \"review\"\nname = \"Review\"\ndescription = \"只读审阅\"\ntools = [\"read\", \"grep\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "review.toml"), []byte(tomlBody), 0o600); err != nil {
		t.Fatalf("写 review.toml: %v", err)
	}
	user, err := f.core.agentPresetReadYAML("review")
	if err != nil {
		t.Fatalf("读用户 preset YAML: %v", err)
	}
	if user.Content == tomlBody || strings.Contains(user.Content, "id = ") {
		t.Fatalf("用户 preset 仍回 TOML 原文：%q", user.Content)
	}
	if !strings.Contains(user.Content, `id: "review"`) || !strings.Contains(user.Content, `- "read"`) {
		t.Fatalf("用户 YAML 缺真实字段：\n%s", user.Content)
	}
}

// TestAgentPresetsReadHTTPYAMLShape 用真实 Transport 认证面验证官方
// agentPresets.read 的 content 是 entry-list YAML，不是 TOML。
func TestAgentPresetsReadHTTPYAMLShape(t *testing.T) {
	f := newAgentAPIFixture(t)
	api := f.api
	api.AgentPresetRead = f.core.agentPresetReadYAML
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialAgentAPI(api)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	cookie := srv.MintInternalCookie()

	request, err := http.NewRequest(http.MethodPost, ts.URL+"/api/agentPresets.read",
		strings.NewReader(`{"agentPreset":"read-only"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "dshd_session", Value: cookie})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST agentPresets.read: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("agentPresets.read = %d %s", response.StatusCode, raw)
	}
	var envelope struct {
		Result struct {
			AgentPreset string `json:"agentPreset"`
			Content     string `json:"content"`
			Name        string `json:"name"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("响应不是 JSON：%v %s", err, raw)
	}
	if envelope.Result.AgentPreset != "read-only" || envelope.Result.Name != "Read only" {
		t.Fatalf("result 元数据 = %+v", envelope.Result)
	}
	if !strings.HasPrefix(envelope.Result.Content, "- id: ") ||
		strings.Contains(envelope.Result.Content, "id = ") {
		t.Fatalf("HTTP content 不是 entry-list YAML：%q", envelope.Result.Content)
	}
}
