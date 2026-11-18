package transport

// DeepSeek 聊天导出的**上传路由**与 `limits` Remote 方法（官方 rc.5 新增包
// `@deepseek-ai/dsh-deepseek-chat-import`）。
//
// 契约原件（rc.5 解包树，逐字）：
//
//	src/protocol.ts:4        UPLOAD_PATH = '/api/deepseek-chat-import/archive'
//	src/upload.ts:53-95      路由校验顺序：415 content-type → 400 workspaceId →
//	                         200 NDJSON（busy / workspace-not-found / too-large
//	                         也都是 200 的 outcome 行）
//	src/upload.ts:150-184    按 content-length 预分配、超限即停、只读 maxUploadBytes
//	src/types.ts:34-73       结果 / 跳过项 / 进度行 / outcome 行的字段名
//	lib/typert.host.js:14-28 `deepseekChatImport/limits`，结果 `{maxUploadBytes}`
//
// 为什么是**独立 exact 路由**而不是 `POST /api/<method>`：官方把它注册成
// `ctx.connection.fetch.register({path, methods:['POST'], requestBody:'streaming'})`，
// 请求体是**裸字节**（不是 JSON 信封），响应是**逐行 NDJSON 流**。
// 与 `handlers_changes_routes.go` 的三条变更路由同一类：必须在 `/api/` 前缀分支
// 之前按路径接住，否则会被当成未知方法吞掉。
//
// 与官方的**两处显式**差异（都写清理由，不做静默近似）：
//
//  1. owner 判定。官方按 Peer 身份判 owner（非 owner 抛
//     `deepseekChatImport/owner-only`）。本仓 `remoteAccess` 的所有已认证 Peer
//     都是 owner（见 `official_stream_remote_access.go:115` 的注释），所以这里
//     唯一的"非 owner"就是**没有 cookie** 的调用方——它按本仓统一规则回 401。
//     也就是说这条路由在本仓永远是 owner-only，且拒绝形状与其余 /api 方法一致。
//  2. 415/400 的**正文编码**。官方 `new Response('content type must be …',
//     {status:415})` 是纯文本（src/upload.ts:69、:72）；本仓 /api 出口只有一个
//     错误构造点（`TestNoHandwrittenErrorBodies` 守着），所以这里回四字段 JSON，
//     **状态码与 message 原文不变**。两者对官方客户端等价：它在非 200 时只把正文
//     当不透明文本拼进 transport 诊断（`HTTP ${status}: ${text}`，
//     见 src/client/upload.ts:148、:217-218），从不解析这两个正文的形状。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const (
	// ChatImportUploadRoute 是接收一份导出归档的路由（官方 protocol.ts:4）。
	ChatImportUploadRoute = "/api/deepseek-chat-import/archive"
	// MethodChatImportLimits 是官方 typert Remote 方法名（typert.host.js:14）。
	MethodChatImportLimits Method = "deepseekChatImport/limits"
)

// OpenAI 风格常量的等价物：本路由只吃这一种 content-type（upload.ts:67-70）。
const chatImportMediaType = "application/octet-stream"

// chatImportNDJSON 是 route 的响应头（官方 NDJSON_HEADERS，upload.ts:53-58）。
const chatImportNDJSON = "application/x-ndjson; charset=utf-8"

// ChatImportLimits 是 `deepseekChatImport/limits` 的结果
// （官方 `DeepseekChatImportLimits`，types.ts:50-53）。
type ChatImportLimits struct {
	// MaxUploadBytes 是一次上传允许携带的最大导出归档字节数。
	MaxUploadBytes int64 `json:"maxUploadBytes"`
}

// ChatImportSkip 是一个没能导入的对话（官方 `DeepseekChatImportSkip`，types.ts:24-31）。
type ChatImportSkip struct {
	// ConversationID 来自索引；解析不出来的条目用条目名。
	ConversationID string `json:"conversationId"`
	// Title 是解析出来的标题；没解析出来就是 null（不是缺键）。
	Title *string `json:"title"`
	// Reason 是点名缺陷的英文诊断。
	Reason string `json:"reason"`
}

