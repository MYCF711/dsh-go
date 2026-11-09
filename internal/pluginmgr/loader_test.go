package pluginmgr

// 红 A / 红 B 的绿侧：**受支持的 Go/WASM 插件真的装载、真的执行、禁用即撤销、卸载即回收**。
//
// 两个隔离基线红（见 $S/HS-07/logs/red-*.log）：
//   - 红 A「安装后工具不可调用」：基线上没有装载器，工具目录里永远不会有插件的
//     工具——`TestPluginRuntimeInstallEnableCall` 在基线上是编译红（`NewLoader` 未定义）；
//   - 红 B「disable 后仍可执行」：基线上 disable 只改 `state.json` 的 enabled 位，
//     之前拿到的工具句柄照样跑 wasm。`TestPluginDisableRevokesExecution` 在基线上
//     同样是编译红（没有可撤销的句柄），绿侧钉住"撤销之后这个句柄再也不会执行"。
//
// 夹具是 `internal/wasmhost/testdata/toolplugin.wasm`——真实的 Go reactor 构建
// （GOOS=wasip1 GOARCH=wasm -buildmode=c-shared），提供 echo / upper 两个工具，
// 按请求信封的 tool 字段分流。所以"到底有没有跨到客人那边跑"可以用输出判定：
// echo 回 "echo:你好"，upper 回 "upper:HELLO"。
//
// 测试全部在 t.TempDir() 下的私有管理器根目录里跑，不碰真实用户目录。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// toolpluginFixture 是真实 wasm 工具插件的入库产物（4.4 MB Go reactor）。
const toolpluginFixture = "toolplugin.wasm"

func readWasmFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "wasmhost", "testdata", name))
	if err != nil {
		t.Fatalf("读不到 wasm 夹具 %s: %v", name, err)
	}
	return raw
}

// echoToolDecls 是夹具声明的两个工具。schema 是**对象**（工具参数永远是 JSON 对象）。
func echoToolDecls() []ToolDeclaration {
	return []ToolDeclaration{
		{
			Name: "echo", Description: "把 text 原样回显（夹具工具）",
			Schema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		},
		{
			Name: "upper", Description: "把 text 里的 ASCII 字母转成大写（夹具工具）",
			Schema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		},
	}
}

// writeWasmPackage 写一个真的能被装载的私有包：payload 是入库的 wasm 夹具。
func writeWasmPackage(t *testing.T, dir, id, version string, tools []ToolDeclaration) string {
	t.Helper()
	raw := readWasmFixture(t, toolpluginFixture)
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
	}
	if len(tools) > 0 {
		manifest["tools"] = tools
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

func newRuntime(t *testing.T) (*Manager, *Loader, *Table) {
	t.Helper()
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	loader, err := NewLoader(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loader.Close(context.Background()) })
	return manager, loader, loader.Table(nil)
}

func mustGetTool(t *testing.T, table *Table, name string) *pluginTool {
	t.Helper()
	tool, ok := table.Get(name)
	if !ok {
		t.Fatalf("工具目录里没有 %q（现有：%v）", name, toolNames(table))
	}
	plugin, ok := tool.(*pluginTool)
	if !ok {
		t.Fatalf("工具 %q 的类型是 %T，不是 *pluginTool", name, tool)
	}
	return plugin
}

func toolNames(table *Table) []string {
	tools := table.Tools()
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name())
	}
	return out
}

func execTool(t *testing.T, tool *pluginTool, raw string) (string, error) {
	t.Helper()
	res, err := tool.Exec(context.Background(), json.RawMessage(raw))
	return res.Text, err
}

