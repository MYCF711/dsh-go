// Package nodeprovider 是 PTC 执行缝的**外部受管 Node** provider：把一次
// 程序运行交给一个受管的 node 子进程执行，宿主本身不解释一行 JS。
//
// # 为什么不违反 R6
//
// R6 的原文是"宿主**不嵌** JS 运行时"。这里宿主对 JS 的角色和对任何一个数据文件
// 一样：把一段 JS **文本**写到临时目录，然后起 `node <那个文件>`。没有链接引擎、
// 没有 fork/vendor 任何官方 JS 包、没有 mvdan 之类的 shell 执行。
//
// 这与既有裁定同形（`dynamicCordisRunner`：Go 核心 + 外部受管 Node 双轨），
// 也与官方自己的形状同形：官方 `dsh-ptc-runtime-node` 同样是"宿主 + 受管 node
// 子进程 + 帧式控制通道"，只不过那个宿主本身是 Node。
//
// # 两种求值形态（与官方 `executionMode` 逐字同名）
//
//   - `per-call`（默认）：程序是 **async 函数体**（`format=function-body`），
//     每次运行一个全新进程，`await`/`return` 可用；声明随调用结束消失。
//     官方 `src/execution.ts:134-142`。
//   - `vm-script`：程序是 **Script 语句表**（`format=script`），在 `node:vm`
//     上下文里求值（`vm.createContext` + `new vm.Script(...).runInContext`），
//     与官方 `src/evaluators/vm-script.ts:13-61` 同形。顶层声明、函数与对象身份
//     在 `scriptPersistence: true` 时按 `stateOwner` 跨次保留（进程常驻）。
//     官方 `src/execution.ts:149-167` + `src/config.ts:10-13`。
//
// `persistent-in-memory`（官方是 Node REPL 求值器，`src/evaluators/repl.ts`）
// **未实现**，`New` 见到就拒——静默降级成别的东西会让"我明明开了保留状态"
// 变成查不出原因的错觉。
//
// # 隔离层级：做到哪一层、没做到哪一层（`node:vm` 不是安全沙箱）
//
// 官方 README 对 vm-script 的原话（`dsh-ptc-runtime-node/README.md:72`）：
// "Script syntax is non-strict unless the program opts into strict mode;
// **the context is not a security boundary**, and Node access remains subject
// to the process sandbox."（中文版 `README.zh.md:72`："context 不是安全边界，
// Node 访问仍受进程沙箱约束。"）
//
// 同一份 README 还把两件事分开写（`:218`）："**Confinement inherits the selected
// backend's limits** — full and partial enforcement are reported separately;
// **sandbox policy and managed-process containment are distinct guarantees**."
//
// 所以本 provider 的防护**只来自进程与限额**，与 vm 上下文无关：
//
//   - **上下文隔离**（`vm-script` 有）：独立 global 与 realm、`require` 不在
//     globalThis 上因此不可见、顶层声明可跨次保留。这是**作用域隔离**。
//   - **不是安全隔离**：`createScriptContext` 把外层 `globalThis` 的自有属性
//     描述符原样拷进 context（官方 `vm-script.ts:36-39`），程序拿到的
//     `process`/`Buffer`/`fetch`/`setTimeout` 就是外层真实对象；`await import(...)`
//     用的是本进程的加载器；`process.exit()` 一样能杀进程。vm 里没有"逃逸"
//     这回事——它从来没被当成笼子。仓库纪律：**不许把它写成一个沙箱**。
//   - **有进程**：受管子进程，结束/超时/取消都收整棵进程树（Windows Job
//     Object，见 process_windows.go）。
//   - **有超时**：elapsed 预算到了就停进程，结果是 `timeout` 并**保留已捕获输出**。
//   - **有取消**：调用方 ctx 取消 → 硬停，结果是 `abort`。
//   - **有界**：输出字节、单帧字节、V8 老年代堆都设上限（超了报 `output-limit`）。
//   - **环境变量擦除**：程序可见的 `process.env` 只留官方
//     `STARTUP_ENVIRONMENT_NAMES`（PATH/PATHEXT/SYSTEMROOT/WINDIR/TEMP/TMP），
//     其余在求值前删掉（官方 `src/environment.ts:5,13-19`）。这是**减少暴露面**，
//     不是文件 confinement。
//   - **没有 OS 文件沙盒**：`SandboxMode()` 报 false，显式档位请求被拒；程序里的
//     `require('node:fs')` / `await import('node:fs/promises')` 按**进程权限**
//     生效，不是按会话档位。
package nodeprovider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"dshgo/internal/ptc"
	"dshgo/internal/sandbox"
)

