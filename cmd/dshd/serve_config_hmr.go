package main

// 官方 #4 `hmr` 的**profile 配置热重载**半边。
//
// 官方原件（`dsh-base/cordis.patch.yml:27-32`，逐字）：
//
//	# Profile configuration reloads by default; module roots are opt-in.
//	- id: hmr
//	  name: '@deepseek-ai/dsh-hmr'
//	  disabled: !!js "!ctx.get('profileContext')"
//	  config:
//	    root: []
//
// 也就是说官方的 HMR 有两半，默认开的是**前半**：
//
//   - **profile 配置热重载**（默认开）：改了 profile 的配置文档，运行中的
//     配置服务重新读一遍并通知消费者——不需要重启；
//   - **module roots 监听**（opt-in，`root: ["."]`）：监视源码目录、热替换模块。
//
// 本仓的缺口正好对应前半：`internal/pluginmgr/hmr.go` 只盯插件安装根
// （`<home>/plugins/packages/*/*/plugin.json`），而 `internal/config` 的
// `Store.Reload()` **没有任何监听者**——改配置树文件后进程内看到的还是旧树。
//
// 这一半在这里补齐，而且刻意做成**独立于插件运行时**的一个小组件：
//
//   - 只关心一个文件（配置树的 JSON），只调 `Store.Reload()`；
//   - 用 `Store.Revision()` 判"真的变了"，避免重复通知；
//   - 变更通过回调广播（`OnChange`），由装配点决定"谁需要知道"；
//   - 读盘失败**不改内存**（Reload 自己就是"先解析再替换"），回调拿到错误。
//
// 为什么不用 fsnotify：本仓零依赖纪律（`go.mod` 只允许标准库 + 已批准的少数包）。
// 轮询一个文件 + revision 比较，在一份配置文档的规模上是足够且可测的；
// 官方自己也对配置用"只盯目标路径 + 必需父目录"的定点监视，不是全树。
//
// **铁律 3 的边界**（`$DSH_HOME/AGENTS.md`）：`home/profiles/*/cordis.patch.yml`
// 是**宿主进程的热重载开关**，运行中写它会让整树热重载、冲掉 idle 子代理的
// inbox 投影。本组件：
//   - 只读、只调 `Store.Reload()`，**没有任何写文件路径**；
//   - `ForbiddenHMRTarget`（`internal/pluginmgr/hmr.go:360`）判过的路径**直接拒绝**
//     （构造期就失败，不是运行期才发现）；
//   - 默认**关闭**（`Enabled:false`），由装配点显式打开——与官方"profile context
//     存在才开"同形，而不是"为了有热重载就默认打开"。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"dshgo/internal/config"
	"dshgo/internal/pluginmgr"
)

var (
	// ErrConfigHMRForbidden 是"要盯的文件是宿主的热重载开关"。
	// 与 `pluginmgr.ErrHMRForbiddenTarget` 同一个判据，同一个文件，只是这里的
	// 消费者是配置树而不是插件装载器。
	ErrConfigHMRForbidden = errors.New("config-hmr: 拒绝监听 profiles/*/cordis.patch.yml（那是宿主的热重载开关，不是 profile 配置树）")
	// ErrConfigHMROff 是"没开启时调了 Run"。
	ErrConfigHMROff = errors.New("config-hmr: 未启用（Enabled=false）")
)

// ConfigHMRConfig 是配置热重载的配置。**零值 = 关闭**。
type ConfigHMRConfig struct {
	// Enabled 必须显式为 true 才工作。
	Enabled bool
	// Path 是被监听的那份配置文档（`<home>/state/config-tree.json`）。
	Path string
	// Interval 是轮询周期，缺省 1s。<=0 表示"只在显式 Tick 时检查"（测试友好）。
	Interval time.Duration
}

// ConfigHMR 是 profile 配置热重载引擎。并发安全。
type ConfigHMR struct {
	cfg   ConfigHMRConfig
	store *config.Store

	mu       sync.Mutex
	lastRev  int64
	checks   int64
	reloads  int64
	skipped  int64
	lastErr  string
	onChange []func(ConfigHMRChange)

	// tickMu 串行化 Tick：两个并发 Tick 会各读一次文件、各判一次 revision，
	// 于是可能对同一次变更通知两遍。
	tickMu sync.Mutex
}

// ConfigHMRChange 是一次**观察到的**配置变更。
type ConfigHMRChange struct {
	// From / To 是变更前后的 revision。
	From int64
	To   int64
	// Errs 是这次广播里消费者报的错误（每个消费者一条）。
	Errs []string
}

// ConfigHMRStatus 是诊断投影（与 pluginmgr.HMRStatus 同风格）。
type ConfigHMRStatus struct {
	Enabled   bool   `json:"enabled"`
	Checks    int64  `json:"checks"`
	Reloads   int64  `json:"reloads"`
	Skipped   int64  `json:"skipped"`
	Revision  int64  `json:"revision"`
	LastError string `json:"lastError,omitempty"`
}

// NewConfigHMR 造一个配置热重载引擎。
//
// 关闭（零值）时允许 store 为 nil——关闭的东西不需要依赖。开启时 store 与 path
// 都必须存在：否则"开着但什么都不会发生"是假象，比报错更糟（与
// `pluginmgr.NewHMR` 的取舍一致）。
func NewConfigHMR(store *config.Store, cfg ConfigHMRConfig) (*ConfigHMR, error) {
	if !cfg.Enabled {
		return &ConfigHMR{cfg: cfg}, nil
	}
	if store == nil {
		return nil, fmt.Errorf("config-hmr: Enabled=true 但没有配置树（没有可重载的对象）")
	}
	if cfg.Path == "" {
		return nil, fmt.Errorf("config-hmr: Enabled=true 但没有 Path（不知道盯哪份文档）")
	}
	if pluginmgr.ForbiddenHMRTarget(cfg.Path) {
		return nil, fmt.Errorf("%w（Path=%s）", ErrConfigHMRForbidden, cfg.Path)
	}
	cfg.Interval = time.Second
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	return &ConfigHMR{cfg: cfg, store: store, lastRev: store.Revision()}, nil
}

