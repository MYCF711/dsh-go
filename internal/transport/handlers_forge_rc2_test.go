package transport

// rc.2 Forge 面 12 个方法的契约测试。
//
// 判据分四层，缺一层就会出现"看起来实现了"的假象：
//  1. 方法名集合与路由：12 个名字必须被 IsOfficialForgeRC2Method 与
//     IsOfficialForgeLiveshareMethod 同时认领（否则 server.go:588 那条分支会漏掉），
//     且不能与 rc.1 的 31 个方法重叠。
//  2. 参数 fail-loud：官方 strict 契约下，缺字段/未知字段/类型错/枚举错
//     必须是 400 bad_request + 四字段，而且**早于**能力判断（GUI 拼错参数
//     得到的应该是能改的错误）。
//  3. 结果形状：逐方法对照官方 jsonSchema（含 nullable、union、数组不为 null）。
//  4. 三种"没有数据"必须可区分：null / union code / 501 官方码，
//     不许任何一个退化成"200 + 空对象"。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// forgeRC2GoodBodies 是 12 个方法按**官方位置参数名**写的最小合法请求体。
//
// 键名逐条抄自 rc.2 typert 描述符的 `parameters[].name`（见 docs/evidence/
// official-method-contract-delta-0.2.2-rc.2.md §1），不按本文件代码生成——
// 否则"表写错了"和"测试写错了"会一起错。
func forgeRC2GoodBodies() map[Method]string {
	return map[Method]string{
		MethodForgePanelChangeRequestCommits: `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7"}`,
		MethodForgePanelChangeRequestReview: `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7",` +
			`"headSha":"9f2c","fresh":false}`,
		MethodForgePanelChangeRequestReviewWrite: `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7",` +
			`"write":{"headSha":"9f2c","kind":"resolve","thread":"T1","resolved":true}}`,
		MethodForgePanelChangeRequestViewed: `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7",` +
			`"headSha":"9f2c"}`,
		MethodForgePanelDiffBases:          `{"sessionId":"s-1","query":""}`,
		MethodForgePanelLocalCommits:       `{"sessionId":"s-1","base":null}`,
		MethodForgePanelRequestReviews:     `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","reviewers":[{"kind":"user","login":"octo"}]}`,
		MethodForgePanelRerunChecks:        `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"headSha":"9f2c"}}`,
		MethodForgePanelReviewerCandidates: `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","query":"oc"}`,
		MethodForgePanelSetFileViewed:      `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"path":"a.go","viewed":true,"headSha":"9f2c"}}`,
		MethodForgePanelUpdateBranch:       `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"rebase","headSha":"9f2c"}`,
		MethodForgePanelWorktreeCleanup:    `{"sessionId":"s-1","revision":null}`,
	}
}

// forgeRC2Call 走**真实分派路径**（serveOfficialForgeLiveshareAPI），不是直接调
// ServeOfficialForgeRC2——否则"接线漏了"这一层永远测不到。
func forgeRC2Call(t *testing.T, srv *Server, method Method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+string(method), strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.serveOfficialForgeLiveshareAPI(rec, req, string(method))
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 的响应不是 JSON 对象: %q", method, rec.Body.String())
		}
	}
	return rec.Code, out
}

