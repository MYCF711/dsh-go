package transport

import "sync"

// ActionCardsViewSource 返回一个会话的官方 actionCards projection 值。
//
// 形状与官方 actionCardProjection.wire.viewSchema 一致：
//
//	{"proposals": {callId: {callId,turn,action,title,brief,details}},
//	 "decisions": {callId: {callId,action,phase,data?}}}
type ActionCardsViewSource func(sessionID string) (map[string]any, error)

var officialActionCardsViewMount = struct {
	mu sync.RWMutex
	m  map[*Server]ActionCardsViewSource
}{m: map[*Server]ActionCardsViewSource{}}

// AttachActionCardsView 给这个 Server 挂上真实 actionCards projection。
func (s *Server) AttachActionCardsView(source ActionCardsViewSource) {
	if s == nil {
		return
	}
	officialActionCardsViewMount.mu.Lock()
	defer officialActionCardsViewMount.mu.Unlock()
	officialActionCardsViewMount.m[s] = source
}

// DetachActionCardsView 摘掉投影源（测试收尾）。
func (s *Server) DetachActionCardsView() {
	if s == nil {
		return
	}
	officialActionCardsViewMount.mu.Lock()
	defer officialActionCardsViewMount.mu.Unlock()
	delete(officialActionCardsViewMount.m, s)
}

// officialActionCardsView 给 session.projections handler 用；第二个返回值
// 表示"宿主是否提供了投影源"，false 时 handler 不应写出该键。
func (s *Server) officialActionCardsView(sessionID string) (map[string]any, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	officialActionCardsViewMount.mu.RLock()
	source, ok := officialActionCardsViewMount.m[s]
	officialActionCardsViewMount.mu.RUnlock()
	if !ok || source == nil {
		return nil, false, nil
	}
	view, err := source(sessionID)
	return view, true, err
}
