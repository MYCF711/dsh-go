package main

// PARITY-W2-STATE：`forgePanel.{track,dismiss,refresh,follow}` 的宿主实现。
//
// # 数据源（一份账本）
//
// 全部读写 `internal/forge` 的绑定存储：`<home>/state/forge-sessions.json`。
// 没有第二个账本，也没有内存镜像——每次快照都从存储读，所以"另一个进程改了"
// 与"本进程改了"看到的完全一样。
//
// # 版本号与唤醒
//
// follow 要真流：本宿主用"写操作后递增版本号 + 等版本变化"唤醒帧机。
// 没有变化时帧机阻塞在 ctx 上（不空转轮询）；有变化立刻出帧。
// 版本号是**进程内**单调计数（跨进程靠帧机每轮重新读存储兜底，见 Snapshot
// 的实现：Version 取"存储内容 hash 的单调投影"不现实，因此这里用
// `storeFingerprint` 把绑定集合算成一个稳定值，进程间也能感知变化）。
//
// # Provider
//
// 生产：`DSHD_FORGE_PROVIDER_TOKEN` 存在时用 GitHub 真实拉取面（HTTPProvider）；
// 不存在时 **不静默回旧值**，refresh 直接为结构化 `ErrForgeNoProvider`。
// 测试：注入 httptest provider（同一接口，真实 HTTP）。

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"dshgo/internal/forge"
	"dshgo/internal/transport"
	"dshgo/internal/worktree"
)

// forgePanelHost 把存储装成 Transport 认的那份函数表。
type forgePanelHost struct {
	store *forge.Store

	// worktrees 是 worktree 生命周期服务（可能与 worktree 面板共用同一实例）。
	// **可以为 nil**：那说明宿主没挂 worktrees 服务，面板状态里
	// `worktree.editable` 就是 false（官方定义的第一条前提），其余 worktree
	// 子对象全为 null —— 不编造"能建 worktree"。
	worktrees *worktree.Service
	// cwd 解析会话的有效工作目录（官方 state.directory）。
	cwd func(sessionID string) (string, error)
	// git 是只读的 git 探针（checkout 的 root/branch/commit 三项事实）。
	git worktree.ExecGit

	// states 是面板状态缓存：follow 每帧都要一次完整状态，而一次状态要读整个
	// 会话 v4 日志。没有缓存时一个打开的 panel 会每秒重读几十 MB。
	states map[string]cachedPanelState
	// checkouts 是 checkout 探针缓存（root/branch/commit 变化是秒级的事）。
	checkouts map[string]cachedCheckout
	// ttl 是面板状态缓存的保鲜期（默认 forgePanelTTL；测试设 0 关掉缓存）。
	ttl time.Duration

	mu      sync.Mutex
	waiters map[chan struct{}]struct{}
}

// cachedPanelState 是一份带时间戳的面板状态快照。
type cachedPanelState struct {
	state transport.OfficialForgePanelState
	at    time.Time
}

// cachedCheckout 是一次 checkout 探针结果。
type cachedCheckout struct {
	facts checkoutFacts
	at    time.Time
}

// forgePanelTTL / forgePanelWaitSlice 是 follow 的节流参数。
//
// 为什么要有它们：官方流允许跳过中间态（"Intermediate states may be skipped when
// the reader is slower than the updates"），所以 1s 级的新鲜度是契约内的；而
// 每次状态要读一次 v4 日志 + 一次 git，100ms 级轮询会把盘读爆。
const (
	forgePanelTTL       = 750 * time.Millisecond
	forgePanelWaitSlice = 500 * time.Millisecond
)

// newForgePanelHost 造一个宿主。home 为空时返回 nil（调用方据此不接线）。
//
// worktrees / resolveCwd 都可以为 nil：为 nil 时面板状态里那部分就是官方的
// null / false，而不是假数据。生产接线在 serve.go 传入同一份 worktree 服务与
// sessionSource.Cwd（与 forgePanel git 族、worktree 面板同源）。
func newForgePanelHost(home string, worktrees *worktree.Service, resolveCwd func(string) (string, error)) (*forgePanelHost, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil, fmt.Errorf("forge: home 为空，不能开绑定存储")
	}
	store, err := forge.Open(home)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(os.Getenv("DSHD_FORGE_PROVIDER_TOKEN")); token != "" {
		store.SetProvider(&forge.HTTPProvider{Token: token})
	}
	return &forgePanelHost{
		store:     store,
		worktrees: worktrees,
		cwd:       resolveCwd,
		git:       worktree.ExecGit{Timeout: 10 * time.Second},
		states:    map[string]cachedPanelState{},
		checkouts: map[string]cachedCheckout{},
		ttl:       forgePanelTTL,
		waiters:   map[chan struct{}]struct{}{},
	}, nil
}

