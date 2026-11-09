package pluginmgr

// 运行时装载器：把"已安装的私有包"变成"真的能被调用的工具"。
//
// 为什么需要这一层：`Manager` 只做**存储**（复制、校验、事务、状态），
// 它连"这个包装得上吗"都不回答。于是"装上了"曾经等于成功——而入口一次都
// 没跑过（docs/official-plugins-022-map.md #2：装完不装载、不执行入口）。
// 本文件是那条缺口的落点：装载、执行、禁用即撤销、卸载即回收。
//
// 真实性边界（每条都有 loader_test.go 的真实 wasm 断言）：
//
//   - `Load` 真的用 wazero 装载入口 wasm（魔数 / 编译 / `_initialize` / ABI 导出
//     全部校验），失败时错误里带插件 id 与路径；
//   - 声明的每个工具真的进工具目录（`Table`），`Exec` 真的跨到客人那边跑；
//   - `Manager.Disable` 之后**工具目录里就没有它**，之前拿到的句柄再调也会
//     被运行时拒绝（"禁用即撤销"，不是"标记一下"）；
//   - `Unload` 关掉 wazero 运行时并释放实例（崩溃隔离与内存回收的落点）；
//   - 注册错误（重名工具、缺 ABI 导出、不是 wasm、process/JS 形态）全部返回，
//     没有一个被吞掉。
//
// 没有做的事（明确不假装）：
//   - 不执行 process 插件（无 process 后端）；
//   - 不执行 JS/Cordis 插件（不嵌 JS 运行时，AGENTS R6）——两者在安装边界上
//     就被显式拒绝（见 manifest.go）；
//   - 不 fork 子进程：wasm 客人在本进程的 wazero 沙箱里跑。"进程回收"在这里
//     的准确含义是"卸载后 wazero 运行时与实例全部关闭、goroutine 归还"，
//     用 `Module.Closed()` 与 goroutine 计数验证，没有子进程可收。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dshgo/internal/spi"
	"dshgo/internal/wasmhost"
)

// LoadedPlugin 是一个已装载插件的只读投影（诊断与 Transport 用）。
type LoadedPlugin struct {
	ID      string   `json:"id"`
	Version string   `json:"version"`
	Kind    string   `json:"kind"`
	Entry   string   `json:"entry"`
	Digest  string   `json:"digest"`
	Tools   []string `json:"tools"`
	// Gen 是装载代数（每次装载/重载 +1）：并发用例靠它证明"重新装载真的发生了"。
	Gen int64 `json:"gen"`
}

type loadedPlugin struct {
	info   LoadedPlugin
	module *wasmhost.Module
	router *wasmhost.ToolRouter
	tools  []*pluginTool
}

// Loader 是"当前进程里活着的那批插件"。
//
// 它同时是 `Manager` 的运行时钩子（`Manager.runtime`）：状态一变，装载集合跟着变。
// 这样"enabled=true 但没跑起来""disabled 但还能调"两种不一致在结构上不存在。
type Loader struct {
	mu      sync.Mutex
	manager *Manager
	byID    map[string]*loadedPlugin
	byTool  map[string]*pluginTool
	gen     int64
	closed  bool
}

