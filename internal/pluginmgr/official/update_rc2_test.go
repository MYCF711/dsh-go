package official_test

// 官方 0.2.2-rc.2 插件管理面增量的**真实磁盘**测试（红先写）。
//
// 权威源（随包 0.2.2-rc.2）：
//   - guide `docs/upgrade-guide/v0.2.2-rc.1/plugin-update-method/guide.md`：
//     `updateBundle` 也接受 `update: 'git'` 的 bundle（`options.spec` 指向同仓库
//     的更新 commit）；`InstallBundleOptions.expected` 不匹配 → `unexpected-package`。
//   - `src/index.ts:738-800`（updateBundle 的真实顺序：updateRefusal → 版本必须
//     更新（semver.valid + gt）→ git 走 planGitUpdate / registry 走 checkRegistryUpdate）；
//   - `src/index.ts:809-823`（planGitUpdate：`options.spec` 必须是**完整 commit** 且
//     origin 与已装副本相同，否则 `invalid-spec`）；
//   - `src/operations.ts:515,520,543`（expected 不匹配 → `unexpected-package`，
//     且安装被回滚）；
//   - `src/install-spec.ts:108-128`（pinnedGitOrigin）。
//
// 这里的状态全部是真实文件：临时 home 下的 `state.json`、真实包目录、真实校验。
// 没有网络、没有 git 客户端，所以"校验通过但拿不到 git 内容"必须如实回四字段 501，
// 不能假装更新成功。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/pluginmgr/official"
	"dshgo/internal/transport"
)

const updateSHA40 = "0123456789abcdef0123456789abcdef01234567"

// newUpdateFixture 与 newFixture 相同，但额外挂上**真实** ListBundles 投影
// （生产装配点是 cmd/dshd/serve_plugins_official.go:41 的 officialPluginBundles；
// 这里镜像同一份字段，好让 listBundles 的 update 判据走真实 Manager 状态）。
func newUpdateFixture(t *testing.T, home string) *fixture {
	t.Helper()
	f := newFixture(t, home)
	api := official.New(f.manager)
	api.ListBundles = func() ([]transport.OfficialBundle, error) {
		return bundleRows(t, f.manager), nil
	}
	f.server.AttachOfficialPluginManagerAPI(api)
	return f
}

// bundleRows 镜像 cmd/dshd/serve_plugins_official.go:152-177 的真实投影。
func bundleRows(t *testing.T, manager *pluginmgr.Manager) []transport.OfficialBundle {
	t.Helper()
	states, err := manager.Bundles()
	if err != nil {
		t.Fatalf("Manager.Bundles: %v", err)
	}
	out := make([]transport.OfficialBundle, 0, len(states))
	for _, state := range states {
		out = append(out, transport.OfficialBundle{
			Name:         state.Name,
			Official:     false,
			Availability: "profile",
			Version:      state.Version,
			Enabled:      state.Enabled,
			Installed:    true,
			Source:       state.Source,
			Optional:     false,
			Removable:    true,
			Rows:         []transport.OfficialBundleRow{},
			Overrides:    []string{},
		})
	}
	return out
}