// DefaultConfig 返回官方 `dsh-ptc-runtime-node` 的那组默认值。
//
// 逐条对应 `src/config.ts:35-50`（`executionMode` / `scriptPersistence` /
// `timeoutMs` / `maxTimeoutMs` / `maxOutputBytes` / `maxOldGenerationSizeMb` /
// `maxMessageBytes` / `maxPendingCalls` / `stopGraceMs` / `graceMs`）。
func DefaultConfig() Config {
	return Config{
		ExecutionMode:          ModePerCall,
		ScriptPersistence:      false,
		TimeoutMs:              120_000,
		MaxTimeoutMs:           600_000,
		MaxOutputBytes:         64 << 20,
		MaxOldGenerationSizeMb: 512,
		MaxMessageBytes:        128 << 20,
		MaxPendingCalls:        128,
		StopGrace:              3 * time.Second,
		Grace:                  3 * time.Second,
	}
}

// ExecutionMode 是 provider 的求值形态，取值与官方 `executionMode` 逐字相同。
type ExecutionMode string

const (
	// ModePerCall：每次运行一个全新进程，程序是 async 函数体（`function-body`）。
	ModePerCall ExecutionMode = "per-call"
	// ModeVMScript：`node:vm` 上下文里的 Script（`script`），可按 owner 常驻。
	ModeVMScript ExecutionMode = "vm-script"
	// ModePersistentInMemory：按 owner 保留 Node REPL。**未实现**（装配期拒绝）。
	ModePersistentInMemory ExecutionMode = "persistent-in-memory"
)

// Config 是 provider 配置。
type Config struct {
	// ExecutionMode 默认 per-call；persistent-in-memory 会被 New 明确拒绝。
	ExecutionMode ExecutionMode
	// ScriptPersistence 只在 vm-script 下有意义：true = 按 stateOwner 保留
	// 上下文与进程（官方 `src/config.ts:12`）。
	ScriptPersistence bool
	// NodePath 显式指定 node；空 = 从 PATH 找。
	NodePath string
	// WorkspaceRoot 是 Resolve 的默认 cwd。
	WorkspaceRoot string
	// TimeoutMs / MaxTimeoutMs 是数字预算的默认值与上限。
	TimeoutMs    int64
	MaxTimeoutMs int64
	// MaxOutputBytes 是日志 + 完成值 + 诊断的合计字节预算。
	MaxOutputBytes int64
	// MaxOldGenerationSizeMb 通过 `--max-old-space-size` 交给 V8。
	MaxOldGenerationSizeMb int64
	// MaxMessageBytes 是单帧上限（防止对端一发就把宿主撑爆）。
	MaxMessageBytes int64
	// MaxPendingCalls 是同时在途的绑定调用上限。
	MaxPendingCalls int
	// StopGrace 是"请合作停止"到"终止进程"的宽限。
	StopGrace time.Duration
	// Grace 是终止后的输出排空宽限。
	Grace time.Duration
}

func (c Config) normalized() Config {
	d := DefaultConfig()
	if c.ExecutionMode == "" {
		c.ExecutionMode = d.ExecutionMode
	}
	if c.TimeoutMs <= 0 {
		c.TimeoutMs = d.TimeoutMs
	}
	if c.MaxTimeoutMs <= 0 {
		c.MaxTimeoutMs = d.MaxTimeoutMs
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = d.MaxOutputBytes
	}
	if c.MaxOldGenerationSizeMb <= 0 {
		c.MaxOldGenerationSizeMb = d.MaxOldGenerationSizeMb
	}
	if c.MaxMessageBytes <= 0 {
		c.MaxMessageBytes = d.MaxMessageBytes
	}
	if c.MaxPendingCalls <= 0 {
		c.MaxPendingCalls = d.MaxPendingCalls
	}
	if c.StopGrace <= 0 {
		c.StopGrace = d.StopGrace
	}
	if c.Grace <= 0 {
		c.Grace = d.Grace
	}
	return c
}

