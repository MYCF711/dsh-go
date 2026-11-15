package creation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeGeneratedPackage 在 dir 里写一份**满足官方判据**的生成包：
// package.json（保留名 + main 入口）、JSON 形式的 cordis.patch.yml、
// 以及已构建的入口文件。overrides 用来逐字段造坏包。
type generatedPackage struct {
	name      string
	version   string
	main      string
	exports   any
	patch     string
	entryPath string
	entryBody string
	client    bool
	extra     map[string]string
}

func defaultGeneratedPackage(name string) generatedPackage {
	return generatedPackage{
		name: name, version: "1.0.0", main: "./index.js",
		patch:     `[{"insert":[{"id":"` + name + `:page","name":"` + name + `"}]}]`,
		entryPath: "index.js", entryBody: "export default {}",
	}
}

func (p generatedPackage) write(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manifest := map[string]any{
		"name": p.name, "version": p.version, "main": p.main,
		"dsh": map[string]any{"bundle": map[string]any{"patch": "./cordis.patch.yml"}},
	}
	if p.exports != nil {
		manifest["exports"] = p.exports
	}
	if p.client {
		manifest["dsh"] = map[string]any{
			"bundle": map[string]any{"patch": "./cordis.patch.yml"},
			"client": map[string]any{},
		}
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("Marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), raw, 0o600); err != nil {
		t.Fatalf("写 package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cordis.patch.yml"), []byte(p.patch), 0o600); err != nil {
		t.Fatalf("写 cordis.patch.yml: %v", err)
	}
	if p.entryPath != "" {
		entry := filepath.Join(dir, filepath.FromSlash(p.entryPath))
		if err := os.MkdirAll(filepath.Dir(entry), 0o700); err != nil {
			t.Fatalf("MkdirAll entry: %v", err)
		}
		if err := os.WriteFile(entry, []byte(p.entryBody), 0o600); err != nil {
			t.Fatalf("写入口: %v", err)
		}
	}
	for name, body := range p.extra {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll extra: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("写 extra %s: %v", name, err)
		}
	}
}

func TestValidateBundleAcceptsGeneratedPackage(t *testing.T) {
	name := BundleName("11111111-2222-4333-8444-555555555555")
	dir := t.TempDir()
	defaultGeneratedPackage(name).write(t, dir)
	if err := ValidateBundle(dir, name); err != nil {
		t.Fatalf("合法生成包应通过校验，实际: %v", err)
	}
}

// 官方 validateGeneratedBundle 的逐条判据都必须真的拒绝，而不是"差不多就行"。
func TestValidateBundleRejectsBrokenPackages(t *testing.T) {
	name := BundleName("11111111-2222-4333-8444-555555555555")
	cases := []struct {
		label   string
		mutate  func(p *generatedPackage)
		wantSub string
	}{
		{
			label:   "包名不是保留名",
			mutate:  func(p *generatedPackage) { p.name = "other-plugin" },
			wantSub: "必须是宿主保留名",
		},
		{
			label:   "版本为空",
			mutate:  func(p *generatedPackage) { p.version = "  " },
			wantSub: "version 不能为空",
		},
		{
			label: "补丁行 id 不属于本包",
			mutate: func(p *generatedPackage) {
				p.patch = `[{"insert":[{"id":"acme.other:page","name":"` + name + `"}]}]`
			},
			wantSub: "必须以",
		},
		{
			label: "补丁行 name 来自别的包",
			mutate: func(p *generatedPackage) {
				p.patch = `[{"insert":[{"id":"` + name + `:page","name":"acme.other"}]}]`
			},
			wantSub: "必须是本包",
		},
		{
			label: "补丁行重复 id",
			mutate: func(p *generatedPackage) {
				p.patch = `[{"insert":[{"id":"` + name + `:page","name":"` + name + `"},` +
					`{"id":"` + name + `:page","name":"` + name + `"}]}]`
			},
			wantSub: "重复",
		},
		{
			label: "group:true 明确拒绝",
			mutate: func(p *generatedPackage) {
				p.patch = `[{"insert":[{"id":"` + name + `:page","name":"` + name + `","group":true}]}]`
			},
			wantSub: "group",
		},
		{
			label: "补丁是 YAML 专有语法",
			mutate: func(p *generatedPackage) {
				p.patch = "- insert:\n    - id: " + name + ":page\n      name: " + name + "\n"
			},
			wantSub: "JSON 数组",
		},
		{
			label: "入口不是已构建文件",
			mutate: func(p *generatedPackage) {
				p.main = "./index.ts"
				p.entryPath = "index.ts"
			},
			wantSub: "不是 .js/.mjs/.cjs",
		},
		{
			label:   "入口文件不存在",
			mutate:  func(p *generatedPackage) { p.entryPath = "" },
			wantSub: "不可读",
		},
		{
			label: "声明 dsh.client 但没有已构建的 ./client 入口",
			mutate: func(p *generatedPackage) {
				p.client = true
			},
			wantSub: "不是 .js/.mjs/.cjs",
		},
		{
			label: "exports 解析不到入口",
			mutate: func(p *generatedPackage) {
				p.exports = map[string]any{".": "./index.js"}
				p.patch = `[{"insert":[{"id":"` + name + `:client","name":"` + name + `/client"}]}]`
			},
			wantSub: "解析不到入口",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.label, func(t *testing.T) {
			pkg := defaultGeneratedPackage(name)
			testCase.mutate(&pkg)
			dir := t.TempDir()
			pkg.write(t, dir)
			err := ValidateBundle(dir, name)
			if err == nil {
				t.Fatalf("坏生成包必须被拒绝（%s）", testCase.label)
			}
			if !strings.Contains(err.Error(), testCase.wantSub) {
				t.Fatalf("错误信息应包含 %q，实际: %v", testCase.wantSub, err)
			}
		})
	}
}

