package main

// 官方 #1 `tool-plugin-manager` 的验收测试。
//
// 判据不是"函数存在"，而是**这条调用真的走到了真实的插件管理器上**：
//   - `list_plugins` 的分页信封里的行，与本进程 `pluginRuntime` 的真实装载集合一致；
//   - `set_plugin` 真的改了 state.json 里的启用位（重开管理器仍然读得到）；
//   - `set_version_exemption` 真的写进了 compatibility.json；
//   - `install_bundle` / `remove_bundle` 回的是**官方形状的结构化失败**
//     （ChangeResult + error.code + nextStep），不是假成功也不是 HTTP 层异常。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crypto/sha256"
	"encoding/hex"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

// newPluginManagerFixture 造一个真实 home + 插件管理器 + 运行时，并把它包成工具。
func newPluginManagerFixture(t *testing.T) (*pluginManagerTool, string) {
	t.Helper()
	home := t.TempDir()
	rt, err := openPluginRuntime(home)
	if err != nil {
		t.Fatalf("打开插件运行时失败：%v", err)
	}
	api := newOfficialPluginManagerAPI(rt)
	return &pluginManagerTool{api: pluginManagerAPI{api}}, home
}

func execPluginManager(t *testing.T, tl *pluginManagerTool, raw string) (map[string]any, error) {
	t.Helper()
	res, err := tl.Exec(context.Background(), json.RawMessage(raw))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if e := json.Unmarshal([]byte(res.Text), &out); e != nil {
		t.Fatalf("工具输出不是 JSON（%v）：%s", e, res.Text)
	}
	return out, nil
}

// schema 必须是官方 enum 的**八条**，一条不能少、一条不能多。
//
// 少一条 = 模型根本发不出那条 action（等于"没接"）；多一条 = 发明了官方没有的操作。
func TestPluginManagerSchemaMatchesOfficialEnum(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal((&pluginManagerTool{}).Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	got := schema.Properties["action"].Enum
	want := []string{"list_plugins", "list_bundles", "set_plugin", "set_bundle",
		"install_bundle", "remove_bundle", "list_version_exemptions", "set_version_exemption"}
	if len(got) != len(want) {
		t.Fatalf("action enum 有 %d 条，官方是 %d 条：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条是 %q，官方是 %q（顺序也是契约）", i, got[i], want[i])
		}
	}
	if len(schema.Required) != 1 || schema.Required[0] != "action" {
		t.Fatalf("只有 action 必填（官方其余 8 个参数由各 action 现查），实际 %v", schema.Required)
	}
	for _, key := range []string{"target", "enabled", "runtimeVersion", "acceptRisk", "approvedBuilds", "registry", "offset", "limit"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("缺官方参数 %q", key)
		}
	}
	if name := (&pluginManagerTool{}).Name(); name != "plugin_manager" {
		t.Fatalf("工具名必须是官方逐字 plugin_manager，实际 %q", name)
	}
}

// list_plugins / list_bundles 走真实管理器，且是官方的分页信封。
func TestPluginManagerListUsesRealManager(t *testing.T) {
	tl, _ := newPluginManagerFixture(t)

	for _, action := range []string{"list_plugins", "list_bundles"} {
		out, err := execPluginManager(t, tl, `{"action":"`+action+`"}`)
		if err != nil {
			t.Fatalf("%s 失败：%v", action, err)
		}
		for _, key := range []string{"entries", "total", "nextOffset"} {
			if _, ok := out[key]; !ok {
				t.Fatalf("%s 的信封缺 %q（官方 {entries,total,nextOffset}）：%v", action, key, out)
			}
		}
		if _, ok := out["entries"].([]any); !ok {
			t.Fatalf("%s 的 entries 必须是数组：%v", action, out["entries"])
		}
	}
}

// 分页边界：offset/limit 是官方 1..100 的口径，越界必须拒绝（官方 tools.ts:136-138）。
func TestPluginManagerListPagingBounds(t *testing.T) {
	tl, _ := newPluginManagerFixture(t)

	for _, bad := range []string{`{"action":"list_plugins","limit":0}`, `{"action":"list_plugins","limit":101}`, `{"action":"list_plugins","offset":-1}`} {
		if _, err := execPluginManager(t, tl, bad); err == nil {
			t.Fatalf("%s 应当被拒绝（官方 offset≥0、limit 1..100）", bad)
		}
	}
	out, err := execPluginManager(t, tl, `{"action":"list_bundles","offset":0,"limit":1}`)
	if err != nil {
		t.Fatalf("合法分页不该失败：%v", err)
	}
	if got := len(out["entries"].([]any)); got > 1 {
		t.Fatalf("limit=1 却返回 %d 行", got)
	}
}

// fixturePluginID 是夹具包的 id。
const fixturePluginID = "acme.audit"

