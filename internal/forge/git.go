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
	// MaxUntrackedFiles 对应官方 maxUntrackedFiles；零值使用官方默认 100。
	MaxUntrackedFiles int
	// TestPaths / InterfacePaths 是官方 Forge 配置的评审分层 glob，先于内置规则。
	TestPaths      []string
	InterfacePaths []string
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
		// %w 而非 %v：`*exec.ExitError` 必须留在链上，判"没有 fork point"这类
		// 分支要看真实退出码，不能去匹配 "exit status N" 文本。
		return out.String(), errb.String(), fmt.Errorf("git %s 失败: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
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
	Layer        string  `json:"layer"`
	Digest       string  `json:"digest"`
}

type LocalDiff struct {
	Base    *string    `json:"base"`
	Files   []DiffFile `json:"files"`
	Omitted int        `json:"omitted"`
}

// CommitRange 是官方 branch scope 的 commits:{from,to}，两端必须是完整对象名。
type CommitRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Scope 是官方 ForgeLocalScope。Base / Commits 仅在 branch 范围使用。
type Scope struct {
	Kind    string
	Base    *string
	Commits *CommitRange
}

func splitZ(s string) []string { return strings.Split(s, "\x00") }

// LocalText 返回 scope 下的真实文件文本：staged 用索引，branch 选定提交范围用
// commits.to，其余（包括完整 branch 范围）读工作树；路径必须在仓库内。
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
		if scope.Commits != nil {
			if !fullCommit(scope.Commits.To) {
				return "", ErrCommitsMissing
			}
			out, _, err := e.run(ctx, root, "show", scope.Commits.To+":"+clean)
			return out, err
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(clean)))
		return string(data), err
	case "uncommitted", "unstaged":
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(clean)))
		return string(data), err
	default:
		return "", fmt.Errorf("forge: 未知 text scope %q", scope.Kind)
	}
}
