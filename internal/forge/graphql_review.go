// Package forge 的 review threads 读/写（官方 `readReview` / `writeReview`）。
//
// # 官方依据（rc.2 解包树，逐段对照）
//
//	dsh-experimental-forge-github/src/threads.ts:47-65（REVIEW_QUERY /
//	  REVIEW_THREADS_QUERY / THREAD_COMMENTS_QUERY 三份文档）
//	dsh-experimental-forge-github/src/threads.ts:145-200（首页 → thread 后续页 →
//	  comment 后续页；每页都要再报 head；页数/字节上限；truncated 语义）
//	dsh-experimental-forge-github/src/threads.ts:204-262（toThread / anchor /
//	  toComment：current/original 位置、side、subject、pending 判定）
//	dsh-experimental-forge-github/src/review-writes.ts:20-118（写前校验 +
//	  7 个 kind 的 mutation 与 pending review 规则）
//	dsh-experimental-forge-github/src/review-writes.ts:120-192（targetOf /
//	  draftThread / missingReview）
//	dsh-experimental-forge-github/src/pull-request-node.ts:8-52（写前 node id + head）
//
// 本文件只做"官方 GraphQL 文档 → 本仓 provider-neutral 结果"的映射，请求本身
// 由 graphql.go 的客户端发（自己 POST，不依赖 `gh`）。
package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"dshgo/internal/transport"
)

// ---- 官方 GraphQL 文档（逐字照抄 threads.ts / review-writes.ts）----

// gqlPageSizeText 是官方 `${PAGE_SIZE}` 在文档里的字面值。
const gqlPageSizeText = "100"

// graphqlReviewCommentFields 是官方 COMMENT_FIELDS。
const graphqlReviewCommentFields = "id state createdAt url body viewerCanUpdate viewerCanDelete " +
	"author { __typename login avatarUrl(size: 48) }"

// graphqlReviewCommentPage 是官方 COMMENT_PAGE（带 cursor 的后续页）。
const graphqlReviewCommentPage = "comments(first: " + gqlPageSizeText + ", after: $cursor) " +
	"{ pageInfo { hasNextPage endCursor } nodes { " + graphqlReviewCommentFields + " } }"

// graphqlReviewThreadFields 是官方 THREAD_FIELDS：root 只取第一条评论的 hunk
// 与 commit（回复会重复这些字段），评论首页不带 cursor。
const graphqlReviewThreadFields = "id isOutdated isResolved path diffSide startDiffSide line startLine " +
	"originalLine originalStartLine subjectType viewerCanReply viewerCanResolve viewerCanUnresolve " +
	"root: comments(first: 1) { nodes { diffHunk commit { oid } originalCommit { oid } } } " +
	"comments(first: " + gqlPageSizeText + ") { pageInfo { hasNextPage endCursor } nodes { " + graphqlReviewCommentFields + " } }"

// graphqlReviewThreadPage 是官方 THREAD_PAGE。
const graphqlReviewThreadPage = "pageInfo { hasNextPage endCursor } nodes { " + graphqlReviewThreadFields + " }"

// graphqlReviewQuery 是官方 REVIEW_QUERY（threads.ts:56-64）。
const graphqlReviewQuery = `query($owner: String!, $name: String!, $number: Int!, $first: Int!) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    isArchived viewerPermission
    pullRequest(number: $number) {
      id headRefOid locked
      reviews(states: PENDING, first: 100) { nodes { id viewerDidAuthor comments { totalCount } } }
      reviewThreads(first: $first) { ` + graphqlReviewThreadPage + ` }
    }
  }
}`

// graphqlReviewThreadsQuery 是官方 REVIEW_THREADS_QUERY（后续页再报 head）。
const graphqlReviewThreadsQuery = `query($id: ID!, $cursor: String!, $first: Int!) {
  node(id: $id) { ... on PullRequest { headRefOid reviewThreads(first: $first, after: $cursor) { ` + graphqlReviewThreadPage + ` } } }
}`

// graphqlReviewCommentsQuery 是官方 THREAD_COMMENTS_QUERY。
const graphqlReviewCommentsQuery = `query($id: ID!, $cursor: String!) {
  node(id: $id) { ... on PullRequestReviewThread { ` + graphqlReviewCommentPage + ` } }
}`

// graphqlReviewWriters 是官方 threads.ts:52 的 WRITERS：能评论锁定会话的仓库权限。
var graphqlReviewWriters = map[string]bool{"ADMIN": true, "MAINTAIN": true, "WRITE": true}

// ---- 读：review threads ----

// gqlPageInfo 是 connection 的 pageInfo。
type gqlPageInfo struct {
	HasNextPage bool    `json:"hasNextPage"`
	EndCursor   *string `json:"endCursor"`
}

// gqlReviewCommit 是 root 里可能为 null 的 commit 引用。
type gqlReviewCommit struct {
	OID string `json:"oid"`
}

