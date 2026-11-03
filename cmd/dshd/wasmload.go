package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/internal/wasmhost"
)

// 这一层做两件事：**装载校验** 与 **装配成工具**。
//
// manifest 里写 `backend = "wasm"` 的模块，装配前会真的被 wasmhost 装起来：
//   - 文件不存在 / 不是 wasm / 编译不过 → 装配失败，错误里带路径与 wasmhost 的原始原因；
//   - 写了 digest 但和文件内容对不上 → 装配失败。这正是 manifest 校验存在的意义：
//     内容被换掉要在**启动时**炸，而不是等第一次调用才发现；
//   - 通过校验的模块登记进装配结果。
//
// 写不写 `provides = ["tool"]`，决定它到底提不提供能力：
//   - **写了** → 生成一个 kernel.Module，其 Register 往工具表里注册**一个** spi.Tool
//     （名字/描述/schema/caps/超时全部来自 manifest，不从客人取）。模型能看见它、
//     能调它，这就是"wasm 插件真的提供了一个工具"的落点。
//   - **没写** → 行为与这一层落地之前完全一致：Provides 为空、Register 为 nil、
//     不进工具表，只能 `dshd wasm call` 手动调。这条"不变"是硬要求：
//     没声明要提供工具的人，不该因为升级宿主就多出一个模型可见的工具。
//
// 为什么不把 digest 塞进 kernel.Module：kernel 的红线是"不识业务"，
// 也不该为了一次显示多一个字段；模块的身份（路径 / 摘要 / 导出）留在这一层。

// wasmToolCap 是 wasm 后端在 manifest 里唯一认的能力名。
//
// 为什么是 `"tool"`（单数）而不是内核服务键 `"tools"`（复数）：这是两个概念。
// "tools" 是**别人提供的服务**（工具目录），wasm 模块要 `Requires` 它才能注册；
// "tool" 是**本模块要贡献的东西**。混成一个名字会让模块看起来像在提供工具目录，
// 而它其实只是往里加一行。
const wasmToolCap = "tool"

// wasmLoaded 是本次装配里真正装起来的一个 wasm 模块。
//
// module 句柄留在这里而不是用完就关：装载意味着它确实能被调用。
// 提供工具的模块，Register 闭包里直接握着同一个 *wasmhost.Module。
type wasmLoaded struct {
	id      string
	path    string
	digest  string
	exports []string
	module  *wasmhost.Module
	// tool 是它注册进工具表的名字；空表示不提供能力。
	tool string
}

var (
	wasmMu      sync.Mutex
	wasmCurrent []wasmLoaded
)

// wasmEntry 是 manifest 里 wasm 关心的那几个字段。
//
// 为什么不把 digest 加到 kernel.ModuleEntry：kernel 的红线是"不识业务"，
// digest 是 **wasm 文件内容**的身份，属于装载层；kernel 只需要 id/backend，
// 不需要知道这条路径后面躺着一个 .wasm。
//
// 代价是同一份 manifest 在这一层再解析一遍——只解析 [[module]] 行，
// 读的是同一份字节，不存在"两份真相"。
type wasmEntry struct {
	ID       string   `toml:"id"`
	Backend  string   `toml:"backend"`
	Path     string   `toml:"path"`
	Digest   string   `toml:"digest"`
	Enabled  *bool    `toml:"enabled"`
	Provides []string `toml:"provides"`

	// 以下五个只在 provides 含 "tool" 时有意义。
	//
	// 为什么不从客人那里问：spi.Tool 的 Name/Description/Schema/Caps 是
	// **稳定性契约**（R4），审批与沙箱也靠 Caps 判定（AGENTS §2）。
	// 让客人自己声明"我只会读"，等于让它自己给自己发许可证。
	// 所以这些只有 manifest 一个来源，改动要走显式审查。
	ToolName        string   `toml:"tool_name"`
	ToolDescription string   `toml:"tool_description"`
	ToolSchema      string   `toml:"tool_schema"`
	ToolCaps        []string `toml:"tool_caps"`
	TimeoutMS       int      `toml:"timeout_ms"`
}

type wasmManifest struct {
	Modules []wasmEntry `toml:"module"`
}