// TestForgeRC2MethodsAreStable 钉住 rc.2 新增的 12 个方法名与路由。
func TestForgeRC2MethodsAreStable(t *testing.T) {
	methods := OfficialForgeRC2Methods()
	want := []string{
		"forgePanel.changeRequestCommits", "forgePanel.changeRequestReview",
		"forgePanel.changeRequestReviewWrite", "forgePanel.changeRequestViewed",
		"forgePanel.diffBases", "forgePanel.localCommits", "forgePanel.requestReviews",
		"forgePanel.rerunChecks", "forgePanel.reviewerCandidates", "forgePanel.setFileViewed",
		"forgePanel.updateBranch", "forgePanel.worktreeCleanup",
	}
	if len(methods) != len(want) {
		t.Fatalf("rc.2 新增方法应是 %d 个，实际 %d", len(want), len(methods))
	}
	got := make([]string, 0, len(methods))
	seen := map[Method]bool{}
	for _, m := range methods {
		if seen[m] {
			t.Fatalf("方法重复：%s", m)
		}
		seen[m] = true
		got = append(got, string(m))
		if !IsOfficialForgeRC2Method(string(m)) {
			t.Fatalf("%s 没被 IsOfficialForgeRC2Method 认出来", m)
		}
		if !IsOfficialForgeLiveshareMethod(string(m)) {
			t.Fatalf("%s 没被 IsOfficialForgeLiveshareMethod 认出来（server.go 的分派会漏）", m)
		}
		if IsOfficialForgeChangeRequestMethod(string(m)) {
			t.Fatalf("%s 被 rc.1 的 Change Request 读取集合认领了（会走错分派）", m)
		}
	}
	sort.Strings(got)
	expected := append([]string(nil), want...)
	sort.Strings(expected)
	if strings.Join(got, "|") != strings.Join(expected, "|") {
		t.Fatalf("方法集合不一致：\n got=%v\nwant=%v", got, expected)
	}
	// rc.1 的 31 个集合不能被污染：那组驱动"逐方法未实现表"，混进这里有真实
	// 契约层的名字会让"每个都回 501"的判据自相矛盾。
	for _, legacy := range OfficialForgeLiveshareMethods() {
		if seen[legacy] {
			t.Fatalf("%s 同时在 rc.1 未实现表与 rc.2 契约层里", legacy)
		}
	}
	// 不在口径里的名字必须不被认领。
	for _, out := range []string{"forgePanel.unknown", "forgePanel.changeRequestViewedX", ""} {
		if IsOfficialForgeRC2Method(out) {
			t.Fatalf("%q 不属于 rc.2 的 12 个方法，却被认领了", out)
		}
	}
}

// TestForgeRC2NotAttachedReturns501：整表没挂时，12 个方法都必须是 501 +
// not_implemented + 四字段 + 点名缺什么，绝不能是 404 / 200 / 空载荷。
func TestForgeRC2NotAttachedReturns501(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	bodies := forgeRC2GoodBodies()
	if len(bodies) != len(OfficialForgeRC2Methods()) {
		t.Fatalf("测试数据应覆盖全部方法，实际 %d", len(bodies))
	}
	for _, method := range OfficialForgeRC2Methods() {
		body, ok := bodies[method]
		if !ok {
			t.Fatalf("%s 没有合法的测试请求体", method)
		}
		status, out := forgeRC2Call(t, srv, method, body)
		if status != http.StatusNotImplemented {
			t.Fatalf("%s 状态码 %d，期望 501（body=%v）", method, status, out)
		}
		assertFourFieldError(t, status, out, http.StatusNotImplemented, "not_implemented")
		if _, has := out["result"]; has {
			t.Fatalf("%s 回了 result：未接线不许带成功载荷（%v）", method, out)
		}
		obj := out["error"].(map[string]any)
		msg, _ := obj["message"].(string)
		if !strings.Contains(msg, string(method)) {
			t.Fatalf("%s 的错误正文没点名方法：%q", method, msg)
		}
		if !strings.Contains(msg, "AttachOfficialForgeRC2API") {
			t.Fatalf("%s 的错误正文没说清缺什么装配：%q", method, msg)
		}
	}
}

