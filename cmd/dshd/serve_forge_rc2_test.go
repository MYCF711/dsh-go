package main

// rc.2 Forge 面宿主适配器的测试。
//
// 覆盖三件事（都不联网、不写用户目录）：
//  1. 官方错误码映射：领域错误 → `forgePanel/*`（readError / reviewError /
//     actionError 的口径），以及"provider 没有这条能力"的官方语义；
//  2. diffBases / localCommits 走**真实 git**（t.TempDir 建仓库）；
//  3. worktreeCleanup 在没有 worktree 服务时按官方回 null（不是报错、也不是假状态）。

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/forge"
	"dshgo/internal/transport"
)

// newRC2TestAPI 造一个只依赖本机能力的适配器（没有真实远端）。
func newRC2TestAPI(resolve func(string) (string, error)) *forgeRC2API {
	return &forgeRC2API{
		provider: forge.NewChangeRequestProviderFromEnv(),
		exec:     forge.NewExecutor(),
		resolve:  resolve,
	}
}

// rc2GitRepo 建一个真仓库并返回路径（默认分支 main）。
func rc2GitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"config", "commit.gpgsign", "false"},
	} {
		rc2RunGit(t, dir, args...)
	}
	return dir
}

func rc2RunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func rc2Commit(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	rc2RunGit(t, dir, "add", name)
	rc2RunGit(t, dir, "commit", "-m", message)
	return rc2RunGit(t, dir, "rev-parse", "HEAD")
}

// TestForgeRC2HostFailureMapping 钉住"领域码 → 官方码"这张表。
func TestForgeRC2HostFailureMapping(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		fallback    string
		unsupported string
		wantCode    string
		wantIS      int
	}{
		{"stale 读", &forge.ForgeHTTPError{Status: 409, Code: "stale_head", Message: "head 动了"},
			transport.CodeForgeDiffFailed, transport.CodeForgeDiffFailed, transport.CodeForgeStaleHead, 409},
		{"unsupported 读", forge.Unsupported("readReview"),
			transport.CodeForgeReviewFailed, transport.CodeForgeReviewUnsupported, transport.CodeForgeReviewUnsupported, 501},
		{"not_found 读", &forge.ForgeHTTPError{Status: 404, Code: "not_found", Message: "没有"},
			transport.CodeForgeDiffFailed, transport.CodeForgeDiffFailed, transport.CodeForgeDiffFailed, 502},
		{"too-large 读", &forge.ForgeHTTPError{Status: 413, Code: "too_large", Message: "太大"},
			transport.CodeForgeDiffFailed, transport.CodeForgeDiffFailed, transport.CodeForgeDiffTooLarge, 413},
		{"auth 读", &forge.ForgeHTTPError{Status: 401, Code: "auth_required", Message: "未登录"},
			transport.CodeForgeDiffFailed, transport.CodeForgeDiffFailed, transport.CodeForgeDiffFailed, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := forgeRC2ReadFailure(tc.err, tc.fallback, tc.unsupported)
			var typed *transport.ForgeRC2Error
			if !errors.As(mapped, &typed) {
				t.Fatalf("应翻成 *transport.ForgeRC2Error，实际 %T", mapped)
			}
			if typed.Code != tc.wantCode || typed.Status != tc.wantIS {
				t.Fatalf("映射错：got %s/%d，want %s/%d", typed.Code, typed.Status, tc.wantCode, tc.wantIS)
			}
		})
	}

	action := []struct {
		name     string
		err      error
		missing  string
		wantCode string
		wantIS   int
	}{
		{"stale 写", &forge.ForgeHTTPError{Status: 409, Code: "stale_head", Message: "head 动了"},
			"x", transport.CodeForgeStaleHead, 409},
		{"unknown 写", &forge.ForgeHTTPError{Status: 502, Code: "provider_unavailable", Message: "断线"},
			"x", transport.CodeForgeActionUnknown, 502},
		{"provider 不提供", forge.Unsupported("requestReviews"),
			"provider 不请求评审", transport.CodeForgeActionFailed, 502},
	}
	for _, tc := range action {
		t.Run(tc.name, func(t *testing.T) {
			mapped := forgeRC2ActionFailure(tc.err, tc.missing)
			var typed *transport.ForgeRC2Error
			if !errors.As(mapped, &typed) {
				t.Fatalf("应翻成 *transport.ForgeRC2Error，实际 %T", mapped)
			}
			if typed.Code != tc.wantCode || typed.Status != tc.wantIS {
				t.Fatalf("映射错：got %s/%d，want %s/%d", typed.Code, typed.Status, tc.wantCode, tc.wantIS)
			}
		})
	}

	// provider 不提供能力时，正文要说清"缺的是这条能力"（官方 act() 的文案形状）。
	mapped := forgeRC2ActionFailure(forge.Unsupported("setViewed"), "provider 不保存已读标记")
	if !strings.Contains(mapped.Error(), "不保存已读标记") {
		t.Fatalf("正文应点名缺失能力：%q", mapped.Error())
	}

	git := []struct {
		err      error
		wantCode string
		wantIS   int
	}{
		{forge.ErrNoForkPoint, transport.CodeForgeNoForkPoint, 422},
		{forge.ErrBaseMissing, transport.CodeForgeBaseMissing, 422},
		{forge.ErrNotBranchRef, "bad_request", 400},
		{forge.ErrNotRepository, transport.CodeForgeNotACheckout, 400},
		{errors.New("git 崩了"), transport.CodeForgeNotACheckout, 502},
	}
	for _, tc := range git {
		mapped := forgeRC2GitFailure(tc.err)
		var typed *transport.ForgeRC2Error
		if !errors.As(mapped, &typed) {
			t.Fatalf("%v 应翻成 *transport.ForgeRC2Error", tc.err)
		}
		if typed.Code != tc.wantCode || typed.Status != tc.wantIS {
			t.Fatalf("%v 映射错：got %s/%d，want %s/%d", tc.err, typed.Code, typed.Status, tc.wantCode, tc.wantIS)
		}
	}
}