// readWasmEntries 只把 manifest 里的 [[module]] 行读出来。文件不存在 = 没有 wasm 模块。
func readWasmEntries(manifestPath string) ([]wasmEntry, error) {
	if strings.TrimSpace(manifestPath) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 没有 manifest 不是错误（与 kernel.LoadManifest 一致）
		}
		return nil, fmt.Errorf("读 manifest %s 失败: %w", manifestPath, err)
	}
	var f wasmManifest
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析 manifest %s 的 wasm 段失败: %w", manifestPath, err)
	}
	return f.Modules, nil
}

// loadWasmModules 装载并校验 manifest 里所有 backend = "wasm" 的模块，
// 返回要追加进候选列表的 kernel.Module。
//
// 返回的模块必须在 Apply **之前**并进候选：Apply 按 id 命中，
// 不在候选里的 id 会被判成"没有编译进二进制"——那正是这次要拆掉的那堵墙。
func loadWasmModules(manifestPath string) ([]kernel.Module, error) {
	entries, err := readWasmEntries(manifestPath)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		replaceWasm(nil)
		return nil, nil
	}
	base := manifestDirOf(manifestPath)
	ctx := context.Background()

	var (
		mods   []kernel.Module
		loaded []wasmLoaded
	)
	// fail 统一收尾：装到一半失败的模块也要关掉，不留 runtime、不留文件句柄。
	fail := func(err error) ([]kernel.Module, error) {
		closeWasmModules(loaded)
		return nil, err
	}

	for _, e := range entries {
		if kernel.Backend(e.Backend) != kernel.BackendWasm {
			continue
		}
		if e.ID == "" {
			// 缺 id 的报错归 Apply，这里不抢它的台词。
			continue
		}
		// enabled = false 就是真的关掉：连文件都不读。
		// 一个被禁用的模块不该因为路径坏了把整个启动拖下水。
		//
		// 但 id 仍要登记进候选：Apply 是"先按 id 找模块、再判断 enabled"，
		// 不登记的话它会把"我写了这行、这次关掉"报成"没编译进二进制"。
		if e.Enabled != nil && !*e.Enabled {
			mods = append(mods, kernel.Module{ID: e.ID, Backend: kernel.BackendWasm})
			continue
		}
		providesTool, unknown := splitWasmProvides(e.Provides)
		if len(unknown) > 0 {
			return fail(fmt.Errorf(
				"模块 %q 声明了 backend = \"wasm\" 和 provides = %v，但 wasm 后端只认 provides = [\"tool\"]："+
					"这一层做的是「一个 wasm 模块 = 一个工具」，%v 这些能力还没有对应实现。"+
					"在它们落地之前写进 manifest，就是在骗下一个读 manifest 的人",
				e.ID, e.Provides, unknown))
		}

		// 先校验工具声明，再装载 4 MiB 的 wasm：manifest 写错了没必要先编译一遍。
		var spec *wasmToolSpec
		if providesTool {
			s, serr := newWasmToolSpec(e)
			if serr != nil {
				return fail(serr)
			}
			spec = s
		}

		p := strings.TrimSpace(e.Path)
		if p == "" {
			return fail(fmt.Errorf(
				"模块 %q 声明了 backend = \"wasm\"，但没有 path：wasm 后端必须给出 .wasm 文件路径", e.ID))
		}
		if !filepath.IsAbs(p) {
			// 相对路径按 manifest 所在目录解析，不是按当前工作目录：
			// 否则同一条命令换个 cwd 跑，指到的就是另一个文件。
			p = filepath.Join(base, p)
		}
		// 再取绝对路径：报错和 `modules graph` 里都能一眼看出到底读了哪个文件，
		// 而不是一个要靠 cwd 去补全的相对路径。
		if abs, aerr := filepath.Abs(p); aerr == nil {
			p = abs
		}

		m, err := wasmhost.Load(ctx, e.ID, p)
		if err != nil {
			return fail(fmt.Errorf("装配 wasm 模块 %q 失败（%s）: %w", e.ID, p, err))
		}
		actual := m.Digest()
		exports := m.Exports()
		entry := wasmLoaded{id: e.ID, path: p, digest: actual, exports: exports, module: m}
		if spec != nil {
			// 工具名与超时是**执行期**参数，直接记在模块上：
			// 请求信封里的 tool 字段与单次调用的 deadline 都从这里取。
			m.SetToolName(spec.name)
			m.SetTimeout(spec.timeout)
			entry.tool = spec.name
		}
		// 先登记再校验：任何一处校验失败，fail 都会把它一起关掉。
		loaded = append(loaded, entry)

		if want := strings.ToLower(strings.TrimSpace(e.Digest)); want != "" {
			if len(want) != 64 {
				return fail(fmt.Errorf(
					"模块 %q 的 digest 不是 64 位十六进制 sha256（写的是 %q，长度 %d）",
					e.ID, e.Digest, len(want)))
			}
			if _, herr := hex.DecodeString(want); herr != nil {
				return fail(fmt.Errorf(
					"模块 %q 的 digest 不是十六进制（写的是 %q）: %v", e.ID, e.Digest, herr))
			}
			if want != actual {
				return fail(fmt.Errorf(
					"模块 %q 的 digest 不匹配（摘要校验失败）：manifest 写的是 %s，文件 %s 实际的 sha256 是 %s。"+
						"内容被换过就该在启动时炸，而不是等第一次调用才发现",
					e.ID, want, p, actual))
			}
		}

		if spec != nil {
			mods = append(mods, kernel.Module{
				ID:      e.ID,
				Backend: kernel.BackendWasm,
				// Requires 必须有 "tools"：工具目录是**别人**提供的服务，
				// 没有它，Register 里的 Inject 会失败；拓扑排序也靠它保证顺序。
				//
				// Provides 这里不写：能力的唯一来源是 manifest，Apply 会把
				// e.Provides（["tool"]）原样复制上来——`modules graph` 的 PROVIDES 列
				// 因此能看到这一行。注意 "tool" 不是服务键：真正的注册靠下面这个 Register，
				// 它 Inject 的是 spi 的 "tools" 服务。两件事别混。
				Requires: []string{spi.ToolsKey.String()},
				Register: wasmToolRegister(spec, m),
			})
			slog.Info("wasm 模块已装载，并注册工具（模型可见）",
				"module", e.ID, "tool", spec.name, "path", p,
				"digest", shortDigest(actual), "exports", strings.Join(exports, ","),
				"timeout", spec.timeout)
			continue
		}

		mods = append(mods, kernel.Module{
			ID:      e.ID,
			Backend: kernel.BackendWasm,
			// Provides 必须为空：它不提供能力这件事要在**装配结果里**就是真的，
			// 不能只靠日志里一句话。
		})

		slog.Warn("wasm 模块已装载并通过校验，但**不提供任何能力**：不进工具表、模型调不到，只能 `dshd wasm call` 手动调"+
			"（要让它提供工具，在 manifest 里写 provides = [\"tool\"] 并给出 tool_name / tool_description / tool_schema）",
			"module", e.ID, "path", p, "digest", shortDigest(actual),
			"exports", strings.Join(exports, ","))
	}

	replaceWasm(loaded)
	return mods, nil
}

