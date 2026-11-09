package relay

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// auditLimitPerShare 限制单次 audit 查询返回的最大条数：审计文件可以无限增长，
// 但一次响应必须有界（客户端拿到的永远是最近的一段真实记录）。
const auditLimitPerShare = 500

// Server 是 self-hosted Liveshare relay。
//
// 真实状态全在磁盘与内存里：owner token 落 <home>/owner-token.json（0600），
// share 记录落 relay-state.json（原子写），审计落 audit.jsonl（append+fsync）。
// 连接（viewer/publisher）只在内存，进程退出即断开——这是真实状态，不是丢数据。
type Server struct {
	cfg       Config
	token     string
	tokenPath string

	mu     sync.Mutex
	shares map[string]*shareRecord
	audit  []AuditEvent

	auditMu sync.Mutex
	warnMu  sync.Mutex
	warn    []string

	httpSrv *http.Server
	ln      net.Listener

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// New 创建 relay 并加载/生成持久状态。返回后即开始过期清理（Close 停止）。
func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Home) == "" {
		return nil, errors.New("relay Home 不能为空")
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return nil, fmt.Errorf("建 relay home 失败: %w", err)
	}
	token, tokenPath, err := loadOrCreateOwnerToken(cfg.Home, cfg.OwnerToken)
	if err != nil {
		return nil, err
	}
	persisted, err := loadPersistedShares(cfg.Home)
	if err != nil {
		return nil, err
	}
	audit, auditErr := loadAudit(cfg.Home)
	s := &Server{
		cfg:       cfg,
		token:     token,
		tokenPath: tokenPath,
		shares:    make(map[string]*shareRecord, len(persisted)),
		audit:     audit,
		stopCh:    make(chan struct{}),
	}
	if auditErr != nil {
		s.warn = append(s.warn, auditErr.Error())
	}
	for _, p := range persisted {
		s.shares[p.ShareID] = &shareRecord{
			persistedShare: p,
			viewers:        map[*viewerConn]struct{}{},
			forks:          map[string]*Fork{},
		}
	}
	s.httpSrv = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	s.wg.Add(1)
	go s.expireLoop()
	return s, nil
}

// Warnings 返回启动时发现但不致命的告警（例如审计文件有坏行）。
func (s *Server) Warnings() []string {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	return append([]string(nil), s.warn...)
}

func (s *Server) addWarning(msg string) {
	s.warnMu.Lock()
	s.warn = append(s.warn, msg)
	s.warnMu.Unlock()
}

// OwnerToken 返回当前 owner token。只给本机 bootstrap/测试用；日志里不许打印。
func (s *Server) OwnerToken() string { return s.token }

// OwnerTokenPath 返回 token 文件路径（CLI 提示用，路径不是秘密）。
func (s *Server) OwnerTokenPath() string { return s.tokenPath }

// Handler 返回 HTTP 处理器（测试可以直接挂 httptest.Server）。
func (s *Server) Handler() http.Handler { return s.routes() }

// Addr 返回监听地址（Listen 之前为空）。
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Listen 在 addr 上开始服务并返回真实地址（支持 127.0.0.1:0）。
func (s *Server) Listen(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.ln = ln
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.httpSrv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.addWarning("relay http 服务退出: " + err.Error())
		}
	}()
	return ln.Addr().String(), nil
}

// Close 停止清理协程、关闭监听与所有连接。可重复调用。
func (s *Server) Close() error {
	var stopErr error
	s.stopOnce.Do(func() {
		close(s.stopCh)
		if s.httpSrv != nil {
			stopErr = s.httpSrv.Close()
		}
		s.mu.Lock()
		for _, share := range s.shares {
			for viewer := range share.viewers {
				_ = viewer.conn.Close()
			}
			share.viewers = map[*viewerConn]struct{}{}
			if share.publisher != nil {
				_ = share.publisher.Close()
				share.publisher = nil
			}
		}
		s.mu.Unlock()
	})
	s.wg.Wait()
	return stopErr
}

func (s *Server) expireLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cfg.expireInterval())
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.expireNow()
		}
	}
}

