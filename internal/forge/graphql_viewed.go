// Package forge 的 viewed 标记读/写（官方 `viewedFiles` / `setViewed`）。
//
// # 官方依据（rc.2 解包树，逐段对照）
//
//	dsh-experimental-forge-github/src/viewed.ts:15-31（VIEWED_QUERY /
//	  MARK_VIEWED_MUTATION / UNMARK_VIEWED_MUTATION）
//	dsh-experimental-forge-github/src/viewed.ts:43-56（VIEWED 状态映射：
//	  VIEWED/UNVIEWED/DISMISSED，GitHub 以后新增的状态按 unviewed 兜底）
//	dsh-experimental-forge-github/src/viewed.ts:74-112（readViewed：每页都报 head、
//	  totalCount > listed 才算 truncated、分页不前进即失败）
//	dsh-experimental-forge-github/src/viewed.ts:114-132（writeViewed：先读
//	  pullRequestNode 校验 head，再发 mark/unmark mutation；mutation 不带 head，
//	  所以读与写之间的 push 只能由 GitHub 自己决定语义）
//	dsh-experimental-forge-github/src/pull-request-node.ts:8-52（读 node id + head）
//
// viewed 标记是**当前登录用户私有**的状态：别人的标记读不到，也不该伪造。
package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"dshgo/internal/transport"
)

// graphqlViewedQuery 是官方 VIEWED_QUERY：一页文件 + 每页的 head。
const graphqlViewedQuery = `query($owner: String!, $name: String!, $number: Int!, $first: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      headRefOid
      files(first: $first, after: $cursor) { totalCount pageInfo { hasNextPage endCursor } nodes { path viewerViewedState } }
    }
  }
}`

// 官方 MARK_VIEWED_MUTATION / UNMARK_VIEWED_MUTATION（都只带 pull request id + path）。
const graphqlMarkViewedMutation = `mutation($id: ID!, $path: String!) {
  markFileAsViewed(input: { pullRequestId: $id, path: $path }) { clientMutationId }
}`
const graphqlUnmarkViewedMutation = `mutation($id: ID!, $path: String!) {
  unmarkFileAsViewed(input: { pullRequestId: $id, path: $path }) { clientMutationId }
}`

