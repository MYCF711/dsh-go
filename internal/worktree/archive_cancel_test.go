package worktree

// 反归档取消（`CancelArchiveCleanup`，官方 archive.ts:87-93）的用例。
//
// 官方 `workspace/session-unarchiving` 监听器：拿到 attempt → 置 cancelled →
// await done → 删 attempt。这里逐条钉住四种情形：
//
//	a. 取消落在**准入之前**（inspection 还没跑 / 还没 `git worktree remove`）→
//	   移除根本不发生，目录、`git worktree list` 注册、分支全都不变；
//	b. 取消落在**准入之后**（git 正在删）→ 不打断，等它结算；removed 之后由官方
//	   恢复路径 `RestoreWorktree` 接手；
//	c. 取消来晚且恢复失败 → 结构化 *Error（CodeRestoreFailed/500）+ 真实 git 错误；
//	d. 并发取消 → 只跑一次 `git worktree remove`、所有取消者拿到同一个终态、
//	   之后不会"删了又恢复"地写第二遍。
//
// 真实 git、真实目录；只有会话日志（Store）与宿主注入（ArchiveHost）是内存实现，
// 与 archive_test.go 的既有写法一致。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingRemovalGit 在 `git worktree remove` 上卡住，把"取消"精确地放到已经
// 准入之后（inspection 全过、git 正在删），同时统计真实移除次数。
type blockingRemovalGit struct {
	Git
	once        sync.Once
	releaseOnce sync.Once
	started     chan struct{}
	release     chan struct{}

	mu      sync.Mutex
	removes []string
}

func newBlockingRemovalGit() *blockingRemovalGit {
	return &blockingRemovalGit{
		Git:     ExecGit{Timeout: 60 * time.Second},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (g *blockingRemovalGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
		g.mu.Lock()
		g.removes = append(g.removes, dir)
		g.mu.Unlock()
		g.once.Do(func() { close(g.started) })
		<-g.release
	}
	return g.Git.Run(ctx, dir, args...)
}

func (g *blockingRemovalGit) removalCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.removes)
}

// unblock 放行被卡住的 `git worktree remove`（可重复调用）。
//
// 为什么要 sync.Once + Cleanup：断言失败时测试 goroutine 会 Goexit，直接
// `close(git.release)` 那一行就跑不到，被卡的清理 goroutine 会让 svc.Close() 永远等
// 下去——失败要失败得干净，不许把整个包挂死。
func (g *blockingRemovalGit) unblock() {
	g.releaseOnce.Do(func() { close(g.release) })
}

// recordLegacyCreated 写旧 `plugin:client-ui-forge/worktree` 的 created 记录：
// `RestoreWorktree`（官方恢复路径）读的是 `snapshot.Created`，取消来晚后要靠它恢复。
func recordLegacyCreated(store *memStore, sessionID, repo, dir, branch, baseCommit string) {
	store.append(sessionID, EventType, Record{
		Type: RecordCreated, Branch: branch, BaseCommit: baseCommit, Directory: dir, SourceRoot: repo,
	})
}

// waitStarted 等一个"后台已经走到关键点"的信号，超时即失败。
func waitStarted(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("等待 %s 超时", what)
	}
}

