// Package forge 的官方 rc.2 Forge provider 面。
//
// 本文件承载 rc.2 新增的失败码（ForgeFailure / ForgeActionFailure）与读/写
// 能力：提交列表、评审人候选与评审请求、失败 check 重跑、分支更新，以及只能
// 走 GraphQL 的能力（viewed 标记、review threads 读写）的**结构化 unsupported**。
//
// 依据官方 @deepseek-ai/dsh-experimental-forge/src/types.ts：
//   - ChangeRequestReviewer（76-92）、ChangeRequestIssue（94-106）
//   - ChangeRequestSnapshot（108-180）、ForgeFailure（182-193）
//   - ForgeReviewersRequest（255-259）、ForgeReviewerCandidate（237-247）
//   - ForgeReviewResult / ForgeReviewWriteResult（405-411）
//   - ForgeActionFailure（413-424）、ForgeCommit（426-438）、ForgeCommitList（440-448）
//   - ForgeViewedState / ForgeViewedFiles（450-470）
//   - ForgeProvider（488-579，commits / reviewerCandidates / requestReviews /
//     rerunChecks / updateBranch / viewedFiles / setViewed / readReview /
//     writeReview 的语义）
//
// 全部走**真实 GitHub REST**（测试只用 local httptest，不联网）：拿不到的
// 数据就返回结构化 unsupported（501）/ 真实 HTTP 错误码，绝不返回空结构冒充
// 结果，也绝不编造 reviewer / commit / check。
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"dshgo/internal/transport"
)

// 官方 ForgeFailure['code']（types.ts:182-193）。
const (
	// FailureNotFound：host 不暴露它（GitHub 404）。
	FailureNotFound = "not-found"
	// FailureUnauthenticated：凭据缺失或被拒（401 / 未配置 token）。
	FailureUnauthenticated = "unauthenticated"
	// FailureTooLarge：host 或 provider 拒绝超过体积上限的答复。
	FailureTooLarge = "too-large"
	// FailureStale：读命名的 head 已不是当前 head。
	FailureStale = "stale"
	// FailureUnsupported：本 provider 不提供该读取。
	FailureUnsupported = "unsupported"
	// FailureUnavailable：其它任何失败（含 5xx / 网络 / 403）。
	FailureUnavailable = "unavailable"
)

// 官方 ForgeActionFailure['code']（types.ts:413-424）。
const (
	// ActionUnauthenticated：凭据缺失或被拒。
	ActionUnauthenticated = "unauthenticated"
	// ActionStale：head 已越过本次确认的 commit。
	ActionStale = "stale"
	// ActionFailed：host 拒绝或不可达，**什么都没改**。
	ActionFailed = "failed"
	// ActionUnknown：请求已到达 host 但答复没确认结果，可能已生效。
	ActionUnknown = "unknown"
)

// gitHubPageSize 是 GitHub REST 列表接口的单页上限（本 provider 只读第一页）。
const gitHubPageSize = 100

// maxReviewerCandidates 是评审人候选的返回上限，与单页上限一致。
const maxReviewerCandidates = gitHubPageSize

// Commit 是官方 ForgeCommit（types.ts:426-438）。
type Commit struct {
	SHA    string  `json:"sha"`
	Parent *string `json:"parent"`
	Title  string  `json:"title"`
	Author *string `json:"author"`
	// AuthoredAt 是 ISO 8601 作者时间。
	AuthoredAt string `json:"authoredAt"`
}

// CommitList 是官方 ForgeCommitList（types.ts:440-448）：commits 为 oldest
// first，truncated 表示列出的只是最早的一批。
type CommitList struct {
	HeadSHA   string   `json:"headSha"`
	Commits   []Commit `json:"commits"`
	Truncated bool     `json:"truncated"`
}

// ReviewerRef 是官方 ForgeReviewersRequest['reviewers'] 的元素
// （types.ts:255-259，Pick<ChangeRequestReviewer,'kind'|'login'>）：kind 取
// user/bot/team，team 的 login 是 `organization/slug`。
type ReviewerRef struct {
	Kind  string `json:"kind"`
	Login string `json:"login"`
}

