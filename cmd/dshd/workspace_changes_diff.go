package main

// `/api/changes.diff` 的后端：从这一轮的快照树里取两侧内容并算出统一 diff。
//
// 官方语义（`dsh-workspace-changes/src/recorder.ts:211-238`）：
//   - binary / oversized 两侧**不读正文**，直接回拒绝支（kind=binary/oversized）；
//   - 文本两侧回 `{kind:'text', path, display, before, after, hunks, coarse}`，
//     `before/after` 说的是"这一侧在快照里存不存在"（新文件 before=false）。
//
// 实现差异（已登记，不是静默省略）：官方用 JS 的行比较，超过 `diffTimeoutMs`
// （默认 100ms）就把整文件当成"全部替换"（coarse=true）。本仓用 git 自己的
// `--no-index` 统一 diff（同一份 unified 形状、3 行上下文），进程上限 30s；
// 因此 `coarse` 恒为 false——没有"JS 行比较超时"这个事件可报，不假装报过。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"dshgo/internal/transport"
)

var workspaceChangesHunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
var workspaceChangesLsTree = regexp.MustCompile(`^\d+ (\S+) ([0-9a-f]+)\s+(\d+)\t`)

// Diff 实现 transport.ChangesAPI 的比较支。
func (s *workspaceChangesStore) Diff(sessionID string, seq int64, index int) (any, bool, error) {
	record, found, err := s.load(sessionID, seq)
	if err != nil || !found {
		return nil, found, err
	}
	if index < 0 || index >= len(record.Files) {
		return nil, false, nil
	}
	file := record.Files[index]
	if file.Binary {
		return transport.ChangesRefusalDiff{Kind: "binary", Path: file.Path, Display: file.Display}, true, nil
	}
	if file.Oversized {
		return transport.ChangesRefusalDiff{Kind: "oversized", Path: file.Path, Display: file.Display}, true, nil
	}
	gitPath, err := workspaceChangesGitPath()
	if err != nil {
		return nil, true, err
	}
	ctx := context.Background()
	env := workspaceChangesRecordEnv(record)

	beforePath := file.OldPath
	if beforePath == "" {
		beforePath = file.Path
	}
	before, beforeExists, err := readWorkspaceChangesSnapshotSide(ctx, gitPath, record, env, record.BeforeTree, beforePath)
	if err != nil {
		return nil, true, err
	}
	after, afterExists, err := readWorkspaceChangesSnapshotSide(ctx, gitPath, record, env, record.AfterTree, file.Path)
	if err != nil {
		return nil, true, err
	}
	if before.oversized || after.oversized {
		return transport.ChangesRefusalDiff{Kind: "oversized", Path: file.Path, Display: file.Display}, true, nil
	}
	if before.binary || after.binary {
		return transport.ChangesRefusalDiff{Kind: "binary", Path: file.Path, Display: file.Display}, true, nil
	}

	hunks, coarse, err := workspaceChangesUnifiedHunks(gitPath, *record, before.text, after.text)
	if err != nil {
		return nil, true, err
	}
	if coarse {
		// git 自己把内容判成二进制（numstat 没报）：按官方"不供正文"的支拒绝，
		// 而不是返回一份没有 hunk 的 text（那会显示成"文件没变"）。
		return transport.ChangesRefusalDiff{Kind: "binary", Path: file.Path, Display: file.Display}, true, nil
	}
	return transport.ChangesTextDiff{
		Kind: "text", Path: file.Path, Display: file.Display,
		Before: beforeExists, After: afterExists, Hunks: hunks, Coarse: false,
	}, true, nil
}

// workspaceChangesSnapshotSide 是一侧快照内容的读取结果。
type workspaceChangesSnapshotSide struct {
	text      string
	oversized bool
	binary    bool
}

// readWorkspaceChangesSnapshotSide 读一侧的 blob；树里没有这个路径就是空内容
// （存在与否由调用方用第二个返回值表达）。
func readWorkspaceChangesSnapshotSide(ctx context.Context, gitPath string, record *workspaceChangesRecord, env []string, tree, path string) (workspaceChangesSnapshotSide, bool, error) {
	if tree == "" || path == "" {
		return workspaceChangesSnapshotSide{}, false, nil
	}
	oid, size, exists, err := workspaceChangesTreeBlob(ctx, gitPath, record, env, tree, path)
	if err != nil || !exists {
		return workspaceChangesSnapshotSide{}, false, err
	}
	if size > workspaceChangesMaxFileBytes {
		return workspaceChangesSnapshotSide{oversized: true}, true, nil
	}
	out, code, err := workspaceChangesGit(ctx, gitPath, record.RepoRoot,
		append(env, "GIT_LITERAL_PATHSPECS=1"), "cat-file", "blob", oid)
	if err != nil {
		return workspaceChangesSnapshotSide{}, true, err
	}
	if code != 0 {
		return workspaceChangesSnapshotSide{}, true, fmt.Errorf("git cat-file blob %s 退出码 %d", oid, code)
	}
	return workspaceChangesSnapshotSide{text: out, binary: strings.ContainsRune(out, 0)}, true, nil
}

