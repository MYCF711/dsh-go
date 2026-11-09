package transport

// dynamicCordisRunner.*（12）真实接线入口。
//
// 官方 Cordis 是进程内调用，没有可抄的 RPC wire；本仓 Go 核心 + 外部受管 node
// 双轨（见 internal/cordis、internal/cordis/extern）。本文件只做 wire 方法分发，
// 真实数据由 cmd/dshd 的 OfficialCordisAPI 提供。

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
	writeJSON(w, http.StatusOK, result)
}