// validate 复刻官方 `src/config.ts:56-57` 的装配期校验，并额外拒未实现的形态。
//
// `scriptPersistence` 与形态绑死是官方的规则（"scriptPersistence requires
// executionMode: vm-script"）：别的形态没有"按 owner 保留的上下文"这个东西，
// 接受它等于承诺一个不存在的语义。
func (c Config) validate() error {
	switch c.ExecutionMode {
	case ModePerCall:
		if c.ScriptPersistence {
			return fmt.Errorf("nodeprovider: scriptPersistence 需要 executionMode %q（官方 config.ts:57），当前是 %q", string(ModeVMScript), string(c.ExecutionMode))
		}
		return nil
	case ModeVMScript:
		return nil
	case ModePersistentInMemory:
		return fmt.Errorf("nodeprovider: executionMode %q 需要 Node REPL 求值器（官方 evaluators/repl.ts），本 provider 未实现（不静默降级）", string(c.ExecutionMode))
	default:
		return fmt.Errorf("nodeprovider: 未知 executionMode %q", string(c.ExecutionMode))
	}
}

// formatFor 把形态映射成程序格式（官方 `src/execution.ts:132-167` 的同一张表）。
func formatFor(mode ExecutionMode) ptc.ProgramFormat {
	if mode == ModeVMScript {
		return ptc.FormatScript
	}
	return ptc.FormatFunctionBody
}

// Provider 是 ptc.Runtime 的 Node 实现。
//
// 并发安全：一个 Provider 可以被多个 goroutine 同时 Run——每次 Run 起自己的进程
// （或按 stateOwner 复用它自己的常驻进程），共享的只有配置与 worker 脚本路径。
// 同一个 stateOwner 上**同时只允许一次运行**（官方 `worker.ts:76-79` 的 assertIdle）。
type Provider struct {
	cfg     Config
	mode    ExecutionMode
	format  ptc.ProgramFormat
	retains bool
	dir     string
	script  string

	once     sync.Once
	nodePath string
	nodeErr  error

	retainedMu sync.Mutex
	retained   map[string]*session
	closed     bool
}

// New 建一个 provider：写好 worker 脚本（当数据文件），不启动任何进程。
//
// 未实现的执行形态在**装配时**就拒，见 Config.validate。
func New(cfg Config) (*Provider, error) {
	cfg = cfg.normalized()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "dsh-ptc-node-*")
	if err != nil {
		return nil, fmt.Errorf("nodeprovider: 建临时目录失败: %w", err)
	}
	script := filepath.Join(dir, "dsh-ptc-worker.cjs")
	if err := os.WriteFile(script, []byte(workerSource), 0o644); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("nodeprovider: 写 worker 脚本失败: %w", err)
	}
	return &Provider{
		cfg:      cfg,
		mode:     cfg.ExecutionMode,
		format:   formatFor(cfg.ExecutionMode),
		retains:  cfg.ExecutionMode == ModeVMScript && cfg.ScriptPersistence,
		dir:      dir,
		script:   script,
		retained: map[string]*session{},
	}, nil
}

// Close 终止所有常驻会话并删掉 worker 脚本的临时目录。
//
// 在途的一次性运行不受影响（它们已各自把脚本读进内核）。
func (p *Provider) Close() error {
	if p == nil {
		return nil
	}
	p.retainedMu.Lock()
	p.closed = true
	sessions := make([]*session, 0, len(p.retained))
	for key, sess := range p.retained {
		sessions = append(sessions, sess)
		delete(p.retained, key)
	}
	dir := p.dir
	p.dir = ""
	p.retainedMu.Unlock()
	for _, sess := range sessions {
		sess.close()
	}
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}

// Language 是官方 `PtcRuntime.language`：这个 provider 跑 TypeScript。
func (p *Provider) Language() string { return "typescript" }

// Isolation 是官方 `PtcRuntime.isolation`：独立进程。诊断描述，不是安全承诺。
//
// `vm-script` 的上下文隔离**不改变**这一层：进程仍是唯一的执行底座。
func (p *Provider) Isolation() string { return "process" }

