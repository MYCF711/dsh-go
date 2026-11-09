package main

// PARITY-W2-PROVIDER：forgePanel 四个 Change Request 读取的宿主实现。
// 真实 GitHub HTTP 读取在 internal/forge；本层只做接口注入与错误映射。

import (
	"context"
	"errors"

	"dshgo/internal/forge"
	"dshgo/internal/transport"
)

type forgeChangeRequestAPI struct {
	provider *forge.ChangeRequestProvider
}

func newForgeChangeRequestAPI() *forgeChangeRequestAPI {
	return &forgeChangeRequestAPI{provider: forge.NewChangeRequestProviderFromEnv()}
}

func (a *forgeChangeRequestAPI) ChangeRequestChecks(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (transport.OfficialForgeChangeRequestChecks, error) {
	result, err := a.provider.ChangeRequestChecks(ctx, params)
	return result, mapForgeChangeRequestError(err)
}

func (a *forgeChangeRequestAPI) ChangeRequestDiff(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (transport.OfficialForgeChangeRequestDiff, error) {
	result, err := a.provider.ChangeRequestDiff(ctx, params)
	return result, mapForgeChangeRequestError(err)
}

func (a *forgeChangeRequestAPI) ChangeRequestText(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (string, error) {
	result, err := a.provider.ChangeRequestText(ctx, params)
	return result, mapForgeChangeRequestError(err)
}

func (a *forgeChangeRequestAPI) ChangeRequestStates(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (transport.OfficialForgeChangeRequestStatesFrame, error) {
	result, err := a.provider.ChangeRequestStates(ctx, params)
	return result, mapForgeChangeRequestError(err)
}

// mapForgeChangeRequestError 把 provider 的 HTTP 错误折成 transport 的结构化
// 四字段错误；未登录/网络/上游 5xx 都不是 501。
func mapForgeChangeRequestError(err error) error {
	var providerErr *forge.ForgeHTTPError
	if errors.As(err, &providerErr) {
		return &transport.ForgeChangeRequestError{Status: providerErr.Status, Code: providerErr.Code, Message: providerErr.Message}
	}
	return err
}

var _ transport.OfficialForgeChangeRequestAPI = (*forgeChangeRequestAPI)(nil)
