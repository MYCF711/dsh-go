package worktree

// rc.2「归档会话时清理工作树」的宿主侧用例：真实 git、真实目录；只有会话日志
// （Store）和宿主注入（ArchiveHost）用内存实现，写法与 worktree_test.go 的
// fake store 一致。

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
)

// archiveWait 是一条归档清理在测试里的等待上限（真 git 命令，给足余量）。
const archiveWait = 20 * time.Second

// AppendCreated 写官方 plugin:worktree/created 记录：内存 store 实现这个可选扩展，
// create() 才会落这条记录，归档用例也能直接预置一条。
func (m *memStore) AppendCreated(sessionID string, creation Creation) error {
	m.append(sessionID, EventTypeCreated, creation)
	return nil
}

// canonicalRepo 造一个真实临时仓库，返回规范化路径（git 与 Go 给的形式不同）。
func canonicalRepo(t *testing.T) string {
	t.Helper()
	return canonicalPath(newRepo(t))
}

func newArchiveService(t *testing.T) (*Service, *memStore) {
	t.Helper()
	store := newMemStore()
	svc := NewService(store, ExecGit{Timeout: 60 * time.Second})
	t.Cleanup(svc.Close)
	return svc, store
}

// addArchiveWorktree 在官方池内建一个真实 checkout，返回目录与 base commit。
func addArchiveWorktree(t *testing.T, repo, branch string) (dir, baseCommit string) {
	t.Helper()
	dir = filepath.Join(repo, ".agents", "worktrees", branch)
	gitRun(t, repo, "worktree", "add", "-b", branch, dir, "HEAD")
	return dir, gitRun(t, repo, "rev-parse", "HEAD")
}

// recordCreated 把一条 plugin:worktree/created 记录写进会话日志（模拟 create() 的写入）。
func recordCreated(t *testing.T, store *memStore, sessionID, repo, dir, branch, baseCommit string) {
	t.Helper()
	store.append(sessionID, EventTypeCreated, Creation{
		Path: dir, Branch: branch, BaseCommit: baseCommit, RepositoryRoot: repo,
	})
}

// permissiveHost 是"可清理"的宿主：可写、没有别的会话引用、没有额外互斥。
func permissiveHost() ArchiveHost {
	return ArchiveHost{
		RetainOnArchive: func() bool { return false },
		ReadOnly:        func(string) bool { return false },
		Unused:          func(string, string) (bool, error) { return true, nil },
	}
}

// gitQuiet 跑一条 git 命令并忽略结果（收尾清理用，不在 Cleanup 里 Fatal）。
func gitQuiet(dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	_ = cmd.Run()
}

