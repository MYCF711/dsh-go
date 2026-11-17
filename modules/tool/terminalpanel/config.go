package terminalpanel

// 本包的配置面：与官方 `dsh-terminal-panel/src/config.ts` 的字段同名同默认值。
//
// 官方字段与默认值（`config.ts:40-52`）：
//
//	backendType        "shell"        （终端后端类型；`terminal_open` 的 type 只认它）
//	cols / rows        160 / 40       （新终端初始尺寸，cols≥2、rows≥1）
//	preferredShells    []             （按名字优先挑 shell；空 = 环境默认）
//	environment        PAGER/GIT_PAGER = cat
//	timeoutMs          30000          （官方是 terminal_send 的上限；本包套在宿主调用上）
//	maxReadBytes       256 * 1024     （一次读取结果（含位置/截断标记）的 UTF-8 字节上限）
//	readUserTerminals  true           （false = 两个**用户终端**工具都不存在；
//	                                   `terminal_open` 不属于这个开关）
//
// # 为什么 ReadUserTerminals 是 bool 而不是"默认 true 的三态"
//
// 官方是 schemastery 的 `z.boolean().default(true)`，profile patch 里只写
// `config: { readUserTerminals: false }` 就是关掉。Go 的零值无法区分"没写"与
// "写了 false"，所以约定：**装配一律从 DefaultConfig() 出发**，再改需要的字段
// （`Module` 会在 Register 时校验，零值 Config 会因为 TimeoutMs/MaxReadBytes
// 非法而响亮失败，不会静默变成一个"没有工具的模块"）。

import (
	"fmt"
	"strconv"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/terminal"
)

// ModuleID 是装配图里的模块 id（dsh.toml 的白名单按它命中）。
//
// 抽成常量是因为它现在有两个出口（`Module` 与 `ModuleFromEnv` 的失败形态）：
// 两处各写一遍字符串，迟早有一处拼错，而拼错的表现是"manifest 里写着却启动报
// 不认识这个模块"。
const ModuleID = "tool-terminalpanel"

// Config 是 `@deepseek-ai/dsh-terminal-panel` 里与本包相关的配置。
type Config struct {
	// ReadUserTerminals 决定是否注册 `user_terminal_list` / `user_terminal_read`。
	// false 时两个工具都不注册（官方："readUserTerminals: false removes both"）。
	//
	// 它**不**管 `terminal_open`：那个工具属于官方另一个包 `@deepseek-ai/dsh-tool-terminal`
	// （`tool-catalog.md:36`），`terminal-panel/src/index.ts:39` 只在 readUserTerminals
	// 为真时才注册那两个用户工具，与终端分配无关。
	ReadUserTerminals bool
	// MaxReadBytes 是一次读取结果的 UTF-8 字节上限（含位置行与截断标记）。
	MaxReadBytes int
	// TimeoutMs 是单次宿主调用（列 roster / 读屏幕 / 分配终端）的上限，毫秒。
	TimeoutMs int

	// ---- 以下四个字段是官方 terminal-panel 的"分配请求"配置 ----
	//
	// 它们不是本包新增的语义：官方 `PanelTerminalBackend.spawn` 就是把
	// `config.cols/rows/preferredShells/environment` 直接塞进 `AgentTerminalRequest`
	// （`terminal-panel/src/backend.ts:103-111`）。取值与边界照 `config.ts:40-44`。

	// BackendType 是终端注册表的后端类型（官方默认 `shell`；
	// `terminal_open` 的 `type` 参数只认它，见 `dsh-terminal/src/index.ts:140-141`）。
	BackendType string
	// Cols / Rows 是新终端的初始尺寸（官方默认 160×40，边界 cols≥2、rows≥1；
	// `dimensions` 越界是**报错**不是截断，`lib/index.js:1541`）。
	Cols int
	Rows int
	// PreferredShells 是按名字优先挑 shell 的顺序（官方默认空 = 环境默认 shell）。
	PreferredShells []string
	// Environment 是追加进终端子 shell 的变量（官方默认 PAGER/GIT_PAGER = cat；
	// 不放这两个，`git log` 之类的程序会在分页器里停住）。
	Environment map[string]string

	// ---- 以下三个是官方 terminal-panel 的 **send 等待阈值**（`config.ts:47-49`）----
	//
	// 它们不属于"分配请求"，而是 `PanelSend` 判定何时把控制权还给模型的那三条线
	// （`terminal-panel/src/session.ts:248-292`）：静默到 promptSilenceMs 就探测前台、
	// 能写就回 stdin_read；静默到 idleSilenceMs 回 inferred_idle；总时长到 timeoutMs
	// 回 timeout。官方顺序校验：prompt <= idle <= timeout（`config.ts:59-68`）。
	//
	// 本宿主把这几个值随 send 请求过桥（`internal/terminal/AgentSendRequest`），
	// 真终端那一侧原样使用——配置只有这一份，不在父进程再复制一遍。

	// PromptSilenceMs 是"输出静默多久算 shell 可能回到提示符"（官方默认 300）。
	PromptSilenceMs int
	// IdleSilenceMs 是"输出静默多久算推断空闲"（官方默认 3000）。
	IdleSilenceMs int
	// ProbeIntervalMs 是两次前台探测之间的最小间隔（官方默认 1000）。
	ProbeIntervalMs int
}

