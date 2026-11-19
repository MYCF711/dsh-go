// Package nodeprovider 是 PTC 执行缝的**外部受管 Node** provider：把一次
// 程序运行交给一个按次起停的 node 子进程执行，宿主本身不解释一行 JS。
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
// # 受管意味着什么（每一条都有测试钉着）
//
//   - **可按次杀**：per-call 模式一次运行一个进程；结束/超时/取消都收整棵进程树
//     （Windows 用 Job Object，见 process_windows.go）。
//   - **超时**：elapsed 预算到了就停进程，结果是 `timeout` 并**保留已捕获输出**。
//   - **取消**：调用方 ctx 取消 → 硬停，结果是 `abort`。
//   - **有界**：输出字节、单帧字节、V8 老年代堆都设上限（超了报 `output-limit`）。
//
// # 未做的部分（如实登记，不假装支持）
//
//   - 只有 per-call（`function-body`）一种求值形态。`repl` / `script` 要 `node:vm`
//     的保留环境与状态生命周期，没实现，`Resolve` 见到就拒。
//   - 没有文件 confinement：`SandboxMode()` 报 false，显式档位请求被拒。
//     换句话说，程序里 `require('node:fs')` 是按**进程权限**生效的，不是按会话档位。
package nodeprovider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dshgo/internal/ptc"
	"dshgo/internal/sandbox"
)

// DefaultConfig 返回官方 `dsh-ptc-runtime-node` README 里的那组默认值。
//
// 逐条对应 README 的配置表（`executionMode` / `timeoutMs` / `maxTimeoutMs` /
// `maxOutputBytes` / `maxOldGenerationSizeMb` / `maxMessageBytes` /
// `maxPendingCalls` / `stopGraceMs` / `graceMs`）。
func DefaultConfig() Config {
	return Config{
		ExecutionMode:          ModePerCall,
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

// ExecutionMode 是 provider 的求值形态。
type ExecutionMode string

const (
	// ModePerCall：每次运行一个全新进程，程序是 async 函数体。本 provider 实现的就是它。
	ModePerCall ExecutionMode = "per-call"
	// ModeVMScript：`node:vm` Script，支持保留环境。**未实现**。
	ModeVMScript ExecutionMode = "vm-script"
	// ModePersistentInMemory：按 owner 保留 REPL。**未实现**。
	ModePersistentInMemory ExecutionMode = "persistent-in-memory"
)

// Config 是 provider 配置。
type Config struct {
	// ExecutionMode 默认 per-call；其余两种会被 New 明确拒绝（不是静默降级）。
	ExecutionMode ExecutionMode
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

// Provider 是 ptc.Runtime 的 Node 实现。
//
// 并发安全：一个 Provider 可以被多个 goroutine 同时 Run——每次 Run 起自己的进程，
// 共享的只有配置与 worker 脚本路径。
type Provider struct {
	cfg    Config
	dir    string
	script string

	once     sync.Once
	nodePath string
	nodeErr  error
}

// New 建一个 provider：写好 worker 脚本（当数据文件），不启动任何进程。
//
// 未实现的执行形态在**装配时**就拒，而不是等到第一次运行——静默降级成 per-call
// 会让"我明明开了 vm-script 保留状态"变成一个查不出原因的错觉。
func New(cfg Config) (*Provider, error) {
	cfg = cfg.normalized()
	switch cfg.ExecutionMode {
	case ModePerCall:
	case ModeVMScript, ModePersistentInMemory:
		return nil, fmt.Errorf("nodeprovider: executionMode %q 需要 node:vm 的保留环境，本 provider 只实现 per-call（不静默降级）", string(cfg.ExecutionMode))
	default:
		return nil, fmt.Errorf("nodeprovider: 未知 executionMode %q", string(cfg.ExecutionMode))
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
	return &Provider{cfg: cfg, dir: dir, script: script}, nil
}

// Close 删掉 worker 脚本的临时目录。在途运行不受影响（它们已各自把脚本读进内核）。
func (p *Provider) Close() error {
	if p == nil || p.dir == "" {
		return nil
	}
	dir := p.dir
	p.dir = ""
	return os.RemoveAll(dir)
}

// Language 是官方 `PtcRuntime.language`：这个 provider 跑 TypeScript。
func (p *Provider) Language() string { return "typescript" }

// Isolation 是官方 `PtcRuntime.isolation`：独立进程。诊断描述，不是安全承诺。
func (p *Provider) Isolation() string { return "process" }

// ProgramFormat：per-call 模式下程序是 async 函数体。
func (p *Provider) ProgramFormat() ptc.ProgramFormat { return ptc.FormatFunctionBody }

// RetainsState：per-call 每次全新进程，不保留。
func (p *Provider) RetainsState() bool { return false }

// Release：不保留状态的 provider 没有可释放的东西。
func (p *Provider) Release(ctx context.Context, owner any) error {
	_ = ctx
	_ = owner
	return nil
}

// ExecutionInstructions 是给模型的用法说明。语法/输出/状态三类规则归 provider
// 所有（官方也是这么分的：tool executor 不猜 provider 的语法）。
func (p *Provider) ExecutionInstructions() string {
	return "This runtime executes the body of an async TypeScript function in a fresh process for every call. " +
		"Top-level `await` and `return` work; declarations are local to the call and do not survive it. " +
		"Erasable TypeScript syntax only (no enum/namespace); annotations are advisory. " +
		"Return a lossless JSON value and/or print with console.log — nothing else is program output. " +
		"Host bindings are async and must be awaited; a failing binding rejects so you can catch it. " +
		"File and network access are those of a plain Node process: this runtime applies no file sandbox."
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

// ErrNodeUnavailable 让上层能把"机器上没 node"与"程序跑挂了"分开。
var ErrNodeUnavailable = errors.New("nodeprovider: node 不可用")
