package main

// 官方 #10 `typert-loader` 的**接线验收**。
//
// 补这一层之前 `Registry.RegisterPackage` 有实现、有语义、**没有任何生产调用者**
// ——挂载包描述的注入层等于没接。所以这里的判据全部是"真的接上了没有"：
//
//  1. 包目录里放了 `typert.json` → 装配点真的把它注进了**那份**注册表，
//     并且 `ValidateParams` 立刻按它生效（不是"注册表里多了一条没人用的记录"）；
//  2. 没有 `typert.json` 的包被**静默跳过**（官方："Packages without the export
//     are skipped"），不是报错；
//  3. 显式清单（`DSHD_TYPERT_PACKAGES`，官方 config `packages`）给错必须
//     **fail-loud 且点名包**（官方原话）；
//  4. 包被卸载 → 它的描述**真的从注册表消失**（官方："withdrawn when the entry
//     unmounts"）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

// writeTypertPackage 造一个真的能被 pluginmgr 安装、且带 typert.json 的包。
func writeTypertPackage(t *testing.T, dir, id, version string, withDescriptor bool) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("payload for " + id + "@" + version)
	sum := sha256Hex(payload)
	manifest := map[string]any{
		"schema": "dsh.local-plugin@1", "id": id, "version": version,
		"kind": "wasm", "entry": "plugin.wasm",
		"files": map[string]string{"plugin.wasm": sum},
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if withDescriptor {
		descriptor := `{
  "schema": "dsh.typert-package@1",
  "name": "` + id + `",
  "version": "` + version + `",
  "methods": [
    {"method": "` + id + `.echo", "summary": "回显", "params": [{"name": "text", "kind": "string", "required": true}]}
  ]
}`
		if err := os.WriteFile(filepath.Join(dir, transport.TypertPackageFileName), []byte(descriptor), 0o644); err != nil {
			t.Fatal(err)
		}
		// 包目录是**哈希登记**的：多一个没登记的文件，安装会直接拒绝。
		// 所以描述文件必须自己进 manifest.files——这条夹具顺带证明了
		// "typert.json 是和包一起被校验的"，不是安装之后谁都能往里丢的。
		manifest["files"].(map[string]string)[transport.TypertPackageFileName] = sha256Hex([]byte(descriptor))
		rewritten, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), rewritten, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// installTypertFixture 造一个 home，装两个包：一个带 typert.json、一个不带。
func installTypertFixture(t *testing.T) (*pluginmgr.Manager, string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "plugins")
	withDesc := writeTypertPackage(t, filepath.Join(home, "src", "withdesc"), "acme.typert", "1.0.0", true)
	without := writeTypertPackage(t, filepath.Join(home, "src", "nodesc"), "acme.plain", "1.0.0", false)

	m, err := pluginmgr.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{withDesc, without} {
		if _, err := m.Install(pkg); err != nil {
			t.Fatalf("装夹具包失败：%v", err)
		}
	}
	return m, root
}

// 自动发现：包目录里的 typert.json 真的注进了注册表，且参数校验立刻按它生效。
func TestTypertLoaderDiscoversInstalledPackages(t *testing.T) {
	m, root := installTypertFixture(t)
	registry := transport.NewRegistry()
	// 先放一条 canonical 描述：包注入不该覆盖它。
	if err := registry.Register(transport.Descriptor{Method: "session.list", Summary: "canonical", Source: "canonical"}); err != nil {
		t.Fatal(err)
	}

	result, err := loadTypertPackagesInto(registry, root, m, "")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("不该有失败（没有 typert.json 的包是静默跳过）：%v", result.Failed)
	}
	if got := result.Loaded["acme.typert"]; got != 1 {
		t.Fatalf("acme.typert 注入了 %d 条，want 1（Loaded=%v）", got, result.Loaded)
	}
	// 没有 typert.json 的包必须出现在 Skipped 里（"静默"= 不是错误，但也不是成功）。
	if !hasExact(result.Skipped, "acme.plain") {
		t.Fatalf("没有 typert.json 的 acme.plain 应当被跳过并记录，实际 Skipped=%v", result.Skipped)
	}

	// 关键判据：注册表里真的有这条，而且 ValidateParams 按它生效。
	descriptor, ok := registry.Lookup("acme.typert.echo")
	if !ok {
		t.Fatal("注册表里没有 acme.typert.echo —— 包描述没有真的注进去")
	}
	if descriptor.Source != "package:acme.typert" {
		t.Fatalf("来源标注不对：%q", descriptor.Source)
	}
	if err := registry.ValidateParams("acme.typert.echo", map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("合法参数被拒：%v", err)
	}
	if err := registry.ValidateParams("acme.typert.echo", map[string]any{}); err == nil {
		t.Fatal("缺必填参数 text 却通过了校验 —— 说明校验没走这份描述符")
	}
	// canonical 那条没被动。
	if _, ok := registry.Lookup("session.list"); !ok {
		t.Fatal("包注入不该删掉 canonical 描述")
	}
}

