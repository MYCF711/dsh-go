package forge

// GraphQL 面（review threads / viewed 标记）的离线测试。
//
// 全部走 local httptest 桩：假的是 **GitHub GraphQL 的响应**（`POST /graphql`），
// 被验的是 provider 真实发出的文档、变量、鉴权头，以及把响应映射成官方 union
// 的每一个字段。没有一条用例会真的出网（Token/BaseURL 都指向桩；没有凭据的
// 分支在发请求之前就短路了）。
//
// 契约来源（只读）：
//
//	dsh-experimental-forge-github/src/threads.ts（REVIEW_QUERY / 分页 / toThread）
//	dsh-experimental-forge-github/src/viewed.ts（VIEWED_QUERY / mark / unmark）
//	dsh-experimental-forge-github/src/review-writes.ts（7 分支 union 的 mutation）
//	dsh-experimental-forge-github/src/mutation.ts（sendMutation 的 failed/unknown）
//	dsh-experimental-forge-github/src/pull-request-node.ts（写前 head 校验）
//	dsh-experimental-forge/src/types.ts（ForgeReviewData / ForgeViewedFiles / union）

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/transport"
)

// gqlCall 是一次被桩看到的 GraphQL 请求。
type gqlCall struct {
	Method    string
	Path      string
	Query     string
	Variables map[string]any
	Auth      string
}

// gqlFixture 是假 GitHub GraphQL：按 handler 返回状态码 + 原始 JSON。
type gqlFixture struct {
	mu      sync.Mutex
	calls   []gqlCall
	handler func(call gqlCall) (int, string)
}

func (f *gqlFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var payload struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "bad graphql body: "+err.Error(), http.StatusBadRequest)
		return
	}
	call := gqlCall{
		Method: r.Method, Path: r.URL.Path, Query: payload.Query,
		Variables: payload.Variables, Auth: r.Header.Get("Authorization"),
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	status, out := f.handler(call)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(out))
}

func (f *gqlFixture) snapshot() []gqlCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gqlCall(nil), f.calls...)
}

func (f *gqlFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// gqlOp 用文档里的 mutation/connection 名字给一次调用分类（桩按它分派）。
func gqlOp(query string) string {
	switch {
	// unmark 的名字里含 "markFileAsViewed"，必须先判。
	case strings.Contains(query, "unmarkFileAsViewed"):
		return "unmark"
	case strings.Contains(query, "markFileAsViewed"):
		return "mark"
	case strings.Contains(query, "addPullRequestReviewThreadReply"):
		return "reply"
	case strings.Contains(query, "addPullRequestReviewThread"):
		return "add-thread"
	// unresolve 的名字里含 "resolveReviewThread"，必须先判。
	case strings.Contains(query, "unresolveReviewThread"):
		return "unresolve"
	case strings.Contains(query, "resolveReviewThread"):
		return "resolve"
	case strings.Contains(query, "submitPullRequestReview"):
		return "submit"
	case strings.Contains(query, "updatePullRequestReviewComment"):
		return "edit-comment"
	case strings.Contains(query, "deletePullRequestReviewComment"):
		return "delete-comment"
	case strings.Contains(query, "deletePullRequestReview("):
		return "discard"
	case strings.Contains(query, "addPullRequestReview("):
		return "add-review"
	case strings.Contains(query, "target: node"):
		return "write-target"
	case strings.Contains(query, "PullRequestReviewThread"):
		return "thread-comments"
	case strings.Contains(query, "reviewThreads(first: $first, after: $cursor)"):
		return "threads-page"
	case strings.Contains(query, "reviewThreads(first: $first)"):
		return "review-first"
	case strings.Contains(query, "reviews(states: PENDING"):
		return "write-check"
	case strings.Contains(query, "viewerViewedState"):
		return "viewed-page"
	case strings.Contains(query, "pullRequest(number: $number) { id headRefOid }"):
		return "pr-node"
	}
	return "unknown"
}

// gqlData 把 data 载荷包成 GraphQL 应答。
func gqlData(t *testing.T, data any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatalf("桩数据无法序列化：%v", err)
	}
	return http.StatusOK, string(raw)
}

// gqlErrors 是 GitHub 只要拒绝就给的那种应答（data 为 null + errors[]）。
func gqlErrors(status int, errType, message string) (int, string) {
	payload := map[string]any{
		"data":   nil,
		"errors": []any{map[string]any{"type": errType, "message": message}},
	}
	raw, _ := json.Marshal(payload)
	return status, string(raw)
}

// newGraphQLProvider 造一个只连桩的 provider。
func newGraphQLProvider(t *testing.T, handler func(gqlCall) (int, string)) (*gqlFixture, *ChangeRequestProvider, string) {
	t.Helper()
	fixture := &gqlFixture{handler: handler}
	server := httptest.NewServer(http.HandlerFunc(fixture.ServeHTTP))
	t.Cleanup(server.Close)
	provider := &ChangeRequestProvider{BaseURL: server.URL, Token: "test-token", HTTP: server.Client()}
	return fixture, provider, "https://github.com/o/r/pull/7"
}

// gqlParams 是 provider 的入参形状。
func gqlParams(rawURL string) transport.OfficialForgeChangeRequestParams {
	return transport.OfficialForgeChangeRequestParams{SessionID: "s-1", URL: rawURL}
}

// reviewThreadNode 造一个 GitHub reviewThreads 节点（字段名与官方文档逐字一致）。
func reviewThreadNode(id string) map[string]any {
	return map[string]any{
		"id": id, "isOutdated": false, "isResolved": false,
		"path": "a.go", "diffSide": "RIGHT", "startDiffSide": "RIGHT",
		"line": 12, "startLine": 10, "originalLine": 9, "originalStartLine": 7,
		"subjectType": "LINE", "viewerCanReply": true, "viewerCanResolve": true, "viewerCanUnresolve": false,
		"root": map[string]any{"nodes": []any{map[string]any{
			"diffHunk":       "@@ -1,3 +1,3 @@",
			"commit":         map[string]any{"oid": headA},
			"originalCommit": map[string]any{"oid": headB},
		}}},
		"comments": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
			"nodes": []any{
				map[string]any{
					"id": "PRRC_1", "state": "COMMENTED", "createdAt": "2026-11-10T00:00:00Z",
					"url": "https://github.com/o/r/pull/7#discussion_r1", "body": "first comment",
					"viewerCanUpdate": true, "viewerCanDelete": true,
					"author": map[string]any{"__typename": "User", "login": "bob", "avatarUrl": "https://avatars/bob"},
				},
				map[string]any{
					"id": "PRRC_2", "state": "PENDING", "createdAt": "2026-11-10T00:00:01Z",
					"url": nil, "body": "draft comment",
					"viewerCanUpdate": true, "viewerCanDelete": true,
					"author": nil,
				},
			},
		},
	}
}

