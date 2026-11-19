package transport

// 官方 rc.2 `forgePanel.{follow,refresh,track,dismiss}` 的**真实现**接线层。
//
// # 契约（只读镜像，逐字段对齐官方 zod）
//
//	%TEMP%/official-api-src/dsh/node_modules/@deepseek-ai/dsh-experimental-client-ui-forge/lib/typert.host.js
//	forgePanel/follow   mode:'stream'  params {sessionId} → ForgePanelState       L934-968
//	forgePanel/track    params {sessionId, url}           → void                 L1484
//	forgePanel/dismiss  params {sessionId, url}           → void                 L883
//	forgePanel/refresh  params {sessionId}                → void                 L1227
//
// 官方 follow 的 `result.typeSymbol` 是
// `@deepseek-ai/dsh-experimental-client-ui-forge/types#ForgePanelState`，描述
// "Stream the panel state of a Session: the current state first, then each later
// state. Intermediate states may be skipped when the reader is slower than the
// updates." —— 也就是说**每一帧就是一个完整的 ForgePanelState**，没有
// baseline/upsert/remove 这层差异信封。
//
// 旧实现发的是 `{type:'baseline', value:{bindings}}` 差异帧，来源写的是
// `@deepseek-ai/dsh-api-forge`；那个包在 rc.2 镜像里**不存在**
// （`ls %TEMP%/official-api-src/dsh/node_modules/@deepseek-ai/` 只有
// `dsh-experimental-client-ui-forge`）。差异帧与官方契约不符，已按官方 zod 重写。
//
// # worktree 两张卡为什么只能走这条面
//
// `worktree.editable` 与 `worktree.creation` **只**出现在
// `ForgePanelState.worktree`（typert.host.js:266-298 / 声明 1945-1946）。
// `session.projections` 的 `forgeWorktree` 键是 `ForgeSessionWorktree`：
//
//	dsh-api-session-controller/lib/typert.host.js:248-253（zod）
//	dsh-api-session-controller/lib/typert.host.js:3107-3108（声明）
//	→ { path, branch, from, baseCommit, repositoryRoot } 五个字段
//
// 那个投影**装不下** editable / creation（官方 zod 是 z.object，未知键会被
// 剥掉），所以"在 session.projections 里发 forgeWorktree"补不出这两张卡。
// 面板状态流是唯一合法出口 —— 这是本文件存在的理由。
//
// # 本文件的分工
//
//	OfficialForgePanelState          官方 ForgePanelState 的完整 Go 形状（逐字段）
//	OfficialForgePanelAPI            宿主实现函数表（nil = 未接线 → 501）
//	ServeOfficialForgePanel          unary 三个方法的纯函数入口
//	OfficialForgePanelFollowStream   follow 的帧机（首次当前状态，之后每个新状态）
//	ServeOfficialForgePanelFollowHTTP follow 的 HTTP NDJSON 出口
//
// 状态**内容**由宿主填（它才知道会话目录、worktree 账本、绑定存储）；本层只保证
// 线上形状是官方那个形状，且**不补默认值、不编造字段**。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// OfficialForgePanelBinding 是绑定存储的 wire 形状（track/dismiss/refresh 的
// 内部载体；follow 的 changeRequests 由宿主从同一份账本翻成官方形状）。
type OfficialForgePanelBinding struct {
	SessionID   string `json:"sessionId"`
	URL         string `json:"url"`
	Provider    string `json:"provider,omitempty"`
	Tracked     bool   `json:"tracked"`
	State       string `json:"state,omitempty"`
	Title       string `json:"title,omitempty"`
	HeadSHA     string `json:"headSha,omitempty"`
	TrackedAt   string `json:"trackedAt,omitempty"`
	DismissedAt string `json:"dismissedAt,omitempty"`
	RefreshedAt string `json:"refreshedAt,omitempty"`
}

// ---------------------------------------------------------------------------
// 官方 ForgePanelState（逐字段照 typert.host.js:1941-1990 的声明）
// ---------------------------------------------------------------------------

