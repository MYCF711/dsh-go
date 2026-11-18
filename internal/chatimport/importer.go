package chatimport

// 一次完整导入的编排：索引选对话 → 逐条转换 → 每条先过 seed 校验再落盘 → 挂进工作区。
//
// 官方原件：`src/importer.ts`。本文件保留官方的两条关键不变量：
//
//  1. **先校验后写**：每条对话先完整转换、再跑与真实会话同一份 seed 校验
//     （官方 `Session.create`），通过才落盘；失败的那条进 `skipped`，后面的继续。
//  2. **失败可替换**：被选中的对话里有一条失败时，索引上更旧的下一条顶上，
//     直到写满 `maxSessions` 或索引用完（`omittedByLimit` 因此按 `next` 算）。
//
// 与官方的**有意**差异（只此一处，且是官方自己标注的可选分支）：
// 本仓目前没有"内存字节 → 图片附件"的存储面（`transport.AttachmentStore` 只吃
// 本机路径），所以图片条目**不解压、不入库**，`FILE` 片段里的图片与普通文件一样
// 落成合成文件卡。这正是官方 `convertWithStoredImages` 在 `deps.attachments ===
// undefined` 时的行为（importer.ts:386 与 conversation.ts:254-255），不是近似。

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"dshgo/codecs/v4"
)

// Dependencies 是一次导入用到的服务与输入（官方 `DeepseekChatImportDependencies`，
// importer.ts:37-58）。
type Dependencies struct {
	// Persist 把一个会话的 header + seed 事件写进持久层；实现方负责 v4 落盘。
	Persist func(ctx context.Context, header ImportedHeader, events []v4.Event) error
	// WorkspaceRegistered 报告目标工作区是否仍然注册着；每次写之前检查。
	WorkspaceRegistered func(workspaceID string) bool
	// NewID 铸唯一的会话、消息与工具调用 id。
	NewID func() string
	// Warn 记录每条没导入的对话、以及每个没能入库的图片。
	Warn func(message string)
}

// ImportedHeader 是一个待写会话的 header（官方 `SessionHeader`，importer.ts:562）。
type ImportedHeader struct {
	// ID 是新会话的 id。
	ID string
	// CreatedAt 是第一条事件的 epoch 毫秒。
	CreatedAt int64
	// Cwd 是目标工作区的规范化路径。
	Cwd string
}

// Workspace 是接收导入会话的工作区（官方 `DeepseekChatImportWorkspace`，
// importer.ts:61-71）。
type Workspace struct {
	// ID 是结果里回报的工作区 id。
	ID string
	// Path 是已存在的规范化目录，也是每个导入会话的工作目录。
	Path string
	// AttachSession 把一个已落盘的会话记成工作区成员。
	AttachSession func(ctx context.Context, sessionID string) error
	// PathExists 报告工作区目录是否仍然存在；nil 表示不检查（测试用）。
	PathExists func(path string) bool
}

// Request 是一次导入请求（官方 `DeepseekChatImportRequest`，importer.ts:84-97）。
type Request struct {
	// Archive 是完整的导出 ZIP 字节。
	Archive []byte
	// Workspace 是接收会话的工作区。
	Workspace Workspace
	// Limits 是条数与尺寸上限。
	Limits Limits
	// OnProgress 在选完之后、以及每写完一个会话时回报进度。
	OnProgress func(done, total int)
}

// directoryExists 由宿主注入（真实实现是 os.Stat），因为本包不碰文件系统。
type directoryCheck func(path string) bool