// TestForgeRC2ParamsFailLoud：参数错必须是 400，且**即使实现已挂**也先报参数。
func TestForgeRC2ParamsFailLoud(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	// 挂一个"只要被调用就失败"的实现：这样一旦参数校验被跳过，测试会以
	// 500/成功的形式暴露出来，而不是伪装成 400。
	called := false
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		ChangeRequestCommits: func(context.Context, string, string) (*OfficialForgeCommitList, error) {
			called = true
			return nil, nil
		},
		DiffBases: func(context.Context, string, string) (OfficialForgeBaseBranches, error) {
			called = true
			return OfficialForgeBaseBranches{}, nil
		},
		UpdateBranch: func(context.Context, string, string, string, string) error {
			called = true
			return nil
		},
	})
	defer srv.DetachOfficialForgeRC2API()

	cases := []struct {
		name   string
		method Method
		body   string
		want   string
	}{
		{"缺 sessionId", MethodForgePanelChangeRequestCommits, `{"url":"https://github.com/o/r/pull/7"}`, "sessionId"},
		{"sessionId 空串", MethodForgePanelChangeRequestCommits, `{"sessionId":" ","url":"https://github.com/o/r/pull/7"}`, "sessionId"},
		{"缺 url", MethodForgePanelChangeRequestCommits, `{"sessionId":"s-1"}`, "url"},
		{"未知字段", MethodForgePanelChangeRequestCommits, `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","extra":1}`, "extra"},
		{"sessionId 类型错", MethodForgePanelChangeRequestCommits, `{"sessionId":7,"url":"https://github.com/o/r/pull/7"}`, "sessionId"},
		{"缺 headSha", MethodForgePanelChangeRequestViewed, `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7"}`, "headSha"},
		{"fresh 不是布尔", MethodForgePanelChangeRequestReview, `{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","headSha":"9f2c","fresh":"yes"}`, "fresh"},
		{"query 缺失", MethodForgePanelDiffBases, `{"sessionId":"s-1"}`, "query"},
		{"base 键缺失", MethodForgePanelLocalCommits, `{"sessionId":"s-1"}`, "base"},
		{"base 类型错", MethodForgePanelLocalCommits, `{"sessionId":"s-1","base":3}`, "base"},
		{"reviewers 空数组", MethodForgePanelRequestReviews,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","reviewers":[]}`, "reviewers"},
		{"reviewers.kind 非枚举", MethodForgePanelRequestReviews,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","reviewers":[{"kind":"alien","login":"octo"}]}`, "kind"},
		{"reviewers.login 空串", MethodForgePanelRequestReviews,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","reviewers":[{"kind":"user","login":" "}]}`, "login"},
		{"request 缺 headSha", MethodForgePanelRerunChecks,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{}}`, "request.headSha"},
		{"setFileViewed path 空串", MethodForgePanelSetFileViewed,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"path":"","viewed":true,"headSha":"9f2c"}}`, "path"},
		{"setFileViewed viewed 类型错", MethodForgePanelSetFileViewed,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","request":{"path":"a.go","viewed":"yes","headSha":"9f2c"}}`, "viewed"},
		{"updateBranch method 非枚举", MethodForgePanelUpdateBranch,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","method":"squash","headSha":"9f2c"}`, "method"},
		{"write 缺 kind", MethodForgePanelChangeRequestReviewWrite,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","write":{"headSha":"9f2c"}}`, "kind"},
		{"write.kind 非枚举", MethodForgePanelChangeRequestReviewWrite,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","write":{"headSha":"9f2c","kind":"publish"}}`, "kind"},
		{"write resolve 缺 resolved", MethodForgePanelChangeRequestReviewWrite,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","write":{"headSha":"9f2c","kind":"resolve","thread":"T1"}}`, "resolved"},
		{"write add-pending 行号非法", MethodForgePanelChangeRequestReviewWrite,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","write":{"headSha":"9f2c","kind":"add-pending",` +
				`"comment":{"path":"a.go","side":"R","startLine":0,"endLine":0,"body":"x"}}}`, "行号"},
		{"write submit-pending event 非枚举", MethodForgePanelChangeRequestReviewWrite,
			`{"sessionId":"s-1","url":"https://github.com/o/r/pull/7","write":{"headSha":"9f2c","kind":"submit-pending",` +
				`"review":"R1","event":"lgtm","body":""}}`, "event"},
		{"worktreeCleanup revision 缺失", MethodForgePanelWorktreeCleanup, `{"sessionId":"s-1"}`, "revision"},
		{"worktreeCleanup revision 非数字", MethodForgePanelWorktreeCleanup, `{"sessionId":"s-1","revision":"2"}`, "revision"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			status, out := forgeRC2Call(t, srv, tc.method, tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("状态码 %d，期望 400（body=%v）", status, out)
			}
			if called {
				t.Fatalf("参数非法时实现被调用了：校验必须早于能力判断")
			}
			assertFourFieldError(t, status, out, http.StatusBadRequest, "bad_request")
			obj := out["error"].(map[string]any)
			msg, _ := obj["message"].(string)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("错误正文没点名字段 %q：%q", tc.want, msg)
			}
		})
	}
}

// TestForgeRC2ResultsShape 逐方法钉住结果形状（对照官方 jsonSchema）。
func TestForgeRC2ResultsShape(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	parent := "1111111111111111111111111111111111111111"
	author := "octo"
	name := "Octo Cat"
	avatar := "https://avatars.example/o.png"
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		ChangeRequestCommits: func(context.Context, string, string) (*OfficialForgeCommitList, error) {
			return &OfficialForgeCommitList{
				HeadSHA: "9f2c",
				Commits: []OfficialForgeCommit{
					{SHA: "9f2c", Parent: &parent, Title: "fix: 一", Author: &author, AuthoredAt: "2026-11-12T01:00:00Z"},
					{SHA: "1111", Parent: nil, Title: "root", Author: nil, AuthoredAt: "2026-11-11T01:00:00Z"},
				},
				Truncated: false,
			}, nil
		},
		ChangeRequestReview: func(context.Context, string, string, string, bool) (*OfficialForgeReviewData, error) {
			viewer := "me"
			return &OfficialForgeReviewData{HeadSHA: "9f2c", Viewer: &viewer, ViewerCanReview: true}, nil
		},
		ChangeRequestReviewWrite: func(context.Context, string, string, json.RawMessage) (OfficialForgeReviewWriteResult, error) {
			return OfficialForgeReviewWriteResult{Code: "unsupported", Message: "provider 不写 review"}, nil
		},
		ChangeRequestViewed: func(context.Context, string, string, string) (*OfficialForgeViewedFiles, error) {
			return &OfficialForgeViewedFiles{HeadSHA: "9f2c", Files: map[string]string{"a.go": "viewed"}}, nil
		},
		DiffBases: func(context.Context, string, string) (OfficialForgeBaseBranches, error) {
			return OfficialForgeBaseBranches{
				Automatic: &OfficialForgeBaseBranch{Name: "main", Worktree: false},
				Remote:    []OfficialForgeBaseRef{{Ref: "refs/remotes/origin/main", Name: "origin/main"}},
			}, nil
		},
		LocalCommits: func(context.Context, string, *string) (OfficialForgeLocalCommits, error) {
			return OfficialForgeLocalCommits{HeadSHA: "9f2c", Truncated: true}, nil
		},
		RequestReviews: func(context.Context, string, string, []OfficialForgeReviewerRef) error { return nil },
		RerunChecks:    func(context.Context, string, string, string) error { return nil },
		ReviewerCandidates: func(context.Context, string, string, string) ([]OfficialForgeReviewerCandidate, error) {
			return []OfficialForgeReviewerCandidate{{Login: "octo", Name: &name, AvatarURL: &avatar, Suggested: true}}, nil
		},
		SetFileViewed: func(context.Context, string, string, OfficialForgeViewedRequest) error { return nil },
		UpdateBranch:  func(context.Context, string, string, string, string) error { return nil },
		WorktreeCleanup: func(context.Context, string, *int64) (*OfficialForgeWorktreeCleanup, error) {
			return &OfficialForgeWorktreeCleanup{Revision: 3, Status: "removed"}, nil
		},
	})
	defer srv.DetachOfficialForgeRC2API()

	bodies := forgeRC2GoodBodies()
	assertOK := func(method Method) map[string]any {
		t.Helper()
		status, out := forgeRC2Call(t, srv, method, bodies[method])
		if status != http.StatusOK {
			t.Fatalf("%s 状态码 %d，期望 200（body=%v）", method, status, out)
		}
		if _, has := out["error"]; has {
			t.Fatalf("%s 回了 error：%v", method, out)
		}
		result, has := out["result"]
		if !has {
			t.Fatalf("%s 没有 result：%v", method, out)
		}
		obj, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("%s 的 result 不是对象：%T", method, result)
		}
		return obj
	}

	commits := assertOK(MethodForgePanelChangeRequestCommits)
	if commits["headSha"] != "9f2c" || commits["truncated"] != false {
		t.Fatalf("commits 头字段错：%v", commits)
	}
	list, ok := commits["commits"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("commits 数组错：%v", commits["commits"])
	}
	first := list[0].(map[string]any)
	for _, key := range []string{"sha", "parent", "title", "author", "authoredAt"} {
		if _, has := first[key]; !has {
			t.Fatalf("commits[0] 缺官方必填字段 %s：%v", key, first)
		}
	}
	// parent/author 官方允许 null，且必须是**键存在**的 null。
	if _, has := list[1].(map[string]any)["parent"]; !has {
		t.Fatalf("commits[1] 缺 parent 键（null 也要有键）")
	}
	if list[1].(map[string]any)["parent"] != nil {
		t.Fatalf("root commit 的 parent 应为 null：%v", list[1])
	}

	review := assertOK(MethodForgePanelChangeRequestReview)
	for _, key := range []string{"headSha", "viewer", "threads", "truncated", "pendingReview", "viewerCanReview"} {
		if _, has := review[key]; !has {
			t.Fatalf("review 结果缺官方必填字段 %s：%v", key, review)
		}
	}
	if threads, ok := review["threads"].([]any); !ok || len(threads) != 0 {
		t.Fatalf("没有线程时 threads 必须是空数组（不是 null）：%v", review["threads"])
	}

	write := assertOK(MethodForgePanelChangeRequestReviewWrite)
	// 官方 union：能力缺失是 200 + {ok:false, code:"unsupported"}，不是 HTTP 错误。
	if write["ok"] != false || write["code"] != "unsupported" {
		t.Fatalf("reviewWrite union 形状错：%v", write)
	}
	if _, has := write["message"]; !has {
		t.Fatalf("reviewWrite 失败时必须有 message：%v", write)
	}

	viewed := assertOK(MethodForgePanelChangeRequestViewed)
	if viewed["headSha"] != "9f2c" {
		t.Fatalf("viewed.headSha 错：%v", viewed)
	}
	files, ok := viewed["files"].(map[string]any)
	if !ok || files["a.go"] != "viewed" {
		t.Fatalf("viewed.files 应是 path→状态 的对象：%v", viewed["files"])
	}

	bases := assertOK(MethodForgePanelDiffBases)
	for _, key := range []string{"automatic", "remote", "local"} {
		if _, has := bases[key]; !has {
			t.Fatalf("diffBases 缺官方必填字段 %s：%v", key, bases)
		}
	}
	if _, ok := bases["local"].([]any); !ok {
		t.Fatalf("diffBases.local 必须是数组（不是 null）：%v", bases["local"])
	}
	automatic, ok := bases["automatic"].(map[string]any)
	if !ok || automatic["name"] != "main" || automatic["worktree"] != false {
		t.Fatalf("diffBases.automatic 形状错：%v", bases["automatic"])
	}

	local := assertOK(MethodForgePanelLocalCommits)
	if local["truncated"] != true || local["headSha"] != "9f2c" {
		t.Fatalf("localCommits 头字段错：%v", local)
	}

	candidates, has := func() ([]any, bool) {
		status, out := forgeRC2Call(t, srv, MethodForgePanelReviewerCandidates, bodies[MethodForgePanelReviewerCandidates])
		if status != http.StatusOK {
			t.Fatalf("reviewerCandidates 状态码 %d", status)
		}
		arr, ok := out["result"].([]any)
		return arr, ok
	}()
	if !has || len(candidates) != 1 {
		t.Fatalf("reviewerCandidates 应是数组：%v", candidates)
	}
	candidate := candidates[0].(map[string]any)
	for _, key := range []string{"login", "name", "avatarUrl", "suggested"} {
		if _, has := candidate[key]; !has {
			t.Fatalf("reviewerCandidates 元素缺 %s：%v", key, candidate)
		}
	}

	cleanup := assertOK(MethodForgePanelWorktreeCleanup)
	if cleanup["revision"] != float64(3) || cleanup["status"] != "removed" {
		t.Fatalf("worktreeCleanup 形状错：%v", cleanup)
	}

	// 四个 void 方法：官方成功无载荷，本仓 L1 出口是空对象。
	for _, method := range []Method{
		MethodForgePanelRequestReviews, MethodForgePanelRerunChecks,
		MethodForgePanelSetFileViewed, MethodForgePanelUpdateBranch,
	} {
		obj := assertOK(method)
		if len(obj) != 0 {
			t.Fatalf("%s 成功时应是空对象（官方 z.void），实际 %v", method, obj)
		}
	}
}

// TestForgeRC2NullableResults：官方结果允许 null 的三条必须给 {"result":null}，
// 而不是 500、空对象或"未接线"。
func TestForgeRC2NullableResults(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		// provider 不读 commits / 不保存已读标记 → (nil, nil)
		ChangeRequestCommits: func(context.Context, string, string) (*OfficialForgeCommitList, error) {
			return nil, nil
		},
		ChangeRequestViewed: func(context.Context, string, string, string) (*OfficialForgeViewedFiles, error) {
			return nil, nil
		},
		WorktreeCleanup: func(context.Context, string, *int64) (*OfficialForgeWorktreeCleanup, error) {
			return nil, nil
		},
	})
	defer srv.DetachOfficialForgeRC2API()

	bodies := forgeRC2GoodBodies()
	for _, method := range []Method{
		MethodForgePanelChangeRequestCommits,
		MethodForgePanelChangeRequestViewed,
		MethodForgePanelWorktreeCleanup,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/"+string(method), strings.NewReader(bodies[method]))
		rec := httptest.NewRecorder()
		srv.serveOfficialForgeLiveshareAPI(rec, req, string(method))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 状态码 %d，期望 200（body=%q）", method, rec.Code, rec.Body.String())
		}
		if strings.TrimSpace(rec.Body.String()) != `{"result":null}` {
			t.Fatalf("%s 应回 {\"result\":null}，实际 %q", method, rec.Body.String())
		}
	}
}