// waitSettled 轮询到非 scheduled 的终态；一直是 scheduled 就失败。
func waitSettled(t *testing.T, svc *Service, sessionID string) *Cleanup {
	t.Helper()
	deadline := time.Now().Add(archiveWait)
	for {
		cleanup, ok := svc.CleanupOf(sessionID, nil)
		if ok && cleanup.Status != CleanupScheduled {
			return cleanup
		}
		if time.Now().After(deadline) {
			if ok {
				t.Fatalf("%s 的清理停在 %s（期望终态）", sessionID, cleanup.Status)
			}
			t.Fatalf("%s 没有清理尝试（期望有）", sessionID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustExist(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%s 应仍存在：%s（%v）", what, path, err)
	}
}

func mustGone(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return
	} else if err != nil {
		t.Fatalf("%s 状态不可判定：%s（%v）", what, path, err)
	}
	t.Fatalf("%s 应已被移除：%s", what, path)
}

func mustBranchKept(t *testing.T, repo, branch string) {
	t.Helper()
	if got := gitRun(t, repo, "branch", "--list", branch); !strings.Contains(got, branch) {
		t.Fatalf("分支 %s 必须永远保留，实际 %q", branch, got)
	}
}

// ---------------------------------------------------------------------------
// 1. 干净 checkout → removed，分支保留
// ---------------------------------------------------------------------------

func TestArchiveCleanupRemovesCleanWorktree(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "archive-clean"
	branch := "dsh-aaaaaaaa-0001"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	svc.SetArchiveHost(permissiveHost())

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	cleanup := waitSettled(t, svc, session)
	if cleanup.Status != CleanupRemoved {
		t.Fatalf("干净 checkout 的清理状态 = %s，期望 removed", cleanup.Status)
	}
	if cleanup.Revision < 1 {
		t.Fatalf("revision = %d，期望从 1 开始递增", cleanup.Revision)
	}
	mustGone(t, dir, "checkout 目录")
	if got := worktreeRegistrations(t, repo, dir); got != 0 {
		t.Fatalf("git 注册应已消失，实际还有 %d 条", got)
	}
	mustBranchKept(t, repo, branch)
}

// ---------------------------------------------------------------------------
// 2. 未跟踪文件 → retained
// ---------------------------------------------------------------------------

func TestArchiveCleanupRetainsDirtyWorktree(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "archive-dirty"
	branch := "dsh-bbbbbbbb-0002"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	svc.SetArchiveHost(permissiveHost())

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	cleanup := waitSettled(t, svc, session)
	if cleanup.Status != CleanupRetained {
		t.Fatalf("有未跟踪文件的 checkout 状态 = %s，期望 retained", cleanup.Status)
	}
	mustExist(t, dir, "dirty checkout 目录")
	mustBranchKept(t, repo, branch)
}

// ---------------------------------------------------------------------------
// 3. locked → retained
// ---------------------------------------------------------------------------

func TestArchiveCleanupRetainsLockedWorktree(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "archive-locked"
	branch := "dsh-cccccccc-0003"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	gitRun(t, repo, "worktree", "lock", dir)
	t.Cleanup(func() { gitQuiet(repo, "worktree", "unlock", dir) })
	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	svc.SetArchiveHost(permissiveHost())

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	cleanup := waitSettled(t, svc, session)
	if cleanup.Status != CleanupRetained {
		t.Fatalf("locked checkout 状态 = %s，期望 retained", cleanup.Status)
	}
	mustExist(t, dir, "locked checkout 目录")
	mustBranchKept(t, repo, branch)
}

// ---------------------------------------------------------------------------
// 4. retainOnArchive → 不排期
// ---------------------------------------------------------------------------

func TestArchiveCleanupRetainsOnPolicy(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "archive-policy"
	branch := "dsh-dddddddd-0004"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	host := permissiveHost()
	host.RetainOnArchive = func() bool { return true }
	svc.SetArchiveHost(host)

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("retainOnArchive 打开时不应报错（只是不排期）: %v", err)
	}
	if cleanup, ok := svc.CleanupOf(session, nil); ok || cleanup != nil {
		t.Fatalf("retainOnArchive 打开时不应有 attempt，实际 %+v ok=%v", cleanup, ok)
	}
	mustExist(t, dir, "保留策略下的 checkout 目录")
	mustBranchKept(t, repo, branch)
}

// ---------------------------------------------------------------------------
// 5. 创建者只读 → retained
// ---------------------------------------------------------------------------

func TestArchiveCleanupRetainsReadOnlyCreator(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "archive-readonly"
	branch := "dsh-eeeeeeee-0005"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	host := permissiveHost()
	host.ReadOnly = func(string) bool { return true }
	svc.SetArchiveHost(host)

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	cleanup := waitSettled(t, svc, session)
	if cleanup.Status != CleanupRetained {
		t.Fatalf("只读创建者的状态 = %s，期望 retained", cleanup.Status)
	}
	mustExist(t, dir, "只读会话的 checkout 目录")
	mustBranchKept(t, repo, branch)
}

// ---------------------------------------------------------------------------
// 6. 还有未归档会话引用（false 与 error）→ retained
// ---------------------------------------------------------------------------

func TestArchiveCleanupRetainsWhenStillUsed(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "archive-used"
	branch := "dsh-ffffffff-0006"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)

	var mu sync.Mutex
	unused := false
	var unusedErr error
	svc.SetArchiveHost(ArchiveHost{
		RetainOnArchive: func() bool { return false },
		ReadOnly:        func(string) bool { return false },
		Unused: func(string, string) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return unused, unusedErr
		},
	})

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	first := waitSettled(t, svc, session)
	if first.Status != CleanupRetained {
		t.Fatalf("仍有会话引用时状态 = %s，期望 retained", first.Status)
	}

	// 宿主拿不到答案（error）必须当"仍被使用"，不许乐观删除。
	mu.Lock()
	unused, unusedErr = false, errors.New("会话查询失败")
	mu.Unlock()
	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("第二次 ScheduleArchiveCleanup: %v", err)
	}
	second := waitSettled(t, svc, session)
	if second.Status != CleanupRetained {
		t.Fatalf("Unused 返回 error 时状态 = %s，期望 retained", second.Status)
	}
	if second.Revision <= first.Revision {
		t.Fatalf("revision 必须单调递增：第一次 %d，第二次 %d", first.Revision, second.Revision)
	}
	mustExist(t, dir, "仍被引用的 checkout 目录")
	mustBranchKept(t, repo, branch)
}

