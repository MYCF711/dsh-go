// Package shellenv 是官方 base 插件 #46 `shell-env` 的 Go 落地：一张**每次 shell
// 工具执行**重建的受管 `DSH_*` 环境注册表。
//
// 官方原件（只读对照，本机解包在 tmp/GO8-RC2/）：
//
//	tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-shell-env/src/index.ts   （227 行）
//	tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-shell-env/src/error.ts   （28 行）
//
// 逐条对齐的东西：服务名 `shellEnv`、注册表 `ShellEnvRegistry`、错误 `ShellEnvError`
// 与码 `SHELL_ENV_CONTRIBUTION_INVALID`、7 个预留键、注册期六条失败文案、collect 期
// 两条失败文案、贡献者按名字排序、最终按键名排序。
//
// 语义要点（官方 index.ts:93-99）：命名空间**每次执行重建**——执行方先把环境里
// 既有的 `DSH_*` 全部丢掉，再叠加注册表的当前快照。内置事实由注册表自己拥有，
// 插件只能贡献额外的事实，且注册是 effect-scoped 的（撤销时连同它声明的键一起释放）。
//
// 本仓与官方的三处结构性差异，都是"没有那个事实就不注入"，不是省略或占位：
//
//  1. `DSH_NODE_EXECUTABLE`：官方给 `process.execPath`（SEA 单文件构建时省略，
//     index.ts:171-172）。本仓宿主不嵌 Node 运行时（AGENTS R6），进程里根本没有
//     "Node 可执行文件"这条事实，所以不注入——不拿空串或假路径冒充。
//  2. `DSH_PROFILE` / `DSH_PROFILE_DIR` / `DSH_INSTALL_DIR`：官方来自
//     `ctx.profileContext`（启动器注入的 profile，index.ts:176-181）。本仓没有
//     profileContext 这一层，所以不注入。
//  3. 官方 `Object.freeze`（index.ts:200）：Go 的 map 没有冻结语义。等价的保证是
//     `Collect` **每次返回独立副本**，调用方改它不会动到注册表内部状态。
//
// 与 R2 有关的一条：这里**没有** `init()`，装配由 `cmd/dshd` 显式完成
// （key 见 `spi.ShellEnvKey`）。
package shellenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// 受管变量的前缀与键名。官方把键名拼成 `${DSH_ENV_PREFIX}XXX`
// （index.ts:76-81，`DSH_ENV_PREFIX` 来自 `@deepseek-ai/dsh-shell`）。
const (
	// EnvPrefix 是所有受管变量的前缀（官方 `DSH_ENV_PREFIX`）。
	// 唯一的字面量在契约层：`spi.ShellEnvPrefix`。
	EnvPrefix = spi.ShellEnvPrefix
	// HomeEnv 是解析出的 dsh home（官方 `DSH_HOME_ENV`，来自 `dsh-home-paths`）。
	HomeEnv = EnvPrefix + "HOME"
	// ShellKey 标记"这是一次受管 shell 调用"。
	ShellKey = EnvPrefix + "SHELL"
	// SessionIDKey 是当前会话 id：**只在有会话上下文时**注入。
	SessionIDKey = EnvPrefix + "SESSION_ID"
	// ProfileKey 是启动器 profile 名（官方有 profileContext 时注入）。
	ProfileKey = EnvPrefix + "PROFILE"
	// ProfileDirKey 是 profile 目录（同上）。
	ProfileDirKey = EnvPrefix + "PROFILE_DIR"
	// InstallDirKey 是安装目录（同上）。
	InstallDirKey = EnvPrefix + "INSTALL_DIR"
	// NodeExecutableKey 是 Node 可执行文件（官方给 `process.execPath`）。
	NodeExecutableKey = EnvPrefix + "NODE_EXECUTABLE"
)

// reservedKeys 是注册表自己拥有的 7 个键：贡献者**不许**声明
// （官方 `RESERVED_BASH_ENV_KEYS`，index.ts:82-90）。
var reservedKeys = map[string]bool{
	HomeEnv:           true,
	ShellKey:          true,
	SessionIDKey:      true,
	ProfileKey:        true,
	ProfileDirKey:     true,
	InstallDirKey:     true,
	NodeExecutableKey: true,
}

// keySuffixRe 是前缀之后允许的形状（官方 `BASH_ENV_KEY_SUFFIX`，index.ts:91）：
// 首字符大写字母，其余大写字母/数字/下划线。`DSH_` 之后接小写或数字开头的
// 声明一律拒——它们在 Windows 上会被环境和 shell 折叠成同一个名字。
var keySuffixRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Variable 是一个受管变量的模型可见元数据（官方 `BashEnvVariable`）。
type Variable struct {
	// Description 是这条环境事实的简要说明；去空白后必须非空。
	Description string
}