// ProgramFormat：per-call → function-body；vm-script → script（官方同一张表）。
func (p *Provider) ProgramFormat() ptc.ProgramFormat { return p.format }

// RetainsState：per-call 每次全新进程；vm-script 取决于 scriptPersistence。
func (p *Provider) RetainsState() bool { return p.retains }

// Release 释放某个 owner 的常驻环境并等待其进程清理。
//
// 官方 `src/execution.ts:119-125`。不保留状态的 provider 没有可释放的东西。
func (p *Provider) Release(ctx context.Context, owner any) error {
	_ = ctx
	if !p.retains || owner == nil {
		return nil
	}
	key, err := stateOwnerKey(owner)
	if err != nil {
		return err
	}
	p.releaseKey(key)
	return nil
}

// ExecutionInstructions 是给模型的用法说明。语法/输出/状态三类规则归 provider
// 所有（官方也是这么分的：tool executor 不猜 provider 的语法）。
func (p *Provider) ExecutionInstructions() string {
	if p.mode != ModeVMScript {
		return "This runtime executes the body of an async TypeScript function in a fresh process for every call. " +
			"Top-level `await` and `return` work; declarations are local to the call and do not survive it. " +
			"Erasable TypeScript syntax only (no enum/namespace); annotations are advisory. " +
			"Return a lossless JSON value and/or print with console.log — nothing else is program output. " +
			"Host bindings are async and must be awaited; a failing binding rejects so you can catch it. " +
			"File and network access are those of a plain Node process: this runtime applies no file sandbox."
	}
	lifetime := "Each call runs in a fresh Node process."
	filePolicy := "Direct filesystem access follows this execution's process identity; process.env starts empty apart from startup paths."
	if p.retains {
		lifetime = "Variables, functions, and imported modules remain in memory for this Agent across calls."
		filePolicy = "Direct filesystem access follows the environment's process identity; process.env starts empty apart from startup paths."
	}
	asynchronousFailure := "Await asynchronous work: an uncaught callback exception or unhandled rejection terminates the process"
	if p.retains {
		asynchronousFailure += " and clears its variables."
	} else {
		asynchronousFailure += "."
	}
	recovery := ""
	if p.retains {
		recovery = " Timeout and cancellation results say whether retained variables were preserved or cleared; this runtime clears them by terminating the process. Recreate missing variables and resources. Do not redeclare existing let, const, or class names."
	}
	return "The program contains TypeScript script statements; only erasable syntax is accepted, with no enum or namespaces. " +
		"Type annotations are advisory. Top-level declarations are supported; top-level return is not supported. " +
		"Emit output with console.log(...) or a final lossless JSON expression. The final expression is awaited if it is a Promise. " +
		"Only printed values and that final expression become program output. " + lifetime +
		" Use await import(...) for modules. Relative paths use the supplied working directory. " + filePolicy + " " +
		"Bare top-level `await` is not supported here: put the awaiting code in an async function and make its call the final expression, e.g. (async () => { ... })(). " +
		asynchronousFailure +
		" Values from bindings and imports can belong to another realm; use Array.isArray for arrays and the supplied binding error classes for failures, because instanceof Array or Error may be false." +
		recovery +
		" This runtime applies no OS file sandbox, and the node:vm context is not a security boundary: it isolates scope, not safety."
}

// SandboxMode：本 provider 不做文件 confinement，因此报 false（官方：
// "Deployment file-policy mode, or undefined for a provider without confinement support"）。
func (p *Provider) SandboxMode() (sandbox.Mode, bool) { return "", false }

// Timeout 是本 provider 的数字预算默认值与上限。
func (p *Provider) Timeout() (ptc.TimeoutSpec, bool) {
	return ptc.TimeoutSpec{DefaultMs: p.cfg.TimeoutMs, MaxMs: p.cfg.MaxTimeoutMs}, true
}