// TestCancelArchiveCleanupBeforeAdmissionKeepsCheckout 是"取消成功"的判据。
func TestCancelArchiveCleanupBeforeAdmissionKeepsCheckout(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "cancel-early"
	branch := "dsh-55555555-0012"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)

	// 把窗口卡在 `unused()` 里：inspection 还没跑、`git worktree remove` 还没准入，
	// 正是官方"取消尚未准入的移除"要覆盖的时机。Unused 返回 true —— 没有取消时
	// 这个 checkout 一定会被删掉（archive_test.go 用例 1 已证）。
	release := make(chan struct{})
	started := make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	host := permissiveHost()
	host.Unused = func(string, string) (bool, error) {
		once.Do(func() { close(started) })
		<-release
		return true, nil
	}
	svc.SetArchiveHost(host)
	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	waitStarted(t, started, "后台 inspection")

	type outcome struct {
		cancellation CleanupCancellation
		err          error
	}
	done := make(chan outcome, 1)
	go func() {
		cancellation, err := svc.CancelArchiveCleanup(session)
		done <- outcome{cancellation: cancellation, err: err}
	}()

	// 官方 archive.ts:91 是 `await attempt.done`：结算之前取消不许返回。
	select {
	case got := <-done:
		t.Fatalf("CancelArchiveCleanup 还没结算就返回了：%+v", got)
	case <-time.After(200 * time.Millisecond):
	}
	unblock()
	result := <-done
	if result.err != nil {
		t.Fatalf("CancelArchiveCleanup: %v", result.err)
	}
	if !result.cancellation.Cancelled || result.cancellation.Revision != 1 {
		t.Fatalf("取消结果 = %+v，期望 cancelled=true revision=1", result.cancellation)
	}
	if result.cancellation.Status != CleanupRetained {
		t.Fatalf("取消及时应结算为 retained，实际 %s", result.cancellation.Status)
	}
	if result.cancellation.Removed() {
		t.Fatal("取消及时不该被报告成 removed")
	}

	// 判据：目录与 `git worktree list` 都不变，分支保留。
	mustExist(t, dir, "取消后的 checkout 目录")
	if got := worktreeRegistrations(t, repo, dir); got != 1 {
		t.Fatalf("取消后的 worktree 注册必须原样保留，实际 %d 条", got)
	}
	if head := gitRun(t, dir, "symbolic-ref", "--short", "HEAD"); head != branch {
		t.Fatalf("checkout 的 HEAD 分支 = %q，期望 %q", head, branch)
	}
	mustBranchKept(t, repo, branch)

	// 官方 archive.ts:92：取消者把 attempt 收走，之后 cleanupOf 为 null。
	if cleanup, ok := svc.CleanupOf(session, nil); ok || cleanup != nil {
		t.Fatalf("取消后 attempt 必须被收走（cleanupOf → null），实际 %+v ok=%v", cleanup, ok)
	}
}

// TestCancelArchiveCleanupAfterAdmissionWaitsAndReportsRemoved 是"取消来晚"的判据：
// 移除已经准入 → 不打断、等结算 → 报告 removed（checkout 真的没了）→ 官方恢复路径接手。
func TestCancelArchiveCleanupAfterAdmissionWaitsAndReportsRemoved(t *testing.T) {
	repo := canonicalRepo(t)
	store := newMemStore()
	git := newBlockingRemovalGit()
	svc := NewService(store, git)
	t.Cleanup(svc.Close)
	t.Cleanup(git.unblock)

	const session = "cancel-late"
	branch := "dsh-66666666-0013"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	recordLegacyCreated(store, session, repo, dir, branch, baseCommit)
	svc.SetArchiveHost(permissiveHost())

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	waitStarted(t, git.started, "`git worktree remove`")

	done := make(chan CleanupCancellation, 1)
	go func() {
		cancellation, err := svc.CancelArchiveCleanup(session)
		if err != nil {
			t.Errorf("CancelArchiveCleanup: %v", err)
		}
		done <- cancellation
	}()
	// 已经准入：取消不打断正在跑的 git，必须等它结算。
	select {
	case got := <-done:
		t.Fatalf("已准入的移除还在跑，取消不许先返回：%+v", got)
	case <-time.After(200 * time.Millisecond):
	}
	git.unblock()
	cancellation := <-done
	if !cancellation.Removed() {
		t.Fatalf("取消来晚必须如实报告 removed，实际 %+v", cancellation)
	}
	mustGone(t, dir, "取消来晚的 checkout 目录")

	// 官方恢复路径（guide §Migration 2 的 Restore worktree）接手。
	if err := svc.RestoreWorktree(session); err != nil {
		t.Fatalf("取消来晚后恢复失败：%v", err)
	}
	mustExist(t, dir, "恢复后的 checkout 目录")
	if got := worktreeRegistrations(t, repo, dir); got != 1 {
		t.Fatalf("恢复后 worktree 注册应为 1 条，实际 %d 条", got)
	}
	if head := gitRun(t, dir, "symbolic-ref", "--short", "HEAD"); head != branch {
		t.Fatalf("恢复出来的 HEAD 分支 = %q，期望 %q", head, branch)
	}
	if state := mustState(t, svc, session); state.Status != StatusCreated {
		t.Fatalf("恢复后状态 = %s，期望 created", state.Status)
	}
	mustBranchKept(t, repo, branch)
}