// ReviewerCandidate 是官方 ForgeReviewerCandidate（types.ts:237-247）。
type ReviewerCandidate struct {
	Login     string  `json:"login"`
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatarUrl"`
	// Suggested 只能来自 GraphQL（历史/代码所有者建议）；REST assignees 拿不到
	// 这个信号，所以本 provider 一律 false，不冒充建议。
	Suggested bool `json:"suggested"`
}

// ActionFailure 是官方 ForgeActionFailure（types.ts:413-424）：Code 取
// ActionXxx 常量。
type ActionFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ViewedFiles 是官方 ForgeViewedFiles（types.ts:455-470）：Files 的值是
// viewed/unviewed/dismissed。
type ViewedFiles struct {
	HeadSHA   string            `json:"headSha"`
	Files     map[string]string `json:"files"`
	Truncated bool              `json:"truncated"`
}

// ReviewWriteResult 是官方 ForgeReviewWriteResult 判别 union 的失败分支
// （types.ts:405-408）：Code 取 unsupported|stale|failed|unknown。
type ReviewWriteResult struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ---- 结构化失败：错误 → 官方码 ----

// unsupportedMessage 是结构化 501 的唯一文案来源（Unsupported 与
// ReviewWriteResult 共用，避免两处漂移）。
func unsupportedMessage(what string) string {
	return what + " 需要 GraphQL，本 provider 未实现"
}

// Unsupported 返回官方 `unsupported`（501）失败：官方这些能力只能走 GraphQL
// （review threads / viewed 标记 / 建议评审人），本 provider 是 REST-only。
func Unsupported(what string) error {
	return &ForgeHTTPError{Status: http.StatusNotImplemented, Code: FailureUnsupported, Message: unsupportedMessage(what)}
}

// IsForgeCode 判断 err 是否携带指定内部码（stale_head / auth_required / ...）。
func IsForgeCode(err error, code string) bool {
	var httpErr *ForgeHTTPError
	return errors.As(err, &httpErr) && httpErr.Code == code
}

// staleHead 是 head 已移动的唯一构造函数（409 / stale_head）。
func staleHead(shown, current string) *ForgeHTTPError {
	return &ForgeHTTPError{Status: http.StatusConflict, Code: "stale_head",
		Message: fmt.Sprintf("head 已移动：显示 %s，当前 %s", shown, current)}
}

// ForgeFailureOf 把 provider 错误折成官方 ForgeFailure 码；nil → ("","")。
//
// 映射（官方 types.ts:182-193 的语义）：
//   - stale_head（409）       → stale：head 变了，换新 head 重读能成功
//   - auth_required（401）    → unauthenticated
//   - not_found（404）        → not-found：host 不暴露它
//   - unsupported（501）      → unsupported
//   - too_large（413）        → too-large
//   - 5xx / 网络 / 403 / 其它 → unavailable（官方唯一的兜底码）
func ForgeFailureOf(err error) (code string, message string) {
	if err == nil {
		return "", ""
	}
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) {
		return FailureUnavailable, err.Error()
	}
	switch httpErr.Code {
	case "stale_head":
		return FailureStale, httpErr.Message
	case "auth_required":
		return FailureUnauthenticated, httpErr.Message
	case "not_found":
		return FailureNotFound, httpErr.Message
	case FailureUnsupported:
		return FailureUnsupported, httpErr.Message
	case "too_large", FailureTooLarge:
		return FailureTooLarge, httpErr.Message
	case "provider_unavailable", "provider_error", "forbidden", "invalid_spec", "bad_request":
		return FailureUnavailable, httpErr.Message
	}
	switch {
	case httpErr.Status == http.StatusUnauthorized:
		return FailureUnauthenticated, httpErr.Message
	case httpErr.Status == http.StatusNotFound:
		return FailureNotFound, httpErr.Message
	case httpErr.Status == http.StatusNotImplemented:
		return FailureUnsupported, httpErr.Message
	case httpErr.Status == http.StatusConflict:
		return FailureStale, httpErr.Message
	case httpErr.Status == http.StatusRequestEntityTooLarge:
		return FailureTooLarge, httpErr.Message
	}
	return FailureUnavailable, httpErr.Message
}

