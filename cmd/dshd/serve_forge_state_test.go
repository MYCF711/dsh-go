package main

// forgePanel 宿主侧探针：绑定必须真落盘（重开宿主仍在）、refresh 真走
// provider（httptest 真 HTTP）；面板状态里的 `worktree.editable` 与
// `worktree.creation` 必须**跟着真实状态机变**（不是常量）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/forge"
	"dshgo/internal/worktree"
)

func newForgeHostForTest(t *testing.T, home string) *forgePanelHost {
	t.Helper()
	h, err := newForgePanelHost(home, forgePanelOptions{})
	if err != nil {
		t.Fatalf("newForgePanelHost: %v", err)
	}
	if h == nil {
		t.Fatal("home 非空时不应返回 nil")
	}
	// 关掉缓存：这些测试就是要看"底层一变，下一次读就变"。
	h.ttl = 0
	return h
}

func TestForgePanelHostPersistsAcrossReopen(t *testing.T) {
	home := t.TempDir()
	h := newForgeHostForTest(t, home)

	b, err := h.track("sess-1", "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	if !b.Tracked || b.SessionID != "sess-1" {
		t.Fatalf("track 返回不对：%+v", b)
	}
	if b.TrackedAt == "" {
		t.Fatal("track 必须给 TrackedAt")
	}

	// 模拟重启：同一 home 重新构造宿主，面板状态里的 changeRequests 仍在。
	h2 := newForgeHostForTest(t, home)
	state, err := h2.state(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.ChangeRequests) != 1 {
		t.Fatalf("重启后应有 1 条 change request，得到 %d", len(state.ChangeRequests))
	}
	ref := state.ChangeRequests[0]
	if ref.URL != "https://github.com/o/r/pull/7" || ref.Origin != "tracked" {
		t.Fatalf("重启后 change request 不对：%+v", ref)
	}
	if ref.Ref == nil || ref.Ref.Repository.Owner != "o" || ref.Ref.Repository.Name != "r" || ref.Ref.Number != 7 {
		t.Fatalf("change request 的 ref 必须从 URL 真解析出来：%+v", ref.Ref)
	}

	// dismiss 也必须落盘，且从可见列表里消失。
	if _, err := h2.dismiss("sess-1", "https://github.com/o/r/pull/7"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	h3 := newForgeHostForTest(t, home)
	state3, err := h3.state(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("state 3: %v", err)
	}
	if len(state3.ChangeRequests) != 0 {
		t.Fatalf("dismiss 后面板不该再列出它：%+v", state3.ChangeRequests)
	}
	if list, _ := h3.store.List(); len(list) != 1 || list[0].Tracked || list[0].DismissedAt.IsZero() {
		t.Fatalf("dismiss 没落盘：%+v", list)
	}
}

func TestForgePanelHostRefreshUsesRealProvider(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"open","headSha":"deadbeef","title":"T"}`))
	}))
	defer srv.Close()

	home := t.TempDir()
	h := newForgeHostForTest(t, home)
	// 生产路径用环境变量注入 provider；这里直接验证 store 注入面（同一 store）。
	if _, err := h.track("sess-1", srv.URL+"/o/r/pull/7"); err != nil {
		t.Fatalf("track: %v", err)
	}
	// 注入真实 HTTP provider（打 httptest 服务，不是 stub 返回值）。
	h.store.SetProvider(&forge.HTTPProvider{Client: &http.Client{Timeout: 10 * time.Second}})

	bindings, err := h.refresh(context.Background(), "sess-1", srv.URL+"/o/r/pull/7")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if hits != 1 {
		t.Fatalf("provider 应被请求 1 次，得到 %d", hits)
	}
	if len(bindings) != 1 || bindings[0].State != "open" || bindings[0].HeadSHA != "deadbeef" {
		t.Fatalf("refresh 未采用真实结果：%+v", bindings)
	}
	if bindings[0].RefreshedAt == "" {
		t.Fatal("refresh 必须记 RefreshedAt")
	}
}

func TestForgePanelHostRefreshWithoutProviderIsStructured(t *testing.T) {
	home := t.TempDir()
	h := newForgeHostForTest(t, home)
	if _, err := h.track("sess-1", "https://github.com/o/r/pull/7"); err != nil {
		t.Fatalf("track: %v", err)
	}
	_, err := h.refresh(context.Background(), "sess-1", "https://github.com/o/r/pull/7")
	if err == nil {
		t.Fatal("没有 provider 时 refresh 必须失败（不能静默回旧值）")
	}
}

func TestForgePanelHostWaitWakesOnBump(t *testing.T) {
	home := t.TempDir()
	h := newForgeHostForTest(t, home)

	done := make(chan error, 1)
	go func() { done <- h.wait(context.Background(), "sess-1") }()

	time.Sleep(50 * time.Millisecond)
	if _, err := h.track("sess-1", "https://github.com/o/r/pull/7"); err != nil {
		t.Fatalf("track: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait 返回错误：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("写操作没有唤醒 wait（follow 会卡住）")
	}
}

// ---------------------------------------------------------------------------
// worktree.editable / worktree.creation 的变异证明
// ---------------------------------------------------------------------------

// panelMemStore 是 worktree.Store 的内存实现（事件就是 v4 事件）。
type panelMemStore struct {
	mu     sync.Mutex
	cwd    string
	events []v4.Event
}

func (s *panelMemStore) append(eventType string, data any) {
	raw, _ := json.Marshal(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, v4.Event{
		Type: eventType, Seq: int64(len(s.events)), Time: time.Now().UnixMilli(), Data: raw,
	})
}

func (s *panelMemStore) Load(string) (worktree.Snapshot, error) {
	s.mu.Lock()
	events := append([]v4.Event(nil), s.events...)
	cwd := s.cwd
	s.mu.Unlock()
	return worktree.Fold(cwd, events), nil
}

func (s *panelMemStore) AppendWorktree(_ string, record worktree.Record) error {
	s.append(worktree.EventType, record)
	return nil
}

func (s *panelMemStore) SetWorkingDirectory(_ string, dir string) error {
	s.append("working-directory/change", map[string]string{"cwd": dir})
	return nil
}

// panelGatedGit 在指定步骤上卡住，让测试能在"创建进行中"这个瞬间读面板状态。
type panelGatedGit struct {
	addGate    chan struct{}
	addBegun   chan struct{}
	resetGate  chan struct{}
	resetBegun chan struct{}
	onceAdd    sync.Once
	onceReset  sync.Once
}

func (g *panelGatedGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "worktree" {
		g.onceAdd.Do(func() { close(g.addBegun) })
		select {
		case <-g.addGate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "", nil
	}
	if len(args) > 0 && args[0] == "reset" {
		g.onceReset.Do(func() { close(g.resetBegun) })
		select {
		case <-g.resetGate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "", nil
	}
	if len(args) > 0 && args[0] == "rev-parse" {
		return "deadbeef\n", nil
	}
	return "", nil
}

func TestForgePanelWorktreeCardsFollowRealState(t *testing.T) {
	store := &panelMemStore{cwd: t.TempDir()}
	git := &panelGatedGit{
		addGate:    make(chan struct{}),
		addBegun:   make(chan struct{}),
		resetGate:  make(chan struct{}),
		resetBegun: make(chan struct{}),
	}
	svc := worktree.NewService(store, git)
	h, err := newForgePanelHost(t.TempDir(), forgePanelOptions{
		Worktrees: svc,
		Cwd:       func(string) (string, error) { return store.cwd, nil },
	})
	if err != nil {
		t.Fatalf("newForgePanelHost: %v", err)
	}
	h.ttl = 0
	ctx := context.Background()

	// 1) 干净会话：可以请求 worktree，创建进度为 null。
	state, err := h.state(ctx, "s-1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !state.Worktree.Editable {
		t.Fatal("没开过 turn、没创建过 worktree 时 editable 必须是 true")
	}
	if state.Worktree.Creation != nil || state.Worktree.Created != nil {
		t.Fatalf("干净会话不该有 creation/created：%+v", state.Worktree)
	}

	// 2) turn 开跑：editable 必须翻成 false（真值来自会话日志里的 turn/start）。
	store.append("turn/start", map[string]any{})
	state, err = h.state(ctx, "s-1")
	if err != nil {
		t.Fatalf("state after turn: %v", err)
	}
	if state.Worktree.Editable {
		t.Fatalf("turn/start 之后 editable 必须是 false：%+v", state.Worktree)
	}

	// 3) 开始创建：creation 必须出现（phase=prepare），held 跟着 inbox hold 变。
	freshStore := &panelMemStore{cwd: t.TempDir()}
	freshGit := &panelGatedGit{
		addGate:    make(chan struct{}),
		addBegun:   make(chan struct{}),
		resetGate:  make(chan struct{}),
		resetBegun: make(chan struct{}),
	}
	freshSvc := worktree.NewService(freshStore, freshGit)
	fresh, err := newForgePanelHost(t.TempDir(), forgePanelOptions{
		Worktrees: freshSvc,
		Cwd:       func(string) (string, error) { return freshStore.cwd, nil },
	})
	if err != nil {
		t.Fatalf("newForgePanelHost: %v", err)
	}
	fresh.ttl = 0

	if err := freshSvc.RequestWorktree("s-2", nil); err != nil {
		t.Fatalf("RequestWorktree: %v", err)
	}
	holdCtx, cancelHold := context.WithCancel(ctx)
	defer cancelHold()
	go func() { _ = freshSvc.Wait(holdCtx, "s-2") }()

	createDone := make(chan error, 1)
	go func() { createDone <- freshSvc.PreStep(ctx, "s-2", 1) }()

	select {
	case <-freshGit.addBegun:
	case <-time.After(5 * time.Second):
		t.Fatal("创建没开始")
	}

	state, err = fresh.state(ctx, "s-2")
	if err != nil {
		t.Fatalf("state during creation: %v", err)
	}
	if state.Worktree.Creation == nil {
		t.Fatalf("创建进行中 creation 必须非 null：%+v", state.Worktree)
	}
	if state.Worktree.Creation.Phase != worktree.ProgressPhasePrepare {
		t.Fatalf("建分支阶段 phase 必须是 prepare，得到 %q", state.Worktree.Creation.Phase)
	}
	if state.Worktree.Creation.Branch == "" {
		t.Fatal("creation.branch 必须是真实分支名")
	}
	if !state.Worktree.Creation.Held {
		t.Fatal("有消息在 inbox hold 里等时 creation.held 必须是 true")
	}
	if state.Worktree.Waiting == nil || *state.Worktree.Waiting != "creation" {
		t.Fatalf("有消息等创建时 waiting 必须是 creation：%v", state.Worktree.Waiting)
	}

	// 4) 分支建好 → phase 进到 checkout（底层步骤一变，面板值跟着变）。
	close(freshGit.addGate)
	select {
	case <-freshGit.resetBegun:
	case <-time.After(5 * time.Second):
		t.Fatal("没走到写文件阶段")
	}
	deadline := time.Now().Add(2 * time.Second)
	phase := ""
	for time.Now().Before(deadline) {
		state, err = fresh.state(ctx, "s-2")
		if err != nil {
			t.Fatalf("state during checkout: %v", err)
		}
		if state.Worktree.Creation != nil && state.Worktree.Creation.Phase == worktree.ProgressPhaseCheckout {
			phase = state.Worktree.Creation.Phase
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if phase != worktree.ProgressPhaseCheckout {
		t.Fatalf("写文件阶段 phase 必须是 checkout，得到 %+v", state.Worktree.Creation)
	}

	// 5) 停掉创建：creation 必须回到 null（不是一直挂着）。
	if err := freshSvc.StopCreation("s-2"); err != nil {
		t.Fatalf("StopCreation: %v", err)
	}
	select {
	case <-createDone:
	case <-time.After(5 * time.Second):
		t.Fatal("停掉后创建没退出")
	}
	state, err = fresh.state(ctx, "s-2")
	if err != nil {
		t.Fatalf("state after stop: %v", err)
	}
	if state.Worktree.Creation != nil {
		t.Fatalf("创建停了 creation 必须回到 null：%+v", state.Worktree.Creation)
	}

	// 6) 出现 created 记录：created 非 null、editable 变 false。
	freshStore.append(worktree.EventType, worktree.Record{
		Type: worktree.RecordCreated, Branch: "dsh-11112222-abcd", BaseCommit: "deadbeef",
		Directory: freshStore.cwd + "-wt", SourceRoot: freshStore.cwd,
	})
	state, err = fresh.state(ctx, "s-2")
	if err != nil {
		t.Fatalf("state after created: %v", err)
	}
	if state.Worktree.Created == nil {
		t.Fatalf("created 记录之后 created 必须非 null：%+v", state.Worktree)
	}
	if state.Worktree.Created.Branch != "dsh-11112222-abcd" || state.Worktree.Created.BaseCommit != "deadbeef" {
		t.Fatalf("created 值不对：%+v", state.Worktree.Created)
	}
	if state.Worktree.Editable {
		t.Fatal("创建过 worktree 之后 editable 必须是 false")
	}
}

// 没有 worktree 服务时，editable 必须是 false（官方定义的第一条前提），
// 而不是"默认 true"。
func TestForgePanelWorktreeUnavailableIsFalse(t *testing.T) {
	h := newForgeHostForTest(t, t.TempDir())
	state, err := h.state(context.Background(), "s-1")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Worktree.Editable {
		t.Fatal("没有 worktrees 服务时 editable 必须 false")
	}
	if state.Worktree.Creation != nil || state.Worktree.Created != nil || state.Worktree.Request != nil {
		t.Fatalf("没有服务时 worktree 子对象必须全 null：%+v", state.Worktree)
	}
}
