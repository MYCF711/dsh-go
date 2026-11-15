package main

// 反归档取消点（官方 `dsh-experimental-worktree/src/archive.ts:87-93`）的宿主侧用例。
//
// 官方顺序是：`workspace/session-unarchiving` 广播 → 监听者置 cancelled → await
// 结算 → 收走 attempt；然后 workspace 才 `setState` 把 id 移出归档集。本仓的接法
// （`officialWorkspaceHost.unarchiveSession` 第 2.5 步）：
//
//	projectWorkspaceEvent(…Unarchiving) → cancelArchivedCleanup → withDocumentWrite → restoreArchivedWorktree
//
// 这里只钉宿主侧的**时序与如实报告**（cancel 在落盘之前、removed 后必须走恢复、
// 恢复失败是四字段 500）；取消状态机本身的判据在
// `internal/worktree/archive_cancel_test.go`（真 git）。

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/worktree"
)

// realGitError 用**真实 git** 造一条真实错误文本（不给夹具编错误字符串）。
//
// 现场：同一个分支已经检出到另一个 worktree，`git worktree add <dir> <branch>` 会被
// git 拒绝（`fatal: '<branch>' is already used by worktree at '<other>'`）。恢复路径
// 在真机上失败时看到的就是这条。
func realGitError(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(repo, "init", "-q")
	run(repo, "config", "user.email", "g1@example.com")
	run(repo, "config", "user.name", "G1")
	run(repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "README.md")
	run(repo, "commit", "-q", "-m", "init")
	branch := "dsh-g1-real-error"
	first := filepath.Join(t.TempDir(), "checked-out-here")
	run(repo, "worktree", "add", "-b", branch, first, "HEAD")
	second := filepath.Join(t.TempDir(), "want-here")

	cmd := exec.Command("git", "worktree", "add", second, branch)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("前置失败：同一分支检出两次应被 git 拒绝，实际成功")
	}
	return strings.TrimSpace(string(out))
}

// TestOfficialWorkspaceUnarchiveCancelsInFlightCleanupBeforeArchiveSetChanges
//
// 取消点必须在**落盘移出之前**：
//   - cancel 及时（结算 retained）→ 反归档成功，**不**调 RestoreWorktree（checkout
//     一个字都没动，恢复会把 409 撞上来）；
//   - 取消调用时这条会话**仍在归档集里**（官方 index.ts:426-430 的顺序就是"广播 →
//     取消/结算 → setState"）。
func TestOfficialWorkspaceUnarchiveCancelsInFlightCleanupBeforeArchiveSetChanges(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "cancel-early")
	api := f.host.workspaceAPI()
	if _, err := api.ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}

	checkout := filepath.Join(t.TempDir(), "checkout-still-there")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("造 checkout 目录: %v", err)
	}
	fake := &fakeArchiveWorktree{
		state:        worktree.State{Status: worktree.StatusCreated, Directory: checkout},
		cancellation: worktree.CleanupCancellation{Cancelled: true, Revision: 1, Status: worktree.CleanupRetained},
	}
	f.host.worktreeOverride = fake

	set, err := api.UnarchiveSession(sessionID)
	if err != nil {
		t.Fatalf("取消及时时反归档应成功：%v", err)
	}
	if len(set) != 0 {
		t.Fatalf("反归档后归档集应为空：%v", set)
	}
	if len(fake.cancelled) != 1 || fake.cancelled[0] != sessionID {
		t.Fatalf("必须调一次 CancelArchiveCleanup(%q)，实际 %v", sessionID, fake.cancelled)
	}
	if len(fake.restored) != 0 {
		t.Fatalf("取消及时（retained）时不许走恢复路径，实际 %v", fake.restored)
	}
	requireJournalEvent(t, f.journals, sessionID, "workspace/session-unarchiving")
}

// TestOfficialWorkspaceUnarchiveCancelSeesArchivedState 钉住"取消发生在落盘之前"。
//
// 用一个假 worktree 能力在 `CancelArchiveCleanup` 里读宿主当前归档集：若取消点在
// 落盘**之后**，它读到的就是"已经不在归档集"——那 E5 的 `unused()` 判据会认为
// 会话还在使用，取消就失去意义。官方 index.ts:426-430 也是先取消、后 setState。
func TestOfficialWorkspaceUnarchiveCancelSeesArchivedState(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "cancel-order")
	api := f.host.workspaceAPI()
	if _, err := api.ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}

	var (
		mu       sync.Mutex
		archived []bool
	)
	fake := &fakeArchiveWorktree{
		state:        worktree.State{Status: worktree.StatusCreated, Directory: filepath.Join(t.TempDir(), "gone")},
		cancellation: worktree.CleanupCancellation{Cancelled: true, Revision: 1, Status: worktree.CleanupRemoved},
	}
	probe := &probeWorktree{fake: fake, observe: func() bool {
		stillArchived, err := f.host.archivedLocally(sessionID)
		if err != nil {
			return false
		}
		mu.Lock()
		archived = append(archived, stillArchived)
		mu.Unlock()
		return stillArchived
	}}
	f.host.worktreeOverride = probe

	if _, err := api.UnarchiveSession(sessionID); err != nil {
		t.Fatalf("unarchiveSession: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(archived) != 1 {
		t.Fatalf("取消点应被调用 1 次，实际 %d 次", len(archived))
	}
	if !archived[0] {
		t.Fatal("取消必须发生在落盘移出**之前**（此时 id 应仍在归档集里）")
	}
}