// reviewFirstPage 造 REVIEW_QUERY 的完整应答。
func reviewFirstPage(head string, hasNext bool, cursor any, threads ...map[string]any) map[string]any {
	nodes := make([]any, 0, len(threads))
	for _, thread := range threads {
		nodes = append(nodes, thread)
	}
	return map[string]any{
		"viewer": map[string]any{"login": "alice"},
		"repository": map[string]any{
			"isArchived": false, "viewerPermission": "WRITE",
			"pullRequest": map[string]any{
				"id": "PR_1", "headRefOid": head, "locked": false,
				"reviews": map[string]any{"nodes": []any{
					map[string]any{"id": "PRR_pending", "viewerDidAuthor": true, "comments": map[string]any{"totalCount": 2}},
				}},
				"reviewThreads": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
					"nodes":    nodes,
				},
			},
		},
	}
}

// ---- 1. review threads 读：官方形状逐字段 ----

func TestGraphQLReviewReadsThreads(t *testing.T) {
	fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		if op := gqlOp(call.Query); op != "review-first" {
			t.Fatalf("第一条请求应是 REVIEW_QUERY，实际 %s：%s", op, call.Query)
		}
		return gqlData(t, reviewFirstPage(headA, false, nil, reviewThreadNode("PRRT_1")))
	})

	data, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false)
	if err != nil {
		t.Fatalf("review 读必须是真能力，实际失败：%v", err)
	}
	calls := fixture.snapshot()
	if len(calls) != 1 {
		t.Fatalf("应发 1 次 GraphQL 请求，实际 %d", len(calls))
	}
	call := calls[0]
	if call.Method != http.MethodPost || call.Path != "/graphql" {
		t.Fatalf("请求应是 POST /graphql，实际 %s %s", call.Method, call.Path)
	}
	if call.Auth != "Bearer test-token" {
		t.Fatalf("鉴权头错：%q", call.Auth)
	}
	if !strings.Contains(call.Query, "reviewThreads") || !strings.Contains(call.Query, "viewerCanResolve") {
		t.Fatalf("查询没有读 reviewThreads/viewerCanResolve：%s", call.Query)
	}
	for key, want := range map[string]any{"owner": "o", "name": "r", "number": float64(7), "first": float64(100)} {
		if got := call.Variables[key]; got != want {
			t.Fatalf("变量 %s = %v，期望 %v", key, got, want)
		}
	}

	if data["headSha"] != headA {
		t.Fatalf("headSha 应是请求的 head，实际 %v", data["headSha"])
	}
	if data["viewer"] != "alice" {
		t.Fatalf("viewer 应是 alice，实际 %v", data["viewer"])
	}
	if data["truncated"] != false {
		t.Fatalf("没有下一页时 truncated 必须是 false，实际 %v", data["truncated"])
	}
	if data["viewerCanReview"] != true {
		t.Fatalf("可写仓库 + 未锁定 → viewerCanReview 必须是 true，实际 %v", data["viewerCanReview"])
	}
	pending, ok := data["pendingReview"].(map[string]any)
	if !ok || pending["id"] != "PRR_pending" || pending["comments"] != 2 {
		t.Fatalf("pendingReview 形状错：%v", data["pendingReview"])
	}
	threads, ok := data["threads"].([]any)
	if !ok || len(threads) != 1 {
		t.Fatalf("threads 应是 1 条数组，实际 %v", data["threads"])
	}
	thread := threads[0].(map[string]any)
	if thread["id"] != "PRRT_1" || thread["path"] != "a.go" || thread["side"] != "R" ||
		thread["subject"] != "line" || thread["resolved"] != false ||
		thread["viewerCanReply"] != true || thread["viewerCanResolve"] != true {
		t.Fatalf("thread 基础字段错：%v", thread)
	}
	if thread["diffHunk"] != "@@ -1,3 +1,3 @@" {
		t.Fatalf("diffHunk 应原样透传，实际 %v", thread["diffHunk"])
	}
	current, ok := thread["current"].(map[string]any)
	if !ok || current["commit"] != headA || current["startLine"] != 10 || current["endLine"] != 12 {
		t.Fatalf("current 位置错（官方 anchor：commit=head、startLine=10、endLine=12）：%v", thread["current"])
	}
	original, ok := thread["original"].(map[string]any)
	if !ok || original["commit"] != headB || original["startLine"] != 7 || original["endLine"] != 9 {
		t.Fatalf("original 位置错（commit=headB、startLine=7、endLine=9）：%v", thread["original"])
	}
	comments, ok := thread["comments"].([]any)
	if !ok || len(comments) != 2 {
		t.Fatalf("comments 应是 2 条，实际 %v", thread["comments"])
	}
	first := comments[0].(map[string]any)
	if first["id"] != "PRRC_1" || first["body"] != "first comment" || first["pending"] != false ||
		first["viewerCanEdit"] != true || first["viewerCanDelete"] != true ||
		first["url"] != "https://github.com/o/r/pull/7#discussion_r1" {
		t.Fatalf("首条评论字段错：%v", first)
	}
	author, ok := first["author"].(map[string]any)
	if !ok || author["login"] != "bob" || author["bot"] != false || author["avatarUrl"] != "https://avatars/bob" {
		t.Fatalf("评论作者形状错：%v", first["author"])
	}
	second := comments[1].(map[string]any)
	if second["pending"] != true {
		t.Fatalf("PENDING 状态的评论 pending 必须是 true，实际 %v", second["pending"])
	}
	if second["author"] != nil {
		t.Fatalf("作者已注销时必须给 null，实际 %v", second["author"])
	}
}

