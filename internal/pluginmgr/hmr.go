package pluginmgr

// HMR（官方插件面 #4）：插件目录变化的**安全**重装。
//
// 基线里 grep `fsnotify|hot reload|hmr` = 0，`modules/tool/plugin` 自述无热重载——
// 改完插件必须重启宿主。本文件补的是一小块、可审计的 HMR，而不是通用文件监听：
//
//  1. **默认关闭**。零值 HMRConfig 就是关的。热重载这个东西一旦默认开着，
//     改一个文件就能让运行中的插件集合变化；这台机器上"整树热重载"曾经把
//     idle 子代理的 inbox 投影冲掉、直接把宿主 exit(1)（$DSH_HOME/AGENTS.md 铁律 3）。
//     要开必须显式 `HMRConfig{Enabled: true}`，并且只在临时 fixture 里演示。
//  2. **绝不写 `home/profiles/*/cordis.patch.yml`**。那是**宿主**的热重载开关，
//     不是插件的：HMR 只有读路径（扫 plugin.json）和 `Loader.Reload` 调用，
//     没有写文件的代码。任何指向那个文件的通知在入口就被 `ErrHMRForbiddenTarget`
//     拒绝（`ForbiddenHMRTarget` 也供宿主别处复用）。
//  3. **有界 + 合并**。变更进一个最多 MaxPending 条的队列，同一插件的一串通知
//     只重装一次；队列满了报错而不是无限吃内存。
//
// 检测方式：扫 `<Root>/packages/<id>/<version>/plugin.json`，用 `size:mtime`
// 做 digest 对比。不引入 fsnotify（本仓不新增依赖），插件根是有限的小目录，
// 定时扫描足够；宿主需要更实时可以在外层用 watcher 调 `Observe(path)`。
//
// 边界（明确不做）：
//   - 不监听、不写 `cordis.yml` / `cordis.patch.yml`；
//   - 不卸载"已经不在磁盘上的包"（那是 Manager 的事，HMR 只看已安装包的版本目录）；
//   - 不做跨进程通知：只在**本进程**的 Loader 上重装。

import (
	"context"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrHMROff 是"HMR 没开启"。关闭状态下的 Run/Observe 都是调用方的 bug。
	ErrHMROff = errors.New("hmr: 未启用（零值 HMRConfig 就是关闭）")
	// ErrHMRForbiddenTarget 是"这个路径是宿主的开关，HMR 不许碰"。
	ErrHMRForbiddenTarget = errors.New("hmr: 拒绝操作 profiles/*/cordis.patch.yml（那是宿主的热重载开关，不是插件的）")
	// ErrHMRQueueFull 是"待重装队列满了"。
	ErrHMRQueueFull = errors.New("hmr: 待重装队列已满（拒绝无界排队）")
	// ErrHMRNotManaged 是"这个路径不在插件根之下"。
	ErrHMRNotManaged = errors.New("hmr: 路径不在插件根之下，不归 HMR 管")
)

// HMRConfig 是 HMR 的配置。**零值 = 关闭**。
type HMRConfig struct {
	// Enabled 必须显式为 true 才工作（默认 false）。
	Enabled bool
	// Root 是插件管理器根目录（含 packages/<id>/<version>/plugin.json）。
	// 为空时 Tick 只报 Status.LastError，不扫描任何目录。
	Root string
	// Interval 是 Run 的扫描周期，缺省 1s。
	Interval time.Duration
	// MaxPending 是待重装队列上限，缺省 64。
	MaxPending int
}

// HMRStatus 是诊断投影。
type HMRStatus struct {
	Enabled   bool   `json:"enabled"`
	Scans     int64  `json:"scans"`
	Reloads   int64  `json:"reloads"`
	Skipped   int64  `json:"skipped"`
	Pending   int    `json:"pending"`
	LastError string `json:"lastError,omitempty"`
}

// HMRResult 是一次 Tick 的结果。
type HMRResult struct {
	Scanned int      `json:"scanned"`
	Changed []string `json:"changed,omitempty"`
	Reloads int      `json:"reloads"`
	Skipped bool     `json:"skipped"`
	Errors  []string `json:"errors,omitempty"`
}

// HMR 是插件热重载引擎。并发安全。
type HMR struct {
	cfg    HMRConfig
	loader *Loader

	mu      sync.Mutex
	prev    map[string]string
	pending map[string]struct{}
	order   []string
	scans   int64
	reloads int64
	skipped int64
	lastErr string
	stopped bool
	stopCh  chan struct{}

	tickMu sync.Mutex
}

