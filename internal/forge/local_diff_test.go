package forge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func localDiffGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func localDiffWrite(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func localDiffRepo(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	localDiffGit(t, root, "init", "-q", "-b", "main")
	localDiffGit(t, root, "config", "user.email", "forge@example.com")
	localDiffGit(t, root, "config", "user.name", "Forge Test")
	localDiffWrite(t, root, "a.go", "package a\n")
	localDiffWrite(t, root, "old.txt", "delete me\n")
	localDiffWrite(t, root, "before.txt", "same rename body\n")
	localDiffWrite(t, root, attributesPath, "a.go dsh-review-layer=interface\nafter_test.go dsh-review-layer=implementation\n")
	localDiffGit(t, root, "add", ".")
	localDiffGit(t, root, "commit", "-qm", "base")
	base := localDiffGit(t, root, "rev-parse", "HEAD")
	localDiffGit(t, root, "remote", "add", "origin", ".")
	localDiffGit(t, root, "update-ref", "refs/remotes/origin/main", base)
	localDiffGit(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return root, base
}

func TestLocalDiffOfficialFieldsAndAutomaticFork(t *testing.T) {
	root, base := localDiffRepo(t)
	localDiffWrite(t, root, "a.go", "package a\n// changed\n")
	if err := os.Remove(filepath.Join(root, "old.txt")); err != nil {
		t.Fatal(err)
	}
	localDiffGit(t, root, "mv", "before.txt", "after_test.go")
	localDiffWrite(t, root, "new.txt", "hello\n")
	localDiffWrite(t, root, "other.txt", "omitted\n")
	executor := NewExecutor()
	executor.MaxUntrackedFiles = 1
	diff, err := executor.LocalDiff(context.Background(), root, Scope{Kind: "branch"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Base == nil || *diff.Base != "origin/main" {
		t.Fatalf("automatic base = %v", diff.Base)
	}
	if diff.Omitted != 1 {
		t.Fatalf("omitted = %d, want 1", diff.Omitted)
	}
	byPath := map[string]DiffFile{}
	for _, file := range diff.Files {
		byPath[file.Path] = file
		if len(file.Digest) != 32 || file.Layer == "" {
			t.Fatalf("必填 layer/digest 缺失：%+v", file)
		}
	}
	if got := byPath["a.go"]; got.Status != "modified" || got.Layer != "interface" || got.Additions != 1 || got.Deletions != 0 {
		t.Fatalf("modified/layer = %+v", got)
	}
	if got := byPath["old.txt"]; got.Status != "deleted" {
		t.Fatalf("deleted = %+v", got)
	}
	renamed := byPath["after_test.go"]
	if renamed.Status != "renamed" || renamed.PreviousPath == nil || *renamed.PreviousPath != "before.txt" {
		t.Fatalf("renamed = %+v", renamed)
	}
	// 仓库属性优先于内置 *_test.go → test。
	if renamed.Layer != "implementation" {
		t.Fatalf(".gitattributes 应覆盖内置 test layer：%+v", renamed)
	}
	untracked := 0
	for _, file := range diff.Files {
		if file.Untracked {
			untracked++
			if file.Status != "added" || file.Additions != 1 || len(file.Hunks) != 1 {
				t.Fatalf("untracked 全文/hunk = %+v", file)
			}
		}
	}
	if untracked != 1 {
		t.Fatalf("maxUntrackedFiles=1，实际列出 %d", untracked)
	}
	again, err := executor.LocalDiff(context.Background(), root, Scope{Kind: "branch"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range again.Files {
		if before := byPath[file.Path].Digest; before != file.Digest {
			t.Fatalf("同一比较 digest 必须稳定：%s %s → %s", file.Path, before, file.Digest)
		}
	}

	// 显式 base 走 chosenForkPoint，label 去掉 refs/heads/。
	localDiffGit(t, root, "branch", "base", base)
	chosen := "refs/heads/base"
	selected, err := executor.LocalDiff(context.Background(), root, Scope{Kind: "branch", Base: &chosen}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Base == nil || *selected.Base != "base" {
		t.Fatalf("chosen base = %v", selected.Base)
	}

	// 没有远端默认分支时，创建记录仍给出真实 worktree fork point。
	localDiffGit(t, root, "update-ref", "-d", "refs/remotes/origin/HEAD")
	localDiffGit(t, root, "update-ref", "-d", "refs/remotes/origin/main")
	if _, err := executor.LocalDiff(context.Background(), root, Scope{Kind: "branch"}, 0); !errors.Is(err, ErrNoForkPoint) {
		t.Fatalf("无 fork point 必须明确失败：%v", err)
	}
	resolvedRoot, rootErr := executor.Root(context.Background(), root)
	if rootErr != nil || !samePath(root, resolvedRoot) {
		t.Fatalf("worktree 路径比较失败：root=%q resolved=%q err=%v", root, resolvedRoot, rootErr)
	}
	created, err := executor.LocalDiff(context.Background(), root, Scope{Kind: "branch"}, 0, &WorktreeFork{Directory: root, Base: "main", BaseCommit: base})
	if err != nil {
		t.Fatal(err)
	}
	if created.Base == nil || *created.Base != "main" {
		t.Fatalf("worktree fork = %v", created.Base)
	}
}

func TestLocalDiffChosenCommitsAndLocalText(t *testing.T) {
	root := t.TempDir()
	localDiffGit(t, root, "init", "-q", "-b", "main")
	localDiffGit(t, root, "config", "user.email", "forge@example.com")
	localDiffGit(t, root, "config", "user.name", "Forge Test")
	localDiffWrite(t, root, "a.txt", "one\n")
	localDiffGit(t, root, "add", ".")
	localDiffGit(t, root, "commit", "-qm", "base")
	base := localDiffGit(t, root, "rev-parse", "HEAD")
	localDiffGit(t, root, "remote", "add", "origin", ".")
	localDiffGit(t, root, "update-ref", "refs/remotes/origin/main", base)
	localDiffGit(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	localDiffWrite(t, root, "a.txt", "two\n")
	localDiffGit(t, root, "commit", "-qam", "second")
	second := localDiffGit(t, root, "rev-parse", "HEAD")
	localDiffWrite(t, root, "a.txt", "three\n")
	localDiffGit(t, root, "commit", "-qam", "third")
	third := localDiffGit(t, root, "rev-parse", "HEAD")
	executor := NewExecutor()
	scope := Scope{Kind: "branch", Commits: &CommitRange{From: second, To: third}}
	diff, err := executor.LocalDiff(context.Background(), root, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Additions != 1 || diff.Files[0].Deletions != 1 {
		t.Fatalf("commits diff = %+v", diff.Files)
	}
	text, err := executor.LocalText(context.Background(), root, scope, "a.txt")
	if err != nil || text != "three\n" {
		t.Fatalf("chosen commits localText = %q, %v", text, err)
	}
	localDiffWrite(t, root, "a.txt", "working\n")
	text, err = executor.LocalText(context.Background(), root, Scope{Kind: "branch"}, "a.txt")
	if err != nil || text != "working\n" {
		t.Fatalf("完整 branch 必须读工作树：%q, %v", text, err)
	}
	bad := Scope{Kind: "branch", Commits: &CommitRange{From: "not-a-sha", To: third}}
	if _, err := executor.LocalDiff(context.Background(), root, bad, 0); !errors.Is(err, ErrCommitsMissing) {
		t.Fatalf("非法 commit 必须 ErrCommitsMissing：%v", err)
	}
}

func TestUntrackedDigestMatchesOfficialFixture(t *testing.T) {
	file := makeUntrackedDiff("new.txt", []byte("hello\n"), false)
	// 由官方 diff.ts:66 的 JSON.stringify + SHA-256 公式独立计算。
	const official = "6e0cc3fa67d7729815797955696d2cb2"
	if file.Digest != official {
		t.Fatalf("digest = %s，官方夹具 = %s", file.Digest, official)
	}
	binary := makeUntrackedDiff("bin.dat", []byte{0, 1, 2}, false)
	if !binary.Binary || binary.Digest == "" || len(binary.Hunks) != 0 {
		t.Fatalf("binary untracked = %+v", binary)
	}
	oversized := makeUntrackedDiff("large.txt", nil, true)
	if !oversized.Oversized || oversized.Digest == "" {
		t.Fatalf("oversized untracked = %+v", oversized)
	}
}

func TestBinaryDigestIncludesBlobNames(t *testing.T) {
	one, ok := parseDiffSection(strings.Split("diff --git a/bin.dat b/bin.dat\nindex 1111111..2222222 100644\nGIT binary patch\n", "\n"))
	if !ok || !one.Binary || one.Status != "modified" || one.Digest == "" {
		t.Fatalf("binary patch = %+v, %v", one, ok)
	}
	deleted, _ := parseDiffSection(strings.Split("diff --git a/gone.dat b/gone.dat\ndeleted file mode 100644\nindex 1111111..0000000\nGIT binary patch\n", "\n"))
	if deleted.Status != "deleted" {
		t.Fatalf("无 ---/+++ 的 binary deleted 被误判：%+v", deleted)
	}
	two, _ := parseDiffSection(strings.Split("diff --git a/bin.dat b/bin.dat\nindex 1111111..3333333 100644\nGIT binary patch\n", "\n"))
	if one.Digest == two.Digest {
		t.Fatal("binary blob 名变化必须改变 digest")
	}
}

func TestBuiltInReviewLayers(t *testing.T) {
	executor := NewExecutor()
	files := []DiffFile{{Path: "pkg/a_test.go"}, {Path: "go.mod"}, {Path: "openapi.yaml"}, {Path: "pkg/a.go"}}
	executor.classifyDiffFiles(files, "")
	want := []string{"test", "interface", "interface", "implementation"}
	for i := range files {
		if files[i].Layer != want[i] {
			t.Fatalf("%s layer = %s，want %s", files[i].Path, files[i].Layer, want[i])
		}
	}
}