// Enabled 报告是否开启。
func (h *ConfigHMR) Enabled() bool { return h != nil && h.cfg.Enabled }

// OnChange 注册一个变更消费者。返回反注册函数。
//
// 消费者在 Tick 的**持有锁之外**被调用：一个慢消费者不该让其它消费者等，
// 也不该在它内部回调到本引擎时死锁。
func (h *ConfigHMR) OnChange(fn func(ConfigHMRChange)) (off func()) {
	if h == nil || fn == nil {
		return func() {}
	}
	h.mu.Lock()
	idx := len(h.onChange)
	h.onChange = append(h.onChange, fn)
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if idx < len(h.onChange) {
			h.onChange[idx] = nil // 置空而不是切片重排：反注册顺序不影响其它下标
		}
	}
}

// Status 返回诊断投影。
func (h *ConfigHMR) Status() ConfigHMRStatus {
	if h == nil {
		return ConfigHMRStatus{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return ConfigHMRStatus{
		Enabled:   h.cfg.Enabled,
		Checks:    h.checks,
		Reloads:   h.reloads,
		Skipped:   h.skipped,
		Revision:  h.lastRev,
		LastError: h.lastErr,
	}
}

// Tick 检查一次磁盘上的配置文档；真变了才 Reload + 广播。关闭态是纯 no-op（Skipped）。
func (h *ConfigHMR) Tick(ctx context.Context) (ConfigHMRChange, bool, error) {
	if h == nil || !h.cfg.Enabled {
		if h != nil {
			h.mu.Lock()
			h.skipped++
			h.mu.Unlock()
		}
		return ConfigHMRChange{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return ConfigHMRChange{}, false, err
	}
	h.tickMu.Lock()
	defer h.tickMu.Unlock()

	h.mu.Lock()
	h.checks++
	h.mu.Unlock()

	from := h.store.Revision()
	// Reload 是"先解析、通过才替换"的：坏文件不会把内存里的树弄坏，
	// 但它会返回错误——那正是调用方该知道的（而不是静默保留旧树）。
	if err := h.store.Reload(); err != nil {
		h.mu.Lock()
		h.lastErr = err.Error()
		h.mu.Unlock()
		return ConfigHMRChange{}, false, err
	}
	to := h.store.Revision()
	if to == from {
		return ConfigHMRChange{}, false, nil
	}

	h.mu.Lock()
	h.reloads++
	h.lastRev = to
	consumers := make([]func(ConfigHMRChange), 0, len(h.onChange))
	for _, fn := range h.onChange {
		if fn != nil {
			consumers = append(consumers, fn)
		}
	}
	h.mu.Unlock()

	change := ConfigHMRChange{From: from, To: to}
	for _, fn := range consumers {
		// 一个消费者 panic 不该让剩下的消费者收不到变更，也不该让宿主崩。
		func() {
			defer func() {
				if r := recover(); r != nil {
					change.Errs = append(change.Errs, fmt.Sprintf("消费者 panic：%v", r))
				}
			}()
			fn(change)
		}()
	}
	if len(change.Errs) > 0 {
		h.mu.Lock()
		h.lastErr = change.Errs[0]
		h.mu.Unlock()
	}
	return change, true, nil
}

// Run 周期检查直到 ctx 取消或 Stop。开启态才允许调用。
func (h *ConfigHMR) Run(ctx context.Context) error {
	if !h.Enabled() {
		return ErrConfigHMROff
	}
	// 启动时先查一次：启动路径不该等第一个 tick。
	if _, _, err := h.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
		h.mu.Lock()
		h.lastErr = err.Error()
		h.mu.Unlock()
	}
	ticker := time.NewTicker(h.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, _, err := h.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				h.mu.Lock()
				h.lastErr = err.Error()
				h.mu.Unlock()
			}
		}
	}
}

// configHMRConfigFromEnv 回答"这次装配要不要开配置热重载"。
//
// 与 `pluginHMRConfig` 同一套开关形状（`DSHD_PLUGIN_HMR`），但**判据不同**：
//   - 插件 HMR 的默认是关（它会重装/卸载运行中的插件）；
//   - profile 配置热重载的默认是**开**（官方 `dsh-base` 的默认就是开：
//     "Profile configuration reloads by default"），除非显式 `DSHD_PROFILE_HMR=0`。
//
// 默认开是安全的，因为这一半**不重装任何插件**：它只把配置树从磁盘重读一遍、
// 把 revision 变化广播给注册过的消费者。真正危险的那一半（模块源码替换）仍然
// 由 `pluginHMRConfig` 守着、默认关。
//
// path 为空（这个进程根本没有配置树文件）时一律关闭：没有可重载的对象。
func configHMRConfigFromEnv(path string) ConfigHMRConfig {
	if path == "" {
		return ConfigHMRConfig{}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DSHD_PROFILE_HMR"))) {
	case "0", "false", "off", "no":
		return ConfigHMRConfig{}
	}
	return ConfigHMRConfig{Enabled: true, Path: path, Interval: 2 * time.Second}
}
