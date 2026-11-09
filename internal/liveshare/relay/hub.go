package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dshgo/internal/liveshare/ws"
)

// ringSize 是每个 share 保留的最近帧数：viewer 断线重连时用 since 补洞。
// 只保留内存里最近的一段，超出即丢（与官方"补洞只覆盖内存里还在的帧"同口径）。
const ringSize = 64

// frameRecord 是广播过的一帧。
type frameRecord struct {
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data"`
}

// shareRecord 是 relay 内的一条分享（持久字段 + 运行时连接状态）。
type shareRecord struct {
	persistedShare
	viewers   map[*viewerConn]struct{}
	publisher *ws.Conn
	forks     map[string]*Fork
	frames    []frameRecord
	seq       int64
}

// viewerConn 是一条 viewer 连接。
type viewerConn struct {
	id        string
	conn      *ws.Conn
	shareID   string
	forkID    string
	subjectID string
}

// event 是发给 viewer/publisher 的一条 JSON 消息。
func event(kind string, fields map[string]any) map[string]any {
	msg := map[string]any{"type": kind}
	for k, v := range fields {
		msg[k] = v
	}
	return msg
}

func writeJSONConn(conn *ws.Conn, msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.WriteMessage(body)
}

// snapshotFramesLocked 取出手里 seq > since 的帧（since<=0 表示只要最新的
// 一条也不补——官方重连语义是"从上次水位往后补"）。
func snapshotFramesLocked(share *shareRecord, since int64) []frameRecord {
	if since <= 0 {
		return nil
	}
	out := make([]frameRecord, 0, len(share.frames))
	for _, frame := range share.frames {
		if frame.Seq > since {
			out = append(out, frame)
		}
	}
	return out
}

// appendFrameLocked 把一帧放进环形缓冲，返回带 seq 的帧。
func appendFrameLocked(share *shareRecord, data json.RawMessage) frameRecord {
	share.seq++
	frame := frameRecord{Seq: share.seq, Data: data}
	share.frames = append(share.frames, frame)
	if len(share.frames) > ringSize {
		share.frames = append([]frameRecord(nil), share.frames[len(share.frames)-ringSize:]...)
	}
	return frame
}

// viewerSnapshotLocked 复制一份 viewer 列表（写连接时不持锁）。
func viewerSnapshotLocked(share *shareRecord) []*viewerConn {
	out := make([]*viewerConn, 0, len(share.viewers))
	for v := range share.viewers {
		out = append(out, v)
	}
	return out
}

// connWriteTimeout 是给单个连接写一帧的上限。写超时就断开该连接：
// 一个卡住/不读的 viewer 不能把 relay 的全局锁或整场分享拖死。
const connWriteTimeout = 5 * time.Second

