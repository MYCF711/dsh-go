package forge

// rc.2 Forge provider 面的测试：全部走 local httptest（不联网），假的是
// GitHub REST 的**响应**，被验的是 provider 的真实请求路径、请求体、错误码与
// 快照字段。没有一条测试会因为没有上游数据而"跳过"。

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

// rc2Call 是一次被 fixture 看到的请求（断言路径/方法/请求体用）。
type rc2Call struct {
	Method string
	Path   string
	Accept string
	Body   string
}

// rc2Fixture 是假 GitHub REST：PR 元数据、commits、reviews、assignees、
// check-runs、compare diff，以及三个写端点。
type rc2Fixture struct {
	mu    sync.Mutex
	calls []rc2Call
}

func (f *rc2Fixture) record(r *http.Request) string {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	f.mu.Lock()
	f.calls = append(f.calls, rc2Call{Method: r.Method, Path: r.URL.Path, Accept: r.Header.Get("Accept"), Body: string(body)})
	f.mu.Unlock()
	return string(body)
}

func (f *rc2Fixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 7, "title": "rc2 provider", "state": "open", "draft": false,
			"html_url": "https://github.com/o/r/pull/7", "additions": 2, "deletions": 1,
			"mergeable": true, "mergeable_state": "behind", "updated_at": "2026-11-12T00:00:00Z",
			"user":                map[string]any{"login": "alice"},
			"head":                map[string]any{"ref": "feature", "sha": headA},
			"base":                map[string]any{"ref": "main", "sha": headB},
			"requested_reviewers": []any{map[string]any{"login": "dave", "avatar_url": "https://avatars/dave", "type": "Bot"}},
			"requested_teams":     []any{map[string]any{"slug": "platform", "name": "Platform", "organization": map[string]any{"login": "o"}}},
		})
	case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/commits":
		// GitHub 的 pulls/{n}/commits 就是 oldest first。
		_, _ = w.Write([]byte(`[
			{"sha":"1111111111111111111111111111111111111111","parents":[{"sha":"0000000000000000000000000000000000000000"}],
			 "commit":{"message":"first commit\n\nbody line","author":{"name":"Alice","date":"2026-11-10T01:02:03Z"}}},
			{"sha":"` + headA + `","parents":[{"sha":"1111111111111111111111111111111111111111"}],
			 "commit":{"message":"second commit","author":{"name":"Bob","date":"2026-11-11T04:05:06Z"}}}
		]`))
	case r.Method == http.MethodGet && path == "/repos/o/r/pulls/7/reviews":
		// 升序：bob 先 PENDING（未提交，不算评审），再 APPROVED；alice 是作者。
		_, _ = w.Write([]byte(`[
			{"state":"PENDING","commit_id":"` + headA + `","user":{"login":"bob","type":"User"}},
			{"state":"APPROVED","commit_id":"` + headA + `","user":{"login":"bob","type":"User","avatar_url":"https://avatars/bob"}},
			{"state":"APPROVED","commit_id":"` + headA + `","user":{"login":"alice","type":"User"}}
		]`))
	case r.Method == http.MethodGet && path == "/repos/o/r/assignees":
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"login": "alice", "name": "Alice A", "avatar_url": "https://avatars/alice"},
			{"login": "bob", "name": "Alan Bob", "avatar_url": "https://avatars/bob"},
			{"login": "carol", "name": "Carol"},
		})
	case r.Method == http.MethodGet && path == "/repos/o/r/commits/"+headA+"/check-runs":
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "check_runs": []map[string]any{
			{"id": 42, "name": "ci", "status": "completed", "conclusion": "failure",
				"details_url": "https://ci.example/42", "started_at": "2026-11-12T00:00:00Z", "completed_at": "2026-11-12T00:01:00Z",
				"check_suite": map[string]any{"name": "build"}},
			{"id": 43, "name": "lint", "status": "completed", "conclusion": "success",
				"details_url": "https://ci.example/43", "started_at": "2026-11-12T00:00:00Z", "completed_at": "2026-11-12T00:01:00Z",
				"check_suite": map[string]any{"name": "build"}},
		}})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/o/r/compare/"):
		if !strings.Contains(r.Header.Get("Accept"), "diff") {
			http.Error(w, "compare 必须带 diff Accept", http.StatusNotAcceptable)
			return
		}
		_, _ = w.Write([]byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n-old\n+new\n context\n"))
	case r.Method == http.MethodGet && path == "/repos/o/r":
		_, _ = w.Write([]byte(`{"allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":false}`))
	case r.Method == http.MethodPost && path == "/repos/o/r/pulls/7/requested_reviewers":
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7}`))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/rerequest"):
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPut && path == "/repos/o/r/pulls/7/update-branch":
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"message":"Updating pull request branch."}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *rc2Fixture) snapshot() []rc2Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rc2Call(nil), f.calls...)
}

// callsFor 数出匹配 method+path 的请求（path 用 HasPrefix / HasSuffix 判）。
func (f *rc2Fixture) callsFor(method, path string) []rc2Call {
	matched := []rc2Call{}
	for _, call := range f.snapshot() {
		if call.Method == method && call.Path == path {
			matched = append(matched, call)
		}
	}
	return matched
}

func newRC2Fixture(t *testing.T) (*rc2Fixture, *ChangeRequestProvider, string) {
	t.Helper()
	fixture := &rc2Fixture{}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	provider := &ChangeRequestProvider{BaseURL: server.URL, Token: "test-token", HTTP: server.Client()}
	return fixture, provider, "https://github.com/o/r/pull/7"
}

// ---- 1. 错误 → 官方码 ----

func TestRC2FailureCodes(t *testing.T) {
	if code, message := ForgeFailureOf(nil); code != "" || message != "" {
		t.Fatalf("nil 应返回空码，实际 (%q,%q)", code, message)
	}
	if ActionFailureOf(nil) != nil {
		t.Fatalf("nil 应返回 nil ActionFailure")
	}
	cases := []struct {
		name       string
		err        error
		wantRead   string
		wantAction string
	}{
		{"stale_head", &ForgeHTTPError{Status: 409, Code: "stale_head", Message: "head moved"}, FailureStale, ActionStale},
		{"auth_required", &ForgeHTTPError{Status: 401, Code: "auth_required", Message: "login"}, FailureUnauthenticated, ActionUnauthenticated},
		{"provider_unavailable", &ForgeHTTPError{Status: 502, Code: "provider_unavailable", Message: "network"}, FailureUnavailable, ActionUnknown},
		{"unsupported", Unsupported("viewedFiles"), FailureUnsupported, ActionFailed},
		{"not_found", &ForgeHTTPError{Status: 404, Code: "not_found", Message: "missing"}, FailureNotFound, ActionFailed},
		{"invalid_spec", &ForgeHTTPError{Status: 422, Code: "invalid_spec", Message: "refused"}, FailureUnavailable, ActionFailed},
	}
	for _, c := range cases {
		code, message := ForgeFailureOf(c.err)
		if code != c.wantRead {
			t.Fatalf("%s: ForgeFailureOf code = %q，期望 %q", c.name, code, c.wantRead)
		}
		if message == "" {
			t.Fatalf("%s: ForgeFailure 必须带 message", c.name)
		}
		action := ActionFailureOf(c.err)
		if action == nil || action.Code != c.wantAction {
			t.Fatalf("%s: ActionFailureOf = %+v，期望 code=%q", c.name, action, c.wantAction)
		}
		if action.Message == "" {
			t.Fatalf("%s: ActionFailure 必须带 message", c.name)
		}
	}
	// 非 ForgeHTTPError 也必须给码，不许出现"没有码的失败"。
	if code, _ := ForgeFailureOf(context.Canceled); code != FailureUnavailable {
		t.Fatalf("非 provider 错误的 code = %q，期望 unavailable", code)
	}
	if !IsForgeCode(&ForgeHTTPError{Code: "stale_head"}, "stale_head") || IsForgeCode(nil, "stale_head") {
		t.Fatal("IsForgeCode 判定不对")
	}
}

// ---- 2. commits ----

func TestChangeRequestCommits(t *testing.T) {
	fixture, provider, rawURL := newRC2Fixture(t)
	ctx := context.Background()
	list, err := provider.ChangeRequestCommits(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL})
	if err != nil {
		t.Fatalf("ChangeRequestCommits: %v", err)
	}
	if list.HeadSHA != headA || list.Truncated {
		t.Fatalf("head/truncated 不对：%+v", list)
	}
	if len(list.Commits) != 2 {
		t.Fatalf("commits = %+v", list.Commits)
	}
	first, second := list.Commits[0], list.Commits[1]
	// oldest first：第一条必须是最早的那个 sha。
	if first.SHA != "1111111111111111111111111111111111111111" || second.SHA != headA {
		t.Fatalf("顺序不是 oldest first：%+v", list.Commits)
	}
	if first.Parent == nil || *first.Parent != "0000000000000000000000000000000000000000" {
		t.Fatalf("first.Parent = %v", first.Parent)
	}
	if first.Title != "first commit" {
		t.Fatalf("title 不是 message 第一行：%q", first.Title)
	}
	if first.Author == nil || *first.Author != "Alice" || first.AuthoredAt != "2026-11-10T01:02:03Z" {
		t.Fatalf("author/authoredAt 不对：%+v", first)
	}
	if second.Parent == nil || *second.Parent != first.SHA {
		t.Fatalf("second.Parent = %v", second.Parent)
	}
	if len(fixture.callsFor(http.MethodGet, "/repos/o/r/pulls/7/commits")) != 1 {
		t.Fatalf("没有打到 pulls/{n}/commits：%+v", fixture.snapshot())
	}
	// head 已移动 → 409 stale_head，不发列表请求。
	if _, err := provider.ChangeRequestCommits(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headB}); !IsForgeCode(err, "stale_head") {
		t.Fatalf("head 不匹配应 stale_head，实际 %v", err)
	}
}

// ---- 3. reviewer candidates ----

func TestChangeRequestReviewerCandidates(t *testing.T) {
	_, provider, rawURL := newRC2Fixture(t)
	ctx := context.Background()
	all, err := provider.ChangeRequestReviewerCandidates(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, "")
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("空 query 应不过滤：%+v", all)
	}
	if all[0].Suggested {
		t.Fatal("REST assignees 拿不到建议，Suggested 必须 false")
	}
	matched, err := provider.ChangeRequestReviewerCandidates(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, "AL")
	if err != nil {
		t.Fatalf("candidates(AL): %v", err)
	}
	logins := []string{}
	for _, candidate := range matched {
		logins = append(logins, candidate.Login)
	}
	if len(matched) != 2 || logins[0] != "alice" || logins[1] != "bob" {
		t.Fatalf(`query "AL" 应只留 login/name 含 al 的 alice/bob，实际 %v`, logins)
	}
	if matched[1].Name == nil || *matched[1].Name != "Alan Bob" {
		t.Fatalf("name 投影不对：%+v", matched[1])
	}
	if matched[1].AvatarURL == nil || *matched[1].AvatarURL != "https://avatars/bob" {
		t.Fatalf("avatarUrl 投影不对：%+v", matched[1])
	}
}

// ---- 4. rerun checks ----

func TestChangeRequestRerunChecks(t *testing.T) {
	fixture, provider, rawURL := newRC2Fixture(t)
	ctx := context.Background()
	if err := provider.ChangeRequestRerunChecks(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, headB); !IsForgeCode(err, "stale_head") {
		t.Fatalf("head 不匹配应 stale_head，实际 %v", err)
	}
	for _, call := range fixture.snapshot() {
		if call.Method == http.MethodPost {
			t.Fatalf("stale 时不许发写请求：%+v", call)
		}
	}
	if err := provider.ChangeRequestRerunChecks(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, headA); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	rerequests := []rc2Call{}
	for _, call := range fixture.snapshot() {
		if call.Method == http.MethodPost && strings.HasSuffix(call.Path, "/rerequest") {
			rerequests = append(rerequests, call)
		}
	}
	if len(rerequests) != 1 || rerequests[0].Path != "/repos/o/r/check-runs/42/rerequest" {
		t.Fatalf("只应重跑失败项 42，实际 %+v", rerequests)
	}
	if !strings.HasPrefix(rerequests[0].Accept, "application/vnd.github") {
		t.Fatalf("重跑请求 Accept 不对：%+v", rerequests[0])
	}
}

// ---- 5. update branch ----

func TestChangeRequestUpdateBranch(t *testing.T) {
	fixture, provider, rawURL := newRC2Fixture(t)
	ctx := context.Background()
	if err := provider.ChangeRequestUpdateBranch(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, "rebase", headA); err != nil {
		t.Fatalf("updateBranch: %v", err)
	}
	puts := fixture.callsFor(http.MethodPut, "/repos/o/r/pulls/7/update-branch")
	if len(puts) != 1 {
		t.Fatalf("应发一次 PUT update-branch，实际 %+v", fixture.snapshot())
	}
	if !strings.Contains(puts[0].Body, `"update_method":"rebase"`) {
		t.Fatalf("update-branch body = %q", puts[0].Body)
	}
	if err := provider.ChangeRequestUpdateBranch(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, "squash", headA); !IsForgeCode(err, "invalid_spec") {
		t.Fatalf("非法 method 应 invalid_spec，实际 %v", err)
	}
	if err := provider.ChangeRequestUpdateBranch(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, "merge", headB); !IsForgeCode(err, "stale_head") {
		t.Fatalf("head 不匹配应 stale_head，实际 %v", err)
	}
}

// ---- 6. GraphQL-only 能力：结构化 unsupported ----

// noForgeCredential 把 provider 钉成"没有 GraphQL 凭据"：不读本机 `gh` 登录态，
// 也不读环境变量。这样"没有凭据"的分支在任何机器上都是确定的，且**不会出网**。
func noForgeCredential(t *testing.T, provider *ChangeRequestProvider) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	provider.Token = ""
	provider.TokenLookup = func() string { return "" }
}

// TestChangeRequestViewedWithoutCredential：没有 GraphQL 凭据时，viewed 读仍是
// 结构化 unsupported（501），**不许**返回空 Files 冒充"没有已读文件"，也不许出网。
//
// 有凭据时的真读（viewerViewedState + 分页 + head 校验）由 graphql_test.go 的
// TestGraphQLViewedReadsMarks 覆盖——那条用例证明这不再是"未实现"。
func TestChangeRequestViewedWithoutCredential(t *testing.T) {
	_, provider, rawURL := newRC2Fixture(t)
	noForgeCredential(t, provider)
	ctx := context.Background()
	viewed, err := provider.ChangeRequestViewed(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, headA)
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotImplemented || httpErr.Code != FailureUnsupported {
		t.Fatalf("没有凭据时 viewedFiles 应 501 unsupported，实际 %v", err)
	}
	if code, _ := ForgeFailureOf(err); code != FailureUnsupported {
		t.Fatalf("ForgeFailureOf = %q，期望 unsupported", code)
	}
	// 不许返回空 Files 冒充"没有已读文件"。
	if viewed.HeadSHA != "" || len(viewed.Files) != 0 || viewed.Truncated {
		t.Fatalf("unsupported 不许返回数据：%+v", viewed)
	}
	// 写侧：请求根本没发出去 → 也是 unsupported（ActionFailureOf 折成官方 failed）。
	if err := provider.ChangeRequestSetFileViewed(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, "a.go", true, headA); !IsForgeCode(err, FailureUnsupported) {
		t.Fatalf("没有凭据时 setViewed 应 unsupported，实际 %v", err)
	}
}

// TestChangeRequestReviewWithoutCredential：没有 GraphQL 凭据时，review 读是
// 结构化 unsupported（官方 review-unsupported 的来源），写是官方 union 的
// unsupported 分支——两者都不许变成 Go error，也不许返回空线程表。
//
// 有凭据时的真读/真写由 graphql_test.go 的 TestGraphQLReviewReadsThreads /
// TestGraphQLReviewWriteResolve 等覆盖（review threads 走 GraphQL、7 个 kind 都发
// 对应 mutation），那组用例证明这不再是"未实现"。
func TestChangeRequestReviewWithoutCredential(t *testing.T) {
	_, provider, rawURL := newRC2Fixture(t)
	noForgeCredential(t, provider)
	ctx := context.Background()
	review, err := provider.ChangeRequestReview(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, headA, false)
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotImplemented || httpErr.Code != FailureUnsupported {
		t.Fatalf("没有凭据时 readReview 应 501 unsupported，实际 %v", err)
	}
	// nil,nil 会被上层当成"没有 thread"（假数据），必须禁止。
	if review != nil {
		t.Fatalf("unsupported 必须返回 nil review，实际 %+v", review)
	}
	write := json.RawMessage(`{"headSha":"` + headA + `","kind":"resolve","thread":"PRRT_1","resolved":true}`)
	result, err := provider.ChangeRequestReviewWrite(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}, write)
	if err != nil {
		t.Fatalf("writeReview 的 unsupported 走官方 union，不应是 Go error：%v", err)
	}
	if result.OK || result.Code != "unsupported" || result.Message == "" {
		t.Fatalf("writeReview union = %+v，期望 {ok:false,code:unsupported}", result)
	}
	if code, _ := ForgeFailureOf(Unsupported("writeReview")); code != FailureUnsupported {
		t.Fatalf("unsupported 码漂移：%q", code)
	}
}

// ---- 7. 快照新字段 ----

func TestSnapshotRC2Fields(t *testing.T) {
	_, provider, rawURL := newRC2Fixture(t)
	snapshot, err := provider.ChangeRequestChecks(context.Background(), transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headA})
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	if snapshot.BaseSHA == nil || *snapshot.BaseSHA != headB {
		t.Fatalf("baseSha = %v，期望 %q", snapshot.BaseSHA, headB)
	}
	if len(snapshot.BranchUpdates) != 2 || snapshot.BranchUpdates[0] != "merge" || snapshot.BranchUpdates[1] != "rebase" {
		t.Fatalf("mergeable_state=behind + open 应给 merge+rebase，实际 %v", snapshot.BranchUpdates)
	}
	if !snapshot.CanRequestReviews {
		t.Fatal("open + token 应 canRequestReviews=true")
	}
	// 这台桩没有 /graphql：GraphQL 不可用 → 退回 REST 下界（unresolvedThreads=0
	// 且 threadsTruncated=true）。真值路径由 graphql_checks_test.go 覆盖。
	if !snapshot.ThreadsTruncated {
		t.Fatal("GraphQL 不可用时 threadsTruncated 必须 true（unresolvedThreads 只是下界）")
	}
	if snapshot.UnresolvedThreads != 0 {
		t.Fatalf("GraphQL 不可用时 unresolvedThreads 只能是下界 0，实际 %d", snapshot.UnresolvedThreads)
	}
	if snapshot.ReviewersTruncated {
		t.Fatal("reviews/requested_reviewers 一次拿全，不该声称截断")
	}
	if !snapshot.CanRerunChecks {
		t.Fatalf("存在可重跑失败项且无 pending 时应 canRerunChecks=true：%+v", snapshot.Checks)
	}
	if len(snapshot.ClosingIssues) != 0 || snapshot.ClosingIssuesTruncated {
		t.Fatalf("REST 无 closingIssuesReferences：应空数组 + false，实际 %+v", snapshot.ClosingIssues)
	}

	// reviewers：requested reviewer（bot）→ requested team → 已提交评审者；作者不在内。
	if len(snapshot.Reviewers) != 3 {
		t.Fatalf("reviewers 数量不对：%+v", snapshot.Reviewers)
	}
	dave := snapshot.Reviewers[0]
	if dave.Kind != "bot" || dave.Login != "dave" || !dave.Requested || dave.Verdict != nil || dave.ReviewedSHA != nil {
		t.Fatalf("requested bot 投影不对：%+v", dave)
	}
	team := snapshot.Reviewers[1]
	if team.Kind != "team" || team.Login != "o/platform" || !team.Requested {
		t.Fatalf("requested team 投影不对：%+v", team)
	}
	bob := snapshot.Reviewers[2]
	if bob.Kind != "user" || bob.Login != "bob" || bob.Requested {
		t.Fatalf("已提交评审者投影不对：%+v", bob)
	}
	if bob.Verdict == nil || *bob.Verdict != "approved" {
		t.Fatalf("最新一条评审是 APPROVED，verdict 应 approved：%+v", bob.Verdict)
	}
	if bob.ReviewedSHA == nil || *bob.ReviewedSHA != headA {
		t.Fatalf("reviewedSha 应是最新 review 的 commit，实际 %v", bob.ReviewedSHA)
	}
	for _, reviewer := range snapshot.Reviewers {
		if strings.EqualFold(reviewer.Login, "alice") {
			t.Fatalf("作者不许出现在 reviewers 里：%+v", reviewer)
		}
	}

	// checks[].rerun：失败项带身份，成功项 null。
	if len(snapshot.Checks) != 2 {
		t.Fatalf("checks = %+v", snapshot.Checks)
	}
	if snapshot.Checks[0].Rerun == nil || *snapshot.Checks[0].Rerun != "42" {
		t.Fatalf("失败 check 应带 rerun=42，实际 %+v", snapshot.Checks[0])
	}
	if snapshot.Checks[1].Rerun != nil {
		t.Fatalf("成功 check 不该带 rerun：%+v", snapshot.Checks[1])
	}
}

// ---- 8. diff 必须带 head（compare 路径 + stale）----

func TestChangeRequestDiffStaleHead(t *testing.T) {
	fixture, provider, rawURL := newRC2Fixture(t)
	ctx := context.Background()
	_, err := provider.ChangeRequestDiff(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headB})
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusConflict || httpErr.Code != "stale_head" {
		t.Fatalf("head 不匹配应 409 stale_head，实际 %v", err)
	}
	for _, call := range fixture.snapshot() {
		if strings.Contains(call.Path, "/compare/") {
			t.Fatalf("stale 时不许读 compare：%+v", call)
		}
	}
	diff, err := provider.ChangeRequestDiff(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headA})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if diff.HeadSHA != headA || len(diff.Files) != 1 || diff.Files[0].Path != "a.go" {
		t.Fatalf("diff 结果不对：%+v", diff)
	}
	compares := []rc2Call{}
	for _, call := range fixture.snapshot() {
		if strings.Contains(call.Path, "/compare/") {
			compares = append(compares, call)
		}
	}
	if len(compares) != 1 {
		t.Fatalf("应走一次 compare，实际 %+v", compares)
	}
	wantPath := "/repos/o/r/compare/" + headB + "..." + headA
	if compares[0].Path != wantPath {
		t.Fatalf("compare 路径 = %q，期望 %q", compares[0].Path, wantPath)
	}
	if !strings.Contains(compares[0].Accept, "diff") {
		t.Fatalf("compare 必须带 diff Accept：%+v", compares[0])
	}
	// ChangeRequestDiffAtHead：headSha 必填，否则 invalid_spec。
	if _, err := provider.ChangeRequestDiffAtHead(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL}); !IsForgeCode(err, "invalid_spec") {
		t.Fatalf("diffAtHead 缺 headSha 应 invalid_spec，实际 %v", err)
	}
	atHead, err := provider.ChangeRequestDiffAtHead(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headA})
	if err != nil || atHead.HeadSHA != headA || len(atHead.Files) != 1 {
		t.Fatalf("diffAtHead = %+v, err=%v", atHead, err)
	}
	if _, err := provider.ChangeRequestDiffAtHead(ctx, transport.OfficialForgeChangeRequestParams{URL: rawURL, HeadSHA: headB}); !IsForgeCode(err, "stale_head") {
		t.Fatalf("diffAtHead 在旧 head 应 stale_head，实际 %v", err)
	}
}

// ---- 9. 合同锁：方法签名必须与父代理 serve 层要调用的形状一致 ----
//
// 这些断言只做编译期校验（任何签名漂移都会让本包直接编译失败），不产生运行时
// 开销，也不假装测了行为。
var (
	_ func(context.Context, transport.OfficialForgeChangeRequestParams) (CommitList, error)                               = (*ChangeRequestProvider)(nil).ChangeRequestCommits
	_ func(context.Context, transport.OfficialForgeChangeRequestParams, string) ([]ReviewerCandidate, error)              = (*ChangeRequestProvider)(nil).ChangeRequestReviewerCandidates
	_ func(context.Context, transport.OfficialForgeChangeRequestParams, []ReviewerRef) error                              = (*ChangeRequestProvider)(nil).ChangeRequestRequestReviews
	_ func(context.Context, transport.OfficialForgeChangeRequestParams, string) error                                     = (*ChangeRequestProvider)(nil).ChangeRequestRerunChecks
	_ func(context.Context, transport.OfficialForgeChangeRequestParams, string, string) error                             = (*ChangeRequestProvider)(nil).ChangeRequestUpdateBranch
	_ func(context.Context, transport.OfficialForgeChangeRequestParams, string) (ViewedFiles, error)                      = (*ChangeRequestProvider)(nil).ChangeRequestViewed
	_ func(context.Context, transport.OfficialForgeChangeRequestParams, string, bool, string) error                       = (*ChangeRequestProvider)(nil).ChangeRequestSetFileViewed
	_ func(context.Context, transport.OfficialForgeChangeRequestParams, string, bool) (map[string]any, error)             = (*ChangeRequestProvider)(nil).ChangeRequestReview
	_ func(context.Context, transport.OfficialForgeChangeRequestParams, json.RawMessage) (ReviewWriteResult, error)       = (*ChangeRequestProvider)(nil).ChangeRequestReviewWrite
	_ func(context.Context, transport.OfficialForgeChangeRequestParams) (transport.OfficialForgeChangeRequestDiff, error) = (*ChangeRequestProvider)(nil).ChangeRequestDiffAtHead
	_ func(error) (string, string)                                                                                        = ForgeFailureOf
	_ func(error) *ActionFailure                                                                                          = ActionFailureOf
	_ func(string) error                                                                                                  = Unsupported
	_ func(error, string) bool                                                                                            = IsForgeCode
)
