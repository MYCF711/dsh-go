package transport

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 界面线的三条铁律，本文件是它们的落点：
//
//  1. 端口一律由 OS 分配（默认 "127.0.0.1:0"），不许写死；
//  2. 壳从 Start 返回的地址拿 URL，不自己拼；
//  3. launch token 经 GET /connect 换 HttpOnly cookie，cookie 永不进 WebView。
//
// 本文件只做协议翻译与认证，不读盘、不 import 会话编解码包（红线 R7）。
// 会话数据的来源由宿主注入（Options.Source / Options.List）。

const (
	// defaultAddr 是开始监听的默认地址：端口 0 = 交给 OS 分配。
	defaultAddr = "127.0.0.1:0"

	// cookieName 是会话 cookie 的名字。
	cookieName = "dshd_session"

	// secretBytes 是 launch token 与 cookie 值的随机字节数。
	// 32 字节 → 64 位 hex，满足「≥32 字符 hex」。
	secretBytes = 32
)

// Options 起服务要的东西。
type Options struct {
	// Source 提供会话数据；nil 时 /api/session.list 返回空数组（测试用）。
	Source Source
	// Addr 是监听地址，留空则 "127.0.0.1:0"（端口由 OS 分配）。
	Addr string
	// List 列会话的摘要；真正实现由宿主注入，这里不直接读盘。
	// 返回 nil 时按空数组处理。
	List func() ([]SessionSummary, error)
	// Load 取首屏（最新 limit 条）；宿主注入。
	Load func(sessionID string, limit int) (LoadResult, error)
	// History 取断流之后补洞的那些（seq > sinceSeq）；宿主注入。
	History func(sessionID string, sinceSeq, limit int) (Page, error)
	// Prompt 发起一轮，立刻返回（回答走 WS）；宿主注入。
	Prompt func(sessionID, text string) (PromptResult, error)
	// Delete 删掉一个会话；宿主注入（删除是不可逆操作，由宿主决定物理删还是标记）。
	Delete func(sessionID string) error
	// Fork 从一个会话开分支；宿主注入。
	Fork func(sessionID, title string) (string, error)
	// WorkspaceList 列当前工作区的顶层条目；宿主注入。
	WorkspaceList func() ([]WorkspaceEntry, error)
	// FSRead 读一个文件；路径解析、大小上限、越界检查都在宿主那边。
	FSRead func(path string) (FSContent, error)
	// FSWrite 写一个文件；宿主注入。
	FSWrite func(path, content string) error
	// ConfigGet 取全部界面配置；宿主注入。
	ConfigGet func() (map[string]string, error)
	// ConfigSet 改若干项（只覆盖给出的 key）；宿主注入。
	ConfigSet func(kv map[string]string) error
	// Cancel 取消在飞的一轮；没有在飞的返回 ErrNoSuchTurn。
	Cancel func(sessionID string) error
	// SetMode 换模式；宿主注入。
	Create func(mode string) (string, error)
}

// Server 是界面线的宿主入口。
type Server struct {
	hub   *Hub
	opt   Options
	token string // 一次性 launch token，只经 Token() 交出

	mu      sync.Mutex
	used    bool                // launch token 是否已被兑换（一次性）
	cookies map[string]struct{} // 已发放的有效会话 cookie
	srv     *http.Server
	url     string
}

// NewServer 造一个还没开始监听的服务，并生成一次性 launch token。
// 返回的 token 只会出现在这一处——调用方把它交给壳，壳用它换 cookie。
func NewServer(opt Options) (*Server, error) {
	token, err := newSecret()
	if err != nil {
		return nil, err
	}
	return &Server{
		hub:     NewHub(),
		opt:     opt,
		token:   token,
		cookies: make(map[string]struct{}),
	}, nil
}

// Token 给壳用：壳拿它去 GET /connect 换 cookie，之后再不出现。
func (s *Server) Token() string {
	if s == nil {
		return ""
	}
	return s.token
}

// Start 开始监听 opt 里的地址（默认 "127.0.0.1:0"）并返回真实地址，
// 形如 "http://127.0.0.1:52341"。重复调用返回同一个地址。
func (s *Server) Start() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.srv != nil {
		return s.url, nil
	}

	addr := s.opt.Addr
	if addr == "" {
		addr = defaultAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}

	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.srv = srv
	s.url = baseURL(ln.Addr())

	go func() {
		// 监听已被 Close 时这里返回 ErrServerClosed，不是错误。
		_ = srv.Serve(ln)
	}()
	return s.url, nil
}

// Close 关掉监听。没 Start 过时是空操作。
func (s *Server) Close() error {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()

	if srv == nil {
		return nil
	}
	return srv.Close()
}

// ServeHTTP 只有三条路由，别加。
//
//	GET  /connect?token=<launch>
//	POST /api/<method>
//	其它 404
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/connect":
		s.handleConnect(w, r)
	case r.URL.Path == "/debug/stats":
		s.handleDebugStats(w, r)
	case r.URL.Path == "/ws":
		s.handleWS(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		s.handleAPI(w, r)
	default:
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
	}
}

// Publish 往某个 topic 推一帧。宿主侧在 turn 产生事件时调它。
func (s *Server) Publish(topic string, f ServerFrame) {
	if s == nil || s.hub == nil {
		return
	}
	s.hub.Publish(topic, f)
}

