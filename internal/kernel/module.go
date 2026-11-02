package kernel

import (
	"fmt"
	"os"
	"sort"

	"github.com/BurntSushi/toml"
)

// Backend 是插件装载层（PLAN §3）。
type Backend string

const (
	// BackendStatic 编译进二进制，零装载开销——内核与官方默认实现用这层。
	BackendStatic Backend = "static"
	// BackendWasm 由 wazero 装载，用于频繁替换的纯逻辑。
	BackendWasm Backend = "wasm"
	// BackendProcess 由 go-plugin 装载，用于第三方 / 重依赖 / 崩溃隔离。
	BackendProcess Backend = "process"
)

// Module 是一个可装配的插件。默认实现与第三方插件形状完全相同（A3）。
type Module struct {
	ID       string
	Backend  Backend
	Provides []string       // 本模块提供的能力（服务键名）
	Requires []string       // 本模块依赖的能力
	Builtin  bool           // true = 内置模式，用户层不可覆盖
	Config   map[string]any // 来自 manifest 的配置
	Register func(*Scope) error
}

// Manifest 是 dsh.toml。
type Manifest struct {
	Profile struct {
		Name string `toml:"name"`
	} `toml:"profile"`
	Modules []ModuleEntry `toml:"module"`
}

// ModuleEntry 是 manifest 里的一行。
type ModuleEntry struct {
	ID       string         `toml:"id"`
	Backend  Backend        `toml:"backend"`
	Path     string         `toml:"path"`
	Enabled  *bool          `toml:"enabled"`
	Provides []string       `toml:"provides"`
	Requires []string       `toml:"requires"`
	Config   map[string]any `toml:"config"`
}

// LoadManifest 读 dsh.toml。文件不存在不算错（返回空 manifest）。
func LoadManifest(path string) (*Manifest, error) {
	m := &Manifest{}
	if path == "" {
		return m, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return m, nil
	}
	if _, err := toml.DecodeFile(path, m); err != nil {
		return nil, fmt.Errorf("读 manifest %s 失败: %w", path, err)
	}
	return m, nil
}

// Apply 按 manifest 过滤、覆盖并排序待装配模块。
//
// 语义（对齐 DSH 的 applyEntryPatches）：按 id 命中；`enabled = false` 是禁用；
// config 是**整块替换**而不是 merge。manifest 里出现未编译进来的 id 是错误，
// 不是静默忽略——否则用户以为改了配置，其实什么都没发生。
func (m *Manifest) Apply(mods []Module) ([]Module, error) {
	byID := make(map[string]int, len(mods))
	known := make([]string, 0, len(mods))
	for i, mod := range mods {
		if mod.ID == "" {
			return nil, fmt.Errorf("模块 #%d 没有 ID", i)
		}
		if _, dup := byID[mod.ID]; dup {
			return nil, fmt.Errorf("模块 id %q 重复注册", mod.ID)
		}
		byID[mod.ID] = i
		known = append(known, mod.ID)
	}

	seen := make(map[string]bool, len(m.Modules))
	out := make([]Module, 0, len(mods))
	for _, e := range m.Modules {
		if e.ID == "" {
			return nil, fmt.Errorf("manifest 里有一行 module 缺 id")
		}
		idx, ok := byID[e.ID]
		if !ok {
			sort.Strings(known)
			return nil, fmt.Errorf("manifest 指定的模块 %q 没有编译进二进制；可用模块: %v", e.ID, known)
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("manifest 里模块 %q 出现多次", e.ID)
		}
		seen[e.ID] = true

		mod := mods[idx]
		if e.Backend != "" {
			mod.Backend = e.Backend
		}
		if len(e.Provides) > 0 {
			mod.Provides = e.Provides
		}
		if len(e.Requires) > 0 {
			mod.Requires = e.Requires
		}
		if e.Config != nil {
			mod.Config = e.Config // 整块替换，不 merge
		}
		out = append(out, mod)
	}
	return out, nil
}