// TestForgeRC2UnsupportedCapabilityIs501：能力缺失必须是 501 + 官方码，
// 与"没挂 API"（not_implemented）可分辨。
func TestForgeRC2UnsupportedCapabilityIs501(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		ChangeRequestReview: func(context.Context, string, string, string, bool) (*OfficialForgeReviewData, error) {
			return nil, ForgeRC2Unsupported(CodeForgeReviewUnsupported, "provider 不读 review threads")
		},
		ChangeRequestViewed: func(context.Context, string, string, string) (*OfficialForgeViewedFiles, error) {
			return nil, ForgeRC2Unsupported(CodeForgeViewedFailed, "provider 不保存已读标记")
		},
	})
	defer srv.DetachOfficialForgeRC2API()
	bodies := forgeRC2GoodBodies()

	status, out := forgeRC2Call(t, srv, MethodForgePanelChangeRequestReview, bodies[MethodForgePanelChangeRequestReview])
	if status != http.StatusNotImplemented {
		t.Fatalf("review 状态码 %d，期望 501", status)
	}
	assertFourFieldError(t, status, out, http.StatusNotImplemented, CodeForgeReviewUnsupported)

	status, out = forgeRC2Call(t, srv, MethodForgePanelChangeRequestViewed, bodies[MethodForgePanelChangeRequestViewed])
	if status != http.StatusNotImplemented {
		t.Fatalf("viewed 状态码 %d，期望 501", status)
	}
	assertFourFieldError(t, status, out, http.StatusNotImplemented, CodeForgeViewedFailed)
}