// TestCancelArchiveCleanupLateRestoreFailureIsStructured 是"取消来晚且恢复失败"的判据：
// 失败必须是结构化 *Error（500 + CodeRestoreFailed）且带**真实 git 错误**，不许静默。
func TestCancelArchiveCleanupLateRestoreFailureIsStructured(t *testing.T) {
	repo := canonicalRepo(t)
	store := newMemStore()
	git := newBlockingRemovalGit()
	svc := NewService(store, git)
	t.Cleanup(svc.Close)
	t.Cleanup(git.unblock)

	const session = "cancel-late-fail"
	branch := "dsh-77777777-0014"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	recordLegacyCreated(store, session, repo, dir, branch, baseCommit)
	svc.SetArchiveHost(permissiveHost())

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	waitStarted(t, git.started, "`git worktree remove`")
	done := make(chan CleanupCancellation, 1)
	go func() {
		cancellation, _ := svc.CancelArchiveCleanup(session)
		done <- cancellation
	}()
	git.unblock()
	if cancellation := <-done; !cancellation.Removed() {
		t.Fatalf("取消来晚必须如实报告 removed，实际 %+v", cancellation)
	}
	mustGone(t, dir, "取消来晚的 checkout 目录")

	// 现场：把这条分支检出到**另一个** worktree —— 恢复时
	// `git worktree add <dir> <branch>` 会被真实 git 拒绝：
	//   fatal: '<branch>' is already checked out at '<other>'
	other := filepath.Join(repo, ".agents", "worktrees", branch+"-other")
	gitRun(t, repo, "worktree", "add", other, branch)
	t.Cleanup(func() { gitQuiet(repo, "worktree", "remove", "--force", other) })

	err := svc.RestoreWorktree(session)
	if err == nil {
		t.Fatal("分支已被别的 worktree 检出时，恢复必须失败（不许假装恢复）")
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("恢复失败必须是结构化 *Error（transport 要翻四字段），实际 %T: %v", err, err)
	}
	if typed.Code != CodeRestoreFailed || typed.Status != 500 {
		t.Fatalf("恢复失败 = %s/%d，期望 %s/500", typed.Code, typed.Status, CodeRestoreFailed)
	}
	// 真实 git 错误必须原样带出来（不是"失败了"这种无信息文案）：
	//   fatal: '<branch>' is already used by worktree at '<other>'
	if !strings.Contains(typed.Message, "is already used by worktree at") {
		t.Fatalf("错误里必须带真实 git 错误（is already used by worktree at）：%s", typed.Message)
	}
	if !strings.Contains(typed.Message, dir) {
		t.Fatalf("错误里必须点名恢复目录 %q：%s", dir, typed.Message)
	}
	mustBranchKept(t, repo, branch)
}