// NewHMR 造一个 HMR 引擎。
//
// 关闭（零值）时允许 loader 为 nil——关闭的东西不需要依赖。开启时 loader 必须
// 存在：否则"开着但什么都不会发生"是假象，比报错更糟。
func NewHMR(loader *Loader, cfg HMRConfig) (*HMR, error) {
	if cfg.MaxPending < 0 {
		return nil, fmt.Errorf("hmr: MaxPending 不能为负数（拿到 %d）", cfg.MaxPending)
	}
	if cfg.Enabled && loader == nil {
		return nil, fmt.Errorf("hmr: Enabled=true 但没有 Loader（没有可重装的运行时）")
	}
	if cfg.Root != "" {
		absolute, err := filepath.Abs(cfg.Root)
		if err != nil {
			return nil, fmt.Errorf("hmr: 解析 Root 失败：%w", err)
		}
		if ForbiddenHMRTarget(absolute) || hasProfilesSegment(absolute) {
			return nil, fmt.Errorf("%w（Root=%s）", ErrHMRForbiddenTarget, absolute)
		}
		cfg.Root = absolute
	}
	if cfg.MaxPending == 0 {
		cfg.MaxPending = 64
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	return &HMR{
		cfg:     cfg,
		loader:  loader,
		pending: map[string]struct{}{},
		stopCh:  make(chan struct{}),
	}, nil
}

// Enabled 报告 HMR 是否开启（**默认 false**）。
func (h *HMR) Enabled() bool { return h != nil && h.cfg.Enabled }

// Status 返回诊断投影。
func (h *HMR) Status() HMRStatus {
	if h == nil {
		return HMRStatus{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return HMRStatus{
		Enabled:   h.cfg.Enabled,
		Scans:     h.scans,
		Reloads:   h.reloads,
		Skipped:   h.skipped,
		Pending:   len(h.pending),
		LastError: h.lastErr,
	}
}

// Observe 记一次"这些路径变了"（给外层 watcher 用）。
//
// 只入队，不立刻重装：一次保存往往触发好几条通知，立刻重装等于对每个事件
// 都重跑一次 wasm 装载。真正的重装在 Tick（或 Run 的周期）里做。
func (h *HMR) Observe(paths ...string) error {
	if !h.Enabled() {
		return ErrHMROff
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, path := range paths {
		if ForbiddenHMRTarget(path) {
			return ErrHMRForbiddenTarget
		}
		id, ok := h.pluginIDFromPath(path)
		if !ok {
			return fmt.Errorf("%w：%s", ErrHMRNotManaged, path)
		}
		if _, queued := h.pending[id]; queued {
			continue // 同一插件的一串通知合并成一条
		}
		if len(h.pending) >= h.cfg.MaxPending {
			return fmt.Errorf("%w（上限 %d，拒绝入队 %s）", ErrHMRQueueFull, h.cfg.MaxPending, id)
		}
		h.pending[id] = struct{}{}
		h.order = append(h.order, id)
	}
	return nil
}

// Tick 扫描一次并重装排队中的插件。
//
// 关闭状态下是**纯 no-op**：不扫描、不重装、不报错（回 Skipped=true）。
func (h *HMR) Tick(ctx context.Context) (HMRResult, error) {
	if h == nil || !h.cfg.Enabled {
		if h != nil {
			h.mu.Lock()
			h.skipped++
			h.mu.Unlock()
		}
		return HMRResult{Skipped: true}, nil
	}
	if h.loader == nil {
		return HMRResult{}, fmt.Errorf("hmr: 未配置 Loader")
	}

	// 一次只跑一个 Tick：两个 Tick 并发重装同一个插件会把 gen 弄乱。
	h.tickMu.Lock()
	defer h.tickMu.Unlock()

	h.mu.Lock()
	scanned, changed := h.scanLocked()
	h.scans++
	for _, id := range changed {
		if _, queued := h.pending[id]; queued {
			continue
		}
		if len(h.pending) >= h.cfg.MaxPending {
			h.lastErr = fmt.Sprintf("变更 %s 未能入队：%v", id, ErrHMRQueueFull)
			continue
		}
		h.pending[id] = struct{}{}
		h.order = append(h.order, id)
	}
	pending := append([]string(nil), h.order...)
	h.order = h.order[:0]
	h.pending = map[string]struct{}{}
	h.mu.Unlock()

	// 失败的插件只记录、不自动重试：坏包会在每个 tick 重跑一遍 wasm 装载，
	// 把 CPU 吃满。下一次文件变化时会重新入队。
	result := HMRResult{Scanned: scanned, Changed: changed}
	for _, id := range pending {
		if _, err := h.loader.Reload(ctx, id); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		result.Reloads++
	}

	h.mu.Lock()
	h.reloads += int64(result.Reloads)
	if len(result.Errors) > 0 {
		h.lastErr = strings.Join(result.Errors, "；")
	} else if h.cfg.Root == "" {
		h.lastErr = "HMR 已开启但未配置 Root（插件根目录），不扫描任何目录"
	} else {
		h.lastErr = ""
	}
	h.mu.Unlock()
	return result, nil
}

// Run 是宿主接线入口：显式开启才循环扫描；Stop（或 ctx 取消）后立刻返回。
func (h *HMR) Run(ctx context.Context) error {
	if !h.Enabled() {
		return ErrHMROff
	}
	// 先立刻扫一次：启动路径不该等第一个 tick。
	if _, err := h.Tick(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(h.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-h.stopCh:
			return nil
		case <-ticker.C:
			if _, err := h.Tick(ctx); err != nil {
				h.mu.Lock()
				h.lastErr = err.Error()
				h.mu.Unlock()
			}
		}
	}
}

// Stop 让 Run 收敛。幂等。
func (h *HMR) Stop() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return
	}
	h.stopped = true
	close(h.stopCh)
}

// scanLocked 扫插件根，返回 (扫到的 manifest 数, 发生变化的插件 id)。
// 调用方必须持有 h.mu。
func (h *HMR) scanLocked() (int, []string) {
	if h.cfg.Root == "" {
		return 0, nil
	}
	pattern := filepath.Join(h.cfg.Root, "packages", "*", "*", "plugin.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		h.lastErr = fmt.Sprintf("扫描插件根失败：%v", err)
		return 0, nil
	}
	current := make(map[string]string, len(matches))
	for _, manifest := range matches {
		info, err := os.Stat(manifest)
		if err != nil {
			continue // 扫描期间被删的版本目录：下一次扫描自然不再出现
		}
		id, ok := h.pluginIDFromPath(manifest)
		if !ok {
			continue
		}
		current[id] = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	}
	if h.prev == nil {
		// 第一次扫描只建立基线：磁盘上现在是什么样，就当作"已经装载的样子"。
		h.prev = current
		return len(current), nil
	}
	changed := make([]string, 0, len(current))
	for id, digest := range current {
		if previous, seen := h.prev[id]; !seen || previous != digest {
			changed = append(changed, id)
		}
	}
	h.prev = current
	sort.Strings(changed)
	return len(current), changed
}

// pluginIDFromPath 从 `<Root>/packages/<id>/...` 里取出插件 id。
func (h *HMR) pluginIDFromPath(path string) (string, bool) {
	if h.cfg.Root == "" {
		return "", false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(h.cfg.Root, absolute)
	if err != nil {
		return "", false
	}
	if relative == "." || strings.HasPrefix(relative, "..") {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 3 || parts[0] != "packages" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// ForbiddenHMRTarget 判定一个路径是否是 HMR 绝对不许写的东西。
//
// 规则故意比"profiles/*/cordis.patch.yml"更宽：HMR 没有任何理由写
// `cordis.patch.yml`（不管在哪个目录），宽一点是纵深防御，不会误伤（它的
// 工作目录只有 packages/）。
func ForbiddenHMRTarget(path string) bool {
	// 统一成 `/` 再判：Windows 传反斜杠、测试/日志里传正斜杠都要判得一样。
	clean := strings.ReplaceAll(filepath.Clean(path), "\\", "/")
	base := pathpkg.Base(clean)
	lowerBase := strings.ToLower(base)
	if lowerBase == "cordis.patch.yml" {
		return true
	}
	if hasProfilesSegment(clean) && strings.HasPrefix(lowerBase, "cordis.") && strings.HasSuffix(lowerBase, ".yml") {
		return true
	}
	return false
}

func hasProfilesSegment(path string) bool {
	normalized := strings.ReplaceAll(filepath.Clean(path), "\\", "/")
	for _, segment := range strings.Split(normalized, "/") {
		if strings.EqualFold(segment, "profiles") {
			return true
		}
	}
	return false
}