// OfficialForgeCheckout 是官方 `ForgeCheckout`（typert.host.js:1894）：
// root / branch / linked / commit。
type OfficialForgeCheckout struct {
	Root   string  `json:"root"`
	Branch *string `json:"branch"`
	Linked bool    `json:"linked"`
	Commit *string `json:"commit"`
}

// OfficialForgeCheckoutState 是官方 `ForgeCheckoutState` 的四个分支
// （typert.host.js:1897-1898）：
//
//	{state:'pending'} | {state:'none'} | {state:'failed', message} | {state:'ready', checkout}
//
// 用自定义 MarshalJSON 保证线上**只有**官方那几组键（zod union 按形状判别）。
type OfficialForgeCheckoutState struct {
	// State 取 pending / none / failed / ready。
	State string
	// Message 只在 failed 分支出现（官方必填非空字符串）。
	Message string
	// Checkout 只在 ready 分支出现。
	Checkout *OfficialForgeCheckout
}

// MarshalJSON 按官方 union 的四个分支输出。
func (c OfficialForgeCheckoutState) MarshalJSON() ([]byte, error) {
	switch c.State {
	case "none", "pending":
		return json.Marshal(struct {
			State string `json:"state"`
		}{State: c.State})
	case "failed":
		return json.Marshal(struct {
			State   string `json:"state"`
			Message string `json:"message"`
		}{State: c.State, Message: c.Message})
	case "ready":
		if c.Checkout == nil {
			return nil, fmt.Errorf("forgePanel: checkout state=ready 时 checkout 不能为 nil")
		}
		return json.Marshal(struct {
			State    string                 `json:"state"`
			Checkout *OfficialForgeCheckout `json:"checkout"`
		}{State: c.State, Checkout: c.Checkout})
	}
	return nil, fmt.Errorf("forgePanel: 非法的 checkout state %q", c.State)
}

// OfficialForgeReadiness 是官方 `ForgeReadiness`（typert.host.js:1949-1950）。
type OfficialForgeReadiness struct {
	// State 取 ready / unauthenticated / unavailable。
	State   string  `json:"state"`
	Message *string `json:"message"`
}

// OfficialForgeChangedFile 是官方 `ForgeChangedFile`（local 投影用）。
type OfficialForgeChangedFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
}

// OfficialForgeLocalChanges 是官方 `ForgeLocalChanges`（typert.host.js:1921-1922）。
type OfficialForgeLocalChanges struct {
	Base  string                     `json:"base"`
	Files []OfficialForgeChangedFile `json:"files"`
}

// OfficialForgePanelChangeRequestRef 是官方 `ChangeRequestRef`
// （typert.host.js:1766-1767：`{repository: ForgeRepository, number}`）。
//
// 与 handlers_forge_change_request_official.go 里那个**扁平**的
// `OfficialForgeChangeRequestRef`（`{forge,host,owner,name,number}`）不是同一个
// 形状：那个是 changeRequestStates 结果里的历史投影，这里是面板状态要的官方嵌套
// 形状。两者不能合并，也不能互相当别名。
type OfficialForgePanelChangeRequestRef struct {
	Repository OfficialForgeRepository `json:"repository"`
	Number     int                     `json:"number"`
}

// OfficialForgePanelChangeRequest 是官方 `ForgePanelChangeRequest`
// （typert.host.js:1937-1938 = SessionChangeRequest + snapshot）。
//
// `update` / `snapshot` 官方都可为 null：宿主只填**真的知道**的部分，不知道的
// 就留 null（不编造 PR 状态）。
type OfficialForgePanelChangeRequest struct {
	URL      string                              `json:"url"`
	Origin   string                              `json:"origin"`
	Ref      *OfficialForgePanelChangeRequestRef `json:"ref"`
	Update   json.RawMessage                     `json:"update"`
	Snapshot json.RawMessage                     `json:"snapshot"`
}

// OfficialForgeWorktreeRequest 是官方 `ForgeWorktreeRequest`（typert.host.js:1981-1982）。
type OfficialForgeWorktreeRequest struct {
	From *string `json:"from"`
}

