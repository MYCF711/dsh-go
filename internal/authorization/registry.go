package authorization

// 官方 base 插件 #23 `authorization`（`@deepseek-ai/dsh-authorization`）的宿主形状。
//
// 补这一层之前的现场：本仓有**具体**的登录流（`internal/account/account.go` 的
// StartSignIn/CancelSignIn + `internal/account/oidc.go` 的 device/code 交换），
// 但没有官方那种**通用 authorization flow 注册表**：
//
//   - 非 account 的凭据流（第三方插件的 OAuth、一次性 code、账号选择）无处注册；
//   - 没有 "notice/prompt 只发给发起它的 surface" 的路由；
//   - 没有 `authorized` / `cancelled` 的**终态契约**（现在只有"登录成功/失败"）。
//
// 官方契约（逐条，出自 `@deepseek-ai/dsh-authorization/README.md`）：
//
//	"register a flow for each credential your plugin holds"    → registerFlow
//	"begin() reports { status: 'authorized' } when the record was
//	 committed and observed during the attempt, and { status: 'cancelled' }
//	 when the human declined or the caller withdrew"           → Begin
//	"cancel(key) withdraws the running attempt from a second call" → Cancel
//	"list() / describe(key) … whether an attempt is running"    → List / Describe
//
//	错误码：NO_FLOW / ALREADY_IN_FLIGHT / NOT_COMMITTED / UNKNOWN_METHOD
//
//	"Each attempt sends notices and prompts only to the surface that started it."
//	"There is no ambient provider to be absent, and no question about which of
//	 two open pages a prompt belongs to."
//
//	"the seam confirms a commit it observed during the attempt — presence alone
//	 would let a re-authorization pass a stale record off as fresh — and refuses
//	 a flow that resolved without one."
//
// 本文件的取舍（全部是"为什么形状改成这样"，没有一处降低契约）：
//
//  1. **surface 是请求带的 Interaction，不是注册表里的环境**。官方的 interaction
//     随请求走；本仓用一个接口 `Interaction` 表达（Notify/Ask），由发起方传入。
//     `Begin` 不收 surface 参数，而是收一个已经绑定好终点的 `Interaction` —— 这就是
//     "notice 只到发起方"在 Go 里的等价物：没有全局 provider 可被误用。
//  2. **commit 必须被观察到**。官方靠 `credentials/record-updated` 事件 +
//     describeRecord 回读。本仓没有那个事件面，所以用一个**显式的**提交证据：
//     flow 必须调 `session.Commit()`（一次，成功即"已写盘"），而 `Begin` 在 flow
//     返回后检查"这次尝试里真的 commit 过"。没 commit → `NOT_COMMITTED`。
//     这比官方更严（官方靠事件推断，这里要求显式声明），但**终态语义一致**：
//     `authorized` 一定意味着记录真的被写下。
//  3. **一次尝试一个 key**。`ALREADY_IN_FLIGHT`；`inFlight` 在 entry 上给 UI
//     提前禁按钮用（官方同义）。
//  4. **人的"不"是结局不是故障**。`Interaction.Ask` 返回 `DeclinedPromptError`
//     时尝试结算成 `cancelled`；其它错误是 flow 失败，返回给调用方。
//     这不放进本文件（那会让本文件依赖 fetch/ui），所以用 `errors.Is` 判。
//  5. **撤销后的孤儿**：调用方 withdraw 之后若 flow 仍然提交了记录，那是"人真的
//     授权过"的记录，保留（官方同义："a record it still manages to commit is a
//     record the human did authorize"）。但本次尝试的**终态**仍然是 cancelled。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// 官方错误码。它们是**稳定字符串**：客户端/UI 按 code 分支，不按中文猜。
const (
	ErrCodeNoFlow          = "NO_FLOW"
	ErrCodeAlreadyInFlight = "ALREADY_IN_FLIGHT"
	ErrCodeNotCommitted    = "NOT_COMMITTED"
	ErrCodeUnknownMethod   = "UNKNOWN_METHOD"
)

