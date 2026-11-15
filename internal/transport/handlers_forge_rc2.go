package transport

// 官方 0.2.2-rc.2 新增的 12 个 `forgePanel` 方法（L1 契约层）。
//
// # 契约来源（只读）
//
//   dsh-experimental-client-ui-forge/lib/typert.host.js（rc.2 解包树）
//     forgePanel/changeRequestCommits      1167
//     forgePanel/changeRequestReview       1288
//     forgePanel/changeRequestReviewWrite  1374
//     forgePanel/changeRequestViewed       （同文件 changeRequestReview 之后）
//     forgePanel/diffBases                 1649
//     forgePanel/localCommits              1788
//     forgePanel/requestReviews            2132
//     forgePanel/rerunChecks               2251
//     forgePanel/reviewerCandidates        （同文件）
//     forgePanel/setFileViewed             （同文件）
//     forgePanel/updateBranch              （同文件）
//     forgePanel/worktreeCleanup           （同文件）
//   dsh-experimental-client-ui-forge/src/index.ts（Remote 的实现与错误码）
//     worktreeCleanup 210 / diffBases 236 / localCommits 323 / changeRequestCommits 384
//     changeRequestViewed 478 / setFileViewed 500 / changeRequestReview 527
//     changeRequestReviewWrite 586 / rerunChecks 648 / requestReviews 666
//     reviewerCandidates 687 / updateBranch 708
//   dsh-experimental-forge/src/types.ts（结果与失败码的权威类型）
//     ChangeRequestSnapshot 109 / ForgeFailure 183 / ForgeActionFailure 414
//     ForgeCommitList 441 / ForgeReviewerCandidate 238 / ForgeViewedFiles 457
//     ForgeReviewData 361 / ForgeReviewWriteResult 404 / WorktreeCleanup（worktree/types.ts）
//   docs/evidence/official-method-contract-delta-0.2.2-rc.2.md §1（逐方法参数/结果 jsonSchema）
//
// # 这一层做什么
//
// 参数按官方**位置参数名**（typert `parameters[].name`，本仓 L1 的键就是它）
// fail-loud 校验；结果按官方 jsonSchema 的形状出；错误用官方 `forgePanel/*`
// 码 + 本仓四字段信封。真实读写由宿主注入的 `OfficialForgeRC2API` 完成：
//
//	transport 不 import internal/forge / internal/worktree（R7）
//	宿主在 cmd/dshd/serve_forge_rc2.go 里把领域能力装成这张函数表
//
// 表里为 nil 的那一条 = 宿主没提供这条能力 → 501 点名缺什么（不许静默成功）。
// 表里的实现在**能力缺失**时会返回 `ForgeRC2Error`（码是官方 `forgePanel/*`，
// 状态 501），于是"provider 不读 review"与"宿主没接线"在错误码上仍然可分辨。
//
// # 三种"没有数据"必须被区分（官方语义）
//
//   - `changeRequestCommits` / `changeRequestViewed` / `worktreeCleanup` 的官方结果
//     **允许 null**，且 null 有确定含义：provider 不读 commits / 不保存已读标记 /
//     没有 worktree 服务。所以这三条在 null 时写 `{"result":null}`——这是官方契约里
//     "这条读不支持"的**成功答案**，不是静默成功（writeJSON 的空值守卫在这里
//     不适用，我们显式走 writeForgeRC2NullResult 并注释理由）。
//   - `changeRequestReviewWrite` 的官方结果是 `{ok:false,code}` union：能力缺失是
//     `code:"unsupported"` 的 **200 结果**，不是 HTTP 错误。
//   - 其余方法的缺失能力是 501 + 官方码（如 `forgePanel/review-unsupported`），
//     并在线上多一个兄弟字段 `capableWhen`（补上什么就能用：当前即 GitHub 凭据）。
//
// # 接线（WIRING，server.go 不动）
//
// 路由：`IsOfficialForgeLiveshareMethod` 已经负责把这 12 个名字认领进
// `s.serveOfficialForgeLiveshareAPI`（server.go:588 的分支），该函数在本包的
// handlers_forge_liveshare_official.go 里，已加入：
//
//	if IsOfficialForgeRC2Method(name) {
//		s.ServeOfficialForgeRC2(w, r, name)
//		return
//	}
//
// 宿主装配（Lead 在 server.go 的 forge 装配段加一行）：
//
//	srv.AttachOfficialForgeRC2API(newForgeRC2API(*home, ...))
//
// 没加这一行时这 12 条仍是可调用的 501（点名"宿主没有 AttachOfficialForgeRC2API"），
// 不会落到 404，也不会假成功。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// 12 个方法
// ---------------------------------------------------------------------------

const (
	MethodForgePanelChangeRequestCommits     Method = "forgePanel.changeRequestCommits"
	MethodForgePanelChangeRequestReview      Method = "forgePanel.changeRequestReview"
	MethodForgePanelChangeRequestReviewWrite Method = "forgePanel.changeRequestReviewWrite"
	MethodForgePanelChangeRequestViewed      Method = "forgePanel.changeRequestViewed"
	MethodForgePanelDiffBases                Method = "forgePanel.diffBases"
	MethodForgePanelLocalCommits             Method = "forgePanel.localCommits"
	MethodForgePanelRequestReviews           Method = "forgePanel.requestReviews"
	MethodForgePanelRerunChecks              Method = "forgePanel.rerunChecks"
	MethodForgePanelReviewerCandidates       Method = "forgePanel.reviewerCandidates"
	MethodForgePanelSetFileViewed            Method = "forgePanel.setFileViewed"
	MethodForgePanelUpdateBranch             Method = "forgePanel.updateBranch"
	MethodForgePanelWorktreeCleanup          Method = "forgePanel.worktreeCleanup"
)

