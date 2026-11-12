package fssearch

// 本文件是 workspaceFiles.search 的核心实现（官方 index.js:189-232 的等价物）。
//
// 官方流程（逐行对照）：
//
//	index.js:196  const needle = spec.query.toLowerCase();
//	index.js:197  const byPath = spec.query.includes("/");
//	index.js:199  const entries = new Map();
//	index.js:200  const admit = (path, type) => { name = 末段;
//	                if ((byPath ? path : name).toLowerCase().includes(needle)) entries.set(path, …) }
//	index.js:204  callback: if (listed === maxEntries) return false;   // 先停，不 admit
//	index.js:205  admit(path, "file");                                  // 只列文件
//	index.js:206-210  逐层 admit(祖先, "directory")
//	index.js:227-229  sort by (depth, path)
//	index.js:231-231  matches.slice(0, maxResults); truncated = len > maxResults
//	index.js:232      incomplete = stopped || exitCode === 2
//
// 本文件不读任何用户级配置；忽略规则见 ignore.go。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// EntryType 是官方 `WorkspaceDirectoryEntry.type` 的取值。
//
// 注意：这条能力只会产出 TypeFile / TypeDirectory（见 doc.go 差异 7）。
type EntryType string

const (
	// TypeFile 是官方 `type:"file"`：rg --files 列出的普通文件。
	TypeFile EntryType = "file"
	// TypeDirectory 是官方 `type:"directory"`：由命中/列出的文件反推的祖先目录。
	TypeDirectory EntryType = "directory"
	// TypeOther 是官方枚举里的第三项。本能力不产出它，保留常量只为让
	// transport 层能逐个核对枚举，不靠字符串字面量。
	TypeOther EntryType = "other"
)

// Match 是一条命中：`/` 连接的 workspace 相对路径 + 条目类型。
type Match struct {
	// Path 是 `/` 连接的 workspace 相对路径，永不以 `/` 开头、永不为空。
	Path string
	// Type 取 TypeFile / TypeDirectory。
	Type EntryType
}

// Request 是官方 `WorkspaceFileSearchRequest`（四项 required）。
type Request struct {
	// Query 是大小写不敏感子串；去空白后不能为空。
	Query string
	// ExcludedPaths 是**字面量**排除（不是 glob 模式）。
	ExcludedPaths []string
	// IncludeHidden 为 true 时列出隐藏项（`.` 前缀）；VCS 元数据仍排除。
	IncludeHidden bool
	// RespectGitignore 为 true 时应用 .gitignore/.ignore/.git/info/exclude；
	// false 等价 rg `--no-ignore`。
	RespectGitignore bool
}

// Config 是部署上限，对应官方 `workspaceFiles.Config.search`（index.js:504-510）。
type Config struct {
	// MaxResults 是结果上限，官方默认 512（search.maxResults）。
	MaxResults int
	// MaxEntries 是枚举文件数上限，官方默认 1e5（search.maxEntries）。
	MaxEntries int
	// Timeout 是整次搜索的时长上限，官方默认 30s（search.timeoutMs）。
	Timeout time.Duration
	// GitGlobalExcludesFile 是**可选的**全局 git 排除文件（对应 git 的
	// core.excludesFile / `~/.config/git/ignore`）。空串表示不读全局排除
	// （与官方 rg 的差异 5，见 doc.go）。
	GitGlobalExcludesFile string
}

