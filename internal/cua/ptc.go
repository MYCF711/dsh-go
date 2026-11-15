package cua

import "fmt"

// PTCEntryID 是官方约定的 profile 条目 id（`dsh-cua/src/access.ts:59`、`:70`、
// `:72` 三处都按 `options.id === 'ptc-runtime'` 找条目）。
const PTCEntryID = "ptc-runtime"

// PTCEntry 是 profile 里一个可编辑条目在本包看到的样子。
//
// 只要 id 与它的配置：`cuaAccess/ptcConfiguration` 的两个字段（persistent/writable）
// 完全由这两样决定，别的（模块名、依赖、生命周期）都不该被这里解释。
type PTCEntry struct {
	ID     string
	Config map[string]any
}

// PTCProbe 是 PTC 执行状态的注入点。
//
// 为什么是注入：官方这两个字段读的是 `ptcRuntime.retainsState` 与 `configEditor`
// （`access.ts:55-61`），那是宿主进程里的服务；本包里没有（也不该有）那个运行时。
// 宿主接线时把真实的条目列表与写入通道传进来，这里就变成真读数；
// 不接线时**如实**回答"没有 ptc-runtime 条目、不可写"，而不是假装能持久化。
type PTCProbe interface {
	// Configuration 读当前状态。
	Configuration() PTCConfiguration
	// Enable 把内存态执行改成持久化；已经持久化时是纯 no-op（官方 `access.ts:68`）。
	Enable() error
}

// PTCProbeOptions 是真实 probe 的注入参数。
type PTCProbeOptions struct {
	// EntryID 默认 `ptc-runtime`。
	EntryID string
	// Entries 返回 profile 里**当前**的条目（每次调用都重新读，别缓存：
	// profile 是用户手里会变的东西）。
	Entries func() []PTCEntry
	// Write 是"把这个条目的配置改成这些值"的通道，必须走宿主既有的配置写入
	// （profile editor / Loader 生命周期），不要在别处另写一份配置文件。
	Write func(entry PTCEntry, values map[string]any) error
	// PersistentValues 是"持久化"要写进去的值。缺省 `{"persistent": true}`。
	// 官方用 `resolvePersistentExecution(programFormat)` 算这组值（`access.ts:72`）；
	// 宿主装配了真的 ptc-runtime 时应该把它传进来，而不是让本包猜格式。
	PersistentValues func() map[string]any
}

// NewPTCProbe 造一个读真实 profile 条目的 probe。
func NewPTCProbe(options PTCProbeOptions) PTCProbe {
	entryID := options.EntryID
	if entryID == "" {
		entryID = PTCEntryID
	}
	return &profilePTCProbe{entryID: entryID, options: options}
}

type profilePTCProbe struct {
	entryID string
	options PTCProbeOptions
}

// Configuration 读 persistent/writable。
//
// persistent 的判据：条目配置里 `persistent`（或官方的 `retainsState`）为真。
// 两个键都认，是因为官方运行时叫 retainsState、配置文件里叫 persistent——
// 只认一个就会在换装配时把"已经持久化"读成 false，然后重复写一遍配置。
func (p *profilePTCProbe) Configuration() PTCConfiguration {
	entry, found := p.find()
	cfg := PTCConfiguration{
		EntryID: p.entryID, EntryFound: found,
		Persistent: found && (isTrue(entry.Config["persistent"]) || isTrue(entry.Config["retainsState"])),
		Writable:   found && p.options.Write != nil,
	}
	if !cfg.Writable {
		cfg.CapableWhen = ptcCapableWhen
	}
	return cfg
}

// Enable 把内存态执行改成持久化。
//
// 三种结果，一个都不能混：
//   - 已经持久化 → no-op 成功（官方同义：already-persistent runtimes are left intact）；
//   - 有条目且可写 → 真写配置，写失败就报错（绝不"看起来成功了"）；
//   - 没有条目 / 没有写入通道 → 501 + capableWhen（别把"没能力"说成"已持久化"）。
func (p *profilePTCProbe) Enable() error {
	if p.Configuration().Persistent {
		return nil
	}
	entry, found := p.find()
	if !found || p.options.Write == nil {
		// 写路径**保持 501**（码仍是既有的 `unavailable`），但必须把"为什么不可能"讲清楚：
		// 不是"宿主还没接线"，是 R6 拦住了；缺哪两个官方模块；以及官方默认也关着。
		return unavailable(ptcCapableWhen,
			"PTC 运行时没有可编辑的 profile 条目（找的是 %q），本宿主没有 ptcRuntime：%s。"+
				"缺的具体模块是 %s（#74，Node VM/worker 评估器）与 %s（#94，生成式 SDK + run_code）；%s",
			p.entryID, ptcR6Blocker, ptcRuntimeNodeModule, ptcToolExecutorModule, ptcOfficialDefaultNote)
	}
	values := map[string]any{"persistent": true}
	if p.options.PersistentValues != nil {
		values = p.options.PersistentValues()
	}
	if err := p.options.Write(entry, values); err != nil {
		return internalError("写 PTC 持久化配置失败（条目 %q）：%v", entry.ID, err)
	}
	return nil
}