// handleDebugStats 暴露两个"长跑才会出问题"的数：goroutine 数与堆大小。
//
// 为什么需要它：serve 会为每一轮起 follow goroutine。
// 它有没有按时退出，从外面完全看不出来——除非把数打出来。
// 要 cookie：这不是给公网看的东西。
func (s *Server) handleDebugStats(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
		return
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	writeJSON(w, http.StatusOK, map[string]any{
		"goroutines": runtime.NumGoroutine(),
		// 三个数放一起才能分清"漏"和"分配器保留"：
		// Sys 涨、HeapAlloc 平、HeapIdle 跟着涨 = 保留；三个都涨才是真漏。
		"heap_bytes":    m.HeapAlloc,
		"heap_idle":     m.HeapIdle,
		"heap_released": m.HeapReleased,
		"sys_bytes":     m.Sys,
	})
}

// handleWS 是界面订阅事件流的地方。
//
// 和 /api 一样要 cookie：**没有 cookie 就不许升级**。
// 升级之后这条连接交给 ServeConn，它负责把 Hub 的帧写回来。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
		return
	}
	conn, err := Upgrade(w, r)
	if err != nil {
		return // Upgrade 已经写过 400
	}
	go ServeConn(conn, s.hub)
}

// handleConnect 拿 launch token 换 cookie。同一个 token 只换一次。
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}

	got := r.URL.Query().Get("token")
	s.mu.Lock()
	// 一次性：换过之后再用必须是 401，且不再生成新 cookie。
	if s.used || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
		return
	}
	value, err := newSecret()
	if err != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, errorBody("cannot mint cookie"))
		return
	}
	s.used = true
	s.cookies[value] = struct{}{}
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}

// handleAPI 走 cookie 认证；没有效 cookie 一律 401。
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/api/")
	// 每个 /api 调用打一行到 stderr。
	//
	// 为什么单独记：界面连没连上宿主，从进程表看不出来（窗口开着、进程在跑，
	// 也可能一次都没调过来）。有了这行，"它到底说没说话"从猜变成查。
	fmt.Fprintf(os.Stderr, "transport: /api/%s\n", name)
	switch Method(name) {
	case MethodSessionList:
		s.handleSessionList(w)
	case MethodSessionLoad:
		s.handleSessionLoad(w, r)
	case MethodSessionHistory:
		s.handleSessionHistory(w, r)
	case MethodSessionPrompt:
		s.handleSessionPrompt(w, r)
	case MethodSessionCancel:
		s.handleSessionCancel(w, r)
	case MethodSessionCreate:
		s.handleSessionCreate(w, r)
	case MethodSessionDelete:
		s.handleSessionDelete(w, r)
	case MethodSessionFork:
		s.handleSessionFork(w, r)
	case MethodWorkspaceList:
		s.handleWorkspaceList(w, r)
	case MethodFSRead:
		s.handleFSRead(w, r)
	case MethodFSWrite:
		s.handleFSWrite(w, r)
	case MethodConfigGet:
		s.handleConfigGet(w, r)
	case MethodConfigSet:
		s.handleConfigSet(w, r)
	default:
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
	}
}

// handleSessionPrompt 是 /api/session.prompt：**立刻返回**，回答走 WS/
// 宿主侧实现负责不阻塞——这里只做协议。
func (s *Server) handleSessionPrompt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID string `json:"sessionId"`
		Text      string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if s.opt.Prompt == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
		return
	}
	res, err := s.opt.Prompt(in.SessionID, in.Text)
	if err != nil {
		// 宿主侧对"上一轮还没结束"会返回错误；协议层不认具体错误类型，
		// 一律按冲突报——界面据此提示"等这一轮结束"，而不是当成参数错误。
		writeJSON(w, http.StatusConflict, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleSessionCancel 是 /api/session.cancel。
func (s *Server) handleSessionCancel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if s.opt.Cancel == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
		return
	}
	if err := s.opt.Cancel(in.SessionID); err != nil {
		writeJSON(w, http.StatusConflict, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

// handleSessionCreate 是 /api/session.create：建会话，返回 id。
func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if s.opt.Create == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
		return
	}
	id, err := s.opt.Create(in.Mode)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": id})
}

// handleSessionLoad 是 /api/session.load：首屏。
func (s *Server) handleSessionLoad(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID string `json:"sessionId"`
		Limit     int    `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if s.opt.Load == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
		return
	}
	res, err := s.opt.Load(in.SessionID, in.Limit)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleSessionHistory 是 /api/session.history：断流后按 seq 补洞。
func (s *Server) handleSessionHistory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID string `json:"sessionId"`
		SinceSeq  int    `json:"sinceSeq"`
		Limit     int    `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if s.opt.History == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
		return
	}
	pg, err := s.opt.History(in.SessionID, in.SinceSeq, in.Limit)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, pg)
}

// handleSessionList 是 /api/session.list：宿主注入的 List 决定内容。
func (s *Server) handleSessionList(w http.ResponseWriter) {
	sessions := make([]SessionSummary, 0)
	if s.opt.List != nil {
		got, err := s.opt.List()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
			return
		}
		if got != nil {
			sessions = got
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Sessions []SessionSummary `json:"sessions"`
	}{Sessions: sessions})
}

// authorized 校验会话 cookie。
//
// 逐个比对全部已发放值（subtle.ConstantTimeCompare），不提前 break，
// 免得"第几个 cookie 命中"从耗时里漏出去。
func (s *Server) authorized(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	got := []byte(c.Value)

	s.mu.Lock()
	defer s.mu.Unlock()

	ok := 0
	for want := range s.cookies {
		ok |= subtle.ConstantTimeCompare(got, []byte(want))
	}
	return ok == 1
}

// newSecret 生成 secretBytes 字节的随机数，返回 hex 串。
func newSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// baseURL 把监听地址变成壳能直接用的 URL。
// 监听在通配地址时换成回环地址给客户端用。
func baseURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func errorBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		// 这里的 payload 全是自家结构，真出错也只剩一个兜底正文。
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"encode failure"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