// TestPluginRuntimeInstallEnableCall 是红 A 的绿侧：安装 → 启用（装载）→ 真实调用。
//
// 三条断言各自都必须是真的：
//   - install 之后工具目录里**没有**它（还没启用）；
//   - enable 之后工具目录里**有**它，而且真的跨到 wasm 客人那边跑出了结果；
//   - 一个模块上的两个工具各自路由（echo/upper 输出不同）。
func TestPluginRuntimeInstallEnableCall(t *testing.T) {
	manager, loader, table := newRuntime(t)
	pkg := writeWasmPackage(t, filepath.Join(t.TempDir(), "echo-pkg"), "acme.echo", "1.0.0", echoToolDecls())

	info, err := manager.Install(pkg)
	if err != nil {
		t.Fatalf("install 失败: %v", err)
	}
	if info.Enabled {
		t.Fatalf("默认安装必须 disabled（官方 installBundle 的 enabled 默认值由调用方给）：%+v", info)
	}
	if _, ok := table.Get("echo"); ok {
		t.Fatalf("install 之后不该有工具：还没 enable；现有 %v", toolNames(table))
	}

	enabled, err := manager.Enable("acme.echo")
	if err != nil {
		t.Fatalf("enable 失败（enable 必须先装载，装载失败不能改状态）: %v", err)
	}
	if !enabled.Enabled {
		t.Fatalf("enable 之后投影必须是 enabled=true：%+v", enabled)
	}
	loaded := loader.Loaded()
	if len(loaded) != 1 || loaded[0].ID != "acme.echo" {
		t.Fatalf("装载集合不对：%+v", loaded)
	}
	if got := strings.Join(loaded[0].Tools, ","); got != "echo,upper" {
		t.Fatalf("装载的工具 = %q，want echo,upper", got)
	}
	if len(loaded[0].Digest) != 64 {
		t.Fatalf("装载投影必须带内容摘要（sha256）：%q", loaded[0].Digest)
	}

	echo := mustGetTool(t, table, "echo")
	if echo.Caps().NeedsApproval() {
		t.Fatalf("夹具没声明 caps，Caps 必须全 false（审批不按工具名猜）：%+v", echo.Caps())
	}
	text, err := execTool(t, echo, `{"text":"你好"}`)
	if err != nil {
		t.Fatalf("真实 wasm 调用失败: %v", err)
	}
	if text != "echo:你好" {
		t.Fatalf("echo 输出 = %q，want %q（说明没有真的跨到客人那边）", text, "echo:你好")
	}

	upper := mustGetTool(t, table, "upper")
	text, err = execTool(t, upper, `{"text":"hello"}`)
	if err != nil {
		t.Fatalf("第二个工具调用失败: %v", err)
	}
	if text != "upper:HELLO" {
		t.Fatalf("upper 输出 = %q，want %q（工具名没被路由进请求信封）", text, "upper:HELLO")
	}

	// 业务失败（空 text）走 isError 那一层：必须有 Hint，且 err 不为 nil。
	res, err := echo.Exec(context.Background(), json.RawMessage(`{"text":""}`))
	if err == nil {
		t.Fatal("客人回了 isError:true，Exec 必须返回 error（否则模型以为工具说没事）")
	}
	if strings.TrimSpace(res.Hint) == "" {
		t.Fatal("业务失败必须带 Hint（R3）")
	}
}

