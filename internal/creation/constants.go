package creation

// 官方 `create_personalized_page` 卡片形状的逐字落地。
//
// 唯一来源（不许自己发明字段名）：
//
//	app.asar 0.2.2-rc.2 / node_modules/@deepseek-ai/dsh-creation-recommendations/
//	src/index.ts（卡片注册与 Creator 任务）、src/types.ts（CreationSlot /
//	PageDetails / PagePhase / PageData / PageVerb / PageLocation）、
//	src/generated-bundle.ts（生成物校验）、src/skill.ts（skill 注册）。
//
// 本文件的每个常量都能在官方源码里逐字找到；注释写的是"官方哪一行"。
// 任何拼写都不许改：GUI（#13 creation-recommendations 条目）按这些字面量
// 渲染卡片与状态文案。

// Action 是官方注册的 kind 名（src/index.ts `const ACTION = 'create_personalized_page'`）。
const Action = "create_personalized_page"

// Skill 是 propose_action 前必须加载的 skill（src/index.ts `skill: 'personalized-pages'`）。
const Skill = "personalized-pages"

// Invitation 是官方给模型的邀请句（src/index.ts `invitation:`，逐字，含弯引号 ’）。
//
// 模型只在卡片存在时用它收尾；换了措辞就等于改了产品文案。
const Invitation = "End with at most one short invitation in the user’s language, in the first person: after they click Create below, " +
	"you will build it in the background as a plugin page that stays available, without blocking the chat; " +
	"the build takes minutes and many tokens."

// StatusGuidance 是官方卡片状态的说明文案（src/index.ts `statusGuidance:`，逐字）。
//
// 宿主没有"卡片渲染层"可挂，所以它作为常量导出给接线方/GUI 对照；本包的
// decision 只负责把 phase 记对，不自己解释 phase。
const StatusGuidance = "Cancelled means a started background creation was stopped before installation. " +
	"Cancelled, failed or interrupted runs can be retried from their existing card; no new proposal is needed. " +
	"A removed page can be restored from its card while its files remain; if they are gone, a new proposal is needed."

// ReservedPrefix 是官方生成包的保留名前缀（src/index.ts
// `const RESERVED_PREFIX = '@dsh-local/view-'`）。
//
// 官方用它拼 npm 包名：`@dsh-local/view-<uuid>`。这个名字是**作用域名**，
// 含 `@` 与 `/`；本宿主 pluginmgr 的包 id 形状是
// `^[a-z0-9][a-z0-9._-]{0,63}$`（internal/pluginmgr/manifest.go 的
// pluginIDPattern），装不了这个拼写。所以宿主用同一个 uuid 的
// HostBundlePrefix（见下）；官方拼写保留在这里，供 skill/GUI/文档对照，
// 不许拿它去 pluginmgr 里查包（永远查不到，会被当成"没装"）。
const ReservedPrefix = "@dsh-local/view-"

// HostBundlePrefix 是**宿主保留名**的前缀：与官方同一 uuid、同一保留语义
// （"Host-reserved name of the created bundle"，src/types.ts 的 PageData.bundleName），
// 拼写换成 pluginmgr 装得下的形状。
const HostBundlePrefix = "dsh-local-view-"

// 官方 `CreationSlot` 的三个枚举值（src/types.ts `CreationSlot`）。
const (
	// SlotSidebarRight 是会话右侧栏；适合"边聊边看"的内容。
	SlotSidebarRight = "sidebar-right"
	// SlotPage 是会话视图的一个 tab（Chat/Trajectory 旁边）。
	SlotPage = "page"
	// SlotSidebarLeft 是左侧入口 + 全局独立页面。
	SlotSidebarLeft = "sidebar-left"
)

// Slots 返回官方 slot 枚举（顺序照 src/index.ts 的 zod `z.enum([...])`）。
func Slots() []string { return []string{SlotSidebarRight, SlotPage, SlotSidebarLeft} }

