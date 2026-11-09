package main

// 插件运行时在**真实宿主装配**里的接线证据。
//
// 与 internal/pluginmgr 的装载器用例分工：
//   - 装载器用例证明"装/卸/撤销/回收"本身；
//   - 这里证明它接进真实宿主之后仍然成立：真 kernel（modules/tools 的静态目录 +
//     插件运行时模块）+ 真工具流水线（审批/超时/截断那一层）+ 真 wasm 客人，
//     以及**既有 HTTP 适配器**（serve_plugins.go 的 newPluginHTTPHandler）在
//     不改一行的前提下驱动装载/卸载——因为状态与运行时的同步写在 Manager 里。
//
// 没有做的（如实记录，见 $S/HS-07/WIRING.md）：
//   - serve.go 的模块列表还没把 plugin-runtime 加进去（写作用域不含 serve.go）；
//     所以"模型能看到插件工具"这一步在生产装配里仍差一行，测试用同一装配
//     方式（kernel.Assemble + kernel.Inject(TableKey)）证明它是对的。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/spi"
	pluginmod "dshgo/modules/tool/plugin"
	"dshgo/modules/tools"
)

// writePluginRuntimeWasmPackage 写一个真的能被装载的私有包（payload 是入库的
// wasm 工具插件夹具：GOOS=wasip1 GOARCH=wasm -buildmode=c-shared 的真实产物）。
func writePluginRuntimeWasmPackage(t *testing.T, dir, id, version string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "wasmhost", "testdata", "toolplugin.wasm"))
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
		"tools": []map[string]any{
			{
				"name": "echo", "description": "把 text 原样回显（夹具工具）",
				"schema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			},
			{
				"name": "upper", "description": "把 text 里的 ASCII 字母转成大写（夹具工具）",
				"schema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
				"caps": []string{},
			},
		},
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

// assemblePluginRuntime 按生产装配方式把"静态工具 + 插件运行时"装进一个真 kernel。
func assemblePluginRuntime(t *testing.T, manager *pluginmgr.Manager) (*kernel.Kernel, spi.ToolRegistry) {
	t.Helper()
	k := kernel.New("plugin-runtime-test")
	t.Cleanup(k.Root().Dispose)
	if _, err := kernel.Assemble(k.Root(), []kernel.Module{
		tools.Module(tools.Options{SpillDir: t.TempDir()}),
		pluginmod.Runtime(manager),
	}); err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	table, err := kernel.Inject(k.Root(), pluginmod.TableKey)
	if err != nil {
		t.Fatalf("注入合成工具目录失败: %v", err)
	}
	return k, table
}

// TestPluginRuntimeRealPipelineCall 走**真流水线**（modules/tools.Pipeline）：
// 审批槽、超时、截断、R3 提示全在，工具本身是真的 wasm 客人。
func TestPluginRuntimeRealPipelineCall(t *testing.T) {
	manager, err := pluginmgr.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	k, table := assemblePluginRuntime(t, manager)
	pipeline := tools.NewPipeline(table, k.Bus(), tools.Options{SpillDir: t.TempDir()})

	pkg := writePluginRuntimeWasmPackage(t, filepath.Join(t.TempDir(), "echo-pkg"), "acme.pipeline", "1.0.0")
	if _, err := manager.Install(pkg); err != nil {
		t.Fatalf("install 失败: %v", err)
	}
	if _, err := pipeline.Run(context.Background(), "echo", json.RawMessage(`{"text":"x"}`)); err == nil {
		t.Fatal("还没 enable 就不该能调用（安装 ≠ 装载）")
	}

	if _, err := manager.Enable("acme.pipeline"); err != nil {
		t.Fatalf("enable 失败: %v", err)
	}
	res, err := pipeline.Run(context.Background(), "echo", json.RawMessage(`{"text":"流水线"}`))
	if err != nil {
		t.Fatalf("真实流水线调用失败: %v", err)
	}
	if res.Text != "echo:流水线" {
		t.Fatalf("流水线结果 = %q，want echo:流水线", res.Text)
	}
	res, err = pipeline.Run(context.Background(), "upper", json.RawMessage(`{"text":"pipe"}`))
	if err != nil {
		t.Fatalf("第二个工具失败: %v", err)
	}
	if res.Text != "upper:PIPE" {
		t.Fatalf("upper 结果 = %q，want upper:PIPE", res.Text)
	}
	t.Logf("真实调用日志：echo=%q upper=%q（wasm 客人实际执行）", "echo:流水线", res.Text)

	// 禁用之后：同一个名字在流水线里也调不到（不是"标记一下"）。
	if _, err := manager.Disable("acme.pipeline"); err != nil {
		t.Fatalf("disable 失败: %v", err)
	}
	if _, err := pipeline.Run(context.Background(), "echo", json.RawMessage(`{"text":"after"}`)); err == nil {
		t.Fatal("disable 之后流水线仍然调得到——「禁用即撤销」不成立")
	} else if !strings.Contains(err.Error(), "echo") {
		t.Fatalf("错误必须点名工具，便于模型改做法：%v", err)
	}
	if _, ok := table.Get("echo"); ok {
		t.Fatal("disable 之后工具目录里不该还有 echo")
	}
}

// TestPluginRuntimeHTTPHandlerDrivesLoad 证明**既有 HTTP 适配器**不需要改一行：
// 状态变更发生在 Manager 里，运行时同步跟着发生。
func TestPluginRuntimeHTTPHandlerDrivesLoad(t *testing.T) {
	manager, err := pluginmgr.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	_, table := assemblePluginRuntime(t, manager)

	server := httptest.NewServer(newPluginHTTPHandler(manager))
	defer server.Close()

	call := func(method, body string) map[string]any {
		t.Helper()
		response, err := http.Post(server.URL+"/api/"+method, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST %s: %v", method, err)
		}
		defer response.Body.Close()
		var envelope map[string]any
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatalf("解析 %s 响应失败: %v", method, err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s 状态码 = %d，响应 %+v", method, response.StatusCode, envelope)
		}
		return envelope
	}

	pkg := writePluginRuntimeWasmPackage(t, filepath.Join(t.TempDir(), "http-pkg"), "acme.http", "1.0.0")
	call("plugin.install", fmt.Sprintf(`{"path":%q}`, pkg))
	if _, ok := table.Get("echo"); ok {
		t.Fatal("install 之后不该有工具（默认 disabled）")
	}
	call("plugin.enable", `{"id":"acme.http"}`)
	tool, ok := table.Get("echo")
	if !ok {
		t.Fatalf("enable 之后 HTTP 路径必须真的装载：%v", namesOf(table))
	}
	result, err := tool.Exec(context.Background(), json.RawMessage(`{"text":"http"}`))
	if err != nil || result.Text != "echo:http" {
		t.Fatalf("HTTP 装载后的真实调用失败：%q %v", result.Text, err)
	}
	call("plugin.disable", `{"id":"acme.http"}`)
	if _, ok := table.Get("echo"); ok {
		t.Fatalf("disable 之后工具必须消失：%v", namesOf(table))
	}
	if _, err := tool.Exec(context.Background(), json.RawMessage(`{"text":"http"}`)); err == nil {
		t.Fatal("disable 之后旧句柄仍然执行——禁用不撤销")
	}
}

func namesOf(table spi.ToolRegistry) []string {
	tools := table.Tools()
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name())
	}
	return out
}