// ---------------------------------------------------------------------------
// 7. Git 拒绝移除 → failed（真实错误路径）
// ---------------------------------------------------------------------------

func TestArchiveCleanupFailedReportsStructured(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "archive-failed"
	branch := "dsh-11111111-0007"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)

	// 现场：checkout 里提交了一个"嵌入式仓库"（gitlink）。git status 仍然干净，
	// 所以 inspection 全过；但 git worktree remove 会真实地拒绝：
	//   fatal: working trees containing submodules cannot be moved or removed
	// 这正是官方"提交过的嵌入式仓库 → failed"的现场。
	nested := filepath.Join(dir, "embedded")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "x.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, nested, "init", "-q")
	gitRun(t, nested, "add", "x.txt")
	gitRun(t, nested, "commit", "-q", "-m", "nested init")
	gitRun(t, dir, "add", "embedded")
	gitRun(t, dir, "commit", "-q", "-m", "embedded repo")
	// 前置：git status 干净（否则走的是 retained，不是 failed）。
	if changed := gitRun(t, dir, "status", "--porcelain=v1", "--untracked-files=all"); changed != "" {
		t.Fatalf("前置失败：现场必须干净才能走到 remove，实际 %q", changed)
	}

	recordCreated(t, store, session, repo, dir, branch, baseCommit)
	svc.SetArchiveHost(permissiveHost())
	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	cleanup := waitSettled(t, svc, session)
	if cleanup.Status != CleanupFailed {
		t.Fatalf("Git 拒绝移除时应是 failed，实际 %s", cleanup.Status)
	}
	// 失败也要有终态可读（不是停在 scheduled），目录与分支都保留。
	if cleanup.Status == CleanupScheduled {
		t.Fatal("failed 必须是终态")
	}
	mustExist(t, dir, "失败的 checkout 目录")
	if got := worktreeRegistrations(t, repo, dir); got != 1 {
		t.Fatalf("失败的移除必须原样留下注册，实际 %d 条", got)
	}
	mustBranchKept(t, repo, branch)
}

// ---------------------------------------------------------------------------
// 8. CleanupOf(&revision) 等待结算
// ---------------------------------------------------------------------------

func TestCleanupOfWaitsOnRevision(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "archive-wait"
	branch := "dsh-22222222-0008"
	dir, baseCommit := addArchiveWorktree(t, repo, branch)
	recordCreated(t, store, session, repo, dir, branch, baseCommit)

	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	host := permissiveHost()
	host.Unused = func(string, string) (bool, error) {
		once.Do(func() { close(started) })
		<-release
		return true, nil
	}
	// 宿主时钟注入必须被真的用上（attempt 的排期/结算时间戳）。
	fixedClock := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	host.Now = func() time.Time { return fixedClock }
	svc.SetArchiveHost(host)

	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("后台 inspection 没有开始")
	}
	// 排期后立刻读得到 scheduled（后台被 Unused 卡住，状态可确定）。
	pending, ok := svc.CleanupOf(session, nil)
	if !ok || pending == nil || pending.Status != CleanupScheduled {
		t.Fatalf("排期后应立刻读到 scheduled，实际 %+v ok=%v", pending, ok)
	}
	revision := pending.Revision

	settled := make(chan *Cleanup, 1)
	go func() {
		cleanup, ok := svc.CleanupOf(session, &revision)
		if !ok {
			settled <- nil
			return
		}
		settled <- cleanup
	}()
	select {
	case cleanup := <-settled:
		t.Fatalf("CleanupOf(&revision) 必须等尝试结算，实际立刻返回 %+v", cleanup)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case cleanup := <-settled:
		if cleanup == nil {
			t.Fatal("等待中的 revision 应返回终态，实际没有尝试")
		}
		if cleanup.Revision != revision || cleanup.Status != CleanupRemoved {
			t.Fatalf("等待结果 = %+v，期望 revision=%d removed", cleanup, revision)
		}
	case <-time.After(archiveWait):
		t.Fatal("等待终态超时")
	}
	mustGone(t, dir, "checkout 目录")

	svc.reclaim.mu.Lock()
	attempt := svc.reclaim.attempts[session]
	startedAt, settledAt := time.Time{}, time.Time{}
	if attempt != nil {
		startedAt, settledAt = attempt.startedAt, attempt.settledAt
	}
	svc.reclaim.mu.Unlock()
	if !startedAt.Equal(fixedClock) || !settledAt.Equal(fixedClock) {
		t.Fatalf("宿主时钟没被用上：startedAt=%v settledAt=%v，期望都是 %v", startedAt, settledAt, fixedClock)
	}
}