// expireNow 清掉所有过期 share（关闭 viewer/publisher、写审计、落盘）。
func (s *Server) expireNow() {
	s.mu.Lock()
	changed := false
	for id, share := range s.shares {
		if s.expiredLocked(share) {
			s.removeShareLocked(share, "expired")
			delete(s.shares, id)
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.persist()
	}
}

// expiredLocked 判断 share 是否过期；过期时立刻清掉（惰性 + 定时双保险）。
func (s *Server) expiredLocked(share *shareRecord) bool {
	if share.ExpiresAt == nil {
		return false
	}
	return !s.now().Before(*share.ExpiresAt)
}

// removeShareLocked 关闭一条 share 的全部连接并写审计。调用方负责从 map 删除
// 与后续 persist。返回 (viewersClosed, forksRevoked)。
func (s *Server) removeShareLocked(share *shareRecord, reason string) (int, int) {
	viewersClosed := 0
	forksRevoked := 0
	forkIDs := make([]string, 0, len(share.forks))
	for id := range share.forks {
		forkIDs = append(forkIDs, id)
	}
	sort.Strings(forkIDs)
	for _, id := range forkIDs {
		forksRevoked++
		s.auditLocked(AuditEvent{Action: "fork-revoked", Actor: "relay",
			Detail: map[string]any{"shareId": share.ShareID, "forkId": id, "reason": reason}})
	}
	for viewer := range share.viewers {
		viewersClosed++
		viewerReason := reason
		if viewer.forkID != "" {
			viewerReason = "fork-revoked"
		}
		_ = writeConnTimed(viewer.conn, event("closed", map[string]any{"reason": viewerReason}))
		_ = viewer.conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
		_ = viewer.conn.WriteClose(1000, viewerReason)
	}
	share.viewers = map[*viewerConn]struct{}{}
	share.forks = map[string]*Fork{}
	if share.publisher != nil {
		_ = writeConnTimed(share.publisher, event("closed", map[string]any{"reason": reason}))
		_ = share.publisher.SetWriteDeadline(time.Now().Add(connWriteTimeout))
		_ = share.publisher.WriteClose(1000, reason)
		share.publisher = nil
	}
	s.auditLocked(AuditEvent{Action: "share-" + reason, Actor: "relay",
		Detail: map[string]any{"shareId": share.ShareID, "viewersClosed": viewersClosed, "forksRevoked": forksRevoked}})
	return viewersClosed, forksRevoked
}

func (s *Server) now() time.Time { return s.cfg.now() }

// ---------------------------------------------------------------------------
// 路由与鉴权
// ---------------------------------------------------------------------------

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/owner/config", s.ownerOnly(s.handleOwnerConfig))
	mux.HandleFunc("GET /v1/owner/identity", s.ownerOnly(s.handleOwnerIdentity))
	mux.HandleFunc("GET /v1/shares", s.ownerOnly(s.handleListShares))
	mux.HandleFunc("POST /v1/shares", s.ownerOnly(s.handleCreateShare))
	mux.HandleFunc("GET /v1/shares/{id}", s.ownerOnly(s.handleGetShare))
	mux.HandleFunc("PATCH /v1/shares/{id}", s.ownerOnly(s.handlePatchShare))
	mux.HandleFunc("DELETE /v1/shares/{id}", s.ownerOnly(s.handleDeleteShare))
	mux.HandleFunc("GET /v1/shares/{id}/audit", s.ownerOnly(s.handleAudit))
	mux.HandleFunc("GET /v1/shares/{id}/capabilities", s.ownerOnly(s.handleGetCapabilities))
	mux.HandleFunc("PUT /v1/shares/{id}/capabilities", s.ownerOnly(s.handlePutCapabilities))
	mux.HandleFunc("GET /v1/connect", s.handleViewerWS)
	mux.HandleFunc("GET /v1/publish", s.handlePublisherWS)
	mux.HandleFunc("GET /s/{id}", s.handleViewerPage)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("relay 没有这个路由：%s %s", r.Method, r.URL.Path))
	})
	return mux
}

func (s *Server) ownerOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isOwnerRequest(r) {
			writeError(w, http.StatusUnauthorized, "owner_token_invalid",
				"owner token 无效：请求头要带 Authorization: Bearer <token>（token 在 relay home 的 owner-token.json）")
			return
		}
		next(w, r)
	}
}

