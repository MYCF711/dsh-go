// 本文件是官方 `@deepseek-ai/dsh-skill` 的 **provider 注册面 + 合并语义** 的 Go 投影。
//
// # 官方出处（本机解包树逐行读过，不是猜的）
//
// 源码：`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-skill/src/index.ts`
//
//	:22   RUNTIME_PROVIDER = 'runtime'（保留 provider 名）
//	:23   RUNTIME_RANK = 250（runtime 内嵌 skill 的 precedence）
//	:17   SKILL_NAME = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
//	:70    SkillInvocationPolicy { modelInvocable, userInvocable } —— 四个组合都保留
//	:100   SkillSummary / :118 SkillCandidate（多了 rank/locator）/ :130 SkillDefinition
//	:143   SkillRegistration（invocation 与 provider 可省略）
//	:245   SkillProvider { name, list(options), get(candidate, options) }
//	:296   SkillProviderControl { signal, invalidate() } —— invalidate 只在**这次注册**
//	       仍然在场时生效（:291-296 的 active 判定）
//	:430   registerProvider(create)：同步注册，`runtime` 保留名抛错，同层重名抛错
//	:474   register(skill)：validation 后同层同名 first-wins + `logger.warn`，返回 no-op disposer
//	:496   list(options) / :512 snapshot(options) → { skills, complete }
//	:540   get(name, options)：名字必须先过 isSkillName，否则 undefined；
//	       载入后**重新校验**定义，`definition.name !== candidate.name` 视为过期：
//	       让缓存失效并返回 undefined（:552-558）
//	:600   collectLayer：先 runtime、再各 provider 的 list()；
//	       provider 抛错 → cacheable=false + `logger.warn`（不丢其它 provider 的结果）；
//	       provider 返回 {candidates, complete:false} → 也算不完整，但**候选照样算数**
//	:615   listLayerCandidates 的索引：(rank, providerOrder, localOrder)，
//	       runtime 的 providerOrder 固定 -1
//	:574   collectLayer 的排序后 first-wins + `logger.warn`（被遮掉的候选可观测）
//	:936   compareSkillSummary = name 码位序升序
//	:956   validateCandidate：非字符串名 / 非 kebab-case / description 空 /
//	       invocation 非对象或非 bool / source 非字符串 / rank 非有限数 /
//	       provider 与注册名不一致 / path 非字符串 —— 一律抛错
//	:1008  validateRuntimeSkill：名字必须 kebab-case，description 不能空
//	:1020  validateDefinition：载入回来的定义同样逐字段校验
//
// 包 README（同目录 `README.md`，"Use this package" 一节）：
//
//   - "The registry merges everything any provider reports"，"sorted by name"；
//   - 四个 invocation 组合**全部保留**，模型面只看 `modelInvocable`、
//     人类面只看 `userInvocable`；
//   - "A provider that fails discovery is logged and skipped, and the observation
//     is reported incomplete so consumers can distinguish failed discovery from
//     confirmed absence"；
//   - "Embedded skills ... registry fills in a default invocation policy and the
//     `runtime` provider label. Same-name runtime registrations in one layer are
//     first-wins with a warning"；
//   - "the registry re-validates the loaded definition and rejects a stale
//     selection whose name changed between discovery and load"。
//
// # 结构与官方的一处刻意差异（已登记）
//
// 官方的 SkillRegistry 是 **host + 每 scope 分层**（`ScopedLayers`，:420-440）：preset
// 的常驻编排挂进来的 provider 落在该 preset 的层，读的时候"近层整条盖掉远层"。
// 本宿主**没有 preset 级 skill provider**（见 `cmd/dshd/serve_agent_api_skills.go`
// 顶部第 3 条：preset 不改变"有哪些 skill"），所以这里只实现**全局层**：
// 层内按 (rank, providerOrder, localOrder) 去重，与官方同层语义逐字一致；
// 跨层 shadowing 留空，等真有 preset 级 provider 时再按官方 ScopedLayers 补。
//
// # 缓存归属
//
// 官方把"已完成收集"缓存在 registry 里（`collectCacheMaxEntries`，默认 128，
// 有 revision 失效）。本宿主的 `skill` 工具**已有**自己的 TTL 缓存
// （`DSHD_SKILLS_CACHE_TTL_MS` / `DSHD_SKILLS_WATCH_POLL_MS` /
// `DSHD_SKILLS_COLLECT_CACHE_MAX`，见 skill.go），再叠一层只会让"改完 skill
// 要等两层失效"变成新坑。所以这里只暴露 **revision**（每次注册/失效 +1），
// 缓存留在消费者那一层：`Tool.collect` 把 revision 一起缓存，revision 一变
// 立刻重扫。官方"失效即清缓存"的可观测行为由此等价成立（用例
// TestStaleDefinitionInvalidatesCachedCatalog 断言）。
package skill

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	// runtimeProviderName 是官方保留 provider 名（`RUNTIME_PROVIDER`，src/index.ts:22）。
	// 内嵌/运行时 skill 只借用这个标签，**不许**被 registerProvider 占用。
	runtimeProviderName = "runtime"
	// runtimeRank 是 runtime 内嵌 skill 的 precedence（`RUNTIME_RANK`，src/index.ts:23）。
	// 数值必须与官方逐字相等：它决定 runtime skill 与各 provider 候选谁赢。
	runtimeRank = 250
)

