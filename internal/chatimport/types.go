package chatimport

// 官方 `src/types.ts` 的逐条对应物：失败原因、跳过项、结果、限制。
//
// 字段名与 JSON 名**逐字**来自官方（本仓 transport 层再把它映射成线上的
// `DeepseekChatImportResult`，两边字段一字不差）。

// FailureReason 说明一次导入整体为什么停下（官方 `DeepseekChatImportFailureReason`，
// src/types.ts:13-21）。这些字符串会原样出现在 NDJSON 的 outcome 行里，
// 界面按它们选本地化解释，所以**不许改字**。
type FailureReason string

const (
	// ReasonTooLarge：文件超过 maxUploadBytes，或某个条目声明/存储超过 maxEntryBytes。
	ReasonTooLarge FailureReason = "too-large"
	// ReasonUnreadableArchive：不是可读的 ZIP、manifest 损坏、stored 条目尺寸自相矛盾、
	// 中央目录有同名条目。
	ReasonUnreadableArchive FailureReason = "unreadable-archive"
	// ReasonMissingManifest：归档里没有 manifest.json。
	ReasonMissingManifest FailureReason = "missing-manifest"
	// ReasonMissingIndex：归档里没有可读的 chat_session_index.json。
	ReasonMissingIndex FailureReason = "missing-index"
	// ReasonUnsupportedVersion：manifest 没有声明 export_format_version 3。
	ReasonUnsupportedVersion FailureReason = "unsupported-version"
	// ReasonNoConversations：索引里没有任何对话。
	ReasonNoConversations FailureReason = "no-conversations"
	// ReasonWorkspaceNotFound：选中的工作区已不存在（导入中途消失时同样停下并保留已写入的会话）。
	ReasonWorkspaceNotFound FailureReason = "workspace-not-found"
	// ReasonBusy：这台宿主上已经有一次导入在跑。
	ReasonBusy FailureReason = "busy"
)

// Failure 是一次导入的整体失败：Reason 讲话，Message 是英文诊断
// （官方 `ExportSourceError` 的两半）。
type Failure struct {
	Reason  FailureReason
	Message string
}

func (f *Failure) Error() string { return f.Message }

// Skip 是一个没能导入的对话（官方 `DeepseekChatImportSkip`，src/types.ts:24-31）。
type Skip struct {
	// ConversationID 来自索引的对话 id；解析不出来的条目用条目名。
	ConversationID string
	// Title 是解析出来的标题；没解析出来时是 nil（wire 上是 null）。
	Title *string
	// Reason 是点名缺陷的英文诊断。
	Reason string
}

// Result 是一次完成的导入（官方 `DeepseekChatImportResult`，src/types.ts:34-47）。
type Result struct {
	// WorkspaceID 是接收导入会话的工作区。
	WorkspaceID string
	// WorkspacePath 是该工作区的规范化目录，也是每个导入会话的工作目录。
	WorkspacePath string
	// SessionIDs 是导入的会话，按导出索引的新旧顺序（新在前）。
	SessionIDs []string
	// ConversationCount 是导出索引列出的对话数。
	ConversationCount int
	// OmittedByLimit 是索引列出、但因为条数上限一次都没尝试的对话数。
	OmittedByLimit int
	// Skipped 是选了但没能导入的对话。
	Skipped []Skip
}

// Limits 是一次导入的部署上限（官方 `DeepseekChatImportLimits`，src/importer.ts:74-81）。
type Limits struct {
	// MaxSessions 是一次导入最多写几个会话。
	MaxSessions int
	// MaxEntryBytes 是单个归档条目**声明**的解压上限；更大的条目永不解压。
	MaxEntryBytes int64
	// MaxTitleBytes 是导入标题的 UTF-8 字节上限。
	MaxTitleBytes int
}

// 官方 `Config` 的默认值（src/index.ts:57-66）。本仓不改这几个数：
// 它们会出现在 limits 面上（maxUploadBytes）与文档里，改了就是改契约。
const (
	// DefaultMaxSessions 是官方 maxSessions 默认值。
	DefaultMaxSessions = 1000
	// DefaultMaxUploadBytes 是官方 maxUploadBytes 默认值（512 MiB）。
	DefaultMaxUploadBytes int64 = 512 * 1024 * 1024
	// DefaultMaxEntryBytes 是官方 maxEntryBytes 默认值（256 MiB）。
	DefaultMaxEntryBytes int64 = 256 * 1024 * 1024
	// DefaultMaxTitleBytes 是官方 maxTitleBytes 默认值。
	DefaultMaxTitleBytes = 80
)

// 归档内部的固定条目名与匹配式（官方 importer.ts:28-34）。
const (
	// ManifestEntry 是声明导出格式版本的条目。
	ManifestEntry = "manifest.json"
	// IndexEntry 是列出全部对话及其顺序的条目。
	IndexEntry = "chat_session_index.json"
	// conversationEntryPattern 是对话文件（`chat_sessions/{id}.json`）。
	conversationEntryPattern = `^chat_sessions/([^/]+)\.json$`
	// imageEntryPattern 是图片（`chat_sessions/{id}_files/...`，第一段是对话 id）。
	imageEntryPattern = `^chat_sessions/([^/]+)_files/[^/]+$`
)
