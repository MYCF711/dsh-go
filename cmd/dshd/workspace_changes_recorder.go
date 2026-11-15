package main

// `dshd run` 的一轮里"这一轮改了哪些文件"的记录器。
//
// 落点（与官方 `dsh-workspace-changes` 的 TurnRecorder 同语义）：
//   - `turn/start` 之后：定位仓库、拍**本轮开始**的工作树快照（私有对象库）；
//   - 这一轮收工时（`turn/end` 之后）：再拍一张，`git diff-tree --numstat` 求差异，
//     把 `{turn}` 追加成一条 `workspace/changes` 事件，并把摘要+快照身份写进
//     `<home>/state/workspace-changes/<sessionId>/records/<seq>.json`。
//
// 为什么快照在**开始**就拍：工具调用随时会改文件，收工后再拍就只剩"改完之后"，
// 没有 before 就没法给出 diff（GUI 的 deliverables 卡片正需要它）。
//
// 失败策略：没有 git / 不是仓库 / 快照失败 / 没有任何变化 → 不写事件、不写记录，
// 失败时向 stderr 说清原因（`dshd run` 的 stderr 归 serve 的轮次日志）。
// 绝不写一条"空摘要"冒充成功：GUI 会据此画一张空卡片。
//
// 本仓的已知裁剪（登记在报告里，不是静默省略）：官方在**非 git** 工作目录里靠
// "文件工具改动前后各存一份拷贝"兜底；本仓还没有那条工具级捕获，所以非 git 会话
// 没有 changes 记录（路由对这些 seq 回 404，不伪造）。git 仓库里的覆盖是完整的。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
)

// workspaceChangesTurn 是一次 turn 的记录器；repo == nil 表示这一轮没有快照。
type workspaceChangesTurn struct {
	home       string
	sessionID  string
	turn       int
	cwd        string
	logw       *session.Log
	gitPath    string
	repo       *workspaceChangesRepo
	objectsDir string
	excludes   []string
	beforeTree string
	finished   bool
}

// workspaceChangesDisabled 是显式关闭开关（排查/性能问题时用；默认开启）。
func workspaceChangesDisabled() bool {
	return os.Getenv("DSHD_WORKSPACE_CHANGES") == "0"
}

// startWorkspaceChangesTurn 在 `turn/start` 之后调用；返回的记录器总是非 nil
// （`finish` 对"没快照"的轮次是空操作）。
func startWorkspaceChangesTurn(home string, logw *session.Log, cwd string, turn int) *workspaceChangesTurn {
	state := &workspaceChangesTurn{home: home, cwd: cwd, turn: turn, logw: logw}
	if workspaceChangesDisabled() || logw == nil {
		return state
	}
	header, err := v4.ReadHeader(logw.Path)
	if err != nil {
		workspaceChangesWarn("workspace-changes：读不到会话 header（%v），这一轮不记录变更", err)
		return state
	}
	// 子代理会话不进变更摘要（官方 eligible()：origin=subagent 或 delegationDepth>0）。
	if header.DelegationDepth > 0 {
		return state
	}
	state.sessionID = header.ID
	// 工作目录以**会话 header**为准（官方 `session.header.cwd` 是这一个会话的
	// 工作区）；传进来的进程 cwd 只是 header 缺失时的兜底。两者不一致时，以
	// header 为准才与"这个会话的文件都相对谁"一致。
	workDir := strings.TrimSpace(header.Cwd)
	if workDir == "" {
		workDir = cwd
	}
	// cwd 与仓库路径统一成同一种拼法，否则 rel 计算会输出去不掉的 `..\..`。
	cwd = workspaceChangesCanonical(workDir)
	state.cwd = cwd
	gitPath, err := workspaceChangesGitPath()
	if err != nil {
		workspaceChangesWarn("workspace-changes：%v；这一轮不记录变更", err)
		return state
	}
	repo, err := locateWorkspaceChangesRepo(context.Background(), gitPath, cwd)
	if err != nil {
		workspaceChangesWarn("workspace-changes：定位仓库失败（%v）；这一轮不记录变更", err)
		return state
	}
	if repo == nil {
		// 非 git 工作目录：本仓还没有官方的"文件工具捕获"兜底，如实不记录。
		return state
	}
	state.gitPath = gitPath
	state.repo = repo
	state.objectsDir = filepath.Join(home, "state", "workspace-changes", header.ID, "objects")
	if err := os.MkdirAll(state.objectsDir, 0o700); err != nil {
		workspaceChangesWarn("workspace-changes：建私有对象库失败（%v）；这一轮不记录变更", err)
		state.repo = nil
		return state
	}
	state.excludes = workspaceChangesExcludes(repo.root, filepath.Dir(state.objectsDir))
	tree, err := snapshotTree(context.Background(), gitPath, *repo, state.objectsDir, state.excludes)
	if err != nil {
		workspaceChangesWarn("workspace-changes：本轮开始快照失败（%v）；这一轮不记录变更", err)
		state.repo = nil
		return state
	}
	state.beforeTree = tree
	return state
}