// Import 把导出索引选中的对话导入一个工作区（官方 `importDeepseekChatExport`，
// importer.ts:153-312）。
//
// 在流式趟之前发现的失败（读不出归档、manifest/index 缺失或损坏、没有任何
// 对话）**一个会话都不写**；归档在流式趟中途坏掉时，已交付的会话已落盘，
// 随后以 `unreadable-archive` 整体失败。
//
// 返回 *Failure 表示整体失败，*ConversationError 之类只会出现在 skipped 里。
func Import(ctx context.Context, deps Dependencies, request Request) (Result, error) {
	workspace := request.Workspace
	limits := request.Limits
	archive, sourceErr := OpenArchive(request.Archive, limits.MaxEntryBytes)
	if sourceErr != nil {
		return Result{}, sourceErr
	}
	skipped := make([]Skip, 0)
	report := func(item Skip) {
		skipped = append(skipped, item)
		if deps.Warn != nil {
			deps.Warn(fmt.Sprintf("deepseek-chat-import: conversation %s was not imported: %s",
				item.ConversationID, item.Reason))
		}
	}

	manifestBytes, err := readMetadata(ctx, archive, ManifestEntry,
		MetadataReasons{Missing: ReasonMissingManifest, Damaged: ReasonUnreadableArchive}, limits.MaxEntryBytes)
	if err != nil {
		return Result{}, err
	}
	manifest, parseErr := ParseManifest(manifestBytes)
	// 版本闸门管着后面每一次转换，所以在读索引之前先判。
	if parseErr != nil || manifest.ExportFormatVersion != SupportedExportFormatVersion {
		return Result{}, &SourceError{
			Reason: ReasonUnsupportedVersion,
			Message: fmt.Sprintf("manifest.json must declare export_format_version %d",
				SupportedExportFormatVersion),
		}
	}
	indexBytes, err := readMetadata(ctx, archive, IndexEntry,
		MetadataReasons{Missing: ReasonMissingIndex, Damaged: ReasonMissingIndex}, limits.MaxEntryBytes)
	if err != nil {
		return Result{}, err
	}
	ranked, err := parseRankedIndex(indexBytes)
	if err != nil {
		return Result{}, err
	}
	if len(ranked) == 0 {
		return Result{}, &SourceError{
			Reason:  ReasonNoConversations,
			Message: "the export contains no importable conversation",
		}
	}
	total := limits.MaxSessions
	if total > len(ranked) {
		total = len(ranked)
	}
	if total <= 0 {
		return Result{}, &SourceError{
			Reason:  ReasonNoConversations,
			Message: "the export contains no importable conversation",
		}
	}
	chosen := make(map[string]int, total)
	for rank := 0; rank < total; rank++ {
		chosen[ranked[rank].ID] = rank
	}
	if request.OnProgress != nil {
		request.OnProgress(0, total)
	}

	options := ConvertOptions{NewID: deps.NewID, MaxTitleBytes: limits.MaxTitleBytes}
	imported := make([]importedSession, 0, total)
	// handled 是被选中的对话里、归档条目已被处理过的（无论成败）。
	handled := make(map[string]struct{}, total)
	// outstanding 是"还欠一个会话"的条数：它驱动后面的替补。
	outstanding := 0
	// next 是下一次替补可以从索引取的位次。
	next := total

	assertWorkspace := func() error {
		registered := deps.WorkspaceRegistered == nil || deps.WorkspaceRegistered(workspace.ID)
		exists := workspace.PathExists == nil || workspace.PathExists(workspace.Path)
		if !registered || !exists {
			return &SourceError{
				Reason:  ReasonWorkspaceNotFound,
				Message: fmt.Sprintf("workspace %s was removed during the import", workspace.ID),
			}
		}
		return nil
	}
	if err := assertWorkspace(); err != nil {
		return Result{}, err
	}

	write := func(conversion Converted, rank int, conversationID string, title *string) bool {
		id, err := writeSession(ctx, deps, conversion, workspace.Path)
		if err == nil && workspace.AttachSession != nil {
			err = attachSession(ctx, workspace, id)
		}
		if err != nil {
			report(Skip{ConversationID: conversationID, Title: title, Reason: err.Error()})
			return false
		}
		imported = append(imported, importedSession{id: id, rank: rank})
		if request.OnProgress != nil {
			request.OnProgress(len(imported), total)
		}
		return true
	}

	var streamErr error
	importConversation := func(conversationID string, data []byte) {
		if err := assertWorkspace(); err != nil {
			// assertWorkspace 的失败是整体失败：用 panic 之外的通道传给 Stream：
			// 这里把错误记在 streamErr 上，由外层在 stream 返回后抛。
			streamErr = err
			return
		}
		rank := chosen[conversationID]
		handled[conversationID] = struct{}{}
		var title *string
		file, parseFailure := ParseConversation(data)
		if parseFailure != nil {
			outstanding++
			report(Skip{ConversationID: conversationID, Reason: parseFailure.Error()})
			return
		}
		title = file.Title
		conversion, convertErr := ConvertConversation(file, options)
		if convertErr != nil {
			outstanding++
			report(Skip{ConversationID: conversationID, Title: title, Reason: convertErr.Error()})
			return
		}
		if !write(conversion, rank, conversationID, title) {
			outstanding++
		}
	}

	handler := StreamHandler{
		Select: func(entry Entry) bool {
			conversation := conversationIDOf(entry.Name)
			if conversation == "" {
				return false
			}
			_, ok := chosen[conversation]
			return ok
		},
		Reject: func(entry Entry, failure *EntryError) {
			conversation := conversationIDOf(entry.Name)
			if conversation == "" {
				return
			}
			if _, ok := chosen[conversation]; !ok {
				return
			}
			handled[conversation] = struct{}{}
			outstanding++
			report(Skip{ConversationID: conversation, Reason: failure.Message})
		},
		Entry: func(_ context.Context, entry Entry, data []byte) error {
			if streamErr != nil {
				return nil
			}
			importConversation(conversationIDOf(entry.Name), data)
			return nil
		},
	}
	if sourceErr := archive.Stream(ctx, limits.MaxEntryBytes, handler); sourceErr != nil {
		return Result{}, sourceErr
	}
	if streamErr != nil {
		return Result{}, streamErr
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// 被选中、但归档里根本没有对应文件：报出来，让索引上更旧的顶上。
	for conversationID := range chosen {
		if _, done := handled[conversationID]; done {
			continue
		}
		outstanding++
		report(Skip{ConversationID: conversationID, Reason: "the export has no file for this conversation"})
	}
	for outstanding > 0 && next < len(ranked) {
		row := ranked[next]
		next++
		if attemptRow(ctx, assertWorkspace, archive, options, limits.MaxEntryBytes, deps,
			row, next-1, write, report) {
			outstanding--
		}
	}
	sort.SliceStable(imported, func(left, right int) bool {
		return imported[left].rank < imported[right].rank
	})
	ids := make([]string, 0, len(imported))
	for _, item := range imported {
		ids = append(ids, item.id)
	}
	return Result{
		WorkspaceID:       workspace.ID,
		WorkspacePath:     workspace.Path,
		SessionIDs:        ids,
		ConversationCount: len(ranked),
		OmittedByLimit:    len(ranked) - next,
		Skipped:           skipped,
	}, nil
}

// importedSession 是一条落盘成功的会话及其索引位次。
type importedSession struct {
	id   string
	rank int
}

// MetadataReasons 是读不出一个元数据条目时的原因分类（官方 `MetadataReasons`，
// importer.ts:126-129）。
type MetadataReasons struct {
	Missing FailureReason
	Damaged FailureReason
}

// readMetadata 读一个导入前必需的元数据条目（官方 `readMetadata`，importer.ts:400-414）。
func readMetadata(ctx context.Context, archive *Archive, name string, reasons MetadataReasons, maxEntryBytes int64) ([]byte, error) {
	bytes, entryErr := archive.Read(ctx, name, maxEntryBytes)
	if entryErr != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reason := reasons.Damaged
		if entryErr.Oversized {
			reason = ReasonTooLarge
		}
		return nil, &SourceError{Reason: reason, Message: entryErr.Message, Cause: entryErr}
	}
	if bytes == nil {
		return nil, &SourceError{
			Reason:  reasons.Missing,
			Message: fmt.Sprintf("the archive has no %s", name),
		}
	}
	return bytes, nil
}