// skillNamePattern 是官方 `SKILL_NAME`（src/index.ts:17）。名字不合形状的 skill
// 官方**整条不认**：`get()` 直接 undefined，provider 候选直接抛错。
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// IsSkillName 判断一个字符串是不是合法的 kebab-case skill 名。
// 与官方 `isSkillName`（src/index.ts:35-38）同一判据。
func IsSkillName(name string) bool { return skillNamePattern.MatchString(name) }

// ---------------------------------------------------------------------------
// 策略与元数据
// ---------------------------------------------------------------------------

// InvocationPolicy 是官方 `SkillInvocationPolicy`（src/index.ts:70-77）的投影：
// 每个 skill 带两个面，四个组合**都合法**（README 的表格）。
//
// 字段不导出、只能由本包构造：官方 `validateInvocation` 要求 invocation 在场时
// 两个字段都必须是 bool（src/index.ts:1088-1096），而 Go 里裸 bool 区分不了
// 「显式两个 false」与「调用方根本没给 policy」。给一个内部 `given` 标记，
// 零值就代表"非法组合/没给"，注册与合并时 fail-loud —— 不让它静默变成
// 「模型面看不见、人类面也看不见」的幽灵 skill。
type InvocationPolicy struct {
	modelInvocable bool
	userInvocable  bool
	given          bool
}

// NewInvocationPolicy 构造一份显式的调用策略（四个组合由两个实参覆盖）。
func NewInvocationPolicy(modelInvocable, userInvocable bool) InvocationPolicy {
	return InvocationPolicy{modelInvocable: modelInvocable, userInvocable: userInvocable, given: true}
}

// BothInvocable：{true,true} —— 模型面与人类面都宣传。
func BothInvocable() InvocationPolicy { return NewInvocationPolicy(true, true) }

// ModelOnlyInvocable：{true,false} —— 只给模型。
func ModelOnlyInvocable() InvocationPolicy { return NewInvocationPolicy(true, false) }

// UserOnlyInvocable：{false,true} —— 只给人类（官方 `disable-model-invocation`）。
func UserOnlyInvocable() InvocationPolicy { return NewInvocationPolicy(false, true) }

// NotInvocable：{false,false} —— 两面都不宣传（只能被显式点名注入）。
func NotInvocable() InvocationPolicy { return NewInvocationPolicy(false, false) }

// ModelInvocable 报"模型面（`skill` 工具的 list/search/加载）能不能选它"。
func (p InvocationPolicy) ModelInvocable() bool { return p.modelInvocable }

// UserInvocable 报"人类面（命令 / `skills.list` 目录）能不能选它"。
func (p InvocationPolicy) UserInvocable() bool { return p.userInvocable }

// Given 报这份策略是不是显式给过（零值 = 没给，注册与合并会 fail-loud）。
func (p InvocationPolicy) Given() bool { return p.given }