// writeStateFile 写一份**真实格式**的 state.json（与 Manager 落盘的 schema 一致），
// 用来造出"已装包来自某个 spec"的现场——不伪造任何运行时状态。
func writeStateFile(t *testing.T, root, id, version, source string, enabled bool) {
	t.Helper()
	state := map[string]any{
		"schema": "dsh.local-plugin-state@1",
		"plugins": map[string]any{
			id: map[string]any{
				"id": id, "version": version, "enabled": enabled,
				"versions": map[string]any{version: map[string]any{"source": source}},
			},
		},
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// craftInstalled 造一个"已安装 + 来源是给定 spec"的真实现场：
// 包目录走 packageFixture（真实 manifest 与 hash），state.json 记录来源。
func craftInstalled(t *testing.T, home, id, version, source string) *fixture {
	t.Helper()
	root := filepath.Join(home, "plugins")
	packageFixture(t, filepath.Join(root, "packages", id, version), id, version, nil)
	writeStateFile(t, root, id, version, source, false)
	return newUpdateFixture(t, home)
}

// bundleRow 从 listBundles 的结果里取一行。
func bundleRow(t *testing.T, f *fixture, name string) map[string]any {
	t.Helper()
	code, out := f.call(t, "pluginManager.listBundles", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("listBundles 状态 %d：%v", code, out)
	}
	rows, ok := out["result"].([]any)
	if !ok {
		t.Fatalf("listBundles 结果不是数组：%v", out)
	}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if row["name"] == name {
			return row
		}
	}
	t.Fatalf("listBundles 里没有 %q：%v", name, rows)
	return nil
}

// TestListBundlesUpdateFollowsRealSource 钉住"update 由真实来源算出来"：
// 本地目录装的包换不了（缺省 update），完整 commit 钉住的 git 包是 git，
// 下载缓存里的包（registry/tarball 装的）是 registry。
func TestListBundlesUpdateFollowsRealSource(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("DSHD_PLUGIN_CACHE", cache)

	home := t.TempDir()
	root := filepath.Join(home, "plugins")
	// 1) 本地目录装（本仓最常用的形态）→ update 缺省。
	// 已安装包目录必须在 packages/<id>/<version>（Manager 校验它），
	// 而 state.json 记的来源是用户当初给的本地目录。
	local := packageFixture(t, filepath.Join(home, "private", "v1"), "acme.local", "1.0.0", nil)
	packageFixture(t, filepath.Join(root, "packages", "acme.local", "1.0.0"), "acme.local", "1.0.0", nil)
	// 2) git 完整 commit 钉住 → update=git。
	packageFixture(t, filepath.Join(root, "packages", "acme.git", "1.0.0"), "acme.git", "1.0.0", nil)
	// 3) 下载缓存里的包 → update=registry。
	packageFixture(t, filepath.Join(root, "packages", "acme.registry", "1.0.0"), "acme.registry", "1.0.0", nil)

	state := map[string]any{
		"schema": "dsh.local-plugin-state@1",
		"plugins": map[string]any{
			"acme.local": map[string]any{
				"id": "acme.local", "version": "1.0.0", "enabled": false,
				"versions": map[string]any{"1.0.0": map[string]any{"source": local}},
			},
			"acme.git": map[string]any{
				"id": "acme.git", "version": "1.0.0", "enabled": false,
				"versions": map[string]any{"1.0.0": map[string]any{
					"source": "github:acme/widget#" + updateSHA40,
				}},
			},
			"acme.registry": map[string]any{
				"id": "acme.registry", "version": "1.0.0", "enabled": false,
				"versions": map[string]any{"1.0.0": map[string]any{
					"source": filepath.Join(cache, "acme.registry", "1.0.0"),
				}},
			},
		},
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newUpdateFixture(t, home)

	if row := bundleRow(t, f, "acme.local"); row["update"] != nil {
		t.Fatalf("本地目录包的 update 必须缺省，实际 %v", row["update"])
	}
	if row := bundleRow(t, f, "acme.local"); row["updatable"] != false {
		t.Fatalf("本地目录包的相容字段 updatable 必须是 false，实际 %v", row["updatable"])
	}

	gitRow := bundleRow(t, f, "acme.git")
	if gitRow["update"] != "git" {
		t.Fatalf("完整 commit 钉住的包 update 应为 git，实际 %v", gitRow["update"])
	}
	if gitRow["updatable"] != false {
		t.Fatalf("update=git 的包 updatable 保持 false（官方映射），实际 %v", gitRow["updatable"])
	}

	registryRow := bundleRow(t, f, "acme.registry")
	if registryRow["update"] != "registry" {
		t.Fatalf("下载缓存里的包 update 应为 registry，实际 %v", registryRow["update"])
	}
	if registryRow["updatable"] != true {
		t.Fatalf("update=registry ⇔ updatable:true（官方 guide），实际 %v", registryRow["updatable"])
	}
}

// TestInstallBundleExpectedMismatchRollsBack 钉住 rc.2 的 `unexpected-package`：
// expected 与实际装的包/版本不符 → 本次安装被回滚，结果是 failed + 该错误码。
func TestInstallBundleExpectedMismatchRollsBack(t *testing.T) {
	f := newUpdateFixture(t, "")
	pkg := packageFixture(t, filepath.Join(f.home, "private", "v1"), "acme.audit", "1.0.0", nil)

	result := f.result(t, "pluginManager.installBundle", map[string]any{
		"spec":     pkg,
		"expected": map[string]any{"name": "acme.audit", "version": "9.9.9"},
	})
	if result["application"] != "failed" {
		t.Fatalf("版本不符必须是 failed，实际 %v", result["application"])
	}
	errObj, ok := result["error"].(map[string]any)
	if !ok {
		t.Fatalf("failed 结果必须带 error：%v", result)
	}
	if errObj["code"] != "unexpected-package" {
		t.Fatalf("error.code 应为 unexpected-package，实际 %v", errObj["code"])
	}
	if diagnostic, _ := errObj["diagnostic"].(string); !strings.Contains(diagnostic, "9.9.9") {
		t.Fatalf("diagnostic 必须点名期望的版本，实际 %q", diagnostic)
	}
	// 回滚是真实的：状态里没有这个包，包目录也不在。
	if _, installed, err := f.manager.Bundle("acme.audit"); err != nil {
		t.Fatalf("Bundle: %v", err)
	} else if installed {
		t.Fatal("expected 校验失败后包仍然装着：没有回滚")
	}

	// 包名不符同样是 unexpected-package（官方：装的不是期望的那个包）。
	f2 := newUpdateFixture(t, "")
	pkg2 := packageFixture(t, filepath.Join(f2.home, "private", "v2"), "acme.audit", "1.0.0", nil)
	result = f2.result(t, "pluginManager.installBundle", map[string]any{
		"spec":     pkg2,
		"expected": map[string]any{"name": "other.pkg", "version": "1.0.0"},
	})
	if errObj, ok := result["error"].(map[string]any); !ok || errObj["code"] != "unexpected-package" {
		t.Fatalf("包名不符应为 unexpected-package：%v", result)
	}
	if _, installed, err := f2.manager.Bundle("acme.audit"); err != nil {
		t.Fatalf("Bundle: %v", err)
	} else if installed {
		t.Fatal("包名不符后包仍然装着：没有回滚")
	}

	// expected 与实际一致 → 正常 applied（这条路径就是 updateBundle 内部用的那条）。
	f3 := newUpdateFixture(t, "")
	pkg3 := packageFixture(t, filepath.Join(f3.home, "private", "v3"), "acme.audit", "1.2.3", nil)
	result = f3.result(t, "pluginManager.installBundle", map[string]any{
		"spec":     pkg3,
		"expected": map[string]any{"name": "acme.audit", "version": "1.2.3"},
	})
	if result["application"] != "applied" {
		t.Fatalf("expected 一致时应 applied，实际 %v", result)
	}
	if _, installed, err := f3.manager.Bundle("acme.audit"); err != nil {
		t.Fatalf("Bundle: %v", err)
	} else if !installed {
		t.Fatal("expected 一致却回滚了")
	}
}

// TestUpdateBundleRegistryRowRefusesSpec 钉住官方 checkRegistryUpdate 的判据：
// registry 来源的 bundle 只能更新到 `name@version`，给了 `options.spec` 就是
// `invalid-spec`，而且**什么都不能改变**。
func TestUpdateBundleRegistryRowRefusesSpec(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("DSHD_PLUGIN_CACHE", cache)
	home := t.TempDir()
	root := filepath.Join(home, "plugins")
	// registry/tarball 装的包在 state.json 里记的是下载缓存目录。
	source := filepath.Join(cache, "acme.registry", "1.0.0")
	packageFixture(t, filepath.Join(root, "packages", "acme.registry", "1.0.0"), "acme.registry", "1.0.0", nil)
	writeStateFile(t, root, "acme.registry", "1.0.0", source, false)
	f := newUpdateFixture(t, home)

	result := f.result(t, "pluginManager.updateBundle", map[string]any{
		"name": "acme.registry", "version": "2.0.0",
		"options": map[string]any{"spec": "github:acme/widget#" + updateSHA40},
	})
	if result["step"] != "check" {
		t.Fatalf("被拒的更新 step 应为 check，实际 %v", result["step"])
	}
	inner, ok := result["result"].(map[string]any)
	if !ok {
		t.Fatalf("updateBundle 结果必须带 result：%v", result)
	}
	errObj, ok := inner["error"].(map[string]any)
	if !ok || errObj["code"] != "invalid-spec" {
		t.Fatalf("registry 行给 spec 应为 invalid-spec：%v", result)
	}
	if diagnostic, _ := errObj["diagnostic"].(string); !strings.Contains(diagnostic, "acme.registry@2.0.0") {
		t.Fatalf("diagnostic 必须点名它只能更新到哪个 spec，实际 %q", diagnostic)
	}
	if state, _, err := f.manager.Bundle("acme.registry"); err != nil {
		t.Fatalf("Bundle: %v", err)
	} else if state.Version != "1.0.0" {
		t.Fatalf("被拒之后版本被改了：%s", state.Version)
	}
}

// TestUpdateBundleGitRowRequiresPinnedSameOrigin 钉住官方 planGitUpdate 的四条判据。
func TestUpdateBundleGitRowRequiresPinnedSameOrigin(t *testing.T) {
	origin := "github:acme/widget#" + updateSHA40
	f := craftInstalled(t, t.TempDir(), "acme.git", "1.0.0", origin)

	// 1) 不给 spec → invalid-spec（git 行只接受钉住完整 commit 的 git spec）。
	result := f.result(t, "pluginManager.updateBundle", map[string]any{
		"name": "acme.git", "version": "2.0.0",
	})
	assertUpdateRefusal(t, result, "invalid-spec", "full commit")

	// 2) 短 commit（不是完整 commit）→ invalid-spec。
	result = f.result(t, "pluginManager.updateBundle", map[string]any{
		"name": "acme.git", "version": "2.0.0", "spec": "github:acme/widget#abc1234",
	})
	assertUpdateRefusal(t, result, "invalid-spec", "full commit")

	// 3) 完整 commit 但**别的仓库** → invalid-spec，且 diagnostic 点名 origin。
	result = f.result(t, "pluginManager.updateBundle", map[string]any{
		"name": "acme.git", "version": "2.0.0", "spec": "github:other/widget#" + updateSHA40,
	})
	assertUpdateRefusal(t, result, "invalid-spec", "github.com/acme/widget#")

	// 4) 版本必须比已装的更新（官方先查版本、再进 git plan）→ invalid-spec。
	result = f.result(t, "pluginManager.updateBundle", map[string]any{
		"name": "acme.git", "version": "1.0.0", "spec": origin,
	})
	assertUpdateRefusal(t, result, "invalid-spec", "1.0.0")

	// 5) 同仓库 + 新版本通过契约校验之后：本仓没有 git 获取器，
	//    必须如实回四字段 501（点名叫缺 git），且不许改任何状态。
	f.fourField(t, "pluginManager.updateBundle", map[string]any{
		"name": "acme.git", "version": "2.0.0", "spec": "github:acme/widget#abcabcabcabcabcabcabcabcabcabcabcabcabca",
	}, http.StatusNotImplemented, "not_implemented")
	if state, _, err := f.manager.Bundle("acme.git"); err != nil {
		t.Fatalf("Bundle: %v", err)
	} else if state.Version != "1.0.0" {
		t.Fatalf("被拒的 git 更新改了版本：%s", state.Version)
	}
}

// assertUpdateRefusal 断言"管理性拒绝"的形状：step=check + failed + 指定错误码，
// 并且 diagnostic 含给定片段（官方把拒绝理由写在 diagnostic 里给用户看）。
func assertUpdateRefusal(t *testing.T, result map[string]any, code, fragment string) {
	t.Helper()
	if result["step"] != "check" {
		t.Fatalf("拒绝时 step 应为 check，实际 %v（%v）", result["step"], result)
	}
	inner, ok := result["result"].(map[string]any)
	if !ok {
		t.Fatalf("updateBundle 结果缺 result：%v", result)
	}
	if inner["application"] != "failed" {
		t.Fatalf("拒绝时 application 应为 failed，实际 %v", inner["application"])
	}
	errObj, ok := inner["error"].(map[string]any)
	if !ok {
		t.Fatalf("拒绝时必须带 error：%v", inner)
	}
	if errObj["code"] != code {
		t.Fatalf("错误码应为 %q，实际 %v", code, errObj["code"])
	}
	if diagnostic, _ := errObj["diagnostic"].(string); !strings.Contains(diagnostic, fragment) {
		t.Fatalf("diagnostic 应含 %q，实际 %q", fragment, diagnostic)
	}
}