// ---------------------------------------------------------------------------
// 9. plugin:worktree/created 折叠
// ---------------------------------------------------------------------------

func TestCreatedRecordFolded(t *testing.T) {
	store := newMemStore()
	first := "/repo/.agents/worktrees/dsh-33333333-0009"
	second := "/repo/.agents/worktrees/dsh-44444444-0010"
	store.append("fold", EventTypeCreated, Creation{Path: first, Branch: "dsh-33333333-0009", BaseCommit: "aaaa", RepositoryRoot: "/repo"})
	// 同一 path 重复出现（分隔符不同）只保留一条。
	store.append("fold", EventTypeCreated, Creation{Path: `\repo\.agents\worktrees\dsh-33333333-0009`, Branch: "dsh-33333333-0009", BaseCommit: "aaaa", RepositoryRoot: "/repo"})
	store.append("fold", EventTypeCreated, Creation{Path: second, Branch: "dsh-44444444-0010", BaseCommit: "bbbb", RepositoryRoot: "/repo"})
	// 载荷不合格（缺字段）跳过。
	store.append("fold", EventTypeCreated, map[string]string{"path": "/repo/.agents/worktrees/broken"})
	// 旧的 plugin:client-ui-forge/worktree 记录行为不变。
	store.append("fold", EventType, Record{
		Type: RecordCreated, Branch: "legacy", BaseCommit: "cccc",
		Directory: "/repo/.agents/worktrees/legacy", SourceRoot: "/repo",
	})

	snapshot, err := store.Load("fold")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(snapshot.Creations) != 2 {
		t.Fatalf("Creations = %d 条（%+v），期望 2 条（去重 + 跳过坏载荷）", len(snapshot.Creations), snapshot.Creations)
	}
	if snapshot.Creations[0].Path != first || snapshot.Creations[1].Path != second {
		t.Fatalf("Creations 顺序 = %+v，期望按出现顺序", snapshot.Creations)
	}
	got := snapshot.Creations[1]
	if got.Branch != "dsh-44444444-0010" || got.BaseCommit != "bbbb" || got.RepositoryRoot != "/repo" {
		t.Fatalf("Creation 字段 = %+v", got)
	}
	// 旧记录语义不变。
	if snapshot.Created == nil || snapshot.Created.Branch != "legacy" {
		t.Fatalf("旧 created 记录的折叠结果变了：%+v", snapshot.Created)
	}
	if snapshot.Branch != "legacy" || snapshot.BaseCommit != "cccc" || snapshot.SourceRoot != "/repo" {
		t.Fatalf("旧记录折叠出来的字段变了：%+v", snapshot)
	}

	// 只有旧记录时 Creations 必须为空（release 前创建的 checkout 永不被扫）。
	legacy := newMemStore()
	legacy.append("legacy", EventType, Record{Type: RecordCreated, Branch: "old", Directory: "/repo/.agents/worktrees/old", SourceRoot: "/repo"})
	oldSnapshot, err := legacy.Load("legacy")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(oldSnapshot.Creations) != 0 {
		t.Fatalf("旧记录不应折出 Creation（否则会把 release 前的 checkout 扫掉）：%+v", oldSnapshot.Creations)
	}
}

// ---------------------------------------------------------------------------
// 10. create() 真的落官方记录 → 归档时清掉（A+B 端到端）
// ---------------------------------------------------------------------------

