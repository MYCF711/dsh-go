// Package forge 提供本机 Git 真值（forgePanel.branches/localDiff/localText）。
//
// 不伪造分支/diff：所有结果都由真实 git 进程产生；无 repo、Git 失败、路径
// 越界都结构化返回错误。命令带取消/超时（调用方 ctx），stderr 只保留片段。
package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotRepository = errors.New("forge: 当前目录不在 Git 仓库内")
	ErrPathOutside   = errors.New("forge: 路径越出仓库")
)

const (
	defaultGitTimeout = 30 * time.Second
	stderrLimit       = 4096
)

type Executor struct {
	GitPath string
	Timeout time.Duration
}

func NewExecutor() *Executor { return &Executor{GitPath: "git", Timeout: defaultGitTimeout} }

func (e *Executor) run(ctx context.Context, dir string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.GitPath, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &limitWriter{w: &errb, n: stderrLimit}
	err := cmd.Run()
	if ctx.Err() != nil {
		return out.String(), errb.String(), fmt.Errorf("git %s 超时/取消: %w", strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		return out.String(), errb.String(), fmt.Errorf("git %s 失败: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), errb.String(), nil
}

type limitWriter struct {
	w *strings.Builder
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	orig := len(p)
	if l.n <= 0 {
		return orig, nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.w.Write(p)
	l.n -= len(p)
	return orig, nil
}

// Root 返回仓库根；不在 repo 内是 ErrNotRepository。
func (e *Executor) Root(ctx context.Context, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	out, _, err := e.run(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	return strings.TrimSpace(out), nil
}

type BranchInfo struct {
	Current  *string  `json:"current"`
	Branches []string `json:"branches"`
}

// Branches 与 git symbolic-ref/for-each-ref 逐字一致：current 为空表示 detached；
// 分支 current 在前，其余按 committerdate 倒序，最多 maxBranches。
func (e *Executor) Branches(ctx context.Context, dir string, maxBranches int) (BranchInfo, error) {
	root, err := e.Root(ctx, dir)
	if err != nil {
		return BranchInfo{}, err
	}
	if maxBranches <= 0 {
		maxBranches = 100
	}
	currentOut, _, curErr := e.run(ctx, root, "symbolic-ref", "--short", "-q", "HEAD")
	var current *string
	if curErr == nil {
		s := strings.TrimSpace(currentOut)
		if s != "" {
			current = &s
		}
	}
	out, _, err := e.run(ctx, root, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return BranchInfo{}, err
	}
	var branches []string
	if current != nil {
		branches = append(branches, *current)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || (current != nil && name == *current) {
			continue
		}
		branches = append(branches, name)
	}
	if len(branches) > maxBranches {
		branches = branches[:maxBranches]
	}
	return BranchInfo{Current: current, Branches: branches}, nil
}

type Hunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

type DiffFile struct {
	Path         string  `json:"path"`
	PreviousPath *string `json:"previousPath"`
	Status       string  `json:"status"`
	Binary       bool    `json:"binary"`
	Oversized    bool    `json:"oversized"`
	Untracked    bool    `json:"untracked"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	Hunks        []Hunk  `json:"hunks"`
}

type LocalDiff struct {
	Base    *string    `json:"base"`
	Files   []DiffFile `json:"files"`
	Omitted int        `json:"omitted"`
}

type Scope struct{ Kind string }

// LocalDiff 执行真实 git diff；untracked 文件最后追加，oversized 依 maxDiffBytes。
func (e *Executor) LocalDiff(ctx context.Context, dir string, scope Scope, maxDiffBytes int) (LocalDiff, error) {
	root, err := e.Root(ctx, dir)
	if err != nil {
		return LocalDiff{}, err
	}
	if maxDiffBytes <= 0 {
		maxDiffBytes = 1 << 20
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", "-M", "-U3", "--numstat", "-z"}
	baseArgs := []string{"diff", "--no-color", "--no-ext-diff", "-M", "-U3"}
	switch scope.Kind {
	case "branch":
		base, err := e.branchBase(ctx, root)
		if err != nil {
			return LocalDiff{}, err
		}
		args = append(args, base+"...HEAD")
		baseArgs = append(baseArgs, base+"...HEAD")
	case "staged":
		args = append(args, "--cached")
		baseArgs = append(baseArgs, "--cached")
	case "unstaged":
		// default
	case "uncommitted":
		args = append(args, "HEAD")
		baseArgs = append(baseArgs, "HEAD")
	default:
		return LocalDiff{}, fmt.Errorf("forge: 未知 diff scope %q", scope.Kind)
	}
	numOut, _, err := e.run(ctx, root, args...)
	if err != nil {
		return LocalDiff{}, err
	}
	stat := parseNumstat(numOut)
	patchOut, _, err := e.run(ctx, root, baseArgs...)
	if err != nil {
		return LocalDiff{}, err
	}
	patch := parsePatch(patchOut)
	files := make([]DiffFile, 0, len(stat))
	for _, s := range stat {
		f := DiffFile{Path: s.path, PreviousPath: s.previous, Status: s.status, Binary: s.binary, Additions: s.add, Deletions: s.del}
		if p, ok := patch[f.Path]; ok {
			f.Hunks = p
		}
		files = append(files, f)
	}
	untrackedOut, _, err := e.run(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err == nil {
		for _, name := range splitZ(untrackedOut) {
			if name == "" {
				continue
			}
			if fi, statErr := statFile(root, name); statErr == nil && fi > int64(maxDiffBytes) {
				files = append(files, DiffFile{Path: name, Status: "added", Untracked: true, Oversized: true})
			} else {
				files = append(files, DiffFile{Path: name, Status: "added", Untracked: true})
			}
		}
	}
	var basePtr *string
	if scope.Kind == "branch" {
		if b, err := e.branchBase(ctx, root); err == nil {
			basePtr = &b
		}
	}
	return LocalDiff{Base: basePtr, Files: files}, nil
}

func (e *Executor) branchBase(ctx context.Context, root string) (string, error) {
	out, _, err := e.run(ctx, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out), nil
	}
	short, _, err := e.run(ctx, root, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(short), nil
}

type numstat struct {
	path, status string
	previous     *string
	add, del     int
	binary       bool
}

func parseNumstat(out string) []numstat {
	parts := splitZ(out)
	var res []numstat
	for i := 0; i < len(parts); i++ {
		line := parts[i]
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		n := numstat{path: fields[2]}
		if fields[0] == "-" || fields[1] == "-" {
			n.binary = true
		} else {
			n.add, _ = strconv.Atoi(fields[0])
			n.del, _ = strconv.Atoi(fields[1])
		}
		n.status = "modified"
		res = append(res, n)
	}
	return res
}

func parsePatch(out string) map[string][]Hunk {
	files := map[string][]Hunk{}
	var current string
	var h *Hunk
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current = ""
			h = nil
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				current = strings.TrimPrefix(fields[3], "b/")
			}
		case strings.HasPrefix(line, "+++ b/"):
			current = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "@@"):
			h = &Hunk{}
			// @@ -oldStart,oldLines +newStart,newLines @@
			fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &h.OldStart, &h.OldLines, &h.NewStart, &h.NewLines)
			files[current] = append(files[current], *h)
		case h != nil && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "\\")):
			h.Lines = append(h.Lines, line)
			files[current][len(files[current])-1] = *h
		}
	}
	return files
}

func splitZ(s string) []string { return strings.Split(s, string(rune(0))) }

func statFile(root, name string) (int64, error) {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// LocalText 返回 scope 下的真实文件文本：staged 用索引，branch 用 HEAD，
// uncommitted/unstaged 用工作树；路径必须在仓库内。
func (e *Executor) LocalText(ctx context.Context, dir string, scope Scope, path string) (string, error) {
	root, err := e.Root(ctx, dir)
	if err != nil {
		return "", err
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", ErrPathOutside
	}
	switch scope.Kind {
	case "staged":
		out, _, err := e.run(ctx, root, "show", ":"+clean)
		return out, err
	case "branch":
		out, _, err := e.run(ctx, root, "show", "HEAD:"+clean)
		return out, err
	case "uncommitted", "unstaged":
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(clean)))
		return string(data), err
	default:
		return "", fmt.Errorf("forge: 未知 text scope %q", scope.Kind)
	}
}