// TestForgeRC2StaleHeadIs409：head 动了必须是 409 + forgePanel/stale-head。
func TestForgeRC2StaleHeadIs409(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		RerunChecks: func(context.Context, string, string, string) error {
			return ForgeRC2Errorf(http.StatusConflict, CodeForgeStaleHead, "head 已从 9f2c 移到 ab12")
		},
		UpdateBranch: func(context.Context, string, string, string, string) error {
			return ForgeRC2Errorf(http.StatusConflict, CodeForgeStaleHead, "head 已移动")
		},
		SetFileViewed: func(context.Context, string, string, OfficialForgeViewedRequest) error {
			return ForgeRC2Errorf(http.StatusConflict, CodeForgeStaleHead, "head 已移动")
		},
	})
	defer srv.DetachOfficialForgeRC2API()
	bodies := forgeRC2GoodBodies()
	for _, method := range []Method{
		MethodForgePanelRerunChecks, MethodForgePanelUpdateBranch, MethodForgePanelSetFileViewed,
	} {
		status, out := forgeRC2Call(t, srv, method, bodies[method])
		if status != http.StatusConflict {
			t.Fatalf("%s 状态码 %d，期望 409（body=%v）", method, status, out)
		}
		assertFourFieldError(t, status, out, http.StatusConflict, CodeForgeStaleHead)
	}
}