// OfficialForgeSessionWorktree 是官方 `ForgeSessionWorktree`（typert.host.js:3107-3108）。
type OfficialForgeSessionWorktree struct {
	Path           string  `json:"path"`
	Branch         string  `json:"branch"`
	From           *string `json:"from"`
	BaseCommit     string  `json:"baseCommit"`
	RepositoryRoot string  `json:"repositoryRoot"`
}

// OfficialForgeWorktreeCreation 是官方 `ForgeWorktreeCreation`（typert.host.js:1973-1974）。
//
// phase 只有 prepare / checkout 两个官方词（"prepare validates and creates the
// branch, then checkout writes the files"）。
type OfficialForgeWorktreeCreation struct {
	Phase  string  `json:"phase"`
	Branch string  `json:"branch"`
	From   *string `json:"from"`
	Held   bool    `json:"held"`
}

// OfficialForgeWorktreeFailure 是官方 `ForgeWorktreeFailure`（typert.host.js:1977-1978）。
type OfficialForgeWorktreeFailure struct {
	Branch  *string `json:"branch"`
	From    *string `json:"from"`
	Phase   string  `json:"phase"`
	Message string  `json:"message"`
}

// OfficialForgeMissingWorktree 是官方 `ForgeMissingWorktree`（typert.host.js:1933-1934）。
type OfficialForgeMissingWorktree struct {
	BranchExists *bool `json:"branchExists"`
}

// OfficialForgeWorktreeRestoration 是官方 `ForgeWorktreeRestoration`
// （typert.host.js:1985-1986）。
type OfficialForgeWorktreeRestoration struct {
	Phase string `json:"phase"`
}

// OfficialForgePanelWorktree 是官方 `ForgePanelWorktree`（typert.host.js:1945-1946）。
//
// 九个子字段一个不少：官方 zod 是 `z.object({...})`，线上必须**九个键全在**
// （可空的地方给 null），少一个键就是一个"字段缺了"的契约违规。
type OfficialForgePanelWorktree struct {
	// Editable：a worktree service is mounted, no turn has started, and none
	// was created（typert.host.js:267）。
	Editable    bool                              `json:"editable"`
	Blocker     *string                           `json:"blocker"`
	Request     *OfficialForgeWorktreeRequest     `json:"request"`
	Created     *OfficialForgeSessionWorktree     `json:"created"`
	Creation    *OfficialForgeWorktreeCreation    `json:"creation"`
	Failure     *OfficialForgeWorktreeFailure     `json:"failure"`
	Missing     *OfficialForgeMissingWorktree     `json:"missing"`
	Restoration *OfficialForgeWorktreeRestoration `json:"restoration"`
	Waiting     *string                           `json:"waiting"`
}

// OfficialForgePanelState 是官方 `ForgePanelState`（typert.host.js:1941-1942）。
type OfficialForgePanelState struct {
	Directory      string                            `json:"directory"`
	Checkout       OfficialForgeCheckoutState        `json:"checkout"`
	Repository     *OfficialForgeRepository          `json:"repository"`
	Readiness      *OfficialForgeReadiness           `json:"readiness"`
	ChangeRequests []OfficialForgePanelChangeRequest `json:"changeRequests"`
	Worktree       OfficialForgePanelWorktree        `json:"worktree"`
	Local          *OfficialForgeLocalChanges        `json:"local"`
}

// OfficialForgeCheckoutNone 是"这个会话不在 checkout 里"的官方值。
//
// 导出是为了让宿主编面板状态时**只有一处**能拼出这个 union 分支（拼错
// state 词就是线上契约违规，自定义 MarshalJSON 会直接报错）。
func OfficialForgeCheckoutNone() OfficialForgeCheckoutState {
	return OfficialForgeCheckoutState{State: "none"}
}

// officialForgeStringPtr 是官方可空字符串字段的取值助手（空串 → null）。
func officialForgeStringPtr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

// ---------------------------------------------------------------------------
// 宿主函数表
// ---------------------------------------------------------------------------

