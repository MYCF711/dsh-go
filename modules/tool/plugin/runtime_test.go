package plugin

// R2 / A5 的落点：插件运行时只能经 *kernel.Scope 装配，作用域撤销必须真的回收。
//
// 三条断言：
//   - `Register` 只拿 *kernel.Scope（没有 *App、没有包级单例），服务经
//     `kernel.Provide` 暴露；
//   - `plugintools` 是"静态工具 + 插件工具"的合成表：装上插件之后两边都看得见；
//   - `k.Root().Dispose()` 之后插件模块被关闭（实例、线性内存、goroutine 归还）——
//     这就是 A5「卸载即撤销」在插件运行时的形态。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/spi"
)

// fakeRegistry 是静态工具目录的最小实现：只用来证明合成表两半都在。
type fakeRegistry struct{ byName map[string]spi.Tool }

func newFakeRegistry() *fakeRegistry { return &fakeRegistry{byName: map[string]spi.Tool{}} }

func (r *fakeRegistry) Register(tool spi.Tool) error {
	if _, dup := r.byName[tool.Name()]; dup {
		return &duplicateToolError{name: tool.Name()}
	}
	r.byName[tool.Name()] = tool
	return nil
}

func (r *fakeRegistry) Tools() []spi.Tool {
	out := make([]spi.Tool, 0, len(r.byName))
	for _, tool := range r.byName {
		out = append(out, tool)
	}
	return out
}

func (r *fakeRegistry) Get(name string) (spi.Tool, bool) {
	tool, ok := r.byName[name]
	return tool, ok
}

type duplicateToolError struct{ name string }

func (e *duplicateToolError) Error() string { return "重复工具 " + e.name }

// staticTool 是编译进二进制的内置工具（不来自插件包）。
type staticTool struct{ name string }

func (t *staticTool) Name() string        { return t.name }
func (t *staticTool) Description() string { return "内置静态工具（测试夹具）" }
func (t *staticTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *staticTool) Caps() spi.Caps { return spi.Caps{} }
func (t *staticTool) Exec(context.Context, json.RawMessage) (spi.Result, error) {
	return spi.Result{Text: "static:" + t.name}, nil
}

// toolsModule 提供 "tools" 服务（真实装配里由 modules/tools 提供）。
func toolsModule() kernel.Module {
	return kernel.Module{
		ID:       "tools",
		Backend:  kernel.BackendStatic,
		Provides: []string{spi.ToolsKey.String()},
		Register: func(s *kernel.Scope) error {
			registry := newFakeRegistry()
			if err := registry.Register(&staticTool{name: "builtin"}); err != nil {
				return err
			}
			return kernel.Provide(s, spi.ToolsKey, spi.ToolRegistry(registry))
		},
	}
}

// writeWasmPackage 写一个真的能被装载的私有包（payload 是入库的 wasm 夹具）。
func writeWasmPackage(t *testing.T, dir, id, version string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "internal", "wasmhost", "testdata", "toolplugin.wasm"))
	if err != nil {
		t.Fatalf("读不到 wasm 夹具: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest := map[string]any{
		"schema": "dsh.local-plugin@1", "id": id, "version": version,
		"kind": "wasm", "entry": "plugin.wasm",
		"files": map[string]string{"plugin.wasm": hex.EncodeToString(sum[:])},
		"tools": []map[string]any{{
			"name": "echo", "description": "回显 text（夹具工具）",
			"schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"text": map[string]any{"type": "string"}},
				"required":   []string{"text"},
			},
		}},
	}
	rawManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), rawManifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPluginRuntimeScopeDispose(t *testing.T) {
	manager, err := pluginmgr.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New("plugin-runtime-scope")
	plan, err := kernel.Assemble(k.Root(), []kernel.Module{toolsModule(), Runtime(manager)})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	if len(plan.Order) != 2 {
		t.Fatalf("装配顺序不对：%d 个模块", len(plan.Order))
	}

	loader, err := kernel.Inject(k.Root(), LoaderKey)
	if err != nil {
		t.Fatalf("注入插件运行时失败（R2：服务必须经作用域提供）: %v", err)
	}
	table, err := kernel.Inject(k.Root(), TableKey)
	if err != nil {
		t.Fatalf("注入合成工具目录失败: %v", err)
	}

	// 合成表：静态工具先可见，插件工具装上了才可见。
	if _, ok := table.Get("builtin"); !ok {
		t.Fatalf("静态工具必须在合成表里：%v", names(table))
	}
	if _, ok := table.Get("echo"); ok {
		t.Fatalf("还没装插件就不该有 echo：%v", names(table))
	}

	pkg := writeWasmPackage(t, filepath.Join(t.TempDir(), "echo-pkg"), "acme.scope", "1.0.0")
	if _, err := manager.Install(pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable("acme.scope"); err != nil {
		t.Fatalf("enable 失败: %v", err)
	}
	tool, ok := table.Get("echo")
	if !ok {
		t.Fatalf("enable 之后插件工具必须在合成表里：%v", names(table))
	}
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"text":"scope"}`))
	if err != nil {
		t.Fatalf("真实 wasm 调用失败: %v", err)
	}
	if res.Text != "echo:scope" {
		t.Fatalf("输出 = %q，want echo:scope", res.Text)
	}
	module, ok := loader.Module("acme.scope")
	if !ok || module == nil {
		t.Fatal("拿不到模块句柄（无法验证回收）")
	}

	// A5：作用域撤销 = 真的卸载。
	k.Root().Dispose()
	if !module.Closed() {
		t.Fatal("作用域撤销之后插件模块必须被关闭（实例、线性内存、goroutine 要还回去）")
	}
	if len(loader.Loaded()) != 0 {
		t.Fatalf("作用域撤销之后装载集合必须为空：%+v", loader.Loaded())
	}
	if _, ok := table.Get("echo"); ok {
		t.Fatal("作用域撤销之后合成表里不该还有插件工具")
	}
}

// TestPluginRuntimeRequiresToolsService：缺 "tools" 服务必须装配失败（fail-loud）。
func TestPluginRuntimeRequiresToolsService(t *testing.T) {
	manager, err := pluginmgr.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New("plugin-runtime-missing-tools")
	t.Cleanup(k.Root().Dispose)
	if _, err := kernel.Assemble(k.Root(), []kernel.Module{Runtime(manager)}); err == nil {
		t.Fatal("没有 tools 服务时装配必须失败，不能装上一张只有插件的表")
	} else if !strings.Contains(err.Error(), "tools") {
		t.Fatalf("装配错误必须点名缺哪个能力：%v", err)
	}
}

// TestPluginRuntimeNoInitRegistration：禁止 init() 注册（R2）。
//
// 为什么这条要真扫源码：`init()` 里的注册拿不到 *Scope、失败也没人能接
// （进程直接死，连四字段错误都没有）。扫描范围是本包全部非测试文件——
// 新增文件自动纳入，不需要有人记得来加一行。
func TestPluginRuntimeNoInitRegistration(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		scanned++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.Name == "init" {
				t.Fatalf("%s 里有 init()：注册必须发生在 kernel.Module.Register(*Scope) 里（R2）", name)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("一个文件都没扫到：这条守卫没在跑")
	}
}

func names(table spi.ToolRegistry) []string {
	tools := table.Tools()
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name())
	}
	return out
}