// TestGraphQLReviewPaginatesThreadsAndComments：thread 分页 + 每条 thread 的
// comment 分页都要跟到底，且后续页必须再次报 head（官方 stale 判据）。
func TestGraphQLReviewPaginatesThreadsAndComments(t *testing.T) {
	second := reviewThreadNode("PRRT_2")
	second["comments"] = map[string]any{
		"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "CC_2"},
		"nodes": []any{map[string]any{
			"id": "PRRC_3", "state": "COMMENTED", "createdAt": "2026-11-10T00:00:02Z",
			"url": nil, "body": "root", "viewerCanUpdate": false, "viewerCanDelete": false,
			"author": map[string]any{"__typename": "Bot", "login": "ci", "avatarUrl": nil},
		}},
	}
	fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		switch gqlOp(call.Query) {
		case "review-first":
			if call.Variables["first"] != float64(100) {
				t.Fatalf("首页 first 应为 100，实际 %v", call.Variables["first"])
			}
			return gqlData(t, reviewFirstPage(headA, true, "TC_1", reviewThreadNode("PRRT_1")))
		case "threads-page":
			if call.Variables["id"] != "PR_1" || call.Variables["cursor"] != "TC_1" {
				t.Fatalf("thread 后续页变量错：%v", call.Variables)
			}
			return gqlData(t, map[string]any{"node": map[string]any{
				"headRefOid": headA,
				"reviewThreads": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
					"nodes":    []any{second},
				},
			}})
		case "thread-comments":
			if call.Variables["id"] != "PRRT_2" || call.Variables["cursor"] != "CC_2" {
				t.Fatalf("comment 后续页变量错：%v", call.Variables)
			}
			return gqlData(t, map[string]any{"node": map[string]any{"comments": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
				"nodes": []any{map[string]any{
					"id": "PRRC_4", "state": "COMMENTED", "createdAt": "2026-11-10T00:00:03Z",
					"url": nil, "body": "reply", "viewerCanUpdate": false, "viewerCanDelete": false,
					"author": nil,
				}},
			}}})
		}
		t.Fatalf("未预期的查询：%s", call.Query)
		return 500, "{}"
	})

	data, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false)
	if err != nil {
		t.Fatalf("分页读失败：%v", err)
	}
	if fixture.count() != 3 {
		t.Fatalf("应发 3 次请求（首页/thread 后续页/comment 后续页），实际 %d", fixture.count())
	}
	threads := data["threads"].([]any)
	if len(threads) != 2 {
		t.Fatalf("应读到 2 条 thread，实际 %v", threads)
	}
	comments := threads[1].(map[string]any)["comments"].([]any)
	if len(comments) != 2 {
		t.Fatalf("第二条 thread 应有 2 条评论，实际 %v", comments)
	}
	if data["truncated"] != false {
		t.Fatalf("读完整时应 truncated=false，实际 %v", data["truncated"])
	}
}

// TestGraphQLReviewStaleHead：head 动了必须是 official `stale`，不许当成
// "没有线程"或空列表。
func TestGraphQLReviewStaleHead(t *testing.T) {
	for _, name := range []string{"首页", "后续页"} {
		t.Run(name, func(t *testing.T) {
			_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
				switch gqlOp(call.Query) {
				case "review-first":
					if name == "首页" {
						return gqlData(t, reviewFirstPage(headB, false, nil, reviewThreadNode("PRRT_1")))
					}
					return gqlData(t, reviewFirstPage(headA, true, "TC_1", reviewThreadNode("PRRT_1")))
				case "threads-page":
					return gqlData(t, map[string]any{"node": map[string]any{
						"headRefOid": headB,
						"reviewThreads": map[string]any{
							"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
							"nodes":    []any{},
						},
					}})
				}
				t.Fatalf("未预期的查询：%s", call.Query)
				return 500, "{}"
			})
			data, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false)
			if err == nil {
				t.Fatalf("head 已移动时必须报错，实际返回 %v", data)
			}
			if code, _ := ForgeFailureOf(err); code != FailureStale {
				t.Fatalf("应是官方 stale，实际 %q（%v）", code, err)
			}
		})
	}
}

// TestGraphQLReviewPullRequestMissing：官方 not-found（不是空线程列表）。
func TestGraphQLReviewPullRequestMissing(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, map[string]any{
			"viewer": map[string]any{"login": "alice"},
			"repository": map[string]any{
				"isArchived": false, "viewerPermission": "WRITE",
				"pullRequest": nil,
			},
		})
	})
	if _, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false); err == nil {
		t.Fatal("PR 不存在时必须报错")
	} else if code, _ := ForgeFailureOf(err); code != FailureNotFound {
		t.Fatalf("应是官方 not-found，实际 %q（%v）", code, err)
	}
}

// TestGraphQLReviewGraphQLErrorsAreUnavailable：GitHub 的 errors[] 不许被
// 当成"没有线程"。
func TestGraphQLReviewGraphQLErrorsAreUnavailable(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlErrors(http.StatusOK, "INTERNAL", "Something went wrong")
	})
	if _, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false); err == nil {
		t.Fatal("GraphQL errors 必须报错")
	} else if code, _ := ForgeFailureOf(err); code != FailureUnavailable {
		t.Fatalf("应是官方 unavailable，实际 %q（%v）", code, err)
	}
}

// TestGraphQLReviewViewerCanReviewArchived：归档仓库/锁定会话的官方判据。
func TestGraphQLReviewViewerCanReviewArchived(t *testing.T) {
	cases := []struct {
		name    string
		archive bool
		locked  bool
		perm    string
		want    bool
	}{
		{"正常仓库", false, false, "READ", true},
		{"归档仓库", true, false, "ADMIN", false},
		{"锁定 + 只读", false, true, "READ", false},
		{"锁定 + 可写", false, true, "WRITE", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
				page := reviewFirstPage(headA, false, nil, reviewThreadNode("PRRT_1"))
				repo := page["repository"].(map[string]any)
				repo["isArchived"] = c.archive
				repo["viewerPermission"] = c.perm
				repo["pullRequest"].(map[string]any)["locked"] = c.locked
				return gqlData(t, page)
			})
			data, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false)
			if err != nil {
				t.Fatalf("读失败：%v", err)
			}
			if data["viewerCanReview"] != c.want {
				t.Fatalf("viewerCanReview 应为 %v，实际 %v", c.want, data["viewerCanReview"])
			}
		})
	}
}

// ---- 2. viewed 读 ----

// TestGraphQLViewedReadsMarks：官方 ForgeViewedFiles（states 映射 + 分页 + 截断）。
func TestGraphQLViewedReadsMarks(t *testing.T) {
	fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		op := gqlOp(call.Query)
		if op != "viewed-page" {
			t.Fatalf("应是 VIEWED_QUERY，实际 %s：%s", op, call.Query)
		}
		cursor, hasCursor := call.Variables["cursor"]
		if !hasCursor {
			if call.Variables["number"] != float64(7) || call.Variables["first"] != float64(100) {
				t.Fatalf("首页变量错：%v", call.Variables)
			}
			return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
				"headRefOid": headA,
				"files": map[string]any{
					"totalCount": 3,
					"pageInfo":   map[string]any{"hasNextPage": true, "endCursor": "FC_1"},
					"nodes": []any{
						map[string]any{"path": "a.go", "viewerViewedState": "VIEWED"},
						map[string]any{"path": "b.go", "viewerViewedState": "UNVIEWED"},
					},
				},
			}}})
		}
		if cursor != "FC_1" {
			t.Fatalf("第二页 cursor 错：%v", cursor)
		}
		return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": headA,
			"files": map[string]any{
				"totalCount": 3,
				"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": nil},
				"nodes": []any{
					map[string]any{"path": "c.go", "viewerViewedState": "DISMISSED"},
				},
			},
		}}})
	})

	viewed, err := provider.ChangeRequestViewed(context.Background(), gqlParams(rawURL), headA)
	if err != nil {
		t.Fatalf("viewed 读必须是真能力，实际失败：%v", err)
	}
	if fixture.count() != 2 {
		t.Fatalf("应读 2 页，实际 %d 次请求", fixture.count())
	}
	if viewed.HeadSHA != headA || viewed.Truncated {
		t.Fatalf("head/truncated 错：%+v", viewed)
	}
	want := map[string]string{"a.go": "viewed", "b.go": "unviewed", "c.go": "dismissed"}
	if len(viewed.Files) != len(want) {
		t.Fatalf("应读 3 个文件，实际 %v", viewed.Files)
	}
	for path, state := range want {
		if viewed.Files[path] != state {
			t.Fatalf("%s 的状态应是 %s，实际 %q", path, state, viewed.Files[path])
		}
	}
}