// TestPluginRuntimeNoInitRegistration：接线文件里禁止 init() 注册（R2）。
func TestPluginRuntimeNoInitRegistration(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "serve_plugin_loader.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 serve_plugin_loader.go 失败: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "init" {
			t.Fatal("serve_plugin_loader.go 里有 init()：装配必须显式调用（R2）")
		}
	}
}

// TestPluginRuntimeNoJSRuntime：宿主的依赖里没有 JS 引擎（不嵌 JS 运行时，R6）。
//
// 为什么这条值得一条测试：想"顺手支持 JS 插件"最省事的做法就是引一个 goja/
// quickjs 绑定进来，而那会同时破坏隔离模型（客人能拿到任意原生能力）与
// 跨平台构建。依赖表是这件事唯一稳定的事实来源。
func TestPluginRuntimeNoJSRuntime(t *testing.T) {
	engines := []string{"goja", "quickjs", "quickjs-go", "otto", "duktape", "v8go", "rogchap.com/v8go", "sobek"}
	for _, name := range []string{"go.mod", "go.sum"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatalf("读不到 %s: %v", name, err)
		}
		lower := strings.ToLower(string(raw))
		for _, engine := range engines {
			if strings.Contains(lower, strings.ToLower(engine)) {
				t.Fatalf("%s 里出现了 JS 引擎 %q：本仓禁止嵌入 JS 运行时（R6）", name, engine)
			}
		}
	}
}
