package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/actioncards"
	"dshgo/internal/market"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

const testPlugin = "acme.audit"

type fakeBundles struct {
	mu      sync.Mutex
	bundles map[string]pluginmgr.BundleState
	err     error
	calls   int
}

func (f *fakeBundles) Bundles() ([]pluginmgr.BundleState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]pluginmgr.BundleState, 0, len(f.bundles))
	for _, bundle := range f.bundles {
		out = append(out, bundle)
	}
	return out, f.err
}

func (f *fakeBundles) Bundle(name string) (pluginmgr.BundleState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bundle, ok := f.bundles[name]
	return bundle, ok, f.err
}

func (f *fakeBundles) Enable(name string) (pluginmgr.PluginInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return pluginmgr.PluginInfo{}, f.err
	}
	bundle, ok := f.bundles[name]
	if !ok {
		return pluginmgr.PluginInfo{}, &pluginmgr.Error{Code: "not_found", Message: "不存在"}
	}
	bundle.Enabled = true
	f.bundles[name] = bundle
	return pluginmgr.PluginInfo{ID: name, Enabled: true}, nil
}

func marketFixture(t *testing.T) (*market.Store, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(market.Document{
			Version: 1, Title: map[string]string{"en": "Catalog"},
			Plugins: []market.Plugin{{
				Name: testPlugin, Title: map[string]string{"en": "Audit"}, Description: map[string]string{"en": "security audit"},
				Labels: []string{"official"}, Versions: []market.Version{{Version: "1.0.0"}},
			}},
		})
	}))
	t.Cleanup(server.Close)
	home := t.TempDir()
	store := market.Open(home)
	store.Client = server.Client()
	if err := store.SetURLs([]string{server.URL + "/index.json"}); err != nil {
		t.Fatalf("SetURLs: %v", err)
	}
	return store, server
}