// workspaceChangesTreeBlob 取一棵树里某个路径的 blob id 与大小。
func workspaceChangesTreeBlob(ctx context.Context, gitPath string, record *workspaceChangesRecord, env []string, tree, path string) (string, int64, bool, error) {
	out, code, err := workspaceChangesGit(ctx, gitPath, record.RepoRoot,
		append(env, "GIT_LITERAL_PATHSPECS=1"), "ls-tree", "-z", "-l", tree, "--", path)
	if err != nil {
		return "", 0, false, err
	}
	if code != 0 {
		return "", 0, false, fmt.Errorf("git ls-tree %s -- %s 退出码 %d", tree, path, code)
	}
	entry := out
	if idx := strings.IndexByte(out, 0); idx >= 0 {
		entry = out[:idx]
	}
	match := workspaceChangesLsTree.FindStringSubmatch(entry)
	if match == nil || match[1] != "blob" {
		// 路径不存在、或者那里是 tree/gitlink：都不供正文。
		return "", 0, false, nil
	}
	size, err := strconv.ParseInt(match[3], 10, 64)
	if err != nil {
		return "", 0, false, fmt.Errorf("ls-tree size 字段非法 %q", match[3])
	}
	return match[2], size, true, nil
}

// workspaceChangesUnifiedHunks 用 git 的 `--no-index --unified=3` 算统一 diff。
//
// coarse=true 表示 git 把某一侧当成二进制（输出 "Binary files … differ"）：
// 调用方据此回 binary 拒绝支。
func workspaceChangesUnifiedHunks(gitPath string, record workspaceChangesRecord, before, after string) ([]transport.ChangesHunk, bool, error) {
	dir, err := os.MkdirTemp(filepath.Join(record.ObjectsDir, ".."), "diff-")
	if err != nil {
		return nil, false, fmt.Errorf("建 diff 临时目录: %w", err)
	}
	defer os.RemoveAll(dir)
	beforeFile := filepath.Join(dir, "before")
	afterFile := filepath.Join(dir, "after")
	if err := os.WriteFile(beforeFile, []byte(before), 0o600); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(afterFile, []byte(after), 0o600); err != nil {
		return nil, false, err
	}
	out, code, err := workspaceChangesGit(context.Background(), gitPath, record.RepoRoot, nil,
		"diff", "--no-index", "--no-color", "--unified=3", "--", beforeFile, afterFile)
	if err != nil {
		return nil, false, err
	}
	// `--no-index` 的退出码：0 = 相同，1 = 有差异，>1 = 真失败。
	if code != 0 && code != 1 {
		return nil, false, fmt.Errorf("git diff --no-index 退出码 %d", code)
	}
	if strings.Contains(out, "Binary files ") && strings.Contains(out, " differ") {
		return nil, true, nil
	}
	return parseWorkspaceChangesUnifiedHunks(out), false, nil
}

// parseWorkspaceChangesUnifiedHunks 把统一 diff 正文解析成官方形状的 hunk。
//
// hunk 头缺省行数按 1 计（`@@ -1 +1 @@`）；`\ No newline at end of file` 不是
// 内容行、官方结构里也没有它，跳过；头之前的 `diff --git`/`index`/`---`/`+++`
// 全部忽略。
func parseWorkspaceChangesUnifiedHunks(out string) []transport.ChangesHunk {
	hunks := []transport.ChangesHunk{}
	var current *transport.ChangesHunk
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			if current != nil {
				hunks = append(hunks, *current)
			}
			match := workspaceChangesHunkHeader.FindStringSubmatch(line)
			if match == nil {
				current = nil
				continue
			}
			current = &transport.ChangesHunk{
				OldStart: atoiOr(match[1], 0), OldLines: atoiOr(match[2], 1),
				NewStart: atoiOr(match[3], 0), NewLines: atoiOr(match[4], 1),
				Lines: []string{},
			}
			continue
		}
		if current == nil || line == "" {
			continue
		}
		switch line[0] {
		case ' ', '+', '-':
			current.Lines = append(current.Lines, line)
		default:
			// `\ No newline…` 与其它元信息都不是内容行。
		}
	}
	if current != nil {
		hunks = append(hunks, *current)
	}
	for i := range hunks {
		if hunks[i].Lines == nil {
			hunks[i].Lines = []string{}
		}
	}
	return hunks
}

func atoiOr(text string, fallback int) int {
	if text == "" {
		return fallback
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}
	return value
}

// workspaceChangesRecordEnv 是读这个会话快照对象需要的 git 环境。
func workspaceChangesRecordEnv(record *workspaceChangesRecord) []string {
	env := []string{"GIT_OBJECT_DIRECTORY=" + record.ObjectsDir}
	if record.RepoObjects != "" {
		env = append(env, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+record.RepoObjects)
	}
	return env
}
