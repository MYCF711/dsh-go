// Package forge 的 GitHub Change Request 只读 provider。
//
// 这是 PARITY-W2-PROVIDER 的真实 HTTP 实现：PR 元数据、head commit 的
// check-runs、reviews、repo merge 能力、unified diff、commit 上的文件内容。
// 不写 GitHub（本包只有 GET）；未登录/网络/上游错误都结构化返回，不编造
// PR、checks 或版本。
package forge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"dshgo/internal/transport"
)

type ChangeRequestProvider struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewChangeRequestProviderFromEnv() *ChangeRequestProvider {
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("DSHD_GITHUB_API_URL")), "/")
	return &ChangeRequestProvider{BaseURL: base, Token: token, HTTP: &http.Client{Timeout: 45 * time.Second}}
}

type changeRequestRef struct {
	Host   string
	Owner  string
	Repo   string
	Number int
	WebURL string
}

func parseChangeRequestURL(raw string) (changeRequestRef, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !(u.Scheme == "http" || u.Scheme == "https") {
		return changeRequestRef{}, errors.New("change request url 必须是 http(s) URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return changeRequestRef{}, fmt.Errorf("不是 GitHub change request URL: %s", raw)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return changeRequestRef{}, fmt.Errorf("change request number 非法: %s", parts[3])
	}
	return changeRequestRef{Host: strings.ToLower(u.Host), Owner: parts[0], Repo: parts[1], Number: number, WebURL: u.String()}, nil
}

func (p *ChangeRequestProvider) apiBase(ref changeRequestRef) string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	if ref.Host == "github.com" || strings.HasSuffix(ref.Host, ".github.com") {
		return "https://api.github.com"
	}
	return "https://" + ref.Host + "/api/v3"
}

type ForgeHTTPError struct {
	Status  int
	Code    string
	Message string
}

func (e *ForgeHTTPError) Error() string { return e.Message }

func (p *ChangeRequestProvider) get(ctx context.Context, ref changeRequestRef, path, accept string) ([]byte, error) {
	if p.Token == "" {
		return nil, &ForgeHTTPError{Status: 401, Code: "auth_required", Message: "GitHub 未登录：设置 GITHUB_TOKEN/GH_TOKEN（本宿主不伪造 PR/checks）"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.apiBase(ref), "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, &ForgeHTTPError{Status: 502, Code: "provider_unavailable", Message: "GitHub 网络失败：" + err.Error()}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, &ForgeHTTPError{Status: 502, Code: "provider_unavailable", Message: "读取 GitHub 响应失败：" + readErr.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, forgeHTTPError(resp.StatusCode, path, body)
	}
	return body, nil
}

type githubPR struct {
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	State          string  `json:"state"`
	Draft          bool    `json:"draft"`
	MergedAt       *string `json:"merged_at"`
	ClosedAt       *string `json:"closed_at"`
	UpdatedAt      string  `json:"updated_at"`
	HTMLURL        string  `json:"html_url"`
	Additions      int     `json:"additions"`
	Deletions      int     `json:"deletions"`
	Mergeable      *bool   `json:"mergeable"`
	MergeableState string  `json:"mergeable_state"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		// SHA 是 host 与 head 一起记录的 base commit（官方 snapshot.baseSha）；
		// rc.2 的 diff 从它起步，不再用 /pulls/{n} 的 diff。
		SHA string `json:"sha"`
	} `json:"base"`
	// RequestedReviewers / RequestedTeams 是当前的评审请求（官方 reviewers
	// 的前半段：requested=true）。type 让 bot 账号能与人对上号。
	RequestedReviewers []githubReviewerUser `json:"requested_reviewers"`
	RequestedTeams     []struct {
		Slug         string `json:"slug"`
		Name         string `json:"name"`
		Organization struct {
			Login string `json:"login"`
		} `json:"organization"`
	} `json:"requested_teams"`
}

// githubReviewerUser 是 REST 里的账号形状（评审请求与已提交评审共用）。
type githubReviewerUser struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	Type      string `json:"type"`
}

// githubReview 是一条已提交的 PR review；state 取
// APPROVED/CHANGES_REQUESTED/COMMENTED/DISMISSED/PENDING，commit_id 是该
// review 提交时所在的 commit（官方 reviewers[].reviewedSha）。
type githubReview struct {
	State    string             `json:"state"`
	CommitID string             `json:"commit_id"`
	User     githubReviewerUser `json:"user"`
}

// githubCommit 是 REST commit（PR commits 列表的元素）。
type githubCommit struct {
	SHA     string `json:"sha"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

func (p *ChangeRequestProvider) fetchPR(ctx context.Context, ref changeRequestRef) (githubPR, error) {
	var pr githubPR
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/pulls/%d", ref.Owner, ref.Repo, ref.Number), "application/vnd.github+json")
	if err != nil {
		return pr, err
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return pr, &ForgeHTTPError{Status: 502, Code: "provider_error", Message: "GitHub PR 响应不是 JSON: " + err.Error()}
	}
	return pr, nil
}

// fetchReviews 读 PR 的已提交评审（GET /pulls/{n}/reviews?per_page=100，GitHub
// 按提交时间升序返回）。失败返回 error，由调用方决定是否容忍：checks() 容忍
// （评审读不到不该让整个快照失败，与旧 reviewDecision 的处置一致）。
func (p *ChangeRequestProvider) fetchReviews(ctx context.Context, ref changeRequestRef) ([]githubReview, error) {
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=%d",
		ref.Owner, ref.Repo, ref.Number, gitHubPageSize), "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var reviews []githubReview
	if err := json.Unmarshal(raw, &reviews); err != nil {
		return nil, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
			Message: "GitHub reviews 响应不是 JSON: " + err.Error()}
	}
	return reviews, nil
}

