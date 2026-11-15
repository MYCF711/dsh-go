package transport

// 官方 base 插件 #23 `authorization` 的 **wire 面**（宿主新增方法组）。
//
// 为什么需要这一层：`internal/authorization` 的注册表是**进程内**服务面
// （插件/模块注册 flow、宿主跑尝试）。但本仓的"surface"是**进程外**的 GUI/CLI：
// 用户在界面里点"登录某个 provider"，请求从 HTTP 进来，notice/prompt 必须回到
// **发起它的那个连接**。官方没有 RPC 是因为官方 surface 在同一个进程里；
// 本仓必须有，否则这个 seam 在产品里等于不存在（注册表有实现、没有调用者）。
//
// 方法名按本仓既有新增约定（`<namespace>.<method>`），命名与官方服务面逐字对齐：
//
//	authorization.list()                       → {entries:[{key,label,methods,inFlight}]}
//	authorization.describe({key})              → entry | null（官方 describe 返回 undefined）
//	authorization.begin({key,method?,...})     → {status:'authorized'|'cancelled'}
//	authorization.cancel({key})                → {cancelled:bool}
//
// 四条都是**新增**（官方 99 个 base 插件里 authorization 不是方法组），所以：
//   - 不改任何既有方法的名字/形状；
//   - 未接线时回结构化 501 + 点名缺什么（不是空清单假成功）。
//
// ## notice/prompt 怎么回到"发起它的那个 surface"
//
// `begin` 是一个**长的**请求：它在 flow 跑完之前不返回。期间 flow 需要
// notify/prompt，这些事件通过**同一条 HTTP 响应**以增量 JSON 行（NDJSON）推回：
//
//	{"type":"notice","notice":{"message":"...","url":"...","code":"..."}}
//	{"type":"prompt","id":"p1","prompt":{"kind":"select","message":"...","options":[...]}}
//	{"type":"prompt_answer","id":"p1"}       ← 服务端确认收到答案（可选）
//	{"type":"outcome","outcome":{"status":"authorized"}}
//
// 答案用**第二个请求** `authorization.answer({key,id,value})` 送回来。
// 这样：
//   - "只发给发起方"是**物理的**：事件只写那条 `begin` 的响应体，没有别的通道；
//   - 无头调用方（没有 GUI）可以直接 `begin` 并在收到 prompt 时回 `answer`，
//     或者干脆不发 answer（超时即 cancelled）。
//
// 这是与官方的显式差异（官方 in-process 回调，本仓 over-wire 回调），写在上面。
//
// ## 为什么 begin 用 NDJSON 而不是先返回 attemptId 再轮询
//
// 轮询会让"prompt 属于哪个页面"重新变成一个注册表问题（正是官方要避免的）。
// 一条连接 = 一个 surface，连接断了 attempt 就 cancelled —— 语义最省、最不会串号。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"dshgo/internal/authorization"
)

// 宿主新增的 authorization 方法名。
const (
	MethodAuthorizationList     Method = "authorization.list"
	MethodAuthorizationDescribe Method = "authorization.describe"
	MethodAuthorizationBegin    Method = "authorization.begin"
	MethodAuthorizationCancel   Method = "authorization.cancel"
	MethodAuthorizationAnswer   Method = "authorization.answer"
)

// authorizationMethods 是本组方法的全集（禁止被别的 handler 抢路由）。
func authorizationMethods() []Method {
	return []Method{
		MethodAuthorizationList, MethodAuthorizationDescribe, MethodAuthorizationBegin,
		MethodAuthorizationCancel, MethodAuthorizationAnswer,
	}
}

// IsAuthorizationMethod 判断是不是本组方法。
func IsAuthorizationMethod(name string) bool {
	for _, method := range authorizationMethods() {
		if Method(name) == method {
			return true
		}
	}
	return false
}

// authorizationMount 把注册表挂到具体 Server（按 *Server 索引，不用全局单例）。
var authorizationMount = struct {
	mu sync.Mutex
	m  map[*Server]*authorizationRegistryHandle
}{m: map[*Server]*authorizationRegistryHandle{}}

// authorizationRegistryHandle 是注册表 + 在飞尝试的 wire 状态。
type authorizationRegistryHandle struct {
	registry *authorization.Registry

	mu      sync.Mutex
	pending map[string]*authorizationAttemptWire // key → 在飞尝试的答案通道
}

// authorizationAttemptWire 是一条在飞尝试的 surface 状态。
type authorizationAttemptWire struct {
	// answers 把 authorization.answer 的答复送回 flow 的 Ask。
	answers chan authorizationAnswer
	// closed 在连接断开/取消时关闭，让待答的 Ask 以 cancelled 结束。
	closed chan struct{}
	once   sync.Once
}

