package transport

// dynamicCordisRunner.*（12）真实接线入口。
//
// 官方 Cordis 是进程内调用，没有可抄的 RPC wire；本仓 Go 核心 + 外部受管 node
// 双轨（见 internal/cordis、internal/cordis/extern）。本文件只做 wire 方法分发，
// 真实数据由 cmd/dshd 的 OfficialCordisAPI 提供。
//
// 2026-11-15：`handlers_media_market_official.go` 里那张 `officialCordisSpecs`
// R6-501 表已删除——它对本文件这 12 个方法**整条不可达**（`server.go` 的
// `IsOfficialCordisMethod` 分支排在 `IsOfficialMediaMarketMethod` 之前），却让
// 账本与 R6 叙事多出一份"明确不做"的结论。那 11 个 wire 常量仍留在 media 文件里
// （见那边的常量块注释），认领与实现在本文件。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

const MethodDynamicCordisRunnerInventory Method = "dynamicCordisRunner.inventory"

// OfficialCordisAPI 是宿主提供的 12 方法实现。
type OfficialCordisAPI interface {
	HandleOfficialCordis(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// OfficialCordisNullResult 是"官方结果就是字面量 null"的哨兵。
//
// `reportClientGuardFailure` / `reportRenderFailure` 的官方 zod 结果都是
// `z.literal(null)`（`dsh-cordis-host-runner/lib/typert.host.js:94` / `:109`）：
// 它们真正的交付是副作用，回执没有载荷。而 `writeJSON` 会把 nil 载荷判成
// "静默成功"并转 500（`server.go` 的"成功响应不许是空值"），所以这里必须显式
// 走 `writeOfficialNullResult`——与 `directoryPicker.pick` 的取消、
// `pluginRegistryProbe.fastest` 的"没有更快的 registry"同一出口。
type OfficialCordisNullResult struct{}

// NullCordisResult 供 OfficialCordisAPI 实现在"官方回执是 null"的方法上返回。
func NullCordisResult() OfficialCordisNullResult { return OfficialCordisNullResult{} }

// CordisAPIError 让实现把结构化错误（无 node / 未知 requestId / 取消）带到 wire。
type CordisAPIError struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e *CordisAPIError) Error() string { return e.Message }

var officialCordisMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialCordisAPI
}{m: map[*Server]OfficialCordisAPI{}}

func (s *Server) AttachOfficialCordisAPI(api OfficialCordisAPI) {
	if s == nil {
		return
	}
	officialCordisMount.mu.Lock()
	defer officialCordisMount.mu.Unlock()
	officialCordisMount.m[s] = api
}

func (s *Server) DetachOfficialCordisAPI() {
	officialCordisMount.mu.Lock()
	defer officialCordisMount.mu.Unlock()
	delete(officialCordisMount.m, s)
}

func (s *Server) officialCordisAPI() (OfficialCordisAPI, bool) {
	officialCordisMount.mu.RLock()
	defer officialCordisMount.mu.RUnlock()
	api, ok := officialCordisMount.m[s]
	return api, ok
}

// IsOfficialCordisMethod 认全部 12 个方法（含此前漏掉的 inventory）。
func IsOfficialCordisMethod(name string) bool {
	switch Method(name) {
	case MethodDynamicCordisRunnerInventory,
		MethodDynamicCordisRunnerGetClientCode,
		MethodDynamicCordisRunnerInvoke,
		MethodDynamicCordisRunnerReportClientGuardFailure,
		MethodDynamicCordisRunnerReportRenderFailure,
		MethodDynamicCordisRunnerResolveInspectQuery,
		MethodDynamicCordisRunnerResolveRequestRun,
		MethodDynamicCordisRunnerRunHostHalf,
		MethodDynamicCordisRunnerSettleUserRun,
		MethodDynamicCordisRunnerStopFromPanel,
		MethodDynamicCordisRunnerSyncInspectManifest,
		MethodDynamicCordisRunnerUndefineFromPanel:
		return true
	}
	return false
}

// ServeOfficialCordisAPI 是 /api/dynamicCordisRunner.* 的入口。
func (s *Server) ServeOfficialCordisAPI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if !IsOfficialCordisMethod(name) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", "不是 dynamicCordisRunner 方法"))
		return
	}
	api, ok := s.officialCordisAPI()
	if !ok {
		writeOfficialError(w, officialNotImplemented("%s 未接线：宿主没有 AttachOfficialCordisAPI", name))
		return
	}
	var raw json.RawMessage
	if r.Body != nil {
		var err error
		raw, err = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeOfficialError(w, officialBadRequest("读参数失败: %v", err))
			return
		}
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	result, err := api.HandleOfficialCordis(r.Context(), name, raw)
	if err != nil {
		var ce *CordisAPIError
		if errors.As(err, &ce) {
			msg := ce.Message
			if ce.Hint != "" {
				msg += "；" + ce.Hint
			}
			writeJSON(w, ce.Status, errorBodyFor(ce.Code, msg))
			return
		}
		writeOfficialError(w, officialBadRequest("%v", err))
		return
	}
	// 官方结果类型本身是 `null` 的那几个方法（见 OfficialCordisNullResult）。
	if _, isNull := result.(OfficialCordisNullResult); isNull {
		writeOfficialNullResult(w)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
