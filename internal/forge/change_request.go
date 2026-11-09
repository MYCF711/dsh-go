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
		code := "provider_error"
		switch resp.StatusCode {
		case 401:
			code = "auth_required"
		case 403:
			code = "forbidden"
		case 404:
			code = "not_found"
		case 409:
			code = "stale_head"
		case 422:
			code = "invalid_spec"
		}
		return nil, &ForgeHTTPError{Status: resp.StatusCode, Code: code, Message: fmt.Sprintf("GitHub %s 返回 HTTP %d: %s", path, resp.StatusCode, truncateForge(string(body), 500))}
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
	} `json:"base"`
	RequestedReviewers []any `json:"requested_reviewers"`
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

func (p *ChangeRequestProvider) reviewDecision(ctx context.Context, ref changeRequestRef, pr githubPR) string {
	if len(pr.RequestedReviewers) > 0 {
		return "review-required"
	}
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100", ref.Owner, ref.Repo, ref.Number), "application/vnd.github+json")
	if err != nil {
		return "none"
	}
	var reviews []struct {
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if json.Unmarshal(raw, &reviews) != nil {
		return "none"
	}
	latest := map[string]string{}
	for _, review := range reviews {
		latest[review.User.Login] = strings.ToUpper(review.State)
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

type githubCheckRuns struct {
	TotalCount int `json:"total_count"`
	CheckRuns  []struct {
		Name        string  `json:"name"`
		Status      string  `json:"status"`
		Conclusion  string  `json:"conclusion"`
		DetailsURL  string  `json:"details_url"`
		StartedAt   *string `json:"started_at"`
		CompletedAt *string `json:"completed_at"`
		CheckSuite  struct {
			Name string `json:"name"`
		} `json:"check_suite"`
	} `json:"check_runs"`
}

func (p *ChangeRequestProvider) checks(ctx context.Context, ref changeRequestRef, headSHA string) (OfficialChecks, error) {
	pr, err := p.fetchPR(ctx, ref)
	if err != nil {
		return OfficialChecks{}, err
	}
	if headSHA != "" && !strings.EqualFold(headSHA, pr.Head.SHA) {
		return OfficialChecks{}, &ForgeHTTPError{Status: 409, Code: "stale_head", Message: fmt.Sprintf("head 已移动：显示 %s，当前 %s", headSHA, pr.Head.SHA)}
	}
	var runs githubCheckRuns
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", ref.Owner, ref.Repo, pr.Head.SHA), "application/vnd.github+json")
	if err != nil {
		return OfficialChecks{}, err
	}
	if err := json.Unmarshal(raw, &runs); err != nil {
		return OfficialChecks{}, &ForgeHTTPError{Status: 502, Code: "provider_error", Message: "checks 响应不是 JSON: " + err.Error()}
	}
	checks := make([]transport.OfficialForgeChangeRequestCheck, 0, len(runs.CheckRuns))
	for _, run := range runs.CheckRuns {
		state := "pending"
		switch strings.ToLower(run.Status) {
		case "completed":
			switch strings.ToLower(run.Conclusion) {
			case "success":
				state = "success"
			case "neutral":
				state = "neutral"
			case "skipped":
				state = "skipped"
			case "cancelled":
				state = "cancelled"
			case "failure", "timed_out", "action_required", "startup_failure":
				state = "failure"
			default:
				state = "neutral"
			}
		}
		check := transport.OfficialForgeChangeRequestCheck{Name: run.Name, State: state, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt}
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
	decision := p.reviewDecision(ctx, ref, pr)
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
	res := transport.OfficialForgeChangeRequestChecks{
		Ref: transport.OfficialForgeChangeRequestRef{Forge: "github", Host: ref.Host, Owner: ref.Owner, Name: ref.Repo, Number: pr.Number},
		URL: pr.HTMLURL, Title: pr.Title, State: state, Draft: pr.Draft, Author: author,
		HeadBranch: pr.Head.Ref, HeadSHA: pr.Head.SHA, BaseBranch: pr.Base.Ref,
		Additions: pr.Additions, Deletions: pr.Deletions, Mergeability: mergeability,
		MergeReady:     state == "open" && !pr.Draft && mergeability == "mergeable" && decision != "changes-requested" && rollup != "failure",
		ReviewDecision: decision, Checks: checks, ChecksRollup: rollup,
		ChecksTruncated: runs.TotalCount > len(checks), UnresolvedThreads: 0,
		UpdatedAt: pr.UpdatedAt, ClosedAt: closedAt,
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

func (p *ChangeRequestProvider) ChangeRequestDiff(ctx context.Context, params transport.OfficialForgeChangeRequestParams) (transport.OfficialForgeChangeRequestDiff, error) {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return transport.OfficialForgeChangeRequestDiff{}, &ForgeHTTPError{Status: 400, Code: "bad_request", Message: err.Error()}
	}
	pr, err := p.fetchPR(ctx, ref)
	if err != nil {
		return transport.OfficialForgeChangeRequestDiff{}, err
	}
	raw, err := p.get(ctx, ref, fmt.Sprintf("/repos/%s/%s/pulls/%d", ref.Owner, ref.Repo, ref.Number), "application/vnd.github.v3.diff")
	if err != nil {
		return transport.OfficialForgeChangeRequestDiff{}, err
	}
	files, err := parseUnifiedDiff(string(raw))
	if err != nil {
		return transport.OfficialForgeChangeRequestDiff{}, &ForgeHTTPError{Status: 502, Code: "provider_error", Message: err.Error()}
	}
	urls := map[string]string{}
	for _, file := range files {
		urls[file.Path] = "https://" + ref.Host + "/" + ref.Owner + "/" + ref.Repo + "/blob/" + pr.Head.SHA + "/" + file.Path
	}
	return transport.OfficialForgeChangeRequestDiff{HeadSHA: pr.Head.SHA, Files: files, FileURLs: urls}, nil
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