func (p *profilePTCProbe) find() (PTCEntry, bool) {
	if p.options.Entries == nil {
		return PTCEntry{}, false
	}
	for _, entry := range p.options.Entries() {
		if entry.ID == p.entryID {
			return entry, true
		}
	}
	return PTCEntry{}, false
}

func isTrue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "true" || typed == "1"
	case float64:
		return typed != 0
	case int:
		return typed != 0
	}
	return false
}

// 这一族"没有 PTC 运行时"的说法必须点名三件事，缺一件客户端就只能读到含糊的
// "暂不支持"，无法判断是"等宿主接线"还是"永远不会有"：
//
//	(a) 被 `AGENTS.md` §1 红线 **R6「不嵌 JS 运行时」**（`AGENTS.md:56`）拦住——
//	    是**设计约束**，不是"暂未实现"；
//	(b) 缺的具体模块：官方的 `@deepseek-ai/dsh-ptc-runtime-node`（#74，Node 专属实现：
//	    `evaluators/vm-script.ts:3` 用 `node:vm` 的 createContext/Script、
//	    `evaluators/async-function.ts:3` 用 `node:vm` 的 compileFunction、
//	    `evaluators/repl.ts:2` 用 `node:repl`、`worker.ts:1-2` 是受管 Node 进程；
//	    官方 `dsh-base/cordis.patch.yml:407-408` 把它挂在 `id: ptc-runtime` 上）
//	    与 `@deepseek-ai/dsh-tool-executor-ptc`（#94，生成式 SDK + `run_code` 工具面；
//	    官方 `dsh-base/cordis.patch.yml:519-521` 挂在 `id: tool-executor-ptc` 上）；
//	(c) 官方默认本身就把 #94 关着（`dsh-base/cordis.patch.yml:521` 的 `disabled: true`），
//	    所以"没有 run_code 工具面"与官方默认形状一致，不是本仓少做了一块。
//
// 三件事写成共享常量：`capableWhen` 与 message 都要讲同一套事实，抄两遍迟早漂移。
const (
	// ptcRuntimeNodeModule 是官方 #74 的包名（Node VM/worker/REPL 评估器，需要 JS 运行时）。
	ptcRuntimeNodeModule = "@deepseek-ai/dsh-ptc-runtime-node"
	// ptcToolExecutorModule 是官方 #94 的包名（生成式 SDK + run_code 工具面）。
	ptcToolExecutorModule = "@deepseek-ai/dsh-tool-executor-ptc"
	// ptcR6Blocker 是"被哪条边界拦住的"这一句的唯一起点。
	ptcR6Blocker = "被 AGENTS.md §1 红线 R6「不嵌 JS 运行时」拦住（设计约束，不是“暂未实现”）"
	// ptcOfficialDefaultNote 是官方默认形状（file:line 可核）。
	ptcOfficialDefaultNote = "官方默认也没有这条工具面（dsh-base/cordis.patch.yml:521 `disabled: true`）"
)

// ptcCapableWhen 是这一族"没有 PTC 运行时"的补救说明。
const ptcCapableWhen = ptcR6Blocker +
	"：这条能力要的官方模块是 " + ptcRuntimeNodeModule +
	"（#74，Node VM/worker/REPL 评估器：`evaluators/vm-script.ts:3` 与 `evaluators/async-function.ts:3` 用 node:vm、" +
	"`evaluators/repl.ts:2` 用 node:repl）与 " + ptcToolExecutorModule +
	"（#94，生成式 SDK + run_code 工具面）：两者都要在宿主里嵌/起 JS 运行时，R6 下不在本仓可做范围；" +
	ptcOfficialDefaultNote + "，所以本仓的形状与官方默认一致"

// DescribePTC 是给报告/诊断用的一句话（不含运行时行为）。
func DescribePTC(cfg PTCConfiguration) string {
	if cfg.Persistent {
		return "PTC 运行时保留状态"
	}
	if cfg.Writable {
		return "PTC 运行时未保留状态，但条目可写"
	}
	return fmt.Sprintf("PTC 运行时未装配（条目 %q 不存在）：%s，缺 %s（#74）与 %s（#94）",
		cfg.EntryID, ptcR6Blocker, ptcRuntimeNodeModule, ptcToolExecutorModule)
}