// TestForgeRC2HostDiffBasesRealGit：真实 git 仓库里列分支 + automatic fork point。
func TestForgeRC2HostDiffBasesRealGit(t *testing.T) {
	dir := rc2GitRepo(t)
	first := rc2Commit(t, dir, "a.txt", "1\n", "first")
	rc2RunGit(t, dir, "remote", "add", "origin", "https://example.invalid/o/r.git")
	rc2RunGit(t, dir, "update-ref", "refs/remotes/origin/main", first)
	rc2RunGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	rc2Commit(t, dir, "b.txt", "2\n", "second")
	rc2RunGit(t, dir, "branch", "feature")

	api := newRC2TestAPI(func(sessionID string) (string, error) { return dir, nil })
	result, err := api.diffBases(context.Background(), "s-1", "")
	if err != nil {
		t.Fatalf("diffBases: %v", err)
	}
	if len(result.Local) != 1 || result.Local[0].Name != "feature" {
		t.Fatalf("local 应只有 feature（当前分支被排除），实际 %v", result.Local)
	}
	if len(result.Remote) != 1 || result.Remote[0].Name != "origin/main" {
		t.Fatalf("remote 应只有 origin/main（origin/HEAD 是 symbolic ref），实际 %v", result.Remote)
	}
	if result.Automatic == nil || result.Automatic.Name != "origin/main" || result.Automatic.Worktree {
		t.Fatalf("automatic 应是 origin/main（worktree=false），实际 %v", result.Automatic)
	}
	// 官方 schema 要求 remote/local 是数组；transport 层再保证不是 nil。
	if result.Remote == nil || result.Local == nil {
		t.Fatalf("remote/local 不许是 nil")
	}

	filtered, err := api.diffBases(context.Background(), "s-1", "FEAT")
	if err != nil {
		t.Fatalf("diffBases: %v", err)
	}
	if len(filtered.Local) != 1 || filtered.Local[0].Name != "feature" {
		t.Fatalf("query 过滤不对：%v", filtered.Local)
	}
}

// TestForgeRC2HostLocalCommitsRealGit：真实仓库里的提交列表。
func TestForgeRC2HostLocalCommitsRealGit(t *testing.T) {
	dir := rc2GitRepo(t)
	fork := rc2Commit(t, dir, "a.txt", "1\n", "first")
	rc2RunGit(t, dir, "remote", "add", "origin", "https://example.invalid/o/r.git")
	rc2RunGit(t, dir, "update-ref", "refs/remotes/origin/main", fork)
	rc2RunGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	second := rc2Commit(t, dir, "b.txt", "2\n", "second")

	api := newRC2TestAPI(func(sessionID string) (string, error) { return dir, nil })
	list, err := api.localCommits(context.Background(), "s-1", nil)
	if err != nil {
		t.Fatalf("localCommits: %v", err)
	}
	if list.HeadSHA != second {
		t.Fatalf("headSha 应是 HEAD，实际 %s", list.HeadSHA)
	}
	if len(list.Commits) != 1 || list.Commits[0].SHA != second {
		t.Fatalf("fork 之后应只有 1 条，实际 %v", list.Commits)
	}
	if list.Commits[0].Title != "second" {
		t.Fatalf("title 错：%q", list.Commits[0].Title)
	}

	// 选择不存在的分支：官方 base-missing。
	missing := "refs/heads/nope"
	_, err = api.localCommits(context.Background(), "s-1", &missing)
	var typed *transport.ForgeRC2Error
	if !errors.As(err, &typed) || typed.Code != transport.CodeForgeBaseMissing {
		t.Fatalf("不存在分支应回 base-missing，实际 %v", err)
	}
}