// TestGraphQLViewedStaleHead：每一页都要重新确认 head。
func TestGraphQLViewedStaleHead(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": headB,
			"files": map[string]any{
				"totalCount": 0,
				"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": nil},
				"nodes":      []any{},
			},
		}}})
	})
	if _, err := provider.ChangeRequestViewed(context.Background(), gqlParams(rawURL), headA); err == nil {
		t.Fatal("head 已移动时必须报错")
	} else if code, _ := ForgeFailureOf(err); code != FailureStale {
		t.Fatalf("应是官方 stale，实际 %q（%v）", code, err)
	}
}

// TestGraphQLViewedUnknownStateIsUnviewed：官方对 GitHub 新增状态的兜底。
func TestGraphQLViewedUnknownStateIsUnviewed(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": headA,
			"files": map[string]any{
				"totalCount": 1,
				"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": nil},
				"nodes":      []any{map[string]any{"path": "a.go", "viewerViewedState": "SOMETHING_NEW"}},
			},
		}}})
	})
	viewed, err := provider.ChangeRequestViewed(context.Background(), gqlParams(rawURL), headA)
	if err != nil {
		t.Fatalf("未知状态不该让整条读失败：%v", err)
	}
	if viewed.Files["a.go"] != "unviewed" {
		t.Fatalf("未知状态应按官方的 unviewed 兜底，实际 %q", viewed.Files["a.go"])
	}
}

// ---- 3. setViewed 写 ----

// TestGraphQLSetViewedMutations：先读 head+node id，再发 mark/unmark mutation。
func TestGraphQLSetViewedMutations(t *testing.T) {
	for _, viewed := range []bool{true, false} {
		name := "mark"
		wantOp := "mark"
		wantField := "markFileAsViewed"
		if !viewed {
			name, wantOp, wantField = "unmark", "unmark", "unmarkFileAsViewed"
		}
		t.Run(name, func(t *testing.T) {
			fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
				switch gqlOp(call.Query) {
				case "pr-node":
					return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
						"id": "PR_1", "headRefOid": headA,
					}}})
				case wantOp:
					if call.Variables["id"] != "PR_1" || call.Variables["path"] != "a.go" {
						t.Fatalf("mutation 变量错：%v", call.Variables)
					}
					if !strings.Contains(call.Query, wantField) {
						t.Fatalf("查询缺 %s：%s", wantField, call.Query)
					}
					return gqlData(t, map[string]any{wantField: map[string]any{"clientMutationId": nil}})
				}
				t.Fatalf("未预期的查询：%s", call.Query)
				return 500, "{}"
			})
			err := provider.ChangeRequestSetFileViewed(context.Background(), gqlParams(rawURL), "a.go", viewed, headA)
			if err != nil {
				t.Fatalf("%s 必须成功，实际 %v", name, err)
			}
			if fixture.count() != 2 {
				t.Fatalf("应是 1 读 + 1 写，实际 %d 次请求", fixture.count())
			}
		})
	}
}

// TestGraphQLSetViewedStaleHead：写前的 head 校验（官方 pullRequestNode）。
func TestGraphQLSetViewedStaleHead(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"id": "PR_1", "headRefOid": headB,
		}}})
	})
	err := provider.ChangeRequestSetFileViewed(context.Background(), gqlParams(rawURL), "a.go", true, headA)
	if err == nil {
		t.Fatal("head 已移动时必须报错")
	}
	failure := ActionFailureOf(err)
	if failure == nil || failure.Code != ActionStale {
		t.Fatalf("应是官方 stale，实际 %+v（%v）", failure, err)
	}
}

// TestGraphQLSetViewedMutationFailure：GitHub 拒绝 → failed；答复不确认 → unknown。
func TestGraphQLSetViewedMutationFailure(t *testing.T) {
	t.Run("拒绝→failed", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			if gqlOp(call.Query) == "pr-node" {
				return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
					"id": "PR_1", "headRefOid": headA,
				}}})
			}
			return gqlErrors(http.StatusOK, "UNPROCESSABLE", "path does not exist")
		})
		err := provider.ChangeRequestSetFileViewed(context.Background(), gqlParams(rawURL), "a.go", true, headA)
		if err == nil {
			t.Fatal("GitHub 拒绝时必须报错")
		}
		if failure := ActionFailureOf(err); failure == nil || failure.Code != ActionFailed {
			t.Fatalf("应是官方 failed（什么都没改），实际 %+v", failure)
		}
	})
	t.Run("5xx→unknown", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			if gqlOp(call.Query) == "pr-node" {
				return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
					"id": "PR_1", "headRefOid": headA,
				}}})
			}
			return 502, `{"message":"Bad Gateway"}`
		})
		err := provider.ChangeRequestSetFileViewed(context.Background(), gqlParams(rawURL), "a.go", true, headA)
		if err == nil {
			t.Fatal("5xx 必须报错")
		}
		if failure := ActionFailureOf(err); failure == nil || failure.Code != ActionUnknown {
			t.Fatalf("5xx 是错误的 → unknown（可能已生效），实际 %+v", failure)
		}
	})
}

// ---- 4. review 写 ----

// writeCheckData 造 WRITE_CHECK_QUERY 的应答。
func writeCheckData(pendingIDs ...string) map[string]any {
	nodes := make([]any, 0, len(pendingIDs))
	for _, id := range pendingIDs {
		nodes = append(nodes, map[string]any{"id": id, "viewerDidAuthor": true})
	}
	return map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"id": "PR_1", "headRefOid": headA,
		"reviews": map[string]any{"nodes": nodes},
	}}}
}