// ParseInvocationPolicy 严格解析一份**未类型化**的 invocation 声明。
//
// 这是官方 `validateInvocation`（src/index.ts:1088-1096）的投影，给非 Go 来源
// （远端 provider 解 JSON、SKILL.md frontmatter 解 YAML）用：`modelInvocable` 与
// `userInvocable` **都必须在场且是 bool**；缺键、非 bool（例如 "yes" 字符串、
// 1 数字）一律返回错误，绝不静默补默认值。
func ParseInvocationPolicy(raw map[string]any) (InvocationPolicy, error) {
	model, ok := raw["modelInvocable"]
	if !ok {
		return InvocationPolicy{}, fmt.Errorf("invocation 缺 modelInvocable（两个字段都必须显式给 bool）")
	}
	user, ok := raw["userInvocable"]
	if !ok {
		return InvocationPolicy{}, fmt.Errorf("invocation 缺 userInvocable（两个字段都必须显式给 bool）")
	}
	modelBool, ok := model.(bool)
	if !ok {
		return InvocationPolicy{}, fmt.Errorf("invocation.modelInvocable 不是 bool（拿到 %T）", model)
	}
	userBool, ok := user.(bool)
	if !ok {
		return InvocationPolicy{}, fmt.Errorf("invocation.userInvocable 不是 bool（拿到 %T）", user)
	}
	return NewInvocationPolicy(modelBool, userBool), nil
}

// ResourceBase 是官方 `SkillResourceBase`（src/index.ts:53-58）的投影：
// 正文里相对资源按哪份基准解析。文件系统 provider 给 directory。
type ResourceBase struct {
	// Kind 只取 "directory" / "url" / "opaque"（官方闭集）。
	Kind string
	// Path 是 Kind=="directory" 时的目录。
	Path string
	// URL 是 Kind=="url" 时的基准。
	URL string
	// Description 是 Kind=="opaque" 时给模型看的说明。
	Description string
}

// Summary 是官方 `SkillSummary`（src/index.ts:79-97）：与调用面无关的元数据。
type Summary struct {
	// Path 是文件系统 provider 给的绝对 SKILL.md 路径；虚拟 skill 没有。
	Path string
	// Name 是 kebab-case 名字（寻址用）。
	Name string
	// Description 是短路由描述。
	Description string
	// WhenToUse 是可选补充路由指引；空 = 省略。
	WhenToUse string
	// Invocation 是解析后的调用策略。
	Invocation InvocationPolicy
	// Source 是官方 `SkillSource` 名（project-dsh/custom/user-dsh/runtime/…）。
	Source string
	// Provider 是拥有这条正文的 provider 名。
	Provider string
	// ResourceBase 是可选资源基准。
	ResourceBase *ResourceBase
}

// Candidate 是官方 `SkillCandidate`（src/index.ts:104-116）：摘要 + 去重键 + locator。
type Candidate struct {
	Summary
	// Rank 越大越靠后：同层同名时 rank 小的先赢（官方 precedence）。
	Rank int
	// Locator 是 provider 自己的不透明句柄，合并时原样保存、get 时原样传回。
	Locator any
}

// Definition 是官方 `SkillDefinition`（src/index.ts:119-125）：完整定义含正文。
type Definition struct {
	Summary
	// Content 是去掉 frontmatter 之后的 Markdown 正文。
	Content string
}

// Observation 是官方 `SkillProviderObservation`（src/index.ts:190-196）：
// 候选 + 「这次发现是不是权威」。官方还允许 provider 直接返回数组（等价
// complete=true）；Go 只有一种形状，provider 必须显式给 Complete ——
// 默默漏掉它只会被当成**不完整**（偏保守，不会把失败伪装成"确认没有"）。
type Observation struct {
	// Candidates 是本次发现到的候选（可以不完整）。
	Candidates []Candidate
	// Complete 为真才是"这次发现权威"；假 → 消费者的观察标记 incomplete。
	Complete bool
}

// LookupOptions 是官方 `SkillLookupOptions`（src/index.ts:132-138）：
// 本宿主只用到 cwd（项目根选择）。signal 由 ctx 承担。
type LookupOptions struct {
	// Cwd 是会话工作目录；空 = 不选项目根。
	Cwd string
}

// Provider 是官方 `SkillProvider`（src/index.ts:245-278）的投影。
type Provider interface {
	// Name 是注册名（同层唯一；`runtime` 保留）。
	Name() string
	// List 返回当前上下文的候选摘要；失败要返回 error（registry 记日志 + 跳过），
	// 部分成功要返回 Complete=false 的 Observation（候选照样算数）。
	List(ctx context.Context, options LookupOptions) (Observation, error)
	// Get 按 locator 载入完整定义；候选已不可载入时返回 (nil, nil)（官方 undefined）。
	Get(ctx context.Context, candidate Candidate, options LookupOptions) (*Definition, error)
}

