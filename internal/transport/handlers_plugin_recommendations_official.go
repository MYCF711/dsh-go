package transport

// pluginRecommendations.describe 的真实宿主接线。
//
// 官方 wire：`describe(agentId, callId) → Available{title,description,enabled}
// | Refused{reason: unknown|unavailable|not-live-root|closing}`。本文件只做
// 参数校验与结果写出；目录/安装状态/actionCards 都由宿主实现提供。
//
// WIRING：`ServeOfficialMediaMarketAPI` 解码参数后、route 之前判断
// `Method(name) == MethodPluginRecommendationsDescribe && api.Describe != nil`，
// 有实现时走本文件；没有实现时保持旧 501。

import (
	"context"
	"fmt"
	"net/http"
)

// OfficialPluginRecommendationsAPI 是宿主实现函数表。Describe 为 nil 表示
// 未接线 → 旧 501。
type OfficialPluginRecommendationsAPI struct {
	Describe func(ctx context.Context, agentID, callID string) (map[string]any, error)
}

var pluginRecommendationsMount = struct {
	byServ map[*Server]OfficialPluginRecommendationsAPI
}{byServ: map[*Server]OfficialPluginRecommendationsAPI{}}

// AttachOfficialPluginRecommendationsAPI 给这个 Server 挂真实实现。
func (s *Server) AttachOfficialPluginRecommendationsAPI(api OfficialPluginRecommendationsAPI) {
	if s == nil {
		return
	}
	pluginRecommendationsMount.byServ[s] = api
}

// DetachOfficialPluginRecommendationsAPI 摘掉（测试清理用）。
func (s *Server) DetachOfficialPluginRecommendationsAPI() {
	if s == nil {
		return
	}
	delete(pluginRecommendationsMount.byServ, s)
}

func (s *Server) officialPluginRecommendationsAPI() OfficialPluginRecommendationsAPI {
	if s == nil {
		return OfficialPluginRecommendationsAPI{}
	}
	return pluginRecommendationsMount.byServ[s]
}

// serveOfficialPluginRecommendationsDescribe 解析官方参数并调用宿主。
func (s *Server) serveOfficialPluginRecommendationsDescribe(w http.ResponseWriter, ctx context.Context, p *officialParams, api OfficialPluginRecommendationsAPI) {
	agentID, err := p.sessionRef()
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	callID, err := p.str("callId")
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	if callID == "" {
		writeOfficialError(w, officialBadRequest("callId 是空串"))
		return
	}
	if err := p.finish(); err != nil {
		writeOfficialError(w, err)
		return
	}
	if api.Describe == nil {
		writeOfficialError(w, fmt.Errorf("%w：pluginRecommendations.describe 未接线", ErrOfficialNotImplemented))
		return
	}
	result, err := api.Describe(ctx, agentID, callID)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	writeJSON(w, http.StatusOK, result)
}
