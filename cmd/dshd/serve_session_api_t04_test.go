package main

// GO.2 T04：官方 session 残余的**宿主侧**测试（真目录 / 真会话 / 真覆盖存储）。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/llm"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

// newT04Host 造一个"和 withOfficialSessionOptions 生产装配同形"的宿主对象。
func newT04Host(t *testing.T, home string, settings *serveSettingsStore) *officialSessionHost {
	t.Helper()
	return &officialSessionHost{
		src:        newSessionSource(home, ""),
		home:       home,
		selections: newModelSelectionStore(home),
		settings:   settings,
	}
}

// upsertT04Model 往真实模型目录里写一条模型（走生产 store 的校验与落盘）。
func upsertT04Model(t *testing.T, home, raw string) {
	t.Helper()
	store := llm.OpenModelCatalog(home)
	if _, err := store.Upsert(json.RawMessage(raw), nil, nil); err != nil {
		t.Fatalf("upsert 模型失败: %v", err)
	}
}

func TestOfficialSessionHostModelCatalogGroupsByOwnedBy(t *testing.T) {
	home := t.TempDir()
	upsertT04Model(t, home,
		`{"id":"model-a","name":"Alpha","owned_by":"local-provider","api":"openai-completions","baseURL":"http://127.0.0.1:1/v1","reasoningEfforts":{"off":null,"low":"low"},"input":["text"]}`)
	upsertT04Model(t, home,
		`{"id":"model-b","name":"Beta","owned_by":"local-provider","api":"openai-completions","baseURL":"http://127.0.0.1:1/v1"}`)
	upsertT04Model(t, home, `{"id":"plain-model","name":"Plain"}`)

	host := newT04Host(t, home, nil)
	catalog, err := host.ModelCatalog()
	if err != nil {
		t.Fatalf("ModelCatalog: %v", err)
	}
	// 没有显式默认模型时，默认 = 目录第一行（真实目录顺序，不是猜的）。
	if catalog.Default.Model != "model-a" || catalog.Default.Provider != "local-provider" {
		t.Fatalf("default 必须是目录第一行及其真实路由：%+v", catalog.Default)
	}
	if len(catalog.Groups) != 2 {
		t.Fatalf("必须按 owned_by 分两组（local-provider/gateway）：%+v", catalog.Groups)
	}
	if catalog.Groups[0].ID != "local-provider" || len(catalog.Groups[0].Models) != 2 {
		t.Fatalf("第一组必须是 local-provider 的两条模型：%+v", catalog.Groups[0])
	}
	if catalog.Groups[1].ID != officialGatewayProviderID || catalog.Groups[1].Models[0].ID != "plain-model" {
		t.Fatalf("没有 owned_by 的模型走 gateway 组：%+v", catalog.Groups[1])
	}
	reasoning := catalog.Groups[0].Models[0].Reasoning
	if reasoning == nil || len(reasoning.Efforts) != 1 || reasoning.Efforts[0].ID != "low" {
		t.Fatalf("reasoningEfforts 必须转成官方 efforts（排除 off）：%+v", reasoning)
	}
	if len(catalog.RoutableProviders) != 2 {
		t.Fatalf("两条真实路由都应可路由（provider 配置解析成功/走网关）：%v", catalog.RoutableProviders)
	}
	// 凭据缺失是 provider 级 failure，不是"整个目录不可用"。
	upsertT04Model(t, home,
		`{"id":"needs-key","name":"NeedsKey","owned_by":"keyed-provider","api":"openai-completions","baseURL":"http://127.0.0.1:1/v1","apiKeyEnv":"T04_MISSING_KEY"}`)
	os.Unsetenv("T04_MISSING_KEY")
	catalog, err = host.ModelCatalog()
	if err != nil {
		t.Fatalf("ModelCatalog(2): %v", err)
	}
	found := false
	for _, failure := range catalog.Failures {
		if failure.ID == "keyed-provider" {
			found = true
		}
	}
	if !found {
		t.Fatalf("凭据缺失必须记 provider 级 failure：%+v", catalog.Failures)
	}
}