// ProviderControl 是官方 `SkillProviderControl`（src/index.ts:281-287）的投影。
//
// Invalidate 只在**这次注册**仍然在场时生效：注册被释放后再调用它是 no-op，
// 不能扰动同名的后来者（官方 :291-296 的 active 判定同一语义）。
type ProviderControl struct {
	invalidate func()
}

// Invalidate 让 registry 的 revision +1（消费者据此清掉自己的收集缓存）。
// 零值安全：未经 registry 构造的 control 调用它什么都不做。
func (c ProviderControl) Invalidate() {
	if c.invalidate != nil {
		c.invalidate()
	}
}

// Registration 是官方 `SkillRegistration`（src/index.ts:143-151）的投影：
// 内嵌/运行时 skill。Invocation 省略 = 官方默认 {true,true}；Provider 省略 = "runtime"。
type Registration struct {
	Path         string
	Name         string
	Description  string
	WhenToUse    string
	Source       string
	ResourceBase *ResourceBase
	Content      string
	// Invocation 为 nil = 省略（官方默认两面都可调用）。
	Invocation *InvocationPolicy
	// Provider 空 = "runtime"（官方 `skill.provider ?? RUNTIME_PROVIDER`）。
	Provider string
}

// Catalog 是官方 `SkillCatalogSnapshot`（src/index.ts:174-181）的投影。
type Catalog struct {
	// Skills 是按 name 码位序排好的胜出摘要。
	Skills []Summary
	// Complete 为假 = 本次发现不完整（有 provider 失败/自报不完整），
	// **不等于"确认没有"**：消费者必须把两者分开说。
	Complete bool

	// entries 是 name → 胜出候选，供同包消费者按名载入正文（不导出：
	// 跨包消费者只该看 Skills，官方也只暴露 summaries）。
	entries map[string]indexedCandidate
}

// inline load 用 context 与 options 由调用方传入。
type indexedCandidate struct {
	candidate     Candidate
	provider      *providerRegistration
	providerOrder int
	localOrder    int
	// definition 只有 runtime 内嵌 skill 有（正文在内存里，不需要再问 provider）。
	definition *Definition
}

// providerRegistration 是一条 provider 注册（官方 `RegisteredProvider`，src/index.ts:339-344）。
type providerRegistration struct {
	provider Provider
	name     string
	order    int
	active   bool
}

// RegistryStats 是"到底发生了什么"的可断言计数器。
//
// 官方只用 logger.warn；本宿主再给一份计数，因为"日志里有"在测试里不可断言、
// 在真机上也会被吞。**不静默**的可观测性由这两者共同保证。
type RegistryStats struct {
	// Providers 是当前在场的 provider 数。
	Providers int
	// RuntimeSkills 是当前在场的 runtime skill 数。
	RuntimeSkills int
	// ProviderListFailures 是 list() 报错被跳过的次数。
	ProviderListFailures uint64
	// IncompleteObservations 是 provider 自报 complete=false 的次数。
	IncompleteObservations uint64
	// ShadowedCandidates 是同层里被更高优先级挡掉的候选数（官方 :574 的 warn）。
	ShadowedCandidates uint64
	// DuplicateRuntimeRegistrations 是同名 runtime 注册被忽略的次数（官方 :479 的 warn）。
	DuplicateRuntimeRegistrations uint64
	// StaleDefinitionInvalidations 是"载入回来的 name 与选中候选不一致"的次数
	// （官方 :552-558：视为过期，让缓存失效）。
	StaleDefinitionInvalidations uint64
	// Invalidations 是 revision 增加的次数（注册/释放/provider 主动失效）。
	Invalidations uint64
}

// Registry 是官方 `SkillRegistry` 的 Go 投影（只实现全局层，见文件头）。
//
// 并发：所有内部状态由 mu 保护；provider.List/Get 在**不持锁**的情况下调用，
// 所以 provider 自己可以慢慢扫盘，也不会因为回调进来死锁。
type Registry struct {
	mu sync.Mutex

	providers []*providerRegistration
	nextOrder int
	runtime   map[string]*Definition
	revision  uint64
	stats     RegistryStats

	warnMu sync.Mutex
	warn   func(msg string)
}

