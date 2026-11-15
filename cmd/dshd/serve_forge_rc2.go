package main

// rc.2 Forge 面 12 个方法的宿主实现（transport.OfficialForgeRC2API）。
//
// 分工：
//   - code host 相关（commits / review / viewed / reviewerCandidates /
//     requestReviews / rerunChecks / updateBranch）走 internal/forge 的真实
//     GitHub REST provider；
//   - 本机相关（diffBases / localCommits）走 internal/forge 的 Git 执行器
//     （真实 git 子进程，见 git_rc2.go）；
//   - 归档清理（worktreeCleanup）走 internal/worktree 的清理状态机。
//
// # 错误码
//
// 本层把领域错误翻成**官方 Remote 码**（transport.CodeForge*），这样客户端看到
// 的就是官方那套 `forgePanel/stale-head` / `forgePanel/review-unsupported` /
// `forgePanel/action-failed`，而不是本仓自造的码。映射依据：
//
//	client-ui-forge/src/index.ts 1082-1150（readError / reviewError / actionError）
//	dsh-experimental-forge/src/index.ts（provider 缺失语义：act() 553、setViewed 390、
//	commits 404、viewedFiles 373、reviewerCandidates 267）
//
// # 接线（Lead 集成）
//
//	// cmd/dshd/serve.go 的 forge 装配段（现有 822-828 行附近）
//	if wt := worktreesOf(...); wt != nil { … }
//	srv.AttachOfficialForgeRC2API(newForgeRC2API(func(sessionID string) (string, error) { … }, worktreeSvc))
//
// 第二个参数就是 worktree 服务（`internal/worktree` 的 *Service，可能为 nil：
// 没有它时 worktreeCleanup 按官方语义回 null，其余 11 条照常）。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"dshgo/internal/forge"
	"dshgo/internal/transport"
	"dshgo/internal/worktree"
)

// forgeRC2API 把 provider + git 执行器 + worktree 服务装成 transport 的函数表。
type forgeRC2API struct {
	provider  *forge.ChangeRequestProvider
	exec      *forge.Executor
	resolve   func(sessionID string) (string, error)
	worktrees *worktree.Service
}

// newForgeRC2API 造一份 rc.2 Forge 实现。
//
// resolve 与会话目录解析器同源（`serve_forge_git.go` 的同一个回调）；
// worktrees 可为 nil。
func newForgeRC2API(resolve func(sessionID string) (string, error), worktrees *worktree.Service) transport.OfficialForgeRC2API {
	api := &forgeRC2API{
		provider:  forge.NewChangeRequestProviderFromEnv(),
		exec:      forge.NewExecutor(),
		resolve:   resolve,
		worktrees: worktrees,
	}
	return transport.OfficialForgeRC2API{
		ChangeRequestCommits:     api.changeRequestCommits,
		ChangeRequestReview:      api.changeRequestReview,
		ChangeRequestReviewWrite: api.changeRequestReviewWrite,
		ChangeRequestViewed:      api.changeRequestViewed,
		DiffBases:                api.diffBases,
		LocalCommits:             api.localCommits,
		RequestReviews:           api.requestReviews,
		RerunChecks:              api.rerunChecks,
		ReviewerCandidates:       api.reviewerCandidates,
		SetFileViewed:            api.setFileViewed,
		UpdateBranch:             api.updateBranch,
		WorktreeCleanup:          api.worktreeCleanup,
	}
}

// ---------------------------------------------------------------------------
// code host（GitHub REST provider）
// ---------------------------------------------------------------------------

func (a *forgeRC2API) changeRequestCommits(ctx context.Context, sessionID, rawURL string) (*transport.OfficialForgeCommitList, error) {
	list, err := a.provider.ChangeRequestCommits(ctx, forgeParams(sessionID, rawURL))
	if err != nil {
		// 官方：provider 不读 commits（commits/compare 缺失）时结果是 **null**
		// （forge/index.ts commits → undefined → Remote 回 null）。所以
		// unsupported 不是错误，而是 null；其余失败才是 forgePanel/diff-failed。
		if forge.IsForgeCode(err, forge.FailureUnsupported) {
			return nil, nil
		}
		return nil, forgeRC2ReadFailure(err, transport.CodeForgeDiffFailed, transport.CodeForgeDiffFailed)
	}
	out := &transport.OfficialForgeCommitList{HeadSHA: list.HeadSHA, Truncated: list.Truncated}
	for _, commit := range list.Commits {
		out.Commits = append(out.Commits, transport.OfficialForgeCommit{
			SHA: commit.SHA, Parent: commit.Parent, Title: commit.Title,
			Author: commit.Author, AuthoredAt: commit.AuthoredAt,
		})
	}
	if out.Commits == nil {
		out.Commits = []transport.OfficialForgeCommit{}
	}
	return out, nil
}

