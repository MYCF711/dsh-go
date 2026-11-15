package main

// workspace/changes 的 git 通道：私有对象库快照 + 两次快照之间的 numstat。
//
// 与官方 `dsh-workspace-changes/src/git.ts` 同一套做法（不是同一份实现）：
//   - 快照走**私有对象库** + 私有 index：仓库自身的 objects/index/refs/work tree
//     一个字节都不动（`git add --all` 只写 GIT_OBJECT_DIRECTORY 指的那份，
//     `git write-tree` 只读私有 index）。仓库自己的对象库以 alternate 只读挂上，
//     所以快照里已提交内容不会重复存储。
//   - 工作树里的私有目录（home 落在仓库内时）从快照里排除，否则每次快照都会
//     把上一次的快照对象再吃进去一遍。
//
// 所有命令都有超时与输出上限：仓库异常大或 git 卡住时这一轮**记录不下来**，
// 而不是把 dshd 挂在那里。失败一律返回错误（调用方 warn 到 stderr，不静默成功）。

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

const (
	// workspaceChangesGitTimeout 是单条 git 命令的上限（官方默认 30s）。
	workspaceChangesGitTimeout = 30 * time.Second
	// workspaceChangesOutputMaxBytes 是单条 git 命令 stdout 的上限（官方 8 MiB）。
	workspaceChangesOutputMaxBytes = 8 << 20
	// workspaceChangesStderrTail 是错误里保留的 stderr 尾部。
	workspaceChangesStderrTail = 4 << 10
)

// workspaceChangesRepo 是被快照的仓库身份。
type workspaceChangesRepo struct {
	root    string
	gitDir  string
	objects string
}

// workspaceChangesGitPath 找 git 可执行文件；没有 git 就让这一轮没有快照。
func workspaceChangesGitPath() (string, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("本机没有 git，无法拍工作树快照: %w", err)
	}
	return path, nil
}