// NewLoader 创建（或取回）管理器的装载器。
//
// 挂上去之后，`Manager` 的 enable/disable/uninstall/update/restore 会自动装载/
// 卸载（见 manager.go 的 setEnabled / Uninstall / installVersionLocked）。
//
// **一个管理器只有一个运行时**，所以这里幂等：已经挂过就返回同一个句柄。
// 为什么不是"第二个就报错"：装配层（kernel.Module.Register）可能被调用多次
// （测试里两个 kernel 共用同一个管理器，生产里热重载也是同一形状），报错会让
// "再装配一次"变成失败；而两个**不同**的运行时同时握着同一批 wasm 实例才是
// 真正要防的——幂等返回从结构上让它不可能发生。`Close` 会摘钩，之后可以再建。
func NewLoader(manager *Manager) (*Loader, error) {
	if manager == nil {
		return nil, badRequest("插件运行时需要已打开的管理器（manager 不能为空）")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.runtime != nil {
		return manager.runtime, nil
	}
	loader := &Loader{manager: manager, byID: map[string]*loadedPlugin{}, byTool: map[string]*pluginTool{}}
	manager.runtime = loader
	return loader, nil
}

// Manager 返回装载器背后的管理器（诊断用；不返回内部指针给模块）。
func (l *Loader) Manager() *Manager { return l.manager }

// Load 按 id 装载当前版本的包。已装载同版本时幂等返回。
func (l *Loader) Load(ctx context.Context, id string) (LoadedPlugin, error) {
	version, err := l.currentVersion(id)
	if err != nil {
		return LoadedPlugin{}, err
	}
	return l.loadRecord(ctx, id, version)
}

// Reload 强制重装：先卸载（关实例）再按当前版本重新装载。
func (l *Loader) Reload(ctx context.Context, id string) (LoadedPlugin, error) {
	version, err := l.currentVersion(id)
	if err != nil {
		return LoadedPlugin{}, err
	}
	if err := l.Unload(ctx, id); err != nil {
		return LoadedPlugin{}, err
	}
	return l.loadRecord(ctx, id, version)
}

func (l *Loader) currentVersion(id string) (string, error) {
	l.manager.mu.Lock()
	defer l.manager.mu.Unlock()
	record := l.manager.state.Plugins[id]
	if record == nil {
		return "", notFound(id)
	}
	return record.Version, nil
}

// loadRecord 是装载的唯一实现。version 由调用方在管理器锁内取好，
// 避免"读到版本、装载时它已经被 update 换掉"的半截状态。
func (l *Loader) loadRecord(ctx context.Context, id, version string) (LoadedPlugin, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(version) == "" {
		return LoadedPlugin{}, badRequest("装载插件需要 id 与已安装的版本（id=%q version=%q）", id, version)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return LoadedPlugin{}, errorf("runtime_closed", http.StatusConflict, false,
			"插件运行时已经关闭（宿主正在退出）：这是关闭流程的正常结果，不要重试",
			"插件 %s 不能装载：运行时已关闭", id)
	}
	if existing := l.byID[id]; existing != nil && existing.info.Version == version {
		return existing.info, nil
	}

	dir := l.manager.packagePath(id, version)
	manifest, err := readAndValidatePackage(dir)
	if err != nil {
		return LoadedPlugin{}, err
	}
	if manifest.ID != id || manifest.Version != version {
		return LoadedPlugin{}, errorf("package_corrupt", http.StatusUnprocessableEntity, false,
			"包目录与 state.json 身份不一致：保留现场并 repair，不要静默重装",
			"已安装包身份不符：state=%s@%s manifest=%s@%s", id, version, manifest.ID, manifest.Version)
	}
	entry := filepath.Join(dir, filepath.FromSlash(manifest.Entry))
	module, err := wasmhost.Load(ctx, id, entry)
	if err != nil {
		return LoadedPlugin{}, errorf("load_failed", http.StatusUnprocessableEntity, false,
			"这是插件本身的装载失败：按错误原文修插件（不是宿主 bug，重试同样失败）",
			"装载插件 %s@%s 的入口 %s 失败: %v", id, version, entry, err)
	}
	// 装载成功之后任何一步失败都必须把模块关掉：留一个调用不到的实例
	// 只会白占 3.5 MiB 线性内存，而且 `Loaded()` 里看不到它。
	fail := func(err error) (LoadedPlugin, error) {
		_ = module.Close(context.Background())
		return LoadedPlugin{}, err
	}

	router := wasmhost.NewToolRouter(module)
	tools, names, err := l.buildTools(id, version, manifest, router)
	if err != nil {
		return fail(err)
	}

	l.gen++
	loaded := &loadedPlugin{
		info: LoadedPlugin{
			ID: id, Version: version, Kind: manifest.Kind, Entry: manifest.Entry,
			Digest: module.Digest(), Tools: names, Gen: l.gen,
		},
		module: module, router: router, tools: tools,
	}
	for _, tool := range tools {
		tool.gen = l.gen
	}
	// 版本变了：旧实例在换指针之前关掉（同一个 id 不允许两个活实例）。
	if previous := l.byID[id]; previous != nil {
		l.releaseLocked(previous)
	}
	l.byID[id] = loaded
	for _, tool := range tools {
		l.byTool[tool.name] = tool
	}
	return loaded.info, nil
}

// buildTools 把 manifest 里的工具声明变成 spi.Tool。
//
// 注册错误全部返回：重名（跨插件）、缺 ABI 导出、声明本身不合法。
// 任何一个失败都不提交，调用方负责关模块。
func (l *Loader) buildTools(id, version string, manifest Manifest, router *wasmhost.ToolRouter) ([]*pluginTool, []string, error) {
	if len(manifest.Tools) == 0 {
		// 没有工具声明 = 装载但不提供能力（与 dsh.toml 的 wasm 模块同义）。
		// 这不是静默成功：LoadedPlugin.Tools 是空的，调用方看得见。
		return nil, nil, nil
	}
	exports := router.Exports()
	if !hasExport(exports, "alloc") || !hasExport(exports, "call") {
		return nil, nil, errorf("invalid_plugin", http.StatusUnprocessableEntity, false,
			"工具插件必须导出 alloc(len)->ptr 与 call(ptr,len)->(ptr<<32|len)；"+
				"Go 客人请用 GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared 构建"+
				"（docs/wasm-tool-abi.md §3）",
			"插件 %s@%s 的 wasm 没有导出工具 ABI（导出表：%v），装载得起来但一次都调不动",
			id, version, exports)
	}
	tools := make([]*pluginTool, 0, len(manifest.Tools))
	names := make([]string, 0, len(manifest.Tools))
	for _, decl := range manifest.Tools {
		name := strings.TrimSpace(decl.Name)
		if owner, dup := l.byTool[name]; dup && owner.pluginID != id {
			return nil, nil, errorf("duplicate_tool", http.StatusConflict, false,
				"工具名在宿主范围内必须唯一：改一个名字，或先卸载提供它的插件",
				"工具 %q 已被插件 %q 占用；插件 %q 不能再注册同名工具", name, owner.pluginID, id)
		}
		caps, err := ParseToolCaps(decl.Caps)
		if err != nil {
			return nil, nil, manifestError("插件 %s 的工具 %q: %v", id, name, err)
		}
		tools = append(tools, &pluginTool{
			loader: l, pluginID: id, name: name,
			desc: strings.TrimSpace(decl.Description), schema: json.RawMessage(decl.Schema),
			caps: caps, timeout: ToolTimeout(decl.TimeoutMS), router: router,
		})
		names = append(names, name)
	}
	return tools, names, nil
}

// releaseLocked 撤掉一个已装载插件：工具出目录、实例关闭。调用方必须持有 l.mu。
func (l *Loader) releaseLocked(p *loadedPlugin) {
	delete(l.byID, p.info.ID)
	for _, tool := range p.tools {
		if l.byTool[tool.name] == tool {
			delete(l.byTool, tool.name)
		}
	}
	if p.module != nil {
		// 清理不看调用方的 ctx：请求被取消时也必须把运行时关掉。
		_ = p.module.Close(context.Background())
	}
}

// Unload 卸载插件：工具立刻从目录里消失，之前拿到的句柄再调会被拒绝。幂等。
func (l *Loader) Unload(ctx context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.byID[id]
	if p == nil {
		return nil
	}
	// 关闭动作固定用 Background：请求侧的取消不能变成"关一半"。
	l.releaseLocked(p)
	return nil
}

// Close 卸载全部插件并关闭运行时。可重复调用。
func (l *Loader) Close(ctx context.Context) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	ids := make([]string, 0, len(l.byID))
	for id := range l.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		l.releaseLocked(l.byID[id])
	}
	l.mu.Unlock()

	// 与管理器脱钩：关闭之后再 enable 不会往一个死运行时里装东西。
	l.manager.mu.Lock()
	if l.manager.runtime == l {
		l.manager.runtime = nil
	}
	l.manager.mu.Unlock()
	return nil
}