// splitWasmProvides 把 manifest 的 provides 拆成「要工具」与「不认识的能力」两半。
func splitWasmProvides(provides []string) (tool bool, unknown []string) {
	for _, c := range provides {
		if c == wasmToolCap {
			tool = true
			continue
		}
		unknown = append(unknown, c)
	}
	return tool, unknown
}

// wasmToolRegister 返回把工具注册进工具表的 Register。
//
// 为什么 Register 要闭包握着 *wasmhost.Module：装配层只能给 *kernel.Scope（R2），
// 模块句柄不能放进 kernel；闭包是唯一既守住 R2 又能拿到句柄的形状。
func wasmToolRegister(spec *wasmToolSpec, m *wasmhost.Module) func(*kernel.Scope) error {
	tool := &wasmTool{
		mod:    m,
		name:   spec.name,
		desc:   spec.desc,
		schema: spec.schema,
		caps:   spec.caps,
	}
	return func(s *kernel.Scope) error {
		reg, err := kernel.Inject(s, spi.ToolsKey)
		if err != nil {
			return err
		}
		if err := reg.Register(tool); err != nil {
			return fmt.Errorf("wasm 工具 %q 注册失败: %w", tool.Name(), err)
		}
		return nil
	}
}

// wasmToolSpec 是 manifest 里对一个 wasm 工具的全部声明。
//
// 每个字段都**只能**从 manifest 来：工具的 Name/Description/Schema/Caps 是
// 稳定性契约（R4），审批与沙箱也靠 Caps 判定（AGENTS §2）——
// 让客人自己声明"我只会读"，等于让它自己给自己发许可证。
type wasmToolSpec struct {
	name    string
	desc    string
	schema  json.RawMessage
	caps    spi.Caps
	timeout time.Duration
}