// ActionFailureOf 把写侧错误折成官方 ForgeActionFailure；nil → nil。
//
// 关键区别是"有没有可能已经改了"：5xx / 网络失败（provider_unavailable）时请求
// 可能已到达 host，官方语义要求报 unknown，让调用方先去 host 核对再重试；
// 4xx 拒绝则可确定没改，报 failed。
func ActionFailureOf(err error) *ActionFailure {
	if err == nil {
		return nil
	}
	var httpErr *ForgeHTTPError
	if !errors.As(err, &httpErr) {
		return &ActionFailure{Code: ActionFailed, Message: err.Error()}
	}
	switch httpErr.Code {
	case "stale_head":
		return &ActionFailure{Code: ActionStale, Message: httpErr.Message}
	case "auth_required":
		return &ActionFailure{Code: ActionUnauthenticated, Message: httpErr.Message}
	case "provider_unavailable", "provider_error":
		return &ActionFailure{Code: ActionUnknown, Message: httpErr.Message}
	case FailureUnsupported:
		// 能力本就不存在，host 那边什么都没发生 → failed（不是 unknown）。
		return &ActionFailure{Code: ActionFailed, Message: httpErr.Message}
	}
	switch {
	case httpErr.Status == http.StatusConflict:
		return &ActionFailure{Code: ActionStale, Message: httpErr.Message}
	case httpErr.Status == http.StatusUnauthorized:
		return &ActionFailure{Code: ActionUnauthenticated, Message: httpErr.Message}
	case httpErr.Status >= 500:
		return &ActionFailure{Code: ActionUnknown, Message: httpErr.Message}
	}
	return &ActionFailure{Code: ActionFailed, Message: httpErr.Message}
}

// ---- 写请求出口：post / put ----

// forgeHTTPError 是 GET / POST / PUT 共用的状态码 → 内部码映射，保证只读与写
// 面同源（401 auth_required、403 forbidden、404 not_found、409 stale_head、
// 422/400 invalid_spec、413 too_large、5xx provider_unavailable）。
func forgeHTTPError(status int, label string, body []byte) *ForgeHTTPError {
	code := "provider_error"
	switch {
	case status == http.StatusUnauthorized:
		code = "auth_required"
	case status == http.StatusForbidden:
		code = "forbidden"
	case status == http.StatusNotFound:
		code = "not_found"
	case status == http.StatusConflict:
		code = "stale_head"
	case status == http.StatusUnprocessableEntity, status == http.StatusBadRequest:
		code = "invalid_spec"
	case status == http.StatusRequestEntityTooLarge:
		code = "too_large"
	case status == http.StatusNotImplemented:
		code = FailureUnsupported
	case status >= 500:
		code = "provider_unavailable"
	}
	return &ForgeHTTPError{Status: status, Code: code,
		Message: fmt.Sprintf("GitHub %s 返回 HTTP %d: %s", label, status, truncateForge(string(body), 500))}
}

// post / put 是 rc.2 写能力的唯一出口：与只读 get 同源（同一个 apiBase、同一
// 套鉴权头、同一套错误映射），响应体上限 8MB（io.LimitReader），**不重试**。
func (p *ChangeRequestProvider) post(ctx context.Context, ref changeRequestRef, path string, body any) ([]byte, error) {
	return p.writeRequest(ctx, http.MethodPost, ref, path, body)
}

func (p *ChangeRequestProvider) put(ctx context.Context, ref changeRequestRef, path string, body any) ([]byte, error) {
	return p.writeRequest(ctx, http.MethodPut, ref, path, body)
}