// isOwnerRequest 用常量时间比较校验 owner token。
func (s *Server) isOwnerRequest(r *http.Request) bool {
	got := ownerTokenFromRequest(r)
	if got == "" || s.token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func ownerTokenFromRequest(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
			return strings.TrimSpace(auth[7:])
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Liveshare-Owner-Token"))
}

// authenticateViewer 解析 viewer 身份：没有凭据 = 匿名（空 subject）。
//
// 凭据来源：Authorization: Bearer <secret>，或 Sec-WebSocket-Protocol 里的
// `liveshare-secret.<secret>`（浏览器 WebSocket 不能自定义 Authorization 头，
// 但可以带子协议）。relay 是唯一验证者，host 侧不编身份。
func (s *Server) authenticateViewer(r *http.Request) (string, error) {
	secret := ""
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
			secret = strings.TrimSpace(auth[7:])
		}
	}
	for _, proto := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, part := range strings.Split(proto, ",") {
			part = strings.TrimSpace(part)
			if after, ok := strings.CutPrefix(part, "liveshare-secret."); ok {
				secret = after
			}
		}
	}
	if secret == "" {
		return "", nil
	}
	for _, viewer := range s.cfg.Viewers {
		if subtle.ConstantTimeCompare([]byte(secret), []byte(viewer.Secret)) == 1 {
			return viewer.SubjectID, nil
		}
	}
	return "", errors.New("viewer 凭据无效")
}

// viewerAllowedLocked 判断某个 subject 能不能看这条 share。
func (s *Server) viewerAllowedLocked(share *shareRecord, subjectID string) bool {
	if share.AccessMode != "selected" {
		return true
	}
	if subjectID == "" {
		return false
	}
	for _, id := range share.AllowedSubjectIDs {
		if id == subjectID {
			return true
		}
	}
	for _, id := range s.cfg.DeploymentAllowedSubjectIDs {
		if id == subjectID {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// HTTP 处理器
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"service":         "dsh-relayd",
		"version":         Version,
		"ownerConfigured": s.token != "",
		"providerId":      s.cfg.providerID(),
		"tenantId":        s.cfg.tenantID(),
	})
}

func (s *Server) handleOwnerConfig(w http.ResponseWriter, _ *http.Request) {
	viewerAuth := "none"
	if len(s.cfg.Viewers) > 0 {
		viewerAuth = "secret"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"providerId":                  s.cfg.providerID(),
		"tenantId":                    s.cfg.tenantID(),
		"deploymentAllowedSubjectIds": append([]string{}, s.cfg.DeploymentAllowedSubjectIDs...),
		"viewerAuth":                  viewerAuth,
	})
}

// handleOwnerIdentity 返回 relay 对 owner token 的验证结论。
//
// state 只有 verified：能走到这里就说明 token 校验通过（中间件已经拒了其它情况）。
// providerId/tenantId 明确写 self-hosted/local，displayName 来自 relay 配置，
// 不会冒充官方账号资料。
func (s *Server) handleOwnerIdentity(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"state":       "verified",
		"providerId":  s.cfg.providerID(),
		"tenantId":    s.cfg.tenantID(),
		"subjectId":   s.cfg.ownerSubjectID(),
		"displayName": s.cfg.ownerDisplayName(),
	})
}

