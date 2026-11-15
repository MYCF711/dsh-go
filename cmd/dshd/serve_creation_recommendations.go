package main

// 官方 0.2.2-rc.2 `create_personalized_page` 卡片 kind 的宿主接线。
//
// # 为什么接在这里，而不是 serve.go 的装配段
//
// `actionCards.act` 的回调是**共享**的：官方账号组的
// `OfficialAccountAPI.ActionCardAct` 一个 Server 只有一份（serve_action_cards.go
// 的 `actionCardAct(home)`），没有"每个 kind 一个 attach 入口"的形状。kind 必须
// 注册到 `actionCardAct(home)` 用的**同一个** `actioncards.Service`——两者都走
// `actioncards.Open(home)`（按 home 进程内复用），所以只要注册进同一个 home 的
// Service，`propose_action` 工具与 `actionCards.act` 就都看得到。
//
// `newPluginRecommendationsAPI` 是 serve.go 装配段里已有的调用点（同一条
// "官方推荐卡"接线），本函数从那里调用即可，不需要改 serve.go。
//
// 真实语义与落地对照见 internal/creation 的包文档：生产路径**不注入 Creator**
// （宿主没有页面渲染运行时与 Cordis 生成器），所以 Create 会返回
// `refused{reason:"creator-unavailable"}`；注入 Creator 时
// 校验 → 复制到 profile → 真调 pluginManager.Install 全部真实执行。

import (
	"context"
	"fmt"
	"os"

	"dshgo/internal/actioncards"
	"dshgo/internal/creation"
	"dshgo/internal/pluginmgr"
)

// newCreationRecommendations 构造服务、注册 kind，并写真实的 personalized-pages skill。
//
// 注册失败就返回错误、不接线：kind 没注册成功时 propose_action 会以
// `ACTION_CARD_UNKNOWN` 拒绝这个 action，而 `actionCards.act` 会回
// `refused{unavailable}`——两种"看起来像坏掉"的现场都不如启动时大声报出来。
func newCreationRecommendations(home string, manager *pluginmgr.Manager, opts ...creation.Option) (*creation.Service, error) {
	options := append([]creation.Option{creation.WithLogf(logCreationRecommendations)}, opts...)
	service, err := creation.New(home, manager, actioncards.Open(home), options...)
	if err != nil {
		return nil, err
	}
	if err := service.EnsureSkill(context.Background()); err != nil {
		service.Close()
		return nil, fmt.Errorf("写 personalized-pages skill: %w", err)
	}
	return service, nil
}

// attachCreationRecommendations 是生产接线：**不注入 Creator**。
//
// 返回的 Service 不需要调用方持有：kind 注册表里的闭包引用着它，进程生命周期内
// 不会释放；这里返回 error 只是为了让装配段能大声报"卡片没接上"。
func attachCreationRecommendations(home string, manager *pluginmgr.Manager) error {
	_, err := newCreationRecommendations(home, manager)
	return err
}

func logCreationRecommendations(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "creation-recommendations: "+format+"\n", args...)
}