func (p *ChangeRequestProvider) writeRequest(ctx context.Context, method string, ref changeRequestRef, path string, body any) ([]byte, error) {
	if strings.TrimSpace(p.Token) == "" {
		return nil, &ForgeHTTPError{Status: http.StatusUnauthorized, Code: "auth_required",
			Message: "GitHub 未登录：设置 GITHUB_TOKEN/GH_TOKEN（本宿主不伪造写结果）"}
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: err.Error()}
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.apiBase(ref), "/")+path, payload)
	if err != nil {
		return nil, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: err.Error()}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "dsh-go/0.2.2")
	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_unavailable",
			Message: "GitHub 写请求失败：" + err.Error()}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_unavailable",
			Message: "读取 GitHub 写响应失败：" + readErr.Error()}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	return nil, forgeHTTPError(resp.StatusCode, method+" "+path, raw)
}

// ---- 读能力 ----

// ChangeRequestCommits 读 PR 的提交列表（官方 commits，types.ts:488-579）。
//
// GET /repos/{o}/{r}/pulls/{n}/commits?per_page=100：GitHub 就是 oldest first，
// 所以不重排；HeadSHA 取同一时刻 fetchPR 的 head（不是列表最后一条，避免
// 分页/竞态时把列表尾部当成 head）；Truncated = 拿满一页。
func (p *ChangeRequestProvider) ChangeRequestCommits(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (CommitList, error) {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return CommitList{}, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	pr, err := p.fetchPR(ctx, ref)
	if err != nil {
		return CommitList{}, err
	}
	if params.HeadSHA != "" && !strings.EqualFold(params.HeadSHA, pr.Head.SHA) {
		return CommitList{}, staleHead(params.HeadSHA, pr.Head.SHA)
	}
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/pulls/%d/commits?per_page=%d",
		ref.Owner, ref.Repo, ref.Number, gitHubPageSize), "application/vnd.github+json")
	if err != nil {
		return CommitList{}, err
	}
	var items []githubCommit
	if err := json.Unmarshal(raw, &items); err != nil {
		return CommitList{}, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
			Message: "GitHub commit 列表不是 JSON: " + err.Error()}
	}
	list := CommitList{HeadSHA: pr.Head.SHA, Commits: make([]Commit, 0, len(items)),
		Truncated: len(items) >= gitHubPageSize}
	for _, item := range items {
		list.Commits = append(list.Commits, toCommit(item))
	}
	return list, nil
}

// toCommit 把 REST commit 投影成官方 ForgeCommit：Parent 取第一父提交，
// Title 取 commit message 第一行，Author 取 commit.author.name（缺失为 null），
// AuthoredAt 取 commit.author.date。
func toCommit(item githubCommit) Commit {
	commit := Commit{SHA: item.SHA, Title: firstLineOf(item.Commit.Message), AuthoredAt: item.Commit.Author.Date}
	if len(item.Parents) > 0 {
		if parent := strings.TrimSpace(item.Parents[0].SHA); parent != "" {
			commit.Parent = &parent
		}
	}
	if author := strings.TrimSpace(item.Commit.Author.Name); author != "" {
		commit.Author = &author
	}
	return commit
}

// firstLineOf 取多行文本的第一行（官方 title / message 都是"第一行"语义）。
func firstLineOf(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ChangeRequestReviewerCandidates 读评审人候选（官方 reviewerCandidates）。
//
// GET /repos/{o}/{r}/assignees?per_page=100（仓库可指派账号）；query 大小写
// 不敏感地匹配 login 或 name，空串不过滤。Suggested 恒为 false：官方建议来自
// GraphQL（改动文件历史 / 代码所有者），REST assignees 没有这个信号，不编造。
func (p *ChangeRequestProvider) ChangeRequestReviewerCandidates(ctx context.Context, params transport.OfficialForgeChangeRequestParams, query string) ([]ReviewerCandidate, error) {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return nil, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/assignees?per_page=%d",
		ref.Owner, ref.Repo, gitHubPageSize), "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var users []struct {
		Login     string  `json:"login"`
		Name      *string `json:"name"`
		AvatarURL string  `json:"avatar_url"`
	}
	if err := json.Unmarshal(raw, &users); err != nil {
		return nil, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
			Message: "GitHub assignees 响应不是 JSON: " + err.Error()}
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	candidates := []ReviewerCandidate{}
	for _, user := range users {
		login := strings.TrimSpace(user.Login)
		if login == "" {
			continue
		}
		name := ""
		if user.Name != nil {
			name = *user.Name
		}
		if needle != "" &&
			!strings.Contains(strings.ToLower(login), needle) &&
			!strings.Contains(strings.ToLower(name), needle) {
			continue
		}
		candidate := ReviewerCandidate{Login: login, Name: user.Name, Suggested: false}
		if avatar := strings.TrimSpace(user.AvatarURL); avatar != "" {
			candidate.AvatarURL = &avatar
		}
		candidates = append(candidates, candidate)
		if len(candidates) >= maxReviewerCandidates {
			break
		}
	}
	return candidates, nil
}