// ChatImportResult 是一次完成的导入（官方 `DeepseekChatImportResult`，types.ts:34-47）。
type ChatImportResult struct {
	// WorkspaceID 是接收导入会话的工作区。
	WorkspaceID string `json:"workspaceId"`
	// WorkspacePath 是该工作区的规范化目录。
	WorkspacePath string `json:"workspacePath"`
	// SessionIDs 是导入的会话，按导出索引新旧（新在前）。
	SessionIDs []string `json:"sessionIds"`
	// ConversationCount 是索引列出的对话数。
	ConversationCount int `json:"conversationCount"`
	// OmittedByLimit 是索引列出、但因条数上限没被尝试的对话数。
	OmittedByLimit int `json:"omittedByLimit"`
	// Skipped 是选了但没能导入的对话。
	Skipped []ChatImportSkip `json:"skipped"`
}

// ChatImportWorkspace 是宿主解析出来的目标工作区。
type ChatImportWorkspace struct {
	// ID 是 registry 里的工作区 id。
	ID string
	// Path 是它的规范化目录（必须真实存在）。
	Path string
}

// ChatImportRequest 是一次导入请求（宿主侧）。
type ChatImportRequest struct {
	// Archive 是完整的导出 ZIP 字节（整份在内存里，不落盘）。
	Archive []byte
	// Workspace 是已解析的目标工作区。
	Workspace ChatImportWorkspace
	// OnProgress 在选完之后、以及每写完一个会话时回报进度。
	OnProgress func(done, total int)
}

// ChatImportFailureReason 是整体失败的原因码（官方 types.ts:13-21，逐字）。
type ChatImportFailureReason string

const (
	// ChatImportTooLarge：超过 maxUploadBytes 或单条目上限。
	ChatImportTooLarge ChatImportFailureReason = "too-large"
	// ChatImportUnreadableArchive：不是可读 ZIP / 结构损坏。
	ChatImportUnreadableArchive ChatImportFailureReason = "unreadable-archive"
	// ChatImportMissingManifest：没有 manifest.json。
	ChatImportMissingManifest ChatImportFailureReason = "missing-manifest"
	// ChatImportMissingIndex：没有可读的 chat_session_index.json。
	ChatImportMissingIndex ChatImportFailureReason = "missing-index"
	// ChatImportUnsupportedVersion：manifest 没声明 export_format_version 3。
	ChatImportUnsupportedVersion ChatImportFailureReason = "unsupported-version"
	// ChatImportNoConversations：索引里没有对话。
	ChatImportNoConversations ChatImportFailureReason = "no-conversations"
	// ChatImportWorkspaceNotFound：选中的工作区已不存在。
	ChatImportWorkspaceNotFound ChatImportFailureReason = "workspace-not-found"
	// ChatImportBusy：这台宿主上已有一次导入在跑。
	ChatImportBusy ChatImportFailureReason = "busy"
)

// ChatImportError 是一次带原因码的导入失败（宿主实现返回它）。
type ChatImportError struct {
	// Reason 是用户可见解释的选择依据。
	Reason ChatImportFailureReason
	// Message 是英文诊断。
	Message string
	// Cause 是底层错误（只进日志，不上线）。
	Cause error
}

func (e *ChatImportError) Error() string { return e.Message }

func (e *ChatImportError) Unwrap() error { return e.Cause }

// ChatImportAPI 是这条路由与 `limits` 方法背后的宿主实现。
//
// 没有它时：路由回 501（点名未接线）、`limits` 回 501——**不**返回伪造的上限，
// 也不假装"导入成功但什么都没写"。
type ChatImportAPI struct {
	// Limits 报告浏览器上传前要检查的上限。
	Limits func() ChatImportLimits
	// ResolveWorkspace 按 registry id 解析目标工作区；不存在时 ok=false
	// （对应官方 `handler.workspace(id) === undefined`）。
	ResolveWorkspace func(workspaceID string) (ChatImportWorkspace, bool)
	// Import 执行一次导入。返回 *ChatImportError 时按 reason 出 outcome 行。
	Import func(ctx context.Context, request ChatImportRequest) (ChatImportResult, error)
}

// chatImportOutcome 是 NDJSON 里的 outcome 值（官方 `DeepseekChatImportResponse`，types.ts:56-58）。
type chatImportOutcome struct {
	OK    bool               `json:"ok"`
	Value *ChatImportResult  `json:"value,omitempty"`
	Error *chatImportFailure `json:"error,omitempty"`
}

type chatImportFailure struct {
	Reason  ChatImportFailureReason `json:"reason"`
	Message string                  `json:"message"`
}