type authorizationAnswer struct {
	id       string
	value    string
	declined bool
}

func (w *authorizationAttemptWire) close() {
	w.once.Do(func() { close(w.closed) })
}

// AttachAuthorizationRegistry 挂上 flow 注册表（官方 #23 的宿主真值）。
func (s *Server) AttachAuthorizationRegistry(registry *authorization.Registry) {
	if s == nil {
		return
	}
	authorizationMount.mu.Lock()
	defer authorizationMount.mu.Unlock()
	if registry == nil {
		delete(authorizationMount.m, s)
		return
	}
	authorizationMount.m[s] = &authorizationRegistryHandle{
		registry: registry,
		pending:  map[string]*authorizationAttemptWire{},
	}
}

// DetachAuthorizationRegistry 摘掉（server 关闭/测试用）。
func (s *Server) DetachAuthorizationRegistry() {
	if s == nil {
		return
	}
	authorizationMount.mu.Lock()
	delete(authorizationMount.m, s)
	authorizationMount.mu.Unlock()
}

func (s *Server) authorizationHandle() (*authorizationRegistryHandle, bool) {
	authorizationMount.mu.Lock()
	defer authorizationMount.mu.Unlock()
	handle, ok := authorizationMount.m[s]
	return handle, ok
}

// ServeOfficialAuthorizationAPI 是四条方法的入口。
func (s *Server) ServeOfficialAuthorizationAPI(w http.ResponseWriter, r *http.Request) {
	name := Method(strings.TrimPrefix(r.URL.Path, "/api/"))
	if !IsAuthorizationMethod(string(name)) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", "不是 authorization 方法"))
		return
	}
	handle, ok := s.authorizationHandle()
	if !ok {
		writeOfficialError(w, officialNotImplemented(
			"%s 未接线：宿主没有 AttachAuthorizationRegistry（接线：srv.AttachAuthorizationRegistry(newAuthorizationRegistry(provider))）", name))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	switch name {
	case MethodAuthorizationList:
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": handle.registry.List()})
	case MethodAuthorizationDescribe:
		key, err := authorizationStringParam(params, "key")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if entry, found := handle.registry.Describe(key); found {
			writeJSON(w, http.StatusOK, map[string]any{"entry": entry})
			return
		}
		// 官方 describe 对没有 flow 的 key 回 undefined；本仓回 entry=null
		// （不是错误：调用方要能区分"没有 flow"与"这次调用坏了"）。
		writeJSON(w, http.StatusOK, map[string]any{"entry": nil})
	case MethodAuthorizationCancel:
		key, err := authorizationStringParam(params, "key")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": handle.registry.Cancel(key)})
	case MethodAuthorizationAnswer:
		handle.serveAnswer(w, params)
	case MethodAuthorizationBegin:
		handle.serveBegin(w, r, params)
	}
}

// serveAnswer 把一条答复送给在飞尝试的 Surface.Prompt。
func (h *authorizationRegistryHandle) serveAnswer(w http.ResponseWriter, params *officialParams) {
	key, err := authorizationStringParam(params, "key")
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	id, err := authorizationStringParam(params, "id")
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	value, hasValue, err := params.takeStr("value")
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	declined, _, err := params.boolean("declined")
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	if err := params.finish(); err != nil {
		writeOfficialError(w, err)
		return
	}
	h.mu.Lock()
	wire, ok := h.pending[strings.TrimSpace(key)]
	h.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusConflict, errorBodyFor("no_attempt",
			"这条 key 没有在飞的授权尝试：先 authorization.begin（或它已经结束）"))
		return
	}
	// "没给 value" 与 "declined=true" 都是**人拒绝**（官方：退出一条问题 = 拒绝）。
	// 给了 value（哪怕是空串）就是真答复——把两者混成一种会让"空答案"变成拒绝，
	// 而空答案在某些 prompt 上是合法的。
	answer := authorizationAnswer{id: id, value: value}
	if declined || !hasValue {
		answer.declined = true
	}
	select {
	case wire.answers <- answer:
		writeJSON(w, http.StatusOK, map[string]any{"accepted": true})
	case <-wire.closed:
		writeJSON(w, http.StatusConflict, errorBodyFor("attempt_closed", "这次授权已经结束，答复没有接收方"))
	}
}