// Error 是一次 authorization 失败。
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("authorization/%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("authorization/%s: %s", e.Code, e.Message)
}

// Unwrap 让调用方用 errors.As 判 *Error，用 Cause 追根因。
func (e *Error) Unwrap() error { return e.Cause }

// CodeOf 取一次错误的官方 code；不是本包的错误时返回 ""。
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Method 是 flow 提供的一种获取凭据的方式（官方 AuthorizationMethod）。
//
// json tag 与官方 wire 逐字一致（`{id, label}`）：这条结构体同时是 RPC 载荷，
// 字段名不是内部实现细节。
type Method struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Notice 是 flow 单向告诉人"下一步做什么"（官方 AuthorizationNotice）。
//
// **绝不携带 secret**：官方原文 "A notice is one-way and never carries a secret"。
type Notice struct {
	Message string `json:"message"`
	// URL 是人必须打开的页面。
	URL string `json:"url,omitempty"`
	// Code 是人在那个页面上要输入的短码。
	Code string `json:"code,omitempty"`
}

// PromptOption 是 select 提示里的一个选项。
type PromptOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// PromptKind 是提示的种类。secret 与 text 只在呈现上不同（要遮蔽、不入日志）。
type PromptKind string

const (
	PromptText   PromptKind = "text"
	PromptSecret PromptKind = "secret"
	PromptSelect PromptKind = "select"
)

// Prompt 是一个 flow 自己回答不了的问题（官方 AuthorizationPrompt）。
type Prompt struct {
	Kind        PromptKind     `json:"kind"`
	Message     string         `json:"message"`
	Placeholder string         `json:"placeholder,omitempty"`
	Options     []PromptOption `json:"options,omitempty"`
}

// Interaction 是发起 authorization 的那个 surface。
//
// 官方："The interaction travels with the request, not a registry." 在 Go 里的
// 等价物就是一个由发起方实现并传进来的接口——没有全局 provider 可以被误用。
type Interaction interface {
	// Notify 单向告知。返回错误说明这个 surface 已经关了/写不进去。
	Notify(ctx context.Context, notice Notice) error
	// Ask 问一个问题；答案由 surface 渲染。
	// 人选择"不"时返回 errors.Is(err, ErrDeclined) 为真的错误。
	Ask(ctx context.Context, prompt Prompt) (string, error)
}

// ErrDeclined 是"人说不"（官方 AuthorizationDeclinedError 的宿主等价物）。
//
// 用 sentinel 而不是类型：surface 实现只需要 `return "", ErrDeclined`。
var ErrDeclined = errors.New("authorization: 人拒绝了这次授权")

// Surface 是一次尝试的会话（官方 AuthorizationSession）。
//
// flow 用它跟人对话，并在**写盘之后**调 Commit。Commit 是本次尝试的提交证据：
// 没有它，`Begin` 不会报 authorized。
type Surface struct {
	ctx    context.Context
	method Method
	key    string
	step   *attempt

	// aborted 在调用方撤销时关闭。
	aborted <-chan struct{}
}

// Context 返回本次尝试的上下文（撤销时被取消）。
func (s *Surface) Context() context.Context { return s.ctx }

// Method 是调用方选中的方式（没选时是 flow 的第一个）。
func (s *Surface) Method() Method { return s.method }

// Key 是这次尝试写的凭据记录 key（`<scope>/<id>`）。
func (s *Surface) Key() string { return s.key }

// Aborted 在调用方撤销时关闭；flow 应当及时停下。
func (s *Surface) Aborted() <-chan struct{} { return s.aborted }

// Notify 把一条通知发给**发起这次尝试的那个 surface**。
func (s *Surface) Notify(notice Notice) error {
	select {
	case <-s.aborted:
		return fmt.Errorf("authorization: 这次尝试已被撤销")
	default:
	}
	if s.step.interaction == nil {
		return &Error{Code: ErrCodeNoFlow, Message: "没有任何 interaction：这次尝试没有可以对话的 surface"}
	}
	return s.step.interaction.Notify(s.ctx, notice)
}

