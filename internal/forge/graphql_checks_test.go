package forge

// ChangeRequestChecks 的 review thread 真值测试（G2）。
//
// 官方口径（只读 rc.2 解包树）：
//
//	dsh-experimental-forge-github/src/graphql.ts:554-555
//	  unresolvedThreads = reviewThreads.nodes.filter(t => !t.isResolved).length
//	  threadsTruncated  = reviewThreads.pageInfo.hasNextPage（读满 maxReviewThreads 后还有下一页）
//	dsh-experimental-forge-github/src/graphql.ts:368-392（readPages：按 maxReviewThreads 追加页）
//	dsh-experimental-forge/src/types.ts:169-171（threadsTruncated = "unresolvedThreads 是下界"）
//
// 还要求：checks 与 changeRequestReview **共用同一份 thread 读取实现**（同一份
// GraphQL 文档 + 同一套分页/截断逻辑），不是两套查询。最后一条用例直接把两次
// 调用的 GraphQL 文档与变量抓下来逐字比对。
//
// 全部走 local httptest（REST 与 GraphQL 同一台桩），不联网。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

// checksServer 是同时提供 REST 与 GraphQL 的桩：ChangeRequestChecks 先走 REST
// （PR 元数据 / check-runs / reviews），再走 GraphQL 读 review threads；只有同一
// 台服务器两条路径都能应答，降级与真值才都是真实请求。
type checksServer struct {
	rest *rc2Fixture
	gql  *gqlFixture
}

func (s *checksServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/graphql" {
		s.gql.ServeHTTP(w, r)
		return
	}
	s.rest.serveHTTP(w, r)
}

func newChecksServer(t *testing.T, graphql func(gqlCall) (int, string)) (*checksServer, *ChangeRequestProvider, string) {
	t.Helper()
	fixture := &checksServer{rest: &rc2Fixture{}, gql: &gqlFixture{handler: graphql}}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	provider := &ChangeRequestProvider{BaseURL: server.URL, Token: "test-token", HTTP: server.Client()}
	return fixture, provider, "https://github.com/o/r/pull/7"
}

// threadWith 造一条指定解析态的 review thread（其余字段与官方文档一致）。
func threadWith(id string, resolved bool) map[string]any {
	node := reviewThreadNode(id)
	node["isResolved"] = resolved
	return node
}

// ---- 1. 全 resolved：真值 0，不是下界 ----

func TestChecksThreadsAllResolved(t *testing.T) {
	fixture, provider, rawURL := newChecksServer(t, func(call gqlCall) (int, string) {
		if op := gqlOp(call.Query); op != "review-first" {
			t.Fatalf("checks 的 thread 读必须复用 REVIEW_QUERY（review-first），实际 %s：%s", op, call.Query)
		}
		return gqlData(t, reviewFirstPage(headA, false, nil,
			threadWith("PRRT_1", true), threadWith("PRRT_2", true)))
	})
	snapshot, err := provider.ChangeRequestChecks(context.Background(), transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headA})
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	if calls := fixture.gql.count(); calls != 1 {
		t.Fatalf("thread 读应只发一次 GraphQL 请求，实际 %d", calls)
	}
	if snapshot.UnresolvedThreads != 0 {
		t.Fatalf("全 resolved 时 unresolvedThreads = %d，期望 0", snapshot.UnresolvedThreads)
	}
	// 读全了：threadsTruncated 必须是 false（真值，不是下界）。
	if snapshot.ThreadsTruncated {
		t.Fatal("thread 已读全（hasNextPage=false）时 threadsTruncated 必须 false")
	}
}

// ---- 2. 部分 resolve：只数未 resolve 的 ----

func TestChecksThreadsPartiallyResolved(t *testing.T) {
	_, provider, rawURL := newChecksServer(t, func(call gqlCall) (int, string) {
		return gqlData(t, reviewFirstPage(headA, false, nil,
			threadWith("PRRT_1", true), threadWith("PRRT_2", false), threadWith("PRRT_3", true)))
	})
	snapshot, err := provider.ChangeRequestChecks(context.Background(), transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headA})
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	if snapshot.UnresolvedThreads != 1 {
		t.Fatalf("3 条里 1 条未 resolve：unresolvedThreads = %d，期望 1", snapshot.UnresolvedThreads)
	}
	if snapshot.ThreadsTruncated {
		t.Fatal("3 条都在一页里读完时 threadsTruncated 必须 false")
	}
}

// ---- 3. 超上限截断：读到 maxReviewThreads 就停，truncated 取 hasNextPage ----