func (a *forgeRC2API) changeRequestReview(ctx context.Context, sessionID, rawURL, headSHA string, fresh bool) (*transport.OfficialForgeReviewData, error) {
	data, err := a.provider.ChangeRequestReview(ctx, forgeParams(sessionID, rawURL), headSHA, fresh)
	if err != nil {
		// 官方 reviewError 1101-1116：stale → stale-head、unsupported →
		// forgePanel/review-unsupported、其余 → review-failed。
		return nil, forgeRC2ReadFailure(err, transport.CodeForgeReviewFailed, transport.CodeForgeReviewUnsupported)
	}
	if _, ok := data["threads"]; !ok {
		// provider 忘了给 threads：这是宿主 bug，不能当成"没有线程"。
		return nil, transport.ForgeRC2Errorf(500, "internal",
			"provider 的 review 结果缺 threads（不许用空值冒充没有线程）")
	}
	// provider 给出的是官方 ForgeReviewData 的 JSON 形状（camelCase），这里做一次
	// 类型搬运 + 形状校验：不合法就报宿主内部错误，绝不把半截数据当线程列表。
	result := &transport.OfficialForgeReviewData{}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, transport.ForgeRC2Errorf(500, "internal", "review 结果无法序列化：%v", err)
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return nil, transport.ForgeRC2Errorf(500, "internal", "review 结果形状非法：%v", err)
	}
	if result.Threads == nil {
		result.Threads = []transport.OfficialForgeReviewThread{}
	}
	return result, nil
}

func (a *forgeRC2API) changeRequestReviewWrite(ctx context.Context, sessionID, rawURL string, write json.RawMessage) (transport.OfficialForgeReviewWriteResult, error) {
	result, err := a.provider.ChangeRequestReviewWrite(ctx, forgeParams(sessionID, rawURL), write)
	if err != nil {
		// 官方 writeReview 的 provider 缺失/失败都在 union 里（types.ts:404-406），
		// 不是 HTTP 错误；这里把 error 兜成 union，让 transport 出官方形状。
		return transport.OfficialForgeReviewWriteResult{Code: "unknown", Message: err.Error()}, nil
	}
	out := transport.OfficialForgeReviewWriteResult{OK: result.OK, Code: result.Code, Message: result.Message}
	if out.OK {
		out.Code, out.Message = "", ""
	}
	return out, nil
}

func (a *forgeRC2API) changeRequestViewed(ctx context.Context, sessionID, rawURL, headSHA string) (*transport.OfficialForgeViewedFiles, error) {
	viewed, err := a.provider.ChangeRequestViewed(ctx, forgeParams(sessionID, rawURL), headSHA)
	if err != nil {
		// 官方 viewedFiles 在 provider 没有这条读时返回 undefined → Remote 回 null
		// （forge/index.ts:373-380）。所以 unsupported **不是错误**，是 null。
		if forge.IsForgeCode(err, forge.FailureUnsupported) {
			return nil, nil
		}
		return nil, forgeRC2ReadFailure(err, transport.CodeForgeViewedFailed, transport.CodeForgeViewedFailed)
	}
	files := map[string]string{}
	for path, state := range viewed.Files {
		files[path] = state
	}
	return &transport.OfficialForgeViewedFiles{HeadSHA: viewed.HeadSHA, Files: files, Truncated: viewed.Truncated}, nil
}