// ---- 写能力 ----

// ChangeRequestRequestReviews 请求追加评审人（官方 requestReviews）。
//
// POST /repos/{o}/{r}/pulls/{n}/requested_reviewers；kind=team 进
// team_reviewers（只取 `organization/slug` 的 slug），其余进 reviewers。
// 官方没有 headSha 参数：这里只做 PR 存在性 / 登录态 preflight，head 不参与
// 语义（GitHub 的 requested_reviewers 不绑定 commit）。
func (p *ChangeRequestProvider) ChangeRequestRequestReviews(ctx context.Context, params transport.OfficialForgeChangeRequestParams, reviewers []ReviewerRef) error {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	if _, err := p.fetchPR(ctx, ref); err != nil {
		return err
	}
	users := []string{}
	teams := []string{}
	seenUser := map[string]bool{}
	seenTeam := map[string]bool{}
	for _, reviewer := range reviewers {
		login := strings.TrimSpace(reviewer.Login)
		if login == "" {
			continue
		}
		if strings.EqualFold(reviewer.Kind, "team") {
			slug := login
			if i := strings.LastIndex(login, "/"); i >= 0 {
				slug = strings.TrimSpace(login[i+1:])
			}
			if slug == "" || seenTeam[strings.ToLower(slug)] {
				continue
			}
			seenTeam[strings.ToLower(slug)] = true
			teams = append(teams, slug)
			continue
		}
		if seenUser[strings.ToLower(login)] {
			continue
		}
		seenUser[strings.ToLower(login)] = true
		users = append(users, login)
	}
	if len(users) == 0 && len(teams) == 0 {
		return &ForgeHTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_spec", Message: "没有要请求的评审人"}
	}
	_, err = p.post(ctx, ref, fmt.Sprintf("/repos/%s/%s/pulls/%d/requested_reviewers", ref.Owner, ref.Repo, ref.Number),
		map[string]any{"reviewers": users, "team_reviewers": teams})
	return err
}

// ChangeRequestRerunChecks 重跑 head commit 上的失败 check（官方 rerunChecks）。
//
// 先 fetchPR 确认 head（headSHA 非空且已移动 → 409 stale_head），再重读
// check-runs，对每个失败且带 id 的 check run 打
// POST /repos/{o}/{r}/check-runs/{id}/rerequest（2xx 视为接受）。
// 没有任何可重跑项 → 422 invalid_spec（官方 failed 的语义）。
func (p *ChangeRequestProvider) ChangeRequestRerunChecks(ctx context.Context, params transport.OfficialForgeChangeRequestParams, headSHA string) error {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	pr, err := p.fetchPR(ctx, ref)
	if err != nil {
		return err
	}
	if headSHA != "" && !strings.EqualFold(headSHA, pr.Head.SHA) {
		return staleHead(headSHA, pr.Head.SHA)
	}
	runs, err := p.checkRuns(ctx, ref, pr.Head.SHA)
	if err != nil {
		return err
	}
	ids := []int64{}
	for _, run := range runs.CheckRuns {
		if rerunIdentity(run) != nil {
			ids = append(ids, run.ID)
		}
	}
	if len(ids) == 0 {
		return &ForgeHTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_spec", Message: "没有可重跑的失败 check"}
	}
	for _, id := range ids {
		if _, err := p.post(ctx, ref, fmt.Sprintf("/repos/%s/%s/check-runs/%d/rerequest",
			ref.Owner, ref.Repo, id), nil); err != nil {
			return err
		}
	}
	return nil
}