// Contributor 是一次插件贡献（官方 `BashEnvContributor`）。
//
// 声明的键让所有权冲突在第一条命令之前就能被发现；`Resolve` 只算**这一次执行**
// 拿得到的值。
type Contributor struct {
	// Name 是稳定名字：用于诊断与重名检测。
	Name string
	// Variables 是这个贡献者**允许**返回的全部键。
	// 注册后不要再改这个映射（它是共享只读数据）。
	Variables map[string]Variable
	// Resolve 算这次执行的值，返回结果里只能出现 Variables 里声明过的键。
	//
	// 值类型是 `any` 而不是 `string`：官方在 collect 里做 `typeof value !== 'string'`
	// 的运行期检查（index.ts:190-195），Go 若把返回类型钉成 `map[string]string`，
	// 那条检查就永远不可能触发——等于把官方的两条防御之一悄悄删掉。用 any 保留它，
	// 违规在 `Collect` 里变成带码的 `ShellEnvError`。
	Resolve func(ctx context.Context) (map[string]any, error)
}

// VariableInfo 是 `List` 返回的一条可枚举声明（官方 `BashEnvVariableInfo`）。
type VariableInfo struct {
	// Contributor 是拥有这个变量的贡献者名。
	Contributor string
	// Key 是声明的受管变量名。
	Key string
	// Description 是变量的模型可见说明。
	Description string
}

// Registry 是受管 `DSH_*` 环境的注册表（官方 `ShellEnvRegistry`，即 `ctx.shellEnv`）。
//
// 并发安全：`Register` 与 `Collect`/`List` 可以并发调用。注册同理是
// effect-scoped 的——`Register` 返回的 disposer 移除贡献者并释放它声明的键。
type Registry struct {
	mu           sync.Mutex
	home         string
	contributors map[string]Contributor
	keyOwners    map[string]string
}

// NewRegistry 造一个注册表。home 是 `DSH_HOME` 的值：空则按官方顺序解析
// （`$DSH_HOME` → `~/.dsh`），见 ResolveHome。
func NewRegistry(home string) *Registry {
	return &Registry{
		home:         ResolveHome(home),
		contributors: map[string]Contributor{},
		keyOwners:    map[string]string{},
	}
}

// Home 返回注册表解析出的 dsh home（`DSH_HOME` 的值）。
func (r *Registry) Home() string { return r.home }

// ResolveHome 解析 dsh home，顺序与官方 `resolveDshHome(config.dshHome)` 一致：
// 显式配置 > `$DSH_HOME` > `~/.dsh`。
//
// 注意这是**官方** `DSH_HOME` 的语义，与本仓宿主自己的 `DSHD_HOME`（默认 `~/.dshd`）
// 不是同一个变量：生产路径上 `cmd/dshd` 会把已解析的 home 显式传进来，
// 这里的兜底只服务"直接构造注册表"的场景。
func ResolveHome(explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv(HomeEnv)); s != "" {
		return s
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".dsh")
	}
	return ".dsh"
}

// Register 注册一个环境贡献者，返回把它撤下来的 disposer。
//
// 失败时返回的 disposer 为 nil，调用方不需要清理。注册期六条失败（文案逐字对齐
// 官方 index.ts:124-146）都是普通 error；只有 collect 期那两条才带错误码。
func (r *Registry) Register(c Contributor) (func(), error) {
	if strings.TrimSpace(c.Name) == "" {
		return nil, fmt.Errorf("bash env contributor name must be non-empty")
	}
	if c.Resolve == nil {
		// 官方靠 TS 类型保证 resolve 存在；Go 的字段可以是 nil，这里补一条 fail-loud，
		// 免得第一次执行时在 collect 里空指针崩掉。
		return nil, fmt.Errorf("bash env contributor %q has no resolver", c.Name)
	}

	// 官方按键的**声明顺序**报第一个违规键；Go 的 map 没有顺序，
	// 所以按键名排序后校验——保证同样的声明每次给出同一句错误。
	keys := make([]string, 0, len(c.Variables))
	for key := range c.Variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.contributors[c.Name]; ok {
		return nil, fmt.Errorf(`bash env contributor %q is already registered`, c.Name)
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, EnvPrefix) || !keySuffixRe.MatchString(key[len(EnvPrefix):]) {
			return nil, fmt.Errorf(`bash env contributor %q declared invalid key %q`, c.Name, key)
		}
		if reservedKeys[key] {
			return nil, fmt.Errorf(`bash env contributor %q cannot own reserved key %q`, c.Name, key)
		}
		if strings.TrimSpace(c.Variables[key].Description) == "" {
			return nil, fmt.Errorf(`bash env contributor %q must describe %q`, c.Name, key)
		}
		if owner, ok := r.keyOwners[key]; ok {
			return nil, fmt.Errorf(`bash env key %q is already owned by contributor %q; contributor %q cannot also own it`, key, owner, c.Name)
		}
	}

	r.contributors[c.Name] = c
	for _, key := range keys {
		r.keyOwners[key] = c.Name
	}

	var mu sync.Mutex
	disposed := false
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if disposed {
			return
		}
		disposed = true
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.contributors, c.Name)
		for _, key := range keys {
			delete(r.keyOwners, key)
		}
	}, nil
}