// api 把宿主动词装成 transport 的函数表。
func (h *forgePanelHost) api() transport.OfficialForgePanelAPI {
	return transport.OfficialForgePanelAPI{
		Track:   h.track,
		Dismiss: h.dismiss,
		Refresh: h.refresh,
		State:   h.state,
		Wait:    h.wait,
	}
}

func (h *forgePanelHost) track(sessionID, rawURL string) (transport.OfficialForgePanelBinding, error) {
	b, err := h.store.Track(sessionID, rawURL, "github")
	if err != nil {
		return transport.OfficialForgePanelBinding{}, forgePanelFault(err)
	}
	h.bump()
	return forgePanelBinding(b), nil
}

func (h *forgePanelHost) dismiss(sessionID, rawURL string) (transport.OfficialForgePanelBinding, error) {
	b, err := h.store.Dismiss(sessionID, rawURL)
	if err != nil {
		return transport.OfficialForgePanelBinding{}, forgePanelFault(err)
	}
	h.bump()
	return forgePanelBinding(b), nil
}

func (h *forgePanelHost) refresh(ctx context.Context, sessionID, rawURL string) ([]transport.OfficialForgePanelBinding, error) {
	// url 缺省：刷新该会话的**全部**绑定（官方 refresh 语义）。
	if strings.TrimSpace(rawURL) == "" {
		list, err := h.store.List()
		if err != nil {
			return nil, forgePanelFault(err)
		}
		out := make([]transport.OfficialForgePanelBinding, 0, len(list))
		var firstErr error
		for _, b := range list {
			if b.SessionID != sessionID {
				continue
			}
			refreshed, rerr := h.store.Refresh(ctx, b.SessionID, b.URL)
			if rerr != nil {
				if firstErr == nil {
					firstErr = rerr
				}
				continue
			}
			out = append(out, forgePanelBinding(refreshed))
		}
		if len(out) == 0 {
			if firstErr != nil {
				return nil, forgePanelFault(firstErr)
			}
			return nil, fmt.Errorf("%w：session=%q 没有可刷新的绑定", transport.ErrForgeBindingNotFound, sessionID)
		}
		h.bump()
		return out, nil
	}
	b, err := h.store.Refresh(ctx, sessionID, rawURL)
	if err != nil {
		return nil, forgePanelFault(err)
	}
	h.bump()
	return []transport.OfficialForgePanelBinding{forgePanelBinding(b)}, nil
}

// state 返回一个会话的**官方 ForgePanelState**（follow 的每一帧就是它）。
//
// 这是 `worktree.editable` 与 `worktree.creation` 的唯一读出口：
//
//	editable = 挂了 worktree 服务 ∧ 没开过 turn ∧ 还没创建过 worktree
//	creation = worktree.Service 里**此刻**正在跑的那次创建（prepare/checkout）
//
// 两个值都来自真实状态机：editable 读会话日志折叠出的 TurnStarted/Created，
// creation 读进程内的活进度。改底层状态（开一轮 / 开始创建 / 创建结束），
// 这里立刻跟着变 —— 不是常量。
//
// 其余官方字段：有真值的给真值（directory / checkout / changeRequests /
// worktree 的 request·created·failure·missing·restoration·waiting），
// 没有真值的给官方允许的 null（repository / readiness / local：
// 宿主没有 forge registry、登录探测与本地 diff 巡检；见
// docs/host-stable-gap-register.md 的如实登记）。
func (h *forgePanelHost) state(ctx context.Context, sessionID string) (transport.OfficialForgePanelState, error) {
	if strings.TrimSpace(sessionID) == "" {
		return transport.OfficialForgePanelState{}, fmt.Errorf("%w：sessionId 是空串", transport.ErrForgeBadParam)
	}
	if cached, ok := h.cachedState(sessionID); ok {
		return cached, nil
	}

	directory := ""
	if h.cwd != nil {
		resolved, err := h.cwd(sessionID)
		if err != nil {
			return transport.OfficialForgePanelState{}, err
		}
		directory = strings.TrimSpace(resolved)
	}

	state := transport.OfficialForgePanelState{
		Directory:      directory,
		Checkout:       transport.OfficialForgeCheckoutNone(),
		ChangeRequests: h.changeRequests(sessionID),
		Worktree:       h.worktreeState(sessionID, directory),
	}
	if directory != "" {
		if checkout, ok := h.checkoutState(ctx, directory, state.Worktree.Created); ok {
			state.Checkout = checkout
		}
	}

	h.storeState(sessionID, state)
	return state, nil
}