// TestForgeRC2HostNotACheckout：会话目录不在 git 仓库里时必须是官方
// not-a-checkout（400），不是 502 也不是空数组。
func TestForgeRC2HostNotACheckout(t *testing.T) {
	dir := t.TempDir() // 不是 git 仓库
	api := newRC2TestAPI(func(sessionID string) (string, error) { return dir, nil })
	_, err := api.diffBases(context.Background(), "s-1", "")
	var typed *transport.ForgeRC2Error
	if !errors.As(err, &typed) {
		t.Fatalf("应回 *transport.ForgeRC2Error，实际 %T", err)
	}
	if typed.Code != transport.CodeForgeNotACheckout {
		t.Fatalf("应是 forgePanel/not-a-checkout，实际 %s", typed.Code)
	}

	// sessionId 解析不出来时也不许静默返回空分支表。
	broken := newRC2TestAPI(func(sessionID string) (string, error) { return "", nil })
	if _, err := broken.diffBases(context.Background(), "s-1", ""); err == nil {
		t.Fatalf("空目录必须报错（不许回假分支表）")
	}
}

// TestForgeRC2HostProviderCapabilities：provider 没有的能力按官方语义表达。
func TestForgeRC2HostProviderCapabilities(t *testing.T) {
	api := newRC2TestAPI(func(sessionID string) (string, error) { return t.TempDir(), nil })

	// review threads：官方 review-unsupported（501）。
	_, err := api.changeRequestReview(context.Background(), "s-1", "https://github.com/o/r/pull/7", "9f2c", false)
	var typed *transport.ForgeRC2Error
	if !errors.As(err, &typed) || typed.Code != transport.CodeForgeReviewUnsupported {
		t.Fatalf("review 能力缺失应是 review-unsupported，实际 %v", err)
	}
	if typed.Status != 501 {
		t.Fatalf("能力缺失状态码应是 501，实际 %d", typed.Status)
	}

	// viewed marks：官方 provider 缺这条读时结果是 **null**（不是错误）。
	viewed, err := api.changeRequestViewed(context.Background(), "s-1", "https://github.com/o/r/pull/7", "9f2c")
	if err != nil || viewed != nil {
		t.Fatalf("viewed 应是 (nil,nil) → 官方 null，实际 %v / %v", viewed, err)
	}

	// setViewed：官方是 failed（action-failed），不是 unsupported 静默成功。
	err = api.setFileViewed(context.Background(), "s-1", "https://github.com/o/r/pull/7",
		transport.OfficialForgeViewedRequest{Path: "a.go", Viewed: true, HeadSHA: "9f2c"})
	if !errors.As(err, &typed) || typed.Code != transport.CodeForgeActionFailed {
		t.Fatalf("setViewed 能力缺失应是 action-failed，实际 %v", err)
	}

	// commits：本 provider **实现** commits，但没有凭据 → 官方前一步就是
	// unauthenticated，映射成 forgePanel/diff-failed（readError 1082-1096），
	// 绝不是"没有提交"的空列表。
	//
	// 另一支"provider 不读 commits → null"要求一个没有 commits 能力的 provider，
	// 由 transport 层的 TestForgeRC2NullableResults 覆盖（那一层才是契约出口）。
	commits, err := api.changeRequestCommits(context.Background(), "s-1", "https://github.com/o/r/pull/7")
	var commitErr *transport.ForgeRC2Error
	if !errors.As(err, &commitErr) || commitErr.Code != transport.CodeForgeDiffFailed {
		t.Fatalf("未登录读 commits 应是 diff-failed，实际 %v / %v", commits, err)
	}

	// writeReview：官方 union 的 unsupported 分支（不是 error）。
	result, err := api.changeRequestReviewWrite(context.Background(), "s-1", "https://github.com/o/r/pull/7",
		[]byte(`{"headSha":"9f2c","kind":"resolve","thread":"T1","resolved":true}`))
	if err != nil {
		t.Fatalf("writeReview 不许回 error：%v", err)
	}
	if result.OK || result.Code != "unsupported" || result.Message == "" {
		t.Fatalf("writeReview 应是 union {ok:false,code:unsupported}，实际 %+v", result)
	}
}

// TestForgeRC2HostWorktreeCleanupWithoutService：没有 worktree 服务时官方结果是
// null（不是错误，也不是编一个 revision）。
func TestForgeRC2HostWorktreeCleanupWithoutService(t *testing.T) {
	api := newRC2TestAPI(func(sessionID string) (string, error) { return t.TempDir(), nil })
	if api.worktrees != nil {
		t.Fatalf("本测试要求没有 worktree 服务")
	}
	cleanup, err := api.worktreeCleanup(context.Background(), "s-1", nil)
	if err != nil || cleanup != nil {
		t.Fatalf("没有 worktree 服务时应是 (nil,nil)，实际 %v / %v", cleanup, err)
	}
}