// ChangeRequestUpdateBranch 把 base 分支的新提交带进 head 分支（官方
// updateBranch）：PUT /repos/{o}/{r}/pulls/{n}/update-branch，body 带
// update_method=merge|rebase；先 fetchPR 校验 headSHA（已移动 → stale_head）。
// GitHub 用 409/422 报冲突，这里统一折成 invalid_spec（**不是** stale_head：
// head 没动，冲突是这次更新的输入不成立）。
func (p *ChangeRequestProvider) ChangeRequestUpdateBranch(ctx context.Context, params transport.OfficialForgeChangeRequestParams, method, headSHA string) error {
	if method != "merge" && method != "rebase" {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: "update method 必须是 merge/rebase"}
	}
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	pr, err := p.fetchPR(ctx, ref)
	if err != nil {
		return err
	}
	if headSHA != "" && !strings.EqualFold(headSHA, pr.Head.SHA) {
		return staleHead(headSHA, pr.Head.SHA)
	}
	_, err = p.put(ctx, ref, fmt.Sprintf("/repos/%s/%s/pulls/%d/update-branch", ref.Owner, ref.Repo, ref.Number),
		map[string]any{"update_method": method})
	if err != nil {
		var httpErr *ForgeHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusConflict {
			return &ForgeHTTPError{Status: httpErr.Status, Code: "invalid_spec", Message: httpErr.Message}
		}
		return err
	}
	return nil
}

// ---- GraphQL-only 能力：结构化 unsupported ----

// ChangeRequestViewed 对应官方 viewedFiles：viewed 标记存在 GraphQL 的
// PullRequestFile.viewerViewedState 上，REST 没有读取入口 → 501 unsupported。
// 不返回空 Files 冒充"没有已读文件"。
func (p *ChangeRequestProvider) ChangeRequestViewed(ctx context.Context, params transport.OfficialForgeChangeRequestParams, headSHA string) (ViewedFiles, error) {
	return ViewedFiles{}, Unsupported("viewedFiles")
}

// ChangeRequestSetFileViewed 对应官方 setViewed：GraphQL 的
// markFileAsViewed / unmarkFileAsViewed，REST 无入口 → 501 unsupported。
func (p *ChangeRequestProvider) ChangeRequestSetFileViewed(ctx context.Context, params transport.OfficialForgeChangeRequestParams, path string, viewed bool, headSHA string) error {
	return Unsupported("setViewed")
}

// ChangeRequestReview 对应官方 readReview：review threads 的解析态只能从
// GraphQL 的 reviewThreads 读，REST 只能拿到 review 列表/评论 → 501
// unsupported（返回 nil, err，不返回空 review 冒充"没有 thread"）。
func (p *ChangeRequestProvider) ChangeRequestReview(ctx context.Context, params transport.OfficialForgeChangeRequestParams, headSHA string, fresh bool) (map[string]any, error) {
	return nil, Unsupported("readReview")
}

// ChangeRequestReviewWrite 对应官方 writeReview：写 review threads / 待发布
// review 只能走 GraphQL mutation → 官方 union 的失败分支
// {ok:false,code:"unsupported",message:...}，**不是** Go error、也不是空成功。
func (p *ChangeRequestProvider) ChangeRequestReviewWrite(ctx context.Context, params transport.OfficialForgeChangeRequestParams, write json.RawMessage) (ReviewWriteResult, error) {
	return ReviewWriteResult{OK: false, Code: FailureUnsupported, Message: unsupportedMessage("writeReview")}, nil
}

// ---- compare 读取（rc.2 diff 复用）----

// compareRef 把 base/head 引用拼进 compare 路径：逐段转义但保留 "/"（分支名
// 可以带斜杠，GitHub 的 compare 端点按路径解析它）。
func compareRef(value string) string {
	escaped := url.PathEscape(strings.TrimSpace(value))
	return strings.ReplaceAll(escaped, "%2F", "/")
}