type createShareRequest struct {
	SessionID         string   `json:"sessionId"`
	SessionTitle      string   `json:"sessionTitle"`
	AccessMode        string   `json:"accessMode"`
	TTLMinutes        *float64 `json:"ttlMinutes"`
	AllowedSubjectIDs []string `json:"allowedSubjectIds"`
	AllowsFork        bool     `json:"allowsFork"`
	SessionMode       string   `json:"sessionMode"`
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req createShareRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "sessionId 不能为空")
		return
	}
	if req.SessionTitle == "" {
		req.SessionTitle = req.SessionID
	}
	switch req.SessionMode {
	case "":
		req.SessionMode = "view"
	case "view", "write":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "sessionMode 只能是 view 或 write")
		return
	}
	switch req.AccessMode {
	case "":
		req.AccessMode = "open"
	case "open", "selected":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "accessMode 只能是 open 或 selected")
		return
	}
	allowed, err := normalizeSubjectIDs(req.AllowedSubjectIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var expiresAt *time.Time
	if req.TTLMinutes != nil {
		ttl := *req.TTLMinutes
		if math.IsNaN(ttl) || math.IsInf(ttl, 0) || ttl <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "ttlMinutes 必须是正数")
			return
		}
		if ttl > 525600 {
			writeError(w, http.StatusBadRequest, "bad_request", "ttlMinutes 上限是 525600（一年）")
			return
		}
		deadline := s.now().Add(time.Duration(ttl * float64(time.Minute)))
		expiresAt = &deadline
	}
	if req.AccessMode == "selected" && len(s.cfg.Viewers) == 0 && len(s.cfg.DeploymentAllowedSubjectIDs) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request",
			"selected 分享需要 relay 配置 viewer 身份目录（--viewers）或部署允许名单（--allowed-subject）")
		return
	}
	if req.AccessMode == "selected" && len(allowed) == 0 && len(s.cfg.DeploymentAllowedSubjectIDs) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request",
			"selected 分享必须给 allowedSubjectIds（或 relay 有部署级允许名单）")
		return
	}

	shareID, err := newShareID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "生成 shareId 失败")
		return
	}
	share := &shareRecord{
		persistedShare: persistedShare{
			ShareID:           shareID,
			SessionID:         req.SessionID,
			SessionTitle:      req.SessionTitle,
			Link:              s.linkFor(r, shareID),
			SessionMode:       req.SessionMode,
			AccessMode:        req.AccessMode,
			AllowedSubjectIDs: allowed,
			AllowsFork:        req.AllowsFork,
			CreatedAt:         s.now().UTC(),
			ExpiresAt:         expiresAt,
		},
		viewers: map[*viewerConn]struct{}{},
		forks:   map[string]*Fork{},
	}
	s.mu.Lock()
	s.shares[shareID] = share
	view := s.shareViewLocked(share)
	s.auditLocked(AuditEvent{Action: "share-created", Actor: "owner",
		Detail: map[string]any{"shareId": shareID, "sessionId": req.SessionID,
			"sessionMode": req.SessionMode, "accessMode": req.AccessMode}})
	s.mu.Unlock()
	s.persist()
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleListShares(w http.ResponseWriter, _ *http.Request) {
	s.expireNow()
	s.mu.Lock()
	shares := make([]Share, 0, len(s.shares))
	for _, share := range s.shares {
		shares = append(shares, s.shareViewLocked(share))
	}
	s.mu.Unlock()
	sort.Slice(shares, func(i, j int) bool { return shares[i].CreatedAt.Before(shares[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares})
}

func (s *Server) handleGetShare(w http.ResponseWriter, r *http.Request) {
	s.expireNow()
	s.mu.Lock()
	share := s.shares[r.PathValue("id")]
	if share == nil {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "share_not_found", "没有这个 share（可能已 stop 或过期）")
		return
	}
	view := s.shareViewLocked(share)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, view)
}

type patchShareRequest struct {
	SessionMode  *string         `json:"sessionMode"`
	AllowsFork   *bool           `json:"allowsFork"`
	TTLMinutes   json.RawMessage `json:"ttlMinutes"` // 缺省=不改；null=永不过期；数字=新截止
	Capabilities *[]Capability   `json:"capabilities"`
}