func TestChecksThreadsTruncated(t *testing.T) {
	firsts := []any{}
	fixture, provider, rawURL := newChecksServer(t, func(call gqlCall) (int, string) {
		firsts = append(firsts, call.Variables["first"])
		// 每页 2 条、还有下一页；maxReviewThreads=2 时读完第一页就停。
		return gqlData(t, reviewFirstPage(headA, true, "TC_1",
			threadWith("PRRT_1", false), threadWith("PRRT_2", true)))
	})
	provider.MaxReviewThreads = 2
	snapshot, err := provider.ChangeRequestChecks(context.Background(), transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headA})
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	if len(firsts) != 1 || firsts[0] != float64(2) {
		t.Fatalf("首页 first 应为 min(PAGE_SIZE, maxReviewThreads)=2，实际 %v", firsts)
	}
	// 达到上限后不许再发第二页（官方 readPages 的循环条件）。
	if calls := fixture.gql.count(); calls != 1 {
		t.Fatalf("读满上限后不应再请求下一页，实际 %d 次", calls)
	}
	if snapshot.UnresolvedThreads != 1 {
		t.Fatalf("读到的 2 条里 1 条未 resolve：unresolvedThreads = %d，期望 1", snapshot.UnresolvedThreads)
	}
	// 达到上限且还有下一页 → 官方 truncated=true（此时 unresolvedThreads 是下界）。
	if !snapshot.ThreadsTruncated {
		t.Fatal("读满 maxReviewThreads 且 hasNextPage=true 时 threadsTruncated 必须 true")
	}
}

// ---- 4. GraphQL 失败：退回 REST 下界，且如实标注 ----

func TestChecksThreadsGraphQLFailureDegrades(t *testing.T) {
	fixture, provider, rawURL := newChecksServer(t, func(call gqlCall) (int, string) {
		return http.StatusInternalServerError, `{"data":null,"errors":[{"type":"INTERNAL","message":"boom"}]}`
	})
	snapshot, err := provider.ChangeRequestChecks(context.Background(), transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headA})
	if err != nil {
		// 降级不是失败：REST 拿到的快照必须照常返回。
		t.Fatalf("GraphQL 失败不该让整个 checks 快照失败：%v", err)
	}
	if calls := fixture.gql.count(); calls != 1 {
		t.Fatalf("应尝试过一次 GraphQL thread 读，实际 %d 次", calls)
	}
	// REST 没有 review thread 解析态：unresolvedThreads 只能是下界 0。
	if snapshot.UnresolvedThreads != 0 {
		t.Fatalf("降级时 unresolvedThreads = %d，期望下界 0（不编造）", snapshot.UnresolvedThreads)
	}
	// 官方 threadsTruncated 的语义就是"unresolvedThreads 是下界"，降级必须置真。
	if !snapshot.ThreadsTruncated {
		t.Fatal("GraphQL 不可用时 threadsTruncated 必须 true（下界标注），不许伪装成真值")
	}
	// 降级只影响这两个字段：REST 的其余快照字段照常。
	if snapshot.Title != "rc2 provider" || snapshot.HeadSHA != headA || len(snapshot.Checks) != 2 {
		t.Fatalf("降级不该破坏 REST 快照其余字段：%+v", snapshot)
	}
	// 能力面说明必须写清"什么时候才是真值"。
	if !containsAll(ThreadsCapableWhen, "GraphQL", "GITHUB_TOKEN", "gh auth login") {
		t.Fatalf("capableWhen 必须说明补救方式：%q", ThreadsCapableWhen)
	}
}

// ---- 5. 共用同一份 thread 读取实现（同一份文档 + 同一套变量）----

func TestChecksSharesReviewThreadQuery(t *testing.T) {
	ctx := context.Background()
	rawURL := "https://github.com/o/r/pull/7"
	reviewFixture, reviewProvider, _ := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, reviewFirstPage(headA, false, nil, threadWith("PRRT_1", true)))
	})
	if _, err := reviewProvider.ChangeRequestReview(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, headA, false); err != nil {
		t.Fatalf("readReview: %v", err)
	}
	checksFixture, checksProvider, _ := newChecksServer(t, func(call gqlCall) (int, string) {
		return gqlData(t, reviewFirstPage(headA, false, nil, threadWith("PRRT_1", true)))
	})
	if _, err := checksProvider.ChangeRequestChecks(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headA}); err != nil {
		t.Fatalf("checks: %v", err)
	}

	reviewCalls := reviewFixture.snapshot()
	checksCalls := checksFixture.gql.snapshot()
	if len(reviewCalls) == 0 || len(checksCalls) == 0 {
		t.Fatalf("两次调用都必须发 GraphQL 请求（review=%d checks=%d）", len(reviewCalls), len(checksCalls))
	}
	if reviewCalls[0].Query != checksCalls[0].Query {
		t.Fatalf("checks 与 readReview 必须共用同一份 thread 查询文档：\nreadReview=%s\nchecks=%s",
			reviewCalls[0].Query, checksCalls[0].Query)
	}
	for key, want := range reviewCalls[0].Variables {
		if got := checksCalls[0].Variables[key]; got != want {
			t.Fatalf("GraphQL 变量 %q 不一致：readReview=%v checks=%v", key, want, got)
		}
	}
	// 文档本身必须真的取 isResolved（不是靠别的手段猜解析态）。
	if !containsAll(checksCalls[0].Query, "isResolved", "reviewThreads(first: $first)") {
		t.Fatalf("thread 查询必须取 isResolved：%s", checksCalls[0].Query)
	}
}

// containsAll 报告 text 是否含全部子串。
func containsAll(text string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(text, sub) {
			return false
		}
	}
	return true
}
