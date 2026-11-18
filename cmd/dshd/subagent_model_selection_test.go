package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/settings"
	"dshgo/internal/settingsform"
	"dshgo/internal/spi"
)

// routeRecordingChat 记录子代理这一步真实发出的模型与推理档。
type routeRecordingChat struct {
	model     string
	reasoning string
	calls     int
}

func (c *routeRecordingChat) Stream(_ context.Context, req llm.Request, _ func(llm.Event) error) (*llm.Response, error) {
	c.calls++
	c.model = req.Model
	c.reasoning = req.Reasoning
	return &llm.Response{Content: "好", FinishReason: "stop"}, nil
}

func writeSubagentSelectionSettings(t *testing.T, home string, value map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Open(home).Write(settingsform.NamespaceSubagentModelSelection, value, nil); err != nil {
		t.Fatal(err)
	}
}

func policySession(t *testing.T, home, cwd, id string, routes []session.AllowedModelRoute) {
	t.Helper()
	if err := serveCreateWithID(home, cwd, id, "default"); err != nil {
		t.Fatalf("建会话 %s: %v", id, err)
	}
	if routes == nil {
		return
	}
	lg, err := session.OpenSession(home, cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.AppendSubagentModelSelectionPolicy(lg, routes); err != nil {
		lg.Close()
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
}

// 设置文档：没有命名空间 = 官方 shipped 默认（关闭 + 空清单）。
func TestSubagentModelSelectionSettingsDefaultsOff(t *testing.T) {
	home := t.TempDir()
	got, err := subagentModelSelectionFromSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || len(got.AllowedModels) != 0 {
		t.Fatalf("默认必须是关闭 + 空清单: %+v", got)
	}
}

func TestSubagentModelSelectionSettingsReadsRoutes(t *testing.T) {
	home := t.TempDir()
	writeSubagentSelectionSettings(t, home, map[string]any{
		"enabled": true,
		"allowedModels": []any{
			map[string]any{"provider": "gateway", "model": "m1"},
			map[string]any{"provider": "acme", "model": "m2"},
		},
	})
	got, err := subagentModelSelectionFromSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || len(got.AllowedModels) != 2 ||
		got.AllowedModels[0] != (session.AllowedModelRoute{Provider: "gateway", Model: "m1"}) ||
		got.AllowedModels[1] != (session.AllowedModelRoute{Provider: "acme", Model: "m2"}) {
		t.Fatalf("读回的设置不对: %+v", got)
	}
}

// 官方 current() 的硬判据：enabled 但一条路由都没有 = 装配期错误，不许静默关闭。
func TestSubagentModelSelectionSettingsRejectsEnabledWithoutRoutes(t *testing.T) {
	home := t.TempDir()
	writeSubagentSelectionSettings(t, home, map[string]any{"enabled": true, "allowedModels": []any{}})
	_, err := subagentModelSelectionFromSettings(home)
	if err == nil || !strings.Contains(err.Error(), "requires at least one allowed model") {
		t.Fatalf("enabled 且空清单必须响亮失败: %v", err)
	}
}

func TestSubagentModelSelectionSettingsRejectsMalformedRoutes(t *testing.T) {
	cases := []struct {
		name  string
		value []any
		want  string
	}{
		{"not-object", []any{"gateway/m1"}, "requires non-empty provider and model ids"},
		{"empty-model", []any{map[string]any{"provider": "gateway", "model": ""}},
			"requires non-empty provider and model ids"},
		{"duplicate", []any{
			map[string]any{"provider": "gateway", "model": "m1"},
			map[string]any{"provider": "gateway", "model": "m1"},
		}, `repeats route "gateway/m1"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeSubagentSelectionSettings(t, home, map[string]any{"enabled": true, "allowedModels": tc.value})
			_, err := subagentModelSelectionFromSettings(home)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want contains %q", err, tc.want)
			}
		})
	}
}

// 设置文档损坏必须响亮失败：读成"没有设置"会让界面保存成功而行为不变。
func TestSubagentModelSelectionSettingsRejectsCorruptDocument(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "settings-namespaces.json"),
		[]byte(`{"version":1,"namespaces":{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := subagentModelSelectionFromSettings(home); err == nil {
		t.Fatal("设置文档损坏必须报错，不许当成默认值")
	}
}

// 新顶层会话创建时写 durable 授权；关闭时一个字节都不写。
func TestServeCreateWritesDurableModelSelectionPolicy(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeSubagentSelectionSettings(t, home, map[string]any{
		"enabled":       true,
		"allowedModels": []any{map[string]any{"provider": "gateway", "model": "m1"}},
	})
	if err := serveCreateWithID(home, cwd, "session-policy-create", "default"); err != nil {
		t.Fatal(err)
	}
	routes, recorded, err := session.SessionSubagentModelSelectionPolicy(home, "session-policy-create")
	if err != nil {
		t.Fatal(err)
	}
	if !recorded || len(routes) != 1 || routes[0].Provider != "gateway" || routes[0].Model != "m1" {
		t.Fatalf("新会话必须带上 durable 授权: %+v %v", routes, recorded)
	}

	// 另一个没有设置文档的 home：关闭时一个字节都不写（官方 shipped 默认）。
	quietHome := t.TempDir()
	if err := serveCreateWithID(quietHome, cwd, "session-no-policy-create", "default"); err != nil {
		t.Fatal(err)
	}
	if _, recorded, err := session.SessionSubagentModelSelectionPolicy(quietHome, "session-no-policy-create"); recorded || err != nil {
		t.Fatalf("关闭时不许写策略: recorded=%v err=%v", recorded, err)
	}
}

// enabled 但清单为空：建会话必须失败（官方 current() 抛错），不许建出一个空授权会话。
func TestServeCreateFailsLoudWhenEnabledWithoutRoutes(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeSubagentSelectionSettings(t, home, map[string]any{"enabled": true, "allowedModels": []any{}})
	err := serveCreateWithID(home, cwd, "session-should-not-exist", "default")
	if err == nil {
		t.Fatal("建会话必须失败")
	}
	if session.SessionExists(home, "session-should-not-exist") {
		t.Fatal("设置非法时不该留下半份会话")
	}
}

func newRouteRunner(t *testing.T, home, model string, chat *routeRecordingChat) *subRunner {
	t.Helper()
	return &subRunner{
		chat: chat, tools: subagentEmptyRegistry{}, model: model,
		maxSteps: 1, timeout: 10 * time.Second, home: home,
	}
}

func testCtx(sessionID string) context.Context {
	return spi.WithSession(context.Background(), sessionID)
}

// 没有 durable 策略：显式选择只能点名**父路由**（官方 assertAllowedModelSelection 无策略分支）。
func TestSubagentRouteWithoutPolicyOnlyAllowsParentRoute(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-parent-route", nil)
	chat := &routeRecordingChat{}
	r := newRouteRunner(t, home, "parent-model", chat)
	ctx := testCtx("session-parent-route")

	provider, model := officialGatewayProviderID, "parent-model"
	got, err := r.resolveChildRoute(ctx, spi.SubagentRequest{Provider: &provider, Model: &model})
	if err != nil {
		t.Fatalf("点名父路由必须允许: %v", err)
	}
	if got.model != "parent-model" || got.chat != chat {
		t.Fatalf("父路由必须原样复用进程客户端: %+v", got)
	}

	other := "other-model"
	_, err = r.resolveChildRoute(ctx, spi.SubagentRequest{Provider: &provider, Model: &other})
	want := `child LLM route "gateway/other-model" is not allowed for this Session` +
		`; without a recorded model-selection policy, explicit selection is limited to the parent route`
	if err == nil || err.Error() != want {
		t.Fatalf("err=%v want=%q", err, want)
	}
}

// 有 durable 策略：清单内的路由允许，清单外的拒绝（错误文案逐字）。
func TestSubagentRouteUsesRecordedPolicy(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeModelCatalog(t, home, `{"id":"child-model","name":"Child"}`)
	policySession(t, home, cwd, "session-policy-route", []session.AllowedModelRoute{
		{Provider: officialGatewayProviderID, Model: "child-model"},
	})
	chat := &routeRecordingChat{}
	r := newRouteRunner(t, home, "parent-model", chat)
	ctx := testCtx("session-policy-route")

	provider, model := officialGatewayProviderID, "child-model"
	got, err := r.resolveChildRoute(ctx, spi.SubagentRequest{Provider: &provider, Model: &model})
	if err != nil {
		t.Fatalf("清单内路由必须允许: %v", err)
	}
	if got.model != "child-model" {
		t.Fatalf("解析后的模型不对: %q", got.model)
	}
	// 真跑一次：子循环必须用解析后的模型发请求。
	if _, err := r.runSub(ctx, spi.SubagentRequest{
		Prompt: "做点事", Provider: &provider, Model: &model,
	}, spi.ActivationOptions{}); err != nil {
		t.Fatalf("委派失败: %v", err)
	}
	if chat.model != "child-model" {
		t.Fatalf("子代理请求的模型 = %q，期望 child-model", chat.model)
	}

	notAllowed := "other-model"
	_, err = r.resolveChildRoute(ctx, spi.SubagentRequest{Provider: &provider, Model: &notAllowed})
	if err == nil || err.Error() != `child LLM route "gateway/other-model" is not allowed for this Session` {
		t.Fatalf("清单外路由必须拒绝: %v", err)
	}
}

// 官方 model-selection.ts:107-118 的四条形状错误逐字。
func TestSubagentRouteRejectsMalformedRequests(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-shapes", nil)
	r := newRouteRunner(t, home, "parent-model", &routeRecordingChat{})
	ctx := testCtx("session-shapes")
	empty, effort, provider, model := "", "high", officialGatewayProviderID, "m"
	cases := []struct {
		name string
		req  spi.SubagentRequest
		want string
	}{
		{"empty-provider", spi.SubagentRequest{Provider: &empty, Model: &model},
			"child LLM `provider` must be non-empty"},
		{"empty-model", spi.SubagentRequest{Provider: &provider, Model: &empty},
			"child LLM `model` must be non-empty"},
		{"empty-effort", spi.SubagentRequest{Provider: &provider, Model: &model, ReasoningEffort: &empty},
			"child LLM `reasoning_effort` must be non-empty"},
		{"provider-only", spi.SubagentRequest{Provider: &provider},
			"child LLM `provider` and `model` must be supplied together"},
		{"effort-only", spi.SubagentRequest{ReasoningEffort: &effort},
			"child LLM `reasoning_effort` requires both `provider` and `model`; supply both or omit all three fields"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.resolveChildRoute(ctx, tc.req)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v want=%q", err, tc.want)
			}
		})
	}
}

// 显式选择拿不到调用方会话 = 响亮失败（没有授权边界）。
func TestSubagentRouteRequiresCallingSession(t *testing.T) {
	home := t.TempDir()
	r := newRouteRunner(t, home, "parent-model", &routeRecordingChat{})
	provider, model := officialGatewayProviderID, "parent-model"
	_, err := r.resolveChildRoute(context.Background(), spi.SubagentRequest{Provider: &provider, Model: &model})
	if err == nil || !strings.Contains(err.Error(), "需要调用方会话") {
		t.Fatalf("没有会话身份必须响亮失败: %v", err)
	}
}

// provider 身份 = 目录 owned_by：点名与目录不一致必须拒绝，不许把 A 的模型发到 B 的配置上。
func TestSubagentRouteRejectsProviderMismatch(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeModelCatalog(t, home, `{"id":"acme-model","name":"Acme","owned_by":"acme"}`)
	policySession(t, home, cwd, "session-mismatch", []session.AllowedModelRoute{
		{Provider: officialGatewayProviderID, Model: "acme-model"},
	})
	r := newRouteRunner(t, home, "parent-model", &routeRecordingChat{})
	provider, model := officialGatewayProviderID, "acme-model"
	_, err := r.resolveChildRoute(testCtx("session-mismatch"),
		spi.SubagentRequest{Provider: &provider, Model: &model})
	if err == nil || !strings.Contains(err.Error(), `provider 是 "acme"`) {
		t.Fatalf("provider 与目录不一致必须拒绝: %v", err)
	}
}

// 推理档：目录声明了就必须是其中一档；合法档要真的进子代理请求。
func TestSubagentRouteValidatesAndForwardsReasoningEffort(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeModelCatalog(t, home, `{"id":"effort-model","name":"Effort","reasoningEfforts":{"off":null,"low":"low","high":"high"}}`)
	policySession(t, home, cwd, "session-effort", []session.AllowedModelRoute{
		{Provider: officialGatewayProviderID, Model: "effort-model"},
	})
	chat := &routeRecordingChat{}
	r := newRouteRunner(t, home, "parent-model", chat)
	ctx := testCtx("session-effort")
	provider, model := officialGatewayProviderID, "effort-model"

	bad := "ultra"
	if _, err := r.resolveChildRoute(ctx, spi.SubagentRequest{
		Provider: &provider, Model: &model, ReasoningEffort: &bad,
	}); err == nil || !strings.Contains(err.Error(), "没有 reasoningEffort=\"ultra\" 的声明") {
		t.Fatalf("未声明的推理档必须拒绝: %v", err)
	}

	good := "high"
	if _, err := r.runSub(ctx, spi.SubagentRequest{
		Prompt: "做点事", Provider: &provider, Model: &model, ReasoningEffort: &good,
	}, spi.ActivationOptions{}); err != nil {
		t.Fatalf("合法推理档委派失败: %v", err)
	}
	if chat.model != "effort-model" || chat.reasoning != "high" {
		t.Fatalf("子代理请求 model=%q reasoning=%q", chat.model, chat.reasoning)
	}
}

// 显式选择但省略推理档 = 用模型默认档（不发 reasoning），不继承父档。
func TestSubagentRouteClearsInheritedReasoningEffort(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	policySession(t, home, cwd, "session-no-effort", []session.AllowedModelRoute{
		{Provider: officialGatewayProviderID, Model: "parent-model"},
	})
	chat := &routeRecordingChat{}
	r := newRouteRunner(t, home, "parent-model", chat)
	provider, model := officialGatewayProviderID, "parent-model"
	if _, err := r.runSub(testCtx("session-no-effort"), spi.SubagentRequest{
		Prompt: "做点事", Provider: &provider, Model: &model,
	}, spi.ActivationOptions{}); err != nil {
		t.Fatal(err)
	}
	if chat.reasoning != "" {
		t.Fatalf("显式选择省略 effort 时不许带 reasoning: %q", chat.reasoning)
	}
}

// 修复前的回归面：没有出现任何模型字段时，resolveChildRoute 不读目录/策略，
// 原样返回父路由（旧调用方行为逐字节不变）。
func TestSubagentRouteOmittedKeepsParentRoute(t *testing.T) {
	r := newRouteRunner(t, t.TempDir(), "parent-model", &routeRecordingChat{})
	got, err := r.resolveChildRoute(context.Background(), spi.SubagentRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("省略模型字段时不许报错: %v", err)
	}
	if got.model != "parent-model" || got.provider != "" || got.reasoning != "" {
		t.Fatalf("省略时必须原样父路由: %+v", got)
	}
}

// 目录损坏是响亮失败（不许在假目录上做授权判断）。
func TestSubagentRouteRejectsCorruptCatalog(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "model-catalog.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	policySession(t, home, cwd, "session-corrupt-catalog", []session.AllowedModelRoute{
		{Provider: officialGatewayProviderID, Model: "m"},
	})
	r := newRouteRunner(t, home, "parent-model", &routeRecordingChat{})
	provider, model := officialGatewayProviderID, "m"
	_, err := r.resolveChildRoute(testCtx("session-corrupt-catalog"),
		spi.SubagentRequest{Provider: &provider, Model: &model})
	if err == nil || !strings.Contains(err.Error(), "读模型目录失败") {
		t.Fatalf("目录损坏必须响亮失败: %v", err)
	}
}

// 目录声明了 provider 却没有可用的 baseURL/凭据：宁可拒绝，不许静默退回父模型。
func TestSubagentRouteRejectsRouteWithoutProviderConfig(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeModelCatalog(t, home, fmt.Sprintf(
		`{"id":"acme-model","name":"Acme","owned_by":"acme","provider":{"baseURL":"%s"}}`, "not-a-url"))
	policySession(t, home, cwd, "session-bad-provider", []session.AllowedModelRoute{
		{Provider: "acme", Model: "acme-model"},
	})
	r := newRouteRunner(t, home, "parent-model", &routeRecordingChat{})
	provider, model := "acme", "acme-model"
	if _, err := r.resolveChildRoute(testCtx("session-bad-provider"),
		spi.SubagentRequest{Provider: &provider, Model: &model}); err == nil {
		t.Fatal("坏 provider 配置必须在供对方前置失败，不许静默退回父模型")
	}
}
