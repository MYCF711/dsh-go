package main

// 官方 #1 `tool-plugin-manager` 的验收测试。
//
// 判据不是"函数存在"，而是**这条调用真的走到了真实的插件管理器上**：
//   - `list_plugins` 的分页信封里的行，与本进程 `pluginRuntime` 的真实装载集合一致；
//   - `set_plugin` 真的改了 state.json 里的启用位（重开管理器仍然读得到）；
//   - `set_version_exemption` 真的写进了 compatibility.json；
//   - `install_bundle` 收**本地目录包**时走真实安装事务，装完 `pluginInventory.list`
//     （设置页「内置插件」的数据源）看得到；`remove_bundle` 卸完它消失；
//   - 网络来源（registry/git/tarball）与装不了的包回**官方形状的结构化失败**
//     （ChangeResult + error.code + nextStep），不是假成功也不是异常。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// 网络来源（registry / git / tarball）在**工具面**上仍然装不了：官方形状失败 + nextStep。
//
// 这条边界是显式的产品决策（工具面不引入网络下载），所以必须钉死"说清为什么"：
// 只回一个 unsupported-platform 而不说"本仓只支持本地目录包"，模型会去改参数重试。
func TestPluginManagerInstallRejectsNetworkSources(t *testing.T) {
	tl, home := newPluginManagerFixture(t)

	specs := []string{
		"@deepseek-ai/dsh-skill@0.2.2",       // 作用域包名 = registry 规格
		"acme-bundle",                        // 裸包名 = registry 规格
		"https://example.com/acme-1.0.0.tgz", // tarball URL
		"git+https://example.com/acme.git",   // git 规格
	}
	for _, spec := range specs {
		out, err := execPluginManager(t, tl, fmt.Sprintf(`{"action":"install_bundle","target":%q}`, spec))
		if err != nil {
			t.Fatalf("%s 不该抛错（官方用 ChangeResult 表达应用性失败）：%v", spec, err)
		}
		if changed, _ := out["changed"].(bool); changed {
			t.Fatalf("%s 报 changed=true —— 这是假成功", spec)
		}
		if app, _ := out["application"].(string); app != "failed" {
			t.Fatalf("%s 的 application 应当是 failed，实际 %v", spec, out["application"])
		}
		// 官方 ChangeResult.stage 只有 install|enable|remove 三值。
		if stage, _ := out["stage"].(string); stage != "install" {
			t.Fatalf("%s 的 stage 必须是官方的 install（不是 resolve），实际 %v", spec, out["stage"])
		}
		if target, _ := out["target"].(string); target != spec {
			t.Fatalf("%s 的 target 必须回显这次 spec，实际 %v", spec, out["target"])
		}
		e, ok := out["error"].(map[string]any)
		if !ok {
			t.Fatalf("%s 必须带 error 对象：%v", spec, out)
		}
		if code, _ := e["code"].(string); code != "unsupported-platform" {
			t.Fatalf("%s 的 error.code 应当是 unsupported-platform，实际 %v", spec, code)
		}
		if diagnostic, _ := e["diagnostic"].(string); !strings.Contains(diagnostic, "本地目录包") {
			t.Fatalf("%s 的 diagnostic 必须说清只支持本地目录包：%v", spec, e["diagnostic"])
		}
		step, ok := out["nextStep"].(string)
		if !ok {
			t.Fatalf("%s 没给 nextStep（官方 withNextStep：没应用的变更要说清下一步）：%v", spec, out)
		}
		if !strings.Contains(step, "本地目录包") || !strings.Contains(step, "没有下载源") {
			t.Fatalf("%s 的 nextStep 必须说清「只支持本地目录包」与「网络来源没有下载源」，实际 %q", spec, step)
		}
	}

	// 被拒的来源一次都不许碰磁盘：没有已装包，也没有包目录。
	out, err := execPluginManager(t, tl, `{"action":"list_bundles"}`)
	if err != nil {
		t.Fatal(err)
	}
	if total, _ := out["total"].(float64); total != 0 {
		t.Fatalf("被拒绝的网络来源不该留下已装包：%v", out)
	}
	if entries, err := os.ReadDir(filepath.Join(home, "plugins", "packages")); err == nil && len(entries) != 0 {
		t.Fatalf("被拒绝的网络来源不该建包目录：%v", entries)
	}
}