func TestOfficialSessionHostResolveModelSelectionValidatesAndPersists(t *testing.T) {
	home := t.TempDir()
	upsertT04Model(t, home,
		`{"id":"model-a","owned_by":"local-provider","api":"openai-completions","baseURL":"http://127.0.0.1:1/v1","reasoningEfforts":{"off":null,"low":"low"}}`)
	lg, err := session.CreateSession(home, home, "s1", modes.DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
		t.Fatal(err)
	}
	if err := lg.Append("model/selection", map[string]any{"model": "model-a", "reasoningEffort": "low"}); err != nil {
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	host := newT04Host(t, home, nil)

	if _, err := host.ResolveModelSelection("s1", "wrong-provider", "model-a", ""); !errors.Is(err, transport.ErrOfficialBadInput) {
		t.Fatalf("provider 与真实路由不一致必须 400 类错误：%v", err)
	}
	if _, err := host.ResolveModelSelection("s1", "local-provider", "model-a", "nope"); !errors.Is(err, transport.ErrOfficialBadInput) {
		t.Fatalf("未声明的 reasoningEffort 必须 400 类错误：%v", err)
	}
	if _, err := host.ResolveModelSelection("s1", officialGatewayProviderID, "not-in-catalog", ""); err != nil {
		t.Fatalf("网关 legacy model id 允许：%v", err)
	}
	selection, err := host.ResolveModelSelection("s1", "local-provider", "model-a", "low")
	if err != nil {
		t.Fatalf("真实可路由的选择必须成功：%v", err)
	}
	if selection.Provider != "local-provider" || selection.Model != "model-a" || selection.ReasoningEffort != "low" {
		t.Fatalf("返回的必须是规范化后的真实选择：%+v", selection)
	}
	raw, err := os.ReadFile(filepath.Join(home, "state", "session-model-selection.json"))
	if err != nil {
		t.Fatalf("provider 元数据必须真实落盘：%v", err)
	}
	var doc modelSelectionDocument
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Sessions["s1"].Provider != "local-provider" {
		t.Fatalf("落盘内容不对：%v %s", err, raw)
	}
	// next 投影必须把 model 与记录对上才用记录的 provider。
	models := newModelOverrideStore(home)
	if err := models.Load(); err != nil {
		t.Fatal(err)
	}
	if err := models.Set("s1", "model-a"); err != nil {
		t.Fatal(err)
	}
	lastUsed, next, err := host.ModelSelection("s1")
	if err != nil {
		t.Fatalf("ModelSelection: %v", err)
	}
	if next == nil || next.Provider != "local-provider" || next.Model != "model-a" || next.ReasoningEffort != "low" {
		t.Fatalf("next 必须是真实选择：%+v", next)
	}
	// lastUsed 来自 v4 的 model/selection 帧（真实发生过的那次请求）。
	if lastUsed == nil || lastUsed.Provider != "local-provider" || lastUsed.Model != "model-a" {
		t.Fatalf("lastUsed 必须来自 v4 model/selection：%+v", lastUsed)
	}
	// 只改 model（session.setModel）不让旧 provider 记录冒充新选择。
	if err := models.Set("s1", "plain-model"); err != nil {
		t.Fatal(err)
	}
	lastUsed, next, err = host.ModelSelection("s1")
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.Model != "plain-model" || next.Provider != officialGatewayProviderID {
		t.Fatalf("旧 provider 记录不能被复用：%+v", next)
	}
	if err := host.ClearModelSelection("s1"); err != nil {
		t.Fatalf("ClearModelSelection: %v", err)
	}
	if record, err := host.selections.Get("s1"); err != nil || record.Provider != "" {
		t.Fatalf("清除后不该还有记录：%+v %v", record, err)
	}
}

func TestOfficialSessionHostAgentPresetAndSubagentCatalog(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "session-preset.json"),
		[]byte(`{"version":1,"sessions":{"s1":"review"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	host := newT04Host(t, home, nil)
	preset, err := host.AgentPreset("s1")
	if err != nil || preset == nil || *preset != "review" {
		t.Fatalf("preset 必须读真实覆盖：%v %v", preset, err)
	}
	preset, err = host.AgentPreset("s2")
	if err != nil || preset == nil || *preset != modes.DefaultID {
		t.Fatalf("没选过的会话回真实默认 preset：%v %v", preset, err)
	}
	// 一个损坏的 preset 文件必须响亮失败，不能静默当"默认"。
	if err := os.WriteFile(filepath.Join(home, "state", "session-preset.json"), []byte(`not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := host.AgentPreset("s1"); !errors.Is(err, transport.ErrStatePersistence) {
		t.Fatalf("损坏的 preset 文件必须回持久化错误：%v", err)
	}
}

func TestOfficialSessionHostSubagentCatalogReadsDurableSpawn(t *testing.T) {
	home := t.TempDir()
	// 先建一条父会话（孩子要挂在它下面）。
	parentLog, err := session.CreateSession(home, home, "parent-1", modes.DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	_ = parentLog.Append("turn/start", session.TurnStartData(1))
	_ = parentLog.Append("turn/end", session.TurnEndData(1, "stop", ""))
	if err := parentLog.Close(); err != nil {
		t.Fatal(err)
	}
	host := newT04Host(t, home, nil)
	record, err := (agent.ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}).Spawn(
		agent.ChildSpawnOptions{ChildSessionID: "kid-1", Prompt: "do it", Answer: "done"})
	if err != nil {
		t.Fatalf("ChildSpawn: %v", err)
	}
	catalog, err := host.SubagentCatalog("parent-1")
	if err != nil {
		t.Fatalf("SubagentCatalog: %v", err)
	}
	if len(catalog) != 1 {
		t.Fatalf("必须从 durable spawn 帧认出唯一子代理：%+v", catalog)
	}
	if catalog[0].ID != "kid-1" || catalog[0].Mode != "continuable" || catalog[0].CreatedAt != record.SpawnedAt {
		t.Fatalf("目录项必须来自真实 spawn 记录：%+v vs %+v", catalog[0], record)
	}
	// 子会话自己的身份投影：mode/seq 来自它自己的日志（不是父的）。
	events, seq, err := host.DurableEvents("kid-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Type != agent.EventChildSpawn || seq < events[0].Seq {
		t.Fatalf("子会话日志第一条必须是 spawn 帧：%+v", events)
	}
}

func TestOfficialSessionHostInitializeDefaultModelNeedsSettingsStore(t *testing.T) {
	home := t.TempDir()
	upsertT04Model(t, home, `{"id":"model-a","name":"Alpha"}`)
	host := newT04Host(t, home, nil)
	if _, err := host.InitializeDefaultModel(); !errors.Is(err, transport.ErrNotImplemented) {
		t.Fatalf("没有共享 settings store 时必须是结构化未实现：%v", err)
	}

	t.Setenv("DSHD_LLM_KEY", "")
	models := newModelOverrideStore(home)
	if err := models.Load(); err != nil {
		t.Fatal(err)
	}
	settings := newServeSettingsStore("", "", models, nil)
	host = newT04Host(t, home, settings)
	changed, err := host.InitializeDefaultModel()
	if err != nil || !changed {
		t.Fatalf("空默认 + 有目录模型时必须真的写入：changed=%v err=%v", changed, err)
	}
	doc, err := settings.Read("model")
	if err != nil {
		t.Fatal(err)
	}
	if effectiveSettingsValue(doc, "defaultModel") != "model-a" {
		t.Fatalf("默认模型必须落进 settings：%+v", doc)
	}
	// 已有可用默认 → no-op（官方"已配置就不动"）。
	changed, err = host.InitializeDefaultModel()
	if err != nil || changed {
		t.Fatalf("已有可用默认时必须是 no-op：changed=%v err=%v", changed, err)
	}
	// provider API key 已配置 → no-op（即使默认为空）。
	t.Setenv("DSHD_LLM_KEY", "test-key")
	emptyHome := t.TempDir()
	upsertT04Model(t, emptyHome, `{"id":"model-a","name":"Alpha"}`)
	emptyModels := newModelOverrideStore(emptyHome)
	if err := emptyModels.Load(); err != nil {
		t.Fatal(err)
	}
	emptySettings := newServeSettingsStore("", "", emptyModels, nil)
	host = newT04Host(t, emptyHome, emptySettings)
	changed, err = host.InitializeDefaultModel()
	if err != nil || changed {
		t.Fatalf("已配置 provider API key 时必须是 no-op：changed=%v err=%v", changed, err)
	}
}

func TestOfficialSessionHostSessionIDsListsRealSessions(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"s-a", "s-b"} {
		lg, err := session.CreateSession(home, home, id, modes.DefaultID)
		if err != nil {
			t.Fatal(err)
		}
		if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
			t.Fatal(err)
		}
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: id}})); err != nil {
			t.Fatal(err)
		}
		if err := lg.Close(); err != nil {
			t.Fatal(err)
		}
	}
	host := newT04Host(t, home, nil)
	ids, err := host.SessionIDs()
	if err != nil {
		t.Fatalf("SessionIDs: %v", err)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["s-a"] || !seen["s-b"] {
		t.Fatalf("control 的会话清单必须是真实磁盘会话：%v", ids)
	}
}
