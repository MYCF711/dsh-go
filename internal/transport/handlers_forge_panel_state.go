package transport

// 官方 `forgePanel.{follow,refresh,track,dismiss}` 的**真实现**接线层。
//
// # 契约（只读镜像）
//
//	%TEMP%/official-api-src/dsh/node_modules/@deepseek-ai/dsh-api-forge/lib/typert.host.js
//	forgePanel/track    params {sessionId, url}                → {binding}
//	forgePanel/dismiss  params {sessionId, url}                → {binding}
//	forgePanel/refresh  params {sessionId, url?}                → {bindings[]}
//	forgePanel/follow   mode:'stream'  ForgePanelStateFrame（union）
//	  {type:'baseline', value: ForgePanelBaseline}
//	  {type:'upsert',   binding: ForgePanelBinding}
//	  {type:'remove',   sessionId, url}
//
// # 为什么本文件只做"接线层"
//
// 共享分派（`server.go` / `official_stream_face.go` / `cmd/dshd/serve.go` 的
// Attach）由 integrator 串行接线（见 WIRING）。本文件提供：
//
//	OfficialForgePanelAPI      宿主实现函数表（nil = 未接线 → 501）
//	ServeOfficialForgePanel     unary 三个方法的纯函数入口
//	OpenOfficialForgePanelFollowStream + ServeOfficialForgePanelFollowHTTP
//	                           follow 的帧机与 HTTP NDJSON 出口
//
// 两条面（HTTP unary / mux 流）共用**同一份**绑定存储，不存在"HTTP 对了 mux 错了"。
//
// # 不复用 workspace.follow 的轮询
//
// forge 绑定变化频率远低于 workspace registry，且 track/dismiss 是**本进程内**
// 的写操作——所以这里用"写操作后推进版本号 + 帧机等版本变化"的唤醒方式，
// 没有变化就阻塞在 ctx 上，不空转。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// OfficialForgePanelBinding 是 wire 上的绑定形状。
type OfficialForgePanelBinding struct {
	SessionID   string `json:"sessionId"`
	URL         string `json:"url"`
	Provider    string `json:"provider,omitempty"`
	Tracked     bool   `json:"tracked"`
	State       string `json:"state,omitempty"`
	Title       string `json:"title,omitempty"`
	HeadSHA     string `json:"headSha,omitempty"`
	TrackedAt   string `json:"trackedAt,omitempty"`
	DismissedAt string `json:"dismissedAt,omitempty"`
	RefreshedAt string `json:"refreshedAt,omitempty"`
}

// OfficialForgePanelSnapshot 是一次绑定快照（版本号用于帧机唤醒）。
type OfficialForgePanelSnapshot struct {
	Version  int64
	Bindings []OfficialForgePanelBinding
}

// OfficialForgePanelAPI 是宿主实现函数表。任一为 nil 表示"这条能力没接线"，
// 调用方必须回 501 —— 不允许静默成功。
type OfficialForgePanelAPI struct {
	Track   func(sessionID, url string) (OfficialForgePanelBinding, error)
	Dismiss func(sessionID, url string) (OfficialForgePanelBinding, error)
	Refresh func(ctx context.Context, sessionID, url string) ([]OfficialForgePanelBinding, error)
	// Snapshot 给 follow 帧机取快照；Version 变化即"有新状态"。
	Snapshot func() (OfficialForgePanelSnapshot, error)
	// Wait 阻塞到版本变化或 ctx 取消（follow 的唤醒点）。nil 时帧机会用
	// 100ms 轮询兜底（仍是真的读存储，不是假帧）。
	Wait func(ctx context.Context, since int64) error
}

// forgePanelMount 把实现挂到某个 Server 上（两个 Server 各挂各的）。
var forgePanelMount = struct {
	mu     sync.RWMutex
	byServ map[*Server]OfficialForgePanelAPI
}{byServ: map[*Server]OfficialForgePanelAPI{}}

// AttachOfficialForgePanelAPI 给这个 Server 挂上真实实现。
func (s *Server) AttachOfficialForgePanelAPI(api OfficialForgePanelAPI) {
	if s == nil {
		return
	}
	forgePanelMount.mu.Lock()
	defer forgePanelMount.mu.Unlock()
	forgePanelMount.byServ[s] = api
}

// DetachOfficialForgePanelAPI 摘掉（测试清理用）。
func (s *Server) DetachOfficialForgePanelAPI() {
	if s == nil {
		return
	}
	forgePanelMount.mu.Lock()
	defer forgePanelMount.mu.Unlock()
	delete(forgePanelMount.byServ, s)
}

func (s *Server) officialForgePanelAPI() OfficialForgePanelAPI {
	if s == nil {
		return OfficialForgePanelAPI{}
	}
	forgePanelMount.mu.RLock()
	defer forgePanelMount.mu.RUnlock()
	return forgePanelMount.byServ[s]
}