func (s *Server) handlePatchShare(w http.ResponseWriter, r *http.Request) {
	s.expireNow()
	var req patchShareRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.SessionMode == nil && req.AllowsFork == nil && len(req.TTLMinutes) == 0 && req.Capabilities == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "至少给一个要改的字段")
		return
	}
	if req.SessionMode != nil && *req.SessionMode != "view" && *req.SessionMode != "write" {
		writeError(w, http.StatusBadRequest, "bad_request", "sessionMode 只能是 view 或 write")
		return
	}
	var expiresAt *time.Time
	clearExpiry := false
	if len(req.TTLMinutes) > 0 {
		if strings.TrimSpace(string(req.TTLMinutes)) == "null" {
			clearExpiry = true
		} else {
			var ttl float64
			if err := json.Unmarshal(req.TTLMinutes, &ttl); err != nil || math.IsNaN(ttl) || math.IsInf(ttl, 0) || ttl <= 0 {
				writeError(w, http.StatusBadRequest, "bad_request", "ttlMinutes 必须是正数或 null")
				return
			}
			deadline := s.now().Add(time.Duration(ttl * float64(time.Minute)))
			expiresAt = &deadline
		}
	}

	s.mu.Lock()
	share := s.shares[r.PathValue("id")]
	if share == nil || s.expiredLocked(share) {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "share_not_found", "没有这个 share（可能已 stop 或过期）")
		return
	}
	changedPermissions := false
	changedCapabilities := false
	if req.SessionMode != nil {
		share.SessionMode = *req.SessionMode
		changedPermissions = true
	}
	if req.AllowsFork != nil {
		share.AllowsFork = *req.AllowsFork
		changedPermissions = true
	}
	if len(req.TTLMinutes) > 0 {
		if clearExpiry {
			share.ExpiresAt = nil
		} else {
			share.ExpiresAt = expiresAt
		}
		changedPermissions = true
	}
	if req.Capabilities != nil {
		share.ViewerCapabilities = normalizeCapabilities(*req.Capabilities)
		changedCapabilities = true
	}
	view := s.shareViewLocked(share)
	if changedPermissions {
		broadcastLocked(share, event("permissions", map[string]any{
			"sessionMode": share.SessionMode, "allowsFork": share.AllowsFork,
			"expiresAt": share.ExpiresAt,
		}))
		s.auditLocked(AuditEvent{Action: "share-updated", Actor: "owner",
			Detail: map[string]any{"shareId": share.ShareID, "sessionMode": share.SessionMode,
				"allowsFork": share.AllowsFork, "expiresAt": share.ExpiresAt}})
	}
	if changedCapabilities {
		broadcastLocked(share, event("capabilities", map[string]any{
			"viewerCapabilities": share.ViewerCapabilities,
		}))
		s.auditLocked(AuditEvent{Action: "capabilities-updated", Actor: "owner",
			Detail: map[string]any{"shareId": share.ShareID, "count": len(share.ViewerCapabilities)}})
	}
	s.mu.Unlock()
	s.persist()
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	share := s.shares[r.PathValue("id")]
	if share == nil {
		s.mu.Unlock()
		// 幂等：已经不在 relay 上 = 目标状态已达成。
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revokedForks": 0, "viewersClosed": 0})
		return
	}
	viewersClosed, forksRevoked := s.removeShareLocked(share, "stopped")
	delete(s.shares, share.ShareID)
	s.mu.Unlock()
	s.persist()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "revokedForks": forksRevoked, "viewersClosed": viewersClosed,
	})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	s.expireNow()
	shareID := r.PathValue("id")
	s.mu.Lock()
	events := make([]AuditEvent, 0, len(s.audit))
	_, live := s.shares[shareID]
	for _, event := range s.audit {
		detail, _ := event.Detail.(map[string]any)
		got, _ := detail["shareId"].(string)
		if got == shareID {
			events = append(events, event)
		}
	}
	s.mu.Unlock()
	if !live && len(events) == 0 {
		writeError(w, http.StatusNotFound, "share_not_found", "没有这个 share 的任何记录（可能从未创建）")
		return
	}
	if len(events) > auditLimitPerShare {
		events = events[len(events)-auditLimitPerShare:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleGetCapabilities(w http.ResponseWriter, r *http.Request) {
	s.expireNow()
	s.mu.Lock()
	share := s.shares[r.PathValue("id")]
	if share == nil {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "share_not_found", "没有这个 share")
		return
	}
	caps := append([]Capability{}, share.ViewerCapabilities...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"shareId": r.PathValue("id"), "viewerCapabilities": caps})
}

func (s *Server) handlePutCapabilities(w http.ResponseWriter, r *http.Request) {
	s.expireNow()
	var req struct {
		Capabilities []Capability `json:"capabilities"`
	}
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Capabilities == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "缺 capabilities（空数组表示清空）")
		return
	}
	s.mu.Lock()
	share := s.shares[r.PathValue("id")]
	if share == nil {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "share_not_found", "没有这个 share")
		return
	}
	share.ViewerCapabilities = normalizeCapabilities(req.Capabilities)
	caps := append([]Capability{}, share.ViewerCapabilities...)
	broadcastLocked(share, event("capabilities", map[string]any{"viewerCapabilities": caps}))
	s.auditLocked(AuditEvent{Action: "capabilities-updated", Actor: "owner",
		Detail: map[string]any{"shareId": share.ShareID, "count": len(caps)}})
	s.mu.Unlock()
	s.persist()
	writeJSON(w, http.StatusOK, map[string]any{"shareId": r.PathValue("id"), "viewerCapabilities": caps})
}

// ---------------------------------------------------------------------------
// 视图与链接
// ---------------------------------------------------------------------------

func (s *Server) shareViewLocked(share *shareRecord) Share {
	view := Share{
		ShareID:            share.ShareID,
		SessionID:          share.SessionID,
		SessionTitle:       share.SessionTitle,
		Link:               share.Link,
		CreatedAt:          share.CreatedAt,
		ExpiresAt:          share.ExpiresAt,
		AllowsFork:         share.AllowsFork,
		SessionMode:        share.SessionMode,
		ViewerCapabilities: append([]Capability{}, share.ViewerCapabilities...),
		Online:             share.publisher != nil,
		AccessMode:         share.AccessMode,
		AllowedSubjectIDs:  append([]string{}, share.AllowedSubjectIDs...),
	}
	return view
}

func (s *Server) linkFor(r *http.Request, shareID string) string {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.PublicBaseURL), "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded == "http" || forwarded == "https" {
			scheme = forwarded
		}
		base = scheme + "://" + r.Host
	}
	return base + "/s/" + shareID
}