// NewRegistry 建一个空 registry。默认的 warning 出口是 stderr（与宿主其它
// 警告一致）；测试用 SetWarningSink 换掉它来断言"没有静默"。
func NewRegistry() *Registry {
	return &Registry{
		runtime: map[string]*Definition{},
		warn: func(msg string) {
			fmt.Fprintf(os.Stderr, "skill: %s\n", msg)
		},
	}
}

// SetWarningSink 替换警告出口（nil = 关掉，只保留计数器）。
func (r *Registry) SetWarningSink(sink func(msg string)) {
	r.warnMu.Lock()
	defer r.warnMu.Unlock()
	r.warn = sink
}

func (r *Registry) warningf(format string, args ...any) {
	r.warnMu.Lock()
	sink := r.warn
	r.warnMu.Unlock()
	if sink != nil {
		sink(fmt.Sprintf(format, args...))
	}
}

// Revision 是当前收集代次：每次注册/释放/失效 +1。消费者把它和自己的缓存
// 一起存，用来判断"缓存还能不能用"。
func (r *Registry) Revision() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revision
}

// Stats 是计数器快照。
func (r *Registry) Stats() RegistryStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := r.stats
	stats.Providers = len(r.providers)
	stats.RuntimeSkills = len(r.runtime)
	return stats
}

func (r *Registry) bumpRevisionLocked() {
	r.revision++
	r.stats.Invalidations++
}

// Invalidate 让 revision +1（宿主侧的"外部世界可能变了"入口，例如工具发现
// 磁盘签名变化）。官方没有这个公开方法，等价能力是 provider 自己的 control.invalidate()。
func (r *Registry) Invalidate() {
	r.mu.Lock()
	r.bumpRevisionLocked()
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// 注册面
// ---------------------------------------------------------------------------

// RegisterProvider 是官方 `registerProvider`（src/index.ts:430-472）的投影。
//
// 同步注册（远端初始化属于 provider.List 内部的事）；`runtime` 是保留名；
// 同名（同层）重复注册报错；返回的 disposer 撤销注册并让缓存失效。
func (r *Registry) RegisterProvider(create func(control ProviderControl) (Provider, error)) (func(), error) {
	if create == nil {
		return nil, fmt.Errorf("skill registerProvider 需要 provider 工厂")
	}
	registration := &providerRegistration{}
	control := ProviderControl{invalidate: func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		// 官方 :291-296：只有**这次注册**仍然在场时 invalidate 才生效。
		if registration.active {
			r.bumpRevisionLocked()
		}
	}}
	provider, err := create(control)
	if err != nil {
		return nil, fmt.Errorf("skill registerProvider 的工厂失败：%w", err)
	}
	if provider == nil {
		return nil, fmt.Errorf("skill registerProvider 的工厂返回了 nil provider")
	}
	name := strings.TrimSpace(provider.Name())
	if name == "" {
		return nil, fmt.Errorf("skill provider 名不能为空")
	}
	if name == runtimeProviderName {
		return nil, fmt.Errorf("skill provider 名 %q 是保留名（runtime skill 专用）", runtimeProviderName)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.providers {
		if existing.name == name {
			return nil, fmt.Errorf("skill provider %q 已经注册过（同一层不允许重名）", name)
		}
	}
	registration.provider = provider
	registration.name = name
	registration.order = r.nextOrder
	registration.active = true
	r.nextOrder++
	r.providers = append(r.providers, registration)
	r.bumpRevisionLocked()

	var once sync.Once
	dispose := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if !registration.active {
				return
			}
			registration.active = false
			for i, existing := range r.providers {
				if existing == registration {
					r.providers = append(r.providers[:i], r.providers[i+1:]...)
					break
				}
			}
			r.bumpRevisionLocked()
		})
	}
	return dispose, nil
}