// graphqlPullRequestNodeQuery 是官方 PULL_REQUEST_NODE_QUERY。
const graphqlPullRequestNodeQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) { id headRefOid } }
}`

// graphqlViewedStates 是官方 STATES（viewed.ts:43）：GitHub 的枚举 → 官方状态。
var graphqlViewedStates = map[string]string{
	"VIEWED":    "viewed",
	"UNVIEWED":  "unviewed",
	"DISMISSED": "dismissed",
}

// gqlViewedNode 是 files 连接里的一个文件。
type gqlViewedNode struct {
	Path              string `json:"path"`
	ViewerViewedState string `json:"viewerViewedState"`
}

// gqlViewedFiles 是一页 files。
type gqlViewedFiles struct {
	TotalCount int             `json:"totalCount"`
	PageInfo   gqlPageInfo     `json:"pageInfo"`
	Nodes      []gqlViewedNode `json:"nodes"`
}

// gqlViewedPull 是 VIEWED_QUERY 里的 pullRequest。
type gqlViewedPull struct {
	HeadRefOID string          `json:"headRefOid"`
	Files      *gqlViewedFiles `json:"files"`
}

// gqlViewedPage 是 VIEWED_QUERY 的应答。
type gqlViewedPage struct {
	Repository *struct {
		PullRequest *gqlViewedPull `json:"pullRequest"`
	} `json:"repository"`
}

// graphqlPullRequestNode 是 PULL_REQUEST_NODE_QUERY 的 pullRequest。
type graphqlPullRequestNode struct {
	ID         string `json:"id"`
	HeadRefOID string `json:"headRefOid"`
}

// graphqlChangeRequestViewed 是官方 viewedFiles 的真实实现。
//
// 官方语义（viewed.ts:74-112）：
//   - 每一页都必须报同一个 head，否则 stale（绝不把两次 push 的标记混在一起）；
//   - files 为 null（GitHub 列不出这个 PR 的 diff）→ unavailable，不是"没有标记"；
//   - truncated 只在 totalCount > 已列出的数量时为 true；
//   - 分页游标不前进 → unavailable（防死循环）。
func (p *ChangeRequestProvider) graphqlChangeRequestViewed(ctx context.Context, params transport.OfficialForgeChangeRequestParams, headSHA string) (ViewedFiles, error) {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return ViewedFiles{}, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	// 没有 GraphQL 凭据时先报能力缺失（上层折成官方 null / 501）。
	if err := p.graphqlRequireCredential(); err != nil {
		return ViewedFiles{}, err
	}
	if strings.TrimSpace(headSHA) == "" {
		return ViewedFiles{}, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec",
			Message: "viewedFiles 需要 headSha（官方 viewedFiles(ref, headSha) 不允许省略）"}
	}
	if !validHeadSha(headSHA) {
		return ViewedFiles{}, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec",
			Message: "headSha 不是完整 commit object name: " + headSHA}
	}
	limit := p.graphqlMaxViewed()
	files := map[string]string{}
	listed := 0
	cursors := map[string]string{}
	var cursor *string
	for {
		variables := map[string]any{
			"owner": ref.Owner, "name": ref.Repo, "number": ref.Number,
			"first": min(graphqlPageSize, limit-listed),
		}
		if cursor != nil {
			variables["cursor"] = *cursor
		}
		raw, err := p.graphqlRead(ctx, ref, graphqlViewedQuery, variables)
		if err != nil {
			return ViewedFiles{}, err
		}
		var page gqlViewedPage
		if err := json.Unmarshal(raw, &page); err != nil {
			return ViewedFiles{}, graphqlUnexpected("viewed file read")
		}
		if page.Repository == nil || page.Repository.PullRequest == nil {
			return ViewedFiles{}, graphqlNotFound(ref.Number)
		}
		pull := page.Repository.PullRequest
		// 官方每条消息里都写 headRefOid，所以两个名字都要加：see gqlViewedPull。
		if moved := graphqlStale(headSHA, pull.HeadRefOID); moved != nil {
			return ViewedFiles{}, moved
		}
		if pull.Files == nil {
			return ViewedFiles{}, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
				Message: "GitHub did not list the pull request files"}
		}
		for _, node := range pull.Files.Nodes {
			state, ok := graphqlViewedStates[node.ViewerViewedState]
			if !ok {
				// GitHub 以后新增的状态按官方口径读成 unviewed（不猜）。
				state = "unviewed"
			}
			files[node.Path] = state
		}
		listed += len(pull.Files.Nodes)
		if !pull.Files.PageInfo.HasNextPage || listed >= limit {
			return ViewedFiles{
				HeadSHA:   pull.HeadRefOID,
				Files:     files,
				Truncated: pull.Files.TotalCount > listed,
			}, nil
		}
		end := pull.Files.PageInfo.EndCursor
		if end == nil || *end == "" {
			return ViewedFiles{}, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
				Message: "GitHub file pagination did not advance"}
		}
		if _, seen := cursors[*end]; seen {
			return ViewedFiles{}, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
				Message: "GitHub file pagination did not advance"}
		}
		cursors[*end] = *end
		value := *end
		cursor = &value
	}
}

// graphqlChangeRequestSetViewed 是官方 setViewed 的真实实现。
//
// 官方语义（viewed.ts:114-132）：mutation 本身不带 head，所以先读 pull request
// 的 node id 并确认 head 仍是调用方看到的那一个；head 已动就是 stale（什么都不写）。
// mutation 只发一次、不重试；拒绝 → failed，答复不确认 → unknown。
func (p *ChangeRequestProvider) graphqlChangeRequestSetViewed(ctx context.Context, params transport.OfficialForgeChangeRequestParams, path string, viewed bool, headSHA string) error {
	ref, err := parseChangeRequestURL(params.URL)
	if err != nil {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	// 没有 GraphQL 凭据时先报能力缺失（ActionFailureOf 折成官方 failed）。
	if err := p.graphqlRequireCredential(); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec", Message: "request.path 是空串"}
	}
	if !validHeadSha(headSHA) {
		return &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec",
			Message: "request.headSha 不是完整 commit object name"}
	}
	node, err := p.graphqlPullRequestNode(ctx, ref, headSHA)
	if err != nil {
		return err
	}
	query, field := graphqlMarkViewedMutation, "markFileAsViewed"
	if !viewed {
		query, field = graphqlUnmarkViewedMutation, "unmarkFileAsViewed"
	}
	if _, failure := p.graphqlSendMutation(ctx, ref, graphqlMutation{
		Query: query, Field: field,
		Variables: map[string]any{"id": node.ID, "path": path},
	}); failure != nil {
		return actionFailureError(failure)
	}
	return nil
}

// graphqlPullRequestNode 读 pull request 的 node id，并确认 head 仍是 headSHA
// （官方 pullRequestNode：head 已动 → stale，PR 不存在 → failed）。
func (p *ChangeRequestProvider) graphqlPullRequestNode(ctx context.Context, ref changeRequestRef, headSHA string) (graphqlPullRequestNode, error) {
	raw, err := p.graphqlRead(ctx, ref, graphqlPullRequestNodeQuery, map[string]any{
		"owner": ref.Owner, "name": ref.Repo, "number": ref.Number,
	})
	if err != nil {
		return graphqlPullRequestNode{}, err
	}
	var page struct {
		Repository *struct {
			PullRequest *graphqlPullRequestNode `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return graphqlPullRequestNode{}, graphqlUnexpected("pull request read")
	}
	if page.Repository == nil || page.Repository.PullRequest == nil {
		return graphqlPullRequestNode{}, &ForgeHTTPError{Status: http.StatusNotFound, Code: "not_found",
			Message: "pull request was not found"}
	}
	node := *page.Repository.PullRequest
	if moved := graphqlStale(headSHA, node.HeadRefOID); moved != nil {
		return graphqlPullRequestNode{}, moved
	}
	return node, nil
}
