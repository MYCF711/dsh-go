package officekit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeEngine 造一个结构合法的引擎包（不真的能跑，只验发现/校验逻辑）。
func writeFakeEngine(t *testing.T, mutate func(manifest map[string]any)) string {
	t.Helper()
	return writeFakeEngineAt(t, t.TempDir(), mutate)
}

// writeFakeEngineAt 在指定包根写一个结构合法的引擎包（给"包在 node_modules 里"的
// 布局用：那种用例必须自己带完整包，不能靠本机已装的真引擎兜底）。
func writeFakeEngineAt(t *testing.T, root string, mutate func(manifest map[string]any)) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "program", "program"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "dsoffice.exe"), []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schemaVersion": 1,
		"version":       "0.2.3",
		"platform":      TargetPlatform(),
		"status":        "built",
		"engine": map[string]any{
			"kind":             "native",
			"executable":       "bin/dsoffice.exe",
			"programDirectory": "program/program",
		},
	}
	if mutate != nil {
		mutate(manifest)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prebuilds.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDiscoverValidEngine(t *testing.T) {
	root := writeFakeEngine(t, nil)
	engine, tried := Discover(DiscoverOptions{SearchRoots: []string{root}})
	if engine == nil {
		t.Fatalf("合法引擎没被发现：tried=%v", tried)
	}
	if engine.Version != "0.2.3" || engine.Platform != TargetPlatform() {
		t.Fatalf("引擎元数据不对：%+v", engine)
	}
	if !strings.HasSuffix(engine.Executable, filepath.Join("bin", "dsoffice.exe")) {
		t.Fatalf("可执行文件路径不对：%s", engine.Executable)
	}
	if !strings.HasSuffix(engine.ProgramDirectory, filepath.Join("program", "program")) {
		t.Fatalf("program 目录不对：%s", engine.ProgramDirectory)
	}
}

// 四种显式写法都必须认：包根、@deepseek-ai 目录、node_modules 根、可执行文件本身。
//
// **夹具必须自成一个完整引擎包，并且把自动发现关掉**（SearchRoots 指向空目录 +
// 清掉 DSHD_OFFICE_KIT_ENGINE）。旧写法只在临时目录里 mkdir 了一层空包目录，
// 四种写法里三种其实解析不出来，靠的是 `Discover` 失败后回落到默认自动发现、
// 在本开发机上扫到了已安装的真引擎才"绿"——CI 的 runner 没有那份安装，
// 于是 2026-11-12 run 43191452874 上红。测试自己假设了本机环境，改的就是这里。
func TestDiscoverExplicitForms(t *testing.T) {
	root := t.TempDir()
	packageRoot := filepath.Join(root, "node_modules", "@deepseek-ai", "libreoffice-kit-"+TargetPlatformSuffix())
	writeFakeEngineAt(t, packageRoot, nil)
	t.Setenv(EngineEnvVar, "")
	noAutoDiscovery := t.TempDir()
	for _, path := range []string{
		packageRoot,
		filepath.Dir(packageRoot), // @deepseek-ai
		root,                      // node_modules 根
		filepath.Join(packageRoot, "bin", "dsoffice.exe"),
	} {
		engine, tried := Discover(DiscoverOptions{Explicit: path, SearchRoots: []string{noAutoDiscovery}})
		if engine == nil {
			t.Fatalf("显式配置 %q 没被认出（自动发现已关掉，不能靠本机安装兜底）：tried=%v", path, tried)
		}
		if !strings.EqualFold(filepath.Clean(engine.Root), filepath.Clean(packageRoot)) {
			t.Fatalf("显式配置 %q 解析到 %q，期望包根 %q", path, engine.Root, packageRoot)
		}
	}
}

func TestDiscoverRejectsBrokenManifests(t *testing.T) {
	cases := map[string]func(map[string]any){
		"schemaVersion": func(m map[string]any) { m["schemaVersion"] = 2 },
		"status":        func(m map[string]any) { m["status"] = "pending" },
		"platform":      func(m map[string]any) { m["platform"] = "darwin-arm64" },
		"kind": func(m map[string]any) {
			m["engine"] = map[string]any{"kind": "wasm", "executable": "bin/dsoffice.exe", "programDirectory": "program/program"}
		},
		"traversal": func(m map[string]any) {
			m["engine"] = map[string]any{"kind": "native", "executable": "../escape.exe", "programDirectory": "program/program"}
		},
		"missing-program": func(m map[string]any) {
			m["engine"] = map[string]any{"kind": "native", "executable": "bin/dsoffice.exe", "programDirectory": "no-such-dir"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeFakeEngine(t, mutate)
			engine, _ := Discover(DiscoverOptions{SearchRoots: []string{root}})
			if engine != nil {
				t.Fatalf("%s 不合法的引擎被接受了：%+v", name, engine)
			}
		})
	}
}

// 没有引擎时必须给“试过哪些路径”，否则排障只能靠猜。
func TestDiscoverReportsTriedPaths(t *testing.T) {
	empty := t.TempDir()
	engine, tried := Discover(DiscoverOptions{SearchRoots: []string{filepath.Join(empty, "a"), filepath.Join(empty, "b")}})
	if engine != nil {
		t.Fatal("空目录里不该发现引擎")
	}
	if len(tried) != 2 {
		t.Fatalf("tried 应记录两处尝试：%v", tried)
	}
}

// TargetPlatformSuffix 是当前平台包名后缀（与官方包名一致）。
func TargetPlatformSuffix() string { return TargetPlatform() }

// 同一台机器并存多个安装时，必须挑**版本最高**的引擎，不能挑到旧的那份。
func TestDiscoverPrefersNewestEngine(t *testing.T) {
	oldEngine := writeFakeEngine(t, func(m map[string]any) { m["version"] = "0.1.5" })
	newEngine := writeFakeEngine(t, func(m map[string]any) { m["version"] = "0.2.3" })
	for _, roots := range [][]string{{oldEngine, newEngine}, {newEngine, oldEngine}} {
		engine, tried := Discover(DiscoverOptions{SearchRoots: roots})
		if engine == nil {
			t.Fatalf("没发现引擎：%v", tried)
		}
		if engine.Version != "0.2.3" {
			t.Fatalf("挑到旧引擎 %s（roots=%v）", engine.Version, roots)
		}
	}
}

// 版本比较本身：0.2.3 > 0.1.5，v 前缀与残缺版本要能处理。
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"0.2.3", "0.1.5", 1},
		{"0.1.5", "0.2.3", -1},
		{"0.2.3", "0.2.3", 0},
		{"v0.2.3", "0.2.3", 0},
		{"0.2.10", "0.2.9", 1},
		{"0.2", "0.2.3", -1},
	}
	for _, testCase := range cases {
		if got := compareVersions(testCase.left, testCase.right); got != testCase.want {
			t.Fatalf("compareVersions(%q,%q) = %d，期望 %d", testCase.left, testCase.right, got, testCase.want)
		}
	}
}