// gqlReviewRootComment 是 root: comments(first: 1) 的第一个节点。
type gqlReviewRootComment struct {
	DiffHunk       string           `json:"diffHunk"`
	Commit         *gqlReviewCommit `json:"commit"`
	OriginalCommit *gqlReviewCommit `json:"originalCommit"`
}

// gqlReviewCommentAuthor 是评论作者。
type gqlReviewCommentAuthor struct {
	Typename  string  `json:"__typename"`
	Login     string  `json:"login"`
	AvatarURL *string `json:"avatarUrl"`
}

// gqlReviewComment 是一条 review 评论。
type gqlReviewComment struct {
	ID              string                  `json:"id"`
	State           string                  `json:"state"`
	CreatedAt       string                  `json:"createdAt"`
	URL             *string                 `json:"url"`
	Body            string                  `json:"body"`
	ViewerCanUpdate bool                    `json:"viewerCanUpdate"`
	ViewerCanDelete bool                    `json:"viewerCanDelete"`
	Author          *gqlReviewCommentAuthor `json:"author"`
}

// gqlCommentPage 是评论分页。
type gqlCommentPage struct {
	PageInfo gqlPageInfo        `json:"pageInfo"`
	Nodes    []gqlReviewComment `json:"nodes"`
}

// gqlReviewThread 是一条 review thread 节点（字段名与官方文档逐字一致）。
type gqlReviewThread struct {
	ID                 string  `json:"id"`
	IsOutdated         bool    `json:"isOutdated"`
	IsResolved         bool    `json:"isResolved"`
	Path               string  `json:"path"`
	DiffSide           string  `json:"diffSide"`
	StartDiffSide      *string `json:"startDiffSide"`
	Line               *int    `json:"line"`
	StartLine          *int    `json:"startLine"`
	OriginalLine       *int    `json:"originalLine"`
	OriginalStartLine  *int    `json:"originalStartLine"`
	SubjectType        string  `json:"subjectType"`
	ViewerCanReply     bool    `json:"viewerCanReply"`
	ViewerCanResolve   bool    `json:"viewerCanResolve"`
	ViewerCanUnresolve bool    `json:"viewerCanUnresolve"`
	Root               struct {
		Nodes []gqlReviewRootComment `json:"nodes"`
	} `json:"root"`
	Comments gqlCommentPage `json:"comments"`
}

// gqlThreadPage 是 review thread 连接的一页。
type gqlThreadPage struct {
	PageInfo gqlPageInfo       `json:"pageInfo"`
	Nodes    []gqlReviewThread `json:"nodes"`
}