// OfficialForgePanelAPI 是宿主实现函数表。任一为 nil 表示"这条能力没接线"，
// 调用方必须回 501 —— 不允许静默成功。
type OfficialForgePanelAPI struct {
	Track   func(sessionID, url string) (OfficialForgePanelBinding, error)
	Dismiss func(sessionID, url string) (OfficialForgePanelBinding, error)
	Refresh func(ctx context.Context, sessionID, url string) ([]OfficialForgePanelBinding, error)
	// State 返回**某个会话**的完整官方 ForgePanelState（follow 的每一帧就是它）。
	// 这是 worktree.editable / worktree.creation 的唯一读出口。
	State func(ctx context.Context, sessionID string) (OfficialForgePanelState, error)
	// Wait 阻塞到该会话面板状态可能变化（或 ctx 取消）。nil 时帧机用
	// officialStreamPollInterval 轮询兜底 —— 仍是真的读状态，不是假帧。
	Wait func(ctx context.Context, sessionID string) error
}

// forgePanelMount 把实现挂到某个 Server 上（两个 Server 各挂各的）。
var forgePanelMount = struct {
	mu     sync.RWMutex
	byServ map[*Server]OfficialForgePanelAPI
}{byServ: map[*Server]OfficialForgePanelAPI{}}

// AttachOfficialForgePanelAPI 给这个 Server 挂上真实实现。
func (s *Server) AttachOfficialForgePanelAPI(api OfficialForgePanelAPI) {
	if s == nil {
		return
	}
	forgePanelMount.mu.Lock()
	defer forgePanelMount.mu.Unlock()
	forgePanelMount.byServ[s] = api
}

// DetachOfficialForgePanelAPI 摘掉（测试清理用）。
func (s *Server) DetachOfficialForgePanelAPI() {
	if s == nil {
		return
	}
	forgePanelMount.mu.Lock()
	defer forgePanelMount.mu.Unlock()
	delete(forgePanelMount.byServ, s)
}

func (s *Server) officialForgePanelAPI() OfficialForgePanelAPI {
	if s == nil {
		return OfficialForgePanelAPI{}
	}
	forgePanelMount.mu.RLock()
	defer forgePanelMount.mu.RUnlock()
	return forgePanelMount.byServ[s]
}

// forgePanelWired 报告这台 Server 上是否有任何一份 forgePanel 实现。
//
// 全 nil = 完全没接线：分派层据此落既有的逐方法 501（语义与接线前一致）。
func (api OfficialForgePanelAPI) forgePanelWired() bool {
	return api.Track != nil || api.Dismiss != nil || api.Refresh != nil || api.State != nil
}