// RegisterScoped 把一个贡献者挂到内核作用域上：作用域撤销时自动移除，
// 连带释放它声明的键。
//
// 这是官方"注册是 effect-scoped"（`ctx.effect(...)`，index.ts:123-155）在本仓的
// 对应物：本仓没有 fiber，生命周期单位就是 `*kernel.Scope`。
func (r *Registry) RegisterScoped(s *kernel.Scope, c Contributor) error {
	if s == nil {
		return fmt.Errorf("shellenv: RegisterScoped 需要作用域（nil 撤不掉贡献者）")
	}
	dispose, err := r.Register(c)
	if err != nil {
		return err
	}
	s.Effect(dispose)
	return nil
}

// Collect 为一次 shell 工具执行构建受管快照（官方 `ShellEnvRegistry.collect`）。
//
// 顺序：内置事实 → 贡献者（按名字排序）逐个 resolve → 按键名排序返回。
// 贡献者返回未声明的键、或值不是字符串，都会得到带码的 `*ShellEnvError`。
// 返回的 map 是独立副本，调用方随便改。
func (r *Registry) Collect(ctx context.Context) (map[string]string, error) {
	r.mu.Lock()
	values := map[string]string{
		HomeEnv:  r.home,
		ShellKey: "1",
	}
	contributors := make([]Contributor, 0, len(r.contributors))
	for _, c := range r.contributors {
		contributors = append(contributors, c)
	}
	r.mu.Unlock()

	// 官方按 `name.localeCompare` 排序后逐个 resolve（index.ts:183），
	// 让"谁先算"有确定答案——贡献者之间可能读同一份宿主状态。
	sort.Slice(contributors, func(i, j int) bool { return contributors[i].Name < contributors[j].Name })

	// `DSH_NODE_EXECUTABLE` 官方在这里给 `process.execPath`（index.ts:171-172）。
	// 本仓宿主不嵌 Node（R6），这条事实不存在——不注入，见包注释第 1 条。
	if id := spi.SessionFrom(ctx); id != "" {
		values[SessionIDKey] = id
	}
	// `DSH_PROFILE` / `DSH_PROFILE_DIR` / `DSH_INSTALL_DIR` 官方来自
	// `ctx.profileContext`（index.ts:176-181）；本仓没有这一层——不注入，见包注释第 2 条。

	for _, c := range contributors {
		resolved, err := c.Resolve(ctx)
		if err != nil {
			return nil, err
		}
		for key, raw := range resolved {
			if _, declared := c.Variables[key]; !declared {
				return nil, &ShellEnvError{
					Message: fmt.Sprintf(`bash env contributor %q returned undeclared key %q`, c.Name, key),
					Code:    CodeContributionInvalid,
				}
			}
			value, ok := raw.(string)
			if !ok {
				return nil, &ShellEnvError{
					Message: fmt.Sprintf(`bash env contributor %q returned a non-string value for %q`, c.Name, key),
					Code:    CodeContributionInvalid,
				}
			}
			values[key] = value
		}
	}

	// 返回一份按键名排序的独立副本（官方 `Object.freeze(Object.fromEntries(sorted))`）。
	ordered := make(map[string]string, len(values))
	for _, key := range SortedKeys(values) {
		ordered[key] = values[key]
	}
	return ordered, nil
}

// List 枚举贡献者声明的变量，**不执行** resolver（官方 `ShellEnvRegistry.list`）。
//
// 与官方同一条已知边界：这里只有插件贡献的变量，注册表自带的内置事实
// （`DSH_HOME` / `DSH_SHELL` / …）不在列表里——官方把这条记为
// `TODO(bash-env-list-builtins)`（index.ts:203-204），此处如实保留同样的形状。
func (r *Registry) List() []VariableInfo {
	r.mu.Lock()
	out := make([]VariableInfo, 0)
	for _, c := range r.contributors {
		for key, v := range c.Variables {
			out = append(out, VariableInfo{Contributor: c.Name, Key: key, Description: v.Description})
		}
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// SortedKeys 返回映射里按键名排好序的键。
//
// 单独导出是因为两个消费者都要它：bash 侧要把快照拼成 `export` 语句，
// pwsh 侧要拼 `cmd.Env` 列表——两边都必须**确定性有序**，否则同一份快照
// 每次执行给出的环境顺序都不一样。
func SortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