// gqlReviewPullRequest 是 REVIEW_QUERY 里的 pullRequest。
type gqlReviewPullRequest struct {
	ID         string `json:"id"`
	HeadRefOID string `json:"headRefOid"`
	Locked     bool   `json:"locked"`
	Reviews    struct {
		Nodes []struct {
			ID              string `json:"id"`
			ViewerDidAuthor bool   `json:"viewerDidAuthor"`
			Comments        struct {
				TotalCount int `json:"totalCount"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads gqlThreadPage `json:"reviewThreads"`
}

// gqlReviewViewer 是 REVIEW_QUERY 的 viewer。
type gqlReviewViewer struct {
	Login string `json:"login"`
}

// gqlReviewRepository 是 REVIEW_QUERY 的 repository。
type gqlReviewRepository struct {
	IsArchived       bool                  `json:"isArchived"`
	ViewerPermission *string               `json:"viewerPermission"`
	PullRequest      *gqlReviewPullRequest `json:"pullRequest"`
}

// gqlReviewFirstPage 是 REVIEW_QUERY 的完整应答。
type gqlReviewFirstPage struct {
	Viewer     *gqlReviewViewer     `json:"viewer"`
	Repository *gqlReviewRepository `json:"repository"`
}

// gqlReviewThreadNextPage 是 REVIEW_THREADS_QUERY 的 node。
type gqlReviewThreadNextPage struct {
	Node *struct {
		HeadRefOID    string        `json:"headRefOid"`
		ReviewThreads gqlThreadPage `json:"reviewThreads"`
	} `json:"node"`
}

// gqlReviewCommentNextPage 是 THREAD_COMMENTS_QUERY 的 node。
type gqlReviewCommentNextPage struct {
	Node *struct {
		Comments gqlCommentPage `json:"comments"`
	} `json:"node"`
}

// graphqlReviewLimits 是这次读的两个上限（官方 ReviewLimits）。
type graphqlReviewLimits struct {
	threads int
	bytes   int
}

// gqlReviewThreadRead 是一条读完的 review thread：节点 + 它的全部评论。
type gqlReviewThreadRead struct {
	Node     gqlReviewThread
	Comments []gqlReviewComment
}

// gqlReviewThreadsRead 是一次 review thread 读的完整结果。
//
// **ChangeRequestReview 与 ChangeRequestChecks 共用它**（G2 要求）：review thread
// 的文档、分页、每页再报 head 的校验、字节预算与截断口径只有这一份实现，不存在
// 第二套查询。
type gqlReviewThreadsRead struct {
	// Threads 是读到的 thread（每条的评论都已读全；官方只收有评论的 thread）。
	Threads []gqlReviewThreadRead
	// Truncated 是官方 `truncated`：达到 maxReviewThreads 或字节预算而还有
	// thread 没读（为真时 Threads 里数出的未解决数只是下界）。
	Truncated bool
	// HeadRefOID 是这次读看到的 head（首页与每个 thread 后续页都报）。
	HeadRefOID string
	// Viewer 是当前登录名（没有时 nil）。
	Viewer any
	// PendingReview 是当前用户的 pending review（没有时 nil）。
	PendingReview any
	// ViewerCanReview 是"能不能评审"（归档 / 锁定的仓库权限规则）。
	ViewerCanReview bool
}

// Unresolved 是官方 unresolvedThreads 的口径（graphql.ts:554）：读到的 thread 里
// 未 resolve 的条数。Truncated 为真时它是下界——官方 types.ts:169-171 就是拿它
// 定义 threadsTruncated 的。
func (r gqlReviewThreadsRead) Unresolved() int {
	count := 0
	for _, thread := range r.Threads {
		if !thread.Node.IsResolved {
			count++
		}
	}
	return count
}

// graphqlReadReviewThreads 是 review thread 读的**唯一实现**。
//
// 与官方 threads.ts:145-200 同构：首页确认 head；每条 thread 的评论读全；
// thread 分页每页再确认 head；达到 maxReviewThreads 或没有下一页就收尾，
// truncated 取"任务还没读完"的那个事实（达到页数上限时看 pageInfo.hasNextPage，
// 字节预算用完时恒 true）。字节预算按每次应答的原始 JSON 计。
func (p *ChangeRequestProvider) graphqlReadReviewThreads(ctx context.Context, ref changeRequestRef, headSHA string) (gqlReviewThreadsRead, error) {
	limits := graphqlReviewLimits{threads: p.graphqlMaxThreads(), bytes: p.graphqlMaxBytes()}
	raw, used, err := p.graphqlReadSized(ctx, ref, graphqlReviewQuery, map[string]any{
		"owner": ref.Owner, "name": ref.Repo, "number": ref.Number,
		"first": min(graphqlPageSize, limits.threads),
	})
	if err != nil {
		return gqlReviewThreadsRead{}, err
	}
	var page gqlReviewFirstPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return gqlReviewThreadsRead{}, graphqlUnexpected("review thread read")
	}
	if page.Repository == nil || page.Repository.PullRequest == nil {
		return gqlReviewThreadsRead{}, graphqlNotFound(ref.Number)
	}
	pull := page.Repository.PullRequest
	if moved := graphqlStale(headSHA, pull.HeadRefOID); moved != nil {
		return gqlReviewThreadsRead{}, moved
	}
	var viewer any
	if page.Viewer != nil {
		viewer = page.Viewer.Login
	}
	var pendingReview any
	for _, review := range pull.Reviews.Nodes {
		if review.ViewerDidAuthor {
			pendingReview = map[string]any{"id": review.ID, "comments": review.Comments.TotalCount}
			break
		}
	}
	permission := ""
	if page.Repository.ViewerPermission != nil {
		permission = *page.Repository.ViewerPermission
	}
	read := gqlReviewThreadsRead{
		Threads:    []gqlReviewThreadRead{},
		HeadRefOID: pull.HeadRefOID,
		Viewer:     viewer, PendingReview: pendingReview,
		ViewerCanReview: !page.Repository.IsArchived && (!pull.Locked || graphqlReviewWriters[permission]),
	}
	// 首页本身就超预算：官方按 too-large 失败（不能只给半页冒充读到了）。
	if used > limits.bytes {
		return gqlReviewThreadsRead{}, graphqlReviewTooLarge(limits.bytes)
	}
	current := pull.ReviewThreads
	cursors := map[string]bool{}
	for {
		for index := range current.Nodes {
			if len(read.Threads) >= limits.threads {
				break
			}
			node := current.Nodes[index]
			comments := make([]gqlReviewComment, 0, len(node.Comments.Nodes))
			comments = append(comments, node.Comments.Nodes...)
			more := node.Comments.PageInfo
			commentCursors := map[string]bool{}
			for more.HasNextPage {
				cursor := more.EndCursor
				if cursor == nil || commentCursors[*cursor] {
					return gqlReviewThreadsRead{}, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
						Message: "GitHub review comment pagination did not advance"}
				}
				commentCursors[*cursor] = true
				next, size, err := p.graphqlReadSized(ctx, ref, graphqlReviewCommentsQuery, map[string]any{
					"id": node.ID, "cursor": *cursor,
				})
				if err != nil {
					return gqlReviewThreadsRead{}, err
				}
				var commentPage gqlReviewCommentNextPage
				if err := json.Unmarshal(next, &commentPage); err != nil || commentPage.Node == nil {
					return gqlReviewThreadsRead{}, graphqlUnexpected("review comment read")
				}
				used += size
				if used > limits.bytes {
					read.Truncated = true
					return read, nil
				}
				comments = append(comments, commentPage.Node.Comments.Nodes...)
				more = commentPage.Node.Comments.PageInfo
			}
			if len(comments) > 0 {
				read.Threads = append(read.Threads, gqlReviewThreadRead{Node: node, Comments: comments})
			}
		}
		if len(read.Threads) >= limits.threads || !current.PageInfo.HasNextPage {
			read.Truncated = current.PageInfo.HasNextPage
			return read, nil
		}
		cursor := current.PageInfo.EndCursor
		if cursor == nil || cursors[*cursor] {
			return gqlReviewThreadsRead{}, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
				Message: "GitHub review thread pagination did not advance"}
		}
		cursors[*cursor] = true
		next, size, err := p.graphqlReadSized(ctx, ref, graphqlReviewThreadsQuery, map[string]any{
			"id": pull.ID, "cursor": *cursor,
			"first": min(graphqlPageSize, limits.threads-len(read.Threads)),
		})
		if err != nil {
			return gqlReviewThreadsRead{}, err
		}
		var following gqlReviewThreadNextPage
		if err := json.Unmarshal(next, &following); err != nil || following.Node == nil {
			return gqlReviewThreadsRead{}, graphqlUnexpected("review thread page read")
		}
		// 后续页必须再报 head：读的过程中 head 动了就是 stale（不许混两个 head）。
		if moved := graphqlStale(headSHA, following.Node.HeadRefOID); moved != nil {
			return gqlReviewThreadsRead{}, moved
		}
		used += size
		if used > limits.bytes {
			read.Truncated = true
			return read, nil
		}
		current = following.Node.ReviewThreads
	}
}

// graphqlChangeRequestReview 是官方 readReview 的真实实现：校验入参后走共用的
// thread 读，再映射成官方 ForgeReviewData 的 JSON 形状。
func (p *ChangeRequestProvider) graphqlChangeRequestReview(ctx context.Context, params transport.OfficialForgeChangeRequestParams, headSHA string) (map[string]any, error) {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return nil, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	// 没有 GraphQL 凭据时先报能力缺失（上层折成官方 review-unsupported）。
	if err := p.graphqlRequireCredential(); err != nil {
		return nil, err
	}
	switch {
	case strings.TrimSpace(headSHA) == "":
		return nil, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec",
			Message: "readReview 需要 headSha（官方 readReview(ref, headSha) 不允许省略）"}
	case !validHeadSha(headSHA):
		return nil, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec",
			Message: "headSha 不是完整 commit object name: " + headSHA}
	}
	read, err := p.graphqlReadReviewThreads(ctx, ref, headSHA)
	if err != nil {
		return nil, err
	}
	threads := make([]any, 0, len(read.Threads))
	for _, thread := range read.Threads {
		threads = append(threads, gqlReviewThreadValue(thread.Node, thread.Comments, headSHA))
	}
	return graphqlReviewValue(headSHA, read.Viewer, threads, read.Truncated, read.PendingReview, read.ViewerCanReview), nil
}

// graphqlReviewValue 收敛官方 ForgeReviewData 的 JSON 形状（camelCase）。
func graphqlReviewValue(headSHA string, viewer any, threads []any, truncated bool, pendingReview any, viewerCanReview bool) map[string]any {
	return map[string]any{
		"headSha":         headSHA,
		"viewer":          viewer,
		"threads":         threads,
		"truncated":       truncated,
		"pendingReview":   pendingReview,
		"viewerCanReview": viewerCanReview,
	}
}

// graphqlReviewTooLarge 是首页就超过字节预算（官方 too-large）。
func graphqlReviewTooLarge(limit int) error {
	return &ForgeHTTPError{Status: http.StatusRequestEntityTooLarge, Code: "too_large",
		Message: fmt.Sprintf("GitHub review 读取超过 %d 字节上限", limit)}
}

// gqlReviewThreadValue 映射一条 thread（官方 threads.ts:204-236 的 toThread）。
//
// 规则（逐条对照官方）：
//   - side：diffSide LEFT → "L"，其余 "R"；
//   - subject：subjectType == FILE → "file"，否则 "line"；
//   - current：只有"行线程 + 未过期 + 有 line + 首条评论的 commit 就是本次 head"
//     才给位置（否则 null：这条 thread 在 head 上没有位置）；
//   - original：行线程且有 originalLine/originalCommit 时给；
//   - viewerCanResolve：已解决时用 viewerCanUnresolve（官方把"取消解决"也归到这位）。
func gqlReviewThreadValue(node gqlReviewThread, comments []gqlReviewComment, head string) map[string]any {
	var root *gqlReviewRootComment
	if len(node.Root.Nodes) > 0 {
		root = &node.Root.Nodes[0]
	}
	isLine := node.SubjectType != "FILE"
	sameSide := node.StartDiffSide == nil || *node.StartDiffSide == node.DiffSide
	placed := isLine && !node.IsOutdated && node.Line != nil && root != nil && root.Commit != nil &&
		strings.EqualFold(root.Commit.OID, head)
	var current any
	if placed && node.Line != nil {
		start := 0
		if sameSide && node.StartLine != nil {
			start = *node.StartLine
		}
		current = gqlReviewAnchorValue(head, start, *node.Line)
	}
	var original any
	if isLine && node.OriginalLine != nil && root != nil && root.OriginalCommit != nil {
		start := 0
		if sameSide && node.OriginalStartLine != nil {
			start = *node.OriginalStartLine
		}
		original = gqlReviewAnchorValue(root.OriginalCommit.OID, start, *node.OriginalLine)
	}
	var diffHunk any
	if root != nil && root.DiffHunk != "" {
		diffHunk = root.DiffHunk
	}
	side := "R"
	if node.DiffSide == "LEFT" {
		side = "L"
	}
	subject := "line"
	if !isLine {
		subject = "file"
	}
	viewerCanResolve := node.ViewerCanResolve
	if node.IsResolved {
		viewerCanResolve = node.ViewerCanUnresolve
	}
	items := make([]any, 0, len(comments))
	for _, comment := range comments {
		items = append(items, gqlReviewCommentValue(comment))
	}
	return map[string]any{
		"id":               node.ID,
		"path":             node.Path,
		"side":             side,
		"subject":          subject,
		"current":          current,
		"original":         original,
		"diffHunk":         diffHunk,
		"resolved":         node.IsResolved,
		"viewerCanReply":   node.ViewerCanReply,
		"viewerCanResolve": viewerCanResolve,
		"comments":         items,
	}
}

// gqlReviewAnchorValue 是官方 anchor()：startLine 非法/缺失时退化成 endLine。
func gqlReviewAnchorValue(commit string, start, end int) map[string]any {
	if start < 1 || start > end {
		start = end
	}
	return map[string]any{"commit": commit, "startLine": start, "endLine": end}
}

// gqlReviewCommentValue 映射一条评论（官方 toComment）。
func gqlReviewCommentValue(comment gqlReviewComment) map[string]any {
	var author any
	if comment.Author != nil {
		var avatar any
		if comment.Author.AvatarURL != nil {
			avatar = *comment.Author.AvatarURL
		}
		author = map[string]any{
			"login":     comment.Author.Login,
			"avatarUrl": avatar,
			"bot":       comment.Author.Typename == "Bot",
		}
	}
	var url any
	if comment.URL != nil {
		url = *comment.URL
	}
	return map[string]any{
		"id":              comment.ID,
		"author":          author,
		"body":            comment.Body,
		"createdAt":       comment.CreatedAt,
		"pending":         comment.State == "PENDING",
		"url":             url,
		"viewerCanEdit":   comment.ViewerCanUpdate,
		"viewerCanDelete": comment.ViewerCanDelete,
	}
}

// ---- 写：review threads / pending review ----

// graphqlWriteCheckedPull 是官方 CHECKED_PULL（写前校验的字段）。
const graphqlWriteCheckedPull = "id headRefOid reviews(states: PENDING, first: 100) { nodes { id viewerDidAuthor } }"

// graphqlWriteCheckQuery 是官方 WRITE_CHECK_QUERY。
const graphqlWriteCheckQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) { ` + graphqlWriteCheckedPull + ` } }
}`

// graphqlWriteTargetCheckQuery 是官方 WRITE_TARGET_CHECK_QUERY（thread/comment 归属校验）。
const graphqlWriteTargetCheckQuery = `query($owner: String!, $name: String!, $number: Int!, $target: ID!) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) { ` + graphqlWriteCheckedPull + ` } }
  target: node(id: $target) { ... on PullRequestReviewThread { pullRequest { id } } ... on PullRequestReviewComment { pullRequest { id } } }
}`

// graphqlAddReviewMutation 是官方 ADD_REVIEW_MUTATION：commitOID + 可选 threads
// （开一个空 pending review 时 threads 为 null）。
const graphqlAddReviewMutation = `mutation($pull: ID!, $commit: GitObjectID!, $threads: [DraftPullRequestReviewThread]) {
  addPullRequestReview(input: { pullRequestId: $pull, commitOID: $commit, threads: $threads }) { pullRequestReview { id } }
}`

// graphqlAddThreadMutation 是官方 ADD_THREAD_MUTATION：给已有 pending review 加行评论。
const graphqlAddThreadMutation = `mutation($review: ID!, $path: String!, $body: String!, $line: Int!, $side: DiffSide!, $startLine: Int, $startSide: DiffSide) {
  addPullRequestReviewThread(input: {
    pullRequestReviewId: $review, path: $path, body: $body, line: $line, side: $side, startLine: $startLine, startSide: $startSide
  }) { thread { id } }
}`

// graphqlReplyMutation 是官方 REPLY_MUTATION：回复 thread（pending 时并入 pending review）。
const graphqlReplyMutation = `mutation($thread: ID!, $body: String!, $review: ID) {
  addPullRequestReviewThreadReply(input: { pullRequestReviewThreadId: $thread, body: $body, pullRequestReviewId: $review }) { comment { id } }
}`

// 官方 RESOLVE_MUTATION / UNRESOLVE_MUTATION。
const graphqlResolveMutation = "mutation($thread: ID!) { resolveReviewThread(input: { threadId: $thread }) { thread { id } } }"
const graphqlUnresolveMutation = "mutation($thread: ID!) { unresolveReviewThread(input: { threadId: $thread }) { thread { id } } }"

// 官方 EDIT_COMMENT_MUTATION / DELETE_COMMENT_MUTATION。
const graphqlEditCommentMutation = `mutation($comment: ID!, $body: String!) {
  updatePullRequestReviewComment(input: { pullRequestReviewCommentId: $comment, body: $body }) { pullRequestReviewComment { id } }
}`
const graphqlDeleteCommentMutation = "mutation($comment: ID!) { deletePullRequestReviewComment(input: { id: $comment }) { clientMutationId } }"

// 官方 SUBMIT_REVIEW_MUTATION / DELETE_REVIEW_MUTATION。
const graphqlSubmitReviewMutation = `mutation($review: ID!, $event: PullRequestReviewEvent!, $body: String) {
  submitPullRequestReview(input: { pullRequestReviewId: $review, event: $event, body: $body }) { pullRequestReview { id } }
}`
const graphqlDeleteReviewMutation = "mutation($review: ID!) { deletePullRequestReview(input: { pullRequestReviewId: $review }) { pullRequestReview { id } } }"

// graphqlReviewEvents 是官方 EVENTS（write.event 名 → GraphQL 枚举）。
var graphqlReviewEvents = map[string]string{
	"comment":         "COMMENT",
	"approve":         "APPROVE",
	"request-changes": "REQUEST_CHANGES",
}

// gqlWriteCheck 是写前校验应答。
type gqlWriteCheck struct {
	Repository *struct {
		PullRequest *gqlWriteCheckedPull `json:"pullRequest"`
	} `json:"repository"`
	Target *struct {
		PullRequest *struct {
			ID string `json:"id"`
		} `json:"pullRequest"`
	} `json:"target"`
}

// gqlWriteCheckedPull 是写前校验读到的 pull request。
type gqlWriteCheckedPull struct {
	ID         string `json:"id"`
	HeadRefOID string `json:"headRefOid"`
	Reviews    struct {
		Nodes []struct {
			ID              string `json:"id"`
			ViewerDidAuthor bool   `json:"viewerDidAuthor"`
		} `json:"nodes"`
	} `json:"reviews"`
}

// gqlLineComment 是官方 ForgeLineComment（add-pending 的 comment）。
type gqlLineComment struct {
	Path      string `json:"path"`
	Side      string `json:"side"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Body      string `json:"body"`
}

// gqlReviewWrite 是官方 ForgeReviewWrite 的解码形状（按 kind 取各自字段）。
type gqlReviewWrite struct {
	HeadSHA  string          `json:"headSha"`
	Kind     string          `json:"kind"`
	Thread   string          `json:"thread"`
	Body     string          `json:"body"`
	Pending  bool            `json:"pending"`
	Resolved bool            `json:"resolved"`
	Review   string          `json:"review"`
	Event    string          `json:"event"`
	Comment  json.RawMessage `json:"comment"`
}

// graphqlChangeRequestWrite 是官方 writeReview 的真实实现。
//
// 顺序与官方 review-writes.ts:60-118 一致：先一次读校验 head、找当前用户的
// pending review、校验 thread/comment 属于这个 PR；再按 kind 发**一次** mutation。
// 任何一步不成立都回官方 union（{ok:false,code}），**不抛 error**——写的结果
// 是否生效只能由 union 说清。
func (p *ChangeRequestProvider) graphqlChangeRequestWrite(ctx context.Context, params transport.OfficialForgeChangeRequestParams, raw json.RawMessage) (ReviewWriteResult, error) {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return ReviewWriteResult{Code: ActionFailed, Message: err.Error()}, nil
	}
	var write gqlReviewWrite
	if err := json.Unmarshal(raw, &write); err != nil || strings.TrimSpace(write.Kind) == "" {
		return ReviewWriteResult{Code: ActionFailed, Message: "write 不是可解码的 ForgeReviewWrite"}, nil
	}
	// 没有 GraphQL 凭据时先报能力缺失（官方 union 的 unsupported 分支）。
	if err := p.graphqlRequireCredential(); err != nil {
		return ReviewWriteResult{Code: FailureUnsupported, Message: err.Error()}, nil
	}
	if !validHeadSha(write.HeadSHA) {
		return ReviewWriteResult{Code: ActionFailed, Message: "write.headSha 不是完整 commit object name"}, nil
	}
	target, targetErr := gqlWriteTarget(&write)
	if targetErr != nil {
		return *targetErr, nil
	}
	// 1) 写前校验：head + pending review + target 归属。
	query := graphqlWriteCheckQuery
	variables := map[string]any{"owner": ref.Owner, "name": ref.Repo, "number": ref.Number}
	if target != "" {
		query = graphqlWriteTargetCheckQuery
		variables["target"] = target
	}
	data, err := p.graphqlRead(ctx, ref, query, variables)
	if err != nil {
		// 官方把校验失败一律折成 failed（unauthenticated 也在内）；唯一例外是
		// "没有 GraphQL 凭据"——那是本宿主的能力缺失（unsupported）。
		if IsForgeCode(err, FailureUnsupported) {
			return ReviewWriteResult{Code: FailureUnsupported, Message: err.Error()}, nil
		}
		return ReviewWriteResult{Code: ActionFailed, Message: err.Error()}, nil
	}
	var checked gqlWriteCheck
	if err := json.Unmarshal(data, &checked); err != nil || checked.Repository == nil || checked.Repository.PullRequest == nil {
		return ReviewWriteResult{Code: ActionFailed, Message: "unexpected GitHub response to the pull request check"}, nil
	}
	pull := checked.Repository.PullRequest
	if moved := graphqlStale(write.HeadSHA, pull.HeadRefOID); moved != nil {
		return ReviewWriteResult{Code: ActionStale, Message: moved.Message}, nil
	}
	if target != "" {
		if checked.Target == nil || checked.Target.PullRequest == nil || checked.Target.PullRequest.ID != pull.ID {
			return ReviewWriteResult{Code: ActionFailed,
				Message: fmt.Sprintf("%s is not a review thread or comment of pull request %d", target, ref.Number)}, nil
		}
	}
	pending := ""
	for _, review := range pull.Reviews.Nodes {
		if review.ViewerDidAuthor {
			pending = review.ID
			break
		}
	}
	if ctx.Err() != nil {
		return ReviewWriteResult{Code: ActionUnknown, Message: "请求在写入前被取消：" + ctx.Err().Error()}, nil
	}
	send := func(mutation graphqlMutation) ReviewWriteResult {
		if _, failure := p.graphqlSendMutation(ctx, ref, mutation); failure != nil {
			return ReviewWriteResult{Code: failure.Code, Message: failure.Message}
		}
		return ReviewWriteResult{OK: true}
	}
	switch write.Kind {
	case "reply":
		// GitHub 会把已有 pending review 的用户的所有回复塞进那个 review，
		// 即使请求没带 review id；所以非 pending 回复在有 pending review 时直接拒绝。
		if !write.Pending && pending != "" {
			return ReviewWriteResult{Code: ActionFailed,
				Message: "GitHub adds a reply to the pending review while one exists; submit or discard it first"}, nil
		}
		review := any(nil)
		if write.Pending {
			id, failure := p.gqlEnsurePendingReview(ctx, ref, pull.ID, write.HeadSHA, pending)
			if failure != nil {
				return *failure, nil
			}
			review = id
		}
		return send(graphqlMutation{Query: graphqlReplyMutation, Field: "addPullRequestReviewThreadReply",
			Variables: map[string]any{"thread": write.Thread, "body": write.Body, "review": review}}), nil
	case "resolve":
		query, field := graphqlResolveMutation, "resolveReviewThread"
		if !write.Resolved {
			query, field = graphqlUnresolveMutation, "unresolveReviewThread"
		}
		return send(graphqlMutation{Query: query, Field: field,
			Variables: map[string]any{"thread": write.Thread}}), nil
	case "edit-comment":
		comment, err := gqlCommentID(write.Comment)
		if err != nil {
			return ReviewWriteResult{Code: ActionFailed, Message: err.Error()}, nil
		}
		return send(graphqlMutation{Query: graphqlEditCommentMutation, Field: "updatePullRequestReviewComment",
			Variables: map[string]any{"comment": comment, "body": write.Body}}), nil
	case "delete-comment":
		comment, err := gqlCommentID(write.Comment)
		if err != nil {
			return ReviewWriteResult{Code: ActionFailed, Message: err.Error()}, nil
		}
		return send(graphqlMutation{Query: graphqlDeleteCommentMutation, Field: "deletePullRequestReviewComment",
			Variables: map[string]any{"comment": comment}}), nil
	case "add-pending":
		var comment gqlLineComment
		if err := json.Unmarshal(write.Comment, &comment); err != nil {
			return ReviewWriteResult{Code: ActionFailed, Message: "write.comment 不是 ForgeLineComment"}, nil
		}
		thread := gqlDraftThread(comment)
		if pending == "" {
			return send(graphqlMutation{Query: graphqlAddReviewMutation, Field: "addPullRequestReview",
				Variables: map[string]any{"pull": pull.ID, "commit": write.HeadSHA, "threads": []any{thread}}}), nil
		}
		mutation := map[string]any{"review": pending}
		for key, value := range thread {
			mutation[key] = value
		}
		return send(graphqlMutation{Query: graphqlAddThreadMutation, Field: "addPullRequestReviewThread",
			Variables: mutation}), nil
	case "submit-pending":
		if pending != write.Review {
			return gqlMissingReview(write.Review), nil
		}
		event := graphqlReviewEvents[write.Event]
		if event == "" {
			return ReviewWriteResult{Code: ActionFailed,
				Message: fmt.Sprintf("write.event %q 不是 comment/approve/request-changes", write.Event)}, nil
		}
		return send(graphqlMutation{Query: graphqlSubmitReviewMutation, Field: "submitPullRequestReview",
			Variables: map[string]any{"review": write.Review, "event": event, "body": write.Body}}), nil
	case "discard-pending":
		if pending != write.Review {
			return gqlMissingReview(write.Review), nil
		}
		return send(graphqlMutation{Query: graphqlDeleteReviewMutation, Field: "deletePullRequestReview",
			Variables: map[string]any{"review": write.Review}}), nil
	}
	return ReviewWriteResult{Code: ActionFailed,
		Message: fmt.Sprintf("write.kind %q 不是 reply/resolve/edit-comment/delete-comment/add-pending/submit-pending/discard-pending", write.Kind)}, nil
}

// gqlEnsurePendingReview 找当前用户的 pending review，没有就开一个空 review
// （官方 review-writes.ts:88-96）。payload 形状不认识时按 unknown（写可能已生效）。
func (p *ChangeRequestProvider) gqlEnsurePendingReview(ctx context.Context, ref changeRequestRef, pullID, headSHA, pending string) (string, *ReviewWriteResult) {
	if pending != "" {
		return pending, nil
	}
	payload, failure := p.graphqlSendMutation(ctx, ref, graphqlMutation{
		Query: graphqlAddReviewMutation, Field: "addPullRequestReview",
		Variables: map[string]any{"pull": pullID, "commit": headSHA, "threads": nil},
	})
	if failure != nil {
		result := ReviewWriteResult{Code: failure.Code, Message: failure.Message}
		return "", &result
	}
	var started struct {
		PullRequestReview *struct {
			ID string `json:"id"`
		} `json:"pullRequestReview"`
	}
	if err := json.Unmarshal(payload, &started); err != nil || started.PullRequestReview == nil || started.PullRequestReview.ID == "" {
		result := ReviewWriteResult{Code: ActionUnknown,
			Message: "unexpected GitHub response to starting a pending review"}
		return "", &result
	}
	return started.PullRequestReview.ID, nil
}

// gqlWriteTarget 返回这次写要校验归属的 thread/comment id（写 pending review 时为 ""）。
func gqlWriteTarget(write *gqlReviewWrite) (string, *ReviewWriteResult) {
	switch write.Kind {
	case "reply", "resolve":
		if strings.TrimSpace(write.Thread) == "" {
			result := ReviewWriteResult{Code: ActionFailed, Message: "write.thread 是空串"}
			return "", &result
		}
		return write.Thread, nil
	case "edit-comment", "delete-comment":
		comment, err := gqlCommentID(write.Comment)
		if err != nil {
			result := ReviewWriteResult{Code: ActionFailed, Message: err.Error()}
			return "", &result
		}
		return comment, nil
	}
	return "", nil
}

// gqlCommentID 取 edit-comment / delete-comment 的 comment id（字符串）。
func gqlCommentID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("write.comment 缺失")
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil || strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("write.comment 必须是非空字符串 id")
	}
	return id, nil
}

// gqlDraftThread 把官方 ForgeLineComment 折成 DraftPullRequestReviewThread
// （官方 draftThread：单行时 startLine/startSide 为 null）。
func gqlDraftThread(comment gqlLineComment) map[string]any {
	side := "RIGHT"
	if comment.Side == "L" {
		side = "LEFT"
	}
	thread := map[string]any{
		"path": comment.Path, "body": comment.Body,
		"line": comment.EndLine, "side": side,
		"startLine": nil, "startSide": nil,
	}
	if comment.StartLine != comment.EndLine {
		thread["startLine"] = comment.StartLine
		thread["startSide"] = side
	}
	return thread
}

// gqlMissingReview 是官方 missingReview：给的 review 已不是当前用户的 pending review。
func gqlMissingReview(review string) ReviewWriteResult {
	return ReviewWriteResult{Code: ActionFailed,
		Message: fmt.Sprintf("the pending review %s is no longer the signed-in user's pending review", review)}
}