// IsOfficialForgePanelStateMethod 报告是不是本文件实现的 4 个方法。
func IsOfficialForgePanelStateMethod(name string) bool {
	switch Method(name) {
	case MethodForgePanelFollow, MethodForgePanelRefresh, MethodForgePanelTrack, MethodForgePanelDismiss:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// unary 三个方法
// ---------------------------------------------------------------------------

func officialForgePanelBindingValue(b OfficialForgePanelBinding) map[string]any {
	return map[string]any{"binding": b}
}

func officialForgePanelTrack(api OfficialForgePanelAPI, p *officialParams) (any, error) {
	sessionID, url, err := officialForgePanelRef(p)
	if err != nil {
		return nil, err
	}
	if api.Track == nil {
		return nil, officialNotImplemented("forgePanel.track 未接线：宿主没有 forgeSession 绑定存储")
	}
	b, err := api.Track(sessionID, url)
	if err != nil {
		return nil, officialForgePanelFault(err)
	}
	return officialForgePanelBindingValue(b), nil
}

func officialForgePanelDismiss(api OfficialForgePanelAPI, p *officialParams) (any, error) {
	sessionID, url, err := officialForgePanelRef(p)
	if err != nil {
		return nil, err
	}
	if api.Dismiss == nil {
		return nil, officialNotImplemented("forgePanel.dismiss 未接线：宿主没有 forgeSession 绑定存储")
	}
	b, err := api.Dismiss(sessionID, url)
	if err != nil {
		return nil, officialForgePanelFault(err)
	}
	return officialForgePanelBindingValue(b), nil
}

func officialForgePanelRefresh(api OfficialForgePanelAPI, p *officialParams) (any, error) {
	sessionID, _, err := p.strAlias("sessionId", "sessionID", "id")
	if err != nil {
		return nil, err
	}
	// url 可选：不给就刷新该会话的全部绑定（官方 refresh 的语义）。
	url := ""
	if p.has("url") {
		u, err := p.str("url")
		if err != nil {
			return nil, err
		}
		url = u
	} else if p.has("changeRequestUrl") {
		u, err := p.str("changeRequestUrl")
		if err != nil {
			return nil, err
		}
		url = u
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	if api.Refresh == nil {
		return nil, officialNotImplemented("forgePanel.refresh 未接线：宿主没有 forgeSession 绑定存储")
	}
	bindings, err := api.Refresh(context.Background(), sessionID, url)
	if err != nil {
		return nil, officialForgePanelFault(err)
	}
	if bindings == nil {
		bindings = []OfficialForgePanelBinding{}
	}
	return map[string]any{"bindings": bindings}, nil
}

func officialForgePanelRef(p *officialParams) (string, string, error) {
	sessionID, _, err := p.strAlias("sessionId", "sessionID", "id")
	if err != nil {
		return "", "", err
	}
	rawURL, _, err := p.strAlias("url", "changeRequestUrl")
	if err != nil {
		return "", "", err
	}
	if err := p.finish(); err != nil {
		return "", "", err
	}
	return sessionID, rawURL, nil
}

// officialForgePanelSession 只读官方 follow/refresh 那一个位置参数 `sessionId`。
func officialForgePanelSession(p *officialParams) (string, error) {
	sessionID, _, err := p.strAlias("sessionId", "sessionID", "id")
	if err != nil {
		return "", err
	}
	if sessionID == "" {
		return "", officialBadRequest("sessionId 是空串")
	}
	return sessionID, p.finish()
}

// officialForgePanelFault 把存储错误翻成官方四字段。
func officialForgePanelFault(err error) error {
	if err == nil {
		return nil
	}
	var typed *officialError
	if errors.As(err, &typed) {
		return err
	}
	switch {
	case errors.Is(err, ErrForgeBindingNotFound):
		return &officialError{status: http.StatusNotFound, code: "not_found", msg: err.Error()}
	case errors.Is(err, ErrForgeInvalidURL), errors.Is(err, ErrForgeBadParam):
		return &officialError{status: http.StatusBadRequest, code: "bad_request", msg: err.Error()}
	case errors.Is(err, ErrForgeNoProvider):
		return &officialError{status: http.StatusNotImplemented, code: "not_implemented", msg: err.Error()}
	}
	return &officialError{status: http.StatusInternalServerError, code: "internal", msg: err.Error()}
}

// 宿主侧用 errors.Is 判断的哨兵错误。
//
// 为什么在这里定义而不是 import internal/forge：R7 的方向是"宿主 → Transport"，
// Transport 不认识领域包。宿主把自己的错误包成这些哨兵即可，两边都不越界。
var (
	// ErrForgeBindingNotFound 表示这条 (session,url) 绑定不存在。
	ErrForgeBindingNotFound = errors.New("forge binding not found")
	// ErrForgeInvalidURL 表示 url 不是合法 code host URL。
	ErrForgeInvalidURL = errors.New("forge invalid url")
	// ErrForgeBadParam 表示参数本身不合法。
	ErrForgeBadParam = errors.New("forge bad param")
	// ErrForgeNoProvider 表示宿主没配 provider。
	ErrForgeNoProvider = errors.New("forge no provider")
)

// ServeOfficialForgePanel 是 unary 三个方法的纯函数入口。
func ServeOfficialForgePanel(w http.ResponseWriter, r *http.Request, name string, api OfficialForgePanelAPI) {
	if !IsOfficialForgePanelStateMethod(name) || Method(name) == MethodForgePanelFollow {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			fmt.Sprintf("不是 forgePanel 的 unary 方法：%q", name)))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	var result any
	switch Method(name) {
	case MethodForgePanelTrack:
		result, err = officialForgePanelTrack(api, params)
	case MethodForgePanelDismiss:
		result, err = officialForgePanelDismiss(api, params)
	case MethodForgePanelRefresh:
		result, err = officialForgePanelRefresh(api, params)
	}
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
// follow 帧机
// ---------------------------------------------------------------------------

// OfficialForgePanelFollowStream 是 forgePanel.follow 的帧机。
//
// 官方语义（typert.host.js:936）："the current state first, then each later
// state. Intermediate states may be skipped when the reader is slower than the
// updates." 所以：
//
//   - 首帧一定是**当前**完整状态；
//   - 之后只在状态真的变了才出帧（按序列化字节比较，不看指针）；
//   - 中间态可以跳（读得慢就只看到最新的）；
//   - 没有变化时阻塞在 Wait / ctx 上，不空转。
type OfficialForgePanelFollowStream struct {
	source    OfficialForgePanelAPI
	sessionID string

	opened bool
	// last 是上一次发出去的 state 序列化结果（用来判断"真的变了"）。
	last []byte
}

var _ OfficialFrameStream = (*OfficialForgePanelFollowStream)(nil)

// OpenOfficialForgePanelFollowStream 造一条 follow 流（sessionId 必填）。
func OpenOfficialForgePanelFollowStream(api OfficialForgePanelAPI, sessionID string) *OfficialForgePanelFollowStream {
	return &OfficialForgePanelFollowStream{source: api, sessionID: sessionID}
}

func (s *OfficialForgePanelFollowStream) Close() {}

func (s *OfficialForgePanelFollowStream) Next(ctx context.Context) (json.RawMessage, bool, *OfficialStreamFault) {
	for {
		if s.source.State == nil {
			return nil, false, &OfficialStreamFault{
				Code: "not_implemented", Status: http.StatusNotImplemented,
				Message: "forgePanel.follow 未接线：宿主没有 forgeSession 面板状态源",
			}
		}
		state, err := s.source.State(ctx, s.sessionID)
		if err != nil {
			return nil, false, officialStreamFaultOf(MethodForgePanelFollow, err)
		}
		raw, merr := json.Marshal(state)
		if merr != nil {
			return nil, false, &OfficialStreamFault{
				Code: "internal", Status: http.StatusInternalServerError,
				Message: fmt.Sprintf("序列化 ForgePanelState 失败：%v", merr),
			}
		}
		// 首帧永远发；之后只有真的变了才发。
		if !s.opened || !bytes.Equal(raw, s.last) {
			s.opened = true
			s.last = raw
			return raw, false, nil
		}
		if s.source.Wait != nil {
			if werr := s.source.Wait(ctx, s.sessionID); werr != nil {
				if errors.Is(werr, context.Canceled) || errors.Is(werr, context.DeadlineExceeded) {
					return nil, true, nil
				}
				return nil, false, officialStreamFaultOf(MethodForgePanelFollow, werr)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, true, nil
		case <-time.After(officialStreamPollInterval):
		}
	}
}

// ServeOfficialForgePanelFollowHTTP 是 `forgePanel.follow` 的 HTTP NDJSON 出口。
//
// 头必须**在**第一行之前写：客户端拿到 200 + ndjson 才知道"订阅成功"；
// 未接线/参数错仍在写头之前回四字段（不是 200 之后报错）。
func (s *Server) ServeOfficialForgePanelFollowHTTP(w http.ResponseWriter, r *http.Request) {
	api := s.officialForgePanelAPI()
	if api.State == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			"forgePanel.follow 未接线：宿主没有 forgeSession 面板状态源"))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	sessionID, err := officialForgePanelSession(params)
	if err != nil {
		writeOfficialError(w, err)
		return
	}

	stream := OpenOfficialForgePanelFollowStream(api, sessionID)
	defer stream.Close()

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	for {
		frame, done, fault := stream.Next(r.Context())
		if fault != nil {
			// 头已经发出：只能在流里报错（客户端据此重连/降级）。
			raw, _ := json.Marshal(map[string]any{
				"error": map[string]any{"code": fault.Code, "message": fault.Message},
			})
			_, _ = w.Write(append(raw, '\n'))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		if done {
			return
		}
		if len(frame) == 0 {
			continue
		}
		if _, werr := w.Write(append(frame, '\n')); werr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