// DefaultConfig 是官方这条能力的默认上限（index.js:504-510）。
func DefaultConfig() Config {
	return Config{
		MaxResults: 512,
		MaxEntries: 100_000,
		Timeout:    30 * time.Second,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.MaxResults <= 0 {
		c.MaxResults = d.MaxResults
	}
	if c.MaxEntries <= 0 {
		c.MaxEntries = d.MaxEntries
	}
	if c.Timeout <= 0 {
		c.Timeout = d.Timeout
	}
	return c
}

// Result 是官方 `WorkspaceFileSearchResult`。
type Result struct {
	// Matches 已按「深度、再路径」排序，并被 MaxResults 截断。
	Matches []Match
	// Truncated 表示扫描完有界文件集后命中数超过 MaxResults。
	Truncated bool
	// Incomplete 表示文件数超过 MaxEntries 被提前终止（官方 rg exit 2 的等价物），
	// 或枚举中遇到不可读目录（rg exit 2 partial discovery）。
	Incomplete bool
}

var (
	// ErrBlankQuery 是官方 `gateway/bad-request`："query must not be blank"。
	ErrBlankQuery = errors.New("fssearch: query 去空白后为空（官方拒绝空搜索词）")
	// ErrInvalidRoot 表示工作区根不是"绝对、无 `..` 元素"的目录路径。
	ErrInvalidRoot = errors.New("fssearch: 工作区根必须是绝对路径且不含 `..` 元素")
	// ErrRootNotFound 对应官方 `workspace-file/not-found`。
	ErrRootNotFound = errors.New("fssearch: 工作区根不存在")
	// ErrRootNotDirectory 对应官方 `workspace-file/not-directory`。
	ErrRootNotDirectory = errors.New("fssearch: 工作区根不是目录")
	// ErrTimeout 对应官方 `workspace-file/search-failed`（超时分支）。
	ErrTimeout = errors.New("fssearch: 搜索超时（对应官方 search.timeoutMs）")
)

// Search 用官方默认上限跑一次搜索（多数调用方的入口）。
func Search(ctx context.Context, root string, req Request) (Result, error) {
	return SearchWithConfig(ctx, root, req, DefaultConfig())
}

// SearchWithConfig 是带部署上限的入口；root 必须是绝对、已清理的目录路径。
func SearchWithConfig(ctx context.Context, root string, req Request, cfg Config) (Result, error) {
	cfg = cfg.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}
	query := trimQuery(req.Query)
	if query == "" {
		return Result{}, ErrBlankQuery
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	cleanRoot, err := validateRoot(root)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(cleanRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, fmt.Errorf("%w: %q", ErrRootNotFound, root)
		}
		return Result{}, fmt.Errorf("fssearch: stat 工作区根 %q: %w", root, err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("%w: %q", ErrRootNotDirectory, root)
	}

	w := &walker{
		cfg:           cfg,
		query:         strings.ToLower(query),
		byPath:        strings.Contains(query, "/"),
		exclusions:    newLiteralExclusions(req.ExcludedPaths),
		includeHidden: req.IncludeHidden,
		entries:       make(map[string]EntryType),
	}
	if w.cfg.Timeout > 0 {
		w.deadline = time.Now().Add(w.cfg.Timeout)
	}
	if req.RespectGitignore {
		w.git = newIgnoreMatcher(cleanRoot, cfg.GitGlobalExcludesFile)
	}
	if err := w.run(ctx, cleanRoot); err != nil {
		return Result{}, err
	}
	return w.result(), nil
}

// trimQuery 复刻 JS `String.prototype.trim()`：去掉 Unicode WhiteSpace 与
// LineTerminator（含 `\uFEFF` / `\u3000`，这两项 Go 的 TrimSpace 处理不同）。
func trimQuery(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ',
			0x00A0,         // NBSP
			0x1680,         // OGHAM SPACE MARK
			0x2028, 0x2029, // LINE/PARAGRAPH SEPARATOR
			0x202F, // NARROW NO-BREAK SPACE
			0x205F, // MEDIUM MATHEMATICAL SPACE
			0x3000, // IDEOGRAPHIC SPACE
			0xFEFF: // ZERO WIDTH NO-BREAK SPACE / BOM
			return true
		}
		return r >= 0x2000 && r <= 0x200A // EN QUAD … HAIR SPACE
	})
}

// validateRoot 要求工作区根是绝对路径、且不含 `..` 元素（不静默清理掉越界写法）：
// 清理后才等价的写法（尾部分隔符）可以接受，含 `..` 的写法一律拒绝，因为
// "搜索到底搜的是哪个目录"必须一眼可见。
func validateRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("%w: 空路径", ErrInvalidRoot)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: %q 不是绝对路径", ErrInvalidRoot, root)
	}
	for _, el := range splitPathElements(root) {
		if el == ".." {
			return "", fmt.Errorf("%w: %q 含 `..` 元素", ErrInvalidRoot, root)
		}
	}
	clean := filepath.Clean(root)
	if clean == "." || clean == "" {
		return "", fmt.Errorf("%w: %q 清理后不是目录路径", ErrInvalidRoot, root)
	}
	return clean, nil
}

// splitPathElements 按 `/` 与 `\` 切分（Windows 两种都可能是用户输入）。
func splitPathElements(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
}

