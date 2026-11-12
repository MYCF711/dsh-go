package terminalpanel

// 本包的配置面：与官方 `dsh-terminal-panel` 的同名三个字段对齐（其余字段属于
// 终端后端 `PanelTerminalBackend`，本包不实现后端，只实现两个只读工具）。
//
// 官方字段与默认值（`terminal-panel/src/config.ts:50-52`）：
//
//	timeoutMs          30000          （官方是 terminal_send 的上限；本包套在宿主调用上）
//	maxReadBytes       256 * 1024     （一次读取结果（含位置/截断标记）的 UTF-8 字节上限）
//	readUserTerminals  true           （false = 两个工具**都不存在**）
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
	ReadUserTerminals bool
	// MaxReadBytes 是一次读取结果的 UTF-8 字节上限（含位置行与截断标记）。
	MaxReadBytes int
	// TimeoutMs 是单次宿主调用（列 roster / 读屏幕）的上限，毫秒。
	TimeoutMs int
}

// DefaultConfig 是官方默认部署的配置（三个字段与官方 config.ts 默认值一致）。
func DefaultConfig() Config {
	return Config{
		ReadUserTerminals: true,
		MaxReadBytes:      terminal.DefaultPanelReadBytes,
		TimeoutMs:         30_000,
	}
}

// validate 校验配置；非法值在 Register 时响亮失败（不许静默退化成默认值）。
//
// 边界逐条照官方 schemastery：`maxReadBytes` 最小 64（`config.ts:51`），
// `timeoutMs` 最小 1（`config.ts:50`）。
func (c Config) validate() error {
	if c.TimeoutMs < 1 {
		return fmt.Errorf("timeoutMs 必须是正整数（官方 min 1，拿到 %d）；装配请从 terminalpanel.DefaultConfig() 出发", c.TimeoutMs)
	}
	if c.MaxReadBytes < 64 {
		return fmt.Errorf("maxReadBytes 不能小于 64（官方 min 64，拿到 %d）；装配请从 terminalpanel.DefaultConfig() 出发", c.MaxReadBytes)
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