// Register 是官方 `register`（src/index.ts:474-494）的投影：内嵌/运行时 skill。
//
// 校验与官方 `validateRuntimeSkill` 一致（kebab-case 名 + 非空 description +
// 合法 policy）；同层同名 first-wins + 警告 + 计数，返回 no-op disposer
// （第二个同名注册不能把赢家删掉）。
func (r *Registry) Register(registration Registration) (func(), error) {
	registration.Name = strings.TrimSpace(registration.Name)
	registration.Description = strings.TrimSpace(registration.Description)
	if !IsSkillName(registration.Name) {
		return nil, fmt.Errorf("runtime skill 名 %q 不是 kebab-case", registration.Name)
	}
	if registration.Description == "" {
		return nil, fmt.Errorf("runtime skill %q 缺 description", registration.Name)
	}
	invocation := BothInvocable()
	if registration.Invocation != nil {
		given := *registration.Invocation
		if !given.Given() {
			return nil, fmt.Errorf("runtime skill %q 的 invocation 没有显式给（两个字段都必须是 bool）", registration.Name)
		}
		invocation = given
	}
	provider := strings.TrimSpace(registration.Provider)
	if provider == "" {
		provider = runtimeProviderName
	}
	if provider != runtimeProviderName && !IsSkillName(provider) {
		return nil, fmt.Errorf("runtime skill %q 的 provider %q 不是合法名字", registration.Name, provider)
	}
	definition := &Definition{
		Summary: Summary{
			Path:         registration.Path,
			Name:         registration.Name,
			Description:  registration.Description,
			WhenToUse:    registration.WhenToUse,
			Invocation:   invocation,
			Source:       registration.Source,
			Provider:     provider,
			ResourceBase: registration.ResourceBase,
		},
		Content: registration.Content,
	}

	r.mu.Lock()
	if _, exists := r.runtime[definition.Name]; exists {
		r.stats.DuplicateRuntimeRegistrations++
		r.mu.Unlock()
		// 官方的 no-op disposer：第二次同名注册不能删掉赢家。
		r.warningf("runtime skill %q 已存在，后注册的这条被忽略（同层 first-wins）", definition.Name)
		return func() {}, nil
	}
	r.runtime[definition.Name] = definition
	r.bumpRevisionLocked()
	r.mu.Unlock()

	var once sync.Once
	dispose := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			current, exists := r.runtime[definition.Name]
			if !exists || current != definition {
				return
			}
			delete(r.runtime, definition.Name)
			r.bumpRevisionLocked()
		})
	}
	return dispose, nil
}

// ---------------------------------------------------------------------------
// 读面
// ---------------------------------------------------------------------------

// List 是官方 `list`（src/index.ts:496-508）：只要摘要。
func (r *Registry) List(ctx context.Context, options LookupOptions) ([]Summary, error) {
	catalog, err := r.Snapshot(ctx, options)
	if err != nil {
		return nil, err
	}
	return catalog.Skills, nil
}

// Snapshot 是官方 `snapshot`（src/index.ts:512-529）：摘要 + 是否完整。
func (r *Registry) Snapshot(ctx context.Context, options LookupOptions) (Catalog, error) {
	return r.collect(ctx, options)
}

// Get 是官方 `get`（src/index.ts:540-560）的投影。
//
//   - 名字先过 isSkillName：不合法直接"没有这个 skill"（返回 nil,nil）；
//   - 选中候选后由拥有它的 provider 载入正文（runtime 内嵌 skill 用内存里的定义）；
//   - 载入回来的定义重新校验（官方 validateDefinition）；
//   - 定义名与选中候选不一致 = 过期：让缓存失效并返回"没有"
//     （官方 :552-558）。绝不把另一个 skill 的正文当成这个名字的内容返回。
func (r *Registry) Get(ctx context.Context, name string, options LookupOptions) (*Definition, error) {
	if !IsSkillName(name) {
		return nil, nil
	}
	catalog, err := r.collect(ctx, options)
	if err != nil {
		return nil, err
	}
	entry, ok := catalog.entries[name]
	if !ok {
		return nil, nil
	}
	return r.load(ctx, entry, options)
}

// loadFrom 按名在**已经收集好的**快照里载入正文（供同包的检索用：省一次全量收集）。
func (c Catalog) loadFrom(ctx context.Context, registry *Registry, name string, options LookupOptions) (*Definition, error) {
	entry, ok := c.entries[name]
	if !ok {
		return nil, nil
	}
	return registry.load(ctx, entry, options)
}