// TestGraphQLReviewWriteResolve：resolve / unresolve 的 mutation 与 union 形状。
func TestGraphQLReviewWriteResolve(t *testing.T) {
	for _, resolved := range []bool{true, false} {
		wantOp, wantField := "resolve", "resolveReviewThread"
		if !resolved {
			wantOp, wantField = "unresolve", "unresolveReviewThread"
		}
		t.Run(wantOp, func(t *testing.T) {
			fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
				switch gqlOp(call.Query) {
				case "write-target":
					return gqlData(t, map[string]any{
						"repository": writeCheckData()["repository"],
						"target":     map[string]any{"pullRequest": map[string]any{"id": "PR_1"}},
					})
				case wantOp:
					if call.Variables["thread"] != "PRRT_1" {
						t.Fatalf("mutation 变量错：%v", call.Variables)
					}
					return gqlData(t, map[string]any{wantField: map[string]any{"thread": map[string]any{"id": "PRRT_1"}}})
				}
				t.Fatalf("未预期的查询：%s", call.Query)
				return 500, "{}"
			})
			write, _ := json.Marshal(map[string]any{
				"headSha": headA, "kind": "resolve", "thread": "PRRT_1", "resolved": resolved,
			})
			result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
			if err != nil {
				t.Fatalf("写不许回 error（官方 union）：%v", err)
			}
			if !result.OK {
				t.Fatalf("resolve 应成功，实际 %+v", result)
			}
			if fixture.count() != 2 {
				t.Fatalf("应是 1 次校验 + 1 次 mutation，实际 %d", fixture.count())
			}
		})
	}
}

// TestGraphQLReviewWriteStaleHead：写前 head 校验 → 官方 union stale。
func TestGraphQLReviewWriteStaleHead(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		page := writeCheckData()
		page["repository"].(map[string]any)["pullRequest"].(map[string]any)["headRefOid"] = headB
		return gqlData(t, page)
	})
	write, _ := json.Marshal(map[string]any{"headSha": headA, "kind": "resolve", "thread": "PRRT_1", "resolved": true})
	result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
	if err != nil {
		t.Fatalf("写不许回 error：%v", err)
	}
	if result.OK || result.Code != "stale" {
		t.Fatalf("应是 union {ok:false,code:stale}，实际 %+v", result)
	}
}

// TestGraphQLReviewWriteReplyStartsPendingReview：pending=true 且没有待发布
// review 时，先开一个空的 pending review（官方 addPullRequestReview threads:null）。
func TestGraphQLReviewWriteReplyStartsPendingReview(t *testing.T) {
	var ops []string
	fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		op := gqlOp(call.Query)
		ops = append(ops, op)
		switch op {
		case "write-target":
			return gqlData(t, map[string]any{
				"repository": writeCheckData()["repository"],
				"target":     map[string]any{"pullRequest": map[string]any{"id": "PR_1"}},
			})
		case "add-review":
			if call.Variables["commit"] != headA || call.Variables["threads"] != nil {
				t.Fatalf("开空 pending review 的变量错：%v", call.Variables)
			}
			return gqlData(t, map[string]any{"addPullRequestReview": map[string]any{
				"pullRequestReview": map[string]any{"id": "PRR_new"},
			}})
		case "reply":
			if call.Variables["review"] != "PRR_new" || call.Variables["thread"] != "PRRT_1" ||
				call.Variables["body"] != "看一下" {
				t.Fatalf("reply mutation 变量错：%v", call.Variables)
			}
			return gqlData(t, map[string]any{"addPullRequestReviewThreadReply": map[string]any{
				"comment": map[string]any{"id": "PRRC_new"},
			}})
		}
		t.Fatalf("未预期的查询：%s", call.Query)
		return 500, "{}"
	})
	write, _ := json.Marshal(map[string]any{
		"headSha": headA, "kind": "reply", "thread": "PRRT_1", "body": "看一下", "pending": true,
	})
	result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
	if err != nil || !result.OK {
		t.Fatalf("pending 回复应成功，实际 %+v / %v", result, err)
	}
	if fixture.count() != 3 {
		t.Fatalf("应是 1 次校验 + 1 次开 review + 1 次回复，实际 %d（%v）", fixture.count(), ops)
	}
}

// TestGraphQLReviewWriteReplyRefusedWhilePendingReviewExists：GitHub 会把
// pending=false 的回复塞进已有的待发布 review，官方因此直接拒绝（failed）。
func TestGraphQLReviewWriteReplyRefusedWhilePendingReviewExists(t *testing.T) {
	fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, map[string]any{
			"repository": writeCheckData("PRR_pending")["repository"],
			"target":     map[string]any{"pullRequest": map[string]any{"id": "PR_1"}},
		})
	})
	write, _ := json.Marshal(map[string]any{
		"headSha": headA, "kind": "reply", "thread": "PRRT_1", "body": "看一下", "pending": false,
	})
	result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
	if err != nil {
		t.Fatalf("写不许回 error：%v", err)
	}
	if result.OK || result.Code != "failed" || !strings.Contains(result.Message, "pending review") {
		t.Fatalf("有 pending review 时的非 pending 回复必须 failed 且点名原因，实际 %+v", result)
	}
	if fixture.count() != 1 {
		t.Fatalf("被拒绝时不许发 mutation，实际 %d 次请求", fixture.count())
	}
}

// TestGraphQLReviewWriteAddPendingComment：官方 ForgeLineComment → DraftPullRequestReviewThread。
func TestGraphQLReviewWriteAddPendingComment(t *testing.T) {
	comment := map[string]any{"path": "a.go", "side": "R", "startLine": 4, "endLine": 6, "body": "这里要改"}
	t.Run("没有 pending review → addPullRequestReview(threads)", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			switch gqlOp(call.Query) {
			case "write-check":
				return gqlData(t, writeCheckData())
			case "add-review":
				threads, ok := call.Variables["threads"].([]any)
				if !ok || len(threads) != 1 {
					t.Fatalf("threads 变量形状错：%v", call.Variables["threads"])
				}
				thread := threads[0].(map[string]any)
				if thread["path"] != "a.go" || thread["body"] != "这里要改" ||
					thread["line"] != float64(6) || thread["side"] != "RIGHT" ||
					thread["startLine"] != float64(4) || thread["startSide"] != "RIGHT" {
					t.Fatalf("行评论字段错：%v", thread)
				}
				return gqlData(t, map[string]any{"addPullRequestReview": map[string]any{
					"pullRequestReview": map[string]any{"id": "PRR_new"},
				}})
			}
			t.Fatalf("未预期的查询：%s", call.Query)
			return 500, "{}"
		})
		write, _ := json.Marshal(map[string]any{"headSha": headA, "kind": "add-pending", "comment": comment})
		result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
		if err != nil || !result.OK {
			t.Fatalf("add-pending 应成功，实际 %+v / %v", result, err)
		}
	})
	t.Run("已有 pending review → addPullRequestReviewThread", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			switch gqlOp(call.Query) {
			case "write-check":
				return gqlData(t, writeCheckData("PRR_pending"))
			case "add-thread":
				if call.Variables["review"] != "PRR_pending" || call.Variables["path"] != "a.go" ||
					call.Variables["line"] != float64(6) || call.Variables["side"] != "RIGHT" {
					t.Fatalf("addPullRequestReviewThread 变量错：%v", call.Variables)
				}
				return gqlData(t, map[string]any{"addPullRequestReviewThread": map[string]any{
					"thread": map[string]any{"id": "PRRT_new"},
				}})
			}
			t.Fatalf("未预期的查询：%s", call.Query)
			return 500, "{}"
		})
		write, _ := json.Marshal(map[string]any{"headSha": headA, "kind": "add-pending", "comment": comment})
		result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
		if err != nil || !result.OK {
			t.Fatalf("add-thread 应成功，实际 %+v / %v", result, err)
		}
	})
}