// IsOfficialForgePanelStateMethod 报告是不是本文件实现的 4 个方法。
func IsOfficialForgePanelStateMethod(name string) bool {
	switch Method(name) {
	case MethodForgePanelFollow, MethodForgePanelRefresh, MethodForgePanelTrack, MethodForgePanelDismiss:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// unary 三个方法
// ---------------------------------------------------------------------------

func officialForgePanelBindingValue(b OfficialForgePanelBinding) map[string]any {
	return map[string]any{"binding": b}
}

func officialForgePanelTrack(api OfficialForgePanelAPI, p *officialParams) (any, error) {
	sessionID, url, err := officialForgePanelRef(p)
	if err != nil {
		return nil, err
	}
	if api.Track == nil {
		return nil, officialNotImplemented("forgePanel.track 未接线：宿主没有 forgeSession 绑定存储")
	}
	b, err := api.Track(sessionID, url)
	if err != nil {
		return nil, officialForgePanelFault(err)
	}
	return officialForgePanelBindingValue(b), nil
}

func officialForgePanelDismiss(api OfficialForgePanelAPI, p *officialParams) (any, error) {
	sessionID, url, err := officialForgePanelRef(p)
	if err != nil {
		return nil, err
	}
	if api.Dismiss == nil {
		return nil, officialNotImplemented("forgePanel.dismiss 未接线：宿主没有 forgeSession 绑定存储")
	}
	b, err := api.Dismiss(sessionID, url)
	if err != nil {
		return nil, officialForgePanelFault(err)
	}
	return officialForgePanelBindingValue(b), nil
}

func officialForgePanelRefresh(api OfficialForgePanelAPI, p *officialParams) (any, error) {
	sessionID, _, err := p.strAlias("sessionId", "sessionID", "id")
	if err != nil {
		return nil, err
	}
	// url 可选：不给就刷新该会话的全部绑定（官方 refresh 的语义）。
	url := ""
	if p.has("url") {
		u, err := p.str("url")
		if err != nil {
			return nil, err
		}
		url = u
	} else if p.has("changeRequestUrl") {
		u, err := p.str("changeRequestUrl")
		if err != nil {
			return nil, err
		}
		url = u
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	if api.Refresh == nil {
		return nil, officialNotImplemented("forgePanel.refresh 未接线：宿主没有 forgeSession 绑定存储")
	}
	bindings, err := api.Refresh(context.Background(), sessionID, url)
	if err != nil {
		return nil, officialForgePanelFault(err)
	}
	if bindings == nil {
		bindings = []OfficialForgePanelBinding{}
	}
	return map[string]any{"bindings": bindings}, nil
}

func officialForgePanelRef(p *officialParams) (string, string, error) {
	sessionID, _, err := p.strAlias("sessionId", "sessionID", "id")
	if err != nil {
		return "", "", err
	}
	rawURL, _, err := p.strAlias("url", "changeRequestUrl")
	if err != nil {
		return "", "", err
	}
	if err := p.finish(); err != nil {
		return "", "", err
	}
	return sessionID, rawURL, nil
}

// officialForgePanelFault 把存储错误翻成官方四字段。
func officialForgePanelFault(err error) error {
	if err == nil {
		return nil
	}
	var typed *officialError
	if errors.As(err, &typed) {
		return err
	}
	switch {
	case errors.Is(err, ErrForgeBindingNotFound):
		return &officialError{status: http.StatusNotFound, code: "not_found", msg: err.Error()}
	case errors.Is(err, ErrForgeInvalidURL), errors.Is(err, ErrForgeBadParam):
		return &officialError{status: http.StatusBadRequest, code: "bad_request", msg: err.Error()}
	case errors.Is(err, ErrForgeNoProvider):
		return &officialError{status: http.StatusNotImplemented, code: "not_implemented", msg: err.Error()}
	}
	return &officialError{status: http.StatusInternalServerError, code: "internal", msg: err.Error()}
}

// 宿主侧用 errors.Is 判断的哨兵错误。
//
// 为什么在这里定义而不是 import internal/forge：R7 的方向是"宿主 → Transport"，
// Transport 不认识领域包。宿主把自己的错误包成这些哨兵即可，两边都不越界。
var (
	// ErrForgeBindingNotFound 表示这条 (session,url) 绑定不存在。
	ErrForgeBindingNotFound = errors.New("forge binding not found")
	// ErrForgeInvalidURL 表示 url 不是合法 code host URL。
	ErrForgeInvalidURL = errors.New("forge invalid url")
	// ErrForgeBadParam 表示参数本身不合法。
	ErrForgeBadParam = errors.New("forge bad param")
	// ErrForgeNoProvider 表示宿主没配 provider。
	ErrForgeNoProvider = errors.New("forge no provider")
)

// ServeOfficialForgePanel 是 unary 三个方法的纯函数入口。
func ServeOfficialForgePanel(w http.ResponseWriter, r *http.Request, name string, api OfficialForgePanelAPI) {
	if !IsOfficialForgePanelStateMethod(name) || Method(name) == MethodForgePanelFollow {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			fmt.Sprintf("不是 forgePanel 的 unary 方法：%q", name)))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	var result any
	switch Method(name) {
	case MethodForgePanelTrack:
		result, err = officialForgePanelTrack(api, params)
	case MethodForgePanelDismiss:
		result, err = officialForgePanelDismiss(api, params)
	case MethodForgePanelRefresh:
		result, err = officialForgePanelRefresh(api, params)
	}
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
// follow 帧机
// ---------------------------------------------------------------------------

// officialForgePanelFollowStream 是 forgePanel.follow 的快照差异帧机。
//
// 首帧 baseline，之后 upsert / remove。差异顺序固定（remove → upsert），
// 不依赖 map 遍历顺序。
type officialForgePanelFollowStream struct {
	source  OfficialForgePanelAPI
	opened  bool
	version int64
	known   map[string]OfficialForgePanelBinding
	pending []json.RawMessage
}

var _ OfficialFrameStream = (*officialForgePanelFollowStream)(nil)

func forgePanelKey(b OfficialForgePanelBinding) string { return b.SessionID + "\x00" + b.URL }

func (s *officialForgePanelFollowStream) Close() {}

func (s *officialForgePanelFollowStream) Next(ctx context.Context) (json.RawMessage, bool, *OfficialStreamFault) {
	for {
		if len(s.pending) > 0 {
			frame := s.pending[0]
			s.pending = s.pending[1:]
			return frame, false, nil
		}
		if s.source.Snapshot == nil {
			return nil, false, &OfficialStreamFault{
				Code: "not_implemented", Status: http.StatusNotImplemented,
				Message: "forgePanel.follow 未接线：宿主没有 forgeSession 绑定存储",
			}
		}
		snap, err := s.source.Snapshot()
		if err != nil {
			return nil, false, officialStreamFaultOf(MethodForgePanelFollow, err)
		}
		if !s.opened {
			s.opened = true
			s.version = snap.Version
			s.record(snap)
			raw, merr := json.Marshal(map[string]any{
				"type": "baseline", "value": map[string]any{"bindings": snap.Bindings},
			})
			if merr != nil {
				return nil, false, &OfficialStreamFault{Code: "internal", Status: 500, Message: merr.Error()}
			}
			return raw, false, nil
		}
		frames := s.diff(snap)
		s.version = snap.Version
		s.record(snap)
		if len(frames) > 0 {
			s.pending = frames[1:]
			return frames[0], false, nil
		}
		// 没有变化：等一次唤醒（或 ctx 取消）。
		if s.source.Wait != nil {
			if werr := s.source.Wait(ctx, s.version); werr != nil {
				if errors.Is(werr, context.Canceled) || errors.Is(werr, context.DeadlineExceeded) {
					return nil, true, nil
				}
				return nil, false, officialStreamFaultOf(MethodForgePanelFollow, werr)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, true, nil
		case <-time.After(officialStreamPollInterval):
		}
	}
}

func (s *officialForgePanelFollowStream) record(snap OfficialForgePanelSnapshot) {
	s.known = make(map[string]OfficialForgePanelBinding, len(snap.Bindings))
	for _, b := range snap.Bindings {
		s.known[forgePanelKey(b)] = b
	}
}

func (s *officialForgePanelFollowStream) diff(snap OfficialForgePanelSnapshot) []json.RawMessage {
	next := make(map[string]OfficialForgePanelBinding, len(snap.Bindings))
	for _, b := range snap.Bindings {
		next[forgePanelKey(b)] = b
	}
	var out []json.RawMessage
	// remove 先：先下掉再上新的，客户端不会短暂看到两条同 key。
	for key, old := range s.known {
		if _, ok := next[key]; ok {
			continue
		}
		raw, err := json.Marshal(map[string]any{
			"type": "remove", "sessionId": old.SessionID, "url": old.URL,
		})
		if err != nil {
			continue
		}
		out = append(out, raw)
	}
	for _, b := range snap.Bindings {
		old, ok := s.known[forgePanelKey(b)]
		if ok && old == b {
			continue
		}
		raw, err := json.Marshal(map[string]any{"type": "upsert", "binding": b})
		if err != nil {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// ServeOfficialForgePanelFollowHTTP 是 `forgePanel.follow` 的 HTTP NDJSON 出口。
//
// 头必须**在**第一行之前写：客户端拿到 200 + ndjson 才知道"订阅成功"；
// 未接线/参数错仍在写头之前回四字段（不是 200 之后报错）。
func (s *Server) ServeOfficialForgePanelFollowHTTP(w http.ResponseWriter, r *http.Request) {
	api := s.officialForgePanelAPI()
	if api.Snapshot == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			"forgePanel.follow 未接线：宿主没有 forgeSession 绑定存储"))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	if err := params.finish(); err != nil {
		writeOfficialError(w, err)
		return
	}

	stream := &officialForgePanelFollowStream{source: api}
	defer stream.Close()

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	for {
		frame, done, fault := stream.Next(r.Context())
		if fault != nil {
			// 头已经发出：只能在流里报错（客户端据此重连/降级）。
			raw, _ := json.Marshal(map[string]any{
				"error": map[string]any{"code": fault.Code, "message": fault.Message},
			})
			_, _ = w.Write(append(raw, '\n'))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		if done {
			return
		}
		if len(frame) == 0 {
			continue
		}
		if _, werr := w.Write(append(frame, '\n')); werr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