func (r *Registry) load(ctx context.Context, entry indexedCandidate, options LookupOptions) (*Definition, error) {
	if entry.definition != nil {
		return entry.definition, nil
	}
	if entry.provider == nil {
		return nil, nil
	}
	loaded, err := entry.provider.provider.Get(ctx, entry.candidate, options)
	if err != nil {
		return nil, fmt.Errorf("skill provider %q 载入 %q 失败：%w", entry.provider.name, entry.candidate.Name, err)
	}
	if loaded == nil {
		// 官方 undefined：候选在这期间没了，不是错误。
		return nil, nil
	}
	if loaded.Provider == "" {
		loaded.Provider = entry.provider.name
	}
	if err := ValidateDefinition(*loaded); err != nil {
		return nil, fmt.Errorf("skill provider %q 返回的定义非法：%w", entry.provider.name, err)
	}
	if loaded.Name != entry.candidate.Name {
		r.mu.Lock()
		if entry.provider.active {
			r.stats.StaleDefinitionInvalidations++
			r.bumpRevisionLocked()
		} else {
			r.stats.StaleDefinitionInvalidations++
		}
		r.mu.Unlock()
		r.warningf("skill provider %q 载入 %q 返回了 %q：视为过期，让缓存失效",
			entry.provider.name, entry.candidate.Name, loaded.Name)
		return nil, nil
	}
	return loaded, nil
}

// collect 是官方 `collectFresh`（src/index.ts:536-570）+ `collectLayer`（:574-596）
// 的投影，**只做一层**（见文件头）。
func (r *Registry) collect(ctx context.Context, options LookupOptions) (Catalog, error) {
	// 先把在场状态拷出来：provider.List 在锁外调用。
	r.mu.Lock()
	providers := append([]*providerRegistration(nil), r.providers...)
	runtimeDefs := make(map[string]*Definition, len(r.runtime))
	for name, definition := range r.runtime {
		runtimeDefs[name] = definition
	}
	r.mu.Unlock()

	// 1) runtime 内嵌 skill 先入场：providerOrder 固定 -1（官方 :615 的 runtimeOrder）。
	runtimeNames := make([]string, 0, len(runtimeDefs))
	for name := range runtimeDefs {
		runtimeNames = append(runtimeNames, name)
	}
	sort.Strings(runtimeNames) // 官方 :528 圈：runtime 候选按 name 码位序，localOrder 依次递增

	indexed := make([]indexedCandidate, 0, len(runtimeNames))
	for order, name := range runtimeNames {
		definition := runtimeDefs[name]
		indexed = append(indexed, indexedCandidate{
			candidate: Candidate{
				Summary: definition.Summary,
				Rank:    runtimeRank,
				Locator: definition,
			},
			providerOrder: -1,
			localOrder:    order,
			definition:    definition,
		})
	}

	// 2) 每个 provider 依次 list()：失败 → 记日志 + 跳过（不丢别人的结果）+ 标不完整。
	complete := true
	for _, registration := range providers {
		observation, err := registration.provider.List(ctx, options)
		if err != nil {
			r.mu.Lock()
			r.stats.ProviderListFailures++
			r.mu.Unlock()
			complete = false
			r.warningf("skill provider %q 本次 list() 失败，已跳过（其它 provider 的结果仍然可用）：%v",
				registration.name, err)
			continue
		}
		if !observation.Complete {
			r.mu.Lock()
			r.stats.IncompleteObservations++
			r.mu.Unlock()
			complete = false
			r.warningf("skill provider %q 自报本次发现不完整：候选仍然算数，观察标为 incomplete", registration.name)
		}
		for localOrder, candidate := range observation.Candidates {
			if err := validateCandidate(candidate, registration.name); err != nil {
				return Catalog{}, err
			}
			indexed = append(indexed, indexedCandidate{
				candidate:     candidate,
				provider:      registration,
				providerOrder: registration.order,
				localOrder:    localOrder,
			})
		}
	}

	// 3) 同层去重顺序：rank → provider 注册顺序 → provider 内顺序（官方
	//    compareIndexedCandidates，:945-951）。排序后 first-wins。
	sort.SliceStable(indexed, func(i, j int) bool {
		left, right := indexed[i], indexed[j]
		if left.candidate.Rank != right.candidate.Rank {
			return left.candidate.Rank < right.candidate.Rank
		}
		if left.providerOrder != right.providerOrder {
			return left.providerOrder < right.providerOrder
		}
		return left.localOrder < right.localOrder
	})

	entries := make(map[string]indexedCandidate, len(indexed))
	for _, entry := range indexed {
		if _, shadowed := entries[entry.candidate.Name]; shadowed {
			r.mu.Lock()
			r.stats.ShadowedCandidates++
			r.mu.Unlock()
			r.warningf("skill %q（source=%s）被更高优先级的同名项遮掉（同层 first-wins）",
				entry.candidate.Name, entry.candidate.Source)
			continue
		}
		entries[entry.candidate.Name] = entry
	}

	// 4) 对外按 name 码位序升序（官方 compareSkillSummary，:936-938）。
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	skills := make([]Summary, 0, len(names))
	for _, name := range names {
		skills = append(skills, entries[name].candidate.Summary)
	}
	return Catalog{Skills: skills, Complete: complete, entries: entries}, nil
}