// workspaceChangesGit 跑一条 git 命令，返回 stdout 与退出码。
//
// 退出码非零**不是** Go error：调用方按命令语义判断（`git add` 的 1 = 跳过了
// 读不了的文件；`git diff` 的 1 = 有差异）。只有超时/启动失败/输出越界才是 error。
func workspaceChangesGit(ctx context.Context, gitPath, dir string, env []string, args ...string) (string, int, error) {
	runCtx, cancel := context.WithTimeout(ctx, workspaceChangesGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, gitPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_COUNT=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &boundedWriter{w: &stdout, n: workspaceChangesOutputMaxBytes}
	cmd.Stderr = &boundedWriter{w: &stderr, n: workspaceChangesStderrTail}
	err := cmd.Run()
	if runCtx.Err() != nil {
		return stdout.String(), -1, fmt.Errorf("git %s 超时/取消: %w", strings.Join(args, " "), runCtx.Err())
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return stdout.String(), -1, fmt.Errorf("git %s 无法启动: %w", strings.Join(args, " "), err)
	}
	code := 0
	if exitErr != nil {
		code = exitErr.ExitCode()
	}
	return stdout.String(), code, nil
}

// boundedWriter 是有上限的写入器（越界后丢弃、不报错）。
type boundedWriter struct {
	w *strings.Builder
	n int
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	original := len(p)
	if b.n <= 0 {
		return original, nil
	}
	if len(p) > b.n {
		p = p[:b.n]
	}
	b.w.Write(p)
	b.n -= len(p)
	return original, nil
}

// locateWorkspaceChangesRepo 定位 cwd 所属仓库；不在仓库里返回 (nil, nil)。
func locateWorkspaceChangesRepo(ctx context.Context, gitPath, cwd string) (*workspaceChangesRepo, error) {
	out, code, err := workspaceChangesGit(ctx, gitPath, cwd, nil,
		"rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-path", "objects")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		// 官方把「not a git repository」当"这个会话没有快照"，其它失败才报错。
		if code == 128 {
			return nil, nil
		}
		return nil, fmt.Errorf("git rev-parse 退出码 %d", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\r\n"), "\n")
	if len(lines) < 3 {
		return nil, fmt.Errorf("git rev-parse 输出不足三行：%q", out)
	}
	root := workspaceChangesCanonical(resolveWorkspaceChangesPath(cwd, lines[0]))
	gitDir := workspaceChangesCanonical(resolveWorkspaceChangesPath(cwd, lines[1]))
	objects := workspaceChangesCanonical(resolveWorkspaceChangesPath(cwd, lines[2]))
	return &workspaceChangesRepo{root: root, gitDir: gitDir, objects: objects}, nil
}

// workspaceChangesCanonical 把路径规范化成"同一个目录只有一种拼法"。
//
// 为什么必须有它（实测踩过）：Windows 的 `git rev-parse --show-toplevel` 会给出
// 8.3 短名（`C:\Users\ADMINI~1\…`），而 `--session` 的 cwd 是长名
// （`C:\Users\Administrator\…`）。两份都对，但 `filepath.Rel` 认不出是同一棵树，
// 于是每条 path 都变成 `..\..\..\Administrator\…` 这种垃圾，deliverables 卡片
// 的显示路径与"工作区内相对路径"同时失真。
func workspaceChangesCanonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		if abs, err := filepath.Abs(resolved); err == nil {
			return filepath.Clean(abs)
		}
		return filepath.Clean(resolved)
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

// resolveWorkspaceChangesPath 把 git 的输出解析成绝对路径（相对 cwd 时按 cwd 拼）。
func resolveWorkspaceChangesPath(cwd, line string) string {
	value := strings.TrimSpace(strings.Trim(line, `"`))
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(cwd, value))
}

// snapshotTree 把工作树（含未跟踪、不含 ignored）写成一棵**私有对象库里的**树。
func snapshotTree(ctx context.Context, gitPath string, repo workspaceChangesRepo, objectsDir string, excludes []string) (string, error) {
	scratch, err := os.MkdirTemp(filepath.Join(objectsDir, ".."), "index-")
	if err != nil {
		return "", fmt.Errorf("建快照临时目录: %w", err)
	}
	defer os.RemoveAll(scratch)

	index := filepath.Join(scratch, "index")
	// 仓库自己的 index 作为起点：`git init` 之后还没有 index 时从空开始。
	if raw, err := os.ReadFile(filepath.Join(repo.gitDir, "index")); err == nil {
		if err := os.WriteFile(index, raw, 0o600); err != nil {
			return "", fmt.Errorf("复制仓库 index: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("读仓库 index: %w", err)
	}

	env := []string{
		"GIT_OBJECT_DIRECTORY=" + objectsDir,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + repo.objects,
		"GIT_INDEX_FILE=" + index,
	}
	addArgs := []string{"add", "--all", "--ignore-errors"}
	if len(excludes) > 0 {
		addArgs = append(addArgs, "--", ".")
		for _, exclude := range excludes {
			addArgs = append(addArgs, ":(exclude)"+exclude)
		}
	}
	_, code, err := workspaceChangesGit(ctx, gitPath, repo.root, env, addArgs...)
	if err != nil {
		return "", err
	}
	// 1 = 有文件读不了，但它已经尽力写进了完整的 index（官方同判据）。
	if code != 0 && code != 1 {
		return "", fmt.Errorf("git add 退出码 %d", code)
	}
	out, code, err := workspaceChangesGit(ctx, gitPath, repo.root, env, "write-tree")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git write-tree 退出码 %d", code)
	}
	tree := strings.TrimSpace(out)
	if tree == "" {
		return "", fmt.Errorf("git write-tree 没给出树 id")
	}
	return tree, nil
}

// workspaceChangesNumstatEntry 是 `git diff-tree --numstat` 的一行。
type workspaceChangesNumstatEntry struct {
	path    string
	oldPath string
	added   int
	deleted int
	binary  bool
}

// parseWorkspaceChangesNumstat 解析 `-r -M -z --numstat` 的 NUL 分隔输出。
//
// rename 记录的形状：`added\tdeleted\t\0oldPath\0newPath\0`（第三个字段为空串）。
// 二进制文件的 added/deleted 是 `-`。
func parseWorkspaceChangesNumstat(out string) ([]workspaceChangesNumstatEntry, error) {
	if !strings.HasSuffix(out, "\x00") {
		return nil, fmt.Errorf("numstat 输出不是以 NUL 结尾（可能被截断）")
	}
	queue := strings.Split(out, "\x00")
	queue = queue[:len(queue)-1]
	entries := make([]workspaceChangesNumstatEntry, 0, len(queue))
	for len(queue) > 0 {
		record := queue[0]
		queue = queue[1:]
		first := strings.IndexByte(record, '\t')
		second := -1
		if first >= 0 {
			if idx := strings.IndexByte(record[first+1:], '\t'); idx >= 0 {
				second = first + 1 + idx
			}
		}
		if second < 0 {
			return nil, fmt.Errorf("numstat 记录形状非法: %q", record)
		}
		addedText := record[:first]
		deletedText := record[first+1 : second]
		path := record[second+1:]
		entry := workspaceChangesNumstatEntry{}
		if path == "" {
			if len(queue) < 2 {
				return nil, fmt.Errorf("numstat rename 记录不完整")
			}
			entry.oldPath = queue[0]
			entry.path = queue[1]
			queue = queue[2:]
		} else {
			entry.path = path
		}
		if addedText == "-" {
			entry.binary = true
		} else {
			added, err := strconv.Atoi(addedText)
			if err != nil {
				return nil, fmt.Errorf("numstat added 字段非法 %q", addedText)
			}
			deleted, err := strconv.Atoi(deletedText)
			if err != nil {
				return nil, fmt.Errorf("numstat deleted 字段非法 %q", deletedText)
			}
			entry.added, entry.deleted = added, deleted
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