// TestGraphQLReviewWriteSubmitAndDiscard：submit/discard 必须先确认 review 就是
// 当前用户的待发布 review，否则 failed（不是 stale）。
func TestGraphQLReviewWriteSubmitAndDiscard(t *testing.T) {
	t.Run("submit", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			switch gqlOp(call.Query) {
			case "write-check":
				return gqlData(t, writeCheckData("PRR_pending"))
			case "submit":
				if call.Variables["review"] != "PRR_pending" || call.Variables["event"] != "APPROVE" ||
					call.Variables["body"] != "LGTM" {
					t.Fatalf("submit 变量错：%v", call.Variables)
				}
				return gqlData(t, map[string]any{"submitPullRequestReview": map[string]any{
					"pullRequestReview": map[string]any{"id": "PRR_pending"},
				}})
			}
			t.Fatalf("未预期的查询：%s", call.Query)
			return 500, "{}"
		})
		write, _ := json.Marshal(map[string]any{
			"headSha": headA, "kind": "submit-pending", "review": "PRR_pending", "event": "approve", "body": "LGTM",
		})
		result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
		if err != nil || !result.OK {
			t.Fatalf("submit 应成功，实际 %+v / %v", result, err)
		}
	})
	t.Run("submit 的 review 已不是当前 pending", func(t *testing.T) {
		fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			return gqlData(t, writeCheckData("PRR_other"))
		})
		write, _ := json.Marshal(map[string]any{
			"headSha": headA, "kind": "submit-pending", "review": "PRR_pending", "event": "comment", "body": "",
		})
		result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
		if err != nil {
			t.Fatalf("写不许回 error：%v", err)
		}
		if result.OK || result.Code != "failed" {
			t.Fatalf("应是 failed，实际 %+v", result)
		}
		if fixture.count() != 1 {
			t.Fatalf("被拒绝时不许发 mutation，实际 %d", fixture.count())
		}
	})
	t.Run("discard", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			switch gqlOp(call.Query) {
			case "write-check":
				return gqlData(t, writeCheckData("PRR_pending"))
			case "discard":
				if call.Variables["review"] != "PRR_pending" {
					t.Fatalf("discard 变量错：%v", call.Variables)
				}
				return gqlData(t, map[string]any{"deletePullRequestReview": map[string]any{
					"pullRequestReview": map[string]any{"id": "PRR_pending"},
				}})
			}
			t.Fatalf("未预期的查询：%s", call.Query)
			return 500, "{}"
		})
		write, _ := json.Marshal(map[string]any{
			"headSha": headA, "kind": "discard-pending", "review": "PRR_pending",
		})
		result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
		if err != nil || !result.OK {
			t.Fatalf("discard 应成功，实际 %+v / %v", result, err)
		}
	})
}

// TestGraphQLReviewWriteEditAndDeleteComment：评论编辑/删除的 mutation。
func TestGraphQLReviewWriteEditAndDeleteComment(t *testing.T) {
	t.Run("edit-comment", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			switch gqlOp(call.Query) {
			case "write-target":
				return gqlData(t, map[string]any{
					"repository": writeCheckData()["repository"],
					"target":     map[string]any{"pullRequest": map[string]any{"id": "PR_1"}},
				})
			case "edit-comment":
				if call.Variables["comment"] != "PRRC_1" || call.Variables["body"] != "改好了" {
					t.Fatalf("edit-comment 变量错：%v", call.Variables)
				}
				return gqlData(t, map[string]any{"updatePullRequestReviewComment": map[string]any{
					"pullRequestReviewComment": map[string]any{"id": "PRRC_1"},
				}})
			}
			t.Fatalf("未预期的查询：%s", call.Query)
			return 500, "{}"
		})
		write, _ := json.Marshal(map[string]any{"headSha": headA, "kind": "edit-comment", "comment": "PRRC_1", "body": "改好了"})
		result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
		if err != nil || !result.OK {
			t.Fatalf("edit-comment 应成功，实际 %+v / %v", result, err)
		}
	})
	t.Run("delete-comment", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			switch gqlOp(call.Query) {
			case "write-target":
				return gqlData(t, map[string]any{
					"repository": writeCheckData()["repository"],
					"target":     map[string]any{"pullRequest": map[string]any{"id": "PR_1"}},
				})
			case "delete-comment":
				if call.Variables["comment"] != "PRRC_1" {
					t.Fatalf("delete-comment 变量错：%v", call.Variables)
				}
				return gqlData(t, map[string]any{"deletePullRequestReviewComment": map[string]any{
					"clientMutationId": nil,
				}})
			}
			t.Fatalf("未预期的查询：%s", call.Query)
			return 500, "{}"
		})
		write, _ := json.Marshal(map[string]any{"headSha": headA, "kind": "delete-comment", "comment": "PRRC_1"})
		result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
		if err != nil || !result.OK {
			t.Fatalf("delete-comment 应成功，实际 %+v / %v", result, err)
		}
	})
}

// TestGraphQLReviewWriteTargetNotInPullRequest：thread/comment 必须属于这个 PR。
func TestGraphQLReviewWriteTargetNotInPullRequest(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, map[string]any{
			"repository": writeCheckData()["repository"],
			"target":     map[string]any{"pullRequest": map[string]any{"id": "PR_OTHER"}},
		})
	})
	write, _ := json.Marshal(map[string]any{"headSha": headA, "kind": "resolve", "thread": "PRRT_1", "resolved": true})
	result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
	if err != nil {
		t.Fatalf("写不许回 error：%v", err)
	}
	if result.OK || result.Code != "failed" || !strings.Contains(result.Message, "is not a review thread") {
		t.Fatalf("别的 PR 的 thread 必须 failed，实际 %+v", result)
	}
}