// chatImportLine 是一行 NDJSON（官方 `DeepseekChatImportLine`，types.ts:65-73）。
type chatImportLine struct {
	Type    string             `json:"type"`
	Done    *int               `json:"done,omitempty"`
	Total   *int               `json:"total,omitempty"`
	Outcome *chatImportOutcome `json:"outcome,omitempty"`
}

// ChatImportUploads 报告这条路由是否已接线（`limits` 与上传共用同一份判断）。
func (s *Server) chatImportReady() bool {
	return s.opt.ChatImport != nil && s.opt.ChatImport.Import != nil && s.opt.ChatImport.Limits != nil
}

// chatImportBusy 试占这次导入的唯一槽位（官方 `claim()`，src/index.ts:120-131）。
//
// 槽位放在 Server 上而不是宿主实现里：官方服务与路由是同一个对象，本仓
// transport 是唯一能看到"这条路由正在跑"的层，把它放这里才不会出现
// "第二个请求以为没人占、于是两个导入同时写同一份 home"。
func (s *Server) chatImportBusy() (release func(), ok bool) {
	s.chatImportSlot.Lock()
	defer s.chatImportSlot.Unlock()
	if s.chatImportActive {
		return nil, false
	}
	s.chatImportActive = true
	var once bool
	return func() {
		// release 只生效一次：正常路径与 defer 路径都会调它。
		if once {
			return
		}
		once = true
		s.chatImportSlot.Lock()
		s.chatImportActive = false
		s.chatImportSlot.Unlock()
	}, true
}

// serveChatImportLimits 服务 `deepseekChatImport/limits`。
//
// 官方形状：无参数、结果是 `{maxUploadBytes}`、只允许 owner
// （lib/typert.host.js:14-28，src/index.ts:111-118）。本仓的 owner 判定就是
// 这张 cookie——`handleAPI` 已经在调用前拒过没有 cookie 的请求。
func (s *Server) serveChatImportLimits(w http.ResponseWriter, r *http.Request) {
	if s.opt.ChatImport == nil || s.opt.ChatImport.Limits == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			"deepseekChatImport/limits 未接线：宿主没有接入 DeepSeek 聊天导入服务"))
		return
	}
	writeJSON(w, http.StatusOK, s.opt.ChatImport.Limits())
}

// serveChatImportUpload 是 `POST /api/deepseek-chat-import/archive` 的实现。
//
// 校验顺序与官方 `createUploadRoute`（src/upload.ts:66-94）逐条一致：
//  1. 非 POST → 404（本仓 /api 的统一用法；官方是路由层 405）；
//  2. 无 cookie → 401（官方非 owner → `deepseekChatImport/owner-only`）；
//  3. content-type 不是 application/octet-stream → 415 + 纯文本；
//  4. 缺 workspaceId → 400 + 纯文本；
//  5. 已有导入在跑 → 200 NDJSON outcome `busy`；
//  6. 工作区不存在 → 200 NDJSON outcome `workspace-not-found`；
//  7. 归档超过 maxUploadBytes → 200 NDJSON outcome `too-large`；
//  8. 其余走 200 NDJSON：进度行若干 + 恰好一行 outcome。
func (s *Server) serveChatImportUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
		return
	}
	if !s.chatImportReady() {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			"deepseek-chat-import 上传路由未接线：宿主没有接入 DeepSeek 聊天导入服务"))
		return
	}
	mediaType := ""
	if raw := r.Header.Get("Content-Type"); raw != "" {
		mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(raw, ";", 2)[0]))
	}
	if mediaType != chatImportMediaType {
		// 官方原文：`content type must be application/octet-stream`（upload.ts:69）；
		// 状态码照官方 415，正文按本仓 /api 的四字段出口编码（见文件头第 2 条）。
		writeJSON(w, http.StatusUnsupportedMediaType,
			errorBodyFor("bad_request", "content type must be application/octet-stream"))
		return
	}
	workspaceID := r.URL.Query().Get("workspaceId")
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "workspaceId is required"))
		return
	}
	limits := s.opt.ChatImport.Limits()
	release, claimed := s.chatImportBusy()
	if !claimed {
		// 占不到槽位：先排空请求体，再按官方形状回 outcome `busy`。
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		writeChatImportOutcome(w, failureOutcome(ChatImportBusy, "another import is running"))
		return
	}
	streaming := false
	defer func() {
		if !streaming {
			release()
		}
	}()

	workspace, ok := s.opt.ChatImport.ResolveWorkspace(workspaceID)
	if !ok {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		writeChatImportOutcome(w, failureOutcome(ChatImportWorkspaceNotFound,
			fmt.Sprintf("workspace %s does not exist", workspaceID)))
		return
	}
	archive, tooLarge := readChatImportBody(w, r, limits.MaxUploadBytes)
	if tooLarge {
		writeChatImportOutcome(w, failureOutcome(ChatImportTooLarge,
			fmt.Sprintf("the archive exceeds %d bytes", limits.MaxUploadBytes)))
		return
	}
	if archive == nil {
		// 读体失败（连接断了）：官方此时不写 outcome 行，客户端按"被中断"处理。
		return
	}
	streaming = true
	s.streamChatImport(w, r, archive, workspace, release)
}