// reviewDecisionFrom 算官方 reviewDecision（approved | changes-requested |
// review-required | none）。只要有未完成的评审请求就先判 review-required
// （旧实现的语义，保留）；否则按每个账号**最新一条**已提交评审聚合。
func reviewDecisionFrom(pr githubPR, reviews []githubReview) string {
	if len(pr.RequestedReviewers) > 0 || len(pr.RequestedTeams) > 0 {
		return "review-required"
	}
	latest := map[string]string{}
	for _, review := range reviews {
		login := strings.TrimSpace(review.User.Login)
		if login == "" || strings.EqualFold(review.State, "PENDING") {
			continue
		}
		latest[strings.ToLower(login)] = strings.ToUpper(review.State)
	}
	decision := "none"
	for _, state := range latest {
		switch state {
		case "CHANGES_REQUESTED":
			return "changes-requested"
		case "APPROVED":
			decision = "approved"
		}
	}
	return decision
}

// reviewerKind 把 REST 账号类型映射成官方 reviewer.kind（Bot → bot，其余 user）。
func reviewerKind(kind string) string {
	if strings.EqualFold(kind, "Bot") {
		return "bot"
	}
	return "user"
}

// reviewVerdict 把 GitHub review state 映射成官方 ReviewVerdict；PENDING 等
// 未提交状态没有 verdict（返回空串 → JSON null）。
func reviewVerdict(state string) string {
	switch strings.ToUpper(state) {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes-requested"
	case "COMMENTED":
		return "commented"
	case "DISMISSED":
		return "dismissed"
	}
	return ""
}