// writeConnTimed 带写超时地写一条 JSON 消息，成功后恢复无 deadline。
func writeConnTimed(conn *ws.Conn, msg any) error {
	_ = conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
	err := writeJSONConn(conn, msg)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

// broadcastLocked 给 share 的全部 viewer 写一条消息。写失败只关闭该连接，
// 不影响其它 viewer（一个卡住的客户端不能拖垮整场分享）。
func broadcastLocked(share *shareRecord, msg any) {
	for _, viewer := range viewerSnapshotLocked(share) {
		if err := writeConnTimed(viewer.conn, msg); err != nil {
			closeViewer(viewer)
		}
	}
}

func closeViewer(viewer *viewerConn) {
	_ = viewer.conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
	_ = viewer.conn.WriteClose(1000, "server-closed")
}

// handleViewerWS 是 GET /v1/connect?share=…[&fork=1][&since=N]。
func (s *Server) handleViewerWS(w http.ResponseWriter, r *http.Request) {
	shareID := strings.TrimSpace(r.URL.Query().Get("share"))
	if shareID == "" {
		http.Error(w, "缺 share 参数", http.StatusBadRequest)
		return
	}
	wantFork := r.URL.Query().Get("fork") == "1"
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)

	subjectID, err := s.authenticateViewer(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	s.mu.Lock()
	share := s.shares[shareID]
	if share == nil || s.expiredLocked(share) {
		s.mu.Unlock()
		http.Error(w, "share 不存在或已过期", http.StatusNotFound)
		return
	}
	if !s.viewerAllowedLocked(share, subjectID) {
		s.mu.Unlock()
		s.recordAudit(AuditEvent{Action: "viewer-denied", Actor: subjectActor(subjectID),
			Detail: map[string]any{"shareId": shareID, "reason": "not-in-allowlist"}})
		http.Error(w, "这个 viewer 不在 share 的允许名单里", http.StatusForbidden)
		return
	}
	if wantFork && !share.AllowsFork {
		s.mu.Unlock()
		s.recordAudit(AuditEvent{Action: "viewer-denied", Actor: subjectActor(subjectID),
			Detail: map[string]any{"shareId": shareID, "reason": "fork-not-allowed"}})
		http.Error(w, "这个 share 不允许 fork", http.StatusForbidden)
		return
	}
	s.mu.Unlock()

	conn, err := ws.Accept(w, r)
	if err != nil {
		return
	}

	s.mu.Lock()
	share = s.shares[shareID]
	if share == nil || s.expiredLocked(share) {
		s.mu.Unlock()
		_ = writeJSONConn(conn, event("closed", map[string]any{"reason": "stopped"}))
		_ = conn.Close()
		return
	}
	if !s.viewerAllowedLocked(share, subjectID) || (wantFork && !share.AllowsFork) {
		s.mu.Unlock()
		_ = writeJSONConn(conn, event("closed", map[string]any{"reason": "denied"}))
		_ = conn.Close()
		return
	}
	viewerID, err := newShareID()
	if err != nil {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	viewer := &viewerConn{id: "viewer-" + strings.TrimPrefix(viewerID, "share-"), conn: conn,
		shareID: shareID, subjectID: subjectID}
	forkID := ""
	if wantFork {
		forkID = "fork-" + strings.TrimPrefix(mustShareID(), "share-")
		share.forks[forkID] = &Fork{ID: forkID, ShareID: shareID, CreatedAt: s.now(), Viewers: 1}
		viewer.forkID = forkID
	}
	share.viewers[viewer] = struct{}{}
	hello := event("hello", map[string]any{
		"shareId":            share.ShareID,
		"forkId":             forkID,
		"sessionMode":        share.SessionMode,
		"allowsFork":         share.AllowsFork,
		"viewerCapabilities": share.ViewerCapabilities,
		"seq":                share.seq,
		"subjectId":          subjectID,
	})
	var backlog []frameRecord
	backlog = snapshotFramesLocked(share, since)
	s.auditLocked(AuditEvent{Action: "viewer-connected", Actor: subjectActor(subjectID),
		Detail: map[string]any{"shareId": shareID, "forkId": forkID, "viewerId": viewer.id}})
	if forkID != "" {
		s.auditLocked(AuditEvent{Action: "fork-created", Actor: subjectActor(subjectID),
			Detail: map[string]any{"shareId": shareID, "forkId": forkID}})
	}
	s.mu.Unlock()

	if err := writeJSONConn(conn, hello); err != nil {
		s.dropViewer(viewer)
		return
	}
	for _, frame := range backlog {
		if err := writeJSONConn(conn, event("frame", map[string]any{"seq": frame.Seq, "data": frame.Data})); err != nil {
			s.dropViewer(viewer)
			return
		}
	}

	for {
		raw, err := conn.ReadMessage()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, ws.ErrClosed) {
				// 读失败就是连接断了；不需要额外审计噪音。
			}
			break
		}
		s.handleViewerMessage(viewer, raw)
	}
	s.dropViewer(viewer)
}

// handleViewerMessage 处理 viewer 发来的消息。
//
// 只认两种：ping（回 pong）与 input（写模式下转发给 owner 的发布连接）。
// 只读 share 发 input 会收到一条结构化 read_only 错误——静默丢弃会让 viewer
// 以为自己的操作生效了。
func (s *Server) handleViewerMessage(viewer *viewerConn, raw []byte) {
	var msg struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		_ = writeJSONConn(viewer.conn, event("error", map[string]any{"code": "bad_message", "message": "不是 JSON 对象"}))
		return
	}
	switch msg.Type {
	case "ping":
		_ = writeJSONConn(viewer.conn, event("pong", nil))
	case "input":
		s.mu.Lock()
		share := s.shares[viewer.shareID]
		if share == nil {
			s.mu.Unlock()
			_ = viewer.conn.WriteClose(1000, "stopped")
			return
		}
		if share.SessionMode != "write" {
			s.mu.Unlock()
			_ = writeJSONConn(viewer.conn, event("error", map[string]any{
				"code": "read_only", "message": "这个 share 是只读的（sessionMode=view）"}))
			return
		}
		publisher := share.publisher
		s.mu.Unlock()
		if publisher == nil {
			_ = writeJSONConn(viewer.conn, event("error", map[string]any{
				"code": "publisher_offline", "message": "owner 的发布连接不在线"}))
			return
		}
		err := writeJSONConn(publisher, event("viewer-input", map[string]any{
			"viewerId": viewer.id, "forkId": viewer.forkID, "data": msg.Data}))
		if err != nil {
			_ = writeJSONConn(viewer.conn, event("error", map[string]any{
				"code": "publisher_offline", "message": "owner 的发布连接已断开"}))
		}
	default:
		_ = writeJSONConn(viewer.conn, event("error", map[string]any{
			"code": "unknown_message", "message": fmt.Sprintf("不认识的 type %q", msg.Type)}))
	}
}