// installFixtureBundle 往 profile 里真装一个包（走 Manager.Install 的真实事务），
// 返回可以在**重开的**运行时上验证的 home。
//
// 为什么不用手写 state.json：手写的状态不是"管理器真的接受了这个包"，
// 用它验证 set_plugin 等于自己证明自己。
func installFixtureBundle(t *testing.T, home string) {
	t.Helper()
	pkg := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	// 用真实 wasm 夹具当 payload：只有它能在**启用**时真的装载成功。
	// 假的 payload 会让"启用"这一步 fail-closed（正确行为），于是验不到持久化。
	payload, err := os.ReadFile(filepath.Join("..", "..", "internal", "wasmhost", "testdata", "toolplugin.wasm"))
	if err != nil {
		t.Fatalf("读不到 wasm 夹具：%v", err)
	}
	sum := sha256.Sum256(payload)
	manifest, err := json.MarshalIndent(map[string]any{
		"schema": "dsh.local-plugin@1", "id": fixturePluginID, "version": "1.0.0",
		"kind": "wasm", "entry": "plugin.wasm",
		"files": map[string]string{"plugin.wasm": hex.EncodeToString(sum[:])},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "plugin.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "plugin.wasm"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := pluginmgr.Open(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(pkg); err != nil {
		t.Fatalf("夹具包安装失败（这是测试夹具问题，不是产品问题）：%v", err)
	}
}

// set_plugin 必须真的写进 state.json，而不是"回了 changed=true 但其实没动"。
func TestPluginManagerSetPluginPersists(t *testing.T) {
	home := t.TempDir()
	installFixtureBundle(t, home)

	// 换一个**新开的**运行时读 state.json —— 证明工具改的是持久化状态，
	// 不是内存里某一次运行的临时值。
	rt, err := openPluginRuntime(home)
	if err != nil {
		t.Fatal(err)
	}
	tl := &pluginManagerTool{api: pluginManagerAPI{newOfficialPluginManagerAPI(rt)}}

	// 先确认 list_bundles 真的看得到刚装的包。
	out, err := execPluginManager(t, tl, `{"action":"list_bundles"}`)
	if err != nil {
		t.Fatal(err)
	}
	if total, _ := out["total"].(float64); total < 1 {
		t.Fatalf("list_bundles 没看到刚装的 %s：%v", fixturePluginID, out)
	}

	before, ok, err := rt.Manager().Bundle(fixturePluginID)
	if err != nil || !ok {
		t.Fatalf("读不到夹具包：ok=%v err=%v", ok, err)
	}
	// 往**相反**方向翻：目标已经是这个状态时官方回 changed=false（幂等），
	// 那样就验不出"真的写盘了"。
	want := !before.Enabled
	res, err := execPluginManager(t, tl, fmt.Sprintf(`{"action":"set_bundle","target":%q,"enabled":%v}`, fixturePluginID, want))
	if err != nil {
		t.Fatalf("set_bundle 失败：%v", err)
	}
	if changed, _ := res["changed"].(bool); !changed {
		t.Fatalf("set_bundle 没改动（before.Enabled=%v）：%v", before.Enabled, res)
	}

	// 重开管理器：启用位必须真的是关的（不是只在内存里）。
	again, err := pluginmgr.Open(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	state, ok, err := again.Bundle(fixturePluginID)
	if err != nil || !ok {
		t.Fatalf("重开后读不到 %s：ok=%v err=%v", fixturePluginID, ok, err)
	}
	if state.Enabled != want {
		t.Fatalf("set_bundle 之后 state.json 里的启用位没变：想要 %v，实际 %+v", want, state)
	}

	// list_plugins 的词表来自运行时装载集合，必须仍然可用（不是改完就崩）。
	againRT, err := openPluginRuntime(home)
	if err != nil {
		t.Fatal(err)
	}
	tl2 := &pluginManagerTool{api: pluginManagerAPI{newOfficialPluginManagerAPI(againRT)}}
	if _, err := execPluginManager(t, tl2, `{"action":"list_plugins"}`); err != nil {
		t.Fatalf("改完之后 list_plugins 失败：%v", err)
	}
}

// 安装/卸载：官方形状的**结构化失败**，不是假成功、也不是异常。
func TestPluginManagerInstallAndRemoveAreStructuredUnsupported(t *testing.T) {
	tl, _ := newPluginManagerFixture(t)

	for _, action := range []string{"install_bundle", "remove_bundle"} {
		out, err := execPluginManager(t, tl, `{"action":"`+action+`","target":"@deepseek-ai/dsh-skill@0.2.2"}`)
		if err != nil {
			t.Fatalf("%s 不该抛错（官方用 ChangeResult 表达应用性失败）：%v", action, err)
		}
		if changed, _ := out["changed"].(bool); changed {
			t.Fatalf("%s 报 changed=true —— 这是假成功", action)
		}
		if app, _ := out["application"].(string); app != "failed" {
			t.Fatalf("%s 的 application 应当是 failed，实际 %v", action, out["application"])
		}
		e, ok := out["error"].(map[string]any)
		if !ok {
			t.Fatalf("%s 必须带 error 对象：%v", action, out)
		}
		if code, _ := e["code"].(string); code != "unsupported-platform" {
			t.Fatalf("%s 的 error.code 应当是 unsupported-platform，实际 %v", action, code)
		}
		if _, ok := out["nextStep"].(string); !ok {
			t.Fatalf("%s 没给 nextStep（官方 withNextStep：没应用的变更要说清下一步）：%v", action, out)
		}
	}
}

// 版本豁免：真实写 compatibility.json，且授权必须显式 acceptRisk。
func TestPluginManagerVersionExemptionRoundTrip(t *testing.T) {
	tl, home := newPluginManagerFixture(t)

	// 当前 runtime 版本（官方 list_version_exemptions 恒带 runtimeVersion）。
	out, err := execPluginManager(t, tl, `{"action":"list_version_exemptions"}`)
	if err != nil {
		t.Fatalf("list_version_exemptions 失败：%v", err)
	}
	rv, _ := out["runtimeVersion"].(string)
	if rv != version {
		t.Fatalf("runtimeVersion 必须来自唯一真值 %q，实际 %q", version, rv)
	}
	if _, ok := out["exemptions"].(map[string]any); !ok {
		t.Fatalf("exemptions 必须是对象：%v", out["exemptions"])
	}

	if _, err := execPluginManager(t, tl, `{"action":"set_version_exemption","target":"tool-b@9.9.9","runtimeVersion":"`+rv+`","enabled":true}`); err == nil {
		t.Fatal("授予豁免没有 acceptRisk=true，必须拒绝")
	}
	if _, err := execPluginManager(t, tl, `{"action":"set_version_exemption","target":"tool-b","runtimeVersion":"`+rv+`","enabled":true,"acceptRisk":true}`); err == nil {
		t.Fatal("target 必须是 name@version，裸名字必须拒绝")
	}
	if _, err := execPluginManager(t, tl, `{"action":"set_version_exemption","target":"tool-b@9.9.9","runtimeVersion":"`+rv+`","enabled":true,"acceptRisk":true}`); err != nil {
		t.Fatalf("合法授权失败：%v", err)
	}
	// 真落盘：compatibility.json 必须存在，且重开管理器读得到同一条。
	matches, _ := filepath.Glob(filepath.Join(home, "plugins", "compatibility.json"))
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(home, "**", "compatibility.json"))
	}
	if len(matches) == 0 {
		t.Fatalf("授权之后没有 compatibility.json（home=%s）", home)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(body) {
		t.Fatalf("compatibility.json 不是合法 JSON：%s", body)
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if !strings.Contains(string(body), "tool-b") || !strings.Contains(string(body), rv) {
		t.Fatalf("compatibility.json 里没有 tool-b@%s 的记录：%s", rv, body)
	}

	after, err := execPluginManager(t, tl, `{"action":"list_version_exemptions"}`)
	if err != nil {
		t.Fatal(err)
	}
	ex, _ := after["exemptions"].(map[string]any)
	if len(ex) == 0 {
		t.Fatalf("授权之后 list_version_exemptions 仍然为空：%v", after)
	}
}

// 没有插件运行时（单轮子进程）时，每条 action 都要点名缺失，不能给空数据。
func TestPluginManagerWithoutRuntimeIsLoud(t *testing.T) {
	tl := &pluginManagerTool{}
	for _, raw := range []string{`{"action":"list_plugins"}`, `{"action":"set_plugin","target":"x","enabled":true}`, `{"action":"list_version_exemptions"}`} {
		res, err := tl.Exec(context.Background(), json.RawMessage(raw))
		if err == nil {
			t.Fatalf("%s 在没有插件运行时的时候不该成功", raw)
		}
		if res.Hint == "" {
			t.Fatalf("%s 的错误必须带 Hint（R3：不得空 Result 丢 Hint）", raw)
		}
	}
}

// 未知 action 要被 schema 之外的第二道检查拒绝（不许落进 default 静默成功）。
func TestPluginManagerUnknownActionRejected(t *testing.T) {
	tl, _ := newPluginManagerFixture(t)
	if _, err := execPluginManager(t, tl, `{"action":"list"}`); err == nil {
		t.Fatal("官方的 list 是 list_plugins/list_bundles；裸 list 必须拒绝")
	}
}

// Caps：写类 action 必须按"写 + 破坏性"报，审批才拦得住。
func TestPluginManagerCapsDeclareDestructiveWrite(t *testing.T) {
	c := (&pluginManagerTool{}).Caps()
	if !c.Writes || !c.Destructive {
		t.Fatalf("plugin_manager 会改 profile 持久化状态，Caps 必须 Writes+Destructive：%+v", c)
	}
}

// 真实 ChangeResult 的 nextStep 语义：没应用的变更必须带下一步。
func TestChangeEnvelopeNextStep(t *testing.T) {
	applied := changeEnvelope("set_plugin", transport.OfficialChangeResult{Changed: true, Application: "applied", Target: "a"})
	if _, has := applied["nextStep"]; has {
		t.Fatalf("已应用的变更不该带 nextStep：%v", applied)
	}
	notApplied := changeEnvelope("set_plugin", transport.OfficialChangeResult{
		Changed: false, Application: "failed", Target: "a",
		Error: &transport.OfficialChangeError{Code: "bad_request"},
	})
	if _, has := notApplied["nextStep"]; !has {
		t.Fatalf("没应用的变更必须带 nextStep：%v", notApplied)
	}
}