// serveBegin 跑一次尝试，用 NDJSON 把 notice/prompt/outcome 推回同一条响应。
func (h *authorizationRegistryHandle) serveBegin(w http.ResponseWriter, r *http.Request, params *officialParams) {
	key, err := authorizationStringParam(params, "key")
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	method, _, _ := params.takeStr("method")
	if err := params.finish(); err != nil {
		writeOfficialError(w, err)
		return
	}
	key = strings.TrimSpace(key)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOfficialError(w, officialNotImplemented("authorization.begin 需要可流式响应（http.Flusher）：这个 listener 不支持"))
		return
	}
	wire := &authorizationAttemptWire{
		answers: make(chan authorizationAnswer, 8),
		closed:  make(chan struct{}),
	}
	h.mu.Lock()
	if _, busy := h.pending[key]; busy {
		h.mu.Unlock()
		writeJSON(w, http.StatusConflict, errorBodyFor("already_in_flight", "这条 key 已经有一次授权在跑"))
		return
	}
	h.pending[key] = wire
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.pending[key] == wire {
			delete(h.pending, key)
		}
		h.mu.Unlock()
		wire.close()
	}()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// 立刻把响应头推出去：客户端（以及本包的测试）靠"头到了"才开始读流。
	// 不 flush 的话，handler 在 flow 里阻塞时客户端连 Do() 都返回不了——
	// 那是"流式"最容易漏的一步。
	flusher.Flush()
	enc := json.NewEncoder(w)
	writeEvent := func(event map[string]any) bool {
		if err := enc.Encode(event); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	interaction := &wireInteraction{
		key:    key,
		wire:   wire,
		cancel: r.Context(),
		write:  writeEvent,
	}
	// 连接断开 = 撤销（官方："a withdrawn attempt settles immediately even when
	// the flow never reacts to its signal"）。Begin 内部会把终态结算成 cancelled。
	outcome, beginErr := h.registry.Begin(r.Context(), key, method, interaction)
	if beginErr != nil {
		code := authorization.CodeOf(beginErr)
		if code == "" {
			code = "authorization_failed"
		}
		_ = writeEvent(map[string]any{"type": "error", "code": code, "message": beginErr.Error()})
		return
	}
	_ = writeEvent(map[string]any{"type": "outcome", "outcome": outcome})
}

// wireInteraction 是"发起这次 begin 的那个 surface"在 wire 上的实现。
//
// notice 直接写响应；prompt 写一条 prompt 事件，然后**等** authorization.answer。
// 连接断了（r.Context() done）或尝试被 cancel 时，Ask 立刻以 ErrDeclined 结束，
// 于是本次尝试结算成 cancelled（官方："A human's 'no' is an outcome"）。
type wireInteraction struct {
	key    string
	wire   *authorizationAttemptWire
	cancel context.Context
	write  func(map[string]any) bool
}

func (i *wireInteraction) Notify(ctx context.Context, notice authorization.Notice) error {
	if !i.write(map[string]any{"type": "notice", "notice": notice}) {
		return fmt.Errorf("authorization: 这条连接的响应写不进去（surface 已经关了）")
	}
	return nil
}

func (i *wireInteraction) Ask(ctx context.Context, prompt authorization.Prompt) (string, error) {
	// prompt id 用序号而不是随机值：同一条连接里单调，人看到的顺序即 id 顺序。
	id := fmt.Sprintf("p%d", nextPromptID())
	if !i.write(map[string]any{"type": "prompt", "id": id, "prompt": prompt}) {
		return "", fmt.Errorf("authorization: 这条连接的响应写不进去（surface 已经关了）")
	}
	select {
	case answer := <-i.wire.answers:
		if answer.id != "" && answer.id != id {
			// 答的是别的问题：这不该发生（一条连接一次只有一个待答 prompt），
			// 但拒绝比"张冠李戴"安全。
			return "", fmt.Errorf("authorization: 答复 id %q 与当前问题 %q 不匹配", answer.id, id)
		}
		if answer.declined {
			return "", authorization.ErrDeclined
		}
		return answer.value, nil
	case <-i.wire.closed:
		return "", authorization.ErrDeclined
	case <-i.cancel.Done():
		return "", authorization.ErrDeclined
	case <-ctx.Done():
		return "", authorization.ErrDeclined
	}
}

// promptSequence 给同一进程里的 prompt 分配稳定可读 id（只用于 wire 对账）。
var promptSequence int64

func nextPromptID() int64 { return atomic.AddInt64(&promptSequence, 1) }

// authorizationStringParam 取一个必填的字符串参数。
func authorizationStringParam(params *officialParams, name string) (string, error) {
	value, ok, err := params.takeStr(name)
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(value) == "" {
		return "", officialBadRequest("缺参数 %s", name)
	}
	return strings.TrimSpace(value), nil
}
