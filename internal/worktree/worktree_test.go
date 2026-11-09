package worktree

// 真实临时 git repo 上的 worktree 状态机测试（不 fake git 的成功路径）。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/codecs/v4"
)

type memStore struct {
	mu     sync.Mutex
	header map[string]string
	events map[string][]v4.Event
}

func newMemStore() *memStore {
	return &memStore{header: map[string]string{}, events: map[string][]v4.Event{}}
}

func (m *memStore) append(sessionID, eventType string, data any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, _ := json.Marshal(data)
	events := m.events[sessionID]
	seq := int64(len(events))
	m.events[sessionID] = append(events, v4.Event{Type: eventType, Seq: seq, Time: time.Now().UnixMilli(), Data: raw})
}

func (m *memStore) Load(sessionID string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Fold(m.header[sessionID], m.events[sessionID]), nil
}

func (m *memStore) AppendWorktree(sessionID string, record Record) error {
	m.append(sessionID, EventType, record)
	return nil
}

func (m *memStore) SetWorkingDirectory(sessionID, dir string) error {
	m.append(sessionID, "working-directory/change", map[string]string{"cwd": dir})
	return nil
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "t103@example.com")
	gitRun(t, dir, "config", "user.name", "T103")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func newTestService(t *testing.T, store Store, sessionID, repo string) *Service {
	t.Helper()
	svc := NewService(store, ExecGit{Timeout: 30 * time.Second})
	if mem, ok := store.(*memStore); ok {
		mem.header[sessionID] = repo
	}
	return svc
}

func mustState(t *testing.T, svc *Service, sessionID string) State {
	t.Helper()
	state, err := svc.State(sessionID)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	return state
}

func TestRequestCreatesRealWorktree(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s1", repo)

	if err := svc.RequestWorktree("s1", nil); err != nil {
		t.Fatalf("RequestWorktree: %v", err)
	}
	if state := mustState(t, svc, "s1"); state.Status != StatusOpen {
		t.Fatalf("请求后状态 = %s，期望 open", state.Status)
	}
	branch := mustState(t, svc, "s1").Branch
	if !strings.HasPrefix(branch, "dsh-") || len(branch) != len("dsh-")+8+1+4 {
		t.Fatalf("分支名 = %q（应 dsh-<8 位>-<4 位>）", branch)
	}

	if err := svc.PreStep(context.Background(), "s1", 1); err != nil {
		t.Fatalf("PreStep: %v", err)
	}
	state := mustState(t, svc, "s1")
	if state.Status != StatusCreated {
		t.Fatalf("PreStep 后状态 = %s，期望 created", state.Status)
	}
	dir := canonicalPath(filepath.Join(repo, ".agents", "worktrees", branch))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("worktree 目录不存在: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("worktree 没有检出文件: %v", err)
	}
	branches := gitRun(t, repo, "branch", "--list", branch)
	if !strings.Contains(branches, branch) {
		t.Fatalf("git 里没有分支 %s: %q", branch, branches)
	}
	if state.Cwd != dir {
		t.Fatalf("会话目录 = %q，期望 %q", state.Cwd, dir)
	}
	// created 后 hold 立即放行。
	if err := svc.Wait(context.Background(), "s1"); err != nil {
		t.Fatalf("created 后 Wait 应返回: %v", err)
	}
}

func TestRequestRefusesAfterTurnStartedOrCreated(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s2", repo)
	store.append("s2", "turn/start", map[string]any{"turn": 1})

	err := svc.RequestWorktree("s2", nil)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeSessionStarted || typed.Status != 400 {
		t.Fatalf("turn/start 后 request = %v，期望 session-started/400", err)
	}
}

func TestRequestValidatesFromAndCheckout(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s3", repo)

	empty := ""
	err := svc.RequestWorktree("s3", &empty)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeInvalidBranch {
		t.Fatalf("空 from = %v，期望 invalid-branch", err)
	}

	notRepo := t.TempDir()
	svc2 := newTestService(t, newMemStore(), "s4", notRepo)
	err = svc2.RequestWorktree("s4", nil)
	if !errors.As(err, &typed) || typed.Code != CodeNotACheckout {
		t.Fatalf("非 repo 目录 = %v，期望 not-a-checkout", err)
	}
}