// 官方 `PageVerb`（src/types.ts）——卡片自己的四个动词；共享的 `dismiss`
// 由 actioncards 提供，不在 kind 的 Verbs 里。
const (
	VerbCreate  = "create"
	VerbUndo    = "undo"
	VerbCancel  = "cancel"
	VerbRestore = "restore"
)

// 官方 `PagePhase`（src/types.ts）：卡片除共享 `dismissed` 之外的 phase。
//
// `kept` 只出现在更早的构建里，读起来等同 `installed`。
const (
	PhaseCreating    = "creating"
	PhaseInstalling  = "installing"
	PhaseCancelled   = "cancelled"
	PhaseInstalled   = "installed"
	PhaseKept        = "kept"
	PhaseRemoved     = "removed"
	PhaseFailed      = "failed"
	PhaseInterrupted = "interrupted"
	PhaseUnconfirmed = "unconfirmed"
)

// Phases 返回官方 PagePhase 全集（顺序照 src/types.ts）。
func Phases() []string {
	return []string{
		PhaseCreating, PhaseInstalling, PhaseCancelled, PhaseInstalled, PhaseKept,
		PhaseRemoved, PhaseFailed, PhaseInterrupted, PhaseUnconfirmed,
	}
}

// 官方 `ChangeResult['application']` 枚举（src/index.ts 的 dataSchema）。
const (
	ApplicationApplied         = "applied"
	ApplicationRestartRequired = "restart-required"
	ApplicationOverridden      = "overridden"
	ApplicationFailed          = "failed"
	ApplicationCancelled       = "cancelled"
)

// StatusFields 是官方"卡片状态里展示哪几个 data 字段"
// （src/index.ts `statusFields: ['slot', 'customLocation', 'application']`）。
//
// 本宿主没有卡片渲染层，所以它不是 kind 的注册字段；导出的用途是让接线方/GUI
// 知道 decision.data 里哪三个键是状态位（decision.data 还可能带官方 PageData
// 的 `bundleName`，它不进状态行）。
var StatusFields = []string{"slot", "customLocation", "application"}

// 宿主的 refused reason 词表。
//
// 官方 reason 是**机器可读的短 token**（'invalid-location' / 'unavailable' /
// 'closing' / 'source-missing'）；前三个/create 的入参校验与官方逐字一致，
// 下面两个是宿主扩展，原因写清在常量注释里。
const (
	// ReasonInvalidLocation：Create/Not now 的 location 非法（官方 'invalid-location'）。
	ReasonInvalidLocation = "invalid-location"
	// ReasonUnavailable：官方 'unavailable'（卡不处于可执行该动词的 phase）。
	ReasonUnavailable = "unavailable"
	// ReasonClosing：宿主正在卸载，不接新工作（官方 'closing'）。
	ReasonClosing = "closing"
	// ReasonSourceMissing：官方 restore 的 'source-missing'——保留的 profile
	// 副本不存在或校验不通过（src/index.ts `refused('source-missing')`）。
	ReasonSourceMissing = "source-missing"
	// ReasonNotLiveRoot：宿主扩展。官方卡片服务只把**live root**（非子代理）
	// 交给 kind；本包在这一步复查真实 v4 头，不是 live root 就明确拒绝，
	// 而不是给子代理会话建一个装不进任何 profile 的"页面"。
	ReasonNotLiveRoot = "not-live-root"
	// ReasonCreatorUnavailable：宿主扩展。官方这一步是 `subagents.startActivation`
	// 起一个 `agentPreset: 'cordis'` 的 Creator 子代理去写插件包；本宿主没有
	// 页面渲染运行时、也没有能写 Cordis 插件包的 Creator（详见包文档的对照表）。
	// 生产接线没有注入 Creator 时，create 就在这里如实拒绝，**不写假包**。
	ReasonCreatorUnavailable = "creator-unavailable"
)