// OfficialForgeRC2Methods 返回 rc.2 新增的 12 个方法（顺序固定，供快照/台账/门禁）。
//
// 它和 `OfficialForgeLiveshareMethods()`（rc.1 的 19+12=31）分开维护：那一组是
// "逐方法未实现表"的驱动集，这一组是"有真实 L1 契约层"的集合。两个集合的交集为空，
// 合起来是 rc.2 的 43 个 forgePanel/liveshareShares 名字。
func OfficialForgeRC2Methods() []Method {
	return []Method{
		MethodForgePanelChangeRequestCommits,
		MethodForgePanelChangeRequestReview,
		MethodForgePanelChangeRequestReviewWrite,
		MethodForgePanelChangeRequestViewed,
		MethodForgePanelDiffBases,
		MethodForgePanelLocalCommits,
		MethodForgePanelRequestReviews,
		MethodForgePanelRerunChecks,
		MethodForgePanelReviewerCandidates,
		MethodForgePanelSetFileViewed,
		MethodForgePanelUpdateBranch,
		MethodForgePanelWorktreeCleanup,
	}
}

// IsOfficialForgeRC2Method 判断一个 /api 路径名是否是这 12 个方法之一。
func IsOfficialForgeRC2Method(name string) bool {
	switch Method(name) {
	case MethodForgePanelChangeRequestCommits,
		MethodForgePanelChangeRequestReview,
		MethodForgePanelChangeRequestReviewWrite,
		MethodForgePanelChangeRequestViewed,
		MethodForgePanelDiffBases,
		MethodForgePanelLocalCommits,
		MethodForgePanelRequestReviews,
		MethodForgePanelRerunChecks,
		MethodForgePanelReviewerCandidates,
		MethodForgePanelSetFileViewed,
		MethodForgePanelUpdateBranch,
		MethodForgePanelWorktreeCleanup:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// 结果形状（照官方 jsonSchema，字段名 camelCase）
// ---------------------------------------------------------------------------

// OfficialForgeCommit 是官方 ForgeCommit（changeRequestCommits / localCommits 的元素）。
type OfficialForgeCommit struct {
	SHA        string  `json:"sha"`
	Parent     *string `json:"parent"`
	Title      string  `json:"title"`
	Author     *string `json:"author"`
	AuthoredAt string  `json:"authoredAt"`
}

// OfficialForgeCommitList 是官方 ForgeCommitList。
//
// forgePanel/localCommits 的结果形状与它逐字段相同（官方 schemas 也相同），
// 所以用同一个类型：两处各写一份只会漂移。
type OfficialForgeCommitList struct {
	HeadSHA   string                `json:"headSha"`
	Commits   []OfficialForgeCommit `json:"commits"`
	Truncated bool                  `json:"truncated"`
}

// OfficialForgeLocalCommits 是 forgePanel/localCommits 的结果（同形状别名）。
type OfficialForgeLocalCommits = OfficialForgeCommitList

// OfficialForgeBaseRef 是 diffBases 的一个分支（full ref + 简称）。
type OfficialForgeBaseRef struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

// OfficialForgeBaseBranch 是 diffBases 的 automatic fork point。
type OfficialForgeBaseBranch struct {
	Name     string `json:"name"`
	Worktree bool   `json:"worktree"`
}

// OfficialForgeBaseBranches 是官方 ForgeBaseBranches。
type OfficialForgeBaseBranches struct {
	Automatic *OfficialForgeBaseBranch `json:"automatic"`
	Remote    []OfficialForgeBaseRef   `json:"remote"`
	Local     []OfficialForgeBaseRef   `json:"local"`
}

// OfficialForgeReviewerCandidate 是官方 ForgeReviewerCandidate。
type OfficialForgeReviewerCandidate struct {
	Login     string  `json:"login"`
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatarUrl"`
	Suggested bool    `json:"suggested"`
}

// OfficialForgeReviewerRef 是 requestReviews 的 `reviewers[]` 元素。
type OfficialForgeReviewerRef struct {
	Kind  string `json:"kind"`
	Login string `json:"login"`
}

// OfficialForgeViewedFiles 是官方 ForgeViewedFiles（`files` 是 path→状态）。
type OfficialForgeViewedFiles struct {
	HeadSHA   string            `json:"headSha"`
	Files     map[string]string `json:"files"`
	Truncated bool              `json:"truncated"`
}

// OfficialForgeViewedRequest 是 setFileViewed 的 `request`。
type OfficialForgeViewedRequest struct {
	Path    string `json:"path"`
	Viewed  bool   `json:"viewed"`
	HeadSHA string `json:"headSha"`
}

// OfficialForgeRerunRequest 是 rerunChecks 的 `request`。
type OfficialForgeRerunRequest struct {
	HeadSHA string `json:"headSha"`
}

// OfficialForgeReviewAnchor 是官方 ForgeAnchor（连续行区间 + 新侧 commit）。
type OfficialForgeReviewAnchor struct {
	Commit    string `json:"commit"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
}

// OfficialForgeReviewAuthor 是官方 ForgeCommentAuthor。
type OfficialForgeReviewAuthor struct {
	Login     string  `json:"login"`
	AvatarURL *string `json:"avatarUrl"`
	Bot       bool    `json:"bot"`
}

// OfficialForgeReviewThreadComment 是官方 ForgeReviewComment。
type OfficialForgeReviewThreadComment struct {
	ID              string                     `json:"id"`
	Author          *OfficialForgeReviewAuthor `json:"author"`
	Body            string                     `json:"body"`
	CreatedAt       string                     `json:"createdAt"`
	Pending         bool                       `json:"pending"`
	URL             *string                    `json:"url"`
	ViewerCanEdit   bool                       `json:"viewerCanEdit"`
	ViewerCanDelete bool                       `json:"viewerCanDelete"`
}

// OfficialForgeReviewThread 是官方 ForgeReviewThread。
type OfficialForgeReviewThread struct {
	ID               string                             `json:"id"`
	Path             string                             `json:"path"`
	Side             string                             `json:"side"`
	Subject          string                             `json:"subject"`
	Current          *OfficialForgeReviewAnchor         `json:"current"`
	Original         *OfficialForgeReviewAnchor         `json:"original"`
	DiffHunk         *string                            `json:"diffHunk"`
	Resolved         bool                               `json:"resolved"`
	ViewerCanReply   bool                               `json:"viewerCanReply"`
	ViewerCanResolve bool                               `json:"viewerCanResolve"`
	Comments         []OfficialForgeReviewThreadComment `json:"comments"`
}

// OfficialForgePendingReview 是官方 ForgePendingReview。
type OfficialForgePendingReview struct {
	ID       string `json:"id"`
	Comments int    `json:"comments"`
}

// OfficialForgeReviewData 是官方 ForgeReviewData。
type OfficialForgeReviewData struct {
	HeadSHA         string                      `json:"headSha"`
	Viewer          *string                     `json:"viewer"`
	Threads         []OfficialForgeReviewThread `json:"threads"`
	Truncated       bool                        `json:"truncated"`
	PendingReview   *OfficialForgePendingReview `json:"pendingReview"`
	ViewerCanReview bool                        `json:"viewerCanReview"`
}

// OfficialForgeReviewWriteResult 是官方 ForgeReviewWriteResult 的判别 union。
type OfficialForgeReviewWriteResult struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// value 把 union 收敛成官方形状：ok 时不带 code/message。
func (r OfficialForgeReviewWriteResult) value() any {
	if r.OK {
		return map[string]any{"ok": true}
	}
	code := r.Code
	if code == "" {
		code = "failed"
	}
	return map[string]any{"ok": false, "code": code, "message": r.Message}
}

// OfficialForgeWorktreeCleanup 是官方 WorktreeCleanup。
type OfficialForgeWorktreeCleanup struct {
	Revision int64  `json:"revision"`
	Status   string `json:"status"`
}

// ---------------------------------------------------------------------------
// 错误：官方 forgePanel/* 码
// ---------------------------------------------------------------------------

// 官方 Remote 错误码（client-ui-forge/src/index.ts 的 RemoteError 第一个参数）。
const (
	CodeForgeNotBound             = "forgePanel/not-bound"
	CodeForgeUnrecognizedURL      = "forgePanel/unrecognized-url"
	CodeForgeStaleHead            = "forgePanel/stale-head"
	CodeForgeDiffFailed           = "forgePanel/diff-failed"
	CodeForgeDiffTooLarge         = "forgePanel/diff-too-large"
	CodeForgeReviewFailed         = "forgePanel/review-failed"
	CodeForgeReviewUnsupported    = "forgePanel/review-unsupported"
	CodeForgeViewedFailed         = "forgePanel/viewed-failed"
	CodeForgeReviewersFailed      = "forgePanel/reviewers-failed"
	CodeForgeActionFailed         = "forgePanel/action-failed"
	CodeForgeActionUnknown        = "forgePanel/action-unknown"
	CodeForgeNotACheckout         = "forgePanel/not-a-checkout"
	CodeForgeBaseMissing          = "forgePanel/base-missing"
	CodeForgeNoForkPoint          = "forgePanel/no-fork-point"
	CodeForgeWorktreesUnavailable = "forgePanel/worktrees-unavailable"
)

// ForgeRC2Error 是带官方码的结构化失败（宿主实现返回它，本层原样出手）。
type ForgeRC2Error struct {
	Status  int
	Code    string
	Message string
	// CapableWhen 是"缺什么条件才能用"的补救说明，只对 501（能力缺失）有意义。
	// 它在线上是 **error 的兄弟字段** `capableWhen`（与 cua/office 的 501 同形），
	// 不进 error 的四字段本身（四字段契约不变）。
	CapableWhen string
}

func (e *ForgeRC2Error) Error() string { return e.Code + ": " + e.Message }

// ForgeRC2Errorf 造一个官方码错误。status<=0 时按码选 400/409/501 的默认值。
func ForgeRC2Errorf(status int, code, format string, args ...any) *ForgeRC2Error {
	return &ForgeRC2Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// ForgeRC2Unsupported 是"这条能力本宿主没有"的结构化 501（码由调用方点名）。
func ForgeRC2Unsupported(code, format string, args ...any) *ForgeRC2Error {
	return &ForgeRC2Error{Status: http.StatusNotImplemented, Code: code, Message: fmt.Sprintf(format, args...)}
}

// ForgeRC2UnsupportedWhen 与 ForgeRC2Unsupported 相同，但显式给出 capableWhen
// （"补上什么就能用"）。没给时本层按官方码用 forgeRC2CapableWhen 的默认文案。
func ForgeRC2UnsupportedWhen(capableWhen, code, format string, args ...any) *ForgeRC2Error {
	err := ForgeRC2Unsupported(code, format, args...)
	err.CapableWhen = capableWhen
	return err
}

// forgeRC2CapableWhen 是官方"缺能力"码的默认补救说明。
//
// 为什么放在 transport：`capableWhen` 是**线上信封**的一部分（四字段之外的兄弟
// 字段），由本层决定怎么出；而"缺哪项能力、补什么就能用"正是官方码的语义
// （review-unsupported = provider 不读评审线程；viewed-failed = 不保存已读标记）。
// 当前生产实现里这两个 501 的共同原因都是"本机没有 GitHub GraphQL 凭据"，
// 所以文案点明凭据；将来换 provider 时按码改这一张表即可。
func forgeRC2CapableWhen(code string) string {
	switch code {
	case CodeForgeReviewUnsupported:
		return "本机配好 GitHub 凭据（GITHUB_TOKEN / GH_TOKEN，或 `gh auth login` 后由 forge 取 " +
			"`gh auth token`）即可经 GraphQL reviewThreads 真读评审线程；未配凭据时如实报缺能力，" +
			"绝不返回空线程列表。"
	case CodeForgeViewedFailed:
		return "本机配好 GitHub 凭据（GITHUB_TOKEN / GH_TOKEN，或 `gh auth login`）即可经 GraphQL " +
			"`PullRequest.files.viewerViewedState` 真读已读标记；缺凭据时按官方语义报 null/失败，" +
			"不伪造标记。"
	}
	return ""
}

// ---------------------------------------------------------------------------
// 宿主实现函数表
// ---------------------------------------------------------------------------

// OfficialForgeRC2API 是宿主注入的真实实现。任一为 nil = 没接线 → 501 点名缺什么。
//
// 约定（与官方 ForgeProvider 一致）：
//   - "provider 不支持这条读"用 (nil, nil) 表达（官方结果 null），或用
//     ForgeRC2Unsupported/官方码错误表达（官方 union/错误）；
//   - 真实失败返回 *ForgeRC2Error 或任意 error（本层按方法兜底映射）；
//   - 任何方法都**不许**返回零值冒充数据。
type OfficialForgeRC2API struct {
	ChangeRequestCommits     func(ctx context.Context, sessionID, url string) (*OfficialForgeCommitList, error)
	ChangeRequestReview      func(ctx context.Context, sessionID, url, headSHA string, fresh bool) (*OfficialForgeReviewData, error)
	ChangeRequestReviewWrite func(ctx context.Context, sessionID, url string, write json.RawMessage) (OfficialForgeReviewWriteResult, error)
	ChangeRequestViewed      func(ctx context.Context, sessionID, url, headSHA string) (*OfficialForgeViewedFiles, error)
	DiffBases                func(ctx context.Context, sessionID, query string) (OfficialForgeBaseBranches, error)
	LocalCommits             func(ctx context.Context, sessionID string, base *string) (OfficialForgeLocalCommits, error)
	RequestReviews           func(ctx context.Context, sessionID, url string, reviewers []OfficialForgeReviewerRef) error
	RerunChecks              func(ctx context.Context, sessionID, url, headSHA string) error
	ReviewerCandidates       func(ctx context.Context, sessionID, url, query string) ([]OfficialForgeReviewerCandidate, error)
	SetFileViewed            func(ctx context.Context, sessionID, url string, request OfficialForgeViewedRequest) error
	UpdateBranch             func(ctx context.Context, sessionID, url, method, headSHA string) error
	WorktreeCleanup          func(ctx context.Context, sessionID string, revision *int64) (*OfficialForgeWorktreeCleanup, error)
}

var officialForgeRC2Mount = struct {
	mu     sync.RWMutex
	byServ map[*Server]OfficialForgeRC2API
}{byServ: map[*Server]OfficialForgeRC2API{}}

// AttachOfficialForgeRC2API 给这个 Server 挂上 rc.2 Forge 面的真实实现。
func (s *Server) AttachOfficialForgeRC2API(api OfficialForgeRC2API) {
	if s == nil {
		return
	}
	officialForgeRC2Mount.mu.Lock()
	defer officialForgeRC2Mount.mu.Unlock()
	officialForgeRC2Mount.byServ[s] = api
}

// DetachOfficialForgeRC2API 摘掉（测试清理用）。
func (s *Server) DetachOfficialForgeRC2API() {
	if s == nil {
		return
	}
	officialForgeRC2Mount.mu.Lock()
	defer officialForgeRC2Mount.mu.Unlock()
	delete(officialForgeRC2Mount.byServ, s)
}

func (s *Server) officialForgeRC2API() (OfficialForgeRC2API, bool) {
	if s == nil {
		return OfficialForgeRC2API{}, false
	}
	officialForgeRC2Mount.mu.RLock()
	defer officialForgeRC2Mount.mu.RUnlock()
	api, ok := officialForgeRC2Mount.byServ[s]
	return api, ok
}

// HasOfficialForgeRC2API 报告这台 Server 是否挂了任意一条 rc.2 Forge 实现。
func (s *Server) HasOfficialForgeRC2API() bool {
	api, ok := s.officialForgeRC2API()
	if !ok {
		return false
	}
	return api.ChangeRequestCommits != nil || api.ChangeRequestReview != nil ||
		api.ChangeRequestReviewWrite != nil || api.ChangeRequestViewed != nil ||
		api.DiffBases != nil || api.LocalCommits != nil || api.RequestReviews != nil ||
		api.RerunChecks != nil || api.ReviewerCandidates != nil || api.SetFileViewed != nil ||
		api.UpdateBranch != nil || api.WorktreeCleanup != nil
}

// ---------------------------------------------------------------------------
// HTTP 入口
// ---------------------------------------------------------------------------

// ServeOfficialForgeRC2 是 12 个方法的唯一入口（参数校验 + 结果/错误出手）。
func (s *Server) ServeOfficialForgeRC2(w http.ResponseWriter, r *http.Request, name string) {
	if !IsOfficialForgeRC2Method(name) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found",
			fmt.Sprintf("不是 rc.2 forgePanel 方法：%q", name)))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	api, attached := s.officialForgeRC2API()
	if !attached {
		writeOfficialError(w, officialNotImplemented(
			"%s 未接线：宿主没有 AttachOfficialForgeRC2API（rc.2 Forge 面没有 provider / worktree 服务）", name))
		return
	}
	ctx := r.Context()
	switch Method(name) {
	case MethodForgePanelChangeRequestCommits:
		sessionID, rawURL, err := forgeRC2SessionURL(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.ChangeRequestCommits == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "provider 不读 change request commits"))
			return
		}
		list, ferr := api.ChangeRequestCommits(ctx, sessionID, rawURL)
		if ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeDiffFailed)
			return
		}
		writeForgeRC2NullableResult(w, list)
	case MethodForgePanelChangeRequestReview:
		sessionID, rawURL, err := forgeRC2SessionURL(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		headSHA, err := forgeRC2RequiredString(params, "headSha")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		fresh, err := forgeRC2RequiredBool(params, "fresh")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.ChangeRequestReview == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "provider 不读 review threads（readReview 未实现）"))
			return
		}
		data, ferr := api.ChangeRequestReview(ctx, sessionID, rawURL, headSHA, fresh)
		if ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeReviewFailed)
			return
		}
		if data == nil {
			writeOfficialError(w, officialInternal("%s：宿主返回了空 review（不许用空值冒充没有线程）", name))
			return
		}
		writeJSON(w, http.StatusOK, data.value())
	case MethodForgePanelChangeRequestReviewWrite:
		sessionID, rawURL, err := forgeRC2SessionURL(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		rawWrite, ok := params.take("write")
		if !ok {
			writeOfficialError(w, officialBadRequest("缺 write"))
			return
		}
		if err := validateForgeRC2ReviewWrite(rawWrite); err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.ChangeRequestReviewWrite == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "provider 不写 review（writeReview 未实现）"))
			return
		}
		result, ferr := api.ChangeRequestReviewWrite(ctx, sessionID, rawURL, rawWrite)
		if ferr != nil {
			// 官方这条 Remote 对 provider 失败**不抛错**，而是回 union：
			// 没能确认结果就是 unknown（必须先到 host 上核对，别重试）。
			writeJSON(w, http.StatusOK, OfficialForgeReviewWriteResult{
				Code: "unknown", Message: ferr.Error(),
			}.value())
			return
		}
		writeJSON(w, http.StatusOK, result.value())
	case MethodForgePanelChangeRequestViewed:
		sessionID, rawURL, err := forgeRC2SessionURL(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		headSHA, err := forgeRC2RequiredString(params, "headSha")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.ChangeRequestViewed == nil {
			writeForgeRC2NullResult(w)
			return
		}
		viewed, ferr := api.ChangeRequestViewed(ctx, sessionID, rawURL, headSHA)
		if ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeViewedFailed)
			return
		}
		writeForgeRC2NullableResult(w, viewed)
	case MethodForgePanelDiffBases:
		sessionID, err := forgeRC2RequiredString(params, "sessionId")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		query, err := forgeRC2RequiredString(params, "query")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.DiffBases == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "宿主没有 git 执行器（diffBases 读本机分支）"))
			return
		}
		branches, ferr := api.DiffBases(ctx, sessionID, query)
		if ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeNotACheckout)
			return
		}
		writeJSON(w, http.StatusOK, forgeRC2BaseBranchesValue(branches))
	case MethodForgePanelLocalCommits:
		sessionID, err := forgeRC2RequiredString(params, "sessionId")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		base, err := forgeRC2NullableString(params, "base")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.LocalCommits == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "宿主没有 git 执行器（localCommits 读本机提交）"))
			return
		}
		list, ferr := api.LocalCommits(ctx, sessionID, base)
		if ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeNotACheckout)
			return
		}
		writeJSON(w, http.StatusOK, list)
	case MethodForgePanelRequestReviews:
		sessionID, rawURL, err := forgeRC2SessionURL(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		reviewers, err := forgeRC2Reviewers(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.RequestReviews == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "provider 不请求评审（requestReviews 未实现）"))
			return
		}
		if ferr := api.RequestReviews(ctx, sessionID, rawURL, reviewers); ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeActionFailed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case MethodForgePanelRerunChecks:
		sessionID, rawURL, err := forgeRC2SessionURL(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		headSHA, err := forgeRC2NestedString(params, "request", "headSha")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.RerunChecks == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "provider 不重跑 check（rerunChecks 未实现）"))
			return
		}
		if ferr := api.RerunChecks(ctx, sessionID, rawURL, headSHA); ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeActionFailed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case MethodForgePanelReviewerCandidates:
		sessionID, rawURL, err := forgeRC2SessionURL(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		query, err := forgeRC2RequiredString(params, "query")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.ReviewerCandidates == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "provider 不列评审候选（reviewerCandidates 未实现）"))
			return
		}
		candidates, ferr := api.ReviewerCandidates(ctx, sessionID, rawURL, query)
		if ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeReviewersFailed)
			return
		}
		if candidates == nil {
			candidates = []OfficialForgeReviewerCandidate{}
		}
		writeJSON(w, http.StatusOK, candidates)
	case MethodForgePanelSetFileViewed:
		sessionID, rawURL, err := forgeRC2SessionURL(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		request, err := forgeRC2ViewedRequest(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.SetFileViewed == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "provider 不保存已读标记（setViewed 未实现）"))
			return
		}
		if ferr := api.SetFileViewed(ctx, sessionID, rawURL, *request); ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeActionFailed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case MethodForgePanelUpdateBranch:
		sessionID, rawURL, err := forgeRC2SessionURL(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		method, err := forgeRC2RequiredString(params, "method")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if method != "merge" && method != "rebase" {
			writeOfficialError(w, officialBadRequest("method 只能是 merge 或 rebase，收到 %q", method))
			return
		}
		headSHA, err := forgeRC2RequiredString(params, "headSha")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.UpdateBranch == nil {
			writeOfficialError(w, forgeRC2NotAttached(name, "provider 不更新分支（updateBranch 未实现）"))
			return
		}
		if ferr := api.UpdateBranch(ctx, sessionID, rawURL, method, headSHA); ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusBadGateway, CodeForgeActionFailed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case MethodForgePanelWorktreeCleanup:
		sessionID, err := forgeRC2RequiredString(params, "sessionId")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		revision, err := forgeRC2NullableNumber(params, "revision")
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.WorktreeCleanup == nil {
			writeForgeRC2NullResult(w)
			return
		}
		cleanup, ferr := api.WorktreeCleanup(ctx, sessionID, revision)
		if ferr != nil {
			writeForgeRC2Error(w, ferr, http.StatusInternalServerError, "internal")
			return
		}
		writeForgeRC2NullableResult(w, cleanup)
	default:
		writeOfficialError(w, officialNotImplemented("不是 rc.2 forgePanel 方法：%q", name))
	}
}

// forgeRC2NotAttached 是"宿主装了 API 表但这一条是 nil"的 501。
//
// 它和"整表没挂"分开报，理由不同：整表没挂是装配漏了一行；这一条 nil 是
// provider 真的没有这项能力（官方语义里那是一个 `unsupported`）。
func forgeRC2NotAttached(name string, detail string) error {
	return officialNotImplemented("%s 未接线：%s", name, detail)
}

// writeForgeRC2Error 把宿主错误翻成四字段。
//
// 带 *ForgeRC2Error 的原样出（码是官方 `forgePanel/*`）；其余 error 按方法的
// 官方兜底码出（例如 commits 读失败 → forgePanel/diff-failed）。
func writeForgeRC2Error(w http.ResponseWriter, err error, status int, fallbackCode string) {
	var typed *ForgeRC2Error
	if errors.As(err, &typed) {
		code := typed.Code
		if code == "" {
			code = fallbackCode
		}
		s := typed.Status
		if s <= 0 {
			s = status
		}
		// 501（能力缺失）额外带 capableWhen：说清"补上什么就能用"。
		// 它是 error 的兄弟字段，四字段本身不动（与 cua/office kit 的 501 同形）。
		capableWhen := typed.CapableWhen
		if capableWhen == "" && s == http.StatusNotImplemented {
			capableWhen = forgeRC2CapableWhen(code)
		}
		if capableWhen != "" {
			writeJSON(w, s, errorBodyFor(code, typed.Message, map[string]any{"capableWhen": capableWhen}))
			return
		}
		writeJSON(w, s, errorBodyFor(code, typed.Message))
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeJSON(w, 499, errorBodyFor("canceled",
			"请求在完成前被取消；远端是否已写入未知，重试前先刷新 change request 状态"))
		return
	}
	if status <= 0 || status == http.StatusOK {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, errorBodyFor(fallbackCode, err.Error()))
}

// writeForgeRC2NullableResult 写"官方结果允许 null"的成功响应。
//
// 为什么绕开 writeJSON：它的空值守卫把 `{"result":null}` 变成 500（那条守卫
// 针对的是"handler 忘了给载荷"）。但这几个方法的官方契约里，null 是**确定的
// 答案**——provider 不读 commits / 不保存已读标记 / 没有 worktree 服务。把它
// 变成 500 等于把一个契约内的结果说成宿主故障；改成一个空对象又会和"确实没有
// 数据"混在一起。所以只在这三处显式写官方形状，并在注释里点名理由。
func writeForgeRC2NullableResult(w http.ResponseWriter, value any) {
	if isNilForgeRC2Value(value) {
		writeForgeRC2NullResult(w)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

// isNilForgeRC2Value 判断"接口里装的是 nil 指针/切片"这种情况。
//
// 为什么不能用 `value == nil`：宿主返回的是 `*OfficialForgeCommitList` 这种
// **类型化 nil**，装进 any 之后接口本身不是 nil，于是会被 writeJSON 当成
// "handler 给了载荷"，序列化成 {"result":null} 再被空值守卫变成 500——
// 而官方对这三个方法恰恰允许 null。必须按反射判 nil。
func isNilForgeRC2Value(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Interface, reflect.Chan, reflect.Func:
		return rv.IsNil()
	}
	return false
}

// writeForgeRC2NullResult 写 `{"result":null}`（官方 nullable 结果的出口）。
func writeForgeRC2NullResult(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{\"result\":null}\n"))
}

// forgeRC2BaseBranchesValue 保证三组字段都是数组/对象（不是 null），
// 因为官方 schema 要求 `remote`/`local` 是数组、`automatic` 可为 null。
func forgeRC2BaseBranchesValue(b OfficialForgeBaseBranches) OfficialForgeBaseBranches {
	if b.Remote == nil {
		b.Remote = []OfficialForgeBaseRef{}
	}
	if b.Local == nil {
		b.Local = []OfficialForgeBaseRef{}
	}
	return b
}

// value 让 *OfficialForgeReviewData 直接进 writeJSON（非 nil）。
func (d OfficialForgeReviewData) value() any {
	if d.Threads == nil {
		d.Threads = []OfficialForgeReviewThread{}
	}
	return d
}

// ---------------------------------------------------------------------------
// 参数解析（官方位置参数名 = L1 的键）
// ---------------------------------------------------------------------------

// forgeRC2SessionURL 取 (sessionId, url) 这对必填参数。
func forgeRC2SessionURL(p *officialParams) (string, string, error) {
	sessionID, err := forgeRC2RequiredString(p, "sessionId")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", "", officialBadRequest("sessionId 是空串")
	}
	rawURL, err := forgeRC2RequiredString(p, "url")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(rawURL) == "" {
		return "", "", officialBadRequest("url 是空串")
	}
	return sessionID, rawURL, nil
}

func forgeRC2RequiredString(p *officialParams, name string) (string, error) {
	raw, ok := p.take(name)
	if !ok {
		return "", officialBadRequest("缺 %s", name)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", officialBadRequest("%s 必须是字符串", name)
	}
	return s, nil
}

func forgeRC2RequiredBool(p *officialParams, name string) (bool, error) {
	raw, ok := p.take(name)
	if !ok {
		return false, officialBadRequest("缺 %s", name)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, officialBadRequest("%s 必须是布尔值", name)
	}
	return b, nil
}

// forgeRC2NullableString 取官方 `string | null`（键必须存在，值可为 null）。
func forgeRC2NullableString(p *officialParams, name string) (*string, error) {
	raw, ok := p.take(name)
	if !ok {
		return nil, officialBadRequest("缺 %s（官方是 string|null，键必须给）", name)
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, officialBadRequest("%s 必须是字符串或 null", name)
	}
	return &s, nil
}

// forgeRC2NullableNumber 取官方 `number | null`。
func forgeRC2NullableNumber(p *officialParams, name string) (*int64, error) {
	raw, ok := p.take(name)
	if !ok {
		return nil, officialBadRequest("缺 %s（官方是 number|null，键必须给）", name)
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	// 必须是 JSON **数字**：字符串 "2" 不算（官方 schema 是 number，strict
	// 解码不会把字符串转成数字）。
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, officialBadRequest("%s 必须是数字或 null", name)
	}
	number, ok := decoded.(float64)
	if !ok {
		return nil, officialBadRequest("%s 必须是数字或 null", name)
	}
	value := int64(number)
	return &value, nil
}

// forgeRC2NestedString 取 `{name: {field: string}}` 里的必填字符串。
func forgeRC2NestedString(p *officialParams, name, field string) (string, error) {
	raw, ok := p.take(name)
	if !ok {
		return "", officialBadRequest("缺 %s", name)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return "", officialBadRequest("%s 必须是 JSON 对象", name)
	}
	value, ok := obj[field]
	if !ok {
		return "", officialBadRequest("缺 %s.%s", name, field)
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return "", officialBadRequest("%s.%s 必须是字符串", name, field)
	}
	if strings.TrimSpace(s) == "" {
		return "", officialBadRequest("%s.%s 是空串", name, field)
	}
	return s, nil
}

// forgeRC2Reviewers 解析 requestReviews 的 `reviewers[]`（官方最小 1 个、login 非空）。
func forgeRC2Reviewers(p *officialParams) ([]OfficialForgeReviewerRef, error) {
	raw, ok := p.take("reviewers")
	if !ok {
		return nil, officialBadRequest("缺 reviewers")
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, officialBadRequest("reviewers 必须是对象数组")
	}
	if len(items) == 0 {
		return nil, officialBadRequest("reviewers 不能是空数组（官方要求至少一个）")
	}
	out := make([]OfficialForgeReviewerRef, 0, len(items))
	for index, item := range items {
		kindRaw, ok := item["kind"]
		if !ok {
			return nil, officialBadRequest("reviewers[%d] 缺 kind", index)
		}
		var kind string
		if err := json.Unmarshal(kindRaw, &kind); err != nil {
			return nil, officialBadRequest("reviewers[%d].kind 必须是字符串", index)
		}
		switch kind {
		case "user", "bot", "team":
		default:
			return nil, officialBadRequest("reviewers[%d].kind 只能是 user/bot/team，收到 %q", index, kind)
		}
		loginRaw, ok := item["login"]
		if !ok {
			return nil, officialBadRequest("reviewers[%d] 缺 login", index)
		}
		var login string
		if err := json.Unmarshal(loginRaw, &login); err != nil {
			return nil, officialBadRequest("reviewers[%d].login 必须是字符串", index)
		}
		if strings.TrimSpace(login) == "" {
			return nil, officialBadRequest("reviewers[%d].login 是空串", index)
		}
		out = append(out, OfficialForgeReviewerRef{Kind: kind, Login: login})
	}
	return out, nil
}

// forgeRC2ViewedRequest 解析 setFileViewed 的 `request`（官方 path 非空）。
func forgeRC2ViewedRequest(p *officialParams) (*OfficialForgeViewedRequest, error) {
	raw, ok := p.take("request")
	if !ok {
		return nil, officialBadRequest("缺 request")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, officialBadRequest("request 必须是 JSON 对象")
	}
	request := &OfficialForgeViewedRequest{}
	pathRaw, ok := obj["path"]
	if !ok {
		return nil, officialBadRequest("缺 request.path")
	}
	if err := json.Unmarshal(pathRaw, &request.Path); err != nil {
		return nil, officialBadRequest("request.path 必须是字符串")
	}
	if request.Path == "" {
		return nil, officialBadRequest("request.path 是空串（官方直接拒绝）")
	}
	viewedRaw, ok := obj["viewed"]
	if !ok {
		return nil, officialBadRequest("缺 request.viewed")
	}
	if err := json.Unmarshal(viewedRaw, &request.Viewed); err != nil {
		return nil, officialBadRequest("request.viewed 必须是布尔值")
	}
	headRaw, ok := obj["headSha"]
	if !ok {
		return nil, officialBadRequest("缺 request.headSha")
	}
	if err := json.Unmarshal(headRaw, &request.HeadSHA); err != nil {
		return nil, officialBadRequest("request.headSha 必须是字符串")
	}
	if strings.TrimSpace(request.HeadSHA) == "" {
		return nil, officialBadRequest("request.headSha 是空串")
	}
	return request, nil
}

// validateForgeRC2ReviewWrite 校验 changeRequestReviewWrite 的 union。
//
// 官方 `write` 是 7 个分支的判别 union，共同要求 `headSha` 与 `kind`；每个分支
// 各有必填字段（见 typert 描述符）。对象本身 `additionalProperties: true`，
// 所以不认识的键放过，但**必填键一个都不能少**。
func validateForgeRC2ReviewWrite(raw json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return officialBadRequest("write 必须是 JSON 对象")
	}
	required := func(field string) error {
		value, ok := obj[field]
		if !ok {
			return officialBadRequest("write 缺 %s", field)
		}
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return officialBadRequest("write.%s 必须是字符串", field)
		}
		if strings.TrimSpace(s) == "" {
			return officialBadRequest("write.%s 是空串", field)
		}
		return nil
	}
	if err := required("headSha"); err != nil {
		return err
	}
	if err := required("kind"); err != nil {
		return err
	}
	var kind string
	_ = json.Unmarshal(obj["kind"], &kind)
	switch kind {
	case "reply":
		if err := required("thread"); err != nil {
			return err
		}
		if err := required("body"); err != nil {
			return err
		}
		var pending bool
		if rawPending, ok := obj["pending"]; !ok {
			return officialBadRequest("write 缺 pending")
		} else if err := json.Unmarshal(rawPending, &pending); err != nil {
			return officialBadRequest("write.pending 必须是布尔值")
		}
		return nil
	case "resolve":
		if err := required("thread"); err != nil {
			return err
		}
		var resolved bool
		if rawResolved, ok := obj["resolved"]; !ok {
			return officialBadRequest("write 缺 resolved")
		} else if err := json.Unmarshal(rawResolved, &resolved); err != nil {
			return officialBadRequest("write.resolved 必须是布尔值")
		}
		return nil
	case "edit-comment":
		if err := required("comment"); err != nil {
			return err
		}
		return required("body")
	case "delete-comment":
		return required("comment")
	case "add-pending":
		return forgeRC2LineComment(obj, "comment")
	case "submit-pending":
		if err := required("review"); err != nil {
			return err
		}
		event, err := forgeRC2RequiredString(mapToParams(obj), "event")
		if err != nil {
			return officialBadRequest("write.event 必填")
		}
		switch event {
		case "comment", "approve", "request-changes":
		default:
			return officialBadRequest("write.event 只能是 comment/approve/request-changes，收到 %q", event)
		}
		body, ok := obj["body"]
		if !ok {
			return officialBadRequest("write 缺 body")
		}
		var text string
		if err := json.Unmarshal(body, &text); err != nil {
			return officialBadRequest("write.body 必须是字符串")
		}
		return nil
	case "discard-pending":
		return required("review")
	default:
		return officialBadRequest("write.kind 只能是 reply/resolve/edit-comment/delete-comment/add-pending/submit-pending/discard-pending，收到 %q", kind)
	}
}

// forgeRC2LineComment 校验官方 ForgeLineComment 形状（add-pending 的 comment）。
func forgeRC2LineComment(obj map[string]json.RawMessage, field string) error {
	raw, ok := obj[field]
	if !ok {
		return officialBadRequest("write 缺 %s", field)
	}
	var comment map[string]json.RawMessage
	if err := json.Unmarshal(raw, &comment); err != nil || comment == nil {
		return officialBadRequest("write.%s 必须是 JSON 对象", field)
	}
	var path, side string
	if pathRaw, ok := comment["path"]; !ok {
		return officialBadRequest("write.%s.path 必填", field)
	} else if err := json.Unmarshal(pathRaw, &path); err != nil || strings.TrimSpace(path) == "" {
		return officialBadRequest("write.%s.path 必须是非空字符串", field)
	}
	if sideRaw, ok := comment["side"]; !ok {
		return officialBadRequest("write.%s.side 必填", field)
	} else if err := json.Unmarshal(sideRaw, &side); err != nil || (side != "L" && side != "R") {
		return officialBadRequest("write.%s.side 只能是 L 或 R", field)
	}
	var startLine, endLine int
	if rawStart, ok := comment["startLine"]; !ok {
		return officialBadRequest("write.%s.startLine 必填", field)
	} else if err := json.Unmarshal(rawStart, &startLine); err != nil {
		return officialBadRequest("write.%s.startLine 必须是数字", field)
	}
	if rawEnd, ok := comment["endLine"]; !ok {
		return officialBadRequest("write.%s.endLine 必填", field)
	} else if err := json.Unmarshal(rawEnd, &endLine); err != nil {
		return officialBadRequest("write.%s.endLine 必须是数字", field)
	}
	if startLine < 1 || endLine < startLine {
		return officialBadRequest("write.%s 行号非法：startLine=%d endLine=%d", field, startLine, endLine)
	}
	var body string
	if rawBody, ok := comment["body"]; !ok {
		return officialBadRequest("write.%s.body 必填", field)
	} else if err := json.Unmarshal(rawBody, &body); err != nil || strings.TrimSpace(body) == "" {
		return officialBadRequest("write.%s.body 必须是非空字符串", field)
	}
	return nil
}

// mapToParams 让已有 helper 能复用同一个 map（只用于再取一个必填串）。
func mapToParams(m map[string]json.RawMessage) *officialParams {
	return &officialParams{m: m, used: map[string]bool{}}
}