// probeWorktree 是包了一层"在取消时观察宿主状态"的假能力。
type probeWorktree struct {
	fake    *fakeArchiveWorktree
	observe func() bool
}

func (p *probeWorktree) State(sessionID string) (worktree.State, error) {
	return p.fake.State(sessionID)
}

func (p *probeWorktree) CancelArchiveCleanup(sessionID string) (worktree.CleanupCancellation, error) {
	p.observe()
	return p.fake.CancelArchiveCleanup(sessionID)
}

func (p *probeWorktree) RestoreWorktree(sessionID string) error {
	return p.fake.RestoreWorktree(sessionID)
}

// TestOfficialWorkspaceUnarchiveLateCancelFailureIsStructured500
//
// 取消来晚（removed）+ 恢复失败 → 反归档必须**响亮**失败：四字段 500 internal，
// 错误里带被清理的目录、真实 git 错误，并明说归档集已经变了。
func TestOfficialWorkspaceUnarchiveLateCancelFailureIsStructured500(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "cancel-late")
	if _, err := f.host.workspaceAPI().ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}

	gitMessage := realGitError(t)
	removed := filepath.Join(t.TempDir(), "checkout-removed")
	fake := &fakeArchiveWorktree{
		state:        worktree.State{Status: worktree.StatusCreated, Directory: removed, Branch: "dsh/g1"},
		cancellation: worktree.CleanupCancellation{Cancelled: true, Revision: 7, Status: worktree.CleanupRemoved},
		restoreErr: &worktree.Error{
			Code: worktree.CodeRestoreFailed, Status: 500,
			Message: "重建 worktree 失败：" + gitMessage,
		},
	}
	f.host.worktreeOverride = fake

	code, out := f.harness().call(t, "workspace.unarchiveSession", map[string]any{"sessionId": sessionID})
	if code != http.StatusInternalServerError {
		t.Fatalf("取消来晚 + 恢复失败应报 500 internal，实际 %d（%v）", code, out)
	}
	errObj, _ := out["error"].(map[string]any)
	if errObj["code"] != "internal" {
		t.Fatalf("四字段 code 应是 internal：%v", out)
	}
	message, _ := errObj["message"].(string)
	for _, want := range []string{removed, "is already used by worktree at", "已移出归档集", "取消来晚", "revision 7"} {
		if !strings.Contains(message, want) {
			t.Fatalf("错误里必须包含 %q（真实 git 错误 + 取消来晚 + 归档态已变）：%s", want, message)
		}
	}
	if len(fake.cancelled) != 1 || len(fake.restored) != 1 {
		t.Fatalf("取消与恢复都必须各走一次：cancelled=%v restored=%v", fake.cancelled, fake.restored)
	}
}

// TestOfficialWorkspaceUnarchiveCancelErrorKeepsArchivedState
//
// 取消失败 = 官方监听者拒绝 → "A rejection retains the archived state."：
// 不落盘、不恢复，500 里带真实原因。
func TestOfficialWorkspaceUnarchiveCancelErrorKeepsArchivedState(t *testing.T) {
	f := newOfficialWorkspaceEventFixture(t)
	sessionID := f.eventSession(t, "cancel-reject")
	if _, err := f.host.workspaceAPI().ArchiveSession(sessionID, false); err != nil {
		t.Fatalf("archiveSession: %v", err)
	}
	fake := &fakeArchiveWorktree{cancelErr: fmt.Errorf("worktree 服务正在停止，无法结算")}
	f.host.worktreeOverride = fake

	code, out := f.harness().call(t, "workspace.unarchiveSession", map[string]any{"sessionId": sessionID})
	if code != http.StatusInternalServerError {
		t.Fatalf("取消失败应报 500 internal，实际 %d（%v）", code, out)
	}
	errObj, _ := out["error"].(map[string]any)
	message, _ := errObj["message"].(string)
	if !strings.Contains(message, "worktree 服务正在停止") || !strings.Contains(message, "归档态保留") {
		t.Fatalf("错误里必须带真实原因并说明归档态保留：%s", message)
	}
	// 归档态必须保留（没落盘），恢复路径不该被调。
	stillArchived, err := f.host.archivedLocally(sessionID)
	if err != nil {
		t.Fatalf("archivedLocally: %v", err)
	}
	if !stillArchived {
		t.Fatal("取消失败时归档态必须保留（官方 A rejection retains the archived state.）")
	}
	if len(fake.restored) != 0 {
		t.Fatalf("取消失败时不许走恢复路径：%v", fake.restored)
	}
}