// workspaceChangesExcludes 给出"私有目录落在仓库工作树里"时要排除的路径。
func workspaceChangesExcludes(root, privateDir string) []string {
	if !workspacePathInside(root, privateDir) {
		return nil
	}
	rel, err := filepath.Rel(root, privateDir)
	if err != nil || rel == "." || rel == "" {
		return nil
	}
	return []string{filepath.ToSlash(rel)}
}

// finish 在 `turn/end` 之后调用：算差异、写事件、写记录。幂等（第二次是空操作）。
func (t *workspaceChangesTurn) finish() {
	if t == nil || t.repo == nil || t.finished {
		return
	}
	t.finished = true
	ctx := context.Background()
	afterTree, err := snapshotTree(ctx, t.gitPath, *t.repo, t.objectsDir, t.excludes)
	if err != nil {
		workspaceChangesWarn("workspace-changes：本轮结束快照失败（%v）；这一轮不写变更", err)
		return
	}
	if afterTree == t.beforeTree {
		// 两次快照一样 = 这一轮没改任何被跟踪/未跟踪的文件：不写事件（官方同判据）。
		return
	}
	env := []string{
		"GIT_OBJECT_DIRECTORY=" + t.objectsDir,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + t.repo.objects,
	}
	out, code, err := workspaceChangesGit(ctx, t.gitPath, t.repo.root, env,
		"diff-tree", "-r", "-M", "-z", "--numstat", t.beforeTree, afterTree)
	if err != nil {
		workspaceChangesWarn("workspace-changes：求差异失败（%v）；这一轮不写变更", err)
		return
	}
	if code != 0 {
		workspaceChangesWarn("workspace-changes：git diff-tree 退出码 %d；这一轮不写变更", code)
		return
	}
	entries, err := parseWorkspaceChangesNumstat(out)
	if err != nil {
		workspaceChangesWarn("workspace-changes：解析 numstat 失败（%v）；这一轮不写变更", err)
		return
	}
	if len(entries) == 0 {
		return
	}
	userHome, _ := os.UserHomeDir()
	files := make([]workspaceChangedFileRecord, 0, len(entries))
	totalAdded, totalDeleted := 0, 0
	for _, entry := range entries {
		absolute := workspaceChangesCanonical(filepath.Join(t.repo.root, filepath.FromSlash(entry.path)))
		files = append(files, workspaceChangedFileRecord{
			Path:    workspaceChangesDurablePath(absolute, t.cwd),
			Display: workspaceChangesDisplayPath(absolute, t.cwd, t.repo.root, userHome),
			Added:   entry.added, Deleted: entry.deleted, Binary: entry.binary,
			OldPath: entry.oldPath,
		})
		totalAdded += entry.added
		totalDeleted += entry.deleted
	}
	sortWorkspaceChangedFiles(files)
	total := len(files)
	if len(files) > workspaceChangesMaxFiles {
		files = files[:workspaceChangesMaxFiles]
	}

	seq := t.logw.Seq()
	if err := t.logw.Append("workspace/changes", map[string]any{"turn": t.turn}); err != nil {
		workspaceChangesWarn("workspace-changes：追加 workspace/changes 事件失败（%v）；这一轮不写变更", err)
		return
	}
	record := workspaceChangesRecord{
		Version: workspaceChangesVersion, SessionID: t.sessionID, Seq: seq, Turn: t.turn,
		Cwd: t.cwd, RepoRoot: t.repo.root, GitDir: t.repo.gitDir, RepoObjects: t.repo.objects,
		ObjectsDir: t.objectsDir, BeforeTree: t.beforeTree, AfterTree: afterTree,
		Files: files, Total: total, Added: totalAdded, Deleted: totalDeleted,
	}
	if err := writeWorkspaceChangesRecord(t.home, record); err != nil {
		workspaceChangesWarn("workspace-changes：写记录失败（%v）；seq=%d 的摘要将不可用", err, seq)
	}
}

// workspaceChangesWarn 把记录失败说给 stderr（serve 会把它收进这一轮的日志）。
func workspaceChangesWarn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
