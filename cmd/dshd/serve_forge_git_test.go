package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/forge"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func repoFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "user.email", "t@example.com")
	gitRun(t, dir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-qm", "init")
	gitRun(t, dir, "branch", "feature")
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "bin.dat")
	gitRun(t, dir, "commit", "-qm", "binary")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\nTWO\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestForgeGitBranchesDiffTextRealRepo(t *testing.T) {
	dir := repoFixture(t)
	api := newForgeGitAPI(func(string) (string, error) { return dir, nil })
	ctx := context.Background()
	raw := func(s string) []byte { return []byte(s) }

	got, err := api.HandleOfficialForgeGit(ctx, "forgePanel.branches", raw(`{"sessionId":"s","maxBranches":10}`))
	if err != nil {
		t.Fatal(err)
	}
	info := got.(forge.BranchInfo)
	if info.Current == nil || *info.Current != "main" {
		t.Fatalf("current=%v", info.Current)
	}
	if len(info.Branches) != 2 || info.Branches[0] != "main" || !forgeContains(info.Branches, "feature") {
		t.Fatalf("branches=%v", info.Branches)
	}

	diffAny, err := api.HandleOfficialForgeGit(ctx, "forgePanel.localDiff", raw(`{"sessionId":"s","scope":{"kind":"unstaged"},"maxDiffBytes":1024}`))
	if err != nil {
		t.Fatal(err)
	}
	diff := diffAny.(forge.LocalDiff)
	var modified, untracked *forge.DiffFile
	for i := range diff.Files {
		switch diff.Files[i].Path {
		case "a.txt":
			modified = &diff.Files[i]
		case "new.txt":
			untracked = &diff.Files[i]
		}
	}
	if modified == nil || modified.Status != "modified" || modified.Additions != 2 || modified.Deletions != 1 || len(modified.Hunks) == 0 {
		t.Fatalf("modified=%+v", modified)
	}
	if untracked == nil || !untracked.Untracked || untracked.Status != "added" {
		t.Fatalf("untracked=%+v", untracked)
	}

	textAny, err := api.HandleOfficialForgeGit(ctx, "forgePanel.localText", raw(`{"sessionId":"s","scope":{"kind":"unstaged"},"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if textAny.(string) != "one\nTWO\nthree\n" {
		t.Fatalf("text=%q", textAny)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0, 3, 4}, 0o644); err != nil {
		t.Fatal(err)
	}
	binAny, err := api.HandleOfficialForgeGit(ctx, "forgePanel.localDiff", raw(`{"sessionId":"s","scope":{"kind":"unstaged"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var binary *forge.DiffFile
	for i := range binAny.(forge.LocalDiff).Files {
		if binAny.(forge.LocalDiff).Files[i].Path == "bin.dat" {
			binary = &binAny.(forge.LocalDiff).Files[i]
		}
	}
	if binary == nil || !binary.Binary || len(binary.Hunks) != 0 {
		t.Fatalf("binary=%+v", binary)
	}
	if _, err := api.HandleOfficialForgeGit(ctx, "forgePanel.localText", raw(`{"sessionId":"s","scope":{"kind":"unstaged"},"path":"../outside"}`)); err == nil {
		t.Fatal("越界路径必须失败")
	}
}

func TestForgeGitNoRepositoryIsStructured(t *testing.T) {
	dir := t.TempDir()
	api := newForgeGitAPI(func(string) (string, error) { return dir, nil })
	_, err := api.HandleOfficialForgeGit(context.Background(), "forgePanel.branches", []byte(`{"sessionId":"s"}`))
	if err == nil || !strings.Contains(err.Error(), "仓库") {
		t.Fatalf("无 repo 必须结构化失败，得到 %v", err)
	}
}

func forgeContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