// Module 返回某个已装载插件的 wasm 模块句柄（只读诊断）。
//
// 为什么要有它："卸载后运行时真的回收了"必须可断言——调用方先拿住句柄，
// 卸载之后用 Module.Closed() 证明实例与 runtime 真的关掉了。
func (l *Loader) Module(id string) (*wasmhost.Module, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	plugin := l.byID[id]
	if plugin == nil || plugin.module == nil {
		return nil, false
	}
	return plugin.module, true
}

// Loaded 返回当前装载集合（按 id 排序）。已关闭时返回空。
func (l *Loader) Loaded() []LoadedPlugin {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LoadedPlugin, 0, len(l.byID))
	for _, p := range l.byID {
		out = append(out, p.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SyncAll 让运行时与已安装状态对齐：装载全部 enabled 的包、卸载其余。
//
// 这是启动路径：宿主重启后不该出现"state.json 说开着、工具表里没有"。
// 错误不吞：全部尝试完再一起返回（一个坏插件不该阻止其他插件起来）。
func (l *Loader) SyncAll(ctx context.Context) ([]LoadedPlugin, error) {
	l.manager.mu.Lock()
	want := make(map[string]string, len(l.manager.state.Plugins))
	for id, record := range l.manager.state.Plugins {
		if record.Enabled {
			want[id] = record.Version
		}
	}
	l.manager.mu.Unlock()

	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var errs []error
	for _, id := range ids {
		if _, err := l.loadRecord(ctx, id, want[id]); err != nil {
			errs = append(errs, err)
		}
	}

	l.mu.Lock()
	stale := make([]string, 0, len(l.byID))
	for id := range l.byID {
		if _, keep := want[id]; !keep {
			stale = append(stale, id)
		}
	}
	l.mu.Unlock()
	sort.Strings(stale)
	for _, id := range stale {
		_ = l.Unload(ctx, id)
	}
	if len(errs) > 0 {
		return l.Loaded(), errors.Join(errs...)
	}
	return l.Loaded(), nil
}

// Table 把插件工具与**已有的静态工具目录**合成一张表。
//
// 为什么是合成而不是替换：静态模块（编译进二进制的那批）与插件包是同一张
// 工具表的两半，模型不该知道谁是谁。`inner` 为 nil 时只暴露插件工具。
func (l *Loader) Table(inner spi.ToolRegistry) *Table { return &Table{loader: l, inner: inner} }

// Table 是 spi.ToolRegistry 的动态实现：插件工具随装载集合实时变化。
type Table struct {
	loader *Loader
	inner  spi.ToolRegistry
}

// Register 把静态工具委托给 inner。插件工具**不能**运行时注册：
// 它们的身份来自包 manifest（R4），凭空多一行等于绕过安装校验。
func (t *Table) Register(tool spi.Tool) error {
	if t.inner == nil {
		return errorf("read_only", http.StatusConflict, false,
			"插件工具由包声明（plugin.json 的 tools），经 install/enable 装载，不能运行时注册",
			"工具目录是只读的：没有静态工具目录可以接收注册")
	}
	return t.inner.Register(tool)
}

// Tools 按"静态在前、插件在后（按名字排序）"返回快照。
func (t *Table) Tools() []spi.Tool {
	var out []spi.Tool
	if t.inner != nil {
		out = append(out, t.inner.Tools()...)
	}
	out = append(out, t.loader.pluginTools()...)
	return out
}

// Get 先查插件工具、再查静态目录：插件工具是动态的，名字一旦卸载就真的没有了。
func (t *Table) Get(name string) (spi.Tool, bool) {
	if tool, ok := t.loader.pluginTool(name); ok {
		return tool, true
	}
	if t.inner != nil {
		return t.inner.Get(name)
	}
	return nil, false
}

func (l *Loader) pluginTool(name string) (spi.Tool, bool) {
	// 挂载开关先判、且不在 l.mu 里问管理器：管理器 → 装载器 这条锁顺序已经存在
	// （Install 持 m.mu 时调 Load），反过来拿会成环。
	if !l.manager.ToolsMounted() {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	tool, ok := l.byTool[name]
	if !ok {
		return nil, false
	}
	return tool, true
}

func (l *Loader) pluginTools() []spi.Tool {
	if !l.manager.ToolsMounted() {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	names := make([]string, 0, len(l.byTool))
	for name := range l.byTool {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]spi.Tool, 0, len(names))
	for _, name := range names {
		out = append(out, l.byTool[name])
	}
	return out
}

// checkLive 是"禁用即撤销"的判定点：句柄的代数必须与当前装载一致。
func (l *Loader) checkLive(tool *pluginTool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errorf("plugin_disabled", http.StatusConflict, false,
			"插件运行时已关闭（宿主正在退出）：不要重试同一个调用",
			"插件 %s 的工具 %q 已随运行时关闭失效", tool.pluginID, tool.name)
	}
	loaded := l.byID[tool.pluginID]
	if loaded == nil || loaded.info.Gen != tool.gen {
		return errorf("plugin_disabled", http.StatusConflict, false,
			"插件已被禁用/卸载：不要重试同一个调用；需要时先 plugin.enable 再调",
			"插件 %s 的工具 %q 已随卸载失效（这个句柄不会再执行任何 wasm）", tool.pluginID, tool.name)
	}
	return nil
}

// pluginTool 是 spi.Tool 在私有包上的实现。
//
// 身份（Name/Description/Schema/Caps）全部来自包 manifest，只有 Exec 真的跨到
// 客人那边。一个模块可以挂多个工具（客人按请求信封的 tool 字段分流），
// 所以"我是谁"必须每次调用跟着请求走（wasmhost.ToolRouter）。
type pluginTool struct {
	loader   *Loader
	pluginID string
	gen      int64
	name     string
	desc     string
	schema   json.RawMessage
	caps     spi.Caps
	timeout  time.Duration
	router   *wasmhost.ToolRouter
}

func (t *pluginTool) Name() string            { return t.name }
func (t *pluginTool) Description() string     { return t.desc }
func (t *pluginTool) Schema() json.RawMessage { return t.schema }
func (t *pluginTool) Caps() spi.Caps          { return t.caps }

// Exec 走一次 ABI，并把三层失败收成模型能看懂的两类（与 wasmhost 的约定一一对应）。
//
// R3：失败必须 `Result{Hint:…}, err`，不许返回空 Result 把 Hint 丢掉。
func (t *pluginTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	if err := t.loader.checkLive(t); err != nil {
		return spi.Result{
			Summary: fmt.Sprintf("插件 %s 的工具 %s 不可用", t.pluginID, t.name),
			Hint:    "这个插件已经被禁用或卸载：不要重试同一个调用；需要时先 plugin.enable 再调",
		}, err
	}
	out, err := t.router.Invoke(ctx, t.name, t.timeout, args)
	if err != nil {
		return spi.Result{
				Summary: fmt.Sprintf("插件 %s 的工具 %s 执行失败", t.pluginID, t.name),
				Hint:    wasmhost.HintForError(err),
			},
			fmt.Errorf("插件 %s 的工具 %q 执行失败: %w", t.pluginID, t.name, err)
	}
	reply, perr := wasmhost.ParseReply(out)
	if perr != nil {
		return spi.Result{
				Summary: fmt.Sprintf("插件 %s 的工具 %s 返回了无法识别的结果", t.pluginID, t.name),
				Hint:    "插件返回的信封不符合 ABI（宿主侧问题）：不要重试，把这条错误报告给用户",
			},
			fmt.Errorf("插件 %s 的工具 %q 的结果信封不合法: %w", t.pluginID, t.name, perr)
	}
	if reply.IsError {
		hint := strings.TrimSpace(reply.Hint)
		if hint == "" {
			hint = "工具报告失败但没给 hint：按 summary/text 的说法改参数再试；同样的参数重试不会有不同结果"
		}
		summary := strings.TrimSpace(reply.Summary)
		if summary == "" {
			summary = "工具报告失败"
		}
		message := summary
		if text := strings.TrimSpace(reply.Text); text != "" && text != summary {
			message = summary + "：" + text
		}
		return spi.Result{Summary: summary, Text: reply.Text, Hint: hint}, errors.New(message)
	}
	return spi.Result{Summary: reply.Summary, Text: reply.Text}, nil
}

func hasExport(exports []string, want string) bool {
	for _, name := range exports {
		if name == want {
			return true
		}
	}
	return false
}