// parseRankedIndex 解析索引并按"最近更新在前"排序（官方 `parseIndex` +
// `compareRanks`，importer.ts:422-439）。
func parseRankedIndex(data []byte) ([]IndexRow, error) {
	rows, err := ParseIndex(data)
	if err != nil {
		return nil, &SourceError{
			Reason:  ReasonMissingIndex,
			Message: "the archive has no readable chat_session_index.json",
			Cause:   err,
		}
	}
	sort.SliceStable(rows, func(left, right int) bool {
		if rows[left].UpdatedAt != rows[right].UpdatedAt {
			return rows[left].UpdatedAt > rows[right].UpdatedAt
		}
		if rows[left].InsertedAt != rows[right].InsertedAt {
			return rows[left].InsertedAt > rows[right].InsertedAt
		}
		return rows[left].ID < rows[right].ID
	})
	return rows, nil
}

// attemptRow 按名读一条被替补的对话并写入（官方 `attemptRow`，importer.ts:290-311）。
func attemptRow(
	ctx context.Context,
	assertWorkspace func() error,
	archive *Archive,
	options ConvertOptions,
	maxEntryBytes int64,
	_ Dependencies,
	row IndexRow,
	rank int,
	write func(Converted, int, string, *string) bool,
	report func(Skip),
) bool {
	if err := assertWorkspace(); err != nil {
		report(Skip{ConversationID: row.ID, Reason: err.Error()})
		return false
	}
	entry := conversationEntry(row.ID)
	data, entryErr := archive.Read(ctx, entry, maxEntryBytes)
	if entryErr != nil {
		report(Skip{ConversationID: row.ID, Reason: entryErr.Message})
		return false
	}
	if data == nil {
		report(Skip{ConversationID: row.ID, Reason: fmt.Sprintf("%s is not in the export", entry)})
		return false
	}
	file, parseFailure := ParseConversation(data)
	if parseFailure != nil {
		report(Skip{ConversationID: row.ID, Reason: parseFailure.Error()})
		return false
	}
	conversion, convertErr := ConvertConversation(file, options)
	if convertErr != nil {
		report(Skip{ConversationID: row.ID, Title: file.Title, Reason: convertErr.Error()})
		return false
	}
	return write(conversion, rank, row.ID, file.Title)
}

