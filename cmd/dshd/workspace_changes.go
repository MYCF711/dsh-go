package main

// 官方 `workspace/changes` 的宿主侧记录与三条路由的后端。
//
// 为什么记录要落盘：serve 的每一轮**就是一个 `dshd run` 子进程**
// （cmd/dshd/serve_tools.go 的 loopRunner），子进程退出后内存就没了，而
// `/api/changes.summary|diff` 由父进程 serve 回答。官方把记录留在宿主内存里
// （Session 活着期间），本仓没有"父进程持有子进程对象"这条路，所以记录与快照
// 对象都落在 `<home>/state/workspace-changes/<sessionId>/`：
//
//	records/<seq>.json   一条 workspace/changes 事件的摘要 + 快照身份
//	objects/             这一会话的私有 git 对象库（快照树与 blob）
//
// 路径安全：sessionId 直接进目录名，所以只接受 `[A-Za-z0-9._-]`（一年份的
// serveID 就是 uuid 形状）。不合形状一律当"没有记录"，不做任何拼接。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"dshgo/internal/transport"
)

const (
	// workspaceChangesVersion 是记录文档格式版本；别的版本当损坏，不猜。
	workspaceChangesVersion = 1
	// workspaceChangesMaxFiles 是官方 `config.maxFiles` 的默认值（500）：
	// `total/added/deleted` 仍统计全量，只有 `files` 被截。
	workspaceChangesMaxFiles = 500
	// workspaceChangesMaxFileBytes 是官方 `config.maxFileBytes`（2 MiB）。
	workspaceChangesMaxFileBytes = 2 << 20
	// workspaceChangesSessionPattern 是能出现在目录名里的会话 id 形状。
	workspaceChangesSessionPattern = `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`
)

var workspaceChangesSessionRE = regexp.MustCompile(workspaceChangesSessionPattern)

// workspaceChangedFileRecord 是摘要里的一行（`path/display` 两种拼法都留着：
// 客户端按 display 排序与显示、用 path 打开文件）。
type workspaceChangedFileRecord struct {
	Path      string `json:"path"`
	Display   string `json:"display"`
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Binary    bool   `json:"binary,omitempty"`
	Oversized bool   `json:"oversized,omitempty"`
	// OldPath 是重命名前的路径（git numstat 的 rename 记录）；比较的 before 侧用它。
	OldPath string `json:"oldPath,omitempty"`
}

// workspaceChangesRecord 是一条 `workspace/changes` 事件的宿主侧记录。
type workspaceChangesRecord struct {
	Version   int    `json:"version"`
	SessionID string `json:"sessionId"`
	Seq       int64  `json:"seq"`
	Turn      int    `json:"turn"`
	Cwd       string `json:"cwd"`
	RepoRoot  string `json:"repoRoot"`
	GitDir    string `json:"gitDir"`
	// RepoObjects 是仓库自己的对象路径（GIT_ALTERNATE_OBJECT_DIRECTORIES 的取值）。
	RepoObjects string `json:"repoObjects"`
	// ObjectsDir 是本会话私有对象库（快照树与 blob 全在这里）。
	ObjectsDir string                       `json:"objectsDir"`
	BeforeTree string                       `json:"beforeTree"`
	AfterTree  string                       `json:"afterTree"`
	Files      []workspaceChangedFileRecord `json:"files"`
	Total      int                          `json:"total"`
	Added      int                          `json:"added"`
	Deleted    int                          `json:"deleted"`
}

// workspaceChangesStore 是三条路由共用的只读后端（每次请求重新读盘：
// 写入者是与这个进程不同的 `dshd run` 子进程）。
type workspaceChangesStore struct{ home string }

func newWorkspaceChangesStore(home string) *workspaceChangesStore {
	return &workspaceChangesStore{home: home}
}

// sessionDir 是某个会话的记录目录（sessionId 形状不合法时返回 err）。
func (s *workspaceChangesStore) sessionDir(sessionID string) (string, error) {
	if !workspaceChangesSessionRE.MatchString(sessionID) {
		return "", fmt.Errorf("会话 id 形状非法，拒绝拼进记录目录")
	}
	return filepath.Join(s.home, "state", "workspace-changes", sessionID), nil
}