// literalExclusions 是官方 `exclusion()` 的 Go 等价物：把 excludedPaths 当**字面量**。
//
// 官方把每个值逐字符转义后交给 rg 当 glob：不含 `/` 的值成为"任意深度同名"的
// 名字 glob；含 `/` 的值成为"从工作区根锚定"的精确路径 glob。转义之后名字里的
// `* ? [ ] { } !` 都不再有模式含义——本实现直接按字符串相等比较，等价。
type literalExclusions struct {
	names map[string]struct{}
	paths map[string]struct{}
}

func newLiteralExclusions(list []string) literalExclusions {
	e := literalExclusions{names: map[string]struct{}{}, paths: map[string]struct{}{}}
	for _, raw := range list {
		if raw == "" {
			continue
		}
		// 官方判定是 `path.includes("/")`（看**原始**字符串），所以 `/build/`
		// 仍走"精确路径"分支，而不会被归一成"任意深度同名 build"。
		if strings.Contains(raw, "/") {
			p := strings.Trim(raw, "/")
			if p == "" || p == "." {
				continue
			}
			e.paths[p] = struct{}{}
			continue
		}
		e.names[raw] = struct{}{}
	}
	return e
}

func (e literalExclusions) excluded(rel, name string) bool {
	if _, ok := e.names[name]; ok {
		return true
	}
	_, ok := e.paths[rel]
	return ok
}

// vcsNames 是官方恒排除的 VCS 元数据名（index.js:171-173），与开关无关。
var vcsNames = map[string]struct{}{
	".git": {}, ".svn": {}, ".hg": {}, ".bzr": {}, ".jj": {}, ".sl": {},
}

func isVCSName(name string) bool {
	_, ok := vcsNames[name]
	return ok
}

// walker 承载一次搜索的全部状态。
type walker struct {
	cfg           Config
	query         string // 已转小写
	byPath        bool
	exclusions    literalExclusions
	includeHidden bool
	git           *ignoreMatcher // nil = respectGitignore:false
	deadline      time.Time
	entries       map[string]EntryType
	listed        int
	stopped       bool // 撞到 MaxEntries：官方 `stopped`
	incomplete    bool // 枚举期读失败：官方 rg exit 2 的等价物
}

// run 从工作区根开始遍历。
func (w *walker) run(ctx context.Context, rootAbs string) error {
	var scopes []ignoreScope
	if w.git != nil {
		scopes = append(scopes, w.git.baseScopes...)
		scopes = append(scopes, w.git.dirScopes(rootAbs)...)
	}
	return w.walkDir(ctx, rootAbs, "", scopes)
}

// walkDir 递归一个目录；relDir 是 workspace 相对目录（根为 ""）。
//
// 子项按"路径序"产出：目录的排序键补一个 `/`，于是 DFS 输出与归并后的
// 全局路径字节序一致（`a.txt` < `a/x` < `a0`），对应官方 rg `--sort=path`
// 的输出顺序——`maxEntries` 的截断点因此与官方相同。
func (w *walker) walkDir(ctx context.Context, absDir, relDir string, scopes []ignoreScope) error {
	if err := w.checkBudget(ctx); err != nil {
		return err
	}
	children, err := os.ReadDir(absDir)
	if err != nil {
		// 读不了的目录：官方是 rg exit 2（部分发现）→ incomplete:true，
		// 不静默当成"这个目录真的没有文件"。
		w.incomplete = true
		return nil
	}
	sorted := make([]dirChild, 0, len(children))
	for _, e := range children {
		key := e.Name()
		if e.IsDir() {
			key += "/"
		}
		sorted = append(sorted, dirChild{key: key, entry: e})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].key < sorted[j].key })

	for _, c := range sorted {
		if err := w.checkBudget(ctx); err != nil {
			return err
		}
		name := c.entry.Name()
		rel := name
		if relDir != "" {
			rel = relDir + "/" + name
		}
		isDir := c.entry.IsDir()
		if isVCSName(name) {
			continue
		}
		if w.exclusions.excluded(rel, name) {
			continue
		}
		if !w.includeHidden && strings.HasPrefix(name, ".") {
			continue
		}
		abs := filepath.Join(absDir, name)
		if w.git != nil && w.git.ignored(scopes, abs, isDir) {
			continue
		}
		if isDir {
			childScopes := scopes
			if w.git != nil {
				if extra := w.git.dirScopes(abs); len(extra) > 0 {
					childScopes = make([]ignoreScope, 0, len(scopes)+len(extra))
					childScopes = append(childScopes, scopes...)
					childScopes = append(childScopes, extra...)
				}
			}
			if err := w.walkDir(ctx, abs, rel, childScopes); err != nil {
				return err
			}
			continue
		}
		// 非普通文件（符号链接/设备…）：官方 "symlinks ... absent"，`--files` 不列。
		if !c.entry.Type().IsRegular() {
			continue
		}
		if w.listed == w.cfg.MaxEntries {
			// 官方 index.js:204：到上限时**先**返回 false 停止，不再 admit 这一条。
			w.stopped = true
			return nil
		}
		w.admit(rel, TypeFile)
		for _, ancestor := range ancestors(rel) {
			w.admit(ancestor, TypeDirectory)
		}
		w.listed++
	}
	return nil
}