// Resolve 校验并补齐执行选项。
func (p *Provider) Resolve(req ptc.RunRequest) (ptc.RunSpec, error) {
	if strings.TrimSpace(req.Program) == "" {
		return ptc.RunSpec{}, ptc.Misuse("program 为空：要么给源码，要么别发起这次运行")
	}
	if err := ptc.ValidateBindings(req.Bindings); err != nil {
		return ptc.RunSpec{}, err
	}
	cwd, err := ptc.ResolveCwd(req.Cwd, p.defaultCwd())
	if err != nil {
		return ptc.RunSpec{}, err
	}
	if st, statErr := os.Stat(cwd); statErr != nil || !st.IsDir() {
		return ptc.RunSpec{}, ptc.Misuse("cwd %q 不是可用的目录：%v", cwd, statErr)
	}
	timeoutMs, err := ptc.ResolveTimeout(req.Timeout, true, ptc.TimeoutSpec{DefaultMs: p.cfg.TimeoutMs, MaxMs: p.cfg.MaxTimeoutMs})
	if err != nil {
		return ptc.RunSpec{}, err
	}
	mode, err := ptc.ResolveSandbox(req.SandboxMode, false)
	if err != nil {
		return ptc.RunSpec{}, err
	}
	return ptc.RunSpec{
		Program:     req.Program,
		StateOwner:  req.StateOwner,
		Bindings:    req.Bindings,
		Cwd:         cwd,
		TimeoutMs:   timeoutMs,
		SandboxMode: mode,
	}, nil
}

func (p *Provider) defaultCwd() string {
	if p.cfg.WorkspaceRoot != "" {
		return p.cfg.WorkspaceRoot
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// resolveNode 找 node 并**只探一次**（`node --version`）。结果缓存，失败也缓存：
// 每次运行都去探测只会把同一个"没有 node"变成一串超时。
func (p *Provider) resolveNode(ctx context.Context) (string, error) {
	p.once.Do(func() {
		p.nodePath, p.nodeErr = probeNode(ctx, p.cfg.NodePath)
	})
	return p.nodePath, p.nodeErr
}

func probeNode(ctx context.Context, explicit string) (string, error) {
	candidate := strings.TrimSpace(explicit)
	if candidate != "" {
		if st, err := os.Stat(candidate); err != nil || st.IsDir() {
			return "", fmt.Errorf("指定的 node 路径不存在或不是文件：%s", candidate)
		}
	} else {
		found, err := lookNode()
		if err != nil {
			return "", err
		}
		candidate = found
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := runVersion(probeCtx, candidate)
	if err != nil {
		return "", fmt.Errorf("node 跑不起来（%s）：%v", candidate, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "v") {
		return "", fmt.Errorf("%s 的输出不像 Node.js：%q", candidate, strings.TrimSpace(out))
	}
	return candidate, nil
}

// heapArg 是交给 V8 的堆上限参数。
func (p *Provider) heapArg() string {
	return "--max-old-space-size=" + strconv.FormatInt(p.cfg.MaxOldGenerationSizeMb, 10)
}

// stateOwnerKey 把 `stateOwner` 折成一个稳定的 key。
//
// 官方用 `WeakMap<object, ProgramWorker>`（`src/execution.ts:42`）：身份 = 对象引用。
// Go 没有 WeakMap，所以这里取"对象的身份"（指针/引用地址），并要求调用方用
// `Release` 显式释放（官方也有 release）。对可比较的值类型退化成按值当身份。
func stateOwnerKey(owner any) (string, error) {
	if owner == nil {
		return "", ptc.Misuse("stateOwner 为 nil")
	}
	value := reflect.ValueOf(owner)
	switch value.Kind() {
	// 只认真正带"对象身份"的引用类型。Slice 的 Pointer 是底层数组地址（空切片
	// 恒为 0），Func 的 Pointer 是代码地址（同一函数的闭包会撞在一起）——
	// 它们都不满足"同一个 owner 一个 key"。
	case reflect.Ptr, reflect.Map, reflect.Chan, reflect.UnsafePointer:
		return fmt.Sprintf("%T#%x", owner, value.Pointer()), nil
	}
	if value.Type().Comparable() {
		return fmt.Sprintf("%T=%v", owner, owner), nil
	}
	return "", ptc.Misuse("stateOwner 必须是有身份的对象（指针/引用）或可比较的值，收到 %T", owner)
}

// ErrNodeUnavailable 让上层能把"机器上没 node"与"程序跑挂了"分开。
var ErrNodeUnavailable = errors.New("nodeprovider: node 不可用")