// 卸载即撤：包从 profile 里去掉之后，它的描述必须从注册表消失。
func TestTypertLoaderWithdrawsOnUninstall(t *testing.T) {
	m, root := installTypertFixture(t)
	registry := transport.NewRegistry()
	if _, err := loadTypertPackagesInto(registry, root, m, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Lookup("acme.typert.echo"); !ok {
		t.Fatal("前置条件不成立：没注进去")
	}

	if err := m.Uninstall("acme.typert"); err != nil {
		t.Fatalf("卸载失败：%v", err)
	}
	// 重扫：包没了，还留在表里的那条必须被撤掉。
	if _, err := loadTypertPackagesInto(registry, root, m, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Lookup("acme.typert.echo"); ok {
		t.Fatal("包已经卸载了，它的描述还在注册表里（官方：registration follows the entry lifecycle）")
	}
	// 直接调 WithdrawPackage 也要能撤（宿主在卸载路径上用的是这个）。
	if left := registry.WithdrawPackage("acme.typert"); left != 0 {
		t.Fatalf("再撤一次应当撤到 0 条，实际 %d", left)
	}
}

// 显式清单给错：fail-loud 且点名包（官方原话）。
func TestTypertLoaderExplicitEntryFailsLoud(t *testing.T) {
	m, root := installTypertFixture(t)

	for _, raw := range []string{
		"acme.missing=" + filepath.Join(t.TempDir(), "nope.json"),
		"acme.typert=" + filepath.Join(t.TempDir(), "nope.json"), // 已装包但显式路径错
	} {
		registry := transport.NewRegistry()
		result, err := loadTypertPackagesInto(registry, root, m, raw)
		if err != nil {
			t.Fatalf("解析显式清单不该在这里失败：%v", err)
		}
		if len(result.Failed) == 0 {
			t.Fatalf("显式条目 %q 读不到却没报错", raw)
		}
		if err := result.Err(); err == nil {
			t.Fatal("result.Err() 必须把失败折成错误")
		}
	}

	// 形状错误（不是 name=path）在解析期就 fail-loud。
	if _, err := typertPackageOverrides("acme.typert"); err == nil {
		t.Fatal("不是 name=path 形状必须报错")
	}
	if _, err := typertPackageOverrides("=/abs/x.json"); err == nil {
		t.Fatal("缺包名必须报错")
	}
}

// 显式清单能注入**不在已安装集合里**的包（官方：nested plugins 用显式清单覆盖）。
func TestTypertLoaderExplicitEntryLoadsNestedPackage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "typert.json")
	descriptor := `{
  "schema": "dsh.typert-package@1",
  "name": "nested.pkg",
  "methods": [{"method": "nested.pkg.ping", "summary": "探活"}]
}`
	if err := os.WriteFile(path, []byte(descriptor), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := transport.NewRegistry()
	result, err := loadTypertPackagesInto(registry, dir, nil, "nested.pkg="+path)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("不该失败：%v", result.Failed)
	}
	if _, ok := registry.Lookup("nested.pkg.ping"); !ok {
		t.Fatal("显式清单里的包没有注入")
	}
}

// 没有注册表：如实报告，不静默建一份孤立的表。
func TestTypertLoaderWithoutRegistryIsLoud(t *testing.T) {
	m, root := installTypertFixture(t)
	result, err := loadTypertPackagesInto(nil, root, m, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) == 0 || !strings.Contains(strings.Join(result.Failed, " "), "注册表") {
		t.Fatalf("没有注册表时必须点名缺什么，实际 %v", result.Failed)
	}
}

func hasExact(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