// TestPluginDisableRevokesExecution 是红 B 的绿侧：**disable 之后仍可执行**必须不成立。
//
// 断言三件事，缺一条都等于"只是标记了一下"：
//   - 工具目录里查不到它；
//   - disable 之前拿到的句柄再调，返回四字段 `plugin_disabled`，且一个字节都不跑；
//   - 重开管理器（真实持久化）之后它仍然是 disabled。
func TestPluginDisableRevokesExecution(t *testing.T) {
	manager, loader, table := newRuntime(t)
	root := manager.root
	pkg := writeWasmPackage(t, filepath.Join(t.TempDir(), "revoke-pkg"), "acme.revoke", "1.0.0", echoToolDecls())
	if _, err := manager.Install(pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable("acme.revoke"); err != nil {
		t.Fatal(err)
	}
	stale := mustGetTool(t, table, "echo")
	if text, err := execTool(t, stale, `{"text":"before"}`); err != nil || text != "echo:before" {
		t.Fatalf("disable 之前必须能跑：text=%q err=%v", text, err)
	}

	module, ok := loader.Module("acme.revoke")
	if !ok || module == nil {
		t.Fatal("装载集合里没有模块句柄（无法验证回收）")
	}

	if _, err := manager.Disable("acme.revoke"); err != nil {
		t.Fatalf("disable 失败: %v", err)
	}
	if _, ok := table.Get("echo"); ok {
		t.Fatalf("disable 之后工具目录里不该还有它：%v", toolNames(table))
	}
	if len(loader.Loaded()) != 0 {
		t.Fatalf("disable 之后装载集合必须为空：%+v", loader.Loaded())
	}
	if !module.Closed() {
		t.Fatal("disable 之后 wasm 运行时必须关闭（否则实例白占 3.5 MiB 线性内存）")
	}
	if _, err := execTool(t, stale, `{"text":"after"}`); err == nil {
		t.Fatal("disable 之后旧句柄仍然执行成功——「禁用即撤销」不成立")
	} else {
		var apiErr *Error
		if !asAPIError(err, &apiErr) || apiErr.Code != "plugin_disabled" {
			t.Fatalf("撤销错误必须是四字段 plugin_disabled，得到 %v", err)
		}
		if !strings.Contains(apiErr.Hint, "不要重试") {
			t.Fatalf("撤销的 Hint 要告诉模型别重试：%q", apiErr.Hint)
		}
	}

	// 真实持久化：重开管理器后仍是 disabled，且不会自动装载。
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := reopened.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 || plugins[0].Enabled {
		t.Fatalf("重开后 enabled 必须为 false：%+v", plugins)
	}
}

// TestPluginRuntimeInstallEnabledLoads：官方 installBundle 默认 enabled=true，
// 那条路必须"安装即装载"，且装载失败时**不留 enabled 记录**（回滚到底）。
func TestPluginRuntimeInstallEnabledLoads(t *testing.T) {
	manager, _, table := newRuntime(t)
	pkg := writeWasmPackage(t, filepath.Join(t.TempDir(), "enabled-pkg"), "acme.enabled", "1.0.0", echoToolDecls())
	job, err := manager.StartInstall(pkg, InstallOptions{Enable: true, RequestID: "enabled-1"})
	if err != nil {
		t.Fatal(err)
	}
	outcome := job.Wait()
	if outcome.Application != "applied" {
		t.Fatalf("安装作业必须 applied：%+v", outcome)
	}
	if outcome.Enabled == nil || !*outcome.Enabled {
		t.Fatalf("启用的安装必须报告 enabled=true：%+v", outcome.Enabled)
	}
	tool := mustGetTool(t, table, "echo")
	if text, err := execTool(t, tool, `{"text":"enabled"}`); err != nil || text != "echo:enabled" {
		t.Fatalf("安装即装载之后必须能真实调用：%q %v", text, err)
	}

	// 反例：同一个形状但入口不是 wasm —— 装载失败，记录不许留下。
	badPkg := writeTestPackage(t, filepath.Join(t.TempDir(), "bad-pkg"), "acme.badenabled", "1.0.0")
	badJob, err := manager.StartInstall(badPkg, InstallOptions{Enable: true, RequestID: "enabled-2"})
	if err != nil {
		t.Fatal(err)
	}
	badOutcome := badJob.Wait()
	if badOutcome.Application != "failed" {
		t.Fatalf("装载失败必须报 failed：%+v", badOutcome)
	}
	if badOutcome.Failure == nil || badOutcome.Failure.Code != "load_failed" {
		t.Fatalf("失败原因必须是四字段 load_failed：%+v", badOutcome.Failure)
	}
	plugins, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, plugin := range plugins {
		if plugin.ID == "acme.badenabled" {
			t.Fatalf("装载失败的安装必须整体回滚，不能留下 enabled 记录：%+v", plugin)
		}
	}
}

// TestPluginRuntimeCrashIsolation 是崩溃隔离：客人 trap 不能带走宿主，也不能永久坏掉。
func TestPluginRuntimeCrashIsolation(t *testing.T) {
	manager, _, table := newRuntime(t)
	pkg := writeWasmPackage(t, filepath.Join(t.TempDir(), "boom-pkg"), "acme.boom", "1.0.0", echoToolDecls())
	if _, err := manager.Install(pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable("acme.boom"); err != nil {
		t.Fatal(err)
	}
	echo := mustGetTool(t, table, "echo")
	if _, err := execTool(t, echo, `{"text":"x","boom":true}`); err == nil {
		t.Fatal("客人 panic（trap）必须当错误返回，不能静默成功")
	}
	// 宿主还活着：同一个工具的下一次调用要能自动重建实例并成功。
	text, err := execTool(t, echo, `{"text":"alive"}`)
	if err != nil {
		t.Fatalf("trap 之后宿主必须还能调（自动重建实例）: %v", err)
	}
	if text != "echo:alive" {
		t.Fatalf("trap 之后输出 = %q", text)
	}
}

// TestPluginRuntimeRegistrationErrorsReturned：注册/装载错误全部返回，没有一个被吞。
func TestPluginRuntimeRegistrationErrorsReturned(t *testing.T) {
	t.Run("duplicate tool name across plugins", func(t *testing.T) {
		manager, _, _ := newRuntime(t)
		first := writeWasmPackage(t, filepath.Join(t.TempDir(), "a"), "acme.first", "1.0.0", echoToolDecls())
		second := writeWasmPackage(t, filepath.Join(t.TempDir(), "b"), "acme.second", "1.0.0", echoToolDecls())
		for _, pkg := range []string{first, second} {
			if _, err := manager.Install(pkg); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := manager.Enable("acme.first"); err != nil {
			t.Fatal(err)
		}
		_, err := manager.Enable("acme.second")
		if err == nil {
			t.Fatal("两个插件声明同名工具必须报错（工具表按名字存，第二处登记必然失败）")
		}
		var apiErr *Error
		if !asAPIError(err, &apiErr) || apiErr.Code != "duplicate_tool" {
			t.Fatalf("错误必须是四字段 duplicate_tool，得到 %v", err)
		}
		// 失败的装载必须整体回滚：第二个插件没进装载集合，第一个还活着。
		plugins, lerr := manager.List()
		if lerr != nil {
			t.Fatal(lerr)
		}
		for _, plugin := range plugins {
			if plugin.ID == "acme.second" && plugin.Enabled {
				t.Fatalf("装载失败的插件不能留下 enabled=true：%+v", plugin)
			}
		}
	})

	t.Run("wasm without tool ABI", func(t *testing.T) {
		manager, _, _ := newRuntime(t)
		// fixture.wasm 只有 add/spin：装载得起来，但一次都调不动。
		dir := filepath.Join(t.TempDir(), "noabi")
		raw := readWasmFixture(t, "fixture.wasm")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		manifest := map[string]any{
			"schema": "dsh.local-plugin@1", "id": "acme.noabi", "version": "1.0.0",
			"kind": "wasm", "entry": "plugin.wasm",
			"files": map[string]string{"plugin.wasm": hex.EncodeToString(sum[:])},
			"tools": echoToolDecls()[:1],
		}
		rawManifest, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), rawManifest, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Install(dir); err != nil {
			t.Fatal(err)
		}
		_, err = manager.Enable("acme.noabi")
		if err == nil {
			t.Fatal("声明了工具却没有 alloc/call 导出：必须在装载时炸，不能等第一次调用")
		}
		var apiErr *Error
		if !asAPIError(err, &apiErr) || apiErr.Code != "invalid_plugin" {
			t.Fatalf("错误必须是四字段 invalid_plugin，得到 %v", err)
		}
		if !strings.Contains(apiErr.Hint, "alloc") || !strings.Contains(apiErr.Hint, "call") {
			t.Fatalf("Hint 必须说清缺哪两个导出：%q", apiErr.Hint)
		}
	})

	t.Run("not a wasm module", func(t *testing.T) {
		manager, _, _ := newRuntime(t)
		pkg := writeTestPackage(t, filepath.Join(t.TempDir(), "text"), "acme.text", "1.0.0")
		if _, err := manager.Install(pkg); err != nil {
			t.Fatal(err)
		}
		_, err := manager.Enable("acme.text")
		if err == nil {
			t.Fatal("不是 wasm 的入口必须在装载时炸")
		}
		var apiErr *Error
		if !asAPIError(err, &apiErr) || apiErr.Code != "load_failed" {
			t.Fatalf("错误必须是四字段 load_failed，得到 %v", err)
		}
	})
}

// TestPluginRuntimeProcessKindRejected：process 后端没有实现，安装即拒（不留静默成功）。
func TestPluginRuntimeProcessKindRejected(t *testing.T) {
	manager, _, _ := newRuntime(t)
	pkg := writeRawPackage(t, filepath.Join(t.TempDir(), "process"), "plugin.bin",
		[]byte("\x7fELF not really\n"),
		map[string]any{
			"schema": "dsh.local-plugin@1", "id": "acme.process", "version": "1.0.0",
			"kind": "process", "entry": "plugin.bin",
		})
	_, err := manager.Install(pkg)
	if err == nil {
		t.Fatal("kind=process 的包必须显式拒绝（process 后端未实现，装上也永远跑不起来）")
	}
	var apiErr *Error
	if !asAPIError(err, &apiErr) || apiErr.Code != "unsupported_plugin" {
		t.Fatalf("错误必须是四字段 unsupported_plugin，得到 %v", err)
	}
}

// TestPluginRuntimeSyncAll：启动时让运行时与已安装状态对齐。
func TestPluginRuntimeSyncAll(t *testing.T) {
	manager, loader, table := newRuntime(t)
	on := writeWasmPackage(t, filepath.Join(t.TempDir(), "on"), "acme.on", "1.0.0", echoToolDecls())
	off := writeWasmPackage(t, filepath.Join(t.TempDir(), "off"), "acme.off", "1.0.0", echoToolDecls())
	for _, pkg := range []string{on, off} {
		if _, err := manager.Install(pkg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.Enable("acme.on"); err != nil {
		t.Fatal(err)
	}
	// 模拟"重启后内存里什么都没有"：先关掉运行时，再挂一个新的。
	if err := loader.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewLoader(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close(context.Background()) })
	restartedTable := restarted.Table(nil)
	if len(restarted.Loaded()) != 0 {
		t.Fatalf("新运行时起步必须是空的：%+v", restarted.Loaded())
	}
	loaded, err := restarted.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("SyncAll 失败: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "acme.on" {
		t.Fatalf("SyncAll 只该装载 enabled 的包：%+v", loaded)
	}
	if _, ok := restartedTable.Get("echo"); !ok {
		t.Fatal("SyncAll 之后工具必须可查")
	}
	_ = table
}

// TestPluginUnloadReclaimsGoroutines：卸载之后 goroutine 归还（"进程回收"的 wasm 含义）。
func TestPluginUnloadReclaimsGoroutines(t *testing.T) {
	manager, loader, _ := newRuntime(t)
	baseline := runtime.NumGoroutine()
	pkg := writeWasmPackage(t, filepath.Join(t.TempDir(), "reclaim"), "acme.reclaim", "1.0.0", echoToolDecls())
	if _, err := manager.Install(pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable("acme.reclaim"); err != nil {
		t.Fatal(err)
	}
	if err := loader.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("卸载后 goroutine 没归还：baseline=%d now=%d", baseline, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// asAPIError 是 errors.As 的薄包装：四字段错误的断言到处都是，别每次写三行。
func asAPIError(err error, target **Error) bool { return errors.As(err, target) }
