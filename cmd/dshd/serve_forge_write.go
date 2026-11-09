package main

// PARITY-W2-WRITE：forgePanel 三个受控写操作的宿主适配器。
//
// 真实写客户端在 internal/forge（与只读 ChangeRequestProvider 共用 URL/
// token/API base）；本文件只把 transport 的函数表接上。生产令牌来自
// GITHUB_TOKEN/GH_TOKEN 或 `gh auth token`；测试/本地 fixture 用
// DSHD_GITHUB_API_URL 指向本地 HTTP server。
//
// WIRING（见 tmp/GO2-TEAM/PARITY-W2-WRITE/WIRING.md）：
//   srv.AttachOfficialForgeWriteAPI(newForgeWriteAPI(*home))
// 并在共享 serveOfficialForgeLiveshareAPI 里加写方法分支。

import (
	"context"

	"dshgo/internal/forge"
	"dshgo/internal/transport"
)

// newForgeWriteAPI 给当前 home 造一份受控写实现。
func newForgeWriteAPI(home string) transport.OfficialForgeWriteAPI {
	_ = home
	provider := forge.NewWriteProviderFromEnv()
	return transport.OfficialForgeWriteAPI{
		MarkReady: func(ctx context.Context, _, rawURL, headSha string) error {
			return provider.MarkReady(ctx, rawURL, headSha)
		},
		Merge: func(ctx context.Context, _, rawURL, method, headSha string) error {
			return provider.Merge(ctx, rawURL, method, headSha)
		},
		SubmitReview: func(ctx context.Context, _, rawURL string, request transport.OfficialForgeReviewRequest) (transport.OfficialForgeReviewResult, error) {
			comment := func(c transport.OfficialForgeReviewComment) forge.ReviewComment {
				return forge.ReviewComment{Path: c.Path, Side: c.Side, StartLine: c.StartLine, EndLine: c.EndLine, Body: c.Body}
			}
			forgeRequest := forge.ReviewRequest{
				HeadSha: request.HeadSha, Kind: request.Kind,
				Event: request.Event, Body: request.Body,
			}
			if request.Comment != nil {
				converted := comment(*request.Comment)
				forgeRequest.Comment = &converted
			}
			for _, c := range request.Comments {
				forgeRequest.Comments = append(forgeRequest.Comments, comment(c))
			}
			result, err := provider.SubmitReview(ctx, rawURL, forgeRequest)
			if err != nil {
				return transport.OfficialForgeReviewResult{}, err
			}
			return transport.OfficialForgeReviewResult{
				OK: result.OK, URL: result.URL, Code: result.Code, Message: result.Message,
			}, nil
		},
	}
}