// ---------------------------------------------------------------------------
// 校验（官方 :956-1050 的投影）
// ---------------------------------------------------------------------------

// validateCandidate 校验一条 provider 候选。官方 `validateCandidate` 抛错即
// "这条 provider 的整个发现结果作废"，本实现原样返回错误（fail-loud）。
//
// 与官方的**一处放宽**（已登记）：官方要求 description 非空，而本宿主的
// `skill` 工具历来允许"没有 frontmatter 的裸 SKILL.md"（用例
// TestListShowsNameAndDescription 的 `no-front`）。空描述宁可留空，也不许编
// 一句假的；人类面的 `skills.list` 仍按官方要求 description 非空，由它自己的
// frontmatter 解析保证（serve_agent_api_skills.go）。
func validateCandidate(candidate Candidate, providerName string) error {
	if candidate.Name == "" {
		return fmt.Errorf("skill provider %q 返回了空名字的候选", providerName)
	}
	if !IsSkillName(candidate.Name) {
		return fmt.Errorf("skill provider %q 返回了非法 skill 名 %q（必须 kebab-case）",
			providerName, candidate.Name)
	}
	if candidate.Provider != providerName {
		return fmt.Errorf("skill provider %q 返回了属于 %q 的候选 %q（provider 归属必须与注册名一致）",
			providerName, candidate.Provider, candidate.Name)
	}
	if !candidate.Invocation.Given() {
		return fmt.Errorf("skill provider %q 的候选 %q 没有显式 invocation 策略"+
			"（modelInvocable/userInvocable 都必须是 bool）", providerName, candidate.Name)
	}
	if candidate.ResourceBase != nil {
		if err := validateResourceBase(*candidate.ResourceBase); err != nil {
			return fmt.Errorf("skill provider %q 的候选 %q：%w", providerName, candidate.Name, err)
		}
	}
	return nil
}

func validateResourceBase(base ResourceBase) error {
	switch base.Kind {
	case "directory":
		if base.Path == "" {
			return fmt.Errorf("resourceBase.kind=directory 但 path 为空")
		}
	case "url":
		if base.URL == "" {
			return fmt.Errorf("resourceBase.kind=url 但 url 为空")
		}
	case "opaque":
		if base.Description == "" {
			return fmt.Errorf("resourceBase.kind=opaque 但 description 为空")
		}
	default:
		return fmt.Errorf("resourceBase.kind %q 不在官方闭集里（directory/url/opaque）", base.Kind)
	}
	return nil
}

// ValidateDefinition 校验 provider 载入回来的完整定义（官方 `validateDefinition`，:1020-1050）。
// 导出的理由：非文件系统 provider 的实现在别处，也要能在自己的用例里对着同一判据断言。
func ValidateDefinition(definition Definition) error {
	if !IsSkillName(definition.Name) {
		return fmt.Errorf("载入回来的 skill 名 %q 不是 kebab-case", definition.Name)
	}
	if !definition.Invocation.Given() {
		return fmt.Errorf("载入回来的 skill %q 没有显式 invocation 策略", definition.Name)
	}
	if definition.Provider == "" {
		return fmt.Errorf("载入回来的 skill %q 没有 provider 归属", definition.Name)
	}
	if definition.ResourceBase != nil {
		if err := validateResourceBase(*definition.ResourceBase); err != nil {
			return fmt.Errorf("载入回来的 skill %q：%w", definition.Name, err)
		}
	}
	return nil
}
