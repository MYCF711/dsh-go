package transport

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	// ContentWidth 是会话内容列宽（0 = 不知道，界面自己算）。由壳/宿主注入。
	ContentWidth int
	// Policy 是宿主这一进程实际用的审批策略（deny/ask/allow/auto）。
	//
	// 它是**进程级事实**，不是会话级"预设"——H5 要的是"当前权限状态"，
	// 而本仓根本没有 preset 这个概念（preset 在我们的代码里指 mode，那是工具可见性）。
	// 所以这里如实返回 policy 名字；没有就留空，界面整段不画。
	Policy string
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
	// Journals 是每会话的序号域（H14）。非 nil 时 session.history 直接读它，
	// 保证与 WS 推送同一个 seq 域；nil 时退回宿主注入的 History（老行为）。
	Journals *Journals
	// PendingApprovals 给出某会话上仍挂着的审批帧，供新连接订阅时重放（H13）。
	PendingApprovals func(sessionID string) []ServerFrame
	// Approve 回一次审批（H13）：requestID 是服务端推给客户端的那个，optionID 是客户端选的。
	Approve func(requestID, optionID string) error
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
	h := NewHub()
	// 订阅时要核对世代（H14），所以哈希表和序号域必须成对存在。
	h.SetJournals(opt.Journals)
	h.SetPendingReplay(opt.PendingApprovals)
	return &Server{
		hub:     h,
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
	case MethodSessionApprove:
		s.handleSessionApprove(w, r)
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
		// ID 是契约快照里的名字（H9）。两个都收：只认一个就是静默 404。
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if s.opt.Prompt == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
		return
	}
	res, err := s.opt.Prompt(fillID(in.SessionID, in.ID), in.Text)
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
		// ID 是契约快照里的名字（H9）。两个都收：只认一个就是静默 404。
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if s.opt.Cancel == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
		return
	}
	if err := s.opt.Cancel(fillID(in.SessionID, in.ID)); err != nil {
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

// fillID 把契约里的 id 别名补进 SessionID。
//
// 为什么必须有：界面按快照发 `{"id":...}`，宿主只读 `sessionId` → 一律 404，
// 而那个 404 长得像"会话不存在"（2026-11-03 H10 就是这么来的）。
// **同一个字段两个名字，等于两套真相；收下两个名字至少不会静默失败。**
// firstNonZero 取第一个非零值：别名冲突时以第一个为准，不允许"两个都不认"。
func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func fillID(sessionID, id string) string {
	if sessionID != "" {
		return sessionID
	}
	return id
}

// handleSessionLoad 是 /api/session.load：首屏。
func (s *Server) handleSessionLoad(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID string `json:"sessionId"`
		// ID 是契约快照里的名字（H9）。两个都收：只认一个就是静默 404。
		ID    string `json:"id"`
		Limit int    `json:"limit"`
		// TailMessages 是契约快照里的名字（H9）。两个都收；冲突时以 limit 为准。
		TailMessages int `json:"tailMessages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if s.opt.Load == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
		return
	}
	res, err := s.opt.Load(fillID(in.SessionID, in.ID), firstNonZero(in.Limit, in.TailMessages))
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
		return
	}
	// H5：权限状态是进程级事实，在这里补上（宿主注入的 Load 只关心会话内容）。
	res.Permission = s.opt.Policy
	// H12：只有**正数**才算"知道宽度"。0 和负数都当未知、字段直接不出现。
	//
	// 负数放过去不是报错，而是让界面画出一个宽度为负的容器——看起来"怪但没崩"，
	// 这种 bug 没人会去查宿主。宁可不给，让界面自己算。
	if s.opt.ContentWidth > 0 {
		res.ContentWidth = s.opt.ContentWidth
	}
	// H14：load 是首屏投影，不能拿来补洞；但它必须告诉客户端
	// "从哪个世代、哪个水位接着收"——否则客户端只能猜。
	if s.opt.Journals != nil {
		j := s.opt.Journals.Get(fillID(in.SessionID, in.ID))
		res.JournalEpoch = j.Epoch()
		res.LastSeq = j.LastSeq()
	}
	writeJSON(w, http.StatusOK, res)
}

// handleSessionApprove 是 /api/session.approve：客户端对一次审批给出决定（H13）。
func (s *Server) handleSessionApprove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID string `json:"requestId"`
		OptionID  string `json:"optionId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.RequestID == "" || in.OptionID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("缺参数：要 requestId 与 optionId"))
		return
	}
	if s.opt.Approve == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("session.approve 未接线"))
		return
	}
	switch err := s.opt.Approve(in.RequestID, in.OptionID); {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "optionId": in.OptionID})
	case errors.Is(err, ErrNoSuchRequest):
		writeJSON(w, http.StatusNotFound, errorBody("no_such_request"))
	case errors.Is(err, ErrBadOption):
		writeJSON(w, http.StatusBadRequest, errorBody("bad_option"))
	default:
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
	}
}