// TestGraphQLReviewWriteMutationOutcomes：mutation 的 failed / unknown 分类。
func TestGraphQLReviewWriteMutationOutcomes(t *testing.T) {
	t.Run("GitHub 拒绝→failed", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			switch gqlOp(call.Query) {
			case "write-target":
				return gqlData(t, map[string]any{
					"repository": writeCheckData()["repository"],
					"target":     map[string]any{"pullRequest": map[string]any{"id": "PR_1"}},
				})
			}
			return gqlErrors(http.StatusOK, "FORBIDDEN", "Resource not accessible")
		})
		write, _ := json.Marshal(map[string]any{"headSha": headA, "kind": "resolve", "thread": "PRRT_1", "resolved": true})
		result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
		if err != nil {
			t.Fatalf("写不许回 error：%v", err)
		}
		if result.OK || result.Code != "failed" {
			t.Fatalf("GitHub 明确拒绝应是 failed，实际 %+v", result)
		}
	})
	t.Run("不确认结果→unknown", func(t *testing.T) {
		_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
			switch gqlOp(call.Query) {
			case "write-target":
				return gqlData(t, map[string]any{
					"repository": writeCheckData()["repository"],
					"target":     map[string]any{"pullRequest": map[string]any{"id": "PR_1"}},
				})
			}
			return gqlErrors(http.StatusOK, "INTERNAL", "Something went wrong while executing your query")
		})
		write, _ := json.Marshal(map[string]any{"headSha": headA, "kind": "resolve", "thread": "PRRT_1", "resolved": true})
		result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
		if err != nil {
			t.Fatalf("写不许回 error：%v", err)
		}
		if result.OK || result.Code != "unknown" {
			t.Fatalf("不确认结果应是 unknown，实际 %+v", result)
		}
	})
}

// ---- 5. 没有凭据：结构化的 unsupported（且不许出网）----

func TestGraphQLNoCredentialIsStructuredUnsupported(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		t.Fatalf("没有凭据时不许发出任何请求：%s", call.Query)
		return 500, "{}"
	})
	provider.Token = ""
	provider.TokenLookup = func() string { return "" }

	if _, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false); err == nil {
		t.Fatal("没有凭据时 review 读必须报错")
	} else if code, _ := ForgeFailureOf(err); code != FailureUnsupported {
		t.Fatalf("没有凭据应是官方 unsupported（501），实际 %q（%v）", code, err)
	}
	if _, err := provider.ChangeRequestViewed(context.Background(), gqlParams(rawURL), headA); err == nil {
		t.Fatal("没有凭据时 viewed 读必须报错")
	} else if code, _ := ForgeFailureOf(err); code != FailureUnsupported {
		t.Fatalf("没有凭据应是官方 unsupported（501），实际 %q（%v）", code, err)
	}
	if err := provider.ChangeRequestSetFileViewed(context.Background(), gqlParams(rawURL), "a.go", true, headA); err == nil {
		t.Fatal("没有凭据时 setViewed 必须报错")
	} else if failure := ActionFailureOf(err); failure == nil || failure.Code != ActionFailed {
		t.Fatalf("没有凭据的写应是官方 failed，实际 %+v", failure)
	}
	write, _ := json.Marshal(map[string]any{"headSha": headA, "kind": "resolve", "thread": "PRRT_1", "resolved": true})
	result, err := provider.ChangeRequestReviewWrite(context.Background(), gqlParams(rawURL), write)
	if err != nil {
		t.Fatalf("写不许回 error：%v", err)
	}
	if result.OK || result.Code != "unsupported" || !strings.Contains(result.Message, "GITHUB_TOKEN") {
		t.Fatalf("没有凭据应是 union {ok:false,code:unsupported} 且点名凭据，实际 %+v", result)
	}
	if fixture.count() != 0 {
		t.Fatalf("没有凭据时一次请求都不许发，实际 %d", fixture.count())
	}
}

// TestGraphQLCredentialChainUsesTokenLookup：env 没有令牌时走凭据链（生产是
// `gh auth token`；测试注入），并把令牌用在 Authorization 上。
func TestGraphQLCredentialChainUsesTokenLookup(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": headA,
			"files": map[string]any{
				"totalCount": 0,
				"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": nil},
				"nodes":      []any{},
			},
		}}})
	})
	provider.Token = ""
	provider.TokenLookup = func() string { return "cli-token" }
	if _, err := provider.ChangeRequestViewed(context.Background(), gqlParams(rawURL), headA); err != nil {
		t.Fatalf("凭据链可解析时必须能读：%v", err)
	}
	calls := fixture.snapshot()
	if len(calls) != 1 || calls[0].Auth != "Bearer cli-token" {
		t.Fatalf("应使用凭据链解析出的令牌，实际 %v", calls)
	}
}

// TestGraphQLCredentialMissingDoesNotTouchNetwork：没有凭据时连端点都不解析
// （不会误连 api.github.com），也不需要网络。
func TestGraphQLCredentialMissingDoesNotTouchNetwork(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	provider := &ChangeRequestProvider{Token: "", TokenLookup: func() string { return "" }}
	_, err := provider.ChangeRequestViewed(context.Background(), gqlParams("https://github.com/o/r/pull/7"), headA)
	if err == nil {
		t.Fatal("没有凭据必须报错")
	}
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != FailureUnsupported {
		t.Fatalf("应是结构化 unsupported（501），实际 %v", err)
	}
}

// ---- 6. 上限与截断（官方 maxReviewThreads / maxOutputBytes / maxViewedFiles）----

// TestGraphQLReviewTruncatesAtThreadLimit：到 maxReviewThreads 就收尾，并把
// "还有下一页"如实写成 truncated=true（不静默截断）。
func TestGraphQLReviewTruncatesAtThreadLimit(t *testing.T) {
	fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, reviewFirstPage(headA, true, "TC_1", reviewThreadNode("PRRT_1")))
	})
	provider.MaxReviewThreads = 1
	data, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false)
	if err != nil {
		t.Fatalf("读失败：%v", err)
	}
	if len(data["threads"].([]any)) != 1 || data["truncated"] != true {
		t.Fatalf("上限 1 时应只读 1 条且 truncated=true，实际 %v / %v", data["threads"], data["truncated"])
	}
	if fixture.count() != 1 {
		t.Fatalf("到上限后不该再翻页，实际 %d 次请求", fixture.count())
	}
	if fixture.snapshot()[0].Variables["first"] != float64(1) {
		t.Fatalf("首页 first 应被上限压到 1，实际 %v", fixture.snapshot()[0].Variables["first"])
	}
}

// TestGraphQLReviewFirstPageTooLarge：首页自己就超预算 → 官方 too-large（不给半页）。
func TestGraphQLReviewFirstPageTooLarge(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, reviewFirstPage(headA, false, nil, reviewThreadNode("PRRT_1")))
	})
	provider.MaxReviewBytes = 64
	if _, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false); err == nil {
		t.Fatal("首页超预算必须报错")
	} else if code, _ := ForgeFailureOf(err); code != FailureTooLarge {
		t.Fatalf("应是官方 too-large，实际 %q（%v）", code, err)
	}
}