func seedCard(t *testing.T, home, sessionID, callID, name string) *actioncards.Service {
	t.Helper()
	lg, err := session.CreateSession(home, filepath.Join(home, "cwd"), sessionID, "default")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"action": Action, "title": "Audit", "brief": "安全审计", "details": map[string]any{"name": name},
	})
	if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, callID, "propose_action", string(args))); err != nil {
		t.Fatalf("tool/call: %v", err)
	}
	if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, callID,
		[]spi.Block{{Type: "text", Text: "Card shown"}}, false, "", nil)); err != nil {
		t.Fatalf("tool/result: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	cards := actioncards.New(home)
	return cards
}

func newService(t *testing.T, home string, bundles *fakeBundles, store *market.Store, cards *actioncards.Service) *Service {
	t.Helper()
	if bundles == nil {
		bundles = &fakeBundles{bundles: map[string]pluginmgr.BundleState{
			testPlugin: {Name: testPlugin, Version: "1.0.0", Enabled: false},
		}}
	}
	if store == nil {
		store, _ = marketFixture(t)
	}
	if cards == nil {
		cards = actioncards.New(home)
	}
	service, err := New(home, bundles, store, cards)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func TestDescribeAvailableAndRefusals(t *testing.T) {
	home := t.TempDir()
	store, _ := marketFixture(t)
	cards := seedCard(t, home, "s-1", "call-1", testPlugin)
	bundles := &fakeBundles{bundles: map[string]pluginmgr.BundleState{
		testPlugin: {Name: testPlugin, Version: "1.0.0", Enabled: false},
	}}
	service := newService(t, home, bundles, store, cards)

	result, err := service.Describe(context.Background(), "s-1", "call-1")
	if err != nil || result["status"] != "available" || result["title"] != "Audit" || result["description"] != "security audit" || result["enabled"] != false {
		t.Fatalf("describe = %+v, %v", result, err)
	}
	if _, err := service.Describe(context.Background(), "missing", "call-1"); err != nil {
		t.Fatalf("not-live-root 不应 error：%v", err)
	}
	if result, _ := service.Describe(context.Background(), "missing", "call-1"); result["reason"] != "not-live-root" {
		t.Fatalf("missing root = %+v", result)
	}
	if result, _ := service.Describe(context.Background(), "s-1", "no-such-call"); result["reason"] != "unknown" {
		t.Fatalf("unknown call = %+v", result)
	}

	// 没有索引源：必须是 unavailable，不能静态推荐。
	emptyHome := t.TempDir()
	emptyCards := seedCard(t, emptyHome, "s-empty", "call-empty", testPlugin)
	empty := newService(t, emptyHome, &fakeBundles{bundles: map[string]pluginmgr.BundleState{
		testPlugin: {Name: testPlugin, Version: "1.0.0"},
	}}, market.Open(emptyHome), emptyCards)
	if result, _ := empty.Describe(context.Background(), "s-empty", "call-empty"); result["reason"] != "unavailable" {
		t.Fatalf("无索引应 unavailable，得到 %+v", result)
	}

	// 已安装但市场里没有：unavailable。
	otherHome := t.TempDir()
	otherCards := seedCard(t, otherHome, "s-other", "call-other", "acme.other")
	otherStore, _ := marketFixture(t)
	other := newService(t, otherHome, &fakeBundles{bundles: map[string]pluginmgr.BundleState{
		"acme.other": {Name: "acme.other", Version: "1.0.0"},
	}}, otherStore, otherCards)
	if result, _ := other.Describe(context.Background(), "s-other", "call-other"); result["reason"] != "unavailable" {
		t.Fatalf("市场无条目应 unavailable，得到 %+v", result)
	}
}

func TestEnableKindReallyEnablesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	store, _ := marketFixture(t)
	cards := seedCard(t, home, "s-1", "call-1", testPlugin)
	bundles := &fakeBundles{bundles: map[string]pluginmgr.BundleState{
		testPlugin: {Name: testPlugin, Version: "1.0.0", Enabled: false},
	}}
	service := newService(t, home, bundles, store, cards)

	result, err := cards.Act(context.Background(), "s-1", "call-1", "enable", nil)
	if err != nil || result.Status != actioncards.StatusSettled || result.Decision == nil || result.Decision.Phase != "enabled" {
		t.Fatalf("enable = %+v, %v", result, err)
	}
	if bundles.calls != 1 {
		t.Fatalf("manager.Enable 调用 %d 次", bundles.calls)
	}
	bundle, _, _ := bundles.Bundle(testPlugin)
	if !bundle.Enabled {
		t.Fatal("manager 状态没有真的打开")
	}
	view, _ := cards.View("s-1")
	if view.Decisions["call-1"].Phase != "enabled" {
		t.Fatalf("decision = %+v", view.Decisions)
	}
	// 重复 action 不重复调用 manager。
	if result, _ := cards.Act(context.Background(), "s-1", "call-1", "enable", nil); result.Status != actioncards.StatusSettled {
		t.Fatalf("重复 enable = %+v", result)
	}
	if bundles.calls != 1 {
		t.Fatalf("重复 enable 又调 manager：%d", bundles.calls)
	}
	_ = service
}

func TestEnableMissingAndFailure(t *testing.T) {
	home := t.TempDir()
	store, _ := marketFixture(t)
	cards := seedCard(t, home, "s-1", "call-1", "acme.missing")
	bundles := &fakeBundles{bundles: map[string]pluginmgr.BundleState{}}
	_ = newService(t, home, bundles, store, cards)
	result, err := cards.Act(context.Background(), "s-1", "call-1", "enable", nil)
	if err != nil || result.Status != actioncards.StatusRefused || result.Reason != "unavailable" {
		t.Fatalf("缺包 = %+v, %v", result, err)
	}

	failHome := t.TempDir()
	failCards := seedCard(t, failHome, "s-fail", "call-fail", testPlugin)
	failBundles := &fakeBundles{bundles: map[string]pluginmgr.BundleState{
		testPlugin: {Name: testPlugin, Version: "1.0.0"},
	}, err: &pluginmgr.Error{Code: "profile_write_failed", Message: "磁盘坏了"}}
	_ = newService(t, failHome, failBundles, store, failCards)
	result, err = failCards.Act(context.Background(), "s-fail", "call-fail", "enable", nil)
	if err != nil || result.Status != actioncards.StatusSettled || result.Decision == nil || result.Decision.Phase != "failed" {
		t.Fatalf("失败 enable = %+v, %v", result, err)
	}
	if result.Decision.Data["errorCode"] != "profile_write_failed" {
		t.Fatalf("失败 errorCode = %+v", result.Decision.Data)
	}
}

func TestReconcileUnconfirmedEnabled(t *testing.T) {
	home := t.TempDir()
	store, _ := marketFixture(t)
	cards := seedCard(t, home, "s-1", "call-1", testPlugin)
	bundles := &fakeBundles{bundles: map[string]pluginmgr.BundleState{
		testPlugin: {Name: testPlugin, Version: "1.0.0", Enabled: true},
	}}
	service := newService(t, home, bundles, store, cards)
	// 先手工写一条 unconfirmed decision（模拟上次启用被 reload 截断）。
	if err := service.cards.Record(context.Background(), "s-1", actioncards.Decision{
		CallID: "call-1", Action: Action, Phase: "unconfirmed",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	result, err := cards.Act(context.Background(), "s-1", "call-1", "dismiss", nil)
	if err != nil || result.Status != actioncards.StatusSettled || result.Decision == nil || result.Decision.Phase != "enabled" {
		t.Fatalf("unconfirmed 插件已开应 reconcile enabled，得到 %+v, %v", result, err)
	}
}

func TestEnsureSkillContainsRealCatalog(t *testing.T) {
	home := t.TempDir()
	store, _ := marketFixture(t)
	bundles := &fakeBundles{bundles: map[string]pluginmgr.BundleState{
		testPlugin: {Name: testPlugin, Version: "1.0.0", Enabled: true},
	}}
	service := newService(t, home, bundles, store, actioncards.New(home))
	if err := service.EnsureSkill(context.Background()); err != nil {
		t.Fatalf("EnsureSkill: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "skills", Skill, "SKILL.md"))
	if err != nil {
		t.Fatalf("读 skill: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "acme.audit") || !strings.Contains(text, "Audit") || !strings.Contains(text, "true") {
		t.Fatalf("skill 没有真实目录：%s", text)
	}
}

func TestNewDuplicateKindRejected(t *testing.T) {
	home := t.TempDir()
	store, _ := marketFixture(t)
	cards := actioncards.New(home)
	bundles := &fakeBundles{bundles: map[string]pluginmgr.BundleState{}}
	if _, err := New(home, bundles, store, cards); err != nil {
		t.Fatalf("first New: %v", err)
	}
	if _, err := New(home, bundles, store, cards); err == nil {
		t.Fatal("重复注册 kind 没有报错")
	}
}

var _ = errors.Is

func TestProposeActionKindRequiresOfficialPluginsSkill(t *testing.T) {
	home := t.TempDir()
	store, _ := marketFixture(t)
	cards := seedCard(t, home, "s-skill", "call-skill", testPlugin)
	bundles := &fakeBundles{bundles: map[string]pluginmgr.BundleState{
		testPlugin: {Name: testPlugin, Version: "1.0.0"},
	}}
	_ = newService(t, home, bundles, store, cards)
	tool := cards.Tool()
	ctx := spi.WithSession(context.Background(), "s-skill")
	args := json.RawMessage(`{"action":"enable_official_plugin","title":"Audit","brief":"安全审计","details":{"name":"acme.audit"}}`)
	if _, err := tool.Exec(ctx, args); err == nil {
		t.Fatal("official-plugins skill 未加载时 propose_action 竟然成功")
	}
	// 真实记录一次 skill 加载，再提案。
	path, err := session.FindByID(home, "s-skill")
	if err != nil {
		t.Fatal(err)
	}
	lg, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatal(err)
	}
	skillArgs, _ := json.Marshal(map[string]any{"name": Skill})
	if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, "skill-call", "skill", string(skillArgs))); err != nil {
		t.Fatal(err)
	}
	if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, "skill-call", []spi.Block{{Type: "text", Text: "loaded"}}, false, "", nil)); err != nil {
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := tool.Exec(ctx, args)
	if err != nil {
		t.Fatalf("skill 加载后 propose_action: %v", err)
	}
	data, _ := result.Data.(map[string]any)
	if data["action"] != Action || data["title"] != "Audit" {
		t.Fatalf("propose_action 结果 = %+v", result.Data)
	}
}