func TestCreateThenArchiveRemovesWorktree(t *testing.T) {
	repo := canonicalRepo(t)
	svc, store := newArchiveService(t)
	const session = "archive-end-to-end"
	store.header[session] = repo
	if err := svc.RequestWorktree(session, nil); err != nil {
		t.Fatalf("RequestWorktree: %v", err)
	}
	if err := svc.PreStep(context.Background(), session, 1); err != nil {
		t.Fatalf("PreStep: %v", err)
	}
	state := mustState(t, svc, session)
	if state.Status != StatusCreated {
		t.Fatalf("创建后状态 = %s，期望 created", state.Status)
	}
	snapshot, err := store.Load(session)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(snapshot.Creations) != 1 {
		t.Fatalf("create() 应落 1 条 plugin:worktree/created，实际 %+v", snapshot.Creations)
	}
	creation := snapshot.Creations[0]
	if creation.Path == "" || creation.Branch != state.Branch || creation.BaseCommit == "" || creation.RepositoryRoot == "" {
		t.Fatalf("created 记录字段不全：%+v", creation)
	}
	// data 形状必须**恰好**是官方 CreatedWorktree 的四个字段（官方 zod 是 .strict()）。
	found := 0
	for _, event := range store.events[session] {
		if event.Type != EventTypeCreated {
			continue
		}
		found++
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatalf("created 记录 data 不是 JSON 对象: %v", err)
		}
		if len(payload) != 4 {
			t.Fatalf("created 记录 data 字段 = %v，官方形状只有 path/branch/baseCommit/repositoryRoot", payload)
		}
		for _, key := range []string{"path", "branch", "baseCommit", "repositoryRoot"} {
			if _, ok := payload[key]; !ok {
				t.Fatalf("created 记录 data 缺 %q：%v", key, payload)
			}
		}
	}
	if found != 1 {
		t.Fatalf("create() 应写 1 条 %s，实际 %d 条", EventTypeCreated, found)
	}

	svc.SetArchiveHost(permissiveHost())
	if err := svc.ScheduleArchiveCleanup(session); err != nil {
		t.Fatalf("ScheduleArchiveCleanup: %v", err)
	}
	cleanup := waitSettled(t, svc, session)
	if cleanup.Status != CleanupRemoved {
		t.Fatalf("端到端清理状态 = %s，期望 removed", cleanup.Status)
	}
	mustGone(t, creation.Path, "checkout 目录")
	mustBranchKept(t, repo, state.Branch)
}

// ---------------------------------------------------------------------------
// 11. 旧记录（release 前创建）不排期
// ---------------------------------------------------------------------------

// legacyStore 只暴露 Store 的三个方法：模拟没有 AppendCreated 扩展的宿主 store。
type legacyStore struct{ Store }

func TestArchiveCleanupSkipsLegacyCreation(t *testing.T) {
	repo := canonicalRepo(t)
	base := newMemStore()
	base.header["legacy-session"] = repo
	svc := NewService(&legacyStore{Store: base}, ExecGit{Timeout: 60 * time.Second})
	t.Cleanup(svc.Close)

	if err := svc.RequestWorktree("legacy-session", nil); err != nil {
		t.Fatalf("RequestWorktree: %v", err)
	}
	if err := svc.PreStep(context.Background(), "legacy-session", 1); err != nil {
		t.Fatalf("PreStep: %v", err)
	}
	dir := mustState(t, svc, "legacy-session").Directory
	snapshot, err := base.Load("legacy-session")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snapshot.Created == nil {
		t.Fatal("前置：旧 created 记录应存在")
	}
	if len(snapshot.Creations) != 0 {
		t.Fatalf("宿主没有 AppendCreated 时不应有 Creation：%+v", snapshot.Creations)
	}

	svc.SetArchiveHost(permissiveHost())
	if err := svc.ScheduleArchiveCleanup("legacy-session"); err != nil {
		t.Fatalf("没有创建记录时 ScheduleArchiveCleanup 不应报错: %v", err)
	}
	if cleanup, ok := svc.CleanupOf("legacy-session", nil); ok || cleanup != nil {
		t.Fatalf("没有创建记录时不应排期，实际 %+v ok=%v", cleanup, ok)
	}
	mustExist(t, dir, "release 前的 checkout 目录")
}