// writeSession 落盘一个会话：header 的 createdAt 取第一条事件的时间，cwd 取工作区
// 规范化路径；写完后**不**写 `session/end-seed`（官方 importer.ts:555-575）。
func writeSession(ctx context.Context, deps Dependencies, conversion Converted, cwd string) (string, error) {
	if deps.Persist == nil {
		return "", fmt.Errorf("no session persistence is wired")
	}
	id := deps.NewID()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	header := ImportedHeader{ID: id, CreatedAt: conversion.CreatedAt, Cwd: cwd}
	if err := deps.Persist(ctx, header, conversion.Events); err != nil {
		return "", err
	}
	return id, nil
}

// attachSession 把一个已落盘的会话记成工作区成员，失败时点名已存的 id
// （官方 `attachSession`，importer.ts:546-553）。
func attachSession(ctx context.Context, workspace Workspace, id string) error {
	if err := workspace.AttachSession(ctx, id); err != nil {
		return fmt.Errorf("stored as Session %s but not added to the workspace: %w", id, err)
	}
	return nil
}

// conversationIDOf 取一个条目名里的对话 id；不是对话条目时返回空串
// （官方 `conversationIdOf`，importer.ts:442-444）。
func conversationIDOf(name string) string {
	if !strings.HasPrefix(name, "chat_sessions/") || !strings.HasSuffix(name, ".json") {
		return ""
	}
	leaf := strings.TrimSuffix(strings.TrimPrefix(name, "chat_sessions/"), ".json")
	if leaf == "" || strings.Contains(leaf, "/") {
		return ""
	}
	return leaf
}

// conversationEntry 是某个对话的归档条目名（官方 `conversationEntry`，importer.ts:475-477）。
func conversationEntry(id string) string {
	return "chat_sessions/" + id + ".json"
}

// yield 让出一次执行权（官方在对话之间 `await` 事件循环，importer.ts:145）。
func yield() {
	runtime.Gosched()
}