// worktreeState 组装官方 `ForgePanelWorktree` 九个子字段。
func (h *forgePanelHost) worktreeState(sessionID, directory string) transport.OfficialForgePanelWorktree {
	out := transport.OfficialForgePanelWorktree{}
	if h.worktrees == nil {
		// 没有 worktree 服务：官方 editable 的第一条前提就不成立。
		return out
	}
	snapshot, err := h.worktrees.Snapshot(sessionID)
	if err != nil {
		// 折叠失败（会话日志读不动）按"这一刻没有可发布的状态"处理：
		// 全部 null/false，而不是拿上一次的值冒充当前值。
		return out
	}
	out.Editable = !snapshot.TurnStarted && snapshot.Created == nil

	if snapshot.Requested && !snapshot.Withdrawn && snapshot.Created == nil {
		out.Request = &transport.OfficialForgeWorktreeRequest{From: forgeAbsoluteBase(snapshot.Base)}
	}
	if snapshot.Created != nil {
		out.Created = &transport.OfficialForgeSessionWorktree{
			Path:           snapshot.Created.Directory,
			Branch:         snapshot.Created.Branch,
			From:           forgeAbsoluteBase(snapshot.Created.Base),
			BaseCommit:     snapshot.Created.BaseCommit,
			RepositoryRoot: snapshot.Created.SourceRoot,
		}
	}
	if snapshot.Failure != nil {
		// 官方 failure.phase 只有 prepare / checkout 两个词；"restoring" 之类的
		// 步骤在官方词表里没有对应项，就不发布 failure（宁缺不编）。
		if phase, ok := forgeWorktreePhase(snapshot.Failure.Step); ok {
			out.Failure = &transport.OfficialForgeWorktreeFailure{
				Branch:  forgeNullable(snapshot.Failure.Branch),
				From:    forgeAbsoluteBase(snapshot.Failure.Base),
				Phase:   phase,
				Message: snapshot.Failure.Reason,
			}
		}
	}
	if progress, ok := h.worktrees.Progress(sessionID); ok {
		switch progress.Kind {
		case worktree.ProgressKindCreation:
			out.Creation = &transport.OfficialForgeWorktreeCreation{
				Phase:  progress.Phase,
				Branch: progress.Branch,
				From:   progress.From,
				Held:   progress.Held,
			}
		case worktree.ProgressKindRestoration:
			out.Restoration = &transport.OfficialForgeWorktreeRestoration{Phase: progress.Phase}
		}
		if progress.Held {
			switch progress.Kind {
			case worktree.ProgressKindCreation:
				out.Waiting = forgeStringPtr("creation")
			case worktree.ProgressKindRestoration:
				out.Waiting = forgeStringPtr("restoration")
			}
		}
	}
	// missing：会话正工作在自己创建的 checkout 里，而那个目录已经不在了。
	if out.Created != nil && directory != "" && sameForgePath(directory, out.Created.Path) {
		if _, statErr := os.Lstat(out.Created.Path); statErr != nil && os.IsNotExist(statErr) {
			// branchExists 留给 null：官方允许"不完整的恢复无法探查分支"这种情形，
			// 这里没有做分支探查，就不假装知道。
			out.Missing = &transport.OfficialForgeMissingWorktree{}
		}
	}
	return out
}

// changeRequests 把该会话可见的绑定翻成官方 `ForgePanelChangeRequest`。
//
// `update`/`snapshot` 是 null：宿主只在 refresh 时读过远端，没有随绑定保存
// 一份快照。官方这两个字段都可为 null，不给假 PR 状态。
func (h *forgePanelHost) changeRequests(sessionID string) []transport.OfficialForgePanelChangeRequest {
	out := []transport.OfficialForgePanelChangeRequest{}
	if h.store == nil {
		return out
	}
	list, err := h.store.List()
	if err != nil {
		return out
	}
	for _, binding := range list {
		if binding.SessionID != sessionID {
			continue
		}
		if !binding.DismissedAt.IsZero() {
			// 官方 changeRequests 只含可见绑定；dismiss 过的不出现。
			continue
		}
		out = append(out, transport.OfficialForgePanelChangeRequest{
			URL:    binding.URL,
			Origin: "tracked",
			Ref:    forgeChangeRequestRef(binding.URL),
		})
	}
	return out
}