// reviewersFrom 拼官方 snapshot.reviewers：先 requested_reviewers[]（host
// 顺序），再 requested_teams[]（kind=team、login=`org/slug`），最后是已提交
// 评审的账号（requested=false，verdict/reviewedSha 取该账号**最新一条**已提交
// review）；按 login（大小写不敏感）去重，作者永不在列表里（与官方一致）。
//
// codeOwner 只能来自 GraphQL（CODEOWNERS 计算），REST 没有这个信号，一律
// false，不编造。
func reviewersFrom(pr githubPR, reviews []githubReview) []transport.OfficialForgeChangeRequestReviewer {
	out := []transport.OfficialForgeChangeRequestReviewer{}
	seen := map[string]bool{}
	author := strings.ToLower(strings.TrimSpace(pr.User.Login))
	add := func(reviewer transport.OfficialForgeChangeRequestReviewer) {
		key := strings.ToLower(strings.TrimSpace(reviewer.Login))
		if key == "" || seen[key] || key == author {
			return
		}
		seen[key] = true
		out = append(out, reviewer)
	}
	for _, user := range pr.RequestedReviewers {
		login := strings.TrimSpace(user.Login)
		if login == "" {
			continue
		}
		reviewer := transport.OfficialForgeChangeRequestReviewer{Kind: reviewerKind(user.Type), Login: login, Requested: true}
		if avatar := strings.TrimSpace(user.AvatarURL); avatar != "" {
			reviewer.AvatarURL = &avatar
		}
		add(reviewer)
	}
	for _, team := range pr.RequestedTeams {
		slug := strings.TrimSpace(team.Slug)
		if slug == "" {
			continue
		}
		login := slug
		if org := strings.TrimSpace(team.Organization.Login); org != "" {
			login = org + "/" + slug
		}
		add(transport.OfficialForgeChangeRequestReviewer{Kind: "team", Login: login, Requested: true})
	}
	// reviews 是提交时间升序，所以后面的条目覆盖前面的 = 最新一条。
	latest := map[string]githubReview{}
	order := []string{}
	for _, review := range reviews {
		login := strings.TrimSpace(review.User.Login)
		if login == "" || strings.EqualFold(review.State, "PENDING") {
			continue
		}
		key := strings.ToLower(login)
		if _, ok := latest[key]; !ok {
			order = append(order, key)
		}
		latest[key] = review
	}
	for _, key := range order {
		review := latest[key]
		reviewer := transport.OfficialForgeChangeRequestReviewer{
			Kind: reviewerKind(review.User.Type), Login: review.User.Login, Requested: false,
		}
		if avatar := strings.TrimSpace(review.User.AvatarURL); avatar != "" {
			reviewer.AvatarURL = &avatar
		}
		if verdict := reviewVerdict(review.State); verdict != "" {
			reviewer.Verdict = &verdict
		}
		if sha := strings.TrimSpace(review.CommitID); sha != "" {
			reviewer.ReviewedSHA = &sha
		}
		add(reviewer)
	}
	return out
}