// TestForgeRC2ActionUnknownIsUnion：宿主答不出结果时，写操作类必须按官方
// 兜底码报错，不能变成成功。
func TestForgeRC2ActionUnknownIsUnion(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		RequestReviews: func(context.Context, string, string, []OfficialForgeReviewerRef) error {
			return errors.New("上游 502，未确认是否已请求")
		},
	})
	defer srv.DetachOfficialForgeRC2API()
	status, out := forgeRC2Call(t, srv, MethodForgePanelRequestReviews,
		forgeRC2GoodBodies()[MethodForgePanelRequestReviews])
	if status != http.StatusBadGateway {
		t.Fatalf("requestReviews 状态码 %d，期望 502", status)
	}
	assertFourFieldError(t, status, out, http.StatusBadGateway, CodeForgeActionFailed)
	if _, has := out["result"]; has {
		t.Fatalf("失败时不许带 result：%v", out)
	}
}

// TestForgeRC2ReviewWriteHostFailureIsUnion：宿主抛错时 reviewWrite 仍回官方
// union（ok:false, code:"unknown"），因为"写没写进去"必须由用户去 host 上核对。
func TestForgeRC2ReviewWriteHostFailureIsUnion(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		ChangeRequestReviewWrite: func(context.Context, string, string, json.RawMessage) (OfficialForgeReviewWriteResult, error) {
			return OfficialForgeReviewWriteResult{}, errors.New("提交时连接被切断")
		},
	})
	defer srv.DetachOfficialForgeRC2API()
	status, out := forgeRC2Call(t, srv, MethodForgePanelChangeRequestReviewWrite,
		forgeRC2GoodBodies()[MethodForgePanelChangeRequestReviewWrite])
	if status != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200（union 形状）", status)
	}
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("没有 union result：%v", out)
	}
	if result["ok"] != false || result["code"] != "unknown" {
		t.Fatalf("写失败必须回 unknown（别让用户盲目重试）：%v", result)
	}
}

