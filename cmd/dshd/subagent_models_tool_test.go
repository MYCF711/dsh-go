package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dshgo/internal/session"
	"dshgo/internal/spi"
)

func seedModelsToolHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	writeModelCatalog(t, home, strings.Join([]string{
		`{"id":"m1","name":"Model One"}`,
		`{"id":"m2","name":"Model Two"}`,
		`{"id":"acme-model","name":"Acme","owned_by":"acme","reasoningEfforts":{"off":null,"low":"low","high":"high"}}`,
	}, ","))
	return home
}

func callModelsTool(t *testing.T, home, sessionID, parentModel, args string) (string, error) {
	t.Helper()
	tool := &subagentModelsTool{home: home}
	ctx := spi.WithSession(context.Background(), sessionID)
	if parentModel != "" {
		ctx = spi.WithAgentModel(ctx, parentModel)
	}
	res, err := tool.Exec(ctx, json.RawMessage(args))
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// 无参数：列被授权且真的注册了的 provider（官方 list-models.ts:65-71）。
func TestListSubagentModelsListsAuthorizedProviders(t *testing.T) {
	home := seedModelsToolHome(t)
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-list-providers", []session.AllowedModelRoute{
		{Provider: officialGatewayProviderID, Model: "m1"},
		{Provider: "acme", Model: "acme-model"},
	})
	text, err := callModelsTool(t, home, "session-list-providers", "", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "gateway — gateway") || !strings.Contains(text, "acme — acme") {
		t.Fatalf("provider 清单不对: %q", text)
	}
}

// 只给 provider：只列"目录广播了、且在授权清单里"的模型。
func TestListSubagentModelsListsAuthorizedModels(t *testing.T) {
	home := seedModelsToolHome(t)
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-list-models", []session.AllowedModelRoute{
		{Provider: officialGatewayProviderID, Model: "m1"},
	})
	text, err := callModelsTool(t, home, "session-list-models", "", `{"provider":"gateway"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "gateway/m1 — Model One") {
		t.Fatalf("模型行不对: %q", text)
	}
	if strings.Contains(text, "gateway/m2") {
		t.Fatalf("未授权的模型不许出现: %q", text)
	}
}

// provider+model：模型行 + 推理档清单（官方 :92-97）。
func TestListSubagentModelsDescribesReasoningEfforts(t *testing.T) {
	home := seedModelsToolHome(t)
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-list-efforts", []session.AllowedModelRoute{
		{Provider: "acme", Model: "acme-model"},
	})
	text, err := callModelsTool(t, home, "session-list-efforts", "", `{"provider":"acme","model":"acme-model"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"acme/acme-model — Acme", "Reasoning efforts:", "high — high", "low — low"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q: %q", want, text)
		}
	}
}

// 官方错误分支逐条（文案逐字）。
func TestListSubagentModelsErrors(t *testing.T) {
	home := seedModelsToolHome(t)
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-list-errors", []session.AllowedModelRoute{
		{Provider: officialGatewayProviderID, Model: "m1"},
	})
	cases := []struct {
		name string
		args string
		want string
	}{
		{"model-without-provider", `{"model":"m1"}`, "`model` requires `provider`"},
		{"empty-provider", `{"provider":""}`, "`provider` must be non-empty"},
		{"provider-not-allowed", `{"provider":"acme"}`, `LLM provider "acme" is not allowed for this Session`},
		{"route-not-allowed", `{"provider":"gateway","model":"m2"}`,
			`child LLM route "gateway/m2" is not allowed for this Session`},
		{"model-not-in-catalog", `{"provider":"gateway","model":"ghost"}`,
			`child LLM route "gateway/ghost" is not allowed for this Session`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := callModelsTool(t, home, "session-list-errors", "", tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want contains %q", err, tc.want)
			}
		})
	}
	// 授权清单里合法、但目录里没有的模型：目录是真值源，不编一个名字出来。
	policySession(t, home, cwd, "session-list-ghost", []session.AllowedModelRoute{
		{Provider: officialGatewayProviderID, Model: "ghost"},
	})
	_, err := callModelsTool(t, home, "session-list-ghost", "", `{"provider":"gateway","model":"ghost"}`)
	if err == nil || !strings.Contains(err.Error(), "不在模型目录里") {
		t.Fatalf("目录里没有的模型必须响亮失败: %v", err)
	}
}

// 没有 durable 策略：只能看到调用方父路由（官方 :50-56）。
func TestListSubagentModelsWithoutPolicyUsesParentRoute(t *testing.T) {
	home := seedModelsToolHome(t)
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-list-no-policy", nil)
	text, err := callModelsTool(t, home, "session-list-no-policy", "m1", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(text) != "gateway — gateway" {
		t.Fatalf("无策略时只该看到父路由的 provider: %q", text)
	}
	text, err = callModelsTool(t, home, "session-list-no-policy", "m1", `{"provider":"gateway","model":"m1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "gateway/m1 — Model One") {
		t.Fatalf("父路由模型行不对: %q", text)
	}
	if _, err := callModelsTool(t, home, "session-list-no-policy", "m1", `{"provider":"gateway","model":"m2"}`); err == nil ||
		!strings.Contains(err.Error(), `child LLM route "gateway/m2" is not allowed`) {
		t.Fatalf("无策略时点名非父模型必须拒绝: %v", err)
	}
}

// 父路由的第二个来源：本会话 v4 的 request/context.model（run 入口每轮都写）。
func TestListSubagentModelsReadsParentModelFromSessionLog(t *testing.T) {
	home := seedModelsToolHome(t)
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-list-log-route", nil)
	lg, err := session.OpenSession(home, cwd, "session-list-log-route")
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Append("request/context", map[string]any{"model": "m2"}); err != nil {
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	text, err := callModelsTool(t, home, "session-list-log-route", "", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(text) != "gateway — gateway" {
		t.Fatalf("provider 清单不对: %q", text)
	}
	if _, err := callModelsTool(t, home, "session-list-log-route", "", `{"provider":"gateway","model":"m2"}`); err != nil {
		t.Fatalf("日志里的父模型必须被认: %v", err)
	}
	if _, err := callModelsTool(t, home, "session-list-log-route", "", `{"provider":"gateway","model":"m1"}`); err == nil {
		t.Fatal("日志里的父模型之外必须拒绝")
	}
}

// 完全不知道父路由 = 官方原文错误，不拿默认模型冒充。
func TestListSubagentModelsRequiresParentRoute(t *testing.T) {
	home := seedModelsToolHome(t)
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-list-no-parent", nil)
	_, err := callModelsTool(t, home, "session-list-no-parent", "", `{}`)
	if err == nil || !strings.Contains(err.Error(),
		"cannot discover child LLM routes without a parent provider and model") {
		t.Fatalf("没有父路由必须响亮失败: %v", err)
	}
}

// 没有调用方 agent：官方原文错误（list-models.ts:46）。
func TestListSubagentModelsRequiresCallingAgent(t *testing.T) {
	tool := &subagentModelsTool{home: t.TempDir()}
	if _, err := tool.Exec(context.Background(), json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "list_subagent_models requires a calling agent") {
		t.Fatalf("没有调用方会话必须响亮失败: %v", err)
	}
}