// newWasmToolSpec 校验并组装工具声明。任何一处不对都在**装配时**炸。
//
// 为什么全都必填：漏了 tool_name 就是模型看到一个空名字的工具；
// 漏了 tool_description 就是模型不知道什么时候该用它；
// 漏了 tool_schema 就是参数没法校验。这些错误在运行时表现为"模型不会用这个工具"，
// 极难归因，所以放在启动路径上失败。
func newWasmToolSpec(e wasmEntry) (*wasmToolSpec, error) {
	name := strings.TrimSpace(e.ToolName)
	if name == "" {
		return nil, fmt.Errorf(
			`模块 %q 写了 provides = ["tool"]，但没有 tool_name：工具名是模型看到的名字，必须显式给出（例：tool_name = "echo"）`, e.ID)
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return nil, fmt.Errorf(
			"模块 %q 的 tool_name = %q 里有空白字符：工具名要能被模型原样写进 tool_calls，不能带空格", e.ID, name)
	}

	desc := strings.TrimSpace(e.ToolDescription)
	if desc == "" {
		return nil, fmt.Errorf(
			"模块 %q 的工具 %q 没有 tool_description：Description 是模型看到的「什么时候用它」，"+
				"缺了它模型只能靠猜，几乎不会正确调用", e.ID, name)
	}

	raw := strings.TrimSpace(e.ToolSchema)
	if raw == "" {
		return nil, fmt.Errorf(
			`模块 %q 的工具 %q 没有 tool_schema：参数 schema 必须写在 manifest 里（例：`+
				`tool_schema = '''{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}'''）`, e.ID, name)
	}
	// 必须是**对象**：工具参数永远是一个 JSON 对象，顶层不能是数组或标量。
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil || probe == nil {
		return nil, fmt.Errorf(
			"模块 %q 的工具 %q 的 tool_schema 不是合法的 JSON 对象：%v（原文 %q）", e.ID, name, err, raw)
	}

	caps, err := parseWasmCaps(e.ToolCaps)
	if err != nil {
		return nil, fmt.Errorf("模块 %q 的工具 %q: %w", e.ID, name, err)
	}

	timeout := wasmhost.DefaultTimeout
	if e.TimeoutMS < 0 {
		return nil, fmt.Errorf("模块 %q 的 timeout_ms = %d，不能是负数", e.ID, e.TimeoutMS)
	}
	if e.TimeoutMS > 0 {
		timeout = time.Duration(e.TimeoutMS) * time.Millisecond
	}

	return &wasmToolSpec{
		name: name, desc: desc,
		schema:  json.RawMessage(raw),
		caps:    caps,
		timeout: timeout,
	}, nil
}

// parseWasmCaps 把 manifest 的 tool_caps 翻成 spi.Caps。
//
// 默认全 false 不是客人的自我介绍，而是**沙箱强制**的：宿主不给客人任何
// FS preopen、不传 args/env、只挂 wasi_snapshot_preview1 的最小面。
// manifest 仍然可以声明更严的能力（比如一个算完要人批的工具写 destructive），
// 那只是让审批更早介入，不是说客人真能碰到文件系统。
func parseWasmCaps(names []string) (spi.Caps, error) {
	var c spi.Caps
	for _, n := range names {
		switch strings.ToLower(strings.TrimSpace(n)) {
		case "":
			// TOML 里多写一个空串不算错，忽略。
		case "writes":
			c.Writes = true
		case "executes":
			c.Executes = true
		case "network":
			c.Network = true
		case "destructive":
			c.Destructive = true
		default:
			return c, fmt.Errorf(
				"tool_caps 里有不认识的能力 %q：只认 writes / executes / network / destructive"+
					"（与 spi.Caps 的四个字段一一对应，拼错会让审批判错档）", n)
		}
	}
	return c, nil
}