// TestForgeRC2NoFakeData：宿主给了空实现返回值时必须报内部错误，不许编造。
func TestForgeRC2NoFakeData(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		ChangeRequestReview: func(context.Context, string, string, string, bool) (*OfficialForgeReviewData, error) {
			return nil, nil // 违反约定：review 不许用空值表示"没有线程"
		},
	})
	defer srv.DetachOfficialForgeRC2API()
	status, out := forgeRC2Call(t, srv, MethodForgePanelChangeRequestReview,
		forgeRC2GoodBodies()[MethodForgePanelChangeRequestReview])
	if status != http.StatusInternalServerError {
		t.Fatalf("状态码 %d，期望 500（空 review 不许当没线程）", status)
	}
	assertFourFieldError(t, status, out, http.StatusInternalServerError, "internal")
}

// TestForgeRC2ReachableThroughHTTP 走**真实的 server.ServeHTTP**（也就是 server.go:588
// 那条 `IsOfficialForgeLiveshareMethod` 分派），证明这 12 个名字在**不改 server.go**
// 的情况下就已经可调：
//   - 没挂实现 → 501 not_implemented（不是 404、不是静默成功）；
//   - 挂了实现 → 200 + 官方结果形状。
//
// 这条用例的意义：rc.1 的那套"逐方法未实现表"只覆盖 31 个名字，新方法如果只在
// 本文件里被 `ServeOfficialForgeRC2` 认领而没进 IsOfficialForgeLiveshareMethod，
// 就会在真实 /api 上落到 404——只有走 ServeHTTP 才测得到。
func TestForgeRC2ReachableThroughHTTP(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	cookie := srv.MintInternalCookie()
	if cookie == "" {
		t.Fatalf("MintInternalCookie 返回空值")
	}
	bodies := forgeRC2GoodBodies()
	call := func(method Method) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/"+string(method), strings.NewReader(bodies[method]))
		req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var out map[string]any
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("%s 的响应不是 JSON 对象: %q", method, rec.Body.String())
			}
		}
		return rec.Code, out
	}

	// 1) 没挂实现：12 个都必须 501 + 四字段，绝不 404。
	for _, method := range OfficialForgeRC2Methods() {
		status, out := call(method)
		if status == http.StatusNotFound {
			t.Fatalf("%s 回了 404：路由没认领这个 rc.2 方法", method)
		}
		if status != http.StatusNotImplemented {
			t.Fatalf("%s 状态码 %d，期望 501（body=%v）", method, status, out)
		}
		assertFourFieldError(t, status, out, http.StatusNotImplemented, "not_implemented")
	}

	// 2) 挂了实现：真实 200 + 官方形状（worktreeCleanup 用 null 这一支最能说明问题）。
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		WorktreeCleanup: func(context.Context, string, *int64) (*OfficialForgeWorktreeCleanup, error) {
			return nil, nil
		},
	})
	defer srv.DetachOfficialForgeRC2API()
	req := httptest.NewRequest(http.MethodPost, "/api/"+string(MethodForgePanelWorktreeCleanup),
		strings.NewReader(bodies[MethodForgePanelWorktreeCleanup]))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("worktreeCleanup 状态码 %d，期望 200（body=%q）", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != `{"result":null}` {
		t.Fatalf("worktreeCleanup 应回 {\"result\":null}，实际 %q", rec.Body.String())
	}
}

