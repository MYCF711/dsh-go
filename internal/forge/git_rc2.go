// 本机 Git 真值：rc.2 的 `forgePanel.diffBases` / `localCommits`。
//
// 这两个方法读的是**会话当前 checkout**（不是 code host），所以真值只有本机
// git 进程能给。官方实现见
// @deepseek-ai/dsh-experimental-client-ui-forge/src/local-diff.ts
//   - forkPoint            115-133（worktree 的 base commit / 远端默认分支的 merge-base）
//   - chosenForkPoint      137-155（`show-ref --verify` + merge-base）
//   - baseBranches         159-180（for-each-ref 两个前缀、去掉 symbolic ref 与当前分支）
//   - localCommits         337-343（fork..HEAD，oldest first，最多 limit 条）
//
// 与 parse.ts 的 parseBaseBranches（47-70）。
//
// 不编造分支/提交：git 失败、不在仓库内、ref 不存在都是结构化错误。
package forge

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// 官方限制（ControllerConfig 的默认值，panel.ts）。
const (
	// DefaultMaxBranches 是 diffBases 每组（remote / local）最多返回的分支数。
	DefaultMaxBranches = 50
	// DefaultMaxCommits 是 localCommits 最多返回的提交数。
	DefaultMaxCommits = 250
)

// 官方 BASE_REF_PREFIXES：local 分支在前、remote-tracking 在后。
var BaseRefPrefixes = []string{"refs/heads/", "refs/remotes/"}

// ErrNotBranchRef 表示给的分支不是 `refs/heads/*` 或 `refs/remotes/*`。
var ErrNotBranchRef = errors.New("forge: 不是分支 ref（官方只接受 refs/heads/ 或 refs/remotes/）")

// ErrBaseMissing 表示选择的分支不存在（官方 BaseMissing → forgePanel/base-missing）。
var ErrBaseMissing = errors.New("forge: 选择的分支不存在")

// ErrNoForkPoint 表示没有可用的 fork point（官方 NoForkPoint → forgePanel/no-fork-point）。
var ErrNoForkPoint = errors.New("forge: 这个 checkout 没有可与分支比较的 fork point")

// BaseRef 是 diffBases 的一个分支（full ref + 去前缀的简称）。
type BaseRef struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

// BaseBranch 是 diffBases 的 automatic fork point。
type BaseBranch struct {
	Name     string `json:"name"`
	Worktree bool   `json:"worktree"`
}

// BaseBranches 是官方 ForgeBaseBranches。
type BaseBranches struct {
	Automatic *BaseBranch `json:"automatic"`
	Remote    []BaseRef   `json:"remote"`
	Local     []BaseRef   `json:"local"`
}

// LocalCommit 是官方 ForgeCommit（localCommits 的元素）。
//
// 名字故意和 rc2.go 的 `Commit`（change request 的提交）区分：两者形状相同但
// 数据来源不同（一个是本机 HEAD 的祖先，一个是 host 的 PR 提交），混用类型
// 会让"读的是哪一边"从代码里消失。
type LocalCommit struct {
	SHA        string  `json:"sha"`
	Parent     *string `json:"parent"`
	Title      string  `json:"title"`
	Author     *string `json:"author"`
	AuthoredAt string  `json:"authoredAt"`
}

// LocalCommitList 是官方 ForgeLocalCommits。
type LocalCommitList struct {
	HeadSHA   string        `json:"headSha"`
	Commits   []LocalCommit `json:"commits"`
	Truncated bool          `json:"truncated"`
}

// WorktreeFork 是"会话当前的 checkout 就是这个会话自己建的 worktree"这一事实。
//
// 官方 forkPoint 在这个事实成立时**不用 merge-base**，而是直接用创建时的 base
// commit（worktree: true）。字段对应 worktree 服务的创建记录。
type WorktreeFork struct {
	// Directory 是 worktree 的绝对目录（用来和仓库根比）。
	Directory string
	// Base 是创建时请求的 base 分支名（可能是空/"HEAD"）。
	Base string
	// BaseCommit 是创建时解析出来的 commit。
	BaseCommit string
}

// CurrentBranch 返回 checkout 当前分支名（detached 时为 nil）。
func (e *Executor) CurrentBranch(ctx context.Context, dir string) (*string, error) {
	out, _, err := e.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// detached 不是错误：官方把 current 当 null，diffBases 就不排除任何本地分支。
		if strings.Contains(err.Error(), "exit status") {
			return nil, nil
		}
		return nil, err
	}
	name := strings.TrimSpace(out)
	if name == "" {
		return nil, nil
	}
	return &name, nil
}