// checkoutState 用一次真实 git 探针填官方 `ForgeCheckoutState`。
//
// ok=false 表示"这个目录不是 checkout"之外的第二种情况：拿不到事实。
// 拿不到就保持 `{state:'none'}`？不 —— 那会是假话。这里把探针失败也当
// "不是 checkout"只因为 `git rev-parse --show-toplevel` 只有在不是仓库时才
// 失败；其它错误（git 没装）同样落这里，但那种情况下宿主本来也建不了 worktree。
func (h *forgePanelHost) checkoutState(ctx context.Context, directory string, created *transport.OfficialForgeSessionWorktree) (transport.OfficialForgeCheckoutState, bool) {
	facts := h.checkoutFacts(ctx, directory)
	if !facts.isCheckout {
		return transport.OfficialForgeCheckoutState{}, false
	}
	linked := false
	if created != nil && sameForgePath(directory, created.Path) {
		linked = true
	}
	return transport.OfficialForgeCheckoutState{
		State: "ready",
		Checkout: &transport.OfficialForgeCheckout{
			Root:   facts.root,
			Branch: forgeNullable(facts.branch),
			Linked: linked,
			Commit: forgeNullable(facts.commit),
		},
	}, true
}

// checkoutFacts 是官方 ForgeCheckout 在本地能确认的三项事实。
type checkoutFacts struct {
	isCheckout bool
	root       string
	branch     string
	commit     string
}

// checkoutFacts 读一次 git（带 3s 缓存）：面板每帧都要它，而 git 是子进程。
func (h *forgePanelHost) checkoutFacts(ctx context.Context, directory string) checkoutFacts {
	h.mu.Lock()
	if cached, ok := h.checkouts[directory]; ok && time.Since(cached.at) < 3*time.Second {
		h.mu.Unlock()
		return cached.facts
	}
	h.mu.Unlock()

	var facts checkoutFacts
	if root, err := h.git.Run(ctx, directory, "rev-parse", "--show-toplevel"); err == nil {
		facts.isCheckout = true
		facts.root = strings.TrimSpace(root)
		if branch, berr := h.git.Run(ctx, directory, "symbolic-ref", "--short", "HEAD"); berr == nil {
			facts.branch = strings.TrimSpace(branch)
		}
		if commit, cerr := h.git.Run(ctx, directory, "rev-parse", "HEAD"); cerr == nil {
			facts.commit = strings.TrimSpace(commit)
		}
	}

	h.mu.Lock()
	h.checkouts[directory] = cachedCheckout{facts: facts, at: time.Now()}
	h.mu.Unlock()
	return facts
}

// cachedState 读面板状态缓存（follow 每帧一次，避免重复折叠会话日志）。
func (h *forgePanelHost) cachedState(sessionID string) (transport.OfficialForgePanelState, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cached, ok := h.states[sessionID]
	if !ok || time.Since(cached.at) >= h.ttl {
		return transport.OfficialForgePanelState{}, false
	}
	return cached.state, true
}

func (h *forgePanelHost) storeState(sessionID string, state transport.OfficialForgePanelState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.states[sessionID] = cachedPanelState{state: state, at: time.Now()}
}

// wait 阻塞到"可能有新状态"：绑定写操作、或一个 500ms 的时间片。
//
// 为什么留时间片：turn/start（editable 翻转）、working-directory/change 这些
// 都不经过本宿主，没有跨进程通知；官方流允许跳中间态，所以 500ms 的兜底
// 重读是契约内行为，不是假流。
func (h *forgePanelHost) wait(ctx context.Context, sessionID string) error {
	_ = sessionID // 唤醒是全局的（同一份账本）；按会话分桶只会更晚唤醒。
	h.mu.Lock()
	ch := make(chan struct{})
	h.waiters[ch] = struct{}{}
	h.mu.Unlock()

	select {
	case <-ctx.Done():
		h.mu.Lock()
		delete(h.waiters, ch)
		h.mu.Unlock()
		return ctx.Err()
	case <-time.After(forgePanelWaitSlice):
		h.mu.Lock()
		delete(h.waiters, ch)
		h.mu.Unlock()
		return nil
	case <-ch:
		return nil
	}
}