func (a *forgeRC2API) reviewerCandidates(ctx context.Context, sessionID, rawURL, query string) ([]transport.OfficialForgeReviewerCandidate, error) {
	candidates, err := a.provider.ChangeRequestReviewerCandidates(ctx, forgeParams(sessionID, rawURL), query)
	if err != nil {
		// 官方 reviewerCandidates：provider 不提供或读失败都是 forgePanel/reviewers-failed。
		return nil, forgeRC2ReadFailure(err, transport.CodeForgeReviewersFailed, transport.CodeForgeReviewersFailed)
	}
	out := make([]transport.OfficialForgeReviewerCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, transport.OfficialForgeReviewerCandidate{
			Login: candidate.Login, Name: candidate.Name,
			AvatarURL: candidate.AvatarURL, Suggested: candidate.Suggested,
		})
	}
	return out, nil
}

func (a *forgeRC2API) requestReviews(ctx context.Context, sessionID, rawURL string, reviewers []transport.OfficialForgeReviewerRef) error {
	refs := make([]forge.ReviewerRef, 0, len(reviewers))
	for _, reviewer := range reviewers {
		refs = append(refs, forge.ReviewerRef{Kind: reviewer.Kind, Login: reviewer.Login})
	}
	if err := a.provider.ChangeRequestRequestReviews(ctx, forgeParams(sessionID, rawURL), refs); err != nil {
		return forgeRC2ActionFailure(err, "provider 不请求评审（requestReviews 未实现）")
	}
	return nil
}

func (a *forgeRC2API) rerunChecks(ctx context.Context, sessionID, rawURL, headSHA string) error {
	if err := a.provider.ChangeRequestRerunChecks(ctx, forgeParams(sessionID, rawURL), headSHA); err != nil {
		return forgeRC2ActionFailure(err, "provider 不重跑 check（rerunChecks 未实现）")
	}
	return nil
}

func (a *forgeRC2API) setFileViewed(ctx context.Context, sessionID, rawURL string, request transport.OfficialForgeViewedRequest) error {
	err := a.provider.ChangeRequestSetFileViewed(ctx, forgeParams(sessionID, rawURL), request.Path, request.Viewed, request.HeadSHA)
	if err == nil {
		return nil
	}
	// 官方 setViewed 在 provider 没有这条能力时给 `failed` + "This provider keeps
	// no viewed marks"（forge/index.ts:390-397）→ actionError → forgePanel/action-failed。
	return forgeRC2ActionFailure(err, "provider 不保存已读标记（setViewed 未实现）")
}