// ForkPoint 解析要比较的 fork point（官方 forkPoint / chosenForkPoint）。
//
// base 为 nil 表示"自动"：先看 worktree 事实，再看远端默认分支的 merge-base。
// base 非 nil 表示用户选了分支：必须是 `refs/heads/*` 或 `refs/remotes/*`，
// 先 `show-ref --verify` 确认存在（官方用 --verify 正是为了拒绝 `@{1}` 这种
// 版本语法），再取 merge-base。
//
// 返回 (commit, label, worktree, error)：commit 为空且 error 为 nil 表示
// "没有 fork point"（官方结果 automatic: null）。
func (e *Executor) ForkPoint(ctx context.Context, root string, base *string, worktree *WorktreeFork) (string, string, bool, error) {
	if base != nil {
		ref := strings.TrimSpace(*base)
		prefix := ""
		for _, candidate := range BaseRefPrefixes {
			if strings.HasPrefix(ref, candidate) && len(ref) > len(candidate) {
				prefix = candidate
				break
			}
		}
		if prefix == "" {
			return "", "", false, fmt.Errorf("%w: %q", ErrNotBranchRef, ref)
		}
		name := ref[len(prefix):]
		if _, _, err := e.run(ctx, root, "show-ref", "--verify", "--quiet", ref); err != nil {
			return "", "", false, fmt.Errorf("%w: %s", ErrBaseMissing, name)
		}
		commit, err := e.mergeBase(ctx, root, "HEAD", ref)
		if err != nil {
			return "", "", false, err
		}
		if commit == "" {
			return "", "", false, nil
		}
		return commit, name, false, nil
	}
	// 自动：会话的 checkout 就是这个会话自己建的 worktree 时用创建时的 base commit。
	if worktree != nil && strings.TrimSpace(worktree.BaseCommit) != "" && samePath(worktree.Directory, root) {
		label := strings.TrimSpace(worktree.Base)
		if label == "" || label == "HEAD" {
			label = shortCommit(worktree.BaseCommit)
		}
		return strings.TrimSpace(worktree.BaseCommit), label, true, nil
	}
	// 否则用远端默认分支的 merge-base（官方 remoteOrder：按 git remote 的列出顺序）。
	remotes, _, err := e.run(ctx, root, "remote")
	if err != nil {
		return "", "", false, err
	}
	for _, remote := range strings.Split(strings.TrimSpace(remotes), "\n") {
		remote = strings.TrimSpace(remote)
		if remote == "" {
			continue
		}
		head, _, headErr := e.run(ctx, root, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+remote+"/HEAD")
		if headErr != nil {
			continue
		}
		branch := strings.TrimSpace(head)
		if branch == "" {
			continue
		}
		commit, mergeErr := e.mergeBase(ctx, root, "HEAD", branch)
		if mergeErr != nil {
			return "", "", false, mergeErr
		}
		if commit == "" {
			return "", "", false, nil
		}
		return commit, branch, false, nil
	}
	return "", "", false, nil
}