// load 读一条记录；found=false 表示这个 seq 没有记录（路由回 404）。
func (s *workspaceChangesStore) load(sessionID string, seq int64) (*workspaceChangesRecord, bool, error) {
	dir, err := s.sessionDir(sessionID)
	if err != nil {
		return nil, false, nil // 形状非法 = 没有这个会话的记录，不是 500
	}
	path := filepath.Join(dir, "records", fmt.Sprintf("%d.json", seq))
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("读取 workspace/changes 记录 %s: %w", path, err)
	}
	var record workspaceChangesRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, false, fmt.Errorf("workspace/changes 记录 %s 损坏: %w", path, err)
	}
	if record.Version != workspaceChangesVersion {
		return nil, false, fmt.Errorf("workspace/changes 记录 %s 版本 %d，本宿主只认 %d",
			path, record.Version, workspaceChangesVersion)
	}
	if record.SessionID != sessionID || record.Seq != seq {
		return nil, false, fmt.Errorf("workspace/changes 记录 %s 的身份与请求不符", path)
	}
	return &record, true, nil
}

// Summary 实现 transport.ChangesAPI 的摘要支。
func (s *workspaceChangesStore) Summary(sessionID string, seq int64) (transport.ChangesSummary, bool, error) {
	record, found, err := s.load(sessionID, seq)
	if err != nil || !found {
		return transport.ChangesSummary{}, found, err
	}
	files := make([]transport.ChangesSummaryFile, 0, len(record.Files))
	for _, file := range record.Files {
		files = append(files, transport.ChangesSummaryFile{
			Path: file.Path, Display: file.Display, Added: file.Added, Deleted: file.Deleted,
			Binary: file.Binary, Oversized: file.Oversized,
		})
	}
	summary := transport.ChangesSummary{
		Turn: record.Turn, Files: files, Total: record.Total, Added: record.Added, Deleted: record.Deleted,
		Cwd: record.Cwd,
	}
	if record.BeforeTree != "" && record.AfterTree != "" {
		summary.Snapshot = &transport.ChangesSnapshot{Before: record.BeforeTree, After: record.AfterTree}
	}
	return summary, true, nil
}

// OpenPath 解析摘要里第 index 个文件当前的绝对路径（打开/定位用）。
func (s *workspaceChangesStore) OpenPath(sessionID string, seq int64, index int) (string, bool, error) {
	record, found, err := s.load(sessionID, seq)
	if err != nil || !found {
		return "", found, err
	}
	if index < 0 || index >= len(record.Files) {
		return "", false, nil
	}
	file := record.Files[index]
	// path 相对 cwd（工作区内）或绝对（工作区外）；两种都由记录真实产生。
	if filepath.IsAbs(file.Path) {
		return filepath.Clean(file.Path), true, nil
	}
	return filepath.Clean(filepath.Join(record.Cwd, filepath.FromSlash(file.Path))), true, nil
}

// writeWorkspaceChangesRecord 落一条记录（原子替换：先写临时文件再 rename）。
func writeWorkspaceChangesRecord(home string, record workspaceChangesRecord) error {
	dir := filepath.Join(home, "state", "workspace-changes", record.SessionID, "records")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("建记录目录 %s: %w", dir, err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("序列化记录: %w", err)
	}
	target := filepath.Join(dir, fmt.Sprintf("%d.json", record.Seq))
	staging := target + ".tmp"
	if err := os.WriteFile(staging, raw, 0o600); err != nil {
		return fmt.Errorf("写记录 %s: %w", staging, err)
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.Remove(staging)
		return fmt.Errorf("提交记录 %s: %w", target, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 路径展示（官方 paths.ts 的三条规则）
// ---------------------------------------------------------------------------

// workspacePathInside 判断 path 是否等于 root 或在其下（两侧都先 Clean）。
func workspacePathInside(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." && !filepath.IsAbs(rel))
}

// workspaceChangesDisplayPath 复刻官方 displayPathOf：工作区/仓库内 → 相对 cwd；
// home 下 → `~/…`；其余 → 绝对路径。统一 `/` 分隔。
func workspaceChangesDisplayPath(abs, cwd, root, home string) string {
	if workspacePathInside(cwd, abs) || workspacePathInside(root, abs) {
		rel, err := filepath.Rel(cwd, abs)
		if err == nil {
			return filepath.ToSlash(rel)
		}
	}
	if home != "" && workspacePathInside(home, abs) {
		rel, err := filepath.Rel(home, abs)
		if err == nil {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(abs)
}

// workspaceChangesDurablePath 复刻官方 durablePathOf：工作区内相对 cwd，其余绝对。
func workspaceChangesDurablePath(abs, cwd string) string {
	if workspacePathInside(cwd, abs) {
		if rel, err := filepath.Rel(cwd, abs); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(abs)
}

// sortWorkspaceChangedFiles 按 display 的码位顺序排（官方 compareDisplay）。
func sortWorkspaceChangedFiles(files []workspaceChangedFileRecord) {
	sort.SliceStable(files, func(i, j int) bool { return files[i].Display < files[j].Display })
}