// githubCheckRun 是 REST 的一条 check run。ID 是 rc.2 重跑身份的基础：
// 官方 snapshot.checks[].rerun 非 null 时，宿主经 /check-runs/{id}/rerequest
// 重跑它。
type githubCheckRun struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Conclusion  string  `json:"conclusion"`
	DetailsURL  string  `json:"details_url"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	CheckSuite  struct {
		Name string `json:"name"`
	} `json:"check_suite"`
}

type githubCheckRuns struct {
	TotalCount int              `json:"total_count"`
	CheckRuns  []githubCheckRun `json:"check_runs"`
}

// checkRuns 读 headSHA 这个 commit 的 check-runs（只读第一页）。调用方负责先
// 用 fetchPR 确认它就是当前 head（checks / ChangeRequestRerunChecks 都先做这
// 一步，head 已移动就 409 stale_head，不发这次读）。
func (p *ChangeRequestProvider) checkRuns(ctx context.Context, ref changeRequestRef, headSHA string) (githubCheckRuns, error) {
	var runs githubCheckRuns
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=%d",
		ref.Owner, ref.Repo, headSHA, gitHubPageSize), "application/vnd.github+json")
	if err != nil {
		return runs, err
	}
	if err := json.Unmarshal(raw, &runs); err != nil {
		return runs, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
			Message: "checks 响应不是 JSON: " + err.Error()}
	}
	return runs, nil
}

// checkState 把 GitHub 的 status/conclusion 投影成官方 CheckState。
func checkState(status, conclusion string) string {
	if !strings.EqualFold(status, "completed") {
		return "pending"
	}
	switch strings.ToLower(conclusion) {
	case "success":
		return "success"
	case "neutral":
		return "neutral"
	case "skipped":
		return "skipped"
	case "cancelled":
		return "cancelled"
	case "failure", "timed_out", "action_required", "startup_failure":
		return "failure"
	}
	return "neutral"
}

// rerunIdentity 返回官方 snapshot.checks[].rerun：只有 completed 且 conclusion
// 属于失败族（failure/timed_out/action_required/startup_failure/cancelled）且
// host 给了 check run id 的项才带重跑身份；其余是 JSON null（不可重跑）。
// 注意 cancelled 的 state 是 cancelled（不是 failure），但按官方口径它仍可重跑。
func rerunIdentity(run githubCheckRun) *string {
	if !strings.EqualFold(run.Status, "completed") || run.ID == 0 {
		return nil
	}
	switch strings.ToLower(run.Conclusion) {
	case "failure", "timed_out", "action_required", "startup_failure", "cancelled":
		id := strconv.Itoa(int(run.ID))
		return &id
	}
	return nil
}

func (p *ChangeRequestProvider) checks(ctx context.Context, ref changeRequestRef, headSHA string) (OfficialChecks, error) {
	pr, err := p.fetchPR(ctx, ref)
	if err != nil {
		return OfficialChecks{}, err
	}
	if headSHA != "" && !strings.EqualFold(headSHA, pr.Head.SHA) {
		return OfficialChecks{}, staleHead(headSHA, pr.Head.SHA)
	}
	runs, err := p.checkRuns(ctx, ref, pr.Head.SHA)
	if err != nil {
		return OfficialChecks{}, err
	}
	// 评审读不到不该让整个快照失败（旧 reviewDecision 同样容忍）；但 reviewers
	// 就只剩评审请求的部分，这是真实可得信息的边界，不编造。
	reviews, _ := p.fetchReviews(ctx, ref)
	checks := make([]transport.OfficialForgeChangeRequestCheck, 0, len(runs.CheckRuns))
	for _, run := range runs.CheckRuns {
		state := checkState(run.Status, run.Conclusion)
		check := transport.OfficialForgeChangeRequestCheck{
			Name: run.Name, State: state, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
			Rerun: rerunIdentity(run),
		}
		if run.DetailsURL != "" {
			u := run.DetailsURL
			check.URL = &u
		}
		if run.CheckSuite.Name != "" {
			workflow := run.CheckSuite.Name
			check.Workflow = &workflow
		}
		checks = append(checks, check)
	}
	rollup := "none"
	if len(checks) > 0 {
		rollup = "success"
		for _, check := range checks {
			switch check.State {
			case "pending":
				rollup = "pending"
			case "success", "neutral", "skipped":
				rollup = "success"
			case "failure", "cancelled":
				rollup = "failure"
			}
		}
	}
	decision := reviewDecisionFrom(pr, reviews)
	mergeability := "unknown"
	if pr.Mergeable != nil {
		if *pr.Mergeable {
			mergeability = "mergeable"
		} else {
			mergeability = "conflicting"
		}
	}
	closedAt := pr.ClosedAt
	if pr.MergedAt != nil {
		closedAt = pr.MergedAt
	}
	state := "open"
	if pr.MergedAt != nil {
		state = "merged"
	} else if strings.EqualFold(pr.State, "closed") {
		state = "closed"
	}
	var author *string
	if pr.User.Login != "" {
		login := pr.User.Login
		author = &login
	}
	var baseSHA *string
	if base := strings.TrimSpace(pr.Base.SHA); base != "" {
		baseSHA = &base
	}
	// BranchUpdates：REST 只有 mergeable_state=="behind" 这一个"head 落后 base"
	// 的信号；官方语义要求空数组表示 head 已最新 / 冲突 / 用户无权更新 —— 这三种
	// 我们无法区分，所以只在明确 behind 且 open 时才给 merge+rebase。
	branchUpdates := []string{}
	if state == "open" && strings.EqualFold(strings.TrimSpace(pr.MergeableState), "behind") {
		branchUpdates = []string{"merge", "rebase"}
	}
	// CanRerunChecks：存在可重跑失败项，且**没有任何** pending check（简化口径：
	// 官方按重跑身份判断同身份的 pending，REST 侧只看得到 check run，按全局
	// pending 保守处理）。
	canRerun := false
	pending := false
	for _, check := range checks {
		if check.Rerun != nil {
			canRerun = true
		}
		if check.State == "pending" {
			pending = true
		}
	}
	res := transport.OfficialForgeChangeRequestChecks{
		Ref: transport.OfficialForgeChangeRequestRef{Forge: "github", Host: ref.Host, Owner: ref.Owner, Name: ref.Repo, Number: pr.Number},
		URL: pr.HTMLURL, Title: pr.Title, State: state, Draft: pr.Draft, Author: author,
		HeadBranch: pr.Head.Ref, HeadSHA: pr.Head.SHA, BaseBranch: pr.Base.Ref,
		Additions: pr.Additions, Deletions: pr.Deletions, Mergeability: mergeability,
		MergeReady:     state == "open" && !pr.Draft && mergeability == "mergeable" && decision != "changes-requested" && rollup != "failure",
		ReviewDecision: decision, Checks: checks, ChecksRollup: rollup,
		ChecksTruncated: runs.TotalCount > len(checks), UnresolvedThreads: 0,
		UpdatedAt: pr.UpdatedAt, ClosedAt: closedAt,

		BaseSHA:       baseSHA,
		BranchUpdates: branchUpdates,
		Reviewers:     reviewersFrom(pr, reviews),
		// REST 的 reviews/requested_reviewers 一次拿全（per_page=100），没有
		// "host 还有更多"的信号，所以不声称截断。
		ReviewersTruncated: false,
		// 派生能力位，不是权限位：open + 有 token（官方同样只按可否调用判断）。
		CanRequestReviews: state == "open" && strings.TrimSpace(p.Token) != "",
		CanRerunChecks:    canRerun && !pending,
		// 官方 threadsTruncated：REST 没有 review-thread 的解析态，unresolvedThreads
		// 只能是下界 0，因此**必须**报截断，不能把它说成"已统计"。
		ThreadsTruncated: true,
		// closingIssues 只能来自 GraphQL 的 closingIssuesReferences；官方 migration
		// 允许 host 不给时用空数组 + false，这里不编造 issue。
		ClosingIssues:          []transport.OfficialForgeChangeRequestIssue{},
		ClosingIssuesTruncated: false,
	}
	res.MergeMethods = p.mergeMethods(ctx, ref)
	return res, nil
}

func (p *ChangeRequestProvider) mergeMethods(ctx context.Context, ref changeRequestRef) []string {
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s", ref.Owner, ref.Repo), "application/vnd.github+json")
	if err != nil {
		return []string{"merge"}
	}
	var repo struct {
		Merge  bool `json:"allow_merge_commit"`
		Squash bool `json:"allow_squash_merge"`
		Rebase bool `json:"allow_rebase_merge"`
	}
	if json.Unmarshal(raw, &repo) != nil {
		return []string{"merge"}
	}
	out := []string{}
	if repo.Merge {
		out = append(out, "merge")
	}
	if repo.Squash {
		out = append(out, "squash")
	}
	if repo.Rebase {
		out = append(out, "rebase")
	}
	if len(out) == 0 {
		out = []string{"merge"}
	}
	return out
}

type OfficialChecks = transport.OfficialForgeChangeRequestChecks

func (p *ChangeRequestProvider) ChangeRequestChecks(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (transport.OfficialForgeChangeRequestChecks, error) {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return transport.OfficialForgeChangeRequestChecks{}, &ForgeHTTPError{Status: 400, Code: "bad_request", Message: err.Error()}
	}
	return p.checks(ctx, ref, params.HeadSHA)
}

// ChangeRequestDiff 读 PR 在某个 head 上的 unified diff。
//
// rc.2 口径（官方 ForgeProvider.diff(ref, headSha, base?)）：diff **必须**与一个
// head 绑定。params.HeadSHA 非空且已不是当前 head → 409 stale_head；否则走
// compare REST（`GET /repos/{o}/{r}/compare/{baseSha}...{headSha}`，Accept
// application/vnd.github.v3.diff），不再用 `/pulls/{n}` 的 diff —— compare 从
// base.sha 起步、两端都固定，且不受 PR 页面 300 文件上限影响。
// params.HeadSHA 为空时按当前 head 读，并在结果里回真实 head（老调用方兼容）。
func (p *ChangeRequestProvider) ChangeRequestDiff(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (transport.OfficialForgeChangeRequestDiff, error) {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return transport.OfficialForgeChangeRequestDiff{}, &ForgeHTTPError{Status: 400, Code: "bad_request", Message: err.Error()}
	}
	pr, err := p.fetchPR(ctx, ref)
	if err != nil {
		return transport.OfficialForgeChangeRequestDiff{}, err
	}
	if params.HeadSHA != "" && !strings.EqualFold(params.HeadSHA, pr.Head.SHA) {
		return transport.OfficialForgeChangeRequestDiff{}, staleHead(params.HeadSHA, pr.Head.SHA)
	}
	head := pr.Head.SHA
	base := strings.TrimSpace(pr.Base.SHA)
	if base == "" {
		// base.sha 缺失（老宿主/替身）时退回 base 分支名：compare 会把分支名解析
		// 成它当前的 tip，与 /pulls/{n} 的 diff 语义一致（都是 merge base 起点）。
		base = strings.TrimSpace(pr.Base.Ref)
	}
	if base == "" || head == "" {
		return transport.OfficialForgeChangeRequestDiff{}, &ForgeHTTPError{Status: 502, Code: "provider_error",
			Message: "GitHub PR 没有 base commit / head commit，无法读 compare diff"}
	}
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/compare/%s...%s",
		ref.Owner, ref.Repo, compareRef(base), compareRef(head)), "application/vnd.github.v3.diff")
	if err != nil {
		return transport.OfficialForgeChangeRequestDiff{}, err
	}
	files, err := parseUnifiedDiff(string(raw))
	if err != nil {
		return transport.OfficialForgeChangeRequestDiff{}, &ForgeHTTPError{Status: 502, Code: "provider_error", Message: err.Error()}
	}
	urls := map[string]string{}
	for _, file := range files {
		urls[file.Path] = "https://" + ref.Host + "/" + ref.Owner + "/" + ref.Repo + "/blob/" + head + "/" + file.Path
	}
	return transport.OfficialForgeChangeRequestDiff{HeadSHA: head, Files: files, FileURLs: urls}, nil
}

// ChangeRequestDiffAtHead 是官方 diff(ref, headSha) 的直译：headSha **必填**，
// 必填为空 → 400 invalid_spec；head 已移动 → 409 stale_head。实现复用
// ChangeRequestDiff（params.HeadSHA 非空时它做的正是这两件事 + compare 读取）。
func (p *ChangeRequestProvider) ChangeRequestDiffAtHead(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (transport.OfficialForgeChangeRequestDiff, error) {
	if strings.TrimSpace(params.HeadSHA) == "" {
		return transport.OfficialForgeChangeRequestDiff{}, &ForgeHTTPError{Status: 400, Code: "invalid_spec",
			Message: "diffAtHead 需要 headSha（官方 diff(ref, headSha) 不允许省略）"}
	}
	return p.ChangeRequestDiff(ctx, params)
}

func (p *ChangeRequestProvider) ChangeRequestText(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (string, error) {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return "", &ForgeHTTPError{Status: 400, Code: "bad_request", Message: err.Error()}
	}
	path := strings.TrimPrefix(params.Path, "/")
	if path == "" || strings.Contains(path, "..") {
		return "", &ForgeHTTPError{Status: 400, Code: "bad_request", Message: "path 非法"}
	}
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", ref.Owner, ref.Repo, url.PathEscape(path), url.QueryEscape(params.Commit)), "application/vnd.github+json")
	if err != nil {
		return "", err
	}
	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if json.Unmarshal(raw, &file) == nil && file.Content != "" && strings.EqualFold(file.Encoding, "base64") {
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
		if err != nil {
			return "", &ForgeHTTPError{Status: 502, Code: "provider_error", Message: "GitHub 文件内容 base64 无效"}
		}
		return string(decoded), nil
	}
	return string(raw), nil
}

func (p *ChangeRequestProvider) ChangeRequestStates(ctx context.Context, _ transport.OfficialForgeChangeRequestParams) (transport.OfficialForgeChangeRequestStatesFrame, error) {
	frame := transport.OfficialForgeChangeRequestStatesFrame{Type: "baseline", States: []transport.OfficialForgeChangeRequestState{}}
	raw := strings.TrimSpace(os.Getenv("DSHD_FORGE_CHANGE_REQUEST_URLS"))
	if raw == "" {
		return frame, nil
	}
	urls := strings.Split(raw, ",")
	sort.Strings(urls)
	for _, item := range urls {
		ref, err := parseChangeRequestURL(item)
		if err != nil {
			return frame, &ForgeHTTPError{Status: 400, Code: "bad_request", Message: err.Error()}
		}
		checks, err := p.checks(ctx, ref, "")
		if err != nil {
			return frame, err
		}
		frame.States = append(frame.States, transport.OfficialForgeChangeRequestState{
			Title: checks.Title, State: checks.State, URL: checks.URL, Draft: checks.Draft,
			ChecksRollup: checks.ChecksRollup, ReviewDecision: checks.ReviewDecision,
			Mergeability: checks.Mergeability, Number: checks.Ref.Number,
		})
	}
	return frame, nil
}

func parseUnifiedDiff(raw string) ([]transport.OfficialForgeChangeRequestFile, error) {
	if strings.TrimSpace(raw) == "" {
		return []transport.OfficialForgeChangeRequestFile{}, nil
	}
	lines := strings.Split(raw, "\n")
	files := []transport.OfficialForgeChangeRequestFile{}
	var current *transport.OfficialForgeChangeRequestFile
	var hunk *transport.OfficialForgeChangeRequestHunk
	var previousPath string
	flushHunk := func() {
		if current != nil && hunk != nil {
			current.Hunks = append(current.Hunks, *hunk)
			hunk = nil
		}
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushHunk()
			files = append(files, transport.OfficialForgeChangeRequestFile{})
			current = &files[len(files)-1]
		case current == nil:
			continue
		case strings.HasPrefix(line, "new file mode"):
			current.Status = "added"
		case strings.HasPrefix(line, "deleted file mode"):
			current.Status = "deleted"
		case strings.HasPrefix(line, "rename from "):
			previousPath = strings.TrimPrefix(line, "rename from ")
			current.PreviousPath = &previousPath
			current.Status = "renamed"
		case strings.HasPrefix(line, "rename to "):
			current.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "--- "):
			if current.Path == "" {
				current.Path = strings.TrimPrefix(strings.TrimPrefix(line, "--- "), "a/")
			}
		case strings.HasPrefix(line, "+++ "):
			current.Path = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case strings.HasPrefix(line, "Binary files "):
			current.Binary = true
		case strings.HasPrefix(line, "@@ "):
			flushHunk()
			oldStart, oldLines, newStart, newLines := parseHunkHeader(line)
			hunk = &transport.OfficialForgeChangeRequestHunk{OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines}
		case hunk != nil && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\\")):
			hunk.Lines = append(hunk.Lines, line)
			if strings.HasPrefix(line, "+") {
				current.Additions++
			} else if strings.HasPrefix(line, "-") {
				current.Deletions++
			}
		}
	}
	flushHunk()
	for i := range files {
		if files[i].Status == "" {
			files[i].Status = "modified"
		}
		if files[i].Path == "" {
			return nil, errors.New("diff 文件缺路径")
		}
	}
	return files, nil
}

func parseHunkHeader(line string) (oldStart, oldLines, newStart, newLines int) {
	_, _ = fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &oldStart, &oldLines, &newStart, &newLines)
	if oldLines == 0 && oldStart == 0 {
		_, _ = fmt.Sscanf(line, "@@ -%d +%d,%d @@", &oldStart, &newStart, &newLines)
	}
	return
}

func truncateForge(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