// wasmTool 是 spi.Tool 在 wasm 上的实现。
//
// 身份全部来自 manifest，只有 Exec 真的跨到客人那边。
type wasmTool struct {
	mod    *wasmhost.Module
	name   string
	desc   string
	schema json.RawMessage
	caps   spi.Caps
}

func (t *wasmTool) Name() string            { return t.name }
func (t *wasmTool) Description() string     { return t.desc }
func (t *wasmTool) Schema() json.RawMessage { return t.schema }
func (t *wasmTool) Caps() spi.Caps          { return t.caps }

// Exec 走一次 ABI，并把三层失败收成模型能看懂的两类。
//
// 分层（与 wasmhost 的约定一一对应）：
//   - 客人 trap / 协议错 / 超时 → 插件坏了，不是参数错。Hint 明说"别原样重试"；
//   - 结果信封 isError:true → **业务失败**：Hint 直接来自客人，
//     客人没写就由这里兜一条可行动的，绝不留空。
//
// R3：失败必须 `Result{Hint:…}, err`，不许返回空 Result 把 Hint 丢掉——
// 模型拿不到 Hint 就只能瞎猜，一次失败会变成三四次无效重试。
func (t *wasmTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	out, err := t.mod.InvokeTool(ctx, args)
	if err != nil {
		return spi.Result{
				Summary: fmt.Sprintf("wasm 插件 %s 执行失败", t.name),
				Hint:    hintForWasmError(err),
			},
			fmt.Errorf("wasm 工具 %q 执行失败: %w", t.name, err)
	}

	rep, perr := wasmhost.ParseReply(out)
	if perr != nil {
		return spi.Result{
				Summary: fmt.Sprintf("wasm 插件 %s 返回了无法识别的结果", t.name),
				Hint:    "插件返回的信封不符合 ABI（宿主侧问题）：不要重试，把这条错误报告给用户",
			},
			fmt.Errorf("wasm 工具 %q 的结果信封不合法: %w", t.name, perr)
	}

	if rep.IsError {
		hint := strings.TrimSpace(rep.Hint)
		if hint == "" {
			// 客人没给 hint 也要给模型一条可行动的：这正是 R3 要拦的形态。
			hint = "工具报告失败但没给 hint：按 summary/text 的说法改参数再试；同样的参数重试不会有不同结果"
		}
		summary := strings.TrimSpace(rep.Summary)
		if summary == "" {
			summary = "工具报告失败"
		}
		// err 的文本会被 agent 层投影成 "Error: …" 直接给模型看，
		// 所以它必须带上客人说的正文，而不是一句 "tool failed"。
		msg := summary
		if text := strings.TrimSpace(rep.Text); text != "" && text != summary {
			msg = summary + "：" + text
		}
		return spi.Result{Summary: summary, Text: rep.Text, Hint: hint}, errors.New(msg)
	}

	// 成功：Text 是客人渲染好的正文，projection 会原样用它。
	return spi.Result{Summary: rep.Summary, Text: rep.Text}, nil
}

// hintForWasmError 把 wasmhost 的错误翻成模型能执行的下一步。
//
// 为什么不用一句话概括所有错误：这四类的**正确反应完全不同**——
// trap 要换做法、超时缩小输入、取消重发、协议错只能报人。
// 给错方向比不给更糟：模型会拿着一句"重试"把同一个坑踩三遍。
func hintForWasmError(err error) string {
	switch {
	case errors.Is(err, wasmhost.ErrGuestTrap):
		return "插件内部崩溃（wasm trap），不是你的参数错了：不要原样重试；换一种做法，或改用别的工具"
	case errors.Is(err, context.DeadlineExceeded):
		return "插件执行超时：输入可能太大，缩小范围再试；连续超时要报告给用户（超时后实例已被关闭，需要重启宿主）"
	case errors.Is(err, context.Canceled):
		return "这次调用被取消（会话被中断）：需要时重新发起，不要把它当成插件失败"
	case errors.Is(err, wasmhost.ErrProtocol):
		return "插件与宿主的 ABI 对不上（宿主侧问题）：不要重试，把这条错误报告给用户"
	default:
		return "wasm 插件调用失败：先按错误原文判断，别反复重试同样的参数"
	}
}

