package main

// 官方 rc.5 新增包 `@deepseek-ai/dsh-deepseek-chat-import` 的**宿主实现**：
// 把 chat.deepseek.com 导出的 `deepseek_data-*.zip` 变成工作区里的 v4 会话。
//
// # 契约来源（rc.5 解包树，逐条对照）
//
//	src/index.ts:56-66       注入 `sessionPersistence` / `workspaceRegistry`
//	src/importer.ts:555-575  写会话：header{version,id,createdAt,cwd,isSeeded:false}
//	                         → create → append(events) → flush → close
//	src/importer.ts:546-553  写后 `workspace.attachSession(id)`
//	src/importer.ts:188-192  每次写之前复核工作区仍注册且目录还在
//	src/index.ts:73-92       路由 + `limits()` 的装配
//
// # 本仓的接线方式
//
//   - **v4 写入**：`internal/session` 的 `CreateImportedSession` + `AppendImportedEvents`，
//     与真实会话同一条写路径（同一个 codec、同一把写锁语义），只是 createdAt 由
//     导出给、一批事件一帧。
//   - **工作区归属**：官方 `attachSession` 的等价物是
//     `officialWorkspaceHost.attachSession`（它同时复核 header.cwd 属于该工作区）。
//     即便不调它，会话也会因为 header.cwd 落在工作区目录而被
//     `derivedSessionIDs` 认出来——attach 的额外作用是把会话**顶到手动顺序最前**，
//     这正是官方那句 "add the Session to the Workspace" 的语义。
//   - **图片**：本仓没有"内存字节 → 图片附件"的存储面，所以图片条目不解压、
//     落成合成文件卡（= 官方 `deps.attachments === undefined` 分支，见
//     `internal/chatimport/importer.go` 头注）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dshgo/codecs/v4"
	"dshgo/internal/chatimport"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// chatImportHost 是这条能力的宿主状态。
type chatImportHost struct {
	home string
	src  *sessionSource
	// workspace 晚绑定：`wsHost` 在 transport.Options 装配时还没创建
	// （它与 createInWorkspace 共用同一个 registry 实例）。
	workspace func() *officialWorkspaceHost

	// 部署上限，默认值逐字取自官方 Config（src/index.ts:57-66）。
	maxSessions    int
	maxUploadBytes int64
	maxEntryBytes  int64
	maxTitleBytes  int

	newID func() string
	warn  func(message string)
}

// newChatImportHost 造宿主实现；默认上限与官方一致。
func newChatImportHost(home string, src *sessionSource, workspace func() *officialWorkspaceHost) *chatImportHost {
	return &chatImportHost{
		home:           home,
		src:            src,
		workspace:      workspace,
		maxSessions:    chatimport.DefaultMaxSessions,
		maxUploadBytes: chatimport.DefaultMaxUploadBytes,
		maxEntryBytes:  chatimport.DefaultMaxEntryBytes,
		maxTitleBytes:  chatimport.DefaultMaxTitleBytes,
		newID:          session.NewID,
		warn: func(message string) {
			fmt.Fprintf(os.Stderr, "%s\n", message)
		},
	}
}

// api 把它装成 transport 侧的形状。
func (h *chatImportHost) api() *transport.ChatImportAPI {
	return &transport.ChatImportAPI{
		Limits: func() transport.ChatImportLimits {
			return transport.ChatImportLimits{MaxUploadBytes: h.maxUploadBytes}
		},
		ResolveWorkspace: h.resolveWorkspace,
		Import:           h.importArchive,
	}
}

// resolveWorkspace 按 **registry id** 解析目标工作区（官方
// `ctx.workspaceRegistry.get(id)`，src/index.ts:77）。
//
// 只认 id，不认标题/路径：`resolveWorkspaceRef` 还接受标题与路径（那是
// `workspace.resolve` 的用法），这里若也接受，一个和标题同名的 id 就会解析到
// 别的目录去——官方 `get(id)` 不做这种模糊匹配。
func (h *chatImportHost) resolveWorkspace(workspaceID string) (transport.ChatImportWorkspace, bool) {
	host := h.workspace()
	if host == nil {
		return transport.ChatImportWorkspace{}, false
	}
	workspace, err := host.resolveWorkspaceRef(workspaceID)
	if err != nil || workspace.WorkspaceID != workspaceID || workspace.Path == "" {
		return transport.ChatImportWorkspace{}, false
	}
	return transport.ChatImportWorkspace{ID: workspace.WorkspaceID, Path: workspace.Path}, true
}