// Prompt 问一个人答得上的问题。
func (s *Surface) Prompt(prompt Prompt) (string, error) {
	select {
	case <-s.aborted:
		return "", fmt.Errorf("authorization: 这次尝试已被撤销")
	default:
	}
	if s.step.interaction == nil {
		return "", &Error{Code: ErrCodeNoFlow, Message: "没有任何 interaction：这次尝试没有可以对话的 surface"}
	}
	return s.step.interaction.Ask(s.ctx, prompt)
}

// Commit 声明"凭据已经写进真实存储"。
//
// 官方靠 `credentials/record-updated` 事件 + 回读确认；本仓没有那条事件，所以
// 提交必须是**显式**的：flow 在真的写完（ctx.credentials.modifyRecord 等价物）
// 之后调它。同一个 flow 调两次只认第一次（幂等），但**会**报出来——重复提交
// 通常说明 flow 里有两处写盘，那是个 bug，不该被静默吞掉。
func (s *Surface) Commit() error {
	if !s.step.markCommitted() {
		return fmt.Errorf("authorization: 同一个 flow 只能 Commit 一次（这次尝试已经有提交证据了）")
	}
	return nil
}

// Flow 是一条可注册的授权流（官方 registerFlow 的参数）。
type Flow struct {
	// Key 是这条 flow 写的凭据记录 key（`<scope>/<id>`）。**必填**。
	Key string
	// Label 是人看到的"正在授权什么"。**必填**。
	Label string
	// Methods 是它提供的方式，最优先的在前。**至少一种**。
	Methods []Method
	// Run 跟人对话并写盘。它返回 nil **不等于**成功：必须 Commit 过。
	Run func(ctx context.Context, session *Surface) error
}

// Entry 是一个已注册 flow 的对外投影（官方 AuthorizationEntry）。
type Entry struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Methods  []Method `json:"methods"`
	InFlight bool     `json:"inFlight"`
}

// Status 是一次成功尝试的终态（官方 AuthorizationStatus）。
type Status string

const (
	StatusAuthorized Status = "authorized"
	StatusCancelled  Status = "cancelled"
)

// Outcome 是 Begin 的结果。
type Outcome struct {
	Status Status `json:"status"`
}

// attempt 是一次在飞尝试。
type attempt struct {
	key string
	// interaction 是发起这次尝试的 surface（nil = 无头调用方）。
	interaction Interaction

	mu        sync.Mutex
	committed bool
	cancelled bool
	done      chan struct{}

	cancel context.CancelFunc
}

func (a *attempt) markCommitted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.committed {
		return false
	}
	a.committed = true
	return true
}

func (a *attempt) wasCommitted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.committed
}

// Registry 是 authorization flow 注册表（官方 ctx.authorization）。
//
// 并发安全。一次尝试一个 key。
type Registry struct {
	mu      sync.Mutex
	flows   map[string]*Flow
	order   []string // 注册顺序：list() 要稳定
	running map[string]*attempt
}

// NewRegistry 造一个空注册表。
func NewRegistry() *Registry {
	return &Registry{flows: map[string]*Flow{}, running: map[string]*attempt{}}
}

// credentialKeyShape 是 `<scope>/<id>`：scope 只允许字母数字与 `-`/`_`/`.`，
// id 同理。为什么严格：key 是**记录的主键**，宽松的名字会让"两条 flow 写同一
// 条记录"这类事故变得看不出来。
func credentialKeyShape(key string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
				r == '-' || r == '_' || r == '.'
			if !ok {
				return false
			}
		}
	}
	return true
}