// DefaultConfig 是官方默认部署的配置（字段与官方 config.ts 的默认值一致）。
func DefaultConfig() Config {
	return Config{
		ReadUserTerminals: true,
		MaxReadBytes:      terminal.DefaultPanelReadBytes,
		TimeoutMs:         30_000,
		BackendType:       "shell",
		Cols:              160,
		Rows:              40,
		PreferredShells:   nil,
		Environment:       map[string]string{"PAGER": "cat", "GIT_PAGER": "cat"},
		PromptSilenceMs:   300,
		IdleSilenceMs:     3_000,
		ProbeIntervalMs:   1_000,
	}
}

// validate 校验配置；非法值在 Register 时响亮失败（不许静默退化成默认值）。
//
// 边界逐条照官方 schemastery：`maxReadBytes` 最小 64（`config.ts:51`），
// `timeoutMs` 最小 1（`config.ts:50`）；`backendType` 非空、`cols` ≥ 2、`rows` ≥ 1
// （`config.ts:40-42`，与官方 `dimensions` 的下限同源，`lib/index.js:1541`）。
func (c Config) validate() error {
	if c.TimeoutMs < 1 {
		return fmt.Errorf("timeoutMs 必须是正整数（官方 min 1，拿到 %d）；装配请从 terminalpanel.DefaultConfig() 出发", c.TimeoutMs)
	}
	if c.MaxReadBytes < 64 {
		return fmt.Errorf("maxReadBytes 不能小于 64（官方 min 64，拿到 %d）；装配请从 terminalpanel.DefaultConfig() 出发", c.MaxReadBytes)
	}
	if strings.TrimSpace(c.BackendType) == "" {
		return fmt.Errorf("backendType 不能为空（官方 min 1 字符）；装配请从 terminalpanel.DefaultConfig() 出发")
	}
	if c.Cols < 2 {
		return fmt.Errorf("cols 不能小于 2（官方 min 2，拿到 %d）；装配请从 terminalpanel.DefaultConfig() 出发", c.Cols)
	}
	if c.Rows < 1 {
		return fmt.Errorf("rows 不能小于 1（官方 min 1，拿到 %d）；装配请从 terminalpanel.DefaultConfig() 出发", c.Rows)
	}
	// 官方 validateConfig（`config.ts:59-68`）：三条顺序约束。写反的后果不是"慢一点"，
	// 而是某档 waitReason 永远出不来（例如 prompt > idle 时 stdin_read 被 inferred_idle 抢走）。
	if c.PromptSilenceMs < 1 || c.IdleSilenceMs < 1 || c.ProbeIntervalMs < 1 {
		return fmt.Errorf("promptSilenceMs / idleSilenceMs / probeIntervalMs 都必须是正整数（官方 min 1，拿到 %d/%d/%d）",
			c.PromptSilenceMs, c.IdleSilenceMs, c.ProbeIntervalMs)
	}
	if c.PromptSilenceMs > c.IdleSilenceMs {
		return fmt.Errorf("promptSilenceMs(%d) 不能大于 idleSilenceMs(%d)（官方 config.ts:60-62）",
			c.PromptSilenceMs, c.IdleSilenceMs)
	}
	if c.IdleSilenceMs > c.TimeoutMs {
		return fmt.Errorf("idleSilenceMs(%d) 不能大于 timeoutMs(%d)（官方 config.ts:63-65）",
			c.IdleSilenceMs, c.TimeoutMs)
	}
	return nil
}