// 端到端：工具装一个**真目录包** -> `pluginInventory.list` 的 entries 里有它
// -> 卸载后消失。
//
// 为什么必须走 `pluginInventory.list` 而不是只看工具自己的 list_bundles：
// 设置页「内置插件」的数据源就是它（宿主装配见
// `cmd/dshd/serve_plugins_official.go:newOfficialPluginManagerAPI` 的 PluginInventoryList）。
// "工具回 changed=true 但设置页永远空着"正是这次要消灭的缺口，所以判据落在出口上。
func TestPluginManagerInstallAndRemoveLocalDirectoryEndToEnd(t *testing.T) {
	home := t.TempDir()
	rt, err := openPluginRuntime(home)
	if err != nil {
		t.Fatalf("打开插件运行时失败：%v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	tl := &pluginManagerTool{api: pluginManagerAPI{newOfficialPluginManagerAPI(rt)}}
	srv := newOfficialPluginManagerTestServer(t, rt)

	// 真目录包：manifest + 真 wasm payload（与装载器测试同一份客人产物）。
	pkg := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "acme"), "1.0.0")

	// 0) 装之前设置页的数据源是空的——后面看到的是这次装出来的，不是巧合。
	if entries := pluginInventoryEntries(t, srv); len(entries) != 0 {
		t.Fatalf("装之前 pluginInventory.list 应为空：%v", entries)
	}

	// 1) 安装：真事务（staging 复制 + 校验 + state.json 提交）。
	out, err := execPluginManager(t, tl, fmt.Sprintf(`{"action":"install_bundle","target":%q}`, pkg))
	if err != nil {
		t.Fatalf("本地目录包安装失败：%v", err)
	}
	if changed, _ := out["changed"].(bool); !changed {
		t.Fatalf("安装没报 changed=true：%v", out)
	}
	if app, _ := out["application"].(string); app != "applied" {
		t.Fatalf("安装的 application 应当是 applied：%v", out)
	}
	if out["bundle"] != fixturePluginID || out["version"] != "1.0.0" {
		t.Fatalf("结果必须点名装上的包与版本（官方 bundle/version）：%v", out)
	}
	if _, has := out["nextStep"]; has {
		t.Fatalf("已应用的变更不该带 nextStep（官方 withNextStep）：%v", out)
	}

	// 磁盘真相：state.json 记了它、包目录真的落地（不是只回了个 changed=true）。
	stateRaw, err := os.ReadFile(filepath.Join(home, "plugins", "state.json"))
	if err != nil {
		t.Fatalf("安装必须真写 state.json：%v", err)
	}
	var state struct {
		Plugins map[string]struct {
			Version  string `json:"version"`
			Enabled  bool   `json:"enabled"`
			Versions map[string]struct {
				Source string `json:"source"`
			} `json:"versions"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		t.Fatalf("state.json 不是合法 JSON：%v（%s）", err, stateRaw)
	}
	record, ok := state.Plugins[fixturePluginID]
	if !ok {
		t.Fatalf("state.json 里没有 %s：%s", fixturePluginID, stateRaw)
	}
	if record.Version != "1.0.0" || !record.Enabled {
		t.Fatalf("state.json 记的版本/启用位不对：%+v", record)
	}
	if source := record.Versions["1.0.0"].Source; source != pkg {
		t.Fatalf("state.json 记的来源应为真实绝对目录 %q，实际 %q", pkg, source)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "packages", fixturePluginID, "1.0.0", "plugin.json")); err != nil {
		t.Fatalf("安装后的包目录不存在：%v", err)
	}

	// 2) 设置页的数据源看得到它（entryId/enabled/fiberPhase 都是真实值）。
	entries := pluginInventoryEntries(t, srv)
	if len(entries) != 1 {
		t.Fatalf("pluginInventory.list 应看到刚装的 1 个包，实际 %d 行：%v", len(entries), entries)
	}
	entry := entries[0]
	if entry["entryId"] != fixturePluginID {
		t.Fatalf("entryId 不对：%v", entry)
	}
	if entry["moduleName"] != "plugin.wasm" {
		t.Fatalf("moduleName 必须是真实 entry：%v", entry)
	}
	if entry["enabled"] != true {
		t.Fatalf("安装默认启用（官方 defaults to true），enabled 应为 true：%v", entry)
	}
	if entry["fiberPhase"] != "active" {
		t.Fatalf("启用安装必须真的装载到当前进程（fiberPhase=active）：%v", entry)
	}

	// 3) 卸载：真事务（包目录进事务区 + state.json 摘记录）。
	out, err = execPluginManager(t, tl, fmt.Sprintf(`{"action":"remove_bundle","target":%q}`, fixturePluginID))
	if err != nil {
		t.Fatalf("卸载失败：%v", err)
	}
	if changed, _ := out["changed"].(bool); !changed {
		t.Fatalf("卸载没报 changed=true：%v", out)
	}
	if app, _ := out["application"].(string); app != "applied" {
		t.Fatalf("卸载的 application 应当是 applied：%v", out)
	}
	if stage, _ := out["stage"].(string); stage != "remove" {
		t.Fatalf("卸载的 stage 必须是 remove：%v", out)
	}

	// 4) 设置页里消失，磁盘上也回收了。
	if entries := pluginInventoryEntries(t, srv); len(entries) != 0 {
		t.Fatalf("卸载之后 pluginInventory.list 必须为空，实际：%v", entries)
	}
	after, err := os.ReadFile(filepath.Join(home, "plugins", "state.json"))
	if err != nil {
		t.Fatalf("卸载后 state.json 必须仍可读：%v", err)
	}
	if strings.Contains(string(after), fixturePluginID) {
		t.Fatalf("卸载后 state.json 仍记着 %s：%s", fixturePluginID, after)
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "packages", fixturePluginID)); !os.IsNotExist(err) {
		t.Fatalf("卸载后包目录应被回收，实际 err=%v", err)
	}
}

// 装不了的包：**官方形状的结构化失败**并说清原因，且状态一步不动。
//
// 四类失败各有各的真相，不许合并成一句"安装失败"：
//   - 路径不存在   → invalid-spec（读不到包目录）
//   - hash 不符    → invalid-spec（文件 SHA-256 与 manifest 登记值不一致）
//   - manifest 坏  → invalid-spec（不是合法 JSON / schema 不对）
//   - Cordis/JS 包 → unsupported-platform（本仓不嵌 JS 运行时，AGENTS R6）
func TestPluginManagerInstallFailuresStayStructured(t *testing.T) {
	home := t.TempDir()
	rt, err := openPluginRuntime(home)
	if err != nil {
		t.Fatalf("打开插件运行时失败：%v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	tl := &pluginManagerTool{api: pluginManagerAPI{newOfficialPluginManagerAPI(rt)}}
	srv := newOfficialPluginManagerTestServer(t, rt)

	cases := []struct {
		name       string
		build      func(t *testing.T) string
		wantCode   string
		wantInDiag string
	}{
		{
			name:       "路径不存在",
			build:      func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-such-package") },
			wantCode:   "invalid-spec",
			wantInDiag: "读取包目录",
		},
		{
			name:       "hash 不符",
			build:      corruptHashPackage,
			wantCode:   "invalid-spec",
			wantInDiag: "SHA-256 不符",
		},
		{
			name:       "manifest 坏 JSON",
			build:      brokenManifestPackage,
			wantCode:   "invalid-spec",
			wantInDiag: "坏 manifest",
		},
		{
			name:       "Cordis/JS 形态",
			build:      cordisStylePackage,
			wantCode:   "unsupported-platform",
			wantInDiag: "Cordis",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := c.build(t)
			out, err := execPluginManager(t, tl, fmt.Sprintf(`{"action":"install_bundle","target":%q}`, spec))
			if err != nil {
				t.Fatalf("装不了的包不该把工具调用变成异常（官方用 ChangeResult）：%v", err)
			}
			if changed, _ := out["changed"].(bool); changed {
				t.Fatalf("%s 报 changed=true —— 这是假成功：%v", c.name, out)
			}
			if app, _ := out["application"].(string); app != "failed" {
				t.Fatalf("%s 的 application 应当是 failed：%v", c.name, out)
			}
			e, ok := out["error"].(map[string]any)
			if !ok {
				t.Fatalf("%s 必须带 error 对象：%v", c.name, out)
			}
			if code, _ := e["code"].(string); code != c.wantCode {
				t.Fatalf("%s 的 error.code 应当是 %s，实际 %v", c.name, c.wantCode, code)
			}
			diagnostic, _ := e["diagnostic"].(string)
			if !strings.Contains(diagnostic, c.wantInDiag) {
				t.Fatalf("%s 的 diagnostic 必须点名原因（要含 %q），实际 %q", c.name, c.wantInDiag, diagnostic)
			}
			if step, ok := out["nextStep"].(string); !ok || strings.TrimSpace(step) == "" {
				t.Fatalf("%s 没给 nextStep（官方 withNextStep）：%v", c.name, out)
			}
			// 失败必须一步不动：没有已装包，设置页也没有条目。
			list, err := execPluginManager(t, tl, `{"action":"list_bundles"}`)
			if err != nil {
				t.Fatal(err)
			}
			if total, _ := list["total"].(float64); total != 0 {
				t.Fatalf("%s 之后不该有已装包：%v", c.name, list)
			}
			if entries := pluginInventoryEntries(t, srv); len(entries) != 0 {
				t.Fatalf("%s 之后 pluginInventory.list 必须为空：%v", c.name, entries)
			}
		})
	}
}

// 卸载不存在的包：官方码是 not-removable（管理性失败），不是 unsupported-platform。
func TestPluginManagerRemoveMissingBundleIsNotRemovable(t *testing.T) {
	tl, _ := newPluginManagerFixture(t)
	out, err := execPluginManager(t, tl, `{"action":"remove_bundle","target":"acme.absent"}`)
	if err != nil {
		t.Fatalf("没装的包卸载失败必须是 ChangeResult，不是异常：%v", err)
	}
	if changed, _ := out["changed"].(bool); changed {
		t.Fatalf("没装的包卸载报 changed=true —— 这是假成功：%v", out)
	}
	e, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("必须带 error 对象：%v", out)
	}
	if code, _ := e["code"].(string); code != "not-removable" {
		t.Fatalf("官方码是 not-removable，实际 %v", code)
	}
	if step, ok := out["nextStep"].(string); !ok || !strings.Contains(step, "list_bundles") {
		t.Fatalf("not-removable 的 nextStep 必须指向 list_bundles：%v", out)
	}
}

// target 必填：官方两句原文（tools.ts:151/157），缺了不许静默成功。
func TestPluginManagerInstallTargetRequired(t *testing.T) {
	tl, _ := newPluginManagerFixture(t)
	for _, c := range []struct{ raw, want string }{
		{`{"action":"install_bundle"}`, "target package spec is required"},
		{`{"action":"install_bundle","target":"   "}`, "target package spec is required"},
		{`{"action":"remove_bundle"}`, "target bundle name is required"},
		{`{"action":"remove_bundle","target":""}`, "target bundle name is required"},
	} {
		res, err := tl.Exec(context.Background(), json.RawMessage(c.raw))
		if err == nil {
			t.Fatalf("%s 缺 target 必须拒绝", c.raw)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s 的错误文案必须是官方原文 %q，实际 %q", c.raw, c.want, err.Error())
		}
		if res.Hint == "" {
			t.Fatalf("%s 的错误必须带 Hint（R3）：%v", c.raw, res)
		}
	}
}

// 宿主没挂接安装/卸载事务时点名缺什么（不是静默成功、也不是 unsupported-platform）。
func TestPluginManagerInstallUnwiredIsLoud(t *testing.T) {
	// 只挂读方法：Exec 的"整个插件面都没接"预检会放行，从而走到安装/卸载的 nil 分支。
	tl := &pluginManagerTool{api: pluginManagerAPI{transport.OfficialPluginManagerAPI{
		ListPlugins: func() ([]transport.OfficialPluginEntry, error) { return nil, nil },
	}}}
	pkg := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "pkg"), "1.0.0")

	res, err := tl.Exec(context.Background(), json.RawMessage(fmt.Sprintf(`{"action":"install_bundle","target":%q}`, pkg)))
	if err == nil || !strings.Contains(err.Error(), "未接线") {
		t.Fatalf("没有 InstallBundle 时必须点名未接线，实际 err=%v", err)
	}
	if res.Hint == "" {
		t.Fatal("必须带 Hint（R3）")
	}
	res, err = tl.Exec(context.Background(), json.RawMessage(`{"action":"remove_bundle","target":"acme.audit"}`))
	if err == nil || !strings.Contains(err.Error(), "未接线") {
		t.Fatalf("没有 RemoveBundle 时必须点名未接线，实际 err=%v", err)
	}
	if res.Hint == "" {
		t.Fatal("必须带 Hint（R3）")
	}
}

// 本地目录包收到 registry 参数：不静默忽略，如实说它没起作用。
func TestPluginManagerInstallWarnsRegistryUnusedForLocalPath(t *testing.T) {
	home := t.TempDir()
	rt, err := openPluginRuntime(home)
	if err != nil {
		t.Fatalf("打开插件运行时失败：%v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	tl := &pluginManagerTool{api: pluginManagerAPI{newOfficialPluginManagerAPI(rt)}}

	pkg := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "acme"), "1.0.0")
	out, err := execPluginManager(t, tl, fmt.Sprintf(
		`{"action":"install_bundle","target":%q,"registry":"https://registry.example"}`, pkg))
	if err != nil {
		t.Fatalf("本地目录包安装失败：%v", err)
	}
	if changed, _ := out["changed"].(bool); !changed {
		t.Fatalf("安装没报 changed=true：%v", out)
	}
	warnings, _ := out["warnings"].([]any)
	found := false
	for _, warning := range warnings {
		if text, _ := warning.(string); strings.Contains(text, "没有使用 registry") {
			found = true
		}
	}
	if !found {
		t.Fatalf("本地目录包收到 registry 参数时必须如实警告它没被使用：%v", out["warnings"])
	}
}

// corruptHashPackage 造一个"manifest 合法但 payload 被改过"的包。
func corruptHashPackage(t *testing.T) string {
	t.Helper()
	dir := writePluginTransportPackage(t, filepath.Join(t.TempDir(), "bad-hash"), "1.0.0")
	payload, err := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	// 末尾加一个字节：文件内容变了，manifest 里登记的 SHA-256 自然不符。
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), append(payload, 0x00), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// brokenManifestPackage 造一个 plugin.json 不是合法 JSON 的包目录。
func brokenManifestPackage(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bad-manifest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"schema":`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// cordisStylePackage 造一个"另一套生态"的插件目录（package.json + JS 入口，没有 plugin.json）。
func cordisStylePackage(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cordis-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"acme-cordis","version":"1.0.0","main":"index.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("module.exports = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// pluginInventoryEntries 从**真实 transport 出口**读 `pluginInventory.list` 的 entries。
//
// 这是设置页「内置插件」看到的那一份数据：走 server 的 handler、而不是工具的
// list_bundles，才能证明"装进去的包在界面数据源里真的出现/消失"。
func pluginInventoryEntries(t *testing.T, srv *transport.Server) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pluginInventory.list", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	srv.ServeOfficialPluginInventoryAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pluginInventory.list 状态码 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Result struct {
			ManagementAvailable *bool            `json:"managementAvailable"`
			Entries             []map[string]any `json:"entries"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("pluginInventory.list 响应不是 JSON：%s", rec.Body.String())
	}
	// 本仓有持久化 profile 管理，managementAvailable 必须显式为 true（不是缺省）。
	if envelope.Result.ManagementAvailable == nil || !*envelope.Result.ManagementAvailable {
		t.Fatalf("managementAvailable 必须是 true：%s", rec.Body.String())
	}
	return envelope.Result.Entries
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