// mergeBase 返回两个 rev 的 merge base；没有共同祖先时返回空串（不是错误）。
func (e *Executor) mergeBase(ctx context.Context, root, a, b string) (string, error) {
	out, stderr, err := e.run(ctx, root, "merge-base", a, b)
	if err != nil {
		// 1 = 没有共同祖先；128 = 找不到对象。前者是"没有 fork point"，
		// 后者才是错误（官方同样区分 merge.exitCode）。
		if strings.Contains(err.Error(), "exit status 1") {
			return "", nil
		}
		return "", fmt.Errorf("git merge-base 失败: %v: %s", err, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(out), nil
}

// BaseBranches 列出 branch scope 能与之比较的分支（官方 baseBranches）。
//
// remote / local 各自最多 limit 条，按 committerdate 倒序（git 的 --sort 给出），
// 跳过 symbolic ref（例如 origin/HEAD）与**当前 checkout 的分支**，query 按
// 大小写不敏感的子串过滤（空串/空白 = 全要）。
func (e *Executor) BaseBranches(ctx context.Context, root, query string, limit int, worktree *WorktreeFork) (BaseBranches, error) {
	if limit <= 0 {
		limit = DefaultMaxBranches
	}
	result := BaseBranches{Remote: []BaseRef{}, Local: []BaseRef{}}
	current, err := e.CurrentBranch(ctx, root)
	if err != nil {
		return result, err
	}
	out, stderr, err := e.run(ctx, root, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%00%(symref)", BaseRefPrefixes[0], BaseRefPrefixes[1])
	if err != nil {
		return result, fmt.Errorf("git for-each-ref 失败: %v: %s", err, strings.TrimSpace(stderr))
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 2)
		ref := strings.TrimSpace(parts[0])
		if ref == "" {
			continue
		}
		if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
			continue // symbolic ref（origin/HEAD）
		}
		var group *[]BaseRef
		var name string
		switch {
		case strings.HasPrefix(ref, BaseRefPrefixes[0]):
			name = ref[len(BaseRefPrefixes[0]):]
			group = &result.Local
			if current != nil && name == *current {
				continue
			}
		case strings.HasPrefix(ref, BaseRefPrefixes[1]):
			name = ref[len(BaseRefPrefixes[1]):]
			group = &result.Remote
		default:
			continue
		}
		if name == "" || len(*group) >= limit {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(name), needle) {
			continue
		}
		*group = append(*group, BaseRef{Ref: ref, Name: name})
	}
	commit, label, onWorktree, forkErr := e.ForkPoint(ctx, root, nil, worktree)
	if forkErr != nil {
		return result, forkErr
	}
	if commit != "" {
		result.Automatic = &BaseBranch{Name: label, Worktree: onWorktree}
	}
	return result, nil
}

// LocalCommits 列出 fork..HEAD 的提交（官方 localCommits：oldest first、最多 limit 条）。
//
// truncated 表示这条分支比 limit 更长；列出的始终是**最新**的那几条（官方
// `listed.slice(-limit)`），与 changeRequestCommits 的"列出的最旧"不同。
func (e *Executor) LocalCommits(ctx context.Context, root, forkCommit string, limit int) (LocalCommitList, error) {
	if limit <= 0 {
		limit = DefaultMaxCommits
	}
	if strings.TrimSpace(forkCommit) == "" {
		return LocalCommitList{}, ErrNoForkPoint
	}
	head, _, err := e.run(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return LocalCommitList{}, fmt.Errorf("git rev-parse HEAD 失败: %v", err)
	}
	// --no-show-signature：配置了 log.showSignature 时 git 会先打印验签文本，
	// 把字段切错（官方同样禁掉）。--topo-order --reverse：父在子前。
	format := "--format=%H%x00%P%x00%an%x00%aI%x00%s%x1e"
	out, stderr, err := e.run(ctx, root, "log", "--no-show-signature", "--topo-order", "--reverse",
		fmt.Sprintf("--max-count=%d", limit+1), format, forkCommit+"..HEAD", "--")
	if err != nil {
		return LocalCommitList{}, fmt.Errorf("git log 失败: %v: %s", err, strings.TrimSpace(stderr))
	}
	commits := parseLogRecords(out)
	truncated := len(commits) > limit
	if truncated {
		commits = commits[len(commits)-limit:]
	}
	if commits == nil {
		commits = []LocalCommit{}
	}
	return LocalCommitList{HeadSHA: strings.TrimSpace(head), Commits: commits, Truncated: truncated}, nil
}

// parseLogRecords 解析 `%H%x00%P%x00%an%x00%aI%x00%s%x1e` 的 log 输出。
func parseLogRecords(out string) []LocalCommit {
	commits := []LocalCommit{}
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimPrefix(record, "\n")
		if strings.TrimSpace(record) == "" {
			continue
		}
		fields := strings.Split(record, "\x00")
		if len(fields) != 5 {
			// 官方同样"字段数不对就当没有"：宁可少一条，不编造字段。
			continue
		}
		commit := LocalCommit{SHA: fields[0], Title: fields[4], AuthoredAt: fields[3]}
		if firstParent := strings.Fields(fields[1]); len(firstParent) > 0 {
			parent := firstParent[0]
			commit.Parent = &parent
		}
		if author := fields[2]; author != "" {
			commit.Author = &author
		}
		commits = append(commits, commit)
	}
	return commits
}

// shortCommit 是官方 `baseCommit.slice(0, 7)`。
func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) <= 7 {
		return commit
	}
	return commit[:7]
}

// samePath 比较两个绝对路径（Windows 上大小写不敏感、分隔符可能不同）。
func samePath(a, b string) bool {
	ca := canonicalizePath(a)
	cb := canonicalizePath(b)
	if ca == "" || cb == "" {
		return false
	}
	return strings.EqualFold(ca, cb)
}

// canonicalizePath 把路径统一成 filepath.Clean 之后的可比较形式。
func canonicalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = strings.ReplaceAll(path, "\\", "/")
	return strings.TrimSuffix(path, "/")
}