func (a *forgeRC2API) updateBranch(ctx context.Context, sessionID, rawURL, method, headSHA string) error {
	if err := a.provider.ChangeRequestUpdateBranch(ctx, forgeParams(sessionID, rawURL), method, headSHA); err != nil {
		return forgeRC2ActionFailure(err, "provider 不更新分支（updateBranch 未实现）")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 本机 git（diffBases / localCommits）
// ---------------------------------------------------------------------------

// checkoutRoot 解析会话目录并返回仓库根 + worktree 事实。
func (a *forgeRC2API) checkoutRoot(ctx context.Context, sessionID string) (string, *forge.WorktreeFork, error) {
	dir, err := a.sessionDir(sessionID)
	if err != nil {
		return "", nil, err
	}
	root, err := a.exec.Root(ctx, dir)
	if err != nil {
		// 官方 not-a-checkout（Remote 注释里点名的码）。
		return "", nil, transport.ForgeRC2Errorf(400, transport.CodeForgeNotACheckout,
			"会话目录不在 git checkout 里：%s", dir)
	}
	return root, a.worktreeFork(sessionID, root), nil
}

func (a *forgeRC2API) diffBases(ctx context.Context, sessionID, query string) (transport.OfficialForgeBaseBranches, error) {
	root, fork, err := a.checkoutRoot(ctx, sessionID)
	if err != nil {
		return transport.OfficialForgeBaseBranches{}, err
	}
	branches, err := a.exec.BaseBranches(ctx, root, query, forge.DefaultMaxBranches, fork)
	if err != nil {
		return transport.OfficialForgeBaseBranches{}, forgeRC2GitFailure(err)
	}
	out := transport.OfficialForgeBaseBranches{Remote: []transport.OfficialForgeBaseRef{}, Local: []transport.OfficialForgeBaseRef{}}
	if branches.Automatic != nil {
		out.Automatic = &transport.OfficialForgeBaseBranch{Name: branches.Automatic.Name, Worktree: branches.Automatic.Worktree}
	}
	for _, ref := range branches.Remote {
		out.Remote = append(out.Remote, transport.OfficialForgeBaseRef{Ref: ref.Ref, Name: ref.Name})
	}
	for _, ref := range branches.Local {
		out.Local = append(out.Local, transport.OfficialForgeBaseRef{Ref: ref.Ref, Name: ref.Name})
	}
	return out, nil
}

func (a *forgeRC2API) localCommits(ctx context.Context, sessionID string, base *string) (transport.OfficialForgeLocalCommits, error) {
	root, fork, err := a.checkoutRoot(ctx, sessionID)
	if err != nil {
		return transport.OfficialForgeLocalCommits{}, err
	}
	commit, _, _, err := a.exec.ForkPoint(ctx, root, base, fork)
	if err != nil {
		return transport.OfficialForgeLocalCommits{}, forgeRC2GitFailure(err)
	}
	if commit == "" {
		return transport.OfficialForgeLocalCommits{}, transport.ForgeRC2Errorf(422, transport.CodeForgeNoForkPoint,
			"这个 checkout 没有可与所选分支比较的 fork point")
	}
	list, err := a.exec.LocalCommits(ctx, root, commit, forge.DefaultMaxCommits)
	if err != nil {
		return transport.OfficialForgeLocalCommits{}, forgeRC2GitFailure(err)
	}
	out := transport.OfficialForgeLocalCommits{HeadSHA: list.HeadSHA, Truncated: list.Truncated}
	for _, item := range list.Commits {
		out.Commits = append(out.Commits, transport.OfficialForgeCommit{
			SHA: item.SHA, Parent: item.Parent, Title: item.Title,
			Author: item.Author, AuthoredAt: item.AuthoredAt,
		})
	}
	if out.Commits == nil {
		out.Commits = []transport.OfficialForgeCommit{}
	}
	return out, nil
}

// worktreeFork 把 worktree 服务的创建记录翻成"自动 fork point"的事实。
//
// 只在会话当前的 checkout 就是它自己建的 worktree 时才返回（官方 forkPoint
// 的 `created.path === inspection.checkout.root` 判据）；否则返回 nil，让
// ForkPoint 退回远端默认分支的 merge-base。
func (a *forgeRC2API) worktreeFork(sessionID, root string) *forge.WorktreeFork {
	if a.worktrees == nil {
		return nil
	}
	state, err := a.worktrees.State(sessionID)
	if err != nil || state.Status != worktree.StatusCreated {
		return nil
	}
	if strings.TrimSpace(state.Directory) == "" {
		return nil
	}
	return &forge.WorktreeFork{
		Directory:  state.Directory,
		Base:       state.Base,
		BaseCommit: state.BaseCommit,
	}
}

// sessionDir 解析会话的工作目录。
func (a *forgeRC2API) sessionDir(sessionID string) (string, error) {
	if a.resolve == nil {
		return "", transport.ForgeRC2Errorf(501, "not_implemented",
			"forgePanel: 宿主没有会话目录解析器")
	}
	dir, err := a.resolve(sessionID)
	if err != nil {
		return "", transport.ForgeRC2Errorf(400, "bad_request", "sessionId 找不到目录：%v", err)
	}
	if strings.TrimSpace(dir) == "" {
		return "", transport.ForgeRC2Errorf(400, "bad_request", "sessionId 找不到目录")
	}
	return dir, nil
}

// ---------------------------------------------------------------------------
// 归档清理（worktreeCleanup）
// ---------------------------------------------------------------------------

func (a *forgeRC2API) worktreeCleanup(ctx context.Context, sessionID string, revision *int64) (*transport.OfficialForgeWorktreeCleanup, error) {
	_ = ctx
	if a.worktrees == nil {
		// 官方：没有 worktree 服务时 cleanupOf 是 undefined → Remote 回 null。
		return nil, nil
	}
	cleanup, ok := a.worktrees.CleanupOf(sessionID, revision)
	if !ok || cleanup == nil {
		return nil, nil
	}
	return &transport.OfficialForgeWorktreeCleanup{Revision: cleanup.Revision, Status: string(cleanup.Status)}, nil
}

// ---------------------------------------------------------------------------
// 错误映射
// ---------------------------------------------------------------------------

// forgeParams 是 provider 的入参（本层只有 session/url 两个位置参数）。
func forgeParams(sessionID, rawURL string) transport.OfficialForgeChangeRequestParams {
	return transport.OfficialForgeChangeRequestParams{SessionID: sessionID, URL: rawURL}
}

// forgeRC2ReadFailure 把**读**失败翻成官方码（readError / reviewError 口径）。
//
//	stale → 409 forgePanel/stale-head（head 动了，重读更晚的状态可能成功）
//	too-large → 413 forgePanel/diff-too-large
//	unsupported → 501 + fallback（例如 forgePanel/review-unsupported）
//	其余 → 502 + fallback（not-found / unauthenticated / unavailable）
func forgeRC2ReadFailure(err error, fallback, unsupportedCode string) error {
	code, message := forge.ForgeFailureOf(err)
	if message == "" {
		message = err.Error()
	}
	switch code {
	case forge.FailureStale:
		return transport.ForgeRC2Errorf(409, transport.CodeForgeStaleHead, "%s", message)
	case forge.FailureTooLarge:
		return transport.ForgeRC2Errorf(413, transport.CodeForgeDiffTooLarge, "%s", message)
	case forge.FailureUnsupported:
		if unsupportedCode == "" {
			unsupportedCode = fallback
		}
		return transport.ForgeRC2Unsupported(unsupportedCode, "%s", message)
	}
	status := 502
	if code == forge.FailureUnauthenticated {
		status = 401
	}
	return transport.ForgeRC2Errorf(status, fallback, "%s", message)
}

// forgeRC2ActionFailure 把**写**失败翻成官方码（actionError 1139-1144 口径）。
//
//	stale → 409 forgePanel/stale-head
//	unknown → 502 forgePanel/action-unknown（结果未知，必须先到 host 上核对）
//	其余（含 provider 没有这条能力）→ 502 forgePanel/action-failed
//
// missing 是"provider 不提供这条能力"时的正文（官方 act() 用的是
// `This provider <missing>`），这样用户看到的是"这条能力没有"，而不是一个空失败。
func forgeRC2ActionFailure(err error, missing string) error {
	failure := forge.ActionFailureOf(err)
	if failure == nil {
		return nil
	}
	switch failure.Code {
	case forge.ActionStale:
		return transport.ForgeRC2Errorf(409, transport.CodeForgeStaleHead, "%s", failure.Message)
	case forge.ActionUnknown:
		return transport.ForgeRC2Errorf(502, transport.CodeForgeActionUnknown,
			"%s（结果未知：先去 host 上核对该操作是否生效，不要直接重试）", failure.Message)
	}
	if forge.IsForgeCode(err, forge.FailureUnsupported) {
		return transport.ForgeRC2Errorf(502, transport.CodeForgeActionFailed, "%s", missing)
	}
	return transport.ForgeRC2Errorf(502, transport.CodeForgeActionFailed, "%s", failure.Message)
}

// forgeRC2GitFailure 把本机 git 失败翻成官方码。
//
//	ErrNoForkPoint → forgePanel/no-fork-point（422）
//	ErrBaseMissing → forgePanel/base-missing（422）
//	ErrNotBranchRef → 400 bad_request（用户给的不是分支 ref）
//	ErrNotRepository → forgePanel/not-a-checkout（400）
//	其余 → 502（git 真的失败了）
func forgeRC2GitFailure(err error) error {
	switch {
	case errors.Is(err, forge.ErrNoForkPoint):
		return transport.ForgeRC2Errorf(422, transport.CodeForgeNoForkPoint, "%s", err.Error())
	case errors.Is(err, forge.ErrBaseMissing):
		return transport.ForgeRC2Errorf(422, transport.CodeForgeBaseMissing, "%s", err.Error())
	case errors.Is(err, forge.ErrNotBranchRef):
		return transport.ForgeRC2Errorf(400, "bad_request", "%s", err.Error())
	case errors.Is(err, forge.ErrNotRepository):
		return transport.ForgeRC2Errorf(400, transport.CodeForgeNotACheckout, "%s", err.Error())
	}
	return transport.ForgeRC2Errorf(502, transport.CodeForgeNotACheckout, "%s", err.Error())
}
