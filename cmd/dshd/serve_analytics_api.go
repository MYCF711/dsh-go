package main

// 官方 productAnalytics.* 三个方法在宿主侧的装配。
//
// 本文件只做"真实管道 → Transport 回调"的映射；策略、脱敏、JSONL hash 链、
// 可选 exporter 都在 internal/analytics。默认 mode=local，一个字节都不出网。
//
// 接线（integrator，见 tmp/GO2-TEAM/PARITY-ANALYTICS/WIRING.md）：
//
//	cmd/dshd/serve.go 的装配段（在 AttachOfficialAccountAPI 之后）：
//	    if err := attachOfficialAnalytics(srv, *home); err != nil {
//	        return err
//	    }
//
// 之所以不在这里直接改 serve.go：本任务写作用域只到"相关 cmd 接线"，而
// serve.go 在共享树有他人未提交改动，按 WIRING 串行接入避免冲突。

import (
	"context"

	"dshgo/internal/analytics"
	"dshgo/internal/officialwire"
	"dshgo/internal/transport"
)

// attachOfficialAnalytics 打开 home 下的 analytics Service 并挂到 Transport。
//
// 打不开（目录不可写、policy.json 损坏）直接返回错误：不能静默挂一个
// "永远 true 但什么也不存"的假实现。
func attachOfficialAnalytics(srv *transport.Server, home string) error {
	service, err := analytics.Open(home)
	if err != nil {
		return err
	}
	srv.AttachOfficialAnalyticsAPI(transport.OfficialAnalyticsAPI{
		Enabled: func(context.Context) (bool, error) {
			return service.Enabled(), nil
		},
		Report: func(ctx context.Context, event transport.OfficialProductEvent) error {
			_, err := service.Report(ctx, analytics.Event{
				EventName:  event.EventName,
				Timestamp:  event.Timestamp,
				Attributes: event.Attributes,
			})
			return err
		},
		WatchPolicy: func(ctx context.Context) (<-chan bool, error) {
			return service.WatchPolicy(ctx)
		},
	})
	return nil
}

// registerOfficialAnalyticsWire 把官方 L2 unary 的两个方法注册到官方 wire。
//
// 官方 L1（`POST /api/productAnalytics.*`）由 transport 的
// serveOfficialAnalyticsAPI 承接；L2 的 `productAnalytics/enabled` 与
// `productAnalytics/report` 是 unary，`productAnalytics/watchPolicy` 是
// mode:'stream'（由 officialMuxStreamOpener → OpenOfficialFrameStream 自动承接，
// **不要**在这里注册成 unary）。
//
// 接线（integrator，见 WIRING.md）：
//
//	cmd/dshd/serve_official_wire.go 的 newOfficialWireServer 末尾、return 之前：
//	    if err := registerOfficialAnalyticsWire(server, client); err != nil {
//	        return nil, err
//	    }
func registerOfficialAnalyticsWire(server *officialwire.Server, client *officialwire.LegacyClient) error {
	if failure := server.Handle("productAnalytics/enabled",
		client.Binding("productAnalytics.enabled", &officialwire.Descriptor{}, nil)); failure != nil {
		return failure
	}
	if failure := server.Handle("productAnalytics/report",
		client.Binding("productAnalytics.report", &officialwire.Descriptor{Required: []string{"event"}}, nil)); failure != nil {
		return failure
	}
	return nil
}