// handleSessionHistory 是 /api/session.history：断流后按 seq 补洞。
//
// H14：补洞必须返回**同一 journal 的原始帧**，不是抽过 text 的 messages。
// 用投影补洞会丢掉 reasoning/tool/approval——那些恰恰是断线后最需要恢复的。
func (s *Server) handleSessionHistory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID string `json:"sessionId"`
		// ID 是契约快照里的名字（H9）。两个都收：只认一个就是静默 404。
		ID           string `json:"id"`
		SinceSeq     int    `json:"sinceSeq"`
		Limit        int    `json:"limit"`
		JournalEpoch int64  `json:"journalEpoch"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)

	// 有 journal 时以它为准：WS 推的和这里补的是同一个序号域。
	if s.opt.Journals != nil {
		sid := fillID(in.SessionID, in.ID)
		if sid == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("缺参数：要 id 或 sessionId"))
			return
		}
		j := s.opt.Journals.Get(sid)
		// 客户端带着一个不认识的老世代，或水位比当前还大：说明两边对不上，
		// 继续"按 seq 补"只会补出错位的数据。明确告诉它重建基线。
		if (in.JournalEpoch != 0 && in.JournalEpoch != j.Epoch()) || in.SinceSeq > j.LastSeq() {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]any{
					"code":    "stale_epoch",
					"message": "journal 世代或水位对不上，需要重新 load 基线",
					"hint":    "丢弃旧水位，重新 session.load 拿 journalEpoch/lastSeq",
				},
				"journalEpoch": j.Epoch(),
				"lastSeq":      j.LastSeq(),
			})
			return
		}
		frames, last, epoch := j.Since(in.SinceSeq, in.Limit)
		writeJSON(w, http.StatusOK, HistoryResult{
			Frames:       frames,
			LastSeq:      last,
			JournalEpoch: epoch,
		})
		return
	}

	if s.opt.History == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("not implemented"))
		return
	}
	pg, err := s.opt.History(fillID(in.SessionID, in.ID), in.SinceSeq, in.Limit)
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

// errorBody 造一个契约快照里的错误信封：
//
//	{"error":{"code":..,"message":..,"hint":..,"retryable":..}}
//
// 为什么不是字符串：字符串错误让照快照写的客户端**连 JSON 都解不动**，
// 真正的原因（401 / 404）被吞成"响应不是合法 JSON"（H8，2026-11-03）。
// alreadyEnveloped 判断这个值是不是已经带了信封（防止二次包装）。
func alreadyEnveloped(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if _, has := m["error"]; has {
		return true
	}
	_, has := m["result"]
	return has
}

// errorBody 造一个契约里的错误信封：
//
//	{"error":{"code":…,"message":…,"hint":…,"retryable":…}}
//
// hint 是必需的：客户端要知道**下一步怎么办**，而这正是 DSH 错误契约的意义。
func errorBody(msg string) map[string]any {
	code := errorCode(msg)
	return map[string]any{"error": map[string]any{
		"code":      code,
		"message":   msg,
		"hint":      errorHint(code),
		"retryable": code == "internal",
	}}
}

// errorHint 给每类错误一句"下一步"。没有就别编，返回空串即可。
func errorHint(code string) string {
	switch code {
	case "unauthorized":
		return "先 GET /connect?token=… 换 cookie，再调这个接口"
	case "not_implemented":
		return "宿主这一侧还没接线，换一个 method 或等它实现"
	case "no_such_session":
		return "先 session.list 拿 id；确认 --home 指向的目录里有这个会话"
	case "no_such_request":
		return "这个审批请求已经不在了（已应答或已超时）；等下一次 approval/request"
	case "bad_option":
		return "optionId 必须是 allow_once / allow_always / reject_once 之一"
	case "bad_request":
		return "检查入参名字（id/sessionId 都认）与类型"
	case "encode_failure":
		return "这是宿主内部序列化出错，不是你参数写错了；把这一步操作和 dshd 的 stderr 报告出来"
	case "internal":
		return "可以直接重试；反复失败就看 dshd 的 stderr"
	}
	return ""
}

// errorCode 把旧的中文错误串映射成稳定的机器码。
//
// 映射表就是契约：加新错误时**先加码再加消息**，否则客户端只能靠猜中文。
func errorCode(msg string) string {
	switch msg {
	case "unauthorized":
		return "unauthorized"
	case "not implemented":
		return "not_implemented"
	case "没有这个会话":
		return "no_such_session"
	}
	// 这两个是审批（H13）的稳定错误码，调用方按码判断，不按中文猜。
	if strings.Contains(msg, "序列化失败") {
		return "encode_failure"
	}
	if msg == "no_such_request" || msg == "bad_option" {
		return msg
	}
	if strings.Contains(msg, "未接线") {
		return "not_implemented"
	}
	if strings.Contains(msg, "参数") {
		return "bad_request"
	}
	return "error"
}

// writeJSON 统一套信封：成功 {"result":…}、失败 {"error":{…}}。
//
// 只在 /api 的 handler 里用，所以可以在这里一把收口——
// 逐个 handler 改的话，总有一个会漏，而漏掉的那个就是"界面看到 0 条"。
func writeJSON(w http.ResponseWriter, status int, v any) {
	// **任何类型**都要套信封，不只 map。
	//
	// 原来只包 map[string]any，于是 LoadResult / PromptResult / []SessionSummary
	// 这些 struct/切片一律裸返回——照契约读 `result` 的客户端拿到空。
	// （2026-11-03 复核把 H8 打回 🟡，就是这一条。）
	if !alreadyEnveloped(v) {
		v = map[string]any{"result": v}
	}
	body, err := json.Marshal(v)
	if err != nil {
		// 这里的 payload 全是自家结构，真出错也只剩一个兜底正文。
		//
		// 但它仍然是**一个错误出口**，必须和别的出口同形状（四字段）。
		// 客户端只有一个解析器：少一个字段就要特判，而特判分支没人会为它写测试。
		fallback, _ := json.Marshal(errorBody("响应序列化失败"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(append(fallback, '\n'))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// statusRecorder 记住实际写出去的 HTTP 状态码。
//
// 为什么需要：`/api` 的日志原来只有方法名，看不出这一调用是成功还是 404/501。
// 界面联调时"它调了但报错"和"它调成功了"在日志上必须能分开。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
