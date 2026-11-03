package transport

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
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
}

// Server 是界面线的宿主入口。
type Server struct {
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
	case strings.HasPrefix(r.URL.Path, "/api/"):
		s.handleAPI(w, r)
	default:
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
	}
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

	switch name := strings.TrimPrefix(r.URL.Path, "/api/"); Method(name) {
	case MethodSessionList:
		s.handleSessionList(w)
	default:
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
	}
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
