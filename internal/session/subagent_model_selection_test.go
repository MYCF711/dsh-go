package session

import (
	"encoding/json"
	"strings"
	"testing"

	"dshgo/codecs/v4"
)

func mustCreatePolicySession(t *testing.T, home, cwd, id string) *Log {
	t.Helper()
	log, err := CreateSession(home, cwd, id, "default")
	if err != nil {
		t.Fatal(err)
	}
	return log
}

// 官方 assertAllowedModelRoutes（model-selection.ts:42-62）的三条判据逐条钉住。
func TestValidateAllowedModelRoutes(t *testing.T) {
	ok := []AllowedModelRoute{{Provider: "gateway", Model: "m1"}, {Provider: "gateway", Model: "m2"}}
	if err := ValidateAllowedModelRoutes(ok); err != nil {
		t.Fatalf("合法路由被拒: %v", err)
	}
	cases := []struct {
		name   string
		routes []AllowedModelRoute
		want   string
	}{
		{"empty-provider", []AllowedModelRoute{{Provider: "", Model: "m"}},
			"subagent model selection requires non-empty provider and model ids"},
		{"empty-model", []AllowedModelRoute{{Provider: "p", Model: ""}},
			"subagent model selection requires non-empty provider and model ids"},
		{"duplicate", []AllowedModelRoute{{Provider: "p", Model: "m"}, {Provider: "p", Model: "m"}},
			`subagent model selection repeats route "p/m"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAllowedModelRoutes(tc.routes)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v want=%q", err, tc.want)
			}
		})
	}
}

// 空清单在**写侧**就拒绝：官方投影 apply 对空数组抛
// `subagent/model-selection-policy requires at least one route`（model-selection-state.ts:42-44）。
func TestAppendSubagentModelSelectionPolicyRejectsEmpty(t *testing.T) {
	log := mustCreatePolicySession(t, t.TempDir(), t.TempDir(), "session-empty-policy")
	defer log.Close()
	err := AppendSubagentModelSelectionPolicy(log, nil)
	if err == nil || !strings.Contains(err.Error(), "requires at least one route") {
		t.Fatalf("空清单必须响亮失败: %v", err)
	}
	if log.Seq() != 0 {
		t.Fatalf("失败时不许推进 seq: %d", log.Seq())
	}
}

// 写入 → 读回：事件名与负载形状逐字对官方（`{allowedModels:[{provider,model}]}`）。
func TestAppendAndReadSubagentModelSelectionPolicy(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	log := mustCreatePolicySession(t, home, cwd, "session-policy")
	routes := []AllowedModelRoute{{Provider: "gateway", Model: "m1"}, {Provider: "acme", Model: "m2"}}
	if err := AppendSubagentModelSelectionPolicy(log, routes); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	got, recorded, err := SessionSubagentModelSelectionPolicy(home, "session-policy")
	if err != nil {
		t.Fatal(err)
	}
	if !recorded || len(got) != 2 || got[0] != routes[0] || got[1] != routes[1] {
		t.Fatalf("读回的路由不对: recorded=%v got=%+v", recorded, got)
	}
	// detached：改返回值不能影响下一次读。
	got[0].Model = "mutated"
	again, _, err := SessionSubagentModelSelectionPolicy(home, "session-policy")
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Model != "m1" {
		t.Fatalf("返回值不是副本: %+v", again)
	}
}

// 官方 apply 只认**第一条** policy 事件（model-selection-state.ts:39）；后续一律忽略。
func TestSubagentModelSelectionPolicyKeepsFirstEvent(t *testing.T) {
	first := json.RawMessage(`{"allowedModels":[{"provider":"p","model":"a"}]}`)
	second := json.RawMessage(`{"allowedModels":[{"provider":"q","model":"b"}]}`)
	events := []v4.Event{
		{Type: "turn/start", Seq: 0},
		{Type: EventSubagentModelSelectionPolicy, Seq: 1, Data: first},
		{Type: EventSubagentModelSelectionPolicy, Seq: 2, Data: second},
	}
	got, recorded, err := SubagentModelSelectionPolicyOf(events)
	if err != nil {
		t.Fatal(err)
	}
	if !recorded || len(got) != 1 || got[0].Provider != "p" || got[0].Model != "a" {
		t.Fatalf("第一条之后的策略事件必须被忽略: recorded=%v got=%+v", recorded, got)
	}
	none, recorded, err := SubagentModelSelectionPolicyOf([]v4.Event{{Type: "user/message", Seq: 0}})
	if err != nil || recorded || none != nil {
		t.Fatalf("没有策略 = (nil,false,nil): %+v %v %v", none, recorded, err)
	}
}

// 坏负载必须响亮失败（官方 apply 抛错），不许当成"没有策略"静默放行。
func TestSubagentModelSelectionPolicyRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"missing-key", `{}`, "缺少 allowedModels"},
		{"empty-array", `{"allowedModels":[]}`, "requires at least one route"},
		{"empty-id", `{"allowedModels":[{"provider":"","model":"m"}]}`,
			"requires non-empty provider and model ids"},
		{"duplicate", `{"allowedModels":[{"provider":"p","model":"m"},{"provider":"p","model":"m"}]}`,
			`repeats route "p/m"`},
		{"not-array", `{"allowedModels":{"provider":"p","model":"m"}}`, "负载解不开"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := SubagentModelSelectionPolicyOf([]v4.Event{{
				Type: EventSubagentModelSelectionPolicy, Data: json.RawMessage(tc.raw),
			}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want contains %q", err, tc.want)
			}
		})
	}
}

// 投影单元：官方 key / stateVersion / init=null / 只认第一条；坏负载 fail-closed
// （保持 nil，严格读取那边会 loud）。
func TestSubagentModelSelectionProjectionUnit(t *testing.T) {
	unit := SubagentModelSelectionUnit()
	if unit.Key != "subagentModelSelectionPolicy" {
		t.Fatalf("投影键必须是官方 key: %q", unit.Key)
	}
	if unit.StateVersion != 1 {
		t.Fatalf("stateVersion 必须是 1: %d", unit.StateVersion)
	}
	if unit.View != nil {
		t.Fatal("官方这份是 host-only 投影：View 必须为 nil（不上线）")
	}
	init := unit.Init(v4.Header{}, 0)
	if init != nil {
		t.Fatalf("init 必须是 null: %v", init)
	}
	state := unit.Apply(init, v4.Event{Type: "turn/start"})
	if state != nil {
		t.Fatalf("无关事件不能改状态: %v", state)
	}
	state = unit.Apply(state, v4.Event{
		Type: EventSubagentModelSelectionPolicy,
		Data: json.RawMessage(`{"allowedModels":[{"provider":"p","model":"m"}]}`),
	})
	list, ok := state.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("合法事件必须折叠成一条路由: %T %v", state, state)
	}
	if err := unit.Schema.Validate(state); err != nil {
		t.Fatalf("折叠结果必须过 schema: %v", err)
	}
	// 第二条忽略。
	again := unit.Apply(state, v4.Event{
		Type: EventSubagentModelSelectionPolicy,
		Data: json.RawMessage(`{"allowedModels":[{"provider":"q","model":"n"}]}`),
	})
	if list2, _ := again.([]any); len(list2) != 1 {
		t.Fatalf("第一条之后的策略必须被忽略: %v", again)
	}
	// 坏负载 fail-closed：保持 nil。
	bad := unit.Apply(nil, v4.Event{
		Type: EventSubagentModelSelectionPolicy,
		Data: json.RawMessage(`{"allowedModels":[]}`),
	})
	if bad != nil {
		t.Fatalf("坏负载必须保持 nil（允许严格读取 loud）: %v", bad)
	}
}

// 子会话继承：父有策略 → 原样写进子日志；父不存在 → 不写不报错；
// 父日志存在但负载坏 → 响亮失败（不静默降级到"只能点名父路由"）。
func TestInheritSubagentModelSelectionPolicy(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	parent, err := CreateSession(home, cwd, "session-parent-policy", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendSubagentModelSelectionPolicy(parent, []AllowedModelRoute{{Provider: "p", Model: "m"}}); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	child := mustCreatePolicySession(t, home, cwd, "session-child-policy")
	if err := InheritSubagentModelSelectionPolicy(child, home, "session-parent-policy"); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	got, recorded, err := SessionSubagentModelSelectionPolicy(home, "session-child-policy")
	if err != nil || !recorded || len(got) != 1 || got[0].Provider != "p" {
		t.Fatalf("子会话没继承: %+v %v %v", got, recorded, err)
	}

	// 父不存在：安静跳过（官方 ctx.sessions.get === undefined）。
	orphan := mustCreatePolicySession(t, home, cwd, "session-orphan")
	if err := InheritSubagentModelSelectionPolicy(orphan, home, "no-such-parent"); err != nil {
		t.Fatalf("父不存在不是错误: %v", err)
	}
	if err := orphan.Close(); err != nil {
		t.Fatal(err)
	}
	if _, recorded, err := SessionSubagentModelSelectionPolicy(home, "session-orphan"); err != nil || recorded {
		t.Fatalf("父不存在时不许写策略: %v %v", recorded, err)
	}

	// 父日志存在但负载坏：必须 loud。
	broken, err := CreateSession(home, cwd, "session-broken-parent", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Append(EventSubagentModelSelectionPolicy, map[string]any{"allowedModels": []any{}}); err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	receiver := mustCreatePolicySession(t, home, cwd, "session-broken-child")
	defer receiver.Close()
	if err := InheritSubagentModelSelectionPolicy(receiver, home, "session-broken-parent"); err == nil {
		t.Fatal("父策略负载坏必须响亮失败，不许静默降级")
	}
}