// TestForgeRC2UnsupportedCarriesCapableWhen：能力缺失（501）必须在四字段之外
// 多一个 sibling `capableWhen`，说清"补上什么就能用"。
//
// 依据：rc.2 的 `forgePanel/review-unsupported` 与 `forgePanel/viewed-failed`
// 就是"provider 缺这条读"（本机当前原因是缺 GitHub GraphQL 凭据）。四字段本身
// 不能变，capableWhen 只做兄弟字段（与 cua / office kit 的 501 同形）。
func TestForgeRC2UnsupportedCarriesCapableWhen(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		ChangeRequestReview: func(context.Context, string, string, string, bool) (*OfficialForgeReviewData, error) {
			// 与 cmd/dshd 适配器同形：provider 的 unsupported → ForgeRC2Unsupported。
			return nil, ForgeRC2Unsupported(CodeForgeReviewUnsupported, "provider 不读 review threads")
		},
		ChangeRequestViewed: func(context.Context, string, string, string) (*OfficialForgeViewedFiles, error) {
			return nil, ForgeRC2UnsupportedWhen("配好 GitHub 凭据后可用（自定义文案）",
				CodeForgeViewedFailed, "provider 不保存已读标记")
		},
	})
	defer srv.DetachOfficialForgeRC2API()
	bodies := forgeRC2GoodBodies()

	status, out := forgeRC2Call(t, srv, MethodForgePanelChangeRequestReview, bodies[MethodForgePanelChangeRequestReview])
	if status != http.StatusNotImplemented {
		t.Fatalf("review 状态码 %d，期望 501", status)
	}
	assertFourFieldError(t, status, out, http.StatusNotImplemented, CodeForgeReviewUnsupported)
	capableWhen, _ := out["capableWhen"].(string)
	if strings.TrimSpace(capableWhen) == "" {
		t.Fatalf("501 必须带 capableWhen：%v", out)
	}
	// 默认文案要能指路（当前这条能力缺的就是 GraphQL 凭据）。
	for _, want := range []string{"GITHUB_TOKEN", "GraphQL"} {
		if !strings.Contains(capableWhen, want) {
			t.Fatalf("capableWhen 没说清怎么补（缺 %q）：%q", want, capableWhen)
		}
	}

	status, out = forgeRC2Call(t, srv, MethodForgePanelChangeRequestViewed, bodies[MethodForgePanelChangeRequestViewed])
	if status != http.StatusNotImplemented {
		t.Fatalf("viewed 状态码 %d，期望 501", status)
	}
	assertFourFieldError(t, status, out, http.StatusNotImplemented, CodeForgeViewedFailed)
	if got, _ := out["capableWhen"].(string); got != "配好 GitHub 凭据后可用（自定义文案）" {
		t.Fatalf("显式给的 capableWhen 应原样出手，实际 %q", got)
	}
}

// TestForgeRC2NonUnsupportedHasNoCapableWhen：capableWhen 只服务"能力缺失"。
// stale（409）之类的失败不得带它，否则客户端会把可重试失败当成"没这能力"。
func TestForgeRC2NonUnsupportedHasNoCapableWhen(t *testing.T) {
	srv := forgeOfficialTestServer(t)
	srv.AttachOfficialForgeRC2API(OfficialForgeRC2API{
		ChangeRequestReview: func(context.Context, string, string, string, bool) (*OfficialForgeReviewData, error) {
			return nil, ForgeRC2Errorf(http.StatusConflict, CodeForgeStaleHead, "head 已移动")
		},
	})
	defer srv.DetachOfficialForgeRC2API()
	status, out := forgeRC2Call(t, srv, MethodForgePanelChangeRequestReview,
		forgeRC2GoodBodies()[MethodForgePanelChangeRequestReview])
	if status != http.StatusConflict {
		t.Fatalf("状态码 %d，期望 409", status)
	}
	if _, has := out["capableWhen"]; has {
		t.Fatalf("409 不该带 capableWhen：%v", out)
	}
}
