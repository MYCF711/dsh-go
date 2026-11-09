package transport

// 官方 0.2.2 `productAnalytics.*` 三个方法（0.2.2-rc.1 app.asar 的
// @deepseek-ai/dsh-client-product-analytics/lib/typert.host.js：:356-415）：
//
//	productAnalytics.enabled     parameters: []   result: boolean
//	productAnalytics.report      parameter event: ProductEvent, result: void
//	productAnalytics.watchPolicy parameters: []   mode:'stream', result: boolean
//
// 本文件只做协议与形状翻译；真实管道在 internal/analytics，经
// OfficialAnalyticsAPI 注入。**未接线时 501 点名缺什么**，不返回假 true /
// 假 void——那会让官方界面把"宿主没做"显示成"策略已生效"。

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

const (
	// MethodProductAnalyticsEnabled 是官方 `productAnalytics.enabled`。
	MethodProductAnalyticsEnabled Method = "productAnalytics.enabled"
	// MethodProductAnalyticsWatchPolicy 是官方 `productAnalytics.watchPolicy`
	// （mode:'stream'，结果是 boolean）。
	MethodProductAnalyticsWatchPolicy Method = "productAnalytics.watchPolicy"
)

// OfficialAnalyticsAPI 是宿主侧的真实产品分析实现。
//
// 三个字段都可为 nil；为 nil 表示当前宿主没有这项能力，调用回四字段 501。
// Report 的错误会原样成为四字段错误（真实管道错误不被吞）。
type OfficialAnalyticsAPI struct {
	Enabled     func(ctx context.Context) (bool, error)
	Report      func(ctx context.Context, event OfficialProductEvent) error
	WatchPolicy func(ctx context.Context) (<-chan bool, error)
}

var officialAnalyticsMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialAnalyticsAPI
}{m: map[*Server]OfficialAnalyticsAPI{}}

// AttachOfficialAnalyticsAPI 给这个 Server 挂上官方产品分析实现。
// 必须在服务开始接受 /api 请求之前调用（serve 的装配阶段）。
func (s *Server) AttachOfficialAnalyticsAPI(api OfficialAnalyticsAPI) {
	if s == nil {
		return
	}
	officialAnalyticsMount.mu.Lock()
	defer officialAnalyticsMount.mu.Unlock()
	officialAnalyticsMount.m[s] = api
}

// DetachOfficialAnalyticsAPI 释放某个 Server 的实现（测试用）。
func (s *Server) DetachOfficialAnalyticsAPI() {
	if s == nil {
		return
	}
	officialAnalyticsMount.mu.Lock()
	defer officialAnalyticsMount.mu.Unlock()
	delete(officialAnalyticsMount.m, s)
}

func (s *Server) officialAnalyticsAPI() (OfficialAnalyticsAPI, bool) {
	if s == nil {
		return OfficialAnalyticsAPI{}, false
	}
	officialAnalyticsMount.mu.RLock()
	defer officialAnalyticsMount.mu.RUnlock()
	api, ok := officialAnalyticsMount.m[s]
	return api, ok
}

// OfficialAnalyticsMethods 返回本文件认领的三个方法（测试钉住集合）。
func OfficialAnalyticsMethods() []Method {
	return []Method{
		MethodProductAnalyticsEnabled,
		MethodProductAnalyticsReport,
		MethodProductAnalyticsWatchPolicy,
	}
}

// IsOfficialAnalyticsMethod 判断一个 /api 路径名是否属于产品分析三个方法。
func IsOfficialAnalyticsMethod(name string) bool {
	switch Method(name) {
	case MethodProductAnalyticsEnabled, MethodProductAnalyticsReport, MethodProductAnalyticsWatchPolicy:
		return true
	}
	return false
}

// serveOfficialAnalyticsAPI 处理三个方法；watchPolicy 走既有 NDJSON 流面。
func (s *Server) serveOfficialAnalyticsAPI(w http.ResponseWriter, r *http.Request, name string) {
	api, ok := s.officialAnalyticsAPI()
	if !ok {
		writeOfficialError(w, officialNotImplemented(
			"%s 未接线：宿主没有 AttachOfficialAnalyticsAPI", name))
		return
	}
	switch Method(name) {
	case MethodProductAnalyticsEnabled:
		params, err := decodeOfficialParams(r)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.Enabled == nil {
			writeOfficialError(w, officialNotImplemented(
				"productAnalytics.enabled 未接线：宿主没有 analytics 策略"))
			return
		}
		enabled, err := api.Enabled(r.Context())
		if err != nil {
			writeOfficialError(w, officialInternal("productAnalytics.enabled 失败：%v", err))
			return
		}
		writeJSON(w, http.StatusOK, enabled)

	case MethodProductAnalyticsReport:
		if api.Report == nil {
			writeOfficialError(w, officialNotImplemented(
				"productAnalytics.report 未接线：宿主没有 analytics 管道"))
			return
		}
		params, err := decodeOfficialParams(r)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		// 复用 account 文件里已经对齐官方 ProductEvent 的解析/错误文案；
		// 只把回调换成 analytics 的真实实现。
		result, err := officialProductAnalyticsReport(r.Context(), OfficialAccountAPI{
			ProductAnalyticsReport: api.Report,
		}, params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodProductAnalyticsWatchPolicy:
		// 官方是 mode:'stream'；本仓 HTTP 面用 NDJSON 长连接，mux 面由
		// OpenOfficialFrameStream 的同一台帧机承接。
		s.serveOfficialStream(w, r, name)
	}
}

// startOfficialAnalyticsPolicyStream 打开 watchPolicy 的真实帧机。
//
// 预取首帧由 startOfficialStream 统一做；这里只负责建立订阅并提供一个
// 可取消的流（Close 会取消 service 侧订阅，避免断线后泄漏 goroutine）。
func (s *Server) startOfficialAnalyticsPolicyStream() (OfficialFrameStream, *OfficialStreamFault) {
	api, ok := s.officialAnalyticsAPI()
	if !ok || api.WatchPolicy == nil {
		return nil, officialStreamNotImplemented(MethodProductAnalyticsWatchPolicy,
			"宿主没有产品分析策略流（OfficialAnalyticsAPI.WatchPolicy 未接线）")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := api.WatchPolicy(ctx)
	if err != nil {
		cancel()
		return nil, officialStreamInternal(MethodProductAnalyticsWatchPolicy, "打开策略流失败：%v", err)
	}
	if ch == nil {
		cancel()
		return nil, officialStreamInternal(MethodProductAnalyticsWatchPolicy, "策略流返回了 nil channel")
	}
	return &officialAnalyticsPolicyStream{ch: ch, cancel: cancel}, nil
}

// officialAnalyticsPolicyStream 把 bool channel 接成官方帧机。
type officialAnalyticsPolicyStream struct {
	ch     <-chan bool
	cancel context.CancelFunc
}

func (s *officialAnalyticsPolicyStream) Next(ctx context.Context) (json.RawMessage, bool, *OfficialStreamFault) {
	select {
	case value, ok := <-s.ch:
		if !ok {
			return nil, true, nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, true, officialStreamInternal(MethodProductAnalyticsWatchPolicy, "序列化 bool 失败：%v", err)
		}
		return raw, false, nil
	case <-ctx.Done():
		return nil, true, nil
	}
}

func (s *officialAnalyticsPolicyStream) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}