// persist 把当前 share 集合落盘。调用方已脱离 mu（或持有后已释放）。
func (s *Server) persist() {
	s.mu.Lock()
	shares := make([]persistedShare, 0, len(s.shares))
	for _, share := range s.shares {
		shares = append(shares, share.persistedShare)
	}
	s.mu.Unlock()
	sort.Slice(shares, func(i, j int) bool { return shares[i].CreatedAt.Before(shares[j].CreatedAt) })
	if err := savePersistedShares(s.cfg.Home, shares); err != nil {
		s.addWarning("写 relay-state.json 失败: " + err.Error())
	}
}

// ---------------------------------------------------------------------------
// 审计
// ---------------------------------------------------------------------------

// auditLocked 在持 mu 时追加一条审计：内存 + 磁盘（append+fsync）。
func (s *Server) auditLocked(e AuditEvent) {
	if e.ID == "" {
		e.ID = mustAuditID()
	}
	e.CreatedAt = s.now().UTC()
	s.audit = append(s.audit, e)
	s.writeAudit(e)
}

// recordAudit 是不持锁版本的审计追加（鉴权拒绝等路径用）。
func (s *Server) recordAudit(e AuditEvent) {
	s.mu.Lock()
	s.auditLocked(e)
	s.mu.Unlock()
}

func (s *Server) writeAudit(e AuditEvent) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	if err := appendAudit(s.cfg.Home, e); err != nil {
		s.addWarning("写 audit.jsonl 失败: " + err.Error())
	}
}

func mustAuditID() string {
	id, err := newAuditID()
	if err != nil {
		return fmt.Sprintf("audit-%d", time.Now().UnixNano())
	}
	return id
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

func normalizeCapabilities(in []Capability) []Capability {
	out := make([]Capability, 0, len(in))
	for _, cap := range in {
		readable := append([]string{}, cap.Readable...)
		writable := append([]string{}, cap.Writable...)
		sort.Strings(readable)
		sort.Strings(writable)
		out = append(out, Capability{Plugin: cap.Plugin, Readable: readable, Writable: writable})
	}
	return out
}

func decodeStrictJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("请求体不是合法 JSON 或含未知字段: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": code, "message": message,
	}})
}

// handleViewerPage 是一个最小可用的 viewer 页面：连上 WS 后把收到的帧打印出来，
// 让 create 返回的 link 不是死链。生产级 viewer 由后续 GUI 线负责。
func (s *Server) handleViewerPage(w http.ResponseWriter, r *http.Request) {
	shareID := r.PathValue("id")
	s.mu.Lock()
	share := s.shares[shareID]
	ok := share != nil && !s.expiredLocked(share)
	mode := ""
	if ok {
		mode = share.SessionMode
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "share 不存在或已过期", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(viewerPageHTML(shareID, mode)))
}

func viewerPageHTML(shareID, mode string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>DSH Liveshare viewer</title>
<style>body{font:14px/1.5 system-ui,sans-serif;margin:24px;max-width:900px}pre{background:#f5f5f5;padding:12px;border-radius:6px;white-space:pre-wrap}input{width:70%;padding:6px}</style></head>
<body><h1>DSH Liveshare viewer</h1>
<p>share: <code>` + shareID + `</code> · mode: <code>` + mode + `</code></p>
<pre id="log"></pre>
<div id="write" style="display:none"><input id="msg" placeholder="给 owner 发一条输入（write 模式）"><button onclick="send()">发送</button></div>
<script>
const log = (s) => { const el = document.getElementById('log'); el.textContent += s + '\n'; };
const ws = new WebSocket(location.origin.replace('http','ws') + '/v1/connect?share=' + encodeURIComponent('` + shareID + `'), ['liveshare.v1']);
ws.onmessage = (e) => { const m = JSON.parse(e.data); if (m.type === 'frame') log('frame #' + m.seq + ': ' + JSON.stringify(m.data)); else log(JSON.stringify(m)); };
ws.onerror = () => log('连接失败：share 可能已停止/过期，或需要 viewer 凭据');
ws.onclose = (e) => log('连接关闭：' + e.code);
if ('` + mode + `' === 'write') document.getElementById('write').style.display = 'block';
function send(){ const el = document.getElementById('msg'); if(!el.value) return; ws.send(JSON.stringify({type:'input',data:{text:el.value}})); el.value=''; }
</script></body></html>`
}