// dropViewer 把连接从 share 上摘掉并关掉。
func (s *Server) dropViewer(viewer *viewerConn) {
	_ = viewer.conn.Close()
	s.mu.Lock()
	share := s.shares[viewer.shareID]
	if share != nil {
		if _, ok := share.viewers[viewer]; ok {
			delete(share.viewers, viewer)
			if viewer.forkID != "" {
				if fork := share.forks[viewer.forkID]; fork != nil {
					fork.Viewers--
				}
			}
			s.auditLocked(AuditEvent{Action: "viewer-disconnected", Actor: subjectActor(viewer.subjectID),
				Detail: map[string]any{"shareId": viewer.shareID, "forkId": viewer.forkID, "viewerId": viewer.id}})
		}
	}
	s.mu.Unlock()
}

// handlePublisherWS 是 GET /v1/publish?share=…（owner 凭据）。
//
// 这是 owner 网关到 relay 的**数据面**：host 把会话帧写进来，relay 广播给
// viewer；viewer 的 input 也经这条连接回到 host。owner 不在线时 share 的
// online=false（list 里如实反映，不假装在线）。
func (s *Server) handlePublisherWS(w http.ResponseWriter, r *http.Request) {
	if !s.isOwnerRequest(r) {
		http.Error(w, "owner token 无效", http.StatusUnauthorized)
		return
	}
	shareID := strings.TrimSpace(r.URL.Query().Get("share"))
	if shareID == "" {
		http.Error(w, "缺 share 参数", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	share := s.shares[shareID]
	if share == nil || s.expiredLocked(share) {
		s.mu.Unlock()
		http.Error(w, "share 不存在或已过期", http.StatusNotFound)
		return
	}
	s.mu.Unlock()

	conn, err := ws.Accept(w, r)
	if err != nil {
		return
	}

	s.mu.Lock()
	share = s.shares[shareID]
	if share == nil {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	if old := share.publisher; old != nil {
		_ = old.WriteClose(1000, "replaced")
	}
	share.publisher = conn
	lastSeq := share.seq
	s.auditLocked(AuditEvent{Action: "publisher-connected", Actor: "owner",
		Detail: map[string]any{"shareId": shareID}})
	s.mu.Unlock()

	if err := writeJSONConn(conn, event("hello", map[string]any{"shareId": shareID, "seq": lastSeq})); err != nil {
		s.dropPublisher(shareID, conn)
		return
	}

	for {
		raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			_ = writeJSONConn(conn, event("error", map[string]any{"code": "bad_message"}))
			continue
		}
		switch msg.Type {
		case "ping":
			_ = writeJSONConn(conn, event("pong", nil))
		case "frame":
			s.mu.Lock()
			share = s.shares[shareID]
			if share == nil {
				s.mu.Unlock()
				_ = conn.WriteClose(1000, "stopped")
				return
			}
			frame := appendFrameLocked(share, msg.Data)
			broadcastLocked(share, event("frame", map[string]any{"seq": frame.Seq, "data": frame.Data}))
			s.mu.Unlock()
		case "state":
			// 预留：owner 上报当前状态；当前不广播，避免伪造"在线"。
		default:
			_ = writeJSONConn(conn, event("error", map[string]any{"code": "unknown_message"}))
		}
	}
	s.dropPublisher(shareID, conn)
}

func (s *Server) dropPublisher(shareID string, conn *ws.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	share := s.shares[shareID]
	if share != nil && share.publisher == conn {
		share.publisher = nil
		s.auditLocked(AuditEvent{Action: "publisher-disconnected", Actor: "owner",
			Detail: map[string]any{"shareId": shareID}})
	}
	s.mu.Unlock()
}

// subjectActor 把 viewer 身份翻成审计 actor；匿名用空串（不带假名字）。
func subjectActor(subjectID string) string {
	if subjectID == "" {
		return "anonymous"
	}
	return subjectID
}

// mustShareID 只用于 fork id 的内部生成；随机失败概率可忽略，但绝不返回
// 一个可预测的常量 id。
func mustShareID() string {
	id, err := newShareID()
	if err != nil {
		return "share-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return id
}
