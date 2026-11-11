package forge

// rc.2 `diffBases` / `localCommits` 的**真实 git** 测试。
//
// 这两条读的是会话当前 checkout，所以"真值"只能由 git 子进程产生。测试用
// `t.TempDir()` 建真仓库（不联网、不依赖宿主），逐条对齐官方 local-diff.ts：
//   - forkPoint/chosenForkPoint（115-155）
//   - baseBranches（159-180）+ parse.ts parseBaseBranches（47-70）
//   - localCommits（337-343）

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitInit 建一个真仓库并返回它的路径，默认分支 main。
func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// runGit 在 dir 里跑一条 git，失败即 t.Fatal（返回 trim 过的 stdout）。
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitFile 写一个文件并提交，返回 commit sha。
func commitFile(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// newExecutor 造一个真实 git 执行器（超时给足，Windows 上首次调用较慢）。
func newExecutor() *Executor {
	// Timeout 必须显式给：Executor.run 直接用 context.WithTimeout(ctx, e.Timeout)，
	// 零值等于"立刻超时"（默认值只在 NewExecutor 里填）。
	return &Executor{GitPath: "git", Timeout: 60 * time.Second}
}

// TestBaseBranchesRealRepo 钉住：local 不含当前分支、remote 不含 symbolic ref
// （origin/HEAD）、automatic 是远端默认分支的 merge-base。
func TestBaseBranchesRealRepo(t *testing.T) {
	dir := gitInit(t)
	base := commitFile(t, dir, "a.txt", "1\n", "first")
	// 造一个远端默认分支的引用：`git remote` 必须能看到 origin（ForkPoint 按
	// remote 列表逐个试 symbolic-ref），所以先加一个**不会联网**的 remote URL。
	runGit(t, dir, "remote", "add", "origin", "https://example.invalid/o/r.git")
	runGit(t, dir, "update-ref", "refs/remotes/origin/main", base)
	runGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	// 当前分支 main 上再提交一个，然后开 feature 分支并切回来前的状态。
	commitFile(t, dir, "b.txt", "2\n", "second")
	runGit(t, dir, "branch", "feature")

	result, err := newExecutor().BaseBranches(context.Background(), dir, "", 50, nil)
	if err != nil {
		t.Fatalf("BaseBranches: %v", err)
	}
	names := func(refs []BaseRef) []string {
		out := make([]string, 0, len(refs))
		for _, ref := range refs {
			out = append(out, ref.Name)
		}
		return out
	}
	local := names(result.Local)
	remote := names(result.Remote)
	if len(local) != 1 || local[0] != "feature" {
		t.Fatalf("local 应只有 feature（当前分支 main 被排除），实际 %v", local)
	}
	if len(remote) != 1 || remote[0] != "origin/main" {
		t.Fatalf("remote 应只有 origin/main（origin/HEAD 是 symbolic ref，必须跳过），实际 %v", remote)
	}
	for _, ref := range result.Local {
		if ref.Ref != "refs/heads/feature" {
			t.Fatalf("local ref 必须是全名：%v", ref.Ref)
		}
	}
	if result.Automatic == nil {
		t.Fatalf("有远端默认分支时应给出 automatic fork point")
	}
	if result.Automatic.Name != "origin/main" {
		t.Fatalf("automatic.name 应是远端默认分支，实际 %q", result.Automatic.Name)
	}
	if result.Automatic.Worktree {
		t.Fatalf("automatic.worktree 只有在会话自己建了 worktree 时才是 true")
	}
}

// TestBaseBranchesQueryFilter 钉住 query 的大小写不敏感子串过滤。
func TestBaseBranchesQueryFilter(t *testing.T) {
	dir := gitInit(t)
	commitFile(t, dir, "a.txt", "1\n", "first")
	runGit(t, dir, "branch", "feature-alpha")
	runGit(t, dir, "branch", "hotfix")

	result, err := newExecutor().BaseBranches(context.Background(), dir, "FEAT", 50, nil)
	if err != nil {
		t.Fatalf("BaseBranches: %v", err)
	}
	if len(result.Local) != 1 || result.Local[0].Name != "feature-alpha" {
		t.Fatalf("query 过滤不对：%v", result.Local)
	}
	all, err := newExecutor().BaseBranches(context.Background(), dir, "  ", 50, nil)
	if err != nil {
		t.Fatalf("BaseBranches: %v", err)
	}
	if len(all.Local) != 2 {
		t.Fatalf("空白 query 应返回全部分支，实际 %v", all.Local)
	}
}

// TestBaseBranchesLimit 钉住每组最多 limit 条（官方 maxBranches）。
func TestBaseBranchesLimit(t *testing.T) {
	dir := gitInit(t)
	commitFile(t, dir, "a.txt", "1\n", "first")
	for _, name := range []string{"b1", "b2", "b3", "b4"} {
		runGit(t, dir, "branch", name)
	}
	result, err := newExecutor().BaseBranches(context.Background(), dir, "", 2, nil)
	if err != nil {
		t.Fatalf("BaseBranches: %v", err)
	}
	if len(result.Local) != 2 {
		t.Fatalf("limit=2 时 local 应只有 2 条，实际 %d（%v）", len(result.Local), result.Local)
	}
}

// TestLocalCommitsRealRepo 钉住 oldest-first、headSha、truncated 语义。
func TestLocalCommitsRealRepo(t *testing.T) {
	dir := gitInit(t)
	fork := commitFile(t, dir, "a.txt", "1\n", "fork point")
	first := commitFile(t, dir, "b.txt", "2\n", "second")
	third := commitFile(t, dir, "c.txt", "3\n", "third")

	list, err := newExecutor().LocalCommits(context.Background(), dir, fork, 250)
	if err != nil {
		t.Fatalf("LocalCommits: %v", err)
	}
	if list.HeadSHA != third {
		t.Fatalf("headSha 应是 HEAD（%s），实际 %s", third, list.HeadSHA)
	}
	if len(list.Commits) != 2 {
		t.Fatalf("fork 之后应有 2 条提交，实际 %d（%v）", len(list.Commits), list.Commits)
	}
	if list.Commits[0].SHA != first || list.Commits[1].SHA != third {
		t.Fatalf("提交必须 oldest first：%v", list.Commits)
	}
	if list.Truncated {
		t.Fatalf("只有 2 条时不该 truncated")
	}
	if list.Commits[0].Title != "second" {
		t.Fatalf("title 应是首行：%q", list.Commits[0].Title)
	}
	if list.Commits[0].Parent == nil || *list.Commits[0].Parent != fork {
		t.Fatalf("parent 应是 fork point：%v", list.Commits[0].Parent)
	}
	if list.Commits[0].Author == nil || *list.Commits[0].Author != "Test User" {
		t.Fatalf("author 应是提交者：%v", list.Commits[0].Author)
	}
	if list.Commits[0].AuthoredAt == "" {
		t.Fatalf("authoredAt 不能为空")
	}
}

// TestLocalCommitsTruncationKeepsNewest 钉住"超限时保留最新的那几条"（官方
// `listed.slice(-limit)`），与 changeRequestCommits 的"保留最旧"相反。
func TestLocalCommitsTruncationKeepsNewest(t *testing.T) {
	dir := gitInit(t)
	fork := commitFile(t, dir, "a.txt", "1\n", "fork point")
	var shas []string
	for index := 0; index < 4; index++ {
		shas = append(shas, commitFile(t, dir, fmt.Sprintf("f%d.txt", index), "x\n", fmt.Sprintf("c%d", index)))
	}
	list, err := newExecutor().LocalCommits(context.Background(), dir, fork, 2)
	if err != nil {
		t.Fatalf("LocalCommits: %v", err)
	}
	if !list.Truncated {
		t.Fatalf("4 条 > limit=2 时必须 truncated")
	}
	if len(list.Commits) != 2 {
		t.Fatalf("limit=2 时应只有 2 条，实际 %d", len(list.Commits))
	}
	if list.Commits[0].SHA != shas[2] || list.Commits[1].SHA != shas[3] {
		t.Fatalf("truncated 时应保留最新的两条：%v", list.Commits)
	}
}

// TestLocalCommitsWithoutFork 钉住没有 fork point 的结构化错误。
func TestLocalCommitsWithoutFork(t *testing.T) {
	dir := gitInit(t)
	commitFile(t, dir, "a.txt", "1\n", "first")
	_, err := newExecutor().LocalCommits(context.Background(), dir, "", 250)
	if !errors.Is(err, ErrNoForkPoint) {
		t.Fatalf("空 fork 应回 ErrNoForkPoint，实际 %v", err)
	}
}

// TestForkPointChosenBase 钉住用户选分支时：ref 必须存在、label 是简称。
func TestForkPointChosenBase(t *testing.T) {
	dir := gitInit(t)
	fork := commitFile(t, dir, "a.txt", "1\n", "first")
	commitFile(t, dir, "b.txt", "2\n", "second")
	// release 指向 fork point（不是 HEAD）：这样 merge-base 才有区分度。
	runGit(t, dir, "branch", "release", fork)

	base := "refs/heads/release"
	commit, label, onWorktree, err := newExecutor().ForkPoint(context.Background(), dir, &base, nil)
	if err != nil {
		t.Fatalf("ForkPoint: %v", err)
	}
	if commit != fork {
		t.Fatalf("merge-base 应是 %s，实际 %s", fork, commit)
	}
	if label != "release" || onWorktree {
		t.Fatalf("label/worktree 应是 release/false，实际 %q/%v", label, onWorktree)
	}

	missing := "refs/heads/nope"
	if _, _, _, err := newExecutor().ForkPoint(context.Background(), dir, &missing, nil); !errors.Is(err, ErrBaseMissing) {
		t.Fatalf("不存在的分支应回 ErrBaseMissing，实际 %v", err)
	}

	short := "release"
	if _, _, _, err := newExecutor().ForkPoint(context.Background(), dir, &short, nil); !errors.Is(err, ErrNotBranchRef) {
		t.Fatalf("非全名 ref 应回 ErrNotBranchRef，实际 %v", err)
	}
}

// TestForkPointWorktree 钉住"会话自己建的 worktree 用它创建时的 base commit"
// （官方 worktree:true 分支，且不查 merge-base）。
func TestForkPointWorktree(t *testing.T) {
	dir := gitInit(t)
	base := commitFile(t, dir, "a.txt", "1\n", "first")
	worktreeDir := filepath.Join(dir, ".agents", "worktrees", "dsh-test")
	runGit(t, dir, "worktree", "add", "-b", "dsh-test", worktreeDir, base)
	// 在 worktree 里再提交一条：自动 fork point 仍然是创建时的 base commit，
	// 而 HEAD 已经前进——这正是官方要的"只比较自己的提交"。
	commitFile(t, worktreeDir, "b.txt", "2\n", "in worktree")

	fork := &WorktreeFork{Directory: worktreeDir, Base: "main", BaseCommit: base}
	commit, label, onWorktree, err := newExecutor().ForkPoint(context.Background(), worktreeDir, nil, fork)
	if err != nil {
		t.Fatalf("ForkPoint: %v", err)
	}
	if !onWorktree {
		t.Fatalf("会话自己的 worktree 必须是 worktree:true")
	}
	if commit != base {
		t.Fatalf("应使用创建时的 base commit %s，实际 %s", base, commit)
	}
	if label != "main" {
		t.Fatalf("label 应是创建时请求的 base 分支，实际 %q", label)
	}

	// 同一个 worktree 事实但 checkout root 不是它 → 不生效（官方判据
	// created.path === inspection.checkout.root）。
	other, _, otherWorktree, err := newExecutor().ForkPoint(context.Background(), dir, nil, fork)
	if err != nil {
		t.Fatalf("ForkPoint: %v", err)
	}
	if otherWorktree {
		t.Fatalf("root 与 worktree 目录不同时不能按 worktree fork point 处理")
	}
	if other != "" || label == "" {
		// 仓库没有远端默认分支：自动 fork point 应为空（automatic: null）。
		if other != "" {
			t.Fatalf("没有远端默认分支时不应给出自动 fork point，实际 %s", other)
		}
	}
}

// TestBaseBranchesWithoutRemote 钉住没有远端时 automatic 为 nil（不是假分支）。
func TestBaseBranchesWithoutRemote(t *testing.T) {
	dir := gitInit(t)
	commitFile(t, dir, "a.txt", "1\n", "first")
	result, err := newExecutor().BaseBranches(context.Background(), dir, "", 50, nil)
	if err != nil {
		t.Fatalf("BaseBranches: %v", err)
	}
	if result.Automatic != nil {
		t.Fatalf("没有远端默认分支时 automatic 必须是 nil，实际 %v", result.Automatic)
	}
	if result.Remote == nil || result.Local == nil {
		t.Fatalf("remote/local 必须是数组（不是 nil）：%v / %v", result.Remote, result.Local)
	}
}