// bump 递增版本号并唤醒所有等待者（写操作后调用）。
func (h *forgePanelHost) bump() {
	h.mu.Lock()
	// 绑定变了：面板状态里的 changeRequests 变了，缓存立刻作废。
	h.states = map[string]cachedPanelState{}
	waiters := make([]chan struct{}, 0, len(h.waiters))
	for ch := range h.waiters {
		waiters = append(waiters, ch)
	}
	h.waiters = map[chan struct{}]struct{}{}
	h.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// forgePanelBinding 把领域绑定翻成 wire 形状（时间统一 RFC3339Nano）。
func forgePanelBinding(b forge.Binding) transport.OfficialForgePanelBinding {
	out := transport.OfficialForgePanelBinding{
		SessionID: b.SessionID,
		URL:       b.URL,
		Provider:  b.Provider,
		Tracked:   b.Tracked,
		State:     b.State,
		Title:     b.Title,
		HeadSHA:   b.HeadSHA,
	}
	if !b.TrackedAt.IsZero() {
		out.TrackedAt = b.TrackedAt.UTC().Format(time.RFC3339Nano)
	}
	if !b.DismissedAt.IsZero() {
		out.DismissedAt = b.DismissedAt.UTC().Format(time.RFC3339Nano)
	}
	if !b.RefreshedAt.IsZero() {
		out.RefreshedAt = b.RefreshedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// forgePanelFault 把领域错误翻成 Transport 的哨兵（四字段由 Transport 出）。
func forgePanelFault(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, forge.ErrNotFound):
		return fmt.Errorf("%w：%v", transport.ErrForgeBindingNotFound, err)
	case errors.Is(err, forge.ErrInvalidURL):
		return fmt.Errorf("%w：%v", transport.ErrForgeInvalidURL, err)
	case errors.Is(err, forge.ErrNoProvider):
		return fmt.Errorf("%w：%v", transport.ErrForgeNoProvider, err)
	}
	return err
}

// ---------------------------------------------------------------------------
// 官方 ForgePanelState 的取值助手
// ---------------------------------------------------------------------------

// forgeNullable 把内部空串翻成官方可空字符串（空 = null）。
func forgeNullable(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	v := s
	return &v
}

// forgeAbsoluteBase 是 worktree 记录的 base 在官方 `from` 位的取法。
//
// **必须**走 worktree.OfficialBase：内部把"HEAD 起"表示成空串**或**字面量
// "HEAD" 两种，官方只有 null 一种。这里若自己判空串，会把 "HEAD" 当成分支名
// 发出去（面板上会显示一个叫 HEAD 的 base 分支）。
func forgeAbsoluteBase(base string) *string { return worktree.OfficialBase(base) }

// forgeStringPtr 造一个非 nil 字符串指针（官方有值枚举位用）。
func forgeStringPtr(s string) *string {
	v := s
	return &v
}

// forgeWorktreePhase 把内部步骤名翻成官方 `ForgeWorktreeCreation.phase`。
//
// 官方只有两个词（typert.host.js:1974）：prepare = 校验并建分支，checkout = 写
// 文件。内部步骤名更细（creating-branch / checking-out-files / preparing-worktree
// / restoring）；前两个能一一对上，restoring 在官方词表里没有对应项，返回 ok=false
// 让调用方宁缺不编。
func forgeWorktreePhase(step string) (string, bool) {
	switch step {
	case "creating-branch", "preparing-worktree":
		return worktree.ProgressPhasePrepare, true
	case "checking-out-files":
		return worktree.ProgressPhaseCheckout, true
	}
	return "", false
}

// forgeChangeRequestRef 从 change request URL 解析官方 `ChangeRequestRef`。
//
// 解析不出来就返回 nil：官方 ref 可空（"null" = 这条绑定还不知道属于哪个仓库
// 的哪个编号），比编一个错的好。
func forgeChangeRequestRef(raw string) *transport.OfficialForgePanelChangeRequestRef {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return nil
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return nil
	}
	host := strings.ToLower(parsed.Host)
	forgeName := "github"
	if host != "github.com" && !strings.HasSuffix(host, ".github.com") && !strings.Contains(host, "gitee") {
		// 不认识的 host：仍给出 ref（仓库身份是真的），forge 名按 host 原样，
		// 不冒充 github。
		forgeName = host
	}
	return &transport.OfficialForgePanelChangeRequestRef{
		Repository: transport.OfficialForgeRepository{
			Forge: forgeName,
			Host:  host,
			Owner: parts[0],
			Name:  parts[1],
		},
		Number: number,
	}
}

// sameForgePath 报告两个目录是不是同一个（Windows 大小写/分隔符）。
func sameForgePath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	na := strings.ToLower(strings.ReplaceAll(strings.TrimRight(a, `\/`), "/", `\`))
	nb := strings.ToLower(strings.ReplaceAll(strings.TrimRight(b, `\/`), "/", `\`))
	return na == nb
}