// importArchive 执行一次导入（官方 `importDeepseekChatExport` 的调用点，src/index.ts:78-90）。
func (h *chatImportHost) importArchive(ctx context.Context, request transport.ChatImportRequest) (transport.ChatImportResult, error) {
	host := h.workspace()
	if host == nil {
		return transport.ChatImportResult{}, &transport.ChatImportError{
			Reason:  transport.ChatImportWorkspaceNotFound,
			Message: "the workspace registry is not wired into this host",
		}
	}
	deps := chatimport.Dependencies{
		Persist:             h.persist,
		WorkspaceRegistered: h.workspaceRegistered,
		NewID:               h.newID,
		Warn:                h.warn,
	}
	workspace := chatimport.Workspace{
		ID:         request.Workspace.ID,
		Path:       request.Workspace.Path,
		PathExists: isExistingDirectory,
		AttachSession: func(ctx context.Context, sessionID string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return host.attachSession(request.Workspace.ID, sessionID)
		},
	}
	result, err := chatimport.Import(ctx, deps, chatimport.Request{
		Archive:   request.Archive,
		Workspace: workspace,
		Limits: chatimport.Limits{
			MaxSessions:   h.maxSessions,
			MaxEntryBytes: h.maxEntryBytes,
			MaxTitleBytes: h.maxTitleBytes,
		},
		OnProgress: request.OnProgress,
	})
	if err != nil {
		return transport.ChatImportResult{}, chatImportError(err)
	}
	skipped := make([]transport.ChatImportSkip, 0, len(result.Skipped))
	for _, item := range result.Skipped {
		skipped = append(skipped, transport.ChatImportSkip{
			ConversationID: item.ConversationID,
			Title:          item.Title,
			Reason:         item.Reason,
		})
	}
	return transport.ChatImportResult{
		WorkspaceID:       result.WorkspaceID,
		WorkspacePath:     result.WorkspacePath,
		SessionIDs:        result.SessionIDs,
		ConversationCount: result.ConversationCount,
		OmittedByLimit:    result.OmittedByLimit,
		Skipped:           skipped,
	}, nil
}

// persist 把一个转换好的对话写成一份真实 v4 会话（官方 `writeSession`，
// src/importer.ts:555-575）：
//
//	Session.create(id, events, header)   ← 与真实会话同一份 seed 校验
//	persistence.create(header) → append(events) → flush() → close()
//	projectionCache.coldSnapshot(...)    ← 本仓没有投影缓存，列表直接读盘
//
// `session/end-seed` 不写：官方也不写，普通 resume 路径第一次打开时补。
func (h *chatImportHost) persist(ctx context.Context, header chatimport.ImportedHeader, events []v4.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	batch := make([]session.ImportedEvent, 0, len(events))
	for _, event := range events {
		surfaceOp := ""
		if len(event.SurfaceOp) > 0 {
			var decoded string
			if err := json.Unmarshal(event.SurfaceOp, &decoded); err == nil {
				surfaceOp = decoded
			}
		}
		batch = append(batch, session.ImportedEvent{
			Type:            event.Type,
			Data:            event.Data,
			Time:            event.Time,
			SurfaceOp:       surfaceOp,
			SourceEventSeqs: event.SourceEventSeqs,
		})
	}
	log, err := session.CreateImportedSession(h.home, header.Cwd, header.ID, header.CreatedAt)
	if err != nil {
		return err
	}
	// 写失败也必须关文件：半份会话留在盘上会以一条损坏会话出现在列表里，
	// 而关掉它至少让"写到这里为止的内容"是完整帧。
	appendErr := log.AppendImportedEvents(batch)
	closeErr := log.CloseImported()
	if appendErr != nil {
		return appendErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

// workspaceRegistered 报告目标工作区是否仍然注册着（官方
// `workspaceRegistered` 谓词，src/index.ts:82）。
func (h *chatImportHost) workspaceRegistered(workspaceID string) bool {
	host := h.workspace()
	if host == nil {
		return false
	}
	workspace, err := host.resolveWorkspaceRef(workspaceID)
	if err != nil {
		return false
	}
	return workspace.WorkspaceID == workspaceID
}

// chatImportError 把引擎失败翻成 transport 的原因码形状
// （官方 `ExportSourceError` → outcome 行，src/upload.ts:126-128）。
func chatImportError(err error) error {
	var sourceError *chatimport.SourceError
	if errors.As(err, &sourceError) {
		reason := transport.ChatImportFailureReason(sourceError.Reason)
		switch reason {
		case transport.ChatImportTooLarge, transport.ChatImportUnreadableArchive,
			transport.ChatImportMissingManifest, transport.ChatImportMissingIndex,
			transport.ChatImportUnsupportedVersion, transport.ChatImportNoConversations,
			transport.ChatImportWorkspaceNotFound:
			return &transport.ChatImportError{Reason: reason, Message: sourceError.Message, Cause: err}
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// 取消不是"归档坏了"：调用方按中断处理（transport 侧不再写 outcome 行）。
		return err
	}
	return err
}

// isExistingDirectory 报告一个路径是不是真实存在的目录（官方 `isDirectory`，
// src/importer.ts:577-584；读不动按"已消失"处理）。
func isExistingDirectory(path string) bool {
	if filepath.Clean(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