// 环境变量名：与官方 config 的三个键一一对应（`terminal-panel/src/config.ts:50-52`）。
//
// # 为什么是环境变量而不是 dsh.toml 的 config
//
// manifest 的 `config` 只写进 `kernel.Module.Config`，而模块的 Register 只拿
// `*Scope`——没有把那条配置下发给模块的通道（这是内核现在的形状，不是本包的取舍）。
// 宿主里既有的"模块级开关"做法就是入口解析环境变量
// （`officialPTYScrollbackRowsFromEnv`、`compactHeadroomFromEnv` 同形）。
// 接官方 config 树（`config.patch.*` → 三个键）是另一件事，已登记在 E8 报告。
const (
	// EnvReadUserTerminals 是 `readUserTerminals`（false = 两个工具都不注册）。
	EnvReadUserTerminals = "DSHD_TERMINAL_PANEL_READ_USER_TERMINALS"
	// EnvMaxReadBytes 是 `maxReadBytes`（一次读取结果的 UTF-8 字节上限）。
	EnvMaxReadBytes = "DSHD_TERMINAL_PANEL_MAX_READ_BYTES"
	// EnvTimeoutMs 是 `timeoutMs`（单次宿主调用的上限，毫秒）。
	EnvTimeoutMs = "DSHD_TERMINAL_PANEL_TIMEOUT_MS"
	// EnvPromptSilenceMs / EnvIdleSilenceMs / EnvProbeIntervalMs 是 send 的三条阈值
	// （官方 `promptSilenceMs` / `idleSilenceMs` / `probeIntervalMs`）。
	EnvPromptSilenceMs = "DSHD_TERMINAL_PANEL_PROMPT_SILENCE_MS"
	EnvIdleSilenceMs   = "DSHD_TERMINAL_PANEL_IDLE_SILENCE_MS"
	EnvProbeIntervalMs = "DSHD_TERMINAL_PANEL_PROBE_INTERVAL_MS"
)

// ConfigFromEnv 从宿主环境变量解析三个官方配置项：没设置的项用官方默认值。
//
// 非法值**返回错误**而不是静默回退：`readUserTerminals=flase` 这种拼错的写法如果
// 被当成"没设置"，用户会以为已经关掉了两个工具，而实际上它们还在表里。
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := DefaultConfig()
	if getenv == nil {
		return cfg, nil
	}
	if raw := strings.TrimSpace(getenv(EnvReadUserTerminals)); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q 不是布尔值（1/0、true/false）", EnvReadUserTerminals, raw)
		}
		cfg.ReadUserTerminals = v
	}
	if raw := strings.TrimSpace(getenv(EnvMaxReadBytes)); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q 不是整数", EnvMaxReadBytes, raw)
		}
		cfg.MaxReadBytes = v // 边界仍由 validate 在 Register 时判（min 64）
	}
	if raw := strings.TrimSpace(getenv(EnvTimeoutMs)); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q 不是整数", EnvTimeoutMs, raw)
		}
		cfg.TimeoutMs = v
	}
	for _, item := range []struct {
		env string
		dst *int
	}{
		{EnvPromptSilenceMs, &cfg.PromptSilenceMs},
		{EnvIdleSilenceMs, &cfg.IdleSilenceMs},
		{EnvProbeIntervalMs, &cfg.ProbeIntervalMs},
	} {
		raw := strings.TrimSpace(getenv(item.env))
		if raw == "" {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q 不是整数", item.env, raw)
		}
		*item.dst = v // 边界与顺序仍由 validate 在 Register 时判
	}
	return cfg, nil
}

// ModuleFromEnv 按宿主环境装配模块；配置非法时 Register **响亮失败**。
//
// 失败形态为什么放在 Register 里而不是直接 panic：`builtinModules` 不能返回
// error（装配集中在 kernel.Assemble），而"静默用一个坏配置"或"静默不注册这两个
// 工具"都会让人以为配置生效了——只有装配失败能把它暴露在启动屏幕上。
func ModuleFromEnv(getenv func(string) string) kernel.Module {
	cfg, err := ConfigFromEnv(getenv)
	if err != nil {
		return kernel.Module{
			ID:       ModuleID,
			Backend:  kernel.BackendStatic,
			Requires: []string{"tools"},
			Register: func(*kernel.Scope) error {
				return fmt.Errorf("%s: 宿主环境里的配置非法: %w", ModuleID, err)
			},
		}
	}
	return Module(cfg)
}
