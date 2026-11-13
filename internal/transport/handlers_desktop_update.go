// handlers_desktop_update.go —— 官方「更新渠道」的壳服务面（`desktopUpdate.channel.*`）。
//
// 为什么在宿主里做：官方把更新渠道归**壳配置**，数据由 Electron 主进程通过隔离
// preload 桥暴露（`dsh-client-ui-settings-general/src/types.ts:29-74`）。本仓是
// Go 宿主 + Go GUI，没有 Electron 主进程，所以由宿主提供**同形**的壳服务：
// 同样的三件事（get / set / subscribe）、同样的状态形状、同样的失败分类。
//
// 用户 2026-11-13 裁定「官方的都要有，关闭的就默认关闭，打开的就默认打开」——
// 这里实现的是官方真实的 channel 语义（含官方那两档渠道与它的失败码），
// 不是给界面编一个占位值。
//
// 三个方法：
//
//	desktopUpdate.channel.get        -> { channel, packagedChannel, changing, error? }
//	desktopUpdate.channel.set        -> { kind, state, reason? }
//	desktopUpdate.channel.subscribe  -> 流（变更推送；取消时退订）
//
// 宿主没接线（Options.UpdateChannel == nil）时回 **501 not_implemented** 并说清原因，
// 不假装"没有渠道可选"。
package transport

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

const (
	// MethodDesktopUpdateChannelGet 是官方壳桥的 `get()`。
	MethodDesktopUpdateChannelGet Method = "desktopUpdate.channel.get"
	// MethodDesktopUpdateChannelSet 是官方壳桥的 `set(channel)`。
	MethodDesktopUpdateChannelSet Method = "desktopUpdate.channel.set"
	// MethodDesktopUpdateChannelSubscribe 是官方壳桥的 `subscribe(listener)`。
	MethodDesktopUpdateChannelSubscribe Method = "desktopUpdate.channel.subscribe"
)

// OfficialDesktopUpdateChannelMethods 按固定顺序返回壳更新渠道的三个方法。
func OfficialDesktopUpdateChannelMethods() []Method {
	return []Method{MethodDesktopUpdateChannelGet, MethodDesktopUpdateChannelSet, MethodDesktopUpdateChannelSubscribe}
}

// IsOfficialDesktopUpdateChannelMethod 判断一个 /api 路径名是否属于这一族。
func IsOfficialDesktopUpdateChannelMethod(name string) bool {
	switch Method(name) {
	case MethodDesktopUpdateChannelGet, MethodDesktopUpdateChannelSet, MethodDesktopUpdateChannelSubscribe:
		return true
	}
	return false
}

// desktopUpdateChannelMount 把壳渠道服务挂到某个 Server 上。
//
// 与 agentTeam 那边同款：用 map 而不是给 Server 加字段，避免 server.go
// 每加一族方法就动一次结构体。
var desktopUpdateChannelMount = struct {
	mu sync.Mutex
	m  map[*Server]UpdateChannelService
}{m: map[*Server]UpdateChannelService{}}

// AttachDesktopUpdateChannel 挂上壳更新渠道服务（传 nil = 摘掉）。
func (s *Server) AttachDesktopUpdateChannel(service UpdateChannelService) {
	if s == nil {
		return
	}
	desktopUpdateChannelMount.mu.Lock()
	defer desktopUpdateChannelMount.mu.Unlock()
	desktopUpdateChannelMount.m[s] = service
}

// DetachDesktopUpdateChannel 摘掉实现（测试收尾用）。
func (s *Server) DetachDesktopUpdateChannel() {
	if s == nil {
		return
	}
	desktopUpdateChannelMount.mu.Lock()
	defer desktopUpdateChannelMount.mu.Unlock()
	delete(desktopUpdateChannelMount.m, s)
}

// desktopUpdateChannelFor 取某个 Server 挂的服务（没挂返回 nil）。
func (s *Server) desktopUpdateChannelFor() UpdateChannelService {
	desktopUpdateChannelMount.mu.Lock()
	defer desktopUpdateChannelMount.mu.Unlock()
	return desktopUpdateChannelMount.m[s]
}

// ServeOfficialDesktopUpdateChannel 分派这一族的三个方法。
func (s *Server) ServeOfficialDesktopUpdateChannel(w http.ResponseWriter, r *http.Request, name string) {
	service := s.desktopUpdateChannelFor()
	if service == nil {
		// 没接线：如实 501 + 说清缺什么（与 handler 层其它族同一形状）。
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			"desktopUpdate.channel 未接线：宿主启动时没有挂 Options.UpdateChannel（这条能力属于壳侧更新配置）"))
		return
	}
	switch Method(name) {
	case MethodDesktopUpdateChannelGet:
		writeJSON(w, http.StatusOK, map[string]any{"result": service.StateView()})
	case MethodDesktopUpdateChannelSet:
		var in struct {
			Channel string `json:"channel"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request",
				"desktopUpdate.channel.set 参数不是合法 JSON：body 是裸对象，形如 {\"channel\":\"stable\"}"))
			return
		}
		channel := strings.TrimSpace(in.Channel)
		if channel == "" {
			writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request",
				"desktopUpdate.channel.set 缺 channel：只能是 stable 或 nightly"))
			return
		}
		// 官方：失败不伪装成功——`kind` 就是三态（applied / unchanged / failed）。
		// 上层据此渲染，这里**不**把 failed 翻成 4xx：那是"用户选择被拒"，
		// 不是"请求写错了"，官方也是把它放进 result 里。
		writeJSON(w, http.StatusOK, map[string]any{"result": service.WireSet(channel)})
	case MethodDesktopUpdateChannelSubscribe:
		s.serveDesktopUpdateChannelStream(w, r, service)
	}
}

// serveDesktopUpdateChannelStream 是订阅面：建立后推一帧当前状态，
// 之后每次变更推一帧；客户端断开即退订。
//
// 为什么先推一帧：官方 `subscribe` 的消费者拿到的是"当前值 + 后续变更"，
// 界面上来就要能画。只推后续变更会让首帧空着，等于让界面自己去 get 一次。
func (s *Server) serveDesktopUpdateChannelStream(w http.ResponseWriter, r *http.Request, service UpdateChannelService) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", "响应流不可用"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	encoder := json.NewEncoder(w)
	writeFrame := func(state map[string]any) bool {
		if err := encoder.Encode(state); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// 首帧：当前状态。
	if !writeFrame(service.StateView()) {
		return
	}

	// 之后的变更：走一个带缓冲的通道，避免订阅回调直接写 HTTP（回调可能在别的 goroutine）。
	changes := make(chan map[string]any, 8)
	unsubscribe := service.WireSubscribe(func(state map[string]any) {
		select {
		case changes <- state:
		default: // 消费者跟不上就丢中间态：这是**状态**不是**事件流**，只承诺"现在是什么"。
		}
	})
	defer unsubscribe()

	for {
		select {
		case <-r.Context().Done():
			return
		case state := <-changes:
			if !writeFrame(state) {
				return
			}
		}
	}
}