// TestGraphQLViewedTruncatedAtLimit：到 maxViewedFiles 就收尾，totalCount 更大
// 时报 truncated=true。
func TestGraphQLViewedTruncatedAtLimit(t *testing.T) {
	fixture, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": headA,
			"files": map[string]any{
				"totalCount": 9,
				"pageInfo":   map[string]any{"hasNextPage": true, "endCursor": "FC_1"},
				"nodes": []any{
					map[string]any{"path": "a.go", "viewerViewedState": "VIEWED"},
					map[string]any{"path": "b.go", "viewerViewedState": "UNVIEWED"},
				},
			},
		}}})
	})
	provider.MaxViewedFiles = 2
	viewed, err := provider.ChangeRequestViewed(context.Background(), gqlParams(rawURL), headA)
	if err != nil {
		t.Fatalf("读失败：%v", err)
	}
	if len(viewed.Files) != 2 || !viewed.Truncated {
		t.Fatalf("上限 2 + totalCount 9：应 2 个文件且 truncated=true，实际 %+v", viewed)
	}
	if fixture.count() != 1 {
		t.Fatalf("到上限后不该再翻页，实际 %d 次请求", fixture.count())
	}
}

// TestGraphQLCLICredentialDisabledInTestBinary：`gh auth token` 兜底在测试进程里
// 默认关闭（否则 go test 会拿本机登录态真的出网），并可由环境变量覆盖。
func TestGraphQLCLICredentialDisabledInTestBinary(t *testing.T) {
	t.Setenv("DSHD_FORGE_CLI_CREDENTIAL", "")
	if cliCredentialEnabled() {
		t.Fatal("测试二进制里 cliCredentialEnabled 必须是 false（离线测试硬规则）")
	}
	t.Setenv("DSHD_FORGE_CLI_CREDENTIAL", "1")
	if !cliCredentialEnabled() {
		t.Fatal("显式置 1 时应启用")
	}
	t.Setenv("DSHD_FORGE_CLI_CREDENTIAL", "0")
	if cliCredentialEnabled() {
		t.Fatal("显式置 0 时应关闭")
	}
}

// ---- 7. 线上形状：provider 的 map 必须能直接反序列化成 transport 的官方结果类型 ----

// TestGraphQLReviewResultHasOfficialUnionShape：cmd/dshd 的适配器是
// "provider 交 map → json.Marshal → json.Unmarshal 到 transport.OfficialForgeReviewData"，
// 所以这里就按那条真实路径验一遍：字段名、指针/null、数组形状都必须对得上。
func TestGraphQLReviewResultHasOfficialUnionShape(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, reviewFirstPage(headA, false, nil, reviewThreadNode("PRRT_1")))
	})
	data, err := provider.ChangeRequestReview(context.Background(), gqlParams(rawURL), headA, false)
	if err != nil {
		t.Fatalf("读失败：%v", err)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("provider 结果无法序列化：%v", err)
	}
	var decoded transport.OfficialForgeReviewData
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("provider 结果不符合官方 ForgeReviewData 形状：%v（%s）", err, encoded)
	}
	if decoded.HeadSHA != headA || decoded.Viewer == nil || *decoded.Viewer != "alice" ||
		decoded.Truncated || !decoded.ViewerCanReview {
		t.Fatalf("顶层字段错：%+v", decoded)
	}
	if decoded.PendingReview == nil || decoded.PendingReview.ID != "PRR_pending" || decoded.PendingReview.Comments != 2 {
		t.Fatalf("pendingReview 错：%+v", decoded.PendingReview)
	}
	if len(decoded.Threads) != 1 {
		t.Fatalf("threads 错：%+v", decoded.Threads)
	}
	thread := decoded.Threads[0]
	if thread.ID != "PRRT_1" || thread.Path != "a.go" || thread.Side != "R" || thread.Subject != "line" ||
		thread.Resolved || !thread.ViewerCanReply || !thread.ViewerCanResolve {
		t.Fatalf("thread 字段错：%+v", thread)
	}
	if thread.Current == nil || thread.Current.Commit != headA || thread.Current.StartLine != 10 || thread.Current.EndLine != 12 {
		t.Fatalf("current 错：%+v", thread.Current)
	}
	if thread.Original == nil || thread.Original.Commit != headB || thread.Original.StartLine != 7 || thread.Original.EndLine != 9 {
		t.Fatalf("original 错：%+v", thread.Original)
	}
	if thread.DiffHunk == nil || *thread.DiffHunk != "@@ -1,3 +1,3 @@" {
		t.Fatalf("diffHunk 错：%+v", thread.DiffHunk)
	}
	if len(thread.Comments) != 2 {
		t.Fatalf("comments 错：%+v", thread.Comments)
	}
	first := thread.Comments[0]
	if first.Author == nil || first.Author.Login != "bob" || first.Author.Bot || first.Author.AvatarURL == nil ||
		first.Pending || !first.ViewerCanEdit || !first.ViewerCanDelete {
		t.Fatalf("评论字段错：%+v", first)
	}
	if second := thread.Comments[1]; !second.Pending || second.Author != nil || second.URL != nil {
		t.Fatalf("PENDING 评论/无作者评论错：%+v", second)
	}
}

// TestGraphQLViewedResultHasOfficialUnionShape：同上，验官方 ForgeViewedFiles。
func TestGraphQLViewedResultHasOfficialUnionShape(t *testing.T) {
	_, provider, rawURL := newGraphQLProvider(t, func(call gqlCall) (int, string) {
		return gqlData(t, map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": headA,
			"files": map[string]any{
				"totalCount": 2,
				"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": nil},
				"nodes": []any{
					map[string]any{"path": "a.go", "viewerViewedState": "VIEWED"},
					map[string]any{"path": "b.go", "viewerViewedState": "DISMISSED"},
				},
			},
		}}})
	})
	viewed, err := provider.ChangeRequestViewed(context.Background(), gqlParams(rawURL), headA)
	if err != nil {
		t.Fatalf("读失败：%v", err)
	}
	encoded, err := json.Marshal(viewed)
	if err != nil {
		t.Fatalf("结果无法序列化：%v", err)
	}
	var decoded transport.OfficialForgeViewedFiles
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("不符合官方 ForgeViewedFiles 形状：%v（%s）", err, encoded)
	}
	if decoded.HeadSHA != headA || decoded.Truncated || len(decoded.Files) != 2 ||
		decoded.Files["a.go"] != "viewed" || decoded.Files["b.go"] != "dismissed" {
		t.Fatalf("viewed 形状错：%+v", decoded)
	}
}