// TestConcurrentCancelArchiveCleanupSettlesOnce 是"并发反归档不双写"的判据：
// 4 个取消者（UI 连点 / 并发反归档）都等同一个结算、拿到同一个终态，`git worktree
// remove` 只跑一次，恢复路径也只做一次（第二次 restore 是 409 present，不是再写一遍）。
func TestConcurrentCancelArchiveCleanupSettlesOnce(t *testing.T) {
	repo := canonicalRepo(t)
	store := newMemStore()
	git := newBlockingRemovalGit()
	svc := NewService(store, git)
	t.Cleanup(svc.Close)
	t.Cleanup(git.unblock)

	const session = "cancel-concurrent"
	const callers = 4
	branch := "dsh-88888888-0015"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	recordLegacyCreated(store, session, repo, dir, branch, baseCommit)
	svc.SetArchiveHost(permissiveHost())

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	waitStarted(t, git.started, "`git worktree remove`")

	results := make([]CleanupCancellation, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for slot := 0; slot < callers; slot++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			results[slot], errs[slot] = svc.CancelArchiveCleanup(session)
		}(slot)
	}
	// 所有取消者都卡在 await 结算上：此时移除还没跑完。
	time.Sleep(100 * time.Millisecond)
	if got := git.removalCount(); got != 1 {
		t.Fatalf("`git worktree remove` 应只被准入 1 次，实际 %d 次", got)
	}
	git.unblock()
	wg.Wait()

	for slot := range results {
		if errs[slot] != nil {
			t.Fatalf("第 %d 个并发取消失败：%v", slot, errs[slot])
		}
		if !results[slot].Cancelled || !results[slot].Removed() {
			t.Fatalf("第 %d 个取消结果 = %+v，期望 cancelled=true removed", slot, results[slot])
		}
		if results[slot] != results[0] {
			t.Fatalf("并发取消者的终态必须一致：第 0 个 %+v，第 %d 个 %+v",
				results[0], slot, results[slot])
		}
	}
	if got := git.removalCount(); got != 1 {
		t.Fatalf("并发取消后 `git worktree remove` 仍应只有 1 次，实际 %d 次", got)
	}
	mustGone(t, dir, "并发取消后的 checkout 目录")
	if cleanup, ok := svc.CleanupOf(session, nil); ok || cleanup != nil {
		t.Fatalf("并发取消后 attempt 必须已被收走，实际 %+v ok=%v", cleanup, ok)
	}

	// 恢复只做一次：第二次是幂等成功（同分支 + 同 base commit 只补记录），
	// **不许**再写一份 checkout —— 判据是 git 侧仍然只有 1 条注册。
	if err := svc.RestoreWorktree(session); err != nil {
		t.Fatalf("并发取消后恢复失败：%v", err)
	}
	if err := svc.RestoreWorktree(session); err != nil {
		t.Fatalf("第二次恢复应幂等成功（只补记录，不重写 checkout）：%v", err)
	}
	if got := worktreeRegistrations(t, repo, dir); got != 1 {
		t.Fatalf("两次恢复后 worktree 注册仍应只有 1 条，实际 %d 条", got)
	}
	mustBranchKept(t, repo, branch)
}

// TestCancelArchiveCleanupWithoutAttemptIsNoop：没有在途尝试时是 no-op（官方
// `attempt === undefined → return`），不报错、不假装取消过。
func TestCancelArchiveCleanupWithoutAttemptIsNoop(t *testing.T) {
	svc, _ := newArchiveService(t)

	cancellation, err := svc.CancelArchiveCleanup("never-archived")
	if err != nil {
		t.Fatalf("没有在途尝试时取消不应报错：%v", err)
	}
	if cancellation != (CleanupCancellation{}) {
		t.Fatalf("没有在途尝试时应返回零值，实际 %+v", cancellation)
	}
	// 空会话 id 是调用方错误（与其他方法一致）。
	if _, err := svc.CancelArchiveCleanup(""); err == nil {
		t.Fatal("空 sessionId 必须报错")
	}
}

// TestCancelArchiveCleanupSurvivesHostPanic：宿主注入的回调 panic 也必须结算——
// 取消者（以及 CleanupOf(&revision)）在 await 这个 attempt，不许被永远挂住。
func TestCancelArchiveCleanupSurvivesHostPanic(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "cancel-panic"
	branch := "dsh-99999999-0016"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	host := permissiveHost()
	started := make(chan struct{})
	host.Unused = func(string, string) (bool, error) {
		close(started)
		panic("宿主回调崩了")
	}
	svc.SetArchiveHost(host)

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	// 等后台真的走到宿主回调里（否则取消可能抢在 inspection 之前结算为 retained，
	// 那样 panic 根本没发生，测不到"结算必须发生"）。
	waitStarted(t, started, "宿主回调")
	settled := make(chan CleanupCancellation, 1)
	go func() {
		cancellation, _ := svc.CancelArchiveCleanup(session)
		settled <- cancellation
	}()
	select {
	case cancellation := <-settled:
		if !cancellation.Cancelled || cancellation.Status != CleanupFailed {
			t.Fatalf("panic 应结算为 failed，实际 %+v", cancellation)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("宿主回调 panic 后取消者被挂住了（结算必须发生）")
	}
	mustExist(t, dir, "panic 现场的 checkout 目录")
	mustBranchKept(t, repo, branch)
}