// replaceWasm 换掉"当前装配里的 wasm 模块"，并关掉上一批。
//
// 为什么敢直接关旧的：这些句柄没有任何出口——模块 Provides 为空、没有 Register，
// 拿不到模块指针的调用方不可能还在用它。
func replaceWasm(next []wasmLoaded) {
	wasmMu.Lock()
	old := wasmCurrent
	wasmCurrent = next
	wasmMu.Unlock()
	closeWasmModules(old)
}

// closeLoadedWasm 卸载本次装配装起来的全部 wasm 模块。可重复调用。
//
// 谁该调：打印完就退出的命令（`modules graph|list`）。进程活着的命令
// （serve / acp / run）不调——模块本来就该活到进程结束。
func closeLoadedWasm(ctx context.Context) {
	wasmMu.Lock()
	cur := wasmCurrent
	wasmCurrent = nil
	wasmMu.Unlock()
	closeWasmModulesWith(ctx, cur)
}

func closeWasmModules(list []wasmLoaded) {
	closeWasmModulesWith(context.Background(), list)
}

func closeWasmModulesWith(ctx context.Context, list []wasmLoaded) {
	for _, x := range list {
		if x.module == nil {
			continue
		}
		if err := x.module.Close(ctx); err != nil {
			slog.Debug("关闭 wasm 模块失败", "module", x.id, "err", err)
		}
	}
}

// loadedWasm 返回本次装配里装起来的 wasm 模块（按 manifest 顺序）。
func loadedWasm() []wasmLoaded {
	wasmMu.Lock()
	defer wasmMu.Unlock()
	out := make([]wasmLoaded, len(wasmCurrent))
	copy(out, wasmCurrent)
	return out
}

// writeWasmSection 把 wasm 模块单独打一节：backend、digest 前 12 位、导出的工具名。
//
// 为什么单独一节而不是塞进主表：主表的列是"装配后提供哪些**能力（服务键）**"，
// 而 wasm 模块的 Provides 是**空的**——工具不是一个服务键，它是往工具表里加一行。
// 混进主表，读的人会以为它提供了 "tools" 这个服务（那是别人提供的）。
// TOOL 列就是这条区分：写 provides=["tool"] 的模块在这里显示工具名，
// 其余显示"（不提供）"，一眼能分出这两类。
func writeWasmSection(w io.Writer) {
	list := loadedWasm()
	if len(list) == 0 {
		return
	}
	fmt.Fprintf(w, "\n# wasm 后端（已装载 / 已校验；TOOL 列 = 注册进工具表的工具名，"+
		"空表示不提供能力、只能 `dshd wasm call` 手动调）\n")
	fmt.Fprintf(w, "%-22s %-9s %-12s %-18s %-22s %s\n", "MODULE", "BACKEND", "DIGEST", "EXPORTS", "TOOL", "PATH")
	for _, x := range list {
		exports := strings.Join(x.exports, ",")
		if exports == "" {
			exports = "（无导出函数）"
		}
		tool := x.tool
		if tool == "" {
			tool = "（不提供）"
		}
		fmt.Fprintf(w, "%-22s %-9s %-12s %-18s %-22s %s\n",
			x.id, string(kernel.BackendWasm), shortDigest(x.digest), exports, tool, x.path)
	}
}

// shortDigest 取 sha256 的前 12 位十六进制：够认出一个文件，又不至于把行撑爆。
func shortDigest(d string) string {
	if len(d) <= 12 {
		return d
	}
	return d[:12]
}

// manifestDirOf 是 manifest 里相对 path 的解析基准目录。
func manifestDirOf(manifestPath string) string {
	if strings.TrimSpace(manifestPath) == "" {
		return "."
	}
	d := filepath.Dir(manifestPath)
	if d == "" {
		return "."
	}
	return d
}