// streamChatImport 写 NDJSON：进度行 + 恰好一行 outcome（官方 `importStream`，
// upload.ts:102-143）。
func (s *Server) streamChatImport(
	w http.ResponseWriter, r *http.Request, archive []byte, workspace ChatImportWorkspace, release func(),
) {
	header := w.Header()
	header.Set("Content-Type", chatImportNDJSON)
	// 压缩中间件认 no-transform，进度行不会被攒住（官方注释同义）。
	header.Set("Cache-Control", "no-store, no-transform")
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	ctx := r.Context()

	send := func(line chatImportLine) error {
		payload, err := json.Marshal(line)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(payload, '\n')); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	result, err := s.opt.ChatImport.Import(ctx, ChatImportRequest{
		Archive:   archive,
		Workspace: workspace,
		OnProgress: func(done, total int) {
			doneValue, totalValue := done, total
			// 进度行写失败（客户端走了）不打断导入：取消由 ctx 决定，
			// 这里只是"没人看了"。
			_ = send(chatImportLine{Type: "progress", Done: &doneValue, Total: &totalValue})
		},
	})
	release()
	if ctx.Err() != nil {
		// 客户端断开或取消：官方此时直接结束流，不写 outcome
		// （upload.ts:124-125 的 `if (signal.aborted) end()`）。
		return
	}
	if err != nil {
		var typed *ChatImportError
		if errors.As(err, &typed) {
			_ = send(chatImportLine{Type: "outcome", Outcome: failureOutcome(typed.Reason, typed.Message)})
			return
		}
		// 非归档级失败：官方让流**出错**（不留 outcome 行，upload.ts:128-134），
		// 客户端据此把这次应答判成中断。本仓的流式响应同样不发 outcome 行，
		// 但**必须**留下服务端证据——"没有 outcome 行"本身看不出原因。
		fmt.Fprintf(os.Stderr, "transport: deepseek-chat-import 导入失败: %v\n", err)
		return
	}
	if result.SessionIDs == nil {
		result.SessionIDs = []string{}
	}
	if result.Skipped == nil {
		result.Skipped = []ChatImportSkip{}
	}
	_ = send(chatImportLine{Type: "outcome", Outcome: &chatImportOutcome{OK: true, Value: &result}})
}

// readChatImportBody 按 content-length 预分配、超限即停地读完整请求体
// （官方 `readBounded`，upload.ts:150-184）。
//
// 返回 (nil, true) 表示超限；(nil, false) 表示读失败（连接断了）。
func readChatImportBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	if declared := r.Header.Get("Content-Length"); declared != "" {
		if parsed, err := strconv.ParseInt(declared, 10, 64); err == nil && parsed > limit {
			return nil, true
		}
	}
	if r.Body == nil {
		return []byte{}, false
	}
	// limit+1 只多读一个字节：用它区分"正好等于上限"与"超过上限"。
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, false
	}
	if int64(len(data)) > limit {
		return nil, true
	}
	return data, false
}

// writeChatImportOutcome 在还没开始流式响应时，直接写一行 outcome 的完整响应
// （官方 `failure`，upload.ts:194-196：状态码仍是 200）。
func writeChatImportOutcome(w http.ResponseWriter, outcome *chatImportOutcome) {
	w.Header().Set("Content-Type", chatImportNDJSON)
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	payload, err := json.Marshal(chatImportLine{Type: "outcome", Outcome: outcome})
	if err != nil {
		return
	}
	_, _ = w.Write(append(payload, '\n'))
}

func failureOutcome(reason ChatImportFailureReason, message string) *chatImportOutcome {
	return &chatImportOutcome{OK: false, Error: &chatImportFailure{Reason: reason, Message: message}}
}