// RegisterFlow 注册一条 flow，返回反注册函数。
//
// 反注册会**拒绝**（返回错误）而不是静默成功：一条还在跑的尝试如果失去 flow，
// 后面的人就说不清它到底授权了什么。想换 flow 就先 Cancel 再反注册。
func (r *Registry) RegisterFlow(flow Flow) (func() error, error) {
	key := strings.TrimSpace(flow.Key)
	if !credentialKeyShape(key) {
		return nil, &Error{Code: ErrCodeNoFlow, Message: fmt.Sprintf("凭据 key 必须是 <scope>/<id>（字母数字 - _ .），收到 %q", flow.Key)}
	}
	if strings.TrimSpace(flow.Label) == "" {
		return nil, &Error{Code: ErrCodeNoFlow, Message: "flow 缺 label（人要知道在授权什么）"}
	}
	if len(flow.Methods) == 0 {
		return nil, &Error{Code: ErrCodeNoFlow, Message: "flow 至少要提供一种 method"}
	}
	seen := map[string]struct{}{}
	for _, method := range flow.Methods {
		id := strings.TrimSpace(method.ID)
		if id == "" || strings.TrimSpace(method.Label) == "" {
			return nil, &Error{Code: ErrCodeNoFlow, Message: "method 的 id / label 都不能为空"}
		}
		if _, dup := seen[id]; dup {
			return nil, &Error{Code: ErrCodeNoFlow, Message: fmt.Sprintf("method %q 在同一个 flow 里重复", id)}
		}
		seen[id] = struct{}{}
	}
	if flow.Run == nil {
		return nil, &Error{Code: ErrCodeNoFlow, Message: "flow 没有 run（注册一个什么都不做的 flow 等于假装能授权）"}
	}

	r.mu.Lock()
	if _, exists := r.flows[key]; exists {
		r.mu.Unlock()
		return nil, &Error{Code: ErrCodeNoFlow, Message: fmt.Sprintf("凭据 %q 已经有 flow 了（一条记录只能有一个写者）", key)}
	}
	copied := flow
	copied.Methods = append([]Method(nil), flow.Methods...)
	r.flows[key] = &copied
	r.order = append(r.order, key)
	r.mu.Unlock()

	disposed := false
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if disposed {
			return nil // 幂等
		}
		if _, running := r.running[key]; running {
			return &Error{Code: ErrCodeAlreadyInFlight, Message: fmt.Sprintf("凭据 %q 还有一次授权在跑，先 cancel 再反注册", key)}
		}
		disposed = true
		delete(r.flows, key)
		for i, existing := range r.order {
			if existing == key {
				r.order = append(r.order[:i], r.order[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// List 列出全部已注册 flow（按注册顺序，稳定）。
func (r *Registry) List() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, len(r.order))
	for _, key := range r.order {
		flow, ok := r.flows[key]
		if !ok {
			continue
		}
		_, inFlight := r.running[key]
		out = append(out, Entry{
			Key:      key,
			Label:    flow.Label,
			Methods:  append([]Method(nil), flow.Methods...),
			InFlight: inFlight,
		})
	}
	return out
}

// Describe 查一条 flow；没有时 ok=false（不是错误）。
func (r *Registry) Describe(key string) (Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	flow, ok := r.flows[key]
	if !ok {
		return Entry{}, false
	}
	_, inFlight := r.running[key]
	return Entry{Key: key, Label: flow.Label, Methods: append([]Method(nil), flow.Methods...), InFlight: inFlight}, true
}

// Keys 是已注册 flow 的 key（排序后，供诊断打印）。
func (r *Registry) Keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.order...)
	sort.Strings(out)
	return out
}

// Begin 跑一次授权尝试（官方 begin）。
//
// interaction 是**发起这次尝试的 surface**：所有 notice/prompt 只到它。
// 传 nil = 无头调用方：notice 被丢弃，任何 prompt 立刻失败——与官方
// "a headless caller supplies an interaction that declines" 同义（本仓把
// "decline" 表达成 nil，因为 Get 不到任何 surface 的时候连"拒绝"都没人收）。
//
// 返回值：
//   - 成功授权 → Outcome{authorized}，nil
//   - 人拒绝 / 调用方撤销 → Outcome{cancelled}，nil
//   - flow 失败 → Outcome{}, err（**不是** outcome：失败要传到调用方）
func (r *Registry) Begin(ctx context.Context, key, method string, interaction Interaction) (Outcome, error) {
	key = strings.TrimSpace(key)
	r.mu.Lock()
	flow, ok := r.flows[key]
	if !ok {
		r.mu.Unlock()
		return Outcome{}, &Error{Code: ErrCodeNoFlow, Message: fmt.Sprintf("没有 flow 认领凭据 %q：先 registerFlow，或确认插件还在", key)}
	}
	if _, busy := r.running[key]; busy {
		r.mu.Unlock()
		return Outcome{}, &Error{Code: ErrCodeAlreadyInFlight, Message: fmt.Sprintf("凭据 %q 已经有一次授权在跑", key)}
	}
	chosen, methodErr := pickMethod(flow, method)
	if methodErr != nil {
		r.mu.Unlock()
		return Outcome{}, methodErr
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	step := &attempt{key: key, done: make(chan struct{}), cancel: cancel}
	step.interaction = interaction
	r.running[key] = step
	r.mu.Unlock()

	// 释放 key 必须在结算**之前**：官方 "The key is released before
	// authorization/settled fires, so a listener that reacts by starting the
	// next attempt is not refused"。本仓没有事件总线，所以直接在 defer 里
	// 先放 key 再关 done。
	defer func() {
		r.mu.Lock()
		delete(r.running, key)
		r.mu.Unlock()
		cancel()
		close(step.done)
	}()

	session := &Surface{
		ctx:     attemptCtx,
		method:  chosen,
		key:     key,
		step:    step,
		aborted: attemptCtx.Done(),
	}
	runErr := flow.Run(attemptCtx, session)

	// 撤销优先于一切：官方 "A withdrawn attempt settles immediately even when
	// the flow never reacts to its signal."
	if ctx.Err() != nil || attemptCtx.Err() != nil {
		return Outcome{Status: StatusCancelled}, nil
	}
	if runErr != nil {
		// 人的"不"是结局，不是故障。
		if errors.Is(runErr, ErrDeclined) {
			return Outcome{Status: StatusCancelled}, nil
		}
		return Outcome{}, runErr
	}
	// flow 返回 nil 但没提交 → 拒绝。这是官方 NOT_COMMITTED 的宿主判据。
	if !step.wasCommitted() {
		return Outcome{}, &Error{Code: ErrCodeNotCommitted,
			Message: "flow 结束了却没有 Commit：authorized 必须意味着记录真的写下了"}
	}
	return Outcome{Status: StatusAuthorized}, nil
}

// Cancel 从**第二次调用**撤销一次在跑的尝试（官方 cancel(key)）。
//
// 为什么需要它：HTTP 的 request/response 传输要回答一个 Cancel 按钮，而按钮的
// 那个请求拿不到第一次调用的 signal。
func (r *Registry) Cancel(key string) bool {
	key = strings.TrimSpace(key)
	r.mu.Lock()
	step, ok := r.running[key]
	r.mu.Unlock()
	if !ok {
		return false
	}
	step.cancel()
	return true
}

// InFlight 报告这个 key 现在有没有尝试在跑。
func (r *Registry) InFlight(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[strings.TrimSpace(key)]
	return ok
}

// pickMethod 按官方规则选方式：点名未提供 → UNKNOWN_METHOD；没点名 → 第一个。
func pickMethod(flow *Flow, method string) (Method, error) {
	method = strings.TrimSpace(method)
	if method == "" {
		return flow.Methods[0], nil
	}
	for _, candidate := range flow.Methods {
		if candidate.ID == method {
			return candidate, nil
		}
	}
	ids := make([]string, 0, len(flow.Methods))
	for _, candidate := range flow.Methods {
		ids = append(ids, candidate.ID)
	}
	return Method{}, &Error{Code: ErrCodeUnknownMethod,
		Message: fmt.Sprintf("flow %q 没有 method %q（可选：%s）", flow.Key, method, strings.Join(ids, ", "))}
}
