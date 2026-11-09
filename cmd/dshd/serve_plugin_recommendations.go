package main

// PARITY-W3-RECOMMENDATIONS：pluginRecommendations.describe 的宿主接线。
//
// 真实目录来自 pluginMarket 索引（<home>/state/plugin-market-indexes.json），
// 安装/启用状态来自真实 pluginManager state，卡片来自 actionCards 的 v4
// 投影；没有索引/没有已安装包时由服务返回 refused{unavailable}，不编推荐。
//
// WIRING（见 tmp/GO2-TEAM/PARITY-W3-RECOMMENDATIONS/WIRING.md）：
//   srv.AttachOfficialPluginRecommendationsAPI(newPluginRecommendationsAPI(*home, pluginManager))

import (
	"context"
	"fmt"

	"dshgo/internal/actioncards"
	"dshgo/internal/market"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/recommendations"
	"dshgo/internal/transport"
)

func newPluginRecommendationsAPI(home string, manager *pluginmgr.Manager) (transport.OfficialPluginRecommendationsAPI, error) {
	service, err := recommendations.New(home, manager, market.Open(home), actioncards.Open(home))
	if err != nil {
		return transport.OfficialPluginRecommendationsAPI{}, err
	}
	if err := service.EnsureSkill(context.Background()); err != nil {
		service.Close()
		return transport.OfficialPluginRecommendationsAPI{}, fmt.Errorf("写 official-plugins skill: %w", err)
	}
	return transport.OfficialPluginRecommendationsAPI{Describe: service.Describe}, nil
}