func TestCancelReleasesHoldWithoutCreating(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s5", repo)
	if err := svc.RequestWorktree("s5", nil); err != nil {
		t.Fatal(err)
	}
	branch := mustState(t, svc, "s5").Branch

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	released := make(chan error, 1)
	go func() { released <- svc.Wait(ctx, "s5") }()
	select {
	case err := <-released:
		t.Fatalf("cancel 前 hold 不应放行: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := svc.CancelWorktree("s5"); err != nil {
		t.Fatalf("CancelWorktree: %v", err)
	}
	select {
	case err := <-released:
		if err != nil {
			t.Fatalf("cancel 后 Wait = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel 后 hold 没放行")
	}
	if state := mustState(t, svc, "s5"); state.Status != StatusWithdrawn {
		t.Fatalf("状态 = %s，期望 withdrawn", state.Status)
	}
	if _, err := os.Stat(filepath.Join(repo, ".agents", "worktrees", branch)); !os.IsNotExist(err) {
		t.Fatalf("取消的请求不应创建目录: %v", err)
	}
}

// blockingGit 第一次 Run 阻塞到 release（或 ctx 取消），用于确定性测 stop。
type blockingGit struct {
	real    Git
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *blockingGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "worktree" {
		g.once.Do(func() { close(g.started) })
		select {
		case <-g.release:
			return "", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return g.real.Run(ctx, dir, args...)
}

func TestStopCreationKeepsHoldWaiting(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	git := &blockingGit{real: ExecGit{Timeout: 30 * time.Second}, started: make(chan struct{}), release: make(chan struct{})}
	svc := NewService(store, git)
	store.header["s6"] = repo
	if err := svc.RequestWorktree("s6", nil); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- svc.PreStep(context.Background(), "s6", 1) }()
	select {
	case <-git.started:
	case <-time.After(2 * time.Second):
		t.Fatal("PreStep 没开始")
	}
	if err := svc.StopCreation("s6"); err != nil {
		t.Fatalf("StopCreation: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("被 stop 的 PreStep 不应返回错误（官方 stopped 不是 failed）: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StopCreation 没结束创建")
	}
	if state := mustState(t, svc, "s6"); state.Status != StatusStopped {
		t.Fatalf("状态 = %s，期望 stopped", state.Status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := svc.Wait(ctx, "s6"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stopped 后 hold 必须继续等（retry/cancel），实际 %v", err)
	}
}

// scriptedGit 只让下一次 `worktree add` 失败，其余命令走真实 git（repoInfo
// 需要真实 rev-parse）。
type scriptedGit struct {
	real Git
	mu   sync.Mutex
	fail bool
}

func (g *scriptedGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	g.mu.Lock()
	fail := g.fail
	if fail && len(args) > 0 && args[0] == "worktree" {
		g.fail = false
	}
	g.mu.Unlock()
	if fail && len(args) > 0 && args[0] == "worktree" {
		return "", errors.New("scripted worktree failure")
	}
	return g.real.Run(ctx, dir, args...)
}

func (g *scriptedGit) setFail(fail bool) {
	g.mu.Lock()
	g.fail = fail
	g.mu.Unlock()
}

func TestFailureThenRetryCreatesNewBranch(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	git := &scriptedGit{real: ExecGit{Timeout: 30 * time.Second}, fail: true}
	svc := NewService(store, git)
	store.header["s7"] = repo
	if err := svc.RequestWorktree("s7", nil); err != nil {
		t.Fatal(err)
	}
	firstBranch := mustState(t, svc, "s7").Branch

	if err := svc.PreStep(context.Background(), "s7", 1); err == nil {
		t.Fatal("失败的 PreStep 必返回错误（结束 turn）")
	}
	state := mustState(t, svc, "s7")
	if state.Status != StatusFailed || state.Reason == "" || state.Step == "" {
		t.Fatalf("失败状态 = %+v（要带 step/reason）", state)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := svc.Wait(ctx, "s7"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("失败后 hold 必须继续等，实际 %v", err)
	}
	// pre-step 不自动重试（等待 retry/cancel）。
	if err := svc.PreStep(context.Background(), "s7", 1); err != nil {
		t.Fatalf("失败后的 pre-step 应跳过: %v", err)
	}
	if err := svc.RetryWorktree("s7"); err != nil {
		t.Fatalf("RetryWorktree: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		state = mustState(t, svc, "s7")
		if state.Status == StatusCreated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("重试后状态 = %+v，期望 created", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state.Branch == firstBranch {
		t.Fatalf("重试必须用新分支名（%s）", firstBranch)
	}
	if err := svc.Wait(context.Background(), "s7"); err != nil {
		t.Fatalf("created 后 hold 放行: %v", err)
	}
}

func TestRestoreRecreatesMissingWorktree(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s8", repo)
	if err := svc.RequestWorktree("s8", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.PreStep(context.Background(), "s8", 1); err != nil {
		t.Fatal(err)
	}
	state := mustState(t, svc, "s8")
	dir := state.Directory
	branch := state.Branch
	// 模拟 worktree 被外部删除。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreWorktree("s8"); err != nil {
		t.Fatalf("RestoreWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("restore 后目录没有文件: %v", err)
	}
	state = mustState(t, svc, "s8")
	if state.Status != StatusCreated || canonicalPath(state.Cwd) != dir {
		t.Fatalf("restore 后状态 = %+v", state)
	}

	// 分支也没了：从起始 commit 重建同名分支。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "worktree", "prune")
	gitRun(t, repo, "branch", "-D", branch)
	if err := svc.RestoreWorktree("s8"); err != nil {
		t.Fatalf("分支删除后的 RestoreWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("从 baseCommit 重建后没有文件: %v", err)
	}
}

func TestContinueInProjectMovesSessionBack(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s9", repo)
	if err := svc.RequestWorktree("s9", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.PreStep(context.Background(), "s9", 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.ContinueInProject("s9"); err != nil {
		t.Fatalf("ContinueInProject: %v", err)
	}
	state := mustState(t, svc, "s9")
	if canonicalPath(state.Cwd) != canonicalPath(repo) {
		t.Fatalf("会话目录 = %q，期望源 checkout %q", state.Cwd, repo)
	}
	if state.Status != StatusWithdrawn {
		t.Fatalf("状态 = %s，期望 withdrawn", state.Status)
	}
	if err := svc.Wait(context.Background(), "s9"); err != nil {
		t.Fatalf("withdrawn 后 hold 放行: %v", err)
	}
}

func TestRestartFoldsPersistedState(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s10", repo)
	if err := svc.RequestWorktree("s10", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.PreStep(context.Background(), "s10", 1); err != nil {
		t.Fatal(err)
	}
	// "重启"：新 Service 读同一份日志。
	restarted := NewService(store, ExecGit{Timeout: 30 * time.Second})
	state := mustState(t, restarted, "s10")
	if state.Status != StatusCreated || state.Branch == "" || state.Cwd == "" {
		t.Fatalf("重启后状态 = %+v", state)
	}
	if err := restarted.Wait(context.Background(), "s10"); err != nil {
		t.Fatalf("重启后 hold 应立即放行: %v", err)
	}
}

// ---------------------------------------------------------------------------
// T155：目录被外部删除/上一次 restore 中断后的恢复（stale 清理，不是放宽判据）
// ---------------------------------------------------------------------------

// failingRestoreStore 让下一次 RecordRestored 写失败（模拟会话写锁/进程退出）。
type failingRestoreStore struct {
	Store
	mu   sync.Mutex
	fail bool
}

func (f *failingRestoreStore) AppendWorktree(sessionID string, record Record) error {
	f.mu.Lock()
	fail := f.fail && record.Type == RecordRestored
	f.mu.Unlock()
	if fail {
		return errors.New("会话正被另一个进程写入")
	}
	return f.Store.AppendWorktree(sessionID, record)
}

func (f *failingRestoreStore) setFail(fail bool) {
	f.mu.Lock()
	f.fail = fail
	f.mu.Unlock()
}

func TestRestoreCleansStalePartialDirectory(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s15", repo)
	if err := svc.RequestWorktree("s15", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.PreStep(context.Background(), "s15", 1); err != nil {
		t.Fatal(err)
	}
	dir := mustState(t, svc, "s15").Directory
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// 上一次失败留下的空目录（没有 .git）——旧实现会 409 present。
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreWorktree("s15"); err != nil {
		t.Fatalf("stale 目录 restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("stale 清理后没有重建文件: %v", err)
	}
	state := mustState(t, svc, "s15")
	if state.Status != StatusCreated || canonicalPath(state.Cwd) != canonicalPath(dir) {
		t.Fatalf("restore 后状态 = %+v", state)
	}
}

func TestRestoreResumesInterruptedCheckout(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s16", repo)
	if err := svc.RequestWorktree("s16", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.PreStep(context.Background(), "s16", 1); err != nil {
		t.Fatal(err)
	}
	state := mustState(t, svc, "s16")
	dir, branch := state.Directory, state.Branch
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// 模拟"git add 已经成功、写记录前进程退出/被锁"：目录有效但记录仍是 created。
	gitRun(t, repo, "worktree", "prune")
	gitRun(t, repo, "worktree", "add", dir, branch)
	if err := svc.RestoreWorktree("s16"); err != nil {
		t.Fatalf("中断的 restore 应幂等完成: %v", err)
	}
	state = mustState(t, svc, "s16")
	if state.Status != StatusCreated || canonicalPath(state.Cwd) != canonicalPath(dir) {
		t.Fatalf("resume 后状态 = %+v", state)
	}
	if got := worktreeRegistrations(t, repo, dir); got != 1 {
		t.Fatalf("worktree 注册应只有一条，实际 %d", got)
	}
}

func TestRestoreBlocksGenuinePresentCheckout(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s17", repo)
	if err := svc.RequestWorktree("s17", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.PreStep(context.Background(), "s17", 1); err != nil {
		t.Fatal(err)
	}
	dir := mustState(t, svc, "s17").Directory
	// 用户在用这个 checkout：HEAD 移动后 restore 必须按官方语义 409。
	if err := os.WriteFile(filepath.Join(dir, "user.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "user.txt")
	gitRun(t, dir, "commit", "-q", "-m", "user work")
	err := svc.RestoreWorktree("s17")
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeWorktreePresent || typed.Status != 409 {
		t.Fatalf("真实存在的 checkout = %v，期望 409 worktree-present", err)
	}
}

func branchOf(t *testing.T, svc *Service, sessionID string) string {
	t.Helper()
	return mustState(t, svc, sessionID).Branch
}

// worktreeRegistrations 数 git 注册表里该目录出现几次（git 用 /，比较前统一）。
func worktreeRegistrations(t *testing.T, repo, dir string) int {
	t.Helper()
	out := strings.ReplaceAll(gitRun(t, repo, "worktree", "list", "--porcelain"), "/", `\`)
	return strings.Count(out, "worktree "+dir)
}

func TestRestorePrunesStaleRegistrationAndRebuilds(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s18", repo)
	if err := svc.RequestWorktree("s18", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.PreStep(context.Background(), "s18", 1); err != nil {
		t.Fatal(err)
	}
	dir := mustState(t, svc, "s18").Directory
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// 目录没了但 git 注册还在（t143 的现场）。
	if _, err := os.Stat(filepath.Join(repo, ".git", "worktrees", branchOf(t, svc, "s18"))); err != nil {
		t.Fatalf("前置：worktree 注册目录应存在: %v", err)
	}
	if err := svc.RestoreWorktree("s18"); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("重建后没有文件: %v", err)
	}
	// 注册表里只有重建后的这一条（没有 stale 重复）。
	if got := worktreeRegistrations(t, repo, dir); got != 1 {
		t.Fatalf("stale 注册没清理，实际 %d 条", got)
	}
	// checkout 可用：git status 能跑。
	gitRun(t, dir, "status", "--porcelain")
}

func TestRestoreRollsBackFailedRecord(t *testing.T) {
	repo := newRepo(t)
	base := newMemStore()
	base.header["s19"] = repo
	store := &failingRestoreStore{Store: base}
	svc := NewService(store, ExecGit{Timeout: 30 * time.Second})
	if err := svc.RequestWorktree("s19", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.PreStep(context.Background(), "s19", 1); err != nil {
		t.Fatal(err)
	}
	dir := mustState(t, svc, "s19").Directory
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	store.setFail(true)
	if err := svc.RestoreWorktree("s19"); err == nil {
		t.Fatal("记录写失败必须报错")
	}
	// 回滚：刚建的目录不能留下（否则下一次被误判 present）。
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("写记录失败后目录应回滚删除: %v", err)
	}
	store.setFail(false)
	if err := svc.RestoreWorktree("s19"); err != nil {
		t.Fatalf("回滚后重试: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("重试后没有文件: %v", err)
	}
}

func TestRestoreAfterRestart(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	svc := newTestService(t, store, "s20", repo)
	if err := svc.RequestWorktree("s20", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.PreStep(context.Background(), "s20", 1); err != nil {
		t.Fatal(err)
	}
	dir := mustState(t, svc, "s20").Directory
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	restarted := NewService(store, ExecGit{Timeout: 30 * time.Second})
	if err := restarted.RestoreWorktree("s20"); err != nil {
		t.Fatalf("重启后恢复: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("重启恢复后没有文件: %v", err)
	}
	state := mustState(t, restarted, "s20")
	if state.Status != StatusCreated || canonicalPath(state.Cwd) != canonicalPath(dir) {
		t.Fatalf("重启恢复后状态 = %+v", state)
	}
}