// 包内 symlink 必须被拒绝：官方允许相对链接，但本仓 pluginmgr 完全禁止包内
// symlink，装进去也过不了——不如在校验阶段就说清楚。
func TestValidateBundleRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建 symlink 需要额外权限；该判据由 validateTree 的 Lstat 分支保证")
	}
	name := BundleName("11111111-2222-4333-8444-555555555555")
	dir := t.TempDir()
	defaultGeneratedPackage(name).write(t, dir)
	if err := os.Symlink("index.js", filepath.Join(dir, "alias.js")); err != nil {
		t.Skipf("无法创建 symlink: %v", err)
	}
	err := ValidateBundle(dir, name)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink 必须被拒绝，实际: %v", err)
	}
}

func TestValidateBundleRejectsEscapingPath(t *testing.T) {
	name := BundleName("11111111-2222-4333-8444-555555555555")
	pkg := defaultGeneratedPackage(name)
	pkg.main = "../outside.js"
	dir := t.TempDir()
	pkg.write(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "..", "outside.js"), []byte("x"), 0o600); err != nil {
		t.Fatalf("写外部文件: %v", err)
	}
	err := ValidateBundle(dir, name)
	if err == nil || !strings.Contains(err.Error(), "逃出") {
		t.Fatalf("包内路径逃逸必须被拒绝，实际: %v", err)
	}
}

// CopyBundle 必须真的把整棵树复制过去，且拒绝覆盖已存在的目标（官方 errorOnExist）。
func TestCopyBundleRealCopyAndNoOverwrite(t *testing.T) {
	name := BundleName("11111111-2222-4333-8444-555555555555")
	source := t.TempDir()
	pkg := defaultGeneratedPackage(name)
	pkg.extra = map[string]string{"lib/panel.js": "export const panel = 1"}
	pkg.write(t, source)

	destination := filepath.Join(t.TempDir(), "created-plugins", "11111111-2222-4333-8444-555555555555")
	if err := CopyBundle(source, destination); err != nil {
		t.Fatalf("CopyBundle: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(destination, "package.json"))
	if err != nil {
		t.Fatalf("复制件缺 package.json: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.Name != name {
		t.Fatalf("复制件 manifest = %+v, %v", manifest, err)
	}
	body, err := os.ReadFile(filepath.Join(destination, "lib", "panel.js"))
	if err != nil || !strings.Contains(string(body), "panel") {
		t.Fatalf("子目录没有复制过去: %v %q", err, body)
	}
	if err := ValidateBundle(destination, name); err != nil {
		t.Fatalf("复制件应通过同一套校验: %v", err)
	}
	if err := CopyBundle(source, destination); err == nil {
		t.Fatal("目标已存在时必须拒绝覆盖（官方 errorOnExist:true）")
	}
}