type dirChild struct {
	key   string
	entry os.DirEntry
}

// admit 是官方 `admit`（index.js:200-203）：按名字或路径做大小写不敏感子串匹配。
func (w *walker) admit(rel string, t EntryType) {
	haystack := rel
	if !w.byPath {
		haystack = rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			haystack = rel[i+1:]
		}
	}
	if strings.Contains(strings.ToLower(haystack), w.query) {
		w.entries[rel] = t
	}
}

// ancestors 返回 rel 的所有祖先目录（由深到浅），与官方 :206-210 同序不同序皆可（map）。
func ancestors(rel string) []string {
	var out []string
	for {
		i := strings.LastIndex(rel, "/")
		if i < 0 {
			return out
		}
		rel = rel[:i]
		out = append(out, rel)
	}
}

func (w *walker) checkBudget(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !w.deadline.IsZero() && !time.Now().Before(w.deadline) {
		return ErrTimeout
	}
	return nil
}

// result 组装结果：排序（深度→路径）→ 截断 → 两个标志。
func (w *walker) result() Result {
	matches := make([]Match, 0, len(w.entries))
	for p, t := range w.entries {
		matches = append(matches, Match{Path: p, Type: t})
	}
	sort.Slice(matches, func(i, j int) bool {
		di, dj := pathDepth(matches[i].Path), pathDepth(matches[j].Path)
		if di != dj {
			return di < dj
		}
		return lessUTF16(matches[i].Path, matches[j].Path)
	})
	truncated := len(matches) > w.cfg.MaxResults
	if truncated {
		matches = matches[:w.cfg.MaxResults]
	}
	return Result{
		Matches:    matches,
		Truncated:  truncated,
		Incomplete: w.stopped || w.incomplete,
	}
}

// pathDepth 是官方 `path.split("/").length`。
func pathDepth(p string) int {
	d := 1
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			d++
		}
	}
	return d
}

// lessUTF16 按 UTF-16 code unit 序比较，与官方排序比较器里的 JS `<` 一致。
//
// 为什么不直接用 Go 的字符串 `<`：Go 按 UTF-8 字节序，补充平面字符（UTF-16 里
// 是代理对）与 U+E000..U+FFFF 的相对顺序和 UTF-16 不同，会让含 emoji 的路径
// 排在官方结果的不同位置。
func lessUTF16(a, b string) bool {
	for a != "" && b != "" {
		ra, sa := utf8.DecodeRuneInString(a)
		rb, sb := utf8.DecodeRuneInString(b)
		ua, na := utf16Units(ra)
		ub, nb := utf16Units(rb)
		n := na
		if nb < n {
			n = nb
		}
		for k := 0; k < n; k++ {
			if ua[k] != ub[k] {
				return ua[k] < ub[k]
			}
		}
		// 有效 UTF-8 路径里，代理范围不会出现在单 code unit 的字符上，
		// 所以逐单元相等之后继续比下一个字符即可。
		a, b = a[sa:], b[sb:]
	}
	return a == "" && b != ""
}

// utf16Units 把一个 rune 展开成 1..2 个 UTF-16 code unit。
func utf16Units(r rune) ([2]uint16, int) {
	if r < 0x10000 {
		return [2]uint16{uint16(r), 0}, 1
	}
	r -= 0x10000
	return [2]uint16{uint16(0xD800 + (r >> 10)), uint16(0xDC00 + (r & 0x3FF))}, 2
}
